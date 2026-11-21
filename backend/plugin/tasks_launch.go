package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"

	"mincode/app/backend/core"
	projectfs "mincode/app/backend/fs"
	"mincode/app/backend/terminal"
)

// Plugin task launch path, a thin caller of the generic process launch
// framework (plugin_process_launch.go).
//
// The WebView names a task only by (pluginId, taskKey, projectRoot). The host
// looks the definition up in its own snapshot (static) or detection cache
// (detected), re-validates it with the current contract lists, resolves cwd
// symlink-safely inside the canonical project root, and computes a v2
// fingerprint over argv, env, cwd, the executable and every ${pluginRoot}
// file an argument names, plus the task key and project root. Confirmations
// are keyed per task and project (EntryKey = taskKey@<root hash>) so the first
// run in every project needs an explicit confirmation in every approval mode.
// AI tools have no path to these commands, and a restored layout never starts
// a task: the frontend only re-attaches to a live session (attachIfExists).

// taskVarCwd is a host-private variable carrying the resolved working
// directory into resolveProcessLaunch. Task args and env can never name it:
// they are validated against the contract variables only.
const taskVarCwd = "${taskCwd}"

var (
	taskSessionsMu   sync.Mutex
	taskSessions     = map[string]processLaunchSession{}
	taskSessionTable = newProcessLaunchSessionTable(&taskSessionsMu, &taskSessions)

	// Test seams.
	taskStartShell  = terminal.StartProfileShell
	taskAttachShell = terminal.AttachLocalShell
)

// TaskLaunch is the resolved launch shown in the confirmation dialog.
type TaskLaunch struct {
	ResolvedProcessLaunch
	TaskKey     string `json:"taskKey"`
	ProjectRoot string `json:"projectRoot"`
}

type taskPrepareRequest struct {
	PluginID    string `json:"pluginId"`
	TaskKey     string `json:"taskKey"`
	ProjectRoot string `json:"projectRoot"`
}

type taskTrustRequest struct {
	PluginID    string `json:"pluginId"`
	TaskKey     string `json:"taskKey"`
	ProjectRoot string `json:"projectRoot"`
	Fingerprint string `json:"fingerprint"`
}

type taskStartRequest struct {
	PluginID       string  `json:"pluginId"`
	TaskKey        string  `json:"taskKey"`
	ProjectRoot    string  `json:"projectRoot"`
	Fingerprint    string  `json:"fingerprint"`
	SessionID      string  `json:"sessionId"`
	Cols           *uint32 `json:"cols"`
	Rows           *uint32 `json:"rows"`
	AttachIfExists bool    `json:"attachIfExists"`
	// AttachOnly re-attaches to a live session and never starts a process:
	// a restored layout uses it so a task is never re-run automatically.
	AttachOnly bool `json:"attachOnly"`
}

// taskEntryKey scopes trust and sessions to one task in one project.
func taskEntryKey(taskKey, root string) string {
	sum := sha256.Sum256([]byte(root))
	return taskKey + "@" + hex.EncodeToString(sum[:])[:16]
}

// taskDefinitionFor re-validates and returns the current definition of
// taskKey for the canonical root, with its display title.
func (manager *pluginManager) taskDefinitionFor(src processLaunchRecord, taskKey, root string) (taskLaunchDefinition, string, error) {
	source, providerID, taskID, err := parseTaskKey(taskKey)
	if err != nil {
		return taskLaunchDefinition{}, "", err
	}
	if source == taskKindStatic {
		for _, entry := range src.entries {
			task, ok := entry.(TaskContribution)
			if !ok || task.Kind != taskKindStatic || task.ID != taskID {
				continue
			}
			if !slices.Contains(task.Platforms, taskGOOS) {
				return taskLaunchDefinition{}, "", processLaunchError(processLaunchErrUnavailable, "task %q does not support %s", task.ID, taskGOOS)
			}
			if !taskWhenFilesExist(root, task.WhenFiles) {
				return taskLaunchDefinition{}, "", processLaunchError(processLaunchErrUnavailable, "task %q does not apply to this project", task.ID)
			}
			cwd := task.Cwd
			definition, err := validateTaskLaunchDefinition(src.root, task.Command, task.Args, task.Env, &cwd)
			if err != nil {
				return taskLaunchDefinition{}, "", processLaunchError(processLaunchErrForbidden, "task %q %v", task.ID, err)
			}
			return definition, task.Title, nil
		}
		return taskLaunchDefinition{}, "", processLaunchError(processLaunchErrUnavailable, "plugin %q no longer contributes task %q", src.pluginID, taskID)
	}
	providerFound := false
	for _, entry := range src.entries {
		if task, ok := entry.(TaskContribution); ok && task.Kind == taskKindProvider && task.ID == providerID {
			providerFound = slices.Contains(task.Platforms, taskGOOS)
		}
	}
	if !providerFound {
		return taskLaunchDefinition{}, "", processLaunchError(processLaunchErrUnavailable, "plugin %q no longer contributes task provider %q", src.pluginID, providerID)
	}
	state, ok := taskDetectLookup(src.pluginID, root, providerID, taskGeneration(src))
	if ok {
		for _, detected := range state.tasks {
			if detected.ID != taskID {
				continue
			}
			cwd := detected.Definition.Cwd
			definition, err := validateTaskLaunchDefinition(src.root, detected.Definition.Command, detected.Definition.Args, detected.Definition.Env, &cwd)
			if err != nil {
				return taskLaunchDefinition{}, "", processLaunchError(processLaunchErrForbidden, "task %q %v", taskID, err)
			}
			return definition, detected.Title, nil
		}
	}
	return taskLaunchDefinition{}, "", processLaunchError(processLaunchErrUnavailable, "task %q is no longer detected; detect tasks again", taskID)
}

// resolveTaskCwd resolves "${projectRoot}[/<relative>]" inside the canonical
// root, following symlinks only when they stay inside it.
func resolveTaskCwd(root, cwd string) (string, error) {
	rest := ""
	if cwd != taskVarProjectRoot {
		var ok bool
		if rest, ok = strings.CutPrefix(cwd, taskVarProjectRoot+"/"); !ok {
			return "", processLaunchError(processLaunchErrForbidden, "working directory %q is invalid", cwd)
		}
	}
	dir := root
	if rest != "" {
		resolved, err := projectfs.ResolveUnderProjectRoot(root, rest)
		if err != nil {
			return "", processLaunchError(processLaunchErrForbidden, "working directory %q leaves the project", cwd)
		}
		dir = resolved
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return "", processLaunchError(processLaunchErrNotFound, "working directory %q does not exist", dir)
	}
	return dir, nil
}

func (manager *pluginManager) resolveTaskLaunch(pluginID, taskKey, projectRoot string) (TaskLaunch, error) {
	if strings.TrimSpace(pluginID) == "" || strings.TrimSpace(taskKey) == "" {
		return TaskLaunch{}, processLaunchError(processLaunchErrNotFound, "pluginId and taskKey are required")
	}
	root, err := canonicalTaskProjectRoot(projectRoot)
	if err != nil {
		return TaskLaunch{}, err
	}
	src, err := manager.processLaunchSource(pluginID, taskPointKey, "tasks")
	if err != nil {
		return TaskLaunch{}, err
	}
	definition, title, err := manager.taskDefinitionFor(src, taskKey, root)
	if err != nil {
		return TaskLaunch{}, err
	}
	cwd, err := resolveTaskCwd(root, definition.Cwd)
	if err != nil {
		return TaskLaunch{}, err
	}
	pluginRoot, err := processLaunchPluginRoot(src.root)
	if err != nil {
		return TaskLaunch{}, err
	}
	extra, _ := json.Marshal(map[string]string{"taskKey": taskKey, "projectRoot": root})
	entryKey := taskEntryKey(taskKey, root)
	src.entries = nil
	launch, err := resolveProcessLaunch(src,
		processLaunchSpec{Command: definition.Command, Args: definition.Args, Env: definition.Env, Cwd: taskVarCwd},
		processLaunchPolicy{
			Point: taskPointKey, EntryKey: entryKey,
			Variables: map[string]string{
				processLaunchVarPluginRoot: pluginRoot,
				taskVarProjectRoot:         root,
				taskVarCwd:                 cwd,
			},
			HashPluginFiles: true,
			Extra:           extra,
		})
	if err != nil {
		return TaskLaunch{}, err
	}
	// TitleLocalized stays empty: the confirmation dialog shows the plain
	// title (detected tasks have no localized titles).
	launch.Title = title
	return TaskLaunch{ResolvedProcessLaunch: launch, TaskKey: taskKey, ProjectRoot: root}, nil
}

func (manager *pluginManager) prepareTask(request taskPrepareRequest) (TaskLaunch, error) {
	return manager.resolveTaskLaunch(request.PluginID, request.TaskKey, request.ProjectRoot)
}

func (manager *pluginManager) trustTask(request taskTrustRequest) (TaskLaunch, error) {
	launch, err := manager.resolveTaskLaunch(request.PluginID, request.TaskKey, request.ProjectRoot)
	if err != nil {
		return TaskLaunch{}, err
	}
	trusted, err := trustProcessLaunch(launch.ResolvedProcessLaunch, request.Fingerprint, fmt.Sprintf("task %q", launch.TaskKey))
	if err != nil {
		return TaskLaunch{}, err
	}
	launch.ResolvedProcessLaunch = trusted
	return launch, nil
}

func lookupTaskSession(sessionID string) (processLaunchSession, bool) {
	return taskSessionTable.lookup(sessionID)
}

// startTask launches a confirmed task in a new PTY session, or re-attaches to
// a live session the same task started earlier. Both paths run every gate.
func (manager *pluginManager) startTask(request taskStartRequest) (string, error) {
	sessionID := strings.TrimSpace(request.SessionID)
	if sessionID == "" {
		return "", processLaunchError(processLaunchErrForbidden, "sessionId is required")
	}
	live := taskAttachShell(sessionID)
	if !live {
		taskSessionTable.forget(sessionID, 0)
		if request.AttachOnly {
			return "", processLaunchError(processLaunchErrNotFound, "task session %q is not running", sessionID)
		}
	}
	launch, err := manager.resolveTaskLaunch(request.PluginID, request.TaskKey, request.ProjectRoot)
	if err != nil {
		return "", err
	}
	what := fmt.Sprintf("task %q", launch.TaskKey)
	if err := checkProcessLaunchStart(launch.ResolvedProcessLaunch, request.Fingerprint, what); err != nil {
		return "", err
	}
	if live {
		if err := taskSessionTable.checkReattach(sessionID, launch.ResolvedProcessLaunch, request.AttachIfExists || request.AttachOnly, taskAttachShell, what); err != nil {
			return "", err
		}
		return "Shell attached", nil
	}
	beforeExec, err := verifyProcessLaunchFiles(launch.ResolvedProcessLaunch)
	if err != nil {
		return "", err
	}
	token := taskSessionTable.register(sessionID, processLaunchSession{
		pluginID: launch.PluginID, point: launch.Point, entryKey: launch.EntryKey, fingerprint: launch.Fingerprint,
	})
	message, err := taskStartShell(terminal.ProfileShellSpec{
		SessionID: sessionID, Executable: launch.Argv[0], Args: launch.Argv[1:], Env: launch.Env, Cwd: launch.Cwd,
		Cols: request.Cols, Rows: request.Rows,
		BeforeExec: beforeExec,
		OnExit:     func() { taskSessionTable.forget(sessionID, token) },
	})
	if err != nil {
		taskSessionTable.forget(sessionID, token)
		if code := processLaunchErrorCode(err); code != "" {
			return "", err
		}
		return "", processLaunchError(processLaunchErrUnavailable, "%v", err)
	}
	return message, nil
}

func pluginHostTaskPrepareHandler(_, args string) (string, error) {
	var request taskPrepareRequest
	if err := core.DecodeArgsPayload(args, &request); err != nil {
		return "", err
	}
	launch, err := host().prepareTask(request)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(launch)
}

func pluginHostTaskTrustHandler(_, args string) (string, error) {
	var request taskTrustRequest
	if err := core.DecodeArgsPayload(args, &request); err != nil {
		return "", err
	}
	launch, err := host().trustTask(request)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(launch)
}

func pluginHostTaskStartHandler(_, args string) (string, error) {
	var request taskStartRequest
	if err := core.DecodeArgsPayload(args, &request); err != nil {
		return "", err
	}
	message, err := host().startTask(request)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(message)
}

func init() {
	core.Register("plugin_host_task_prepare", pluginHostTaskPrepareHandler)
	core.Register("plugin_host_task_trust", pluginHostTaskTrustHandler)
	core.Register("plugin_host_task_start", pluginHostTaskStartHandler)
}
