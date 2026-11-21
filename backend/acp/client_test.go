package acp

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"
)

func TestBridgeClientTextFilesStayWithinWorkspace(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	client := newBridgeClient("test")
	client.addRoot(root)

	insidePath := filepath.Join(root, "nested", "note.txt")
	if _, err := client.WriteTextFile(context.Background(), acpsdk.WriteTextFileRequest{
		Path:    insidePath,
		Content: "first\nsecond\nthird",
	}); err != nil {
		t.Fatalf("write workspace file: %v", err)
	}
	line, limit := 2, 1
	response, err := client.ReadTextFile(context.Background(), acpsdk.ReadTextFileRequest{
		Path:  insidePath,
		Line:  &line,
		Limit: &limit,
	})
	if err != nil {
		t.Fatalf("read workspace file: %v", err)
	}
	if response.Content != "second" {
		t.Fatalf("unexpected paged content: %q", response.Content)
	}

	outsidePath := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsidePath, []byte("secret"), 0o600); err != nil {
		t.Fatalf("prepare outside file: %v", err)
	}
	if _, err := client.ReadTextFile(context.Background(), acpsdk.ReadTextFileRequest{Path: outsidePath}); err == nil {
		t.Fatal("expected outside read to be rejected")
	}
	if _, err := client.WriteTextFile(context.Background(), acpsdk.WriteTextFileRequest{Path: outsidePath, Content: "overwrite"}); err == nil {
		t.Fatal("expected outside write to be rejected")
	}
}

func TestBridgeClientRejectsSymlinkAndOversizedFiles(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	client := newBridgeClient("test")
	client.addRoot(root)

	oversizedPath := filepath.Join(root, "oversized.txt")
	oversized := strings.Repeat("x", int(maxACPTextFileBytes)+1)
	if _, err := client.WriteTextFile(context.Background(), acpsdk.WriteTextFileRequest{
		Path: oversizedPath, Content: oversized,
	}); err == nil {
		t.Fatal("expected oversized write to be rejected")
	}
	if err := os.WriteFile(oversizedPath, []byte(oversized), 0o600); err != nil {
		t.Fatalf("prepare oversized file: %v", err)
	}
	if _, err := client.ReadTextFile(context.Background(), acpsdk.ReadTextFileRequest{Path: oversizedPath}); err == nil {
		t.Fatal("expected oversized read to be rejected")
	}

	if runtime.GOOS == "windows" {
		t.Skip("symbolic link creation is not generally available on Windows CI")
	}
	outsidePath := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsidePath, []byte("secret"), 0o600); err != nil {
		t.Fatalf("prepare symlink target: %v", err)
	}
	linkPath := filepath.Join(root, "linked.txt")
	if err := os.Symlink(outsidePath, linkPath); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	if _, err := client.ReadTextFile(context.Background(), acpsdk.ReadTextFileRequest{Path: linkPath}); err == nil {
		t.Fatal("expected symlink read to be rejected")
	}
	if _, err := client.WriteTextFile(context.Background(), acpsdk.WriteTextFileRequest{Path: linkPath, Content: "overwrite"}); err == nil {
		t.Fatal("expected symlink write to be rejected")
	}
}

func TestTerminalWriterKeepsBoundedUTF8Tail(t *testing.T) {
	terminal := &acpTerminal{limit: 5}
	terminal.appendOutput([]byte("ab世界"))
	output, truncated, _ := terminal.snapshot()
	if !truncated {
		t.Fatal("expected terminal output to be truncated")
	}
	if output != "界" {
		t.Fatalf("unexpected UTF-8 tail: %q", output)
	}
}

func TestAcpGatewayStaleAttemptCannotClearCurrentConnecting(t *testing.T) {
	gateway := newAcpGateway()
	firstRevision, firstCtx, _ := gateway.beginConnect(context.Background(), "test")
	secondRevision, secondCtx, _ := gateway.beginConnect(context.Background(), "test")

	select {
	case <-firstCtx.Done():
	default:
		t.Fatal("expected newer connection attempt to cancel the previous attempt")
	}
	gateway.failConnect("test", firstRevision)
	if len(gateway.connecting) != 1 {
		t.Fatal("stale failure cleared the current connection attempt")
	}
	select {
	case <-secondCtx.Done():
		t.Fatal("stale failure cancelled the current connection attempt")
	default:
	}

	if err := gateway.finishConnect("test", firstRevision, &acpConnection{}); err == nil {
		t.Fatal("expected stale connection attempt to be rejected")
	}
	if len(gateway.connecting) != 1 {
		t.Fatal("stale completion cleared the current connection attempt")
	}

	gateway.failConnect("test", secondRevision)
	if len(gateway.connecting) != 0 {
		t.Fatal("current failed connection attempt was not cleared")
	}
	select {
	case <-secondCtx.Done():
	default:
		t.Fatal("current failed connection attempt was not cancelled")
	}
}
