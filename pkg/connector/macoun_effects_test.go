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
