package snapshot

import (
	"encoding/json"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"mincode/app/backend/core"
)

func writeRandom(t *testing.T, root, rel string, size int, seed uint64) {
	t.Helper()
	r := rand.New(rand.NewChaCha8([32]byte{byte(seed), byte(seed >> 8), byte(seed >> 16)}))
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(r.Uint32())
	}
	path := filepath.Join(root, filepath.FromSlash(rel))
	require(t, os.MkdirAll(filepath.Dir(path), 0o755) == nil)
	require(t, os.WriteFile(path, data, 0o644) == nil)
}

func privateRefs(t *testing.T, root string) string {
	t.Helper()
	st, err := openStore(root)
	require(t, err == nil, err)
	out, err := st.git(ctx, nil, "for-each-ref")
	require(t, err == nil, err)
	return string(out)
}

func TestPruneKeepsOnlyReferencedTrees(t *testing.T) {
	root := newTestRepo(t, "清理 prune")
	write(t, root, "a.txt", "v1")
	t1 := capture(t, root)
	write(t, root, "a.txt", "v2 unique")
	t2 := capture(t, root)
	v2 := blobHash(t, root, "a.txt")
	write(t, root, "a.txt", "v3")
	t3 := capture(t, root)
	write(t, root, "a.txt", "v4 current")
	t4 := capture(t, root) // the private index now describes v4

	// Invalid keep IDs are rejected before anything is pruned.
	for _, bad := range [][]string{{"HEAD"}, {t1.Tree, "--all"}, {v2}} {
		require(t, Prune(ctx, root, bad) != nil, "Prune accepted", bad)
	}
	require(t, storeHasObject(t, root, t2.Tree) && storeHasObject(t, root, v2))

	require(t, Prune(ctx, root, []string{t1.Tree, t3.Tree, t3.Tree}) == nil)
	_, err := DiffTrees(ctx, root, t1.Tree, t3.Tree)
	require(t, err == nil, err)
	require(t, len(treeFiles(t, root, t1.Tree)) == 2)
	require(t, !storeHasObject(t, root, t2.Tree), "unreferenced tree survived")
	require(t, !storeHasObject(t, root, v2), "unreferenced blob survived")
	_, err = DiffTrees(ctx, root, t1.Tree, t2.Tree)
	require(t, err != nil, "DiffTrees on a pruned tree must fail")
	require(t, privateRefs(t, root) == "", "temporary keep refs left behind", privateRefs(t, root))

	// A tree that no longer exists is ignored, not an error.
	require(t, Prune(ctx, root, []string{t1.Tree, t2.Tree}) == nil)
	require(t, storeHasObject(t, root, t1.Tree))

	// Objects the private index still needs survive, so the next capture of
	// the unchanged worktree works without re-adding anything.
	require(t, capture(t, root).Tree == t4.Tree)
	write(t, root, "b.txt", "b")
	capture(t, root)

	// Pruning only ever touches the private store, which stays private.
	if runtime.GOOS != "windows" {
		_ = filepath.WalkDir(storeDirFor(root), func(path string, d fs.DirEntry, err error) error {
			require(t, err == nil, err)
			info, err := os.Lstat(path)
			require(t, err == nil, err)
			if info.Mode().Perm()&0o077 != 0 {
				t.Errorf("%s has mode %v after prune", path, info.Mode().Perm())
			}
			return nil
		})
	}
	// A project with no store: nothing to do.
	require(t, Prune(ctx, t.TempDir(), nil) == nil)
}

func TestStoreSizeShrinksAfterPrune(t *testing.T) {
	root := newTestRepo(t, "容量 size")
	empty, err := StoreSize(t.TempDir())
	require(t, err == nil && empty == 0, empty, err)
	for i := 0; i < 16; i++ {
		writeRandom(t, root, "blob/"+strings.Repeat("f", i+1)+".bin", 256<<10, uint64(i+1))
	}
	capture(t, root)
	require(t, os.RemoveAll(filepath.Join(root, "blob")) == nil)
	capture(t, root)
	before, err := StoreSize(root)
	require(t, err == nil, err)
	require(t, before > 4<<20, before)

	require(t, Prune(ctx, root, nil) == nil)
	after, err := StoreSize(root)
	require(t, err == nil, err)
	t.Logf("store size %d -> %d", before, after)
	require(t, after < before-3<<20, before, after)
	require(t, MaxStoreBytes == 1<<30)
}

func TestRetentionHandlers(t *testing.T) {
	root := newTestRepo(t, "清理 handlers")
	writeRandom(t, root, "r.bin", 64<<10, 7)
	c := capture(t, root)
	args, _ := json.Marshal(map[string]any{"projectRoot": root})
	out, err := core.DispatchContext(ctx, "ai_snapshot_size", string(args))
	require(t, err == nil, err)
	var size struct {
		Bytes int64 `json:"bytes"`
	}
	require(t, json.Unmarshal([]byte(out), &size) == nil && size.Bytes > 0, out)

	args, _ = json.Marshal(map[string]any{"projectRoot": root, "keep": []string{c.Tree}})
	out, err = core.DispatchContext(ctx, "ai_snapshot_prune", string(args))
	require(t, err == nil, err)
	require(t, strings.HasPrefix(out, `{"bytes":`), out)
	require(t, storeHasObject(t, root, c.Tree))

	args, _ = json.Marshal(map[string]any{"projectRoot": root, "keep": []string{"nope"}})
	_, err = core.DispatchContext(ctx, "ai_snapshot_prune", string(args))
	require(t, err != nil, "invalid keep accepted")
}

// The store runs with gc.auto=0, so Prune is the only place objects get packed.
func TestPrunePacksLooseObjects(t *testing.T) {
	root := newTestRepo(t, "pack")
	var keep []string
	for i := 0; i < 5; i++ {
		write(t, root, "a.txt", strings.Repeat("v", i+1))
		keep = append(keep, capture(t, root).Tree)
	}
	st, err := openStore(root)
	require(t, err == nil, err)
	countLoose := func() string {
		out, err := st.git(ctx, nil, "count-objects", "-v")
		require(t, err == nil, err)
		for _, line := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(line, "count: ") {
				return strings.TrimPrefix(line, "count: ")
			}
		}
		t.Fatalf("count-objects output without count: %s", out)
		return ""
	}
	require(t, countLoose() != "0", "expected loose objects before pruning")
	require(t, Prune(ctx, root, keep) == nil)
	require(t, countLoose() == "0", "Prune left loose objects", countLoose())
	for _, tree := range keep {
		require(t, len(treeFiles(t, root, tree)) > 0, "kept tree lost", tree)
	}
}
