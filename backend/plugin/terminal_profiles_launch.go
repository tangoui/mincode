package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"

	"mincode/app/backend/core"
	"mincode/app/backend/terminal"
)

// Plugin terminal profile launch path, a thin caller of the generic process
// launch framework (plugin_process_launch.go).
//
// Trust model: the WebView names a profile only by (pluginId, profileId) plus
// an optional workspace folder. The host resolves command, args, env and cwd
// from its own contribution snapshot, verifies the plugin is enabled, not
// failed, effective in the active workspace and still granted the shell
// capability, and computes a launch fingerprint over the resolved argv, env,
// cwd and (for ${pluginRoot} executables) the executable's sha256. A launch
// needs a stored user confirmation of exactly that fingerprint; any change
// re-prompts. This is deliberately stricter than start_local_shell, whose
// customPath/extraEnv come from the user's own terminal settings.
//
// Confirmations are host KV entries "<pluginId>.terminalProfiles.trust" in the
// __host__ namespace, which plugin data export/import and configuration sync
// never include (sessions, trust removal and TOCTOU checks live in the generic
// framework). They survive disable and upgrade (an upgrade that changes the
// spec or executable changes the fingerprint) and are deleted on uninstall and
// Clear Plugin Data. AI tools have no path to these commands.

const (
	terminalProfileErrNotFound    = processLaunchErrNotFound
	terminalProfileErrForbidden   = processLaunchErrForbidden
	terminalProfileErrUntrusted   = processLaunchErrUntrusted
	terminalProfileErrChanged     = processLaunchErrChanged
	terminalProfileErrUnavailable = processLaunchErrUnavailable
)

func terminalProfileError(code, format string, args ...any) error {
	return processLaunchError(code, format, args...)
}

// TerminalProfileLaunch is the resolved launch shown in the confirmation
// dialog. Argv[0] is the resolved executable.
type TerminalProfileLaunch struct {
	PluginID         string            `json:"pluginId"`
	PluginName       string            `json:"pluginName"`
	ProfileID        string            `json:"profileId"`
	Title            string            `json:"title"`
	TitleLocalized   map[string]string `json:"titleLocalized,omitempty"`
	Argv             []string          `json:"argv"`
	Env              map[string]string `json:"env"`
	Cwd              string            `json:"cwd"`
	PluginExecutable bool              `json:"pluginExecutable"`
	ExecutableSHA256 string            `json:"executableSha256,omitempty"`
	Fingerprint      string            `json:"fingerprint"`
	Trusted          bool              `json:"trusted"`
	// PreviouslyTrusted is true when an older fingerprint was confirmed, so
	// the dialog can say the launch changed.
	PreviouslyTrusted bool `json:"previouslyTrusted"`
}

type terminalProfilePrepareRequest struct {
	PluginID        string `json:"pluginId"`
	ProfileID       string `json:"profileId"`
	WorkspaceFolder string `json:"workspaceFolder"`
}

type terminalProfileTrustRequest struct {
	PluginID        string `json:"pluginId"`
	ProfileID       string `json:"profileId"`
	Fingerprint     string `json:"fingerprint"`
	WorkspaceFolder string `json:"workspaceFolder"`
}

type terminalProfileStartRequest struct {
	PluginID        string  `json:"pluginId"`
	ProfileID       string  `json:"profileId"`
	Fingerprint     string  `json:"fingerprint"`
	SessionID       string  `json:"sessionId"`
	WorkspaceFolder string  `json:"workspaceFolder"`
	Cols            *uint32 `json:"cols"`
	Rows            *uint32 `json:"rows"`
	AttachIfExists  bool    `json:"attachIfExists"`
}

// terminalProfileSession is the generic launch session tag.
type terminalProfileSession = processLaunchSession

var (
	// terminalProfileSessions tags live sessions started from a profile so a
	// remounted pane can re-attach to its own running process. Re-attaching
	// runs the same gates as a launch (enabled, effective, shell granted,
	// confirmed fingerprint equal to the one the session was started with).
	// Entries are dropped when the session ends (process exit or close), and
	// when the plugin is disabled, uninstalled or its data is cleared; the
	// running process itself is never killed by that purge.
	terminalProfileSessionsMu   sync.Mutex
	terminalProfileSessions     = map[string]terminalProfileSession{}
	terminalProfileSessionTable = newProcessLaunchSessionTable(&terminalProfileSessionsMu, &terminalProfileSessions)

	// Test seams. terminalProfileAttachShell only reports whether a live
	// local session with the ID exists; it never starts anything.
	terminalProfileStartShell  = terminal.StartProfileShell
	terminalProfileAttachShell = terminal.AttachLocalShell
	terminalProfileLoginPath   = core.LoginShellPath
	terminalProfileGOOS        = runtime.GOOS
)

func terminalProfileTrustKey(pluginID string) string {
	return processLaunchTrustKey(pluginID, terminalProfilePointKey)
}

type terminalProfileSource struct {
	record  processLaunchRecord
	profile TerminalProfileContribution
}

// terminalProfileSourceFor applies every record gate and returns a copy of
// the host's own profile entry.
func (manager *pluginManager) terminalProfileSourceFor(pluginID, profileID string) (terminalProfileSource, error) {
	profileID = strings.TrimSpace(profileID)
	if strings.TrimSpace(pluginID) == "" || profileID == "" {
		return terminalProfileSource{}, terminalProfileError(terminalProfileErrNotFound, "pluginId and profileId are required")
	}
	record, err := manager.processLaunchSource(pluginID, terminalProfilePointKey, "terminal profiles")
	if err != nil {
		return terminalProfileSource{}, err
	}
	for _, entry := range record.entries {
		profile, ok := entry.(TerminalProfileContribution)
		if !ok || profile.ID != profileID {
			continue
		}
		profile.Args = append([]string(nil), profile.Args...)
		profile.Env = cloneStringMap(profile.Env)
		profile.Platforms = append([]string(nil), profile.Platforms...)
		profile.TitleLocalized = cloneStringMap(profile.TitleLocalized)
		record.entries = nil
		return terminalProfileSource{record: record, profile: profile}, nil
	}
	return terminalProfileSource{}, terminalProfileError(terminalProfileErrUnavailable, "plugin %q no longer contributes terminal profile %q", record.pluginID, profileID)
}

type terminalProfileVarValues struct {
	workspaceFolder string
	userHome        string
	pluginRoot      string
}

func (vars terminalProfileVarValues) table() map[string]string {
	return map[string]string{
		terminalProfileVarWorkspace:  vars.workspaceFolder,
		terminalProfileVarUserHome:   vars.userHome,
		terminalProfileVarPluginRoot: vars.pluginRoot,
	}
}

// prepareTerminalProfile resolves the launch and reports whether it is
// confirmed. It never starts anything.
func (manager *pluginManager) prepareTerminalProfile(pluginID, profileID, workspaceFolder string) (TerminalProfileLaunch, error) {
	resolved, err := manager.resolveTerminalProfileLaunch(pluginID, profileID, workspaceFolder)
	if err != nil {
		return TerminalProfileLaunch{}, err
	}
	return terminalProfileLaunchFrom(resolved), nil
}

func (manager *pluginManager) resolveTerminalProfileLaunch(pluginID, profileID, workspaceFolder string) (ResolvedProcessLaunch, error) {
	source, err := manager.terminalProfileSourceFor(pluginID, profileID)
	if err != nil {
		return ResolvedProcessLaunch{}, err
	}
	profile := source.profile
	if !slices.Contains(profile.Platforms, terminalProfileGOOS) {
		return ResolvedProcessLaunch{}, terminalProfileError(terminalProfileErrUnavailable, "terminal profile %q does not support %s", profile.ID, terminalProfileGOOS)
	}
	// The denylist is enforced at install and again here, so a contract
	// update applies to already installed packages.
	for name := range profile.Env {
		if err := validateTerminalProfileEnvName(name); err != nil {
			return ResolvedProcessLaunch{}, terminalProfileError(terminalProfileErrForbidden, "terminal profile %q %v", profile.ID, err)
		}
	}
	vars, err := resolveTerminalProfileVariables(source.record.root, workspaceFolder)
	if err != nil {
		return ResolvedProcessLaunch{}, err
	}
	launch, err := resolveProcessLaunch(source.record,
		processLaunchSpec{Command: profile.Command, Args: profile.Args, Env: profile.Env, Cwd: profile.Cwd},
		processLaunchPolicy{
			Point: terminalProfilePointKey, EntryKey: profile.ID, Variables: vars.table(),
			AllowAbsolute: true, LegacyTerminalProfileFingerprint: true,
		})
	if err != nil {
		return ResolvedProcessLaunch{}, err
	}
	launch.Title, launch.TitleLocalized = profile.Title, profile.TitleLocalized
	return launch, nil
}

func terminalProfileLaunchFrom(launch ResolvedProcessLaunch) TerminalProfileLaunch {
	return TerminalProfileLaunch{
		PluginID: launch.PluginID, PluginName: launch.PluginName, ProfileID: launch.EntryKey,
		Title: launch.Title, TitleLocalized: launch.TitleLocalized,
		Argv: launch.Argv, Env: launch.Env, Cwd: launch.Cwd,
		PluginExecutable: launch.PluginExecutable, ExecutableSHA256: launch.ExecutableSHA256,
		Fingerprint: launch.Fingerprint, Trusted: launch.Trusted, PreviouslyTrusted: launch.PreviouslyTrusted,
	}
}

func resolveTerminalProfileVariables(root, workspaceFolder string) (terminalProfileVarValues, error) {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return terminalProfileVarValues{}, terminalProfileError(terminalProfileErrUnavailable, "the user home directory is unknown")
	}
	vars := terminalProfileVarValues{userHome: home, workspaceFolder: home}
	if folder := strings.TrimSpace(workspaceFolder); folder != "" {
		if !filepath.IsAbs(folder) || strings.ContainsRune(folder, 0) {
			return terminalProfileVarValues{}, terminalProfileError(terminalProfileErrForbidden, "workspaceFolder must be an absolute path")
		}
		info, err := os.Stat(folder)
		if err != nil || !info.IsDir() {
			return terminalProfileVarValues{}, terminalProfileError(terminalProfileErrNotFound, "workspace folder %q does not exist", folder)
		}
		vars.workspaceFolder = filepath.Clean(folder)
	}
	if vars.pluginRoot, err = processLaunchPluginRoot(root); err != nil {
		return terminalProfileVarValues{}, err
	}
	return vars, nil
}

// terminalProfileLaunchFingerprint is fingerprint v1 (see
// legacyTerminalProfileFingerprint); it must stay byte-identical.
func terminalProfileLaunchFingerprint(launch TerminalProfileLaunch) string {
	return legacyTerminalProfileFingerprint(ResolvedProcessLaunch{
		PluginID: launch.PluginID, EntryKey: launch.ProfileID, Argv: launch.Argv, Env: launch.Env,
		Cwd: launch.Cwd, ExecutableSHA256: launch.ExecutableSHA256,
	})
}

// trustTerminalProfile stores the confirmation only when fingerprint equals
// the freshly resolved launch.
func (manager *pluginManager) trustTerminalProfile(request terminalProfileTrustRequest) (TerminalProfileLaunch, error) {
	resolved, err := manager.resolveTerminalProfileLaunch(request.PluginID, request.ProfileID, request.WorkspaceFolder)
	if err != nil {
		return TerminalProfileLaunch{}, err
	}
	trusted, err := trustProcessLaunch(resolved, request.Fingerprint, fmt.Sprintf("terminal profile %q", resolved.EntryKey))
	if err != nil {
		return TerminalProfileLaunch{}, err
	}
	return terminalProfileLaunchFrom(trusted), nil
}

func lookupTerminalProfileSession(sessionID string) (terminalProfileSession, bool) {
	return terminalProfileSessionTable.lookup(sessionID)
}

// startTerminalProfile launches a confirmed profile in a new PTY session, or
// re-attaches to a live session this profile started earlier. Both paths run
// every gate first: re-attaching after the plugin was disabled, left the
// workspace, lost the shell capability or changed its launch is refused, and
// a live session is never replaced by a new process.
func (manager *pluginManager) startTerminalProfile(request terminalProfileStartRequest) (string, error) {
	sessionID := strings.TrimSpace(request.SessionID)
	if sessionID == "" {
		return "", terminalProfileError(terminalProfileErrForbidden, "sessionId is required")
	}
	live := terminalProfileAttachShell(sessionID)
	if !live {
		// The exit callback normally removes the entry; this only drops a
		// stale one.
		terminalProfileSessionTable.forget(sessionID, 0)
	}
	launch, err := manager.resolveTerminalProfileLaunch(request.PluginID, request.ProfileID, request.WorkspaceFolder)
	if err != nil {
		return "", err
	}
	what := fmt.Sprintf("terminal profile %q", launch.EntryKey)
	if err := checkProcessLaunchStart(launch, request.Fingerprint, what); err != nil {
		return "", err
	}
	if live {
		if err := terminalProfileSessionTable.checkReattach(sessionID, launch, request.AttachIfExists, terminalProfileAttachShell, what); err != nil {
			return "", err
		}
		return "Shell attached", nil
	}

	// Re-hash the plugin-shipped executable right before the start (bypassing
	// the (size, mtime) cache) and pin its file identity so a swap between
	// this check and the exec is refused.
	beforeExec, err := verifyProcessLaunchFiles(launch)
	if err != nil {
		return "", err
	}
	token := terminalProfileSessionTable.register(sessionID, terminalProfileSession{
		pluginID: launch.PluginID, point: launch.Point, entryKey: launch.EntryKey, fingerprint: launch.Fingerprint,
	})
	message, err := terminalProfileStartShell(terminal.ProfileShellSpec{
		SessionID: sessionID, Executable: launch.Argv[0], Args: launch.Argv[1:], Env: launch.Env, Cwd: launch.Cwd,
		Cols: request.Cols, Rows: request.Rows,
		BeforeExec: beforeExec,
		OnExit:     func() { terminalProfileSessionTable.forget(sessionID, token) },
	})
	if err != nil {
		terminalProfileSessionTable.forget(sessionID, token)
		if code := processLaunchErrorCode(err); code != "" {
			return "", err
		}
		return "", terminalProfileError(terminalProfileErrUnavailable, "%v", err)
	}
	return message, nil
}

func pluginHostTerminalProfilePrepareHandler(_, args string) (string, error) {
	var request terminalProfilePrepareRequest
	if err := core.DecodeArgsPayload(args, &request); err != nil {
		return "", err
	}
	launch, err := host().prepareTerminalProfile(request.PluginID, request.ProfileID, request.WorkspaceFolder)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(launch)
}

func pluginHostTerminalProfileTrustHandler(_, args string) (string, error) {
	var request terminalProfileTrustRequest
	if err := core.DecodeArgsPayload(args, &request); err != nil {
		return "", err
	}
	launch, err := host().trustTerminalProfile(request)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(launch)
}

func pluginHostTerminalProfileStartHandler(_, args string) (string, error) {
	var request terminalProfileStartRequest
	if err := core.DecodeArgsPayload(args, &request); err != nil {
		return "", err
	}
	message, err := host().startTerminalProfile(request)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(message)
}

func init() {
	core.Register("plugin_host_terminal_profile_prepare", pluginHostTerminalProfilePrepareHandler)
	core.Register("plugin_host_terminal_profile_trust", pluginHostTerminalProfileTrustHandler)
	core.Register("plugin_host_terminal_profile_start", pluginHostTerminalProfileStartHandler)
}
