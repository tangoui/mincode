package fs

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"mincode/app/backend/core"
)

const (
	defaultCodeSearchLimit            = 40
	maxCodeSearchLimit                = 100
	maxCodeSearchQueryRunes           = 256
	maxCodeSearchFiles                = 50_000
	maxCodeSearchFileBytes      int64 = 1 << 20 // 1 MB
	maxCodeSearchMatchesPerFile       = 3
	maxCodeSearchLineBytes            = 1 << 20
)

// codeSearchTimeout is the wall-clock budget of one search. A variable only so
// tests that drive the deadline through their own context can lift it.
var codeSearchTimeout = 5 * time.Second

type localFsCodeSearchMatch struct {
	RelativePath string `json:"relativePath"`
	Kind         string `json:"kind"`
	Line         int    `json:"line,omitempty"`
	Column       int    `json:"column,omitempty"`
	LineText     string `json:"lineText,omitempty"`
	Score        int    `json:"score"`
	Reason       string `json:"reason"`
}

type localFsCodeSearchResult struct {
	Query           string                   `json:"query"`
	Mode            string                   `json:"mode"`
	Engine          string                   `json:"engine"`
	FilesConsidered int                      `json:"filesConsidered"`
	Results         []localFsCodeSearchMatch `json:"results"`
	Truncated       bool                     `json:"truncated"`
	DurationMs      int64                    `json:"durationMs"`
}

type codeSearchNeedle struct {
	value string
	lower string
	exact bool
}

type ripgrepJSONEvent struct {
	Type string `json:"type"`
	Data struct {
		Path struct {
			Text string `json:"text"`
		} `json:"path"`
		Lines struct {
			Text string `json:"text"`
		} `json:"lines"`
		LineNumber int `json:"line_number"`
		Submatches []struct {
			Start int `json:"start"`
			End   int `json:"end"`
		} `json:"submatches"`
	} `json:"data"`
}

// A desktop app launched from Finder or the Dock inherits launchd's minimal PATH,
// so a Homebrew or MacPorts ripgrep is invisible to LookPath even when installed.
// Probing the standard prefixes keeps the fast engine reachable instead of
// silently dropping to the much slower builtin scanner.
var codeSearchExecutablePrefixes = []string{
	"/opt/homebrew/bin",
	"/usr/local/bin",
	"/opt/local/bin",
	"/home/linuxbrew/.linuxbrew/bin",
}

func lookupCodeSearchExecutable(name string) (string, error) {
	if path, err := exec.LookPath(name); err == nil {
		return path, nil
	}
	for _, prefix := range codeSearchExecutablePrefixes {
		candidate := filepath.Join(prefix, name)
		// Stat, not Lstat: package managers install these as symlinks into a
		// versioned cellar, and the prefixes are fixed system locations.
		info, err := os.Stat(candidate)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			continue
		}
		return candidate, nil
	}
	return "", exec.ErrNotFound
}

var findCodeSearchExecutable = lookupCodeSearchExecutable

type activeCodeSearch struct {
	cancel context.CancelFunc
	token  uint64
}

var activeCodeSearches = struct {
	sync.Mutex
	nextToken uint64
	requests  map[string]activeCodeSearch
}{requests: make(map[string]activeCodeSearch)}

func init() {
	core.RegisterCtx("local_fs_search_code", handleSearchCode)
	core.Register("local_fs_cancel_code_search", handleCancelCodeSearch)
}

func handleSearchCode(callCtx context.Context, _ string, argsJSON string) (string, error) {
	var args struct {
		ProjectRoot string `json:"projectRoot"`
		ScopePath   string `json:"scopePath"`
		Query       string `json:"query"`
		Mode        string `json:"mode"`
		Limit       int    `json:"limit"`
		RequestID   string `json:"requestId"`
	}
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	ctx, cancel := registerCodeSearch(callCtx, args.RequestID)
	defer cancel()
	result, err := searchCodeContext(ctx, args.ProjectRoot, args.ScopePath, args.Query, args.Mode, args.Limit)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(result)
}

func handleCancelCodeSearch(_ string, argsJSON string) (string, error) {
	var args struct {
		RequestID string `json:"requestId"`
	}
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	requestID := strings.TrimSpace(args.RequestID)
	if requestID == "" {
		return "", fmt.Errorf("requestId is required")
	}
	activeCodeSearches.Lock()
	request, exists := activeCodeSearches.requests[requestID]
	activeCodeSearches.Unlock()
	if exists {
		request.cancel()
	}
	return "", nil
}

// registerCodeSearch derives from the IPC call ctx, so the search stops either on
// local_fs_cancel_search_code or when the frontend cancels the call itself.
func registerCodeSearch(parent context.Context, requestID string) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return ctx, cancel
	}
	activeCodeSearches.Lock()
	if previous, exists := activeCodeSearches.requests[requestID]; exists {
		previous.cancel()
	}
	activeCodeSearches.nextToken++
	token := activeCodeSearches.nextToken
	activeCodeSearches.requests[requestID] = activeCodeSearch{cancel: cancel, token: token}
	activeCodeSearches.Unlock()
	return ctx, func() {
		activeCodeSearches.Lock()
		if request, exists := activeCodeSearches.requests[requestID]; exists && request.token == token {
			delete(activeCodeSearches.requests, requestID)
		}
		activeCodeSearches.Unlock()
		cancel()
	}
}

// searchCode remains the compatibility entry point for callers without cancellation.
func searchCode(projectRoot, scopePath, query, mode string, limit int) (localFsCodeSearchResult, error) {
	return searchCodeContext(context.Background(), projectRoot, scopePath, query, mode, limit)
}

// codeSearchStopped reports whether the search must end here, and whether it ended
// because the time budget ran out rather than because the caller cancelled. The
// timeout is a budget, not a failure: a tree too large to scan in full returns
// whatever matched inside the window, flagged truncated, since partial results
// beat an error the user reads as "search is broken". A cancel still aborts —
// nobody is waiting for that answer.
func codeSearchStopped(ctx context.Context) (stopped, expired bool) {
	err := ctx.Err()
	if err == nil {
		return false, false
	}
	return true, errors.Is(err, context.DeadlineExceeded)
}

func searchCodeContext(parent context.Context, projectRoot, scopePath, query, mode string, limit int) (localFsCodeSearchResult, error) {
	startedAt := time.Now()
	ctx, cancel := context.WithTimeout(parent, codeSearchTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return localFsCodeSearchResult{}, err
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return localFsCodeSearchResult{}, fmt.Errorf("search_query_empty")
	}
	if utf8.RuneCountInString(query) > maxCodeSearchQueryRunes {
		return localFsCodeSearchResult{}, fmt.Errorf("search_query_too_long")
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "auto"
	}
	switch mode {
	case "auto", "path", "text", "symbol":
	default:
		return localFsCodeSearchResult{}, fmt.Errorf("search_mode_invalid")
	}
	if limit <= 0 {
		limit = defaultCodeSearchLimit
	}
	if limit > maxCodeSearchLimit {
		limit = maxCodeSearchLimit
	}

	root, err := canonicalLocalProjectRoot(projectRoot)
	if err != nil {
		return localFsCodeSearchResult{}, err
	}
	// A model can infer a conventional directory such as "src" even when this
	// project has a different layout. Code search is read-only, so an absent
	// optional scope safely falls back to the bound project instead of producing
	// a misleading tool failure. Existing paths still go through the normal
	// root-boundary and symlink checks below.
	scope, err := resolveUnderRoot(root, scopePath)
	if err != nil {
		if !canFallbackToProjectRoot(scopePath, err) {
			return localFsCodeSearchResult{}, err
		}
		scope = root
	}
	info, err := os.Lstat(scope)
	if err != nil {
		return localFsCodeSearchResult{}, fmt.Errorf("scope_not_a_directory")
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return localFsCodeSearchResult{}, fmt.Errorf("scope_not_a_directory")
	}

	files, filesTruncated, err := listCodeSearchFilesForScope(ctx, root, scope, info)
	if err != nil {
		return localFsCodeSearchResult{}, err
	}
	expired := false
	if stopped, deadline := codeSearchStopped(ctx); stopped {
		if !deadline {
			return localFsCodeSearchResult{}, ctx.Err()
		}
		expired = true
	}
	needles := buildCodeSearchNeedles(query)
	pathScores := make(map[string]int, len(files))
	pathReasons := make(map[string]string, len(files))
	for _, relativePath := range files {
		if stopped, deadline := codeSearchStopped(ctx); stopped {
			if !deadline {
				return localFsCodeSearchResult{}, ctx.Err()
			}
			expired = true
			break
		}
		if score, reason, matched := scoreCodeSearchPath(relativePath, query, needles); matched {
			pathScores[relativePath] = score
			pathReasons[relativePath] = reason
		}
	}

	matches := make([]localFsCodeSearchMatch, 0, limit*2)
	engine := "path-index"
	searchTruncated := false
	matchedContentPaths := make(map[string]struct{})
	if mode != "path" && !expired {
		budget := max(limit*8, 160)
		contentMatches, truncated, usedRipgrep := searchCodeWithRipgrep(
			ctx, root, scope, mode, contentNeedles(mode, needles), pathScores, budget,
		)
		if usedRipgrep {
			engine = "ripgrep"
			matches = append(matches, contentMatches...)
			searchTruncated = truncated
		} else {
			engine = "builtin-parallel"
			contentMatches, truncated = searchCodeWithBuiltin(
				ctx, root, files, mode, contentNeedles(mode, needles), pathScores, budget,
			)
			matches = append(matches, contentMatches...)
			searchTruncated = truncated
		}
		for _, match := range matches {
			matchedContentPaths[match.RelativePath] = struct{}{}
		}
	}

	if mode == "auto" || mode == "path" {
		for _, relativePath := range files {
			score, matched := pathScores[relativePath]
			if !matched {
				continue
			}
			if mode == "auto" {
				if _, alreadyMatched := matchedContentPaths[relativePath]; alreadyMatched {
					continue
				}
			}
			matches = append(matches, localFsCodeSearchMatch{
				RelativePath: relativePath,
				Kind:         "path",
				Score:        score,
				Reason:       pathReasons[relativePath],
			})
		}
	}

	if stopped, deadline := codeSearchStopped(ctx); stopped {
		if !deadline {
			return localFsCodeSearchResult{}, ctx.Err()
		}
		expired = true
	}
	var resultLimitTruncated bool
	matches, resultLimitTruncated = rankAndLimitCodeSearchMatches(matches, limit)
	result := localFsCodeSearchResult{
		Query:           query,
		Mode:            mode,
		Engine:          engine,
		FilesConsidered: len(files),
		Results:         matches,
		Truncated:       filesTruncated || searchTruncated || resultLimitTruncated || expired,
		DurationMs:      time.Since(startedAt).Milliseconds(),
	}
	return result, nil
}

func canFallbackToProjectRoot(scopePath string, err error) bool {
	if !errors.Is(err, os.ErrNotExist) {
		return false
	}
	path := strings.TrimSpace(scopePath)
	if path == "" || filepath.IsAbs(path) || filepath.VolumeName(path) != "" {
		return false
	}
	for _, segment := range strings.FieldsFunc(path, func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		if segment == ".." {
			return false
		}
	}
	return true
}

// listCodeSearchFilesForScope supports either a project-relative directory or one
// regular file. AI agents commonly narrow a follow-up search to a file returned
// by an earlier tool call, so treating that file as a one-file scope avoids a
// needless scope_not_a_directory failure while preserving the root and sensitive
// file protections used for directory searches.
func listCodeSearchFilesForScope(ctx context.Context, root, scope string, info os.FileInfo) ([]string, bool, error) {
	if info.Mode().IsRegular() {
		relativePath, err := filepath.Rel(root, scope)
		if err != nil {
			return nil, false, fmt.Errorf("resolve_code_search_file_scope: %w", err)
		}
		relativePath = filepath.ToSlash(relativePath)
		if relativePath == "." || relativePath == ".." || strings.HasPrefix(relativePath, "../") {
			return nil, false, fmt.Errorf("scope_escapes_root")
		}
		if isSensitiveCodeSearchPath(relativePath) || !isSafeAIContextFile(root, relativePath) {
			return []string{}, false, nil
		}
		return []string{relativePath}, false, nil
	}
	if !info.IsDir() {
		return nil, false, fmt.Errorf("scope_not_a_directory")
	}
	return listCodeSearchFiles(ctx, root, scope)
}

func listCodeSearchFiles(ctx context.Context, root, scope string) ([]string, bool, error) {
	if files, truncated, ok := listGitCodeSearchFiles(ctx, root, scope); ok {
		return files, truncated, nil
	}
	if stopped, deadline := codeSearchStopped(ctx); stopped {
		// Running out of budget mid-enumeration yields nothing to scan, but the
		// caller still reports a truncated result rather than a failure.
		if !deadline {
			return nil, true, ctx.Err()
		}
		return nil, true, nil
	}
	return walkCodeSearchFiles(ctx, root, scope)
}

func listGitCodeSearchFiles(ctx context.Context, root, scope string) ([]string, bool, bool) {
	gitRootCommand := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "--show-toplevel")
	core.PrepareBackgroundCommand(gitRootCommand)
	gitRootOutput, err := gitRootCommand.Output()
	if err != nil {
		return nil, false, false
	}
	gitRoot, err := filepath.EvalSymlinks(strings.TrimSpace(string(gitRootOutput)))
	if err != nil || !withinRoot(gitRoot, root) {
		return nil, false, false
	}
	listCommand := exec.CommandContext(ctx,
		"git", "--literal-pathspecs", "-C", scope, "ls-files", "--cached", "--others",
		"--exclude-standard", "--full-name", "-z", "--", ".",
	)
	core.PrepareBackgroundCommand(listCommand)
	output, err := listCommand.Output()
	if err != nil {
		return nil, false, false
	}

	files := make([]string, 0, min(len(output)/32, maxCodeSearchFiles))
	truncated := false
	for _, rawPath := range strings.Split(string(output), "\x00") {
		if ctx.Err() != nil {
			return nil, false, false
		}
		if rawPath == "" {
			continue
		}
		fullPath := filepath.Join(gitRoot, filepath.FromSlash(rawPath))
		relativePath, err := filepath.Rel(root, fullPath)
		if err != nil || relativePath == "." || relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) {
			continue
		}
		relativePath = filepath.ToSlash(relativePath)
		if shouldSkipAIContextPath(relativePath) || isSensitiveCodeSearchPath(relativePath) || !isSafeAIContextFile(root, relativePath) {
			continue
		}
		files = append(files, relativePath)
		if len(files) >= maxCodeSearchFiles {
			truncated = true
			break
		}
	}
	sort.Strings(files)
	return files, truncated, true
}

func walkCodeSearchFiles(ctx context.Context, root, scope string) ([]string, bool, error) {
	files := make([]string, 0, 1024)
	truncated := false
	err := filepath.WalkDir(scope, func(path string, entry os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if path == scope {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if shouldSkipAiTreeDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		relativePath, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relativePath = filepath.ToSlash(relativePath)
		if isSensitiveCodeSearchPath(relativePath) {
			return nil
		}
		files = append(files, relativePath)
		if len(files) >= maxCodeSearchFiles {
			truncated = true
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return files, true, nil
		}
		return nil, false, fmt.Errorf("list_code_search_files: %w", err)
	}
	sort.Strings(files)
	return files, truncated, nil
}

func isSensitiveCodeSearchPath(path string) bool {
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
		strings.HasSuffix(name, ".p12") || strings.HasSuffix(name, ".pfx") ||
		strings.HasSuffix(name, ".kdbx")
}

func buildCodeSearchNeedles(query string) []codeSearchNeedle {
	seen := make(map[string]struct{})
	needles := make([]codeSearchNeedle, 0, 8)
	add := func(value string, exact bool) {
		value = strings.TrimSpace(value)
		lower := strings.ToLower(value)
		if value == "" {
			return
		}
		if _, exists := seen[lower]; exists {
			return
		}
		seen[lower] = struct{}{}
		needles = append(needles, codeSearchNeedle{value: value, lower: lower, exact: exact})
	}
	add(query, true)
	for _, term := range strings.FieldsFunc(query, func(char rune) bool {
		return !(unicode.IsLetter(char) || unicode.IsDigit(char) || char == '_' || char == '-' || char == '.')
	}) {
		if utf8.RuneCountInString(term) < 2 {
			continue
		}
		add(term, false)
		if len(needles) >= 8 {
			break
		}
	}
	return needles
}

func scoreCodeSearchPath(path, query string, needles []codeSearchNeedle) (int, string, bool) {
	pathLower := strings.ToLower(filepath.ToSlash(path))
	nameLower := strings.ToLower(filepath.Base(filepath.FromSlash(path)))
	queryLower := strings.ToLower(strings.TrimSpace(query))
	if score, reason, matched := scoreSingleCodeSearchPath(pathLower, nameLower, queryLower); matched {
		return score, reason, true
	}
	bestScore := 0
	bestReason := ""
	for _, needle := range needles {
		if needle.exact {
			continue
		}
		score, reason, matched := scoreSingleCodeSearchPath(pathLower, nameLower, needle.lower)
		if matched && score-120 > bestScore {
			bestScore = score - 120
			bestReason = reason + " (query term)"
		}
	}
	return bestScore, bestReason, bestScore > 0
}

func scoreSingleCodeSearchPath(path, name, query string) (int, string, bool) {
	if query == "" {
		return 0, "", false
	}
	if name == query || path == query {
		return 1000, "exact path or file-name match", true
	}
	if strings.HasPrefix(name, query) {
		return 920, "file-name prefix match", true
	}
	if strings.HasPrefix(path, query) {
		return 880, "path prefix match", true
	}
	if strings.Contains(name, query) {
		return 820, "file-name contains query", true
	}
	if strings.Contains(path, query) {
		return 760, "path contains query", true
	}
	if fuzzyCodeSearchSubsequence(name, query) {
		return 620, "fuzzy file-name match", true
	}
	if fuzzyCodeSearchSubsequence(path, query) {
		return 560, "fuzzy path match", true
	}
	return 0, "", false
}

func fuzzyCodeSearchSubsequence(value, query string) bool {
	queryRunes := []rune(query)
	if len(queryRunes) == 0 {
		return false
	}
	index := 0
	for _, char := range value {
		if index < len(queryRunes) && char == queryRunes[index] {
			index++
		}
	}
	return index == len(queryRunes)
}

func searchCodeWithRipgrep(
	ctx context.Context,
	root, scope, mode string,
	needles []codeSearchNeedle,
	pathScores map[string]int,
	budget int,
) ([]localFsCodeSearchMatch, bool, bool) {
	rgPath, err := findCodeSearchExecutable("rg")
	if err != nil {
		return nil, false, false
	}
	patterns := make([]string, 0, len(needles))
	for _, needle := range needles {
		patterns = append(patterns, regexp.QuoteMeta(needle.value))
	}
	if len(patterns) == 0 {
		return nil, false, true
	}
	scopeRelative, err := filepath.Rel(root, scope)
	if err != nil {
		return nil, false, false
	}
	if scopeRelative == "." {
		scopeRelative = "."
	}

	args := []string{
		"--json", "--no-config", "--no-messages", "--hidden", "--ignore-case",
		"--max-count", fmt.Sprint(maxCodeSearchMatchesPerFile * 4),
		"--max-filesize", "1M",
	}
	// Derived from the list the walk and the per-match filter already share, so the
	// two can never disagree. Every directory rg is not told to skip is one it reads
	// in full — inside the time budget — only for the match to be dropped afterwards.
	for _, name := range aiTreeSkipDirNames {
		args = append(args, "--glob", "!"+name+"/**", "--glob", "!**/"+name+"/**")
	}
	args = append(args,
		"--glob", "!.env", "--glob", "!.env.*", "--glob", "!**/.env", "--glob", "!**/.env.*",
		"--glob", "!**/.npmrc", "--glob", "!**/.pypirc", "--glob", "!**/.netrc",
		"--glob", "!**/id_rsa", "--glob", "!**/id_ed25519", "--glob", "!**/credentials",
		"--glob", "!**/*.pem", "--glob", "!**/*.key", "--glob", "!**/*.p12",
		"--glob", "!**/*.pfx", "--glob", "!**/*.kdbx",
		"--regexp", strings.Join(patterns, "|"), filepath.ToSlash(scopeRelative),
	)
	command := exec.CommandContext(ctx, rgPath, args...)
	core.PrepareBackgroundCommand(command)
	command.Dir = root
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, false, false
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return nil, false, false
	}

	matches := make([]localFsCodeSearchMatch, 0, min(budget, 256))
	retainedPerFile := make(map[string]int)
	truncated := false
	killed := false
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*maxCodeSearchLineBytes)
	for scanner.Scan() {
		var event ripgrepJSONEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil || event.Type != "match" {
			continue
		}
		relativePath := filepath.ToSlash(filepath.Clean(event.Data.Path.Text))
		// ripgrep enumerates the scope itself, so each hit is vetted on its own
		// rather than against the pre-walk list. Gating on that list discarded
		// roughly half the hits on a large tree — after paying for the full scan —
		// because the walk stops at maxCodeSearchFiles. ripgrep does not follow
		// symlinks, and isSafeAIContextFile re-checks every path component, so the
		// walk's exclusions still hold.
		if isSensitiveCodeSearchPath(relativePath) || shouldSkipAIContextPath(relativePath) ||
			!isSafeAIContextFile(root, relativePath) {
			continue
		}
		if retainedPerFile[relativePath] >= maxCodeSearchMatchesPerFile {
			continue
		}
		lineText := strings.TrimRight(event.Data.Lines.Text, "\r\n")
		match, ok := makeCodeSearchContentMatch(
			relativePath, event.Data.LineNumber, firstRipgrepColumn(event, event.Data.Lines.Text), lineText,
			mode, needles, pathScores[relativePath],
		)
		if !ok {
			continue
		}
		matches = append(matches, match)
		retainedPerFile[relativePath]++
		if len(matches) >= budget {
			truncated = true
			killed = true
			_ = command.Process.Kill()
			break
		}
	}
	waitErr := command.Wait()
	if scanner.Err() != nil && !killed {
		return nil, false, false
	}
	if ctx.Err() != nil {
		return matches, true, true
	}
	if killed {
		return matches, true, true
	}
	if waitErr != nil {
		var exitError *exec.ExitError
		if !errors.As(waitErr, &exitError) || exitError.ExitCode() != 1 {
			return nil, false, false
		}
	}
	return matches, truncated, true
}

func firstRipgrepColumn(event ripgrepJSONEvent, line string) int {
	if len(event.Data.Submatches) == 0 {
		return 1
	}
	return byteOffsetToUTF16Column(line, event.Data.Submatches[0].Start)
}

func searchCodeWithBuiltin(
	parent context.Context,
	root string,
	files []string,
	mode string,
	needles []codeSearchNeedle,
	pathScores map[string]int,
	budget int,
) ([]localFsCodeSearchMatch, bool) {
	// This child preserves the request's parent cancellation while allowing an
	// internal early-stop once enough candidates have been collected.
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	jobs := make(chan string)
	results := make(chan []localFsCodeSearchMatch)
	workerCount := min(max(runtime.GOMAXPROCS(0), 2), 8)
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case relativePath, ok := <-jobs:
					if !ok {
						return
					}
					matches := scanCodeSearchFile(
						ctx, root, relativePath, mode, needles, pathScores[relativePath],
					)
					if len(matches) == 0 {
						continue
					}
					select {
					case results <- matches:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, relativePath := range files {
			select {
			case jobs <- relativePath:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		workers.Wait()
		close(results)
	}()

	matches := make([]localFsCodeSearchMatch, 0, min(budget, 256))
	truncated := false
	for fileMatches := range results {
		matches = append(matches, fileMatches...)
		if len(matches) >= budget {
			truncated = true
			cancel()
		}
	}
	if ctx.Err() == context.DeadlineExceeded {
		truncated = true
	}
	return matches, truncated
}

func scanCodeSearchFile(
	ctx context.Context,
	root, relativePath, mode string,
	needles []codeSearchNeedle,
	pathScore int,
) []localFsCodeSearchMatch {
	fullPath := filepath.Join(root, filepath.FromSlash(relativePath))
	info, err := os.Lstat(fullPath)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > maxCodeSearchFileBytes {
		return nil
	}
	file, err := os.Open(fullPath)
	if err != nil {
		return nil
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	peek, _ := reader.Peek(searchPeekBytes)
	if sampleLooksLikeBinaryForPreview(peek) {
		return nil
	}

	matches := make([]localFsCodeSearchMatch, 0, maxCodeSearchMatchesPerFile)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), maxCodeSearchLineBytes)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		if lineNumber%128 == 0 {
			select {
			case <-ctx.Done():
				return matches
			default:
			}
		}
		lineText := scanner.Text()
		column := firstCodeSearchColumn(lineText, needles)
		if column == 0 {
			continue
		}
		match, ok := makeCodeSearchContentMatch(
			relativePath, lineNumber, column, lineText, mode, needles, pathScore,
		)
		if !ok {
			continue
		}
		matches = append(matches, match)
		if len(matches) >= maxCodeSearchMatchesPerFile {
			break
		}
	}
	return matches
}

func firstCodeSearchColumn(line string, needles []codeSearchNeedle) int {
	lineLower := strings.ToLower(line)
	best := -1
	for _, needle := range needles {
		index := strings.Index(lineLower, needle.lower)
		if index >= 0 && (best < 0 || index < best) {
			best = index
		}
	}
	if best < 0 {
		return 0
	}
	return byteOffsetToUTF16Column(line, best)
}

// byteOffsetToUTF16Column converts a UTF-8 byte offset into Monaco's 1-based
// UTF-16 column convention. Invalid offsets are clamped to the line bounds.
func byteOffsetToUTF16Column(line string, offset int) int {
	if offset < 0 {
		offset = 0
	}
	if offset > len(line) {
		offset = len(line)
	}
	offset = len(string([]byte(line)[:offset]))
	units := 0
	for _, r := range line[:offset] {
		if r > 0xFFFF {
			units += 2
		} else {
			units++
		}
	}
	return units + 1
}

// Text mode is intentionally a literal, full-query search. Query-term expansion
// remains available for path ranking and the broader auto/symbol discovery modes.
func contentNeedles(mode string, needles []codeSearchNeedle) []codeSearchNeedle {
	if mode == "text" && len(needles) > 0 {
		return needles[:1]
	}
	return needles
}

func makeCodeSearchContentMatch(
	relativePath string,
	line, column int,
	lineText, mode string,
	needles []codeSearchNeedle,
	pathScore int,
) (localFsCodeSearchMatch, bool) {
	lineLower := strings.ToLower(lineText)
	matchedExact := false
	matched := false
	for _, needle := range needles {
		if strings.Contains(lineLower, needle.lower) {
			matched = true
			matchedExact = matchedExact || needle.exact
		}
	}
	if !matched {
		return localFsCodeSearchMatch{}, false
	}
	declaration := looksLikeCodeDeclaration(lineText)
	if mode == "symbol" && !declaration {
		return localFsCodeSearchMatch{}, false
	}
	kind := "text"
	baseScore := 700
	reason := "text match"
	if matchedExact {
		baseScore += 60
		reason = "exact text match"
	}
	if declaration && mode != "text" {
		kind = "symbol"
		baseScore = 920
		if matchedExact {
			baseScore += 40
		}
		reason = "symbol declaration match"
	}
	if pathScore > 0 {
		baseScore += min(pathScore/4, 250)
	}
	return localFsCodeSearchMatch{
		RelativePath: relativePath,
		Kind:         kind,
		Line:         line,
		Column:       column,
		// Keep indentation so the reported source column also identifies the
		// matching position in the displayed snippet.
		LineText: truncateRunes(strings.TrimRight(lineText, "\r\n"), 240),
		Score:    baseScore,
		Reason:   reason,
	}, true
}

func looksLikeCodeDeclaration(line string) bool {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(line)))
	if len(fields) == 0 {
		return false
	}
	modifiers := map[string]struct{}{
		"export": {}, "default": {}, "public": {}, "private": {}, "protected": {},
		"internal": {}, "static": {}, "async": {}, "abstract": {}, "final": {},
		"sealed": {}, "readonly": {}, "pub": {}, "unsafe": {}, "extern": {},
	}
	index := 0
	for index < len(fields) {
		field := strings.Trim(fields[index], "{}()[]:;")
		if _, modifier := modifiers[field]; !modifier {
			break
		}
		index++
	}
	if index >= len(fields) {
		return false
	}
	keyword := strings.Trim(fields[index], "{}()[]:;")
	switch keyword {
	case "func", "function", "class", "interface", "type", "struct", "enum", "trait", "def", "fn", "const", "let", "var":
		return true
	default:
		return false
	}
}

func rankAndLimitCodeSearchMatches(matches []localFsCodeSearchMatch, limit int) ([]localFsCodeSearchMatch, bool) {
	deduplicated := make(map[string]localFsCodeSearchMatch, len(matches))
	for _, match := range matches {
		key := fmt.Sprintf("%s\x00%s\x00%d\x00%d", match.RelativePath, match.Kind, match.Line, match.Column)
		if previous, exists := deduplicated[key]; !exists || match.Score > previous.Score {
			deduplicated[key] = match
		}
	}
	ranked := make([]localFsCodeSearchMatch, 0, len(deduplicated))
	for _, match := range deduplicated {
		ranked = append(ranked, match)
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Score != ranked[j].Score {
			return ranked[i].Score > ranked[j].Score
		}
		if ranked[i].RelativePath != ranked[j].RelativePath {
			return ranked[i].RelativePath < ranked[j].RelativePath
		}
		if ranked[i].Line != ranked[j].Line {
			return ranked[i].Line < ranked[j].Line
		}
		return ranked[i].Column < ranked[j].Column
	})
	truncated := len(ranked) > limit
	if truncated {
		ranked = ranked[:limit]
	}
	return ranked, truncated
}
