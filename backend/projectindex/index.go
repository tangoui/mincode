// Package projectindex provides an in-memory, metadata-only index for files in
// saved local projects. It never reads file contents and never follows symlinks.
package projectindex

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"mincode/app/backend/core"
)

const (
	maxRoots        = 12
	maxFilesPerRoot = 50_000
	maxSearchLimit  = 200

	gitScanTimeout = 60 * time.Second
)

var ignoredDirectories = map[string]struct{}{
	".git": {}, "node_modules": {}, "dist": {}, "build": {}, "vendor": {},
	".cache": {}, "coverage": {}, "target": {},
}

var errFileLimit = errors.New("project index file limit reached")

type FileEntry struct {
	Root       string `json:"root"`
	Path       string `json:"path"`
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	ModifiedAt int64  `json:"modifiedAt"`
}

type RootResult struct {
	Root      string `json:"root"`
	Files     int    `json:"files"`
	Changed   int    `json:"changed"`
	Truncated bool   `json:"truncated"`
}

type RefreshResult struct {
	Roots []RootResult `json:"roots"`
}

type refreshArgs struct {
	Roots []string `json:"roots"`
}

type searchArgs struct {
	Roots []string `json:"roots"`
	Query string   `json:"query"`
	Limit int      `json:"limit"`
}

type fileSearchMetadata struct {
	nameLower string
	pathLower string
}

type rootIndex struct {
	files     map[string]FileEntry
	metadata  map[string]fileSearchMetadata
	truncated bool
}

type indexStore struct {
	mu    sync.RWMutex
	roots map[string]rootIndex
}

var store = indexStore{roots: map[string]rootIndex{}}

func init() {
	core.Register("project_index_refresh", refreshHandler)
	core.Register("project_index_search", searchHandler)
}

func normalizeRoots(roots []string, verify bool) ([]string, error) {
	if len(roots) > maxRoots {
		return nil, fmt.Errorf("too many project roots: maximum is %d", maxRoots)
	}
	seen := make(map[string]struct{}, len(roots))
	result := make([]string, 0, len(roots))
	for _, raw := range roots {
		root := filepath.Clean(strings.TrimSpace(raw))
		if root == "." || root == "" || !filepath.IsAbs(root) {
			return nil, fmt.Errorf("project root must be an absolute path: %q", raw)
		}
		if verify {
			info, err := os.Lstat(root)
			if err != nil {
				return nil, fmt.Errorf("inspect project root %q: %w", root, err)
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return nil, fmt.Errorf("project root is not a real directory: %q", root)
			}
		}
		if _, ok := seen[root]; ok {
			continue
		}
		seen[root] = struct{}{}
		result = append(result, root)
	}
	return result, nil
}

func isSensitivePath(path string) bool {
	parts := strings.Split(strings.ToLower(filepath.ToSlash(path)), "/")
	for _, part := range parts {
		if part == ".env" || strings.HasPrefix(part, ".env.") {
			return true
		}
		switch part {
		case ".npmrc", ".pypirc", ".netrc", "id_rsa", "id_ed25519", "credentials":
			return true
		}
	}
	name := parts[len(parts)-1]
	return strings.HasSuffix(name, ".pem") || strings.HasSuffix(name, ".key") ||
		strings.HasSuffix(name, ".p12") || strings.HasSuffix(name, ".pfx") || strings.HasSuffix(name, ".kdbx")
}

func shouldIndexPath(relative string) bool {
	if isSensitivePath(relative) {
		return false
	}
	for _, part := range strings.Split(filepath.ToSlash(relative), "/") {
		if _, ignored := ignoredDirectories[part]; ignored {
			return false
		}
	}
	return true
}

func fileEntryForPath(root, relative string, info os.FileInfo) FileEntry {
	return FileEntry{
		Root: root, Path: relative, Name: filepath.Base(filepath.FromSlash(relative)),
		Size: info.Size(), ModifiedAt: info.ModTime().UnixMilli(),
	}
}

func scanRootWithGit(root string) (map[string]FileEntry, bool, bool) {
	cmd, cancel := core.CommandWithTimeout(gitScanTimeout, "git", "-C", root, "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	defer cancel()
	output, err := cmd.Output()
	if err != nil {
		return nil, false, false
	}
	files := make(map[string]FileEntry)
	for _, raw := range strings.Split(string(output), "\x00") {
		if raw == "" {
			continue
		}
		relative := filepath.ToSlash(filepath.Clean(raw))
		if relative == "." || relative == ".." || strings.HasPrefix(relative, "../") || !shouldIndexPath(relative) {
			continue
		}
		fullPath := filepath.Join(root, filepath.FromSlash(relative))
		info, err := os.Lstat(fullPath)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			continue
		}
		files[relative] = fileEntryForPath(root, relative, info)
		if len(files) >= maxFilesPerRoot {
			return files, true, true
		}
	}
	return files, false, true
}

func scanRootWithWalk(root string) (map[string]FileEntry, bool, error) {
	files := make(map[string]FileEntry)
	truncated := false
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if _, ignored := ignoredDirectories[entry.Name()]; ignored {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if !shouldIndexPath(relative) {
			return nil
		}
		if len(files) >= maxFilesPerRoot {
			truncated = true
			return errFileLimit
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		files[relative] = fileEntryForPath(root, relative, info)
		return nil
	})
	if err != nil && !errors.Is(err, errFileLimit) {
		return nil, false, fmt.Errorf("scan project root %q: %w", root, err)
	}
	return files, truncated, nil
}

func scanRoot(root string) (map[string]FileEntry, bool, error) {
	if files, truncated, ok := scanRootWithGit(root); ok {
		return files, truncated, nil
	}
	return scanRootWithWalk(root)
}

func makeSearchMetadata(files map[string]FileEntry) map[string]fileSearchMetadata {
	metadata := make(map[string]fileSearchMetadata, len(files))
	for path, entry := range files {
		metadata[path] = fileSearchMetadata{nameLower: strings.ToLower(entry.Name), pathLower: strings.ToLower(entry.Path)}
	}
	return metadata
}
func changedCount(previous, next map[string]FileEntry) int {
	changed := 0
	for path, current := range next {
		old, exists := previous[path]
		if !exists || old.Size != current.Size || old.ModifiedAt != current.ModifiedAt {
			changed++
		}
	}
	for path := range previous {
		if _, exists := next[path]; !exists {
			changed++
		}
	}
	return changed
}

func refreshRoots(roots []string) (RefreshResult, error) {
	normalized, err := normalizeRoots(roots, true)
	if err != nil {
		return RefreshResult{}, err
	}
	scanned := make(map[string]rootIndex, len(normalized))
	for _, root := range normalized {
		files, truncated, err := scanRoot(root)
		if err != nil {
			return RefreshResult{}, err
		}
		scanned[root] = rootIndex{files: files, metadata: makeSearchMetadata(files), truncated: truncated}
	}

	store.mu.Lock()
	results := make([]RootResult, 0, len(normalized))
	for _, root := range normalized {
		next := scanned[root]
		previous := store.roots[root]
		results = append(results, RootResult{
			Root: root, Files: len(next.files), Changed: changedCount(previous.files, next.files),
			Truncated: next.truncated,
		})
	}
	// A refresh is scoped to the requested roots. Keep other indexed projects
	// available so independent workspaces do not disappear between refreshes.
	for root, next := range scanned {
		store.roots[root] = next
	}
	store.mu.Unlock()
	return RefreshResult{Roots: results}, nil
}

func refreshHandler(_ string, argsJSON string) (string, error) {
	var args refreshArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	result, err := refreshRoots(args.Roots)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(result)
}

type rankedEntry struct {
	entry FileEntry
	score int
}

func fuzzyScore(value, query string) (int, bool) {
	if query == "" {
		return 0, true
	}
	// Use a rune scan so ranking is stable for non-ASCII file names.
	wanted := []rune(query)
	last := -1
	gaps := 0
	position := 0
	for index, char := range []rune(value) {
		if position < len(wanted) && char == wanted[position] {
			if last >= 0 {
				gaps += index - last - 1
			}
			last = index
			position++
		}
	}
	if position != len(wanted) {
		return 0, false
	}
	return gaps + max(0, len([]rune(value))-len(wanted)), true
}

func matchScore(metadata fileSearchMetadata, query string) (int, bool) {
	name, path := metadata.nameLower, metadata.pathLower
	if name == query || path == query {
		return 0, true
	}
	if strings.HasPrefix(name, query) {
		return 100 + len(name) - len(query), true
	}
	if strings.HasPrefix(path, query) {
		return 200 + len(path) - len(query), true
	}
	if strings.Contains(name, query) {
		return 300 + strings.Index(name, query), true
	}
	if strings.Contains(path, query) {
		return 400 + strings.Index(path, query), true
	}
	if score, ok := fuzzyScore(name, query); ok {
		return 500 + score, true
	}
	if score, ok := fuzzyScore(path, query); ok {
		return 600 + score, true
	}
	return 0, false
}
func searchFiles(roots []string, query string, limit int) ([]FileEntry, error) {
	normalized, err := normalizeRoots(roots, false)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 80
	}
	if limit > maxSearchLimit {
		limit = maxSearchLimit
	}
	selected := make(map[string]struct{}, len(normalized))
	for _, root := range normalized {
		selected[root] = struct{}{}
	}

	store.mu.RLock()
	entries := make([]struct {
		entry    FileEntry
		metadata fileSearchMetadata
	}, 0)
	for root, index := range store.roots {
		if len(selected) > 0 {
			if _, ok := selected[root]; !ok {
				continue
			}
		}
		for path, entry := range index.files {
			entries = append(entries, struct {
				entry    FileEntry
				metadata fileSearchMetadata
			}{entry, index.metadata[path]})
		}
	}
	store.mu.RUnlock()

	normalizedQuery := strings.ToLower(strings.TrimSpace(query))
	ranked := make([]rankedEntry, 0, len(entries))
	for _, item := range entries {
		score, matched := matchScore(item.metadata, normalizedQuery)
		entry := item.entry
		if normalizedQuery == "" {
			score, matched = 0, true
		}
		if matched {
			ranked = append(ranked, rankedEntry{entry: entry, score: score})
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score < ranked[j].score
		}
		if normalizedQuery != "" && len(ranked[i].entry.Name) != len(ranked[j].entry.Name) {
			return len(ranked[i].entry.Name) < len(ranked[j].entry.Name)
		}
		if ranked[i].entry.ModifiedAt != ranked[j].entry.ModifiedAt {
			return ranked[i].entry.ModifiedAt > ranked[j].entry.ModifiedAt
		}
		if ranked[i].entry.Name != ranked[j].entry.Name {
			return ranked[i].entry.Name < ranked[j].entry.Name
		}
		if ranked[i].entry.Root != ranked[j].entry.Root {
			return ranked[i].entry.Root < ranked[j].entry.Root
		}
		return ranked[i].entry.Path < ranked[j].entry.Path
	})
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	result := make([]FileEntry, len(ranked))
	for index, item := range ranked {
		result[index] = item.entry
	}
	return result, nil
}

func searchHandler(_ string, argsJSON string) (string, error) {
	var args searchArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	result, err := searchFiles(args.Roots, args.Query, args.Limit)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(result)
}
