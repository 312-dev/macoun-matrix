// macoun-matrix - A Matrix-iMessage puppeting bridge (fork of corten-matrix).
// SPDX-License-Identifier: MPL-2.0
//
// macoun: make the delete-portal command stick.
//
// bridgev2's delete-portal deletes the portal and its Matrix room and tells
// the connector nothing. The chat's CloudKit rows stay live in the cloud
// store, so the next startup portal pass (createPortalsFromCloudSync) creates
// the room again and backfills its history into it.
//
// The command registered here records the deletion first, the way an Apple
// chat delete received over APNs does (handleChatDelete): the chat joins
// recentlyDeletedPortals and its cloud_chat and cloud_message rows are
// soft-deleted, which is what the portal pass and the echo suppression read.
// It does not delete anything on Apple's side. delete-all-portals gets the
// same treatment for every portal.
//
// One difference from an Apple delete: Apple's recycle bin does not know about
// this deletion, so the deleted-chat seed could take the chat for one the user
// restored and undo the tombstone. A marker row in cloud_maintenance
// ("macoun_local_delete:<portal id>") says the deletion was made here; the
// seed leaves such chats deleted. The marker goes away when the chat comes
// back on purpose: restore-chat, or a new message reviving the chat.

package connector

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

func localDeleteTask(portalID string) string {
	return "macoun_local_delete:" + portalID
}

// isPortalDeletedLocally reports whether the portal was deleted with
// delete-portal and has not come back since.
func (s *cloudBackfillStore) isPortalDeletedLocally(ctx context.Context, portalID string) (bool, error) {
	var ts int64
	err := s.db.QueryRow(ctx,
		`SELECT completed_ts FROM cloud_maintenance WHERE login_id=$1 AND task=$2`,
		s.loginID, localDeleteTask(portalID),
	).Scan(&ts)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *cloudBackfillStore) clearPortalDeletedLocally(ctx context.Context, portalID string) error {
	_, err := s.db.Exec(ctx,
		`DELETE FROM cloud_maintenance WHERE login_id=$1 AND task=$2`,
		s.loginID, localDeleteTask(portalID),
	)
	return err
}

// recordPortalDeletedLocally writes the deletion of a portal into the cloud
// store: a tombstone chat row when the chat has none, every chat and message
// row of the portal soft-deleted, any restore override removed, and the local
// delete marker set. After it the portal is in listDeletedPortalIDs and out of
// listPortalIDsWithNewestTimestamp.
func (s *cloudBackfillStore) recordPortalDeletedLocally(ctx context.Context, portalID string) error {
	if err := s.clearRestoreOverride(ctx, portalID); err != nil {
		return fmt.Errorf("failed to clear restore override: %w", err)
	}
	// ON CONFLICT DO NOTHING inside: a chat that has rows keeps them, and the
	// soft-delete below marks them; a chat without any row gets a deleted one
	// so the deletion is on record at all.
	if err := s.ensureDeletedChatTombstoneByPortalID(ctx, portalID); err != nil {
		return fmt.Errorf("failed to record chat tombstone: %w", err)
	}
	if err := s.deleteLocalChatByPortalID(ctx, portalID); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO cloud_maintenance (login_id, task, completed_ts)
		VALUES ($1, $2, $3)
		ON CONFLICT (login_id, task) DO UPDATE SET completed_ts=excluded.completed_ts
	`, s.loginID, localDeleteTask(portalID), time.Now().UnixMilli())
	return err
}

// recordPortalDeletedLocally is the client side of the delete-portal command.
func (c *IMClient) recordPortalDeletedLocally(ctx context.Context, portalID string) error {
	c.trackDeletedChat(portalID)
	if c.cloudStore == nil {
		return nil
	}
	return c.cloudStore.recordPortalDeletedLocally(ctx, portalID)
}

// cmdDeletePortal replaces bridgev2's delete-portal (same name, so registering
// it overrides the built-in): record the deletion, then run the built-in.
var cmdDeletePortal = &commands.FullHandler{
	Name: commands.CommandDeletePortal.Name,
	Func: fnDeletePortal,
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionAdmin,
		Description: "Delete the current portal room and keep it deleted across restarts. Nothing is deleted on Apple's side; a new message or restore-chat brings the chat back.",
	},
	RequiresAdmin:  true,
	RequiresPortal: true,
}

func fnDeletePortal(ce *commands.Event) {
	// A portal of another network type, or of a login that is not loaded, has
	// no cloud store to write to; the built-in behaviour is all there is.
	if login := ce.Bridge.GetCachedUserLoginByID(ce.Portal.Receiver); login != nil {
		if client, ok := login.Client.(*IMClient); ok && client != nil {
			// Fail closed, like the other delete paths: if the deletion cannot
			// be recorded, deleting the room would only have it re-created.
			if err := client.recordPortalDeletedLocally(ce.Ctx, string(ce.Portal.ID)); err != nil {
				ce.Reply("Failed to record the deletion, so the room was left in place: %v", err)
				return
			}
		}
	}
	commands.CommandDeletePortal.Func(ce)
}

// cmdDeleteAllPortals replaces bridgev2's delete-all-portals the same way:
// every portal's deletion is recorded before the built-in deletes the rooms.
var cmdDeleteAllPortals = &commands.FullHandler{
	Name: commands.CommandDeleteAllPortals.Name,
	Func: fnDeleteAllPortals,
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionAdmin,
		Description: "Delete all portals the bridge knows about and keep them deleted across restarts. Nothing is deleted on Apple's side.",
	},
	RequiresAdmin: true,
}

// portalDeletionRecorder is what recordPortalDeletions needs from a login's
// client; *IMClient implements it.
type portalDeletionRecorder interface {
	recordPortalDeletedLocally(ctx context.Context, portalID string) error
}

// recordPortalDeletions records the deletion of each portal with the recorder
// of its login. recorderFor returns nil for a portal whose login is not loaded
// or is not an iMessage login; such portals are skipped. It stops at the first
// failure and reports how many deletions were recorded.
func recordPortalDeletions(ctx context.Context, portals []networkid.PortalKey, recorderFor func(networkid.UserLoginID) portalDeletionRecorder) (recorded int, err error) {
	for _, key := range portals {
		recorder := recorderFor(key.Receiver)
		if recorder == nil {
			continue
		}
		if err = recorder.recordPortalDeletedLocally(ctx, string(key.ID)); err != nil {
			return recorded, fmt.Errorf("portal %s: %w", key.ID, err)
		}
		recorded++
	}
	return recorded, nil
}

func fnDeleteAllPortals(ce *commands.Event) {
	portals, err := ce.Bridge.GetAllPortals(ce.Ctx)
	if err != nil {
		ce.Reply("Failed to get portals: %v", err)
		return
	}
	keys := make([]networkid.PortalKey, 0, len(portals))
	for _, portal := range portals {
		keys = append(keys, portal.PortalKey)
	}
	_, err = recordPortalDeletions(ce.Ctx, keys, func(loginID networkid.UserLoginID) portalDeletionRecorder {
		if login := ce.Bridge.GetCachedUserLoginByID(loginID); login != nil {
			if client, ok := login.Client.(*IMClient); ok && client != nil {
				return client
			}
		}
		return nil
	})
	if err != nil {
		// Fail closed: rooms deleted without a recorded deletion come back.
		ce.Reply("Failed to record the deletions, so no room was deleted: %v", err)
		return
	}
	commands.CommandDeleteAllPortals.Func(ce)
}
