package plugin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func aiContextProviderTestContext() *pointContext {
	return &pointContext{
		manifest: &Manifest{ID: "acme.ctx", APIVersion: 2},
		spec:     aiContextProviderSpec,
		actions: map[string]ManifestUIAction{
			"fetch":    {ID: "fetch", Risk: "read", MaxPayloadBytes: 2048},
			"callable": {ID: "callable", Risk: "read", AICallable: true, MaxPayloadBytes: 2048},
			"write":    {ID: "write", Risk: "write", MaxPayloadBytes: 1024},
		},
	}
}

func TestAiContextProviderDecode(t *testing.T) {
	ctx := aiContextProviderTestContext()
	value, id, err := decodeAiContextProviderEntry(ctx, 0, "", json.RawMessage(`{"id":"issues","mention":"issue","title":" Issue ","description":"Attach an issue","descriptionLocalized":{"zh":"附加问题"},"action":"fetch","argument":{"required":true,"placeholder":"#123"}}`))
	if err != nil || id != "issues" {
		t.Fatalf("decode = %v, %q", err, id)
	}
	provider := value.(AiContextProviderContribution)
	if provider.Title != "Issue" || provider.Mention != "issue" || provider.Argument == nil || !provider.Argument.Required || provider.Argument.Placeholder != "#123" || provider.DescriptionLocalized["zh"] != "附加问题" {
		t.Fatalf("provider = %+v", provider)
	}
	cases := map[string]string{
		`{"id":"Bad","mention":"issue","title":"x","description":"d","action":"fetch"}`:                       "must match",
		`{"id":"a","mention":"i","title":"x","description":"d","action":"fetch"}`:                             "mention",
		`{"id":"a","mention":"Issue","title":"x","description":"d","action":"fetch"}`:                         "mention",
		`{"id":"a","mention":"file","title":"x","description":"d","action":"fetch"}`:                          "reserved",
		`{"id":"a","mention":"issue","title":"x","action":"fetch"}`:                                           "description is required",
		`{"id":"a","mention":"issue","title":"x","description":"d‮","action":"fetch"}`:                        "disallowed character",
		`{"id":"a","mention":"issue","title":"x","description":"d","action":"missing"}`:                       "not one of this plugin's contributes.actions",
		`{"id":"a","mention":"issue","title":"x","description":"d","action":"write"}`:                         `risk "read"`,
		`{"id":"a","mention":"issue","title":"x","description":"d","action":"callable"}`:                      "must not be aiCallable",
		`{"id":"a","mention":"issue","title":"x","description":"d","action":"fetch","argument":{"kind":"x"}}`: "unknown field",
		`{"id":"a","mention":"issue","title":"x","description":"d","action":"fetch","prompt":"x"}`:            "unknown field",
	}
	for raw, want := range cases {
		if _, _, err := decodeAiContextProviderEntry(ctx, 0, "", json.RawMessage(raw)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", raw, err, want)
		}
	}
}

func TestFinalizeAiContextProviderEntriesRejectsDuplicateMentions(t *testing.T) {
	entries := []contributionEntry{
		{Index: 0, ID: "a", Value: AiContextProviderContribution{ID: "a", Mention: "issue"}},
		{Index: 1, ID: "b", Value: AiContextProviderContribution{ID: "b", Mention: "issue"}},
		{Index: 2, ID: "c", Value: AiContextProviderContribution{ID: "c", Mention: "pr"}},
	}
	kept, issues := finalizeAiContextProviderEntries(nil, entries, nil)
	if len(kept) != 2 || kept[1].ID != "c" || len(issues) != 1 || issues[0].ID != "b" || !strings.Contains(issues[0].Message, `mention "issue"`) {
		t.Fatalf("kept = %+v, issues = %+v", kept, issues)
	}
}

func TestAiContextProvidersExpandPermission(t *testing.T) {
	base := AiContextProviderContribution{ID: "a", Mention: "issue", Action: "fetch"}
	resolved := func(providers ...AiContextProviderContribution) *resolvedContribution {
		entries := make([]any, len(providers))
		for index, provider := range providers {
			entries[index] = provider
		}
		return &resolvedContribution{Entries: entries}
	}
	renamed := base
	renamed.Mention = "ticket"
	otherAction := base
	otherAction.Action = "fetch2"
	cases := []struct {
		name               string
		current, candidate *resolvedContribution
		want               bool
	}{
		{"unchanged", resolved(base), resolved(base), false},
		{"mention", resolved(base), resolved(renamed), false},
		{"removed", resolved(base), resolved(), false},
		{"changed action", resolved(base), resolved(otherAction), true},
		{"new provider", nil, resolved(base), true},
		{"no candidate", resolved(base), nil, false},
	}
	for _, test := range cases {
		if got := aiContextProvidersExpandPermission(test.current, test.candidate); got != test.want {
			t.Errorf("%s = %v", test.name, got)
		}
	}
}

func stringPointer(value string) *string { return &value }

func TestNormalizeAiContextRequest(t *testing.T) {
	request := aiContextRequest{PluginID: " runtime.test ", ProviderID: "ctx", RequestID: "ctx-request-01", Argument: stringPointer("  ")}
	if err := normalizeAiContextRequest(&request); err != nil || request.PluginID != "runtime.test" || request.Argument != nil || request.Locale != "en" {
		t.Fatalf("request = %+v, %v", request, err)
	}
	cases := map[string]aiContextRequest{
		"plugin":        {ProviderID: "ctx"},
		"provider":      {PluginID: "p", ProviderID: "Bad"},
		"locale":        {PluginID: "p", ProviderID: "ctx", Locale: "../x"},
		"multiline arg": {PluginID: "p", ProviderID: "ctx", Argument: stringPointer("a\nb")},
		"long arg":      {PluginID: "p", ProviderID: "ctx", Argument: stringPointer(strings.Repeat("x", aiContextArgumentMaxRunes+1))},
	}
	for name, request := range cases {
		if err := normalizeAiContextRequest(&request); contributionErrorCode(err) != contributionErrInvalidRequest {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestParseAiContextResult(t *testing.T) {
	response, err := parseAiContextResult(json.RawMessage(`{"text":"a\r\nb‮c​d\te\u0007","title":" Issue #1 ","truncated":false}`))
	if err != nil || response.Text != "a\nbcd\te" || response.RemovedCharacters != 3 || response.Title != "Issue #1" || response.Truncated {
		t.Fatalf("response = %+v, %v", response, err)
	}
	long := `{"text":"` + strings.Repeat("é", aiContextResultMaxChars+5) + `"}`
	if response, err := parseAiContextResult(json.RawMessage(long)); err != nil || !response.Truncated || len([]rune(response.Text)) != aiContextResultMaxChars {
		t.Fatalf("long = %v, %v", response.Truncated, err)
	}
	if response, err := parseAiContextResult(json.RawMessage(`{"text":"x","truncated":true}`)); err != nil || !response.Truncated {
		t.Fatalf("plugin truncated = %+v, %v", response, err)
	}
	for name, raw := range map[string]string{
		"ok field":      `{"ok":true}`,
		"missing text":  `{"title":"x"}`,
		"nul":           `{"text":"a\u0000b"}`,
		"invalid utf-8": "{\"text\":\"\xff\"}",
		"title lines":   `{"text":"x","title":"a\nb"}`,
		"text type":     `{"text":1}`,
	} {
		if _, err := parseAiContextResult(json.RawMessage(raw)); contributionErrorCode(err) != contributionErrInvalidResult {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestPluginAiContextGating(t *testing.T) {
	manager, record := contributionInvokeManager(t)
	record.mu.Lock()
	record.manifest.contributions = map[string]*resolvedContribution{aiContextProviderPointKey: {Entries: []any{
		AiContextProviderContribution{ID: "ctx", Mention: "ctx", Title: "Ctx", Description: "d", Action: "run"},
		AiContextProviderContribution{ID: "arg", Mention: "arg", Title: "Arg", Description: "d", Action: "run", Argument: &AiContextProviderArgument{Required: true}},
	}}}
	record.mu.Unlock()
	valid := func() aiContextRequest {
		return aiContextRequest{PluginID: record.id, ProviderID: "ctx", RequestID: "ctx-request-01"}
	}
	// The helper process answers {"ok":true}, which result validation rejects.
	if _, err := manager.aiContext(context.Background(), valid()); contributionErrorCode(err) != contributionErrInvalidResult {
		t.Fatalf("dispatch err = %v", err)
	}
	assertNoContributionRequestsLeft(t)
	cases := map[string]struct {
		mutate func(*aiContextRequest)
		code   string
	}{
		"unknown plugin":   {func(r *aiContextRequest) { r.PluginID = "missing.plugin" }, contributionErrNotFound},
		"unknown provider": {func(r *aiContextRequest) { r.ProviderID = "other" }, contributionErrNotFound},
		"unexpected arg":   {func(r *aiContextRequest) { r.Argument = stringPointer("x") }, contributionErrInvalidRequest},
		"missing arg":      {func(r *aiContextRequest) { r.ProviderID = "arg" }, contributionErrInvalidRequest},
		"bad request id":   {func(r *aiContextRequest) { r.RequestID = "short" }, contributionErrInvalidRequest},
	}
	for name, test := range cases {
		request := valid()
		test.mutate(&request)
		if _, err := manager.aiContext(context.Background(), request); contributionErrorCode(err) != test.code {
			t.Errorf("%s: err = %v, want %s", name, err, test.code)
		}
	}
	withArg := valid()
	withArg.ProviderID, withArg.Argument = "arg", stringPointer("#12")
	if _, err := manager.aiContext(context.Background(), withArg); contributionErrorCode(err) != contributionErrInvalidResult {
		t.Fatalf("argument dispatch err = %v", err)
	}
	assertNoContributionRequestsLeft(t)
}

func TestPluginHostAiContextHandlerRejectsBadArguments(t *testing.T) {
	for _, args := range []string{`nope`, `{"pluginId":"a","text":"x"}`} {
		if _, err := pluginHostAiContextHandler("", args); contributionErrorCode(err) != contributionErrInvalidRequest {
			t.Errorf("%s: err = %v", args, err)
		}
	}
}
