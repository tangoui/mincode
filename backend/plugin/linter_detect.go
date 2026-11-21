package plugin

import (
	"os"
	"path/filepath"
	"strings"

	"mincode/app/backend/core"
)

// plugin_host_linters_detect reports which contributed linters' detect.files
// exist in the active workspace's project root, so the frontend can ask the
// user once whether to use a linter the project is set up for. Detection never
// runs a linter, never reads file contents and only returns linter refs.
//
// The project root and its first-level subdirectories are checked (projects
// often keep the front end in web/ or vue-app/). Hidden directories, symlinks
// and dependency or build output directories are not entered, and a nested
// detect path never passes through a symlinked directory.

const linterDetectSubdirsMax = 256

var linterDetectSkippedDirs = map[string]bool{
	"node_modules": true, "vendor": true, "dist": true, "build": true, "target": true, "out": true,
}

type linterDetectCandidate struct {
	Ref   string
	Files []string
}

type linterDetectResult struct {
	Root    string   `json:"root"`
	Linters []string `json:"linters"`
}

// detectLinters checks the linters of enabled plugins effective in the active
// workspace against the announced project root.
func (manager *pluginManager) detectLinters() linterDetectResult {
	root := manager.workspaceRootPath()
	result := linterDetectResult{Root: root, Linters: []string{}}
	if root == "" {
		return result
	}
	manager.mu.RLock()
	records := manager.orderedRecordsLocked()
	manager.mu.RUnlock()
	var candidates []linterDetectCandidate
	for _, record := range records {
		record.mu.Lock()
		if record.contributionsListedLocked() && manager.workspaceActiveLocked(record) {
			if resolved := record.manifest.contribution(linterPointKey); resolved != nil {
				for _, entry := range resolved.Entries {
					if linter, ok := entry.(LinterContribution); ok && linter.Detect != nil {
						candidates = append(candidates, linterDetectCandidate{Ref: record.id + "/" + linter.ID, Files: linter.Detect.Files})
					}
				}
			}
		}
		record.mu.Unlock()
	}
	result.Linters = detectLinterProjectFiles(root, candidates)
	return result
}

// detectLinterProjectFiles returns, in order, the refs of candidates with at
// least one detect path present in root or a scanned subdirectory.
func detectLinterProjectFiles(root string, candidates []linterDetectCandidate) []string {
	found := []string{}
	if root == "" || len(candidates) == 0 {
		return found
	}
	dirs := linterDetectDirs(root)
	names := make([]map[string]os.DirEntry, len(dirs))
	for index, dir := range dirs {
		names[index] = linterDirEntries(dir)
	}
	for _, candidate := range candidates {
	search:
		for _, file := range candidate.Files {
			for index, dir := range dirs {
				if linterDetectPathExists(dir, names[index], file) {
					found = append(found, candidate.Ref)
					break search
				}
			}
		}
	}
	return found
}

// linterDetectDirs returns root followed by at most linterDetectSubdirsMax
// first-level subdirectories in name order.
func linterDetectDirs(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	dirs := []string{root}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || strings.HasPrefix(name, ".") || linterDetectSkippedDirs[name] {
			continue
		}
		if len(dirs) > linterDetectSubdirsMax {
			break
		}
		dirs = append(dirs, filepath.Join(root, name))
	}
	return dirs
}

func linterDirEntries(dir string) map[string]os.DirEntry {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := make(map[string]os.DirEntry, len(entries))
	for _, entry := range entries {
		names[entry.Name()] = entry
	}
	return names
}

// linterDetectPathExists checks a slash-separated detect path below dir
// without following symlinked directories on the way.
func linterDetectPathExists(dir string, names map[string]os.DirEntry, file string) bool {
	segments := strings.Split(file, "/")
	first, ok := names[segments[0]]
	if !ok {
		return false
	}
	if len(segments) == 1 {
		return true
	}
	if !first.IsDir() || first.Type()&os.ModeSymlink != 0 {
		return false
	}
	current := filepath.Join(dir, segments[0])
	for index, segment := range segments[1:] {
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if err != nil {
			return false
		}
		last := index == len(segments)-2
		if !last && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			return false
		}
	}
	return true
}

func pluginHostLintersDetectHandler(_, _ string) (string, error) {
	return core.EncodeResult(host().detectLinters())
}

func init() {
	core.Register("plugin_host_linters_detect", pluginHostLintersDetectHandler)
}
