package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

// jdtlsBundles[].dependencies: library jars that register no commands load,
// hash and are confirmed together with the entry's main jar.

var jdtlsBundleTestDependencies = map[string]string{
	"lib/deps/junit.jar": "junit-v1",
	"lib/deps/asm.jar":   "asm-v1",
}

func jdtlsBundleWithDependencies(dependencies ...string) []any {
	return []any{map[string]any{"path": "${pluginRoot}/lib/debug.jar", "commands": jdtlsBundleTestCommands, "dependencies": dependencies}}
}

// useJdtlsBundleManifest replaces the fixture plugin's package with one
// declaring bundles, after writing jars (relative path → content) into it.
func (fixture *jdtlsBundleFixture) useJdtlsBundleManifest(t *testing.T, bundles []any, jars map[string]string) {
	t.Helper()
	root := writeJdtlsBundleManifest(t, jdtlsBundleTestPlugin, []string{"shell"}, bundles)
	for relative, content := range jars {
		writeJdtlsBundleJar(t, root, relative, content)
	}
	preview, err := inspectInstallDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	fixture.record.mu.Lock()
	fixture.record.root, fixture.record.manifest = root, &preview.Manifest
	fixture.record.mu.Unlock()
	fixture.root = root
}

func newJdtlsBundleDependencyFixture(t *testing.T) *jdtlsBundleFixture {
	t.Helper()
	fixture := newJdtlsBundleFixture(t)
	fixture.useJdtlsBundleManifest(t, jdtlsBundleWithDependencies("${pluginRoot}/lib/deps/junit.jar", "${pluginRoot}/lib/deps/asm.jar"), jdtlsBundleTestDependencies)
	return fixture
}

func (fixture *jdtlsBundleFixture) jarPaths(t *testing.T, relatives ...string) []string {
	t.Helper()
	paths := make([]string, 0, len(relatives))
	for _, relative := range relatives {
		path, err := resolvePluginPath(fixture.root, relative)
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	slices.Sort(paths)
	return paths
}

func TestJdtlsBundleDependenciesDecode(t *testing.T) {
	ctx := jdtlsBundleTestContext(t)
	writeJdtlsBundleJar(t, ctx.root, "lib/deps/a.jar", "a")
	writeJdtlsBundleJar(t, ctx.root, "lib/deps/b.JAR", "b")
	bundle, err := decodeJdtlsBundleForTest(ctx, `{"path":"${pluginRoot}/lib/debug.jar","commands":["x"],"dependencies":[" ${pluginRoot}/lib/deps/a.jar ","${pluginRoot}/lib/deps/b.JAR"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(bundle.Dependencies, []string{"${pluginRoot}/lib/deps/a.jar", "${pluginRoot}/lib/deps/b.JAR"}) {
		t.Fatalf("dependencies = %v", bundle.Dependencies)
	}
	// Absent and empty lists are the same: no dependencies.
	for _, raw := range []string{`{"path":"${pluginRoot}/lib/debug.jar","commands":["x"]}`, `{"path":"${pluginRoot}/lib/debug.jar","commands":["x"],"dependencies":[]}`} {
		if bundle, err := decodeJdtlsBundleForTest(ctx, raw); err != nil || len(bundle.Dependencies) != 0 {
			t.Fatalf("%s: bundle %+v err %v", raw, bundle, err)
		}
	}
	outside := writeJdtlsBundleJar(t, t.TempDir(), "evil.jar", "evil")
	writeJdtlsBundleJar(t, ctx.root, "lib/notes.txt", "text")
	cases := map[string]string{
		outside:                           "${pluginRoot}/",
		"lib/deps/a.jar":                  "${pluginRoot}/",
		"${pluginRoot}/../evil.jar":       ".. segments",
		"${pluginRoot}/lib/notes.txt":     "must name a .jar file",
		"${pluginRoot}/lib/missing.jar":   "does not exist",
		"${pluginRoot}/${pluginRoot}.jar": "variable",
		"${pluginRoot}/lib/debug.jar":     "main jar",
		"":                                "${pluginRoot}/",
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink(outside, filepath.Join(ctx.root, "lib", "link.jar")); err != nil {
			t.Fatal(err)
		}
		cases["${pluginRoot}/lib/link.jar"] = "symlink"
	}
	for path, want := range cases {
		encoded, _ := json.Marshal(map[string]any{"path": "${pluginRoot}/lib/debug.jar", "commands": []string{"x"}, "dependencies": []string{path}})
		if _, err := decodeJdtlsBundleForTest(ctx, string(encoded)); err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "dependency") {
			t.Fatalf("%q: err = %v, want %q", path, err, want)
		}
	}
	for raw, want := range map[string]string{
		`{"path":"${pluginRoot}/lib/debug.jar","commands":["x"],"dependencies":["${pluginRoot}/lib/deps/a.jar","${pluginRoot}/lib/deps/a.jar"]}`: "more than once",
		`{"path":"${pluginRoot}/lib/debug.jar","commands":["x"],"dependencies":"${pluginRoot}/lib/deps/a.jar"}`:                                  "dependencies",
		`{"path":"${pluginRoot}/lib/debug.jar","commands":["x"],"dependencies":[1]}`:                                                             "dependencies",
	} {
		if _, err := decodeJdtlsBundleForTest(ctx, raw); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: err = %v, want %q", raw, err, want)
		}
	}
	// At most dependenciesMax entries.
	if jdtlsBundleDependenciesMax != 64 {
		t.Fatalf("dependenciesMax = %d, want 64", jdtlsBundleDependenciesMax)
	}
	tooMany := make([]string, jdtlsBundleDependenciesMax+1)
	for index := range tooMany {
		relative := fmt.Sprintf("lib/many/d%02d.jar", index)
		writeJdtlsBundleJar(t, ctx.root, relative, "d")
		tooMany[index] = "${pluginRoot}/" + relative
	}
	encoded, _ := json.Marshal(map[string]any{"path": "${pluginRoot}/lib/debug.jar", "commands": []string{"x"}, "dependencies": tooMany[:jdtlsBundleDependenciesMax]})
	if _, err := decodeJdtlsBundleForTest(ctx, string(encoded)); err != nil {
		t.Fatalf("%d dependencies: %v", jdtlsBundleDependenciesMax, err)
	}
	encoded, _ = json.Marshal(map[string]any{"path": "${pluginRoot}/lib/debug.jar", "commands": []string{"x"}, "dependencies": tooMany})
	if _, err := decodeJdtlsBundleForTest(ctx, string(encoded)); err == nil || !strings.Contains(err.Error(), "more than 64 dependencies") {
		t.Fatalf("too many dependencies: err = %v", err)
	}
	// Each dependency obeys jarMaxBytes.
	previous := jdtlsBundleJarMaxBytes
	jdtlsBundleJarMaxBytes = 4
	t.Cleanup(func() { jdtlsBundleJarMaxBytes = previous })
	writeJdtlsBundleJar(t, ctx.root, "lib/debug.jar", "jar")
	writeJdtlsBundleJar(t, ctx.root, "lib/deps/big.jar", "too big")
	if _, err := decodeJdtlsBundleForTest(ctx, `{"path":"${pluginRoot}/lib/debug.jar","commands":["x"],"dependencies":["${pluginRoot}/lib/deps/big.jar"]}`); err == nil || !strings.Contains(err.Error(), "exceeds 4 bytes") {
		t.Fatalf("oversized dependency: err = %v", err)
	}
}

// A jar may appear once per plugin: a later entry that reuses an earlier
// entry's main or dependency jar is skipped with an issue.
func TestJdtlsBundleDependenciesUniqueWithinPlugin(t *testing.T) {
	root := writeJdtlsBundleManifest(t, jdtlsBundleTestPlugin, []string{"shell"}, []any{
		map[string]any{"path": "${pluginRoot}/lib/debug.jar", "commands": []string{"a"}, "dependencies": []string{"${pluginRoot}/lib/deps/junit.jar"}},
		map[string]any{"path": "${pluginRoot}/lib/extra.jar", "commands": []string{"b"}, "dependencies": []string{"${pluginRoot}/lib/deps/junit.jar"}},
		map[string]any{"path": "${pluginRoot}/lib/third.jar", "commands": []string{"c"}, "dependencies": []string{"${pluginRoot}/lib/debug.jar"}},
		map[string]any{"path": "${pluginRoot}/lib/deps/junit.jar", "commands": []string{"d"}},
	})
	writeJdtlsBundleJar(t, root, "lib/deps/junit.jar", "junit")
	writeJdtlsBundleJar(t, root, "lib/third.jar", "third")
	if _, err := inspectInstallDirectory(root); err == nil || !strings.Contains(err.Error(), "already used by jdtls bundle") {
		t.Fatalf("strict install: err = %v", err)
	}
	preview, err := inspectPackageDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	entries := jdtlsBundleEntries(&preview.Manifest)
	if len(entries) != 1 || entries[0].Path != "${pluginRoot}/lib/debug.jar" {
		t.Fatalf("entries = %+v", entries)
	}
	issues := 0
	for _, issue := range preview.Manifest.contributions[jdtlsBundlePointKey].Issues {
		if strings.Contains(issue.Message, "already used by jdtls bundle") {
			issues++
		}
	}
	if issues != 3 {
		t.Fatalf("issues = %+v", preview.Manifest.contributions[jdtlsBundlePointKey].Issues)
	}
}

func TestJdtlsBundleDependenciesConfirmedAndLoadedTogether(t *testing.T) {
	fixture := newJdtlsBundleDependencyFixture(t)
	if paths := fixture.manager.jdtlsBundlePaths(); len(paths) != 0 {
		t.Fatalf("untrusted group provided: %v", paths)
	}
	bundle := fixture.bundle(t, jdtlsBundleTestPlugin)
	if len(bundle.Jars) != 3 || bundle.Trusted {
		t.Fatalf("bundle = %+v", bundle)
	}
	// Main jar first with its commands, then the dependencies sorted by path,
	// without commands and naming their main jar by its relative path (the
	// same value the fingerprint uses).
	main, asm, junit := bundle.Jars[0], bundle.Jars[1], bundle.Jars[2]
	if main.Name != "debug.jar" || main.DependencyOf != "" || len(main.Commands) != 2 ||
		asm.Name != "asm.jar" || asm.DependencyOf != "lib/debug.jar" || len(asm.Commands) != 0 || asm.Bytes != int64(len("asm-v1")) || len(asm.SHA256) != 64 ||
		junit.Name != "junit.jar" || junit.DependencyOf != "lib/debug.jar" {
		t.Fatalf("jars = %+v", bundle.Jars)
	}
	// One confirmation covers the whole group, stored as one trust entry.
	fixture.trust(t, jdtlsBundleTestPlugin)
	want := fixture.jarPaths(t, "lib/debug.jar", "lib/deps/junit.jar", "lib/deps/asm.jar")
	if paths := fixture.manager.jdtlsBundlePaths(); !slices.Equal(paths, want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	store, err := readProcessLaunchTrust(jdtlsBundleTestPlugin, jdtlsBundlePointKey)
	if err != nil || len(store.Entries) != 1 {
		t.Fatalf("trust store = %+v, %v", store, err)
	}
	// The command still routes to the main jar.
	if owner, _, jar := fixture.manager.jdtlsCommandRoute(jdtlsBundleTestCommands[0]); owner != jdtlsBundleTestPlugin || jar != fixture.jarPaths(t, "lib/debug.jar")[0] {
		t.Fatalf("route = %q %q", owner, jar)
	}
}

// Review Focus 3: replacing one dependency (same size and modification time)
// changes the fingerprint; until the group is confirmed again none of its
// jars loads, including the unchanged main jar.
func TestJdtlsBundleDependencyChangeNeedsReconfirmation(t *testing.T) {
	fixture := newJdtlsBundleDependencyFixture(t)
	before := fixture.trust(t, jdtlsBundleTestPlugin)
	jar := filepath.Join(fixture.root, "lib", "deps", "asm.jar")
	info, err := os.Stat(jar)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jar, []byte("asm-v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(jar, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if paths := fixture.manager.jdtlsBundlePaths(); len(paths) != 0 {
		t.Fatalf("group with a changed dependency provided: %v", paths)
	}
	after := fixture.bundle(t, jdtlsBundleTestPlugin)
	if after.Trusted || !after.PreviouslyTrusted || after.Fingerprint == before.Fingerprint {
		t.Fatalf("after change = %+v", after)
	}
	if _, err := fixture.manager.trustJdtlsBundles(jdtlsBundleTestPlugin, before.Fingerprint); processLaunchErrorCode(err) != processLaunchErrChanged {
		t.Fatalf("trusting the old fingerprint: %v", err)
	}
	fixture.trust(t, jdtlsBundleTestPlugin)
	if paths := fixture.manager.jdtlsBundlePaths(); len(paths) != 3 {
		t.Fatalf("re-confirmed group: %v", paths)
	}
}

// A missing or unreadable dependency makes the whole group unavailable.
func TestJdtlsBundleMissingDependencyDisablesGroup(t *testing.T) {
	fixture := newJdtlsBundleDependencyFixture(t)
	fixture.trust(t, jdtlsBundleTestPlugin)
	if err := os.Remove(filepath.Join(fixture.root, "lib", "deps", "junit.jar")); err != nil {
		t.Fatal(err)
	}
	if paths := fixture.manager.jdtlsBundlePaths(); len(paths) != 0 {
		t.Fatalf("group with a missing dependency provided: %v", paths)
	}
	bundle := fixture.bundle(t, jdtlsBundleTestPlugin)
	if bundle.Error == "" || !strings.Contains(bundle.Error, "junit.jar") || len(bundle.Jars) != 0 || bundle.Trusted {
		t.Fatalf("bundle = %+v", bundle)
	}
	if _, err := fixture.manager.trustJdtlsBundles(jdtlsBundleTestPlugin, bundle.Fingerprint); err == nil {
		t.Fatal("trusted a group with a missing dependency")
	}
	if owner := fixture.manager.jdtlsCommandOwner(jdtlsBundleTestCommands[0]); owner != "" {
		t.Fatalf("command routed with a missing dependency: %q", owner)
	}
	if runtime.GOOS == "windows" {
		return
	}
	// A dependency replaced by a symlink leaving the package.
	outside := writeJdtlsBundleJar(t, t.TempDir(), "junit.jar", "junit-v1")
	if err := os.Symlink(outside, filepath.Join(fixture.root, "lib", "deps", "junit.jar")); err != nil {
		t.Fatal(err)
	}
	if paths := fixture.manager.jdtlsBundlePaths(); len(paths) != 0 {
		t.Fatalf("group with an escaping dependency provided: %v", paths)
	}
}

// The fingerprint covers the set of jars: dependency order does not matter,
// adding or removing a dependency does.
func TestJdtlsBundleDependenciesFingerprint(t *testing.T) {
	fixture := newJdtlsBundleDependencyFixture(t)
	before := fixture.trust(t, jdtlsBundleTestPlugin)
	fixture.useJdtlsBundleManifest(t, jdtlsBundleWithDependencies("${pluginRoot}/lib/deps/asm.jar", "${pluginRoot}/lib/deps/junit.jar"), jdtlsBundleTestDependencies)
	if again := fixture.bundle(t, jdtlsBundleTestPlugin); again.Fingerprint != before.Fingerprint || !again.Trusted {
		t.Fatalf("reordered dependencies: %+v", again)
	}
	fixture.useJdtlsBundleManifest(t, jdtlsBundleWithDependencies("${pluginRoot}/lib/deps/asm.jar"), jdtlsBundleTestDependencies)
	if fewer := fixture.bundle(t, jdtlsBundleTestPlugin); fewer.Fingerprint == before.Fingerprint || fewer.Trusted || len(fewer.Jars) != 2 {
		t.Fatalf("removed dependency: %+v", fewer)
	}
	if paths := fixture.manager.jdtlsBundlePaths(); len(paths) != 0 {
		t.Fatalf("changed group loaded without confirmation: %v", paths)
	}
	// Without dependencies the fingerprint is the one of a plain bundle.
	plain := newJdtlsBundleFixture(t).bundle(t, jdtlsBundleTestPlugin)
	fixture.useJdtlsBundleManifest(t, jdtlsBundleWithDependencies(), nil)
	if none := fixture.bundle(t, jdtlsBundleTestPlugin); none.Fingerprint != plain.Fingerprint {
		t.Fatalf("empty dependencies changed the fingerprint: %s vs %s", none.Fingerprint, plain.Fingerprint)
	}
}

// A trusted group loads at the first jdtls start; a session that lacks one of
// its jars is not "loaded" and needs a restart.
func TestJdtlsBundleDependenciesLoadedAtFirstJdtlsStart(t *testing.T) {
	fixture := newJdtlsBundleDependencyFixture(t)
	fixture.trust(t, jdtlsBundleTestPlugin)
	fixture.startJdtlsFromFileOpen()
	if len(fixture.session) != 3 {
		t.Fatalf("jdtls started with %v", fixture.session)
	}
	status := fixture.status(t)
	if status.RestartRequired || len(status.Bundles) != 1 || !status.Bundles[0].Loaded {
		t.Fatalf("status = %+v", status)
	}
	fixture.session = fixture.jarPaths(t, "lib/debug.jar", "lib/deps/asm.jar")
	status = fixture.status(t)
	if !status.RestartRequired || status.Bundles[0].Loaded {
		t.Fatalf("status without one dependency = %+v", status)
	}
}

func setJdtlsBundleBytesMax(t *testing.T, limit int64) {
	t.Helper()
	previous := jdtlsBundleBytesMax
	jdtlsBundleBytesMax = limit
	t.Cleanup(func() { jdtlsBundleBytesMax = previous })
}

// All of a plugin's jars together stay within bundleBytesMax: checked from
// file sizes at install (decode for one entry, finalize across entries) and
// again before any hashing at load.
func TestJdtlsBundleTotalSizeCap(t *testing.T) {
	if jdtlsBundleBytesMax != 256<<20 {
		t.Fatalf("bundleBytesMax = %d, want 256 MiB", jdtlsBundleBytesMax)
	}
	ctx := jdtlsBundleTestContext(t) // lib/debug.jar: 6 bytes
	writeJdtlsBundleJar(t, ctx.root, "lib/deps/a.jar", "aaaa")
	writeJdtlsBundleJar(t, ctx.root, "lib/deps/b.jar", "bbbb")
	setJdtlsBundleBytesMax(t, 14)
	if _, err := decodeJdtlsBundleForTest(ctx, `{"path":"${pluginRoot}/lib/debug.jar","commands":["x"],"dependencies":["${pluginRoot}/lib/deps/a.jar","${pluginRoot}/lib/deps/b.jar"]}`); err != nil {
		t.Fatalf("14 bytes in total: %v", err)
	}
	setJdtlsBundleBytesMax(t, 13)
	if _, err := decodeJdtlsBundleForTest(ctx, `{"path":"${pluginRoot}/lib/debug.jar","commands":["x"],"dependencies":["${pluginRoot}/lib/deps/a.jar","${pluginRoot}/lib/deps/b.jar"]}`); err == nil || !strings.Contains(err.Error(), "exceed 13 bytes in total") {
		t.Fatalf("over the total: err = %v", err)
	}

	// Across entries: the entry that crosses the cap is skipped.
	root := writeJdtlsBundleManifest(t, jdtlsBundleTestPlugin, []string{"shell"}, []any{
		map[string]any{"path": "${pluginRoot}/lib/debug.jar", "commands": []string{"a"}}, // 6 bytes
		map[string]any{"path": "${pluginRoot}/lib/extra.jar", "commands": []string{"b"}}, // 8 bytes
	})
	setJdtlsBundleBytesMax(t, 10)
	if _, err := inspectInstallDirectory(root); err == nil || !strings.Contains(err.Error(), "exceed 10 bytes in total") {
		t.Fatalf("strict install over the plugin total: err = %v", err)
	}
	preview, err := inspectPackageDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	if entries := jdtlsBundleEntries(&preview.Manifest); len(entries) != 1 || entries[0].Path != "${pluginRoot}/lib/debug.jar" {
		t.Fatalf("entries = %+v", entries)
	}
}

// A jar that grows after installation past the plugin total makes the whole
// group unavailable without hashing anything.
func TestJdtlsBundleTotalSizeCapAtLoad(t *testing.T) {
	fixture := newJdtlsBundleDependencyFixture(t) // 6 + 8 + 6 bytes
	setJdtlsBundleBytesMax(t, 20)
	fixture.trust(t, jdtlsBundleTestPlugin)
	if paths := fixture.manager.jdtlsBundlePaths(); len(paths) != 3 {
		t.Fatalf("paths = %v", paths)
	}
	writeJdtlsBundleJar(t, fixture.root, "lib/deps/asm.jar", "asm-v1+")
	hashed := countJdtlsBundleHashes(t)
	if paths := fixture.manager.jdtlsBundlePaths(); len(paths) != 0 {
		t.Fatalf("group over the total provided: %v", paths)
	}
	if hashed.Load() != 0 {
		t.Fatalf("hashed %d jars of a group over the total", hashed.Load())
	}
	if bundle := fixture.bundle(t, jdtlsBundleTestPlugin); !strings.Contains(bundle.Error, "in total") || bundle.Trusted || len(bundle.Jars) != 0 {
		t.Fatalf("bundle = %+v", bundle)
	}
}

func countJdtlsBundleHashes(t *testing.T) *atomic.Int32 {
	t.Helper()
	counter := &atomic.Int32{}
	previous := jdtlsBundleHashJar
	jdtlsBundleHashJar = func(root, relative string, fresh bool) (JdtlsBundleJar, error) {
		counter.Add(1)
		return previous(root, relative, fresh)
	}
	t.Cleanup(func() { jdtlsBundleHashJar = previous })
	return counter
}

// The jdtls provider never hashes the jars of a plugin the user has never
// confirmed (they could not load anyway); the status the confirmation reads
// still hashes them.
func TestJdtlsBundleProviderSkipsNeverTrustedPlugins(t *testing.T) {
	fixture := newJdtlsBundleDependencyFixture(t)
	hashed := countJdtlsBundleHashes(t)
	if paths := fixture.manager.jdtlsBundlePaths(); len(paths) != 0 {
		t.Fatalf("paths = %v", paths)
	}
	fixture.manager.checkJdtlsBundleSet()
	if hashed.Load() != 0 {
		t.Fatalf("provider hashed %d jars of a never-trusted plugin", hashed.Load())
	}
	if bundle := fixture.bundle(t, jdtlsBundleTestPlugin); len(bundle.Jars) != 3 || hashed.Load() != 3 {
		t.Fatalf("status: bundle %+v, hashed %d", bundle, hashed.Load())
	}
	// Once trusted (or previously trusted) the provider hashes again.
	fixture.trust(t, jdtlsBundleTestPlugin)
	hashed.Store(0)
	if paths := fixture.manager.jdtlsBundlePaths(); len(paths) != 3 || hashed.Load() != 3 {
		t.Fatalf("trusted: paths %v, hashed %d", paths, hashed.Load())
	}
}
