package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestTestProviderDecode(t *testing.T) {
	ctx := batchTwoTestContext(testProviderSpec)
	value, id, err := decodeTestProviderEntry(ctx, 0, "", json.RawMessage(`{"id":"jest","title":"Jest","discoverAction":"list","runAction":"write"}`))
	if err != nil || id != "jest" {
		t.Fatalf("decode = %v, %q", err, id)
	}
	if provider := value.(TestProviderContribution); provider.RunAction != "write" || provider.RunRisk != "write" {
		t.Fatalf("provider = %+v", provider)
	}
	value, _, err = decodeTestProviderEntry(ctx, 0, "", json.RawMessage(`{"id":"jest","title":"Jest","discoverAction":"list","runAction":"other"}`))
	if err != nil || value.(TestProviderContribution).RunRisk != "read" {
		t.Fatalf("read run = %+v, %v", value, err)
	}
	cases := map[string]string{
		`{"id":"a","title":"x","discoverAction":"write","runAction":"list"}`:           `risk "read"`,
		`{"id":"a","title":"x","discoverAction":"list","runAction":"list"}`:            "must differ",
		`{"id":"a","title":"x","discoverAction":"list","runAction":"missing"}`:         "contributes.actions",
		`{"id":"a","title":"x","discoverAction":"list"}`:                               "runAction",
		`{"id":"a","title":"","discoverAction":"list","runAction":"write"}`:            "title is required",
		`{"id":"a","title":"x","discoverAction":"list","runAction":"write","extra":1}`: "unknown field",
		`{"id":"Jest","title":"x","discoverAction":"list","runAction":"write"}`:        "must match",
	}
	for raw, want := range cases {
		if _, _, err := decodeTestProviderEntry(ctx, 0, "", json.RawMessage(raw)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", raw, err, want)
		}
	}
}

func TestTestProvidersExpandPermission(t *testing.T) {
	point := contributionPoints[testProviderPointKey]
	base := TestProviderContribution{ID: "a", Title: "A", DiscoverAction: "list", RunAction: "run", RunRisk: "read"}
	riskier, retitled := base, base
	riskier.RunRisk = "write"
	retitled.Title = "B"
	wrap := func(provider TestProviderContribution) *resolvedContribution {
		return &resolvedContribution{Entries: []any{provider}}
	}
	if point.ExpandsPermission(wrap(base), wrap(retitled)) {
		t.Fatal("retitling must not expand permission")
	}
	if !point.ExpandsPermission(wrap(base), wrap(riskier)) {
		t.Fatal("a riskier runAction must expand permission")
	}
}

func TestParseTestDiscoverResult(t *testing.T) {
	raw := `{"tests":[
		{"id":"suite","label":" Suite ","path":"src/a.test.ts","line":1,"children":[
			{"id":"suite/a","label":"a","path":"src/a.test.ts","line":3},
			{"id":"suite/bad","label":"","children":[{"id":"orphan","label":"o"}]},
			{"id":"suite","label":"duplicate"}
		]},
		{"id":"escape","label":"x","path":"../a"},
		{"id":"noline","label":"x","line":3},
		{"id":"empty","label":"Empty","children":[]},
		{"id":"html","label":"x","html":1}
	]}`
	response, err := parseTestDiscoverResult(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Tests) != 2 || response.Dropped != 5 || response.Truncated {
		t.Fatalf("response = %+v", response)
	}
	suite := response.Tests[0]
	if suite.Label != "Suite" || len(suite.Children) != 1 || suite.Children[0].Line != 3 || response.Tests[1].Children != nil {
		t.Fatalf("suite = %+v", response.Tests)
	}
	// Nesting deeper than depthMax is dropped.
	deep := `{"id":"leaf","label":"leaf"}`
	for level := testProviderDepthMax; level > 0; level-- {
		deep = fmt.Sprintf(`{"id":"n%d","label":"n","children":[%s]}`, level, deep)
	}
	response, err = parseTestDiscoverResult(json.RawMessage(`{"tests":[` + deep + `]}`))
	if err != nil || response.Dropped != 1 {
		t.Fatalf("deep = %+v, %v", response, err)
	}
	nodes := make([]string, testProviderTestsMax+1)
	for index := range nodes {
		nodes[index] = fmt.Sprintf(`{"id":"t%d","label":"t"}`, index)
	}
	response, err = parseTestDiscoverResult(json.RawMessage(`{"tests":[` + strings.Join(nodes, ",") + `]}`))
	if err != nil || !response.Truncated || len(response.Tests) != testProviderTestsMax {
		t.Fatalf("truncated = %d, %v", len(response.Tests), err)
	}
	for _, bad := range []string{`{"tests":null}`, `{"tests":[],"x":1}`, `[]`} {
		if _, err := parseTestDiscoverResult(json.RawMessage(bad)); err == nil {
			t.Errorf("%s must be rejected", bad)
		}
	}
}

func TestParseTestRunResult(t *testing.T) {
	raw := `{"output":"\u001b[32mok\u001b[0m\r\ndone","results":[
		{"id":"suite/a","status":"passed","durationMs":12.6},
		{"id":"suite/b","status":"failed","message":"expected \u001b[31m1\u001b[0m"},
		{"id":"suite/c","status":"unknown"},
		{"id":"suite/a","status":"passed"},
		{"id":"suite/d","status":"passed","durationMs":-1},
		{"id":"suite/e","status":"skipped","extra":1}
	]}`
	response, err := parseTestRunResult(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 2 || response.Dropped != 4 || response.Output != "ok\ndone" || response.OutputTruncated {
		t.Fatalf("response = %+v", response)
	}
	if first := response.Results[0]; first.DurationMs == nil || *first.DurationMs != 13 {
		t.Fatalf("duration = %+v", first)
	}
	if response.Results[1].Message != "expected 1" {
		t.Fatalf("message = %q", response.Results[1].Message)
	}
	long := strings.Repeat("x", testProviderOutputMaxRunes+5)
	response, err = parseTestRunResult(json.RawMessage(`{"results":[],"output":"` + long + `"}`))
	if err != nil || !response.OutputTruncated || len([]rune(response.Output)) > testProviderOutputMaxRunes {
		t.Fatalf("output = %d %v, %v", len(response.Output), response.OutputTruncated, err)
	}
	if _, err := parseTestRunResult(json.RawMessage(`{"results":null}`)); err == nil {
		t.Fatal("missing results must be rejected")
	}
}

func TestPluginTestProviderGating(t *testing.T) {
	manager, record := startedContributionManager(t)
	record.mu.Lock()
	record.manifest.contributions = map[string]*resolvedContribution{testProviderPointKey: {Entries: []any{TestProviderContribution{
		ID: "jest", Title: "Jest", DiscoverAction: "run", RunAction: "run2", RunRisk: "read",
	}}}}
	record.mu.Unlock()
	root := t.TempDir()
	discover := testDiscoverRequest{PluginID: record.id, ProviderID: "jest", RequestID: "request-0001", ProjectRoot: root}
	if _, err := manager.discoverTests(context.Background(), discover); contributionErrorCode(err) != contributionErrInvalidResult {
		t.Fatalf("discover err = %v", err)
	}
	assertNoContributionRequestsLeft(t)
	missingRoot := discover
	missingRoot.ProjectRoot = ""
	if _, err := manager.discoverTests(context.Background(), missingRoot); contributionErrorCode(err) != contributionErrForbidden {
		t.Fatalf("missing root err = %v", err)
	}
	unknown := discover
	unknown.ProviderID = "other"
	if _, err := manager.discoverTests(context.Background(), unknown); contributionErrorCode(err) != contributionErrNotFound {
		t.Fatalf("unknown provider err = %v", err)
	}
	run := testRunRequest{PluginID: record.id, ProviderID: "jest", RequestID: "request-0002", ProjectRoot: root, Tests: []string{"a", "a"}}
	if _, err := manager.runTests(context.Background(), run); contributionErrorCode(err) != contributionErrInvalidRequest {
		t.Fatalf("repeated ids err = %v", err)
	}
	run.Tests = make([]string, testProviderRunTestsMax+1)
	for index := range run.Tests {
		run.Tests[index] = fmt.Sprintf("t%d", index)
	}
	if _, err := manager.runTests(context.Background(), run); contributionErrorCode(err) != contributionErrInvalidRequest {
		t.Fatalf("too many ids err = %v", err)
	}
	assertNoContributionRequestsLeft(t)
}

func TestTestProviderPayload(t *testing.T) {
	project := &contributionProject{Name: "app", Root: "/work/app"}
	encoded, _ := encodeContributionJSON(testProviderPayload{Provider: "jest", Locale: "en", Project: project})
	if string(encoded) != `{"provider":"jest","locale":"en","project":{"name":"app","root":"/work/app"}}` {
		t.Fatalf("discover payload = %s", encoded)
	}
	all := []string{}
	encoded, _ = encodeContributionJSON(testProviderPayload{Provider: "jest", Locale: "en", Project: project, Tests: &all})
	if !strings.HasSuffix(string(encoded), `,"tests":[]}`) {
		t.Fatalf("run payload = %s", encoded)
	}
}

func TestPluginHostTestHandlersRejectBadArguments(t *testing.T) {
	for _, args := range []string{`nope`, `{"pluginId":"a","extra":1}`} {
		if _, err := pluginHostTestDiscoverHandler("", args); contributionErrorCode(err) != contributionErrInvalidRequest {
			t.Errorf("discover %s: err = %v", args, err)
		}
		if _, err := pluginHostTestRunHandler("", args); contributionErrorCode(err) != contributionErrInvalidRequest {
			t.Errorf("run %s: err = %v", args, err)
		}
	}
}
