package terminal

import (
	"errors"
	"net"
	"os"
	"testing"
)

func TestSSHConnectionDiagnosticsEmitsCredentialFreeStages(t *testing.T) {
	var events []sshConnectionProgressPayload
	diagnostics := newSSHConnectionDiagnosticsWithEmitter("session-1", func(name string, data any) {
		if name != sshConnectionProgressEvent {
			t.Fatalf("unexpected event name %q", name)
		}
		payload, ok := data.(sshConnectionProgressPayload)
		if !ok {
			t.Fatalf("unexpected payload type %T", data)
		}
		events = append(events, payload)
	})

	diagnostics.begin(sshConnectionHopTarget, sshConnectionStageTCP)
	diagnostics.succeed(sshConnectionHopTarget, sshConnectionStageTCP)
	diagnostics.begin(sshConnectionHopTarget, sshConnectionStageHandshake)
	diagnostics.failActiveOr(sshConnectionHopTarget, sshConnectionStageHandshake, errors.New("ssh: handshake failed"))

	if len(events) != 4 {
		t.Fatalf("expected 4 progress events, got %d", len(events))
	}
	for _, event := range events {
		if event.SessionID != "session-1" || event.AttemptID == 0 {
			t.Fatalf("invalid event identity: %#v", event)
		}
	}
	failed := events[len(events)-1]
	if failed.State != sshConnectionStateFailed || failed.Stage != sshConnectionStageHandshake {
		t.Fatalf("unexpected failure event: %#v", failed)
	}
	if failed.ErrorCode != "handshake" {
		t.Fatalf("expected stable handshake code, got %q", failed.ErrorCode)
	}
}

func TestClassifySSHConnectionError(t *testing.T) {
	tests := []struct {
		name  string
		stage string
		err   error
		want  string
	}{
		{name: "dns", stage: sshConnectionStageTCP, err: &net.DNSError{Err: "no such host", Name: "invalid.example"}, want: "dns"},
		{name: "key missing", stage: sshConnectionStageAuthentication, err: os.ErrNotExist, want: "key-not-found"},
		{name: "refused", stage: sshConnectionStageTCP, err: errors.New("connect: connection refused"), want: "refused"},
		{name: "host mismatch", stage: sshConnectionStageHostKey, err: errors.New("knownhosts: key mismatch"), want: "host-key-mismatch"},
		{name: "agent", stage: sshConnectionStageAuthentication, err: errors.New("SSH_AUTH_SOCK not set"), want: "agent-unavailable"},
		{name: "auth", stage: sshConnectionStageAuthentication, err: errors.New("ssh: unable to authenticate, attempted methods [none]"), want: "authentication"},
		{name: "shell fallback", stage: sshConnectionStageShell, err: errors.New("channel rejected"), want: "shell"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := classifySSHConnectionError(test.stage, test.err); got != test.want {
				t.Fatalf("classifySSHConnectionError() = %q, want %q", got, test.want)
			}
		})
	}
}
