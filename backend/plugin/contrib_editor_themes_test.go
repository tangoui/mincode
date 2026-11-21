package plugin

import (
	"encoding/json"
	"strings"
	"testing"
)

func decodeEditorThemeForTest(t *testing.T, mutate func(map[string]any)) (EditorThemeContribution, error) {
	t.Helper()
	entry := map[string]any{
		"id": "acme.night", "title": "Night", "titleLocalized": map[string]string{"zh": "夜"}, "kind": "dark",
		"colors": map[string]any{
			"editor.background": "#1E1E1E", "editor.foreground": "#d4d4d4", "editorCursor.foreground": "#fff",
			"editor.selectionBackground": "#264f78", "editorLineNumber.foreground": "#858585",
		},
		"rules": []any{
			map[string]any{"token": "comment", "foreground": "6A9955", "fontStyle": "italic "},
			map[string]any{"token": "keyword", "foreground": "569cd6"},
			map[string]any{"token": "string", "foreground": "ce9178", "background": "1e1e1e"},
			map[string]any{"token": ""},
		},
	}
	if mutate != nil {
		mutate(entry)
	}
	raw, _ := json.Marshal(entry)
	ctx := &pointContext{manifest: &Manifest{ID: "acme", APIVersion: 2}, spec: editorThemeSpec}
	value, _, err := decodeEditorThemeEntry(ctx, 0, "", raw)
	if err != nil {
		return EditorThemeContribution{}, err
	}
	return value.(EditorThemeContribution), nil
}

func TestEditorThemeDecode(t *testing.T) {
	theme, err := decodeEditorThemeForTest(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if theme.ID != "acme.night" || theme.Kind != "dark" || theme.TitleLocalized["zh"] != "夜" {
		t.Fatalf("theme = %+v", theme)
	}
	if theme.Colors["editor.background"] != "#1e1e1e" || theme.Colors["editorCursor.foreground"] != "#ffffff" {
		t.Fatalf("colors not normalized: %v", theme.Colors)
	}
	if len(theme.Rules) != 4 || theme.Rules[0].Foreground != "6a9955" || theme.Rules[0].FontStyle != "italic" {
		t.Fatalf("rules = %+v", theme.Rules)
	}
	// A translucent selection is composited on the background; opaque #rrggbbff is accepted.
	if _, err := decodeEditorThemeForTest(t, func(entry map[string]any) {
		colors := entry["colors"].(map[string]any)
		colors["editor.selectionBackground"] = "#264f7880"
		colors["editor.background"] = "#1e1e1eff"
	}); err != nil {
		t.Fatalf("translucent selection: %v", err)
	}
	// Rules and every optional color may be omitted.
	if theme, err := decodeEditorThemeForTest(t, func(entry map[string]any) {
		entry["colors"] = map[string]any{"editor.background": "#ffffff", "editor.foreground": "#1f2328"}
		entry["kind"] = "light"
		delete(entry, "rules")
	}); err != nil || theme.Rules != nil {
		t.Fatalf("minimal theme: %+v %v", theme, err)
	}
}

func TestEditorThemeRejects(t *testing.T) {
	color := func(key string, value any) func(map[string]any) {
		return func(entry map[string]any) { entry["colors"].(map[string]any)[key] = value }
	}
	rule := func(value map[string]any) func(map[string]any) {
		return func(entry map[string]any) { entry["rules"] = []any{value} }
	}
	cases := map[string]struct {
		mutate func(map[string]any)
		want   string
	}{
		"foreign id":          {func(entry map[string]any) { entry["id"] = "other.night" }, "must be"},
		"bad name":            {func(entry map[string]any) { entry["id"] = "acme.Night" }, "must be"},
		"kind":                {func(entry map[string]any) { entry["kind"] = "hc" }, "kind"},
		"missing background":  {func(entry map[string]any) { delete(entry["colors"].(map[string]any), "editor.background") }, "missing required color"},
		"unknown color":       {color("terminal.background", "#000000"), "unsupported key"},
		"named color":         {color("editor.foreground", "white"), "hex"},
		"css function":        {color("editor.foreground", "rgb(1,2,3)"), "hex"},
		"translucent base":    {color("editor.background", "#1e1e1e80"), "opaque"},
		"low foreground":      {color("editor.foreground", "#3a3a3a"), "contrast of editor.foreground on editor.background"},
		"hidden cursor":       {color("editorCursor.foreground", "#252525"), "editorCursor.foreground"},
		"hidden selection":    {color("editor.selectionBackground", "#d4d4d4"), "editor.selectionBackground"},
		"hidden comment":      {rule(map[string]any{"token": "comment", "foreground": "2a2a2a"}), "rules[0]"},
		"hidden on rule bg":   {rule(map[string]any{"token": "string", "foreground": "d4d4d4", "background": "cccccc"}), "its background"},
		"rule hash color":     {rule(map[string]any{"token": "comment", "foreground": "#6a9955"}), "without '#'"},
		"rule bad token":      {rule(map[string]any{"token": "a b"}), "token"},
		"rule font style":     {rule(map[string]any{"token": "comment", "fontStyle": "oblique"}), "fontStyle"},
		"rule extra field":    {rule(map[string]any{"token": "comment", "fontFamily": "x"}), "parse"},
		"font field":          {func(entry map[string]any) { entry["fontFamily"] = "x" }, "parse"},
		"control title":       {func(entry map[string]any) { entry["title"] = "a\u0007" }, "title"},
		"colors not object":   {func(entry map[string]any) { entry["colors"] = nil }, "colors"},
		"too many rules":      {func(entry map[string]any) { entry["rules"] = make([]any, editorThemeRulesMax+1) }, ""},
		"css injection value": {color("editor.background", "#000;}body{x"), "hex"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := decodeEditorThemeForTest(t, tc.mutate)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestEditorThemeSharesMonacoAllowlistWithThemes(t *testing.T) {
	if len(editorThemeSpec.MonacoColors) != len(themeSpec.MonacoColors) {
		t.Fatalf("editorThemes monacoColors differs from themes")
	}
	for index, id := range themeSpec.MonacoColors {
		if editorThemeSpec.MonacoColors[index] != id {
			t.Fatalf("monacoColors[%d] = %q, want %q", index, editorThemeSpec.MonacoColors[index], id)
		}
	}
}
