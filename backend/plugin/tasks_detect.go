package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"mincode/app/backend/core"
	projectfs "mincode/app/backend/fs"
	"mincode/app/plugins/api"
)

// Task listing and user-initiated detection.
//
// plugin_host_tasks_list returns, for one canonical project root, the static
// tasks whose platform matches and whose when.files all exist (resolved
// symlink-safely inside the project), plus the detected tasks cached for the
// root. Listing never activates a plugin.
//
// plugin_host_tasks_detect calls a provider's read-only action with
// {provider, project: {name, root?}, platform} — root only when the plugin
// declares workspace.read — and validates the strict result {"tasks": [...]}.
// Every detected task passes exactly the static validator (without when);
// invalid tasks are dropped and counted. Results are cached in memory per
// (plugin, generation, provider, root) for at most detectCacheRootsPerPlugin
// roots per plugin (least recently used evicted), and purged when the plugin
// is disabled, removed or loaded from a different package generation.

var (
	taskDetectedMax        = int(taskSpec.Limit("detectedTasksMax"))
	taskDetectPayloadMax   = int(taskSpec.Limit("detectPayloadMaxBytes"))
	taskDetectResultMax    = int(taskSpec.Limit("detectResultMaxBytes"))
	taskDetectTimeout      = time.Duration(taskSpec.Limit("detectTimeoutMs")) * time.Millisecond
	taskDetectConcurrency  = int(taskSpec.Limit("detectConcurrentPerPlugin"))
	taskDetectCacheRootMax = int(taskSpec.Limit("detectCacheRootsPerPlugin"))

	// taskGOOS is the platform seam for tests.
	taskGOOS = terminalProfileGOOS
)

// detectedTask is one validated detected task.
type detectedTask struct {
	ID         string
	Title      string
	Detail     string
	Group      string
	Definition taskLaunchDefinition
}

type taskDetectState struct {
	generation string
	tasks      []detectedTask
	dropped    int
	detectedAt int64
	err        string
}

type taskDetectRoot struct {
	providers map[string]*taskDetectState
	used      int64
}

var taskDetectCache = struct {
	mu      sync.Mutex
	seq     int64
	plugins map[string]map[string]*taskDetectRoot // pluginID → canonical root → state
}{plugins: map[string]map[string]*taskDetectRoot{}}

// taskGeneration identifies the package a record was loaded from; a cached
// detection from another generation is stale.
func taskGeneration(src processLaunchRecord) string {
	return src.pluginVersion + "\x00" + src.devGeneration + "\x00" + src.root
}

func purgeTaskDetectCache(pluginID string) {
	taskDetectCache.mu.Lock()
	delete(taskDetectCache.plugins, pluginID)
	taskDetectCache.mu.Unlock()
}

func taskDetectLookup(pluginID, root, providerID, generation string) (taskDetectState, bool) {
	taskDetectCache.mu.Lock()
	defer taskDetectCache.mu.Unlock()
	entry := taskDetectCache.plugins[pluginID][root]
	if entry == nil {
		return taskDetectState{}, false
	}
	state := entry.providers[providerID]
	if state == nil || state.generation != generation {
		return taskDetectState{}, false
	}
	taskDetectCache.seq++
	entry.used = taskDetectCache.seq
	return *state, true
}

func taskDetectStore(pluginID, root, providerID string, state taskDetectState) {
	taskDetectCache.mu.Lock()
	defer taskDetectCache.mu.Unlock()
	roots := taskDetectCache.plugins[pluginID]
	if roots == nil {
		roots = map[string]*taskDetectRoot{}
		taskDetectCache.plugins[pluginID] = roots
	}
	entry := roots[root]
	if entry == nil {
		entry = &taskDetectRoot{providers: map[string]*taskDetectState{}}
		roots[root] = entry
	}
	taskDetectCache.seq++
	entry.used = taskDetectCache.seq
	entry.providers[providerID] = &state
	for len(roots) > taskDetectCacheRootMax {
		oldest, oldestUsed := "", int64(0)
		for key, candidate := range roots {
			if oldest == "" || candidate.used < oldestUsed {
				oldest, oldestUsed = key, candidate.used
			}
		}
		delete(roots, oldest)
	}
}

func init() {
	registerRecordRemovalHook(func(_ *pluginManager, pluginID string, _ removalReason) {
		purgeTaskDetectCache(pluginID)
	})
	registerRecordLifecycleHook(func(_ *pluginManager, record *pluginRecord) {
		// Stop and crash keep the cache (detection is user-initiated and every
		// run re-validates); disable purges it, and a different package
		// generation makes entries stale through taskGeneration.
		record.mu.Lock()
		disabled := !record.enabled
		pluginID := record.id
		record.mu.Unlock()
		if disabled {
			purgeTaskDetectCache(pluginID)
		}
	})
}

// canonicalTaskProjectRoot canonicalizes a project root and refuses roots
// that are not a project: the filesystem root, the user home, and anything
// inside the application data or plugin directories.
func canonicalTaskProjectRoot(projectRoot string) (string, error) {
	projectRoot = strings.TrimSpace(projectRoot)
	if projectRoot == "" || !filepath.IsAbs(projectRoot) || strings.ContainsRune(projectRoot, 0) {
		return "", processLaunchError(processLaunchErrForbidden, "projectRoot must be an absolute path")
	}
	root, err := projectfs.CanonicalProjectRoot(projectRoot)
	if err != nil {
		return "", processLaunchError(processLaunchErrNotFound, "project folder %q does not exist", projectRoot)
	}
	if filepath.Dir(root) == root {
		return "", processLaunchError(processLaunchErrForbidden, "tasks cannot run in the filesystem root")
	}
	canonical := func(path string) string {
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			return resolved
		}
		return filepath.Clean(path)
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" && canonical(home) == root {
		return "", processLaunchError(processLaunchErrForbidden, "tasks cannot run in the user home folder")
	}
	for _, protected := range []string{userDataDir(), pluginsDirectory()} {
		if strings.TrimSpace(protected) == "" {
			continue
		}
		base := canonical(protected)
		if root == base || strings.HasPrefix(root, base+string(filepath.Separator)) {
			return "", processLaunchError(processLaunchErrForbidden, "tasks cannot run inside the application data folder")
		}
	}
	return root, nil
}

// taskWhenFilesExist reports whether every when.files path exists under the
// canonical root without leaving it (symlinks followed only inside the root).
func taskWhenFilesExist(root string, files []string) bool {
	for _, file := range files {
		resolved, err := projectfs.ResolveUnderProjectRoot(root, file)
		if err != nil {
			return false
		}
		if _, err := os.Lstat(resolved); err != nil {
			return false
		}
	}
	return true
}

// TaskListItem is one runnable task in plugin_host_tasks_list.
type TaskListItem struct {
	PluginID       string            `json:"pluginId"`
	PluginName     string            `json:"pluginName"`
	Key            string            `json:"key"`
	Source         string            `json:"source"`
	ProviderID     string            `json:"providerId,omitempty"`
	Title          string            `json:"title"`
	TitleLocalized map[string]string `json:"titleLocalized,omitempty"`
	Detail         string            `json:"detail,omitempty"`
	Group          string            `json:"group"`
	Command        string            `json:"command"`
	Args           []string          `json:"args"`
}

// TaskProviderItem is one provider and its detection state for the root.
type TaskProviderItem struct {
	PluginID       string            `json:"pluginId"`
	PluginName     string            `json:"pluginName"`
	ProviderID     string            `json:"providerId"`
	Title          string            `json:"title"`
	TitleLocalized map[string]string `json:"titleLocalized,omitempty"`
	State          string            `json:"state"` // idle | ready | error
	DetectedAt     int64             `json:"detectedAt,omitempty"`
	Dropped        int               `json:"dropped,omitempty"`
	Error          string            `json:"error,omitempty"`
}

type TaskList struct {
	ProjectRoot string             `json:"projectRoot"`
	Tasks       []TaskListItem     `json:"tasks"`
	Providers   []TaskProviderItem `json:"providers"`
}

type taskListRequest struct {
	ProjectRoot string `json:"projectRoot"`
}

// taskSources returns the gated launch records of every plugin contributing
// tasks, in manager order. Plugins failing a gate are skipped.
func (manager *pluginManager) taskSources() []processLaunchRecord {
	if _, _, ok := enabledContributionPoint(taskPointKey); !ok {
		return nil
	}
	manager.mu.RLock()
	ids := append([]string(nil), manager.order...)
	for id := range manager.records {
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	manager.mu.RUnlock()
	sources := []processLaunchRecord{}
	for _, id := range ids {
		if src, err := manager.processLaunchSource(id, taskPointKey, "tasks"); err == nil && len(src.entries) > 0 {
			sources = append(sources, src)
		}
	}
	return sources
}

func (manager *pluginManager) listTasks(projectRoot string) (TaskList, error) {
	root, err := canonicalTaskProjectRoot(projectRoot)
	if err != nil {
		return TaskList{}, err
	}
	list := TaskList{ProjectRoot: root, Tasks: []TaskListItem{}, Providers: []TaskProviderItem{}}
	for _, src := range manager.taskSources() {
		generation := taskGeneration(src)
		for _, entry := range src.entries {
			task, ok := entry.(TaskContribution)
			if !ok || !slices.Contains(task.Platforms, taskGOOS) {
				continue
			}
			switch task.Kind {
			case taskKindStatic:
				if !taskWhenFilesExist(root, task.WhenFiles) {
					continue
				}
				list.Tasks = append(list.Tasks, TaskListItem{
					PluginID: src.pluginID, PluginName: src.pluginName, Key: staticTaskKey(task.ID), Source: taskKindStatic,
					Title: task.Title, TitleLocalized: cloneStringMap(task.TitleLocalized), Detail: task.Detail, Group: task.Group,
					Command: task.Command, Args: append([]string{}, task.Args...),
				})
			case taskKindProvider:
				provider := TaskProviderItem{
					PluginID: src.pluginID, PluginName: src.pluginName, ProviderID: task.ID,
					Title: task.Title, TitleLocalized: cloneStringMap(task.TitleLocalized), State: "idle",
				}
				if state, ok := taskDetectLookup(src.pluginID, root, task.ID, generation); ok {
					provider.DetectedAt, provider.Dropped, provider.Error = state.detectedAt, state.dropped, state.err
					provider.State = "ready"
					if state.err != "" {
						provider.State = "error"
					}
					for _, detected := range state.tasks {
						list.Tasks = append(list.Tasks, TaskListItem{
							PluginID: src.pluginID, PluginName: src.pluginName, Key: detectedTaskKey(task.ID, detected.ID),
							Source: "detected", ProviderID: task.ID, Title: detected.Title, Detail: detected.Detail, Group: detected.Group,
							Command: detected.Definition.Command, Args: append([]string{}, detected.Definition.Args...),
						})
					}
				}
				list.Providers = append(list.Providers, provider)
			}
		}
	}
	return list, nil
}

type taskDetectRequest struct {
	PluginID    string `json:"pluginId"`
	ProviderID  string `json:"providerId"`
	ProjectRoot string `json:"projectRoot"`
	RequestID   string `json:"requestId"`
}

type taskDetectProject struct {
	Name string `json:"name"`
	Root string `json:"root,omitempty"`
}

type taskDetectPayload struct {
	Provider string            `json:"provider"`
	Project  taskDetectProject `json:"project"`
	Platform string            `json:"platform"`
}

// TaskDetectResult reports one detection.
type TaskDetectResult struct {
	Count   int `json:"count"`
	Dropped int `json:"dropped"`
}

// taskProviderLocked finds a provider entry in the record's current snapshot.
func taskProviderLocked(record *pluginRecord, providerID string) (TaskContribution, error) {
	if record.manifest != nil {
		if resolved := record.manifest.contribution(taskPointKey); resolved != nil {
			for _, entry := range resolved.Entries {
				if task, ok := entry.(TaskContribution); ok && task.Kind == taskKindProvider && task.ID == providerID {
					return task, nil
				}
			}
		}
	}
	return TaskContribution{}, newContributionError(contributionErrNotFound, "task provider %q is not contributed by %q", providerID, record.id)
}

func (manager *pluginManager) detectTasks(ctx context.Context, request taskDetectRequest) (TaskDetectResult, error) {
	providerID := strings.TrimSpace(request.ProviderID)
	if !taskIDPattern.MatchString(providerID) {
		return TaskDetectResult{}, newContributionError(contributionErrInvalidRequest, "providerId %q is invalid", request.ProviderID)
	}
	root, err := canonicalTaskProjectRoot(request.ProjectRoot)
	if err != nil {
		return TaskDetectResult{}, err
	}
	// The launch gates (enabled, effective, shell granted) apply to detection
	// too: a provider only exists to feed runnable tasks.
	src, err := manager.processLaunchSource(request.PluginID, taskPointKey, "tasks")
	if err != nil {
		return TaskDetectResult{}, err
	}
	var provider TaskContribution
	found := false
	for _, entry := range src.entries {
		if task, ok := entry.(TaskContribution); ok && task.Kind == taskKindProvider && task.ID == providerID {
			provider, found = task, true
		}
	}
	if !found {
		return TaskDetectResult{}, newContributionError(contributionErrNotFound, "task provider %q is not contributed by %q", providerID, src.pluginID)
	}
	if !slices.Contains(provider.Platforms, taskGOOS) {
		return TaskDetectResult{}, newContributionError(contributionErrUnavailable, "task provider %q does not support %s", providerID, taskGOOS)
	}
	record, err := manager.record(src.pluginID)
	if err != nil {
		return TaskDetectResult{}, newContributionError(contributionErrNotFound, "plugin %q not found", src.pluginID)
	}
	record.mu.Lock()
	action, _ := contributedActionLocked(record, provider.Action)
	workspaceRead := manifestHasCapability(record.manifest, api.CapabilityWorkspaceRead)
	record.mu.Unlock()

	payload := taskDetectPayload{Provider: providerID, Project: taskDetectProject{Name: filepath.Base(root)}, Platform: taskGOOS}
	if workspaceRead {
		payload.Project.Root = root
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(payload); err != nil {
		return TaskDetectResult{}, newContributionError(contributionErrInvalidRequest, "encode payload: %v", err)
	}
	encoded := bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))
	limit := taskDetectPayloadMax
	if action.MaxPayloadBytes > 0 && action.MaxPayloadBytes < limit {
		limit = action.MaxPayloadBytes
	}
	if len(encoded) > limit {
		return TaskDetectResult{}, newContributionError(contributionErrTooLarge, "payload is %d bytes, task provider %q allows %d", len(encoded), providerID, limit)
	}
	raw, err := manager.invokeContributedAction(ctx, contributedActionCall{
		PluginID: src.pluginID, Point: taskPointKey, EntryID: providerID, ActionID: provider.Action,
		RequestID: request.RequestID, Payload: encoded, Timeout: taskDetectTimeout,
		MaxResultBytes: taskDetectResultMax, Concurrency: taskDetectConcurrency,
		Validate: func(record *pluginRecord) error {
			current, err := taskProviderLocked(record, providerID)
			if err != nil {
				return err
			}
			if current.Action != provider.Action {
				return newContributionError(contributionErrUnavailable, "task provider %q changed its action", providerID)
			}
			return nil
		},
	})
	generation := taskGeneration(src)
	if err != nil {
		var coded *contributionError
		// A cancelled request keeps the previous result.
		if !errors.As(err, &coded) || coded.Code != contributionErrCancelled {
			taskDetectStore(src.pluginID, root, providerID, taskDetectState{generation: generation, detectedAt: time.Now().UnixMilli(), err: err.Error()})
		}
		return TaskDetectResult{}, err
	}
	tasks, dropped, err := parseDetectedTasks(src.root, raw)
	if err != nil {
		taskDetectStore(src.pluginID, root, providerID, taskDetectState{generation: generation, detectedAt: time.Now().UnixMilli(), err: err.Error()})
		return TaskDetectResult{}, err
	}
	taskDetectStore(src.pluginID, root, providerID, taskDetectState{
		generation: generation, tasks: tasks, dropped: dropped, detectedAt: time.Now().UnixMilli(),
	})
	return TaskDetectResult{Count: len(tasks), Dropped: dropped}, nil
}

type detectedTaskResult struct {
	Tasks []json.RawMessage `json:"tasks"`
}

type detectedTaskEntry struct {
	ID      string            `json:"id"`
	Title   string            `json:"title"`
	Detail  *string           `json:"detail"`
	Group   *string           `json:"group"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	Cwd     *string           `json:"cwd"`
}

// parseDetectedTasks validates the strict result {"tasks": [...]}. Invalid or
// repeated tasks are dropped and counted; a malformed envelope or more than
// detectedTasksMax tasks fails the detection.
func parseDetectedTasks(pluginRoot string, raw json.RawMessage) ([]detectedTask, int, error) {
	var result detectedTaskResult
	if err := decodeStrictJSON(raw, &result, true); err != nil || result.Tasks == nil {
		return nil, 0, newContributionError(contributionErrInvalidResult, "result must be {\"tasks\": [...]}")
	}
	if len(result.Tasks) > taskDetectedMax {
		return nil, 0, newContributionError(contributionErrInvalidResult, "result has %d tasks, the limit is %d", len(result.Tasks), taskDetectedMax)
	}
	tasks := []detectedTask{}
	seen := map[string]bool{}
	dropped := 0
	for _, item := range result.Tasks {
		task, err := validateDetectedTask(pluginRoot, item)
		if err != nil || seen[task.ID] {
			dropped++
			continue
		}
		seen[task.ID] = true
		tasks = append(tasks, task)
	}
	sort.SliceStable(tasks, func(i, j int) bool { return tasks[i].Title < tasks[j].Title })
	return tasks, dropped, nil
}

func validateDetectedTask(pluginRoot string, raw json.RawMessage) (detectedTask, error) {
	var entry detectedTaskEntry
	if err := decodeStrictJSON(raw, &entry, true); err != nil {
		return detectedTask{}, err
	}
	id := strings.TrimSpace(entry.ID)
	if !taskDetectedIDPattern.MatchString(id) {
		return detectedTask{}, fmt.Errorf("detected task id %q is invalid", id)
	}
	title, err := validateContributionLine(entry.Title, taskTitleMaxRunes, true)
	if err != nil {
		return detectedTask{}, fmt.Errorf("title %w", err)
	}
	detail, group, err := validateTaskDetailGroup(entry.Detail, entry.Group)
	if err != nil {
		return detectedTask{}, err
	}
	definition, err := validateTaskLaunchDefinition(pluginRoot, entry.Command, entry.Args, entry.Env, entry.Cwd)
	if err != nil {
		return detectedTask{}, err
	}
	return detectedTask{ID: id, Title: title, Detail: detail, Group: group, Definition: definition}, nil
}

func pluginHostTasksListHandler(_, args string) (string, error) {
	var request taskListRequest
	if err := core.DecodeArgsPayload(args, &request); err != nil {
		return "", err
	}
	list, err := host().listTasks(request.ProjectRoot)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(list)
}

func pluginHostTasksDetectHandler(_, args string) (string, error) {
	var request taskDetectRequest
	if err := core.DecodeArgsPayload(args, &request); err != nil {
		return "", err
	}
	result, err := host().detectTasks(context.Background(), request)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(result)
}

func init() {
	core.Register("plugin_host_tasks_list", pluginHostTasksListHandler)
	core.Register("plugin_host_tasks_detect", pluginHostTasksDetectHandler)
}
