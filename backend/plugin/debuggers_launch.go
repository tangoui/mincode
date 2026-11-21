package plugin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"mincode/app/backend/core"
	"mincode/app/backend/debug"
	"mincode/app/plugins/api"
)

// Plugin debugger launch path: a caller of the generic process launch
// framework (plugin_process_launch.go), of invokeContributedAction for the
// plugin's read-only actions, and of package debug for the adapter.
//
// The WebView names a debugger only by (pluginId, type, projectRoot) plus the
// user's run configuration. Starting is three steps:
//
//  1. prepare calls the plugin's side-effect-free resolveAction, validates its
//     {variables, request, arguments} result, resolves the adapter launch
//     (${resolved.<name>} as the whole command must be an absolute path to a
//     regular executable; ${port} stays literal in argv and the fingerprint)
//     and caches it under a random PrepareID for prepareTTL;
//  2. trust stores the confirmation of exactly the reviewed fingerprint
//     (plugin id, version, development generation, file hashes, final command
//     line, project root); it is required in every approval mode;
//  3. start consumes the PrepareID, re-reads the confirmation, re-checks the
//     plugin and the debugger entry, re-hashes pinned plugin files and starts
//     the adapter through debug.StartAdapter (a jdtls adapter instead runs its
//     command in the built-in jdtls and connects through debug.ConnectAdapter,
//     see debuggers_jdtls.go), then runs the DAP launch sequence
//     (debug.Session.Begin).
//
// The DAP arguments and request come from the resolve result and the user's
// run configuration; they are not part of the fingerprint, so editing a run
// configuration needs no new confirmation while a different interpreter does.
// Explicit jdtls javaExec paths/targets/bytes are interpreter identity too.
// Trust is kept per debugger and project (EntryKey = type@<root hash>), with
// up to 8 confirmed launches per key (processLaunchFingerprintsPerEntry), so
// run configurations that resolve different interpreters, or a jdtls launch
// and an attach, can alternate without asking again.
// Disable, uninstall, reload and upgrade stop the plugin's sessions; a stop or
// crash of the plugin's own process does not. AI tools have no path here.

var (
	debuggerResolveTimeout         = time.Duration(debuggerSpec.Limit("resolveTimeoutMs")) * time.Millisecond
	debuggerResolveMaxBytes        = int(debuggerSpec.Limit("resolveResultMaxBytes"))
	debuggerEntryPointsTimeout     = time.Duration(debuggerSpec.Limit("entryPointsTimeoutMs")) * time.Millisecond
	debuggerEntryPointsMax         = int(debuggerSpec.Limit("entryPointsMax"))
	debuggerCheckTimeout           = time.Duration(debuggerSpec.Limit("checkTimeoutMs")) * time.Millisecond
	debuggerSettingsTimeout        = time.Duration(debuggerSpec.Limit("settingsTimeoutMs")) * time.Millisecond
	debuggerExceptionTypesMax      = int(debuggerSpec.Limit("exceptionTypesMax"))
	debuggerExceptionTypeRunes     = int(debuggerSpec.Limit("exceptionTypeMaxRunes"))
	debuggerFunctionBreakpointsMax = int(debuggerSpec.Limit("functionBreakpointsMax"))
	debuggerDataBreakpointsMax     = int(debuggerSpec.Limit("dataBreakpointsMax"))
	debuggerMessageMaxBytes        = int(debuggerSpec.Limit("messageMaxBytes"))
	debuggerStderrMaxBytes         = int(debuggerSpec.Limit("stderrMaxBytes"))
	debuggerSessionsMax            = int(debuggerSpec.Limit("sessionsPerPlugin"))
	debuggerTCPConnectTimeout      = time.Duration(debuggerSpec.Limit("tcpConnectTimeoutMs")) * time.Millisecond
	debuggerResolvedVariablesMx    = 32
	// debuggerAddressBreakpointsMax bounds the debugger addresses (not
	// breakpoints) of one start request's addressBreakpoints.
	debuggerAddressBreakpointsMax = 256

	debuggerNow          = time.Now
	debuggerStartAdapter = debug.StartAdapter
	debuggerInvoke       = func(manager *pluginManager, ctx context.Context, call contributedActionCall) (json.RawMessage, error) {
		return manager.invokeContributedAction(ctx, call)
	}
	debuggerStopForOwner = debug.StopForOwner
	debuggerStopSession  = debug.StopSession
)

const (
	// debuggerPrepareTTL is how long a prepared launch can be trusted/started.
	debuggerPrepareTTL = 2 * time.Minute
	// debuggerPreparedMax bounds cached prepared launches (oldest evicted).
	debuggerPreparedMax = 16
	// debuggerBeginTimeout bounds the DAP launch sequence.
	debuggerBeginTimeout = 60 * time.Second
	// debuggerActionConcurrency is the per-plugin slot count of resolve,
	// entry point, check and settings calls together.
	debuggerActionConcurrency = 4
	debuggerMissingMax        = 16
	debuggerExceptionFilterMx = 64
	// debuggerSettingsResultMaxBytes bounds the settings result, which must
	// be {}.
	debuggerSettingsResultMaxBytes = 1024
	// debuggerBreakpointNameRunes bounds a function breakpoint name and a
	// data breakpoint dataId; debuggerBreakpointConditionRunes a condition or
	// hit condition of either.
	debuggerBreakpointNameRunes      = 1024
	debuggerBreakpointConditionRunes = 4096
)

// debuggerDataAccessTypes are the DAP DataBreakpointAccessType values.
var debuggerDataAccessTypes = []string{"read", "write", "readWrite"}

// debuggerExceptionTypePattern is the generic dotted identifier an exception
// type list item must be (ASCII letters, digits, _ and $; segments joined by
// single dots). The host does not judge whether it names a real type.
var debuggerExceptionTypePattern = regexp.MustCompile(`^[A-Za-z_$][\w$]*(\.[A-Za-z_$][\w$]*)*$`)

// PreparedDebugLaunch is a resolved debugger launch awaiting confirmation or
// start. Launch is shown in the confirmation dialog; Launch.Cwd is the
// project root and ${port} stays literal in Launch.Argv.
type PreparedDebugLaunch struct {
	PrepareID   string                `json:"prepareId"`
	Launch      ResolvedProcessLaunch `json:"launch"`
	Type        string                `json:"type"`
	Kind        string                `json:"kind"`
	ProjectRoot string                `json:"projectRoot"`
	Request     string                `json:"request"`
	Arguments   json.RawMessage       `json:"arguments"`
}

type preparedDebugEntry struct {
	prepared PreparedDebugLaunch
	src      processLaunchRecord
	debugger DebuggerContribution
	spec     string // DebuggerContribution.SpecFingerprint
	// requestedRoot is the project root as the frontend spelled it, so
	// breakpoint paths under either spelling pass debug.SourcePathInProject.
	requestedRoot string
	// outsideCwd is debuggerOutsideCwd of the prepared arguments, judged once
	// at prepare; the same value is fingerprinted (also when a jdtls launch is
	// re-resolved at start) and Begin passes it as ConfirmedOutsideCwd.
	outsideCwd string
	// testRunID is the test run (test_runs.go) this launch starts, or "";
	// start binds the session to it and refuses a run that has ended.
	testRunID string
	expires   time.Time
}

var debuggerPrepared = struct {
	mu      sync.Mutex
	entries map[string]*preparedDebugEntry
}{entries: map[string]*preparedDebugEntry{}}

type debuggerListItem struct {
	PluginID              string            `json:"pluginId"`
	PluginName            string            `json:"pluginName"`
	Type                  string            `json:"type"`
	Title                 string            `json:"title"`
	TitleLocalized        map[string]string `json:"titleLocalized,omitempty"`
	Languages             []string          `json:"languages"`
	ConfigurationSchema   json.RawMessage   `json:"configurationSchema,omitempty"`
	ConfigurationSnippets []json.RawMessage `json:"configurationSnippets"`
	Remote                *DebuggerRemote   `json:"remote,omitempty"`
	HasEntryPoints        bool              `json:"hasEntryPoints"`
	HasCheck              bool              `json:"hasCheck"`
	// ExceptionTypes is set when the debugger offers the "pause only on these
	// exception types" list; HasSettings when it has a settingsAction.
	ExceptionTypes *DebuggerTextHints `json:"exceptionTypes,omitempty"`
	HasSettings    bool               `json:"hasSettings"`
	// FunctionBreakpoints words the method breakpoint input (offered only
	// when the adapter declares supportsFunctionBreakpoints); HotCodeReplace
	// names the adapter's custom code-changed event and apply request.
	FunctionBreakpoints *DebuggerTextHints      `json:"functionBreakpoints,omitempty"`
	HotCodeReplace      *DebuggerHotCodeReplace `json:"hotCodeReplace,omitempty"`
	// AdapterKind is the adapter's kind ("stdio", "tcp", "jdtls"); the
	// frontend loads the jdtls bundles before preparing a "jdtls" debugger.
	AdapterKind string `json:"adapterKind"`
}

type debuggerPrepareRequest struct {
	PluginID      string          `json:"pluginId"`
	Type          string          `json:"type"`
	ProjectRoot   string          `json:"projectRoot"`
	Configuration json.RawMessage `json:"configuration"`
	NoDebug       bool            `json:"noDebug"`
}

type debuggerTrustRequest struct {
	PrepareID   string `json:"prepareId"`
	Fingerprint string `json:"fingerprint"`
}

// debuggerStartRequest starts a prepared launch with the project's
// breakpoints. FunctionBreakpoints and DataBreakpoints (DAP shapes) reach the
// adapter only when it declares supportsFunctionBreakpoints /
// supportsDataBreakpoints; each list has at most functionBreakpointsMax /
// dataBreakpointsMax entries (debuggerFunctionBreakpoints,
// debuggerDataBreakpoints). AddressBreakpoints are line breakpoints keyed by
// a debugger-provided source address (debug.ValidDebuggerAddress), never a
// project path; at most debuggerAddressBreakpointsMax addresses
// (debuggerAddressBreakpoints).
type debuggerStartRequest struct {
	PrepareID           string                              `json:"prepareId"`
	Fingerprint         string                              `json:"fingerprint"`
	Breakpoints         map[string][]debug.SourceBreakpoint `json:"breakpoints"`
	AddressBreakpoints  map[string][]debug.SourceBreakpoint `json:"addressBreakpoints"`
	FunctionBreakpoints []debug.FunctionBreakpoint          `json:"functionBreakpoints"`
	DataBreakpoints     []debug.DataBreakpoint              `json:"dataBreakpoints"`
	ExceptionFilters    []string                            `json:"exceptionFilters"`
}

// DebuggerStartResult is the started session. SkippedBreakpoints lists the
// breakpoint paths that failed the project-root check (stale or outside).
type DebuggerStartResult struct {
	SessionID          string          `json:"sessionId"`
	Capabilities       json.RawMessage `json:"capabilities"`
	SkippedBreakpoints []string        `json:"skippedBreakpoints"`
}

type debuggerEntryPointsRequest struct {
	PluginID    string `json:"pluginId"`
	Type        string `json:"type"`
	ProjectRoot string `json:"projectRoot"`
	File        string `json:"file"`
	LanguageID  string `json:"languageId"`
}

// DebuggerEntryPoint is one runnable line offered by entryPointsAction.
type DebuggerEntryPoint struct {
	Line          int             `json:"line"`
	Label         string          `json:"label"`
	Configuration json.RawMessage `json:"configuration"`
}

// debuggerCheckRequest names the debugger to check. Configuration is the
// run configuration about to start (optional): it reaches checkAction as
// payload.configuration, so the plugin checks the interpreter or toolchain
// that configuration selects.
type debuggerCheckRequest struct {
	PluginID      string          `json:"pluginId"`
	Type          string          `json:"type"`
	ProjectRoot   string          `json:"projectRoot"`
	Configuration json.RawMessage `json:"configuration,omitempty"`
}

// debuggerSettingsRequest carries the project's saved settings of one
// debugger type. ExceptionTypes is required; an empty list clears the
// plugin's previous filter.
type debuggerSettingsRequest struct {
	PluginID       string   `json:"pluginId"`
	Type           string   `json:"type"`
	ProjectRoot    string   `json:"projectRoot"`
	ExceptionTypes []string `json:"exceptionTypes"`
}

// debuggerSettingsResult is the (empty) result of applying settings.
type debuggerSettingsResult struct{}

// DebuggerCheckResult reports missing tools for a debugger.
type DebuggerCheckResult struct {
	Ready   bool                  `json:"ready"`
	Missing []DebuggerMissingTool `json:"missing"`
}

// DebuggerMissingTool is one missing tool. InstallCommand is only shown; the
// frontend runs it in a visible terminal after the user confirms.
type DebuggerMissingTool struct {
	Name           string `json:"name"`
	Hint           string `json:"hint"`
	InstallCommand string `json:"installCommand,omitempty"`
}

// debuggerGateLocked reports whether record offers debuggers right now: the
// point is enabled and the plugin is an enabled, listed, workspace-effective
// process plugin granted shell. Caller holds record.mu.
func (manager *pluginManager) debuggerGateLocked(record *pluginRecord) bool {
	if _, _, ok := enabledContributionPoint(debuggerPointKey); !ok {
		return false
	}
	return record.manifest != nil && record.builtin == nil && !record.manifest.isDeclarative() &&
		record.contributionsListedLocked() && manager.workspaceActiveLocked(record) &&
		slices.Contains(record.capabilities, api.CapabilityShell) && record.manifest.contributions[debuggerPointKey] != nil
}

// listDebuggers lists the offered debuggers on this platform. A type offered
// by several plugins belongs to the first in plugin order; later entries are
// not listed and cannot be prepared.
func (manager *pluginManager) listDebuggers() []debuggerListItem {
	items := []debuggerListItem{}
	manager.mu.RLock()
	records := manager.orderedRecordsLocked()
	manager.mu.RUnlock()
	seen := map[string]bool{}
	for _, record := range records {
		record.mu.Lock()
		if manager.debuggerGateLocked(record) {
			for _, entry := range record.manifest.contributions[debuggerPointKey].Entries {
				debugger, ok := entry.(DebuggerContribution)
				if !ok || seen[debugger.Type] || !slices.Contains(debugger.Platforms, terminalProfileGOOS) {
					continue
				}
				seen[debugger.Type] = true
				items = append(items, debuggerListItem{
					PluginID: record.id, PluginName: record.manifest.Name,
					Type: debugger.Type, Title: debugger.Title, TitleLocalized: debugger.TitleLocalized,
					Languages: slices.Clone(debugger.Languages), ConfigurationSchema: debugger.ConfigurationSchema,
					ConfigurationSnippets: slices.Clone(debugger.ConfigurationSnippets), Remote: debugger.Remote,
					HasEntryPoints: debugger.EntryPointsAction != "", HasCheck: debugger.CheckAction != "",
					ExceptionTypes: cloneDebuggerTextHints(debugger.ExceptionTypes), HasSettings: debugger.SettingsAction != "",
					FunctionBreakpoints: cloneDebuggerTextHints(debugger.FunctionBreakpoints), HotCodeReplace: cloneDebuggerHotCodeReplace(debugger.HotCodeReplace),
					AdapterKind: debugger.Adapter.Kind,
				})
			}
		}
		record.mu.Unlock()
	}
	return items
}

// debuggerTypeOwner returns the plugin that owns debugger type, or "".
func (manager *pluginManager) debuggerTypeOwner(debuggerType string) string {
	for _, item := range manager.listDebuggers() {
		if item.Type == debuggerType {
			return item.PluginID
		}
	}
	return ""
}

// debuggerSource applies the record gates and returns the plugin's current
// debugger entry and the canonical project root.
func (manager *pluginManager) debuggerSource(pluginID, debuggerType, projectRoot string) (processLaunchRecord, DebuggerContribution, string, error) {
	fail := func(err error) (processLaunchRecord, DebuggerContribution, string, error) {
		return processLaunchRecord{}, DebuggerContribution{}, "", err
	}
	pluginID, debuggerType = strings.TrimSpace(pluginID), strings.TrimSpace(debuggerType)
	if pluginID == "" || !debuggerTypePattern.MatchString(debuggerType) {
		return fail(processLaunchError(processLaunchErrNotFound, "pluginId and a valid debugger type are required"))
	}
	root, err := canonicalProjectLaunchRoot(projectRoot, "debuggers")
	if err != nil {
		return fail(err)
	}
	src, err := manager.processLaunchSource(pluginID, debuggerPointKey, "debuggers")
	if err != nil {
		return fail(err)
	}
	for _, entry := range src.entries {
		debugger, ok := entry.(DebuggerContribution)
		if !ok || debugger.Type != debuggerType {
			continue
		}
		if !slices.Contains(debugger.Platforms, terminalProfileGOOS) {
			return fail(processLaunchError(processLaunchErrUnavailable, "debugger %q does not support %s", debuggerType, terminalProfileGOOS))
		}
		if owner := manager.debuggerTypeOwner(debuggerType); owner != src.pluginID {
			return fail(processLaunchError(processLaunchErrUnavailable, "debugger type %q is provided by plugin %q", debuggerType, owner))
		}
		return src, debugger, root, nil
	}
	return fail(processLaunchError(processLaunchErrUnavailable, "plugin %q no longer contributes debugger %q", src.pluginID, debuggerType))
}

// debuggerUnchanged is the invokeContributedAction Validate check: the
// plugin still offers the same debugger entry with action.
func debuggerUnchanged(debuggerType, specFingerprint, action string) func(record *pluginRecord) error {
	return func(record *pluginRecord) error {
		if !slices.Contains(record.capabilities, api.CapabilityShell) {
			return newContributionError(contributionErrForbidden, "plugin %q is not granted the shell capability", record.id)
		}
		if resolved := record.manifest.contributions[debuggerPointKey]; resolved != nil {
			for _, entry := range resolved.Entries {
				if debugger, ok := entry.(DebuggerContribution); ok && debugger.Type == debuggerType {
					if debugger.SpecFingerprint != specFingerprint || !slices.Contains(debuggerActions(debugger), action) {
						return newContributionError(contributionErrUnavailable, "debugger %q changed", debuggerType)
					}
					return nil
				}
			}
		}
		return newContributionError(contributionErrUnavailable, "debugger %q is no longer contributed", debuggerType)
	}
}

func debuggerRequestID() string {
	var buf [12]byte
	_, _ = rand.Read(buf[:])
	return "debugger-" + hex.EncodeToString(buf[:])
}

// invokeDebuggerAction calls one of the debugger's read-only actions.
func (manager *pluginManager) invokeDebuggerAction(ctx context.Context, src processLaunchRecord, debugger DebuggerContribution, action string, payload any, timeout time.Duration, maxBytes int) (json.RawMessage, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode debugger payload: %w", err)
	}
	return debuggerInvoke(manager, ctx, contributedActionCall{
		PluginID: src.pluginID, Point: debuggerPointKey, EntryID: debugger.Type, ActionID: action,
		RequestID: debuggerRequestID(), Payload: encoded, Timeout: timeout, MaxResultBytes: maxBytes,
		Concurrency: debuggerActionConcurrency,
		Validate:    debuggerUnchanged(debugger.Type, debugger.SpecFingerprint, action),
	})
}

type debuggerResolveResult struct {
	Variables map[string]string `json:"variables"`
	Request   *string           `json:"request"`
	Arguments json.RawMessage   `json:"arguments"`
}

// parseDebuggerResolveResult strictly decodes {variables?, request,
// arguments}. Variable values are plain single-line text (no ${...}).
func parseDebuggerResolveResult(raw json.RawMessage) (debuggerResolveResult, error) {
	invalid := func(format string, args ...any) (debuggerResolveResult, error) {
		return debuggerResolveResult{}, newContributionError(contributionErrInvalidResult, "resolve result "+format, args...)
	}
	var result debuggerResolveResult
	if err := decodeStrictJSON(raw, &result, true); err != nil {
		return invalid("%v", err)
	}
	if result.Request == nil || (*result.Request != debuggerRequestLaunch && *result.Request != debuggerRequestAttach) {
		return invalid("request must be \"launch\" or \"attach\"")
	}
	arguments := strings.TrimSpace(string(result.Arguments))
	if !strings.HasPrefix(arguments, "{") {
		return invalid("arguments must be a JSON object")
	}
	result.Arguments = json.RawMessage(arguments)
	if len(result.Variables) > debuggerResolvedVariablesMx {
		return invalid("has more than %d variables", debuggerResolvedVariablesMx)
	}
	for _, name := range slices.Sorted(maps.Keys(result.Variables)) {
		if !debuggerResolvedName.MatchString(name) {
			return invalid("variable name %q must match %s", name, debuggerResolvedName)
		}
		if err := validateProcessLaunchText(result.Variables[name], debuggerArgMaxRunes, nil); err != nil {
			return invalid("variable %s %v", name, err)
		}
	}
	return result, nil
}

// resolveDebuggerLaunch builds the launch of debugger with the resolve
// result's variables; outsideCwd (debuggerOutsideCwd, judged by the caller)
// is part of its fingerprint.
func resolveDebuggerLaunch(src processLaunchRecord, debugger DebuggerContribution, root string, variables map[string]string, outsideCwd string) (ResolvedProcessLaunch, error) {
	// Re-validate with the current contract lists (a contract update applies
	// to already installed packages).
	adapter, err := validateDebuggerAdapter(src.root, debugger.Adapter.Kind, debugger.Adapter.Command, debugger.Adapter.Args, debugger.Adapter.Env)
	if err != nil {
		return ResolvedProcessLaunch{}, processLaunchError(processLaunchErrForbidden, "debugger %q adapter %v", debugger.Type, err)
	}
	pluginRoot, err := processLaunchPluginRoot(src.root)
	if err != nil {
		return ResolvedProcessLaunch{}, err
	}
	values := map[string]string{
		processLaunchVarPluginRoot: pluginRoot,
		languageServerVarProject:   root,
		debuggerVarPort:            debuggerVarPort,
	}
	for _, arg := range adapter.Args {
		for _, match := range debuggerResolvedVariable.FindAllStringSubmatch(arg, -1) {
			value, ok := variables[match[1]]
			if !ok {
				return ResolvedProcessLaunch{}, newContributionError(contributionErrInvalidResult, "resolve result has no variable %q for %s", match[1], match[0])
			}
			values[match[0]] = value
		}
	}
	command := adapter.Command
	allowAbsolute := false
	if name, ok, _ := debuggerResolvedCommandName(command); ok {
		value, found := variables[name]
		if !found {
			return ResolvedProcessLaunch{}, newContributionError(contributionErrInvalidResult, "resolve result has no variable %q for the adapter command", name)
		}
		if !filepath.IsAbs(value) {
			return ResolvedProcessLaunch{}, processLaunchError(processLaunchErrForbidden, "resolved adapter command %q must be an absolute path", value)
		}
		command, allowAbsolute = filepath.Clean(value), true
		// A resolved executable inside the plugin directory is pinned by hash
		// like a ${pluginRoot} command, so swapping it needs a new review.
		if resolved, err := filepath.EvalSymlinks(command); err == nil {
			if relative, err := filepath.Rel(pluginRoot, resolved); err == nil && filepath.IsLocal(relative) {
				command, allowAbsolute = processLaunchVarPluginRoot+"/"+filepath.ToSlash(relative), false
			}
		}
	}
	extra, _ := json.Marshal(struct {
		Type string `json:"type"`
		Kind string `json:"kind"`
		Cwd  string `json:"cwd,omitempty"`
	}{debugger.Type, adapter.Kind, outsideCwd})
	launch, err := resolveProcessLaunch(src,
		processLaunchSpec{Command: command, Args: adapter.Args, Env: adapter.Env, Cwd: languageServerVarProject},
		processLaunchPolicy{
			Point: debuggerPointKey, EntryKey: taskEntryKey(debugger.Type, root),
			Variables:       values,
			Deferred:        []string{debuggerVarPort},
			AllowAbsolute:   allowAbsolute,
			HashPluginFiles: true,
			Extra:           extra,
		})
	if err != nil {
		return ResolvedProcessLaunch{}, err
	}
	launch.Title, launch.TitleLocalized = debugger.Title, debugger.TitleLocalized
	return launch, nil
}

// debuggerOutsideCwd is the real path of the DAP launch `cwd` when it lies
// outside the project (debug.CwdOutsideProject), else "": a link re-pointed
// elsewhere is another directory and needs a new confirmation. It joins the launch fingerprint (omitted
// when empty, so other launches keep theirs): a working directory outside the
// project, which the debuggee's terminal may then use, is shown in and needs
// its own confirmation, while one in the project needs none.
// It is judged once per prepare, and that one value is both fingerprinted and
// kept with the prepared launch (a variable so tests can pin this).
var debuggerOutsideCwd = func(root string, arguments json.RawMessage) string {
	return debug.CwdOutsideProject(root, debug.LaunchArgumentsCwd(arguments))
}

// prepareDebugger resolves a launch and caches it under a new PrepareID.
func (manager *pluginManager) prepareDebugger(ctx context.Context, request debuggerPrepareRequest) (PreparedDebugLaunch, error) {
	configuration := strings.TrimSpace(string(request.Configuration))
	if configuration == "" {
		configuration = "{}"
	}
	if !strings.HasPrefix(configuration, "{") || !json.Valid([]byte(configuration)) {
		return PreparedDebugLaunch{}, newContributionError(contributionErrInvalidRequest, "configuration must be a JSON object")
	}
	src, debugger, root, err := manager.debuggerSource(request.PluginID, request.Type, request.ProjectRoot)
	if err != nil {
		return PreparedDebugLaunch{}, err
	}
	return manager.prepareDebuggerLaunch(src, debugger, root, request.ProjectRoot, "", func() (debuggerResolveResult, error) {
		raw, err := manager.invokeDebuggerAction(ctx, src, debugger, debugger.ResolveAction, map[string]any{
			"type":          debugger.Type,
			"project":       map[string]string{"root": root},
			"configuration": json.RawMessage(configuration),
			"noDebug":       request.NoDebug,
			"platform":      terminalProfileGOOS,
		}, debuggerResolveTimeout, debuggerResolveMaxBytes)
		if err != nil {
			return debuggerResolveResult{}, err
		}
		return parseDebuggerResolveResult(raw)
	})
}

// prepareDebuggerLaunch is the one prepare path behind run configurations
// (prepareDebugger) and test runs (prepareTestRun): resolve returns the
// validated resolve result, which becomes a prepared launch cached under a
// new PrepareID. testRunID binds the session start to that test run.
func (manager *pluginManager) prepareDebuggerLaunch(src processLaunchRecord, debugger DebuggerContribution, root, requestedRoot, testRunID string, resolve func() (debuggerResolveResult, error)) (PreparedDebugLaunch, error) {
	// A jdtls adapter needs java-debug loaded before resolve can use it; its
	// launch does not depend on the resolve variables.
	var launch ResolvedProcessLaunch
	var err error
	jdtls := debugger.Adapter.Kind == debuggerKindJdtls
	if jdtls {
		if launch, err = manager.resolveJdtlsDebuggerLaunch(src, debugger, root, nil, ""); err != nil {
			return PreparedDebugLaunch{}, err
		}
	}
	result, err := resolve()
	if err != nil {
		return PreparedDebugLaunch{}, err
	}
	// Judged once: this value is fingerprinted and kept for Begin.
	outsideCwd := debuggerOutsideCwd(root, result.Arguments)
	if jdtls {
		// The initial call gates resolve on the loaded bundle; trust is applied
		// again only after the actual debuggee JVM has been resolved.
		if launch, err = manager.resolveJdtlsDebuggerLaunch(src, debugger, root, result.Arguments, outsideCwd); err != nil {
			return PreparedDebugLaunch{}, err
		}
	} else {
		if launch, err = resolveDebuggerLaunch(src, debugger, root, result.Variables, outsideCwd); err != nil {
			return PreparedDebugLaunch{}, err
		}
	}
	prepareID, err := newDebuggerPrepareID()
	if err != nil {
		return PreparedDebugLaunch{}, err
	}
	prepared := PreparedDebugLaunch{
		PrepareID: prepareID, Launch: launch, Type: debugger.Type, Kind: debugger.Adapter.Kind,
		ProjectRoot: root, Request: *result.Request, Arguments: result.Arguments,
	}
	storePreparedDebugLaunch(&preparedDebugEntry{
		prepared: prepared, src: src, debugger: debugger, spec: debugger.SpecFingerprint,
		requestedRoot: cleanRequestedRoot(requestedRoot), outsideCwd: outsideCwd, testRunID: testRunID,
	})
	return prepared, nil
}

// cleanRequestedRoot returns the project root as the frontend spelled it,
// lexically cleaned ("" if empty).
func cleanRequestedRoot(root string) string {
	if root = strings.TrimSpace(root); root == "" {
		return ""
	}
	return filepath.Clean(root)
}

func newDebuggerPrepareID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", processLaunchError(processLaunchErrUnavailable, "create prepare id: %v", err)
	}
	return hex.EncodeToString(buf[:]), nil
}

// storePreparedDebugLaunch caches entry, dropping expired entries and then
// the oldest ones beyond debuggerPreparedMax.
func storePreparedDebugLaunch(entry *preparedDebugEntry) {
	now := debuggerNow()
	entry.expires = now.Add(debuggerPrepareTTL)
	debuggerPrepared.mu.Lock()
	defer debuggerPrepared.mu.Unlock()
	for id, cached := range debuggerPrepared.entries {
		if !now.Before(cached.expires) {
			delete(debuggerPrepared.entries, id)
		}
	}
	for len(debuggerPrepared.entries) >= debuggerPreparedMax {
		oldest := ""
		for id, cached := range debuggerPrepared.entries {
			if oldest == "" || cached.expires.Before(debuggerPrepared.entries[oldest].expires) {
				oldest = id
			}
		}
		delete(debuggerPrepared.entries, oldest)
	}
	debuggerPrepared.entries[entry.prepared.PrepareID] = entry
}

// preparedDebugLaunch returns the cached entry; take also removes it.
func preparedDebugLaunch(prepareID string, take bool) (*preparedDebugEntry, error) {
	prepareID = strings.TrimSpace(prepareID)
	debuggerPrepared.mu.Lock()
	defer debuggerPrepared.mu.Unlock()
	entry := debuggerPrepared.entries[prepareID]
	if entry == nil || prepareID == "" {
		return nil, processLaunchError(processLaunchErrNotFound, "the prepared debug launch is unknown or was already used; prepare it again")
	}
	if !debuggerNow().Before(entry.expires) || take {
		delete(debuggerPrepared.entries, prepareID)
	}
	if !debuggerNow().Before(entry.expires) {
		return nil, processLaunchError(processLaunchErrNotFound, "the prepared debug launch expired; prepare it again")
	}
	copy := *entry
	return &copy, nil
}

func forgetPreparedDebugLaunches(pluginID string) {
	debuggerPrepared.mu.Lock()
	defer debuggerPrepared.mu.Unlock()
	for id, entry := range debuggerPrepared.entries {
		if entry.src.pluginID == pluginID {
			delete(debuggerPrepared.entries, id)
		}
	}
}

func debuggerWhat(debuggerType string) string {
	return fmt.Sprintf("debugger %q", debuggerType)
}

// trustDebugger confirms the reviewed fingerprint of a prepared launch.
func (manager *pluginManager) trustDebugger(request debuggerTrustRequest) (PreparedDebugLaunch, error) {
	entry, err := preparedDebugLaunch(request.PrepareID, false)
	if err != nil {
		return PreparedDebugLaunch{}, err
	}
	trusted, err := trustProcessLaunch(entry.prepared.Launch, request.Fingerprint, debuggerWhat(entry.prepared.Type))
	if err != nil {
		return PreparedDebugLaunch{}, err
	}
	debuggerPrepared.mu.Lock()
	if cached := debuggerPrepared.entries[entry.prepared.PrepareID]; cached != nil {
		cached.prepared.Launch = trusted
	}
	debuggerPrepared.mu.Unlock()
	entry.prepared.Launch = trusted
	return entry.prepared, nil
}

// debuggerValid builds the Valid check of a started session.
func (manager *pluginManager) debuggerValid(src processLaunchRecord, debuggerType, specFingerprint string) func() bool {
	return manager.projectLaunchValid(src, debuggerPointKey, func(entry any) (bool, bool) {
		debugger, ok := entry.(DebuggerContribution)
		if !ok || debugger.Type != debuggerType {
			return false, false
		}
		return true, debugger.SpecFingerprint == specFingerprint
	})
}

// debuggerBreakpoints keeps the breakpoints whose path passes the project
// root check (debug.SourcePathInProject) and returns the other paths.
func debuggerBreakpoints(root string, breakpoints map[string][]debug.SourceBreakpoint) (map[string][]debug.SourceBreakpoint, []string, error) {
	kept := map[string][]debug.SourceBreakpoint{}
	skipped := []string{}
	for _, path := range slices.Sorted(maps.Keys(breakpoints)) {
		for _, breakpoint := range breakpoints[path] {
			if breakpoint.Line < 1 {
				return nil, nil, newContributionError(contributionErrInvalidRequest, "breakpoint line must be at least 1, got %d", breakpoint.Line)
			}
		}
		clean, err := debug.SourcePathInProject(root, path, false)
		if err != nil {
			skipped = append(skipped, path)
			continue
		}
		kept[clean] = append(kept[clean], breakpoints[path]...)
	}
	return kept, skipped, nil
}

// debuggerAddressBreakpoints validates the start request's address
// breakpoints: at most debuggerAddressBreakpointsMax addresses, each passing
// debug.ValidDebuggerAddress (kept verbatim; the host never resolves it), and
// the same per-breakpoint rule as line breakpoints. Any invalid entry refuses
// the whole start, so a bad address never reaches the adapter.
func debuggerAddressBreakpoints(breakpoints map[string][]debug.SourceBreakpoint) (map[string][]debug.SourceBreakpoint, error) {
	if len(breakpoints) > debuggerAddressBreakpointsMax {
		return nil, newContributionError(contributionErrInvalidRequest, "more than %d breakpoint addresses", debuggerAddressBreakpointsMax)
	}
	out := make(map[string][]debug.SourceBreakpoint, len(breakpoints))
	for _, address := range slices.Sorted(maps.Keys(breakpoints)) {
		if _, err := debug.ValidDebuggerAddress(address); err != nil {
			return nil, newContributionError(contributionErrInvalidRequest, "addressBreakpoints: %s", strings.TrimPrefix(err.Error(), "invalid_request: "))
		}
		for _, breakpoint := range breakpoints[address] {
			if breakpoint.Line < 1 {
				return nil, newContributionError(contributionErrInvalidRequest, "addressBreakpoints[%q]: breakpoint line must be at least 1, got %d", address, breakpoint.Line)
			}
		}
		out[address] = breakpoints[address]
	}
	return out, nil
}

func debuggerExceptionFilters(filters []string) ([]string, error) {
	if len(filters) > debuggerExceptionFilterMx {
		return nil, newContributionError(contributionErrInvalidRequest, "more than %d exception filters", debuggerExceptionFilterMx)
	}
	out := []string{}
	for _, filter := range filters {
		value, err := validateContributionLine(filter, debuggerTitleMaxRunes, true)
		if err != nil {
			return nil, newContributionError(contributionErrInvalidRequest, "exception filter %v", err)
		}
		if !slices.Contains(out, value) {
			out = append(out, value)
		}
	}
	return out, nil
}

// debuggerFunctionBreakpoints validates the start request's function
// breakpoints: at most functionBreakpointsMax, each name a non-empty single
// line (trimmed) of at most debuggerBreakpointNameRunes characters; the host
// never interprets the name, the adapter does. Order and count are kept: the
// adapter answers in request order and the frontend maps results by index.
func debuggerFunctionBreakpoints(breakpoints []debug.FunctionBreakpoint) ([]debug.FunctionBreakpoint, error) {
	if len(breakpoints) > debuggerFunctionBreakpointsMax {
		return nil, newContributionError(contributionErrInvalidRequest, "more than %d function breakpoints", debuggerFunctionBreakpointsMax)
	}
	out := make([]debug.FunctionBreakpoint, 0, len(breakpoints))
	for index, breakpoint := range breakpoints {
		name, err := validateContributionLine(breakpoint.Name, debuggerBreakpointNameRunes, true)
		if err != nil {
			return nil, newContributionError(contributionErrInvalidRequest, "functionBreakpoints[%d] name %v", index, err)
		}
		if err := debuggerBreakpointConditions(breakpoint.Condition, breakpoint.HitCondition); err != nil {
			return nil, newContributionError(contributionErrInvalidRequest, "functionBreakpoints[%d] %v", index, err)
		}
		out = append(out, debug.FunctionBreakpoint{Name: name, Condition: breakpoint.Condition, HitCondition: breakpoint.HitCondition})
	}
	return out, nil
}

// debuggerDataBreakpoints validates the start request's data breakpoints: at
// most dataBreakpointsMax, each dataId (an adapter token from
// dataBreakpointInfo, sent back verbatim) non-blank, at most
// debuggerBreakpointNameRunes characters without control characters, and
// accessType empty or one of debuggerDataAccessTypes.
func debuggerDataBreakpoints(breakpoints []debug.DataBreakpoint) ([]debug.DataBreakpoint, error) {
	if len(breakpoints) > debuggerDataBreakpointsMax {
		return nil, newContributionError(contributionErrInvalidRequest, "more than %d data breakpoints", debuggerDataBreakpointsMax)
	}
	out := make([]debug.DataBreakpoint, 0, len(breakpoints))
	for index, breakpoint := range breakpoints {
		if _, err := validateContributionLine(breakpoint.DataID, debuggerBreakpointNameRunes, true); err != nil {
			return nil, newContributionError(contributionErrInvalidRequest, "dataBreakpoints[%d] dataId %v", index, err)
		}
		if utf8.RuneCountInString(breakpoint.DataID) > debuggerBreakpointNameRunes {
			return nil, newContributionError(contributionErrInvalidRequest, "dataBreakpoints[%d] dataId must be at most %d characters", index, debuggerBreakpointNameRunes)
		}
		if err := checkContributionRunes(breakpoint.DataID, false, false); err != nil {
			return nil, newContributionError(contributionErrInvalidRequest, "dataBreakpoints[%d] dataId %v", index, err)
		}
		if breakpoint.AccessType != "" && !slices.Contains(debuggerDataAccessTypes, breakpoint.AccessType) {
			return nil, newContributionError(contributionErrInvalidRequest, "dataBreakpoints[%d] accessType %q must be one of %s", index, breakpoint.AccessType, strings.Join(debuggerDataAccessTypes, ", "))
		}
		if err := debuggerBreakpointConditions(breakpoint.Condition, breakpoint.HitCondition); err != nil {
			return nil, newContributionError(contributionErrInvalidRequest, "dataBreakpoints[%d] %v", index, err)
		}
		out = append(out, breakpoint)
	}
	return out, nil
}

// debuggerBreakpointConditions bounds a breakpoint's condition and hit
// condition (expressions for the adapter, sent verbatim).
func debuggerBreakpointConditions(condition, hitCondition string) error {
	for _, value := range [...]struct{ field, text string }{{"condition", condition}, {"hitCondition", hitCondition}} {
		if !utf8.ValidString(value.text) {
			return fmt.Errorf("%s must be valid UTF-8", value.field)
		}
		if utf8.RuneCountInString(value.text) > debuggerBreakpointConditionRunes {
			return fmt.Errorf("%s must be at most %d characters", value.field, debuggerBreakpointConditionRunes)
		}
	}
	return nil
}

// startDebugger consumes a confirmed prepared launch, starts the adapter and
// runs the DAP launch sequence.
func (manager *pluginManager) startDebugger(ctx context.Context, request debuggerStartRequest) (DebuggerStartResult, error) {
	entry, err := preparedDebugLaunch(request.PrepareID, true)
	if err != nil {
		return DebuggerStartResult{}, err
	}
	prepared := entry.prepared
	launch := prepared.Launch
	what := debuggerWhat(prepared.Type)
	// Re-read the confirmation: it may have been evicted or revoked since prepare.
	launch.Trusted, launch.PreviouslyTrusted = false, false
	if err := applyProcessLaunchTrust(&launch); err != nil {
		return DebuggerStartResult{}, err
	}
	if err := checkProcessLaunchStart(launch, request.Fingerprint, what); err != nil {
		return DebuggerStartResult{}, err
	}
	valid := manager.debuggerValid(entry.src, prepared.Type, entry.spec)
	if !valid() {
		return DebuggerStartResult{}, processLaunchError(processLaunchErrChanged, "%s or its plugin changed since it was prepared", what)
	}
	if entry.testRunID != "" && !testRunAwaitingSession(entry.testRunID, entry.src.pluginID) {
		return DebuggerStartResult{}, processLaunchError(processLaunchErrNotFound, "the test run has ended; run the tests again")
	}
	breakpoints, skipped, err := debuggerBreakpoints(entry.requestedRoot, request.Breakpoints)
	if err != nil {
		return DebuggerStartResult{}, err
	}
	addressBreakpoints, err := debuggerAddressBreakpoints(request.AddressBreakpoints)
	if err != nil {
		return DebuggerStartResult{}, err
	}
	functionBreakpoints, err := debuggerFunctionBreakpoints(request.FunctionBreakpoints)
	if err != nil {
		return DebuggerStartResult{}, err
	}
	dataBreakpoints, err := debuggerDataBreakpoints(request.DataBreakpoints)
	if err != nil {
		return DebuggerStartResult{}, err
	}
	filters, err := debuggerExceptionFilters(request.ExceptionFilters)
	if err != nil {
		return DebuggerStartResult{}, err
	}
	// The session keeps the root as the frontend spelled it: SourcePathInProject
	// accepts paths under that spelling and under the canonical root, and the
	// frontend matches SessionInfo.ProjectRoot against its own spelling. The
	// adapter still runs in the canonical (reviewed) directory.
	sessionRoot := entry.requestedRoot
	if sessionRoot == "" {
		sessionRoot = prepared.ProjectRoot
	}
	var session *debug.Session
	if prepared.Kind == debuggerKindJdtls {
		session, err = manager.connectJdtlsDebugger(ctx, entry, launch, sessionRoot, valid)
	} else {
		session, err = manager.startDebuggerProcess(ctx, prepared, launch, sessionRoot, valid)
	}
	if err != nil {
		if processLaunchErrorCode(err) != "" {
			return DebuggerStartResult{}, err
		}
		return DebuggerStartResult{}, processLaunchError(processLaunchErrUnavailable, "%v", err)
	}
	// Bound before Begin, so a session that ends during the launch sequence
	// still ends its test run.
	if entry.testRunID != "" && !manager.bindTestRunSession(entry.testRunID, entry.src.pluginID, session) {
		debuggerStopSession(context.Background(), session, "test run ended before its session started")
		return DebuggerStartResult{}, processLaunchError(processLaunchErrNotFound, "the test run has ended; run the tests again")
	}
	if prepared.Kind == debuggerKindJdtls {
		// Adapter setup can take time. Re-check the executable immediately
		// before delivering the cached launch arguments, not just before connect.
		fresh, checkErr := manager.resolveJdtlsDebuggerLaunch(entry.src, entry.debugger, prepared.ProjectRoot, prepared.Arguments, entry.outsideCwd)
		if checkErr == nil && fresh.Fingerprint != launch.Fingerprint {
			checkErr = processLaunchError(processLaunchErrChanged, "%s javaExec changed before launch; review it again", what)
		}
		if checkErr != nil {
			debuggerStopSession(context.Background(), session, "debug executable changed before launch")
			return DebuggerStartResult{}, checkErr
		}
	}
	beginCtx, cancelBegin := context.WithTimeout(ctx, debuggerBeginTimeout)
	defer cancelBegin()
	capabilities, err := session.Begin(beginCtx, debug.BeginParams{
		AdapterID: prepared.Type, Request: prepared.Request, Arguments: prepared.Arguments,
		Breakpoints: breakpoints, AddressBreakpoints: addressBreakpoints,
		FunctionBreakpoints: functionBreakpoints, DataBreakpoints: dataBreakpoints,
		ExceptionFilters: filters, ConfirmedOutsideCwd: entry.outsideCwd,
	})
	if err != nil {
		// Graceful stop: debugpy puts the debuggee in its own process group,
		// so killing the adapter alone could leave a launched program running.
		debuggerStopSession(context.Background(), session, fmt.Sprintf("debug launch failed: %v", err))
		return DebuggerStartResult{}, processLaunchError(processLaunchErrUnavailable, "%v", err)
	}
	if len(capabilities) == 0 {
		capabilities = json.RawMessage("null")
	}
	return DebuggerStartResult{SessionID: session.ID(), Capabilities: capabilities, SkippedBreakpoints: skipped}, nil
}

// startDebuggerProcess re-hashes the pinned plugin files and starts a stdio
// or tcp adapter process.
func (manager *pluginManager) startDebuggerProcess(ctx context.Context, prepared PreparedDebugLaunch, launch ResolvedProcessLaunch, sessionRoot string, valid func() bool) (*debug.Session, error) {
	beforeExec, err := verifyProcessLaunchFiles(launch)
	if err != nil {
		return nil, err
	}
	cwd := launch.Cwd
	if cwd == "" {
		cwd = prepared.ProjectRoot
	}
	startCtx, cancelStart := context.WithTimeout(ctx, debuggerTCPConnectTimeout+5*time.Second)
	defer cancelStart()
	return debuggerStartAdapter(startCtx, debug.AdapterSpec{
		Kind: prepared.Kind, Executable: launch.Argv[0], Args: slices.Clone(launch.Argv[1:]),
		Env: maps.Clone(launch.Env), Cwd: cwd,
		BeforeExec: beforeExec, Valid: valid,
		StderrMaxBytes: debuggerStderrMaxBytes, MaxMessageBytes: debuggerMessageMaxBytes,
		ConnectTimeout: debuggerTCPConnectTimeout,
		OwnerPluginID:  launch.PluginID, DebuggerType: prepared.Type, ProjectRoot: sessionRoot,
		MaxSessionsPerOwner: debuggerSessionsMax,
	})
}

// debuggerEntryPoints asks entryPointsAction for runnable lines of file.
func (manager *pluginManager) debuggerEntryPoints(ctx context.Context, request debuggerEntryPointsRequest) ([]DebuggerEntryPoint, error) {
	src, debugger, root, err := manager.debuggerSource(request.PluginID, request.Type, request.ProjectRoot)
	if err != nil {
		return nil, err
	}
	if !languageIDPattern.MatchString(request.LanguageID) {
		return nil, newContributionError(contributionErrInvalidRequest, "languageId %q is invalid", request.LanguageID)
	}
	file, err := debug.SourcePathInProject(strings.TrimSpace(request.ProjectRoot), request.File, false)
	if err != nil {
		return nil, err
	}
	if debugger.EntryPointsAction == "" {
		return []DebuggerEntryPoint{}, nil
	}
	raw, err := manager.invokeDebuggerAction(ctx, src, debugger, debugger.EntryPointsAction, map[string]any{
		"type":       debugger.Type,
		"project":    map[string]string{"root": root},
		"file":       file,
		"languageId": request.LanguageID,
	}, debuggerEntryPointsTimeout, debuggerResolveMaxBytes)
	if err != nil {
		return nil, err
	}
	return parseDebuggerEntryPoints(raw)
}

// parseDebuggerEntryPoints requires an array; invalid items are dropped and
// at most entryPointsMax are kept.
func parseDebuggerEntryPoints(raw json.RawMessage) ([]DebuggerEntryPoint, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil || items == nil {
		return nil, newContributionError(contributionErrInvalidResult, "entry points must be a JSON array")
	}
	out := []DebuggerEntryPoint{}
	for _, item := range items {
		if len(out) == debuggerEntryPointsMax {
			break
		}
		var candidate DebuggerEntryPoint
		if decodeStrictJSON(item, &candidate, true) != nil || candidate.Line < 1 {
			continue
		}
		label, err := validateContributionLine(candidate.Label, debuggerTitleMaxRunes, true)
		if err != nil {
			continue
		}
		configuration, err := canonicalContributionObject(candidate.Configuration, debuggerSnippetMaxBytes, debuggerJSONMaxDepth)
		if err != nil || configuration == nil {
			continue
		}
		out = append(out, DebuggerEntryPoint{Line: candidate.Line, Label: label, Configuration: configuration})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Line < out[j].Line })
	return out, nil
}

// checkDebugger asks checkAction which tools are missing. A run
// configuration in the request is passed through as payload.configuration
// (omitted when absent or null).
func (manager *pluginManager) checkDebugger(ctx context.Context, request debuggerCheckRequest) (DebuggerCheckResult, error) {
	configuration := strings.TrimSpace(string(request.Configuration))
	if configuration == "null" {
		configuration = ""
	}
	if configuration != "" && (!strings.HasPrefix(configuration, "{") || !json.Valid([]byte(configuration))) {
		return DebuggerCheckResult{}, newContributionError(contributionErrInvalidRequest, "configuration must be a JSON object")
	}
	src, debugger, root, err := manager.debuggerSource(request.PluginID, request.Type, request.ProjectRoot)
	if err != nil {
		return DebuggerCheckResult{}, err
	}
	if debugger.CheckAction == "" {
		return DebuggerCheckResult{Ready: true, Missing: []DebuggerMissingTool{}}, nil
	}
	payload := map[string]any{
		"type":     debugger.Type,
		"project":  map[string]string{"root": root},
		"platform": terminalProfileGOOS,
	}
	if configuration != "" {
		payload["configuration"] = json.RawMessage(configuration)
	}
	raw, err := manager.invokeDebuggerAction(ctx, src, debugger, debugger.CheckAction, payload, debuggerCheckTimeout, debuggerResolveMaxBytes)
	if err != nil {
		return DebuggerCheckResult{}, err
	}
	return parseDebuggerCheckResult(raw)
}

func parseDebuggerCheckResult(raw json.RawMessage) (DebuggerCheckResult, error) {
	invalid := func(format string, args ...any) (DebuggerCheckResult, error) {
		return DebuggerCheckResult{}, newContributionError(contributionErrInvalidResult, "check result "+format, args...)
	}
	var result struct {
		Ready   *bool                 `json:"ready"`
		Missing []DebuggerMissingTool `json:"missing"`
	}
	if err := decodeStrictJSON(raw, &result, true); err != nil {
		return invalid("%v", err)
	}
	if result.Ready == nil {
		return invalid("ready is required")
	}
	if len(result.Missing) > debuggerMissingMax {
		return invalid("lists more than %d missing tools", debuggerMissingMax)
	}
	out := DebuggerCheckResult{Ready: *result.Ready, Missing: []DebuggerMissingTool{}}
	for index, tool := range result.Missing {
		var err error
		if tool.Name, err = validateContributionLine(tool.Name, debuggerTitleMaxRunes, true); err != nil {
			return invalid("missing[%d] name %v", index, err)
		}
		if tool.Hint, err = validateContributionLine(tool.Hint, debuggerArgMaxRunes, false); err != nil {
			return invalid("missing[%d] hint %v", index, err)
		}
		if tool.InstallCommand, err = validateContributionLine(tool.InstallCommand, debuggerArgMaxRunes, false); err != nil {
			return invalid("missing[%d] installCommand %v", index, err)
		}
		out.Missing = append(out.Missing, tool)
	}
	if out.Ready && len(out.Missing) > 0 {
		return invalid("is ready but lists missing tools")
	}
	return out, nil
}

// normalizeDebuggerExceptionTypes trims and de-duplicates (first spelling
// wins, order kept) an exception type list, then requires every item to be a
// dotted identifier (debuggerExceptionTypePattern) of at most
// exceptionTypeMaxRunes and the list to have at most exceptionTypesMax items.
// A nil list is refused; an empty one is valid and stays non-nil. The
// frontend mirrors this rule before saving a list.
func normalizeDebuggerExceptionTypes(values []string) ([]string, error) {
	if values == nil {
		return nil, newContributionError(contributionErrInvalidRequest, "exceptionTypes is required (send [] to clear)")
	}
	out := []string{}
	seen := map[string]bool{}
	for index, raw := range values {
		value := strings.TrimSpace(raw)
		if utf8.RuneCountInString(value) > debuggerExceptionTypeRunes {
			return nil, newContributionError(contributionErrInvalidRequest, "exceptionTypes[%d] is longer than %d characters", index, debuggerExceptionTypeRunes)
		}
		if !debuggerExceptionTypePattern.MatchString(value) {
			return nil, newContributionError(contributionErrInvalidRequest, "exceptionTypes[%d] %q must be a dotted identifier matching %s", index, value, debuggerExceptionTypePattern)
		}
		if seen[value] {
			continue
		}
		if len(out) == debuggerExceptionTypesMax {
			return nil, newContributionError(contributionErrInvalidRequest, "exceptionTypes has more than %d distinct items", debuggerExceptionTypesMax)
		}
		seen[value] = true
		out = append(out, value)
	}
	return out, nil
}

// applyDebuggerSettings sends the project's saved settings of a debugger type
// to its settingsAction as {type, project: {root}, exceptionTypes} (an empty
// list is sent too, so the plugin clears its previous filter) and expects {}
// back. A debugger without settingsAction has nothing to apply. Errors start
// with their code (no wrapping).
func (manager *pluginManager) applyDebuggerSettings(ctx context.Context, request debuggerSettingsRequest) error {
	exceptionTypes, err := normalizeDebuggerExceptionTypes(request.ExceptionTypes)
	if err != nil {
		return err
	}
	src, debugger, root, err := manager.debuggerSource(request.PluginID, request.Type, request.ProjectRoot)
	if err != nil {
		return err
	}
	if debugger.SettingsAction == "" {
		return nil
	}
	payload := map[string]any{
		"type":           debugger.Type,
		"project":        map[string]string{"root": root},
		"exceptionTypes": exceptionTypes,
	}
	// Errors are returned as is, like checkDebugger: the WebView gets the
	// message verbatim and expects it to start with the error code.
	raw, err := manager.invokeDebuggerAction(ctx, src, debugger, debugger.SettingsAction, payload, debuggerSettingsTimeout, debuggerSettingsResultMaxBytes)
	if err != nil {
		return err
	}
	var result debuggerSettingsResult
	if !strings.HasPrefix(strings.TrimSpace(string(raw)), "{") {
		return newContributionError(contributionErrInvalidResult, "debugger %q settings result must be {}", debugger.Type)
	}
	if err := decodeStrictJSON(raw, &result, true); err != nil {
		return newContributionError(contributionErrInvalidResult, "debugger %q settings result must be {}: %v", debugger.Type, err)
	}
	return nil
}

func cloneDebuggerTextHints(declared *DebuggerTextHints) *DebuggerTextHints {
	if declared == nil {
		return nil
	}
	clone := *declared
	clone.PlaceholderLocalized = maps.Clone(declared.PlaceholderLocalized)
	clone.DescriptionLocalized = maps.Clone(declared.DescriptionLocalized)
	return &clone
}

func cloneDebuggerHotCodeReplace(declared *DebuggerHotCodeReplace) *DebuggerHotCodeReplace {
	if declared == nil {
		return nil
	}
	clone := *declared
	return &clone
}

// stopPluginDebugSessions stops every debug session of pluginID and drops its
// prepared launches (disable, uninstall, reload, upgrade).
func stopPluginDebugSessions(pluginID, reason string) {
	forgetPreparedDebugLaunches(pluginID)
	debuggerStopForOwner(pluginID, reason)
}

func pluginHostDebuggersListHandler(_ context.Context, _, _ string) (string, error) {
	return core.EncodeResult(host().listDebuggers())
}

func pluginHostDebuggerPrepareHandler(ctx context.Context, _, args string) (string, error) {
	var request debuggerPrepareRequest
	if err := core.DecodeArgsPayload(args, &request); err != nil {
		return "", err
	}
	prepared, err := host().prepareDebugger(ctx, request)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(prepared)
}

func pluginHostDebuggerTrustHandler(_ context.Context, _, args string) (string, error) {
	var request debuggerTrustRequest
	if err := core.DecodeArgsPayload(args, &request); err != nil {
		return "", err
	}
	prepared, err := host().trustDebugger(request)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(prepared)
}

func pluginHostDebuggerStartHandler(ctx context.Context, _, args string) (string, error) {
	var request debuggerStartRequest
	if err := core.DecodeArgsPayload(args, &request); err != nil {
		return "", err
	}
	result, err := host().startDebugger(ctx, request)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(result)
}

func pluginHostDebuggerEntryPointsHandler(ctx context.Context, _, args string) (string, error) {
	var request debuggerEntryPointsRequest
	if err := core.DecodeArgsPayload(args, &request); err != nil {
		return "", err
	}
	points, err := host().debuggerEntryPoints(ctx, request)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(points)
}

func pluginHostDebuggerCheckHandler(ctx context.Context, _, args string) (string, error) {
	var request debuggerCheckRequest
	if err := core.DecodeArgsPayload(args, &request); err != nil {
		return "", err
	}
	result, err := host().checkDebugger(ctx, request)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(result)
}

func pluginHostDebuggerApplySettingsHandler(ctx context.Context, _, args string) (string, error) {
	var request debuggerSettingsRequest
	if err := core.DecodeArgsPayload(args, &request); err != nil {
		return "", err
	}
	if err := host().applyDebuggerSettings(ctx, request); err != nil {
		return "", err
	}
	return core.EncodeResult(debuggerSettingsResult{})
}

func init() {
	core.RegisterCtx("plugin_host_debuggers_list", pluginHostDebuggersListHandler)
	core.RegisterCtx("plugin_host_debugger_prepare", pluginHostDebuggerPrepareHandler)
	core.RegisterCtx("plugin_host_debugger_trust", pluginHostDebuggerTrustHandler)
	core.RegisterCtx("plugin_host_debugger_start", pluginHostDebuggerStartHandler)
	core.RegisterCtx("plugin_host_debugger_entry_points", pluginHostDebuggerEntryPointsHandler)
	core.RegisterCtx("plugin_host_debugger_check", pluginHostDebuggerCheckHandler)
	core.RegisterCtx("plugin_host_debugger_apply_settings", pluginHostDebuggerApplySettingsHandler)
	registerRecordRemovalHook(func(_ *pluginManager, pluginID string, reason removalReason) {
		if reason == removalReasonUninstall {
			stopPluginDebugSessions(pluginID, "uninstall")
		}
	})
	// Disable (and any change that makes a session invalid, such as a new
	// development generation) stops the sessions; a stop or crash of the
	// plugin process does not.
	registerRecordLifecycleHook(func(_ *pluginManager, record *pluginRecord) {
		record.mu.Lock()
		disabled := !record.enabled
		pluginID := record.id
		record.mu.Unlock()
		if disabled {
			stopPluginDebugSessions(pluginID, "disabled")
		}
	})
}
