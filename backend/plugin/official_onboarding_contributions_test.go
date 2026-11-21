package plugin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

// The official packages that adopt localizations, walkthroughs and
// uriHandlers must pass strict host inspection: the declarative packs are
// installed and enabled through the real manager, their snapshot entries and
// canonical assets are checked, and the code-review link handler resolves a
// real mincode:// link.

func officialPackageRoot(id string) string {
	return filepath.Join("..", "..", "official-plugins", id)
}

func installOfficialDeclarativeForTest(t *testing.T, id, point string, entries any) *pluginManager {
	t.Helper()
	useTemporaryPluginData(t)
	manager := newPluginManager()
	manifest, err := manager.InstallLocal(officialPackageRoot(id))
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.isDeclarative() {
		t.Fatalf("%s must be declarative", id)
	}
	if err := manager.SetEnabled(context.Background(), manifest.ID, true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { contributionAssets.purgePlugin(manifest.ID) })
	snapshot := manager.Contributions([]string{point})
	if len(snapshot.Plugins) != 1 || snapshot.Plugins[0].PluginID != id {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	resolved, ok := snapshot.Plugins[0].Points[point]
	if !ok || len(resolved.Issues) != 0 {
		t.Fatalf("%s %s point = %+v", id, point, resolved)
	}
	data, err := json.Marshal(resolved.Entries)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, entries); err != nil {
		t.Fatal(err)
	}
	return manager
}

func fetchOfficialAssetForTest(t *testing.T, manager *pluginManager, id, point, key string) []byte {
	t.Helper()
	response, err := manager.ContributionAssets(contributionAssetsRequest{PluginID: id, Point: point, Keys: []string{key}})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Assets) != 1 || len(response.Missing)+len(response.Stale)+len(response.Deferred) != 0 {
		t.Fatalf("%s asset %s = %+v", id, key, response)
	}
	data, err := base64.StdEncoding.DecodeString(response.Assets[0].Data)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestOfficialTraditionalChineseLanguagePack(t *testing.T) {
	var packs []LocalizationContribution
	manager := installOfficialDeclarativeForTest(t, "lang-zh-hant", localizationPointKey, &packs)
	if len(packs) != 2 || packs[0].Locale != "zh-Hant-TW" || packs[1].Locale != "zh-Hant-HK" {
		t.Fatalf("packs = %+v", packs)
	}
	for _, pack := range packs {
		if pack.Messages == nil || pack.Messages.Count < 4000 || pack.PluginTitles != nil {
			t.Fatalf("pack = %+v", pack)
		}
		var messages map[string]string
		if err := json.Unmarshal(fetchOfficialAssetForTest(t, manager, "lang-zh-hant", localizationPointKey, pack.Messages.Asset), &messages); err != nil {
			t.Fatal(err)
		}
		if len(messages) != pack.Messages.Count || messages["nav.database"] == "" {
			t.Fatalf("%s: %d messages, nav.database = %q", pack.Locale, len(messages), messages["nav.database"])
		}
	}
}

func TestOfficialTranslatedLanguagePacks(t *testing.T) {
	for _, locale := range []string{"ja", "ko", "de", "fr", "es", "pt-BR", "ru", "it", "vi", "tr"} {
		id := "lang-" + strings.ToLower(locale)
		t.Run(id, func(t *testing.T) {
			var packs []LocalizationContribution
			manager := installOfficialDeclarativeForTest(t, id, localizationPointKey, &packs)
			if len(packs) != 1 || packs[0].Locale != locale || packs[0].Messages == nil || packs[0].Messages.Count < 4000 || packs[0].PluginTitles != nil {
				t.Fatalf("packs = %+v", packs)
			}
			var messages map[string]string
			if err := json.Unmarshal(fetchOfficialAssetForTest(t, manager, id, localizationPointKey, packs[0].Messages.Asset), &messages); err != nil {
				t.Fatal(err)
			}
			if len(messages) != packs[0].Messages.Count || messages["nav.settings"] == "" || messages["nav.settings"] == "Settings" {
				t.Fatalf("%s: %d messages, nav.settings = %q", locale, len(messages), messages["nav.settings"])
			}
		})
	}
}

func TestOfficialGettingStartedWalkthrough(t *testing.T) {
	var walkthroughs []WalkthroughContribution
	manager := installOfficialDeclarativeForTest(t, "getting-started", walkthroughPointKey, &walkthroughs)
	if len(walkthroughs) != 1 || walkthroughs[0].ID != "mincode-basics" || !walkthroughs[0].Featured || len(walkthroughs[0].Steps) != 10 {
		t.Fatalf("walkthroughs = %+v", walkthroughs)
	}
	for _, step := range walkthroughs[0].Steps {
		if step.Command != "" || step.Image != "" || step.Markdown == "" || step.MarkdownLocalized["zh"] == "" {
			t.Fatalf("step = %+v", step)
		}
		for _, key := range []string{step.Markdown, step.MarkdownLocalized["zh"]} {
			if len(fetchOfficialAssetForTest(t, manager, "getting-started", walkthroughPointKey, key)) == 0 {
				t.Fatalf("step %s has empty markdown", step.ID)
			}
		}
	}
}

func TestOfficialCodeReviewLinkHandler(t *testing.T) {
	manifest := readContributionManifestForTest(t, officialPackageRoot("code-review"))
	phaseGRequireNoIssues(t, "code-review", manifest)
	handlers := phaseGEntries[UriHandlerContribution](manifest, uriHandlerPointKey)
	if len(handlers) != 1 || handlers[0].ID != "open-url" || handlers[0].Target.Type != "view" || handlers[0].Target.View != "main" {
		t.Fatalf("handlers = %+v", handlers)
	}
	pull := "https://github.com/o/r/pull/12"
	link, err := parsePluginLink("mincode://code-review/open?url=" + url.QueryEscape(pull))
	if err != nil {
		t.Fatal(err)
	}
	handler, pathParams, ok := matchUriHandler(handlers, link.segments)
	if !ok || handler.ID != "open-url" || len(pathParams) != 0 {
		t.Fatalf("match = %+v %+v %v", handler, pathParams, ok)
	}
	params, err := coerceUriQuery(handler.Query, link.query)
	if err != nil || len(params) != 1 || params["url"] != pull {
		t.Fatalf("params = %+v, %v", params, err)
	}
	for _, query := range []string{"", "?other=1", "?url=a&url=b"} {
		link, err := parsePluginLink("mincode://code-review/open" + query)
		if err != nil {
			continue
		}
		if _, err := coerceUriQuery(handler.Query, link.query); err == nil {
			t.Fatalf("query %q must be rejected", query)
		}
	}
}
