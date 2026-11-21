package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

// java.configuration.runtimes for the built-in jdtls.
//
// jdtls compiles a folder without a build file (an "invisible project") for
// its default VM, which is the JDK jdtls runs on unless
// java.configuration.runtimes marks another one default. Since jdtls needs a
// recent Java (25 for 1.63), class files of such a folder would otherwise
// target that Java and fail on an older JDK with UnsupportedClassVersionError.
// The host therefore sends, in initialize's
// initializationOptions.settings.java.configuration.runtimes, every JDK it
// can find without running anything. That is where jdtls 1.63 reads its
// settings (BaseInitHandler: Preferences.createFrom(settings), then the
// runtimes are configured); later it only takes them from
// workspace/didChangeConfiguration, which MinCode does not send, and asks
// workspace/configuration solely for java.format.tabSize / insertSpaces
// (code actions), which the frontend answers with null (jdtls defaults).
// JDKs are looked up in JAVA_HOME first, then the usual install folders (see
// jdkJavaPatterns), one per Java version, read from each JDK's release
// file. The default is Java 21 when installed, else the newest LTS (8, 11,
// 17, 21, 25, …) found, else none (jdtls keeps its own JVM). Maven and Gradle
// projects keep their configured release and pick the matching runtime.
// Runtimes a client sends itself are left alone.

// maxJdtlsRuntimes bounds the runtimes list.
const maxJdtlsRuntimes = 32

// preferredDefaultJava is the default runtime's Java version when installed.
const preferredDefaultJava = 21

type jdtlsRuntime struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Default bool   `json:"default,omitempty"`
}

// discoverJdtlsRuntimes finds the installed JDKs (tests replace it).
var discoverJdtlsRuntimes = func() []jdtlsRuntime {
	var candidates []string
	for _, pattern := range jdkJavaPatterns() {
		matches, _ := filepath.Glob(pattern)
		slices.Reverse(matches) // newest-looking first
		candidates = append(candidates, matches...)
	}
	return javaRuntimes(os.Getenv("JAVA_HOME"), candidates)
}

// javaRuntimes turns JAVA_HOME and candidate java executables into jdtls
// runtimes: the JDK home of each (symlinks resolved), with a readable
// release file, the first one per Java version. Relative paths are skipped
// (jdtls would resolve them against the project).
func javaRuntimes(javaHome string, candidates []string) []jdtlsRuntime {
	if javaHome = strings.TrimSpace(javaHome); javaHome != "" && filepath.IsAbs(javaHome) {
		candidates = append([]string{filepath.Join(javaHome, "bin", javaExecutableName())}, candidates...)
	}
	var runtimes []jdtlsRuntime
	majors := map[int]bool{}
	homes := map[string]bool{}
	for _, java := range candidates {
		if len(runtimes) >= maxJdtlsRuntimes {
			break
		}
		if !filepath.IsAbs(java) {
			continue
		}
		resolved, err := filepath.EvalSymlinks(java)
		if err != nil || isJavaInstallerStub(java, resolved) || !isExecutableJavaRuntime(resolved) {
			continue
		}
		home, major := jdkHomeOf(resolved)
		if homes[home] {
			continue
		}
		homes[home] = true
		if major < 8 || majors[major] {
			continue
		}
		majors[major] = true
		runtimes = append(runtimes, jdtlsRuntime{Name: executionEnvironment(major), Path: home})
	}
	if index := defaultRuntimeIndex(runtimes); index >= 0 {
		runtimes[index].Default = true
	}
	return runtimes
}

// A bad candidate must not consume the per-version slot before a usable JDK.
// Match host process-launch permission semantics without executing the program.
func isExecutableJavaRuntime(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && (runtime.GOOS == "windows" || info.Mode().Perm()&0o111 != 0)
}

// executionEnvironment is the jdtls runtime name of a Java version.
func executionEnvironment(major int) string {
	if major == 8 {
		return "JavaSE-1.8"
	}
	return "JavaSE-" + strconv.Itoa(major)
}

func runtimeMajor(runtime jdtlsRuntime) int {
	return javaVersionNumber(strings.TrimPrefix(runtime.Name, "JavaSE-"))
}

// isLTS reports whether a Java version is a long-term-support release.
func isLTS(major int) bool {
	return major == 8 || major == 11 || major >= 17 && (major-17)%4 == 0
}

func defaultRuntimeIndex(runtimes []jdtlsRuntime) int {
	best := -1
	for index, runtime := range runtimes {
		major := runtimeMajor(runtime)
		if major == preferredDefaultJava {
			return index
		}
		if isLTS(major) && (best < 0 || major > runtimeMajor(runtimes[best])) {
			best = index
		}
	}
	return best
}

// jdkHomeOf returns the JDK home of a resolved java executable and its
// release version: <home>/bin/java, or for a Java 8 JRE inside a JDK
// (Debian's <jdk>/jre/bin/java) the JDK one level up, which holds release.
func jdkHomeOf(java string) (string, int) {
	home := filepath.Dir(filepath.Dir(java))
	if major := releaseJavaMajor(home); major != 0 || filepath.Base(home) != "jre" {
		return home, major
	}
	parent := filepath.Dir(home)
	if major := releaseJavaMajor(parent); major != 0 {
		return parent, major
	}
	return home, 0
}

// releaseJavaMajor reads JAVA_VERSION from a JDK home's release file
// ("21.0.5", "1.8.0_392", "27-ea"); 0 when absent, not a regular file or
// unreadable.
func releaseJavaMajor(home string) int {
	path := filepath.Join(home, "release")
	if !isRegularFile(path) {
		return 0
	}
	file, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer file.Close()
	scanner := bufio.NewScanner(io.LimitReader(file, 64*1024))
	for scanner.Scan() {
		value, ok := strings.CutPrefix(strings.TrimSpace(scanner.Text()), "JAVA_VERSION=")
		if !ok {
			continue
		}
		version := strings.Trim(value, `"`)
		end := strings.IndexFunc(version, func(r rune) bool { return (r < '0' || r > '9') && r != '.' })
		if end >= 0 {
			version = version[:end]
		}
		return javaVersionNumber(version)
	}
	return 0
}

// addJdtlsRuntimes sets settings.java.configuration.runtimes in jdtls's
// initializationOptions to the discovered JDKs unless the client sent
// runtimes or settings that are not an object. It reports whether options
// changed.
func (session *session) addJdtlsRuntimes(options map[string]json.RawMessage) bool {
	settings, err := jsonObject(options["settings"])
	if err != nil {
		return false
	}
	if _, dotted := settings["java.configuration.runtimes"]; dotted {
		return false
	}
	java, err := jsonObject(settings["java"])
	if err != nil {
		return false
	}
	configuration, err := jsonObject(java["configuration"])
	if err != nil {
		return false
	}
	if _, ok := configuration["runtimes"]; ok {
		return false
	}
	runtimes := discoverJdtlsRuntimes()
	if len(runtimes) == 0 {
		session.log("lifecycle jdtls runtimes none found")
		return false
	}
	configuration["runtimes"], _ = json.Marshal(runtimes)
	java["configuration"], _ = json.Marshal(configuration)
	settings["java"], _ = json.Marshal(java)
	options["settings"], _ = json.Marshal(settings)
	defaultMajor := 0
	if index := slices.IndexFunc(runtimes, func(runtime jdtlsRuntime) bool { return runtime.Default }); index >= 0 {
		defaultMajor = runtimeMajor(runtimes[index])
	}
	session.log(fmt.Sprintf("lifecycle jdtls runtimes configured count=%d default=%d", len(runtimes), defaultMajor))
	return true
}
