package notes

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// AssetMiddleware 让 宿主资源服务 同源提供笔记图片：只处理 GET/HEAD /__notes-assets/<sha256>.<ext>，
// 文件名不符合内容寻址格式时返回 404，其他路径交给 next（前端资源或开发服务器代理）。
func AssetMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath := r.URL.Path
		if requestPath != strings.TrimSuffix(assetRefPrefix, "/") && !strings.HasPrefix(requestPath, assetRefPrefix) {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(requestPath, assetRefPrefix)
		if !assetNamePattern.MatchString(name) {
			http.NotFound(w, r)
			return
		}
		serveAsset(w, r, name)
	})
}

// serveAsset 只提供附件目录中的普通文件（不跟随软链接）；name 已通过 assetNamePattern 校验。
func serveAsset(w http.ResponseWriter, r *http.Request, name string) {
	dir, err := assetsDir()
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	filePath := filepath.Join(dir, name)
	info, err := os.Lstat(filePath)
	if err != nil || !info.Mode().IsRegular() {
		if err == nil || errors.Is(err, fs.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	file, err := os.Open(filePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	defer file.Close()
	header := w.Header()
	header.Set("Content-Type", assetContentTypes[strings.TrimPrefix(path.Ext(name), ".")])
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Cache-Control", "private, max-age=31536000, immutable")
	http.ServeContent(w, r, name, info.ModTime(), file)
}
