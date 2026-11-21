package plugin

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCustomAiPromptsSerializedResponseBound(t *testing.T) {
	useTemporaryPluginData(t)
	manifest := Manifest{ID: "wire-test"}
	revision := uint64(0)
	for i := 0; i < 16; i++ {
		raw, _ := json.Marshal(map[string]any{"revision": revision, "prompt": customAiPrompt{Title: "HTML", Body: strings.Repeat("<", 16384), Enabled: true}})
		snapshot, err := mutateCustomAiPrompts(manifest, "save", raw)
		if err != nil {
			loaded, readErr := readCustomAiPrompts(manifest)
			if readErr != nil || loaded.Revision != revision {
				t.Fatalf("failed save committed: revision=%d err=%v", loaded.Revision, readErr)
			}
			return
		}
		revision = snapshot.Revision
		encoded, _ := json.Marshal(snapshot)
		if len(encoded) >= pluginRPCMaxMessage-4096 {
			t.Fatalf("successful save produced unreadable response: %d", len(encoded))
		}
	}
}
func TestCustomAiPromptsGenericReplacementSerializesAndInvalidatesRevision(t *testing.T) {
	useTemporaryPluginData(t)
	manifest := Manifest{ID: "raw-test"}
	_, err := mutateCustomAiPrompts(manifest, "save", json.RawMessage(`{"revision":0,"prompt":{"title":"First","body":"x"}}`))
	if err != nil {
		t.Fatal(err)
	}
	lock := customAiPromptLock(manifest.ID)
	lock.Lock()
	done := make(chan error, 1)
	go func() {
		done <- pluginKVSetRaw(manifest.ID, customAiPromptsKey, json.RawMessage(`{"version":1,"revision":1,"prompts":[]}`))
	}()
	select {
	case err := <-done:
		lock.Unlock()
		t.Fatalf("raw replacement bypassed document lock: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	lock.Unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := mutateCustomAiPrompts(manifest, "save", json.RawMessage(`{"revision":1,"prompt":{"title":"Stale","body":"x"}}`)); err == nil {
		t.Fatal("raw replacement did not invalidate stale revision")
	}
	loaded, err := readCustomAiPrompts(manifest)
	if err != nil || loaded.Revision != 2 || len(loaded.Prompts) != 0 {
		t.Fatalf("replacement: %+v %v", loaded, err)
	}
}

func TestCustomAiPromptsImportAndDeleteInvalidateEditors(t *testing.T) {
	useTemporaryPluginData(t)
	manifest := Manifest{ID: "import-test"}
	_, err := mutateCustomAiPrompts(manifest, "save", json.RawMessage(`{"revision":0,"prompt":{"title":"First","body":"x"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := pluginKVReplaceNamespacesRepository(pluginDataSnapshot{manifest.ID: {customAiPromptsKey: json.RawMessage(`{"version":1,"revision":0,"prompts":[]}`)}}); err != nil {
		t.Fatal(err)
	}
	loaded, err := readCustomAiPrompts(manifest)
	if err != nil || loaded.Revision != 2 {
		t.Fatalf("import revision: %+v %v", loaded, err)
	}
	if err := pluginKVDeleteRaw(manifest.ID, customAiPromptsKey); err != nil {
		t.Fatal(err)
	}
	loaded, err = readCustomAiPrompts(manifest)
	if err != nil || loaded.Revision != 3 || len(loaded.Prompts) != 0 {
		t.Fatalf("delete revision: %+v %v", loaded, err)
	}
}

func TestCustomAiPromptsCorruptReplacementKeepsRevisionClock(t *testing.T) {
	useTemporaryPluginData(t)
	manifest := Manifest{ID: "corrupt-test"}
	if _, err := mutateCustomAiPrompts(manifest, "save", json.RawMessage(`{"revision":0,"prompt":{"title":"First","body":"x"}}`)); err != nil {
		t.Fatal(err)
	}
	if err := pluginKVSetRaw(manifest.ID, customAiPromptsKey, json.RawMessage(`null`)); err != nil {
		t.Fatal(err)
	}
	if _, err := readCustomAiPrompts(manifest); err == nil {
		t.Fatal("corrupt document accepted")
	}
	if err := pluginKVSetRaw(manifest.ID, customAiPromptsKey, json.RawMessage(`{"version":1,"revision":0,"prompts":[]}`)); err != nil {
		t.Fatal(err)
	}
	loaded, err := readCustomAiPrompts(manifest)
	if err != nil || loaded.Revision != 3 {
		t.Fatalf("corruption reset revision clock: %+v %v", loaded, err)
	}
}
