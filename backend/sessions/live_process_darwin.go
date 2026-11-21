//go:build darwin

package sessions

import (
	"mincode/app/backend/core"

	"path/filepath"
	"strings"
	"unicode"
)

// platformLiveCwds 通过 ps（pid + 完整命令路径）与 lsof（这些 pid 的 cwd）
// 映射正在运行的 AI CLI 进程到其工作目录。best-effort：任何失败都返回空集合。
func platformLiveCwds(want map[string]struct{}) map[string]struct{} {
	psCmd, cancelPs := core.CommandWithTimeout(core.ShortCommandTimeout, "ps", "-axo", "pid=,comm=")
	defer cancelPs()
	psOut, err := psCmd.Output()
	if err != nil {
		return nil
	}
	pids := matchingProcessIDs(string(psOut), want)
	if len(pids) == 0 {
		return nil
	}
	lsofCmd, cancelLsof := core.CommandWithTimeout(core.ShortCommandTimeout, "lsof", "-a", "-p", strings.Join(pids, ","), "-d", "cwd", "-F", "pn")
	defer cancelLsof()
	lsofOut, err := lsofCmd.Output()
	if err != nil || len(lsofOut) == 0 {
		return nil
	}
	cwds := make(map[string]struct{}, len(pids))
	sawPid := false
	for _, line := range strings.Split(string(lsofOut), "\n") {
		if strings.HasPrefix(line, "p") {
			sawPid = true
		} else if sawPid && strings.HasPrefix(line, "n") {
			cwds[strings.TrimPrefix(line, "n")] = struct{}{}
			sawPid = false
		}
	}
	return cwds
}

func matchingProcessIDs(output string, want map[string]struct{}) []string {
	pids := make([]string, 0, 8)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		separator := strings.IndexFunc(line, unicode.IsSpace)
		if separator < 0 {
			continue
		}
		pid := strings.TrimSpace(line[:separator])
		command := strings.TrimSpace(line[separator:])
		if pid == "" || command == "" {
			continue
		}
		name := filepath.Base(command)
		if _, ok := want[name]; ok {
			pids = append(pids, pid)
		}
	}
	return pids
}
