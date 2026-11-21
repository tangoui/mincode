package plugin

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"mincode/app/plugins/api"
)

type aiTurnHarness struct {
	registry *aiTurnRegistry
	now      time.Time
	mu       sync.Mutex
	events   []string
	timers   []func()
	grants   map[string]aiGrants
}

func newAITurnHarness() *aiTurnHarness {
	h := &aiTurnHarness{registry: newAITurnRegistry(), now: time.Unix(1_800_000_000, 0), grants: map[string]aiGrants{}}
	h.registry.now = func() time.Time { return h.now }
	h.registry.emit = func(name string, data any) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.events = append(h.events, name)
	}
	h.registry.after = func(_ time.Duration, f func()) { h.timers = append(h.timers, f) }
	h.registry.grants = func(id string) aiGrants { return h.grants[id] }
	return h
}

func (h *aiTurnHarness) start(t *testing.T, pluginID, key, prompt string) aiTurnState {
	t.Helper()
	state, err := h.registry.start(pluginID, "Bot", aiTurnStartParams{ConversationKey: key, Title: "飞书 · 项目群", Sender: "张三", Prompt: prompt})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestAITurnLifecycle(t *testing.T) {
	h := newAITurnHarness()
	state := h.start(t, "chat-bot", "feishu:oc_1", "服务状态如何？")
	if state.Status != aiTurnPending || state.Done || len(h.events) != 1 || h.events[0] != pluginAITurnEvent {
		t.Fatalf("start: %+v %v", state, h.events)
	}
	pending := h.registry.pending()
	if len(pending) != 1 || pending[0].Prompt != "" || pending[0].Title != "飞书 · 项目群" {
		t.Fatalf("pending must list turns without prompts: %+v", pending)
	}
	request, err := h.registry.claim(state.ID)
	if err != nil || request.Prompt != "服务状态如何？" || request.Sender != "张三" || request.PluginName != "Bot" {
		t.Fatalf("claim: %+v %v", request, err)
	}
	if _, err := h.registry.claim(state.ID); err == nil {
		t.Fatal("a turn must be claimed only once")
	}
	text, activity := "正在查询", "调用 plugin.invoke"
	if err := h.registry.update(aiTurnUpdate{ID: state.ID, Status: "awaiting_approval", Text: &text, Activity: &activity, ConversationID: "conversation-abc"}); err != nil {
		t.Fatal(err)
	}
	polled, err := h.registry.poll("chat-bot", state.ID)
	if err != nil || polled.Status != aiTurnAwaitingApproval || polled.Text != text || polled.Activity != activity || polled.ConversationID != "conversation-abc" {
		t.Fatalf("poll: %+v %v", polled, err)
	}
	if _, err := h.registry.poll("other-plugin", state.ID); err == nil {
		t.Fatal("another plugin must not read the turn")
	}
	final := "一切正常"
	if err := h.registry.update(aiTurnUpdate{ID: state.ID, Status: "completed", Text: &final}); err != nil {
		t.Fatal(err)
	}
	polled, _ = h.registry.poll("chat-bot", state.ID)
	if !polled.Done || polled.Status != aiTurnCompleted || polled.Text != final || polled.Activity != "" {
		t.Fatalf("completed: %+v", polled)
	}
	late := "late"
	if err := h.registry.update(aiTurnUpdate{ID: state.ID, Status: "running", Text: &late}); err != nil {
		t.Fatal(err)
	}
	if polled, _ = h.registry.poll("chat-bot", state.ID); polled.Text != final || polled.Status != aiTurnCompleted {
		t.Fatalf("a finished turn must not change: %+v", polled)
	}
	h.now = h.now.Add(aiTurnRetention + time.Minute)
	h.start(t, "chat-bot", "feishu:oc_1", "again")
	if _, err := h.registry.poll("chat-bot", state.ID); err == nil {
		t.Fatal("finished turns must be pruned after retention")
	}
}

func TestAITurnValidationAndLimits(t *testing.T) {
	h := newAITurnHarness()
	for name, params := range map[string]aiTurnStartParams{
		"empty prompt":     {ConversationKey: "k", Prompt: "  "},
		"huge prompt":      {ConversationKey: "k", Prompt: strings.Repeat("x", aiTurnMaxPromptBytes+1)},
		"bad key":          {ConversationKey: "a/b", Prompt: "hi"},
		"bad conversation": {ConversationKey: "k", ConversationID: "../x", Prompt: "hi"},
	} {
		if _, err := h.registry.start("p", "P", params); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	for i := 0; i < aiTurnMaxActive; i++ {
		h.start(t, "p", "k", "hi")
	}
	if _, err := h.registry.start("p", "P", aiTurnStartParams{ConversationKey: "k", Prompt: "hi"}); err == nil {
		t.Fatal("active turn limit not enforced")
	}
	h.start(t, "q", "k", "another plugin has its own limit")

	h = newAITurnHarness()
	for i := 0; i < aiTurnMaxPerHour; i++ {
		state := h.start(t, "p", "k", "hi")
		if _, err := h.registry.cancel("p", state.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.registry.start("p", "P", aiTurnStartParams{ConversationKey: "k", Prompt: "hi"}); err == nil {
		t.Fatal("hourly limit not enforced")
	}
	h.now = h.now.Add(time.Hour + time.Second)
	h.start(t, "p", "k", "allowed after an hour")
}

func TestAITurnTimeoutsCancelAndStop(t *testing.T) {
	h := newAITurnHarness()
	unclaimed := h.start(t, "p", "k", "hi")
	h.timers[0]()
	if state, _ := h.registry.poll("p", unclaimed.ID); state.Status != aiTurnFailed || !strings.Contains(state.Error, "未就绪") {
		t.Fatalf("unclaimed turn must fail: %+v", state)
	}

	claimed := h.start(t, "p", "k", "hi")
	if _, err := h.registry.claim(claimed.ID); err != nil {
		t.Fatal(err)
	}
	h.timers[2]()
	if state, _ := h.registry.poll("p", claimed.ID); state.Status != aiTurnRunning {
		t.Fatalf("claim timeout must not fail a claimed turn: %+v", state)
	}
	h.events = nil
	h.timers[3]()
	if state, _ := h.registry.poll("p", claimed.ID); state.Status != aiTurnFailed || len(h.events) != 1 || h.events[0] != pluginAITurnCancelEvent {
		t.Fatalf("overlong turn must fail and cancel the run: %+v %v", state, h.events)
	}

	running := h.start(t, "p", "k", "hi")
	h.events = nil
	if _, err := h.registry.cancel("q", running.ID); err == nil {
		t.Fatal("another plugin cancelled the turn")
	}
	state, err := h.registry.cancel("p", running.ID)
	if err != nil || state.Status != aiTurnCancelled || len(h.events) != 1 || h.events[0] != pluginAITurnCancelEvent {
		t.Fatalf("cancel: %+v %v %v", state, err, h.events)
	}

	a, b := h.start(t, "p", "k", "one"), h.start(t, "q", "k", "two")
	h.registry.abandonPlugin("p")
	if state, _ := h.registry.poll("p", a.ID); state.Status != aiTurnCancelled {
		t.Fatalf("stopped plugin's turn must be cancelled: %+v", state)
	}
	if state, _ := h.registry.poll("q", b.ID); state.Status != aiTurnPending {
		t.Fatalf("other plugin's turn must be untouched: %+v", state)
	}
}

func TestAITurnUpdateBounds(t *testing.T) {
	h := newAITurnHarness()
	state := h.start(t, "p", "k", "hi")
	if err := h.registry.update(aiTurnUpdate{ID: state.ID, Status: "running"}); err == nil {
		t.Fatal("an unclaimed turn must not accept updates")
	}
	if _, err := h.registry.claim(state.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.registry.update(aiTurnUpdate{ID: state.ID, Status: "exploded"}); err == nil {
		t.Fatal("unknown status accepted")
	}
	long := strings.Repeat("中", aiTurnMaxTextBytes)
	if err := h.registry.update(aiTurnUpdate{ID: state.ID, Text: &long}); err != nil {
		t.Fatal(err)
	}
	polled, _ := h.registry.poll("p", state.ID)
	if len(polled.Text) > aiTurnMaxTextBytes || !strings.HasPrefix(long, polled.Text) || !json.Valid([]byte(`"`+polled.Text+`"`)) {
		t.Fatalf("text must be cut on a rune boundary within %d bytes: %d", aiTurnMaxTextBytes, len(polled.Text))
	}
}

func TestAICapabilityIsAccepted(t *testing.T) {
	v2 := Manifest{APIVersion: api.ManifestAPIVersionV2, Capabilities: []api.Capability{api.CapabilityAI}}
	if err := validateCapabilities(&v2); err != nil {
		t.Fatal(err)
	}
	if err := validateMarketplaceCapabilities([]api.Capability{api.CapabilityAI}); err != nil {
		t.Fatalf("marketplace installs must accept the ai capability: %v", err)
	}
}

func TestAITurnHostMethodsRequireCapability(t *testing.T) {
	runtimeHost := newProcessRuntime(t.TempDir(), Manifest{ID: "p", APIVersion: api.ManifestAPIVersionV2}, nil, nil)
	var got *rpcError
	runtimeHost.handleAITurnRequest(rpcEnvelope{Method: "host.ai.turn.poll", Params: json.RawMessage(`{"id":"aiturn-missing"}`)}, func(_ any, err *rpcError) { got = err })
	if got == nil || got.Code != -32004 {
		t.Fatalf("unknown turn: %+v", got)
	}
	described, _ := runtimeHost.describeAPI().(map[string]any)
	methods, _ := described["methods"].([]map[string]any)
	found := false
	for _, method := range methods {
		if method["name"] == "host.ai.turn.start" {
			found = true
			if method["available"] != false {
				t.Fatalf("host.ai.turn.start must be unavailable without the ai capability: %+v", method)
			}
		}
	}
	if !found {
		t.Fatal("host.ai.turn.start missing from host.api.describe")
	}
}

func TestAITurnInstructionsAndAllowedPlugins(t *testing.T) {
	h := newAITurnHarness()
	state, err := h.registry.start("bot", "Bot", aiTurnStartParams{
		ConversationKey: "k", Prompt: "hi", Instructions: "  只回答运维问题  ",
		AllowedPlugins: []string{"log-explorer", "xxl-job-manager", "log-explorer"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if pending := h.registry.pending(); pending[0].Instructions != "" || pending[0].AllowedPlugins != nil {
		t.Fatalf("pending must not expose turn details: %+v", pending[0])
	}
	request, err := h.registry.claim(state.ID)
	if err != nil || request.Instructions != "只回答运维问题" || len(request.AllowedPlugins) != 2 || request.AllowedPlugins[1] != "xxl-job-manager" {
		t.Fatalf("claim: %+v %v", request, err)
	}
	open, _ := h.registry.start("bot", "Bot", aiTurnStartParams{ConversationKey: "k", Prompt: "hi"})
	if request, _ := h.registry.claim(open.ID); request.AllowedPlugins != nil {
		t.Fatalf("omitted allowedPlugins must mean no restriction: %+v", request)
	}
	none, _ := h.registry.start("bot", "Bot", aiTurnStartParams{ConversationKey: "k", Prompt: "hi", AllowedPlugins: []string{}})
	if request, _ := h.registry.claim(none.ID); request.AllowedPlugins == nil || len(request.AllowedPlugins) != 0 {
		t.Fatalf("an empty allow-list must forbid every plugin: %+v", request)
	}
	for name, params := range map[string]aiTurnStartParams{
		"long instructions": {ConversationKey: "k", Prompt: "hi", Instructions: strings.Repeat("x", aiTurnMaxInstructions+1)},
		"bad plugin id":     {ConversationKey: "k", Prompt: "hi", AllowedPlugins: []string{"../x"}},
		"too many plugins":  {ConversationKey: "k", Prompt: "hi", AllowedPlugins: make([]string, aiTurnMaxAllowed+1)},
	} {
		if _, err := h.registry.start("other", "O", params); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestAITurnRiskCapGrantsAndChatApproval(t *testing.T) {
	h := newAITurnHarness()
	if _, err := h.registry.start("bot", "Bot", aiTurnStartParams{ConversationKey: "k", Prompt: "hi", MaxRisk: "admin"}); err == nil {
		t.Fatal("unknown maxRisk accepted")
	}
	for _, params := range []aiTurnStartParams{
		{ConversationKey: "k", Prompt: "hi", ChatApproval: true},
		{ConversationKey: "k", Prompt: "hi", WorkspaceAccess: true},
	} {
		if _, err := h.registry.start("bot", "Bot", params); err == nil || !strings.Contains(err.Error(), "not granted") {
			t.Fatalf("ungranted option accepted: %v", err)
		}
	}
	h.grants["bot"] = aiGrants{ChatApproval: true}
	guest := h.start(t, "bot", "k", "hi")
	state, err := h.registry.start("bot", "Bot", aiTurnStartParams{ConversationKey: "k", Prompt: "hi", ChatApproval: true, MaxRisk: "write"})
	if err != nil {
		t.Fatal(err)
	}
	request, _ := h.registry.claim(state.ID)
	if !request.ChatApproval || request.MaxRisk != "write" || request.WorkspaceAccess {
		t.Fatalf("claim must carry the options: %+v", request)
	}
	text := "partial"
	approval := &aiTurnApproval{ID: "approval-1", Summary: "Invoke plugin x", Risk: "write", Tool: "plugin.invoke"}
	if err := h.registry.update(aiTurnUpdate{ID: state.ID, Status: "awaiting_approval", Text: &text, Approval: approval}); err != nil {
		t.Fatal(err)
	}
	polled, _ := h.registry.poll("bot", state.ID)
	if polled.Approval == nil || polled.Approval.ID != "approval-1" {
		t.Fatalf("chat approval not exposed: %+v", polled)
	}
	if _, err := h.registry.approve("other", state.ID, "approval-1", true); err == nil {
		t.Fatal("another plugin approved the turn")
	}
	if _, err := h.registry.approve("bot", state.ID, "approval-2", true); err == nil {
		t.Fatal("a stale approval id was accepted")
	}
	h.events = nil
	approved, err := h.registry.approve("bot", state.ID, "approval-1", true)
	if err != nil || approved.Approval != nil || approved.Status != aiTurnRunning || len(h.events) != 1 || h.events[0] != pluginAITurnApprovalEvent {
		t.Fatalf("approve: %+v %v %v", approved, err, h.events)
	}
	if _, err := h.registry.approve("bot", state.ID, "approval-1", true); err == nil {
		t.Fatal("an approval must be decided only once")
	}

	// A turn without chat approval never exposes approvals, even if the grant exists.
	h.registry.claim(guest.ID)
	_ = h.registry.update(aiTurnUpdate{ID: guest.ID, Status: "awaiting_approval", Approval: approval})
	if polled, _ := h.registry.poll("bot", guest.ID); polled.Approval != nil {
		t.Fatalf("approval leaked to a turn without chat approval: %+v", polled)
	}
	// Revoking the grant on the desktop stops chat decisions immediately.
	chat, _ := h.registry.start("bot", "Bot", aiTurnStartParams{ConversationKey: "k", Prompt: "hi", ChatApproval: true})
	h.registry.claim(chat.ID)
	_ = h.registry.update(aiTurnUpdate{ID: chat.ID, Status: "awaiting_approval", Approval: approval})
	h.grants["bot"] = aiGrants{}
	if _, err := h.registry.approve("bot", chat.ID, "approval-1", true); err == nil {
		t.Fatal("approval accepted after the grant was revoked")
	}
}

func TestAITurnHistoryNone(t *testing.T) {
	h := newAITurnHarness()
	state, err := h.registry.start("drama", "Drama", aiTurnStartParams{ConversationKey: "k", Prompt: "hi", History: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if pending := h.registry.pending(); pending[0].History != "" {
		t.Fatalf("pending must not expose turn details: %+v", pending[0])
	}
	request, err := h.registry.claim(state.ID)
	if err != nil || request.History != "none" {
		t.Fatalf("claim must forward history none: %+v %v", request, err)
	}
	encoded, _ := json.Marshal(request)
	if !strings.Contains(string(encoded), `"history":"none"`) {
		t.Fatalf("claim JSON: %s", encoded)
	}
	open := h.start(t, "drama", "k", "hi")
	request, _ = h.registry.claim(open.ID)
	encoded, _ = json.Marshal(request)
	if request.History != "" || strings.Contains(string(encoded), "history") {
		t.Fatalf("an omitted history must keep the default and stay off the wire: %s", encoded)
	}
	for _, value := range []string{"all", "None", " none", "last", "0"} {
		if _, err := h.registry.start("other", "O", aiTurnStartParams{ConversationKey: "k", Prompt: "hi", History: value}); err == nil || !strings.Contains(err.Error(), "history") {
			t.Fatalf("history %q accepted: %v", value, err)
		}
	}
}

func TestAITurnStartRejectsUnknownHistory(t *testing.T) {
	runtimeHost := newProcessRuntime(t.TempDir(), Manifest{ID: "p", APIVersion: api.ManifestAPIVersionV2, Capabilities: []api.Capability{api.CapabilityAI}}, nil, nil)
	var got *rpcError
	runtimeHost.handleAITurnRequest(rpcEnvelope{Method: "host.ai.turn.start", Params: json.RawMessage(`{"conversationKey":"k","prompt":"hi","history":"everything"}`)}, func(_ any, err *rpcError) { got = err })
	if got == nil || got.Code != -32602 || !strings.Contains(got.Message, "history") {
		t.Fatalf("an unknown history value must be rejected as invalid params: %+v", got)
	}
}

func TestAITurnHistoryFeatureIsAdvertisedToAIPlugins(t *testing.T) {
	if offer := offeredRuntimeFeatures(map[api.Capability]struct{}{api.CapabilityAI: {}}); offer["aiTurnHistory"] != 1 {
		t.Fatalf("ai plugins must be offered aiTurnHistory 1: %v", offer)
	}
	if _, ok := offeredRuntimeFeatures(map[api.Capability]struct{}{})["aiTurnHistory"]; ok {
		t.Fatal("aiTurnHistory offered without the ai capability")
	}
	withAI := (&processRuntime{capabilities: map[api.Capability]struct{}{api.CapabilityAI: {}}}).describeAPI().(map[string]any)
	if features := withAI["features"].(map[string]int); features["aiTurnHistory"] != 1 {
		t.Fatalf("host.api.describe must list aiTurnHistory for ai plugins: %v", features)
	}
}
