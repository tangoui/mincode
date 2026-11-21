//go:build !windows

package git

import (
	"context"
	"os/exec"
	"time"

	"mincode/app/backend/core"
)

type pluginStatusUnixProcess struct{ cmd *exec.Cmd }

func startPluginStatusProcess(ctx context.Context, cmd *exec.Cmd) (pluginStatusProcess, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	core.PrepareProcessTree(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &pluginStatusUnixProcess{cmd: cmd}, nil
}
func (process *pluginStatusUnixProcess) terminate(time.Time) error {
	// Kill the group even when its leader has already exited. A successful
	// cmd.Wait does not prove that descendants released inherited pipes.
	return core.KillProcessTree(process.cmd)
}
func (*pluginStatusUnixProcess) close() {}
