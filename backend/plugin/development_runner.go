package plugin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mincode/app/backend/core"
)

const pluginDevRunnerChangedEvent = "plugin:development-runner-changed"

type pluginDevRunnerState struct {
	ProjectRoot string `json:"projectRoot"`
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
}

type pluginDevRunner struct {
	mu      sync.Mutex
	entries map[string]*pluginDevRunnerEntry
}

type pluginDevRunnerEntry struct {
	state  pluginDevRunnerState
	cmd    *exec.Cmd
	done   chan struct{}
	output *pluginDevRunnerOutput
}

// 仅保留有界输出并映射固定错误，避免将插件日志中的凭据暴露到界面。
type pluginDevRunnerOutput struct {
	mu   sync.Mutex
	tail string
}

func (output *pluginDevRunnerOutput) Write(data []byte) (int, error) {
	n := len(data)
	output.mu.Lock()
	defer output.mu.Unlock()
	if len(data) > 8192 {
		data = data[len(data)-8192:]
	}
	output.tail += string(data)
	if len(output.tail) > 8192 {
		output.tail = output.tail[len(output.tail)-8192:]
	}
	return n, nil
}

func (output *pluginDevRunnerOutput) reason() string {
	output.mu.Lock()
	defer output.mu.Unlock()
	for _, message := range []string{
		"MinCode development bridge is unavailable or incompatible",
		"MinCode development bridge not found",
		"开发配对已过期",
		"检查初始运行包",
		"发布初始 runtime generation",
	} {
		if strings.Contains(output.tail, message) {
			return message
		}
	}
	return ""
}

var developmentRunner = &pluginDevRunner{entries: map[string]*pluginDevRunnerEntry{}}

func init() {
	core.Register("plugin_host_development_runner_list", pluginDevRunnerListHandler)
	core.Register("plugin_host_development_runner_start", pluginDevRunnerStartHandler)
	core.Register("plugin_host_development_runner_stop", pluginDevRunnerStopHandler)
}

func pluginDevRunnerListHandler(_, _ string) (string, error) {
	return core.EncodeResult(developmentRunner.list())
}

func pluginDevRunnerStartHandler(_, argsJSON string) (string, error) {
	var args struct {
		ProjectRoot string `json:"projectRoot"`
	}
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	state, err := developmentRunner.start(args.ProjectRoot)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(state)
}

func pluginDevRunnerStopHandler(_, argsJSON string) (string, error) {
	var args struct {
		ProjectRoot string `json:"projectRoot"`
	}
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if err := developmentRunner.stop(args.ProjectRoot); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

func (runner *pluginDevRunner) list() []pluginDevRunnerState {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	states := make([]pluginDevRunnerState, 0, len(runner.entries))
	for _, entry := range runner.entries {
		states = append(states, entry.state)
	}
	return states
}

func (runner *pluginDevRunner) start(rawRoot string) (pluginDevRunnerState, error) {
	projectRoot, err := canonicalPluginDevRoot(rawRoot)
	if err != nil {
		return pluginDevRunnerState{}, fmt.Errorf("start plugin development: %w", err)
	}
	cliArgs := []string{"plugin", "dev", projectRoot}
	// 源码工程由 DevKit 按可选的 mincode.config.json 或约定的 dist-plugin/ 定位运行包，未构建时先构建。
	if _, err := readPluginDevDescriptor(projectRoot, filepath.Join(projectRoot, "plugin.template.json")); err != nil {
		if _, err := readPluginDevDescriptor(projectRoot, filepath.Join(projectRoot, "plugin.json")); err != nil {
			return pluginDevRunnerState{}, errors.New("plugin project requires plugin.template.json (source) or plugin.json (package)")
		}
		// 运行包项目：项目根即运行包，DevKit 监测其中的运行包文件并在变化时热重载。
		cliArgs = append(cliArgs, "--package", ".")
	}
	if err := host().devBridge.Start(); err != nil {
		return pluginDevRunnerState{}, fmt.Errorf("refresh plugin development discovery: %w", err)
	}
	cli, err := resolveMincodeCLI()
	if err != nil {
		return pluginDevRunnerState{}, err
	}

	runner.mu.Lock()
	if existing := runner.entries[projectRoot]; existing != nil && (existing.state.Status == "starting" || existing.state.Status == "running") {
		state := existing.state
		runner.mu.Unlock()
		return state, nil
	}
	cmd := exec.Command(cli, cliArgs...)
	cmd.Dir = projectRoot
	cmd.Env = os.Environ()
	core.PrepareProcessTree(cmd)
	output := &pluginDevRunnerOutput{}
	cmd.Stdout = output
	cmd.Stderr = output
	entry := &pluginDevRunnerEntry{state: pluginDevRunnerState{ProjectRoot: projectRoot, Status: "starting"}, cmd: cmd, done: make(chan struct{}), output: output}
	runner.entries[projectRoot] = entry
	runner.mu.Unlock()
	core.Emit(pluginDevRunnerChangedEvent, entry.state)

	if err := cmd.Start(); err != nil {
		runner.finish(projectRoot, entry, fmt.Errorf("start MinCode plugin DevKit: %w", err))
		return pluginDevRunnerState{}, err
	}
	runner.mu.Lock()
	entry.state.Status = "running"
	state := entry.state
	runner.mu.Unlock()
	core.Emit(pluginDevRunnerChangedEvent, state)
	go func() { runner.finish(projectRoot, entry, cmd.Wait()) }()
	return state, nil
}

func (runner *pluginDevRunner) finish(projectRoot string, entry *pluginDevRunnerEntry, waitErr error) {
	runner.mu.Lock()
	current := runner.entries[projectRoot]
	if current != entry {
		runner.mu.Unlock()
		return
	}
	entry.state.Status = "stopped"
	if waitErr != nil && !strings.Contains(strings.ToLower(waitErr.Error()), "signal: killed") {
		entry.state.Status = "error"
		entry.state.Error = waitErr.Error()
		if entry.output != nil {
			if reason := entry.output.reason(); reason != "" {
				entry.state.Error += ": " + reason
			}
		}
	}
	state := entry.state
	close(entry.done)
	runner.mu.Unlock()
	core.Emit(pluginDevRunnerChangedEvent, state)
}

func (runner *pluginDevRunner) stop(rawRoot string) error {
	projectRoot, err := canonicalPluginDevRoot(rawRoot)
	if err != nil {
		return err
	}
	runner.mu.Lock()
	entry := runner.entries[projectRoot]
	if entry == nil || entry.cmd == nil || entry.state.Status == "stopped" || entry.state.Status == "error" {
		runner.mu.Unlock()
		return nil
	}
	entry.state.Status = "stopping"
	state := entry.state
	runner.mu.Unlock()
	core.Emit(pluginDevRunnerChangedEvent, state)
	if err := core.KillProcessTree(entry.cmd); err != nil {
		return fmt.Errorf("stop plugin development process: %w", err)
	}
	select {
	case <-entry.done:
	case <-time.After(5 * time.Second):
		return errors.New("timed out stopping plugin development process")
	}
	return nil
}

func shutdownPluginDevelopmentRunner(ctx context.Context) {
	for _, state := range developmentRunner.list() {
		if state.Status == "running" || state.Status == "starting" || state.Status == "stopping" {
			done := make(chan struct{})
			go func(root string) { _ = developmentRunner.stop(root); close(done) }(state.ProjectRoot)
			select {
			case <-done:
			case <-ctx.Done():
				return
			}
		}
	}
}
