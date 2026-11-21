package lsp

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"mincode/app/backend/core"
)

// The jdtls launchers run jdtls on $JAVA_HOME/bin/java whenever JAVA_HOME is
// set: Homebrew's wrapper only defaults JAVA_HOME to its own openjdk when it
// is unset, and jdtls.py takes $JAVA_HOME/bin/java over PATH. jdtls itself
// needs a newer Java than many users point JAVA_HOME at (1.63 bundles
// require JavaSE-25; the launcher checks only 21), and with an older JVM it
// dies during OSGi startup. So the built-in jdtls gets JAVA_HOME only when
// that JDK is new enough; otherwise it is removed from jdtls's environment
// and the launcher falls back to its own JDK. Projects still compile and run
// against any installed JDK through java.configuration.runtimes
// (jdtls_runtimes.go). When the version of $JAVA_HOME/bin/java cannot be
// determined, JAVA_HOME is kept: the launcher decides as before.

// jdtlsLauncherMinJava is the floor jdtls.py itself enforces, used when the
// install's bundle manifests cannot be read.
const jdtlsLauncherMinJava = 21

const maxManifestBytes = 1 << 20

// jdtlsJavaProbeTimeout bounds one `java -version` probe; a probe that
// timed out is not retried for jdtlsJavaTimeoutTTL, so a hanging JAVA_HOME
// java does not delay every jdtls start (tests shorten both).
var (
	jdtlsJavaProbeTimeout = 5 * time.Second
	jdtlsJavaTimeoutTTL   = time.Minute
)

// jdtlsRequirementBundles are the jdtls core bundles whose manifests state
// the Java version jdtls needs to start (jdt.ls.core needs jdt.core and
// jdt.launching, so the highest of the three is the floor).
var jdtlsRequirementBundles = []string{"org.eclipse.jdt.ls.core_", "org.eclipse.jdt.core_", "org.eclipse.jdt.launching_"}

type jdtlsRequiredEntry struct {
	modified time.Time
	major    int
	unknown  bool
}

type javaHomeEntry struct {
	modified time.Time
	size     int64
	major    int
	// timedOut entries expire jdtlsJavaTimeoutTTL after probedAt.
	timedOut bool
	probedAt time.Time
}

// jdtlsJavaCache caches the required Java per plugins directory and the
// probed major per java binary, both keyed with the file's mtime.
var jdtlsJavaCache = struct {
	sync.Mutex
	required map[string]jdtlsRequiredEntry
	homes    map[string]javaHomeEntry
}{required: map[string]jdtlsRequiredEntry{}, homes: map[string]javaHomeEntry{}}

// jdtlsJavaHomeEnv removes JAVA_HOME from env when its java is older than
// the jdtls install at executable requires.
func jdtlsJavaHomeEnv(env []string, executable string, log func(string)) []string {
	home := strings.TrimSpace(envLookup(env, "JAVA_HOME"))
	if home == "" {
		return env
	}
	required, unknown := jdtlsRequiredJavaStatus(executable)
	if unknown {
		if log != nil {
			log("lifecycle jdtls JavaSE requirement unknown; JAVA_HOME ignored, launcher selects its JDK")
		}
		return envWithout(env, "JAVA_HOME")
	}
	// Do not probe a relative home against the host's cwd. Unknown metadata
	// above still removes it, just like an absolute inherited home.
	if !filepath.IsAbs(home) {
		return env
	}
	major, timedOut := javaHomeProbe(home)
	if timedOut && log != nil {
		log(fmt.Sprintf("lifecycle jdtls JAVA_HOME java -version timed out; JAVA_HOME kept, not probed again for %s", jdtlsJavaTimeoutTTL))
	}
	if major == 0 {
		return env
	}
	if major >= required {
		return env
	}
	if log != nil {
		log(fmt.Sprintf("lifecycle jdtls JAVA_HOME ignored java=%d required=%d; the launcher uses its own JDK", major, required))
	}
	return envWithout(env, "JAVA_HOME")
}

func sameEnvName(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func envLookup(env []string, name string) string {
	value := ""
	for _, entry := range env {
		if key, v, ok := strings.Cut(entry, "="); ok && sameEnvName(key, name) {
			value = v // the last entry wins, as for exec
		}
	}
	return value
}

func envWithout(env []string, name string) []string {
	out := make([]string, 0, len(env))
	for _, entry := range env {
		if key, _, _ := strings.Cut(entry, "="); !sameEnvName(key, name) {
			out = append(out, entry)
		}
	}
	return out
}

// jdtlsRequiredJava returns the Java major version the jdtls install whose
// launcher is executable needs: the highest JavaSE requirement in its core
// bundles' manifests, or jdtlsLauncherMinJava when none can be read.
func jdtlsRequiredJava(executable string) int {
	major, _ := jdtlsRequiredJavaStatus(executable)
	return major
}

func jdtlsRequiredJavaStatus(executable string) (int, bool) {
	plugins := jdtlsPluginsDir(executable)
	if plugins == "" {
		return jdtlsLauncherMinJava, false
	}
	info, err := os.Stat(plugins)
	if err != nil {
		return jdtlsLauncherMinJava, false
	}
	jdtlsJavaCache.Lock()
	defer jdtlsJavaCache.Unlock()
	if entry, ok := jdtlsJavaCache.required[plugins]; ok && entry.modified.Equal(info.ModTime()) {
		return entry.major, entry.unknown
	}
	required, unknown := 0, false
	for _, prefix := range jdtlsRequirementBundles {
		jars, _ := filepath.Glob(filepath.Join(plugins, prefix+"*.jar"))
		for _, jar := range jars {
			major, understood := manifestRequiredJavaStatus(jar)
			required = max(required, major)
			unknown = unknown || !understood
		}
	}
	if required == 0 {
		required = jdtlsLauncherMinJava
	}
	jdtlsJavaCache.required[plugins] = jdtlsRequiredEntry{modified: info.ModTime(), major: required, unknown: unknown}
	return required, unknown
}

// jdtlsPluginsDir finds the plugins folder of the jdtls install that the
// launcher executable belongs to: <prefix>/plugins (upstream archive),
// <prefix>/libexec/plugins (Homebrew wrapper) or
// <prefix>/share/java/jdtls/plugins (Linux packages).
func jdtlsPluginsDir(executable string) string {
	if executable == "" {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return ""
	}
	prefix := filepath.Dir(filepath.Dir(resolved))
	for _, dir := range []string{
		filepath.Join(prefix, "plugins"),
		filepath.Join(prefix, "libexec", "plugins"),
		filepath.Join(prefix, "share", "java", "jdtls", "plugins"),
	} {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}
	return ""
}

var (
	breeJavaSE = regexp.MustCompile(`^JavaSE-([0-9][0-9.]*)$`)
)

// manifestRequiredJava returns the JavaSE version a bundle jar requires
// (osgi.ee Require-Capability or Bundle-RequiredExecutionEnvironment), or 0.
func manifestRequiredJava(jar string) int {
	major, _ := manifestRequiredJavaStatus(jar)
	return major
}

func manifestRequiredJavaStatus(jar string) (int, bool) {
	headers := readJarManifest(jar)
	required, understood := 0, true
	if capability := headers["Require-Capability"]; capability != "" {
		required, understood = javaEECapabilityMinimum(capability)
	}
	if environments := headers["Bundle-RequiredExecutionEnvironment"]; environments != "" {
		lowest := 0 // the header lists alternatives
		for _, environment := range strings.Split(environments, ",") {
			match := breeJavaSE.FindStringSubmatch(strings.TrimSpace(environment))
			if match == nil {
				continue
			}
			if version := javaVersionNumber(match[1]); version > 0 && (lowest == 0 || version < lowest) {
				lowest = version
			}
		}
		required = max(required, lowest)
	}
	return required, understood
}

// readJarManifest returns the main-section headers of a jar's
// META-INF/MANIFEST.MF (continuation lines unfolded), or nil.
func readJarManifest(jar string) map[string]string {
	if !isRegularFile(jar) {
		return nil
	}
	archive, err := zip.OpenReader(jar)
	if err != nil {
		return nil
	}
	defer archive.Close()
	for _, file := range archive.File {
		if file.Name != "META-INF/MANIFEST.MF" {
			continue
		}
		reader, err := file.Open()
		if err != nil {
			return nil
		}
		data, err := io.ReadAll(io.LimitReader(reader, maxManifestBytes))
		_ = reader.Close()
		if err != nil {
			return nil
		}
		return parseManifest(string(data))
	}
	return nil
}

func parseManifest(text string) map[string]string {
	headers := map[string]string{}
	last := ""
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		switch {
		case line == "":
			return headers // end of the main section
		case strings.HasPrefix(line, " ") && last != "":
			headers[last] += line[1:]
		default:
			name, value, ok := strings.Cut(line, ":")
			if !ok {
				last = ""
				continue
			}
			last = name
			headers[name] = strings.TrimPrefix(value, " ")
		}
	}
	return headers
}

// javaVersionNumber turns "1.8" into 8 and "21" or "21.0" into 21; 0 when
// invalid.
func javaVersionNumber(text string) int {
	parts := strings.Split(text, ".")
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0
	}
	if major == 1 && len(parts) > 1 {
		major, _ = strconv.Atoi(parts[1])
	}
	return major
}

// javaHomeMajor returns the major version that home/bin/java reports for
// `java -version`, or 0 when home is relative, java is missing, the macOS
// installer stub, fails, times out or prints no version.
func javaHomeMajor(home string) int {
	major, _ := javaHomeProbe(home)
	return major
}

// javaHomeProbe is javaHomeMajor; timedOut reports that this call ran a
// probe that timed out. Results are cached per resolved binary and its
// mtime and size; a timeout only for jdtlsJavaTimeoutTTL.
func javaHomeProbe(home string) (major int, timedOut bool) {
	if !filepath.IsAbs(home) {
		return 0, false
	}
	java := filepath.Join(home, "bin", javaExecutableName())
	resolved, err := filepath.EvalSymlinks(java)
	if err != nil || isJavaInstallerStub(java, resolved) {
		return 0, false
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return 0, false
	}
	jdtlsJavaCache.Lock()
	defer jdtlsJavaCache.Unlock()
	if entry, ok := jdtlsJavaCache.homes[resolved]; ok && entry.modified.Equal(info.ModTime()) && entry.size == info.Size() &&
		(!entry.timedOut || time.Since(entry.probedAt) < jdtlsJavaTimeoutTTL) {
		return entry.major, false
	}
	major, timedOut = probeJavaMajor(resolved)
	jdtlsJavaCache.homes[resolved] = javaHomeEntry{modified: info.ModTime(), size: info.Size(), major: major, timedOut: timedOut, probedAt: time.Now()}
	return major, timedOut
}

// isRegularFile reports whether path (symlinks followed) is a regular file.
// Callers check it before opening: opening a FIFO for reading blocks.
func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// probeJavaMajor runs `<java> -version` from the temp directory, bounded by
// jdtlsJavaProbeTimeout; the whole process group is killed on timeout.
func probeJavaMajor(java string) (major int, timedOut bool) {
	ctx, cancel := context.WithTimeout(context.Background(), jdtlsJavaProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, java, "-version")
	core.PrepareBackgroundCommand(cmd)
	cmd.Dir = os.TempDir()
	cmd.Env = core.ExpandedEnv()
	output := &limitedBuffer{max: 64 * 1024}
	cmd.Stdout = output
	cmd.Stderr = output
	core.PrepareProcessTree(cmd)
	cmd.Cancel = func() error { return core.KillProcessTree(cmd) }
	cmd.WaitDelay = 500 * time.Millisecond
	err := cmd.Run()
	if ctx.Err() != nil {
		return 0, true
	}
	if err != nil {
		return 0, false
	}
	return parseJavaMajorVersion(output.String()), false
}

// javaVersionLine matches `openjdk version "21.0.5"`, `java version
// "1.8.0_392"` or `openjdk version "27-ea"`.
var javaVersionLine = regexp.MustCompile(`(?m)^\S+ version "(\d+)(?:\.(\d+))?[^"]*"`)

func javaExecutableName() string {
	if runtime.GOOS == "windows" {
		return "java.exe"
	}
	return "java"
}

// parseJavaMajorVersion returns the major version in `java -version` output
// (1.8 is 8), or 0.
func parseJavaMajorVersion(output string) int {
	match := javaVersionLine.FindStringSubmatch(output)
	if match == nil {
		return 0
	}
	major, _ := strconv.Atoi(match[1])
	if major == 1 && match[2] != "" {
		major, _ = strconv.Atoi(match[2])
	}
	return major
}
