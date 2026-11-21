package ai

import (
	"sync"
	"testing"
	"time"
)

type recordedEvents struct {
	mu     sync.Mutex
	events []map[string]any
}

func (r *recordedEvents) emit(payload map[string]any) {
	r.mu.Lock()
	r.events = append(r.events, payload)
	r.mu.Unlock()
}

func (r *recordedEvents) snapshot() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]map[string]any(nil), r.events...)
}

func eventSummary(events []map[string]any) []string {
	out := make([]string, 0, len(events))
	for _, event := range events {
		text, _ := event["text"].(string)
		out = append(out, event["type"].(string)+":"+text)
	}
	return out
}

func TestStreamEmitterBatchesReasoningDeltas(t *testing.T) {
	rec := &recordedEvents{}
	em := &streamEmitter{emit: rec.emit}
	for _, token := range []string{"Let", " me", " think"} {
		em.reasoning(token)
	}
	if got := rec.snapshot(); len(got) != 0 {
		t.Fatalf("reasoning should be buffered, got %v", eventSummary(got))
	}
	deadline := time.Now().Add(time.Second)
	for len(rec.snapshot()) == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	got := eventSummary(rec.snapshot())
	if len(got) != 1 || got[0] != "reasoning:Let me think" {
		t.Fatalf("expected one merged reasoning event, got %v", got)
	}
}

func TestStreamEmitterPreservesReasoningTextToolOrder(t *testing.T) {
	rec := &recordedEvents{}
	em := &streamEmitter{emit: rec.emit}
	em.reasoning("a")
	em.reasoning("b")
	em.chunk("x")
	em.chunk("y")
	em.reasoning("c")
	em.toolCall("1", "tool", "{}")
	em.reasoning("d")
	em.done()

	got := eventSummary(rec.snapshot())
	want := []string{"reasoning:ab", "chunk:xy", "reasoning:c", "tool_call:", "reasoning:d", "done:"}
	if len(got) != len(want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events = %v, want %v", got, want)
		}
	}
	// 窗口到期的定时器不能在 done 之后再推送任何内容。
	time.Sleep(3 * streamEmitterFlushDelay)
	if after := rec.snapshot(); len(after) != len(want) {
		t.Fatalf("late events after done: %v", eventSummary(after))
	}
}

func TestAIStreamHTTPClientIsShared(t *testing.T) {
	first, second := aiStreamHTTPClient(), aiStreamHTTPClient()
	if first != second || first.Transport != aiStreamTransport {
		t.Fatal("AI requests must reuse one client and transport")
	}
	if aiStreamTransport.Proxy != nil {
		t.Fatal("AI transport must not pick up environment proxies")
	}
	if !aiStreamTransport.ForceAttemptHTTP2 || aiStreamTransport.IdleConnTimeout <= 0 {
		t.Fatal("AI transport must enable HTTP/2 and expire idle connections")
	}
	if aiStreamTransport.ResponseHeaderTimeout != 120*time.Second {
		t.Fatal("response header timeout changed")
	}
}
