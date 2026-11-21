package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func sseServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	return server
}

func eventsOfType(events []map[string]any, kind string) []map[string]any {
	out := []map[string]any{}
	for _, event := range events {
		if event["type"] == kind {
			out = append(out, event)
		}
	}
	return out
}

func TestForEachSSEDataLineJoinsMultiLineDataAndStopsAtDone(t *testing.T) {
	input := "data: {\"a\":\ndata: 1}\n\n" +
		"event: ping\n\n" +
		"data: {\"b\":2}\n" +
		"data: {\"c\":3}\n\n" +
		"data: [DONE]\n\n" +
		"data: {\"after\":true}\n\n"
	var got []string
	if err := forEachSSEDataLine(strings.NewReader(input), func(data string) bool { got = append(got, data); return false }); err != nil {
		t.Fatal(err)
	}
	want := []string{"{\"a\":\n1}", "{\"b\":2}", "{\"c\":3}"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestForEachSSEDataLineFlushesPendingAtEOF(t *testing.T) {
	var got []string
	if err := forEachSSEDataLine(strings.NewReader("data: {\"x\":1}"), func(data string) bool { got = append(got, data); return false }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "{\"x\":1}" {
		t.Fatalf("got %q", got)
	}
}

func TestStreamAnthropicReturnsMidStreamError(t *testing.T) {
	server := sseServer(t, "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\n"+
		"data: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n")
	rec := &recordedEvents{}
	err := streamAnthropic(context.Background(), server.Client(), &aiStreamConfig{BaseURL: server.URL, Model: "claude"},
		[]chatMessage{{Role: "user", Content: "hi"}}, &streamEmitter{emit: rec.emit})
	if err == nil || !strings.Contains(err.Error(), "Overloaded") {
		t.Fatalf("expected overloaded error, got %v", err)
	}
}

func TestStreamOpenAIReturnsErrorFrame(t *testing.T) {
	server := sseServer(t, "data: {\"error\":{\"message\":\"rate limited\"}}\n\n")
	rec := &recordedEvents{}
	err := streamOpenAI(context.Background(), server.Client(), &aiStreamConfig{BaseURL: server.URL, Model: "gpt-4o"},
		[]chatMessage{{Role: "user", Content: "hi"}}, &streamEmitter{emit: rec.emit})
	if err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("expected rate limited error, got %v", err)
	}
}

func TestStreamOpenAIResponsesHandlesIncompleteAndErrorMessage(t *testing.T) {
	server := sseServer(t, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"+
		"data: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\",\"usage\":{\"input_tokens\":3,\"output_tokens\":4}}}\n\n")
	rec := &recordedEvents{}
	em := &streamEmitter{emit: rec.emit}
	if err := streamOpenAIResponses(context.Background(), server.Client(), &aiStreamConfig{BaseURL: server.URL, Model: "gpt-5"},
		[]chatMessage{{Role: "user", Content: "hi"}}, em); err != nil {
		t.Fatal(err)
	}
	events := rec.snapshot()
	if len(eventsOfType(events, "truncated")) != 1 || len(eventsOfType(events, "usage")) != 1 {
		t.Fatalf("expected truncated + usage, got %v", eventSummary(events))
	}

	errServer := sseServer(t, "data: {\"type\":\"error\",\"code\":\"server_error\",\"message\":\"boom\"}\n\n")
	err := streamOpenAIResponses(context.Background(), errServer.Client(), &aiStreamConfig{BaseURL: errServer.URL, Model: "gpt-5"},
		[]chatMessage{{Role: "user", Content: "hi"}}, &streamEmitter{emit: func(map[string]any) {}})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected top-level error message, got %v", err)
	}
}

func TestToolCallAccumulatorWithoutIndex(t *testing.T) {
	acc := &toolCallAccumulator{}
	frames := []string{
		`{"id":"a","function":{"name":"read","arguments":"{\"p\":"}}`,
		`{"function":{"arguments":"1}"}}`,
		`{"id":"b","function":{"name":"write","arguments":"{}"}}`,
		`{"id":"a","function":{"name":"read"}}`,
		`{"index":100000,"id":"x","function":{"name":"evil"}}`,
	}
	for _, frame := range frames {
		acc.mergeOpenAIDelta([]json.RawMessage{json.RawMessage(frame)})
	}
	calls := acc.pending()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %#v", calls)
	}
	if calls[0].id != "a" || calls[0].name != "read" || calls[0].args != `{"p":1}` {
		t.Fatalf("first call = %#v", calls[0])
	}
	if calls[1].id != "b" || calls[1].name != "write" {
		t.Fatalf("second call = %#v", calls[1])
	}
}

func TestToolCallAccumulatorConcatenatesSplitNames(t *testing.T) {
	acc := &toolCallAccumulator{}
	for _, frame := range []string{
		`{"index":0,"id":"c","function":{"name":"workspace_"}}`,
		`{"index":0,"function":{"name":"read_file"}}`,
	} {
		acc.mergeOpenAIDelta([]json.RawMessage{json.RawMessage(frame)})
	}
	if calls := acc.pending(); len(calls) != 1 || calls[0].name != "workspace_read_file" {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestTruncatedStreamDropsIncompleteToolCalls(t *testing.T) {
	server := sseServer(t, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"t1\",\"name\":\"ok\"}}\n\n"+
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{}\"}}\n\n"+
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n"+
		"data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"t2\",\"name\":\"cut\"}}\n\n"+
		"data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"path\\\":\\\"a\"}}\n\n"+
		"data: {\"type\":\"content_block_stop\",\"index\":1}\n\n"+
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"max_tokens\"},\"usage\":{\"output_tokens\":5}}\n\n")
	rec := &recordedEvents{}
	if err := streamAnthropic(context.Background(), server.Client(), &aiStreamConfig{BaseURL: server.URL, Model: "claude"},
		[]chatMessage{{Role: "user", Content: "hi"}}, &streamEmitter{emit: rec.emit}); err != nil {
		t.Fatal(err)
	}
	calls := eventsOfType(rec.snapshot(), "tool_call")
	if len(calls) != 1 || calls[0]["id"] != "t1" {
		t.Fatalf("expected only the complete call, got %v", calls)
	}
	if len(eventsOfType(rec.snapshot(), "truncated")) != 1 {
		t.Fatal("expected truncated event")
	}
}

func TestStreamAnthropicEmitsSignedThinkingBlocks(t *testing.T) {
	server := sseServer(t, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\",\"signature\":\"\"}}\n\n"+
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"hmm\"}}\n\n"+
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"sig\"}}\n\n"+
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n"+
		"data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"redacted_thinking\",\"data\":\"enc\"}}\n\n"+
		"data: {\"type\":\"content_block_stop\",\"index\":1}\n\n")
	rec := &recordedEvents{}
	if err := streamAnthropic(context.Background(), server.Client(), &aiStreamConfig{BaseURL: server.URL, Model: "claude"},
		[]chatMessage{{Role: "user", Content: "hi"}}, &streamEmitter{emit: rec.emit}); err != nil {
		t.Fatal(err)
	}
	blocks := eventsOfType(rec.snapshot(), "thinking_block")
	if len(blocks) != 2 || blocks[0]["thinking"] != "hmm" || blocks[0]["signature"] != "sig" ||
		blocks[1]["blockType"] != "redacted_thinking" || blocks[1]["data"] != "enc" {
		t.Fatalf("thinking blocks = %v", blocks)
	}
}

func TestAnthropicRequestBodyReplaysThinkingInToolLoop(t *testing.T) {
	messages := []chatMessage{
		{Role: "user", Content: "old"},
		{Role: "assistant", Content: "old answer", ThinkingBlocks: []thinkingBlock{{Type: "thinking", Thinking: "x", Signature: "old"}}},
		{Role: "user", Content: "do it"},
		{Role: "assistant", ToolCalls: []toolCallInfo{{ID: "t1", Name: "read", Arguments: "{}"}},
			ThinkingBlocks: []thinkingBlock{{Type: "thinking", Thinking: "plan", Signature: "sig"}}},
		{Role: "tool", ToolCallID: "t1", Content: "ok"},
	}
	config := &aiStreamConfig{Model: "claude-sonnet-4-5", ReasoningEffort: "high", MaxOutputTokens: 32000}
	body := anthropicRequestBody(config, messages, false)
	if body["thinking"] == nil {
		t.Fatal("thinking should stay enabled when blocks are replayable")
	}
	chat := body["messages"].([]map[string]any)
	if _, isString := chat[1]["content"].(string); !isString {
		t.Fatalf("earlier-turn thinking must not be replayed: %#v", chat[1]["content"])
	}
	current := chat[3]["content"].([]map[string]any)
	if current[0]["type"] != "thinking" || current[0]["signature"] != "sig" || current[1]["type"] != "tool_use" {
		t.Fatalf("current turn must start with thinking: %#v", current)
	}

	messages[3].ThinkingBlocks = nil
	body = anthropicRequestBody(config, messages, false)
	if body["thinking"] != nil || body["output_config"] != nil {
		t.Fatalf("thinking must be disabled when the tool loop has no replayable blocks: %#v", body["thinking"])
	}
}

func TestAICheckRedirectStripsCredentialsAcrossHosts(t *testing.T) {
	first, _ := http.NewRequest(http.MethodPost, "https://api.example.com/v1/messages", nil)
	sameHost, _ := http.NewRequest(http.MethodPost, "https://api.example.com/v2/messages", nil)
	sameHost.Header.Set("x-api-key", "secret")
	if err := aiCheckRedirect(sameHost, []*http.Request{first}); err != nil || sameHost.Header.Get("x-api-key") == "" {
		t.Fatalf("same-host redirect must keep credentials: %v", err)
	}
	otherHost, _ := http.NewRequest(http.MethodPost, "https://evil.example.net/v1/messages", nil)
	otherHost.Header.Set("x-api-key", "secret")
	otherHost.Header.Set("Authorization", "Bearer secret")
	if err := aiCheckRedirect(otherHost, []*http.Request{first}); err != nil {
		t.Fatal(err)
	}
	if otherHost.Header.Get("x-api-key") != "" || otherHost.Header.Get("Authorization") != "" {
		t.Fatal("cross-host redirect must drop credentials")
	}
}

func TestAICheckRedirectRejectsHTTPSDowngrade(t *testing.T) {
	first, _ := http.NewRequest(http.MethodPost, "https://api.example.com/v1/messages", nil)
	downgrade, _ := http.NewRequest(http.MethodPost, "http://api.example.com/v1/messages", nil)
	if err := aiCheckRedirect(downgrade, []*http.Request{first}); err == nil {
		t.Fatal("same-host https→http redirect must be rejected")
	}
	plainFirst, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:8080/v1", nil)
	plainNext, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:8080/v2", nil)
	if err := aiCheckRedirect(plainNext, []*http.Request{plainFirst}); err != nil {
		t.Fatalf("http→http redirect must stay allowed: %v", err)
	}
}

func TestAIStreamClientDoesNotSendCredentialsAfterDowngrade(t *testing.T) {
	var plainHits atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plainHits.Add(1)
	}))
	defer plain.Close()
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/v1/messages", http.StatusTemporaryRedirect)
	}))
	defer secure.Close()

	client := secure.Client()
	client.CheckRedirect = aiCheckRedirect
	req, _ := http.NewRequest(http.MethodPost, secure.URL+"/v1/messages", strings.NewReader(`{"prompt":"secret"}`))
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := client.Do(req)
	if err == nil {
		resp.Body.Close()
		t.Fatal("https→http redirect must fail")
	}
	if plainHits.Load() != 0 {
		t.Fatal("plain http target must not be contacted")
	}
}

func TestOpenAIReasoningModelParameters(t *testing.T) {
	cases := map[string]bool{"o1": true, "o3-mini": true, "o4-mini": true, "gpt-5": true, "gpt-5.1": true,
		"openai/gpt-5-mini": true, "gpt-6-astra": true, "gpt-6.1-sol": true, "openai/gpt-6-luna": true,
		"gpt-4o": false, "gpt-4.1": false, "omni": false}
	for model, want := range cases {
		if got := isOpenAIReasoningModel(model); got != want {
			t.Errorf("isOpenAIReasoningModel(%q) = %v", model, got)
		}
	}
	if got := openAIMaxTokensField(&aiStreamConfig{Model: "o3"}); got != "max_completion_tokens" {
		t.Fatalf("o3 field = %s", got)
	}
	if got := openAIMaxTokensField(&aiStreamConfig{Model: "deepseek-chat", BaseURL: "https://proxy.local"}); got != "max_tokens" {
		t.Fatalf("compat field = %s", got)
	}
}

func TestReadErrorBodyTruncates(t *testing.T) {
	text := readErrorBody(strings.NewReader(strings.Repeat("错", 10000)))
	if len(text) > aiErrorTextLimit+len("…") || !strings.HasSuffix(text, "…") {
		t.Fatalf("error text not truncated: %d bytes", len(text))
	}
}

func TestStreamEmitterDiscardDropsBufferedAndLateEvents(t *testing.T) {
	rec := &recordedEvents{}
	em := &streamEmitter{emit: rec.emit}
	em.chunk("buffered")
	em.discard()
	em.chunk("late")
	em.done()
	time.Sleep(3 * streamEmitterFlushDelay)
	if got := rec.snapshot(); len(got) != 0 {
		t.Fatalf("no events expected after discard, got %v", eventSummary(got))
	}
}

// hangingSSEServer 写完 body 后保持连接不关闭，模拟最后一个事件后仍挂起的代理。
func hangingSSEServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() {
		close(release)
		server.Close()
	})
	return server
}

func runWithDeadline(t *testing.T, fn func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not finish after terminal event")
		return nil
	}
}

func TestStreamsStopAtTerminalEventWithoutConnectionClose(t *testing.T) {
	msgs := []chatMessage{{Role: "user", Content: "hi"}}
	cases := []struct {
		name string
		body string
		run  func(*httptest.Server, *streamEmitter) error
	}{
		{"anthropic", "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
			"data: {\"type\":\"message_stop\"}\n\n",
			func(s *httptest.Server, em *streamEmitter) error {
				return streamAnthropic(context.Background(), s.Client(), &aiStreamConfig{BaseURL: s.URL, Model: "claude"}, msgs, em)
			}},
		{"responses", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n" +
			"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n",
			func(s *httptest.Server, em *streamEmitter) error {
				return streamOpenAIResponses(context.Background(), s.Client(), &aiStreamConfig{BaseURL: s.URL, Model: "gpt-5"}, msgs, em)
			}},
		{"openai-chat", "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n",
			func(s *httptest.Server, em *streamEmitter) error {
				return streamOpenAI(context.Background(), s.Client(), &aiStreamConfig{BaseURL: s.URL, Model: "gpt-4o"}, msgs, em)
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := hangingSSEServer(t, tc.body)
			rec := &recordedEvents{}
			err := runWithDeadline(t, func() error { return tc.run(server, &streamEmitter{emit: rec.emit}) })
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
