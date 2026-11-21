package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"mincode/app/backend/core"
	backendfs "mincode/app/backend/fs"
	"mincode/app/plugins/api"
)

// Workspace file API and host events for process plugins.
//
// Workspace root: the frontend announces the active workspace's project root
// (plugin_host_workspace_scope_begin / plugin_host_workspace_scope_commit). host.workspace.* resolve every path under
// that canonical root with the shared backendfs containment rules, so a
// relative path or a symlink never reaches outside it.
//   - root/list/stat/read need workspace.read; write needs workspace.write.
//   - VCS internals and credential files (contract sensitiveFilenames) are
//     never listed, read or written; writes also skip dependency and MinCode
//     directories (contract fileTemplates deniedPathSegments).
//   - write replaces one whole file atomically, with an optional sha256
//     precondition; it never follows a symlink at the target.
//
// Events: a running plugin subscribes with host.events.subscribe {events};
// the host sends plugin.event {event, data} best-effort (3 s timeout, at most
// hostEventInflightMax deliveries in flight per process, excess dropped and
// logged). Events never start a plugin and are not replayed after a restart.
// Events that carry paths require workspace.read.
const (
	workspaceReadDefaultBytes = 256 * 1024
	// Base64 plus the JSON envelope must stay under the 1 MiB RPC line limit.
	workspaceReadMaxBytes  = 512 * 1024
	workspaceWriteMaxBytes = 512 * 1024
	workspaceListMax       = 2000
	workspaceHashMaxBytes  = 64 * 1024 * 1024

	hostEventMethod          = "plugin.event"
	hostEventTimeout         = 3 * time.Second
	hostEventInflightMax     = 8
	hostEventParamsMaxBytes  = 64 * 1024
	hostEventPathMaxLength   = 4096
	hostEventSubscriptionMax = 16

	hostEventWorkspaceChanged     = "workspace.changed"
	hostEventWorkspaceRootChanged = "workspace.rootChanged"
	hostEventActiveFileChanged    = "editor.activeFileChanged"
	hostEventFileSaved            = "editor.fileSaved"
	hostEventSelectionChanged     = "editor.selectionChanged"
	hostEventGitHeadChanged       = "git.headChanged"
)

// hostEvents lists every event and the capability its payload requires.
var hostEvents = map[string]api.Capability{
	hostEventWorkspaceChanged:     "",
	hostEventWorkspaceRootChanged: api.CapabilityWorkspaceRead,
	hostEventActiveFileChanged:    api.CapabilityWorkspaceRead,
	hostEventFileSaved:            api.CapabilityWorkspaceRead,
	hostEventSelectionChanged:     api.CapabilityWorkspaceRead,
	hostEventGitHeadChanged:       api.CapabilityWorkspaceRead,
	hostEventAIActivity:           api.CapabilityAIActivity,
	hostEventAITasks:              api.CapabilityAITasks,
	// tests.runEnded {runId, reason} goes only to the plugin that owns the
	// run (test_runs.go), subscribed or not; listing it lets SDKs subscribe.
	testRunEndedEvent: api.CapabilityWorkspaceRead,
}

// frontendHostEvents are the events the desktop frontend may publish; the
// workspace events originate in the backend.
var frontendHostEvents = map[string]bool{hostEventActiveFileChanged: true, hostEventFileSaved: true, hostEventSelectionChanged: true}

var workspaceVCSSegments = []string{".git", ".hg", ".svn"}

// pluginWorkspaceRoot is the canonical root host.workspace.* resolve against;
// a variable so tests need not touch the host singleton.
var pluginWorkspaceRoot = func() string { return host().workspaceRootPath() }

// pluginWorkspaceRootInput is the same root as the frontend announced it.
var pluginWorkspaceRootInput = func() string {
	if state := host().scopeState.Load(); state != nil {
		return state.input
	}
	return ""
}

func init() {
	registerHostMethod("host.workspace.root", nil, api.CapabilityWorkspaceRead, hostWorkspaceRoot)
	registerHostMethod("host.workspace.list", nil, api.CapabilityWorkspaceRead, hostWorkspaceList)
	registerHostMethod("host.workspace.stat", nil, api.CapabilityWorkspaceRead, hostWorkspaceStat)
	registerHostMethod("host.workspace.read", nil, api.CapabilityWorkspaceRead, hostWorkspaceRead)
	registerHostMethod("host.workspace.write", nil, api.CapabilityWorkspaceWrite, hostWorkspaceWrite)
	registerGenerationHostMethod("host.events.subscribe", nil, "", hostEventsSubscribeGeneration)
	core.Register("plugin_host_set_workspace_root", pluginHostSetWorkspaceRootHandler)
	core.Register("plugin_host_publish_event", pluginHostPublishEventHandler)
}

// ---- workspace root ----

func (manager *pluginManager) workspaceRootPath() string {
	return manager.workspaceScope().Root
}

// SetWorkspaceRoot is an internal compatibility helper. No core/RPC command
// reaches it: frontend writes always require the explicit begin ticket.
func (manager *pluginManager) SetWorkspaceRoot(root string) error {
	scope, err := manager.beginWorkspaceScope(context.Background(), manager.activeWorkspaceID(), false)
	if err != nil {
		return err
	}
	_, err = manager.CommitWorkspaceScope(scope.WorkspaceID, scope.Revision, root)
	return err
}

func canonicalWorkspaceRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", nil
	}
	if !filepath.IsAbs(root) {
		return "", errors.New("workspace root must be an absolute path")
	}
	canonical, err := backendfs.CanonicalProjectRoot(root)
	if err != nil {
		return "", fmt.Errorf("workspace root: %w", err)
	}
	// A filesystem root would expose the whole disk to workspace.read plugins.
	if filepath.Dir(canonical) == canonical {
		return "", errors.New("workspace root must not be a filesystem root")
	}
	return canonical, nil
}

func pluginHostSetWorkspaceRootHandler(_, _ string) (string, error) {
	return "", legacyWorkspaceScopeError()
}

// ---- path policy ----

func workspaceRPCError(code, format string, args ...any) *rpcError {
	return &rpcError{Code: -32002, Message: fmt.Sprintf(format, args...), Data: map[string]string{"code": code}}
}

// workspacePathDenied reports whether a slash-separated root-relative path is
// off limits: VCS internals and credential files always, dependency and
// MinCode directories for writes.
func workspacePathDenied(relative string, write bool) bool {
	if relative == "" || relative == "." {
		return false
	}
	for _, segment := range strings.Split(relative, "/") {
		for _, denied := range workspaceVCSSegments {
			if strings.EqualFold(segment, denied) {
				return true
			}
		}
		if write {
			for _, denied := range fileTemplateDeniedSegments {
				if strings.EqualFold(segment, denied) {
					return true
				}
			}
		}
	}
	return isFileTemplateSensitiveName(path.Base(relative))
}

func workspaceRootOrError() (string, *rpcError) {
	root := pluginWorkspaceRoot()
	if root == "" {
		return "", workspaceRPCError("no_root", "no workspace root is open")
	}
	return root, nil
}

func workspaceResolveError(err error) *rpcError {
	switch {
	case errors.Is(err, backendfs.ErrProjectPathInvalid):
		return &rpcError{Code: -32602, Message: "invalid workspace path", Data: map[string]string{"code": "invalid_path"}}
	case errors.Is(err, backendfs.ErrProjectPathEscapesRoot):
		return workspaceRPCError("denied", "workspace path escapes the workspace root")
	case backendfs.IsProjectPathNotExist(err):
		return workspaceRPCError("not_found", "workspace path not found")
	default:
		return workspaceRPCError("io", "workspace path could not be resolved")
	}
}

// resolveWorkspaceEntry resolves an existing entry; "" or "." is the root.
func resolveWorkspaceEntry(relative string, write bool) (backendfs.ProjectFile, *rpcError) {
	root, rpcErr := workspaceRootOrError()
	if rpcErr != nil {
		return backendfs.ProjectFile{}, rpcErr
	}
	if trimmed := strings.TrimSpace(relative); trimmed == "" || trimmed == "." {
		return backendfs.ProjectFile{Root: root, RelativePath: "", Path: root, ResolvedRelativePath: ""}, nil
	}
	file, err := backendfs.ResolveProjectFile(root, relative)
	if err != nil {
		return backendfs.ProjectFile{}, workspaceResolveError(err)
	}
	if workspacePathDenied(file.RelativePath, write) || workspacePathDenied(file.ResolvedRelativePath, write) {
		return backendfs.ProjectFile{}, workspaceRPCError("denied", "workspace path is not accessible to plugins")
	}
	return file, nil
}

type workspaceEntry struct {
	Path       string `json:"path"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Size       int64  `json:"size"`
	ModifiedAt int64  `json:"modifiedAt"`
	SHA256     string `json:"sha256,omitempty"`
}

func workspaceEntryType(mode os.FileMode) string {
	switch {
	case mode&os.ModeSymlink != 0:
		return "symlink"
	case mode.IsDir():
		return "dir"
	case mode.IsRegular():
		return "file"
	default:
		return "other"
	}
}

func decodeWorkspaceParams(params json.RawMessage, target any) *rpcError {
	if len(params) == 0 || string(params) == "null" {
		return nil
	}
	if err := json.Unmarshal(params, target); err != nil {
		return &rpcError{Code: -32602, Message: "invalid workspace params", Data: map[string]string{"code": "invalid_params"}}
	}
	return nil
}

// ---- host.workspace.* ----

func hostWorkspaceRoot(_ *processRuntime, _ json.RawMessage) (any, *rpcError) {
	root := pluginWorkspaceRoot()
	var value any
	if root != "" {
		value = root
	}
	return map[string]any{"root": value}, nil
}

func hostWorkspaceList(_ *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request struct {
		Path string `json:"path"`
	}
	if rpcErr := decodeWorkspaceParams(params, &request); rpcErr != nil {
		return nil, rpcErr
	}
	dir, rpcErr := resolveWorkspaceEntry(request.Path, false)
	if rpcErr != nil {
		return nil, rpcErr
	}
	info, err := os.Stat(dir.Path)
	if err != nil || !info.IsDir() {
		return nil, workspaceRPCError("not_directory", "workspace path is not a directory")
	}
	items, err := os.ReadDir(dir.Path)
	if err != nil {
		return nil, workspaceRPCError("io", "workspace directory could not be read")
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name() < items[j].Name() })
	entries := make([]workspaceEntry, 0, min(len(items), workspaceListMax))
	truncated := false
	for _, item := range items {
		relative := path.Join(dir.RelativePath, item.Name())
		if workspacePathDenied(relative, false) {
			continue
		}
		if len(entries) == workspaceListMax {
			truncated = true
			break
		}
		entry := workspaceEntry{Path: relative, Name: item.Name(), Type: workspaceEntryType(item.Type())}
		if itemInfo, err := item.Info(); err == nil {
			entry.Type = workspaceEntryType(itemInfo.Mode())
			entry.ModifiedAt = itemInfo.ModTime().UnixMilli()
			if itemInfo.Mode().IsRegular() {
				entry.Size = itemInfo.Size()
			}
		}
		entries = append(entries, entry)
	}
	return map[string]any{"path": dir.RelativePath, "entries": entries, "truncated": truncated}, nil
}

func hostWorkspaceStat(_ *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request struct {
		Path   string `json:"path"`
		SHA256 bool   `json:"sha256"`
	}
	if rpcErr := decodeWorkspaceParams(params, &request); rpcErr != nil {
		return nil, rpcErr
	}
	file, rpcErr := resolveWorkspaceEntry(request.Path, false)
	if rpcErr != nil {
		return nil, rpcErr
	}
	info, err := os.Stat(file.Path)
	if err != nil {
		return nil, workspaceResolveError(err)
	}
	entry := workspaceEntry{Path: file.RelativePath, Name: path.Base(file.RelativePath), Type: workspaceEntryType(info.Mode()), ModifiedAt: info.ModTime().UnixMilli()}
	if file.RelativePath == "" {
		entry.Name = filepath.Base(file.Root)
	}
	if info.Mode().IsRegular() {
		entry.Size = info.Size()
		if request.SHA256 {
			if info.Size() > workspaceHashMaxBytes {
				return nil, workspaceRPCError("too_large", "file is too large to hash")
			}
			digest, err := workspaceFileSHA256(file.Path)
			if err != nil {
				return nil, workspaceRPCError("io", "file could not be read")
			}
			entry.SHA256 = digest
		}
	}
	return entry, nil
}

func hostWorkspaceRead(_ *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request struct {
		Path     string `json:"path"`
		Offset   int64  `json:"offset"`
		MaxBytes int64  `json:"maxBytes"`
	}
	if rpcErr := decodeWorkspaceParams(params, &request); rpcErr != nil {
		return nil, rpcErr
	}
	if request.Offset < 0 || request.MaxBytes < 0 {
		return nil, &rpcError{Code: -32602, Message: "offset and maxBytes must not be negative", Data: map[string]string{"code": "invalid_params"}}
	}
	maxBytes := request.MaxBytes
	if maxBytes == 0 {
		maxBytes = workspaceReadDefaultBytes
	}
	maxBytes = min(maxBytes, workspaceReadMaxBytes)
	file, rpcErr := resolveWorkspaceEntry(request.Path, false)
	if rpcErr != nil {
		return nil, rpcErr
	}
	handle, err := os.Open(file.Path)
	if err != nil {
		return nil, workspaceResolveError(err)
	}
	defer handle.Close()
	info, err := handle.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, workspaceRPCError("not_file", "workspace path is not a regular file")
	}
	offset := min(request.Offset, info.Size())
	buffer := make([]byte, min(maxBytes, info.Size()-offset))
	read, err := handle.ReadAt(buffer, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, workspaceRPCError("io", "file could not be read")
	}
	next := offset + int64(read)
	return map[string]any{
		"path": file.RelativePath, "data": base64.StdEncoding.EncodeToString(buffer[:read]),
		"offset": offset, "nextOffset": next, "eof": next >= info.Size(),
		"size": info.Size(), "modifiedAt": info.ModTime().UnixMilli(),
	}, nil
}

func hostWorkspaceWrite(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request struct {
		Path          string  `json:"path"`
		Data          string  `json:"data"`
		ExpectedSHA   *string `json:"expectedSha256"`
		CreateParents bool    `json:"createParents"`
	}
	if rpcErr := decodeWorkspaceParams(params, &request); rpcErr != nil {
		return nil, rpcErr
	}
	data, err := base64.StdEncoding.DecodeString(request.Data)
	if err != nil {
		return nil, &rpcError{Code: -32602, Message: "data must be base64", Data: map[string]string{"code": "invalid_params"}}
	}
	if len(data) > workspaceWriteMaxBytes {
		return nil, workspaceRPCError("too_large", "file exceeds %d bytes", workspaceWriteMaxBytes)
	}
	root, rpcErr := workspaceRootOrError()
	if rpcErr != nil {
		return nil, rpcErr
	}
	relative, err := backendfs.CleanProjectRelativePath(request.Path)
	if err != nil {
		return nil, workspaceResolveError(err)
	}
	if workspacePathDenied(relative, true) {
		return nil, workspaceRPCError("denied", "workspace path is not writable by plugins")
	}
	// Capture scope and target existence before any native mutation (including
	// createParents). Failure paths simply discard this completion.
	manager := (*pluginManager)(nil)
	if runtimeHost != nil {
		manager = runtimeHost.ownerManager
	}
	if manager == nil {
		manager = host()
	}
	mutation := backendfs.Mutation{Root: root, Kind: "write", NewPath: relative, NewCanonical: filepath.Join(root, filepath.FromSlash(relative))}
	complete := manager.observeWorkspaceMutation(mutation)
	parent, rpcErr := workspaceWriteParent(root, path.Dir(relative), request.CreateParents)
	if rpcErr != nil {
		return nil, rpcErr
	}
	target := filepath.Join(parent, path.Base(relative))
	mode := os.FileMode(0o644)
	exists := false
	if info, err := os.Lstat(target); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, workspaceRPCError("denied", "plugins cannot write through a symlink")
		}
		if !info.Mode().IsRegular() {
			return nil, workspaceRPCError("not_file", "workspace path is not a regular file")
		}
		exists, mode = true, info.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, workspaceRPCError("io", "workspace path could not be inspected")
	}
	if request.ExpectedSHA != nil {
		want := strings.ToLower(strings.TrimSpace(*request.ExpectedSHA))
		if want == "" && exists {
			return nil, workspaceRPCError("conflict", "file already exists")
		}
		if want != "" {
			if !exists {
				return nil, workspaceRPCError("conflict", "file no longer exists")
			}
			current, err := workspaceFileSHA256(target)
			if err != nil {
				return nil, workspaceRPCError("io", "file could not be read")
			}
			if current != want {
				return nil, workspaceRPCError("conflict", "file changed since it was read")
			}
		}
	}
	mutation.NewCanonical = target
	if err := writeWorkspaceFileAtomic(parent, target, data, mode); err != nil {
		return nil, workspaceRPCError("io", "file could not be written")
	}
	if complete != nil {
		complete(mutation)
	}
	digest := sha256.Sum256(data)
	return map[string]any{"path": relative, "size": len(data), "sha256": hex.EncodeToString(digest[:]), "created": !exists}, nil
}

// workspaceWriteParent resolves (and optionally creates) the directory a new
// file goes into, keeping it inside the root and out of denied directories.
func workspaceWriteParent(root, parentRelative string, create bool) (string, *rpcError) {
	if parentRelative == "." || parentRelative == "" {
		return root, nil
	}
	resolved, err := backendfs.ResolveUnderProjectRoot(root, parentRelative)
	if err != nil && create && backendfs.IsProjectPathNotExist(err) {
		existing, missing := root, strings.Split(parentRelative, "/")
		for index := len(missing) - 1; index > 0; index-- {
			if candidate, err := backendfs.ResolveUnderProjectRoot(root, strings.Join(missing[:index], "/")); err == nil {
				existing, missing = candidate, missing[index:]
				break
			}
		}
		if mkdirErr := os.MkdirAll(filepath.Join(append([]string{existing}, missing...)...), 0o755); mkdirErr != nil {
			return "", workspaceRPCError("io", "parent directory could not be created")
		}
		resolved, err = backendfs.ResolveUnderProjectRoot(root, parentRelative)
	}
	if err != nil {
		return "", workspaceResolveError(err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", workspaceRPCError("not_directory", "parent path is not a directory")
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || workspacePathDenied(filepath.ToSlash(relative), true) {
		return "", workspaceRPCError("denied", "workspace path is not writable by plugins")
	}
	return resolved, nil
}

func writeWorkspaceFileAtomic(dir, target string, data []byte, mode os.FileMode) (err error) {
	temporary, err := os.CreateTemp(dir, ".mincode-plugin-*.tmp")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(temporary.Name())
		}
	}()
	if _, err = temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err = temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if err = temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), target)
}

func workspaceFileSHA256(filePath string) (string, error) {
	handle, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer handle.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, io.LimitReader(handle, workspaceHashMaxBytes+1)); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// ---- events ----

// Direct internal callers capture their generation before waiting for the
// subscription lock; RPC dispatch supplies its immutable request generation.
func hostEventsSubscribe(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	return hostEventsSubscribeGeneration(runtimeHost, runtimeHost.Generation(), params)
}

func hostEventsSubscribeGeneration(runtimeHost *processRuntime, generation uint64, params json.RawMessage) (any, *rpcError) {
	runtimeHost.eventSubscribeMu.Lock()
	defer runtimeHost.eventSubscribeMu.Unlock()

	var request struct {
		Events []string `json:"events"`
	}
	if err := json.Unmarshal(params, &request); err != nil || len(request.Events) > hostEventSubscriptionMax {
		return nil, &rpcError{Code: -32602, Message: "invalid events params", Data: map[string]string{"code": "invalid_params"}}
	}
	subscriptions := map[string]struct{}{}
	for _, event := range request.Events {
		capability, known := hostEvents[event]
		if !known {
			return nil, &rpcError{Code: -32602, Message: fmt.Sprintf("unknown event %q", event), Data: map[string]string{"code": "unknown_event"}}
		}
		if capability != "" {
			if _, granted := runtimeHost.capabilities[capability]; !granted {
				return nil, &rpcError{Code: -32001, Message: fmt.Sprintf("event %q requires the %s capability", event, capability)}
			}
		}
		if !runtimeHost.eventSourceAvailable(event) {
			return nil, workspaceRPCError("unavailable", "event source is unavailable")
		}
		subscriptions[event] = struct{}{}
	}
	runtimeHost.mu.Lock()
	// Stop/reload does not join already-admitted host requests. Check the
	// original RPC generation under the same lock as subscription replacement,
	// including empty replacements, before touching the frontier or any source.
	if runtimeHost.generation != generation {
		runtimeHost.mu.Unlock()
		return nil, &rpcError{Code: -32001, Message: "plugin process generation changed"}
	}
	previousSubscriptions, previousSubscribedAt := runtimeHost.eventSubscriptions, runtimeHost.fileEventsSubscribedAt
	runtimeHost.eventSubscriptions = subscriptions
	runtimeHost.fileEventsSubscribedAt = time.Now()
	runtimeHost.mu.Unlock()
	if _, subscribed := subscriptions[hostEventGitHeadChanged]; subscribed {
		host().ensureGitHeadWatcher()
	}
	manager := runtimeHost.ownerManager
	if manager == nil {
		manager = host()
	}
	if err := manager.syncWorkspaceFileEvents(); err != nil {
		for event := range subscriptions {
			if isWorkspaceFileEvent(event) {
				runtimeHost.mu.Lock()
				if runtimeHost.generation == generation {
					runtimeHost.eventSubscriptions, runtimeHost.fileEventsSubscribedAt = previousSubscriptions, previousSubscribedAt
				}
				runtimeHost.mu.Unlock()
				manager.syncGitStatusEvents()
				return nil, workspaceRPCError("unavailable", "workspace file observation source is unavailable")
			}
		}
	}
	accepted := make([]string, 0, len(subscriptions))
	for event := range subscriptions {
		accepted = append(accepted, event)
	}
	sort.Strings(accepted)
	return map[string]any{"events": accepted}, nil
}

func (runtimeHost *processRuntime) subscribedTo(event string) bool {
	runtimeHost.mu.Lock()
	defer runtimeHost.mu.Unlock()
	_, subscribed := runtimeHost.eventSubscriptions[event]
	return subscribed
}

// describeEvents lists the events and whether this plugin may subscribe.
func (runtimeHost *processRuntime) describeEvents() []map[string]any {
	names := make([]string, 0, len(hostEvents))
	for name := range hostEvents {
		names = append(names, name)
	}
	sort.Strings(names)
	events := make([]map[string]any, 0, len(names))
	for _, name := range names {
		available := runtimeHost.eventSourceAvailable(name)
		entry := map[string]any{"name": name, "available": available}
		if capability := hostEvents[name]; capability != "" {
			_, granted := runtimeHost.capabilities[capability]
			entry["capability"], entry["available"] = capability, granted && available
		}
		events = append(events, entry)
	}
	return events
}

// publishHostEvent delivers an event to every running, workspace-effective
// plugin that subscribed to it. It never blocks the caller.
func (manager *pluginManager) publishHostEvent(event string, data any) {
	params, err := json.Marshal(map[string]any{"event": event, "data": data})
	if err != nil || len(params) > hostEventParamsMaxBytes {
		return
	}
	manager.mu.RLock()
	records := manager.orderedRecordsLocked()
	manager.mu.RUnlock()
	for _, record := range records {
		record.mu.Lock()
		runtimeHost := record.runtime
		deliver := runtimeHost != nil && record.state == pluginStateRunning && manager.workspaceActiveLocked(record)
		record.mu.Unlock()
		if !deliver || !runtimeHost.subscribedTo(event) {
			continue
		}
		if runtimeHost.eventsInflight.Add(1) > hostEventInflightMax {
			runtimeHost.eventsInflight.Add(-1)
			// Selection changes are frequent and superseded by the next one.
			if event != hostEventSelectionChanged {
				record.appendLog("warn", "event dropped while the plugin was busy: "+event)
			}
			continue
		}
		generation := runtimeHost.Generation()
		go func() {
			defer runtimeHost.eventsInflight.Add(-1)
			ctx, cancel := context.WithTimeout(context.Background(), hostEventTimeout)
			defer cancel()
			if _, err := runtimeHost.callGeneration(ctx, generation, hostEventMethod, params); err != nil {
				record.appendLog("warn", "event delivery failed ("+event+"): "+err.Error())
			}
		}()
	}
}

// pluginHostPublishEventHandler accepts editor events from the desktop
// frontend. Paths arrive absolute and leave root-relative; files outside the
// workspace root, VCS internals and credential files are never announced.
func pluginHostPublishEventHandler(_, argsJSON string) (string, error) {
	var args struct {
		Event     string          `json:"event"`
		Path      string          `json:"path"`
		Selection *eventSelection `json:"selection"`
	}
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if !frontendHostEvents[args.Event] {
		return "", fmt.Errorf("unsupported plugin host event %q", args.Event)
	}
	if len(args.Path) > hostEventPathMaxLength {
		return "", errors.New("event path is too long")
	}
	manager := host()
	root := manager.workspaceRootPath()
	relative, inside := workspaceRelativeEventPath(root, args.Path)
	if (args.Event == hostEventFileSaved || args.Event == hostEventSelectionChanged) && !inside {
		return core.EncodeResult(nil)
	}
	if args.Event == hostEventSelectionChanged {
		if !args.Selection.valid() {
			return "", errors.New("invalid selection")
		}
		manager.publishHostEvent(args.Event, map[string]any{"path": relative, "selection": args.Selection})
		return core.EncodeResult(nil)
	}
	var data any
	if inside {
		data = relative
	}
	manager.publishHostEvent(args.Event, map[string]any{"path": data})
	return core.EncodeResult(nil)
}

// eventSelection is a 1-based editor selection; the cursor is at End.
type eventSelection struct {
	StartLine   int `json:"startLine"`
	StartColumn int `json:"startColumn"`
	EndLine     int `json:"endLine"`
	EndColumn   int `json:"endColumn"`
}

func (s *eventSelection) valid() bool {
	if s == nil {
		return false
	}
	for _, value := range []int{s.StartLine, s.StartColumn, s.EndLine, s.EndColumn} {
		if value < 1 || value > hostPositionMax {
			return false
		}
	}
	return true
}

// workspaceRelativeEventPath maps an absolute editor path to a root-relative
// one that plugins may see.
func workspaceRelativeEventPath(root, absolute string) (string, bool) {
	absolute = strings.TrimSpace(absolute)
	if root == "" || absolute == "" || !filepath.IsAbs(absolute) {
		return "", false
	}
	candidate := filepath.Clean(absolute)
	if resolved, err := filepath.EvalSymlinks(candidate); err == nil {
		candidate = resolved
	} else if parent, err := filepath.EvalSymlinks(filepath.Dir(candidate)); err == nil {
		candidate = filepath.Join(parent, filepath.Base(candidate))
	}
	relative, err := filepath.Rel(root, candidate)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	relative = filepath.ToSlash(relative)
	if workspacePathDenied(relative, false) {
		return "", false
	}
	return relative, true
}
