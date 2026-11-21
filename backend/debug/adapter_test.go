package debug

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeAdapterEnv selects the fake-adapter role of the test binary itself.
const fakeAdapterEnv = "GO_DEBUG_FAKE_ADAPTER"

// fakePIDFileEnv names the file where the forkexit mode records its child.
const fakePIDFileEnv = "GO_DEBUG_FAKE_PIDFILE"

// TestMain lets the test binary double as a debug adapter process:
//
//	stdio   serve DAP on stdin/stdout
//	tcp     serve DAP on the port given by --listen=127.0.0.1:<port>
//	exit    print "No module named debugpy" to stderr and exit 1
//	silent  never listen; sleep until killed
//	forkexit start a "silent" child that inherits stdout (same process
//	         group), write its pid to $GO_DEBUG_FAKE_PIDFILE, then exit 1
//
// With $GO_DEBUG_FAKE_PROGRAM set it is instead a debuggee started through
// runInTerminal (see runFakeProgram).
func TestMain(m *testing.M) {
	if os.Getenv(fakeProgramEnv) != "" {
		os.Exit(runFakeProgram(os.Args[1:]))
	}
	if mode := os.Getenv(fakeAdapterEnv); mode != "" {
		os.Exit(runFakeAdapterProcess(mode, os.Args[1:]))
	}
	os.Exit(m.Run())
}

var listenArg = regexp.MustCompile(`^--listen=(127\.0\.0\.1:[0-9]+)$`)

func runFakeAdapterProcess(mode string, args []string) int {
	switch mode {
	case "stdio":
		serveFakeDAP(os.Stdin, os.Stdout)
		return 0
	case "tcp":
		var addr string
		for _, a := range args {
			if m := listenArg.FindStringSubmatch(a); m != nil {
				addr = m[1]
			}
		}
		if addr == "" {
			fmt.Fprintf(os.Stderr, "fake adapter: no numeric --listen in %q\n", args)
			return 3
		}
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "fake adapter: listen %s: %v\n", addr, err)
			return 3
		}
		conn, err := ln.Accept()
		_ = ln.Close()
		if err != nil {
			return 3
		}
		serveFakeDAP(conn, conn)
		return 0
	case "exit":
		fmt.Fprintln(os.Stderr, "Traceback: No module named debugpy")
		return 1
	case "silent":
		time.Sleep(time.Hour)
		return 0
	case "forkexit":
		exe, err := os.Executable()
		if err != nil {
			return 3
		}
		child := exec.Command(exe)
		child.Env = append(os.Environ(), fakeAdapterEnv+"=silent")
		child.Stdout = os.Stdout // keeps a stdio session's pipe open
		if err := child.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "fake adapter: start child: %v\n", err)
			return 3
		}
		if err := os.WriteFile(os.Getenv(fakePIDFileEnv), []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
			return 3
		}
		return 1
	}
	fmt.Fprintf(os.Stderr, "fake adapter: unknown mode %q\n", mode)
	return 2
}

// serveFakeDAP answers every request with success; initialize also sends
// `initialized`, disconnect ends the session.
func serveFakeDAP(r io.Reader, w io.Writer) {
	br := bufio.NewReader(r)
	var seq int64
	send := func(m Message) {
		seq++
		m.Seq = seq
		_ = WriteMessage(w, m)
	}
	ok := true
	for {
		req, err := ReadMessage(br, 4<<20)
		if err != nil {
			return
		}
		if req.Type != "request" {
			continue
		}
		var body json.RawMessage
		if req.Command == "initialize" {
			body = json.RawMessage(`{"supportsConfigurationDoneRequest":true}`)
		}
		send(Message{Type: "response", RequestSeq: req.Seq, Command: req.Command, Success: &ok, Body: body})
		switch req.Command {
		case "initialize":
			send(Message{Type: "event", Event: "initialized"})
		case "disconnect":
			return
		}
	}
}

// emitRecorder captures registry emissions for one test.
type emitRecorder struct {
	mu     sync.Mutex
	events []Event
	ch     chan Event
}

func captureEmits(t *testing.T) *emitRecorder {
	t.Helper()
	rec := &emitRecorder{ch: make(chan Event, 256)}
	prev := setEventSink(func(ev Event) {
		rec.mu.Lock()
		rec.events = append(rec.events, ev)
		rec.mu.Unlock()
		select {
		case rec.ch <- ev:
		default:
		}
	})
	t.Cleanup(func() {
		StopAll("test cleanup")
		setEventSink(prev)
	})
	return rec
}

// waitEnded waits for the mincode.sessionEnded event of id and returns its reason.
func (r *emitRecorder) waitEnded(t *testing.T, id string) string {
	t.Helper()
	deadline := time.After(testTimeout)
	for {
		select {
		case ev := <-r.ch:
			if ev.SessionID == id && ev.Event == SessionEndedEvent {
				var body struct {
					Reason string `json:"reason"`
				}
				_ = json.Unmarshal(ev.Body, &body)
				return body.Reason
			}
		case <-deadline:
			t.Fatalf("no %s event for %s", SessionEndedEvent, id)
			return ""
		}
	}
}

// captureCmds records every adapter process StartAdapter starts.
func captureCmds(t *testing.T) func() []*exec.Cmd {
	t.Helper()
	var mu sync.Mutex
	var cmds []*exec.Cmd
	prev := afterAdapterStart
	afterAdapterStart = func(cmd *exec.Cmd) {
		mu.Lock()
		cmds = append(cmds, cmd)
		mu.Unlock()
	}
	t.Cleanup(func() { afterAdapterStart = prev })
	return func() []*exec.Cmd {
		mu.Lock()
		defer mu.Unlock()
		return append([]*exec.Cmd(nil), cmds...)
	}
}

func fakeSpec(t *testing.T, kind, mode string) AdapterSpec {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	spec := AdapterSpec{
		Kind:           kind,
		Executable:     exe,
		Env:            map[string]string{fakeAdapterEnv: mode},
		Cwd:            t.TempDir(),
		ConnectTimeout: testTimeout,
		OwnerPluginID:  "owner-a",
		DebuggerType:   "fake",
		ProjectRoot:    t.TempDir(),
	}
	if kind == "tcp" {
		spec.Args = []string{"--listen=127.0.0.1:${port}"}
	}
	return spec
}

func beginFake(t *testing.T, s *Session) {
	t.Helper()
	if _, err := s.Begin(testCtx(t), BeginParams{AdapterID: "fake", Request: "launch"}); err != nil {
		t.Fatalf("Begin: %v", err)
	}
}

func listed(id string) bool {
	for _, info := range List() {
		if info.ID == id {
			return true
		}
	}
	return false
}

func TestStartAdapterStdio(t *testing.T) {
	rec := captureEmits(t)
	cmds := captureCmds(t)
	s, err := StartAdapter(testCtx(t), fakeSpec(t, "stdio", "stdio"))
	if err != nil {
		t.Fatalf("StartAdapter: %v", err)
	}
	beginFake(t, s)

	infos := List()
	if len(infos) != 1 || infos[0].ID != s.ID() || infos[0].OwnerPluginID != "owner-a" || infos[0].DebuggerType != "fake" || infos[0].StartedAt == 0 {
		t.Fatalf("List() = %+v", infos)
	}
	if got, ok := Lookup(s.ID()); !ok || got != s {
		t.Fatalf("Lookup(%q) = %v, %v", s.ID(), got, ok)
	}

	s.Close("user stopped")
	if reason := rec.waitEnded(t, s.ID()); reason != "user stopped" {
		t.Fatalf("sessionEnded reason = %q", reason)
	}
	all := cmds()
	if len(all) != 1 || all[0].ProcessState == nil {
		t.Fatalf("adapter process not reaped after Close: %v", all)
	}
	if _, ok := Lookup(s.ID()); ok || len(List()) != 0 {
		t.Fatalf("session still registered after Close: %+v", List())
	}
	// The registry forwards adapter events (initialized) to the frontend.
	rec.mu.Lock()
	defer rec.mu.Unlock()
	found := false
	for _, ev := range rec.events {
		found = found || (ev.SessionID == s.ID() && ev.Event == "initialized")
	}
	if !found {
		t.Fatalf("initialized event not emitted: %+v", rec.events)
	}
}

func TestStartAdapterTCPSubstitutesPort(t *testing.T) {
	rec := captureEmits(t)
	s, err := StartAdapter(testCtx(t), fakeSpec(t, "tcp", "tcp"))
	if err != nil {
		t.Fatalf("StartAdapter: %v", err)
	}
	beginFake(t, s)
	if !listed(s.ID()) {
		t.Fatal("tcp session not listed")
	}
	if _, err := s.Request(testCtx(t), "disconnect", nil); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	rec.waitEnded(t, s.ID()) // adapter exit closes the connection and the session
}

func TestStartAdapterTCPNeverListens(t *testing.T) {
	captureEmits(t)
	cmds := captureCmds(t)
	spec := fakeSpec(t, "tcp", "silent")
	spec.ConnectTimeout = 500 * time.Millisecond
	start := time.Now()
	_, err := StartAdapter(testCtx(t), spec)
	if err == nil || !strings.Contains(err.Error(), "did not accept") {
		t.Fatalf("err = %v, want did not accept", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("StartAdapter took %v", time.Since(start))
	}
	all := cmds()
	if len(all) != 1 || all[0].ProcessState == nil {
		t.Fatalf("silent adapter not killed: %v", all)
	}
	if len(List()) != 0 {
		t.Fatalf("registry not empty: %+v", List())
	}
}

func TestStartAdapterExitsEarly(t *testing.T) {
	captureEmits(t)
	_, err := StartAdapter(testCtx(t), fakeSpec(t, "tcp", "exit"))
	if err == nil || !strings.Contains(err.Error(), "No module named debugpy") {
		t.Fatalf("err = %v, want stderr excerpt", err)
	}
	if len(List()) != 0 {
		t.Fatalf("registry not empty: %+v", List())
	}
}

func TestStdioAdapterExitReasonIncludesStderr(t *testing.T) {
	rec := captureEmits(t)
	s, err := StartAdapter(testCtx(t), fakeSpec(t, "stdio", "exit"))
	if err != nil {
		t.Fatalf("StartAdapter: %v", err)
	}
	if reason := rec.waitEnded(t, s.ID()); !strings.Contains(reason, "No module named debugpy") {
		t.Fatalf("close reason = %q, want stderr excerpt", reason)
	}
}

func TestBeforeExecRefusalStartsNothing(t *testing.T) {
	captureEmits(t)
	cmds := captureCmds(t)
	spec := fakeSpec(t, "stdio", "stdio")
	spec.BeforeExec = func() error { return errors.New("permission denied by user") }
	_, err := StartAdapter(testCtx(t), spec)
	if err == nil || !strings.Contains(err.Error(), "permission denied by user") {
		t.Fatalf("err = %v", err)
	}
	if n := len(cmds()); n != 0 {
		t.Fatalf("%d processes started", n)
	}
	if len(List()) != 0 {
		t.Fatalf("registry not empty: %+v", List())
	}
}

func TestStartAdapterRejectsBadSpec(t *testing.T) {
	captureEmits(t)
	for _, spec := range []AdapterSpec{
		{Kind: "pipe", Executable: "x"},
		{Kind: "stdio"},
	} {
		if _, err := StartAdapter(testCtx(t), spec); err == nil {
			t.Fatalf("spec %+v accepted", spec)
		}
	}
}

func TestStopForOwnerClosesOnlyThatOwner(t *testing.T) {
	rec := captureEmits(t)
	specA := fakeSpec(t, "stdio", "stdio")
	specB := fakeSpec(t, "stdio", "stdio")
	specB.OwnerPluginID = "owner-b"
	a, err := StartAdapter(testCtx(t), specA)
	if err != nil {
		t.Fatal(err)
	}
	b, err := StartAdapter(testCtx(t), specB)
	if err != nil {
		t.Fatal(err)
	}
	StopForOwner("owner-a", "plugin disabled")
	if reason := rec.waitEnded(t, a.ID()); reason != "plugin disabled" {
		t.Fatalf("reason = %q", reason)
	}
	if listed(a.ID()) || !listed(b.ID()) {
		t.Fatalf("List() = %+v", List())
	}
	beginFake(t, b) // the other owner's session still works
}

func TestValidFalseClosesSession(t *testing.T) {
	rec := captureEmits(t)
	prev := validCheckInterval
	validCheckInterval = 20 * time.Millisecond
	t.Cleanup(func() { validCheckInterval = prev })

	var valid atomic.Bool
	valid.Store(true)
	spec := fakeSpec(t, "stdio", "stdio")
	spec.Valid = valid.Load
	s, err := StartAdapter(testCtx(t), spec)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	if !listed(s.ID()) {
		t.Fatal("session closed while still valid")
	}
	valid.Store(false)
	if reason := rec.waitEnded(t, s.ID()); reason != "plugin changed" {
		t.Fatalf("reason = %q", reason)
	}
	if listed(s.ID()) {
		t.Fatal("session still listed")
	}
}

func TestMaxSessionsPerOwner(t *testing.T) {
	rec := captureEmits(t)
	spec := fakeSpec(t, "stdio", "stdio")
	spec.MaxSessionsPerOwner = 1
	first, err := StartAdapter(testCtx(t), spec)
	if err != nil {
		t.Fatal(err)
	}
	cmds := captureCmds(t)
	if _, err := StartAdapter(testCtx(t), spec); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Fatalf("second session: err = %v", err)
	}
	if n := len(cmds()); n != 0 {
		t.Fatalf("rejected start spawned %d processes", n)
	}
	other := spec
	other.OwnerPluginID = "owner-b"
	if _, err := StartAdapter(testCtx(t), other); err != nil {
		t.Fatalf("other owner: %v", err)
	}
	first.Close("done")
	rec.waitEnded(t, first.ID())
	if _, err := StartAdapter(testCtx(t), spec); err != nil {
		t.Fatalf("after close: %v", err)
	}
}

// fakeJavaDebugServer is an in-process DAP server on a loopback port, like
// the one java-debug opens inside jdtls. It records the requests it saw and
// whether the client closed the connection.
type fakeJavaDebugServer struct {
	port     int
	mu       sync.Mutex
	commands []string
	// closed is closed once the client connection reached EOF.
	closed chan struct{}
	// ignoreDisconnect leaves disconnect unanswered.
	ignoreDisconnect bool
}

func startFakeJavaDebugServer(t *testing.T, ignoreDisconnect bool) *fakeJavaDebugServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	server := &fakeJavaDebugServer{port: ln.Addr().(*net.TCPAddr).Port, closed: make(chan struct{}), ignoreDisconnect: ignoreDisconnect}
	go func() {
		conn, err := ln.Accept()
		_ = ln.Close()
		if err != nil {
			close(server.closed)
			return
		}
		defer conn.Close()
		defer close(server.closed)
		br := bufio.NewReader(conn)
		var seq int64
		ok := true
		for {
			req, err := ReadMessage(br, 4<<20)
			if err != nil {
				return
			}
			if req.Type != "request" {
				continue
			}
			server.mu.Lock()
			server.commands = append(server.commands, req.Command)
			server.mu.Unlock()
			if req.Command == "disconnect" && server.ignoreDisconnect {
				continue
			}
			seq++
			_ = WriteMessage(conn, Message{Seq: seq, Type: "response", RequestSeq: req.Seq, Command: req.Command, Success: &ok})
			if req.Command == "initialize" {
				seq++
				_ = WriteMessage(conn, Message{Seq: seq, Type: "event", Event: "initialized"})
			}
			// java-debug keeps the connection open after disconnect: the
			// client must close it.
		}
	}()
	return server
}

func (s *fakeJavaDebugServer) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.commands...)
}

func (s *fakeJavaDebugServer) waitClosed(t *testing.T) {
	t.Helper()
	select {
	case <-s.closed:
	case <-time.After(testTimeout):
		t.Fatal("client never closed the connection")
	}
}

func connectSpec(port int) ConnectSpec {
	return ConnectSpec{
		Port: port, OwnerPluginID: "owner-j", DebuggerType: "java", ProjectRoot: "/tmp/project",
		ConnectTimeout: testTimeout,
	}
}

func TestConnectAdapterNoProcessCleanup(t *testing.T) {
	rec := captureEmits(t)
	cmds := captureCmds(t)
	server := startFakeJavaDebugServer(t, false)
	s, err := ConnectAdapter(testCtx(t), connectSpec(server.port))
	if err != nil {
		t.Fatalf("ConnectAdapter: %v", err)
	}
	beginFake(t, s)
	infos := List()
	if len(infos) != 1 || infos[0].ID != s.ID() || infos[0].OwnerPluginID != "owner-j" || infos[0].DebuggerType != "java" {
		t.Fatalf("List() = %+v", infos)
	}
	// Graceful stop: disconnect is sent, then the connection is closed.
	StopSession(context.Background(), s, "user stopped")
	if reason := rec.waitEnded(t, s.ID()); reason != "user stopped" {
		t.Fatalf("reason = %q", reason)
	}
	server.waitClosed(t)
	if seen := server.seen(); len(seen) == 0 || seen[len(seen)-1] != "disconnect" {
		t.Fatalf("server saw %v, want a final disconnect", seen)
	}
	if n := len(cmds()); n != 0 {
		t.Fatalf("ConnectAdapter started %d processes", n)
	}
	if listed(s.ID()) {
		t.Fatal("session still listed")
	}
}

func TestConnectAdapterValidFalseCloses(t *testing.T) {
	rec := captureEmits(t)
	prev := validCheckInterval
	validCheckInterval = 20 * time.Millisecond
	t.Cleanup(func() { validCheckInterval = prev })
	server := startFakeJavaDebugServer(t, false)
	var valid atomic.Bool
	valid.Store(true)
	spec := connectSpec(server.port)
	spec.Valid = valid.Load
	s, err := ConnectAdapter(testCtx(t), spec)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	if !listed(s.ID()) {
		t.Fatal("session closed while still valid")
	}
	valid.Store(false)
	if reason := rec.waitEnded(t, s.ID()); reason != "plugin changed" {
		t.Fatalf("reason = %q", reason)
	}
	server.waitClosed(t)
}

func TestConnectAdapterPeerCloseEndsSession(t *testing.T) {
	rec := captureEmits(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			_ = conn.Close()
		}
	}()
	s, err := ConnectAdapter(testCtx(t), connectSpec(ln.Addr().(*net.TCPAddr).Port))
	if err != nil {
		t.Fatal(err)
	}
	if reason := rec.waitEnded(t, s.ID()); !strings.Contains(reason, "adapter connection") {
		t.Fatalf("reason = %q", reason)
	}
}

func TestConnectAdapterRejectsBadSpec(t *testing.T) {
	captureEmits(t)
	for _, port := range []int{0, -1, 65536} {
		if _, err := ConnectAdapter(testCtx(t), connectSpec(port)); err == nil || !strings.Contains(err.Error(), "port") {
			t.Fatalf("port %d: err = %v", port, err)
		}
	}
	// Nothing listens: the connect fails within the timeout.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	spec := connectSpec(port)
	spec.ConnectTimeout = 300 * time.Millisecond
	if _, err := ConnectAdapter(testCtx(t), spec); err == nil || !strings.Contains(err.Error(), "did not accept") {
		t.Fatalf("closed port: err = %v", err)
	}
	if len(List()) != 0 {
		t.Fatalf("registry not empty: %+v", List())
	}
}

func TestConnectAdapterMaxSessionsAndShutdown(t *testing.T) {
	rec := captureEmits(t)
	first := startFakeJavaDebugServer(t, true) // ignores disconnect: Shutdown must still close it
	spec := connectSpec(first.port)
	spec.MaxSessionsPerOwner = 1
	s, err := ConnectAdapter(testCtx(t), spec)
	if err != nil {
		t.Fatal(err)
	}
	second := startFakeJavaDebugServer(t, false)
	spec.Port = second.port
	if _, err := ConnectAdapter(testCtx(t), spec); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Fatalf("second session: err = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	Shutdown(ctx)
	t.Cleanup(resetShutdownForTest)
	rec.waitEnded(t, s.ID())
	first.waitClosed(t)
	if _, err := ConnectAdapter(testCtx(t), connectSpec(second.port)); err == nil || !strings.Contains(err.Error(), "shutting down") {
		t.Fatalf("after Shutdown: err = %v", err)
	}
}
