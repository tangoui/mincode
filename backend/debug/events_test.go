package debug

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

type pumpRecorder struct {
	mu     sync.Mutex
	events []Event
}

func (r *pumpRecorder) emit(ev Event) {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
}

func (r *pumpRecorder) snapshot() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.events...)
}

// waitN waits until at least n events were emitted.
func (r *pumpRecorder) waitN(t *testing.T, n int) []Event {
	t.Helper()
	deadline := time.Now().Add(testTimeout)
	for time.Now().Before(deadline) {
		if evs := r.snapshot(); len(evs) >= n {
			return evs
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("got %d events, want %d: %s", len(r.snapshot()), n, describe(r.snapshot()))
	return nil
}

func outputEv(category, text string) Event {
	body, _ := json.Marshal(map[string]string{"category": category, "output": text})
	return Event{SessionID: "s1", Event: "output", Body: body}
}

// describe renders events as "event[category:output]" for assertions.
func describe(evs []Event) string {
	parts := make([]string, 0, len(evs))
	for _, ev := range evs {
		if ev.Event != "output" {
			parts = append(parts, ev.Event)
			continue
		}
		var b map[string]any
		_ = json.Unmarshal(ev.Body, &b)
		extra := ""
		if _, ok := b["source"]; ok {
			extra = "+source"
		}
		parts = append(parts, "output["+toString(b["category"])+":"+toString(b["output"])+extra+"]")
	}
	return strings.Join(parts, " ")
}

func toString(v any) string {
	s, _ := v.(string)
	return s
}

func TestOutputCoalescesConsecutiveSameCategory(t *testing.T) {
	rec := &pumpRecorder{}
	p := newEventPump(50*time.Millisecond, 64<<10, rec.emit)
	p.push(outputEv("stdout", "a"))
	p.push(outputEv("stdout", "b"))
	p.push(outputEv("stdout", "c"))
	if n := len(rec.snapshot()); n != 0 {
		t.Fatalf("output emitted before the coalescing window: %d events", n)
	}
	if got := describe(rec.waitN(t, 1)); got != "output[stdout:abc]" {
		t.Fatalf("emitted %s", got)
	}
	time.Sleep(80 * time.Millisecond)
	if n := len(rec.snapshot()); n != 1 {
		t.Fatalf("extra emissions: %s", describe(rec.snapshot()))
	}
}

func TestOutputCategoryChangeKeepsOrder(t *testing.T) {
	rec := &pumpRecorder{}
	p := newEventPump(50*time.Millisecond, 64<<10, rec.emit)
	p.push(outputEv("stdout", "a"))
	p.push(outputEv("stderr", "x"))
	p.push(outputEv("stdout", "b"))
	p.push(Event{SessionID: "s1", Event: "output", Body: json.RawMessage(`{"output":"c"}`)})
	p.push(Event{SessionID: "s1", Event: "output", Body: json.RawMessage(`{"output":"d"}`)})
	got := describe(rec.waitN(t, 4))
	if got != "output[stdout:a] output[stderr:x] output[stdout:b] output[console:cd]" {
		t.Fatalf("emitted %s", got)
	}
}

func TestNonOutputEventsImmediateAndOrdered(t *testing.T) {
	rec := &pumpRecorder{}
	p := newEventPump(time.Hour, 64<<10, rec.emit)
	p.push(outputEv("stdout", "a"))
	p.push(Event{SessionID: "s1", Event: "stopped"})
	p.push(Event{SessionID: "s1", Event: "continued"})
	if got := describe(rec.snapshot()); got != "output[stdout:a] stopped continued" {
		t.Fatalf("emitted %s", got)
	}
}

func TestOutputWithExtraFieldsNotMerged(t *testing.T) {
	rec := &pumpRecorder{}
	p := newEventPump(time.Hour, 64<<10, rec.emit)
	p.push(outputEv("stdout", "a"))
	p.push(Event{SessionID: "s1", Event: "output", Body: json.RawMessage(`{"category":"stdout","output":"b","source":{"path":"/x.py"}}`)})
	if got := describe(rec.snapshot()); got != "output[stdout:a] output[stdout:b+source]" {
		t.Fatalf("emitted %s", got)
	}
}

func TestOutputBatchLimit(t *testing.T) {
	rec := &pumpRecorder{}
	p := newEventPump(time.Hour, 8, rec.emit)
	p.push(outputEv("stdout", "12345"))
	p.push(outputEv("stdout", "6789")) // would make 9 bytes: flush "12345" first
	if got := describe(rec.snapshot()); got != "output[stdout:12345]" {
		t.Fatalf("emitted %s", got)
	}
	p.push(outputEv("stderr", "abcdefghijklmnopqrst")) // larger than a batch: split
	p.close()
	got := describe(rec.snapshot())
	want := "output[stdout:12345] output[stdout:6789] output[stderr:abcdefgh] output[stderr:ijklmnop] output[stderr:qrst]"
	if got != want {
		t.Fatalf("emitted %s\nwant    %s", got, want)
	}
}

func TestOutputSplitKeepsUTF8(t *testing.T) {
	rec := &pumpRecorder{}
	p := newEventPump(time.Hour, 4, rec.emit)
	p.push(outputEv("stdout", "中文")) // 6 bytes
	p.close()
	if got := describe(rec.snapshot()); got != "output[stdout:中] output[stdout:文]" {
		t.Fatalf("emitted %s", got)
	}
}

func TestPumpCloseFlushesThenDrops(t *testing.T) {
	rec := &pumpRecorder{}
	p := newEventPump(time.Hour, 64<<10, rec.emit)
	p.push(outputEv("stdout", "a"))
	p.close()
	p.push(outputEv("stdout", "late"))
	p.push(Event{SessionID: "s1", Event: "stopped"})
	if got := describe(rec.snapshot()); got != "output[stdout:a]" {
		t.Fatalf("emitted %s", got)
	}
}
