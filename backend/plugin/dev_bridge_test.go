package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mincode/app/plugins/api"
)

func TestPluginDevUIURLRequiresExplicitLoopback(t *testing.T) {
	valid, err := validatePluginDevUIURL("http://127.0.0.1:5173/0123456789abcdef0123456789abcdef0123456789abcdef/")
	if err != nil || valid == "" {
		t.Fatalf("expected loopback URL, got %q, %v", valid, err)
	}
	for _, candidate := range []string{
		"https://127.0.0.1:5173/session/",
		"http://localhost:5173/session/",
		"http://0.0.0.0:5173/session/",
		"http://127.0.0.1/session/",
		"http://127.0.0.1:5173/session/?token=secret",
		"http://127.0.0.1:5173/session/",
		"http://127.0.0.1:5173/0123456789abcdef0123456789abcdef0123456789abcdef/extra/",
	} {
		if _, err := validatePluginDevUIURL(candidate); err == nil {
			t.Fatalf("expected URL rejection for %q", candidate)
		}
	}
}

func TestPluginDevLeaseExpiryAndConstantTimeLookup(t *testing.T) {
	bridge := newPluginDevBridge()
	session := &pluginDevSession{
		ID: "session", LastSeenAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute), leaseToken: "lease-token",
	}
	bridge.sessions[session.ID] = session
	bridge.byLease[session.leaseToken] = session

	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/heartbeat", nil)
	request.Header.Set("Authorization", "Bearer lease-token")
	if authenticated := bridge.authenticateLease(request); authenticated == nil || authenticated.ID != session.ID {
		t.Fatal("expected valid lease")
	}
	request.Header.Set("Authorization", "Bearer forged-token")
	if authenticated := bridge.authenticateLease(request); authenticated != nil {
		t.Fatal("expected forged lease rejection")
	}

	session.LastSeenAt = time.Now().Add(-pluginDevHeartbeatTTL - time.Second)
	request.Header.Set("Authorization", "Bearer lease-token")
	if authenticated := bridge.authenticateLease(request); authenticated != nil {
		t.Fatal("expected stale heartbeat rejection")
	}
}

func TestValidatePluginDevGeneration(t *testing.T) {
	project := t.TempDir()
	root := filepath.Join(project, ".mincode", "dev", "generations", "00000000000000000001")
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte("runtime")
	if err := os.WriteFile(filepath.Join(root, "bin", "plugin"), content, 0o700); err != nil {
		t.Fatal(err)
	}
	fileHash := sha256.Sum256(content)
	metadata, err := json.Marshal(pluginDevGenerationManifest{
		ProtocolVersion: pluginDevProtocolVersion,
		Generation:      1,
		Files:           map[string]string{"bin/plugin": hex.EncodeToString(fileHash[:])},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".mincode-generation.json"), append(metadata, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	ready := sha256.Sum256(metadata)
	if err := os.WriteFile(filepath.Join(root, ".mincode-ready"), []byte(hex.EncodeToString(ready[:])+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validatePluginDevGeneration(project, root, 1); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bin", "plugin"), []byte("changed"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := validatePluginDevGeneration(project, root, 1); err == nil {
		t.Fatal("changed generation accepted")
	}
}

func TestPluginDevPruneDisconnectsExpiredSession(t *testing.T) {
	bridge := newPluginDevBridge()
	disconnected := make(chan pluginDevSession, 1)
	bridge.onDisconnected = func(session pluginDevSession) { disconnected <- session }
	session := &pluginDevSession{
		ID: "expired", LastSeenAt: time.Now().Add(-pluginDevHeartbeatTTL - time.Second),
		ExpiresAt: time.Now().Add(time.Minute), leaseToken: "lease",
	}
	bridge.sessions[session.ID] = session
	bridge.byLease[session.leaseToken] = session
	if sessions := bridge.Sessions(); len(sessions) != 0 {
		t.Fatalf("expected expired session removal, got %+v", sessions)
	}
	select {
	case got := <-disconnected:
		if got.ID != session.ID {
			t.Fatalf("unexpected disconnected session: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("expired session did not trigger cleanup")
	}
}

func TestPluginDevLogRateLimit(t *testing.T) {
	session := &pluginDevSession{}
	now := time.Now()
	for index := 0; index < pluginDevLogRateLimit; index++ {
		if !session.acceptLogLocked(now) {
			t.Fatalf("log %d rejected before limit", index)
		}
	}
	if session.acceptLogLocked(now) {
		t.Fatal("log accepted above limit")
	}
	if !session.acceptLogLocked(now.Add(pluginDevLogRateWindow)) {
		t.Fatal("log window did not reset")
	}
}

func TestPluginDevLogRedaction(t *testing.T) {
	got := redactPluginDevLog("request token=abc password:secret authorization=Bearer-123 header Bearer jwt.value {\"secret\":\"json-value\"}\n-----BEGIN PRIVATE KEY-----\nprivate-value\n-----END PRIVATE KEY----- ok")
	for _, secret := range []string{"abc", "Bearer-123", "jwt.value", "json-value", "private-value"} {
		if strings.Contains(got, secret) {
			t.Fatalf("sensitive value %q was not redacted: %s", secret, got)
		}
	}
	if !strings.Contains(got, "Bearer [REDACTED]") || !strings.Contains(got, "[REDACTED PEM]") {
		t.Fatalf("unexpected redaction: %s", got)
	}
	long := redactPluginDevLog(strings.Repeat("x", 5*1024))
	if len(long) != 4*1024 {
		t.Fatalf("unexpected bounded log length: %d", len(long))
	}
}

func TestPluginDevSessionPublicCopyClonesSlices(t *testing.T) {
	logs := make([]pluginDevLog, pluginDevMaxLogs+10)
	for index := range logs {
		logs[index] = pluginDevLog{Level: "info", Message: fmt.Sprintf("log-%d", index)}
	}
	session := pluginDevSession{
		Capabilities: []string{"ui"},
		Manifest:     pluginDevManifestSummary{Platforms: []string{"darwin"}},
		Logs:         logs,
	}
	copy := session.publicCopy()
	if len(copy.Logs) != len(logs) {
		t.Fatalf("unexpected log count: %d", len(copy.Logs))
	}
	copy.Logs[0].Message = "changed"
	copy.Capabilities[0] = "changed"
	copy.Manifest.Platforms[0] = "changed"
	if session.Logs[0].Message == "changed" || session.Capabilities[0] == "changed" || session.Manifest.Platforms[0] == "changed" {
		t.Fatal("public copy shares slice storage")
	}
}

func TestSummarizePluginDevManifest(t *testing.T) {
	manifest := Manifest{
		APIVersion: 2, Platforms: []string{"darwin"}, Capabilities: []api.Capability{api.CapabilityUI},
		Runtime: ProcessRuntimeManifest{Type: "process", Entry: "bin/plugin"},
		Contributes: ManifestContributions{
			Commands: []ManifestCommand{{ID: "command"}},
			Views:    []ManifestView{{ID: "view"}},
			Actions:  []ManifestUIAction{{ID: "action"}},
		},
	}
	summary := summarizePluginDevManifest(manifest)
	capabilities := pluginDevCapabilities(manifest)
	if summary.Activation != pluginActivationOnDemand || summary.RuntimeEntry != "bin/plugin" || summary.CommandCount != 1 || summary.ViewCount != 1 || summary.ActionCount != 1 {
		t.Fatalf("unexpected manifest summary: %+v", summary)
	}
	if len(capabilities) != 1 || capabilities[0] != string(api.CapabilityUI) {
		t.Fatalf("unexpected capabilities: %+v", capabilities)
	}
}

func TestDevelopmentManifestPermissionExpansion(t *testing.T) {
	base := Manifest{
		Capabilities: []api.Capability{api.CapabilityCommands, api.CapabilityUI},
		Runtime:      ProcessRuntimeManifest{Type: "process", Entry: "bin/plugin"},
		Contributes: ManifestContributions{Actions: []ManifestUIAction{{
			ID: "read", Risk: "read", UICallable: true, Permissions: []api.Capability{},
		}}},
	}
	unchanged := base
	unchanged.Capabilities = append([]api.Capability(nil), base.Capabilities...)
	unchanged.Contributes.Actions = append([]ManifestUIAction(nil), base.Contributes.Actions...)
	if developmentManifestExpandsPermission(base, unchanged) {
		t.Fatal("unchanged manifest was classified as expansion")
	}
	capability := unchanged
	capability.Capabilities = append(capability.Capabilities, api.CapabilityShell)
	if !developmentManifestExpandsPermission(base, capability) {
		t.Fatal("new capability was not classified as expansion")
	}
	entry := unchanged
	entry.Runtime.Entry = "bin/other"
	if !developmentManifestExpandsPermission(base, entry) {
		t.Fatal("runtime entry change was not classified as expansion")
	}
	risk := unchanged
	risk.Contributes.Actions = []ManifestUIAction{{ID: "read", Risk: "write", UICallable: true, Permissions: []api.Capability{}}}
	if !developmentManifestExpandsPermission(base, risk) {
		t.Fatal("action risk expansion was not detected")
	}
	resultHandling := unchanged
	resultHandling.Contributes.Actions = []ManifestUIAction{{
		ID: "read", Risk: "write", UICallable: true, ResultHandling: "save-text-file", Permissions: []api.Capability{},
	}}
	if !developmentManifestExpandsPermission(base, resultHandling) {
		t.Fatal("host result handling expansion was not detected")
	}
}

func TestPluginDevStartRenewsExistingDiscovery(t *testing.T) {
	bridge := newPluginDevBridge()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	bridge.listener = listener
	bridge.discoveryPath = filepath.Join(t.TempDir(), "plugin-development.json")
	bridge.endpoint = "http://" + listener.Addr().String() + pluginDevBridgePrefix
	bridge.instanceToken = "test-token"
	if err := bridge.Start(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(bridge.discoveryPath)
	if err != nil {
		t.Fatal(err)
	}
	var discovery pluginDevDiscovery
	if err := json.Unmarshal(data, &discovery); err != nil {
		t.Fatal(err)
	}
	if time.Until(discovery.ExpiresAt) < 23*time.Hour || discovery.InstanceToken != bridge.instanceToken {
		t.Fatal("existing bridge discovery was not renewed")
	}
}

func TestPluginDevDiscoveryFileIsPrivate(t *testing.T) {
	bridge := newPluginDevBridge()
	bridge.discoveryPath = filepath.Join(t.TempDir(), "plugin-development.json")
	bridge.endpoint = "http://127.0.0.1:12345/plugin-dev/v1"
	bridge.instanceID = "instance"
	bridge.instanceToken = "token"
	if err := bridge.writeDiscoveryLocked(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(bridge.discoveryPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("expected 0600 discovery file, got %o", info.Mode().Perm())
	}
	contents, err := os.ReadFile(bridge.discoveryPath)
	if err != nil {
		t.Fatal(err)
	}
	var discovery pluginDevDiscovery
	if err := json.Unmarshal(contents, &discovery); err != nil {
		t.Fatal(err)
	}
	if discovery.ProtocolVersion != pluginDevProtocolVersion || discovery.InstanceToken != "token" {
		t.Fatalf("unexpected discovery payload: %+v", discovery)
	}
}

func TestPluginDevRequestBodyExactLimit(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"exact", `{}` + strings.Repeat(" ", pluginDevRequestMaxBytes-2), true},
		{"one_extra_whitespace", `{}` + strings.Repeat(" ", pluginDevRequestMaxBytes-1), false},
		{"oversize_json", `{"id":"` + strings.Repeat("x", pluginDevRequestMaxBytes) + `"}`, false},
		{"unknown", `{"remember":true}`, false},
		{"trailing", `{} {}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "http://127.0.0.1", strings.NewReader(tc.body))
			var value struct {
				ID string `json:"id"`
			}
			err := decodePluginDevRequest(r, &value)
			if (err == nil) != tc.valid {
				t.Fatalf("%d bytes valid=%v err=%v", len(tc.body), tc.valid, err)
			}
		})
	}
}

func TestPluginDevOtherEndpointsShareExactBodyLimit(t *testing.T) {
	for _, route := range []string{"build_error", "logs"} {
		t.Run(route, func(t *testing.T) {
			b := newPluginDevBridge()
			defer b.Close()
			session := &pluginDevSession{ID: "shared-limit", leaseToken: "lease", LastSeenAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)}
			b.sessions[session.ID] = session
			b.byLease[session.leaseToken] = session
			for _, extra := range []int{0, 1} {
				prefix := `{"message":"ok","level":"info"}`
				if route == "build_error" {
					prefix = `{"message":"ok"}`
				}
				body := prefix + strings.Repeat(" ", pluginDevRequestMaxBytes-len(prefix)+extra)
				r := httptest.NewRequest("POST", "http://127.0.0.1", strings.NewReader(body))
				r.RemoteAddr = "127.0.0.1:4567"
				r.Header.Set("Authorization", "Bearer lease")
				w := httptest.NewRecorder()
				if route == "logs" {
					b.handleLogs(w, r)
				} else {
					b.handleBuildError(w, r)
				}
				want := 202
				if extra > 0 {
					want = 400
				}
				if w.Code != want {
					t.Fatalf("%s size=%d got=%d want=%d", route, len(body), w.Code, want)
				}
			}
		})
	}
}
