package plugin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func linterTestContext() *pointContext {
	return &pointContext{
		manifest: &Manifest{ID: "acme.lint", APIVersion: 2},
		spec:     linterSpec,
		actions: map[string]ManifestUIAction{
			"lint":  {ID: "lint", Risk: "read", MaxPayloadBytes: 262144},
			"write": {ID: "write", Risk: "write", MaxPayloadBytes: 1024},
		},
	}
}

func TestLinterDecode(t *testing.T) {
	ctx := linterTestContext()
	value, id, err := decodeLinterEntry(ctx, 0, "", json.RawMessage(`{"id":"demo","title":" Demo ","titleLocalized":{"zh":"演示"},"languages":["json","c++"],"action":"lint"}`))
	if err != nil || id != "demo" {
		t.Fatalf("decode = %v, %q", err, id)
	}
	linter := value.(LinterContribution)
	if linter.Title != "Demo" || strings.Join(linter.Triggers, ",") != "open,save" || linter.DebounceMs != 750 || len(linter.Languages) != 2 || linter.TitleLocalized["zh"] != "演示" {
		t.Fatalf("linter = %+v", linter)
	}
	value, _, err = decodeLinterEntry(ctx, 0, "", json.RawMessage(`{"id":"demo","title":"x","languages":["json"],"action":"lint","triggers":["type","save"],"debounceMs":300}`))
	if err != nil || value.(LinterContribution).DebounceMs != 300 || strings.Join(value.(LinterContribution).Triggers, ",") != "type,save" {
		t.Fatalf("typed = %+v, %v", value, err)
	}
	cases := map[string]string{
		`{"id":"Bad","title":"x","languages":["json"],"action":"lint"}`:                                     "must match",
		`{"id":"a","title":"","languages":["json"],"action":"lint"}`:                                        "title is required",
		`{"id":"a","title":"x","languages":[],"action":"lint"}`:                                             "needs 1-16 languages",
		`{"id":"a","title":"x","languages":["JSON"],"action":"lint"}`:                                       `language "JSON"`,
		`{"id":"a","title":"x","languages":["json","json"],"action":"lint"}`:                                "repeats language",
		`{"id":"a","title":"x","languages":["json"],"action":"missing"}`:                                    "not one of this plugin's contributes.actions",
		`{"id":"a","title":"x","languages":["json"],"action":"write"}`:                                      `risk "read"`,
		`{"id":"a","title":"x","languages":["json"],"action":"lint","triggers":["change"]}`:                 `trigger "change"`,
		`{"id":"a","title":"x","languages":["json"],"action":"lint","triggers":["save","save"]}`:            "repeats trigger",
		`{"id":"a","title":"x","languages":["json"],"action":"lint","triggers":[]}`:                         "must not be empty",
		`{"id":"a","title":"x","languages":["json"],"action":"lint","debounceMs":500}`:                      `only with the "type" trigger`,
		`{"id":"a","title":"x","languages":["json"],"action":"lint","triggers":["type"],"debounceMs":100}`:  "debounceMs must be 300-5000",
		`{"id":"a","title":"x","languages":["json"],"action":"lint","triggers":["type"],"debounceMs":6000}`: "debounceMs must be 300-5000",
		`{"id":"a","title":"x","languages":["json"],"action":"lint","url":"x"}`:                             "unknown field",
		`{"id":"a","title":"x‮","languages":["json"],"action":"lint"}`:                                      "disallowed character",
	}
	for raw, want := range cases {
		if _, _, err := decodeLinterEntry(ctx, 0, "", json.RawMessage(raw)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", raw, err, want)
		}
	}
}

func TestLintersExpandPermission(t *testing.T) {
	base := LinterContribution{ID: "a", Languages: []string{"json"}, Action: "lint", Triggers: []string{"open"}}
	resolved := func(linters ...LinterContribution) *resolvedContribution {
		entries := make([]any, len(linters))
		for index, linter := range linters {
			entries[index] = linter
		}
		return &resolvedContribution{Entries: entries}
	}
	moreLanguages := base
	moreLanguages.Languages = []string{"json", "yaml"}
	otherAction := base
	otherAction.Action = "lint2"
	moreTriggers := base
	moreTriggers.Triggers = []string{"open", "type"}
	cases := []struct {
		name               string
		current, candidate *resolvedContribution
		want               bool
	}{
		{"unchanged", resolved(base), resolved(base), false},
		{"triggers", resolved(base), resolved(moreTriggers), false},
		{"removed", resolved(base), resolved(), false},
		{"added language", resolved(base), resolved(moreLanguages), true},
		{"changed action", resolved(base), resolved(otherAction), true},
		{"new linter", nil, resolved(base), true},
		{"no candidate", resolved(base), nil, false},
	}
	for _, test := range cases {
		if got := lintersExpandPermission(test.current, test.candidate); got != test.want {
			t.Errorf("%s = %v", test.name, got)
		}
	}
}

func validLintRequest() lintRequest {
	return lintRequest{
		PluginID: "runtime.test", LinterID: "lint", RequestID: "lint-request-01", LanguageID: "json",
		FileName: "/Users/me/project/src/a.json", RelativePath: "src/a.json", Text: `{"a":1}`, Trigger: "save",
	}
}

func TestNormalizeLintRequest(t *testing.T) {
	request := validLintRequest()
	if err := normalizeLintRequest(&request); err != nil || request.FileName != "a.json" || request.RelativePath != "src/a.json" {
		t.Fatalf("request = %+v, %v", request, err)
	}
	cases := map[string]func(*lintRequest){
		"linter id":         func(r *lintRequest) { r.LinterID = "Bad" },
		"language":          func(r *lintRequest) { r.LanguageID = "JSON" },
		"trigger":           func(r *lintRequest) { r.Trigger = "change" },
		"empty trigger":     func(r *lintRequest) { r.Trigger = "" },
		"file name":         func(r *lintRequest) { r.FileName = "/" },
		"escaping relative": func(r *lintRequest) { r.RelativePath = "../x.json" },
		"invalid utf-8":     func(r *lintRequest) { r.Text = "\xff" },
	}
	for name, mutate := range cases {
		request := validLintRequest()
		mutate(&request)
		if err := normalizeLintRequest(&request); contributionErrorCode(err) != contributionErrInvalidRequest {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestBuildLintPayloadGatesRelativePath(t *testing.T) {
	request := validLintRequest()
	_ = normalizeLintRequest(&request)
	without, _ := buildLintPayload(LinterContribution{ID: "lint"}, request, false)
	if strings.Contains(string(without), "relativePath") || strings.Contains(string(without), "/Users/") {
		t.Fatalf("payload without workspace.read = %s", without)
	}
	with, _ := buildLintPayload(LinterContribution{ID: "lint"}, request, true)
	if !strings.Contains(string(with), `"relativePath":"src/a.json"`) || !strings.Contains(string(with), `"trigger":"save"`) || !strings.Contains(string(with), `"linter":"lint"`) {
		t.Fatalf("payload with workspace.read = %s", with)
	}
}

func TestParseLinterResult(t *testing.T) {
	response, err := parseLinterResult(json.RawMessage(`{"diagnostics":[
		{"startLineNumber":3,"startColumn":5,"endLineNumber":1,"endColumn":2,"message":" bad‮ thing ","code":"E1","source":"demo","tags":["deprecated","deprecated"],"extra":{"url":"https://x"}},
		{"startLineNumber":1,"startColumn":1,"endLineNumber":1,"endColumn":2,"severity":"error","message":"ok"},
		{"startLineNumber":0,"startColumn":1,"endLineNumber":1,"endColumn":2,"message":"zero line"},
		{"startLineNumber":1.5,"startColumn":1,"endLineNumber":1,"endColumn":2,"message":"fraction"},
		{"startLineNumber":1,"startColumn":1,"endLineNumber":1,"endColumn":2,"message":"  "},
		{"startLineNumber":1,"startColumn":1,"endLineNumber":1,"endColumn":2,"severity":"fatal","message":"x"},
		{"startLineNumber":1,"startColumn":1,"endLineNumber":1,"endColumn":2,"message":"x","tags":["hidden"]},
		{"startLineNumber":1,"startColumn":1,"endLineNumber":1,"endColumn":2,"message":"x","code":"a\nb"},
		"not an object"
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Diagnostics) != 2 || response.Dropped != 7 || response.Truncated {
		t.Fatalf("response = %+v", response)
	}
	first := response.Diagnostics[0]
	if first.StartLineNumber != 1 || first.StartColumn != 2 || first.EndLineNumber != 3 || first.EndColumn != 5 {
		t.Fatalf("reversed range = %+v", first)
	}
	if first.Severity != "warning" || first.Message != "bad  thing" || first.Code != "E1" || first.Source != "demo" || len(first.Tags) != 1 {
		t.Fatalf("first = %+v", first)
	}
	if response.Diagnostics[1].Severity != "error" {
		t.Fatalf("second = %+v", response.Diagnostics[1])
	}
	item := `{"startLineNumber":1,"startColumn":1,"endLineNumber":1,"endColumn":2,"message":"x"}`
	many := `{"diagnostics":[` + strings.TrimSuffix(strings.Repeat(item+",", linterDiagnosticsMax+3), ",") + `]}`
	if response, err := parseLinterResult(json.RawMessage(many)); err != nil || !response.Truncated || len(response.Diagnostics) != linterDiagnosticsMax {
		t.Fatalf("many = %d, %v, %v", len(response.Diagnostics), response.Truncated, err)
	}
	if response, err := parseLinterResult(json.RawMessage(`{"diagnostics":[]}`)); err != nil || response.Diagnostics == nil {
		t.Fatalf("empty = %+v, %v", response, err)
	}
	for name, raw := range map[string]string{
		"ok field":      `{"ok":true}`,
		"missing":       `{}`,
		"null":          `{"diagnostics":null}`,
		"not array":     `{"diagnostics":{}}`,
		"extra":         `{"diagnostics":[],"more":1}`,
		"invalid utf-8": "{\"diagnostics\":[\"\xff\"]}",
	} {
		if _, err := parseLinterResult(json.RawMessage(raw)); contributionErrorCode(err) != contributionErrInvalidResult {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestSanitizeLintMessage(t *testing.T) {
	if got := sanitizeLintMessage("a\r\nb\x00c"); got != "a\nb c" {
		t.Fatalf("got %q", got)
	}
	long := sanitizeLintMessage(strings.Repeat("x", linterMessageMaxRunes+10))
	if len([]rune(long)) != linterMessageMaxRunes || !strings.HasSuffix(long, "…") {
		t.Fatalf("long = %d runes", len([]rune(long)))
	}
	lines := sanitizeLintMessage(strings.Repeat("l\n", linterMessageMaxLines+4))
	if strings.Count(lines, "\n") != linterMessageMaxLines-1 || !strings.HasSuffix(lines, "…") {
		t.Fatalf("lines = %q", lines)
	}
	exact := strings.Repeat("y", linterMessageMaxRunes)
	if got := sanitizeLintMessage(exact); got != exact {
		t.Fatal("a message at the limit must be kept")
	}
}

func linterInvokeManager(t *testing.T, linter LinterContribution) (*pluginManager, *pluginRecord) {
	t.Helper()
	manager, record := contributionInvokeManager(t)
	record.mu.Lock()
	record.manifest.contributions = map[string]*resolvedContribution{linterPointKey: {Entries: []any{linter}}}
	record.mu.Unlock()
	return manager, record
}

func TestPluginLintGating(t *testing.T) {
	manager, record := linterInvokeManager(t, LinterContribution{ID: "lint", Title: "Lint", Languages: []string{"json"}, Action: "run", Triggers: []string{"open", "save"}, DebounceMs: 750})
	// The helper process answers {"ok":true}, which result validation rejects.
	if _, err := manager.lint(context.Background(), validLintRequest()); contributionErrorCode(err) != contributionErrInvalidResult {
		t.Fatalf("dispatch err = %v", err)
	}
	assertNoContributionRequestsLeft(t)
	cases := map[string]struct {
		mutate func(*lintRequest)
		code   string
	}{
		"unknown plugin":    {func(r *lintRequest) { r.PluginID = "missing.plugin" }, contributionErrNotFound},
		"unknown linter":    {func(r *lintRequest) { r.LinterID = "other" }, contributionErrNotFound},
		"other language":    {func(r *lintRequest) { r.LanguageID = "yaml" }, contributionErrUnavailable},
		"undeclared type":   {func(r *lintRequest) { r.Trigger = "type" }, contributionErrInvalidRequest},
		"payload too large": {func(r *lintRequest) { r.Text = strings.Repeat("x", 1100) }, contributionErrTooLarge},
		"bad request id":    {func(r *lintRequest) { r.RequestID = "short" }, contributionErrInvalidRequest},
	}
	for name, test := range cases {
		request := validLintRequest()
		test.mutate(&request)
		if _, err := manager.lint(context.Background(), request); contributionErrorCode(err) != test.code {
			t.Errorf("%s: err = %v, want %s", name, err, test.code)
		}
	}
	slot := record.id + "\x00" + linterPointKey
	contributionRequests.mu.Lock()
	contributionRequests.slots[slot] = linterConcurrency
	contributionRequests.mu.Unlock()
	_, err := manager.lint(context.Background(), validLintRequest())
	contributionRequests.mu.Lock()
	delete(contributionRequests.slots, slot)
	contributionRequests.mu.Unlock()
	if contributionErrorCode(err) != contributionErrBusy {
		t.Fatalf("busy err = %v", err)
	}
	assertNoContributionRequestsLeft(t)
}

func TestPluginHostLintHandlerRejectsBadArguments(t *testing.T) {
	for _, args := range []string{`nope`, `{"pluginId":"a","extra":1}`} {
		if _, err := pluginHostLintHandler("", args); contributionErrorCode(err) != contributionErrInvalidRequest {
			t.Errorf("%s: err = %v", args, err)
		}
	}
}

func TestLinterDecodeDetectAndEnableSetting(t *testing.T) {
	ctx := linterTestContext()
	ctx.manifest.Contributes.Points = map[string]json.RawMessage{"configuration": json.RawMessage(`{"properties":{
		"eslint.enabled":{"type":"boolean","scope":"workspace"},
		"global.flag":{"type":"boolean"},
		"workspace.text":{"type":"string","scope":"workspace"}}}`)}
	value, _, err := decodeLinterEntry(ctx, 0, "", json.RawMessage(`{"id":"eslint","title":"ESLint","languages":["javascript"],"action":"lint",
		"detect":{"files":["eslint.config.js",".github/workflows"," package.json "]},"enableSetting":"eslint.enabled"}`))
	if err != nil {
		t.Fatal(err)
	}
	linter := value.(LinterContribution)
	if strings.Join(linter.Detect.Files, ",") != "eslint.config.js,.github/workflows,package.json" || linter.EnableSetting != "eslint.enabled" {
		t.Fatalf("linter = %+v", linter)
	}
	value, _, err = decodeLinterEntry(ctx, 0, "", json.RawMessage(`{"id":"plain","title":"x","languages":["json"],"action":"lint"}`))
	if err != nil || value.(LinterContribution).Detect != nil || value.(LinterContribution).EnableSetting != "" {
		t.Fatalf("plain = %+v, %v", value, err)
	}
	tooMany := `["a","b","c","d","e","f","g","h","i","j","k","l","m","n","o","p","q"]`
	cases := map[string]string{
		`"detect":{"files":[]}`:                                   "detect.files needs 1-16 paths",
		`"detect":{"files":` + tooMany + `}`:                      "detect.files needs 1-16 paths",
		`"detect":{}`:                                             "detect.files needs 1-16 paths",
		`"detect":{"files":["../up"]}`:                            `detect path "../up"`,
		`"detect":{"files":["/etc/passwd"]}`:                      `detect path "/etc/passwd"`,
		`"detect":{"files":["src/*.js"]}`:                         `detect path "src/*.js"`,
		`"detect":{"files":["a\\b"]}`:                             `detect path`,
		`"detect":{"files":["a","a"]}`:                            "repeats detect path",
		`"detect":{"files":["a"],"globs":["x"]}`:                  "unknown field",
		`"enableSetting":"missing.key"`:                           `enableSetting "missing.key"`,
		`"enableSetting":"global.flag"`:                           `enableSetting "global.flag"`,
		`"enableSetting":"workspace.text"`:                        `enableSetting "workspace.text"`,
		`"enableSetting":""`:                                      `enableSetting ""`,
		`"detect":{"files":["` + strings.Repeat("x", 129) + `"]}`: "detect path",
	}
	for field, want := range cases {
		raw := `{"id":"a","title":"x","languages":["json"],"action":"lint",` + field + `}`
		if _, _, err := decodeLinterEntry(ctx, 0, "", json.RawMessage(raw)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", field, err, want)
		}
	}
	// Without any contributes.configuration an enableSetting can never be valid.
	if _, _, err := decodeLinterEntry(linterTestContext(), 0, "", json.RawMessage(`{"id":"a","title":"x","languages":["json"],"action":"lint","enableSetting":"eslint.enabled"}`)); err == nil {
		t.Fatal("enableSetting accepted without configuration")
	}
}
