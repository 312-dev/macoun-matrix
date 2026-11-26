// macoun-matrix - A Matrix-iMessage puppeting bridge (fork of corten-matrix).
// SPDX-License-Identifier: MPL-2.0
//
// macoun: iMessage groups from the Matrix side: create a group, rename it,
// set or clear its photo, add and remove participants, leave.
//
// The other direction already exists: handleRename, handleParticipantChange
// and handleIconChange turn Apple's group messages into Matrix room changes.
// rustpush can also send each of those messages (send_rename_group,
// send_change_participants, send_icon_change, send_icon_clear); this file
// connects Matrix room events to them.
//
// What an iMessage group is, as far as this code is concerned:
//
//   - a group id (a UUID, "sender_guid" on the wire). The portal id is
//     "gid:" + the lowercase UUID, and the original spelling is kept for
//     sending, because Apple compares it case-sensitively.
//   - a participant list that includes the user's own handle;
//   - optionally a name ("cv_name") and a photo.
//
// There is no "create group" request in the protocol. A group starts to exist
// on Apple's side when the first message carrying a new group id and its
// participant list arrives. CreateGroup therefore only sets the group up
// locally (portal, group id, participants, name), so that the first message
// the user sends from the new room is that first message, and so that replies,
// which carry the same group id, land in the same room (makePortalKey finds
// the existing gid: portal). Nothing is sent to the participants before the
// user writes something.
//
// Only iMessage groups are handled. Carrier (SMS/MMS/RCS) groups have no
// group messages to send, and their rooms refuse these changes. Membership
// changes are limited to gid: portals: a legacy portal whose id is its
// participant list would have to be re-keyed, which the incoming path does
// with ReIDPortal and this code does not attempt.

package connector

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"image"
	_ "image/gif"  // group photos set from Matrix may be GIF,
	_ "image/jpeg" // JPEG
	"image/png"    // or PNG; Apple gets a PNG.
	"strings"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/ptr"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // WebP avatars are common on Matrix.
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/lrhodin/corten-matrix/pkg/rustpushgo"
)

var (
	_ bridgev2.GroupCreatingNetworkAPI      = (*IMClient)(nil)
	_ bridgev2.RoomNameHandlingNetworkAPI   = (*IMClient)(nil)
	_ bridgev2.RoomAvatarHandlingNetworkAPI = (*IMClient)(nil)
	_ bridgev2.MembershipHandlingNetworkAPI = (*IMClient)(nil)
)

const (
	// groupCreateType is the only entry of the provisioning group_creation map.
	groupCreateType = "group"

	// groupMinOthers is the smallest number of other people in a group: with
	// one it is a direct chat.
	groupMinOthers = 2

	// groupMinMembersAfterRemoval: Apple's clients only offer "remove" and
	// "leave" while at least this many people, the user included, stay in the
	// group. Below that the conversation would stop being a group.
	groupMinMembersAfterRemoval = 3

	// groupPhotoSize is the edge of the square PNG Apple's clients exchange as
	// a group photo.
	groupPhotoSize = 570
)

var (
	errNotIMessageGroup      = errors.New("this is not an iMessage group chat")
	errCarrierGroup          = errors.New("carrier (SMS/MMS/RCS) groups cannot be changed from Matrix")
	errLegacyGroupMembers    = errors.New("this group's room is keyed by its participant list; changing its members from Matrix is not supported")
	errGroupTooSmall         = errors.New("iMessage only allows removing someone or leaving while at least three people remain in the group")
	errEmptyGroupName        = errors.New("an iMessage group name cannot be cleared from Matrix")
	errGroupParticipants     = errors.New("the group's participant list is not known yet; it is learned from the next message in the group")
	errUnsupportedMembership = errors.New("this membership change has no iMessage equivalent")
	errOnlyGhostInvites      = errors.New("only iMessage contacts can be added to an iMessage group")
)

// groupSender is the part of the rustpush client the Matrix-side group
// operations use. *rustpushgo.Client implements it.
type groupSender interface {
	SendRenameGroup(conversation rustpushgo.WrappedConversation, newName string, handle string) (string, error)
	SendChangeParticipants(conversation rustpushgo.WrappedConversation, newParticipants []string, groupVersion uint64, handle string) (string, error)
	SendIconChange(conversation rustpushgo.WrappedConversation, photoData []byte, groupVersion uint64, handle string) (string, error)
	SendIconClear(conversation rustpushgo.WrappedConversation, groupVersion uint64, handle string) (string, error)
	ValidateTargets(targets []string, handle string) []string
}

func (c *IMClient) groupOps() groupSender {
	if c.groupSenderOverride != nil {
		return c.groupSenderOverride
	}
	if c.client == nil {
		return nil
	}
	return c.client
}

// groupVersionNow is the group_version for a participant or photo change. It
// has to grow with every change; rustpush's own advice is the Unix time in
// seconds.
func groupVersionNow() uint64 {
	return uint64(time.Now().Unix())
}

// matrixGroupConversation returns the conversation to address a group message
// to, after checking that the portal is an iMessage group these operations can
// work on. The participant list always contains the user's own handle: group
// messages go to the user's other devices too, and cloud_chat rows that came
// from CloudKit leave the owner out.
func (c *IMClient) matrixGroupConversation(portal *bridgev2.Portal) (rustpushgo.WrappedConversation, error) {
	portalID := string(portal.ID)
	if !strings.HasPrefix(portalID, "gid:") && !strings.Contains(portalID, ",") {
		return rustpushgo.WrappedConversation{}, errNotIMessageGroup
	}
	if c.isPortalSMS(portalID) {
		return rustpushgo.WrappedConversation{}, errCarrierGroup
	}
	conv := c.portalToConversation(portal)
	if len(conv.Participants) == 0 {
		return rustpushgo.WrappedConversation{}, errGroupParticipants
	}
	conv.Participants = c.groupParticipantsWithSelf(conv.Participants)
	return conv, nil
}

// groupParticipantsWithSelf normalizes a participant list and makes sure the
// user's own handle is in it exactly once, as c.handle.
func (c *IMClient) groupParticipantsWithSelf(participants []string) []string {
	out := make([]string, 0, len(participants)+1)
	seen := make(map[string]bool, len(participants)+1)
	add := func(handle string) {
		if handle != "" && !seen[handle] {
			seen[handle] = true
			out = append(out, handle)
		}
	}
	for _, p := range participants {
		normalized := normalizeIdentifierForPortalID(p)
		if normalized == "" || c.isMyHandle(normalized) {
			continue
		}
		add(normalized)
	}
	add(normalizeIdentifierForPortalID(c.handle))
	return out
}

// setGroupMetadata stores the group id and name on the portal. The caller (or
// bridgev2, after a successful handler) saves the portal.
func setGroupMetadata(portal *bridgev2.Portal, senderGuid, groupName *string) {
	meta := &PortalMetadata{}
	if existing, ok := portal.Metadata.(*PortalMetadata); ok && existing != nil {
		*meta = *existing
	}
	if senderGuid != nil {
		meta.SenderGuid = *senderGuid
	}
	if groupName != nil {
		meta.GroupName = *groupName
	}
	portal.Metadata = meta
}

// HandleMatrixRoomName renames the iMessage group when the Matrix room is
// renamed. It mirrors handleRename: besides the room name, the cached cv_name
// (used to address every later message), the portal metadata and the
// cloud_chat display name all move to the new name.
func (c *IMClient) HandleMatrixRoomName(ctx context.Context, msg *bridgev2.MatrixRoomName) (bool, error) {
	ops := c.groupOps()
	if ops == nil {
		return false, bridgev2.ErrNotLoggedIn
	}
	newName := strings.TrimSpace(msg.Content.Name)
	if newName == "" {
		return false, errEmptyGroupName
	}
	conv, err := c.matrixGroupConversation(msg.Portal)
	if err != nil {
		return false, err
	}
	// conv still carries the old name: the rename message states both.
	if _, err = ops.SendRenameGroup(conv, newName, c.handle); err != nil {
		return false, fmt.Errorf("failed to rename the iMessage group: %w", err)
	}

	portalID := string(msg.Portal.ID)
	c.imGroupNamesMu.Lock()
	if c.imGroupNames == nil {
		c.imGroupNames = make(map[string]string)
	}
	c.imGroupNames[portalID] = newName
	c.imGroupNamesMu.Unlock()
	if c.cloudStore != nil {
		if err = c.cloudStore.updateDisplayNameByPortalID(ctx, portalID, newName); err != nil {
			zerolog.Ctx(ctx).Warn().Err(err).Str("portal_id", portalID).
				Msg("Failed to update cloud_chat display_name after a rename from Matrix")
		}
	}
	setGroupMetadata(msg.Portal, nil, &newName)
	msg.Portal.Name = newName
	msg.Portal.NameSet = true
	return true, nil
}

// groupPhotoPNG converts an image in any decodable format to the square PNG
// Apple's clients use for group photos: centre-cropped to a square, scaled to
// groupPhotoSize.
func groupPhotoPNG(data []byte) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("the room avatar is not an image this bridge can read: %w", err)
	}
	bounds := src.Bounds()
	side := bounds.Dx()
	if bounds.Dy() < side {
		side = bounds.Dy()
	}
	if side <= 0 {
		return nil, errors.New("the room avatar is an empty image")
	}
	x0 := bounds.Min.X + (bounds.Dx()-side)/2
	y0 := bounds.Min.Y + (bounds.Dy()-side)/2
	crop := image.Rect(x0, y0, x0+side, y0+side)
	dst := image.NewRGBA(image.Rect(0, 0, groupPhotoSize, groupPhotoSize))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, crop, draw.Src, nil)
	var out bytes.Buffer
	if err = png.Encode(&out, dst); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func (c *IMClient) downloadMatrixMedia(ctx context.Context, uri id.ContentURIString) ([]byte, error) {
	if c.matrixMediaOverride != nil {
		return c.matrixMediaOverride(ctx, uri)
	}
	return c.Main.Bridge.Bot.DownloadMedia(ctx, uri, nil)
}

// HandleMatrixRoomAvatar sets or clears the iMessage group photo when the
// Matrix room avatar changes. Like handleIconChange it keeps the photo in
// group_photo_cache under a timestamp, which is also the avatar id GetChatInfo
// reports, so a later resync does not replace the avatar the user just set.
func (c *IMClient) HandleMatrixRoomAvatar(ctx context.Context, msg *bridgev2.MatrixRoomAvatar) (bool, error) {
	ops := c.groupOps()
	if ops == nil {
		return false, bridgev2.ErrNotLoggedIn
	}
	conv, err := c.matrixGroupConversation(msg.Portal)
	if err != nil {
		return false, err
	}
	portalID := string(msg.Portal.ID)

	if msg.Content.URL == "" {
		if _, err = ops.SendIconClear(conv, groupVersionNow(), c.handle); err != nil {
			return false, fmt.Errorf("failed to clear the iMessage group photo: %w", err)
		}
		if c.cloudStore != nil {
			if err = c.cloudStore.clearGroupPhoto(ctx, portalID); err != nil {
				zerolog.Ctx(ctx).Warn().Err(err).Str("portal_id", portalID).Msg("Failed to clear cached group photo")
			}
		}
		msg.Portal.AvatarID = ""
		msg.Portal.AvatarMXC = ""
		msg.Portal.AvatarHash = [32]byte{}
		return true, nil
	}

	original, err := c.downloadMatrixMedia(ctx, msg.Content.URL)
	if err != nil {
		return false, fmt.Errorf("failed to download the room avatar: %w", err)
	}
	photo, err := groupPhotoPNG(original)
	if err != nil {
		return false, err
	}
	version := groupVersionNow()
	if _, err = ops.SendIconChange(conv, photo, version, c.handle); err != nil {
		return false, fmt.Errorf("failed to set the iMessage group photo: %w", err)
	}
	tsMS := time.Now().UnixMilli()
	if c.cloudStore != nil {
		if err = c.cloudStore.saveGroupPhoto(ctx, portalID, tsMS, photo); err != nil {
			zerolog.Ctx(ctx).Warn().Err(err).Str("portal_id", portalID).Msg("Failed to cache the group photo")
		}
	}
	// The room keeps the image the user uploaded; the hash is of that image,
	// which is what AvatarMXC points at.
	msg.Portal.AvatarID = networkid.AvatarID(fmt.Sprintf("icon-change:%d", tsMS))
	msg.Portal.AvatarMXC = msg.Content.URL
	msg.Portal.AvatarHash = sha256.Sum256(original)
	return true, nil
}

// groupMembershipPlan works out the participant list after a Matrix membership
// change. current must contain the user's own handle. target is the handle
// being invited or removed; it is ignored for a self-leave.
func groupMembershipPlan(change bridgev2.MembershipChangeType, current []string, self, target string) (next []string, err error) {
	without := func(handle string) []string {
		out := make([]string, 0, len(current))
		for _, p := range current {
			if p != handle {
				out = append(out, p)
			}
		}
		return out
	}
	switch change {
	case bridgev2.Invite:
		for _, p := range current {
			if p == target {
				return nil, nil // already a member; nothing to send
			}
		}
		return append(append([]string{}, current...), target), nil
	case bridgev2.Kick, bridgev2.BanJoined:
		next = without(target)
		if len(next) == len(current) {
			return nil, nil // not a member; nothing to send
		}
	case bridgev2.Leave:
		next = without(self)
	default:
		return nil, errUnsupportedMembership
	}
	if len(next) < groupMinMembersAfterRemoval {
		return nil, errGroupTooSmall
	}
	return next, nil
}

// HandleMatrixMembership adds or removes an iMessage group participant when a
// ghost is invited to or removed from the room, and leaves the group when the
// user leaves the room (bridgev2 only forwards that with bridge_matrix_leave
// enabled). Rooms keep invite and kick locked for Matrix users
// (hardenedPowerLevels), so on most homeservers these events only arrive as
// Beeper state requests; the group-add, group-remove and group-leave commands
// (macoun_group_commands.go) reach the same code from any client.
func (c *IMClient) HandleMatrixMembership(ctx context.Context, msg *bridgev2.MatrixMembershipChange) (*bridgev2.MatrixMembershipResult, error) {
	portalID := string(msg.Portal.ID)
	isGroup := strings.HasPrefix(portalID, "gid:") || strings.Contains(portalID, ",")
	if !isGroup && msg.Type == bridgev2.Leave {
		// Leaving the room of a direct chat changes nothing on Apple's side.
		return nil, nil
	}
	target := ""
	if msg.Type != bridgev2.Leave {
		ghost, ok := msg.Target.(*bridgev2.Ghost)
		if !ok || ghost == nil {
			return nil, errUnsupportedMembership
		}
		target = string(ghost.ID)
	}
	_, err := c.changeGroupParticipants(ctx, msg.Portal, msg.Type, target)
	return nil, err
}

// changeGroupParticipants applies one membership change (bridgev2.Invite, Kick
// or Leave) to an iMessage group and reports whether a change was sent.
// target is the handle being added or removed and is ignored for Leave. The
// stored participant list is updated at once, as handleParticipantChange does
// for a change made on Apple's side, so the next outgoing message goes to the
// new list.
func (c *IMClient) changeGroupParticipants(ctx context.Context, portal *bridgev2.Portal, change bridgev2.MembershipChangeType, target string) (sent bool, err error) {
	ops := c.groupOps()
	if ops == nil {
		return false, bridgev2.ErrNotLoggedIn
	}
	portalID := string(portal.ID)
	if !strings.HasPrefix(portalID, "gid:") && !strings.Contains(portalID, ",") {
		return false, errNotIMessageGroup
	}
	if !strings.HasPrefix(portalID, "gid:") {
		return false, errLegacyGroupMembers
	}
	conv, err := c.matrixGroupConversation(portal)
	if err != nil {
		return false, err
	}
	self := normalizeIdentifierForPortalID(c.handle)
	if change != bridgev2.Leave {
		target = normalizeIdentifierForPortalID(target)
		if target == "" || c.isMyHandle(target) {
			return false, errUnsupportedMembership
		}
	}

	next, err := groupMembershipPlan(change, conv.Participants, self, target)
	if err != nil || next == nil {
		return false, err
	}
	if change == bridgev2.Invite {
		if valid := ops.ValidateTargets([]string{target}, c.handle); len(valid) == 0 {
			return false, fmt.Errorf("%s cannot be reached over iMessage, so it cannot join an iMessage group", target)
		}
	}
	// conv holds the list before the change; rustpush addresses the message to
	// both lists, so someone being added is told too.
	if _, err = ops.SendChangeParticipants(conv, next, groupVersionNow(), c.handle); err != nil {
		return false, fmt.Errorf("failed to change the iMessage group's participants: %w", err)
	}
	if c.cloudStore != nil {
		if err = c.cloudStore.updateChatParticipants(ctx, portalID, next); err != nil {
			zerolog.Ctx(ctx).Warn().Err(err).Str("portal_id", portalID).
				Msg("Failed to store the participant list after a change from Matrix")
		}
	}
	c.imGroupParticipantsMu.Lock()
	if c.imGroupParticipants == nil {
		c.imGroupParticipants = make(map[string][]string)
	}
	c.imGroupParticipants[portalID] = next
	c.imGroupParticipantsMu.Unlock()
	return true, nil
}

// matrixMayEditGroupMeta reports whether Matrix users may send name and avatar
// events in the portal's room: only in iMessage groups, where
// HandleMatrixRoomName and HandleMatrixRoomAvatar can carry them to Apple.
func (c *IMClient) matrixMayEditGroupMeta(portal *bridgev2.Portal) bool {
	if portal == nil {
		return false
	}
	portalID := string(portal.ID)
	if !strings.HasPrefix(portalID, "gid:") && !strings.Contains(portalID, ",") {
		return false
	}
	return !c.isPortalSMS(portalID)
}

// syncGroupMetaLevels sets the power levels of the room name and room avatar
// events and reports whether it changed anything. Where editing is allowed the
// level is the room's users_default, so the bridge's user (who is never raised
// above it, see resetEscalatedUsers) can rename the group and set its photo.
// Elsewhere an entry that sits below state_default is removed, which puts the
// event back under state_default; rooms that never had an entry are left
// untouched, so this causes no power-level event in them.
func syncGroupMetaLevels(pl *event.PowerLevelsEventContent, allow bool) bool {
	if pl == nil {
		return false
	}
	changed := false
	for _, evtType := range []event.Type{event.StateRoomName, event.StateRoomAvatar} {
		level, has := pl.Events[evtType.Type]
		switch {
		case allow && (!has || level != pl.UsersDefault):
			if pl.Events == nil {
				pl.Events = make(map[string]int)
			}
			pl.Events[evtType.Type] = pl.UsersDefault
			changed = true
		case !allow && has && level < pl.StateDefault():
			delete(pl.Events, evtType.Type)
			changed = true
		}
	}
	return changed
}

// newGroupID returns a fresh group id: a random (version 4) UUID in the
// uppercase spelling Apple's clients use.
func newGroupID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%X-%X-%X-%X-%X", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// CreateGroup sets up a new iMessage group for the given participants and
// returns its portal; see the file comment for why nothing is sent yet. Every
// participant must be reachable over iMessage: one who is not would turn the
// conversation into a carrier group, which this bridge cannot start.
func (c *IMClient) CreateGroup(ctx context.Context, params *bridgev2.GroupCreateParams) (*bridgev2.CreateChatResponse, error) {
	ops := c.groupOps()
	if ops == nil {
		return nil, bridgev2.ErrNotLoggedIn
	}
	if params.Type != "" && params.Type != groupCreateType {
		return nil, fmt.Errorf("unknown group type %q", params.Type)
	}

	raw := make([]string, 0, len(params.Participants))
	for _, p := range params.Participants {
		raw = append(raw, string(p))
	}
	participants := c.groupParticipantsWithSelf(raw)
	others := participants[:len(participants)-1]
	if len(others) < groupMinOthers {
		return nil, fmt.Errorf("an iMessage group needs at least %d other people", groupMinOthers)
	}
	reachable := make(map[string]bool, len(others))
	for _, valid := range ops.ValidateTargets(others, c.handle) {
		reachable[normalizeIdentifierForPortalID(valid)] = true
	}
	var unreachable []string
	for _, other := range others {
		if !reachable[other] {
			unreachable = append(unreachable, other)
		}
	}
	if len(unreachable) > 0 {
		return nil, fmt.Errorf("not reachable over iMessage, so an iMessage group cannot include: %s", strings.Join(unreachable, ", "))
	}

	groupID, err := newGroupID()
	if err != nil {
		return nil, fmt.Errorf("failed to generate a group id: %w", err)
	}
	portalID := "gid:" + strings.ToLower(groupID)
	portalKey := networkid.PortalKey{ID: networkid.PortalID(portalID), Receiver: c.UserLogin.ID}
	name := ""
	if params.Name != nil {
		name = strings.TrimSpace(params.Name.Name)
	}

	// The participant list and group id must be in place before the room
	// exists: portalToConversation reads them for the first outgoing message,
	// and GetChatInfo reads the list for the room's members.
	if c.cloudStore != nil {
		var displayName *string
		if name != "" {
			displayName = &name
		}
		if err = c.cloudStore.upsertChat(ctx, groupID, "", groupID, portalID, "iMessage", displayName, nil, participants, time.Now().UnixMilli()); err != nil {
			return nil, fmt.Errorf("failed to store the new group: %w", err)
		}
	}
	c.imGroupGuidsMu.Lock()
	if c.imGroupGuids == nil {
		c.imGroupGuids = make(map[string]string)
	}
	c.imGroupGuids[portalID] = groupID
	c.imGroupGuidsMu.Unlock()
	c.imGroupParticipantsMu.Lock()
	if c.imGroupParticipants == nil {
		c.imGroupParticipants = make(map[string][]string)
	}
	c.imGroupParticipants[portalID] = participants
	c.imGroupParticipantsMu.Unlock()
	if name != "" {
		c.imGroupNamesMu.Lock()
		if c.imGroupNames == nil {
			c.imGroupNames = make(map[string]string)
		}
		c.imGroupNames[portalID] = name
		c.imGroupNamesMu.Unlock()
	}

	members := make(map[networkid.UserID]bridgev2.ChatMember, len(participants))
	for _, p := range participants {
		userID := makeUserID(p)
		member := bridgev2.ChatMember{
			EventSender: bridgev2.EventSender{Sender: userID},
			Membership:  event.MembershipJoin,
		}
		if c.isMyHandle(p) {
			member.EventSender.IsFromMe = true
			member.EventSender.SenderLogin = c.UserLogin.ID
		}
		members[userID] = member
	}
	info := &bridgev2.ChatInfo{
		Type: ptr.Ptr(database.RoomTypeDefault),
		Members: &bridgev2.ChatMemberList{
			IsFull:    true,
			MemberMap: members,
		},
		ExcludeChangesFromTimeline: true,
		// The group id and cv_name go into the portal metadata when the room
		// is created, so they survive a restart before the first message.
		ExtraUpdates: func(_ context.Context, portal *bridgev2.Portal) bool {
			var groupName *string
			if name != "" {
				groupName = &name
			}
			setGroupMetadata(portal, &groupID, groupName)
			return true
		},
	}
	if name != "" {
		info.Name = &name
	} else if c.Main != nil {
		// No iMessage name: the room gets the same contact-built name
		// GetChatInfo gives unnamed groups.
		if built := c.buildGroupName(participants); built != "" {
			info.Name = &built
		}
	}
	return &bridgev2.CreateChatResponse{PortalKey: portalKey, PortalInfo: info}, nil
}

// groupManagerLevel is the power level of the group manager: one step above
// the ghosts, which sit at users_default (0).
const groupManagerLevel = 1

// groupManagerFor returns the Matrix user who may invite and remove ghosts in
// the portal's room, or "" when nobody may.
func (c *IMClient) groupManagerFor(portal *bridgev2.Portal) id.UserID {
	return ""
}

// groupAllowedUserLevel is the highest power level the bridge lets userID
// keep in a room whose baseline (users_default) is given.
func groupAllowedUserLevel(userID, manager id.UserID, baseline int) int {
	return baseline
}

// groupMembershipLevels returns the invite and kick power levels of a room.
func groupMembershipLevels(managed bool) (invite, kick int) {
	return plLockLevel, plLockLevel
}

// syncGroupManagerLevel gives the group manager groupManagerLevel, or takes it
// away again where there is no manager, and reports whether it changed pl.
func syncGroupManagerLevel(pl *event.PowerLevelsEventContent, botMXID, loginUser, manager id.UserID) bool {
	return false
}

// groupRoomOps is what the membership handler does on the Matrix side of a
// portal room. The bridge-backed implementation is bridgeGroupRoomOps.
type groupRoomOps interface {
	// RemoveFromRoom takes userID out of the portal's room, whether invited or
	// joined, as the bridge bot.
	RemoveFromRoom(ctx context.Context, portal *bridgev2.Portal, userID id.UserID, reason string) error
	// Notice posts a bot notice in the portal's room.
	Notice(ctx context.Context, portal *bridgev2.Portal, text string)
	// ResyncMembers makes the room's member list follow the stored
	// participant list.
	ResyncMembers(portal *bridgev2.Portal)
}

type bridgeGroupRoomOps struct{ c *IMClient }

func (o bridgeGroupRoomOps) RemoveFromRoom(ctx context.Context, portal *bridgev2.Portal, userID id.UserID, reason string) error {
	_, err := o.c.Main.Bridge.Bot.SendState(ctx, portal.MXID, event.StateMember, userID.String(), &event.Content{
		Parsed: &event.MemberEventContent{Membership: event.MembershipLeave, Reason: reason},
	}, time.Time{})
	return err
}

func (o bridgeGroupRoomOps) Notice(ctx context.Context, portal *bridgev2.Portal, text string) {
	if _, err := o.c.Main.Bridge.Bot.SendMessage(ctx, portal.MXID, event.EventMessage, &event.Content{
		Parsed: &event.MessageEventContent{MsgType: event.MsgNotice, Body: text},
	}, nil); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Stringer("room_id", portal.MXID).Msg("Failed to post group notice")
	}
}

func (o bridgeGroupRoomOps) ResyncMembers(portal *bridgev2.Portal) {
	o.c.UserLogin.QueueRemoteEvent(&simplevent.ChatResync{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventChatResync,
			PortalKey: portal.PortalKey,
			LogContext: func(lc zerolog.Context) zerolog.Context {
				return lc.Str("source", "group_membership")
			},
		},
		GetChatInfoFunc: o.c.GetChatInfo,
	})
}

func (c *IMClient) groupRoom() groupRoomOps {
	if c.groupRoomOverride != nil {
		return c.groupRoomOverride
	}
	return bridgeGroupRoomOps{c}
}
