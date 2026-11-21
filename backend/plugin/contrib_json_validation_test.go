package plugin

import (
	"encoding/json"
	"strings"
	"testing"
)

func writeJSONValidationPackage(t *testing.T, entries []any, files map[string]string) string {
	t.Helper()
	return writeKeymapPackage(t, "", func(manifest map[string]any) {
		manifest["id"] = "acme-schemas"
		manifest["contributes"] = map[string]any{"jsonValidation": entries}
	}, nil, files)
}

func TestJSONValidationSnapshotCarriesAssetKeys(t *testing.T) {
	useTemporaryPluginData(t)
	schema := "\xef\xbb\xbf" + `{"$schema":"http://json-schema.org/draft-07/schema#","$id":"https://example.com/x","id":"legacy","type":"object",
	  "properties":{"name":{"type":"string","pattern":"^[a-z]+$","description":"<b>"}},"patternProperties":{"^x-":{}},
	  "definitions":{"a":{"$ref":"#/properties/name"}}}`
	root := writeJSONValidationPackage(t, []any{
		map[string]any{"fileMatch": []string{".mcp.json", "**/.cursor/mcp.json"}, "schema": "schemas/mcp.schema.json", "title": " MCP ", "titleLocalized": map[string]string{"zh": "MCP 服务器"}},
		map[string]any{"fileMatch": []string{"*.acme.jsonc"}, "schema": "assets/acme.json"},
	}, map[string]string{"schemas/mcp.schema.json": schema, "assets/acme.json": `{"type":"object"}`})
	inspection, err := inspectInstallDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	resolved := inspection.Manifest.contribution(jsonValidationPointKey)
	if resolved == nil || len(resolved.Entries) != 2 || len(resolved.Issues) != 0 || len(resolved.Assets) != 2 {
		t.Fatalf("resolved = %+v", resolved)
	}
	first := resolved.Entries[0].(JSONValidationContribution)
	if first.ID != "schemas/mcp.schema.json" || first.Title != "MCP" || first.TitleLocalized["zh"] != "MCP 服务器" || len(first.FileMatch) != 2 || len(first.Schema) != 64 {
		t.Fatalf("entry = %+v", first)
	}
	asset := resolved.Assets[first.Schema]
	if asset.Mime != jsonValidationMime {
		t.Fatalf("asset = %+v", asset)
	}
	canonical, _, err := validateJSONValidationAsset(jsonValidationAssetHint, ".json", []byte(schema))
	if err != nil {
		t.Fatal(err)
	}
	text := string(canonical)
	if strings.Contains(text, "example.com/x") || strings.Contains(text, "legacy") || !strings.HasPrefix(text, `{"$schema":`) || !strings.Contains(text, `"<b>"`) {
		t.Fatalf("canonical = %s", text)
	}
	if contributionAssetKey(canonical) != first.Schema {
		t.Fatal("snapshot key must be the canonical asset hash")
	}
	snapshot, _ := json.Marshal(resolved.Entries)
	if strings.Contains(string(snapshot), "pattern") {
		t.Fatalf("snapshot must not inline the schema: %s", snapshot)
	}
}

func TestJSONValidationPatternRules(t *testing.T) {
	accepted := []string{".mcp.json", "**/.cursor/mcp.json", "*.mcdemo.json", "config/acme-settings.jsonc", "tsconfig.*.json", "abc.JSON"}
	for _, pattern := range accepted {
		if err := validateJSONValidationPattern(pattern); err != nil {
			t.Errorf("%q: %v", pattern, err)
		}
	}
	rejected := map[string]string{
		"*.json":                           "too broad",
		"**/*.json":                        "too broad",
		"*.jsonc":                          "too broad",
		"?.json":                           "too broad",
		"ab*.json":                         "too broad",
		"package.yaml":                     ".json or .jsonc",
		"/abs/mcp.json":                    "relative path",
		"a//mcp.json":                      "relative path",
		"../mcp.json":                      "..",
		"./mcp.json":                       ". segments",
		"{a,b}.json":                       "may only use",
		"":                                 "is empty",
		strings.Repeat("a", 125) + ".json": "exceeds 128",
	}
	for pattern, want := range rejected {
		err := validateJSONValidationPattern(pattern)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", pattern, err, want)
		}
	}
}

func TestJSONValidationAssetRules(t *testing.T) {
	deep := strings.Repeat(`{"a":`, jsonValidationMaxDepth+1) + "1" + strings.Repeat("}", jsonValidationMaxDepth+1)
	var builder strings.Builder
	builder.WriteString(`{"anyOf":[`)
	for index := 0; index <= jsonValidationRegexMax; index++ {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(`{"pattern":"x"}`)
	}
	builder.WriteString(`]}`)
	cases := map[string]struct {
		ext, data, want string
	}{
		"array root":      {".json", `[]`, "root must be an object"},
		"remote ref":      {".json", `{"$ref":"https://example.com/s.json"}`, "same-document"},
		"relative ref":    {".json", `{"items":{"$ref":"other.json#/a"}}`, "same-document"},
		"dynamic ref":     {".json", `{"$dynamicRef":"http://x"}`, "same-document"},
		"nested id":       {".json", `{"properties":{"a":{"$id":"urn:x"}}}`, "nested $id"},
		"long schema uri": {".json", `{"$schema":"` + strings.Repeat("a", 257) + `"}`, "$schema exceeds"},
		"long pattern":    {".json", `{"pattern":"` + strings.Repeat("a", 1025) + `"}`, "pattern exceeds"},
		"long key regex":  {".json", `{"patternProperties":{"` + strings.Repeat("a", 1025) + `":{}}}`, "patternProperties key exceeds"},
		"too many regex":  {".json", builder.String(), "more than 1024 patterns"},
		"too deep":        {".json", deep, "nested deeper"},
		"comments":        {".json", "{/* c */}", "invalid JSON"},
		"duplicate keys":  {".json", `{"a":1,"a":2}`, "duplicate key"},
		"trailing":        {".json", `{} {}`, "trailing"},
		"wrong ext":       {".jsonc", `{}`, ".json file"},
	}
	for name, test := range cases {
		_, _, err := validateJSONValidationAsset(jsonValidationAssetHint, test.ext, []byte(test.data))
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: err = %v, want %q", name, err, test.want)
		}
	}
	// Non-keyword positions are data, not keywords.
	for _, data := range []string{
		`{"properties":{"$ref":{"type":"string"},"pattern":{"type":"string"}}}`,
		`{"enum":["$ref","https://example.com"]}`,
		`{"properties":{"a":{"id":"nested-id-keyword-is-fine"}}}`,
	} {
		if _, _, err := validateJSONValidationAsset(jsonValidationAssetHint, ".json", []byte(data)); err != nil {
			t.Errorf("%s: %v", data, err)
		}
	}
	nodes := `{"enum":[` + strings.TrimSuffix(strings.Repeat("0,", jsonValidationMaxNodes), ",") + `]}`
	if _, _, err := validateJSONValidationAsset(jsonValidationAssetHint, ".json", []byte(nodes)); err == nil || !strings.Contains(err.Error(), "nodes") {
		t.Fatalf("nodes err = %v", err)
	}
}

func TestJSONValidationEntryIssues(t *testing.T) {
	useTemporaryPluginData(t)
	files := map[string]string{"schemas/a.json": `{}`, "other/b.json": `{}`}
	cases := map[string]struct {
		entry map[string]any
		want  string
	}{
		"no patterns":    {map[string]any{"fileMatch": []string{}, "schema": "schemas/a.json"}, "fileMatch needs 1-8"},
		"repeat pattern": {map[string]any{"fileMatch": []string{"abc.json", "ABC.json"}, "schema": "schemas/a.json"}, "repeats"},
		"unknown key":    {map[string]any{"fileMatch": []string{"abc.json"}, "schema": "schemas/a.json", "url": "x"}, "unknown field"},
		"missing schema": {map[string]any{"fileMatch": []string{"abc.json"}}, "schema is required"},
		"outside dirs":   {map[string]any{"fileMatch": []string{"abc.json"}, "schema": "other/b.json"}, "under schemas/ or assets/"},
		"bidi title":     {map[string]any{"fileMatch": []string{"abc.json"}, "schema": "schemas/a.json", "title": "a\u202eb"}, "title contains"},
		"missing file":   {map[string]any{"fileMatch": []string{"abc.json"}, "schema": "schemas/none.json"}, "schemas/none.json"},
	}
	for name, test := range cases {
		root := writeJSONValidationPackage(t, []any{test.entry}, files)
		_, err := inspectInstallDirectory(root)
		if err == nil || !strings.Contains(err.Error(), test.want) || !strings.Contains(err.Error(), "invalid jsonValidation contribution") {
			t.Errorf("%s: err = %v, want %q", name, err, test.want)
		}
	}
	root := writeJSONValidationPackage(t, []any{
		map[string]any{"fileMatch": []string{"abc.json"}, "schema": "schemas/a.json"},
		map[string]any{"fileMatch": []string{"def.json"}, "schema": "schemas/a.json"},
	}, files)
	if _, err := inspectInstallDirectory(root); err == nil || !strings.Contains(err.Error(), "duplicate jsonValidation schema") {
		t.Fatalf("duplicate err = %v", err)
	}
}
