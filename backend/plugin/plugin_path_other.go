//go:build !windows

package plugin

import (
	"io/fs"
	"path/filepath"
)

// pluginPathCanonicalNeeded: without links in any component the checked path
// is already what EvalSymlinks would return.
const pluginPathCanonicalNeeded = false

// pluginPathIsLink reports whether an Lstat result is a symlink.
func pluginPathIsLink(info fs.FileInfo) bool {
	return info.Mode()&fs.ModeSymlink != 0
}

// pluginRealPath returns path with every symlink followed.
func pluginRealPath(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}
