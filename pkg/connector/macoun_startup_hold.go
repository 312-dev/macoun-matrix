// macoun-matrix - A Matrix-iMessage puppeting bridge (fork of corten-matrix).
// SPDX-License-Identifier: MPL-2.0
//
// macoun: scope the startup hold of the APNs reorder buffer to the chats that
// need it.
//
// While the startup CloudKit sync runs, upstream holds every incoming message,
// tapback and edit in the reorder buffer (messageBuffer) and releases them all
// when the sync and the portal pass finish. Typing notifications are not
// buffered, so during a long startup the user sees "typing" and then nothing.
//
// The hold protects three things:
//
//  1. Ordering. Forward backfill inserts CloudKit history after the newest
//     message already in a room and skips everything older. A live message
//     that reaches a room first would become that newest message, and the
//     history the backfill was about to insert would be skipped for good.
//  2. Deleted chats. Until this run has loaded the deletions it can learn
//     (stored tombstones, CloudKit chat tombstones, the recycle bin), an APNs
//     replay could put content into, or re-create, a chat the user deleted.
//  3. New rooms. A chat whose room does not exist yet is created by the portal
//     pass, with its history first; on a first login that is every chat.
//
// None of these applies to a chat that already has its room, is not deleted,
// and for which this run's CloudKit sync brought no history newer than what
// the room already shows. Once the sync and the deleted-chat seed are done
// (scopeStartupHold), messages for such chats leave the buffer through its
// normal quiet window. Everything else stays held exactly as before, until
// setCloudSyncDone or onForwardBackfillDone flush the buffer.
//
// The decision is made per message at the time it is looked at, not from a
// list computed once, so a chat whose backfill completes during the portal
// pass stops being held from then on.

package connector

import (
	"context"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"github.com/lrhodin/corten-matrix/pkg/rustpushgo"
)

// startupHoldFacts is what the hold decision needs to know about the chat a
// buffered message belongs to.
type startupHoldFacts struct {
	// PortalKnown is false when the message could not be tied to a chat.
	PortalKnown bool
	// HasRoom: the chat has a Matrix room.
	HasRoom bool
	// Deleted: the chat is in recentlyDeletedPortals or soft-deleted in the
	// cloud store.
	Deleted bool
	// LookupFailed: one of the lookups errored; the facts are not reliable.
	LookupFailed bool
	// NewestCloud is the timestamp of the newest live CloudKit message stored
	// for the chat, zero when there is none.
	NewestCloud time.Time
	// HasBridged and NewestBridged describe the newest message in the room.
	HasBridged    bool
	NewestBridged time.Time
}

// startupHoldDecision reports whether a message must stay in the reorder
// buffer until the startup pass finishes, and why. scoped is false until this
// run's CloudKit sync and deleted-chat seed have completed; before that
// nothing is known and everything is held.
//
// The forward-backfill test is the one bridgev2 applies when it handles the
// ChatResync the portal pass queues (simplevent.ChatResync.CheckNeedsBackfill):
// a backfill runs when CloudKit's newest message is later than the newest
// message in the room, or when the room has no message at all.
func startupHoldDecision(scoped bool, f startupHoldFacts) (hold bool, reason string) {
	switch {
	case !scoped:
		return true, "startup sync has not finished"
	case !f.PortalKnown:
		return true, "chat unknown"
	case f.LookupFailed:
		return true, "chat state lookup failed"
	case f.Deleted:
		return true, "chat is deleted"
	case !f.HasRoom:
		return true, "chat has no room yet"
	case f.NewestCloud.IsZero():
		return false, ""
	case !f.HasBridged || f.NewestCloud.After(f.NewestBridged):
		return true, "forward backfill pending"
	}
	return false, ""
}

// bufferedMessagePortalKey resolves the chat of a buffered message the way its
// handler will: tapbacks and edits by their target message first (handleTapback,
// handleEdit), everything else through makePortalKey and the SMS group redirect
// (handleMessage).
func (c *IMClient) bufferedMessagePortalKey(log zerolog.Logger, msg rustpushgo.WrappedMessage) (networkid.PortalKey, bool) {
	var portalKey networkid.PortalKey
	switch {
	case msg.IsTapback:
		portalKey = c.resolvePortalByTargetMessage(log, ptrStringOr(msg.TapbackTargetUuid, ""))
	case msg.IsEdit:
		portalKey = c.resolvePortalByTargetMessage(log, ptrStringOr(msg.EditTargetUuid, ""))
	}
	if portalKey.ID == "" {
		portalKey = c.makePortalKey(msg.Participants, msg.GroupName, msg.Sender, msg.SenderGuid, msg.IsSms)
		if !msg.IsTapback && !msg.IsEdit && msg.IsSms && isComputedDMPortalID(portalKey.ID) {
			if groupKey, ok := c.resolveSMSGroupRedirectPortal(msg); ok {
				portalKey = groupKey
			}
		}
	}
	if portalKey.ID == "" || portalKey.ID == "unknown" {
		return networkid.PortalKey{}, false
	}
	return portalKey, true
}

func (c *IMClient) startupHoldFactsFor(ctx context.Context, portalKey networkid.PortalKey) startupHoldFacts {
	facts := startupHoldFacts{PortalKnown: true}
	portalID := string(portalKey.ID)

	c.recentlyDeletedPortalsMu.RLock()
	_, facts.Deleted = c.recentlyDeletedPortals[portalID]
	c.recentlyDeletedPortalsMu.RUnlock()
	if facts.Deleted {
		return facts
	}

	portal, err := c.Main.Bridge.GetExistingPortalByKey(ctx, portalKey)
	if err != nil {
		facts.LookupFailed = true
		return facts
	}
	facts.HasRoom = portal != nil && portal.MXID != ""
	if !facts.HasRoom || c.cloudStore == nil {
		return facts
	}

	info, err := c.cloudStore.getSoftDeletedPortalInfo(ctx, portalID)
	if err != nil {
		facts.LookupFailed = true
		return facts
	}
	if info.Deleted {
		facts.Deleted = true
		return facts
	}

	newestCloudMS, err := c.cloudStore.newestLiveCloudMessageTS(ctx, portalID)
	if err != nil {
		facts.LookupFailed = true
		return facts
	}
	if newestCloudMS <= 0 {
		return facts
	}
	facts.NewestCloud = time.UnixMilli(newestCloudMS)

	// Same lookup bridgev2 makes before CheckNeedsBackfill.
	last, err := c.Main.Bridge.DB.Message.GetLastPartAtOrBeforeTime(ctx, portalKey, time.Now().Add(10*time.Second))
	if err != nil {
		facts.LookupFailed = true
		return facts
	}
	if last != nil {
		facts.HasBridged = true
		facts.NewestBridged = last.Timestamp
	}
	return facts
}

// startupHoldApplies reports whether msg must stay in the reorder buffer
// until the startup pass finishes. Only meaningful while !isCloudSyncDone().
func (c *IMClient) startupHoldApplies(msg rustpushgo.WrappedMessage) bool {
	if !c.startupHoldScoped.Load() {
		return true
	}
	log := c.UserLogin.Log.With().Str("component", "imessage").Str("msg_uuid", msg.Uuid).Logger()
	portalKey, ok := c.bufferedMessagePortalKey(log, msg)
	facts := startupHoldFacts{}
	if ok {
		facts = c.startupHoldFactsFor(context.Background(), portalKey)
	}
	hold, reason := startupHoldDecision(true, facts)
	if hold {
		log.Debug().Str("portal_id", string(portalKey.ID)).Str("reason", reason).
			Msg("Holding message until the startup pass finishes")
	}
	return hold
}

// scopeStartupHold narrows the startup hold from "every message" to "messages
// for chats that still need the startup pass" and releases what the buffer
// already holds for the other chats. Call it only after this run's CloudKit
// sync has completed and the deleted-chat seed has run: the decision reads the
// CloudKit rows that sync stored and the deletions the seed recorded.
func (c *IMClient) scopeStartupHold(log zerolog.Logger) {
	if c.isCloudSyncDone() {
		return
	}
	c.startupHoldScoped.Store(true)
	if c.msgBuffer == nil {
		return
	}
	released, stillHeld := c.msgBuffer.releaseUnheld()
	log.Info().Int("released", released).Int("still_held", stillHeld).
		Msg("Startup hold now applies only to chats that are deleted, have no room yet, or have CloudKit history waiting for forward backfill")
}
