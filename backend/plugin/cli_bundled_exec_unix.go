//go:build !windows

package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

// execCLI replaces the process with the CLI; tests replace it.
var execCLI = syscall.Exec

// RunBundledCLI replaces this process with the CLI shipped next to the desktop
// binary, passing args through. It returns only on failure, with exit code 1
// after writing the reason to stderr.
func RunBundledCLI(args []string) int {
	cli := bundledMincodeCLI(bundledCLIExecutable(), runtime.GOOS)
	if cli == "" {
		fmt.Fprintln(os.Stderr, "mincode: 当前安装包不包含 mincode 命令，请从官网下载最新版 MinCode")
		return 1
	}
	appDir := os.Getenv("APPDIR")
	if appDir != "" {
		// AppRun runs the app from $APPDIR/usr/lib; the AppImage runtime keeps the
		// caller's directory in OWD.
		if owd := os.Getenv("OWD"); owd != "" {
			if err := os.Chdir(owd); err != nil {
				fmt.Fprintf(os.Stderr, "mincode: 无法回到启动目录 %s：%v\n", owd, err)
				return 1
			}
		}
	}
	argv := append([]string{cli}, args...)
	err := execCLI(cli, argv, bundledCLIEnvironment(os.Environ()))
	fmt.Fprintf(os.Stderr, "mincode: 启动 %s 失败：%v\n", cli, err)
	return 1
}

// bundledCLIEnvironment undoes what AppRun set up for the desktop app when env
// comes from an AppImage (APPDIR set): search path entries inside the mount and
// the WebKit/GSettings variables pointing into it are dropped, so the CLI and the
// tools it runs (go, node, git) use the user's own PATH and system libraries.
// MINCODE_DESKTOP is set to the AppImage file (unless already set) so a bare
// `mincode` starts the AppImage instead of the binary inside this mount, which
// disappears when this process exits. Outside an AppImage env is returned unchanged.
func bundledCLIEnvironment(env []string) []string {
	appDir, appImage, desktopSet := "", "", false
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		switch key {
		case "APPDIR":
			appDir = value
		case "APPIMAGE":
			appImage = value
		case "MINCODE_DESKTOP":
			desktopSet = value != ""
		}
	}
	if appDir == "" {
		return env
	}
	appDir = filepath.Clean(appDir)
	inside := func(path string) bool {
		path = filepath.Clean(path)
		return path == appDir || strings.HasPrefix(path, appDir+"/")
	}
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			out = append(out, entry)
			continue
		}
		switch key {
		case "PATH", "LD_LIBRARY_PATH", "XDG_DATA_DIRS":
			var kept []string
			for _, dir := range strings.Split(value, ":") {
				if dir != "" && !inside(dir) {
					kept = append(kept, dir)
				}
			}
			if len(kept) == 0 {
				continue
			}
			entry = key + "=" + strings.Join(kept, ":")
		case "GSETTINGS_SCHEMA_DIR", "WEBKIT_INJECTED_BUNDLE_PATH":
			if inside(value) {
				continue
			}
		case "MINCODE_DESKTOP":
			if value == "" {
				continue
			}
		}
		out = append(out, entry)
	}
	if appImage != "" && !desktopSet {
		out = append(out, "MINCODE_DESKTOP="+appImage)
	}
	return out
}
