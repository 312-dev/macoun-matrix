// macoun-matrix: iMessage expressive effects (bubble and screen effects).
// SPDX-License-Identifier: MPL-2.0

package connector

import (
	"fmt"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
)

// imessageEffectKey is the content key on m.room.message that carries an
// iMessage effect, in both directions. The value is Apple's effect id
// verbatim (the expressiveSendStyleId, "iid" in the message payload). The key
// and the value format are the ones Beeper's iMessage bridge
// (github.com/beeper/imessage, msgconv.EffectIDField) uses, so clients that
// already read that key work unchanged.
const imessageEffectKey = "com.beeper.imessage.effect"

const effectInvisibleInk = "com.apple.MobileSMS.expressivesend.invisibleink"

// imessageEffects lists every effect id the bridge passes on, with the name
// Messages shows for it. Ids outside this list are dropped in both
// directions. The screen effects are the effect bundles in
// /System/Library/Messages/iMessageEffects on iOS 18 and 26; the names match
// the Messages effect picker (balloons is CKHappyBirthdayEffect and
// celebration is CKSparklesEffect).
var imessageEffects = map[string]string{
	// Bubble effects.
	"com.apple.MobileSMS.expressivesend.impact": "Slam",
	"com.apple.MobileSMS.expressivesend.loud":   "Loud",
	"com.apple.MobileSMS.expressivesend.gentle": "Gentle",
	effectInvisibleInk:                          "Invisible Ink",
	// Screen effects.
	"com.apple.messages.effect.CKEchoEffect":          "Echo",
	"com.apple.messages.effect.CKSpotlightEffect":     "Spotlight",
	"com.apple.messages.effect.CKHappyBirthdayEffect": "Balloons",
	"com.apple.messages.effect.CKConfettiEffect":      "Confetti",
	"com.apple.messages.effect.CKHeartEffect":         "Love",
	"com.apple.messages.effect.CKLasersEffect":        "Lasers",
	"com.apple.messages.effect.CKFireworksEffect":     "Fireworks",
	"com.apple.messages.effect.CKSparklesEffect":      "Celebration",
	"com.apple.messages.effect.CKShootingStarEffect":  "Shooting Star",
}

// maxInvisibleInkHTMLBytes bounds the text part that gets a spoiler-wrapped
// formatted_body for invisible ink. Escaping can grow a body several times
// over, and a part sized close to the event limit could then exceed it; a
// longer part keeps only the effect key.
const maxInvisibleInkHTMLBytes = 8192

// knownIMessageEffect reports whether id is an effect the bridge passes on.
func knownIMessageEffect(id string) bool {
	_, ok := imessageEffects[id]
	return ok
}

// applyIMessageEffect marks every part of an incoming message with the effect
// it was sent with. The body is left as the sender wrote it. Invisible ink
// also wraps the formatted_body of text parts in a Matrix spoiler, so a client
// that knows nothing about the key still hides the text.
func applyIMessageEffect(cm *bridgev2.ConvertedMessage, effect *string) {
	if cm == nil || effect == nil || !knownIMessageEffect(*effect) {
		return
	}
	for _, part := range cm.Parts {
		if part == nil || part.Content == nil {
			continue
		}
		if part.Extra == nil {
			part.Extra = map[string]any{}
		}
		part.Extra[imessageEffectKey] = *effect
		if *effect != effectInvisibleInk {
			continue
		}
		content := part.Content
		switch content.MsgType {
		case event.MsgText, event.MsgNotice, event.MsgEmote:
		default:
			continue
		}
		if len(content.Body) > maxInvisibleInkHTMLBytes {
			continue
		}
		content.EnsureHasHTML()
		content.FormattedBody = fmt.Sprintf("<span data-mx-spoiler>%s</span>", content.FormattedBody)
	}
}
