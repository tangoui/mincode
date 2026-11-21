package ai

// 流式对话 ai_stream_chat 见 stream.go（经 core.Emit 推送事件到前端）。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var supportedAIReasoningEfforts = map[string]struct{}{
	"none": {}, "minimal": {}, "low": {}, "medium": {}, "high": {}, "xhigh": {}, "max": {}, "ultra": {},
}

func normalizeAIReasoningEffort(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if _, ok := supportedAIReasoningEfforts[normalized]; ok {
		return normalized
	}
	return ""
}

// aiStreamConfig 对齐 Tauri AiStreamConfig（camelCase）。
type aiStreamConfig struct {
	BaseURL           string `json:"baseUrl"`
	APIKey            string `json:"apiKey"`
	Model             string `json:"model"`
	APIFormat         string `json:"apiFormat"`
	ReasoningEffort   string `json:"reasoningEffort"`
	ContextWindow     uint32 `json:"contextWindow"`
	MaxInputTokens    uint32 `json:"maxInputTokens"`
	MaxOutputTokens   uint32 `json:"maxOutputTokens"`
	OpenAIStreamUsage string `json:"openaiStreamUsage"`
	// Anthropic 格式的凭据头：缺省走 x-api-key，"bearer" 走 Authorization: Bearer。
	AnthropicAuth string `json:"anthropicAuth"`
	// 可选工具定义列表，用于 function calling / tool use（流式对话使用）
	Tools []aiToolDef `json:"tools"`
}

// aiToolDef 对齐 Tauri AiToolDef。
type aiToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// aiHTTPClient：整体超时 120s（避免长 think 间隔被切断），复用 AI 连接池与重定向策略。
func aiHTTPClient() *http.Client {
	return aiTimeoutHTTPClient(120 * time.Second)
}

// normalizeBaseURL 对齐 Rust：若末段形如 v1/v2… 则原样，否则追加 /v1。
func normalizeBaseURL(url string) string {
	trimmed := strings.TrimRight(url, "/")
	lastSeg := trimmed
	if idx := strings.LastIndex(trimmed, "/"); idx >= 0 {
		lastSeg = trimmed[idx+1:]
	}
	isVersion := false
	if len(lastSeg) > 1 && lastSeg[0] == 'v' {
		if _, err := parseUint(lastSeg[1:]); err == nil {
			isVersion = true
		}
	}
	if isVersion {
		return trimmed
	}
	return trimmed + "/v1"
}

// parseUint 解析无符号十进制整数（对齐 Rust u32::parse 的判定；仅用于版本段识别）。
func parseUint(s string) (uint64, error) {
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	var n uint64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("invalid digit")
		}
		n = n*10 + uint64(c-'0')
	}
	return n, nil
}

// stripInlineCompletionFences 去除模型返回的 markdown 代码围栏。对齐 Rust strip_inline_completion_fences。
func stripInlineCompletionFences(text string) string {
	t := strings.TrimSpace(text)
	if strings.HasPrefix(t, "```") {
		if idx := strings.LastIndex(t, "```"); idx > 0 {
			t = strings.TrimSpace(t[:idx])
		}
		if strings.HasPrefix(t, "```") {
			rest := strings.TrimLeft(t, "`")
			rest = strings.TrimLeft(rest, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")
			rest = strings.TrimPrefix(rest, "\n")
			t = strings.TrimSpace(rest)
		}
	}
	return strings.TrimSpace(t)
}

// completeWithConfig 按配置的 API 形态分发一次非流式补全。
func completeWithConfig(
	config *aiStreamConfig, system, user string, outputTokens uint32,
) (completionResult, error) {
	return completeWithClient(aiHTTPClient(), config, system, user, outputTokens)
}

func completeWithClient(
	client *http.Client, config *aiStreamConfig, system, user string, outputTokens uint32,
) (completionResult, error) {
	if err := validateAiBaseURL(config.BaseURL); err != nil {
		return completionResult{}, err
	}
	switch config.APIFormat {
	case "anthropic":
		return completeAnthropicJSON(client, config, system, user, outputTokens)
	case "openai-responses":
		return completeOpenAIResponsesJSON(client, config, system, user, outputTokens)
	default:
		return completeOpenAIJSON(client, config, system, user, outputTokens)
	}
}

// aiPostJSON 发送 JSON POST 并返回解析后的响应体（含状态码校验）。
func aiPostJSON(client *http.Client, url string, headers map[string]string, body any) (map[string]json.RawMessage, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("序列化请求失败: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(buf))
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
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", res.StatusCode, readErrorBody(res.Body))
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, aiJSONResponseLimit+1))
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}
	if len(raw) > aiJSONResponseLimit {
		return nil, fmt.Errorf("AI 响应超出上限(%dB)", aiJSONResponseLimit)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("解析 JSON 失败: %w", err)
	}
	return out, nil
}

// aiJSONResponseLimit 非流式补全响应体上限。
const aiJSONResponseLimit = 8 << 20

func configuredOutputTokens(config *aiStreamConfig, fallback uint32) uint32 {
	if config.MaxOutputTokens > 0 {
		return config.MaxOutputTokens
	}
	return fallback
}

// completionResult separates the generated text from the reason it is empty.
type completionResult struct {
	Content string
	Reason  string
}

func completeOpenAIJSON(client *http.Client, config *aiStreamConfig, system, user string, outputTokens uint32) (completionResult, error) {
	url := normalizeBaseURL(config.BaseURL) + "/chat/completions"
	headers := map[string]string{}
	if config.APIKey != "" {
		headers["Authorization"] = "Bearer " + config.APIKey
	}
	body := map[string]any{
		"model": config.Model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"stream":                     false,
		openAIMaxTokensField(config): configuredOutputTokens(config, outputTokens),
	}
	// 推理模型（o 系列 / gpt-5 / gpt-6）只接受默认 temperature，显式传值会直接 400。
	if !isOpenAIReasoningModel(config.Model) {
		body["temperature"] = 0.15
	}
	v, err := aiPostJSON(client, url, headers, body)
	if err != nil {
		return completionResult{}, err
	}
	result := extractOpenAIContent(v)
	result.Content = stripInlineCompletionFences(result.Content)
	return result, nil
}

func completeOpenAIResponsesJSON(client *http.Client, config *aiStreamConfig, system, user string, outputTokens uint32) (completionResult, error) {
	url := normalizeBaseURL(config.BaseURL) + "/responses"
	headers := map[string]string{}
	if config.APIKey != "" {
		headers["Authorization"] = "Bearer " + config.APIKey
	}
	body := map[string]any{
		"model":             config.Model,
		"input":             system + "\n\n" + user,
		"stream":            false,
		"max_output_tokens": configuredOutputTokens(config, outputTokens),
	}
	if !isOpenAIReasoningModel(config.Model) {
		body["temperature"] = 0.15
	}
	v, err := aiPostJSON(client, url, headers, body)
	if err != nil {
		return completionResult{}, err
	}
	result := extractOpenAIResponsesContent(v)
	result.Content = stripInlineCompletionFences(result.Content)
	return result, nil
}

func completeAnthropicJSON(client *http.Client, config *aiStreamConfig, system, user string, outputTokens uint32) (completionResult, error) {
	url := normalizeBaseURL(config.BaseURL) + "/messages"
	headers := anthropicAuthHeaders(config)
	body := map[string]any{
		"model":       config.Model,
		"max_tokens":  configuredOutputTokens(config, outputTokens),
		"temperature": 0.15,
		"system":      system,
		"messages": []map[string]string{
			{"role": "user", "content": user},
		},
	}
	v, err := aiPostJSON(client, url, headers, body)
	if err != nil {
		return completionResult{}, err
	}
	result := extractAnthropicContent(v)
	result.Content = stripInlineCompletionFences(result.Content)
	return result, nil
}

// extractOpenAIContent 提取 choices[0].message.content。
func extractOpenAIContent(v map[string]json.RawMessage) completionResult {
	choicesRaw, ok := v["choices"]
	if !ok {
		return completionResult{Reason: "响应中没有 choices 字段"}
	}
	var choices []struct {
		Message struct {
			// 网关返回的 content 可能是字符串，也可能是分段数组，两种都要接住。
			Content json.RawMessage `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	}
	if err := json.Unmarshal(choicesRaw, &choices); err != nil || len(choices) == 0 {
		return completionResult{Reason: "响应的 choices 为空"}
	}
	return completionResult{
		Content: decodeTextParts(choices[0].Message.Content),
		Reason:  describeFinishReason(choices[0].FinishReason),
	}
}

// extractAnthropicContent 取第一个 type 为 text 的块。开启 extended thinking 后
// content[0] 是 thinking 块且没有 text 字段，按下标取会得到空字符串。
func extractAnthropicContent(v map[string]json.RawMessage) completionResult {
	result := completionResult{Reason: describeStopReason(decodeJSONString(v["stop_reason"]))}
	contentRaw, ok := v["content"]
	if !ok {
		if result.Reason == "" {
			result.Reason = "响应中没有 content 字段"
		}
		return result
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(contentRaw, &blocks); err != nil {
		return result
	}
	var parts []string
	for _, block := range blocks {
		if block.Type == "text" && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	result.Content = strings.Join(parts, "")
	return result
}

// extractOpenAIResponsesContent 从 output[].content[].text 提取正文。
// output_text 只是官方 SDK 的便利属性，并不存在于 HTTP 响应里，按它取必然为空。
func extractOpenAIResponsesContent(v map[string]json.RawMessage) completionResult {
	result := completionResult{}
	if incomplete, ok := v["incomplete_details"]; ok {
		var details struct {
			Reason string `json:"reason"`
		}
		if json.Unmarshal(incomplete, &details) == nil && details.Reason != "" {
			result.Reason = describeFinishReason(details.Reason)
		}
	}
	outputRaw, ok := v["output"]
	if !ok {
		if result.Reason == "" {
			result.Reason = "响应中没有 output 字段"
		}
		return result
	}
	var items []struct {
		Type    string `json:"type"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(outputRaw, &items); err != nil {
		return result
	}
	var parts []string
	for _, item := range items {
		// reasoning 块和 message 块并列出现，只有后者带用户可见文本。
		for _, part := range item.Content {
			if part.Type == "output_text" && part.Text != "" {
				parts = append(parts, part.Text)
			}
		}
	}
	result.Content = strings.Join(parts, "")
	return result
}

// decodeTextParts 接受字符串或 [{type,text}] 两种 content 形态。
func decodeTextParts(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var builder strings.Builder
	for _, part := range parts {
		builder.WriteString(part.Text)
	}
	return builder.String()
}

func decodeJSONString(raw json.RawMessage) string {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}

// describeFinishReason 把截断/过滤这类「请求成功但没有正文」的原因翻译成人话，
// 否则用户只会看到一句没有信息量的「AI 未返回提交信息」。
func describeFinishReason(reason string) string {
	switch reason {
	case "length", "max_output_tokens", "max_tokens":
		return "输出长度上限耗尽（推理模型的思考过程也计入该预算）"
	case "content_filter":
		return "内容被服务端安全策略拦截"
	}
	return ""
}

func describeStopReason(reason string) string {
	if reason == "max_tokens" {
		return describeFinishReason("max_tokens")
	}
	if reason == "refusal" {
		return "模型拒绝了该请求"
	}
	return ""
}
