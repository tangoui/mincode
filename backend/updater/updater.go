// Package updater exposes Tango's signed desktop updater to the settings UI.
package updater

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"mincode/app/backend/core"
	"mincode/app/backend/misc"

	tangoupdater "github.com/tangoui/tango/service/updater"
)

const (
	ManifestURL = "https://github.com/tangoui/mincode/releases/latest/download/latest.json"
	AppID       = "com.tangoui.mincode"
	progress    = "app-update:progress"
)

// ManifestPublicKey is injected with -ldflags at packaging time.
var ManifestPublicKey string

var (
	manifestURL = ManifestURL
	noRelease   = errors.New("update manifest not found")
	updateMu    sync.Mutex
	readyUpdate string
	quit        func()
)

type updateInfo struct {
	Available      bool   `json:"available"`
	Version        string `json:"version"`
	CurrentVersion string `json:"currentVersion"`
	Notes          string `json:"notes"`
	PubDate        string `json:"pubDate,omitempty"`
	Platform       string `json:"platform"`
	DownloadSize   int64  `json:"downloadSize,omitempty"`
}

type installResult struct {
	Version string `json:"version"`
	Ready   bool   `json:"ready"`
}

func init() {
	core.RegisterCtx("check_update", checkUpdateHandler)
	core.RegisterCtx("download_and_install", downloadAndInstallHandler)
	core.Register("restart_app", restartAppHandler)
}

func newUpdater() (*tangoupdater.Updater, error) {
	dataDir := core.UserDataDir()
	if !filepath.IsAbs(dataDir) {
		return nil, errors.New("无法确定更新数据目录")
	}
	return &tangoupdater.Updater{
		ManifestURL: manifestURL,
		PublicKey:   strings.TrimSpace(ManifestPublicKey),
		Version:     misc.AppVersion,
		AppID:       AppID,
		StateDir:    filepath.Join(dataDir, AppID, "updates"),
		Platform:    runtime.GOOS + "-" + runtime.GOARCH,
	}, nil
}

// Confirm records a successful desktop startup and wires the quit that lets
// Tango's update helper replace and relaunch the running installation.
func Confirm(quitApp func()) {
	updateMu.Lock()
	defer updateMu.Unlock()
	quit = quitApp
	u, err := newUpdater()
	if err == nil {
		var outcome tangoupdater.Outcome
		outcome, err = u.Confirm()
		if outcome.Status == tangoupdater.StatusRolledBack {
			log.Printf("update to %s rolled back: %s", outcome.To, outcome.Reason)
		}
	}
	if err != nil {
		log.Printf("confirm update: %v", err)
	}
}

func checkUpdateHandler(ctx context.Context, _, _ string) (string, error) {
	updateMu.Lock()
	defer updateMu.Unlock()
	u, err := newUpdater()
	if err != nil {
		return "", err
	}
	release, err := check(ctx, u)
	if err != nil {
		return "", err
	}
	info := updateInfo{CurrentVersion: u.Version, Platform: u.Platform}
	if release != nil {
		info.Available = true
		info.Version = release.Version
		info.Notes = release.Notes
		info.DownloadSize = release.Full.Size
		if !release.PubDate.IsZero() {
			info.PubDate = release.PubDate.Format(time.RFC3339)
		}
	}
	return core.EncodeResult(info)
}

func check(ctx context.Context, u *tangoupdater.Updater) (*tangoupdater.Release, error) {
	if u.PublicKey == "" {
		return nil, errors.New("此构建未配置更新公钥")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	base, err := url.Parse(u.ManifestURL)
	if err != nil {
		return nil, errors.New("更新清单地址无效")
	}
	transport := &manifestTransport{base: base}
	client := &http.Client{Transport: transport}
	u.Client = client
	defer func() { u.Client = nil }()
	core.Emit(progress, tangoupdater.Event{Kind: tangoupdater.EventChecking})
	release, err := u.Check(ctx)
	if errors.Is(err, noRelease) {
		err = nil
	}
	if err != nil {
		log.Printf("check update: %v", err)
		message := "无法验证更新清单，请稍后重试"
		var requestErr *url.Error
		if errors.As(err, &requestErr) || errors.Is(err, context.DeadlineExceeded) {
			message = "无法检查更新，请检查网络连接后重试"
		}
		core.Emit(progress, tangoupdater.Event{Kind: tangoupdater.EventError, Error: message})
		return nil, errors.New(message)
	}
	if release == nil {
		core.Emit(progress, tangoupdater.Event{Kind: tangoupdater.EventUpToDate})
		return nil, nil
	}
	if transport.release != nil {
		rebaseAsset(&release.Full, base, transport.release)
		if release.Patch != nil {
			rebaseAsset(release.Patch, base, transport.release)
		}
	}
	core.Emit(progress, tangoupdater.Event{Kind: tangoupdater.EventAvailable, Release: release})
	return release, nil
}

type manifestTransport struct {
	base    *url.URL
	release *url.URL
}

func (t *manifestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, noRelease
	}
	// GitHub redirects latest/download to the tag-specific asset URL, then
	// redirects to a temporary CDN URL. Keep the stable, same-repo tag URL.
	if project, ok := strings.CutSuffix(t.base.Path, "/releases/latest/download/latest.json"); ok &&
		t.base.Scheme == req.URL.Scheme && t.base.Host == req.URL.Host {
		prefix := project + "/releases/download/"
		if tagAndFile, ok := strings.CutPrefix(req.URL.Path, prefix); ok {
			if tag, file, ok := strings.Cut(tagAndFile, "/"); ok && tag != "" && file == "latest.json" {
				t.release = req.URL
			}
		}
	}
	return resp, nil
}

func rebaseAsset(asset *tangoupdater.File, base, release *url.URL) {
	file, err := url.Parse(asset.URL)
	if err != nil || file.Scheme != base.Scheme || file.Host != base.Host {
		return
	}
	relative, ok := strings.CutPrefix(file.Path, path.Dir(base.Path)+"/")
	if !ok || relative == "" {
		return
	}
	file.Path = path.Join(path.Dir(release.Path), relative)
	file.RawPath = ""
	asset.URL = file.String()
}

func downloadAndInstallHandler(ctx context.Context, _, argsJSON string) (string, error) {
	var args struct {
		Version string `json:"version"`
	}
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	updateMu.Lock()
	defer updateMu.Unlock()
	if readyUpdate != "" {
		return core.EncodeResult(installResult{Version: readyUpdate, Ready: true})
	}
	u, err := newUpdater()
	if err != nil {
		return "", err
	}
	release, err := check(ctx, u)
	if err != nil {
		return "", err
	}
	if release == nil || (args.Version != "" && args.Version != release.Version) {
		return "", errors.New("可用版本已变化，请重新检查更新")
	}
	u.OnEvent = func(event tangoupdater.Event) {
		// The response carries a user-facing error. Keep path and network
		// details out of the frontend progress stream.
		event.Error = ""
		core.Emit(progress, event)
	}
	dir, err := os.MkdirTemp(u.StateDir, "download-")
	if err != nil {
		log.Printf("prepare update download: %v", err)
		return "", errors.New("无法创建更新下载目录")
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	payload, err := u.Download(ctx, release, dir)
	if err != nil {
		log.Printf("download update: %v", err)
		return "", errors.New("更新下载或校验失败，请重试")
	}
	if err := u.Apply(payload); err != nil {
		log.Printf("stage update: %v", err)
		switch {
		case errors.Is(err, tangoupdater.ErrUnsupported):
			return "", errors.New("当前安装方式不支持应用内更新")
		case errors.Is(err, tangoupdater.ErrNotWritable):
			return "", errors.New("应用目录不可写，无法安装更新")
		default:
			return "", errors.New("无法准备更新，请重试")
		}
	}
	readyUpdate = release.Version
	return core.EncodeResult(installResult{Version: release.Version, Ready: true})
}

func restartAppHandler(_, _ string) (string, error) {
	updateMu.Lock()
	defer updateMu.Unlock()
	if readyUpdate == "" || quit == nil {
		return "", errors.New("没有等待重启的更新")
	}
	go quit()
	return core.EncodeResult(nil)
}
