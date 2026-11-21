package debug

import (
	"encoding/json"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	// outputCoalesceWindow is how long `output` events are held for merging.
	outputCoalesceWindow = 50 * time.Millisecond
	// outputBatchMaxBytes caps the text of one merged `output` event.
	outputBatchMaxBytes = 64 << 10
)

// eventPump forwards one session's events to emit. Consecutive plain
// `output` events (body with only category and output) of the same category
// are merged for up to one window, each batch holding at most maxBatch bytes
// of text; a category change flushes the pending batch. Every other event
// is emitted immediately, after any pending output, so arrival order is kept
// across categories and kinds. push is safe for concurrent use and never
// blocks on anything but emit.
type eventPump struct {
	window   time.Duration
	maxBatch int
	emit     func(Event)

	mu      sync.Mutex
	closed  bool
	order   []string // category with pending text (at most one)
	pending map[string]*outputBatch
	timer   *time.Timer
}

type outputBatch struct {
	sessionID string
	text      []byte
}

func newEventPump(window time.Duration, maxBatch int, emit func(Event)) *eventPump {
	return &eventPump{window: window, maxBatch: maxBatch, emit: emit, pending: map[string]*outputBatch{}}
}

// push forwards ev, possibly after merging it with later output.
func (p *eventPump) push(ev Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	category, text, ok := plainOutput(ev)
	if !ok {
		p.flushLocked()
		p.emit(ev)
		return
	}
	// Only consecutive output of one category is merged: a category change
	// or a full batch flushes what is pending, so arrival order is kept.
	if len(p.order) > 0 && p.order[0] != category {
		p.flushLocked()
	}
	if b := p.pending[category]; b != nil && len(b.text)+len(text) > p.maxBatch {
		p.flushLocked()
	}
	if len(text) > p.maxBatch { // a single chunk larger than a batch
		p.flushLocked()
		for len(text) > p.maxBatch {
			cut := utf8Cut(text, p.maxBatch)
			p.emitOutputLocked(ev.SessionID, category, text[:cut])
			text = text[cut:]
		}
	}
	if text == "" {
		return
	}
	b := p.pending[category]
	if b == nil {
		b = &outputBatch{sessionID: ev.SessionID}
		p.pending[category] = b
		p.order = append(p.order, category)
	}
	b.text = append(b.text, text...)
	if p.timer == nil {
		p.timer = time.AfterFunc(p.window, p.flush)
	}
}

// close emits pending output; later pushes are dropped.
func (p *eventPump) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.flushLocked()
	p.closed = true
}

func (p *eventPump) flush() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.flushLocked()
}

func (p *eventPump) flushLocked() {
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
	for _, category := range p.order {
		b := p.pending[category]
		delete(p.pending, category)
		p.emitOutputLocked(b.sessionID, category, string(b.text))
	}
	p.order = p.order[:0]
}

func (p *eventPump) emitOutputLocked(sessionID, category, text string) {
	body, err := json.Marshal(map[string]string{"category": category, "output": text})
	if err != nil {
		return // unreachable: strings always encode
	}
	p.emit(Event{SessionID: sessionID, Event: "output", Body: body})
}

// plainOutput reports whether ev is an `output` event whose body holds only
// category and output, and returns them (category defaults to "console").
func plainOutput(ev Event) (category, text string, ok bool) {
	if ev.Event != "output" {
		return "", "", false
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(ev.Body, &body) != nil {
		return "", "", false
	}
	for key := range body {
		if key != "category" && key != "output" {
			return "", "", false
		}
	}
	if raw, has := body["category"]; has && json.Unmarshal(raw, &category) != nil {
		return "", "", false
	}
	if json.Unmarshal(body["output"], &text) != nil {
		return "", "", false
	}
	if category == "" {
		category = "console"
	}
	return category, text, true
}

// utf8Cut returns the largest index ≤ max that does not split a rune in s,
// or max itself when no rune boundary is found (invalid UTF-8).
func utf8Cut(s string, max int) int {
	for i := max; i > 0 && i > max-utf8.UTFMax; i-- {
		if utf8.RuneStart(s[i]) {
			return i
		}
	}
	return max
}
