// macoun-matrix - A Matrix-iMessage puppeting bridge (fork of corten-matrix).
// SPDX-License-Identifier: MPL-2.0
//
// macoun: report an outage of the validation-data / anisette provider (the Mac
// relay) as a bridge state, so it shows up in Matrix and not only in the log.
//
// Messaging does not depend on the provider from minute to minute: sending and
// receiving keep working while it is away, and what needs it (IDS
// re-registration, Apple ID token refresh, CloudKit) retries on its own. The
// state therefore must not say the login is disconnected or logged out. The
// codes bridgev2 offers were checked against what it does with them
// (bridgev2/bridgestate.go, mautrix v0.28):
//
//   - UNKNOWN_ERROR posts a notice at once, but also makes bridgev2 tear the
//     login down and reconnect it (unknownErrorReconnect); client.go uses it
//     for exactly that. Wrong for a dependency the login can live without.
//   - TRANSIENT_DISCONNECT tells clients the login is disconnected, and with
//     bridge_status_notices: errors the notice only appears after three
//     minutes in that state.
//   - BAD_CREDENTIALS and LOGGED_OUT ask the user to log in again.
//   - CONNECTED with user_action OPEN_NATIVE is the one combination that keeps
//     the login shown as connected and still counts as an error for
//     bridge_status_notices: errors (sendNotice treats that user action as an
//     error whatever the state), so the notice is posted immediately and the
//     next plain CONNECTED is posted as the recovery. OPEN_NATIVE means "look
//     at the native side", which here is the Mac.
//
// So an outage is CONNECTED + error code macoun-provider-unreachable +
// OPEN_NATIVE + a message, and recovery is a plain CONNECTED with a message.
// With bridge_status_notices: none nothing is posted; the states still go to
// the homeserver's bridge-status endpoint when one is configured.
//
// The Rust side records the final outcome of every provider call
// (record_provider_result); this file polls that and turns changes into at
// most one outage state and one recovery state per outage.

package connector

import (
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2/status"

	"github.com/lrhodin/corten-matrix/pkg/rustpushgo"
)

const (
	// providerUnreachableError is the bridge state error code of an outage.
	providerUnreachableError status.BridgeStateErrorCode = "macoun-provider-unreachable"

	// providerStatusPollInterval is how often the outage mask is read. A
	// provider call happens at most about once a minute, so this adds little
	// delay to a notice.
	providerStatusPollInterval = 30 * time.Second
)

// providerOutageTracker turns successive readings of the provider outage mask
// into bridge states, one per outage and one per recovery.
type providerOutageTracker struct {
	mu sync.Mutex
	// announced is the mask that was reported with the current outage, 0 when
	// no outage has been reported.
	announced uint64
}

func providerCallNames(mask uint64) []string {
	var names []string
	if mask&rustpushgo.ProviderValidationData != 0 {
		names = append(names, "validation data")
	}
	if mask&rustpushgo.ProviderAnisette != 0 {
		names = append(names, "anisette")
	}
	return names
}

// providerOutageState is the bridge state for a login that works but whose
// provider did not answer.
func providerOutageState(mask uint64) status.BridgeState {
	return status.BridgeState{
		StateEvent: status.StateConnected,
		Error:      providerUnreachableError,
		UserAction: status.UserActionOpenNative,
		Message: "The Mac relay did not answer (" + strings.Join(providerCallNames(mask), ", ") +
			"). Messages are still sent and received. Apple registration and token refresh are retried automatically; check that the Mac is awake and reachable.",
	}
}

func providerRecoveredState() status.BridgeState {
	return status.BridgeState{
		StateEvent: status.StateConnected,
		Message:    "The Mac relay answers again.",
	}
}

// observe takes the current outage mask and whether the login is currently in
// the CONNECTED state, and returns the state to send, or nil.
//
// An outage is reported once, when the mask first becomes non-zero; further
// failing calls, or a second kind of call starting to fail, report nothing
// more. Recovery is reported once, when the mask returns to zero. Nothing is
// reported while the login is not connected: a CONNECTED-based state would be
// false then, and connectedState carries the outage when the login connects.
func (t *providerOutageTracker) observe(mask uint64, connected bool) *status.BridgeState {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch {
	case mask != 0 && t.announced == 0:
		if !connected {
			return nil
		}
		t.announced = mask
		state := providerOutageState(mask)
		return &state
	case mask == 0 && t.announced != 0:
		t.announced = 0
		if !connected {
			return nil
		}
		state := providerRecoveredState()
		return &state
	}
	return nil
}

// connectedState is the state to send when the login becomes connected: plain
// CONNECTED, or the outage state when an outage has been reported and is not
// over, so that connecting does not read as the provider having recovered.
func (t *providerOutageTracker) connectedState() status.BridgeState {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.announced != 0 {
		return providerOutageState(t.announced)
	}
	return status.BridgeState{StateEvent: status.StateConnected}
}

// runProviderStatusLoop polls the provider outage mask until stopChan closes.
func (c *IMClient) runProviderStatusLoop(log zerolog.Logger, stopChan <-chan struct{}) {
	ticker := time.NewTicker(providerStatusPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stopChan:
			return
		case <-ticker.C:
			connected := c.UserLogin.BridgeState.GetPrevUnsent().StateEvent == status.StateConnected
			state := c.providerOutage.observe(rustpushgo.ProviderOutageMask(), connected)
			if state == nil {
				continue
			}
			if state.Error == providerUnreachableError {
				log.Warn().Str("message", state.Message).Msg("macoun: provider outage reported as bridge state")
			} else {
				log.Info().Msg("macoun: provider recovery reported as bridge state")
			}
			c.UserLogin.BridgeState.Send(*state)
		}
	}
}
