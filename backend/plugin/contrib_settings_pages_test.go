package plugin

import (
	"encoding/json"
	"strings"
	"testing"
)

// writeSettingsPagesManifest writes an API v2 process plugin with a tab view
// "settings" and a floating view "pet" plus the given settingsPages value.
func writeSettingsPagesManifest(t *testing.T, pages string) string {
	t.Helper()
	return writeTestManifestV2(t, func(manifest *Manifest) {
		manifest.Contributes.Views = append(manifest.Contributes.Views,
			ManifestView{ID: "settings", Title: "Proxy settings", Entry: "ui/index.html", TitleLocalized: map[string]string{"zh": "代理设置"}},
			ManifestView{ID: "pet", Title: "Pet", Entry: "ui/index.html", Floating: &ManifestFloating{Width: 200, Height: 240}},
		)
		manifest.Contributes.Points = map[string]json.RawMessage{"settingsPages": json.RawMessage(pages)}
	})
}

func TestSettingsPagesDecode(t *testing.T) {
	cases := map[string]struct {
		pages    string
		want     *SettingsPageContribution
		contains string
	}{
		"view title default": {
			pages: `[{"id":"main","view":"settings"}]`,
			want:  &SettingsPageContribution{ID: "main", View: "settings", Title: "Proxy settings", TitleLocalized: map[string]string{"zh": "代理设置"}},
		},
		"own title": {
			pages: `[{"id":"main","view":"settings","title":" Rules ","titleLocalized":{"zh":"规则"}}]`,
			want:  &SettingsPageContribution{ID: "main", View: "settings", Title: "Rules", TitleLocalized: map[string]string{"zh": "规则"}},
		},
		"missing view":    {pages: `[{"id":"main","view":"missing"}]`, contains: `settings page "main" view "missing" is not one of this plugin's contributes.views`},
		"floating view":   {pages: `[{"id":"main","view":"pet"}]`, contains: `settings page "main" view "pet" is a floating view`},
		"two pages":       {pages: `[{"id":"main","view":"settings"},{"id":"more","view":"dashboard"}]`, contains: "maxEntries"},
		"title too long":  {pages: `[{"id":"main","view":"settings","title":"` + strings.Repeat("a", 49) + `"}]`, contains: "title exceeds 48 characters"},
		"localized long":  {pages: `[{"id":"main","view":"settings","titleLocalized":{"zh":"` + strings.Repeat("规", 49) + `"}}]`, contains: "titleLocalized"},
		"unknown field":   {pages: `[{"id":"main","view":"settings","surface":"settings"}]`, contains: "parse settings page"},
		"bad id":          {pages: `[{"id":" ","view":"settings"}]`, contains: "id"},
		"missing view id": {pages: `[{"id":"main"}]`, contains: "view is required"},
		"multiline title": {pages: `[{"id":"main","view":"settings","title":"a\nb"}]`, contains: "title"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			manifest, err := readManifest(writeSettingsPagesManifest(t, tc.pages))
			if tc.want != nil {
				if err != nil {
					t.Fatal(err)
				}
				if err := manifest.contributionIssuesError(); err != nil {
					t.Fatal(err)
				}
				pages := manifest.SettingsPages()
				if len(pages) != 1 || pages[0].ID != tc.want.ID || pages[0].View != tc.want.View || pages[0].Title != tc.want.Title ||
					len(pages[0].TitleLocalized) != len(tc.want.TitleLocalized) || pages[0].TitleLocalized["zh"] != tc.want.TitleLocalized["zh"] {
					t.Fatalf("pages = %+v, want %+v", pages, *tc.want)
				}
				return
			}
			if err == nil {
				err = manifest.contributionIssuesError()
			}
			if tc.contains == "maxEntries" {
				if err == nil || !strings.Contains(err.Error(), "at most 1 settings pages") {
					t.Fatalf("err = %v, want maxEntries", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("err = %v, want %q", err, tc.contains)
			}
		})
	}
}

func TestSettingsPagesSnapshotAndPermission(t *testing.T) {
	manifest, err := readManifest(writeSettingsPagesManifest(t, `[{"id":"main","view":"settings"}]`))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(manifest.SettingsPages()[0])
	if err != nil || string(encoded) != `{"id":"main","view":"settings","title":"Proxy settings","titleLocalized":{"zh":"代理设置"}}` {
		t.Fatalf("encoded = %s %v", encoded, err)
	}
	// A settings page gives the plugin no new data: adding one in an update
	// never asks for approval again.
	contributionPointsMu.RLock()
	point := contributionPoints[settingsPagesPointKey]
	contributionPointsMu.RUnlock()
	if point == nil || point.ExpandsPermission != nil {
		t.Fatalf("settingsPages point = %+v", point)
	}
	var empty Manifest
	if pages := empty.SettingsPages(); pages != nil {
		t.Fatalf("empty manifest pages = %+v", pages)
	}
}

// A view title may be up to 200 bytes; the inherited page title is cut to 48
// runes at a rune boundary (the frontend drops longer titles), and the view's
// translations are copied, not shared.
func TestSettingsPagesInheritLongViewTitle(t *testing.T) {
	long := strings.Repeat("a", 59) + "規"
	root := writeTestManifestV2(t, func(manifest *Manifest) {
		manifest.Contributes.Views = append(manifest.Contributes.Views, ManifestView{
			ID: "settings", Title: long, Entry: "ui/index.html",
			TitleLocalized: map[string]string{"zh": strings.Repeat("规", 47) + " 设置页", "en": "‮Settings"},
		})
		manifest.Contributes.Points = map[string]json.RawMessage{"settingsPages": json.RawMessage(`[{"id":"main","view":"settings"}]`)}
	})
	manifest, err := readManifestStrict(root)
	if err != nil {
		t.Fatal(err)
	}
	pages := manifest.SettingsPages()
	if len(pages) != 1 {
		t.Fatalf("pages = %+v", pages)
	}
	page := pages[0]
	if page.Title != strings.Repeat("a", 48) {
		t.Fatalf("title = %q", page.Title)
	}
	if page.TitleLocalized["zh"] != strings.Repeat("规", 47) || page.TitleLocalized["en"] != "Settings" {
		t.Fatalf("titleLocalized = %q", page.TitleLocalized)
	}
	for _, title := range []string{page.Title, page.TitleLocalized["zh"], page.TitleLocalized["en"]} {
		if _, err := validateContributionLine(title, settingsPageTitleMaxRunes, true); err != nil {
			t.Fatalf("inherited title %q breaks the point rules: %v", title, err)
		}
	}
	var view ManifestView
	for _, candidate := range manifest.Contributes.Views {
		if candidate.ID == "settings" {
			view = candidate
		}
	}
	page.TitleLocalized["zh"] = "changed"
	if view.TitleLocalized["zh"] == "changed" {
		t.Fatal("inherited titleLocalized shares the view's map")
	}
	if got := settingsPageInheritedTitle(strings.Repeat("规", 47) + "界x"); got != strings.Repeat("规", 47)+"界" {
		t.Fatalf("rune boundary cut = %q", got)
	}
}
