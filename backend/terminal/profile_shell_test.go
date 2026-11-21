//go:build !windows

package terminal

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidateProfileShellSpecRejectsUnsafeLaunches(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "tool")
	plain := filepath.Join(dir, "plain")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plain, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	valid := ProfileShellSpec{SessionID: "s", Executable: exe, Cwd: dir, Env: map[string]string{"A": "b"}}
	if err := validateProfileShellSpec(valid); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}
	cases := map[string]func(*ProfileShellSpec){
		"no session":      func(s *ProfileShellSpec) { s.SessionID = " " },
		"relative exe":    func(s *ProfileShellSpec) { s.Executable = "tool" },
		"missing exe":     func(s *ProfileShellSpec) { s.Executable = filepath.Join(dir, "missing") },
		"directory exe":   func(s *ProfileShellSpec) { s.Executable = dir },
		"not executable":  func(s *ProfileShellSpec) { s.Executable = plain },
		"relative cwd":    func(s *ProfileShellSpec) { s.Cwd = "." },
		"missing cwd":     func(s *ProfileShellSpec) { s.Cwd = filepath.Join(dir, "missing") },
		"bad env name":    func(s *ProfileShellSpec) { s.Env = map[string]string{"A=B": "x"} },
		"nul in argument": func(s *ProfileShellSpec) { s.Args = []string{"a\x00b"} },
	}
	for name, mutate := range cases {
		spec := valid
		mutate(&spec)
		if err := validateProfileShellSpec(spec); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
}

func TestDedupeEnvKeepsLastValue(t *testing.T) {
	got := dedupeEnv([]string{"A=1", "b=2", "A=3", "B=4"}, true)
	if len(got) != 2 || got[0] != "A=3" || got[1] != "B=4" {
		t.Fatalf("got %v", got)
	}
}

func TestStartProfileShellRunsExactExecutable(t *testing.T) {
	dir := t.TempDir()
	spec := ProfileShellSpec{SessionID: "profile-test-session", Executable: "/bin/sh", Args: []string{"-c", "sleep 5"}, Cwd: dir}
	message, err := StartProfileShell(spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if proc, ok := sessions.remove(spec.SessionID); ok {
			shutdownShellProcess(proc)
		}
	})
	if message == "" || !AttachLocalShell(spec.SessionID) {
		t.Fatalf("session not published: %q", message)
	}
	if AttachLocalShell("unknown-session") {
		t.Fatal("unknown session reported as attachable")
	}
}

func TestStartProfileShellBeforeExecRefusalStartsNothing(t *testing.T) {
	spec := ProfileShellSpec{
		SessionID: "profile-test-refused", Executable: "/bin/sh", Args: []string{"-c", "sleep 5"}, Cwd: t.TempDir(),
		BeforeExec: func() error { return errors.New("executable changed") },
		OnExit:     func() { t.Error("OnExit called for a refused start") },
	}
	if _, err := StartProfileShell(spec); err == nil || !strings.Contains(err.Error(), "executable changed") {
		t.Fatalf("expected BeforeExec refusal, got %v", err)
	}
	if AttachLocalShell(spec.SessionID) {
		t.Fatal("refused start published a session")
	}
}

func TestStartProfileShellOnExitRunsAfterCloseAndExit(t *testing.T) {
	for name, command := range map[string]string{"close": "sleep 5", "exit": "exit 0"} {
		t.Run(name, func(t *testing.T) {
			exited := make(chan struct{})
			spec := ProfileShellSpec{
				SessionID: "profile-test-exit-" + name, Executable: "/bin/sh", Args: []string{"-c", command}, Cwd: t.TempDir(),
				BeforeExec: func() error { return nil },
				OnExit:     func() { close(exited) },
			}
			if _, err := StartProfileShell(spec); err != nil {
				t.Fatal(err)
			}
			if name == "close" {
				if proc, ok := sessions.remove(spec.SessionID); ok {
					shutdownShellProcess(proc)
				}
			}
			select {
			case <-exited:
			case <-time.After(10 * time.Second):
				t.Fatal("OnExit was not called")
			}
			if AttachLocalShell(spec.SessionID) {
				t.Fatal("ended session is still attachable")
			}
		})
	}
}
