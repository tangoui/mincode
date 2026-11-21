package plugin

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"mincode/app/backend/core"
)

type mincodeCLIStatus struct {
	Platform       string `json:"platform"`
	Path           string `json:"path,omitempty"`
	State          string `json:"state"` // installed | missing | conflict | unavailable
	Installable    bool   `json:"installable"`
	LinkPath       string `json:"linkPath,omitempty"`
	ConflictTarget string `json:"conflictTarget,omitempty"`
	// PathHint is the directory holding the command link when it is not on the
	// login PATH, so the UI can tell the user how to add it.
	PathHint string `json:"pathHint,omitempty"`
}

func init() {
	core.Register("plugin_host_cli_status", func(_, _ string) (string, error) {
		return core.EncodeResult(mincodeCLIStatusNow())
	})
	core.Register("plugin_host_cli_install", func(_, argsJSON string) (string, error) {
		var args struct {
			Replace bool `json:"replace"`
		}
		if err := core.DecodeArgs(argsJSON, &args); err != nil {
			return "", err
		}
		if err := installMincodeCLI(args.Replace); err != nil {
			return "", err
		}
		return core.EncodeResult(mincodeCLIStatusNow())
	})
	core.Register("plugin_host_cli_uninstall", func(_, _ string) (string, error) {
		if err := uninstallMincodeCLI(); err != nil {
			return "", err
		}
		return core.EncodeResult(mincodeCLIStatusNow())
	})
}

// bundledCLIFlag hands the rest of the command line to the bundled mincode CLI.
// The AppImage command wrapper (appImageWrapperScript) runs `<AppImage> --mincode-cli …`
// because the CLI inside the AppImage mount has no stable path.
const bundledCLIFlag = "--mincode-cli"

// IsBundledCLIMode reports whether the desktop binary was started as the CLI
// hand-off: the flag must be the first argument.
func IsBundledCLIMode(args []string) bool {
	return len(args) >= 1 && args[0] == bundledCLIFlag
}

// bundledCLIExecutable is the desktop binary RunBundledCLI resolves the CLI
// from; tests replace it.
var bundledCLIExecutable = currentExecutable

func currentExecutable() string {
	executable, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		return resolved
	}
	return executable
}

// bundledMincodeCLI finds the CLI shipped with this desktop build. The desktop
// binary is also named mincode, so it is never returned as its own CLI.
func bundledMincodeCLI(executable, goos string) string {
	if executable == "" {
		return ""
	}
	dir := filepath.Dir(executable)
	name := "mincode"
	if goos == "windows" {
		name += ".exe"
	}
	var candidates []string
	if goos == "darwin" {
		candidates = []string{
			filepath.Join(dir, "..", "Helpers", name),
			// 开发布局：bin/<name>.app/Contents/MacOS/ 下运行，CLI 构建在 bin/cli/。
			filepath.Join(dir, "..", "..", "..", "cli", name),
		}
	} else {
		candidates = []string{filepath.Join(dir, "bin", name), filepath.Join(dir, "cli", name)}
		if goos == "linux" && filepath.Base(dir) == "bin" {
			candidates = append(candidates, filepath.Join(dir, "..", "lib", filepath.Base(executable), name))
		}
	}
	for _, candidate := range candidates {
		if isOtherRegularFile(candidate, executable) {
			return filepath.Clean(candidate)
		}
	}
	return ""
}

func isOtherRegularFile(path, self string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	resolved, err := filepath.EvalSymlinks(path)
	return err == nil && resolved != self
}

func resolveMincodeCLI() (string, error) {
	for _, key := range []string{"MINCODE_CLI", "MINCODE_PLUGIN_DEV_CLI"} {
		if configured := strings.TrimSpace(os.Getenv(key)); configured != "" {
			return configured, nil
		}
	}
	executable := currentExecutable()
	if cli := bundledMincodeCLI(executable, runtime.GOOS); cli != "" {
		return cli, nil
	}
	if candidate, err := exec.LookPath("mincode"); err == nil && isOtherRegularFile(candidate, executable) {
		return candidate, nil
	}
	return "", errors.New("mincode CLI is unavailable; reinstall MinCode or set MINCODE_CLI (development builds: build the mincode CLI into bin/cli/mincode)")
}

func mincodeCLIStatusNow() mincodeCLIStatus {
	cli := bundledMincodeCLI(currentExecutable(), runtime.GOOS)
	if cli == "" {
		return mincodeCLIStatus{Platform: runtime.GOOS, State: "unavailable"}
	}
	status := platformCLIStatus(cli)
	status.Platform, status.Path = runtime.GOOS, cli
	return status
}

func sameFile(a, b string) bool {
	resolvedA, errA := filepath.EvalSymlinks(a)
	resolvedB, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && resolvedA == resolvedB
}

func cliLinkState(link, cli string) (state, conflictTarget string) {
	info, err := os.Lstat(link)
	if errors.Is(err, fs.ErrNotExist) {
		return "missing", ""
	}
	if err != nil {
		return "conflict", link
	}
	if info.Mode()&fs.ModeSymlink == 0 {
		return "conflict", link
	}
	target, _ := os.Readlink(link)
	if sameFile(link, cli) {
		return "installed", ""
	}
	return "conflict", target
}

// isOwnedCLILink reports whether a symlink points at a MinCode app's bundled CLI,
// including one from an app copy that was moved or deleted since.
func isOwnedCLILink(link, cli string) bool {
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&fs.ModeSymlink == 0 {
		return false
	}
	target, err := os.Readlink(link)
	return err == nil && (sameFile(link, cli) || strings.HasSuffix(target, ".app/Contents/Helpers/mincode"))
}

func installMincodeCLI(replace bool) error {
	cli := bundledMincodeCLI(currentExecutable(), runtime.GOOS)
	if cli == "" {
		return errors.New("当前安装包不包含 mincode 命令，请从官网下载最新版 MinCode")
	}
	return platformInstallCLI(cli, replace)
}

// uninstallMincodeCLI also runs when the bundled CLI is gone, so platforms can
// still remove a link left by a moved or deleted MinCode copy.
func uninstallMincodeCLI() error {
	return platformUninstallCLI(bundledMincodeCLI(currentExecutable(), runtime.GOOS))
}
