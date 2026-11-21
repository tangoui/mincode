package sshauth

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

type KnownHostEntry struct {
	ID          string   `json:"id"`
	Hosts       []string `json:"hosts"`
	Marker      string   `json:"marker,omitempty"`
	Algorithm   string   `json:"algorithm"`
	Fingerprint string   `json:"fingerprint"`
	Line        int      `json:"line"`
}

func KnownHostsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	return filepath.Join(home, ".ssh", "known_hosts"), nil
}

func ListKnownHosts() (string, []KnownHostEntry, error) {
	knownHostsMu.Lock()
	defer knownHostsMu.Unlock()
	path, err := KnownHostsPath()
	if err != nil {
		return "", nil, err
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return path, []KnownHostEntry{}, nil
	}
	if err != nil {
		return "", nil, fmt.Errorf("read known_hosts: %w", err)
	}
	entries := make([]KnownHostEntry, 0)
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		marker, hosts, key, _, _, parseErr := ssh.ParseKnownHosts([]byte(line + "\n"))
		if parseErr != nil || key == nil {
			continue
		}
		entries = append(entries, KnownHostEntry{
			ID: knownHostEntryID(path, lineNo, line), Hosts: hosts, Marker: marker,
			Algorithm: key.Type(), Fingerprint: ssh.FingerprintSHA256(key), Line: lineNo,
		})
	}
	if err := scanner.Err(); err != nil {
		return "", nil, fmt.Errorf("scan known_hosts: %w", err)
	}
	return path, entries, nil
}

func RemoveKnownHost(id string) error {
	knownHostsMu.Lock()
	defer knownHostsMu.Unlock()
	path, err := KnownHostsPath()
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read known_hosts: %w", err)
	}
	if info, statErr := os.Lstat(path); statErr != nil {
		return fmt.Errorf("stat known_hosts: %w", statErr)
	} else if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to rewrite symlinked known_hosts")
	}
	lines := strings.SplitAfter(string(raw), "\n")
	kept := make([]string, 0, len(lines))
	removed := false
	for index, line := range lines {
		plain := strings.TrimSuffix(line, "\n")
		plain = strings.TrimSuffix(plain, "\r")
		if strings.TrimSpace(plain) != "" && knownHostEntryID(path, index+1, plain) == id {
			removed = true
			continue
		}
		kept = append(kept, line)
	}
	if !removed {
		return fmt.Errorf("known_hosts entry not found")
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat known_hosts: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".known_hosts-*")
	if err != nil {
		return fmt.Errorf("create known_hosts temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod known_hosts temp file: %w", err)
	}
	if _, err := tmp.WriteString(strings.Join(kept, "")); err != nil {
		tmp.Close()
		return fmt.Errorf("write known_hosts temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync known_hosts temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close known_hosts temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace known_hosts: %w", err)
	}
	return nil
}

func knownHostEntryID(path string, lineNo int, line string) string {
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%s", path, lineNo, line)))
	return hex.EncodeToString(hash[:16])
}
