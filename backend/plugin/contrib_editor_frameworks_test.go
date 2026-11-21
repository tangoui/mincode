package plugin

import (
	"encoding/json"
	"testing"
)

func TestEditorFrameworksRejectUnsafeMetadata(t *testing.T) {
	for _, raw := range []string{
		`{"id":"demo","languages":["typescript"],"match":{"all":[{"file":"../secret"}],"any":[],"exclude":[]},"completions":[]}`,
		`{"id":"demo","languages":["typescript"],"match":{"all":[{"file":".env"}],"any":[],"exclude":[]},"completions":[]}`,
		`{"id":"demo","languages":["typescript"],"match":{"all":[{"file":"config.json","field":["__proto__"]}],"any":[],"exclude":[]},"completions":[]}`,
		`{"id":"demo","languages":["typescript"],"match":{"all":[],"any":[],"exclude":[]},"completions":[]}`,
	} {
		if _, _, err := decodeEditorFramework(nil, 0, "", json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted unsafe metadata: %s", raw)
		}
	}
}

func TestEditorFrameworksStrictPackage(t *testing.T) {
	root := writeKeymapPackage(t, "", func(manifest map[string]any) {
		manifest["contributes"] = map[string]any{"editorFrameworks": []any{map[string]string{"path": "frameworks/demo.json"}}}
	}, nil, map[string]string{"frameworks/demo.json": `{"id":"demo","languages":["typescript"],"match":{"all":[{"file":"pages.json"}],"any":[{"file":"App.vue"}],"exclude":[]},"completions":[{"label":"demo.request","insertText":"request","context":"demo.","detail":"request(options)","documentation":"Request data"}]}`})
	inspection, err := inspectInstallDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(inspection.Manifest.contribution("editorFrameworks").Entries)
	if err != nil {
		t.Fatal(err)
	}
	var entries []map[string]any
	if json.Unmarshal(raw, &entries) != nil || len(entries) != 1 || entries[0]["id"] != "demo" {
		t.Fatalf("snapshot: %s", raw)
	}
}
