package connector

import (
	"context"
	"html"
	"net/url"
	"regexp"
	"strings"

	"github.com/rs/zerolog"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// Outbound Matrix formatting and mentions. rustpush's parse_html_to_parts
// (lib.rs) turns the HTML passed to SendMessage into iMessage parts: bold,
// italic, underline, strikethrough, line breaks, and <mention uri="...">
// elements. That last one is not Matrix HTML. Here each matrix.to pill whose
// user stands for an iMessage handle is rewritten into it, since only the
// bridge can map a Matrix user to a handle. A pill it cannot map (a Matrix
// user with no iMessage handle) is sent as its plain text.

// pillAnchor matches a matrix.to user link as clients send it: the target is
// the user ID, possibly percent-encoded, optionally followed by ?via= and the
// like; the link text is the pill's display name.
var pillAnchor = regexp.MustCompile(`(?s)<a\s+href="https://matrix\.to/#/([^"?]+)[^"]*"\s*>(.*?)</a>`)

// mxReply matches a reply fallback, which would otherwise be sent as quoted
// text: the reply itself rides on reply_guid.
var mxReply = regexp.MustCompile(`(?s)<mx-reply>.*?</mx-reply>`)

// blockEnd and listItem cover the block structure parse_html_to_parts has no
// notion of: it skips unknown tags, so without this "<p>a</p><p>b</p>" would
// arrive as "ab". Insignificant newlines between tags are dropped first, so
// they do not double the breaks the block ends become.
var (
	tagNewline = regexp.MustCompile(`>\s*\n\s*`)
	blockEnd   = regexp.MustCompile(`(?i)</(p|div|li|h[1-6]|blockquote|pre|tr)\s*>`)
	listItem   = regexp.MustCompile(`(?i)<li(\s[^>]*)?>`)
	// blockOther is the rest of the block markup, which carries no text:
	// opening tags and list and table containers.
	blockOther = regexp.MustCompile(`(?i)</?(p|div|ul|ol|h[1-6]|blockquote|pre|table|thead|tbody|tr|td|th)(\s[^>]*)?>`)
	trailingBr = regexp.MustCompile(`(?i)(<br\s*/?>\s*)+$`)
	// inlineFormat is what parse_html_to_parts turns into iMessage text flags.
	inlineFormat = regexp.MustCompile(`(?i)<(b|strong|i|em|u|del|s|strike)\s*>`)
)

// rewriteOutboundHTML prepares a Matrix formatted_body for parse_html_to_parts.
// resolve maps a Matrix user to an iMessage handle URI, or "" when the user
// has none. It returns the HTML and the number of mentions it produced.
func rewriteOutboundHTML(src string, resolve func(id.UserID) string) (string, int) {
	src = mxReply.ReplaceAllString(src, "")
	if !strings.Contains(src, "<pre") {
		src = tagNewline.ReplaceAllString(src, ">")
	}
	src = listItem.ReplaceAllString(src, "• ")
	src = blockEnd.ReplaceAllString(src, "<br/>")
	src = blockOther.ReplaceAllString(src, "")
	src = trailingBr.ReplaceAllString(src, "")
	mentions := 0
	out := pillAnchor.ReplaceAllStringFunc(src, func(a string) string {
		m := pillAnchor.FindStringSubmatch(a)
		inner := m[2]
		target, err := url.PathUnescape(html.UnescapeString(m[1]))
		if err != nil || !strings.HasPrefix(target, "@") {
			return inner
		}
		uri := resolve(id.UserID(target))
		if uri == "" {
			return inner
		}
		mentions++
		return `<mention uri="` + html.EscapeString(uri) + `">` + inner + `</mention>`
	})
	return out, mentions
}

// outboundMentionURI is the iMessage handle a mentioned Matrix user stands
// for: the logged-in user's own sending handle, or a ghost's identifier,
// which is its handle URI (makeUserID).
func (c *IMClient) outboundMentionURI(userID id.UserID) string {
	if userID == c.UserLogin.UserMXID {
		return c.handle
	}
	if ghostID, ok := c.Main.Bridge.Matrix.ParseGhostMXID(userID); ok {
		return string(ghostID)
	}
	return ""
}

// outboundEditText is the text argument for SendEdit. SendEdit carries text
// only, so an edit with HTML sends it in an envelope, "\x00HT\x01<html>\x00"
// followed by the text, which split_html_envelope (lib.rs) takes apart. HTML
// holding a NUL can't be enveloped and goes as plain text.
func outboundEditText(html *string, text string) string {
	if html == nil || strings.ContainsRune(*html, 0) {
		return text
	}
	return "\x00HT\x01" + *html + "\x00" + text
}

// outboundHTML returns the HTML to send alongside a text message, or nil to
// send plain text. Only a single-chunk iMessage whose text is the event body
// carries it: the text-message relay has no formatting, a split message's
// HTML can't be cut at the same offsets as its chunks, and a link-preview
// envelope means the text sent differs from the body the HTML describes.
// HTML with neither a mention nor inline formatting is not sent either: the
// plain body already says everything iMessage can show, with its line breaks
// exactly as typed.
func (c *IMClient) outboundHTML(ctx context.Context, content *event.MessageEventContent, isSms bool, textToSend string, chunks int) *string {
	if isSms || chunks != 1 || content.Format != event.FormatHTML || content.FormattedBody == "" || textToSend != content.Body {
		return nil
	}
	out, mentions := rewriteOutboundHTML(content.FormattedBody, c.outboundMentionURI)
	if mentions == 0 && !inlineFormat.MatchString(out) {
		return nil
	}
	zerolog.Ctx(ctx).Debug().Int("mentions", mentions).Msg("Sending Matrix formatting to iMessage")
	return &out
}
