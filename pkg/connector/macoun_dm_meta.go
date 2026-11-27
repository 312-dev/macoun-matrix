package connector

import "maunium.net/go/mautrix/bridgev2"

// dmRoomMetaUnsent reports a DM whose name or avatar the bridge chose but
// never got into the room. bridgev2 (v0.28 ghost.go updateDMPortals) pushes a
// ghost's name and avatar to its DM rooms in a goroutine that keeps the
// caller's context. When the ghost is first named while its DM room is being
// created, that context is room creation's, which is cancelled as soon as
// creation returns, so the m.room.name send fails with "context canceled".
// updateName then holds the name in memory with NameSet=false and retries only
// when the ghost changes again, which for a contact whose name is settled is
// never; the room stays unnamed and clients show its member list, bot
// included. Queueing a ChatInfoChange for the portal runs UpdateInfo, which
// ends in UpdateInfoFromGhost and resends both.
func dmRoomMetaUnsent(portal *bridgev2.Portal) bool {
	return (!portal.NameSet && portal.Name != "") || (!portal.AvatarSet && portal.AvatarID != "")
}
