package connector

import (
	"context"
	"testing"
)

func portalIsStartupCandidate(t *testing.T, ctx context.Context, store *cloudBackfillStore, portalID string) bool {
	t.Helper()
	infos, err := store.listPortalIDsWithNewestTimestamp(ctx, false)
	if err != nil {
		t.Fatalf("listPortalIDsWithNewestTimestamp: %v", err)
	}
	for _, info := range infos {
		if info.PortalID == portalID {
			return true
		}
	}
	return false
}

func portalIsRecordedDeleted(t *testing.T, ctx context.Context, store *cloudBackfillStore, portalID string) bool {
	t.Helper()
	ids, err := store.listDeletedPortalIDs(ctx)
	if err != nil {
		t.Fatalf("listDeletedPortalIDs: %v", err)
	}
	for _, id := range ids {
		if id == portalID {
			return true
		}
	}
	return false
}

// TestMacounDeletePortalStaysDeleted: bridgev2's delete-portal writes nothing
// to the cloud store, so the chat is still a candidate for the next startup
// portal pass, which re-creates its room. Recording the deletion takes it out
// of the candidates and puts it among the deleted portals a start reloads.
func TestMacounDeletePortalStaysDeleted(t *testing.T) {
	ctx, store := recycleBinFixture(t)
	const portalID = "tel:+15550400"
	if _, err := store.db.Exec(ctx,
		`INSERT INTO cloud_chat (login_id, cloud_chat_id, portal_id, created_ts, updated_ts) VALUES ($1, 'iMessage;-;+15550400', $2, 1, 1)`,
		store.loginID, portalID,
	); err != nil {
		t.Fatal(err)
	}
	insertStartupHoldRow(t, ctx, store, portalID, "m1", "rec-m1", 1000, 1, nil, false)
	insertStartupHoldRow(t, ctx, store, portalID, "m2", "rec-m2", 2000, 1, nil, false)

	// What the built-in command leaves behind: nothing changed here.
	if !portalIsStartupCandidate(t, ctx, store, portalID) || portalIsRecordedDeleted(t, ctx, store, portalID) {
		t.Fatal("fixture: a live chat must be a startup candidate and not recorded as deleted")
	}

	if err := store.recordPortalDeletedLocally(ctx, portalID); err != nil {
		t.Fatalf("recordPortalDeletedLocally: %v", err)
	}
	if portalIsStartupCandidate(t, ctx, store, portalID) {
		t.Fatal("deleted portal is still a candidate for the startup portal pass")
	}
	if !portalIsRecordedDeleted(t, ctx, store, portalID) {
		t.Fatal("deleted portal is not among the deleted portals reloaded at startup")
	}
	if known, _ := store.hasMessageUUID(ctx, "m1"); !known {
		t.Fatal("message UUIDs must survive the deletion for echo suppression")
	}

	// The seed must not take the chat for one restored on Apple's side: two of
	// its older messages sit in the recycle bin, its newest do not.
	state := recycleStateFor(t, ctx, store, []string{"m1|"}, portalID)
	if !state.NeedsUndelete() {
		t.Fatalf("fixture should look restored to the recycle-bin classifier, got %+v", state)
	}
	locallyDeleted, err := store.isPortalDeletedLocally(ctx, portalID)
	if err != nil || !locallyDeleted {
		t.Fatalf("isPortalDeletedLocally = %v, %v; want true", locallyDeleted, err)
	}
	if recycleBinMayAutoRecover(state, true, locallyDeleted) {
		t.Fatal("the seed would undo a delete-portal deletion")
	}
	if !recycleBinMayAutoRecover(state, true, false) {
		t.Fatal("control: without the marker the same state is auto-recovered")
	}

	// restore-chat ends the deletion.
	if err = store.setRestoreOverride(ctx, portalID); err != nil {
		t.Fatal(err)
	}
	if locallyDeleted, _ = store.isPortalDeletedLocally(ctx, portalID); locallyDeleted {
		t.Fatal("restore override must clear the delete-portal marker")
	}
}

// TestMacounDeletePortalWithoutCloudRows: a chat that only ever existed through
// live messages has no chat row; the deletion still has to be on record.
func TestMacounDeletePortalWithoutCloudRows(t *testing.T) {
	ctx, store := recycleBinFixture(t)
	const portalID = "tel:+15550401"
	if err := store.recordPortalDeletedLocally(ctx, portalID); err != nil {
		t.Fatalf("recordPortalDeletedLocally: %v", err)
	}
	if !portalIsRecordedDeleted(t, ctx, store, portalID) {
		t.Fatal("deletion of a chat without CloudKit rows left no record")
	}
}

// TestMacounDeletePortalCommandOverridesBuiltin: the command must carry the
// built-in's name, or registering it would add a second command instead of
// replacing the one that forgets the deletion.
func TestMacounDeletePortalCommandOverridesBuiltin(t *testing.T) {
	if cmdDeletePortal.Name != "delete-portal" || !cmdDeletePortal.RequiresPortal || !cmdDeletePortal.RequiresAdmin {
		t.Fatalf("unexpected command shape: %+v", cmdDeletePortal)
	}
	found := false
	for _, h := range BridgeCommands() {
		if h == cmdDeletePortal {
			found = true
		}
	}
	if !found {
		t.Fatal("cmdDeletePortal is not registered in BridgeCommands")
	}
}
