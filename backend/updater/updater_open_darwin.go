//go:build darwin

package updater

import "os/exec"

func openUpdatePackage(path string) error {
	return exec.Command("open", path).Start()
}
