package connector

import (
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
	t0 := time.UnixMilli(1_700_000_000_000)
	settled := startupHoldFacts{PortalKnown: true, HasRoom: true, NewestCloud: t0, HasBridged: true, NewestBridged: t0}
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
		{"room exists, CloudKit has nothing newer", true, settled, false},
		{"room exists, chat was never in CloudKit", true, startupHoldFacts{PortalKnown: true, HasRoom: true}, false},
		{"room newer than CloudKit", true, with(func(f *startupHoldFacts) { f.NewestBridged = t0.Add(time.Minute) }), false},
		{"CloudKit has newer history: forward backfill pending", true, with(func(f *startupHoldFacts) { f.NewestCloud = t0.Add(time.Millisecond) }), true},
		{"CloudKit history but empty room", true, with(func(f *startupHoldFacts) { f.HasBridged = false }), true},
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

// TestStartupHoldNewestLiveCloudMessageTS: the value compared against the room
// must count what the portal pass counts: live CloudKit records only.
func TestStartupHoldNewestLiveCloudMessageTS(t *testing.T) {
	ctx, store := recycleBinFixture(t)
	const portalID = "tel:+15550101"
	insertRecycleTestMessage(t, ctx, store, portalID, "live", 1000, false)
	insertRecycleTestMessage(t, ctx, store, portalID, "deleted-newer", 3000, true)
	// A UUID stub, as persistMessageUUID writes for a live APNs message.
	if _, err := store.db.Exec(ctx,
		`INSERT INTO cloud_message (login_id, record_name, guid, portal_id, sender, is_from_me, service, timestamp_ms, created_ts, updated_ts, deleted)
		 VALUES ($1, '', 'stub-newest', $2, 'x', FALSE, 'iMessage', 5000, 1, 1, FALSE)`,
		store.loginID, portalID,
	); err != nil {
		t.Fatal(err)
	}
	got, err := store.newestLiveCloudMessageTS(ctx, portalID)
	if err != nil || got != 1000 {
		t.Fatalf("newestLiveCloudMessageTS = %d, %v; want 1000", got, err)
	}
	if got, err = store.newestLiveCloudMessageTS(ctx, "tel:+15550199"); err != nil || got != 0 {
		t.Fatalf("portal without rows: %d, %v; want 0", got, err)
	}
}
