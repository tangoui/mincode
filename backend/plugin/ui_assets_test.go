package plugin

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestPluginUIHostServesSessionAssets(t *testing.T) {
	root := t.TempDir()
	uiRoot := filepath.Join(root, "ui")
	if err := os.MkdirAll(filepath.Join(uiRoot, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"index.html":                        "<!doctype html><title>Plugin</title>",
		"app.js":                            "globalThis.pluginReady = true;",
		"styles.css":                        "body { color: canvastext; }",
		"drawio/index.html":                 "<!doctype html><title>draw.io</title>",
		"drawio/js/main.js":                 "globalThis.drawioReady = true;",
		"drawio/css/main.css":               "body { display: grid; }",
		"drawio/js/app.min.js.part-0000":    "bundle-chunk",
		"drawio/stencils/basic.xml":         "<shapes/>",
		"drawio/fonts/mathjax-tex-font.ttf": "font-bytes",
		"drawio/resources/dia_zh.txt":       "shape=Shape",
		"unsupported.bin":                   "not served",
	}
	for name, contents := range files {
		path := filepath.Join(uiRoot, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	host := newPluginUIHost()
	defer host.Shutdown()
	session, err := host.Begin(
		"example.visual",
		"dashboard",
		1,
		root,
		"ui/index.html",
		"tango://app:9250",
		[]pluginUIActionDTO{{ID: "dashboard.read", Risk: "read", MaxPayloadBytes: 1024}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if session.RequestTimeoutMS != int(pluginInvokeTimeout/time.Millisecond) {
		t.Fatalf("request timeout = %d", session.RequestTimeoutMS)
	}
	parsed, err := url.Parse(session.URL)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Fragment = ""
	entryURL := parsed.String()
	assetBase := strings.TrimSuffix(entryURL, "index.html")

	response, err := http.Get(entryURL) // #nosec G107 -- URL is a test-only loopback listener.
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if string(body) != files["index.html"] {
		t.Fatalf("GET body = %q", body)
	}
	if got := response.Header.Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := response.Header.Get("Cache-Control"); got != pluginUIAssetCacheControl {
		t.Fatalf("Cache-Control = %q", got)
	}
	if got := response.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
	csp := response.Header.Get("Content-Security-Policy")
	assetOrigin := "http://" + parsed.Host
	for _, expected := range []string{
		"script-src " + assetOrigin + " blob:",
		"style-src " + assetOrigin + " blob:",
		"img-src " + assetOrigin + " data: blob:",
		"font-src " + assetOrigin + " data: blob:",
		"connect-src " + assetOrigin,
		"frame-ancestors tango://app:9250",
	} {
		if !strings.Contains(csp, expected) {
			t.Fatalf("Content-Security-Policy %q does not contain %q", csp, expected)
		}
	}
	for _, forbidden := range []string{"script-src 'self'", "style-src 'self'", "img-src 'self'", "font-src 'self'"} {
		if strings.Contains(csp, forbidden) {
			t.Fatalf("Content-Security-Policy %q unexpectedly contains %q", csp, forbidden)
		}
	}

	headRequest, err := http.NewRequest(http.MethodHead, assetBase+"app.js", nil)
	if err != nil {
		t.Fatal(err)
	}
	// sandbox="allow-scripts" intentionally creates an opaque frame origin.
	// ES module requests therefore arrive with Origin: null and need the narrow
	// matching CORS response to execute.
	headRequest.Header.Set("Origin", "null")
	headResponse, err := http.DefaultClient.Do(headRequest)
	if err != nil {
		t.Fatal(err)
	}
	headBody, readErr := io.ReadAll(headResponse.Body)
	_ = headResponse.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if headResponse.StatusCode != http.StatusOK {
		t.Fatalf("HEAD status = %d, want %d", headResponse.StatusCode, http.StatusOK)
	}
	if len(headBody) != 0 {
		t.Fatalf("HEAD returned %d body bytes", len(headBody))
	}
	if got := headResponse.Header.Get("Content-Type"); got != "text/javascript; charset=utf-8" {
		t.Fatalf("HEAD Content-Type = %q", got)
	}
	if got := headResponse.Header.Get("Access-Control-Allow-Origin"); got != "null" {
		t.Fatalf("HEAD Access-Control-Allow-Origin = %q, want %q", got, "null")
	}
	if got := headResponse.Header.Get("Vary"); got != "Origin" {
		t.Fatalf("HEAD Vary = %q, want %q", got, "Origin")
	}

	// The sandboxed draw.io shell fetches its nested HTML entry from the asset
	// origin; opaque-origin fetches need the same narrowly-scoped CORS response.
	drawioRequest, err := http.NewRequest(http.MethodGet, assetBase+"drawio/index.html", nil)
	if err != nil {
		t.Fatal(err)
	}
	drawioRequest.Header.Set("Origin", "null")
	drawioResponse, err := http.DefaultClient.Do(drawioRequest)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, drawioResponse.Body)
	_ = drawioResponse.Body.Close()
	if drawioResponse.StatusCode != http.StatusOK {
		t.Fatalf("draw.io asset status = %d, want %d", drawioResponse.StatusCode, http.StatusOK)
	}
	if got := drawioResponse.Header.Get("Access-Control-Allow-Origin"); got != "null" {
		t.Fatalf("draw.io Access-Control-Allow-Origin = %q, want %q", got, "null")
	}
	if got := drawioResponse.Header.Get("Vary"); got != "Origin" {
		t.Fatalf("draw.io Vary = %q, want %q", got, "Origin")
	}

	nonOpaqueRequest, err := http.NewRequest(http.MethodHead, assetBase+"app.js", nil)
	if err != nil {
		t.Fatal(err)
	}
	nonOpaqueRequest.Header.Set("Origin", "https://attacker.example")
	nonOpaqueResponse, err := http.DefaultClient.Do(nonOpaqueRequest)
	if err != nil {
		t.Fatal(err)
	}
	_ = nonOpaqueResponse.Body.Close()
	if got := nonOpaqueResponse.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("non-opaque Access-Control-Allow-Origin = %q, want empty", got)
	}

	assertPluginUIStatus(t, host, http.MethodGet, entryURL, "attacker.example", http.StatusForbidden)
	assertPluginUIStatus(t, host, http.MethodPost, entryURL, parsed.Host, http.StatusMethodNotAllowed)
	assertPluginUIStatus(t, host, http.MethodGet, entryURL+"?cache=1", parsed.Host, http.StatusNotFound)
	assertPluginUIStatus(t, host, http.MethodGet, assetBase+"drawio/index.html", parsed.Host, http.StatusOK)
	assertPluginUIStatus(t, host, http.MethodGet, assetBase+"drawio/js/main.js", parsed.Host, http.StatusOK)
	assertPluginUIStatus(t, host, http.MethodGet, assetBase+"drawio/css/main.css", parsed.Host, http.StatusOK)
	assertPluginUIStatus(t, host, http.MethodGet, assetBase+"drawio/js/app.min.js.part-0000", parsed.Host, http.StatusOK)
	assertPluginUIStatus(t, host, http.MethodGet, assetBase+"drawio/stencils/basic.xml", parsed.Host, http.StatusOK)
	assertPluginUIStatus(t, host, http.MethodGet, assetBase+"drawio/fonts/mathjax-tex-font.ttf", parsed.Host, http.StatusOK)
	assertPluginUIStatus(t, host, http.MethodGet, assetBase+"drawio/resources/dia_zh.txt", parsed.Host, http.StatusOK)
	assertPluginUIStatus(t, host, http.MethodGet, assetBase+"nested/", parsed.Host, http.StatusNotFound)
	assertPluginUIStatus(t, host, http.MethodGet, assetBase+"unsupported.bin", parsed.Host, http.StatusUnsupportedMediaType)
	assertPluginUIStatus(t, host, http.MethodGet, assetBase+"../index.html", parsed.Host, http.StatusNotFound)
	assertPluginUIStatus(t, host, http.MethodGet, assetBase+"%2e%2e/index.html", parsed.Host, http.StatusNotFound)

	outside := filepath.Join(t.TempDir(), "outside.js")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(uiRoot, "outside.js")); err != nil {
		if runtime.GOOS == "windows" {
			t.Logf("skipping symlink assertion: %v", err)
		} else {
			t.Fatal(err)
		}
	} else {
		assertPluginUIStatus(t, host, http.MethodGet, assetBase+"outside.js", parsed.Host, http.StatusNotFound)
	}

	host.CloseSession(session.SessionID)
	assertPluginUIStatus(t, host, http.MethodGet, entryURL, parsed.Host, http.StatusNotFound)
}

func TestPluginUIHostRevokesPluginSessions(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "ui"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ui", "index.html"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}

	host := newPluginUIHost()
	defer host.Shutdown()
	first, err := host.Begin("example.visual", "one", 1, root, "ui/index.html", "tango://app", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := host.Begin("example.visual", "two", 1, root, "ui/index.html", "tango://app", nil)
	if err != nil {
		t.Fatal(err)
	}
	other, err := host.Begin("example.other", "one", 1, root, "ui/index.html", "tango://app", nil)
	if err != nil {
		t.Fatal(err)
	}

	host.RevokePlugin("example.visual")
	for _, revoked := range []pluginUISessionDTO{first, second} {
		parsed, parseErr := url.Parse(revoked.URL)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		parsed.Fragment = ""
		assertPluginUIStatus(t, host, http.MethodGet, parsed.String(), parsed.Host, http.StatusNotFound)
	}
	parsedOther, err := url.Parse(other.URL)
	if err != nil {
		t.Fatal(err)
	}
	parsedOther.Fragment = ""
	assertPluginUIStatus(t, host, http.MethodGet, parsedOther.String(), parsedOther.Host, http.StatusOK)
}

func TestValidatePluginUIParentOrigin(t *testing.T) {
	valid := map[string]string{
		"http://localhost":           "http://localhost",
		"https://localhost:34115/":   "https://localhost:34115",
		"http://127.0.0.1:34115":     "http://127.0.0.1:34115",
		"https://[::1]:34115":        "https://[::1]:34115",
		"tango://app":                "tango://app",
		"tango://app:9250":           "tango://app:9250",
		"  HTTP://LOCALHOST:34115  ": "http://LOCALHOST:34115",
	}
	for input, expected := range valid {
		t.Run("valid_"+strings.NewReplacer(":", "_", "/", "_").Replace(input), func(t *testing.T) {
			actual, err := validatePluginUIParentOrigin(input)
			if err != nil {
				t.Fatalf("validatePluginUIParentOrigin(%q): %v", input, err)
			}
			if actual != expected {
				t.Fatalf("origin = %q, want %q", actual, expected)
			}
		})
	}

	invalid := []string{
		"",
		"null",
		"https://example.com",
		"http://192.168.1.10",
		"ftp://localhost",
		"tango://example",
		"tango://app:",
		"tango://app:0",
		"tango://app:65536",
		"tango://user@app",
		"http://user@localhost",
		"file://localhost/tmp",
		"http://localhost/path",
		"http://localhost?query=1",
		"http://localhost/#fragment",
	}
	for _, input := range invalid {
		t.Run("invalid_"+strings.NewReplacer(":", "_", "/", "_").Replace(input), func(t *testing.T) {
			if _, err := validatePluginUIParentOrigin(input); err == nil {
				t.Fatalf("validatePluginUIParentOrigin(%q) succeeded", input)
			}
		})
	}
}

func assertPluginUIStatus(t *testing.T, host http.Handler, method, target, requestHost string, expected int) {
	t.Helper()
	request := httptest.NewRequest(method, target, nil)
	request.Host = requestHost
	response := httptest.NewRecorder()
	host.ServeHTTP(response, request)
	if response.Code != expected {
		t.Fatalf("%s %s with Host %q returned %d, want %d", method, target, requestHost, response.Code, expected)
	}
}

func TestPluginUIHostEscapesEntryPathSegments(t *testing.T) {
	root := t.TempDir()
	uiRoot := filepath.Join(root, "ui", "nested")
	if err := os.MkdirAll(uiRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	entryName := "entry #%.html"
	contents := "<!doctype html><title>Escaped plugin entry</title>"
	if err := os.WriteFile(filepath.Join(uiRoot, entryName), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	host := newPluginUIHost()
	defer host.Shutdown()
	session, err := host.Begin(
		"example.visual",
		"escaped-entry",
		1,
		root,
		"ui/nested/"+entryName,
		"tango://app",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(session.URL, "/nested/entry%20%23%25.html#") {
		t.Fatalf("session URL does not escape path segments: %q", session.URL)
	}
	parsed, err := url.Parse(session.URL)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Fragment = ""
	response, err := http.Get(parsed.String()) // #nosec G107 -- URL is a test-only loopback listener.
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if string(body) != contents {
		t.Fatalf("GET body = %q", body)
	}
}

func TestPluginUIHostServesIdentityOverLoopback(t *testing.T) {
	root := t.TempDir()
	uiRoot := filepath.Join(root, "ui")
	if err := os.MkdirAll(uiRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	script := strings.Repeat("console.log('plugin');\n", 200)
	if err := os.WriteFile(filepath.Join(uiRoot, "app.js"), []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	// A binary asset must not be re-encoded.
	png := append([]byte{0x89, 'P', 'N', 'G'}, []byte(strings.Repeat("\x00\x01\x02", 64))...)
	if err := os.WriteFile(filepath.Join(uiRoot, "icon.png"), png, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(uiRoot, "index.html"), []byte("<!doctype html>"), 0o600); err != nil {
		t.Fatal(err)
	}

	host := newPluginUIHost()
	defer host.Shutdown()
	session, err := host.Begin("example.visual", "compress", 1, root, "ui/index.html", "tango://app:9250", nil)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(session.URL)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Fragment = ""
	assetBase := strings.TrimSuffix(parsed.String(), "index.html")

	// Disable the client's automatic gzip handling so Accept-Encoding is sent
	// exactly as specified and responses are not transparently decompressed.
	rawClient := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	get := func(method, path, acceptEncoding string) *http.Response {
		t.Helper()
		request, err := http.NewRequest(method, assetBase+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if acceptEncoding != "" {
			request.Header.Set("Accept-Encoding", acceptEncoding)
		}
		response, err := rawClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	for _, tc := range []struct {
		path, acceptEncoding, want string
	}{
		{"app.js", "gzip", script},
		{"app.js", "", script},
		{"icon.png", "gzip", string(png)},
	} {
		response := get(http.MethodGet, tc.path, tc.acceptEncoding)
		body, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if got := response.Header.Get("Content-Encoding"); got != "" {
			t.Fatalf("%s Content-Encoding = %q, want identity", tc.path, got)
		}
		if got := response.Header.Get("Content-Length"); got != fmt.Sprint(len(tc.want)) {
			t.Fatalf("%s Content-Length = %q, want %d", tc.path, got, len(tc.want))
		}
		if got := response.Header.Get("Cache-Control"); got != pluginUIAssetCacheControl {
			t.Fatalf("%s Cache-Control = %q, want %q", tc.path, got, pluginUIAssetCacheControl)
		}
		if string(body) != tc.want {
			t.Fatalf("%s body mismatch (%d bytes)", tc.path, len(body))
		}
	}

	head := get(http.MethodHead, "app.js", "gzip")
	headBody, err := io.ReadAll(head.Body)
	_ = head.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if got := head.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("HEAD Content-Encoding = %q, want identity", got)
	}
	if len(headBody) != 0 {
		t.Fatalf("HEAD returned %d body bytes", len(headBody))
	}
}

// pluginUIAssetBase returns the session URL without its fragment and entry file.
func pluginUIAssetBase(t *testing.T, session pluginUISessionDTO) (base, host string) {
	t.Helper()
	parsed, err := url.Parse(session.URL)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Fragment = ""
	return strings.TrimSuffix(parsed.String(), "index.html"), parsed.Host
}

func TestPluginUIHostKeepsAssetURLStablePerAssetGeneration(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "ui"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ui", "index.html"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	otherRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(otherRoot, "ui"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(otherRoot, "ui", "index.html"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	host := newPluginUIHost()
	defer host.Shutdown()
	begin := func(pluginID, packageRoot, origin string) pluginUISessionDTO {
		t.Helper()
		session, err := host.Begin(pluginID, "main", 1, packageRoot, "ui/index.html", origin, nil)
		if err != nil {
			t.Fatal(err)
		}
		return session
	}

	first := begin("example.visual", root, "tango://app")
	second := begin("example.visual", root, "tango://app")
	firstBase, assetHost := pluginUIAssetBase(t, first)
	secondBase, _ := pluginUIAssetBase(t, second)
	if firstBase != secondBase {
		t.Fatalf("asset URL changed within one asset generation: %q != %q", firstBase, secondBase)
	}
	if first.SessionID == second.SessionID || first.Nonce == second.Nonce || first.URL == second.URL {
		t.Fatal("sessions must keep distinct session IDs, nonces and fragments")
	}
	for _, session := range []pluginUISessionDTO{first, second} {
		if strings.Contains(firstBase, session.SessionID) || strings.Contains(firstBase, session.Nonce) {
			t.Fatalf("asset URL path %q exposes session identity", firstBase)
		}
	}
	if other, _ := pluginUIAssetBase(t, begin("example.other", root, "tango://app")); other == firstBase {
		t.Fatal("different plugins must not share an asset URL")
	}

	// Closing one session keeps the shared asset URL served for the other.
	host.CloseSession(first.SessionID)
	assertPluginUIStatus(t, host, http.MethodGet, firstBase+"index.html", assetHost, http.StatusOK)
	// With no open session the URL is not served, and errors are never cacheable.
	host.CloseSession(second.SessionID)
	request := httptest.NewRequest(http.MethodGet, firstBase+"index.html", nil)
	request.Host = assetHost
	recorder := httptest.NewRecorder()
	host.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("closed asset generation returned %d with Cache-Control %q", recorder.Code, recorder.Header().Get("Cache-Control"))
	}
	// Reopening the same generation reuses the URL so cached copies stay valid.
	reopened := begin("example.visual", root, "tango://app")
	if base, _ := pluginUIAssetBase(t, reopened); base != firstBase {
		t.Fatalf("reopened session asset URL = %q, want %q", base, firstBase)
	}

	// Revocation (disable, reload, upgrade, uninstall, Clear Plugin Data) retires the URL for good.
	host.RevokePlugin("example.visual")
	revoked := begin("example.visual", root, "tango://app")
	revokedBase, _ := pluginUIAssetBase(t, revoked)
	if revokedBase == firstBase {
		t.Fatal("revocation must issue a new asset URL")
	}
	assertPluginUIStatus(t, host, http.MethodGet, firstBase+"index.html", assetHost, http.StatusNotFound)
	assertPluginUIStatus(t, host, http.MethodGet, revokedBase+"index.html", assetHost, http.StatusOK)

	// A different package root or parent origin is a different asset generation.
	moved := begin("example.visual", otherRoot, "tango://app")
	movedBase, _ := pluginUIAssetBase(t, moved)
	if movedBase == revokedBase {
		t.Fatal("a new package root must issue a new asset URL")
	}
	assertPluginUIStatus(t, host, http.MethodGet, revokedBase+"index.html", assetHost, http.StatusNotFound)
	if base, _ := pluginUIAssetBase(t, begin("example.visual", otherRoot, "tango://app:9250")); base == movedBase {
		t.Fatal("a new parent origin must issue a new asset URL")
	}
}
