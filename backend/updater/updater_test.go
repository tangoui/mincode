package updater

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mincode/app/backend/core"

	tangoupdater "github.com/tangoui/tango/service/updater"
)

func useManifest(t *testing.T, url, key string) {
	t.Helper()
	previousURL, previousKey := manifestURL, ManifestPublicKey
	manifestURL, ManifestPublicKey = url, key
	t.Setenv("MINCODE_DATA_DIR", t.TempDir())
	t.Cleanup(func() { manifestURL, ManifestPublicKey = previousURL, previousKey })
}

func updateKey(t *testing.T) (string, string) {
	t.Helper()
	pub, priv, err := tangoupdater.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func TestCheckUpdateReportsNewerRelease(t *testing.T) {
	pub, priv := updateKey(t)
	platform := runtime.GOOS + "-" + runtime.GOARCH
	now := time.Now().UTC().Truncate(time.Second)
	signed, err := tangoupdater.SignManifest(priv, &tangoupdater.Manifest{
		App: AppID, Version: "99.0.0", PubDate: now, Expires: now.Add(time.Hour), Notes: "Sample release",
		Platforms: map[string]tangoupdater.Asset{
			platform: {
				Format: tangoupdater.FormatExe,
				File: tangoupdater.File{
					URL: "mincode.exe", Size: 42, SHA256: strings.Repeat("a", 64),
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/latest/download/latest.json":
			http.Redirect(w, r, "/releases/download/v99.0.0/latest.json", http.StatusFound)
		case "/releases/download/v99.0.0/latest.json":
			http.Redirect(w, r, "/cdn/latest.json", http.StatusFound)
		case "/cdn/latest.json":
			_, _ = w.Write(signed)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	useManifest(t, server.URL+"/releases/latest/download/latest.json", pub)

	var eventReleaseURL string
	core.SetEmitter(func(name string, data any) {
		if event, ok := data.(tangoupdater.Event); name == "app-update:progress" && ok && event.Kind == tangoupdater.EventAvailable {
			eventReleaseURL = event.Release.Full.URL
		}
	})
	t.Cleanup(func() { core.SetEmitter(nil) })

	raw, err := core.Dispatch("check_update", "{}")
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Available    bool
		Version      string
		Notes        string
		PubDate      time.Time
		Platform     string
		DownloadSize int64
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Available || result.Version != "99.0.0" || result.Notes != "Sample release" ||
		result.Platform != platform || result.DownloadSize != 42 || !result.PubDate.Equal(now) {
		t.Fatalf("unexpected check result: %s", raw)
	}
	if want := server.URL + "/releases/download/v99.0.0/mincode.exe"; eventReleaseURL != want {
		t.Fatalf("asset URL = %q, want %q", eventReleaseURL, want)
	}
}

func TestCheckUpdateNoRelease404IsUpToDate(t *testing.T) {
	pub, _ := updateKey(t)
	server := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(server.Close)
	useManifest(t, server.URL+"/latest.json", pub)

	raw, err := core.Dispatch("check_update", "{}")
	if err != nil || !strings.Contains(raw, "\"available\":false") {
		t.Fatalf("404 check: %q, %v", raw, err)
	}
}

func TestAppImageChecksForUpdatesWithoutDownloading(t *testing.T) {
	t.Setenv("APPIMAGE", "/tmp/MinCode.AppImage")
	pub, priv := updateKey(t)
	platform := runtime.GOOS + "-" + runtime.GOARCH
	signed, err := tangoupdater.SignManifest(priv, &tangoupdater.Manifest{
		App: AppID, Version: "99.0.0", PubDate: time.Now().UTC(),
		Platforms: map[string]tangoupdater.Asset{
			platform: {
				Format: tangoupdater.FormatDir,
				File: tangoupdater.File{
					URL: "mincode.tar.gz", Size: 42, SHA256: strings.Repeat("a", 64),
				},
				TreeSHA256: strings.Repeat("b", 64),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/latest.json" {
			_, _ = w.Write(signed)
			return
		}
		downloads.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	useManifest(t, server.URL+"/latest.json", pub)

	raw, err := core.Dispatch("check_update", "{}")
	if err != nil {
		t.Fatal(err)
	}
	var check struct {
		Available    bool
		Version      string
		ReleaseURL   string
		DownloadSize int64
	}
	if err := json.Unmarshal([]byte(raw), &check); err != nil {
		t.Fatal(err)
	}
	if !check.Available || check.Version != "99.0.0" ||
		check.ReleaseURL != "https://github.com/tangoui/mincode/releases/latest" || check.DownloadSize != 0 {
		t.Errorf("unexpected check result: %s", raw)
	}

	raw, err = core.Dispatch("download_and_install", `{"version":"99.0.0"}`)
	if err != nil {
		t.Errorf("install error: %v", err)
	}
	var install struct {
		Version    string
		Ready      bool
		ReleaseURL string
	}
	if err := json.Unmarshal([]byte(raw), &install); err != nil {
		t.Errorf("decode install result: %v", err)
	}
	if install.Ready || install.Version != "99.0.0" || install.ReleaseURL != check.ReleaseURL ||
		!strings.Contains(raw, "请从 GitHub Releases 下载新版安装包") {
		t.Errorf("unexpected install result: %s", raw)
	}
	if downloads.Load() != 0 {
		t.Errorf("requested %d update payloads", downloads.Load())
	}
}

func TestSystemPackageOwnsExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("package-manager test fixtures use shell scripts")
	}
	for _, tt := range []struct {
		name, exe, manager, args string
		want                     bool
	}{
		{"deb", "/usr/bin/MinCode", "dpkg-query", "-S -- /usr/bin/MinCode", true},
		{"rpm", "/usr/bin/MinCode", "rpm", "-qf -- /usr/bin/MinCode", true},
		{"unowned system path", "/usr/bin/MinCode", "", "", false},
		{"extracted tarball", "/home/user/MinCode/MinCode", "rpm", "-qf -- /home/user/MinCode/MinCode", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			bin := t.TempDir()
			t.Setenv("PATH", bin)
			if tt.manager != "" {
				script := "#!/bin/sh\n[ \"$*\" = '" + tt.args + "' ]\n"
				if err := os.WriteFile(filepath.Join(bin, tt.manager), []byte(script), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if got := systemPackageOwnsExecutable(t.Context(), tt.exe); got != tt.want {
				t.Fatalf("systemPackageOwnsExecutable(%q) = %t, want %t", tt.exe, got, tt.want)
			}
		})
	}
}

func TestCheckUpdateOfflineReturnsFriendlyError(t *testing.T) {
	pub, _ := updateKey(t)
	server := httptest.NewServer(http.NotFoundHandler())
	server.Close()
	useManifest(t, server.URL+"/latest.json", pub)

	_, err := core.Dispatch("check_update", "{}")
	if err == nil || err.Error() != "无法检查更新，请检查网络连接后重试" {
		t.Fatalf("offline check: %v", err)
	}
}

func TestCheckUpdateWithoutPublicKeyIsDisabled(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		fmt.Fprint(w, "{}")
	}))
	t.Cleanup(server.Close)
	useManifest(t, server.URL+"/latest.json", "")

	_, err := core.Dispatch("check_update", "{}")
	if err == nil || err.Error() != "此构建未配置更新公钥" || requests.Load() != 0 {
		t.Fatalf("unconfigured check: %v, requests = %d", err, requests.Load())
	}
}
