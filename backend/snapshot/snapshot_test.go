package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"mincode/app/backend/core"
	projectfs "mincode/app/backend/fs"
)

var ctx = context.Background()

// TestMain points the store at a temporary directory so no test ever writes to
// the real user data directory.
func TestMain(m *testing.M) {
	base, err := os.MkdirTemp("", "mincode-snapshot-store-*")
	if err != nil {
		panic(err)
	}
	storeBaseDir = func() string { return filepath.Join(base, "snapshots") }
	code := m.Run()
	_ = os.RemoveAll(base)
	os.Exit(code)
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
}

func require(t *testing.T, ok bool, args ...any) {
	t.Helper()
	if !ok {
		t.Fatal(append([]any{"requirement failed"}, args...)...)
	}
}

// runTestGit runs git in dir with a clean, isolated configuration so the
// developer's own config cannot influence test fixtures.
func runTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return string(out)
}

// newTestRepo creates <tmp>/<name> as a Git repository with one commit that
// tracks tracked.txt. The identity is set in the repository's local config only.
func newTestRepo(t *testing.T, name string) string {
	t.Helper()
	requireGit(t)
	root := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	canon, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	initTestRepo(t, canon)
	write(t, canon, "tracked.txt", "tracked")
	runTestGit(t, canon, "add", "tracked.txt")
	runTestGit(t, canon, "commit", "-q", "-m", "initial")
	return canon
}

func initTestRepo(t *testing.T, dir string) {
	t.Helper()
	runTestGit(t, dir, "init", "-q", "-b", "main")
	runTestGit(t, dir, "config", "user.name", "Snapshot Test")
	runTestGit(t, dir, "config", "user.email", "snapshot@test.invalid")
	runTestGit(t, dir, "config", "commit.gpgsign", "false")
}

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeSized(t *testing.T, root, rel string, size int64) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func remove(t *testing.T, root, rel string) {
	t.Helper()
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
		t.Fatal(err)
	}
}

func hashFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "missing"
		}
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// gitState summarises everything a snapshot must never touch in the project's
// own repository: index, refs, stash, HEAD, config and hooks, plus a digest of
// every file under .git.
func gitState(t *testing.T, root string) string {
	t.Helper()
	gitDir := filepath.Join(root, ".git")
	var hooks []string
	entries, _ := os.ReadDir(filepath.Join(gitDir, "hooks"))
	for _, e := range entries {
		hooks = append(hooks, e.Name())
	}
	head := strings.TrimSpace(runTestGit(t, root, "rev-parse", "HEAD"))
	symbolic := strings.TrimSpace(runTestGit(t, root, "symbolic-ref", "-q", "HEAD"))
	refs := runTestGit(t, root, "for-each-ref", "--format=%(refname) %(objectname)")
	stash := runTestGit(t, root, "stash", "list")
	all := sha256.New()
	_ = filepath.WalkDir(gitDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(gitDir, path)
		all.Write([]byte(rel + "\x00"))
		if !d.IsDir() {
			data, _ := os.ReadFile(path)
			all.Write(data)
		}
		return nil
	})
	return strings.Join([]string{
		"index=" + hashFile(t, filepath.Join(gitDir, "index")),
		"head=" + head + " " + symbolic,
		"refs=" + refs,
		"stash=" + stash,
		"config=" + hashFile(t, filepath.Join(gitDir, "config")),
		"hooks=" + strings.Join(hooks, ","),
		"all=" + hex.EncodeToString(all.Sum(nil)),
	}, "\n")
}

func assertChanges(t *testing.T, changes []FileChange, want map[string]string) {
	t.Helper()
	got := map[string]string{}
	for _, c := range changes {
		got[c.Path] = c.Status
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("changes = %v, want %v", got, want)
	}
}

// treeFiles lists the paths recorded in a snapshot tree.
func treeFiles(t *testing.T, root, tree string) []string {
	t.Helper()
	changes, err := DiffTrees(ctx, root, emptyTree, tree)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, c := range changes {
		out = append(out, c.Path)
	}
	sort.Strings(out)
	return out
}

func contains(list []string, item string) bool {
	for _, v := range list {
		if v == item {
			return true
		}
	}
	return false
}

func TestCaptureRoundTripLeavesProjectGitUntouched(t *testing.T) {
	root := newTestRepo(t, "项目 with space")
	before := gitState(t, root)
	write(t, root, "a.txt", "one")
	c1, err := CaptureProject(ctx, root)
	require(t, err == nil && c1.Tree != "" && c1.Unsupported == "", err, c1)
	write(t, root, "a.txt", "two")
	write(t, root, "b.txt", "new")
	remove(t, root, "tracked.txt")
	c2, err := CaptureProject(ctx, root)
	require(t, err == nil, err)
	changes, err := DiffTrees(ctx, root, c1.Tree, c2.Tree)
	require(t, err == nil, err)
	assertChanges(t, changes, map[string]string{"a.txt": "modified", "b.txt": "added", "tracked.txt": "deleted"})
	if after := gitState(t, root); after != before {
		t.Fatalf("project git state changed: %v -> %v", before, after)
	}
	if got := treeFiles(t, root, c1.Tree); !reflect.DeepEqual(got, []string{"a.txt", "tracked.txt"}) {
		t.Fatalf("tree files = %v", got)
	}
}

func TestCaptureHonoursGitignoreAndSkipsLargeFiles(t *testing.T) {
	root := newTestRepo(t, "忽略 规则")
	write(t, root, ".gitignore", "dist/\n")
	write(t, root, "dist/out.js", "built")
	write(t, root, "src/main.go", "package main")
	write(t, root, ".git/info/exclude", "secret.local\n")
	write(t, root, "secret.local", "x")
	writeSized(t, root, "big.bin", 11<<20)
	odd := "#odd [x]*?! .bin "
	if runtime.GOOS == "windows" {
		odd = "#odd [x]! .bin"
	}
	writeSized(t, root, "nested dir/"+odd, 11<<20)
	writeSized(t, root, "edge.bin", 10<<20) // exactly 10 MiB is kept

	c1, err := CaptureProject(ctx, root)
	require(t, err == nil, err)
	wantSkipped := []string{"big.bin", "nested dir/" + odd}
	sort.Strings(wantSkipped)
	if !reflect.DeepEqual(c1.Skipped, wantSkipped) {
		t.Fatalf("Skipped = %q, want %q", c1.Skipped, wantSkipped)
	}
	files := treeFiles(t, root, c1.Tree)
	want := []string{".gitignore", "edge.bin", "src/main.go", "tracked.txt"}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("tree files = %q, want %q", files, want)
	}

	// A file that shrinks back under the limit is captured again, and one that
	// grows over it drops out even though the private index already holds it.
	write(t, root, "big.bin", "small now")
	writeSized(t, root, "edge.bin", 10<<20+1)
	c2, err := CaptureProject(ctx, root)
	require(t, err == nil, err)
	wantSkipped = []string{"edge.bin", "nested dir/" + odd}
	sort.Strings(wantSkipped)
	if !reflect.DeepEqual(c2.Skipped, wantSkipped) {
		t.Fatalf("second Skipped = %q, want %q", c2.Skipped, wantSkipped)
	}
	changes, err := DiffTrees(ctx, root, c1.Tree, c2.Tree)
	require(t, err == nil, err)
	assertChanges(t, changes, map[string]string{"big.bin": "added", "edge.bin": "deleted"})
}

func TestCaptureIncludesTrackedIgnoredAndDropsNewlyIgnored(t *testing.T) {
	root := newTestRepo(t, "tracked ignored")
	write(t, root, "build/keep.txt", "forced")
	runTestGit(t, root, "add", "-f", "build/keep.txt")
	runTestGit(t, root, "commit", "-q", "-m", "force")
	write(t, root, ".gitignore", "build/\n")
	write(t, root, "later.log", "log")
	c1, err := CaptureProject(ctx, root)
	require(t, err == nil, err)
	files := treeFiles(t, root, c1.Tree)
	require(t, contains(files, "build/keep.txt") && contains(files, "later.log"), files)

	write(t, root, ".gitignore", "build/\n*.log\n")
	c2, err := CaptureProject(ctx, root)
	require(t, err == nil, err)
	files = treeFiles(t, root, c2.Tree)
	require(t, contains(files, "build/keep.txt") && !contains(files, "later.log"), files)
}

func TestCaptureKeepsBytesExactDespiteAttributes(t *testing.T) {
	root := newTestRepo(t, "属性 crlf")
	write(t, root, ".gitattributes", "* text=auto eol=lf\n*.bin filter=lfs diff=lfs merge=lfs -text\n")
	write(t, root, "crlf.txt", "a\r\nb\r\n")
	c, err := CaptureProject(ctx, root)
	require(t, err == nil, err)
	st, err := openStore(root)
	require(t, err == nil, err)
	out, err := st.git(ctx, nil, "cat-file", "blob", c.Tree+":crlf.txt")
	require(t, err == nil, err)
	if string(out) != "a\r\nb\r\n" {
		t.Fatalf("blob = %q", out)
	}
}

func TestCaptureReportsNonGitProject(t *testing.T) {
	requireGit(t)
	dir := filepath.Join(t.TempDir(), "普通 目录")
	require(t, os.MkdirAll(dir, 0o755) == nil)
	write(t, dir, "a.txt", "x")
	c, err := CaptureProject(ctx, dir)
	require(t, err == nil, err)
	if c.Unsupported != "not_git" || c.Tree != "" || c.Head != "" || c.Branch != "" || len(c.Skipped) != 0 {
		t.Fatalf("capture = %+v", c)
	}
	canon, _ := filepath.EvalSymlinks(dir)
	if c.ProjectRoot != canon {
		t.Fatalf("projectRoot = %q, want %q", c.ProjectRoot, canon)
	}
	if _, err := os.Stat(storeDirFor(canon)); !os.IsNotExist(err) {
		t.Fatalf("store created for non-git project: %v", err)
	}
}

func TestCaptureRecordsHeadAndBranch(t *testing.T) {
	root := newTestRepo(t, "分支 head")
	c1, err := CaptureProject(ctx, root)
	require(t, err == nil, err)
	head := strings.TrimSpace(runTestGit(t, root, "rev-parse", "HEAD"))
	require(t, c1.Head == head && c1.Branch == "main", c1)

	runTestGit(t, root, "checkout", "-q", "-b", "feature/x")
	c2, err := CaptureProject(ctx, root)
	require(t, err == nil, err)
	require(t, c2.Head == head && c2.Branch == "feature/x", c2)

	runTestGit(t, root, "checkout", "-q", "--detach")
	c3, err := CaptureProject(ctx, root)
	require(t, err == nil, err)
	require(t, c3.Head == head && c3.Branch == "", c3)
}

func TestCaptureEmptyRepository(t *testing.T) {
	requireGit(t)
	root := filepath.Join(t.TempDir(), "空 仓库")
	require(t, os.MkdirAll(root, 0o755) == nil)
	root, _ = filepath.EvalSymlinks(root)
	initTestRepo(t, root)
	write(t, root, "a.txt", "x")
	c, err := CaptureProject(ctx, root)
	require(t, err == nil, err)
	require(t, c.Head == "" && c.Branch == "main" && c.Tree != "" && c.Unsupported == "", c)
	require(t, reflect.DeepEqual(treeFiles(t, root, c.Tree), []string{"a.txt"}))
}

func TestCaptureNestedRepositoriesAreSubmodules(t *testing.T) {
	root := newTestRepo(t, "嵌套 nested")
	c1, err := CaptureProject(ctx, root)
	require(t, err == nil, err)

	inner := filepath.Join(root, "inner repo")
	require(t, os.MkdirAll(inner, 0o755) == nil)
	initTestRepo(t, inner)
	write(t, inner, "x.txt", "x")
	runTestGit(t, inner, "add", "x.txt")
	runTestGit(t, inner, "commit", "-q", "-m", "inner")
	empty := filepath.Join(root, "fresh")
	require(t, os.MkdirAll(empty, 0o755) == nil)
	initTestRepo(t, empty) // no commit yet: cannot be a gitlink
	write(t, empty, "y.txt", "y")

	c2, err := CaptureProject(ctx, root)
	require(t, err == nil, err)
	require(t, reflect.DeepEqual(c2.Skipped, []string{"fresh/"}), c2.Skipped)
	changes, err := DiffTrees(ctx, root, c1.Tree, c2.Tree)
	require(t, err == nil, err)
	assertChanges(t, changes, map[string]string{"inner repo": "submodule"})
}

func TestCaptureSubdirectoryRootUsesRepositoryIgnoreRules(t *testing.T) {
	top := newTestRepo(t, "top 顶层")
	write(t, top, ".gitignore", "*.log\n")
	write(t, top, "outside.txt", "o")
	write(t, top, "pkg/sub/a.txt", "a")
	write(t, top, "pkg/sub/debug.log", "l")
	root := filepath.Join(top, "pkg", "sub")
	c, err := CaptureProject(ctx, root)
	require(t, err == nil, err)
	require(t, reflect.DeepEqual(treeFiles(t, root, c.Tree), []string{"a.txt"}), treeFiles(t, root, c.Tree))

	writeSized(t, root, "big.dat", 11<<20)
	c2, err := CaptureProject(ctx, root)
	require(t, err == nil, err)
	require(t, reflect.DeepEqual(c2.Skipped, []string{"big.dat"}), c2.Skipped)
	require(t, c2.Tree == c.Tree, "large file must not change the tree")

	// An empty subdirectory still yields a valid (empty) tree.
	emptyRoot := filepath.Join(top, "nothing here")
	require(t, os.MkdirAll(emptyRoot, 0o755) == nil)
	c3, err := CaptureProject(ctx, emptyRoot)
	require(t, err == nil, err)
	require(t, c3.Tree == emptyTree, c3)
}

func TestStorePermissionsAndLocation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions")
	}
	root := newTestRepo(t, "权限 perms")
	write(t, root, "a.txt", "x")
	_, err := CaptureProject(ctx, root)
	require(t, err == nil, err)
	sum := sha256.Sum256([]byte(root))
	dir := filepath.Join(storeBaseDir(), hex.EncodeToString(sum[:])[:16])
	require(t, storeDirFor(root) == dir, storeDirFor(root), dir)
	for _, p := range []string{filepath.Join(dir, "index"), filepath.Join(dir, "git", "HEAD")} {
		_, err := os.Stat(p)
		require(t, err == nil, err)
	}
	checked := 0
	err = filepath.WalkDir(storeBaseDir(), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s has mode %v", path, info.Mode().Perm())
		}
		if d.IsDir() && info.Mode().Perm() != 0o700 {
			t.Errorf("dir %s has mode %v", path, info.Mode().Perm())
		}
		checked++
		return nil
	})
	require(t, err == nil && checked > 5, err, checked)
}

func TestDiffTreesRejectsInvalidIDs(t *testing.T) {
	root := newTestRepo(t, "bad ids")
	c, err := CaptureProject(ctx, root)
	require(t, err == nil, err)
	for _, bad := range []string{"", "--output=/tmp/x", "HEAD", c.Tree + "x"} {
		if _, err := DiffTrees(ctx, root, bad, c.Tree); err == nil {
			t.Errorf("DiffTrees accepted from=%q", bad)
		}
	}
}

func TestCaptureHonoursContextCancel(t *testing.T) {
	root := newTestRepo(t, "cancel")
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := CaptureProject(cctx, root); err == nil {
		t.Fatal("expected error for cancelled context")
	}
	tctx, cancel2 := context.WithTimeout(ctx, time.Nanosecond)
	defer cancel2()
	time.Sleep(time.Millisecond)
	if _, err := CaptureProject(tctx, root); err == nil {
		t.Fatal("expected error for expired context")
	}
}

func TestHandlersUseCamelCase(t *testing.T) {
	root := newTestRepo(t, "handlers 命令")
	write(t, root, "a.txt", "1")
	args, _ := json.Marshal(map[string]string{"projectRoot": root + string(filepath.Separator) + "."})
	out, err := core.DispatchContext(ctx, "ai_snapshot_capture", string(args))
	require(t, err == nil, err)
	var raw map[string]any
	require(t, json.Unmarshal([]byte(out), &raw) == nil, out)
	for _, key := range []string{"projectRoot", "tree", "head", "branch", "skipped", "unsupported"} {
		_, ok := raw[key]
		require(t, ok, "missing key", key, out)
	}
	require(t, raw["skipped"] != nil, "skipped must be an array", out)
	canonical, err := projectfs.CanonicalProjectRoot(root)
	require(t, err == nil, err)
	require(t, raw["projectRoot"] == canonical, "capture must report the canonical root", raw["projectRoot"], canonical)
	from := raw["tree"].(string)

	write(t, root, "a.txt", "2")
	out, err = core.DispatchContext(ctx, "ai_snapshot_capture", string(args))
	require(t, err == nil, err)
	require(t, json.Unmarshal([]byte(out), &raw) == nil, out)
	to := raw["tree"].(string)

	diffArgs, _ := json.Marshal(map[string]string{"projectRoot": root, "from": from, "to": to})
	out, err = core.DispatchContext(ctx, "ai_snapshot_diff", string(diffArgs))
	require(t, err == nil, err)
	if out != `{"changes":[{"path":"a.txt","status":"modified"}]}` {
		t.Fatalf("diff result = %s", out)
	}
	out, err = core.DispatchContext(ctx, "ai_snapshot_diff", `{"projectRoot":`+jsonString(root)+`,"from":"`+from+`","to":"`+from+`"}`)
	require(t, err == nil && out == `{"changes":[]}`, err, out)

	_, err = core.DispatchContext(ctx, "ai_snapshot_capture", `{"projectRoot":""}`)
	require(t, err != nil, "empty root must fail")
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// blobHash returns the blob ID git would give the file, without writing it.
func blobHash(t *testing.T, root, rel string) string {
	t.Helper()
	return strings.TrimSpace(runTestGit(t, root, "hash-object", "--no-filters", "--", rel))
}

func storeHasObject(t *testing.T, root, id string) bool {
	t.Helper()
	st, err := openStore(root)
	require(t, err == nil, err)
	_, err = st.git(ctx, nil, "cat-file", "-e", id)
	return err == nil
}

func TestCaptureNeverHashesFilesOutsideProjectView(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses HOME-based global git config")
	}
	root := newTestRepo(t, "全局 忽略 global")
	// The project view (ls-files --exclude-standard) honours the user's global
	// excludesFile; the private store must not hash anything it excludes.
	home := t.TempDir()
	ignoreFile := filepath.Join(home, "global ignore")
	require(t, os.WriteFile(ignoreFile, []byte("gignored/\n"), 0o644) == nil)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	require(t, os.WriteFile(filepath.Join(home, ".gitconfig"),
		[]byte("[core]\n\texcludesFile = \""+ignoreFile+"\"\n"), 0o644) == nil)

	writeSized(t, root, "gignored/huge.bin", 11<<20)
	f, _ := os.OpenFile(filepath.Join(root, "gignored", "huge.bin"), os.O_WRONLY, 0)
	_, _ = f.WriteAt([]byte("unique-global"), 0)
	_ = f.Close()
	fresh := filepath.Join(root, "gignored", "fresh")
	require(t, os.MkdirAll(fresh, 0o755) == nil)
	initTestRepo(t, fresh)
	write(t, fresh, "y.txt", "y")

	newline := "big\nname.bin"
	writeSized(t, root, newline, 11<<20)
	f, _ = os.OpenFile(filepath.Join(root, newline), os.O_WRONLY, 0)
	_, _ = f.WriteAt([]byte("unique-newline"), 0)
	_ = f.Close()
	write(t, root, "b[1]*.txt", "literal")

	c, err := CaptureProject(ctx, root)
	require(t, err == nil, err)
	require(t, reflect.DeepEqual(c.Skipped, []string{newline}), c.Skipped)
	files := treeFiles(t, root, c.Tree)
	require(t, reflect.DeepEqual(files, []string{"b[1]*.txt", "tracked.txt"}), files)
	require(t, !storeHasObject(t, root, blobHash(t, root, "gignored/huge.bin")), "globally ignored big file was hashed")
	require(t, !storeHasObject(t, root, blobHash(t, root, newline)), "big file with newline in name was hashed")
}

func TestCaptureDoesNotRehashUnchangedFiles(t *testing.T) {
	root := newTestRepo(t, "缓存 stat cache")
	write(t, root, "a.txt", "stable content")
	// Back-date the file so git does not treat it as racily clean (same
	// timestamp as the index), which would legitimately force a re-hash.
	past := time.Now().Add(-24 * time.Hour)
	require(t, os.Chtimes(filepath.Join(root, "a.txt"), past, past) == nil)
	c1, err := CaptureProject(ctx, root)
	require(t, err == nil, err)
	id := blobHash(t, root, "a.txt")
	st, _ := openStore(root)
	obj := filepath.Join(st.gitDir, "objects", id[:2], id[2:])
	old := time.Now().Add(-48 * time.Hour)
	require(t, os.Chtimes(obj, old, old) == nil)
	write(t, root, "b.txt", "other")
	c2, err := CaptureProject(ctx, root)
	require(t, err == nil, err)
	require(t, c1.Tree != c2.Tree)
	info, err := os.Stat(obj)
	require(t, err == nil, err)
	// git freshens the mtime of an existing loose object whenever it re-hashes it.
	require(t, info.ModTime().Before(time.Now().Add(-time.Hour)), "unchanged file was re-hashed", info.ModTime())
}

// TestCaptureUnchangedLargeTreeIsFast guards the 3 s capture budget: a repeat
// capture of an unchanged ~30k-file tree must rely on the private index's stat
// cache and must not do per-path pathspec matching (which is quadratic).
func TestCaptureUnchangedLargeTreeIsFast(t *testing.T) {
	if testing.Short() {
		t.Skip("creates 30k files")
	}
	root := newTestRepo(t, "大 仓库 large")
	past := time.Now().Add(-24 * time.Hour)
	for i := 0; i < 300; i++ {
		dir := filepath.Join(root, "d"+strings.Repeat("x", i%3), "sub"+string(rune('a'+i%26))+hex.EncodeToString([]byte{byte(i)}))
		require(t, os.MkdirAll(dir, 0o755) == nil)
		for j := 0; j < 100; j++ {
			p := filepath.Join(dir, "f"+hex.EncodeToString([]byte{byte(j)})+".txt")
			require(t, os.WriteFile(p, []byte(p), 0o644) == nil)
			// Back-date so entries are not racily clean, as real project files are.
			require(t, os.Chtimes(p, past, past) == nil)
		}
	}
	_, err := CaptureProject(ctx, root)
	require(t, err == nil, err)
	start := time.Now()
	c2, err := CaptureProject(ctx, root)
	elapsed := time.Since(start)
	require(t, err == nil, err)
	t.Logf("repeat capture of 30k files took %v", elapsed)
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("repeat capture took %v, want < 1.5s", elapsed)
	}
	require(t, len(treeFiles(t, root, c2.Tree)) == 30001)
}

func TestCaptureNestedRepositoryCommitIsSubmoduleChange(t *testing.T) {
	root := newTestRepo(t, "嵌套 提交")
	inner := filepath.Join(root, "inner")
	require(t, os.MkdirAll(inner, 0o755) == nil)
	initTestRepo(t, inner)
	write(t, inner, "x.txt", "1")
	runTestGit(t, inner, "add", "x.txt")
	runTestGit(t, inner, "commit", "-q", "-m", "one")
	c1, err := CaptureProject(ctx, root)
	require(t, err == nil, err)
	write(t, inner, "x.txt", "2")
	runTestGit(t, inner, "commit", "-q", "-am", "two")
	c2, err := CaptureProject(ctx, root)
	require(t, err == nil, err)
	changes, err := DiffTrees(ctx, root, c1.Tree, c2.Tree)
	require(t, err == nil, err)
	assertChanges(t, changes, map[string]string{"inner": "submodule"})
}

// ---------- restore ----------

func capture(t *testing.T, root string) Capture {
	t.Helper()
	c, err := CaptureProject(ctx, root)
	require(t, err == nil && c.Tree != "", err, c)
	return c
}

func changedPaths(t *testing.T, root, from, to string) []string {
	t.Helper()
	changes, err := DiffTrees(ctx, root, from, to)
	require(t, err == nil, err)
	paths := []string{}
	for _, c := range changes {
		paths = append(paths, c.Path)
	}
	return paths
}

func read(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if os.IsNotExist(err) {
		return "<missing>"
	}
	require(t, err == nil, err)
	return string(data)
}

func exists(root, rel string) bool {
	_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil
}

func assertResult(t *testing.T, got RestoreResult, restored, deleted, conflicts []string) {
	t.Helper()
	want := RestoreResult{Restored: restored, Deleted: deleted, Conflicts: conflicts}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("restore result = %+v, want %+v", got, want)
	}
}

func TestRestoreRevertsAgentChangesAndDeletesCreatedFiles(t *testing.T) {
	root := newTestRepo(t, "回退 restore 项目")
	write(t, root, "a.txt", "one")
	write(t, root, "keep/old.txt", "old")
	runTestGit(t, root, "add", "-A")
	runTestGit(t, root, "commit", "-q", "-m", "base")
	before := capture(t, root)
	state := gitState(t, root)

	// The agent edits, creates (in new nested dirs), and deletes files.
	write(t, root, "a.txt", "two")
	write(t, root, "b.txt", "new")
	write(t, root, "新 目录/deep dir/c.txt", "c")
	remove(t, root, "keep/old.txt")
	after := capture(t, root)

	paths := changedPaths(t, root, before.Tree, after.Tree)
	res, err := Restore(ctx, RestoreRequest{ProjectRoot: root, Target: before.Tree, Expected: after.Tree, Paths: paths})
	require(t, err == nil, err)
	assertResult(t, res, []string{"a.txt", "keep/old.txt"}, []string{"b.txt", "新 目录/deep dir/c.txt"}, []string{})
	require(t, read(t, root, "a.txt") == "one", read(t, root, "a.txt"))
	require(t, read(t, root, "keep/old.txt") == "old")
	require(t, !exists(root, "b.txt"))
	// Directories that only existed for the created file are removed too.
	require(t, !exists(root, "新 目录"), "empty created dir left behind")
	require(t, capture(t, root).Tree == before.Tree, "restored tree differs from target")
	if got := gitState(t, root); got != state {
		t.Fatalf("project git state changed by restore: %v -> %v", state, got)
	}
	// No temp files left anywhere.
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if strings.Contains(d.Name(), "mincode-restore") {
			t.Errorf("temp file left: %s", path)
		}
		return nil
	})
	// Restoring again is a no-op that reports the same end state.
	res, err = Restore(ctx, RestoreRequest{ProjectRoot: root, Target: before.Tree, Expected: after.Tree, Paths: paths})
	require(t, err == nil, err)
	assertResult(t, res, []string{"a.txt", "keep/old.txt"}, []string{"b.txt", "新 目录/deep dir/c.txt"}, []string{})
}

func TestRestoreLeavesFilesEditedAfterTheAgentAsConflicts(t *testing.T) {
	root := newTestRepo(t, "冲突 conflict")
	write(t, root, "a.txt", "one")
	write(t, root, "c.txt", "c1")
	before := capture(t, root)
	write(t, root, "a.txt", "two")
	write(t, root, "b.txt", "agent")
	write(t, root, "c.txt", "c2")
	write(t, root, "d.txt", "agent d")
	after := capture(t, root)
	paths := changedPaths(t, root, before.Tree, after.Tree)

	write(t, root, "a.txt", "three")     // user edits after the agent
	write(t, root, "d.txt", "user d")    // user edits a file the agent created
	write(t, root, "c.txt", "c1")        // user already reverted by hand
	write(t, root, "e.txt", "user only") // not in the request: never touched
	res, err := Restore(ctx, RestoreRequest{ProjectRoot: root, Target: before.Tree, Expected: after.Tree, Paths: paths})
	require(t, err == nil, err)
	assertResult(t, res, []string{"c.txt"}, []string{"b.txt"}, []string{"a.txt", "d.txt"})
	require(t, read(t, root, "a.txt") == "three")
	require(t, read(t, root, "d.txt") == "user d")
	require(t, read(t, root, "c.txt") == "c1")
	require(t, read(t, root, "e.txt") == "user only")
	require(t, !exists(root, "b.txt"))
}

func TestRestoreTreatsDeletedOrDirectoryReplacedFilesAsConflicts(t *testing.T) {
	root := newTestRepo(t, "删除 或 目录")
	write(t, root, "a.txt", "one")
	write(t, root, "b.txt", "one")
	before := capture(t, root)
	write(t, root, "a.txt", "two")
	write(t, root, "b.txt", "two")
	write(t, root, "c.txt", "created")
	after := capture(t, root)
	paths := changedPaths(t, root, before.Tree, after.Tree)

	remove(t, root, "a.txt")
	remove(t, root, "b.txt")
	write(t, root, "b.txt/inner.txt", "dir now")
	remove(t, root, "c.txt")
	write(t, root, "c.txt/x", "dir now")
	res, err := Restore(ctx, RestoreRequest{ProjectRoot: root, Target: before.Tree, Expected: after.Tree, Paths: paths})
	require(t, err == nil, err)
	assertResult(t, res, []string{}, []string{}, []string{"a.txt", "b.txt", "c.txt"})
	require(t, !exists(root, "a.txt"))
	require(t, read(t, root, "b.txt/inner.txt") == "dir now")
	require(t, read(t, root, "c.txt/x") == "dir now")
}

func TestRestoreRejectsPathsOutsideTheProject(t *testing.T) {
	root := newTestRepo(t, "越界 outside")
	outside := t.TempDir()
	write(t, outside, "x", "outside")
	write(t, root, "a.txt", "one")
	write(t, root, "real dir/x", "inside")
	before := capture(t, root)
	write(t, root, "a.txt", "two")
	after := capture(t, root)
	require(t, os.Symlink(outside, filepath.Join(root, "out")) == nil)
	require(t, os.Symlink(filepath.Join(root, "real dir"), filepath.Join(root, "in link")) == nil)
	// A relative symlink that stays inside the project is still never followed.
	require(t, os.Symlink("real dir", filepath.Join(root, "rel link")) == nil)

	bad := []string{
		"../x", "a/../../x", "..", filepath.Join(outside, "x"), "/etc/passwd",
		"out/x", "in link/x", "rel link/x", "rel link/new.txt", "out/new/y",
		"", ".", "./a.txt", "a//b", "a.txt/", "a\x00b",
		".git/config", ".GIT/HEAD", "sub/.git/hooks/pre-commit", ".git",
	}
	if runtime.GOOS == "windows" {
		bad = append(bad, `..\x`, `C:\x`, `a\b`)
	}
	for _, p := range bad {
		_, err := Restore(ctx, RestoreRequest{ProjectRoot: root, Target: before.Tree, Expected: after.Tree, Paths: []string{p}})
		if err == nil {
			t.Errorf("Restore accepted path %q", p)
		}
	}
	require(t, read(t, outside, "x") == "outside")
	require(t, read(t, root, "real dir/x") == "inside")
	require(t, read(t, root, "a.txt") == "two")
}

func TestRestoreRejectsBadRequestWithoutChangingFiles(t *testing.T) {
	root := newTestRepo(t, "坏 请求")
	write(t, root, "a.txt", "one")
	before := capture(t, root)
	write(t, root, "a.txt", "two")
	write(t, root, "b.txt", "agent")
	after := capture(t, root)

	// One bad path anywhere in the list: nothing is touched.
	_, err := Restore(ctx, RestoreRequest{ProjectRoot: root, Target: before.Tree, Expected: after.Tree, Paths: []string{"a.txt", "b.txt", "../x"}})
	require(t, err != nil, "bad path accepted")
	require(t, read(t, root, "a.txt") == "two" && read(t, root, "b.txt") == "agent")

	blob := blobHash(t, root, "a.txt")
	missing := strings.Repeat("ab", 20)
	for _, ids := range [][2]string{{"", after.Tree}, {before.Tree, "HEAD"}, {"--output=/tmp/x", after.Tree}, {blob, after.Tree}, {before.Tree, missing}} {
		_, err := Restore(ctx, RestoreRequest{ProjectRoot: root, Target: ids[0], Expected: ids[1], Paths: []string{"a.txt"}})
		if err == nil {
			t.Errorf("Restore accepted target=%q expected=%q", ids[0], ids[1])
		}
	}
	require(t, read(t, root, "a.txt") == "two")

	res, err := Restore(ctx, RestoreRequest{ProjectRoot: root, Target: before.Tree, Expected: after.Tree})
	require(t, err == nil, err)
	assertResult(t, res, []string{}, []string{}, []string{})

	_, err = Restore(ctx, RestoreRequest{ProjectRoot: t.TempDir(), Target: before.Tree, Expected: after.Tree, Paths: []string{"a.txt"}})
	require(t, err != nil, "restore without a store must fail")
}

func TestRestoreNeverDeletesSkippedLargeFile(t *testing.T) {
	root := newTestRepo(t, "大文件 large")
	write(t, root, "grow.bin", "small")
	before := capture(t, root)
	writeSized(t, root, "grow.bin", 11<<20) // crosses the 10 MiB limit: absent from after
	writeSized(t, root, "new big.bin", 11<<20)
	after := capture(t, root)
	require(t, reflect.DeepEqual(after.Skipped, []string{"grow.bin", "new big.bin"}), after.Skipped)
	paths := changedPaths(t, root, before.Tree, after.Tree)
	require(t, reflect.DeepEqual(paths, []string{"grow.bin"}), paths)

	res, err := Restore(ctx, RestoreRequest{ProjectRoot: root, Target: before.Tree, Expected: after.Tree, Paths: []string{"grow.bin", "new big.bin"}})
	require(t, err == nil, err)
	assertResult(t, res, []string{}, []string{}, []string{"grow.bin", "new big.bin"})
	for _, p := range []string{"grow.bin", "new big.bin"} {
		info, err := os.Stat(filepath.Join(root, p))
		require(t, err == nil && info.Size() == 11<<20, p, err)
	}
}

func TestRestoreReportsGitlinksAsConflicts(t *testing.T) {
	root := newTestRepo(t, "子仓库 gitlink")
	inner := filepath.Join(root, "inner")
	require(t, os.MkdirAll(inner, 0o755) == nil)
	initTestRepo(t, inner)
	write(t, inner, "x.txt", "1")
	runTestGit(t, inner, "add", "x.txt")
	runTestGit(t, inner, "commit", "-q", "-m", "one")
	write(t, root, "a.txt", "one")
	before := capture(t, root)
	write(t, inner, "x.txt", "2")
	runTestGit(t, inner, "commit", "-q", "-am", "two")
	write(t, root, "a.txt", "two")
	after := capture(t, root)
	innerHead := runTestGit(t, inner, "rev-parse", "HEAD")

	res, err := Restore(ctx, RestoreRequest{ProjectRoot: root, Target: before.Tree, Expected: after.Tree,
		Paths: []string{"a.txt", "inner", "inner/x.txt"}})
	require(t, err == nil, err)
	assertResult(t, res, []string{"a.txt"}, []string{}, []string{"inner", "inner/x.txt"})
	require(t, runTestGit(t, inner, "rev-parse", "HEAD") == innerHead)
	require(t, read(t, inner, "x.txt") == "2")
}

func TestRestoreSubdirectoryProjectRoot(t *testing.T) {
	top := newTestRepo(t, "顶层 top")
	write(t, top, "a.txt", "top level")
	write(t, top, "pkg 包/a.txt", "one")
	root := filepath.Join(top, "pkg 包")
	before := capture(t, root)
	write(t, root, "a.txt", "two")
	write(t, root, "n.txt", "new")
	after := capture(t, root)
	paths := changedPaths(t, root, before.Tree, after.Tree)
	require(t, reflect.DeepEqual(paths, []string{"a.txt", "n.txt"}), paths)

	res, err := Restore(ctx, RestoreRequest{ProjectRoot: root + string(filepath.Separator) + ".", Target: before.Tree, Expected: after.Tree, Paths: paths})
	require(t, err == nil, err)
	assertResult(t, res, []string{"a.txt"}, []string{"n.txt"}, []string{})
	require(t, read(t, root, "a.txt") == "one")
	require(t, read(t, top, "a.txt") == "top level")
	require(t, !exists(root, "n.txt"))
}

func TestRestoreKeepsPermissionBits(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions")
	}
	root := newTestRepo(t, "权限 exec")
	write(t, root, "script.sh", "echo 1")
	require(t, os.Chmod(filepath.Join(root, "script.sh"), 0o750) == nil)
	write(t, root, "tool.sh", "tool")
	require(t, os.Chmod(filepath.Join(root, "tool.sh"), 0o755) == nil)
	write(t, root, "run.sh", "run")
	require(t, os.Chmod(filepath.Join(root, "run.sh"), 0o755) == nil)
	before := capture(t, root)

	write(t, root, "script.sh", "echo 2") // WriteFile keeps the existing mode
	remove(t, root, "tool.sh")
	require(t, os.Chmod(filepath.Join(root, "run.sh"), 0o644) == nil) // mode-only change
	after := capture(t, root)
	paths := changedPaths(t, root, before.Tree, after.Tree)
	require(t, reflect.DeepEqual(paths, []string{"run.sh", "script.sh", "tool.sh"}), paths)

	res, err := Restore(ctx, RestoreRequest{ProjectRoot: root, Target: before.Tree, Expected: after.Tree, Paths: paths})
	require(t, err == nil, err)
	assertResult(t, res, []string{"run.sh", "script.sh", "tool.sh"}, []string{}, []string{})
	mode := func(rel string) os.FileMode {
		info, err := os.Stat(filepath.Join(root, rel))
		require(t, err == nil, err)
		return info.Mode().Perm()
	}
	require(t, read(t, root, "script.sh") == "echo 1")
	require(t, mode("script.sh") == 0o750, mode("script.sh"))
	require(t, mode("tool.sh")&0o100 != 0 && mode("tool.sh")&0o022 == 0, mode("tool.sh"))
	require(t, mode("run.sh")&0o111 == 0o111, mode("run.sh"))
	require(t, capture(t, root).Tree == before.Tree)
}

func TestRestoreSymlinksWithoutFollowingThem(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	root := newTestRepo(t, "链接 symlink")
	outside := t.TempDir()
	write(t, outside, "secret", "outside")
	write(t, root, "a.txt", "a")
	require(t, os.Symlink("a.txt", filepath.Join(root, "ln changed")) == nil)
	require(t, os.Symlink("a.txt", filepath.Join(root, "ln deleted")) == nil)
	before := capture(t, root)

	require(t, os.Remove(filepath.Join(root, "ln changed")) == nil)
	require(t, os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "ln changed")) == nil)
	require(t, os.Remove(filepath.Join(root, "ln deleted")) == nil)
	require(t, os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "ln created")) == nil)
	after := capture(t, root)
	paths := changedPaths(t, root, before.Tree, after.Tree)

	res, err := Restore(ctx, RestoreRequest{ProjectRoot: root, Target: before.Tree, Expected: after.Tree, Paths: paths})
	require(t, err == nil, err)
	assertResult(t, res, []string{"ln changed", "ln deleted"}, []string{"ln created"}, []string{})
	for _, ln := range []string{"ln changed", "ln deleted"} {
		target, err := os.Readlink(filepath.Join(root, ln))
		require(t, err == nil && target == "a.txt", ln, target, err)
	}
	require(t, !exists(root, "ln created"))
	require(t, read(t, outside, "secret") == "outside")
	require(t, read(t, root, "a.txt") == "a")

	// A symlink that now stands where the agent left a regular file is a conflict.
	write(t, root, "f.txt", "1")
	c1 := capture(t, root)
	write(t, root, "f.txt", "2")
	c2 := capture(t, root)
	remove(t, root, "f.txt")
	require(t, os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "f.txt")) == nil)
	res, err = Restore(ctx, RestoreRequest{ProjectRoot: root, Target: c1.Tree, Expected: c2.Tree, Paths: []string{"f.txt"}})
	require(t, err == nil, err)
	assertResult(t, res, []string{}, []string{}, []string{"f.txt"})
	require(t, read(t, outside, "secret") == "outside")
}

func TestRestoreHandlerUsesCamelCase(t *testing.T) {
	root := newTestRepo(t, "恢复 handler")
	write(t, root, "a.txt", "1")
	before := capture(t, root)
	write(t, root, "a.txt", "2")
	write(t, root, "b.txt", "b")
	after := capture(t, root)
	args, _ := json.Marshal(map[string]any{"projectRoot": root, "target": before.Tree, "expected": after.Tree, "paths": []string{"a.txt", "b.txt"}})
	out, err := core.DispatchContext(ctx, "ai_snapshot_restore", string(args))
	require(t, err == nil, err)
	require(t, out == `{"restored":["a.txt"],"deleted":["b.txt"],"conflicts":[]}`, out)
	require(t, read(t, root, "a.txt") == "1")

	args, _ = json.Marshal(map[string]any{"projectRoot": root, "target": before.Tree, "expected": after.Tree, "paths": []string{"../x"}})
	_, err = core.DispatchContext(ctx, "ai_snapshot_restore", string(args))
	require(t, err != nil, "bad path accepted by handler")
}

func TestRestoreAcrossDirectoryToFileSwap(t *testing.T) {
	root := newTestRepo(t, "目录 变 文件")
	write(t, root, "a/b.txt", "b")
	write(t, root, "a/c.txt", "c")
	before := capture(t, root)
	require(t, os.RemoveAll(filepath.Join(root, "a")) == nil)
	write(t, root, "a", "agent file") // the agent replaced directory a/ with file a
	after := capture(t, root)
	paths := changedPaths(t, root, before.Tree, after.Tree)
	require(t, reflect.DeepEqual(paths, []string{"a", "a/b.txt", "a/c.txt"}), paths)

	for run := 0; run < 2; run++ { // the second run is a no-op with the same report
		res, err := Restore(ctx, RestoreRequest{ProjectRoot: root, Target: before.Tree, Expected: after.Tree, Paths: paths})
		require(t, err == nil, err)
		assertResult(t, res, []string{"a/b.txt", "a/c.txt"}, []string{"a"}, []string{})
		require(t, read(t, root, "a/b.txt") == "b" && read(t, root, "a/c.txt") == "c")
		require(t, capture(t, root).Tree == before.Tree)
	}

	// The user edited the agent's file afterwards: nothing is touched.
	require(t, os.RemoveAll(filepath.Join(root, "a")) == nil)
	write(t, root, "a", "user edit")
	res, err := Restore(ctx, RestoreRequest{ProjectRoot: root, Target: before.Tree, Expected: after.Tree, Paths: paths})
	require(t, err == nil, err)
	assertResult(t, res, []string{}, []string{}, []string{"a", "a/b.txt", "a/c.txt"})
	require(t, read(t, root, "a") == "user edit")
}

func TestRestoreAcrossFileToDirectorySwap(t *testing.T) {
	root := newTestRepo(t, "文件 变 目录")
	write(t, root, "a", "original file")
	before := capture(t, root)
	remove(t, root, "a")
	write(t, root, "a/b.txt", "b") // the agent replaced file a with directory a/
	write(t, root, "a/c.txt", "c")
	after := capture(t, root)
	paths := changedPaths(t, root, before.Tree, after.Tree)
	require(t, reflect.DeepEqual(paths, []string{"a", "a/b.txt", "a/c.txt"}), paths)

	for run := 0; run < 2; run++ {
		res, err := Restore(ctx, RestoreRequest{ProjectRoot: root, Target: before.Tree, Expected: after.Tree, Paths: paths})
		require(t, err == nil, err)
		assertResult(t, res, []string{"a"}, []string{"a/b.txt", "a/c.txt"}, []string{})
		require(t, read(t, root, "a") == "original file")
		require(t, capture(t, root).Tree == before.Tree)
	}
}

func TestRestoreKeepsUserFilesInAgentCreatedDirectory(t *testing.T) {
	root := newTestRepo(t, "用户 文件 in agent dir")
	write(t, root, "a", "original file")
	before := capture(t, root)
	remove(t, root, "a")
	write(t, root, "a/b.txt", "b")
	after := capture(t, root)
	paths := changedPaths(t, root, before.Tree, after.Tree)
	write(t, root, "a/user.txt", "mine") // the user added a file to the agent's directory

	res, err := Restore(ctx, RestoreRequest{ProjectRoot: root, Target: before.Tree, Expected: after.Tree, Paths: paths})
	require(t, err == nil, err)
	assertResult(t, res, []string{}, []string{"a/b.txt"}, []string{"a"})
	require(t, read(t, root, "a/user.txt") == "mine")
	info, err := os.Lstat(filepath.Join(root, "a"))
	require(t, err == nil && info.IsDir(), err)
}

// The project's own fsmonitor hook and untracked cache never run for the
// read-only commands a capture sends to the project repository.
func TestCaptureIgnoresProjectFsmonitorAndUntrackedCache(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell hook")
	}
	root := newTestRepo(t, "fsmonitor")
	marker := filepath.Join(t.TempDir(), "fsmonitor-ran")
	hook := filepath.Join(t.TempDir(), "fsmonitor.sh")
	require(t, os.WriteFile(hook, []byte("#!/bin/sh\ntouch '"+marker+"'\nexit 1\n"), 0o755) == nil)
	runTestGit(t, root, "config", "core.fsmonitor", hook)
	runTestGit(t, root, "config", "core.untrackedCache", "true")
	write(t, root, "a.txt", "one")
	before := gitState(t, root)
	c, err := CaptureProject(ctx, root)
	require(t, err == nil && c.Tree != "", err, c)
	_, statErr := os.Stat(marker)
	require(t, os.IsNotExist(statErr), "the project's fsmonitor hook ran during capture")
	if after := gitState(t, root); after != before {
		t.Fatalf("project git state changed: %v -> %v", before, after)
	}
}
