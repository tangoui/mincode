package updater

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mincode/app/backend/updateauth"
)

func TestParseManifestRawAndEnvelope(t *testing.T) {
	manifestJSON := `{"version":"1.2.3","version_code":123,"notes":"notes","pub_date":"2026-08-10T00:00:00Z","platforms":{"darwin-aarch64":{"url":"/MinCode.dmg","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","file_size":42}}}`

	for name, payload := range map[string]string{
		"raw":      manifestJSON,
		"envelope": `{"code":200,"msg":"ok","data":` + manifestJSON + `}`,
	} {
		t.Run(name, func(t *testing.T) {
			manifest, err := parseManifest([]byte(payload))
			if err != nil {
				t.Fatalf("parseManifest() error = %v", err)
			}
			if manifest.Version != "1.2.3" || manifest.VersionCode != 123 {
				t.Fatalf("unexpected manifest: %#v", manifest)
			}
			if manifest.Platforms["darwin-aarch64"].FileSize != 42 {
				t.Fatalf("unexpected platform payload: %#v", manifest.Platforms)
			}
		})
	}
}

func TestParseManifestRejectsReadableNonJSONResponse(t *testing.T) {
	_, err := parseManifest([]byte("Error: database unavailable"))
	if err == nil || !strings.Contains(err.Error(), "更新清单不是有效 JSON") {
		t.Fatalf("expected readable JSON error, got %v", err)
	}
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		left  string
		right string
		want  int
	}{
		{left: "0.1.1", right: "0.1.0", want: 1},
		{left: "1.0.0", right: "0.9.9", want: 1},
		{left: "1.0.0", right: "1.0.0-beta", want: 1},
		{left: "v1.2.3", right: "1.2.3", want: 0},
		{left: "1.0.0-beta", right: "1.0.0", want: -1},
		{left: "1.0.0+local", right: "1.0.0", want: 0},
		{left: "1.0.0-beta.10", right: "1.0.0-beta.2", want: 1},
		{left: "1.0.0.10", right: "1.0.0.2", want: 1},
		{left: "1.0.0", right: "1.0.0.0", want: 0},
		{left: "1.0.0.1-beta", right: "1.0.0.1", want: -1},
	}
	for _, test := range tests {
		t.Run(test.left+"_vs_"+test.right, func(t *testing.T) {
			got, err := compareVersions(test.left, test.right)
			if err != nil {
				t.Fatal(err)
			}
			if (got < 0 && test.want >= 0) || (got == 0 && test.want != 0) || (got > 0 && test.want <= 0) {
				t.Fatalf("compareVersions(%q, %q) = %d, want sign %d", test.left, test.right, got, test.want)
			}
		})
	}
	for _, invalid := range []string{"", "oops", "1.0", "1.0.0.1.2", "1.0.0-", "1.0.0+", "1.0.0-beta..2"} {
		if _, err := compareVersions(invalid, "1.0.0"); err == nil {
			t.Errorf("expected invalid remote version %q to be rejected", invalid)
		}
		if _, err := compareVersions("1.0.0", invalid); err == nil {
			t.Errorf("expected invalid current version %q to be rejected", invalid)
		}
	}
}

func TestValidateRemoteURL(t *testing.T) {
	for _, raw := range []string{
		"https://api.mincodeai.cn/release.dmg",
		"http://127.0.0.1:8787/release.dmg",
		"http://localhost:8787/release.dmg",
	} {
		parsed, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateRemoteURL(parsed); err != nil {
			t.Fatalf("validateRemoteURL(%q) error = %v", raw, err)
		}
	}

	parsed, err := url.Parse("http://updates.example.com/release.dmg")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateRemoteURL(parsed); err == nil || !strings.Contains(err.Error(), "必须使用 HTTPS") {
		t.Fatalf("expected insecure remote HTTP rejection, got %v", err)
	}
}

func TestDownloadAndInstallHandlerVerifiesAndOpensPackage(t *testing.T) {
	packageBody := []byte("verified MinCode package")
	server := newUpdateTestServer(t, packageBody, hex.EncodeToString(sumSHA256(packageBody)), int64(len(packageBody)))
	defer server.Close()
	setTestCacheHome(t)

	originalOpen := openUpdatePackageFn
	defer func() { openUpdatePackageFn = originalOpen }()
	var openedPath string
	openUpdatePackageFn = func(path string) error {
		openedPath = path
		return nil
	}

	resultJSON, err := downloadAndInstallHandler("", fmt.Sprintf(`{"serverUrl":%q}`, server.URL))
	if err != nil {
		t.Fatalf("downloadAndInstallHandler() error = %v", err)
	}
	var result downloadResult
	if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if !result.Opened || result.Version != "9.9.9" || openedPath == "" {
		t.Fatalf("unexpected result=%#v openedPath=%q", result, openedPath)
	}
	stored, err := os.ReadFile(openedPath)
	if err != nil {
		t.Fatalf("read opened package: %v", err)
	}
	if string(stored) != string(packageBody) {
		t.Fatalf("stored package mismatch: %q", stored)
	}
}

func TestDownloadAndInstallHandlerRejectsHashMismatchBeforeOpen(t *testing.T) {
	packageBody := []byte("tampered package")
	wrongHash := strings.Repeat("0", sha256.Size*2)
	server := newUpdateTestServer(t, packageBody, wrongHash, int64(len(packageBody)))
	defer server.Close()
	setTestCacheHome(t)

	originalOpen := openUpdatePackageFn
	defer func() { openUpdatePackageFn = originalOpen }()
	opened := false
	openUpdatePackageFn = func(string) error {
		opened = true
		return nil
	}

	_, err := downloadAndInstallHandler("", fmt.Sprintf(`{"serverUrl":%q}`, server.URL))
	if err == nil || !strings.Contains(err.Error(), "SHA-256 校验失败") {
		t.Fatalf("expected hash mismatch, got %v", err)
	}
	if opened {
		t.Fatal("installer was opened after failed hash verification")
	}
}

func TestDownloadAndInstallHandlerRejectsSizeMismatchBeforeOpen(t *testing.T) {
	packageBody := []byte("size mismatch")
	server := newUpdateTestServer(t, packageBody, hex.EncodeToString(sumSHA256(packageBody)), int64(len(packageBody)+1))
	defer server.Close()
	setTestCacheHome(t)

	originalOpen := openUpdatePackageFn
	defer func() { openUpdatePackageFn = originalOpen }()
	opened := false
	openUpdatePackageFn = func(string) error {
		opened = true
		return nil
	}

	_, err := downloadAndInstallHandler("", fmt.Sprintf(`{"serverUrl":%q}`, server.URL))
	if err == nil || !strings.Contains(err.Error(), "大小") {
		t.Fatalf("expected size mismatch, got %v", err)
	}
	if opened {
		t.Fatal("installer was opened after failed size verification")
	}
}

func signTestManifest(t *testing.T, manifest updateManifest) updateManifest {
	t.Helper()
	seed := sha256.Sum256([]byte("mincode updater test signing seed"))
	privateKey := ed25519.NewKeyFromSeed(seed[:])
	ManifestPublicKey = base64.StdEncoding.EncodeToString(privateKey.Public().(ed25519.PublicKey))
	if err := updateauth.Sign(&manifest, base64.StdEncoding.EncodeToString(privateKey)); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func newUpdateTestServer(t *testing.T, packageBody []byte, hash string, fileSize int64) *httptest.Server {
	t.Helper()
	platform, err := currentPlatform()
	if err != nil {
		t.Skip(err)
	}
	extension := map[string]string{
		"windows-x86_64": ".exe",
		"darwin-x86_64":  ".dmg",
		"darwin-aarch64": ".dmg",
		"linux-x86_64":   ".AppImage",
	}[platform]
	packagePath := "/MinCode-9.9.9" + extension

	mux := http.NewServeMux()
	mux.HandleFunc(manifestPath, func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		pubDate := "2026-08-10T00:00:00Z"
		manifest := signTestManifest(t, updateManifest{
			Version: "9.9.9", VersionCode: 999, Notes: "test release", PubDate: &pubDate,
			Platforms: map[string]manifestPlatform{
				platform: {URL: packagePath, SHA256: hash, FileSize: fileSize},
			},
		})
		_ = json.NewEncoder(writer).Encode(map[string]any{"code": 200, "msg": "ok", "data": manifest})
	})
	mux.HandleFunc(packagePath, func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Length", fmt.Sprintf("%d", len(packageBody)))
		_, _ = writer.Write(packageBody)
	})
	return httptest.NewServer(mux)
}

func sumSHA256(value []byte) []byte {
	sum := sha256.Sum256(value)
	return sum[:]
}

func setTestCacheHome(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	// os.UserHomeDir reads USERPROFILE on Windows.
	t.Setenv("USERPROFILE", root)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("LOCALAPPDATA", filepath.Join(root, "cache"))
}

func TestFetchManifestReportsHTTPBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Error(writer, "Error: updater unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	_, err := fetchManifest(context.Background(), server.URL)
	if err == nil || !strings.Contains(err.Error(), "Error: updater unavailable") {
		t.Fatalf("expected response body in HTTP error, got %v", err)
	}
}

func TestFetchManifestAcrossPoolFailsOverToFallback(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead.Close() // 端口已关闭，模拟主地址网络不可达。

	working := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		manifest := signTestManifest(t, updateManifest{Version: "1.2.3", Platforms: map[string]manifestPlatform{}})
		_ = json.NewEncoder(writer).Encode(manifest)
	}))
	defer working.Close()

	serverURL, manifest, err := fetchManifestAcrossPool(context.Background(), []string{dead.URL, working.URL})
	if err != nil {
		t.Fatalf("fetchManifestAcrossPool() error = %v", err)
	}
	if serverURL != working.URL {
		t.Fatalf("serverURL = %q, want fallback %q", serverURL, working.URL)
	}
	if manifest.Version != "1.2.3" {
		t.Fatalf("manifest.Version = %q, want 1.2.3", manifest.Version)
	}
}

func TestFetchManifestAcrossPoolStopsOnClientError(t *testing.T) {
	fallbackHit := false
	working := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		fallbackHit = true
		writer.Header().Set("Content-Type", "application/json")
		manifest := signTestManifest(t, updateManifest{Version: "1.2.3", Platforms: map[string]manifestPlatform{}})
		_ = json.NewEncoder(writer).Encode(manifest)
	}))
	defer working.Close()

	primary := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "not found", http.StatusNotFound)
	}))
	defer primary.Close()

	_, _, err := fetchManifestAcrossPool(context.Background(), []string{primary.URL, working.URL})
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("expected HTTP 404 error, got %v", err)
	}
	if fallbackHit {
		t.Fatal("client error must not fail over to the next server")
	}
}

func TestNoActiveReleaseIsReportedAsUpToDate(t *testing.T) {
	fallbackHit := false
	fallback := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { fallbackHit = true }))
	defer fallback.Close()
	primary := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusNotFound)
		_, _ = writer.Write([]byte(`{"code":"no_active_release","msg":"No active update"}`))
	}))
	defer primary.Close()

	_, _, err := fetchManifestAcrossPool(context.Background(), []string{primary.URL, fallback.URL})
	if !errors.Is(err, errNoActiveRelease) {
		t.Fatalf("expected errNoActiveRelease, got %v", err)
	}
	if fallbackHit {
		t.Fatal("a healthy service without releases must not fail over")
	}

	resolved, err := resolveAvailableUpdate(context.Background(), primary.URL, "1.0.0")
	if err != nil {
		t.Fatalf("resolveAvailableUpdate() error = %v", err)
	}
	if resolved.Info.Available || resolved.Info.Version != "" || resolved.Info.CurrentVersion != "1.0.0" {
		t.Fatalf("unexpected info %+v", resolved.Info)
	}

	args, _ := json.Marshal(updaterArgs{ServerURL: primary.URL})
	if _, err := downloadAndInstallHandler("", string(args)); err == nil || err.Error() != "当前已是最新版本" {
		t.Fatalf("download without releases: %v", err)
	}
}

func TestFetchManifestAcrossPoolReportsExhaustedPool(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead.Close()

	_, _, err := fetchManifestAcrossPool(context.Background(), []string{dead.URL, dead.URL})
	if err == nil || !strings.Contains(err.Error(), "更新服务暂时不可用") {
		t.Fatalf("expected exhausted pool error, got %v", err)
	}
}

func TestVerifyManifestSignature(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manifest := updateManifest{
		Version: "1.2.3", VersionCode: 10203, Notes: "signed release",
		Platforms: map[string]manifestPlatform{
			"darwin-aarch64": {URL: "https://example.com/mincode.dmg", SHA256: strings.Repeat("a", 64), FileSize: 42},
		},
	}
	if err := updateauth.Sign(&manifest, base64.StdEncoding.EncodeToString(privateKey)); err != nil {
		t.Fatal(err)
	}
	previous := ManifestPublicKey
	ManifestPublicKey = base64.StdEncoding.EncodeToString(publicKey)
	t.Cleanup(func() { ManifestPublicKey = previous })
	if err := verifyManifestSignature(manifest); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	tampered := manifest
	tampered.Notes = "tampered"
	if err := verifyManifestSignature(tampered); err == nil || !strings.Contains(err.Error(), "签名验证失败") {
		t.Fatalf("tampered manifest accepted: %v", err)
	}
}

func TestVerifyManifestSignatureRejectsMissingOrInvalidTrust(t *testing.T) {
	previous := ManifestPublicKey
	t.Cleanup(func() { ManifestPublicKey = previous })
	manifest := updateManifest{Signature: base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))}
	ManifestPublicKey = ""
	if err := verifyManifestSignature(manifest); err == nil || !strings.Contains(err.Error(), "公钥未配置") {
		t.Fatalf("missing trust root accepted: %v", err)
	}
	ManifestPublicKey = base64.StdEncoding.EncodeToString([]byte("short"))
	if err := verifyManifestSignature(manifest); err == nil || !strings.Contains(err.Error(), "公钥长度无效") {
		t.Fatalf("invalid trust root accepted: %v", err)
	}
}

func TestUpToDateReportsInstallerSizeWithoutOffering(t *testing.T) {
	packageBody := []byte("current MinCode package")
	server := newUpdateTestServer(t, packageBody, hex.EncodeToString(sumSHA256(packageBody)), int64(len(packageBody)))
	defer server.Close()

	for _, current := range []string{"9.9.9", "10.0.0"} {
		resolved, err := resolveAvailableUpdate(context.Background(), server.URL, current)
		if err != nil {
			t.Fatalf("resolveAvailableUpdate(%s) error = %v", current, err)
		}
		if resolved.Info.Available || resolved.PackageURL != "" || resolved.Info.DownloadSize != int64(len(packageBody)) {
			t.Fatalf("current %s: info=%#v packageURL=%q", current, resolved.Info, resolved.PackageURL)
		}
	}
	resolved, err := resolveAvailableUpdate(context.Background(), server.URL, "9.9.8")
	if err != nil || !resolved.Info.Available || resolved.Info.DownloadSize != int64(len(packageBody)) {
		t.Fatalf("older version: info=%#v err=%v", resolved.Info, err)
	}
}
