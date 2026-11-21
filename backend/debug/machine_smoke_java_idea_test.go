package debug

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"mincode/app/backend/lsp"
)

// TestMachineSmokeJavaIdea checks "pause only on these exception types"
// against a real jdtls with the java-debug bundle (same opt-in environment as
// TestMachineSmokeJava). The plugin process is not part of this harness, so
// the test sends vscode.java.updateDebugSettings through the jdtls session
// exactly as the java-debug plugin's debug.applySettings does (one JSON
// string argument), before each launch like the host does right before start.
//
// The program throws and catches an IllegalStateException, then a
// NullPointerException. With "caught" exceptions on:
//   - list [java.lang.NullPointerException]: the first pause is the NPE, and
//     exceptionInfo names it;
//   - list [] (cleared, new session): the first pause is the
//     IllegalStateException;
//   - list [java.lang.RuntimeException]: subclasses match too;
//   - list [java.lang.NullPointerException] with "caught" off (uncaught
//     only, or no exception filter): no exception pause, the program reaches
//     the breakpoint on the line after the caught NPE.
//
// It also records how a running session takes a changed list (the finding
// the sessions store relies on): java-debug 0.53.1 keeps a session's
// exception breakpoints after updateDebugSettings (its settings listener
// re-applies them only when the settings carry exceptionFiltersUpdated,
// which the plugin does not send), and applies the new list when the
// session's exception breakpoints are set again (DAP setExceptionBreakpoints).
func TestMachineSmokeJavaIdea(t *testing.T) {
	javaExec, jar := smokeJava(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("JAVA_HOME", "")
	root := t.TempDir()
	source := filepath.Join(root, "src", "ExcApp.java")
	text := "public class ExcApp {\n" +
		"    public static void main(String[] args) {\n" +
		"        int ready = 1;\n" +
		"        try { throw new IllegalStateException(\"first\"); } catch (IllegalStateException e) { System.out.println(\"caught \" + e.getMessage()); }\n" +
		"        try { Object o = null; o.hashCode(); } catch (NullPointerException e) { System.out.println(\"caught npe\"); }\n" +
		"        System.out.println(\"done \" + ready);\n" +
		"    }\n" +
		"}\n"
	const readyLine, doneLine = 3, 6
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	source = filepath.Join(root, "src", "ExcApp.java")
	ctx, cancel := context.WithTimeout(context.Background(), javaSmokeTimeout)
	defer cancel()
	rec := captureEmits(t)
	t.Cleanup(func() {
		if t.Failed() {
			rec.logAll(t)
		}
	})
	t.Cleanup(func() { lsp.Shutdown(context.Background()) })
	lsp.SetJdtlsBundleProvider(func() []string { return []string{jar} })
	t.Cleanup(func() { lsp.SetJdtlsBundleProvider(nil) })
	stop := startJdtls(ctx, t, root, source, text)
	defer stop()
	var classpaths [2][]string
	jdtlsCommand(ctx, t, root, "vscode.java.resolveClasspath", []any{"ExcApp", ""}, &classpaths)
	args, _ := json.Marshal(map[string]any{"type": "java", "mainClass": "ExcApp", "classPaths": classpaths[1], "modulePaths": nonNil(classpaths[0]), "javaExec": javaExec, "cwd": root, "console": "internalConsole"})

	// updateSettings is what debug.applySettings sends.
	updateSettings := func(types []string) {
		t.Helper()
		list, err := json.Marshal(types)
		if err != nil {
			t.Fatal(err)
		}
		settings := `{"logLevel":"WARN","showLogicalStructure":true,"showToString":true,"exceptionFilters":{"exceptionTypes":` + string(list) + `}}`
		var ignored json.RawMessage
		jdtlsCommand(ctx, t, root, "vscode.java.updateDebugSettings", []any{settings}, &ignored)
	}
	launch := func(name string, breakpoints map[string][]SourceBreakpoint, filters ...string) *Session {
		t.Helper()
		var port int
		jdtlsCommand(ctx, t, root, "vscode.java.startDebugSession", []any{}, &port)
		s, err := ConnectAdapter(ctx, ConnectSpec{Port: port, OwnerPluginID: "machine-smoke", DebuggerType: "java", ProjectRoot: root})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close(name + " cleanup") })
		if filters == nil {
			filters = []string{"caught"}
		}
		if _, err := s.Begin(ctx, BeginParams{AdapterID: "java", Request: "launch", Arguments: args, Breakpoints: breakpoints, ExceptionFilters: filters}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return s
	}
	// nextPause continues from pause (when > 0) and returns the following
	// pause's reason, thread and, for an exception, the exceptionInfo id;
	// reason "" when the program ended instead.
	type pause struct {
		reason    string
		thread    int
		exception string
	}
	nextPause := func(s *Session, previous pause, count int) pause {
		t.Helper()
		if previous.thread != 0 {
			smokeRequest(ctx, t, s, "continue", map[string]any{"threadId": previous.thread}, nil)
		}
		// Bounded, so a lost event fails with the recorded events instead
		// of waiting for the whole smoke timeout.
		waitCtx, cancelWait := context.WithTimeout(ctx, smokePauseTimeout)
		defer cancelWait()
		ev, ended := rec.waitNthEvent(waitCtx, t, s.ID(), "stopped", count)
		if ended {
			return pause{}
		}
		var body struct {
			Reason   string `json:"reason"`
			ThreadID int    `json:"threadId"`
		}
		_ = json.Unmarshal(ev.Body, &body)
		p := pause{reason: body.Reason, thread: body.ThreadID}
		if body.Reason == "exception" {
			var info struct {
				ExceptionID string `json:"exceptionId"`
				Description string `json:"description"`
			}
			smokeRequest(ctx, t, s, "exceptionInfo", map[string]any{"threadId": body.ThreadID}, &info)
			p.exception = info.ExceptionID
			t.Logf("pause %d: exception %s (%s)", count, info.ExceptionID, info.Description)
		} else {
			t.Logf("pause %d: %s", count, body.Reason)
		}
		return p
	}
	finish := func(s *Session, last pause, count int) {
		t.Helper()
		if p := nextPause(s, last, count); p.reason != "" {
			t.Fatalf("pause %d %+v, want the program to end", count, p)
		}
	}

	// The list holds the NPE only: the IllegalStateException is not a pause.
	updateSettings([]string{"java.lang.NullPointerException"})
	s := launch("npe only", nil)
	p := nextPause(s, pause{}, 1)
	if p.reason != "exception" || p.exception != "java.lang.NullPointerException" {
		t.Fatalf("first pause %+v, want the NullPointerException", p)
	}
	finish(s, p, 2)

	// Cleared (an explicit []), a restarted session pauses on every caught exception.
	updateSettings([]string{})
	s = launch("cleared", nil)
	p = nextPause(s, pause{}, 1)
	if p.reason != "exception" || p.exception != "java.lang.IllegalStateException" {
		t.Fatalf("first pause %+v, want the IllegalStateException", p)
	}
	p = nextPause(s, p, 2)
	if p.exception != "java.lang.NullPointerException" {
		t.Fatalf("second pause %+v, want the NullPointerException", p)
	}
	finish(s, p, 3)

	// A listed type matches its subclasses.
	updateSettings([]string{"java.lang.RuntimeException"})
	s = launch("superclass", nil)
	p = nextPause(s, pause{}, 1)
	if p.exception != "java.lang.IllegalStateException" {
		t.Fatalf("first pause %+v, want the IllegalStateException (a RuntimeException)", p)
	}
	smokeRequest(ctx, t, s, "disconnect", map[string]any{"terminateDebuggee": true}, nil)

	// "Caught" off with the list set (uncaught only, then no exception
	// breakpoints at all): the caught NPE is no pause; the program reaches
	// the breakpoint after it.
	updateSettings([]string{"java.lang.NullPointerException"})
	for _, filters := range [][]string{{"uncaught"}, {}} {
		s = launch(fmt.Sprintf("caught off %v", filters), map[string][]SourceBreakpoint{source: {{Line: doneLine}}}, filters...)
		p = nextPause(s, pause{}, 1)
		if p.reason != "breakpoint" {
			t.Fatalf("filters %v: first pause %+v, want the breakpoint after the caught NullPointerException", filters, p)
		}
		var trace struct {
			StackFrames []struct {
				Line int `json:"line"`
			} `json:"stackFrames"`
		}
		smokeRequest(ctx, t, s, "stackTrace", map[string]any{"threadId": p.thread}, &trace)
		if len(trace.StackFrames) == 0 || trace.StackFrames[0].Line != doneLine {
			t.Fatalf("filters %v: paused at %+v, want line %d", filters, trace.StackFrames, doneLine)
		}
		finish(s, p, 2)
	}

	// Changing the list while a session runs: not applied by the update alone.
	updateSettings([]string{"java.lang.ArithmeticException"})
	s = launch("changed, not re-sent", map[string][]SourceBreakpoint{source: {{Line: readyLine}}})
	p = nextPause(s, pause{}, 1)
	if p.reason != "breakpoint" {
		t.Fatalf("first pause %+v, want the breakpoint", p)
	}
	updateSettings([]string{"java.lang.NullPointerException"})
	finish(s, p, 2)
	t.Log("finding: a running session ignores updateDebugSettings until its exception breakpoints are set again")

	// The same change followed by setExceptionBreakpoints (what the sessions store does) applies.
	updateSettings([]string{"java.lang.ArithmeticException"})
	s = launch("changed, re-sent", map[string][]SourceBreakpoint{source: {{Line: readyLine}}})
	p = nextPause(s, pause{}, 1)
	if p.reason != "breakpoint" {
		t.Fatalf("first pause %+v, want the breakpoint", p)
	}
	updateSettings([]string{"java.lang.NullPointerException"})
	smokeRequest(ctx, t, s, "setExceptionBreakpoints", map[string]any{"filters": []string{"caught"}}, nil)
	p = nextPause(s, p, 2)
	if p.reason != "exception" || p.exception != "java.lang.NullPointerException" {
		t.Fatalf("pause after re-sending %+v, want the NullPointerException", p)
	}
	finish(s, p, 3)
	updateSettings([]string{})
}

// waitNthEvent waits until session id emitted its n-th (1-based) name event
// and returns it, or reports ended when the session ended first.
func (r *emitRecorder) waitNthEvent(ctx context.Context, t *testing.T, id, name string, n int) (Event, bool) {
	t.Helper()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		r.mu.Lock()
		seen, ended := 0, false
		for _, ev := range r.events {
			if ev.SessionID != id {
				continue
			}
			if ev.Event == name {
				if seen++; seen == n {
					r.mu.Unlock()
					return ev, false
				}
			}
			ended = ended || ev.Event == SessionEndedEvent
		}
		r.mu.Unlock()
		if ended {
			return Event{}, true
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			t.Fatalf("no %q event %d for %s: %v", name, n, id, ctx.Err())
			return Event{}, false
		}
	}
}

// smokePauseTimeout bounds one wait for a pause or the end of a program.
const smokePauseTimeout = 90 * time.Second

// smokeStepFilters is arguments.stepFilters as the java-debug plugin's
// debug.resolve sends it with the default settings.
var smokeStepFilters = map[string]any{
	"skipClasses":            []string{"java.*", "javax.*", "jdk.*", "sun.*", "com.sun.*", "org.springframework.*", "org.apache.tomcat.*", "org.apache.catalina.*", "org.apache.coyote.*", "*$$*"},
	"skipSynthetics":         true,
	"skipStaticInitializers": true,
	"skipConstructors":       false,
}

// smokeVariable is one DAP variable with its presentation hint.
type smokeVariable struct {
	Name               string `json:"name"`
	Value              string `json:"value"`
	Type               string `json:"type"`
	VariablesReference int    `json:"variablesReference"`
	PresentationHint   *struct {
		Lazy bool `json:"lazy"`
	} `json:"presentationHint"`
}

func (v smokeVariable) lazy() bool { return v.PresentationHint != nil && v.PresentationHint.Lazy }

// TestMachineSmokeJavaIdeaStepFilters checks Step Into with the plugin's
// default step filters and the readable boxed values / Map entries of the
// patched java-debug jar (scripts/value-format-patch.mjs) against a real
// jdtls (same opt-in environment as TestMachineSmokeJava; the jar must be
// the one fetch-assets.mjs puts in lib/).
//
// At `total = twice(list.size())`, F7 (DAP stepIn) with the step filters
// lands in Spk.twice: ArrayList.size() is skipped. Without them it lands in
// java.util.ArrayList.
//
// Values, in main's frame, with the settings debug.applySettings sends: the
// List<Integer> element is `1` and the Map entry `"a" -> 1`, in the first
// variables answer and not lazy (upstream: lazy `Integer@<id>` resolving to
// `Integer@<id> "1"` only on a further request). Boxed locals show their
// primitive formatted like that primitive (a Double like a double, hex with
// DAP format.hex); a null boxed local stays `null`;
// a class with its own toString() keeps upstream's lazy `Type@<id>`.
func TestMachineSmokeJavaIdeaStepFilters(t *testing.T) {
	javaExec, jar := smokeJava(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("JAVA_HOME", "")
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "src", "Spk.java")
	text := "import java.util.*;\n" +
		"public class Spk {\n" +
		"    static int twice(int n) { return n * 2; }\n" +
		"    static class Point { int x = 3; public String toString() { return \"P\" + x; } }\n" +
		"    public static void main(String[] args) {\n" +
		"        List<Integer> list = new ArrayList<>(List.of(1, 2, 3));\n" +
		"        Map<String, Integer> map = new HashMap<>(); map.put(\"a\", 1);\n" +
		"        Long big = 5L; Character ch = 'x'; Boolean flag = true; Double d = 2.5; Integer none = null; Point point = new Point(); double pd = 2.5;\n" +
		"        int total = 0;\n" +
		"        total = twice(list.size());\n" +
		"        System.out.println(total + \" \" + map + big + ch + flag + d + none + point + pd);\n" +
		"    }\n" +
		"}\n"
	const callLine, twiceLine = 10, 3
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), javaSmokeTimeout)
	defer cancel()
	rec := captureEmits(t)
	t.Cleanup(func() {
		if t.Failed() {
			rec.logAll(t)
		}
	})
	t.Cleanup(func() { lsp.Shutdown(context.Background()) })
	lsp.SetJdtlsBundleProvider(func() []string { return []string{jar} })
	t.Cleanup(func() { lsp.SetJdtlsBundleProvider(nil) })
	stop := startJdtls(ctx, t, root, source, text)
	defer stop()
	var classpaths [2][]string
	jdtlsCommand(ctx, t, root, "vscode.java.resolveClasspath", []any{"Spk", ""}, &classpaths)
	// The settings debug.applySettings sends (no exception types).
	var ignored json.RawMessage
	jdtlsCommand(ctx, t, root, "vscode.java.updateDebugSettings", []any{`{"logLevel":"WARN","showLogicalStructure":true,"showToString":true,"exceptionFilters":{"exceptionTypes":[]}}`}, &ignored)

	type frame struct {
		ID     int    `json:"id"`
		Name   string `json:"name"`
		Line   int    `json:"line"`
		Source struct {
			Name string `json:"name"`
			Path string `json:"path"`
		} `json:"source"`
	}
	topFrame := func(s *Session, thread int) frame {
		t.Helper()
		var trace struct {
			StackFrames []frame `json:"stackFrames"`
		}
		smokeRequest(ctx, t, s, "stackTrace", map[string]any{"threadId": thread, "startFrame": 0, "levels": 20}, &trace)
		if len(trace.StackFrames) == 0 {
			t.Fatal("empty stack trace")
		}
		return trace.StackFrames[0]
	}
	variables := func(s *Session, ref int) []smokeVariable {
		t.Helper()
		var body struct {
			Variables []smokeVariable `json:"variables"`
		}
		smokeRequest(ctx, t, s, "variables", map[string]any{"variablesReference": ref}, &body)
		return body.Variables
	}
	// stepInFromCall launches, pauses at the call line, steps in and
	// returns the session, the thread and the new top frame.
	stepInFromCall := func(name string, filters map[string]any) (*Session, int, frame) {
		t.Helper()
		arguments := map[string]any{"type": "java", "mainClass": "Spk", "classPaths": classpaths[1], "modulePaths": nonNil(classpaths[0]), "javaExec": javaExec, "cwd": root, "console": "internalConsole"}
		if filters != nil {
			arguments["stepFilters"] = filters
		}
		args, _ := json.Marshal(arguments)
		var port int
		jdtlsCommand(ctx, t, root, "vscode.java.startDebugSession", []any{}, &port)
		s, err := ConnectAdapter(ctx, ConnectSpec{Port: port, OwnerPluginID: "machine-smoke", DebuggerType: "java", ProjectRoot: root})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close(name + " cleanup") })
		if _, err := s.Begin(ctx, BeginParams{AdapterID: "java", Request: "launch", Arguments: args, Breakpoints: map[string][]SourceBreakpoint{source: {{Line: callLine}}}}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		// Each wait is bounded, so a lost event fails with the recorded
		// events instead of waiting for the whole smoke timeout.
		waitCtx, cancelWait := context.WithTimeout(ctx, smokePauseTimeout)
		defer cancelWait()
		ev, ended := rec.waitNthEvent(waitCtx, t, s.ID(), "stopped", 1)
		if ended {
			t.Fatalf("%s: the program ended before the breakpoint", name)
		}
		var stopped struct {
			ThreadID int `json:"threadId"`
		}
		_ = json.Unmarshal(ev.Body, &stopped)
		if top := topFrame(s, stopped.ThreadID); top.Line != callLine {
			t.Fatalf("%s: paused at %+v, want line %d", name, top, callLine)
		}
		smokeRequest(ctx, t, s, "stepIn", map[string]any{"threadId": stopped.ThreadID}, nil)
		stepCtx, cancelStep := context.WithTimeout(ctx, smokePauseTimeout)
		defer cancelStep()
		if _, ended := rec.waitNthEvent(stepCtx, t, s.ID(), "stopped", 2); ended {
			t.Fatalf("%s: the program ended instead of stepping in", name)
		}
		top := topFrame(s, stopped.ThreadID)
		t.Logf("%s: F7 top frame %s (%s) line %d", name, top.Name, top.Source.Path, top.Line)
		return s, stopped.ThreadID, top
	}

	// With the default step filters F7 skips ArrayList.size() into twice.
	s, thread, top := stepInFromCall("step filters", smokeStepFilters)
	if !strings.Contains(top.Name, "twice") || top.Line != twiceLine || top.Source.Path != source {
		t.Fatalf("with step filters F7 stopped in %+v, want Spk.twice at line %d", top, twiceLine)
	}

	// Values in main's frame (one below twice).
	var trace struct {
		StackFrames []frame `json:"stackFrames"`
	}
	smokeRequest(ctx, t, s, "stackTrace", map[string]any{"threadId": thread, "startFrame": 0, "levels": 20}, &trace)
	if len(trace.StackFrames) < 2 {
		t.Fatalf("stack %+v, want twice above main", trace.StackFrames)
	}
	var scopes struct {
		Scopes []struct {
			VariablesReference int `json:"variablesReference"`
		} `json:"scopes"`
	}
	smokeRequest(ctx, t, s, "scopes", map[string]any{"frameId": trace.StackFrames[1].ID}, &scopes)
	locals := map[string]smokeVariable{}
	for _, local := range variables(s, scopes.Scopes[0].VariablesReference) {
		locals[local.Name] = local
		t.Logf("local %s = %q (%s) lazy=%v", local.Name, local.Value, local.Type, local.lazy())
	}
	first := func(container string) smokeVariable {
		t.Helper()
		children := variables(s, locals[container].VariablesReference)
		if len(children) == 0 {
			t.Fatalf("%s %+v has no elements", container, locals[container])
		}
		t.Logf("%s = %q; element %s = %q (%s) lazy=%v", container, locals[container].Value, children[0].Name, children[0].Value, children[0].Type, children[0].lazy())
		return children[0]
	}
	if element := first("list"); element.Value != "1" || element.lazy() {
		t.Errorf("list element %+v, want 1, not lazy", element)
	}
	entry := first("map")
	if entry.Value != `"a" -> 1` || entry.lazy() || entry.VariablesReference == 0 {
		t.Errorf("map entry %+v, want \"a\" -> 1, not lazy, expandable", entry)
	} else {
		names := []string{}
		for _, part := range variables(s, entry.VariablesReference) {
			names = append(names, part.Name+"="+part.Value)
		}
		t.Logf("map entry children %v", names)
		if !slices.Contains(names, `key="a"`) || !slices.Contains(names, "value=1") {
			t.Errorf("map entry children %v, want key=\"a\" and value=1", names)
		}
	}
	// Formatted like the primitive: java-debug shows a double 2.5 as 2.500000.
	for name, want := range map[string]string{"big": "5", "flag": "true", "d": locals["pd"].Value, "none": "null"} {
		if got := locals[name]; got.Value != want || got.lazy() {
			t.Errorf("local %s = %+v, want %s, not lazy", name, got, want)
		}
	}
	if ch := locals["ch"]; !strings.Contains(ch.Value, "x") || strings.Contains(ch.Value, "@") || ch.lazy() {
		t.Errorf("local ch = %+v, want the char x, not lazy", ch)
	}
	// A class with its own toString() keeps upstream's display.
	if point := locals["point"]; !regexp.MustCompile(`Point@\d+$`).MatchString(point.Value) || !point.lazy() {
		t.Errorf("local point = %+v, want upstream's lazy Point@<id>", point)
	} else if resolved := variables(s, point.VariablesReference); len(resolved) != 1 || !strings.HasSuffix(resolved[0].Value, `"P3"`) {
		t.Errorf("point resolved to %+v, want its toString \"P3\"", resolved)
	}
	// Hex display (DAP format.hex) applies to a boxed integer like to an int.
	var hex struct {
		Variables []smokeVariable `json:"variables"`
	}
	smokeRequest(ctx, t, s, "variables", map[string]any{"variablesReference": locals["list"].VariablesReference, "format": map[string]any{"hex": true}}, &hex)
	if len(hex.Variables) == 0 || !regexp.MustCompile(`^0x0*1$`).MatchString(hex.Variables[0].Value) {
		t.Errorf("list element with format.hex %+v, want 0x…1", hex.Variables)
	} else {
		t.Logf("list element with format.hex = %q", hex.Variables[0].Value)
	}
	smokeRequest(ctx, t, s, "disconnect", map[string]any{"terminateDebuggee": true}, nil)

	// Without step filters F7 enters the JDK's ArrayList.size().
	s, _, top = stepInFromCall("no step filters", nil)
	if strings.Contains(top.Name, "twice") || !strings.Contains(top.Name, "ArrayList") {
		t.Fatalf("without step filters F7 stopped in %+v, want java.util.ArrayList", top)
	}
	smokeRequest(ctx, t, s, "disconnect", map[string]any{"terminateDebuggee": true}, nil)
}

// TestMachineSmokeJavaIdeaSetVariable checks DAP setVariable (the Variables
// pane's "Set Value") against a real jdtls with the java-debug bundle (same
// opt-in environment as TestMachineSmokeJava), paused on a breakpoint before
// the program prints its values:
//   - `int total` 0 → 41: the response carries value 41 and type int, and
//     the variables read again (and the program's output) show 41;
//   - `abc` for the int: rejected with java-debug's message (logged); total
//     still reads 41, nothing half-applied;
//   - an int[] element: set like a local;
//   - an element of a List (a logical-structure child, `0` → shown as `1`
//     with the patched jar): whatever java-debug answers is logged; it must
//     either take effect (the element and the output show the new value) or
//     fail with a message, and in both cases leave the other values intact.
//     Measured 2026-11-06 (java-debug 0.53.1): rejected with "Failed to set
//     variable. Reason: java.lang.IllegalArgumentException: SetVariableRequest:
//     Variable 0 cannot be found." (the container is the ArrayList object,
//     which has no field "0"); the list and the output keep 1.
func TestMachineSmokeJavaIdeaSetVariable(t *testing.T) {
	javaExec, jar := smokeJava(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("JAVA_HOME", "")
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "src", "SetVar.java")
	text := "import java.util.*;\n" +
		"public class SetVar {\n" +
		"    public static void main(String[] args) {\n" +
		"        List<Integer> list = new ArrayList<>(List.of(1, 2, 3));\n" +
		"        int[] arr = {4, 5};\n" +
		"        int total = 0;\n" +
		"        System.out.println(\"values \" + total + \" \" + list + \" \" + arr[0]);\n" +
		"    }\n" +
		"}\n"
	const printLine = 7
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), javaSmokeTimeout)
	defer cancel()
	rec := captureEmits(t)
	t.Cleanup(func() {
		if t.Failed() {
			rec.logAll(t)
		}
	})
	t.Cleanup(func() { lsp.Shutdown(context.Background()) })
	lsp.SetJdtlsBundleProvider(func() []string { return []string{jar} })
	t.Cleanup(func() { lsp.SetJdtlsBundleProvider(nil) })
	stop := startJdtls(ctx, t, root, source, text)
	defer stop()
	var classpaths [2][]string
	jdtlsCommand(ctx, t, root, "vscode.java.resolveClasspath", []any{"SetVar", ""}, &classpaths)
	var ignored json.RawMessage
	jdtlsCommand(ctx, t, root, "vscode.java.updateDebugSettings", []any{`{"logLevel":"WARN","showLogicalStructure":true,"showToString":true,"exceptionFilters":{"exceptionTypes":[]}}`}, &ignored)

	args, _ := json.Marshal(map[string]any{"type": "java", "mainClass": "SetVar", "classPaths": classpaths[1], "modulePaths": nonNil(classpaths[0]), "javaExec": javaExec, "cwd": root, "console": "internalConsole"})
	var port int
	jdtlsCommand(ctx, t, root, "vscode.java.startDebugSession", []any{}, &port)
	s, err := ConnectAdapter(ctx, ConnectSpec{Port: port, OwnerPluginID: "machine-smoke", DebuggerType: "java", ProjectRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close("set variable cleanup") })
	capabilities, err := s.Begin(ctx, BeginParams{AdapterID: "java", Request: "launch", Arguments: args, Breakpoints: map[string][]SourceBreakpoint{source: {{Line: printLine}}}})
	if err != nil {
		t.Fatal(err)
	}
	var caps struct {
		SupportsSetVariable bool `json:"supportsSetVariable"`
	}
	_ = json.Unmarshal(capabilities, &caps)
	if !caps.SupportsSetVariable {
		t.Fatalf("capabilities %s, want supportsSetVariable", capabilities)
	}
	waitCtx, cancelWait := context.WithTimeout(ctx, smokePauseTimeout)
	defer cancelWait()
	ev, ended := rec.waitNthEvent(waitCtx, t, s.ID(), "stopped", 1)
	if ended {
		t.Fatal("the program ended before the breakpoint")
	}
	var stopped struct {
		ThreadID int `json:"threadId"`
	}
	_ = json.Unmarshal(ev.Body, &stopped)
	var trace struct {
		StackFrames []struct {
			ID   int `json:"id"`
			Line int `json:"line"`
		} `json:"stackFrames"`
	}
	smokeRequest(ctx, t, s, "stackTrace", map[string]any{"threadId": stopped.ThreadID, "startFrame": 0, "levels": 20}, &trace)
	if len(trace.StackFrames) == 0 || trace.StackFrames[0].Line != printLine {
		t.Fatalf("paused at %+v, want line %d", trace.StackFrames, printLine)
	}
	var scopes struct {
		Scopes []struct {
			VariablesReference int `json:"variablesReference"`
		} `json:"scopes"`
	}
	smokeRequest(ctx, t, s, "scopes", map[string]any{"frameId": trace.StackFrames[0].ID}, &scopes)
	localsRef := scopes.Scopes[0].VariablesReference
	variables := func(ref int) map[string]smokeVariable {
		t.Helper()
		var body struct {
			Variables []smokeVariable `json:"variables"`
		}
		smokeRequest(ctx, t, s, "variables", map[string]any{"variablesReference": ref}, &body)
		out := map[string]smokeVariable{}
		for _, v := range body.Variables {
			out[v.Name] = v
		}
		return out
	}
	type setResult struct {
		Value              string `json:"value"`
		Type               string `json:"type"`
		VariablesReference int    `json:"variablesReference"`
	}
	setVariable := func(ref int, name, value string) (setResult, error) {
		t.Helper()
		var result setResult
		body, err := s.Request(ctx, "setVariable", map[string]any{"variablesReference": ref, "name": name, "value": value})
		if err == nil {
			err = json.Unmarshal(body, &result)
		}
		return result, err
	}
	locals := variables(localsRef)
	if locals["total"].Value != "0" {
		t.Fatalf("total = %+v, want 0", locals["total"])
	}

	// int total 0 → 41.
	result, err := setVariable(localsRef, "total", "41")
	if err != nil {
		t.Fatalf("set total to 41: %v", err)
	}
	t.Logf("set total = 41: response %+v", result)
	if result.Value != "41" || result.Type != "int" {
		t.Fatalf("set total response %+v, want value 41 type int", result)
	}
	if got := variables(localsRef)["total"].Value; got != "41" {
		t.Fatalf("total reads %q after the set, want 41", got)
	}

	// abc for an int: rejected, nothing changes.
	_, err = setVariable(localsRef, "total", "abc")
	var requestErr *RequestError
	if !errors.As(err, &requestErr) {
		t.Fatalf("set total to abc: %v, want a DAP error response", err)
	}
	t.Logf("set total = abc: rejected with java-debug's message %q", requestErr.Message)
	if got := variables(localsRef)["total"].Value; got != "41" {
		t.Fatalf("total reads %q after the rejected set, want 41", got)
	}

	// An array element, set like a local.
	arr := locals["arr"]
	result, err = setVariable(arr.VariablesReference, "0", "9")
	if err != nil {
		t.Fatalf("set arr[0] to 9: %v", err)
	}
	t.Logf("set arr[0] = 9: response %+v", result)
	if got := variables(arr.VariablesReference)["0"].Value; got != "9" {
		t.Fatalf("arr[0] reads %q after the set, want 9", got)
	}

	// A List element: a logical-structure child of the ArrayList.
	list := locals["list"]
	before := variables(list.VariablesReference)
	t.Logf("list = %q, element 0 = %+v", list.Value, before["0"])
	if before["0"].Value != "1" {
		t.Fatalf("list element 0 %+v, want 1 (the patched jar's display)", before["0"])
	}
	result, err = setVariable(list.VariablesReference, "0", "7")
	elementSet := err == nil
	if elementSet {
		t.Logf("set list[0] = 7: response %+v", result)
	} else {
		t.Logf("set list[0] = 7: %v", err)
		if !errors.As(err, &requestErr) || strings.TrimSpace(requestErr.Message) == "" {
			t.Fatalf("set list[0]: %v, want a DAP error response with a message", err)
		}
	}
	// Whatever the answer, the values read again are consistent with it.
	after := variables(list.VariablesReference)
	t.Logf("list element 0 after the attempt: %+v (%d elements)", after["0"], len(after))
	if len(after) != len(before) {
		t.Fatalf("list has %d elements after the attempt, want %d", len(after), len(before))
	}
	wantElement := "1"
	if elementSet {
		wantElement = "7"
	}
	if after["0"].Value != wantElement {
		t.Errorf("list element 0 reads %q after the attempt (response ok=%v %+v), want %s", after["0"].Value, elementSet, result, wantElement)
	}
	locals = variables(localsRef)
	if locals["total"].Value != "41" {
		t.Errorf("total reads %q after the list attempt, want 41", locals["total"].Value)
	}

	// The program sees what was set.
	smokeRequest(ctx, t, s, "continue", map[string]any{"threadId": stopped.ThreadID}, nil)
	endCtx, cancelEnd := context.WithTimeout(ctx, smokePauseTimeout)
	defer cancelEnd()
	if _, ended := rec.waitNthEvent(endCtx, t, s.ID(), "stopped", 2); !ended {
		t.Fatal("paused again, want the program to end")
	}
	want := fmt.Sprintf("values 41 [%s, 2, 3] 9", wantElement)
	if out := rec.output(s.ID()); !strings.Contains(out, want) {
		t.Errorf("program output %q, want %q", out, want)
	} else {
		t.Logf("program output: %s", want)
	}
}
