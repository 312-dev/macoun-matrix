// macoun-matrix: tests for the outbound send limiter.
// SPDX-License-Identifier: MPL-2.0

package connector

import (
	"testing"
	"time"

	"maunium.net/go/mautrix/bridgev2/networkid"
)

type fakeClock struct{ t time.Time }

func (f *fakeClock) now() time.Time { return f.t }

func testLimiter(l outboundLimits) (*outboundLimiter, *fakeClock) {
	clk := &fakeClock{t: time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)}
	lim := newOutboundLimiter(l)
	lim.now = clk.now
	return lim, clk
}

func TestOutboundLimiterPerChat(t *testing.T) {
	lim, clk := testLimiter(outboundLimits{perChat: 3, perChatWindow: time.Minute, global: 100, globalWindow: time.Hour, newChats: 100, newChatWindow: time.Hour})
	for i := 0; i < 3; i++ {
		if ok, why := lim.decide("a", true); !ok {
			t.Fatalf("send %d refused: %s", i, why)
		}
	}
	if ok, _ := lim.decide("a", true); ok {
		t.Fatal("fourth send in a minute was allowed")
	}
	if ok, _ := lim.decide("b", true); !ok {
		t.Fatal("another chat was limited by chat a")
	}
	clk.t = clk.t.Add(61 * time.Second)
	if ok, why := lim.decide("a", true); !ok {
		t.Fatalf("send after the window refused: %s", why)
	}
}

func TestOutboundLimiterGlobal(t *testing.T) {
	lim, clk := testLimiter(outboundLimits{perChat: 100, perChatWindow: time.Minute, global: 2, globalWindow: time.Hour, newChats: 100, newChatWindow: time.Hour})
	lim.decide("a", true)
	lim.decide("b", true)
	if ok, _ := lim.decide("c", true); ok {
		t.Fatal("global limit not applied")
	}
	clk.t = clk.t.Add(time.Hour + time.Second)
	if ok, _ := lim.decide("c", true); !ok {
		t.Fatal("global window did not expire")
	}
}

func TestOutboundLimiterNewChats(t *testing.T) {
	lim, _ := testLimiter(outboundLimits{perChat: 100, perChatWindow: time.Minute, global: 100, globalWindow: time.Hour, newChats: 2, newChatWindow: 24 * time.Hour})
	if ok, _ := lim.decide("n1", false); !ok {
		t.Fatal("first new chat refused")
	}
	if ok, _ := lim.decide("n2", false); !ok {
		t.Fatal("second new chat refused")
	}
	if ok, _ := lim.decide("n3", false); ok {
		t.Fatal("third new chat allowed")
	}
	// Admitted new chats keep working, and existing chats are unaffected.
	if ok, _ := lim.decide("n1", false); !ok {
		t.Fatal("follow-up in an admitted new chat refused")
	}
	if ok, _ := lim.decide("old", true); !ok {
		t.Fatal("existing chat refused by the new-chat limit")
	}
}

func TestOutboundLimiterRefusalRecordsNothing(t *testing.T) {
	lim, _ := testLimiter(outboundLimits{perChat: 1, perChatWindow: time.Minute, global: 2, globalWindow: time.Hour, newChats: 100, newChatWindow: time.Hour})
	lim.decide("a", true)
	for i := 0; i < 5; i++ {
		lim.decide("a", true) // refused per chat
	}
	if ok, why := lim.decide(networkid.PortalID("b"), true); !ok {
		t.Fatalf("refused sends consumed the global budget: %s", why)
	}
}

func TestOutboundLimitEnvOff(t *testing.T) {
	t.Setenv("MACOUN_OUTBOUND_LIMIT", "off")
	if !outboundLimitDisabled() {
		t.Fatal("MACOUN_OUTBOUND_LIMIT=off not honoured")
	}
	t.Setenv("MACOUN_OUTBOUND_LIMIT", "")
	if outboundLimitDisabled() {
		t.Fatal("limiter disabled by default")
	}
}
