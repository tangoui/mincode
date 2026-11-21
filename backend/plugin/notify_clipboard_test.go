package plugin

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"mincode/app/plugins/api"
)

func TestHostNotifyValidatesAndRateLimits(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	var events []pluginNotification
	limiter := &notifyLimiter{sent: map[string][]time.Time{}, now: func() time.Time { return now }, limit: 3,
		emit: func(name string, data any) {
			if name != pluginNotifyEvent {
				t.Fatalf("event %s", name)
			}
			events = append(events, data.(pluginNotification))
		}}
	post := func(params string) *rpcError {
		_, err := limiter.post("focus-timer", "Focus Timer", nil, json.RawMessage(params))
		return err
	}
	if err := post(`{"title":"  专注结束\u0007 ","body":"休息 5 分钟","level":"success","system":true,"viewId":"main"}`); err != nil {
		t.Fatal(err)
	}
	got := events[0]
	if got.Title != "专注结束" || got.Body != "休息 5 分钟" || got.Level != "success" || !got.System || got.ViewID != "main" || got.PluginName != "Focus Timer" {
		t.Fatalf("notification %+v", got)
	}
	for name, params := range map[string]string{
		"missing title": `{"body":"x"}`, "bad level": `{"title":"x","level":"critical"}`, "bad view": `{"title":"x","viewId":"../x"}`, "not json": `x`,
	} {
		if err := post(params); err == nil || err.Code != -32602 {
			t.Fatalf("%s: %v", name, err)
		}
	}
	long := strings.Repeat("长", 900)
	if err := post(`{"title":"` + long + `","body":"` + long + `"}`); err != nil {
		t.Fatal(err)
	}
	if last := events[len(events)-1]; len([]rune(last.Title)) != notifyMaxTitleRunes || len([]rune(last.Body)) != notifyMaxBodyRunes {
		t.Fatalf("text must be bounded: %d %d", len([]rune(last.Title)), len([]rune(last.Body)))
	}
	post(`{"title":"third"}`)
	if err := post(`{"title":"fourth"}`); err == nil || err.Code != -32005 {
		t.Fatalf("rate limit not enforced: %v", err)
	}
	if _, err := limiter.post("other-plugin", "Other", nil, json.RawMessage(`{"title":"x"}`)); err != nil {
		t.Fatalf("limits are per plugin: %v", err)
	}
	now = now.Add(time.Minute + time.Second)
	if err := post(`{"title":"later"}`); err != nil {
		t.Fatalf("limit must reset after a minute: %v", err)
	}
}

func TestNotifyActionsValidation(t *testing.T) {
	commands := map[string]struct{}{"sync.retry": {}, "sync.openLog": {}, "view.open": {}}
	now := time.Unix(1_800_000_000, 0)
	var events []pluginNotification
	limiter := &notifyLimiter{sent: map[string][]time.Time{}, now: func() time.Time { return now }, limit: 100,
		emit: func(_ string, data any) { events = append(events, data.(pluginNotification)) }}
	post := func(actions string) (any, *rpcError) {
		return limiter.post("p", "P", commands, json.RawMessage(`{"title":"t","actions":`+actions+`}`))
	}
	result, err := post(`[{"id":"retry","title":"重试","command":"sync.retry"},{"id":"log","title":"查看日志","command":"sync.openLog"}]`)
	if err != nil || len(events) != 1 || len(events[0].Actions) != 2 {
		t.Fatalf("two actions: %v %+v", err, events)
	}
	if got := events[0].Actions[1]; got != (pluginNotificationAction{ID: "log", Title: "查看日志", Command: "sync.openLog"}) {
		t.Fatalf("action %+v", got)
	}
	if raw, _ := json.Marshal(result); string(raw) != `{"actions":2,"delivered":true}` {
		t.Fatalf("result %s", raw)
	}
	for _, empty := range []string{`null`, `[]`} {
		result, err := post(empty)
		raw, _ := json.Marshal(result)
		if err != nil || len(events[len(events)-1].Actions) != 0 || string(raw) != `{"actions":0,"delivered":true}` {
			t.Fatalf("%s: %v %s %+v", empty, err, raw, events[len(events)-1])
		}
	}
	action := func(id, title, command string) string {
		raw, _ := json.Marshal(pluginNotificationAction{ID: id, Title: title, Command: command})
		return string(raw)
	}
	ok := action("a", "A", "view.open")
	for name, tc := range map[string]struct{ actions, message string }{
		"three":           {`[` + ok + `,` + action("b", "B", "sync.retry") + `,` + action("c", "C", "sync.openLog") + `]`, "at most 2 notification actions"},
		"foreign command": {`[` + action("a", "A", "other.cmd") + `]`, `notification action 1 command "other.cmd" is not one of this plugin's commands`},
		"newline title":   {`[` + action("a", "a\nb", "view.open") + `]`, ""},
		"tab title":       {`[` + action("a", "a\tb", "view.open") + `]`, ""},
		"bidi title":      {`[` + action("a", "a‮b", "view.open") + `]`, ""},
		"separator title": {`[` + action("a", "a b", "view.open") + `]`, ""},
		"long title":      {`[` + action("a", strings.Repeat("x", 25), "view.open") + `]`, ""},
		"blank title":     {`[` + action("a", "  ", "view.open") + `]`, ""},
		"bad id":          {`[` + action("a b", "A", "view.open") + `]`, ""},
		"long id":         {`[` + action(strings.Repeat("a", 33), "A", "view.open") + `]`, ""},
		"duplicate id":    {`[` + ok + `,` + action("a", "B", "sync.retry") + `]`, ""},
		"object":          {`{}`, ""},
		"args":            {`[{"id":"a","title":"A","command":"view.open","args":{}}]`, ""},
		"missing command": {`[{"id":"a","title":"A"}]`, ""},
		"element null":    {`[null]`, ""},
		"second invalid":  {`[` + ok + `,` + action("b", "B", "nope") + `]`, "notification action 2"},
	} {
		before := len(events)
		_, err := post(tc.actions)
		if err == nil || err.Code != -32602 || !strings.Contains(err.Message, tc.message) {
			t.Fatalf("%s: %+v", name, err)
		}
		if len(events) != before {
			t.Fatalf("%s: notification emitted", name)
		}
	}
	if sent := len(limiter.sent["p"]); sent != 3 {
		t.Fatalf("rejected calls must not consume the rate limit: %d", sent)
	}
	if _, err := limiter.post("p", "P", nil, json.RawMessage(`{"title":"t","actions":[`+ok+`]}`)); err == nil || err.Code != -32602 {
		t.Fatalf("a plugin without commands has no valid action: %v", err)
	}
	exact := strings.Repeat("长", notifyActionTitleMaxRunes)
	if _, err := post(`[` + action("a", " "+exact+" ", "view.open") + `]`); err != nil || events[len(events)-1].Actions[0].Title != exact {
		t.Fatalf("24-character title is allowed and trimmed: %v", err)
	}
}

func TestNotifyWithoutActionsUnchanged(t *testing.T) {
	var emitted any
	limiter := &notifyLimiter{sent: map[string][]time.Time{}, now: time.Now, limit: 10, emit: func(_ string, data any) { emitted = data }}
	if _, err := limiter.post("p", "P", map[string]struct{}{"c": {}}, json.RawMessage(`{"title":"t"}`)); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(emitted)
	if string(raw) != `{"pluginId":"p","pluginName":"P","title":"t","level":"info"}` {
		t.Fatalf("event changed: %s", raw)
	}
}

func TestHostNoticesNeverCarryActions(t *testing.T) {
	record := shortcutPluginRecord("acme.shot", shortcutEntry("open", "CmdOrCtrl+Shift+O", "view", "main"))
	h := newShortcutHarness(t, shortcutManager(t, record), "mac", nil)
	h.registry.reject("acme.shot", "Shot", "open", "failed")
	_, notices, _, _ := h.snapshot()
	if len(notices) != 1 || notices[0].Code == "" {
		t.Fatalf("notices %+v", notices)
	}
	raw, _ := json.Marshal(notices[0])
	if strings.Contains(string(raw), `"actions"`) {
		t.Fatalf("host notice carries actions: %s", raw)
	}
}

func TestHostClipboardReadAndWrite(t *testing.T) {
	previous := clipboardDispatch
	t.Cleanup(func() { clipboardDispatch = previous })
	state := `{"changeCount":7,"concealed":false}`
	text := `"hello 世界"`
	reads, written := 0, ""
	clipboardDispatch = func(cmd, args string) (string, error) {
		switch cmd {
		case clipboardStateCommand:
			return state, nil
		case clipboardReadCommand:
			reads++
			return text, nil
		case clipboardWriteCommand:
			written = args
			return "null", nil
		}
		return "", errors.New("unexpected " + cmd)
	}
	read := func(params string) map[string]any {
		result, err := hostClipboardRead(nil, json.RawMessage(params))
		if err != nil {
			t.Fatal(err)
		}
		return result.(map[string]any)
	}
	if got := read(`{}`); got["changed"] != true || got["text"] != "hello 世界" || got["seq"] != int64(7) {
		t.Fatalf("first read %v", got)
	}
	if got := read(`{"since":7}`); got["changed"] != false || reads != 1 {
		t.Fatalf("unchanged clipboard must not be read again: %v %d", got, reads)
	}
	state = `{"changeCount":8,"concealed":true}`
	if got := read(`{"since":7}`); got["changed"] != true || got["concealed"] != true || got["text"] != "" || reads != 1 {
		t.Fatalf("concealed content leaked: %v", got)
	}
	state = `{"changeCount":9,"concealed":false}`
	big, _ := json.Marshal(strings.Repeat("a", clipboardMaxTextBytes) + "尾")
	text = string(big)
	if got := read(`{"since":8}`); got["truncated"] != true || len(got["text"].(string)) != clipboardMaxTextBytes {
		t.Fatalf("large text must be truncated: %v", got["truncated"])
	}
	if _, err := hostClipboardWrite(nil, json.RawMessage(`{"text":"复制"}`)); err != nil || written != `{"text":"复制"}` {
		t.Fatalf("write: %v %s", err, written)
	}
	if _, err := hostClipboardWrite(nil, json.RawMessage(`{}`)); err == nil {
		t.Fatal("missing text accepted")
	}
}

func TestNotificationAndClipboardCapabilities(t *testing.T) {
	for _, capability := range []api.Capability{api.CapabilityNotifications, api.CapabilityClipboard} {
		v2 := Manifest{APIVersion: api.ManifestAPIVersionV2, Capabilities: []api.Capability{capability}}
		if err := validateCapabilities(&v2); err != nil {
			t.Fatal(err)
		}
		if err := validateMarketplaceCapabilities([]api.Capability{capability}); err != nil {
			t.Fatalf("marketplace must accept %s: %v", capability, err)
		}
	}
	runtimeHost := newProcessRuntime(t.TempDir(), Manifest{ID: "p", APIVersion: api.ManifestAPIVersionV2}, nil, nil)
	var got *rpcError
	runtimeHost.serveRegisteredHostMethod(rpcEnvelope{Method: "host.clipboard.read", Params: json.RawMessage(`{}`)}, func(_ any, err *rpcError) { got = err })
	if got == nil || got.Code != -32001 {
		t.Fatalf("clipboard must require its capability: %+v", got)
	}
}
