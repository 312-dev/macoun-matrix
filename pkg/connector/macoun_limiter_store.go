// macoun-matrix - A Matrix-iMessage puppeting bridge (fork of corten-matrix).
// SPDX-License-Identifier: MPL-2.0
//
// macoun: persisted, encrypted state for the outbound limiter (idskeys.go).
//
// Without persistence a crash loop or a deliberate restart would reset the
// new-conversation budget. macoun keeps the new-recipient state in its own
// table in the bridge database:
//
//	macoun_outbound_limiter(login_id TEXT PRIMARY KEY, state BLOB (BYTEA on Postgres), updated_at BIGINT)
//
// The table is created on first use and no existing table is touched, so an
// older binary on the same database simply ignores it. state is AES-256-GCM
// with a random nonce (nonce || ciphertext), the login ID as associated data,
// and a 32-byte key in macoun-limiter.key (mode 0600) next to session.json.
// The plaintext is JSON {"v":1,"admitted":{"<portal id>":<unix ms>}}: which
// chats were first contacts and when, which is the only sensitive part (it
// says who was messaged for the first time). Anything that fails to decrypt or
// parse is ignored with a warning and replaced on the next save; the limiter
// then starts empty.

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
	"os"
	"path/filepath"
	"time"

	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

const limiterKeyFile = "macoun-limiter.key"

type limiterStore struct {
	db      *dbutil.Database
	key     []byte
	loginID string
}

type limiterState struct {
	V        int              `json:"v"`
	Admitted map[string]int64 `json:"admitted"`
}

// loadOrCreateLimiterKey reads the key file in the bridge state directory,
// creating it with a fresh random key when it does not exist.
func loadOrCreateLimiterKey() ([]byte, error) {
	sp, err := sessionFilePath()
	if err != nil {
		return nil, err
	}
	return loadOrCreateKeyAt(filepath.Join(filepath.Dir(sp), limiterKeyFile))
}

func loadOrCreateKeyAt(path string) ([]byte, error) {
	if b, err := os.ReadFile(path); err == nil {
		if len(b) != 32 {
			return nil, fmt.Errorf("%s has %d bytes, want 32", path, len(b))
		}
		return b, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return loadOrCreateKeyAt(path) // lost a race with another creator
	} else if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Write(key); err != nil {
		return nil, err
	}
	return key, f.Sync()
}

func (s *limiterStore) ensureTable(ctx context.Context) error {
	blobType := "BLOB"
	if s.db.Dialect == dbutil.Postgres {
		blobType = "BYTEA"
	}
	_, err := s.db.Exec(ctx, `CREATE TABLE IF NOT EXISTS macoun_outbound_limiter (
		login_id   TEXT PRIMARY KEY,
		state      `+blobType+` NOT NULL,
		updated_at BIGINT NOT NULL
	)`)
	return err
}

func (s *limiterStore) aead() (cipher.AEAD, error) {
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// load returns the persisted admissions. A missing row is not an error; a row
// that fails to decrypt or parse is reported so the caller can log and ignore it.
func (s *limiterStore) load(ctx context.Context) (map[networkid.PortalID]time.Time, error) {
	if err := s.ensureTable(ctx); err != nil {
		return nil, err
	}
	var blob []byte
	err := s.db.QueryRow(ctx, `SELECT state FROM macoun_outbound_limiter WHERE login_id=$1`, s.loginID).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	gcm, err := s.aead()
	if err != nil {
		return nil, err
	}
	if len(blob) < gcm.NonceSize() {
		return nil, errors.New("limiter state too short")
	}
	plain, err := gcm.Open(nil, blob[:gcm.NonceSize()], blob[gcm.NonceSize():], []byte(s.loginID))
	if err != nil {
		return nil, fmt.Errorf("limiter state does not decrypt: %w", err)
	}
	var st limiterState
	if err := json.Unmarshal(plain, &st); err != nil {
		return nil, fmt.Errorf("limiter state does not parse: %w", err)
	}
	if st.V != 1 {
		return nil, fmt.Errorf("limiter state version %d not supported", st.V)
	}
	out := make(map[networkid.PortalID]time.Time, len(st.Admitted))
	for p, ms := range st.Admitted {
		out[networkid.PortalID(p)] = time.UnixMilli(ms)
	}
	return out, nil
}

func (s *limiterStore) save(ctx context.Context, admitted map[networkid.PortalID]time.Time) error {
	if err := s.ensureTable(ctx); err != nil {
		return err
	}
	st := limiterState{V: 1, Admitted: make(map[string]int64, len(admitted))}
	for p, at := range admitted {
		st.Admitted[string(p)] = at.UnixMilli()
	}
	plain, err := json.Marshal(st)
	if err != nil {
		return err
	}
	gcm, err := s.aead()
	if err != nil {
		return err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return err
	}
	blob := gcm.Seal(nonce, nonce, plain, []byte(s.loginID))
	_, err = s.db.Exec(ctx, `INSERT INTO macoun_outbound_limiter (login_id, state, updated_at) VALUES ($1, $2, $3)
		ON CONFLICT (login_id) DO UPDATE SET state=excluded.state, updated_at=excluded.updated_at`,
		s.loginID, blob, time.Now().UnixMilli())
	return err
}
