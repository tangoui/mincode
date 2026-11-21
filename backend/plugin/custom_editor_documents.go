package plugin

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mincode/app/backend/core"
	backendfs "mincode/app/backend/fs"
	"mincode/app/plugins/api"
)

// Document bridge for contributes.customEditors.
//
// plugin_host_ui_begin_document binds exactly one project file to a fresh
// plugin UI session. The iframe never names paths: the trusted host frame
// reads and writes through the opaque session binding with the session nonce.
// Every call re-admits the session (nonce, generation, running/enabled/
// effective plugin, editor still declared for the same view), re-resolves the
// bound path symlink-safely under the canonical project root, and requires a
// regular file. Writes are staged in an app-private directory, committed with
// an atomic same-directory rename after a sha256 conflict check against the
// baseline, and never create, rename or delete any other project file.
//
// Errors are "<code>: <detail>" with code one of conflict, too_large,
// not_found, forbidden, stale_session, invalid_request.

const (
	customEditorErrConflict       = "conflict"
	customEditorErrTooLarge       = "too_large"
	customEditorErrNotFound       = "not_found"
	customEditorErrForbidden      = "forbidden"
	customEditorErrStaleSession   = "stale_session"
	customEditorErrInvalidRequest = "invalid_request"

	customEditorStagingDirectory = "custom-editor-staging"
)

// Contract limits; variables so focused tests can shrink them.
var (
	customEditorReadMaxBytes     = customEditorSpec.Limit("readMaxBytes")
	customEditorWriteMaxBytes    = customEditorSpec.Limit("writeMaxBytes")
	customEditorChunkBytes       = customEditorSpec.Limit("chunkBytes")
	customEditorOpenDocumentsCap = int(customEditorSpec.Limit("openDocumentsPerPlugin"))
)

type customEditorDocumentError struct {
	code   string
	detail string
}

func (err *customEditorDocumentError) Error() string { return err.code + ": " + err.detail }

func customEditorError(code, format string, args ...any) error {
	return &customEditorDocumentError{code: code, detail: fmt.Sprintf(format, args...)}
}

// customEditorErrorCode extracts the code of a document bridge error.
func customEditorErrorCode(err error) string {
	var documentErr *customEditorDocumentError
	if errors.As(err, &documentErr) {
		return documentErr.code
	}
	return ""
}

type customEditorBaseline struct {
	size    int64
	modTime time.Time
	sha256  string
}

type customEditorUpload struct {
	id   string
	file *os.File
	size int64
	// conflictSHA is the on-disk sha256 reported by the last conflicting
	// commit; force only overwrites while the file still has exactly it.
	conflictSHA string
}

func (upload *customEditorUpload) discard() {
	if upload == nil || upload.file == nil {
		return
	}
	name := upload.file.Name()
	_ = upload.file.Close()
	_ = os.Remove(name)
	upload.file = nil
}

type customEditorBinding struct {
	mu           sync.Mutex
	sessionID    string
	pluginID     string
	editorID     string
	viewID       string
	root         string
	relativePath string
	resolvedPath string
	access       string
	generation   uint64
	baseline     customEditorBaseline
	upload       *customEditorUpload
	closed       bool
}

// close discards staged data; the caller holds binding.mu.
func (binding *customEditorBinding) closeLocked() {
	binding.closed = true
	binding.upload.discard()
	binding.upload = nil
}

type customEditorDocumentStore struct {
	mu       sync.Mutex
	bindings map[string]*customEditorBinding
	// staged records staging directories already swept of leftovers.
	staged map[string]bool
}

var customEditorDocuments = &customEditorDocumentStore{
	bindings: map[string]*customEditorBinding{},
	staged:   map[string]bool{},
}

func (store *customEditorDocumentStore) get(sessionID string) *customEditorBinding {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.bindings[sessionID]
}

// remove detaches bindings and discards their uploads outside store.mu.
func (store *customEditorDocumentStore) remove(match func(*customEditorBinding) bool) {
	store.mu.Lock()
	var removed []*customEditorBinding
	for id, binding := range store.bindings {
		if match(binding) {
			removed = append(removed, binding)
			delete(store.bindings, id)
		}
	}
	store.mu.Unlock()
	for _, binding := range removed {
		binding.mu.Lock()
		binding.closeLocked()
		binding.mu.Unlock()
	}
}

func (store *customEditorDocumentStore) removeSession(sessionID string) {
	store.remove(func(binding *customEditorBinding) bool { return binding.sessionID == sessionID })
}

func (store *customEditorDocumentStore) purgePlugin(pluginID string) {
	store.remove(func(binding *customEditorBinding) bool { return binding.pluginID == pluginID })
}

// purgeStale drops bindings whose UI session was closed or revoked, or whose
// session now belongs to another generation.
func (store *customEditorDocumentStore) purgeStale(uiHost *pluginUIHost) {
	live := map[string]uint64{}
	if uiHost != nil {
		uiHost.mu.RLock()
		for id, session := range uiHost.sessions {
			live[id] = session.Generation
		}
		uiHost.mu.RUnlock()
	}
	store.remove(func(binding *customEditorBinding) bool {
		generation, ok := live[binding.sessionID]
		return !ok || generation != binding.generation
	})
}

func (store *customEditorDocumentStore) countPlugin(pluginID string) int {
	count := 0
	for _, binding := range store.bindings {
		if binding.pluginID == pluginID {
			count++
		}
	}
	return count
}

func init() {
	core.Register("plugin_host_ui_begin_document", pluginHostUIBeginDocumentHandler)
	core.Register("plugin_host_ui_document_read", pluginHostUIDocumentReadHandler)
	core.Register("plugin_host_ui_document_write", pluginHostUIDocumentWriteHandler)
	registerRecordLifecycleHook(func(_ *pluginManager, record *pluginRecord) {
		customEditorDocuments.purgePlugin(record.id)
	})
}

type customEditorBeginDocumentArgs struct {
	PluginID     string `json:"pluginId"`
	EditorID     string `json:"editorId"`
	ProjectRoot  string `json:"projectRoot"`
	RelativePath string `json:"relativePath"`
	ParentOrigin string `json:"parentOrigin"`
}

type customEditorDocumentDTO struct {
	Name          string `json:"name"`
	RelativePath  string `json:"relativePath"`
	Path          string `json:"path,omitempty"`
	Size          int64  `json:"size"`
	ModifiedAt    int64  `json:"modifiedAt"`
	Access        string `json:"access"`
	MaxReadBytes  int64  `json:"maxReadBytes"`
	ChunkBytes    int64  `json:"chunkBytes"`
	MaxWriteBytes int64  `json:"maxWriteBytes"`
}

type customEditorBeginDocumentResult struct {
	Session  pluginUISessionDTO      `json:"session"`
	Document customEditorDocumentDTO `json:"document"`
}

type customEditorReadArgs struct {
	SessionID string `json:"sessionId"`
	Nonce     string `json:"nonce"`
	Offset    int64  `json:"offset"`
	Length    int64  `json:"length"`
}

type customEditorReadResult struct {
	Data       string `json:"data"`
	EOF        bool   `json:"eof"`
	Size       int64  `json:"size"`
	ModifiedAt int64  `json:"modifiedAt"`
}

type customEditorWriteArgs struct {
	SessionID string `json:"sessionId"`
	Nonce     string `json:"nonce"`
	Op        string `json:"op"`
	UploadID  string `json:"uploadId"`
	Data      string `json:"data"`
	// Force is set only by the trusted host frame after the user confirmed
	// overwriting a conflicting file; the iframe protocol has no such field.
	Force bool `json:"force"`
}

type customEditorWriteResult struct {
	UploadID   string `json:"uploadId,omitempty"`
	Received   *int64 `json:"received,omitempty"`
	Committed  bool   `json:"committed,omitempty"`
	Size       *int64 `json:"size,omitempty"`
	ModifiedAt *int64 `json:"modifiedAt,omitempty"`
}

func pluginHostUIBeginDocumentHandler(_, argsJSON string) (string, error) {
	var args customEditorBeginDocumentArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", customEditorError(customEditorErrInvalidRequest, "%v", err)
	}
	result, err := host().BeginCustomEditorDocument(args)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(result)
}

func pluginHostUIDocumentReadHandler(_, argsJSON string) (string, error) {
	var args customEditorReadArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", customEditorError(customEditorErrInvalidRequest, "%v", err)
	}
	result, err := host().ReadCustomEditorDocument(args)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(result)
}

func pluginHostUIDocumentWriteHandler(_, argsJSON string) (string, error) {
	var args customEditorWriteArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", customEditorError(customEditorErrInvalidRequest, "%v", err)
	}
	result, err := host().WriteCustomEditorDocument(args)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(result)
}

// customEditorPathError maps backend/fs resolution failures to bridge codes.
func customEditorPathError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, backendfs.ErrProjectPathInvalid):
		return customEditorError(customEditorErrInvalidRequest, "relative path is invalid")
	case errors.Is(err, backendfs.ErrProjectPathEscapesRoot):
		return customEditorError(customEditorErrForbidden, "path resolves outside the project")
	default:
		return customEditorError(customEditorErrNotFound, "document is unavailable")
	}
}

// BeginCustomEditorDocument validates the request statically (no plugin code
// runs for a rejected request), then opens the editor view's UI session and
// binds the resolved file to it.
func (manager *pluginManager) BeginCustomEditorDocument(args customEditorBeginDocumentArgs) (customEditorBeginDocumentResult, error) {
	pluginID := strings.TrimSpace(args.PluginID)
	editorID := strings.TrimSpace(args.EditorID)
	if pluginID == "" || editorID == "" {
		return customEditorBeginDocumentResult{}, customEditorError(customEditorErrInvalidRequest, "pluginId and editorId are required")
	}
	if manager.uiHost == nil {
		return customEditorBeginDocumentResult{}, customEditorError(customEditorErrStaleSession, "plugin UI host is unavailable")
	}
	record, err := manager.record(pluginID)
	if err != nil {
		return customEditorBeginDocumentResult{}, customEditorError(customEditorErrNotFound, "plugin %q not found", pluginID)
	}
	record.mu.Lock()
	available := manager.contributionsAvailableLocked(record) && record.manifest != nil
	var editor CustomEditorContribution
	var editorFound, canReadWorkspace, canWriteWorkspace bool
	if available {
		editor, editorFound = record.manifest.customEditor(editorID)
		canReadWorkspace = manifestHasCapability(record.manifest, api.CapabilityWorkspaceRead)
		canWriteWorkspace = manifestHasCapability(record.manifest, api.CapabilityWorkspaceWrite)
	}
	record.mu.Unlock()
	if !available {
		return customEditorBeginDocumentResult{}, customEditorError(customEditorErrForbidden, "plugin %q is not available in this workspace", pluginID)
	}
	if !editorFound {
		return customEditorBeginDocumentResult{}, customEditorError(customEditorErrNotFound, "custom editor %q not found", editorID)
	}
	access := editor.Access
	if access == customEditorAccessReadWrite && !canWriteWorkspace {
		access = customEditorAccessRead
	}

	relative, err := backendfs.CleanProjectRelativePath(args.RelativePath)
	if err != nil {
		return customEditorBeginDocumentResult{}, customEditorPathError(err)
	}
	if !customEditorMatchesAny(editor.FilenamePatterns, relative) {
		return customEditorBeginDocumentResult{}, customEditorError(customEditorErrForbidden, "custom editor %q does not handle %q", editorID, path.Base(relative))
	}
	file, err := backendfs.ResolveProjectFile(args.ProjectRoot, relative)
	if err != nil {
		return customEditorBeginDocumentResult{}, customEditorPathError(err)
	}
	// A symlink inside the project must not alias a file the editor does not handle.
	if !customEditorMatchesAny(editor.FilenamePatterns, file.ResolvedRelativePath) {
		return customEditorBeginDocumentResult{}, customEditorError(customEditorErrForbidden, "custom editor %q does not handle the link target", editorID)
	}
	binding := &customEditorBinding{
		pluginID: pluginID, editorID: editorID, viewID: editor.ViewID,
		root: file.Root, relativePath: file.RelativePath, resolvedPath: file.Path, access: access,
	}
	handle, info, err := binding.openLocked()
	if err != nil {
		return customEditorBeginDocumentResult{}, err
	}
	baseline := customEditorBaseline{size: info.Size(), modTime: info.ModTime()}
	if access == customEditorAccessReadWrite {
		hasher := sha256.New()
		if _, err := io.Copy(hasher, io.LimitReader(handle, customEditorReadMaxBytes+1)); err != nil {
			_ = handle.Close()
			return customEditorBeginDocumentResult{}, customEditorError(customEditorErrNotFound, "document could not be read")
		}
		baseline.sha256 = hex.EncodeToString(hasher.Sum(nil))
	}
	_ = handle.Close()
	binding.baseline = baseline

	customEditorDocuments.purgeStale(manager.uiHost)
	customEditorDocuments.mu.Lock()
	full := customEditorDocuments.countPlugin(pluginID) >= customEditorOpenDocumentsCap
	customEditorDocuments.mu.Unlock()
	if full {
		return customEditorBeginDocumentResult{}, customEditorError(customEditorErrTooLarge, "too many open documents for this plugin")
	}

	session, err := manager.BeginUI(pluginID, editor.ViewID, args.ParentOrigin)
	if err != nil {
		return customEditorBeginDocumentResult{}, customEditorError(customEditorErrForbidden, "%v", err)
	}
	// A document frame is bound to one file; desktop drops never reach it.
	session.FileDrop = nil
	binding.sessionID = session.SessionID
	binding.generation = session.Generation
	customEditorDocuments.mu.Lock()
	if customEditorDocuments.countPlugin(pluginID) >= customEditorOpenDocumentsCap {
		customEditorDocuments.mu.Unlock()
		manager.CloseUI(session.SessionID)
		return customEditorBeginDocumentResult{}, customEditorError(customEditorErrTooLarge, "too many open documents for this plugin")
	}
	customEditorDocuments.bindings[session.SessionID] = binding
	customEditorDocuments.mu.Unlock()

	document := customEditorDocumentDTO{
		Name:         path.Base(file.RelativePath),
		RelativePath: file.RelativePath,
		Size:         baseline.size,
		ModifiedAt:   baseline.modTime.UnixMilli(),
		Access:       access,
		MaxReadBytes: customEditorReadMaxBytes,
		ChunkBytes:   customEditorChunkBytes,
	}
	if canReadWorkspace {
		document.Path = file.Path
	}
	if access == customEditorAccessReadWrite {
		document.MaxWriteBytes = customEditorWriteMaxBytes
	}
	return customEditorBeginDocumentResult{Session: session, Document: document}, nil
}

func customEditorMatchesAny(patterns []string, relativePath string) bool {
	for _, pattern := range patterns {
		if customEditorPatternMatches(pattern, relativePath) {
			return true
		}
	}
	return false
}

// admitCustomEditorDocument re-validates the session and plugin for every
// read/write. It returns the binding and whether workspace.write is still
// declared by the current manifest.
func (manager *pluginManager) admitCustomEditorDocument(sessionID, nonce string) (*customEditorBinding, bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	stale := func(detail string) error { return customEditorError(customEditorErrStaleSession, "%s", detail) }
	if manager.uiHost == nil || sessionID == "" {
		return nil, false, stale("plugin UI session is unavailable")
	}
	session, err := manager.uiHost.Session(sessionID, nonce)
	if err != nil {
		manager.uiHost.mu.RLock()
		_, exists := manager.uiHost.sessions[sessionID]
		manager.uiHost.mu.RUnlock()
		if !exists {
			customEditorDocuments.removeSession(sessionID)
		}
		return nil, false, stale("plugin UI session is unavailable")
	}
	binding := customEditorDocuments.get(sessionID)
	if binding == nil {
		return nil, false, stale("no document is bound to this session")
	}
	if binding.pluginID != session.PluginID || binding.generation != session.Generation || binding.viewID != session.ViewID {
		customEditorDocuments.removeSession(sessionID)
		return nil, false, stale("document session no longer matches the plugin")
	}
	record, err := manager.record(binding.pluginID)
	if err != nil {
		customEditorDocuments.removeSession(sessionID)
		return nil, false, stale("plugin is no longer installed")
	}
	record.mu.Lock()
	admitted := manager.workspaceActiveLocked(record) && record.state == pluginStateRunning &&
		record.manifest != nil && record.runtime != nil && record.runtime.Generation() == binding.generation
	canWrite := false
	if admitted {
		editor, ok := record.manifest.customEditor(binding.editorID)
		admitted = ok && editor.ViewID == binding.viewID
		canWrite = ok && editor.Access == customEditorAccessReadWrite &&
			manifestHasCapability(record.manifest, api.CapabilityWorkspaceWrite)
	}
	record.mu.Unlock()
	if !admitted {
		customEditorDocuments.removeSession(sessionID)
		return nil, false, stale("plugin or custom editor is no longer available")
	}
	return binding, canWrite, nil
}

// openLocked re-resolves the bound path and opens it. The opened handle must
// be the same regular, non-symlink file the canonical path names now, so a
// swap between resolution and open is detected.
func (binding *customEditorBinding) openLocked() (*os.File, os.FileInfo, error) {
	resolved, err := backendfs.ResolveUnderProjectRoot(binding.root, binding.relativePath)
	if err != nil {
		return nil, nil, customEditorPathError(err)
	}
	if resolved != binding.resolvedPath {
		return nil, nil, customEditorError(customEditorErrForbidden, "document path now resolves to a different file")
	}
	linkInfo, err := os.Lstat(resolved)
	if err != nil {
		return nil, nil, customEditorError(customEditorErrNotFound, "document is unavailable")
	}
	if !linkInfo.Mode().IsRegular() {
		return nil, nil, customEditorError(customEditorErrForbidden, "document is not a regular file")
	}
	handle, err := os.Open(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, customEditorError(customEditorErrNotFound, "document is unavailable")
		}
		return nil, nil, customEditorError(customEditorErrForbidden, "document cannot be opened")
	}
	info, err := handle.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(info, linkInfo) {
		_ = handle.Close()
		return nil, nil, customEditorError(customEditorErrForbidden, "document changed type while opening")
	}
	if info.Size() > customEditorReadMaxBytes {
		_ = handle.Close()
		return nil, nil, customEditorError(customEditorErrTooLarge, "document exceeds %d bytes", customEditorReadMaxBytes)
	}
	return handle, info, nil
}

// ReadCustomEditorDocument returns one bounded chunk of the bound file.
func (manager *pluginManager) ReadCustomEditorDocument(args customEditorReadArgs) (customEditorReadResult, error) {
	if args.Offset < 0 {
		return customEditorReadResult{}, customEditorError(customEditorErrInvalidRequest, "offset must not be negative")
	}
	binding, _, err := manager.admitCustomEditorDocument(args.SessionID, args.Nonce)
	if err != nil {
		return customEditorReadResult{}, err
	}
	binding.mu.Lock()
	defer binding.mu.Unlock()
	if binding.closed {
		return customEditorReadResult{}, customEditorError(customEditorErrStaleSession, "document session was closed")
	}
	handle, info, err := binding.openLocked()
	if err != nil {
		return customEditorReadResult{}, err
	}
	defer handle.Close()
	length := args.Length
	if length <= 0 || length > customEditorChunkBytes {
		length = customEditorChunkBytes
	}
	size := info.Size()
	result := customEditorReadResult{Size: size, ModifiedAt: info.ModTime().UnixMilli()}
	if args.Offset >= size {
		result.EOF = true
		return result, nil
	}
	if remaining := size - args.Offset; length > remaining {
		length = remaining
	}
	buffer := make([]byte, length)
	read, err := handle.ReadAt(buffer, args.Offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return customEditorReadResult{}, customEditorError(customEditorErrNotFound, "document could not be read")
	}
	result.Data = base64.StdEncoding.EncodeToString(buffer[:read])
	result.EOF = args.Offset+int64(read) >= size
	return result, nil
}

// WriteCustomEditorDocument runs one staged-save step.
func (manager *pluginManager) WriteCustomEditorDocument(args customEditorWriteArgs) (customEditorWriteResult, error) {
	op := strings.TrimSpace(args.Op)
	switch op {
	case "begin", "append", "commit", "abort":
	default:
		return customEditorWriteResult{}, customEditorError(customEditorErrInvalidRequest, "op must be begin, append, commit or abort")
	}
	binding, canWrite, err := manager.admitCustomEditorDocument(args.SessionID, args.Nonce)
	if err != nil {
		return customEditorWriteResult{}, err
	}
	binding.mu.Lock()
	defer binding.mu.Unlock()
	if binding.closed {
		return customEditorWriteResult{}, customEditorError(customEditorErrStaleSession, "document session was closed")
	}
	uploadID := strings.TrimSpace(args.UploadID)
	if op == "abort" {
		if binding.upload != nil && (uploadID == "" || binding.upload.id == uploadID) {
			binding.upload.discard()
			binding.upload = nil
		}
		return customEditorWriteResult{UploadID: uploadID}, nil
	}
	if binding.access != customEditorAccessReadWrite || !canWrite {
		return customEditorWriteResult{}, customEditorError(customEditorErrForbidden, "this custom editor may not write documents")
	}
	chunk, err := customEditorDecodeChunk(args.Data)
	if err != nil {
		return customEditorWriteResult{}, err
	}
	if op == "begin" {
		binding.upload.discard()
		binding.upload = nil
		upload, err := newCustomEditorUpload()
		if err != nil {
			return customEditorWriteResult{}, err
		}
		binding.upload = upload
	} else if binding.upload == nil || uploadID == "" || binding.upload.id != uploadID {
		return customEditorWriteResult{}, customEditorError(customEditorErrInvalidRequest, "unknown upload")
	}
	upload := binding.upload
	if len(chunk) > 0 {
		if upload.size+int64(len(chunk)) > customEditorWriteMaxBytes {
			upload.discard()
			binding.upload = nil
			return customEditorWriteResult{}, customEditorError(customEditorErrTooLarge, "document exceeds %d bytes", customEditorWriteMaxBytes)
		}
		if _, err := upload.file.Write(chunk); err != nil {
			upload.discard()
			binding.upload = nil
			return customEditorWriteResult{}, customEditorError(customEditorErrForbidden, "could not stage document data")
		}
		upload.size += int64(len(chunk))
		// Any new data invalidates a previous overwrite confirmation.
		upload.conflictSHA = ""
	}
	if op != "commit" {
		received := upload.size
		return customEditorWriteResult{UploadID: upload.id, Received: &received}, nil
	}
	info, err := binding.commitLocked(args.Force)
	if err != nil {
		return customEditorWriteResult{}, err
	}
	size, modifiedAt := info.Size(), info.ModTime().UnixMilli()
	return customEditorWriteResult{UploadID: uploadID, Committed: true, Size: &size, ModifiedAt: &modifiedAt}, nil
}

func customEditorDecodeChunk(data string) ([]byte, error) {
	if data == "" {
		return nil, nil
	}
	if int64(len(data)) > int64(base64.StdEncoding.EncodedLen(int(customEditorChunkBytes))) {
		return nil, customEditorError(customEditorErrTooLarge, "chunk exceeds %d bytes", customEditorChunkBytes)
	}
	chunk, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return nil, customEditorError(customEditorErrInvalidRequest, "data must be base64")
	}
	if int64(len(chunk)) > customEditorChunkBytes {
		return nil, customEditorError(customEditorErrTooLarge, "chunk exceeds %d bytes", customEditorChunkBytes)
	}
	return chunk, nil
}

func customEditorStagingDir() (string, error) {
	directory := filepath.Join(userDataDir(), pluginDataDirectory, customEditorStagingDirectory)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", customEditorError(customEditorErrForbidden, "staging directory is unavailable")
	}
	customEditorDocuments.mu.Lock()
	sweep := !customEditorDocuments.staged[directory]
	customEditorDocuments.staged[directory] = true
	customEditorDocuments.mu.Unlock()
	if sweep {
		// Leftovers of a previous run can never be committed; remove them.
		if entries, err := os.ReadDir(directory); err == nil {
			for _, entry := range entries {
				_ = os.RemoveAll(filepath.Join(directory, entry.Name()))
			}
		}
	}
	return directory, nil
}

func newCustomEditorUpload() (*customEditorUpload, error) {
	directory, err := customEditorStagingDir()
	if err != nil {
		return nil, err
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return nil, customEditorError(customEditorErrForbidden, "could not create upload")
	}
	id := hex.EncodeToString(random)
	file, err := os.OpenFile(filepath.Join(directory, id+".upload"), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, customEditorError(customEditorErrForbidden, "could not create upload")
	}
	return &customEditorUpload{id: id, file: file}, nil
}

// commitLocked replaces the bound file with the staged upload.
func (binding *customEditorBinding) commitLocked(force bool) (os.FileInfo, error) {
	upload := binding.upload
	handle, info, err := binding.openLocked()
	if err != nil {
		return nil, err
	}
	hasher := sha256.New()
	_, err = io.Copy(hasher, handle)
	_ = handle.Close()
	if err != nil {
		return nil, customEditorError(customEditorErrNotFound, "document could not be read")
	}
	current := hex.EncodeToString(hasher.Sum(nil))
	if current != binding.baseline.sha256 {
		if !force || upload.conflictSHA == "" || upload.conflictSHA != current {
			upload.conflictSHA = current
			return nil, customEditorError(customEditorErrConflict, "document changed on disk since it was opened")
		}
	}

	directory := filepath.Dir(binding.resolvedPath)
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(binding.resolvedPath)+".mincode-*.tmp")
	if err != nil {
		return nil, customEditorError(customEditorErrForbidden, "document directory is not writable")
	}
	temporaryName := temporary.Name()
	fail := func(detail string) (os.FileInfo, error) {
		_ = temporary.Close()
		_ = os.Remove(temporaryName)
		return nil, customEditorError(customEditorErrForbidden, "%s", detail)
	}
	if err := temporary.Chmod(info.Mode().Perm()); err != nil {
		return fail("could not preserve document permissions")
	}
	if _, err := upload.file.Seek(0, io.SeekStart); err != nil {
		return fail("could not read staged document")
	}
	written := sha256.New()
	copied, err := io.Copy(io.MultiWriter(temporary, written), io.LimitReader(upload.file, customEditorWriteMaxBytes+1))
	if err != nil || copied != upload.size {
		return fail("could not write document")
	}
	if err := temporary.Sync(); err != nil {
		return fail("could not flush document")
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryName)
		return nil, customEditorError(customEditorErrForbidden, "could not write document")
	}
	if err := os.Rename(temporaryName, binding.resolvedPath); err != nil {
		_ = os.Remove(temporaryName)
		return nil, customEditorError(customEditorErrForbidden, "could not replace document")
	}
	if dir, err := os.Open(directory); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	upload.discard()
	binding.upload = nil
	saved, err := os.Stat(binding.resolvedPath)
	if err != nil {
		return nil, customEditorError(customEditorErrNotFound, "document is unavailable after saving")
	}
	binding.baseline = customEditorBaseline{size: saved.Size(), modTime: saved.ModTime(), sha256: hex.EncodeToString(written.Sum(nil))}
	return saved, nil
}
