package acp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"mincode/app/backend/core"

	acpsdk "github.com/coder/acp-go-sdk"
)

const (
	acpClientName    = "MinCode"
	acpClientVersion = "1.0.0"
)

// launchAgent 启动 Agent 子进程（stdio JSON-RPC）并完成协议初始化握手。
// 失败时会尽力杀掉已启动的子进程，避免泄漏。
// （插件智能体用它确认程序文件仍是用户确认过的那一份）。
func launchAgent(ctx context.Context, serverID, command string, args []string, env map[string]string, cwd string) (conn *acpConnection, err error) {
	processEnv := buildAgentProcessEnv(env)
	resolvedCommand := resolveAgentExecutable(command, processEnv)
	cmd := exec.Command(resolvedCommand, args...)
	cmd.Env = processEnv
	core.PrepareProcessTree(cmd)
	if cwd != "" {
		cmd.Dir = cwd
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr := core.NewCappedWriter(64 * 1024)
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动 ACP Agent %q 失败: %w", command, err)
	}
	defer func() {
		if err != nil && cmd.Process != nil {
			_ = core.KillProcessTree(cmd)
			_ = cmd.Wait()
		}
	}()

	connCtx, cancel := context.WithCancel(ctx)

	client := newBridgeClient(serverID)
	csc := acpsdk.NewClientSideConnection(client, stdin, stdout)

	initRes, err := csc.Initialize(connCtx, acpsdk.InitializeRequest{
		ProtocolVersion: acpsdk.ProtocolVersionNumber,
		ClientCapabilities: acpsdk.ClientCapabilities{
			Fs: acpsdk.FileSystemCapabilities{
				ReadTextFile:  true,
				WriteTextFile: true,
			},
		},
		ClientInfo: &acpsdk.Implementation{
			Name:    acpClientName,
			Version: acpClientVersion,
		},
	})
	if err != nil {
		cancel()
		return nil, acpLaunchError(command, err, stderr)
	}

	return &acpConnection{
		cmd:               cmd,
		csc:               csc,
		client:            client,
		cancel:            cancel,
		command:           command,
		serverID:          serverID,
		stderr:            stderr,
		agentCapabilities: initRes.AgentCapabilities,
		authMethods:       initRes.AuthMethods,
		protocolVersion:   int(initRes.ProtocolVersion),
	}, nil
}

func acpLaunchError(command string, cause error, stderr *core.CappedWriter) error {
	if stderr != nil {
		data, truncated := stderr.Bytes()
		if detail := strings.TrimSpace(string(data)); detail != "" {
			if truncated {
				detail += "\n[stderr truncated]"
			}
			return fmt.Errorf("ACP Agent %q 初始化失败: %w; stderr: %s", command, cause, detail)
		}
	}
	return fmt.Errorf("ACP Agent %q 初始化失败: %w", command, cause)
}

func buildAgentProcessEnv(env map[string]string) []string {
	values := map[string]string{}
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	for key, value := range env {
		values[key] = value
	}
	if _, explicit := env["PATH"]; !explicit && runtime.GOOS != "windows" {
		values["PATH"] = mergePathLists(core.LoginShellPath(), values["PATH"], "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin")
	}
	out := make([]string, 0, len(values))
	for key, value := range values {
		out = append(out, key+"="+value)
	}
	return out
}

func resolveAgentExecutable(command string, processEnv []string) string {
	if command == "" || strings.ContainsAny(command, `/\\`) {
		return command
	}
	pathValue := envValue(processEnv, "PATH")
	for _, dir := range filepath.SplitList(pathValue) {
		if dir == "" {
			continue
		}
		for _, name := range executableNames(command) {
			candidate := filepath.Join(dir, name)
			info, err := os.Stat(candidate)
			if err == nil && !info.IsDir() && (runtime.GOOS == "windows" || info.Mode().Perm()&0o111 != 0) {
				return candidate
			}
		}
	}
	return command
}

func executableNames(command string) []string {
	if runtime.GOOS != "windows" || filepath.Ext(command) != "" {
		return []string{command}
	}
	extensions := filepath.SplitList(os.Getenv("PATHEXT"))
	if len(extensions) == 0 {
		extensions = []string{".COM", ".EXE", ".BAT", ".CMD"}
	}
	names := []string{command}
	for _, extension := range extensions {
		names = append(names, command+strings.ToLower(extension), command+strings.ToUpper(extension))
	}
	return names
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for i := len(env) - 1; i >= 0; i-- {
		if strings.HasPrefix(env[i], prefix) {
			return strings.TrimPrefix(env[i], prefix)
		}
	}
	return ""
}

func mergePathLists(paths ...string) string {
	seen := map[string]struct{}{}
	merged := make([]string, 0)
	for _, pathValue := range paths {
		for _, dir := range filepath.SplitList(pathValue) {
			dir = strings.TrimSpace(dir)
			if dir == "" {
				continue
			}
			if _, exists := seen[dir]; exists {
				continue
			}
			seen[dir] = struct{}{}
			merged = append(merged, dir)
		}
	}
	return strings.Join(merged, string(os.PathListSeparator))
}
