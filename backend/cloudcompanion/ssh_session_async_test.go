package cloudcompanion

import (
	"errors"
	"sync"
	"testing"
	"time"

	"mincode/app/backend/terminal"
)

func sshCreateEvent(requestID string) CloudPayloadEvent {
	return CloudPayloadEvent{
		RequestID:      requestID,
		SourceDeviceID: "mobile-phone-0000000000000001",
		Channel:        "ssh.session.create",
		Payload: map[string]any{
			"sessionId": "cloud-terminal-0123456789abcdef",
			"profileId": "profile-1",
			"cols":      float64(100),
			"rows":      float64(30),
		},
	}
}

// memoryTerminalStore keeps test state out of the real app database.
type memoryTerminalStore struct {
	mu        sync.Mutex
	processed map[string]bool
	bindings  map[string]bool
}

func (s *memoryTerminalStore) operationProcessed(requestID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.processed[requestID], nil
}

func (s *memoryTerminalStore) markOperation(requestID, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.processed[requestID] = true
	return nil
}

func (s *memoryTerminalStore) saveBinding(binding *cloudTerminalBinding) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bindings[binding.SessionID] = true
	return nil
}

func (s *memoryTerminalStore) deleteBinding(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.bindings, sessionID)
}

func newTestTerminalManager(t *testing.T) *cloudTerminalManager {
	t.Helper()
	m := &cloudTerminalManager{
		bindings: map[string]*cloudTerminalBinding{},
		store:    &memoryTerminalStore{processed: map[string]bool{}, bindings: map[string]bool{}},
	}
	t.Cleanup(func() {
		m.mu.Lock()
		bindings := m.bindings
		m.bindings = map[string]*cloudTerminalBinding{}
		m.mu.Unlock()
		for _, binding := range bindings {
			binding.stop()
		}
	})
	return m
}

func TestSSHCreateRunsInBackgroundAndBindsSessionBeforeConnecting(t *testing.T) {
	m := newTestTerminalManager(t)
	release := make(chan struct{})
	started := make(chan struct{})
	var startCalls int
	start := func(profileID, sessionID string, cols, rows uint32) error {
		startCalls++
		// While SSH is still connecting (e.g. waiting for an MFA code) the session must already
		// be bound to the phone, so the prompt can be delivered there.
		if m.bindingFor(sessionID) == nil {
			t.Error("session not bound to the phone before SSH finished connecting")
		}
		close(started)
		<-release
		return nil
	}
	results := make(chan error, 2)
	done := func(err error) { results <- err }

	m.startSSHCreate(sshCreateEvent("req-1"), start, done)
	<-started
	// A replay of the same request while connecting must not start a second connection.
	m.startSSHCreate(sshCreateEvent("req-1"), start, done)
	select {
	case err := <-results:
		t.Fatalf("create must not finish while SSH is still connecting: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-results:
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("create never finished")
	}
	if startCalls != 1 {
		t.Fatalf("expected one SSH start, got %d", startCalls)
	}
	if binding := m.bindingFor("cloud-terminal-0123456789abcdef"); binding == nil ||
		binding.SourceDeviceID != "mobile-phone-0000000000000001" {
		t.Fatal("session should stay bound to the phone after connecting")
	}
}

func TestSSHCreateFailureRemovesBindingAndReportsError(t *testing.T) {
	m := newTestTerminalManager(t)
	results := make(chan error, 1)
	m.startSSHCreate(sshCreateEvent("req-2"), func(string, string, uint32, uint32) error {
		return errors.New("认证失败")
	}, func(err error) { results <- err })
	select {
	case err := <-results:
		if err == nil || err.Error() != "认证失败" {
			t.Fatalf("expected the SSH error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("create never finished")
	}
	if m.bindingFor("cloud-terminal-0123456789abcdef") != nil {
		t.Fatal("failed session must not stay bound")
	}
}

func TestSSHCreateRejectsInvalidParametersImmediately(t *testing.T) {
	m := newTestTerminalManager(t)
	event := sshCreateEvent("req-3")
	event.Payload["profileId"] = ""
	results := make(chan error, 1)
	m.startSSHCreate(event, func(string, string, uint32, uint32) error {
		t.Error("SSH must not start with invalid parameters")
		return nil
	}, func(err error) { results <- err })
	if err := <-results; err == nil {
		t.Fatal("expected a validation error")
	}
}

func TestMFAPromptIsDeliveredToThePhoneThatStartedTheSession(t *testing.T) {
	m := newTestTerminalManager(t)
	m.setBinding(newCloudTerminalBinding("cloud-terminal-0123456789abcdef", "mobile-phone-0000000000000001"))
	sent := make(chan map[string]any, 1)
	m.sendMFAPrompt = func(target, requestID string, payload map[string]any) error {
		if target != "mobile-phone-0000000000000001" {
			t.Errorf("prompt sent to %q", target)
		}
		sent <- payload
		return nil
	}

	handled := m.deliverMFAPrompt(terminal.SshMfaPromptPayload{
		SessionID: "cloud-terminal-0123456789abcdef",
		Name:      "JumpServer",
		Prompts:   []terminal.SshMfaPromptItem{{Prompt: "Verification code: ", Echo: true}},
	})
	if !handled {
		t.Fatal("prompt for a phone session must be handled by the cloud bridge")
	}
	select {
	case payload := <-sent:
		if payload["sessionId"] != "cloud-terminal-0123456789abcdef" || payload["name"] != "JumpServer" {
			t.Fatalf("unexpected payload %+v", payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("prompt never sent")
	}

	if m.deliverMFAPrompt(terminal.SshMfaPromptPayload{SessionID: "desktop-own-session"}) {
		t.Fatal("prompts for desktop sessions must stay on the desktop")
	}
}

func TestMFAAnswerFromThePhoneReachesTheWaitingLogin(t *testing.T) {
	m := newTestTerminalManager(t)
	m.setBinding(newCloudTerminalBinding("cloud-terminal-0123456789abcdef", "mobile-phone-0000000000000001"))

	if err := m.answerMFA("mobile-phone-0000000000000001", "cloud-terminal-0123456789abcdef", map[string]any{
		"answers": []any{"123456"},
	}); err == nil {
		t.Fatal("answer with no waiting prompt must fail")
	}
	if err := m.answerMFA("mobile-other-00000000000000", "cloud-terminal-0123456789abcdef", map[string]any{
		"answers": []any{"123456"},
	}); err == nil {
		t.Fatal("another device must not answer this session's prompt")
	}
}
