package sessions

// 自定义 AI CLI 会话来源（adapter = "custom"）：用户在「CC 会话 → 工具设置」里声明
// 会话目录、文件匹配规则、JSONL/JSON 格式与字段映射，后端据此通用解析，
// 覆盖未内置适配器、但会把会话落盘为 JSON 行或 JSON 文档的大多数 CLI。

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	customSessionMaxFiles     = 500
	customSessionMaxWalk      = 20000
	customSessionMaxDepth     = 6
	customSessionMaxJSONBytes = 32 << 20
	customSessionMaxLineBytes = 32 << 20
	customSessionTitleRunes   = 120
	customSessionCacheConfigs = 16
)

// 字段映射为空时使用的默认值：逗号分隔的候选点路径，按顺序取第一个命中的值。
const (
	defaultCustomSessionIDFields   = "sessionId,session_id"
	defaultCustomCwdFields         = "cwd,projectRoot,workdir,directory"
	defaultCustomTitleFields       = "title"
	defaultCustomMessagesFields    = "messages"
	defaultCustomRoleFields        = "role,type,message.role"
	defaultCustomContentFields     = "content,message.content,text"
	defaultCustomTimestampFields   = "timestamp,time,created_at,createdAt"
	defaultCustomModelFields       = "model,message.model"
	defaultCustomUserRoles         = "user,human"
	defaultCustomAssistantRoles    = "assistant,ai,model,gemini,agent,bot"
	defaultCustomJSONLGlob         = "*.jsonl"
	defaultCustomJSONGlob          = "*.json"
	customSessionFormatJSONL       = "jsonl"
	customSessionFormatJSON        = "json"
	customSessionRoleUser          = "user"
	customSessionRoleAssistant     = "assistant"
	customSessionStatusCompleted   = "completed"
	customSessionStatusInProgress  = "in_progress"
	customSessionMessageTypeNormal = "message"
)

var customProcessNamePattern = regexp.MustCompile(`^[A-Za-z0-9._+-]+$`)

type customSessionFields struct {
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	Title     string `json:"title"`
	Messages  string `json:"messages"`
	Role      string `json:"role"`
	Content   string `json:"content"`
	Timestamp string `json:"timestamp"`
	Model     string `json:"model"`
}

// customSessionConfig 是前端传入的自定义来源配置。
type customSessionConfig struct {
	Dir            string              `json:"dir"`
	Glob           string              `json:"glob"`
	Format         string              `json:"format"`
	Fields         customSessionFields `json:"fields"`
	UserRoles      string              `json:"userRoles"`
	AssistantRoles string              `json:"assistantRoles"`
	ProcessName    string              `json:"processName"`
}

type customSessionSpec struct {
	root           string
	glob           string
	format         string
	sessionID      [][]string
	cwd            [][]string
	title          [][]string
	messages       [][]string
	role           [][]string
	content        [][]string
	timestamp      [][]string
	model          [][]string
	userRoles      map[string]struct{}
	assistantRoles map[string]struct{}
	processName    string
	key            string
}

func fieldOrDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// parseFieldPaths 把 "a.b,c" 解析为 [[a b] [c]]，忽略空段。
func parseFieldPaths(value string) [][]string {
	var out [][]string
	for _, candidate := range strings.Split(value, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		var segments []string
		for _, segment := range strings.Split(candidate, ".") {
			if segment = strings.TrimSpace(segment); segment != "" {
				segments = append(segments, segment)
			}
		}
		if len(segments) > 0 {
			out = append(out, segments)
		}
	}
	return out
}

func parseRoleSet(value string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, role := range strings.Split(value, ",") {
		if role = strings.ToLower(strings.TrimSpace(role)); role != "" {
			out[role] = struct{}{}
		}
	}
	return out
}

// newCustomSessionSpec 校验并规范化配置；目录必须是存在的绝对路径，且不能是根目录或用户主目录本身。
func newCustomSessionSpec(cfg *customSessionConfig) (*customSessionSpec, error) {
	if cfg == nil {
		return nil, fmt.Errorf("缺少自定义会话来源配置")
	}
	dir := expandSessionPath(cfg.Dir)
	if dir == "" || !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("会话目录必须是绝对路径")
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil || !isDir(root) {
		return nil, fmt.Errorf("会话目录不存在：%s", dir)
	}
	if filepath.Dir(root) == root {
		return nil, fmt.Errorf("不能把根目录作为会话目录")
	}
	if home, ok := sessionHomeDir(); ok {
		if homeReal, herr := filepath.EvalSymlinks(home); herr == nil && homeReal == root {
			return nil, fmt.Errorf("不能把用户主目录作为会话目录，请指定更具体的子目录")
		}
	}

	format := strings.ToLower(strings.TrimSpace(cfg.Format))
	if format == "" {
		format = customSessionFormatJSONL
	}
	if format != customSessionFormatJSONL && format != customSessionFormatJSON {
		return nil, fmt.Errorf("不支持的会话格式：%s", cfg.Format)
	}
	glob := filepath.ToSlash(strings.TrimSpace(cfg.Glob))
	if glob == "" {
		glob = defaultCustomJSONLGlob
		if format == customSessionFormatJSON {
			glob = defaultCustomJSONGlob
		}
	}
	if strings.HasPrefix(glob, "/") || strings.Contains("/"+glob+"/", "/../") {
		return nil, fmt.Errorf("文件匹配规则必须是相对路径：%s", cfg.Glob)
	}
	if _, err := path.Match(glob, "probe"); err != nil {
		return nil, fmt.Errorf("文件匹配规则无效：%s", cfg.Glob)
	}

	fields := cfg.Fields
	spec := &customSessionSpec{
		root:           root,
		glob:           glob,
		format:         format,
		sessionID:      parseFieldPaths(fieldOrDefault(fields.SessionID, defaultCustomSessionIDFields)),
		cwd:            parseFieldPaths(fieldOrDefault(fields.Cwd, defaultCustomCwdFields)),
		title:          parseFieldPaths(fieldOrDefault(fields.Title, defaultCustomTitleFields)),
		messages:       parseFieldPaths(fieldOrDefault(fields.Messages, defaultCustomMessagesFields)),
		role:           parseFieldPaths(fieldOrDefault(fields.Role, defaultCustomRoleFields)),
		content:        parseFieldPaths(fieldOrDefault(fields.Content, defaultCustomContentFields)),
		timestamp:      parseFieldPaths(fieldOrDefault(fields.Timestamp, defaultCustomTimestampFields)),
		model:          parseFieldPaths(fieldOrDefault(fields.Model, defaultCustomModelFields)),
		userRoles:      parseRoleSet(fieldOrDefault(cfg.UserRoles, defaultCustomUserRoles)),
		assistantRoles: parseRoleSet(fieldOrDefault(cfg.AssistantRoles, defaultCustomAssistantRoles)),
	}
	if name := filepath.Base(strings.TrimSpace(cfg.ProcessName)); customProcessNamePattern.MatchString(name) && name != "." && name != ".." {
		spec.processName = name
	}
	key, _ := json.Marshal([]any{
		spec.root, spec.glob, spec.format, spec.sessionID, spec.cwd, spec.title, spec.messages,
		spec.role, spec.content, spec.timestamp, spec.model,
		sortedRoleKeys(spec.userRoles), sortedRoleKeys(spec.assistantRoles),
	})
	spec.key = string(key)
	return spec, nil
}

func sortedRoleKeys(roles map[string]struct{}) []string {
	out := make([]string, 0, len(roles))
	for role := range roles {
		out = append(out, role)
	}
	sort.Strings(out)
	return out
}

// matches 判断相对 root 的路径是否命中规则：含 "/" 时按相对路径匹配，否则只匹配文件名。
func (s *customSessionSpec) matches(rel string) bool {
	rel = filepath.ToSlash(rel)
	target := path.Base(rel)
	if strings.Contains(s.glob, "/") {
		target = rel
	}
	ok, err := path.Match(s.glob, target)
	return err == nil && ok
}

// collect 在 root 下按规则收集会话文件，限制目录深度与遍历条目数，按修改时间倒序取前 N 个。
func (s *customSessionSpec) collect() []string {
	type candidate struct {
		path  string
		mtime int64
	}
	var found []candidate
	visited := 0
	_ = filepath.WalkDir(s.root, func(current string, entry fs.DirEntry, err error) error {
		if err != nil || entry == nil {
			return nil
		}
		visited++
		if visited > customSessionMaxWalk {
			return fs.SkipAll
		}
		if current == s.root {
			return nil
		}
		rel, relErr := filepath.Rel(s.root, current)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			name := entry.Name()
			if name == ".git" || name == "node_modules" || strings.Count(rel, "/")+1 > customSessionMaxDepth {
				return fs.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() || !s.matches(rel) {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return nil
		}
		found = append(found, candidate{path: current, mtime: info.ModTime().UnixNano()})
		return nil
	})
	sort.SliceStable(found, func(i, j int) bool { return found[i].mtime > found[j].mtime })
	if len(found) > customSessionMaxFiles {
		found = found[:customSessionMaxFiles]
	}
	out := make([]string, 0, len(found))
	for _, item := range found {
		out = append(out, item.path)
	}
	return out
}

// resolveFile 校验前端回传的文件路径位于 root 内、命中匹配规则且为普通文件。
func (s *customSessionSpec) resolveFile(filePath string) (string, error) {
	resolved, err := ensureUnderRoot(filePath, s.root)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(s.root, resolved)
	if err != nil || !s.matches(rel) {
		return "", fmt.Errorf("文件不匹配该会话来源的规则")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("会话文件不存在")
	}
	return resolved, nil
}

// ─── JSON 值读取 ─────────────────────────────────────────────────────────────

func decodeCustomJSON(data []byte) (any, bool) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return nil, false
	}
	return value, true
}

func resolveCustomPath(value any, segments []string) (any, bool) {
	current := value
	for _, segment := range segments {
		switch node := current.(type) {
		case map[string]any:
			next, ok := node[segment]
			if !ok {
				return nil, false
			}
			current = next
		case []any:
			index, err := strconv.Atoi(segment)
			if err != nil || index < 0 || index >= len(node) {
				return nil, false
			}
			current = node[index]
		default:
			return nil, false
		}
	}
	return current, current != nil
}

func customScalar(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case json.Number:
		return v.String()
	default:
		return ""
	}
}

func firstCustomScalar(record any, candidates [][]string) string {
	for _, candidate := range candidates {
		if value, ok := resolveCustomPath(record, candidate); ok {
			if text := customScalar(value); text != "" {
				return text
			}
		}
	}
	return ""
}

// customText 提取可见文本：字符串直接返回；内容块数组拼接 text/content；跳过思考与工具块。
func customText(value any, depth int) string {
	if depth > 4 {
		return ""
	}
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			if text := customText(item, depth+1); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		switch strings.ToLower(customScalar(v["type"])) {
		case "thinking", "reasoning", "redacted_thinking", "tool_use", "tool_result", "function_call", "function_call_output":
			return ""
		}
		for _, key := range []string{"text", "content", "parts"} {
			if text := customText(v[key], depth+1); text != "" {
				return text
			}
		}
	}
	return ""
}

// normalizeCustomTimestamp 把 Unix 秒/毫秒数值转成 RFC3339，其余字符串原样返回。
func normalizeCustomTimestamp(value string) string {
	number, err := strconv.ParseFloat(value, 64)
	if err != nil || number <= 0 {
		return value
	}
	switch {
	case number >= 1e12:
		return time.UnixMilli(int64(number)).UTC().Format(time.RFC3339Nano)
	case number >= 1e9:
		return time.Unix(int64(number), 0).UTC().Format(time.RFC3339)
	default:
		return value
	}
}

func (s *customSessionSpec) mapRole(raw string) string {
	role := strings.ToLower(strings.TrimSpace(raw))
	if role == "" {
		return ""
	}
	if _, ok := s.userRoles[role]; ok {
		return customSessionRoleUser
	}
	if _, ok := s.assistantRoles[role]; ok {
		return customSessionRoleAssistant
	}
	return ""
}

func (s *customSessionSpec) message(record any) (claudeSessionMessage, bool) {
	role := ""
	for _, candidate := range s.role {
		if value, ok := resolveCustomPath(record, candidate); ok {
			if role = s.mapRole(customScalar(value)); role != "" {
				break
			}
		}
	}
	if role == "" {
		return claudeSessionMessage{}, false
	}
	text := ""
	for _, candidate := range s.content {
		if value, ok := resolveCustomPath(record, candidate); ok {
			if text = customText(value, 0); text != "" {
				break
			}
		}
	}
	if text == "" {
		return claudeSessionMessage{}, false
	}
	message := claudeSessionMessage{Role: role, Content: text, MsgType: customSessionMessageTypeNormal}
	if timestamp := normalizeCustomTimestamp(firstCustomScalar(record, s.timestamp)); timestamp != "" {
		message.Timestamp = &timestamp
	}
	if model := firstCustomScalar(record, s.model); model != "" {
		message.Model = &model
	}
	return message, true
}

// ─── 文件解析 ────────────────────────────────────────────────────────────────

type customParsedSession struct {
	id        string
	cwd       string
	title     string
	model     string
	firstUser string
	lastRole  string
	count     uint32
	messages  []claudeSessionMessage
}

func (s *customSessionSpec) absorbMetadata(parsed *customParsedSession, record any) {
	if parsed.id == "" {
		parsed.id = firstCustomScalar(record, s.sessionID)
	}
	if parsed.cwd == "" {
		parsed.cwd = firstCustomScalar(record, s.cwd)
	}
	if parsed.title == "" {
		parsed.title = firstCustomScalar(record, s.title)
	}
}

func (s *customSessionSpec) absorbMessage(parsed *customParsedSession, record any, keep bool) {
	message, ok := s.message(record)
	if !ok {
		return
	}
	parsed.count++
	parsed.lastRole = message.Role
	if parsed.firstUser == "" && message.Role == customSessionRoleUser {
		parsed.firstUser = message.Content
	}
	if parsed.model == "" && message.Model != nil {
		parsed.model = *message.Model
	}
	if keep {
		parsed.messages = append(parsed.messages, message)
	}
}

func (s *customSessionSpec) parseFile(filePath string, keepMessages bool) (*customParsedSession, error) {
	parsed := &customParsedSession{}
	if s.format == customSessionFormatJSON {
		info, err := os.Stat(filePath)
		if err != nil {
			return nil, err
		}
		if info.Size() > customSessionMaxJSONBytes {
			return nil, fmt.Errorf("会话文件过大：%s", filePath)
		}
		data, err := os.ReadFile(filePath)
		if err != nil {
			return nil, err
		}
		document, ok := decodeCustomJSON(data)
		if !ok {
			return nil, fmt.Errorf("无法解析 JSON：%s", filePath)
		}
		var items []any
		if list, isList := document.([]any); isList {
			items = list
		} else {
			s.absorbMetadata(parsed, document)
			for _, candidate := range s.messages {
				if value, found := resolveCustomPath(document, candidate); found {
					if list, isList := value.([]any); isList {
						items = list
						break
					}
				}
			}
		}
		for _, item := range items {
			s.absorbMessage(parsed, item, keepMessages)
		}
		return parsed, nil
	}

	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), customSessionMaxLineBytes)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		record, ok := decodeCustomJSON(line)
		if !ok {
			continue
		}
		s.absorbMetadata(parsed, record)
		s.absorbMessage(parsed, record, keepMessages)
	}
	return parsed, nil
}

func truncateRunes(text string, max int) string {
	runes := []rune(strings.Join(strings.Fields(text), " "))
	if len(runes) <= max {
		return string(runes)
	}
	return string(runes[:max]) + "…"
}

func (s *customSessionSpec) summarize(filePath string) *codexSession {
	parsed, err := s.parseFile(filePath, false)
	if err != nil || parsed.count == 0 {
		return nil
	}
	id := parsed.id
	if id == "" {
		id = strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
	}
	title := truncateRunes(parsed.title, customSessionTitleRunes)
	if title == "" && parsed.firstUser != "" {
		title = firstSentenceTitle(parsed.firstUser, 80)
	}
	if title == "" {
		title = idPrefixTitle(id)
	}
	status := ""
	switch parsed.lastRole {
	case customSessionRoleUser:
		status = customSessionStatusInProgress
	case customSessionRoleAssistant:
		status = customSessionStatusCompleted
	}
	return &codexSession{
		ID:           id,
		Title:        title,
		LastAt:       fileMtimeMs(filePath),
		MessageCount: parsed.count,
		Model:        parsed.model,
		Cwd:          parsed.cwd,
		FilePath:     filePath,
		Status:       status,
	}
}

// ─── 缓存与命令入口 ──────────────────────────────────────────────────────────

var customSessionCaches = struct {
	sync.Mutex
	byKey map[string]*claudeFileCache[*codexSession]
}{byKey: map[string]*claudeFileCache[*codexSession]{}}

func customSessionCache(spec *customSessionSpec) *claudeFileCache[*codexSession] {
	customSessionCaches.Lock()
	defer customSessionCaches.Unlock()
	if cache, ok := customSessionCaches.byKey[spec.key]; ok {
		return cache
	}
	if len(customSessionCaches.byKey) >= customSessionCacheConfigs {
		clear(customSessionCaches.byKey)
	}
	cache := newClaudeFileCache(spec.summarize)
	customSessionCaches.byKey[spec.key] = cache
	return cache
}

func listCustomSessions(cfg *customSessionConfig) ([]codexSession, error) {
	spec, err := newCustomSessionSpec(cfg)
	if err != nil {
		return nil, err
	}
	cache := customSessionCache(spec)
	sessions := make([]codexSession, 0)
	for _, filePath := range spec.collect() {
		if session := cache.get(filePath); session != nil {
			sessions = append(sessions, *session)
		}
	}
	sortSessionsByLastAtDesc(sessions)
	if spec.processName != "" {
		reconcileSessionStatusesByProcess(spec.processName, sessions)
	}
	return sessions, nil
}

func customSessionMessages(cfg *customSessionConfig, filePath string, limit int) ([]claudeSessionMessage, error) {
	spec, err := newCustomSessionSpec(cfg)
	if err != nil {
		return nil, err
	}
	resolved, err := spec.resolveFile(filePath)
	if err != nil {
		return nil, err
	}
	parsed, err := spec.parseFile(resolved, true)
	if err != nil {
		return nil, err
	}
	if parsed.messages == nil {
		return []claudeSessionMessage{}, nil
	}
	return tailMessages(parsed.messages, limit), nil
}

func deleteCustomSession(cfg *customSessionConfig, filePath string) error {
	spec, err := newCustomSessionSpec(cfg)
	if err != nil {
		return err
	}
	resolved, err := spec.resolveFile(filePath)
	if err != nil {
		return err
	}
	if err := os.Remove(resolved); err != nil {
		return fmt.Errorf("删除失败：%v", err)
	}
	return nil
}
