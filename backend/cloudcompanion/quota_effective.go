package cloudcompanion

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"mincode/app/backend/core"
	appcrypto "mincode/app/backend/crypto"
	"mincode/app/backend/license"
)

// A verified paid entitlement stays usable offline for this long after it was issued.
const quotaOfflineGrace = 7 * 24 * time.Hour

// The cache is sealed with the machine key (AES-GCM), so an edited, copied or
// plaintext file is ignored rather than trusted. The legacy plaintext file is
// only read once to carry over its signed member snapshot.
const (
	quotaCacheFileName       = "quota-cache.enc"
	legacyQuotaCacheFileName = "quota-cache.json"
	// A clock set back further than this no longer honours the offline grace of
	// a cached paid snapshot, which would otherwise never run out.
	quotaClockRollbackTolerance = time.Hour
	// The prefix appcrypto.EncryptForStorage writes; DecryptFromStorage returns
	// unprefixed input unchanged, so the prefix is checked before decrypting.
	sealedStoragePrefix = "enc:v2:"
)

// FallbackFreeQuota is the free policy applied when the cloud's free policy was
// never obtained (offline first launch, cloud outage) or the cache was removed,
// so signed-out users are never unlimited. Release builds inject the cloud's
// current policy with -ldflags "-X …/cloudcompanion.FallbackFreeQuota=…"; the
// default below matches the free plan when this was written.
var FallbackFreeQuota = ""

const defaultFallbackFreeQuota = "max_devices=1,max_active_workspaces=3,max_windows_per_workspace=6,max_enabled_plugins=6,allow_plugins=1"

// EffectiveQuota is the limit set the desktop applies to new workspaces and tabs.
// Limits is nil only when no free policy has ever been obtained; callers must then not invent limits.
type EffectiveQuota struct {
	Source       string               `json:"source"` // verified | offline | free
	Plan         string               `json:"plan,omitempty"`
	AccountID    int64                `json:"accountId,omitempty"`
	ExpiresAt    *int64               `json:"expiresAt,omitempty"`
	CheckedAt    int64                `json:"checkedAt,omitempty"`
	OfflineUntil int64                `json:"offlineUntil,omitempty"`
	Limits       *license.QuotaPolicy `json:"limits,omitempty"`
	Reason       string               `json:"reason,omitempty"` // signed_out | unconfigured | fetch_failed | expired | offline_expired | clock_rollback | device_limit
	DeviceLimit  *DeviceUsage         `json:"deviceLimit,omitempty"`
	// Fallback marks free limits taken from FallbackFreeQuota rather than the cloud.
	Fallback bool `json:"fallback,omitempty"`
}

// DeviceUsage reports the cloud's active device count when it refused this device.
type DeviceUsage struct {
	Current int64 `json:"current"`
	Limit   int64 `json:"limit"`
}

type freeQuotaCache struct {
	APIBaseURL string              `json:"apiBaseUrl"`
	Limits     license.QuotaPolicy `json:"limits"`
	FetchedAt  int64               `json:"fetchedAt"`
	// The cloud's signed attestation and the nonce it answered, re-verified on
	// every read when a trust root is pinned.
	Attestation json.RawMessage `json:"attestation,omitempty"`
	Nonce       string          `json:"nonce,omitempty"`
}

type quotaCache struct {
	MemberAPIBaseURL string                 `json:"memberApiBaseUrl,omitempty"`
	Member           *license.QuotaSnapshot `json:"member,omitempty"`
	Free             *freeQuotaCache        `json:"free,omitempty"`
	// LastSeen is the latest clock reading observed, for clock rollback detection.
	LastSeen int64 `json:"lastSeen,omitempty"`
}

var quotaCacheMu sync.Mutex

func init() {
	core.Register("cloud_quota_effective", func(_, _ string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), cloudHTTPTimeout)
		defer cancel()
		return core.EncodeResult(resolveEffectiveQuota(ctx))
	})
}

func resolveEffectiveQuota(ctx context.Context) EffectiveQuota {
	now := cloudNow()
	rolledBack := observeClock(now)
	session, err := readSession()
	member := err == nil && isMemberSession(session)
	reason := "signed_out"
	if member {
		reason = "unconfigured"
		if trust, trustErr := pinnedQuotaTrust(); trustErr == nil {
			reason = "fetch_failed"
			snapshot, fetchErr := FetchMemberQuotaSnapshot(ctx, trust)
			if fetchErr == nil && snapshot.AccountID == session.AccountID {
				updateQuotaCache(func(cache *quotaCache) {
					cache.MemberAPIBaseURL, cache.Member = session.APIBaseURL, &snapshot
					// Cloud-issued time resets the clock record: it corrects a clock that
					// ran fast, while a clock set back cannot drag the record below it.
					cache.LastSeen = max(now.Unix(), snapshot.IssuedAt)
				})
				return memberQuota("verified", snapshot)
			}
			var limitErr *DeviceLimitError
			if errors.As(fetchErr, &limitErr) {
				// The cloud refused this device outright; an older cached paid snapshot must not outlive that answer.
				updateQuotaCache(func(cache *quotaCache) { cache.MemberAPIBaseURL, cache.Member = "", nil })
				result := freeQuota(ctx, session.APIBaseURL, "device_limit", now)
				result.DeviceLimit = &DeviceUsage{Current: limitErr.Current, Limit: limitErr.Limit}
				return result
			}
			if cache := readQuotaCache(); cache.Member != nil {
				result, cachedReason := cachedMemberQuota(*cache.Member, cache.MemberAPIBaseURL, session, trust, now, rolledBack)
				if result != nil {
					return *result
				}
				reason = cachedReason
			}
		}
	}
	base := session.APIBaseURL
	if !member {
		if base, err = cloudAPIBaseURL(""); err != nil {
			return fallbackQuota(reason)
		}
	}
	return freeQuota(ctx, base, reason, now)
}

// observeClock records the latest clock reading and reports whether the clock
// now reads well before a time already seen.
func observeClock(now time.Time) bool {
	rolledBack := false
	mutateQuotaCache(func(cache *quotaCache) bool {
		if cache.LastSeen > 0 && now.Add(quotaClockRollbackTolerance).Unix() < cache.LastSeen {
			rolledBack = true
			return false
		}
		// Coarse steps keep frequent quota checks from rewriting the file.
		if now.Unix() < cache.LastSeen+60 {
			return false
		}
		cache.LastSeen = now.Unix()
		return true
	})
	return rolledBack
}

func fallbackFreePolicy() license.QuotaPolicy {
	if policy, ok := parseFallbackQuota(FallbackFreeQuota); ok {
		return policy
	}
	policy, _ := parseFallbackQuota(defaultFallbackFreeQuota)
	return policy
}

// parseFallbackQuota reads "key=value,…" (the form -ldflags can carry); every
// limit is required and an invalid value rejects the whole string.
func parseFallbackQuota(text string) (license.QuotaPolicy, bool) {
	values := map[string]int64{}
	for _, part := range strings.Split(text, ",") {
		key, raw, found := strings.Cut(strings.TrimSpace(part), "=")
		number, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if !found || err != nil {
			return license.QuotaPolicy{}, false
		}
		values[strings.TrimSpace(key)] = number
	}
	required := []string{"max_devices", "max_active_workspaces", "max_windows_per_workspace", "max_enabled_plugins", "allow_plugins"}
	for _, key := range required {
		if _, ok := values[key]; !ok {
			return license.QuotaPolicy{}, false
		}
	}
	if len(values) != len(required) || (values["allow_plugins"] != 0 && values["allow_plugins"] != 1) {
		return license.QuotaPolicy{}, false
	}
	policy := license.QuotaPolicy{
		MaxDevices: values["max_devices"], MaxActiveWorkspaces: values["max_active_workspaces"],
		MaxWindowsPerWorkspace: values["max_windows_per_workspace"], MaxEnabledPlugins: values["max_enabled_plugins"],
		PluginsDisabled: values["allow_plugins"] == 0,
	}
	return policy, validQuotaPolicy(policy)
}

func fallbackQuota(reason string) EffectiveQuota {
	limits := fallbackFreePolicy()
	return EffectiveQuota{Source: "free", Plan: "free", Limits: &limits, Reason: reason, Fallback: true}
}

func memberQuota(source string, snapshot license.QuotaSnapshot) EffectiveQuota {
	limits := snapshot.Limits
	return EffectiveQuota{Source: source, Plan: snapshot.Plan, AccountID: snapshot.AccountID, ExpiresAt: snapshot.ExpiresAt,
		CheckedAt: snapshot.IssuedAt, Limits: &limits}
}

// The cached snapshot is re-verified as of its own issue time; offline age and grant expiry use the real clock.
func cachedMemberQuota(cached license.QuotaSnapshot, cachedBase string, session persistedSession, trust QuotaTrust, now time.Time, rolledBack bool) (*EffectiveQuota, string) {
	if cached.AccountID != session.AccountID || cachedBase != session.APIBaseURL {
		return nil, "fetch_failed"
	}
	raw, err := json.Marshal(cached)
	if err != nil {
		return nil, "fetch_failed"
	}
	verified, err := license.VerifyQuotaSnapshot(raw, time.Unix(cached.IssuedAt, 0), trust.KeyID, trust.PublicKeyPEM, session.AccountID)
	if err != nil || verified.IssuedAt > now.Add(5*time.Minute).Unix() {
		return nil, "fetch_failed"
	}
	if verified.ExpiresAt != nil && *verified.ExpiresAt <= now.Unix() {
		return nil, "expired"
	}
	if rolledBack {
		return nil, "clock_rollback"
	}
	offlineUntil := time.Unix(verified.IssuedAt, 0).Add(quotaOfflineGrace)
	if !now.Before(offlineUntil) {
		return nil, "offline_expired"
	}
	result := memberQuota("offline", verified)
	result.OfflineUntil = offlineUntil.Unix()
	return &result, ""
}

func freeQuota(ctx context.Context, base, reason string, now time.Time) EffectiveQuota {
	trust, trustErr := pinnedQuotaTrust()
	var pinned *QuotaTrust
	if trustErr == nil {
		pinned = &trust
	}
	if fetched, err := fetchFreeQuotaPolicy(ctx, base, pinned); err == nil {
		fetched.FetchedAt = now.Unix()
		updateQuotaCache(func(cache *quotaCache) { cache.Free = &fetched })
		limits := fetched.Limits
		return EffectiveQuota{Source: "free", Plan: "free", CheckedAt: now.Unix(), Limits: &limits, Reason: reason}
	}
	if cached := readQuotaCache().Free; cached != nil && cached.APIBaseURL == base {
		if limits, ok := trustedFreeLimits(*cached, pinned); ok {
			return EffectiveQuota{Source: "free", Plan: "free", CheckedAt: cached.FetchedAt, Limits: &limits, Reason: reason}
		}
	}
	return fallbackQuota(reason)
}

// trustedFreeLimits re-verifies a cached free policy. With a pinned trust root
// only the signed limits count, so even a cache re-sealed with the machine key
// cannot carry limits the cloud never signed.
func trustedFreeLimits(cached freeQuotaCache, trust *QuotaTrust) (license.QuotaPolicy, bool) {
	if trust == nil {
		return cached.Limits, validQuotaPolicy(cached.Limits)
	}
	attestation, err := license.VerifyFreePolicyAttestation(cached.Attestation, trust.KeyID, trust.PublicKeyPEM, cached.Nonce)
	if err != nil || !validQuotaPolicy(attestation.Limits) {
		return license.QuotaPolicy{}, false
	}
	return attestation.Limits, true
}

// fetchFreeQuotaPolicy reads the public free policy. With a pinned trust root
// (every release build) only an attestation signed over this request's nonce
// is accepted, so a fake or intercepting server cannot lift the free limits;
// builds without one accept the unsigned policy, which older clouds only send.
func fetchFreeQuotaPolicy(ctx context.Context, base string, trust *QuotaTrust) (freeQuotaCache, error) {
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return freeQuotaCache{}, err
	}
	nonce := hex.EncodeToString(nonceBytes)
	endpoint := strings.TrimRight(base, "/") + "/api/mincode/quotaPolicy/free?nonce=" + nonce
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return freeQuotaCache{}, err
	}
	request.Header.Set("Accept", "application/json")
	client := cloudHTTPClient()
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return freeQuotaCache{}, err
	}
	defer response.Body.Close()
	raw, err := core.ReadAllLimited(response.Body, 64*1024)
	if err != nil {
		return freeQuotaCache{}, err
	}
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			Plan   string `json:"plan"`
			Limits struct {
				MaxDevices             *int64 `json:"max_devices"`
				MaxActiveWorkspaces    *int64 `json:"max_active_workspaces"`
				MaxWindowsPerWorkspace *int64 `json:"max_windows_per_workspace"`
				MaxEnabledPlugins      *int64 `json:"max_enabled_plugins"`
				AllowPlugins           *bool  `json:"allow_plugins"`
			} `json:"limits"`
			Attestation json.RawMessage `json:"attestation"`
		} `json:"data"`
	}
	if response.StatusCode != http.StatusOK || json.Unmarshal(raw, &envelope) != nil || envelope.Code != 1 || envelope.Data.Plan != "free" {
		return freeQuotaCache{}, errors.New("免费策略响应无效")
	}
	if trust != nil {
		if len(envelope.Data.Attestation) == 0 {
			return freeQuotaCache{}, errors.New("免费策略未签名")
		}
		attestation, err := license.VerifyFreePolicyAttestation(envelope.Data.Attestation, trust.KeyID, trust.PublicKeyPEM, nonce)
		if err != nil || !validQuotaPolicy(attestation.Limits) {
			return freeQuotaCache{}, errors.New("免费策略签名无效")
		}
		return freeQuotaCache{APIBaseURL: base, Limits: attestation.Limits, Attestation: envelope.Data.Attestation, Nonce: nonce}, nil
	}
	limits := envelope.Data.Limits
	if limits.MaxDevices == nil || limits.MaxActiveWorkspaces == nil || limits.MaxWindowsPerWorkspace == nil {
		return freeQuotaCache{}, errors.New("免费策略缺少额度字段")
	}
	policy := license.QuotaPolicy{MaxDevices: *limits.MaxDevices, MaxActiveWorkspaces: *limits.MaxActiveWorkspaces, MaxWindowsPerWorkspace: *limits.MaxWindowsPerWorkspace}
	// Clouds that predate the plugin limit omit it, which means unlimited.
	if limits.MaxEnabledPlugins != nil {
		policy.MaxEnabledPlugins = *limits.MaxEnabledPlugins
	}
	policy.PluginsDisabled = limits.AllowPlugins != nil && !*limits.AllowPlugins
	if !validQuotaPolicy(policy) {
		return freeQuotaCache{}, errors.New("免费策略额度超出范围")
	}
	return freeQuotaCache{APIBaseURL: base, Limits: policy}, nil
}

func validQuotaPolicy(policy license.QuotaPolicy) bool {
	const max = 1<<32 - 1
	return policy.MaxDevices >= 0 && policy.MaxDevices <= max && policy.MaxActiveWorkspaces >= 0 &&
		policy.MaxActiveWorkspaces <= max && policy.MaxWindowsPerWorkspace >= 0 && policy.MaxWindowsPerWorkspace <= max &&
		policy.MaxEnabledPlugins >= 0 && policy.MaxEnabledPlugins <= max
}

func quotaCachePath() (string, error) {
	root := identityRootDir()
	if root == "" {
		return "", errors.New("应用数据目录不可用")
	}
	return filepath.Join(root, identityDataDirectory, quotaCacheFileName), nil
}

func readQuotaCache() quotaCache {
	quotaCacheMu.Lock()
	defer quotaCacheMu.Unlock()
	return readQuotaCacheLocked()
}

func readQuotaCacheLocked() quotaCache {
	path, err := quotaCachePath()
	if err != nil {
		return quotaCache{}
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return readLegacyQuotaCacheLocked(filepath.Dir(path))
	}
	if err != nil || len(raw) > 128*1024 || !strings.HasPrefix(string(raw), sealedStoragePrefix) {
		return quotaCache{}
	}
	plain, err := appcrypto.DecryptFromStorage(string(raw))
	var cache quotaCache
	if err != nil || json.Unmarshal([]byte(plain), &cache) != nil {
		return quotaCache{}
	}
	return cache
}

// readLegacyQuotaCacheLocked keeps only the cloud-signed member snapshot of the
// old plaintext cache (it is re-verified on use); its unsigned free policy and
// clock record cannot be trusted and are dropped.
func readLegacyQuotaCacheLocked(dir string) quotaCache {
	legacy := filepath.Join(dir, legacyQuotaCacheFileName)
	raw, err := os.ReadFile(legacy)
	if err != nil {
		return quotaCache{}
	}
	var old quotaCache
	if len(raw) > 64*1024 || json.Unmarshal(raw, &old) != nil {
		return quotaCache{}
	}
	return quotaCache{MemberAPIBaseURL: old.MemberAPIBaseURL, Member: old.Member}
}

func updateQuotaCache(change func(*quotaCache)) {
	mutateQuotaCache(func(cache *quotaCache) bool {
		change(cache)
		return true
	})
}

// mutateQuotaCache writes the cache back only when change reports a change.
func mutateQuotaCache(change func(*quotaCache) bool) {
	quotaCacheMu.Lock()
	defer quotaCacheMu.Unlock()
	cache := readQuotaCacheLocked()
	if !change(&cache) {
		return
	}
	path, err := quotaCachePath()
	if err != nil {
		return
	}
	raw, err := json.Marshal(cache)
	if err != nil {
		return
	}
	sealed, err := appcrypto.EncryptForStorage(string(raw))
	if err == nil && writeSecureFile(path, []byte(sealed)) == nil {
		_ = os.Remove(filepath.Join(filepath.Dir(path), legacyQuotaCacheFileName))
	}
}
