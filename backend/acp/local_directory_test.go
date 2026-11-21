package acp

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mincode/app/backend/core"

	acpsdk "github.com/coder/acp-go-sdk"
)

func TestCheckLocalAgentDirectory(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		want bool
	}{
		{dir, true},
		{filepath.Join(dir, "missing"), false},
		{file, false},
		{filepath.Join(file, "child"), false},
	} {
		if got, err := checkLocalAgentDirectory(tc.path); got != tc.want || err != nil {
			t.Errorf("checkLocalAgentDirectory(%q) = %v, %v; want %v, nil", tc.path, got, err, tc.want)
		}
	}
	t.Run("permission error is preserved", func(t *testing.T) {
		denied := &os.PathError{Op: "stat", Path: dir, Err: os.ErrPermission}
		stat := func(string) (os.FileInfo, error) { return nil, denied }
		if got, err := checkLocalAgentDirectoryWithStat(dir, stat); got || !errors.Is(err, denied) {
			t.Fatalf("got %v, %v; want false and original permission error", got, err)
		}
	})
}

func TestEnsureLocalAgentDefaultDirectory(t *testing.T) {
	dir := t.TempDir()
	want := filepath.Join(dir, "MinCode", "agent-workspace")
	for range 2 {
		got, err := ensureLocalAgentDefaultDirectory(dir)
		if got != want || err != nil {
			t.Fatalf("got %q, %v; want %q, nil", got, err, want)
		}
		if info, err := os.Stat(got); err != nil || !info.IsDir() {
			t.Fatalf("default directory was not created: %v", err)
		}
	}

	blockedDir := t.TempDir()
	blocked := filepath.Join(blockedDir, "MinCode", "agent-workspace")
	if err := os.MkdirAll(filepath.Dir(blocked), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blocked, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{"", "relative", blockedDir} {
		if got, err := ensureLocalAgentDefaultDirectory(root); got != "" || err == nil {
			t.Errorf("ensureLocalAgentDefaultDirectory(%q) = %q, %v; want empty path and error", root, got, err)
		}
	}
	if content, err := os.ReadFile(blocked); err != nil || string(content) != "keep" {
		t.Fatalf("occupying file changed: %q, %v", content, err)
	}
}

func TestLocalAgentTransportCloseDisconnectsServer(t *testing.T) {
	exited := make(chan AcpDisconnectedPayload, 1)
	core.SetEmitter(func(name string, data any) {
		if name == AcpDisconnectedEvent {
			exited <- data.(AcpDisconnectedPayload)
		}
	})
	t.Cleanup(func() { core.SetEmitter(nil) })
	g := newAcpGateway()
	serverID := "local-agent-transport-close"
	rev, _, _ := g.beginConnect(context.Background(), serverID)
	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = writer.Close() })
	conn := &acpConnection{csc: acpsdk.NewClientSideConnection(nil, io.Discard, reader), serverID: serverID}
	if err := g.finishConnect(serverID, rev, conn); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-exited:
		if len(event.ServerIDs) != 1 || event.ServerIDs[0] != serverID {
			t.Fatalf("unexpected disconnect payload: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("transport closed without a disconnect event")
	}
	if _, err := g.session(serverID); err == nil {
		t.Fatal("closed ACP connection is still registered")
	}
}

func TestEnsureLocalAgentDefaultDirectoryRejectsSymlinks(t *testing.T) {
	outside := t.TempDir()
	for _, name := range []string{"MinCode", filepath.Join("MinCode", "agent-workspace")} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			link := filepath.Join(root, name)
			if err := os.MkdirAll(filepath.Dir(link), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, link); err != nil {
				t.Skipf("symlink creation unavailable: %v", err)
			}
			if path, err := ensureLocalAgentDefaultDirectory(root); err == nil || path != "" {
				t.Fatalf("accepted redirected directory %q: %v", path, err)
			}
			if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
				t.Fatalf("default directory creation changed the symlink target: %v, %v", entries, err)
			}
		})
	}
}
