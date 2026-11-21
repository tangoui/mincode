package market

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"mincode/app/backend/misc"
)

func TestSelectReleasePicksHighestVersionForHost(t *testing.T) {
	digest := strings.Repeat("a", 64)
	target := func(id int64, goos, goarch string) releaseTarget {
		return releaseTarget{ArtifactID: id, OS: goos, Arch: goarch, Format: "zip", SizeBytes: 10, SHA256: digest}
	}
	releases := []release{
		{Version: "0.9.0", Targets: []releaseTarget{target(1, "darwin", "arm64")}},
		{Version: "0.10.0", Targets: []releaseTarget{target(2, "linux", "amd64"), target(3, "darwin", "arm64")}},
		{Version: "1.0.0", Targets: []releaseTarget{target(4, "linux", "amd64")}},
	}
	selected, chosen, err := selectRelease(releases, "darwin", "arm64", "0.1.0")
	if err != nil || selected.Version != "0.10.0" || chosen.ArtifactID != 3 {
		t.Fatalf("selectRelease = %s/%d, %v; want 0.10.0/3", selected.Version, chosen.ArtifactID, err)
	}
	if _, _, err := selectRelease(releases, "windows", "amd64", "0.1.0"); err == nil || !strings.Contains(err.Error(), "windows/amd64") {
		t.Fatalf("missing target error = %v", err)
	}
	if _, _, err := selectRelease(nil, "darwin", "arm64", "0.1.0"); err == nil || !strings.Contains(err.Error(), "no published release") {
		t.Fatalf("empty release error = %v", err)
	}
	for name, bad := range map[string]releaseTarget{
		"artifact id": {ArtifactID: 0, OS: "darwin", Arch: "arm64", Format: "zip", SizeBytes: 10, SHA256: digest},
		"format":      {ArtifactID: 1, OS: "darwin", Arch: "arm64", Format: "tar", SizeBytes: 10, SHA256: digest},
		"size":        {ArtifactID: 1, OS: "darwin", Arch: "arm64", Format: "zip", SizeBytes: maxArtifactBytes + 1, SHA256: digest},
		"digest":      {ArtifactID: 1, OS: "darwin", Arch: "arm64", Format: "zip", SizeBytes: 10, SHA256: strings.Repeat("A", 64)},
	} {
		if _, _, err := selectRelease([]release{{Version: "1.0.0", Targets: []releaseTarget{bad}}}, "darwin", "arm64", "0.1.0"); err == nil {
			t.Fatalf("invalid %s accepted", name)
		}
	}
	if _, _, err := selectRelease([]release{{Version: "v1.0.0"}}, "darwin", "arm64", "0.1.0"); err == nil {
		t.Fatal("invalid version accepted")
	}
}

func TestSelectReleaseSkipsVersionsThisMinCodeCannotRun(t *testing.T) {
	digest := strings.Repeat("a", 64)
	here := []releaseTarget{{ArtifactID: 1, OS: "darwin", Arch: "arm64", Format: "zip", SizeBytes: 10, SHA256: digest}}
	releases := []release{
		{Version: "1.0.0", Targets: here},
		{Version: "1.1.0", MinHostVersion: "0.2.0", Targets: here},
		{Version: "1.2.0", MinHostVersion: "0.3.0", Targets: here},
	}
	if selected, _, err := selectRelease(releases, "darwin", "arm64", "0.2.5"); err != nil || selected.Version != "1.1.0" {
		t.Fatalf("selectRelease(0.2.5) = %s, %v; want newest compatible 1.1.0", selected.Version, err)
	}
	if selected, _, err := selectRelease(releases, "darwin", "arm64", "0.3.0"); err != nil || selected.Version != "1.2.0" {
		t.Fatalf("selectRelease(0.3.0) = %s, %v; want 1.2.0 (minimum is inclusive)", selected.Version, err)
	}
	tooOld := []release{{Version: "2.0.0", MinHostVersion: "0.4.0", Targets: here}, {Version: "1.5.0", MinHostVersion: "0.2.0", Targets: here}}
	if _, _, err := selectRelease(tooOld, "darwin", "arm64", "0.1.0"); err == nil || err.Error() != "requires MinCode 0.2.0 or later (this is 0.1.0)" {
		t.Fatalf("too old host error = %v; want the lowest minimum", err)
	}
	tooNew := []release{{Version: "1.0.0", MaxHostVersionExclusive: "0.2.0", Targets: here}}
	if _, _, err := selectRelease(tooNew, "darwin", "arm64", "0.2.0"); err == nil || err.Error() != "supports MinCode below 0.2.0 only (this is 0.2.0)" {
		t.Fatalf("too new host error = %v (maximum is exclusive)", err)
	}
	if _, _, err := selectRelease([]release{{Version: "1.0.0", MinHostVersion: "v0.2", Targets: here}}, "darwin", "arm64", "0.1.0"); err == nil {
		t.Fatal("malformed host range accepted")
	}
}

func TestFetchReleasesValidatesEnvelope(t *testing.T) {
	body := `{"code":1,"data":{"plugin_key":"todo","releases":[{"version":"0.1.9","manifest_api":2,"runtime_protocol":1,"min_host_version":"","max_host_version_exclusive":"","targets":[{"artifact_id":48,"os":"darwin","arch":"arm64","format":"zip","size_bytes":5,"sha256":"` + strings.Repeat("c", 64) + `"}]}]}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != releasesPath || r.Header.Get("ba-user-token") != "" {
			t.Errorf("unexpected request %s", r.URL)
		}
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	list, err := fetchReleases(context.Background(), base, "todo")
	if err != nil || len(list.Releases) != 1 || list.Releases[0].Targets[0].ArtifactID != 48 || list.Releases[0].RuntimeProtocol != 1 || list.RequiresPaidHost {
		t.Fatalf("fetchReleases = %+v, %v", list, err)
	}
	if _, err := fetchReleases(context.Background(), base, "draw"); err == nil {
		t.Fatal("release list for another plugin accepted")
	}
	body = `{"code":1,"data":{"plugin_key":"todo","product":{"requires_paid_host":true},"releases":[]}}`
	if list, err := fetchReleases(context.Background(), base, "todo"); err != nil || !list.RequiresPaidHost {
		t.Fatalf("requires_paid_host not parsed: %+v, %v", list, err)
	}
	body = `{"code":0,"msg":"error","data":null}`
	if _, err := fetchReleases(context.Background(), base, "todo"); err == nil {
		t.Fatal("unsuccessful envelope accepted")
	}
}

func TestFetchReleasesSendsHostVersion(t *testing.T) {
	previous := misc.AppVersion
	t.Cleanup(func() { misc.AppVersion = previous })
	var query url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		_, _ = w.Write([]byte(`{"code":1,"data":{"plugin_key":"todo","releases":[]}}`))
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)

	misc.AppVersion = "0.2.0"
	if _, err := fetchReleases(context.Background(), base, "todo"); err != nil {
		t.Fatal(err)
	}
	if len(query) != 2 || query.Get("plugin_key") != "todo" || query.Get("host_version") != "0.2.0" {
		t.Fatalf("release list query = %v; want plugin_key and host_version", query)
	}
	misc.AppVersion = "dev"
	if _, err := fetchReleases(context.Background(), base, "todo"); err != nil {
		t.Fatal(err)
	}
	if query.Has("host_version") || query.Get("plugin_key") != "todo" {
		t.Fatalf("release list query = %v; an invalid version must not be sent", query)
	}
}

func TestInstallPreviewRefusesPaidHostPluginOnFreePlan(t *testing.T) {
	body := `{"code":1,"data":{"plugin_key":"todo","product":{"requires_paid_host":true},"releases":[]}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	t.Setenv("MINCODE_CLOUD_SERVER_URL", server.URL)
	t.Setenv("MINCODE_MARKET_SERVER_URL", "")
	previousPaid, previousRecord := hostPlanPaid, recordPaidHostRequirement
	t.Cleanup(func() { hostPlanPaid, recordPaidHostRequirement = previousPaid, previousRecord })
	recordPaidHostRequirement = func(string, bool) error { t.Error("requirement recorded before a successful preview"); return nil }

	hostPlanPaid = func() bool { return false }
	if _, err := installPreviewHandler("", `{"productKey":"todo"}`); err == nil || !strings.Contains(err.Error(), "paid MinCode plan") {
		t.Fatalf("free plan preview error = %v", err)
	}
	// A paid plan passes the gate and fails later only because nothing is published.
	hostPlanPaid = func() bool { return true }
	if _, err := installPreviewHandler("", `{"productKey":"todo"}`); err == nil || !strings.Contains(err.Error(), "no published release") {
		t.Fatalf("paid plan preview error = %v", err)
	}
}
