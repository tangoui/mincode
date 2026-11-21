package plugin

import (
	"io/fs"
	"os"
	"path/filepath"
)

// Source-directory installs ignore development dependencies and caches that are
// never part of a runnable plugin package. Runtime and UI output directories
// such as bin, ui, dist, and build are deliberately not ignored.
var pluginInstallIgnoredDirectories = map[string]struct{}{
	".cache":        {},
	".git":          {},
	".hg":           {},
	".mincode":      {},
	".npm":          {},
	".parcel-cache": {},
	".pnpm-store":   {},
	".svn":          {},
	".turbo":        {},
	".venv":         {},
	".vite":         {},
	".yarn":         {},
	"__pycache__":   {},
	"coverage":      {},
	"node_modules":  {},
	"venv":          {},
}

// These directories are source/build inputs, not files needed by an already
// built plugin. They are ignored only when the selected directory is clearly a
// source workspace that also contains a complete built ui/index.html.
var pluginInstallSourceDirectories = map[string]struct{}{
	".github": {},
	"backend": {},
	"docs":    {},
	"editor":  {},
	"public":  {},
	"scripts": {},
	"src":     {},
	"test":    {},
	"tests":   {},
	"ui-src":  {},
}

type pluginInstallFilter struct {
	source                string
	skipSourceDirectories bool
}

func newPluginInstallFilter(source string) pluginInstallFilter {
	return pluginInstallFilter{
		source:                filepath.Clean(source),
		skipSourceDirectories: isBuiltPluginSourceWorkspace(source),
	}
}

func isBuiltPluginSourceWorkspace(source string) bool {
	if !isRegularFile(filepath.Join(source, "ui", "index.html")) || !isRegularFile(filepath.Join(source, "package.json")) {
		return false
	}
	for name := range pluginInstallSourceDirectories {
		if info, err := os.Stat(filepath.Join(source, name)); err == nil && info.IsDir() {
			return true
		}
	}
	return false
}

func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func (filter pluginInstallFilter) skip(path string, entry fs.DirEntry) bool {
	if !entry.IsDir() || entry.Type()&fs.ModeSymlink != 0 {
		return false
	}
	name := filepath.Base(entry.Name())
	if _, ignored := pluginInstallIgnoredDirectories[name]; ignored {
		return true
	}
	if !filter.skipSourceDirectories || filepath.Dir(filepath.Clean(path)) != filter.source {
		return false
	}
	_, ignored := pluginInstallSourceDirectories[name]
	return ignored
}
