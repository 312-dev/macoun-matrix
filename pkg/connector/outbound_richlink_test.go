// macoun-matrix: test for the outbound rich-link envelope (vanishing-message bug).
// SPDX-License-Identifier: MPL-2.0

package connector

import (
	"context"
	"strings"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
)

// fakePreviews makes the outbound og: scraper return the given title for the
// listed URLs and nothing for anything else, and records what was fetched.
func fakePreviews(t *testing.T, titles map[string]string) *[]string {
	t.Helper()
	var fetched []string
	orig := outboundPageMetadata
	outboundPageMetadata = func(_ context.Context, u string) map[string]string {
		fetched = append(fetched, u)
		if title, ok := titles[u]; ok {
			return map[string]string{"title": title}
		}
		return map[string]string{}
	}
	t.Cleanup(func() { outboundPageMetadata = orig })
	return &fetched
}

func convertText(c *IMClient, body string) string {
	return c.convertURLPreviewToIMessage(context.Background(), &event.MessageEventContent{
		MsgType: event.MsgText,
		Body:    body,
	})
}

// A message whose only "link" is a file name must go out as plain text. When
// the bridge wraps it in a rich-link envelope with no page behind it, Messages
// on the recipient's device renders an empty link balloon and the text is never
// shown, while the sender sees a normal sent message.
func TestOutboundFilenameIsNotSentAsRichLink(t *testing.T) {
	fakePreviews(t, nil)
	c := &IMClient{Main: &IMConnector{Bridge: &bridgev2.Bridge{}}}
	for _, body := range []string{
		"the backup is in notes.db",
		"restore backup.old first",
		"cp corten-matrix.db.pre-tombstones /tmp",
	} {
		got := convertText(c, body)
		if strings.HasPrefix(got, "\x00RL") {
			t.Errorf("%q was wrapped as a rich link with no preview: %q", body, got)
		}
		if got != body {
			t.Errorf("%q was rewritten to %q", body, got)
		}
	}
}

// A link with a real preview keeps its rich link, including when a file name
// that isLikelyURL rejects comes first in the message.
func TestOutboundRealLinkKeepsRichLink(t *testing.T) {
	fetched := fakePreviews(t, map[string]string{"https://example.com": "Example Domain"})
	c := &IMClient{Main: &IMConnector{Bridge: &bridgev2.Bridge{}}}
	body := "see main.go and example.com"
	got := convertText(c, body)
	want := "\x00RL\x01example.com\x01https://example.com\x01Example Domain\x01\x00" + body
	if got != want {
		t.Fatalf("got %q, want %q (fetched %v)", got, want, *fetched)
	}
}
