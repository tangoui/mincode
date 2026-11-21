package terminal

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh/agent"
)

func TestInspectSSHAgent(t *testing.T) {
	t.Run("reports unavailable without a socket", func(t *testing.T) {
		status := inspectSSHAgent("", net.DialTimeout)
		if status.State != "unavailable" {
			t.Fatalf("state = %q, want unavailable", status.State)
		}
		if status.IdentityCount != 0 || len(status.Identities) != 0 {
			t.Fatalf("unexpected identities: %#v", status)
		}
	})

	t.Run("lists safe identity metadata", func(t *testing.T) {
		dir, err := os.MkdirTemp("/tmp", "mincode-ssh-agent-")
		if err != nil {
			t.Fatalf("create temporary socket directory: %v", err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		socket := filepath.Join(dir, "agent.sock")
		listener, err := net.Listen("unix", socket)
		if err != nil {
			t.Fatalf("listen unix socket: %v", err)
		}
		t.Cleanup(func() { _ = listener.Close() })

		keyring := agent.NewKeyring()
		_, privateKey, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatalf("generate ed25519 key: %v", err)
		}
		if err := keyring.Add(agent.AddedKey{PrivateKey: privateKey, Comment: "person@laptop"}); err != nil {
			t.Fatalf("add agent key: %v", err)
		}
		go func() {
			conn, acceptErr := listener.Accept()
			if acceptErr == nil {
				_ = agent.ServeAgent(keyring, conn)
				_ = conn.Close()
			}
		}()

		status := inspectSSHAgent(socket, net.DialTimeout)
		if status.State != "ready" || status.IdentityCount != 1 {
			t.Fatalf("unexpected status: %#v", status)
		}
		if len(status.Identities) != 1 {
			t.Fatalf("identity metadata = %#v, want one identity", status.Identities)
		}
		identity := status.Identities[0]
		if identity.Algorithm != "ssh-ed25519" || identity.Fingerprint == "" {
			t.Fatalf("unexpected identity: %#v", identity)
		}
	})
}
