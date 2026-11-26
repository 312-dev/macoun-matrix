// macoun-matrix - A Matrix-iMessage puppeting bridge (fork of corten-matrix).
// SPDX-License-Identifier: MPL-2.0
//
// macoun: how the periodic contact sync decides whether a group room's name
// changes. Names the bridge sets are local to the homeserver: bridgev2 drops
// the bridge bot's own m.room.name events before they reach
// HandleMatrixRoomName, so none of this is ever sent to Apple.
//
// A group nobody has named gets a name built from its members ("Sam
// Rivera, Alex Chen"). The bridge records that name in
// PortalMetadata.GeneratedName, and while the room still carries it, later
// syncs replace it as the roster or contact names change. A name a
// participant gives the group, or one the user sets from Matrix, is not the
// recorded name, so it is never overwritten by a built one.

package connector

import (
	"context"

	"maunium.net/go/mautrix/bridgev2"
)

// groupNamePlaceholder is the name earlier builds gave a group whose members
// were not known yet. A room still called this is treated as unnamed.
const groupNamePlaceholder = "Group Chat"

// planGroupNameRefresh reports what refreshGroupPortalNamesFromContacts does
// with a group portal whose room is called current. newName and
// authoritative are resolveGroupName's results: authoritative is true for a
// name a participant gave the group, false for one built from the members.
// rename means set newName on the room. record means PortalMetadata.
// GeneratedName must be rewritten: to newName when that is a built name, or
// cleared when the group has a participant's name. A room named before
// GeneratedName existed, whose name is exactly what the bridge builds today,
// gets record without rename.
func planGroupNameRefresh(current, newName string, authoritative bool, meta *PortalMetadata) (rename, record bool) {
	if newName == "" {
		return false, false
	}
	if newName != current {
		if !authoritative && !groupNameIsBridgeBuilt(current, meta) {
			// A name someone chose (from Matrix, or a carrier group's
			// own name) is not replaced by a built one.
			return false, false
		}
		return true, true
	}
	want := ""
	if !authoritative {
		want = newName
	}
	have := ""
	if meta != nil {
		have = meta.GeneratedName
	}
	return false, want != have
}

// groupNameIsBridgeBuilt reports whether a group room's name is one the bridge
// chose itself and may replace: none, the old placeholder, or the name it
// last built.
func groupNameIsBridgeBuilt(current string, meta *PortalMetadata) bool {
	if current == "" || current == groupNamePlaceholder {
		return true
	}
	return meta != nil && meta.GeneratedName != "" && current == meta.GeneratedName
}

// recordGeneratedGroupName returns an ExtraUpdates step that stores the name
// the bridge built (or clears the record when the name is a participant's),
// running next after if one is given.
func recordGeneratedGroupName(generated string, next bridgev2.ExtraUpdater[*bridgev2.Portal]) bridgev2.ExtraUpdater[*bridgev2.Portal] {
	return func(ctx context.Context, p *bridgev2.Portal) bool {
		changed := false
		if next != nil {
			changed = next(ctx, p)
		}
		meta, ok := p.Metadata.(*PortalMetadata)
		if !ok {
			meta = &PortalMetadata{}
		}
		if meta.GeneratedName != generated {
			meta.GeneratedName = generated
			p.Metadata = meta
			changed = true
		}
		return changed
	}
}
