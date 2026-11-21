package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mincode/app/plugins/api"
)

func TestPluginStorageChunkAndTruncate(t *testing.T) {
	useTemporaryPluginData(t)
	id := "files.plugin"
	if err := pluginStorageWrite(id, "nested/data.bin", 0, []byte("hello world"), true); err != nil {
		t.Fatal(err)
	}
	if err := pluginStorageWrite(id, "nested/data.bin", 0, []byte("hi"), true); err != nil {
		t.Fatal(err)
	}
	data, eof, err := pluginStorageRead(id, "nested/data.bin", 0, 2)
	if err != nil || string(data) != "hi" || !eof {
		t.Fatalf("truncate read: %q %v %v", data, eof, err)
	}
	if err := pluginStorageWrite(id, "nested/data.bin", 2, []byte("!"), false); err != nil {
		t.Fatal(err)
	}
	data, eof, err = pluginStorageRead(id, "nested/data.bin", 1, 2)
	if err != nil || string(data) != "i!" || !eof {
		t.Fatalf("read: %q %v %v", data, eof, err)
	}
	files, err := pluginStorageList(id, "nested")
	if err != nil || len(files) != 1 || files[0] != "nested/data.bin" {
		t.Fatalf("list: %v %v", files, err)
	}
	meta, err := pluginStorageStat(id, "nested/data.bin")
	if err != nil || meta == nil {
		t.Fatalf("stat: %v %v", meta, err)
	}
	if err := pluginStorageDelete(id, "nested/data.bin"); err != nil {
		t.Fatal(err)
	}
	if files, err := pluginStorageList(id, "nested"); err != nil || len(files) != 0 {
		t.Fatalf("deleted list: %v %v", files, err)
	}
	if err := pluginStorageWrite(id, "empty", 0, nil, true); err != nil {
		t.Fatal(err)
	}
	if data, eof, err := pluginStorageRead(id, "empty", 0, 1); err != nil || len(data) != 0 || !eof {
		t.Fatalf("empty: %q %v %v", data, eof, err)
	}
}

func TestPluginStoragePathSymlinksAndQuota(t *testing.T) {
	useTemporaryPluginData(t)
	root, _ := pluginStorageRoot("files.plugin")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../outside", "safe/../outside", "/outside", "link/data", "a\\b"} {
		if err := pluginStorageWrite("files.plugin", name, 0, []byte("x"), true); err == nil {
			t.Errorf("accepted path %q", name)
		}
	}
	if _, _, err := pluginStorageRead("files.plugin", "link/data", 0, 1); err == nil {
		t.Fatal("read traversed symlink")
	}
	if err := os.Remove(filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < pluginStorageMaxFiles; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("file-%d", i)), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := pluginStorageWrite("files.plugin", "extra", 0, []byte("x"), true); err == nil {
		t.Fatal("file count exceeded")
	}
	if err := pluginStorageWrite("files.plugin", "file-0", 0, make([]byte, pluginStorageMaxChunkBytes+1), true); err == nil {
		t.Fatal("oversized chunk accepted")
	}
	if err := pluginStorageWrite("files.plugin", "file-0", pluginStorageMaxFileBytes, []byte("x"), true); err == nil {
		t.Fatal("oversized file accepted")
	}
}

func TestPluginStorageRejectsParentSymlinkAndTotalQuota(t *testing.T) {
	useTemporaryPluginData(t)
	root, err := pluginStorageRoot("quota.plugin")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "redirect")); err != nil {
		t.Fatal(err)
	}
	if err := pluginStorageWrite("quota.plugin", "redirect/data", 0, []byte("x"), true); err == nil {
		t.Fatal("parent symlink accepted")
	}
	if err := os.Remove(filepath.Join(root, "redirect")); err != nil {
		t.Fatal(err)
	}
	// Sparse files exercise the aggregate quota without allocating 1 GiB in memory.
	for index := 0; index < int(pluginStorageMaxBytes/pluginStorageMaxFileBytes); index++ {
		file, err := os.Create(filepath.Join(root, fmt.Sprintf("large-%d", index)))
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(pluginStorageMaxFileBytes); err != nil {
			file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := pluginStorageWrite("quota.plugin", "overflow", 0, []byte("x"), true); err == nil {
		t.Fatal("aggregate quota exceeded")
	}
}

func TestPluginStorageRPCSDKContract(t *testing.T) {
	useTemporaryPluginData(t)
	call := func(capabilities []api.Capability, method, params string) rpcEnvelope {
		t.Helper()
		h := contextRuntime(t, capabilities, nil)
		replies := drainRuntimeWrites(h)
		h.handleHostRequest(1, rpcEnvelope{ID: json.RawMessage(`"1"`), Method: method, Params: json.RawMessage(params)})
		return <-replies
	}
	allowed := []api.Capability{api.CapabilityStorage}
	if r := call(nil, "host.storage.file.write", `{"path":"a","data":"eA==","truncate":true}`); r.Error == nil || r.Error.Code != -32001 {
		t.Fatalf("capability: %+v", r)
	}
	if r := call(allowed, "host.storage.file.write", `{"path":"a","data":"eA==","truncate":true}`); r.Error != nil || string(r.Result) != "true" {
		t.Fatalf("write: %+v", r)
	}
	if r := call(allowed, "host.storage.file.read", `{"path":"a","offset":0,"maxBytes":1}`); r.Error != nil || !strings.Contains(string(r.Result), `"nextOffset":1`) || !strings.Contains(string(r.Result), `"data":"eA=="`) {
		t.Fatalf("read: %+v", r)
	}
	if r := call(allowed, "host.storage.file.list", `{"path":""}`); r.Error != nil || string(r.Result) != `["a"]` {
		t.Fatalf("list: %+v", r)
	}
	if r := call(allowed, "host.storage.file.stat", `{"path":"a"}`); r.Error != nil || !strings.Contains(string(r.Result), `"size":1`) {
		t.Fatalf("stat: %+v", r)
	}
	if r := call(allowed, "host.storage.file.write", `{"path":"a","data":"!"}`); r.Error == nil {
		t.Fatalf("invalid base64: %+v", r)
	}
	if r := call(allowed, "host.storage.file.delete", `{"path":"a"}`); r.Error != nil || string(r.Result) != "true" {
		t.Fatalf("delete: %+v", r)
	}
	if r := call(allowed, "host.storage.file.read", `{"path":"a","offset":0,"maxBytes":1}`); r.Error != nil || string(r.Result) != "null" {
		t.Fatalf("missing read: %+v", r)
	}
}

func TestPluginStorageClearDataWithKV(t *testing.T) {
	useTemporaryPluginData(t)
	id := "clear.plugin"
	manager := &pluginManager{records: map[string]*pluginRecord{id: {id: id}}}
	if err := pluginStorageWrite(id, "data", 0, []byte("secret"), true); err != nil {
		t.Fatal(err)
	}
	if err := pluginKVSetRaw(id, "key", json.RawMessage(`"value"`)); err != nil {
		t.Fatal(err)
	}
	root, _ := pluginStorageRoot(id)
	if err := manager.ClearPluginData(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("files remain: %v", err)
	}
	if _, found, err := pluginKVGetRaw(id, "key"); err != nil || found {
		t.Fatalf("KV remains: %v %v", found, err)
	}
}
