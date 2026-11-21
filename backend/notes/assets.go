package notes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"mincode/app/backend/core"
)

// 图片附件：内容寻址存放在数据目录的 notes-assets/ 下，文件名为内容 SHA-256（小写十六进制）+ 扩展名，
// 正文以 /__notes-assets/<文件名> 引用，由 AssetMiddleware 同源提供。note_assets 表记录入库时间，
// 供延迟回收使用；文件本身是事实来源，表行缺失或多余都由 collectGarbage 修正。

const (
	// maxAssetBytes 是单张图片上限（解码后字节）。
	maxAssetBytes = 20 << 20
	// maxAssetRequestBytes 限制 notes_asset_put 请求体：上限图片的 base64 长度，外加 JSON 与文件名余量。
	maxAssetRequestBytes = (maxAssetBytes+2)/3*4 + 64<<10
	// maxAssetFileNameBytes 限制仅作说明用途的原始文件名。
	maxAssetFileNameBytes = 1024

	assetDirName = "notes-assets"
	// assetRefPrefix 是正文中图片引用与 AssetServer 路由的路径前缀。
	assetRefPrefix = "/__notes-assets/"
	// assetTempPrefix 是写入中临时文件的前缀；中断遗留的由 collectGarbage 清理。
	assetTempPrefix = ".tmp-"
	// assetGracePeriod 内的附件即使未被引用也不回收，避免误删撤销中或尚未保存的图片。
	assetGracePeriod = 24 * time.Hour
)

var (
	// assetNamePattern 是附件文件名的唯一合法形式，也用于中间件路由校验。
	assetNamePattern = regexp.MustCompile(`^[0-9a-f]{64}\.(png|jpg|gif|webp)$`)
	// assetReferencePattern 在正文中查找附件文件名；只要正文包含文件名即视为引用。
	assetReferencePattern = regexp.MustCompile(`[0-9a-f]{64}\.(?:png|jpg|gif|webp)`)

	assetContentTypes = map[string]string{
		"png":  "image/png",
		"jpg":  "image/jpeg",
		"gif":  "image/gif",
		"webp": "image/webp",
	}

	// assetMu 串行化写入与回收：回收期间的写入等待其结束，被回收的文件会被重新写入。
	assetMu sync.Mutex

	// assetCollectorEnabled 为 false 时 notes_tree 不触发后台回收（测试中关闭，直接调用 collectGarbage）。
	assetCollectorEnabled = true
	assetCollectorOnce    sync.Once
	// assetLogf 记录后台回收的结果；测试替换为静默或捕获。
	assetLogf = log.Printf
)

// assetsDir 返回图片附件目录（不保证已存在）。
func assetsDir() (string, error) {
	root := core.UserDataDir()
	if root == "" {
		return "", errors.New("notes: 应用数据目录不可用")
	}
	return filepath.Join(root, appDataDirectory, assetDirName), nil
}

// detectAssetType 只按文件头魔数判定图片类型，返回扩展名。
func detectAssetType(data []byte) (string, bool) {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return "png", true
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		return "jpg", true
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return "gif", true
	case len(data) >= 12 && bytes.HasPrefix(data, []byte("RIFF")) && string(data[8:12]) == "WEBP":
		return "webp", true
	}
	return "", false
}

type assetPutArgs struct {
	FileName   *string `json:"fileName"`
	DataBase64 *string `json:"dataBase64"`
}

type assetPutResult struct {
	Ref string `json:"ref"`
}

func handleAssetPut(_ context.Context, args assetPutArgs) (any, error) {
	if args.FileName != nil && (len(*args.FileName) > maxAssetFileNameBytes || !singleLine(*args.FileName)) {
		return nil, errInvalidRequest
	}
	if args.DataBase64 == nil {
		return nil, errInvalidRequest
	}
	data, err := base64.StdEncoding.DecodeString(*args.DataBase64)
	if err != nil {
		return nil, errInvalidRequest
	}
	ref, err := putAsset(deref(args.FileName), data)
	if err != nil {
		return nil, err
	}
	return assetPutResult{Ref: ref}, nil
}

// putAsset 校验并保存一张图片，返回正文引用 /__notes-assets/<sha256>.<ext>。
// name 只是说明性的原始文件名，不参与类型判定与命名。同一内容已存在时不重复写入，
// 但会刷新文件修改时间，使回收宽限期重新计算。
func putAsset(name string, data []byte) (ref string, err error) {
	_ = name
	if len(data) > maxAssetBytes {
		return "", errAssetTooLarge
	}
	ext, ok := detectAssetType(data)
	if !ok {
		return "", errAssetType
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	fileName := hash + "." + ext

	dir, err := assetsDir()
	if err != nil {
		return "", err
	}
	db, err := openNotesDB()
	if err != nil {
		return "", err
	}

	assetMu.Lock()
	defer assetMu.Unlock()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("notes: create assets dir: %w", err)
	}
	path := filepath.Join(dir, fileName)
	now := time.Now()
	if sameAssetFile(path, data) {
		if err := os.Chtimes(path, now, now); err != nil {
			return "", fmt.Errorf("notes: touch asset: %w", err)
		}
	} else if err := writeAssetFile(dir, path, data); err != nil {
		return "", err
	}
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO note_assets(hash, ext, size, created_at) VALUES (?, ?, ?, ?) ON CONFLICT(hash) DO NOTHING`,
		hash, ext, len(data), now.UnixMilli()); err != nil {
		return "", fmt.Errorf("notes: record asset: %w", err)
	}
	return assetRefPrefix + fileName, nil
}

// sameAssetFile 判断 path 已是内容相同的普通文件（损坏或被替换的文件会被重写）。
func sameAssetFile(path string, data []byte) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != int64(len(data)) {
		return false
	}
	existing, err := os.ReadFile(path)
	return err == nil && bytes.Equal(existing, data)
}

// writeAssetFile 先写同目录临时文件再原子替换，避免中断时留下半个附件。
func writeAssetFile(dir, path string, data []byte) (err error) {
	tmp, err := os.CreateTemp(dir, assetTempPrefix+"*")
	if err != nil {
		return fmt.Errorf("notes: create asset temp: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		return fmt.Errorf("notes: write asset: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("notes: sync asset: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("notes: close asset: %w", err)
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("notes: commit asset: %w", err)
	}
	return nil
}

// startAssetCollector 在进程内第一次加载笔记树时于后台回收一次附件；失败只记日志。
func startAssetCollector() {
	if !assetCollectorEnabled {
		return
	}
	assetCollectorOnce.Do(func() {
		go func() {
			removed, err := collectGarbage(time.Now())
			if err != nil {
				assetLogf("notes: asset garbage collection: %v", err)
			}
			if removed > 0 {
				assetLogf("notes: removed %d unreferenced note assets", removed)
			}
		}()
	})
}

// collectGarbage 删除未被任何笔记正文引用、且最近一次写入早于 now-24h 的附件文件及其 note_assets 行；
// 同时清理文件已丢失的 note_assets 行与早于 24h 的中断写入临时文件。返回删除的附件文件数。
// 附件的“年龄”取入库时间与文件修改时间中较新者。目录中的其他条目不动。
func collectGarbage(now time.Time) (removed int, err error) {
	dir, err := assetsDir()
	if err != nil {
		return 0, err
	}
	writeDB, err := openNotesDB()
	if err != nil {
		return 0, err
	}
	readDB, err := openNotesReadDB()
	if err != nil {
		return 0, err
	}
	ctx := context.Background()
	cutoff := now.Add(-assetGracePeriod)

	assetMu.Lock()
	defer assetMu.Unlock()

	recorded, err := recordedAssets(ctx, readDB)
	if err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return 0, fmt.Errorf("notes: read assets dir: %w", err)
	}

	var errs []error
	present := map[string]time.Time{}
	for _, entry := range entries {
		name := entry.Name()
		info, err := entry.Info()
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, err)
			}
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		switch {
		case assetNamePattern.MatchString(name):
			present[name] = info.ModTime()
		case strings.HasPrefix(name, assetTempPrefix) && info.ModTime().Before(cutoff):
			if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, err)
			}
		}
	}

	referenced, err := referencedAssetNames(ctx, readDB)
	if err != nil {
		return 0, err
	}
	for name, modified := range present {
		if referenced[name] {
			continue
		}
		lastWritten := modified
		if created, ok := recorded[name]; ok && created.After(lastWritten) {
			lastWritten = created
		}
		if !lastWritten.Before(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
			continue
		}
		delete(present, name)
		removed++
	}

	var stale []string
	for name := range recorded {
		if _, ok := present[name]; !ok {
			stale = append(stale, strings.SplitN(name, ".", 2)[0])
		}
	}
	if err := deleteAssetRows(ctx, writeDB, stale); err != nil {
		errs = append(errs, err)
	}
	return removed, errors.Join(errs...)
}

// recordedAssets 读取 note_assets：文件名 → 入库时间。
func recordedAssets(ctx context.Context, db queryer) (map[string]time.Time, error) {
	rows, err := db.QueryContext(ctx, `SELECT hash, ext, created_at FROM note_assets`)
	if err != nil {
		return nil, fmt.Errorf("notes: list assets: %w", err)
	}
	defer rows.Close()
	recorded := map[string]time.Time{}
	for rows.Next() {
		var hash, ext string
		var createdAt int64
		if err := rows.Scan(&hash, &ext, &createdAt); err != nil {
			return nil, fmt.Errorf("notes: scan asset: %w", err)
		}
		recorded[hash+"."+ext] = time.UnixMilli(createdAt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("notes: list assets: %w", err)
	}
	return recorded, nil
}

// referencedAssetNames 逐篇扫描正文，收集出现过的附件文件名。
func referencedAssetNames(ctx context.Context, db queryer) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT body FROM notes`)
	if err != nil {
		return nil, fmt.Errorf("notes: scan note bodies: %w", err)
	}
	defer rows.Close()
	referenced := map[string]bool{}
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, fmt.Errorf("notes: scan note body: %w", err)
		}
		for _, name := range assetReferencePattern.FindAllString(body, -1) {
			referenced[name] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("notes: scan note bodies: %w", err)
	}
	return referenced, nil
}

func deleteAssetRows(ctx context.Context, db execer, hashes []string) error {
	for _, hash := range hashes {
		if _, err := db.ExecContext(ctx, `DELETE FROM note_assets WHERE hash = ?`, hash); err != nil {
			return fmt.Errorf("notes: delete asset row: %w", err)
		}
	}
	return nil
}
