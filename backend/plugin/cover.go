package plugin

import (
	"bytes"
	"encoding/base64"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"io/fs"
	"os"
	"strings"
	"sync"
	"time"
)

const maxCoverBytes = 512 * 1024

// Covers are optional, bounded raster assets. Invalid assets never break the catalog.
func coverData(files fs.FS, name string) string {
	if name == "" || !fs.ValidPath(name) || strings.ContainsAny(name, "\\:") {
		return ""
	}
	file, err := files.Open(name)
	if err != nil {
		return ""
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxCoverBytes {
		return ""
	}
	data, err := io.ReadAll(io.LimitReader(file, maxCoverBytes+1))
	if err != nil || len(data) > maxCoverBytes {
		return ""
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width <= 0 || config.Height <= 0 || config.Width > 4096 || config.Height > 4096 {
		return ""
	}
	if format != "png" && format != "jpeg" && format != "gif" {
		return ""
	}
	return "data:image/" + format + ";base64," + base64.StdEncoding.EncodeToString(data)
}

// coverCacheKey identifies one cover file revision; a changed size or mtime
// forces a re-read so the catalog never serves stale artwork.
type coverCacheKey struct {
	directory, name string
}

type coverCacheEntry struct {
	size    int64
	modTime time.Time
	data    string
}

const maxCoverCacheEntries = 256

var coverCache = struct {
	sync.Mutex
	entries map[coverCacheKey]coverCacheEntry
}{entries: map[coverCacheKey]coverCacheEntry{}}

// localCoverData returns the cover as a data URL. Results (including rejected
// covers) are cached per file revision so plugin_host_list does not re-read,
// decode and base64-encode unchanged images on every call.
func localCoverData(directory, name string) string {
	if name == "" || !fs.ValidPath(name) || strings.ContainsAny(name, "\\:") {
		return ""
	}
	// Root confines symlink resolution as well as relative paths to the plugin directory.
	root, err := os.OpenRoot(directory)
	if err != nil {
		return ""
	}
	defer root.Close()
	info, err := root.Stat(name)
	if err != nil {
		return ""
	}
	key := coverCacheKey{directory: directory, name: name}
	coverCache.Lock()
	cached, ok := coverCache.entries[key]
	coverCache.Unlock()
	if ok && cached.size == info.Size() && cached.modTime.Equal(info.ModTime()) {
		return cached.data
	}
	data := coverData(root.FS(), name)
	coverCache.Lock()
	if len(coverCache.entries) >= maxCoverCacheEntries {
		clear(coverCache.entries)
	}
	coverCache.entries[key] = coverCacheEntry{size: info.Size(), modTime: info.ModTime(), data: data}
	coverCache.Unlock()
	return data
}
