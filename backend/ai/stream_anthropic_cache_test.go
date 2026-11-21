package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func cacheControl(value any) any {
	if block, ok := value.(map[string]any); ok {
		return block["cache_control"]
	}
	return nil
}

func TestAnthropicRequestBodyMarksCacheBreakpoints(t *testing.T) {
	t.Parallel()

	config := &aiStreamConfig{Model: "claude", Tools: []aiToolDef{
		{Name: "first", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "last", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}}
	messages := []chatMessage{
		{Role: "system", Content: "rules"},
		{Role: "user", Content: "earlier"},
		{Role: "assistant", Content: "", ToolCalls: []toolCallInfo{{ID: "call-1", Name: "first", Arguments: `{}`}}},
		{Role: "tool", ToolCallID: "call-1", Content: "result"},
	}
	body := anthropicRequestBody(config, messages, true)

	tools := body["tools"].([]map[string]any)
	if tools[0]["cache_control"] != nil || cacheControl(tools[1]) == nil {
		t.Fatalf("only the last tool is a breakpoint: %#v", tools)
	}
	system := body["system"].([]map[string]any)
	if len(system) != 1 || system[0]["text"] != "rules" || cacheControl(system[0]) == nil {
		t.Fatalf("system must be one cached text block: %#v", system)
	}
	chat := body["messages"].([]map[string]any)
	if chat[0]["content"] != "earlier" {
		t.Fatalf("earlier messages stay unmarked: %#v", chat[0])
	}
	last := chat[len(chat)-1]["content"].([]map[string]any)
	if last[0]["type"] != "tool_result" || cacheControl(last[0]) == nil {
		t.Fatalf("the newest block carries the moving breakpoint: %#v", last)
	}

	plain := anthropicRequestBody(config, messages, false)
	encoded, _ := json.Marshal(plain)
	if strings.Contains(string(encoded), "cache_control") || plain["system"] != "rules" {
		t.Fatalf("cache disabled must send no markers: %s", encoded)
	}
}

func TestAnthropicRequestBodyCachesPlainUserText(t *testing.T) {
	t.Parallel()

	body := anthropicRequestBody(&aiStreamConfig{Model: "claude"}, []chatMessage{{Role: "user", Content: "你好"}}, true)
	content := body["messages"].([]map[string]any)[0]["content"].([]map[string]any)
	if len(content) != 1 || content[0]["text"] != "你好" || cacheControl(content[0]) == nil {
		t.Fatalf("plain text becomes one cached block: %#v", content)
	}
	if _, ok := body["system"]; ok {
		t.Fatal("no system field without system messages")
	}
}

func TestStreamAnthropicRetriesWithoutCacheWhenRejected(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(raw))
		first := len(bodies) == 1
		mu.Unlock()
		if first {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Extra inputs are not permitted: cache_control"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":7}}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	emitter := &streamEmitter{emit: func(map[string]any) {}}
	config := &aiStreamConfig{BaseURL: server.URL, Model: "claude"}
	if err := streamAnthropic(context.Background(), server.Client(), config, []chatMessage{{Role: "user", Content: "hello"}}, emitter); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || !strings.Contains(bodies[0], "cache_control") || strings.Contains(bodies[1], "cache_control") {
		t.Fatalf("expected a cached attempt then a plain retry, got %q", bodies)
	}
}
