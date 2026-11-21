package sessions

import (
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
)

// ─── AI CLI 进程存活探测 ──────────────────────────────────────────────────

// liveCwdsTTL 内复用同一次进程探测结果：前端按项目每 4 秒轮询一次，
// 多个项目并发轮询时不必各自再起 ps/lsof。
const liveCwdsTTL = 2 * time.Second

type liveCwdsCacheEntry struct {
	mu   sync.Mutex
	at   time.Time
	cwds map[string]struct{}
}

var (
	liveCwdsCacheMu sync.Mutex
	liveCwdsCache   = make(map[string]*liveCwdsCacheEntry)
)

// liveSessionCwds 返回当前正在运行、且可执行文件基名在 names 中的
// AI CLI 进程的规范化工作目录集合。平台不支持时返回空集合，
// 调用方回退到转录文件推断。返回的集合在调用方之间共享，只读。
func liveSessionCwds(names ...string) map[string]struct{} {
	if len(names) == 0 {
		return nil
	}
	sorted := slices.Clone(names)
	slices.Sort(sorted)
	key := strings.Join(sorted, "\x00")

	liveCwdsCacheMu.Lock()
	entry, ok := liveCwdsCache[key]
	if !ok {
		entry = &liveCwdsCacheEntry{}
		liveCwdsCache[key] = entry
	}
	liveCwdsCacheMu.Unlock()

	entry.mu.Lock()
	defer entry.mu.Unlock()
	if !entry.at.IsZero() && time.Since(entry.at) < liveCwdsTTL {
		return entry.cwds
	}
	entry.cwds = probeLiveSessionCwds(names)
	entry.at = time.Now()
	return entry.cwds
}

var livePlatformProbe = platformLiveCwds

func probeLiveSessionCwds(names []string) map[string]struct{} {
	want := make(map[string]struct{}, len(names))
	for _, name := range names {
		want[name] = struct{}{}
	}
	raw := livePlatformProbe(want)
	if len(raw) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(raw))
	for cwd := range raw {
		if normalized := normalizeLiveCwd(cwd); normalized != "" {
			out[normalized] = struct{}{}
		}
	}
	return out
}

// normalizeLiveCwd 规范化工作目录以便存活比较：
// 解析符号链接、去掉尾部分隔符、Windows 盘符小写。
func normalizeLiveCwd(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	if runtime.GOOS == "windows" && len(path) >= 2 && path[1] == ':' {
		path = strings.ToLower(path[:2]) + path[2:]
	}
	if len(path) > 1 {
		path = strings.TrimRight(path, "/\\")
	}
	return path
}

// cwdIsLive 判断会话记录的 cwd 是否命中某个正在运行进程的工作目录。
func cwdIsLive(cwd string, live map[string]struct{}) bool {
	if len(live) == 0 {
		return false
	}
	_, ok := live[normalizeLiveCwd(cwd)]
	return ok
}

// applySessionLiveness 用进程存活信息确认尚未结束的转录状态。
// 已完成或失败的转录是权威终态，不能被等待下一条输入的常驻 CLI 覆盖。
func applySessionLiveness(transcriptStatus string, live bool) string {
	if transcriptStatus != "in_progress" {
		return transcriptStatus
	}
	if live {
		return "in_progress"
	}
	return "interrupted"
}

type sessionLiveCandidate struct {
	UUID   string
	Cwd    string
	LastAt uint64
}

// freshestLiveSession 对每个命中存活进程 cwd 的目录，
// 只把最近更新的会话标记为运行中，避免同目录历史会话被误报。
func freshestLiveSession(candidates []sessionLiveCandidate, live map[string]struct{}) map[string]struct{} {
	running := make(map[string]struct{})
	if len(live) == 0 {
		return running
	}
	best := make(map[string]sessionLiveCandidate)
	for _, candidate := range candidates {
		normalized := normalizeLiveCwd(candidate.Cwd)
		if normalized == "" || !cwdIsLive(normalized, live) {
			continue
		}
		if current, ok := best[normalized]; !ok || candidate.LastAt > current.LastAt {
			best[normalized] = candidate
		}
	}
	for _, candidate := range best {
		running[candidate.UUID] = struct{}{}
	}
	return running
}
