package connector

import (
	"testing"

	"maunium.net/go/mautrix/bridgev2/status"

	"github.com/lrhodin/corten-matrix/pkg/rustpushgo"
)

// TestMacounProviderOutageOneNoticePerOutage plays a provider that fails,
// keeps failing in more ways, and recovers: one outage state, one recovery
// state, nothing per failed call.
func TestMacounProviderOutageOneNoticePerOutage(t *testing.T) {
	var tracker providerOutageTracker
	a, v := rustpushgo.ProviderAnisette, rustpushgo.ProviderValidationData
	masks := []uint64{0, 0, a, a, a | v, v, a, 0, 0, 0}
	var sent []status.BridgeState
	for _, mask := range masks {
		if state := tracker.observe(mask, true); state != nil {
			sent = append(sent, *state)
		}
	}
	if len(sent) != 2 {
		t.Fatalf("sent %d states, want one outage and one recovery: %+v", len(sent), sent)
	}
	outage, recovery := sent[0], sent[1]

	// The outage must leave the login connected and still be an error for
	// bridge_status_notices: errors, which bridgev2 decides by this user action.
	if outage.StateEvent != status.StateConnected || outage.Error != providerUnreachableError ||
		outage.UserAction != status.UserActionOpenNative || outage.Message == "" {
		t.Fatalf("outage state: %+v", outage)
	}
	if recovery.StateEvent != status.StateConnected || recovery.Error != "" || recovery.UserAction != "" {
		t.Fatalf("recovery state: %+v", recovery)
	}

	// A second outage is reported again.
	if state := tracker.observe(v, true); state == nil || state.Error != providerUnreachableError {
		t.Fatalf("second outage not reported: %+v", state)
	}
}

// TestMacounProviderOutageWaitsForConnectedLogin: no CONNECTED-based state is
// sent while the login is not connected; the outage is reported once it is,
// and a login that connects during a reported outage does not read as a
// recovery.
func TestMacounProviderOutageWaitsForConnectedLogin(t *testing.T) {
	var tracker providerOutageTracker
	a := rustpushgo.ProviderAnisette
	if state := tracker.observe(a, false); state != nil {
		t.Fatalf("reported an outage for a login that is not connected: %+v", state)
	}
	if state := tracker.connectedState(); state.Error != "" {
		t.Fatalf("unreported outage leaked into the connected state: %+v", state)
	}
	if state := tracker.observe(a, true); state == nil || state.Error != providerUnreachableError {
		t.Fatalf("outage not reported once connected: %+v", state)
	}
	// The login reconnects while the provider is still away.
	if state := tracker.connectedState(); state.Error != providerUnreachableError {
		t.Fatalf("connecting during an outage must keep the outage state, got %+v", state)
	}
	// Recovery while disconnected is not sent; the next connect is plain.
	if state := tracker.observe(0, false); state != nil {
		t.Fatalf("recovery sent while not connected: %+v", state)
	}
	if state := tracker.connectedState(); state.Error != "" || state.StateEvent != status.StateConnected {
		t.Fatalf("connected state after recovery: %+v", state)
	}
}
