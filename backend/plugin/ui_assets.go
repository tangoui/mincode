package plugin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"mincode/app/plugins/api"
)

const (
	pluginUIRoutePrefix = "/plugin-ui/"
	pluginUIFileMaxSize = 5 * 1024 * 1024
	// Static UI files are cacheable under their asset generation URL: the URL
	// changes whenever the package may have changed (see pluginUIAssetScope),
	// so a cached copy never needs revalidation. Error responses stay no-store.
	pluginUIAssetCacheControl = "private, max-age=31536000, immutable"
)

// pluginUIAssetScope is one installed generation of a plugin's static UI. Its
// random key is the asset URL capability and stays stable across the plugin's
// UI sessions so the WebView can reuse cached documents and compiled scripts.
// The scope is retired, and a fresh key issued, whenever the host revokes the
// plugin's UI (disable, reload, development generation swap, crash/error,
// upgrade/downgrade, Clear Plugin Data, uninstall) or the package root or
// parent origin changes. Session identity (session ID, nonce, generation) is
// never part of the asset URL path; it travels only in the URL fragment.
type pluginUIAssetScope struct {
	Key          string
	PluginID     string
	Root         string
	ParentOrigin string
	sessions     int
	// framed: the plugin uses the framed transport, so its pages may reach the
	// direct binary channel (CSP connect-src).
	framed bool
	// wasm: the plugin declares ui.wasm, so its pages may compile WebAssembly
	// (CSP 'wasm-unsafe-eval'). A capability change revokes the plugin's UI,
	// which retires this scope, so the mark never outlives the declaration.
	wasm bool
}

type pluginUISession struct {
	ctx          context.Context
	cancel       context.CancelFunc
	ID           string
	assets       *pluginUIAssetScope
	Nonce        string
	PluginID     string
	ViewID       string
	Generation   uint64
	Root         string
	Entry        string
	ParentOrigin string
	CreatedAt    time.Time
	// Framed sessions belong to a framed-transport plugin and may open the
	// direct binary channel (ui_direct.go).
	Framed bool
}

type pluginUISessionDTO struct {
	Mode             string              `json:"mode"`
	SessionID        string              `json:"sessionId"`
	PluginID         string              `json:"pluginId"`
	ViewID           string              `json:"viewId"`
	URL              string              `json:"url"`
	Nonce            string              `json:"nonce"`
	Generation       uint64              `json:"generation"`
	RequestTimeoutMS int                 `json:"requestTimeoutMs"`
	Actions          []pluginUIActionDTO `json:"actions"`
	FileDrop         *ManifestFileDrop   `json:"fileDrop,omitempty"`
	// Binary: the plugin uses the framed transport, so the view may exchange
	// binary frames with its process (ui_calls.go).
	Binary bool `json:"binary,omitempty"`
	// Prefetch: views[].prefetch, invoked by the host through this session
	// (plugin_host_ui_invoke) while the frame loads (view_prefetch.go).
	Prefetch *pluginUIPrefetchDTO `json:"prefetch,omitempty"`
	// Capabilities: the plugin's declared capabilities, so the host frame
	// offers page features gated on one (ai.config → features.aiConfig).
	Capabilities []api.Capability `json:"capabilities,omitempty"`
}

type pluginUIActionDTO struct {
	ID              string `json:"id"`
	Risk            string `json:"risk"`
	MaxPayloadBytes int    `json:"maxPayloadBytes"`
	ResultHandling  string `json:"resultHandling,omitempty"`
}

type pluginUIHost struct {
	mu       sync.RWMutex
	listener net.Listener
	server   *http.Server
	address  string
	sessions map[string]*pluginUISession
	// assets holds the current asset scope per plugin ID; byAssetKey indexes
	// the same scopes by their URL key.
	assets     map[string]*pluginUIAssetScope
	byAssetKey map[string]*pluginUIAssetScope
	// Direct binary channel (ui_direct.go): unused tokens, the open connection
	// per session, and where frames from views go.
	directTickets map[string]pluginUIDirectTicket
	directConns   map[string]*pluginUIDirectConn
	directInbound func(sessionID, channel string, data []byte) bool
}

func newPluginUIHost() *pluginUIHost {
	return &pluginUIHost{sessions: map[string]*pluginUISession{}, assets: map[string]*pluginUIAssetScope{}, byAssetKey: map[string]*pluginUIAssetScope{}}
}

// assetScopeLocked returns the plugin's current asset scope for root and
// parent origin, issuing candidateKey as a new key when there is none or the
// current one no longer matches.
func (host *pluginUIHost) assetScopeLocked(pluginID, root, parentOrigin, candidateKey string) *pluginUIAssetScope {
	if scope := host.assets[pluginID]; scope != nil {
		if scope.Root == root && scope.ParentOrigin == parentOrigin {
			return scope
		}
		host.retireAssetScopeLocked(scope)
	}
	scope := &pluginUIAssetScope{Key: candidateKey, PluginID: pluginID, Root: root, ParentOrigin: parentOrigin}
	host.assets[pluginID] = scope
	host.byAssetKey[candidateKey] = scope
	return scope
}

func (host *pluginUIHost) retireAssetScopeLocked(scope *pluginUIAssetScope) {
	if host.assets[scope.PluginID] == scope {
		delete(host.assets, scope.PluginID)
	}
	delete(host.byAssetKey, scope.Key)
}

// releaseSessionLocked removes a session and its hold on the asset scope.
func (host *pluginUIHost) releaseSessionLocked(session *pluginUISession) {
	session.cancel()
	host.closeDirectLocked(session.ID)
	delete(host.sessions, session.ID)
	if session.assets != nil && session.assets.sessions > 0 {
		session.assets.sessions--
	}
}

func (host *pluginUIHost) startLocked() error {
	if host.listener != nil {
		return nil
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("start plugin UI asset server: %w", err)
	}
	host.listener = listener
	host.address = listener.Addr().String()
	server := &http.Server{
		Handler:           host,
		ReadHeaderTimeout: 3 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 * 1024,
	}
	host.server = server
	go func() { _ = server.Serve(listener) }()
	return nil
}

func (host *pluginUIHost) Begin(pluginID, viewID string, generation uint64, root, entry, parentOrigin string, actions []pluginUIActionDTO) (pluginUISessionDTO, error) {
	origin, err := validatePluginUIParentOrigin(parentOrigin)
	if err != nil {
		return pluginUISessionDTO{}, err
	}
	sessionID, err := randomHex(16)
	if err != nil {
		return pluginUISessionDTO{}, err
	}
	candidateKey, err := randomHex(32)
	if err != nil {
		return pluginUISessionDTO{}, err
	}
	nonce, err := randomHex(32)
	if err != nil {
		return pluginUISessionDTO{}, err
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if err := host.startLocked(); err != nil {
		return pluginUISessionDTO{}, err
	}
	scope := host.assetScopeLocked(pluginID, root, origin, candidateKey)
	scope.sessions++
	sessionCtx, cancel := context.WithCancel(context.Background())
	session := &pluginUISession{
		ctx: sessionCtx, cancel: cancel,
		ID: sessionID, assets: scope, Nonce: nonce, PluginID: pluginID, ViewID: viewID,
		Generation: generation, Root: root, Entry: filepath.ToSlash(entry), ParentOrigin: origin, CreatedAt: time.Now(),
	}
	host.sessions[sessionID] = session
	entryURL := escapePluginUIPath(strings.TrimPrefix(session.Entry, "ui/"))
	return pluginUISessionDTO{
		Mode: "asset", SessionID: sessionID, PluginID: pluginID, ViewID: viewID,
		URL: "http://" + host.address + pluginUIRoutePrefix + scope.Key + "/" + entryURL +
			"#mincodeSession=" + url.QueryEscape(sessionID) + "&nonce=" + url.QueryEscape(nonce) + "&generation=" + fmt.Sprintf("%d", generation),
		Nonce: nonce, Generation: generation, RequestTimeoutMS: int(pluginInvokeTimeout / time.Millisecond),
		Actions: append([]pluginUIActionDTO(nil), actions...),
	}, nil
}

func (host *pluginUIHost) BeginDevelopment(pluginID, viewID string, generation uint64, rawURL, parentOrigin string, actions []pluginUIActionDTO) (pluginUISessionDTO, error) {
	origin, err := validatePluginUIParentOrigin(parentOrigin)
	if err != nil {
		return pluginUISessionDTO{}, err
	}
	developmentURL, err := validatePluginDevUIURL(rawURL)
	if err != nil || developmentURL == "" {
		return pluginUISessionDTO{}, errors.New("development UI URL is unavailable")
	}
	sessionID, err := randomHex(16)
	if err != nil {
		return pluginUISessionDTO{}, err
	}
	nonce, err := randomHex(32)
	if err != nil {
		return pluginUISessionDTO{}, err
	}
	host.mu.Lock()
	sessionCtx, cancel := context.WithCancel(context.Background())
	session := &pluginUISession{
		ctx: sessionCtx, cancel: cancel, ID: sessionID, Nonce: nonce, PluginID: pluginID, ViewID: viewID,
		Generation: generation, ParentOrigin: origin, CreatedAt: time.Now(),
	}
	host.sessions[sessionID] = session
	host.mu.Unlock()
	separator := "#"
	if strings.Contains(developmentURL, "#") {
		separator = "&"
	}
	return pluginUISessionDTO{
		Mode: "development", SessionID: sessionID, PluginID: pluginID, ViewID: viewID,
		URL:   developmentURL + separator + "mincodeSession=" + url.QueryEscape(sessionID) + "&nonce=" + url.QueryEscape(nonce) + "&generation=" + fmt.Sprintf("%d", generation),
		Nonce: nonce, Generation: generation, RequestTimeoutMS: int(pluginInvokeTimeout / time.Millisecond),
		Actions: append([]pluginUIActionDTO(nil), actions...),
	}, nil
}

func escapePluginUIPath(relative string) string {
	segments := strings.Split(filepath.ToSlash(relative), "/")
	for index, segment := range segments {
		segments[index] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}

func (host *pluginUIHost) Session(sessionID, nonce string) (*pluginUISession, error) {
	host.mu.RLock()
	session := host.sessions[strings.TrimSpace(sessionID)]
	if session == nil || session.Nonce != strings.TrimSpace(nonce) {
		host.mu.RUnlock()
		return nil, errors.New("plugin UI session is unavailable")
	}
	copy := *session
	host.mu.RUnlock()
	return &copy, nil
}

// CloseSession ends one view session. The plugin's asset key stays valid
// while another of its sessions is open and is reused by the next session of
// the same asset generation; with no open session the asset server answers 404.
// CloseSession ends one session and returns its plugin ID ("" when unknown).
func (host *pluginUIHost) CloseSession(sessionID string) string {
	host.mu.Lock()
	defer host.mu.Unlock()
	session := host.sessions[strings.TrimSpace(sessionID)]
	if session == nil {
		return ""
	}
	host.releaseSessionLocked(session)
	return session.PluginID
}

// SessionCount reports the plugin's open UI sessions (views, floating windows,
// custom editor documents).
func (host *pluginUIHost) SessionCount(pluginID string) int {
	host.mu.Lock()
	defer host.mu.Unlock()
	count := 0
	for _, session := range host.sessions {
		if session.PluginID == pluginID {
			count++
		}
	}
	return count
}

// RevokePlugin ends every session of the plugin and retires its asset
// generation, so the next session receives a new asset URL and copies cached
// under the old URL are never requested again.
// It returns the IDs of the sessions it closed (sorted; never nonces).
func (host *pluginUIHost) RevokePlugin(pluginID string) []string {
	host.mu.Lock()
	revoked := make([]string, 0)
	for _, session := range host.sessions {
		if session.PluginID == pluginID {
			revoked = append(revoked, session.ID)
			host.releaseSessionLocked(session)
		}
	}
	sort.Strings(revoked)
	if scope := host.assets[pluginID]; scope != nil {
		host.retireAssetScopeLocked(scope)
	}
	host.mu.Unlock()
	return revoked
}

func (host *pluginUIHost) Shutdown() {
	host.mu.Lock()
	server := host.server
	host.server = nil
	host.listener = nil
	host.address = ""
	for _, session := range host.sessions {
		session.cancel()
		host.closeDirectLocked(session.ID)
	}
	host.sessions = map[string]*pluginUISession{}
	host.assets = map[string]*pluginUIAssetScope{}
	host.byAssetKey = map[string]*pluginUIAssetScope{}
	host.mu.Unlock()
	if server != nil {
		_ = server.Close()
	}
}

func (host *pluginUIHost) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Referrer-Policy", "no-referrer")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), clipboard-read=(), clipboard-write=(), display-capture=(), fullscreen=()")
	if request.URL.Path == pluginUIDirectPath {
		host.mu.RLock()
		address := host.address
		host.mu.RUnlock()
		if request.Host != address {
			http.Error(response, "invalid host", http.StatusForbidden)
			return
		}
		host.serveDirect(response, request)
		return
	}
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		response.Header().Set("Allow", "GET, HEAD")
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	host.mu.RLock()
	address := host.address
	host.mu.RUnlock()
	if request.Host != address {
		http.Error(response, "invalid host", http.StatusForbidden)
		return
	}
	trimmed := strings.TrimPrefix(request.URL.Path, pluginUIRoutePrefix)
	if trimmed == request.URL.Path {
		http.NotFound(response, request)
		return
	}
	key, relative, found := strings.Cut(trimmed, "/")
	if !found || key == "" || relative == "" {
		http.NotFound(response, request)
		return
	}
	var scope pluginUIAssetScope
	host.mu.RLock()
	current := host.byAssetKey[key]
	if current != nil {
		scope = *current
	}
	host.mu.RUnlock()
	// Assets are served only while the scope is current and at least one of
	// the plugin's view sessions is open.
	if current == nil || scope.sessions == 0 {
		http.NotFound(response, request)
		return
	}
	response.Header().Set("Content-Security-Policy", pluginUICSP(scope.ParentOrigin, "http://"+address, scope.framed, scope.wasm))
	if request.URL.RawQuery != "" || strings.Contains(relative, "\\") || strings.ContainsRune(relative, '\x00') {
		http.NotFound(response, request)
		return
	}
	cleaned := filepath.ToSlash(filepath.Clean(relative))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
		http.NotFound(response, request)
		return
	}
	resolved, err := resolvePluginSubdirPath(scope.Root, "ui", filepath.FromSlash(cleaned))
	if err != nil {
		http.NotFound(response, request)
		return
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Size() > pluginUIFileMaxSize {
		http.NotFound(response, request)
		return
	}
	contentType := pluginUIContentType(resolved)
	if contentType == "" {
		http.Error(response, "unsupported media type", http.StatusUnsupportedMediaType)
		return
	}
	file, err := os.Open(resolved)
	if err != nil {
		http.NotFound(response, request)
		return
	}
	defer file.Close()
	response.Header().Set("Content-Type", contentType)
	// The plugin iframe intentionally has an opaque origin because its sandbox
	// omits allow-same-origin. Fetching any packaged asset (including HTML used
	// by offline editors such as Draw) therefore requires a CORS response even
	// though the request uses this session's tokenized loopback URL. Allow only
	// the opaque origin; the unguessable asset generation key remains the
	// asset authorization boundary and ordinary web origins receive no CORS access.
	response.Header().Add("Vary", "Origin")
	if request.Header.Get("Origin") == "null" {
		response.Header().Set("Access-Control-Allow-Origin", "null")
	}
	// Assets are served identity-encoded: the listener is bound to 127.0.0.1,
	// so gzip would only spend CPU and a fresh flate state per request without
	// saving meaningful transfer time. Only this successful static file
	// response is cacheable (see pluginUIAssetCacheControl); a cached copy may
	// outlive revocation, but it is package content, never user data, and its
	// URL is never issued again after the asset generation is retired.
	response.Header().Set("Cache-Control", pluginUIAssetCacheControl)
	response.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))
	response.WriteHeader(http.StatusOK)
	if request.Method != http.MethodGet {
		return
	}
	_, _ = io.CopyN(response, file, info.Size())
}

func pluginUIContentType(path string) string {
	name := strings.ToLower(filepath.Base(path))
	// Split draw.io bundles (<name>.part-0001…) are reassembled client-side by
	// the plugin shell; serve the opaque chunks as binary.
	if extension := filepath.Ext(name); len(extension) == len(".part-0000") && strings.HasPrefix(extension, ".part-") {
		digits := true
		for _, r := range extension[len(".part-"):] {
			if r < '0' || r > '9' {
				digits = false
				break
			}
		}
		if digits {
			return "application/octet-stream"
		}
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	case ".txt":
		return "text/plain; charset=utf-8"
	case ".xml":
		return "application/xml; charset=utf-8"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	case ".woff":
		return "font/woff"
	case ".woff2":
		return "font/woff2"
	case ".ttf":
		return "font/ttf"
	case ".wasm":
		// Compiled only by pages of ui.wasm plugins ('wasm-unsafe-eval');
		// elsewhere the CSP refuses compilation.
		return "application/wasm"
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	case ".ogg":
		return "audio/ogg"
	case ".mp4":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	default:
		return ""
	}
}

func pluginUICSP(parentOrigin, assetOrigin string, direct, wasm bool) string {
	// The iframe intentionally omits allow-same-origin, so WebKit gives the
	// document an opaque origin and does not match CSP 'self' for subresources.
	// Pin packaged assets and fetches to this host-owned loopback listener. Blob
	// scripts/styles/images are needed by offline editors such as draw.io, while
	// remote network origins remain unavailable. Media and workers come only
	// from packaged assets or blob: (local files the page holds), so neither
	// widens what the page can reach; workers inherit this policy.
	connect := assetOrigin
	if direct {
		// Framed-transport plugins may open the direct binary channel on this
		// same listener (ws://, no other host).
		connect += " ws://" + strings.TrimPrefix(assetOrigin, "http://")
	}
	script := assetOrigin + " blob:"
	if wasm {
		// ui.wasm: WebAssembly compilation only, never 'unsafe-eval'.
		script += " 'wasm-unsafe-eval'"
	}
	local := assetOrigin + " blob:"
	return "default-src 'none'; script-src " + script + "; style-src " + assetOrigin + " blob:; img-src " + assetOrigin + " data: blob:; font-src " + assetOrigin + " data: blob:; connect-src " + connect + "; frame-src 'none'; child-src 'none'; worker-src " + local + "; object-src 'none'; media-src " + local + "; manifest-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors " + parentOrigin
}

func validatePluginUIParentOrigin(value string) (string, error) {
	value = strings.TrimSpace(value)
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" && parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("invalid plugin UI parent origin")
	}
	if strings.HasSuffix(parsed.Host, ":") {
		return "", errors.New("invalid plugin UI parent origin port")
	}
	if err := validatePluginUIOriginPort(parsed.Port()); err != nil {
		return "", err
	}
	hostname := strings.ToLower(parsed.Hostname())
	switch parsed.Scheme {
	case "tango":
		// tango serves the app from tango://app (the Vite dev server is proxied
		// through the same scheme, so development uses that origin too).
		if hostname != "app" {
			return "", errors.New("plugin UI parent origin must be the local tango origin")
		}
	case "http", "https":
		if hostname != "localhost" && net.ParseIP(hostname) == nil {
			return "", errors.New("plugin UI parent origin must be local")
		}
		if ip := net.ParseIP(hostname); ip != nil && !ip.IsLoopback() {
			return "", errors.New("plugin UI parent origin must be loopback")
		}
	default:
		return "", errors.New("unsupported plugin UI parent origin")
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}

func validatePluginUIOriginPort(port string) error {
	if port == "" {
		return nil
	}
	value, err := strconv.Atoi(port)
	if err != nil || value < 1 || value > 65535 {
		return errors.New("invalid plugin UI parent origin port")
	}
	return nil
}

func randomHex(size int) (string, error) {
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("generate plugin UI token: %w", err)
	}
	return hex.EncodeToString(data), nil
}
