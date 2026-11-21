package git

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"mincode/app/backend/core"
)

// Staged changes for plugin commit message providers (the plugin host's
// scmCommitMessage point). Unlike git_get_commit_message_context, whose text
// goes to the user's own AI service, this output goes to third-party plugin
// code, so it is built defensively:
//
//   - Files whose path or old path the caller denies (VCS directories,
//     credential file names) are dropped together with their names and only
//     counted. The diff names the kept paths, excludes every denied path
//     (a literal pathspec also matches everything under a directory of the
//     same name), and then keeps only the sections whose header is one of a
//     kept file, so nothing else can reach the plugin.
//   - Repository configuration and the inherited environment never run a
//     command: external diff drivers (diff.external, diff.<driver>.command,
//     GIT_EXTERNAL_DIFF) and textconv filters are off, the fsmonitor hook is
//     off, transports are refused (protocol.allow=never, GIT_NO_LAZY_FETCH)
//     so partial clones cannot fetch missing objects with the repository's
//     ssh or credential settings, and the GIT_* variables that redirect the
//     repository, add configuration or name programs are removed. diff
//     --cached reads the index and the object store only, so clean/smudge
//     filters never run either.
//   - Output is bounded: at most filesMax files and diffMaxBytes of diff, cut
//     on a line boundary (never inside a UTF-8 sequence).
//   - Every git process is bound to the caller's context: cancelling it kills
//     the process.

// StagedFile is one staged path; Status is one of added, modified, deleted,
// renamed, copied or typeChanged. OldPath is set for renames and copies.
type StagedFile struct {
	Path    string `json:"path"`
	Status  string `json:"status"`
	OldPath string `json:"oldPath,omitempty"`
}

// StagedChanges is what a commit message provider may see of the index.
type StagedChanges struct {
	Files []StagedFile
	// OmittedFiles counts staged files left out because deny matched their
	// path or old path.
	OmittedFiles   int
	FilesTruncated bool
	Diff           string
	DiffTruncated  bool
}

// ErrNoStagedChanges reports an index without staged changes.
var ErrNoStagedChanges = errors.New("no staged changes")

// stagedSafeConfig are the -c options of every plugin-facing git call.
var stagedSafeConfig = []string{
	"-c", "core.quotepath=false",
	"-c", "core.fsmonitor=false",
	"-c", "diff.external=",
	"-c", "protocol.allow=never",
}

// stagedStrippedEnv are inherited variables removed from plugin-facing git
// calls; stagedStrippedEnvPrefixes remove whole families (GIT_CONFIG_* adds
// configuration, GIT_SSH* names programs).
var (
	stagedStrippedEnv = map[string]bool{
		"GIT_DIR": true, "GIT_WORK_TREE": true, "GIT_INDEX_FILE": true, "GIT_OBJECT_DIRECTORY": true,
		"GIT_ALTERNATE_OBJECT_DIRECTORIES": true, "GIT_NAMESPACE": true, "GIT_CEILING_DIRECTORIES": true,
		"GIT_EXTERNAL_DIFF": true, "GIT_PAGER": true, "GIT_ASKPASS": true, "GIT_EXEC_PATH": true,
		"GIT_NO_LAZY_FETCH": true,
	}
	stagedStrippedEnvPrefixes = []string{"GIT_CONFIG", "GIT_SSH"}
)

// stagedPathspecBatchBytes bounds the include pathspec bytes of one diff
// command so the command line stays below the Windows limit (32 767
// characters) unless the exclusions alone are very long.
const stagedPathspecBatchBytes = 16 * 1024

var stagedStatusNames = map[byte]string{
	'A': "added",
	'M': "modified",
	'D': "deleted",
	'R': "renamed",
	'C': "copied",
	'T': "typeChanged",
	// Unmerged entries appear while a conflict is being resolved.
	'U': "modified",
}

// stagedEnvironment removes the stripped variables (case-insensitively, for
// Windows) and sets a harmless pager and GIT_NO_LAZY_FETCH.
func stagedEnvironment(env []string) []string {
	out := make([]string, 0, len(env)+2)
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(key)
		stripped := stagedStrippedEnv[upper]
		for _, prefix := range stagedStrippedEnvPrefixes {
			stripped = stripped || strings.HasPrefix(upper, prefix)
		}
		if !stripped {
			out = append(out, entry)
		}
	}
	return append(out, "GIT_PAGER=cat", "GIT_NO_LAZY_FETCH=1")
}

// runStagedGit runs one plugin-facing git command in repoPath with the safe
// configuration and environment, bounded by gitDefaultTimeout and ctx. A
// cancelled ctx kills the process and returns ctx's error.
func runStagedGit(ctx context.Context, repoPath string, args ...string) (gitOutput, error) {
	if err := ctx.Err(); err != nil {
		return gitOutput{exitCode: -1}, err
	}
	timed, cancel := context.WithTimeout(ctx, gitDefaultTimeout)
	defer cancel()
	cmd, err := newGitCommand(append(append([]string{}, stagedSafeConfig...), args...)...)
	if err != nil {
		return gitOutput{exitCode: -1}, err
	}
	cmd.Env = stagedEnvironment(cmd.Env)
	core.PrepareProcessTree(cmd)
	if dir := resolveRepoDir(repoPath); dir != "" {
		cmd.Dir = dir
	}
	stdout := core.NewCappedWriter(gitMaxOutputBytes)
	stderr := core.NewCappedWriter(gitMaxOutputBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return gitOutput{exitCode: -1}, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var runErr error
	select {
	case runErr = <-done:
	case <-timed.Done():
		_ = core.KillProcessTree(cmd)
		runErr = <-done
	}
	stdoutBytes, stdoutTruncated := stdout.Bytes()
	stderrBytes, stderrTruncated := stderr.Bytes()
	out := gitOutput{stdout: stdoutBytes, stderr: stderrBytes, truncated: stdoutTruncated || stderrTruncated, exitCode: -1}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if timed.Err() == context.DeadlineExceeded {
		out.timedOut = true
		return out, nil
	}
	if cmd.ProcessState != nil {
		out.exitCode = cmd.ProcessState.ExitCode()
	}
	if runErr != nil {
		if _, ok := runErr.(*exec.ExitError); !ok {
			return out, runErr
		}
	}
	return out, nil
}

// RepositoryLayout is where a work tree keeps its repository, all paths
// symlink-resolved: GitDir is the work tree's own git directory and
// CommonDir the one holding objects and refs (they differ for linked
// worktrees).
type RepositoryLayout struct {
	TopLevel  string
	GitDir    string
	CommonDir string
}

// RepositoryLayoutContext reads the layout of the repository containing
// path (git rev-parse --show-toplevel --absolute-git-dir --git-common-dir)
// with the same command-free configuration as StagedChangesForPlugin.
func RepositoryLayoutContext(ctx context.Context, path string) (RepositoryLayout, error) {
	out, err := runStagedGit(ctx, path, "rev-parse", "--show-toplevel", "--absolute-git-dir", "--git-common-dir")
	if err != nil {
		return RepositoryLayout{}, err
	}
	if err := gitEnsureSuccess(out, "git rev-parse"); err != nil {
		return RepositoryLayout{}, err
	}
	lines := strings.Split(strings.TrimSuffix(gitStdoutString(out), "\n"), "\n")
	if len(lines) != 3 || lines[0] == "" || lines[1] == "" || lines[2] == "" {
		return RepositoryLayout{}, errors.New("git rev-parse: not a work tree")
	}
	common := filepath.FromSlash(lines[2])
	if !filepath.IsAbs(common) {
		// --git-common-dir is relative to the working directory.
		common = filepath.Join(resolveRepoDir(path), common)
	}
	layout := RepositoryLayout{
		TopLevel:  resolveRepoDir(filepath.FromSlash(lines[0])),
		GitDir:    resolveRepoDir(filepath.FromSlash(lines[1])),
		CommonDir: resolveRepoDir(common),
	}
	for _, dir := range []string{layout.TopLevel, layout.GitDir, layout.CommonDir} {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			return RepositoryLayout{}, errors.New("git rev-parse: repository folders are missing")
		}
	}
	return layout, nil
}

// StagedChangesForPlugin is StagedChangesForPluginContext without a context.
func StagedChangesForPlugin(repoPath string, deny func(relative string) bool, filesMax, diffMaxBytes int) (StagedChanges, error) {
	return StagedChangesForPluginContext(context.Background(), repoPath, deny, filesMax, diffMaxBytes)
}

// StagedChangesForPluginContext lists the staged files of the repository at
// repoPath and diffs those deny keeps. deny receives slash-separated
// repository-relative paths; a file is left out when it matches the path or,
// for renames and copies, the old path. Returns ErrNoStagedChanges when the
// index has no staged change (a non-empty index whose files are all denied
// is not an error), and ctx's error when ctx ends.
func StagedChangesForPluginContext(ctx context.Context, repoPath string, deny func(relative string) bool, filesMax, diffMaxBytes int) (StagedChanges, error) {
	out, err := runStagedGit(ctx, repoPath, "diff", "--cached", "--name-status", "-z", "-M", "--no-ext-diff", "--no-textconv", "--no-relative")
	if err != nil {
		return StagedChanges{}, err
	}
	if err := gitEnsureSuccess(out, "git diff --cached --name-status"); err != nil {
		return StagedChanges{}, err
	}
	if out.truncated {
		return StagedChanges{}, errors.New("git diff --cached --name-status: output too large")
	}
	all, err := parseStagedNameStatus(out.stdout)
	if err != nil {
		return StagedChanges{}, err
	}
	if len(all) == 0 {
		return StagedChanges{}, ErrNoStagedChanges
	}
	var changes StagedChanges
	changes.Files = []StagedFile{}
	var denied []string
	for _, file := range all {
		if deny(file.Path) || (file.OldPath != "" && deny(file.OldPath)) {
			changes.OmittedFiles++
			denied = append(denied, file.Path)
			if file.OldPath != "" {
				denied = append(denied, file.OldPath)
			}
			continue
		}
		if len(changes.Files) == filesMax {
			changes.FilesTruncated = true
			continue
		}
		changes.Files = append(changes.Files, file)
	}
	changes.Diff, changes.DiffTruncated, err = stagedDiff(ctx, repoPath, changes.Files, denied, diffMaxBytes)
	if err != nil {
		return StagedChanges{}, err
	}
	return changes, nil
}

// parseStagedNameStatus parses `diff --name-status -z` output: a status
// field then one path, or two (old, new) for renames and copies.
func parseStagedNameStatus(output []byte) ([]StagedFile, error) {
	if len(output) == 0 {
		return nil, nil
	}
	fields := bytes.Split(bytes.TrimSuffix(output, []byte{0}), []byte{0})
	var files []StagedFile
	for index := 0; index < len(fields); {
		status := fields[index]
		if len(status) == 0 {
			return nil, errors.New("git diff --name-status: empty status")
		}
		name, known := stagedStatusNames[status[0]]
		paths := 1
		if status[0] == 'R' || status[0] == 'C' {
			paths = 2
		}
		if index+paths >= len(fields) {
			return nil, errors.New("git diff --name-status: truncated record")
		}
		file := StagedFile{Status: name}
		if paths == 2 {
			file.OldPath, file.Path = string(fields[index+1]), string(fields[index+2])
		} else {
			file.Path = string(fields[index+1])
		}
		index += paths + 1
		if !known {
			// X (unknown) and B (broken pair) are not produced by these
			// options; anything else is reported as a modification.
			file.Status = "modified"
		}
		files = append(files, file)
	}
	return files, nil
}

// stagedDiff diffs the given files in batches of pathspecs, every batch
// excluding the denied paths, stops once the output passes maxBytes, keeps
// only the sections of the given files and cuts the result to maxBytes.
func stagedDiff(ctx context.Context, repoPath string, files []StagedFile, denied []string, maxBytes int) (string, bool, error) {
	if len(files) == 0 {
		return "", false, nil
	}
	excludes := make([]string, 0, len(denied))
	for _, path := range denied {
		excludes = append(excludes, ":(exclude,literal)"+path)
	}
	var diff strings.Builder
	batch := []string{}
	batchBytes := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		args := append([]string{"diff", "--cached", "--no-color", "--no-ext-diff", "--no-textconv", "--no-relative",
			"--src-prefix=a/", "--dst-prefix=b/", "--submodule=short", "-M", "--unified=3", "--"}, batch...)
		args = append(args, excludes...)
		out, err := runStagedGit(ctx, repoPath, args...)
		if err != nil {
			return err
		}
		if err := gitEnsureSuccess(out, "git diff --cached"); err != nil {
			return err
		}
		diff.WriteString(filterStagedDiff(string(out.stdout), files))
		batch, batchBytes = batch[:0], 0
		return nil
	}
	for _, file := range files {
		if diff.Len() > maxBytes {
			break
		}
		specs := []string{":(literal)" + file.Path}
		if file.OldPath != "" {
			// Both sides in the same batch so -M pairs the rename.
			specs = append(specs, ":(literal)"+file.OldPath)
		}
		size := 0
		for _, spec := range specs {
			size += len(spec) + 1
		}
		if batchBytes+size > stagedPathspecBatchBytes {
			if err := flush(); err != nil {
				return "", false, err
			}
		}
		batch = append(batch, specs...)
		batchBytes += size
	}
	if diff.Len() <= maxBytes {
		if err := flush(); err != nil {
			return "", false, err
		}
	}
	text, truncated := TruncateStagedDiff(diff.String(), maxBytes)
	return text, truncated, nil
}

// filterStagedDiff keeps the sections of diff whose `diff --git` header
// names one of files (old and new path of a rename, or either path alone
// when git did not pair them) and drops everything else, including text
// before the first header. The comparison is on the exact header git
// writes with the a/ and b/ prefixes, C-quoting paths that need it.
func filterStagedDiff(diff string, files []StagedFile) string {
	allowed := make(map[string]bool, len(files))
	header := func(oldPath, newPath string) string {
		return "diff --git " + quoteDiffPath("a/"+oldPath) + " " + quoteDiffPath("b/"+newPath)
	}
	for _, file := range files {
		allowed[header(file.Path, file.Path)] = true
		if file.OldPath != "" {
			allowed[header(file.OldPath, file.Path)] = true
			allowed[header(file.OldPath, file.OldPath)] = true
		}
	}
	var out strings.Builder
	keep := false
	for rest := diff; rest != ""; {
		line := rest
		if end := strings.IndexByte(rest, '\n'); end >= 0 {
			line = rest[:end+1]
		}
		rest = rest[len(line):]
		if strings.HasPrefix(line, "diff --git ") {
			keep = allowed[strings.TrimSuffix(line, "\n")]
		}
		if keep {
			out.WriteString(line)
		}
	}
	return out.String()
}

// quoteDiffPath quotes a diff header path the way git does with
// core.quotepath=false: paths with a control character, a double quote or a
// backslash are written in C style within double quotes.
func quoteDiffPath(path string) string {
	needs := false
	for index := 0; index < len(path); index++ {
		if char := path[index]; char < 0x20 || char == '"' || char == '\\' || char == 0x7f {
			needs = true
			break
		}
	}
	if !needs {
		return path
	}
	var quoted strings.Builder
	quoted.WriteByte('"')
	for index := 0; index < len(path); index++ {
		char := path[index]
		switch char {
		case '"', '\\':
			quoted.WriteByte('\\')
			quoted.WriteByte(char)
		case '\a':
			quoted.WriteString(`\a`)
		case '\b':
			quoted.WriteString(`\b`)
		case '\t':
			quoted.WriteString(`\t`)
		case '\n':
			quoted.WriteString(`\n`)
		case '\v':
			quoted.WriteString(`\v`)
		case '\f':
			quoted.WriteString(`\f`)
		case '\r':
			quoted.WriteString(`\r`)
		default:
			if char < 0x20 || char == 0x7f {
				quoted.WriteByte('\\')
				quoted.WriteByte('0' + char>>6)
				quoted.WriteByte('0' + (char>>3)&7)
				quoted.WriteByte('0' + char&7)
			} else {
				quoted.WriteByte(char)
			}
		}
	}
	quoted.WriteByte('"')
	return quoted.String()
}

// TruncateStagedDiff cuts diff to at most maxBytes, after the last complete
// line that fits; a first line longer than maxBytes is cut on a UTF-8
// boundary instead. truncated reports a cut.
func TruncateStagedDiff(diff string, maxBytes int) (string, bool) {
	if len(diff) <= maxBytes {
		return diff, false
	}
	if maxBytes <= 0 {
		return "", true
	}
	head := diff[:maxBytes]
	if end := strings.LastIndexByte(head, '\n'); end >= 0 {
		return head[:end+1], true
	}
	for len(head) > 0 && !utf8.RuneStart(diff[len(head)]) {
		head = head[:len(head)-1]
	}
	return head, true
}
