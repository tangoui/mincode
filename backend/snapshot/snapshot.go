// Package snapshot 把项目工作区拍成私有 Git 对象库里的树对象，并比较两棵树。
//
// 每个项目在 core.UserDataDir()/snapshots/<sha256(规范化项目根)[:16]>/ 下有独立存储：
// git/ 是私有 GIT_DIR（git init --bare），index 是私有 GIT_INDEX_FILE。对私有库的
// 每条命令都显式设置 GIT_DIR、GIT_WORK_TREE、GIT_INDEX_FILE；对项目自身仓库只执行
// 只读命令（rev-parse、symbolic-ref、ls-files），因此绝不改动项目的暂存区、引用、
// 贮藏、钩子或配置，也不产生提交。
//
// 快照内容与路径都以传入的项目根目录为准：项目根是仓库子目录时，私有命令以仓库顶层
// 为 GIT_WORK_TREE、以项目根为当前目录，再用 write-tree --prefix 取出项目根对应的
// 子树，树内路径因此相对于项目根。
//
// 快照包含哪些文件完全由项目视角决定（项目仓库的 ls-files -c -o --exclude-standard，
// 遵循 .gitignore、info/exclude 和用户全局忽略规则），私有索引逐路径按这份清单维护。
package snapshot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"mincode/app/backend/core"
	projectfs "mincode/app/backend/fs"
)

const (
	// maxFileBytes：超过 10 MiB 的文件不进快照，记入 Capture.Skipped。
	maxFileBytes = 10 << 20
	// emptyTree 是 SHA-1 对象格式下的空树 ID。
	emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	// UnsupportedNotGit 表示项目根不在 Git 工作区内。
	UnsupportedNotGit = "not_git"
)

// privateAttributes 写入私有库的 info/attributes（优先级最高），关闭换行转换、
// clean/smudge 过滤器（如 git-lfs）、ident 和编码转换：快照保存工作区的原始字节，
// 也不会因过滤器把内容写到别处。
const privateAttributes = "* -text -filter -ident -working-tree-encoding\n"

// storeBaseDir 返回所有项目快照存储的上级目录；测试替换为临时目录。
var storeBaseDir = func() string { return filepath.Join(core.UserDataDir(), "snapshots") }

// Capture 是一次拍快照的结果。非 Git 项目时 Unsupported 为 "not_git"，其余为空。
// ProjectRoot 是规范化后的项目根目录（绝对路径、解析符号链接），前端以它为记录键，
// 同一项目的不同写法（尾部分隔符、".."、符号链接）因此只对应一份记录。
type Capture struct {
	ProjectRoot string   `json:"projectRoot"`
	Tree        string   `json:"tree"`
	Head        string   `json:"head"`
	Branch      string   `json:"branch"`
	Skipped     []string `json:"skipped"`
	Unsupported string   `json:"unsupported"`
}

// FileChange 是两棵快照树之间一个路径的变化。Status 取 "added" | "modified" |
// "deleted" | "submodule"（任一侧为 gitlink，即嵌套子仓库）。
type FileChange struct {
	Path   string `json:"path"`
	Status string `json:"status"`
}

// store 描述一个项目的私有快照存储。
type store struct {
	dir      string // 存储根目录
	gitDir   string // 私有 GIT_DIR
	index    string // 私有 GIT_INDEX_FILE
	cwd      string // 私有命令的当前目录：规范化项目根
	workTree string // 私有 GIT_WORK_TREE：项目所在仓库的顶层目录
}

var (
	storeLocksMu sync.Mutex
	storeLocks   = map[string]*sync.Mutex{}
)

func lockStore(dir string) func() {
	storeLocksMu.Lock()
	l, ok := storeLocks[dir]
	if !ok {
		l = &sync.Mutex{}
		storeLocks[dir] = l
	}
	storeLocksMu.Unlock()
	l.Lock()
	return l.Unlock
}

// storeDirFor 返回规范化项目根对应的存储目录。
func storeDirFor(canonRoot string) string {
	sum := sha256.Sum256([]byte(canonRoot))
	return filepath.Join(storeBaseDir(), hex.EncodeToString(sum[:])[:16])
}

// openStore 返回项目根对应的存储（不创建任何文件）。GIT_WORK_TREE 默认为项目根，
// 拍快照时改为仓库顶层。
func openStore(canonRoot string) (*store, error) {
	if canonRoot == "" {
		return nil, errors.New("project_root_empty")
	}
	dir := storeDirFor(canonRoot)
	return &store{
		dir:      dir,
		gitDir:   filepath.Join(dir, "git"),
		index:    filepath.Join(dir, "index"),
		cwd:      canonRoot,
		workTree: canonRoot,
	}, nil
}

func (s *store) exists() bool {
	_, err := os.Stat(filepath.Join(s.gitDir, "HEAD"))
	return err == nil
}

// git 在私有库上运行一条 git 命令。全局与系统配置被隔离，用户配置中的过滤器、
// autocrlf、fsmonitor 等不会作用于私有库。
func (s *store) git(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	full := append([]string{
		"-c", "gc.auto=0",
		"-c", "maintenance.auto=false",
		"-c", "core.autocrlf=false",
		"-c", "core.safecrlf=false",
		"-c", "core.fsmonitor=false",
	}, args...)
	return runGit(ctx, s.cwd, map[string]string{
		"GIT_DIR":               s.gitDir,
		"GIT_WORK_TREE":         s.workTree,
		"GIT_INDEX_FILE":        s.index,
		"GIT_CONFIG_NOSYSTEM":   "1",
		"GIT_CONFIG_GLOBAL":     os.DevNull,
		"GIT_LITERAL_PATHSPECS": "1",
	}, stdin, full...)
}

// ensure 创建存储目录并在首次使用时初始化私有裸库；随后校正权限并写入私有属性。
func (s *store) ensure(ctx context.Context) error {
	if err := mkdirPrivate(filepath.Dir(s.dir)); err != nil {
		return err
	}
	if err := mkdirPrivate(s.dir); err != nil {
		return err
	}
	if !s.exists() {
		// 先在临时目录初始化再改名，中途失败不会留下半成品 git/。
		tmp, err := os.MkdirTemp(s.dir, ".git-init-")
		if err != nil {
			return fmt.Errorf("snapshot_store_init_failed: %w", err)
		}
		defer os.RemoveAll(tmp)
		if _, err := runGit(ctx, s.dir, map[string]string{
			"GIT_CONFIG_NOSYSTEM": "1",
			"GIT_CONFIG_GLOBAL":   os.DevNull,
		}, nil, "init", "-q", "--bare", "--template=", "--shared=0600", tmp); err != nil {
			return fmt.Errorf("snapshot_store_init_failed: %w", err)
		}
		if err := os.Chmod(tmp, 0o700); err != nil {
			return err
		}
		if err := os.Rename(tmp, s.gitDir); err != nil {
			return fmt.Errorf("snapshot_store_init_failed: %w", err)
		}
	}
	if err := mkdirPrivate(filepath.Join(s.gitDir, "info")); err != nil {
		return err
	}
	return writePrivateFile(filepath.Join(s.gitDir, "info", "attributes"), []byte(privateAttributes))
}

func mkdirPrivate(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("snapshot_store_dir_failed: %w", err)
	}
	return os.Chmod(dir, 0o700)
}

func writePrivateFile(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// projectRepo 是项目根所在仓库的只读信息。
type projectRepo struct {
	topLevel string // 仓库顶层（git 返回的真实路径）
	prefix   string // 项目根相对顶层的前缀，以 "/" 结尾；项目根即顶层时为空
}

// inspectProject 用只读的 rev-parse 确认项目根位于 Git 工作区内。ok=false 表示不是 Git 项目。
func inspectProject(ctx context.Context, root string) (projectRepo, bool, error) {
	out, err := projectGit(ctx, root, "rev-parse", "--show-toplevel", "--show-prefix")
	if err != nil {
		var ge *gitError
		if errors.As(err, &ge) && ctx.Err() == nil {
			return projectRepo{}, false, nil
		}
		return projectRepo{}, false, err
	}
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(lines) != 2 || lines[0] == "" {
		return projectRepo{}, false, fmt.Errorf("snapshot_unexpected_git_output")
	}
	return projectRepo{topLevel: filepath.FromSlash(lines[0]), prefix: lines[1]}, true, nil
}

// readHead 读取项目的 HEAD 提交与分支名（只读）。尚无提交时 Head 为空，分离 HEAD 时 Branch 为空。
func readHead(ctx context.Context, root string) (head, branch string, err error) {
	out, err := projectGit(ctx, root, "rev-parse", "-q", "--verify", "HEAD^{commit}")
	switch {
	case err == nil:
		head = strings.TrimSpace(string(out))
	case isExit(err, 1) && ctx.Err() == nil:
		head = ""
	default:
		return "", "", err
	}
	out, err = projectGit(ctx, root, "symbolic-ref", "--short", "-q", "HEAD")
	switch {
	case err == nil:
		branch = strings.TrimSpace(string(out))
	case isExit(err, 1) && ctx.Err() == nil:
		branch = ""
	default:
		return "", "", err
	}
	return head, branch, nil
}

// CaptureProject 把项目根当前的工作区写入私有对象库，返回树 ID 以及 HEAD/分支。
// 整个过程受 ctx 约束。
func CaptureProject(ctx context.Context, projectRoot string) (Capture, error) {
	if err := ctx.Err(); err != nil {
		return Capture{}, err
	}
	root, err := projectfs.CanonicalProjectRoot(projectRoot)
	if err != nil {
		return Capture{}, err
	}
	repo, ok, err := inspectProject(ctx, root)
	if err != nil {
		return Capture{}, err
	}
	if !ok {
		return Capture{ProjectRoot: root, Skipped: []string{}, Unsupported: UnsupportedNotGit}, nil
	}
	head, branch, err := readHead(ctx, root)
	if err != nil {
		return Capture{}, err
	}

	st, err := openStore(root)
	if err != nil {
		return Capture{}, err
	}
	st.workTree = repo.topLevel
	unlock := lockStore(st.dir)
	defer unlock()
	if err := st.ensure(ctx); err != nil {
		return Capture{}, err
	}

	// 项目视角下应进入快照的路径：已跟踪 + 未忽略的未跟踪文件（只读命令，遵循项目的
	// .gitignore、info/exclude 以及用户的全局 core.excludesFile）。私有索引完全由这份
	// 清单驱动，私有库不自行判断忽略规则，因此项目视角之外的文件永远不会被哈希。
	out, err := projectGit(ctx, root, "ls-files", "-z", "-c", "-o", "--exclude-standard")
	if err != nil {
		return Capture{}, err
	}
	want := map[string]bool{}
	skipped := []string{}
	for _, p := range splitNUL(out) {
		name := strings.TrimSuffix(p, "/")
		if want[name] {
			continue
		}
		info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			continue // 已删除的跟踪文件：不在工作区，也不进快照
		}
		mode := info.Mode()
		switch {
		case mode.IsRegular():
			if info.Size() > maxFileBytes {
				skipped = append(skipped, name)
				continue
			}
		case mode&os.ModeSymlink != 0:
		case mode.IsDir():
			// 目录条目只会是嵌套仓库或子模块，记录为 gitlink。未初始化的子模块（无 .git）
			// 不记录；没有提交的嵌套仓库无法成为 gitlink（git add 会整体失败），记入
			// Skipped（以 "/" 结尾）。
			if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(name), ".git")); err != nil {
				continue
			}
			if !nestedRepoHasCommit(ctx, filepath.Join(root, filepath.FromSlash(name))) {
				if ctx.Err() != nil {
					return Capture{}, ctx.Err()
				}
				skipped = append(skipped, name+"/")
				continue
			}
		default:
			continue // FIFO、套接字等特殊文件 git 无法记录
		}
		want[name] = true
	}
	sort.Strings(skipped)

	// 先移出私有索引里不再需要的条目（已删除、后来被忽略、变大超限的文件），
	// update-index 只改索引，不哈希任何内容。
	out, err = st.git(ctx, nil, "ls-files", "-z", "-c")
	if err != nil {
		return Capture{}, err
	}
	var extra []string
	for _, p := range splitNUL(out) {
		if !want[p] {
			extra = append(extra, p)
		}
	}
	if err := st.removeFromIndex(ctx, extra); err != nil {
		return Capture{}, err
	}
	// 再按清单逐路径写入（不经忽略规则；stat 缓存让未变化的文件不被重新哈希）。
	if err := st.addPaths(ctx, want); err != nil {
		return Capture{}, err
	}

	tree, err := st.writeTree(ctx, repo.prefix, len(want) == 0)
	if err != nil {
		return Capture{}, err
	}
	if err := os.Chmod(st.index, 0o600); err != nil && !os.IsNotExist(err) {
		return Capture{}, err
	}
	return Capture{ProjectRoot: root, Tree: tree, Head: head, Branch: branch, Skipped: skipped}, nil
}

// addPaths 把清单中的路径写入私有索引。用 update-index --stdin 而不是 git add 的
// pathspec：路径逐字处理、不做 pathspec 匹配（add 对每个条目匹配全部 pathspec，
// 文件多时接近 O(N²)）；stat 信息未变的条目直接跳过、不重新哈希；含提交的嵌套仓库
// 目录记录为 gitlink；列出之后又被删除的路径由 --remove 从索引移除，不会报错。
func (s *store) addPaths(ctx context.Context, want map[string]bool) error {
	if len(want) == 0 {
		return nil
	}
	paths := make([]string, 0, len(want))
	for p := range want {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	_, err := s.git(ctx, nulList(paths), "update-index", "-z", "--add", "--remove", "--stdin")
	return err
}

func (s *store) removeFromIndex(ctx context.Context, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	// update-index 只改私有索引，不碰工作区；git rm --pathspec-from-file 在子目录里会误报
	// "pathspec arguments cannot be used together"，因此不用它。不在索引里的路径被忽略。
	_, err := s.git(ctx, nulList(paths), "update-index", "-z", "--force-remove", "--stdin")
	return err
}

func (s *store) writeTree(ctx context.Context, prefix string, empty bool) (string, error) {
	var out []byte
	var err error
	switch {
	case empty:
		out, err = s.git(ctx, nil, "mktree")
	case prefix == "":
		out, err = s.git(ctx, nil, "write-tree")
	default:
		out, err = s.git(ctx, nil, "write-tree", "--prefix="+prefix)
	}
	if err != nil {
		return "", err
	}
	tree := strings.TrimSpace(string(out))
	if !isObjectID(tree) {
		return "", fmt.Errorf("snapshot_unexpected_git_output")
	}
	return tree, nil
}

func nestedRepoHasCommit(ctx context.Context, dir string) bool {
	_, err := projectGit(ctx, dir, "rev-parse", "-q", "--verify", "HEAD^{commit}")
	return err == nil
}

// DiffTrees 比较同一项目的两棵快照树，返回按路径排序的变化。
func DiffTrees(ctx context.Context, projectRoot, from, to string) ([]FileChange, error) {
	if !isObjectID(from) || !isObjectID(to) {
		return nil, errors.New("snapshot_invalid_tree")
	}
	root, err := projectfs.CanonicalProjectRoot(projectRoot)
	if err != nil {
		return nil, err
	}
	st, err := openStore(root)
	if err != nil {
		return nil, err
	}
	if !st.exists() {
		return nil, errors.New("snapshot_store_missing")
	}
	out, err := st.git(ctx, nil, "diff-tree", "-r", "-z", "--raw", "--no-renames", "--no-commit-id", from, to)
	if err != nil {
		return nil, err
	}
	return parseRawDiff(out)
}

// parseRawDiff 解析 diff-tree -z --raw 输出：":旧模式 新模式 旧ID 新ID 状态\0路径\0"。
func parseRawDiff(out []byte) ([]FileChange, error) {
	fields := splitNUL(out)
	changes := []FileChange{}
	for i := 0; i < len(fields); i++ {
		meta := fields[i]
		if !strings.HasPrefix(meta, ":") || i+1 >= len(fields) {
			return nil, fmt.Errorf("snapshot_unexpected_git_output")
		}
		i++
		parts := strings.Fields(meta[1:])
		if len(parts) != 5 || parts[4] == "" {
			return nil, fmt.Errorf("snapshot_unexpected_git_output")
		}
		status := ""
		switch {
		case parts[0] == "160000" || parts[1] == "160000":
			status = "submodule"
		case parts[4][0] == 'A':
			status = "added"
		case parts[4][0] == 'D':
			status = "deleted"
		default: // M、T 等
			status = "modified"
		}
		changes = append(changes, FileChange{Path: fields[i], Status: status})
	}
	return changes, nil
}

func isObjectID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func splitNUL(b []byte) []string {
	var out []string
	for _, part := range bytes.Split(b, []byte{0}) {
		if len(part) > 0 {
			out = append(out, string(part))
		}
	}
	return out
}

func nulList(paths []string) []byte {
	var b bytes.Buffer
	for _, p := range paths {
		b.WriteString(p)
		b.WriteByte(0)
	}
	return b.Bytes()
}

// ---------- git 进程封装（对齐 backend/git runGitStdinEnv 的写法，改为受 ctx 约束） ----------

// gitError 表示 git 以非零状态退出。
type gitError struct {
	args     []string
	exitCode int
	stderr   string
}

func (e *gitError) Error() string {
	msg := strings.TrimSpace(e.stderr)
	if msg == "" {
		msg = fmt.Sprintf("exit status %d", e.exitCode)
	}
	sub := ""
	for i := 0; i < len(e.args); i++ {
		if e.args[i] == "-c" || e.args[i] == "-C" {
			i++
			continue
		}
		sub = e.args[i]
		break
	}
	return fmt.Sprintf("snapshot_git_failed: git %s: %s", sub, msg)
}

func isExit(err error, code int) bool {
	var ge *gitError
	return errors.As(err, &ge) && ge.exitCode == code
}

// gitTool 解析一次 git 可执行文件及其 PATH（并入登录 shell 的 PATH，弥补从 Finder 启动时的精简 PATH）。
type gitTool struct {
	path    string
	envPath string
}

var gitExecutable = sync.OnceValues(func() (gitTool, error) {
	pathValue := os.Getenv("PATH")
	if runtime.GOOS != "windows" {
		pathValue = mergePathLists(core.LoginShellPath(), pathValue, "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin")
	}
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
				return gitTool{path: candidate, envPath: pathValue}, nil
			}
		}
	}
	return gitTool{}, errors.New("snapshot_git_not_found")
})

func mergePathLists(paths ...string) string {
	seen := map[string]bool{}
	var merged []string
	for _, pathValue := range paths {
		for _, dir := range filepath.SplitList(pathValue) {
			dir = strings.TrimSpace(dir)
			if dir == "" || seen[dir] {
				continue
			}
			seen[dir] = true
			merged = append(merged, dir)
		}
	}
	return strings.Join(merged, string(os.PathListSeparator))
}

// projectGit 在项目仓库里运行只读 git 命令。关掉 fsmonitor 和未跟踪缓存：
// 项目配置的 fsmonitor 钩子不会被运行，也不会为写缓存去动项目的索引。
func projectGit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	full := append([]string{"-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false"}, args...)
	return runGit(ctx, dir, nil, nil, full...)
}

// runGit 在 dir 中运行 git。继承环境里的所有 GIT_* 变量都会被去掉，避免外部的
// GIT_DIR/GIT_INDEX_FILE 等把命令指向别的仓库；extraEnv 再显式设置所需变量。
// 参数原样传给 git（不经过 shell），路径中的空格、中文不需要转义。
func runGit(ctx context.Context, dir string, extraEnv map[string]string, stdin []byte, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tool, err := gitExecutable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(tool.path, args...)
	core.PrepareProcessTree(cmd)
	cmd.Dir = dir
	env := make([]string, 0, len(os.Environ())+8)
	for _, entry := range os.Environ() {
		upper := strings.ToUpper(entry)
		if strings.HasPrefix(upper, "GIT_") || strings.HasPrefix(upper, "PATH=") {
			continue
		}
		env = append(env, entry)
	}
	env = append(env,
		"PATH="+tool.envPath,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_PAGER=cat",
		"GIT_EDITOR=true",
	)
	for key, value := range extraEnv {
		env = append(env, key+"="+value)
	}
	cmd.Env = env
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout bytes.Buffer
	stderr := core.NewCappedWriter(64 << 10)
	cmd.Stdout = &stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var runErr error
	select {
	case runErr = <-done:
	case <-ctx.Done():
		_ = core.KillProcessTree(cmd)
		<-done
		return nil, ctx.Err()
	}
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			errBytes, _ := stderr.Bytes()
			return stdout.Bytes(), &gitError{args: args, exitCode: exitErr.ExitCode(), stderr: string(errBytes)}
		}
		return nil, runErr
	}
	return stdout.Bytes(), nil
}
