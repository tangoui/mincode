package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"mincode/app/plugins/api"
)

// Plugin process launch framework.
//
// Every contribution point that starts a user-visible program from a plugin
// manifest (terminalProfiles, tasks, languageServers) goes through this file:
//
//   - processLaunchSource applies the record gates (point enabled, installed,
//     process plugin, enabled, not failed, listed, active in the workspace,
//     shell capability granted) and copies the host's own entries. The WebView
//     never supplies command, args or env; it names an entry only.
//   - resolveProcessLaunch turns an unresolved spec into argv/env/cwd, hashes a
//     ${pluginRoot} executable and (v2) the ${pluginRoot} files named by
//     arguments, and computes the launch fingerprint.
//   - The trust store keeps one confirmed fingerprint per entry in the host KV
//     key "<pluginId>.<point>.trust" (namespace __host__), which data export,
//     import and configuration sync never include. A launch needs a stored
//     confirmation of exactly its fingerprint in every approval mode.
//   - verifyProcessLaunchFiles re-hashes every pinned plugin file without the
//     cache immediately before the start and returns a BeforeExec check that
//     refuses a file swapped between that check and the exec (TOCTOU).
//   - processLaunchSessionTable tags live sessions with the fingerprint they
//     were started with so a remounted consumer can re-attach only to its own
//     unchanged launch.
//
// Fingerprint v1 (terminalProfiles only, kept byte-identical for stored
// confirmations) hashes {pluginId, profileId, argv, env, cwd, executableSha256}.
// Fingerprint v2 (new points) hashes {v:2, point, pluginId, pluginVersion,
// devGeneration, entryKey, argv, env, cwd, executableSha256, pluginFiles,
// extra}; deferred variables (resolved per run, e.g. a task input) stay
// literal so one confirmation covers every value.

const (
	processLaunchErrNotFound    = "not_found"
	processLaunchErrForbidden   = "forbidden"
	processLaunchErrUntrusted   = "untrusted"
	processLaunchErrChanged     = "changed"
	processLaunchErrUnavailable = "unavailable"
	processLaunchErrLimit       = "limit"

	processLaunchTrustVersion       = 2
	processLaunchTrustLegacyVersion = 1
	processLaunchTrustEntriesMax    = 256
	processLaunchPluginFilesMax     = 4
	processLaunchFingerprintVersion = 2

	processLaunchVarPluginRoot = "${pluginRoot}"
)

// processLaunchTrustPoints are the points whose trust keys are deleted on
// uninstall and Clear Plugin Data.
var processLaunchTrustPoints = []string{"terminalProfiles", "tasks", "languageServers", "mcpServers", "acpAgents", "debuggers", "jdtlsBundles"}

var (
	processLaunchBareCommand       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
	processLaunchEnvNamePattern    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	processLaunchWindowsAbsolute   = regexp.MustCompile(`^[A-Za-z]:\\`)
	processLaunchVariablePattern   = regexp.MustCompile(`\$\{[^}]*\}`)
	processLaunchPluginRootFlagArg = regexp.MustCompile(`^--[A-Za-z0-9][A-Za-z0-9._-]*=\$\{pluginRoot\}/(.+)$`)

	processLaunchTrustMu sync.Mutex

	processLaunchHashMu    sync.Mutex
	processLaunchHashCache = map[string]processLaunchHashEntry{}

	processLaunchSessionMu     sync.Mutex
	processLaunchSessionSeq    uint64
	processLaunchSessionTables []*processLaunchSessionTable
)

type processLaunchHashEntry struct {
	size    int64
	modTime time.Time
	sum     string
}

func processLaunchError(code, format string, args ...any) error {
	return fmt.Errorf("%s: %s", code, fmt.Sprintf(format, args...))
}

// processLaunchErrorCode returns the "<code>:" prefix of a host error, or "".
func processLaunchErrorCode(err error) string {
	code, _, ok := strings.Cut(err.Error(), ":")
	if !ok {
		return ""
	}
	switch code {
	case processLaunchErrNotFound, processLaunchErrForbidden, processLaunchErrUntrusted, processLaunchErrChanged, processLaunchErrUnavailable, processLaunchErrLimit:
		return code
	}
	return ""
}

// processLaunchRecord is the gated, copied view of one plugin record.
type processLaunchRecord struct {
	pluginID      string
	pluginName    string
	pluginVersion string
	root          string
	// devGeneration identifies the development-session generation a record
	// was loaded from ("" for installed plugins), so a rebuilt generation
	// always needs a new confirmation.
	devGeneration string
	entries       []any
}

// processLaunchSource applies every record gate for point and returns a copy
// of the host's own entries. noun names the entries in messages.
func (manager *pluginManager) processLaunchSource(pluginID, point, noun string) (processLaunchRecord, error) {
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return processLaunchRecord{}, processLaunchError(processLaunchErrNotFound, "pluginId is required")
	}
	if _, _, ok := enabledContributionPoint(point); !ok {
		return processLaunchRecord{}, processLaunchError(processLaunchErrUnavailable, "%s are not enabled in this host", noun)
	}
	record, err := manager.record(pluginID)
	if err != nil {
		return processLaunchRecord{}, processLaunchError(processLaunchErrUnavailable, "plugin %q is not installed", pluginID)
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	if record.manifest == nil || record.builtin != nil {
		return processLaunchRecord{}, processLaunchError(processLaunchErrUnavailable, "plugin %q has no %s", pluginID, noun)
	}
	if record.manifest.isDeclarative() {
		return processLaunchRecord{}, processLaunchError(processLaunchErrForbidden, "declarative plugin %q cannot contribute %s", pluginID, noun)
	}
	if !record.enabled {
		return processLaunchRecord{}, processLaunchError(processLaunchErrUnavailable, "plugin %q is disabled", pluginID)
	}
	if record.state == pluginStateError || !record.contributionsListedLocked() {
		return processLaunchRecord{}, processLaunchError(processLaunchErrUnavailable, "plugin %q is not available", pluginID)
	}
	if !manager.workspaceActiveLocked(record) {
		return processLaunchRecord{}, processLaunchError(processLaunchErrUnavailable, "plugin %q is not active in this workspace", pluginID)
	}
	if !slices.Contains(record.capabilities, api.CapabilityShell) {
		return processLaunchRecord{}, processLaunchError(processLaunchErrForbidden, "plugin %q is not granted the shell capability", pluginID)
	}
	resolved := record.manifest.contributions[point]
	if resolved == nil {
		return processLaunchRecord{}, processLaunchError(processLaunchErrUnavailable, "plugin %q has no %s", pluginID, noun)
	}
	source := processLaunchRecord{
		pluginID: pluginID, pluginName: record.manifest.Name, pluginVersion: record.manifest.Version,
		root: record.root, entries: append([]any(nil), resolved.Entries...),
	}
	source.devGeneration = processLaunchDevGenerationLocked(record)
	return source, nil
}

// processLaunchDevGenerationLocked identifies the development-session
// generation of record ("" for installed plugins). Caller holds record.mu.
func processLaunchDevGenerationLocked(record *pluginRecord) string {
	if record.source != pluginSourceDevelopment {
		return ""
	}
	if filepath.Base(filepath.Dir(record.root)) != "generations" {
		return record.developmentID + ":" + record.root
	}
	return filepath.Base(record.root)
}

// processLaunchSpec is an unresolved launch as stored in the host snapshot.
type processLaunchSpec struct {
	Command string
	Args    []string
	Env     map[string]string
	Cwd     string
}

// processLaunchPolicy tells resolveProcessLaunch how a point resolves.
type processLaunchPolicy struct {
	Point    string
	EntryKey string
	// Variables maps every resolvable variable ("${pluginRoot}", ...) to its
	// value for this launch. ${pluginRoot} must be present.
	Variables map[string]string
	// Deferred variables stay literal in argv/env and the fingerprint.
	Deferred []string
	// AllowAbsolute permits an absolute command path (terminalProfiles).
	AllowAbsolute bool
	// HashPluginFiles hashes ${pluginRoot} files named by arguments (v2).
	HashPluginFiles bool
	// Extra is point-specific canonical JSON included in the v2 fingerprint.
	Extra json.RawMessage
	// LegacyTerminalProfileFingerprint selects the v1 fingerprint.
	LegacyTerminalProfileFingerprint bool
}

// ProcessLaunchFile is one hashed ${pluginRoot} file named by an argument.
type ProcessLaunchFile struct {
	Relative string `json:"relative"`
	SHA256   string `json:"sha256"`
	path     string
}

// ResolvedProcessLaunch is the resolved launch shown in a confirmation
// dialog. Argv[0] is the resolved executable. Callers fill in Title.
type ResolvedProcessLaunch struct {
	PluginID          string              `json:"pluginId"`
	PluginName        string              `json:"pluginName"`
	PluginVersion     string              `json:"pluginVersion"`
	Point             string              `json:"point"`
	EntryKey          string              `json:"entryKey"`
	Title             string              `json:"title"`
	TitleLocalized    map[string]string   `json:"titleLocalized,omitempty"`
	Argv              []string            `json:"argv"`
	Env               map[string]string   `json:"env"`
	Cwd               string              `json:"cwd"`
	PluginExecutable  bool                `json:"pluginExecutable"`
	ExecutableSHA256  string              `json:"executableSha256,omitempty"`
	PluginFiles       []ProcessLaunchFile `json:"pluginFiles"`
	Fingerprint       string              `json:"fingerprint"`
	Trusted           bool                `json:"trusted"`
	PreviouslyTrusted bool                `json:"previouslyTrusted"`
}

// processLaunchPluginRoot resolves the plugin directory for ${pluginRoot}.
func processLaunchPluginRoot(root string) (string, error) {
	if rootAbs, err := filepath.Abs(root); err == nil && strings.TrimSpace(root) != "" {
		if resolved, err := filepath.EvalSymlinks(rootAbs); err == nil {
			return resolved, nil
		}
	}
	return "", processLaunchError(processLaunchErrUnavailable, "the plugin directory is unavailable")
}

// resolveProcessLaunch resolves spec for src under policy and reports whether
// the result is confirmed. It never starts anything.
func resolveProcessLaunch(src processLaunchRecord, spec processLaunchSpec, policy processLaunchPolicy) (ResolvedProcessLaunch, error) {
	pointSpec, ok := api.ContributionPointSpecFor(policy.Point)
	if !ok {
		return ResolvedProcessLaunch{}, processLaunchError(processLaunchErrUnavailable, "point %q is unknown", policy.Point)
	}
	if _, ok := policy.Variables[processLaunchVarPluginRoot]; !ok {
		return ResolvedProcessLaunch{}, processLaunchError(processLaunchErrUnavailable, "the plugin directory is unavailable")
	}
	// The denylist is enforced at install and again here, so a contract
	// update applies to already installed packages.
	for name := range spec.Env {
		if err := validateProcessLaunchEnvName(pointSpec, name); err != nil {
			return ResolvedProcessLaunch{}, processLaunchError(processLaunchErrForbidden, "%s %q %v", policy.Point, policy.EntryKey, err)
		}
	}
	hashMax := pointSpec.Limits["pluginExecutableHashMaxBytes"]
	executable, pluginExecutable, err := resolveProcessLaunchCommand(src.root, spec.Command, policy.AllowAbsolute)
	if err != nil {
		return ResolvedProcessLaunch{}, err
	}
	executableHash := ""
	if pluginExecutable {
		if executableHash, err = processLaunchFileHash(executable, hashMax); err != nil {
			return ResolvedProcessLaunch{}, err
		}
	}
	cwd, err := resolveProcessLaunchCwd(src.root, spec.Cwd, policy.Variables)
	if err != nil {
		return ResolvedProcessLaunch{}, err
	}
	expand := processLaunchExpander(policy.Variables, policy.Deferred)
	argv := make([]string, 0, len(spec.Args)+1)
	argv = append(argv, executable)
	for _, arg := range spec.Args {
		argv = append(argv, expand.Replace(arg))
	}
	env := make(map[string]string, len(spec.Env))
	for name, value := range spec.Env {
		env[name] = expand.Replace(value)
	}
	launch := ResolvedProcessLaunch{
		PluginID: src.pluginID, PluginName: src.pluginName, PluginVersion: src.pluginVersion,
		Point: policy.Point, EntryKey: policy.EntryKey,
		Argv: argv, Env: env, Cwd: cwd,
		PluginExecutable: pluginExecutable, ExecutableSHA256: executableHash,
		PluginFiles: []ProcessLaunchFile{},
	}
	if policy.HashPluginFiles {
		maxFiles := int(pointSpec.Limits["pluginRootFilesMax"])
		if maxFiles <= 0 {
			maxFiles = processLaunchPluginFilesMax
		}
		if launch.PluginFiles, err = hashProcessLaunchPluginFiles(src.root, spec.Args, maxFiles, hashMax); err != nil {
			return ResolvedProcessLaunch{}, err
		}
	}
	if policy.LegacyTerminalProfileFingerprint {
		launch.Fingerprint = legacyTerminalProfileFingerprint(launch)
	} else {
		launch.Fingerprint = processLaunchFingerprintV2(launch, src.devGeneration, policy.Extra)
	}
	if err := applyProcessLaunchTrust(&launch); err != nil {
		return ResolvedProcessLaunch{}, err
	}
	return launch, nil
}

func processLaunchExpander(variables map[string]string, deferred []string) *strings.Replacer {
	pairs := []string{}
	for _, name := range slices.Sorted(maps.Keys(variables)) {
		if slices.Contains(deferred, name) {
			continue
		}
		pairs = append(pairs, name, variables[name])
	}
	return strings.NewReplacer(pairs...)
}

// validateProcessLaunchEnvName enforces the env name pattern and the point's
// contract denylist (names and prefixes, case-insensitive).
func validateProcessLaunchEnvName(spec api.ContributionPointSpec, name string) error {
	if !processLaunchEnvNamePattern.MatchString(name) {
		return fmt.Errorf("env name %q must match ^[A-Za-z_][A-Za-z0-9_]{0,63}$", name)
	}
	upper := strings.ToUpper(name)
	if upperSet(spec.List("deniedEnvNames"))[upper] {
		return fmt.Errorf("env name %q is not allowed (it changes how programs are loaded or where the shell reads configuration)", name)
	}
	for _, prefix := range upperList(spec.List("deniedEnvPrefixes")) {
		if strings.HasPrefix(upper, prefix) {
			return fmt.Errorf("env name %q uses the denied prefix %s", name, prefix)
		}
	}
	return nil
}

// validateProcessLaunchText checks an argument or environment value: valid
// UTF-8, bounded, single line, no bidi overrides, and "${...}" only for
// variables (none when variables is empty). "$VAR" stays a literal.
func validateProcessLaunchText(value string, maxRunes int, variables []string) error {
	if !utf8.ValidString(value) {
		return errors.New("must be valid UTF-8")
	}
	if utf8.RuneCountInString(value) > maxRunes {
		return fmt.Errorf("exceeds %d characters", maxRunes)
	}
	for _, r := range value {
		if isStatusBarUnsafeRune(r, false) {
			return errors.New("must not contain control or bidirectional override characters")
		}
	}
	for _, match := range processLaunchVariablePattern.FindAllString(value, -1) {
		if !slices.Contains(variables, match) {
			if len(variables) == 0 {
				return fmt.Errorf("uses variable %s, which is not allowed here", match)
			}
			return fmt.Errorf("uses unknown variable %s (allowed: %s)", match, strings.Join(variables, ", "))
		}
	}
	if stripped := processLaunchVariablePattern.ReplaceAllString(value, ""); strings.Contains(stripped, "${") {
		return errors.New("has an unterminated ${ variable")
	}
	return nil
}

// checkProcessLaunchRelative validates a "/"-separated package-relative path
// without empty, "." or ".." segments.
func checkProcessLaunchRelative(relative string) (string, error) {
	if relative == "" || strings.HasPrefix(relative, "/") || strings.HasSuffix(relative, "/") {
		return "", errors.New("must name a file inside the plugin directory")
	}
	if strings.ContainsAny(relative, "\\\x00") {
		return "", errors.New("must use / separators")
	}
	for _, segment := range strings.Split(relative, "/") {
		switch segment {
		case "", ".":
			return "", errors.New("must not contain empty or . segments")
		case "..":
			return "", errors.New("must not contain .. segments")
		}
	}
	return relative, nil
}

// resolveProcessLaunchExecutable resolves a ${pluginRoot} executable
// symlink-safely inside the plugin root and requires a regular file (with an
// execute bit on Unix).
func resolveProcessLaunchExecutable(root, relative string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errors.New("${pluginRoot} is unavailable")
	}
	resolved, err := resolvePluginPath(root, relative)
	if err != nil {
		return "", fmt.Errorf("${pluginRoot}/%s: %w", relative, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("${pluginRoot}/%s: %w", relative, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("${pluginRoot}/%s is not a regular file", relative)
	}
	if !processLaunchExecutableModeOK(info) {
		return "", fmt.Errorf("${pluginRoot}/%s is not executable", relative)
	}
	return resolved, nil
}

func processLaunchExecutableModeOK(info os.FileInfo) bool {
	return runtime.GOOS == "windows" || info.Mode().Perm()&0o111 != 0
}

// resolveProcessLaunchCommand returns the absolute executable and whether it
// lives inside the plugin root.
func resolveProcessLaunchCommand(root, command string, allowAbsolute bool) (string, bool, error) {
	if rest, ok := strings.CutPrefix(command, processLaunchVarPluginRoot+"/"); ok {
		if _, err := checkProcessLaunchRelative(rest); err != nil {
			return "", false, processLaunchError(processLaunchErrForbidden, "command %v", err)
		}
		resolved, err := resolveProcessLaunchExecutable(root, rest)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return "", false, processLaunchError(processLaunchErrNotFound, "%v", err)
			}
			return "", false, processLaunchError(processLaunchErrForbidden, "%v", err)
		}
		return resolved, true, nil
	}
	if processLaunchBareCommand.MatchString(command) {
		if found := lookupProcessLaunchProgram(command); found != "" {
			return found, false, nil
		}
		return "", false, processLaunchError(processLaunchErrNotFound, "program %q was not found on PATH", command)
	}
	if !allowAbsolute {
		return "", false, processLaunchError(processLaunchErrForbidden, "command %q must be ${pluginRoot}/<relative path> or a bare program name", command)
	}
	if !filepath.IsAbs(command) {
		return "", false, processLaunchError(processLaunchErrForbidden, "command %q is not an absolute path on this platform", command)
	}
	info, err := os.Stat(command)
	if err != nil {
		return "", false, processLaunchError(processLaunchErrNotFound, "program %q does not exist", command)
	}
	if !info.Mode().IsRegular() || !processLaunchExecutableModeOK(info) {
		return "", false, processLaunchError(processLaunchErrForbidden, "program %q is not an executable file", command)
	}
	return filepath.Clean(command), false, nil
}

// lookupProcessLaunchProgram searches the login-shell PATH first, then the
// host PATH.
func lookupProcessLaunchProgram(name string) string {
	for _, dir := range filepath.SplitList(processLaunchLoginPath()) {
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && processLaunchExecutableModeOK(info) {
			return candidate
		}
	}
	if found, err := exec.LookPath(name); err == nil {
		if absolute, err := filepath.Abs(found); err == nil {
			return absolute
		}
	}
	return ""
}

// processLaunchLoginPath reads the terminalProfiles seam so tests that stub
// the login PATH cover every point.
func processLaunchLoginPath() string { return terminalProfileLoginPath() }

// resolveProcessLaunchCwd resolves "<variable>" or "<variable>/<relative>".
// ${pluginRoot}/<relative> is resolved symlink-safely inside the plugin.
func resolveProcessLaunchCwd(root, cwd string, variables map[string]string) (string, error) {
	base, rest := cwd, ""
	if index := strings.Index(cwd, "}/"); index >= 0 {
		base, rest = cwd[:index+1], cwd[index+2:]
	}
	dir, ok := variables[base]
	if !ok || dir == "" {
		return "", processLaunchError(processLaunchErrForbidden, "working directory %q is invalid", cwd)
	}
	if base == processLaunchVarPluginRoot && rest != "" {
		resolved, err := resolvePluginPath(root, rest)
		if err != nil {
			return "", processLaunchError(processLaunchErrForbidden, "working directory %v", err)
		}
		dir, rest = resolved, ""
	}
	if rest != "" {
		if _, err := checkProcessLaunchRelative(rest); err != nil {
			return "", processLaunchError(processLaunchErrForbidden, "working directory %v", err)
		}
		dir = filepath.Join(dir, filepath.FromSlash(rest))
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return "", processLaunchError(processLaunchErrNotFound, "working directory %q does not exist", dir)
	}
	return dir, nil
}

// processLaunchFileHash hashes a plugin file, cached by (path, size, mtime).
func processLaunchFileHash(path string, maxBytes int64) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", processLaunchError(processLaunchErrNotFound, "program %q does not exist", path)
	}
	if info.Size() > maxBytes {
		return "", processLaunchError(processLaunchErrForbidden, "plugin file exceeds %d bytes", maxBytes)
	}
	processLaunchHashMu.Lock()
	cached, ok := processLaunchHashCache[path]
	processLaunchHashMu.Unlock()
	if ok && cached.size == info.Size() && cached.modTime.Equal(info.ModTime()) {
		return cached.sum, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return "", processLaunchError(processLaunchErrNotFound, "open plugin file: %v", err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, io.LimitReader(file, maxBytes+1)); err != nil {
		return "", processLaunchError(processLaunchErrUnavailable, "hash plugin file: %v", err)
	}
	sum := hex.EncodeToString(hash.Sum(nil))
	processLaunchHashMu.Lock()
	processLaunchHashCache[path] = processLaunchHashEntry{size: info.Size(), modTime: info.ModTime(), sum: sum}
	processLaunchHashMu.Unlock()
	return sum, nil
}

func forgetProcessLaunchHash(path string) {
	processLaunchHashMu.Lock()
	delete(processLaunchHashCache, path)
	processLaunchHashMu.Unlock()
}

// processLaunchPluginFileRelative returns the relative path of an argument
// that is exactly "${pluginRoot}/<rel>" or "--name=${pluginRoot}/<rel>".
func processLaunchPluginFileRelative(arg string) (string, bool) {
	if rest, ok := strings.CutPrefix(arg, processLaunchVarPluginRoot+"/"); ok {
		return rest, !strings.Contains(rest, "${")
	}
	if match := processLaunchPluginRootFlagArg.FindStringSubmatch(arg); match != nil {
		return match[1], !strings.Contains(match[1], "${")
	}
	return "", false
}

// hashProcessLaunchPluginFiles hashes the regular ${pluginRoot} files named by
// whole arguments (deduplicated, sorted). Directories and missing paths are
// not hashed; a path escaping the plugin directory is refused.
func hashProcessLaunchPluginFiles(root string, args []string, maxFiles int, maxBytes int64) ([]ProcessLaunchFile, error) {
	seen := map[string]bool{}
	files := []ProcessLaunchFile{}
	for _, arg := range args {
		relative, ok := processLaunchPluginFileRelative(arg)
		if !ok || seen[relative] {
			continue
		}
		seen[relative] = true
		if _, err := checkProcessLaunchRelative(relative); err != nil {
			return nil, processLaunchError(processLaunchErrForbidden, "argument ${pluginRoot}/%s %v", relative, err)
		}
		resolved, err := resolvePluginPath(root, relative)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, processLaunchError(processLaunchErrForbidden, "argument ${pluginRoot}/%s: %v", relative, err)
		}
		info, err := os.Stat(resolved)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if len(files) == maxFiles {
			return nil, processLaunchError(processLaunchErrLimit, "arguments name more than %d plugin files", maxFiles)
		}
		sum, err := processLaunchFileHash(resolved, maxBytes)
		if err != nil {
			return nil, err
		}
		files = append(files, ProcessLaunchFile{Relative: relative, SHA256: sum, path: resolved})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Relative < files[j].Relative })
	return files, nil
}

// legacyTerminalProfileFingerprint is fingerprint v1. Its canonical JSON
// (including encoding/json's HTML escaping) must never change: stored
// terminal profile confirmations depend on it.
func legacyTerminalProfileFingerprint(launch ResolvedProcessLaunch) string {
	canonical, _ := json.Marshal(struct {
		PluginID         string            `json:"pluginId"`
		ProfileID        string            `json:"profileId"`
		Argv             []string          `json:"argv"`
		Env              map[string]string `json:"env"`
		Cwd              string            `json:"cwd"`
		ExecutableSHA256 string            `json:"executableSha256"`
	}{launch.PluginID, launch.EntryKey, launch.Argv, launch.Env, launch.Cwd, launch.ExecutableSHA256})
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

func processLaunchFingerprintV2(launch ResolvedProcessLaunch, devGeneration string, extra json.RawMessage) string {
	if len(extra) == 0 {
		extra = nil
	}
	canonical, _ := json.Marshal(struct {
		V                int                 `json:"v"`
		Point            string              `json:"point"`
		PluginID         string              `json:"pluginId"`
		PluginVersion    string              `json:"pluginVersion"`
		DevGeneration    string              `json:"devGeneration,omitempty"`
		EntryKey         string              `json:"entryKey"`
		Argv             []string            `json:"argv"`
		Env              map[string]string   `json:"env"`
		Cwd              string              `json:"cwd"`
		ExecutableSHA256 string              `json:"executableSha256"`
		PluginFiles      []ProcessLaunchFile `json:"pluginFiles"`
		Extra            json.RawMessage     `json:"extra,omitempty"`
	}{processLaunchFingerprintVersion, launch.Point, launch.PluginID, launch.PluginVersion, devGeneration, launch.EntryKey,
		launch.Argv, launch.Env, launch.Cwd, launch.ExecutableSHA256, launch.PluginFiles, extra})
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

// processLaunchTrustEntry is the confirmation of one entry key; At (unix ms)
// orders eviction when the store is full. Fingerprint is the latest confirmed
// launch. Points listed in processLaunchFingerprintsPerEntry also keep the
// other recently confirmed launches of the same key in Others (newest first),
// so switching between them does not ask again.
type processLaunchTrustEntry struct {
	Fingerprint string   `json:"fingerprint"`
	Others      []string `json:"others,omitempty"`
	At          int64    `json:"at"`
}

// processLaunchFingerprintsPerEntry is how many confirmed fingerprints a
// point keeps per entry key; points not listed keep one, so a new
// confirmation replaces the old one. A debugger's entry key is the debugger
// type and project, but its fingerprint also names the interpreter or JDK a
// run configuration resolves to (and launch versus attach for jdtls
// adapters), so a project with several run configurations would otherwise
// ask again on every switch. Every kept fingerprint was reviewed by the user;
// a plugin version, development generation or plugin file change still
// invalidates all of them because each fingerprint covers those.
var processLaunchFingerprintsPerEntry = map[string]int{
	debuggerPointKey: 8,
}

// confirms reports whether fingerprint is one of the entry's confirmations.
func (entry processLaunchTrustEntry) confirms(fingerprint string) bool {
	return fingerprint != "" && (entry.Fingerprint == fingerprint || slices.Contains(entry.Others, fingerprint))
}

// withConfirmed returns the entry with fingerprint as the latest confirmation,
// keeping at most keep confirmations in total.
func (entry processLaunchTrustEntry) withConfirmed(fingerprint string, keep int, at int64) processLaunchTrustEntry {
	next := processLaunchTrustEntry{Fingerprint: fingerprint, At: at}
	if keep <= 1 {
		return next
	}
	for _, old := range append([]string{entry.Fingerprint}, entry.Others...) {
		if old == "" || old == fingerprint || slices.Contains(next.Others, old) {
			continue
		}
		if len(next.Others) >= keep-1 {
			break
		}
		next.Others = append(next.Others, old)
	}
	return next
}

type processLaunchTrust struct {
	Version int                                `json:"version"`
	Entries map[string]processLaunchTrustEntry `json:"entries"`
}

type processLaunchLegacyTrust struct {
	Version  int               `json:"version"`
	Profiles map[string]string `json:"profiles"`
}

func processLaunchTrustKey(pluginID, point string) string {
	return pluginID + "." + point + ".trust"
}

// readProcessLaunchTrust reads the store; the terminalProfiles v1 layout
// {"version":1,"profiles":{...}} is accepted. An unreadable record only means
// "not confirmed".
func readProcessLaunchTrust(pluginID, point string) (processLaunchTrust, error) {
	store := processLaunchTrust{Version: processLaunchTrustVersion, Entries: map[string]processLaunchTrustEntry{}}
	raw, found, err := pluginKVGetRaw(hostPluginID, processLaunchTrustKey(pluginID, point))
	if err != nil || !found {
		return store, err
	}
	var probe struct {
		Version int `json:"version"`
	}
	if json.Unmarshal(raw, &probe) != nil {
		return store, nil
	}
	switch probe.Version {
	case processLaunchTrustVersion:
		var stored processLaunchTrust
		if json.Unmarshal(raw, &stored) == nil {
			keep := processLaunchFingerprintsPerEntry[point]
			for key, entry := range stored.Entries {
				if entry.Fingerprint == "" {
					continue
				}
				// Keep only what this point allows (a hand-edited or older
				// record must not widen the confirmations).
				entry.Others = slices.DeleteFunc(slices.Clone(entry.Others), func(f string) bool { return f == "" || f == entry.Fingerprint })
				if keep <= 1 {
					entry.Others = nil
				} else if len(entry.Others) > keep-1 {
					entry.Others = entry.Others[:keep-1]
				}
				store.Entries[key] = entry
			}
		}
	case processLaunchTrustLegacyVersion:
		var legacy processLaunchLegacyTrust
		if json.Unmarshal(raw, &legacy) == nil {
			for key, fingerprint := range legacy.Profiles {
				if fingerprint != "" {
					store.Entries[key] = processLaunchTrustEntry{Fingerprint: fingerprint}
				}
			}
		}
	}
	return store, nil
}

// writeProcessLaunchTrust stores at most maxEntries entries, evicting the
// oldest confirmations first.
func writeProcessLaunchTrust(pluginID, point string, store processLaunchTrust, maxEntries int) error {
	if maxEntries <= 0 {
		maxEntries = processLaunchTrustEntriesMax
	}
	for len(store.Entries) > maxEntries {
		oldestKey, oldestAt := "", int64(0)
		for _, key := range slices.Sorted(maps.Keys(store.Entries)) {
			if at := store.Entries[key].At; oldestKey == "" || at < oldestAt {
				oldestKey, oldestAt = key, at
			}
		}
		delete(store.Entries, oldestKey)
	}
	store.Version = processLaunchTrustVersion
	raw, err := json.Marshal(store)
	if err != nil {
		return err
	}
	return pluginKVSetRaw(hostPluginID, processLaunchTrustKey(pluginID, point), raw)
}

func applyProcessLaunchTrust(launch *ResolvedProcessLaunch) error {
	store, err := readProcessLaunchTrust(launch.PluginID, launch.Point)
	if err != nil {
		return processLaunchError(processLaunchErrUnavailable, "read %s confirmations: %v", launch.Point, err)
	}
	if stored, ok := store.Entries[launch.EntryKey]; ok {
		launch.Trusted = stored.confirms(launch.Fingerprint)
		launch.PreviouslyTrusted = !launch.Trusted
	}
	return nil
}

// trustProcessLaunch stores the confirmation only when reviewed equals the
// freshly resolved launch's fingerprint. what names the entry in messages.
func trustProcessLaunch(launch ResolvedProcessLaunch, reviewed, what string) (ResolvedProcessLaunch, error) {
	if strings.TrimSpace(reviewed) != launch.Fingerprint {
		return ResolvedProcessLaunch{}, processLaunchError(processLaunchErrChanged, "%s changed since it was reviewed", what)
	}
	maxEntries := processLaunchTrustEntriesMax
	if spec, ok := api.ContributionPointSpecFor(launch.Point); ok && spec.Limits["trustEntriesMax"] > 0 {
		maxEntries = int(spec.Limits["trustEntriesMax"])
	}
	processLaunchTrustMu.Lock()
	defer processLaunchTrustMu.Unlock()
	store, err := readProcessLaunchTrust(launch.PluginID, launch.Point)
	if err != nil {
		return ResolvedProcessLaunch{}, processLaunchError(processLaunchErrUnavailable, "read %s confirmations: %v", launch.Point, err)
	}
	store.Entries[launch.EntryKey] = store.Entries[launch.EntryKey].withConfirmed(launch.Fingerprint, processLaunchFingerprintsPerEntry[launch.Point], time.Now().UnixMilli())
	if err := writeProcessLaunchTrust(launch.PluginID, launch.Point, store, maxEntries); err != nil {
		return ResolvedProcessLaunch{}, processLaunchError(processLaunchErrUnavailable, "save %s confirmation: %v", launch.Point, err)
	}
	launch.Trusted, launch.PreviouslyTrusted = true, false
	return launch, nil
}

// checkProcessLaunchStart refuses a start that is not confirmed or whose
// reviewed fingerprint differs from the fresh launch.
func checkProcessLaunchStart(launch ResolvedProcessLaunch, reviewed, what string) error {
	if !launch.Trusted {
		if launch.PreviouslyTrusted {
			return processLaunchError(processLaunchErrChanged, "%s changed and must be reviewed again", what)
		}
		return processLaunchError(processLaunchErrUntrusted, "%s has not been confirmed", what)
	}
	if strings.TrimSpace(reviewed) != launch.Fingerprint {
		return processLaunchError(processLaunchErrChanged, "%s changed since it was reviewed", what)
	}
	return nil
}

func deleteProcessLaunchTrust(pluginID string) {
	processLaunchTrustMu.Lock()
	defer processLaunchTrustMu.Unlock()
	for _, point := range processLaunchTrustPoints {
		_ = pluginKVDeleteRaw(hostPluginID, processLaunchTrustKey(pluginID, point))
	}
}

// processLaunchFileIdentity pins a plugin file that was verified by hash.
type processLaunchFileIdentity struct {
	path string
	info os.FileInfo
}

// verifyProcessLaunchFile re-reads path without the hash cache and requires
// its sha256 to equal want. The file is opened once; the hash is taken from
// that handle, and the handle, the path (without following a symlink) and a
// second stat after hashing must all name the same unchanged file.
func verifyProcessLaunchFile(path, want string, maxBytes int64) (*processLaunchFileIdentity, error) {
	changed := func(detail string) error {
		forgetProcessLaunchHash(path)
		return processLaunchError(processLaunchErrChanged, "plugin file %q %s; review the launch again", path, detail)
	}
	before, err := os.Lstat(path)
	if err != nil {
		return nil, processLaunchError(processLaunchErrNotFound, "program %q does not exist", path)
	}
	if !before.Mode().IsRegular() {
		return nil, changed("is no longer a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, processLaunchError(processLaunchErrNotFound, "open plugin file: %v", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, changed("was replaced while it was checked")
	}
	if opened.Size() > maxBytes {
		return nil, processLaunchError(processLaunchErrForbidden, "plugin file exceeds %d bytes", maxBytes)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, io.LimitReader(file, maxBytes+1)); err != nil {
		return nil, processLaunchError(processLaunchErrUnavailable, "hash plugin file: %v", err)
	}
	if sum := hex.EncodeToString(hash.Sum(nil)); sum != want {
		return nil, changed("changed after it was reviewed")
	}
	identity := &processLaunchFileIdentity{path: path, info: opened}
	if err := identity.verifyUnchanged(); err != nil {
		return nil, err
	}
	return identity, nil
}

// verifyUnchanged fails when the path no longer names the verified file with
// the same size and modification time. It runs again immediately before the
// process is created.
func (identity *processLaunchFileIdentity) verifyUnchanged() error {
	current, err := os.Lstat(identity.path)
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(identity.info, current) ||
		current.Size() != identity.info.Size() || !current.ModTime().Equal(identity.info.ModTime()) {
		forgetProcessLaunchHash(identity.path)
		return processLaunchError(processLaunchErrChanged, "plugin file %q changed before it could start; review the launch again", identity.path)
	}
	return nil
}

// verifyProcessLaunchFiles re-hashes the ${pluginRoot} executable and every
// hashed plugin file right before a start and returns the BeforeExec check
// that pins them until the exec (nil when nothing is pinned).
func verifyProcessLaunchFiles(launch ResolvedProcessLaunch) (func() error, error) {
	spec, _ := api.ContributionPointSpecFor(launch.Point)
	maxBytes := spec.Limits["pluginExecutableHashMaxBytes"]
	identities := []*processLaunchFileIdentity{}
	if launch.PluginExecutable {
		identity, err := verifyProcessLaunchFile(launch.Argv[0], launch.ExecutableSHA256, maxBytes)
		if err != nil {
			return nil, err
		}
		identities = append(identities, identity)
	}
	for _, file := range launch.PluginFiles {
		identity, err := verifyProcessLaunchFile(file.path, file.SHA256, maxBytes)
		if err != nil {
			return nil, err
		}
		identities = append(identities, identity)
	}
	if len(identities) == 0 {
		return nil, nil
	}
	return func() error {
		for _, identity := range identities {
			if err := identity.verifyUnchanged(); err != nil {
				return err
			}
		}
		return nil
	}, nil
}

// processLaunchSession tags a live session started from an entry with the
// fingerprint the user confirmed for it. token distinguishes successive starts
// under the same session ID so a late exit callback never removes a newer
// entry.
type processLaunchSession struct {
	pluginID    string
	point       string
	entryKey    string
	fingerprint string
	token       uint64
}

// processLaunchSessionTable is one point's live-session map. mu and entries
// point at package variables so a point can keep its historical names.
type processLaunchSessionTable struct {
	mu      *sync.Mutex
	entries *map[string]processLaunchSession
}

func newProcessLaunchSessionTable(mu *sync.Mutex, entries *map[string]processLaunchSession) *processLaunchSessionTable {
	table := &processLaunchSessionTable{mu: mu, entries: entries}
	processLaunchSessionMu.Lock()
	processLaunchSessionTables = append(processLaunchSessionTables, table)
	processLaunchSessionMu.Unlock()
	return table
}

func (table *processLaunchSessionTable) register(sessionID string, session processLaunchSession) uint64 {
	processLaunchSessionMu.Lock()
	processLaunchSessionSeq++
	session.token = processLaunchSessionSeq
	processLaunchSessionMu.Unlock()
	table.mu.Lock()
	defer table.mu.Unlock()
	(*table.entries)[sessionID] = session
	return session.token
}

func (table *processLaunchSessionTable) lookup(sessionID string) (processLaunchSession, bool) {
	table.mu.Lock()
	defer table.mu.Unlock()
	session, ok := (*table.entries)[sessionID]
	return session, ok
}

// forget drops the entry for sessionID; token 0 drops it unconditionally,
// any other token only when it still matches.
func (table *processLaunchSessionTable) forget(sessionID string, token uint64) {
	table.mu.Lock()
	defer table.mu.Unlock()
	if session, ok := (*table.entries)[sessionID]; ok && (token == 0 || session.token == token) {
		delete(*table.entries, sessionID)
	}
}

// purge forgets every session of pluginID. The processes keep running (they
// are user processes); they can no longer be re-attached.
func (table *processLaunchSessionTable) purge(pluginID string) {
	table.mu.Lock()
	defer table.mu.Unlock()
	for sessionID, session := range *table.entries {
		if session.pluginID == pluginID {
			delete(*table.entries, sessionID)
		}
	}
}

// checkReattach allows re-attaching to a live session only when it was
// started by the same entry with exactly the currently confirmed fingerprint.
// isLive reports whether the session still runs; what names the entry.
func (table *processLaunchSessionTable) checkReattach(sessionID string, launch ResolvedProcessLaunch, attachIfExists bool, isLive func(string) bool, what string) error {
	session, owned := table.lookup(sessionID)
	switch {
	case !attachIfExists:
		return processLaunchError(processLaunchErrForbidden, "session %q is already running", sessionID)
	case !owned || session.pluginID != launch.PluginID || session.point != launch.Point || session.entryKey != launch.EntryKey:
		return processLaunchError(processLaunchErrUnavailable, "session %q can no longer be re-attached to %s; close it to start again", sessionID, what)
	case session.fingerprint != launch.Fingerprint:
		return processLaunchError(processLaunchErrUnavailable, "session %q runs an earlier launch of %s; close it to start the reviewed one", sessionID, what)
	}
	if !isLive(sessionID) {
		table.forget(sessionID, session.token)
		return processLaunchError(processLaunchErrUnavailable, "session %q has ended", sessionID)
	}
	return nil
}

func purgeProcessLaunchSessions(pluginID string) {
	processLaunchSessionMu.Lock()
	tables := append([]*processLaunchSessionTable(nil), processLaunchSessionTables...)
	processLaunchSessionMu.Unlock()
	for _, table := range tables {
		table.purge(pluginID)
	}
}

func init() {
	registerRecordRemovalHook(func(_ *pluginManager, pluginID string, _ removalReason) {
		purgeProcessLaunchSessions(pluginID)
		deleteProcessLaunchTrust(pluginID)
	})
	// Disable ends re-attach rights; stop, crash and reload do not (a stopped
	// on-demand plugin still owns its launches).
	registerRecordLifecycleHook(func(_ *pluginManager, record *pluginRecord) {
		record.mu.Lock()
		disabled := !record.enabled
		pluginID := record.id
		record.mu.Unlock()
		if disabled {
			purgeProcessLaunchSessions(pluginID)
		}
	})
}
