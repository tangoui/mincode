package lsp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"

	"mincode/app/backend/core"
)

// Plugin language servers (contributes.languageServers). The plugin host
// (backend/plugin/language_servers_launch.go) resolves, confirms and gates a
// launch, then calls StartExternal. From here on an external session is a
// normal session with these extra rules:
//
//   - it is only ever started by StartExternal: project_lsp_start and
//     project_lsp_install reject "plugin:" ids, so the AI workspace tools
//     (which use those commands) cannot start one;
//   - request, notify, reply, stop and logs reach it only while it is live;
//   - initialize is rewritten so the only workspace is the project root and
//     initializationOptions are the host's validated copy;
//   - workspace/didChangeWorkspaceFolders is refused, and every "uri" the
//     client sends must be a file:// URI inside the project root;
//   - frames are capped at MessageMaxBytes in both directions and stderr at
//     StderrMaxBytes;
//   - the owner plugin's lifecycle stops it (StopExternalForOwner).

// ExternalServerIDPrefix marks a plugin server id: "plugin:<pluginId>/<id>".
const ExternalServerIDPrefix = "plugin:"

var (
	errExternalStart   = errors.New("forbidden: plugin language servers start only through plugin_host_language_server_start")
	errExternalInstall = errors.New("forbidden: plugin language servers cannot be installed by the host")

	externalServerIDPattern = regexp.MustCompile(`^plugin:[a-z0-9][a-z0-9._-]{1,127}/[a-z0-9][a-z0-9-]{0,31}$`)

	// externalDocumentMethods carry textDocument.uri, which is required.
	externalDocumentMethods = map[string]bool{
		"textDocument/didOpen":           true,
		"textDocument/didChange":         true,
		"textDocument/didSave":           true,
		"textDocument/didClose":          true,
		"textDocument/willSave":          true,
		"textDocument/willSaveWaitUntil": true,
	}
	// externalRefusedMethods would widen the server's workspace.
	externalRefusedMethods = map[string]bool{
		"workspace/didChangeWorkspaceFolders": true,
	}
)

// ServerStatus is the status reported for a started server.
type ServerStatus = serverStatus

// ExternalServerSpec is one resolved, confirmed plugin language server.
type ExternalServerSpec struct {
	// ServerID is "plugin:<OwnerPluginID>/<id>".
	ServerID      string
	OwnerPluginID string
	Name          string
	Languages     []string
	// Executable is the absolute resolved program; Args are fully expanded.
	Executable string
	Args       []string
	// Env is merged over the host environment (the contract denylist keeps
	// PATH and loader variables out).
	Env map[string]string
	// InitializationOptions replace whatever the client sends in initialize.
	InitializationOptions json.RawMessage
	// Fingerprint identifies the confirmed launch; a live session with a
	// different fingerprint is replaced.
	Fingerprint string
	// BeforeExec runs immediately before the process is created and refuses
	// a plugin file swapped since it was verified.
	BeforeExec func() error
	// Valid reports whether the owner plugin still offers this exact launch
	// (enabled, active, same version, generation and entry). It is checked on
	// every client message; a session whose owner is gone is stopped.
	Valid               func() bool
	MessageMaxBytes     int
	StderrMaxBytes      int
	MaxSessionsPerOwner int
}

// IsExternalServerID reports whether id names a plugin language server.
func IsExternalServerID(id string) bool {
	return strings.HasPrefix(id, ExternalServerIDPrefix)
}

// ExternalServerID builds the session id of a plugin server.
func ExternalServerID(pluginID, id string) string {
	return ExternalServerIDPrefix + pluginID + "/" + id
}

// sessionServerID validates a server id used to look up a session: a
// built-in definition id or a syntactically valid plugin id.
func sessionServerID(serverID string) (string, error) {
	if IsExternalServerID(serverID) {
		if !externalServerIDPattern.MatchString(serverID) {
			return "", fmt.Errorf("unsupported language server: %s", serverID)
		}
		return serverID, nil
	}
	definition, err := requireDefinition(serverID)
	if err != nil {
		return "", err
	}
	return definition.ID, nil
}

// StartExternal starts (or returns the live session of) a plugin language
// server for root. Callers must have applied every plugin gate first.
func StartExternal(root string, spec ExternalServerSpec) (ServerStatus, error) {
	if !externalServerIDPattern.MatchString(spec.ServerID) || !strings.HasPrefix(spec.ServerID, ExternalServerIDPrefix+spec.OwnerPluginID+"/") {
		return ServerStatus{}, fmt.Errorf("forbidden: invalid plugin language server id %q", spec.ServerID)
	}
	if !filepath.IsAbs(spec.Executable) {
		return ServerStatus{}, errors.New("forbidden: the language server executable must be resolved")
	}
	canonical, err := canonicalRoot(root)
	if err != nil {
		return ServerStatus{}, err
	}
	copied := spec
	copied.Languages = slices.Clone(spec.Languages)
	copied.Args = slices.Clone(spec.Args)
	copied.Env = make(map[string]string, len(spec.Env))
	for name, value := range spec.Env {
		copied.Env[name] = value
	}
	copied.InitializationOptions = slices.Clone(spec.InitializationOptions)

	key := sessionKey(canonical, spec.ServerID)
	manager.Lock()
	defer manager.Unlock()
	if active := manager.sessions[key]; active != nil && !active.closed.Load() {
		if active.external != nil && active.external.Fingerprint == spec.Fingerprint {
			return statusFor(active), nil
		}
		delete(manager.sessions, key)
		active.close(errors.New("language server launch changed"))
	}
	if spec.MaxSessionsPerOwner > 0 {
		live := 0
		for _, active := range manager.sessions {
			if active.external != nil && active.external.OwnerPluginID == spec.OwnerPluginID && !active.closed.Load() {
				live++
			}
		}
		if live >= spec.MaxSessionsPerOwner {
			return ServerStatus{}, fmt.Errorf("limit: plugin %q already runs %d language server sessions", spec.OwnerPluginID, live)
		}
	}
	active := &session{
		key:  key,
		root: canonical,
		definition: serverDefinition{
			ID: spec.ServerID, Name: spec.Name, Languages: copied.Languages, Command: spec.Executable,
		},
		pending:    map[uint64]chan rpcReply{},
		generation: generation.Add(1),
		external:   &copied,
	}
	if err := active.start(); err != nil {
		return ServerStatus{}, err
	}
	manager.sessions[key] = active
	return statusFor(active), nil
}

// startExternal creates the plugin server process. BeforeExec runs before
// any pipe is opened, so a refused start leaks nothing.
func (session *session) startExternal() error {
	spec := session.external
	if spec.BeforeExec != nil {
		if err := spec.BeforeExec(); err != nil {
			return err
		}
	}
	session.cmd = exec.Command(spec.Executable, spec.Args...)
	session.cmd.Dir = session.root
	session.cmd.Env = core.MergeEnv(core.ExpandedEnv(), spec.Env)
	core.PrepareProcessTree(session.cmd)
	stdin, err := session.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("open language-server stdin: %w", err)
	}
	stdout, err := session.cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return fmt.Errorf("open language-server stdout: %w", err)
	}
	session.stderr = &limitedBuffer{max: spec.StderrMaxBytes}
	session.cmd.Stderr = session.stderr
	if err := session.cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return fmt.Errorf("unavailable: start %s: %w", spec.Name, err)
	}
	session.stdin = stdin
	session.run(stdout)
	return nil
}

func externalStatus(active *session) serverStatus {
	spec := active.external
	status := serverStatus{
		ID: spec.ServerID, Name: spec.Name, Languages: spec.Languages,
		Installed: true, Executable: spec.Executable, Platform: runtime.GOOS,
		Running: !active.closed.Load(), Generation: active.generation,
	}
	if active.closed.Load() && active.stderr != nil {
		status.Error = active.failureText()
	}
	return status
}

// ExternalSessionLive reports whether a live session of serverID for root
// runs the launch with fingerprint.
func ExternalSessionLive(root, serverID, fingerprint string) bool {
	canonical, err := canonicalRoot(root)
	if err != nil {
		return false
	}
	manager.RLock()
	active := manager.sessions[sessionKey(canonical, serverID)]
	manager.RUnlock()
	return active != nil && !active.closed.Load() && active.external != nil && active.external.Fingerprint == fingerprint
}

// retireInvalidExternal stops an external session whose owner no longer
// offers it and reports whether it did.
func retireInvalidExternal(active *session) bool {
	if active.external == nil || active.external.Valid == nil || active.external.Valid() {
		return false
	}
	manager.Lock()
	if manager.sessions[active.key] == active {
		delete(manager.sessions, active.key)
	}
	manager.Unlock()
	stopExternalSession(active, "plugin-unavailable")
	return true
}

func stopExternalSession(active *session, reason string) {
	live := !active.closed.Load()
	active.close(fmt.Errorf("plugin language server stopped: %s", reason))
	if live {
		params, _ := json.Marshal(map[string]any{"reason": reason, "final": true})
		active.forward(serverEvent{
			Root: active.root, ServerID: active.definition.ID, Generation: active.generation,
			Method: "$/serverStopped", Params: params,
		})
	}
}

// StopExternal stops the session of serverID for root (the frontend's
// Restart and the plugin host's reset use it); it reports whether one ran.
func StopExternal(root, serverID, reason string) bool {
	canonical, err := canonicalRoot(root)
	if err != nil {
		return false
	}
	key := sessionKey(canonical, serverID)
	manager.Lock()
	active := manager.sessions[key]
	if active != nil && active.external != nil {
		delete(manager.sessions, key)
	} else {
		active = nil
	}
	manager.Unlock()
	if active == nil {
		return false
	}
	stopExternalSession(active, reason)
	return true
}

// StopExternalForOwner stops every session of pluginID and tells the
// frontend (a final $/serverStopped: no automatic restart). It returns the
// stopped server ids.
func StopExternalForOwner(pluginID, reason string) []string {
	manager.Lock()
	stopped := []*session{}
	for key, active := range manager.sessions {
		if active.external != nil && active.external.OwnerPluginID == pluginID {
			stopped = append(stopped, active)
			delete(manager.sessions, key)
		}
	}
	manager.Unlock()
	ids := []string{}
	for _, active := range stopped {
		stopExternalSession(active, reason)
		if !slices.Contains(ids, active.definition.ID) {
			ids = append(ids, active.definition.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

// BuiltinLanguages maps each language id to the built-in (non-auxiliary)
// servers that handle it.
func BuiltinLanguages() map[string][]string {
	out := map[string][]string{}
	for _, definition := range serverDefinitions {
		if definition.Auxiliary {
			continue
		}
		for _, language := range definition.Languages {
			out[language] = append(out[language], definition.ID)
		}
	}
	return out
}

// externalRoots returns the roots a plugin session accepts URIs under: the
// canonical root and, when it resolves to the same directory, the spelling
// the client uses (so a symlinked project path keeps working).
func externalRoots(active *session, requestedRoot string) []string {
	roots := []string{active.root}
	requested := strings.TrimSpace(requestedRoot)
	if requested != "" && filepath.IsAbs(requested) {
		requested = filepath.Clean(requested)
		if requested != active.root {
			if canonical, err := canonicalRoot(requested); err == nil && canonical == active.root {
				roots = append(roots, requested)
			}
		}
	}
	return roots
}

// confineExternalMessage applies the plugin session rules to one client
// message and returns the params to send. Built-in sessions pass unchanged.
func confineExternalMessage(active *session, requestedRoot, method string, params json.RawMessage) (json.RawMessage, error) {
	if active.external == nil {
		return params, nil
	}
	if externalRefusedMethods[method] {
		return nil, fmt.Errorf("forbidden: %s is not allowed for plugin language servers", method)
	}
	roots := externalRoots(active, requestedRoot)
	if method == "initialize" {
		return rewriteExternalInitialize(active, roots[len(roots)-1], params)
	}
	if externalDocumentMethods[method] {
		var document struct {
			TextDocument *struct {
				URI *string `json:"uri"`
			} `json:"textDocument"`
		}
		if err := json.Unmarshal(params, &document); err != nil || document.TextDocument == nil || document.TextDocument.URI == nil {
			return nil, fmt.Errorf("forbidden: %s requires textDocument.uri", method)
		}
		if err := confineExternalURI(roots, *document.TextDocument.URI); err != nil {
			return nil, err
		}
		return params, nil
	}
	if len(params) == 0 {
		return params, nil
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(params)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("invalid language-server params: %w", err)
	}
	if err := confineExternalURIs(roots, value, 0); err != nil {
		return nil, err
	}
	return params, nil
}

// confineExternalURIs checks every string member whose name ends in "uri"
// (uri, targetUri, newUri, oldUri, rootUri, scopeUri, ...).
func confineExternalURIs(roots []string, value any, depth int) error {
	if depth > 64 {
		return errors.New("forbidden: language-server params are nested too deeply")
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, member := range typed {
			if text, ok := member.(string); ok && strings.HasSuffix(strings.ToLower(key), "uri") {
				if err := confineExternalURI(roots, text); err != nil {
					return err
				}
				continue
			}
			if err := confineExternalURIs(roots, member, depth+1); err != nil {
				return err
			}
		}
	case []any:
		for _, member := range typed {
			if err := confineExternalURIs(roots, member, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// confineExternalURI requires a local file:// URI inside one of roots.
func confineExternalURI(roots []string, raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "file" || (parsed.Host != "" && !strings.EqualFold(parsed.Host, "localhost")) {
		return fmt.Errorf("forbidden: language-server URI %q must be a local file:// URI", raw)
	}
	path := parsed.Path
	if len(path) >= 3 && path[0] == '/' && path[2] == ':' && runtime.GOOS == "windows" {
		path = path[1:]
	}
	path = filepath.Clean(filepath.FromSlash(path))
	if !filepath.IsAbs(path) {
		return fmt.Errorf("forbidden: language-server URI %q must be absolute", raw)
	}
	for _, root := range roots {
		if pathInsideRoot(root, path) {
			return nil
		}
	}
	// A path under a symlinked directory: resolve the nearest existing
	// ancestor and compare with the canonical root.
	if resolved, ok := resolveExistingPrefix(path); ok && pathInsideRoot(roots[0], resolved) {
		return nil
	}
	return fmt.Errorf("forbidden: language-server URI %q is outside the project root", raw)
}

func pathInsideRoot(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil || filepath.IsAbs(relative) {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// resolveExistingPrefix resolves symlinks in the longest existing prefix of
// path and re-appends the rest.
func resolveExistingPrefix(path string) (string, bool) {
	rest := []string{}
	current := path
	for {
		if _, err := os.Lstat(current); err == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", false
			}
			for index := len(rest) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, rest[index])
			}
			return resolved, true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", false
		}
		rest = append(rest, filepath.Base(current))
		current = parent
	}
}

// fileURI builds the file:// URI of an absolute path.
func fileURI(path string) string {
	slashed := filepath.ToSlash(path)
	if !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}
	return (&url.URL{Scheme: "file", Path: slashed}).String()
}

// rewriteExternalInitialize keeps the client's capabilities and identity but
// pins the workspace to root and the initializationOptions to the host copy.
func rewriteExternalInitialize(active *session, root string, params json.RawMessage) (json.RawMessage, error) {
	fields := map[string]json.RawMessage{}
	if trimmed := strings.TrimSpace(string(params)); trimmed != "" && trimmed != "null" {
		if err := json.Unmarshal(params, &fields); err != nil {
			return nil, errors.New("forbidden: initialize params must be an object")
		}
	}
	uri := fileURI(root)
	encode := func(value any) json.RawMessage {
		raw, _ := json.Marshal(value)
		return raw
	}
	fields["rootUri"] = encode(uri)
	fields["rootPath"] = encode(root)
	fields["workspaceFolders"] = encode([]map[string]string{{"uri": uri, "name": filepath.Base(root)}})
	if options := active.external.InitializationOptions; len(options) > 0 {
		fields["initializationOptions"] = slices.Clone(options)
	} else {
		delete(fields, "initializationOptions")
	}
	rewritten, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("encode initialize params: %w", err)
	}
	if len(rewritten) > active.messageLimit() {
		return nil, errors.New("initialize params exceed the size limit")
	}
	return rewritten, nil
}
