// macoun-matrix - A Matrix-iMessage puppeting bridge (fork of corten-matrix).
// SPDX-License-Identifier: MPL-2.0

package connector

import (
	"context"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
)

// A group room created before its members were known was named "Group Chat"
// and kept that name after the roster arrived (seen on a live bridge: a
// three-person iMessage group, named "Group Chat" for months
// while its cloud_chat row lists both other members).
func TestGroupNameRefreshReplacesPlaceholder(t *testing.T) {
	rename, record := planGroupNameRefresh("Group Chat", "Sam Rivera, Alex Chen", false, &PortalMetadata{})
	if !rename || !record {
		t.Fatalf(`placeholder: rename=%v record=%v, want both true`, rename, record)
	}
}

// With no members to name the group by, there is no name to give it: the
// room is created unnamed and named once the roster is known.
func TestBuildGroupNameWithoutMembersIsEmpty(t *testing.T) {
	if got := (&IMClient{}).buildGroupName(nil); got != "" {
		t.Fatalf("buildGroupName(nil) = %q, want empty", got)
	}
}

func TestGroupNameRefreshCases(t *testing.T) {
	built := &PortalMetadata{GeneratedName: "Alex Chen, Sam Rivera"}
	cases := []struct {
		name                   string
		current, newName       string
		authoritative          bool
		meta                   *PortalMetadata
		wantRename, wantRecord bool
	}{
		{"unnamed room gets a built name", "", "Alex Chen", false, nil, true, true},
		{"built name follows the roster", "Alex Chen, Sam Rivera", "Alex Chen, Sam Rivera, Jo Park", false, built, true, true},
		{"name the user chose is kept", "Weekend trip", "Alex Chen, Sam Rivera", false, built, false, false},
		{"carrier group's own name is kept", "Book Club", "Alex Chen, Sam Rivera, Jo Park", false, &PortalMetadata{}, false, false},
		{"participant's name replaces a chosen one", "Weekend trip", "Book Club", true, nil, true, true},
		{"older room already named after its members is recorded", "Alex Chen, Sam Rivera", "Alex Chen, Sam Rivera", false, &PortalMetadata{}, false, true},
		{"recorded and unchanged does nothing", "Alex Chen, Sam Rivera", "Alex Chen, Sam Rivera", false, built, false, false},
		{"participant names the group as the built name: record cleared", "Alex Chen, Sam Rivera", "Alex Chen, Sam Rivera", true, built, false, true},
		{"nothing to build", "Group Chat", "", false, nil, false, false},
	}
	for _, tc := range cases {
		rename, record := planGroupNameRefresh(tc.current, tc.newName, tc.authoritative, tc.meta)
		if rename != tc.wantRename || record != tc.wantRecord {
			t.Errorf("%s: rename=%v record=%v, want %v %v", tc.name, rename, record, tc.wantRename, tc.wantRecord)
		}
	}
}

func TestRecordGeneratedGroupNameRunsNextAndStores(t *testing.T) {
	ran := false
	next := func(context.Context, *bridgev2.Portal) bool { ran = true; return false }
	p := &bridgev2.Portal{Portal: &database.Portal{Metadata: &PortalMetadata{SenderGuid: "G"}}}
	if !recordGeneratedGroupName("Alex Chen", next)(context.Background(), p) {
		t.Fatal("storing a new name should report a change")
	}
	meta := p.Metadata.(*PortalMetadata)
	if !ran || meta.GeneratedName != "Alex Chen" || meta.SenderGuid != "G" {
		t.Fatalf("ran=%v meta=%+v", ran, meta)
	}
	if recordGeneratedGroupName("Alex Chen", nil)(context.Background(), p) {
		t.Fatal("storing the same name again should report no change")
	}
}
