package debug

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"mincode/app/backend/lsp"
)

// Phase 3b feasibility probe (throwaway spike, kept as a basis): running and
// debugging JUnit tests through Microsoft's vscode-java-test jdtls plugin
// (com.microsoft.java.test.plugin) and java-debug, the way the VS Code
// extension does. Same opt-in environment as TestMachineSmokeJava plus the
// unpacked VSIX's extension/server directory:
//
//	DEVELOPER_DIR=/Library/Developer/CommandLineTools CGO_ENABLED=1 \
//	  MINCODE_DEBUG_MACHINE_SMOKE=1 \
//	  MINCODE_DEBUG_SMOKE_JAVA_HOME=/opt/homebrew/opt/openjdk@21 \
//	  MINCODE_DEBUG_SMOKE_JAVA_DEBUG_JAR=$PWD/../official-plugins/java-debug/lib/com.microsoft.java.debug.plugin-0.53.1.jar \
//	  MINCODE_DEBUG_SMOKE_JAVA_TEST_DIR=/tmp/mincode-jt-probe/vsix/x/extension/server \
//	  MINCODE_DEBUG_SMOKE_JAVA_TEST_OUT=/tmp/mincode-jt-probe/out \
//	  go test ./backend/debug -run TestMachineSmokeJavaTestProbe -v -timeout 900s
//
// Raw payloads (discovery JSON, launch arguments, runner socket streams) are
// written to MINCODE_DEBUG_SMOKE_JAVA_TEST_OUT when set.

// javaTestBundleJars are package.json contributes.javaExtensions of
// vscode-java-test 0.47.0 (the order of the manifest).
var javaTestBundleJars = []string{
	"com.microsoft.java.test.plugin-0.43.1.jar",
	"junit-jupiter-api_5.14.4.jar",
	"junit-jupiter-api_6.0.1.jar",
	"junit-jupiter-engine_5.14.4.jar",
	"junit-jupiter-engine_6.0.1.jar",
	"junit-jupiter-migrationsupport_5.14.4.jar",
	"junit-jupiter-params_5.14.4.jar",
	"junit-jupiter-params_6.0.1.jar",
	"junit-platform-commons_1.14.4.jar",
	"junit-platform-commons_6.0.1.jar",
	"junit-platform-engine_1.14.4.jar",
	"junit-platform-engine_6.0.1.jar",
	"junit-platform-launcher_1.14.4.jar",
	"junit-platform-launcher_6.0.1.jar",
	"junit-platform-runner_1.14.4.jar",
	"junit-platform-suite-api_1.14.4.jar",
	"junit-platform-suite-api_6.0.1.jar",
	"junit-platform-suite-commons_1.14.4.jar",
	"junit-platform-suite-engine_1.14.4.jar",
	"junit-platform-suite-engine_6.0.1.jar",
	"junit-vintage-engine_5.14.4.jar",
	"org.apiguardian.api_1.1.2.jar",
	"org.eclipse.jdt.junit4.runtime_1.4.0.v20251113-1434.jar",
	"org.eclipse.jdt.junit5.runtime_1.2.0.v20251113-1434.jar",
	"org.eclipse.jdt.junit6.runtime_1.0.0.v20251112-1701.jar",
	"org.jacoco.core_0.8.15.202606040825.jar",
	"org.objectweb.asm.commons_9.10.1.jar",
	"org.objectweb.asm.tree_9.10.1.jar",
	"org.objectweb.asm_9.10.1.jar",
	"org.opentest4j_1.3.0.jar",
}

// Project classpath of the fixture: JUnit 5 from the VSIX, JUnit 4 and
// hamcrest from jdtls's own plugins directory (the VSIX has no junit4 jar).
var javaTestProjectJars = []string{
	"junit-jupiter-api_5.14.4.jar",
	"junit-jupiter-engine_5.14.4.jar",
	"junit-jupiter-params_5.14.4.jar",
	"junit-platform-commons_1.14.4.jar",
	"junit-platform-engine_1.14.4.jar",
	"junit-platform-launcher_1.14.4.jar",
	"org.opentest4j_1.3.0.jar",
	"org.apiguardian.api_1.1.2.jar",
}

const (
	probeCalcTest = "package demo;\n" +
		"\n" +
		"import static org.junit.jupiter.api.Assertions.*;\n" +
		"import static org.junit.jupiter.api.Assumptions.assumeTrue;\n" +
		"\n" +
		"import org.junit.jupiter.api.Disabled;\n" +
		"import org.junit.jupiter.api.DisplayName;\n" +
		"import org.junit.jupiter.api.Nested;\n" +
		"import org.junit.jupiter.api.Test;\n" +
		"import org.junit.jupiter.params.ParameterizedTest;\n" +
		"import org.junit.jupiter.params.provider.ValueSource;\n" +
		"\n" +
		"class CalcTest {\n" + // 13
		"    @Test\n" +
		"    void adds() {\n" + // 15
		"        int sum = Calc.add(2, 3);\n" + // 16  (debug breakpoint)
		"        assertEquals(5, sum);\n" +
		"    }\n" +
		"\n" +
		"    @Test\n" +
		"    void failing() {\n" + // 21
		"        assertEquals(6, Calc.add(2, 3), \"two plus three\");\n" +
		"    }\n" +
		"\n" +
		"    @Test\n" +
		"    void erroring() {\n" + // 26
		"        throw new IllegalStateException(\"boom\");\n" +
		"    }\n" +
		"\n" +
		"    @Test\n" +
		"    @Disabled(\"not today\")\n" +
		"    void skipped() {\n" + // 32
		"    }\n" +
		"\n" +
		"    @Test\n" +
		"    void assumed() {\n" + // 36
		"        assumeTrue(false, \"assumption\");\n" +
		"    }\n" +
		"\n" +
		"    @ParameterizedTest\n" +
		"    @ValueSource(ints = {1, 2, -3})\n" +
		"    @DisplayName(\"positive numbers\")\n" +
		"    void positive(int value) {\n" + // 43
		"        assertTrue(value > 0, \"value \" + value);\n" +
		"    }\n" +
		"\n" +
		"    @Nested\n" +
		"    class Inner {\n" + // 48
		"        @Test\n" +
		"        void innerPasses() {\n" + // 50
		"            assertEquals(4, Calc.add(2, 2));\n" +
		"        }\n" +
		"    }\n" +
		"}\n"
	probeLegacyTest = "package demo;\n" +
		"\n" +
		"import static org.junit.Assert.assertEquals;\n" +
		"\n" +
		"import org.junit.Ignore;\n" +
		"import org.junit.Test;\n" +
		"\n" +
		"public class LegacyTest {\n" +
		"    @Test\n" +
		"    public void passes() {\n" +
		"        assertEquals(5, Calc.add(2, 3));\n" +
		"    }\n" +
		"\n" +
		"    @Test\n" +
		"    public void fails() {\n" +
		"        assertEquals(\"legacy\", 6, Calc.add(2, 3));\n" +
		"    }\n" +
		"\n" +
		"    @Ignore(\"later\")\n" +
		"    @Test\n" +
		"    public void ignored() {\n" +
		"    }\n" +
		"}\n"
	probeCalc = "package demo;\n" +
		"\n" +
		"public class Calc {\n" +
		"    public static int add(int a, int b) {\n" +
		"        return a + b;\n" +
		"    }\n" +
		"}\n"
	probeBreakpointLine = 16
)

// javaTestItem is com.microsoft.java.test.plugin.model.JavaTestItem.
type javaTestItem struct {
	ID          string          `json:"id"`
	Label       string          `json:"label"`
	FullName    string          `json:"fullName"`
	Children    []javaTestItem  `json:"children"`
	TestLevel   int             `json:"testLevel"`
	TestKind    int             `json:"testKind"`
	ProjectName string          `json:"projectName"`
	JdtHandler  string          `json:"jdtHandler"`
	URI         string          `json:"uri"`
	Range       json.RawMessage `json:"range"`
	UniqueID    string          `json:"uniqueId"`
	NatureIDs   []string        `json:"natureIds"`
	SortText    string          `json:"sortText"`
}

func (item javaTestItem) walk(visit func(javaTestItem)) {
	visit(item)
	for _, child := range item.Children {
		child.walk(visit)
	}
}

// javaTestArgs is the body of vscode.java.test.junit.argument's Response.
type javaTestArgs struct {
	ProjectName      string   `json:"projectName"`
	MainClass        string   `json:"mainClass"`
	WorkingDirectory string   `json:"workingDirectory"`
	Classpath        []string `json:"classpath"`
	Modulepath       []string `json:"modulepath"`
	VMArguments      []string `json:"vmArguments"`
	ProgramArguments []string `json:"programArguments"`
}

type probeFixture struct {
	ctx    context.Context
	rec    *emitRecorder
	root   string
	test   string // CalcTest.java
	out    string // raw payload directory ("" = none)
	java   string
	server string // VSIX extension/server
}

func (f *probeFixture) save(t *testing.T, name string, data []byte) {
	t.Helper()
	if f.out == "" {
		return
	}
	if err := os.WriteFile(filepath.Join(f.out, name), data, 0o644); err != nil {
		t.Errorf("save %s: %v", name, err)
	}
}

func (f *probeFixture) saveJSON(t *testing.T, name string, v any) {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	f.save(t, name, data)
}

func copyProbeFile(t *testing.T, from, to string) {
	t.Helper()
	data, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeProbeProject writes an Eclipse project (.project/.classpath: jdtls
// imports it offline) with main and test (test=true) source folders and lib
// jars.
func writeProbeProject(t *testing.T, root, server string) {
	t.Helper()
	jdtls := filepath.Join(filepath.Dir(filepath.Dir(resolvedJdtls(t))), "libexec", "plugins")
	files := map[string]string{
		"src/main/java/demo/Calc.java":       probeCalc,
		"src/test/java/demo/CalcTest.java":   probeCalcTest,
		"src/test/java/demo/LegacyTest.java": probeLegacyTest,
		".project": `<?xml version="1.0" encoding="UTF-8"?>
<projectDescription><name>probe</name><comment></comment><projects></projects>
<buildSpec><buildCommand><name>org.eclipse.jdt.core.javabuilder</name><arguments></arguments></buildCommand></buildSpec>
<natures><nature>org.eclipse.jdt.core.javanature</nature></natures></projectDescription>
`,
		".settings/org.eclipse.jdt.core.prefs": "eclipse.preferences.version=1\n" +
			"org.eclipse.jdt.core.compiler.codegen.targetPlatform=17\n" +
			"org.eclipse.jdt.core.compiler.compliance=17\n" +
			"org.eclipse.jdt.core.compiler.source=17\n",
	}
	var classpath strings.Builder
	classpath.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<classpath>
	<classpathentry kind="src" path="src/main/java"/>
	<classpathentry kind="src" output="bin-test" path="src/test/java"><attributes><attribute name="test" value="true"/></attributes></classpathentry>
	<classpathentry kind="con" path="org.eclipse.jdt.launching.JRE_CONTAINER"/>
`)
	if err := os.MkdirAll(filepath.Join(root, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, jar := range javaTestProjectJars {
		copyProbeFile(t, filepath.Join(server, jar), filepath.Join(root, "lib", jar))
		fmt.Fprintf(&classpath, "\t<classpathentry kind=\"lib\" path=\"lib/%s\"><attributes><attribute name=\"test\" value=\"true\"/></attributes></classpathentry>\n", jar)
	}
	for _, prefix := range []string{"org.junit_4", "org.hamcrest_"} {
		matches, _ := filepath.Glob(filepath.Join(jdtls, prefix+"*.jar"))
		if len(matches) == 0 {
			t.Logf("no %s*.jar in %s: JUnit 4 part will not compile", prefix, jdtls)
			continue
		}
		name := filepath.Base(matches[0])
		copyProbeFile(t, matches[0], filepath.Join(root, "lib", name))
		fmt.Fprintf(&classpath, "\t<classpathentry kind=\"lib\" path=\"lib/%s\"><attributes><attribute name=\"test\" value=\"true\"/></attributes></classpathentry>\n", name)
	}
	classpath.WriteString("\t<classpathentry kind=\"output\" path=\"bin\"/>\n</classpath>\n")
	files[".classpath"] = classpath.String()
	for name, text := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func resolvedJdtls(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("jdtls")
	if err != nil {
		t.Skip(err)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// probeEnv returns the java-test server dir and the raw output dir.
func probeEnv(t *testing.T) (server, out string) {
	t.Helper()
	server = os.Getenv("MINCODE_DEBUG_SMOKE_JAVA_TEST_DIR")
	if server == "" {
		t.Skip("set MINCODE_DEBUG_SMOKE_JAVA_TEST_DIR to the vscode-java-test VSIX's extension/server directory")
	}
	for _, jar := range javaTestBundleJars {
		if _, err := os.Stat(filepath.Join(server, jar)); err != nil {
			t.Skipf("MINCODE_DEBUG_SMOKE_JAVA_TEST_DIR: %v", err)
		}
	}
	out = os.Getenv("MINCODE_DEBUG_SMOKE_JAVA_TEST_OUT")
	if out != "" {
		if err := os.MkdirAll(out, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return server, out
}

// isolateHome points HOME and the JVM's user.home at a fresh temp dir.
func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("JDK_JAVA_OPTIONS", "-Duser.home="+home)
	t.Setenv("JAVA_HOME", "")
	return home
}

// jdtlsLogLines returns lines of the host's jdtls log and of jdtls's
// workspace .log files under home that match one of the needles.
func jdtlsLogLines(t *testing.T, root, home string, needles ...string) []string {
	t.Helper()
	var text strings.Builder
	raw, _ := lspDispatch(t, "project_lsp_logs", map[string]any{"root": root, "serverId": "jdtls"})
	var logs struct {
		Output string `json:"output"`
	}
	_ = json.Unmarshal([]byte(raw), &logs)
	text.WriteString(logs.Output)
	_ = filepath.WalkDir(home, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && entry.Name() == ".log" {
			if data, err := os.ReadFile(path); err == nil {
				text.WriteString("\n--- " + path + "\n")
				text.Write(data)
			}
		}
		return nil
	})
	var lines []string
	for _, line := range strings.Split(text.String(), "\n") {
		lower := strings.ToLower(line)
		for _, needle := range needles {
			if strings.Contains(lower, strings.ToLower(needle)) {
				lines = append(lines, line)
				break
			}
		}
	}
	return lines
}

// waitClass waits until jdtls's build wrote rel under root.
func waitClass(ctx context.Context, t *testing.T, root, rel string) time.Duration {
	t.Helper()
	started := time.Now()
	for {
		if _, err := os.Stat(filepath.Join(root, rel)); err == nil {
			return time.Since(started)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("%s never built", rel)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// TestMachineSmokeJavaTestProbeBundleTiming compares cold jdtls start-ups
// (fresh HOME each) with java-debug only and with java-debug plus the 30
// java-test jars, and checks that the java-test commands are only there with
// the jars.
func TestMachineSmokeJavaTestProbeBundleTiming(t *testing.T) {
	_, jar := smokeJava(t)
	server, _ := probeEnv(t)
	t.Cleanup(func() { lsp.Shutdown(context.Background()) })
	t.Cleanup(func() { lsp.SetJdtlsBundleProvider(nil) })
	ctx, cancel := context.WithTimeout(context.Background(), javaSmokeTimeout)
	defer cancel()
	all := []string{jar}
	for _, name := range javaTestBundleJars {
		all = append(all, filepath.Join(server, name))
	}
	for run := 0; run < 2; run++ {
		for _, mode := range []struct {
			name    string
			bundles []string
		}{{"java-debug only", []string{jar}}, {"java-debug + 30 java-test jars", all}} {
			home := isolateHome(t)
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			writeProbeProject(t, root, server)
			bundles := mode.bundles
			lsp.SetJdtlsBundleProvider(func() []string { return bundles })
			started := time.Now()
			stop := startJdtls(ctx, t, root, filepath.Join(root, "src/test/java/demo/CalcTest.java"), probeCalcTest)
			ready := time.Since(started)
			built := waitClass(ctx, t, root, "bin-test/demo/CalcTest.class")
			loaded, _ := lsp.JdtlsSessionBundles(root)
			cmdCtx, cancelCmd := context.WithTimeout(ctx, 60*time.Second)
			_, cmdErr := lsp.ExecuteBuiltinCommand(cmdCtx, root, "jdtls", "vscode.java.test.findJavaProjects", json.RawMessage(fmt.Sprintf("[%q]", fileURL(root))))
			cancelCmd()
			t.Logf("run %d %s: ServiceReady %s, CalcTest.class +%s, %d bundles loaded, findJavaProjects err=%v",
				run, mode.name, ready.Round(time.Millisecond), built.Round(time.Millisecond), len(loaded), cmdErr)
			for _, line := range jdtlsLogLines(t, root, home, "bundle", "!MESSAGE", "!ENTRY") {
				t.Logf("  log: %s", line)
			}
			stop()
		}
	}
}

// TestMachineSmokeJavaTestProbe drives discovery, launch-argument
// resolution, run (noDebug) and debug of JUnit 5 / JUnit 4 tests, and
// records the RemoteTestRunner socket stream.
func TestMachineSmokeJavaTestProbe(t *testing.T) {
	javaExec, jar := smokeJava(t)
	server, out := probeEnv(t)
	home := isolateHome(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeProbeProject(t, root, server)
	ctx, cancel := context.WithTimeout(context.Background(), javaSmokeTimeout)
	defer cancel()
	rec := captureEmits(t)
	t.Cleanup(func() { lsp.Shutdown(context.Background()) })
	bundles := []string{jar}
	for _, name := range javaTestBundleJars {
		bundles = append(bundles, filepath.Join(server, name))
	}
	lsp.SetJdtlsBundleProvider(func() []string { return bundles })
	t.Cleanup(func() { lsp.SetJdtlsBundleProvider(nil) })
	f := &probeFixture{ctx: ctx, rec: rec, root: root, test: filepath.Join(root, "src/test/java/demo/CalcTest.java"), out: out, java: javaExec, server: server}
	started := time.Now()
	t.Cleanup(startJdtls(ctx, t, root, f.test, probeCalcTest))
	t.Logf("ServiceReady after %s", time.Since(started).Round(time.Millisecond))
	waitClass(ctx, t, root, "bin-test/demo/CalcTest.class")
	waitClass(ctx, t, root, "bin-test/demo/LegacyTest.class")
	for _, line := range jdtlsLogLines(t, root, home, "!MESSAGE", "error") {
		t.Logf("log: %s", line)
	}

	// 1. Discovery.
	raw := f.command(t, "vscode.java.test.findJavaProjects", fileURL(root))
	f.save(t, "findJavaProjects.json", raw)
	var projects []javaTestItem
	_ = json.Unmarshal(raw, &projects)
	var project javaTestItem
	for _, item := range projects {
		t.Logf("project %q level %d kind %d handler %q natures %v", item.Label, item.TestLevel, item.TestKind, item.JdtHandler, item.NatureIDs)
		if item.Label == "probe" {
			project = item
		}
	}
	if project.JdtHandler == "" {
		t.Fatalf("findJavaProjects found no `probe` project: %s", raw)
	}
	raw = f.command(t, "vscode.java.test.findTestPackagesAndTypes", project.JdtHandler)
	f.save(t, "findTestPackagesAndTypes.json", raw)
	t.Logf("findTestPackagesAndTypes: %s", raw)
	raw = f.command(t, "vscode.java.test.get.testpath", []string{fileURL(root)})
	f.save(t, "get.testpath.json", raw)
	t.Logf("get.testpath: %s", raw)

	methods := map[string]javaTestItem{} // "CalcTest#adds" → item
	classes := map[string]javaTestItem{}
	for _, file := range []string{"CalcTest", "LegacyTest"} {
		uri := fileURL(filepath.Join(root, "src/test/java/demo", file+".java"))
		raw = f.command(t, "vscode.java.test.findTestTypesAndMethods", uri)
		f.save(t, "findTestTypesAndMethods-"+file+".json", raw)
		var items []javaTestItem
		if err := json.Unmarshal(raw, &items); err != nil {
			t.Fatalf("decode findTestTypesAndMethods: %v (%s)", err, raw)
		}
		for _, item := range items {
			item.walk(func(it javaTestItem) {
				t.Logf("  %s: level %d kind %d id %q label %q range %s handler %q", file, it.TestLevel, it.TestKind, it.ID, it.Label, it.Range, it.JdtHandler)
				switch it.TestLevel {
				case 5:
					classes[it.FullName] = it
				case 6:
					methods[it.FullName] = it
				}
			})
		}
		raw = f.command(t, "vscode.java.test.resolvePath", uri)
		f.save(t, "resolvePath-"+file+".json", raw)
	}
	if c, ok := classes["demo.CalcTest"]; ok {
		raw = f.command(t, "vscode.java.test.findDirectTestChildrenForClass", c.JdtHandler)
		f.save(t, "findDirectTestChildrenForClass.json", raw)
	}
	keys := make([]string, 0, len(methods))
	for k := range methods {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	t.Logf("methods: %v", keys)

	// 2. Runs (noDebug). Names are those the extension sends: class level →
	// fullName, method level → jdtHandler.
	type run struct {
		name     string
		level    int
		kind     int
		names    []string
		noDebug  bool
		breakpnt bool
	}
	method := func(full string) string {
		item, ok := methods[full]
		if !ok {
			t.Fatalf("no discovered method %q (have %v)", full, keys)
		}
		return item.JdtHandler
	}
	runs := []run{
		{name: "junit5-class", level: 5, kind: 0, names: []string{"demo.CalcTest"}, noDebug: true},
		{name: "junit5-method-adds", level: 6, kind: 0, names: []string{method("demo.CalcTest#adds()")}, noDebug: true},
		{name: "junit5-rerun-failed", level: 6, kind: 0, names: []string{method("demo.CalcTest#failing()"), method("demo.CalcTest#erroring()")}, noDebug: true},
		{name: "junit5-parameterized", level: 6, kind: 0, names: []string{method("demo.CalcTest#positive(int)")}, noDebug: true},
		{name: "junit4-class", level: 5, kind: 1, names: []string{"demo.LegacyTest"}, noDebug: true},
		{name: "junit5-debug-adds", level: 6, kind: 0, names: []string{method("demo.CalcTest#adds()")}, noDebug: false, breakpnt: true},
	}
	for _, r := range runs {
		f.runTests(t, r.name, project.ProjectName, r.level, r.kind, r.names, r.noDebug, r.breakpnt)
	}
}

// command runs a java-test jdtls command and returns its raw result.
func (f *probeFixture) command(t *testing.T, command string, args ...any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	cmdCtx, cancel := context.WithTimeout(f.ctx, 60*time.Second)
	defer cancel()
	started := time.Now()
	result, err := lsp.ExecuteBuiltinCommand(cmdCtx, f.root, "jdtls", command, encoded)
	if err != nil {
		t.Fatalf("%s %s: %v", command, encoded, err)
	}
	t.Logf("%s (%s, %d bytes)", command, time.Since(started).Round(time.Millisecond), len(result))
	return result
}

// joinJavaArgs joins args like vscode-java-debug's concatArgs: quoted when
// containing a space or a double quote.
func joinJavaArgs(args []string) string {
	parts := make([]string, 0, len(args))
	for _, arg := range args {
		if strings.ContainsAny(arg, " \"") {
			arg = `"` + strings.ReplaceAll(arg, `"`, `\"`) + `"`
		}
		parts = append(parts, arg)
	}
	return strings.Join(parts, " ")
}

// runTests resolves the launch arguments, listens for the runner on
// 127.0.0.1, launches through java-debug and records the stream.
func (f *probeFixture) runTests(t *testing.T, name, projectName string, level, kind int, names []string, noDebug, breakpoint bool) {
	t.Helper()
	request, _ := json.Marshal(map[string]any{
		"projectName": projectName, "testLevel": level, "testKind": kind,
		"testNames": names, "testHandles": []string{},
	})
	raw := f.command(t, "vscode.java.test.junit.argument", string(request))
	var response struct {
		Body         *javaTestArgs `json:"body"`
		ErrorMessage string        `json:"errorMessage"`
		Status       any           `json:"status"`
	}
	if err := json.Unmarshal(raw, &response); err != nil || response.Body == nil {
		t.Fatalf("%s: junit.argument = %s (%v)", name, raw, err)
	}
	f.save(t, "junit.argument-"+name+".json", append(append(request, '\n'), raw...))
	args := response.Body
	t.Logf("%s: mainClass %s, %d classpath entries, vmArgs %v, programArgs %v", name, args.MainClass, len(args.Classpath), args.VMArguments, args.ProgramArguments)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	program := slices.Clone(args.ProgramArguments)
	if i := slices.Index(program, "-port"); i >= 0 && i+1 < len(program) {
		program[i+1] = fmt.Sprint(port)
	} else {
		program = append(program, "-port", fmt.Sprint(port))
	}
	var (
		streamMu sync.Mutex
		stream   strings.Builder
		done     = make(chan struct{})
	)
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		for {
			line, err := reader.ReadString('\n')
			streamMu.Lock()
			stream.WriteString(line)
			streamMu.Unlock()
			if err != nil {
				return
			}
		}
	}()

	var dapPort int
	jdtlsCommand(f.ctx, t, f.root, "vscode.java.startDebugSession", []any{}, &dapPort)
	s, err := ConnectAdapter(f.ctx, ConnectSpec{Port: dapPort, OwnerPluginID: "machine-smoke", DebuggerType: "java", ProjectRoot: f.root})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(name + " done")
	launch := map[string]any{
		"type": "java", "name": "Launch Java Tests - " + name, "request": "launch",
		"projectName": args.ProjectName, "cwd": args.WorkingDirectory,
		"mainClass": args.MainClass, "classPaths": args.Classpath, "modulePaths": nonNil(args.Modulepath),
		"vmArgs": joinJavaArgs(args.VMArguments), "args": joinJavaArgs(program),
		"noDebug": noDebug, "console": "internalConsole", "javaExec": f.java,
	}
	launchArgs, _ := json.Marshal(launch)
	f.save(t, "launch-"+name+".json", launchArgs)
	params := BeginParams{AdapterID: "java", Request: "launch", Arguments: launchArgs}
	if breakpoint {
		params.Breakpoints = map[string][]SourceBreakpoint{f.test: {{Line: probeBreakpointLine}}}
	}
	started := time.Now()
	if _, err := s.Begin(f.ctx, params); err != nil {
		t.Fatalf("%s: Begin: %v", name, err)
	}
	if breakpoint {
		stopped := f.rec.waitEvent(f.ctx, t, s.ID(), "stopped")
		var stop idea2Stop
		_ = json.Unmarshal(stopped.Body, &stop)
		var trace struct {
			StackFrames []idea3Frame `json:"stackFrames"`
		}
		smokeRequest(f.ctx, t, s, "stackTrace", map[string]any{"threadId": stop.ThreadID, "startFrame": 0, "levels": 3}, &trace)
		if len(trace.StackFrames) == 0 || trace.StackFrames[0].Line != probeBreakpointLine {
			t.Fatalf("%s: paused at %+v, want line %d", name, trace.StackFrames, probeBreakpointLine)
		}
		t.Logf("%s: paused (%s) in %s line %d of %s after %s", name, stop.Reason, trace.StackFrames[0].Name, trace.StackFrames[0].Line, trace.StackFrames[0].path(), time.Since(started).Round(time.Millisecond))
		streamMu.Lock()
		t.Logf("%s: stream while paused:\n%s", name, stream.String())
		streamMu.Unlock()
		smokeRequest(f.ctx, t, s, "continue", map[string]any{"threadId": stop.ThreadID}, nil)
	}
	select {
	case <-done:
	case <-time.After(120 * time.Second):
		t.Errorf("%s: runner stream not closed after 120s", name)
	}
	ended := f.rec.waitEvent(f.ctx, t, s.ID(), SessionEndedEvent)
	streamMu.Lock()
	text := stream.String()
	streamMu.Unlock()
	f.save(t, "stream-"+name+".txt", []byte(text))
	t.Logf("%s: %s after %s; runner stream (%d bytes):\n%s", name, SessionEndedEvent, time.Since(started).Round(time.Millisecond), len(text), text)
	if ended.Body != nil {
		t.Logf("%s: ended %s", name, ended.Body)
	}
	output := f.rec.output(s.ID())
	f.save(t, "output-"+name+".txt", []byte(output))
	if len(output) > 1500 {
		output = output[:1500] + "…"
	}
	t.Logf("%s: debuggee output:\n%s", name, output)
	if !strings.Contains(text, "%TESTC") {
		t.Errorf("%s: no %%TESTC in the runner stream", name)
	}
}
