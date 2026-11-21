package backup

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newTestStaging(t *testing.T) (*Staging, *fakeClock, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "backup-staging")
	clock := &fakeClock{t: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)}
	return NewStaging(dir, clock.now), clock, dir
}

func hexSHA(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func stagedFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestStagingCreateThenChunkedRead(t *testing.T) {
	s, _, dir := newTestStaging(t)
	payload := bytes.Repeat([]byte("0123456789abcdef"), (MaxChunk*2+100)/16+1)
	token, size, sha, err := s.Create("plugin.a", func(w io.Writer) error {
		_, err := w.Write(payload)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(token) {
		t.Fatalf("token %q is not 32 lowercase hex characters", token)
	}
	if size != int64(len(payload)) || sha != hexSHA(payload) {
		t.Fatalf("size/sha = %d %s, want %d %s", size, sha, len(payload), hexSHA(payload))
	}
	var got []byte
	for {
		chunk, err := s.Read("plugin.a", token, int64(len(got)), MaxChunk*4)
		if err != nil {
			t.Fatal(err)
		}
		if len(chunk) > MaxChunk {
			t.Fatalf("chunk of %d bytes exceeds MaxChunk", len(chunk))
		}
		if len(chunk) == 0 {
			break
		}
		got = append(got, chunk...)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("chunked read does not match payload")
	}
	beyond, err := s.Read("plugin.a", token, size+10, 10)
	if err != nil || len(beyond) != 0 {
		t.Fatalf("read beyond end = %d bytes, %v", len(beyond), err)
	}
	path, err := s.Path("plugin.a", token)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("staged file mode %v, want 0600", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("staging dir mode %v, want 0700", dirInfo.Mode().Perm())
	}
	if names := stagedFiles(t, dir); len(names) != 1 {
		t.Fatalf("staging dir holds %v, want only the staged file", names)
	}
	s.Release("plugin.a", token)
	if _, err := s.Read("plugin.a", token, 0, 10); !errors.Is(err, ErrStagingNotFound) {
		t.Fatalf("read after release = %v", err)
	}
	if names := stagedFiles(t, dir); len(names) != 0 {
		t.Fatalf("release left %v", names)
	}
}

func TestStagingOtherOwnerCannotSeeEntry(t *testing.T) {
	s, _, _ := newTestStaging(t)
	token, _, _, err := s.Create("plugin.a", func(w io.Writer) error {
		_, err := w.Write([]byte("secret backup"))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read("plugin.b", token, 0, 10); !errors.Is(err, ErrStagingNotFound) {
		t.Fatalf("other owner Read = %v", err)
	}
	if _, err := s.Path("plugin.b", token); !errors.Is(err, ErrStagingNotFound) {
		t.Fatalf("other owner Path = %v", err)
	}
	if ErrStagingNotFound.Error() != "暂存的备份不存在或已过期" {
		t.Fatalf("ErrStagingNotFound text %q", ErrStagingNotFound.Error())
	}
	s.Release("plugin.b", token)
	if _, err := s.Path("plugin.a", token); err != nil {
		t.Fatalf("other owner Release removed the entry: %v", err)
	}
	if _, err := s.Read("plugin.a", "not-a-token", 0, 10); !errors.Is(err, ErrStagingNotFound) {
		t.Fatalf("unknown token Read = %v", err)
	}
}

func TestStagingCreateFailureRemovesFile(t *testing.T) {
	s, _, dir := newTestStaging(t)
	boom := errors.New("boom")
	if _, _, _, err := s.Create("plugin.a", func(w io.Writer) error {
		_, _ = w.Write([]byte("partial"))
		return boom
	}); !errors.Is(err, boom) {
		t.Fatalf("Create error = %v, want boom", err)
	}
	if names := stagedFiles(t, dir); len(names) != 0 {
		t.Fatalf("failed Create left %v", names)
	}
}

func TestStagingCreateRejectsOversizedBackup(t *testing.T) {
	s, _, dir := newTestStaging(t)
	chunk := make([]byte, 1<<20)
	_, _, _, err := s.Create("plugin.a", func(w io.Writer) error {
		for written := 0; written <= MaxBackupBytes; written += len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				return err
			}
		}
		return nil
	})
	if !errors.Is(err, ErrBackupTooLarge) {
		t.Fatalf("oversized Create = %v, want ErrBackupTooLarge", err)
	}
	if names := stagedFiles(t, dir); len(names) != 0 {
		t.Fatalf("oversized Create left %v", names)
	}
}

func TestStagingUploadAppendCommit(t *testing.T) {
	s, _, _ := newTestStaging(t)
	payload := []byte(strings.Repeat("backup-bytes-", 1000))
	token, err := s.Begin("plugin.a")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(token) {
		t.Fatalf("token %q is not 32 lowercase hex characters", token)
	}
	half := len(payload) / 2
	n, err := s.Append("plugin.a", token, 0, payload[:half])
	if err != nil || n != int64(half) {
		t.Fatalf("first Append = %d, %v", n, err)
	}
	if _, err := s.Path("plugin.a", token); !errors.Is(err, ErrStagingNotFound) {
		t.Fatalf("uncommitted Path = %v", err)
	}
	if _, err := s.Read("plugin.a", token, 0, 10); !errors.Is(err, ErrStagingNotFound) {
		t.Fatalf("uncommitted Read = %v", err)
	}
	if _, err := s.Append("plugin.a", token, int64(half)+1, payload[half:]); err == nil {
		t.Fatal("Append with a gap was accepted")
	}
	if _, err := s.Append("plugin.a", token, 0, payload[half:]); err == nil {
		t.Fatal("Append rewinding the offset was accepted")
	}
	if _, err := s.Append("plugin.b", token, int64(half), payload[half:]); !errors.Is(err, ErrStagingNotFound) {
		t.Fatalf("other owner Append = %v", err)
	}
	if _, err := s.Append("plugin.a", token, int64(half), make([]byte, MaxChunk+1)); err == nil {
		t.Fatal("oversized chunk was accepted")
	}
	n, err = s.Append("plugin.a", token, int64(half), payload[half:])
	if err != nil || n != int64(len(payload)) {
		t.Fatalf("second Append = %d, %v", n, err)
	}
	if err := s.Commit("plugin.b", token, hexSHA(payload)); !errors.Is(err, ErrStagingNotFound) {
		t.Fatalf("other owner Commit = %v", err)
	}
	if err := s.Commit("plugin.a", token, hexSHA(payload)); err != nil {
		t.Fatal(err)
	}
	path, err := s.Path("plugin.a", token)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(stored, payload) {
		t.Fatalf("committed file mismatch (%v)", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("uploaded file mode %v (%v), want 0600", info.Mode().Perm(), err)
	}
	if _, err := s.Append("plugin.a", token, int64(len(payload)), []byte("x")); err == nil {
		t.Fatal("Append after Commit was accepted")
	}
}

func TestStagingCommitChecksumMismatchDeletes(t *testing.T) {
	s, _, dir := newTestStaging(t)
	token, err := s.Begin("plugin.a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append("plugin.a", token, 0, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := s.Commit("plugin.a", token, hexSHA([]byte("other"))); !errors.Is(err, ErrStagingChecksum) {
		t.Fatalf("Commit = %v, want ErrStagingChecksum", err)
	}
	if ErrStagingChecksum.Error() != "备份文件校验失败" {
		t.Fatalf("ErrStagingChecksum text %q", ErrStagingChecksum.Error())
	}
	if err := s.Commit("plugin.a", token, hexSHA([]byte("hello"))); !errors.Is(err, ErrStagingNotFound) {
		t.Fatalf("Commit after mismatch = %v, want ErrStagingNotFound", err)
	}
	if names := stagedFiles(t, dir); len(names) != 0 {
		t.Fatalf("checksum mismatch left %v", names)
	}
}

func TestStagingAppendRejectsOversizedUpload(t *testing.T) {
	s, _, dir := newTestStaging(t)
	token, err := s.Begin("plugin.a")
	if err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, MaxChunk)
	var offset int64
	var appendErr error
	for offset <= MaxBackupBytes {
		var n int64
		n, appendErr = s.Append("plugin.a", token, offset, chunk)
		if appendErr != nil {
			break
		}
		offset = n
	}
	if !errors.Is(appendErr, ErrBackupTooLarge) {
		t.Fatalf("oversized upload = %v at %d, want ErrBackupTooLarge", appendErr, offset)
	}
	if offset != MaxBackupBytes {
		t.Fatalf("upload stopped at %d, want exactly MaxBackupBytes", offset)
	}
	if names := stagedFiles(t, dir); len(names) != 0 {
		t.Fatalf("oversized upload left %v", names)
	}
}

func TestStagingSweepRemovesExpiredEntries(t *testing.T) {
	s, clock, dir := newTestStaging(t)
	write := func(w io.Writer) error {
		_, err := w.Write([]byte("data"))
		return err
	}
	old, _, _, err := s.Create("plugin.a", write)
	if err != nil {
		t.Fatal(err)
	}
	oldUpload, err := s.Begin("plugin.a")
	if err != nil {
		t.Fatal(err)
	}
	clock.advance(StagingTTL - time.Minute)
	fresh, _, _, err := s.Create("plugin.a", write)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stray"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	clock.advance(2 * time.Minute)
	s.Sweep()
	if _, err := s.Path("plugin.a", old); !errors.Is(err, ErrStagingNotFound) {
		t.Fatalf("expired entry survived Sweep: %v", err)
	}
	if _, err := s.Append("plugin.a", oldUpload, 0, []byte("x")); !errors.Is(err, ErrStagingNotFound) {
		t.Fatalf("expired upload survived Sweep: %v", err)
	}
	if _, err := s.Path("plugin.a", fresh); err != nil {
		t.Fatalf("fresh entry removed by Sweep: %v", err)
	}
	if names := stagedFiles(t, dir); len(names) != 1 || names[0] != fresh {
		t.Fatalf("after Sweep the dir holds %v, want only %s", names, fresh)
	}
}

func TestNewStagingRemovesLeftovers(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "backup-staging")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "0123456789abcdef0123456789abcdef"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewStaging(dir, nil)
	if names := stagedFiles(t, dir); len(names) != 0 {
		t.Fatalf("NewStaging left %v", names)
	}
	token, _, _, err := s.Create("plugin.a", func(w io.Writer) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Path("plugin.a", token); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("staging dir mode %v (%v), want 0700", info.Mode().Perm(), err)
	}
}

func TestStagingReadKeepsEntryAlive(t *testing.T) {
	s, clock, _ := newTestStaging(t)
	token, _, _, err := s.Create("plugin.a", func(w io.Writer) error {
		_, err := w.Write([]byte("data"))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	// A slow multi-target upload keeps reading past the TTL.
	for i := 0; i < 3; i++ {
		clock.advance(StagingTTL - time.Minute)
		if _, err := s.Read("plugin.a", token, 0, 4); err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		s.Sweep()
	}
	if _, err := s.Path("plugin.a", token); err != nil {
		t.Fatalf("entry being read was swept: %v", err)
	}
}
