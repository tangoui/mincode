package plugin

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCustomAiPromptsPersistConflictAndDisable(t *testing.T) {
	useTemporaryPluginData(t)
	manifest := Manifest{ID: "custom-test"}
	first, err := mutateCustomAiPrompts(manifest, "save", json.RawMessage(`{"revision":0,"prompt":{"title":"Review","description":"Check code","category":"Code","body":"Review {{selection}}","enabled":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision != 1 || len(first.Prompts) != 1 || !strings.HasPrefix(first.Prompts[0].ID, "custom-") {
		t.Fatalf("first: %+v", first)
	}
	loaded, err := readCustomAiPrompts(manifest)
	if err != nil || loaded.Revision != 1 || loaded.Prompts[0].Body != "Review {{selection}}" {
		t.Fatalf("reload: %+v %v", loaded, err)
	}
	if _, err = mutateCustomAiPrompts(manifest, "delete", json.RawMessage(`{"revision":0,"id":"`+first.Prompts[0].ID+`"}`)); err == nil {
		t.Fatal("stale revision accepted")
	}
	raw, _ := json.Marshal(map[string]any{"revision": 1, "prompt": customAiPrompt{ID: first.Prompts[0].ID, Title: "Changed", Body: "New", Enabled: false}})
	second, err := mutateCustomAiPrompts(manifest, "save", raw)
	if err != nil || second.Revision != 2 || len(second.Entries) != 0 {
		t.Fatalf("disabled: %+v %v", second, err)
	}
	other, err := readCustomAiPrompts(Manifest{ID: "other"})
	if err != nil || len(other.Prompts) != 0 {
		t.Fatalf("namespace: %+v %v", other, err)
	}
	deleted, err := mutateCustomAiPrompts(manifest, "delete", json.RawMessage(`{"revision":2,"id":"`+first.Prompts[0].ID+`"}`))
	if err != nil || deleted.Revision != 3 || len(deleted.Prompts) != 0 {
		t.Fatalf("delete: %+v %v", deleted, err)
	}
}

func TestCustomAiPromptsValidationAndCapacity(t *testing.T) {
	useTemporaryPluginData(t)
	manifest := Manifest{ID: "custom-test"}
	for _, prompt := range []customAiPrompt{
		{Title: "Bad", Body: "{{clipboard}}"}, {Title: "Bad", Body: "hidden‮text"},
		{Title: strings.Repeat("x", 65), Body: "x"}, {Title: "Bad", Body: strings.Repeat("x", 16385)},
		{Title: "Bad", Body: "x", Category: strings.Repeat("类", 31)}, {ID: "builtin", Title: "Override", Body: "x"},
	} {
		raw, _ := json.Marshal(map[string]any{"revision": 0, "prompt": prompt})
		if _, err := mutateCustomAiPrompts(manifest, "save", raw); err == nil {
			t.Fatalf("accepted invalid prompt %+v", prompt)
		}
	}
	for i := 0; i < 32; i++ {
		raw, _ := json.Marshal(map[string]any{"revision": i, "prompt": customAiPrompt{Title: "P", Body: "x", Enabled: false}})
		if _, err := mutateCustomAiPrompts(manifest, "save", raw); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := mutateCustomAiPrompts(manifest, "save", json.RawMessage(`{"revision":32,"prompt":{"title":"Overflow","body":"x"}}`)); err == nil {
		t.Fatal("capacity exceeded")
	}
	if err := pluginKVSetRaw(manifest.ID, customAiPromptsKey, json.RawMessage(`{"version":1,"revision":33,"prompts":[{"id":"custom-1234567890abcdef","title":"Bad","body":"{{secret}}","enabled":true}]}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := readCustomAiPrompts(manifest); err == nil {
		t.Fatal("corrupt imported document accepted")
	}
}
