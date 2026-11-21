package host

import (
	"bytes"
	"io"
	"io/fs"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"

	"github.com/tangoui/tango/asset"
)

// 插件兼容：官方及第三方插件的界面按插件开发约定用 --wails-draggable 标拖拽区，
// 经本路由返回的文本资源里改写为 tango 的 --tango-drag。MinCode 自己的样式
// 已直接使用 --tango-drag。
var (
	pluginDragToken = []byte("--wails-draggable")
	tangoDragToken  = []byte("--tango-drag")
)

// Handler 返回挂在 tango Options.Assets 上的总路由：大事件引用（events.go）
// 和前端资产。调用、事件、流都走 tango ipc，不经资源请求。
// devURL 非空时反向代理到 Vite 开发服务器；否则从 frontend 文件系统服务。
func (h *Host) Handler(frontend fs.FS, devURL string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(eventRefPath, h.handleEventRef)
	mux.Handle("/", assetHandler(frontend, devURL))
	return mux
}

func assetHandler(frontend fs.FS, devURL string) http.Handler {
	if devURL != "" {
		target, err := url.Parse(devURL)
		if err == nil {
			proxy := httputil.NewSingleHostReverseProxy(target)
			proxy.ModifyResponse = rewriteResponse
			return proxy
		}
	}
	if frontend == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "frontend not built: run the Vue build first", http.StatusNotFound)
		})
	}
	return rewriteHandler(asset.FS(frontend, asset.Options{SPA: true}))
}

// bufferRW 缓冲响应体以便改写拖拽属性。
type bufferRW struct {
	http.ResponseWriter
	buf    bytes.Buffer
	status int
}

func (b *bufferRW) WriteHeader(status int) { b.status = status }

func (b *bufferRW) Write(p []byte) (int, error) { return b.buf.Write(p) }

func rewriteHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &bufferRW{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		body := rec.buf.Bytes()
		if textual(rec.Header().Get("Content-Type")) && bytes.Contains(body, pluginDragToken) {
			body = bytes.ReplaceAll(body, pluginDragToken, tangoDragToken)
			rec.Header().Set("Content-Length", strconv.Itoa(len(body)))
		}
		w.WriteHeader(rec.status)
		_, _ = w.Write(body)
	})
}

// rewriteResponse 供开发代理改写 Vite 响应（CSS 在开发模式经 JS 模块注入）。
func rewriteResponse(resp *http.Response) error {
	if resp.Body == nil || !textual(resp.Header.Get("Content-Type")) {
		return nil
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return err
	}
	if bytes.Contains(body, pluginDragToken) {
		body = bytes.ReplaceAll(body, pluginDragToken, tangoDragToken)
		resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	return nil
}

func textual(contentType string) bool {
	if contentType == "" {
		return true
	}
	return strings.Contains(contentType, "text/") ||
		strings.Contains(contentType, "javascript") ||
		strings.Contains(contentType, "json") ||
		strings.Contains(contentType, "html") ||
		strings.Contains(contentType, "css") ||
		strings.Contains(contentType, "svg")
}
