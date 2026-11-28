package connector

import (
	"testing"

	"maunium.net/go/mautrix/id"
)

func outboundTestResolve(userID id.UserID) string {
	switch userID {
	case "@imessage_tel=3a+15551234567:example.org":
		return "tel:+15551234567"
	case "@imessage_mailto=3a+jo@example.com:example.org":
		return "mailto:jo@example.com"
	}
	return ""
}

func TestOutboundHTMLRewrite(t *testing.T) {
	cases := []struct {
		name, in, want string
		mentions       int
	}{
		{
			name:     "pill to mention",
			in:       `hi <a href="https://matrix.to/#/@imessage_tel=3a+15551234567:example.org">Sam</a>!`,
			want:     `hi <mention uri="tel:+15551234567">Sam</mention>!`,
			mentions: 1,
		},
		{
			name:     "percent-encoded target with via",
			in:       `<a href="https://matrix.to/#/%40imessage_tel%3D3a%2B15551234567%3Aexample.org?via=example.org">@Sam</a>`,
			want:     `<mention uri="tel:+15551234567">@Sam</mention>`,
			mentions: 1,
		},
		{
			name:     "unmapped user stays text",
			in:       `ask <a href="https://matrix.to/#/@someone:matrix.org">Someone</a>`,
			want:     `ask Someone`,
			mentions: 0,
		},
		{
			name:     "room link stays text",
			in:       `see <a href="https://matrix.to/#/#room:example.org">#room</a>`,
			want:     `see #room`,
			mentions: 0,
		},
		{
			name:     "ordinary link untouched",
			in:       `<a href="https://example.com">site</a>`,
			want:     `<a href="https://example.com">site</a>`,
			mentions: 0,
		},
		{
			name:     "reply fallback dropped",
			in:       `<mx-reply><blockquote>old</blockquote></mx-reply><b>new</b>`,
			want:     `<b>new</b>`,
			mentions: 0,
		},
		{
			name:     "paragraphs and lists become breaks",
			in:       "<p>one</p>\n<p><b>two</b></p>\n<ul>\n<li>a</li>\n<li>b</li>\n</ul>\n",
			want:     `one<br/><b>two</b><br/>• a<br/>• b`,
			mentions: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, n := rewriteOutboundHTML(tc.in, outboundTestResolve)
			if got != tc.want || n != tc.mentions {
				t.Fatalf("got %q (%d mentions), want %q (%d)", got, n, tc.want, tc.mentions)
			}
		})
	}
}

func TestOutboundHTMLGate(t *testing.T) {
	plainOnly, _ := rewriteOutboundHTML("<p>just</p><p>text</p>", outboundTestResolve)
	if inlineFormat.MatchString(plainOnly) {
		t.Fatalf("paragraph-only HTML must not count as formatting: %q", plainOnly)
	}
	bold, _ := rewriteOutboundHTML("<strong>x</strong>", outboundTestResolve)
	if !inlineFormat.MatchString(bold) {
		t.Fatalf("strong must count as formatting: %q", bold)
	}
}

func TestOutboundEditText(t *testing.T) {
	if got := outboundEditText(nil, "plain"); got != "plain" {
		t.Fatalf("nil HTML must send plain text, got %q", got)
	}
	h := `<b>hi</b>`
	if got, want := outboundEditText(&h, "hi"), "\x00HT\x01<b>hi</b>\x00hi"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	nul := "a\x00b"
	if got := outboundEditText(&nul, "ab"); got != "ab" {
		t.Fatalf("HTML with a NUL must fall back to plain text, got %q", got)
	}
}
