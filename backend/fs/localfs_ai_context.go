package fs

import (
	"fmt"
	"io"
	"mincode/app/backend/core"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxAIContextFileContentBytes = 224 * 1024
	aiContextGitListTimeout      = 30 * time.Second
)

func handleListAIContextTree(_ string, argsJSON string) (string, error) {
	var args struct {
		ProjectRoot string `json:"projectRoot"`
		DirPath     string `json:"dirPath"`
	}
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	result, err := listAIContextTree(args.ProjectRoot, args.DirPath)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(result)
}

func listAIContextTree(projectRoot, dirPath string) (localFsAIContextTreeResult, error) {
	root, err := canonicalLocalProjectRoot(projectRoot)
	if err != nil {
		return localFsAIContextTreeResult{}, err
	}
	dir, err := resolveUnderRoot(root, dirPath)
	if err != nil {
		return localFsAIContextTreeResult{}, err
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return localFsAIContextTreeResult{}, fmt.Errorf("path_is_not_directory")
	}

	if result, ok := listGitAIContextTree(root, dir); ok {
		return result, nil
	}
	return walkAIContextTree(root, dir)
}

func listGitAIContextTree(root, dir string) (localFsAIContextTreeResult, bool) {
	rootCmd, cancelRoot := core.CommandWithTimeout(core.ShortCommandTimeout, "git", "-C", root, "rev-parse", "--show-toplevel")
	defer cancelRoot()
	gitRootOutput, err := rootCmd.Output()
	if err != nil {
		return localFsAIContextTreeResult{}, false
	}
	gitRoot, err := filepath.EvalSymlinks(strings.TrimSpace(string(gitRootOutput)))
	if err != nil || !withinRoot(gitRoot, root) {
		return localFsAIContextTreeResult{}, false
	}

	listCmd, cancelList := core.CommandWithTimeout(
		aiContextGitListTimeout,
		"git", "--literal-pathspecs", "-C", dir, "ls-files", "--cached", "--others",
		"--exclude-standard", "--full-name", "-z", "--", ".",
	)
	defer cancelList()
	output, err := listCmd.Output()
	if err != nil {
		return localFsAIContextTreeResult{}, false
	}

	selectedRelative, err := filepath.Rel(root, dir)
	if err != nil {
		return localFsAIContextTreeResult{}, false
	}
	selectedRelative = filepath.ToSlash(selectedRelative)
	entries := make(map[string]localFsAIContextEntry, maxAIContextTreeEntries)
	truncated := false
	for _, rawPath := range strings.Split(string(output), "\x00") {
		if rawPath == "" {
			continue
		}
		fullPath := filepath.Join(gitRoot, filepath.FromSlash(rawPath))
		relative, err := filepath.Rel(root, fullPath)
		if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || relative == ".." {
			continue
		}
		relative = filepath.ToSlash(relative)
		if shouldSkipAIContextPath(relative) || !isSafeAIContextFile(root, relative) {
			continue
		}
		addAIContextParentEntries(entries, relative, selectedRelative, &truncated)
		addAIContextEntry(entries, localFsAIContextEntry{Path: relative, IsDir: false}, &truncated)
		if truncated {
			break
		}
	}
	return sortedAIContextTreeResult(entries, truncated), true
}

func walkAIContextTree(root, dir string) (localFsAIContextTreeResult, error) {
	entries := make(map[string]localFsAIContextEntry, maxAIContextTreeEntries)
	truncated := false
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == dir {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() && shouldSkipAiTreeDir(entry.Name()) {
			return filepath.SkipDir
		}
		if !entry.IsDir() && !entry.Type().IsRegular() {
			return nil
		}
		if len(entries) >= maxAIContextTreeEntries {
			truncated = true
			return filepath.SkipAll
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		addAIContextEntry(entries, localFsAIContextEntry{
			Path: filepath.ToSlash(relative), IsDir: entry.IsDir(),
		}, &truncated)
		if truncated {
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return localFsAIContextTreeResult{}, fmt.Errorf("list_ai_context_tree: %w", err)
	}
	return sortedAIContextTreeResult(entries, truncated), nil
}

func shouldSkipAIContextPath(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if shouldSkipAiTreeDir(part) {
			return true
		}
	}
	return false
}

func isSafeAIContextFile(root, relative string) bool {
	current := root
	parts := strings.Split(filepath.FromSlash(relative), string(filepath.Separator))
	for index, part := range parts {
		if part == "" || part == "." || part == ".." {
			return false
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
		if index == len(parts)-1 {
			return info.Mode().IsRegular()
		}
		if !info.IsDir() {
			return false
		}
	}
	return false
}

func addAIContextParentEntries(
	entries map[string]localFsAIContextEntry,
	filePath string,
	selectedRelative string,
	truncated *bool,
) {
	for parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(filePath))); parent != "." && parent != selectedRelative; parent = filepath.ToSlash(filepath.Dir(filepath.FromSlash(parent))) {
		addAIContextEntry(entries, localFsAIContextEntry{Path: parent, IsDir: true}, truncated)
		if *truncated {
			return
		}
	}
}

func addAIContextEntry(
	entries map[string]localFsAIContextEntry,
	entry localFsAIContextEntry,
	truncated *bool,
) {
	if _, exists := entries[entry.Path]; exists {
		return
	}
	if len(entries) >= maxAIContextTreeEntries {
		*truncated = true
		return
	}
	entries[entry.Path] = entry
}

func sortedAIContextTreeResult(
	entries map[string]localFsAIContextEntry,
	truncated bool,
) localFsAIContextTreeResult {
	result := make([]localFsAIContextEntry, 0, len(entries))
	for _, entry := range entries {
		result = append(result, entry)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return localFsAIContextTreeResult{Entries: result, Truncated: truncated}
}

func handleReadAIContextTextFile(_ string, argsJSON string) (string, error) {
	var args struct {
		ProjectRoot string `json:"projectRoot"`
		FilePath    string `json:"filePath"`
	}
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	result, err := readAIContextTextFile(args.ProjectRoot, args.FilePath)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(result)
}

func readAIContextTextFile(projectRoot, filePath string) (localFsAIContextFileResult, error) {
	root, err := canonicalLocalProjectRoot(projectRoot)
	if err != nil {
		return localFsAIContextFileResult{}, err
	}
	path, err := resolveUnderRoot(root, filePath)
	if err != nil {
		return localFsAIContextFileResult{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return localFsAIContextFileResult{}, fmt.Errorf("stat: %w", err)
	}
	if !info.Mode().IsRegular() {
		return localFsAIContextFileResult{}, fmt.Errorf("path_is_not_file")
	}

	file, err := os.Open(path)
	if err != nil {
		return localFsAIContextFileResult{}, fmt.Errorf("read: %w", err)
	}
	defer file.Close()

	if info.Size() <= maxAIContextFileContentBytes {
		data, err := io.ReadAll(file)
		if err != nil {
			return localFsAIContextFileResult{}, fmt.Errorf("read: %w", err)
		}
		content := strings.ToValidUTF8(string(data), "�")
		truncated := len(content) > maxAIContextFileContentBytes
		if truncated {
			content = compactAIContextText(content, info.Size())
		}
		return localFsAIContextFileResult{Content: content, Size: info.Size(), Truncated: truncated}, nil
	}

	marker := fmt.Sprintf("\n\n[MinCode: middle of file omitted; original size %d bytes]\n\n", info.Size())
	available := maxAIContextFileContentBytes - len(marker)
	headBudget := available / 2
	tailBudget := available - headBudget

	head := make([]byte, headBudget)
	headCount, readErr := io.ReadFull(file, head)
	if readErr != nil && readErr != io.ErrUnexpectedEOF && readErr != io.EOF {
		return localFsAIContextFileResult{}, fmt.Errorf("read_head: %w", readErr)
	}
	head = head[:headCount]

	if _, err := file.Seek(-int64(tailBudget), io.SeekEnd); err != nil {
		return localFsAIContextFileResult{}, fmt.Errorf("seek_tail: %w", err)
	}
	tail := make([]byte, tailBudget)
	tailCount, readErr := io.ReadFull(file, tail)
	if readErr != nil && readErr != io.ErrUnexpectedEOF && readErr != io.EOF {
		return localFsAIContextFileResult{}, fmt.Errorf("read_tail: %w", readErr)
	}
	tail = tail[:tailCount]

	content := utf8PrefixWithin(strings.ToValidUTF8(string(head), "�"), headBudget) + marker +
		utf8SuffixWithin(strings.ToValidUTF8(string(tail), "�"), tailBudget)
	return localFsAIContextFileResult{Content: content, Size: info.Size(), Truncated: true}, nil
}

func compactAIContextText(content string, originalSize int64) string {
	marker := fmt.Sprintf("\n\n[MinCode: middle of file omitted; original size %d bytes]\n\n", originalSize)
	available := maxAIContextFileContentBytes - len(marker)
	headBudget := available / 2
	tailBudget := available - headBudget
	return utf8PrefixWithin(content, headBudget) + marker + utf8SuffixWithin(content, tailBudget)
}

func utf8PrefixWithin(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	end := maxBytes
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end]
}

func utf8SuffixWithin(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	start := len(value) - maxBytes
	for start < len(value) && !utf8.RuneStart(value[start]) {
		start++
	}
	return value[start:]
}
