package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"mincode/app/backend/debug"
)

// jdtls debug adapters (adapter.kind "jdtls"): java-debug runs inside the
// built-in jdtls as one of the plugin's confirmed jdtlsBundles jars, so the
// host starts no process.
//
//   - prepare requires that the plugin owns the adapter command through a
//     confirmed, unchanged jar (jdtlsCommandRoute) and that the project's
//     running jdtls was started with that jar; otherwise it fails with
//     "unavailable: java-debug is not loaded into the Java language
//     service" so the frontend runs the bundle flow first. It runs no jdtls
//     command. The launch shown for confirmation is
//     ["jdtls", "workspace/executeCommand", <command>] with the plugin's jars
//     and hashes as PluginFiles; its fingerprint (processLaunchFingerprintV2)
//     covers plugin id, version, development generation, project, command,
//     jar hashes and {type, kind, javaExec identity when explicit}, and it is confirmed like any debugger;
//   - start re-checks the confirmation, re-resolves the launch (a changed
//     jar or an unloaded bundle refuses the start), runs the command in jdtls
//     (lsp.ExecuteBuiltinCommand, jdtlsCommandTimeout, arguments []), requires
//     an integer port 1-65535 and connects to 127.0.0.1:<port>
//     (debug.ConnectAdapter). Ending the session closes the connection only;
//     java-debug manages the debuggee.

const (
	debuggerJdtlsNotLoaded = "java-debug is not loaded into the Java language service"
	// debuggerJdtlsMethod is the second argv word of the confirmation.
	debuggerJdtlsMethod      = "workspace/executeCommand"
	debuggerJdtlsResultRunes = 64
)

// debuggerConnectAdapter is debug.ConnectAdapter; tests may replace it.
var debuggerConnectAdapter = debug.ConnectAdapter

// resolveJdtlsDebuggerLaunch builds the confirmation of a jdtls adapter for
// the canonical project root; outsideCwd (debuggerOutsideCwd, judged once at
// prepare) is part of its fingerprint.
func (manager *pluginManager) resolveJdtlsDebuggerLaunch(src processLaunchRecord, debugger DebuggerContribution, root string, arguments json.RawMessage, outsideCwd string) (ResolvedProcessLaunch, error) {
	adapter, err := validateDebuggerAdapter(src.root, debugger.Adapter.Kind, debugger.Adapter.Command, debugger.Adapter.Args, debugger.Adapter.Env)
	if err != nil {
		return ResolvedProcessLaunch{}, processLaunchError(processLaunchErrForbidden, "debugger %q adapter %v", debugger.Type, err)
	}
	owner, status, jar := manager.jdtlsCommandRoute(adapter.Command)
	if owner != src.pluginID || jar == "" {
		return ResolvedProcessLaunch{}, processLaunchError(processLaunchErrUnavailable, debuggerJdtlsNotLoaded)
	}
	loaded, running := jdtlsSessionBundles(root)
	if !running || !slices.Contains(loaded, jar) {
		return ResolvedProcessLaunch{}, processLaunchError(processLaunchErrUnavailable, debuggerJdtlsNotLoaded)
	}
	files := make([]ProcessLaunchFile, 0, len(status.Jars))
	for _, jar := range status.Jars {
		files = append(files, ProcessLaunchFile{Relative: jar.relative, SHA256: jar.SHA256, path: jar.path})
	}
	slices.SortFunc(files, func(a, b ProcessLaunchFile) int { return strings.Compare(a.Relative, b.Relative) })
	javaExec, err := resolveJdtlsJavaExec(arguments)
	if err != nil {
		return ResolvedProcessLaunch{}, err
	}
	extra, _ := json.Marshal(struct {
		Type     string                 `json:"type"`
		Kind     string                 `json:"kind"`
		JavaExec *jdtlsJavaExecIdentity `json:"javaExec,omitempty"`
		Cwd      string                 `json:"cwd,omitempty"`
	}{debugger.Type, adapter.Kind, javaExec, outsideCwd})
	launch := ResolvedProcessLaunch{
		PluginID: src.pluginID, PluginName: src.pluginName, PluginVersion: src.pluginVersion,
		Point: debuggerPointKey, EntryKey: taskEntryKey(debugger.Type, root),
		Title: debugger.Title, TitleLocalized: debugger.TitleLocalized,
		Argv: []string{jdtlsServerID, debuggerJdtlsMethod, adapter.Command},
		Env:  map[string]string{}, Cwd: root, PluginFiles: files,
	}
	launch.Fingerprint = processLaunchFingerprintV2(launch, src.devGeneration, extra)
	if err := applyProcessLaunchTrust(&launch); err != nil {
		return ResolvedProcessLaunch{}, err
	}
	return launch, nil
}

// connectJdtlsDebugger re-resolves a confirmed jdtls launch, runs its command
// in jdtls and connects to the returned port.
func (manager *pluginManager) connectJdtlsDebugger(ctx context.Context, entry *preparedDebugEntry, launch ResolvedProcessLaunch, sessionRoot string, valid func() bool) (*debug.Session, error) {
	prepared := entry.prepared
	what := debuggerWhat(prepared.Type)
	fresh, err := manager.resolveJdtlsDebuggerLaunch(entry.src, entry.debugger, prepared.ProjectRoot, prepared.Arguments, entry.outsideCwd)
	if err != nil {
		return nil, err
	}
	if fresh.Fingerprint != launch.Fingerprint {
		return nil, processLaunchError(processLaunchErrChanged, "%s changed and must be reviewed again", what)
	}
	command := fresh.Argv[2]
	commandCtx, cancel := context.WithTimeout(ctx, jdtlsCommandTimeout)
	defer cancel()
	result, err := jdtlsExecuteCommand(commandCtx, prepared.ProjectRoot, jdtlsServerID, command, json.RawMessage("[]"))
	if err != nil {
		if err.Error() == "unavailable: Java language service is not running" {
			return nil, err
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, processLaunchError(processLaunchErrUnavailable, "%s timed out after %s", command, jdtlsCommandTimeout)
		}
		return nil, processLaunchError(processLaunchErrUnavailable, "%s: %s", command, boundedJdtlsMessage(err.Error()))
	}
	port, ok := debuggerJdtlsPort(result)
	if !ok {
		return nil, processLaunchError(processLaunchErrUnavailable, "%s returned %s instead of a port number (1-65535)", command, debuggerJdtlsResultExcerpt(result))
	}
	connectCtx, cancelConnect := context.WithTimeout(ctx, debuggerTCPConnectTimeout+5*time.Second)
	defer cancelConnect()
	return debuggerConnectAdapter(connectCtx, debug.ConnectSpec{
		Port: port, OwnerPluginID: launch.PluginID, DebuggerType: prepared.Type, ProjectRoot: sessionRoot,
		Valid: valid, MaxMessageBytes: debuggerMessageMaxBytes, ConnectTimeout: debuggerTCPConnectTimeout,
		MaxSessionsPerOwner: debuggerSessionsMax,
	})
}

// debuggerJdtlsPort accepts a JSON integer 1-65535 (no string, fraction or
// exponent).
func debuggerJdtlsPort(raw json.RawMessage) (int, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil || decoder.More() {
		return 0, false
	}
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	port, err := strconv.Atoi(number.String())
	if err != nil || port < 1 || port > 65535 {
		return 0, false
	}
	return port, true
}

func debuggerJdtlsResultExcerpt(raw json.RawMessage) string {
	text := strings.ToValidUTF8(strings.TrimSpace(string(raw)), "�")
	if text == "" {
		return "nothing"
	}
	if utf8.RuneCountInString(text) > debuggerJdtlsResultRunes {
		text = string([]rune(text)[:debuggerJdtlsResultRunes]) + "…"
	}
	return text
}
