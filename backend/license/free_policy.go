package license

import (
	"bytes"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
)

// FreePolicyAttestation is the cloud's signature over the public free policy
// for one request. The desktop picks the nonce, so a captured attestation only
// verifies for the request it answered and a fake cloud cannot mint one.
type FreePolicyAttestation struct {
	PolicyRevision int64
	Nonce          string
	IssuedAt       int64
	Limits         QuotaPolicy
	SigningKeyID   string
	Signature      string
}

var freePolicyNoncePattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

var freePolicyFields = []string{"schema_version", "kind", "product_scope", "plan", "policy_revision", "nonce", "issued_at", "limits", "signing_key_id", "signature"}
var freePolicyLimitFields = []string{"max_devices", "max_active_workspaces", "max_windows_per_workspace", "max_enabled_plugins", "allow_plugins"}

// VerifyFreePolicyAttestation authenticates raw against a build-pinned key and
// the nonce this desktop sent. It rejects unknown fields and any other schema.
func VerifyFreePolicyAttestation(raw []byte, pinnedKeyID string, publicKeyPEM []byte, expectedNonce string) (FreePolicyAttestation, error) {
	if !quotaKeyIDPattern.MatchString(pinnedKeyID) || len(publicKeyPEM) == 0 {
		return FreePolicyAttestation{}, fmt.Errorf("quota trust root is not configured")
	}
	if !freePolicyNoncePattern.MatchString(expectedNonce) {
		return FreePolicyAttestation{}, fmt.Errorf("invalid free policy nonce")
	}
	key, err := parseQuotaPublicKey(publicKeyPEM)
	if err != nil {
		return FreePolicyAttestation{}, fmt.Errorf("invalid quota public key: %w", err)
	}
	a, err := parseFreePolicyAttestation(raw)
	if err != nil {
		return FreePolicyAttestation{}, err
	}
	if a.Nonce != expectedNonce {
		return FreePolicyAttestation{}, fmt.Errorf("free policy nonce does not match this request")
	}
	if a.SigningKeyID != pinnedKeyID {
		return FreePolicyAttestation{}, fmt.Errorf("quota signing key ID does not match pinned key")
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(a.Signature)
	if err != nil || base64.StdEncoding.EncodeToString(signature) != a.Signature {
		return FreePolicyAttestation{}, fmt.Errorf("invalid quota signature encoding")
	}
	digest := sha256.Sum256(freePolicyCanonicalPayload(a))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature); err != nil {
		return FreePolicyAttestation{}, fmt.Errorf("free policy signature verification failed: %w", err)
	}
	return a, nil
}

func parseFreePolicyAttestation(raw []byte) (FreePolicyAttestation, error) {
	var a FreePolicyAttestation
	if len(raw) > 16*1024 {
		return a, fmt.Errorf("free policy attestation too large")
	}
	fields, err := strictQuotaObject(raw, freePolicyFields)
	if err != nil {
		return a, err
	}
	var schema int
	var kind, scope, plan string
	if quotaNumber(fields["schema_version"], &schema) != nil || schema != 1 ||
		json.Unmarshal(fields["kind"], &kind) != nil || kind != "free_policy" ||
		json.Unmarshal(fields["product_scope"], &scope) != nil || scope != "mincode" ||
		json.Unmarshal(fields["plan"], &plan) != nil || plan != "free" {
		return a, fmt.Errorf("unsupported free policy identity")
	}
	if quotaNumber(fields["policy_revision"], &a.PolicyRevision) != nil || a.PolicyRevision < 1 ||
		quotaNumber(fields["issued_at"], &a.IssuedAt) != nil || a.IssuedAt < 1 {
		return a, fmt.Errorf("invalid free policy revision or time")
	}
	if json.Unmarshal(fields["nonce"], &a.Nonce) != nil || !freePolicyNoncePattern.MatchString(a.Nonce) ||
		json.Unmarshal(fields["signing_key_id"], &a.SigningKeyID) != nil || !quotaKeyIDPattern.MatchString(a.SigningKeyID) ||
		json.Unmarshal(fields["signature"], &a.Signature) != nil {
		return a, fmt.Errorf("invalid free policy strings")
	}
	limits, err := strictQuotaObject(fields["limits"], freePolicyLimitFields)
	if err != nil {
		return a, fmt.Errorf("invalid free policy limits: %w", err)
	}
	for name, target := range map[string]*int64{
		"max_devices": &a.Limits.MaxDevices, "max_active_workspaces": &a.Limits.MaxActiveWorkspaces,
		"max_windows_per_workspace": &a.Limits.MaxWindowsPerWorkspace, "max_enabled_plugins": &a.Limits.MaxEnabledPlugins,
	} {
		if quotaNumber(limits[name], target) != nil || *target > maxQuotaLimit {
			return a, fmt.Errorf("invalid free policy limits")
		}
	}
	var allow bool
	if json.Unmarshal(limits["allow_plugins"], &allow) != nil {
		return a, fmt.Errorf("invalid free policy limits")
	}
	a.Limits.PluginsDisabled = !allow
	return a, nil
}

// freePolicyCanonicalPayload mirrors Entitlement::signFreePolicy's field order
// and JSON_UNESCAPED_SLASHES; every signed string is ASCII by the parser.
func freePolicyCanonicalPayload(a FreePolicyAttestation) []byte {
	var b bytes.Buffer
	b.WriteString(`{"schema_version":1,"kind":"free_policy","product_scope":"mincode","plan":"free","policy_revision":`)
	b.WriteString(strconv.FormatInt(a.PolicyRevision, 10))
	b.WriteString(`,"nonce":`)
	b.Write(quotaJSONString(a.Nonce))
	b.WriteString(`,"issued_at":`)
	b.WriteString(strconv.FormatInt(a.IssuedAt, 10))
	b.WriteString(`,"limits":{"max_devices":`)
	b.WriteString(strconv.FormatInt(a.Limits.MaxDevices, 10))
	b.WriteString(`,"max_active_workspaces":`)
	b.WriteString(strconv.FormatInt(a.Limits.MaxActiveWorkspaces, 10))
	b.WriteString(`,"max_windows_per_workspace":`)
	b.WriteString(strconv.FormatInt(a.Limits.MaxWindowsPerWorkspace, 10))
	b.WriteString(`,"max_enabled_plugins":`)
	b.WriteString(strconv.FormatInt(a.Limits.MaxEnabledPlugins, 10))
	b.WriteString(`,"allow_plugins":`)
	b.WriteString(strconv.FormatBool(!a.Limits.PluginsDisabled))
	b.WriteString(`},"signing_key_id":`)
	b.Write(quotaJSONString(a.SigningKeyID))
	b.WriteByte('}')
	return b.Bytes()
}
