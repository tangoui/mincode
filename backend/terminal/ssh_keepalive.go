package terminal

import (
	"context"
	"time"
)

// Existing profile miss counts must not close a connection before this window.
const minimumSSHKeepAliveFailureWindow = 30 * time.Minute

func sshKeepAliveShouldClose(misses, maxMisses uint32, failedSince, now time.Time) bool {
	return misses >= maxMisses && !failedSince.IsZero() &&
		now.Sub(failedSince) >= minimumSSHKeepAliveFailureWindow
}

func startSSHKeepAlive(sessionID string, proc *shellProcess, intervalSeconds, maxMisses uint32) {
	if intervalSeconds == 0 {
		return
	}
	if intervalSeconds < 5 {
		intervalSeconds = 5
	}
	if intervalSeconds > 300 {
		intervalSeconds = 300
	}
	if maxMisses == 0 {
		maxMisses = 3
	}
	if maxMisses > 10 {
		maxMisses = 10
	}
	ctx, cancel := context.WithCancel(context.Background())
	proc.mu.Lock()
	if proc.sshKeepAliveCancel != nil {
		proc.sshKeepAliveCancel()
	}
	proc.sshKeepAliveCancel = cancel
	proc.mu.Unlock()
	go func() {
		ticker := time.NewTicker(time.Duration(intervalSeconds) * time.Second)
		defer ticker.Stop()
		misses := uint32(0)
		var failedSince time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				proc.mu.Lock()
				handle := proc.sshClient
				active := proc.sshLinkState == sshLinkActive
				proc.mu.Unlock()
				if !active || handle == nil || handle.client == nil {
					continue
				}
				_, _, err := handle.client.SendRequest("keepalive@openssh.com", true, nil)
				if err == nil {
					misses = 0
					failedSince = time.Time{}
					continue
				}
				if failedSince.IsZero() {
					failedSince = time.Now()
				}
				if misses < maxMisses {
					misses++
				}
				if sshKeepAliveShouldClose(misses, maxMisses, failedSince, time.Now()) {
					_ = handle.client.Close()
					return
				}
			}
		}
	}()
}
