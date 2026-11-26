// macoun-matrix - A Matrix-iMessage puppeting bridge (fork of corten-matrix).
// SPDX-License-Identifier: MPL-2.0
//
// macoun: bot commands for iMessage groups.
//
// Renaming a group and setting its photo work through the room's own name and
// avatar (macoun_groups.go). Membership does not: every portal room keeps
// invite and kick locked for Matrix users (hardenedPowerLevels), so that
// nobody can pull an unbridged Matrix user into a bridged conversation, and a
// failed invite cannot be undone on an ordinary homeserver. These commands are
// the way to change membership from any Matrix client:
//
//	new-group <identifier> <identifier> [...] [name ...]   anywhere
//	group-add <identifier>                                 in the group's room
//	group-remove <identifier>                              in the group's room
//	group-leave                                            in the group's room
//
// An identifier is a phone number or an email address, as for start-chat,
// written without spaces.
// After a change the room's members are resynced from the stored participant
// list, so the ghost joins or leaves the room.

package connector

import (
	"strings"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/provisionutil"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"
)

var cmdNewGroup = &commands.FullHandler{
	Name: "new-group",
	Func: fnNewGroup,
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "Create an iMessage group with two or more people. Everyone must be reachable over iMessage. Nothing is sent until you write the first message.",
		Args:        "<_phone or email_> <_phone or email_> [...] [name _group name_]",
	},
	RequiresLogin: true,
}

var cmdGroupAdd = &commands.FullHandler{
	Name: "group-add",
	Func: func(ce *commands.Event) { fnGroupMembership(ce, bridgev2.Invite) },
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "Add someone to this iMessage group.",
		Args:        "<_phone or email_>",
	},
	RequiresLogin:  true,
	RequiresPortal: true,
}

var cmdGroupRemove = &commands.FullHandler{
	Name: "group-remove",
	Func: func(ce *commands.Event) { fnGroupMembership(ce, bridgev2.Kick) },
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "Remove someone from this iMessage group. iMessage allows it while at least three people remain.",
		Args:        "<_phone or email_>",
	},
	RequiresLogin:  true,
	RequiresPortal: true,
}

var cmdGroupLeave = &commands.FullHandler{
	Name: "group-leave",
	Func: func(ce *commands.Event) { fnGroupMembership(ce, bridgev2.Leave) },
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "Leave this iMessage group. iMessage allows it while at least three other people remain. The room stays; remove it with delete-portal.",
	},
	RequiresLogin:  true,
	RequiresPortal: true,
}

// parseNewGroupArgs splits the arguments of new-group into participant
// identifiers and an optional name: everything after the word "name" is the
// name. Identifiers are normalized like start-chat's.
func parseNewGroupArgs(args []string) (participants []networkid.UserID, name string) {
	for i, arg := range args {
		if strings.EqualFold(arg, "name") {
			name = strings.TrimSpace(strings.Join(args[i+1:], " "))
			break
		}
		if identifier := normalizeStartChatIdentifier(arg); identifier != "" {
			participants = append(participants, networkid.UserID(identifier))
		}
	}
	return participants, name
}

func fnNewGroup(ce *commands.Event) {
	login := ce.User.GetDefaultLogin()
	if login == nil {
		ce.Reply("You're not signed in to iMessage. Run `$cmdprefix login` first.")
		return
	}
	participants, name := parseNewGroupArgs(ce.Args)
	if len(participants) < groupMinOthers {
		ce.Reply("Usage: `$cmdprefix new-group <phone or email> <phone or email> [...] [name <group name>]` with at least two other people.")
		return
	}
	params := &bridgev2.GroupCreateParams{Type: groupCreateType, Participants: participants}
	if name != "" {
		params.Name = &event.RoomNameEventContent{Name: name}
	}
	// provisionutil.CreateGroup is what the provisioning API calls: it checks
	// the request against the advertised capabilities, calls CreateGroup and
	// creates the room.
	resp, err := provisionutil.CreateGroup(ce.Ctx, login, params)
	if err != nil {
		ce.Reply("Failed to create the group: %v", err)
		return
	}
	ce.Reply("Created the group: [%s](%s). The other people see it once you send the first message.", resp.ID, resp.MXID.URI().MatrixToURL())
}

func fnGroupMembership(ce *commands.Event, change bridgev2.MembershipChangeType) {
	login := ce.Bridge.GetCachedUserLoginByID(ce.Portal.Receiver)
	var client *IMClient
	if login != nil {
		client, _ = login.Client.(*IMClient)
	}
	if client == nil {
		ce.Reply("This room does not belong to a signed-in iMessage login.")
		return
	}
	target := ""
	if change != bridgev2.Leave {
		if len(ce.Args) != 1 {
			ce.Reply("Usage: `$cmdprefix %s <phone or email>`", ce.Command)
			return
		}
		target = normalizeStartChatIdentifier(ce.Args[0])
	}
	sent, err := client.changeGroupParticipants(ce.Ctx, ce.Portal, change, target)
	if err != nil {
		ce.Reply("Failed: %v", err)
		return
	}
	if !sent {
		ce.Reply("Nothing to change: the group already looks that way.")
		return
	}
	// Let the room's member list follow the stored participant list.
	portalKey := ce.Portal.PortalKey
	login.QueueRemoteEvent(&simplevent.ChatResync{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventChatResync,
			PortalKey: portalKey,
			LogContext: func(lc zerolog.Context) zerolog.Context {
				return lc.Str("source", "group_command")
			},
		},
		GetChatInfoFunc: client.GetChatInfo,
	})
	switch change {
	case bridgev2.Invite:
		ce.Reply("Added %s to the group.", target)
	case bridgev2.Kick:
		ce.Reply("Removed %s from the group.", target)
	default:
		ce.Reply("You left the group on iMessage. This room stays until you remove it with `$cmdprefix delete-portal`.")
	}
}
