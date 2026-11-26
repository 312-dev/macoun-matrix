// macoun-matrix: iMessage expressive effects (bubble and screen effects).
// SPDX-License-Identifier: MPL-2.0

package connector

import (
	"context"
	"fmt"

	"github.com/rs/zerolog"
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

// outboundTextWithEffect returns the text to hand to rustpush for the first
// part of an outbound Matrix message: the text itself, behind the effect
// envelope when the message's content asks for a known effect. An unknown id
// is logged and dropped, and so is any effect on a message bound for the
// text-message relay, which carries no effects.
func outboundTextWithEffect(ctx context.Context, raw map[string]any, isSms bool, text string) string {
	value, ok := raw[imessageEffectKey]
	if !ok {
		return text
	}
	effect, _ := value.(string)
	if effect == "" {
		return text
	}
	log := zerolog.Ctx(ctx)
	if !knownIMessageEffect(effect) {
		log.Warn().Str("effect_id", effect).Msg("Dropping unknown iMessage effect from outbound message")
		return text
	}
	if isSms {
		log.Debug().Str("effect_id", effect).Msg("Dropping iMessage effect from a message to the text-message relay")
		return text
	}
	// The envelope ends at the first NUL; knownIMessageEffect already rules
	// out any id that holds one.
	return "\x00FX\x01" + effect + "\x00" + text
}

// outboundAttachmentEffect returns the body argument for SendAttachment: the
// effect envelope alone when the message's content asks for a known effect,
// nil otherwise, under the same rules as outboundTextWithEffect.
// send_attachment splits the envelope off into the message's effect
// (attachment_effect_and_subject in lib.rs).
func outboundAttachmentEffect(ctx context.Context, raw map[string]any, isSms bool) *string {
	if body := outboundTextWithEffect(ctx, raw, isSms, ""); body != "" {
		return &body
	}
	return nil
}
