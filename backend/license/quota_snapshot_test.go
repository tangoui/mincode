package license

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
	"time"
)

const quotaTestPayload = `{"schema_version":2,"product_scope":"mincode","account_id":7,"plan":"annual","grant_id":"42","policy_revision":3,"issued_at":1700000000,"expires_at":1700007200,"snapshot_expires_at":1700003600,"limits":{"max_devices":0,"max_active_workspaces":3,"max_windows_per_workspace":10},"signing_key_id":"test-key"}`

func testQuotaKey(t *testing.T) (*rsa.PrivateKey, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return key, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
}

func signedQuota(t *testing.T, key *rsa.PrivateKey, payload string) []byte {
	t.Helper()
	digest := sha256.Sum256([]byte(payload))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return []byte(strings.TrimSuffix(payload, "}") + `,"signature":"` + base64.StdEncoding.EncodeToString(sig) + `"}`)
}

func TestParseQuotaSnapshotRequiresExplicitLimitsAndSignature(t *testing.T) {
	key, _ := testQuotaKey(t)
	now := time.Unix(1700001800, 0)
	valid := signedQuota(t, key, quotaTestPayload)
	got, err := ParseQuotaSnapshot(valid, now)
	if err != nil || got.SchemaVersion != 2 || got.AccountID != 7 || got.Limits.MaxDevices != 0 || got.Limits.MaxActiveWorkspaces != 3 {
		t.Fatalf("unexpected parsed limits: %+v, %v", got.Limits, err)
	}
	for name, raw := range map[string][]byte{
		"missing limit":     signedQuota(t, key, strings.Replace(quotaTestPayload, `"max_windows_per_workspace":10`, "", 1)),
		"negative limit":    signedQuota(t, key, strings.Replace(quotaTestPayload, `"max_devices":0`, `"max_devices":-1`, 1)),
		"missing signature": []byte(quotaTestPayload),
		"missing account":   signedQuota(t, key, strings.Replace(quotaTestPayload, `"account_id":7,`, "", 1)),
		"null account":      signedQuota(t, key, strings.Replace(quotaTestPayload, `"account_id":7`, `"account_id":null`, 1)),
		"zero account":      signedQuota(t, key, strings.Replace(quotaTestPayload, `"account_id":7`, `"account_id":0`, 1)),
		"negative account":  signedQuota(t, key, strings.Replace(quotaTestPayload, `"account_id":7`, `"account_id":-7`, 1)),
		"string account":    signedQuota(t, key, strings.Replace(quotaTestPayload, `"account_id":7`, `"account_id":"7"`, 1)),
		"float account":     signedQuota(t, key, strings.Replace(quotaTestPayload, `"account_id":7`, `"account_id":7.0`, 1)),
		"overflow account":  signedQuota(t, key, strings.Replace(quotaTestPayload, `"account_id":7`, `"account_id":9223372036854775808`, 1)),
		"legacy v1":         signedQuota(t, key, strings.Replace(strings.Replace(quotaTestPayload, `"schema_version":2`, `"schema_version":1`, 1), `"account_id":7,`, "", 1)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseQuotaSnapshot(raw, now); err == nil {
				t.Fatal("accepted malformed snapshot")
			}
		})
	}
}

func TestParseQuotaSnapshotRejectsExpiryAndFutureIssuedAt(t *testing.T) {
	key, _ := testQuotaKey(t)
	raw := signedQuota(t, key, quotaTestPayload)
	for _, now := range []time.Time{time.Unix(1700004000, 0), time.Unix(1699998000, 0)} {
		if _, err := ParseQuotaSnapshot(raw, now); err == nil {
			t.Fatalf("accepted invalid time %v", now)
		}
	}
}

func TestParseQuotaSnapshotToleratesBoundedClockSkew(t *testing.T) {
	key, _ := testQuotaKey(t)
	raw := signedQuota(t, key, quotaTestPayload)
	// Local clock up to five minutes behind the server still accepts the snapshot.
	for _, now := range []time.Time{time.Unix(1700000000-1, 0), time.Unix(1700000000-quotaSnapshotMaxClockSkewSeconds, 0)} {
		s, err := ParseQuotaSnapshot(raw, now)
		if err != nil {
			t.Fatalf("rejected snapshot at skew %v: %v", 1700000000-now.Unix(), err)
		}
		// The absolute server expiry is kept; skew never lengthens the window.
		if s.SnapshotExpiresAt != 1700003600 {
			t.Fatalf("snapshot expiry = %d", s.SnapshotExpiresAt)
		}
	}
	if _, err := ParseQuotaSnapshot(raw, time.Unix(1700000000-quotaSnapshotMaxClockSkewSeconds-1, 0)); err == nil {
		t.Fatal("accepted issued_at beyond the allowed skew")
	}
	// Expiry is judged by the local clock against the server's absolute deadline.
	if _, err := ParseQuotaSnapshot(raw, time.Unix(1700003600, 0)); err == nil {
		t.Fatal("accepted snapshot at its expiry")
	}
}

func TestVerifyQuotaSnapshot(t *testing.T) {
	key, pub := testQuotaKey(t)
	now := time.Unix(1700001800, 0)
	raw := signedQuota(t, key, quotaTestPayload)
	got, err := VerifyQuotaSnapshot(raw, now, "test-key", pub, 7)
	if err != nil || got.AccountID != 7 || got.GrantID != "42" || got.Limits.MaxDevices != 0 {
		t.Fatalf("verified snapshot = %+v, %v", got, err)
	}

	// The signature authenticates the reconstructed PHP payload, not HTTP whitespace
	// or field ordering. Reformatting the envelope must leave verification intact.
	var formatted bytes.Buffer
	if err := json.Indent(&formatted, raw, "", "  "); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyQuotaSnapshot(formatted.Bytes(), now, "test-key", pub, 7); err != nil {
		t.Fatalf("formatted snapshot: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	var reordered bytes.Buffer
	reordered.WriteString(`{"signature":`)
	reordered.Write(fields["signature"])
	reordered.WriteByte(',')
	reordered.Write(raw[1 : len(raw)-1-len(fields["signature"])-len(`,"signature":`)])
	reordered.WriteByte('}')
	if _, err := VerifyQuotaSnapshot(reordered.Bytes(), now, "test-key", pub, 7); err != nil {
		t.Fatalf("reordered snapshot: %v", err)
	}
}

func TestVerifyQuotaSnapshotRequiresAccountBinding(t *testing.T) {
	key, pub := testQuotaKey(t)
	now := time.Unix(1700001800, 0)
	for name, tc := range map[string]struct {
		payload  string
		expected int64
		message  string
	}{
		"different signed account":  {strings.Replace(quotaTestPayload, `"account_id":7`, `"account_id":8`, 1), 7, "account ID does not match"},
		"invalid expected account":  {quotaTestPayload, 0, "expected quota account ID must be positive"},
		"negative expected account": {quotaTestPayload, -1, "expected quota account ID must be positive"},
		"v1 with account":           {strings.Replace(quotaTestPayload, `"schema_version":2`, `"schema_version":1`, 1), 7, "unsupported quota snapshot identity"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := VerifyQuotaSnapshot(signedQuota(t, key, tc.payload), now, "test-key", pub, tc.expected)
			if err == nil || got != (QuotaSnapshot{}) || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("accepted invalid account binding: %+v, %v", got, err)
			}
		})
	}
}

func TestVerifyQuotaSnapshotFreeAndPerpetual(t *testing.T) {
	key, pub := testQuotaKey(t)
	for name, payload := range map[string]string{
		"free":      strings.Replace(strings.Replace(quotaTestPayload, `"plan":"annual","grant_id":"42"`, `"plan":"free","grant_id":""`, 1), `"expires_at":1700007200`, `"expires_at":null`, 1),
		"perpetual": strings.Replace(strings.Replace(quotaTestPayload, `"plan":"annual"`, `"plan":"perpetual"`, 1), `"expires_at":1700007200`, `"expires_at":null`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := VerifyQuotaSnapshot(signedQuota(t, key, payload), time.Unix(1700001800, 0), "test-key", pub, 7)
			if err != nil || got.Plan != name || got.ExpiresAt != nil {
				t.Fatalf("verified %s = %+v, %v", name, got, err)
			}
		})
	}
}

func TestVerifyQuotaSnapshotRejectsInvalidTrustAndSignature(t *testing.T) {
	key, pub := testQuotaKey(t)
	raw := signedQuota(t, key, quotaTestPayload)
	now := time.Unix(1700001800, 0)
	other, otherPub := testQuotaKey(t)
	shortKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	shortDER, err := x509.MarshalPKIXPublicKey(&shortKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	shortPub := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: shortDER})
	for name, tc := range map[string]struct {
		raw []byte
		id  string
		pub []byte
	}{
		"missing pin":        {raw, "", pub},
		"wrong id":           {raw, "other", pub},
		"wrong key":          {raw, "test-key", otherPub},
		"short key":          {raw, "test-key", shortPub},
		"wrong signature":    {signedQuota(t, other, quotaTestPayload), "test-key", pub},
		"malformed key":      {raw, "test-key", []byte("not a PEM key")},
		"missing key":        {raw, "test-key", nil},
		"tampered plan":      {bytes.Replace(raw, []byte(`"annual"`), []byte(`"free"`), 1), "test-key", pub},
		"tampered limit":     {bytes.Replace(raw, []byte(`"max_devices":0`), []byte(`"max_devices":100`), 1), "test-key", pub},
		"tampered account":   {bytes.Replace(raw, []byte(`"account_id":7`), []byte(`"account_id":8`), 1), "test-key", pub},
		"tampered signature": {bytes.Replace(raw, []byte(`"signature":"`), []byte(`"signature":"!`), 1), "test-key", pub},
		"unsigned":           {bytes.Replace(raw, fieldsSignature(raw), []byte(`"signature":""`), 1), "test-key", pub},
		"expired snapshot":   {raw, "test-key", pub},
	} {
		t.Run(name, func(t *testing.T) {
			at := now
			if name == "expired snapshot" {
				at = time.Unix(1700003600, 0)
			}
			if got, err := VerifyQuotaSnapshot(tc.raw, at, tc.id, tc.pub, 7); err == nil || got != (QuotaSnapshot{}) {
				t.Fatalf("accepted invalid snapshot: %+v, %v", got, err)
			}
		})
	}
}

func fieldsSignature(raw []byte) []byte {
	start := bytes.Index(raw, []byte(`"signature":`))
	return raw[start : len(raw)-1]
}

func TestVerifyQuotaSnapshotRejectsMalformedJSONAndTimes(t *testing.T) {
	key, pub := testQuotaKey(t)
	now := time.Unix(1700001800, 0)
	cases := map[string]string{
		"duplicate field":           strings.Replace(quotaTestPayload, `"plan":"annual"`, `"plan":"annual","plan":"annual"`, 1),
		"duplicate account":         strings.Replace(quotaTestPayload, `"account_id":7`, `"account_id":7,"account_id":7`, 1),
		"escaped duplicate account": strings.Replace(quotaTestPayload, `"account_id":7`, `"account_id":7,"account_\u0069d":7`, 1),
		"escaped duplicate key":     strings.Replace(quotaTestPayload, `"plan":"annual"`, `"plan":"annual","pl\u0061n":"annual"`, 1),
		"duplicate limit":           strings.Replace(quotaTestPayload, `"max_devices":0`, `"max_devices":0,"max_devices":0`, 1),
		"unknown field":             strings.Replace(quotaTestPayload, `"plan":"annual"`, `"unknown":1,"plan":"annual"`, 1),
		"unknown limit":             strings.Replace(quotaTestPayload, `"max_devices":0`, `"extra":0,"max_devices":0`, 1),
		"missing expiry":            strings.Replace(quotaTestPayload, `"expires_at":1700007200,`, "", 1),
		"null limit":                strings.Replace(quotaTestPayload, `"max_devices":0`, `"max_devices":null`, 1),
		"float":                     strings.Replace(quotaTestPayload, `"max_devices":0`, `"max_devices":0.0`, 1),
		"integer overflow":          strings.Replace(quotaTestPayload, `"max_devices":0`, `"max_devices":18446744073709551616`, 1),
		"negative":                  strings.Replace(quotaTestPayload, `"max_devices":0`, `"max_devices":-1`, 1),
		"escaped plan":              strings.Replace(quotaTestPayload, `"annual"`, `"annu\u0061l"`, 1),
		"bad scope":                 strings.Replace(quotaTestPayload, `"mincode"`, `"other"`, 1),
		"too long":                  strings.Replace(quotaTestPayload, `"snapshot_expires_at":1700003600`, `"snapshot_expires_at":1700003601`, 1),
		"grant expired":             strings.Replace(quotaTestPayload, `"expires_at":1700007200`, `"expires_at":1700001800`, 1),
		"trailing":                  quotaTestPayload + ` {}`,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := VerifyQuotaSnapshot(signedQuota(t, key, payload), now, "test-key", pub, 7); err == nil {
				t.Fatal("accepted invalid signed JSON")
			}
		})
	}
	for name, at := range map[string]time.Time{
		"before issue beyond skew": time.Unix(1700000000-quotaSnapshotMaxClockSkewSeconds-1, 0),
		"grant expiry":             time.Unix(1700007200, 0),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := VerifyQuotaSnapshot(signedQuota(t, key, quotaTestPayload), at, "test-key", pub, 7); err == nil {
				t.Fatal("accepted invalid time")
			}
		})
	}
}

const quotaTestPayloadV3 = `{"schema_version":3,"product_scope":"mincode","account_id":7,"plan":"annual","grant_id":"42","policy_revision":3,"issued_at":1700000000,"expires_at":1700007200,"snapshot_expires_at":1700003600,"limits":{"max_devices":0,"max_active_workspaces":3,"max_windows_per_workspace":10,"max_enabled_plugins":2,"allow_plugins":true},"signing_key_id":"test-key"}`

func TestQuotaSnapshotSchemaThreeCarriesPluginLimit(t *testing.T) {
	key, publicPEM := testQuotaKey(t)
	now := time.Unix(1700001800, 0)
	got, err := VerifyQuotaSnapshot(signedQuota(t, key, quotaTestPayloadV3), now, "test-key", publicPEM, 7)
	if err != nil || got.SchemaVersion != 3 || got.Limits.MaxEnabledPlugins != 2 || got.Limits.MaxWindowsPerWorkspace != 10 {
		t.Fatalf("schema 3 snapshot = %+v, %v", got, err)
	}
	v2, err := VerifyQuotaSnapshot(signedQuota(t, key, quotaTestPayload), now, "test-key", publicPEM, 7)
	if err != nil || v2.Limits.MaxEnabledPlugins != 0 {
		t.Fatalf("schema 2 snapshot must keep working with an unlimited plugin quota: %+v, %v", v2.Limits, err)
	}
	for name, payload := range map[string]string{
		"v2 with plugin limit":     strings.Replace(quotaTestPayload, `"max_windows_per_workspace":10`, `"max_windows_per_workspace":10,"max_enabled_plugins":2`, 1),
		"v3 without plugin limit":  strings.Replace(quotaTestPayloadV3, `,"max_enabled_plugins":2`, "", 1),
		"v3 without plugin switch": strings.Replace(quotaTestPayloadV3, `,"allow_plugins":true`, "", 1),
		"v3 numeric plugin switch": strings.Replace(quotaTestPayloadV3, `"allow_plugins":true`, `"allow_plugins":1`, 1),
		"v3 plugin limit overflow": strings.Replace(quotaTestPayloadV3, `"max_enabled_plugins":2`, `"max_enabled_plugins":4294967296`, 1),
		"v3 negative plugin limit": strings.Replace(quotaTestPayloadV3, `"max_enabled_plugins":2`, `"max_enabled_plugins":-1`, 1),
		"unknown schema 4":         strings.Replace(quotaTestPayloadV3, `"schema_version":3`, `"schema_version":4`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseQuotaSnapshot(signedQuota(t, key, payload), now); err == nil {
				t.Fatal("accepted malformed snapshot")
			}
		})
	}
	disabled, err := VerifyQuotaSnapshot(signedQuota(t, key, strings.Replace(quotaTestPayloadV3, `"allow_plugins":true`, `"allow_plugins":false`, 1)),
		now, "test-key", publicPEM, 7)
	if err != nil || !disabled.Limits.PluginsDisabled || got.Limits.PluginsDisabled {
		t.Fatalf("plugin switch decoded incorrectly: allowed=%+v disabled=%+v err=%v", got.Limits, disabled.Limits, err)
	}
	switched := strings.Replace(string(signedQuota(t, key, quotaTestPayloadV3)), `"allow_plugins":true`, `"allow_plugins":false`, 1)
	if _, err := VerifyQuotaSnapshot([]byte(switched), now, "test-key", publicPEM, 7); err == nil {
		t.Fatal("accepted a snapshot whose plugin switch was changed after signing")
	}
	// Relabeling a schema 3 payload as schema 2 must not verify, and vice versa.
	tampered := strings.Replace(string(signedQuota(t, key, quotaTestPayloadV3)), `"max_enabled_plugins":2`, `"max_enabled_plugins":0`, 1)
	if _, err := VerifyQuotaSnapshot([]byte(tampered), now, "test-key", publicPEM, 7); err == nil {
		t.Fatal("accepted a snapshot whose plugin limit was changed after signing")
	}
}

// The offline cache stores the parsed struct and re-verifies its marshaled form.
func TestQuotaSnapshotCacheRoundTripPreservesSchemaShape(t *testing.T) {
	key, publicPEM := testQuotaKey(t)
	now := time.Unix(1700001800, 0)
	for _, payload := range []string{quotaTestPayload, quotaTestPayloadV3, strings.Replace(quotaTestPayloadV3, `"allow_plugins":true`, `"allow_plugins":false`, 1)} {
		parsed, err := VerifyQuotaSnapshot(signedQuota(t, key, payload), now, "test-key", publicPEM, 7)
		if err != nil {
			t.Fatal(err)
		}
		cached, err := json.Marshal(parsed)
		if err != nil {
			t.Fatal(err)
		}
		if parsed.SchemaVersion == 2 && bytes.Contains(cached, []byte("max_enabled_plugins")) {
			t.Fatalf("schema 2 cache gained a field it was never signed with: %s", cached)
		}
		var decoded QuotaSnapshot
		if err := json.Unmarshal(cached, &decoded); err != nil {
			t.Fatal(err)
		}
		again, err := json.Marshal(decoded)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyQuotaSnapshot(again, now, "test-key", publicPEM, 7); err != nil {
			t.Fatalf("schema %d snapshot failed re-verification after a cache round trip: %v", parsed.SchemaVersion, err)
		}
	}
}

func TestEvaluateQuotaRefusesAllPluginsWhenPlanForbidsThem(t *testing.T) {
	for _, current := range []int64{0, 5} {
		decision, err := EvaluateQuota(QuotaPolicy{PluginsDisabled: true}, QuotaPlugin, current, 1)
		if err != nil || decision.Allowed || decision.Code != "plugins_not_allowed" {
			t.Fatalf("current=%d: %+v, %v", current, decision, err)
		}
	}
	if decision, err := EvaluateQuota(QuotaPolicy{PluginsDisabled: true}, QuotaWindow, 1, 1); err != nil || !decision.Allowed {
		t.Fatalf("plugin switch must not affect other resources: %+v, %v", decision, err)
	}
}
