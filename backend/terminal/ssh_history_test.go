package terminal

import (
	"encoding/json"
	"fmt"
	"os"

	"path/filepath"
	"strings"
	"testing"

	"mincode/app/backend/core"
)

func TestSSHHistorySafeFilter(t *testing.T) {
	for _, s := range []string{"echo\rboom", "echo\x1b[31m", "curl --password abc", "mysql -pabc", "curl https://u:p@host", "echo\u202ehidden"} {
		if safeHistoryCommand(s) {
			t.Errorf("accepted %q", s)
		}
	}
}
func TestSSHHistoryMerge(t *testing.T) {
	data := mergeSSHHistory(sshHistoryData{Enabled: true}, []string{"ls", "pwd"})
	data.Entries[0].Favorite = true
	before := data.Entries[0].ImportedAt
	data = mergeSSHHistory(data, []string{"ls", "whoami"})
	if len(data.Entries) != 3 || !data.Entries[0].Favorite || data.Entries[0].ImportedAt != before {
		t.Fatal(data)
	}
}
func TestSSHHistoryStorageAndIsolation(t *testing.T) {
	p := sshHistoryPayload{ProfileID: "a", Host: "host", Port: 22, Username: "user"}
	key := historyKey(p)
	p.Username = "root"
	if historyKey(p) == key {
		t.Fatal("user collision")
	}
	p.Username = "user"
	p.Port = 23
	if historyKey(p) == key {
		t.Fatal("port collision")
	}
	path := filepath.Join(t.TempDir(), "history", "test.json")
	data, err := readSSHHistory(path)
	if err != nil || !data.Enabled || len(data.Entries) != 0 {
		t.Fatal(data, err)
	}
	data = mergeSSHHistory(data, []string{"ls"})
	data.Enabled = false
	if err := writeSSHHistory(path, data); err != nil {
		t.Fatal(err)
	}
	got, err := readSSHHistory(path)
	if err != nil || got.Enabled || len(got.Entries) != 1 {
		t.Fatal(got, err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
}

func TestSSHHistoryLimit(t *testing.T) {
	commands := make([]string, 6000)
	for i := range commands {
		commands[i] = fmt.Sprintf("echo %d", i)
	}
	data := mergeSSHHistory(sshHistoryData{Enabled: true}, commands)
	if len(data.Entries) != 5000 {
		t.Fatal(len(data.Entries))
	}
}

func TestSSHHistoryPathCollisionAndMigration(t *testing.T) {
	root := t.TempDir()
	p := sshHistoryPayload{ProfileID: "a", Host: "h", Port: 22, Username: "u"}
	conflict := filepath.Join(root, "mincode")
	if err := os.WriteFile(conflict, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	path, err := sshHistoryPath(root, p)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeSSHHistory(path, mergeSSHHistory(sshHistoryData{Enabled: true}, []string{"pwd"})); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(conflict)
	if string(b) != "keep" {
		t.Fatal("conflicting file modified")
	}
	root = t.TempDir()
	old := filepath.Join(root, "mincode", "ssh-command-history", historyKey(p)+".json")
	data := mergeSSHHistory(sshHistoryData{Enabled: false}, []string{"ls"})
	data.Entries[0].Favorite = true
	if err := writeSSHHistory(old, data); err != nil {
		t.Fatal(err)
	}
	path, err = sshHistoryPath(root, p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := readSSHHistory(path)
	if err != nil || got.Enabled || len(got.Entries) != 1 || !got.Entries[0].Favorite {
		t.Fatalf("migration: %+v %v", got, err)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatal("legacy removed")
	}
}

func TestSSHHistoryLegacyLiveFieldsIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	legacy := `{"entries":[{"id":"saved","command":"pwd","favorite":true,"importedAt":"2026-01-01T00:00:00Z"}],"enabled":false,"live":true,"liveError":"obsolete"}`
	if err := os.WriteFile(path, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := readSSHHistory(path)
	if err != nil {
		t.Fatal(err)
	}
	if data.Enabled || len(data.Entries) != 1 || data.Entries[0] != (sshHistoryEntry{ID: "saved", Command: "pwd", Favorite: true, ImportedAt: "2026-01-01T00:00:00Z"}) {
		t.Fatalf("legacy data changed: %+v", data)
	}
	if err := writeSSHHistory(path, data); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"live", "liveError"} {
		if _, ok := fields[key]; ok {
			t.Errorf("obsolete field %q persisted", key)
		}
	}
}

func TestSSHHistoryRejectsRemovedAction(t *testing.T) {
	if _, err := core.Dispatch("ssh_history_start", "{}"); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("removed command still registered: %v", err)
	}
	if result, err := handleSSHHistory("start", "{}"); err == nil || result != "" {
		t.Fatalf("removed action accepted: %q, %v", result, err)
	}
}
