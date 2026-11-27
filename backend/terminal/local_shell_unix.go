//go:build !windows

package terminal

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/creack/pty"

	"mincode/app/backend/agentcontrol"
	"mincode/app/backend/core"
	"mincode/app/backend/proctree"
)

// localPTYHandle 为跨平台 PTY 抽象，unix 用 creack/pty，windows 用 conpty。
type localPTYHandle interface {
	io.ReadWriteCloser
	Resize(cols, rows uint16) error
	Pid() int
}

// unixPTY 用 creack/pty 包装 os/exec.Cmd，实现 localPTYHandle。
type unixPTY struct {
	controlToken string
	f            *os.File
	cmd          *exec.Cmd
	// waited 在后台 cmd.Wait 回收子进程后关闭，避免遗留僵尸进程。
	waited chan struct{}
	// tty is the PTY's terminal device (proctree.TTYOf): processes still attached
	// to it belong to this session even after leaving the shell's process tree.
	tty            int64
	resumeEligible bool
}

// sessionEndGrace is how long the processes of a closed terminal get to exit
// after the hangup before they are killed.
const sessionEndGrace = 1500 * time.Millisecond

// newUnixPTY 在子进程启动后立即挂上唯一的 Wait 回收协程；Pid() 在回收后仍可读取，
// 退出检测仍依赖 PTY 读端 EOF/EIO，不受影响。
func newUnixPTY(f *os.File, cmd *exec.Cmd) *unixPTY {
	u := &unixPTY{f: f, cmd: cmd, waited: make(chan struct{}), tty: proctree.NoTTY}
	// pty.Start hands the terminal to the child as its stdin.
	if tty, ok := cmd.Stdin.(*os.File); ok {
		u.tty = proctree.TTYOf(tty.Name())
	}
	go func() {
		defer close(u.waited)
		_ = cmd.Wait()
	}()
	return u
}

func (u *unixPTY) paneToken() string { return u.controlToken }

func (u *unixPTY) Read(p []byte) (int, error)  { return u.f.Read(p) }
func (u *unixPTY) Write(p []byte) (int, error) { return u.f.Write(p) }

// Close ends the session with everything that runs in it. Killing only the
// shell (and dropping the PTY) leaves background jobs, nohup jobs and programs
// that ignore the hangup running, and once the shell is gone they can no
// longer be told from other processes: the session's processes are listed
// first, then hung up and, after sessionEndGrace, killed (in the background).
func (u *unixPTY) Close() error {
	members := u.processes()
	_ = u.f.Close()
	if u.cmd != nil && u.cmd.Process != nil {
		_ = u.cmd.Process.Kill()
	}
	if len(members) > 0 {
		sessionEnds.Add(1)
		go func() {
			defer sessionEnds.Done()
			proctree.Terminate(members, sessionEndGrace)
		}()
	}
	return nil
}

// processes lists the processes of this session, nil when unknown.
func (u *unixPTY) processes() []proctree.Process {
	if !proctree.Supported() {
		return nil
	}
	procs, err := proctree.List()
	if err != nil {
		return nil
	}
	return u.processesIn(procs)
}

// processesIn returns the processes of this session from a listing.
func (u *unixPTY) processesIn(procs []proctree.Process) []proctree.Process {
	if u.cmd == nil || u.cmd.Process == nil {
		return nil
	}
	return proctree.Members(procs, u.cmd.Process.Pid, u.tty)
}
func (u *unixPTY) Resize(cols, rows uint16) error {
	return pty.Setsize(u.f, &pty.Winsize{Rows: rows, Cols: cols})
}
func (u *unixPTY) Pid() int {
	if u.cmd == nil || u.cmd.Process == nil {
		return 0
	}
	return u.cmd.Process.Pid
}

// resolvedSystemDefaultShell 对齐 Rust 侧 resolved_system_default_shell（非 Windows）：
// 优先 $SHELL，其次 /bin/zsh、/bin/bash、/bin/sh。
func resolvedSystemDefaultShell() string {
	if s := strings.TrimSpace(os.Getenv("SHELL")); s != "" {
		if fi, err := os.Stat(s); err == nil && !fi.IsDir() {
			return s
		}
	}
	for _, candidate := range []string{"/bin/zsh", "/bin/bash", "/bin/sh"} {
		if fi, err := os.Stat(candidate); err == nil && !fi.IsDir() {
			return candidate
		}
	}
	return "/bin/sh"
}

// resolveShellCommand 依据 shellType/customPath 决定要执行的 shell 可执行文件名，
// 对齐 Rust 侧 start_local_shell 中的匹配逻辑（非 Windows 分支）。
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
		return "powershell"
	case "pwsh":
		return "pwsh"
	case "bash":
		return "bash"
	case "zsh":
		return "zsh"
	case "fish":
		return "fish"
	case "wsl":
		return "wsl"
	default:
		return "bash"
	}
}

// startLocalPTY 启动本地 PTY 子进程（非 Windows）。对齐 Rust start_local_shell 的
// Unix 分支：TERM/COLORTERM、登录 shell PATH、UTF-8 locale 兜底、cwd 默认主目录。
func startLocalPTY(payload StartLocalShellPayload, cols, rows uint16) (localPTYHandle, string, error) {
	requestedShellCmd := resolveShellCommand(payload.ShellType, payload.CustomPath)
	shellCmd := requestedShellCmd
	loginPath := core.LoginShellPath()
	fallbackFrom := ""
	if payload.CustomPath == nil || strings.TrimSpace(*payload.CustomPath) == "" {
		if executable := findExecutable(requestedShellCmd, loginPath); executable != "" {
			shellCmd = executable
		} else {
			fallbackFrom = requestedShellCmd
			shellCmd = resolvedSystemDefaultShell()
			fmt.Printf("[app] local shell %q unavailable; falling back to %q\n", fallbackFrom, shellCmd)
		}
	}

	cmd := exec.Command(shellCmd, payload.InitialArgs...)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor")
	if loginPath != "" {
		cmd.Env = append(cmd.Env, "PATH="+loginPath)
	}
	if lang := utf8LangFallback(); lang != "" {
		cmd.Env = append(cmd.Env, "LANG="+lang)
	}
	for k, v := range payload.ExtraEnv {
		if strings.TrimSpace(k) != "" {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}

	var token string
	cmd.Env, token = paneEnvironment(cmd.Env, payload.SessionID, controlCLIDir, true)

	lower := strings.ToLower(shellCmd)
	if strings.Contains(lower, "powershell") || strings.Contains(lower, "pwsh") {
		cmd.Args = append(cmd.Args, "-NoLogo", "-NoProfile")
	}

	if dir := strings.TrimSpace(derefStr(payload.Cwd)); dir != "" {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			cmd.Dir = dir
		}
	}
	if cmd.Dir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			if fi, err := os.Stat(home); err == nil && fi.IsDir() {
				cmd.Dir = home
			}
		}
	}

	cmd.SysProcAttr = &syscall.SysProcAttr{}
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: rows, Cols: cols})
	if err != nil {
		agentcontrol.RevokePaneToken(payload.SessionID, token)
		return nil, "", err
	}
	handle := newUnixPTY(f, cmd)
	handle.controlToken = token
	handle.resumeEligible = payload.desktopPane
	return handle, fallbackFrom, nil
}

func findExecutable(command, searchPath string) string {
	if strings.ContainsRune(command, filepath.Separator) {
		if fi, err := os.Stat(command); err == nil && !fi.IsDir() && fi.Mode().Perm()&0o111 != 0 {
			return command
		}
		return ""
	}
	for _, dir := range filepath.SplitList(searchPath) {
		candidate := filepath.Join(dir, command)
		if fi, err := os.Stat(candidate); err == nil && !fi.IsDir() && fi.Mode().Perm()&0o111 != 0 {
			return candidate
		}
	}
	if executable, err := exec.LookPath(command); err == nil {
		return executable
	}
	return ""
}

// utf8LangFallback returns a UTF-8 LANG value when the inherited locale is
// not UTF-8, or "" when no override is needed.
func utf8LangFallback() string {
	if hasUTF8Locale() {
		return ""
	}
	base := "zh_CN"
	if lang := strings.TrimSpace(os.Getenv("LANG")); lang != "" {
		if b, _, ok := strings.Cut(lang, "."); ok && b != "" && b != "C" && b != "POSIX" {
			base = b
		} else if lang != "C" && lang != "POSIX" {
			base = lang
		}
	}
	return base + ".UTF-8"
}

func hasUTF8Locale() bool {
	for _, k := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		v := strings.ToUpper(strings.ReplaceAll(os.Getenv(k), "-", ""))
		if strings.Contains(v, "UTF8") {
			return true
		}
	}
	return false
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// cwdFromPid 对齐 Rust 侧 cwd_from_pid：macOS 经 libproc、Linux 读 /proc/<pid>/cwd；
// 原生读取失败时回退用 lsof 子进程查询（-Fn 输出以 "n" 开头一行即路径）。
func cwdFromPid(pid int) (string, error) {
	if cwd, err := nativeProcessCwd(pid); err == nil && cwd != "" {
		return cwd, nil
	}
	cmd, cancel := core.CommandWithTimeout(5*time.Second, "lsof", "-a", "-p", strconv.Itoa(pid), "-d", "cwd", "-Fn")
	defer cancel()
	out, err := cmd.Output()
	if err != nil {
		return "", errors.New("无法读取该 shell 的当前目录（进程可能已退出）")
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n") && len(line) > 1 {
			return line[1:], nil
		}
	}
	return "", errors.New("无法读取该 shell 的当前目录（进程可能已退出）")
}
