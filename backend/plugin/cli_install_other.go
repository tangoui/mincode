//go:build !darwin && !linux && !windows

package plugin

import (
	"errors"
	"os/exec"
)

// Other platforms have no MinCode-managed install location; report whether
// the bundled command is already the one on PATH.
func platformCLIStatus(cli string) mincodeCLIStatus {
	status := mincodeCLIStatus{State: "missing"}
	if onPath, err := exec.LookPath("mincode"); err == nil && sameFile(onPath, cli) {
		status.State = "installed"
	}
	return status
}

func platformInstallCLI(string, bool) error {
	return errors.New("mincode 命令由安装程序加入 PATH；如果终端找不到它，请重新打开终端或重新安装 MinCode")
}

func platformUninstallCLI(string) error {
	return errors.New("mincode 命令由安装程序管理，卸载 MinCode 时会一并移除")
}
