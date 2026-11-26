// macoun-matrix - A Matrix-iMessage puppeting bridge (fork of corten-matrix).
// SPDX-License-Identifier: MPL-2.0
//
// macoun: how the periodic contact sync decides whether a group room's name
// changes. Names the bridge sets are local to the homeserver: bridgev2 drops
// the bridge bot's own m.room.name events before they reach
// HandleMatrixRoomName, so none of this is ever sent to Apple.

package connector

// planGroupNameRefresh reports whether refreshGroupPortalNamesFromContacts
// should set newName on a group portal whose room is called current.
// authoritative is resolveGroupName's second result: true for a name a
// participant gave the group, false for one built from the members.
func planGroupNameRefresh(current, newName string, authoritative bool) (rename bool) {
	if newName == "" || newName == current {
		return false
	}
	// Don't overwrite an existing portal name with a contact-derived
	// fallback: only authoritative sources (user-set iMessage group names
	// from the in-memory cache or CloudKit) rename.
	if !authoritative && current != "" {
		return false
	}
	return true
}
