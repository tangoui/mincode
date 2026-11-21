package plugin

import (
	"fmt"
	"runtime"
	"strings"

	"mincode/app/plugins/api"

	"golang.org/x/mod/semver"
)

// MarketplaceRelease is compatibility metadata only; it does not identify a
// download, authorize installation, or change the existing upgrade policy.
type MarketplaceRelease struct {
	Manifest        Manifest
	Status          string
	TargetOS        string
	TargetArch      string
	MinHostVersion  string // inclusive; empty means unbounded
	MaxHostVersion  string // exclusive; empty means unbounded
	RuntimeProtocol int
}

type MarketplaceCompatibility struct {
	Compatible         bool
	Reason             string
	VersionChange      string // install, upgrade, downgrade, unchanged
	RequiresReapproval bool   // newly requested capabilities, independent of the replacement lifecycle
	RequiresReenable   bool   // a successful replacement disables the plugin even with no new capabilities
	AddedCapabilities  []api.Capability
}

// EvaluateMarketplaceCompatibility makes a side-effect-free preflight decision.
// Versions use unprefixed semver text, like plugin manifests. This metadata
// does not authorize installation: downgrades require a separately confirmed
// local ZIP, and unchanged versions cannot replace an installed package.
func EvaluateMarketplaceCompatibility(release MarketplaceRelease, installed *Manifest, hostVersion, targetOS, targetArch string) (MarketplaceCompatibility, error) {
	if targetOS == "" || targetArch == "" || release.TargetOS == "" || release.TargetArch == "" {
		return MarketplaceCompatibility{}, fmt.Errorf("marketplace target OS and arch are required")
	}
	if !validMarketplaceVersion(release.Manifest.Version) {
		return MarketplaceCompatibility{}, fmt.Errorf("invalid marketplace plugin version %q", release.Manifest.Version)
	}
	if installed != nil {
		if !validMarketplaceVersion(installed.Version) {
			return MarketplaceCompatibility{}, fmt.Errorf("invalid installed plugin version %q", installed.Version)
		}
		if installed.ID == "" || installed.ID != release.Manifest.ID {
			return MarketplaceCompatibility{}, fmt.Errorf("marketplace plugin ID does not match installed plugin")
		}
	}
	if release.Manifest.ID == "" {
		return MarketplaceCompatibility{}, fmt.Errorf("marketplace plugin ID is required")
	}
	if !validMarketplaceVersion(hostVersion) {
		return MarketplaceCompatibility{}, fmt.Errorf("invalid host version %q", hostVersion)
	}
	for _, bound := range []struct{ name, value string }{{"minimum", release.MinHostVersion}, {"maximum", release.MaxHostVersion}} {
		if bound.value != "" && !validMarketplaceVersion(bound.value) {
			return MarketplaceCompatibility{}, fmt.Errorf("invalid %s host version %q", bound.name, bound.value)
		}
	}
	if release.MinHostVersion != "" && release.MaxHostVersion != "" && semver.Compare("v"+release.MinHostVersion, "v"+release.MaxHostVersion) >= 0 {
		return MarketplaceCompatibility{}, fmt.Errorf("marketplace host version range is empty")
	}
	switch release.Status {
	case "published", "withdrawn", "blocked":
	default:
		return MarketplaceCompatibility{}, fmt.Errorf("invalid marketplace release status %q", release.Status)
	}
	if err := validateMarketplaceCapabilities(release.Manifest.Capabilities); err != nil {
		return MarketplaceCompatibility{}, err
	}
	if release.RuntimeProtocol != api.RuntimeProtocolVersion && release.RuntimeProtocol <= 0 {
		return MarketplaceCompatibility{}, fmt.Errorf("invalid runtime protocol %d", release.RuntimeProtocol)
	}

	result := MarketplaceCompatibility{VersionChange: "install"}
	if installed != nil {
		switch semver.Compare("v"+release.Manifest.Version, "v"+installed.Version) {
		case 1:
			result.VersionChange = "upgrade"
		case -1:
			result.VersionChange = "downgrade"
		default:
			result.VersionChange = "unchanged"
		}
		approved := make(map[api.Capability]bool, len(installed.Capabilities))
		for _, capability := range installed.Capabilities {
			approved[capability] = true
		}
		for _, capability := range release.Manifest.Capabilities {
			if !approved[capability] {
				result.AddedCapabilities = append(result.AddedCapabilities, capability)
				approved[capability] = true
			}
		}
		result.RequiresReapproval = len(result.AddedCapabilities) > 0
	}
	incompatible := func(reason string) (MarketplaceCompatibility, error) {
		result.Reason = reason
		return result, nil
	}
	if release.Status != "published" {
		return incompatible("release " + release.Status)
	}
	if !marketplaceSupportedTarget(targetOS, targetArch) || !marketplaceSupportedTarget(release.TargetOS, release.TargetArch) || targetOS != release.TargetOS || targetArch != release.TargetArch {
		return incompatible("unsupported or mismatched target")
	}
	if len(release.Manifest.Platforms) > 0 {
		platformFound := false
		for _, platform := range release.Manifest.Platforms {
			if platform == targetOS {
				platformFound = true
			}
		}
		if !platformFound {
			return incompatible("plugin manifest does not support target OS")
		}
	}
	if release.Manifest.APIVersion != api.ManifestAPIVersionV2 {
		return incompatible("unsupported manifest API version")
	}
	if release.RuntimeProtocol != api.RuntimeProtocolVersion {
		return incompatible("unsupported runtime protocol")
	}
	if release.MinHostVersion != "" && semver.Compare("v"+hostVersion, "v"+release.MinHostVersion) < 0 {
		return incompatible("host below minimum version")
	}
	if release.MaxHostVersion != "" && semver.Compare("v"+hostVersion, "v"+release.MaxHostVersion) >= 0 {
		return incompatible("host at or above maximum version")
	}
	if result.VersionChange == "downgrade" {
		return incompatible("downgrade requires a separately confirmed local ZIP package")
	}
	if result.VersionChange == "unchanged" {
		return incompatible("same-version replacement is not allowed")
	}
	result.Compatible = true
	result.RequiresReenable = result.VersionChange == "upgrade"
	return result, nil
}

// EvaluateMarketplaceArchive checks the manifest inside a previewed marketplace
// archive, not the listing metadata, against this host and any installed copy.
func EvaluateMarketplaceArchive(preview InstallPreview, release MarketplaceRelease, hostVersion string) (MarketplaceCompatibility, error) {
	release.Manifest = preview.Manifest
	manager := host()
	manager.mu.RLock()
	record := manager.records[preview.Manifest.ID]
	manager.mu.RUnlock()
	if record == nil {
		if preview.Installed {
			return MarketplaceCompatibility{Reason: "an unregistered plugin directory already uses this ID"}, nil
		}
		return EvaluateMarketplaceCompatibility(release, nil, hostVersion, runtime.GOOS, runtime.GOARCH)
	}
	record.mu.Lock()
	var installed *Manifest
	if record.manifest != nil {
		copied := *record.manifest
		installed = &copied
	}
	source := record.source
	record.mu.Unlock()
	if installed == nil || source != pluginSourceLocal {
		return MarketplaceCompatibility{Reason: "a built-in plugin already uses this ID"}, nil
	}
	return EvaluateMarketplaceCompatibility(release, installed, hostVersion, runtime.GOOS, runtime.GOARCH)
}

func validMarketplaceVersion(version string) bool {
	return version != "" && !strings.HasPrefix(version, "v") && strings.TrimSpace(version) == version && semver.IsValid("v"+version)
}

// Mirrors the four target pairs supported by backend/updater.currentPlatform.
func marketplaceSupportedTarget(goos, goarch string) bool {
	switch goos + "/" + goarch {
	case "windows/amd64", "darwin/amd64", "darwin/arm64", "linux/amd64":
		return true
	default:
		return false
	}
}

func validateMarketplaceCapabilities(capabilities []api.Capability) error {
	known := make(map[api.Capability]bool)
	for _, capability := range append(api.CapabilitiesV1(), api.CapabilitiesV2Only()...) {
		known[capability] = true
	}
	for _, capability := range capabilities {
		if !known[capability] {
			return fmt.Errorf("unknown marketplace plugin capability %q", capability)
		}
	}
	return nil
}
