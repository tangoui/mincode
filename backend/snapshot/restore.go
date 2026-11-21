package snapshot

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	projectfs "mincode/app/backend/fs"
)

// RestoreRequest 描述一次文件恢复：把 Paths 恢复成 Target 树里的样子。Expected 是
// 这些文件当前应有内容所在的树（最后一个被回退回合的 after），用来判断用户在助手
// 之后是否又改过它们。路径相对于项目根，即快照树内的路径。
type RestoreRequest struct {
	ProjectRoot string   `json:"projectRoot"`
	Target      string   `json:"target"`
	Expected    string   `json:"expected"`
	Paths       []string `json:"paths"`
}

// RestoreResult 列出恢复的结果（均按路径排序、不为 nil）。Restored：已写回或本来就与
// Target 一致；Deleted：已删除或本来就不存在（Target 中没有）；Conflicts：磁盘内容与
// Expected 不同（被修改、被删除、变成目录等），以及嵌套子仓库（gitlink）和超出
// 快照范围的路径——这些都没有被改动。
type RestoreResult struct {
	Restored  []string `json:"restored"`
	Deleted   []string `json:"deleted"`
	Conflicts []string `json:"conflicts"`
}

const (
	modeFile    = "100644"
	modeExec    = "100755"
	modeSymlink = "120000"
	modeGitlink = "160000"
	modeTree    = "040000"
	// blobFetchBudget 限制一次 cat-file --batch 读入内存的内容总量。
	blobFetchBudget = 64 << 20
)

// treeEntry 是 ls-tree 列出的一条记录。
type treeEntry struct {
	mode string
	id   string
	size int64 // blob 才有，其余为 -1
}

func (e *treeEntry) isBlob() bool {
	return e != nil && (e.mode == modeFile || e.mode == modeExec || e.mode == modeSymlink)
}

// listTree 列出树中的全部条目（含子树本身），键为相对树根的路径。
func (s *store) listTree(ctx context.Context, tree string) (map[string]*treeEntry, error) {
	out, err := s.git(ctx, nil, "ls-tree", "-r", "-t", "-l", "-z", "--full-tree", tree)
	if err != nil {
		return nil, err
	}
	entries := map[string]*treeEntry{}
	for _, rec := range splitNUL(out) {
		meta, name, ok := strings.Cut(rec, "\t")
		parts := strings.Fields(meta)
		if !ok || len(parts) != 4 || name == "" {
			return nil, fmt.Errorf("snapshot_unexpected_git_output")
		}
		e := &treeEntry{mode: parts[0], id: parts[2], size: -1}
		if parts[3] != "-" {
			n, err := strconv.ParseInt(parts[3], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("snapshot_unexpected_git_output")
			}
			e.size = n
		}
		if e.mode == "40000" {
			e.mode = modeTree
		}
		entries[name] = e
	}
	return entries, nil
}

// objectTypes 返回每个 ID 在私有库里的对象类型；不存在的对象为 "missing"。
func (s *store) objectTypes(ctx context.Context, ids []string) (map[string]string, error) {
	types := map[string]string{}
	if len(ids) == 0 {
		return types, nil
	}
	var in bytes.Buffer
	for _, id := range ids {
		in.WriteString(id + "\n")
	}
	out, err := s.git(ctx, in.Bytes(), "cat-file", "--batch-check=%(objectname) %(objecttype)")
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(lines) != len(ids) {
		return nil, fmt.Errorf("snapshot_unexpected_git_output")
	}
	for i, line := range lines {
		parts := strings.Fields(line)
		if len(parts) != 2 || parts[0] != ids[i] {
			return nil, fmt.Errorf("snapshot_unexpected_git_output")
		}
		types[ids[i]] = parts[1]
	}
	return types, nil
}

// readBlobs 用一次 cat-file --batch 读出若干 blob 的内容。
func (s *store) readBlobs(ctx context.Context, ids []string) (map[string][]byte, error) {
	blobs := map[string][]byte{}
	if len(ids) == 0 {
		return blobs, nil
	}
	var in bytes.Buffer
	for _, id := range ids {
		in.WriteString(id + "\n")
	}
	out, err := s.git(ctx, in.Bytes(), "cat-file", "--batch")
	if err != nil {
		return nil, err
	}
	r := bufio.NewReader(bytes.NewReader(out))
	for _, id := range ids {
		header, err := r.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("snapshot_unexpected_git_output")
		}
		parts := strings.Fields(header)
		if len(parts) != 3 || parts[0] != id || parts[1] != "blob" {
			return nil, fmt.Errorf("snapshot_object_missing: %s", id)
		}
		n, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("snapshot_unexpected_git_output")
		}
		data := make([]byte, n)
		if _, err := io.ReadFull(r, data); err != nil {
			return nil, fmt.Errorf("snapshot_unexpected_git_output")
		}
		if b, err := r.ReadByte(); err != nil || b != '\n' {
			return nil, fmt.Errorf("snapshot_unexpected_git_output")
		}
		blobs[id] = data
	}
	return blobs, nil
}

// cleanRestorePath 校验一条请求路径：必须是快照树里那种规范的相对路径（"/" 分隔，
// 无空段、"."、".."，不是绝对路径或带盘符），且任何一段都不能是 .git（不区分大小写，
// 并忽略 Windows 会去掉的结尾点/空格和 HFS+ 会忽略的不可见字符）。
func cleanRestorePath(p string) (string, error) {
	invalid := fmt.Errorf("snapshot_invalid_path: %q", p)
	if p == "" || strings.ContainsRune(p, 0) || strings.HasPrefix(p, "/") ||
		filepath.IsAbs(p) || filepath.VolumeName(p) != "" {
		return "", invalid
	}
	if runtime.GOOS == "windows" && strings.ContainsAny(p, `\:`) {
		return "", invalid
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." || isDotGit(seg) {
			return "", invalid
		}
	}
	return p, nil
}

func isDotGit(seg string) bool {
	var b strings.Builder
	for _, r := range seg {
		// git 的 is_hfs_dotgit 忽略的零宽/方向控制字符。
		if r >= 0x200c && r <= 0x200f || r >= 0x202a && r <= 0x202e || r >= 0x206a && r <= 0x206f || r == 0xfeff {
			continue
		}
		b.WriteRune(r)
	}
	return strings.EqualFold(strings.TrimRight(b.String(), ". "), ".git")
}

// diskKind 是路径在磁盘上的状态（不跟随符号链接）。
type diskKind int

const (
	diskAbsent diskKind = iota
	diskFile
	diskSymlink
	diskDir
	diskOther
)

type diskState struct {
	kind diskKind
	info fs.FileInfo
	// blockedBy 非空时，路径的某个上级是普通文件（或其他非目录），路径因此不存在。
	blockedBy string
}

// inspectPath 逐级 Lstat 路径的每个上级目录：任何一级是符号链接都返回错误（绝不跟随），
// 某一级不存在则路径不存在，某一级不是目录则记入 blockedBy。最后 Lstat 路径本身。
func inspectPath(r *os.Root, rel string) (diskState, error) {
	segs := strings.Split(rel, "/")
	for i := 1; i < len(segs); i++ {
		prefix := strings.Join(segs[:i], "/")
		info, err := r.Lstat(filepath.FromSlash(prefix))
		if errors.Is(err, fs.ErrNotExist) {
			return diskState{kind: diskAbsent}, nil
		}
		if err != nil {
			return diskState{}, err
		}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			return diskState{}, fmt.Errorf("snapshot_path_symlink: %q", prefix)
		case !info.IsDir():
			return diskState{kind: diskAbsent, blockedBy: prefix}, nil
		}
	}
	info, err := r.Lstat(filepath.FromSlash(rel))
	if errors.Is(err, fs.ErrNotExist) {
		return diskState{kind: diskAbsent}, nil
	}
	if err != nil {
		return diskState{}, err
	}
	st := diskState{info: info}
	switch mode := info.Mode(); {
	case mode.IsRegular():
		st.kind = diskFile
	case mode&fs.ModeSymlink != 0:
		st.kind = diskSymlink
	case mode.IsDir():
		st.kind = diskDir
	default:
		st.kind = diskOther
	}
	return st, nil
}

// blobID 计算内容的 Git blob ID（不经过任何过滤器）；算法按 ID 长度选择 SHA-1/SHA-256。
func blobID(like string, size int64, content io.Reader) (string, error) {
	var h hash.Hash
	if len(like) == 64 {
		h = sha256.New()
	} else {
		h = sha1.New()
	}
	fmt.Fprintf(h, "blob %d\x00", size)
	n, err := io.Copy(h, content)
	if err != nil {
		return "", err
	}
	if n != size {
		return "", errors.New("snapshot_file_changed")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func isExecutable(mode fs.FileMode) bool { return mode&0o100 != 0 }

// matches 判断磁盘状态是否与树条目一致。e 为 nil 表示树里没有该路径，此时磁盘上也
// 必须不存在；e 为目录（树）时磁盘上是目录或不存在即一致。普通文件比较原始字节（与 hash-object --no-filters 相同）和可执行位
// （Windows 不比较）；符号链接比较链接目标；子模块永不一致。
func matches(r *os.Root, rel string, d diskState, e *treeEntry) (bool, error) {
	if e == nil {
		return d.kind == diskAbsent, nil
	}
	switch e.mode {
	case modeFile, modeExec:
		if d.kind != diskFile || d.info.Size() != e.size {
			return false, nil
		}
		if runtime.GOOS != "windows" && isExecutable(d.info.Mode()) != (e.mode == modeExec) {
			return false, nil
		}
		f, err := r.Open(filepath.FromSlash(rel))
		if err != nil {
			return false, nil
		}
		defer f.Close()
		id, err := blobID(e.id, e.size, f)
		if err != nil {
			return false, nil
		}
		return id == e.id, nil
	case modeSymlink:
		if d.kind != diskSymlink {
			return false, nil
		}
		target, err := r.Readlink(filepath.FromSlash(rel))
		if err != nil {
			return false, nil
		}
		id, err := blobID(e.id, int64(len(target)), strings.NewReader(target))
		return err == nil && id == e.id, nil
	case modeTree:
		// 树条目表示该路径上“没有文件”（其下的文件各自作为请求路径处理）：
		// 磁盘上是目录或不存在都算一致。
		return d.kind == diskDir || d.kind == diskAbsent, nil
	default:
		return false, nil
	}
}

type restoreAction struct {
	path   string
	target *treeEntry // nil：删除
	disk   diskState
}

// Restore 把请求中的路径恢复成 Target 树中的内容。
//
// 流程：先校验全部参数（树 ID、每条路径的格式与上级目录不含符号链接），任何一条不合法
// 就返回错误、不动任何文件；再逐条判断冲突；最后先删除、再写入。
//
// 判断规则：磁盘状态已与 Target 一致的路径不需要改动（计入 Restored/Deleted）；否则磁盘
// 状态必须与 Expected 一致（Expected 没有该路径时磁盘上也必须没有），不一致即冲突。
// 树里的目录条目视为该路径上“没有文件”，因此文件与目录互换（a ↔ a/b）可以恢复：
// 先删除，空目录随之移除，再写入。子模块（gitlink）和位于子模块之下的路径一律记为
// 冲突，绝不写入或删除；需要写入但上级被（不在本次删除之列的）普通文件占据、或要写入
// 处的目录删不空（用户在里面加了文件）时也记为冲突。被跳过的大文件不在任何树里，而磁盘上存在，因此总是冲突，不会被删。
//
// 写入使用同目录临时文件加重命名；覆盖已有文件时保留其权限位，仅在 Target 与 Expected
// 的可执行位不同时（助手改过可执行位）按 Target 调整；新文件按 Target 的模式创建
// （100755 为 0755，否则 0644，受 umask 影响）。缺失的上级目录会被创建；删除文件后，
// 不再含任何内容、且在 Target 中不是目录的上级目录会被一并删除。
func Restore(ctx context.Context, req RestoreRequest) (RestoreResult, error) {
	res := RestoreResult{Restored: []string{}, Deleted: []string{}, Conflicts: []string{}}
	if err := ctx.Err(); err != nil {
		return res, err
	}
	if !isObjectID(req.Target) || !isObjectID(req.Expected) {
		return res, errors.New("snapshot_invalid_tree")
	}
	root, err := projectfs.CanonicalProjectRoot(req.ProjectRoot)
	if err != nil {
		return res, err
	}
	paths := make([]string, 0, len(req.Paths))
	seen := map[string]bool{}
	for _, p := range req.Paths {
		clean, err := cleanRestorePath(p)
		if err != nil {
			return res, err
		}
		if !seen[clean] {
			seen[clean] = true
			paths = append(paths, clean)
		}
	}
	sort.Strings(paths)

	st, err := openStore(root)
	if err != nil {
		return res, err
	}
	if !st.exists() {
		return res, errors.New("snapshot_store_missing")
	}
	unlock := lockStore(st.dir)
	defer unlock()

	types, err := st.objectTypes(ctx, []string{req.Target, req.Expected})
	if err != nil {
		return res, err
	}
	if types[req.Target] != "tree" || types[req.Expected] != "tree" {
		return res, errors.New("snapshot_invalid_tree")
	}
	if len(paths) == 0 {
		return res, nil
	}
	target, err := st.listTree(ctx, req.Target)
	if err != nil {
		return res, err
	}
	expected, err := st.listTree(ctx, req.Expected)
	if err != nil {
		return res, err
	}

	r, err := os.OpenRoot(root)
	if err != nil {
		return res, err
	}
	defer r.Close()

	// 第一遍：只读。任何路径的上级含符号链接都在动手前报错。
	states := make([]diskState, len(paths))
	for i, p := range paths {
		if states[i], err = inspectPath(r, p); err != nil {
			return res, err
		}
	}

	// 第二遍：判断冲突（仍然只读）。
	var deletes, writes []restoreAction
	toDelete := map[string]bool{}
	for i, p := range paths {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		d := states[i]
		tgt, exp := target[p], expected[p]
		if isGitlink(tgt) || isGitlink(exp) || underGitlink(p, target) || underGitlink(p, expected) {
			res.Conflicts = append(res.Conflicts, p)
			continue
		}
		// 上级被普通文件占据时该路径不存在（d.kind 为 absent）；只有需要写入时才受阻。
		done, err := matches(r, p, d, tgt)
		if err != nil {
			return res, err
		}
		if done {
			if tgt.isBlob() {
				res.Restored = append(res.Restored, p)
			} else {
				res.Deleted = append(res.Deleted, p)
			}
			continue
		}
		ok, err := matches(r, p, d, exp)
		if err != nil {
			return res, err
		}
		switch {
		case !ok:
			res.Conflicts = append(res.Conflicts, p)
		case !tgt.isBlob():
			// Target 在此没有文件（没有条目或是目录）：删除磁盘上的文件/符号链接；
			// 若是目录（Expected 在此也是目录），只在其内容删完后变空时才删除。
			deletes = append(deletes, restoreAction{path: p, disk: d})
			toDelete[p] = true
		case d.blockedBy != "" && !toDelete[d.blockedBy]:
			res.Conflicts = append(res.Conflicts, p)
		default:
			// 磁盘上是目录（Expected 在此是目录、Target 是文件）时，要等目录内的文件
			// 删除、目录变空被移除后才能写入，写入前会再检查。
			writes = append(writes, restoreAction{path: p, target: tgt, disk: d})
		}
	}

	// 第三遍：先删除（深的路径在前），再写入。
	targetDirs := map[string]bool{}
	for p, e := range target {
		if e.mode == modeTree {
			targetDirs[p] = true
		}
	}
	for i := len(deletes) - 1; i >= 0; i-- {
		p := deletes[i].path
		now, err := inspectPath(r, p)
		if err != nil {
			return finish(res), err
		}
		if now.kind == diskDir {
			// 只删空目录：用户在其中留下的文件不会被删除。
			if err := r.Remove(filepath.FromSlash(p)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				res.Conflicts = append(res.Conflicts, p)
				continue
			}
		} else if now.kind != diskAbsent {
			if err := r.Remove(filepath.FromSlash(p)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return finish(res), err
			}
		}
		res.Deleted = append(res.Deleted, p)
		removeEmptyParents(r, p, targetDirs)
	}
	for start := 0; start < len(writes); {
		// 按内存预算分批读取 blob。
		end, budget := start, int64(0)
		var ids []string
		for end < len(writes) && (end == start || budget+writes[end].target.size <= blobFetchBudget) {
			budget += writes[end].target.size
			ids = append(ids, writes[end].target.id)
			end++
		}
		blobs, err := st.readBlobs(ctx, ids)
		if err != nil {
			return finish(res), err
		}
		for _, w := range writes[start:end] {
			if err := ctx.Err(); err != nil {
				return finish(res), err
			}
			if w.disk.kind == diskDir {
				// 文件被助手换成了目录：目录内容已在上面删除，空目录随之移除。
				// 目录仍在（用户在里面加了文件）就记为冲突，不动它。
				now, err := inspectPath(r, w.path)
				if err != nil {
					return finish(res), err
				}
				if now.kind != diskAbsent {
					res.Conflicts = append(res.Conflicts, w.path)
					continue
				}
				w.disk = now
			}
			if err := writeEntry(r, w, expected[w.path], blobs[w.target.id]); err != nil {
				return finish(res), err
			}
			res.Restored = append(res.Restored, w.path)
		}
		start = end
	}
	return finish(res), nil
}

func finish(res RestoreResult) RestoreResult {
	sort.Strings(res.Restored)
	sort.Strings(res.Deleted)
	sort.Strings(res.Conflicts)
	return res
}

func isGitlink(e *treeEntry) bool { return e != nil && e.mode == modeGitlink }

// underGitlink 判断路径是否位于树中某个子模块（gitlink）之下。
func underGitlink(p string, entries map[string]*treeEntry) bool {
	for dir := path.Dir(p); dir != "."; dir = path.Dir(dir) {
		if e := entries[dir]; e != nil && e.mode == modeGitlink {
			return true
		}
	}
	return false
}

// removeEmptyParents 删除 p 的上级中已空、且在 Target 中不是目录的目录（只删空目录，
// 不跟随符号链接）。
func removeEmptyParents(r *os.Root, p string, targetDirs map[string]bool) {
	for dir := path.Dir(p); dir != "." && !targetDirs[dir]; dir = path.Dir(dir) {
		info, err := r.Lstat(filepath.FromSlash(dir))
		if err != nil || !info.IsDir() {
			return
		}
		if r.Remove(filepath.FromSlash(dir)) != nil {
			return // 非空
		}
	}
}

// writeEntry 原子地把 Target 条目写到 w.path：在同一目录写临时文件（或建临时符号链接），
// 再重命名覆盖。
func writeEntry(r *os.Root, w restoreAction, exp *treeEntry, content []byte) error {
	if int64(len(content)) != w.target.size {
		return fmt.Errorf("snapshot_object_missing: %s", w.target.id)
	}
	// 动手前再确认一次上级目录不含符号链接。
	if _, err := inspectPath(r, w.path); err != nil {
		return err
	}
	dir := path.Dir(w.path)
	if dir != "." {
		if err := r.MkdirAll(filepath.FromSlash(dir), 0o755); err != nil {
			return err
		}
		if _, err := inspectPath(r, w.path); err != nil {
			return err
		}
	}
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return err
	}
	tmp := filepath.FromSlash(path.Join(dir, ".mincode-restore-"+hex.EncodeToString(suffix[:])))
	final := filepath.FromSlash(w.path)

	if w.target.mode == modeSymlink {
		if err := r.Symlink(string(content), tmp); err != nil {
			return err
		}
	} else {
		createPerm := fs.FileMode(0o644)
		if w.target.mode == modeExec {
			createPerm = 0o755
		}
		var keepPerm fs.FileMode
		keep := w.disk.kind == diskFile
		if keep {
			keepPerm = w.disk.info.Mode().Perm()
			if exp != nil && exp.mode != w.target.mode {
				if w.target.mode == modeExec {
					keepPerm |= (keepPerm & 0o444) >> 2
				} else {
					keepPerm &^= 0o111
				}
			}
		}
		f, err := r.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, createPerm)
		if err != nil {
			return err
		}
		_, err = f.Write(content)
		if err == nil && keep {
			err = f.Chmod(keepPerm)
		}
		if err == nil {
			err = f.Sync()
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = r.Remove(tmp)
			return err
		}
	}
	if err := r.Rename(tmp, final); err != nil {
		_ = r.Remove(tmp)
		return err
	}
	return nil
}
