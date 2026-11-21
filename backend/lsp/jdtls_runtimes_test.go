package lsp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fakeJDK lays out a JDK home with bin/java and a release file (none when
// version is "") and returns the home.
func fakeJDK(t *testing.T, version string) string {
	t.Helper()
	home := t.TempDir()
	writeExecutable(t, filepath.Join(home, "bin", javaExecutableName()), "#!/bin/sh\nexit 1\n")
	if version != "" {
		release := "IMPLEMENTOR=\"Test\"\nJAVA_VERSION=\"" + version + "\"\nOS_NAME=\"Darwin\"\n"
		if err := os.WriteFile(filepath.Join(home, "release"), []byte(release), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func javaOf(home string) string { return filepath.Join(home, "bin", javaExecutableName()) }

func TestJavaRuntimesFromInstalledJDKs(t *testing.T) {
	jdk8, jdk11, jdk17, jdk21, jdk25, jdk27 := fakeJDK(t, "1.8.0_392"), fakeJDK(t, "11.0.2"), fakeJDK(t, "17.0.12"), fakeJDK(t, "21.0.5"), fakeJDK(t, "25"), fakeJDK(t, "27-ea")
	otherJdk21, noRelease := fakeJDK(t, "21.0.1"), fakeJDK(t, "")

	// A symlinked java (Homebrew opt/ links) names the resolved JDK home.
	linked := filepath.Join(t.TempDir(), "opt", "openjdk@17")
	if err := os.MkdirAll(filepath.Join(linked, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(javaOf(jdk17), javaOf(linked)); err != nil {
		t.Fatal(err)
	}
	realJdk17, _ := filepath.EvalSymlinks(jdk17)

	got := javaRuntimes(jdk11, []string{javaOf(jdk25), javaOf(otherJdk21), javaOf(jdk21), javaOf(linked), javaOf(noRelease), javaOf(jdk8), javaOf(jdk27), javaOf(jdk11)})
	want := []jdtlsRuntime{
		{Name: "JavaSE-11", Path: resolved(t, jdk11)}, // JAVA_HOME first
		{Name: "JavaSE-25", Path: resolved(t, jdk25)},
		{Name: "JavaSE-21", Path: resolved(t, otherJdk21), Default: true}, // first JDK of a version wins
		{Name: "JavaSE-17", Path: realJdk17},
		{Name: "JavaSE-1.8", Path: resolved(t, jdk8)},
		{Name: "JavaSE-27", Path: resolved(t, jdk27)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("runtimes =\n%+v\nwant\n%+v", got, want)
	}

	// Without 21, the newest LTS is the default; a non-LTS never is.
	got = javaRuntimes("", []string{javaOf(jdk17), javaOf(jdk25), javaOf(jdk27)})
	if defaults := defaultRuntimes(got); !reflect.DeepEqual(defaults, []string{"JavaSE-25"}) {
		t.Fatalf("default = %v (%+v)", defaults, got)
	}
	got = javaRuntimes("", []string{javaOf(jdk27)})
	if defaults := defaultRuntimes(got); len(defaults) != 0 || len(got) != 1 {
		t.Fatalf("non-LTS only: %+v", got)
	}
	if got := javaRuntimes(t.TempDir(), nil); len(got) != 0 {
		t.Fatalf("no JDKs: %+v", got)
	}

	// Relative JAVA_HOME or candidates are skipped: jdtls would resolve them
	// against the project.
	t.Chdir(filepath.Dir(jdk21))
	if got := javaRuntimes(filepath.Base(jdk21), []string{filepath.Join(filepath.Base(jdk17), "bin", javaExecutableName())}); len(got) != 0 {
		t.Fatalf("relative paths used: %+v", got)
	}
}

func TestJavaRuntimesDebianJava8JRE(t *testing.T) {
	// Debian / Ubuntu Java 8: /usr/lib/jvm/java-8-openjdk-amd64/jre/bin/java,
	// release in the JDK folder above jre/.
	jdk := t.TempDir()
	writeExecutable(t, filepath.Join(jdk, "jre", "bin", javaExecutableName()), "#!/bin/sh\nexit 1\n")
	if err := os.WriteFile(filepath.Join(jdk, "release"), []byte("JAVA_VERSION=\"1.8.0_422\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := javaRuntimes("", []string{filepath.Join(jdk, "jre", "bin", javaExecutableName())})
	want := []jdtlsRuntime{{Name: "JavaSE-1.8", Path: resolved(t, jdk), Default: true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Debian JRE runtimes = %+v, want %+v", got, want)
	}
	// Through JAVA_HOME=<jdk>/jre as well.
	if got := javaRuntimes(filepath.Join(jdk, "jre"), nil); !reflect.DeepEqual(got, want) {
		t.Fatalf("JAVA_HOME jre runtimes = %+v", got)
	}
	// A jre/ without a JDK release above stays unknown and is skipped.
	bare := t.TempDir()
	writeExecutable(t, filepath.Join(bare, "jre", "bin", javaExecutableName()), "#!/bin/sh\nexit 1\n")
	if got := javaRuntimes("", []string{filepath.Join(bare, "jre", "bin", javaExecutableName())}); len(got) != 0 {
		t.Fatalf("JRE without release: %+v", got)
	}
}

func TestJdtlsInitializeBundlesAndRuntimesTogether(t *testing.T) {
	setBundleProvider(t, func() []string { return []string{"/provided.jar"} })
	setRuntimeDiscovery(t, []jdtlsRuntime{{Name: "JavaSE-21", Path: "/jdk21", Default: true}})
	s := &session{root: t.TempDir(), definition: serverDefinition{ID: jdtlsServerID}}
	out, err := s.prepareInitialize(json.RawMessage(`{"initializationOptions":{"bundles":["/client.jar"],"settings":{"java":{"home":"/x"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := initializeBundles(t, out); !reflect.DeepEqual(got, []string{"/provided.jar"}) || strings.Contains(string(out), "/client.jar") {
		t.Fatalf("bundles = %v (%s)", got, out)
	}
	want := []any{map[string]any{"name": "JavaSE-21", "path": "/jdk21", "default": true}}
	if got := initializeRuntimes(t, out); !reflect.DeepEqual(got, want) || !strings.Contains(string(out), `"home":"/x"`) {
		t.Fatalf("runtimes = %v (%s)", got, out)
	}
	if logs := s.logs(); !strings.Contains(logs, "client-supplied bundles dropped") || !strings.Contains(logs, "bundles configured count=1") || !strings.Contains(logs, "runtimes configured count=1 default=21") {
		t.Fatalf("logs: %s", logs)
	}
}

func resolved(t *testing.T, path string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

func defaultRuntimes(runtimes []jdtlsRuntime) []string {
	var names []string
	for _, runtime := range runtimes {
		if runtime.Default {
			names = append(names, runtime.Name)
		}
	}
	return names
}

func setRuntimeDiscovery(t *testing.T, runtimes []jdtlsRuntime) {
	t.Helper()
	saved := discoverJdtlsRuntimes
	discoverJdtlsRuntimes = func() []jdtlsRuntime { return runtimes }
	t.Cleanup(func() { discoverJdtlsRuntimes = saved })
}

func initializeRuntimes(t *testing.T, raw json.RawMessage) any {
	t.Helper()
	var params struct {
		InitializationOptions struct {
			Settings struct {
				Java struct {
					Configuration map[string]any `json:"configuration"`
				} `json:"java"`
			} `json:"settings"`
		} `json:"initializationOptions"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return params.InitializationOptions.Settings.Java.Configuration["runtimes"]
}

func TestJdtlsInitializeConfiguresDiscoveredRuntimes(t *testing.T) {
	SetJdtlsBundleProvider(nil)
	setRuntimeDiscovery(t, []jdtlsRuntime{{Name: "JavaSE-17", Path: "/jdk17"}, {Name: "JavaSE-21", Path: "/jdk21", Default: true}})
	want := []any{
		map[string]any{"name": "JavaSE-17", "path": "/jdk17"},
		map[string]any{"name": "JavaSE-21", "path": "/jdk21", "default": true},
	}

	s := &session{root: t.TempDir(), definition: serverDefinition{ID: jdtlsServerID}}
	out, err := s.prepareInitialize(json.RawMessage(`{"processId":7}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := initializeRuntimes(t, out); !reflect.DeepEqual(got, want) {
		t.Fatalf("runtimes = %v, want %v (%s)", got, want, out)
	}
	if logs := s.logs(); !strings.Contains(logs, "runtimes configured count=2 default=21") || strings.Contains(logs, "/jdk") {
		t.Fatalf("runtimes not logged or paths leaked: %s", logs)
	}

	// Other settings are kept and merged into.
	out, err = s.prepareInitialize(json.RawMessage(`{"initializationOptions":{"settings":{"java":{"home":"/x","configuration":{"updateBuildConfiguration":"automatic"}},"other":1},"extendedClientCapabilities":{"a":true}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := initializeRuntimes(t, out); !reflect.DeepEqual(got, want) {
		t.Fatalf("merged runtimes = %v (%s)", got, out)
	}
	for _, kept := range []string{`"home":"/x"`, `"updateBuildConfiguration":"automatic"`, `"other":1`, `"extendedClientCapabilities":{"a":true}`} {
		if !strings.Contains(string(out), kept) {
			t.Fatalf("lost %s: %s", kept, out)
		}
	}

	// Client-supplied runtimes (nested or dotted) win; malformed settings are
	// left to jdtls.
	for _, params := range []string{
		`{"initializationOptions":{"settings":{"java":{"configuration":{"runtimes":[]}}}}}`,
		`{"initializationOptions":{"settings":{"java.configuration.runtimes":[]}}}`,
		`{"initializationOptions":{"settings":"bad"}}`,
	} {
		out, err := s.prepareInitialize(json.RawMessage(params))
		if err != nil || string(out) != params {
			t.Fatalf("client settings %s changed: %s %v", params, out, err)
		}
	}

	// No JDK found: initialize unchanged.
	setRuntimeDiscovery(t, nil)
	params := json.RawMessage(`{"processId":7}`)
	if out, err := s.prepareInitialize(params); err != nil || string(out) != string(params) {
		t.Fatalf("no runtimes: %s %v", out, err)
	}
}
