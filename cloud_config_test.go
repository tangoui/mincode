package main

import (
	"testing"

	"mincode/app/backend/core"
)

func TestDesktopCloudDefault(t *testing.T) {
	for _, key := range []string{"MINCODE_CLOUD_SERVER_URL", "MINCODE_MARKET_SERVER_URL", "MINCODE_API_BASE_URL"} {
		t.Setenv(key, "")
	}
	if got := core.ResolveMinCodeCloudServer(""); got != "https://api.mincodeai.cn" {
		t.Fatalf("desktop default = %q, want public cloud", got)
	}
}

func TestDesktopCloudOriginParsing(t *testing.T) {
	for _, tc := range []struct{ data, want string }{
		{`{"cloud":{"apiUrl":" https://cloud.example.test:8443/ "}}`, "https://cloud.example.test:8443"},
		{`{}`, ""},
		{`{"cloud":`, ""},
		{`{"cloud":{"apiUrl":"http://cloud.example.test"}}`, ""},
		{`{"cloud":{"apiUrl":"https://cloud.example.test/path"}}`, ""},
		{`{"cloud":{"apiUrl":"https://user@cloud.example.test"}}`, ""},
	} {
		if got := desktopCloudOrigin([]byte(tc.data)); got != tc.want {
			t.Errorf("desktopCloudOrigin(%q) = %q, want %q", tc.data, got, tc.want)
		}
	}
}

func TestDesktopCloudConfigPrecedence(t *testing.T) {
	core.SetDesktopCloudDefault(desktopCloudOrigin([]byte(`{"cloud":{"apiUrl":"https://cloud.example.test"}}`)))
	t.Cleanup(func() { core.SetDesktopCloudDefault(desktopCloudOrigin(desktopProjectConfig)) })
	for _, key := range []string{"MINCODE_CLOUD_SERVER_URL", "MINCODE_MARKET_SERVER_URL", "MINCODE_API_BASE_URL"} {
		t.Setenv(key, "")
	}
	if got := core.ResolveMinCodeCloudServer(""); got != "https://cloud.example.test" {
		t.Fatalf("configured default = %q", got)
	}
	t.Setenv("MINCODE_CLOUD_SERVER_URL", "http://127.0.0.1:8000")
	if got := core.ResolveMinCodeCloudServer(""); got != "http://127.0.0.1:8000" {
		t.Fatalf("environment override = %q", got)
	}
	if got := core.ResolveMinCodeCloudServer("https://argument.example.test"); got != "https://argument.example.test" {
		t.Fatalf("explicit override = %q", got)
	}
}
