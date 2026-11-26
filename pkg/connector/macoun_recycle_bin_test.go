package connector

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"

	"github.com/rs/zerolog"

	"github.com/lrhodin/corten-matrix/pkg/rustpushgo"
)

// fakeRecycleFeed is a recycle-bin zone as CloudKit serves it: an append-only
// change feed, 200 changes per request, addressed by a continuation token.
type fakeRecycleFeed struct {
	changes  map[string][]fakeRecycleChange
	requests int
	failing  bool
}

type fakeRecycleChange struct {
	recordName string
	guid       string // empty for a tombstone
}

const fakeRecyclePageSize = 200

func (f *fakeRecycleFeed) SyncRecoverableZone(zone string, token *string) (rustpushgo.WrappedRecoverableZonePage, error) {
	f.requests++
	if f.failing {
		return rustpushgo.WrappedRecoverableZonePage{}, errors.New("fake CloudKit failure")
	}
	start := 0
	if token != nil {
		start, _ = strconv.Atoi(*token)
	}
	feed := f.changes[zone]
	end := start + fakeRecyclePageSize
	if end > len(feed) {
		end = len(feed)
	}
	var page rustpushgo.WrappedRecoverableZonePage
	for _, ch := range feed[start:end] {
		if ch.guid == "" {
			page.RemovedRecordNames = append(page.RemovedRecordNames, ch.recordName)
			continue
		}
		page.MessageEntries = append(page.MessageEntries, ch.guid+"|")
		page.MessageRecordNames = append(page.MessageRecordNames, ch.recordName)
	}
	if end > start {
		next := strconv.Itoa(end)
		page.ContinuationToken = &next
	}
	page.Done = end == len(feed)
	if page.Done {
		page.Status = 3
	} else {
		page.Status = 1
	}
	return page, nil
}

// legacyRecoverableGUIDs reads a zone the way list_recoverable_message_guids in
// rustpushgo does: from the start of the feed on every call, at most 256 pages.
func legacyRecoverableGUIDs(t *testing.T, f *fakeRecycleFeed, zone string) (guids []string, truncated bool) {
	t.Helper()
	var token *string
	for page := 0; page < 256; page++ {
		resp, err := f.SyncRecoverableZone(zone, token)
		if err != nil {
			t.Fatalf("legacy read: %v", err)
		}
		guids = append(guids, resp.MessageEntries...)
		if resp.Done {
			return guids, false
		}
		token = resp.ContinuationToken
	}
	return guids, true
}

func recycleBinFixture(t *testing.T) (context.Context, *cloudBackfillStore) {
	t.Helper()
	ctx := context.Background()
	store := newCloudBackfillStore(newTestSQLiteDB(t), testSQLLoginID)
	if err := store.ensureSchema(ctx); err != nil {
		t.Fatalf("ensureSchema: %v", err)
	}
	return ctx, store
}

func insertRecycleTestMessage(t *testing.T, ctx context.Context, store *cloudBackfillStore, portalID, guid string, ts int64, deleted bool) {
	t.Helper()
	if _, err := store.db.Exec(ctx,
		`INSERT INTO cloud_message (login_id, record_name, guid, portal_id, sender, is_from_me, service, timestamp_ms, created_ts, updated_ts, deleted)
		 VALUES ($1, $2, $3, $4, 'x', FALSE, 'iMessage', $5, 1, 1, $6)`,
		store.loginID, "rec-"+guid, guid, portalID, ts, deleted,
	); err != nil {
		t.Fatalf("insert cloud_message: %v", err)
	}
}

func recycleStateFor(t *testing.T, ctx context.Context, store *cloudBackfillStore, entries []string, portalID string) recycleBinPortalState {
	t.Helper()
	states, err := store.classifyRecycleBinPortals(ctx, entries)
	if err != nil {
		t.Fatalf("classifyRecycleBinPortals: %v", err)
	}
	for _, st := range states {
		if st.PortalID == portalID {
			return st
		}
	}
	t.Fatalf("portal %s not classified (%d states)", portalID, len(states))
	return recycleBinPortalState{}
}

// deletedChatBehindLongFeed builds the situation that bridged a deleted chat
// back into Matrix: a chat the user deleted (tombstoned locally, every row
// deleted=TRUE) whose recycle-bin records sit behind more than 256 pages of
// older changes, while two messages the user had deleted from that chat long
// before sit near the start of the feed.
func deletedChatBehindLongFeed(t *testing.T, ctx context.Context, store *cloudBackfillStore) (*fakeRecycleFeed, string) {
	t.Helper()
	const portalID = "tel:+15550100"
	zone := recycleBinZones[0]
	feed := &fakeRecycleFeed{changes: map[string][]fakeRecycleChange{}}
	add := func(guid string) {
		feed.changes[zone] = append(feed.changes[zone], fakeRecycleChange{recordName: "r-" + guid, guid: guid})
	}
	// Ten messages in the deleted chat; msg-0 is the oldest.
	for i := 0; i < 10; i++ {
		insertRecycleTestMessage(t, ctx, store, portalID, fmt.Sprintf("msg-%d", i), int64(1000+i), true)
	}
	add("msg-0")
	add("msg-1")
	// Unrelated recycle-bin history that pushes the chat deletion past the
	// 256-page limit of the legacy reader.
	for i := 0; i < 256*fakeRecyclePageSize; i++ {
		add(fmt.Sprintf("other-%d", i))
	}
	// The chat deletion: every remaining message moves to the recycle bin.
	for i := 2; i < 10; i++ {
		add(fmt.Sprintf("msg-%d", i))
	}
	return feed, portalID
}

// TestRecycleBinLegacyReadUndeletesDeletedChat reproduces the bug with the
// legacy reader and shows the incremental copy does not have it.
func TestRecycleBinLegacyReadUndeletesDeletedChat(t *testing.T) {
	ctx, store := recycleBinFixture(t)
	feed, portalID := deletedChatBehindLongFeed(t, ctx, store)

	legacy, truncated := legacyRecoverableGUIDs(t, feed, recycleBinZones[0])
	if !truncated {
		t.Fatal("fixture must exceed the legacy 256-page limit")
	}
	legacyState := recycleStateFor(t, ctx, store, legacy, portalID)
	if !legacyState.NeedsUndelete() {
		t.Fatalf("expected the truncated legacy read to misread the deleted chat as restored, got %+v", legacyState)
	}

	snap, err := store.syncRecycleBin(ctx, feed, zerolog.Nop())
	if err != nil {
		t.Fatalf("syncRecycleBin: %v", err)
	}
	if !snap.Complete {
		t.Fatal("expected a complete snapshot")
	}
	state := recycleStateFor(t, ctx, store, snap.Entries, portalID)
	if !state.LooksDeleted() {
		t.Fatalf("complete recycle bin must show the chat as deleted, got %+v", state)
	}
	if recycleBinMayAutoRecover(state, snap.Complete, false) {
		t.Fatal("a chat whose newest messages are in the recycle bin must not be auto-recovered")
	}
}

// TestRecycleBinIncompleteSnapshotNeverAutoRecovers covers the pass that
// cannot reach the end of the feed: whatever the partial copy suggests, a
// local deletion stays.
func TestRecycleBinIncompleteSnapshotNeverAutoRecovers(t *testing.T) {
	ctx, store := recycleBinFixture(t)
	feed, portalID := deletedChatBehindLongFeed(t, ctx, store)

	// Store only the start of the feed, as a pass interrupted after one page
	// leaves it.
	first, err := feed.SyncRecoverableZone(recycleBinZones[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.ensureRecycleBinSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err = store.applyRecycleBinPage(ctx, recycleBinZones[0], first); err != nil {
		t.Fatal(err)
	}
	feed.failing = true

	snap, err := store.syncRecycleBin(ctx, feed, zerolog.Nop())
	if err != nil {
		t.Fatalf("syncRecycleBin: %v", err)
	}
	if snap.Complete {
		t.Fatal("a pass with a failed read must not be complete")
	}
	state := recycleStateFor(t, ctx, store, snap.Entries, portalID)
	if !state.NeedsUndelete() {
		t.Fatalf("fixture should look restored on a partial copy, got %+v", state)
	}
	if recycleBinMayAutoRecover(state, snap.Complete, false) {
		t.Fatal("an incomplete snapshot must never undo a local deletion")
	}
}

// TestRecycleBinLaterStartsReadOnlyNewChanges is the startup-cost half: the
// legacy reader pays for the whole feed on every start, the copy pays once.
func TestRecycleBinLaterStartsReadOnlyNewChanges(t *testing.T) {
	ctx, store := recycleBinFixture(t)
	feed, _ := deletedChatBehindLongFeed(t, ctx, store)
	zone := recycleBinZones[0]

	for start := 1; start <= 2; start++ {
		feed.requests = 0
		legacyRecoverableGUIDs(t, feed, zone)
		if feed.requests != 256 {
			t.Fatalf("legacy start %d: expected 256 requests, got %d", start, feed.requests)
		}
	}

	feed.requests = 0
	first, err := store.syncRecycleBin(ctx, feed, zerolog.Nop())
	if err != nil || !first.Complete {
		t.Fatalf("first sync: complete=%v err=%v", first.Complete, err)
	}
	wantEntries := 2 + 256*fakeRecyclePageSize + 8
	if len(first.Entries) != wantEntries {
		t.Fatalf("first sync: expected %d entries, got %d", wantEntries, len(first.Entries))
	}
	firstRequests := feed.requests

	feed.requests = 0
	second, err := store.syncRecycleBin(ctx, feed, zerolog.Nop())
	if err != nil || !second.Complete {
		t.Fatalf("second sync: complete=%v err=%v", second.Complete, err)
	}
	if feed.requests != len(recycleBinZones) {
		t.Fatalf("second start should cost one request per zone (%d), got %d (first start: %d)",
			len(recycleBinZones), feed.requests, firstRequests)
	}
	if len(second.Entries) != wantEntries {
		t.Fatalf("second sync changed the snapshot: %d entries", len(second.Entries))
	}

	// A restore (tombstone in the feed) and a new deletion arrive later.
	feed.changes[zone] = append(feed.changes[zone],
		fakeRecycleChange{recordName: "r-msg-9"},
		fakeRecycleChange{recordName: "r-new", guid: "new-guid"},
	)
	feed.requests = 0
	third, err := store.syncRecycleBin(ctx, feed, zerolog.Nop())
	if err != nil || !third.Complete {
		t.Fatalf("third sync: complete=%v err=%v", third.Complete, err)
	}
	if feed.requests != len(recycleBinZones) {
		t.Fatalf("third start: expected %d requests, got %d", len(recycleBinZones), feed.requests)
	}
	has := map[string]bool{}
	for _, e := range third.Entries {
		has[e] = true
	}
	if has["msg-9|"] || !has["new-guid|"] || len(third.Entries) != wantEntries {
		t.Fatalf("third sync did not apply the tombstone and the new record (entries=%d, msg-9=%v, new=%v)",
			len(third.Entries), has["msg-9|"], has["new-guid|"])
	}
}

// TestRecycleBinRepeatedFailureRestartsFeed checks the stand-in for "token no
// longer accepted": the stored position survives occasional failures and is
// dropped, with its records, after recycleBinTokenFailureLimit in a row.
func TestRecycleBinRepeatedFailureRestartsFeed(t *testing.T) {
	ctx, store := recycleBinFixture(t)
	zone := recycleBinZones[0]
	feed := &fakeRecycleFeed{changes: map[string][]fakeRecycleChange{
		zone: {{recordName: "r-a", guid: "a"}, {recordName: "r-b", guid: "b"}},
	}}
	if snap, err := store.syncRecycleBin(ctx, feed, zerolog.Nop()); err != nil || !snap.Complete {
		t.Fatalf("initial sync: complete=%v err=%v", snap.Complete, err)
	}

	feed.failing = true
	for pass := 1; pass < recycleBinTokenFailureLimit; pass++ {
		snap, err := store.syncRecycleBin(ctx, feed, zerolog.Nop())
		if err != nil {
			t.Fatalf("failing pass %d: %v", pass, err)
		}
		if snap.Complete || len(snap.Entries) != 2 {
			t.Fatalf("failing pass %d: want incomplete snapshot with the stored 2 entries, got complete=%v entries=%d",
				pass, snap.Complete, len(snap.Entries))
		}
		if token, _, _ := store.recycleBinZoneState(ctx, zone); token == nil {
			t.Fatalf("position dropped after only %d failures", pass)
		}
	}
	if _, err := store.syncRecycleBin(ctx, feed, zerolog.Nop()); err != nil {
		t.Fatal(err)
	}
	if token, _, _ := store.recycleBinZoneState(ctx, zone); token != nil {
		t.Fatal("position should be dropped after repeated failures")
	}

	feed.failing = false
	snap, err := store.syncRecycleBin(ctx, feed, zerolog.Nop())
	if err != nil || !snap.Complete || len(snap.Entries) != 2 {
		t.Fatalf("after reset: complete=%v entries=%d err=%v", snap.Complete, len(snap.Entries), err)
	}
}
