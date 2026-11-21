package plugin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func searchProviderTestContext() *pointContext {
	return &pointContext{
		manifest: &Manifest{ID: "acme.search", APIVersion: 2},
		spec:     searchProviderSpec,
		actions: map[string]ManifestUIAction{
			"search": {ID: "search", Risk: "read", MaxPayloadBytes: 2048},
			"write":  {ID: "write", Risk: "write", MaxPayloadBytes: 1024},
		},
	}
}

func TestSearchProviderDecode(t *testing.T) {
	ctx := searchProviderTestContext()
	value, id, err := decodeSearchProviderEntry(ctx, 0, "", json.RawMessage(`{"id":"issues","prefix":"jira","title":" Jira ","titleLocalized":{"zh":"工单"},"placeholder":"Key","action":"search"}`))
	if err != nil || id != "issues" {
		t.Fatalf("decode = %v, %q", err, id)
	}
	provider := value.(SearchProviderContribution)
	if provider.Title != "Jira" || provider.Prefix != "jira" || provider.MinQueryLength != 1 || provider.Placeholder != "Key" || provider.TitleLocalized["zh"] != "工单" {
		t.Fatalf("provider = %+v", provider)
	}
	value, _, err = decodeSearchProviderEntry(ctx, 0, "", json.RawMessage(`{"id":"all","prefix":"k8s","title":"x","action":"search","minQueryLength":0}`))
	if err != nil || value.(SearchProviderContribution).MinQueryLength != 0 {
		t.Fatalf("minQueryLength 0 = %+v, %v", value, err)
	}
	cases := map[string]string{
		`{"id":"a","prefix":"http","title":"x","action":"search"}`:                      "reserved",
		`{"id":"a","prefix":"run","title":"x","action":"search"}`:                       "reserved",
		`{"id":"a","prefix":"J","title":"x","action":"search"}`:                         "must match",
		`{"id":"a","prefix":"j","title":"x","action":"search"}`:                         "must match",
		`{"id":"a","prefix":"a23456789012345678","title":"x","action":"search"}`:        "must match",
		`{"id":"a","prefix":"jira","title":"x","action":"write"}`:                       `risk "read"`,
		`{"id":"a","prefix":"jira","title":"x","action":"missing"}`:                     "contributes.actions",
		`{"id":"a","prefix":"jira","title":"","action":"search"}`:                       "title is required",
		`{"id":"a","prefix":"jira","title":"x","action":"search","minQueryLength":9}`:   "minQueryLength",
		`{"id":"a","prefix":"jira","title":"x","action":"search","minQueryLength":-1}`:  "minQueryLength",
		`{"id":"a","prefix":"jira","title":"x","action":"search","extra":1}`:            "unknown field",
		`{"id":"A","prefix":"jira","title":"x","action":"search"}`:                      "must match",
		`{"id":"a","prefix":"jira","title":"x","placeholder":"a\nb","action":"search"}`: "placeholder",
	}
	for raw, want := range cases {
		if _, _, err := decodeSearchProviderEntry(ctx, 0, "", json.RawMessage(raw)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", raw, err, want)
		}
	}
}

func TestSearchProviderFinalizeRejectsDuplicatePrefix(t *testing.T) {
	entries := []contributionEntry{
		{Index: 0, ID: "a", Value: SearchProviderContribution{ID: "a", Prefix: "jira"}},
		{Index: 1, ID: "b", Value: SearchProviderContribution{ID: "b", Prefix: "jira"}},
		{Index: 2, ID: "c", Value: SearchProviderContribution{ID: "c", Prefix: "wiki"}},
	}
	kept, issues := finalizeSearchProviderEntries(nil, entries, nil)
	if len(kept) != 2 || kept[1].ID != "c" || len(issues) != 1 || issues[0].ID != "b" || !strings.Contains(issues[0].Message, "already used") {
		t.Fatalf("kept = %+v, issues = %+v", kept, issues)
	}
}

func TestSearchProvidersExpandPermission(t *testing.T) {
	base := SearchProviderContribution{ID: "a", Prefix: "jira", Action: "search"}
	resolved := func(providers ...SearchProviderContribution) *resolvedContribution {
		entries := make([]any, len(providers))
		for index, provider := range providers {
			entries[index] = provider
		}
		return &resolvedContribution{Entries: entries}
	}
	retitled, otherAction := base, base
	retitled.Title = "New"
	otherAction.Action = "search2"
	cases := []struct {
		name               string
		current, candidate *resolvedContribution
		want               bool
	}{
		{"unchanged", resolved(base), resolved(base), false},
		{"retitled", resolved(base), resolved(retitled), false},
		{"removed", resolved(base), resolved(), false},
		{"changed action", resolved(base), resolved(otherAction), true},
		{"new provider", nil, resolved(base), true},
		{"no candidate", resolved(base), nil, false},
	}
	for _, test := range cases {
		if got := searchProvidersExpandPermission(test.current, test.candidate); got != test.want {
			t.Errorf("%s = %v, want %v", test.name, got, test.want)
		}
	}
}

func searchTestScope() searchTargetScope {
	return searchTargetScope{
		commands: map[string]ManifestCommand{
			"open":    {ID: "open", Action: "search", Risk: "read", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"key": map[string]any{"type": "string"}, "mode": map[string]any{"type": "string", "enum": []any{"a", "b"}}}, "required": []any{"key"}}},
			"plain":   {ID: "plain", Action: "search", Risk: "read"},
			"delete":  {ID: "delete", Action: "search", Risk: "dangerous"},
			"viaRisk": {ID: "viaRisk", Action: "write", Risk: "read"},
			"toView":  {ID: "toView", Target: &ManifestCommandTarget{Type: "view", ViewID: "main"}},
		},
		views:   map[string]ManifestView{"main": {ID: "main", Title: "Main"}},
		actions: map[string]ManifestUIAction{"search": {ID: "search", Risk: "read"}, "write": {ID: "write", Risk: "write"}},
	}
}

func TestParseSearchResultTargets(t *testing.T) {
	scope := searchTestScope()
	raw := `{"items":[
		{"id":"v1","title":" View ","detail":"d","target":{"type":"view","view":"main","args":{"key":"A-1","n":2,"b":true}}},
		{"id":"c1","title":"Cmd","target":{"type":"command","command":"open","args":{"key":"A-1","mode":"a"}}},
		{"id":"c2","title":"Plain","target":{"type":"command","command":"plain"}},
		{"id":"u1","title":"Url","target":{"type":"url","url":"HTTPS://example.com/a?b=1"}},
		{"id":"x1","title":"Foreign view","target":{"type":"view","view":"other"}},
		{"id":"x2","title":"Write","target":{"type":"command","command":"delete"}},
		{"id":"x3","title":"Action risk","target":{"type":"command","command":"viaRisk"}},
		{"id":"x4","title":"Args without schema","target":{"type":"command","command":"plain","args":{"a":1}}},
		{"id":"x5","title":"Bad enum","target":{"type":"command","command":"open","args":{"key":"x","mode":"z"}}},
		{"id":"x6","title":"Missing required","target":{"type":"command","command":"open","args":{"mode":"a"}}},
		{"id":"x7","title":"Unknown arg","target":{"type":"command","command":"open","args":{"key":"x","other":1}}},
		{"id":"x8","title":"Userinfo","target":{"type":"url","url":"https://user:pw@example.com/"}},
		{"id":"x9","title":"Script","target":{"type":"url","url":"javascript:alert(1)"}},
		{"id":"y1","title":"File","target":{"type":"url","url":"file:///etc/passwd"}},
		{"id":"y2","title":"No host","target":{"type":"url","url":"https:///path"}},
		{"id":"y3","title":"Nested args","target":{"type":"view","view":"main","args":{"a":{"b":1}}}},
		{"id":"y4","title":"View command","target":{"type":"command","command":"toView"}},
		{"id":"y5","title":"","target":{"type":"view","view":"main"}},
		{"id":"bad id!","title":"x","target":{"type":"view","view":"main"}},
		{"id":"v1","title":"Duplicate","target":{"type":"view","view":"main"}},
		{"id":"y6","title":"Extra","target":{"type":"view","view":"main","url":"https://x.test"}},
		{"id":"y7","title":"Html","extra":1,"target":{"type":"view","view":"main"}}
	]}`
	response, err := parseSearchResult(json.RawMessage(raw), scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 4 || response.Dropped != 18 || response.Truncated {
		t.Fatalf("response = %+v", response)
	}
	if item := response.Items[0]; item.Title != "View" || item.Target.Args["n"] != float64(2) {
		t.Fatalf("view item = %+v", item)
	}
	if item := response.Items[3]; item.Target.URL != "https://example.com/a?b=1" {
		t.Fatalf("url item = %+v", item)
	}
	for _, bad := range []string{`{"items":null}`, `[]`, `{"items":[],"more":1}`, `{"ok":true}`} {
		if _, err := parseSearchResult(json.RawMessage(bad), scope); err == nil {
			t.Errorf("%s must be rejected", bad)
		}
	}
}

// Without a file-enabled query (no scope root), the contract's new file
// type and column stay unusable: legacy results are parsed as before.
func TestParseSearchResultLegacyRejectsFileTargets(t *testing.T) {
	response, err := parseSearchResult(json.RawMessage(`{"items":[
		{"id":"f1","title":"File","target":{"type":"file","path":"src/main.go","line":1}},
		{"id":"v1","title":"View","target":{"type":"view","view":"main"}},
		{"id":"v2","title":"Column","target":{"type":"view","view":"main","column":1}}
	]}`), searchTestScope())
	if err != nil || len(response.Items) != 1 || response.Items[0].ID != "v1" || response.Dropped != 2 || response.FileScope != nil || response.ProviderRuntime != nil {
		t.Fatalf("legacy parse = %+v, %v", response, err)
	}
}

func TestParseSearchResultTruncatesAndBoundsArgs(t *testing.T) {
	scope := searchTestScope()
	items := make([]string, searchProviderItemsMax+5)
	for index := range items {
		items[index] = `{"id":"i` + strings.Repeat("x", index%3) + string(rune('a'+index%26)) + `-` + string(rune('0'+index/26)) + `","title":"t","target":{"type":"view","view":"main"}}`
	}
	response, err := parseSearchResult(json.RawMessage(`{"items":[`+strings.Join(items, ",")+`]}`), scope)
	if err != nil || !response.Truncated || len(response.Items)+response.Dropped != searchProviderItemsMax {
		t.Fatalf("truncated = %+v, %v", response, err)
	}
	many := map[string]any{}
	for index := 0; index <= searchProviderArgsMaxProperties; index++ {
		many["k"+string(rune('a'+index))] = "v"
	}
	if err := validateContributionScalarArgs(many, searchProviderArgsMaxProperties, searchProviderArgsMaxBytes); err == nil {
		t.Fatal("too many args must be rejected")
	}
	if err := validateContributionScalarArgs(map[string]any{"a": strings.Repeat("x", searchProviderArgsMaxBytes)}, searchProviderArgsMaxProperties, searchProviderArgsMaxBytes); err == nil {
		t.Fatal("oversized args must be rejected")
	}
}

func TestNormalizeSearchRequest(t *testing.T) {
	valid := func() searchRequest {
		return searchRequest{PluginID: "runtime.test", ProviderID: "issues", RequestID: "request-0001", Query: "abc"}
	}
	request := valid()
	if err := normalizeSearchRequest(&request); err != nil || request.Locale != "en" {
		t.Fatalf("valid = %v, %+v", err, request)
	}
	cases := map[string]func(*searchRequest){
		"bad plugin":   func(r *searchRequest) { r.PluginID = "Bad Id" },
		"bad provider": func(r *searchRequest) { r.ProviderID = "X" },
		"newline":      func(r *searchRequest) { r.Query = "a\nb" },
		"bidi":         func(r *searchRequest) { r.Query = "a‮b" },
		"too long":     func(r *searchRequest) { r.Query = strings.Repeat("é", searchProviderQueryMaxRunes+1) },
		"bad locale":   func(r *searchRequest) { r.Locale = "en_US!" },
	}
	for name, mutate := range cases {
		request := valid()
		mutate(&request)
		if err := normalizeSearchRequest(&request); contributionErrorCode(err) != contributionErrInvalidRequest {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func searchInvokeManager(t *testing.T, provider SearchProviderContribution) (*pluginManager, *pluginRecord) {
	t.Helper()
	manager, record := contributionInvokeManager(t)
	record.mu.Lock()
	record.manifest.contributions = map[string]*resolvedContribution{searchProviderPointKey: {Entries: []any{provider}}}
	record.mu.Unlock()
	return manager, record
}

func TestPluginSearchGating(t *testing.T) {
	manager, record := searchInvokeManager(t, SearchProviderContribution{ID: "issues", Prefix: "jira", Title: "Issues", Action: "run", MinQueryLength: 2})
	valid := func() searchRequest {
		return searchRequest{PluginID: record.id, ProviderID: "issues", RequestID: "request-0001", Query: "abc"}
	}
	// The helper process answers {"ok":true}: dispatched, then rejected by
	// result validation.
	if _, err := manager.search(context.Background(), valid()); contributionErrorCode(err) != contributionErrInvalidResult {
		t.Fatalf("dispatch err = %v", err)
	}
	assertNoContributionRequestsLeft(t)
	cases := map[string]struct {
		mutate func(*searchRequest)
		code   string
	}{
		"unknown plugin":   {func(r *searchRequest) { r.PluginID = "missing.plugin" }, contributionErrNotFound},
		"unknown provider": {func(r *searchRequest) { r.ProviderID = "other" }, contributionErrNotFound},
		"too short":        {func(r *searchRequest) { r.Query = "a" }, contributionErrInvalidRequest},
		"bad request id":   {func(r *searchRequest) { r.RequestID = "short" }, contributionErrInvalidRequest},
	}
	for name, test := range cases {
		request := valid()
		test.mutate(&request)
		if _, err := manager.search(context.Background(), request); contributionErrorCode(err) != test.code {
			t.Errorf("%s: err = %v, want %s", name, err, test.code)
		}
	}
	slot := record.id + "\x00" + searchProviderPointKey
	contributionRequests.mu.Lock()
	contributionRequests.slots[slot] = searchProviderConcurrency
	contributionRequests.mu.Unlock()
	_, err := manager.search(context.Background(), valid())
	contributionRequests.mu.Lock()
	delete(contributionRequests.slots, slot)
	contributionRequests.mu.Unlock()
	if contributionErrorCode(err) != contributionErrBusy {
		t.Fatalf("busy err = %v", err)
	}
	// A cancelled context reports cancellation and leaves no request behind.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.search(ctx, valid()); err == nil {
		t.Fatal("a cancelled search must fail")
	}
	if err := manager.SetEnabled(context.Background(), record.id, false); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.search(context.Background(), valid()); contributionErrorCode(err) != contributionErrUnavailable {
		t.Fatalf("disabled err = %v", err)
	}
	assertNoContributionRequestsLeft(t)
}

func TestPluginHostSearchHandlerRejectsBadArguments(t *testing.T) {
	for _, args := range []string{`nope`, `{"pluginId":"a","extra":1}`} {
		if _, err := pluginHostSearchHandler("", args); contributionErrorCode(err) != contributionErrInvalidRequest {
			t.Errorf("%s: err = %v", args, err)
		}
	}
}
