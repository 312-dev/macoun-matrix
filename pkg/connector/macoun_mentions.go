package connector

import (
	"context"
	"html"
	"regexp"
	"strings"

	"github.com/rs/zerolog"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/lrhodin/corten-matrix/pkg/rustpushgo"
)

// Inbound iMessage formatting and mentions. rustpush's parts_to_html renders a
// message with bold/italic/underline/strikethrough, text effects or mentions
// as HTML, each mention as <a href="tel:+1...">@Name</a>. Here that HTML
// becomes the Matrix formatted_body, with each mention rewritten to a
// matrix.to link to the mentioned user and listed in m.mentions, so clients
// render pills and notify the person mentioned.

// mentionAnchor matches the anchors parts_to_html emits: an escaped handle
// URI and an escaped display name prefixed with "@".
var mentionAnchor = regexp.MustCompile(`<a href="([^"]*)">@([^<]*)</a>`)

// rewriteMentionHTML turns parts_to_html output into a Matrix formatted_body.
// resolve maps an iMessage handle URI (tel:/mailto:) to a Matrix user; a
// mention it cannot resolve is left as its plain "@Name" text. Line breaks
// become <br/>, since parts_to_html keeps them as raw newlines.
func rewriteMentionHTML(src string, resolve func(uri string) id.UserID) (string, []id.UserID) {
	var users []id.UserID
	seen := map[id.UserID]bool{}
	out := mentionAnchor.ReplaceAllStringFunc(src, func(a string) string {
		m := mentionAnchor.FindStringSubmatch(a)
		uri, name := html.UnescapeString(m[1]), html.UnescapeString(m[2])
		mxid := resolve(uri)
		if mxid == "" {
			return html.EscapeString("@" + name)
		}
		if !seen[mxid] {
			seen[mxid] = true
			users = append(users, mxid)
		}
		return `<a href="` + html.EscapeString("https://matrix.to/#/"+string(mxid)) + `">` + html.EscapeString(name) + `</a>`
	})
	return strings.ReplaceAll(out, "\n", "<br/>"), users
}

// convertMessageWithMentions is convertMessage plus the message's formatting
// and mentions. It formats single-part text only: a message long enough to
// be split across events keeps plain text, since the HTML can't be cut at
// the same offsets as the body.
func (c *IMClient) convertMessageWithMentions(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, msg *rustpushgo.WrappedMessage) (*bridgev2.ConvertedMessage, error) {
	cm, err := convertMessage(ctx, portal, intent, msg)
	if err != nil || msg.Html == nil || *msg.Html == "" || len(cm.Parts) != 1 {
		return cm, err
	}
	content := cm.Parts[0].Content
	if content == nil || content.MsgType != event.MsgText || content.Format != "" {
		return cm, nil
	}
	formatted, users := rewriteMentionHTML(*msg.Html, func(uri string) id.UserID {
		return c.mentionTarget(portal, uri)
	})
	before := *content
	content.Format = event.FormatHTML
	content.FormattedBody = formatted
	if len(users) > 0 {
		content.Mentions = &event.Mentions{UserIDs: users}
	}
	if measuredSize(content, false) > maxTextContentBytes {
		*content = before
		return cm, nil
	}
	zerolog.Ctx(ctx).Debug().Int("mentions", len(users)).Msg("Bridged iMessage formatting as HTML")
	return cm, nil
}

// mentionTarget maps a mentioned iMessage handle to its Matrix user: the
// logged-in user's own Matrix account for one of their handles, otherwise
// the handle's ghost.
func (c *IMClient) mentionTarget(portal *bridgev2.Portal, uri string) id.UserID {
	handle := normalizeIdentifierForPortalID(uri)
	if handle == "" {
		return ""
	}
	if c.isMyHandle(handle) {
		return c.UserLogin.UserMXID
	}
	if portal == nil || portal.Bridge == nil || portal.Bridge.Matrix == nil {
		return ""
	}
	return portal.Bridge.Matrix.GhostIntent(makeUserID(handle)).GetMXID()
}
