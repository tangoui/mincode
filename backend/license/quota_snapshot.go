package license

import (
	"bytes"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"time"
	"unicode/utf8"
)

// QuotaSnapshot is an entitlement wire contract, not an admission decision.
// VerifyQuotaSnapshot authenticates and binds a snapshot to an expected account.
// Device admission is not implemented here; verified limits alone must not be
// used for resource admission.
type QuotaSnapshot struct {
	SchemaVersion     int         `json:"schema_version"`
	ProductScope      string      `json:"product_scope"`
	AccountID         int64       `json:"account_id"`
	Plan              string      `json:"plan"`
	GrantID           string      `json:"grant_id"`
	PolicyRevision    int64       `json:"policy_revision"`
	IssuedAt          int64       `json:"issued_at"`
	ExpiresAt         *int64      `json:"expires_at"`
	SnapshotExpiresAt int64       `json:"snapshot_expires_at"`
	Limits            QuotaPolicy `json:"limits"`
	SigningKeyID      string      `json:"signing_key_id"`
	Signature         string      `json:"signature"`
}

const maxQuotaLimit int64 = 1<<32 - 1

// quotaSnapshotMaxClockSkewSeconds bounds how far issued_at may lie ahead of the local clock.
const quotaSnapshotMaxClockSkewSeconds int64 = 5 * 60

var quotaKeyIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
var quotaGrantIDPattern = regexp.MustCompile(`^[1-9][0-9]*$`)
var quotaIntegerPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)

// ParseQuotaSnapshot validates the wire format and validity window, but does
// NOT authenticate the snapshot. Never use its result for authorization.
func ParseQuotaSnapshot(raw []byte, now time.Time) (QuotaSnapshot, error) {
	if len(raw) == 0 || len(raw) > 64*1024 || !utf8.Valid(raw) {
		return QuotaSnapshot{}, fmt.Errorf("invalid quota snapshot encoding or size")
	}
	fields, err := strictQuotaObject(raw, []string{
		"schema_version", "product_scope", "account_id", "plan", "grant_id", "policy_revision", "issued_at",
		"expires_at", "snapshot_expires_at", "limits", "signing_key_id", "signature",
	})
	if err != nil {
		return QuotaSnapshot{}, fmt.Errorf("invalid quota snapshot: %w", err)
	}
	var s QuotaSnapshot
	if err := quotaNumber(fields["schema_version"], &s.SchemaVersion); err != nil {
		return QuotaSnapshot{}, err
	}
	limitNames, ok := quotaLimitFields(s.SchemaVersion)
	if !ok {
		return QuotaSnapshot{}, fmt.Errorf("unsupported quota snapshot identity or revision")
	}
	limits, err := strictQuotaObject(fields["limits"], limitNames)
	if err != nil {
		return QuotaSnapshot{}, fmt.Errorf("invalid quota limits: %w", err)
	}
	if s.SchemaVersion >= 3 {
		if err := quotaNumber(limits["max_enabled_plugins"], &s.Limits.MaxEnabledPlugins); err != nil {
			return QuotaSnapshot{}, err
		}
		switch string(limits["allow_plugins"]) {
		case "true":
		case "false":
			s.Limits.PluginsDisabled = true
		default:
			return QuotaSnapshot{}, fmt.Errorf("invalid quota plugin switch")
		}
	}
	for _, field := range []struct {
		raw []byte
		to  *int64
	}{
		{fields["account_id"], &s.AccountID},
		{fields["policy_revision"], &s.PolicyRevision},
		{fields["issued_at"], &s.IssuedAt},
		{fields["snapshot_expires_at"], &s.SnapshotExpiresAt},
		{limits["max_devices"], &s.Limits.MaxDevices},
		{limits["max_active_workspaces"], &s.Limits.MaxActiveWorkspaces},
		{limits["max_windows_per_workspace"], &s.Limits.MaxWindowsPerWorkspace},
	} {
		if err := quotaNumber(field.raw, field.to); err != nil {
			return QuotaSnapshot{}, err
		}
	}
	if !bytes.Equal(fields["expires_at"], []byte("null")) {
		var expires int64
		if err := quotaNumber(fields["expires_at"], &expires); err != nil {
			return QuotaSnapshot{}, err
		}
		s.ExpiresAt = &expires
	}
	for _, field := range []struct {
		raw []byte
		to  *string
	}{
		{fields["product_scope"], &s.ProductScope},
		{fields["plan"], &s.Plan},
		{fields["grant_id"], &s.GrantID},
		{fields["signing_key_id"], &s.SigningKeyID},
		{fields["signature"], &s.Signature},
	} {
		if err := json.Unmarshal(field.raw, field.to); err != nil || !bytes.Equal(field.raw, quotaJSONString(*field.to)) {
			return QuotaSnapshot{}, fmt.Errorf("noncanonical quota snapshot string")
		}
	}
	if s.ProductScope != "mincode" || s.AccountID < 1 || s.PolicyRevision < 1 {
		return QuotaSnapshot{}, fmt.Errorf("unsupported quota snapshot identity or revision")
	}
	if (s.Plan != "free" && s.Plan != "annual" && s.Plan != "perpetual") ||
		(s.Plan == "free" && (s.GrantID != "" || s.ExpiresAt != nil)) ||
		(s.Plan != "free" && !quotaGrantIDPattern.MatchString(s.GrantID)) ||
		(s.Plan == "annual" && s.ExpiresAt == nil) || (s.Plan == "perpetual" && s.ExpiresAt != nil) {
		return QuotaSnapshot{}, fmt.Errorf("invalid quota plan or grant")
	}
	if !quotaKeyIDPattern.MatchString(s.SigningKeyID) || s.Signature == "" {
		return QuotaSnapshot{}, fmt.Errorf("quota snapshot requires a signing key and signature")
	}
	if s.Limits.MaxDevices > maxQuotaLimit || s.Limits.MaxActiveWorkspaces > maxQuotaLimit || s.Limits.MaxWindowsPerWorkspace > maxQuotaLimit ||
		s.Limits.MaxEnabledPlugins > maxQuotaLimit {
		return QuotaSnapshot{}, fmt.Errorf("quota limits out of range")
	}
	current := now.Unix()
	// A local clock slightly behind the server may see issued_at in the future. Tolerate a
	// bounded skew; expiry stays the server's absolute snapshot_expires_at, so the accepted
	// window is never longer than the signed one.
	if s.IssuedAt <= 0 || s.IssuedAt > current+quotaSnapshotMaxClockSkewSeconds || s.SnapshotExpiresAt <= current ||
		s.SnapshotExpiresAt <= s.IssuedAt || s.SnapshotExpiresAt-s.IssuedAt > 3600 {
		return QuotaSnapshot{}, fmt.Errorf("quota snapshot not currently valid")
	}
	if s.ExpiresAt != nil && (*s.ExpiresAt <= current || *s.ExpiresAt <= s.IssuedAt || s.SnapshotExpiresAt > *s.ExpiresAt) {
		return QuotaSnapshot{}, fmt.Errorf("quota grant expired or inconsistent")
	}
	return s, nil
}

// ValidateQuotaTrustRoot checks a build-provisioned key before any member API
// request. It does not authenticate a snapshot or grant resource access.
func ValidateQuotaTrustRoot(keyID string, publicKeyPEM []byte) error {
	if !quotaKeyIDPattern.MatchString(keyID) || len(publicKeyPEM) == 0 {
		return fmt.Errorf("quota trust root is not configured")
	}
	if _, err := parseQuotaPublicKey(publicKeyPEM); err != nil {
		return fmt.Errorf("invalid quota public key: %w", err)
	}
	return nil
}

// VerifyQuotaSnapshot requires an independently pinned key ID and RSA public
// key (PEM SPKI or PKCS#1), never a key obtained from the untrusted response.
// Key rotation requires callers to select an already trusted ID/key pair.
// expectedAccountID must come from the authenticated account context, not the
// response. This does not perform device admission.
func VerifyQuotaSnapshot(raw []byte, now time.Time, pinnedKeyID string, publicKeyPEM []byte, expectedAccountID int64) (QuotaSnapshot, error) {
	if expectedAccountID < 1 {
		return QuotaSnapshot{}, fmt.Errorf("expected quota account ID must be positive")
	}
	if !quotaKeyIDPattern.MatchString(pinnedKeyID) || len(publicKeyPEM) == 0 {
		return QuotaSnapshot{}, fmt.Errorf("quota trust root is not configured")
	}
	key, err := parseQuotaPublicKey(publicKeyPEM)
	if err != nil {
		return QuotaSnapshot{}, fmt.Errorf("invalid quota public key: %w", err)
	}
	s, err := ParseQuotaSnapshot(raw, now)
	if err != nil {
		return QuotaSnapshot{}, err
	}
	if s.SigningKeyID != pinnedKeyID {
		return QuotaSnapshot{}, fmt.Errorf("quota signing key ID does not match pinned key")
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(s.Signature)
	if err != nil || base64.StdEncoding.EncodeToString(signature) != s.Signature {
		return QuotaSnapshot{}, fmt.Errorf("invalid quota signature encoding")
	}
	payload, err := quotaCanonicalPayload(s)
	if err != nil {
		return QuotaSnapshot{}, err
	}
	digest := sha256.Sum256(payload)
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature); err != nil {
		return QuotaSnapshot{}, fmt.Errorf("quota signature verification failed: %w", err)
	}
	if s.AccountID != expectedAccountID {
		return QuotaSnapshot{}, fmt.Errorf("quota snapshot account ID does not match expected account")
	}
	return s, nil
}

// quotaLimitFields lists the exact limits a schema carries. Schema 3 is only
// issued to clients that request it, so schema 2 must keep its original shape.
func quotaLimitFields(schemaVersion int) ([]string, bool) {
	switch schemaVersion {
	case 2:
		return []string{"max_devices", "max_active_workspaces", "max_windows_per_workspace"}, true
	case 3:
		return []string{"max_devices", "max_active_workspaces", "max_windows_per_workspace", "max_enabled_plugins", "allow_plugins"}, true
	}
	return nil, false
}

// MarshalJSON re-emits the wire shape of the snapshot's own schema, so a cached
// schema 2 snapshot still passes the strict parser and signature check.
func (s QuotaSnapshot) MarshalJSON() ([]byte, error) {
	type wire QuotaSnapshot
	type limitsV2 struct {
		MaxDevices             int64 `json:"max_devices"`
		MaxActiveWorkspaces    int64 `json:"max_active_workspaces"`
		MaxWindowsPerWorkspace int64 `json:"max_windows_per_workspace"`
	}
	v2 := limitsV2{s.Limits.MaxDevices, s.Limits.MaxActiveWorkspaces, s.Limits.MaxWindowsPerWorkspace}
	if s.SchemaVersion >= 3 {
		type limitsV3 struct {
			limitsV2
			MaxEnabledPlugins int64 `json:"max_enabled_plugins"`
			AllowPlugins      bool  `json:"allow_plugins"`
		}
		return json.Marshal(struct {
			wire
			Limits limitsV3 `json:"limits"`
		}{wire(s), limitsV3{v2, s.Limits.MaxEnabledPlugins, !s.Limits.PluginsDisabled}})
	}
	return json.Marshal(struct {
		wire
		Limits limitsV2 `json:"limits"`
	}{wire(s), v2})
}

// UnmarshalJSON reads the wire shape back, including the schema 3 allow_plugins
// switch, so a cached snapshot re-marshals to the exact bytes that were signed.
func (s *QuotaSnapshot) UnmarshalJSON(data []byte) error {
	type wire QuotaSnapshot
	var decoded struct {
		wire
		Limits struct {
			QuotaPolicy
			AllowPlugins *bool `json:"allow_plugins"`
		} `json:"limits"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*s = QuotaSnapshot(decoded.wire)
	s.Limits = decoded.Limits.QuotaPolicy
	if decoded.Limits.AllowPlugins != nil {
		s.Limits.PluginsDisabled = !*decoded.Limits.AllowPlugins
	}
	return nil
}

func strictQuotaObject(raw []byte, names []string) (map[string]json.RawMessage, error) {
	allowed := make(map[string]bool, len(names))
	for _, name := range names {
		allowed[name] = true
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return nil, fmt.Errorf("expected JSON object")
	}
	fields := make(map[string]json.RawMessage, len(names))
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("invalid JSON field: %w", err)
		}
		name, ok := key.(string)
		if !ok || !allowed[name] || fields[name] != nil {
			return nil, fmt.Errorf("unknown or duplicate quota field")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("invalid quota field: %w", err)
		}
		fields[name] = value
	}
	if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
		return nil, fmt.Errorf("invalid JSON object end")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing quota JSON data")
	}
	for _, name := range names {
		if fields[name] == nil {
			return nil, fmt.Errorf("quota snapshot missing %s", name)
		}
	}
	return fields, nil
}

func quotaNumber[T ~int | ~int64](raw []byte, target *T) error {
	if !quotaIntegerPattern.Match(raw) {
		return fmt.Errorf("noncanonical quota integer")
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("invalid quota integer: %w", err)
	}
	return nil
}

func quotaJSONString(value string) []byte {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

func quotaCanonicalPayload(s QuotaSnapshot) ([]byte, error) {
	// Mirrors Entitlement.php's insertion order and JSON_UNESCAPED_SLASHES.
	// All signed strings are restricted to ASCII by the parser.
	var b bytes.Buffer
	b.WriteString(`{"schema_version":`)
	b.WriteString(strconv.Itoa(s.SchemaVersion))
	b.WriteString(`,"product_scope":"mincode","account_id":`)
	b.WriteString(strconv.FormatInt(s.AccountID, 10))
	b.WriteString(`,"plan":`)
	b.Write(quotaJSONString(s.Plan))
	b.WriteString(`,"grant_id":`)
	b.Write(quotaJSONString(s.GrantID))
	b.WriteString(`,"policy_revision":`)
	b.WriteString(strconv.FormatInt(s.PolicyRevision, 10))
	b.WriteString(`,"issued_at":`)
	b.WriteString(strconv.FormatInt(s.IssuedAt, 10))
	b.WriteString(`,"expires_at":`)
	if s.ExpiresAt == nil {
		b.WriteString("null")
	} else {
		b.WriteString(strconv.FormatInt(*s.ExpiresAt, 10))
	}
	b.WriteString(`,"snapshot_expires_at":`)
	b.WriteString(strconv.FormatInt(s.SnapshotExpiresAt, 10))
	b.WriteString(`,"limits":{"max_devices":`)
	b.WriteString(strconv.FormatInt(s.Limits.MaxDevices, 10))
	b.WriteString(`,"max_active_workspaces":`)
	b.WriteString(strconv.FormatInt(s.Limits.MaxActiveWorkspaces, 10))
	b.WriteString(`,"max_windows_per_workspace":`)
	b.WriteString(strconv.FormatInt(s.Limits.MaxWindowsPerWorkspace, 10))
	if s.SchemaVersion >= 3 {
		b.WriteString(`,"max_enabled_plugins":`)
		b.WriteString(strconv.FormatInt(s.Limits.MaxEnabledPlugins, 10))
		b.WriteString(`,"allow_plugins":`)
		b.WriteString(strconv.FormatBool(!s.Limits.PluginsDisabled))
	}
	b.WriteString(`},"signing_key_id":`)
	b.Write(quotaJSONString(s.SigningKeyID))
	b.WriteByte('}')
	return b.Bytes(), nil
}

func parseQuotaPublicKey(raw []byte) (*rsa.PublicKey, error) {
	block, rest := pem.Decode(raw)
	if block == nil || len(bytes.TrimSpace(rest)) != 0 || len(block.Headers) != 0 {
		return nil, fmt.Errorf("expected a single RSA public key PEM block")
	}
	var key *rsa.PublicKey
	switch block.Type {
	case "PUBLIC KEY":
		parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		key, _ = parsed.(*rsa.PublicKey)
	case "RSA PUBLIC KEY":
		var err error
		key, err = x509.ParsePKCS1PublicKey(block.Bytes)
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("expected RSA public key")
	}
	if key == nil || key.N.BitLen() < 3072 || key.E < 3 {
		return nil, fmt.Errorf("RSA public key must be at least 3072 bits")
	}
	return key, nil
}
