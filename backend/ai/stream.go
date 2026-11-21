package ai

// AI 流式对话：对齐 Tauri src-tauri/src/ai.rs 的 ai_stream_chat。
// 与 Tauri Channel 不同，宿主侧由前端传入 channelId，Go 通过
// core.Emit("stream:"+channelId, event) 推送流式事件（对齐 platform 抽象层
// streamInvoke 的通用通道约定）；handler 本身阻塞执行至流结束（与 Tauri
// invoke 的 Promise 语义一致）。
//
// 事件契约（与 Rust StreamEvent serde tag="type" snake_case 一致）：
//   {type:"chunk",text} | {type:"done"} | {type:"error",message}
//   | {type:"tool_call",id,name,arguments} | {type:"truncated"}
//   | {type:"thinking_block",blockType,thinking,signature,data}（仅 Anthropic）

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"mincode/app/backend/core"
)

func init() {
	core.Register("ai_stream_chat", aiStreamChatHandler)
	core.Register("ai_stream_cancel", aiStreamCancelHandler)
	core.HandleStream("ai-chat", aiChatStreamHandler)
	core.Register("ai_test_connection", aiTestConnectionHandler)
}

// aiSSEBufMax SSE 单行缓冲上限，防御恶意/异常服务端发送无 \n 的超大行。
const aiSSEBufMax = 4 * 1024 * 1024

// aiStreamIdleTimeout is enforced in Go so background WebView throttling cannot
// leave a provider connection hanging indefinitely.
const aiStreamIdleTimeout = 5 * time.Minute

var errAIStreamIdleTimeout = errors.New("The AI response stream stopped producing data.")

const missingToolResponse = "[工具未执行或已取消]"

// ── 数据结构 ─────────────────────────────────────────────────────────────────

// chatMessage 对齐 Tauri ChatMessage。
type chatMessage struct {
	Role        string         `json:"role"`
	Content     string         `json:"content"`
	Attachments []aiAttachment `json:"attachments"`
	ToolCalls   []toolCallInfo `json:"toolCalls"`
	ToolCallID  string         `json:"toolCallId"`
	// ThinkingBlocks 仅 Anthropic 使用：开启思考的工具循环必须回传上一轮带签名的思考块。
	ThinkingBlocks []thinkingBlock `json:"thinkingBlocks"`
}

// thinkingBlock 对应 Anthropic 的 thinking / redacted_thinking 内容块。
type thinkingBlock struct {
	Type      string `json:"type"`
	Thinking  string `json:"thinking"`
	Signature string `json:"signature"`
	Data      string `json:"data"`
}

type toolCallInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type aiAttachment struct {
	Name     string `json:"name"`
	MimeType string `json:"mimeType"`
	Size     uint64 `json:"size"`
	Kind     string `json:"kind"`
	Data     string `json:"data"`
}

type aiStreamChatArgs struct {
	ChannelID string         `json:"channelId"`
	Config    aiStreamConfig `json:"config"`
	Messages  []chatMessage  `json:"messages"`
}

// streamEmitter 将流式事件推送到前端：emit 决定输出通道
// （事件通道 core.Emit 或 Streams 连接），批处理逻辑与通道无关。
// 正文与推理各自在 16ms 窗口内合并；两者交替时先推送另一类已缓冲内容，
// 保证前端看到的事件顺序与供应商原始增量顺序一致。
type streamEmitter struct {
	emit     func(map[string]any)
	mu       sync.Mutex
	text     strings.Builder
	thinking strings.Builder
	timer    *time.Timer
	// closed 在终止事件或取消后置位，之后到期的批处理定时器与迟到增量一律丢弃。
	// 使用原子值：取消时连接写入可能正阻塞并持有 mu，discard 不能等锁。
	closed atomic.Bool
}

const streamEmitterFlushDelay = 16 * time.Millisecond

// newEventEmitter 构造经 前端事件通道输出的发射器（兼容旧链路）。
func newEventEmitter(event string) *streamEmitter {
	return &streamEmitter{emit: func(payload map[string]any) { core.Emit(event, payload) }}
}

func (e *streamEmitter) chunk(text string) {
	if text == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed.Load() {
		return
	}
	e.emitThinkingLocked()
	e.text.WriteString(text)
	e.armLocked()
}

func (e *streamEmitter) reasoning(text string) {
	if text == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed.Load() {
		return
	}
	e.emitTextLocked()
	e.thinking.WriteString(text)
	e.armLocked()
}

func (e *streamEmitter) armLocked() {
	if e.timer == nil {
		e.timer = time.AfterFunc(streamEmitterFlushDelay, e.flush)
	}
}

func (e *streamEmitter) emitTextLocked() {
	if e.text.Len() == 0 {
		return
	}
	text := e.text.String()
	e.text.Reset()
	e.emit(map[string]any{"type": "chunk", "text": text})
}

func (e *streamEmitter) emitThinkingLocked() {
	if e.thinking.Len() == 0 {
		return
	}
	text := e.thinking.String()
	e.thinking.Reset()
	e.emit(map[string]any{"type": "reasoning", "text": text})
}

func (e *streamEmitter) stopTimerLocked() {
	if e.timer != nil {
		e.timer.Stop()
	}
	e.timer = nil
}

func (e *streamEmitter) flushLocked() {
	e.stopTimerLocked()
	if e.closed.Load() {
		return
	}
	e.emitThinkingLocked()
	e.emitTextLocked()
}

// flush 将短时间窗口内的增量合并成一次输出。任一时刻至多一类缓冲非空
// （交替时已先推送另一类），这里的先后顺序只是兜底。
func (e *streamEmitter) flush() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.flushLocked()
}

// send 先推送已缓冲的增量，再推送 payload；terminal 为 true 时关闭发射器。
func (e *streamEmitter) send(payload map[string]any, terminal bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed.Load() {
		return
	}
	e.flushLocked()
	e.emit(payload)
	if terminal {
		e.closed.Store(true)
	}
}

// discard 在请求取消后丢弃尚未推送的增量，并阻止之后的任何输出。
func (e *streamEmitter) discard() {
	e.closed.Store(true)
	if !e.mu.TryLock() {
		// 持锁方（阻塞中的写入）返回后会看到 closed，残留定时器到期也只会空转。
		return
	}
	defer e.mu.Unlock()
	e.stopTimerLocked()
	e.text.Reset()
	e.thinking.Reset()
}

func (e *streamEmitter) done() {
	e.send(map[string]any{"type": "done"}, true)
}

func (e *streamEmitter) error(message string) {
	e.send(map[string]any{"type": "error", "message": message}, true)
}

func (e *streamEmitter) toolCall(id, name, arguments string) {
	e.send(map[string]any{
		"type": "tool_call", "id": id, "name": name, "arguments": arguments,
	}, false)
}

// thinkingBlock 推送带签名的完整思考块；Anthropic 要求在工具循环的下一次请求中原样回传。
func (e *streamEmitter) thinkingBlock(block thinkingBlock) {
	e.send(map[string]any{
		"type": "thinking_block", "blockType": block.Type, "thinking": block.Thinking,
		"signature": block.Signature, "data": block.Data,
	}, false)
}

// toolCallProgressEvent 告知前端某个工具调用正在生成：名字一出现就推送，之后参数每增长约 1KB 再推送一次。
// 只带长度不带参数内容（半截 JSON 不可用）；最终的 tool_call 仍是唯一权威。index 只在本次响应内唯一。
func (e *streamEmitter) toolCallProgressEvent(index int, name string, bytes int) {
	e.send(map[string]any{
		"type": "tool_call_progress", "index": index, "name": name, "bytes": bytes,
	}, false)
}

func (e *streamEmitter) truncated() {
	e.send(map[string]any{"type": "truncated"}, false)
}

type aiTokenUsage struct {
	InputTokens       uint64
	OutputTokens      uint64
	CachedInputTokens uint64
	CacheWriteTokens  uint64
	ReasoningTokens   uint64
}

func (u aiTokenUsage) empty() bool {
	return u.InputTokens == 0 && u.OutputTokens == 0 && u.CachedInputTokens == 0 &&
		u.CacheWriteTokens == 0 && u.ReasoningTokens == 0
}

func (e *streamEmitter) usage(usage aiTokenUsage, model string) {
	if usage.empty() {
		return
	}
	e.send(map[string]any{
		"type": "usage", "inputTokens": usage.InputTokens,
		"outputTokens": usage.OutputTokens, "cachedInputTokens": usage.CachedInputTokens,
		"cacheWriteTokens": usage.CacheWriteTokens, "reasoningTokens": usage.ReasoningTokens,
		"requestCount": 1, "model": model,
	}, false)
}

type aiStreamState struct {
	mu       sync.Mutex
	cancel   context.CancelFunc
	canceled bool
}

func (s *aiStreamState) register(cancel context.CancelFunc) (registered bool, preCanceled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.canceled {
		cancel()
		return false, true
	}
	if s.cancel != nil {
		return false, false
	}
	s.cancel = cancel
	return true, false
}

func (s *aiStreamState) cancelStream() {
	s.mu.Lock()
	s.canceled = true
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

var activeAIStreams sync.Map // channelId -> *aiStreamState

// ── 校验与客户端 ─────────────────────────────────────────────────────────────

// validateAiBaseURL 对齐 Rust validate_ai_base_url。
func validateAiBaseURL(raw string) error {
	if err := core.ValidateRequestURL(raw); err != nil {
		return fmt.Errorf("AI base_url 校验失败: %w", err)
	}
	return nil
}

// aiStreamTransport 所有 AI 请求共用一个 Transport，跨请求复用 TCP/TLS 连接并启用
// HTTP/2（自定义 DialContext 时必须显式 ForceAttemptHTTP2）。与原先逐请求新建的
// Transport 语义一致：不读取环境代理（Proxy 为 nil），连接超时 30s，响应头超时 120s；
// 空闲连接 90s 后回收，避免后台 goroutine 与连接堆积。
var aiStreamTransport = &http.Transport{
	DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
	ForceAttemptHTTP2:     true,
	MaxIdleConns:          32,
	MaxIdleConnsPerHost:   4,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   30 * time.Second,
	ResponseHeaderTimeout: 120 * time.Second,
	ExpectContinueTimeout: time.Second,
}

// aiStreamClient 流式专用客户端：不设整体 Timeout（流式响应时长不可预估）；
// 重定向时复检云元数据端点。
var aiStreamClient = &http.Client{
	Transport:     aiStreamTransport,
	CheckRedirect: aiCheckRedirect,
}

// aiCredentialHeaders 是各供应商携带密钥的请求头。Go 只会在跨域重定向时剥离
// Authorization，x-api-key / api-key 会被原样转发，必须手动移除。
var aiCredentialHeaders = []string{"Authorization", "X-Api-Key", "Api-Key"}

// aiCheckRedirect 拒绝云元数据端点、https→http 降级与过多跳转；跳转到其他主机时
// 移除密钥头，避免 API Key 泄露给第三方。
func aiCheckRedirect(req *http.Request, via []*http.Request) error {
	if core.IsCloudMetadataHost(req.URL.Hostname()) {
		return errors.New("AI 重定向到云元数据端点已被拒绝")
	}
	if len(via) >= 10 {
		return errors.New("重定向次数过多")
	}
	if len(via) > 0 && strings.EqualFold(via[0].URL.Scheme, "https") && !strings.EqualFold(req.URL.Scheme, "https") {
		return errors.New("AI 重定向从 https 降级到 http 已被拒绝")
	}
	if len(via) > 0 && !strings.EqualFold(req.URL.Host, via[0].URL.Host) {
		for _, header := range aiCredentialHeaders {
			req.Header.Del(header)
		}
	}
	return nil
}

// aiTimeoutHTTPClient 返回共用 Transport 与重定向策略、带整体超时的非流式客户端。
func aiTimeoutHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Transport: aiStreamTransport, CheckRedirect: aiCheckRedirect, Timeout: timeout}
}

func aiStreamHTTPClient() *http.Client {
	return aiStreamClient
}

// ── 消息构造 ─────────────────────────────────────────────────────────────────

// textWithFileAttachments 对齐 Rust text_with_file_attachments。
func textWithFileAttachments(m *chatMessage) string {
	text := m.Content
	if strings.TrimSpace(text) == "" {
		text = "请分析附件。"
	}
	var b strings.Builder
	b.WriteString(text)
	for _, item := range m.Attachments {
		if item.Kind == "text" {
			b.WriteString(fmt.Sprintf("\n\n## 文本附件：%s\n```text\n%s\n```", item.Name, item.Data))
		}
	}
	return b.String()
}

// sanitizeChatMessages 对齐 Rust sanitize_chat_messages：确保 assistant 的每个
// tool_call 都有对应 tool 回复，丢弃孤立 tool 消息。
func approximateMessageTokens(message *chatMessage) uint64 {
	chars := uint64(len([]rune(message.Content)))
	for _, attachment := range message.Attachments {
		if attachment.Kind == "text" {
			chars += uint64(len([]rune(attachment.Data)))
		}
	}
	for _, call := range message.ToolCalls {
		chars += uint64(len([]rune(call.Name)) + len([]rune(call.Arguments)))
	}
	return (chars+3)/4 + 8
}

func limitChatMessages(messages []chatMessage, config *aiStreamConfig) ([]chatMessage, error) {
	inputLimit := uint64(config.MaxInputTokens)
	if config.ContextWindow > 0 {
		output := uint64(config.MaxOutputTokens)
		if output >= uint64(config.ContextWindow) {
			return nil, errors.New("最大输出 Token 必须小于上下文窗口")
		}
		contextInput := uint64(config.ContextWindow) - output
		if inputLimit == 0 || contextInput < inputLimit {
			inputLimit = contextInput
		}
	}
	if inputLimit == 0 {
		return messages, nil
	}

	// The leading system messages (workspace context, instructions, compaction
	// summary) are kept first; the most recent messages fill the rest of the
	// budget. The tail is always sent: the last non-system message plus any
	// system messages after it (e.g. the plan-mode reminder), so a trailing
	// reminder never displaces the user's latest message.
	prefix := 0
	for prefix < len(messages)-1 && messages[prefix].Role == "system" {
		prefix++
	}
	prefixCost := uint64(0)
	for i := 0; i < prefix; i++ {
		prefixCost += approximateMessageTokens(&messages[i])
	}
	bodyBudget := uint64(0)
	if prefixCost < inputLimit {
		bodyBudget = inputLimit - prefixCost
	}

	tail := len(messages) - 1
	for tail > prefix && messages[tail].Role == "system" {
		tail--
	}
	if tail >= prefix && messages[tail].Role == "system" {
		tail = len(messages) - 1 // only system messages after the prefix
	}
	body := make([]chatMessage, 0, len(messages)-prefix)
	used := uint64(0)
	for i := len(messages) - 1; i >= prefix; i-- {
		cost := approximateMessageTokens(&messages[i])
		if i < tail && used+cost > bodyBudget {
			break
		}
		body = append(body, messages[i])
		used += cost
	}
	for left, right := 0, len(body)-1; left < right; left, right = left+1, right-1 {
		body[left], body[right] = body[right], body[left]
	}

	kept := make([]chatMessage, 0, prefix+len(body))
	for i := 0; i < prefix; i++ {
		cost := approximateMessageTokens(&messages[i])
		if used+cost > inputLimit {
			continue
		}
		kept = append(kept, messages[i])
		used += cost
	}
	return append(kept, body...), nil
}

func sanitizeChatMessages(messages []chatMessage) []chatMessage {
	out := make([]chatMessage, 0, len(messages))
	i := 0

	for i < len(messages) {
		message := messages[i]

		if message.Role == "assistant" && len(message.ToolCalls) > 0 {
			toolCalls := make([]toolCallInfo, 0, len(message.ToolCalls))
			for _, tc := range message.ToolCalls {
				if strings.TrimSpace(tc.ID) == "" || strings.TrimSpace(tc.Name) == "" {
					continue
				}
				toolCalls = append(toolCalls, toolCallInfo{
					ID:        strings.TrimSpace(tc.ID),
					Name:      strings.TrimSpace(tc.Name),
					Arguments: tc.Arguments,
				})
			}

			if len(toolCalls) == 0 {
				if strings.TrimSpace(message.Content) != "" {
					out = append(out, chatMessage{
						Role:        message.Role,
						Content:     message.Content,
						Attachments: message.Attachments,
					})
				}
				i++
				continue
			}

			responses := map[string]chatMessage{}
			j := i + 1
			for j < len(messages) && messages[j].Role == "tool" {
				id := strings.TrimSpace(messages[j].ToolCallID)
				if id != "" {
					responses[id] = messages[j]
				}
				j++
			}

			out = append(out, chatMessage{
				Role:           message.Role,
				Content:        message.Content,
				Attachments:    message.Attachments,
				ToolCalls:      toolCalls,
				ThinkingBlocks: message.ThinkingBlocks,
			})

			for _, tc := range toolCalls {
				content := missingToolResponse
				var attachments []aiAttachment
				if resp, ok := responses[tc.ID]; ok {
					content = resp.Content
					attachments = resp.Attachments
				}
				out = append(out, chatMessage{
					Role:        "tool",
					Content:     content,
					Attachments: attachments,
					ToolCallID:  tc.ID,
				})
			}

			i = j
			continue
		}

		if message.Role == "tool" {
			i++
			continue
		}

		out = append(out, message)
		i++
	}

	return out
}

func normalizedImageAttachment(item aiAttachment) (aiAttachment, bool) {
	mimeType := strings.ToLower(strings.TrimSpace(item.MimeType))
	data := strings.TrimSpace(item.Data)
	if item.Kind != "image" || !strings.HasPrefix(mimeType, "image/") || data == "" {
		return aiAttachment{}, false
	}

	if strings.HasPrefix(strings.ToLower(data), "data:") {
		comma := strings.IndexByte(data, ',')
		if comma < 0 {
			return aiAttachment{}, false
		}
		header := data[len("data:"):comma]
		parts := strings.Split(header, ";")
		if len(parts) < 2 || !strings.EqualFold(strings.TrimSpace(parts[len(parts)-1]), "base64") {
			return aiAttachment{}, false
		}
		embeddedMimeType := strings.ToLower(strings.TrimSpace(parts[0]))
		if !strings.HasPrefix(embeddedMimeType, "image/") {
			return aiAttachment{}, false
		}
		mimeType = embeddedMimeType
		data = data[comma+1:]
	}

	data = strings.Join(strings.Fields(data), "")
	if data == "" {
		return aiAttachment{}, false
	}
	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(data)
	}
	if err != nil || len(decoded) == 0 {
		return aiAttachment{}, false
	}

	detectedMimeType := http.DetectContentType(decoded)
	switch detectedMimeType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		return aiAttachment{}, false
	}

	item.MimeType = detectedMimeType
	item.Data = base64.StdEncoding.EncodeToString(decoded)
	return item, true
}

func hasImageAttachments(m *chatMessage) bool {
	for _, item := range m.Attachments {
		if _, ok := normalizedImageAttachment(item); ok {
			return true
		}
	}
	return false
}

// openaiMessageJSON 对齐 Rust openai_message_json。
func openaiMessageJSON(m *chatMessage) map[string]any {
	// tool role 消息（工具执行结果）
	if m.Role == "tool" {
		toolCallID := strings.TrimSpace(m.ToolCallID)
		if toolCallID == "" {
			return map[string]any{"role": "user", "content": m.Content}
		}
		return map[string]any{
			"role":         "tool",
			"content":      m.Content,
			"tool_call_id": toolCallID,
		}
	}

	// assistant 消息带 tool_calls
	if m.Role == "assistant" && len(m.ToolCalls) > 0 {
		tcArray := make([]map[string]any, 0, len(m.ToolCalls))
		for _, tc := range m.ToolCalls {
			if strings.TrimSpace(tc.ID) == "" || strings.TrimSpace(tc.Name) == "" {
				continue
			}
			tcArray = append(tcArray, map[string]any{
				"id":   tc.ID,
				"type": "function",
				"function": map[string]any{
					"name":      tc.Name,
					"arguments": tc.Arguments,
				},
			})
		}
		if len(tcArray) == 0 {
			return map[string]any{"role": "assistant", "content": textWithFileAttachments(m)}
		}
		val := map[string]any{"role": "assistant", "tool_calls": tcArray}
		// OpenAI 兼容 API 要求 tool_calls 消息 content 为 null 或字符串
		if m.Content == "" {
			val["content"] = nil
		} else {
			val["content"] = m.Content
		}
		return val
	}

	if !hasImageAttachments(m) {
		return map[string]any{"role": m.Role, "content": textWithFileAttachments(m)}
	}

	content := []map[string]any{
		{"type": "text", "text": textWithFileAttachments(m)},
	}
	for _, item := range m.Attachments {
		image, ok := normalizedImageAttachment(item)
		if !ok {
			continue
		}
		content = append(content, map[string]any{
			"type": "image_url",
			"image_url": map[string]any{
				"url":    fmt.Sprintf("data:%s;base64,%s", image.MimeType, image.Data),
				"detail": "auto",
			},
		})
	}
	return map[string]any{"role": m.Role, "content": content}
}

func openaiMessagesJSON(messages []chatMessage) []map[string]any {
	items := make([]map[string]any, 0, len(messages)+2)
	for i := range messages {
		message := &messages[i]
		items = append(items, openaiMessageJSON(message))
		if message.Role != "tool" || !hasImageAttachments(message) {
			continue
		}
		observation := chatMessage{
			Role:        "user",
			Content:     "Visual observation returned by the preceding Computer Use tool.",
			Attachments: message.Attachments,
		}
		items = append(items, openaiMessageJSON(&observation))
	}
	return items
}

// openAIProviderToolName 确保持久化工具调用满足 OpenAI 名称契约。
// 当前工具由前端传输层规范化；此处兜底旧客户端和已卸载工具留下的历史会话。
func openAIProviderToolName(name string) string {
	name = strings.TrimSpace(name)
	valid := name != ""
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		valid = false
		break
	}
	if valid {
		return name
	}

	var normalized strings.Builder
	previousReplacement := false
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-' {
			normalized.WriteRune(r)
			previousReplacement = false
			continue
		}
		if !previousReplacement {
			normalized.WriteByte('_')
			previousReplacement = true
		}
	}
	result := strings.Trim(normalized.String(), "_")
	if result == "" {
		return "tool"
	}
	return result
}

// openaiResponsesInput 对齐 Rust openai_responses_input_json。
func openaiResponsesInput(messages []chatMessage) []map[string]any {
	items := make([]map[string]any, 0, len(messages))
	for i := range messages {
		message := &messages[i]
		if message.Role == "tool" && strings.TrimSpace(message.ToolCallID) != "" {
			items = append(items, map[string]any{
				"type": "function_call_output", "call_id": strings.TrimSpace(message.ToolCallID),
				"output": message.Content,
			})
			if hasImageAttachments(message) {
				content := []map[string]any{{"type": "input_text", "text": "Visual observation returned by the preceding Computer Use tool."}}
				for _, attachment := range message.Attachments {
					image, ok := normalizedImageAttachment(attachment)
					if !ok {
						continue
					}
					content = append(content, map[string]any{
						"type":      "input_image",
						"image_url": fmt.Sprintf("data:%s;base64,%s", image.MimeType, image.Data),
					})
				}
				items = append(items, map[string]any{"type": "message", "role": "user", "content": content})
			}
			continue
		}
		if message.Role == "assistant" {
			text := message.Content
			if len(message.ToolCalls) == 0 {
				text = textWithFileAttachments(message)
			}
			if len(message.ToolCalls) == 0 || strings.TrimSpace(text) != "" {
				items = append(items, map[string]any{
					"type": "message", "role": "assistant",
					"content": []map[string]any{{"type": "output_text", "text": text}},
				})
			}
			for _, tc := range message.ToolCalls {
				if strings.TrimSpace(tc.ID) == "" || strings.TrimSpace(tc.Name) == "" {
					continue
				}
				items = append(items, map[string]any{
					"type": "function_call", "call_id": strings.TrimSpace(tc.ID),
					"name": openAIProviderToolName(tc.Name), "arguments": tc.Arguments,
				})
			}
			continue
		}
		role := message.Role
		if role == "tool" {
			role = "user"
		}
		content := []map[string]any{{"type": "input_text", "text": textWithFileAttachments(message)}}
		for _, attachment := range message.Attachments {
			image, ok := normalizedImageAttachment(attachment)
			if !ok {
				continue
			}
			content = append(content, map[string]any{
				"type":      "input_image",
				"image_url": fmt.Sprintf("data:%s;base64,%s", image.MimeType, image.Data),
			})
		}
		items = append(items, map[string]any{
			"type": "message", "role": role,
			"content": content,
		})
	}
	return items
}

// anthropicContentJSON 对齐 Rust anthropic_content_json：无图返回字符串，有图返回块数组。
func anthropicContentJSON(m *chatMessage) any {
	if !hasImageAttachments(m) {
		return textWithFileAttachments(m)
	}
	content := []map[string]any{
		{"type": "text", "text": textWithFileAttachments(m)},
	}
	for _, item := range m.Attachments {
		image, ok := normalizedImageAttachment(item)
		if !ok {
			continue
		}
		content = append(content, map[string]any{
			"type": "image",
			"source": map[string]any{
				"type":       "base64",
				"media_type": image.MimeType,
				"data":       image.Data,
			},
		})
	}
	return content
}

// anthropicToolInput guarantees Anthropic's tool_use.input is always a JSON object.
// Persisted or interrupted histories can contain empty, malformed, null, array, or
// scalar arguments; replaying those values verbatim causes the entire request to fail.
func anthropicToolInput(arguments string) map[string]any {
	var input map[string]any
	if err := json.Unmarshal([]byte(arguments), &input); err != nil || input == nil {
		return map[string]any{}
	}
	return input
}

// anthropicMessageJSON 对齐 Rust anthropic_message_json（处理 tool_use / tool_result）。
func anthropicMessageJSON(m *chatMessage) map[string]any {
	if m.Role == "tool" {
		toolCallID := strings.TrimSpace(m.ToolCallID)
		if toolCallID == "" {
			return map[string]any{"role": "user", "content": m.Content}
		}
		toolContent := any(m.Content)
		if hasImageAttachments(m) {
			blocks := []map[string]any{{"type": "text", "text": m.Content}}
			for _, item := range m.Attachments {
				image, ok := normalizedImageAttachment(item)
				if !ok {
					continue
				}
				blocks = append(blocks, map[string]any{
					"type": "image",
					"source": map[string]any{
						"type": "base64", "media_type": image.MimeType, "data": image.Data,
					},
				})
			}
			toolContent = blocks
		}
		return map[string]any{
			"role": "user",
			"content": []map[string]any{{
				"type":        "tool_result",
				"tool_use_id": toolCallID,
				"content":     toolContent,
			}},
		}
	}

	if m.Role == "assistant" && len(m.ToolCalls) > 0 {
		blocks := []map[string]any{}
		if m.Content != "" {
			blocks = append(blocks, map[string]any{"type": "text", "text": m.Content})
		}
		for _, tc := range m.ToolCalls {
			if strings.TrimSpace(tc.ID) == "" || strings.TrimSpace(tc.Name) == "" {
				continue
			}
			blocks = append(blocks, map[string]any{
				"type":  "tool_use",
				"id":    strings.TrimSpace(tc.ID),
				"name":  strings.TrimSpace(tc.Name),
				"input": anthropicToolInput(tc.Arguments),
			})
		}
		if len(blocks) == 0 {
			return map[string]any{"role": "assistant", "content": anthropicContentJSON(m)}
		}
		return map[string]any{"role": "assistant", "content": blocks}
	}

	return map[string]any{"role": m.Role, "content": anthropicContentJSON(m)}
}

// openaiToolsJSON 对齐 Rust openai_tools_json。
func openaiToolsJSON(tools []aiToolDef) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  json.RawMessage(t.InputSchema),
			},
		})
	}
	return out
}

// anthropicToolsJSON 对齐 Rust anthropic_tools_json。
func anthropicToolsJSON(tools []aiToolDef) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		out = append(out, map[string]any{
			"name":         t.Name,
			"description":  t.Description,
			"input_schema": json.RawMessage(t.InputSchema),
		})
	}
	return out
}

// openaiResponsesToolsJSON 使用 Responses API 的扁平 function 工具格式。
func openaiResponsesToolsJSON(tools []aiToolDef) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		out = append(out, map[string]any{
			"type":        "function",
			"name":        t.Name,
			"description": t.Description,
			// Responses otherwise tries strict compilation, which rejects keywords
			// such as uniqueItems used in built-in and MCP tool schemas.
			"strict":     false,
			"parameters": json.RawMessage(t.InputSchema),
		})
	}
	return out
}

// ── 工具调用累积器 ────────────────────────────────────────────────────────────

// accToolCall 累积中的单个工具调用（id / name / arguments 缓冲）。
// args 用字节切片追加，避免长参数逐 token 拼接字符串带来的平方级复制。
type accToolCall struct {
	id   string
	name string
	args []byte
}

// maxStreamToolCalls 限制单次响应的工具调用数量，防止异常 index 撑爆内存。
const maxStreamToolCalls = 128

// toolCallProgressStep 是两次进度推送之间参数至少增长的字节数。
const toolCallProgressStep = 1024

// toolCallProgress 记录每个调用上次推送时的名字与参数长度，用于节流进度事件。
// 供应商在模型说完前就流出工具名和参数；推送进度让界面立刻显示“正在写入……”，
// 也让长参数生成期间持续有事件，空闲看门狗不会误判超时。
type toolCallProgress struct {
	reported map[int]toolCallProgressMark
}

type toolCallProgressMark struct {
	name  string
	bytes int
}

// observe 在名字首次出现、名字分片补全或参数增长满一步时推送进度。
func (p *toolCallProgress) observe(em *streamEmitter, index int, name string, bytes int) {
	if name == "" || index < 0 {
		return
	}
	if p.reported == nil {
		p.reported = map[int]toolCallProgressMark{}
	}
	mark, seen := p.reported[index]
	if seen && mark.name == name && bytes < mark.bytes+toolCallProgressStep {
		return
	}
	if !seen && len(p.reported) >= maxStreamToolCalls {
		return
	}
	p.reported[index] = toolCallProgressMark{name: name, bytes: bytes}
	em.toolCallProgressEvent(index, name, bytes)
}

// pendingToolCall 是流结束后统一推送的工具调用。
type pendingToolCall struct {
	id, name, args string
}

// emitToolCalls 推送流结束后收集的工具调用。输出被截断时，参数不完整的 JSON
// 会被丢弃（空参数视为合法），避免执行半截参数。返回实际推送数量。
func emitToolCalls(em *streamEmitter, calls []pendingToolCall, truncated bool) int {
	emitted := 0
	for _, call := range calls {
		if truncated && strings.TrimSpace(call.args) != "" && !json.Valid([]byte(call.args)) {
			continue
		}
		em.toolCall(call.id, call.name, call.args)
		emitted++
	}
	return emitted
}

func generatedToolCallID(index int) string {
	return fmt.Sprintf("call_%d_%s", index, strings.ReplaceAll(uuid.NewString(), "-", ""))
}

// toolCallAccumulator 累积 OpenAI 流式 tool_calls delta，流结束后统一发送。
type toolCallAccumulator struct {
	calls []accToolCall
}

func (a *toolCallAccumulator) hasNamedCall() bool {
	for _, call := range a.calls {
		if call.name != "" {
			return true
		}
	}
	return false
}

// mergeOpenAIDelta 处理 OpenAI 格式的 tool_calls delta 数组。
func (a *toolCallAccumulator) mergeOpenAIDelta(toolCalls []json.RawMessage) {
	for _, raw := range toolCalls {
		var tc struct {
			Index    *int    `json:"index"`
			ID       *string `json:"id"`
			Function struct {
				Name      *string `json:"name"`
				Arguments *string `json:"arguments"`
			} `json:"function"`
		}
		if err := json.Unmarshal(raw, &tc); err != nil {
			continue
		}
		id := ""
		if tc.ID != nil {
			id = strings.TrimSpace(*tc.ID)
		}
		idx := a.resolveIndex(tc.Index, id)
		if idx < 0 {
			continue
		}
		for len(a.calls) <= idx {
			a.calls = append(a.calls, accToolCall{})
		}
		call := &a.calls[idx]
		if id != "" {
			call.id = id
		}
		if tc.Function.Name != nil {
			call.name = mergeToolCallName(call.name, *tc.Function.Name)
		}
		if tc.Function.Arguments != nil {
			call.args = append(call.args, *tc.Function.Arguments...)
		}
	}
}

// resolveIndex 定位 delta 所属的调用：优先使用 index；部分兼容网关省略 index，
// 此时按 id 匹配，新 id 开新调用，无 id 归入最后一个调用。越界返回 -1。
func (a *toolCallAccumulator) resolveIndex(index *int, id string) int {
	if index != nil {
		if *index < 0 || *index >= maxStreamToolCalls {
			return -1
		}
		return *index
	}
	if id != "" {
		for i := range a.calls {
			if a.calls[i].id == id {
				return i
			}
		}
		if len(a.calls) >= maxStreamToolCalls {
			return -1
		}
		return len(a.calls)
	}
	if len(a.calls) == 0 {
		return 0
	}
	return len(a.calls) - 1
}

// mergeToolCallName 兼容两种上游：分片下发名称（需要拼接），或每帧重复完整名称（需要覆盖）。
func mergeToolCallName(current, incoming string) string {
	if current == "" || incoming == current || strings.HasPrefix(incoming, current) {
		return incoming
	}
	if strings.HasSuffix(current, incoming) {
		return current
	}
	return current + incoming
}

// pending 返回已累积且有名称的工具调用。
func (a *toolCallAccumulator) pending() []pendingToolCall {
	out := make([]pendingToolCall, 0, len(a.calls))
	for index, call := range a.calls {
		if call.name == "" {
			continue
		}
		id := call.id
		if id == "" {
			id = generatedToolCallID(index)
		}
		out = append(out, pendingToolCall{id: id, name: call.name, args: string(call.args)})
	}
	return out
}

// reportProgress 为累积中的每个调用推送节流后的进度。
func (a *toolCallAccumulator) reportProgress(progress *toolCallProgress, em *streamEmitter) {
	for index, call := range a.calls {
		progress.observe(em, index, call.name, len(call.args))
	}
}

// flush 将所有累积的工具调用推送给前端。
func (a *toolCallAccumulator) flush(em *streamEmitter, truncated bool) int {
	return emitToolCalls(em, a.pending(), truncated)
}

// mergeOpenAIChoiceTools 兼容标准 tool_calls、旧式 function_call，以及仅在最终帧返回的 message.tool_calls。
func mergeOpenAIChoiceTools(data string, acc *toolCallAccumulator) (finishReason string, ok bool) {
	var payload struct {
		Choices []struct {
			Delta struct {
				ToolCalls    []json.RawMessage `json:"tool_calls"`
				FunctionCall *struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function_call"`
			} `json:"delta"`
			Message struct {
				ToolCalls []json.RawMessage `json:"tool_calls"`
			} `json:"message"`
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
	}
	if json.Unmarshal([]byte(data), &payload) != nil || len(payload.Choices) == 0 {
		return "", false
	}
	choice := payload.Choices[0]
	if len(choice.Delta.ToolCalls) > 0 {
		acc.mergeOpenAIDelta(choice.Delta.ToolCalls)
	}
	if choice.Delta.FunctionCall != nil {
		legacy, _ := json.Marshal(map[string]any{
			"index": 0,
			"function": map[string]any{
				"name": choice.Delta.FunctionCall.Name, "arguments": choice.Delta.FunctionCall.Arguments,
			},
		})
		acc.mergeOpenAIDelta([]json.RawMessage{legacy})
	}
	if len(choice.Message.ToolCalls) > 0 && !acc.hasNamedCall() {
		acc.mergeOpenAIDelta(choice.Message.ToolCalls)
	}
	if choice.FinishReason != nil {
		finishReason = *choice.FinishReason
	}
	return finishReason, true
}

func validateOpenAIToolFinish(finishReason string, acc *toolCallAccumulator) error {
	if (finishReason == "tool_calls" || finishReason == "function_call") && !acc.hasNamedCall() {
		return errors.New("上游声明了工具调用，但未返回可解析的工具数据")
	}
	return nil
}

type responsesToolCallAccumulator struct {
	calls map[string]*accToolCall
	order []string
}

func newResponsesToolCallAccumulator() *responsesToolCallAccumulator {
	return &responsesToolCallAccumulator{calls: map[string]*accToolCall{}}
}

func (a *responsesToolCallAccumulator) ensure(itemID, callID, name string) *accToolCall {
	itemID = strings.TrimSpace(itemID)
	callID = strings.TrimSpace(callID)
	key := callID
	if key == "" {
		key = itemID
	}
	if key == "" {
		key = fmt.Sprintf("response_call_%d", len(a.order))
	}
	call := a.calls[callID]
	if call == nil {
		call = a.calls[itemID]
	}
	if call == nil {
		call = &accToolCall{}
		a.order = append(a.order, key)
	}
	a.calls[key] = call
	if callID != "" {
		a.calls[callID] = call
		call.id = callID
	}
	if itemID != "" {
		a.calls[itemID] = call
		if call.id == "" {
			call.id = itemID
		}
	}
	if strings.TrimSpace(name) != "" {
		call.name = strings.TrimSpace(name)
	}
	return call
}

// reportProgress 按调用出现顺序推送节流后的进度。
func (a *responsesToolCallAccumulator) reportProgress(progress *toolCallProgress, em *streamEmitter) {
	for index, key := range a.order {
		if call := a.calls[key]; call != nil {
			progress.observe(em, index, call.name, len(call.args))
		}
	}
}

func (a *responsesToolCallAccumulator) mergeItem(itemType, itemID, callID, name, arguments string) {
	if itemType != "function_call" || strings.TrimSpace(name) == "" {
		return
	}
	call := a.ensure(itemID, callID, name)
	if arguments != "" {
		call.args = append(call.args[:0], arguments...)
	}
}

func (a *responsesToolCallAccumulator) appendArguments(itemID, callID, delta string) {
	call := a.ensure(itemID, callID, "")
	call.args = append(call.args, delta...)
}

func (a *responsesToolCallAccumulator) setArguments(itemID, callID, arguments string) {
	call := a.ensure(itemID, callID, "")
	call.args = append(call.args[:0], arguments...)
}

func (a *responsesToolCallAccumulator) pending() []pendingToolCall {
	out := make([]pendingToolCall, 0, len(a.order))
	for index, key := range a.order {
		call := a.calls[key]
		if call == nil || strings.TrimSpace(call.name) == "" {
			continue
		}
		id := strings.TrimSpace(call.id)
		if id == "" {
			id = generatedToolCallID(index)
		}
		out = append(out, pendingToolCall{id: id, name: call.name, args: string(call.args)})
		if len(out) >= maxStreamToolCalls {
			break
		}
	}
	return out
}

func (a *responsesToolCallAccumulator) flush(em *streamEmitter, truncated bool) int {
	return emitToolCalls(em, a.pending(), truncated)
}

// ── SSE 流处理 ───────────────────────────────────────────────────────────────

// aiPostStream 发送 JSON POST 并返回流式响应体（含状态码校验）。
func aiPostStream(ctx context.Context, client *http.Client, url string, headers map[string]string, body any) (io.ReadCloser, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("序列化请求失败: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("构造请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求失败: %w", err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		defer res.Body.Close()
		return nil, fmt.Errorf("HTTP %d: %s", res.StatusCode, readErrorBody(res.Body))
	}
	return newIdleTimeoutReadCloser(res.Body, aiStreamIdleTimeout), nil
}

// aiErrorBodyReadLimit / aiErrorTextLimit 限制错误响应的读取量与展示长度：
// 网关常返回整页 HTML，全部读入并原样展示既浪费内存也淹没真正的错误信息。
const (
	aiErrorBodyReadLimit = 64 << 10
	aiErrorTextLimit     = 4 << 10
)

// readErrorBody 读取至多 aiErrorBodyReadLimit 字节的错误响应体并截断为可展示文本。
func readErrorBody(body io.Reader) string {
	raw, _ := io.ReadAll(io.LimitReader(body, aiErrorBodyReadLimit))
	return truncateErrorText(string(raw))
}

// truncateErrorText 按 UTF-8 边界截断错误文本。
func truncateErrorText(text string) string {
	text = strings.TrimSpace(strings.ToValidUTF8(text, "\uFFFD"))
	if len(text) <= aiErrorTextLimit {
		return text
	}
	cut := aiErrorTextLimit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…"
}

// idleTimeoutReadCloser 给每次响应体读取设置独立的空闲期限。
type idleTimeoutReadCloser struct {
	body    io.ReadCloser
	timeout time.Duration
}

func newIdleTimeoutReadCloser(body io.ReadCloser, timeout time.Duration) io.ReadCloser {
	return &idleTimeoutReadCloser{body: body, timeout: timeout}
}

func (r *idleTimeoutReadCloser) Read(buffer []byte) (int, error) {
	type readResult struct {
		n   int
		err error
	}
	result := make(chan readResult, 1)
	go func() {
		n, err := r.body.Read(buffer)
		result <- readResult{n: n, err: err}
	}()

	timer := time.NewTimer(r.timeout)
	defer timer.Stop()
	select {
	case read := <-result:
		return read.n, read.err
	case <-timer.C:
		_ = r.body.Close()
		return 0, errAIStreamIdleTimeout
	}
}

func (r *idleTimeoutReadCloser) Close() error {
	return r.body.Close()
}

func aiStreamErrorMessage(err error) string {
	if errors.Is(err, errAIStreamIdleTimeout) {
		return errAIStreamIdleTimeout.Error()
	}
	return err.Error()
}

// forEachSSEDataLine 按 SSE 事件读取流，对每个 data 负载调用 fn。
// 同一事件的多行 data 按规范以 \n 拼接；部分代理省略事件间空行，因此已缓冲内容
// 本身是完整 JSON 时，遇到下一行 data 先单独派发。收到 [DONE] 或 fn 返回 true
// （已收到供应商的终止事件）即结束读取，不再等待服务端关闭连接：部分代理在最后
// 一个事件后仍保持连接，否则要等到空闲超时才会结束。
func forEachSSEDataLine(body io.Reader, fn func(data string) (stop bool)) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), aiSSEBufMax)
	var pending []string
	pendingSize := 0
	dispatch := func() bool {
		if len(pending) == 0 {
			return false
		}
		data := strings.Join(pending, "\n")
		pending = pending[:0]
		pendingSize = 0
		return fn(data)
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if !utf8.ValidString(line) {
			return errors.New("AI SSE 包含无效 UTF-8")
		}
		if line == "" {
			if dispatch() {
				return nil
			}
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimPrefix(line[len("data:"):], " ")
		if data == "[DONE]" {
			dispatch()
			return nil
		}
		if len(pending) > 0 && json.Valid([]byte(strings.Join(pending, "\n"))) && dispatch() {
			return nil
		}
		pendingSize += len(data)
		if pendingSize > aiSSEBufMax {
			return fmt.Errorf("AI 响应行超出上限(%dB)", aiSSEBufMax)
		}
		pending = append(pending, data)
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return fmt.Errorf("AI 响应行超出上限(%dB)", aiSSEBufMax)
		}
		return fmt.Errorf("读取流失败: %w", err)
	}
	dispatch()
	return nil
}

// ── OpenAI 兼容流式 ──────────────────────────────────────────────────────────

// shouldRequestOpenAIStreamUsage keeps the compatibility default while allowing
// known-compatible proxies to opt in to provider-reported streaming usage.
func shouldRequestOpenAIStreamUsage(config *aiStreamConfig) bool {
	mode := strings.ToLower(strings.TrimSpace(config.OpenAIStreamUsage))
	return mode == "always" || (mode != "never" && strings.Contains(strings.ToLower(config.BaseURL), "api.openai.com"))
}

// isOpenAIReasoningModel 判断 o 系列 / gpt-5 / gpt-6 等推理模型：它们拒绝 max_tokens 与 temperature。
func isOpenAIReasoningModel(model string) bool {
	name := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	for _, prefix := range []string{"o1", "o3", "o4", "gpt-5", "gpt-6"} {
		if name == prefix || strings.HasPrefix(name, prefix+"-") || strings.HasPrefix(name, prefix+".") {
			return true
		}
	}
	return false
}

// openAIMaxTokensField 返回 Chat Completions 输出上限字段名。官方端点的推理模型
// 只接受 max_completion_tokens；兼容代理多数仍只识别 max_tokens。
func openAIMaxTokensField(config *aiStreamConfig) string {
	official := strings.Contains(strings.ToLower(config.BaseURL), "api.openai.com")
	if isOpenAIReasoningModel(config.Model) || (official && normalizeAIReasoningEffort(config.ReasoningEffort) != "") {
		return "max_completion_tokens"
	}
	return "max_tokens"
}

// streamErrorMessage 解析流中的错误帧：error 可能是对象 {message} 或纯字符串。
func streamErrorMessage(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var object struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	}
	if json.Unmarshal(raw, &object) == nil {
		if msg := strings.TrimSpace(object.Message); msg != "" {
			return msg
		}
		if object.Type != "" {
			return object.Type
		}
	}
	var text string
	if json.Unmarshal(raw, &text) == nil && strings.TrimSpace(text) != "" {
		return strings.TrimSpace(text)
	}
	return truncateErrorText(string(raw))
}

func streamOpenAI(ctx context.Context, client *http.Client, config *aiStreamConfig, messages []chatMessage, em *streamEmitter) error {
	url := normalizeBaseURL(config.BaseURL) + "/chat/completions"
	headers := map[string]string{}
	if config.APIKey != "" {
		headers["Authorization"] = "Bearer " + config.APIKey
	}

	chatMessages := openaiMessagesJSON(messages)

	body := map[string]any{
		"model":    config.Model,
		"messages": chatMessages,
		"stream":   true,
	}
	// 兼容代理对 stream_options 的支持不一致：自动模式仅对官方端点启用，
	// 用户可为已确认支持的代理显式开启，也可强制关闭。
	if shouldRequestOpenAIStreamUsage(config) {
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	reasoningEffort := normalizeAIReasoningEffort(config.ReasoningEffort)
	if len(config.Tools) > 0 {
		body["tools"] = openaiToolsJSON(config.Tools)
		body["tool_choice"] = "auto"
	}
	if reasoningEffort != "" {
		body["reasoning_effort"] = reasoningEffort
	}
	if config.MaxOutputTokens > 0 {
		body[openAIMaxTokensField(config)] = config.MaxOutputTokens
	}

	res, err := aiPostStream(ctx, client, url, headers, body)
	if err != nil {
		return err
	}
	defer res.Close()

	acc := &toolCallAccumulator{}
	var progress toolCallProgress
	upstreamError := ""
	truncated := false
	toolCallFinish := false
	finished := false
	usage := aiTokenUsage{}
	usageRequested := shouldRequestOpenAIStreamUsage(config)

	err = forEachSSEDataLine(res, func(data string) bool {
		var payload struct {
			Choices []struct {
				Delta struct {
					Content          *string `json:"content"`
					ReasoningContent *string `json:"reasoning_content"`
					Reasoning        *string `json:"reasoning"`
				} `json:"delta"`
			} `json:"choices"`
			Usage struct {
				PromptTokens     uint64 `json:"prompt_tokens"`
				CompletionTokens uint64 `json:"completion_tokens"`
				PromptDetails    struct {
					CachedTokens uint64 `json:"cached_tokens"`
				} `json:"prompt_tokens_details"`
				CompletionDetails struct {
					ReasoningTokens uint64 `json:"reasoning_tokens"`
				} `json:"completion_tokens_details"`
			} `json:"usage"`
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal([]byte(data), &payload) != nil {
			return false
		}
		if msg := streamErrorMessage(payload.Error); msg != "" {
			upstreamError = msg
			return true
		}
		hasUsage := payload.Usage.PromptTokens > 0 || payload.Usage.CompletionTokens > 0
		if hasUsage {
			usage = aiTokenUsage{
				InputTokens: payload.Usage.PromptTokens, OutputTokens: payload.Usage.CompletionTokens,
				CachedInputTokens: payload.Usage.PromptDetails.CachedTokens,
				ReasoningTokens:   payload.Usage.CompletionDetails.ReasoningTokens,
			}
		}
		if len(payload.Choices) == 0 {
			// 请求了用量时，用量块在 finish_reason 之后单独到达。
			return finished && hasUsage
		}
		choice := payload.Choices[0]
		delta := choice.Delta
		if delta.Content != nil && *delta.Content != "" {
			em.chunk(*delta.Content)
		}
		// 推理模型的思考过程
		if delta.ReasoningContent != nil && *delta.ReasoningContent != "" {
			em.reasoning(*delta.ReasoningContent)
		} else if delta.Reasoning != nil && *delta.Reasoning != "" {
			em.reasoning(*delta.Reasoning)
		}
		finishReason, parsed := mergeOpenAIChoiceTools(data, acc)
		if parsed {
			acc.reportProgress(&progress, em)
		}
		if parsed && finishReason != "" {
			truncated = finishReason == "length"
			toolCallFinish = finishReason == "tool_calls" || finishReason == "function_call"
			finished = true
		}
		return finished && (!usageRequested || hasUsage)
	})
	if err != nil {
		return err
	}
	if upstreamError != "" {
		return fmt.Errorf("上游返回错误: %s", upstreamError)
	}

	if toolCallFinish {
		if err := validateOpenAIToolFinish("tool_calls", acc); err != nil {
			return err
		}
	}
	// 流结束后发送完整的工具调用；截断时丢弃参数不完整的调用。
	acc.flush(em, truncated)
	if truncated {
		em.truncated()
	}
	em.usage(usage, config.Model)
	return nil
}

// ── OpenAI Responses 流式 ────────────────────────────────────────────────────

func streamOpenAIResponses(ctx context.Context, client *http.Client, config *aiStreamConfig, messages []chatMessage, em *streamEmitter) error {
	url := normalizeBaseURL(config.BaseURL) + "/responses"
	headers := map[string]string{}
	if config.APIKey != "" {
		headers["Authorization"] = "Bearer " + config.APIKey
	}

	body := map[string]any{
		"model":  config.Model,
		"input":  openaiResponsesInput(messages),
		"stream": true,
	}
	reasoningEffort := normalizeAIReasoningEffort(config.ReasoningEffort)
	if len(config.Tools) > 0 {
		body["tools"] = openaiResponsesToolsJSON(config.Tools)
		body["tool_choice"] = "auto"
	}
	if reasoningEffort != "" {
		body["reasoning"] = map[string]any{"effort": reasoningEffort}
	}
	if config.MaxOutputTokens > 0 {
		body["max_output_tokens"] = config.MaxOutputTokens
	}

	res, err := aiPostStream(ctx, client, url, headers, body)
	if err != nil {
		return err
	}
	defer res.Close()

	calls := newResponsesToolCallAccumulator()
	var progress toolCallProgress
	truncated := false
	emittedText := false
	upstreamFailed := false
	upstreamError := ""
	usage := aiTokenUsage{}
	err = forEachSSEDataLine(res, func(data string) bool {
		var payload struct {
			Type      string `json:"type"`
			Delta     string `json:"delta"`
			ItemID    string `json:"item_id"`
			CallID    string `json:"call_id"`
			Arguments string `json:"arguments"`
			Item      struct {
				Type      string `json:"type"`
				ID        string `json:"id"`
				CallID    string `json:"call_id"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"item"`
			Response struct {
				Status string `json:"status"`
				Usage  struct {
					InputTokens  uint64 `json:"input_tokens"`
					OutputTokens uint64 `json:"output_tokens"`
					InputDetails struct {
						CachedTokens uint64 `json:"cached_tokens"`
					} `json:"input_tokens_details"`
					OutputDetails struct {
						ReasoningTokens uint64 `json:"reasoning_tokens"`
					} `json:"output_tokens_details"`
				} `json:"usage"`
				Output []struct {
					Type      string `json:"type"`
					ID        string `json:"id"`
					CallID    string `json:"call_id"`
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
					Content   []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
				} `json:"output"`
				Error *struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"response"`
			Error   json.RawMessage `json:"error"`
			Message string          `json:"message"`
		}
		if json.Unmarshal([]byte(data), &payload) != nil {
			return false
		}
		if payload.Type == "response.output_text.delta" && payload.Delta != "" {
			emittedText = true
			em.chunk(payload.Delta)
		}
		switch payload.Type {
		case "response.output_item.added", "response.output_item.done":
			if payload.Item.Type == "function_call" {
				calls.mergeItem(payload.Item.Type, payload.Item.ID, payload.Item.CallID, payload.Item.Name, payload.Item.Arguments)
			}
		case "response.function_call_arguments.delta":
			calls.appendArguments(payload.ItemID, payload.CallID, payload.Delta)
		case "response.function_call_arguments.done":
			calls.setArguments(payload.ItemID, payload.CallID, payload.Arguments)
		}
		switch payload.Type {
		case "response.output_item.added", "response.function_call_arguments.delta":
			calls.reportProgress(&progress, em)
		case "response.completed", "response.incomplete":
			if payload.Type == "response.incomplete" {
				truncated = true
			}
			usage = aiTokenUsage{
				InputTokens:       payload.Response.Usage.InputTokens,
				OutputTokens:      payload.Response.Usage.OutputTokens,
				CachedInputTokens: payload.Response.Usage.InputDetails.CachedTokens,
				ReasoningTokens:   payload.Response.Usage.OutputDetails.ReasoningTokens,
			}
			for _, item := range payload.Response.Output {
				calls.mergeItem(item.Type, item.ID, item.CallID, item.Name, item.Arguments)
				if !emittedText {
					for _, content := range item.Content {
						if content.Type == "output_text" && content.Text != "" {
							em.chunk(content.Text)
							emittedText = true
						}
					}
				}
			}
		case "response.failed", "error":
			upstreamFailed = true
			if msg := streamErrorMessage(payload.Error); msg != "" {
				upstreamError = msg
			} else if strings.TrimSpace(payload.Message) != "" {
				upstreamError = strings.TrimSpace(payload.Message)
			} else if payload.Response.Error != nil {
				upstreamError = payload.Response.Error.Message
			}
		}
		// 检测截断：response.completed 中 status == "incomplete"
		if payload.Type == "response.completed" && payload.Response.Status == "incomplete" {
			truncated = true
		}
		switch payload.Type {
		case "response.completed", "response.incomplete", "response.failed", "error":
			return true
		}
		return false
	})
	if err != nil {
		return err
	}
	if upstreamFailed {
		if strings.TrimSpace(upstreamError) == "" {
			upstreamError = "上游返回失败事件但未提供错误信息"
		}
		return fmt.Errorf("Responses API 返回错误: %s", upstreamError)
	}
	emittedCalls := calls.flush(em, truncated)
	if truncated {
		em.truncated()
	}
	if !emittedText && emittedCalls == 0 && !truncated {
		return errors.New("Responses API 未返回可识别的文本或工具调用")
	}
	em.usage(usage, config.Model)
	return nil
}

// ── Anthropic 原生流式 ───────────────────────────────────────────────────────

// Anthropic 的 thinking 用 token 预算而不是 effort 字符串，这里是档位到预算的唯一映射。
var anthropicThinkingBudgets = map[string]int{
	"low":    1024,
	"medium": 4096,
	"high":   16384,
	"xhigh":  24576,
	"max":    32768,
	"ultra":  65536,
}

// budget_tokens 必须小于 max_tokens，否则 Anthropic 直接返回 400。用户显式配置了较小的
// 输出上限时，宁可压缩思考预算也不要让整个请求失败。
func anthropicThinkingBudget(effort string, maxTokens int) (int, bool) {
	budget, ok := anthropicThinkingBudgets[effort]
	if !ok {
		return 0, false
	}
	if maxTokens > 0 && budget >= maxTokens {
		budget = maxTokens - 1024
	}
	if budget < 1024 {
		return 0, false
	}
	return budget, true
}

var anthropicClaudeVersionPattern = regexp.MustCompile(`claude-(?:opus|sonnet|haiku)-(\d+)(?:[-.](\d{1,2}))?(?:\D|$)`)

// anthropicUsesAdaptiveThinking reports whether the model takes
// thinking.type=adaptive with output_config.effort. Claude 4.6+ (and the
// Mythos/Fable families) do; Claude Opus 4.7+ and every 5.x model reject
// thinking.type=enabled with budget_tokens. Older Claude models and
// Anthropic-compatible third-party models keep the budget form.
func anthropicUsesAdaptiveThinking(model string) bool {
	name := strings.ToLower(model)
	if strings.Contains(name, "claude-mythos") || strings.Contains(name, "claude-fable") {
		return true
	}
	match := anthropicClaudeVersionPattern.FindStringSubmatch(name)
	if match == nil {
		return false
	}
	major, _ := strconv.Atoi(match[1])
	minor, _ := strconv.Atoi(match[2])
	return major > 4 || (major == 4 && minor >= 6)
}

// anthropicAdaptiveEffort maps the app's effort levels onto output_config.effort
// (low, medium, high, xhigh, max). xhigh is unavailable on the 4.6 models and
// Mythos Preview, so it falls back to high there.
func anthropicAdaptiveEffort(model, effort string) string {
	switch effort {
	case "low", "medium", "high":
		return effort
	case "xhigh":
		name := strings.ToLower(model)
		if strings.Contains(name, "4-6") || strings.Contains(name, "4.6") || strings.Contains(name, "mythos-preview") {
			return "high"
		}
		return "xhigh"
	default:
		return "max"
	}
}

func anthropicSystemPrompt(messages []chatMessage) string {
	parts := make([]string, 0, 2)
	for i := range messages {
		if messages[i].Role == "system" && strings.TrimSpace(messages[i].Content) != "" {
			parts = append(parts, messages[i].Content)
		}
	}
	return strings.Join(parts, "\n\n")
}

// anthropicRequestBody builds the Messages API body. With cache enabled it marks
// prompt-caching breakpoints: Anthropic caches the prefix in the order tools →
// system → messages up to each marked block, so marking the last tool, the system
// prompt and the newest message lets every agent-loop step read the prefix the
// previous step wrote. Prefixes below the model minimum are simply not cached.
func anthropicRequestBody(config *aiStreamConfig, messages []chatMessage, cache bool) map[string]any {
	// Anthropic 使用独立 system 字段；合并全部 system，避免规则消息遮蔽内置安全和工具指令。
	system := anthropicSystemPrompt(messages)

	reasoningEffort := normalizeAIReasoningEffort(config.ReasoningEffort)
	_, thinkingEffort := anthropicThinkingBudgets[reasoningEffort]
	// 当前工具循环（最后一条用户消息之后）中的助手消息需要回传思考块。
	turnStart := anthropicCurrentTurnStart(messages)
	if thinkingEffort && anthropicThinkingWouldBreakToolLoop(config.Model, messages, turnStart) {
		// 进行中的工具循环缺少可回传的思考块（例如中途开启思考或切换模型），
		// Claude 会拒绝该请求；本次请求关闭思考，下一轮用户消息后自动恢复。
		reasoningEffort = ""
		thinkingEffort = false
	}

	chatMessages := make([]map[string]any, 0, len(messages))
	for i := range messages {
		if messages[i].Role == "system" {
			continue
		}
		message := anthropicMessageJSON(&messages[i])
		if thinkingEffort && i > turnStart && messages[i].Role == "assistant" {
			prependAnthropicThinkingBlocks(message, messages[i].ThinkingBlocks)
		}
		chatMessages = append(chatMessages, message)
	}

	maxTokens := int(config.MaxOutputTokens)
	if maxTokens == 0 && reasoningEffort != "" {
		if budget, ok := anthropicThinkingBudgets[reasoningEffort]; ok {
			maxTokens = budget + 16384
		}
	}
	if maxTokens == 0 {
		maxTokens = 16384
	}

	body := map[string]any{
		"model":      config.Model,
		"max_tokens": maxTokens,
		"messages":   chatMessages,
		"stream":     true,
	}
	if system != "" {
		body["system"] = system
	}
	if len(config.Tools) > 0 {
		body["tools"] = anthropicToolsJSON(config.Tools)
	}
	if thinkingEffort && anthropicUsesAdaptiveThinking(config.Model) {
		body["thinking"] = map[string]any{"type": "adaptive", "display": "summarized"}
		body["output_config"] = map[string]any{"effort": anthropicAdaptiveEffort(config.Model, reasoningEffort)}
	} else if reasoningEffort != "" {
		if budget, ok := anthropicThinkingBudget(reasoningEffort, maxTokens); ok {
			body["thinking"] = map[string]any{
				"type":          "enabled",
				"budget_tokens": budget,
			}
		}
	}

	if cache {
		addAnthropicCacheBreakpoints(body)
	}
	return body
}

// anthropicCurrentTurnStart 返回最后一条用户输入（非工具结果）的下标；没有则为 -1。
func anthropicCurrentTurnStart(messages []chatMessage) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			return i
		}
	}
	return -1
}

// anthropicThinkingWouldBreakToolLoop 判断开启思考是否会被 Claude 拒绝：工具循环中
// 最后一条助手消息带工具调用却没有可回传的思考块。仅对 Claude 模型生效，兼容网关
// 的其他模型通常不校验思考块。
func anthropicThinkingWouldBreakToolLoop(model string, messages []chatMessage, turnStart int) bool {
	if !strings.Contains(strings.ToLower(model), "claude") {
		return false
	}
	for i := len(messages) - 1; i > turnStart; i-- {
		switch messages[i].Role {
		case "tool", "system":
			continue
		case "assistant":
			return len(messages[i].ToolCalls) > 0 && len(validThinkingBlocks(messages[i].ThinkingBlocks)) == 0
		default:
			return false
		}
	}
	return false
}

func validThinkingBlocks(blocks []thinkingBlock) []map[string]any {
	out := make([]map[string]any, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case "thinking":
			if block.Signature != "" {
				out = append(out, map[string]any{"type": "thinking", "thinking": block.Thinking, "signature": block.Signature})
			}
		case "redacted_thinking":
			if block.Data != "" {
				out = append(out, map[string]any{"type": "redacted_thinking", "data": block.Data})
			}
		}
	}
	return out
}

// prependAnthropicThinkingBlocks 把思考块放到助手消息内容最前面（Anthropic 要求的顺序）。
func prependAnthropicThinkingBlocks(message map[string]any, blocks []thinkingBlock) {
	thinking := validThinkingBlocks(blocks)
	if len(thinking) == 0 {
		return
	}
	switch content := message["content"].(type) {
	case []map[string]any:
		message["content"] = append(thinking, content...)
	case string:
		if content != "" {
			thinking = append(thinking, map[string]any{"type": "text", "text": content})
		}
		message["content"] = thinking
	}
}

var anthropicEphemeralCache = map[string]any{"type": "ephemeral"}

func addAnthropicCacheBreakpoints(body map[string]any) {
	if tools, ok := body["tools"].([]map[string]any); ok && len(tools) > 0 {
		tools[len(tools)-1]["cache_control"] = anthropicEphemeralCache
	}
	if system, ok := body["system"].(string); ok && system != "" {
		body["system"] = []map[string]any{{"type": "text", "text": system, "cache_control": anthropicEphemeralCache}}
	}
	if messages, ok := body["messages"].([]map[string]any); ok && len(messages) > 0 {
		last := messages[len(messages)-1]
		switch content := last["content"].(type) {
		case string:
			if content != "" {
				last["content"] = []map[string]any{{"type": "text", "text": content, "cache_control": anthropicEphemeralCache}}
			}
		case []map[string]any:
			if len(content) > 0 {
				content[len(content)-1]["cache_control"] = anthropicEphemeralCache
			}
		}
	}
}

func anthropicRejectsCacheControl(err error) bool {
	message := err.Error()
	return strings.HasPrefix(message, "HTTP 400") && strings.Contains(message, "cache_control")
}

func streamAnthropic(ctx context.Context, client *http.Client, config *aiStreamConfig, messages []chatMessage, em *streamEmitter) error {
	url := normalizeBaseURL(config.BaseURL) + "/messages"
	headers := anthropicAuthHeaders(config)

	res, err := aiPostStream(ctx, client, url, headers, anthropicRequestBody(config, messages, true))
	if err != nil && anthropicRejectsCacheControl(err) {
		// Some Anthropic-compatible gateways reject cache_control; retry once without it.
		res, err = aiPostStream(ctx, client, url, headers, anthropicRequestBody(config, messages, false))
	}
	if err != nil {
		return err
	}
	defer res.Close()

	// tool_use / thinking 累积：content_block index -> 缓冲。工具调用在流结束后统一推送，
	// 以便截断时丢弃参数不完整的调用。
	toolBlocks := map[int]*accToolCall{}
	var progress toolCallProgress
	thinkingBlocks := map[int]*thinkingBlock{}
	var pendingCalls []pendingToolCall
	truncated := false
	upstreamError := ""
	usage := aiTokenUsage{}

	err = forEachSSEDataLine(res, func(data string) bool {
		var payload struct {
			Type  string `json:"type"`
			Index int    `json:"index"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				Thinking    string `json:"thinking"`
				Signature   string `json:"signature"`
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
			} `json:"delta"`
			ContentBlock struct {
				Type      string `json:"type"`
				ID        string `json:"id"`
				Name      string `json:"name"`
				Thinking  string `json:"thinking"`
				Signature string `json:"signature"`
				Data      string `json:"data"`
			} `json:"content_block"`
			Message struct {
				Usage struct {
					InputTokens              uint64 `json:"input_tokens"`
					CacheReadInputTokens     uint64 `json:"cache_read_input_tokens"`
					CacheCreationInputTokens uint64 `json:"cache_creation_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
			Usage struct {
				OutputTokens uint64 `json:"output_tokens"`
			} `json:"usage"`
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal([]byte(data), &payload) != nil {
			return false
		}

		switch payload.Type {
		case "message_stop":
			return true
		case "error":
			upstreamError = streamErrorMessage(payload.Error)
			if upstreamError == "" {
				upstreamError = "上游返回错误事件但未提供错误信息"
			}
			return true
		case "message_start":
			usage.CachedInputTokens = payload.Message.Usage.CacheReadInputTokens
			usage.CacheWriteTokens = payload.Message.Usage.CacheCreationInputTokens
			// Anthropic 将普通输入、缓存读取和缓存写入分列；统一成总输入，便于跨供应商展示。
			usage.InputTokens = payload.Message.Usage.InputTokens + usage.CachedInputTokens + usage.CacheWriteTokens
		case "content_block_delta":
			switch payload.Delta.Type {
			case "text_delta":
				if payload.Delta.Text != "" {
					em.chunk(payload.Delta.Text)
				}
			case "thinking_delta":
				if payload.Delta.Thinking != "" {
					em.reasoning(payload.Delta.Thinking)
					if block, ok := thinkingBlocks[payload.Index]; ok {
						block.Thinking += payload.Delta.Thinking
					}
				}
			case "signature_delta":
				if block, ok := thinkingBlocks[payload.Index]; ok {
					block.Signature += payload.Delta.Signature
				}
			case "input_json_delta":
				if entry, ok := toolBlocks[payload.Index]; ok {
					entry.args = append(entry.args, payload.Delta.PartialJSON...)
					progress.observe(em, payload.Index, entry.name, len(entry.args))
				}
			}
		case "content_block_start":
			switch payload.ContentBlock.Type {
			case "tool_use":
				toolBlocks[payload.Index] = &accToolCall{
					id:   payload.ContentBlock.ID,
					name: payload.ContentBlock.Name,
				}
				progress.observe(em, payload.Index, payload.ContentBlock.Name, 0)
			case "thinking":
				thinkingBlocks[payload.Index] = &thinkingBlock{
					Type: "thinking", Thinking: payload.ContentBlock.Thinking,
					Signature: payload.ContentBlock.Signature,
				}
				if payload.ContentBlock.Thinking != "" {
					em.reasoning(payload.ContentBlock.Thinking)
				}
			case "redacted_thinking":
				thinkingBlocks[payload.Index] = &thinkingBlock{
					Type: "redacted_thinking", Data: payload.ContentBlock.Data,
				}
			}
		case "content_block_stop":
			if block, ok := thinkingBlocks[payload.Index]; ok {
				delete(thinkingBlocks, payload.Index)
				// 只有带签名（或加密数据）的块才能被 Anthropic 接受回传。
				if block.Signature != "" || block.Data != "" {
					em.thinkingBlock(*block)
				}
			}
			if entry, ok := toolBlocks[payload.Index]; ok {
				delete(toolBlocks, payload.Index)
				if entry.name != "" {
					id := strings.TrimSpace(entry.id)
					if id == "" {
						id = generatedToolCallID(payload.Index)
					}
					pendingCalls = append(pendingCalls, pendingToolCall{id: id, name: entry.name, args: string(entry.args)})
				}
			}
		case "message_delta":
			usage.OutputTokens = payload.Usage.OutputTokens
			// 检测截断：stop_reason == "max_tokens" 表示输出被截断
			if payload.Delta.StopReason == "max_tokens" {
				truncated = true
			}
		}
		return false
	})
	if err != nil {
		return err
	}
	if upstreamError != "" {
		return fmt.Errorf("Anthropic 返回错误: %s", upstreamError)
	}
	emitToolCalls(em, pendingCalls, truncated)
	if truncated {
		em.truncated()
	}
	em.usage(usage, config.Model)
	return nil
}

// ── 命令入口 ─────────────────────────────────────────────────────────────────

type aiTestConnectionArgs struct {
	Config aiStreamConfig `json:"config"`
}

func aiTestConnectionHandler(_ string, argsJSON string) (string, error) {
	var args aiTestConnectionArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if err := validateAiBaseURL(args.Config.BaseURL); err != nil {
		return "", err
	}
	if strings.TrimSpace(args.Config.Model) == "" {
		return "", errors.New("请先选择或填写 AI 模型")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	messages := []chatMessage{{Role: "user", Content: "Reply with OK."}}
	emitter := newEventEmitter("ai:connection-test:" + uuid.NewString())

	var err error
	switch args.Config.APIFormat {
	case "anthropic":
		err = streamAnthropic(ctx, aiStreamHTTPClient(), &args.Config, messages, emitter)
	case "openai-responses":
		err = streamOpenAIResponses(ctx, aiStreamHTTPClient(), &args.Config, messages, emitter)
	default:
		err = streamOpenAI(ctx, aiStreamHTTPClient(), &args.Config, messages, emitter)
	}
	if err != nil {
		return "", fmt.Errorf("AI 连接测试失败: %w", err)
	}
	return core.EncodeResult(true)
}

// aiStreamChatHandler 对齐 Tauri ai_stream_chat：阻塞执行至流结束；
// 事件经 core.Emit("stream:"+channelId, …) 推送。
func aiStreamChatHandler(_ string, argsJson string) (string, error) {
	var args aiStreamChatArgs
	if err := core.DecodeArgs(argsJson, &args); err != nil {
		return "", err
	}
	if strings.TrimSpace(args.ChannelID) == "" {
		return "", errors.New("缺少 channelId")
	}
	args.Config.ReasoningEffort = normalizeAIReasoningEffort(args.Config.ReasoningEffort)

	em := newEventEmitter("stream:" + args.ChannelID)
	ctx, cancel := context.WithCancel(context.Background())
	stateValue, _ := activeAIStreams.LoadOrStore(args.ChannelID, &aiStreamState{})
	state := stateValue.(*aiStreamState)
	registered, preCanceled := state.register(cancel)
	if !registered {
		cancel()
		if preCanceled {
			activeAIStreams.CompareAndDelete(args.ChannelID, state)
			return "", nil
		}
		return "", errors.New("channelId 已存在")
	}
	defer func() {
		cancel()
		activeAIStreams.CompareAndDelete(args.ChannelID, state)
	}()

	return "", runAiStreamChat(ctx, &args, em)
}

// runAiStreamChat 执行一次流式对话，事件经 em 输出（事件通道或 Streams 连接共用）。
// 取消返回 nil；其余错误先经 em.error 通知前端再向上返回。
func runAiStreamChat(ctx context.Context, args *aiStreamChatArgs, em *streamEmitter) error {
	if err := validateAiBaseURL(args.Config.BaseURL); err != nil {
		em.error(err.Error())
		return err
	}

	rawCount := len(args.Messages)
	limitedMessages, limitErr := limitChatMessages(args.Messages, &args.Config)
	if limitErr != nil {
		em.error(limitErr.Error())
		return limitErr
	}
	messages := sanitizeChatMessages(limitedMessages)
	if len(messages) != rawCount {
		fmt.Fprintf(os.Stderr, "[ai] limit_and_sanitize_chat_messages: %d -> %d messages\n", rawCount, len(messages))
	}

	client := aiStreamHTTPClient()

	var err error
	switch args.Config.APIFormat {
	case "anthropic":
		err = streamAnthropic(ctx, client, &args.Config, messages, em)
	case "openai-responses":
		err = streamOpenAIResponses(ctx, client, &args.Config, messages, em)
	default:
		err = streamOpenAI(ctx, client, &args.Config, messages, em)
	}

	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			em.discard()
			return nil
		}
		em.error(aiStreamErrorMessage(err))
		return err
	}
	if ctx.Err() != nil {
		em.discard()
		return nil
	}
	em.done()
	return nil
}

// aiChatStreamHandler：IPC 流版 ai_stream_chat。前端连 Stream("ai-chat")，
// 首帧 JSON 发参数（channelId/config/messages），随后以 JSON 帧收流式事件；
// 断开连接即取消。与事件版共用 runAiStreamChat，ai_stream_cancel 亦对 channelId 生效。
func aiChatStreamHandler(conn core.StreamConn) {
	var args aiStreamChatArgs
	if err := conn.ReceiveJSON(&args); err != nil {
		return
	}
	channelID := strings.TrimSpace(args.ChannelID)
	if channelID == "" {
		return
	}
	args.Config.ReasoningEffort = normalizeAIReasoningEffort(args.Config.ReasoningEffort)

	ctx, cancel := context.WithCancel(context.Background())
	// 串行化连接写入；发送失败说明前端已无法消费，立即取消 Provider 请求。
	var sendMu sync.Mutex
	em := &streamEmitter{emit: func(payload map[string]any) {
		if ctx.Err() != nil {
			return
		}
		sendMu.Lock()
		defer sendMu.Unlock()
		if err := conn.SendJSON(payload); err != nil {
			cancel()
		}
	}}

	stateValue, _ := activeAIStreams.LoadOrStore(channelID, &aiStreamState{})
	state := stateValue.(*aiStreamState)
	registered, _ := state.register(cancel)
	if !registered {
		cancel()
		return
	}
	defer func() {
		cancel()
		activeAIStreams.CompareAndDelete(channelID, state)
	}()

	// 对端断开（页面重载/关闭/主动 close）时 Receive 出错，触发取消。
	go func() {
		for {
			if _, err := conn.Receive(); err != nil {
				cancel()
				return
			}
		}
	}()

	_ = runAiStreamChat(ctx, &args, em)
}

type aiStreamCancelArgs struct {
	ChannelID string `json:"channelId"`
}

// aiStreamCancelHandler 幂等取消指定 channelId 对应的供应商 HTTP 请求。
func aiStreamCancelHandler(_ string, argsJson string) (string, error) {
	var args aiStreamCancelArgs
	if err := core.DecodeArgs(argsJson, &args); err != nil {
		return "", err
	}
	channelID := strings.TrimSpace(args.ChannelID)
	if channelID == "" {
		return "", errors.New("缺少 channelId")
	}
	newState := &aiStreamState{}
	stateValue, loaded := activeAIStreams.LoadOrStore(channelID, newState)
	state := stateValue.(*aiStreamState)
	state.cancelStream()
	if !loaded {
		time.AfterFunc(30*time.Second, func() {
			activeAIStreams.CompareAndDelete(channelID, state)
		})
	}
	return "", nil
}
