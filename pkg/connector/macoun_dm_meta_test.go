package connector

import (
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

func dmPortal(name string, nameSet bool, avatarID string, avatarSet bool) *bridgev2.Portal {
	return &bridgev2.Portal{Portal: &database.Portal{
		RoomType:  database.RoomTypeDM,
		Name:      name,
		NameSet:   nameSet,
		AvatarID:  networkid.AvatarID(avatarID),
		AvatarSet: avatarSet,
	}}
}

func TestDMRoomMetaUnsent(t *testing.T) {
	cases := []struct {
		label string
		p     *bridgev2.Portal
		want  bool
	}{
		// The state a cancelled m.room.name send leaves behind.
		{"name chosen, send failed", dmPortal("Sam", false, "", true), true},
		{"name in the room", dmPortal("Sam", true, "", true), false},
		// Never named: left on implicit naming.
		{"pristine", dmPortal("", false, "", false), false},
		{"avatar chosen, send failed", dmPortal("Sam", true, "contact:tel:+15555550123:abcd", false), true},
		{"no photo", dmPortal("Sam", true, "", false), false},
	}

	for _, tc := range cases {
		if got := dmRoomMetaUnsent(tc.p); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.label, got, tc.want)
		}
	}
}
