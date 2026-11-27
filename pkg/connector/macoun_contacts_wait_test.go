package connector

import (
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func TestWaitForContactsReadyBlocksUntilGate(t *testing.T) {
	c := &IMClient{contactsReadyCh: make(chan struct{}), stopChan: make(chan struct{})}
	done := make(chan bool, 1)
	go func() { done <- c.waitForContactsReady(zerolog.Nop(), "test") }()

	select {
	case <-done:
		t.Fatal("returned before the first contact sync")
	case <-time.After(100 * time.Millisecond):
	}
	close(c.contactsReadyCh)
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("returned false after the gate opened")
		}
	case <-time.After(time.Second):
		t.Fatal("did not return after the gate opened")
	}
}

func TestWaitForContactsReadyStopsWithClient(t *testing.T) {
	c := &IMClient{contactsReadyCh: make(chan struct{}), stopChan: make(chan struct{})}
	done := make(chan bool, 1)
	go func() { done <- c.waitForContactsReady(zerolog.Nop(), "test") }()
	close(c.stopChan)
	select {
	case ok := <-done:
		if ok {
			t.Fatal("returned true although the client stopped")
		}
	case <-time.After(time.Second):
		t.Fatal("did not return after the client stopped")
	}
}

func TestWaitForContactsReadyWhenAlreadyReady(t *testing.T) {
	c := &IMClient{contactsReady: true}
	if !c.waitForContactsReady(zerolog.Nop(), "test") {
		t.Fatal("returned false although contacts were ready")
	}
}
