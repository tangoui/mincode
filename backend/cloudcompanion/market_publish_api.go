package cloudcompanion

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"mincode/app/backend/core"
	"mincode/app/backend/marketpublish"
)

// Each publish operation gets its own adapter. Only this package retains the bound session;
// marketpublish and the future development bridge receive an email, never member credentials.
// A switch/logout during the run also fails closed, not just the Account check before lookup.
func MarketPublishAPI() marketpublish.API { return &marketPublishAPI{} }

type marketPublishAPI struct {
	mu      sync.Mutex
	session *persistedSession
}

func (a *marketPublishAPI) Account(ctx context.Context) (string, bool, error) {
	session, err := publishSession(ctx)
	if err != nil {
		var apiErr *marketpublish.APIError
		if errors.As(err, &apiErr) && apiErr.Reason == "not_signed_in" {
			return "", false, nil
		}
		return "", false, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.session != nil && !sameMemberCredential(*a.session, session) {
		return "", false, publishAccountChanged()
	}
	a.session = &session
	return session.Email, true, nil
}
func (a *marketPublishAPI) current(ctx context.Context) (persistedSession, error) {
	if err := ctx.Err(); err != nil {
		return persistedSession{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.session == nil {
		return persistedSession{}, &marketpublish.APIError{Reason: "not_signed_in", Message: "请先在 MinCode 中登录云账号"}
	}
	if !sameMemberSession(*a.session) {
		return persistedSession{}, publishAccountChanged()
	}
	return *a.session, nil
}
func (a *marketPublishAPI) Lookup(ctx context.Context, key string) (marketpublish.Lookup, error) {
	session, err := a.current(ctx)
	if err != nil {
		return marketpublish.Lookup{}, err
	}
	return marketplaceLookupSession(ctx, session, key)
}
func (a *marketPublishAPI) CreateDraft(ctx context.Context, in marketpublish.DraftInput) (int64, bool, error) {
	session, err := a.current(ctx)
	if err != nil {
		return 0, false, err
	}
	return marketplaceCreateDraftSession(ctx, session, in)
}
func (a *marketPublishAPI) UpdateDraft(ctx context.Context, id int64, in marketpublish.DraftInput) error {
	session, err := a.current(ctx)
	if err != nil {
		return err
	}
	return marketplaceUpdateDraftSession(ctx, session, id, in)
}
func (a *marketPublishAPI) UploadCover(ctx context.Context, path string, progress func(int64, int64)) (string, error) {
	session, err := a.current(ctx)
	if err != nil {
		return "", err
	}
	return marketplaceUploadCoverSession(ctx, session, path, progress)
}
func (a *marketPublishAPI) UploadArtifact(ctx context.Context, id int64, target marketpublish.Target, path, hash string, progress func(int64, int64)) error {
	session, err := a.current(ctx)
	if err != nil {
		return err
	}
	return memberMultipartPostSession(ctx, session, developerAPIPath+"upload", artifactFields(id, target), "file", path, filepath.Base(path), hash, progress, nil)
}
func (a *marketPublishAPI) Submit(ctx context.Context, id int64) error {
	session, err := a.current(ctx)
	if err != nil {
		return err
	}
	return developerRequest(ctx, session, "submit", nil, url.Values{"release_id": {strconv.FormatInt(id, 10)}}, nil)
}
func (a *marketPublishAPI) FetchCover(ctx context.Context, rawURL string) ([]byte, error) {
	session, err := a.current(ctx)
	if err != nil {
		return nil, err
	}
	// Only the exact configured session origin gets the local-development exception.
	// Cross-origin covers must remain public at both the URL and actual dial boundaries.
	endpoint, sessionOrigin, err := resolveMarketCoverURL(session.APIBaseURL, rawURL)
	if err != nil {
		return nil, errors.New("云端封面地址无效")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, errors.New("无法创建封面读取请求")
	}
	client := marketCoverClientForURL(sessionOrigin)
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("读取云端封面失败，请稍后重试")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("读取云端封面失败，请稍后重试")
	}
	if response.ContentLength > 2<<20 {
		return nil, errors.New("云端封面超过 2 MiB")
	}
	raw, err := core.ReadAllLimited(response.Body, 2<<20)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, errors.New("读取云端封面失败或图片超过 2 MiB")
	}
	if !sameMemberSession(session) {
		return nil, publishAccountChanged()
	}
	return raw, ctx.Err()
}
func (a *marketPublishAPI) DeveloperURL(ctx context.Context) string {
	session, err := a.current(ctx)
	if err != nil {
		return ""
	}
	return strings.TrimRight(session.APIBaseURL, "/") + "/marketplace?tab=account"
}

var _ marketpublish.API = (*marketPublishAPI)(nil)

// A fresh transport per fetch cannot reuse a connection authorized for another origin. The
// resolver and dialer are injected together so tests can exercise the real boundary offline.
var marketCoverClientForURL = func(sessionOrigin bool) *http.Client {
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	return newMarketCoverHTTPClient(sessionOrigin, net.DefaultResolver.LookupNetIP, dialer.DialContext)
}

func newMarketCoverHTTPClient(sessionOrigin bool,
	lookup func(context.Context, string, string) ([]netip.Addr, error),
	dial func(context.Context, string, string) (net.Conn, error)) *http.Client {
	return &http.Client{
		Timeout: cloudHTTPTimeout,
		Transport: &http.Transport{
			Proxy:               nil,
			DialContext:         marketCoverDialContext(sessionOrigin, lookup, dial),
			TLSHandshakeTimeout: 10 * time.Second,
			DisableKeepAlives:   true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func resolveMarketCoverURL(baseURL, rawURL string) (*url.URL, bool, error) {
	invalid := errors.New("云端封面地址无效")
	base, err := url.Parse(strings.TrimRight(baseURL, "/") + "/")
	if err != nil || base.Hostname() == "" {
		return nil, false, invalid
	}
	reference, err := url.Parse(rawURL)
	if err != nil || reference.User != nil || reference.Fragment != "" || reference.Opaque != "" {
		return nil, false, invalid
	}
	resolved := base.ResolveReference(reference)
	if resolved.Hostname() == "" || resolved.User != nil || resolved.Scheme != "http" && resolved.Scheme != "https" {
		return nil, false, invalid
	}
	port := func(u *url.URL) string {
		if u.Port() != "" {
			return u.Port()
		}
		if u.Scheme == "https" {
			return "443"
		}
		return "80"
	}
	sameOrigin := resolved.Scheme == base.Scheme && strings.EqualFold(resolved.Hostname(), base.Hostname()) && port(resolved) == port(base)
	if !sameOrigin {
		if resolved.Scheme != "https" || port(resolved) != "443" {
			return nil, false, invalid
		}
		if ip, err := netip.ParseAddr(resolved.Hostname()); err == nil && !marketCoverPublicIP(ip) {
			return nil, false, invalid
		}
	}
	return resolved, sameOrigin, nil
}

// Public unicast only. IsGlobalUnicast alone also accepts RFC1918, CGNAT, documentation,
// benchmarking and some transition ranges; none is an acceptable cross-origin cover server.
var marketCoverNonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	// Special relay range, including the non-globally-reachable 6a44 relay 192.88.99.2.
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
}

func marketCoverPublicIP(ip netip.Addr) bool {
	if !ip.IsValid() || ip.Zone() != "" {
		return false
	}
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	// Only currently allocated global IPv6 unicast; excludes local-use NAT64/mapped routes.
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	for _, prefix := range marketCoverNonPublicPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

func marketCoverDialContext(sessionOrigin bool,
	lookup func(context.Context, string, string) ([]netip.Addr, error),
	dial func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// The URL gate established exact scheme/host/port equality before this transport existed.
		if sessionOrigin {
			return dial(ctx, network, address)
		}
		refused := errors.New("云端封面必须使用公共 HTTPS 地址")
		host, port, err := net.SplitHostPort(address)
		if err != nil || port != "443" {
			return nil, refused
		}
		var ips []netip.Addr
		if ip, err := netip.ParseAddr(host); err == nil {
			ips = []netip.Addr{ip}
		} else {
			ips, err = lookup(ctx, "ip", host)
			if err != nil {
				return nil, refused
			}
		}
		if len(ips) == 0 {
			return nil, refused
		}
		// Reject the entire DNS answer, including mixed public/private results, before any dial.
		for _, ip := range ips {
			if !marketCoverPublicIP(ip) {
				return nil, refused
			}
		}
		for _, ip := range ips {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			// Dial the checked numeric IP, never hostname (which would resolve again and permit rebinding).
			conn, err := dial(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
			if err == nil {
				return conn, nil
			}
		}
		return nil, errors.New("无法连接公共封面服务器")
	}
}
