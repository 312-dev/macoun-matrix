// macoun-matrix - A Matrix-iMessage puppeting bridge (fork of corten-matrix).
// SPDX-License-Identifier: MPL-2.0
//
// Outbound send limiter. This file replaces corten-matrix's closed idskeys.go,
// which is a stub in the public source. It is an independent, deliberately
// simple implementation and does not attempt to reproduce the closed one.
// REVIEW: the limits below are guesses at "well above one person typing, well
// below a runaway loop"; they have not been tuned against Apple's behaviour.
//
// What it limits: every Matrix->iMessage send that reaches HandleMatrixMessage
// (text and attachments, before any IDS lookup or APNs traffic for that send):
//
//   - per chat:            30 sends per rolling minute
//   - per login, all chats: 300 sends per rolling hour
//   - new recipients:      20 chats per rolling 24 hours that have no message in
//     the bridge database yet (a first message to someone). Once admitted, a
//     chat counts as existing for the rest of the process lifetime.
//
// Why: each send to a handle triggers an IDS key lookup and an APNs send from
// the account's registration. Floods and bursts of first-contact messages are
// what Apple's spam heuristics look for, and a Matrix-side loop (a bot, a
// misbehaving client, a replayed queue) could otherwise drive both without
// limit. Ordinary conversation never comes near these numbers.
//
// What it does not limit: IDS lookups the bridge makes for other reasons
// (StatusKit/presence, carrier-route checks, backfill), receipts, typing,
// reactions, edits and unsends, which do not pass through HandleMatrixMessage's
// send path.
//
// A refused send fails visibly through the normal Matrix message-status path,
// with a notice in the room. It is never dropped while reporting success.
// State is in memory only, so a restart resets the windows. Set
// MACOUN_OUTBOUND_LIMIT=off to disable the limiter.

package connector

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
)

type outboundLimits struct {
	perChat       int
	perChatWindow time.Duration
	global        int
	globalWindow  time.Duration
	newChats      int
	newChatWindow time.Duration
}

var defaultOutboundLimits = outboundLimits{
	perChat:       30,
	perChatWindow: time.Minute,
	global:        300,
	globalWindow:  time.Hour,
	newChats:      20,
	newChatWindow: 24 * time.Hour,
}

type outboundLimiter struct {
	mu       sync.Mutex
	limits   outboundLimits
	now      func() time.Time
	global   []time.Time
	perChat  map[networkid.PortalID][]time.Time
	newChats []time.Time
	admitted map[networkid.PortalID]struct{}
}

func newOutboundLimiter(l outboundLimits) *outboundLimiter {
	return &outboundLimiter{
		limits:   l,
		now:      time.Now,
		perChat:  make(map[networkid.PortalID][]time.Time),
		admitted: make(map[networkid.PortalID]struct{}),
	}
}

// pruneBefore drops timestamps older than cutoff from a slice kept in order.
func pruneBefore(ts []time.Time, cutoff time.Time) []time.Time {
	i := 0
	for i < len(ts) && !ts[i].After(cutoff) {
		i++
	}
	return ts[i:]
}

// decide reports whether a send to portal may go ahead and, when it may,
// records it. hasHistory says whether the chat already has messages. A refused
// send records nothing. The returned reason is empty when allowed.
func (l *outboundLimiter) decide(portal networkid.PortalID, hasHistory bool) (bool, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()

	l.global = pruneBefore(l.global, now.Add(-l.limits.globalWindow))
	chat := pruneBefore(l.perChat[portal], now.Add(-l.limits.perChatWindow))
	l.newChats = pruneBefore(l.newChats, now.Add(-l.limits.newChatWindow))
	if len(chat) == 0 {
		delete(l.perChat, portal)
	} else {
		l.perChat[portal] = chat
	}

	_, wasAdmitted := l.admitted[portal]
	isNew := !hasHistory && !wasAdmitted

	switch {
	case len(chat) >= l.limits.perChat:
		return false, fmt.Sprintf("more than %d messages to this chat in %s", l.limits.perChat, l.limits.perChatWindow)
	case len(l.global) >= l.limits.global:
		return false, fmt.Sprintf("more than %d messages from this account in %s", l.limits.global, l.limits.globalWindow)
	case isNew && len(l.newChats) >= l.limits.newChats:
		return false, fmt.Sprintf("more than %d new conversations in %s", l.limits.newChats, l.limits.newChatWindow)
	}

	l.global = append(l.global, now)
	l.perChat[portal] = append(chat, now)
	if isNew {
		l.newChats = append(l.newChats, now)
		l.admitted[portal] = struct{}{}
	}
	return true, ""
}

var outboundLimiters sync.Map // networkid.UserLoginID -> *outboundLimiter

func outboundLimitDisabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("MACOUN_OUTBOUND_LIMIT")))
	return v == "off" || v == "0" || v == "false"
}

func (c *IMClient) outboundLimiter() *outboundLimiter {
	var key networkid.UserLoginID
	if c.UserLogin != nil {
		key = c.UserLogin.ID
	}
	if l, ok := outboundLimiters.Load(key); ok {
		return l.(*outboundLimiter)
	}
	l, _ := outboundLimiters.LoadOrStore(key, newOutboundLimiter(defaultOutboundLimits))
	return l.(*outboundLimiter)
}

// portalHasHistory reports whether the bridge database holds any message in
// the portal. On a lookup error it answers true, so a database problem never
// blocks a reply in an existing conversation.
func (c *IMClient) portalHasHistory(ctx context.Context, portal *bridgev2.Portal) bool {
	if c.Main == nil || c.Main.Bridge == nil || c.Main.Bridge.DB == nil {
		return true
	}
	last, err := c.Main.Bridge.DB.Message.GetLastPartAtOrBeforeTime(ctx, portal.PortalKey, time.Now().Add(time.Minute))
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Outbound limiter: history lookup failed, treating chat as existing")
		return true
	}
	return last != nil
}

func errOutboundLimited(reason string) error {
	return bridgev2.WrapErrorInStatus(
		errors.New("not sent: outbound limit reached (" + reason + "). Wait a little and send again")).
		WithIsCertain(true).
		WithErrorAsMessage().
		WithSendNotice(true).
		WithErrorReason(event.MessageStatusGenericError)
}

// ensureIdentityKeys runs the outbound precheck for a Matrix->iMessage message
// before it is handed to the sender. A non-nil response or error means the
// send was resolved at this stage and must not continue; (nil, nil) lets the
// normal send path proceed. Here the precheck is the outbound limiter above.
func (c *IMClient) ensureIdentityKeys(ctx context.Context, msg *bridgev2.MatrixMessage) (*bridgev2.MatrixMessageResponse, error) {
	if outboundLimitDisabled() || msg == nil || msg.Portal == nil {
		return nil, nil
	}
	hasHistory := c.portalHasHistory(ctx, msg.Portal)
	if ok, reason := c.outboundLimiter().decide(msg.Portal.ID, hasHistory); !ok {
		zerolog.Ctx(ctx).Warn().
			Str("portal_id", string(msg.Portal.ID)).
			Bool("has_history", hasHistory).
			Str("reason", reason).
			Msg("Outbound limiter refused send")
		return nil, errOutboundLimited(reason)
	}
	return nil, nil
}
