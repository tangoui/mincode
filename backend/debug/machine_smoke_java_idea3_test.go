package debug

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"mincode/app/backend/core"
	"mincode/app/backend/lsp"
)

// Phase 3a of Java debugging close to IDEA, against a real jdtls with the
// java-debug bundle (same opt-in environment as TestMachineSmokeJava):
//
//	DEVELOPER_DIR=/Library/Developer/CommandLineTools CGO_ENABLED=1 \
//	  MINCODE_DEBUG_MACHINE_SMOKE=1 \
//	  MINCODE_DEBUG_SMOKE_JAVA_HOME=/opt/homebrew/opt/openjdk@21 \
//	  MINCODE_DEBUG_SMOKE_JAVA_DEBUG_JAR=$PWD/../official-plugins/java-debug/lib/com.microsoft.java.debug.plugin-0.53.1.jar \
//	  go test ./backend/debug -run TestMachineSmokeJavaIdea3 -v -timeout 900s
//
// TestMachineSmokeJavaIdea3FirstDebugTiming additionally needs
// MINCODE_DEBUG_SMOKE_TIMING=1 (see its comment for the command).

// idea3Frame is one DAP stack frame with its source.
type idea3Frame struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Line   int    `json:"line"`
	Source *struct {
		Name            string `json:"name"`
		Path            string `json:"path"`
		SourceReference int    `json:"sourceReference"`
	} `json:"source"`
}

func (frame idea3Frame) path() string {
	if frame.Source == nil {
		return ""
	}
	return frame.Source.Path
}

func (f *idea2Fixture) sourceFrames(t *testing.T, s *Session, threadID int) []idea3Frame {
	t.Helper()
	var trace struct {
		StackFrames []idea3Frame `json:"stackFrames"`
	}
	smokeRequest(f.ctx, t, s, "stackTrace", map[string]any{"threadId": threadID, "startFrame": 0, "levels": 20}, &trace)
	if len(trace.StackFrames) == 0 {
		t.Fatalf("thread %d has no frames", threadID)
	}
	return trace.StackFrames
}

// addressBreakpointEvents returns the `breakpoint` events of s whose source
// path is address, as raw bodies.
func (f *idea2Fixture) addressBreakpointEvents(s *Session, address string) []string {
	f.rec.mu.Lock()
	defer f.rec.mu.Unlock()
	var bodies []string
	for _, ev := range f.rec.events {
		if ev.SessionID != s.ID() || ev.Event != "breakpoint" {
			continue
		}
		var body struct {
			Breakpoint struct {
				Source *struct {
					Path string `json:"path"`
				} `json:"source"`
			} `json:"breakpoint"`
		}
		if json.Unmarshal(ev.Body, &body) == nil && body.Breakpoint.Source != nil && body.Breakpoint.Source.Path == address {
			bodies = append(bodies, string(ev.Body))
		}
	}
	return bodies
}

// TestMachineSmokeJavaIdea3AddressBreakpoint checks breakpoints in a library
// class addressed by the debugger's own source address (a `jdt://` URI):
// the address of java.util.ArrayList comes from a JDK frame of the stack
// (ArrayList.forEach calling a lambda), the first statement line of
// ArrayList.add from the class's source (project_lsp_jdt_contents).
//
//   - Start: BeginParams.AddressBreakpoints {<address>: [add's first line]}
//     → the program pauses in ArrayList.add with the top frame's source path
//     equal to the address and its line equal to the requested one; the
//     first pause whose caller is LibApp.main is the `list.add("seed")` call
//     (JDK code may call add earlier: such pauses are counted and resumed).
//   - Mid-session: debug_set_breakpoints {address} while paused in main →
//     the next `list.add` pauses in ArrayList.add the same way.
//
// What java-debug does is logged (verification of the start results and of
// the mid-session answer, later `breakpoint` events, line mapping).
func TestMachineSmokeJavaIdea3AddressBreakpoint(t *testing.T) {
	text := "import java.util.ArrayList;\n" +
		"\n" +
		"public class LibApp {\n" +
		"    public static void main(String[] args) {\n" +
		"        ArrayList<String> list = new ArrayList<>();\n" +
		"        list.add(\"seed\");\n" + // 6
		"        list.forEach(s -> {\n" +
		"            System.out.println(\"item \" + s);\n" + // 8
		"        });\n" +
		"        list.add(\"marker\");\n" + // 10
		"        System.out.println(\"size \" + list.size());\n" +
		"    }\n" +
		"}\n"
	const seedLine, lambdaLine, markerLine = 6, 8, 10
	f := newIdea2Fixture(t, "LibApp", text)

	// 1. The address of ArrayList: a JDK frame below the lambda.
	probe, _ := f.launch(t, "address probe", map[string][]SourceBreakpoint{f.source: {{Line: lambdaLine}}})
	stop, ended := f.waitStop(t, probe, 1)
	if ended {
		t.Fatal("the probe program ended, want a pause in the lambda")
	}
	var address string
	for _, frame := range f.sourceFrames(t, probe, stop.ThreadID) {
		t.Logf("probe frame %s line %d source %+v", frame.Name, frame.Line, frame.Source)
		if strings.HasPrefix(frame.Name, "ArrayList.forEach") && frame.path() != "" {
			address = frame.path()
			if frame.Source.SourceReference <= 0 {
				t.Logf("ArrayList frame has no sourceReference")
			}
		}
	}
	if !strings.HasPrefix(address, "jdt://") {
		t.Fatalf("no ArrayList.forEach frame with a jdt:// source path; got %q", address)
	}
	if _, err := ValidDebuggerAddress(address); err != nil {
		t.Fatalf("java-debug's address %q fails the host rule: %v", address, err)
	}
	t.Logf("ArrayList address %q (%d characters)", address, len(address))
	result, err := lspDispatch(t, "project_lsp_jdt_contents", map[string]any{"root": f.root, "uri": address})
	if err != nil {
		t.Fatalf("source of %s: %v", address, err)
	}
	var content string
	if err := json.Unmarshal([]byte(result), &content); err != nil {
		t.Fatalf("decode source: %v", err)
	}
	addLine := 0
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "public boolean add(E e) {" && i+1 < len(lines) {
			addLine = i + 2
			break
		}
	}
	if addLine == 0 {
		t.Fatalf("no `public boolean add(E e) {` in the ArrayList source (%d characters)", len(content))
	}
	t.Logf("ArrayList.add's first statement: line %d %q", addLine, strings.TrimSpace(lines[addLine-1]))
	probe.Close("address probe done")

	// expectAddPause resumes pauses in ArrayList.add that JDK code caused
	// until one is called from LibApp.main at callerLine, and checks the top
	// frame's address and line. Returns the paused thread and the number of
	// earlier (JDK) pauses.
	expectAddPause := func(s *Session, first, callerLine int) (threadID, n, skipped int) {
		t.Helper()
		for n = first; n < first+200; n++ {
			stop, ended := f.waitStop(t, s, n)
			if ended {
				t.Fatalf("the program ended after %d pauses, want a pause in ArrayList.add called from line %d", n-first, callerLine)
			}
			frames := f.sourceFrames(t, s, stop.ThreadID)
			top := frames[0]
			if top.path() != address || top.Line != addLine {
				t.Fatalf("pause %d (%s) in %s line %d source %q, want line %d of %q", n, stop.Reason, top.Name, top.Line, top.path(), addLine, address)
			}
			if len(frames) > 1 && strings.HasPrefix(frames[1].Name, "LibApp.main") {
				if frames[1].Line != callerLine {
					t.Fatalf("pause %d called from main line %d, want %d", n, frames[1].Line, callerLine)
				}
				t.Logf("pause %d: reason %q, %s line %d, called from main line %d (%d JDK pauses before)", n, stop.Reason, top.Name, top.Line, callerLine, skipped)
				return stop.ThreadID, n, skipped
			}
			if skipped < 3 && len(frames) > 1 {
				t.Logf("JDK pause %d: called from %s line %d", n, frames[1].Name, frames[1].Line)
			}
			skipped++
			smokeRequest(f.ctx, t, s, "continue", map[string]any{"threadId": stop.ThreadID}, nil)
		}
		t.Fatalf("no pause called from main line %d in 200 pauses", callerLine)
		return 0, 0, 0
	}

	// 2. Sent with the start request.
	started := time.Now()
	s, _ := f.launchWith(t, "address breakpoint at start", BeginParams{AddressBreakpoints: map[string][]SourceBreakpoint{address: {{Line: addLine}}}})
	set := f.rec.waitEvent(f.ctx, t, s.ID(), BreakpointsSetEvent)
	var startResults struct {
		Path        string `json:"path"`
		Address     bool   `json:"address"`
		Breakpoints []struct {
			ID       int    `json:"id"`
			Verified bool   `json:"verified"`
			Line     int    `json:"line"`
			Message  string `json:"message"`
		} `json:"breakpoints"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(set.Body, &startResults); err != nil || startResults.Path != address || !startResults.Address || len(startResults.Breakpoints) != 1 {
		t.Fatalf("start results %s (%v), want one result for the address with address: true", set.Body, err)
	}
	t.Logf("start result after %s: %s", time.Since(started).Round(time.Millisecond), set.Body)
	threadID, n, skipped := expectAddPause(s, 1, seedLine)
	t.Logf("first pause from main after %s (%d JDK pauses first)", time.Since(started).Round(time.Millisecond), skipped)
	for _, body := range f.addressBreakpointEvents(s, address) {
		t.Logf("breakpoint event for the address: %s", body)
	}
	smokeRequest(f.ctx, t, s, "continue", map[string]any{"threadId": threadID}, nil)
	threadID, n, _ = expectAddPause(s, n+1, markerLine)
	clear, err := core.Dispatch("debug_set_breakpoints", `{"sessionId":"`+s.ID()+`","address":`+jsonString(address)+`,"breakpoints":[]}`)
	if err != nil {
		t.Fatalf("clear the address breakpoint: %v", err)
	}
	t.Logf("cleared: %s", clear)
	smokeRequest(f.ctx, t, s, "continue", map[string]any{"threadId": threadID}, nil)
	if _, ended := f.waitStop(t, s, n+1); !ended {
		t.Fatal("paused again after clearing the address breakpoint, want the program to end")
	}
	if out := f.rec.output(s.ID()); !strings.Contains(out, "size 2") {
		t.Errorf("program output %q, want size 2", out)
	}

	// 3. Added mid-session, while paused in main before list.add("marker").
	mid, _ := f.launch(t, "address breakpoint mid-session", map[string][]SourceBreakpoint{f.source: {{Line: markerLine}}})
	stop, ended = f.waitStop(t, mid, 1)
	if ended {
		t.Fatal("the program ended, want a pause at the marker line")
	}
	if frames := f.sourceFrames(t, mid, stop.ThreadID); frames[0].Line != markerLine {
		t.Fatalf("paused at %+v, want line %d", frames[0], markerLine)
	}
	answer, err := core.Dispatch("debug_set_breakpoints", `{"sessionId":"`+mid.ID()+`","address":`+jsonString(address)+`,"breakpoints":[{"line":`+jsonInt(addLine)+`}]}`)
	if err != nil {
		t.Fatalf("debug_set_breakpoints with the address: %v", err)
	}
	t.Logf("mid-session answer: %s", answer)
	smokeRequest(f.ctx, t, mid, "continue", map[string]any{"threadId": stop.ThreadID}, nil)
	threadID, n, _ = expectAddPause(mid, 2, markerLine)
	if _, err := core.Dispatch("debug_set_breakpoints", `{"sessionId":"`+mid.ID()+`","address":`+jsonString(address)+`,"breakpoints":[]}`); err != nil {
		t.Fatalf("clear: %v", err)
	}
	smokeRequest(f.ctx, t, mid, "continue", map[string]any{"threadId": threadID}, nil)
	if _, ended := f.waitStop(t, mid, n+1); !ended {
		t.Fatal("paused again after clearing the address breakpoint, want the program to end")
	}
}

func jsonString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func jsonInt(value int) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

// TestMachineSmokeJavaIdea3SpringBootConfig checks what a Spring Boot run
// configuration of the Java plugin hands java-debug, without Spring
// dependencies: a plain main class prints the system property
// spring.profiles.active and two environment variables.
//
// The plugin's debug.resolve (official-plugins/java-debug/backend, a
// separate `package main` module this test cannot import) turns
//
//	{"profiles": ["dev", "local"], "envFile": ".env", "env": {"SHARED": "env"}}
//
// with a .env of
//
//	export FROM_ENV_FILE="from file"
//	SHARED=file
//
// into vmArgs "-Dspring.profiles.active=dev,local" and env
// {FROM_ENV_FILE: "from file", SHARED: "env"} (env wins over the file); its
// unit tests (resolve_test.go TestResolveSpringBootProfilesAndEnvFile,
// envfile_test.go) cover that conversion. Here the same launch arguments
// go to the real java-debug, and the debuggee must see them.
func TestMachineSmokeJavaIdea3SpringBootConfig(t *testing.T) {
	text := "public class BootApp {\n" +
		"    public static void main(String[] args) {\n" +
		"        System.out.println(\"profiles=\" + System.getProperty(\"spring.profiles.active\"));\n" +
		"        System.out.println(\"fromEnvFile=\" + System.getenv(\"FROM_ENV_FILE\"));\n" +
		"        System.out.println(\"shared=\" + System.getenv(\"SHARED\"));\n" +
		"    }\n" +
		"}\n"
	f := newIdea2Fixture(t, "BootApp", text)

	// The plugin's resolve result for the configuration above.
	var arguments map[string]any
	if err := json.Unmarshal(f.args, &arguments); err != nil {
		t.Fatal(err)
	}
	arguments["vmArgs"] = "-Dspring.profiles.active=dev,local"
	arguments["env"] = map[string]string{"FROM_ENV_FILE": "from file", "SHARED": "env"}
	args, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	f.args = args

	s, _ := f.launch(t, "spring boot config", nil)
	if _, ended := f.waitStop(t, s, 1); !ended {
		t.Fatal("the program paused, want it to run to the end")
	}
	out := f.rec.output(s.ID())
	for _, want := range []string{"profiles=dev,local", "fromEnvFile=from file", "shared=env"} {
		if !strings.Contains(out, want) {
			t.Errorf("program output %q, want %q", out, want)
		}
	}
	t.Logf("program output: %q", out)
}

// firstDebugStages are the stage names of one timed debug start, in order.
// Stages a path does not have stay zero.
var firstDebugStages = []string{
	"stop",       // B: project_lsp_stop of the jdtls without the bundle (the frontend kills it)
	"spawn",      // project_lsp_start (launcher process start)
	"initialize", // initialize request: JVM, OSGi, bundles until the reply
	"ready",      // initialized → ServiceReady (project import)
	"mainClass",  // vscode.java.resolveMainClass (debug.resolve with no mainClass)
	"classpath",  // vscode.java.resolveClasspath
	"javaExec",   // vscode.java.resolveJavaExecutable (debug.resolve without javaExec)
	"settings",   // vscode.java.updateDebugSettings (debug.applySettings)
	"startDebug", // vscode.java.startDebugSession
	"connect",    // ConnectAdapter to java-debug's port
	"begin",      // DAP initialize → launch → breakpoints → configurationDone
	"firstStop",  // Begin returned (configurationDone answered) → first stopped event; the JVM starts during begin (launch)
	"total",      // sum of the stages above
	"buildLag",   // not serial: ServiceReady → main class file on disk (- if already there)
}

// firstDebugSettle is how long path B's jdtls without the bundle runs
// before the debug click.
const firstDebugSettle = 8 * time.Second

// firstDebugMaxStalls is how many stalled Maven runs the timing smoke
// records before it skips the remaining Maven runs.
const firstDebugMaxStalls = 2

type firstDebugRow struct {
	path   string
	run    int
	stages map[string]time.Duration
	notes  []string
	// allowStall: the run may need Maven Central; stalled: it never got
	// ServiceReady (not part of the medians).
	allowStall, stalled bool
}

// firstDebugProject is a temp project: kind "maven" (pom.xml without
// dependencies, src/main/java/demo/App.java) or "plain" (src/App.java). The
// folder name is unique: the Homebrew jdtls launcher keys its -data
// workspace on the folder's base name only.
type firstDebugProject struct {
	kind, root, source, classFile, text string
	breakpointLine                      int
}

const firstDebugPOM = `<?xml version="1.0" encoding="UTF-8"?>
<project xmlns="http://maven.apache.org/POM/4.0.0" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"
         xsi:schemaLocation="http://maven.apache.org/POM/4.0.0 https://maven.apache.org/xsd/maven-4.0.0.xsd">
  <modelVersion>4.0.0</modelVersion>
  <groupId>demo</groupId>
  <artifactId>timing-app</artifactId>
  <version>1.0.0</version>
  <packaging>jar</packaging>
  <properties>
    <maven.compiler.release>17</maven.compiler.release>
    <project.build.sourceEncoding>UTF-8</project.build.sourceEncoding>
  </properties>
</project>
`

func newFirstDebugProject(t *testing.T, kind string, n int) firstDebugProject {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, fmt.Sprintf("timing-%s-%d-%d", kind, n, time.Now().UnixNano()))
	body := "    public static void main(String[] args) {\n" +
		"        int a = 2, b = 3;\n" +
		"        int total = a + b;\n" +
		"        System.out.println(\"total \" + total);\n" +
		"    }\n" +
		"}\n"
	p := firstDebugProject{kind: kind, root: root}
	write := func(rel, text string) {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if kind == "maven" {
		p.text = "package demo;\n\npublic class App {\n" + body
		p.breakpointLine = 6
		p.source = filepath.Join(root, "src", "main", "java", "demo", "App.java")
		p.classFile = filepath.Join(root, "target", "classes", "demo", "App.class")
		write("pom.xml", firstDebugPOM)
		write("src/main/java/demo/App.java", p.text)
	} else {
		p.text = "public class App {\n" + body
		p.breakpointLine = 4
		p.source = filepath.Join(root, "src", "App.java")
		write("src/App.java", p.text)
	}
	return p
}

// startJdtlsTimed is startJdtls with the spawn, initialize and import
// stages timed and the language/status timeline kept.
// With row.allowStall a missing ServiceReady (a Maven download that never
// finishes) is recorded in row.stalled instead of failing the test.
func startJdtlsTimed(ctx context.Context, t *testing.T, p firstDebugProject, row *firstDebugRow) {
	t.Helper()
	root := p.root
	var mu sync.Mutex
	var timeline []string
	began := time.Now()
	core.SetEmitter(func(_ string, payload any) {
		raw, err := json.Marshal(payload)
		if err != nil {
			return
		}
		var event struct {
			ServerID   string          `json:"serverId"`
			Generation uint64          `json:"generation"`
			Method     string          `json:"method"`
			Params     json.RawMessage `json:"params"`
			RequestID  *uint64         `json:"requestId"`
		}
		if json.Unmarshal(raw, &event) != nil || event.ServerID != "jdtls" {
			return
		}
		if event.Method == "language/status" {
			var status struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			}
			_ = json.Unmarshal(event.Params, &status)
			mu.Lock()
			timeline = append(timeline, fmt.Sprintf("%s %s %q", time.Since(began).Round(time.Millisecond), status.Type, status.Message))
			mu.Unlock()
		}
		if event.RequestID == nil {
			return
		}
		result := json.RawMessage("null")
		if event.Method == "workspace/configuration" {
			var params struct {
				Items []json.RawMessage `json:"items"`
			}
			_ = json.Unmarshal(event.Params, &params)
			result, _ = json.Marshal(make([]any, len(params.Items)))
		}
		_, _ = lspDispatch(t, "project_lsp_reply", map[string]any{
			"root": root, "serverId": "jdtls", "generation": event.Generation, "requestId": *event.RequestID, "result": result,
		})
	})

	mark := time.Now()
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
	gen := status.Generation
	row.stages["spawn"] = time.Since(mark)

	mark = time.Now()
	rootURI := fileURL(root)
	if _, err := lspDispatch(t, "project_lsp_request", map[string]any{
		"root": root, "serverId": "jdtls", "generation": gen, "method": "initialize", "timeoutMs": 60000,
		"params": map[string]any{
			"processId":        nil,
			"clientInfo":       map[string]string{"name": "mincode"},
			"rootUri":          rootURI,
			"workspaceFolders": []any{map[string]string{"uri": rootURI, "name": filepath.Base(root)}},
			"capabilities": map[string]any{
				"workspace": map[string]any{"configuration": true, "workspaceFolders": true, "executeCommand": map[string]any{}},
			},
		},
	}); err != nil {
		t.Fatalf("jdtls initialize: %v", err)
	}
	row.stages["initialize"] = time.Since(mark)

	mark = time.Now()
	for _, message := range []struct {
		method string
		params any
	}{
		{"initialized", map[string]any{}},
		{"textDocument/didOpen", map[string]any{"textDocument": map[string]any{"uri": fileURL(p.source), "languageId": "java", "version": 1, "text": p.text}}},
	} {
		if _, err := lspDispatch(t, "project_lsp_notify", map[string]any{"root": root, "serverId": "jdtls", "generation": gen, "method": message.method, "params": message.params}); err != nil {
			t.Fatalf("jdtls %s: %v", message.method, err)
		}
	}
	readyCtx, cancel := context.WithTimeout(ctx, javaSmokeReadyTimeout)
	defer cancel()
	for {
		ready, running := lsp.JdtlsReady(root)
		if ready {
			break
		}
		if !running {
			logs, _ := lspDispatch(t, "project_lsp_logs", map[string]any{"root": root, "serverId": "jdtls"})
			t.Fatalf("jdtls exited before ServiceReady; logs: %s", logs)
		}
		select {
		case <-time.After(20 * time.Millisecond):
		case <-readyCtx.Done():
			mu.Lock()
			seen := slices.Clone(timeline)
			mu.Unlock()
			if row.allowStall {
				row.stalled = true
				last := ""
				if len(seen) > 0 {
					last = seen[len(seen)-1]
				}
				row.notes = append(row.notes, fmt.Sprintf("STALLED: no ServiceReady after %s, last status %s", javaSmokeReadyTimeout, last))
				return
			}
			t.Fatalf("jdtls not ServiceReady after %s (statuses %v)", javaSmokeReadyTimeout, seen)
		}
	}
	row.stages["ready"] = time.Since(mark)

	// The main class file may come from the build after ServiceReady.
	if p.classFile != "" {
		if _, err := os.Stat(p.classFile); err == nil {
			row.notes = append(row.notes, "class file present at ServiceReady")
		} else {
			buildCtx, cancelBuild := context.WithTimeout(ctx, 60*time.Second)
			defer cancelBuild()
			for {
				if _, err := os.Stat(p.classFile); err == nil {
					row.stages["buildLag"] = time.Since(mark) - row.stages["ready"]
					break
				}
				select {
				case <-time.After(20 * time.Millisecond):
				case <-buildCtx.Done():
					t.Fatalf("%s not built 60s after ServiceReady", p.classFile)
				}
			}
		}
	}
	mu.Lock()
	row.notes = append(row.notes, "statuses: "+strings.Join(timeline, "; "))
	mu.Unlock()
}

// stopJdtlsLikeFrontend stops root's jdtls the way the frontend's restart
// does (project_lsp_stop: the host kills the process tree) and waits until
// the session is gone.
func stopJdtlsLikeFrontend(t *testing.T, root string) {
	t.Helper()
	if _, err := lspDispatch(t, "project_lsp_stop", map[string]any{"root": root, "serverId": "jdtls"}); err != nil {
		t.Fatalf("project_lsp_stop: %v", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, running := lsp.JdtlsReady(root); !running {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("jdtls still running 30s after project_lsp_stop")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// debugOnce runs the jdtls commands of debug.resolve and debug.applySettings,
// starts a session with a breakpoint on p's breakpoint line, times every
// stage until the first stop, then lets the program end.
func debugOnce(ctx context.Context, t *testing.T, rec *emitRecorder, p firstDebugProject, javaExec string, row *firstDebugRow) {
	t.Helper()
	debugOnceWith(ctx, t, rec, p, javaExec, "", row)
}

// debugOnceWith is debugOnce; a non-empty knownMainClass is the run
// configuration's mainClass (debug.resolve then skips resolveMainClass).
func debugOnceWith(ctx context.Context, t *testing.T, rec *emitRecorder, p firstDebugProject, javaExec, knownMainClass string, row *firstDebugRow) {
	t.Helper()
	root := p.root
	timed := func(stage string, run func()) {
		mark := time.Now()
		run()
		row.stages[stage] = time.Since(mark)
	}
	mainClass, projectName := knownMainClass, ""
	if knownMainClass == "" {
		timed("mainClass", func() {
			var items []struct {
				MainClass   string `json:"mainClass"`
				ProjectName string `json:"projectName"`
			}
			jdtlsCommand(ctx, t, root, "vscode.java.resolveMainClass", []any{fileURL(root)}, &items)
			if len(items) != 1 {
				t.Fatalf("resolveMainClass = %+v, want one main class", items)
			}
			mainClass, projectName = items[0].MainClass, items[0].ProjectName
		})
	}
	var classpaths [2][]string
	timed("classpath", func() {
		jdtlsCommand(ctx, t, root, "vscode.java.resolveClasspath", []any{mainClass, projectName}, &classpaths)
	})
	if len(classpaths[1]) == 0 {
		t.Fatalf("resolveClasspath(%s, %s) returned no class path", mainClass, projectName)
	}
	timed("javaExec", func() {
		var resolved *string
		jdtlsCommand(ctx, t, root, "vscode.java.resolveJavaExecutable", []any{mainClass, projectName}, &resolved)
	})
	timed("settings", func() {
		var ignored json.RawMessage
		jdtlsCommand(ctx, t, root, "vscode.java.updateDebugSettings", []any{`{"logLevel":"WARN","showLogicalStructure":true,"showToString":true,"exceptionFilters":{"exceptionTypes":[]}}`}, &ignored)
	})
	var port int
	timed("startDebug", func() {
		jdtlsCommand(ctx, t, root, "vscode.java.startDebugSession", []any{}, &port)
	})
	var s *Session
	timed("connect", func() {
		var err error
		s, err = ConnectAdapter(ctx, ConnectSpec{Port: port, OwnerPluginID: "machine-smoke", DebuggerType: "java", ProjectRoot: root})
		if err != nil {
			t.Fatalf("ConnectAdapter: %v", err)
		}
	})
	defer s.Close("timing run done")
	arguments := map[string]any{"type": "java", "mainClass": mainClass, "classPaths": classpaths[1], "modulePaths": nonNil(classpaths[0]), "javaExec": javaExec, "cwd": root, "console": "internalConsole", "stepFilters": smokeStepFilters}
	if projectName != "" {
		arguments["projectName"] = projectName
	}
	args, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	timed("begin", func() {
		beginCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		if _, err := s.Begin(beginCtx, BeginParams{AdapterID: "java", Request: "launch", Arguments: args,
			Breakpoints: map[string][]SourceBreakpoint{p.source: {{Line: p.breakpointLine}}}}); err != nil {
			t.Fatalf("Begin: %v", err)
		}
	})
	var stop idea2Stop
	timed("firstStop", func() {
		waitCtx, cancel := context.WithTimeout(ctx, smokePauseTimeout)
		defer cancel()
		ev, ended := rec.waitNthEvent(waitCtx, t, s.ID(), "stopped", 1)
		if ended {
			t.Fatalf("%s %s: the program ended before the breakpoint; output %q", row.path, p.kind, rec.output(s.ID()))
		}
		_ = json.Unmarshal(ev.Body, &stop)
	})
	if stop.Reason != "breakpoint" {
		t.Fatalf("stopped reason %q, want breakpoint", stop.Reason)
	}
	smokeRequest(ctx, t, s, "continue", map[string]any{"threadId": stop.ThreadID}, nil)
	waitCtx, cancel := context.WithTimeout(ctx, smokePauseTimeout)
	defer cancel()
	if _, ended := rec.waitNthEvent(waitCtx, t, s.ID(), "stopped", 2); !ended {
		t.Fatal("paused again, want the program to end")
	}
	if out := rec.output(s.ID()); !strings.Contains(out, "total 5") {
		t.Errorf("program output %q, want total 5", out)
	}
	row.notes = append(row.notes, fmt.Sprintf("mainClass %s project %q", mainClass, projectName))
}

func (row *firstDebugRow) sum() {
	var total time.Duration
	for _, stage := range firstDebugStages {
		if stage == "total" || stage == "buildLag" {
			continue
		}
		total += row.stages[stage]
	}
	row.stages["total"] = total
}

// medianDuration is the median of values; with an even count it is the
// upper of the two middle values.
func medianDuration(values []time.Duration) time.Duration {
	if len(values) == 0 {
		return 0
	}
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	return sorted[len(sorted)/2]
}

// steadySlowerThanCold reports whether the steady-state median is slower
// than the cold one; compared is false (nothing to check) when either path
// has no run.
func steadySlowerThanCold(cold, steady []time.Duration) (slower, compared bool) {
	if len(cold) == 0 || len(steady) == 0 {
		return false, false
	}
	return medianDuration(steady) > medianDuration(cold), true
}

// TestFirstDebugSteadyCheckNeedsBothMedians: the timing smoke's
// "steady not slower than cold" check compares two medians only when both
// paths have runs (one stalled cold run with MINCODE_DEBUG_SMOKE_TIMING_RUNS=1
// leaves cold empty, and an empty median is 0).
func TestFirstDebugSteadyCheckNeedsBothMedians(t *testing.T) {
	ms := func(values ...int) []time.Duration {
		var out []time.Duration
		for _, value := range values {
			out = append(out, time.Duration(value)*time.Millisecond)
		}
		return out
	}
	for _, c := range []struct {
		cold, steady     []time.Duration
		slower, compared bool
	}{
		{ms(900, 1000, 1100), ms(200, 300), false, true},
		{ms(100), ms(200), true, true},
		{nil, ms(200), false, false},
		{ms(1000), nil, false, false},
	} {
		slower, compared := steadySlowerThanCold(c.cold, c.steady)
		if slower != c.slower || compared != c.compared {
			t.Errorf("cold %v steady %v: (%v, %v), want (%v, %v)", c.cold, c.steady, slower, compared, c.slower, c.compared)
		}
	}
}

func formatStage(d time.Duration) string {
	if d == 0 {
		return "-"
	}
	return strconv.FormatInt(d.Milliseconds(), 10)
}

// TestMachineSmokeJavaIdea3FirstDebugTiming measures where the time goes
// from "start debugging" to the first breakpoint, for a minimal Maven
// project (no dependencies; jdtls's Maven import still resolves the default
// build plugins from Maven Central into the temp HOME's ~/.m2) and a plain
// folder, each run in a fresh project folder:
//
//   - A cold: fresh HOME (empty ~/.m2 and jdtls workspace data), jdtls not
//     running → start jdtls with the bundle, import, resolve, launch, stop.
//   - A cold mainClass-set: the same with the run configuration's mainClass
//     set (debug.resolve skips resolveMainClass).
//   - A warm-m2 (Maven only): the same with ~/.m2 already filled (the
//     usual Maven user), jdtls workspace data still new.
//   - B: jdtls already running WITHOUT the bundle (ServiceReady, then
//     firstDebugSettle for its background work) → stop it
//     like the frontend's restart, start it with the bundle (warm workspace
//     data), resolve, launch, stop.
//   - C: jdtls running WITH the bundle (right after B) → resolve, launch, stop.
//
// Each path runs MINCODE_DEBUG_SMOKE_TIMING_RUNS times (default 3); the
// per-run table and the medians are logged. Only loose sanity bounds are
// asserted; a Maven run (any path) whose download never finishes is logged
// as STALLED and left out of the medians. MINCODE_DEBUG_SMOKE_TIMING_KINDS
// (default "maven,plain") selects the projects, MINCODE_DEBUG_SMOKE_TIMING_IDLE_MS
// adds an idle wait between ServiceReady and the debug click on the A paths.
// It needs Maven Central for the cold Maven paths and takes about 5 minutes
// (worst case with two stalled downloads about 11 minutes), so it has its own
// switch MINCODE_DEBUG_SMOKE_TIMING=1 and is skipped by the usual
// `-run TestMachineSmokeJavaIdea3` command. Run it alone:
//
//	DEVELOPER_DIR=/Library/Developer/CommandLineTools CGO_ENABLED=1 \
//	  MINCODE_DEBUG_MACHINE_SMOKE=1 MINCODE_DEBUG_SMOKE_TIMING=1 \
//	  MINCODE_DEBUG_SMOKE_JAVA_HOME=/opt/homebrew/opt/openjdk@21 \
//	  MINCODE_DEBUG_SMOKE_JAVA_DEBUG_JAR=$PWD/../official-plugins/java-debug/lib/com.microsoft.java.debug.plugin-0.53.1.jar \
//	  go test ./backend/debug -run TestMachineSmokeJavaIdea3FirstDebugTiming -v -timeout 1800s
//
// Results: the "实现结论" section of
// docs/superpowers/specs/2026-11-09-java-debug-idea-phase3a-design.md.
func TestMachineSmokeJavaIdea3FirstDebugTiming(t *testing.T) {
	javaExec, jar := smokeJava(t)
	if os.Getenv("MINCODE_DEBUG_SMOKE_TIMING") != "1" {
		t.Skip("set MINCODE_DEBUG_SMOKE_TIMING=1 to run the first-debug timing smoke (about 5 minutes, needs Maven Central)")
	}
	runs := 3
	if value, err := strconv.Atoi(os.Getenv("MINCODE_DEBUG_SMOKE_TIMING_RUNS")); err == nil && value > 0 {
		runs = value
	}
	kinds := []string{"maven", "plain"}
	if value := os.Getenv("MINCODE_DEBUG_SMOKE_TIMING_KINDS"); value != "" {
		kinds = strings.Split(value, ",")
	}
	t.Setenv("JAVA_HOME", "")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	rec := captureEmits(t)
	t.Cleanup(func() { core.SetEmitter(nil) })
	t.Cleanup(func() { lsp.Shutdown(context.Background()) })
	withBundle := func() { lsp.SetJdtlsBundleProvider(func() []string { return []string{jar} }) }
	withoutBundle := func() { lsp.SetJdtlsBundleProvider(func() []string { return nil }) }
	t.Cleanup(func() { lsp.SetJdtlsBundleProvider(nil) })

	// The host re-hashes every bundle jar on each plugin_host_jdtls_bundles_status
	// (the frontend read it every second while waiting until Task 5; now on
	// jdtls's ServiceReady event, with a 500 ms fallback poll).
	if data, err := os.ReadFile(jar); err == nil {
		mark := time.Now()
		for i := 0; i < 3; i++ {
			sha256.Sum256(data)
		}
		t.Logf("java-debug jar %d bytes: sha256 %s per hash (from memory)", len(data), (time.Since(mark) / 3).Round(time.Microsecond))
	}

	// HOME holds the launcher's jdtls workspace data; on macOS the JVM's
	// user.home (where Maven keeps ~/.m2) ignores HOME, so it is set too:
	// the user's Maven repository is never read or written.
	setHome := func(dir string) {
		t.Setenv("HOME", dir)
		t.Setenv("JDK_JAVA_OPTIONS", "-Duser.home="+dir)
	}
	// MINCODE_DEBUG_SMOKE_TIMING_IDLE_MS: wait that long between ServiceReady
	// and the debug click on the cold paths (does the post-ServiceReady
	// work finish in the background?).
	var idle time.Duration
	if value, err := strconv.Atoi(os.Getenv("MINCODE_DEBUG_SMOKE_TIMING_IDLE_MS")); err == nil && value > 0 {
		idle = time.Duration(value) * time.Millisecond
	}
	var rows []firstDebugRow
	n := 0
	// Maven runs may stall on a download (no ServiceReady within
	// javaSmokeReadyTimeout): each stall is recorded as STALLED; after
	// firstDebugMaxStalls stalls the remaining Maven runs are skipped, so the
	// worst case stays near firstDebugMaxStalls × 3 min plus the normal runs.
	stalls := 0
	stallBudgetLeft := func(kind, path string, run int) bool {
		if kind != "maven" || stalls < firstDebugMaxStalls {
			return true
		}
		t.Logf("%s run %d: skipped after %d stalled Maven runs", path, run, stalls)
		return false
	}
	recordStall := func(row *firstDebugRow, root string) {
		stalls++
		t.Logf("%s run %d: %v", row.path, row.run, row.notes)
		rows = append(rows, *row)
		stopJdtlsLikeFrontend(t, root)
	}
	newRow := func(path string, run int) *firstDebugRow {
		return &firstDebugRow{path: path, run: run, stages: map[string]time.Duration{}}
	}
	finish := func(row *firstDebugRow) {
		row.sum()
		rows = append(rows, *row)
		t.Logf("%s run %d: total %s %v", row.path, row.run, row.stages["total"].Round(time.Millisecond), row.notes)
	}
	coldStart := func(path string, kind string, run int, knownMainClass string) {
		if !stallBudgetLeft(kind, path, run) {
			return
		}
		n++
		p := newFirstDebugProject(t, kind, n)
		row := newRow(path, run)
		row.allowStall = kind == "maven"
		withBundle()
		startJdtlsTimed(ctx, t, p, row)
		if row.stalled {
			recordStall(row, p.root)
			return
		}
		if bundles, running := lsp.JdtlsSessionBundles(p.root); !running || !slices.Contains(bundles, jar) {
			t.Fatalf("jdtls bundles %v (running %v), want %s", bundles, running, jar)
		}
		if idle > 0 {
			time.Sleep(idle)
			row.notes = append(row.notes, fmt.Sprintf("idle %s after ServiceReady before the debug click", idle))
		}
		debugOnceWith(ctx, t, rec, p, javaExec, knownMainClass, row)
		if kind == "maven" {
			files := 0
			_ = filepath.WalkDir(filepath.Join(os.Getenv("HOME"), ".m2"), func(_ string, entry os.DirEntry, err error) error {
				if err == nil && !entry.IsDir() {
					files++
				}
				return nil
			})
			row.notes = append(row.notes, fmt.Sprintf("~/.m2 holds %d files", files))
		}
		finish(row)
		stopJdtlsLikeFrontend(t, p.root)
	}

	for _, kind := range kinds {
		for run := 1; run <= runs; run++ {
			setHome(t.TempDir())
			coldStart("A cold "+kind, kind, run, "")
		}
		mainClass := "App"
		if kind == "maven" {
			mainClass = "demo.App"
		}
		for run := 1; run <= runs; run++ {
			setHome(t.TempDir())
			coldStart("A cold mainClass-set "+kind, kind, run, mainClass)
		}
		setHome(t.TempDir())
		if kind == "maven" {
			coldStart("warm-up (fills ~/.m2) "+kind, kind, 0, "")
			for run := 1; run <= runs; run++ {
				coldStart("A warm-m2 "+kind, kind, run, "")
			}
		}
		for run := 1; run <= runs; run++ {
			if !stallBudgetLeft(kind, "B restart "+kind, run) {
				continue
			}
			n++
			p := newFirstDebugProject(t, kind, n)
			withoutBundle()
			pre := newRow("B restart "+kind, run)
			pre.allowStall = kind == "maven"
			startJdtlsTimed(ctx, t, p, pre)
			if pre.stalled {
				pre.notes = append(pre.notes, "(the jdtls without the bundle, before the timed restart)")
				recordStall(pre, p.root)
				continue
			}
			// The project has been open a while: let jdtls finish its
			// background build and indexing before the debug click.
			time.Sleep(firstDebugSettle)
			if bundles, _ := lsp.JdtlsSessionBundles(p.root); len(bundles) != 0 {
				t.Fatalf("jdtls without the bundle has bundles %v", bundles)
			}
			row := newRow("B restart "+kind, run)
			row.allowStall = kind == "maven"
			withBundle()
			mark := time.Now()
			stopJdtlsLikeFrontend(t, p.root)
			row.stages["stop"] = time.Since(mark)
			startJdtlsTimed(ctx, t, p, row)
			if row.stalled {
				recordStall(row, p.root)
				continue
			}
			debugOnce(ctx, t, rec, p, javaExec, row)
			finish(row)
			steady := newRow("C steady "+kind, run)
			debugOnce(ctx, t, rec, p, javaExec, steady)
			finish(steady)
			stopJdtlsLikeFrontend(t, p.root)
		}
	}

	// The per-run table, then the medians per path.
	header := fmt.Sprintf("%-28s %3s", "path", "run")
	for _, stage := range firstDebugStages {
		header += fmt.Sprintf(" %10s", stage)
	}
	lines := []string{header}
	var paths []string
	for _, row := range rows {
		line := fmt.Sprintf("%-28s %3d", row.path, row.run)
		if row.stalled {
			lines = append(lines, line+"  STALLED (no ServiceReady in "+javaSmokeReadyTimeout.String()+", Maven download; see the log)")
			continue
		}
		for _, stage := range firstDebugStages {
			line += fmt.Sprintf(" %10s", formatStage(row.stages[stage]))
		}
		lines = append(lines, line)
		if !slices.Contains(paths, row.path) {
			paths = append(paths, row.path)
		}
	}
	lines = append(lines, "", "medians (ms):", header)
	for _, path := range paths {
		line := fmt.Sprintf("%-28s %3s", path, "med")
		for _, stage := range firstDebugStages {
			var values []time.Duration
			for _, row := range rows {
				if row.path == path && !row.stalled {
					values = append(values, row.stages[stage])
				}
			}
			line += fmt.Sprintf(" %10s", formatStage(medianDuration(values)))
		}
		lines = append(lines, line)
	}
	t.Logf("first-debug timing (ms):\n%s", strings.Join(lines, "\n"))

	// Loose sanity bounds only: every run reached the breakpoint (checked in
	// debugOnce) within a few minutes, and the steady state is not slower
	// than a cold start.
	for _, row := range rows {
		if row.stages["total"] > 5*time.Minute {
			t.Errorf("%s run %d took %s", row.path, row.run, row.stages["total"])
		}
	}
	for _, kind := range kinds {
		var cold, steady []time.Duration
		for _, row := range rows {
			switch row.path {
			case "A cold " + kind:
				if !row.stalled {
					cold = append(cold, row.stages["total"])
				}
			case "C steady " + kind:
				steady = append(steady, row.stages["total"])
			}
		}
		slower, compared := steadySlowerThanCold(cold, steady)
		if !compared {
			t.Logf("%s: no cold or no steady-state run (stalled or skipped): the medians are not compared", kind)
		} else if slower {
			t.Errorf("%s: steady-state median %s slower than the cold median %s", kind, medianDuration(steady), medianDuration(cold))
		}
	}
}

// jdtlsGeneration returns the generation of root's running jdtls
// (project_lsp_start answers the running session without restarting it).
func jdtlsGeneration(t *testing.T, root string) uint64 {
	t.Helper()
	out, err := lspDispatch(t, "project_lsp_start", map[string]any{"root": root, "serverId": "jdtls"})
	if err != nil {
		t.Fatalf("project_lsp_start: %v", err)
	}
	var status struct {
		Generation uint64 `json:"generation"`
		Running    bool   `json:"running"`
	}
	if err := json.Unmarshal([]byte(out), &status); err != nil || !status.Running || status.Generation == 0 {
		t.Fatalf("project_lsp_start = %s (%v)", out, err)
	}
	return status.Generation
}

// TestMachineSmokeJavaIdea3FirstDebugNoRestart: with the debugger's bundle
// already trusted (and its jar unchanged), the jdtls that opening a Java file
// starts is initialized with the bundle, so the first debug runs on that same
// jdtls (same generation, never stopped) and later debugs are fast. With the
// bundle not trusted yet, the file-open jdtls runs without it, and loading it
// needs the confirmed restart (Review Focus 5).
//
// The trust decision is the plugin host's (plugin.jdtlsBundlePaths, covered
// by TestTrustedBundleLoadedAtFirstJdtlsStartNeedsNoRestart and
// TestUntrustedOrChangedBundleStillNeedsConfirmAndRestart); the debug package
// cannot import plugin, so the provider here stands in for it with a trusted
// switch. What the frontend reads on the first debug is
// plugin_host_jdtls_bundles_status, whose `loaded` comes from
// lsp.JdtlsSessionBundles: asserted directly. A plain folder project keeps the
// smoke independent of Maven Central; HOME and user.home are temporary.
func TestMachineSmokeJavaIdea3FirstDebugNoRestart(t *testing.T) {
	javaExec, jar := smokeJava(t)
	t.Setenv("JAVA_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("JDK_JAVA_OPTIONS", "-Duser.home="+home)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	rec := captureEmits(t)
	t.Cleanup(func() { core.SetEmitter(nil) })
	t.Cleanup(func() { lsp.Shutdown(context.Background()) })
	var trusted sync.Mutex
	isTrusted := false
	lsp.SetJdtlsBundleProvider(func() []string {
		trusted.Lock()
		defer trusted.Unlock()
		if isTrusted {
			return []string{jar}
		}
		return nil
	})
	t.Cleanup(func() { lsp.SetJdtlsBundleProvider(nil) })
	setTrusted := func(value bool) {
		trusted.Lock()
		isTrusted = value
		trusted.Unlock()
	}
	loaded := func(root string) bool {
		bundles, running := lsp.JdtlsSessionBundles(root)
		return running && slices.Contains(bundles, jar)
	}

	// Trusted: open a Java file → jdtls starts with the bundle → debug twice.
	setTrusted(true)
	p := newFirstDebugProject(t, "plain", 1)
	startJdtlsTimed(ctx, t, p, &firstDebugRow{path: "file-open jdtls", stages: map[string]time.Duration{}})
	if !loaded(p.root) {
		t.Fatal("the file-open jdtls was started without the trusted bundle: the first debug would restart it")
	}
	generation := jdtlsGeneration(t, p.root)
	first := &firstDebugRow{path: "first debug", stages: map[string]time.Duration{}}
	debugOnce(ctx, t, rec, p, javaExec, first)
	first.sum()
	if got := jdtlsGeneration(t, p.root); got != generation || !loaded(p.root) {
		t.Fatalf("jdtls replaced by the first debug: generation %d → %d", generation, got)
	}
	second := &firstDebugRow{path: "second debug", stages: map[string]time.Duration{}}
	debugOnce(ctx, t, rec, p, javaExec, second)
	second.sum()
	if got := jdtlsGeneration(t, p.root); got != generation {
		t.Fatalf("jdtls replaced by the second debug: generation %d → %d", generation, got)
	}
	t.Logf("trusted bundle: first debug %s, second debug %s (host side: jdtls commands + DAP until the first stop), jdtls generation %d throughout",
		first.stages["total"].Round(time.Millisecond), second.stages["total"].Round(time.Millisecond), generation)
	if second.stages["total"] > 3*time.Second {
		t.Errorf("second debug took %s, want ≤ 3s", second.stages["total"])
	}
	stopJdtlsLikeFrontend(t, p.root)

	// Not trusted yet: the file-open jdtls runs without the jar; after the
	// user confirms, only a restart (new generation) loads it.
	setTrusted(false)
	p = newFirstDebugProject(t, "plain", 2)
	startJdtlsTimed(ctx, t, p, &firstDebugRow{path: "file-open jdtls (untrusted)", stages: map[string]time.Duration{}})
	if bundles, _ := lsp.JdtlsSessionBundles(p.root); len(bundles) != 0 {
		t.Fatalf("untrusted bundle loaded into the file-open jdtls: %v", bundles)
	}
	untrusted := jdtlsGeneration(t, p.root)
	setTrusted(true)
	if loaded(p.root) {
		t.Fatal("confirming must not change the running jdtls's bundles")
	}
	stopJdtlsLikeFrontend(t, p.root)
	startJdtlsTimed(ctx, t, p, &firstDebugRow{path: "restarted jdtls", stages: map[string]time.Duration{}})
	if restarted := jdtlsGeneration(t, p.root); restarted == untrusted || !loaded(p.root) {
		t.Fatalf("restart after the confirmation: generation %d → %d, loaded %v", untrusted, restarted, loaded(p.root))
	}
	debugOnce(ctx, t, rec, p, javaExec, &firstDebugRow{path: "debug after restart", stages: map[string]time.Duration{}})
	stopJdtlsLikeFrontend(t, p.root)
}
