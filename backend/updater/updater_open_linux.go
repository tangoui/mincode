//go:build linux

package updater

import "os/exec"

func openUpdatePackage(path string) error {
	return exec.Command("xdg-open", path).Start()
}
