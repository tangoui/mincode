package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestLanguageFeatureDecode(t *testing.T) {
	ctx := batchTwoTestContext(languageFeatureSpec)
	value, id, err := decodeLanguageFeatureEntry(ctx, 0, "", json.RawMessage(`{"id":"docs","title":"Docs","languages":["yaml","json"],"features":["hover","completion"],"triggerCharacters":[".",":"],"action":"list"}`))
	if err != nil || id != "docs" {
		t.Fatalf("decode = %v, %q", err, id)
	}
	provider := value.(LanguageFeatureContribution)
	if len(provider.Languages) != 2 || len(provider.Features) != 2 || len(provider.TriggerCharacters) != 2 || provider.Action != "list" {
		t.Fatalf("provider = %+v", provider)
	}
	cases := map[string]string{
		`{"id":"a","title":"x","languages":[],"features":["hover"],"action":"list"}`:                                       "languages",
		`{"id":"a","title":"x","languages":["YAML"],"features":["hover"],"action":"list"}`:                                 "must match",
		`{"id":"a","title":"x","languages":["yaml","yaml"],"features":["hover"],"action":"list"}`:                          "repeats language",
		`{"id":"a","title":"x","languages":["yaml"],"features":[],"action":"list"}`:                                        "at least one feature",
		`{"id":"a","title":"x","languages":["yaml"],"features":["rename"],"action":"list"}`:                                "must be one of",
		`{"id":"a","title":"x","languages":["yaml"],"features":["hover","hover"],"action":"list"}`:                         "repeats feature",
		`{"id":"a","title":"x","languages":["yaml"],"features":["hover"],"triggerCharacters":["."],"action":"list"}`:       "need the completion",
		`{"id":"a","title":"x","languages":["yaml"],"features":["completion"],"triggerCharacters":["ab"],"action":"list"}`: "printable ASCII",
		`{"id":"a","title":"x","languages":["yaml"],"features":["completion"],"triggerCharacters":[" "],"action":"list"}`:  "printable ASCII",
		`{"id":"a","title":"x","languages":["yaml"],"features":["hover"],"action":"write"}`:                                `risk "read"`,
		`{"id":"a","title":"x","languages":["yaml"],"features":["hover"],"action":"list","extra":1}`:                       "unknown field",
	}
	for raw, want := range cases {
		if _, _, err := decodeLanguageFeatureEntry(ctx, 0, "", json.RawMessage(raw)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", raw, err, want)
		}
	}
	if _, _, err := decodeLanguageFeatureEntry(ctx, 0, "", json.RawMessage(`{"id":"lens","title":"Lens","languages":["go"],"features":["codeLens","codeAction"],"action":"list"}`)); err != nil {
		t.Fatalf("codeLens/codeAction provider rejected: %v", err)
	}
	if _, _, err := decodeLanguageFeatureEntry(ctx, 0, "", json.RawMessage(`{"id":"lens","title":"Lens","languages":["go"],"features":["codeLens"],"triggerCharacters":["."],"action":"list"}`)); err == nil {
		t.Fatal("triggerCharacters without completion must be rejected for codeLens providers too")
	}
	many := `["a","b","c","d","e","f","g","h","i"]`
	if _, _, err := decodeLanguageFeatureEntry(ctx, 0, "", json.RawMessage(`{"id":"a","title":"x","languages":["yaml"],"features":["completion"],"triggerCharacters":`+many+`,"action":"list"}`)); err == nil {
		t.Fatal("too many trigger characters must be rejected")
	}
}

func TestLanguageFeaturesExpandPermission(t *testing.T) {
	point := contributionPoints[languageFeaturePointKey]
	base := LanguageFeatureContribution{ID: "a", Title: "A", Languages: []string{"yaml"}, Features: []string{"hover"}, Action: "list"}
	moreLanguages, moreFeatures, retitled := base, base, base
	moreLanguages.Languages = []string{"json", "yaml"}
	moreFeatures.Features = []string{"completion", "hover"}
	retitled.Title = "B"
	wrap := func(provider LanguageFeatureContribution) *resolvedContribution {
		return &resolvedContribution{Entries: []any{provider}}
	}
	if point.ExpandsPermission(wrap(base), wrap(retitled)) {
		t.Fatal("retitling must not expand permission")
	}
	if !point.ExpandsPermission(wrap(base), wrap(moreLanguages)) || !point.ExpandsPermission(wrap(base), wrap(moreFeatures)) {
		t.Fatal("new languages or features must expand permission")
	}
}

func TestLanguageFeatureDecodeNewKinds(t *testing.T) {
	ctx := batchTwoTestContext(languageFeatureSpec)
	value, _, err := decodeLanguageFeatureEntry(ctx, 0, "", json.RawMessage(`{"id":"nav","title":"Nav","languages":["go"],"features":["definition","references","codeLens","inlayHints","documentSymbol"],"action":"list"}`))
	if err != nil {
		t.Fatalf("new kinds rejected: %v", err)
	}
	if got := value.(LanguageFeatureContribution).Features; !slices.Equal(got, []string{"definition", "references", "codeLens", "inlayHints", "documentSymbol"}) {
		t.Fatalf("features = %v", got)
	}
	_, _, err = decodeLanguageFeatureEntry(ctx, 0, "", json.RawMessage(`{"id":"nav","title":"Nav","languages":["go"],"features":["rename"],"action":"list"}`))
	if err == nil || !strings.Contains(err.Error(), `feature "rename" must be one of hover, completion, definition, references, codeLens, inlayHints, documentSymbol`) {
		t.Fatalf("rename: err = %v", err)
	}
	_, _, err = decodeLanguageFeatureEntry(ctx, 0, "", json.RawMessage(`{"id":"nav","title":"Nav","languages":["go"],"features":["definition"],"triggerCharacters":["."],"action":"list"}`))
	if err == nil || !strings.Contains(err.Error(), "triggerCharacters need the completion feature") {
		t.Fatalf("definition with triggerCharacters: err = %v", err)
	}
	for feature, passive := range languageFeaturePassive {
		if !passive || !slices.Contains(languageFeatureFeatures, feature) {
			t.Fatalf("languageFeaturePassive[%q] = %v is not a contract feature", feature, passive)
		}
	}
	if len(languageFeaturePassive) != 3 || !languageFeaturePassive["codeLens"] || !languageFeaturePassive["inlayHints"] || !languageFeaturePassive["documentSymbol"] {
		t.Fatalf("languageFeaturePassive = %v", languageFeaturePassive)
	}
}

func TestLanguageFeaturesExpandPermissionNewKind(t *testing.T) {
	point := contributionPoints[languageFeaturePointKey]
	base := LanguageFeatureContribution{ID: "a", Title: "A", Languages: []string{"go"}, Features: []string{"hover"}, Action: "list"}
	withLens := base
	withLens.Features = []string{"hover", "codeLens"}
	wrap := func(provider LanguageFeatureContribution) *resolvedContribution {
		return &resolvedContribution{Entries: []any{provider}}
	}
	if !point.ExpandsPermission(wrap(base), wrap(withLens)) {
		t.Fatal("adding codeLens must expand permission")
	}
}

func validLanguageFeatureRequest() languageFeatureRequest {
	return languageFeatureRequest{
		PluginID: "runtime.test", ProviderID: "docs", RequestID: "request-0001", Feature: "hover", LanguageID: "yaml",
		FileName: "/Users/me/project/a.yaml", RelativePath: "a.yaml", Text: "key: value\nother: 1", StartLine: 1,
		Position: &languageFeaturePosition{Line: 2, Column: 3},
	}
}

func TestNormalizeLanguageFeatureRequest(t *testing.T) {
	request := validLanguageFeatureRequest()
	if err := normalizeLanguageFeatureRequest(&request); err != nil || request.FileName != "a.yaml" || request.Locale != "en" {
		t.Fatalf("valid = %+v, %v", request, err)
	}
	window := validLanguageFeatureRequest()
	window.StartLine, window.Position.Line = 40, 41
	if err := normalizeLanguageFeatureRequest(&window); err != nil {
		t.Fatalf("window = %v", err)
	}
	cases := map[string]func(*languageFeatureRequest){
		"feature":        func(r *languageFeatureRequest) { r.Feature = "rename" },
		"language":       func(r *languageFeatureRequest) { r.LanguageID = "YAML" },
		"file name":      func(r *languageFeatureRequest) { r.FileName = "/" },
		"relative":       func(r *languageFeatureRequest) { r.RelativePath = "../a.yaml" },
		"utf-8":          func(r *languageFeatureRequest) { r.Text = "\xff" },
		"too large":      func(r *languageFeatureRequest) { r.Text = strings.Repeat("a", languageFeatureTextMaxBytes+1) },
		"start line":     func(r *languageFeatureRequest) { r.StartLine = 0 },
		"line before":    func(r *languageFeatureRequest) { r.StartLine, r.Position.Line = 5, 4 },
		"line after":     func(r *languageFeatureRequest) { r.Position.Line = 3 },
		"column":         func(r *languageFeatureRequest) { r.Position.Column = 10 },
		"zero column":    func(r *languageFeatureRequest) { r.Position.Column = 0 },
		"trigger":        func(r *languageFeatureRequest) { r.TriggerCharacter = "ab" },
		"locale":         func(r *languageFeatureRequest) { r.Locale = "en_US!" },
		"bad provider":   func(r *languageFeatureRequest) { r.ProviderID = "X" },
		"bad plugin id":  func(r *languageFeatureRequest) { r.PluginID = "Bad Id" },
		"bidi file name": func(r *languageFeatureRequest) { r.FileName = "a\u202e.yaml" },
	}
	for name, mutate := range cases {
		request := validLanguageFeatureRequest()
		mutate(&request)
		if err := normalizeLanguageFeatureRequest(&request); contributionErrorCode(err) != contributionErrInvalidRequest {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func validDocumentFeatureRequest(feature string) languageFeatureRequest {
	request := validLanguageFeatureRequest()
	request.Feature = feature
	request.Position = nil
	if feature == "codeAction" {
		request.Range = &languageFeatureRange{StartLine: 1, StartColumn: 1, EndLine: 1, EndColumn: 4}
		request.Diagnostics = []languageFeatureDiagnostic{{
			Range: LanguageRange{StartLine: 2, StartColumn: 1, EndLine: 2, EndColumn: 6}, Message: "unknown key\nother", Severity: "warning", Source: "lint",
		}}
	}
	return request
}

func TestNormalizeDocumentFeatureRequest(t *testing.T) {
	for _, feature := range []string{"codeLens", "codeAction"} {
		request := validDocumentFeatureRequest(feature)
		if err := normalizeLanguageFeatureRequest(&request); err != nil {
			t.Fatalf("%s valid = %v", feature, err)
		}
	}
	empty := validDocumentFeatureRequest("codeAction")
	empty.Diagnostics = nil
	if err := normalizeLanguageFeatureRequest(&empty); err != nil {
		t.Fatalf("codeAction without diagnostics = %v", err)
	}
	tooMany := make([]languageFeatureDiagnostic, languageFeatureDiagnosticsMax+1)
	for index := range tooMany {
		tooMany[index] = languageFeatureDiagnostic{Range: LanguageRange{StartLine: 1, StartColumn: 1, EndLine: 1, EndColumn: 2}, Message: "m", Severity: "error"}
	}
	cases := map[string]struct {
		feature string
		mutate  func(*languageFeatureRequest)
	}{
		"lens window":   {"codeLens", func(r *languageFeatureRequest) { r.StartLine = 2 }},
		"lens position": {"codeLens", func(r *languageFeatureRequest) { r.Position = &languageFeaturePosition{Line: 1, Column: 1} }},
		"lens range": {"codeLens", func(r *languageFeatureRequest) {
			r.Range = &languageFeatureRange{StartLine: 1, StartColumn: 1, EndLine: 1, EndColumn: 1}
		}},
		"action missing range":  {"codeAction", func(r *languageFeatureRequest) { r.Range = nil }},
		"action range past end": {"codeAction", func(r *languageFeatureRequest) { r.Range.EndLine = 3 }},
		"action range column":   {"codeAction", func(r *languageFeatureRequest) { r.Range.EndColumn = 30 }},
		"action reversed range": {"codeAction", func(r *languageFeatureRequest) { r.Range.StartColumn, r.Range.EndColumn = 4, 2 }},
		"diagnostic severity":   {"codeAction", func(r *languageFeatureRequest) { r.Diagnostics[0].Severity = "fatal" }},
		"diagnostic blank":      {"codeAction", func(r *languageFeatureRequest) { r.Diagnostics[0].Message = " " }},
		"diagnostic long": {"codeAction", func(r *languageFeatureRequest) {
			r.Diagnostics[0].Message = strings.Repeat("m", languageFeatureDiagnosticMessageMax+1)
		}},
		"diagnostic source":    {"codeAction", func(r *languageFeatureRequest) { r.Diagnostics[0].Source = "a\nb" }},
		"diagnostic range":     {"codeAction", func(r *languageFeatureRequest) { r.Diagnostics[0].Range.StartLine = 9 }},
		"too many diagnostics": {"codeAction", func(r *languageFeatureRequest) { r.Diagnostics = tooMany }},
		"hover with range": {"hover", func(r *languageFeatureRequest) {
			r.Range = &languageFeatureRange{StartLine: 1, StartColumn: 1, EndLine: 1, EndColumn: 1}
		}},
		"hover with diagnostics": {"hover", func(r *languageFeatureRequest) { r.Diagnostics = tooMany[:1] }},
	}
	for name, test := range cases {
		request := validDocumentFeatureRequest(test.feature)
		if test.feature == "hover" {
			request = validLanguageFeatureRequest()
		}
		test.mutate(&request)
		if err := normalizeLanguageFeatureRequest(&request); contributionErrorCode(err) != contributionErrInvalidRequest {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestBuildLanguageFeaturePayload(t *testing.T) {
	encode := func(request languageFeatureRequest, workspaceRead bool) string {
		if err := normalizeLanguageFeatureRequest(&request); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(buildLanguageFeaturePayload("docs", request, workspaceRead))
		if err != nil {
			t.Fatal(err)
		}
		return string(encoded)
	}
	hover := encode(validLanguageFeatureRequest(), false)
	if !strings.Contains(hover, `"position":{"line":2,"column":3}`) || strings.Contains(hover, "relativePath") || strings.Contains(hover, "range") || strings.Contains(hover, "diagnostics") {
		t.Fatalf("hover payload = %s", hover)
	}
	lens := encode(validDocumentFeatureRequest("codeLens"), true)
	if strings.Contains(lens, "position") || strings.Contains(lens, "range") || strings.Contains(lens, "diagnostics") || !strings.Contains(lens, `"relativePath":"a.yaml"`) {
		t.Fatalf("codeLens payload = %s", lens)
	}
	action := validDocumentFeatureRequest("codeAction")
	if payload := encode(action, false); !strings.Contains(payload, `"range":{"startLine":1,"startColumn":1,"endLine":1,"endColumn":4}`) ||
		!strings.Contains(payload, `"diagnostics":[{"range":{"startLine":2,"startColumn":1,"endLine":2,"endColumn":6},"message":"unknown key\nother","severity":"warning","source":"lint"}]`) || strings.Contains(payload, "position") {
		t.Fatalf("codeAction payload = %s", payload)
	}
	action.Diagnostics = nil
	if payload := encode(action, false); !strings.Contains(payload, `"diagnostics":[]`) {
		t.Fatalf("codeAction payload without diagnostics = %s", payload)
	}
}

func TestParseLanguageCodeActionResult(t *testing.T) {
	lines := strings.Split("const a = 1\nconst b = 2", "\n")
	edit := func(startLine, startColumn, endLine, endColumn int, text string) string {
		encoded, _ := json.Marshal(text)
		return fmt.Sprintf(`{"range":{"startLine":%d,"startColumn":%d,"endLine":%d,"endColumn":%d},"text":%s}`, startLine, startColumn, endLine, endColumn, encoded)
	}
	action := func(title string, extra string, edits ...string) string {
		return `{"title":"` + title + `"` + extra + `,"edits":[` + strings.Join(edits, ",") + `]}`
	}
	raw := `{"actions":[` + strings.Join([]string{
		action("Use let", `,"kind":"quickfix","isPreferred":true`, edit(1, 1, 1, 6, "let"), edit(2, 1, 2, 6, "let")),
		action("Delete line", "", edit(1, 1, 2, 1, "")),
		action("Past end", "", edit(1, 1, 1, 2, "x"), edit(3, 1, 3, 1, "y")),
		action("Column past line", "", edit(2, 1, 2, 40, "x")),
		action("Overlap", "", edit(1, 1, 1, 6, "a"), edit(1, 3, 1, 8, "b")),
		action("No edits", ""),
		action("Bad kind", `,"kind":"run.shell"`, edit(1, 1, 1, 2, "x")),
		action("Other file", `,"uri":"file:///etc/hosts"`, edit(1, 1, 1, 2, "x")),
		action("Command", `,"command":"rm"`, edit(1, 1, 1, 2, "x")),
		action("Bidi", "", edit(1, 1, 1, 2, "x\u202ey")),
	}, ",") + `]}`
	response, err := parseLanguageCodeActionResult(json.RawMessage(raw), lines)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Actions) != 2 || response.Dropped != 8 {
		t.Fatalf("actions = %+v", response)
	}
	first := response.Actions[0]
	if first.Title != "Use let" || first.Kind != "quickfix" || !first.IsPreferred || len(first.Edits) != 2 || first.Edits[1].Range.StartLine != 2 {
		t.Fatalf("first action = %+v", first)
	}
	if second := response.Actions[1]; second.Edits[0].Text != "" || second.Edits[0].Range.EndLine != 2 {
		t.Fatalf("deletion = %+v", second)
	}
	edits := make([]string, languageFeatureCodeActionEditsMax+1)
	for index := range edits {
		edits[index] = edit(1, 1, 1, 1, "x")
	}
	if response, err = parseLanguageCodeActionResult(json.RawMessage(`{"actions":[`+action("Many", "", edits...)+`]}`), lines); err != nil || len(response.Actions) != 0 || response.Dropped != 1 {
		t.Fatalf("too many edits = %+v, %v", response, err)
	}
	big := edit(1, 1, 1, 1, strings.Repeat("x", languageFeatureCodeActionEditTextMax+1))
	if response, err = parseLanguageCodeActionResult(json.RawMessage(`{"actions":[`+action("Big", "", big)+`]}`), lines); err != nil || len(response.Actions) != 0 {
		t.Fatalf("oversized edit text = %+v, %v", response, err)
	}
	items := make([]string, languageFeatureCodeActionItemsMax+1)
	for index := range items {
		items[index] = action("Fix", "", edit(1, 1, 1, 1, "x"))
	}
	response, err = parseLanguageCodeActionResult(json.RawMessage(`{"actions":[`+strings.Join(items, ",")+`]}`), lines)
	if err != nil || !response.Truncated || len(response.Actions) != languageFeatureCodeActionItemsMax {
		t.Fatalf("truncated = %d, %v", len(response.Actions), err)
	}
	for _, bad := range []string{`{"actions":null}`, `[]`, `{"actions":[],"edit":{}}`} {
		if _, err := parseLanguageCodeActionResult(json.RawMessage(bad), lines); err == nil {
			t.Errorf("%s must be rejected", bad)
		}
	}
}

func TestParseLanguageHoverResult(t *testing.T) {
	response, err := parseLanguageHoverResult(json.RawMessage(`{"contents":["**key**\r\nA key", "", 3, "b\u202e", "ok", "x"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !response.Truncated || len(response.Contents) != 1 || response.Contents[0] != "**key**\nA key" || response.Dropped != 3 {
		t.Fatalf("hover = %+v", response)
	}
	for _, bad := range []string{`{"contents":null}`, `{"contents":[],"x":1}`, `[]`} {
		if _, err := parseLanguageHoverResult(json.RawMessage(bad)); err == nil {
			t.Errorf("%s must be rejected", bad)
		}
	}
}

func TestParseLanguageCompletionResult(t *testing.T) {
	raw := `{"incomplete":true,"items":[
		{"label":"apiVersion","insertText":"apiVersion: ","detail":"string","documentation":"The API\nversion","kind":"property"},
		{"label":"kind"},
		{"label":"","kind":"field"},
		{"label":"x","kind":"weird"},
		{"label":"x","detail":"a\nb"},
		{"label":"x","html":"<b>"}
	]}`
	response, err := parseLanguageCompletionResult(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !response.Incomplete || len(response.Items) != 2 || response.Dropped != 4 || response.Items[0].Kind != "property" {
		t.Fatalf("completion = %+v", response)
	}
	items := make([]string, languageFeatureCompletionItemsMax+1)
	for index := range items {
		items[index] = `{"label":"a"}`
	}
	response, err = parseLanguageCompletionResult(json.RawMessage(`{"items":[` + strings.Join(items, ",") + `]}`))
	if err != nil || !response.Truncated || len(response.Items) != languageFeatureCompletionItemsMax {
		t.Fatalf("truncated = %d, %v", len(response.Items), err)
	}
	if _, err := parseLanguageCompletionResult(json.RawMessage(`{"items":null}`)); err == nil {
		t.Fatal("missing items must be rejected")
	}
}

func TestPluginLanguageFeatureGating(t *testing.T) {
	manager, record := startedContributionManager(t)
	record.mu.Lock()
	record.manifest.contributions = map[string]*resolvedContribution{languageFeaturePointKey: {Entries: []any{LanguageFeatureContribution{
		ID: "docs", Title: "Docs", Languages: []string{"yaml"}, Features: []string{"hover"}, Action: "run",
	}}}}
	record.mu.Unlock()
	valid := func() languageFeatureRequest {
		request := validLanguageFeatureRequest()
		request.PluginID = record.id
		return request
	}
	if _, err := manager.languageFeature(context.Background(), valid()); contributionErrorCode(err) != contributionErrInvalidResult {
		t.Fatalf("dispatch err = %v", err)
	}
	assertNoContributionRequestsLeft(t)
	cases := map[string]struct {
		mutate func(*languageFeatureRequest)
		code   string
	}{
		"unknown plugin":   {func(r *languageFeatureRequest) { r.PluginID = "missing.plugin" }, contributionErrNotFound},
		"unknown provider": {func(r *languageFeatureRequest) { r.ProviderID = "other" }, contributionErrNotFound},
		"other feature":    {func(r *languageFeatureRequest) { r.Feature = "completion" }, contributionErrInvalidRequest},
		"other language":   {func(r *languageFeatureRequest) { r.LanguageID = "json" }, contributionErrInvalidRequest},
		"trigger on hover": {func(r *languageFeatureRequest) { r.TriggerCharacter = "." }, contributionErrInvalidRequest},
	}
	for name, test := range cases {
		request := valid()
		test.mutate(&request)
		if _, err := manager.languageFeature(context.Background(), request); contributionErrorCode(err) != test.code {
			t.Errorf("%s: err = %v, want %s", name, err, test.code)
		}
	}
	assertNoContributionRequestsLeft(t)
}

func TestPluginHostLanguageFeatureHandlerRejectsBadArguments(t *testing.T) {
	for _, args := range []string{`nope`, `{"pluginId":"a","extra":1}`} {
		if _, err := pluginHostLanguageFeatureHandler("", args); contributionErrorCode(err) != contributionErrInvalidRequest {
			t.Errorf("%s: err = %v", args, err)
		}
	}
}

func TestLanguageFeatureHoverPayloadUnchanged(t *testing.T) {
	payload := languageFeaturePayload{
		Provider: "p", Feature: "hover", LanguageID: "json", FileName: "a.json", Text: "{}", StartLine: 1,
		Position: &languageFeaturePosition{Line: 1, Column: 1}, Locale: "en",
	}
	encoded, err := encodeContributionJSON(payload)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"provider":"p","feature":"hover","languageId":"json","fileName":"a.json","text":"{}","startLine":1,"position":{"line":1,"column":1},"locale":"en"}`
	if string(encoded) != want {
		t.Fatalf("hover payload changed:\n got %s\nwant %s", encoded, want)
	}
	completion := payload
	completion.Feature, completion.TriggerCharacter = "completion", "."
	encoded, err = encodeContributionJSON(completion)
	if err != nil {
		t.Fatal(err)
	}
	const wantCompletion = `{"provider":"p","feature":"completion","languageId":"json","fileName":"a.json","text":"{}","startLine":1,"position":{"line":1,"column":1},"triggerCharacter":".","locale":"en"}`
	if string(encoded) != wantCompletion {
		t.Fatalf("completion payload changed:\n got %s\nwant %s", encoded, wantCompletion)
	}
	include := false
	references := payload
	references.Feature, references.IncludeDeclaration = "references", &include
	encoded, _ = encodeContributionJSON(references)
	if !strings.Contains(string(encoded), `"position":{"line":1,"column":1},"includeDeclaration":false,"locale"`) {
		t.Fatalf("references payload = %s", encoded)
	}
	inlay := payload
	inlay.Feature, inlay.Position, inlay.Range = "inlayHints", nil, &languageFeatureRange{StartLine: 1, EndLine: 1}
	encoded, _ = encodeContributionJSON(inlay)
	if !strings.Contains(string(encoded), `"startLine":1,"range":{"startLine":1,"endLine":1},"locale"`) || strings.Contains(string(encoded), "position") {
		t.Fatalf("inlayHints payload = %s", encoded)
	}
}

func TestNormalizeLanguageFeatureRequestPerKind(t *testing.T) {
	yes := true
	whole := func(feature string) languageFeatureRequest {
		request := validLanguageFeatureRequest()
		request.Feature, request.Position = feature, nil
		return request
	}
	withFeature := func(feature string) languageFeatureRequest {
		request := validLanguageFeatureRequest()
		request.Feature = feature
		return request
	}
	valid := map[string]func() languageFeatureRequest{
		"hover":      validLanguageFeatureRequest,
		"completion": func() languageFeatureRequest { return withFeature("completion") },
		"definition": func() languageFeatureRequest { return withFeature("definition") },
		"references": func() languageFeatureRequest {
			r := withFeature("references")
			r.IncludeDeclaration = &yes
			return r
		},
		"codeLens":       func() languageFeatureRequest { return whole("codeLens") },
		"documentSymbol": func() languageFeatureRequest { return whole("documentSymbol") },
		"inlayHints": func() languageFeatureRequest {
			r := whole("inlayHints")
			r.Range = &languageFeatureRange{StartLine: 1, EndLine: 2}
			return r
		},
		"inlayHints window": func() languageFeatureRequest {
			r := whole("inlayHints")
			r.StartLine, r.Range = 40, &languageFeatureRange{StartLine: 41, EndLine: 41}
			return r
		},
	}
	for name, build := range valid {
		request := build()
		if err := normalizeLanguageFeatureRequest(&request); err != nil {
			t.Errorf("%s: valid request rejected: %v", name, err)
		}
	}
	invalid := map[string]func() languageFeatureRequest{
		"codeLens with position": func() languageFeatureRequest {
			r := whole("codeLens")
			r.Position = &languageFeaturePosition{Line: 1, Column: 1}
			return r
		},
		"documentSymbol startLine 5": func() languageFeatureRequest { r := whole("documentSymbol"); r.StartLine = 5; return r },
		"codeLens startLine 2":       func() languageFeatureRequest { r := whole("codeLens"); r.StartLine = 2; return r },
		"codeLens with range": func() languageFeatureRequest {
			r := whole("codeLens")
			r.Range = &languageFeatureRange{StartLine: 1, EndLine: 1}
			return r
		},
		"documentSymbol with include": func() languageFeatureRequest { r := whole("documentSymbol"); r.IncludeDeclaration = &yes; return r },
		"references without include":  func() languageFeatureRequest { return withFeature("references") },
		"hover with include":          func() languageFeatureRequest { r := withFeature("hover"); r.IncludeDeclaration = &yes; return r },
		"definition with include":     func() languageFeatureRequest { r := withFeature("definition"); r.IncludeDeclaration = &yes; return r },
		"hover with range": func() languageFeatureRequest {
			r := withFeature("hover")
			r.Range = &languageFeatureRange{StartLine: 1, EndLine: 1}
			return r
		},
		"hover without position":      func() languageFeatureRequest { return whole("hover") },
		"completion without position": func() languageFeatureRequest { return whole("completion") },
		"definition without position": func() languageFeatureRequest { return whole("definition") },
		"references without position": func() languageFeatureRequest { r := whole("references"); r.IncludeDeclaration = &yes; return r },
		"inlayHints without range":    func() languageFeatureRequest { return whole("inlayHints") },
		"inlayHints with position": func() languageFeatureRequest {
			r := whole("inlayHints")
			r.Range, r.Position = &languageFeatureRange{StartLine: 1, EndLine: 1}, &languageFeaturePosition{Line: 1, Column: 1}
			return r
		},
		"range end past text": func() languageFeatureRequest {
			r := whole("inlayHints")
			r.Range = &languageFeatureRange{StartLine: 1, EndLine: 3}
			return r
		},
		"range start after end": func() languageFeatureRequest {
			r := whole("inlayHints")
			r.Range = &languageFeatureRange{StartLine: 2, EndLine: 1}
			return r
		},
		"range before window": func() languageFeatureRequest {
			r := whole("inlayHints")
			r.StartLine, r.Range = 40, &languageFeatureRange{StartLine: 39, EndLine: 40}
			return r
		},
		"range zero": func() languageFeatureRequest { r := whole("inlayHints"); r.Range = &languageFeatureRange{}; return r },
		"definition position outside": func() languageFeatureRequest {
			r := withFeature("definition")
			r.Position.Line = 3
			return r
		},
		"definition trigger": func() languageFeatureRequest {
			r := withFeature("definition")
			r.TriggerCharacter = "."
			return r
		},
	}
	for name, build := range invalid {
		request := build()
		if err := normalizeLanguageFeatureRequest(&request); contributionErrorCode(err) != contributionErrInvalidRequest {
			t.Errorf("%s: err = %v, want invalid_request", name, err)
		}
	}
}

func TestLanguageFeatureTimeoutPerKind(t *testing.T) {
	cases := []struct {
		feature     string
		timeout     time.Duration
		concurrency int
		group       string
	}{
		{"hover", 2 * time.Second, 2, ""},
		{"completion", 2 * time.Second, 2, ""},
		{"definition", 2 * time.Second, 2, ""},
		{"references", 5 * time.Second, 2, ""},
		{"codeLens", 2 * time.Second, 2, "passive"},
		{"inlayHints", 2 * time.Second, 2, "passive"},
		{"documentSymbol", 2 * time.Second, 2, "passive"},
	}
	for _, test := range cases {
		timeout, concurrency, group := languageFeatureCallShape(test.feature)
		if timeout != test.timeout || concurrency != test.concurrency || group != test.group {
			t.Errorf("%s = %v/%d/%q, want %v/%d/%q", test.feature, timeout, concurrency, group, test.timeout, test.concurrency, test.group)
		}
	}
}

func TestLanguageFeaturePassiveSlotsAreSeparate(t *testing.T) {
	manager, record := startedContributionManager(t)
	record.mu.Lock()
	record.manifest.contributions = map[string]*resolvedContribution{languageFeaturePointKey: {Entries: []any{LanguageFeatureContribution{
		ID: "docs", Title: "Docs", Languages: []string{"yaml"}, Features: []string{"hover", "inlayHints"}, Action: "run",
	}}}}
	record.mu.Unlock()
	passive := func(requestID string) languageFeatureRequest {
		request := validLanguageFeatureRequest()
		request.PluginID, request.RequestID, request.Feature = record.id, requestID, "inlayHints"
		// The helper plugin never answers a payload containing helper-hold.
		request.Text, request.Position, request.Range = "helper-hold", nil, &languageFeatureRange{StartLine: 1, EndLine: 1}
		return request
	}
	held := map[string]chan error{"passive-0001": make(chan error, 1), "passive-0002": make(chan error, 1)}
	for requestID, done := range held {
		go func() {
			_, err := manager.languageFeature(context.Background(), passive(requestID))
			done <- err
		}()
	}
	passiveSlot := record.id + "\x00" + languageFeaturePointKey + "\x00passive"
	deadline := time.Now().Add(5 * time.Second)
	for {
		contributionRequests.mu.Lock()
		taken := contributionRequests.slots[passiveSlot]
		contributionRequests.mu.Unlock()
		if taken == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("passive slots taken = %d, want 2", taken)
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := manager.languageFeature(context.Background(), passive("passive-0003")); contributionErrorCode(err) != contributionErrBusy {
		t.Fatalf("third passive request: err = %v, want busy", err)
	}
	hover := validLanguageFeatureRequest()
	hover.PluginID, hover.RequestID = record.id, "hover-000001"
	hover.Text, hover.Position = "helper-hover", &languageFeaturePosition{Line: 1, Column: 1}
	response, err := manager.languageFeature(context.Background(), hover)
	if err != nil || len(response.Contents) != 1 || response.Contents[0] != "ok" {
		t.Fatalf("hover while passive slots are full = %+v, %v", response, err)
	}
	// Each held request releases its slot exactly once: one by cancel, one by plugin stop.
	if !cancelContributionRequest("passive-0001") {
		t.Fatal("passive-0001 is not in flight")
	}
	if err := <-held["passive-0001"]; contributionErrorCode(err) != contributionErrCancelled {
		t.Fatalf("cancelled passive: err = %v", err)
	}
	cancelPluginContributionRequests(record.id)
	if err := <-held["passive-0002"]; contributionErrorCode(err) != contributionErrUnavailable {
		t.Fatalf("stopped passive: err = %v", err)
	}
	assertNoContributionRequestsLeft(t)
}

func TestLanguageFeatureNewKindResultFailsClosed(t *testing.T) {
	manager, record := startedContributionManager(t)
	record.mu.Lock()
	record.manifest.contributions = map[string]*resolvedContribution{languageFeaturePointKey: {Entries: []any{LanguageFeatureContribution{
		ID: "docs", Title: "Docs", Languages: []string{"yaml"}, Features: []string{"definition", "documentSymbol"}, Action: "run",
	}}}}
	record.mu.Unlock()
	request := validLanguageFeatureRequest()
	request.PluginID, request.Feature = record.id, "definition"
	// The helper answers {"ok":true}: each kind parses its own shape and
	// fails closed, never falling through to another kind's parser.
	if _, err := manager.languageFeature(context.Background(), request); contributionErrorCode(err) != contributionErrInvalidResult || !strings.Contains(err.Error(), `{"locations": [...]}`) {
		t.Fatalf("definition result: err = %v", err)
	}
	symbols := validLanguageFeatureRequest()
	symbols.PluginID, symbols.Feature, symbols.Position = record.id, "documentSymbol", nil
	if _, err := manager.languageFeature(context.Background(), symbols); contributionErrorCode(err) != contributionErrInvalidResult || !strings.Contains(err.Error(), `{"symbols": [...]}`) {
		t.Fatalf("documentSymbol result: err = %v", err)
	}
	assertNoContributionRequestsLeft(t)
}
