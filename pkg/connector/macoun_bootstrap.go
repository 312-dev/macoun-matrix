// macoun-matrix - A Matrix-iMessage puppeting bridge (fork of corten-matrix).
// SPDX-License-Identifier: MPL-2.0
//
// macoun: let message flow survive a CloudKit bootstrap that cannot finish.
//
// Upstream keeps the APNs gate closed until the startup CloudKit sync
// succeeds: inbound messages wait in the in-memory reorder buffer and new
// portals are not created. In macoun-matrix every CloudKit call needs anisette
// headers from the Mac relay, so a bridge restart while the Mac is asleep or
// unreachable would hold every inbound message in memory for as long as the
// Mac is away, and lose them if the bridge restarts again.
//
// Upstream's reason for the closed gate is that, before tombstones are known,
// an APNs echo can re-create a portal for a deleted chat. On an install that
// has completed a sync before, the bootstrap has already reloaded the deleted
// portals recorded in the database (listDeletedPortalIDs, ahead of the retry
// loop), so that risk shrinks to chats deleted in iCloud since the last
// successful sync. After a few failed attempts the gate is opened on such an
// install; on a fresh install (no sync state) the upstream behaviour is kept.
//
// The bootstrap loop keeps retrying after the gate opens, and the post-sync
// steps (portal creation, recycle-bin seeding, backfill) still run when it
// succeeds. Two side effects: a panic inside a later bootstrap attempt takes
// the controller's recover path (bridge stays up, CloudKit sync stops until a
// restart), and live messages in chats whose portals are created by that later
// pass can land before their backfilled history.

package connector

import (
	"context"

	"github.com/rs/zerolog"
)

// macounBootstrapFailOpenAfter is how many failed bootstrap attempts (one per
// cloudSyncRetryInterval, so about this many minutes) to wait before opening
// the gate. Long enough to ride out a transient CloudKit error, short enough
// that a held message is not noticeably late.
const macounBootstrapFailOpenAfter = 3

// macounFailOpenDecision is the pure part of macounShouldFailOpen.
func macounFailOpenDecision(failures int, hasPriorSync bool) bool {
	return failures >= macounBootstrapFailOpenAfter && hasPriorSync
}

func (c *IMClient) macounShouldFailOpen(ctx context.Context, log zerolog.Logger, failures int) bool {
	if failures < macounBootstrapFailOpenAfter || c.cloudStore == nil {
		return false
	}
	prior, err := c.cloudStore.hasAnySyncState(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("macoun: could not check for earlier CloudKit sync state; keeping the APNs gate closed")
		return false
	}
	return macounFailOpenDecision(failures, prior)
}
