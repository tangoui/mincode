package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func statusDescendantHelper(mode string) {
	pidFile, heartbeat := os.Getenv("MINCODE_STATUS_PID"), os.Getenv("MINCODE_STATUS_HEARTBEAT")
	if mode == "heartbeat" {
		file, err := os.OpenFile(heartbeat, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			os.Exit(3)
		}
		defer file.Close()
		if err = os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			os.Exit(4)
		}
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		stop := time.NewTimer(time.Minute)
		defer stop.Stop()
		for {
			select {
			case <-ticker.C:
				if _, err = file.Write([]byte{'x'}); err != nil {
					os.Exit(5)
				}
			case <-stop.C:
				return
			}
		}
	}
	child := statusChild("heartbeat")
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	if err := child.Start(); err != nil {
		os.Exit(6)
	}
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(pidFile); err == nil {
			break
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			os.Exit(7)
		}
	}
	if mode == "cancelDescendant" {
		<-time.NewTimer(time.Minute).C
	}
	if mode == "overflowDescendant" {
		_, _ = os.Stdout.Write(bytes.Repeat([]byte{'x'}, 4*1024*1024+1))
	}
	// The child deliberately holds inherited stdout after the root exits.
	os.Exit(0)
}

func TestStatusEventsDescendantCleanup(t *testing.T) {
	for _, mode := range []string{"pipeDescendant", "overflowDescendant", "cancelDescendant"} {
		t.Run(mode, func(t *testing.T) {
			attempts := 1
			if mode == "overflowDescendant" {
				attempts = 12
			}
			for i := 0; i < attempts; i++ {
				dir := t.TempDir()
				pidFile, heartbeat := filepath.Join(dir, "pid"), filepath.Join(dir, "heartbeat")
				cmd := statusChild(mode)
				cmd.Env = append(cmd.Env, "MINCODE_STATUS_PID="+pidFile, "MINCODE_STATUS_HEARTBEAT="+heartbeat)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				started := time.Now()
				var err error
				if mode == "cancelDescendant" {
					result := make(chan error, 1)
					go func() { _, runErr := runPluginStatusCommand(ctx, cmd); result <- runErr }()
					ready := time.NewTimer(2 * time.Second)
					tick := time.NewTicker(time.Millisecond)
					for {
						if _, statErr := os.Stat(pidFile); statErr == nil {
							break
						}
						select {
						case <-tick.C:
						case <-ready.C:
							cancel()
							t.Fatal("descendant did not start before cancellation")
						}
					}
					ready.Stop()
					tick.Stop()
					cancelledAt := time.Now()
					cancel()
					select {
					case err = <-result:
					case <-time.After(time.Second):
						t.Fatal("cancellation did not synchronously join the contained tree")
					}
					if !errors.Is(err, context.Canceled) || time.Since(cancelledAt) > pluginStatusCleanupReserve {
						t.Fatalf("cancellation cleanup was not bounded: %v", err)
					}
				} else {
					_, err = runPluginStatusCommand(ctx, cmd)
				}
				cancel()
				if time.Since(started) > 5*time.Second {
					t.Fatal("tree cleanup exceeded the sample bound")
				}
				if mode == "overflowDescendant" && err == nil {
					t.Fatal("overflow became a successful snapshot")
				}
				raw, readErr := os.ReadFile(pidFile)
				if readErr != nil {
					t.Fatal(readErr)
				}
				pid, parseErr := strconv.Atoi(string(raw))
				if parseErr != nil {
					t.Fatal(parseErr)
				}
				process, findErr := os.FindProcess(pid)
				if statusProcessLookupFailed(findErr, statusProcessGoneError) {
					t.Fatal(findErr)
				}
				// A failing RED must not leave the deliberately orphaned helper running.
				if process != nil {
					defer func() { _ = process.Kill(); _ = process.Release() }()
				}
				if err := statusHeartbeatStopped(heartbeat); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

// This assertion remains mandatory even if lookup proves the PID is gone.
func statusHeartbeatStopped(heartbeat string) error {
	first, err := os.Stat(heartbeat)
	if err != nil {
		return err
	}
	timer := time.NewTimer(70 * time.Millisecond)
	defer timer.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			current, err := os.Stat(heartbeat)
			if err != nil {
				return err
			}
			if current.Size() != first.Size() {
				return fmt.Errorf("descendant kept running after sampler returned (root exit/overflow cleanup race)")
			}
		case <-timer.C:
			return nil
		}
	}
}

func TestStatusHeartbeatRejectsLiveDescendant(t *testing.T) {
	dir := t.TempDir()
	pidFile, heartbeat := filepath.Join(dir, "pid"), filepath.Join(dir, "heartbeat")
	child := statusChild("heartbeat")
	child.Env = append(child.Env, "MINCODE_STATUS_PID="+pidFile, "MINCODE_STATUS_HEARTBEAT="+heartbeat)
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	ready := false
	for !ready {
		select {
		case <-tick.C:
			info, err := os.Stat(heartbeat)
			ready = err == nil && info.Size() > 0
		case <-deadline.C:
			t.Fatal("live heartbeat helper was not ready")
		}
	}
	process, err := os.FindProcess(child.Process.Pid)
	if err != nil || process == nil {
		t.Fatalf("live descendant lookup: %v", err)
	}
	defer process.Release()
	if err := statusHeartbeatStopped(heartbeat); err == nil {
		t.Fatal("live descendant passed the containment assertion")
	}
}
