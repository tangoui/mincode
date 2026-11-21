package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"mincode/app/backend/core"
)

const (
	jdtlsServerID = "jdtls"
	// restartRequiredEventName tells the frontend that a running built-in
	// server must restart to apply a change (e.g. the trusted jdtls bundles).
	restartRequiredEventName = "project-lsp:restart-required"
)

var jdtlsBundleProvider struct {
	sync.RWMutex
	provide func() []string
}

// SetJdtlsBundleProvider registers the source of the jar paths that the
// built-in jdtls loads through initializationOptions.bundles. The plugin
// package registers it (lsp must not import plugin); nil clears it.
func SetJdtlsBundleProvider(provide func() []string) {
	jdtlsBundleProvider.Lock()
	jdtlsBundleProvider.provide = provide
	jdtlsBundleProvider.Unlock()
}

func providedJdtlsBundles() ([]string, bool) {
	jdtlsBundleProvider.RLock()
	provide := jdtlsBundleProvider.provide
	jdtlsBundleProvider.RUnlock()
	if provide == nil {
		return nil, false
	}
	return provide(), true
}

// NotifyJdtlsRestartRequired announces that the jdtls bundle set changed;
// running jdtls sessions keep their old bundles until restarted.
func NotifyJdtlsRestartRequired() {
	core.Emit(restartRequiredEventName, struct {
		ServerID string `json:"serverId"`
		Reason   string `json:"reason"`
	}{jdtlsServerID, "bundles"})
}

// prepareJdtlsInitialize sends exactly the provider's (user-confirmed)
// bundles in initializationOptions.bundles: client-supplied bundles are
// dropped so unconfirmed jars never load into jdtls. The session records the
// list it loaded. It also adds the installed JDKs as
// settings.java.configuration.runtimes (jdtls_runtimes.go). With nothing to
// drop or add, params are sent unchanged.
func (session *session) prepareJdtlsInitialize(params json.RawMessage) (json.RawMessage, error) {
	provided, _ := providedJdtlsBundles()
	bundles := normalizeBundles(provided)
	session.setLoadedBundles(bundles)
	fields, options, err := session.initializeOptions(params)
	if err != nil {
		return nil, err
	}
	_, clientBundles := options["bundles"]
	changed := session.addJdtlsRuntimes(options)
	if clientBundles || len(bundles) > 0 {
		if clientBundles {
			delete(options, "bundles")
			session.log("lifecycle jdtls client-supplied bundles dropped")
		}
		if len(bundles) > 0 {
			options["bundles"], _ = json.Marshal(bundles)
		}
		session.log(fmt.Sprintf("lifecycle jdtls bundles configured count=%d", len(bundles)))
		changed = true
	}
	if !changed {
		return params, nil
	}
	fields["initializationOptions"], _ = json.Marshal(options)
	return json.Marshal(fields)
}

func normalizeBundles(paths []string) []string {
	bundles := make([]string, 0, len(paths))
	for _, path := range paths {
		if path != "" {
			bundles = append(bundles, path)
		}
	}
	slices.Sort(bundles)
	return slices.Compact(bundles)
}

func (session *session) setLoadedBundles(bundles []string) {
	session.jdtlsMu.Lock()
	session.bundles = bundles
	session.jdtlsMu.Unlock()
}

// loadedBundles returns a copy of the bundles sent in this session's initialize.
func (session *session) loadedBundles() []string {
	session.jdtlsMu.Lock()
	defer session.jdtlsMu.Unlock()
	return slices.Clone(session.bundles)
}

// observeLanguageStatus tracks jdtls language/status notifications. jdtls
// 1.63 sends Starting…, ProjectStatus, Started, then ServiceReady once the
// project import is done; only ServiceReady makes the session ready (commands
// such as resolveClasspath/startDebugSession need the imported project).
func (session *session) observeLanguageStatus(method string, params json.RawMessage) {
	if method != "language/status" || session.definition.ID != jdtlsServerID || session.external != nil {
		return
	}
	var status struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(params, &status) != nil || !validStatusType(status.Type) {
		return
	}
	session.jdtlsMu.Lock()
	session.status = status.Type
	session.jdtlsMu.Unlock()
	if status.Type == "ServiceReady" {
		session.ready.Store(true)
	}
}

// validStatusType bounds the server-supplied status to a short identifier.
func validStatusType(kind string) bool {
	if kind == "" || len(kind) > 32 {
		return false
	}
	for _, r := range kind {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}

// runningBuiltinSession returns the live built-in session for root, without
// the generation check the frontend bridge requires.
func runningBuiltinSession(root, serverID string) (*session, bool) {
	if IsExternalServerID(serverID) {
		return nil, false
	}
	canonical, err := canonicalRoot(root)
	if err != nil {
		return nil, false
	}
	manager.RLock()
	active := manager.sessions[sessionKey(canonical, serverID)]
	manager.RUnlock()
	if active == nil || active.closed.Load() || active.external != nil {
		return nil, false
	}
	return active, true
}

// JdtlsSessionBundles returns the bundles the running built-in jdtls for
// root was initialized with; ok is false when it is not running.
func JdtlsSessionBundles(root string) ([]string, bool) {
	active, ok := runningBuiltinSession(root, jdtlsServerID)
	if !ok {
		return nil, false
	}
	return active.loadedBundles(), true
}

// JdtlsReady reports whether the built-in jdtls for root is running and has
// sent ServiceReady (sticky for the session's lifetime).
func JdtlsReady(root string) (ready bool, running bool) {
	active, ok := runningBuiltinSession(root, jdtlsServerID)
	if !ok {
		return false, false
	}
	return active.ready.Load(), true
}

// JdtlsStatus returns the last language/status type (e.g. "Starting",
// "Started", "ServiceReady") of the running built-in jdtls for root; "" when
// it is not running or has not reported a status yet.
func JdtlsStatus(root string) string {
	active, ok := runningBuiltinSession(root, jdtlsServerID)
	if !ok {
		return ""
	}
	active.jdtlsMu.Lock()
	defer active.jdtlsMu.Unlock()
	return active.status
}

// ExecuteBuiltinCommand sends workspace/executeCommand to the running
// built-in jdtls for root (serverID must be "jdtls"). The caller's ctx bounds
// the request (it is not capped at requestTimeout); a ctx without deadline
// gets requestTimeout. A ctx that is already done sends nothing.
func ExecuteBuiltinCommand(ctx context.Context, root, serverID, command string, args json.RawMessage) (json.RawMessage, error) {
	if serverID != jdtlsServerID {
		return nil, errors.New("invalid_request: only the built-in jdtls accepts commands")
	}
	if command == "" {
		return nil, errors.New("invalid_request: command is required")
	}
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage("[]")
	}
	var list []json.RawMessage
	if err := json.Unmarshal(args, &list); err != nil {
		return nil, errors.New("invalid_request: command arguments must be a JSON array")
	}
	params, err := json.Marshal(struct {
		Command   string          `json:"command"`
		Arguments json.RawMessage `json:"arguments"`
	}{command, args})
	if err != nil {
		return nil, fmt.Errorf("encode command: %w", err)
	}
	if len(params) > maxMessageBytes {
		return nil, errors.New("invalid_request: command arguments exceed size limit")
	}
	active, ok := runningBuiltinSession(root, jdtlsServerID)
	if !ok {
		return nil, errors.New("unavailable: Java language service is not running")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, requestTimeout)
		defer cancel()
	}
	return active.request(ctx, "workspace/executeCommand", params)
}

// jdtlsReloadBundlesCommand is jdtls's built-in command that installs the
// OSGi bundles (arbitrary jar paths) given as its first argument. MinCode
// loads bundles only through initialize (prepareJdtlsInitialize), so the
// command is refused on every path into the built-in jdtls.
const jdtlsReloadBundlesCommand = "java.reloadBundles"

// guardOutgoing refuses client messages that would bypass the host's
// controls on what a server loads:
//
//   - "initialize" sent as a notification (lsp4j runs the initialize handler
//     for it too, skipping prepareInitialize's bundle filter);
//   - for the built-in jdtls: workspace/executeCommand sent as a
//     notification (lsp4j would execute it), and java.reloadBundles.
//
// A built-in jdtls executeCommand request is re-encoded from the decoded
// command/arguments so what jdtls receives is exactly what was checked (no
// duplicate keys or other fields). It returns the params to send.
func (session *session) guardOutgoing(method string, params json.RawMessage, notification bool) (json.RawMessage, error) {
	if notification && method == "initialized" && session.external == nil && !session.initialized.Load() {
		return nil, errors.New("forbidden: initialized requires a completed initialize request")
	}
	if notification && method == "initialize" {
		session.log("error category=initialize-notification-refused")
		return nil, errors.New("forbidden: initialize must be sent as a request")
	}
	if method != "workspace/executeCommand" || session.external != nil || session.definition.ID != jdtlsServerID {
		return params, nil
	}
	if notification {
		session.log("error category=execute-command-notification-refused")
		return nil, errors.New("forbidden: workspace/executeCommand must be sent as a request")
	}
	var decoded struct {
		Command       string          `json:"command"`
		Arguments     json.RawMessage `json:"arguments,omitempty"`
		WorkDoneToken json.RawMessage `json:"workDoneToken,omitempty"`
	}
	if err := json.Unmarshal(params, &decoded); err != nil || decoded.Command == "" {
		return nil, errors.New("invalid_request: workspace/executeCommand needs a command")
	}
	if strings.EqualFold(strings.TrimSpace(decoded.Command), jdtlsReloadBundlesCommand) {
		session.log("error category=reload-bundles-refused")
		return nil, errors.New("forbidden: java.reloadBundles is not available; Java bundles load only from confirmed plugins when the Java language service starts")
	}
	return json.Marshal(decoded)
}
