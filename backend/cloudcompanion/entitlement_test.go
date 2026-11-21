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
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRedeemMemberCodeContract(t *testing.T) {
	root := t.TempDir()
	previousRoot := identityRootDir
	identityRootDir = func() string { return root }
	t.Cleanup(func() { identityRootDir = previousRoot })
	const code = "ABCD-1234-EFGH-5678"
	requests := 0
	fail := false
	switchAccount := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if strings.Contains(r.URL.String(), code) {
			t.Error("redemption code leaked into URL")
		}
		switch r.URL.Path {
		case "/api/common/refreshToken":
			if r.Method != http.MethodPost || r.PostFormValue("refreshToken") != "refresh" {
				t.Error("invalid refresh request")
			}
			_, _ = w.Write([]byte(`{"code":1,"data":{"token":"fresh-token"}}`))
		case "/api/mincode/entitlement/redeem":
			if r.Method != http.MethodPost || r.Header.Get("ba-user-token") != "fresh-token" ||
				r.PostFormValue("code") != code || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
				t.Error("invalid redemption request")
			}
			if switchAccount {
				if err := writeSession(persistedSession{APIBaseURL: "http://" + r.Host, AccountID: 99, AccessToken: "other", RefreshToken: "other", AuthProvider: "member"}); err != nil {
					t.Error(err)
				}
			}
			if fail {
				_, _ = w.Write([]byte(`{"code":0,"msg":"failed","data":{"grant_id":"123"}}`))
			} else {
				_, _ = w.Write([]byte(`{"code":1,"data":{"grant_id":"123"}}`))
			}
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
		}
	}))
	defer server.Close()
	if _, err := redeemMemberCode(context.Background(), code, 42); err == nil || requests != 0 {
		t.Fatalf("unsigned-in redemption: %v (requests %d)", err, requests)
	}
	write := func() {
		t.Helper()
		if err := writeSession(persistedSession{APIBaseURL: server.URL, AccountID: 42, AccessToken: "old", RefreshToken: "refresh", AuthProvider: "member"}); err != nil {
			t.Fatal(err)
		}
	}
	write()
	if _, err := redeemMemberCode(context.Background(), code, 43); err == nil || requests != 0 {
		t.Fatalf("stale displayed account sent redemption: %v (requests %d)", err, requests)
	}
	if _, err := redeemMemberCode(context.Background(), "invalid", 42); err == nil || requests != 0 {
		t.Fatalf("invalid code sent: %v (requests %d)", err, requests)
	}
	result, err := redeemMemberCode(context.Background(), code, 42)
	if err != nil || result.GrantID != "123" || requests != 2 {
		t.Fatalf("redeem: %+v %v (requests %d)", result, err, requests)
	}
	fail = true
	if _, err := redeemMemberCode(context.Background(), code, 42); err == nil {
		t.Fatal("failed envelope accepted")
	}
	fail = false
	switchAccount = true
	if _, err := redeemMemberCode(context.Background(), code, 42); err == nil || !strings.Contains(err.Error(), "已切换") {
		t.Fatalf("switched account accepted: %v", err)
	}
}

func TestCloudEntitlementSnapshotHandlerFailsClosed(t *testing.T) {
	root := t.TempDir()
	previousRoot := identityRootDir
	identityRootDir = func() string { return root }
	t.Cleanup(func() { identityRootDir = previousRoot })
	t.Setenv("MINCODE_DEV_QUOTA_TRUST_FILE", filepath.Join(root, "missing-trust.json"))
	previousID, previousKey := QuotaSnapshotKeyID, QuotaSnapshotPublicKeyBase64
	t.Cleanup(func() { QuotaSnapshotKeyID, QuotaSnapshotPublicKeyBase64 = previousID, previousKey })
	QuotaSnapshotKeyID, QuotaSnapshotPublicKeyBase64 = "", ""
	call := func(account int64) snapshotResponse {
		t.Helper()
		result, err := cloudEntitlementSnapshotHandler("", fmt.Sprintf(`{"expectedAccountId":%d}`, account))
		if err != nil {
			t.Fatal(err)
		}
		var response snapshotResponse
		if err := json.Unmarshal([]byte(result), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	if result := call(42); result.Status != "signed_out" || result.Snapshot != nil {
		t.Fatalf("signed out: %+v", result)
	}
	if err := writeSession(persistedSession{APIBaseURL: "https://example.com", AccountID: 42, AccessToken: "access", RefreshToken: "refresh", AuthProvider: "member"}); err != nil {
		t.Fatal(err)
	}
	if result := call(43); result.Status != "failed" || result.Snapshot != nil {
		t.Fatalf("wrong displayed account: %+v", result)
	}
	if result := call(42); result.Status != "unconfigured" || result.Snapshot != nil {
		t.Fatalf("missing pinned key: %+v", result)
	}
	QuotaSnapshotKeyID, QuotaSnapshotPublicKeyBase64 = "release", "not-base64"
	if result := call(42); result.Status != "unconfigured" || result.Snapshot != nil {
		t.Fatalf("invalid pinned key: %+v", result)
	}
	QuotaSnapshotPublicKeyBase64 = base64.StdEncoding.EncodeToString([]byte("not an RSA public key"))
	if result := call(42); result.Status != "unconfigured" || result.Snapshot != nil {
		t.Fatalf("malformed RSA key: %+v", result)
	}
}

func TestFetchMemberQuotaSnapshotAuthenticatedAndAccountBound(t *testing.T) {
	root := t.TempDir()
	previousRoot := identityRootDir
	identityRootDir = func() string { return root }
	t.Cleanup(func() { identityRootDir = previousRoot })
	key, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	trust := QuotaTrust{KeyID: "test-key", PublicKeyPEM: pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})}
	account := int64(42)
	requests := 0
	var switchSession bool
	var redirect, tamper, expired bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch r.URL.Path {
		case "/api/common/refreshToken":
			if r.Method != http.MethodPost || r.PostFormValue("refreshToken") != "refresh" {
				t.Errorf("unexpected refresh request")
			}
			_, _ = w.Write([]byte(`{"code":1,"data":{"token":"new-access"}}`))
		case "/api/mincode/entitlement/snapshot":
			if redirect {
				http.Redirect(w, r, "/api/other", http.StatusFound)
				return
			}
			if r.Method != http.MethodGet || r.Header.Get("ba-user-token") != "new-access" {
				t.Errorf("unexpected snapshot credentials")
			}
			if switchSession {
				if err := writeSession(persistedSession{APIBaseURL: "http://" + r.Host, AccountID: 99, AccessToken: "other", RefreshToken: "other-refresh", AuthProvider: "member"}); err != nil {
					t.Error(err)
				}
			}
			issued := time.Now().Unix() - 1
			if expired {
				issued -= 3600
			}
			payload := fmt.Sprintf(`{"schema_version":2,"product_scope":"mincode","account_id":%d,"plan":"free","grant_id":"","policy_revision":1,"issued_at":%d,"expires_at":null,"snapshot_expires_at":%d,"limits":{"max_devices":1,"max_active_workspaces":2,"max_windows_per_workspace":3},"signing_key_id":"test-key"}`, account, issued, issued+1800)
			digest := sha256.Sum256([]byte(payload))
			sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
			if err != nil {
				t.Error(err)
				return
			}
			if tamper {
				payload = strings.Replace(payload, `"max_devices":1`, `"max_devices":9`, 1)
			}
			_, _ = fmt.Fprintf(w, `{"code":1,"data":%s,"signature":"%s"}}`, strings.TrimSuffix(payload, "}"), base64.StdEncoding.EncodeToString(sig))
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
		}
	}))
	defer server.Close()
	write := func() {
		t.Helper()
		if err := writeSession(persistedSession{APIBaseURL: server.URL, AccountID: 42, AccessToken: "old", RefreshToken: "refresh", AuthProvider: "member"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := FetchMemberQuotaSnapshot(context.Background(), trust); err == nil || requests != 0 {
		t.Fatalf("unsigned-in session requested snapshot: %v, %d", err, requests)
	}
	write()
	if _, err := FetchMemberQuotaSnapshot(context.Background(), QuotaTrust{}); err == nil || requests != 0 {
		t.Fatalf("untrusted request: %v, %d", err, requests)
	}
	got, err := FetchMemberQuotaSnapshot(context.Background(), trust)
	if err != nil || got.AccountID != 42 || got.Limits.MaxDevices != 1 {
		t.Fatalf("snapshot: %+v %v", got, err)
	}
	oldID, oldKey := QuotaSnapshotKeyID, QuotaSnapshotPublicKeyBase64
	t.Cleanup(func() { QuotaSnapshotKeyID, QuotaSnapshotPublicKeyBase64 = oldID, oldKey })
	QuotaSnapshotKeyID, QuotaSnapshotPublicKeyBase64 = trust.KeyID, base64.StdEncoding.EncodeToString(trust.PublicKeyPEM)
	result, err := cloudEntitlementSnapshotHandler("", `{"expectedAccountId":42}`)
	if err != nil {
		t.Fatal(err)
	}
	var display snapshotResponse
	if err := json.Unmarshal([]byte(result), &display); err != nil {
		t.Fatal(err)
	}
	if display.Status != "verified" || display.Snapshot == nil || display.Snapshot.AccountID != 42 || display.Snapshot.Limits.MaxDevices != 1 {
		t.Fatalf("verified handler response: %+v", display)
	}
	account = 43
	if _, err := FetchMemberQuotaSnapshot(context.Background(), trust); err == nil || !strings.Contains(err.Error(), "account ID") {
		t.Fatalf("cross-account snapshot accepted: %v", err)
	}
	account = 42
	tamper = true
	if _, err := FetchMemberQuotaSnapshot(context.Background(), trust); err == nil {
		t.Fatal("tampered limits accepted")
	}
	tamper = false
	expired = true
	if _, err := FetchMemberQuotaSnapshot(context.Background(), trust); err == nil {
		t.Fatal("expired snapshot accepted")
	}
	expired = false
	redirect = true
	if _, err := FetchMemberQuotaSnapshot(context.Background(), trust); err == nil {
		t.Fatal("redirect accepted")
	}
	redirect = false
	switchSession = true
	if _, err := FetchMemberQuotaSnapshot(context.Background(), trust); err == nil || !strings.Contains(err.Error(), "已切换") {
		t.Fatalf("switched session accepted: %v", err)
	}
	write()
	if _, err := FetchMemberQuotaSnapshot(context.Background(), QuotaTrust{KeyID: "wrong-key", PublicKeyPEM: trust.PublicKeyPEM}); err == nil {
		t.Fatal("wrong pinned key ID accepted")
	}
}
