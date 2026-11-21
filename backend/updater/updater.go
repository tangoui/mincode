// Package updater implements MinCode's explicit desktop update flow:
// check manifest -> download package -> verify SHA-256 -> open system installer.
// It intentionally does not execute downloaded binaries silently.
package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"golang.org/x/mod/semver"

	"mincode/app/backend/core"
	"mincode/app/backend/misc"
	"mincode/app/backend/updateauth"
)

const (
	manifestPath        = "/api/mincode/updater/manifest"
	maxManifestBytes    = 1024 * 1024
	maxUpdatePackage    = int64(1024 * 1024 * 1024)
	manifestHTTPTimeout = 20 * time.Second
	downloadHTTPTimeout = 20 * time.Minute
)

type manifestPlatform = updateauth.Platform

type updateManifest = updateauth.Manifest

type updateInfo struct {
	Available      bool   `json:"available"`
	Version        string `json:"version"`
	CurrentVersion string `json:"currentVersion"`
	Notes          string `json:"notes"`
	PubDate        string `json:"pubDate,omitempty"`
	Platform       string `json:"platform"`
	DownloadSize   int64  `json:"downloadSize,omitempty"`
}

type downloadResult struct {
	Version  string `json:"version"`
	FileName string `json:"fileName"`
	Opened   bool   `json:"opened"`
}

type updaterArgs struct {
	ServerURL string `json:"serverUrl"`
}

type resolvedUpdate struct {
	Info       updateInfo
	Package    manifestPlatform
	PackageURL string
}

// ManifestPublicKey is injected at build time as a base64-encoded raw Ed25519 public key.
// Production builds deliberately reject update manifests when this trust root is absent.
var ManifestPublicKey string

var openUpdatePackageFn = openUpdatePackage

// errNoActiveRelease means the update service is healthy but has never activated a release.
var errNoActiveRelease = errors.New("update service has no active release")

func init() {
	core.Register("check_update", checkUpdateHandler)
	core.Register("download_and_install", downloadAndInstallHandler)
	core.Register("restart_app", restartAppHandler)
}

func checkUpdateHandler(_, argsJSON string) (string, error) {
	var args updaterArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	resolved, err := resolveAvailableUpdate(context.Background(), args.ServerURL, misc.AppVersion)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(resolved.Info)
}

func downloadAndInstallHandler(_, argsJSON string) (string, error) {
	var args updaterArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	resolved, err := resolveAvailableUpdate(context.Background(), args.ServerURL, misc.AppVersion)
	if err != nil {
		return "", err
	}
	if !resolved.Info.Available {
		return "", fmt.Errorf("当前已是最新版本")
	}

	packagePath, err := downloadAndVerifyPackage(context.Background(), resolved)
	if err != nil {
		return "", err
	}
	if err := openUpdatePackageFn(packagePath); err != nil {
		return "", fmt.Errorf("打开安装包: %w", err)
	}
	return core.EncodeResult(downloadResult{
		Version:  resolved.Info.Version,
		FileName: filepath.Base(packagePath),
		Opened:   true,
	})
}

func restartAppHandler(_, _ string) (string, error) {
	restartCurrentProcess()
	return core.EncodeResult(nil)
}

func resolveAvailableUpdate(ctx context.Context, configuredServerURL, currentVersion string) (resolvedUpdate, error) {
	// 主地址网络不可达时依序尝试备用地址池；显式配置会钉死单一地址。
	serverURL, manifest, err := fetchManifestAcrossPool(ctx, resolveUpdateServers(configuredServerURL))
	if errors.Is(err, errNoActiveRelease) {
		platform, _ := currentPlatform()
		return resolvedUpdate{Info: updateInfo{CurrentVersion: strings.TrimSpace(currentVersion), Platform: platform}}, nil
	}
	if err != nil {
		return resolvedUpdate{}, err
	}
	platform, err := currentPlatform()
	if err != nil {
		return resolvedUpdate{}, err
	}

	info := updateInfo{
		Version:        strings.TrimSpace(manifest.Version),
		CurrentVersion: strings.TrimSpace(currentVersion),
		Notes:          manifest.Notes,
		Platform:       platform,
	}
	if manifest.PubDate != nil {
		info.PubDate = strings.TrimSpace(*manifest.PubDate)
	}
	if info.Version == "" {
		return resolvedUpdate{}, fmt.Errorf("更新清单缺少版本号")
	}
	if info.CurrentVersion == "" {
		return resolvedUpdate{}, fmt.Errorf("当前应用版本号为空")
	}
	comparison, err := compareVersions(info.Version, info.CurrentVersion)
	if err != nil {
		return resolvedUpdate{}, fmt.Errorf("比较更新版本: %w", err)
	}
	pkg, ok := manifest.Platforms[platform]
	if comparison <= 0 {
		// Up to date: still show the size of this platform's installer, but offer nothing to download.
		if ok && validateManifestPackage(pkg) == nil {
			info.DownloadSize = pkg.FileSize
		}
		return resolvedUpdate{Info: info}, nil
	}
	if !ok {
		return resolvedUpdate{}, fmt.Errorf("版本 %s 未提供当前平台 %s 的安装包", info.Version, platform)
	}
	if err := validateManifestPackage(pkg); err != nil {
		return resolvedUpdate{}, fmt.Errorf("版本 %s 的 %s 安装包配置无效: %w", info.Version, platform, err)
	}
	packageURL, err := resolvePackageURL(serverURL, pkg.URL)
	if err != nil {
		return resolvedUpdate{}, err
	}
	info.Available = true
	info.DownloadSize = pkg.FileSize
	return resolvedUpdate{Info: info, Package: pkg, PackageURL: packageURL}, nil
}

func fetchManifest(ctx context.Context, serverURL string) (updateManifest, error) {
	base, err := validatedBaseURL(serverURL)
	if err != nil {
		return updateManifest{}, err
	}
	endpoint := base.ResolveReference(&url.URL{Path: manifestPath})
	client := updateHTTPClient(manifestHTTPTimeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return updateManifest{}, fmt.Errorf("创建更新请求: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return updateManifest{}, &manifestUnavailableError{err: transportError("请求更新清单", err)}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxManifestBytes+1))
	if err != nil {
		return updateManifest{}, &manifestUnavailableError{err: transportError("读取更新清单", err)}
	}
	if len(body) > maxManifestBytes {
		return updateManifest{}, fmt.Errorf("更新清单超过 1MB 限制")
	}
	if resp.StatusCode == http.StatusNotFound && isNoActiveReleaseBody(body) {
		return updateManifest{}, errNoActiveRelease
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := fmt.Errorf("更新服务返回 HTTP %d: %s", resp.StatusCode, responseSnippet(body))
		if resp.StatusCode >= http.StatusInternalServerError {
			return updateManifest{}, &manifestUnavailableError{err: err}
		}
		return updateManifest{}, err
	}
	manifest, err := parseManifest(body)
	if err != nil {
		return updateManifest{}, err
	}
	if err := verifyManifestSignature(manifest); err != nil {
		return updateManifest{}, err
	}
	return manifest, nil
}

func isNoActiveReleaseBody(body []byte) bool {
	var marker struct {
		Code string `json:"code"`
	}
	return json.Unmarshal(body, &marker) == nil && marker.Code == "no_active_release"
}

func parseManifest(body []byte) (updateManifest, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return updateManifest{}, fmt.Errorf("更新清单不是有效 JSON: %w", err)
	}

	payload := body
	if rawCode, ok := root["code"]; ok {
		var code int
		if err := json.Unmarshal(rawCode, &code); err != nil {
			return updateManifest{}, fmt.Errorf("更新清单响应 code 必须是整数")
		}
		message := ""
		_ = json.Unmarshal(root["msg"], &message)
		if code < 200 || code >= 300 {
			return updateManifest{}, fmt.Errorf("更新服务拒绝请求（代码 %d）: %s", code, fallbackMessage(message))
		}
		data, ok := root["data"]
		if !ok || len(data) == 0 || string(data) == "null" {
			return updateManifest{}, fmt.Errorf("更新清单响应缺少 data")
		}
		payload = data
	}

	var manifest updateManifest
	if err := json.Unmarshal(payload, &manifest); err != nil {
		return updateManifest{}, fmt.Errorf("更新清单字段解析失败: %w", err)
	}
	if manifest.Platforms == nil {
		manifest.Platforms = map[string]manifestPlatform{}
	}
	return manifest, nil
}

func downloadAndVerifyPackage(ctx context.Context, update resolvedUpdate) (string, error) {
	packageURL, err := url.Parse(update.PackageURL)
	if err != nil {
		return "", fmt.Errorf("解析安装包地址: %w", err)
	}
	if err := validateRemoteURL(packageURL); err != nil {
		return "", err
	}
	if err := validatePackageExtension(update.Info.Platform, packageURL.Path); err != nil {
		return "", err
	}

	cacheRoot, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("获取更新缓存目录: %w", err)
	}
	dir := filepath.Join(cacheRoot, "MinCode", "updates", sanitizePathPart(update.Info.Version))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("创建更新缓存目录: %w", err)
	}
	filename := filepath.Base(packageURL.Path)
	if filename == "." || filename == "/" || filename == "" {
		return "", fmt.Errorf("安装包地址缺少文件名")
	}
	targetPath := filepath.Join(dir, filename)
	if ok, _ := fileMatches(targetPath, update.Package.SHA256, update.Package.FileSize); ok {
		return targetPath, nil
	}

	tempFile, err := os.CreateTemp(dir, ".mincode-update-*")
	if err != nil {
		return "", fmt.Errorf("创建临时安装包: %w", err)
	}
	tempPath := tempFile.Name()
	keepTemp := false
	defer func() {
		_ = tempFile.Close()
		if !keepTemp {
			_ = os.Remove(tempPath)
		}
	}()

	client := updateHTTPClient(downloadHTTPTimeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, packageURL.String(), nil)
	if err != nil {
		return "", fmt.Errorf("创建安装包下载请求: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", transportError("下载安装包", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("安装包下载返回 HTTP %d: %s", resp.StatusCode, responseSnippet(body))
	}
	if resp.ContentLength > maxUpdatePackage {
		return "", fmt.Errorf("安装包超过 1GB 限制")
	}
	if update.Package.FileSize > 0 && resp.ContentLength > 0 && resp.ContentLength != update.Package.FileSize {
		return "", fmt.Errorf("安装包大小与清单不一致")
	}

	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(tempFile, hasher), io.LimitReader(resp.Body, maxUpdatePackage+1))
	if err != nil {
		return "", transportError("写入安装包", err)
	}
	if written > maxUpdatePackage {
		return "", fmt.Errorf("安装包超过 1GB 限制")
	}
	if written != update.Package.FileSize {
		return "", fmt.Errorf("安装包大小校验失败：期望 %d 字节，实际 %d 字节", update.Package.FileSize, written)
	}
	actualHash := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(actualHash, update.Package.SHA256) {
		return "", fmt.Errorf("安装包 SHA-256 校验失败")
	}
	if err := tempFile.Sync(); err != nil {
		return "", fmt.Errorf("写入安装包缓存: %w", err)
	}
	if err := tempFile.Close(); err != nil {
		return "", fmt.Errorf("关闭安装包缓存: %w", err)
	}
	_ = os.Remove(targetPath)
	if err := os.Rename(tempPath, targetPath); err != nil {
		return "", fmt.Errorf("保存安装包: %w", err)
	}
	keepTemp = true
	return targetPath, nil
}

func updateHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("更新下载重定向次数过多")
			}
			return validateRemoteURL(req.URL)
		},
	}
}

// transportError 把 Go 网络层的原始错误（含本机 IP、端口、syscall 细节）转换成
// 面向用户的简短说明。原始错误只写入日志，不暴露给 UI。
func transportError(action string, err error) error {
	slog.Warn("updater transport error", "action", action, "error", err)
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Timeout() {
		return fmt.Errorf("%s超时，请检查网络后重试", action)
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return fmt.Errorf("无法解析更新服务地址，请检查网络或 DNS 设置")
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		switch opErr.Op {
		case "dial":
			return fmt.Errorf("无法连接更新服务，请检查网络后重试")
		case "read", "write":
			return fmt.Errorf("与更新服务的连接中断，请检查网络后重试")
		}
	}
	text := err.Error()
	switch {
	case strings.Contains(text, "context deadline exceeded"), strings.Contains(text, "Client.Timeout"):
		return fmt.Errorf("%s超时，请检查网络后重试", action)
	case strings.Contains(text, "connection reset"), strings.Contains(text, "EOF"), strings.Contains(text, "broken pipe"):
		return fmt.Errorf("与更新服务的连接中断，请检查网络后重试")
	case strings.Contains(text, "no such host"):
		return fmt.Errorf("无法解析更新服务地址，请检查网络或 DNS 设置")
	case strings.Contains(text, "connection refused"):
		return fmt.Errorf("无法连接更新服务，请稍后重试")
	case strings.Contains(text, "certificate"), strings.Contains(text, "tls:"):
		return fmt.Errorf("与更新服务的安全连接异常，请检查系统时间或代理设置")
	}
	return fmt.Errorf("网络异常，暂时无法访问更新服务")
}

func verifyManifestSignature(manifest updateManifest) error {
	publicKeyValue := strings.TrimSpace(ManifestPublicKey)
	if publicKeyValue == "" {
		if requireManifestSignature || strings.TrimSpace(manifest.Signature) != "" {
			return fmt.Errorf("更新签名公钥未配置")
		}
		return nil
	}
	return updateauth.Verify(manifest, publicKeyValue)
}

func validatedBaseURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("更新服务地址无效")
	}
	if err := validateRemoteURL(parsed); err != nil {
		return nil, err
	}
	return parsed, nil
}

func resolvePackageURL(serverURL, packageReference string) (string, error) {
	base, err := validatedBaseURL(serverURL)
	if err != nil {
		return "", err
	}
	reference, err := url.Parse(strings.TrimSpace(packageReference))
	if err != nil {
		return "", fmt.Errorf("安装包地址无效: %w", err)
	}
	resolved := base.ResolveReference(reference)
	if err := validateRemoteURL(resolved); err != nil {
		return "", err
	}
	return resolved.String(), nil
}

func validateRemoteURL(value *url.URL) error {
	if value == nil || value.Host == "" {
		return fmt.Errorf("更新地址缺少主机名")
	}
	switch strings.ToLower(value.Scheme) {
	case "https":
		return nil
	case "http":
		host := strings.TrimSpace(value.Hostname())
		ip := net.ParseIP(host)
		if strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback()) {
			return nil
		}
		return fmt.Errorf("生产更新地址必须使用 HTTPS；HTTP 仅允许本机回环地址")
	default:
		return fmt.Errorf("更新地址仅支持 HTTP/HTTPS")
	}
}

func validateManifestPackage(pkg manifestPlatform) error {
	if strings.TrimSpace(pkg.URL) == "" {
		return fmt.Errorf("缺少下载地址")
	}
	sha := strings.TrimSpace(pkg.SHA256)
	decoded, err := hex.DecodeString(sha)
	if err != nil || len(decoded) != sha256.Size {
		return fmt.Errorf("SHA-256 必须是 64 位十六进制字符串")
	}
	if pkg.FileSize <= 0 || pkg.FileSize > maxUpdatePackage {
		return fmt.Errorf("文件大小必须在 1 字节到 1GB 之间")
	}
	return nil
}

func currentPlatform() (string, error) {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "windows/amd64":
		return "windows-x86_64", nil
	case "darwin/amd64":
		return "darwin-x86_64", nil
	case "darwin/arm64":
		return "darwin-aarch64", nil
	case "linux/amd64":
		return "linux-x86_64", nil
	default:
		return "", fmt.Errorf("当前平台暂不支持自动升级: %s/%s", runtime.GOOS, runtime.GOARCH)
	}
}

func validatePackageExtension(platform, path string) error {
	ext := strings.ToLower(filepath.Ext(path))
	allowed := map[string]map[string]bool{
		"windows-x86_64": {".exe": true, ".msi": true},
		"darwin-x86_64":  {".dmg": true, ".pkg": true, ".zip": true},
		"darwin-aarch64": {".dmg": true, ".pkg": true, ".zip": true},
		"linux-x86_64":   {".appimage": true, ".deb": true, ".rpm": true, ".zip": true},
	}
	if !allowed[platform][ext] {
		return fmt.Errorf("当前平台不支持该安装包格式: %s", ext)
	}
	return nil
}

// manifestUnavailableError 标记主地址不可达（网络故障或服务端 5xx），
// 此类错误可以切换到地址池中的下一个地址重试。
type manifestUnavailableError struct{ err error }

func (e *manifestUnavailableError) Error() string { return e.err.Error() }
func (e *manifestUnavailableError) Unwrap() error { return e.err }

func isManifestUnavailable(err error) bool {
	var unavailable *manifestUnavailableError
	return errors.As(err, &unavailable)
}

func fetchManifestAcrossPool(ctx context.Context, servers []string) (string, updateManifest, error) {
	var lastErr error
	for _, server := range servers {
		manifest, err := fetchManifest(ctx, server)
		if err == nil {
			return server, manifest, nil
		}
		if !isManifestUnavailable(err) {
			return "", updateManifest{}, err
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("更新服务地址池为空")
	}
	return "", updateManifest{}, fmt.Errorf("更新服务暂时不可用: %w", lastErr)
}

func resolveUpdateServers(configured string) []string {
	// MINCODE_LICENSE_SERVER_URL remains a compatibility fallback for one-server
	// deployments. Prefer MINCODE_UPDATE_SERVER_URL or the shared API base URL.
	return core.ResolveMinCodeAPIServers(
		configured,
		"MINCODE_UPDATE_SERVER_URL",
		"MINCODE_LICENSE_SERVER_URL",
	)
}

// Older local builds used a fourth numeric component; retain its precedence
// without treating malformed versions as zero or build metadata as prerelease.
var updateVersionPattern = regexp.MustCompile(`^v?((?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*))(?:\.(0|[1-9][0-9]*))?([-+].*)?$`)

type updateVersion struct {
	semver string
	base   string
	fourth string
}

func parseUpdateVersion(raw string) (updateVersion, error) {
	match := updateVersionPattern.FindStringSubmatch(strings.TrimSpace(raw))
	if match == nil {
		return updateVersion{}, fmt.Errorf("无效的版本号")
	}
	version := "v" + match[1] + match[3]
	if !semver.IsValid(version) {
		return updateVersion{}, fmt.Errorf("无效的版本号")
	}
	fourth := match[2]
	if fourth == "" {
		fourth = "0"
	}
	return updateVersion{semver: version, base: "v" + match[1], fourth: fourth}, nil
}

func compareVersions(left, right string) (int, error) {
	leftVersion, err := parseUpdateVersion(left)
	if err != nil {
		return 0, fmt.Errorf("远程版本: %w", err)
	}
	rightVersion, err := parseUpdateVersion(right)
	if err != nil {
		return 0, fmt.Errorf("当前版本: %w", err)
	}
	if result := semver.Compare(leftVersion.base, rightVersion.base); result != 0 {
		return result, nil
	}
	if result := semver.Compare("v0.0."+leftVersion.fourth, "v0.0."+rightVersion.fourth); result != 0 {
		return result, nil
	}
	return semver.Compare(leftVersion.semver, rightVersion.semver), nil
}

func fileMatches(path, expectedHash string, expectedSize int64) (bool, error) {
	stat, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if stat.Size() != expectedSize {
		return false, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return false, err
	}
	return strings.EqualFold(hex.EncodeToString(hasher.Sum(nil)), expectedHash), nil
}

func sanitizePathPart(value string) string {
	var builder strings.Builder
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '.' || char == '-' || char == '_' {
			builder.WriteRune(char)
		}
	}
	if builder.Len() == 0 {
		return "unknown"
	}
	return builder.String()
}

func responseSnippet(body []byte) string {
	value := strings.TrimSpace(string(body))
	if len(value) > 200 {
		value = value[:200] + "…"
	}
	if value == "" {
		return "空响应"
	}
	return value
}

func fallbackMessage(message string) string {
	if strings.TrimSpace(message) == "" {
		return "服务端未提供错误说明"
	}
	return message
}
