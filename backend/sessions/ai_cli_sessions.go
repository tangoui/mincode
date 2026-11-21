package sessions

// 通用 AI CLI 会话扫描（Pi / Gemini / Qoder / Grok / Kimi / OpenCode / 用户自定义来源）。
// 对齐 Tauri src-tauri/src/terminal/ai_cli_sessions.rs 的 3 个命令：
//   list_ai_cli_sessions        {adapter,custom?}                 -> []codexSession
//   get_ai_cli_session_messages {adapter,filePath,limit,custom?}  -> []claudeSessionMessage
//   delete_ai_cli_session       {adapter,filePath,custom?}        -> void
// adapter = "custom" 时 custom 为必填的自定义来源配置（见 custom_sessions.go）。
//
// SQLite 依赖（OpenCode 的 opencode.db）改用纯 Go 驱动 modernc.org/sqlite，无 cgo，
// 跨平台交叉编译无痛。pi/gemini/qoder/grok/kimi 均为纯文件系统解析。

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"mincode/app/backend/core"

	_ "modernc.org/sqlite"
)

func init() {
	core.Register("list_ai_cli_sessions", listAiCliSessionsHandler)
	core.Register("get_ai_cli_session_messages", getAiCliSessionMessagesHandler)
	core.Register("delete_ai_cli_session", deleteAiCliSessionHandler)
}

// ─── 通用辅助 ────────────────────────────────────────────────────────────────

// readFirstFullLine 读取文件第一行（不含换行符），对齐 Rust read_first_line。
func readFirstFullLine(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	if scanner.Scan() {
		return scanner.Text()
	}
	return ""
}

// jsonStr 依次尝试多个 key，返回首个非空 trim 后的字符串值。对齐 Rust json_str。
func jsonStr(v map[string]json.RawMessage, keys ...string) string {
	for _, k := range keys {
		raw, ok := v[k]
		if !ok {
			continue
		}
		var s string
		if json.Unmarshal(raw, &s) == nil {
			if t := strings.TrimSpace(s); t != "" {
				return t
			}
		}
	}
	return ""
}

// idPrefixTitle 返回 id 前 8 个字符 + "…"，对齐 Rust `format!("{}…", id 前 8 字符)`。
func idPrefixTitle(id string) string {
	runes := []rune(id)
	if len(runes) > 8 {
		runes = runes[:8]
	}
	return string(runes) + "…"
}

// collectFilesRecursive 递归收集满足 pred 的文件（最大深度 6），对齐 Rust collect_files_recursive。
func collectFilesRecursive(dir string, depth int, pred func(string) bool) []string {
	if depth > 6 {
		return nil
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if e.IsDir() {
			out = append(out, collectFilesRecursive(p, depth+1, pred)...)
		} else if pred(p) {
			out = append(out, p)
		}
	}
	return out
}

// ensureUnderRoot 校验 target 规范化后位于 root 之下，对齐 Rust ensure_under_root。
func ensureUnderRoot(target, root string) (string, error) {
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("无法定位目录：%v", err)
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", fmt.Errorf("路径不存在：%v", err)
	}
	rel, err := filepath.Rel(rootReal, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("路径不在允许的会话目录内")
	}
	return resolved, nil
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// ─── 通用 AI CLI 会话状态推断 ─────────────────────────────────────────────────

// inferGenericJsonlStatus 从 JSONL 文件尾部推断会话状态（适用于 Gemini、Qoder CN、Kimi 等 JSONL）。
func inferGenericJsonlStatus(path string) string {
	tail := readJsonlTail(path, 32*1024)
	if len(tail) == 0 {
		return ""
	}
	for i := len(tail) - 1; i >= 0; i-- {
		var v map[string]json.RawMessage
		if json.Unmarshal([]byte(tail[i]), &v) != nil {
			continue
		}
		role := jsonStr(v, "role", "type")
		switch role {
		case "assistant", "gemini", "ai", "agent":
			return "completed"
		case "user", "human":
			return "in_progress"
		}
	}
	return ""
}

// inferGeminiJSONStatus 从 Gemini JSON 的 messages 数组末尾推断状态。
func inferGeminiJSONStatus(v map[string]json.RawMessage) string {
	raw, ok := v["messages"]
	if !ok {
		return ""
	}
	var msgs []map[string]json.RawMessage
	if json.Unmarshal(raw, &msgs) != nil || len(msgs) == 0 {
		return ""
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		role := jsonStr(msgs[i], "type", "role")
		switch role {
		case "gemini", "assistant":
			return "completed"
		case "user":
			return "in_progress"
		}
	}
	return ""
}

// ─── Pi ─────────────────────────────────────────────────────────────────────

func piRoot() (string, bool) {
	if dir, ok := sessionRootOverride("pi"); ok {
		sessionsDir := filepath.Join(dir, "sessions")
		return sessionsDir, isDir(sessionsDir)
	}
	if dir, ok := sessionRootFromEnv("PI_CODING_AGENT_DIR"); ok {
		sessionsDir := filepath.Join(dir, "sessions")
		if isDir(sessionsDir) {
			return sessionsDir, true
		}
	}
	home, ok := sessionHomeDir()
	if !ok {
		return "", false
	}
	dir := filepath.Join(home, ".pi", "agent", "sessions")
	return dir, isDir(dir)
}

func piMessageText(message map[string]json.RawMessage) string {
	raw, ok := message["content"]
	if !ok {
		return ""
	}
	var plain string
	if json.Unmarshal(raw, &plain) == nil {
		return strings.TrimSpace(plain)
	}
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if jsonStr(block, "type") != "text" {
			continue
		}
		if text := jsonStr(block, "text"); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func piActiveEntries(path string) (map[string]json.RawMessage, []map[string]json.RawMessage) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, nil
	}
	var header map[string]json.RawMessage
	entries := make([]map[string]json.RawMessage, 0)
	byID := make(map[string]map[string]json.RawMessage)
	for _, line := range strings.Split(string(content), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var entry map[string]json.RawMessage
		if json.Unmarshal([]byte(line), &entry) != nil {
			continue
		}
		if jsonStr(entry, "type") == "session" && header == nil {
			header = entry
			continue
		}
		entries = append(entries, entry)
		if id := jsonStr(entry, "id"); id != "" {
			byID[id] = entry
		}
	}
	if len(entries) == 0 {
		return header, nil
	}
	leafID := jsonStr(entries[len(entries)-1], "id")
	if leafID == "" {
		return header, entries
	}
	branch := make([]map[string]json.RawMessage, 0, len(entries))
	seen := make(map[string]struct{})
	for leafID != "" {
		if _, exists := seen[leafID]; exists {
			break
		}
		seen[leafID] = struct{}{}
		entry, exists := byID[leafID]
		if !exists {
			break
		}
		branch = append(branch, entry)
		leafID = jsonStr(entry, "parentId")
	}
	if len(branch) == 0 {
		return header, entries
	}
	for left, right := 0, len(branch)-1; left < right; left, right = left+1, right-1 {
		branch[left], branch[right] = branch[right], branch[left]
	}
	return header, branch
}

func piMessagesFromEntries(entries []map[string]json.RawMessage, limit int) []claudeSessionMessage {
	out := make([]claudeSessionMessage, 0)
	for _, entry := range entries {
		entryType := jsonStr(entry, "type")
		if entryType == "compaction" || entryType == "branch_summary" {
			text := jsonStr(entry, "summary")
			if text != "" {
				ts := jsonStr(entry, "timestamp")
				out = append(out, claudeSessionMessage{Role: "assistant", Content: text, Timestamp: &ts, MsgType: entryType})
			}
			continue
		}
		if entryType != "message" {
			continue
		}
		var message map[string]json.RawMessage
		if json.Unmarshal(entry["message"], &message) != nil {
			continue
		}
		role := jsonStr(message, "role")
		if role != "user" && role != "assistant" {
			continue
		}
		text := piMessageText(message)
		if text == "" {
			continue
		}
		model := jsonStr(message, "model")
		var modelPtr *string
		if model != "" {
			modelCopy := model
			modelPtr = &modelCopy
		}
		ts := jsonStr(entry, "timestamp")
		out = append(out, claudeSessionMessage{Role: role, Content: text, Model: modelPtr, Timestamp: &ts, MsgType: "message"})
	}
	return tailMessages(out, limit)
}

func parsePiSession(path string) *codexSession {
	header, entries := piActiveEntries(path)
	if header == nil {
		return nil
	}
	id := jsonStr(header, "id")
	if id == "" {
		id = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	cwd := jsonStr(header, "cwd")
	title := ""
	model := ""
	status := ""
	messages := piMessagesFromEntries(entries, 0)
	for _, entry := range entries {
		switch jsonStr(entry, "type") {
		case "session_info":
			if name := jsonStr(entry, "name"); name != "" {
				title = name
			}
		case "model_change":
			if nextModel := jsonStr(entry, "modelId"); nextModel != "" {
				model = nextModel
			}
		}
	}
	for _, message := range messages {
		if title == "" && message.Role == "user" {
			title = firstSentenceTitle(message.Content, 60)
		}
		if model == "" && message.Model != nil && *message.Model != "" {
			model = *message.Model
		}
		if message.Role == "user" {
			status = "in_progress"
		} else if message.Role == "assistant" {
			status = "completed"
		}
	}
	if title == "" {
		title = idPrefixTitle(id)
	}
	return &codexSession{ID: id, Title: title, LastAt: fileMtimeMs(path), MessageCount: uint32(len(messages)), Model: model, Cwd: cwd, FilePath: path, Status: status}
}

func listPiSessions() []codexSession {
	root, ok := piRoot()
	if !ok {
		return []codexSession{}
	}
	paths := collectFilesRecursive(root, 0, func(path string) bool {
		return strings.EqualFold(filepath.Ext(path), ".jsonl")
	})
	out := make([]codexSession, 0, len(paths))
	for _, path := range paths {
		if session := parsePiSession(path); session != nil && session.MessageCount > 0 {
			out = append(out, *session)
		}
	}
	sortSessionsByLastAtDesc(out)
	return out
}

func piMessagesFromJsonl(path string, limit int) []claudeSessionMessage {
	_, entries := piActiveEntries(path)
	return piMessagesFromEntries(entries, limit)
}

// ─── Gemini ─────────────────────────────────────────────────────────────────

func geminiRoot() (string, bool) {
	if dir, ok := sessionRootOverride("gemini"); ok {
		return dir, true
	}
	if dir := strings.TrimSpace(os.Getenv("GEMINI_DIR")); dir != "" && isDir(dir) {
		return dir, true
	}
	home, ok := sessionHomeDir()
	if !ok {
		return "", false
	}
	return filepath.Join(home, ".gemini"), true
}

// arrayFirstString 从 value 中读取数组首元素字符串（用于 directories[0]）。
func arrayFirstString(v map[string]json.RawMessage, key string) string {
	raw, ok := v[key]
	if !ok {
		return ""
	}
	var arr []string
	if json.Unmarshal(raw, &arr) == nil && len(arr) > 0 {
		return arr[0]
	}
	return ""
}

func parseGeminiJSON(content, path string) *codexSession {
	var v map[string]json.RawMessage
	if json.Unmarshal([]byte(content), &v) != nil {
		return nil
	}
	id := jsonStr(v, "sessionId", "id")
	if id == "" {
		id = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	title := jsonStr(v, "title", "summary")
	model := ""
	var messageCount uint32
	cwd := arrayFirstString(v, "directories")

	if raw, ok := v["messages"]; ok {
		var msgs []map[string]json.RawMessage
		if json.Unmarshal(raw, &msgs) == nil {
			for _, msg := range msgs {
				msgType := jsonStr(msg, "type", "role")
				switch msgType {
				case "user":
					messageCount++
					if title == "" {
						title = firstSentenceTitle(jsonStr(msg, "content", "text"), 100)
					}
				case "gemini", "assistant":
					messageCount++
					if model == "" {
						model = jsonStr(msg, "model")
					}
				}
			}
		}
	}
	if messageCount == 0 {
		return nil
	}
	if title == "" {
		title = idPrefixTitle(id)
	}
	return &codexSession{
		ID:           id,
		Title:        title,
		LastAt:       fileMtimeMs(path),
		MessageCount: messageCount,
		Model:        model,
		Cwd:          cwd,
		FilePath:     path,
		Status:       inferGeminiJSONStatus(v),
	}
}

func parseGeminiJsonlPrefix(path string) *codexSession {
	content := readFirstFullLine(path)
	if strings.TrimSpace(content) == "" {
		return nil
	}
	var meta map[string]json.RawMessage
	if json.Unmarshal([]byte(strings.TrimSpace(content)), &meta) != nil {
		return nil
	}
	if _, ok := meta["$set"]; ok {
		return nil
	}
	id := jsonStr(meta, "sessionId", "id")
	if id == "" {
		id = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	title := jsonStr(meta, "title", "summary")
	cwd := arrayFirstString(meta, "directories")
	messageCount := saturatingSub(countFileLines(path), 1)
	if messageCount == 0 {
		return nil
	}
	if title == "" {
		title = idPrefixTitle(id)
	}
	return &codexSession{
		ID:           id,
		Title:        title,
		LastAt:       fileMtimeMs(path),
		MessageCount: messageCount,
		Model:        jsonStr(meta, "model"),
		Cwd:          cwd,
		FilePath:     path,
		Status:       inferGenericJsonlStatus(path),
	}
}

func saturatingSub(a, b uint32) uint32 {
	if a < b {
		return 0
	}
	return a - b
}

func listGeminiSessions() []codexSession {
	root, ok := geminiRoot()
	if !ok {
		return []codexSession{}
	}
	tmp := filepath.Join(root, "tmp")
	if !isDir(tmp) {
		return []codexSession{}
	}
	sessions := make([]codexSession, 0)
	projects, err := os.ReadDir(tmp)
	if err != nil {
		return sessions
	}
	for _, proj := range projects {
		chats := filepath.Join(tmp, proj.Name(), "chats")
		if !isDir(chats) {
			continue
		}
		files, ferr := os.ReadDir(chats)
		if ferr != nil {
			continue
		}
		for _, f := range files {
			name := f.Name()
			if !strings.HasPrefix(name, "session-") {
				continue
			}
			path := filepath.Join(chats, name)
			ext := strings.TrimPrefix(filepath.Ext(name), ".")
			var parsed *codexSession
			switch ext {
			case "json":
				if data, rerr := os.ReadFile(path); rerr == nil {
					parsed = parseGeminiJSON(string(data), path)
				}
			case "jsonl":
				parsed = parseGeminiJsonlPrefix(path)
			}
			if parsed != nil {
				sessions = append(sessions, *parsed)
			}
		}
	}
	sortSessionsByLastAtDesc(sessions)
	return sessions
}

func geminiMessagesFromJSON(content string, limit int) []claudeSessionMessage {
	var v map[string]json.RawMessage
	if json.Unmarshal([]byte(content), &v) != nil {
		return []claudeSessionMessage{}
	}
	raw, ok := v["messages"]
	if !ok {
		return []claudeSessionMessage{}
	}
	var msgs []map[string]json.RawMessage
	if json.Unmarshal(raw, &msgs) != nil {
		return []claudeSessionMessage{}
	}
	out := make([]claudeSessionMessage, 0)
	for _, msg := range msgs {
		msgType := jsonStr(msg, "type", "role")
		var role string
		switch msgType {
		case "user":
			role = "user"
		case "gemini", "assistant":
			role = "assistant"
		default:
			continue
		}
		text := jsonStr(msg, "content", "text")
		if strings.TrimSpace(text) == "" {
			continue
		}
		var model *string
		if role == "assistant" {
			m := jsonStr(msg, "model")
			model = &m
		}
		ts := jsonStr(msg, "timestamp")
		out = append(out, claudeSessionMessage{
			Role:      role,
			Content:   text,
			Model:     model,
			Timestamp: &ts,
			MsgType:   msgType,
		})
	}
	return tailMessages(out, limit)
}

func geminiMessagesFromJsonl(content string, limit int) []claudeSessionMessage {
	out := make([]claudeSessionMessage, 0)
	for idx, line := range strings.Split(content, "\n") {
		if idx == 0 || strings.TrimSpace(line) == "" {
			continue
		}
		var v map[string]json.RawMessage
		if json.Unmarshal([]byte(line), &v) != nil {
			continue
		}
		if _, ok := v["$set"]; ok {
			continue
		}
		msgType := jsonStr(v, "type", "role")
		var role string
		switch msgType {
		case "user":
			role = "user"
		case "gemini", "assistant":
			role = "assistant"
		default:
			continue
		}
		text := jsonStr(v, "content", "text")
		if strings.TrimSpace(text) == "" {
			continue
		}
		var model *string
		if role == "assistant" {
			m := jsonStr(v, "model")
			model = &m
		}
		ts := jsonStr(v, "timestamp")
		out = append(out, claudeSessionMessage{
			Role:      role,
			Content:   text,
			Model:     model,
			Timestamp: &ts,
			MsgType:   msgType,
		})
	}
	return tailMessages(out, limit)
}

// ─── Qoder CN / Qoder ─────────────────────────────────────────────────────────

func qodercnRoot() (string, bool) {
	if dir, ok := sessionRootOverride("qodercn"); ok {
		return dir, true
	}
	home, ok := sessionHomeDir()
	if !ok {
		return "", false
	}
	return filepath.Join(home, ".qoder-cn"), true
}

func qoderRoot() (string, bool) {
	if dir, ok := sessionRootOverride("qoder"); ok {
		return dir, true
	}
	home, ok := sessionHomeDir()
	if !ok {
		return "", false
	}
	return filepath.Join(home, ".qoder"), true
}

// messageText extracts visible text from the string or content-block forms used
// by Qoder and Grok transcripts. Thinking/tool payloads are intentionally ignored.
func messageText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return strings.TrimSpace(text)
	}
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if text := jsonStr(block, "text", "content"); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func qoderMessageFromRecord(v map[string]json.RawMessage) (claudeSessionMessage, bool) {
	role := jsonStr(v, "type", "role")
	if role != "user" && role != "assistant" {
		return claudeSessionMessage{}, false
	}
	var message map[string]json.RawMessage
	if raw, ok := v["message"]; ok && json.Unmarshal(raw, &message) == nil {
		if nestedRole := jsonStr(message, "role"); nestedRole == "user" || nestedRole == "assistant" {
			role = nestedRole
		}
	}
	text := ""
	if raw, ok := message["content"]; ok {
		text = messageText(raw)
	}
	if text == "" {
		if raw, ok := v["content"]; ok {
			text = messageText(raw)
		}
	}
	if text == "" {
		return claudeSessionMessage{}, false
	}
	ts := jsonStr(v, "timestamp")
	model := jsonStr(v, "model")
	return claudeSessionMessage{Role: role, Content: text, Model: &model, Timestamp: &ts, MsgType: "message"}, true
}

func parseQoderCNJsonl(path string) *codexSession {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var id, cwd, title, model string
	count := uint32(0)
	for _, line := range strings.Split(string(data), "\n") {
		var v map[string]json.RawMessage
		if json.Unmarshal([]byte(line), &v) != nil {
			continue
		}
		if id == "" {
			id = jsonStr(v, "sessionId", "id")
		}
		if cwd == "" {
			cwd = jsonStr(v, "cwd", "projectRoot")
		}
		if message, ok := qoderMessageFromRecord(v); ok {
			count++
			if title == "" && message.Role == "user" {
				title = firstSentenceTitle(message.Content, 80)
			}
			if model == "" && message.Model != nil {
				model = *message.Model
			}
		}
	}
	if count == 0 {
		return nil
	}
	if id == "" {
		id = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	if title == "" {
		title = idPrefixTitle(id)
	}
	return &codexSession{ID: id, Title: title, LastAt: fileMtimeMs(path), MessageCount: count, Model: model, Cwd: cwd, FilePath: path, Status: inferGenericJsonlStatus(path)}
}

func listQoderCNSessions() []codexSession {
	root, ok := qodercnRoot()
	if !ok {
		return []codexSession{}
	}
	transcripts := filepath.Join(root, "projects")
	sessions := make([]codexSession, 0)
	for _, path := range collectFilesRecursive(transcripts, 0, func(p string) bool {
		return strings.EqualFold(filepath.Ext(p), ".jsonl")
	}) {
		if session := parseQoderCNJsonl(path); session != nil {
			sessions = append(sessions, *session)
		}
	}
	sortSessionsByLastAtDesc(sessions)
	return sessions
}

func qoderCNMessagesFromJsonl(path string, limit int) []claudeSessionMessage {
	data, _ := os.ReadFile(path)
	messages := make([]claudeSessionMessage, 0)
	for _, line := range strings.Split(string(data), "\n") {
		var v map[string]json.RawMessage
		if json.Unmarshal([]byte(line), &v) != nil {
			continue
		}
		if message, ok := qoderMessageFromRecord(v); ok {
			messages = append(messages, message)
		}
	}
	return tailMessages(messages, limit)
}

func listQoderSessions() []codexSession {
	root, ok := qoderRoot()
	if !ok {
		return []codexSession{}
	}
	sessions := make([]codexSession, 0)
	for _, statePath := range collectFilesRecursive(filepath.Join(root, "projects"), 0, func(p string) bool {
		return filepath.Base(p) == "state.json"
	}) {
		data, err := os.ReadFile(statePath)
		if err != nil {
			continue
		}
		var state struct {
			SessionID            string   `json:"sessionId"`
			UpdatedAt            string   `json:"updatedAt"`
			WorkspaceDirectories []string `json:"workspaceDirectories"`
		}
		if json.Unmarshal(data, &state) != nil || strings.TrimSpace(state.SessionID) == "" {
			continue
		}
		lastAt := fileMtimeMs(statePath)
		if parsed, err := time.Parse(time.RFC3339Nano, state.UpdatedAt); err == nil {
			lastAt = uint64(parsed.UnixMilli())
		}
		cwd := ""
		if len(state.WorkspaceDirectories) > 0 {
			cwd = state.WorkspaceDirectories[0]
		}
		sessions = append(sessions, codexSession{ID: state.SessionID, Title: idPrefixTitle(state.SessionID), LastAt: lastAt, Cwd: cwd, FilePath: statePath})
	}
	sortSessionsByLastAtDesc(sessions)
	return sessions
}

// ─── Grok ─────────────────────────────────────────────────────────────────────

func grokRoot() (string, bool) {
	if dir, ok := sessionRootOverride("grok"); ok {
		return dir, true
	}
	home, ok := sessionHomeDir()
	if !ok {
		return "", false
	}
	return filepath.Join(home, ".grok"), true
}

type grokSummary struct {
	Info struct {
		ID  string `json:"id"`
		Cwd string `json:"cwd"`
	} `json:"info"`
	GeneratedTitle string `json:"generated_title"`
	SessionSummary string `json:"session_summary"`
	UpdatedAt      string `json:"updated_at"`
	Messages       uint32 `json:"num_chat_messages"`
	Model          string `json:"current_model_id"`
}

func parseGrokSummary(path string) *codexSession {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var summary grokSummary
	if json.Unmarshal(data, &summary) != nil || strings.TrimSpace(summary.Info.ID) == "" {
		return nil
	}
	lastAt := fileMtimeMs(path)
	if parsed, err := time.Parse(time.RFC3339Nano, summary.UpdatedAt); err == nil {
		lastAt = uint64(parsed.UnixMilli())
	}
	title := strings.TrimSpace(summary.GeneratedTitle)
	if title == "" {
		title = strings.TrimSpace(summary.SessionSummary)
	}
	if title == "" {
		title = idPrefixTitle(summary.Info.ID)
	}
	return &codexSession{ID: summary.Info.ID, Title: title, LastAt: lastAt, MessageCount: summary.Messages, Model: summary.Model, Cwd: summary.Info.Cwd, FilePath: path}
}

func listGrokSessions() []codexSession {
	root, ok := grokRoot()
	if !ok {
		return []codexSession{}
	}
	sessions := make([]codexSession, 0)
	for _, path := range collectFilesRecursive(filepath.Join(root, "sessions"), 0, func(p string) bool {
		return filepath.Base(p) == "summary.json"
	}) {
		if session := parseGrokSummary(path); session != nil {
			sessions = append(sessions, *session)
		}
	}
	sortSessionsByLastAtDesc(sessions)
	return sessions
}

func grokMessagesFromJsonl(path string, limit int) []claudeSessionMessage {
	data, _ := os.ReadFile(path)
	messages := make([]claudeSessionMessage, 0)
	for _, line := range strings.Split(string(data), "\n") {
		var v map[string]json.RawMessage
		if json.Unmarshal([]byte(line), &v) != nil {
			continue
		}
		role := jsonStr(v, "type", "role")
		if role != "user" && role != "assistant" {
			continue
		}
		raw, ok := v["content"]
		if !ok {
			continue
		}
		text := messageText(raw)
		if text == "" {
			continue
		}
		messages = append(messages, claudeSessionMessage{Role: role, Content: text, MsgType: "message"})
	}
	return tailMessages(messages, limit)
}

// ─── Kimi ─────────────────────────────────────────────────────────────────────

func kimiRoots() []string {
	if dir, ok := sessionRootOverride("kimi"); ok {
		return []string{dir}
	}
	home, ok := sessionHomeDir()
	if !ok {
		return nil
	}
	return []string{
		filepath.Join(home, ".kimi-code"),
		filepath.Join(home, ".kimi"),
	}
}

func loadKimiSessionIndex(root string) map[string]string {
	m := make(map[string]string)
	index := filepath.Join(root, "session_index.jsonl")
	data, err := os.ReadFile(index)
	if err != nil {
		return m
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var v map[string]json.RawMessage
		if json.Unmarshal([]byte(line), &v) != nil {
			continue
		}
		id := jsonStr(v, "sessionId", "id")
		cwd := jsonStr(v, "workDir", "cwd", "directory")
		if id != "" && cwd != "" {
			m[id] = cwd
		}
	}
	return m
}

func parseKimiSessionDir(sessionDir, cwdHint string) *codexSession {
	statePath := filepath.Join(sessionDir, "state.json")
	if !isFile(statePath) {
		return nil
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		return nil
	}
	var v map[string]json.RawMessage
	if json.Unmarshal(data, &v) != nil {
		return nil
	}
	id := filepath.Base(sessionDir)
	if id == "" {
		return nil
	}
	title := jsonStr(v, "custom_title", "title", "lastPrompt")
	wirePath := filepath.Join(sessionDir, "agents", "main", "wire.jsonl")
	var messageCount uint32
	if isFile(wirePath) {
		messageCount = countFileLines(wirePath)
	}
	if messageCount == 0 && title == "" {
		return nil
	}
	if title == "" {
		title = idPrefixTitle(id)
	}
	status := ""
	if isFile(wirePath) {
		status = inferGenericJsonlStatus(wirePath)
	}
	return &codexSession{
		ID:           id,
		Title:        title,
		LastAt:       fileMtimeMs(statePath),
		MessageCount: messageCount,
		Model:        "",
		Cwd:          cwdHint,
		FilePath:     sessionDir,
		Status:       status,
	}
}

func listKimiSessions() []codexSession {
	sessions := make([]codexSession, 0)
	for _, root := range kimiRoots() {
		sessionsDir := filepath.Join(root, "sessions")
		if !isDir(sessionsDir) {
			continue
		}
		index := loadKimiSessionIndex(root)
		buckets, err := os.ReadDir(sessionsDir)
		if err != nil {
			continue
		}
		for _, bucket := range buckets {
			bucketPath := filepath.Join(sessionsDir, bucket.Name())
			if !isDir(bucketPath) {
				continue
			}
			entries, eerr := os.ReadDir(bucketPath)
			if eerr != nil {
				continue
			}
			for _, entry := range entries {
				sessionDir := filepath.Join(bucketPath, entry.Name())
				if !isDir(sessionDir) {
					continue
				}
				cwd := index[entry.Name()]
				if s := parseKimiSessionDir(sessionDir, cwd); s != nil {
					sessions = append(sessions, *s)
				}
			}
		}
	}
	sortSessionsByLastAtDesc(sessions)
	return sessions
}

func kimiMessagesFromWire(path string, limit int) []claudeSessionMessage {
	data, _ := os.ReadFile(path)
	out := make([]claudeSessionMessage, 0)
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var v map[string]json.RawMessage
		if json.Unmarshal([]byte(line), &v) != nil {
			continue
		}
		role := jsonStr(v, "role", "type")
		var mapped string
		switch role {
		case "user", "human":
			mapped = "user"
		case "assistant", "ai", "agent":
			mapped = "assistant"
		default:
			if _, ok := v["user"]; ok {
				mapped = "user"
			} else if _, ok := v["assistant"]; ok {
				mapped = "assistant"
			} else {
				continue
			}
		}
		text := jsonStr(v, "content", "text", "message")
		if strings.TrimSpace(text) == "" {
			// 回退：从 v[mapped].content 提取字符串
			if raw, ok := v[mapped]; ok {
				var inner map[string]json.RawMessage
				if json.Unmarshal(raw, &inner) == nil {
					if c := jsonStr(inner, "content"); strings.TrimSpace(c) != "" {
						out = append(out, claudeSessionMessage{
							Role:    mapped,
							Content: c,
							MsgType: "message",
						})
					}
				}
			}
			continue
		}
		out = append(out, claudeSessionMessage{
			Role:    mapped,
			Content: text,
			MsgType: "message",
		})
	}
	return tailMessages(out, limit)
}

// ─── OpenCode（SQLite）─────────────────────────────────────────────────────────

func opencodeDBPath() (string, bool) {
	if dir, ok := sessionRootOverride("opencode"); ok {
		db := filepath.Join(dir, "opencode.db")
		return db, isFile(db)
	}
	if p := strings.TrimSpace(os.Getenv("OPENCODE_DB")); p != "" && isFile(p) {
		return p, true
	}
	var data string
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		data = x
	} else if home, ok := sessionHomeDir(); ok {
		data = filepath.Join(home, ".local", "share")
	} else {
		return "", false
	}
	db := filepath.Join(data, "opencode", "opencode.db")
	if isFile(db) {
		return db, true
	}
	return "", false
}

// openReadOnlySQLite 只读打开 SQLite（modernc 驱动的 DSN 参数）。不用 immutable：
// OpenCode 运行时最新的会话只在 -wal 里，immutable 会忽略 WAL，新会话就看不到。
func openReadOnlySQLite(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(2000)", filepath.ToSlash(path))
	return sql.Open("sqlite", dsn)
}

// openOpencodeDB 打开 OpenCode 数据库；只读连接打不开 WAL（例如没有 -shm 且目录不可写）
// 时退回 immutable，至少能读到已经落盘的会话。
func openOpencodeDB() (*sql.DB, bool) {
	dbPath, ok := opencodeDBPath()
	if !ok {
		return nil, false
	}
	db, err := openReadOnlySQLite(dbPath)
	if err == nil && db.Ping() == nil {
		if _, err = db.Exec("SELECT count(*) FROM sqlite_master"); err == nil {
			return db, true
		}
	}
	if db != nil {
		db.Close()
	}
	db, err = sql.Open("sqlite", fmt.Sprintf("file:%s?mode=ro&immutable=1", filepath.ToSlash(dbPath)))
	if err != nil {
		return nil, false
	}
	return db, true
}

// opencodeUsesV2 区分 OpenCode 2.x 的 session_v2/session_message 与 1.x 的 session/message/part。
func opencodeUsesV2(db *sql.DB) bool {
	var name string
	return db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'session_v2'`).Scan(&name) == nil
}

func opencodeModelID(raw string) string {
	var model map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &model) == nil {
		if id := jsonStr(model, "id"); id != "" {
			return id
		}
	}
	return raw
}

func listOpencodeSessions() []codexSession {
	db, ok := openOpencodeDB()
	if !ok {
		return []codexSession{}
	}
	defer db.Close()
	v2 := opencodeUsesV2(db)
	query := `SELECT s.id, COALESCE(s.title, ''), COALESCE(s.directory, ''), s.time_updated, s.model,
                (SELECT COUNT(*) FROM message m WHERE m.session_id = s.id) AS msg_count
         FROM session s
         WHERE s.parent_id IS NULL OR s.parent_id = ''
         ORDER BY s.time_updated DESC`
	if v2 {
		query = `SELECT s.id, COALESCE(s.title, ''), s.directory, s.time_updated, s.model,
                (SELECT COUNT(*) FROM session_message m WHERE m.session_id = s.id AND m.type IN ('user', 'assistant')) AS msg_count
         FROM session_v2 s
         WHERE (s.parent_id IS NULL OR s.parent_id = '') AND s.time_archived IS NULL
         ORDER BY s.time_updated DESC`
	}
	rows, err := db.Query(query)
	if err != nil {
		return []codexSession{}
	}
	collected := make([]codexSession, 0)
	for rows.Next() {
		var id, title, cwd string
		var lastAt int64
		var modelJSON sql.NullString
		var msgCount int64
		if scanErr := rows.Scan(&id, &title, &cwd, &lastAt, &modelJSON, &msgCount); scanErr != nil {
			continue
		}
		model := ""
		if modelJSON.Valid {
			model = opencodeModelID(modelJSON.String)
		}
		if strings.TrimSpace(title) == "" {
			title = idPrefixTitle(id)
		}
		if lastAt < 0 {
			lastAt = 0
		}
		if msgCount < 0 {
			msgCount = 0
		}
		collected = append(collected, codexSession{
			ID:           id,
			Title:        title,
			LastAt:       uint64(lastAt),
			MessageCount: uint32(msgCount),
			Model:        model,
			Cwd:          cwd,
			FilePath:     fmt.Sprintf("opencode://%s", id),
		})
	}
	rows.Close()
	// 状态要再查一次库；必须在遍历结束后查，单连接时嵌套查询会互相等待。
	for i := range collected {
		if v2 {
			collected[i].Status = opencodeV2Status(db, collected[i].ID)
		} else {
			collected[i].Status = opencodeLastMessageRole(db, collected[i].ID)
		}
	}
	return collected
}

// opencodeLastMessageRole 查询 OpenCode 1.x 会话最后一条消息的 role 并推断状态。
func opencodeLastMessageRole(db *sql.DB, sessionID string) string {
	var role string
	err := db.QueryRow(
		`SELECT role FROM message WHERE session_id = ? ORDER BY time_created DESC LIMIT 1`,
		sessionID,
	).Scan(&role)
	if err != nil {
		return ""
	}
	switch role {
	case "assistant":
		return "completed"
	case "user":
		return "in_progress"
	}
	return ""
}

// opencodeV2Status 由 OpenCode 2.x 会话最后一条 user/assistant/idle 记录推断状态；
// idle 记录带本轮结果（outcome）。
func opencodeV2Status(db *sql.DB, sessionID string) string {
	var kind, data string
	err := db.QueryRow(
		`SELECT type, data FROM session_message
         WHERE session_id = ? AND type IN ('user', 'assistant', 'idle')
         ORDER BY seq DESC LIMIT 1`,
		sessionID,
	).Scan(&kind, &data)
	if err != nil {
		return ""
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal([]byte(data), &fields)
	switch kind {
	case "user":
		return "in_progress"
	case "assistant":
		if _, failed := fields["error"]; failed {
			return "failed"
		}
		return "completed"
	case "idle":
		switch jsonStr(fields, "outcome") {
		case "failed", "error":
			return "failed"
		case "succeeded", "":
			return "completed"
		}
		return "interrupted"
	}
	return ""
}

func opencodeMessages(sessionID string, limit int) []claudeSessionMessage {
	db, ok := openOpencodeDB()
	if !ok {
		return []claudeSessionMessage{}
	}
	defer db.Close()
	if opencodeUsesV2(db) {
		return opencodeV2Messages(db, sessionID, limit)
	}
	rows, err := db.Query(
		`SELECT m.data, p.data FROM message m
         LEFT JOIN part p ON p.message_id = m.id
         WHERE m.session_id = ?
         ORDER BY m.time_created ASC, p.id ASC`,
		sessionID,
	)
	if err != nil {
		return []claudeSessionMessage{}
	}
	defer rows.Close()
	out := make([]claudeSessionMessage, 0)
	for rows.Next() {
		var msgData string
		var partData sql.NullString
		if scanErr := rows.Scan(&msgData, &partData); scanErr != nil {
			continue
		}
		var msg map[string]json.RawMessage
		_ = json.Unmarshal([]byte(msgData), &msg)
		role := jsonStr(msg, "role")
		if role != "user" && role != "assistant" {
			continue
		}
		text := ""
		if partData.Valid {
			var part map[string]json.RawMessage
			if json.Unmarshal([]byte(partData.String), &part) == nil {
				text = jsonStr(part, "text", "content")
			}
		}
		if strings.TrimSpace(text) == "" {
			text = jsonStr(msg, "content", "text")
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		model := jsonStr(msg, "modelID", "model")
		out = append(out, claudeSessionMessage{
			Role:    role,
			Content: text,
			Model:   &model,
			MsgType: "message",
		})
	}
	return tailMessages(out, limit)
}

// opencodeV2Messages 读 OpenCode 2.x 的消息：user 的 data.text，assistant 的
// data.content 里 type=text 的片段（推理、工具调用不展示）。
func opencodeV2Messages(db *sql.DB, sessionID string, limit int) []claudeSessionMessage {
	rows, err := db.Query(
		`SELECT type, data FROM session_message
         WHERE session_id = ? AND type IN ('user', 'assistant')
         ORDER BY seq ASC`,
		sessionID,
	)
	if err != nil {
		return []claudeSessionMessage{}
	}
	defer rows.Close()
	out := make([]claudeSessionMessage, 0)
	for rows.Next() {
		var kind, data string
		if rows.Scan(&kind, &data) != nil {
			continue
		}
		var payload struct {
			Text    string `json:"text"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			Model struct {
				ID string `json:"id"`
			} `json:"model"`
		}
		if json.Unmarshal([]byte(data), &payload) != nil {
			continue
		}
		text := payload.Text
		if kind == "assistant" {
			parts := make([]string, 0, len(payload.Content))
			for _, part := range payload.Content {
				if part.Type == "text" && strings.TrimSpace(part.Text) != "" {
					parts = append(parts, part.Text)
				}
			}
			text = strings.Join(parts, "\n\n")
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		model := payload.Model.ID
		out = append(out, claudeSessionMessage{
			Role:    kind,
			Content: text,
			Model:   &model,
			MsgType: "message",
		})
	}
	return tailMessages(out, limit)
}

// ─── 公共命令 ─────────────────────────────────────────────────────────────────

func adapterStorageRoot(adapter string) (string, bool) {
	switch adapter {
	case "pi":
		return piRoot()
	case "gemini":
		return geminiRoot()
	case "qodercn":
		return qodercnRoot()
	case "qoder":
		return qoderRoot()
	case "grok":
		return grokRoot()
	case "kimi":
		for _, p := range kimiRoots() {
			if isDir(filepath.Join(p, "sessions")) {
				return p, true
			}
		}
		return "", false
	case "opencode":
		if p, ok := opencodeDBPath(); ok {
			return filepath.Dir(p), true
		}
		return "", false
	default:
		return "", false
	}
}

func sortSessionsByLastAtDesc(sessions []codexSession) {
	sort.SliceStable(sessions, func(i, j int) bool {
		return sessions[i].LastAt > sessions[j].LastAt
	})
}

func tailMessages(msgs []claudeSessionMessage, limit int) []claudeSessionMessage {
	if limit > 0 && len(msgs) > limit {
		return msgs[len(msgs)-limit:]
	}
	return msgs
}

type listAiCliSessionsArgs struct {
	Adapter string               `json:"adapter"`
	Custom  *customSessionConfig `json:"custom"`
}

func listAiCliSessionsHandler(_ string, argsJSON string) (string, error) {
	var args listAiCliSessionsArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	var sessions []codexSession
	switch args.Adapter {
	case "custom":
		custom, err := listCustomSessions(args.Custom)
		if err != nil {
			return "", err
		}
		return core.EncodeResult(custom)
	case "pi":
		sessions = listPiSessions()
	case "gemini":
		sessions = listGeminiSessions()
	case "qodercn":
		sessions = listQoderCNSessions()
	case "qoder":
		sessions = listQoderSessions()
	case "grok":
		sessions = listGrokSessions()
	case "kimi":
		sessions = listKimiSessions()
	case "opencode":
		sessions = listOpencodeSessions()
	default:
		sessions = []codexSession{}
	}
	reconcileAiCliStatuses(args.Adapter, sessions)
	return core.EncodeResult(sessions)
}

type getAiCliSessionMessagesArgs struct {
	Adapter  string               `json:"adapter"`
	FilePath string               `json:"filePath"`
	Limit    *uint32              `json:"limit"`
	Custom   *customSessionConfig `json:"custom"`
}

func getAiCliSessionMessagesHandler(_ string, argsJSON string) (string, error) {
	var args getAiCliSessionMessagesArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	max := 200
	if args.Limit != nil {
		max = int(*args.Limit)
	}
	var msgs []claudeSessionMessage
	switch args.Adapter {
	case "custom":
		custom, err := customSessionMessages(args.Custom, args.FilePath, max)
		if err != nil {
			return "", err
		}
		msgs = custom
	case "pi":
		root, ok := piRoot()
		if !ok {
			return "", fmt.Errorf("无法定位 Pi 会话存储目录")
		}
		file, err := ensureUnderRoot(args.FilePath, root)
		if err != nil {
			return "", err
		}
		msgs = piMessagesFromJsonl(file, max)
	case "gemini":
		content, _ := os.ReadFile(args.FilePath)
		if strings.EqualFold(filepath.Ext(args.FilePath), ".jsonl") {
			msgs = geminiMessagesFromJsonl(string(content), max)
		} else {
			msgs = geminiMessagesFromJSON(string(content), max)
		}
	case "qodercn":
		root, ok := qodercnRoot()
		if !ok {
			return "", fmt.Errorf("无法定位 Qoder CN 会话存储目录")
		}
		file, err := ensureUnderRoot(args.FilePath, root)
		if err != nil {
			return "", err
		}
		msgs = qoderCNMessagesFromJsonl(file, max)
	case "qoder":
		msgs = []claudeSessionMessage{}
	case "grok":
		root, ok := grokRoot()
		if !ok {
			return "", fmt.Errorf("无法定位 Grok 会话存储目录")
		}
		summary, err := ensureUnderRoot(args.FilePath, root)
		if err != nil {
			return "", err
		}
		msgs = grokMessagesFromJsonl(filepath.Join(filepath.Dir(summary), "chat_history.jsonl"), max)
	case "kimi":
		wire := filepath.Join(args.FilePath, "agents", "main", "wire.jsonl")
		msgs = kimiMessagesFromWire(wire, max)
	case "opencode":
		id := strings.TrimPrefix(args.FilePath, "opencode://")
		msgs = opencodeMessages(id, max)
	default:
		msgs = []claudeSessionMessage{}
	}
	return core.EncodeResult(msgs)
}

type deleteAiCliSessionArgs struct {
	Adapter  string               `json:"adapter"`
	FilePath string               `json:"filePath"`
	Custom   *customSessionConfig `json:"custom"`
}

func deleteAiCliSessionHandler(_ string, argsJSON string) (string, error) {
	var args deleteAiCliSessionArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if args.Adapter == "custom" {
		return "", deleteCustomSession(args.Custom, args.FilePath)
	}
	if args.Adapter == "opencode" {
		return "", fmt.Errorf("OpenCode 会话请在 OpenCode CLI 内管理，不支持从此处删除")
	}
	root, ok := adapterStorageRoot(args.Adapter)
	if !ok {
		return "", fmt.Errorf("无法定位会话存储目录")
	}
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("无法定位存储目录：%v", err)
	}
	if args.Adapter == "qoder" || args.Adapter == "grok" {
		file, verr := ensureUnderRoot(args.FilePath, rootReal)
		if verr != nil {
			return "", verr
		}
		if rmErr := os.RemoveAll(filepath.Dir(file)); rmErr != nil {
			return "", fmt.Errorf("删除失败：%v", rmErr)
		}
		return "", nil
	}
	if args.Adapter == "kimi" {
		dir, verr := ensureUnderRoot(args.FilePath, filepath.Join(rootReal, "sessions"))
		if verr != nil {
			return "", verr
		}
		if rmErr := os.RemoveAll(dir); rmErr != nil {
			return "", fmt.Errorf("删除失败：%v", rmErr)
		}
		return "", nil
	}
	file, verr := ensureUnderRoot(args.FilePath, rootReal)
	if verr != nil {
		return "", verr
	}
	if rmErr := os.Remove(file); rmErr != nil {
		return "", fmt.Errorf("删除失败：%v", rmErr)
	}
	return "", nil
}
