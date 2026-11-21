package http

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"mincode/app/backend/core"
)

// checkSameOriginRedirect 是 HTTP 调试与 SSE 客户端共用的重定向策略。
//
// net/http 只会在跨主机跳转时剥离 Authorization / Cookie，用户自定义的
// X-Api-Key 等头仍会被转发；307/308 还会通过 GetBody 重放请求体。因此这里
// 只允许同源（scheme + host + 有效端口均相同）跳转，并拒绝 https → http 降级。
// 每一跳仍复用初次请求的 URL 校验，避免借跳转访问被拦截的地址。
func checkSameOriginRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= httpMaxRedirects {
		return errors.New("超过最大重定向次数")
	}
	if err := core.ValidateRequestURL(req.URL.String()); err != nil {
		return fmt.Errorf("重定向目标被拒绝: %w", err)
	}
	if len(via) == 0 {
		return nil
	}
	origin := via[0].URL
	if strings.EqualFold(origin.Scheme, "https") && !strings.EqualFold(req.URL.Scheme, "https") {
		return errors.New("已拒绝从 https 降级到 http 的重定向")
	}
	if !sameOrigin(origin, req.URL) {
		return fmt.Errorf("已拒绝跨域重定向（为避免泄露请求头和请求体）: %s", req.URL.Redacted())
	}
	return nil
}

func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) &&
		strings.EqualFold(a.Hostname(), b.Hostname()) &&
		effectivePort(a) == effectivePort(b)
}

func effectivePort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	return "80"
}
