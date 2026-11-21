package plugin

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"mincode/app/plugins/api"
)

type customEditorFixture struct {
	manager *pluginManager
	record  *pluginRecord
	runtime *processRuntime
	project string
	staging string
}

func newCustomEditorFixture(t *testing.T, access string, capabilities ...api.Capability) *customEditorFixture {
	t.Helper()
	dataDir := useTemporaryPluginData(t)
	uiHost := newPluginUIHost()
	t.Cleanup(uiHost.Shutdown)
	pluginRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(pluginRoot, "ui"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginRoot, "ui", "index.html"), []byte("<!doctype html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	editor := CustomEditorContribution{
		ID: "drawio", ViewID: "canvas", Title: "Draw", FilenamePatterns: []string{"*.drawio"},
		Priority: customEditorPriorityOption, Access: access, Icon: "box",
	}
	manifest := &Manifest{
		ID: "draw.plugin", APIVersion: 2, Capabilities: append([]api.Capability{api.CapabilityUI}, capabilities...),
		Contributes: ManifestContributions{Views: []ManifestView{{ID: "canvas", Title: "Canvas", Entry: "ui/index.html"}}},
		contributions: map[string]*resolvedContribution{
			customEditorPointKey: {Entries: []any{editor}},
		},
	}
	runtimeHost := &processRuntime{generation: 1}
	record := &pluginRecord{
		id: "draw.plugin", source: pluginSourceLocal, root: pluginRoot, enabled: true,
		state: pluginStateRunning, manifest: manifest, runtime: runtimeHost,
	}
	manager := &pluginManager{records: map[string]*pluginRecord{record.id: record}, order: []string{record.id}, uiHost: uiHost}
	project := t.TempDir()
	t.Cleanup(func() { customEditorDocuments.purgePlugin(record.id) })
	return &customEditorFixture{
		manager: manager, record: record, runtime: runtimeHost, project: project,
		staging: filepath.Join(dataDir, pluginDataDirectory, customEditorStagingDirectory),
	}
}

func (fixture *customEditorFixture) write(t *testing.T, relative, content string) string {
	t.Helper()
	target := filepath.Join(fixture.project, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
	return target
}

func (fixture *customEditorFixture) begin(relative string) (customEditorBeginDocumentResult, error) {
	return fixture.manager.BeginCustomEditorDocument(customEditorBeginDocumentArgs{
		PluginID: "draw.plugin", EditorID: "drawio", ProjectRoot: fixture.project,
		RelativePath: relative, ParentOrigin: "tango://app:9250",
	})
}

func (fixture *customEditorFixture) mustBegin(t *testing.T, relative string) customEditorBeginDocumentResult {
	t.Helper()
	result, err := fixture.begin(relative)
	if err != nil {
		t.Fatalf("begin %s: %v", relative, err)
	}
	return result
}

func (fixture *customEditorFixture) read(result customEditorBeginDocumentResult, offset, length int64) (customEditorReadResult, error) {
	return fixture.manager.ReadCustomEditorDocument(customEditorReadArgs{
		SessionID: result.Session.SessionID, Nonce: result.Session.Nonce, Offset: offset, Length: length,
	})
}

func (fixture *customEditorFixture) writeOp(result customEditorBeginDocumentResult, op, uploadID, data string, force bool) (customEditorWriteResult, error) {
	encoded := ""
	if data != "" {
		encoded = base64.StdEncoding.EncodeToString([]byte(data))
	}
	return fixture.manager.WriteCustomEditorDocument(customEditorWriteArgs{
		SessionID: result.Session.SessionID, Nonce: result.Session.Nonce, Op: op, UploadID: uploadID, Data: encoded, Force: force,
	})
}

func (fixture *customEditorFixture) save(t *testing.T, result customEditorBeginDocumentResult, content string, force bool) (customEditorWriteResult, error) {
	t.Helper()
	begun, err := fixture.writeOp(result, "begin", "", content, false)
	if err != nil {
		t.Fatalf("upload begin: %v", err)
	}
	return fixture.writeOp(result, "commit", begun.UploadID, "", force)
}

func expectCustomEditorCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s error, got nil", code)
	}
	if got := customEditorErrorCode(err); got != code || !strings.HasPrefix(err.Error(), code+": ") {
		t.Fatalf("expected %s, got %q", code, err.Error())
	}
}

func stagingEntries(t *testing.T, directory string) int {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func TestCustomEditorDocumentBeginAndRead(t *testing.T) {
	fixture := newCustomEditorFixture(t, customEditorAccessRead)
	fixture.write(t, "diagrams/Chart.drawio", "hello world")
	result := fixture.mustBegin(t, "diagrams/Chart.drawio")
	document := result.Document
	if document.Name != "Chart.drawio" || document.RelativePath != "diagrams/Chart.drawio" || document.Size != 11 ||
		document.Access != "read" || document.Path != "" || document.MaxWriteBytes != 0 || document.ModifiedAt <= 0 ||
		document.MaxReadBytes != customEditorReadMaxBytes || document.ChunkBytes != customEditorChunkBytes {
		t.Fatalf("unexpected document %+v", document)
	}
	if result.Session.ViewID != "canvas" || result.Session.Nonce == "" {
		t.Fatalf("unexpected session %+v", result.Session)
	}
	chunk, err := fixture.read(result, 6, 3)
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := base64.StdEncoding.DecodeString(chunk.Data); string(data) != "wor" || chunk.EOF || chunk.Size != 11 {
		t.Fatalf("unexpected chunk %+v", chunk)
	}
	chunk, err = fixture.read(result, 6, 0)
	if data, _ := base64.StdEncoding.DecodeString(chunk.Data); err != nil || string(data) != "world" || !chunk.EOF {
		t.Fatalf("chunk=%+v err=%v", chunk, err)
	}
	chunk, err = fixture.read(result, 50, 10)
	if err != nil || chunk.Data != "" || !chunk.EOF {
		t.Fatalf("past EOF chunk=%+v err=%v", chunk, err)
	}
	_, err = fixture.read(result, -1, 10)
	expectCustomEditorCode(t, err, customEditorErrInvalidRequest)
}

func TestCustomEditorDocumentReadChunksAreCapped(t *testing.T) {
	fixture := newCustomEditorFixture(t, customEditorAccessRead)
	previous := customEditorChunkBytes
	customEditorChunkBytes = 4
	t.Cleanup(func() { customEditorChunkBytes = previous })
	fixture.write(t, "a.drawio", "0123456789")
	result := fixture.mustBegin(t, "a.drawio")
	chunk, err := fixture.read(result, 0, 1<<20)
	if data, _ := base64.StdEncoding.DecodeString(chunk.Data); err != nil || string(data) != "0123" || chunk.EOF {
		t.Fatalf("chunk=%+v err=%v", chunk, err)
	}
}

func TestCustomEditorDocumentPathIncludedOnlyWithWorkspaceRead(t *testing.T) {
	fixture := newCustomEditorFixture(t, customEditorAccessRead, api.CapabilityWorkspaceRead)
	target := fixture.write(t, "a.drawio", "x")
	result := fixture.mustBegin(t, "a.drawio")
	canonical, _ := filepath.EvalSymlinks(target)
	if result.Document.Path != canonical {
		t.Fatalf("path %q, want %q", result.Document.Path, canonical)
	}
}

func TestCustomEditorDocumentBeginRejections(t *testing.T) {
	fixture := newCustomEditorFixture(t, customEditorAccessRead)
	fixture.write(t, "notes.md", "x")
	fixture.write(t, "a.drawio", "x")
	outside := filepath.Join(t.TempDir(), "secret.drawio")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(fixture.project, "escape.drawio")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(fixture.project, "notes.md"), filepath.Join(fixture.project, "alias.drawio")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(fixture.project, "folder.drawio"), 0o755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		path, code string
	}{
		{"notes.md", customEditorErrForbidden},
		{"escape.drawio", customEditorErrForbidden},
		{"alias.drawio", customEditorErrForbidden},
		{"folder.drawio", customEditorErrForbidden},
		{"missing.drawio", customEditorErrNotFound},
		{"../a.drawio", customEditorErrInvalidRequest},
		{"/etc/a.drawio", customEditorErrInvalidRequest},
	}
	for _, testCase := range cases {
		_, err := fixture.begin(testCase.path)
		if customEditorErrorCode(err) != testCase.code {
			t.Errorf("%s: expected %s, got %v", testCase.path, testCase.code, err)
		}
	}
	_, err := fixture.manager.BeginCustomEditorDocument(customEditorBeginDocumentArgs{
		PluginID: "draw.plugin", EditorID: "nope", ProjectRoot: fixture.project, RelativePath: "a.drawio", ParentOrigin: "tango://app:9250",
	})
	expectCustomEditorCode(t, err, customEditorErrNotFound)

	fixture.record.mu.Lock()
	fixture.record.enabled = false
	fixture.record.mu.Unlock()
	_, err = fixture.begin("a.drawio")
	expectCustomEditorCode(t, err, customEditorErrForbidden)
}

func TestCustomEditorDocumentSizeLimit(t *testing.T) {
	fixture := newCustomEditorFixture(t, customEditorAccessRead)
	previous := customEditorReadMaxBytes
	customEditorReadMaxBytes = 8
	t.Cleanup(func() { customEditorReadMaxBytes = previous })
	fixture.write(t, "big.drawio", "0123456789")
	_, err := fixture.begin("big.drawio")
	expectCustomEditorCode(t, err, customEditorErrTooLarge)

	fixture.write(t, "small.drawio", "0123")
	result := fixture.mustBegin(t, "small.drawio")
	fixture.write(t, "small.drawio", "0123456789")
	_, err = fixture.read(result, 0, 4)
	expectCustomEditorCode(t, err, customEditorErrTooLarge)
}

func TestCustomEditorDocumentSymlinkRetargetIsRejected(t *testing.T) {
	fixture := newCustomEditorFixture(t, customEditorAccessRead)
	fixture.write(t, "real.drawio", "real")
	fixture.write(t, "other.drawio", "other")
	link := filepath.Join(fixture.project, "link.drawio")
	if err := os.Symlink(filepath.Join(fixture.project, "real.drawio"), link); err != nil {
		t.Fatal(err)
	}
	result := fixture.mustBegin(t, "link.drawio")
	if _, err := fixture.read(result, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "x.drawio")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	_, err := fixture.read(result, 0, 0)
	expectCustomEditorCode(t, err, customEditorErrForbidden)
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(fixture.project, "other.drawio"), link); err != nil {
		t.Fatal(err)
	}
	_, err = fixture.read(result, 0, 0)
	expectCustomEditorCode(t, err, customEditorErrForbidden)
}

func TestCustomEditorDocumentSessionValidation(t *testing.T) {
	fixture := newCustomEditorFixture(t, customEditorAccessRead)
	fixture.write(t, "a.drawio", "abc")
	result := fixture.mustBegin(t, "a.drawio")

	_, err := fixture.manager.ReadCustomEditorDocument(customEditorReadArgs{SessionID: result.Session.SessionID, Nonce: "wrong"})
	expectCustomEditorCode(t, err, customEditorErrStaleSession)
	// A wrong nonce must not tear down the real binding.
	if _, err := fixture.read(result, 0, 0); err != nil {
		t.Fatalf("binding lost after wrong nonce: %v", err)
	}

	// A plain UI session (not opened through begin_document) has no document.
	plain, err := fixture.manager.BeginUI("draw.plugin", "canvas", "tango://app:9250")
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.manager.ReadCustomEditorDocument(customEditorReadArgs{SessionID: plain.SessionID, Nonce: plain.Nonce})
	expectCustomEditorCode(t, err, customEditorErrStaleSession)

	// Generation change (reload/crash restart) invalidates the binding.
	fixture.runtime.mu.Lock()
	fixture.runtime.generation = 2
	fixture.runtime.mu.Unlock()
	_, err = fixture.read(result, 0, 0)
	expectCustomEditorCode(t, err, customEditorErrStaleSession)
	if customEditorDocuments.get(result.Session.SessionID) != nil {
		t.Fatal("stale generation binding was not purged")
	}
}

func TestCustomEditorDocumentClosedSessionAndRemovedEditor(t *testing.T) {
	fixture := newCustomEditorFixture(t, customEditorAccessRead)
	fixture.write(t, "a.drawio", "abc")
	first := fixture.mustBegin(t, "a.drawio")
	fixture.manager.CloseUI(first.Session.SessionID)
	_, err := fixture.read(first, 0, 0)
	expectCustomEditorCode(t, err, customEditorErrStaleSession)
	if customEditorDocuments.get(first.Session.SessionID) != nil {
		t.Fatal("closed session binding was not purged")
	}

	second := fixture.mustBegin(t, "a.drawio")
	fixture.record.mu.Lock()
	fixture.record.manifest.contributions = map[string]*resolvedContribution{}
	fixture.record.mu.Unlock()
	_, err = fixture.read(second, 0, 0)
	expectCustomEditorCode(t, err, customEditorErrStaleSession)
}

func TestCustomEditorDocumentOpenDocumentsCap(t *testing.T) {
	fixture := newCustomEditorFixture(t, customEditorAccessRead)
	previous := customEditorOpenDocumentsCap
	customEditorOpenDocumentsCap = 2
	t.Cleanup(func() { customEditorOpenDocumentsCap = previous })
	fixture.write(t, "a.drawio", "abc")
	first := fixture.mustBegin(t, "a.drawio")
	fixture.mustBegin(t, "a.drawio")
	_, err := fixture.begin("a.drawio")
	expectCustomEditorCode(t, err, customEditorErrTooLarge)
	// Closing a session frees its slot (stale bindings are purged on begin).
	fixture.manager.CloseUI(first.Session.SessionID)
	fixture.mustBegin(t, "a.drawio")
}

func TestCustomEditorDocumentWriteRequiresReadWrite(t *testing.T) {
	fixture := newCustomEditorFixture(t, customEditorAccessRead)
	fixture.write(t, "a.drawio", "abc")
	result := fixture.mustBegin(t, "a.drawio")
	_, err := fixture.writeOp(result, "begin", "", "x", false)
	expectCustomEditorCode(t, err, customEditorErrForbidden)

	writable := newCustomEditorFixture(t, customEditorAccessReadWrite, api.CapabilityWorkspaceWrite)
	writable.write(t, "a.drawio", "abc")
	result = writable.mustBegin(t, "a.drawio")
	if result.Document.Access != "readWrite" || result.Document.MaxWriteBytes != customEditorWriteMaxBytes {
		t.Fatalf("unexpected document %+v", result.Document)
	}
	// Dropping workspace.write from the current manifest revokes writes.
	writable.record.mu.Lock()
	writable.record.manifest.Capabilities = []api.Capability{api.CapabilityUI}
	writable.record.mu.Unlock()
	_, err = writable.writeOp(result, "begin", "", "x", false)
	expectCustomEditorCode(t, err, customEditorErrForbidden)
}

func TestCustomEditorDocumentAtomicSave(t *testing.T) {
	fixture := newCustomEditorFixture(t, customEditorAccessReadWrite, api.CapabilityWorkspaceWrite)
	target := fixture.write(t, "a.drawio", "old")
	result := fixture.mustBegin(t, "a.drawio")
	begun, err := fixture.writeOp(result, "begin", "", "new ", false)
	if err != nil || begun.UploadID == "" || begun.Received == nil || *begun.Received != 4 {
		t.Fatalf("begin=%+v err=%v", begun, err)
	}
	appended, err := fixture.writeOp(result, "append", begun.UploadID, "content", false)
	if err != nil || *appended.Received != 11 {
		t.Fatalf("append=%+v err=%v", appended, err)
	}
	if stagingEntries(t, fixture.staging) != 1 {
		t.Fatal("upload is not staged in the private staging directory")
	}
	committed, err := fixture.writeOp(result, "commit", begun.UploadID, "!", false)
	if err != nil || !committed.Committed || *committed.Size != 12 || *committed.ModifiedAt <= 0 {
		t.Fatalf("commit=%+v err=%v", committed, err)
	}
	content, _ := os.ReadFile(target)
	if string(content) != "new content!" {
		t.Fatalf("content %q", content)
	}
	info, _ := os.Stat(target)
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v not preserved", info.Mode().Perm())
	}
	entries, _ := os.ReadDir(fixture.project)
	if len(entries) != 1 {
		t.Fatalf("save left extra project files: %v", entries)
	}
	if stagingEntries(t, fixture.staging) != 0 {
		t.Fatal("staged upload not removed after commit")
	}
	// The baseline moved: a second save without external change succeeds.
	if _, err := fixture.save(t, result, "second", false); err != nil {
		t.Fatalf("second save: %v", err)
	}
	// Unknown upload IDs are rejected.
	_, err = fixture.writeOp(result, "append", "nope", "x", false)
	expectCustomEditorCode(t, err, customEditorErrInvalidRequest)
	_, err = fixture.writeOp(result, "rename", "", "", false)
	expectCustomEditorCode(t, err, customEditorErrInvalidRequest)
}

func TestCustomEditorDocumentConflictAndForce(t *testing.T) {
	fixture := newCustomEditorFixture(t, customEditorAccessReadWrite, api.CapabilityWorkspaceWrite)
	target := fixture.write(t, "a.drawio", "base")
	result := fixture.mustBegin(t, "a.drawio")
	fixture.write(t, "a.drawio", "external")

	begun, err := fixture.writeOp(result, "begin", "", "mine", false)
	if err != nil {
		t.Fatal(err)
	}
	// force without a prior conflict report is not honored.
	_, err = fixture.writeOp(result, "commit", begun.UploadID, "", true)
	expectCustomEditorCode(t, err, customEditorErrConflict)
	if content, _ := os.ReadFile(target); string(content) != "external" {
		t.Fatalf("conflicting commit wrote %q", content)
	}
	// The file changes again after the conflict was reported: force is refused.
	fixture.write(t, "a.drawio", "external 2")
	_, err = fixture.writeOp(result, "commit", begun.UploadID, "", true)
	expectCustomEditorCode(t, err, customEditorErrConflict)
	// Confirmed overwrite of exactly the reported on-disk version succeeds.
	committed, err := fixture.writeOp(result, "commit", begun.UploadID, "", true)
	if err != nil || !committed.Committed {
		t.Fatalf("forced commit: %+v %v", committed, err)
	}
	if content, _ := os.ReadFile(target); string(content) != "mine" {
		t.Fatalf("content %q", content)
	}
}

func TestCustomEditorDocumentWriteLimits(t *testing.T) {
	fixture := newCustomEditorFixture(t, customEditorAccessReadWrite, api.CapabilityWorkspaceWrite)
	previousChunk, previousWrite := customEditorChunkBytes, customEditorWriteMaxBytes
	customEditorChunkBytes, customEditorWriteMaxBytes = 4, 6
	t.Cleanup(func() { customEditorChunkBytes, customEditorWriteMaxBytes = previousChunk, previousWrite })
	fixture.write(t, "a.drawio", "base")
	result := fixture.mustBegin(t, "a.drawio")

	_, err := fixture.writeOp(result, "begin", "", "12345", false)
	expectCustomEditorCode(t, err, customEditorErrTooLarge)
	begun, err := fixture.writeOp(result, "begin", "", "1234", false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.writeOp(result, "append", begun.UploadID, "567", false)
	expectCustomEditorCode(t, err, customEditorErrTooLarge)
	if stagingEntries(t, fixture.staging) != 0 {
		t.Fatal("oversized upload was not discarded")
	}
	_, err = fixture.writeOp(result, "commit", begun.UploadID, "", false)
	expectCustomEditorCode(t, err, customEditorErrInvalidRequest)
	_, err = fixture.manager.WriteCustomEditorDocument(customEditorWriteArgs{
		SessionID: result.Session.SessionID, Nonce: result.Session.Nonce, Op: "begin", Data: "!!!!",
	})
	expectCustomEditorCode(t, err, customEditorErrInvalidRequest)
}

func TestCustomEditorDocumentAbortCleansStaging(t *testing.T) {
	fixture := newCustomEditorFixture(t, customEditorAccessReadWrite, api.CapabilityWorkspaceWrite)
	target := fixture.write(t, "a.drawio", "base")
	result := fixture.mustBegin(t, "a.drawio")
	begun, err := fixture.writeOp(result, "begin", "", "abc", false)
	if err != nil {
		t.Fatal(err)
	}
	// A second begin replaces the first upload.
	second, err := fixture.writeOp(result, "begin", "", "def", false)
	if err != nil || second.UploadID == begun.UploadID || stagingEntries(t, fixture.staging) != 1 {
		t.Fatalf("replace begin: %+v %v", second, err)
	}
	if _, err := fixture.writeOp(result, "abort", second.UploadID, "", false); err != nil {
		t.Fatal(err)
	}
	if stagingEntries(t, fixture.staging) != 0 {
		t.Fatal("abort left staged data")
	}
	if content, _ := os.ReadFile(target); string(content) != "base" {
		t.Fatalf("abort changed the document: %q", content)
	}
	_, err = fixture.writeOp(result, "commit", second.UploadID, "", false)
	expectCustomEditorCode(t, err, customEditorErrInvalidRequest)
}

func TestCustomEditorDocumentLifecycleCleanup(t *testing.T) {
	fixture := newCustomEditorFixture(t, customEditorAccessReadWrite, api.CapabilityWorkspaceWrite)
	fixture.write(t, "a.drawio", "base")
	result := fixture.mustBegin(t, "a.drawio")
	if _, err := fixture.writeOp(result, "begin", "", "abc", false); err != nil {
		t.Fatal(err)
	}
	if stagingEntries(t, fixture.staging) != 1 {
		t.Fatal("expected one staged upload")
	}
	// Stop/crash/disable/reload/uninstall all run the record lifecycle hooks.
	fixture.manager.clearRecordContext(fixture.record)
	if customEditorDocuments.get(result.Session.SessionID) != nil {
		t.Fatal("lifecycle hook kept the binding")
	}
	if stagingEntries(t, fixture.staging) != 0 {
		t.Fatal("lifecycle hook kept staged data")
	}
	_, err := fixture.read(result, 0, 0)
	expectCustomEditorCode(t, err, customEditorErrStaleSession)
}

func TestCustomEditorStagingSweepsLeftovers(t *testing.T) {
	fixture := newCustomEditorFixture(t, customEditorAccessReadWrite, api.CapabilityWorkspaceWrite)
	if err := os.MkdirAll(fixture.staging, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.staging, "old.upload"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.write(t, "a.drawio", "base")
	result := fixture.mustBegin(t, "a.drawio")
	if _, err := fixture.writeOp(result, "begin", "", "abc", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(fixture.staging, "old.upload")); !os.IsNotExist(err) {
		t.Fatal("leftover staging file was not swept")
	}
	info, err := os.Stat(fixture.staging)
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o700) {
		t.Fatalf("staging dir mode: %v %v", info, err)
	}
}
