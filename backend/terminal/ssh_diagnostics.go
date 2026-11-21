package terminal

import (
	"errors"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mincode/app/backend/core"
)

const sshConnectionProgressEvent = "ssh-connection-progress"

const (
	sshConnectionHopTarget = "target"
	sshConnectionHopJump   = "jump"

	sshConnectionStageTCP            = "tcp"
	sshConnectionStageProxy          = "proxy"
	sshConnectionStageHandshake      = "handshake"
	sshConnectionStageHostKey        = "host-key"
	sshConnectionStageAuthentication = "authentication"
	sshConnectionStageShell          = "shell"

	sshConnectionStateStarted   = "started"
	sshConnectionStateSucceeded = "succeeded"
	sshConnectionStateFailed    = "failed"
)

// sshConnectionProgressPayload is a credential-free connection diagnostic event.
// It intentionally carries stable codes and timings only; raw errors may contain
// private-key paths or server-provided text and therefore stay on the command result path.
type sshConnectionProgressPayload struct {
	SessionID      string `json:"sessionId"`
	AttemptID      uint64 `json:"attemptId"`
	Hop            string `json:"hop"`
	Stage          string `json:"stage"`
	State          string `json:"state"`
	ElapsedMs      uint64 `json:"elapsedMs"`
	TotalElapsedMs uint64 `json:"totalElapsedMs"`
	ErrorCode      string `json:"errorCode,omitempty"`
}

type sshConnectionDiagnostics struct {
	mu            sync.Mutex
	sessionID     string
	attemptID     uint64
	startedAt     time.Time
	activeHop     string
	activeStage   string
	activeStarted time.Time
	failed        bool
	emit          func(string, any)
}

var sshConnectionAttemptCounter atomic.Uint64

func newSSHConnectionDiagnostics(sessionID string) *sshConnectionDiagnostics {
	return newSSHConnectionDiagnosticsWithEmitter(sessionID, core.Emit)
}

func newSSHConnectionDiagnosticsWithEmitter(sessionID string, emit func(string, any)) *sshConnectionDiagnostics {
	return &sshConnectionDiagnostics{
		sessionID: strings.TrimSpace(sessionID),
		attemptID: sshConnectionAttemptCounter.Add(1),
		startedAt: time.Now(),
		emit:      emit,
	}
}

func (d *sshConnectionDiagnostics) begin(hop, stage string) {
	if d == nil {
		return
	}
	now := time.Now()
	d.mu.Lock()
	d.activeHop = hop
	d.activeStage = stage
	d.activeStarted = now
	d.emitLocked(sshConnectionStateStarted, hop, stage, 0, "", now)
	d.mu.Unlock()
}

func (d *sshConnectionDiagnostics) succeed(hop, stage string) {
	if d == nil {
		return
	}
	now := time.Now()
	d.mu.Lock()
	started := d.activeStarted
	if d.activeHop != hop || d.activeStage != stage || started.IsZero() {
		started = now
	}
	d.emitLocked(sshConnectionStateSucceeded, hop, stage, durationMillis(started, now), "", now)
	if d.activeHop == hop && d.activeStage == stage {
		d.activeHop = ""
		d.activeStage = ""
		d.activeStarted = time.Time{}
	}
	d.mu.Unlock()
}

func (d *sshConnectionDiagnostics) fail(hop, stage string, err error) {
	if d == nil {
		return
	}
	now := time.Now()
	d.mu.Lock()
	if d.failed {
		d.mu.Unlock()
		return
	}
	started := d.activeStarted
	if d.activeHop != hop || d.activeStage != stage || started.IsZero() {
		started = now
		d.activeHop = hop
		d.activeStage = stage
	}
	d.failed = true
	d.emitLocked(sshConnectionStateFailed, hop, stage, durationMillis(started, now), classifySSHConnectionError(stage, err), now)
	d.mu.Unlock()
}

func (d *sshConnectionDiagnostics) failActiveOr(hop, stage string, err error) {
	if d == nil {
		return
	}
	d.mu.Lock()
	activeHop, activeStage, failed := d.activeHop, d.activeStage, d.failed
	d.mu.Unlock()
	if failed {
		return
	}
	if activeHop != "" && activeStage != "" {
		d.fail(activeHop, activeStage, err)
		return
	}
	d.fail(hop, stage, err)
}

func (d *sshConnectionDiagnostics) emitLocked(state, hop, stage string, elapsedMs uint64, errorCode string, now time.Time) {
	if d.emit == nil || d.sessionID == "" {
		return
	}
	d.emit(sshConnectionProgressEvent, sshConnectionProgressPayload{
		SessionID:      d.sessionID,
		AttemptID:      d.attemptID,
		Hop:            hop,
		Stage:          stage,
		State:          state,
		ElapsedMs:      elapsedMs,
		TotalElapsedMs: durationMillis(d.startedAt, now),
		ErrorCode:      errorCode,
	})
}

func durationMillis(start, end time.Time) uint64 {
	if start.IsZero() || end.Before(start) {
		return 0
	}
	return uint64(end.Sub(start).Milliseconds())
}

func classifySSHConnectionError(stage string, err error) string {
	if err == nil {
		return "unknown"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "dns"
	}
	if errors.Is(err, os.ErrNotExist) {
		return "key-not-found"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}

	message := strings.ToLower(err.Error())
	switch {
	// SOCKS5 代理路径的失败优先归类为 proxy，避免被通用的 refused/timeout 吞掉。
	case strings.Contains(message, "socks5 代理"):
		return "proxy"
	case strings.Contains(message, "connection refused") || strings.Contains(message, "actively refused"):
		return "refused"
	case strings.Contains(message, "no route to host") || strings.Contains(message, "network is unreachable"):
		return "unreachable"
	case strings.Contains(message, "host key mismatch") || strings.Contains(message, "knownhosts: key mismatch"):
		return "host-key-mismatch"
	case strings.Contains(message, "known_hosts") || strings.Contains(message, "host key"):
		return "host-key-verification"
	case strings.Contains(message, "ssh_auth_sock") || strings.Contains(message, "ssh agent"):
		return "agent-unavailable"
	case strings.Contains(message, "encrypted private key") || strings.Contains(message, "passphrase"):
		return "key-passphrase"
	case strings.Contains(message, "private key") || strings.Contains(message, "私钥"):
		return "key-invalid"
	case strings.Contains(message, "mfa") && strings.Contains(message, "timeout"):
		return "mfa-timeout"
	case strings.Contains(message, "unable to authenticate") || strings.Contains(message, "no supported methods remain") || strings.Contains(message, "permission denied"):
		return "authentication"
	}

	switch stage {
	case sshConnectionStageTCP, sshConnectionStageProxy:
		return "network"
	case sshConnectionStageHandshake:
		return "handshake"
	case sshConnectionStageHostKey:
		return "host-key-verification"
	case sshConnectionStageAuthentication:
		return "authentication"
	case sshConnectionStageShell:
		return "shell"
	default:
		return "unknown"
	}
}
