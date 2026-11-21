package sftp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// sessionFileClient is a remoteClient fake whose entries are keyed by path.
type sessionFileClient struct {
	remoteClient
	infos  map[string]previewInfo
	data   map[string][]byte
	opened []string
	closed bool
}

func (c *sessionFileClient) Lstat(p string) (os.FileInfo, error) {
	info, ok := c.infos[p]
	if !ok {
		return nil, &os.PathError{Op: "lstat", Path: p, Err: os.ErrNotExist}
	}
	return info, nil
}

func (c *sessionFileClient) Open(p string) (remoteFile, error) {
	c.opened = append(c.opened, p)
	return &sessionFileFake{reader: bytes.NewReader(c.data[p])}, nil
}

func (c *sessionFileClient) Close() error { c.closed = true; return nil }

type sessionFileFake struct {
	remoteFile
	reader *bytes.Reader
	closed bool
}

func (f *sessionFileFake) Read(p []byte) (int, error) { return f.reader.Read(p) }
func (f *sessionFileFake) Close() error               { f.closed = true; return nil }

// useFakeSession registers client as session id and removes it afterwards.
func useFakeSession(t *testing.T, id string, client *sessionFileClient, exclusive bool) *sftpState {
	t.Helper()
	state := &sftpState{client: client, exclusive: exclusive}
	sessionsMu.Lock()
	sessions[id] = state
	sessionsMu.Unlock()
	t.Cleanup(func() {
		sessionsMu.Lock()
		delete(sessions, id)
		sessionsMu.Unlock()
	})
	return state
}

func newSessionFileClient() *sessionFileClient {
	return &sessionFileClient{
		infos: map[string]previewInfo{
			"/var/log/app.log": {size: 5},
			"/var/log":         {mode: os.ModeDir | 0o755},
			"/var/log/link":    {mode: os.ModeSymlink | 0o777},
			"/var/log/fifo":    {mode: os.ModeNamedPipe},
		},
		data: map[string][]byte{"/var/log/app.log": []byte("hello")},
	}
}

func TestOpenSessionFileReadsRegularFile(t *testing.T) {
	client := newSessionFileClient()
	state := useFakeSession(t, "session-file-ok", client, true)
	reader, size, release, err := OpenSessionFile("session-file-ok", "/var/log/app.log")
	if err != nil {
		t.Fatal(err)
	}
	if size != 5 {
		t.Fatalf("size = %d", size)
	}
	// An exclusive session stays held until release.
	if state.mu.TryLock() {
		t.Fatal("exclusive session not held while the file is open")
	}
	data, err := io.ReadAll(reader)
	if err != nil || string(data) != "hello" {
		t.Fatalf("read %q %v", data, err)
	}
	release()
	release() // idempotent
	if !state.mu.TryLock() {
		t.Fatal("session still held after release")
	}
	state.mu.Unlock()
	if _, err := reader.Read(make([]byte, 1)); err == nil {
		t.Fatal("read after release succeeded")
	}
	openSessionFiles.mu.Lock()
	open := len(openSessionFiles.files)
	openSessionFiles.mu.Unlock()
	if open != 0 {
		t.Fatalf("open session files = %d after release", open)
	}
}

func TestOpenSessionFileRejectsSymlinkAndDirectory(t *testing.T) {
	client := newSessionFileClient()
	state := useFakeSession(t, "session-file-kinds", client, false)
	for remotePath, want := range map[string]error{
		"/var/log":         ErrRemoteNotRegular,
		"/var/log/link":    ErrRemoteNotRegular,
		"/var/log/fifo":    ErrRemoteNotRegular,
		"/var/log/missing": ErrRemoteFileNotFound,
		"var/log/app.log":  ErrRemotePathInvalid,
		"/":                ErrRemotePathInvalid,
		"":                 ErrRemotePathInvalid,
		"/a\x00b":          ErrRemotePathInvalid,
	} {
		reader, _, release, err := OpenSessionFile("session-file-kinds", remotePath)
		if !errors.Is(err, want) || reader != nil || release != nil {
			t.Fatalf("%q: err = %v, want %v", remotePath, err, want)
		}
	}
	if len(client.opened) != 0 {
		t.Fatalf("opened %v", client.opened)
	}
	// Every refusal gave the session back.
	if !state.mu.TryLock() {
		t.Fatal("session left held")
	}
	state.mu.Unlock()
}

func TestOpenSessionFileUnknownSession(t *testing.T) {
	_, _, _, err := OpenSessionFile("session-file-missing", "/var/log/app.log")
	if !errors.Is(err, ErrSessionUnavailable) {
		t.Fatalf("err = %v", err)
	}
	// A session whose client is gone (disconnecting) is unavailable too.
	state := useFakeSession(t, "session-file-closed", newSessionFileClient(), false)
	state.client = nil
	_, _, _, err = OpenSessionFile("session-file-closed", "/var/log/app.log")
	if !errors.Is(err, ErrSessionUnavailable) {
		t.Fatalf("closed session err = %v", err)
	}
}

func TestOpenSessionFileErrorsHideEndpoints(t *testing.T) {
	for _, err := range []error{ErrSessionUnavailable, ErrRemotePathInvalid, ErrRemoteFileNotFound, ErrRemoteNotRegular, ErrRemoteReadFailed} {
		if strings.Contains(err.Error(), "/") {
			t.Fatalf("error mentions a path: %v", err)
		}
	}
}

func TestOpenSessionFileContextWaitsForBusySessionAndCancels(t *testing.T) {
	client := newSessionFileClient()
	state := useFakeSession(t, "session-file-busy", client, true)
	// A transfer holds the exclusive FTP session.
	state.mu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	returned := make(chan error, 1)
	go func() {
		_, _, _, err := OpenSessionFileContext(ctx, "session-file-busy", "/var/log/app.log")
		returned <- err
	}()
	select {
	case err := <-returned:
		t.Fatalf("open returned while the session was busy: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-returned:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not end the wait")
	}
	// The abandoned open completes once the transfer ends and gives the
	// session back at once: nothing stays held or registered.
	state.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for {
		openSessionFiles.mu.Lock()
		open := len(openSessionFiles.files)
		openSessionFiles.mu.Unlock()
		opened := func() int {
			state.mu.RLock()
			defer state.mu.RUnlock()
			return len(client.opened)
		}()
		if opened == 1 && open == 0 && state.mu.TryLock() {
			state.mu.Unlock()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("abandoned open leaked: opened=%d registered=%d", opened, open)
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Not cancelled: the open waits for the session and then succeeds.
	state.mu.Lock()
	go func() {
		time.Sleep(10 * time.Millisecond)
		state.mu.Unlock()
	}()
	reader, size, release, err := OpenSessionFileContext(context.Background(), "session-file-busy", "/var/log/app.log")
	if err != nil || size != 5 {
		t.Fatalf("open after wait: %v size %d", err, size)
	}
	data, _ := io.ReadAll(reader)
	release()
	if string(data) != "hello" {
		t.Fatalf("data = %q", data)
	}
}

func TestOpenSessionFileAbortedOnDisconnect(t *testing.T) {
	client := newSessionFileClient()
	useFakeSession(t, "session-file-abort", client, false)
	reader, _, release, err := OpenSessionFile("session-file-abort", "/var/log/app.log")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	abortSessionFiles("other-session")
	if _, err := reader.Read(make([]byte, 1)); err != nil {
		t.Fatalf("another session's disconnect aborted the read: %v", err)
	}
	abortSessionFiles("session-file-abort")
	if _, err := reader.Read(make([]byte, 1)); !errors.Is(err, ErrSessionUnavailable) {
		t.Fatalf("read after disconnect err = %v", err)
	}
}
