package connector

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/lrhodin/corten-matrix/pkg/rustpushgo"
)

// startupHoldTestBuffer is a reorder buffer whose CloudKit-sync state and
// dispatch target are controlled by the test.
type startupHoldTestBuffer struct {
	*messageBuffer
	mu         sync.Mutex
	syncDone   bool
	dispatched []string
}

func newStartupHoldTestBuffer() *startupHoldTestBuffer {
	tb := &startupHoldTestBuffer{}
	tb.messageBuffer = &messageBuffer{
		syncDoneFn: func() bool {
			tb.mu.Lock()
			defer tb.mu.Unlock()
			return tb.syncDone
		},
		dispatchFn: func(msg rustpushgo.WrappedMessage) {
			tb.mu.Lock()
			defer tb.mu.Unlock()
			tb.dispatched = append(tb.dispatched, msg.Uuid)
		},
	}
	return tb
}

func (tb *startupHoldTestBuffer) delivered() []string {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	return append([]string(nil), tb.dispatched...)
}

// waitDelivered waits up to four quiet windows for n dispatched messages.
func (tb *startupHoldTestBuffer) waitDelivered(n int) []string {
	deadline := time.Now().Add(4 * messageBufferQuietWindow)
	for time.Now().Before(deadline) {
		if got := tb.delivered(); len(got) >= n {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	return tb.delivered()
}

// TestStartupHoldDeliversDuringCloudSync: a message that arrives while the
// startup CloudKit sync is still running, for a chat that needs nothing from
// that sync, is delivered after the normal quiet window instead of waiting for
// the sync to finish.
func TestStartupHoldDeliversDuringCloudSync(t *testing.T) {
	tb := newStartupHoldTestBuffer()
	tb.holdAppliesFn = func(rustpushgo.WrappedMessage) bool { return false }
	tb.add(rustpushgo.WrappedMessage{Uuid: "live-1", TimestampMs: 1})
	if got := tb.waitDelivered(1); len(got) != 1 {
		t.Fatalf("message was held for the whole CloudKit sync: delivered=%v", got)
	}
}

// TestStartupHoldKeepsHeldMessagesUntilFlush: messages the hold applies to stay
// in the buffer while other chats flow, and the final flush delivers them in
// timestamp order.
func TestStartupHoldKeepsHeldMessagesUntilFlush(t *testing.T) {
	tb := newStartupHoldTestBuffer()
	held := map[string]bool{"held-2": true, "held-1": true}
	tb.holdAppliesFn = func(msg rustpushgo.WrappedMessage) bool { return held[msg.Uuid] }

	tb.add(rustpushgo.WrappedMessage{Uuid: "held-2", TimestampMs: 20})
	tb.add(rustpushgo.WrappedMessage{Uuid: "live-b", TimestampMs: 40})
	tb.add(rustpushgo.WrappedMessage{Uuid: "held-1", TimestampMs: 10})
	tb.add(rustpushgo.WrappedMessage{Uuid: "live-a", TimestampMs: 30})

	got := tb.waitDelivered(2)
	// One more quiet window: a held message must not trickle out later.
	time.Sleep(2 * messageBufferQuietWindow)
	got = tb.delivered()
	if len(got) != 2 || got[0] != "live-a" || got[1] != "live-b" {
		t.Fatalf("want only the unheld messages, in timestamp order; delivered=%v", got)
	}

	tb.mu.Lock()
	tb.syncDone = true
	tb.mu.Unlock()
	tb.flush()
	got = tb.delivered()
	if len(got) != 4 || got[2] != "held-1" || got[3] != "held-2" {
		t.Fatalf("final flush must deliver the held messages in timestamp order; delivered=%v", got)
	}
}

// TestStartupHoldReleaseUnheld: everything is held until the hold is scoped;
// releaseUnheld then lets go of exactly the messages it no longer applies to.
func TestStartupHoldReleaseUnheld(t *testing.T) {
	tb := newStartupHoldTestBuffer()
	scoped := false
	tb.holdAppliesFn = func(msg rustpushgo.WrappedMessage) bool {
		tb.mu.Lock()
		defer tb.mu.Unlock()
		return !scoped || msg.Uuid == "new-room"
	}
	tb.add(rustpushgo.WrappedMessage{Uuid: "settled-2", TimestampMs: 3})
	tb.add(rustpushgo.WrappedMessage{Uuid: "new-room", TimestampMs: 2})
	tb.add(rustpushgo.WrappedMessage{Uuid: "settled-1", TimestampMs: 1})
	time.Sleep(2 * messageBufferQuietWindow)
	if got := tb.delivered(); len(got) != 0 {
		t.Fatalf("nothing may be delivered before the hold is scoped; delivered=%v", got)
	}

	tb.mu.Lock()
	scoped = true
	tb.mu.Unlock()
	released, stillHeld := tb.releaseUnheld()
	if released != 2 || stillHeld != 1 {
		t.Fatalf("releaseUnheld: released=%d stillHeld=%d, want 2 and 1", released, stillHeld)
	}
	if got := tb.delivered(); len(got) != 2 || got[0] != "settled-1" || got[1] != "settled-2" {
		t.Fatalf("want the two settled messages in timestamp order; delivered=%v", got)
	}

	tb.flush()
	if got := tb.delivered(); len(got) != 3 || got[2] != "new-room" {
		t.Fatalf("final flush must deliver the remaining held message; delivered=%v", got)
	}
}

func TestStartupHoldDecision(t *testing.T) {
	settled := startupHoldFacts{PortalKnown: true, HasRoom: true}
	with := func(mod func(*startupHoldFacts)) startupHoldFacts {
		f := settled
		mod(&f)
		return f
	}
	cases := []struct {
		name   string
		scoped bool
		facts  startupHoldFacts
		hold   bool
	}{
		{"before the sync and seed finish, everything is held", false, settled, true},
		{"room exists, no forward backfill", true, settled, false},
		{"forward backfill with content", true, with(func(f *startupHoldFacts) { f.BackfillWouldRun, f.BackfillHasContent = true, true }), true},
		{"forward backfill would run but finds nothing that matters", true, with(func(f *startupHoldFacts) { f.BackfillWouldRun = true }), false},
		{"no room yet", true, with(func(f *startupHoldFacts) { f.HasRoom = false }), true},
		{"deleted chat", true, with(func(f *startupHoldFacts) { f.Deleted = true }), true},
		{"unknown chat", true, startupHoldFacts{}, true},
		{"lookup failed", true, with(func(f *startupHoldFacts) { f.LookupFailed = true }), true},
	}
	for _, tc := range cases {
		if hold, reason := startupHoldDecision(tc.scoped, tc.facts); hold != tc.hold {
			t.Errorf("%s: hold=%v (reason %q), want %v", tc.name, hold, reason, tc.hold)
		}
	}
}

func TestStartupHoldForwardBackfillWouldRun(t *testing.T) {
	t0 := time.UnixMilli(1_700_000_000_000)
	cases := []struct {
		name       string
		cloudMS    int64
		hasBridged bool
		bridged    time.Time
		want       bool
	}{
		{"chat was never in CloudKit", 0, true, t0, false},
		{"CloudKit and room agree", t0.UnixMilli(), true, t0, false},
		{"room is newer", t0.UnixMilli(), true, t0.Add(time.Minute), false},
		{"CloudKit is newer", t0.UnixMilli() + 1, true, t0, true},
		{"CloudKit rows but empty room", t0.UnixMilli(), false, time.Time{}, true},
	}
	for _, tc := range cases {
		if got := forwardBackfillWouldRun(tc.cloudMS, tc.hasBridged, tc.bridged); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func insertStartupHoldRow(t *testing.T, ctx context.Context, store *cloudBackfillStore, portalID, guid, recordName string, ts, updatedTS int64, tapbackType any, deleted bool) {
	t.Helper()
	if _, err := store.db.Exec(ctx,
		`INSERT INTO cloud_message (login_id, record_name, guid, portal_id, sender, is_from_me, service, timestamp_ms, created_ts, updated_ts, deleted, tapback_type)
		 VALUES ($1, $2, $3, $4, 'x', FALSE, 'iMessage', $5, $6, $6, $7, $8)`,
		store.loginID, recordName, guid, portalID, ts, updatedTS, deleted, tapbackType,
	); err != nil {
		t.Fatalf("insert cloud_message %s: %v", guid, err)
	}
}

// TestStartupHoldNewestLiveCloudMessageTS: the value compared against the room
// must count what the portal pass counts: live CloudKit records only.
func TestStartupHoldNewestLiveCloudMessageTS(t *testing.T) {
	ctx, store := recycleBinFixture(t)
	const portalID = "tel:+15550101"
	insertStartupHoldRow(t, ctx, store, portalID, "live", "rec-live", 1000, 1, nil, false)
	insertStartupHoldRow(t, ctx, store, portalID, "deleted-newer", "rec-del", 3000, 1, nil, true)
	// A UUID stub, as persistMessageUUID writes for a live APNs message.
	insertStartupHoldRow(t, ctx, store, portalID, "stub-newest", "", 5000, 1, nil, false)
	got, err := store.newestLiveCloudMessageTS(ctx, portalID)
	if err != nil || got != 1000 {
		t.Fatalf("newestLiveCloudMessageTS = %d, %v; want 1000", got, err)
	}
	if got, err = store.newestLiveCloudMessageTS(ctx, "tel:+15550199"); err != nil || got != 0 {
		t.Fatalf("portal without rows: %d, %v; want 0", got, err)
	}
}

// TestStartupHoldPendingForwardBackfillContent: which CloudKit rows past the
// room's newest message keep a chat held.
func TestStartupHoldPendingForwardBackfillContent(t *testing.T) {
	const (
		anchorTS  = int64(1000)
		syncStart = int64(50_000) // updated_ts values at or above this are "this run"
	)
	type row struct {
		guid      string
		ts        int64
		updatedTS int64
		tapback   any
		deleted   bool
		stub      bool
	}
	cases := []struct {
		name      string
		rows      []row
		hasAnchor bool
		want      bool
	}{
		{"nothing after the anchor", []row{{guid: "a", ts: 900, updatedTS: 1}}, true, false},
		{"old tapback tail (delivered by an earlier run)", []row{{guid: "t", ts: 2000, updatedTS: 40_000, tapback: 2001}}, true, false},
		{"tapback imported by this run", []row{{guid: "t", ts: 2000, updatedTS: 50_000, tapback: 2001}}, true, true},
		{"message after the anchor", []row{{guid: "m", ts: 2000, updatedTS: 1}}, true, true},
		{"message at the anchor timestamp with a later GUID", []row{{guid: "zzz", ts: 1000, updatedTS: 1}}, true, true},
		{"the anchor itself", []row{{guid: "anchor", ts: 1000, updatedTS: 1}}, true, false},
		{"deleted message after the anchor", []row{{guid: "d", ts: 2000, updatedTS: 1, deleted: true}}, true, false},
		{"UUID stub of a live APNs message", []row{{guid: "s", ts: 2000, updatedTS: 60_000, stub: true}}, true, false},
		{"empty room, CloudKit has a message", []row{{guid: "a", ts: 900, updatedTS: 1}}, false, true},
		{"empty room, only an old tapback", []row{{guid: "t", ts: 900, updatedTS: 1, tapback: 2000}}, false, false},
	}
	for i, tc := range cases {
		ctx, store := recycleBinFixture(t)
		portalID := "tel:+1555020" + string(rune('0'+i))
		for _, r := range tc.rows {
			recordName := "rec-" + r.guid
			if r.stub {
				recordName = ""
			}
			insertStartupHoldRow(t, ctx, store, portalID, r.guid, recordName, r.ts, r.updatedTS, r.tapback, r.deleted)
		}
		got, err := store.pendingForwardBackfillContent(ctx, portalID, tc.hasAnchor, anchorTS, "anchor", syncStart)
		if err != nil || got != tc.want {
			t.Errorf("%s: got %v, %v; want %v", tc.name, got, err, tc.want)
		}
	}
}

// TestRehydrateKnownEmpty: a rehydrate that recovered nothing is not repeated
// until the portal gets rows written after it.
func TestRehydrateKnownEmpty(t *testing.T) {
	ctx, store := recycleBinFixture(t)
	const portalID = "tel:+15550300"
	insertStartupHoldRow(t, ctx, store, portalID, "old", "rec-old", 1000, 1, nil, false)

	if known, err := store.rehydrateKnownEmpty(ctx, portalID); err != nil || known {
		t.Fatalf("before any rehydrate: known=%v err=%v, want false", known, err)
	}
	if err := store.noteRehydrateFoundNothing(ctx, portalID); err != nil {
		t.Fatal(err)
	}
	if known, err := store.rehydrateKnownEmpty(ctx, portalID); err != nil || !known {
		t.Fatalf("after an empty rehydrate: known=%v err=%v, want true", known, err)
	}
	if known, err := store.rehydrateKnownEmpty(ctx, "tel:+15550301"); err != nil || known {
		t.Fatalf("another portal: known=%v err=%v, want false", known, err)
	}
	// CloudKit delivers a row later.
	insertStartupHoldRow(t, ctx, store, portalID, "new", "rec-new", 2000, time.Now().Add(time.Minute).UnixMilli(), nil, false)
	if known, err := store.rehydrateKnownEmpty(ctx, portalID); err != nil || known {
		t.Fatalf("after a newer row: known=%v err=%v, want false", known, err)
	}
}
