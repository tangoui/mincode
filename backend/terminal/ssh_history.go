package terminal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"mincode/app/backend/core"
)

type sshHistoryEntry struct {
	ID         string `json:"id"`
	Command    string `json:"command"`
	Favorite   bool   `json:"favorite"`
	ImportedAt string `json:"importedAt"`
}
type sshHistoryData struct {
	Entries []sshHistoryEntry `json:"entries"`
	Enabled bool              `json:"enabled"`
}
type sshHistoryPayload struct {
	SessionID string `json:"sessionId"`
	ProfileID string `json:"profileId"`
	Host      string `json:"host"`
	Port      uint16 `json:"port"`
	Username  string `json:"username"`
	Operation string `json:"operation"`
	ID        string `json:"id"`
	Enabled   bool   `json:"enabled"`
}

var sshHistoryMu sync.Mutex

// Conservative filtering is intentional. It cannot identify every possible secret.
var historySecret = regexp.MustCompile(`(?i)(password|passwd|passphrase|token|secret|api[_-]?key|authorization|private[_ -]?key|credential|://[^ /]+:[^ /]+@|\s-[pP]\s*\S|\s-a\s+\S)`)

func historyHash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func historyKey(p sshHistoryPayload) string {
	b, _ := json.Marshal([]any{p.ProfileID, strings.ToLower(strings.TrimSpace(p.Host)), p.Port, strings.TrimSpace(p.Username)})
	return historyHash(string(b))
}
func safeHistoryCommand(s string) bool {
	if s == "" || len(s) > 4096 || !utf8.ValidString(s) || strings.TrimSpace(s) != s || historySecret.MatchString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) {
			return false
		}
	}
	return true
}
func mergeSSHHistory(data sshHistoryData, commands []string) sshHistoryData {
	old := map[string]sshHistoryEntry{}
	for _, e := range data.Entries {
		old[e.ID] = e
	}
	entries := []sshHistoryEntry{}
	seen := map[string]bool{}
	for _, command := range commands {
		id := historyHash(command)
		e, ok := old[id]
		if !ok {
			e = sshHistoryEntry{ID: id, Command: command, ImportedAt: time.Now().UTC().Format(time.RFC3339)}
		}
		if !seen[id] {
			entries = append(entries, e)
			seen[id] = true
		}
	}
	for _, e := range data.Entries {
		if !seen[e.ID] {
			entries = append(entries, e)
		}
	}
	if len(entries) > 5000 {
		entries = entries[:5000]
	}
	data.Entries = entries
	return data
}
func readSSHHistory(path string) (sshHistoryData, error) {
	data := sshHistoryData{Entries: []sshHistoryEntry{}, Enabled: true}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return data, nil
	}
	if err != nil {
		return data, err
	}
	err = json.Unmarshal(b, &data)
	if data.Entries == nil {
		data.Entries = []sshHistoryEntry{}
	}
	return data, err
}
func writeSSHHistory(path string, data sshHistoryData) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".history-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
func init() {
	for _, action := range []string{"list", "update"} {
		action := action
		core.Register("ssh_history_"+action, func(_, args string) (string, error) { return handleSSHHistory(action, args) })
	}
}
func handleSSHHistory(action, args string) (string, error) {
	if action != "list" && action != "update" {
		return "", fmt.Errorf("未知历史操作")
	}
	var p sshHistoryPayload
	if err := core.DecodeArgsPayload(args, &p); err != nil {
		return "", err
	}
	if p.ProfileID == "" || p.Host == "" || p.Username == "" || p.Port == 0 {
		return "", fmt.Errorf("缺少服务器标识")
	}
	sshHistoryMu.Lock()
	defer sshHistoryMu.Unlock()
	path, err := sshHistoryPath(core.UserDataDir(), p)
	if err != nil {
		return "", err
	}
	data, err := readSSHHistory(path)
	if err != nil {
		return "", fmt.Errorf("读取本地历史失败: %w", err)
	}
	switch action {
	case "update":
		switch p.Operation {
		case "clear":
			data.Entries = []sshHistoryEntry{}
		case "setEnabled":
			data.Enabled = p.Enabled
		case "delete", "toggleFavorite":
			found := false
			for i := range data.Entries {
				if data.Entries[i].ID == p.ID {
					found = true
					if p.Operation == "delete" {
						data.Entries = append(data.Entries[:i], data.Entries[i+1:]...)
					} else {
						data.Entries[i].Favorite = !data.Entries[i].Favorite
					}
					break
				}
			}
			if !found {
				return "", fmt.Errorf("历史命令不存在")
			}
		default:
			return "", fmt.Errorf("未知历史操作")
		}
	}
	if action != "list" {
		if err := writeSSHHistory(path, data); err != nil {
			return "", fmt.Errorf("保存本地历史失败: %w", err)
		}
	}
	return core.EncodeResult(data)
}

// Use the application's namespace, not the legacy name which may be a file.
// Preserve old data when its directory is usable; never remove a conflicting file.
func sshHistoryPath(root string, p sshHistoryPayload) (string, error) {
	if root == "" {
		return "", fmt.Errorf("无法确定用户数据目录")
	}
	path := filepath.Join(root, "com.tangoui.mincode", "ssh-command-history", historyKey(p)+".json")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		legacyRoot := filepath.Join(root, "mincode")
		if info, err := os.Stat(legacyRoot); err == nil && info.IsDir() {
			old := filepath.Join(legacyRoot, "ssh-command-history", historyKey(p)+".json")
			if _, err := os.Stat(old); err == nil {
				data, err := readSSHHistory(old)
				if err != nil {
					return "", err
				}
				if err := writeSSHHistory(path, data); err != nil {
					return "", err
				}
			}
		}
	}
	return path, nil
}
