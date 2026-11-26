// macoun-matrix - A Matrix-iMessage puppeting bridge (fork of corten-matrix).
// SPDX-License-Identifier: MPL-2.0
//
// macoun: a persisted index of the CloudKit attachment zone.
//
// Every CloudKit sync pass needs two things from attachmentManateeZone before
// it may sync messages or download anything:
//
//   - the attachment map (GUID -> record name, MIME type, size, ...) that
//     message ingestion uses to attach metadata to the messages it imports;
//   - the Ford key of every video record, registered in the Ford key caches
//     (ford_cache.go and the Rust side), because MMCS may serve a blob that
//     only another record's key decrypts.
//
// Both are built from the zone's change feed, and both used to live only in
// memory, so every pass, including the one that holds incoming messages at
// startup, read the whole zone again: 200 records per request, tens of
// thousands of records on a long-lived account.
//
// The index stores each record of the feed (encrypted) together with the feed
// position after it. A pass loads the index, registers the Ford keys from it,
// and asks CloudKit only for changes after the stored position. A pass that
// stops half way leaves a consistent prefix behind and the next one continues
// from there.
//
// Tables (created on first use, never altered; an older build ignores them and
// keeps re-reading the zone, which stays correct):
//
//	macoun_attachment_index(login_id, record_name, seq, sealed)
//	macoun_attachment_index_state(login_id, continuation_token, failures, updated_ts)
//
// sealed is AES-256-GCM (nonce || ciphertext, associated data
// "<login id>\x00<record name>") over JSON of attachmentIndexEntry, which
// includes the Ford keys and the filename. The 32-byte key is in
// macoun-attachment-index.key (mode 0600) next to session.json. A row that
// does not decrypt makes the whole index unusable for that pass: it is dropped
// and the zone is read from the start, as without an index.
//
// Records deleted from the zone are not removed from the index: the Rust page
// type does not report them. A stale entry costs a map entry and a cached key
// that nothing asks for.

package connector

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/dbutil"

	"github.com/lrhodin/corten-matrix/pkg/rustpushgo"
)

const (
	attachmentIndexKeyFile = "macoun-attachment-index.key"

	// attachmentIndexFailureLimit: after this many passes in a row that could
	// not read a single page from the stored position, the index is dropped
	// and the zone is read from its start. Stands in for "CloudKit no longer
	// accepts this continuation token", which has no documented signal.
	attachmentIndexFailureLimit = 3
)

// attachmentIndexEntry is one attachment record as the index stores it.
type attachmentIndexEntry struct {
	Row         cloudAttachmentRow `json:"row"`
	FordKey     []byte             `json:"ford_key,omitempty"`
	AvidFordKey []byte             `json:"avid_ford_key,omitempty"`
}

func attachmentIndexEntryFromInfo(att rustpushgo.WrappedCloudAttachmentInfo) attachmentIndexEntry {
	entry := attachmentIndexEntry{Row: cloudAttachmentRow{
		GUID:       att.Guid,
		MimeType:   ptrStringOr(att.MimeType, ""),
		UTIType:    ptrStringOr(att.UtiType, ""),
		Filename:   ptrStringOr(att.Filename, ""),
		FileSize:   att.FileSize,
		RecordName: att.RecordName,
		HasAvid:    att.HasAvid,
	}}
	if att.FordKey != nil {
		entry.FordKey = *att.FordKey
	}
	if att.AvidFordKey != nil {
		entry.AvidFordKey = *att.AvidFordKey
	}
	return entry
}

// attachmentIndex persists the attachment zone for one login.
type attachmentIndex struct {
	db      *dbutil.Database
	loginID string
	aead    cipher.AEAD
}

func newAttachmentIndex(db *dbutil.Database, loginID string, key []byte) (*attachmentIndex, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &attachmentIndex{db: db, loginID: loginID, aead: aead}, nil
}

// openAttachmentIndex opens the index with the key file in the bridge state
// directory, creating the key on first use.
func openAttachmentIndex(db *dbutil.Database, loginID string) (*attachmentIndex, error) {
	sp, err := sessionFilePath()
	if err != nil {
		return nil, err
	}
	key, err := loadOrCreateKeyAt(filepath.Join(filepath.Dir(sp), attachmentIndexKeyFile))
	if err != nil {
		return nil, err
	}
	return newAttachmentIndex(db, loginID, key)
}

func (x *attachmentIndex) ensureSchema(ctx context.Context) error {
	for _, query := range []string{
		`CREATE TABLE IF NOT EXISTS macoun_attachment_index (
			login_id TEXT NOT NULL,
			record_name TEXT NOT NULL,
			seq BIGINT NOT NULL,
			sealed ` + sqlBlobType(x.db) + ` NOT NULL,
			PRIMARY KEY (login_id, record_name)
		)`,
		`CREATE TABLE IF NOT EXISTS macoun_attachment_index_state (
			login_id TEXT PRIMARY KEY,
			continuation_token TEXT,
			failures INTEGER NOT NULL DEFAULT 0,
			updated_ts BIGINT NOT NULL
		)`,
	} {
		if _, err := x.db.Exec(ctx, query); err != nil {
			return fmt.Errorf("failed to ensure attachment index schema: %w", err)
		}
	}
	return nil
}

func (x *attachmentIndex) aad(recordName string) []byte {
	return []byte(x.loginID + "\x00" + recordName)
}

func (x *attachmentIndex) seal(entry attachmentIndexEntry) ([]byte, error) {
	plain, err := json.Marshal(entry)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, x.aead.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return x.aead.Seal(nonce, nonce, plain, x.aad(entry.Row.RecordName)), nil
}

func (x *attachmentIndex) open(recordName string, sealed []byte) (attachmentIndexEntry, error) {
	var entry attachmentIndexEntry
	if len(sealed) < x.aead.NonceSize() {
		return entry, errors.New("attachment index row too short")
	}
	plain, err := x.aead.Open(nil, sealed[:x.aead.NonceSize()], sealed[x.aead.NonceSize():], x.aad(recordName))
	if err != nil {
		return entry, fmt.Errorf("attachment index row does not decrypt: %w", err)
	}
	if err = json.Unmarshal(plain, &entry); err != nil {
		return entry, fmt.Errorf("attachment index row does not parse: %w", err)
	}
	return entry, nil
}

// load returns the stored entries in feed order and the feed position after
// them. A nil token means there is no usable index.
func (x *attachmentIndex) load(ctx context.Context) (entries []attachmentIndexEntry, token *string, err error) {
	var tok sql.NullString
	err = x.db.QueryRow(ctx,
		`SELECT continuation_token FROM macoun_attachment_index_state WHERE login_id=$1`, x.loginID,
	).Scan(&tok)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (!tok.Valid || tok.String == "")) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	rows, err := x.db.Query(ctx,
		`SELECT record_name, sealed FROM macoun_attachment_index WHERE login_id=$1 ORDER BY seq`, x.loginID,
	)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var recordName string
		var sealed []byte
		if err = rows.Scan(&recordName, &sealed); err != nil {
			return nil, nil, err
		}
		entry, openErr := x.open(recordName, sealed)
		if openErr != nil {
			return nil, nil, openErr
		}
		entries = append(entries, entry)
	}
	if err = rows.Err(); err != nil {
		return nil, nil, err
	}
	return entries, &tok.String, nil
}

// applyPage stores the records of one page and the feed position after it in
// one transaction. A nil token keeps the stored position.
func (x *attachmentIndex) applyPage(ctx context.Context, entries []attachmentIndexEntry, token *string) error {
	return x.db.DoTxn(ctx, nil, func(ctx context.Context) error {
		var seq sql.NullInt64
		if err := x.db.QueryRow(ctx,
			`SELECT MAX(seq) FROM macoun_attachment_index WHERE login_id=$1`, x.loginID,
		).Scan(&seq); err != nil {
			return err
		}
		next := seq.Int64
		for _, entry := range entries {
			if entry.Row.RecordName == "" {
				continue
			}
			sealed, err := x.seal(entry)
			if err != nil {
				return err
			}
			next++
			if _, err = x.db.Exec(ctx, `
				INSERT INTO macoun_attachment_index (login_id, record_name, seq, sealed)
				VALUES ($1, $2, $3, $4)
				ON CONFLICT (login_id, record_name) DO UPDATE SET seq=excluded.seq, sealed=excluded.sealed
			`, x.loginID, entry.Row.RecordName, next, sealed); err != nil {
				return err
			}
		}
		_, err := x.db.Exec(ctx, `
			INSERT INTO macoun_attachment_index_state (login_id, continuation_token, failures, updated_ts)
			VALUES ($1, $2, 0, $3)
			ON CONFLICT (login_id) DO UPDATE SET
				continuation_token=COALESCE(excluded.continuation_token, macoun_attachment_index_state.continuation_token),
				failures=0,
				updated_ts=excluded.updated_ts
		`, x.loginID, token, time.Now().UnixMilli())
		return err
	})
}

// reset drops the index so the next pass reads the zone from its start.
func (x *attachmentIndex) reset(ctx context.Context) error {
	return x.db.DoTxn(ctx, nil, func(ctx context.Context) error {
		if _, err := x.db.Exec(ctx, `DELETE FROM macoun_attachment_index WHERE login_id=$1`, x.loginID); err != nil {
			return err
		}
		_, err := x.db.Exec(ctx, `DELETE FROM macoun_attachment_index_state WHERE login_id=$1`, x.loginID)
		return err
	})
}

// noteFailure counts a pass that read nothing from the stored position and
// reports whether the limit was reached and the index was reset.
func (x *attachmentIndex) noteFailure(ctx context.Context) (reset bool, err error) {
	var failures int
	if _, err = x.db.Exec(ctx,
		`UPDATE macoun_attachment_index_state SET failures=failures+1 WHERE login_id=$1`, x.loginID,
	); err != nil {
		return false, err
	}
	err = x.db.QueryRow(ctx,
		`SELECT failures FROM macoun_attachment_index_state WHERE login_id=$1`, x.loginID,
	).Scan(&failures)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil || failures < attachmentIndexFailureLimit {
		return false, err
	}
	return true, x.reset(ctx)
}

// attachmentZoneSource is the CloudKit side of an attachment sync pass.
type attachmentZoneSource struct {
	// fetchPage returns one page of the zone's change feed from token.
	fetchPage func(token *string) (rustpushgo.WrappedCloudSyncAttachmentsPage, error)
	// fetchFallback queries the zone directly for records the change feed
	// missed; knownRecordNames are excluded from the answer.
	fetchFallback func(knownRecordNames []string) (rustpushgo.WrappedCloudSyncAttachmentsPage, error)
	// registerFordKey adds a key to the Ford key caches.
	registerFordKey func(key []byte)
	// retryDelay is the wait before retrying a failed page.
	retryDelay func(err error, consecutiveErrors int) time.Duration
}

// syncAttachmentZone builds the attachment map and fills the Ford key caches.
// With an index it starts from the stored records and feed position and reads
// only later changes; with a nil index, or one that cannot be used, it reads
// the whole zone. It returns the map and the last feed position seen.
func syncAttachmentZone(ctx context.Context, index *attachmentIndex, src attachmentZoneSource, log zerolog.Logger) (map[string]cloudAttachmentRow, *string) {
	attMap := make(map[string]cloudAttachmentRow)
	var token *string
	take := func(entry attachmentIndexEntry) {
		attMap[entry.Row.GUID] = entry.Row
		if len(entry.FordKey) > 0 {
			src.registerFordKey(entry.FordKey)
		}
		if len(entry.AvidFordKey) > 0 {
			src.registerFordKey(entry.AvidFordKey)
		}
	}

	if index != nil {
		if err := index.ensureSchema(ctx); err != nil {
			log.Warn().Err(err).Msg("Attachment index unavailable; reading the whole attachment zone")
			index = nil
		}
	}
	if index != nil {
		entries, stored, err := index.load(ctx)
		if err != nil {
			log.Warn().Err(err).Msg("Attachment index unreadable; dropping it and reading the whole attachment zone")
			if resetErr := index.reset(ctx); resetErr != nil {
				log.Warn().Err(resetErr).Msg("Failed to drop the attachment index; not using it in this pass")
				index = nil
			}
		} else if stored != nil {
			for _, entry := range entries {
				take(entry)
			}
			token = stored
			log.Info().Int("attachments", len(attMap)).
				Msg("Loaded attachment index; asking CloudKit only for attachment changes since the stored position")
		}
	}
	resumed := token != nil

	consecutiveErrors := 0
	pagesRead := 0
	const maxConsecutiveAttErrors = 3
	for page := 0; page < maxCloudSyncPages; page++ {
		if ctx.Err() != nil {
			break
		}
		resp, syncErr := src.fetchPage(token)
		if syncErr != nil {
			consecutiveErrors++
			retryDelay := src.retryDelay(syncErr, consecutiveErrors)
			log.Warn().Err(syncErr).
				Int("page", page).
				Int("imported_so_far", len(attMap)).
				Int("consecutive_errors", consecutiveErrors).
				Dur("retry_after", retryDelay).
				Msg("CloudKit attachment sync page failed (FFI error)")
			if consecutiveErrors >= maxConsecutiveAttErrors {
				log.Error().
					Int("page", page).
					Int("imported_so_far", len(attMap)).
					Msg("CloudKit attachment sync: too many consecutive FFI errors, stopping pagination")
				break
			}
			time.Sleep(retryDelay)
			continue
		}
		consecutiveErrors = 0
		pagesRead++

		entries := make([]attachmentIndexEntry, 0, len(resp.Attachments))
		for _, att := range resp.Attachments {
			entry := attachmentIndexEntryFromInfo(att)
			entries = append(entries, entry)
			take(entry)
		}
		if index != nil {
			if err := index.applyPage(ctx, entries, resp.ContinuationToken); err != nil {
				// The map built in this pass is still complete; only later
				// passes lose the shortcut.
				log.Warn().Err(err).Int("page", page).Msg("Failed to store attachment index page; dropping the index")
				if resetErr := index.reset(ctx); resetErr != nil {
					log.Warn().Err(resetErr).Msg("Failed to drop the attachment index")
				}
				index = nil
			}
		}

		prev := ptrStringOr(token, "")
		token = resp.ContinuationToken
		if resp.Done || (page > 0 && prev == ptrStringOr(token, "")) {
			break
		}
	}

	if index != nil && resumed && pagesRead == 0 {
		if reset, err := index.noteFailure(ctx); err != nil {
			log.Warn().Err(err).Msg("Failed to record attachment index read failure")
		} else if reset {
			log.Warn().Int("limit", attachmentIndexFailureLimit).
				Msg("Attachment zone could not be read from the stored position in several passes; dropped the index, the next pass reads the whole zone")
		}
	}

	// QueryRecords fallback: query attachmentManateeZone directly for records
	// the FetchRecordChanges feed missed. Pass all record_names already in attMap
	// so Rust only returns genuinely new records.
	knownNames := make([]string, 0, len(attMap))
	for _, row := range attMap {
		knownNames = append(knownNames, row.RecordName)
	}
	if fallback, fallbackErr := src.fetchFallback(knownNames); fallbackErr != nil {
		log.Warn().Err(fallbackErr).Msg("CloudKit attachment QueryRecords fallback failed")
	} else {
		entries := make([]attachmentIndexEntry, 0, len(fallback.Attachments))
		for _, att := range fallback.Attachments {
			entry := attachmentIndexEntryFromInfo(att)
			entries = append(entries, entry)
			take(entry)
		}
		if len(entries) > 0 {
			log.Info().Int("count", len(entries)).Msg("CloudKit attachment QueryRecords fallback added records")
			// Stored only next to an existing position: without one the next
			// pass reads the whole zone anyway and must not find a partial index.
			if index != nil && token != nil {
				if err := index.applyPage(ctx, entries, nil); err != nil {
					log.Warn().Err(err).Msg("Failed to store fallback attachment records in the index")
				}
			}
		}
	}

	log.Info().Int("pages_read", pagesRead).Bool("from_index", resumed).Int("attachments", len(attMap)).
		Msg("CloudKit attachment zone read")
	return attMap, token
}
