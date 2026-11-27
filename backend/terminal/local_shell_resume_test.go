package terminal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"mincode/app/backend/core"
)

func TestTerminalResumeCommand(t *testing.T) {
	for _, args := range [][]string{
		{"tool", "--password", "fixture"},
		{"tool", "TOKEN=fixture"},
		{"tool", "--api-key=fixture"},
		{"tool", "--db-password", "fixture"},
		{"tool", "Authorization: Bearer fixture"},
		{"tool", "Basic Zml4dHVyZQ=="},
		{"tool", "-----BEGIN OPENSSH PRIVATE KEY-----"},
		{"tool", "postgres://user:fixture@localhost/db"},
		{"tool", "https://example.test/?access%5Ftoken=fixture"},
		{"tool", "one\ntwo"},
		{"tool", "\x1b[200~"},
	} {
		if command := terminalResumeCommand(args, "/bin/zsh"); command != "" {
			t.Errorf("unsafe command accepted for %q", args)
		}
	}
	args := []string{"sleep", "1000"}
	if command := terminalResumeCommand(args, "/bin/zsh"); command != "sleep 1000" {
		t.Errorf("simple command = %q", command)
	}
	args = []string{"tool", "", "two words", "$(touch file)", "a'b", "a\\b"}
	if got, want := terminalResumeCommand(args, "/bin/zsh"), `tool '' 'two words' '$(touch file)' 'a'\''b' 'a'\\'b'`; got != want {
		t.Errorf("quoted command = %q, want %q", got, want)
	}
}

func TestTerminalResumeRecordsArePrivateAndTakenOnce(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MINCODE_DATA_DIR", dir)
	path := filepath.Join(dir, core.AppDataNamespace, "terminal-resume.json")
	want := map[string]terminalResumeRecord{"pane": {Command: "sleep 1000", Cwd: dir}}
	if err := writeTerminalResumeRecords(want); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got&0o077 != 0 {
		t.Fatalf("resume file exposes permissions %o", got)
	}
	var wg sync.WaitGroup
	results := make(chan map[string]terminalResumeRecord, 2)
	for i := 0; i < cap(results); i++ {
		wg.Go(func() {
			result, err := core.Dispatch("take_local_shell_resume", "{}")
			if err != nil {
				t.Error(err)
				return
			}
			var records map[string]terminalResumeRecord
			if err := json.Unmarshal([]byte(result), &records); err != nil {
				t.Error(err)
				return
			}
			results <- records
		})
	}
	wg.Wait()
	close(results)
	taken := 0
	for records := range results {
		if len(records) == 0 {
			continue
		}
		if !reflect.DeepEqual(records, want) {
			t.Errorf("records = %v, want %v", records, want)
		}
		taken++
	}
	if taken != 1 {
		t.Errorf("records returned %d times, want 1", taken)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("consumed file still present: %v", err)
	}
	if err := writeTerminalResumeRecords(want); err != nil {
		t.Fatal(err)
	}
	if err := writeTerminalResumeRecords(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("empty snapshot left a stale file: %v", err)
	}
	if err := os.WriteFile(path, []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := core.Dispatch("take_local_shell_resume", "{}"); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Errorf("invalid snapshot error = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("invalid file still present: %v", err)
	}
}
