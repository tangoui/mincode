package sessions

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"mincode/app/backend/core"
)

// 会话存储目录的用户覆盖：前端「CC 会话 → 工具设置」里填写的数据目录。
// 解析优先级统一为：用户覆盖 > CLI 自身环境变量 > 默认目录。

func init() {
	core.Register("set_ai_session_roots", setAiSessionRootsHandler)
}

// sessionRootAdapters 列出允许覆盖数据目录的内置会话来源。
var sessionRootAdapters = map[string]struct{}{
	"claude":   {},
	"codex":    {},
	"pi":       {},
	"gemini":   {},
	"qodercn":  {},
	"qoder":    {},
	"grok":     {},
	"kimi":     {},
	"opencode": {},
}

var sessionRootOverrides = struct {
	sync.RWMutex
	roots map[string]string
}{roots: map[string]string{}}

// expandSessionPath 展开 ~ 前缀并清理路径；空串原样返回。
func expandSessionPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~"+string(filepath.Separator)) {
		if home, ok := sessionHomeDir(); ok {
			path = filepath.Join(home, path[1:])
		}
	}
	return filepath.Clean(path)
}

// sessionRootOverride 返回某个来源的用户覆盖目录。
func sessionRootOverride(adapter string) (string, bool) {
	sessionRootOverrides.RLock()
	defer sessionRootOverrides.RUnlock()
	dir, ok := sessionRootOverrides.roots[adapter]
	return dir, ok && dir != ""
}

// sessionRootFromEnv 读取 CLI 自身的数据目录环境变量（支持 ~ 前缀）。
func sessionRootFromEnv(name string) (string, bool) {
	dir := expandSessionPath(os.Getenv(name))
	if dir == "" || !filepath.IsAbs(dir) {
		return "", false
	}
	return dir, true
}

type setAiSessionRootsArgs struct {
	Roots map[string]string `json:"roots"`
}

// setAiSessionRootsHandler 以整表替换的方式更新覆盖目录，未出现的来源恢复默认。
// 返回展开 ~ 后的生效目录，前端据此给启动的 CLI 注入数据目录环境变量。
func setAiSessionRootsHandler(_ string, argsJSON string) (string, error) {
	var args setAiSessionRootsArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	next := make(map[string]string, len(args.Roots))
	for adapter, raw := range args.Roots {
		if _, ok := sessionRootAdapters[adapter]; !ok {
			return "", fmt.Errorf("未知的会话来源：%s", adapter)
		}
		dir := expandSessionPath(raw)
		if dir == "" {
			continue
		}
		if !filepath.IsAbs(dir) {
			return "", fmt.Errorf("%s 的数据目录必须是绝对路径：%s", adapter, raw)
		}
		next[adapter] = dir
	}
	sessionRootOverrides.Lock()
	sessionRootOverrides.roots = next
	sessionRootOverrides.Unlock()
	resolved := make(map[string]string, len(next))
	for adapter, dir := range next {
		resolved[adapter] = dir
	}
	return core.EncodeResult(resolved)
}
