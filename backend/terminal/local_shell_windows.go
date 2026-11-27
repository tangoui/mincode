//go:build windows

package terminal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/UserExistsError/conpty"

	"mincode/app/backend/agentcontrol"
)

// localPTYHandle 为跨平台 PTY 抽象，unix 用 creack/pty，windows 用 conpty。
type localPTYHandle interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	Close() error
	Resize(cols, rows uint16) error
	Pid() int
}

// windowsPTY 用 conpty 包装 ConPTY 会话，实现 localPTYHandle。
type windowsPTY struct {
	controlToken string
	cp           *conpty.ConPty
}

func (w *windowsPTY) paneToken() string { return w.controlToken }

func (w *windowsPTY) Read(p []byte) (int, error)  { return w.cp.Read(p) }
func (w *windowsPTY) Write(p []byte) (int, error) { return w.cp.Write(p) }
func (w *windowsPTY) Close() error {
	go func() { _, _ = w.cp.Wait(context.Background()) }()
	return w.cp.Close()
}
func (w *windowsPTY) Resize(cols, rows uint16) error {
	return w.cp.Resize(int(cols), int(rows))
}
func (w *windowsPTY) Pid() int { return w.cp.Pid() }

// resolvedSystemDefaultShell 对齐 Rust 侧 Windows 分支：优先 pwsh，其次 Windows
// PowerShell 5.1，最后 ComSpec（cmd）。
func resolvedSystemDefaultShell() string {
	for _, exe := range []string{"pwsh.exe", "pwsh"} {
		if p := findExecutableOnPath(exe); p != "" {
			return p
		}
	}
	root := strings.TrimSpace(os.Getenv("SystemRoot"))
	if root == "" {
		root = `C:\Windows`
	}
	ps51 := filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	if fi, err := os.Stat(ps51); err == nil && !fi.IsDir() {
		return ps51
	}
	if cs := strings.TrimSpace(os.Getenv("ComSpec")); cs != "" {
		return cs
	}
	return `C:\Windows\System32\cmd.exe`
}

func findExecutableOnPath(exe string) string {
	pathVar := os.Getenv("PATH")
	for _, dir := range filepath.SplitList(pathVar) {
		full := filepath.Join(dir, exe)
		if fi, err := os.Stat(full); err == nil && !fi.IsDir() {
			return full
		}
	}
	return ""
}

// resolveShellCommand 对齐 Rust 侧 start_local_shell 中的匹配逻辑（Windows 分支）。
func resolveShellCommand(shellType, customPath *string) string {
	if customPath != nil && strings.TrimSpace(*customPath) != "" {
		return *customPath
	}
	st := ""
	if shellType != nil {
		st = *shellType
	}
	switch st {
	case "", "system":
		return resolvedSystemDefaultShell()
	case "powershell":
		return "powershell.exe"
	case "pwsh":
		return "pwsh.exe"
	case "bash":
		return "bash"
	case "zsh":
		return "zsh"
	case "fish":
		return "fish"
	case "wsl":
		return "wsl.exe"
	case "cmd":
		return comSpec()
	default:
		return comSpec()
	}
}

func comSpec() string {
	if cs := strings.TrimSpace(os.Getenv("ComSpec")); cs != "" {
		return cs
	}
	return `C:\Windows\System32\cmd.exe`
}

var windowsCmdDangerChars = []rune{'&', '|', '<', '>', '^', '%', '!'}

func validateWindowsCmdFragment(value, field string) error {
	if value == "" {
		return fmt.Errorf("Windows 自定义 Shell 的 %s 不能为空", field)
	}
	for _, ch := range value {
		if ch < 0x20 {
			return fmt.Errorf("Windows 自定义 Shell 的 %s 不能包含控制字符", field)
		}
	}
	if strings.Contains(value, `"`) {
		return fmt.Errorf("Windows 自定义 Shell 的 %s 不能包含双引号", field)
	}
	for _, ch := range value {
		for _, bad := range windowsCmdDangerChars {
			if ch == bad {
				return fmt.Errorf("Windows 自定义 Shell 的 %s 包含高风险命令解释字符，已拒绝执行", field)
			}
		}
	}
	return nil
}

func quoteWindowsCmdArg(value string) string {
	if value == "" || strings.ContainsAny(value, " \t\r\n") {
		return `"` + value + `"`
	}
	return value
}

// startLocalPTY 启动本地 PTY 子进程（Windows）。对齐 Rust start_local_shell 的
// Windows 分支：非 exe/内置 shell 需经 cmd.exe /C 启动并切 UTF-8 代码页。
func startLocalPTY(payload StartLocalShellPayload, cols, rows uint16) (localPTYHandle, string, error) {
	shellCmd := resolveShellCommand(payload.ShellType, payload.CustomPath)
	lower := strings.ToLower(shellCmd)
	isExe := strings.HasSuffix(lower, ".exe") || strings.HasSuffix(lower, ".com")
	isBuiltin := map[string]bool{
		"cmd": true, "cmd.exe": true, "powershell": true, "powershell.exe": true,
		"pwsh": true, "pwsh.exe": true, "bash": true, "zsh": true, "fish": true,
		"wsl": true, "wsl.exe": true,
	}[lower]

	var commandLine string
	if !isExe && !isBuiltin {
		if err := validateWindowsCmdFragment(shellCmd, "customPath"); err != nil {
			return nil, "", err
		}
		parts := []string{quoteWindowsCmdArg(shellCmd)}
		for _, arg := range payload.InitialArgs {
			if err := validateWindowsCmdFragment(arg, "initialArgs"); err != nil {
				return nil, "", err
			}
			parts = append(parts, quoteWindowsCmdArg(arg))
		}
		commandLine = comSpec() + ` /C "chcp 65001 >nul & ` + strings.Join(parts, " ") + `"`
	} else {
		args := []string{shellCmd}
		if strings.Contains(lower, "powershell") || strings.Contains(lower, "pwsh") {
			args = append(args, "-NoLogo", "-NoProfile")
		}
		args = append(args, payload.InitialArgs...)
		commandLine = joinWindowsArgs(args)
	}

	opts := []conpty.ConPtyOption{
		conpty.ConPtyDimensions(int(cols), int(rows)),
	}
	if dir := strings.TrimSpace(derefStr(payload.Cwd)); dir != "" {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			opts = append(opts, conpty.ConPtyWorkDir(dir))
		}
	}
	env := os.Environ()
	env = append(env, "TERM=xterm-256color", "COLORTERM=truecolor")
	for k, v := range payload.ExtraEnv {
		if strings.TrimSpace(k) != "" {
			env = append(env, k+"="+v)
		}
	}
	executable := strings.ToLower(filepath.Base(shellCmd))
	local := executable != "wsl" && executable != "wsl.exe"
	env, token := paneEnvironment(env, payload.SessionID, controlCLIDir, local)
	opts = append(opts, conpty.ConPtyEnv(env))

	cp, err := conpty.Start(commandLine, opts...)
	if err != nil {
		agentcontrol.RevokePaneToken(payload.SessionID, token)
		return nil, "", err
	}
	return &windowsPTY{cp: cp, controlToken: token}, "", nil
}

func joinWindowsArgs(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = quoteWindowsCmdArg(a)
	}
	return strings.Join(quoted, " ")
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// cwdFromPid 在 Windows 上暂不支持读取子进程当前目录（无 Rust sysinfo 等价的
// 简单标准库方案），与前端约定：调用方需容忍此错误并保留原 cwd 展示。
func cwdFromPid(pid int) (string, error) {
	return "", errors.New("Windows 平台暂不支持读取该 shell 的当前目录")
}
