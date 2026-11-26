package connector

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"reflect"
	"strings"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/lrhodin/corten-matrix/pkg/rustpushgo"
)

const (
	groupTestSelf    = "tel:+15550001000"
	groupTestAlice   = "tel:+15550001001"
	groupTestBob     = "tel:+15550001002"
	groupTestCarol   = "mailto:carol@example.com"
	groupTestDave    = "tel:+15550001004"
	groupTestGUID    = "0A1B2C3D-0000-4000-8000-00000000ABCD"
	groupTestPortal  = "gid:0a1b2c3d-0000-4000-8000-00000000abcd"
	groupTestOldName = "Old name"
)

// groupCall is one rustpush group call recorded by fakeGroupSender.
type groupCall struct {
	op              string
	conv            rustpushgo.WrappedConversation
	name            string
	newParticipants []string
	photo           []byte
	handle          string
}

// fakeGroupSender stands in for the rustpush client: it records every call
// and never talks to Apple.
type fakeGroupSender struct {
	calls       []groupCall
	fail        error
	unreachable map[string]bool
	validated   [][]string
}

func (f *fakeGroupSender) record(call groupCall) (string, error) {
	if f.fail != nil {
		return "", f.fail
	}
	f.calls = append(f.calls, call)
	return "MSG-ID", nil
}

func (f *fakeGroupSender) SendRenameGroup(conv rustpushgo.WrappedConversation, newName string, handle string) (string, error) {
	return f.record(groupCall{op: "rename", conv: conv, name: newName, handle: handle})
}

func (f *fakeGroupSender) SendChangeParticipants(conv rustpushgo.WrappedConversation, newParticipants []string, _ uint64, handle string) (string, error) {
	return f.record(groupCall{op: "participants", conv: conv, newParticipants: newParticipants, handle: handle})
}

func (f *fakeGroupSender) SendIconChange(conv rustpushgo.WrappedConversation, photoData []byte, _ uint64, handle string) (string, error) {
	return f.record(groupCall{op: "icon", conv: conv, photo: photoData, handle: handle})
}

func (f *fakeGroupSender) SendIconClear(conv rustpushgo.WrappedConversation, _ uint64, handle string) (string, error) {
	return f.record(groupCall{op: "icon-clear", conv: conv, handle: handle})
}

func (f *fakeGroupSender) ValidateTargets(targets []string, _ string) []string {
	f.validated = append(f.validated, targets)
	var valid []string
	for _, target := range targets {
		if !f.unreachable[target] {
			valid = append(valid, target)
		}
	}
	return valid
}

// groupTestFixture is a client with a cloud store and a fake rustpush, and
// one four-person iMessage group (self, Alice, Bob, Carol) whose stored
// participant list leaves the owner out, as CloudKit's does.
func groupTestFixture(t *testing.T) (context.Context, *IMClient, *fakeGroupSender, *bridgev2.Portal) {
	t.Helper()
	ctx, store := recycleBinFixture(t)
	sender := &fakeGroupSender{}
	client := &IMClient{
		handle:              groupTestSelf,
		allHandles:          []string{groupTestSelf, "mailto:me@example.com"},
		cloudStore:          store,
		UserLogin:           &bridgev2.UserLogin{UserLogin: &database.UserLogin{ID: testSQLLoginID}},
		smsPortals:          map[string]bool{},
		imGroupNames:        map[string]string{},
		imGroupGuids:        map[string]string{},
		imGroupParticipants: map[string][]string{},
		groupSenderOverride: sender,
	}
	oldName := groupTestOldName
	if err := store.upsertChat(ctx, "chat-"+groupTestGUID, "", groupTestGUID, groupTestPortal, "iMessage",
		&oldName, nil, []string{groupTestAlice, groupTestBob, groupTestCarol}, 1); err != nil {
		t.Fatalf("upsertChat: %v", err)
	}
	return ctx, client, sender, groupTestPortalObject(groupTestPortal, &PortalMetadata{SenderGuid: groupTestGUID, GroupName: groupTestOldName})
}

func groupTestPortalObject(portalID string, meta *PortalMetadata) *bridgev2.Portal {
	return &bridgev2.Portal{Portal: &database.Portal{
		PortalKey: networkid.PortalKey{ID: networkid.PortalID(portalID), Receiver: testSQLLoginID},
		Name:      groupTestOldName,
		Metadata:  meta,
	}}
}

func roomNameEvent(portal *bridgev2.Portal, name string) *bridgev2.MatrixRoomName {
	return &bridgev2.MatrixRoomName{MatrixEventBase: bridgev2.MatrixEventBase[*event.RoomNameEventContent]{
		Content: &event.RoomNameEventContent{Name: name},
		Portal:  portal,
	}}
}

func roomAvatarEvent(portal *bridgev2.Portal, url id.ContentURIString) *bridgev2.MatrixRoomAvatar {
	return &bridgev2.MatrixRoomAvatar{MatrixEventBase: bridgev2.MatrixEventBase[*event.RoomAvatarEventContent]{
		Content: &event.RoomAvatarEventContent{URL: url},
		Portal:  portal,
	}}
}

func membershipEvent(portal *bridgev2.Portal, change bridgev2.MembershipChangeType, ghostID string) *bridgev2.MatrixMembershipChange {
	evt := &bridgev2.MatrixMembershipChange{
		MatrixRoomMeta: bridgev2.MatrixRoomMeta[*event.MemberEventContent]{
			MatrixEventBase: bridgev2.MatrixEventBase[*event.MemberEventContent]{Portal: portal},
		},
		Type: change,
	}
	if ghostID != "" {
		evt.Target = &bridgev2.Ghost{Ghost: &database.Ghost{ID: networkid.UserID(ghostID)}}
	}
	return evt
}

func wantParticipants(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: got %v, want %v", what, got, want)
	}
}

// TestMacounGroupRename: renaming the room sends one rename addressed to the
// whole group, under the group's own id and old name, and moves every local
// copy of the name.
func TestMacounGroupRename(t *testing.T) {
	ctx, client, sender, portal := groupTestFixture(t)

	changed, err := client.HandleMatrixRoomName(ctx, roomNameEvent(portal, "  Trip planning "))
	if err != nil || !changed {
		t.Fatalf("HandleMatrixRoomName: changed=%v err=%v", changed, err)
	}
	if len(sender.calls) != 1 || sender.calls[0].op != "rename" {
		t.Fatalf("want exactly one rename call, got %+v", sender.calls)
	}
	call := sender.calls[0]
	if call.name != "Trip planning" || call.handle != groupTestSelf {
		t.Fatalf("rename call: name=%q handle=%q", call.name, call.handle)
	}
	wantParticipants(t, "rename recipients", call.conv.Participants, groupTestAlice, groupTestBob, groupTestCarol, groupTestSelf)
	if call.conv.SenderGuid == nil || *call.conv.SenderGuid != groupTestGUID {
		t.Fatalf("rename must carry the group id in its original spelling, got %v", call.conv.SenderGuid)
	}
	if call.conv.GroupName == nil || *call.conv.GroupName != groupTestOldName {
		t.Fatalf("rename must carry the old name as the conversation name, got %v", call.conv.GroupName)
	}
	if portal.Name != "Trip planning" || !portal.NameSet {
		t.Fatalf("portal name not updated: %q set=%v", portal.Name, portal.NameSet)
	}
	if meta := portal.Metadata.(*PortalMetadata); meta.GroupName != "Trip planning" || meta.SenderGuid != groupTestGUID {
		t.Fatalf("portal metadata: %+v", meta)
	}
	if stored, _ := client.cloudStore.getDisplayNameByPortalID(ctx, groupTestPortal); stored != "Trip planning" {
		t.Fatalf("cloud_chat display name: %q", stored)
	}
	// The next message is addressed with the new name.
	if conv := client.portalToConversation(portal); conv.GroupName == nil || *conv.GroupName != "Trip planning" {
		t.Fatalf("later messages must use the new name, got %v", conv.GroupName)
	}
}

// TestMacounGroupRenameRefusals: nothing is sent and nothing changes when the
// rename cannot or must not go out.
func TestMacounGroupRenameRefusals(t *testing.T) {
	ctx, client, sender, portal := groupTestFixture(t)

	if changed, err := client.HandleMatrixRoomName(ctx, roomNameEvent(portal, "   ")); changed || !errors.Is(err, errEmptyGroupName) {
		t.Fatalf("empty name: changed=%v err=%v", changed, err)
	}
	dm := groupTestPortalObject(groupTestAlice, nil)
	if changed, err := client.HandleMatrixRoomName(ctx, roomNameEvent(dm, "x")); changed || !errors.Is(err, errNotIMessageGroup) {
		t.Fatalf("direct chat: changed=%v err=%v", changed, err)
	}
	client.smsPortals[groupTestPortal] = true
	if changed, err := client.HandleMatrixRoomName(ctx, roomNameEvent(portal, "x")); changed || !errors.Is(err, errCarrierGroup) {
		t.Fatalf("carrier group: changed=%v err=%v", changed, err)
	}
	client.smsPortals[groupTestPortal] = false

	sender.fail = errors.New("apple said no")
	if changed, err := client.HandleMatrixRoomName(ctx, roomNameEvent(portal, "New")); changed || err == nil {
		t.Fatalf("failed send: changed=%v err=%v", changed, err)
	}
	if len(sender.calls) != 0 || portal.Name != groupTestOldName {
		t.Fatalf("a refused rename must leave everything as it was: calls=%d name=%q", len(sender.calls), portal.Name)
	}
}

func groupTestImage(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestMacounGroupPhoto: a room avatar becomes a 570x570 PNG icon change, and
// removing the avatar becomes an icon clear.
func TestMacounGroupPhoto(t *testing.T) {
	ctx, client, sender, portal := groupTestFixture(t)
	original := groupTestImage(t, 120, 80)
	const mxc = id.ContentURIString("mxc://example.com/avatar")
	client.matrixMediaOverride = func(_ context.Context, uri id.ContentURIString) ([]byte, error) {
		if uri != mxc {
			t.Fatalf("downloaded %q, want %q", uri, mxc)
		}
		return original, nil
	}

	changed, err := client.HandleMatrixRoomAvatar(ctx, roomAvatarEvent(portal, mxc))
	if err != nil || !changed {
		t.Fatalf("set avatar: changed=%v err=%v", changed, err)
	}
	if len(sender.calls) != 1 || sender.calls[0].op != "icon" {
		t.Fatalf("want one icon change, got %+v", sender.calls)
	}
	sent, format, err := image.Decode(bytes.NewReader(sender.calls[0].photo))
	if err != nil || format != "png" || sent.Bounds().Dx() != groupPhotoSize || sent.Bounds().Dy() != groupPhotoSize {
		t.Fatalf("photo sent to Apple must be a %dx%d PNG: format=%q bounds=%v err=%v", groupPhotoSize, groupPhotoSize, format, sent.Bounds(), err)
	}
	wantParticipants(t, "icon recipients", sender.calls[0].conv.Participants, groupTestAlice, groupTestBob, groupTestCarol, groupTestSelf)
	if portal.AvatarMXC != mxc || portal.AvatarID == "" || portal.AvatarHash == [32]byte{} {
		t.Fatalf("portal avatar fields not set: id=%q mxc=%q", portal.AvatarID, portal.AvatarMXC)
	}
	if _, cached, _ := client.cloudStore.getGroupPhoto(ctx, groupTestPortal); !bytes.Equal(cached, sender.calls[0].photo) {
		t.Fatal("the photo was not cached for later chat info")
	}

	changed, err = client.HandleMatrixRoomAvatar(ctx, roomAvatarEvent(portal, ""))
	if err != nil || !changed {
		t.Fatalf("clear avatar: changed=%v err=%v", changed, err)
	}
	if len(sender.calls) != 2 || sender.calls[1].op != "icon-clear" {
		t.Fatalf("want an icon clear, got %+v", sender.calls[1:])
	}
	if portal.AvatarMXC != "" || portal.AvatarID != "" {
		t.Fatalf("portal avatar fields not cleared: id=%q mxc=%q", portal.AvatarID, portal.AvatarMXC)
	}
	if _, cached, _ := client.cloudStore.getGroupPhoto(ctx, groupTestPortal); len(cached) != 0 {
		t.Fatal("cached photo survived the clear")
	}

	// Something that is not an image is refused before anything is sent.
	client.matrixMediaOverride = func(context.Context, id.ContentURIString) ([]byte, error) { return []byte("not an image"), nil }
	if changed, err = client.HandleMatrixRoomAvatar(ctx, roomAvatarEvent(portal, mxc)); changed || err == nil || len(sender.calls) != 2 {
		t.Fatalf("undecodable avatar: changed=%v err=%v calls=%d", changed, err, len(sender.calls))
	}
}

// TestMacounGroupAddAndRemoveParticipant: inviting a ghost adds its handle,
// removing a ghost drops it; each sends the old list as the conversation and
// the new list as the change, and the stored list follows.
func TestMacounGroupAddAndRemoveParticipant(t *testing.T) {
	ctx, client, sender, portal := groupTestFixture(t)

	if _, err := client.HandleMatrixMembership(ctx, membershipEvent(portal, bridgev2.Invite, groupTestDave)); err != nil {
		t.Fatalf("invite: %v", err)
	}
	if len(sender.calls) != 1 || sender.calls[0].op != "participants" {
		t.Fatalf("want one participant change, got %+v", sender.calls)
	}
	wantParticipants(t, "invite: old list", sender.calls[0].conv.Participants, groupTestAlice, groupTestBob, groupTestCarol, groupTestSelf)
	wantParticipants(t, "invite: new list", sender.calls[0].newParticipants, groupTestAlice, groupTestBob, groupTestCarol, groupTestSelf, groupTestDave)
	if len(sender.validated) != 1 || !reflect.DeepEqual(sender.validated[0], []string{groupTestDave}) {
		t.Fatalf("the new participant must be checked for iMessage reachability, got %v", sender.validated)
	}
	stored, _ := client.cloudStore.getChatParticipantsByPortalID(ctx, groupTestPortal)
	wantParticipants(t, "stored list after invite", stored, groupTestAlice, groupTestBob, groupTestCarol, groupTestSelf, groupTestDave)

	if _, err := client.HandleMatrixMembership(ctx, membershipEvent(portal, bridgev2.Kick, groupTestBob)); err != nil {
		t.Fatalf("kick: %v", err)
	}
	wantParticipants(t, "kick: old list", sender.calls[1].conv.Participants, groupTestAlice, groupTestBob, groupTestCarol, groupTestDave, groupTestSelf)
	wantParticipants(t, "kick: new list", sender.calls[1].newParticipants, groupTestAlice, groupTestCarol, groupTestDave, groupTestSelf)
	stored, _ = client.cloudStore.getChatParticipantsByPortalID(ctx, groupTestPortal)
	wantParticipants(t, "stored list after kick", stored, groupTestAlice, groupTestCarol, groupTestDave, groupTestSelf)

	// Inviting someone who is already in, or removing someone who is not,
	// sends nothing.
	if _, err := client.HandleMatrixMembership(ctx, membershipEvent(portal, bridgev2.Invite, groupTestAlice)); err != nil || len(sender.calls) != 2 {
		t.Fatalf("invite of a member: err=%v calls=%d", err, len(sender.calls))
	}
	if _, err := client.HandleMatrixMembership(ctx, membershipEvent(portal, bridgev2.Kick, groupTestBob)); err != nil || len(sender.calls) != 2 {
		t.Fatalf("kick of a non-member: err=%v calls=%d", err, len(sender.calls))
	}
}

// TestMacounGroupMembershipRefusals: changes iMessage would not accept, or
// this bridge cannot express, send nothing and leave the list alone.
func TestMacounGroupMembershipRefusals(t *testing.T) {
	ctx, client, sender, portal := groupTestFixture(t)
	storedBefore, _ := client.cloudStore.getChatParticipantsByPortalID(ctx, groupTestPortal)

	sender.unreachable = map[string]bool{groupTestDave: true}
	if _, err := client.HandleMatrixMembership(ctx, membershipEvent(portal, bridgev2.Invite, groupTestDave)); err == nil {
		t.Fatal("inviting someone who is not on iMessage must fail")
	}
	if _, err := client.HandleMatrixMembership(ctx, membershipEvent(portal, bridgev2.BanLeft, groupTestDave)); !errors.Is(err, errUnsupportedMembership) {
		t.Fatalf("ban of a non-member: %v", err)
	}
	if _, err := client.HandleMatrixMembership(ctx, membershipEvent(portal, bridgev2.Kick, groupTestSelf)); !errors.Is(err, errUnsupportedMembership) {
		t.Fatalf("kick of the user's own ghost: %v", err)
	}
	legacy := groupTestPortalObject(groupTestAlice+","+groupTestBob+","+groupTestSelf, nil)
	if _, err := client.HandleMatrixMembership(ctx, membershipEvent(legacy, bridgev2.Invite, groupTestDave)); !errors.Is(err, errLegacyGroupMembers) {
		t.Fatalf("legacy participant-keyed group: %v", err)
	}
	dm := groupTestPortalObject(groupTestAlice, nil)
	if _, err := client.HandleMatrixMembership(ctx, membershipEvent(dm, bridgev2.Invite, groupTestDave)); !errors.Is(err, errNotIMessageGroup) {
		t.Fatalf("invite into a direct chat: %v", err)
	}
	if res, err := client.HandleMatrixMembership(ctx, membershipEvent(dm, bridgev2.Leave, "")); res != nil || err != nil {
		t.Fatalf("leaving a direct chat's room is not an error: %v %v", res, err)
	}

	// Remove Carol (four people become three), then nobody else may go.
	if _, err := client.HandleMatrixMembership(ctx, membershipEvent(portal, bridgev2.Kick, groupTestCarol)); err != nil || len(sender.calls) != 1 {
		t.Fatalf("kick down to three: err=%v calls=%d", err, len(sender.calls))
	}
	if _, err := client.HandleMatrixMembership(ctx, membershipEvent(portal, bridgev2.Kick, groupTestBob)); !errors.Is(err, errGroupTooSmall) {
		t.Fatalf("kick below three: %v", err)
	}
	if _, err := client.HandleMatrixMembership(ctx, membershipEvent(portal, bridgev2.Leave, "")); !errors.Is(err, errGroupTooSmall) {
		t.Fatalf("leave below three: %v", err)
	}
	if len(sender.calls) != 1 {
		t.Fatalf("refused changes sent %d messages", len(sender.calls)-1)
	}
	if len(storedBefore) != 3 {
		t.Fatalf("fixture: stored list should start with three others, got %v", storedBefore)
	}
}

// TestMacounGroupLeave: leaving the room removes the user's own handle from
// the group.
func TestMacounGroupLeave(t *testing.T) {
	ctx, client, sender, portal := groupTestFixture(t)
	if _, err := client.HandleMatrixMembership(ctx, membershipEvent(portal, bridgev2.Leave, "")); err != nil {
		t.Fatalf("leave: %v", err)
	}
	if len(sender.calls) != 1 || sender.calls[0].op != "participants" {
		t.Fatalf("want one participant change, got %+v", sender.calls)
	}
	wantParticipants(t, "leave: old list", sender.calls[0].conv.Participants, groupTestAlice, groupTestBob, groupTestCarol, groupTestSelf)
	wantParticipants(t, "leave: new list", sender.calls[0].newParticipants, groupTestAlice, groupTestBob, groupTestCarol)
}

// TestMacounGroupCreate: creating a group sends nothing, and leaves the bridge
// in the state where the first message from the new room goes to every
// participant under one new group id, which is also the id replies will carry.
func TestMacounGroupCreate(t *testing.T) {
	ctx, client, sender, _ := groupTestFixture(t)
	params := &bridgev2.GroupCreateParams{
		Type:         groupCreateType,
		Participants: []networkid.UserID{networkid.UserID(groupTestAlice), networkid.UserID(groupTestCarol), networkid.UserID(groupTestAlice)},
		Name:         &event.RoomNameEventContent{Name: "Weekend"},
	}
	resp, err := client.CreateGroup(ctx, params)
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if len(sender.calls) != 0 {
		t.Fatalf("creating a group must not send anything, sent %+v", sender.calls)
	}
	portalID := string(resp.PortalKey.ID)
	if !strings.HasPrefix(portalID, "gid:") || portalID != strings.ToLower(portalID) || resp.PortalKey.Receiver != testSQLLoginID {
		t.Fatalf("portal key: %+v", resp.PortalKey)
	}
	if resp.PortalInfo == nil || resp.PortalInfo.Name == nil || *resp.PortalInfo.Name != "Weekend" || len(resp.PortalInfo.Members.MemberMap) != 3 {
		t.Fatalf("portal info: %+v", resp.PortalInfo)
	}
	if me := resp.PortalInfo.Members.MemberMap[networkid.UserID(groupTestSelf)]; !me.IsFromMe {
		t.Fatal("the user must be a member of the new room as themselves")
	}

	// What bridgev2 does next: create the portal and apply the chat info.
	portal := groupTestPortalObject(portalID, nil)
	if !resp.PortalInfo.ExtraUpdates(ctx, portal) {
		t.Fatal("ExtraUpdates must report a change so the metadata is saved")
	}
	meta := portal.Metadata.(*PortalMetadata)
	if meta.GroupName != "Weekend" || "gid:"+strings.ToLower(meta.SenderGuid) != portalID || meta.SenderGuid != strings.ToUpper(meta.SenderGuid) {
		t.Fatalf("portal metadata: %+v (portal %s)", meta, portalID)
	}

	// The first message: after a restart the caches are empty and only the
	// portal metadata and the cloud store remain.
	client.imGroupGuids = map[string]string{}
	client.imGroupNames = map[string]string{}
	client.imGroupParticipants = map[string][]string{}
	conv := client.portalToConversation(portal)
	wantParticipants(t, "first message recipients", conv.Participants, groupTestAlice, groupTestCarol, groupTestSelf)
	if conv.SenderGuid == nil || *conv.SenderGuid != meta.SenderGuid || conv.GroupName == nil || *conv.GroupName != "Weekend" || conv.IsSms {
		t.Fatalf("first message conversation: %+v", conv)
	}
	// A reply carries that sender_guid; makePortalKey keys an iMessage group
	// message by "gid:" + its lowercase sender_guid and uses the portal of that
	// id when it has a room, so the reply lands in this room.
	if replyPortalID := "gid:" + strings.ToLower(*conv.SenderGuid); replyPortalID != portalID {
		t.Fatalf("a reply would be keyed %s, the room is %s", replyPortalID, portalID)
	}

	// Two groups never share an id.
	again, err := client.CreateGroup(ctx, params)
	if err != nil || again.PortalKey.ID == resp.PortalKey.ID {
		t.Fatalf("second group: key=%v err=%v", again.PortalKey, err)
	}
}

// TestMacounGroupCreateRefusals: too few people, or someone who cannot be
// reached over iMessage, and nothing is stored.
func TestMacounGroupCreateRefusals(t *testing.T) {
	ctx, client, sender, _ := groupTestFixture(t)
	chatsBefore := countRows(t, ctx, client.cloudStore, "cloud_chat")

	one := &bridgev2.GroupCreateParams{Participants: []networkid.UserID{networkid.UserID(groupTestAlice), networkid.UserID(groupTestSelf)}}
	if _, err := client.CreateGroup(ctx, one); err == nil {
		t.Fatal("a group with one other person must be refused")
	}
	sender.unreachable = map[string]bool{groupTestBob: true}
	mixed := &bridgev2.GroupCreateParams{Participants: []networkid.UserID{networkid.UserID(groupTestAlice), networkid.UserID(groupTestBob)}}
	if _, err := client.CreateGroup(ctx, mixed); err == nil || !strings.Contains(err.Error(), groupTestBob) {
		t.Fatalf("a participant who is not on iMessage must be refused by name, got %v", err)
	}
	if _, err := client.CreateGroup(ctx, &bridgev2.GroupCreateParams{Type: "channel", Participants: mixed.Participants}); err == nil {
		t.Fatal("an unknown group type must be refused")
	}
	if after := countRows(t, ctx, client.cloudStore, "cloud_chat"); after != chatsBefore {
		t.Fatalf("refused creations stored %d chat rows", after-chatsBefore)
	}
}

func countRows(t *testing.T, ctx context.Context, store *cloudBackfillStore, table string) int {
	t.Helper()
	var n int
	if err := store.db.QueryRow(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestMacounGroupMetaPowerLevels: in an iMessage group the name and avatar
// events drop to the level every user has; anywhere else such an entry is
// removed, and a room that never had one is not touched.
func TestMacounGroupMetaPowerLevels(t *testing.T) {
	name, avatar := event.StateRoomName.Type, event.StateRoomAvatar.Type

	pl := &event.PowerLevelsEventContent{}
	if !syncGroupMetaLevels(pl, true) || pl.Events[name] != 0 || pl.Events[avatar] != 0 {
		t.Fatalf("iMessage group: events=%v", pl.Events)
	}
	if _, has := pl.Events[name]; !has {
		t.Fatal("the name level must be an explicit entry, or state_default (50) applies")
	}
	if syncGroupMetaLevels(pl, true) {
		t.Fatal("a second pass over the same levels must change nothing")
	}

	// The group turns into a carrier group: back under state_default.
	if !syncGroupMetaLevels(pl, false) {
		t.Fatal("entries below state_default must be removed where editing is not allowed")
	}
	if _, has := pl.Events[name]; has {
		t.Fatalf("name entry survived: %v", pl.Events)
	}

	// A direct chat that never had entries, and one where someone locked the
	// name harder than state_default: both left alone.
	if syncGroupMetaLevels(&event.PowerLevelsEventContent{}, false) {
		t.Fatal("a room without entries must not get a power-level change")
	}
	locked := &event.PowerLevelsEventContent{Events: map[string]int{name: 100}}
	if syncGroupMetaLevels(locked, false) || locked.Events[name] != 100 {
		t.Fatalf("a stricter entry must be kept: %v", locked.Events)
	}

	client := &IMClient{smsPortals: map[string]bool{"gid:sms": true}}
	for portalID, want := range map[string]bool{
		groupTestPortal: true,
		"tel:+15550001001,tel:+15550001002,tel:+15550001000": true,
		groupTestAlice: false,
		"gid:sms":      false,
	} {
		if got := client.matrixMayEditGroupMeta(groupTestPortalObject(portalID, nil)); got != want {
			t.Errorf("matrixMayEditGroupMeta(%s) = %v, want %v", portalID, got, want)
		}
	}
	if client.matrixMayEditGroupMeta(nil) {
		t.Error("no portal, no editing")
	}
}

// TestMacounNewGroupArgs: identifiers are normalized like start-chat's and the
// name is everything after the word "name".
func TestMacounNewGroupArgs(t *testing.T) {
	participants, name := parseNewGroupArgs([]string{"+1(555)000-1001", "Carol@Example.com", "tel:+15550001002", "name", "Weekend", "trip"})
	want := []networkid.UserID{networkid.UserID(groupTestAlice), networkid.UserID(groupTestCarol), networkid.UserID(groupTestBob)}
	if !reflect.DeepEqual(participants, want) || name != "Weekend trip" {
		t.Fatalf("participants=%v name=%q", participants, name)
	}
	if participants, name = parseNewGroupArgs([]string{"+15550001001", "+15550001002"}); len(participants) != 2 || name != "" {
		t.Fatalf("no name: participants=%v name=%q", participants, name)
	}
	found := map[string]bool{}
	for _, h := range BridgeCommands() {
		found[h.Name] = true
	}
	for _, cmd := range []string{"new-group", "group-add", "group-remove", "group-leave"} {
		if !found[cmd] {
			t.Errorf("%s is not registered", cmd)
		}
	}
}
