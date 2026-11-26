// corten-matrix - A Matrix-iMessage puppeting bridge.
// Copyright (C) 2024 Ludvig Rhodin
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package connector

import (
	"go.mau.fi/util/ffmpeg"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
)

func supportedIfFFmpeg() event.CapabilitySupportLevel {
	if ffmpeg.Supported() {
		return event.CapLevelPartialSupport
	}
	return event.CapLevelRejected
}

const iMessageMaxFileSize = 2000 * 1024 * 1024 // 2 GB
const capabilityID = "fi.mau.imessage.capabilities.2026_11"

var caps = &event.RoomFeatures{
	ID: capabilityID,

	Formatting: map[event.FormattingFeature]event.CapabilitySupportLevel{
		event.FmtBold:   event.CapLevelDropped,
		event.FmtItalic: event.CapLevelDropped,
	},
	File: map[event.CapabilityMsgType]*event.FileFeatures{
		event.MsgImage: {
			MimeTypes: map[string]event.CapabilitySupportLevel{
				"image/jpeg": event.CapLevelFullySupported,
				"image/png":  event.CapLevelFullySupported,
				"image/gif":  event.CapLevelFullySupported,
				"image/heic": event.CapLevelFullySupported,
				"image/heif": event.CapLevelFullySupported,
				"image/webp": event.CapLevelFullySupported,
			},
			Caption: event.CapLevelFullySupported,
			MaxSize: iMessageMaxFileSize,
		},
		event.CapMsgGIF: {
			MimeTypes: map[string]event.CapabilitySupportLevel{
				"image/gif": event.CapLevelFullySupported,
			},
			Caption: event.CapLevelFullySupported,
			MaxSize: iMessageMaxFileSize,
		},
		event.MsgVideo: {
			MimeTypes: map[string]event.CapabilitySupportLevel{
				"video/mp4":       event.CapLevelFullySupported,
				"video/quicktime": supportedIfFFmpeg(),
			},
			Caption: event.CapLevelFullySupported,
			MaxSize: iMessageMaxFileSize,
		},
		event.MsgAudio: {
			MimeTypes: map[string]event.CapabilitySupportLevel{
				"*/*": event.CapLevelFullySupported,
			},
			MaxSize: iMessageMaxFileSize,
		},
		event.CapMsgVoice: {
			MimeTypes: map[string]event.CapabilitySupportLevel{
				"*/*": event.CapLevelFullySupported,
			},
			MaxSize: iMessageMaxFileSize,
		},
		event.MsgFile: {
			MimeTypes: map[string]event.CapabilitySupportLevel{
				"*/*": event.CapLevelFullySupported,
			},
			Caption: event.CapLevelFullySupported,
			MaxSize: iMessageMaxFileSize,
		},
	},
	// iMessage itself imposes no documented text limit, but a Matrix event has
	// to fit in a 65,536-byte PDU. Advertise what one event can actually hold so
	// clients can warn before sending something larger. This is deliberately
	// tighter than maxOutboundTextBytes: clients should respect this number, and
	// the outbound splitter only catches whatever gets past it.
	MaxTextLength: maxTextContentBytes,

	Reply:               event.CapLevelFullySupported,
	Edit:                event.CapLevelFullySupported,
	Delete:              event.CapLevelFullySupported,
	DeleteChat:          true,
	Reaction:            event.CapLevelFullySupported,
	ReactionCount:       1,
	ReadReceipts:        true,
	TypingNotifications: true,
}

var capsDM *event.RoomFeatures

func init() {
	// The DM copy is taken first: renaming, a room avatar and membership
	// changes only mean something in a group (macoun_groups.go). The handlers
	// also refuse carrier (SMS/MMS/RCS) groups, which share these features
	// because a portal's service can change over its lifetime.
	c := *caps
	capsDM = &c
	capsDM.ID = capabilityID + "+dm"

	caps.State = event.StateFeatureMap{
		event.StateRoomName.Type:   {Level: event.CapLevelFullySupported},
		event.StateRoomAvatar.Type: {Level: event.CapLevelFullySupported},
	}
	caps.MemberActions = event.MemberFeatureMap{
		event.MemberActionInvite: event.CapLevelFullySupported,
		event.MemberActionKick:   event.CapLevelFullySupported,
		// Leaving reaches Apple only when bridge_matrix_leave is enabled, and
		// only while the group stays large enough.
		event.MemberActionLeave: event.CapLevelPartialSupport,
	}
}

var generalCaps = &bridgev2.NetworkGeneralCapabilities{
	DisappearingMessages: false,
	AggressiveUpdateInfo: true,
	Provisioning: bridgev2.ProvisioningCapabilities{
		ResolveIdentifier: bridgev2.ResolveIdentifierCapabilities{
			CreateDM:    true,
			LookupPhone: true,
			LookupEmail: true,
		},
		GroupCreation: map[string]bridgev2.GroupTypeCapabilities{
			groupCreateType: {
				TypeDescription: "iMessage group",
				Name:            bridgev2.GroupFieldCapability{Allowed: true},
				Participants:    bridgev2.GroupFieldCapability{Allowed: true, Required: true, MinLength: groupMinOthers},
			},
		},
	},
}

func (c *IMConnector) GetCapabilities() *bridgev2.NetworkGeneralCapabilities {
	return generalCaps
}

// Bump the capabilities version whenever `caps` changes, otherwise portals keep
// serving the previously cached capability event. Last bumped when the group
// state and member actions were added.
func (c *IMConnector) GetBridgeInfoVersion() (info, capabilities int) {
	return 1, 3
}
