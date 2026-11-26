// macoun-matrix: tests for the outbound send limiter and its persisted state.
// SPDX-License-Identifier: MPL-2.0

package connector

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

type fakeClock struct{ t time.Time }

func (f *fakeClock) now() time.Time { return f.t }

func testLimiter(l outboundLimits) (*outboundLimiter, *fakeClock) {
	clk := &fakeClock{t: time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)}
	lim := newOutboundLimiter(l)
	lim.now = clk.now
	return lim, clk
}

func roomy() outboundLimits {
	return outboundLimits{perChat: 1000, perChatWindow: time.Minute, global: 1000, globalWindow: time.Hour, newChatsDay: 1000, newChatsWeek: 1000}
}

func TestOutboundLimiterPerChat(t *testing.T) {
	l := roomy()
	l.perChat = 3
	lim, clk := testLimiter(l)
	for i := 0; i < 3; i++ {
		if ok, why, _ := lim.decide("a", true); !ok {
			t.Fatalf("send %d refused: %s", i, why)
		}
	}
	if ok, _, _ := lim.decide("a", true); ok {
		t.Fatal("fourth send in a minute was allowed")
	}
	if ok, _, _ := lim.decide("b", true); !ok {
		t.Fatal("another chat was limited by chat a")
	}
	clk.t = clk.t.Add(61 * time.Second)
	if ok, why, _ := lim.decide("a", true); !ok {
		t.Fatalf("send after the window refused: %s", why)
	}
}

func TestOutboundLimiterGlobal(t *testing.T) {
	l := roomy()
	l.global = 2
	lim, clk := testLimiter(l)
	lim.decide("a", true)
	lim.decide("b", true)
	if ok, _, _ := lim.decide("c", true); ok {
		t.Fatal("global limit not applied")
	}
	clk.t = clk.t.Add(time.Hour + time.Second)
	if ok, _, _ := lim.decide("c", true); !ok {
		t.Fatal("global window did not expire")
	}
}

// The default new-recipient limits are 10 per 24
// hours and 64 per 7 days.
func TestOutboundLimiterNewChatsDefaults(t *testing.T) {
	if defaultOutboundLimits.newChatsDay != 10 || defaultOutboundLimits.newChatsWeek != 64 {
		t.Fatalf("new-chat limits %d/%d, want 10/64", defaultOutboundLimits.newChatsDay, defaultOutboundLimits.newChatsWeek)
	}
	lim, clk := testLimiter(defaultOutboundLimits)
	n := 0
	for day := 0; day < 7; day++ {
		for i := 0; i < 12; i++ {
			ok, _, isNew := lim.decide(networkid.PortalID(fmt.Sprintf("d%d-%d", day, i)), false)
			if ok != isNew {
				t.Fatalf("allowed=%v but newChat=%v", ok, isNew)
			}
			if ok {
				n++
			}
		}
		clk.t = clk.t.Add(24*time.Hour + time.Minute)
	}
	// 10 a day for 7 days would be 70; the weekly cap holds it at 64.
	if n != 64 {
		t.Fatalf("admitted %d new chats in a week, want 64", n)
	}
}

func TestOutboundLimiterNewChats(t *testing.T) {
	l := roomy()
	l.newChatsDay = 2
	lim, clk := testLimiter(l)
	if ok, _, isNew := lim.decide("n1", false); !ok || !isNew {
		t.Fatal("first new chat refused or not marked new")
	}
	lim.decide("n2", false)
	if ok, _, _ := lim.decide("n3", false); ok {
		t.Fatal("third new chat in a day allowed")
	}
	if ok, _, isNew := lim.decide("n1", false); !ok || isNew {
		t.Fatal("follow-up in an admitted chat refused or counted as new")
	}
	if ok, _, _ := lim.decide("old", true); !ok {
		t.Fatal("existing chat refused by the new-chat limit")
	}
	clk.t = clk.t.Add(25 * time.Hour)
	if ok, _, _ := lim.decide("n3", false); !ok {
		t.Fatal("daily new-chat window did not expire")
	}
}

func TestOutboundLimiterRefusalRecordsNothing(t *testing.T) {
	l := roomy()
	l.perChat, l.global = 1, 2
	lim, _ := testLimiter(l)
	lim.decide("a", true)
	for i := 0; i < 5; i++ {
		lim.decide("a", true) // refused per chat
	}
	if ok, why, _ := lim.decide("b", true); !ok {
		t.Fatalf("refused sends consumed the global budget: %s", why)
	}
}

func TestOutboundLimiterRestoreDropsOldAdmissions(t *testing.T) {
	lim, clk := testLimiter(defaultOutboundLimits)
	lim.restore(map[networkid.PortalID]time.Time{
		"recent": clk.t.Add(-time.Hour),
		"stale":  clk.t.Add(-8 * 24 * time.Hour),
	})
	snap := lim.snapshot()
	if _, ok := snap["recent"]; !ok {
		t.Fatal("recent admission lost on restore")
	}
	if _, ok := snap["stale"]; ok {
		t.Fatal("admission older than 7 days kept")
	}
	if _, _, isNew := lim.decide("recent", false); isNew {
		t.Fatal("restored admission counted as a new chat again")
	}
}

func TestOutboundLimitEnvOff(t *testing.T) {
	t.Setenv("MACOUN_OUTBOUND_LIMIT", "off")
	if !outboundLimitDisabled() {
		t.Fatal("MACOUN_OUTBOUND_LIMIT=off not honoured")
	}
	t.Setenv("MACOUN_OUTBOUND_LIMIT", "")
	if outboundLimitDisabled() {
		t.Fatal("limiter disabled by default")
	}
}

func testLimiterStore(t *testing.T) (*limiterStore, *dbutil.Database) {
	t.Helper()
	db, err := dbutil.NewWithDialect("file:"+filepath.Join(t.TempDir(), "lim.db")+"?_txlock=immediate", "sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	key, err := loadOrCreateKeyAt(filepath.Join(t.TempDir(), limiterKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	return &limiterStore{db: db, key: key, loginID: "D:123"}, db
}

func TestLimiterStoreRoundTripAndAtRestEncryption(t *testing.T) {
	s, db := testLimiterStore(t)
	ctx := context.Background()
	if got, err := s.load(ctx); err != nil || got != nil {
		t.Fatalf("empty store: got %v, %v", got, err)
	}
	at := time.UnixMilli(time.Now().UnixMilli())
	if err := s.save(ctx, map[networkid.PortalID]time.Time{"tel:+15550100": at}); err != nil {
		t.Fatal(err)
	}
	got, err := s.load(ctx)
	if err != nil || !got["tel:+15550100"].Equal(at) {
		t.Fatalf("round trip: got %v, %v", got, err)
	}
	var blob []byte
	if err := db.QueryRow(ctx, `SELECT state FROM macoun_outbound_limiter`).Scan(&blob); err != nil {
		t.Fatal(err)
	}
	if containsBytes(blob, []byte("15550100")) {
		t.Fatal("portal ID stored in plaintext")
	}
}

func TestLimiterStoreCorruptOrForeignStateIsIgnored(t *testing.T) {
	s, db := testLimiterStore(t)
	ctx := context.Background()
	if err := s.save(ctx, map[networkid.PortalID]time.Time{"p": time.Now()}); err != nil {
		t.Fatal(err)
	}
	// Wrong key (e.g. a lost key file): refused, not misread.
	other := *s
	other.key = make([]byte, 32)
	if _, err := other.load(ctx); err == nil {
		t.Fatal("state decrypted with the wrong key")
	}
	// Another login's row cannot be swapped in (login ID is associated data).
	swapped := *s
	swapped.loginID = "D:999"
	if _, err := db.Exec(ctx, `INSERT INTO macoun_outbound_limiter SELECT 'D:999', state, updated_at FROM macoun_outbound_limiter WHERE login_id='D:123'`); err != nil {
		t.Fatal(err)
	}
	if _, err := swapped.load(ctx); err == nil {
		t.Fatal("row copied from another login was accepted")
	}
	// Garbage bytes.
	if _, err := db.Exec(ctx, `UPDATE macoun_outbound_limiter SET state=$1 WHERE login_id='D:123'`, []byte("garbage")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.load(ctx); err == nil {
		t.Fatal("corrupt state accepted")
	}
	// A later save replaces the corrupt row.
	if err := s.save(ctx, map[networkid.PortalID]time.Time{"q": time.Now()}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.load(ctx); err != nil || len(got) != 1 {
		t.Fatalf("save after corruption: %v, %v", got, err)
	}
}

func TestLimiterKeyFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", limiterKeyFile)
	k1, err := loadOrCreateKeyAt(p)
	if err != nil {
		t.Fatal(err)
	}
	k2, err := loadOrCreateKeyAt(p)
	if err != nil || string(k1) != string(k2) {
		t.Fatal("key not stable across loads")
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode %v, want 0600", fi.Mode().Perm())
	}
	if err := os.WriteFile(p, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateKeyAt(p); err == nil {
		t.Fatal("truncated key file accepted")
	}
}

func containsBytes(h, n []byte) bool {
	for i := 0; i+len(n) <= len(h); i++ {
		if string(h[i:i+len(n)]) == string(n) {
			return true
		}
	}
	return false
}
