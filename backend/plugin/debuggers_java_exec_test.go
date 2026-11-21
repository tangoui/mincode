package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"mincode/app/backend/debug"
)

func prepareJdtlsJavaExec(t *testing.T, fixture *jdtlsDebuggerFixture, java string, extra string) PreparedDebugLaunch {
	t.Helper()
	args := map[string]any{"mainClass": "example.Main", "args": extra, "vmArgs": extra}
	if java != "" {
		args["javaExec"] = java
	}
	raw, _ := json.Marshal(map[string]any{"request": "launch", "arguments": args})
	fixture.resolve = string(raw)
	prepared, err := fixture.prepare()
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}
func trustJdtlsJavaExec(t *testing.T, fixture *jdtlsDebuggerFixture, prepared PreparedDebugLaunch) {
	t.Helper()
	if _, err := fixture.manager.trustDebugger(debuggerTrustRequest{PrepareID: prepared.PrepareID, Fingerprint: prepared.Launch.Fingerprint}); err != nil {
		t.Fatal(err)
	}
}

func TestJdtlsJavaExecChangesRequireNewTrust(t *testing.T) {
	f := newJdtlsDebuggerFixture(t)
	f.load(t)
	a := writeTerminalProfileExecutable(t, t.TempDir(), "bin/java-a")
	b := writeTerminalProfileExecutable(t, t.TempDir(), "bin/java-b")
	first := prepareJdtlsJavaExec(t, f, a, "")
	if first.Launch.Trusted {
		t.Fatal("first executable already trusted")
	}
	trustJdtlsJavaExec(t, f, first)
	same := prepareJdtlsJavaExec(t, f, a, "different run arguments")
	if !same.Launch.Trusted || same.Launch.Fingerprint != first.Launch.Fingerprint {
		t.Fatal("ordinary arguments changed executable trust")
	}
	changed := prepareJdtlsJavaExec(t, f, b, "")
	if changed.Launch.Trusted || changed.Launch.Fingerprint == first.Launch.Fingerprint {
		t.Fatal("changed javaExec reused old trust")
	}
	if _, err := debuggerStartForTest(f.manager, changed, changed.Launch.Fingerprint, nil); processLaunchErrorCode(err) != processLaunchErrUntrusted && processLaunchErrorCode(err) != processLaunchErrChanged {
		t.Fatalf("unconfirmed changed executable started: %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatal("untrusted change reached jdtls")
	}
}

func TestJdtlsJavaExecPreparedLaunchRejectsFileChanges(t *testing.T) {
	for _, change := range []string{"bytes_preserving_metadata", "permissions", "removed", "symlink_target"} {
		t.Run(change, func(t *testing.T) {
			if runtime.GOOS == "windows" && (change == "permissions" || change == "symlink_target") {
				t.Skip("POSIX permissions/symlink fixture")
			}
			f := newJdtlsDebuggerFixture(t)
			f.load(t)
			java := writeTerminalProfileExecutable(t, t.TempDir(), "bin/java")
			configured := java
			if change == "symlink_target" {
				configured = filepath.Join(t.TempDir(), "java")
				if err := os.Symlink(java, configured); err != nil {
					t.Fatal(err)
				}
			}
			prepared := prepareJdtlsJavaExec(t, f, configured, "")
			trustJdtlsJavaExec(t, f, prepared)
			switch change {
			case "bytes_preserving_metadata":
				info, _ := os.Stat(java)
				data, _ := os.ReadFile(java)
				data[len(data)-1] ^= 1
				if err := os.WriteFile(java, data, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(java, info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
			case "permissions":
				if err := os.Chmod(java, 0644); err != nil {
					t.Fatal(err)
				}
			case "removed":
				if err := os.Remove(java); err != nil {
					t.Fatal(err)
				}
			case "symlink_target":
				other := writeTerminalProfileExecutable(t, t.TempDir(), "bin/java")
				if err := os.Remove(configured); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, configured); err != nil {
					t.Fatal(err)
				}
			}
			// A bad fake port is reached by the old code; failing for that reason is not a rejection.
			f.setPort(`0`)
			_, err := debuggerStartForTest(f.manager, prepared, prepared.Launch.Fingerprint, nil)
			if err == nil || len(f.calls) != 0 {
				t.Fatalf("changed executable reached jdtls: calls=%d err=%v", len(f.calls), err)
			}
		})
	}
}

func TestJdtlsJavaExecRecheckedBeforeDAPBegin(t *testing.T) {
	f := newJdtlsDebuggerFixture(t)
	f.load(t)
	java := writeTerminalProfileExecutable(t, t.TempDir(), "bin/java")
	prepared := prepareJdtlsJavaExec(t, f, java, "")
	trustJdtlsJavaExec(t, f, prepared)
	f.setPort(jsonInt(fakeJavaDebugPort(t)))
	original := jdtlsExecuteCommand
	jdtlsExecuteCommand = func(ctx context.Context, root, server, command string, args json.RawMessage) (json.RawMessage, error) {
		result, err := original(ctx, root, server, command, args)
		if err == nil {
			data, _ := os.ReadFile(java)
			data[len(data)-1] ^= 1
			if e := os.WriteFile(java, data, 0755); e != nil {
				return nil, e
			}
		}
		return result, err
	}
	t.Cleanup(func() { jdtlsExecuteCommand = original })
	if _, err := debuggerStartForTest(f.manager, prepared, prepared.Launch.Fingerprint, nil); err == nil {
		t.Fatal("executable swapped during adapter setup still reached DAP Begin")
	}
}

func TestJdtlsJavaExecChangedBytesCreateNewConfirmation(t *testing.T) {
	f := newJdtlsDebuggerFixture(t)
	f.load(t)
	java := writeTerminalProfileExecutable(t, t.TempDir(), "bin/java")
	first := prepareJdtlsJavaExec(t, f, java, "")
	trustJdtlsJavaExec(t, f, first)
	info, _ := os.Stat(java)
	data, _ := os.ReadFile(java)
	data[len(data)-1] ^= 1
	if err := os.WriteFile(java, data, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(java, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	second := prepareJdtlsJavaExec(t, f, java, "")
	if second.Launch.Trusted || second.Launch.Fingerprint == first.Launch.Fingerprint {
		t.Fatal("same-size content replacement reused cached identity")
	}
}

func TestJdtlsJavaExecPathMatchesDAPSpelling(t *testing.T) {
	f := newJdtlsDebuggerFixture(t)
	f.load(t)
	java := writeTerminalProfileExecutable(t, t.TempDir(), "bin/java")
	// Do not clean only the fingerprint while DAP receives another spelling.
	rawPath := filepath.Dir(java) + string(filepath.Separator) + ".." + string(filepath.Separator) + "bin" + string(filepath.Separator) + "java"
	raw, _ := json.Marshal(map[string]any{"request": "launch", "arguments": map[string]any{"javaExec": rawPath}})
	f.resolve = string(raw)
	if _, err := f.prepare(); err == nil {
		t.Fatal("accepted different normalized/DAP paths")
	}
}

func TestJdtlsJavaExecRejectsInvalidResolveValues(t *testing.T) {
	for _, value := range []any{true, 42, "relative/java", filepath.Join(t.TempDir(), "missing"), t.TempDir()} {
		t.Run("invalid", func(t *testing.T) {
			f := newJdtlsDebuggerFixture(t)
			f.load(t)
			raw, _ := json.Marshal(map[string]any{"request": "launch", "arguments": map[string]any{"javaExec": value}})
			f.resolve = string(raw)
			if _, err := f.prepare(); err == nil {
				t.Fatalf("invalid javaExec accepted: %v", value)
			}
		})
	}
}

func TestJdtlsJavaExecApprovedChangeReachesDAP(t *testing.T) {
	f := newJdtlsDebuggerFixture(t)
	f.load(t)
	a := writeTerminalProfileExecutable(t, t.TempDir(), "bin/java-a")
	b := writeTerminalProfileExecutable(t, t.TempDir(), "bin/java-b")
	first := prepareJdtlsJavaExec(t, f, a, "")
	trustJdtlsJavaExec(t, f, first)
	second := prepareJdtlsJavaExec(t, f, b, "")
	trustJdtlsJavaExec(t, f, second)
	launched := make(chan json.RawMessage, 1)
	f.setPort(jsonInt(fakeJavaDebugPort(t, func(message debug.Message) {
		if message.Command == "launch" {
			launched <- message.Arguments
		}
	})))
	result, err := debuggerStartForTest(f.manager, second, second.Launch.Fingerprint, nil)
	if err != nil || result.SessionID == "" {
		t.Fatalf("approved executable failed: %v", err)
	}
	select {
	case raw := <-launched:
		var args map[string]any
		if err := json.Unmarshal(raw, &args); err != nil {
			t.Fatal(err)
		}
		if args["javaExec"] != b {
			t.Fatalf("DAP executable=%v", args["javaExec"])
		}
	case <-time.After(time.Second):
		t.Fatal("missing DAP launch arguments")
	}
}

// javaExec is the user's JDK, not a plugin file: errors from the shared file
// checks must not tell the user that a plugin file changed.
func TestJdtlsJavaExecErrorsNameJavaExec(t *testing.T) {
	big := filepath.Join(t.TempDir(), "java")
	maxBytes := debuggerSpec.Limit("pluginExecutableHashMaxBytes")
	f, err := os.OpenFile(big, os.O_CREATE|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(int64(maxBytes) + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	raw, _ := json.Marshal(map[string]any{"javaExec": big})
	_, err = resolveJdtlsJavaExec(raw)
	if err == nil {
		t.Fatal("an oversized javaExec was accepted")
	}
	if processLaunchErrorCode(err) != processLaunchErrForbidden || strings.Contains(err.Error(), "plugin file") || !strings.Contains(err.Error(), "javaExec") {
		t.Fatalf("err = %v, want forbidden naming javaExec", err)
	}
}
