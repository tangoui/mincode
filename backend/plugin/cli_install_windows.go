package plugin

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Windows puts the command's bin directory on PATH instead of linking a file.
// The installer does this for normal installs; portable copies and users whose
// PATH was reset add it to the current user's Path from MinCode.

const (
	userEnvironmentKey    = `Environment`
	machineEnvironmentKey = `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`
)

func platformCLIStatus(cli string) mincodeCLIStatus {
	dir := filepath.Dir(cli)
	status := mincodeCLIStatus{State: "missing", Installable: true, LinkPath: dir}
	if userPath, _, err := readEnvironmentPath(registry.CURRENT_USER, userEnvironmentKey); err == nil && pathListContains(userPath, dir, "windows") {
		status.State = "installed"
		return status
	}
	machinePath, _, err := readEnvironmentPath(registry.LOCAL_MACHINE, machineEnvironmentKey)
	if err != nil {
		// Without the machine key, the PATH MinCode started with is the best signal.
		machinePath = os.Getenv("PATH")
	}
	if pathListContains(machinePath, dir, "windows") {
		// A machine-wide install manages PATH itself; MinCode cannot remove it.
		status.State, status.Installable = "installed", false
	}
	return status
}

func platformInstallCLI(cli string, _ bool) error {
	dir := filepath.Dir(cli)
	return updateUserPath(func(current string) (string, bool) { return windowsPathWithDir(current, dir) })
}

func platformUninstallCLI(cli string) error {
	if cli == "" {
		return errors.New("当前安装包不包含 mincode 命令，无需从 PATH 移除")
	}
	dir := filepath.Dir(cli)
	return updateUserPath(func(current string) (string, bool) { return windowsPathWithoutDir(current, dir) })
}

// readEnvironmentPath returns the raw (unexpanded) Path value and its type;
// a missing value reads as "" with REG_EXPAND_SZ.
func readEnvironmentPath(root registry.Key, keyPath string) (string, uint32, error) {
	key, err := registry.OpenKey(root, keyPath, registry.QUERY_VALUE)
	if err != nil {
		return "", 0, err
	}
	defer key.Close()
	value, valueType, err := key.GetStringValue("Path")
	if errors.Is(err, registry.ErrNotExist) {
		return "", registry.EXPAND_SZ, nil
	}
	return value, valueType, err
}

// updateUserPath rewrites HKCU\Environment\Path keeping the value's type
// (REG_EXPAND_SZ when new, so %VARIABLE% entries keep working) and tells
// running programs that the environment changed.
func updateUserPath(edit func(string) (string, bool)) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, userEnvironmentKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("打开用户环境变量失败：%w", err)
	}
	defer key.Close()
	current, valueType, err := key.GetStringValue("Path")
	if errors.Is(err, registry.ErrNotExist) {
		current, valueType, err = "", registry.EXPAND_SZ, nil
	}
	if err != nil {
		return fmt.Errorf("读取用户 PATH 失败：%w", err)
	}
	next, changed := edit(current)
	if !changed {
		return nil
	}
	if valueType == registry.SZ {
		err = key.SetStringValue("Path", next)
	} else {
		err = key.SetExpandStringValue("Path", next)
	}
	if err != nil {
		return fmt.Errorf("写入用户 PATH 失败：%w", err)
	}
	broadcastEnvironmentChange()
	return nil
}

var procSendMessageTimeoutW = windows.NewLazySystemDLL("user32.dll").NewProc("SendMessageTimeoutW")

// broadcastEnvironmentChange is best effort: Path is already saved, and new
// terminals opened from Explorer pick it up once Explorer re-reads it.
func broadcastEnvironmentChange() {
	const (
		hwndBroadcast   = 0xffff
		wmSettingChange = 0x001a
		smtoAbortIfHung = 0x0002
	)
	environment, err := syscall.UTF16PtrFromString("Environment")
	if err != nil || procSendMessageTimeoutW.Find() != nil {
		return
	}
	var result uintptr
	_, _, _ = procSendMessageTimeoutW.Call(
		hwndBroadcast,
		wmSettingChange,
		0,
		uintptr(unsafe.Pointer(environment)),
		smtoAbortIfHung,
		5000,
		uintptr(unsafe.Pointer(&result)),
	)
}
