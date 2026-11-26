package connector

import (
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"
)

func rosterTestClient() *IMClient {
	return &IMClient{
		handle:              groupTestSelf,
		allHandles:          []string{groupTestSelf, "mailto:me@example.com"},
		UserLogin:           &bridgev2.UserLogin{UserLogin: &database.UserLogin{ID: testSQLLoginID}},
		imGroupParticipants: map[string][]string{},
	}
}

// A gid: group whose roster the bridge does not know (cloud_chat holds only a
// participantless synthetic row and nothing is cached) must not produce a
// full member list: bridgev2 treats a full list as the truth and removes
// every ghost missing from it, which here would be everyone but the owner.
func TestGroupChatMemberListUnknownRosterIsNotFull(t *testing.T) {
	c := rosterTestClient()
	members := c.groupChatMemberList(nil)
	if members.IsFull {
		t.Fatalf("unknown roster produced a full member list %v; bridgev2 would remove every ghost", members.MemberMap)
	}
	if _, ok := members.MemberMap[makeUserID(groupTestSelf)]; !ok {
		t.Fatalf("owner missing from member list %v", members.MemberMap)
	}
}

func TestGroupChatMemberListKnownRosterIsFull(t *testing.T) {
	c := rosterTestClient()
	members := c.groupChatMemberList([]string{groupTestAlice, groupTestBob})
	if !members.IsFull {
		t.Fatal("known roster should produce a full member list")
	}
	for _, h := range []string{groupTestSelf, groupTestAlice, groupTestBob} {
		m, ok := members.MemberMap[makeUserID(h)]
		if !ok || m.Membership != event.MembershipJoin {
			t.Fatalf("%s not joined in %v", h, members.MemberMap)
		}
	}
	if len(members.MemberMap) != 3 {
		t.Fatalf("member list = %v, want self, Alice and Bob", members.MemberMap)
	}
}

// rosterSyncEvents returns the member lists of the ChatInfoChange events the
// client queued through remoteEventOverride.
func rosterSyncEvents(t *testing.T, events []bridgev2.RemoteEvent) []*bridgev2.ChatMemberList {
	t.Helper()
	var out []*bridgev2.ChatMemberList
	for _, evt := range events {
		change, ok := evt.(*simplevent.ChatInfoChange)
		if !ok || change.ChatInfoChange == nil || change.ChatInfoChange.MemberChanges == nil {
			t.Fatalf("queued %T, want a ChatInfoChange with member changes", evt)
		}
		if change.GetPortalKey().ID != groupTestPortal {
			t.Fatalf("queued for portal %q, want %q", change.GetPortalKey().ID, groupTestPortal)
		}
		out = append(out, change.ChatInfoChange.MemberChanges)
	}
	return out
}

// A live message carries Apple's current roster for the group. A member on
// that roster whose ghost is not in the Matrix room (here: removed earlier by
// a resync that did not know the roster) must be brought back, without
// waiting for that member to send something. The sync only adds: removals
// arrive as participant-change messages, which handleParticipantChange syncs
// with a full list.
func TestLiveGroupRosterSyncsRoomMembers(t *testing.T) {
	c := rosterTestClient()
	var queued []bridgev2.RemoteEvent
	c.remoteEventOverride = func(evt bridgev2.RemoteEvent) { queued = append(queued, evt) }
	key := networkid.PortalKey{ID: groupTestPortal, Receiver: testSQLLoginID}

	c.syncLiveGroupRoomMembers(key, []string{groupTestAlice, groupTestSelf, groupTestBob})
	lists := rosterSyncEvents(t, queued)
	if len(lists) != 1 {
		t.Fatalf("queued %d member syncs, want 1", len(lists))
	}
	if lists[0].IsFull {
		t.Fatal("a live roster sync must only add members, not remove them")
	}
	if !lists[0].ExcludeChangesFromTimeline {
		t.Fatal("a live roster sync should stay out of the timeline")
	}
	for _, h := range []string{groupTestSelf, groupTestAlice, groupTestBob} {
		if m, ok := lists[0].MemberMap[makeUserID(h)]; !ok || m.Membership != event.MembershipJoin {
			t.Fatalf("%s not joined in %v", h, lists[0].MemberMap)
		}
	}

	// The same roster again (any order, any spelling) queues nothing.
	c.syncLiveGroupRoomMembers(key, []string{groupTestBob, groupTestAlice, groupTestSelf})
	if len(queued) != 1 {
		t.Fatalf("unchanged roster queued %d syncs in total, want 1", len(queued))
	}

	// A changed roster syncs again.
	c.syncLiveGroupRoomMembers(key, []string{groupTestSelf, groupTestAlice, groupTestBob, groupTestCarol})
	lists = rosterSyncEvents(t, queued)
	if len(lists) != 2 {
		t.Fatalf("changed roster: %d syncs in total, want 2", len(lists))
	}
	if _, ok := lists[1].MemberMap[makeUserID(groupTestCarol)]; !ok {
		t.Fatalf("Carol missing from %v", lists[1].MemberMap)
	}
}

func TestLiveGroupRosterSyncSkips(t *testing.T) {
	c := rosterTestClient()
	var queued []bridgev2.RemoteEvent
	c.remoteEventOverride = func(evt bridgev2.RemoteEvent) { queued = append(queued, evt) }

	// Legacy participant-keyed portals and DMs carry their members in the ID.
	c.syncLiveGroupRoomMembers(networkid.PortalKey{ID: groupTestAlice + "," + groupTestBob}, []string{groupTestSelf, groupTestAlice, groupTestBob})
	c.syncLiveGroupRoomMembers(networkid.PortalKey{ID: groupTestAlice}, []string{groupTestSelf, groupTestAlice})
	// A roster of fewer than two is a partial or control-message list.
	c.syncLiveGroupRoomMembers(networkid.PortalKey{ID: groupTestPortal}, []string{groupTestAlice})
	// A roster of only the owner's own handles names nobody to add.
	c.syncLiveGroupRoomMembers(networkid.PortalKey{ID: groupTestPortal}, []string{groupTestSelf, "mailto:me@example.com"})
	if len(queued) != 0 {
		t.Fatalf("queued %d member syncs, want none", len(queued))
	}
}
