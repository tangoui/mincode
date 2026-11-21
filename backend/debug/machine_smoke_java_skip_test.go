package debug

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"mincode/app/backend/lsp"
)

// TestMachineSmokeJavaExceptionSkip checks exception breakpoints with the
// java-debug plugin's exceptionFilters.skipClasses (its step filter
// patterns, sent through vscode.java.updateDebugSettings exactly as
// debug.applySettings does) against a real jdtls and the patched java-debug
// jar (scripts/exception-skip-patch.mjs; same opt-in environment as
// TestMachineSmokeJava).
//
// The program catches a ClassNotFoundException thrown inside the JDK
// (Class.forName), catches its own IllegalStateException, then dies of an
// uncaught NumberFormatException thrown inside the JDK (Integer.parseInt).
// Pauses, as exceptionId/breakMode:
//   - caught + uncaught, no skip: all three;
//   - caught + uncaught, skip: the own ISE and the uncaught NFE (upstream
//     0.53.1 drops the NFE too: its one exception request carries the
//     filters for both);
//   - uncaught only, skip: the NFE (upstream: nothing); also without skip;
//   - caught only, skip: the own ISE;
//   - exceptionTypes [java.lang.NumberFormatException], caught + uncaught,
//     skip: the NFE (the per-type request path).
func TestMachineSmokeJavaExceptionSkip(t *testing.T) {
	javaExec, jar := smokeJava(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("JAVA_HOME", "")
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "src", "SkipApp.java")
	text := "public class SkipApp {\n" +
		"    public static void main(String[] args) throws Exception {\n" +
		"        int ready = 1;\n" +
		"        try { Class.forName(\"no.such.Klass\"); } catch (ClassNotFoundException e) { System.out.println(\"cnfe\"); }\n" +
		"        try { throw new IllegalStateException(\"own\"); } catch (IllegalStateException e) { System.out.println(\"own\"); }\n" +
		"        Integer.parseInt(\"x\" + ready);\n" +
		"    }\n" +
		"}\n"
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
	jdtlsCommand(ctx, t, root, "vscode.java.resolveClasspath", []any{"SkipApp", ""}, &classpaths)
	args, _ := json.Marshal(map[string]any{"type": "java", "mainClass": "SkipApp", "classPaths": classpaths[1], "modulePaths": nonNil(classpaths[0]), "javaExec": javaExec, "cwd": root, "console": "internalConsole"})

	update := func(types, skip []string) {
		t.Helper()
		filters := map[string]any{"exceptionTypes": types, "skipClasses": skip}
		settings, _ := json.Marshal(map[string]any{"logLevel": "WARN", "showLogicalStructure": true, "showToString": true, "exceptionFilters": filters})
		var ignored json.RawMessage
		jdtlsCommand(ctx, t, root, "vscode.java.updateDebugSettings", []any{string(settings)}, &ignored)
	}
	run := func(name string, filters []string) []string {
		t.Helper()
		var port int
		jdtlsCommand(ctx, t, root, "vscode.java.startDebugSession", []any{}, &port)
		s, err := ConnectAdapter(ctx, ConnectSpec{Port: port, OwnerPluginID: "machine-smoke", DebuggerType: "java", ProjectRoot: root})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close(name + " cleanup") })
		if _, err := s.Begin(ctx, BeginParams{AdapterID: "java", Request: "launch", Arguments: args, ExceptionFilters: filters}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var got []string
		thread := 0
		for n := 1; n <= 6; n++ {
			if thread != 0 {
				smokeRequest(ctx, t, s, "continue", map[string]any{"threadId": thread}, nil)
			}
			waitCtx, cancelWait := context.WithTimeout(ctx, smokePauseTimeout)
			ev, ended := rec.waitNthEvent(waitCtx, t, s.ID(), "stopped", n)
			cancelWait()
			if ended {
				break
			}
			var body struct {
				ThreadID int `json:"threadId"`
			}
			_ = json.Unmarshal(ev.Body, &body)
			thread = body.ThreadID
			var info struct {
				ExceptionID string `json:"exceptionId"`
				BreakMode   string `json:"breakMode"`
			}
			smokeRequest(ctx, t, s, "exceptionInfo", map[string]any{"threadId": thread}, &info)
			got = append(got, info.ExceptionID+"/"+info.BreakMode)
		}
		t.Logf("%s: pauses %v", name, got)
		return got
	}

	// The plugin's default stepFilters.skipClasses.
	skip := []string{"java.*", "javax.*", "jdk.*", "sun.*", "com.sun.*", "org.springframework.*", "org.apache.tomcat.*", "org.apache.catalina.*", "org.apache.coyote.*", "*$$*"}
	none := []string{}
	const (
		cnfe = "java.lang.ClassNotFoundException/always"
		ise  = "java.lang.IllegalStateException/always"
		nfe  = "java.lang.NumberFormatException/userUnhandled"
	)
	for _, c := range []struct {
		name          string
		types, skip   []string
		filters, want []string
	}{
		{"caught+uncaught, no skip", none, none, []string{"caught", "uncaught"}, []string{cnfe, ise, nfe}},
		{"caught+uncaught, skip", none, skip, []string{"caught", "uncaught"}, []string{ise, nfe}},
		{"uncaught only, skip", none, skip, []string{"uncaught"}, []string{nfe}},
		{"uncaught only, no skip", none, none, []string{"uncaught"}, []string{nfe}},
		{"caught only, skip", none, skip, []string{"caught"}, []string{ise}},
		{"NFE type, caught+uncaught, skip", []string{"java.lang.NumberFormatException"}, skip, []string{"caught", "uncaught"}, []string{nfe}},
	} {
		update(c.types, c.skip)
		if got := run(c.name, c.filters); !slices.Equal(got, c.want) {
			t.Errorf("%s: pauses %v, want %v", c.name, got, c.want)
		}
	}
	update(none, none)
}
