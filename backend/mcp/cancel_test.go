package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestToolCallRegistryCancelsRunningCall(t *testing.T) {
	registry := newToolCallRegistry()
	ctx, release := registry.start("call-1")
	defer release()
	registry.cancel("call-1")
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("cancel did not reach the running call")
	}
}

func TestToolCallRegistryRemembersEarlyCancel(t *testing.T) {
	registry := newToolCallRegistry()
	registry.cancel("call-2")
	ctx, release := registry.start("call-2")
	defer release()
	if ctx.Err() == nil {
		t.Fatal("a call cancelled before it started must start cancelled")
	}
	if _, left := registry.cancelled["call-2"]; left {
		t.Fatal("the early cancel must be consumed")
	}
}

func TestToolCallRegistryReleaseAndPrune(t *testing.T) {
	registry := newToolCallRegistry()
	_, release := registry.start("call-3")
	release()
	if len(registry.running) != 0 {
		t.Fatal("release must unregister the call")
	}
	registry.cancelled["stale"] = time.Now().Add(-2 * toolCallPreCancelTTL)
	_, releaseOther := registry.start("call-4")
	defer releaseOther()
	if _, left := registry.cancelled["stale"]; left {
		t.Fatal("stale early cancels must be pruned")
	}
	if ctx, releaseEmpty := registry.start(""); ctx.Err() != nil {
		t.Fatal("calls without an id are not cancellable but must run")
	} else {
		releaseEmpty()
	}
}

type recordingWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *recordingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *recordingWriter) lines() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.Split(strings.TrimSpace(w.buf.String()), "\n")
}

func TestStdioToolCallCancelNotifiesServer(t *testing.T) {
	stdin := &recordingWriter{}
	conn := &stdioConn{serverID: "test", stdin: stdin, pending: map[uint64]chan rpcReply{}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := conn.callTool(ctx, "slow", json.RawMessage(`{}`))
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	for len(stdin.lines()) < 1 || stdin.lines()[0] == "" {
		if time.Now().After(deadline) {
			t.Fatal("tools/call was not sent")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, errToolCallCancelled) {
			t.Fatalf("err = %v, want errToolCallCancelled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled call did not return")
	}
	if len(conn.pending) != 0 {
		t.Fatal("the cancelled request must leave no pending reply")
	}
	lines := stdin.lines()
	if len(lines) != 2 {
		t.Fatalf("messages = %q", lines)
	}
	var call, notice struct {
		ID     uint64         `json:"id"`
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	_ = json.Unmarshal([]byte(lines[0]), &call)
	_ = json.Unmarshal([]byte(lines[1]), &notice)
	if call.Method != "tools/call" || notice.Method != "notifications/cancelled" || notice.Params["requestId"] != float64(call.ID) {
		t.Fatalf("call = %+v, notice = %+v", call, notice)
	}
}
