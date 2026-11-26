// macoun-matrix - A Matrix-iMessage puppeting bridge (fork of corten-matrix).
// SPDX-License-Identifier: MPL-2.0

package connector

import "testing"

// A group room created before its members were known was named "Group Chat"
// and kept that name after the roster arrived (seen on a live bridge: a
// three-person iMessage group, named "Group Chat" for months
// while its cloud_chat row lists both other members).
func TestGroupNameRefreshReplacesPlaceholder(t *testing.T) {
	if !planGroupNameRefresh("Group Chat", "Sam Rivera, Alex Chen", false) {
		t.Fatal(`a built name should replace the "Group Chat" placeholder`)
	}
}

// With no members to name the group by, there is no name to give it: the
// room is created unnamed and named once the roster is known.
func TestBuildGroupNameWithoutMembersIsEmpty(t *testing.T) {
	if got := (&IMClient{}).buildGroupName(nil); got != "" {
		t.Fatalf("buildGroupName(nil) = %q, want empty", got)
	}
}
