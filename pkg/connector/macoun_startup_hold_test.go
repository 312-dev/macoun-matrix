package connector

import (
	"sync"
	"testing"
	"time"

	"github.com/lrhodin/corten-matrix/pkg/rustpushgo"
)

// startupHoldTestBuffer is a reorder buffer whose CloudKit-sync state and
// dispatch target are controlled by the test.
type startupHoldTestBuffer struct {
	*messageBuffer
	mu         sync.Mutex
	syncDone   bool
	dispatched []string
}

func newStartupHoldTestBuffer() *startupHoldTestBuffer {
	tb := &startupHoldTestBuffer{}
	tb.messageBuffer = &messageBuffer{
		syncDoneFn: func() bool {
			tb.mu.Lock()
			defer tb.mu.Unlock()
			return tb.syncDone
		},
		dispatchFn: func(msg rustpushgo.WrappedMessage) {
			tb.mu.Lock()
			defer tb.mu.Unlock()
			tb.dispatched = append(tb.dispatched, msg.Uuid)
		},
	}
	return tb
}

func (tb *startupHoldTestBuffer) delivered() []string {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	return append([]string(nil), tb.dispatched...)
}

// waitDelivered waits up to four quiet windows for n dispatched messages.
func (tb *startupHoldTestBuffer) waitDelivered(n int) []string {
	deadline := time.Now().Add(4 * messageBufferQuietWindow)
	for time.Now().Before(deadline) {
		if got := tb.delivered(); len(got) >= n {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	return tb.delivered()
}

// TestStartupHoldDeliversDuringCloudSync: a message that arrives while the
// startup CloudKit sync is still running, for a chat that needs nothing from
// that sync, is delivered after the normal quiet window instead of waiting for
// the sync to finish.
func TestStartupHoldDeliversDuringCloudSync(t *testing.T) {
	tb := newStartupHoldTestBuffer()
	tb.add(rustpushgo.WrappedMessage{Uuid: "live-1", TimestampMs: 1})
	if got := tb.waitDelivered(1); len(got) != 1 {
		t.Fatalf("message was held for the whole CloudKit sync: delivered=%v", got)
	}
}
