// 杂项/平台信息域。对齐 Tauri src-tauri/src/main.rs 与 terminal/system_info.rs。
//
// 命令契约：
//
//	set_minimize_to_tray {value}              -> void
//	get_autostart_enabled                      -> bool
//	set_autostart          {value}             -> void
//	get_app_version                            -> string
//	get_platform_info                          -> string ("macos"/"windows"/"linux")
//	check_tcp_port       {host, port}          -> bool
//	detect_cli_tools                           -> [{name, version, installed}]
//	list_port_processes  {ports}               -> [{port, pid, process_name, state}]
//	kill_process_by_pid  {pid}                 -> bool
//	create_popup_tab_window {label,tabData,title} -> void（见下方说明）
//	get_popup_tab_data {label?}                -> string | null（见下方说明）
//	focus_popup_tab_window {label}               -> void
//	close_popup_tab_window {label}               -> void
//	capture_screenshot                               -> void（调用系统区域截图并复制到剪贴板）
//
// 关于 popup 标签页独立窗口：对齐 Tauri src-tauri/src/main.rs 的
// create_popup_tab_window / get_popup_tab_data（PopupTabStore = Arc<Mutex<HashMap>>）。
// 宿主创建多窗口，Name 即窗口标识（对齐 Tauri label）。
//   - create：把 tabData 暂存到 label 键；若同名窗口已存在则聚焦，否则新建窗口，
//     URL 为 /?mode=popup-tab&label=<label>（前端据 query 读取自身 label 与数据）。
//   - get：因 core.Dispatch 无窗口上下文，用 app.Window.Current().Name() 识别当前
//     活动窗口（弹窗刚创建并聚焦即为 current），取出并移除其暂存数据。
package misc

import (
	"mincode/app/backend/core"

	"context"
	"fmt"
	"math"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func init() {
	core.Register("set_minimize_to_tray", setMinimizeToTrayHandler)
	core.Register("get_autostart_enabled", getAutostartEnabledHandler)
	core.Register("set_autostart", setAutostartHandler)
	core.Register("get_app_version", getAppVersionHandler)
	core.Register("get_platform_info", getPlatformInfoHandler)
	core.Register("get_runtime_platform", getRuntimePlatformHandler)
	core.Register("get_user_home_dir", getUserHomeDirHandler)
	core.Register("focus_main_window", focusMainWindowHandler)
	core.Register("create_popup_tab_window", createPopupTabWindowHandler)
	core.Register("get_popup_tab_data", getPopupTabDataHandler)
	core.Register("focus_popup_tab_window", focusPopupTabWindowHandler)
	core.Register("close_popup_tab_window", closePopupTabWindowHandler)
	core.Register("capture_screenshot", captureScreenshotHandler)
	core.Register("check_tcp_port", checkTcpPortHandler)
	core.Register("detect_cli_tools", detectCliToolsHandler)
	core.Register("list_port_processes", listPortProcessesHandler)
	core.Register("kill_process_by_pid", killProcessByPidHandler)
}

// ---- screenshot ----

// captureScreenshotHandler runs the platform screenshot tool in interactive
// mode. The tool owns its native selection UI and writes the chosen image to the
// operating system clipboard, so the frontend does not need filesystem access
// or a second image-preview implementation.
func captureScreenshotHandler(_, _ string) (string, error) {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("screencapture", "-i", "-c", "-d")
	case "windows":
		command = exec.Command("SnippingTool.exe", "/clip")
	case "linux":
		if path, err := exec.LookPath("gnome-screenshot"); err == nil {
			command = exec.Command(path, "-a", "-c")
		} else if path, err := exec.LookPath("flameshot"); err == nil {
			command = exec.Command(path, "gui", "-c")
		} else {
			return "", fmt.Errorf("screenshot tool is unavailable")
		}
	default:
		return "", fmt.Errorf("screenshot capture is unsupported on this platform")
	}

	// Keep the IPC request alive until the system picker completes. This lets
	// permission or launch failures reach the frontend instead of being silently
	// dropped by a detached process.
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("run screenshot capture: %w", err)
	}
	return core.EncodeResult(nil)
}

// ---- set_minimize_to_tray ----

var minimizeToTray atomic.Bool

// AppVersion is injected by tagged release builds; local builds retain the development default.
var AppVersion = "0.4.5"

var autostartManager interface {
	Enable() error
	Disable() error
	IsEnabled() (bool, error)
}

func SetAutostartManager(manager interface {
	Enable() error
	Disable() error
	IsEnabled() (bool, error)
}) {
	autostartManager = manager
}

func MinimizeToTrayEnabled() bool {
	return minimizeToTray.Load()
}

type minimizeToTrayArgs struct {
	Value bool `json:"value"`
}

func setMinimizeToTrayHandler(_, argsJson string) (string, error) {
	var a minimizeToTrayArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	minimizeToTray.Store(a.Value)
	return core.EncodeResult(nil)
}

// ---- autostart ----

type autostartArgs struct {
	Value bool `json:"value"`
}

func getAutostartEnabledHandler(_, _ string) (string, error) {
	if autostartManager == nil {
		return "", fmt.Errorf("autostart is unavailable")
	}
	value, err := autostartManager.IsEnabled()
	if err != nil {
		return "", err
	}
	return core.EncodeResult(value)
}

func setAutostartHandler(_, argsJson string) (string, error) {
	if autostartManager == nil {
		return "", fmt.Errorf("autostart is unavailable")
	}
	var args autostartArgs
	if err := core.DecodeArgs(argsJson, &args); err != nil {
		return "", err
	}
	if args.Value {
		if err := autostartManager.Enable(); err != nil {
			return "", err
		}
	} else if err := autostartManager.Disable(); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

// ---- app metadata ----

func getAppVersionHandler(_, _ string) (string, error) {
	return core.EncodeResult(AppVersion)
}

// ---- get_platform_info ----

func getPlatformInfoHandler(_, _ string) (string, error) {
	if runtime.GOOS == "darwin" {
		return core.EncodeResult("macos")
	}
	return core.EncodeResult(runtime.GOOS)
}

func getRuntimePlatformHandler(_, _ string) (string, error) {
	return core.EncodeResult(struct {
		OS   string `json:"os"`
		Arch string `json:"arch"`
	}{OS: runtime.GOOS, Arch: runtime.GOARCH})
}

// ---- get_user_home_dir ----

func getUserHomeDirHandler(_, _ string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("获取用户主目录失败: %w", err)
	}
	return core.EncodeResult(home)
}

// ---- main window ----
//
// 窗口操作经 seam 注入（由 tango 宿主 main.go 设置），
// 使 misc 不依赖宿主框架。nil 表示该能力在当前宿主不可用。
var (
	FocusMainWindow   func() error
	CreatePopupWindow func(label, title, url string) error
	CurrentWindowName func() string
	PopupWindowAction func(label, action string) error
)

func focusMainWindowHandler(_, _ string) (string, error) {
	if FocusMainWindow == nil {
		return "", fmt.Errorf("主窗口不可用")
	}
	if err := FocusMainWindow(); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

// ---- popup 标签页独立窗口 ----
//
// 对齐 Tauri PopupTabStore：以 label 为键暂存序列化后的 tabData（JSON 字符串）。
// 弹窗前端挂载后调用 get_popup_tab_data 取出并移除。

var (
	popupTabStoreMu sync.Mutex
	popupTabStore   = map[string]string{}
)

type createPopupTabWindowArgs struct {
	Label   string `json:"label"`
	TabData string `json:"tabData"`
	Title   string `json:"title"`
}

func createPopupTabWindowHandler(_, argsJson string) (string, error) {
	var a createPopupTabWindowArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	label := strings.TrimSpace(a.Label)
	// 校验 label 前缀，避免窗口标识冲突（对齐 Tauri 的 "popup-" 校验）。
	if !strings.HasPrefix(label, "popup-") {
		return "", fmt.Errorf("无效的 popup 窗口标识: %q", label)
	}

	// 暂存数据（覆盖同 label 旧值），供弹窗挂载后取用。
	popupTabStoreMu.Lock()
	popupTabStore[label] = a.TabData
	popupTabStoreMu.Unlock()

	if CreatePopupWindow == nil {
		return "", fmt.Errorf("应用实例不可用，无法创建 popup 窗口")
	}
	title := strings.TrimSpace(a.Title)
	if title == "" {
		title = "MinCode"
	}
	// 弹窗 URL 携带 label，前端据 query 读取自身标识与暂存数据。
	popupURL := "/?mode=popup-tab&label=" + url.QueryEscape(label)
	if err := CreatePopupWindow(label, title, popupURL); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

type popupTabWindowArgs struct {
	Label string `json:"label"`
}

func getPopupTabDataHandler(_, argsJson string) (string, error) {
	var args popupTabWindowArgs
	if err := core.DecodeArgs(argsJson, &args); err != nil {
		return "", err
	}
	label := strings.TrimSpace(args.Label)
	if label == "" {
		// 兼容旧前端：没有传 label 时仍以当前活动窗口识别。
		if CurrentWindowName == nil {
			return core.EncodeResult(nil)
		}
		label = CurrentWindowName()
	}
	if !strings.HasPrefix(label, "popup-") {
		return core.EncodeResult(nil)
	}

	popupTabStoreMu.Lock()
	data, ok := popupTabStore[label]
	if ok {
		delete(popupTabStore, label)
	}
	popupTabStoreMu.Unlock()

	if !ok {
		return core.EncodeResult(nil)
	}
	// data 已是 JSON 字符串，作为字符串结果返回（对齐 Tauri get_popup_tab_data）。
	return core.EncodeResult(data)
}

func focusPopupTabWindowHandler(_, argsJson string) (string, error) {
	var args popupTabWindowArgs
	if err := core.DecodeArgs(argsJson, &args); err != nil {
		return "", err
	}
	if PopupWindowAction == nil {
		return "", fmt.Errorf("popup 窗口不可用")
	}
	if err := PopupWindowAction(args.Label, "focus"); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

func closePopupTabWindowHandler(_, argsJson string) (string, error) {
	var args popupTabWindowArgs
	if err := core.DecodeArgs(argsJson, &args); err != nil {
		return "", err
	}
	if PopupWindowAction == nil {
		return "", fmt.Errorf("popup 窗口不可用")
	}
	if err := PopupWindowAction(args.Label, "close"); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

// ---- check_tcp_port ----

type checkTcpPortArgs struct {
	Host string `json:"host"`
	Port uint16 `json:"port"`
}

func checkTcpPortHandler(_, argsJson string) (string, error) {
	var a checkTcpPortArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	addr := net.JoinHostPort(strings.TrimSpace(a.Host), strconv.Itoa(int(a.Port)))
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return core.EncodeResult(false)
	}
	_ = conn.Close()
	return core.EncodeResult(true)
}

// ---- detect_cli_tools ----

type cliToolInfo struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Installed bool   `json:"installed"`
}

type cliToolProbe struct {
	name    string
	command string
}

type detectCliToolsArgs struct {
	Force bool `json:"force"`
}

type cliToolResultsCache struct {
	mu        sync.Mutex
	ready     bool
	expiresAt time.Time
	results   []cliToolInfo
}

const (
	cliProbeTimeout  = 8 * time.Second
	cliToolsCacheTTL = 5 * time.Minute
)

var detectedCliToolsCache cliToolResultsCache

func cliToolProbeCatalog() []cliToolProbe {
	return []cliToolProbe{
		{name: "Node.js", command: "node"},
		{name: "Git", command: "git"},
		{name: "Claude Code", command: "claude"},
		{name: "Gemini CLI", command: "gemini"},
		{name: "Codex CLI", command: "codex"},
		{name: "Kimi CLI", command: "kimi"},
		{name: "GLM CLI", command: "glm"},
		{name: "OpenCode", command: "opencode"},
		{name: "QoderCN CLI", command: "qodercn"},
		{name: "Qoder CLI", command: "qoder"},
		{name: "Grok CLI", command: "grok"},
	}
}

func (cache *cliToolResultsCache) get(
	force bool,
	now time.Time,
	load func() []cliToolInfo,
) []cliToolInfo {
	cache.mu.Lock()
	defer cache.mu.Unlock()

	if !force && cache.ready && now.Before(cache.expiresAt) {
		return append([]cliToolInfo(nil), cache.results...)
	}

	results := load()
	cache.results = append(cache.results[:0], results...)
	cache.ready = true
	cache.expiresAt = now.Add(cliToolsCacheTTL)
	return append([]cliToolInfo(nil), cache.results...)
}

func detectCliToolsHandler(_, argsJson string) (string, error) {
	var args detectCliToolsArgs
	if err := core.DecodeArgs(argsJson, &args); err != nil {
		return "", err
	}
	results := detectedCliToolsCache.get(args.Force, time.Now(), detectCliTools)
	return core.EncodeResult(results)
}

func detectCliTools() []cliToolInfo {
	probes := cliToolProbeCatalog()
	pathValue := cliProbePath()
	results := make([]cliToolInfo, len(probes))
	var wg sync.WaitGroup
	for i, probe := range probes {
		wg.Add(1)
		go func(idx int, probe cliToolProbe) {
			defer wg.Done()
			results[idx] = probeCliTool(probe.name, probe.command, pathValue)
		}(i, probe)
	}
	wg.Wait()
	return results
}

func probeCliTool(name, command, pathValue string) cliToolInfo {
	resolvedCommand := resolveCliExecutable(command, pathValue)
	if resolvedCommand == "" {
		return cliToolInfo{Name: name, Installed: false}
	}
	c := cliToolProbeCommand(resolvedCommand, pathValue)
	done := make(chan struct{})
	var out []byte
	var runErr error
	go func() {
		out, runErr = c.CombinedOutput()
		close(done)
	}()
	select {
	case <-time.After(cliProbeTimeout):
		if c.Process != nil {
			_ = c.Process.Kill()
		}
		<-done
		return cliToolInfo{Name: name, Installed: false}
	case <-done:
	}
	if runErr != nil {
		return cliToolInfo{Name: name, Installed: false}
	}
	text := strings.TrimSpace(string(out))
	version := ""
	for _, line := range strings.Split(text, "\n") {
		if s := strings.TrimSpace(line); s != "" {
			version = s
			break
		}
	}
	low := strings.ToLower(version)
	if version == "" || low == "unknown" ||
		strings.Contains(low, "not found") || strings.Contains(low, "command not found") {
		return cliToolInfo{Name: name, Installed: false}
	}
	return cliToolInfo{Name: name, Version: version, Installed: true}
}

func cliToolProbeCommand(resolvedCommand, pathValue string) *exec.Cmd {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/C", resolvedCommand, "--version")
	} else {
		cmd = exec.Command(resolvedCommand, "--version")
	}
	core.PrepareBackgroundCommand(cmd)
	cmd.Env = withEnvironmentValue(os.Environ(), "PATH", pathValue)
	return cmd
}

func cliProbePath() string {
	pathValue := os.Getenv("PATH")
	if runtime.GOOS == "windows" {
		return pathValue
	}
	return mergeCliProbePaths(core.LoginShellPath(), pathValue, "/opt/homebrew/bin:/opt/homebrew/sbin:/usr/local/bin:/usr/bin:/bin")
}

func mergeCliProbePaths(paths ...string) string {
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

func withEnvironmentValue(environment []string, key, value string) []string {
	prefix := key + "="
	resolved := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			resolved = append(resolved, entry)
		}
	}
	return append(resolved, prefix+value)
}

func resolveCliExecutable(command, pathValue string) string {
	names := []string{command}
	if runtime.GOOS == "windows" && filepath.Ext(command) == "" {
		names = []string{command + ".exe", command + ".cmd", command + ".bat", command}
	}
	for _, dir := range filepath.SplitList(pathValue) {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		for _, name := range names {
			candidate := filepath.Join(dir, name)
			info, err := os.Stat(candidate)
			if err == nil && !info.IsDir() && (runtime.GOOS == "windows" || info.Mode().Perm()&0o111 != 0) {
				return candidate
			}
		}
	}
	return ""
}

// ---- list_port_processes ----

type portProcessInfo struct {
	Port        uint16 `json:"port"`
	Pid         uint32 `json:"pid"`
	ProcessName string `json:"process_name"`
	State       string `json:"state"`
}

type listPortProcessesArgs struct {
	Ports []uint16 `json:"ports"`
}

func listPortProcessesHandler(_, argsJson string) (string, error) {
	var a listPortProcessesArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	wanted := map[uint16]bool{}
	for _, p := range a.Ports {
		wanted[p] = true
	}
	results := make([]portProcessInfo, 0)
	if runtime.GOOS == "windows" {
		results = listPortProcessesWindows(wanted)
	} else {
		results = listPortProcessesUnix(wanted)
	}
	return core.EncodeResult(results)
}

func listPortProcessesUnix(wanted map[uint16]bool) []portProcessInfo {
	cmd, cancel := core.CommandWithTimeout(core.ShortCommandTimeout, "lsof", "-i", "-P", "-n")
	defer cancel()
	out, err := cmd.Output()
	results := make([]portProcessInfo, 0)
	if err != nil {
		return results
	}
	lines := strings.Split(string(out), "\n")
	for i, line := range lines {
		if i == 0 {
			continue // header
		}
		parts := strings.Fields(line)
		if len(parts) < 9 {
			continue
		}
		nameAddr := parts[8] // e.g. *:80 or 127.0.0.1:8080
		idx := strings.LastIndex(nameAddr, ":")
		if idx < 0 {
			continue
		}
		portStr := nameAddr[idx+1:]
		p, perr := strconv.ParseUint(portStr, 10, 16)
		if perr != nil {
			continue
		}
		if !wanted[uint16(p)] {
			continue
		}
		pid, _ := strconv.ParseUint(parts[1], 10, 32)
		state := parts[3]
		if len(parts) >= 10 {
			state = parts[9]
		}
		results = append(results, portProcessInfo{
			Port:        uint16(p),
			Pid:         uint32(pid),
			ProcessName: parts[0],
			State:       state,
		})
	}
	return results
}

func listPortProcessesWindows(wanted map[uint16]bool) []portProcessInfo {
	cmd, cancel := core.CommandWithTimeout(core.ShortCommandTimeout, "netstat", "-ano")
	defer cancel()
	out, err := cmd.Output()
	results := make([]portProcessInfo, 0)
	if err != nil {
		return results
	}
	names := windowsPidNameMap()
	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.Fields(line)
		if len(parts) < 4 || (parts[0] != "TCP" && parts[0] != "UDP") {
			continue
		}
		local := parts[1]
		idx := strings.LastIndex(local, ":")
		if idx < 0 {
			continue
		}
		p, perr := strconv.ParseUint(local[idx+1:], 10, 16)
		if perr != nil || !wanted[uint16(p)] {
			continue
		}
		state := parts[3]
		pidStr := "0"
		if len(parts) >= 5 {
			pidStr = parts[4]
		}
		pid, _ := strconv.ParseUint(pidStr, 10, 32)
		results = append(results, portProcessInfo{
			Port:        uint16(p),
			Pid:         uint32(pid),
			ProcessName: names[uint32(pid)],
			State:       state,
		})
	}
	return results
}

func windowsPidNameMap() map[uint32]string {
	names := map[uint32]string{}
	cmd, cancel := core.CommandWithTimeout(core.ShortCommandTimeout, "tasklist", "/FO", "CSV", "/NH")
	defer cancel()
	out, err := cmd.Output()
	if err != nil {
		return names
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Split(line, "\",\"")
		if len(fields) < 2 {
			continue
		}
		name := strings.Trim(fields[0], "\"")
		pid, perr := strconv.ParseUint(strings.Trim(fields[1], "\""), 10, 32)
		if perr != nil {
			continue
		}
		names[uint32(pid)] = name
	}
	return names
}

// ---- kill_process_by_pid ----

type killProcessArgs struct {
	Pid uint32 `json:"pid"`
}

func killProcessByPidHandler(_, argsJson string) (string, error) {
	var a killProcessArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if err := validateKillTargetPid(a.Pid); err != nil {
		return "", err
	}
	var c *exec.Cmd
	var cancel context.CancelFunc
	if runtime.GOOS == "windows" {
		c, cancel = core.CommandWithTimeout(core.ShortCommandTimeout, "taskkill", "/F", "/PID", strconv.Itoa(int(a.Pid)))
	} else {
		c, cancel = core.CommandWithTimeout(core.ShortCommandTimeout, "kill", "-9", strconv.Itoa(int(a.Pid)))
	}
	defer cancel()
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("无法终止进程 %d: %w", a.Pid, err)
	}
	return core.EncodeResult(true)
}

// validateKillTargetPid 拒绝特殊 PID：kill 0 会杀掉 MinCode 所在进程组，1 为 init/launchd，
// 超出 int32 的值会被 kill 截断为负数（-1 表示当前用户的全部进程），也不允许杀自身。
func validateKillTargetPid(pid uint32) error {
	if pid <= 1 || pid > math.MaxInt32 || int(pid) == os.Getpid() {
		return fmt.Errorf("kill_process_by_pid: 拒绝终止进程 %d", pid)
	}
	return nil
}
