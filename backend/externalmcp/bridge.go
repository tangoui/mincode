// Package externalmcp exposes an opt-in, metadata-only MCP bridge to local stdio clients.
package externalmcp

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"mincode/app/backend/core"
	_ "mincode/app/backend/projectindex"
)

const flag = "--external-local-mcp"
const directory = "com.mincode.desktop/external-mcp"

type project struct {
	Name string `json:"name"`
	Root string `json:"root"`
}
type config struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}
type state struct {
	// op 串行化整个启用/更新/停用流程（含索引刷新），避免停用被稍后的启动覆盖或并发启用泄漏监听器。
	op       sync.Mutex
	mu       sync.RWMutex
	server   *http.Server
	listener net.Listener
	token    string
	projects []project
	path     string
}

var bridge state
var dataDir = core.UserDataDir
var dispatch = core.Dispatch

func IsHelper(args []string) bool { return len(args) == 1 && args[0] == flag }

func init() {
	core.Register("external_mcp_enable", command)
	core.Register("external_mcp_update", command)
	core.Register("external_mcp_disable", command)
	core.Register("external_mcp_status", command)
	core.Register("external_mcp_client_executable", command)
}

// transientExecutable reports macOS launches whose path disappears later: a mounted DMG or Gatekeeper App Translocation.
func transientExecutable(goos, executable string) bool {
	return goos == "darwin" && (strings.HasPrefix(executable, "/Volumes/") || strings.Contains(executable, "/AppTranslocation/"))
}

func command(cmd, raw string) (string, error) {
	if len(raw) > 65536 {
		return "", errors.New("MCP configuration too large")
	}
	switch cmd {
	case "external_mcp_client_executable":
		executable, err := os.Executable()
		if err != nil {
			return "", fmt.Errorf("resolve external MCP client executable: %w", err)
		}
		if !filepath.IsAbs(executable) {
			return "", errors.New("external MCP client executable is not an absolute path")
		}
		info, err := os.Stat(executable)
		if err != nil || !info.Mode().IsRegular() {
			return "", errors.New("external MCP client executable is unavailable")
		}
		return core.EncodeResult(map[string]any{"executable": executable, "transient": transientExecutable(runtime.GOOS, executable)})
	case "external_mcp_disable":
		Shutdown()
		return core.EncodeResult(map[string]any{"enabled": false})
	case "external_mcp_status":
		bridge.mu.RLock()
		defer bridge.mu.RUnlock()
		return core.EncodeResult(map[string]any{"enabled": bridge.server != nil, "projectCount": len(bridge.projects), "rendezvousFile": bridge.path})
	}
	var args struct {
		Projects []project `json:"projects"`
	}
	if err := core.DecodeArgsPayload(raw, &args); err != nil {
		return "", err
	}
	projects, err := validateProjects(args.Projects)
	if err != nil {
		return "", err
	}
	if err := enable(cmd, projects); err != nil {
		return "", err
	}
	return command("external_mcp_status", "{}")
}

func enable(cmd string, projects []project) error {
	bridge.op.Lock()
	defer bridge.op.Unlock()
	if cmd == "external_mcp_update" {
		bridge.mu.RLock()
		enabled := bridge.server != nil
		bridge.mu.RUnlock()
		if !enabled {
			return errors.New("external MCP is disabled")
		}
	}
	// Clear old authorization before refreshing. An empty scope is allowed, but never searched.
	bridge.shutdown()
	roots := make([]string, 0, len(projects))
	for _, p := range projects {
		roots = append(roots, p.Root)
	}
	if len(roots) > 0 {
		encoded, _ := json.Marshal(map[string]any{"roots": roots})
		if _, err := dispatch("project_index_refresh", string(encoded)); err != nil {
			return fmt.Errorf("refresh MCP project index: %w", err)
		}
	}
	return bridge.start(projects)
}

func validateProjects(input []project) ([]project, error) {
	if len(input) > 12 {
		return nil, errors.New("at most 12 projects are allowed")
	}
	seen := map[string]bool{}
	output := make([]project, 0, len(input))
	for _, p := range input {
		if !filepath.IsAbs(p.Root) || strings.TrimSpace(p.Root) != p.Root || len(p.Root) > 4096 || len(p.Name) > 128 {
			return nil, errors.New("invalid project")
		}
		root := filepath.Clean(p.Root)
		info, err := os.Lstat(root)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("project root must be a real directory")
		}
		// Canonicalize parent symlinks too; use only the resulting path in the index.
		root, err = filepath.EvalSymlinks(root)
		if err != nil {
			return nil, errors.New("invalid project root")
		}
		if seen[root] {
			continue
		}
		seen[root] = true
		name := strings.TrimSpace(p.Name)
		if strings.ContainsAny(name, "/\\\r\n\x00") || filepath.IsAbs(name) {
			return nil, errors.New("project name must be a short display label")
		}
		if name == "" {
			name = filepath.Base(root)
		}
		output = append(output, project{Name: name, Root: root})
	}
	return output, nil
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func rendezvousPath() (string, error) {
	base := dataDir()
	if base == "" {
		return "", errors.New("app data directory unavailable")
	}
	release, err := lockPrivateParents(base)
	if err != nil {
		return "", err
	}
	defer release()
	// com.mincode.desktop 是各模块共享的应用数据目录，沿用继承 ACL；只要求它是真实目录，
	// 不修改其权限。令牌的私有性由 external-mcp 这一级保证。
	parent := filepath.Join(base, "com.mincode.desktop")
	if err := os.Mkdir(parent, 0700); err != nil && !os.IsExist(err) {
		return "", err
	}
	if info, err := os.Lstat(parent); err != nil || info.Mode().Type() != fs.ModeDir {
		return "", errors.New("MCP data directory must be a real directory, not a symlink")
	}
	unlockParent, err := lockPrivateParents(parent)
	if err != nil {
		return "", err
	}
	defer unlockParent()
	private := filepath.Join(parent, "external-mcp")
	if err := makePrivateDir(private); err != nil {
		return "", fmt.Errorf("MCP data directory must be user-only and not a symlink: %w", err)
	}
	unlockPrivate, err := lockPrivateParents(private)
	if err != nil {
		return "", err
	}
	defer unlockPrivate()
	return filepath.Join(base, directory, "current.json"), nil
}
func lockRendezvousParents(path string) (func(), error) {
	dir := filepath.Dir(path)
	release, err := lockPrivateParents(dir)
	if err != nil {
		return nil, err
	}
	if err := verifyPrivatePath(dir, true); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

func (b *state) start(projects []project) error {
	path, err := rendezvousPath()
	if err != nil {
		return err
	}
	release, err := lockRendezvousParents(path)
	if err != nil {
		return err
	}
	defer release()
	if err := verifyPrivatePath(path, false); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("unsafe MCP rendezvous record: %w", err)
	}
	token, err := randomToken()
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	url := "http://" + listener.Addr().String() + "/tool"
	tmp, err := createPrivateTemp(filepath.Dir(path))
	if err != nil {
		listener.Close()
		return err
	}
	defer os.Remove(tmp.Name())
	if err = protectPrivateFile(tmp); err == nil {
		err = json.NewEncoder(tmp).Encode(config{URL: url, Token: token})
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		listener.Close()
		return err
	}
	b.mu.Lock()
	previousServer, previousListener := b.server, b.listener
	b.token = token
	b.projects = projects
	b.listener = listener
	b.path = path
	b.server = &http.Server{Handler: http.HandlerFunc(b.handle), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 8 * time.Second, MaxHeaderBytes: 4096}
	server := b.server
	b.mu.Unlock()
	// 被替换的旧服务必须关闭，否则其监听器会一直泄漏。
	if previousServer != nil {
		_ = previousServer.Close()
	}
	if previousListener != nil {
		_ = previousListener.Close()
	}
	go server.Serve(listener)
	return nil
}

// RemoveStaleRecord 仅删除崩溃 GUI 遗留的安全私有常规文件。
// 不安全权限、重解析路径与其他异常记录保留，使启用流程保持拒绝。
func RemoveStaleRecord() {
	base := dataDir()
	if base == "" {
		return
	}
	path := filepath.Join(base, directory, "current.json")
	removePrivateRecord(path)
}

func removePrivateRecord(path string) {
	release, err := lockRendezvousParents(path)
	if err != nil {
		return
	}
	defer release()
	if err := verifyPrivatePath(path, false); err == nil {
		_ = os.Remove(path)
	}
}

// Shutdown 等待进行中的启用/更新完成后再停用，保证停用不会被其覆盖。
func Shutdown() {
	bridge.op.Lock()
	defer bridge.op.Unlock()
	bridge.shutdown()
}

// shutdown 要求调用方已持有 b.op。
func (b *state) shutdown() {
	b.mu.Lock()
	server := b.server
	path := b.path
	b.server = nil
	b.listener = nil
	b.token = ""
	b.projects = nil
	b.path = ""
	if path != "" {
		removePrivateRecord(path)
	}
	b.mu.Unlock()
	if server != nil {
		_ = server.Close()
	}
}

type call struct {
	Name      string `json:"name"`
	Arguments struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	} `json:"arguments"`
}

func (b *state) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost || r.URL.Path != "/tool" {
		http.Error(w, "not found", 404)
		return
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || host != "127.0.0.1" {
		http.Error(w, "unauthorized", 401)
		return
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	candidate := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if b.server == nil || len(candidate) != len(b.token) || subtle.ConstantTimeCompare([]byte(candidate), []byte(b.token)) != 1 {
		http.Error(w, "unauthorized", 401)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var c call
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&c); err != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	switch c.Name {
	case "workspace.list_projects":
		list := make([]map[string]string, 0, len(b.projects))
		for i, p := range b.projects {
			list = append(list, map[string]string{"id": fmt.Sprintf("project-%d", i+1), "name": p.Name})
		}
		_ = json.NewEncoder(w).Encode(list)
	case "workspace.search_files":
		if len(b.projects) == 0 {
			_ = json.NewEncoder(w).Encode([]any{})
			return
		}
		if len(c.Arguments.Query) == 0 || len(c.Arguments.Query) > 256 || c.Arguments.Limit < 0 || c.Arguments.Limit > 50 {
			http.Error(w, "invalid search", 400)
			return
		}
		roots := make([]string, 0, len(b.projects))
		ids := map[string]string{}
		for i, p := range b.projects {
			roots = append(roots, p.Root)
			ids[p.Root] = fmt.Sprintf("project-%d", i+1)
		}
		limit := c.Arguments.Limit
		if limit == 0 {
			limit = 20
		}
		args, _ := json.Marshal(map[string]any{"roots": roots, "query": c.Arguments.Query, "limit": limit})
		raw, err := core.Dispatch("project_index_search", string(args))
		if err != nil {
			http.Error(w, "index unavailable", 503)
			return
		}
		var entries []struct {
			Root string `json:"root"`
			Path string `json:"path"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal([]byte(raw), &entries); err != nil {
			http.Error(w, "index unavailable", 503)
			return
		}
		results := make([]map[string]string, 0, len(entries))
		for _, entry := range entries {
			if id := ids[entry.Root]; id != "" {
				results = append(results, map[string]string{"projectId": id, "path": entry.Path, "name": entry.Name})
			}
		}
		_ = json.NewEncoder(w).Encode(results)
	default:
		http.Error(w, "tool not allowed", 403)
	}
}
