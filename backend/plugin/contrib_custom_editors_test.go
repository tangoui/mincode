package plugin

import (
	"encoding/json"
	"strings"
	"testing"

	"mincode/app/plugins/api"
)

func customEditorTestContext(capabilities ...api.Capability) *pointContext {
	return &pointContext{
		manifest: &Manifest{ID: "draw.plugin", APIVersion: 2, Icon: "box", Capabilities: capabilities},
		spec:     customEditorSpec,
		views: map[string]ManifestView{
			"canvas": {ID: "canvas", Title: "Canvas", TitleLocalized: map[string]string{"zh": "画布"}, Entry: "ui/index.html"},
		},
	}
}

func decodeCustomEditorForTest(t *testing.T, ctx *pointContext, raw string) (CustomEditorContribution, error) {
	t.Helper()
	value, _, err := decodeCustomEditorEntry(ctx, 0, "", json.RawMessage(raw))
	if err != nil {
		return CustomEditorContribution{}, err
	}
	return value.(CustomEditorContribution), nil
}

func TestCustomEditorDecodeDefaultsAndSnapshot(t *testing.T) {
	editor, err := decodeCustomEditorForTest(t, customEditorTestContext(), `{"id":"drawio","viewId":"canvas","filenamePatterns":["*.drawio","docs/**/*.dio"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if editor.Priority != "option" || editor.Access != "read" || editor.Title != "Canvas" || editor.TitleLocalized["zh"] != "画布" || editor.Icon != "box" {
		t.Fatalf("unexpected snapshot %+v", editor)
	}
	editor, err = decodeCustomEditorForTest(t, customEditorTestContext(), `{"id":"drawio","viewId":"canvas","title":"Draw","titleLocalized":{"zh":"绘图"},"filenamePatterns":["*.drawio"],"priority":"default"}`)
	if err != nil || editor.Title != "Draw" || editor.TitleLocalized["zh"] != "绘图" || editor.Priority != "default" {
		t.Fatalf("editor=%+v err=%v", editor, err)
	}
}

func TestCustomEditorDecodeRejectsInvalidEntries(t *testing.T) {
	cases := map[string]string{
		"unknown view":     `{"id":"a","viewId":"missing","filenamePatterns":["*.drawio"]}`,
		"bad id":           `{"id":"A","viewId":"canvas","filenamePatterns":["*.drawio"]}`,
		"no patterns":      `{"id":"a","viewId":"canvas","filenamePatterns":[]}`,
		"unknown field":    `{"id":"a","viewId":"canvas","filenamePatterns":["*.drawio"],"extra":1}`,
		"bad priority":     `{"id":"a","viewId":"canvas","filenamePatterns":["*.drawio"],"priority":"always"}`,
		"bad access":       `{"id":"a","viewId":"canvas","filenamePatterns":["*.drawio"],"access":"write"}`,
		"duplicate":        `{"id":"a","viewId":"canvas","filenamePatterns":["*.drawio","*.DRAWIO"]}`,
		"title too long":   `{"id":"a","viewId":"canvas","title":"` + strings.Repeat("x", 65) + `","filenamePatterns":["*.drawio"]}`,
		"multiline title":  `{"id":"a","viewId":"canvas","title":"a\nb","filenamePatterns":["*.drawio"]}`,
		"too many locales": `{"id":"a","viewId":"canvas","titleLocalized":{` + manyLocales(17) + `},"filenamePatterns":["*.drawio"]}`,
	}
	for name, raw := range cases {
		if _, err := decodeCustomEditorForTest(t, customEditorTestContext(), raw); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
}

func manyLocales(count int) string {
	parts := make([]string, count)
	for index := range parts {
		parts[index] = `"l` + strings.Repeat("x", index+1) + `":"t"`
	}
	return strings.Join(parts, ",")
}

func TestCustomEditorPatternBreadthRules(t *testing.T) {
	accepted := []string{"*.drawio", "*.excalidraw.json", "docs/**/*.bpmn", "Dockerfile", "config/app.yaml", "file-?.jfr", "a+b@c.x", "**/*.pprof"}
	for _, pattern := range accepted {
		if err := validateCustomEditorPattern(pattern); err != nil {
			t.Errorf("%q rejected: %v", pattern, err)
		}
	}
	rejected := []string{"", "*", "**", "**/*", "*.*", "*.?", ".*", "*.", "*x", "*.{a,b}", "[ab].x", "!x.md", `a\b.md`, "a b.md", "/abs.md", "../x.md", "a/../b.md", "dir/", "a//b.md", "./a.md", "*.md/*", "docs/*", strings.Repeat("a", 129)}
	for _, pattern := range rejected {
		if err := validateCustomEditorPattern(pattern); err == nil {
			t.Errorf("%q accepted", pattern)
		}
	}
}

func TestCustomEditorPatternMatchingMirrorsFrontend(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"*.drawio", "a/b/Chart.DRAWIO", true},
		{"*.drawio", "chart.drawio.bak", false},
		{"docs/**/*.bpmn", "docs/x/y/flow.bpmn", true},
		{"docs/**/*.bpmn", "src/docs/flow.bpmn", false},
		{"docs/*.bpmn", "docs/x/flow.bpmn", false},
		{"file-?.jfr", "file-1.jfr", true},
		{"Dockerfile", "svc/dockerfile", true},
	}
	for _, testCase := range cases {
		if got := customEditorPatternMatches(testCase.pattern, testCase.path); got != testCase.want {
			t.Errorf("%q vs %q = %v", testCase.pattern, testCase.path, got)
		}
	}
}

func TestCustomEditorDefaultMayNotClaimSensitiveFiles(t *testing.T) {
	if len(customEditorSpec.SensitiveFilenames) == 0 {
		t.Fatal("contract lists no sensitiveFilenames")
	}
	for _, pattern := range []string{"*.pem", ".env", "**/*.key", "server.key"} {
		raw := `{"id":"a","viewId":"canvas","filenamePatterns":["` + pattern + `"],"priority":"default"}`
		_, err := decodeCustomEditorForTest(t, customEditorTestContext(), raw)
		if err == nil || !strings.Contains(err.Error(), "sensitive") {
			t.Errorf("default %q: err=%v", pattern, err)
		}
		// The same pattern stays available as an explicit option.
		raw = `{"id":"a","viewId":"canvas","filenamePatterns":["` + pattern + `"],"priority":"option"}`
		if _, err := decodeCustomEditorForTest(t, customEditorTestContext(), raw); err != nil {
			t.Errorf("option %q rejected: %v", pattern, err)
		}
	}
}

func TestCustomEditorReadWriteRequiresWorkspaceWrite(t *testing.T) {
	raw := `{"id":"a","viewId":"canvas","filenamePatterns":["*.drawio"],"access":"readWrite"}`
	if _, err := decodeCustomEditorForTest(t, customEditorTestContext(api.CapabilityWorkspaceRead), raw); err == nil || !strings.Contains(err.Error(), "workspace.write") {
		t.Fatalf("readWrite without workspace.write: %v", err)
	}
	editor, err := decodeCustomEditorForTest(t, customEditorTestContext(api.CapabilityWorkspaceWrite), raw)
	if err != nil || editor.Access != "readWrite" {
		t.Fatalf("editor=%+v err=%v", editor, err)
	}
}

func TestCustomEditorsExpandPermission(t *testing.T) {
	entry := func(id, priority, access string, patterns ...string) CustomEditorContribution {
		return CustomEditorContribution{ID: id, Priority: priority, Access: access, FilenamePatterns: patterns}
	}
	resolved := func(entries ...CustomEditorContribution) *resolvedContribution {
		out := &resolvedContribution{}
		for _, editor := range entries {
			out.Entries = append(out.Entries, editor)
		}
		return out
	}
	current := resolved(entry("a", "default", "read", "*.drawio"))
	if customEditorsExpandPermission(current, resolved(entry("a", "default", "read", "*.drawio"), entry("b", "option", "read", "*.x1"))) {
		t.Fatal("new option editor must not require approval")
	}
	if !customEditorsExpandPermission(current, resolved(entry("a", "default", "readWrite", "*.drawio"))) {
		t.Fatal("read -> readWrite must require approval")
	}
	if !customEditorsExpandPermission(current, resolved(entry("a", "default", "read", "*.drawio", "*.dio"))) {
		t.Fatal("a new default pattern must require approval")
	}
	if customEditorsExpandPermission(current, resolved()) {
		t.Fatal("removing editors must not require approval")
	}
}
