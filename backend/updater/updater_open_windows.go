//go:build windows

package updater

import "os/exec"

func openUpdatePackage(path string) error {
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", path).Start()
}
