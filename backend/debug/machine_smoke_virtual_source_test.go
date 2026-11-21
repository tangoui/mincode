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

// TestMachineSmokeJavaVirtualSource stops inside a lambda that a JDK method
// calls, so the stack holds JDK frames, and checks what the editor relies on
// for them: java-debug reports a non-file path with a sourceReference, and the
// DAP source request returns the class's text (attached source or
// decompiled). Same opt-in environment as TestMachineSmokeJava.
func TestMachineSmokeJavaVirtualSource(t *testing.T) {
	javaExec, jar := smokeJava(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("JAVA_HOME", "")
	root := t.TempDir()
	source := filepath.Join(root, "src", "SrcApp.java")
	text := "import java.util.List;\n\npublic class SrcApp {\n    public static void main(String[] args) {\n        List.of(1, 2).forEach(n -> {\n            System.out.println(\"n=\" + n);\n        });\n    }\n}\n"
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
	source = filepath.Join(root, "src", "SrcApp.java")
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
	jdtlsCommand(ctx, t, root, "vscode.java.resolveClasspath", []any{"SrcApp", ""}, &classpaths)
	var port int
	jdtlsCommand(ctx, t, root, "vscode.java.startDebugSession", []any{}, &port)
	s, err := ConnectAdapter(ctx, ConnectSpec{Port: port, OwnerPluginID: "machine-smoke", DebuggerType: "java", ProjectRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close("virtual source smoke cleanup") })
	args, _ := json.Marshal(map[string]any{"type": "java", "mainClass": "SrcApp", "classPaths": classpaths[1], "modulePaths": nonNil(classpaths[0]), "javaExec": javaExec, "cwd": root, "console": "internalConsole"})
	if _, err := s.Begin(ctx, BeginParams{AdapterID: "java", Request: "launch", Arguments: args, Breakpoints: map[string][]SourceBreakpoint{source: {{Line: 6}}}}); err != nil {
		t.Fatal(err)
	}
	stopped := rec.waitEvent(ctx, t, s.ID(), "stopped")
	var thread struct {
		ThreadID int `json:"threadId"`
	}
	_ = json.Unmarshal(stopped.Body, &thread)
	var trace struct {
		StackFrames []struct {
			Name   string `json:"name"`
			Line   int    `json:"line"`
			Source *struct {
				Name            string `json:"name"`
				Path            string `json:"path"`
				SourceReference int    `json:"sourceReference"`
			} `json:"source"`
		} `json:"stackFrames"`
	}
	smokeRequest(ctx, t, s, "stackTrace", map[string]any{"threadId": thread.ThreadID, "startFrame": 0, "levels": 20}, &trace)
	var found bool
	for _, frame := range trace.StackFrames {
		if frame.Source == nil || !strings.HasPrefix(frame.Source.Path, "jdt://") {
			continue
		}
		result, err := lspDispatch(t, "project_lsp_jdt_contents", map[string]any{"root": root, "uri": frame.Source.Path})
		if err != nil {
			t.Fatalf("jdt:// source %s: %v", frame.Source.Path, err)
		}
		var content string
		if err := json.Unmarshal([]byte(result), &content); err != nil {
			t.Fatalf("decode jdt:// source: %v (%s)", err, result)
		}
		if !strings.Contains(content, "class ") && !strings.Contains(content, "interface ") {
			t.Fatalf("source of %s = %.200q, want Java class text", frame.Name, content)
		}
		t.Logf("JDK frame %s line %d: source %q returned %d characters", frame.Name, frame.Line, frame.Source.Path, len(content))
		found = true
		break
	}
	if !found {
		t.Fatalf("no JDK jdt:// frame on the stack: %+v", trace.StackFrames)
	}
	smokeRequest(ctx, t, s, "continue", map[string]any{"threadId": thread.ThreadID}, nil)
}
