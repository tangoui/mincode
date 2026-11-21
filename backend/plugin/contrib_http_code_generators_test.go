package plugin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func writeHTTPCodeGeneratorPackage(t *testing.T, entries []any, files map[string]string) string {
	t.Helper()
	return writeKeymapPackage(t, "", func(manifest map[string]any) {
		manifest["id"] = "http-codegen-test"
		manifest["contributes"] = map[string]any{httpCodeGeneratorPointKey: entries}
	}, nil, files)
}

func httpCodeGeneratorEntry(id, escape, template string) map[string]any {
	return map[string]any{"id": id, "title": "Generator " + id, "escape": escape, "template": template}
}

func TestHTTPCodeGeneratorsSnapshotCarriesAssetKeys(t *testing.T) {
	useTemporaryPluginData(t)
	httpie := httpCodeGeneratorEntry("httpie", "shell", "codegen/httpie.tmpl")
	httpie["titleLocalized"] = map[string]string{"zh": "HTTPie"}
	root := writeHTTPCodeGeneratorPackage(t, []any{httpie, httpCodeGeneratorEntry("copy", "json", "assets/copy.tmpl")}, map[string]string{
		"codegen/httpie.tmpl": "\ufeffhttp {{method|raw}} {{url}}{{#headers}} \\\r\n  {{name}}:{{value}}{{/headers}}\r\n",
		"assets/copy.tmpl":    "http {{method|raw}} {{url}}{{#headers}} \\\r\n  {{name}}:{{value}}{{/headers}}\r\n",
	})
	inspection, err := inspectInstallDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	items := inspection.Manifest.httpCodeGenerators()
	if len(items) != 2 || items[0].Escape != "shell" || len(items[0].Template) != 64 || items[0].TitleLocalized["zh"] != "HTTPie" {
		t.Fatalf("items = %+v", items)
	}
	if items[0].Template != items[1].Template {
		t.Fatal("the BOM must be stripped before hashing, so equal templates share one key")
	}

	manager := newPluginManager()
	manifest, err := manager.InstallLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetEnabled(context.Background(), manifest.ID, true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { contributionAssets.purgePlugin(manifest.ID) })
	snapshot := manager.Contributions([]string{httpCodeGeneratorPointKey})
	if len(snapshot.Plugins) != 1 || len(snapshot.Plugins[0].Points[httpCodeGeneratorPointKey].Entries) != 2 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	encoded, _ := json.Marshal(snapshot.Plugins[0].Points[httpCodeGeneratorPointKey].Entries[0])
	if !strings.Contains(string(encoded), `"template":"`+items[0].Template+`"`) || strings.Contains(string(encoded), "codegen/") {
		t.Fatalf("entry = %s", encoded)
	}
}

func TestHTTPCodeGeneratorAssetValidation(t *testing.T) {
	cases := map[string]string{
		"zero width":        "curl {{url}}\u200b",
		"nul":               "curl {{url}}\x00",
		"invalid utf-8":     "curl \xff",
		"delimiter change":  "{{=<% %>=}}",
		"ampersand":         "{{&body}}",
		"nested list":       "{{#headers}}{{#query}}{{/query}}{{/headers}}",
		"unclosed":          "{{#hasBody}}",
		"line separator":    "curl\u2028{{url}}",
		"bidi isolate":      "curl \u2066{{url}}",
		"whitespace in tag": "{{ url }}",
	}
	for name, template := range cases {
		if _, _, err := validateHTTPCodeGeneratorAsset("", ".tmpl", []byte(template)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	canonical, mime, err := validateHTTPCodeGeneratorAsset("", ".tmpl", []byte("\ufeff{{! c }}\r\n\tcurl {{url|shell}}\n"))
	if err != nil || mime != "text/plain" || strings.HasPrefix(string(canonical), "\ufeff") {
		t.Fatalf("canonical=%q mime=%q err=%v", canonical, mime, err)
	}
	if _, _, err := validateHTTPCodeGeneratorAsset("", ".txt", []byte("curl")); err == nil {
		t.Fatal("non-.tmpl accepted")
	}
}

func TestHTTPCodeGeneratorEntryRules(t *testing.T) {
	useTemporaryPluginData(t)
	template := map[string]string{"codegen/t.tmpl": "curl {{url}}\n"}
	bad := []map[string]any{
		{"id": "Bad", "title": "T", "escape": "shell", "template": "codegen/t.tmpl"},
		{"id": "t", "title": "", "escape": "shell", "template": "codegen/t.tmpl"},
		{"id": "t", "title": "T", "escape": "bash", "template": "codegen/t.tmpl"},
		{"id": "t", "title": "T", "escape": "shell"},
		{"id": "t", "title": "T", "escape": "shell", "template": "codegen/t.tmpl", "run": "x"},
		{"id": "t", "title": "T", "escape": "shell", "template": "../t.tmpl"},
	}
	for index, entry := range bad {
		root := writeHTTPCodeGeneratorPackage(t, []any{entry}, template)
		if _, err := inspectInstallDirectory(root); err == nil {
			t.Errorf("case %d accepted: %v", index, entry)
		}
	}
}
