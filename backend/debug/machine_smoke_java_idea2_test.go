package debug

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"mincode/app/backend/lsp"
)

// Phase 2 of Java debugging close to IDEA, against a real jdtls with the
// java-debug bundle (same opt-in environment as TestMachineSmokeJava; the
// jar must be the one fetch-assets.mjs puts in official-plugins/java-debug/lib):
//
//	MINCODE_DEBUG_MACHINE_SMOKE=1 \
//	  MINCODE_DEBUG_SMOKE_JAVA_HOME=/opt/homebrew/opt/openjdk@21 \
//	  MINCODE_DEBUG_SMOKE_JAVA_DEBUG_JAR=$PWD/../official-plugins/java-debug/lib/com.microsoft.java.debug.plugin-0.53.1.jar \
//	  go test ./backend/debug -run TestMachineSmokeJavaIdea2 -v -timeout 900s

// idea2Fixture is a temp project with one Java source, a running jdtls with
// java-debug, and the launch arguments of its main class.
type idea2Fixture struct {
	ctx    context.Context
	rec    *emitRecorder
	root   string
	source string
	args   json.RawMessage
}

// newIdea2Fixture writes src/<class>.java with text, starts jdtls on it and
// resolves the launch arguments, with the settings debug.applySettings sends.
func newIdea2Fixture(t *testing.T, class, text string) *idea2Fixture {
	t.Helper()
	javaExec, jar := smokeJava(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("JAVA_HOME", "")
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "src", class+".java")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), javaSmokeTimeout)
	t.Cleanup(cancel)
	rec := captureEmits(t)
	t.Cleanup(func() {
		if t.Failed() {
			rec.logAll(t)
		}
	})
	t.Cleanup(func() { lsp.Shutdown(context.Background()) })
	lsp.SetJdtlsBundleProvider(func() []string { return []string{jar} })
	t.Cleanup(func() { lsp.SetJdtlsBundleProvider(nil) })
	t.Cleanup(startJdtls(ctx, t, root, source, text))
	var classpaths [2][]string
	jdtlsCommand(ctx, t, root, "vscode.java.resolveClasspath", []any{class, ""}, &classpaths)
	var ignored json.RawMessage
	jdtlsCommand(ctx, t, root, "vscode.java.updateDebugSettings", []any{`{"logLevel":"WARN","showLogicalStructure":true,"showToString":true,"exceptionFilters":{"exceptionTypes":[]}}`}, &ignored)
	args, err := json.Marshal(map[string]any{"type": "java", "mainClass": class, "classPaths": classpaths[1], "modulePaths": nonNil(classpaths[0]), "javaExec": javaExec, "cwd": root, "console": "internalConsole", "stepFilters": smokeStepFilters})
	if err != nil {
		t.Fatal(err)
	}
	return &idea2Fixture{ctx: ctx, rec: rec, root: root, source: source, args: args}
}

// launch starts a debug session with breakpoints and returns it with the
// adapter's capabilities.
func (f *idea2Fixture) launch(t *testing.T, name string, breakpoints map[string][]SourceBreakpoint) (*Session, json.RawMessage) {
	t.Helper()
	return f.launchWith(t, name, BeginParams{Breakpoints: breakpoints})
}

// launchWith starts a debug session with params (adapter id, request and
// arguments filled in) and returns it with the adapter's capabilities.
func (f *idea2Fixture) launchWith(t *testing.T, name string, params BeginParams) (*Session, json.RawMessage) {
	t.Helper()
	var port int
	jdtlsCommand(f.ctx, t, f.root, "vscode.java.startDebugSession", []any{}, &port)
	s, err := ConnectAdapter(f.ctx, ConnectSpec{Port: port, OwnerPluginID: "machine-smoke", DebuggerType: "java", ProjectRoot: f.root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(name + " cleanup") })
	params.AdapterID, params.Request, params.Arguments = "java", "launch", f.args
	capabilities, err := s.Begin(f.ctx, params)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return s, capabilities
}

// idea2Stop is one stopped event.
type idea2Stop struct {
	Reason   string `json:"reason"`
	ThreadID int    `json:"threadId"`
}

// waitStop waits for the n-th (1-based) stopped event of s; ended when the
// program ended first.
func (f *idea2Fixture) waitStop(t *testing.T, s *Session, n int) (stop idea2Stop, ended bool) {
	t.Helper()
	waitCtx, cancel := context.WithTimeout(f.ctx, smokePauseTimeout)
	defer cancel()
	ev, ended := f.rec.waitNthEvent(waitCtx, t, s.ID(), "stopped", n)
	if ended {
		return idea2Stop{}, true
	}
	_ = json.Unmarshal(ev.Body, &stop)
	return stop, false
}

// idea2Frame is one DAP stack frame.
type idea2Frame struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Line       int    `json:"line"`
	CanRestart *bool  `json:"canRestart"`
}

func (f *idea2Fixture) frames(t *testing.T, s *Session, threadID int) []idea2Frame {
	t.Helper()
	var trace struct {
		StackFrames []idea2Frame `json:"stackFrames"`
	}
	smokeRequest(f.ctx, t, s, "stackTrace", map[string]any{"threadId": threadID, "startFrame": 0, "levels": 20}, &trace)
	if len(trace.StackFrames) == 0 {
		t.Fatalf("thread %d has no frames", threadID)
	}
	return trace.StackFrames
}

// evaluate returns the result of expression in frameID.
func (f *idea2Fixture) evaluate(t *testing.T, s *Session, frameID int, expression string) string {
	t.Helper()
	var result struct {
		Result string `json:"result"`
	}
	smokeRequest(f.ctx, t, s, "evaluate", map[string]any{"expression": expression, "frameId": frameID, "context": "watch"}, &result)
	return result.Result
}

// TestMachineSmokeJavaIdea2RestartFrame checks Drop Frame (DAP restartFrame):
// main → outer() → inner(), paused in inner; restartFrame on outer's frame
// drops inner and outer, and the stopped event that follows is at outer's
// first line. inner then runs again (its run counter reads 2 at the
// breakpoint). Restarting main, the bottom frame, is rejected with
// java-debug's message, and the session stays paused where it was.
func TestMachineSmokeJavaIdea2RestartFrame(t *testing.T) {
	text := "public class DropApp {\n" +
		"    static int innerRuns = 0;\n" +
		"    public static void main(String[] args) {\n" +
		"        outer();\n" +
		"        System.out.println(\"runs \" + innerRuns);\n" +
		"    }\n" +
		"    static void outer() {\n" +
		"        int base = 1;\n" +
		"        inner(base);\n" +
		"    }\n" +
		"    static void inner(int base) {\n" +
		"        innerRuns++;\n" +
		"        System.out.println(\"inner \" + (innerRuns + base));\n" +
		"    }\n" +
		"}\n"
	const outerFirstLine, innerBreakLine = 8, 13
	f := newIdea2Fixture(t, "DropApp", text)
	s, capabilities := f.launch(t, "restart frame", map[string][]SourceBreakpoint{f.source: {{Line: innerBreakLine}}})
	var caps struct {
		SupportsRestartFrame bool `json:"supportsRestartFrame"`
	}
	_ = json.Unmarshal(capabilities, &caps)
	if !caps.SupportsRestartFrame {
		t.Fatalf("capabilities %s, want supportsRestartFrame", capabilities)
	}

	stop, ended := f.waitStop(t, s, 1)
	if ended {
		t.Fatal("the program ended before the breakpoint in inner")
	}
	frames := f.frames(t, s, stop.ThreadID)
	if len(frames) < 3 || frames[0].Line != innerBreakLine || !strings.Contains(frames[1].Name, "outer") {
		t.Fatalf("paused at %+v, want inner:%d called from outer", frames, innerBreakLine)
	}
	for _, frame := range frames {
		if frame.CanRestart != nil {
			t.Logf("frame %s declares canRestart=%v", frame.Name, *frame.CanRestart)
		}
	}
	if got := f.evaluate(t, s, frames[0].ID, "innerRuns"); got != "1" {
		t.Fatalf("innerRuns = %q at the first pause, want 1", got)
	}

	// Drop to outer: the next stop is at its first line, inner and outer are gone.
	smokeRequest(f.ctx, t, s, "restartFrame", map[string]any{"frameId": frames[1].ID}, nil)
	stop, ended = f.waitStop(t, s, 2)
	if ended {
		t.Fatal("the program ended after restartFrame, want a stop in outer")
	}
	t.Logf("stopped after restartFrame: reason %q", stop.Reason)
	frames = f.frames(t, s, stop.ThreadID)
	if !strings.Contains(frames[0].Name, "outer") || frames[0].Line != outerFirstLine {
		t.Fatalf("top frame after restartFrame %+v, want outer at line %d", frames[0], outerFirstLine)
	}
	if len(frames) < 2 || !strings.Contains(frames[1].Name, "main") {
		t.Fatalf("frames after restartFrame %+v, want outer called from main", frames)
	}

	// inner runs again: the counter has gone up once more at its breakpoint.
	smokeRequest(f.ctx, t, s, "continue", map[string]any{"threadId": stop.ThreadID}, nil)
	stop, ended = f.waitStop(t, s, 3)
	if ended {
		t.Fatal("the program ended, want inner's breakpoint again")
	}
	frames = f.frames(t, s, stop.ThreadID)
	if frames[0].Line != innerBreakLine {
		t.Fatalf("paused at %+v, want inner:%d again", frames[0], innerBreakLine)
	}
	if got := f.evaluate(t, s, frames[0].ID, "innerRuns"); got != "2" {
		t.Fatalf("innerRuns = %q after the drop, want 2 (inner ran again)", got)
	}

	// The bottom frame cannot be restarted: java-debug's message, nothing moves.
	bottom := frames[len(frames)-1]
	_, err := s.Request(f.ctx, "restartFrame", map[string]any{"frameId": bottom.ID})
	var requestErr *RequestError
	if !errors.As(err, &requestErr) || strings.TrimSpace(requestErr.Message) == "" {
		t.Fatalf("restartFrame on the bottom frame %s: %v, want a DAP error response with a message", bottom.Name, err)
	}
	t.Logf("restartFrame on %s: rejected with java-debug's message %q", bottom.Name, requestErr.Message)
	frames = f.frames(t, s, stop.ThreadID)
	if frames[0].Line != innerBreakLine {
		t.Fatalf("paused at %+v after the rejected restart, want inner:%d", frames[0], innerBreakLine)
	}

	smokeRequest(f.ctx, t, s, "continue", map[string]any{"threadId": stop.ThreadID}, nil)
	if _, ended := f.waitStop(t, s, 4); !ended {
		t.Fatal("paused again, want the program to end")
	}
	if out := f.rec.output(s.ID()); !strings.Contains(out, "runs 2") {
		t.Errorf("program output %q, want runs 2", out)
	}
}

// TestMachineSmokeJavaIdea2StepInTargets checks Smart Step Into (DAP
// stepInTargets): paused on `int r = add(twice(1), twice(2));`, java-debug
// lists the calls of the line (at least twice and add), and stepIn with the
// add target's id enters add, skipping both calls of twice.
func TestMachineSmokeJavaIdea2StepInTargets(t *testing.T) {
	text := "public class StepTargetsApp {\n" +
		"    public static void main(String[] args) {\n" +
		"        int r = add(twice(1), twice(2));\n" +
		"        System.out.println(\"r \" + r);\n" +
		"    }\n" +
		"    static int twice(int x) {\n" +
		"        return x * 2;\n" +
		"    }\n" +
		"    static int add(int a, int b) {\n" +
		"        return a + b;\n" +
		"    }\n" +
		"}\n"
	const callLine = 3
	f := newIdea2Fixture(t, "StepTargetsApp", text)
	s, capabilities := f.launch(t, "step in targets", map[string][]SourceBreakpoint{f.source: {{Line: callLine}}})
	var caps struct {
		SupportsStepInTargetsRequest bool `json:"supportsStepInTargetsRequest"`
	}
	_ = json.Unmarshal(capabilities, &caps)
	if !caps.SupportsStepInTargetsRequest {
		t.Fatalf("capabilities %s, want supportsStepInTargetsRequest", capabilities)
	}

	stop, ended := f.waitStop(t, s, 1)
	if ended {
		t.Fatal("the program ended before the breakpoint on the call line")
	}
	frames := f.frames(t, s, stop.ThreadID)
	if frames[0].Line != callLine {
		t.Fatalf("paused at %+v, want main:%d", frames[0], callLine)
	}

	var answer struct {
		Targets []struct {
			ID     int    `json:"id"`
			Label  string `json:"label"`
			Line   int    `json:"line"`
			Column int    `json:"column"`
		} `json:"targets"`
	}
	smokeRequest(f.ctx, t, s, "stepInTargets", map[string]any{"frameId": frames[0].ID}, &answer)
	if len(answer.Targets) < 2 {
		t.Fatalf("stepInTargets %+v, want at least 2 targets (twice and add)", answer.Targets)
	}
	addTarget := -1
	for _, target := range answer.Targets {
		t.Logf("step-in target %d: %q (line %d, column %d)", target.ID, target.Label, target.Line, target.Column)
		if strings.Contains(target.Label, "add") {
			addTarget = target.ID
		}
	}
	if addTarget < 0 {
		t.Fatalf("stepInTargets %+v, want a target for add", answer.Targets)
	}

	smokeRequest(f.ctx, t, s, "stepIn", map[string]any{"threadId": stop.ThreadID, "targetId": addTarget}, nil)
	stop, ended = f.waitStop(t, s, 2)
	if ended {
		t.Fatal("the program ended after stepIn into add, want a stop in add")
	}
	frames = f.frames(t, s, stop.ThreadID)
	if method := idea2MethodName(frames[0].Name); method != "add" {
		t.Fatalf("top frame after stepIn with add's target %+v (method %q), want add", frames[0], method)
	}
	if got := f.evaluate(t, s, frames[0].ID, "a + b"); got != "6" {
		t.Errorf("a + b = %q in add, want 6 (twice ran for both arguments)", got)
	}

	smokeRequest(f.ctx, t, s, "continue", map[string]any{"threadId": stop.ThreadID}, nil)
	if _, ended := f.waitStop(t, s, 3); !ended {
		t.Fatal("paused again, want the program to end")
	}
	if out := f.rec.output(s.ID()); !strings.Contains(out, "r 6") {
		t.Errorf("program output %q, want r 6", out)
	}
}

// TestMachineSmokeJavaIdea2Completions checks completions in the evaluate,
// watch and console inputs (DAP completions): paused in a frame with a local
// `list` (an ArrayList), `{text: "li", column: 3}` offers list and
// `{text: "list.si", column: 8}` offers size(). Columns are 1-based UTF-16
// (MinCode initializes with columnsStartAt1). java-debug 0.53.1 answers items
// such as {label: "size() : int", text: "size()", type: "function", start: 0}
// without a length (and some 145 items for "li", classes included), so the
// frontend replaces the identifier before the cursor unless an item has a
// length (then that many code units before the cursor; start is not used).
func TestMachineSmokeJavaIdea2Completions(t *testing.T) {
	text := "import java.util.ArrayList;\n" +
		"public class CompletionsApp {\n" +
		"    public static void main(String[] args) {\n" +
		"        ArrayList<String> list = new ArrayList<>();\n" +
		"        list.add(\"a\");\n" +
		"        System.out.println(\"size \" + list.size());\n" +
		"    }\n" +
		"}\n"
	const breakLine = 6
	f := newIdea2Fixture(t, "CompletionsApp", text)
	s, capabilities := f.launch(t, "completions", map[string][]SourceBreakpoint{f.source: {{Line: breakLine}}})
	var caps struct {
		SupportsCompletionsRequest bool `json:"supportsCompletionsRequest"`
	}
	_ = json.Unmarshal(capabilities, &caps)
	if !caps.SupportsCompletionsRequest {
		t.Fatalf("capabilities %s, want supportsCompletionsRequest", capabilities)
	}

	stop, ended := f.waitStop(t, s, 1)
	if ended {
		t.Fatal("the program ended before the breakpoint")
	}
	frames := f.frames(t, s, stop.ThreadID)
	if frames[0].Line != breakLine {
		t.Fatalf("paused at %+v, want main:%d", frames[0], breakLine)
	}

	type completionItem struct {
		Label  string `json:"label"`
		Text   string `json:"text"`
		Type   string `json:"type"`
		Start  *int   `json:"start"`
		Length *int   `json:"length"`
	}
	complete := func(text string, column int) []completionItem {
		t.Helper()
		var answer struct {
			Targets []completionItem `json:"targets"`
		}
		smokeRequest(f.ctx, t, s, "completions", map[string]any{"text": text, "column": column, "frameId": frames[0].ID}, &answer)
		t.Logf("completions %q@%d: %d items", text, column, len(answer.Targets))
		for i, item := range answer.Targets {
			if i == 3 {
				break
			}
			start, length := "-", "-"
			if item.Start != nil {
				start = strconv.Itoa(*item.Start)
			}
			if item.Length != nil {
				length = strconv.Itoa(*item.Length)
			}
			t.Logf("completions %q@%d: label %q text %q type %q start %s length %s", text, column, item.Label, item.Text, item.Type, start, length)
		}
		return answer.Targets
	}
	inserted := func(item completionItem) string {
		if item.Text != "" {
			return item.Text
		}
		return item.Label
	}

	found := false
	for _, item := range complete("li", 3) {
		if inserted(item) == "list" || item.Label == "list" || strings.HasPrefix(item.Label, "list ") {
			found = true
		}
	}
	if !found {
		t.Fatal(`completions "li" have no list`)
	}
	found = false
	for _, item := range complete("list.si", 8) {
		if strings.HasPrefix(inserted(item), "size(") || strings.HasPrefix(item.Label, "size()") {
			found = true
		}
	}
	if !found {
		t.Fatal(`completions "list.si" have no size()`)
	}

	smokeRequest(f.ctx, t, s, "continue", map[string]any{"threadId": stop.ThreadID}, nil)
	if _, ended := f.waitStop(t, s, 2); !ended {
		t.Fatal("paused again, want the program to end")
	}
	if out := f.rec.output(s.ID()); !strings.Contains(out, "size 1") {
		t.Errorf("program output %q, want size 1", out)
	}
}

// idea2MethodName is the method of a java-debug frame name such as
// "StepTargetsApp.add(int, int)" or "add(int, int)".
func idea2MethodName(frame string) string {
	name, _, _ := strings.Cut(frame, "(")
	if dot := strings.LastIndex(name, "."); dot >= 0 {
		name = name[dot+1:]
	}
	return strings.TrimSpace(name)
}

// idea2Breakpoint is one function breakpoint result (setFunctionBreakpoints
// response or `breakpoint` event).
type idea2Breakpoint struct {
	ID       int    `json:"id"`
	Verified bool   `json:"verified"`
	Message  string `json:"message"`
}

// verifiedLater reports whether a `breakpoint` event of s marked id verified.
func (f *idea2Fixture) verifiedLater(s *Session, id int) bool {
	f.rec.mu.Lock()
	defer f.rec.mu.Unlock()
	for _, ev := range f.rec.events {
		if ev.SessionID != s.ID() || ev.Event != "breakpoint" {
			continue
		}
		var body struct {
			Breakpoint idea2Breakpoint `json:"breakpoint"`
		}
		if json.Unmarshal(ev.Body, &body) == nil && body.Breakpoint.ID == id && body.Breakpoint.Verified {
			return true
		}
	}
	return false
}

// TestMachineSmokeJavaIdea2FunctionBreakpoints checks method breakpoints
// (DAP setFunctionBreakpoints). Measured with java-debug 0.53.1
// (SetFunctionBreakpointsRequestHandler splits the name on "#" and compares
// the JDI class name and the method name of each method entry):
//   - the name is `<binary class name>#<method>`: `FuncApp#helper`, a nested
//     class `FuncApp$Nested#run` (the JDI name, so a class in a package is
//     `com.example.Foo#bar`);
//     `FuncApp.helper`, `helper`, `FuncApp::helper` and `FuncApp.Nested#run`
//     never stop (the first three are answered with id 0);
//   - no signature: `FuncApp#helper(int)` / `(I)` never verify, and
//     `FuncApp#helper` stops in every overload;
//   - a name is answered `verified: false` (no message) until its class is
//     loaded, then a `breakpoint` event ("changed", same id) verifies it; a
//     wrong class or method name simply stays unverified, it is no error;
//   - the stop's reason is "function breakpoint".
func TestMachineSmokeJavaIdea2FunctionBreakpoints(t *testing.T) {
	text := "public class FuncApp {\n" +
		"    public static void main(String[] args) {\n" +
		"        int total = helper(1) + helper(\"ab\");\n" +
		"        Later later = new Later();\n" +
		"        System.out.println(\"total \" + total + \" \" + later.work() + \" \" + new Nested().run());\n" +
		"    }\n" +
		"    static int helper(int x) {\n" +
		"        return x * 10;\n" +
		"    }\n" +
		"    static int helper(String s) {\n" +
		"        return s.length();\n" +
		"    }\n" +
		"    static class Nested {\n" +
		"        int run() {\n" +
		"            return 5;\n" +
		"        }\n" +
		"    }\n" +
		"}\n" +
		"class Later {\n" +
		"    int work() {\n" +
		"        return 7;\n" +
		"    }\n" +
		"}\n"
	f := newIdea2Fixture(t, "FuncApp", text)
	start := []FunctionBreakpoint{{Name: "FuncApp#helper"}, {Name: "FuncApp#nope"}, {Name: "Later#work"}, {Name: "FuncApp.helper"}}
	s, capabilities := f.launchWith(t, "function breakpoints", BeginParams{FunctionBreakpoints: start})
	var caps struct {
		SupportsFunctionBreakpoints bool `json:"supportsFunctionBreakpoints"`
	}
	_ = json.Unmarshal(capabilities, &caps)
	if !caps.SupportsFunctionBreakpoints {
		t.Fatalf("capabilities %s, want supportsFunctionBreakpoints", capabilities)
	}

	// The start-time results: one per name, in order; none verified yet (no class is loaded).
	set := f.rec.waitEvent(f.ctx, t, s.ID(), BreakpointsSetEvent)
	var startResults struct {
		Path                string            `json:"path"`
		FunctionBreakpoints []idea2Breakpoint `json:"functionBreakpoints"`
	}
	if err := json.Unmarshal(set.Body, &startResults); err != nil || startResults.Path != "" || len(startResults.FunctionBreakpoints) != len(start) {
		t.Fatalf("start results %s (%v), want %d function breakpoint results without a path", set.Body, err, len(start))
	}
	t.Logf("start results %s", set.Body)
	helperID, laterID := startResults.FunctionBreakpoints[0].ID, startResults.FunctionBreakpoints[2].ID

	expectStop := func(n int, method string) idea2Stop {
		t.Helper()
		stop, ended := f.waitStop(t, s, n)
		if ended {
			t.Fatalf("the program ended, want stop %d in %s", n, method)
		}
		frames := f.frames(t, s, stop.ThreadID)
		if got := idea2MethodName(frames[0].Name); got != method || stop.Reason != "function breakpoint" {
			t.Fatalf("stop %d: reason %q in %+v, want a function breakpoint at the entry of %s", n, stop.Reason, frames[0], method)
		}
		t.Logf("stop %d: reason %q in %s line %d", n, stop.Reason, frames[0].Name, frames[0].Line)
		return stop
	}

	// FuncApp#helper stops in both overloads.
	stop := expectStop(1, "helper")
	if !f.verifiedLater(s, helperID) {
		t.Errorf("no breakpoint event verified FuncApp#helper (id %d) once FuncApp was loaded", helperID)
	}
	smokeRequest(f.ctx, t, s, "continue", map[string]any{"threadId": stop.ThreadID}, nil)
	stop = expectStop(2, "helper")
	if frames := f.frames(t, s, stop.ThreadID); !strings.Contains(frames[0].Name, "String") {
		t.Fatalf("second stop in %s, want helper(String)", frames[0].Name)
	}

	// Later#work verifies when Later is loaded and stops there.
	smokeRequest(f.ctx, t, s, "continue", map[string]any{"threadId": stop.ThreadID}, nil)
	stop = expectStop(3, "work")
	if !f.verifiedLater(s, laterID) {
		t.Errorf("no breakpoint event verified Later#work (id %d) once Later was loaded", laterID)
	}

	// Mid-session: a loaded class verifies at once, wrong names stay unverified (no error).
	var answer struct {
		Breakpoints []idea2Breakpoint `json:"breakpoints"`
	}
	names := []string{"FuncApp#main", "FuncApp$Nested#run", "FuncApp.Nested#run", "NoSuch#foo", "FuncApp#helper(int)"}
	breakpoints := make([]map[string]any, len(names))
	for i, name := range names {
		breakpoints[i] = map[string]any{"name": name}
	}
	smokeRequest(f.ctx, t, s, "setFunctionBreakpoints", map[string]any{"breakpoints": breakpoints}, &answer)
	if len(answer.Breakpoints) != len(names) {
		t.Fatalf("setFunctionBreakpoints answered %+v, want %d results", answer.Breakpoints, len(names))
	}
	for i, result := range answer.Breakpoints {
		t.Logf("setFunctionBreakpoints %q: %+v", names[i], result)
		if want := i == 0; result.Verified != want {
			t.Errorf("%q verified = %v, want %v", names[i], result.Verified, want)
		}
	}
	nestedID, dottedID, wrongID := answer.Breakpoints[1].ID, answer.Breakpoints[2].ID, answer.Breakpoints[3].ID

	// FuncApp$Nested#run stops once Nested is loaded; the dotted and wrong names never verify.
	smokeRequest(f.ctx, t, s, "continue", map[string]any{"threadId": stop.ThreadID}, nil)
	stop = expectStop(4, "run")
	if !f.verifiedLater(s, nestedID) {
		t.Errorf("no breakpoint event verified FuncApp$Nested#run (id %d)", nestedID)
	}
	if f.verifiedLater(s, dottedID) || f.verifiedLater(s, wrongID) {
		t.Errorf("FuncApp.Nested#run (id %d) or NoSuch#foo (id %d) was verified, want both unverified", dottedID, wrongID)
	}

	smokeRequest(f.ctx, t, s, "setFunctionBreakpoints", map[string]any{"breakpoints": []any{}}, nil)
	smokeRequest(f.ctx, t, s, "continue", map[string]any{"threadId": stop.ThreadID}, nil)
	if _, ended := f.waitStop(t, s, 5); !ended {
		t.Fatal("paused again after clearing the method breakpoints, want the program to end")
	}
	if out := f.rec.output(s.ID()); !strings.Contains(out, "total 12 7 5") {
		t.Errorf("program output %q, want total 12 7 5", out)
	}
}

// idea2Variable is one DAP variable.
type idea2Variable struct {
	Name               string `json:"name"`
	Value              string `json:"value"`
	VariablesReference int    `json:"variablesReference"`
}

// variables returns the children of reference.
func (f *idea2Fixture) variables(t *testing.T, s *Session, reference int) []idea2Variable {
	t.Helper()
	var body struct {
		Variables []idea2Variable `json:"variables"`
	}
	smokeRequest(f.ctx, t, s, "variables", map[string]any{"variablesReference": reference}, &body)
	return body.Variables
}

// locals returns the variables of the first scope (Local) of frameID.
func (f *idea2Fixture) locals(t *testing.T, s *Session, frameID int) (int, []idea2Variable) {
	t.Helper()
	var scopes struct {
		Scopes []struct {
			Name               string `json:"name"`
			VariablesReference int    `json:"variablesReference"`
		} `json:"scopes"`
	}
	smokeRequest(f.ctx, t, s, "scopes", map[string]any{"frameId": frameID}, &scopes)
	if len(scopes.Scopes) == 0 {
		t.Fatalf("frame %d has no scopes", frameID)
	}
	return scopes.Scopes[0].VariablesReference, f.variables(t, s, scopes.Scopes[0].VariablesReference)
}

// idea2DataInfo is a dataBreakpointInfo response.
type idea2DataInfo struct {
	DataID      *string  `json:"dataId"`
	Description string   `json:"description"`
	AccessTypes []string `json:"accessTypes"`
	CanPersist  *bool    `json:"canPersist"`
}

func (f *idea2Fixture) dataInfo(t *testing.T, s *Session, reference int, name string) (idea2DataInfo, json.RawMessage) {
	t.Helper()
	body, err := s.Request(f.ctx, "dataBreakpointInfo", map[string]any{"variablesReference": reference, "name": name})
	if err != nil {
		t.Fatalf("dataBreakpointInfo %s: %v", name, err)
	}
	var info idea2DataInfo
	if len(body) == 0 || string(body) == "null" {
		return info, body
	}
	if err := json.Unmarshal(body, &info); err != nil {
		t.Fatalf("decode dataBreakpointInfo %s: %v (%s)", name, err, body)
	}
	return info, body
}

// TestMachineSmokeJavaIdea2DataBreakpoints checks field breakpoints (DAP
// dataBreakpointInfo / setDataBreakpoints). Measured with java-debug 0.53.1:
//   - dataBreakpointInfo on a field of an object (variablesReference of the
//     object, name of the field) answers
//     {"dataId":"Counter#count","description":"Counter.count : int",
//     "accessTypes":["read","write","readWrite"],"canPersist":true}: the id
//     is `<JDI class name>#<field>`, so it is the same in every session and
//     always persistable; a static field of the class (`total`, not listed
//     among the object's children) answers the same way;
//   - a local variable, the object itself (a scope's variable) and an unknown
//     field get a response without a body (no dataId);
//   - setDataBreakpoints answers {id, verified: true} for a loaded class;
//     write stops at the assignment, read at each read (also `count + 1`
//     inside a method), and the stop's reason is "data breakpoint";
//   - sent at start (before any class is loaded) every one is answered
//     verified: false; a `breakpoint` event (reason "new", same id,
//     verified) follows when the class loads, and a saved id stops the next
//     session as well; an unknown class or field stays unverified, an id
//     without "#" gets id 0. None of these is an error.
func TestMachineSmokeJavaIdea2DataBreakpoints(t *testing.T) {
	text := "public class DataApp {\n" +
		"    public static void main(String[] args) {\n" +
		"        Counter c = new Counter();\n" +
		"        int local = 1;\n" +
		"        c.count = 5;\n" +
		"        int seen = c.count + local;\n" +
		"        c.bump();\n" +
		"        System.out.println(\"seen \" + seen + \" \" + c.count);\n" +
		"    }\n" +
		"}\n" +
		"class Counter {\n" +
		"    int count;\n" +
		"    static int total;\n" +
		"    void bump() {\n" +
		"        count = count + 1;\n" +
		"    }\n" +
		"}\n"
	const localLine, writeLine, readLine, bumpLine = 4, 5, 6, 15
	f := newIdea2Fixture(t, "DataApp", text)
	s, capabilities := f.launch(t, "data breakpoints", map[string][]SourceBreakpoint{f.source: {{Line: localLine}}})
	var caps struct {
		SupportsDataBreakpoints bool `json:"supportsDataBreakpoints"`
	}
	_ = json.Unmarshal(capabilities, &caps)
	if !caps.SupportsDataBreakpoints {
		t.Fatalf("capabilities %s, want supportsDataBreakpoints", capabilities)
	}
	stop, ended := f.waitStop(t, s, 1)
	if ended {
		t.Fatal("the program ended before the line breakpoint")
	}
	frames := f.frames(t, s, stop.ThreadID)
	localsRef, locals := f.locals(t, s, frames[0].ID)
	counterRef := 0
	for _, v := range locals {
		if v.Name == "c" {
			counterRef = v.VariablesReference
		}
	}
	if counterRef == 0 {
		t.Fatalf("locals %+v, want the object c", locals)
	}

	info, raw := f.dataInfo(t, s, counterRef, "count")
	t.Logf("dataBreakpointInfo c.count: %s", raw)
	if info.DataID == nil || *info.DataID != "Counter#count" || info.Description != "Counter.count : int" ||
		strings.Join(info.AccessTypes, ",") != "read,write,readWrite" || info.CanPersist == nil || !*info.CanPersist {
		t.Fatalf("dataBreakpointInfo c.count = %s, want Counter#count, all access types, canPersist", raw)
	}
	if static, raw := f.dataInfo(t, s, counterRef, "total"); static.DataID == nil || *static.DataID != "Counter#total" {
		t.Errorf("dataBreakpointInfo c.total = %s, want Counter#total", raw)
	}
	for _, probe := range []struct {
		reference int
		name      string
	}{{counterRef, "nope"}, {localsRef, "local"}, {localsRef, "c"}} {
		if none, raw := f.dataInfo(t, s, probe.reference, probe.name); none.DataID != nil {
			t.Errorf("dataBreakpointInfo %s = %s, want no dataId", probe.name, raw)
		}
	}

	setData := func(accessType string) {
		t.Helper()
		var answer struct {
			Breakpoints []idea2Breakpoint `json:"breakpoints"`
		}
		smokeRequest(f.ctx, t, s, "setDataBreakpoints", map[string]any{"breakpoints": []any{map[string]any{"dataId": *info.DataID, "accessType": accessType}}}, &answer)
		if len(answer.Breakpoints) != 1 || !answer.Breakpoints[0].Verified {
			t.Fatalf("setDataBreakpoints %s answered %+v, want one verified", accessType, answer.Breakpoints)
		}
	}
	expectStop := func(n, line int, what string) idea2Stop {
		t.Helper()
		smokeRequest(f.ctx, t, s, "continue", map[string]any{"threadId": stop.ThreadID}, nil)
		next, ended := f.waitStop(t, s, n)
		if ended {
			t.Fatalf("the program ended, want the %s at line %d", what, line)
		}
		top := f.frames(t, s, next.ThreadID)[0]
		if next.Reason != "data breakpoint" || top.Line != line {
			t.Fatalf("stop %d: reason %q at %s:%d, want the %s (data breakpoint) at line %d", n, next.Reason, top.Name, top.Line, what, line)
		}
		t.Logf("stop %d: reason %q at %s:%d", n, next.Reason, top.Name, top.Line)
		return next
	}

	setData("write")
	stop = expectStop(2, writeLine, "write")
	if got := f.evaluate(t, s, f.frames(t, s, stop.ThreadID)[0].ID, "c.count"); got != "0" {
		t.Errorf("c.count = %q at the write stop, want 0 (it stops before the write)", got)
	}
	setData("read")
	stop = expectStop(3, readLine, "read")
	stop = expectStop(4, bumpLine, "read inside bump")
	smokeRequest(f.ctx, t, s, "setDataBreakpoints", map[string]any{"breakpoints": []any{}}, nil)
	smokeRequest(f.ctx, t, s, "continue", map[string]any{"threadId": stop.ThreadID}, nil)
	if _, ended := f.waitStop(t, s, 5); !ended {
		t.Fatal("paused again after clearing the field breakpoints, want the program to end")
	}
	if out := f.rec.output(s.ID()); !strings.Contains(out, "seen 6 6") {
		t.Errorf("program output %q, want seen 6 6", out)
	}

	// The saved id at the start of the next session: unverified until Counter loads, then it stops.
	start := []DataBreakpoint{{DataID: "Counter#count", AccessType: "write"}, {DataID: "Nope#x", AccessType: "write"}, {DataID: "Counter#nope", AccessType: "read"}, {DataID: "garbage", AccessType: "write"}}
	next, _ := f.launchWith(t, "data breakpoints at start", BeginParams{DataBreakpoints: start})
	set := f.rec.waitEvent(f.ctx, t, next.ID(), BreakpointsSetEvent)
	var startResults struct {
		Path            string            `json:"path"`
		DataBreakpoints []idea2Breakpoint `json:"dataBreakpoints"`
	}
	if err := json.Unmarshal(set.Body, &startResults); err != nil || startResults.Path != "" || len(startResults.DataBreakpoints) != len(start) {
		t.Fatalf("start results %s (%v), want %d data breakpoint results without a path", set.Body, err, len(start))
	}
	t.Logf("start results %s", set.Body)
	for i, result := range startResults.DataBreakpoints {
		if result.Verified {
			t.Errorf("%s verified at start, want unverified (no class is loaded)", start[i].DataID)
		}
	}
	first, ended := f.waitStop(t, next, 1)
	if ended {
		t.Fatal("the next session ended, want the saved write breakpoint to stop it")
	}
	if top := f.frames(t, next, first.ThreadID)[0]; first.Reason != "data breakpoint" || top.Line != writeLine {
		t.Fatalf("next session stopped with %q at line %d, want a data breakpoint at line %d", first.Reason, top.Line, writeLine)
	}
	if id := startResults.DataBreakpoints[0].ID; !f.verifiedLater(next, id) {
		t.Errorf("no breakpoint event verified Counter#count (id %d) once Counter was loaded", id)
	}
	for _, i := range []int{1, 2} {
		if id := startResults.DataBreakpoints[i].ID; f.verifiedLater(next, id) {
			t.Errorf("%s (id %d) was verified, want it unverified", start[i].DataID, id)
		}
	}
}

// jdtlsSave writes text to source and tells root's jdtls like the editor
// does on save: textDocument/didChange (full text) then didSave.
func jdtlsSave(t *testing.T, root, source, text string, version int) {
	t.Helper()
	if err := os.WriteFile(source, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	// project_lsp_start returns the running server's status (its generation).
	out, err := lspDispatch(t, "project_lsp_start", map[string]any{"root": root, "serverId": "jdtls"})
	if err != nil {
		t.Fatalf("project_lsp_start: %v", err)
	}
	var status struct {
		Generation uint64 `json:"generation"`
	}
	if err := json.Unmarshal([]byte(out), &status); err != nil || status.Generation == 0 {
		t.Fatalf("project_lsp_start = %s (%v)", out, err)
	}
	uri := fileURL(source)
	for _, note := range []struct {
		method string
		params any
	}{
		{"textDocument/didChange", map[string]any{
			"textDocument":   map[string]any{"uri": uri, "version": version},
			"contentChanges": []any{map[string]any{"text": text}},
		}},
		{"textDocument/didSave", map[string]any{"textDocument": map[string]any{"uri": uri}, "text": text}},
	} {
		if _, err := lspDispatch(t, "project_lsp_notify", map[string]any{"root": root, "serverId": "jdtls", "generation": status.Generation, "method": note.method, "params": note.params}); err != nil {
			t.Fatalf("jdtls %s: %v", note.method, err)
		}
	}
}

// idea2HotCodeReplace is the body of java-debug's hotcodereplace event.
type idea2HotCodeReplace struct {
	ChangeType string `json:"changeType"`
	Message    string `json:"message"`
}

// hotCodeReplaceEvents returns the hotcodereplace events of s so far.
func (f *idea2Fixture) hotCodeReplaceEvents(s *Session) []idea2HotCodeReplace {
	f.rec.mu.Lock()
	defer f.rec.mu.Unlock()
	var out []idea2HotCodeReplace
	for _, ev := range f.rec.events {
		if ev.SessionID != s.ID() || ev.Event != "hotcodereplace" {
			continue
		}
		var body idea2HotCodeReplace
		_ = json.Unmarshal(ev.Body, &body)
		out = append(out, body)
	}
	return out
}

// waitHotCodeReplace waits for the n-th (1-based) hotcodereplace event of
// s and returns it.
func (f *idea2Fixture) waitHotCodeReplace(t *testing.T, s *Session, n int) idea2HotCodeReplace {
	t.Helper()
	waitCtx, cancel := context.WithTimeout(f.ctx, smokePauseTimeout)
	defer cancel()
	ev, ended := f.rec.waitNthEvent(waitCtx, t, s.ID(), "hotcodereplace", n)
	if ended {
		t.Fatalf("the session ended before hotcodereplace event %d", n)
	}
	var body idea2HotCodeReplace
	if err := json.Unmarshal(ev.Body, &body); err != nil {
		t.Fatalf("hotcodereplace body %s: %v", ev.Body, err)
	}
	return body
}

// redefineResult is the body of java-debug's redefineClasses response.
type redefineResult struct {
	ChangedClasses []string `json:"changedClasses"`
	ErrorMessage   string   `json:"errorMessage"`
}

// redefine saves text (as the editor does: jdtls compiles on didSave), waits
// for the BUILD_COMPLETE event of s (the n-th hotcodereplace event) and sends
// redefineClasses like the frontend does in auto mode. It returns the
// response, then the hotcodereplace events that followed it.
func (f *idea2Fixture) redefine(t *testing.T, s *Session, text string, version, n int) (redefineResult, []idea2HotCodeReplace) {
	t.Helper()
	jdtlsSave(t, f.root, f.source, text, version)
	if built := f.waitHotCodeReplace(t, s, n); built != (idea2HotCodeReplace{ChangeType: "BUILD_COMPLETE", Message: "Build completed."}) {
		t.Fatalf("hotcodereplace event %d = %+v, want BUILD_COMPLETE \"Build completed.\"", n, built)
	}
	body, err := s.Request(f.ctx, "redefineClasses", map[string]any{})
	if err != nil {
		t.Fatalf("redefineClasses: %v", err)
	}
	t.Logf("redefineClasses: %s", body)
	var result redefineResult
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode redefineClasses %s: %v", body, err)
	}
	// STARTING … END (END is the last event of the procedure).
	for i := n + 1; ; i++ {
		if f.waitHotCodeReplace(t, s, i).ChangeType == "END" {
			return result, f.hotCodeReplaceEvents(s)[n:]
		}
	}
}

// TestMachineSmokeJavaIdea2HotCodeReplace checks hot code replace (java-debug's
// custom event hotcodereplace and request redefineClasses; java-debug
// 0.53.1, measured here):
//
//   - Saving a source (didChange + didSave, as the editor does) makes jdtls
//     build and every listening session sends hotcodereplace
//     {"changeType":"BUILD_COMPLETE","message":"Build completed."}, in auto
//     and manual mode alike (java-debug does not tell them apart).
//   - redefineClasses (no arguments) answers {"changedClasses":["HotApp"]}
//     and sends STARTING "Start hot code replacement procedure...", then
//     END "Completed hot code replace"; the program runs the new code.
//   - An unsupported change (a method added) answers success with
//     {"changedClasses":[],"errorMessage":"Failed to redefine classes: Add
//     method not implemented"} and sends STARTING, ERROR "Add method not
//     implemented", END; the session goes on and the program keeps the old code.
//   - Paused inside a changed method, java-debug drops that frame itself: a
//     continued then a stopped event (reason "step") at the method's first line.
//   - hotCodeReplace "never" (sent lowercase, java-debug's @SerializedName)
//     at a session's start: that session sends no event.
func TestMachineSmokeJavaIdea2HotCodeReplace(t *testing.T) {
	text := "public class HotApp {\n" +
		"    public static void main(String[] args) {\n" +
		"        for (int i = 0; i < 6; i++) {\n" +
		"            int v = value();\n" +
		"            System.out.println(\"value \" + v);\n" +
		"        }\n" +
		"    }\n" +
		"    static int value() {\n" +
		"        int base = 10;\n" +
		"        return base + 1;\n" +
		"    }\n" +
		"}\n"
	const printLine, valueFirstLine, valueReturnLine = 5, 9, 10
	f := newIdea2Fixture(t, "HotApp", text)
	settings := func(mode string) {
		t.Helper()
		var ignored json.RawMessage
		jdtlsCommand(f.ctx, t, f.root, "vscode.java.updateDebugSettings", []any{`{"logLevel":"WARN","showLogicalStructure":true,"showToString":true,"hotCodeReplace":"` + mode + `","exceptionFilters":{"exceptionTypes":[]}}`}, &ignored)
	}
	settings("auto")
	s, _ := f.launch(t, "hot code replace", map[string][]SourceBreakpoint{f.source: {{Line: printLine}}})
	settings("never")
	quiet, _ := f.launch(t, "hot code replace never", map[string][]SourceBreakpoint{f.source: {{Line: printLine}}})
	settings("auto")
	stopAt := func(s *Session, n int) (idea2Stop, []idea2Frame) {
		t.Helper()
		stop, ended := f.waitStop(t, s, n)
		if ended {
			t.Fatalf("the program ended, want stop %d", n)
		}
		return stop, f.frames(t, s, stop.ThreadID)
	}
	stop, frames := stopAt(s, 1)
	if got := f.evaluate(t, s, frames[0].ID, "v"); frames[0].Line != printLine || got != "11" {
		t.Fatalf("first stop at line %d with v = %q, want line %d and 11", frames[0].Line, got, printLine)
	}
	stopAt(quiet, 1)

	// A method body change: replaced, the next call runs the new code.
	changed := strings.Replace(text, "return base + 1;", "return base + 2;", 1)
	result, events := f.redefine(t, s, changed, 2, 1)
	if strings.Join(result.ChangedClasses, ",") != "HotApp" || result.ErrorMessage != "" {
		t.Fatalf("redefineClasses = %+v, want changedClasses [HotApp]", result)
	}
	if len(events) != 2 || events[0] != (idea2HotCodeReplace{"STARTING", "Start hot code replacement procedure..."}) ||
		events[1] != (idea2HotCodeReplace{"END", "Completed hot code replace"}) {
		t.Fatalf("events after redefineClasses %+v, want STARTING, END", events)
	}
	smokeRequest(f.ctx, t, s, "continue", map[string]any{"threadId": stop.ThreadID}, nil)
	stop, frames = stopAt(s, 2)
	if got := f.evaluate(t, s, frames[0].ID, "v"); got != "12" {
		t.Fatalf("v = %q after the replace, want 12 (the new code)", got)
	}
	// The session started with "never" heard nothing.
	if got := f.hotCodeReplaceEvents(quiet); len(got) != 0 {
		t.Errorf("the never session got hotcodereplace events %+v", got)
	}
	smokeRequest(f.ctx, t, quiet, "disconnect", map[string]any{"terminateDebuggee": true}, nil)

	// An added method: refused with java-debug's message, the session goes on with the old code.
	added := strings.Replace(changed, "    static int value() {\n", "    static int extra() {\n        return 3;\n    }\n    static int value() {\n", 1)
	built := len(f.hotCodeReplaceEvents(s))
	result, events = f.redefine(t, s, added, 3, built+1)
	if len(result.ChangedClasses) != 0 || result.ErrorMessage != "Failed to redefine classes: Add method not implemented" {
		t.Fatalf("redefineClasses with an added method = %+v, want no class and the failure", result)
	}
	if len(events) != 3 || events[1] != (idea2HotCodeReplace{"ERROR", "Add method not implemented"}) {
		t.Fatalf("events after the refused redefineClasses %+v, want STARTING, ERROR, END", events)
	}
	smokeRequest(f.ctx, t, s, "continue", map[string]any{"threadId": stop.ThreadID}, nil)
	stop, frames = stopAt(s, 3)
	if got := f.evaluate(t, s, frames[0].ID, "v"); got != "12" {
		t.Fatalf("v = %q after the refused replace, want 12 (the code before it)", got)
	}

	// Back to the loaded shape, then a change while paused in the changed
	// method: java-debug drops its frame and stops at its first line.
	built = len(f.hotCodeReplaceEvents(s))
	if result, _ = f.redefine(t, s, changed, 4, built+1); result.ErrorMessage != "" {
		t.Fatalf("redefineClasses back to the loaded shape = %+v", result)
	}
	var answer struct {
		Breakpoints []idea2Breakpoint `json:"breakpoints"`
	}
	smokeRequest(f.ctx, t, s, "setBreakpoints", map[string]any{"source": map[string]any{"path": f.source}, "breakpoints": []any{map[string]any{"line": valueReturnLine}}}, &answer)
	smokeRequest(f.ctx, t, s, "continue", map[string]any{"threadId": stop.ThreadID}, nil)
	stop, frames = stopAt(s, 4)
	if frames[0].Line != valueReturnLine || !strings.Contains(frames[0].Name, "value") {
		t.Fatalf("paused at %+v, want value():%d", frames[0], valueReturnLine)
	}
	built = len(f.hotCodeReplaceEvents(s))
	if result, _ = f.redefine(t, s, strings.Replace(text, "return base + 1;", "return base + 3;", 1), 5, built+1); strings.Join(result.ChangedClasses, ",") != "HotApp" {
		t.Fatalf("redefineClasses while paused in value() = %+v", result)
	}
	dropped, frames := stopAt(s, 5)
	if dropped.Reason != "step" || frames[0].Line != valueFirstLine || !strings.Contains(frames[0].Name, "value") {
		t.Fatalf("after the replace: stop %+v at %+v, want java-debug to drop value() and stop at line %d (reason step)", dropped, frames[0], valueFirstLine)
	}
	f.rec.mu.Lock()
	continued := 0
	for _, ev := range f.rec.events {
		if ev.SessionID == s.ID() && ev.Event == "continued" {
			continued++
		}
	}
	f.rec.mu.Unlock()
	if continued == 0 {
		t.Errorf("no continued event before java-debug's own stop")
	}
	smokeRequest(f.ctx, t, s, "setBreakpoints", map[string]any{"source": map[string]any{"path": f.source}, "breakpoints": []any{map[string]any{"line": printLine}}}, &answer)
	smokeRequest(f.ctx, t, s, "continue", map[string]any{"threadId": dropped.ThreadID}, nil)
	_, frames = stopAt(s, 6)
	if got := f.evaluate(t, s, frames[0].ID, "v"); got != "13" {
		t.Fatalf("v = %q after the replace in value(), want 13", got)
	}
	t.Logf("program output %q", f.rec.output(s.ID()))
}
