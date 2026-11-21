package snapshot

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"

	projectfs "mincode/app/backend/fs"
)

// MaxStoreBytes 是每个项目快照存储的占用上限（1 GiB）。超出时由前端按回合时间从旧到新
// 把树从 keep 中去掉并再次 Prune，直到 StoreSize 低于上限。
const MaxStoreBytes = 1 << 30

// keepRefPrefix 是 Prune 期间在私有库里临时创建的引用的命名空间。
const keepRefPrefix = "refs/mincode-keep/"

// Prune 只保留 keep 中各棵树可达的对象，其余对象在私有库里 gc --prune=now 清除。
// 私有索引引用的对象（最近一次拍快照时的工作区内容）也会保留，否则下次拍快照会
// 引用缺失的对象。keep 中格式不合法或不是树的 ID 返回错误且不做任何清理；私有库里
// 已不存在的树直接忽略。项目还没有快照存储时什么也不做。只操作私有库。
func Prune(ctx context.Context, projectRoot string, keep []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ids := make([]string, 0, len(keep))
	seen := map[string]bool{}
	for _, id := range keep {
		if !isObjectID(id) {
			return errors.New("snapshot_invalid_tree")
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	root, err := projectfs.CanonicalProjectRoot(projectRoot)
	if err != nil {
		return err
	}
	st, err := openStore(root)
	if err != nil {
		return err
	}
	unlock := lockStore(st.dir)
	defer unlock()
	if !st.exists() {
		return nil
	}

	types, err := st.objectTypes(ctx, ids)
	if err != nil {
		return err
	}
	var roots []string
	for _, id := range ids {
		switch types[id] {
		case "tree":
			roots = append(roots, id)
		case "missing":
		default:
			return errors.New("snapshot_invalid_tree")
		}
	}

	// 树不被任何引用指向，gc 会把它们当作垃圾；清理期间用临时引用把 keep 标为可达。
	if err := st.dropKeepRefs(ctx); err != nil {
		return err
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = st.dropKeepRefs(cctx)
	}()
	if len(roots) > 0 {
		var cmds strings.Builder
		for i, id := range roots {
			fmt.Fprintf(&cmds, "create %s%d %s\n", keepRefPrefix, i, id)
		}
		if _, err := st.git(ctx, []byte(cmds.String()), "-c", "core.logAllRefUpdates=false", "update-ref", "--stdin"); err != nil {
			return err
		}
	}
	_, err = st.git(ctx, nil,
		"-c", "core.logAllRefUpdates=false",
		"-c", "gc.reflogExpire=now",
		"-c", "gc.reflogExpireUnreachable=now",
		"-c", "gc.rerereResolved=0",
		"-c", "gc.rerereUnresolved=0",
		"-c", "gc.writeCommitGraph=false",
		"gc", "--quiet", "--prune=now")
	return err
}

// dropKeepRefs 删除 keepRefPrefix 下的全部引用（包括上次中断留下的）。
func (s *store) dropKeepRefs(ctx context.Context) error {
	out, err := s.git(ctx, nil, "for-each-ref", "--format=%(refname)", keepRefPrefix)
	if err != nil {
		return err
	}
	var cmds strings.Builder
	for _, ref := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.HasPrefix(ref, keepRefPrefix) {
			cmds.WriteString("delete " + ref + "\n")
		}
	}
	if cmds.Len() == 0 {
		return nil
	}
	_, err = s.git(ctx, []byte(cmds.String()), "-c", "core.logAllRefUpdates=false", "update-ref", "--stdin")
	return err
}

// StoreSize 返回项目快照存储占用的字节数：存储目录下所有普通文件大小之和（不跟随
// 符号链接）。还没有存储时为 0。
func StoreSize(projectRoot string) (int64, error) {
	root, err := projectfs.CanonicalProjectRoot(projectRoot)
	if err != nil {
		return 0, err
	}
	st, err := openStore(root)
	if err != nil {
		return 0, err
	}
	var total int64
	err = filepath.WalkDir(st.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil // 存储不存在，或遍历时文件被 git 删除
			}
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}
