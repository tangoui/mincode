package terminal

import (
	"testing"
	"time"
)

func TestReconnectDelayUsesBoundedExponentialBackoff(t *testing.T) {
	base := time.Second
	maxDelay := 5 * time.Second
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 5 * time.Second, 5 * time.Second}
	for attempt, expected := range want {
		if got := reconnectDelay(uint32(attempt), base, maxDelay); got != expected {
			t.Fatalf("attempt %d: got %s, want %s", attempt, got, expected)
		}
	}
}

func TestSSHGraceDurationCoversConfiguredAttempts(t *testing.T) {
	got := sshGraceDuration(5, time.Second, 15*time.Second)
	if got != 35*time.Second {
		t.Fatalf("got %s, want 35s", got)
	}
	if got := sshGraceDuration(1, 250*time.Millisecond, time.Second); got != minimumSSHGracePeriod {
		t.Fatalf("minimum grace period not applied: %s", got)
	}
	if got := sshGraceDuration(20, time.Minute, 5*time.Minute); got != maximumSSHGracePeriod {
		t.Fatalf("maximum grace period not applied: %s", got)
	}
}

func TestReconnectClamps(t *testing.T) {
	if got := clampReconnectAttempts(0); got != defaultSSHReconnectAttempts {
		t.Fatalf("default attempts = %d", got)
	}
	if got := clampReconnectAttempts(99); got != 20 {
		t.Fatalf("max attempts = %d", got)
	}
	if got := clampReconnectDelay(1, time.Second); got != 250*time.Millisecond {
		t.Fatalf("min delay = %s", got)
	}
}

func TestEnterSSHGracePeriodIgnoresReplacedSession(t *testing.T) {
	const sessionID = "ssh-grace-replaced-session-test"
	cancelled := false
	newDone := make(chan struct{})
	newProc := &shellProcess{
		stdinDone:     newDone,
		sshResizeTx:   make(chan [2]uint16, 1),
		sshTaskCancel: func() { cancelled = true },
	}
	sessions.put(sessionID, newProc)
	defer sessions.remove(sessionID)

	// 旧 relay 退出：proc 已被同 ID 新会话替换。
	oldProc := &shellProcess{}
	enterSSHGracePeriod(sessionID, oldProc, make(chan struct{}))
	// 同一 proc 上已被取代的 relay（done 不再是当前 stdinDone）。
	enterSSHGracePeriod(sessionID, newProc, make(chan struct{}))

	newProc.mu.Lock()
	defer newProc.mu.Unlock()
	if newProc.sshLinkState != sshLinkActive || newProc.graceToken != 0 {
		t.Fatalf("new session entered grace: state=%v token=%d", newProc.sshLinkState, newProc.graceToken)
	}
	if newProc.stdinDone != newDone || newProc.sshResizeTx == nil || newProc.sshTaskCancel == nil {
		t.Fatal("new session relay state was cleared by a stale relay")
	}
	select {
	case <-newDone:
		t.Fatal("new session stdinDone was closed by a stale relay")
	default:
	}
	if cancelled {
		t.Fatal("stale relay must not cancel the new session task")
	}
}
