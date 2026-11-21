package sftp

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateSftpTransferPayload(t *testing.T) {
	valid := sftpTransferStartPayload{
		JobID:      "sftpx-1.2:3",
		SessionID:  "session-1",
		Direction:  "upload",
		LocalPath:  "/tmp/source.txt",
		RemotePath: "/srv/source.txt",
	}
	if err := validateSftpTransferPayload(valid); err != nil {
		t.Fatalf("valid payload rejected: %v", err)
	}
	cases := []sftpTransferStartPayload{
		{JobID: "../bad", SessionID: "s", Direction: "upload", LocalPath: "/tmp/a", RemotePath: "/a"},
		{JobID: "job", SessionID: "", Direction: "upload", LocalPath: "/tmp/a", RemotePath: "/a"},
		{JobID: "job", SessionID: "s", Direction: "sideways", LocalPath: "/tmp/a", RemotePath: "/a"},
		{JobID: "job", SessionID: "s", Direction: "download", LocalPath: "", RemotePath: "/a"},
		{JobID: "job", SessionID: "s", Direction: "download", LocalPath: "/tmp/a", RemotePath: ""},
	}
	for index, payload := range cases {
		if err := validateSftpTransferPayload(payload); err == nil {
			t.Fatalf("case %d should be rejected", index)
		}
	}
}

func TestResolveSftpWorkspaceTransferPath(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "src", "file.txt")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}

	expectedSource, err := filepath.EvalSymlinks(source)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveSftpWorkspaceTransferPath(sftpWorkspaceTransferStartPayload{
		ProjectRoot: root, Direction: "upload", WorkspacePath: "src/file.txt",
	})
	if err != nil || resolved != expectedSource {
		t.Fatalf("valid workspace upload rejected: path=%q err=%v", resolved, err)
	}
	for _, workspacePath := range []string{"../outside", "/tmp/file", "C:/outside"} {
		if _, err := resolveSftpWorkspaceTransferPath(sftpWorkspaceTransferStartPayload{
			ProjectRoot: root, Direction: "upload", WorkspacePath: workspacePath,
		}); err == nil {
			t.Fatalf("workspace path %q should be rejected", workspacePath)
		}
	}

	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveSftpWorkspaceTransferPath(sftpWorkspaceTransferStartPayload{
		ProjectRoot: root, Direction: "upload", WorkspacePath: "escape/file.txt",
	}); err == nil {
		t.Fatal("upload through a symlink outside the workspace should be rejected")
	}
	if _, err := resolveSftpWorkspaceTransferPath(sftpWorkspaceTransferStartPayload{
		ProjectRoot: root, Direction: "download", WorkspacePath: "downloads/file.txt",
	}); err == nil {
		t.Fatal("workspace download should remain disabled until descriptor-relative writes are available")
	}
}

func TestCopySftpTransferReportsProgress(t *testing.T) {
	payload := sftpTransferStartPayload{JobID: "job", SessionID: "session", Direction: "download"}
	job := &sftpTransferJob{payload: payload, status: "running"}
	job.start(6, 2)
	var destination bytes.Buffer
	if err := copySftpTransfer(context.Background(), &destination, bytes.NewBufferString("cdef"), 2, job); err != nil {
		t.Fatal(err)
	}
	if destination.String() != "cdef" {
		t.Fatalf("unexpected copied data %q", destination.String())
	}
	snapshot := job.snapshot()
	if snapshot.BytesTransferred != 6 || snapshot.TotalBytes != 6 || snapshot.ResumedFrom != 2 {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
}

func TestCopySftpTransferHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	job := &sftpTransferJob{payload: sftpTransferStartPayload{JobID: "job", SessionID: "session"}}
	err := copySftpTransfer(ctx, &bytes.Buffer{}, bytes.NewBufferString("data"), 0, job)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}
