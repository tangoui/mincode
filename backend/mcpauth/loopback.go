package mcpauth

import (
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"regexp"
	"sync"
	"time"
)

const (
	callbackPath = "/callback"
	closePage    = `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>MinCode</title></head><body><p>可以关闭此页面</p></body></html>`
)

// errorCodePattern restricts the "error" query parameter echoed into error
// text to an RFC 6749 error code.
var errorCodePattern = regexp.MustCompile(`^[a-z_]{1,64}$`)

type loopbackResult struct {
	code string
	err  error
}

// Loopback is the one-shot RFC 8252 redirect listener on 127.0.0.1. It hands
// the authorization code to Wait only for a GET /callback whose state matches;
// anything else gets an error page and the wait continues.
type Loopback struct {
	state     string
	host      string
	server    *http.Server
	result    chan loopbackResult
	done      chan struct{}
	deliver   sync.Once
	closeOnce sync.Once
}

// StartLoopback listens on 127.0.0.1 with a random port and returns the
// redirect URI http://127.0.0.1:<port>/callback.
func StartLoopback(state string) (*Loopback, string, error) {
	if state == "" {
		return nil, "", authError("MCP_AUTH_INVALID_STATE", "state 不能为空")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", authError("MCP_AUTH_LOOPBACK", "无法监听本机回调端口: %v", err)
	}
	lb := &Loopback{
		state:  state,
		host:   listener.Addr().String(),
		result: make(chan loopbackResult, 1),
		done:   make(chan struct{}),
	}
	lb.server = &http.Server{
		Handler:           lb,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	go func() { _ = lb.server.Serve(listener) }()
	return lb, "http://" + lb.host + callbackPath, nil
}

func (lb *Loopback) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	header := w.Header()
	header.Set("Cache-Control", "no-store")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Content-Security-Policy", "default-src 'none'")
	// Host check blocks DNS-rebinding pages from probing the listener.
	if r.Host != lb.host {
		http.Error(w, "无效的回调请求", http.StatusBadRequest)
		return
	}
	if r.URL.Path != callbackPath {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "无效的回调请求", http.StatusMethodNotAllowed)
		return
	}
	query := r.URL.Query()
	if subtle.ConstantTimeCompare([]byte(query.Get("state")), []byte(lb.state)) != 1 {
		http.Error(w, "无效的回调请求", http.StatusBadRequest)
		return
	}
	if oauthErr := query.Get("error"); oauthErr != "" {
		if !errorCodePattern.MatchString(oauthErr) {
			oauthErr = "unknown_error"
		}
		lb.finish(loopbackResult{err: authError("MCP_AUTH_DENIED", "授权未完成（%s）", oauthErr)})
		writeClosePage(w)
		return
	}
	code := query.Get("code")
	if code == "" {
		http.Error(w, "无效的回调请求", http.StatusBadRequest)
		return
	}
	lb.finish(loopbackResult{code: code})
	writeClosePage(w)
}

func writeClosePage(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, closePage)
}

func (lb *Loopback) finish(result loopbackResult) {
	lb.deliver.Do(func() { lb.result <- result })
}

// Wait blocks until the browser delivers the callback, the context ends
// ([MCP_AUTH_TIMEOUT] on deadline, [MCP_AUTH_CANCELLED] otherwise) or the
// listener is closed ([MCP_AUTH_CANCELLED]).
func (lb *Loopback) Wait(ctx context.Context) (string, error) {
	select {
	case result := <-lb.result:
		return result.code, result.err
	case <-lb.done:
		return "", authError("MCP_AUTH_CANCELLED", "登录已取消")
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", authError("MCP_AUTH_TIMEOUT", "等待浏览器授权超时")
		}
		return "", authError("MCP_AUTH_CANCELLED", "登录已取消")
	}
}

// Close stops the listener. It is safe to call more than once.
func (lb *Loopback) Close() {
	lb.closeOnce.Do(func() {
		close(lb.done)
		_ = lb.server.Close()
	})
}
