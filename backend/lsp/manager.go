package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mincode/app/backend/core"
)

type serverStatus struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Languages  []string `json:"languages"`
	Installed  bool     `json:"installed"`
	Executable string   `json:"executable,omitempty"`
	Heavy      bool     `json:"heavy"`
	// Auxiliary 标记“辅助型”服务器（如 Tailwind/Angular）：它们与主服务器
	// 共享同一批语言，前端会把文档同步扇出给所有服务器，并在补全/悬停等
	// 能力上合并结果，而不是互相顶替。
	Auxiliary           bool   `json:"auxiliary,omitempty"`
	InstallHint         string `json:"installHint"`
	Installable         bool   `json:"installable"`
	Platform            string `json:"platform"`
	Running             bool   `json:"running"`
	Generation          uint64 `json:"generation,omitempty"`
	Error               string `json:"error,omitempty"`
	PrerequisiteMissing bool   `json:"prerequisiteMissing,omitempty"`
	PrerequisiteHint    string `json:"prerequisiteHint,omitempty"`
	// Companions must run beside this server (Vue 3.x hybrid → vue-ts); the
	// frontend enables them implicitly with it.
	Companions []string `json:"companions,omitempty"`
}

type serverEvent struct {
	Root       string          `json:"root"`
	ServerID   string          `json:"serverId"`
	Generation uint64          `json:"generation"`
	Method     string          `json:"method"`
	Params     json.RawMessage `json:"params"`
	// RequestID is set when the server initiates a request (id + method both present).
	// The frontend must call project_lsp_reply with this id to prevent the server timing out.
	RequestID *uint64 `json:"requestId,omitempty"`
}

type startArgs struct {
	Root     string `json:"root"`
	ServerID string `json:"serverId"`
}

type messageArgs struct {
	Root       string          `json:"root"`
	ServerID   string          `json:"serverId"`
	Generation uint64          `json:"generation"`
	Method     string          `json:"method"`
	Params     json.RawMessage `json:"params"`
}

type requestArgs struct {
	messageArgs
	TimeoutMs int `json:"timeoutMs"`
}

var manager = struct {
	sync.RWMutex
	sessions map[string]*session
}{sessions: map[string]*session{}}

var generation atomic.Uint64

// lookPathCache caches exec.LookPath results for 5 seconds to avoid repeated
// filesystem scans on every listServersHandler call.
var (
	lookPathMu    sync.Mutex
	lookPathCache = map[string]struct {
		path string
		err  error
		at   time.Time
	}{}
)

func cachedLookPath(command string) (string, error) {
	lookPathMu.Lock()
	if entry, ok := lookPathCache[command]; ok && time.Since(entry.at) < 5*time.Second {
		path, err := entry.path, entry.err
		lookPathMu.Unlock()
		return path, err
	}
	lookPathMu.Unlock()
	path, err := lookPathExpanded(command)
	lookPathMu.Lock()
	lookPathCache[command] = struct {
		path string
		err  error
		at   time.Time
	}{path, err, time.Now()}
	lookPathMu.Unlock()
	return path, err
}

// invalidateLookPathCache removes a command from the cache so the next call
// re-scans the filesystem. Call this after successfully installing a binary.
func invalidateLookPathCache(command string) {
	lookPathMu.Lock()
	delete(lookPathCache, command)
	lookPathMu.Unlock()
}

// primeLookPathCacheFromShell finds a binary using the shell with the expanded
// environment and caches the result — but ONLY on success. On failure we skip
// writing to the cache so that cachedLookPath falls through to lookPathExpanded,
// which searches hardcoded + glob candidate dirs with direct stat() calls and
// often finds binaries (e.g. in nvm versioned dirs) that the non-login sh cannot.
func primeLookPathCacheFromShell(command string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", "where "+command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", "command -v "+command)
	}
	core.PrepareBackgroundCommand(cmd)
	cmd.Env = core.ExpandedEnv()
	out, err := cmd.Output()
	if err != nil {
		return // don't cache negatives; let lookPathExpanded try its glob list
	}
	// 'where' on Windows may return multiple lines; take the first.
	lines := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)
	path := strings.TrimSpace(lines[0])
	if path == "" {
		return
	}
	lookPathMu.Lock()
	lookPathCache[command] = struct {
		path string
		err  error
		at   time.Time
	}{path, nil, time.Now()}
	lookPathMu.Unlock()
}

// lookPathExpanded searches PATH plus common package-manager bin directories
// that macOS/Linux GUI apps often don't inherit from the user's shell profile.
func lookPathExpanded(command string) (string, error) {
	if path, err := exec.LookPath(command); err == nil {
		return path, nil
	}
	home, _ := os.UserHomeDir()
	var candidates []string
	if runtime.GOOS == "windows" {
		appData := os.Getenv("APPDATA")
		localAppData := os.Getenv("LOCALAPPDATA")
		programFiles := os.Getenv("ProgramFiles")
		candidates = []string{
			filepath.Join(appData, "npm"),                               // npm -g default (official Node installer)
			filepath.Join(home, "go", "bin"),                            // go install
			filepath.Join(home, ".cargo", "bin"),                        // cargo / winget rust-analyzer
			filepath.Join(home, ".dotnet", "tools"),                     // dotnet tool install -g
			filepath.Join(home, "scoop", "shims"),                       // Scoop package manager
			filepath.Join(localAppData, "Microsoft", "WinGet", "Links"), // winget symlinks
			filepath.Join(programFiles, "LLVM", "bin"),                  // winget install LLVM (clangd)
			filepath.Join(programFiles, "Go", "bin"),                    // official Go installer
			"/usr/bin", "/usr/local/bin",                                // Git Bash / MSYS2
		}
		// nvm-windows stores each Node version in %APPDATA%\nvm\v<version>
		for _, pattern := range []string{
			filepath.Join(appData, "nvm", "v*"),
			filepath.Join(localAppData, "nvm", "v*"),
		} {
			if matches, err := filepath.Glob(pattern); err == nil {
				candidates = append(candidates, matches...)
			}
		}
		// fnm on Windows
		for _, pattern := range []string{
			filepath.Join(localAppData, "fnm", "node-versions", "*", "installation"),
			filepath.Join(localAppData, "Microsoft", "fnm", "node-versions", "*", "installation"),
		} {
			if matches, err := filepath.Glob(pattern); err == nil {
				candidates = append(candidates, matches...)
			}
		}
		// volta on Windows
		if localAppData != "" {
			candidates = append(candidates, filepath.Join(localAppData, "Volta", "bin"))
		}
		candidates = append(candidates, core.WindowsToolPathCandidates()...)
		// On Windows, npm packages install as .cmd wrappers; also try .exe.
		for _, dir := range candidates {
			for _, ext := range []string{"", ".exe", ".cmd", ".bat"} {
				full := filepath.Join(dir, command+ext)
				if info, statErr := os.Stat(full); statErr == nil && !info.IsDir() {
					return full, nil
				}
			}
		}
		return "", fmt.Errorf("%s: executable not found", command)
	}

	candidates = []string{
		"/usr/local/bin",
		"/opt/homebrew/bin",
		"/opt/homebrew/sbin",
		"/opt/homebrew/opt/llvm/bin", // brew install llvm (Apple Silicon)
		"/usr/local/opt/llvm/bin",    // brew install llvm (Intel Mac)
		"/opt/homebrew/opt/ruby/bin", // brew install ruby (Apple Silicon)
		"/usr/local/opt/ruby/bin",    // brew install ruby (Intel Mac)
		filepath.Join(home, "go/bin"),
		filepath.Join(home, ".cargo/bin"),
		filepath.Join(home, ".npm-global/bin"),
		filepath.Join(home, ".local/bin"),
		"/usr/local/go/bin",
		"/usr/local/npm/bin",
		"/usr/local/lib/node_modules/.bin",
		"/usr/local/share/dotnet",
		"/usr/local/share/dotnet/x64",
		filepath.Join(home, ".dotnet/tools"),
		// Xcode / sourcekit-lsp
		"/Applications/Xcode.app/Contents/Developer/usr/bin",
		"/Library/Developer/CommandLineTools/usr/bin",
	}
	// Homebrew Ruby: gem executables land in the gemhome bin dir, not the ruby
	// keg bin dir. Enumerate versioned gem bin dirs via glob so we find them
	// regardless of the installed Ruby version (e.g. /opt/homebrew/lib/ruby/gems/4.0.0/bin).
	for _, pattern := range []string{
		"/opt/homebrew/lib/ruby/gems/*/bin",
		"/usr/local/lib/ruby/gems/*/bin",
		filepath.Join(home, ".gem/ruby/*/bin"),
		filepath.Join(home, ".local/share/gem/ruby/*/bin"),
	} {
		if matches, err := filepath.Glob(pattern); err == nil {
			candidates = append(candidates, matches...)
		}
	}
	// Node version managers: nvm, fnm, and asdf put node binaries in versioned
	// subdirectories that GUI apps often don't inherit from the user's shell profile.
	for _, pattern := range []string{
		filepath.Join(home, ".nvm/versions/node/*/bin"),
		filepath.Join(home, ".local/share/fnm/node-versions/*/installation/bin"),
		filepath.Join(home, ".asdf/installs/nodejs/*/bin"),
		filepath.Join(home, ".volta/bin"), // volta uses shims in a single dir
	} {
		if matches, err := filepath.Glob(pattern); err == nil {
			candidates = append(candidates, matches...)
		}
	}
	// opam: ocamllsp 等装在 switch 的 bin 目录（如 ~/.opam/default/bin），
	// GUI 进程不会继承 shell 里 eval $(opam env) 的 PATH，需自己探测。
	for _, pattern := range []string{
		filepath.Join(home, ".opam/*/bin"),
	} {
		if matches, err := filepath.Glob(pattern); err == nil {
			candidates = append(candidates, matches...)
		}
	}
	for _, dir := range candidates {
		full := filepath.Join(dir, command)
		if info, statErr := os.Stat(full); statErr == nil && !info.IsDir() {
			return full, nil
		}
	}
	return "", fmt.Errorf("%s: executable not found", command)
}

type replyArgs struct {
	Root       string          `json:"root"`
	ServerID   string          `json:"serverId"`
	Generation uint64          `json:"generation"`
	RequestID  uint64          `json:"requestId"`
	Result     json.RawMessage `json:"result"`
}

func replyHandler(_, argsJSON string) (string, error) {
	var args replyArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	active, err := findSession(messageArgs{Root: args.Root, ServerID: args.ServerID, Generation: args.Generation})
	if err != nil {
		return "", err
	}
	if err := active.reply(args.RequestID, args.Result); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

func init() {
	core.Register("project_lsp_list_servers", listServersHandler)
	core.Register("project_lsp_start", startHandler)
	core.Register("project_lsp_stop", stopHandler)
	core.Register("project_lsp_request", requestHandler)
	core.Register("project_lsp_notify", notifyHandler)
	core.Register("project_lsp_reply", replyHandler)
	core.Register("project_lsp_install", installHandler)
	core.Register("project_lsp_logs", logsHandler)
	core.Register("project_lsp_jdt_contents", jdtContentsHandler)
}

func sessionKey(root, serverID string) string { return root + "\x00" + serverID }

func listServersHandler(_, _ string) (string, error) {
	manager.RLock()
	defer manager.RUnlock()
	servers := make([]serverStatus, 0, len(serverDefinitions))
	for _, definition := range serverDefinitions {
		status := executableStatus(definition)
		for _, active := range manager.sessions {
			if active.definition.ID == definition.ID && !active.closed.Load() {
				status.Running = true
				status.Generation = active.generation
				break
			}
		}
		servers = append(servers, status)
	}
	return core.EncodeResult(servers)
}

func startHandler(_, argsJSON string) (string, error) {
	var args startArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	active, err := start(args.Root, args.ServerID)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(statusFor(active))
}

func start(root, serverID string) (*session, error) {
	if IsExternalServerID(serverID) {
		return nil, errExternalStart
	}
	definition, err := requireDefinition(serverID)
	if err != nil {
		return nil, err
	}
	canonical, err := canonicalRoot(root)
	if err != nil {
		return nil, err
	}
	key := sessionKey(canonical, definition.ID)
	manager.Lock()
	defer manager.Unlock()
	if active := manager.sessions[key]; active != nil && !active.closed.Load() {
		return active, nil
	}
	active := &session{key: key, root: canonical, definition: definition, pending: map[uint64]chan rpcReply{}, generation: generation.Add(1)}
	if err := active.start(); err != nil {
		active.log("error process start failed")
		active.close(err)
		manager.sessions[key] = active
		return nil, err
	}
	manager.sessions[key] = active
	return active, nil
}

func statusFor(active *session) serverStatus {
	if active.external != nil {
		return externalStatus(active)
	}
	status := executableStatus(active.definition)
	status.Running = !active.closed.Load()
	status.Generation = active.generation
	if active.closed.Load() {
		status.Error = active.failureText()
	}
	return status
}

func stopHandler(_, argsJSON string) (string, error) {
	var args startArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if err := stop(args.Root, args.ServerID); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

func stop(root, serverID string) error {
	id, err := sessionServerID(serverID)
	if err != nil {
		return err
	}
	canonical, err := canonicalRoot(root)
	if err != nil {
		return err
	}
	key := sessionKey(canonical, id)
	manager.Lock()
	active := manager.sessions[key]
	// Retain the bounded log tail until the next start (including explicit stops).
	manager.Unlock()
	if active != nil {
		active.close(fmt.Errorf("language server stopped"))
	}
	return nil
}

func findSession(args messageArgs) (*session, error) {
	id, err := sessionServerID(args.ServerID)
	if err != nil {
		return nil, err
	}
	canonical, err := canonicalRoot(args.Root)
	if err != nil {
		return nil, err
	}
	manager.RLock()
	active := manager.sessions[sessionKey(canonical, id)]
	manager.RUnlock()
	if active == nil || active.closed.Load() {
		return nil, fmt.Errorf("language server is not running")
	}
	if args.Generation != active.generation {
		return nil, fmt.Errorf("language server session is stale")
	}
	if retireInvalidExternal(active) {
		return nil, fmt.Errorf("unavailable: the plugin that provides this language server is no longer available")
	}
	return active, nil
}

const maxJdtContentsURIBytes = 8 * 1024

// jdtContentsHandler reads a JDT class file (source or decompiled source)
// through the already-running built-in jdtls. It is intentionally narrower
// than project_lsp_request: only jdt:// URIs reach java/classFileContents.
func jdtContentsHandler(_, argsJSON string) (string, error) {
	var args struct {
		Root string `json:"root"`
		URI  string `json:"uri"`
	}
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if strings.TrimSpace(args.Root) == "" || args.URI == "" || !strings.HasPrefix(args.URI, "jdt://") ||
		len([]byte(args.URI)) > maxJdtContentsURIBytes || strings.ContainsAny(args.URI, "\x00\r\n") {
		return "", fmt.Errorf("invalid_request: uri must be a valid jdt:// class source")
	}
	active, ok := runningBuiltinSession(args.Root, jdtlsServerID)
	if !ok {
		return "", fmt.Errorf("unavailable: Java language service is not running")
	}
	params, _ := json.Marshal(map[string]string{"uri": args.URI})
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	result, err := active.request(ctx, "java/classFileContents", params)
	if err != nil {
		return "", fmt.Errorf("not_found: Java source %q: %w", args.URI, err)
	}
	var content *string
	if err := json.Unmarshal(result, &content); err != nil || content == nil || *content == "" {
		return "", fmt.Errorf("not_found: Java source %q is unavailable", args.URI)
	}
	return core.EncodeResult(*content)
}

func requestHandler(_, argsJSON string) (string, error) {
	var args requestArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if args.Method == "" || len(args.Params) > maxMessageBytes {
		return "", fmt.Errorf("invalid language-server request")
	}
	active, err := findSession(args.messageArgs)
	if err != nil {
		return "", err
	}
	params, err := confineExternalMessage(active, args.Root, args.Method, args.Params)
	if err != nil {
		return "", err
	}
	args.Params = params
	timeout := requestTimeout
	if args.TimeoutMs > 0 && args.TimeoutMs <= int(requestTimeout.Milliseconds()) {
		timeout = time.Duration(args.TimeoutMs) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	result, err := active.request(ctx, args.Method, args.Params)
	if err != nil {
		return "", err
	}
	return string(result), nil
}

func notifyHandler(_, argsJSON string) (string, error) {
	var args messageArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if args.Method == "" || len(args.Params) > maxMessageBytes {
		return "", fmt.Errorf("invalid language-server notification")
	}
	active, err := findSession(args)
	if err != nil {
		return "", err
	}
	params, err := confineExternalMessage(active, args.Root, args.Method, args.Params)
	if err != nil {
		return "", err
	}
	if err := active.notify(args.Method, params); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

func installHandler(_, argsJSON string) (string, error) {
	var args struct {
		ServerID string `json:"serverId"`
	}
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if IsExternalServerID(args.ServerID) {
		return "", errExternalInstall
	}
	definition, err := requireDefinition(args.ServerID)
	if err != nil {
		return "", err
	}
	hint, installable, prerequisite, prerequisiteHint := resolveInstall(definition)
	if !installable || hint == "" {
		return "", fmt.Errorf("no auto-install command available for %s", definition.Name)
	}
	if err := checkInstallPrerequisite(prerequisite, prerequisiteHint); err != nil {
		return "", err
	}
	var output string
	var installErr error
	if runtime.GOOS == "windows" && (definition.ID == "csharp-ls" || definition.ID == "fsautocomplete") {
		invalidateLookPathCache("dotnet")
		if definition.ID == "csharp-ls" {
			output, installErr = installWindowsCSharp(lookPathExpanded, runLSPInstallCommand)
		} else {
			output, installErr = installWindowsDotnetTool("fsautocomplete", "0.85.4", 8, lookPathExpanded, runLSPInstallCommand)
		}
	} else if runtime.GOOS == "windows" && (definition.ID == "lua-ls" || definition.ID == "terraform-ls" || definition.ID == "dart-ls") {
		// A verified existing executable is success, not a failed winget upgrade.
		if path, err := lookPathExpanded(definition.Command); err == nil {
			output = "Already installed: " + path
		} else {
			output, installErr = runInstall(hint)
			if installErr != nil {
				if _, err := lookPathExpanded(definition.Command); err == nil {
					installErr = nil
				}
			}
		}
	} else {
		output, installErr = runInstall(hint)
	}
	if installErr == nil {
		if args.ServerID == "r-languageserver" {
			invalidateRServerProbe()
		}
		// A Vue hybrid install also installs the primary, companion and shared
		// TypeScript server. Drop cached misses for every affected executable.
		if args.ServerID == vueCompanionID {
			invalidateLookPathCache("vue-language-server")
			primeLookPathCacheFromShell("vue-language-server")
			tryPrimeCacheViaNpm("vue-language-server")
		}
		invalidateLookPathCache(definition.Command)
		// Probe via shell (handles custom shell-configured paths).
		primeLookPathCacheFromShell(definition.Command)
		// For npm installs, also query `npm prefix -g` directly — this is
		// definitive because we ask the same npm that ran the install where
		// it put its global binaries.
		if strings.Contains(hint, "npm install") || strings.Contains(hint, "npm add") {
			tryPrimeCacheViaNpm(definition.Command)
		}
	}
	type installResult struct {
		Output string `json:"output"`
		Error  string `json:"error,omitempty"`
	}
	result := installResult{Output: output}
	if installErr != nil {
		result.Error = installErr.Error()
	}
	return core.EncodeResult(result)
}

// logsHandler 返回会话的有界 lifecycle/protocol 日志和 stderr 缓冲，
// 未启动过是正常的按需启动状态，返回空日志而非传输错误。
func logsHandler(_, argsJSON string) (string, error) {
	var args startArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	id, err := sessionServerID(args.ServerID)
	if err != nil {
		return "", err
	}
	canonical, err := canonicalRoot(args.Root)
	if err != nil {
		return "", err
	}
	manager.RLock()
	active := manager.sessions[sessionKey(canonical, id)]
	manager.RUnlock()
	if active == nil {
		return core.EncodeResult(struct {
			Output  string `json:"output"`
			Running bool   `json:"running"`
		}{})
	}
	result := struct {
		Output  string `json:"output"`
		Running bool   `json:"running"`
	}{Output: active.logs(), Running: !active.closed.Load()}
	return core.EncodeResult(result)
}

// tryPrimeCacheViaNpm queries `npm prefix -g` to find the npm global prefix,
// then checks whether command exists at <prefix>/bin/<command>. This is the
// most reliable post-install detection for npm packages because it asks npm
// itself rather than guessing from PATH or glob patterns.
func tryPrimeCacheViaNpm(command string) {
	npmPath, err := lookPathExpanded("npm")
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, npmPath, "prefix", "-g")
	core.PrepareBackgroundCommand(cmd)
	cmd.Env = core.ExpandedEnv()
	out, runErr := cmd.Output()
	if runErr != nil {
		return
	}
	prefix := strings.TrimSpace(string(out))
	if prefix == "" {
		return
	}
	var full string
	if runtime.GOOS == "windows" {
		for _, ext := range []string{".cmd", ".bat", ".exe", ""} {
			c := filepath.Join(prefix, command+ext)
			if info, e := os.Stat(c); e == nil && !info.IsDir() {
				full = c
				break
			}
		}
	} else {
		c := filepath.Join(prefix, "bin", command)
		if info, e := os.Stat(c); e == nil && !info.IsDir() {
			full = c
		}
	}
	if full == "" {
		return
	}
	lookPathMu.Lock()
	lookPathCache[command] = struct {
		path string
		err  error
		at   time.Time
	}{full, nil, time.Now()}
	lookPathMu.Unlock()
}

// Validate again at execution time: a stale UI must not run commands whose
// prerequisite is absent, even if the button was enabled when the list loaded.
func checkInstallPrerequisite(command, hint string) error {
	if command == "" {
		return nil
	}
	if _, err := cachedLookPath(command); err != nil {
		return fmt.Errorf("missing installation prerequisite %s: %s", command, hint)
	}
	return nil
}

func runInstall(hint string) (string, error) {
	// 部分安装链耗时远超普通包：opam init 引导编译 OCaml 编译器、
	// brew tap + 大体积 bottle（如 Dart SDK）等，十分钟级属正常范围。
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", hint)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", hint)
	}
	core.PrepareBackgroundCommand(cmd)
	cmd.Env = core.ExpandedEnv()
	var buf limitedBuffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	core.PrepareProcessTree(cmd)
	cmd.Cancel = func() error { return core.KillProcessTree(cmd) }
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	if ctx.Err() != nil {
		err = fmt.Errorf("language-server installation timed out: %w", ctx.Err())
	}
	return decodeInstallOutput([]byte(buf.String())), err
}

func Shutdown(_ context.Context) {
	manager.Lock()
	sessions := manager.sessions
	manager.sessions = map[string]*session{}
	manager.Unlock()
	for _, active := range sessions {
		active.close(fmt.Errorf("application is shutting down"))
	}
	// Give killed processes a moment to exit so their per-session jdtls
	// configuration directories are removed before the app quits; any
	// left behind are swept by the next MinCode run (jdtls_config.go).
	deadline := time.After(shutdownCleanupWait)
	for _, active := range sessions {
		if active.exited == nil || active.configDir == "" {
			continue
		}
		select {
		case <-active.exited:
		case <-deadline:
			return
		}
	}
}

// shutdownCleanupWait bounds how long Shutdown waits for killed language
// servers to exit.
const shutdownCleanupWait = 3 * time.Second

func resolveProjectURI(root, uri string) (string, error) {
	parsed, err := url.Parse(uri)
	if err != nil || parsed.Scheme != "file" {
		return "", fmt.Errorf("language-server URI must use file:// scheme")
	}
	fsPath := filepath.FromSlash(parsed.Path)
	absolute, err := filepath.Abs(fsPath)
	if err != nil {
		return "", fmt.Errorf("resolve language-server URI: %w", err)
	}
	relative, err := filepath.Rel(root, absolute)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("language-server URI escapes project root")
	}
	return absolute, nil
}
