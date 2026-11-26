// graft: test for the outbound rich-link envelope (vanishing-message bug).
// SPDX-License-Identifier: MPL-2.0

package connector

import (
	"context"
	"strings"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
)

// A message whose only "link" is a file name must go out as plain text. When
// the bridge wraps it in a rich-link envelope with no page behind it, Messages
// on the recipient's device renders an empty link balloon and the text is never
// shown, while the sender sees a normal sent message.
func TestOutboundFilenameIsNotSentAsRichLink(t *testing.T) {
	c := &IMClient{Main: &IMConnector{Bridge: &bridgev2.Bridge{}}}
	for _, body := range []string{
		"the backup is in notes.db",
		"restore backup.old first",
		"cp corten-matrix.db.pre-tombstones /tmp",
	} {
		got := c.convertURLPreviewToIMessage(context.Background(), &event.MessageEventContent{
			MsgType: event.MsgText,
			Body:    body,
		})
		if strings.HasPrefix(got, "\x00RL") {
			t.Errorf("%q was wrapped as a rich link with no preview: %q", body, got)
		}
		if got != body {
			t.Errorf("%q was rewritten to %q", body, got)
		}
	}
}
