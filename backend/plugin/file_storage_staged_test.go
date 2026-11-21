package plugin

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mincode/app/plugins/api"
)

func stagedFiles(t *testing.T, root string) []string {
	t.Helper()
	var found []string
	_ = filepath.WalkDir(root, func(name string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && strings.HasPrefix(entry.Name(), pluginStorageTempPrefix) {
			found = append(found, name)
		}
		return nil
	})
	return found
}

func mustBegin(t *testing.T, owner any, id, name string) string {
	t.Helper()
	result, err := pluginStorageBegin(owner, id, name)
	if err != nil {
		t.Fatalf("begin %s: %v", name, err)
	}
	if !validStorageSessionID(result.Session) || result.MaxChunkBytes != pluginStorageMaxChunkBytes {
		t.Fatalf("begin result %+v", result)
	}
	return result.Session
}

func readWhole(t *testing.T, id, name string) []byte {
	t.Helper()
	var out []byte
	for offset := int64(0); ; {
		data, eof, err := pluginStorageRead(id, name, offset, pluginStorageMaxChunkBytes)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		out = append(out, data...)
		offset += int64(len(data))
		if eof {
			return out
		}
	}
}

func TestPluginStorageStagedWriteReplacesAtomically(t *testing.T) {
	useTemporaryPluginData(t)
	id := "staged.plugin"
	if err := pluginStorageWrite(id, "docs/a.bin", 0, []byte("old"), true); err != nil {
		t.Fatal(err)
	}
	session := mustBegin(t, nil, id, "docs/a.bin")
	payload := bytes.Repeat([]byte("0123456789abcdef"), (pluginStorageMaxChunkBytes*2+100)/16)
	for offset := 0; offset < len(payload); offset += pluginStorageMaxChunkBytes {
		end := min(offset+pluginStorageMaxChunkBytes, len(payload))
		if err := pluginStorageAppend(nil, id, session, int64(offset), payload[offset:end]); err != nil {
			t.Fatal(err)
		}
	}
	// Until commit the old content stays readable and the staging file is hidden.
	if got := readWhole(t, id, "docs/a.bin"); string(got) != "old" {
		t.Fatalf("visible before commit: %d bytes", len(got))
	}
	if files, err := pluginStorageList(id, ""); err != nil || len(files) != 1 || files[0] != "docs/a.bin" {
		t.Fatalf("list during staging: %v %v", files, err)
	}
	if err := pluginStorageCommit(nil, id, session, int64(len(payload)+1)); err == nil {
		t.Fatal("commit accepted a wrong size")
	}
	// A failed commit ends the session.
	if err := pluginStorageAppend(nil, id, session, int64(len(payload)), []byte("x")); err == nil {
		t.Fatal("session survived failed commit")
	}
	root, _ := pluginStorageRoot(id)
	if left := stagedFiles(t, root); len(left) != 0 {
		t.Fatalf("staging files left: %v", left)
	}
	session = mustBegin(t, nil, id, "docs/a.bin")
	if err := pluginStorageAppend(nil, id, session, 0, payload); err == nil {
		t.Fatal("oversized append accepted")
	}
	for offset := 0; offset < len(payload); offset += pluginStorageMaxChunkBytes {
		end := min(offset+pluginStorageMaxChunkBytes, len(payload))
		if err := pluginStorageAppend(nil, id, session, int64(offset), payload[offset:end]); err != nil {
			t.Fatal(err)
		}
	}
	if err := pluginStorageAppend(nil, id, session, 5, []byte("x")); err == nil {
		t.Fatal("non-sequential append accepted")
	}
	if err := pluginStorageCommit(nil, id, session, int64(len(payload))); err != nil {
		t.Fatal(err)
	}
	if got := readWhole(t, id, "docs/a.bin"); !bytes.Equal(got, payload) {
		t.Fatalf("committed %d bytes, want %d", len(got), len(payload))
	}
	if left := stagedFiles(t, root); len(left) != 0 {
		t.Fatalf("staging files left: %v", left)
	}
	// Empty files and new nested files commit too.
	session = mustBegin(t, nil, id, "new/deep/empty")
	if err := pluginStorageCommit(nil, id, session, 0); err != nil {
		t.Fatal(err)
	}
	if meta, err := pluginStorageStat(id, "new/deep/empty"); err != nil || meta.(map[string]any)["size"] != int64(0) {
		t.Fatalf("empty stat %v %v", meta, err)
	}
}

func TestPluginStorageStagedCommitRevalidatesPath(t *testing.T) {
	useTemporaryPluginData(t)
	id := "staged.paths"
	for _, name := range []string{"../x", "a/../x", "/x", "a\\b", ""} {
		if _, err := pluginStorageBegin(nil, id, name); err == nil {
			t.Errorf("begin accepted %q", name)
		}
	}
	root, _ := pluginStorageRoot(id)
	outside := t.TempDir()

	// Parent directory swapped for a symlink between begin and commit.
	session := mustBegin(t, nil, id, "dir/file")
	if err := pluginStorageAppend(nil, id, session, 0, []byte("data")); err != nil {
		t.Fatal(err)
	}
	reopen := closeStagingHandles(t, id)
	if err := os.Rename(filepath.Join(root, "dir"), filepath.Join(root, "moved")); err != nil {
		t.Fatal(err)
	}
	reopen("dir/", "moved/")
	if err := os.Symlink(outside, filepath.Join(root, "dir")); err != nil {
		t.Fatal(err)
	}
	if err := pluginStorageCommit(nil, id, session, 4); err == nil {
		t.Fatal("commit followed a parent symlink")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("wrote outside the sandbox: %v", entries)
	}
	if err := os.Remove(filepath.Join(root, "dir")); err != nil {
		t.Fatal(err)
	}

	// Target replaced by a directory between begin and commit.
	session = mustBegin(t, nil, id, "target")
	if err := os.Mkdir(filepath.Join(root, "target"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := pluginStorageCommit(nil, id, session, 0); err == nil {
		t.Fatal("commit replaced a directory")
	}
	// Only the tampered session's file (unreachable through the symlink) may
	// remain, inside the directory the "attacker" moved.
	for _, name := range stagedFiles(t, root) {
		if !strings.Contains(name, "moved") {
			t.Fatalf("staging file left: %s", name)
		}
	}

	// A target leaf swapped for a symlink is refused at commit as well.
	session = mustBegin(t, nil, id, "leaf")
	if err := os.WriteFile(filepath.Join(outside, "victim"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "victim"), filepath.Join(root, "leaf")); err != nil {
		t.Fatal(err)
	}
	if err := pluginStorageCommit(nil, id, session, 0); err == nil {
		t.Fatal("commit replaced a symlink")
	}
	if data, _ := os.ReadFile(filepath.Join(outside, "victim")); string(data) != "keep" {
		t.Fatal("symlink target modified")
	}
}

// closeStagingHandles closes staging handles without ending the sessions, so a
// test can tamper with the tree like an external actor (Windows cannot rename a
// directory holding open files). reopen points the sessions at the moved files.
func closeStagingHandles(t *testing.T, id string) (reopen func(from, to string)) {
	t.Helper()
	state, err := pluginStorageStateFor(id, false)
	if err != nil || state == nil {
		t.Fatal("no storage state")
	}
	root, _ := pluginStorageRoot(id)
	state.mu.Lock()
	for _, session := range state.sessions {
		_ = session.file.Close()
	}
	state.mu.Unlock()
	return func(from, to string) {
		state.mu.Lock()
		defer state.mu.Unlock()
		for _, session := range state.sessions {
			moved := filepath.Join(root, filepath.FromSlash(strings.Replace(session.stage, from, to, 1)))
			file, err := os.OpenFile(moved, os.O_WRONLY|os.O_APPEND, 0)
			if err != nil {
				t.Fatal(err)
			}
			session.file = file
		}
	}
}

func TestPluginStorageStagedCommitRechecksQuotaAndCount(t *testing.T) {
	useTemporaryPluginData(t)
	id := "staged.quota"
	root, _ := pluginStorageRoot(id)

	// File count filled externally after begin: commit of a new file fails.
	session := mustBegin(t, nil, id, "extra")
	for i := 0; i < pluginStorageMaxFiles; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("file-%d", i)), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := pluginStorageCommit(nil, id, session, 0); err == nil || !strings.Contains(err.Error(), "count") {
		t.Fatalf("commit ignored file count: %v", err)
	}
	// Replacing an existing file is still fine at the count limit.
	session = mustBegin(t, nil, id, "file-0")
	if err := pluginStorageAppend(nil, id, session, 0, []byte("yy")); err != nil {
		t.Fatal(err)
	}
	if err := pluginStorageCommit(nil, id, session, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := pluginStorageBegin(nil, id, "another"); err == nil {
		t.Fatal("begin accepted a new file at the count limit")
	}
	for i := 0; i < pluginStorageMaxFiles; i++ {
		_ = os.Remove(filepath.Join(root, fmt.Sprintf("file-%d", i)))
	}
	pluginStorageMu.Lock() // namespace-wide change: drop the cache
	pluginStorageMu.Unlock()

	// Total quota filled externally (sparse files) after begin.
	session = mustBegin(t, nil, id, "overflow")
	if err := pluginStorageAppend(nil, id, session, 0, []byte("x")); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < int(pluginStorageMaxBytes/pluginStorageMaxFileBytes); index++ {
		file, err := os.Create(filepath.Join(root, fmt.Sprintf("large-%d", index)))
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(pluginStorageMaxFileBytes); err != nil {
			file.Close()
			t.Fatal(err)
		}
		file.Close()
	}
	if err := pluginStorageCommit(nil, id, session, 1); err == nil || !strings.Contains(err.Error(), "quota") {
		t.Fatalf("commit ignored quota: %v", err)
	}
	// The commit walk refreshed the cache: appends are refused now too.
	session = mustBegin(t, nil, id, "large-0")
	if err := pluginStorageAppend(nil, id, session, 0, []byte("ok")); err != nil {
		t.Fatalf("rewrite within quota refused: %v", err)
	}
	if err := pluginStorageAbort(nil, id, session); err != nil {
		t.Fatal(err)
	}
	session = mustBegin(t, nil, id, "small")
	if err := pluginStorageAppend(nil, id, session, 0, []byte("x")); err == nil {
		t.Fatal("append beyond quota accepted")
	}
	if err := pluginStorageWrite(id, "direct", 0, []byte("x"), true); err == nil {
		t.Fatal("direct write beyond quota accepted")
	}
	_ = pluginStorageAbort(nil, id, session)

	// Per-file limit on append.
	for index := 0; index < int(pluginStorageMaxBytes/pluginStorageMaxFileBytes); index++ {
		_ = os.Remove(filepath.Join(root, fmt.Sprintf("large-%d", index)))
	}
	pluginStorageMu.Lock()
	pluginStorageMu.Unlock()
	session = mustBegin(t, nil, id, "big")
	state, _ := pluginStorageStateFor(id, false)
	state.mu.Lock()
	state.sessions[session].size = pluginStorageMaxFileBytes // pretend it is full
	state.mu.Unlock()
	if err := pluginStorageAppend(nil, id, session, pluginStorageMaxFileBytes, []byte("x")); err == nil {
		t.Fatal("append beyond the per-file limit accepted")
	}
	_ = pluginStorageAbort(nil, id, session)
}

func TestPluginStorageStagedCleanup(t *testing.T) {
	useTemporaryPluginData(t)
	id := "staged.cleanup"
	root, _ := pluginStorageRoot(id)

	session := mustBegin(t, nil, id, "a")
	if err := pluginStorageAppend(nil, id, session, 0, []byte("data")); err != nil {
		t.Fatal(err)
	}
	if err := pluginStorageAbort(nil, id, session); err != nil {
		t.Fatal(err)
	}
	if err := pluginStorageAbort(nil, id, session); err != nil {
		t.Fatalf("second abort: %v", err)
	}
	if left := stagedFiles(t, root); len(left) != 0 {
		t.Fatalf("abort left %v", left)
	}
	if _, err := pluginStorageStat(id, "a"); err != nil {
		t.Fatal(err)
	}
	if meta, _ := pluginStorageStat(id, "a"); meta != nil {
		t.Fatal("aborted write became visible")
	}

	// Idle timeout.
	previous := pluginStorageSessionIdle
	pluginStorageSessionIdle = 50 * time.Millisecond
	session = mustBegin(t, nil, id, "slow")
	pluginStorageSessionIdle = previous
	deadline := time.Now().Add(5 * time.Second)
	for len(stagedFiles(t, root)) != 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if left := stagedFiles(t, root); len(left) != 0 {
		t.Fatalf("timeout left %v", left)
	}
	if err := pluginStorageAppend(nil, id, session, 0, []byte("x")); err == nil {
		t.Fatal("expired session accepted an append")
	}

	// Plugin stop (record lifecycle hook) drops every session.
	first := mustBegin(t, nil, id, "one")
	second := mustBegin(t, nil, id, "two/nested")
	manager := &pluginManager{records: map[string]*pluginRecord{}}
	manager.runRecordLifecycleHooks(&pluginRecord{id: id})
	if left := stagedFiles(t, root); len(left) != 0 {
		t.Fatalf("stop left %v", left)
	}
	for _, s := range []string{first, second} {
		if err := pluginStorageCommit(nil, id, s, 0); err == nil {
			t.Fatal("session survived plugin stop")
		}
	}
}

func TestPluginStorageStagedSessionLimitAndOwner(t *testing.T) {
	useTemporaryPluginData(t)
	id := "staged.limit"
	owner, other := &struct{ int }{1}, &struct{ int }{2}
	sessions := []string{}
	for i := 0; i < pluginStorageMaxSessions; i++ {
		sessions = append(sessions, mustBegin(t, owner, id, fmt.Sprintf("f%d", i)))
	}
	if _, err := pluginStorageBegin(owner, id, "overflow"); err == nil {
		t.Fatal("session limit not enforced")
	}
	// Re-beginning the same path replaces the abandoned session.
	replaced := mustBegin(t, owner, id, "f0")
	if err := pluginStorageAppend(owner, id, sessions[0], 0, []byte("x")); err == nil {
		t.Fatal("superseded session still usable")
	}
	sessions[0] = replaced
	// Another owner (runtime generation) cannot touch the sessions.
	if err := pluginStorageAppend(other, id, sessions[1], 0, []byte("x")); err == nil {
		t.Fatal("foreign owner appended")
	}
	if err := pluginStorageCommit(other, id, sessions[1], -1); err == nil {
		t.Fatal("foreign owner committed")
	}
	// Sessions of other plugins are independent.
	extra := mustBegin(t, owner, "staged.other", "f")
	if err := pluginStorageAbort(owner, "staged.other", extra); err != nil {
		t.Fatal(err)
	}
	if err := pluginStorageAbort(owner, id, sessions[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := pluginStorageBegin(owner, id, "overflow"); err != nil {
		t.Fatalf("slot not released: %v", err)
	}
	abortPluginStorageSessions(id)
}

func TestPluginStorageStagedRPCContract(t *testing.T) {
	useTemporaryPluginData(t)
	allowed := []api.Capability{api.CapabilityStorage}
	h := contextRuntime(t, allowed, nil)
	replies := drainRuntimeWrites(h)
	call := func(method, params string) rpcEnvelope {
		t.Helper()
		h.handleHostRequest(1, rpcEnvelope{ID: json.RawMessage(`"1"`), Method: method, Params: json.RawMessage(params)})
		return <-replies
	}
	denied := contextRuntime(t, nil, nil)
	deniedReplies := drainRuntimeWrites(denied)
	denied.handleHostRequest(1, rpcEnvelope{ID: json.RawMessage(`"1"`), Method: storageWriteBeginMethod, Params: json.RawMessage(`{"path":"a"}`)})
	if r := <-deniedReplies; r.Error == nil || r.Error.Code != -32001 {
		t.Fatalf("capability: %+v", r)
	}
	r := call(storageWriteBeginMethod, `{"path":"big.bin"}`)
	if r.Error != nil {
		t.Fatalf("begin: %+v", r.Error)
	}
	var begin pluginStorageBeginResult
	if err := json.Unmarshal(r.Result, &begin); err != nil || begin.MaxChunkBytes != 512*1024 || begin.IdleTimeoutMs <= 0 {
		t.Fatalf("begin result %s", r.Result)
	}
	chunk := bytes.Repeat([]byte{7}, pluginStorageMaxChunkBytes)
	encoded := base64.StdEncoding.EncodeToString(chunk)
	for i := 0; i < 2; i++ {
		r = call(storageWriteAppendMethod, fmt.Sprintf(`{"session":%q,"offset":%d,"data":%q}`, begin.Session, i*len(chunk), encoded))
		if r.Error != nil || !strings.Contains(string(r.Result), fmt.Sprintf(`"size":%d`, (i+1)*len(chunk))) {
			t.Fatalf("append: %+v", r)
		}
	}
	if r = call(storageWriteAppendMethod, fmt.Sprintf(`{"session":%q,"offset":0,"data":"!"}`, begin.Session)); r.Error == nil {
		t.Fatal("invalid base64 accepted")
	}
	if r = call(storageWriteCommitMethod, `{"session":"nope"}`); r.Error == nil || r.Error.Code != -32602 {
		t.Fatalf("bad session id: %+v", r)
	}
	if r = call(storageWriteCommitMethod, fmt.Sprintf(`{"session":%q,"size":%d}`, begin.Session, 2*len(chunk))); r.Error != nil || string(r.Result) != "true" {
		t.Fatalf("commit: %+v", r)
	}
	// A 512 KiB read frame fits in one JSON-RPC line.
	r = call("host.storage.file.read", fmt.Sprintf(`{"path":"big.bin","offset":0,"maxBytes":%d}`, pluginStorageMaxChunkBytes))
	if r.Error != nil || !strings.Contains(string(r.Result), fmt.Sprintf(`"nextOffset":%d`, pluginStorageMaxChunkBytes)) {
		t.Fatalf("read: %v", r.Error)
	}
	r = call(storageWriteBeginMethod, `{"path":"gone"}`)
	_ = json.Unmarshal(r.Result, &begin)
	if r = call(storageWriteAbortMethod, fmt.Sprintf(`{"session":%q}`, begin.Session)); r.Error != nil || string(r.Result) != "true" {
		t.Fatalf("abort: %+v", r)
	}
	if r = call(storageWriteCommitMethod, fmt.Sprintf(`{"session":%q}`, begin.Session)); r.Error == nil {
		t.Fatal("aborted session committed")
	}
	description, _ := json.Marshal(h.describeAPI())
	for _, method := range []string{storageWriteBeginMethod, storageWriteAppendMethod, storageWriteCommitMethod, storageWriteAbortMethod} {
		if !strings.Contains(string(description), fmt.Sprintf(`{"available":true,"capability":"storage","name":%q}`, method)) {
			t.Fatalf("describe lacks %s: %s", method, description)
		}
	}
}

func TestPluginStorageDirectWriteKeepsCacheConsistent(t *testing.T) {
	useTemporaryPluginData(t)
	id := "cache.plugin"
	if err := pluginStorageWrite(id, "a", 0, bytes.Repeat([]byte("a"), pluginStorageMaxChunkBytes), true); err != nil {
		t.Fatalf("512 KiB direct chunk: %v", err)
	}
	if err := pluginStorageWrite(id, "a", 4, []byte("XY"), false); err != nil {
		t.Fatal(err)
	}
	if err := pluginStorageWrite(id, "a", int64(pluginStorageMaxChunkBytes), []byte("tail"), false); err != nil {
		t.Fatal(err)
	}
	got := readWhole(t, id, "a")
	if len(got) != pluginStorageMaxChunkBytes+4 || string(got[:8]) != "aaaaXYaa" || string(got[len(got)-4:]) != "tail" {
		t.Fatalf("patched content wrong: %d %q", len(got), got[:8])
	}
	if err := pluginStorageWrite(id, "b", 0, []byte("bb"), true); err != nil {
		t.Fatal(err)
	}
	if err := pluginStorageDelete(id, "b"); err != nil {
		t.Fatal(err)
	}
	state, _ := pluginStorageStateFor(id, false)
	root, err := openPluginStorage(id, false)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	files, total, err := state.walkUsage(root)
	state.mu.Lock()
	defer state.mu.Unlock()
	if err != nil || !state.usageKnown || state.files != files || state.bytes != total || files != 1 || total != int64(len(got)) {
		t.Fatalf("cache %d/%d want %d/%d (%v)", state.files, state.bytes, files, total, err)
	}
}

// A plugin can derive the staging file name from its session id; touching it
// through the ordinary file API would subtract bytes that were never counted
// and let later direct writes bypass the namespace quota.
func TestPluginStorageRejectsHostScratchNames(t *testing.T) {
	useTemporaryPluginData(t)
	id := "staged.plugin"
	session := mustBegin(t, nil, id, "big.bin")
	if err := pluginStorageAppend(nil, id, session, 0, bytes.Repeat([]byte("x"), 4096)); err != nil {
		t.Fatal(err)
	}
	stage := pluginStorageStagePrefix + session
	for _, name := range []string{stage, "dir/" + stage, pluginStorageTempPrefix + "0123456789abcdef0123456789abcdef", pluginStorageTempPrefix + "dir/file"} {
		if err := pluginStorageDelete(id, name); err == nil {
			t.Fatalf("delete %q accepted", name)
		}
		if err := pluginStorageWrite(id, name, 0, nil, true); err == nil {
			t.Fatalf("write %q accepted", name)
		}
		if _, _, err := pluginStorageRead(id, name, 0, 16); err == nil {
			t.Fatalf("read %q accepted", name)
		}
		if _, err := pluginStorageStat(id, name); err == nil {
			t.Fatalf("stat %q accepted", name)
		}
		if _, err := pluginStorageBegin(nil, id, name); err == nil {
			t.Fatalf("begin %q accepted", name)
		}
	}
	// The staged session itself is unaffected and still commits.
	if err := pluginStorageCommit(nil, id, session, 4096); err != nil {
		t.Fatal(err)
	}
	if got := readWhole(t, id, "big.bin"); len(got) != 4096 {
		t.Fatalf("committed %d bytes", len(got))
	}
}
