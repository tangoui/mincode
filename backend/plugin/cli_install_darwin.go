package plugin

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// darwinCLILink is where macOS users opt in to the `mincode` command.
const darwinCLILink = "/usr/local/bin/mincode"

func platformCLIStatus(cli string) mincodeCLIStatus {
	status := mincodeCLIStatus{Installable: true, LinkPath: darwinCLILink}
	status.State, status.ConflictTarget = cliLinkState(darwinCLILink, cli)
	if status.State == "conflict" && isOwnedCLILink(darwinCLILink, cli) {
		// A link left by a moved or older MinCode copy is repaired without asking.
		status.State, status.ConflictTarget = "missing", ""
	}
	return status
}

func platformInstallCLI(cli string, replace bool) error {
	if strings.HasPrefix(cli, "/Volumes/") || strings.Contains(cli, "/AppTranslocation/") {
		return errors.New("请先把 MinCode 拖到「应用程序」文件夹并从那里打开，再安装 mincode 命令")
	}
	if state, _ := cliLinkState(darwinCLILink, cli); state == "installed" {
		return nil
	} else if state == "conflict" && !replace && !isOwnedCLILink(darwinCLILink, cli) {
		return fmt.Errorf("%s 已存在且不是 MinCode 的命令", darwinCLILink)
	}
	if info, err := os.Lstat(darwinCLILink); err == nil && info.IsDir() {
		return fmt.Errorf("%s 是一个目录，无法替换", darwinCLILink)
	}
	err := os.MkdirAll(filepath.Dir(darwinCLILink), 0o755)
	if err == nil {
		err = os.Remove(darwinCLILink)
		if errors.Is(err, fs.ErrNotExist) {
			err = nil
		}
	}
	if err == nil {
		err = os.Symlink(cli, darwinCLILink)
	}
	if errors.Is(err, fs.ErrPermission) {
		return runPrivilegedShell(
			`"/bin/mkdir -p /usr/local/bin && /bin/ln -sfn " & quoted form of (item 1 of argv) & " /usr/local/bin/mincode"`,
			"MinCode 需要管理员权限，把 mincode 命令安装到 /usr/local/bin。",
			cli,
		)
	}
	return err
}

func platformUninstallCLI(cli string) error {
	if _, err := os.Lstat(darwinCLILink); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if !isOwnedCLILink(darwinCLILink, cli) {
		return fmt.Errorf("%s 不是 MinCode 安装的命令，未删除", darwinCLILink)
	}
	err := os.Remove(darwinCLILink)
	if errors.Is(err, fs.ErrPermission) {
		return runPrivilegedShell(`"/bin/rm -f /usr/local/bin/mincode"`, "MinCode 需要管理员权限，移除 /usr/local/bin/mincode。")
	}
	return err
}

// runPrivilegedShell asks macOS for an administrator password and runs an
// AppleScript shell expression. Paths are passed as argv and quoted by
// AppleScript, never spliced into the script source.
func runPrivilegedShell(shellExpression, prompt string, argv ...string) error {
	script := []string{
		"-e", "on run argv",
		"-e", fmt.Sprintf("do shell script %s with prompt %q with administrator privileges", shellExpression, prompt),
		"-e", "end run",
	}
	output, err := exec.Command("/usr/bin/osascript", append(script, argv...)...).CombinedOutput()
	if err == nil {
		return nil
	}
	if strings.Contains(string(output), "-128") {
		return errors.New("已取消")
	}
	return fmt.Errorf("需要管理员权限的操作失败：%s", strings.TrimSpace(string(output)))
}
