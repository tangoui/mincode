package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func writeUsageFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
}

func useUsageRoots(t *testing.T) (dataRoot, cacheRoot string) {
	t.Helper()
	base := t.TempDir()
	prevData, prevCache := userDataDir, userCacheDir
	userDataDir = func() string { return filepath.Join(base, "data") }
	userCacheDir = func() (string, error) { return filepath.Join(base, "cache"), nil }
	t.Cleanup(func() { userDataDir, userCacheDir = prevData, prevCache })
	return filepath.Join(base, "data", appDataDirectory), filepath.Join(base, "cache", appDataDirectory)
}

func TestCollectDataUsageIsolatesCache(t *testing.T) {
	dataRoot, cacheRoot := useUsageRoots(t)
	writeUsageFile(t, filepath.Join(filepath.Dir(cacheRoot), "MinCode", "commercial-cache"), 100)
	writeUsageFile(t, filepath.Join(cacheRoot, "oss-cache"), 7)
	for _, tc := range []struct {
		name, dataOverride, want string
		bytes                    int64
	}{
		{"", "", cacheRoot, 7},
		{"OssE2E", "", cacheRoot + "-OssE2E", 0},
		{"OssE2E", filepath.Dir(dataRoot), filepath.Join(filepath.Dir(dataRoot), "cache", appDataDirectory), 0},
		{"MinCode", "", cacheRoot + "-MinCode", 0},
	} {
		t.Setenv("MINCODE_APP_NAME", tc.name)
		t.Setenv("MINCODE_DATA_DIR", tc.dataOverride)
		usage, err := CollectDataUsage(context.Background())
		if err != nil || usage.CacheRoot != tc.want || usage.TotalBytes != tc.bytes {
			t.Errorf("name=%q override=%q: usage=%+v err=%v, want cache %q with %d bytes", tc.name, tc.dataOverride, usage, err, tc.want, tc.bytes)
		}
	}
}

func TestCollectDataUsageOmitsRemovedCategories(t *testing.T) {
	useUsageRoots(t)
	usage, err := CollectDataUsage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"plugins", "pluginData", "configSync", "externalMcp", "browser", "agentWorktrees"} {
		if _, ok := usageByID(t, usage)[id]; ok {
			t.Errorf("unexpected storage category %q", id)
		}
	}
}

func usageByID(t *testing.T, usage DataUsage) map[string]DataUsageItem {
	t.Helper()
	byID := map[string]DataUsageItem{}
	for _, item := range usage.Items {
		byID[item.ID] = item
	}
	return byID
}

func TestCollectDataUsageGroupsEntriesAndSumsToTotal(t *testing.T) {
	dataRoot, cacheRoot := useUsageRoots(t)
	writeUsageFile(t, filepath.Join(dataRoot, databaseFileName), 100)
	writeUsageFile(t, filepath.Join(dataRoot, databaseFileName+"-wal"), 50)
	writeUsageFile(t, filepath.Join(dataRoot, "plugins", "a", "main.js"), 30)
	writeUsageFile(t, filepath.Join(dataRoot, "plugins", "b", "main.js"), 20)
	writeUsageFile(t, filepath.Join(dataRoot, "terminal-activity", "x.json"), 7)
	writeUsageFile(t, filepath.Join(dataRoot, "notes-assets", "a.png"), 11)
	writeUsageFile(t, filepath.Join(dataRoot, ".key_salt"), 3)
	writeUsageFile(t, filepath.Join(dataRoot, "updates", "1.0", "pkg.zip"), 40)
	writeUsageFile(t, filepath.Join(cacheRoot, "misc.tmp"), 5)

	usage, err := CollectDataUsage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if usage.DataRoot != dataRoot || usage.CacheRoot != cacheRoot {
		t.Fatalf("roots = %q, %q", usage.DataRoot, usage.CacheRoot)
	}
	byID := usageByID(t, usage)
	cases := map[string]struct {
		bytes, files int64
		path         string
	}{
		"database":   {150, 2, filepath.Join(dataRoot, databaseFileName)},
		"terminal":   {7, 1, filepath.Join(dataRoot, "terminal-activity")},
		"notes":      {11, 1, filepath.Join(dataRoot, "notes-assets")},
		"other":      {53, 3, dataRoot},
		"updates":    {40, 1, filepath.Join(dataRoot, "updates")},
		"cacheOther": {5, 1, cacheRoot},
	}
	for id, want := range cases {
		got := byID[id]
		if got.Bytes != want.bytes || got.Files != want.files || got.Path != want.path {
			t.Errorf("%s = %+v, want bytes=%d files=%d path=%s", id, got, want.bytes, want.files, want.path)
		}
	}
	if !byID["other"].Exists || !byID["updates"].Exists {
		t.Errorf("exists flags wrong: %+v", byID)
	}
	if byID["cacheOther"].Group != "cache" || byID["database"].Group != "data" {
		t.Errorf("groups wrong: %+v", byID)
	}
	if usage.TotalBytes != 266 {
		t.Errorf("total = %d, want 266", usage.TotalBytes)
	}
}

func TestCollectDataUsageHandlesMissingDirectories(t *testing.T) {
	useUsageRoots(t)
	usage, err := CollectDataUsage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if usage.TotalBytes != 0 || len(usage.Items) == 0 {
		t.Fatalf("usage = %+v", usage)
	}
	for _, item := range usage.Items {
		if item.Exists || item.Bytes != 0 {
			t.Errorf("%s should be empty: %+v", item.ID, item)
		}
	}
}

func TestCollectDataUsageDoesNotFollowSymlinks(t *testing.T) {
	dataRoot, _ := useUsageRoots(t)
	outside := filepath.Join(t.TempDir(), "big.bin")
	writeUsageFile(t, outside, 4096)
	writeUsageFile(t, filepath.Join(dataRoot, "notes-assets", "a.png"), 10)
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(dataRoot, "notes-assets", "link")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	usage, err := CollectDataUsage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := usageByID(t, usage)["notes"].Bytes; got >= 4096 {
		t.Fatalf("notes bytes = %d, symlink target was counted", got)
	}
}

func TestCollectDataUsageStopsWhenCancelled(t *testing.T) {
	dataRoot, _ := useUsageRoots(t)
	writeUsageFile(t, filepath.Join(dataRoot, "plugins", "a.js"), 10)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CollectDataUsage(ctx); err == nil {
		t.Fatal("expected a cancellation error")
	}
}
