package plugin

import (
	"strings"
	"testing"

	"mincode/app/plugins/api"
)

func marketplaceTestRelease(version string) MarketplaceRelease {
	return MarketplaceRelease{
		Manifest: Manifest{
			ID: "example.plugin", Version: version, APIVersion: api.ManifestAPIVersionV2,
			Platforms: []string{"darwin"}, Capabilities: []api.Capability{api.CapabilityStorage},
		},
		Status: "published", TargetOS: "darwin", TargetArch: "arm64", RuntimeProtocol: api.RuntimeProtocolVersion,
	}
}

func TestEvaluateMarketplaceCompatibilityTable(t *testing.T) {
	installed := func(version string, capabilities ...api.Capability) *Manifest {
		return &Manifest{ID: "example.plugin", Version: version, Capabilities: capabilities}
	}
	tests := []struct {
		name       string
		release    MarketplaceRelease
		installed  *Manifest
		host       string
		os, arch   string
		compatible bool
		change     string
		reason     string
		reapproval bool
		reenable   bool
	}{
		{name: "stable upgrade", release: marketplaceTestRelease("1.1.0"), installed: installed("1.0.0", api.CapabilityStorage), host: "2.0.0", os: "darwin", arch: "arm64", compatible: true, change: "upgrade", reenable: true},
		{name: "pre release is ordered by semver", release: marketplaceTestRelease("1.1.0-beta.1"), installed: installed("1.1.0-alpha.1", api.CapabilityStorage), host: "2.0.0", os: "darwin", arch: "arm64", compatible: true, change: "upgrade", reenable: true},
		{name: "downgrade is classified", release: marketplaceTestRelease("0.9.0"), installed: installed("1.0.0", api.CapabilityStorage), host: "2.0.0", os: "darwin", arch: "arm64", change: "downgrade", reason: "local ZIP"},
		{name: "unchanged", release: marketplaceTestRelease("1.0.0"), installed: installed("1.0.0", api.CapabilityStorage), host: "2.0.0", os: "darwin", arch: "arm64", change: "unchanged", reason: "same-version"},
		{name: "stable supersedes prerelease", release: marketplaceTestRelease("1.1.0"), installed: installed("1.1.0-rc.1", api.CapabilityStorage), host: "2.0.0", os: "darwin", arch: "arm64", compatible: true, change: "upgrade", reenable: true},
		{name: "minimum inclusive", release: func() MarketplaceRelease { r := marketplaceTestRelease("1.0.0"); r.MinHostVersion = "2.0.0"; return r }(), host: "2.0.0", os: "darwin", arch: "arm64", compatible: true, change: "install"},
		{name: "below minimum", release: func() MarketplaceRelease { r := marketplaceTestRelease("1.0.0"); r.MinHostVersion = "2.0.1"; return r }(), host: "2.0.0", os: "darwin", arch: "arm64", reason: "minimum", change: "install"},
		{name: "below maximum", release: func() MarketplaceRelease { r := marketplaceTestRelease("1.0.0"); r.MaxHostVersion = "3.0.0"; return r }(), host: "2.9.9", os: "darwin", arch: "arm64", compatible: true, change: "install"},
		{name: "maximum exclusive", release: func() MarketplaceRelease { r := marketplaceTestRelease("1.0.0"); r.MaxHostVersion = "3.0.0"; return r }(), host: "3.0.0", os: "darwin", arch: "arm64", reason: "maximum", change: "install"},
		{name: "unsupported architecture", release: marketplaceTestRelease("1.0.0"), host: "2.0.0", os: "darwin", arch: "386", reason: "target", change: "install"},
		{name: "supported windows target", release: func() MarketplaceRelease {
			r := marketplaceTestRelease("1.0.0")
			r.TargetOS = "windows"
			r.TargetArch = "amd64"
			r.Manifest.Platforms = []string{"windows"}
			return r
		}(), host: "2.0.0", os: "windows", arch: "amd64", compatible: true, change: "install"},
		{name: "manifest OS mismatch", release: func() MarketplaceRelease {
			r := marketplaceTestRelease("1.0.0")
			r.Manifest.Platforms = []string{"linux"}
			return r
		}(), host: "2.0.0", os: "darwin", arch: "arm64", reason: "manifest", change: "install"},
		{name: "API v2", release: func() MarketplaceRelease {
			r := marketplaceTestRelease("1.0.0")
			r.Manifest.APIVersion = api.ManifestAPIVersionV2
			r.Manifest.Capabilities = append(r.Manifest.Capabilities, api.CapabilityUI)
			return r
		}(), host: "2.0.0", os: "darwin", arch: "arm64", compatible: true, change: "install"},
		{name: "manifest API v1 is unsupported", release: func() MarketplaceRelease { r := marketplaceTestRelease("1.0.0"); r.Manifest.APIVersion = 1; return r }(), host: "2.0.0", os: "darwin", arch: "arm64", reason: "API", change: "install"},
		{name: "unsupported API", release: func() MarketplaceRelease { r := marketplaceTestRelease("1.0.0"); r.Manifest.APIVersion = 3; return r }(), host: "2.0.0", os: "darwin", arch: "arm64", reason: "API", change: "install"},
		{name: "unsupported protocol", release: func() MarketplaceRelease { r := marketplaceTestRelease("1.0.0"); r.RuntimeProtocol = 2; return r }(), host: "2.0.0", os: "darwin", arch: "arm64", reason: "protocol", change: "install"},
		{name: "wrong target", release: marketplaceTestRelease("1.0.0"), host: "2.0.0", os: "linux", arch: "amd64", reason: "target", change: "install"},
		{name: "withdrawn", release: func() MarketplaceRelease { r := marketplaceTestRelease("1.0.0"); r.Status = "withdrawn"; return r }(), host: "2.0.0", os: "darwin", arch: "arm64", reason: "withdrawn", change: "install"},
		{name: "blocked", release: func() MarketplaceRelease { r := marketplaceTestRelease("1.0.0"); r.Status = "blocked"; return r }(), host: "2.0.0", os: "darwin", arch: "arm64", reason: "blocked", change: "install"},
		{name: "downgrade with added capability is not authorized", release: func() MarketplaceRelease {
			r := marketplaceTestRelease("0.9.0")
			r.Manifest.Capabilities = append(r.Manifest.Capabilities, api.CapabilityNetwork)
			return r
		}(), installed: installed("1.0.0", api.CapabilityStorage), host: "2.0.0", os: "darwin", arch: "arm64", change: "downgrade", reason: "local ZIP", reapproval: true},
		{name: "added capability needs approval", release: func() MarketplaceRelease {
			r := marketplaceTestRelease("1.1.0")
			r.Manifest.Capabilities = append(r.Manifest.Capabilities, api.CapabilityNetwork)
			return r
		}(), installed: installed("1.0.0", api.CapabilityStorage), host: "2.0.0", os: "darwin", arch: "arm64", compatible: true, change: "upgrade", reapproval: true, reenable: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := EvaluateMarketplaceCompatibility(test.release, test.installed, test.host, test.os, test.arch)
			if err != nil {
				t.Fatal(err)
			}
			if got.Compatible != test.compatible || got.VersionChange != test.change || got.RequiresReapproval != test.reapproval || got.RequiresReenable != test.reenable {
				t.Fatalf("got %#v, want compatible=%v change=%q reapproval=%v reenable=%v", got, test.compatible, test.change, test.reapproval, test.reenable)
			}
			if test.reapproval && (len(got.AddedCapabilities) != 1 || got.AddedCapabilities[0] != api.CapabilityNetwork) {
				t.Fatalf("unexpected added capabilities: %v", got.AddedCapabilities)
			}
			if test.reason != "" && !strings.Contains(got.Reason, test.reason) {
				t.Fatalf("reason %q does not contain %q", got.Reason, test.reason)
			}
		})
	}
}

func TestEvaluateMarketplaceCompatibilityRejectsInvalidInputs(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*MarketplaceRelease)
		host   string
	}{
		{name: "invalid release version", mutate: func(r *MarketplaceRelease) { r.Manifest.Version = "not-semver" }, host: "2.0.0"},
		{name: "invalid host version", host: "not-semver"},
		{name: "invalid minimum", mutate: func(r *MarketplaceRelease) { r.MinHostVersion = "bad" }, host: "2.0.0"},
		{name: "empty host range", mutate: func(r *MarketplaceRelease) { r.MinHostVersion = "2.0.0"; r.MaxHostVersion = "2.0.0" }, host: "2.0.0"},
		{name: "invalid protocol", mutate: func(r *MarketplaceRelease) { r.RuntimeProtocol = 0 }, host: "2.0.0"},
		{name: "invalid status", mutate: func(r *MarketplaceRelease) { r.Status = "draft" }, host: "2.0.0"},
		{name: "invalid capability", mutate: func(r *MarketplaceRelease) { r.Manifest.Capabilities = []api.Capability{"made-up"} }, host: "2.0.0"},
		{name: "empty target", host: "2.0.0"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			release := marketplaceTestRelease("1.0.0")
			if test.mutate != nil {
				test.mutate(&release)
			}
			os, arch := "darwin", "arm64"
			if test.name == "empty target" {
				os = ""
			}
			if _, err := EvaluateMarketplaceCompatibility(release, nil, test.host, os, arch); err == nil {
				t.Fatal("expected invalid input error")
			}
		})
	}
}
