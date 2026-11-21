package sessions

import (
	"os"
	"path/filepath"
	"strings"

	"mincode/app/backend/core"
)

// ─── 会话状态与进程存活的校正 ─────────────────────────────────────────────

// reconcileClaudeStatuses 用 claude 进程存活信息校正转录推断状态。
func reconcileClaudeStatuses(sessions []claudeSessionMeta) {
	live := liveSessionCwds("claude")
	candidates := make([]sessionLiveCandidate, 0, len(sessions))
	for _, s := range sessions {
		candidates = append(candidates, sessionLiveCandidate{UUID: s.UUID, Cwd: s.DecodedPath, LastAt: s.LastAt})
	}
	running := freshestLiveSession(candidates, live)
	for i := range sessions {
		_, isRunning := running[sessions[i].UUID]
		sessions[i].Status = applySessionLiveness(sessions[i].Status, isRunning)
	}
}

// aiCliProcessNames 把会话适配器标识映射到其 CLI 可执行文件基名，
// 与前端 buildNewSessionCommand 使用的命令保持一致。
var aiCliProcessNames = map[string]string{
	"pi":       "pi",
	"gemini":   "gemini",
	"qodercn":  "qoderclicn",
	"qoder":    "qodercli",
	"grok":     "grok",
	"kimi":     "kimi",
	"opencode": "opencode",
}

// reconcileCodexStatuses 用 codex 进程存活信息校正转录推断状态。
func reconcileCodexStatuses(sessions []codexSession) {
	reconcileSessionStatusesByProcess("codex", sessions)
}

// reconcileAiCliStatuses 为其余 AI CLI 适配器套用同一套进程存活校正；
// 未知适配器保持转录推断状态不变。
func reconcileAiCliStatuses(adapter string, sessions []codexSession) {
	if name, ok := aiCliProcessNames[strings.TrimSpace(adapter)]; ok {
		reconcileSessionStatusesByProcess(name, sessions)
	}
}

var liveCwdsForProcess = func(processName string) map[string]struct{} {
	return liveSessionCwds(processName)
}

// reconcileSessionStatusesByProcess 按可执行文件基名探测存活进程的 cwd，
// 再逐条校正共享 codexSession 结构的会话状态。
func reconcileSessionStatusesByProcess(processName string, sessions []codexSession) {
	live := liveCwdsForProcess(processName)
	candidates := make([]sessionLiveCandidate, 0, len(sessions))
	for _, s := range sessions {
		candidates = append(candidates, sessionLiveCandidate{UUID: s.ID, Cwd: s.Cwd, LastAt: s.LastAt})
	}
	running := freshestLiveSession(candidates, live)
	for i := range sessions {
		_, isRunning := running[sessions[i].ID]
		sessions[i].Status = applySessionLiveness(sessions[i].Status, isRunning)
	}
}

// sessionFileCwd 读取 Claude JSONL 转录中最早记录的 cwd 字段。
func sessionFileCwd(path string) string {
	return claudeCwdCache.get(path)
}

type claudeSessionStatusesArgs struct {
	EncodedDir string `json:"encodedDir"`
}

// claudeSessionStatusesHandler 返回某个项目目录下 uuid→状态 的最新映射，
// 供前端轮询侧边栏状态圆点，避免整列表重载。
func claudeSessionStatusesHandler(_ string, argsJson string) (string, error) {
	var args claudeSessionStatusesArgs
	if err := core.DecodeArgs(argsJson, &args); err != nil {
		return "", err
	}
	dir, err := resolveClaudeProjectDir(args.EncodedDir)
	if err != nil {
		return core.EncodeResult(map[string]string{})
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return core.EncodeResult(map[string]string{})
	}
	statuses := make(map[string]string, len(entries))
	candidates := make([]sessionLiveCandidate, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".jsonl" {
			continue
		}
		uuid := strings.TrimSuffix(e.Name(), ".jsonl")
		path := filepath.Join(dir, e.Name())
		statuses[uuid] = claudeStatusCache.get(path)
		candidates = append(candidates, sessionLiveCandidate{
			UUID:   uuid,
			Cwd:    sessionFileCwd(path),
			LastAt: fileMtimeMs(path),
		})
	}
	running := freshestLiveSession(candidates, liveSessionCwds("claude"))
	for uuid, transcript := range statuses {
		_, isRunning := running[uuid]
		statuses[uuid] = applySessionLiveness(transcript, isRunning)
	}
	return core.EncodeResult(statuses)
}

// codexSessionStatusesHandler 返回 codex 会话 id→状态 的最新映射，供前端轮询状态圆点。
func codexSessionStatusesHandler(_ string, _ string) (string, error) {
	codexDir, ok := codexHomeDir()
	if !ok {
		return core.EncodeResult(map[string]string{})
	}
	sessionsDir := filepath.Join(codexDir, "sessions")
	if info, err := os.Stat(sessionsDir); err != nil || !info.IsDir() {
		return core.EncodeResult(map[string]string{})
	}
	sessions := codexSessionsCache.list(sessionsDir)
	statuses := make(map[string]string, len(sessions))
	candidates := make([]sessionLiveCandidate, 0, len(sessions))
	for _, s := range sessions {
		statuses[s.ID] = s.Status
		candidates = append(candidates, sessionLiveCandidate{UUID: s.ID, Cwd: s.Cwd, LastAt: s.LastAt})
	}
	running := freshestLiveSession(candidates, liveSessionCwds("codex"))
	for id, transcript := range statuses {
		_, isRunning := running[id]
		statuses[id] = applySessionLiveness(transcript, isRunning)
	}
	return core.EncodeResult(statuses)
}
