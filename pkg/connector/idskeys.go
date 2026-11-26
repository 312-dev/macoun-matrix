// macoun-matrix - A Matrix-iMessage puppeting bridge (fork of corten-matrix).
// SPDX-License-Identifier: MPL-2.0
//
// Outbound send limiter. This file replaces corten-matrix's closed idskeys.go,
// which is a stub in the public source. It is an independent implementation.
//
// What it limits: every Matrix->iMessage send that reaches HandleMatrixMessage
// (text and attachments, before any IDS lookup or APNs traffic for that send):
//
//   - new recipients: a chat with no history (see portalHasHistory) is a first
//     contact. At most 10 new chats per rolling 24 hours and 64 per rolling 7
//     days are admitted; an admitted chat then counts as existing.
//   - per chat: 30 sends per rolling minute
//   - per login, all chats: 300 sends per rolling hour
//
// Why: each send to a handle triggers an IDS key lookup and an APNs send from
// the account's registration. Bursts of first-contact messages are what
// Apple's spam heuristics look for, and a Matrix-side loop (a bot, a
// misbehaving client, a replayed queue) could otherwise drive sends without
// limit. Ordinary conversation never comes near these numbers.
// REVIEW: the per-chat and per-login numbers are judgement calls.
//
// What it does not limit: IDS lookups the bridge makes for other reasons
// (StatusKit/presence, carrier-route checks, backfill), receipts, typing,
// reactions, edits and unsends, which do not pass through HandleMatrixMessage's
// send path.
//
// A refused send fails visibly through the normal Matrix message-status path,
// with a notice in the room. It is never dropped
// while reporting success. The new-recipient state (admitted chats and their
// times) is persisted, encrypted, in the bridge database
// (macoun_limiter_store.go), so a restart does not reset it; the per-chat and
// per-login windows are in memory only. Set MACOUN_OUTBOUND_LIMIT=off to
// disable the limiter.

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
	newChatsDay   int
	newChatsWeek  int
}

const (
	newChatDayWindow  = 24 * time.Hour
	newChatWeekWindow = 7 * 24 * time.Hour
)

var defaultOutboundLimits = outboundLimits{
	perChat:       30,
	perChatWindow: time.Minute,
	global:        300,
	globalWindow:  time.Hour,
	newChatsDay:   10,
	newChatsWeek:  64,
}

type outboundLimiter struct {
	mu       sync.Mutex
	limits   outboundLimits
	now      func() time.Time
	global   []time.Time
	perChat  map[networkid.PortalID][]time.Time
	admitted map[networkid.PortalID]time.Time // first-contact chats, by admission time
}

func newOutboundLimiter(l outboundLimits) *outboundLimiter {
	return &outboundLimiter{
		limits:   l,
		now:      time.Now,
		perChat:  make(map[networkid.PortalID][]time.Time),
		admitted: make(map[networkid.PortalID]time.Time),
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

// pruneAdmittedLocked forgets admissions older than the weekly window. By then
// the chat has bridged messages, so it no longer counts as new anyway.
func (l *outboundLimiter) pruneAdmittedLocked(now time.Time) {
	cutoff := now.Add(-newChatWeekWindow)
	for p, at := range l.admitted {
		if !at.After(cutoff) {
			delete(l.admitted, p)
		}
	}
}

// decide reports whether a send to portal may go ahead and, when it may,
// records it. hasHistory says whether the chat already has messages. A refused
// send records nothing. reason is empty when allowed; newChat reports that a
// first contact was admitted (persistent state changed).
func (l *outboundLimiter) decide(portal networkid.PortalID, hasHistory bool) (allowed bool, reason string, newChat bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()

	l.global = pruneBefore(l.global, now.Add(-l.limits.globalWindow))
	chat := pruneBefore(l.perChat[portal], now.Add(-l.limits.perChatWindow))
	if len(chat) == 0 {
		delete(l.perChat, portal)
	} else {
		l.perChat[portal] = chat
	}
	l.pruneAdmittedLocked(now)

	_, wasAdmitted := l.admitted[portal]
	isNew := !hasHistory && !wasAdmitted
	day, week := 0, len(l.admitted)
	for _, at := range l.admitted {
		if at.After(now.Add(-newChatDayWindow)) {
			day++
		}
	}

	switch {
	case len(chat) >= l.limits.perChat:
		return false, fmt.Sprintf("more than %d messages to this chat in %s", l.limits.perChat, l.limits.perChatWindow), false
	case len(l.global) >= l.limits.global:
		return false, fmt.Sprintf("more than %d messages from this account in %s", l.limits.global, l.limits.globalWindow), false
	case isNew && day >= l.limits.newChatsDay:
		return false, fmt.Sprintf("more than %d new conversations in 24 hours", l.limits.newChatsDay), false
	case isNew && week >= l.limits.newChatsWeek:
		return false, fmt.Sprintf("more than %d new conversations in 7 days", l.limits.newChatsWeek), false
	}

	l.global = append(l.global, now)
	l.perChat[portal] = append(chat, now)
	if isNew {
		l.admitted[portal] = now
	}
	return true, "", isNew
}

// snapshot returns the persistent part of the state.
func (l *outboundLimiter) snapshot() map[networkid.PortalID]time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make(map[networkid.PortalID]time.Time, len(l.admitted))
	for p, at := range l.admitted {
		out[p] = at
	}
	return out
}

// restore merges persisted admissions, dropping any outside the weekly window.
func (l *outboundLimiter) restore(admitted map[networkid.PortalID]time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for p, at := range admitted {
		l.admitted[p] = at
	}
	l.pruneAdmittedLocked(l.now())
}

type limiterEntry struct {
	limiter *outboundLimiter
	store   *limiterStore // nil when persistence is unavailable
}

var (
	outboundLimiters   = map[networkid.UserLoginID]*limiterEntry{}
	outboundLimitersMu sync.Mutex
)

func outboundLimitDisabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("MACOUN_OUTBOUND_LIMIT")))
	return v == "off" || v == "0" || v == "false"
}

// outboundLimiterFor returns the login's limiter, loading persisted state on
// first use. Persistence problems are logged and never block sending.
func (c *IMClient) outboundLimiterFor(ctx context.Context) *limiterEntry {
	var key networkid.UserLoginID
	if c.UserLogin != nil {
		key = c.UserLogin.ID
	}
	outboundLimitersMu.Lock()
	defer outboundLimitersMu.Unlock()
	if e, ok := outboundLimiters[key]; ok {
		return e
	}
	e := &limiterEntry{limiter: newOutboundLimiter(defaultOutboundLimits)}
	if c.Main != nil && c.Main.Bridge != nil && c.Main.Bridge.DB != nil {
		log := zerolog.Ctx(ctx)
		if k, err := loadOrCreateLimiterKey(); err != nil {
			log.Warn().Err(err).Msg("Outbound limiter: no key for persisted state, keeping it in memory only")
		} else {
			e.store = &limiterStore{db: c.Main.Bridge.DB.Database, key: k, loginID: string(key)}
			if admitted, err := e.store.load(ctx); err != nil {
				log.Warn().Err(err).Msg("Outbound limiter: ignoring unreadable persisted state")
			} else {
				e.limiter.restore(admitted)
			}
		}
	}
	outboundLimiters[key] = e
	return e
}

// portalHasHistory reports whether the chat already has history: CloudKit
// knows the chat or holds messages for it, or the bridge has bridged messages in it. On a lookup error it
// answers true, so a database problem never blocks a reply in an existing
// conversation.
func (c *IMClient) portalHasHistory(ctx context.Context, portal *bridgev2.Portal) bool {
	log := zerolog.Ctx(ctx)
	if c.cloudStore != nil {
		var exists bool
		err := c.cloudStore.db.QueryRow(ctx, `SELECT (EXISTS(SELECT 1 FROM cloud_chat WHERE login_id=$1 AND portal_id=$2)
			OR EXISTS(SELECT 1 FROM cloud_message WHERE login_id=$1 AND portal_id=$2))`,
			c.cloudStore.loginID, string(portal.ID)).Scan(&exists)
		if err != nil {
			log.Warn().Err(err).Msg("Outbound limiter: CloudKit history lookup failed, treating chat as existing")
			return true
		}
		if exists {
			return true
		}
	}
	if c.Main == nil || c.Main.Bridge == nil || c.Main.Bridge.DB == nil {
		return c.cloudStore == nil // no evidence either way: only a CloudKit miss counts as new
	}
	last, err := c.Main.Bridge.DB.Message.GetLastPartAtOrBeforeTime(ctx, portal.PortalKey, time.Now().Add(time.Minute))
	if err != nil {
		log.Warn().Err(err).Msg("Outbound limiter: history lookup failed, treating chat as existing")
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
	entry := c.outboundLimiterFor(ctx)
	ok, reason, newChat := entry.limiter.decide(msg.Portal.ID, hasHistory)
	if !ok {
		zerolog.Ctx(ctx).Warn().
			Str("portal_id", string(msg.Portal.ID)).
			Bool("has_history", hasHistory).
			Str("reason", reason).
			Msg("Outbound limiter refused send")
		return nil, errOutboundLimited(reason)
	}
	if newChat && entry.store != nil {
		if err := entry.store.save(ctx, entry.limiter.snapshot()); err != nil {
			zerolog.Ctx(ctx).Warn().Err(err).Msg("Outbound limiter: could not persist state")
		}
	}
	return nil, nil
}
