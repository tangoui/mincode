package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"mincode/app/backend/core"
	"mincode/app/plugins/api"
)

func captureUIMessages(t *testing.T) chan map[string]any {
	t.Helper()
	events := make(chan map[string]any, 64)
	core.SetEmitter(func(name string, data any) {
		if name == pluginUIMessageEvent {
			events <- data.(map[string]any)
		}
	})
	t.Cleanup(func() { core.SetEmitter(nil) })
	return events
}

func uiMessageRuntime(capabilities ...api.Capability) *processRuntime {
	r := newProcessRuntime("", Manifest{ID: "runtime.ui-messages", Capabilities: capabilities}, func(string, string) {}, nil)
	r.generation = 3
	return r
}

func TestHostUIPostMessageEmitsToViews(t *testing.T) {
	events := captureUIMessages(t)
	hostUIMessageLimiter = newHostRateLimiter(hostUIMessagesPerSecond, time.Second)
	r := uiMessageRuntime(api.CapabilityUI)

	if _, rpcErr := postUIMessageFromProcess(r, 3, json.RawMessage(`{"message":{"chunk":"he"},"viewId":"main"}`)); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	event := <-events
	if event["pluginId"] != "runtime.ui-messages" || event["generation"] != uint64(3) || event["viewId"] != "main" ||
		string(event["message"].(json.RawMessage)) != `{"chunk":"he"}` {
		t.Fatalf("event = %v", event)
	}
	if _, rpcErr := postUIMessageFromProcess(r, 3, json.RawMessage(`{"message":"broadcast"}`)); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	if event := <-events; event["viewId"] != "" {
		t.Fatalf("broadcast viewId = %v", event["viewId"])
	}
}

func TestHostUIPostMessageRejectsInvalidMessages(t *testing.T) {
	events := captureUIMessages(t)
	hostUIMessageLimiter = newHostRateLimiter(hostUIMessagesPerSecond, time.Second)
	r := uiMessageRuntime(api.CapabilityUI)
	oversized := `{"message":"` + strings.Repeat("x", pluginUIMessageMaxBytes) + `"}`
	for _, params := range []string{`{}`, `{"message":null}`, `[]`, `{"message":1,"viewId":"../x"}`, oversized} {
		if _, rpcErr := postUIMessageFromProcess(r, 3, json.RawMessage(params)); rpcErr == nil || rpcErr.Code != -32602 {
			t.Errorf("params %.40q: error = %v", params, rpcErr)
		}
	}
	if _, rpcErr := postUIMessageFromProcess(r, 2, json.RawMessage(`{"message":1}`)); rpcErr == nil {
		t.Error("message from a replaced generation accepted")
	}
	select {
	case event := <-events:
		t.Fatalf("invalid message emitted: %v", event)
	default:
	}
}

func TestHostUIPostMessageIsRateLimited(t *testing.T) {
	captureUIMessages(t)
	hostUIMessageLimiter = newHostRateLimiter(2, time.Second)
	t.Cleanup(func() { hostUIMessageLimiter = newHostRateLimiter(hostUIMessagesPerSecond, time.Second) })
	r := uiMessageRuntime(api.CapabilityUI)
	for i := 0; i < 2; i++ {
		if _, rpcErr := postUIMessageFromProcess(r, 3, json.RawMessage(`{"message":1}`)); rpcErr != nil {
			t.Fatal(rpcErr)
		}
	}
	if _, rpcErr := postUIMessageFromProcess(r, 3, json.RawMessage(`{"message":1}`)); rpcErr == nil || rpcErr.Code != -32029 {
		t.Fatalf("third message error = %v", rpcErr)
	}
}

func TestHostUIPostMessageNotificationNeedsUICapability(t *testing.T) {
	events := captureUIMessages(t)
	hostUIMessageLimiter = newHostRateLimiter(hostUIMessagesPerSecond, time.Second)
	notification := []byte(`{"jsonrpc":"2.0","method":"host.ui.postMessage","params":{"message":"x"}}`)
	dropped := false
	if !uiMessageRuntime().handleStdoutMessage(3, notification, &dropped) {
		t.Fatal("notification stopped the reader")
	}
	select {
	case event := <-events:
		t.Fatalf("message emitted without the ui capability: %v", event)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestUIMessagesRoundTripInOrder(t *testing.T) {
	events := captureUIMessages(t)
	hostUIMessageLimiter = newHostRateLimiter(hostUIMessagesPerSecond, time.Second)
	runtimeHost, err := startProtocolHelper(t, "runtime.messages", "", api.CapabilityUI)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	generation := runtimeHost.Generation()
	for i := 0; i < 20; i++ {
		message, _ := json.Marshal(map[string]int{"seq": i})
		if _, err := runtimeHost.deliverUIMessage(ctx, generation, "main", message); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 20; i++ {
		select {
		case event := <-events:
			want, _ := json.Marshal(map[string]any{"echo": map[string]int{"seq": i}})
			if event["viewId"] != "main" || string(event["message"].(json.RawMessage)) != string(want) {
				t.Fatalf("message %d = %v %s", i, event["viewId"], event["message"])
			}
		case <-ctx.Done():
			t.Fatalf("message %d was not echoed", i)
		}
	}
	if _, err := runtimeHost.deliverUIMessage(ctx, generation, "main", json.RawMessage(`{`)); err == nil {
		t.Fatal("invalid JSON delivered")
	}
}

// Batched host.ui.postMessage notifications are handled inline like single
// ones, so view messages keep the order the plugin sent them in.
func TestHostUIPostMessageBatchedKeepsOrder(t *testing.T) {
	events := captureUIMessages(t)
	hostUIMessageLimiter = newHostRateLimiter(hostUIMessagesPerSecond, time.Second)
	r := uiMessageRuntime(api.CapabilityUI)
	entries := make([]string, 30)
	for i := range entries {
		entries[i] = fmt.Sprintf(`{"jsonrpc":"2.0","method":"host.ui.postMessage","params":{"message":%d}}`, i)
	}
	dropped := false
	if !r.handleStdoutMessage(3, []byte("["+strings.Join(entries, ",")+"]"), &dropped) {
		t.Fatal("batch stopped the reader")
	}
	for i := range entries {
		select {
		case event := <-events:
			if got := string(event["message"].(json.RawMessage)); got != fmt.Sprint(i) {
				t.Fatalf("message %d = %s", i, got)
			}
		default:
			t.Fatalf("message %d was not emitted inline", i)
		}
	}
}
