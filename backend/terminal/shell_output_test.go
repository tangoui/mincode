package terminal

import (
	"encoding/json"
	"sync"
	"testing"

	"mincode/app/backend/core"
)

func TestDispatchLocalShellOutputAlwaysEmitsFrontendEvent(t *testing.T) {
	const sessionID = "local-output-event-test"
	conn := newFakeStreamConn()
	session := &wireStreamSession{conns: map[core.StreamConn]struct{}{conn: {}}}
	session.active.Store(true)

	streamHub.mu.Lock()
	previous := streamHub.sessions[sessionID]
	streamHub.sessions[sessionID] = session
	streamHub.mu.Unlock()
	t.Cleanup(func() {
		streamHub.mu.Lock()
		if previous != nil {
			streamHub.sessions[sessionID] = previous
		} else {
			delete(streamHub.sessions, sessionID)
		}
		streamHub.mu.Unlock()
		_ = conn.Close()
	})

	var mu sync.Mutex
	var emitted []shellOutputPayload
	core.SetEmitter(func(name string, data any) {
		if name != shellOutputEventName(sessionID) {
			return
		}
		encoded, err := json.Marshal(data)
		if err != nil {
			t.Errorf("marshal event: %v", err)
			return
		}
		var payload shellOutputPayload
		if err := json.Unmarshal(encoded, &payload); err != nil {
			t.Errorf("decode event: %v", err)
			return
		}
		mu.Lock()
		emitted = append(emitted, payload)
		mu.Unlock()
	})
	t.Cleanup(func() { core.SetEmitter(nil) })

	dispatchLocalShellOutput(sessionID, "123", 0)

	mu.Lock()
	defer mu.Unlock()
	if len(emitted) != 1 || emitted[0].SessionID != sessionID || emitted[0].Data != "123" {
		t.Fatalf("local output event = %#v, want one complete event", emitted)
	}
	select {
	case <-conn.out:
	default:
		t.Fatal("explicit stream client did not receive local output")
	}
}
