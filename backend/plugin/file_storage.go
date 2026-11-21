package plugin

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// File data is independent of both the installed package and the SQLite KV store.
// Like KV, it survives uninstall and is removed by explicit ClearPluginData.
const (
	pluginStorageDirectory          = "plugin-storage"
	pluginStorageMaxFileBytes int64 = 64 * 1024 * 1024
	pluginStorageMaxBytes     int64 = 1024 * 1024 * 1024
	pluginStorageMaxFiles           = 8192
	// Per-call read, write and staged-append body. 512 KiB is about 683 KiB of
	// base64, well inside the 1 MiB JSON-RPC line cap; older SDKs send 64 KiB
	// chunks, which remain valid.
	pluginStorageMaxChunkBytes = 512 * 1024
	pluginStorageMaxPathBytes  = 1024
	// Staged writes (host.storage.file.write.begin/append/commit/abort).
	pluginStorageMaxSessions = 4
	pluginStorageStagePrefix = pluginStorageTempPrefix + "stage-"
)

// pluginStorageSessionIdle aborts a staged write that receives no call for this
// long. A variable so tests can shorten it.
var pluginStorageSessionIdle = 60 * time.Second

// pluginStorageGate orders storage operations. Per-plugin operations hold it
// shared and then serialize on their plugin's state mutex, so plugins never
// wait on each other. Namespace-wide operations elsewhere (purge, import,
// export) take it exclusively through Lock/Unlock; releasing it drops every
// cached usage figure because those operations replace files behind the cache.
type pluginStorageGate struct{ rw sync.RWMutex }

func (gate *pluginStorageGate) Lock() { gate.rw.Lock() }
func (gate *pluginStorageGate) Unlock() {
	pluginStorageForgetUsage()
	gate.rw.Unlock()
}
func (gate *pluginStorageGate) RLock()   { gate.rw.RLock() }
func (gate *pluginStorageGate) RUnlock() { gate.rw.RUnlock() }

var pluginStorageMu pluginStorageGate

// pluginStorageState is the per-plugin cache and staged-write registry. It is
// keyed by the storage root path, so a changed data directory never reuses it.
type pluginStorageState struct {
	pluginID string
	mu       sync.Mutex
	// Cached committed usage, excluding active staging files. Recomputed by one
	// WalkDir when unknown and at every commit; maintained incrementally by
	// direct writes and deletes.
	usageKnown bool
	files      int
	bytes      int64
	sessions   map[string]*pluginStorageSession
}

type pluginStorageSession struct {
	id       string
	owner    any // the processRuntime that began it; nil for direct callers
	name     string
	stage    string // staging file path relative to the storage root
	file     *os.File
	size     int64
	replaced int64 // target size at begin, so a rewrite is not counted twice
	newFile  bool
	timer    *time.Timer
	active   time.Time // last call; a timer that fired while an append held the lock re-checks it
	idle     time.Duration
}

var (
	pluginStorageStatesMu sync.Mutex
	pluginStorageStates   = map[string]*pluginStorageState{}
)

func pluginStorageForgetUsage() {
	pluginStorageStatesMu.Lock()
	defer pluginStorageStatesMu.Unlock()
	for _, state := range pluginStorageStates {
		state.usageKnown = false
	}
}

func pluginStorageStateFor(pluginID string, create bool) (*pluginStorageState, error) {
	root, err := pluginStorageRoot(pluginID)
	if err != nil {
		return nil, err
	}
	pluginStorageStatesMu.Lock()
	defer pluginStorageStatesMu.Unlock()
	state := pluginStorageStates[root]
	if state == nil && create {
		state = &pluginStorageState{pluginID: pluginID, sessions: map[string]*pluginStorageSession{}}
		pluginStorageStates[root] = state
	}
	return state, nil
}

// lockPluginStorage serializes one plugin's namespace mutations.
func lockPluginStorage(pluginID string) (*pluginStorageState, func(), error) {
	pluginStorageMu.RLock()
	state, err := pluginStorageStateFor(pluginID, true)
	if err != nil {
		pluginStorageMu.RUnlock()
		return nil, nil, err
	}
	state.mu.Lock()
	return state, func() {
		state.mu.Unlock()
		pluginStorageMu.RUnlock()
	}, nil
}

func pluginStorageRoot(pluginID string) (string, error) {
	id, err := validatePluginID(pluginID)
	if err != nil {
		return "", err
	}
	if filepath.Base(id) != id || strings.ContainsAny(id, `/\`) || id == "." || id == ".." {
		return "", errors.New("plugin storage: invalid plugin id")
	}
	return filepath.Join(userDataDir(), pluginDataDirectory, pluginStorageDirectory, id), nil
}

func validPluginStoragePath(name string, allowEmpty bool) error {
	if allowEmpty && name == "" {
		return nil
	}
	if name == "" || len(name) > pluginStorageMaxPathBytes || strings.ContainsAny(name, "\\\x00") || strings.HasPrefix(name, "/") || strings.Contains(name, ":") {
		return errors.New("invalid plugin storage path")
	}
	for _, segment := range strings.Split(name, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return errors.New("plugin storage path escapes sandbox")
		}
		// Host scratch names (direct-write temps, staged-write files) are not
		// counted in the cached usage; letting a plugin write or delete them
		// would skew the quota and hide files from list/export.
		if strings.HasPrefix(segment, pluginStorageTempPrefix) {
			return errors.New("plugin storage path uses a reserved name")
		}
	}
	return nil
}

// isPluginStorageScratchName reports host-generated temporary names (direct
// write temp files and staged-write files), hidden from list results.
func isPluginStorageScratchName(base string) bool {
	rest, ok := strings.CutPrefix(base, pluginStorageTempPrefix)
	if !ok {
		return false
	}
	rest = strings.TrimPrefix(rest, "stage-")
	if len(rest) != 32 {
		return false
	}
	_, err := hex.DecodeString(rest)
	return err == nil
}

// Open each directory through its parent Root. No path-based I/O after opening
// the sandbox: a renamed parent cannot redirect reads or atomic replacement.
func openPluginStorage(pluginID string, create bool) (*os.Root, error) {
	name, err := pluginStorageRoot(pluginID)
	if err != nil {
		return nil, err
	}
	base := userDataDir()
	if create {
		if err := os.MkdirAll(base, 0o700); err != nil {
			return nil, err
		}
	}
	current, err := os.OpenRoot(base)
	if err != nil {
		return nil, err
	}
	relative, err := filepath.Rel(base, name)
	if err != nil {
		current.Close()
		return nil, err
	}
	for _, segment := range strings.Split(relative, string(filepath.Separator)) {
		if segment == "." || segment == ".." || segment == "" {
			current.Close()
			return nil, errors.New("invalid plugin storage root")
		}
		info, statErr := current.Lstat(segment)
		if errors.Is(statErr, fs.ErrNotExist) && create {
			if err := current.Mkdir(segment, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
				current.Close()
				return nil, err
			}
			info, statErr = current.Lstat(segment)
		}
		if statErr != nil {
			current.Close()
			return nil, statErr
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			current.Close()
			return nil, errors.New("plugin storage root contains a symlink or non-directory")
		}
		next, err := current.OpenRoot(segment)
		current.Close()
		if err != nil {
			return nil, err
		}
		current = next
	}
	return current, nil
}

func checkStoragePath(root *os.Root, name string, missing bool, directory bool) error {
	if err := validPluginStoragePath(name, directory); err != nil {
		return err
	}
	if name == "" {
		return nil
	}
	parts := strings.Split(name, "/")
	for i := range parts {
		component := strings.Join(parts[:i+1], "/")
		info, err := root.Lstat(component)
		if errors.Is(err, fs.ErrNotExist) && missing {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("plugin storage path contains a symlink")
		}
		if (i < len(parts)-1 || directory) && !info.IsDir() {
			return errors.New("plugin storage parent is not a directory")
		}
	}
	return nil
}

// openStorageParent validates name, creates its parent directories and returns
// a Root for the parent directory plus the leaf name. Holding that descriptor
// across temporary creation and rename means an external symlink swap cannot
// change the rename destination.
func openStorageParent(root *os.Root, name string, create bool) (*os.Root, string, error) {
	parent := path.Dir(name)
	if parent != "." {
		if err := checkStoragePath(root, parent, create, true); err != nil {
			return nil, "", err
		}
		if create {
			if err := root.MkdirAll(parent, 0o700); err != nil {
				return nil, "", err
			}
		}
	}
	if err := checkStoragePath(root, name, true, false); err != nil {
		return nil, "", err
	}
	if parent == "." {
		dir, err := root.OpenRoot(".")
		return dir, name, err
	}
	dir, err := root.OpenRoot(parent)
	return dir, path.Base(name), err
}

func pluginStorageRead(pluginID, name string, offset, maxBytes int64) ([]byte, bool, error) {
	if offset < 0 || maxBytes < 1 || maxBytes > pluginStorageMaxChunkBytes {
		return nil, false, errors.New("invalid plugin storage read range")
	}
	root, err := openPluginStorage(pluginID, false)
	if err != nil {
		return nil, false, err
	}
	defer root.Close()
	if err := checkStoragePath(root, name, false, false); err != nil {
		return nil, false, err
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, errors.New("plugin storage path is not a regular file")
	}
	if offset > info.Size() {
		return nil, false, errors.New("read offset is beyond end of file")
	}
	data := make([]byte, min(maxBytes, info.Size()-offset))
	n, err := file.ReadAt(data, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, false, err
	}
	return data[:n], offset+int64(n) >= info.Size(), nil
}

// walkUsage counts committed regular files, skipping active staging files
// (accounted separately as staged bytes). Symlinks and special files fail.
func (state *pluginStorageState) walkUsage(root *os.Root) (int, int64, error) {
	skip := make(map[string]struct{}, len(state.sessions))
	for _, session := range state.sessions {
		skip[session.stage] = struct{}{}
	}
	count, total := 0, int64(0)
	err := fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("plugin storage contains a symlink")
		}
		if entry.IsDir() {
			return nil
		}
		if _, staged := skip[name]; staged {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("plugin storage contains a non-regular file")
		}
		count++
		total += info.Size()
		return nil
	})
	return count, total, err
}

func (state *pluginStorageState) ensureUsage(root *os.Root) error {
	if state.usageKnown {
		return nil
	}
	files, bytes, err := state.walkUsage(root)
	if err != nil {
		return err
	}
	state.files, state.bytes, state.usageKnown = files, bytes, true
	return nil
}

// stagedBytes is the growth the plugin's open sessions would add on commit.
func (state *pluginStorageState) stagedBytes(except *pluginStorageSession) int64 {
	total := int64(0)
	for _, session := range state.sessions {
		if session != except && session.size > session.replaced {
			total += session.size - session.replaced
		}
	}
	return total
}

func (state *pluginStorageState) stagedNewFiles() int {
	count := 0
	for _, session := range state.sessions {
		if session.newFile {
			count++
		}
	}
	return count
}

func newStorageToken() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(random[:]), nil
}

// truncate replaces the previous logical content at offset (discarding the old
// tail). Subsequent chunks use truncate=false to append or patch without loss.
// Each call atomically replaces the target: it streams the kept prefix, the new
// chunk and any kept tail into a temporary file, fsyncs it and renames it over
// the target. Quota checks use the per-plugin usage cache, not a directory walk.
func pluginStorageWrite(pluginID, name string, offset int64, data []byte, truncate bool) error {
	if err := validPluginStoragePath(name, false); err != nil {
		return err
	}
	if len(data) > pluginStorageMaxChunkBytes || offset < 0 || offset > pluginStorageMaxFileBytes-int64(len(data)) {
		return errors.New("invalid plugin storage write range")
	}
	state, unlock, err := lockPluginStorage(pluginID)
	if err != nil {
		return err
	}
	defer unlock()
	root, err := openPluginStorage(pluginID, true)
	if err != nil {
		return err
	}
	defer root.Close()
	dir, leaf, err := openStorageParent(root, name, true)
	if err != nil {
		return err
	}
	defer dir.Close()
	info, err := dir.Lstat(leaf)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	exists := err == nil
	if exists && !info.Mode().IsRegular() {
		return errors.New("plugin storage path is not a regular file")
	}
	var source *os.File
	closeSource := func() error {
		if source == nil {
			return nil
		}
		err := source.Close()
		source = nil
		return err
	}
	defer closeSource()
	oldSize := int64(0)
	if exists {
		if source, err = dir.Open(leaf); err != nil {
			return err
		}
		opened, err := source.Stat()
		if err != nil {
			return err
		}
		if !opened.Mode().IsRegular() {
			return errors.New("plugin storage path is not a regular file")
		}
		if opened.Size() > pluginStorageMaxFileBytes {
			return errors.New("plugin storage file exceeds limit")
		}
		oldSize = opened.Size()
	}
	if offset > oldSize {
		return errors.New("write offset is beyond end of file")
	}
	end := offset + int64(len(data))
	newSize := end
	if !truncate && oldSize > newSize {
		newSize = oldSize
	}
	if newSize > pluginStorageMaxFileBytes {
		return errors.New("plugin storage file exceeds limit")
	}
	if err := state.ensureUsage(root); err != nil {
		return err
	}
	count := state.files
	if !exists {
		count++
	}
	if count > pluginStorageMaxFiles {
		return errors.New("plugin storage file count exceeded")
	}
	if state.bytes-oldSize+newSize+state.stagedBytes(nil) > pluginStorageMaxBytes {
		return errors.New("plugin storage quota exceeded")
	}
	token, err := newStorageToken()
	if err != nil {
		return err
	}
	tempName := pluginStorageTempPrefix + token
	tmp, err := dir.OpenFile(tempName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer dir.Remove(tempName)
	if source != nil && offset > 0 {
		_, err = io.Copy(tmp, io.NewSectionReader(source, 0, offset))
	}
	if err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil && newSize > end {
		_, err = io.Copy(tmp, io.NewSectionReader(source, end, newSize-end))
	}
	if err == nil {
		var written int64
		if written, err = tmp.Seek(0, io.SeekCurrent); err == nil && written != newSize {
			err = errors.New("plugin storage file changed during write")
		}
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	// Release our own handle before replacing the file so the rename does not
	// depend on the platform's share-delete semantics for open files.
	if closeErr := closeSource(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if current, err := dir.Lstat(leaf); err == nil {
		if !current.Mode().IsRegular() {
			return errors.New("plugin storage destination changed")
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := dir.Rename(tempName, leaf); err != nil {
		state.usageKnown = false
		return fmt.Errorf("atomic plugin storage write: %w", err)
	}
	state.files = count
	state.bytes += newSize - oldSize
	return nil
}

// ---------------------------------------------------------------------------
// Staged writes: begin creates a private staging file next to the target,
// append only appends to it (no read of the old file, no directory walk, no
// fsync), and commit re-validates everything once before an atomic rename.

type pluginStorageBeginResult struct {
	Session       string `json:"session"`
	MaxChunkBytes int    `json:"maxChunkBytes"`
	IdleTimeoutMs int64  `json:"idleTimeoutMs"`
}

var errStorageSessionUnknown = errors.New("unknown or expired plugin storage write session")

func pluginStorageBegin(owner any, pluginID, name string) (pluginStorageBeginResult, error) {
	if err := validPluginStoragePath(name, false); err != nil {
		return pluginStorageBeginResult{}, err
	}
	state, unlock, err := lockPluginStorage(pluginID)
	if err != nil {
		return pluginStorageBeginResult{}, err
	}
	defer unlock()
	root, err := openPluginStorage(pluginID, true)
	if err != nil {
		return pluginStorageBeginResult{}, err
	}
	defer root.Close()
	// A new begin for the same path supersedes an abandoned one (for example
	// after the plugin's previous attempt failed mid-transfer).
	for _, session := range state.sessions {
		if session.name == name && session.owner == owner {
			state.dropSession(root, session)
		}
	}
	if len(state.sessions) >= pluginStorageMaxSessions {
		return pluginStorageBeginResult{}, fmt.Errorf("too many concurrent plugin storage write sessions (max %d)", pluginStorageMaxSessions)
	}
	dir, leaf, err := openStorageParent(root, name, true)
	if err != nil {
		return pluginStorageBeginResult{}, err
	}
	defer dir.Close()
	replaced, newFile := int64(0), true
	if info, err := dir.Lstat(leaf); err == nil {
		if !info.Mode().IsRegular() {
			return pluginStorageBeginResult{}, errors.New("plugin storage path is not a regular file")
		}
		replaced, newFile = info.Size(), false
	} else if !errors.Is(err, fs.ErrNotExist) {
		return pluginStorageBeginResult{}, err
	}
	if err := state.ensureUsage(root); err != nil {
		return pluginStorageBeginResult{}, err
	}
	if newFile && state.files+state.stagedNewFiles()+1 > pluginStorageMaxFiles {
		return pluginStorageBeginResult{}, errors.New("plugin storage file count exceeded")
	}
	id, err := newStorageToken()
	if err != nil {
		return pluginStorageBeginResult{}, err
	}
	stageLeaf := pluginStorageStagePrefix + id
	file, err := dir.OpenFile(stageLeaf, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return pluginStorageBeginResult{}, err
	}
	stage := stageLeaf
	if parent := path.Dir(name); parent != "." {
		stage = parent + "/" + stageLeaf
	}
	idle := pluginStorageSessionIdle
	session := &pluginStorageSession{id: id, owner: owner, name: name, stage: stage, file: file, replaced: replaced, newFile: newFile, idle: idle, active: time.Now()}
	session.timer = time.AfterFunc(idle, func() { abortPluginStorageSession(pluginID, id) })
	state.sessions[id] = session
	return pluginStorageBeginResult{Session: id, MaxChunkBytes: pluginStorageMaxChunkBytes, IdleTimeoutMs: idle.Milliseconds()}, nil
}

func (state *pluginStorageState) session(owner any, id string) (*pluginStorageSession, error) {
	session := state.sessions[id]
	if session == nil || session.owner != owner {
		return nil, errStorageSessionUnknown
	}
	return session, nil
}

// dropSession closes and deletes a session's staging file. root may be nil.
func (state *pluginStorageState) dropSession(root *os.Root, session *pluginStorageSession) {
	delete(state.sessions, session.id)
	session.timer.Stop()
	if session.file != nil {
		_ = session.file.Close()
		session.file = nil
	}
	if root == nil {
		opened, err := openPluginStorage(state.pluginID, false)
		if err != nil {
			return
		}
		defer opened.Close()
		root = opened
	}
	_ = root.Remove(session.stage)
}

func pluginStorageAppend(owner any, pluginID, id string, offset int64, data []byte) error {
	if len(data) > pluginStorageMaxChunkBytes {
		return errors.New("plugin storage write chunk exceeds limit")
	}
	state, unlock, err := lockPluginStorage(pluginID)
	if err != nil {
		return err
	}
	defer unlock()
	session, err := state.session(owner, id)
	if err != nil {
		return err
	}
	if offset != session.size {
		return fmt.Errorf("staged write offset %d does not match staged size %d", offset, session.size)
	}
	if session.size+int64(len(data)) > pluginStorageMaxFileBytes {
		return errors.New("plugin storage file exceeds limit")
	}
	if !state.usageKnown {
		root, err := openPluginStorage(pluginID, false)
		if err != nil {
			return err
		}
		err = state.ensureUsage(root)
		root.Close()
		if err != nil {
			return err
		}
	}
	growth := max(int64(0), session.size+int64(len(data))-session.replaced)
	if state.bytes+state.stagedBytes(session)+growth > pluginStorageMaxBytes {
		return errors.New("plugin storage quota exceeded")
	}
	session.active = time.Now()
	session.timer.Reset(session.idle)
	if len(data) == 0 {
		return nil
	}
	if _, err := session.file.Write(data); err != nil {
		state.dropSession(nil, session)
		return err
	}
	session.size += int64(len(data))
	return nil
}

// pluginStorageCommit fsyncs the staged file and atomically renames it over the
// target after re-checking path containment, symlinks, the per-file limit, the
// file count and the total quota. expectedSize < 0 skips the size check.
// Commit always ends the session.
func pluginStorageCommit(owner any, pluginID, id string, expectedSize int64) error {
	state, unlock, err := lockPluginStorage(pluginID)
	if err != nil {
		return err
	}
	defer unlock()
	session, err := state.session(owner, id)
	if err != nil {
		return err
	}
	root, err := openPluginStorage(pluginID, false)
	if err != nil {
		state.dropSession(nil, session)
		return err
	}
	defer root.Close()
	committed := false
	defer func() {
		if !committed {
			state.dropSession(root, session)
		}
	}()
	if expectedSize >= 0 && expectedSize != session.size {
		return fmt.Errorf("staged write has %d bytes, expected %d", session.size, expectedSize)
	}
	if session.size > pluginStorageMaxFileBytes {
		return errors.New("plugin storage file exceeds limit")
	}
	err = session.file.Sync()
	if closeErr := session.file.Close(); err == nil {
		err = closeErr
	}
	session.file = nil
	if err != nil {
		return err
	}
	dir, leaf, err := openStorageParent(root, session.name, false)
	if err != nil {
		return err
	}
	defer dir.Close()
	stageLeaf := path.Base(session.stage)
	staged, err := dir.Lstat(stageLeaf)
	if err != nil {
		return fmt.Errorf("staged plugin storage file is missing: %w", err)
	}
	if !staged.Mode().IsRegular() || staged.Size() != session.size {
		return errors.New("staged plugin storage file changed")
	}
	oldSize, exists := int64(0), false
	if current, err := dir.Lstat(leaf); err == nil {
		if !current.Mode().IsRegular() {
			return errors.New("plugin storage path is not a regular file")
		}
		oldSize, exists = current.Size(), true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	// Authoritative usage, once per commit. walkUsage skips every active
	// session's staging file, this one included.
	files, bytes, err := state.walkUsage(root)
	if err != nil {
		return err
	}
	state.files, state.bytes, state.usageKnown = files, bytes, true
	if !exists && files+1 > pluginStorageMaxFiles {
		return errors.New("plugin storage file count exceeded")
	}
	if bytes-oldSize+session.size+state.stagedBytes(session) > pluginStorageMaxBytes {
		return errors.New("plugin storage quota exceeded")
	}
	if err := dir.Rename(stageLeaf, leaf); err != nil {
		state.usageKnown = false
		return fmt.Errorf("atomic plugin storage write: %w", err)
	}
	committed = true
	delete(state.sessions, session.id)
	session.timer.Stop()
	if !exists {
		state.files++
	}
	state.bytes += session.size - oldSize
	return nil
}

func pluginStorageAbort(owner any, pluginID, id string) error {
	state, unlock, err := lockPluginStorage(pluginID)
	if err != nil {
		return err
	}
	defer unlock()
	// Aborting an unknown or already finished session is not an error.
	if session, err := state.session(owner, id); err == nil {
		state.dropSession(nil, session)
	}
	return nil
}

func abortPluginStorageSession(pluginID, id string) {
	pluginStorageMu.RLock()
	defer pluginStorageMu.RUnlock()
	state, err := pluginStorageStateFor(pluginID, false)
	if err != nil || state == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if session := state.sessions[id]; session != nil && time.Since(session.active) >= session.idle {
		state.dropSession(nil, session)
	}
}

// abortPluginStorageSessions drops every staged write of a plugin; it runs when
// the plugin's runtime stops (which also precedes uninstall, clear data and
// data import).
func abortPluginStorageSessions(pluginID string) {
	pluginStorageMu.RLock()
	defer pluginStorageMu.RUnlock()
	state, err := pluginStorageStateFor(pluginID, false)
	if err != nil || state == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.sessions) == 0 {
		return
	}
	root, _ := openPluginStorage(pluginID, false)
	if root != nil {
		defer root.Close()
	}
	for _, session := range state.sessions {
		if root == nil {
			// Without a root the staging file cannot be removed safely; still
			// release the handle.
			delete(state.sessions, session.id)
			session.timer.Stop()
			if session.file != nil {
				_ = session.file.Close()
			}
			continue
		}
		state.dropSession(root, session)
	}
}

func pluginStorageDelete(pluginID, name string) error {
	if err := validPluginStoragePath(name, false); err != nil {
		return err
	}
	state, unlock, err := lockPluginStorage(pluginID)
	if err != nil {
		return err
	}
	defer unlock()
	root, err := openPluginStorage(pluginID, false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	if err := checkStoragePath(root, name, false, false); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("plugin storage delete requires a regular file")
	}
	if err := root.Remove(name); err != nil {
		state.usageKnown = false
		return err
	}
	if state.usageKnown {
		state.files--
		state.bytes -= info.Size()
	}
	return nil
}

func pluginStorageList(pluginID, prefix string) ([]string, error) {
	if err := validPluginStoragePath(prefix, true); err != nil {
		return nil, err
	}
	root, err := openPluginStorage(pluginID, false)
	if errors.Is(err, fs.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if prefix != "" {
		if err := checkStoragePath(root, prefix, false, true); errors.Is(err, fs.ErrNotExist) {
			return []string{}, nil
		} else if err != nil {
			return nil, err
		}
	}
	files := []string{}
	start := "."
	if prefix != "" {
		start = prefix
	}
	err = fs.WalkDir(root.FS(), start, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("plugin storage contains a symlink")
		}
		if entry.IsDir() {
			return nil
		}
		if isPluginStorageScratchName(path.Base(name)) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("plugin storage contains a non-regular file")
		}
		files = append(files, name)
		return nil
	})
	sort.Strings(files)
	return files, err
}

func pluginStorageStat(pluginID, name string) (any, error) {
	if err := validPluginStoragePath(name, false); err != nil {
		return nil, err
	}
	root, err := openPluginStorage(pluginID, false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err := checkStoragePath(root, name, false, false); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	info, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("plugin storage path is not a regular file")
	}
	return map[string]any{"size": info.Size(), "modifiedAt": info.ModTime().UTC().Format("2006-01-02T15:04:05.000Z")}, nil
}

func encodeStorageData(data []byte) string { return base64.StdEncoding.EncodeToString(data) }
func decodeStorageData(value string) ([]byte, error) {
	if len(value) > base64.StdEncoding.EncodedLen(pluginStorageMaxChunkBytes) {
		return nil, errors.New("plugin storage write chunk exceeds limit")
	}
	data, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, errors.New("plugin storage data must be base64")
	}
	return data, nil
}
