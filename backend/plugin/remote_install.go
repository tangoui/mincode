package plugin

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mincode/app/backend/core"
)

func requirePluginZIP(source string) error {
	info, err := os.Lstat(strings.TrimSpace(source))
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || !isPluginPackagePath(strings.TrimSpace(source)) {
		return errors.New("only regular .min/.zip plugin packages are accepted; directory installation is disabled")
	}
	return nil
}

func validatePluginDownloadURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return errors.New("plugin download requires an HTTPS URL without credentials or fragment")
	}
	return nil
}

// Resolve and dial the same public address to prevent DNS rebinding and local-network access.
func dialPluginDownload(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, errors.New("download host has no addresses")
	}
	for _, addr := range ips {
		ip := addr.IP
		if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
			return nil, errors.New("plugin downloads cannot access local or private networks")
		}
	}
	return (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
}

func downloadPluginZIP(ctx context.Context, raw string) (string, func(), error) {
	if err := validatePluginDownloadURL(raw); err != nil {
		return "", func() {}, err
	}
	transport := &http.Transport{DialContext: dialPluginDownload, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, ResponseHeaderTimeout: 20 * time.Second}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many plugin download redirects")
		}
		return validatePluginDownloadURL(req.URL.String())
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return "", func() {}, err
	}
	response, err := client.Do(req)
	if err != nil {
		return "", func() {}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", func() {}, errors.New("plugin download did not return HTTP 200")
	}
	if response.ContentLength > pluginInstallMaxBytes {
		return "", func() {}, errors.New("plugin download exceeds ZIP size limit")
	}
	dir, err := os.MkdirTemp("", "mincode-plugin-download-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	path := filepath.Join(dir, "plugin.min")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		cleanup()
		return "", func() {}, err
	}
	n, copyErr := io.Copy(file, io.LimitReader(response.Body, pluginInstallMaxBytes+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || n > pluginInstallMaxBytes {
		cleanup()
		return "", func() {}, errors.New("plugin download incomplete or exceeds ZIP size limit")
	}
	return path, cleanup, nil
}

func init() {
	core.Register("plugin_host_remote_preview", func(_, args string) (string, error) {
		var request struct {
			URL string `json:"url"`
		}
		if err := core.DecodeArgs(args, &request); err != nil {
			return "", err
		}
		path, cleanup, err := downloadPluginZIP(context.Background(), strings.TrimSpace(request.URL))
		if err != nil {
			return "", err
		}
		preview, err := host().PreviewInstall(path)
		if err != nil {
			cleanup()
			return "", err
		}
		// Keep the exact previewed archive for confirmation, never re-download on install.
		time.AfterFunc(30*time.Minute, cleanup)
		return core.EncodeResult(preview)
	})
}
