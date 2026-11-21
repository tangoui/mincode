package plugin

import (
	"errors"
	"os"
	"path/filepath"
)

// linuxPackageCLILink is where the deb/rpm packages install the command.
const linuxPackageCLILink = "/usr/local/bin/mincode"

func platformCLIStatus(cli string) mincodeCLIStatus {
	if sameFile(linuxPackageCLILink, cli) {
		// The system package owns this link; there is nothing to install or remove.
		return mincodeCLIStatus{State: "installed", LinkPath: linuxPackageCLILink}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return mincodeCLIStatus{State: "missing"}
	}
	link := linuxCLILinkPath(home)
	status := mincodeCLIStatus{Installable: true, LinkPath: link}
	status.State, status.ConflictTarget = userCLILinkState(link, cli, linuxAppImagePath())
	if dir := filepath.Dir(link); !pathListContains(os.Getenv("PATH"), dir, "linux") {
		status.PathHint = dir
	}
	return status
}

func platformInstallCLI(cli string, replace bool) error {
	if sameFile(linuxPackageCLILink, cli) {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return errors.New("找不到当前用户的主目录，无法安装 mincode 命令")
	}
	return installUserCLILink(linuxCLILinkPath(home), cli, linuxAppImagePath(), replace)
}

func platformUninstallCLI(cli string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return errors.New("找不到当前用户的主目录")
	}
	return uninstallUserCLILink(linuxCLILinkPath(home), cli)
}

// linuxAppImagePath is the running AppImage file (set by the AppImage runtime),
// or "" for tarball and package installs.
func linuxAppImagePath() string {
	appImage := os.Getenv("APPIMAGE")
	if appImage == "" || !filepath.IsAbs(appImage) {
		return ""
	}
	if info, err := os.Stat(appImage); err != nil || !info.Mode().IsRegular() {
		return ""
	}
	return appImage
}
