package sshauth

import (
	"os"
	"path/filepath"
	"strings"
)

var preferredPrivateKeyNames = []string{
	"id_ed25519",
	"id_rsa",
	"id_ecdsa",
	"id_dsa",
}

// PrivateKeyPaths 将私钥文件或目录规范化为按优先级排列的候选私钥文件。
// 目录选择只扫描当前层级，优先尝试常见默认文件名，再尝试其余普通文件。
func PrivateKeyPaths(raw string) []string {
	path := expandUserPath(raw)
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil || strings.TrimSpace(home) == "" {
			return append([]string(nil), preferredPrivateKeyNames[:2]...)
		}
		sshDir := filepath.Join(home, ".ssh")
		return []string{
			filepath.Join(sshDir, preferredPrivateKeyNames[0]),
			filepath.Join(sshDir, preferredPrivateKeyNames[1]),
		}
	}

	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return []string{path}
	}
	return privateKeyPathsInDirectory(path)
}

func expandUserPath(raw string) string {
	path := strings.TrimSpace(raw)
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil || strings.TrimSpace(home) == "" {
			return path
		}
		if path == "~" {
			return home
		}
		return filepath.Join(home, path[2:])
	}
	return path
}

func privateKeyPathsInDirectory(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []string{dir}
	}

	byName := make(map[string]os.DirEntry, len(entries))
	for _, entry := range entries {
		byName[entry.Name()] = entry
	}

	paths := make([]string, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	appendCandidate := func(name string, entry os.DirEntry) {
		if seen[name] || !isPrivateKeyCandidate(dir, name, entry) {
			return
		}
		seen[name] = true
		paths = append(paths, filepath.Join(dir, name))
	}

	for _, name := range preferredPrivateKeyNames {
		if entry, ok := byName[name]; ok {
			appendCandidate(name, entry)
		}
	}
	for _, entry := range entries {
		appendCandidate(entry.Name(), entry)
	}

	if len(paths) == 0 {
		for _, name := range preferredPrivateKeyNames[:2] {
			paths = append(paths, filepath.Join(dir, name))
		}
	}
	return paths
}

func isPrivateKeyCandidate(dir, name string, entry os.DirEntry) bool {
	lower := strings.ToLower(name)
	if strings.HasSuffix(lower, ".pub") || lower == "authorized_keys" || lower == "known_hosts" || lower == "config" {
		return false
	}
	info, err := os.Stat(filepath.Join(dir, name))
	return err == nil && info.Mode().IsRegular() && !entry.IsDir()
}
