package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func themeTestColors() map[string]any {
	return map[string]any{
		"canvas": "#f2f3f5", "surface": "#ffffff", "sidebar": "#ffffff", "border": "#e5e6eb",
		"border-strong": "#c9cdd4", "muted": "#f7f8fa", "hover": "#f2f3f5", "accent": "#3a64d8",
		"accent-hover": "#5d87ff", "accent-soft": "#eef2ff", "text": "#1d2129", "text-regular": "#4e5969",
		"text-muted": "#6b7480", "text-disabled": "#c9cdd4", "text-on-accent": "#ffffff",
	}
}

func themeTestEntry(id string) map[string]any {
	return map[string]any{
		"id": id, "title": "Test", "titleLocalized": map[string]string{"zh": "测试"}, "kind": "light",
		"colors": themeTestColors(),
		"editor": map[string]any{
			"colors": map[string]string{"editor.background": "#ffffff", "editorCursor.foreground": "#3a64d8cc"},
			"rules":  []map[string]string{{"token": "comment", "foreground": "6b7480", "fontStyle": "italic underline"}, {"token": ""}},
		},
		"terminal": map[string]string{"background": "#fff", "brightBlue": "#3a64d8"},
	}
}

func writeThemePackage(t *testing.T, mutate func(map[string]any), themes []any, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manifest := map[string]any{
		"apiVersion": 2, "id": "theme-test", "name": "Theme Test", "version": "1.0.0",
		"category": "development", "platforms": []string{runtime.GOOS},
		"runtime":     map[string]any{"type": "declarative"},
		"contributes": map[string]any{"themes": themes},
	}
	if mutate != nil {
		mutate(manifest)
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, manifestFilename), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

// readManifestStrict applies the install-time rule: contribution issues are fatal.
func readManifestStrict(root string) (Manifest, error) {
	manifest, err := readManifest(root)
	if err != nil {
		return manifest, err
	}
	return manifest, manifest.contributionIssuesError()
}

func themeJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestThemeContributionInlineAndFileReferences(t *testing.T) {
	root := writeThemePackage(t, nil,
		[]any{themeTestEntry("theme-test.inline"), map[string]string{"path": "themes/a.json"}, map[string]string{"path": "assets/b.json"}},
		map[string]string{
			"themes/a.json": themeJSON(t, themeTestEntry("theme-test.file")),
			"assets/b.json": themeJSON(t, themeTestEntry("theme-test.asset")),
		})
	manifest, err := readManifestStrict(root)
	if err != nil {
		t.Fatal(err)
	}
	themes := manifest.ThemeContributions()
	if len(themes) != 3 || themes[1].ID != "theme-test.file" || themes[2].ID != "theme-test.asset" {
		t.Fatalf("themes = %+v", themes)
	}
	if themes[0].Editor == nil || themes[0].Editor.Rules[0].FontStyle != "italic underline" || themes[0].TitleLocalized["zh"] != "测试" {
		t.Fatalf("theme fields lost: %+v", themes[0])
	}
}

func TestThemeContributionValidationRejects(t *testing.T) {
	cases := map[string]struct {
		mutate func(map[string]any)
		want   string
	}{
		"foreign id":      {func(e map[string]any) { e["id"] = "other.theme" }, `must start with "theme-test."`},
		"bare prefix":     {func(e map[string]any) { e["id"] = "theme-test." }, "must start with"},
		"bad kind":        {func(e map[string]any) { e["kind"] = "sepia" }, "kind must be"},
		"empty title":     {func(e map[string]any) { e["title"] = " " }, "title must be"},
		"missing token":   {func(e map[string]any) { delete(e["colors"].(map[string]any), "text") }, `missing required token "text"`},
		"unknown token":   {func(e map[string]any) { e["colors"].(map[string]any)["font"] = "#ffffff" }, `unsupported key "font"`},
		"named color":     {func(e map[string]any) { e["colors"].(map[string]any)["surface"] = "white" }, "must be a hex color"},
		"css injection":   {func(e map[string]any) { e["colors"].(map[string]any)["surface"] = "#fff;background:url(x)" }, "must be a hex color"},
		"five hex digits": {func(e map[string]any) { e["colors"].(map[string]any)["surface"] = "#fffff" }, "must be a hex color"},
		"font field":      {func(e map[string]any) { e["fontFamily"] = "serif" }, "fontFamily"},
		"monaco key": {func(e map[string]any) {
			e["editor"].(map[string]any)["colors"] = map[string]string{"editor.fontSize": "#ffffff"}
		}, `unsupported key "editor.fontSize"`},
		"terminal key": {func(e map[string]any) { e["terminal"] = map[string]string{"fontFamily": "#ffffff"} }, `unsupported key "fontFamily"`},
		"rule with hash": {func(e map[string]any) {
			e["editor"].(map[string]any)["rules"] = []map[string]string{{"token": "a", "foreground": "#ffffff"}}
		}, "without '#'"},
		"rule font style": {func(e map[string]any) {
			e["editor"].(map[string]any)["rules"] = []map[string]string{{"token": "a", "fontStyle": "oblique"}}
		}, "fontStyle"},
		"rule font family": {func(e map[string]any) {
			e["editor"].(map[string]any)["rules"] = []map[string]any{{"token": "a", "fontFamily": "x"}}
		}, "fontFamily"},
		"rule token chars": {func(e map[string]any) { e["editor"].(map[string]any)["rules"] = []map[string]string{{"token": "a b"}} }, "token"},
		"low text":         {func(e map[string]any) { e["colors"].(map[string]any)["text"] = "#999999" }, "contrast of text on surface"},
		"low muted":        {func(e map[string]any) { e["colors"].(map[string]any)["text-muted"] = "#c0c0c0" }, "contrast of text-muted on surface"},
		"low on accent":    {func(e map[string]any) { e["colors"].(map[string]any)["text-on-accent"] = "#7090ee" }, "contrast of text-on-accent on accent"},
		"low danger":       {func(e map[string]any) { e["colors"].(map[string]any)["danger"] = "#ffb0b0" }, "contrast of danger on surface"},
		"translucent text": {func(e map[string]any) { e["colors"].(map[string]any)["text"] = "#1d212940" }, "contrast of text on surface"},
		"default danger": {func(e map[string]any) {
			e["kind"] = "dark"
			e["colors"].(map[string]any)["surface"] = "#6b2020"
			e["colors"].(map[string]any)["text"] = "#ffffff"
			e["colors"].(map[string]any)["text-muted"] = "#e0e0e0"
		}, "contrast of danger on surface"},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			entry := themeTestEntry("theme-test.case")
			testCase.mutate(entry)
			root := writeThemePackage(t, nil, []any{entry}, nil)
			if _, err := readManifestStrict(root); err == nil || !strings.Contains(err.Error(), testCase.want) || !strings.Contains(err.Error(), "invalid theme contribution") {
				t.Fatalf("err = %v, want %q", err, testCase.want)
			}
			manifest, err := readManifest(root)
			if err != nil {
				t.Fatalf("lenient load failed: %v", err)
			}
			if len(manifest.ThemeContributions()) != 0 || len(manifest.contribution(themePointKey).Issues) != 1 {
				t.Fatalf("lenient load kept an invalid theme: %+v", manifest.contribution(themePointKey))
			}
		})
	}
}

func TestThemeContributionLimitsAndReferences(t *testing.T) {
	rules := make([]map[string]string, themeRulesMax+1)
	for index := range rules {
		rules[index] = map[string]string{"token": "a"}
	}
	tooMany := themeTestEntry("theme-test.rules")
	tooMany["editor"].(map[string]any)["rules"] = rules
	if _, err := readManifestStrict(writeThemePackage(t, nil, []any{tooMany}, nil)); err == nil || !strings.Contains(err.Error(), "exceeds 500 rules") {
		t.Fatalf("rules limit err = %v", err)
	}
	duplicate := []any{themeTestEntry("theme-test.same"), themeTestEntry("theme-test.same")}
	if _, err := readManifestStrict(writeThemePackage(t, nil, duplicate, nil)); err == nil || !strings.Contains(err.Error(), `duplicate theme id "theme-test.same"`) {
		t.Fatalf("duplicate err = %v", err)
	}
	for name, reference := range map[string]map[string]any{
		"extra field": {"path": "themes/a.json", "x": 1},
		"wrong dir":   {"path": "keymaps/a.json"},
		"wrong ext":   {"path": "themes/a.css"},
		"escape":      {"path": "../a.json"},
		"missing":     {"path": "themes/missing.json"},
	} {
		files := map[string]string{"themes/a.json": themeJSON(t, themeTestEntry("theme-test.a")), "keymaps/a.json": "{}", "themes/a.css": "{}"}
		if _, err := readManifestStrict(writeThemePackage(t, nil, []any{reference}, files)); err == nil {
			t.Fatalf("%s: reference accepted", name)
		}
	}
	// Themes are data for both runtime kinds.
	process := writeThemePackage(t, func(manifest map[string]any) {
		manifest["runtime"] = map[string]any{"type": "process", "entry": "bin/run"}
	}, []any{themeTestEntry("theme-test.proc")}, map[string]string{"bin/run": "#!/bin/sh\n"})
	manifest, err := readManifestStrict(process)
	if err != nil || len(manifest.ThemeContributions()) != 1 {
		t.Fatalf("process plugin theme: %v", err)
	}
}

func TestThemeContrastRatio(t *testing.T) {
	black, _ := parseThemeColor("#000")
	white, _ := parseThemeColor("#ffffffff")
	if ratio := themeContrastRatio(black, white); ratio < 20.99 || ratio > 21.01 {
		t.Fatalf("black/white = %v", ratio)
	}
	gray, _ := parseThemeColor("#767676")
	if ratio := themeContrastRatio(gray, white); ratio < 4.5 || ratio > 4.6 {
		t.Fatalf("#767676/white = %v", ratio)
	}
	half, _ := parseThemeColor("#00000080")
	if composite := half.over(white); composite.r < 0.49 || composite.r > 0.51 {
		t.Fatalf("composite = %+v", composite)
	}
}

// The official themes package must validate strictly, ship only generic
// theme ids, and every theme must clear the contrast floors.
func TestOfficialThemesPluginValidatesWithoutIssues(t *testing.T) {
	root := filepath.Join("..", "..", "official-plugins", "themes")
	manifest, err := readManifestStrict(root)
	if err != nil {
		t.Fatalf("official themes plugin: %v", err)
	}
	if !manifest.isDeclarative() || manifest.Version != "0.2.0" || !containsString(manifest.Tags, "theme") {
		t.Fatalf("official themes plugin must be declarative 0.2.0 and tagged theme: %+v", manifest)
	}
	themes := manifest.ThemeContributions()
	ids := make([]string, 0, len(themes))
	kinds := map[string]int{}
	for _, theme := range themes {
		ids = append(ids, theme.ID)
		kinds[theme.Kind]++
		if theme.Editor == nil || len(theme.Editor.Rules) == 0 || len(theme.Terminal) < 16 || theme.TitleLocalized["zh"] == "" {
			t.Fatalf("theme %s must ship editor rules, a full terminal palette and a zh title", theme.ID)
		}
		for token := range themeKnownToken {
			if _, ok := theme.Colors[token]; !ok {
				t.Fatalf("theme %s should define every token, missing %q", theme.ID, token)
			}
		}
	}
	want := "themes.nightfall,themes.forest,themes.dusk,themes.sepia,themes.mint,themes.dracula,themes.nord,themes.tokyo-night,themes.catppuccin-mocha,themes.gruvbox-dark,themes.one-dark,themes.solarized-light,themes.github-light,themes.catppuccin-latte,themes.solarized-dark,themes.gruvbox-light"
	if strings.Join(ids, ",") != want || kinds[themeKindDark] == 0 || kinds[themeKindLight] == 0 {
		t.Fatalf("themes = %v kinds=%v, want %s", ids, kinds, want)
	}
}
