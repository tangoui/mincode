package terminalactivity

import (
	"context"
	"testing"
	"time"
)

func pollerRunning() bool {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.pollerRunning
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestInboxPollerStopsWhenIdleAndRestartsOnRegistration(t *testing.T) {
	previousInterval := inboxPollInterval
	inboxPollInterval = 5 * time.Millisecond
	state.mu.Lock()
	state.initialized = true
	state.inboxDir = t.TempDir()
	state.registrations = make(map[string]registration)
	state.mu.Unlock()
	t.Cleanup(func() {
		Shutdown(context.Background())
		inboxPollInterval = previousInterval
	})

	state.mu.Lock()
	startPollerLocked()
	state.mu.Unlock()
	waitFor(t, "idle poller to exit", func() bool { return !pollerRunning() })

	state.mu.Lock()
	state.registrations["token"] = registration{PaneID: "pane-1", CreatedAt: time.Now()}
	startPollerLocked()
	firstDone := state.done
	state.mu.Unlock()
	time.Sleep(30 * time.Millisecond)
	if !pollerRunning() {
		t.Fatal("poller exited while a registration is live")
	}

	unregisterPane("pane-1")
	waitFor(t, "poller to exit after last unregister", func() bool { return !pollerRunning() })
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("poller goroutine did not finish")
	}

	state.mu.Lock()
	state.registrations["token-2"] = registration{PaneID: "pane-2", CreatedAt: time.Now()}
	startPollerLocked()
	secondDone := state.done
	state.mu.Unlock()
	if !pollerRunning() {
		t.Fatal("poller did not restart on registration")
	}
	Shutdown(context.Background())
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not stop the running poller")
	}
	if pollerRunning() {
		t.Fatal("poller still marked running after shutdown")
	}
}
