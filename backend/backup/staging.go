package backup

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// StagingTTL is how long a staged backup is kept after it was created or
// last written to or read.
const StagingTTL = 30 * time.Minute

// MaxChunk bounds one Read result and one Append chunk.
const MaxChunk = 512 << 10

var (
	ErrStagingNotFound = errors.New("暂存的备份不存在或已过期")
	ErrStagingChecksum = errors.New("备份文件校验失败")
	ErrStagingOffset   = errors.New("备份分块偏移不连续")
	ErrStagingChunk    = errors.New("备份分块超过 512 KiB 上限")
	ErrStagingRange    = errors.New("备份读取范围无效")
)

const (
	stagingDirMode  = 0o700
	stagingFileMode = 0o600
	stagingTempGlob = ".create-*"
)

// Staging holds backup files between the steps of a multi-call transfer: a
// backup built by Create is read back in chunks, and an uploaded backup is
// written with Begin/Append/Commit before it is restored from Path.
//
// Every entry belongs to an owner (the plugin id); any other owner sees
// ErrStagingNotFound, even with the right token. Entries live in memory, so
// files left by a previous run are deleted by NewStaging.
type Staging struct {
	dir string
	now func() time.Time

	mu      sync.Mutex
	entries map[string]*stagingEntry
	// creating holds the temp file names of Create calls in progress, which
	// Sweep must not treat as stray.
	creating map[string]struct{}
}

type stagingEntry struct {
	owner     string
	path      string
	size      int64
	touched   time.Time
	committed bool
	// hash accumulates an upload; nil once committed.
	hash hash.Hash
}

// NewStaging returns a staging area in dir, creating it with mode 0700 and
// deleting any files a previous run left there. now defaults to time.Now.
func NewStaging(dir string, now func() time.Time) *Staging {
	if now == nil {
		now = time.Now
	}
	s := &Staging{dir: dir, now: now, entries: map[string]*stagingEntry{}, creating: map[string]struct{}{}}
	if s.ensureDir() == nil {
		s.removeStray()
	}
	return s
}

func (s *Staging) ensureDir() error {
	if err := os.MkdirAll(s.dir, stagingDirMode); err != nil {
		return fmt.Errorf("创建备份暂存目录: %w", err)
	}
	if err := os.Chmod(s.dir, stagingDirMode); err != nil {
		return fmt.Errorf("设置备份暂存目录权限: %w", err)
	}
	return nil
}

func newStagingToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("生成暂存令牌: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// sizeLimitWriter fails once more than MaxBackupBytes would be written.
type sizeLimitWriter struct {
	w        io.Writer
	n        int64
	exceeded bool
}

func (l *sizeLimitWriter) Write(p []byte) (int, error) {
	if l.n+int64(len(p)) > MaxBackupBytes {
		l.exceeded = true
		return 0, ErrBackupTooLarge
	}
	n, err := l.w.Write(p)
	l.n += int64(n)
	return n, err
}

// Create stages the bytes write produces for owner and returns the token,
// size and lowercase hex SHA-256. The file is written under a temporary name
// and renamed into place only when write succeeds; on any error, or when
// more than MaxBackupBytes is written, nothing is kept.
func (s *Staging) Create(owner string, write func(io.Writer) error) (token string, size int64, sha string, err error) {
	if err := s.ensureDir(); err != nil {
		return "", 0, "", err
	}
	token, err = newStagingToken()
	if err != nil {
		return "", 0, "", err
	}
	// The temp file is created and registered under s.mu so a concurrent
	// Sweep never sees it as stray.
	s.mu.Lock()
	file, err := os.CreateTemp(s.dir, stagingTempGlob)
	if err != nil {
		s.mu.Unlock()
		return "", 0, "", fmt.Errorf("创建暂存文件: %w", err)
	}
	tempPath := file.Name()
	tempName := filepath.Base(tempPath)
	s.creating[tempName] = struct{}{}
	s.mu.Unlock()
	defer func() {
		if err != nil {
			_ = file.Close()
			_ = os.Remove(tempPath)
			s.mu.Lock()
			delete(s.creating, tempName)
			s.mu.Unlock()
		}
	}()

	if err = file.Chmod(stagingFileMode); err != nil {
		return "", 0, "", fmt.Errorf("设置暂存文件权限: %w", err)
	}
	digest := sha256.New()
	limited := &sizeLimitWriter{w: io.MultiWriter(file, digest)}
	writeErr := write(limited)
	if limited.exceeded {
		err = ErrBackupTooLarge
		return "", 0, "", err
	}
	if writeErr != nil {
		err = writeErr
		return "", 0, "", err
	}
	if err = file.Sync(); err != nil {
		return "", 0, "", fmt.Errorf("写入暂存文件: %w", err)
	}
	if err = file.Close(); err != nil {
		return "", 0, "", fmt.Errorf("写入暂存文件: %w", err)
	}
	finalPath := filepath.Join(s.dir, token)
	s.mu.Lock()
	if err = os.Rename(tempPath, finalPath); err != nil {
		s.mu.Unlock()
		return "", 0, "", fmt.Errorf("保存暂存文件: %w", err)
	}
	delete(s.creating, tempName)
	s.entries[token] = &stagingEntry{owner: owner, path: finalPath, size: limited.n, touched: s.now(), committed: true}
	s.mu.Unlock()
	return token, limited.n, hex.EncodeToString(digest.Sum(nil)), nil
}

// lookup returns the entry for token if owner owns it. Callers hold s.mu.
func (s *Staging) lookup(owner, token string) (*stagingEntry, bool) {
	entry, ok := s.entries[token]
	if !ok || entry.owner != owner {
		return nil, false
	}
	return entry, true
}

// committed returns the committed entry's path and size. touch keeps the
// entry alive for another StagingTTL (a read in progress, e.g. a slow
// upload to several targets).
func (s *Staging) committed(owner, token string, touch bool) (string, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.lookup(owner, token)
	if !ok || !entry.committed {
		return "", 0, ErrStagingNotFound
	}
	if touch {
		entry.touched = s.now()
	}
	return entry.path, entry.size, nil
}

// Read returns up to max bytes (capped at MaxChunk) of a committed entry
// starting at offset. An offset at or beyond the end returns an empty slice.
func (s *Staging) Read(owner, token string, offset int64, max int) ([]byte, error) {
	path, size, err := s.committed(owner, token, true)
	if err != nil {
		return nil, err
	}
	if offset < 0 || max < 0 {
		return nil, ErrStagingRange
	}
	if max > MaxChunk {
		max = MaxChunk
	}
	if offset >= size || max == 0 {
		return []byte{}, nil
	}
	if remaining := size - offset; int64(max) > remaining {
		max = int(remaining)
	}
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrStagingNotFound
		}
		return nil, fmt.Errorf("读取暂存文件: %w", err)
	}
	defer file.Close()
	buf := make([]byte, max)
	n, err := file.ReadAt(buf, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("读取暂存文件: %w", err)
	}
	return buf[:n], nil
}

// Begin starts an upload for owner and returns its token. The upload is not
// visible to Path or Read until Commit succeeds.
func (s *Staging) Begin(owner string) (string, error) {
	if err := s.ensureDir(); err != nil {
		return "", err
	}
	token, err := newStagingToken()
	if err != nil {
		return "", err
	}
	path := filepath.Join(s.dir, token)
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, stagingFileMode)
	if err != nil {
		return "", fmt.Errorf("创建暂存文件: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("创建暂存文件: %w", err)
	}
	s.entries[token] = &stagingEntry{owner: owner, path: path, touched: s.now(), hash: sha256.New()}
	return token, nil
}

// Append writes data at offset, which must equal the bytes received so far,
// and returns the new total. Chunks over MaxChunk are refused; an upload
// that would exceed MaxBackupBytes, or whose write fails, is discarded.
func (s *Staging) Append(owner, token string, offset int64, data []byte) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.lookup(owner, token)
	if !ok {
		return 0, ErrStagingNotFound
	}
	if entry.committed {
		return 0, errors.New("备份上传已完成")
	}
	if len(data) > MaxChunk {
		return 0, ErrStagingChunk
	}
	if offset != entry.size {
		return 0, ErrStagingOffset
	}
	if entry.size+int64(len(data)) > MaxBackupBytes {
		s.removeLocked(token, entry)
		return 0, ErrBackupTooLarge
	}
	if err := appendFile(entry.path, data); err != nil {
		s.removeLocked(token, entry)
		return 0, err
	}
	entry.hash.Write(data)
	entry.size += int64(len(data))
	entry.touched = s.now()
	return entry.size, nil
}

func appendFile(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, stagingFileMode)
	if err != nil {
		return fmt.Errorf("写入暂存文件: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("写入暂存文件: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("写入暂存文件: %w", err)
	}
	return nil
}

// Commit finishes an upload. sha is the expected lowercase hex SHA-256 of
// the whole file; on mismatch the upload is deleted and ErrStagingChecksum
// returned.
func (s *Staging) Commit(owner, token string, sha string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.lookup(owner, token)
	if !ok || entry.committed {
		return ErrStagingNotFound
	}
	got := hex.EncodeToString(entry.hash.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(got), []byte(strings.ToLower(sha))) != 1 {
		s.removeLocked(token, entry)
		return ErrStagingChecksum
	}
	entry.committed = true
	entry.hash = nil
	entry.touched = s.now()
	return nil
}

// Path returns the file of a committed entry owned by owner.
func (s *Staging) Path(owner, token string) (string, error) {
	path, _, err := s.committed(owner, token, false)
	return path, err
}

// Release deletes owner's entry, committed or not. Unknown tokens and
// entries of other owners are ignored.
func (s *Staging) Release(owner, token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry, ok := s.lookup(owner, token); ok {
		s.removeLocked(token, entry)
	}
}

// Sweep deletes entries untouched for longer than StagingTTL and any file in
// the directory that no entry or in-progress Create accounts for.
func (s *Staging) Sweep() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for token, entry := range s.entries {
		if now.Sub(entry.touched) > StagingTTL {
			s.removeLocked(token, entry)
		}
	}
	s.removeStrayLocked()
}

func (s *Staging) removeLocked(token string, entry *stagingEntry) {
	delete(s.entries, token)
	_ = os.Remove(entry.path)
}

func (s *Staging) removeStray() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeStrayLocked()
}

// removeStrayLocked deletes non-directory files in dir that are neither a
// tracked entry nor an in-progress Create. Callers hold s.mu.
func (s *Staging) removeStrayLocked() {
	items, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, item := range items {
		name := item.Name()
		if item.IsDir() {
			continue
		}
		if _, ok := s.entries[name]; ok {
			continue
		}
		if _, ok := s.creating[name]; ok {
			continue
		}
		_ = os.Remove(filepath.Join(s.dir, name))
	}
}
