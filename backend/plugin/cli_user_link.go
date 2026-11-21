package plugin

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// User-level `mincode` command links. These helpers carry no build tag so the
// Linux install path (and the Windows PATH list editing) can be tested on any
// development machine; the platform files only wire them to the real system.

// appImageWrapperMarker identifies the shell script MinCode writes when it runs
// as an AppImage: the AppImage mount path changes on every launch, so the
// command re-enters the AppImage instead of linking into the mount.
const appImageWrapperMarker = "# mincode command installed by MinCode (AppImage)"

// linuxCLILinkPath is the per-user command location that needs no root and is
// on PATH by default on most distributions (systemd file-hierarchy).
func linuxCLILinkPath(home string) string {
	return filepath.Join(home, ".local", "bin", "mincode")
}

// appImageWrapperScript execs the AppImage in CLI mode. The path is single-quoted
// for sh; each embedded quote closes the string, adds an escaped quote and reopens it.
func appImageWrapperScript(appImage string) []byte {
	quoted := "'" + strings.ReplaceAll(appImage, "'", `'\''`) + "'"
	return []byte("#!/bin/sh\n" + appImageWrapperMarker + "\nexec " + quoted + " --mincode-cli \"$@\"\n")
}

// isOwnedUserCLILink reports whether link is MinCode's: a symlink to cli, or a
// wrapper script carrying the marker (possibly for an AppImage moved since).
func isOwnedUserCLILink(link, cli string) bool {
	info, err := os.Lstat(link)
	if err != nil {
		return false
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return cli != "" && sameFile(link, cli)
	}
	if !info.Mode().IsRegular() || info.Size() > 4096 {
		return false
	}
	content, err := os.ReadFile(link)
	return err == nil && bytes.Contains(content, []byte("\n"+appImageWrapperMarker+"\n"))
}

// userCLILinkState is cliLinkState for the user link: with appImage set the
// wanted form is the wrapper script, otherwise a symlink to cli. A MinCode link
// in the other form (or for a moved AppImage) is reported missing so it is
// repaired without asking.
func userCLILinkState(link, cli, appImage string) (state, conflictTarget string) {
	info, err := os.Lstat(link)
	if errors.Is(err, fs.ErrNotExist) {
		return "missing", ""
	}
	if err != nil {
		return "conflict", link
	}
	if appImage != "" {
		if info.Mode().IsRegular() {
			if content, err := os.ReadFile(link); err == nil && bytes.Equal(content, appImageWrapperScript(appImage)) {
				return "installed", ""
			}
		}
	} else if info.Mode()&fs.ModeSymlink != 0 && sameFile(link, cli) {
		return "installed", ""
	}
	if isOwnedUserCLILink(link, cli) {
		return "missing", ""
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		if target, err := os.Readlink(link); err == nil {
			return "conflict", target
		}
	}
	return "conflict", link
}

// installUserCLILink points link at cli (or writes the AppImage wrapper when
// appImage is set). The parent directory is created; an existing file that is
// not MinCode's is replaced only with replace. The new entry is staged next to
// link and renamed over it, so a failure never leaves the command half-written.
func installUserCLILink(link, cli, appImage string, replace bool) error {
	state, _ := userCLILinkState(link, cli, appImage)
	if state == "installed" {
		return nil
	}
	if state == "conflict" && !replace {
		return fmt.Errorf("%s 已存在且不是 MinCode 的命令", link)
	}
	if info, err := os.Lstat(link); err == nil && info.IsDir() {
		return fmt.Errorf("%s 是一个目录，无法替换", link)
	}
	dir := filepath.Dir(link)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建 %s 失败：%w", dir, err)
	}
	staged, err := stageUserCLILink(dir, cli, appImage)
	if err != nil {
		return fmt.Errorf("安装 mincode 命令失败：%w", err)
	}
	if err := os.Rename(staged, link); err != nil {
		_ = os.Remove(staged)
		return fmt.Errorf("安装 mincode 命令失败：%w", err)
	}
	return nil
}

func stageUserCLILink(dir, cli, appImage string) (string, error) {
	if appImage == "" {
		staged := filepath.Join(dir, fmt.Sprintf(".mincode-%d.tmp", os.Getpid()))
		_ = os.Remove(staged)
		return staged, os.Symlink(cli, staged)
	}
	file, err := os.CreateTemp(dir, ".mincode-*.tmp")
	if err != nil {
		return "", err
	}
	staged := file.Name()
	_, err = file.Write(appImageWrapperScript(appImage))
	if err == nil {
		err = file.Chmod(0o755)
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(staged)
		return "", err
	}
	return staged, nil
}

// uninstallUserCLILink removes link only when it is MinCode's.
func uninstallUserCLILink(link, cli string) error {
	if _, err := os.Lstat(link); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if !isOwnedUserCLILink(link, cli) {
		return fmt.Errorf("%s 不是 MinCode 安装的命令，未删除", link)
	}
	if err := os.Remove(link); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("移除 %s 失败：%w", link, err)
	}
	return nil
}

// pathListContains reports whether a PATH-style list has dir as an entry.
// Windows entries compare case-insensitively and may be quoted; trailing
// separators are ignored on both families.
func pathListContains(pathEnv, dir, goos string) bool {
	want := normalizePathEntry(dir, goos)
	if want == "" {
		return false
	}
	for _, entry := range strings.Split(pathEnv, pathListSeparator(goos)) {
		if samePathEntry(normalizePathEntry(entry, goos), want, goos) {
			return true
		}
	}
	return false
}

// windowsPathWithDir appends dir to a user Path value unless it is already listed.
func windowsPathWithDir(current, dir string) (string, bool) {
	if pathListContains(current, dir, "windows") {
		return current, false
	}
	trimmed := strings.TrimRight(current, ";")
	if trimmed == "" {
		return dir, true
	}
	return trimmed + ";" + dir, true
}

// windowsPathWithoutDir drops only the entries equal to dir, keeping every other
// entry (including %VARIABLE% references) byte for byte.
func windowsPathWithoutDir(current, dir string) (string, bool) {
	want := normalizePathEntry(dir, "windows")
	entries := strings.Split(current, ";")
	kept := entries[:0:0]
	for _, entry := range entries {
		if want != "" && samePathEntry(normalizePathEntry(entry, "windows"), want, "windows") {
			continue
		}
		kept = append(kept, entry)
	}
	if len(kept) == len(entries) {
		return current, false
	}
	return strings.Join(kept, ";"), true
}

func pathListSeparator(goos string) string {
	if goos == "windows" {
		return ";"
	}
	return ":"
}

func normalizePathEntry(entry, goos string) string {
	entry = strings.TrimSpace(entry)
	if goos == "windows" {
		entry = strings.TrimSpace(strings.Trim(entry, `"`))
		trimmed := strings.TrimRight(entry, `\/`)
		if len(trimmed) == 2 && trimmed[1] == ':' {
			return trimmed + `\` // keep drive roots such as C:\
		}
		return trimmed
	}
	if entry == "" {
		return ""
	}
	return path.Clean(entry)
}

func samePathEntry(a, b, goos string) bool {
	if a == "" || b == "" {
		return false
	}
	if goos == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
