package terminal

import (
	"encoding/json"
	"strings"
	"testing"

	"mincode/app/backend/core"
)

func invokeShellRunCommand(t *testing.T, payload string) RunShellCommandResult {
	t.Helper()
	encoded, err := cmdRunShellCommand("", payload)
	if err != nil {
		t.Fatalf("cmdRunShellCommand: %v", err)
	}
	var result RunShellCommandResult
	if err := json.Unmarshal([]byte(encoded), &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	return result
}

func TestShellRunCommandCapturesOutputAndExitCode(t *testing.T) {
	result := invokeShellRunCommand(t, `{"payload":{"command":"echo mincode-run && echo boom >&2"}}`)
	if !strings.Contains(result.Stdout, "mincode-run") {
		t.Fatalf("stdout missing command output: %q", result.Stdout)
	}
	if !strings.Contains(result.Stderr, "boom") {
		t.Fatalf("stderr missing output: %q", result.Stderr)
	}
	if result.ExitCode != 0 || result.TimedOut {
		t.Fatalf("unexpected result state: %+v", result)
	}
}

func TestShellRunCommandReportsNonZeroExit(t *testing.T) {
	result := invokeShellRunCommand(t, `{"payload":{"command":"exit 3"}}`)
	if result.ExitCode != 3 || result.TimedOut {
		t.Fatalf("unexpected result state: %+v", result)
	}
}

func TestShellRunCommandEnforcesTimeout(t *testing.T) {
	result := invokeShellRunCommand(t, `{"payload":{"command":"sleep 5","timeoutMs":200}}`)
	if !result.TimedOut {
		t.Fatalf("expected timed out result, got %+v", result)
	}
}

func TestShellRunCommandTruncatesOversizedOutput(t *testing.T) {
	result := invokeShellRunCommand(
		t,
		`{"payload":{"command":"printf 'abcdefghij%.0s' $(seq 1 100)","outputLimit":64}}`,
	)
	if !result.StdoutTruncated || len(result.Stdout) > 64 {
		t.Fatalf("expected stdout truncated to 64 bytes, got %d bytes (truncated=%v)", len(result.Stdout), result.StdoutTruncated)
	}
}

func TestShellRunCommandRejectsInvalidPayloads(t *testing.T) {
	if _, err := cmdRunShellCommand("", `{"payload":{"command":"  "}}`); err == nil {
		t.Fatal("expected error for empty command")
	}
	if _, err := cmdRunShellCommand("", `{"payload":{"command":"echo ok","cwd":"relative/path"}}`); err == nil {
		t.Fatal("expected error for relative cwd")
	}
	if _, err := cmdRunShellCommand("", `{"payload":{"command":"echo ok","timeoutMs":0}}`); err == nil {
		t.Fatal("expected error for non-positive timeoutMs")
	}
	if _, err := cmdRunShellCommand("", `{"payload":{"command":"echo ok","outputLimit":0}}`); err == nil {
		t.Fatal("expected error for non-positive outputLimit")
	}
}

func TestShellRunCommandRegisteredOnce(t *testing.T) {
	// core.Register 重复注册会 panic；这里确认命令已在注册表中。
	for _, name := range core.Commands() {
		if name == "shell_run_command" {
			return
		}
	}
	t.Fatal("shell_run_command not registered")
}
