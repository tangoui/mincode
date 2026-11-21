package core

import (
	"reflect"
	"testing"
)

func TestResolveMinCodeAPIServersReturnsDefaultWithoutUnverifiedFallbacks(t *testing.T) {
	t.Setenv("MINCODE_API_BASE_URL", "")
	t.Setenv("MINCODE_LICENSE_SERVER_URL", "")
	t.Setenv("MINCODE_UPDATE_SERVER_URL", "")

	got := ResolveMinCodeAPIServers("")
	want := append([]string{DefaultMinCodeAPIBaseURL}, MinCodeAPIFallbackBaseURLs...)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pool = %v, want %v", got, want)
	}
	if len(got) != 1 || got[0] != DefaultMinCodeAPIBaseURL {
		t.Fatalf("default pool = %v, want only verified production origin", got)
	}
	if got[0] != DefaultMinCodeAPIBaseURL {
		t.Fatalf("primary origin = %q, want %q", got[0], DefaultMinCodeAPIBaseURL)
	}

	if pinned := ResolveMinCodeAPIServers("https://custom.example.test"); !reflect.DeepEqual(pinned, []string{"https://custom.example.test"}) {
		t.Fatalf("explicit config = %v, want single pinned origin", pinned)
	}
}

func TestResolveMinCodeAPIServerPrecedence(t *testing.T) {
	t.Setenv("MINCODE_API_BASE_URL", "https://shared.example.test")
	t.Setenv("MINCODE_LICENSE_SERVER_URL", "https://license.example.test")
	t.Setenv("MINCODE_UPDATE_SERVER_URL", "https://update.example.test")

	if got := ResolveMinCodeAPIServer("https://argument.example.test", "MINCODE_LICENSE_SERVER_URL"); got != "https://argument.example.test" {
		t.Fatalf("explicit URL = %q", got)
	}
	if got := ResolveMinCodeAPIServer("", "MINCODE_LICENSE_SERVER_URL"); got != "https://license.example.test" {
		t.Fatalf("service URL = %q", got)
	}
	if got := ResolveMinCodeAPIServer("", "MINCODE_UPDATE_SERVER_URL"); got != "https://update.example.test" {
		t.Fatalf("update URL = %q", got)
	}
}

func TestResolveMinCodeAPIServerUsesSharedThenProductionDefault(t *testing.T) {
	t.Setenv("MINCODE_API_BASE_URL", "https://shared.example.test")
	if got := ResolveMinCodeAPIServer(""); got != "https://shared.example.test" {
		t.Fatalf("shared URL = %q", got)
	}

	t.Setenv("MINCODE_API_BASE_URL", "")
	if got := ResolveMinCodeAPIServer(""); got != DefaultMinCodeAPIBaseURL {
		t.Fatalf("default URL = %q", got)
	}
}

func TestResolveMinCodeCloudServerSharedPrecedence(t *testing.T) {
	t.Setenv("MINCODE_API_BASE_URL", "https://shared.example.test")
	t.Setenv("MINCODE_MARKET_SERVER_URL", "https://market.example.test")
	t.Setenv("MINCODE_CLOUD_SERVER_URL", "https://cloud.example.test")

	if got := ResolveMinCodeCloudServer(" https://argument.example.test "); got != "https://argument.example.test" {
		t.Fatalf("explicit URL = %q", got)
	}
	if got := ResolveMinCodeCloudServer(""); got != "https://cloud.example.test" {
		t.Fatalf("cloud URL = %q", got)
	}
	t.Setenv("MINCODE_CLOUD_SERVER_URL", "")
	if got := ResolveMinCodeCloudServer(""); got != "https://market.example.test" {
		t.Fatalf("legacy market URL = %q", got)
	}
	t.Setenv("MINCODE_MARKET_SERVER_URL", "")
	if got := ResolveMinCodeCloudServer(""); got != "https://shared.example.test" {
		t.Fatalf("shared URL = %q", got)
	}
	t.Setenv("MINCODE_API_BASE_URL", "")
	if got := ResolveMinCodeCloudServer(""); got != defaultMinCodeCloudBaseURL {
		t.Fatalf("default URL = %q", got)
	}
}
