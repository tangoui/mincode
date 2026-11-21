package market

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"time"

	"golang.org/x/mod/semver"

	"mincode/app/backend/cloudcompanion"
	"mincode/app/backend/core"
	"mincode/app/backend/misc"
	"mincode/app/backend/plugin"
)

const (
	releasesPath        = "/api/mincode/marketplace/releases"
	maxArtifactBytes    = 64 << 20
	previewArchiveTTL   = 30 * time.Minute
	maxReleasesResponse = 1 << 20
)

var sha256Pattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type releaseTarget struct {
	ArtifactID int64  `json:"artifact_id"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	Format     string `json:"format"`
	SizeBytes  int64  `json:"size_bytes"`
	SHA256     string `json:"sha256"`
}

type release struct {
	Version                 string          `json:"version"`
	ManifestAPI             int             `json:"manifest_api"`
	RuntimeProtocol         int             `json:"runtime_protocol"`
	MinHostVersion          string          `json:"min_host_version"`
	MaxHostVersionExclusive string          `json:"max_host_version_exclusive"`
	Targets                 []releaseTarget `json:"targets"`
}

// InstallPreview pairs the host's read-only package preflight with the release
// it was downloaded from. SourcePath inside Preview is the verified archive.
type InstallPreview struct {
	Preview       plugin.InstallPreview `json:"preview"`
	Version       string                `json:"version"`
	SHA256        string                `json:"sha256"`
	VersionChange string                `json:"versionChange"`
	// RequiresPaidHost mirrors the cloud product flag; enabling then needs a paid host plan.
	RequiresPaidHost bool `json:"requiresPaidHost"`
}

// hostPlanPaid is a variable so tests can stub the cloud quota lookup.
var hostPlanPaid = func() bool { return plugin.HostPlanPaid() }

// recordPaidHostRequirement is a variable so tests do not need the plugin KV store.
var recordPaidHostRequirement = plugin.RecordMarketPaidHostRequirement

type releaseList struct {
	Releases         []release
	RequiresPaidHost bool
}

func init() {
	core.Register("market_plugin_install_preview", installPreviewHandler)
}

func installPreviewHandler(_, argsJSON string) (string, error) {
	var args struct {
		ProductKey string `json:"productKey"`
	}
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if !pluginKeyPattern.MatchString(args.ProductKey) {
		return "", errors.New("market install: invalid plugin ID")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	base, err := serverURL()
	if err != nil {
		return "", fmt.Errorf("market install: %w", err)
	}
	list, err := fetchReleases(ctx, base, args.ProductKey)
	if err != nil {
		return "", fmt.Errorf("market install: %w", err)
	}
	if list.RequiresPaidHost && !hostPlanPaid() {
		return "", fmt.Errorf("market install: %w", plugin.ErrPaidHostRequired)
	}
	selected, target, err := selectRelease(list.Releases, runtime.GOOS, runtime.GOARCH, misc.AppVersion)
	if err != nil {
		return "", fmt.Errorf("market install: %w", err)
	}
	path, cleanup, err := downloadVerified(ctx, base.String(), target)
	if err != nil {
		return "", fmt.Errorf("market install: %w", err)
	}
	result, err := previewArchive(path, args.ProductKey, selected, target)
	if err != nil {
		cleanup()
		return "", fmt.Errorf("market install: %w", err)
	}
	// Desktop enforcement is advisory (sideloading bypasses it); the cloud download gate is authoritative.
	if err := recordPaidHostRequirement(args.ProductKey, list.RequiresPaidHost); err != nil {
		cleanup()
		return "", fmt.Errorf("market install: %w", err)
	}
	result.RequiresPaidHost = list.RequiresPaidHost
	// Keep the exact verified archive for the confirmation step; it is never re-downloaded.
	time.AfterFunc(previewArchiveTTL, cleanup)
	return core.EncodeResult(result)
}

func fetchReleases(ctx context.Context, base *url.URL, productKey string) (releaseList, error) {
	endpoint := *base
	endpoint.Path = releasesPath
	query := url.Values{"plugin_key": {productKey}}
	// host_version tells the cloud which MinCode is asking; it uses the same rule as the catalog request.
	if validHostVersion(misc.AppVersion) {
		query.Set("host_version", misc.AppVersion)
	}
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return releaseList{}, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := marketHTTPClient.Do(req)
	if err != nil {
		return releaseList{}, fmt.Errorf("release list request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return releaseList{}, fmt.Errorf("release list HTTP status %d", resp.StatusCode)
	}
	body, err := core.ReadAllLimited(resp.Body, maxReleasesResponse)
	if err != nil {
		return releaseList{}, err
	}
	var envelope struct {
		Code *int `json:"code"`
		Data struct {
			PluginKey string `json:"plugin_key"`
			Product   struct {
				RequiresPaidHost bool `json:"requires_paid_host"`
			} `json:"product"`
			Releases []release `json:"releases"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Code == nil || *envelope.Code != 1 {
		return releaseList{}, errors.New("invalid release list response")
	}
	if envelope.Data.PluginKey != productKey || len(envelope.Data.Releases) > 50 {
		return releaseList{}, errors.New("release list does not match the requested plugin")
	}
	return releaseList{Releases: envelope.Data.Releases, RequiresPaidHost: envelope.Data.Product.RequiresPaidHost}, nil
}

// selectRelease picks the highest published version that ships an artifact for this platform and
// supports hostVersion. When releases exist for the platform but none supports this MinCode, the error
// says which MinCode version is needed instead of failing later in the package preflight.
func selectRelease(releases []release, goos, goarch, hostVersion string) (release, releaseTarget, error) {
	var best release
	var bestTarget releaseTarget
	found, forPlatform := false, false
	lowestMin, highestMax := "", ""
	for _, candidate := range releases {
		if len(candidate.Version) > 64 || !versionPattern.MatchString(candidate.Version) {
			return release{}, releaseTarget{}, errors.New("release list contains an invalid version")
		}
		for _, bound := range []string{candidate.MinHostVersion, candidate.MaxHostVersionExclusive} {
			if bound != "" && !validHostVersion(bound) {
				return release{}, releaseTarget{}, errors.New("release list contains an invalid MinCode version range")
			}
		}
		for _, target := range candidate.Targets {
			if target.OS != goos || target.Arch != goarch {
				continue
			}
			if target.ArtifactID < 1 || target.Format != "zip" || target.SizeBytes < 1 || target.SizeBytes > maxArtifactBytes || !sha256Pattern.MatchString(target.SHA256) {
				return release{}, releaseTarget{}, errors.New("release list contains invalid artifact metadata")
			}
			forPlatform = true
			switch hostCompatibility(hostVersion, candidate.MinHostVersion, candidate.MaxHostVersionExclusive) {
			case "host_too_old":
				if lowestMin == "" || semver.Compare("v"+candidate.MinHostVersion, "v"+lowestMin) < 0 {
					lowestMin = candidate.MinHostVersion
				}
				continue
			case "host_too_new":
				if highestMax == "" || semver.Compare("v"+candidate.MaxHostVersionExclusive, "v"+highestMax) > 0 {
					highestMax = candidate.MaxHostVersionExclusive
				}
				continue
			}
			if !found || semver.Compare("v"+candidate.Version, "v"+best.Version) > 0 {
				best, bestTarget, found = candidate, target, true
			}
		}
	}
	if !found {
		switch {
		case len(releases) == 0:
			return release{}, releaseTarget{}, errors.New("this plugin has no published release yet")
		case !forPlatform:
			return release{}, releaseTarget{}, fmt.Errorf("no published package for %s/%s", goos, goarch)
		case lowestMin != "":
			return release{}, releaseTarget{}, fmt.Errorf("requires MinCode %s or later (this is %s)", lowestMin, hostVersion)
		default:
			return release{}, releaseTarget{}, fmt.Errorf("supports MinCode below %s only (this is %s)", highestMax, hostVersion)
		}
	}
	return best, bestTarget, nil
}

func downloadVerified(ctx context.Context, origin string, target releaseTarget) (string, func(), error) {
	dir, err := os.MkdirTemp("", "mincode-market-plugin-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	path := filepath.Join(dir, "plugin.min")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		cleanup()
		return "", func() {}, err
	}
	digest := sha256.New()
	written, downloadErr := cloudcompanion.DownloadMarketArtifact(ctx, origin, target.ArtifactID, target.SizeBytes, io.MultiWriter(file, digest))
	closeErr := file.Close()
	if downloadErr != nil {
		cleanup()
		return "", func() {}, downloadErr
	}
	if closeErr != nil {
		cleanup()
		return "", func() {}, closeErr
	}
	if written != target.SizeBytes || hex.EncodeToString(digest.Sum(nil)) != target.SHA256 {
		cleanup()
		return "", func() {}, errors.New("downloaded package does not match the published size and SHA-256")
	}
	return path, cleanup, nil
}

func previewArchive(path, productKey string, selected release, target releaseTarget) (InstallPreview, error) {
	preview, err := plugin.PreviewDownloadedArchive(path)
	if err != nil {
		return InstallPreview{}, err
	}
	if preview.Manifest.ID != productKey || preview.Manifest.Version != selected.Version {
		return InstallPreview{}, errors.New("package manifest does not match the published release")
	}
	compatibility, err := plugin.EvaluateMarketplaceArchive(preview, plugin.MarketplaceRelease{
		Status: "published", TargetOS: target.OS, TargetArch: target.Arch,
		MinHostVersion: selected.MinHostVersion, MaxHostVersion: selected.MaxHostVersionExclusive,
		RuntimeProtocol: selected.RuntimeProtocol,
	}, misc.AppVersion)
	if err != nil {
		return InstallPreview{}, err
	}
	if !compatibility.Compatible {
		return InstallPreview{}, fmt.Errorf("v%s cannot be installed here: %s", selected.Version, compatibility.Reason)
	}
	return InstallPreview{Preview: preview, Version: selected.Version, SHA256: target.SHA256, VersionChange: compatibility.VersionChange}, nil
}
