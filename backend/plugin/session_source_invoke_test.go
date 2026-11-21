package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mincode/app/plugins/api"
)

// sessionSourceManager is a running helper plugin contributing the session
// source "team" backed by its read action "run".
func sessionSourceManager(t *testing.T, capabilities ...api.Capability) (*pluginManager, *pluginRecord) {
	t.Helper()
	manager, record := startedContributionManager(t)
	record.mu.Lock()
	record.manifest.Capabilities = capabilities
	record.manifest.contributions = map[string]*resolvedContribution{sessionSourcePointKey: {Entries: []any{
		SessionSourceContribution{ID: "team", Title: "Team", Icon: "chat", Mark: "TE", Action: "run"},
	}}}
	record.mu.Unlock()
	return manager, record
}

// sessionProjectRoot is a project folder named "demo" and its canonical path.
func sessionProjectRoot(t *testing.T) (string, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "demo")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return root, canonical
}

func TestSessionListPayloadByCapability(t *testing.T) {
	root, canonical := sessionProjectRoot(t)
	list := func(manager *pluginManager, record *pluginRecord, requestID, scope, projectRoot string) (sessionListResponse, error) {
		return manager.sessionList(context.Background(), sessionListRequest{
			PluginID: record.id, SourceID: "team", RequestID: requestID, Scope: scope, ProjectRoot: projectRoot, Locale: "zh-CN",
		})
	}

	manager, record := sessionSourceManager(t, api.CapabilityWorkspaceRead)
	response, err := list(manager, record, "request-list-0001", "workspace", root)
	if err != nil || len(response.Sessions) != 1 || response.Sessions[0].ID != "s1" {
		t.Fatalf("workspace list = %+v, %v", response, err)
	}
	rootJSON, _ := json.Marshal(canonical)
	waitForLog(t, record, `helper session payload {"source":"team","request":"list","scope":"workspace","project":{"name":"demo","root":`+string(rootJSON)+`},"locale":"zh-CN"}`)

	if _, err := list(manager, record, "request-list-0002", "all", ""); err != nil {
		t.Fatalf("all list err = %v", err)
	}
	waitForLog(t, record, `helper session payload {"source":"team","request":"list","scope":"all","locale":"zh-CN"}`)
	assertNoContributionRequestsLeft(t)

	// Without workspace.read the plugin learns only the folder name.
	manager, record = sessionSourceManager(t)
	if _, err := list(manager, record, "request-list-0003", "workspace", root); err != nil {
		t.Fatalf("no capability list err = %v", err)
	}
	waitForLog(t, record, `helper session payload {"source":"team","request":"list","scope":"workspace","project":{"name":"demo"},"locale":"zh-CN"}`)
	for _, line := range pluginLogLines(record) {
		if strings.Contains(line, canonical) {
			t.Fatalf("project root leaked without workspace.read: %s", line)
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		scope, root, code string
	}{
		"workspace without root": {"workspace", "", contributionErrInvalidRequest},
		"home folder":            {"workspace", home, contributionErrForbidden},
		"unknown scope":          {"everything", "", contributionErrInvalidRequest},
		"all with root":          {"all", root, contributionErrInvalidRequest},
	}
	for name, test := range cases {
		if _, err := list(manager, record, "request-list-0004", test.scope, test.root); contributionErrorCode(err) != test.code {
			t.Errorf("%s: err = %v, want %s", name, err, test.code)
		}
	}
	assertNoContributionRequestsLeft(t)

	for _, args := range []string{
		`{"pluginId":"runtime.test","sourceId":"team","requestId":"request-list-0005","scope":"all","path":"/etc"}`,
		`nope`,
	} {
		if _, err := pluginHostSessionListHandler("", args); contributionErrorCode(err) != contributionErrInvalidRequest {
			t.Errorf("list handler %s: err = %v", args, err)
		}
		if _, err := pluginHostSessionMessagesHandler("", strings.Replace(args, `"scope":"all"`, `"sessionId":"s1"`, 1)); contributionErrorCode(err) != contributionErrInvalidRequest {
			t.Errorf("messages handler %s: err = %v", args, err)
		}
	}
}

func TestSessionMessagesPayload(t *testing.T) {
	manager, record := sessionSourceManager(t, api.CapabilityWorkspaceRead)
	response, err := manager.sessionMessages(context.Background(), sessionMessagesRequest{
		PluginID: record.id, SourceID: "team", RequestID: "request-msgs-0001", SessionID: "s1",
	})
	if err != nil || len(response.Messages) != 1 || response.Messages[0].Text != "hi" {
		t.Fatalf("messages = %+v, %v", response, err)
	}
	waitForLog(t, record, `helper session payload {"source":"team","request":"messages","session":"s1","locale":"en"}`)
	assertNoContributionRequestsLeft(t)
}

func sessionListJSON(items ...string) json.RawMessage {
	return json.RawMessage(`{"sessions":[` + strings.Join(items, ",") + `]}`)
}

func TestSessionListResultValidation(t *testing.T) {
	items := make([]string, sessionSourceSessionsMax+1)
	for index := range items {
		items[index] = fmt.Sprintf(`{"id":"s%d","title":"Session %d"}`, index, index)
	}
	response, err := parseSessionListResult(sessionListJSON(items...))
	if err != nil || len(response.Sessions) != sessionSourceSessionsMax || !response.Truncated || response.Dropped != 0 {
		t.Fatalf("201 sessions = %d %v %d, %v", len(response.Sessions), response.Truncated, response.Dropped, err)
	}
	if response.Sessions[0].ID != "s0" || response.Sessions[199].ID != "s199" {
		t.Fatalf("order = %s .. %s", response.Sessions[0].ID, response.Sessions[199].ID)
	}

	response, err = parseSessionListResult(sessionListJSON(
		`{"id":"b","title":" Beta ","detail":"two","model":"gpt","lastAt":1700000000000,"messageCount":3}`,
		`{"id":"a"}`,
		`{"id":"b","title":"Duplicate"}`,
		`{"id":" padded ","title":"x"}`,
		`{"id":"late","lastAt":253402300800000}`,
		`{"id":"neg","messageCount":-1}`,
		`{"id":"bidi","title":"evil‮txt"}`,
		`{"id":"soft","title":"a­b"}`,
		`{"id":"sep","detail":"a b"}`,
		`{"id":"multi","title":"a\nb"}`,
		`{"id":"frac","lastAt":1.5}`,
		`{"id":"extra","html":"<b>"}`,
		`{"id":"`+strings.Repeat("x", sessionSourceSessionIDMaxLength+1)+`"}`,
		`{"id":"longmodel","model":"`+strings.Repeat("m", sessionSourceModelMaxRunes+1)+`"}`,
		`{"id":"c","title":null,"messageCount":0,"lastAt":253402300799999}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(response.Sessions))
	for index, session := range response.Sessions {
		ids[index] = session.ID
	}
	if strings.Join(ids, ",") != "b,a,c" || response.Dropped != 12 || response.Truncated {
		t.Fatalf("sessions = %v dropped %d truncated %v", ids, response.Dropped, response.Truncated)
	}
	first := response.Sessions[0]
	if first.Title != "Beta" || first.Detail != "two" || first.Model != "gpt" || first.LastAt == nil || *first.LastAt != 1700000000000 ||
		first.MessageCount == nil || *first.MessageCount != 3 {
		t.Fatalf("first = %+v", first)
	}
	if last := response.Sessions[2]; last.Title != "" || last.LastAt == nil || *last.LastAt != sessionSourceTimestampMax ||
		last.MessageCount == nil || *last.MessageCount != 0 {
		t.Fatalf("last = %+v", last)
	}
	encoded, _ := json.Marshal(response.Sessions[1])
	if string(encoded) != `{"id":"a","title":""}` {
		t.Fatalf("minimal session = %s", encoded)
	}

	for _, bad := range []string{`{"sessions":null}`, `{}`, `[]`, `{"sessions":[],"more":1}`} {
		if _, err := parseSessionListResult(json.RawMessage(bad)); err == nil {
			t.Errorf("%s must be rejected", bad)
		}
	}
}

func sessionMessagesJSON(items ...string) json.RawMessage {
	return json.RawMessage(`{"messages":[` + strings.Join(items, ",") + `]}`)
}

func TestSessionMessagesTailAndPlainText(t *testing.T) {
	items := make([]string, 250)
	for index := range items {
		items[index] = fmt.Sprintf(`{"role":"user","text":"m%d"}`, index+1)
	}
	response, err := parseSessionMessagesResult(sessionMessagesJSON(items...))
	if err != nil || len(response.Messages) != sessionSourceMessagesMax || !response.Truncated || response.Dropped != 0 {
		t.Fatalf("250 messages = %d %v %d, %v", len(response.Messages), response.Truncated, response.Dropped, err)
	}
	if response.Messages[0].Text != "m51" || response.Messages[199].Text != "m250" {
		t.Fatalf("tail = %s .. %s", response.Messages[0].Text, response.Messages[199].Text)
	}

	long := strings.Repeat("é", sessionSourceMessageTextMaxRunes+1)
	markup := `<img src=x onerror=alert(1)> [x](file:///etc/passwd)`
	response, err = parseSessionMessagesResult(sessionMessagesJSON(
		`{"role":"admin","text":"x"}`,
		`{"role":"assistant","text":"\u001b[31mred\u001b[0m\r\nline","timestamp":1700000000000,"model":"gpt-5"}`,
		`{"role":"tool","text":`+mustJSONString(markup)+`}`,
		`{"role":"system","text":"`+long+`"}`,
		`{"role":"user","text":"zero​width­ tab\tend"}`,
		`{"role":"user","text":"x","timestamp":-1}`,
		`{"role":"user","text":"x","model":"a\nb"}`,
		`{"role":"user"}`,
		`{"role":"user","text":"x","extra":true}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Messages) != 4 || response.Dropped != 5 || response.Truncated {
		t.Fatalf("messages = %+v dropped %d", response.Messages, response.Dropped)
	}
	if red := response.Messages[0]; red.Role != "assistant" || red.Text != "red\nline" || red.Model != "gpt-5" ||
		red.Timestamp == nil || *red.Timestamp != 1700000000000 || red.Truncated {
		t.Fatalf("ansi message = %+v", red)
	}
	if response.Messages[1].Text != markup {
		t.Fatalf("markup must stay literal text: %q", response.Messages[1].Text)
	}
	if cut := response.Messages[2]; !cut.Truncated || len([]rune(cut.Text)) != sessionSourceMessageTextMaxRunes {
		t.Fatalf("long message = %d runes truncated %v", len([]rune(cut.Text)), cut.Truncated)
	}
	if clean := response.Messages[3].Text; clean != "zerowidthtab\tend" {
		t.Fatalf("format characters must be removed: %q", clean)
	}

	for _, bad := range []string{`{"messages":null}`, `{}`, `{"messages":[],"sessions":[]}`} {
		if _, err := parseSessionMessagesResult(json.RawMessage(bad)); err == nil {
			t.Errorf("%s must be rejected", bad)
		}
	}

	manager, record := sessionSourceManager(t)
	for name, sessionID := range map[string]string{
		"129 bytes": strings.Repeat("s", sessionSourceSessionIDMaxLength+1),
		"empty":     "",
		"padded":    " s1",
		"bidi":      "s‮1",
		"format":    "s­1",
	} {
		_, err := manager.sessionMessages(context.Background(), sessionMessagesRequest{
			PluginID: record.id, SourceID: "team", RequestID: "request-msgs-0002", SessionID: sessionID,
		})
		if contributionErrorCode(err) != contributionErrInvalidRequest {
			t.Errorf("%s session id: err = %v", name, err)
		}
	}
	assertNoContributionRequestsLeft(t)
}

func mustJSONString(value string) string {
	encoded, err := encodeContributionJSON(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func TestSessionSourceLookupRechecked(t *testing.T) {
	manager, record := sessionSourceManager(t)
	source := SessionSourceContribution{ID: "team", Title: "Team", Icon: "chat", Mark: "TE", Action: "run"}
	validate := sessionSourceUnchanged(source)
	check := func(name, want string) {
		t.Helper()
		record.mu.Lock()
		err := validate(record)
		record.mu.Unlock()
		if contributionErrorCode(err) != want {
			t.Errorf("%s: err = %v, want %q", name, err, want)
		}
	}
	check("unchanged", "")

	// A new generation that dropped the entry while the request was waiting.
	record.mu.Lock()
	record.manifest.contributions = map[string]*resolvedContribution{sessionSourcePointKey: {Entries: []any{}}}
	record.mu.Unlock()
	check("removed", contributionErrUnavailable)

	// A new generation pointing the entry at another action.
	record.mu.Lock()
	record.manifest.contributions = map[string]*resolvedContribution{sessionSourcePointKey: {Entries: []any{
		SessionSourceContribution{ID: "team", Title: "Team", Icon: "chat", Mark: "TE", Action: "other"},
	}}}
	record.mu.Unlock()
	check("changed action", contributionErrUnavailable)

	// Restored: an entry ID the plugin does not contribute is not found.
	record.mu.Lock()
	record.manifest.contributions = map[string]*resolvedContribution{sessionSourcePointKey: {Entries: []any{source}}}
	record.mu.Unlock()
	if _, err := manager.sessionList(context.Background(), sessionListRequest{
		PluginID: record.id, SourceID: "foreign", RequestID: "request-look-0001", Scope: "all",
	}); contributionErrorCode(err) != contributionErrNotFound {
		t.Errorf("foreign source: err = %v", err)
	}
	if _, err := manager.sessionMessages(context.Background(), sessionMessagesRequest{
		PluginID: record.id, SourceID: "foreign", RequestID: "request-look-0002", SessionID: "s1",
	}); contributionErrorCode(err) != contributionErrNotFound {
		t.Errorf("foreign source messages: err = %v", err)
	}

	// A disabled plugin is never called.
	if err := manager.SetEnabled(context.Background(), record.id, false); err != nil {
		t.Fatal(err)
	}
	record.mu.Lock()
	record.manifest.contributions = map[string]*resolvedContribution{sessionSourcePointKey: {Entries: []any{source}}}
	record.mu.Unlock()
	if _, err := manager.sessionList(context.Background(), sessionListRequest{
		PluginID: record.id, SourceID: "team", RequestID: "request-look-0003", Scope: "all",
	}); contributionErrorCode(err) != contributionErrUnavailable {
		t.Errorf("disabled plugin: err = %v", err)
	}
	for _, line := range pluginLogLines(record) {
		if strings.Contains(line, "helper session payload") {
			t.Fatalf("plugin was called: %s", line)
		}
	}
	assertNoContributionRequestsLeft(t)
}
