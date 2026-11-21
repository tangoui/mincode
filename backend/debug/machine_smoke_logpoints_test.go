package debug

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mincode/app/backend/lsp"
)

// Opt-in proof against real debugpy: the first stop must be the ordinary
// breakpoint AFTER the logpoint, while its interpolated text reaches output.
func TestMachineSmokePythonLogpoint(t *testing.T) {
	python := smokePython(t)
	root := t.TempDir()
	file := filepath.Join(root, "logpoints.py")
	if err := os.WriteFile(file, []byte("value = 7\nvalue += 1\ndone = value\nprint('done', done)\n"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	file = filepath.Join(root, "logpoints.py")
	ctx, cancel := context.WithTimeout(context.Background(), machineSmokeTimeout)
	defer cancel()
	rec := captureEmits(t)
	s, err := StartAdapter(ctx, AdapterSpec{Kind: "stdio", Executable: python, Args: []string{"-m", "debugpy.adapter"}, ProjectRoot: root, OwnerPluginID: "machine-smoke", DebuggerType: "python"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			rec.logAll(t)
		}
	})
	args, _ := json.Marshal(map[string]any{"type": "python", "program": file, "console": "internalConsole", "justMyCode": true})
	caps, err := s.Begin(ctx, BeginParams{AdapterID: "debugpy", Request: "launch", Arguments: args, Breakpoints: map[string][]SourceBreakpoint{file: {{Line: 2, LogMessage: "LOG value={value}"}, {Line: 3}}}})
	if err != nil {
		t.Fatal(err)
	}
	var capability struct {
		SupportsLogPoints bool `json:"supportsLogPoints"`
	}
	_ = json.Unmarshal(caps, &capability)
	if !capability.SupportsLogPoints {
		t.Fatal("debugpy did not advertise logpoint support")
	}
	stopped := rec.waitEvent(ctx, t, s.ID(), "stopped")
	var stop struct {
		ThreadID int `json:"threadId"`
	}
	_ = json.Unmarshal(stopped.Body, &stop)
	var trace struct {
		StackFrames []struct {
			Line int `json:"line"`
		} `json:"stackFrames"`
	}
	smokeRequest(ctx, t, s, "stackTrace", map[string]any{"threadId": stop.ThreadID}, &trace)
	if len(trace.StackFrames) == 0 || trace.StackFrames[0].Line != 3 {
		t.Fatalf("logpoint paused the program or ordinary breakpoint failed: %+v", trace)
	}
	smokeRequest(ctx, t, s, "continue", map[string]any{"threadId": stop.ThreadID}, nil)
	rec.waitEvent(ctx, t, s.ID(), SessionEndedEvent)
	rec.mu.Lock()
	var output strings.Builder
	for _, ev := range rec.events {
		if ev.SessionID == s.ID() && ev.Event == "output" {
			var body struct {
				Output string `json:"output"`
			}
			_ = json.Unmarshal(ev.Body, &body)
			output.WriteString(body.Output)
		}
	}
	text := output.String()
	rec.mu.Unlock()
	if !strings.Contains(text, "LOG value=7") || !strings.Contains(text, "done 8") {
		t.Fatalf("missing interpolated logpoint/program output: %q", text)
	}
}

// Reuses the isolated jdtls machine harness without changing its four-mode test.
func TestMachineSmokeJavaLogpoint(t *testing.T) {
	javaExec, jar := smokeJava(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("JAVA_HOME", "")
	root := t.TempDir()
	source := filepath.Join(root, "src", "LogApp.java")
	text := "public class LogApp {\n public static void main(String[] args) {\n  int value = 7;\n  value++;\n  int done = value;\n  System.out.println(\"done \" + done);\n }\n}\n"
	if err := os.MkdirAll(filepath.Dir(source), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	source = filepath.Join(root, "src", "LogApp.java")
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
	jdtlsCommand(ctx, t, root, "vscode.java.resolveClasspath", []any{"LogApp", ""}, &classpaths)
	var port int
	jdtlsCommand(ctx, t, root, "vscode.java.startDebugSession", []any{}, &port)
	s, err := ConnectAdapter(ctx, ConnectSpec{Port: port, OwnerPluginID: "machine-smoke", DebuggerType: "java", ProjectRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close("logpoint smoke cleanup") })
	args, _ := json.Marshal(map[string]any{"type": "java", "mainClass": "LogApp", "classPaths": classpaths[1], "modulePaths": nonNil(classpaths[0]), "javaExec": javaExec, "cwd": root, "console": "internalConsole"})
	caps, err := s.Begin(ctx, BeginParams{AdapterID: "java", Request: "launch", Arguments: args, Breakpoints: map[string][]SourceBreakpoint{source: {{Line: 4, LogMessage: "LOG value={value}"}, {Line: 5}}}})
	if err != nil {
		t.Fatal(err)
	}
	var capability struct {
		SupportsLogPoints bool `json:"supportsLogPoints"`
	}
	_ = json.Unmarshal(caps, &capability)
	if !capability.SupportsLogPoints {
		t.Fatal("java-debug did not advertise logpoint support")
	}
	stopped := rec.waitEvent(ctx, t, s.ID(), "stopped")
	var thread struct {
		ThreadID int `json:"threadId"`
	}
	_ = json.Unmarshal(stopped.Body, &thread)
	var trace struct {
		StackFrames []struct {
			Line int `json:"line"`
		} `json:"stackFrames"`
	}
	smokeRequest(ctx, t, s, "stackTrace", map[string]any{"threadId": thread.ThreadID}, &trace)
	if len(trace.StackFrames) == 0 || trace.StackFrames[0].Line != 5 {
		t.Fatalf("logpoint stopped or ordinary breakpoint failed: %+v", trace)
	}
	smokeRequest(ctx, t, s, "continue", map[string]any{"threadId": thread.ThreadID}, nil)
	rec.waitEvent(ctx, t, s.ID(), SessionEndedEvent)
	output := rec.output(s.ID())
	if !strings.Contains(output, "LOG value=7") || !strings.Contains(output, "done 8") {
		t.Fatalf("missing interpolated Java logpoint or program output: %q", output)
	}
}
