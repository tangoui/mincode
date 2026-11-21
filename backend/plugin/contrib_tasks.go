package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// Tasks (contributes.tasks) let a process plugin that declares the shell
// capability offer named commands for a project: the Tasks tab in the project
// sidebar and "Run Task" palette commands. An entry only describes a launch;
// it never runs anything by itself:
//
//   - "static" tasks are declared in the manifest and listed only when every
//     when.files path exists in the project;
//   - "provider" entries name a read-only, non-AI-callable action the host
//     calls only on an explicit user request (tasks_detect.go); every detected
//     task passes exactly the static validator;
//   - launches are resolved by the host from its own snapshot or detection
//     cache by (pluginId, taskKey, projectRoot) — the WebView never supplies
//     command, args, env or cwd — and re-validated with the current contract
//     lists at every prepare and start (tasks_launch.go);
//   - the first run per task and project, and every run after the resolved
//     launch, a ${pluginRoot} file or the plugin version changed, needs an
//     explicit user confirmation in every approval mode;
//   - AI tools cannot run plugin tasks.
//
// Commands are a bare program name looked up on the login PATH or
// "${pluginRoot}/<relative>"; absolute paths are rejected. Arguments and
// environment values may use only ${projectRoot} and ${pluginRoot}; cwd is
// ${projectRoot} or ${projectRoot}/<relative>, resolved symlink-safely inside
// the project at launch.

const (
	taskPointKey        = "tasks"
	taskKindStatic      = "static"
	taskKindProvider    = "provider"
	taskVarProjectRoot  = "${projectRoot}"
	taskDefaultGroup    = "other"
	taskKeyStaticPrefix = "static:"
	taskKeyDetected     = "detected:"
)

var (
	taskSpec              = mustContributionSpec(taskPointKey)
	taskTitleMaxRunes     = int(taskSpec.Limit("titleMaxRunes"))
	taskDetailMaxRunes    = int(taskSpec.Limit("detailMaxRunes"))
	taskLocalesMax        = int(taskSpec.Limit("localesMax"))
	taskArgsMax           = int(taskSpec.Limit("argsMax"))
	taskArgMaxRunes       = int(taskSpec.Limit("argMaxRunes"))
	taskArgsTotalBytes    = int(taskSpec.Limit("argsTotalBytes"))
	taskEnvMax            = int(taskSpec.Limit("envMax"))
	taskEnvValueMaxRunes  = int(taskSpec.Limit("envValueMaxRunes"))
	taskCommandMaxLength  = int(taskSpec.Limit("commandMaxLength"))
	taskWhenFilesMax      = int(taskSpec.Limit("whenFilesMax"))
	taskWhenFileMaxLength = int(taskSpec.Limit("whenFileMaxLength"))

	taskIDPattern          = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	taskDetectedIDPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9:._-]{0,63}$`)
	taskWhenSegmentPattern = regexp.MustCompile(`^[A-Za-z0-9._@+-]+$`)
)

// TaskContribution is one validated contributes.tasks entry as kept in the
// snapshot. Static tasks carry the unresolved launch (variables intact);
// providers carry their action. SpecFingerprint hashes the canonical entry.
type TaskContribution struct {
	ID              string            `json:"id"`
	Kind            string            `json:"kind"`
	Title           string            `json:"title"`
	TitleLocalized  map[string]string `json:"titleLocalized,omitempty"`
	Detail          string            `json:"detail,omitempty"`
	Group           string            `json:"group,omitempty"`
	Command         string            `json:"command,omitempty"`
	Args            []string          `json:"args,omitempty"`
	Env             map[string]string `json:"env,omitempty"`
	Cwd             string            `json:"cwd,omitempty"`
	WhenFiles       []string          `json:"whenFiles,omitempty"`
	Action          string            `json:"action,omitempty"`
	Platforms       []string          `json:"platforms"`
	SpecFingerprint string            `json:"specFingerprint"`
}

type taskManifestWhen struct {
	Files []string `json:"files"`
}

type taskManifestEntry struct {
	ID             string            `json:"id"`
	Kind           string            `json:"kind"`
	Title          string            `json:"title"`
	TitleLocalized map[string]string `json:"titleLocalized"`
	Detail         *string           `json:"detail"`
	Group          *string           `json:"group"`
	Command        *string           `json:"command"`
	Args           []string          `json:"args"`
	Env            map[string]string `json:"env"`
	Cwd            *string           `json:"cwd"`
	When           *taskManifestWhen `json:"when"`
	Action         *string           `json:"action"`
	Platforms      []string          `json:"platforms"`
}

// taskLaunchDefinition is the unresolved launch shared by static and detected
// tasks. Cwd is "${projectRoot}" or "${projectRoot}/<relative>".
type taskLaunchDefinition struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	Cwd     string            `json:"cwd"`
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:              taskPointKey,
		Decode:           decodeTaskEntry,
		DuplicateMessage: func(id string) string { return fmt.Sprintf("task id %q is duplicated", id) },
		IssuePrefix:      "invalid tasks contribution",
		IssueText:        taskIssueText,
		EntryNoun:        "tasks",
	})
}

func taskIssueText(issue ContributionIssue) string {
	if issue.ID != "" {
		return fmt.Sprintf("task %q skipped: %s", issue.ID, issue.Message)
	}
	return fmt.Sprintf("task entry %d skipped: %s", issue.Entry, issue.Message)
}

func decodeTaskEntry(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var entry taskManifestEntry
	if err := decodeStrictJSON(raw, &entry, true); err != nil {
		return nil, strings.TrimSpace(entry.ID), fmt.Errorf("parse task: %w", err)
	}
	id := strings.TrimSpace(entry.ID)
	if !taskIDPattern.MatchString(id) {
		return nil, id, fmt.Errorf("task id %q must match ^[a-z0-9][a-z0-9-]{0,31}$", id)
	}
	task := TaskContribution{ID: id, Kind: entry.Kind}
	title, err := validateContributionLine(entry.Title, taskTitleMaxRunes, true)
	if err != nil {
		return nil, id, fmt.Errorf("task %q title %w", id, err)
	}
	task.Title = title
	if task.TitleLocalized, err = validateContributionLocalized(entry.TitleLocalized, taskTitleMaxRunes, taskLocalesMax); err != nil {
		return nil, id, fmt.Errorf("task %q titleLocalized %w", id, err)
	}
	if task.Platforms, err = normalizeTaskPlatforms(entry.Platforms); err != nil {
		return nil, id, fmt.Errorf("task %q %w", id, err)
	}
	switch entry.Kind {
	case taskKindStatic:
		if entry.Action != nil {
			return nil, id, fmt.Errorf("task %q is static and must not declare action", id)
		}
		if entry.Command == nil {
			return nil, id, fmt.Errorf("task %q command is required", id)
		}
		if task.Detail, task.Group, err = validateTaskDetailGroup(entry.Detail, entry.Group); err != nil {
			return nil, id, fmt.Errorf("task %q %w", id, err)
		}
		definition, err := validateTaskLaunchDefinition(ctx.root, *entry.Command, entry.Args, entry.Env, entry.Cwd)
		if err != nil {
			return nil, id, fmt.Errorf("task %q %w", id, err)
		}
		task.Command, task.Args, task.Env, task.Cwd = definition.Command, definition.Args, definition.Env, definition.Cwd
		if entry.When != nil {
			if task.WhenFiles, err = validateTaskWhenFiles(entry.When.Files); err != nil {
				return nil, id, fmt.Errorf("task %q %w", id, err)
			}
		}
	case taskKindProvider:
		if entry.Command != nil || entry.Args != nil || entry.Env != nil || entry.Cwd != nil || entry.When != nil || entry.Detail != nil || entry.Group != nil {
			return nil, id, fmt.Errorf("task %q is a provider and may declare only id, kind, title, titleLocalized, action and platforms", id)
		}
		actionID := ""
		if entry.Action != nil {
			actionID = strings.TrimSpace(*entry.Action)
		}
		action, ok := ctx.actions[actionID]
		if actionID == "" || !ok {
			return nil, id, fmt.Errorf("task %q action %q is not one of this plugin's contributes.actions", id, actionID)
		}
		if action.Risk != "read" {
			return nil, id, fmt.Errorf("task %q action %q must have risk \"read\", not %q", id, actionID, action.Risk)
		}
		if action.AICallable {
			return nil, id, fmt.Errorf("task %q action %q must not be aiCallable (task detection is user-initiated)", id, actionID)
		}
		task.Action = actionID
	default:
		return nil, id, fmt.Errorf("task %q kind must be static or provider", id)
	}
	task.SpecFingerprint = taskSpecFingerprint(task)
	return task, id, nil
}

func validateTaskDetailGroup(detail, group *string) (string, string, error) {
	outDetail, outGroup := "", taskDefaultGroup
	if detail != nil {
		value, err := validateContributionLine(*detail, taskDetailMaxRunes, false)
		if err != nil {
			return "", "", fmt.Errorf("detail %w", err)
		}
		outDetail = value
	}
	if group != nil {
		if !slices.Contains(taskSpec.List("groups"), *group) {
			return "", "", fmt.Errorf("group %q must be one of %s", *group, strings.Join(taskSpec.List("groups"), ", "))
		}
		outGroup = *group
	}
	return outDetail, outGroup, nil
}

func normalizeTaskPlatforms(platforms []string) ([]string, error) {
	all := taskSpec.List("platforms")
	if platforms == nil {
		return append([]string(nil), all...), nil
	}
	if len(platforms) == 0 {
		return nil, errors.New("platforms must not be empty")
	}
	seen := map[string]bool{}
	for _, platform := range platforms {
		if !slices.Contains(all, platform) {
			return nil, fmt.Errorf("platform %q must be darwin, linux or windows", platform)
		}
		if seen[platform] {
			return nil, fmt.Errorf("lists platform %q more than once", platform)
		}
		seen[platform] = true
	}
	out := []string{}
	for _, platform := range all {
		if seen[platform] {
			out = append(out, platform)
		}
	}
	return out, nil
}

// validateTaskLaunchDefinition is the one validator for static and detected
// tasks, run at install, on detection and again at every prepare and start
// with the current contract lists. root is the plugin directory; a
// ${pluginRoot} executable must exist there.
func validateTaskLaunchDefinition(root, rawCommand string, args []string, env map[string]string, cwd *string) (taskLaunchDefinition, error) {
	variables := taskSpec.List("variables")
	command := strings.TrimSpace(rawCommand)
	if command == "" {
		return taskLaunchDefinition{}, errors.New("command is required")
	}
	if len(command) > taskCommandMaxLength {
		return taskLaunchDefinition{}, fmt.Errorf("command exceeds %d characters", taskCommandMaxLength)
	}
	if err := validateProcessLaunchText(command, taskCommandMaxLength, []string{processLaunchVarPluginRoot}); err != nil {
		return taskLaunchDefinition{}, fmt.Errorf("command %w", err)
	}
	if rest, ok := strings.CutPrefix(command, processLaunchVarPluginRoot); ok {
		relative, found := strings.CutPrefix(rest, "/")
		if !found {
			return taskLaunchDefinition{}, errors.New("command must be ${pluginRoot}/<relative path>")
		}
		if _, err := checkProcessLaunchRelative(relative); err != nil {
			return taskLaunchDefinition{}, fmt.Errorf("command %w", err)
		}
		if _, err := resolveProcessLaunchExecutable(root, relative); err != nil {
			return taskLaunchDefinition{}, fmt.Errorf("command %w", err)
		}
	} else if strings.Contains(command, "${") {
		return taskLaunchDefinition{}, errors.New("command may only use ${pluginRoot} as a prefix")
	} else if !processLaunchBareCommand.MatchString(command) {
		return taskLaunchDefinition{}, errors.New("command must be a bare program name or ${pluginRoot}/<relative path> (absolute paths are not allowed)")
	}
	definition := taskLaunchDefinition{Command: command, Args: []string{}, Env: map[string]string{}, Cwd: taskVarProjectRoot}

	if len(args) > taskArgsMax {
		return taskLaunchDefinition{}, fmt.Errorf("has more than %d args", taskArgsMax)
	}
	total := 0
	for index, arg := range args {
		if err := validateProcessLaunchText(arg, taskArgMaxRunes, variables); err != nil {
			return taskLaunchDefinition{}, fmt.Errorf("args[%d] %w", index, err)
		}
		total += len(arg)
		definition.Args = append(definition.Args, arg)
	}
	if total > taskArgsTotalBytes {
		return taskLaunchDefinition{}, fmt.Errorf("args exceed %d bytes in total", taskArgsTotalBytes)
	}

	if len(env) > taskEnvMax {
		return taskLaunchDefinition{}, fmt.Errorf("env has more than %d variables", taskEnvMax)
	}
	for _, name := range slices.Sorted(maps.Keys(env)) {
		if err := validateProcessLaunchEnvName(taskSpec, name); err != nil {
			return taskLaunchDefinition{}, err
		}
		if err := validateProcessLaunchText(env[name], taskEnvValueMaxRunes, variables); err != nil {
			return taskLaunchDefinition{}, fmt.Errorf("env %s %w", name, err)
		}
		definition.Env[name] = env[name]
	}

	if cwd != nil {
		value := strings.TrimSpace(*cwd)
		if value != taskVarProjectRoot {
			rest, ok := strings.CutPrefix(value, taskVarProjectRoot+"/")
			if !ok {
				return taskLaunchDefinition{}, errors.New("cwd must be ${projectRoot}, optionally followed by /<relative path>")
			}
			if err := validateProcessLaunchText(rest, taskCommandMaxLength, nil); err != nil {
				return taskLaunchDefinition{}, fmt.Errorf("cwd %w", err)
			}
			if _, err := checkProcessLaunchRelative(rest); err != nil {
				return taskLaunchDefinition{}, fmt.Errorf("cwd %w", err)
			}
		}
		definition.Cwd = value
	}
	return definition, nil
}

// validateTaskWhenFiles accepts literal project-relative paths: "/"-separated
// segments of [A-Za-z0-9._@+-], no "." or "..", no globs.
func validateTaskWhenFiles(files []string) ([]string, error) {
	if len(files) == 0 {
		return nil, errors.New("when.files must list at least one path")
	}
	if len(files) > taskWhenFilesMax {
		return nil, fmt.Errorf("when.files has more than %d paths", taskWhenFilesMax)
	}
	out := make([]string, 0, len(files))
	for _, file := range files {
		if file == "" || len(file) > taskWhenFileMaxLength || strings.HasPrefix(file, "/") || strings.HasSuffix(file, "/") {
			return nil, fmt.Errorf("when.files path %q must be a project-relative path of at most %d characters", file, taskWhenFileMaxLength)
		}
		for _, segment := range strings.Split(file, "/") {
			if segment == "." || segment == ".." || !taskWhenSegmentPattern.MatchString(segment) {
				return nil, fmt.Errorf("when.files path %q must be a literal path (no globs, . or .. segments)", file)
			}
		}
		out = append(out, file)
	}
	return out, nil
}

// taskSpecFingerprint hashes the canonical entry.
func taskSpecFingerprint(task TaskContribution) string {
	copy := task
	copy.SpecFingerprint = ""
	canonical, _ := json.Marshal(copy)
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

// staticTaskKey and detectedTaskKey build the taskKey the frontend names a
// task by; parseTaskKey splits one.
func staticTaskKey(id string) string { return taskKeyStaticPrefix + id }

func detectedTaskKey(providerID, taskID string) string {
	return taskKeyDetected + providerID + ":" + taskID
}

func parseTaskKey(key string) (source, providerID, taskID string, err error) {
	if id, ok := strings.CutPrefix(key, taskKeyStaticPrefix); ok && taskIDPattern.MatchString(id) {
		return taskKindStatic, "", id, nil
	}
	if rest, ok := strings.CutPrefix(key, taskKeyDetected); ok {
		provider, id, found := strings.Cut(rest, ":")
		if found && taskIDPattern.MatchString(provider) && taskDetectedIDPattern.MatchString(id) {
			return "detected", provider, id, nil
		}
	}
	return "", "", "", processLaunchError(processLaunchErrNotFound, "taskKey %q is invalid", key)
}
