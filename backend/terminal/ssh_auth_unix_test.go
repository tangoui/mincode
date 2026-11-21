//go:build !windows

package terminal

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTerminalSSHAgentAuthCleanupClosesSocket(t *testing.T) {
	// unix socket 路径有长度上限，使用包目录下的短相对路径而非 t.TempDir()。
	dir, err := os.MkdirTemp(".", "agent")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "a.sock")
	listener, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv("SSH_AUTH_SOCK", sock)

	methods, cleanup, err := terminalSSHAuthMethods("agent", "", "", "", "agent-cleanup-test", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(methods) != 1 || cleanup == nil {
		t.Fatalf("unexpected agent auth result: methods=%d cleanup=%v", len(methods), cleanup != nil)
	}
	server, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	cleanup()
	cleanup()
	_ = server.SetReadDeadline(time.Now().Add(2 * time.Second))
	if n, err := server.Read(make([]byte, 1)); err == nil || n != 0 {
		t.Fatalf("agent socket should be closed by cleanup: n=%d err=%v", n, err)
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("agent socket remained open after cleanup")
	}
}

func TestTerminalSSHAuthMethodsAlwaysReturnCleanup(t *testing.T) {
	cases := []struct {
		authType string
		password string
	}{
		{authType: "password", password: "secret"},
		{authType: "password"},
		{authType: "unsupported"},
	}
	for _, tc := range cases {
		_, cleanup, _ := terminalSSHAuthMethods(tc.authType, tc.password, "", "", "", nil)
		if cleanup == nil {
			t.Fatalf("%s: cleanup must never be nil", tc.authType)
		}
		cleanup()
	}
	t.Setenv("SSH_AUTH_SOCK", "")
	if _, cleanup, err := terminalSSHAuthMethods("agent", "", "", "", "", nil); err == nil || cleanup == nil {
		t.Fatalf("missing agent socket: err=%v cleanup=%v", err, cleanup != nil)
	}
}
