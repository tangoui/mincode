package terminal

import "time"

const (
	defaultSSHReconnectAttempts  = uint32(5)
	defaultSSHReconnectBaseDelay = time.Second
	defaultSSHReconnectMaxDelay  = 15 * time.Second
	minimumSSHGracePeriod        = 30 * time.Second
	maximumSSHGracePeriod        = 5 * time.Minute
)

func clampReconnectAttempts(value uint32) uint32 {
	if value == 0 {
		return defaultSSHReconnectAttempts
	}
	if value > 20 {
		return 20
	}
	return value
}

func clampReconnectDelay(valueMs uint32, fallback time.Duration) time.Duration {
	if valueMs == 0 {
		return fallback
	}
	delay := time.Duration(valueMs) * time.Millisecond
	if delay < 250*time.Millisecond {
		return 250 * time.Millisecond
	}
	if delay > 5*time.Minute {
		return 5 * time.Minute
	}
	return delay
}

func reconnectDelay(attempt uint32, baseDelay, maxDelay time.Duration) time.Duration {
	if baseDelay <= 0 {
		baseDelay = defaultSSHReconnectBaseDelay
	}
	if maxDelay <= 0 {
		maxDelay = defaultSSHReconnectMaxDelay
	}
	if maxDelay < baseDelay {
		maxDelay = baseDelay
	}
	delay := baseDelay
	for i := uint32(0); i < attempt && delay < maxDelay; i++ {
		if delay > maxDelay/2 {
			return maxDelay
		}
		delay *= 2
	}
	if delay > maxDelay {
		return maxDelay
	}
	return delay
}

func sshGraceDuration(maxAttempts uint32, baseDelay, maxDelay time.Duration) time.Duration {
	if maxAttempts == 0 {
		return minimumSSHGracePeriod
	}
	total := 5 * time.Second
	for attempt := uint32(0); attempt < maxAttempts; attempt++ {
		total += reconnectDelay(attempt, baseDelay, maxDelay)
		if total >= maximumSSHGracePeriod {
			return maximumSSHGracePeriod
		}
	}
	if total < minimumSSHGracePeriod {
		return minimumSSHGracePeriod
	}
	return total
}

// enterSSHGracePeriod 由 relay 退出时调用。会话已被同 ID 的新实例替换，或该 relay
// 已不是 proc 当前的 relay（done 不再是 stdinDone）时为 no-op，避免旧 goroutine 误伤新会话。
func enterSSHGracePeriod(sessionID string, proc *shellProcess, done <-chan struct{}) {
	if current, ok := sessions.get(sessionID); !ok || current != proc {
		return
	}
	proc.mu.Lock()
	if proc.sshLinkState == sshLinkGracePeriod || proc.stdinDone != done {
		proc.mu.Unlock()
		return
	}
	proc.sshLinkState = sshLinkGracePeriod
	proc.stdinTx = nil
	if proc.stdinDone != nil {
		close(proc.stdinDone)
		proc.stdinDone = nil
	}
	proc.sshResizeTx = nil
	proc.sshTaskCancel = nil
	proc.sshReconnectAttempts = 0
	proc.graceToken++
	token := proc.graceToken
	baseDelay := proc.sshReconnectBaseDelay
	maxDelay := proc.sshReconnectMaxDelay
	maxAttempts := proc.sshReconnectMaxAttempts
	proc.mu.Unlock()

	emitShellSessionEnded(sessionID, "ssh_channel_closed", true)
	go runSSHRecoveryLoop(sessionID, token, maxAttempts, baseDelay, maxDelay)
}

func runSSHRecoveryLoop(sessionID string, token uint64, maxAttempts uint32, baseDelay, maxDelay time.Duration) {
	deadline := time.Now().Add(sshGraceDuration(maxAttempts, baseDelay, maxDelay))
	attempt := uint32(0)
	for time.Now().Before(deadline) {
		delay := reconnectDelay(attempt, baseDelay, maxDelay)
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		if delay > remaining {
			delay = remaining
		}
		time.Sleep(delay)

		if !sshGraceTokenIsCurrent(sessionID, token) {
			return
		}
		if tryRecoverSSHShellToken(sessionID, token) {
			return
		}
		proc, ok := sessions.get(sessionID)
		if !ok {
			return
		}
		proc.mu.Lock()
		attempt = proc.sshReconnectAttempts
		exhausted := proc.sshRedial != nil && proc.sshReconnectMaxAttempts > 0 && attempt >= proc.sshReconnectMaxAttempts
		proc.mu.Unlock()
		if exhausted {
			break
		}
		// When smart redial is disabled, continue probing the existing SSH transport
		// at a bounded cadence for the normal grace window.
		if maxAttempts == 0 {
			attempt++
		}
	}
	expireSSHGracePeriod(sessionID, token)
}

func sshGraceTokenIsCurrent(sessionID string, token uint64) bool {
	proc, ok := sessions.get(sessionID)
	if !ok {
		return false
	}
	proc.mu.Lock()
	defer proc.mu.Unlock()
	return proc.graceToken == token && proc.sshLinkState == sshLinkGracePeriod
}

func expireSSHGracePeriod(sessionID string, token uint64) {
	if !sshGraceTokenIsCurrent(sessionID, token) {
		return
	}
	if old, ok := sessions.remove(sessionID); ok {
		if old.control != nil {
			old.control.end()
		}
		shutdownShellProcess(old)
	}
	emitShellSessionEnded(sessionID, "ssh_grace_expired", false)
}

func tryRecoverSSHShell(sessionID string) bool {
	return tryRecoverSSHShellToken(sessionID, ^uint64(0))
}

func tryRecoverSSHShellToken(sessionID string, token uint64) bool {
	proc, ok := sessions.get(sessionID)
	if !ok {
		return false
	}
	proc.sshReconnectMu.Lock()
	defer proc.sshReconnectMu.Unlock()

	proc.mu.Lock()
	if proc.sshClient == nil {
		proc.mu.Unlock()
		return false
	}
	if proc.sshLinkState == sshLinkActive && proc.stdinTx != nil {
		proc.mu.Unlock()
		return true
	}
	if proc.sshLinkState != sshLinkGracePeriod || (token != ^uint64(0) && proc.graceToken != token) {
		proc.mu.Unlock()
		return false
	}
	meta := proc.sshRecoveryMeta
	proc.mu.Unlock()
	if meta == nil {
		meta = &sshRecoveryMeta{cols: 120, rows: 32}
	}

	if err := startSSHShellRelay(sessionID, proc, meta.cols, meta.rows); err != nil {
		proc.mu.Lock()
		stillGrace := proc.sshLinkState == sshLinkGracePeriod && (token == ^uint64(0) || proc.graceToken == token)
		redial := proc.sshRedial
		if proc.sshReconnectMaxAttempts > 0 && proc.sshReconnectAttempts >= proc.sshReconnectMaxAttempts {
			redial = nil
		}
		if redial != nil {
			proc.sshReconnectAttempts++
		}
		proc.mu.Unlock()
		if !stillGrace || redial == nil {
			return false
		}

		client, jump, dialErr := redial()
		if dialErr != nil {
			return false
		}
		proc.portForwardMu.Lock()
		proc.mu.Lock()
		oldClient, oldJump := proc.sshClient, proc.jumpClient
		proc.sshClient = &sshClientHandle{client: client}
		if jump != nil {
			proc.jumpClient = &sshClientHandle{client: jump}
		} else {
			proc.jumpClient = nil
		}
		keepAliveInterval := proc.sshKeepAliveInterval
		keepAliveMaxMisses := proc.sshKeepAliveMaxMisses
		for _, forward := range proc.portForwards {
			if forward.cancel != nil {
				forward.cancel()
			}
		}
		proc.portForwards = nil
		proc.mu.Unlock()
		proc.portForwardMu.Unlock()

		if oldClient != nil && oldClient.client != nil {
			_ = oldClient.client.Close()
		}
		if oldJump != nil && oldJump.client != nil {
			_ = oldJump.client.Close()
		}
		if err := startSSHShellRelay(sessionID, proc, meta.cols, meta.rows); err != nil {
			_ = client.Close()
			if jump != nil {
				_ = jump.Close()
			}
			return false
		}
		startSSHKeepAlive(sessionID, proc, keepAliveInterval, keepAliveMaxMisses)
		restartSessionPortForwards(proc)
	}

	proc.mu.Lock()
	proc.sshLinkState = sshLinkActive
	proc.sshReconnectAttempts = 0
	proc.mu.Unlock()
	emitShellSessionRecovered(sessionID)
	return true
}
