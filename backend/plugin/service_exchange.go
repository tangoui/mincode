package plugin

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// The service file exchange (design §8) is the host-private directory through
// which service calls move files. Plugins only ever see opaque tokens
// ("sf_" + 48 hex); every disk name is host-generated, so a plugin-supplied
// file name never becomes part of a path.
//
// Layout under the root:
//
//	calls/<random>/<random>  input snapshots, adopted uploads and outputs of one call
//	uploads/<random>         upload sessions and committed uploads
//
// Access rules: an input (snapshot or adopted upload) is readable only by the
// provider it was given to; an upload is referenced only by its uploader (to
// adopt it into a call) and is not readable; an output is readable by nobody
// until handOutputs gives it to the caller, after which it belongs to the
// caller. Unknown tokens and tokens owned by someone else both report
// not_found, so a plugin cannot probe for other plugins' files.
//
// The whole exchange is ephemeral: manager startup removes whatever a previous
// run left in the root (startupClearServiceExchange; no call can be running
// yet), and the first use does the same under the same once (ensureDir) for a
// process whose manager never started.

const (
	serviceExchangeDirectory   = "plugin-exchange"
	serviceExchangeTokenPrefix = "sf_"
	// Session IDs use a different prefix so they are never confused with file
	// tokens.
	serviceExchangeSessionPrefix = "ss_"
	serviceExchangeTokenBytes    = 24
	// serviceExchangeMaxSessions bounds open upload/create sessions per plugin.
	serviceExchangeMaxSessions = 8
	// serviceExchangeNameMaxBytes bounds the sanitized display name.
	serviceExchangeNameMaxBytes = 255
	serviceExchangeExtMaxBytes  = 32
)

// Limits come from the services contribution point; they are variables so
// tests can lower them.
var (
	serviceChunkMaxBytes       = int(serviceSpec.Limit("chunkMaxBytes"))
	serviceCallMaxBytes        = serviceSpec.Limit("callMaxBytes")
	serviceOutputRetention     = time.Duration(serviceSpec.Limit("outputRetentionMs")) * time.Millisecond
	serviceFileCallsConcurrent = int(serviceSpec.Limit("fileCallsConcurrent"))
	serviceExchangeMaxBytes    = serviceSpec.Limit("exchangeMaxBytes")

	// serviceExchangeNow is the clock for output and upload retention.
	serviceExchangeNow = time.Now
	// serviceExchangeSessionIdle discards an upload or create session that
	// receives no call for this long (like staged storage writes).
	serviceExchangeSessionIdle = 60 * time.Second
)

// serviceFiles is the host's exchange. Its root is resolved when used, so it
// follows userDataDir.
var serviceFiles = newServiceExchangeAt(func() string {
	return filepath.Join(userDataDir(), pluginDataDirectory, serviceExchangeDirectory)
})

// startupClearServiceExchange clears the exchange's leftovers at manager
// startup. Tests replace it so starting a manager never clears the real user
// data directory.
var startupClearServiceExchange = func() { serviceFiles.clearLeftovers() }

func init() {
	// Stop, crash, disable, reload and uninstall end the plugin's runtime; its
	// sessions, the inputs it was given and the outputs it holds go with it.
	registerRecordLifecycleHook(func(_ *pluginManager, record *pluginRecord) {
		serviceFiles.dropPlugin(record.id)
	})
}

// exchangeFile is the handle a plugin sees for an exchange file.
type exchangeFile struct {
	Token string `json:"token"`
	Name  string `json:"name"`
	Mime  string `json:"mime"`
	Size  int64  `json:"size"`
}

type exchangeKind uint8

const (
	exchangeInput exchangeKind = iota + 1
	exchangeUpload
	exchangeOutput
)

type exchangeCall struct {
	id    string
	dir   string
	bytes int64 // inputs, outputs and open create sessions of this call
	files int   // entries still on disk in dir
	ended bool
}

type exchangeEntry struct {
	file exchangeFile
	kind exchangeKind
	path string
	call *exchangeCall // nil for uploads
	// owner may read (inputs, handed outputs) or adopt (uploads) the file and
	// release it; "" for an output not yet handed to its caller.
	owner string
	// creator is the provider that created an output.
	creator string
	// expires is zero while the file's call is running.
	expires time.Time
}

type exchangeSession struct {
	id     string
	owner  string
	name   string        // sanitized; create sessions only
	call   *exchangeCall // nil for upload sessions
	path   string
	file   *os.File
	size   int64
	timer  *time.Timer
	active time.Time // real time of the last call
	idle   time.Duration
}

type serviceExchange struct {
	rootFn func() string
	// firstUse clears what a previous run left in the root.
	firstUse sync.Once

	mu sync.Mutex
	// gen changes at clearAll so copies that ran outside the lock notice.
	gen      uint64
	used     int64 // bytes on disk: committed files plus open sessions
	calls    map[string]*exchangeCall
	files    map[string]*exchangeEntry
	sessions map[string]*exchangeSession
}

func newServiceExchange(root string) *serviceExchange {
	return newServiceExchangeAt(func() string { return root })
}

func newServiceExchangeAt(rootFn func() string) *serviceExchange {
	return &serviceExchange{
		rootFn:   rootFn,
		calls:    map[string]*exchangeCall{},
		files:    map[string]*exchangeEntry{},
		sessions: map[string]*exchangeSession{},
	}
}

func exchangeNotFound() error {
	return newContributionError(contributionErrNotFound, "unknown or expired service file")
}

func exchangeSessionNotFound() error {
	return newContributionError(contributionErrNotFound, "unknown or expired service file session")
}

func exchangeCallNotFound() error {
	return newContributionError(contributionErrNotFound, "unknown or finished service call")
}

func exchangeFull() error {
	return newContributionError(contributionErrBusy, "the service file exchange is full (max %d bytes)", serviceExchangeMaxBytes)
}

// exchangeIOError drops the path from a filesystem error: these messages can
// reach plugins, which must never learn exchange or source paths.
func exchangeIOError(op string, err error) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		err = pathErr.Err
	}
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) {
		err = linkErr.Err
	}
	return fmt.Errorf("service file %s: %w", op, err)
}

func newExchangeRandom(prefix string) (string, error) {
	var random [serviceExchangeTokenBytes]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(random[:]), nil
}

// root returns the absolute exchange root; a relative root (for example an
// unresolved data directory) is refused so nothing is created or removed
// relative to the working directory.
func (x *serviceExchange) root() (string, error) {
	root := x.rootFn()
	if root == "" || !filepath.IsAbs(root) {
		return "", errors.New("service file exchange: no usable data directory")
	}
	return filepath.Clean(root), nil
}

// clearLeftovers removes what a previous run left in the root, once per
// exchange; an unusable root is left for ensureDir to report.
func (x *serviceExchange) clearLeftovers() {
	if root, err := x.root(); err == nil {
		x.clearLeftoversAt(root)
	}
}

func (x *serviceExchange) clearLeftoversAt(root string) {
	x.firstUse.Do(func() {
		if err := os.RemoveAll(root); err != nil {
			log.Printf("plugin service exchange: clearing leftovers failed: %v", err)
		}
	})
}

func (x *serviceExchange) ensureDir(parts ...string) (string, error) {
	root, err := x.root()
	if err != nil {
		return "", err
	}
	x.clearLeftoversAt(root)
	dir := filepath.Join(append([]string{root}, parts...)...)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", exchangeIOError("exchange directory", err)
	}
	// MkdirAll leaves existing directories alone; keep the root private.
	_ = os.Chmod(root, 0o700)
	return dir, nil
}

// sweepLocked removes files whose retention has passed.
func (x *serviceExchange) sweepLocked() {
	now := serviceExchangeNow()
	for token, entry := range x.files {
		if !entry.expires.IsZero() && !now.Before(entry.expires) {
			x.removeEntryLocked(token, entry)
		}
	}
}

func (x *serviceExchange) sweep() {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.sweepLocked()
}

// scheduleSweep sweeps once after d (plus a margin) so expired files leave
// the disk even when nothing else touches the exchange.
func (x *serviceExchange) scheduleSweep(d time.Duration) {
	time.AfterFunc(d+time.Second, x.sweep)
}

func (x *serviceExchange) removeEntryLocked(token string, entry *exchangeEntry) {
	delete(x.files, token)
	_ = os.Remove(entry.path)
	x.used -= entry.file.Size
	if call := entry.call; call != nil {
		call.bytes -= entry.file.Size
		call.files--
		if call.ended && call.files <= 0 {
			_ = os.RemoveAll(call.dir)
		}
	}
}

func (x *serviceExchange) addEntryLocked(entry *exchangeEntry) {
	x.files[entry.file.Token] = entry
	if entry.call != nil {
		entry.call.files++
	}
}

// dropSessionLocked closes and deletes a session's file.
func (x *serviceExchange) dropSessionLocked(session *exchangeSession) {
	delete(x.sessions, session.id)
	session.timer.Stop()
	if session.file != nil {
		_ = session.file.Close()
		session.file = nil
	}
	_ = os.Remove(session.path)
	x.used -= session.size
	if session.call != nil {
		session.call.bytes -= session.size
	}
}

// expireSession is the idle timer of one session.
func (x *serviceExchange) expireSession(id string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	session := x.sessions[id]
	if session == nil {
		return
	}
	if idle := time.Since(session.active); idle < session.idle {
		session.timer.Reset(session.idle - idle)
		return
	}
	x.dropSessionLocked(session)
}

func (x *serviceExchange) activeCallLocked(callID string) (*exchangeCall, error) {
	call := x.calls[callID]
	if call == nil || call.ended {
		return nil, exchangeCallNotFound()
	}
	return call, nil
}

// beginCall reserves an exchange directory for one call that moves files. It
// fails with busy when fileCallsConcurrent calls are running or the exchange
// is full.
func (x *serviceExchange) beginCall(callID string) error {
	if callID == "" {
		return newContributionError(contributionErrInvalidRequest, "missing service call id")
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	x.sweepLocked()
	if x.calls[callID] != nil {
		return newContributionError(contributionErrInvalidRequest, "duplicate service call id")
	}
	if len(x.calls) >= serviceFileCallsConcurrent {
		return newContributionError(contributionErrBusy, "too many service calls with files are running (max %d)", serviceFileCallsConcurrent)
	}
	if x.used >= serviceExchangeMaxBytes {
		return exchangeFull()
	}
	name, err := newExchangeRandom("")
	if err != nil {
		return err
	}
	dir, err := x.ensureDir("calls", name[:32])
	if err != nil {
		return err
	}
	x.calls[callID] = &exchangeCall{id: callID, dir: dir}
	return nil
}

// endCall deletes the call's inputs, open create sessions and outputs not
// handed to the caller. Handed outputs are kept for outputRetentionMs when
// keepOutputs, and deleted otherwise (failed or cancelled calls).
func (x *serviceExchange) endCall(callID string, keepOutputs bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	call := x.calls[callID]
	if call == nil {
		return
	}
	delete(x.calls, callID)
	call.ended = true
	for _, session := range x.sessions {
		if session.call == call {
			x.dropSessionLocked(session)
		}
	}
	expires := serviceExchangeNow().Add(serviceOutputRetention)
	kept := false
	for token, entry := range x.files {
		if entry.call != call {
			continue
		}
		if keepOutputs && entry.kind == exchangeOutput && entry.owner != "" {
			entry.expires = expires
			kept = true
			continue
		}
		x.removeEntryLocked(token, entry)
	}
	if call.files <= 0 {
		_ = os.RemoveAll(call.dir)
	}
	if kept {
		x.scheduleSweep(serviceOutputRetention)
	}
}

// snapshotPath copies absPath into the call's directory as an input readable
// only by owner (the provider). The copy runs outside the lock against a
// reservation; a source that grows meanwhile is cut at its size at open.
func (x *serviceExchange) snapshotPath(callID, owner, absPath string) (exchangeFile, error) {
	if owner == "" || !filepath.IsAbs(absPath) {
		return exchangeFile{}, newContributionError(contributionErrInvalidRequest, "invalid service file source")
	}
	// Check the type before opening: opening a FIFO or device can block
	// forever and would hold one of the global file-call slots.
	pathInfo, err := os.Stat(absPath)
	if err != nil {
		return exchangeFile{}, newContributionError(contributionErrNotFound, "service file source is not available")
	}
	if !pathInfo.Mode().IsRegular() {
		return exchangeFile{}, newContributionError(contributionErrInvalidRequest, "service file source is not a regular file")
	}
	source, err := os.Open(absPath)
	if err != nil {
		return exchangeFile{}, newContributionError(contributionErrNotFound, "service file source is not available")
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(info, pathInfo) {
		return exchangeFile{}, newContributionError(contributionErrInvalidRequest, "service file source changed while opening")
	}
	size := info.Size()
	if size > serviceFileMaxBytes {
		return exchangeFile{}, newContributionError(contributionErrTooLarge, "service file exceeds %d bytes", serviceFileMaxBytes)
	}

	x.mu.Lock()
	x.sweepLocked()
	call, err := x.activeCallLocked(callID)
	if err == nil {
		err = x.reserveLocked(call, size)
	}
	if err != nil {
		x.mu.Unlock()
		return exchangeFile{}, err
	}
	gen := x.gen
	x.used += size
	call.bytes += size
	x.mu.Unlock()

	path, written, copyErr := copyExchangeSnapshot(call.dir, source, size)

	x.mu.Lock()
	defer x.mu.Unlock()
	if gen != x.gen {
		// clearAll reset the accounting and removed the directory.
		if path != "" {
			_ = os.Remove(path)
		}
		return exchangeFile{}, exchangeCallNotFound()
	}
	x.used -= size
	call.bytes -= size
	if copyErr != nil || call.ended {
		if path != "" {
			_ = os.Remove(path)
		}
		if copyErr != nil {
			return exchangeFile{}, copyErr
		}
		return exchangeFile{}, exchangeCallNotFound()
	}
	token, err := newExchangeRandom(serviceExchangeTokenPrefix)
	if err != nil {
		_ = os.Remove(path)
		return exchangeFile{}, err
	}
	name := sanitizeExchangeName(filepath.Base(absPath))
	file := exchangeFile{Token: token, Name: name, Mime: serviceMimeForName(name), Size: written}
	x.used += written
	call.bytes += written
	x.addEntryLocked(&exchangeEntry{file: file, kind: exchangeInput, path: path, call: call, owner: owner})
	return file, nil
}

// serviceSnapshotChunkBytes is the read size of snapshotReader. It is small
// because a remote Read (pkg/sftp File.Read) fills the whole buffer before it
// returns: with 32 KiB pieces, progress (and the remote stall timer it
// resets) moves whenever about 32 KiB arrived, so a slow but live transfer is
// never taken for a stalled one.
const serviceSnapshotChunkBytes = 32 * 1024

// snapshotReader copies up to size bytes from r into the call's directory as
// an input named name (sanitized) and readable only by owner. size is
// reserved against the call and exchange quotas before the first read, and r
// is cut at size: a source that grows while it is read (a log file being
// written) yields exactly its first size bytes. progress, when set, gets the
// bytes written so far after each chunk.
//
// Reads run in a helper goroutine so that ctx ends the snapshot even while a
// Read is blocked: the partial file is removed, the reservation returned and
// the error is ctx's cause when that is a contribution error (cancelled
// otherwise). The helper exits as soon as its pending Read returns; closing r
// is the caller's business.
func (x *serviceExchange) snapshotReader(ctx context.Context, callID, owner, name string, r io.Reader, size int64, progress func(int64)) (exchangeFile, error) {
	if owner == "" || r == nil || size < 0 {
		return exchangeFile{}, newContributionError(contributionErrInvalidRequest, "invalid service file source")
	}
	if size > serviceFileMaxBytes {
		return exchangeFile{}, newContributionError(contributionErrTooLarge, "service file exceeds %d bytes", serviceFileMaxBytes)
	}
	if err := snapshotContextError(ctx); err != nil {
		return exchangeFile{}, err
	}
	x.mu.Lock()
	x.sweepLocked()
	call, err := x.activeCallLocked(callID)
	if err == nil {
		err = x.reserveLocked(call, size)
	}
	if err != nil {
		x.mu.Unlock()
		return exchangeFile{}, err
	}
	gen := x.gen
	x.used += size
	call.bytes += size
	x.mu.Unlock()

	path, written, copyErr := copyExchangeReader(ctx, call.dir, r, size, progress)

	x.mu.Lock()
	defer x.mu.Unlock()
	if gen != x.gen {
		// clearAll reset the accounting and removed the directory.
		if path != "" {
			_ = os.Remove(path)
		}
		return exchangeFile{}, exchangeCallNotFound()
	}
	x.used -= size
	call.bytes -= size
	if copyErr != nil || call.ended {
		if path != "" {
			_ = os.Remove(path)
		}
		if copyErr != nil {
			return exchangeFile{}, copyErr
		}
		return exchangeFile{}, exchangeCallNotFound()
	}
	token, err := newExchangeRandom(serviceExchangeTokenPrefix)
	if err != nil {
		_ = os.Remove(path)
		return exchangeFile{}, err
	}
	clean := sanitizeExchangeName(name)
	file := exchangeFile{Token: token, Name: clean, Mime: serviceMimeForName(clean), Size: written}
	x.used += written
	call.bytes += written
	x.addEntryLocked(&exchangeEntry{file: file, kind: exchangeInput, path: path, call: call, owner: owner})
	return file, nil
}

// snapshotBytes stores data as an input of the call named name (sanitized),
// readable only by owner, under the same quotas as every snapshot.
func (x *serviceExchange) snapshotBytes(callID, owner, name string, data []byte) (exchangeFile, error) {
	return x.snapshotReader(context.Background(), callID, owner, name, bytes.NewReader(data), int64(len(data)), nil)
}

// snapshotContextError is the error a snapshot reports once ctx has ended:
// its cause when that is a contribution error (a stall timeout, say), a
// cancellation otherwise.
func snapshotContextError(ctx context.Context) error {
	if ctx.Err() == nil {
		return nil
	}
	var coded *contributionError
	if cause := context.Cause(ctx); errors.As(cause, &coded) {
		return coded
	}
	return newContributionError(contributionErrCancelled, "the service file snapshot was cancelled")
}

// copyExchangeReader writes at most size bytes of source to a new file in
// dir. On error the returned path, when set, names the partial file.
func copyExchangeReader(ctx context.Context, dir string, source io.Reader, size int64, progress func(int64)) (string, int64, error) {
	name, err := newExchangeRandom("")
	if err != nil {
		return "", 0, err
	}
	path := filepath.Join(dir, name[:32])
	target, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, exchangeIOError("snapshot", err)
	}
	written, err := pumpExchangeReader(ctx, target, source, size, progress)
	if closeErr := target.Close(); err == nil && closeErr != nil {
		err = exchangeIOError("snapshot", closeErr)
	}
	if err != nil {
		return path, 0, err
	}
	return path, written, nil
}

type exchangeChunk struct {
	data []byte
	err  error
}

// pumpExchangeReader reads source in a helper goroutine and writes what it
// gets to target until EOF, size bytes or the end of ctx. The helper never
// outlives its pending Read: once this returns it drops what it read.
func pumpExchangeReader(ctx context.Context, target io.Writer, source io.Reader, size int64, progress func(int64)) (int64, error) {
	if size == 0 {
		return 0, nil
	}
	chunks := make(chan exchangeChunk)
	quit := make(chan struct{})
	defer close(quit)
	go func() {
		for {
			buffer := make([]byte, min(int64(serviceSnapshotChunkBytes), size))
			n, err := source.Read(buffer)
			select {
			case chunks <- exchangeChunk{data: buffer[:n], err: err}:
			case <-quit:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	var written int64
	for {
		var chunk exchangeChunk
		select {
		case <-ctx.Done():
			return written, snapshotContextError(ctx)
		case chunk = <-chunks:
		}
		if ctx.Err() != nil {
			return written, snapshotContextError(ctx)
		}
		data := chunk.data
		if remaining := size - written; int64(len(data)) > remaining {
			data = data[:remaining]
		}
		if len(data) > 0 {
			if _, err := target.Write(data); err != nil {
				return written, exchangeIOError("snapshot", err)
			}
			written += int64(len(data))
			if progress != nil {
				progress(written)
			}
		}
		switch {
		case written >= size || errors.Is(chunk.err, io.EOF):
			return written, nil
		case chunk.err != nil:
			// A coded error from the source (a remote read) passes as is;
			// anything else gets a fixed message, since a raw error may name
			// a path.
			var coded *contributionError
			if errors.As(chunk.err, &coded) {
				return written, coded
			}
			return written, newContributionError(contributionErrUnavailable, "reading the service file source failed")
		}
	}
}

// reserveLocked checks that size more bytes fit the call and the exchange.
func (x *serviceExchange) reserveLocked(call *exchangeCall, size int64) error {
	if call != nil && call.bytes+size > serviceCallMaxBytes {
		return newContributionError(contributionErrTooLarge, "service call files exceed %d bytes", serviceCallMaxBytes)
	}
	if x.used+size > serviceExchangeMaxBytes {
		return exchangeFull()
	}
	return nil
}

func copyExchangeSnapshot(dir string, source io.Reader, size int64) (string, int64, error) {
	name, err := newExchangeRandom("")
	if err != nil {
		return "", 0, err
	}
	path := filepath.Join(dir, name[:32])
	target, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, exchangeIOError("snapshot", err)
	}
	written, err := io.Copy(target, io.LimitReader(source, size))
	if closeErr := target.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return path, 0, exchangeIOError("snapshot", err)
	}
	return path, written, nil
}

// beginSessionLocked opens a session file in dir for owner.
func (x *serviceExchange) beginSessionLocked(owner, name string, call *exchangeCall, dir string) (string, error) {
	count := 0
	for _, session := range x.sessions {
		if session.owner == owner {
			count++
		}
	}
	if count >= serviceExchangeMaxSessions {
		return "", newContributionError(contributionErrBusy, "too many open service file sessions (max %d)", serviceExchangeMaxSessions)
	}
	id, err := newExchangeRandom(serviceExchangeSessionPrefix)
	if err != nil {
		return "", err
	}
	diskName, err := newExchangeRandom("")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, diskName[:32])
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", exchangeIOError("session", err)
	}
	idle := serviceExchangeSessionIdle
	session := &exchangeSession{id: id, owner: owner, name: name, call: call, path: path, file: file, active: time.Now(), idle: idle}
	session.timer = time.AfterFunc(idle, func() { x.expireSession(id) })
	x.sessions[id] = session
	return id, nil
}

// sessionLocked returns owner's session; upload selects upload (true) or
// create (false) sessions.
func (x *serviceExchange) sessionLocked(owner, id string, upload bool) (*exchangeSession, error) {
	session := x.sessions[id]
	if session == nil || owner == "" || session.owner != owner || (session.call == nil) != upload {
		return nil, exchangeSessionNotFound()
	}
	return session, nil
}

func (x *serviceExchange) appendLocked(session *exchangeSession, offset int64, data []byte) error {
	if len(data) > serviceChunkMaxBytes {
		return newContributionError(contributionErrTooLarge, "service file chunk exceeds %d bytes", serviceChunkMaxBytes)
	}
	if offset != session.size {
		return newContributionError(contributionErrInvalidRequest, "service file offset %d does not match written size %d", offset, session.size)
	}
	growth := int64(len(data))
	if session.size+growth > serviceFileMaxBytes {
		return newContributionError(contributionErrTooLarge, "service file exceeds %d bytes", serviceFileMaxBytes)
	}
	if err := x.reserveLocked(session.call, growth); err != nil {
		return err
	}
	session.active = time.Now()
	session.timer.Reset(session.idle)
	if growth == 0 {
		return nil
	}
	if _, err := session.file.Write(data); err != nil {
		x.dropSessionLocked(session)
		return exchangeIOError("write", err)
	}
	session.size += growth
	x.used += growth
	if session.call != nil {
		session.call.bytes += growth
	}
	return nil
}

// finishSessionLocked closes a session's file and ends the session, keeping
// the file and its accounted bytes for the entry that takes it over.
func (x *serviceExchange) finishSessionLocked(session *exchangeSession) error {
	delete(x.sessions, session.id)
	session.timer.Stop()
	err := session.file.Close()
	session.file = nil
	if err != nil {
		_ = os.Remove(session.path)
		x.used -= session.size
		if session.call != nil {
			session.call.bytes -= session.size
		}
		return exchangeIOError("commit", err)
	}
	return nil
}

// uploadBegin opens an upload session for a caller plugin.
func (x *serviceExchange) uploadBegin(owner string) (string, error) {
	if owner == "" {
		return "", newContributionError(contributionErrInvalidRequest, "missing upload owner")
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	x.sweepLocked()
	if x.used >= serviceExchangeMaxBytes {
		return "", exchangeFull()
	}
	dir, err := x.ensureDir("uploads")
	if err != nil {
		return "", err
	}
	return x.beginSessionLocked(owner, "", nil, dir)
}

func (x *serviceExchange) uploadAppend(owner, session string, offset int64, data []byte) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	current, err := x.sessionLocked(owner, session, true)
	if err != nil {
		return err
	}
	return x.appendLocked(current, offset, data)
}

// uploadCommit ends an upload session and returns an upload token that only
// owner can reference (adoptUpload). Unadopted uploads expire after
// outputRetentionMs.
func (x *serviceExchange) uploadCommit(owner, session, name string) (string, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	current, err := x.sessionLocked(owner, session, true)
	if err != nil {
		return "", err
	}
	token, err := newExchangeRandom(serviceExchangeTokenPrefix)
	if err != nil {
		x.dropSessionLocked(current)
		return "", err
	}
	if err := x.finishSessionLocked(current); err != nil {
		return "", err
	}
	clean := sanitizeExchangeName(name)
	x.addEntryLocked(&exchangeEntry{
		file:    exchangeFile{Token: token, Name: clean, Mime: serviceMimeForName(clean), Size: current.size},
		kind:    exchangeUpload,
		path:    current.path,
		owner:   owner,
		expires: serviceExchangeNow().Add(serviceOutputRetention),
	})
	x.scheduleSweep(serviceOutputRetention)
	return token, nil
}

// uploadInfo returns uploader's committed upload without consuming it.
func (x *serviceExchange) uploadInfo(uploader, uploadToken string) (exchangeFile, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.sweepLocked()
	upload := x.files[uploadToken]
	if upload == nil || upload.kind != exchangeUpload || uploader == "" || upload.owner != uploader {
		return exchangeFile{}, exchangeNotFound()
	}
	return upload.file, nil
}

// adoptUpload moves uploader's upload into the call as an input readable by
// provider, under a new token. The upload token is consumed.
func (x *serviceExchange) adoptUpload(callID, uploader, uploadToken, provider string) (exchangeFile, error) {
	if provider == "" {
		return exchangeFile{}, newContributionError(contributionErrInvalidRequest, "missing provider")
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	x.sweepLocked()
	upload := x.files[uploadToken]
	if upload == nil || upload.kind != exchangeUpload || uploader == "" || upload.owner != uploader {
		return exchangeFile{}, exchangeNotFound()
	}
	call, err := x.activeCallLocked(callID)
	if err != nil {
		return exchangeFile{}, err
	}
	if call.bytes+upload.file.Size > serviceCallMaxBytes {
		return exchangeFile{}, newContributionError(contributionErrTooLarge, "service call files exceed %d bytes", serviceCallMaxBytes)
	}
	token, err := newExchangeRandom(serviceExchangeTokenPrefix)
	if err != nil {
		return exchangeFile{}, err
	}
	diskName, err := newExchangeRandom("")
	if err != nil {
		return exchangeFile{}, err
	}
	path := filepath.Join(call.dir, diskName[:32])
	if err := os.Rename(upload.path, path); err != nil {
		return exchangeFile{}, exchangeIOError("adopt", err)
	}
	delete(x.files, uploadToken)
	file := upload.file
	file.Token = token
	call.bytes += file.Size
	x.addEntryLocked(&exchangeEntry{file: file, kind: exchangeInput, path: path, call: call, owner: provider})
	return file, nil
}

// createBegin opens an output file of the call for provider.
func (x *serviceExchange) createBegin(callID, provider, name string) (string, error) {
	if provider == "" {
		return "", newContributionError(contributionErrInvalidRequest, "missing provider")
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	x.sweepLocked()
	call, err := x.activeCallLocked(callID)
	if err != nil {
		return "", err
	}
	return x.beginSessionLocked(provider, sanitizeExchangeName(name), call, call.dir)
}

func (x *serviceExchange) createAppend(provider, session string, offset int64, data []byte) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	current, err := x.sessionLocked(provider, session, false)
	if err != nil {
		return err
	}
	return x.appendLocked(current, offset, data)
}

// createCommit ends a create session. The output is readable by nobody until
// handOutputs gives it to the caller.
func (x *serviceExchange) createCommit(provider, session string) (exchangeFile, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	current, err := x.sessionLocked(provider, session, false)
	if err != nil {
		return exchangeFile{}, err
	}
	token, err := newExchangeRandom(serviceExchangeTokenPrefix)
	if err != nil {
		x.dropSessionLocked(current)
		return exchangeFile{}, err
	}
	if err := x.finishSessionLocked(current); err != nil {
		return exchangeFile{}, err
	}
	file := exchangeFile{Token: token, Name: current.name, Mime: serviceMimeForName(current.name), Size: current.size}
	x.addEntryLocked(&exchangeEntry{file: file, kind: exchangeOutput, path: current.path, call: current.call, creator: provider})
	return file, nil
}

// readableLocked returns reader's readable entry: an input given to it or an
// output handed to it.
func (x *serviceExchange) readableLocked(reader, token string) (*exchangeEntry, error) {
	entry := x.files[token]
	if entry == nil || reader == "" || entry.kind == exchangeUpload || entry.owner != reader {
		return nil, exchangeNotFound()
	}
	return entry, nil
}

// read returns up to length bytes at offset; an offset at or past the end
// returns no bytes.
func (x *serviceExchange) read(reader, token string, offset int64, length int) ([]byte, error) {
	if offset < 0 || length < 0 {
		return nil, newContributionError(contributionErrInvalidRequest, "invalid service file range")
	}
	if length > serviceChunkMaxBytes {
		return nil, newContributionError(contributionErrTooLarge, "service file read exceeds %d bytes", serviceChunkMaxBytes)
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	x.sweepLocked()
	entry, err := x.readableLocked(reader, token)
	if err != nil {
		return nil, err
	}
	if offset >= entry.file.Size {
		return []byte{}, nil
	}
	length = int(min(int64(length), entry.file.Size-offset))
	file, err := os.Open(entry.path)
	if err != nil {
		return nil, exchangeIOError("read", err)
	}
	defer file.Close()
	data := make([]byte, length)
	read, err := file.ReadAt(data, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, exchangeIOError("read", err)
	}
	return data[:read], nil
}

// handOutputs checks that every token is an output this provider created in
// this call, then makes them readable by caller. Either all are handed or
// none.
func (x *serviceExchange) handOutputs(callID, provider, caller string, tokens []string) ([]exchangeFile, error) {
	if caller == "" {
		return nil, newContributionError(contributionErrInvalidRequest, "missing caller")
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	x.sweepLocked()
	call, err := x.activeCallLocked(callID)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(tokens))
	entries := make([]*exchangeEntry, 0, len(tokens))
	for _, token := range tokens {
		if seen[token] {
			return nil, newContributionError(contributionErrInvalidRequest, "duplicate service file token")
		}
		seen[token] = true
		entry := x.files[token]
		if entry == nil || entry.kind != exchangeOutput || entry.call != call || provider == "" || entry.creator != provider ||
			(entry.owner != "" && entry.owner != caller) {
			return nil, exchangeNotFound()
		}
		entries = append(entries, entry)
	}
	files := make([]exchangeFile, 0, len(entries))
	for _, entry := range entries {
		entry.owner = caller
		files = append(files, entry.file)
	}
	return files, nil
}

// copyOut writes reader's file to dstPath atomically (temporary file in the
// destination directory, then rename). It is how outputs reach the caller's
// storage or a path the user chose in a save dialog.
func (x *serviceExchange) copyOut(reader, token, dstPath string) error {
	if !filepath.IsAbs(dstPath) {
		return newContributionError(contributionErrInvalidRequest, "invalid service file destination")
	}
	x.mu.Lock()
	x.sweepLocked()
	entry, err := x.readableLocked(reader, token)
	var source *os.File
	var size int64
	if err == nil {
		size = entry.file.Size
		source, err = os.Open(entry.path)
		if err != nil {
			err = exchangeIOError("save", err)
		}
	}
	x.mu.Unlock()
	if err != nil {
		return err
	}
	defer source.Close()
	temp, err := os.CreateTemp(filepath.Dir(dstPath), ".mincode-service-*")
	if err != nil {
		return exchangeIOError("save", err)
	}
	tempPath := temp.Name()
	_, err = io.Copy(temp, io.LimitReader(source, size))
	if err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Chmod(tempPath, 0o644)
	}
	if err == nil {
		err = os.Rename(tempPath, dstPath)
	}
	if err != nil {
		_ = os.Remove(tempPath)
		return exchangeIOError("save", err)
	}
	return nil
}

// release deletes a file early. Only its current owner can release it;
// anything else is ignored.
func (x *serviceExchange) release(reader, token string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if entry := x.files[token]; entry != nil && reader != "" && entry.owner == reader {
		x.removeEntryLocked(token, entry)
	}
}

// callFile reports whether token is a file of callID that reader owns.
func (x *serviceExchange) callFile(reader, callID, token string) bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.sweepLocked()
	entry := x.files[token]
	return entry != nil && reader != "" && entry.owner == reader && entry.call != nil && entry.call.id == callID
}

// releaseCall deletes every file of callID that reader owns.
func (x *serviceExchange) releaseCall(reader, callID string) {
	if reader == "" {
		return
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	for token, entry := range x.files {
		if entry.owner == reader && entry.call != nil && entry.call.id == callID {
			x.removeEntryLocked(token, entry)
		}
	}
}

// dropPlugin removes what pluginID holds when its runtime ends: its upload
// and create sessions, the inputs it was given, its uploads, the outputs
// handed to it and the outputs it created that were not handed over yet.
// Outputs it created and already handed to a caller belong to the caller and
// stay (providers are idle-stopped routinely after a call).
func (x *serviceExchange) dropPlugin(pluginID string) {
	if pluginID == "" {
		return
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	for _, session := range x.sessions {
		if session.owner == pluginID {
			x.dropSessionLocked(session)
		}
	}
	for token, entry := range x.files {
		if entry.owner == pluginID || (entry.kind == exchangeOutput && entry.owner == "" && entry.creator == pluginID) {
			x.removeEntryLocked(token, entry)
		}
	}
}

// clearAll forgets every call, session and file and removes the exchange
// directory.
func (x *serviceExchange) clearAll() error {
	x.mu.Lock()
	defer x.mu.Unlock()
	for _, session := range x.sessions {
		session.timer.Stop()
		if session.file != nil {
			_ = session.file.Close()
			session.file = nil
		}
	}
	for _, call := range x.calls {
		call.ended = true
	}
	x.gen++
	x.used = 0
	x.calls = map[string]*exchangeCall{}
	x.files = map[string]*exchangeEntry{}
	x.sessions = map[string]*exchangeSession{}
	root, err := x.root()
	if err != nil {
		return err
	}
	return os.RemoveAll(root)
}

// sanitizeExchangeName turns a plugin- or path-supplied name into a safe
// display base name: the last path element, valid UTF-8, no control, format
// or reserved characters, no trailing dots or spaces, at most 255 bytes with
// the extension kept. It is never used as a disk name.
func sanitizeExchangeName(name string) string {
	name = strings.ToValidUTF8(name, "_")
	if index := strings.LastIndexAny(name, `/\`); index >= 0 {
		name = name[index+1:]
	}
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || strings.ContainsRune(`<>:"|?*`, r) {
			return '_'
		}
		return r
	}, name)
	name = strings.TrimRight(strings.TrimSpace(name), ". ")
	if name == "" {
		return "file"
	}
	if len(name) > serviceExchangeNameMaxBytes {
		ext := filepath.Ext(name)
		if len(ext) > serviceExchangeExtMaxBytes {
			ext = ""
		}
		stem := name[:serviceExchangeNameMaxBytes-len(ext)]
		for !utf8.ValidString(stem) {
			stem = stem[:len(stem)-1]
		}
		name = stem + ext
	}
	return name
}
