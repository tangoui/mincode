package cloudcompanion

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mincode/app/backend/license"
)

func TestResolveEffectiveQuota(t *testing.T) {
	root := t.TempDir()
	previousRoot, previousNow := identityRootDir, cloudNow
	previousID, previousKey := QuotaSnapshotKeyID, QuotaSnapshotPublicKeyBase64
	identityRootDir = func() string { return root }
	t.Cleanup(func() {
		identityRootDir, cloudNow = previousRoot, previousNow
		QuotaSnapshotKeyID, QuotaSnapshotPublicKeyBase64 = previousID, previousKey
	})
	key, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	QuotaSnapshotKeyID = "test-key"
	QuotaSnapshotPublicKeyBase64 = base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))

	issued := time.Now().Unix() - 1
	expires := issued + 30*86400
	snapshotDown, freeDown, deviceFull := false, false, false
	freeMode := "signed" // signed | unsigned | forged | replayed
	replayedNonce := strings.Repeat("ab", 16)
	signFree := func(nonce string) string {
		payload := fmt.Sprintf(`{"schema_version":1,"kind":"free_policy","product_scope":"mincode","plan":"free","policy_revision":4,"nonce":"%s","issued_at":%d,"limits":{"max_devices":1,"max_active_workspaces":3,"max_windows_per_workspace":7,"max_enabled_plugins":0,"allow_plugins":true},"signing_key_id":"test-key"}`, nonce, time.Now().Unix())
		digest := sha256.Sum256([]byte(payload))
		signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
		if err != nil {
			t.Error(err)
		}
		return fmt.Sprintf(`%s,"signature":"%s"}`, strings.TrimSuffix(payload, "}"), base64.StdEncoding.EncodeToString(signature))
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/common/refreshToken":
			_, _ = w.Write([]byte(`{"code":1,"data":{"token":"access"}}`))
		case "/api/mincode/entitlement/snapshot":
			if !strings.HasPrefix(r.URL.Query().Get("device_id"), "desktop-") || r.URL.Query().Get("platform") == "" {
				t.Errorf("snapshot request did not identify this device: %s", r.URL.RawQuery)
			}
			if snapshotDown {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			if deviceFull {
				_, _ = w.Write([]byte(`{"code":0,"msg":"Device limit reached","data":{"reason":"device_limit","current":3,"limit":3}}`))
				return
			}
			payload := fmt.Sprintf(`{"schema_version":2,"product_scope":"mincode","account_id":42,"plan":"annual","grant_id":"5","policy_revision":2,"issued_at":%d,"expires_at":%d,"snapshot_expires_at":%d,"limits":{"max_devices":3,"max_active_workspaces":20,"max_windows_per_workspace":20},"signing_key_id":"test-key"}`, issued, expires, issued+1800)
			digest := sha256.Sum256([]byte(payload))
			signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
			if err != nil {
				t.Error(err)
				return
			}
			_, _ = fmt.Fprintf(w, `{"code":1,"data":%s,"signature":"%s"}}`, strings.TrimSuffix(payload, "}"), base64.StdEncoding.EncodeToString(signature))
		case "/api/mincode/quotaPolicy/free":
			if freeDown {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			attestation := ""
			switch freeMode {
			case "signed":
				attestation = `,"attestation":` + signFree(r.URL.Query().Get("nonce"))
			case "forged":
				attestation = `,"attestation":` + strings.Replace(signFree(r.URL.Query().Get("nonce")), `"max_active_workspaces":3`, `"max_active_workspaces":0`, 1)
			case "replayed":
				attestation = `,"attestation":` + signFree(replayedNonce)
			}
			_, _ = fmt.Fprintf(w, `{"code":1,"data":{"plan":"free","limits":{"max_devices":0,"max_active_workspaces":0,"max_windows_per_workspace":0}%s,"authorization_usable":false}}`, attestation)
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
		}
	}))
	defer server.Close()
	t.Setenv("MINCODE_CLOUD_SERVER_URL", server.URL)
	at := func(offset time.Duration) { cloudNow = func() time.Time { return time.Now().Add(offset) } }
	resolve := func() EffectiveQuota { return resolveEffectiveQuota(context.Background()) }

	at(0)
	if got := resolve(); got.Source != "free" || got.Reason != "signed_out" || got.Fallback || got.Limits == nil || got.Limits.MaxWindowsPerWorkspace != 7 {
		t.Fatalf("signed out: %+v", got)
	}
	// With a pinned trust root the unsigned limits (all 0 = unlimited here) are never used.
	for _, mode := range []string{"unsigned", "forged", "replayed"} {
		freeMode = mode
		got := resolve()
		if got.Source != "free" || got.Limits == nil || got.Limits.MaxActiveWorkspaces != 3 || got.Limits.MaxWindowsPerWorkspace != 7 {
			t.Fatalf("%s free policy must leave the last signed one: %+v", mode, got)
		}
	}
	updateQuotaCache(func(cache *quotaCache) { cache.Free.Limits.MaxActiveWorkspaces = 0 })
	if got := resolve(); got.Limits.MaxActiveWorkspaces != 3 {
		t.Fatalf("a cache with edited limits must use the signed ones: %+v", got)
	}
	updateQuotaCache(func(cache *quotaCache) { cache.Free.Attestation = nil })
	if got := resolve(); !got.Fallback || got.Limits.MaxActiveWorkspaces != 3 || got.Limits.MaxWindowsPerWorkspace != 6 {
		t.Fatalf("an unsigned cached policy must give way to the fallback: %+v", got)
	}
	freeMode = "signed"
	if err := writeSession(persistedSession{APIBaseURL: server.URL, AccountID: 42, AccessToken: "old", RefreshToken: "refresh", AuthProvider: "member"}); err != nil {
		t.Fatal(err)
	}
	if got := resolve(); got.Source != "verified" || got.Plan != "annual" || got.Limits.MaxActiveWorkspaces != 20 {
		t.Fatalf("online: %+v", got)
	}

	deviceFull = true
	got := resolve()
	if got.Source != "free" || got.Reason != "device_limit" || got.DeviceLimit == nil || got.DeviceLimit.Limit != 3 || got.Limits.MaxActiveWorkspaces != 3 {
		t.Fatalf("refused device must use free limits: %+v", got)
	}
	deviceFull, snapshotDown = false, true
	if got := resolve(); got.Source == "offline" {
		t.Fatalf("a device the cloud refused must not fall back to the cached paid snapshot: %+v", got)
	}
	snapshotDown = false
	if got := resolve(); got.Source != "verified" {
		t.Fatalf("re-admitted: %+v", got)
	}

	snapshotDown = true
	at(3 * 24 * time.Hour)
	if got := resolve(); got.Source != "offline" {
		t.Fatalf("offline: %+v", got)
	}
	at(3*24*time.Hour - 2*time.Hour)
	if got := resolve(); got.Source != "free" || got.Reason != "clock_rollback" || got.Limits.MaxActiveWorkspaces != 3 {
		t.Fatalf("a clock set back must not keep the offline grace: %+v", got)
	}
	at(6 * 24 * time.Hour)
	got = resolve()
	if got.Source != "offline" || got.Limits.MaxWindowsPerWorkspace != 20 || got.OfflineUntil != issued+int64(quotaOfflineGrace/time.Second) {
		t.Fatalf("offline within grace: %+v", got)
	}
	at(8 * 24 * time.Hour)
	if got := resolve(); got.Source != "free" || got.Reason != "offline_expired" || got.Limits.MaxActiveWorkspaces != 3 {
		t.Fatalf("offline past grace: %+v", got)
	}

	expires = issued + 86400
	snapshotDown = false
	at(0)
	if got := resolve(); got.Source != "verified" {
		t.Fatalf("refresh with short grant: %+v", got)
	}
	snapshotDown = true
	at(2 * 24 * time.Hour)
	if got := resolve(); got.Source != "free" || got.Reason != "expired" {
		t.Fatalf("expired grant must fall back to free: %+v", got)
	}

	freeDown = true
	if got := resolve(); got.Source != "free" || got.Fallback || got.Limits == nil || got.Limits.MaxWindowsPerWorkspace != 7 {
		t.Fatalf("cached free policy: %+v", got)
	}
	updateQuotaCache(func(cache *quotaCache) { cache.Free = nil })
	if got := resolve(); got.Source != "free" || !got.Fallback || got.Limits == nil || got.Limits.MaxActiveWorkspaces != 3 {
		t.Fatalf("no reachable policy must fall back to the built-in free limits: %+v", got)
	}

	if err := writeSession(persistedSession{APIBaseURL: server.URL, AccountID: 43, AccessToken: "old", RefreshToken: "refresh", AuthProvider: "member"}); err != nil {
		t.Fatal(err)
	}
	freeDown = false
	at(0)
	if got := resolve(); got.Source == "offline" || got.Source == "verified" {
		t.Fatalf("another account's cached entitlement was used: %+v", got)
	}
}

func TestQuotaCacheRejectsTampering(t *testing.T) {
	root := t.TempDir()
	previousRoot, previousNow := identityRootDir, cloudNow
	identityRootDir, cloudNow = func() string { return root }, time.Now
	t.Cleanup(func() { identityRootDir, cloudNow = previousRoot, previousNow })
	t.Setenv("MINCODE_CLOUD_SERVER_URL", "http://127.0.0.1:1")
	path, err := quotaCachePath()
	if err != nil {
		t.Fatal(err)
	}
	unlimited := &freeQuotaCache{APIBaseURL: "http://127.0.0.1:1", FetchedAt: time.Now().Unix()}

	updateQuotaCache(func(cache *quotaCache) { cache.Free = unlimited })
	sealed, err := os.ReadFile(path)
	if err != nil || !strings.HasPrefix(string(sealed), sealedStoragePrefix) || strings.Contains(string(sealed), "free") {
		t.Fatalf("cache must be sealed on disk: %q %v", sealed, err)
	}
	if cache := readQuotaCache(); cache.Free == nil {
		t.Fatal("sealed cache must read back")
	}

	flipped := append([]byte{}, sealed...)
	flipped[len(flipped)-3] ^= 1
	plain, _ := json.Marshal(quotaCache{Free: unlimited})
	for name, raw := range map[string][]byte{"edited": flipped, "plaintext": plain, "garbage": []byte(sealedStoragePrefix + "AAAA")} {
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if cache := readQuotaCache(); cache.Free != nil || cache.Member != nil {
			t.Fatalf("%s cache was trusted: %+v", name, cache)
		}
		if got := resolveEffectiveQuota(context.Background()); got.Source != "free" || !got.Fallback || got.Limits.MaxActiveWorkspaces != 3 {
			t.Fatalf("%s cache must leave the fallback free limits: %+v", name, got)
		}
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got := resolveEffectiveQuota(context.Background()); got.Source != "free" || !got.Fallback || got.Limits == nil {
		t.Fatalf("a deleted cache must not lift limits: %+v", got)
	}
}

func TestLegacyQuotaCacheKeepsOnlySignedSnapshot(t *testing.T) {
	root := t.TempDir()
	previousRoot := identityRootDir
	identityRootDir = func() string { return root }
	t.Cleanup(func() { identityRootDir = previousRoot })
	path, err := quotaCachePath()
	if err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(filepath.Dir(path), legacyQuotaCacheFileName)
	raw, _ := json.Marshal(quotaCache{
		MemberAPIBaseURL: "https://cloud.example",
		Member:           &license.QuotaSnapshot{AccountID: 7, Plan: "annual"},
		Free:             &freeQuotaCache{APIBaseURL: "https://cloud.example"},
		LastSeen:         1,
	})
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cache := readQuotaCache()
	if cache.Member == nil || cache.Member.AccountID != 7 || cache.Free != nil || cache.LastSeen != 0 {
		t.Fatalf("legacy cache: %+v", cache)
	}
	updateQuotaCache(func(*quotaCache) {})
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy cache must be removed once sealed: %v", err)
	}
	if cache := readQuotaCache(); cache.Member == nil || cache.Member.AccountID != 7 {
		t.Fatalf("migrated snapshot lost: %+v", cache)
	}
}

func TestClockRollbackRejectsCachedSnapshot(t *testing.T) {
	root := t.TempDir()
	previousRoot := identityRootDir
	identityRootDir = func() string { return root }
	t.Cleanup(func() { identityRootDir = previousRoot })
	now := time.Now()
	if observeClock(now) {
		t.Fatal("first observation cannot be a rollback")
	}
	if observeClock(now.Add(-30*time.Minute)) || observeClock(now.Add(10*time.Second)) {
		t.Fatal("small drift must be tolerated")
	}
	if !observeClock(now.Add(-2 * time.Hour)) {
		t.Fatal("a clock set back hours must be detected")
	}
	if cache := readQuotaCache(); cache.LastSeen != now.Unix() {
		t.Fatalf("the rolled-back reading must not lower the record: %d", cache.LastSeen)
	}
}

func TestFallbackFreeQuota(t *testing.T) {
	previous := FallbackFreeQuota
	t.Cleanup(func() { FallbackFreeQuota = previous })
	FallbackFreeQuota = "max_devices=2, max_active_workspaces=5,max_windows_per_workspace=8,max_enabled_plugins=4,allow_plugins=0"
	if got := fallbackFreePolicy(); got.MaxDevices != 2 || got.MaxActiveWorkspaces != 5 || got.MaxWindowsPerWorkspace != 8 || got.MaxEnabledPlugins != 4 || !got.PluginsDisabled {
		t.Fatalf("injected policy: %+v", got)
	}
	for _, bad := range []string{"", "max_devices=1", "max_devices=1,max_active_workspaces=3,max_windows_per_workspace=6,max_enabled_plugins=0,allow_plugins=2",
		"max_devices=-1,max_active_workspaces=3,max_windows_per_workspace=6,max_enabled_plugins=0,allow_plugins=1",
		"max_devices=1,max_active_workspaces=3,max_windows_per_workspace=6,max_enabled_plugins=0,allow_plugins=1,extra=1"} {
		FallbackFreeQuota = bad
		if got := fallbackFreePolicy(); got.MaxDevices != 1 || got.MaxActiveWorkspaces != 3 || got.MaxWindowsPerWorkspace != 6 || got.MaxEnabledPlugins != 6 || got.PluginsDisabled {
			t.Fatalf("%q must fall back to the default: %+v", bad, got)
		}
	}
}
