package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// The full trust audit log is a host-side record of every confirmation full
// trust skipped, plus grants, revokes and lapses. It lives in the __host__ KV
// namespace (never exported, synced or backed up) and holds no payloads: the
// subject is only an action, command or service ID.
const (
	fullTrustAuditKeyPrefix       = "full-trust-audit/"
	fullTrustAuditMax             = 200
	fullTrustAuditCoalesce        = 10 * time.Minute
	fullTrustAuditSubjectMaxRunes = 128
	fullTrustAuditVersion         = 1
)

const (
	fullTrustAuditSkipped = "skipped"
	fullTrustAuditGranted = "granted"
	fullTrustAuditRevoked = "revoked"
	fullTrustAuditLapsed  = "lapsed"
)

type FullTrustAuditEntry struct {
	At          int64  `json:"at"`
	LastAt      int64  `json:"lastAt"`
	Count       int    `json:"count"`
	Kind        string `json:"kind"`
	Surface     string `json:"surface,omitempty"`
	Risk        string `json:"risk,omitempty"`
	Subject     string `json:"subject,omitempty"`
	Counterpart string `json:"counterpart,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

type fullTrustAuditLog struct {
	Version int                   `json:"version"`
	Entries []FullTrustAuditEntry `json:"entries"`
}

// fullTrustNow is the audit clock; tests replace it.
var fullTrustNow = func() time.Time { return time.Now() }

// fullTrustAuditMu serializes the read-modify-write of every audit log.
var fullTrustAuditMu sync.Mutex

func fullTrustAuditKey(pluginID string) (string, error) {
	id := strings.TrimSpace(pluginID)
	if !pluginIDPattern.MatchString(id) {
		return "", fmt.Errorf("full trust audit: invalid plugin id %q", pluginID)
	}
	return fullTrustAuditKeyPrefix + id, nil
}

// sanitizeFullTrustAuditText drops control and format runes (escape
// sequences, bidi overrides) and caps the length.
func sanitizeFullTrustAuditText(value string) string {
	var builder strings.Builder
	runes := 0
	for _, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == utf8.RuneError {
			continue
		}
		if runes == fullTrustAuditSubjectMaxRunes {
			break
		}
		builder.WriteRune(r)
		runes++
	}
	return strings.TrimSpace(builder.String())
}

func loadFullTrustAudit(key string) (fullTrustAuditLog, error) {
	raw, found, err := pluginKVGetRaw(hostPluginID, key)
	if err != nil {
		return fullTrustAuditLog{}, fmt.Errorf("read full trust audit: %w", err)
	}
	if !found {
		return fullTrustAuditLog{Version: fullTrustAuditVersion}, nil
	}
	var log fullTrustAuditLog
	if err := json.Unmarshal(raw, &log); err != nil || log.Version != fullTrustAuditVersion {
		return fullTrustAuditLog{}, errors.New("read full trust audit: unsupported or corrupt log")
	}
	return log, nil
}

// appendFullTrustAudit records one event. Skips with the same (surface, risk,
// subject, counterpart) within fullTrustAuditCoalesce of the previous one in
// the current run (no grant, revoke or lapse in between) only bump its count;
// the log keeps the newest fullTrustAuditMax entries.
func appendFullTrustAudit(pluginID string, entry FullTrustAuditEntry) error {
	key, err := fullTrustAuditKey(pluginID)
	if err != nil {
		return err
	}
	switch entry.Kind {
	case fullTrustAuditSkipped, fullTrustAuditGranted, fullTrustAuditRevoked, fullTrustAuditLapsed:
	default:
		return fmt.Errorf("full trust audit: unknown kind %q", entry.Kind)
	}
	now := fullTrustNow().UnixMilli()
	entry = FullTrustAuditEntry{
		At: now, LastAt: now, Count: 1, Kind: entry.Kind,
		Surface:     sanitizeFullTrustAuditText(entry.Surface),
		Risk:        sanitizeFullTrustAuditText(entry.Risk),
		Subject:     sanitizeFullTrustAuditText(entry.Subject),
		Counterpart: sanitizeFullTrustAuditText(entry.Counterpart),
		Reason:      sanitizeFullTrustAuditText(entry.Reason),
	}

	fullTrustAuditMu.Lock()
	defer fullTrustAuditMu.Unlock()
	log, err := loadFullTrustAudit(key)
	if err != nil {
		// A corrupt log must not block auditing: start a fresh one.
		log = fullTrustAuditLog{Version: fullTrustAuditVersion}
	}
	if !coalesceFullTrustAudit(log.Entries, entry) {
		log.Entries = append(log.Entries, entry)
	}
	if extra := len(log.Entries) - fullTrustAuditMax; extra > 0 {
		log.Entries = append([]FullTrustAuditEntry(nil), log.Entries[extra:]...)
	}
	raw, err := json.Marshal(log)
	if err != nil {
		return fmt.Errorf("encode full trust audit: %w", err)
	}
	if err := pluginKVSetRaw(hostPluginID, key, raw); err != nil {
		return fmt.Errorf("write full trust audit: %w", err)
	}
	return nil
}

func coalesceFullTrustAudit(entries []FullTrustAuditEntry, entry FullTrustAuditEntry) bool {
	if entry.Kind != fullTrustAuditSkipped {
		return false
	}
	for i := len(entries) - 1; i >= 0; i-- {
		previous := &entries[i]
		if previous.Kind != fullTrustAuditSkipped {
			return false
		}
		if previous.Surface == entry.Surface && previous.Risk == entry.Risk &&
			previous.Subject == entry.Subject && previous.Counterpart == entry.Counterpart {
			if entry.LastAt-previous.LastAt > fullTrustAuditCoalesce.Milliseconds() {
				return false
			}
			previous.Count++
			previous.LastAt = entry.LastAt
			return true
		}
	}
	return false
}

// readFullTrustAudit returns the log oldest first; an empty slice when none.
func readFullTrustAudit(pluginID string) ([]FullTrustAuditEntry, error) {
	key, err := fullTrustAuditKey(pluginID)
	if err != nil {
		return nil, err
	}
	fullTrustAuditMu.Lock()
	defer fullTrustAuditMu.Unlock()
	log, err := loadFullTrustAudit(key)
	if err != nil {
		return nil, err
	}
	if log.Entries == nil {
		return []FullTrustAuditEntry{}, nil
	}
	return log.Entries, nil
}

func clearFullTrustAudit(pluginID string) error {
	key, err := fullTrustAuditKey(pluginID)
	if err != nil {
		return err
	}
	fullTrustAuditMu.Lock()
	defer fullTrustAuditMu.Unlock()
	if err := pluginKVDeleteRaw(hostPluginID, key); err != nil {
		return fmt.Errorf("clear full trust audit: %w", err)
	}
	return nil
}
