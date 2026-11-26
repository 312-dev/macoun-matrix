// macoun-matrix: tests for iMessage expressive effects.
// SPDX-License-Identifier: MPL-2.0

package connector

import (
	"context"
	"strings"
	"testing"

	"github.com/lrhodin/corten-matrix/pkg/rustpushgo"
	"maunium.net/go/mautrix/event"
)

const testEffectKey = "com.beeper.imessage.effect"

func incomingWithEffect(text string, effect *string) *rustpushgo.WrappedMessage {
	return &rustpushgo.WrappedMessage{Uuid: "00000000-0000-4000-8000-0000000000E1", Text: &text, Effect: effect}
}

func strp(s string) *string { return &s }

// An iMessage sent with a screen effect reaches Matrix with the effect id
// under com.beeper.imessage.effect and its text unchanged.
func TestMacounIncomingScreenEffectIsBridged(t *testing.T) {
	const confetti = "com.apple.messages.effect.CKConfettiEffect"
	cm, err := convertMessage(context.Background(), nil, nil, incomingWithEffect("happy birthday", strp(confetti)))
	if err != nil {
		t.Fatal(err)
	}
	if len(cm.Parts) != 1 {
		t.Fatalf("got %d parts, want 1", len(cm.Parts))
	}
	part := cm.Parts[0]
	if got := part.Extra[testEffectKey]; got != confetti {
		t.Fatalf("%s = %v, want %s", testEffectKey, got, confetti)
	}
	if part.Content.Body != "happy birthday" {
		t.Fatalf("body = %q, want the text unchanged", part.Content.Body)
	}
	if part.Content.FormattedBody != "" {
		t.Fatalf("formatted_body = %q, want none for a screen effect", part.Content.FormattedBody)
	}
}

// Invisible ink carries the same key, and the text part also gets a Matrix
// spoiler so clients that ignore the key still hide it. The body stays plain.
func TestMacounIncomingInvisibleInkIsSpoiler(t *testing.T) {
	const ink = "com.apple.MobileSMS.expressivesend.invisibleink"
	cm, err := convertMessage(context.Background(), nil, nil, incomingWithEffect("the code is <1234>", strp(ink)))
	if err != nil {
		t.Fatal(err)
	}
	part := cm.Parts[0]
	if got := part.Extra[testEffectKey]; got != ink {
		t.Fatalf("%s = %v, want %s", testEffectKey, got, ink)
	}
	if part.Content.Body != "the code is <1234>" {
		t.Fatalf("body = %q, want the text unchanged", part.Content.Body)
	}
	if part.Content.Format != event.FormatHTML {
		t.Fatalf("format = %q, want %q", part.Content.Format, event.FormatHTML)
	}
	want := "<span data-mx-spoiler>the code is &lt;1234&gt;</span>"
	if part.Content.FormattedBody != want {
		t.Fatalf("formatted_body = %q, want %q", part.Content.FormattedBody, want)
	}
}

// A message with no effect, or with an id the bridge does not know, carries
// no effect key.
func TestMacounIncomingWithoutKnownEffectHasNoKey(t *testing.T) {
	for _, effect := range []*string{nil, strp(""), strp("com.apple.messages.effect.CKMadeUpEffect")} {
		cm, err := convertMessage(context.Background(), nil, nil, incomingWithEffect("hi", effect))
		if err != nil {
			t.Fatal(err)
		}
		for _, part := range cm.Parts {
			if v, ok := part.Extra[testEffectKey]; ok {
				t.Fatalf("effect %v: got %s = %v, want no key", effect, testEffectKey, v)
			}
			if strings.Contains(part.Content.FormattedBody, "data-mx-spoiler") {
				t.Fatalf("effect %v: unexpected spoiler %q", effect, part.Content.FormattedBody)
			}
		}
	}
}

// The effect on an outbound Matrix message reaches rustpush as the effect
// envelope ahead of the text; send_message in lib.rs turns it into the
// message's effect.
func TestOutboundEffectEnvelope(t *testing.T) {
	const slam = "com.apple.MobileSMS.expressivesend.impact"
	got := outboundTextWithEffect(context.Background(), map[string]any{testEffectKey: slam}, false, "hello")
	if want := "\x00FX\x01" + slam + "\x00hello"; got != want {
		t.Fatalf("outbound text = %q, want %q", got, want)
	}
	// Ahead of a rich-link envelope, which lib.rs parses after it.
	rl := "\x00RL\x01example.com\x01https://example.com\x01Example\x01\x00example.com"
	got = outboundTextWithEffect(context.Background(), map[string]any{testEffectKey: slam}, false, rl)
	if want := "\x00FX\x01" + slam + "\x00" + rl; got != want {
		t.Fatalf("outbound text = %q, want %q", got, want)
	}
}

// Every listed effect id is accepted.
func TestOutboundEffectAllowlist(t *testing.T) {
	ids := []string{
		"com.apple.MobileSMS.expressivesend.impact",
		"com.apple.MobileSMS.expressivesend.loud",
		"com.apple.MobileSMS.expressivesend.gentle",
		"com.apple.MobileSMS.expressivesend.invisibleink",
		"com.apple.messages.effect.CKEchoEffect",
		"com.apple.messages.effect.CKSpotlightEffect",
		"com.apple.messages.effect.CKHappyBirthdayEffect",
		"com.apple.messages.effect.CKConfettiEffect",
		"com.apple.messages.effect.CKHeartEffect",
		"com.apple.messages.effect.CKLasersEffect",
		"com.apple.messages.effect.CKFireworksEffect",
		"com.apple.messages.effect.CKSparklesEffect",
		"com.apple.messages.effect.CKShootingStarEffect",
	}
	for _, id := range ids {
		got := outboundTextWithEffect(context.Background(), map[string]any{testEffectKey: id}, false, "x")
		if !strings.HasPrefix(got, "\x00FX\x01"+id+"\x00") {
			t.Errorf("%s was not sent: %q", id, got)
		}
	}
}

// An unknown or malformed effect is not sent, and the text goes out as is.
// So does any effect on a message to the text-message relay, which has no
// effects.
func TestOutboundEffectRefusals(t *testing.T) {
	for _, raw := range []map[string]any{
		nil,
		{},
		{testEffectKey: ""},
		{testEffectKey: 7},
		{testEffectKey: "com.apple.messages.effect.CKMadeUpEffect"},
		{testEffectKey: "com.apple.messages.effect.CKConfettiEffect\x00x"},
	} {
		if got := outboundTextWithEffect(context.Background(), raw, false, "hello"); got != "hello" {
			t.Errorf("raw %v: outbound text = %q, want plain text", raw, got)
		}
	}
	sms := outboundTextWithEffect(context.Background(), map[string]any{testEffectKey: "com.apple.messages.effect.CKConfettiEffect"}, true, "hello")
	if sms != "hello" {
		t.Errorf("SMS outbound text = %q, want plain text", sms)
	}
}

// A photo or video can carry an effect as well as text: the key on an
// m.image/m.video/m.file event goes to send_attachment as the envelope alone.
func TestOutboundAttachmentEffect(t *testing.T) {
	ctx := context.Background()
	slam := map[string]any{imessageEffectKey: "com.apple.MobileSMS.expressivesend.impact"}
	got := outboundAttachmentEffect(ctx, slam, false)
	if got == nil || *got != "\x00FX\x01com.apple.MobileSMS.expressivesend.impact\x00" {
		t.Fatalf("outboundAttachmentEffect = %v", got)
	}
	if got := outboundAttachmentEffect(ctx, slam, true); got != nil {
		t.Fatalf("SMS attachment kept the effect: %q", *got)
	}
	if got := outboundAttachmentEffect(ctx, map[string]any{imessageEffectKey: "com.example.nope"}, false); got != nil {
		t.Fatalf("unknown effect kept: %q", *got)
	}
	if got := outboundAttachmentEffect(ctx, nil, false); got != nil {
		t.Fatalf("no effect asked for, got %q", *got)
	}
}
