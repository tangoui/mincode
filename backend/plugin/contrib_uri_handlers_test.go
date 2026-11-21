package plugin

import (
	"context"
	"encoding/json"
	"math"
	"net/url"
	"strings"
	"testing"
)

func uriHandlerTestContext(pluginID string) *pointContext {
	return &pointContext{
		manifest: &Manifest{ID: pluginID, APIVersion: 2},
		spec:     uriHandlerSpec,
		views:    map[string]ManifestView{"main": {ID: "main", Title: "Main"}},
		commands: map[string]ManifestCommand{
			"refresh": {ID: "refresh", Action: "read", Risk: "read"},
			"open":    {ID: "open", Action: "read", Risk: "read", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"key": map[string]any{"type": "string"}}}},
			"purge":   {ID: "purge", Action: "read", Risk: "dangerous"},
			"sneaky":  {ID: "sneaky", Action: "write", Risk: "read"},
			"toView":  {ID: "toView", Target: &ManifestCommandTarget{Type: "view", ViewID: "main"}},
		},
		actions: map[string]ManifestUIAction{"read": {ID: "read", Risk: "read"}, "write": {ID: "write", Risk: "write"}},
	}
}

func TestUriHandlerDecode(t *testing.T) {
	ctx := uriHandlerTestContext("jira-tools")
	value, id, err := decodeUriHandlerEntry(ctx, 0, "", json.RawMessage(`{"id":"issue","path":"/issues/{key}","title":"Open issue",
		"target":{"type":"view","view":"main"},
		"query":{"type":"object","properties":{"focus":{"type":"string","enum":["comments","history"]},"note":{"type":"string","maxLength":20}}}}`))
	if err != nil || id != "issue" {
		t.Fatalf("decode = %v, %q", err, id)
	}
	handler := value.(UriHandlerContribution)
	if handler.Target.View != "main" || handler.Query["additionalProperties"] != false || len(handler.segments) != 2 || handler.segments[1].Param != "key" {
		t.Fatalf("handler = %+v", handler)
	}
	if _, _, err := decodeUriHandlerEntry(ctx, 0, "", json.RawMessage(`{"id":"refresh","path":"/refresh","target":{"type":"command","command":"refresh"}}`)); err != nil {
		t.Fatalf("command handler = %v", err)
	}
	cases := map[string]string{
		`{"id":"a","path":"/x","target":{"type":"view","view":"other"}}`:                                                                                           "not one of this plugin's contributes.views",
		`{"id":"a","path":"/x","target":{"type":"command","command":"purge"}}`:                                                                                     `effective risk "read"`,
		`{"id":"a","path":"/x","target":{"type":"command","command":"sneaky"}}`:                                                                                    `effective risk "read"`,
		`{"id":"a","path":"/x","target":{"type":"command","command":"toView"}}`:                                                                                    "action command",
		`{"id":"a","path":"/x","target":{"type":"command","command":"missing"}}`:                                                                                   "contributes.commands",
		`{"id":"a","path":"/x","target":{"type":"command","command":"open"},"query":{"type":"object","properties":{"k":{"type":"string"}}}}`:                       "remove query",
		`{"id":"a","path":"/x/{key}","target":{"type":"command","command":"open"}}`:                                                                                "must not contain {key}",
		`{"id":"a","path":"x","target":{"type":"view","view":"main"}}`:                                                                                             "must start with /",
		`{"id":"a","path":"/","target":{"type":"view","view":"main"}}`:                                                                                             "must start with /",
		`{"id":"a","path":"/Issues","target":{"type":"view","view":"main"}}`:                                                                                       "path segment",
		`{"id":"a","path":"/a//b","target":{"type":"view","view":"main"}}`:                                                                                         "path segment",
		`{"id":"a","path":"/{Key}","target":{"type":"view","view":"main"}}`:                                                                                        "path parameter",
		`{"id":"a","path":"/{k}/{k}","target":{"type":"view","view":"main"}}`:                                                                                      "repeats parameter",
		`{"id":"a","path":"/a/b/c/d/e/f/g/h/i","target":{"type":"view","view":"main"}}`:                                                                            "at most 8 segments",
		`{"id":"a","path":"/x","target":{"type":"view","view":"main"},"query":{"type":"object","properties":{"k":{"type":"string","pattern":"^a"}}}}`:              "unsupported keyword",
		`{"id":"a","path":"/x","target":{"type":"view","view":"main"},"query":{"type":"object","properties":{"k":{"type":"string","maxLength":2000}}}}`:            "maxLength",
		`{"id":"a","path":"/x","target":{"type":"view","view":"main"},"query":{"type":"object","properties":{"k":{"type":"integer","maxLength":2}}}}`:              "maxLength requires type string",
		`{"id":"a","path":"/x","target":{"type":"view","view":"main"},"query":{"type":"object","additionalProperties":true,"properties":{"k":{"type":"string"}}}}`: "additionalProperties",
		`{"id":"a","path":"/x","target":{"type":"view","view":"main","command":"refresh"}}`:                                                                        "must not name a command",
		`{"id":"a","path":"/x","target":{"type":"url","view":"main"}}`:                                                                                             "must be view or command",
		`{"id":"A","path":"/x","target":{"type":"view","view":"main"}}`:                                                                                            "must match",
	}
	for raw, want := range cases {
		if _, _, err := decodeUriHandlerEntry(ctx, 0, "", json.RawMessage(raw)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", raw, err, want)
		}
	}
	reserved := uriHandlerTestContext("settings")
	if _, _, err := decodeUriHandlerEntry(reserved, 0, "", json.RawMessage(`{"id":"a","path":"/x","target":{"type":"view","view":"main"}}`)); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("reserved plugin id err = %v", err)
	}
}

func TestUriHandlerFinalizeRejectsDuplicateShape(t *testing.T) {
	decode := func(id, path string) contributionEntry {
		segments, err := parseUriHandlerPath(path)
		if err != nil {
			t.Fatal(err)
		}
		return contributionEntry{ID: id, Value: UriHandlerContribution{ID: id, Path: path, segments: segments}}
	}
	entries := []contributionEntry{decode("a", "/issues/{key}"), decode("b", "/issues/{id}"), decode("c", "/issues/new"), decode("d", "/issues/{key}/x")}
	kept, issues := finalizeUriHandlerEntries(nil, entries, nil)
	if len(kept) != 3 || len(issues) != 1 || issues[0].ID != "b" {
		t.Fatalf("kept = %+v, issues = %+v", kept, issues)
	}
}

func TestUriHandlersExpandPermission(t *testing.T) {
	base := UriHandlerContribution{ID: "a", Path: "/x", Target: UriHandlerTarget{Type: "view", View: "main"}}
	resolved := func(handlers ...UriHandlerContribution) *resolvedContribution {
		entries := make([]any, len(handlers))
		for index, handler := range handlers {
			entries[index] = handler
		}
		return &resolvedContribution{Entries: entries}
	}
	retitled, retargeted := base, base
	retitled.Title = "New"
	retargeted.Target = UriHandlerTarget{Type: "command", Command: "refresh"}
	for _, test := range []struct {
		name               string
		current, candidate *resolvedContribution
		want               bool
	}{
		{"unchanged", resolved(base), resolved(base), false},
		{"retitled", resolved(base), resolved(retitled), false},
		{"retargeted", resolved(base), resolved(retargeted), true},
		{"new handler", nil, resolved(base), true},
		{"removed", resolved(base), resolved(), false},
	} {
		if got := uriHandlersExpandPermission(test.current, test.candidate); got != test.want {
			t.Errorf("%s = %v, want %v", test.name, got, test.want)
		}
	}
}

func TestParsePluginLink(t *testing.T) {
	link, err := parsePluginLink("mincode://Jira-Tools/issues/ABC%2F1/?focus=comments#frag")
	if err != nil || link.pluginID != "jira-tools" || link.path != "/issues/ABC%2F1" || len(link.segments) != 2 || link.segments[1] != "ABC/1" || link.query.Get("focus") != "comments" {
		t.Fatalf("link = %+v, %v", link, err)
	}
	for _, raw := range []string{
		"",
		"https://jira-tools/issues",
		"mincode://user@jira-tools/issues",
		"mincode://jira-tools:80/issues",
		"mincode://settings/issues",
		"mincode://plugin/issues",
		"mincode://jira-tools",
		"mincode://jira-tools/",
		"mincode://jira-tools/a?x=1&x=2",
		"mincode://jira-tools/a?x=%zz",
		"mincode://jira-tools/a//b",
		"mincode://bad_host!/a",
		"mincode://jira-tools/a/b/c/d/e/f/g/h/i",
		"mincode://jira-tools/" + strings.Repeat("a", 2100),
		"mincode://jira-tools/a\nb",
	} {
		if _, err := parsePluginLink(raw); err == nil {
			t.Errorf("%q must be rejected", raw)
		}
	}
}

func TestMatchUriHandlerPrefersLiterals(t *testing.T) {
	handler := func(id, path string) UriHandlerContribution {
		segments, err := parseUriHandlerPath(path)
		if err != nil {
			t.Fatal(err)
		}
		return UriHandlerContribution{ID: id, Path: path, segments: segments}
	}
	handlers := []UriHandlerContribution{handler("param", "/issues/{key}"), handler("literal", "/issues/new"), handler("both", "/{kind}/{key}")}
	if matched, params, ok := matchUriHandler(handlers, []string{"issues", "new"}); !ok || matched.ID != "literal" || len(params) != 0 {
		t.Fatalf("literal = %+v, %v", matched, params)
	}
	if matched, params, ok := matchUriHandler(handlers, []string{"issues", "ABC-1"}); !ok || matched.ID != "param" || params["key"] != "ABC-1" {
		t.Fatalf("param = %+v, %v", matched, params)
	}
	if matched, _, ok := matchUriHandler(handlers, []string{"wiki", "x"}); !ok || matched.ID != "both" {
		t.Fatalf("both = %+v", matched)
	}
	if _, _, ok := matchUriHandler(handlers, []string{"issues"}); ok {
		t.Fatal("segment count must match")
	}
}

func TestCoerceUriQuery(t *testing.T) {
	schema := map[string]any{"type": "object", "additionalProperties": false, "required": []any{"n"}, "properties": map[string]any{
		"n":     map[string]any{"type": "integer"},
		"f":     map[string]any{"type": "number"},
		"b":     map[string]any{"type": "boolean"},
		"mode":  map[string]any{"type": "string", "enum": []any{"a", "b"}},
		"short": map[string]any{"type": "string", "maxLength": float64(3)},
	}}
	params, err := coerceUriQuery(schema, url.Values{"n": {"42"}, "f": {"1.5"}, "b": {"true"}, "mode": {"a"}, "short": {"abc"}})
	if err != nil || params["n"] != float64(42) || params["f"] != 1.5 || params["b"] != true || params["mode"] != "a" {
		t.Fatalf("params = %+v, %v", params, err)
	}
	for name, query := range map[string]url.Values{
		"missing required": {"f": {"1"}},
		"not integer":      {"n": {"1.5"}},
		"not number":       {"n": {"1"}, "f": {"x"}},
		"not boolean":      {"n": {"1"}, "b": {"yes"}},
		"enum":             {"n": {"1"}, "mode": {"c"}},
		"unknown":          {"n": {"1"}, "other": {"x"}},
		"max length":       {"n": {"1"}, "short": {"abcd"}},
		"newline":          {"n": {"1"}, "mode": {"a\nb"}},
		"infinite":         {"n": {"1"}, "f": {"Inf"}},
	} {
		if _, err := coerceUriQuery(schema, query); err == nil {
			t.Errorf("%s must be rejected", name)
		}
	}
	if _, err := coerceUriQuery(nil, url.Values{"x": {"1"}}); err == nil {
		t.Fatal("a handler without query must reject parameters")
	}
	if params, err := coerceUriQuery(nil, url.Values{}); err != nil || len(params) != 0 {
		t.Fatalf("empty = %+v, %v", params, err)
	}
}

func uriResolveManager(t *testing.T, handlers ...UriHandlerContribution) (*pluginManager, *pluginRecord) {
	t.Helper()
	manager, record := contributionInvokeManager(t)
	entries := make([]any, len(handlers))
	for index, handler := range handlers {
		segments, err := parseUriHandlerPath(handler.Path)
		if err != nil {
			t.Fatal(err)
		}
		handler.segments = segments
		entries[index] = handler
	}
	record.mu.Lock()
	record.manifest.contributions = map[string]*resolvedContribution{uriHandlerPointKey: {Entries: entries}}
	record.mu.Unlock()
	return manager, record
}

func TestResolveURI(t *testing.T) {
	query := map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"focus": map[string]any{"type": "string", "enum": []any{"comments"}}}}
	manager, record := uriResolveManager(t,
		UriHandlerContribution{ID: "issue", Path: "/issues/{key}", Title: "Open issue", Target: UriHandlerTarget{Type: "view", View: "view"}, Query: query},
		UriHandlerContribution{ID: "run", Path: "/run", Target: UriHandlerTarget{Type: "command", Command: "run"}},
	)
	response, err := manager.resolveURI(uriResolveRequest{URL: "mincode://runtime.test/issues/ABC-1?focus=comments"})
	if err != nil {
		t.Fatal(err)
	}
	if response.PluginID != record.id || response.HandlerID != "issue" || response.Target.View != "view" || response.Target.ViewTitle != "View" ||
		response.PathParams["key"] != "ABC-1" || response.Params["focus"] != "comments" || response.Payload != "" {
		t.Fatalf("view response = %+v", response)
	}
	response, err = manager.resolveURI(uriResolveRequest{URL: "mincode://runtime.test/run"})
	if err != nil || response.Target.Type != "command" || response.Target.Command != "run" || response.Target.CommandTitle != "Run" {
		t.Fatalf("command response = %+v, %v", response, err)
	}
	// Resolving never activates the plugin.
	record.mu.Lock()
	state := record.state
	record.mu.Unlock()
	if state == pluginStateRunning {
		t.Fatal("resolve must not start the plugin")
	}
	for raw, code := range map[string]string{
		"mincode://runtime.test/issues/ABC-1?focus=other": contributionErrInvalidRequest,
		"mincode://runtime.test/issues/ABC-1?x=1":         contributionErrInvalidRequest,
		"mincode://runtime.test/run?x=1":                  contributionErrInvalidRequest,
		"mincode://runtime.test/missing":                  contributionErrNotFound,
		"mincode://other.plugin/run":                      contributionErrNotFound,
		"mincode://runtime.test/issues/a%0Ab":             contributionErrInvalidRequest,
		"https://runtime.test/run":                        contributionErrInvalidRequest,
	} {
		if _, err := manager.resolveURI(uriResolveRequest{URL: raw}); contributionErrorCode(err) != code {
			t.Errorf("%s: err = %v, want %s", raw, err, code)
		}
	}
	// A risk upgrade in a later generation makes the command unreachable.
	record.mu.Lock()
	record.manifest.Contributes.Commands[0].Risk = "write"
	record.mu.Unlock()
	if _, err := manager.resolveURI(uriResolveRequest{URL: "mincode://runtime.test/run"}); contributionErrorCode(err) != contributionErrUnavailable {
		t.Fatalf("write command err = %v", err)
	}
	if err := manager.SetEnabled(context.Background(), record.id, false); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.resolveURI(uriResolveRequest{URL: "mincode://runtime.test/issues/ABC-1"}); contributionErrorCode(err) != contributionErrUnavailable {
		t.Fatalf("disabled err = %v", err)
	}
}

func TestResolveURICommandPayload(t *testing.T) {
	manager, record := uriResolveManager(t, UriHandlerContribution{ID: "run", Path: "/run", Target: UriHandlerTarget{Type: "command", Command: "run"}})
	record.mu.Lock()
	record.manifest.Contributes.Commands[0].InputSchema = map[string]any{"type": "object", "properties": map[string]any{"key": map[string]any{"type": "string"}, "n": map[string]any{"type": "integer"}}}
	record.mu.Unlock()
	response, err := manager.resolveURI(uriResolveRequest{URL: "mincode://runtime.test/run?key=%3Cb%3E&n=3"})
	if err != nil || response.Payload != `{"key":"<b>","n":3}` {
		t.Fatalf("payload = %q, %v", response.Payload, err)
	}
	record.mu.Lock()
	record.manifest.Contributes.Commands[0].InputSchema = nil
	record.manifest.Contributes.Commands[0].Payload = `{"static":true}`
	record.mu.Unlock()
	if response, err := manager.resolveURI(uriResolveRequest{URL: "mincode://runtime.test/run"}); err != nil || response.Payload != `{"static":true}` {
		t.Fatalf("static payload = %q, %v", response.Payload, err)
	}
}

func TestPluginHostURIResolveHandlerRejectsBadArguments(t *testing.T) {
	for _, args := range []string{`nope`, `{"url":"x","extra":1}`} {
		if _, err := pluginHostURIResolveHandler("", args); contributionErrorCode(err) != contributionErrInvalidRequest {
			t.Errorf("%s: err = %v", args, err)
		}
	}
}

func TestValidateCommandInputValues(t *testing.T) {
	schema := map[string]any{"type": "object", "required": []any{"a"}, "properties": map[string]any{
		"a": map[string]any{"type": "string"},
		"i": map[string]any{"type": "integer"},
		"e": map[string]any{"type": "string", "enum": []any{"x"}},
	}}
	if err := validateCommandInputValues(schema, map[string]any{"a": "v", "i": float64(2), "e": "x"}); err != nil {
		t.Fatal(err)
	}
	for name, values := range map[string]map[string]any{
		"missing":  {"i": float64(1)},
		"type":     {"a": float64(1)},
		"integer":  {"a": "v", "i": 1.5},
		"enum":     {"a": "v", "e": "y"},
		"unknown":  {"a": "v", "z": "y"},
		"newline":  {"a": "a\nb"},
		"bidi":     {"a": "a‮b"},
		"infinity": {"a": "v", "i": math.Inf(1)},
	} {
		if err := validateCommandInputValues(schema, values); err == nil {
			t.Errorf("%s must be rejected", name)
		}
	}
}
