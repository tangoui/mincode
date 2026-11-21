//go:build !windows

package lsp

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// waitGone waits until pid no longer exists (killed and reaped).
func waitGone(t *testing.T, pid int) bool {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func TestJdtlsJavaProbeKillsProcessGroupAndCachesTimeout(t *testing.T) {
	resetJdtlsJavaCaches(t)
	savedTimeout, savedTTL := jdtlsJavaProbeTimeout, jdtlsJavaTimeoutTTL
	jdtlsJavaProbeTimeout = 2 * time.Second
	jdtlsJavaTimeoutTTL = time.Hour
	t.Cleanup(func() { jdtlsJavaProbeTimeout, jdtlsJavaTimeoutTTL = savedTimeout, savedTTL })
	home := t.TempDir()
	scratch := t.TempDir()
	pids := filepath.Join(scratch, "pids")
	runs := filepath.Join(scratch, "runs")
	// The background child keeps the output pipe open and outlives its parent
	// unless the whole process group is killed. Starting a shell and forking
	// has no upper bound on a loaded machine, so the probe may time out before
	// the PID is on disk; the checks below handle both orders instead of
	// assuming the fork always wins the race.
	writeExecutable(t, filepath.Join(home, "bin", "java"),
		"#!/bin/sh\necho run >> '"+runs+"'\nsleep 30 &\necho $! >> '"+pids+"'\nsleep 30\n")
	t.Setenv("JAVA_HOME", home)
	launcher := fakeJdtlsInstall(t, nil)
	var logs []string
	log := func(message string) { logs = append(logs, message) }

	started := time.Now()
	if env := builtinServerEnv(serverDefinition{ID: jdtlsServerID}, launcher, log); envValue(env, "JAVA_HOME") != home {
		t.Fatal("JAVA_HOME of a hanging java not kept")
	}
	// The probe must give up instead of waiting out the 30 s sleep.
	if elapsed := time.Since(started); elapsed > 20*time.Second {
		t.Fatalf("probe took %s", elapsed)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "timed out") || strings.Contains(logs[0], home) {
		t.Fatalf("logs = %q", logs)
	}
	assertProbeChildrenGone(t, pids)
	runsBefore := probeRuns(t, runs)

	// Within the TTL a timed-out java is not probed again, nor logged. An
	// unchanged run count and log prove no probe ran; the time bound only
	// guards against waiting out the timeout, with headroom for a loaded machine.
	started = time.Now()
	builtinServerEnv(serverDefinition{ID: jdtlsServerID}, launcher, log)
	if elapsed := time.Since(started); elapsed > jdtlsJavaProbeTimeout/2 || probeRuns(t, runs) != runsBefore || len(logs) != 1 {
		t.Fatalf("cached timeout: %s, runs %d (before %d), logs %q", elapsed, probeRuns(t, runs), runsBefore, logs)
	}
	// After the TTL it is probed (and logged) again.
	jdtlsJavaTimeoutTTL = 0
	builtinServerEnv(serverDefinition{ID: jdtlsServerID}, launcher, log)
	if len(logs) != 2 || !strings.Contains(logs[1], "timed out") {
		t.Fatalf("expired timeout: logs %q", logs)
	}
	assertProbeChildrenGone(t, pids)
}

// assertProbeChildrenGone checks that every background child the fake java
// recorded was killed with its process group. A probe killed before it forked
// leaves no PID; that run still covered the timeout, just not the group kill.
func assertProbeChildrenGone(t *testing.T, pids string) {
	t.Helper()
	data, err := os.ReadFile(pids)
	if os.IsNotExist(err) {
		t.Log("fake java was killed before forking (loaded machine); process-group kill not observed")
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.FieldsSeq(string(data)) {
		pid, err := strconv.Atoi(line)
		if err != nil {
			t.Fatal(err)
		}
		if !waitGone(t, pid) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("background child %d survived the probe timeout: the process group was not killed", pid)
		}
	}
}

// returnsPromptly runs f and fails if it blocks (a FIFO opened for reading
// waits for a writer).
func returnsPromptly(t *testing.T, name string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		f()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("%s blocked on a FIFO", name)
	}
}

func TestJdtlsFileReadsSkipFIFOs(t *testing.T) {
	resetJdtlsJavaCaches(t)
	home := t.TempDir()
	writeExecutable(t, filepath.Join(home, "bin", "java"), "#!/bin/sh\nexit 1\n")
	if err := syscall.Mkfifo(filepath.Join(home, "release"), 0o644); err != nil {
		t.Fatal(err)
	}
	returnsPromptly(t, "releaseJavaMajor", func() {
		if got := releaseJavaMajor(home); got != 0 {
			t.Errorf("FIFO release = %d", got)
		}
	})
	returnsPromptly(t, "javaRuntimes", func() {
		if got := javaRuntimes(home, nil); len(got) != 0 {
			t.Errorf("FIFO release runtimes = %+v", got)
		}
	})
	launcher := fakeJdtlsInstall(t, nil)
	if err := syscall.Mkfifo(filepath.Join(filepath.Dir(filepath.Dir(launcher)), "plugins", "org.eclipse.jdt.core_9.jar"), 0o644); err != nil {
		t.Fatal(err)
	}
	returnsPromptly(t, "jdtlsRequiredJava", func() {
		if got := jdtlsRequiredJava(launcher); got != jdtlsLauncherMinJava {
			t.Errorf("FIFO jar required = %d", got)
		}
	})
}
