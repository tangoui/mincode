package debug

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeAdapter is a scripted in-memory debug adapter for tests. It owns one end
// of a net.Pipe; the other end is handed to the code under test.
//
// Requests from the client are recorded (Commands) and dispatched to the
// handler registered for their command via Handle. Commands without a handler
// get an empty success response. Handlers run on the fake's read goroutine and
// may reply later (store the request and call Respond from anywhere). Responses
// the client sends to reverse requests are delivered on Responses.
type fakeAdapter struct {
	t    testing.TB
	conn net.Conn
	r    *bufio.Reader

	wmu sync.Mutex // serializes writes to conn
	seq atomic.Int64

	mu       sync.Mutex
	commands []string
	requests []Message
	handlers map[string]func(req Message)

	responses chan Message
	done      chan struct{}
}

// newFakeAdapter returns a fake adapter and the client end of its pipe. The
// fake does not process messages until Start is called, so register handlers
// first. Both ends are closed on test cleanup.
func newFakeAdapter(t testing.TB) (*fakeAdapter, io.ReadWriteCloser) {
	t.Helper()
	adapterEnd, clientEnd := net.Pipe()
	fa := &fakeAdapter{
		t:         t,
		conn:      adapterEnd,
		r:         bufio.NewReader(adapterEnd),
		handlers:  map[string]func(Message){},
		responses: make(chan Message, 16),
		done:      make(chan struct{}),
	}
	t.Cleanup(func() {
		_ = adapterEnd.Close()
		_ = clientEnd.Close()
	})
	return fa, clientEnd
}

// Handle registers h for requests with the given command. Call before Start.
func (fa *fakeAdapter) Handle(command string, h func(req Message)) {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	fa.handlers[command] = h
}

// Start runs the fake's read loop until the pipe closes or a frame is bad.
func (fa *fakeAdapter) Start() {
	go func() {
		defer close(fa.done)
		for {
			m, err := ReadMessage(fa.r, 4<<20)
			if err != nil {
				return
			}
			switch m.Type {
			case "request":
				fa.mu.Lock()
				fa.commands = append(fa.commands, m.Command)
				fa.requests = append(fa.requests, m)
				h := fa.handlers[m.Command]
				fa.mu.Unlock()
				if h != nil {
					h(m)
				} else {
					fa.Respond(m, true, nil, "")
				}
			case "response":
				fa.responses <- m
			}
		}
	}()
}

// Commands returns the commands received so far, in arrival order.
func (fa *fakeAdapter) Commands() []string {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	return append([]string(nil), fa.commands...)
}

// Requests returns the requests received so far with the given command.
func (fa *fakeAdapter) Requests(command string) []Message {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	var out []Message
	for _, m := range fa.requests {
		if m.Command == command {
			out = append(out, m)
		}
	}
	return out
}

// Respond answers req. body is JSON-encoded unless nil.
func (fa *fakeAdapter) Respond(req Message, success bool, body any, message string) {
	ok := success
	fa.write(Message{
		Type:       "response",
		RequestSeq: req.Seq,
		Command:    req.Command,
		Success:    &ok,
		Message:    message,
		Body:       fa.marshal(body),
	})
}

// SendEvent emits an adapter event. body is JSON-encoded unless nil.
func (fa *fakeAdapter) SendEvent(event string, body any) {
	fa.write(Message{Type: "event", Event: event, Body: fa.marshal(body)})
}

// SendRequest emits a reverse request and returns its seq.
func (fa *fakeAdapter) SendRequest(command string, args any) int64 {
	return fa.write(Message{Type: "request", Command: command, Arguments: fa.marshal(args)})
}

// WriteRaw writes raw bytes to the client (for malformed-frame tests).
func (fa *fakeAdapter) WriteRaw(b []byte) {
	fa.wmu.Lock()
	defer fa.wmu.Unlock()
	if _, err := fa.conn.Write(b); err != nil && !errors.Is(err, io.ErrClosedPipe) {
		fa.t.Errorf("fake adapter raw write: %v", err)
	}
}

func (fa *fakeAdapter) write(m Message) int64 {
	fa.wmu.Lock()
	defer fa.wmu.Unlock()
	m.Seq = fa.seq.Add(1)
	if err := WriteMessage(fa.conn, m); err != nil && !errors.Is(err, io.ErrClosedPipe) {
		fa.t.Errorf("fake adapter write %s %s%s: %v", m.Type, m.Command, m.Event, err)
	}
	return m.Seq
}

func (fa *fakeAdapter) marshal(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	if raw, ok := v.(json.RawMessage); ok {
		return raw
	}
	b, err := json.Marshal(v)
	if err != nil {
		// Errorf, not Fatalf: this may run on the fake's goroutine.
		fa.t.Errorf("fake adapter marshal: %v", err)
		return nil
	}
	return b
}
