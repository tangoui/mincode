package plugin

import (
	"archive/zip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const dataTestPlugin = "data.plugin"

func pluginDataTestManager() (*pluginManager, *pluginRecord, *testLifecyclePlugin) {
	manager := &pluginManager{records: map[string]*pluginRecord{}}
	lifecycle := &testLifecyclePlugin{}
	record := &pluginRecord{id: dataTestPlugin, source: pluginSourceDevelopment, builtin: lifecycle,
		enabled: true, scope: pluginScopeGlobal, state: pluginStateRunning}
	manager.records[record.id] = record
	manager.order = append(manager.order, record.id)
	return manager, record, lifecycle
}

func mustSetPluginKV(t *testing.T, id, key, value string) {
	t.Helper()
	if err := pluginKVSetRaw(id, key, json.RawMessage(value)); err != nil {
		t.Fatal(err)
	}
}

func mustWriteStorage(t *testing.T, id, name, content string) {
	t.Helper()
	if err := pluginStorageWrite(id, name, 0, []byte(content), true); err != nil {
		t.Fatal(err)
	}
}

func storageContent(t *testing.T, id string) map[string]string {
	t.Helper()
	out := map[string]string{}
	if err := walkPluginStorage(id, func(name string, source io.Reader) error {
		data, err := io.ReadAll(source)
		out[name] = string(data)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func kvContent(t *testing.T, id string) map[string]string {
	t.Helper()
	values, err := pluginKVNamespaceRepository(id)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for key, value := range values {
		out[key] = string(value)
	}
	return out
}

// seedSourceMachine stores a knowledge-base-like plugin: KV that references
// bodies kept in file storage, plus another plugin and the host namespace.
func seedSourceMachine(t *testing.T) {
	useTemporaryPluginData(t)
	mustSetPluginKV(t, hostPluginID, dataTestPlugin, "true")
	mustSetPluginKV(t, dataTestPlugin, "doc.1", `{"contentRef":"bodies/1.txt"}`)
	mustWriteStorage(t, dataTestPlugin, "bodies/1.txt", "正文 <1> \"quoted\"")
	mustWriteStorage(t, dataTestPlugin, "bodies/2.bin", "\x00\x01\xff")
	mustSetPluginKV(t, "kv.only", "profiles", `[1,2]`)
}

// seedTargetMachine switches to a fresh data directory with stale data that an import must replace.
func seedTargetMachine(t *testing.T) (*pluginManager, *pluginRecord, *testLifecyclePlugin) {
	useTemporaryPluginData(t)
	mustSetPluginKV(t, dataTestPlugin, "stale", `1`)
	mustWriteStorage(t, dataTestPlugin, "old.txt", "old")
	mustSetPluginKV(t, "untouched", "keep", `"yes"`)
	return pluginDataTestManager()
}

func TestConfigExportCarriesPluginFilesAndRestoresThem(t *testing.T) {
	seedSourceMachine(t)
	target := filepath.Join(t.TempDir(), "config.json")
	config := map[string]json.RawMessage{
		"format": json.RawMessage(`"mincode-config"`), "version": json.RawMessage(`2`),
		"values": json.RawMessage(`{"app_settings":{"storage":"plain","value":{"a":1}}}`), "plugins": json.RawMessage(`"ignored"`),
	}
	summaries, err := writeConfigExportWithPlugins(target, config)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(summaries, []pluginDataSummary{{ID: dataTestPlugin, Keys: 1, Files: 2, Bytes: int64(len("正文 <1> \"quoted\"") + 3)}, {ID: "kv.only", Keys: 1}}) {
		t.Fatalf("summaries = %+v", summaries)
	}
	var written map[string]any
	raw, _ := os.ReadFile(target)
	if err := json.Unmarshal(raw, &written); err != nil {
		t.Fatalf("export is not valid JSON: %v\n%s", err, raw)
	}
	if written["format"] != "mincode-config" || written["plugins"].(map[string]any)[hostPluginID] != nil {
		t.Fatalf("export = %s", raw)
	}

	manager, record, lifecycle := seedTargetMachine(t)
	read, stages, err := readConfigExportFile(target, false)
	if err != nil || len(stages) != 2 || string(read["version"]) != "2" || read["plugins"] != nil {
		t.Fatalf("read: %v %v %v", err, stages, read)
	}
	if len(storageContent(t, dataTestPlugin)) != 1 {
		t.Fatal("validation pass must not write")
	}
	_, stages, err = readConfigExportFile(target, true)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupStages(stages)
	result, err := manager.applyPluginDataStages(context.Background(), stages)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Imported, []string{dataTestPlugin, "kv.only"}) || !reflect.DeepEqual(result.NotInstalled, []string{"kv.only"}) {
		t.Fatalf("result = %+v", result)
	}
	if got := storageContent(t, dataTestPlugin); !reflect.DeepEqual(got, map[string]string{"bodies/1.txt": "正文 <1> \"quoted\"", "bodies/2.bin": "\x00\x01\xff"}) {
		t.Fatalf("files = %q", got)
	}
	if got := kvContent(t, dataTestPlugin); !reflect.DeepEqual(got, map[string]string{"doc.1": `{"contentRef":"bodies/1.txt"}`}) {
		t.Fatalf("kv = %v", got)
	}
	if kvContent(t, "untouched")["keep"] != `"yes"` || kvContent(t, "kv.only")["profiles"] != `[1,2]` {
		t.Fatal("other namespaces wrong")
	}
	if _, found, _ := pluginKVGetRaw(hostPluginID, dataTestPlugin); found {
		t.Fatal("host namespace travelled with the export")
	}
	if lifecycle.shutdownCalls.Load() != 1 || lifecycle.startupCalls.Load() != 1 || record.state != pluginStateRunning {
		t.Fatalf("running plugin not restarted: %d %d %s", lifecycle.shutdownCalls.Load(), lifecycle.startupCalls.Load(), record.state)
	}
}

func TestConfigImportRejectsBadPluginDataWithoutWriting(t *testing.T) {
	for name, tc := range map[string]struct{ plugins, want string }{
		"host namespace": {`{"__host__":{"kv":{"x":1}}}`, "宿主命名空间"},
		"bad json value": {`{"p":{"kv":{"a:b":1}}}`, "非法字符"},
		"escape path":    {`{"p":{"files":{"../x":"eA=="}}}`, "无效"},
		"bad base64":     {`{"p":{"files":{"x":"***"}}}`, "无效"},
		"unknown field":  {`{"p":{"secrets":{}}}`, "格式无效"},
		"duplicate":      {`{"p":{},"p":{}}`, "重复"},
	} {
		t.Run(name, func(t *testing.T) {
			useTemporaryPluginData(t)
			source := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(source, []byte(`{"format":"mincode-config","plugins":`+tc.plugins+`}`), 0o600); err != nil {
				t.Fatal(err)
			}
			_, _, err := readConfigExportFile(source, true)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v", err)
			}
			entries, _ := os.ReadDir(filepath.Join(userDataDir(), pluginDataDirectory, "plugin-storage-import"))
			if len(entries) != 0 {
				t.Fatalf("staging left behind: %v", entries)
			}
		})
	}
}

func TestPluginDataArchiveRoundTrip(t *testing.T) {
	seedSourceMachine(t)
	source, _, _ := pluginDataTestManager()
	archivePath := filepath.Join(t.TempDir(), "data.zip")
	manifest, err := source.ExportPluginDataArchive(dataTestPlugin, archivePath)
	if err != nil || manifest.Keys != 1 || manifest.Files != 2 || manifest.PluginID != dataTestPlugin {
		t.Fatalf("export: %+v %v", manifest, err)
	}

	manager, _, lifecycle := seedTargetMachine(t)
	imported, err := manager.ImportPluginDataArchive(context.Background(), dataTestPlugin, archivePath)
	if err != nil || imported.Files != 2 || imported.Keys != 1 {
		t.Fatalf("import: %+v %v", imported, err)
	}
	if got := storageContent(t, dataTestPlugin); len(got) != 2 || got["bodies/2.bin"] != "\x00\x01\xff" {
		t.Fatalf("files = %q", got)
	}
	if got := kvContent(t, dataTestPlugin); len(got) != 1 || got["doc.1"] == "" {
		t.Fatalf("kv = %v", got)
	}
	if kvContent(t, "untouched")["keep"] != `"yes"` || lifecycle.startupCalls.Load() != 1 {
		t.Fatal("import touched another plugin or did not restart")
	}

	manager.records["other"] = &pluginRecord{id: "other", builtin: &testLifecyclePlugin{}, state: pluginStateStopped}
	if _, err := manager.ImportPluginDataArchive(context.Background(), "other", archivePath); err == nil || !strings.Contains(err.Error(), "不能导入") {
		t.Fatalf("archive of another plugin accepted: %v", err)
	}
}

func TestPluginDataArchiveRejectsUnsafeEntries(t *testing.T) {
	for name, entries := range map[string]map[string]string{
		"traversal":     {"files/../../evil": "x"},
		"unknown entry": {"notes.txt": "x"},
		"bad kv":        {"kv.json": `{"k":`},
	} {
		t.Run(name, func(t *testing.T) {
			manager, _, lifecycle := seedTargetMachine(t)
			archivePath := filepath.Join(t.TempDir(), "bad.zip")
			file, _ := os.Create(archivePath)
			writer := zip.NewWriter(file)
			entries[pluginDataArchiveManifestName] = `{"format":"mincode-plugin-data","version":1,"pluginId":"` + dataTestPlugin + `"}`
			for entryName, content := range entries {
				entry, _ := writer.Create(entryName)
				_, _ = entry.Write([]byte(content))
			}
			_ = writer.Close()
			_ = file.Close()
			if _, err := manager.ImportPluginDataArchive(context.Background(), dataTestPlugin, archivePath); err == nil {
				t.Fatal("unsafe archive accepted")
			}
			if storageContent(t, dataTestPlugin)["old.txt"] != "old" || kvContent(t, dataTestPlugin)["stale"] != "1" || lifecycle.shutdownCalls.Load() != 0 {
				t.Fatal("rejected archive changed data or stopped the plugin")
			}
		})
	}
}
