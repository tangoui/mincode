// Git 域命令：全部通过系统 `git` CLI 子进程实现（与 Tauri src-tauri/src/git 同方案）。
//
// 契约对齐：
//   - 入参 JSON key 为 camelCase（repoPath / relativePath / commitHash / ...）。
//   - 返回结构 JSON key 为 snake_case（old_path / short_hash / is_current / line_start / ...），
//     与 Tauri serde 默认（不 rename）一致，前端 src/components/business/git/types.ts 消费。
package git

import (
	"mincode/app/backend/core"

	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

// ---- 常量（对齐 git/types.rs） ----

const (
	gitDefaultTimeout = 30 * time.Second
	// commit 会同步执行 pre-commit/commit-msg 等仓库钩子，需为 lint、类型检查预留时间。
	gitCommitTimeout           = 5 * time.Minute
	gitNetworkTimeout          = 120 * time.Second
	gitMaxOutputBytes          = 2 * 1024 * 1024
	gitStagedPatchMax          = 30_000
	gitCommitMessageContextMax = 12_000
)

// LOG_FORMAT：%H<US>%h<US>%an<US>%ae<US>%s<US>%at<US>%P（US = \x1f）
const gitLogFormat = "%H%x1f%h%x1f%an%x1f%ae%x1f%s%x1f%at%x1f%P"

// ---- 返回结构（snake_case JSON） ----

type gitFileEntry struct {
	xy      string  // exact porcelain-v2 status, host-only; never serialized
	Path    string  `json:"path"`
	Status  string  `json:"status"`
	OldPath *string `json:"old_path"`
}

type gitStatus struct {
	Branch    string         `json:"branch"`
	Upstream  *string        `json:"upstream"`
	Ahead     int            `json:"ahead"`
	Behind    int            `json:"behind"`
	Staged    []gitFileEntry `json:"staged"`
	Unstaged  []gitFileEntry `json:"unstaged"`
	Untracked []gitFileEntry `json:"untracked"`
}

type gitTrackingStatus struct {
	Branch   string  `json:"branch"`
	Upstream *string `json:"upstream"`
	Ahead    int     `json:"ahead"`
	Behind   int     `json:"behind"`
}

type gitDiffHunkHeader struct {
	Index              int    `json:"index"`
	Header             string `json:"header"`
	ModifiedActionLine uint32 `json:"modified_action_line"`
}

type gitFileDiff struct {
	Original        string              `json:"original"`
	Modified        string              `json:"modified"`
	Language        string              `json:"language"`
	Binary          bool                `json:"binary"`
	Hunks           []gitDiffHunkHeader `json:"hunks"`
	WorktreeContent string              `json:"worktree_content"`
	WorktreeExists  bool                `json:"worktree_exists"`
	WorktreeIsText  bool                `json:"worktree_is_text"`
}

type gitBranchEntry struct {
	Name      string `json:"name"`
	IsCurrent bool   `json:"is_current"`
	IsRemote  bool   `json:"is_remote"`
}

type gitBranches struct {
	Current  string           `json:"current"`
	Branches []gitBranchEntry `json:"branches"`
}

type gitStashEntry struct {
	Index   int            `json:"index"`
	Message string         `json:"message"`
	Oid     string         `json:"oid"`
	Files   []gitFileEntry `json:"files"`
}

type gitSyncResult struct {
	Ok      bool   `json:"ok"`
	Message string `json:"message"`
}

type gitLogEntry struct {
	Hash      string   `json:"hash"`
	ShortHash string   `json:"short_hash"`
	Author    string   `json:"author"`
	Email     string   `json:"email"`
	Message   string   `json:"message"`
	Time      int64    `json:"time"`
	Parents   []string `json:"parents"`
	Refs      []string `json:"refs"`
}

type gitCommitFile struct {
	Path    string  `json:"path"`
	OldPath *string `json:"old_path"`
	Status  string  `json:"status"`
}

type gitCommitDetail struct {
	Commit gitLogEntry     `json:"commit"`
	Files  []gitCommitFile `json:"files"`
}

type gitCliCheck struct {
	Available bool    `json:"available"`
	Version   *string `json:"version"`
	Os        string  `json:"os"`
	Error     *string `json:"error"`
}

type gitTagEntry struct {
	Name    string  `json:"name"`
	Message *string `json:"message"`
	// TargetHash 是标签引用本身指向的对象：附注标签是标签对象，轻量标签就是提交。
	TargetHash string `json:"target_hash"`
	// CommitHash 是标签最终指向的提交，附注标签已解引用。
	CommitHash string `json:"commit_hash"`
	Annotated  bool   `json:"annotated"`
	// Date 是 ISO 8601：附注标签取打标签时间，轻量标签取提交时间。
	Date string `json:"date"`
}

type blameHunk struct {
	LineStart       uint32 `json:"line_start"`
	LineEnd         uint32 `json:"line_end"`
	CommitHash      string `json:"commit_hash"`
	CommitHashShort string `json:"commit_hash_short"`
	Author          string `json:"author"`
	AuthorEmail     string `json:"author_email"`
	Date            string `json:"date"`
	Message         string `json:"message"`
}

// ---- git 子进程执行 ----

type gitOutput struct {
	stdout    []byte
	stderr    []byte
	truncated bool
	exitCode  int // -1 表示进程未正常返回退出码
	timedOut  bool
}

func mergePathLists(paths ...string) string {
	seen := map[string]struct{}{}
	merged := make([]string, 0)
	for _, pathValue := range paths {
		for _, dir := range filepath.SplitList(pathValue) {
			dir = strings.TrimSpace(dir)
			if dir == "" {
				continue
			}
			if _, ok := seen[dir]; ok {
				continue
			}
			seen[dir] = struct{}{}
			merged = append(merged, dir)
		}
	}
	return strings.Join(merged, string(os.PathListSeparator))
}

func resolveGitExecutable(pathValue string) (string, error) {
	names := []string{"git"}
	if runtime.GOOS == "windows" {
		names = []string{"git.exe", "git.cmd", "git.bat", "git"}
	}
	for _, dir := range filepath.SplitList(pathValue) {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		for _, name := range names {
			candidate := filepath.Join(dir, name)
			info, err := os.Stat(candidate)
			if err != nil || info.IsDir() {
				continue
			}
			if runtime.GOOS == "windows" || info.Mode().Perm()&0o111 != 0 {
				return candidate, nil
			}
		}
	}
	return "", fmt.Errorf("未找到 Git 可执行文件，请安装 Git 并确保登录 Shell 的 PATH 配置正确")
}

func newGitCommand(args ...string) (*exec.Cmd, error) {
	pathValue := os.Getenv("PATH")
	if runtime.GOOS != "windows" {
		pathValue = mergePathLists(
			core.LoginShellPath(),
			pathValue,
			"/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin",
		)
	}
	gitPath, err := resolveGitExecutable(pathValue)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(gitPath, args...)
	cmd.Env = append(os.Environ(),
		"PATH="+pathValue,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_PAGER=cat",
		"GIT_EDITOR=true",
	)
	return cmd, nil
}

// resolveRepoDir 将 repoPath 规范化为绝对路径，并在路径存在时解析符号链接。
// 前端/历史数据可能传入相对路径（如 "claw-specs"），若直接作为 cmd.Dir 使用，
// Go 会相对进程当前工作目录 chdir，路径不存在时报 "chdir xxx: no such file or directory"。
// 这里统一解析为绝对路径，避免依赖进程工作目录；同时消除 macOS /var →
// /private/var 等别名，确保它能与 Git 返回的仓库根目录进行可靠比较。
func resolveRepoDir(repoPath string) string {
	if repoPath == "" {
		return ""
	}
	resolvedPath := filepath.Clean(repoPath)
	if !filepath.IsAbs(resolvedPath) {
		abs, err := filepath.Abs(resolvedPath)
		if err != nil {
			return repoPath
		}
		resolvedPath = abs
	}
	if canonicalPath, err := filepath.EvalSymlinks(resolvedPath); err == nil {
		return filepath.Clean(canonicalPath)
	}
	return resolvedPath
}

// runGit 在 repoPath 目录执行 git，带超时与默认输出上限。
func runGit(repoPath string, timeout time.Duration, args ...string) (gitOutput, error) {
	return runGitWithLimit(repoPath, timeout, gitMaxOutputBytes, args...)
}

// runGitWithLimit 用于确实需要更大完整输出的命令；调用方必须设置明确上限并处理 truncated。
func runGitWithLimit(repoPath string, timeout time.Duration, outputLimit int, args ...string) (gitOutput, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd, err := newGitCommand(args...)
	if err != nil {
		return gitOutput{exitCode: -1}, err
	}
	core.PrepareProcessTree(cmd)
	if dir := resolveRepoDir(repoPath); dir != "" {
		cmd.Dir = dir
	}
	stdout := core.NewCappedWriter(outputLimit)
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
	case <-ctx.Done():
		_ = core.KillProcessTree(cmd)
		runErr = <-done
	}
	stdoutBytes, stdoutTruncated := stdout.Bytes()
	stderrBytes, stderrTruncated := stderr.Bytes()

	out := gitOutput{
		stdout:    stdoutBytes,
		stderr:    stderrBytes,
		truncated: stdoutTruncated || stderrTruncated,
		exitCode:  -1,
	}
	if ctx.Err() == context.DeadlineExceeded {
		out.timedOut = true
		return out, nil
	}
	if cmd.ProcessState != nil {
		out.exitCode = cmd.ProcessState.ExitCode()
	}
	// runErr 非退出码类错误（如 git 不存在）向上抛出。
	if runErr != nil {
		if _, ok := runErr.(*exec.ExitError); !ok {
			return out, runErr
		}
	}
	return out, nil
}

// runGitStdin：与 runGit 相同，但把 stdinData 作为标准输入喂给 git。
func runGitStdin(repoPath, stdinData string, timeout time.Duration, args ...string) (gitOutput, error) {
	return runGitStdinEnv(repoPath, stdinData, timeout, nil, args...)
}

// runGitStdinEnv 仅供需要隔离 Git 内部状态的命令使用，例如通过临时 Index
// 构造只包含所选文件的 Stash。extraEnv 会覆盖同名环境变量。
func runGitStdinEnv(repoPath, stdinData string, timeout time.Duration, extraEnv map[string]string, args ...string) (gitOutput, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd, err := newGitCommand(args...)
	if err != nil {
		return gitOutput{exitCode: -1}, err
	}
	core.PrepareProcessTree(cmd)
	if dir := resolveRepoDir(repoPath); dir != "" {
		cmd.Dir = dir
	}
	for key, value := range extraEnv {
		prefix := key + "="
		filtered := cmd.Env[:0]
		for _, entry := range cmd.Env {
			if !strings.HasPrefix(entry, prefix) {
				filtered = append(filtered, entry)
			}
		}
		cmd.Env = append(filtered, prefix+value)
	}
	cmd.Stdin = strings.NewReader(stdinData)

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
	case <-ctx.Done():
		_ = core.KillProcessTree(cmd)
		runErr = <-done
	}
	stdoutBytes, stdoutTruncated := stdout.Bytes()
	stderrBytes, stderrTruncated := stderr.Bytes()

	out := gitOutput{
		stdout:    stdoutBytes,
		stderr:    stderrBytes,
		truncated: stdoutTruncated || stderrTruncated,
		exitCode:  -1,
	}
	if ctx.Err() == context.DeadlineExceeded {
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

func gitStdoutString(o gitOutput) string {
	return string(o.stdout)
}

func gitStderrString(o gitOutput) string {
	return strings.TrimSpace(string(o.stderr))
}

func gitSucceeded(o gitOutput) bool {
	return !o.timedOut && o.exitCode == 0
}

// gitEnsureSuccess 在失败时返回带上下文的错误。
func gitEnsureSuccess(o gitOutput, label string) error {
	if o.timedOut {
		return core.ErrString(label + ": git 命令超时")
	}
	if o.exitCode != 0 {
		detail := gitStderrString(o)
		if detail == "" {
			detail = "exit code " + strconv.Itoa(o.exitCode)
		}
		return core.ErrString(label + " 失败: " + detail)
	}
	if o.truncated {
		return core.ErrString(label + ": git 输出超过大小上限")
	}
	return nil
}

// gitLine 返回 stdout 第一行（trim），退出码非 0 或空则返回 ("", false)。
func gitLine(repoPath string, args ...string) (string, bool, error) {
	o, err := runGit(repoPath, gitDefaultTimeout, args...)
	if err != nil {
		return "", false, err
	}
	if o.timedOut {
		return "", false, core.ErrString("git 命令超时")
	}
	if o.exitCode != 0 {
		return "", false, nil
	}
	for _, l := range strings.Split(gitStdoutString(o), "\n") {
		l = strings.TrimRight(strings.TrimRight(l, "\r"), " ")
		l = strings.TrimSpace(l)
		if l != "" {
			return l, true, nil
		}
	}
	return "", false, nil
}

// gitLines 返回非空行列表（退出码非 0 返回空）。
func gitLines(repoPath string, args ...string) ([]string, error) {
	o, err := runGit(repoPath, gitDefaultTimeout, args...)
	if err != nil {
		return nil, err
	}
	if o.timedOut {
		return nil, core.ErrString("git 命令超时")
	}
	if o.exitCode != 0 {
		return []string{}, nil
	}
	var out []string
	for _, l := range strings.Split(gitStdoutString(o), "\n") {
		l = strings.TrimRight(l, "\r")
		if l != "" {
			out = append(out, l)
		}
	}
	return out, nil
}

// gitShowText 执行 `git show --no-textconv <spec>` 并按文本编码解码，失败或二进制内容返回空串。
func gitShowText(repoPath, spec string) string {
	text, _ := gitShowDecodedText(repoPath, spec)
	return text
}

// gitShowDecodedText 的 bool 表示内容是否适合交给 Monaco 文本编辑器。
// Git 中不存在该版本时仍按空文本处理，只有成功读取到二进制内容时返回 false。
func gitShowDecodedText(repoPath, spec string) (string, bool) {
	o, err := runGit(repoPath, gitDefaultTimeout, "show", "--no-textconv", spec)
	if err != nil || !gitSucceeded(o) {
		return "", true
	}
	return decodeGitText(o.stdout)
}

func decodeGitText(data []byte) (string, bool) {
	if len(data) == 0 {
		return "", true
	}
	if bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) {
		data = data[3:]
	}
	if bytes.HasPrefix(data, []byte{0xff, 0xfe}) {
		return decodeGitUTF16(data[2:], binary.LittleEndian)
	}
	if bytes.HasPrefix(data, []byte{0xfe, 0xff}) {
		return decodeGitUTF16(data[2:], binary.BigEndian)
	}
	if order, ok := detectGitUTF16(data); ok {
		return decodeGitUTF16(data, order)
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return "", false
	}
	return string(data), true
}

func detectGitUTF16(data []byte) (binary.ByteOrder, bool) {
	if len(data) < 4 || len(data)%2 != 0 {
		return nil, false
	}
	pairs := len(data) / 2
	evenZeros := 0
	oddZeros := 0
	for index, value := range data {
		if value != 0 {
			continue
		}
		if index%2 == 0 {
			evenZeros++
		} else {
			oddZeros++
		}
	}
	if oddZeros*3 >= pairs*2 && evenZeros*10 <= pairs {
		return binary.LittleEndian, true
	}
	if evenZeros*3 >= pairs*2 && oddZeros*10 <= pairs {
		return binary.BigEndian, true
	}
	return nil, false
}

func decodeGitUTF16(data []byte, order binary.ByteOrder) (string, bool) {
	if len(data)%2 != 0 {
		return "", false
	}
	units := make([]uint16, len(data)/2)
	for index := range units {
		units[index] = order.Uint16(data[index*2 : index*2+2])
	}
	text := string(utf16.Decode(units))
	if strings.IndexByte(text, 0) >= 0 {
		return "", false
	}
	return text, true
}

// resolveGitRepositoryRoot resolves any directory inside a Git worktree to the
// worktree root. Editor tabs may originate from a nested local-project folder,
// while Git object specifications always use repository-root-relative paths.
func resolveGitRepositoryRoot(repoPath string) (string, error) {
	trimmedPath := strings.TrimSpace(repoPath)
	if trimmedPath == "" {
		return "", core.ErrString("repo_path_empty")
	}

	resolvedPath := resolveRepoDir(trimmedPath)
	o, err := runGit(resolvedPath, gitDefaultTimeout, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("打开仓库 %q 失败: %w", resolvedPath, err)
	}
	if err := gitEnsureSuccess(o, "打开仓库"); err != nil {
		return "", fmt.Errorf("打开仓库目录 %q 失败: %w", resolvedPath, err)
	}

	root := strings.TrimSpace(gitStdoutString(o))
	if root == "" {
		return "", core.ErrString("git_repository_root_empty")
	}
	return resolveRepoDir(root), nil
}

// gitEnsureRepo 验证目录是 Git 仓库。保留 --git-dir 的语义，以便不要求
// 调用方一定是可检出的工作树；需要工作树根目录的调用方使用
// resolveGitRepositoryRoot。
func gitEnsureRepo(repoPath string) error {
	trimmedPath := strings.TrimSpace(repoPath)
	if trimmedPath == "" {
		return core.ErrString("repo_path_empty")
	}

	resolvedPath := resolveRepoDir(trimmedPath)
	o, err := runGit(resolvedPath, gitDefaultTimeout, "rev-parse", "--git-dir")
	if err != nil {
		return fmt.Errorf("打开仓库 %q 失败: %w", resolvedPath, err)
	}
	if err := gitEnsureSuccess(o, "打开仓库"); err != nil {
		return fmt.Errorf("打开仓库目录 %q 失败: %w", resolvedPath, err)
	}
	return nil
}

// gitBuildRefMap 构建 commit hash → refs 列表。
func gitBuildRefMap(repoPath string) map[string][]string {
	lines, err := gitLines(repoPath, "for-each-ref",
		"--format=%(objectname) %(refname:short)", "refs/heads", "refs/tags")
	m := map[string][]string{}
	if err != nil {
		return m
	}
	for _, line := range lines {
		if oid, name, ok := strings.Cut(line, " "); ok {
			if len(oid) >= 7 && name != "" {
				m[oid] = append(m[oid], name)
			}
		}
	}
	return m
}

// ---- 路径规范化（对齐 utils.rs normalize_git_relative_path） ----

// normalizeGitRelativePath normalizes a path relative to repoPath. Callers that
// accept a nested project directory should use normalizeGitRelativePathInRepository
// with the discovered repository root instead.
func normalizeGitRelativePath(repoPath, relativePath string) (string, error) {
	return normalizeGitRelativePathInRepository(repoPath, resolveRepoDir(repoPath), relativePath)
}

// normalizeGitRelativePathInRepository accepts an absolute file path or a path
// relative to repoPath, then returns a repository-root-relative Git path. Keeping
// these two bases separate is essential when an editor opens a nested directory
// inside a larger Git repository.
func normalizeGitRelativePathInRepository(repoPath, repositoryRoot, relativePath string) (string, error) {
	raw := strings.TrimSpace(relativePath)
	if raw == "" {
		return "", core.ErrString("relative_path_empty")
	}

	base := resolveRepoDir(strings.TrimSpace(repoPath))
	root := resolveRepoDir(strings.TrimSpace(repositoryRoot))
	if base == "" || root == "" {
		return "", core.ErrString("repo_path_empty")
	}

	// Frontend paths are slash-normalized. Preserve the existing cross-platform
	// behaviour for legacy callers that send Windows-style relative paths.
	raw = strings.ReplaceAll(raw, "\\", "/")
	var absolutePath string
	if filepath.IsAbs(raw) {
		absolutePath = resolveRepoDir(raw)
	} else {
		absolutePath = filepath.Join(base, filepath.FromSlash(raw))
	}

	relative, err := filepath.Rel(root, absolutePath)
	if err != nil {
		return "", core.ErrString("path_escapes_repo_root")
	}
	normalized := filepath.ToSlash(relative)
	if normalized == "." || normalized == "" || normalized == ".." || strings.HasPrefix(normalized, "../") {
		return "", core.ErrString("path_escapes_repo_root")
	}
	for _, segment := range strings.Split(normalized, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", core.ErrString("invalid_relative_path")
		}
	}
	return normalized, nil
}

// langFromPath：由扩展名推断 Monaco 语言 ID（对齐 utils.rs lang_from_path）。
func langFromPath(path string) string {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	switch ext {
	case "ts", "tsx":
		return "typescript"
	case "js", "jsx", "mjs", "cjs":
		return "javascript"
	case "rs":
		return "rust"
	case "py":
		return "python"
	case "go":
		return "go"
	case "java":
		return "java"
	case "kt":
		return "kotlin"
	case "swift":
		return "swift"
	case "c", "h":
		return "c"
	case "cpp", "cxx", "cc", "hpp":
		return "cpp"
	case "cs":
		return "csharp"
	case "html", "htm":
		return "html"
	case "css":
		return "css"
	case "scss", "sass":
		return "scss"
	case "json":
		return "json"
	case "toml":
		return "ini"
	case "yaml", "yml":
		return "yaml"
	case "xml":
		return "xml"
	case "md", "mdx":
		return "markdown"
	case "sql":
		return "sql"
	case "sh", "bash", "zsh":
		return "shell"
	case "dockerfile":
		return "dockerfile"
	default:
		return "plaintext"
	}
}

func mapIndexStatus(c byte) string {
	switch c {
	case 'A':
		return "added"
	case 'D':
		return "deleted"
	case 'R':
		return "renamed"
	case 'T':
		return "typechange"
	default:
		return "modified"
	}
}

func mapWorktreeStatus(c byte) string {
	switch c {
	case 'D':
		return "deleted"
	case 'R':
		return "renamed"
	case 'T':
		return "typechange"
	default:
		return "modified"
	}
}

func nameStatusToStatus(c byte) string {
	switch c {
	case 'A':
		return "added"
	case 'D':
		return "deleted"
	case 'R', 'C':
		return "renamed"
	case 'T':
		return "typechange"
	default:
		return "modified"
	}
}

func strPtr(s string) *string { return &s }
