package terminal

import (
	"net"
	"os"
	"strings"
	"time"

	"mincode/app/backend/core"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

const sshAgentStatusDialTimeout = 2 * time.Second

// SSHAgentIdentity is safe-to-display metadata for a public identity currently
// exposed by the system SSH agent. It intentionally excludes agent comments,
// which may contain a local account name, host, or file path.
type SSHAgentIdentity struct {
	Algorithm   string `json:"algorithm"`
	Fingerprint string `json:"fingerprint"`
}

// SSHAgentStatus describes whether this desktop process can use the system SSH
// agent. State is one of ready, empty, or unavailable.
type SSHAgentStatus struct {
	State         string             `json:"state"`
	IdentityCount int                `json:"identityCount"`
	Identities    []SSHAgentIdentity `json:"identities"`
}

func init() {
	core.Register("get_ssh_agent_status", cmdGetSSHAgentStatus)
}

func cmdGetSSHAgentStatus(_, _ string) (string, error) {
	return core.EncodeResult(inspectSSHAgent(os.Getenv("SSH_AUTH_SOCK"), net.DialTimeout))
}

func inspectSSHAgent(socket string, dial func(network, address string, timeout time.Duration) (net.Conn, error)) SSHAgentStatus {
	socket = strings.TrimSpace(socket)
	if socket == "" {
		return SSHAgentStatus{State: "unavailable", Identities: []SSHAgentIdentity{}}
	}

	conn, err := dial("unix", socket, sshAgentStatusDialTimeout)
	if err != nil {
		return SSHAgentStatus{State: "unavailable", Identities: []SSHAgentIdentity{}}
	}
	defer conn.Close()

	keys, err := agent.NewClient(conn).List()
	if err != nil {
		return SSHAgentStatus{State: "unavailable", Identities: []SSHAgentIdentity{}}
	}

	identities := make([]SSHAgentIdentity, 0, min(len(keys), 8))
	for _, key := range keys[:min(len(keys), 8)] {
		publicKey, err := ssh.ParsePublicKey(key.Blob)
		if err != nil {
			continue
		}
		identities = append(identities, SSHAgentIdentity{
			Algorithm:   publicKey.Type(),
			Fingerprint: ssh.FingerprintSHA256(publicKey),
		})
	}
	if len(keys) == 0 {
		return SSHAgentStatus{State: "empty", Identities: identities}
	}
	return SSHAgentStatus{
		State:         "ready",
		IdentityCount: len(keys),
		Identities:    identities,
	}
}
