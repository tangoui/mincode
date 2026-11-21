package plugin

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// writeLocalizationPackage writes a declarative package whose
// contributes.localizations are `entries`; files are extra package files.
func writeLocalizationPackage(t *testing.T, entries []any, files map[string]string) string {
	t.Helper()
	return writeKeymapPackage(t, "", func(manifest map[string]any) {
		manifest["id"] = "language-pack-test"
		manifest["contributes"] = map[string]any{"localizations": entries}
	}, nil, files)
}

func localizationEntry(locale string, fields map[string]any) map[string]any {
	entry := map[string]any{"locale": locale, "languageName": "Japanese", "localizedLanguageName": "日本語"}
	for key, value := range fields {
		entry[key] = value
	}
	return entry
}

func TestLocalizationsDeclarativePackageSnapshot(t *testing.T) {
	useTemporaryPluginData(t)
	root := writeLocalizationPackage(t, []any{
		localizationEntry("ja", map[string]any{"messages": "l10n/host.ja.json", "pluginTitles": "l10n/plugins.ja.json"}),
		localizationEntry("zh-TW", map[string]any{"languageName": " Chinese (Taiwan) ", "localizedLanguageName": "繁體中文", "messages": "locales/zh-TW.json"}),
	}, map[string]string{
		// Unsorted keys and whitespace are canonicalised away.
		"l10n/host.ja.json":    "{ \"settings.title\": \"設定\",\n \"common.save\": \"保存\", \"ai.greeting\": \"こんにちは {name}\\n次へ\" }",
		"l10n/plugins.ja.json": `{"other.plugin":{"views":{"main":"メイン"},"commands":{"run":"実行"}},"another":{"commands":{"x.y":"X"}}}`,
		"locales/zh-TW.json":   `{"settings.title":"設定"}`,
	})
	inspection, err := inspectInstallDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	resolved := inspection.Manifest.contribution(localizationPointKey)
	if resolved == nil || len(resolved.Entries) != 2 || len(resolved.Issues) != 0 {
		t.Fatalf("resolved = %+v", resolved)
	}
	ja := resolved.Entries[0].(LocalizationContribution)
	wantMessages := `{"ai.greeting":"こんにちは {name}\n次へ","common.save":"保存","settings.title":"設定"}`
	if ja.Messages == nil || ja.Messages.Count != 3 || ja.Messages.Asset != contributionAssetKey([]byte(wantMessages)) {
		t.Fatalf("ja messages = %+v", ja.Messages)
	}
	wantTitles := `{"another":{"commands":{"x.y":"X"}},"other.plugin":{"commands":{"run":"実行"},"views":{"main":"メイン"}}}`
	if ja.PluginTitles == nil || strings.Join(ja.PluginTitles.Plugins, ",") != "another,other.plugin" || ja.PluginTitles.Asset != contributionAssetKey([]byte(wantTitles)) {
		t.Fatalf("ja plugin titles = %+v", ja.PluginTitles)
	}
	tw := resolved.Entries[1].(LocalizationContribution)
	if tw.LanguageName != "Chinese (Taiwan)" || tw.PluginTitles != nil || tw.Messages.Count != 1 {
		t.Fatalf("zh-TW = %+v", tw)
	}
	if len(resolved.Assets) != 3 {
		t.Fatalf("assets = %+v", resolved.Assets)
	}
	snapshot, err := json.Marshal(resolved.Entries)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(snapshot), "l10n/") || strings.Contains(string(snapshot), "設定") {
		t.Fatalf("snapshot must carry keys only, got %s", snapshot)
	}
}

func TestLocalizationsRejectInvalidEntries(t *testing.T) {
	useTemporaryPluginData(t)
	files := map[string]string{
		"l10n/ok.json":       `{"a.b":"x"}`,
		"l10n/nested.json":   `{"a":{"b":"c"}}`,
		"l10n/jsonc.json":    "{\"a\":\"x\", // comment\n}",
		"l10n/dup.json":      `{"a":"x","a":"y"}`,
		"l10n/bidi.json":     "{\"a\":\"x\xe2\x80\xaey\"}",
		"l10n/zw.json":       "{\"a\":\"x\xe2\x80\x8by\"}",
		"l10n/tab.json":      `{"a":"x\ty"}`,
		"l10n/blank.json":    `{"a":"  "}`,
		"l10n/badkey.json":   `{"-a":"x"}`,
		"l10n/array.json":    `["a"]`,
		"l10n/empty.json":    `{}`,
		"l10n/titles.json":   `{"p1":{"menus":{"a":"b"}}}`,
		"l10n/titlenl.json":  `{"p1":{"commands":{"a":"b\nc"}}}`,
		"l10n/titleid.json":  `{"Bad ID":{"commands":{"a":"b"}}}`,
		"other/ok.json":      `{"a":"x"}`,
		"l10n/long-key.json": `{"` + strings.Repeat("k", 129) + `":"x"}`,
	}
	cases := []struct {
		name  string
		entry map[string]any
		want  string
	}{
		{"builtin", localizationEntry("zh", map[string]any{"messages": "l10n/ok.json"}), "built in"},
		{"builtin alias", localizationEntry("zh-Hans-CN", map[string]any{"messages": "l10n/ok.json"}), "built in"},
		{"bad tag", localizationEntry("ja_JP", map[string]any{"messages": "l10n/ok.json"}), "language tag"},
		{"no files", localizationEntry("ja", nil), "at least one"},
		{"unknown field", localizationEntry("ja", map[string]any{"messages": "l10n/ok.json", "extra": 1}), "unknown field"},
		{"blank name", localizationEntry("ja", map[string]any{"messages": "l10n/ok.json", "languageName": " "}), "languageName"},
		{"bidi name", localizationEntry("ja", map[string]any{"messages": "l10n/ok.json", "localizedLanguageName": "日\u202e本"}), "bidirectional"},
		{"nested", localizationEntry("ja", map[string]any{"messages": "l10n/nested.json"}), "flat object"},
		{"jsonc", localizationEntry("ja", map[string]any{"messages": "l10n/jsonc.json"}), "strict JSON"},
		{"duplicate key", localizationEntry("ja", map[string]any{"messages": "l10n/dup.json"}), "duplicate key"},
		{"bidi value", localizationEntry("ja", map[string]any{"messages": "l10n/bidi.json"}), "bidirectional"},
		{"zero width", localizationEntry("ja", map[string]any{"messages": "l10n/zw.json"}), "zero-width"},
		{"tab", localizationEntry("ja", map[string]any{"messages": "l10n/tab.json"}), "control"},
		{"blank value", localizationEntry("ja", map[string]any{"messages": "l10n/blank.json"}), "blank"},
		{"bad key", localizationEntry("ja", map[string]any{"messages": "l10n/badkey.json"}), "message key"},
		{"long key", localizationEntry("ja", map[string]any{"messages": "l10n/long-key.json"}), "message key"},
		{"empty", localizationEntry("ja", map[string]any{"messages": "l10n/empty.json"}), "at least one"},
		{"array", localizationEntry("ja", map[string]any{"messages": "l10n/array.json"}), "JSON object"},
		{"outside dirs", localizationEntry("ja", map[string]any{"messages": "other/ok.json"}), "under l10n/"},
		{"missing file", localizationEntry("ja", map[string]any{"messages": "l10n/missing.json"}), "l10n/missing.json"},
		{"titles group", localizationEntry("ja", map[string]any{"pluginTitles": "l10n/titles.json"}), `"commands" and "views"`},
		{"title newline", localizationEntry("ja", map[string]any{"pluginTitles": "l10n/titlenl.json"}), "control"},
		{"title plugin id", localizationEntry("ja", map[string]any{"pluginTitles": "l10n/titleid.json"}), "plugin id"},
		{"messages as titles", localizationEntry("ja", map[string]any{"pluginTitles": "l10n/ok.json"}), "must be an object"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := writeLocalizationPackage(t, []any{tc.entry}, files)
			_, err := inspectInstallDirectory(root)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "invalid localization contribution") {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}

	// Duplicate locales within one plugin.
	root := writeLocalizationPackage(t, []any{
		localizationEntry("ja", map[string]any{"messages": "l10n/ok.json"}),
		localizationEntry("ja", map[string]any{"messages": "l10n/ok.json"}),
	}, files)
	if _, err := inspectInstallDirectory(root); err == nil || !strings.Contains(err.Error(), `duplicate language pack locale "ja"`) {
		t.Fatalf("duplicate err = %v", err)
	}
}

func TestLocalizationsLenientSkipsBadEntries(t *testing.T) {
	useTemporaryPluginData(t)
	root := writeLocalizationPackage(t, []any{
		localizationEntry("ja", map[string]any{"messages": "l10n/ok.json"}),
		localizationEntry("ko", map[string]any{"messages": "l10n/bad.json"}),
	}, map[string]string{"l10n/ok.json": `{"a":"x"}`, "l10n/bad.json": "{\"a\":\"x\xe2\x81\xa6\"}"})
	manifest, err := readManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	resolved := manifest.contribution(localizationPointKey)
	if resolved == nil || len(resolved.Entries) != 1 || len(resolved.Issues) != 1 || resolved.Issues[0].ID != "ko" {
		t.Fatalf("resolved = %+v", resolved)
	}
	if len(resolved.Assets) != 1 {
		t.Fatalf("assets of the skipped entry must not be servable: %+v", resolved.Assets)
	}
	if got := manifest.localizations(); len(got) != 1 || got[0].Locale != "ja" {
		t.Fatalf("localizations() = %+v", got)
	}
}

func TestLocalizationsAssetLimits(t *testing.T) {
	var body strings.Builder
	body.WriteString("{")
	for index := 0; index <= localizationMessagesMaxKeys; index++ {
		if index > 0 {
			body.WriteString(",")
		}
		body.WriteString(`"k` + strconv.Itoa(index) + `":"v"`)
	}
	body.WriteString("}")
	if _, _, err := validateLocalizationAsset(localizationMessagesHint, ".json", []byte(body.String())); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("too many keys err = %v", err)
	}
	long := `{"a":"` + strings.Repeat("あ", localizationValueMaxRunes+1) + `"}`
	if _, _, err := validateLocalizationAsset(localizationMessagesHint, ".json", []byte(long)); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("long value err = %v", err)
	}
	if _, _, err := validateLocalizationAsset("other", ".json", []byte(`{}`)); err == nil {
		t.Fatal("unknown hint accepted")
	}
	if _, _, err := validateLocalizationAsset(localizationMessagesHint, ".txt", []byte(`{}`)); err == nil {
		t.Fatal("non-JSON extension accepted")
	}
	canonical, summary, err := validateLocalizationAsset(localizationMessagesHint, ".json", []byte(`{"b":"<&>","a":"1"}`))
	if err != nil || summary.count != 2 {
		t.Fatalf("canonical err = %v summary = %+v", err, summary)
	}
	var decoded map[string]string
	if err := json.Unmarshal(canonical, &decoded); err != nil || decoded["b"] != "<&>" || !strings.HasPrefix(string(canonical), `{"a":`) {
		t.Fatalf("canonical = %s", canonical)
	}
}

// TestLocalizationsAssetFetchServesAndDetectsChanges exercises the on-demand
// asset command for a language pack: canonical bytes are served, and a file
// edited on disk after install is reported stale, never served.
func TestLocalizationsAssetFetchServesAndDetectsChanges(t *testing.T) {
	useTemporaryPluginData(t)
	manager := newPluginManager()
	root := writeLocalizationPackage(t, []any{
		localizationEntry("ja", map[string]any{"messages": "l10n/host.ja.json"}),
	}, map[string]string{"l10n/host.ja.json": `{ "b": "2", "a": "1" }`})
	inspection, err := inspectInstallDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	manifest := inspection.Manifest
	record := &pluginRecord{id: manifest.ID, source: pluginSourceLocal, root: root, manifest: &manifest, enabled: true, state: pluginStateStopped, scope: pluginScopeGlobal}
	manager.mu.Lock()
	manager.records[record.id] = record
	manager.order = append(manager.order, record.id)
	manager.mu.Unlock()
	t.Cleanup(func() { contributionAssets.purgePlugin(record.id) })

	key := manifest.localizations()[0].Messages.Asset
	response, err := manager.ContributionAssets(contributionAssetsRequest{PluginID: record.id, Point: localizationPointKey, Keys: []string{key}})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Assets) != 1 || response.Assets[0].Mime != "application/json" {
		t.Fatalf("response = %+v", response)
	}
	if data, _ := base64.StdEncoding.DecodeString(response.Assets[0].Data); string(data) != `{"a":"1","b":"2"}` {
		t.Fatalf("data = %s", data)
	}

	if err := os.WriteFile(filepath.Join(root, "l10n", "host.ja.json"), []byte(`{"a":"changed"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	contributionAssets.purgePlugin(record.id)
	response, _ = manager.ContributionAssets(contributionAssetsRequest{PluginID: record.id, Point: localizationPointKey, Keys: []string{key}})
	if len(response.Assets) != 0 || strings.Join(response.Stale, ",") != key {
		t.Fatalf("stale response = %+v", response)
	}

	// An edit that makes the file invalid is reported missing.
	if err := os.WriteFile(filepath.Join(root, "l10n", "host.ja.json"), []byte("{\"a\":\"x\xe2\x80\xaey\"}"), 0o600); err != nil {
		t.Fatal(err)
	}
	response, _ = manager.ContributionAssets(contributionAssetsRequest{PluginID: record.id, Point: localizationPointKey, Keys: []string{key}})
	if len(response.Assets) != 0 || strings.Join(response.Missing, ",") != key {
		t.Fatalf("invalid response = %+v", response)
	}
}

func TestLocalizationAssetRejectsByteOrderMark(t *testing.T) {
	_, _, err := validateLocalizationAsset(localizationMessagesHint, ".json", []byte("\xef\xbb\xbf{\"settings.title\":\"x\"}"))
	if err == nil || !strings.Contains(err.Error(), "byte order mark") {
		t.Fatalf("err = %v, want byte order mark rejection", err)
	}
}
