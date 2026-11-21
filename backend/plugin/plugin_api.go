// Plugin API 域：通用插件后端原语。
// 对齐 Tauri src-tauri/src/plugin_api.rs。
//
// 命令契约（入参 camelCase）：
//
//	plugin_exec {cmd, pluginId, cwd?, timeoutSecs?}
//	    -> {stdout, stderr, exit_code, success}   (返回 snake_case exit_code)
//	plugin_kv_get    {pluginId, key}        -> string | null
//	plugin_kv_set    {pluginId, key, value} -> void
//	plugin_kv_delete {pluginId, key}        -> void
//	plugin_kv_list   {pluginId}             -> [string]
//
// KV 持久化：共享 SQLite plugin_kv 表。
package plugin

import (
	"context"
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
	"mincode/app/plugins/api"
)

func init() {
	core.Register("plugin_exec", pluginExecHandler)
	core.RegisterCtx("workspace_exec", workspaceExecHandler)
	core.Register("plugin_kv_get", pluginKvGetHandler)
	core.Register("plugin_kv_set", pluginKvSetHandler)
	core.Register("plugin_kv_delete", pluginKvDeleteHandler)
	core.Register("plugin_kv_list", pluginKvListHandler)
}

const pluginExecMaxBytes = 4 * 1024 * 1024

// ---- plugin_exec ----

type pluginExecArgs struct {
	Cmd         string  `json:"cmd"`
	PluginID    *string `json:"pluginId"`
	Cwd         *string `json:"cwd"`
	TimeoutSecs *uint64 `json:"timeoutSecs"`
}

type execOutput struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	Success  bool   `json:"success"`
}

func pluginExecHandler(_, argsJson string) (string, error) {
	var a pluginExecArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if a.PluginID == nil || strings.TrimSpace(*a.PluginID) == "" {
		return "", errors.New("plugin_exec 必须提供 plugin_id")
	}
	pluginID := strings.TrimSpace(*a.PluginID)
	if !host().HasCapability(pluginID, api.CapabilityShell) {
		return "", fmt.Errorf("plugin %q is not enabled, running, or granted shell capability", pluginID)
	}
	return executeCommandArgs(context.Background(), a)
}

// workspaceExecHandler honours the caller's context: stopping an AI run or closing the window
// cancels the IPC call, which kills the process tree instead of waiting for the timeout.
func workspaceExecHandler(ctx context.Context, _, argsJson string) (string, error) {
	var a pluginExecArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	return executeCommandArgs(ctx, a)
}

func executeCommandArgs(ctx context.Context, a pluginExecArgs) (string, error) {
	cmdStr := strings.TrimSpace(a.Cmd)
	if cmdStr == "" {
		return "", errors.New("拒绝执行空命令")
	}

	secs := uint64(30)
	if a.TimeoutSecs != nil {
		secs = *a.TimeoutSecs
	}
	if secs < 1 {
		secs = 1
	} else if secs > 300 {
		secs = 300
	}

	var cwd string
	if a.Cwd != nil && strings.TrimSpace(*a.Cwd) != "" {
		validated, err := validateExecCwd(strings.TrimSpace(*a.Cwd))
		if err != nil {
			return "", err
		}
		cwd = validated
	}

	out, err := runShellCommandContext(ctx, cmdStr, cwd, time.Duration(secs)*time.Second)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(out)
}

func runShellCommandContext(parent context.Context, cmdStr, cwd string, timeout time.Duration) (execOutput, error) {
	return runShellCommandEnvironment(parent, cmdStr, cwd, timeout, nil)
}

func runShellCommandEnvironment(parent context.Context, cmdStr, cwd string, timeout time.Duration, environment []string) (execOutput, error) {
	return runShellCommandEnvironmentLimit(parent, cmdStr, cwd, timeout, environment, pluginExecMaxBytes)
}

func runShellCommandEnvironmentLimit(parent context.Context, cmdStr, cwd string, timeout time.Duration, environment []string, streamLimit int) (execOutput, error) {
	var c *exec.Cmd
	if runtime.GOOS == "windows" {
		c = exec.Command("cmd", "/C", cmdStr)
	} else {
		c = exec.Command("sh", "-c", cmdStr)
	}
	c.Env = environment
	core.PrepareProcessTree(c)
	if cwd != "" {
		c.Dir = cwd
	}
	stdoutPipe, err := c.StdoutPipe()
	if err != nil {
		return execOutput{}, err
	}
	stderrPipe, err := c.StderrPipe()
	if err != nil {
		return execOutput{}, err
	}
	if err := c.Start(); err != nil {
		return execOutput{}, err
	}

	var so, se []byte
	var soTrunc, seTrunc bool
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); so, soTrunc = core.ReadCapped(stdoutPipe, streamLimit) }()
	go func() { defer wg.Done(); se, seTrunc = core.ReadCapped(stderrPipe, streamLimit) }()

	done := make(chan error, 1)
	go func() {
		waitErr := c.Wait()
		wg.Wait()
		done <- waitErr
	}()

	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	select {
	case <-ctx.Done():
		_ = core.KillProcessTree(c)
		<-done
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return execOutput{}, fmt.Errorf("plugin_exec 超时（%ds）: %s", int(timeout.Seconds()), sanitizeForLog(cmdStr))
		}
		return execOutput{}, fmt.Errorf("plugin_exec 已取消: %w", ctx.Err())
	case waitErr := <-done:
		exitCode := -1
		success := false
		if waitErr == nil {
			exitCode = 0
			success = true
		} else if ee, ok := waitErr.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		}
		if soTrunc {
			so = append(so, []byte("\n[app] stdout truncated\n")...)
		}
		if seTrunc {
			se = append(se, []byte("\n[app] stderr truncated\n")...)
		}
		return execOutput{
			Stdout:   string(so),
			Stderr:   string(se),
			ExitCode: exitCode,
			Success:  success,
		}, nil
	}
}

func sanitizeForLog(s string) string {
	return strings.NewReplacer("\n", "\\n", "\r", "\\r").Replace(s)
}

func validateExecCwd(cwd string) (string, error) {
	canon, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("plugin_exec cwd 无法解析: %w", err)
	}
	info, err := os.Stat(canon)
	if err != nil || !info.IsDir() {
		return "", errors.New("plugin_exec cwd 不是目录")
	}
	if home, herr := os.UserHomeDir(); herr == nil {
		sensitive := []string{
			filepath.Join(home, ".ssh"),
			filepath.Join(home, ".aws"),
			filepath.Join(home, ".gnupg"),
			filepath.Join(home, ".config", "gh"),
			filepath.Join(home, ".docker"),
			filepath.Join(home, ".kube"),
		}
		for _, root := range sensitive {
			if canon == root || strings.HasPrefix(canon, root+string(filepath.Separator)) {
				return "", fmt.Errorf("plugin_exec cwd 拒绝指向敏感目录: %s", root)
			}
		}
	}
	return canon, nil
}

// ---- plugin_kv ----

var userDataDir = core.UserDataDir

// pluginDataDirectory 保持插件安装资源的既有生产路径不变。
const pluginDataDirectory = "com.mincode.desktop"

const (
	pluginKVMaxKeyBytes = 256
	// pluginKVMaxValueBytes bounds one stored value. A process plugin moves
	// values above about 1 MiB only after negotiating features.largeStorage
	// (runtime_features.go); larger data belongs in plugin storage files.
	pluginKVMaxValueBytes = 4 * 1024 * 1024
	pluginKVMaxKeys       = 4096
)

func validatePluginID(id string) (string, error) {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return "", errors.New("plugin_kv 必须提供非空 plugin_id")
	}
	if strings.Contains(trimmed, ":") {
		return "", errors.New("plugin_kv: plugin_id 含非法字符")
	}
	for _, r := range trimmed {
		if r < 0x20 || r == 0x7f {
			return "", errors.New("plugin_kv: plugin_id 含非法字符")
		}
	}
	return trimmed, nil
}

func validatePluginKey(key string) (string, error) {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return "", errors.New("plugin_kv 必须提供非空 key")
	}
	if len(trimmed) > pluginKVMaxKeyBytes {
		return "", fmt.Errorf("plugin_kv key 超过 %d 字节", pluginKVMaxKeyBytes)
	}
	for _, r := range trimmed {
		if r < 0x20 || r == 0x7f || r == ':' {
			return "", errors.New("plugin_kv key 含非法字符")
		}
	}
	return trimmed, nil
}

func pluginKVGetRaw(pluginID, key string) (json.RawMessage, bool, error) {
	id, err := validatePluginID(pluginID)
	if err != nil {
		return nil, false, err
	}
	validatedKey, err := validatePluginKey(key)
	if err != nil {
		return nil, false, err
	}
	return pluginKVGetRepository(id, validatedKey)
}

func pluginKVSetRaw(pluginID, key string, value json.RawMessage) error {
	id, err := validatePluginID(pluginID)
	if err != nil {
		return err
	}
	validatedKey, err := validatePluginKey(key)
	if err != nil {
		return err
	}
	if len(value) == 0 || !json.Valid(value) {
		return errors.New("plugin_kv value 必须是合法 JSON")
	}
	if len(value) > pluginKVMaxValueBytes {
		return fmt.Errorf("plugin_kv value 超过 %d 字节", pluginKVMaxValueBytes)
	}
	if validatedKey == customAiPromptsKey {
		err = replaceCustomAiPromptsRaw(id, value, false)
	} else {
		err = pluginKVSetRepository(id, validatedKey, value)
	}
	if err == nil && validatedKey == customAiPromptsKey {
		notifyCustomAiPrompts(id)
	}
	return err
}

func pluginKVDeleteRaw(pluginID, key string) error {
	id, err := validatePluginID(pluginID)
	if err != nil {
		return err
	}
	validatedKey, err := validatePluginKey(key)
	if err != nil {
		return err
	}
	if validatedKey == customAiPromptsKey {
		err = replaceCustomAiPromptsRaw(id, nil, true)
	} else {
		err = pluginKVDeleteRepository(id, validatedKey)
	}
	if err == nil && validatedKey == customAiPromptsKey {
		notifyCustomAiPrompts(id)
	}
	return err
}

func pluginKVDeleteNamespace(pluginID string) error {
	id, err := validatePluginID(pluginID)
	if err != nil {
		return err
	}
	lock := customAiPromptLock(id)
	lock.Lock()
	defer lock.Unlock()
	return pluginKVDeleteNamespaceRepository(id)
}

func pluginKVListRaw(pluginID string) ([]string, error) {
	id, err := validatePluginID(pluginID)
	if err != nil {
		return nil, err
	}
	return pluginKVListRepository(id)
}

type kvGetArgs struct {
	PluginID string `json:"pluginId"`
	Key      string `json:"key"`
}

func pluginKvGetHandler(_, argsJson string) (string, error) {
	var args kvGetArgs
	if err := core.DecodeArgs(argsJson, &args); err != nil {
		return "", err
	}
	raw, found, err := pluginKVGetRaw(args.PluginID, args.Key)
	if err != nil {
		return "", err
	}
	if !found {
		return core.EncodeResult(nil)
	}
	// 保持现有前端契约：返回存储值的 JSON 字符串表示。
	return core.EncodeResult(string(raw))
}

type kvSetArgs struct {
	PluginID string `json:"pluginId"`
	Key      string `json:"key"`
	Value    string `json:"value"`
}

func pluginKvSetHandler(_, argsJson string) (string, error) {
	var args kvSetArgs
	if err := core.DecodeArgs(argsJson, &args); err != nil {
		return "", err
	}
	// value 若为合法 JSON 则原样存，否则作为 JSON 字符串存。
	value := json.RawMessage(args.Value)
	if !json.Valid(value) {
		encoded, err := json.Marshal(args.Value)
		if err != nil {
			return "", err
		}
		value = encoded
	}
	if err := pluginKVSetRaw(args.PluginID, args.Key, value); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

func pluginKvDeleteHandler(_, argsJson string) (string, error) {
	var args kvGetArgs
	if err := core.DecodeArgs(argsJson, &args); err != nil {
		return "", err
	}
	if err := pluginKVDeleteRaw(args.PluginID, args.Key); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

type kvListArgs struct {
	PluginID string `json:"pluginId"`
}

func pluginKvListHandler(_, argsJson string) (string, error) {
	var args kvListArgs
	if err := core.DecodeArgs(argsJson, &args); err != nil {
		return "", err
	}
	keys, err := pluginKVListRaw(args.PluginID)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(keys)
}
