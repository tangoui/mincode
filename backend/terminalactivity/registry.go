package terminalactivity

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"mincode/app/backend/core"
)

const (
	hookModeArgument = "--terminal-activity-hook"
	activityEvent    = "terminal-activity"
	registrationTTL  = 24 * time.Hour
)

type registration struct {
	PaneID    string
	CreatedAt time.Time
}

type preparePayload struct {
	PaneID string `json:"paneId"`
}

type unregisterPayload struct {
	PaneID string `json:"paneId"`
}

type PrepareResult struct {
	Enhanced     bool              `json:"enhanced"`
	SettingsPath string            `json:"settingsPath,omitempty"`
	ExtraEnv     map[string]string `json:"extraEnv,omitempty"`
}

type hookInput struct {
	HookEventName    string `json:"hook_event_name"`
	Prompt           string `json:"prompt"`
	ToolName         string `json:"tool_name"`
	NotificationType string `json:"notification_type"`
}

type hookEnvelope struct {
	Token    string    `json:"token"`
	Received time.Time `json:"received"`
	Input    hookInput `json:"input"`
}

type activitySnapshot struct {
	PaneID string `json:"paneId"`
	Phase  string `json:"phase"`
	// Event is the Claude hook that produced the phase (SessionStart, Stop, ...).
	Event     string `json:"event,omitempty"`
	Source    string `json:"source"`
	Summary   string `json:"summary,omitempty"`
	AutoLabel string `json:"autoLabel,omitempty"`
	UpdatedAt int64  `json:"updatedAt"`
}

type registryState struct {
	mu                     sync.Mutex
	initialized            bool
	inboxDir               string
	settingsPath           string
	registrations          map[string]registration
	pollerRunning          bool
	stop                   chan struct{}
	done                   chan struct{}
	claudeSupportsSettings bool
	claudeProbeFailedAt    time.Time
}

var state = registryState{registrations: make(map[string]registration)}

func init() {
	core.Register("terminal_activity_prepare_claude", prepareClaudeHandler)
	core.Register("terminal_activity_unregister", unregisterHandler)
}

func IsHookMode(args []string) bool {
	return len(args) == 1 && args[0] == hookModeArgument
}

func randomToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func activityRoot() (string, error) {
	root := core.UserDataDir()
	if root == "" {
		return "", errors.New("user data directory is unavailable")
	}
	return filepath.Join(root, "com.tangoui.mincode", "terminal-activity"), nil
}

func ensureInitialized() error {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.initialized {
		return nil
	}
	root, err := activityRoot()
	if err != nil {
		return err
	}
	suffix, err := randomToken()
	if err != nil {
		return err
	}
	state.inboxDir = filepath.Join(root, fmt.Sprintf("inbox-%d-%s", os.Getpid(), suffix[:12]))
	if err := os.MkdirAll(state.inboxDir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(state.inboxDir, 0o700); err != nil {
		return err
	}
	state.settingsPath = filepath.Join(state.inboxDir, "claude-hooks.json")
	if err := writeSettings(state.settingsPath); err != nil {
		return err
	}
	state.initialized = true
	return nil
}

// startPollerLocked 在持有 state.mu 时调用，确保收件箱轮询正在运行。
// 轮询仅在存在登记时运行：最后一个登记移除后自行退出，下次登记再拉起。
func startPollerLocked() {
	if state.pollerRunning || !state.initialized {
		return
	}
	state.stop = make(chan struct{})
	state.done = make(chan struct{})
	state.pollerRunning = true
	go consumeInbox(state.inboxDir, state.stop, state.done)
}

var (
	resolveClaudeExecutableForProbe = resolveClaudeExecutable
	runClaudeHelpForProbe           = runClaudeHelp
	// 探测要跑登录 shell 和 claude --help，在 profile 较重的机器上并不便宜。
	// 正向结果永久缓存；失败结果只压制这段时间，保证安装 CLI 后还能恢复增强模式。
	claudeProbeRetryInterval = time.Minute
)

func claudeSupportsSettings() bool {
	state.mu.Lock()
	if state.claudeSupportsSettings {
		state.mu.Unlock()
		return true
	}
	if !state.claudeProbeFailedAt.IsZero() && time.Since(state.claudeProbeFailedAt) < claudeProbeRetryInterval {
		state.mu.Unlock()
		return false
	}
	state.mu.Unlock()

	supported := false
	if path := resolveClaudeExecutableForProbe(); path != "" {
		output, err := runClaudeHelpForProbe(path)
		supported = err == nil && strings.Contains(string(output), "--settings")
	}

	state.mu.Lock()
	if supported {
		state.claudeSupportsSettings = true
		state.claudeProbeFailedAt = time.Time{}
	} else {
		state.claudeProbeFailedAt = time.Now()
	}
	state.mu.Unlock()
	return supported
}

func resolveClaudeExecutable() string {
	if path, err := exec.LookPath("claude"); err == nil {
		return path
	}
	if runtime.GOOS == "windows" {
		return ""
	}
	return findExecutableOnPath("claude", claudeProbePath())
}

func runClaudeHelp(path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--help")
	core.PrepareBackgroundCommand(cmd)
	return cmd.CombinedOutput()
}

func claudeProbePath() string {
	paths := []string{os.Getenv("PATH"), core.LoginShellPath()}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".local", "bin"))
	}
	paths = append(paths, "/opt/homebrew/bin:/opt/homebrew/sbin:/usr/local/bin:/usr/bin:/bin")
	return mergeProbePaths(paths...)
}

func mergeProbePaths(paths ...string) string {
	seen := make(map[string]struct{})
	merged := make([]string, 0)
	for _, pathValue := range paths {
		for _, directory := range filepath.SplitList(pathValue) {
			directory = strings.TrimSpace(directory)
			if directory == "" {
				continue
			}
			if _, exists := seen[directory]; exists {
				continue
			}
			seen[directory] = struct{}{}
			merged = append(merged, directory)
		}
	}
	return strings.Join(merged, string(os.PathListSeparator))
}

func findExecutableOnPath(command, searchPath string) string {
	for _, directory := range filepath.SplitList(searchPath) {
		candidate := filepath.Join(directory, command)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0 {
			return candidate
		}
	}
	return ""
}

func prepareClaudeHandler(_, argsJSON string) (string, error) {
	var payload preparePayload
	if err := core.DecodeArgsPayload(argsJSON, &payload); err != nil {
		return "", err
	}
	paneID := strings.TrimSpace(payload.PaneID)
	if paneID == "" {
		return "", errors.New("paneId is required")
	}
	if !claudeSupportsSettings() {
		return core.EncodeResult(PrepareResult{Enhanced: false})
	}
	if err := ensureInitialized(); err != nil {
		return "", err
	}
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	state.mu.Lock()
	state.registrations[token] = registration{PaneID: paneID, CreatedAt: time.Now()}
	startPollerLocked()
	inboxDir := state.inboxDir
	settingsPath := state.settingsPath
	state.mu.Unlock()
	return core.EncodeResult(PrepareResult{
		Enhanced:     true,
		SettingsPath: settingsPath,
		ExtraEnv: map[string]string{
			"MINCODE_CLI_TAB_TOKEN":           token,
			"MINCODE_TERMINAL_ACTIVITY_INBOX": inboxDir,
		},
	})
}

func unregisterHandler(_, argsJSON string) (string, error) {
	var payload unregisterPayload
	if err := core.DecodeArgsPayload(argsJSON, &payload); err != nil {
		return "", err
	}
	unregisterPane(strings.TrimSpace(payload.PaneID))
	return core.EncodeResult(nil)
}

func unregisterPane(paneID string) {
	state.mu.Lock()
	for token, item := range state.registrations {
		if item.PaneID == paneID {
			delete(state.registrations, token)
		}
	}
	state.mu.Unlock()
}

func sweepExpiredRegistrations(now time.Time) {
	state.mu.Lock()
	for token, item := range state.registrations {
		if now.Sub(item.CreatedAt) > registrationTTL {
			delete(state.registrations, token)
		}
	}
	state.mu.Unlock()
}

func writeSettings(path string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	handler := map[string]any{
		"type":    "command",
		"command": executable,
		"args":    []string{hookModeArgument},
		"timeout": 5,
		"async":   true,
	}
	settings := map[string]any{"hooks": map[string]any{}}
	hooks := settings["hooks"].(map[string]any)
	for _, event := range []string{
		"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse",
		"PostToolUseFailure", "PermissionRequest", "Stop", "StopFailure", "SessionEnd",
	} {
		hooks[event] = []any{map[string]any{"hooks": []any{handler}}}
	}
	hooks["Notification"] = []any{map[string]any{
		"matcher": "permission_prompt|idle_prompt|agent_needs_input|agent_completed",
		"hooks":   []any{handler},
	}}
	data, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}
