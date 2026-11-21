//go:build linux

package sessions

import (
	"bytes"
	"os"
	"path/filepath"
)

// platformLiveCwds 遍历 /proc，将正在运行的 AI CLI 进程映射到其工作目录。
func platformLiveCwds(want map[string]struct{}) map[string]struct{} {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	cwds := make(map[string]struct{})
	for _, entry := range entries {
		if !entry.IsDir() || !isProcPid(entry.Name()) {
			continue
		}
		cmdline, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err != nil || len(cmdline) == 0 {
			continue
		}
		argv0 := cmdline
		if idx := bytes.IndexByte(cmdline, 0); idx >= 0 {
			argv0 = cmdline[:idx]
		}
		if _, ok := want[filepath.Base(string(argv0))]; !ok {
			continue
		}
		if cwd, err := os.Readlink(filepath.Join("/proc", entry.Name(), "cwd")); err == nil && cwd != "" {
			cwds[cwd] = struct{}{}
		}
	}
	return cwds
}

func isProcPid(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
