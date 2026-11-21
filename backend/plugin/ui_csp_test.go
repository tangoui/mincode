package plugin

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mincode/app/plugins/api"
)

// cspDirectives splits a policy into directive name -> source list.
func cspDirectives(t *testing.T, csp string) map[string]string {
	t.Helper()
	directives := map[string]string{}
	for _, part := range strings.Split(csp, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, sources, _ := strings.Cut(part, " ")
		if _, duplicate := directives[name]; duplicate {
			t.Fatalf("duplicate directive %s in %s", name, csp)
		}
		directives[name] = sources
	}
	return directives
}

func TestPluginUICSPAllowsLocalMediaAndWorkersAndWasmOnlyWhenDeclared(t *testing.T) {
	const parent, asset = "tango://app:9250", "http://127.0.0.1:4100"
	for _, tc := range []struct {
		name            string
		framed, wasm    bool
		script, connect string
	}{
		{"plain", false, false, asset + " blob:", asset},
		{"framed", true, false, asset + " blob:", asset + " ws://127.0.0.1:4100"},
		{"wasm", false, true, asset + " blob: 'wasm-unsafe-eval'", asset},
		{"framed+wasm", true, true, asset + " blob: 'wasm-unsafe-eval'", asset + " ws://127.0.0.1:4100"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			csp := pluginUICSP(parent, asset, tc.framed, tc.wasm)
			got := cspDirectives(t, csp)
			want := map[string]string{
				"default-src":     "'none'",
				"script-src":      tc.script,
				"style-src":       asset + " blob:",
				"img-src":         asset + " data: blob:",
				"font-src":        asset + " data: blob:",
				"connect-src":     tc.connect,
				"frame-src":       "'none'",
				"child-src":       "'none'",
				"worker-src":      asset + " blob:",
				"object-src":      "'none'",
				"media-src":       asset + " blob:",
				"manifest-src":    "'none'",
				"base-uri":        "'none'",
				"form-action":     "'none'",
				"frame-ancestors": parent,
			}
			if len(got) != len(want) {
				t.Fatalf("directives = %v, want %v", got, want)
			}
			for name, sources := range want {
				if got[name] != sources {
					t.Fatalf("%s = %q, want %q (policy %s)", name, got[name], sources, csp)
				}
			}
			if strings.Contains(csp, "'unsafe-eval'") {
				t.Fatalf("policy must never allow unsafe-eval: %s", csp)
			}
		})
	}
}

func TestPluginUIContentTypeServesWasmAndMedia(t *testing.T) {
	for path, want := range map[string]string{
		"ui/vendor/decoder.WASM": "application/wasm",
		"ui/sounds/done.mp3":     "audio/mpeg",
		"ui/sounds/done.wav":     "audio/wav",
		"ui/sounds/done.ogg":     "audio/ogg",
		"ui/intro.mp4":           "video/mp4",
		"ui/intro.webm":          "video/webm",
	} {
		if got := pluginUIContentType(path); got != want {
			t.Fatalf("%s content type = %q, want %q", path, got, want)
		}
	}
}

func TestPluginUIWasmFollowsTheMarkedAssetScope(t *testing.T) {
	host := newPluginUIHost()
	defer host.Shutdown()
	plain := directTestSession(t, host, false)
	csp, _ := fetchCSP(t, plain)
	if strings.Contains(csp, "wasm-unsafe-eval") {
		t.Fatalf("unmarked session allows WebAssembly: %s", csp)
	}
	host.MarkSessionWebAssembly(plain.SessionID)
	csp, _ = fetchCSP(t, plain)
	if !strings.Contains(cspDirectives(t, csp)["script-src"], "'wasm-unsafe-eval'") {
		t.Fatalf("marked session lacks WebAssembly: %s", csp)
	}
	// A revoked plugin gets a fresh asset scope: the mark does not survive an
	// upgrade that drops ui.wasm.
	host.RevokePlugin("example.direct")
	next := directTestSession(t, host, false)
	csp, _ = fetchCSP(t, next)
	if strings.Contains(csp, "wasm-unsafe-eval") {
		t.Fatalf("a new asset scope inherited WebAssembly: %s", csp)
	}
}

func TestPluginUIServesWasmAssets(t *testing.T) {
	host := newPluginUIHost()
	defer host.Shutdown()
	session := directTestSession(t, host, false)
	parsed, err := url.Parse(session.URL)
	if err != nil {
		t.Fatal(err)
	}
	root := host.sessions[session.SessionID].Root
	if err := os.WriteFile(filepath.Join(root, "ui", "decoder.wasm"), []byte("\x00asm\x01\x00\x00\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	parsed.Fragment = ""
	parsed.Path = strings.TrimSuffix(parsed.Path, "index.html") + "decoder.wasm"
	response, err := http.Get(parsed.String()) // #nosec G107 -- test-only loopback listener.
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "application/wasm" {
		t.Fatalf("wasm asset: %d %q", response.StatusCode, response.Header.Get("Content-Type"))
	}
}

func TestBeginUIMarksWebAssemblyOnlyForUIWasmPlugins(t *testing.T) {
	for _, tc := range []struct {
		name         string
		capabilities []api.Capability
		wasm         bool
	}{
		{"ui", []api.Capability{api.CapabilityUI}, false},
		{"ui.wasm", []api.Capability{api.CapabilityUI, api.CapabilityUIWasm}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useTemporaryPluginData(t)
			root := t.TempDir()
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(executable)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "helper.exe"), data, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(root, "ui"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "ui", "index.html"), []byte("<!doctype html>"), 0o600); err != nil {
				t.Fatal(err)
			}
			// runtime.test is the ID TestPluginHelperProcess answers for.
			manifest := Manifest{ID: "runtime.test", Name: "Page", Version: "1.0.0", APIVersion: 2, Capabilities: tc.capabilities,
				Runtime:     ProcessRuntimeManifest{Type: "process", Entry: "helper.exe", Args: []string{"-test.run=^TestPluginHelperProcess$"}},
				Contributes: ManifestContributions{Views: []ManifestView{{ID: "view", Title: "View", Entry: "ui/index.html"}}}}
			manager := newDirectTestManager(t, root, manifest)
			session, err := manager.BeginUI(manifest.ID, "view", "tango://app")
			if err != nil {
				t.Fatal(err)
			}
			csp, _ := fetchCSP(t, session)
			if got := strings.Contains(csp, "'wasm-unsafe-eval'"); got != tc.wasm {
				t.Fatalf("wasm-unsafe-eval = %v, want %v: %s", got, tc.wasm, csp)
			}
		})
	}
}
