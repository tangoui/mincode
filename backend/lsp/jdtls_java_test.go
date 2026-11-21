package lsp

import (
	"archive/zip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeJdtlsInstall lays out <prefix>/bin/jdtls and <prefix>/plugins with one
// jar per manifest (file name => MANIFEST.MF text) and returns the launcher.
func fakeJdtlsInstall(t *testing.T, manifests map[string]string) string {
	t.Helper()
	prefix := t.TempDir()
	launcher := filepath.Join(prefix, "bin", "jdtls")
	writeExecutable(t, launcher, "#!/bin/sh\nexit 0\n")
	plugins := filepath.Join(prefix, "plugins")
	if err := os.MkdirAll(plugins, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, manifest := range manifests {
		writeJar(t, filepath.Join(plugins, name), manifest)
	}
	return launcher
}

func writeJar(t *testing.T, path, manifest string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	entry, err := archive.Create("META-INF/MANIFEST.MF")
	if err == nil {
		_, err = entry.Write([]byte(manifest))
	}
	if err == nil {
		err = archive.Close()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
}

func writeExecutable(t *testing.T, path, script string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// fakeJavaHome returns a JDK home whose bin/java prints version (as
// `java -version` does, on stderr) and appends a line to the returned marker
// file on every run.
func fakeJavaHome(t *testing.T, version string) (home, marker string) {
	t.Helper()
	home = t.TempDir()
	marker = filepath.Join(t.TempDir(), "runs")
	writeExecutable(t, filepath.Join(home, "bin", "java"),
		"#!/bin/sh\necho run >> '"+marker+"'\necho 'openjdk version \""+version+"\" 2025-01-01' >&2\n")
	return home, marker
}

func probeRuns(t *testing.T, marker string) int {
	t.Helper()
	data, err := os.ReadFile(marker)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "run\n")
}

func resetJdtlsJavaCaches(t *testing.T) {
	t.Helper()
	clear := func() {
		jdtlsJavaCache.Lock()
		jdtlsJavaCache.required = map[string]jdtlsRequiredEntry{}
		jdtlsJavaCache.homes = map[string]javaHomeEntry{}
		jdtlsJavaCache.Unlock()
	}
	clear()
	t.Cleanup(clear)
}

// manifest folds a header the way jar manifests do (72-byte lines).
func manifest(headers ...string) string {
	var b strings.Builder
	b.WriteString("Manifest-Version: 1.0\r\n")
	for _, header := range headers {
		for len(header) > 70 {
			b.WriteString(header[:70] + "\r\n ")
			header = header[70:]
		}
		b.WriteString(header + "\r\n")
	}
	return b.String()
}

func TestJdtlsRequiredJavaReadsBundleManifests(t *testing.T) {
	resetJdtlsJavaCaches(t)
	launcher := fakeJdtlsInstall(t, map[string]string{
		"org.eclipse.jdt.ls.core_1.63.0.jar": manifest(`Require-Capability: osgi.ee;filter:="(&(osgi.ee=JavaSE)(version=21))"`),
		"org.eclipse.jdt.core_3.48.0.jar":    manifest("Bundle-RequiredExecutionEnvironment: JavaSE-25"),
		// Not one of the bundles the minimum is read from.
		"org.eclipse.jdt.core.manipulation_1.26.0.jar": manifest("Bundle-RequiredExecutionEnvironment: JavaSE-99"),
	})
	if got := jdtlsRequiredJava(launcher); got != 25 {
		t.Fatalf("required Java = %d, want 25", got)
	}

	// Homebrew: bin/jdtls is a wrapper (here a symlink) of libexec/bin/jdtls.
	prefix := t.TempDir()
	writeExecutable(t, filepath.Join(prefix, "libexec", "bin", "jdtls"), "#!/bin/sh\n")
	if err := os.MkdirAll(filepath.Join(prefix, "libexec", "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeJar(t, filepath.Join(prefix, "libexec", "plugins", "org.eclipse.jdt.ls.core_1.0.jar"),
		manifest(`Require-Capability: osgi.ee;filter:="(&(osgi.ee=JavaSE)(version=1.8))"`, "Bundle-RequiredExecutionEnvironment: JavaSE-11, JavaSE-17"))
	wrapper := writeExecutable(t, filepath.Join(prefix, "bin", "jdtls"), "#!/bin/sh\n")
	link := filepath.Join(t.TempDir(), "jdtls")
	if err := os.Symlink(wrapper, link); err != nil {
		t.Fatal(err)
	}
	if got := jdtlsRequiredJava(link); got != 11 {
		t.Fatalf("Homebrew layout required Java = %d, want 11 (1.8 capability, lowest BREE alternative 11)", got)
	}

	// Nothing readable: the launcher's own floor.
	if got := jdtlsRequiredJava(fakeJdtlsInstall(t, nil)); got != jdtlsLauncherMinJava {
		t.Fatalf("unknown install required Java = %d, want %d", got, jdtlsLauncherMinJava)
	}
	if got := jdtlsRequiredJava(""); got != jdtlsLauncherMinJava {
		t.Fatalf("no executable: %d", got)
	}
}

func TestParseJavaMajorVersion(t *testing.T) {
	for output, want := range map[string]int{
		`openjdk version "21.0.5" 2024-10-15`:                            21,
		"Picked up JAVA_TOOL_OPTIONS: -Dx=1\njava version \"1.8.0_392\"": 8,
		`openjdk version "27-ea" 2026-09-15`:                             27,
		`openjdk version "25" 2025-09-16`:                                25,
		`no version here`:                                                0,
	} {
		if got := parseJavaMajorVersion(output); got != want {
			t.Errorf("parseJavaMajorVersion(%q) = %d, want %d", output, got, want)
		}
	}
}

func TestJdtlsEnvDropsTooOldJavaHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell scripts as fake java executables")
	}
	resetJdtlsJavaCaches(t)
	launcher := fakeJdtlsInstall(t, map[string]string{
		"org.eclipse.jdt.core_3.48.0.jar": manifest("Bundle-RequiredExecutionEnvironment: JavaSE-25"),
	})
	jdtls := serverDefinition{ID: jdtlsServerID}
	var logs []string
	log := func(message string) { logs = append(logs, message) }

	// Too old: dropped, logged without paths, probed once per home.
	old, oldRuns := fakeJavaHome(t, "21.0.5")
	t.Setenv("JAVA_HOME", old)
	for range 2 {
		if env := builtinServerEnv(jdtls, launcher, log); hasEnv(env, "JAVA_HOME") {
			t.Fatalf("JAVA_HOME of Java 21 kept for a jdtls requiring 25")
		}
	}
	if runs := probeRuns(t, oldRuns); runs != 1 {
		t.Fatalf("java -version ran %d times, want 1 (cached per home)", runs)
	}
	if len(logs) != 2 || !strings.Contains(logs[0], "JAVA_HOME") || !strings.Contains(logs[0], "java=21") || !strings.Contains(logs[0], "required=25") || strings.Contains(logs[0], old) {
		t.Fatalf("logs = %q", logs)
	}
	// A changed java binary is probed again.
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(old, "bin", "java"), future, future); err != nil {
		t.Fatal(err)
	}
	builtinServerEnv(jdtls, launcher, nil)
	if runs := probeRuns(t, oldRuns); runs != 2 {
		t.Fatalf("java -version ran %d times after the binary changed, want 2", runs)
	}

	// Other servers never probe nor lose JAVA_HOME.
	if env := builtinServerEnv(serverDefinition{ID: "kotlin-ls"}, launcher, log); envValue(env, "JAVA_HOME") != old {
		t.Fatal("JAVA_HOME of another server changed")
	}

	// New enough: kept.
	logs = nil
	current, _ := fakeJavaHome(t, "25.0.1")
	t.Setenv("JAVA_HOME", current)
	if env := builtinServerEnv(jdtls, launcher, log); envValue(env, "JAVA_HOME") != current || len(logs) != 0 {
		t.Fatalf("JAVA_HOME of Java 25 not kept (logs %q)", logs)
	}

	// Unset: unchanged.
	t.Setenv("JAVA_HOME", "")
	os.Unsetenv("JAVA_HOME")
	if env := builtinServerEnv(jdtls, launcher, log); hasEnv(env, "JAVA_HOME") {
		t.Fatal("JAVA_HOME appeared")
	}

	// Relative JAVA_HOME: never probed, kept as is (the probe runs from the
	// temp directory, which would make it name another java).
	cwd := t.TempDir()
	_, relativeRuns := fakeJavaHome(t, "17.0.1")
	writeExecutable(t, filepath.Join(cwd, "jdk", "bin", "java"),
		"#!/bin/sh\necho run >> '"+relativeRuns+"'\necho 'openjdk version \"17.0.1\"' >&2\n")
	t.Chdir(cwd)
	for _, relative := range []string{"jdk", "./jdk"} {
		t.Setenv("JAVA_HOME", relative)
		if env := builtinServerEnv(jdtls, launcher, log); envValue(env, "JAVA_HOME") != relative {
			t.Fatalf("relative JAVA_HOME %q changed", relative)
		}
		if javaHomeMajor(relative) != 0 {
			t.Fatalf("relative JAVA_HOME %q probed", relative)
		}
	}
	if runs := probeRuns(t, relativeRuns); runs != 0 {
		t.Fatalf("relative JAVA_HOME java ran %d times", runs)
	}

	// Version unknown (java fails, no java, no version line): kept.
	broken := t.TempDir()
	writeExecutable(t, filepath.Join(broken, "bin", "java"), "#!/bin/sh\nexit 1\n")
	silent := t.TempDir()
	writeExecutable(t, filepath.Join(silent, "bin", "java"), "#!/bin/sh\necho hello\n")
	for _, home := range []string{broken, silent, t.TempDir()} {
		t.Setenv("JAVA_HOME", home)
		if env := builtinServerEnv(jdtls, launcher, log); envValue(env, "JAVA_HOME") != home {
			t.Fatalf("JAVA_HOME %s with an unknown version not kept", home)
		}
	}
}

func TestJdtlsJavaProbeSkipsMacOSStub(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the /usr/bin/java installer stub exists on macOS only")
	}
	resetJdtlsJavaCaches(t)
	if got := javaHomeMajor("/usr"); got != 0 {
		t.Fatalf("/usr/bin/java probed: %d", got)
	}
}

func hasEnv(env []string, name string) bool {
	for _, entry := range env {
		if strings.HasPrefix(entry, name+"=") {
			return true
		}
	}
	return false
}
