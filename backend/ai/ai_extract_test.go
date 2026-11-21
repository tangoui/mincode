package ai

import (
	"encoding/json"
	"testing"
)

func decodeResponseFixture(t *testing.T, payload string) map[string]json.RawMessage {
	t.Helper()
	var v map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// output_text 只存在于官方 SDK 的便利属性里，HTTP 响应中没有这个字段。按它取内容
// 会让每一次调用都静默返回空串，用户只看到「AI 未返回提交信息」。
func TestExtractOpenAIResponsesReadsOutputArrayNotOutputText(t *testing.T) {
	v := decodeResponseFixture(t, `{
		"status": "completed",
		"output": [
			{"type": "reasoning", "summary": []},
			{"type": "message", "role": "assistant", "content": [
				{"type": "output_text", "annotations": [], "text": "fix(ai): 修正提交信息提取"}
			]}
		]
	}`)
	result := extractOpenAIResponsesContent(v)
	if result.Content != "fix(ai): 修正提交信息提取" {
		t.Fatalf("未能从 output[].content[].text 提取正文: %#v", result)
	}
	if _, present := v["output_text"]; present {
		t.Fatal("fixture 不应含有 output_text，否则测不出真实形态")
	}
}

func TestExtractOpenAIResponsesExplainsTruncation(t *testing.T) {
	v := decodeResponseFixture(t, `{
		"status": "incomplete",
		"incomplete_details": {"reason": "max_output_tokens"},
		"output": [{"type": "reasoning", "summary": []}]
	}`)
	result := extractOpenAIResponsesContent(v)
	if result.Content != "" {
		t.Fatalf("截断的响应不应产出正文: %#v", result)
	}
	if result.Reason == "" {
		t.Fatal("耗尽输出预算必须给出可读原因，否则用户无从判断该调大哪个设置")
	}
}

// 开启 extended thinking 后 content[0] 是 thinking 块，没有 text 字段。
func TestExtractAnthropicSkipsThinkingBlock(t *testing.T) {
	v := decodeResponseFixture(t, `{
		"stop_reason": "end_turn",
		"content": [
			{"type": "thinking", "thinking": "先看 diff 涉及哪些包", "signature": "abc"},
			{"type": "text", "text": "chore(fs): 跳过包管理器缓存目录"}
		]
	}`)
	result := extractAnthropicContent(v)
	if result.Content != "chore(fs): 跳过包管理器缓存目录" {
		t.Fatalf("thinking 块之后的正文被漏掉了: %#v", result)
	}
}

func TestExtractAnthropicExplainsMaxTokens(t *testing.T) {
	v := decodeResponseFixture(t, `{"stop_reason": "max_tokens", "content": []}`)
	result := extractAnthropicContent(v)
	if result.Content != "" || result.Reason == "" {
		t.Fatalf("达到 max_tokens 必须带出原因: %#v", result)
	}
}

func TestExtractOpenAIChatAcceptsStringAndPartsContent(t *testing.T) {
	plain := extractOpenAIContent(decodeResponseFixture(t, `{
		"choices": [{"message": {"content": "feat: 纯字符串正文"}, "finish_reason": "stop"}]
	}`))
	if plain.Content != "feat: 纯字符串正文" {
		t.Fatalf("字符串形态解析失败: %#v", plain)
	}

	parts := extractOpenAIContent(decodeResponseFixture(t, `{
		"choices": [{"message": {"content": [
			{"type": "text", "text": "feat: "},
			{"type": "text", "text": "分段正文"}
		]}, "finish_reason": "stop"}]
	}`))
	if parts.Content != "feat: 分段正文" {
		t.Fatalf("分段数组形态解析失败: %#v", parts)
	}

	truncated := extractOpenAIContent(decodeResponseFixture(t, `{
		"choices": [{"message": {"content": ""}, "finish_reason": "length"}]
	}`))
	if truncated.Reason == "" {
		t.Fatalf("finish_reason=length 必须带出原因: %#v", truncated)
	}
}

func TestConfiguredOutputTokensPrefersExplicitLimit(t *testing.T) {
	config := &aiStreamConfig{MaxOutputTokens: 4096}
	if got := configuredOutputTokens(config, 160); got != 4096 {
		t.Fatalf("configuredOutputTokens = %d, want 4096", got)
	}
}
