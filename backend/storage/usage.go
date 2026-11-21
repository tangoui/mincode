package storage

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"mincode/app/backend/core"
)

// 设置页“存储明细”按类别统计应用数据占用。类别按顶层条目名归类，
// 未识别的条目（密钥、会话凭据等小文件）计入 other，保证各项之和等于总量。
var usageCategories = []struct {
	id    string
	match func(name string) bool
}{
	{"database", func(name string) bool { return strings.HasPrefix(name, databaseFileName) }},
	{"notes", func(name string) bool { return name == "notes-assets" }},
	{"plugins", func(name string) bool { return name == "plugins" }},
	{"pluginData", func(name string) bool { return name == "plugin-storage" || name == "plugin-development.json" }},
	{"terminal", func(name string) bool { return name == "terminal-activity" || name == "ssh-command-history" }},
	{"configSync", func(name string) bool { return name == "config-sync" }},
	{"externalMcp", func(name string) bool { return name == "external-mcp" }},
}

// 缓存目录（os.UserCacheDir()/MinCode）下的已知子目录，其余计入 cacheOther。
var cacheCategories = map[string]string{
	"updates":         "updates",
	"agent-worktrees": "agentWorktrees",
	"EmbeddedBrowser": "browser",
}

var userCacheDir = os.UserCacheDir

// DataUsageItem 是一个类别的占用。Path 为该类别最具代表性的位置：单一条目时指向它，
// 多个条目或兜底类别时指向所在目录。
type DataUsageItem struct {
	ID     string `json:"id"`
	Group  string `json:"group"`
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	Files  int64  `json:"files"`
	Exists bool   `json:"exists"`
}

// DataUsage 汇总应用数据目录与缓存目录的占用。
type DataUsage struct {
	DataRoot   string          `json:"dataRoot"`
	CacheRoot  string          `json:"cacheRoot"`
	TotalBytes int64           `json:"totalBytes"`
	Items      []DataUsageItem `json:"items"`
}

func init() {
	core.RegisterCtx("app_data_usage", func(ctx context.Context, _, _ string) (string, error) {
		usage, err := CollectDataUsage(ctx)
		if err != nil {
			return "", err
		}
		return core.EncodeResult(usage)
	})
}

// CollectDataUsage 统计数据目录与缓存目录。不跟随符号链接，目录不存在时对应类别记 0。
func CollectDataUsage(ctx context.Context) (DataUsage, error) {
	root := userDataDir()
	if root == "" {
		return DataUsage{}, errors.New("应用数据目录不可用")
	}
	usage := DataUsage{DataRoot: filepath.Join(root, appDataDirectory)}

	dataItems, err := collectGroup(ctx, usage.DataRoot, "data", func(name string) string {
		for _, c := range usageCategories {
			if c.match(name) {
				return c.id
			}
		}
		return "other"
	}, append(categoryIDs(), "other"), "other")
	if err != nil {
		return DataUsage{}, err
	}
	usage.Items = append(usage.Items, dataItems...)

	if cacheBase, err := userCacheDir(); err == nil && cacheBase != "" {
		usage.CacheRoot = filepath.Join(cacheBase, "MinCode")
		cacheItems, err := collectGroup(ctx, usage.CacheRoot, "cache", func(name string) string {
			if id, ok := cacheCategories[name]; ok {
				return id
			}
			return "cacheOther"
		}, []string{"updates", "agentWorktrees", "browser", "cacheOther"}, "cacheOther")
		if err != nil {
			return DataUsage{}, err
		}
		usage.Items = append(usage.Items, cacheItems...)
	}

	for _, item := range usage.Items {
		usage.TotalBytes += item.Bytes
	}
	return usage, nil
}

func categoryIDs() []string {
	ids := make([]string, 0, len(usageCategories))
	for _, c := range usageCategories {
		ids = append(ids, c.id)
	}
	return ids
}

// collectGroup 按顶层条目归类统计 dir，返回 order 顺序的类别（含 0 字节的类别）；
// fallback 是未识别条目所归入的兜底类别。
func collectGroup(ctx context.Context, dir, group string, classify func(string) string, order []string, fallback string) ([]DataUsageItem, error) {
	items := make(map[string]*DataUsageItem, len(order))
	entriesByID := make(map[string][]string, len(order))
	for _, id := range order {
		items[id] = &DataUsageItem{ID: id, Group: group, Path: dir}
	}

	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		id := classify(entry.Name())
		item := items[id]
		path := filepath.Join(dir, entry.Name())
		bytes, files, err := treeSize(ctx, path)
		if err != nil {
			return nil, err
		}
		item.Bytes += bytes
		item.Files += files
		item.Exists = true
		entriesByID[id] = append(entriesByID[id], path)
	}

	result := make([]DataUsageItem, 0, len(order))
	for _, id := range order {
		item := items[id]
		switch paths := entriesByID[id]; {
		case id == fallback:
			// 兜底类别散落多处，定位到所在目录。
		case id == "database" && item.Exists:
			// 数据库由主文件与 -wal/-shm 等组成，定位到主文件。
			item.Path = filepath.Join(dir, databaseFileName)
		case len(paths) == 1:
			item.Path = paths[0]
		}
		result = append(result, *item)
	}
	return result, nil
}

// treeSize 返回 path（文件或目录）下普通文件的总字节数与文件数；符号链接只计自身。
func treeSize(ctx context.Context, path string) (int64, int64, error) {
	var bytes, files int64
	err := filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			// 统计过程中被删除或无权读取的条目跳过，不让单个条目拖垮整页。
			if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
				return nil
			}
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		bytes += info.Size()
		files++
		return nil
	})
	return bytes, files, err
}
