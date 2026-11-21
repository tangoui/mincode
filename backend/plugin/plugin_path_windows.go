//go:build windows

package plugin

import (
	"fmt"
	"io/fs"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// pluginPathCanonicalNeeded: Windows paths accepted by the Lstat walk keep the
// caller's case and short (8.3) names; resolvePluginPath canonicalizes them.
const pluginPathCanonicalNeeded = true

// pluginPathIsLink reports whether an Lstat result is a symlink or any other
// reparse point. Junctions (mount points), cloud placeholders and other
// reparse points are not reported as fs.ModeSymlink by recent Go releases,
// so the raw FILE_ATTRIBUTE_REPARSE_POINT attribute is checked as well.
func pluginPathIsLink(info fs.FileInfo) bool {
	if info.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
		return true
	}
	if data, ok := info.Sys().(*syscall.Win32FileAttributeData); ok && data != nil {
		return data.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
	}
	return false
}

// pluginRealPath returns the final path of path with every symlink and
// junction followed, as the file system resolves it when the file is opened.
// filepath.EvalSymlinks is not enough on Windows: since Go 1.23 it neither
// follows junctions (it returns a junction leaf unchanged) nor resolves paths
// below one. Non-surrogate reparse points (OneDrive placeholders, Dedup files)
// resolve to themselves. The path must exist.
func pluginRealPath(path string) (string, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	handle, err := windows.CreateFile(name, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", &fs.PathError{Op: "open", Path: path, Err: err}
	}
	defer windows.CloseHandle(handle)
	buffer := make([]uint16, windows.MAX_PATH)
	for {
		// flags 0 = FILE_NAME_NORMALIZED | VOLUME_NAME_DOS.
		n, err := windows.GetFinalPathNameByHandle(handle, &buffer[0], uint32(len(buffer)), 0)
		if err != nil {
			return "", &fs.PathError{Op: "GetFinalPathNameByHandle", Path: path, Err: err}
		}
		if n < uint32(len(buffer)) {
			return trimFinalPathPrefix(windows.UTF16ToString(buffer[:n])), nil
		}
		if n > 32768 {
			return "", fmt.Errorf("final path of %s is too long", path)
		}
		buffer = make([]uint16, n)
	}
}

// trimFinalPathPrefix turns \\?\C:\x into C:\x and \\?\UNC\host\share into
// \\host\share, the spelling filepath.EvalSymlinks and filepath.Abs use.
func trimFinalPathPrefix(path string) string {
	if rest, ok := strings.CutPrefix(path, `\\?\UNC\`); ok {
		return `\\` + rest
	}
	return strings.TrimPrefix(path, `\\?\`)
}
