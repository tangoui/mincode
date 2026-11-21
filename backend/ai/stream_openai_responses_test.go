package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
)

func TestOpenAIResponsesStreamsWithToolSchemaAndConversationHistory(t *testing.T) {
	const schema = `{"type":"object","properties":{"tasks":{"type":"array","items":{"type":"object",` +
		`"properties":{"dependsOn":{"type":"array","items":{"type":"string"},"uniqueItems":true}}}}}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Input []struct {
				Type    string `json:"type"`
				Role    string `json:"role"`
				CallID  string `json:"call_id"`
				Output  string `json:"output"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"input"`
			Tools []struct {
				Strict     *bool           `json:"strict"`
				Parameters json.RawMessage `json:"parameters"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if r.URL.Path != "/v1/responses" || len(request.Tools) != 1 {
			http.Error(w, "expected a Responses request with one tool", http.StatusBadRequest)
			return
		}
		tool := request.Tools[0]
		if tool.Strict == nil || *tool.Strict {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"code":"invalid_json_schema","message":"Unimplemented keys: [\"uniqueItems\"]"}}`)
			return
		}
		if string(tool.Parameters) != schema {
			t.Errorf("tool parameters = %s, want %s", tool.Parameters, schema)
		}
		if len(request.Input) != 7 {
			http.Error(w, "expected seven conversation items", http.StatusBadRequest)
			return
		}
		for i, want := range []struct{ role, contentType, text string }{
			{"system", "input_text", "Be brief."},
			{"user", "input_text", "Hello"},
			{"assistant", "output_text", "How can I help?"},
			{"user", "input_text", "Read the plan."},
			{"assistant", "output_text", "I'll look it up."},
		} {
			item := request.Input[i]
			if item.Type != "message" || item.Role != want.role || len(item.Content) != 1 ||
				item.Content[0].Type != want.contentType || item.Content[0].Text != want.text {
				http.Error(w, fmt.Sprintf("invalid input[%d]: %+v", i, item), http.StatusBadRequest)
				return
			}
		}
		call, result := request.Input[5], request.Input[6]
		if call.Type != "function_call" || call.CallID != "call-1" ||
			result.Type != "function_call_output" || result.CallID != call.CallID || result.Output != "Ready." {
			http.Error(w, "expected a matched tool call and result", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n"+
			"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	}))
	t.Cleanup(server.Close)

	events := &recordedEvents{}
	err := runAiStreamChat(context.Background(), &aiStreamChatArgs{
		Config: aiStreamConfig{
			BaseURL: server.URL, Model: "gpt-5", APIFormat: "openai-responses",
			Tools: []aiToolDef{{Name: "agent_run_parallel", InputSchema: json.RawMessage(schema)}},
		},
		Messages: []chatMessage{
			{Role: "system", Content: "Be brief."},
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "How can I help?"},
			{Role: "user", Content: "Read the plan."},
			{Role: "assistant", Content: "I'll look it up.", ToolCalls: []toolCallInfo{
				{ID: "call-1", Name: "agent_run_parallel", Arguments: "{}"},
			}},
			{Role: "tool", Content: "Ready.", ToolCallID: "call-1"},
		},
	}, &streamEmitter{emit: events.emit})
	if err != nil {
		t.Fatal(err)
	}
	if got := eventsOfType(events.snapshot(), "done"); len(got) != 1 {
		t.Fatalf("expected one done event, got %v", got)
	}
}

func TestOpenAIResponsesInputNormalizesPersistedToolNames(t *testing.T) {
	messages := []chatMessage{{
		Role: "assistant",
		ToolCalls: []toolCallInfo{
			{ID: "call-1", Name: "workspace.read_file", Arguments: `{"path":"README.md"}`},
			{ID: "call-2", Name: "  mcp:读取 文件  ", Arguments: `{}`},
			{ID: "call-3", Name: "already-valid_2", Arguments: `{}`},
		},
	}}

	items := openaiResponsesInput(messages)
	if len(items) != 3 {
		t.Fatalf("expected 3 function calls, got %d", len(items))
	}

	want := []string{"workspace_read_file", "mcp", "already-valid_2"}
	validName := regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	for i, item := range items {
		name, _ := item["name"].(string)
		if name != want[i] {
			t.Errorf("item %d name = %q, want %q", i, name, want[i])
		}
		if !validName.MatchString(name) {
			t.Errorf("item %d has invalid provider name %q", i, name)
		}
	}
}

func TestOpenAIProviderToolNameFallsBackForInvalidOnlyName(t *testing.T) {
	if got := openAIProviderToolName("中文 / ."); got != "tool" {
		t.Fatalf("openAIProviderToolName() = %q, want tool", got)
	}
}
