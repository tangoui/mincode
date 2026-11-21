package sessions

// AI 会话浏览域：Claude Code 与 Codex CLI 的本地会话文件读取。
// 对齐 Tauri src-tauri/src/terminal/system_info.rs 的 7 个命令。
// 说明：list_ai_cli_sessions/get_ai_cli_session_messages/delete_ai_cli_session（Gemini/Qwen/Kimi/OpenCode）
// 依赖 SQLite（rusqlite），go.mod 暂无对应驱动，另作后续子任务，不在此文件实现。

import (
	"mincode/app/backend/core"

	"bufio"
	"encoding/json"

	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

func init() {
	core.Register("list_claude_projects", listClaudeProjectsHandler)
	core.Register("get_project_sessions", getProjectSessionsHandler)
	core.Register("claude_session_statuses", claudeSessionStatusesHandler)
	core.Register("get_session_messages", getSessionMessagesHandler)
	core.Register("delete_claude_session", deleteClaudeSessionHandler)
	core.Register("rename_claude_session", renameClaudeSessionHandler)
	core.Register("list_codex_sessions", listCodexSessionsHandler)
	core.Register("codex_session_statuses", codexSessionStatusesHandler)
	core.Register("get_codex_session_messages", getCodexSessionMessagesHandler)
	core.Register("delete_codex_session", deleteCodexSessionHandler)
}

// ─── 数据结构（camelCase，对齐前端）──────────────────────────────────────────

type claudeProject struct {
	EncodedDir   string `json:"encodedDir"`
	DecodedPath  string `json:"decodedPath"`
	SessionCount int    `json:"sessionCount"`
	LastAt       uint64 `json:"lastAt"`
}

type claudeSessionMeta struct {
	UUID         string `json:"uuid"`
	EncodedDir   string `json:"encodedDir"`
	DecodedPath  string `json:"decodedPath"`
	Title        string `json:"title"`
	LastAt       uint64 `json:"lastAt"`
	MessageCount uint32 `json:"messageCount"`
	Model        string `json:"model"`
	Status       string `json:"status"`
}

type claudeSessionMessage struct {
	Role            string  `json:"role"`
	Content         string  `json:"content"`
	Model           *string `json:"model"`
	InputTokens     *uint64 `json:"inputTokens"`
	OutputTokens    *uint64 `json:"outputTokens"`
	CacheReadTokens *uint64 `json:"cacheReadTokens"`
	DurationMs      *uint64 `json:"durationMs"`
	Timestamp       *string `json:"timestamp"`
	MsgType         string  `json:"msgType"`
}

type codexSession struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	LastAt       uint64 `json:"lastAt"`
	MessageCount uint32 `json:"messageCount"`
	Model        string `json:"model"`
	Cwd          string `json:"cwd"`
	FilePath     string `json:"filePath"`
	Status       string `json:"status"`
}

type codexSessionCacheEntry struct {
	size            int64
	modTimeUnixNano int64
	session         codexSession
	valid           bool
}

type codexSessionMetadataCache struct {
	mu      sync.Mutex
	entries map[string]codexSessionCacheEntry
}

var codexSessionsCache = newCodexSessionMetadataCache()

func newCodexSessionMetadataCache() *codexSessionMetadataCache {
	return &codexSessionMetadataCache{entries: make(map[string]codexSessionCacheEntry)}
}

// ─── 通用辅助 ────────────────────────────────────────────────────────────────

func sessionHomeDir() (string, bool) {
	if v := os.Getenv("USERPROFILE"); v != "" {
		return v, true
	}
	if v := os.Getenv("HOME"); v != "" {
		return v, true
	}
	return "", false
}

// claudeProjectsDir 优先使用用户覆盖目录，其次 CLAUDE_CONFIG_DIR，最后 ~/.claude。
func claudeProjectsDir() (string, bool) {
	if dir, ok := sessionRootOverride("claude"); ok {
		return filepath.Join(dir, "projects"), true
	}
	if dir, ok := sessionRootFromEnv("CLAUDE_CONFIG_DIR"); ok {
		return filepath.Join(dir, "projects"), true
	}
	home, ok := sessionHomeDir()
	if !ok {
		return "", false
	}
	return filepath.Join(home, ".claude", "projects"), true
}

func resolveClaudeProjectDir(encodedDir string) (string, error) {
	projectsDir, ok := claudeProjectsDir()
	if !ok {
		return "", core.ErrString("无法定位 ~/.claude/projects 目录")
	}
	if isUnsafeSegment(encodedDir) {
		return "", core.ErrString("非法的会话标识")
	}
	projectsRoot, err := filepath.EvalSymlinks(projectsDir)
	if err != nil {
		return "", core.ErrString("无法定位 Claude 项目目录：" + err.Error())
	}
	target, err := filepath.EvalSymlinks(filepath.Join(projectsDir, encodedDir))
	if err != nil {
		return "", core.ErrString("会话项目目录不存在：" + err.Error())
	}
	if !isUnder(target, projectsRoot) {
		return "", core.ErrString("只能操作 Claude projects 目录内的会话")
	}
	return target, nil
}

func resolveClaudeSessionFile(encodedDir, uuid string) (string, error) {
	if !isSafeClaudeSessionFileSegment(uuid) {
		return "", core.ErrString("非法的会话标识")
	}
	dir, err := resolveClaudeProjectDir(encodedDir)
	if err != nil {
		return "", err
	}
	target, err := filepath.EvalSymlinks(filepath.Join(dir, uuid+".jsonl"))
	if err != nil {
		return "", core.ErrString("会话文件不存在：" + err.Error())
	}
	if !isUnder(target, dir) || filepath.Ext(target) != ".jsonl" {
		return "", core.ErrString("只能操作项目目录内的 .jsonl 会话文件")
	}
	return target, nil
}

// codexHomeDir 优先使用用户覆盖目录，其次 CODEX_HOME，最后 ~/.codex。
func codexHomeDir() (string, bool) {
	if dir, ok := sessionRootOverride("codex"); ok {
		return dir, true
	}
	if dir, ok := sessionRootFromEnv("CODEX_HOME"); ok {
		return dir, true
	}
	home, ok := sessionHomeDir()
	if !ok {
		return "", false
	}
	return filepath.Join(home, ".codex"), true
}

func fileMtimeMs(path string) uint64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return uint64(info.ModTime().UnixMilli())
}

// percentDecode 对齐 Rust percent_decode（仅解 %XX，非法序列保留原字节）。
func percentDecode(s string) string {
	src := []byte(s)
	out := make([]byte, 0, len(src))
	hex := func(b byte) (byte, bool) {
		switch {
		case b >= '0' && b <= '9':
			return b - '0', true
		case b >= 'a' && b <= 'f':
			return b - 'a' + 10, true
		case b >= 'A' && b <= 'F':
			return b - 'A' + 10, true
		}
		return 0, false
	}
	i := 0
	for i < len(src) {
		if src[i] == '%' && i+2 < len(src) {
			if h, ok1 := hex(src[i+1]); ok1 {
				if l, ok2 := hex(src[i+2]); ok2 {
					out = append(out, h*16+l)
					i += 3
					continue
				}
			}
		}
		out = append(out, src[i])
		i++
	}
	return string(out)
}

// claudeProjectDirFallback 对齐 Rust：Windows 处理 X-- 盘符前缀，否则 '-' → '/'。
func claudeProjectDirFallback(encodedDir string) string {
	decoded := percentDecode(encodedDir)
	if runtime.GOOS == "windows" {
		b := []byte(decoded)
		if len(b) >= 3 && isASCIIAlpha(b[0]) && b[1] == '-' && b[2] == '-' {
			drive := decoded[:1]
			rest := strings.ReplaceAll(decoded[3:], "-", "\\")
			return drive + ":\\" + rest
		}
	}
	return strings.ReplaceAll(decoded, "-", "/")
}

func isASCIIAlpha(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// readJsonlPrefixAndCount 一次顺序读取中提取前缀、统计非空行数，并记下最后一条真人输入。
// 这一趟本来就要读完整个文件来数行数，顺手取标题不额外增加 IO；末尾的用户条目绝大多数
// 是工具结果，先用字节判断挡掉，只有疑似真人输入的少数几行才值得 JSON 解析。
func readJsonlPrefixAndCount(path string, maxBytes int, sizeHint int64) (string, string, uint32) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", 0
	}
	defer f.Close()

	reader := bufio.NewReaderSize(f, 64*1024)
	// stat 的大小可能已经过时，只用于预分配，读取和前缀截取仍按原上限处理。
	capacity := maxBytes
	if sizeHint >= 0 && sizeHint < int64(maxBytes) {
		capacity = int(sizeHint)
	}
	prefix := make([]byte, 0, capacity)
	lastPrompt := ""
	var count uint32
	for {
		line, readErr := reader.ReadString('\n')
		if len(line) > 0 {
			if strings.TrimSpace(line) != "" {
				count++
			}
			if looksLikeHumanPromptLine(line) {
				if title := claudePromptTitle(line); title != "" {
					lastPrompt = title
				}
			}
			if len(prefix) < maxBytes {
				remaining := maxBytes - len(prefix)
				if len(line) > remaining {
					line = line[:remaining]
				}
				if len(line) > cap(prefix)-len(prefix) {
					grown := make([]byte, len(prefix), maxBytes)
					copy(grown, prefix)
					prefix = grown
				}
				prefix = append(prefix, line...)
			}
		}
		if readErr != nil {
			break
		}
	}
	return string(prefix), lastPrompt, count
}

// looksLikeHumanPromptLine 是便宜的预筛：会话动辄上万行，逐行 JSON 解析太贵，
// 而工具结果、系统注入条目都带着固定标记，用子串就能先排掉。
func looksLikeHumanPromptLine(line string) bool {
	if !strings.Contains(line, `"type":"user"`) {
		return false
	}
	for _, marker := range []string{`"toolUseResult"`, `"tool_result"`, `"isMeta":true`, `"isSidechain":true`} {
		if strings.Contains(line, marker) {
			return false
		}
	}
	return true
}

// claudePromptTitle 解析一行 JSONL，返回它作为标题时该显示的文本。
func claudePromptTitle(line string) string {
	var v map[string]json.RawMessage
	if json.Unmarshal([]byte(line), &v) != nil {
		return ""
	}
	return claudePromptTitleFromEntry(v)
}

// claudePromptTitleFromEntry 只认真人发出的那句话：工具结果、子代理转录、
// Claude 自己塞进对话的包装条目都不能当标题，否则一列会话全叫同一个名字。
func claudePromptTitleFromEntry(v map[string]json.RawMessage) string {
	if rawString(v, "type") != "user" || rawBool(v, "isMeta") || rawBool(v, "isSidechain") {
		return ""
	}
	if _, isToolResult := v["toolUseResult"]; isToolResult {
		return ""
	}
	msgRaw, ok := v["message"]
	if !ok {
		return ""
	}
	var msg map[string]json.RawMessage
	if json.Unmarshal(msgRaw, &msg) != nil {
		return ""
	}
	content, ok := msg["content"]
	if !ok {
		return ""
	}
	text := humanPromptText(content)
	if isSyntheticPromptText(text) {
		return ""
	}
	return firstSentenceTitle(text, 100)
}

// humanPromptText 只取用户真正打出来的文本：字符串 content 或 type=text 的 block。
// 这里不能复用 extractContentText——它会把 tool_result 也拼进来。
func humanPromptText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(content, &s) == nil {
		return strings.TrimSpace(s)
	}
	var arr []map[string]json.RawMessage
	if json.Unmarshal(content, &arr) != nil {
		return ""
	}
	var parts []string
	for _, block := range arr {
		if rawString(block, "type") != "text" {
			continue
		}
		var txt string
		if raw, ok := block["text"]; ok {
			_ = json.Unmarshal(raw, &txt)
		}
		parts = appendNonEmptyText(parts, txt)
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// isSyntheticPromptText 认出 Claude 写进转录、但用户没有发过的包装条目。
func isSyntheticPromptText(text string) bool {
	trimmed := strings.TrimLeft(text, " \t\r\n")
	if trimmed == "" {
		return true
	}
	for _, prefix := range []string{
		"<local-command-",
		"<command-name>",
		"<command-message>",
		"<command-args>",
		"<environment_context>",
		"<system-reminder>",
		"<task-notification>",
		// 上下文压缩后 Claude 自己续写的那条，用户并没有发过。
		"This session is being continued from a previous conversation",
	} {
		if strings.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return false
}

// countFileLines 统计非空行数。
func countFileLines(path string) uint32 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var count uint32
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) != "" {
			count++
		}
	}
	if err := scanner.Err(); err != nil {
		return count
	}
	return count
}

// firstSentenceTitle 对齐 Rust system_info.rs 版本：压缩空白后逐字符取到句末标点或 maxChars。
func firstSentenceTitle(text string, maxChars int) string {
	compact := strings.Join(strings.Fields(text), " ")
	trimmed := strings.TrimSpace(compact)
	if trimmed == "" {
		return ""
	}
	var title []rune
	for _, ch := range trimmed {
		title = append(title, ch)
		if len(title) >= maxChars || isSentenceEnd(ch) {
			break
		}
	}
	return string(title)
}

func isSentenceEnd(ch rune) bool {
	switch ch {
	case '.', '?', '!', '。', '？', '！':
		return true
	}
	return false
}

func isEnvironmentContextText(text string) bool {
	return strings.HasPrefix(strings.TrimLeft(text, " \t\r\n"), "<environment_context>")
}

// extractContentText 从 content 字段提取纯文本（string 或 blocks 数组）。
func extractContentText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(content, &s); err == nil {
		return s
	}
	var arr []map[string]json.RawMessage
	if err := json.Unmarshal(content, &arr); err != nil {
		return ""
	}
	var parts []string
	for _, block := range arr {
		var t string
		if raw, ok := block["type"]; ok {
			_ = json.Unmarshal(raw, &t)
		}
		switch t {
		case "text":
			var txt string
			if raw, ok := block["text"]; ok {
				_ = json.Unmarshal(raw, &txt)
			}
			parts = appendNonEmptyText(parts, txt)
		case "tool_use":
			// Claude records TodoWrite as a tool call rather than assistant text.
			// Preserve a concise, human-readable version so session summaries can
			// identify completed and pending work instead of seeing an empty turn.
			parts = appendNonEmptyText(parts, describeClaudeToolUse(block))
		case "tool_result":
			if inner, ok := block["content"]; ok {
				parts = appendNonEmptyText(parts, extractContentText(inner))
			}
		}
	}
	return strings.Join(parts, "\n")
}

func appendNonEmptyText(parts []string, value string) []string {
	if value = strings.TrimSpace(value); value != "" {
		return append(parts, value)
	}
	return parts
}

func describeClaudeToolUse(block map[string]json.RawMessage) string {
	name := strings.TrimSpace(rawString(block, "name"))
	if name == "" {
		return ""
	}
	if name != "TodoWrite" {
		return "[Tool: " + name + "]"
	}

	input, ok := block["input"]
	if !ok {
		return "[Tool: TodoWrite]"
	}
	var payload struct {
		Todos []struct {
			Content    string `json:"content"`
			ActiveForm string `json:"activeForm"`
			Status     string `json:"status"`
		} `json:"todos"`
	}
	if err := json.Unmarshal(input, &payload); err != nil || len(payload.Todos) == 0 {
		return "[Tool: TodoWrite]"
	}

	items := make([]string, 0, len(payload.Todos))
	for _, todo := range payload.Todos {
		content := strings.TrimSpace(todo.Content)
		if content == "" {
			content = strings.TrimSpace(todo.ActiveForm)
		}
		if content == "" {
			continue
		}
		status := strings.TrimSpace(todo.Status)
		if status == "" {
			status = "unknown"
		}
		items = append(items, "- ["+status+"] "+content)
	}
	if len(items) == 0 {
		return "[Tool: TodoWrite]"
	}
	return "[TodoWrite]\n" + strings.Join(items, "\n")
}

// extractCodexContent 从 Codex content 数组提取纯文本（input_text/output_text/text）。
func extractCodexContent(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(content, &s); err == nil {
		return s
	}
	var arr []map[string]json.RawMessage
	if err := json.Unmarshal(content, &arr); err != nil {
		return ""
	}
	var parts []string
	for _, block := range arr {
		var t string
		if raw, ok := block["type"]; ok {
			_ = json.Unmarshal(raw, &t)
		}
		switch t {
		case "input_text", "output_text", "text":
			var txt string
			if raw, ok := block["text"]; ok {
				_ = json.Unmarshal(raw, &txt)
			}
			parts = append(parts, txt)
		}
	}
	return strings.Join(parts, "\n")
}

// jsonLines 逐行迭代 JSONL 文件内容（跳过空行、解析失败行）。
func jsonLines(content string, fn func(v map[string]json.RawMessage)) {
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var v map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			continue
		}
		fn(v)
	}
}

func rawString(m map[string]json.RawMessage, key string) string {
	raw, ok := m[key]
	if !ok {
		return ""
	}
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

func rawBool(m map[string]json.RawMessage, key string) bool {
	raw, ok := m[key]
	if !ok {
		return false
	}
	var b bool
	_ = json.Unmarshal(raw, &b)
	return b
}

// ─── 会话状态推断 ─────────────────────────────────────────────────────────────

// readJsonlTail 读取文件末尾最多 maxBytes 字节，返回最后若干完整行。
func readJsonlTail(path string, maxBytes int) []string {
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	size := info.Size()
	if size == 0 {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	offset := size - int64(maxBytes)
	if offset < 0 {
		offset = 0
	}
	if _, err := f.Seek(offset, 0); err != nil {
		return nil
	}
	buf := make([]byte, size-offset)
	n, _ := f.Read(buf)
	lines := strings.Split(string(buf[:n]), "\n")
	// 如果 offset > 0，第一行可能不完整，丢弃。
	if offset > 0 && len(lines) > 1 {
		lines = lines[1:]
	}
	// 只保留最后 30 行非空行即可。
	var tail []string
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			tail = append(tail, l)
		}
	}
	if len(tail) > 30 {
		tail = tail[len(tail)-30:]
	}
	return tail
}

// inferClaudeSessionStatus 从 Claude Code JSONL 文件尾部推断会话状态。
// 返回 "completed"、"failed" 或 ""（无法判断）。
func inferClaudeSessionStatus(path string) string {
	tail := readJsonlTail(path, 32*1024)
	if len(tail) == 0 {
		return ""
	}
	// 从最后一行向前扫描，找到第一个有 type 字段的条目。
	for i := len(tail) - 1; i >= 0; i-- {
		var v map[string]json.RawMessage
		if json.Unmarshal([]byte(tail[i]), &v) != nil {
			continue
		}
		lineType := rawString(v, "type")
		if lineType == "" {
			continue
		}
		switch lineType {
		case "assistant":
			// 检查是否有错误标记
			if msgRaw, ok := v["message"]; ok {
				var msg map[string]json.RawMessage
				if json.Unmarshal(msgRaw, &msg) == nil {
					if rawString(msg, "stop_reason") == "error" {
						return "failed"
					}
				}
			}
			return "completed"
		case "user":
			return "in_progress"
		case "summary", "result":
			return "completed"
		case "system":
			continue
		default:
			// 尝试从 message.role 推断
			if msgRaw, ok := v["message"]; ok {
				var msg map[string]json.RawMessage
				if json.Unmarshal(msgRaw, &msg) == nil {
					switch rawString(msg, "role") {
					case "assistant":
						return "completed"
					case "user":
						return "in_progress"
					}
				}
			}
			continue
		}
	}
	return ""
}

// inferCodexSessionStatus 从 Codex CLI JSONL 文件尾部推断会话状态。
func inferCodexSessionStatus(path string) string {
	tail := readJsonlTail(path, 32*1024)
	if len(tail) == 0 {
		return ""
	}
	for i := len(tail) - 1; i >= 0; i-- {
		var v map[string]json.RawMessage
		if json.Unmarshal([]byte(tail[i]), &v) != nil {
			continue
		}
		lineType := rawString(v, "type")
		if lineType != "response_item" {
			continue
		}
		var payload map[string]json.RawMessage
		if praw, ok := v["payload"]; ok {
			_ = json.Unmarshal(praw, &payload)
		}
		if payload == nil {
			continue
		}
		role := rawString(payload, "role")
		switch role {
		case "assistant":
			if rawString(payload, "status") == "failed" {
				return "failed"
			}
			return "completed"
		case "user":
			return "in_progress"
		}
	}
	return ""
}

// ─── Claude Code ─────────────────────────────────────────────────────────────

func listClaudeProjectsHandler(_ string, _ string) (string, error) {
	projectsDir, ok := claudeProjectsDir()
	if !ok {
		return core.EncodeResult([]claudeProject{})
	}
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return core.EncodeResult([]claudeProject{})
	}
	projects := make([]claudeProject, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		encodedDir := e.Name()
		dirPath := filepath.Join(projectsDir, encodedDir)
		sessionEntries, err := os.ReadDir(dirPath)
		if err != nil {
			continue
		}
		sessionCount := 0
		var lastAt uint64
		for _, se := range sessionEntries {
			if se.IsDir() || filepath.Ext(se.Name()) != ".jsonl" {
				continue
			}
			sessionCount++
			ms := fileMtimeMs(filepath.Join(dirPath, se.Name()))
			if ms > lastAt {
				lastAt = ms
			}
		}
		if sessionCount == 0 {
			continue
		}
		projects = append(projects, claudeProject{
			EncodedDir:   encodedDir,
			DecodedPath:  claudeProjectDirFallback(encodedDir),
			SessionCount: sessionCount,
			LastAt:       lastAt,
		})
	}
	sort.SliceStable(projects, func(i, j int) bool {
		return projects[i].LastAt > projects[j].LastAt
	})
	return core.EncodeResult(projects)
}

type getProjectSessionsArgs struct {
	EncodedDir string `json:"encodedDir"`
}

func getProjectSessionsHandler(_ string, argsJson string) (string, error) {
	var args getProjectSessionsArgs
	if err := core.DecodeArgs(argsJson, &args); err != nil {
		return "", err
	}
	dir, err := resolveClaudeProjectDir(args.EncodedDir)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return core.EncodeResult([]claudeSessionMeta{})
	}
	fallback := claudeProjectDirFallback(args.EncodedDir)
	titleOverrides, err := claudeTitles.list(args.EncodedDir)
	if err != nil {
		return "", core.ErrString("读取 Claude 会话标题失败：" + err.Error())
	}
	sessions := make([]claudeSessionMeta, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".jsonl" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		uuid := strings.TrimSuffix(e.Name(), ".jsonl")
		var lastAt uint64
		var stamp fileStamp
		// 与原来的路径 stat 一样跟随符号链接，同一份元数据用于列表和两个缓存。
		info, statErr := os.Stat(path)
		if statErr == nil {
			lastAt = uint64(info.ModTime().UnixMilli())
			stamp = fileStampFromInfo(info)
		}

		summary := claudeSummaryCache.getWithStamp(path, stamp, statErr == nil)
		title := summary.FirstTitle
		lastPrompt := summary.LastPrompt
		decodedPath := summary.decodedPath(fallback)

		// 会话越长，开头那句话越不能代表它现在在做什么，所以标题优先用最后一条真人输入；
		// 整段转录里一条真人输入都没有时（比如只跑过斜杠命令），才退回开头那句。
		if lastPrompt != "" {
			title = lastPrompt
		}
		if override := strings.TrimSpace(titleOverrides[uuid]); override != "" {
			title = override
		}
		if title == "" {
			title = core.TakeChars(uuid, 8) + "..."
		}

		sessions = append(sessions, claudeSessionMeta{
			UUID:         uuid,
			EncodedDir:   args.EncodedDir,
			DecodedPath:  decodedPath,
			Title:        title,
			LastAt:       lastAt,
			MessageCount: summary.MessageCount,
			Model:        summary.Model,
			Status:       claudeStatusCache.getWithStamp(path, stamp, statErr == nil),
		})
	}
	reconcileClaudeStatuses(sessions)
	sort.SliceStable(sessions, func(i, j int) bool {
		return sessions[i].LastAt > sessions[j].LastAt
	})
	return core.EncodeResult(sessions)
}

type getSessionMessagesArgs struct {
	EncodedDir string  `json:"encodedDir"`
	UUID       string  `json:"uuid"`
	Limit      *uint32 `json:"limit"`
}

func getSessionMessagesHandler(_ string, argsJson string) (string, error) {
	var args getSessionMessagesArgs
	if err := core.DecodeArgs(argsJson, &args); err != nil {
		return "", err
	}
	path, err := resolveClaudeSessionFile(args.EncodedDir, args.UUID)
	if err != nil {
		return "", err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return core.EncodeResult([]claudeSessionMessage{})
	}

	maxN := 200
	if args.Limit != nil {
		maxN = int(*args.Limit)
	}
	messages := make([]claudeSessionMessage, 0)

	jsonLines(string(content), func(v map[string]json.RawMessage) {
		msgType := rawString(v, "type")
		if msgType == "system" || msgType == "summary" {
			return
		}
		var timestamp *string
		if ts := rawString(v, "timestamp"); ts != "" {
			timestamp = &ts
		}

		var msg map[string]json.RawMessage
		if msgRaw, ok := v["message"]; ok {
			_ = json.Unmarshal(msgRaw, &msg)
		}

		role := msgType
		if r := rawString(msg, "role"); r != "" {
			role = r
		}

		contentText := ""
		if c, ok := msg["content"]; ok {
			contentText = extractContentText(c)
		}

		var model *string
		if m := rawString(msg, "model"); m != "" {
			model = &m
		}

		var inputTokens, outputTokens, cacheReadTokens *uint64
		if usageRaw, ok := msg["usage"]; ok {
			var usage map[string]json.RawMessage
			if json.Unmarshal(usageRaw, &usage) == nil {
				inputTokens = rawUint64Ptr(usage, "input_tokens")
				outputTokens = rawUint64Ptr(usage, "output_tokens")
				cacheReadTokens = rawUint64Ptr(usage, "cache_read_input_tokens")
			}
		}

		durationMs := rawUint64Ptr(v, "durationMs")
		if durationMs == nil {
			durationMs = rawUint64Ptr(v, "duration_ms")
		}

		if contentText == "" && role == "" {
			return
		}

		messages = append(messages, claudeSessionMessage{
			Role:            role,
			Content:         contentText,
			Model:           model,
			InputTokens:     inputTokens,
			OutputTokens:    outputTokens,
			CacheReadTokens: cacheReadTokens,
			DurationMs:      durationMs,
			Timestamp:       timestamp,
			MsgType:         msgType,
		})
	})

	if len(messages) > maxN {
		messages = messages[len(messages)-maxN:]
	}
	return core.EncodeResult(messages)
}

func rawUint64Ptr(m map[string]json.RawMessage, key string) *uint64 {
	raw, ok := m[key]
	if !ok {
		return nil
	}
	var n uint64
	if err := json.Unmarshal(raw, &n); err != nil {
		return nil
	}
	return &n
}

type deleteClaudeSessionArgs struct {
	EncodedDir string `json:"encodedDir"`
	UUID       string `json:"uuid"`
}

func deleteClaudeSessionHandler(_ string, argsJson string) (string, error) {
	var args deleteClaudeSessionArgs
	if err := core.DecodeArgs(argsJson, &args); err != nil {
		return "", err
	}
	target, err := resolveClaudeSessionFile(args.EncodedDir, args.UUID)
	if err != nil {
		return "", err
	}
	if err := os.Remove(target); err != nil {
		return "", core.ErrString("删除失败：" + err.Error())
	}

	// 成功删除 transcript 后 best-effort 清理标题，存储故障不能反转删除结果。
	_ = claudeTitles.delete(args.EncodedDir, args.UUID)
	return core.EncodeResult(nil)
}

type renameClaudeSessionArgs struct {
	EncodedDir string `json:"encodedDir"`
	UUID       string `json:"uuid"`
	Title      string `json:"title"`
}

func renameClaudeSessionHandler(_ string, argsJson string) (string, error) {
	var args renameClaudeSessionArgs
	if err := core.DecodeArgs(argsJson, &args); err != nil {
		return "", err
	}
	title, err := validateClaudeSessionTitle(args.Title)
	if err != nil {
		return "", core.ErrString(err.Error())
	}
	if _, err := resolveClaudeSessionFile(args.EncodedDir, args.UUID); err != nil {
		return "", err
	}
	if err := claudeTitles.upsert(args.EncodedDir, args.UUID, title); err != nil {
		return "", core.ErrString("重命名 Claude 会话失败：" + err.Error())
	}
	return core.EncodeResult(title)
}

// ─── Codex CLI ───────────────────────────────────────────────────────────────

func parseCodexSession(path string, lastAt uint64) (*codexSession, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}

	sessionID := ""
	title := ""
	model := ""
	cwd := ""
	var messageCount uint32

	jsonLines(string(data), func(v map[string]json.RawMessage) {
		lineType := rawString(v, "type")
		var payload map[string]json.RawMessage
		if praw, ok := v["payload"]; ok {
			_ = json.Unmarshal(praw, &payload)
		}
		switch lineType {
		case "session_meta":
			if sessionID == "" {
				if id := rawString(payload, "id"); id != "" {
					sessionID = id
				}
			}
			if model == "" {
				if m := rawString(payload, "model"); m != "" {
					model = m
				}
			}
			if cwd == "" {
				if c := rawString(payload, "cwd"); c != "" {
					cwd = c
				}
			}
		case "turn_context":
			if cwd == "" {
				if c := rawString(payload, "cwd"); c != "" {
					cwd = c
				} else if c := rawString(v, "cwd"); c != "" {
					cwd = c
				}
			}
		case "response_item":
			if payload == nil {
				return
			}
			role := rawString(payload, "role")
			if role != "user" && role != "assistant" {
				return
			}
			if role == "user" {
				if c, ok := payload["content"]; ok {
					if isEnvironmentContextText(extractCodexContent(c)) {
						return
					}
				}
			}
			messageCount++
			if title == "" && role == "user" {
				if c, ok := payload["content"]; ok {
					title = firstSentenceTitle(extractCodexContent(c), 100)
				}
			}
			if model == "" && role == "assistant" {
				if m := rawString(payload, "model"); m != "" {
					model = m
				}
			}
		}
	})

	if messageCount == 0 {
		return nil, false
	}
	if sessionID == "" {
		sessionID = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	if title == "" {
		title = core.TakeChars(sessionID, 8) + "…"
	}

	return &codexSession{
		ID:           sessionID,
		Title:        title,
		LastAt:       lastAt,
		MessageCount: messageCount,
		Model:        model,
		Cwd:          cwd,
		FilePath:     path,
		Status:       inferCodexSessionStatus(path),
	}, true
}

func (cache *codexSessionMetadataCache) list(dir string) []codexSession {
	cache.mu.Lock()
	defer cache.mu.Unlock()

	seen := make(map[string]struct{}, len(cache.entries))
	sessions := cache.collect(dir, 0, seen)
	for path := range cache.entries {
		if _, ok := seen[path]; !ok {
			delete(cache.entries, path)
		}
	}
	return sessions
}

func (cache *codexSessionMetadataCache) collect(
	dir string,
	depth int,
	seen map[string]struct{},
) []codexSession {
	if depth > 4 {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var sessions []codexSession
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		if entry.IsDir() {
			sessions = append(sessions, cache.collect(path, depth+1, seen)...)
			continue
		}
		if filepath.Ext(entry.Name()) != ".jsonl" || entry.Name() == "history.jsonl" {
			continue
		}

		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		seen[path] = struct{}{}
		modTimeUnixNano := info.ModTime().UnixNano()
		if cached, ok := cache.entries[path]; ok &&
			cached.size == info.Size() &&
			cached.modTimeUnixNano == modTimeUnixNano {
			if cached.valid {
				sessions = append(sessions, cached.session)
			}
			continue
		}

		session, valid := parseCodexSession(path, uint64(info.ModTime().UnixMilli()))
		cache.entries[path] = codexSessionCacheEntry{
			size:            info.Size(),
			modTimeUnixNano: modTimeUnixNano,
			valid:           valid,
		}
		if valid {
			cacheEntry := cache.entries[path]
			cacheEntry.session = *session
			cache.entries[path] = cacheEntry
			sessions = append(sessions, *session)
		}
	}
	return sessions
}

func (cache *codexSessionMetadataCache) clear() {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	clear(cache.entries)
}

func (cache *codexSessionMetadataCache) remove(path string) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	delete(cache.entries, path)
}

func listCodexSessionsHandler(_ string, _ string) (string, error) {
	codexDir, ok := codexHomeDir()
	if !ok {
		return core.EncodeResult([]codexSession{})
	}
	sessionsDir := filepath.Join(codexDir, "sessions")
	if info, err := os.Stat(sessionsDir); err != nil || !info.IsDir() {
		codexSessionsCache.clear()
		return core.EncodeResult([]codexSession{})
	}
	sessions := codexSessionsCache.list(sessionsDir)
	reconcileCodexStatuses(sessions)
	sort.SliceStable(sessions, func(i, j int) bool {
		return sessions[i].LastAt > sessions[j].LastAt
	})
	if sessions == nil {
		sessions = []codexSession{}
	}
	return core.EncodeResult(sessions)
}

type deleteCodexSessionArgs struct {
	FilePath string `json:"filePath"`
}

func resolveCodexSessionFile(filePath string) (string, error) {
	codexDir, ok := codexHomeDir()
	if !ok {
		return "", core.ErrString("无法定位 ~/.codex 目录")
	}
	sessionsRoot, err := filepath.EvalSymlinks(filepath.Join(codexDir, "sessions"))
	if err != nil {
		return "", core.ErrString("无法定位 Codex 会话目录：" + err.Error())
	}
	target, err := filepath.EvalSymlinks(filePath)
	if err != nil {
		return "", core.ErrString("会话文件不存在：" + err.Error())
	}
	if !isUnder(target, sessionsRoot) || filepath.Ext(target) != ".jsonl" || filepath.Base(target) == "history.jsonl" {
		return "", core.ErrString("只能读取或删除 Codex sessions 目录内的非 history .jsonl 会话文件")
	}
	return target, nil
}

func deleteCodexSessionHandler(_ string, argsJson string) (string, error) {
	var args deleteCodexSessionArgs
	if err := core.DecodeArgs(argsJson, &args); err != nil {
		return "", err
	}
	target, err := resolveCodexSessionFile(args.FilePath)
	if err != nil {
		return "", err
	}
	if err := os.Remove(target); err != nil {
		return "", core.ErrString("删除失败：" + err.Error())
	}
	codexSessionsCache.remove(target)
	return core.EncodeResult(nil)
}

func getCodexSessionMessagesHandler(_ string, argsJson string) (string, error) {
	var args deleteCodexSessionArgs // 复用 {filePath}
	if err := core.DecodeArgs(argsJson, &args); err != nil {
		return "", err
	}
	path, err := resolveCodexSessionFile(args.FilePath)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", core.ErrString("读取会话文件失败：" + err.Error())
	}
	messages := make([]claudeSessionMessage, 0)

	jsonLines(string(data), func(v map[string]json.RawMessage) {
		if rawString(v, "type") != "response_item" {
			return
		}
		payloadRaw, ok := v["payload"]
		if !ok {
			return
		}
		var payload map[string]json.RawMessage
		if json.Unmarshal(payloadRaw, &payload) != nil {
			return
		}
		if rawString(payload, "type") != "message" {
			return
		}
		role := rawString(payload, "role")
		if role == "developer" || role == "system" || (role != "user" && role != "assistant") {
			return
		}
		contentText := ""
		if c, ok := payload["content"]; ok {
			contentText = extractCodexContent(c)
		}
		if strings.TrimSpace(contentText) == "" {
			return
		}
		if role == "user" && isEnvironmentContextText(contentText) {
			return
		}
		var timestamp *string
		if ts := rawString(v, "timestamp"); ts != "" {
			timestamp = &ts
		}
		messages = append(messages, claudeSessionMessage{
			Role:      role,
			Content:   contentText,
			Timestamp: timestamp,
			MsgType:   "message",
		})
	})

	return core.EncodeResult(messages)
}

// ─── 路径安全辅助 ────────────────────────────────────────────────────────────

func isUnsafeSegment(seg string) bool {
	return strings.Contains(seg, "/") || strings.Contains(seg, "\\") ||
		seg == "." || seg == ".."
}

func isSafeClaudeSessionFileSegment(seg string) bool {
	return seg != "" && !isUnsafeSegment(seg)
}

func isUnder(target, root string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
