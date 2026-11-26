package connector

import (
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
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
