package lsp

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mincode/app/backend/core"
)

const (
	rServerProbeMarker     = "MINCODE_R_LANGUAGESERVER_READY"
	rServerProbeExpression = "if (requireNamespace('languageserver', quietly = TRUE)) cat('" + rServerProbeMarker + "\\n')"
	rServerProbeTTL        = 5 * time.Second
	rServerProbeTimeout    = 3 * time.Second
)

type rServerProbeEntry struct {
	installed bool
	at        time.Time
}

var (
	rServerProbeMu    sync.Mutex
	rServerProbeCache = map[string]rServerProbeEntry{}
	rServerProbeRun   = runRServerProbe
)

func runRServerProbe(ctx context.Context, executable string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	core.PrepareBackgroundCommand(cmd)
	cmd.Env = core.ExpandedEnv()
	var output limitedBuffer
	cmd.Stdout = &output
	core.PrepareProcessTree(cmd)
	cmd.Cancel = func() error { return core.KillProcessTree(cmd) }
	cmd.WaitDelay = 100 * time.Millisecond
	err := cmd.Run()
	return []byte(output.String()), err
}

// R 本体存在不代表已安装 languageserver；禁用启动配置，仅执行固定的包探测表达式。
func rServerInstalled(executable string) bool {
	if executable == "" {
		return false
	}
	absolute, err := filepath.Abs(executable)
	if err != nil {
		return false
	}
	rServerProbeMu.Lock()
	defer rServerProbeMu.Unlock()
	if entry, ok := rServerProbeCache[absolute]; ok && time.Since(entry.at) < rServerProbeTTL {
		return entry.installed
	}
	ctx, cancel := context.WithTimeout(context.Background(), rServerProbeTimeout)
	defer cancel()
	output, runErr := rServerProbeRun(ctx, absolute, "--vanilla", "--slave", "-e", rServerProbeExpression)
	installed := runErr == nil && ctx.Err() == nil && rServerProbeSucceeded(output)
	rServerProbeCache[absolute] = rServerProbeEntry{installed: installed, at: time.Now()}
	return installed
}

func rServerProbeSucceeded(output []byte) bool {
	for _, line := range strings.Split(string(output), "\n") {
		if strings.TrimSpace(line) == rServerProbeMarker {
			return true
		}
	}
	return false
}

func invalidateRServerProbe() {
	// 与探测共用锁，避免安装后的失效被安装前尚未完成的探测覆盖。
	rServerProbeMu.Lock()
	clear(rServerProbeCache)
	rServerProbeMu.Unlock()
}
