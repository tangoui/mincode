package ai

import "testing"

func TestAnthropicToolInputAlwaysReturnsObject(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		arguments string
		wantEmpty bool
	}{
		{name: "object", arguments: `{"path":"README.md"}`},
		{name: "empty object", arguments: `{}`, wantEmpty: true},
		{name: "empty", arguments: ``, wantEmpty: true},
		{name: "malformed", arguments: `{`, wantEmpty: true},
		{name: "null", arguments: `null`, wantEmpty: true},
		{name: "array", arguments: `[]`, wantEmpty: true},
		{name: "string", arguments: `"value"`, wantEmpty: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := anthropicToolInput(test.arguments)
			if input == nil {
				t.Fatal("tool input must never be nil")
			}
			if test.wantEmpty && len(input) != 0 {
				t.Fatalf("expected empty object, got %#v", input)
			}
			if !test.wantEmpty && input["path"] != "README.md" {
				t.Fatalf("expected valid object to be preserved, got %#v", input)
			}
		})
	}
}

func TestAnthropicMessageSanitizesHistoricalToolArguments(t *testing.T) {
	t.Parallel()

	payload := anthropicMessageJSON(&chatMessage{
		Role: "assistant",
		ToolCalls: []toolCallInfo{{
			ID:        "call-1",
			Name:      "workspace.read_file",
			Arguments: "null",
		}},
	})
	blocks, ok := payload["content"].([]map[string]any)
	if !ok || len(blocks) != 1 {
		t.Fatalf("unexpected Anthropic content: %#v", payload["content"])
	}
	input, ok := blocks[0]["input"].(map[string]any)
	if !ok || input == nil || len(input) != 0 {
		t.Fatalf("tool_use.input must be an object, got %#v", blocks[0]["input"])
	}
}

func TestAnthropicSystemPromptMergesAllSystemMessages(t *testing.T) {
	t.Parallel()

	messages := []chatMessage{
		{Role: "system", Content: "custom rules"},
		{Role: "system", Content: "built-in safety guidance"},
		{Role: "user", Content: "hello"},
	}
	if got, want := anthropicSystemPrompt(messages), "custom rules\n\nbuilt-in safety guidance"; got != want {
		t.Fatalf("unexpected merged system prompt: got %q want %q", got, want)
	}
}

func TestTokenUsageEmitterIncludesProviderUsage(t *testing.T) {
	t.Parallel()

	var emitted map[string]any
	emitter := &streamEmitter{emit: func(payload map[string]any) { emitted = payload }}
	emitter.usage(aiTokenUsage{
		InputTokens:       120,
		OutputTokens:      30,
		CachedInputTokens: 80,
		CacheWriteTokens:  10,
		ReasoningTokens:   5,
	}, "test-model")
	if emitted["type"] != "usage" || emitted["inputTokens"] != uint64(120) {
		t.Fatalf("unexpected usage event: %#v", emitted)
	}
	if emitted["model"] != "test-model" || emitted["requestCount"] != 1 {
		t.Fatalf("missing usage metadata: %#v", emitted)
	}
}

func TestShouldRequestOpenAIStreamUsage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		baseURL string
		mode    string
		want    bool
	}{
		{name: "auto official", baseURL: "https://api.openai.com/v1", mode: "auto", want: true},
		{name: "legacy empty official", baseURL: "https://api.openai.com/v1", want: true},
		{name: "auto proxy", baseURL: "https://proxy.example.com/v1", mode: "auto", want: false},
		{name: "always proxy", baseURL: "https://proxy.example.com/v1", mode: "always", want: true},
		{name: "never official", baseURL: "https://api.openai.com/v1", mode: "never", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := &aiStreamConfig{BaseURL: test.baseURL, OpenAIStreamUsage: test.mode}
			if got := shouldRequestOpenAIStreamUsage(config); got != test.want {
				t.Fatalf("shouldRequestOpenAIStreamUsage() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestAnthropicRequestBodyThinkingMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		model, effort string
		adaptive      bool
		wantEffort    string
	}{
		{model: "claude-opus-5-5", effort: "high", adaptive: true, wantEffort: "high"},
		{model: "claude-opus-4-7", effort: "ultra", adaptive: true, wantEffort: "max"},
		{model: "claude-sonnet-4-6", effort: "xhigh", adaptive: true, wantEffort: "high"},
		{model: "claude-mythos-5-1", effort: "xhigh", adaptive: true, wantEffort: "xhigh"},
		{model: "claude-sonnet-4-5-20250929", effort: "high"},
		{model: "claude-opus-4-20250514", effort: "high"},
		{model: "deepseek-chat", effort: "high"},
	}
	for _, tt := range tests {
		body := anthropicRequestBody(&aiStreamConfig{Model: tt.model, ReasoningEffort: tt.effort},
			[]chatMessage{{Role: "user", Content: "hi"}}, false)
		thinking, _ := body["thinking"].(map[string]any)
		if tt.adaptive {
			if thinking["type"] != "adaptive" {
				t.Fatalf("%s: thinking = %v, want adaptive", tt.model, thinking)
			}
			cfg, _ := body["output_config"].(map[string]any)
			if cfg["effort"] != tt.wantEffort {
				t.Fatalf("%s: effort = %v, want %s", tt.model, cfg["effort"], tt.wantEffort)
			}
			continue
		}
		if thinking["type"] != "enabled" || thinking["budget_tokens"] == nil {
			t.Fatalf("%s: thinking = %v, want budget form", tt.model, thinking)
		}
		if _, ok := body["output_config"]; ok {
			t.Fatalf("%s: unexpected output_config", tt.model)
		}
	}

	none := anthropicRequestBody(&aiStreamConfig{Model: "claude-opus-5-5", ReasoningEffort: "none"},
		[]chatMessage{{Role: "user", Content: "hi"}}, false)
	if _, ok := none["thinking"]; ok {
		t.Fatalf("effort none should not enable thinking: %v", none["thinking"])
	}
}
