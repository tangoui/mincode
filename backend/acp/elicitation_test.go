package acp

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"mincode/app/backend/core"

	acpsdk "github.com/coder/acp-go-sdk"
)

func TestScopeElicitationParamsCopiesSessionAndToolCall(t *testing.T) {
	create := `{"jsonrpc":"2.0","id":4,"method":"elicitation/create","params":{"sessionId":"s1","toolCallId":"t1","_meta":{"codex":{"delivery":"async"}}}}` + "\n"
	update := " \t" + `{"method": "session/update", "params": {"sessionId":"s1"}}` + "\r\n"
	output, err := io.ReadAll(scopeElicitationParams(strings.NewReader(create + update)))
	if err != nil {
		t.Fatal(err)
	}
	first, rest, _ := strings.Cut(string(output), "\n")
	if rest != update {
		t.Fatalf("session/update = %q, want %q", rest, update)
	}
	var message struct {
		Params struct {
			Meta map[string]json.RawMessage `json:"_meta"`
		} `json:"params"`
	}
	if err := json.Unmarshal([]byte(first), &message); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"mincode/sessionId":  `"s1"`,
		"mincode/toolCallId": `"t1"`,
		"codex":              `{"delivery":"async"}`,
	} {
		if got := string(message.Params.Meta[key]); got != want {
			t.Errorf("metadata %s = %s, want %s", key, got, want)
		}
	}
}

func TestScopeElicitationParamsPassesThroughMalformed(t *testing.T) {
	input := "starting agent\n" + `{"method":"elicitation/create","params":`
	output, err := io.ReadAll(scopeElicitationParams(strings.NewReader(input)))
	if err != nil || string(output) != input {
		t.Fatalf("got (%q, %v), want (%q, nil)", output, err, input)
	}
}

func TestScopeElicitationParamsStreamsOversizedLine(t *testing.T) {
	oversized := strings.Repeat("x", 11*1024*1024) + "\n"
	create := `{"method":"elicitation/create","params":{"sessionId":"s1"}}` + "\n"
	source := strings.NewReader(oversized + create)
	reader := scopeElicitationParams(source)
	var first [1]byte
	if _, err := io.ReadFull(reader, first[:]); err != nil {
		t.Fatal(err)
	}
	if source.Len() <= len(create) {
		t.Fatal("scope reader read the entire oversized line before returning any bytes")
	}
	rest, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) < len(oversized)-1 || first[0] != 'x' || string(rest[:len(oversized)-1]) != oversized[1:] {
		t.Fatal("oversized line changed")
	}
	if !strings.Contains(string(rest[len(oversized)-1:]), `"mincode/sessionId":"s1"`) {
		t.Fatal("next line was not scoped")
	}
}

func TestBridgeClientElicitationAcceptRoundTrip(t *testing.T) {
	events := captureElicitationEvents(t)
	request := acpsdk.UnstableCreateElicitationRequest{Form: &acpsdk.UnstableCreateElicitationForm{
		Meta: map[string]any{
			"mincode/sessionId":  "s1",
			"mincode/toolCallId": "t1",
			"codex":              map[string]any{"delivery": "async"},
		},
		Mode:    "form",
		Message: "Pick one",
		RequestedSchema: acpsdk.UnstableElicitationSchema{
			Type:       "object",
			Properties: map[string]any{"q": map[string]any{"type": "string"}},
			Required:   []string{"q"},
		},
	}}
	result := startElicitation(t, t.Context(), request)
	event := awaitElicitation(t, events)
	requestID, _ := event.Payload["requestId"].(string)
	if event.Name != "acp:elicitation-request" || requestID == "" ||
		event.Payload["serverId"] != "server" || event.Payload["sessionId"] != "s1" ||
		event.Payload["toolCallId"] != "t1" || event.Payload["message"] != "Pick one" {
		t.Fatalf("unexpected request event: %+v", event)
	}
	meta, _ := json.Marshal(event.Payload["meta"])
	if string(meta) != `{"codex":{"delivery":"async"}}` {
		t.Fatalf("meta = %s", meta)
	}
	schema, _ := json.Marshal(event.Payload["requestedSchema"])
	if string(schema) != `{"properties":{"q":{"type":"string"}},"required":["q"],"type":"object"}` {
		t.Fatalf("requestedSchema = %s", schema)
	}
	if err := resolveElicitation(requestID, "server", "s1", "accept", map[string]any{"q": "A"}); err != nil {
		t.Fatal(err)
	}
	response := awaitElicitation(t, result)
	if response.Accept == nil || response.Accept.Content["q"] != "A" {
		t.Fatalf("unexpected response: %+v", response)
	}
}

func TestBridgeClientElicitationContextCancelWithdraws(t *testing.T) {
	events := captureElicitationEvents(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := startElicitation(t, ctx, acpsdk.NewUnstableCreateElicitationRequestForm(acpsdk.UnstableElicitationSchema{}))
	request := awaitElicitation(t, events)
	cancel()
	response := awaitElicitation(t, result)
	if response.Cancel == nil {
		t.Fatalf("expected cancel response, got %+v", response)
	}
	closed := awaitElicitation(t, events)
	if closed.Name != "acp:elicitation-closed" || closed.Payload["reason"] != "withdrawn" ||
		closed.Payload["requestId"] != request.Payload["requestId"] ||
		closed.Payload["serverId"] != "server" || closed.Payload["sessionId"] != "" {
		t.Fatalf("unexpected closed event: %+v", closed)
	}
}

func TestResolveElicitationRejectsWrongOwnerAndDoubleAnswer(t *testing.T) {
	events := captureElicitationEvents(t)
	result := startElicitation(t, t.Context(), acpsdk.NewUnstableCreateElicitationRequestForm(acpsdk.UnstableElicitationSchema{}))
	event := awaitElicitation(t, events)
	requestID, _ := event.Payload["requestId"].(string)
	for _, attempt := range []struct {
		requestID string
		serverID  string
		sessionID string
		action    string
	}{
		{"unknown", "server", "", "decline"},
		{requestID, "other", "", "decline"},
		{requestID, "server", "s1", "decline"},
		{requestID, "server", "", "invalid"},
	} {
		if err := resolveElicitation(attempt.requestID, attempt.serverID, attempt.sessionID, attempt.action, nil); err == nil {
			t.Errorf("accepted invalid response: %+v", attempt)
		}
	}
	// A form with no wire session ID is routed by the frontend, but answered with its original ID.
	args, err := json.Marshal(map[string]any{
		"requestId": requestID, "serverId": "server", "sessionId": "", "action": "decline",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := core.Dispatch("acp_respond_elicitation", string(args)); err != nil {
		t.Fatal(err)
	}
	if _, err := core.Dispatch("acp_respond_elicitation", string(args)); err == nil {
		t.Fatal("accepted a second response")
	}
	if response := awaitElicitation(t, result); response.Decline == nil {
		t.Fatalf("expected decline response, got %+v", response)
	}
}

func TestBridgeClientElicitationDeclinesURL(t *testing.T) {
	events := captureElicitationEvents(t)
	request := acpsdk.NewUnstableCreateElicitationRequestUrl("e1", "https://example.com")
	result := startElicitation(t, t.Context(), request)
	if response := awaitElicitation(t, result); response.Decline == nil {
		t.Fatalf("expected decline response, got %+v", response)
	}
	select {
	case event := <-events:
		t.Fatalf("unexpected event for URL request: %+v", event)
	default:
	}
}

func TestSDKWireElicitationWithdraws(t *testing.T) {
	for _, end := range []string{"agent cancels request", "transport closes"} {
		t.Run(end, func(t *testing.T) {
			events := captureElicitationEvents(t)
			stdout, agent := io.Pipe()
			t.Cleanup(func() { _ = agent.Close() })
			replies := make(collectElicitationReplies, 1)
			conn := acpsdk.NewClientSideConnection(newBridgeClient("server"), replies, scopeElicitationParams(stdout))
			_, err := io.WriteString(agent, `{"jsonrpc":"2.0","id":1,"method":"elicitation/create","params":{"mode":"form","message":"Pick one","sessionId":"s1","toolCallId":"t1","requestedSchema":{"type":"object","properties":{"q":{"type":"string"}}}}}`+"\n")
			if err != nil {
				t.Fatal(err)
			}
			request := awaitElicitation(t, events)
			if request.Payload["sessionId"] != "s1" || request.Payload["toolCallId"] != "t1" {
				t.Fatalf("wire identifiers were lost: %+v", request)
			}
			if end == "agent cancels request" {
				if _, err := io.WriteString(agent, `{"jsonrpc":"2.0","method":"$/cancel_request","params":{"requestId":1}}`+"\n"); err != nil {
					t.Fatal(err)
				}
			} else {
				_ = agent.Close()
				awaitElicitation(t, conn.Done())
			}
			closed := awaitElicitation(t, events)
			if closed.Name != "acp:elicitation-closed" || closed.Payload["reason"] != "withdrawn" ||
				closed.Payload["requestId"] != request.Payload["requestId"] {
				t.Fatalf("unexpected closed event: %+v", closed)
			}
			var reply struct {
				Result struct {
					Action string `json:"action"`
				} `json:"result"`
			}
			data := awaitElicitation(t, replies)
			if err := json.Unmarshal(data, &reply); err != nil || reply.Result.Action != "cancel" {
				t.Fatalf("expected wire cancel, got %s (%v)", data, err)
			}
		})
	}
}

type collectElicitationReplies chan []byte

func (c collectElicitationReplies) Write(p []byte) (int, error) {
	c <- append([]byte(nil), p...)
	return len(p), nil
}

type capturedElicitationEvent struct {
	Name    string
	Payload map[string]any
}

func captureElicitationEvents(t *testing.T) <-chan capturedElicitationEvent {
	t.Helper()
	events := make(chan capturedElicitationEvent, 4)
	core.SetEmitter(func(name string, data any) {
		if !strings.HasPrefix(name, "acp:elicitation-") {
			return
		}
		raw, err := json.Marshal(data)
		var payload map[string]any
		if err == nil {
			err = json.Unmarshal(raw, &payload)
		}
		if err != nil {
			t.Errorf("marshal %s: %v", name, err)
		}
		events <- capturedElicitationEvent{Name: name, Payload: payload}
	})
	t.Cleanup(func() { core.SetEmitter(nil) })
	return events
}

func startElicitation(t *testing.T, ctx context.Context, request acpsdk.UnstableCreateElicitationRequest) <-chan acpsdk.UnstableCreateElicitationResponse {
	t.Helper()
	result := make(chan acpsdk.UnstableCreateElicitationResponse, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		response, err := newBridgeClient("server").UnstableCreateElicitation(ctx, request)
		if err != nil {
			t.Errorf("create elicitation: %v", err)
		}
		result <- response
	}()
	t.Cleanup(func() {
		// t.Context is cancelled before cleanup, so even an unanswered form can exit.
		awaitElicitation(t, done)
	})
	return result
}

func awaitElicitation[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case result := <-ch:
		return result
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for elicitation")
		var zero T
		return zero
	}
}
