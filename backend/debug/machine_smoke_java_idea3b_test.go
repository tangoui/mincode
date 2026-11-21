package debug

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"mincode/app/backend/lsp"
)

// Phase 3b of Java debugging close to IDEA (JUnit runs), end to end against
// a real jdtls that loads the jars the java-debug plugin ships
// (contributes.jdtlsBundles of official-plugins/java-debug/plugin.json:
// java-debug, java-test and its 28 dependency jars, as put in lib/ by
// `node official-plugins/java-debug/scripts/fetch-assets.mjs`) and the real
// plugin process: the smoke builds official-plugins/java-debug/backend,
// starts it and plays the host over its stdio JSON-RPC (plugin.initialize,
// plugin.invoke tests.discover / tests.resolveRun, plugin.event
// tests.runEnded; it answers host.jdtls.executeCommand through the real
// jdtls, records host.tests.publish). The debug sessions are started like
// the host does for a jdtls debugger (vscode.java.startDebugSession,
// ConnectAdapter, Begin with the plugin's launch arguments).
//
// Same opt-in environment as TestMachineSmokeJava; the java-test jars are
// taken from the folder of MINCODE_DEBUG_SMOKE_JAVA_DEBUG_JAR:
//
//	DEVELOPER_DIR=/Library/Developer/CommandLineTools CGO_ENABLED=1 \
//	  MINCODE_DEBUG_MACHINE_SMOKE=1 \
//	  MINCODE_DEBUG_SMOKE_JAVA_HOME=/opt/homebrew/opt/openjdk@21 \
//	  MINCODE_DEBUG_SMOKE_JAVA_DEBUG_JAR=$PWD/../official-plugins/java-debug/lib/com.microsoft.java.debug.plugin-0.53.1.jar \
//	  go test ./backend/debug -run TestMachineSmokeJavaIdea3b -v -timeout 900s
//
// HOME and the JVMs' user.home (JDK_JAVA_OPTIONS) point at a temp dir and
// JAVA_HOME is empty: the user's ~/.m2 and jdtls caches are never touched.
// TMPDIR is not isolated: jdtls writes its testNames*.txt lists to
// java.io.tmpdir (on macOS the per-user temp folder whatever $TMPDIR says)
// and the plugin's start-up sweep removes lists older than a day there;
// only lists new since the start count as left behind.
//
// The fake host checks every host.tests.publish call like the real one
// (backend/plugin/test_runs.go, mirrored by idea3bCheckEvent: the
// package is not importable here): params ≤ 256 KiB and ≤ 500 events, ≤ 200
// calls per second, only runs this host resolved that are still live (not
// finished, not 10 s past tests.runEnded), each event decoded strictly with
// only the fields its type allows, ids ≤ 256 bytes, labels one line of ≤ 256
// characters, kind suite|test, relative paths, lines with a path; it fails
// the smoke on any event the real host would drop (dropped must stay 0) and
// on texts it would cut (message 4096 characters, trace / expected / actual /
// output 64 KiB). Not covered (unit tested in package plugin and the
// frontend): the 10000-id cap per run, marking unfinished tests
// "terminated", the UI.

const (
	idea3bSlowTest = "package demo;\n" +
		"\n" +
		"import org.junit.jupiter.api.MethodOrderer;\n" +
		"import org.junit.jupiter.api.Order;\n" +
		"import org.junit.jupiter.api.Test;\n" +
		"import org.junit.jupiter.api.TestMethodOrder;\n" +
		"\n" +
		"@TestMethodOrder(MethodOrderer.OrderAnnotation.class)\n" +
		"class SlowTest {\n" +
		"    @Test\n" +
		"    @Order(1)\n" +
		"    void first() {\n" +
		"    }\n" +
		"\n" +
		"    @Test\n" +
		"    @Order(2)\n" +
		"    void sleeps() throws Exception {\n" +
		"        Thread.sleep(120_000);\n" +
		"    }\n" +
		"\n" +
		"    @Test\n" +
		"    @Order(3)\n" +
		"    void after() {\n" +
		"    }\n" +
		"}\n"
	idea3bCalcPath = "src/test/java/demo/CalcTest.java"
)

// idea3bEvent is a host.tests.publish event (plugin-sdk-go host.TestRunEvent).
type idea3bEvent struct {
	Type       string   `json:"type"`
	ID         string   `json:"id"`
	ParentID   string   `json:"parentId"`
	Label      string   `json:"label"`
	Kind       string   `json:"kind"`
	Path       string   `json:"path"`
	Line       int      `json:"line"`
	DurationMs *float64 `json:"durationMs"`
	Message    string   `json:"message"`
	Expected   *string  `json:"expected"`
	Actual     *string  `json:"actual"`
	Trace      string   `json:"trace"`
	Text       string   `json:"text"`
}

var idea3bEventTypes = []string{"tree", "started", "passed", "failed", "errored", "skipped", "output", "finished"}

// idea3bRawEvent is an event as published, every field optional, for the
// strict per-type check.
type idea3bRawEvent struct {
	Type       string   `json:"type"`
	ID         *string  `json:"id"`
	ParentID   *string  `json:"parentId"`
	Label      *string  `json:"label"`
	Kind       *string  `json:"kind"`
	Path       *string  `json:"path"`
	Line       *int     `json:"line"`
	DurationMs *float64 `json:"durationMs"`
	Message    *string  `json:"message"`
	Expected   *string  `json:"expected"`
	Actual     *string  `json:"actual"`
	Trace      *string  `json:"trace"`
	Text       *string  `json:"text"`
}

// idea3bCheckEvent mirrors the real host's parseTestRunEvent: it returns
// the event, or why the host would drop it ("") — and, separately, why the
// host would cut one of its texts.
func idea3bCheckEvent(raw json.RawMessage) (event idea3bEvent, drop, cut string) {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var in idea3bRawEvent
	if err := decoder.Decode(&in); err != nil {
		return event, "strict decode: " + err.Error(), ""
	}
	if !slices.Contains(idea3bEventTypes, in.Type) {
		return event, "unknown type " + in.Type, ""
	}
	present := map[string]bool{
		"id": in.ID != nil, "parentId": in.ParentID != nil, "label": in.Label != nil, "kind": in.Kind != nil,
		"path": in.Path != nil, "line": in.Line != nil, "durationMs": in.DurationMs != nil, "message": in.Message != nil,
		"expected": in.Expected != nil, "actual": in.Actual != nil, "trace": in.Trace != nil, "text": in.Text != nil,
	}
	var required, optional []string
	switch in.Type {
	case "tree":
		required, optional = []string{"id", "label", "kind"}, []string{"parentId", "path", "line"}
	case "started":
		required = []string{"id"}
	case "passed", "failed", "errored", "skipped":
		required, optional = []string{"id"}, []string{"durationMs", "message", "expected", "actual", "trace"}
	case "output":
		required, optional = []string{"text"}, []string{"id"}
	case "finished":
		optional = []string{"durationMs"}
	}
	for field, set := range present {
		if set && !slices.Contains(required, field) && !slices.Contains(optional, field) {
			return event, in.Type + " must not have " + field, ""
		}
	}
	for _, field := range required {
		if !present[field] {
			return event, in.Type + " needs " + field, ""
		}
	}
	validID := func(id string) bool {
		return id != "" && len(id) <= 256 && utf8.ValidString(id) && strings.TrimSpace(id) == id &&
			!strings.ContainsFunc(id, func(r rune) bool { return unicode.IsControl(r) || r == 0x2028 || r == 0x2029 })
	}
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	event = idea3bEvent{Type: in.Type, ID: str(in.ID), ParentID: str(in.ParentID), Label: str(in.Label), Kind: str(in.Kind),
		Path: str(in.Path), DurationMs: in.DurationMs, Message: str(in.Message), Expected: in.Expected, Actual: in.Actual,
		Trace: str(in.Trace), Text: str(in.Text)}
	if in.Line != nil {
		event.Line = *in.Line
	}
	switch {
	case in.ID != nil && !validID(*in.ID):
		return event, fmt.Sprintf("invalid id %q", *in.ID), ""
	case in.ParentID != nil && (!validID(*in.ParentID) || *in.ParentID == event.ID):
		return event, fmt.Sprintf("invalid parentId %q", *in.ParentID), ""
	case in.Label != nil && (strings.TrimSpace(*in.Label) == "" || utf8.RuneCountInString(strings.TrimSpace(*in.Label)) > 256 ||
		!utf8.ValidString(*in.Label) || strings.ContainsFunc(*in.Label, unicode.IsControl)):
		return event, fmt.Sprintf("invalid label %q", *in.Label), ""
	case in.Kind != nil && *in.Kind != "suite" && *in.Kind != "test":
		return event, "invalid kind " + *in.Kind, ""
	case in.Path != nil && (*in.Path == "" || len(*in.Path) > 1024 || filepath.IsAbs(*in.Path) || strings.ContainsAny(*in.Path, "\\:") ||
		path.Clean(*in.Path) != *in.Path || strings.HasPrefix(*in.Path, "../")):
		return event, fmt.Sprintf("invalid path %q", *in.Path), ""
	case in.Line != nil && (*in.Line < 1 || *in.Line > 10_000_000 || in.Path == nil):
		return event, fmt.Sprintf("invalid line %d", *in.Line), ""
	case in.DurationMs != nil && (math.IsNaN(*in.DurationMs) || *in.DurationMs < 0):
		return event, "invalid durationMs", ""
	case in.Text != nil && strings.TrimSpace(*in.Text) == "":
		return event, "empty output text", ""
	}
	switch {
	case utf8.RuneCountInString(event.Message) > 4096:
		cut = "message over 4096 characters"
	case len(str(in.Trace)) > 64*1024 || len(str(in.Expected)) > 64*1024 || len(str(in.Actual)) > 64*1024 || len(event.Text) > 64*1024:
		cut = "trace, expected, actual or output over 64 KiB"
	}
	return event, "", cut
}

// --- fake host over the plugin's stdio ---------------------------------------

type idea3bReply struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// idea3bHost runs the java-debug plugin process and plays its host. Its
// goroutines never touch a testing.T: what goes wrong is recorded in
// problems and reported by the (sub)test that calls checkProblems.
type idea3bHost struct {
	ctx     context.Context
	root    string
	allowed map[string]bool // jdtls commands of plugin.json's jdtlsBundles

	cmd     *exec.Cmd
	stdin   io.WriteCloser
	exited  chan struct{}
	writeMu sync.Mutex

	mu       sync.Mutex
	nextID   int
	pending  map[string]chan idea3bReply
	events   map[string][]idea3bEvent // by run id
	calls    map[string][]time.Time   // publish calls by run id
	allCalls []time.Time              // every publish call (the host's per-plugin rate)
	live     map[string]bool          // runs resolved and not yet closed
	endedAt  map[string]time.Time     // tests.runEnded sent
	problems []string
	commands []string
	refused  []string
	logs     []string
	stderr   strings.Builder
}

func startIdea3bHost(ctx context.Context, t *testing.T, binary, root string, allowed map[string]bool) *idea3bHost {
	t.Helper()
	h := &idea3bHost{
		ctx: ctx, root: root, allowed: allowed, exited: make(chan struct{}),
		pending: map[string]chan idea3bReply{}, events: map[string][]idea3bEvent{}, calls: map[string][]time.Time{},
		live: map[string]bool{}, endedAt: map[string]time.Time{},
	}
	h.cmd = exec.Command(binary)
	h.cmd.Dir = filepath.Dir(binary)
	stdin, err := h.cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := h.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := h.cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	h.stdin = stdin
	if err := h.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		data, _ := io.ReadAll(stderr)
		h.mu.Lock()
		h.stderr.Write(data)
		h.mu.Unlock()
	}()
	go h.read(stdout)
	t.Cleanup(func() {
		_ = h.stdin.Close()
		select {
		case <-h.exited:
		case <-time.After(10 * time.Second):
			_ = h.cmd.Process.Kill()
			<-h.exited
		}
	})
	var init json.RawMessage
	if err := h.call("plugin.initialize", map[string]any{
		"apiVersion": 1, "runtimeProtocolVersion": 1, "manifestApiVersion": 2, "transport": "stdio",
		"features": map[string]any{},
		"plugin":   map[string]any{"id": "java-debug", "name": "Java Debugger", "version": "0.1.0", "capabilities": []string{"shell", "ui", "workspace.read"}},
		"host":     map[string]any{"name": "MinCode", "platform": "darwin"},
	}, &init, 10*time.Second); err != nil {
		t.Fatalf("plugin.initialize: %v", err)
	}
	return h
}

func (h *idea3bHost) problem(format string, args ...any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.problems = append(h.problems, fmt.Sprintf(format, args...))
}

// checkProblems fails t with the problems recorded since the last check.
func (h *idea3bHost) checkProblems(t *testing.T) {
	t.Helper()
	h.mu.Lock()
	problems := h.problems
	h.problems = nil
	h.mu.Unlock()
	for _, problem := range problems {
		t.Error(problem)
	}
}

func (h *idea3bHost) write(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		h.problem("encode plugin message: %v", err)
		return
	}
	h.writeMu.Lock()
	defer h.writeMu.Unlock()
	_, _ = h.stdin.Write(append(data, '\n'))
}

func (h *idea3bHost) read(stdout io.Reader) {
	defer func() {
		_ = h.cmd.Wait()
		close(h.exited)
	}()
	reader := bufio.NewReaderSize(stdout, 1<<20)
	for {
		line, err := reader.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			h.handle(line)
		}
		if err != nil {
			return
		}
	}
}

func (h *idea3bHost) handle(line []byte) {
	var env struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		idea3bReply
	}
	if err := json.Unmarshal(line, &env); err != nil {
		h.problem("plugin wrote a non-JSON line: %q", line)
		return
	}
	if env.Method == "" {
		var id string
		_ = json.Unmarshal(env.ID, &id)
		h.mu.Lock()
		ch := h.pending[id]
		delete(h.pending, id)
		h.mu.Unlock()
		if ch != nil {
			ch <- env.idea3bReply
		}
		return
	}
	if len(env.ID) == 0 || string(env.ID) == "null" {
		// A notification: served, never answered.
		go h.serve(env.Method, env.Params)
		return
	}
	go func() {
		result, code, message := h.serve(env.Method, env.Params)
		reply := map[string]any{"jsonrpc": "2.0", "id": env.ID}
		if code != 0 {
			reply["error"] = map[string]any{"code": code, "message": message}
		} else {
			reply["result"] = result
		}
		h.write(reply)
	}()
}

// serve answers a host request of the plugin.
func (h *idea3bHost) serve(method string, params json.RawMessage) (any, int, string) {
	switch method {
	case "host.jdtls.executeCommand":
		var p struct {
			Root      string          `json:"root"`
			Command   string          `json:"command"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, -32602, "invalid params"
		}
		h.mu.Lock()
		h.commands = append(h.commands, p.Command)
		h.mu.Unlock()
		if !h.allowed[p.Command] || p.Root != h.root {
			h.mu.Lock()
			h.refused = append(h.refused, p.Command+" "+p.Root)
			h.mu.Unlock()
			return nil, -32001, "command not declared"
		}
		ctx, cancel := context.WithTimeout(h.ctx, 60*time.Second)
		defer cancel()
		result, err := lsp.ExecuteBuiltinCommand(ctx, p.Root, "jdtls", p.Command, p.Arguments)
		if err != nil {
			return nil, -32000, err.Error()
		}
		if len(result) == 0 {
			result = json.RawMessage("null")
		}
		return result, 0, ""
	case "host.configuration.get":
		return map[string]any{"values": map[string]any{}}, 0, ""
	case "host.log":
		h.mu.Lock()
		h.logs = append(h.logs, string(params))
		h.mu.Unlock()
		return true, 0, ""
	case "host.tests.publish":
		return h.publish(params)
	}
	return nil, -32601, "method not found"
}

// publish checks a host.tests.publish call like the real host and records
// its events; anything the real host would refuse, drop or cut is a problem.
func (h *idea3bHost) publish(params json.RawMessage) (any, int, string) {
	now := time.Now()
	h.mu.Lock()
	recent := 0
	for _, at := range h.allCalls {
		if now.Sub(at) < time.Second {
			recent++
		}
	}
	h.allCalls = append(h.allCalls, now)
	h.mu.Unlock()
	if recent >= 200 {
		h.problem("more than 200 host.tests.publish calls per second")
		return nil, -32029, "busy"
	}
	if len(params) > 256*1024 {
		h.problem("host.tests.publish params of %d bytes", len(params))
		return nil, -32602, "too large"
	}
	decoder := json.NewDecoder(strings.NewReader(string(params)))
	decoder.DisallowUnknownFields()
	var p struct {
		RunID  *string           `json:"runId"`
		Events []json.RawMessage `json:"events"`
	}
	if err := decoder.Decode(&p); err != nil || p.RunID == nil || *p.RunID == "" || p.Events == nil {
		h.problem("invalid host.tests.publish params: %v %.200s", err, params)
		return nil, -32602, "invalid params"
	}
	if len(p.Events) > 500 {
		h.problem("host.tests.publish with %d events", len(p.Events))
		return nil, -32602, "too many events"
	}
	runID := *p.RunID
	h.mu.Lock()
	defer h.mu.Unlock()
	ended, wasEnded := h.endedAt[runID]
	if !h.live[runID] || wasEnded && now.Sub(ended) > 10*time.Second {
		h.problems = append(h.problems, fmt.Sprintf("host.tests.publish for run %s, which is unknown or closed", runID))
		return nil, -32001, "the test run is unknown, belongs to another plugin or has ended"
	}
	h.calls[runID] = append(h.calls[runID], now)
	accepted, dropped := 0, 0
	for _, raw := range p.Events {
		if !h.live[runID] {
			dropped++
			h.problems = append(h.problems, fmt.Sprintf("run %s: event after finished: %.200s", runID, raw))
			continue
		}
		event, drop, cut := idea3bCheckEvent(raw)
		if drop != "" {
			dropped++
			h.problems = append(h.problems, fmt.Sprintf("run %s: the host would drop %.300s: %s", runID, raw, drop))
			continue
		}
		if cut != "" {
			h.problems = append(h.problems, fmt.Sprintf("run %s: the host would cut %s %s: %s", runID, event.Type, event.ID, cut))
		}
		accepted++
		h.events[runID] = append(h.events[runID], event)
		if event.Type == "finished" {
			delete(h.live, runID)
		}
	}
	return map[string]int{"accepted": accepted, "dropped": dropped}, 0, ""
}

// call sends a request to the plugin and decodes its result.
func (h *idea3bHost) call(method string, params, out any, timeout time.Duration) error {
	h.mu.Lock()
	h.nextID++
	id := fmt.Sprintf("smoke-%d", h.nextID)
	ch := make(chan idea3bReply, 1)
	h.pending[id] = ch
	h.mu.Unlock()
	h.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	select {
	case reply := <-ch:
		if reply.Error != nil {
			return fmt.Errorf("%s: %d %s", method, reply.Error.Code, reply.Error.Message)
		}
		if out != nil {
			return json.Unmarshal(reply.Result, out)
		}
		return nil
	case <-h.exited:
		return fmt.Errorf("%s: the plugin exited (stderr %q)", method, h.stderrText())
	case <-time.After(timeout):
		return fmt.Errorf("%s: no reply after %s", method, timeout)
	}
}

func (h *idea3bHost) invoke(action string, payload, out any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return h.call("plugin.invoke", map[string]any{"action": action, "payload": string(encoded)}, out, 70*time.Second)
}

func (h *idea3bHost) runEnded(t *testing.T, runID, reason string) {
	t.Helper()
	h.mu.Lock()
	if _, ok := h.endedAt[runID]; !ok {
		h.endedAt[runID] = time.Now()
	}
	h.mu.Unlock()
	if err := h.call("plugin.event", map[string]any{"event": "tests.runEnded", "data": map[string]string{"runId": runID, "reason": reason}}, nil, 10*time.Second); err != nil {
		t.Fatalf("tests.runEnded: %v", err)
	}
}

func (h *idea3bHost) runEvents(runID string) []idea3bEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.events[runID])
}

func (h *idea3bHost) publishCalls(runID string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.calls[runID])
}

func (h *idea3bHost) stderrText() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.stderr.String()
}

// waitEvents polls the run's events until done accepts them.
func (h *idea3bHost) waitEvents(runID string, timeout time.Duration, done func([]idea3bEvent) bool) ([]idea3bEvent, bool) {
	deadline := time.Now().Add(timeout)
	for {
		events := h.runEvents(runID)
		if done(events) {
			return events, true
		}
		if time.Now().After(deadline) {
			return events, false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func hasEvent(events []idea3bEvent, typ, id string) bool {
	return slices.ContainsFunc(events, func(e idea3bEvent) bool { return e.Type == typ && (id == "" || e.ID == id) })
}

// results maps each test id to its last result event.
func results(events []idea3bEvent) map[string]idea3bEvent {
	out := map[string]idea3bEvent{}
	for _, event := range events {
		switch event.Type {
		case "passed", "failed", "errored", "skipped":
			out[event.ID] = event
		}
	}
	return out
}

func logEvents(t *testing.T, name string, events []idea3bEvent) {
	t.Helper()
	var b strings.Builder
	for _, e := range events {
		fmt.Fprintf(&b, "  %-8s %s", e.Type, e.ID)
		if e.Type == "tree" {
			fmt.Fprintf(&b, " parent=%q label=%q kind=%s %s:%d", e.ParentID, e.Label, e.Kind, e.Path, e.Line)
		}
		if e.Message != "" {
			fmt.Fprintf(&b, " message=%q", firstLine(e.Message))
		}
		if e.Expected != nil || e.Actual != nil {
			fmt.Fprintf(&b, " expected=%q actual=%q", deref(e.Expected), deref(e.Actual))
		}
		if e.Trace != "" {
			fmt.Fprintf(&b, " trace=%q", firstLine(e.Trace))
		}
		if e.Text != "" {
			fmt.Fprintf(&b, " text=%q", firstLine(e.Text))
		}
		b.WriteByte('\n')
	}
	t.Logf("%s: %d events\n%s", name, len(events), b.String())
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

// --- the fixture ----------------------------------------------------------------

type idea3bFixture struct {
	ctx  context.Context
	rec  *emitRecorder
	host *idea3bHost
	root string
	java string
}

// idea3bRun is a resolved run: its launch and what the smoke learnt from it.
type idea3bRun struct {
	id        string
	arguments map[string]any
	port      int
	nameFile  string
}

func newRunID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return "run-" + hex.EncodeToString(b)
}

var (
	idea3bPortPattern     = regexp.MustCompile(`(?:^|\s)-port\s+(\d+)`)
	idea3bNameFilePattern = regexp.MustCompile(`(?:^|\s)-testNameFile\s+("(?:[^"\\]|\\.)*"|\S+)`)
)

// resolveRun calls tests.resolveRun and reads the port and the test name
// list from the launch.
func (f *idea3bFixture) resolveRun(t *testing.T, ids []string, debug bool) idea3bRun {
	t.Helper()
	run := idea3bRun{id: newRunID(t)}
	f.host.mu.Lock()
	f.host.live[run.id] = true
	f.host.mu.Unlock()
	var result struct {
		Variables map[string]string `json:"variables"`
		Request   string            `json:"request"`
		Arguments map[string]any    `json:"arguments"`
	}
	if err := f.host.invoke("tests.resolveRun", map[string]any{
		"provider": "junit", "project": map[string]string{"root": f.root}, "testIds": ids, "debug": debug, "runId": run.id,
	}, &result); err != nil {
		t.Fatalf("tests.resolveRun %v: %v", ids, err)
	}
	if result.Request != "launch" || result.Arguments == nil {
		t.Fatalf("tests.resolveRun %v = %+v", ids, result)
	}
	run.arguments = result.Arguments
	args, _ := run.arguments["args"].(string)
	match := idea3bPortPattern.FindStringSubmatch(args)
	if match == nil {
		t.Fatalf("tests.resolveRun %v: no -port in args %q", ids, args)
	}
	run.port, _ = strconv.Atoi(match[1])
	if m := idea3bNameFilePattern.FindStringSubmatch(args); m != nil {
		run.nameFile = m[1]
		if unquoted, err := strconv.Unquote(m[1]); err == nil {
			run.nameFile = unquoted
		}
	}
	if noDebug, _ := run.arguments["noDebug"].(bool); noDebug == debug {
		t.Errorf("tests.resolveRun %v debug=%v: noDebug = %v", ids, debug, run.arguments["noDebug"])
	}
	t.Logf("resolveRun %v debug=%v: mainClass %v, port %d, testNameFile %q, javaExec %v, args %q",
		ids, debug, run.arguments["mainClass"], run.port, run.nameFile, run.arguments["javaExec"], args)
	return run
}

// start launches the run's session like the host does for a jdtls debugger.
func (f *idea3bFixture) start(t *testing.T, run idea3bRun, breakpoints map[string][]SourceBreakpoint) *Session {
	t.Helper()
	var dapPort int
	jdtlsCommand(f.ctx, t, f.root, "vscode.java.startDebugSession", []any{}, &dapPort)
	s, err := ConnectAdapter(f.ctx, ConnectSpec{Port: dapPort, OwnerPluginID: "java-debug", DebuggerType: "java", ProjectRoot: f.root})
	if err != nil {
		t.Fatal(err)
	}
	// A failed run must not leave its test JVM behind: stop like the host
	// (disconnect with terminateDebuggee).
	t.Cleanup(func() { StopSession(context.Background(), s, "smoke done") })
	arguments := map[string]any{"name": "JUnit " + run.id, "request": "launch"}
	for key, value := range run.arguments {
		arguments[key] = value
	}
	encoded, _ := json.Marshal(arguments)
	if _, err := s.Begin(f.ctx, BeginParams{AdapterID: "java", Request: "launch", Arguments: encoded, Breakpoints: breakpoints}); err != nil {
		t.Fatalf("Begin %s: %v", run.id, err)
	}
	return s
}

// finish waits for the session to end, tells the plugin (tests.runEnded)
// and waits for the run's finished event.
func (f *idea3bFixture) finish(t *testing.T, name string, run idea3bRun, s *Session) []idea3bEvent {
	t.Helper()
	ended := f.rec.waitEvent(f.ctx, t, s.ID(), SessionEndedEvent)
	f.host.runEnded(t, run.id, "sessionEnded")
	events, ok := f.host.waitEvents(run.id, 30*time.Second, func(events []idea3bEvent) bool { return hasEvent(events, "finished", "") })
	logEvents(t, name, events)
	if !ok {
		t.Fatalf("%s: no finished event (session ended: %s; output:\n%s)", name, ended.Body, f.rec.output(s.ID()))
	}
	if last := events[len(events)-1]; last.Type != "finished" {
		t.Errorf("%s: last event is %s %s, want finished", name, last.Type, last.ID)
	}
	f.requireCleanedUp(t, name, run)
	return events
}

// requireCleanedUp waits until the plugin deleted the run's test name list
// and no longer listens on its port.
func (f *idea3bFixture) requireCleanedUp(t *testing.T, name string, run idea3bRun) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, statErr := os.Lstat(run.nameFile)
		gone := run.nameFile == "" || errors.Is(statErr, os.ErrNotExist)
		conn, dialErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", run.port), time.Second)
		if conn != nil {
			_ = conn.Close()
		}
		if gone && dialErr != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("%s: not cleaned up after 10s: test name list %q still there: %v, port %d still accepts: %v", name, run.nameFile, !gone, run.port, dialErr == nil)
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// testNameFiles lists the testNames*.txt files in the temp folders the
// plugin and jdtls use.
func testNameFiles() map[string]bool {
	out := map[string]bool{}
	for _, dir := range []string{os.TempDir(), "/tmp"} {
		matches, _ := filepath.Glob(filepath.Join(dir, "testNames*.txt"))
		for _, match := range matches {
			if real, err := filepath.EvalSymlinks(match); err == nil {
				out[real] = true
			}
		}
	}
	return out
}

// idea3bBundles reads the plugin's jdtlsBundles (main jars and their
// dependencies, in manifest order) and their commands.
func idea3bBundles(t *testing.T, pluginRoot string) (jars []string, commands map[string]bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(pluginRoot, "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Contributes struct {
			JdtlsBundles []struct {
				Path         string   `json:"path"`
				Commands     []string `json:"commands"`
				Dependencies []string `json:"dependencies"`
			} `json:"jdtlsBundles"`
		} `json:"contributes"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	commands = map[string]bool{}
	expand := func(path string) string {
		return filepath.Join(pluginRoot, filepath.FromSlash(strings.TrimPrefix(path, "${pluginRoot}/")))
	}
	for _, bundle := range manifest.Contributes.JdtlsBundles {
		jars = append(jars, expand(bundle.Path))
		for _, dep := range bundle.Dependencies {
			jars = append(jars, expand(dep))
		}
		for _, command := range bundle.Commands {
			commands[command] = true
		}
	}
	for _, jar := range jars {
		if _, err := os.Stat(jar); err != nil {
			t.Skipf("%v: run node official-plugins/java-debug/scripts/fetch-assets.mjs", err)
		}
	}
	return jars, commands
}

// buildJavaDebugPlugin builds the plugin's backend from source (GOWORK=off,
// like the official build) into a temp dir.
func buildJavaDebugPlugin(t *testing.T, pluginRoot string) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "mincode-java-debug")
	cmd := exec.Command("go", "build", "-o", binary, ".")
	cmd.Dir = filepath.Join(pluginRoot, "backend")
	cmd.Env = append(os.Environ(), "GOWORK=off")
	started := time.Now()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build the java-debug plugin: %v\n%s", err, out)
	}
	t.Logf("built the java-debug plugin in %s", time.Since(started).Round(time.Millisecond))
	return binary
}

// --- the smoke ----------------------------------------------------------------------

// TestMachineSmokeJavaIdea3b covers, in order: the shipped bundle set loads;
// tests.discover (tree and gutter lines); a class run with passing, failing
// (expected/actual), erroring, skipped (@Disabled and assumption),
// parameterized and nested tests, with a foreign connection to the run's
// port before the JVM; a JUnit 4 class run; a debug run of one method that stops at a
// breakpoint in it and completes after continue; rerunning only the failed
// tests; stopping a session mid-run (the plugin ends the run without
// finished and cleans up); a run ended before its session started; no
// testNames*.txt left behind.
func TestMachineSmokeJavaIdea3b(t *testing.T) {
	javaExec, jar := smokeJava(t)
	lib := filepath.Dir(jar)
	pluginRoot := filepath.Dir(lib)
	bundles, commands := idea3bBundles(t, pluginRoot)
	t.Logf("%d jars from plugin.json jdtlsBundles, %d commands", len(bundles), len(commands))
	binary := buildJavaDebugPlugin(t, pluginRoot) // before HOME changes: the user's Go build cache
	beforeNames := testNameFiles()

	home := isolateHome(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeProbeProject(t, root, lib)
	if err := os.WriteFile(filepath.Join(root, "src/test/java/demo/SlowTest.java"), []byte(idea3bSlowTest), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), javaSmokeTimeout)
	defer cancel()
	rec := captureEmits(t)
	t.Cleanup(func() { lsp.Shutdown(context.Background()) })
	lsp.SetJdtlsBundleProvider(func() []string { return bundles })
	t.Cleanup(func() { lsp.SetJdtlsBundleProvider(nil) })
	t.Cleanup(startJdtls(ctx, t, root, filepath.Join(root, idea3bCalcPath), probeCalcTest))
	for _, class := range []string{"CalcTest", "LegacyTest", "SlowTest"} {
		waitClass(ctx, t, root, "bin-test/demo/"+class+".class")
	}

	// The shipped set loads: every jar is in the session, none failed to
	// install (asm core is jdtls's own).
	loaded, _ := lsp.JdtlsSessionBundles(root)
	if len(loaded) != len(bundles) {
		t.Errorf("jdtls session has %d bundles, want %d", len(loaded), len(bundles))
	}
	if lines := jdtlsLogLines(t, root, home, "Install bundle failure", "Failed to load extension bundles"); len(lines) > 0 {
		t.Errorf("bundle errors in the jdtls log:\n%s", strings.Join(lines, "\n"))
	}

	h := startIdea3bHost(ctx, t, binary, root, commands)
	f := &idea3bFixture{ctx: ctx, rec: rec, host: h, root: root, java: javaExec}
	const (
		calc        = "probe@demo.CalcTest"
		adds        = calc + "#adds()"
		failing     = calc + "#failing()"
		erroring    = calc + "#erroring()"
		skipped     = calc + "#skipped()"
		assumed     = calc + "#assumed()"
		positive    = calc + "#positive(int)"
		inner       = calc + "$Inner"
		innerPasses = inner + "#innerPasses()"
		slow        = "probe@demo.SlowTest"
	)

	t.Run("discover", func(t *testing.T) {
		defer h.checkProblems(t)
		var result struct {
			Tests []discoveredSmokeTest `json:"tests"`
		}
		started := time.Now()
		if err := h.invoke("tests.discover", map[string]any{"provider": "junit", "locale": "en", "project": map[string]string{"name": "probe", "root": root}}, &result); err != nil {
			t.Fatalf("tests.discover: %v", err)
		}
		nodes := map[string]discoveredSmokeTest{}
		var walk func([]discoveredSmokeTest, string)
		var lines []string
		walk = func(tests []discoveredSmokeTest, indent string) {
			for _, node := range tests {
				nodes[node.ID] = node
				lines = append(lines, fmt.Sprintf("%s%s %q %s:%d", indent, node.ID, node.Label, node.Path, node.Line))
				walk(node.Children, indent+"  ")
			}
		}
		walk(result.Tests, "  ")
		t.Logf("tests.discover (%s):\n%s", time.Since(started).Round(time.Millisecond), strings.Join(lines, "\n"))
		want := map[string]int{
			calc: 13, adds: 15, failing: 21, erroring: 26, skipped: 32, assumed: 36, positive: 43, inner: 48, innerPasses: 50,
			"probe@demo.LegacyTest": 8, "probe@demo.LegacyTest#passes": 10, "probe@demo.LegacyTest#fails": 15, "probe@demo.LegacyTest#ignored": 21,
			slow: 9, slow + "#first()": 12, slow + "#sleeps()": 17, slow + "#after()": 23,
		}
		for id, line := range want {
			node, ok := nodes[id]
			switch {
			case !ok:
				t.Errorf("no discovered %s", id)
			case node.Line != line || !strings.HasPrefix(node.Path, "src/test/java/demo/"):
				t.Errorf("%s at %s:%d, want line %d", id, node.Path, node.Line, line)
			}
		}
		if len(result.Tests) != 1 || result.Tests[0].ID != "probe@demo.*" {
			t.Errorf("roots %+v, want the package probe@demo.*", result.Tests)
		}
	})

	var failedIDs []string
	t.Run("run class", func(t *testing.T) {
		defer h.checkProblems(t)
		run := f.resolveRun(t, []string{calc}, false)
		// A foreign connection before the JVM: closed, the run goes on.
		if conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", run.port), 2*time.Second); err != nil {
			t.Errorf("dial the run's port: %v", err)
		} else {
			_, _ = conn.Write([]byte("GET / HTTP/1.0\r\n\r\n"))
			_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
			if _, err := io.ReadAll(conn); err != nil {
				t.Errorf("the plugin did not close a foreign connection: %v", err)
			}
			_ = conn.Close()
		}
		s := f.start(t, run, nil)
		events := f.finish(t, "run class", run, s)
		got := results(events)
		expect := map[string]string{
			adds: "passed", failing: "failed", erroring: "errored", skipped: "skipped", assumed: "skipped", innerPasses: "passed",
		}
		for id, status := range expect {
			if got[id].Type != status {
				t.Errorf("%s: %q, want %s", id, got[id].Type, status)
			}
		}
		if e := got[failing]; deref(e.Expected) != "6" || deref(e.Actual) != "5" || !strings.Contains(e.Message, "two plus three") || e.Trace == "" {
			t.Errorf("failing(): expected %q actual %q message %q trace %d bytes", deref(e.Expected), deref(e.Actual), e.Message, len(e.Trace))
		}
		if e := got[erroring]; !strings.Contains(e.Trace, "IllegalStateException") || !strings.Contains(e.Trace+e.Message, "boom") {
			t.Errorf("erroring(): message %q trace %q", e.Message, firstLine(e.Trace))
		}
		// The runner sends "@Ignore: skipped(demo.CalcTest)" without the
		// @Disabled reason; an assumption failure comes with its message.
		if e := got[assumed]; !strings.Contains(e.Message, "assumption") {
			t.Errorf("assumed(): message %q", e.Message)
		}
		// Parameterized: one tree node per invocation under the method.
		var invocations []idea3bEvent
		for _, e := range events {
			if e.Type == "tree" && e.ParentID == positive {
				invocations = append(invocations, e)
			}
		}
		statuses := map[string]int{}
		for _, e := range invocations {
			statuses[got[e.ID].Type]++
			if got[e.ID].Type == "failed" {
				failedIDs = append(failedIDs, e.ID)
			}
		}
		if len(invocations) != 3 || statuses["passed"] != 2 || statuses["failed"] != 1 {
			t.Errorf("positive(int) invocations %+v, statuses %v; want 3 with 2 passed, 1 failed", invocations, statuses)
		}
		for _, e := range events {
			if e.Type == "tree" && e.ID == adds && (e.Path != idea3bCalcPath || e.Line != 15) {
				t.Errorf("adds() tree event at %s:%d, want %s:15", e.Path, e.Line, idea3bCalcPath)
			}
			if e.Type == "started" && !slices.ContainsFunc(events, func(tree idea3bEvent) bool { return tree.Type == "tree" && tree.ID == e.ID }) {
				t.Errorf("started %s without a tree event", e.ID)
			}
		}
		for _, id := range []string{adds, failing, erroring, innerPasses} {
			if !hasEvent(events, "started", id) {
				t.Errorf("no started event for %s", id)
			}
		}
		failedIDs = append(failedIDs, failing, erroring)
	})

	t.Run("run JUnit 4 class", func(t *testing.T) {
		defer h.checkProblems(t)
		legacy := "probe@demo.LegacyTest"
		run := f.resolveRun(t, []string{legacy}, false)
		s := f.start(t, run, nil)
		got := results(f.finish(t, "run JUnit 4 class", run, s))
		expect := map[string]string{legacy + "#passes": "passed", legacy + "#fails": "failed", legacy + "#ignored": "skipped"}
		for id, status := range expect {
			if got[id].Type != status {
				t.Errorf("%s: %q, want %s", id, got[id].Type, status)
			}
		}
		// JUnit 4's assertEquals(String, long, long) throws a plain
		// AssertionError (only ComparisonFailure carries expected/actual).
		if e := got[legacy+"#fails"]; !strings.Contains(e.Message, "expected:<6> but was:<5>") || e.Trace == "" {
			t.Errorf("fails: message %q, trace %d bytes", e.Message, len(e.Trace))
		}
	})

	t.Run("debug method", func(t *testing.T) {
		defer h.checkProblems(t)
		run := f.resolveRun(t, []string{adds}, true)
		s := f.start(t, run, map[string][]SourceBreakpoint{filepath.Join(root, idea3bCalcPath): {{Line: probeBreakpointLine}}})
		stopped := rec.waitEvent(ctx, t, s.ID(), "stopped")
		var stop idea2Stop
		_ = json.Unmarshal(stopped.Body, &stop)
		var trace struct {
			StackFrames []idea3Frame `json:"stackFrames"`
		}
		smokeRequest(ctx, t, s, "stackTrace", map[string]any{"threadId": stop.ThreadID, "startFrame": 0, "levels": 3}, &trace)
		if len(trace.StackFrames) == 0 || trace.StackFrames[0].Line != probeBreakpointLine || !strings.HasPrefix(trace.StackFrames[0].Name, "CalcTest.adds") {
			t.Fatalf("paused at %+v, want CalcTest.adds line %d", trace.StackFrames, probeBreakpointLine)
		}
		t.Logf("paused (%s) in %s line %d", stop.Reason, trace.StackFrames[0].Name, trace.StackFrames[0].Line)
		if events, _ := h.waitEvents(run.id, 5*time.Second, func(events []idea3bEvent) bool { return hasEvent(events, "started", adds) }); !hasEvent(events, "started", adds) || len(results(events)) != 0 {
			t.Errorf("while paused: want adds() started without a result, got %+v", events)
		}
		smokeRequest(ctx, t, s, "continue", map[string]any{"threadId": stop.ThreadID}, nil)
		events := f.finish(t, "debug method", run, s)
		got := results(events)
		if got[adds].Type != "passed" || len(got) != 1 {
			t.Errorf("results %v, want only adds() passed", got)
		}
	})

	t.Run("rerun failed", func(t *testing.T) {
		defer h.checkProblems(t)
		if len(failedIDs) != 3 {
			t.Fatalf("failed tests of the class run: %v", failedIDs)
		}
		run := f.resolveRun(t, failedIDs, false)
		s := f.start(t, run, nil)
		events := f.finish(t, "rerun failed", run, s)
		got := results(events)
		for id, e := range got {
			if id != failing && id != erroring && !strings.HasPrefix(id, positive+"/") {
				t.Errorf("rerun reported %s %s: only the failed methods should run", e.Type, id)
			}
		}
		if got[failing].Type != "failed" || got[erroring].Type != "errored" {
			t.Errorf("rerun: failing() %q, erroring() %q", got[failing].Type, got[erroring].Type)
		}
		for _, id := range failedIDs {
			if strings.HasPrefix(id, positive+"/") && got[id].Type != "failed" {
				t.Errorf("rerun: the failed invocation %s is %q, want failed", id, got[id].Type)
			}
		}
		for _, id := range []string{adds, skipped, assumed, innerPasses} {
			if hasEvent(events, "started", id) {
				t.Errorf("rerun started %s", id)
			}
		}
	})

	t.Run("stop mid-run", func(t *testing.T) {
		defer h.checkProblems(t)
		run := f.resolveRun(t, []string{slow}, false)
		s := f.start(t, run, nil)
		sleeps := slow + "#sleeps()"
		if _, ok := h.waitEvents(run.id, 60*time.Second, func(events []idea3bEvent) bool { return hasEvent(events, "started", sleeps) }); !ok {
			t.Fatalf("sleeps() never started: %+v", h.runEvents(run.id))
		}
		if jvm := runnerProcesses(run.port); len(jvm) == 0 {
			t.Errorf("no test JVM with -port %d found while sleeps() runs: the exit check below would prove nothing", run.port)
		}
		stopped := time.Now()
		StopSession(ctx, s, "user stopped the test run")
		rec.waitEvent(ctx, t, s.ID(), SessionEndedEvent)
		sessionEnded := time.Since(stopped)
		h.runEnded(t, run.id, "stopped")
		f.requireCleanedUp(t, "stop mid-run", run)
		t.Logf("stop mid-run: session ended %s after the stop, run cleaned up %s after tests.runEnded",
			sessionEnded.Round(time.Millisecond), (time.Since(stopped) - sessionEnded).Round(time.Millisecond))
		if jvm := runnerProcesses(run.port); len(jvm) > 0 {
			t.Errorf("the test JVM still runs after the stop: %v", jvm)
		}
		calls := h.publishCalls(run.id)
		time.Sleep(3 * time.Second) // runEndDrain is 2 s: nothing may follow
		events := h.runEvents(run.id)
		logEvents(t, "stop mid-run", events)
		if h.publishCalls(run.id) != calls {
			t.Errorf("publishes after the run was cleaned up")
		}
		got := results(events)
		if got[slow+"#first()"].Type != "passed" {
			t.Errorf("first(): %q, want passed", got[slow+"#first()"].Type)
		}
		if e, ok := got[sleeps]; ok {
			t.Errorf("sleeps() has a result %s; it was stopped", e.Type)
		}
		if hasEvent(events, "started", slow+"#after()") || hasEvent(events, "finished", "") {
			t.Errorf("after() started or the run finished: the host must mark them terminated")
		}
	})

	t.Run("ended before the session", func(t *testing.T) {
		defer h.checkProblems(t)
		run := f.resolveRun(t, []string{slow}, false)
		h.runEnded(t, run.id, "expired")
		f.requireCleanedUp(t, "ended before the session", run)
		if events := h.runEvents(run.id); len(events) != 0 {
			t.Errorf("events of a run that never started: %+v", events)
		}
	})

	h.checkProblems(t)
	if err := h.call("plugin.shutdown", map[string]any{}, nil, 10*time.Second); err != nil {
		t.Errorf("plugin.shutdown: %v", err)
	}
	select {
	case <-h.exited:
	case <-time.After(10 * time.Second):
		t.Errorf("the plugin did not exit after plugin.shutdown")
	}
	// Publish violations recorded while the plugin shut down.
	h.checkProblems(t)
	h.mu.Lock()
	t.Logf("plugin jdtls commands: %d calls (%v); refused %v; host.log %v; stderr %q", len(h.commands), uniqueSorted(h.commands), h.refused, h.logs, h.stderr.String())
	if len(h.refused) > 0 {
		t.Errorf("the plugin called undeclared jdtls commands: %v", h.refused)
	}
	h.mu.Unlock()
	for name := range testNameFiles() {
		if !beforeNames[name] {
			t.Errorf("test name list left behind: %s", name)
		}
	}
}

type discoveredSmokeTest struct {
	ID       string                `json:"id"`
	Label    string                `json:"label"`
	Path     string                `json:"path"`
	Line     int                   `json:"line"`
	Children []discoveredSmokeTest `json:"children"`
}

func uniqueSorted(values []string) []string {
	out := slices.Clone(values)
	slices.Sort(out)
	return slices.Compact(out)
}

// runnerProcesses lists the processes whose command line has "-port <port>"
// (the run's test JVM).
func runnerProcesses(port int) []string {
	out, err := exec.Command("ps", "-axo", "pid=,command=").Output()
	if err != nil {
		return nil
	}
	var found []string
	needle := fmt.Sprintf("-port %d ", port)
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line+" ", needle) && strings.Contains(line, "RemoteTestRunner") {
			found = append(found, strings.TrimSpace(line)[:min(len(strings.TrimSpace(line)), 120)])
		}
	}
	return found
}

// TestIdea3bCheckEvent keeps the smoke's mirror of the host's event check
// honest (runs without the smoke environment).
func TestIdea3bCheckEvent(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		drop bool
		cut  bool
	}{
		{`{"type":"tree","id":"p@a.B","label":"B","kind":"suite","parentId":"p@a.*","path":"src/B.java","line":3}`, false, false},
		{`{"type":"started","id":"p@a.B#m()"}`, false, false},
		{`{"type":"failed","id":"p@a.B#m()","message":"x","expected":"1","actual":"2","trace":"t","durationMs":1.5}`, false, false},
		{`{"type":"output","text":"hi"}`, false, false},
		{`{"type":"finished"}`, false, false},
		{`{"type":"started","id":"p@a.B#m()","label":"m"}`, true, false},    // field the type does not allow
		{`{"type":"passed"}`, true, false},                                  // id missing
		{`{"type":"started","id":"p@a.B#m()","extra":1}`, true, false},      // unknown field
		{`{"type":"bogus","id":"x"}`, true, false},                          // unknown type
		{`{"type":"tree","id":"x","label":"x","kind":"leaf"}`, true, false}, // kind
		{`{"type":"tree","id":"x","label":"x","kind":"test","line":3}`, true, false},
		{`{"type":"tree","id":"x","label":"x","kind":"test","path":"/abs/B.java"}`, true, false},
		{`{"type":"tree","id":"x","label":"x","kind":"test","parentId":"x"}`, true, false},
		{`{"type":"started","id":" x"}`, true, false},
		{`{"type":"started","id":"` + strings.Repeat("x", 257) + `"}`, true, false},
		{`{"type":"tree","id":"x","label":"` + strings.Repeat("é", 257) + `","kind":"test"}`, true, false},
		{`{"type":"output","text":" "}`, true, false},
		{`{"type":"failed","id":"x","trace":"` + strings.Repeat("x", 64*1024+1) + `"}`, false, true},
	} {
		_, drop, cut := idea3bCheckEvent(json.RawMessage(tc.raw))
		if (drop != "") != tc.drop || (cut != "") != tc.cut {
			t.Errorf("%.120s: drop %q cut %q, want drop %v cut %v", tc.raw, drop, cut, tc.drop, tc.cut)
		}
	}
}
