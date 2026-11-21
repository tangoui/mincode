package debug

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testTimeout bounds every wait in these tests so a broken session fails fast
// instead of hanging the test binary.
const testTimeout = 5 * time.Second

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	t.Cleanup(cancel)
	return ctx
}

// sessionRecorder collects OnEvent / OnClose callbacks.
type sessionRecorder struct {
	events     chan Event
	closes     atomic.Int32
	closed     chan string
	closedOnce sync.Once
}

func newSessionRecorder() *sessionRecorder {
	return &sessionRecorder{events: make(chan Event, 64), closed: make(chan string, 1)}
}

func (r *sessionRecorder) options(id string) SessionOptions {
	return SessionOptions{
		ID:      id,
		OnEvent: func(ev Event) { r.events <- ev },
		OnClose: func(reason string) {
			r.closes.Add(1)
			r.closedOnce.Do(func() { r.closed <- reason })
		},
	}
}

func (r *sessionRecorder) waitClosed(t *testing.T) string {
	t.Helper()
	select {
	case reason := <-r.closed:
		return reason
	case <-time.After(testTimeout):
		t.Fatal("OnClose was not called")
		return ""
	}
}

func (r *sessionRecorder) nextEvent(t *testing.T) Event {
	t.Helper()
	select {
	case ev := <-r.events:
		return ev
	case <-time.After(testTimeout):
		t.Fatal("no event forwarded")
		return Event{}
	}
}

func TestBeginRunsInitSequenceInOrder(t *testing.T) {
	fa, conn := newFakeAdapter(t)
	var mu sync.Mutex
	var launchReq *Message
	initializedSent := false
	launchBeforeInitialized := false

	fa.Handle("initialize", func(req Message) {
		fa.Respond(req, true, map[string]any{
			"supportsConfigurationDoneRequest": true,
			"exceptionBreakpointFilters":       []map[string]any{{"filter": "raised", "label": "Raised"}},
		}, "")
	})
	// A debugpy-style adapter: the launch response is withheld until
	// configurationDone, and `initialized` is only sent once launch arrives.
	// A client that waited for the launch response before configuring would
	// deadlock here.
	fa.Handle("launch", func(req Message) {
		mu.Lock()
		launchReq = &req
		launchBeforeInitialized = !initializedSent
		initializedSent = true
		mu.Unlock()
		fa.SendEvent("initialized", nil)
	})
	fa.Handle("configurationDone", func(req Message) {
		fa.Respond(req, true, nil, "")
		mu.Lock()
		lr := launchReq
		mu.Unlock()
		if lr != nil {
			fa.Respond(*lr, true, nil, "")
		}
	})
	fa.Start()

	rec := newSessionRecorder()
	s := newSession(conn, rec.options("s1"))
	defer s.Close("test done")

	caps, err := s.Begin(testCtx(t), BeginParams{
		AdapterID: "python",
		Request:   "launch",
		Arguments: json.RawMessage(`{"program":"/proj/main.py"}`),
		Breakpoints: map[string][]SourceBreakpoint{
			"/proj/main.py": {{Line: 3}, {Line: 7, Condition: "x > 1", HitCondition: "2"}},
		},
		ExceptionFilters: []string{"raised"},
	})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if !strings.Contains(string(caps), "supportsConfigurationDoneRequest") {
		t.Fatalf("capabilities = %s", caps)
	}

	want := []string{"initialize", "launch", "setBreakpoints", "setExceptionBreakpoints", "configurationDone"}
	if got := fa.Commands(); !reflect.DeepEqual(got, want) {
		t.Fatalf("commands = %v, want %v", got, want)
	}
	mu.Lock()
	if !launchBeforeInitialized {
		t.Fatal("launch was not sent before the initialized event")
	}
	mu.Unlock()

	var initArgs map[string]any
	if err := json.Unmarshal(fa.Requests("initialize")[0].Arguments, &initArgs); err != nil {
		t.Fatalf("initialize arguments: %v", err)
	}
	for k, v := range map[string]any{
		"clientID": "mincode", "clientName": "MinCode", "adapterID": "python",
		"linesStartAt1": true, "columnsStartAt1": true, "pathFormat": "path",
		"supportsVariableType": true, "supportsRunInTerminalRequest": true,
	} {
		if initArgs[k] != v {
			t.Errorf("initialize %s = %v, want %v", k, initArgs[k], v)
		}
	}
	if loc, _ := initArgs["locale"].(string); loc == "" {
		t.Errorf("initialize locale missing: %v", initArgs)
	}

	if got := string(fa.Requests("launch")[0].Arguments); got != `{"program":"/proj/main.py"}` {
		t.Errorf("launch arguments = %s", got)
	}

	var bpArgs struct {
		Source struct {
			Path string `json:"path"`
		} `json:"source"`
		Breakpoints []SourceBreakpoint `json:"breakpoints"`
	}
	if err := json.Unmarshal(fa.Requests("setBreakpoints")[0].Arguments, &bpArgs); err != nil {
		t.Fatalf("setBreakpoints arguments: %v", err)
	}
	wantBps := []SourceBreakpoint{{Line: 3}, {Line: 7, Condition: "x > 1", HitCondition: "2"}}
	if bpArgs.Source.Path != "/proj/main.py" || !reflect.DeepEqual(bpArgs.Breakpoints, wantBps) {
		t.Errorf("setBreakpoints arguments = %s", fa.Requests("setBreakpoints")[0].Arguments)
	}

	var exArgs struct {
		Filters []string `json:"filters"`
	}
	if err := json.Unmarshal(fa.Requests("setExceptionBreakpoints")[0].Arguments, &exArgs); err != nil {
		t.Fatalf("setExceptionBreakpoints arguments: %v", err)
	}
	if !reflect.DeepEqual(exArgs.Filters, []string{"raised"}) {
		t.Errorf("exception filters = %v", exArgs.Filters)
	}
}

func TestBeginSkipsExceptionBreakpointsWithoutFilters(t *testing.T) {
	fa, conn := newFakeAdapter(t)
	fa.Handle("initialize", func(req Message) {
		fa.Respond(req, true, map[string]any{"supportsConfigurationDoneRequest": true}, "")
		fa.SendEvent("initialized", nil) // adapters may send it before launch
	})
	fa.Start()

	s := newSession(conn, SessionOptions{ID: "s1"})
	defer s.Close("test done")
	if _, err := s.Begin(testCtx(t), BeginParams{AdapterID: "go", Request: "attach"}); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	want := []string{"initialize", "attach", "configurationDone"}
	if got := fa.Commands(); !reflect.DeepEqual(got, want) {
		t.Fatalf("commands = %v, want %v", got, want)
	}
}

func TestBeginRecordsTheConfirmedOutsideCwd(t *testing.T) {
	for request, want := range map[string]string{"launch": filepath.Clean("/work/dir"), "attach": ""} {
		fa, conn := newFakeAdapter(t)
		fa.Handle("initialize", func(req Message) {
			fa.Respond(req, true, map[string]any{"supportsConfigurationDoneRequest": true}, "")
			fa.SendEvent("initialized", nil)
		})
		fa.Handle(request, func(req Message) { fa.Respond(req, true, nil, "") })
		fa.Handle("configurationDone", func(req Message) { fa.Respond(req, true, nil, "") })
		fa.Start()
		s := newSession(conn, SessionOptions{ID: "s1"})
		// The launch arguments' cwd alone is never taken as confirmed.
		if _, err := s.Begin(testCtx(t), BeginParams{AdapterID: "go", Request: request, Arguments: json.RawMessage(`{"cwd":"/other"}`), ConfirmedOutsideCwd: "/work/dir/"}); err != nil {
			t.Fatalf("%s: Begin: %v", request, err)
		}
		if got := s.confirmedCwd(); got != want {
			t.Errorf("%s: confirmedCwd = %q, want %q", request, got, want)
		}
		s.Close("test done")
	}
}

func TestBeginReturnsLaunchFailure(t *testing.T) {
	fa, conn := newFakeAdapter(t)
	fa.Handle("initialize", func(req Message) {
		fa.Respond(req, true, map[string]any{"supportsConfigurationDoneRequest": true}, "")
	})
	// Launch fails before `initialized` is ever sent; Begin must not wait for it.
	fa.Handle("launch", func(req Message) {
		fa.Respond(req, false, nil, "interpreter not found")
	})
	fa.Start()

	s := newSession(conn, SessionOptions{ID: "s1"})
	defer s.Close("test done")
	_, err := s.Begin(testCtx(t), BeginParams{AdapterID: "python", Request: "launch"})
	var re *RequestError
	if !errors.As(err, &re) || re.Command != "launch" || re.Message != "interpreter not found" {
		t.Fatalf("err = %v, want launch RequestError", err)
	}
}

// java-debug deliberately omits initialized for noDebug launches. A successful
// launch response is sufficient in that mode, even when configurationDone is
// advertised and the caller supplied saved breakpoints.
func TestBeginNoDebugWithoutInitialized(t *testing.T) {
	for _, tc := range []struct {
		name, request, arguments string
		success, wantSuccess     bool
	}{
		{"run", "launch", `{"noDebug":true}`, true, true},
		{"run failure", "launch", `{"noDebug":true}`, false, false},
		{"debug still requires initialized", "launch", `{"noDebug":false}`, true, false},
		{"attach still requires initialized", "attach", `{"noDebug":true}`, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fa, conn := newFakeAdapter(t)
			fa.Handle("initialize", func(req Message) {
				fa.Respond(req, true, map[string]any{"supportsConfigurationDoneRequest": true}, "")
			})
			fa.Handle(tc.request, func(req Message) {
				fa.Respond(req, tc.success, nil, "launch rejected")
			})
			fa.Start()
			s := newSession(conn, SessionOptions{ID: "no-debug"})
			defer s.Close("test done")
			ctx, cancel := context.WithTimeout(testCtx(t), 100*time.Millisecond)
			defer cancel()
			caps, err := s.Begin(ctx, BeginParams{
				AdapterID: "java", Request: tc.request, Arguments: json.RawMessage(tc.arguments),
				Breakpoints:      map[string][]SourceBreakpoint{"/proj/App.java": {{Line: 4}}},
				ExceptionFilters: []string{"caught"},
			})
			if tc.wantSuccess {
				if err != nil {
					t.Fatalf("Begin: %v", err)
				}
				if !supportsConfigurationDone(caps) {
					t.Fatalf("lost capabilities: %s", caps)
				}
			} else if !tc.success {
				var re *RequestError
				if !errors.As(err, &re) || re.Message != "launch rejected" {
					t.Fatalf("error = %v, want launch failure", err)
				}
			} else if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("error = %v, want deadline awaiting initialized", err)
			}
			if got, want := fa.Commands(), []string{"initialize", tc.request}; !reflect.DeepEqual(got, want) {
				t.Fatalf("commands = %v, want %v", got, want)
			}
		})
	}
}

func TestBeginNoDebugSucceedsWhenProgramEndsRightAway(t *testing.T) {
	for i := 0; i < 32; i++ {
		fa, conn := newFakeAdapter(t)
		fa.Handle("initialize", func(req Message) {
			fa.Respond(req, true, map[string]any{"supportsConfigurationDoneRequest": true}, "")
		})
		fa.Handle("launch", func(req Message) {
			fa.Respond(req, true, nil, "")
			fa.SendEvent("terminated", nil)
		})
		fa.Start()
		s := newSession(conn, SessionOptions{ID: "short-run"})
		if _, err := s.Begin(testCtx(t), BeginParams{AdapterID: "java", Request: "launch", Arguments: json.RawMessage(`{"noDebug":true}`)}); err != nil {
			t.Fatalf("iteration %d: Begin: %v", i, err)
		}
		s.Close("test done")
	}
}

// debugpy still sends initialized in noDebug mode and waits for configurationDone
// before answering launch. Do not skip that handshake just because noDebug is true.
func TestBeginNoDebugWithInitialized(t *testing.T) {
	fa, conn := newFakeAdapter(t)
	var launch Message
	fa.Handle("initialize", func(req Message) {
		fa.Respond(req, true, map[string]any{"supportsConfigurationDoneRequest": true}, "")
	})
	fa.Handle("launch", func(req Message) {
		launch = req
		fa.SendEvent("initialized", nil)
	})
	fa.Handle("configurationDone", func(req Message) {
		fa.Respond(req, true, nil, "")
		fa.Respond(launch, true, nil, "")
	})
	fa.Start()
	s := newSession(conn, SessionOptions{ID: "no-debug-configured"})
	defer s.Close("test done")
	if _, err := s.Begin(testCtx(t), BeginParams{AdapterID: "python", Request: "launch", Arguments: json.RawMessage(`{"noDebug":true}`)}); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if got, want := fa.Commands(), []string{"initialize", "launch", "configurationDone"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("commands = %v, want %v", got, want)
	}
}

func TestBeginNoDebugConfiguresWhenInitializedPrecedesLaunchResponse(t *testing.T) {
	for i := 0; i < 32; i++ {
		fa, conn := newFakeAdapter(t)
		fa.Handle("initialize", func(req Message) {
			fa.Respond(req, true, map[string]any{"supportsConfigurationDoneRequest": true}, "")
		})
		fa.Handle("launch", func(req Message) {
			fa.SendEvent("initialized", nil)
			fa.Respond(req, true, nil, "")
		})
		fa.Start()
		s := newSession(conn, SessionOptions{ID: "early-run-response"})
		if _, err := s.Begin(testCtx(t), BeginParams{AdapterID: "fake", Request: "launch", Arguments: json.RawMessage(`{"noDebug":true}`)}); err != nil {
			t.Fatalf("iteration %d: Begin: %v", i, err)
		}
		if got, want := fa.Commands(), []string{"initialize", "launch", "configurationDone"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("iteration %d: commands = %v, want %v", i, got, want)
		}
		s.Close("test done")
	}
}

func TestBeginRejectsUnknownRequestKind(t *testing.T) {
	_, conn := newFakeAdapter(t)
	s := newSession(conn, SessionOptions{ID: "s1"})
	defer s.Close("test done")
	if _, err := s.Begin(testCtx(t), BeginParams{AdapterID: "x", Request: "run"}); err == nil {
		t.Fatal("expected error for request kind \"run\"")
	}
}

func TestRequestReturnsRequestErrorOnFailure(t *testing.T) {
	fa, conn := newFakeAdapter(t)
	fa.Handle("evaluate", func(req Message) { fa.Respond(req, false, nil, "boom") })
	fa.Start()

	s := newSession(conn, SessionOptions{ID: "s1"})
	defer s.Close("test done")
	_, err := s.Request(testCtx(t), "evaluate", map[string]any{"expression": "1/0"})
	var re *RequestError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v (%T), want *RequestError", err, err)
	}
	if re.Message != "boom" || re.Command != "evaluate" {
		t.Fatalf("RequestError = %+v", re)
	}
}

func TestRequestReturnsBody(t *testing.T) {
	fa, conn := newFakeAdapter(t)
	fa.Handle("threads", func(req Message) {
		fa.Respond(req, true, map[string]any{"threads": []any{}}, "")
	})
	fa.Start()

	s := newSession(conn, SessionOptions{ID: "s1"})
	defer s.Close("test done")
	body, err := s.Request(testCtx(t), "threads", nil)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if string(body) != `{"threads":[]}` {
		t.Fatalf("body = %s", body)
	}
	if req := fa.Requests("threads")[0]; req.Arguments != nil {
		t.Fatalf("nil args sent as %s", req.Arguments)
	}
}

func TestRunInTerminalAnsweredWithFailure(t *testing.T) {
	fa, conn := newFakeAdapter(t)
	fa.Start()

	s := newSession(conn, SessionOptions{ID: "s1"})
	defer s.Close("test done")

	for _, cmd := range []string{"runInTerminal", "startDebugging", "somethingElse"} {
		seq := fa.SendRequest(cmd, map[string]any{"args": []string{"python"}})
		select {
		case resp := <-fa.responses:
			if resp.Type != "response" || resp.RequestSeq != seq || resp.Command != cmd {
				t.Fatalf("%s: response = %+v, want request_seq %d", cmd, resp, seq)
			}
			if resp.Success == nil || *resp.Success {
				t.Fatalf("%s: success = %v, want false", cmd, resp.Success)
			}
			if cmd == "runInTerminal" && resp.Message != "runInTerminal is not supported yet" {
				t.Fatalf("runInTerminal message = %q", resp.Message)
			}
			if resp.Message == "" {
				t.Fatalf("%s: empty failure message", cmd)
			}
		case <-time.After(testTimeout):
			t.Fatalf("%s: no response to reverse request", cmd)
		}
	}
}

func TestEventsForwardedAndCloseOnTerminated(t *testing.T) {
	fa, conn := newFakeAdapter(t)
	fa.Start()

	rec := newSessionRecorder()
	s := newSession(conn, rec.options("s42"))

	fa.SendEvent("stopped", map[string]any{"reason": "breakpoint", "threadId": 1})
	fa.SendEvent("terminated", nil)

	ev1 := rec.nextEvent(t)
	ev2 := rec.nextEvent(t)
	if ev1.SessionID != "s42" || ev1.Event != "stopped" || !strings.Contains(string(ev1.Body), `"breakpoint"`) {
		t.Fatalf("first event = %+v (body %s)", ev1, ev1.Body)
	}
	if ev2.SessionID != "s42" || ev2.Event != "terminated" {
		t.Fatalf("second event = %+v", ev2)
	}
	rec.waitClosed(t)

	s.Close("again") // idempotent
	if n := rec.closes.Load(); n != 1 {
		t.Fatalf("OnClose called %d times, want 1", n)
	}
	if _, err := s.Request(testCtx(t), "threads", nil); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("Request after close: err = %v, want ErrSessionClosed", err)
	}
}

// The host's synthetic events share the mincode.* names; an adapter (or a
// remote debug server it relays) must not be able to send them, or a fake
// mincode.sessionEnded would hide a running session and disable Stop.
func TestAdapterCannotSendReservedHostEvents(t *testing.T) {
	fa, conn := newFakeAdapter(t)
	fa.Start()
	rec := newSessionRecorder()
	s := newSession(conn, rec.options("s7"))
	defer s.Close("test done")

	fa.SendEvent(SessionEndedEvent, map[string]any{"reason": "spoofed"})
	fa.SendEvent(RunInTerminalEvent, map[string]any{"terminalSessionId": "user-shell"})
	fa.SendEvent("mincode.anything", nil)
	fa.SendEvent("stopped", map[string]any{"reason": "breakpoint", "threadId": 1})

	ev := rec.nextEvent(t)
	if ev.Event != "stopped" {
		t.Fatalf("first forwarded event = %q, want the reserved mincode.* events dropped", ev.Event)
	}
}

func TestRequestCancelledByContext(t *testing.T) {
	fa, conn := newFakeAdapter(t)
	slow := make(chan Message, 1)
	fa.Handle("slow", func(req Message) { slow <- req }) // never answered in time
	fa.Handle("ping", func(req Message) { fa.Respond(req, true, map[string]any{"pong": true}, "") })
	fa.Start()

	s := newSession(conn, SessionOptions{ID: "s1"})
	defer s.Close("test done")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := s.Request(ctx, "slow", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if n := s.pendingCount(); n != 0 {
		t.Fatalf("pending requests after timeout = %d, want 0", n)
	}

	// A late answer to the abandoned request is dropped, not misrouted.
	fa.Respond(<-slow, true, map[string]any{"late": true}, "")
	body, err := s.Request(testCtx(t), "ping", nil)
	if err != nil {
		t.Fatalf("Request after timeout: %v", err)
	}
	if string(body) != `{"pong":true}` {
		t.Fatalf("body = %s", body)
	}
}

func TestCloseFailsPendingRequests(t *testing.T) {
	fa, conn := newFakeAdapter(t)
	got := make(chan struct{})
	fa.Handle("slow", func(Message) { close(got) })
	fa.Start()

	rec := newSessionRecorder()
	s := newSession(conn, rec.options("s1"))
	errc := make(chan error, 1)
	go func() {
		_, err := s.Request(testCtx(t), "slow", nil)
		errc <- err
	}()
	<-got
	s.Close("user stopped")
	select {
	case err := <-errc:
		if !errors.Is(err, ErrSessionClosed) || !strings.Contains(err.Error(), "user stopped") {
			t.Fatalf("err = %v, want ErrSessionClosed with reason", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("pending request not released by Close")
	}
	if reason := rec.waitClosed(t); reason != "user stopped" {
		t.Fatalf("OnClose reason = %q", reason)
	}
}

func TestReadErrorClosesSession(t *testing.T) {
	fa, conn := newFakeAdapter(t)
	fa.Start()

	rec := newSessionRecorder()
	opts := rec.options("s1")
	opts.MaxMessageBytes = 64
	newSession(conn, opts)

	body := fmt.Sprintf(`{"seq":1,"type":"event","event":"output","body":{"output":%q}}`, strings.Repeat("x", 100))
	fa.WriteRaw([]byte(fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)))

	reason := rec.waitClosed(t)
	if !strings.Contains(reason, ErrFrameTooLarge.Error()) {
		t.Fatalf("OnClose reason = %q, want frame-too-large error", reason)
	}
	// The connection is closed: the fake's read loop ends.
	select {
	case <-fa.done:
	case <-time.After(testTimeout):
		t.Fatal("session did not close the connection after a read error")
	}
	select {
	case ev := <-rec.events:
		t.Fatalf("event forwarded from oversize frame: %+v", ev)
	default:
	}
}

func TestAdapterEOFClosesSession(t *testing.T) {
	fa, conn := newFakeAdapter(t)
	rec := newSessionRecorder()
	newSession(conn, rec.options("s1"))
	_ = fa.conn.Close() // adapter side hangs up
	if reason := rec.waitClosed(t); reason == "" {
		t.Fatal("empty close reason")
	}
}

// beginFakeWithBreakpoints scripts an adapter that sends `initialized` right
// after initialize and answers setBreakpoints per source path via bpReply
// (nil body + non-empty msg = failure).
func beginFakeWithBreakpoints(t *testing.T, bpReply func(path string) (body any, msg string), exReply func() (body any, msg string)) (*fakeAdapter, io.ReadWriteCloser) {
	t.Helper()
	fa, conn := newFakeAdapter(t)
	fa.Handle("initialize", func(req Message) {
		fa.Respond(req, true, map[string]any{"supportsConfigurationDoneRequest": true}, "")
		fa.SendEvent("initialized", nil)
	})
	fa.Handle("setBreakpoints", func(req Message) {
		var args struct {
			Source struct {
				Path string `json:"path"`
			} `json:"source"`
		}
		_ = json.Unmarshal(req.Arguments, &args)
		body, msg := bpReply(args.Source.Path)
		fa.Respond(req, msg == "", body, msg)
	})
	fa.Handle("setExceptionBreakpoints", func(req Message) {
		body, msg := exReply()
		fa.Respond(req, msg == "", body, msg)
	})
	fa.Start()
	return fa, conn
}

func TestBeginContinuesWhenSetBreakpointsFails(t *testing.T) {
	fa, conn := beginFakeWithBreakpoints(t,
		func(path string) (any, string) {
			if path == "/proj/a.py" {
				return nil, "file not found"
			}
			return map[string]any{"breakpoints": []any{}}, ""
		},
		func() (any, string) { return nil, "unknown filter" },
	)
	s := newSession(conn, SessionOptions{ID: "s1"})
	defer s.Close("test done")

	_, err := s.Begin(testCtx(t), BeginParams{
		AdapterID: "python",
		Request:   "launch",
		Breakpoints: map[string][]SourceBreakpoint{
			"/proj/a.py": {{Line: 1}},
			"/proj/b.py": {{Line: 2}},
		},
		ExceptionFilters: []string{"bogus"},
	})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	want := []string{"initialize", "launch", "setBreakpoints", "setBreakpoints", "setExceptionBreakpoints", "configurationDone"}
	if got := fa.Commands(); !reflect.DeepEqual(got, want) {
		t.Fatalf("commands = %v, want %v", got, want)
	}
}

func TestBeginReportsBreakpointResults(t *testing.T) {
	_, conn := beginFakeWithBreakpoints(t,
		func(path string) (any, string) {
			if path == "/proj/b.py" {
				return nil, "nope"
			}
			return map[string]any{"breakpoints": []map[string]any{{"verified": true, "line": 3}}}, ""
		},
		func() (any, string) { return nil, "" },
	)
	rec := newSessionRecorder()
	s := newSession(conn, rec.options("s7"))
	defer s.Close("test done")

	_, err := s.Begin(testCtx(t), BeginParams{
		AdapterID: "python",
		Request:   "launch",
		Breakpoints: map[string][]SourceBreakpoint{
			"/proj/a.py": {{Line: 3}},
			"/proj/b.py": {{Line: 4}},
		},
		ExceptionFilters: []string{"raised"},
	})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	// Synthetic events are emitted by Begin before it returns.
	var got []map[string]json.RawMessage
	for len(rec.events) > 0 {
		ev := <-rec.events
		if ev.Event != "mincode.breakpointsSet" {
			continue
		}
		if ev.SessionID != "s7" {
			t.Fatalf("event session = %q", ev.SessionID)
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(ev.Body, &body); err != nil {
			t.Fatalf("event body %s: %v", ev.Body, err)
		}
		got = append(got, body)
	}
	if len(got) != 3 {
		t.Fatalf("got %d mincode.breakpointsSet events, want 3: %v", len(got), got)
	}

	a, b, ex := got[0], got[1], got[2]
	if string(a["path"]) != `"/proj/a.py"` || string(a["breakpoints"]) != `[{"line":3,"verified":true}]` {
		t.Errorf("a.py result = %v", a)
	}
	if _, ok := a["error"]; ok {
		t.Errorf("a.py has error: %s", a["error"])
	}
	if string(b["path"]) != `"/proj/b.py"` || string(b["breakpoints"]) != `null` || string(b["error"]) != `"nope"` {
		t.Errorf("b.py result = %v", b)
	}
	if string(ex["path"]) != `""` || string(ex["exceptionFilters"]) != `null` {
		t.Errorf("exception filter result = %v", ex)
	}
	if _, ok := ex["error"]; ok {
		t.Errorf("exception filters have error: %s", ex["error"])
	}
	if _, ok := ex["breakpoints"]; ok {
		t.Errorf("exception filter result uses breakpoints key: %v", ex)
	}
}

func TestNoEventsAfterClose(t *testing.T) {
	fa, conn := newFakeAdapter(t)
	fa.Start()

	var (
		mu         sync.Mutex
		closed     bool
		seen       []string
		lateEvents []string
	)
	closedCh := make(chan struct{})
	newSession(conn, SessionOptions{
		ID: "s1",
		OnEvent: func(ev Event) {
			mu.Lock()
			defer mu.Unlock()
			seen = append(seen, ev.Event)
			if closed {
				lateEvents = append(lateEvents, ev.Event)
			}
		},
		OnClose: func(string) {
			mu.Lock()
			closed = true
			mu.Unlock()
			close(closedCh)
		},
	})

	// Both frames in one write so the session's reader buffers them together.
	var buf bytes.Buffer
	for _, m := range []Message{
		{Seq: 1, Type: "event", Event: "terminated"},
		{Seq: 2, Type: "event", Event: "output", Body: json.RawMessage(`{"output":"late"}`)},
	} {
		if err := WriteMessage(&buf, m); err != nil {
			t.Fatal(err)
		}
	}
	fa.WriteRaw(buf.Bytes())

	select {
	case <-closedCh:
	case <-time.After(testTimeout):
		t.Fatal("OnClose was not called")
	}
	time.Sleep(100 * time.Millisecond) // give a buggy read loop time to deliver the buffered frame

	mu.Lock()
	defer mu.Unlock()
	if len(lateEvents) > 0 {
		t.Fatalf("events after close: seen=%v late=%v", seen, lateEvents)
	}
	if !reflect.DeepEqual(seen, []string{"terminated"}) {
		t.Fatalf("seen = %v, want [terminated]", seen)
	}
}

// TestResponseBeforeTerminatedIsNotLost covers the response-vs-close race: the
// adapter answers a request and then sends `terminated` (a short program
// ending right after configurationDone). The read loop delivers the response
// before it closes the session, so wait must report the response even when
// it observes both at once. The wait starts only after the close, which
// makes both select cases ready; repeated so a random pick cannot hide it.
func TestResponseBeforeTerminatedIsNotLost(t *testing.T) {
	for i := 0; i < 32; i++ {
		fa, conn := newFakeAdapter(t)
		fa.Handle("configurationDone", func(req Message) {
			fa.Respond(req, true, map[string]any{"ok": i}, "")
			fa.SendEvent("terminated", nil)
		})
		fa.Start()
		s := newSession(conn, SessionOptions{ID: "s1"})
		c, err := s.send("configurationDone", nil)
		if err != nil {
			t.Fatalf("send: %v", err)
		}
		select {
		case <-s.done:
		case <-time.After(testTimeout):
			t.Fatal("session did not close on terminated")
		}
		body, err := s.wait(testCtx(t), c)
		if err != nil {
			t.Fatalf("iteration %d: wait = %v, want the response delivered before terminated", i, err)
		}
		if string(body) != fmt.Sprintf(`{"ok":%d}`, i) {
			t.Fatalf("body = %s", body)
		}
	}
}

func TestBeginSucceedsWhenProgramEndsRightAway(t *testing.T) {
	fa, conn := newFakeAdapter(t)
	var mu sync.Mutex
	var launch Message
	fa.Handle("initialize", func(req Message) {
		fa.Respond(req, true, map[string]any{"supportsConfigurationDoneRequest": true}, "")
	})
	fa.Handle("launch", func(req Message) {
		mu.Lock()
		launch = req
		mu.Unlock()
		fa.SendEvent("initialized", nil)
	})
	fa.Handle("configurationDone", func(req Message) {
		fa.Respond(req, true, nil, "")
		mu.Lock()
		l := launch
		mu.Unlock()
		fa.Respond(l, true, nil, "")
		fa.SendEvent("exited", map[string]any{"exitCode": 0})
		fa.SendEvent("terminated", nil)
	})
	fa.Start()
	s := newSession(conn, SessionOptions{ID: "s1"})
	if _, err := s.Begin(testCtx(t), BeginParams{AdapterID: "fake", Request: "launch"}); err != nil {
		t.Fatalf("Begin: %v", err)
	}
}
