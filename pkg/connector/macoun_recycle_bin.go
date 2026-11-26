// macoun-matrix - A Matrix-iMessage puppeting bridge (fork of corten-matrix).
// SPDX-License-Identifier: MPL-2.0
//
// macoun: a local, incrementally maintained copy of Apple's recycle bin.
//
// The deleted-chat seed (seedDeletedChatsFromRecycleBin) decides which chats
// the user deleted by looking at what sits in the two recycle-bin zones. The
// zones are only readable as a change feed, 200 changes per request. Reading
// the feed from its start on every bridge start has two costs:
//
//   - time: an account with a long feed pays for every page again on every
//     start, while incoming messages wait for the seed;
//   - correctness: any fixed page limit cuts the feed off at its oldest part.
//     Deletions recorded after the cut are invisible, and a chat whose
//     tombstone is known locally then looks "restored", because only a few of
//     its older messages appear in the truncated list. The seed undeletes such
//     a chat and its history is bridged again.
//
// So the feed is read once, each record is stored here, and the feed position
// (CloudKit's continuation token) is stored with it in the same transaction.
// Later starts ask only for changes after that position: usually one request
// returning nothing. Records that leave the recycle bin (restored by the user
// or purged by Apple) arrive as tombstones in the feed and are removed from the
// copy, so the copy always equals what a full read would return.
//
// Tables (created on first use, never altered; an older build ignores them):
//
//	macoun_recycle_bin_zone(login_id, zone, continuation_token, complete,
//	                        failures, updated_ts)
//	macoun_recycle_bin_record(login_id, zone, record_name, kind, payload)
//
// kind is "m" for a recoverable message (payload: the "guid|base64-metadata"
// entry the seed consumes) or "c" for a chat record (payload: JSON of
// rustpushgo.WrappedCloudSyncChat). Neither holds message text.

package connector

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/rs/zerolog"

	"github.com/lrhodin/corten-matrix/pkg/rustpushgo"
)

// recycleBinZones are the CloudKit zones that make up the recycle bin.
var recycleBinZones = []string{"recoverableMessageDeleteZone", "chatBotRecoverableMessageDeleteZone"}

const (
	recycleBinKindMessage = "m"
	recycleBinKindChat    = "c"

	// recycleBinMaxPagesPerPass bounds one pass over one zone. It is a guard
	// against a feed that never reports its end, not a tuning knob: a pass that
	// stops here is reported as incomplete and the next pass continues from
	// the stored position.
	recycleBinMaxPagesPerPass = 4096

	// recycleBinTokenFailureLimit is how many passes in a row may fail while a
	// stored position exists before the position and the stored records of
	// that zone are dropped and the feed is read from its start again. CloudKit
	// does not say in a documented way that a continuation token is no longer
	// accepted, so repeated failure stands in for that answer; a single failure
	// is far more likely to be the network or the anisette provider.
	recycleBinTokenFailureLimit = 3

	recycleBinProgressEvery = 50
)

// recoverableZonePager is the part of the rustpush client the recycle-bin copy
// needs: one page of a zone's change feed per call.
type recoverableZonePager interface {
	SyncRecoverableZone(zoneName string, continuationToken *string) (rustpushgo.WrappedRecoverableZonePage, error)
}

// recycleBinSnapshot is the recycle bin as the local copy has it.
type recycleBinSnapshot struct {
	Chats   []rustpushgo.WrappedCloudSyncChat
	Entries []string // "guid|base64-metadata", one per recoverable message
	// Complete is true when this pass read every zone to the end of its feed,
	// so Chats and Entries are the whole recycle bin as of now. When false the
	// copy may lack recent deletions and must not be used as evidence that
	// something is absent from the recycle bin.
	Complete  bool
	PagesRead int
}

func (s *cloudBackfillStore) ensureRecycleBinSchema(ctx context.Context) error {
	for _, query := range []string{
		`CREATE TABLE IF NOT EXISTS macoun_recycle_bin_zone (
			login_id TEXT NOT NULL,
			zone TEXT NOT NULL,
			continuation_token TEXT,
			complete BOOLEAN NOT NULL DEFAULT FALSE,
			failures INTEGER NOT NULL DEFAULT 0,
			updated_ts BIGINT NOT NULL,
			PRIMARY KEY (login_id, zone)
		)`,
		`CREATE TABLE IF NOT EXISTS macoun_recycle_bin_record (
			login_id TEXT NOT NULL,
			zone TEXT NOT NULL,
			record_name TEXT NOT NULL,
			kind TEXT NOT NULL,
			payload TEXT NOT NULL,
			PRIMARY KEY (login_id, zone, record_name)
		)`,
	} {
		if _, err := s.db.Exec(ctx, query); err != nil {
			return fmt.Errorf("failed to ensure recycle-bin schema: %w", err)
		}
	}
	return nil
}

func (s *cloudBackfillStore) recycleBinZoneState(ctx context.Context, zone string) (token *string, failures int, err error) {
	var tok sql.NullString
	err = s.db.QueryRow(ctx,
		`SELECT continuation_token, failures FROM macoun_recycle_bin_zone WHERE login_id=$1 AND zone=$2`,
		s.loginID, zone,
	).Scan(&tok, &failures)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	if tok.Valid && tok.String != "" {
		token = &tok.String
	}
	return token, failures, nil
}

// applyRecycleBinPage stores one page of a zone's feed and the feed position
// after it in a single transaction, so a crash can never leave a position
// stored without the records before it.
func (s *cloudBackfillStore) applyRecycleBinPage(ctx context.Context, zone string, page rustpushgo.WrappedRecoverableZonePage) error {
	if len(page.MessageEntries) != len(page.MessageRecordNames) {
		return fmt.Errorf("recycle-bin page for %s has %d message entries but %d record names",
			zone, len(page.MessageEntries), len(page.MessageRecordNames))
	}
	return s.db.DoTxn(ctx, nil, func(ctx context.Context) error {
		upsert := func(recordName, kind, payload string) error {
			_, err := s.db.Exec(ctx, `
				INSERT INTO macoun_recycle_bin_record (login_id, zone, record_name, kind, payload)
				VALUES ($1, $2, $3, $4, $5)
				ON CONFLICT (login_id, zone, record_name) DO UPDATE SET
					kind=excluded.kind, payload=excluded.payload
			`, s.loginID, zone, recordName, kind, payload)
			return err
		}
		for i, entry := range page.MessageEntries {
			if entry == "" || page.MessageRecordNames[i] == "" {
				continue
			}
			if err := upsert(page.MessageRecordNames[i], recycleBinKindMessage, entry); err != nil {
				return err
			}
		}
		for _, chat := range page.Chats {
			if chat.RecordName == "" {
				continue
			}
			payload, err := json.Marshal(chat)
			if err != nil {
				return err
			}
			if err = upsert(chat.RecordName, recycleBinKindChat, string(payload)); err != nil {
				return err
			}
		}
		for _, recordName := range page.RemovedRecordNames {
			if _, err := s.db.Exec(ctx,
				`DELETE FROM macoun_recycle_bin_record WHERE login_id=$1 AND zone=$2 AND record_name=$3`,
				s.loginID, zone, recordName,
			); err != nil {
				return err
			}
		}
		// An empty token means "nothing new, keep your position"; COALESCE
		// keeps the stored one. complete only ever moves to true here.
		_, err := s.db.Exec(ctx, `
			INSERT INTO macoun_recycle_bin_zone (login_id, zone, continuation_token, complete, failures, updated_ts)
			VALUES ($1, $2, $3, $4, 0, $5)
			ON CONFLICT (login_id, zone) DO UPDATE SET
				continuation_token=COALESCE(excluded.continuation_token, macoun_recycle_bin_zone.continuation_token),
				complete=(macoun_recycle_bin_zone.complete OR excluded.complete),
				failures=0,
				updated_ts=excluded.updated_ts
		`, s.loginID, zone, page.ContinuationToken, page.Done, time.Now().UnixMilli())
		return err
	})
}

// noteRecycleBinFailure counts a failed pass over a zone. Once
// recycleBinTokenFailureLimit passes in a row have failed with a stored
// position, the position and the zone's records are dropped so the next pass
// reads the feed from its start. It reports whether that reset happened.
func (s *cloudBackfillStore) noteRecycleBinFailure(ctx context.Context, zone string, hadToken bool) (reset bool, err error) {
	err = s.db.DoTxn(ctx, nil, func(ctx context.Context) error {
		if _, err := s.db.Exec(ctx, `
			INSERT INTO macoun_recycle_bin_zone (login_id, zone, continuation_token, complete, failures, updated_ts)
			VALUES ($1, $2, NULL, FALSE, 1, $3)
			ON CONFLICT (login_id, zone) DO UPDATE SET
				failures=macoun_recycle_bin_zone.failures+1, updated_ts=excluded.updated_ts
		`, s.loginID, zone, time.Now().UnixMilli()); err != nil {
			return err
		}
		var failures int
		if err := s.db.QueryRow(ctx,
			`SELECT failures FROM macoun_recycle_bin_zone WHERE login_id=$1 AND zone=$2`,
			s.loginID, zone,
		).Scan(&failures); err != nil {
			return err
		}
		if !hadToken || failures < recycleBinTokenFailureLimit {
			return nil
		}
		reset = true
		if _, err := s.db.Exec(ctx,
			`DELETE FROM macoun_recycle_bin_record WHERE login_id=$1 AND zone=$2`, s.loginID, zone,
		); err != nil {
			return err
		}
		_, err := s.db.Exec(ctx, `
			UPDATE macoun_recycle_bin_zone
			SET continuation_token=NULL, complete=FALSE, failures=0
			WHERE login_id=$1 AND zone=$2
		`, s.loginID, zone)
		return err
	})
	return reset, err
}

// loadRecycleBinRecords returns the stored recycle bin: chat records and
// recoverable message entries across all zones.
func (s *cloudBackfillStore) loadRecycleBinRecords(ctx context.Context) (chats []rustpushgo.WrappedCloudSyncChat, entries []string, err error) {
	rows, err := s.db.Query(ctx,
		`SELECT kind, payload FROM macoun_recycle_bin_record WHERE login_id=$1 ORDER BY zone, record_name`,
		s.loginID,
	)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	seenGUID := make(map[string]bool)
	for rows.Next() {
		var kind, payload string
		if err = rows.Scan(&kind, &payload); err != nil {
			return nil, nil, err
		}
		switch kind {
		case recycleBinKindMessage:
			// The same message can sit in the bin under two record names;
			// consumers expect each GUID once.
			guid := payload
			for i := 0; i < len(payload); i++ {
				if payload[i] == '|' {
					guid = payload[:i]
					break
				}
			}
			if guid == "" || seenGUID[guid] {
				continue
			}
			seenGUID[guid] = true
			entries = append(entries, payload)
		case recycleBinKindChat:
			var chat rustpushgo.WrappedCloudSyncChat
			if jsonErr := json.Unmarshal([]byte(payload), &chat); jsonErr != nil {
				return nil, nil, fmt.Errorf("stored recycle-bin chat record is not valid JSON: %w", jsonErr)
			}
			chats = append(chats, chat)
		}
	}
	return chats, entries, rows.Err()
}

// syncRecycleBin brings the local copy of the recycle bin up to date and
// returns it. Each zone is read from its stored feed position to the end of
// the feed; every page is committed before the next is requested, so an
// interrupted pass resumes where it stopped.
//
// A zone that cannot be read leaves the snapshot incomplete (Complete=false)
// but still returns what is stored: enough to recognise known deletions, not
// enough to conclude that anything has left the recycle bin.
func (s *cloudBackfillStore) syncRecycleBin(ctx context.Context, pager recoverableZonePager, log zerolog.Logger) (recycleBinSnapshot, error) {
	// One pass at a time: the seed and a restore command may ask together.
	s.recycleBinMu.Lock()
	defer s.recycleBinMu.Unlock()
	var snap recycleBinSnapshot
	if err := s.ensureRecycleBinSchema(ctx); err != nil {
		return snap, err
	}
	snap.Complete = true
	for _, zone := range recycleBinZones {
		token, _, err := s.recycleBinZoneState(ctx, zone)
		if err != nil {
			return snap, err
		}
		hadToken := token != nil
		if !hadToken {
			log.Info().Str("zone", zone).
				Msg("DELETE-SEED: no stored position for this recycle-bin zone; reading its whole change feed once (later starts read only new changes)")
		}
		zoneDone := false
		zonePages := 0
		for zonePages < recycleBinMaxPagesPerPass {
			if ctx.Err() != nil {
				break
			}
			page, pageErr := pager.SyncRecoverableZone(zone, token)
			if pageErr != nil {
				reset, noteErr := s.noteRecycleBinFailure(ctx, zone, hadToken)
				if noteErr != nil {
					log.Warn().Err(noteErr).Str("zone", zone).Msg("DELETE-SEED: failed to record recycle-bin read failure")
				}
				log.Warn().Err(pageErr).Str("zone", zone).Int("pages_read", zonePages).Bool("had_position", hadToken).
					Bool("position_reset", reset).
					Msg("DELETE-SEED: failed to read recycle-bin changes; using the stored copy, which may lack recent deletions")
				break
			}
			if err = s.applyRecycleBinPage(ctx, zone, page); err != nil {
				return snap, err
			}
			zonePages++
			snap.PagesRead++
			if page.Done {
				zoneDone = true
				break
			}
			if page.ContinuationToken == nil || (token != nil && *page.ContinuationToken == *token) {
				// More is promised but the position did not move; asking
				// again would return the same page forever.
				log.Warn().Str("zone", zone).Int("pages_read", zonePages).
					Msg("DELETE-SEED: recycle-bin feed did not advance; stopping this pass")
				break
			}
			token = page.ContinuationToken
			if zonePages%recycleBinProgressEvery == 0 {
				log.Info().Str("zone", zone).Int("pages_read", zonePages).
					Msg("DELETE-SEED: reading recycle-bin change feed")
			}
		}
		if !zoneDone {
			snap.Complete = false
		}
	}
	chats, entries, err := s.loadRecycleBinRecords(ctx)
	if err != nil {
		return snap, err
	}
	snap.Chats = chats
	snap.Entries = entries
	return snap, nil
}

// recycleBinMayAutoRecover reports whether the seed may undo a local chat
// deletion because the recycle bin no longer shows the chat as deleted.
//
// "No longer shows" is an argument from absence: the chat's newest messages
// are not in the recycle bin. That only means something when the whole recycle
// bin was read. With an incomplete snapshot the deletion may simply sit in the
// unread part, and undoing the tombstone would bridge a deleted chat's history
// back into Matrix.
//
// It also means nothing for a chat deleted with delete-portal
// (locallyDeleted): that deletion never reached Apple, so the recycle bin was
// never going to show it.
func recycleBinMayAutoRecover(state recycleBinPortalState, snapshotComplete, locallyDeleted bool) bool {
	return snapshotComplete && !locallyDeleted && state.NeedsUndelete()
}

// recycleBinNow brings the local recycle-bin copy up to date and returns it,
// for the paths outside the startup seed (restore-chat, chat recovery, the
// debug dump). An incomplete read is returned as it is, with a log line: these
// callers look for what is in the recycle bin, and a partial list still finds
// most of it.
func (c *IMClient) recycleBinNow(ctx context.Context) (recycleBinSnapshot, error) {
	if c.client == nil || c.cloudStore == nil {
		return recycleBinSnapshot{}, errors.New("CloudKit client or cloud store unavailable")
	}
	log := c.UserLogin.Log.With().Str("component", "cloud_sync").Logger()
	snapshot, err := c.cloudStore.syncRecycleBin(ctx, c.client, log)
	if err == nil && !snapshot.Complete {
		log.Warn().Int("recoverable_chats", len(snapshot.Chats)).Int("recoverable_guids", len(snapshot.Entries)).
			Msg("Recycle-bin copy could not be brought fully up to date; using what is stored")
	}
	return snapshot, err
}

// recoverableChats returns the chat records in Apple's recycle bin.
func (c *IMClient) recoverableChats(ctx context.Context) ([]rustpushgo.WrappedCloudSyncChat, error) {
	snapshot, err := c.recycleBinNow(ctx)
	return snapshot.Chats, err
}

// recoverableMessageEntries returns the "guid|base64-metadata" entries of the
// messages in Apple's recycle bin.
func (c *IMClient) recoverableMessageEntries(ctx context.Context) ([]string, error) {
	snapshot, err := c.recycleBinNow(ctx)
	return snapshot.Entries, err
}
