package plugin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// Full trust lets the user skip a plugin's confirmations (design:
// docs/superpowers/specs/2026-11-03-plugin-full-trust-design.md). The record
// lives only in the __host__ KV namespace, which the settings export, the
// plugin data archive, the encrypted backup and config sync never carry, and
// which no host.* method reaches. It is bound to a fingerprint of the
// installed package; any change found on read deletes it for good.
const (
	fullTrustRecordVersion   = 1
	fullTrustKeyPrefix       = "full-trust/"
	fullTrustChangedEvent    = "plugin:full-trust-changed"
	fullTrustPackageMaxFiles = 2000
	fullTrustPackageMaxBytes = 64 << 20
)

// Eligibility reasons (FullTrustSubject.Reason).
const (
	fullTrustReasonNotInstalled      = "not_installed"
	fullTrustReasonBuiltin           = "builtin"
	fullTrustReasonDevelopment       = "development"
	fullTrustReasonDeclarative       = "declarative"
	fullTrustReasonNoOperations      = "no_operations"
	fullTrustReasonDisabled          = "disabled"
	fullTrustReasonPackageUnreadable = "package_unreadable"
)

// Lapse reasons (FullTrustStatus.Lapsed, audit reason, event reason).
const (
	fullTrustLapseVersionChanged     = "version_changed"
	fullTrustLapsePermissionsChanged = "permissions_changed"
	fullTrustLapsePackageChanged     = "package_changed"
	fullTrustLapseNotEligible        = "not_eligible"
	fullTrustLapseDisabled           = "disabled"
	fullTrustLapseUninstalled        = "uninstalled"
	fullTrustLapseUpgraded           = "upgraded"
	fullTrustLapseCleared            = "cleared"
	fullTrustReasonRevoked           = "revoked"
)

func fullTrustKey(pluginID string) string { return fullTrustKeyPrefix + pluginID }

type fullTrustRecord struct {
	Version           int      `json:"version"`
	PluginVersion     string   `json:"pluginVersion"`
	Capabilities      []string `json:"capabilities"`
	PermissionsDigest string   `json:"permissionsDigest"`
	PackageDigest     string   `json:"packageDigest"`
	Fingerprint       string   `json:"fingerprint"`
	GrantedAt         int64    `json:"grantedAt"`
}

// FullTrustSubject describes what the user would trust right now. Fingerprint
// and record are set only when Eligible.
type FullTrustSubject struct {
	PluginID     string          `json:"pluginId"`
	Name         string          `json:"name"`
	Version      string          `json:"version"`
	Capabilities []string        `json:"capabilities"`
	Fingerprint  string          `json:"fingerprint"`
	Eligible     bool            `json:"eligible"`
	Reason       string          `json:"reason,omitempty"`
	record       fullTrustRecord // candidate record (not stored)
}

type FullTrustStatus struct {
	PluginID        string           `json:"pluginId"`
	Trusted         bool             `json:"trusted"`
	GrantedAt       int64            `json:"grantedAt,omitempty"`
	LegacyDangerous bool             `json:"legacyDangerous"`
	Lapsed          string           `json:"lapsed,omitempty"`
	Subject         FullTrustSubject `json:"subject"`
}

// fullTrustMu serializes changes to trust records so a lapse never deletes a
// record granted after it was read.
var fullTrustMu sync.Mutex

// fullTrustPackageDigest hashes every regular file under root as sorted
// "rel\x00size\x00sha256\n" lines. Symlinks, special files and packages over
// the file or byte limit are unreadable.
func fullTrustPackageDigest(root string) (string, error) {
	type packageFile struct {
		rel  string
		size int64
		sum  string
	}
	files := []packageFile{}
	var total int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%q is a symlink", path)
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("%q is not a regular file", path)
		}
		if len(files) == fullTrustPackageMaxFiles {
			return fmt.Errorf("package has more than %d files", fullTrustPackageMaxFiles)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		if total > fullTrustPackageMaxBytes {
			return fmt.Errorf("package exceeds %d bytes", fullTrustPackageMaxBytes)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		sum, err := processLaunchFileHash(path, fullTrustPackageMaxBytes)
		if err != nil {
			return err
		}
		files = append(files, packageFile{rel: filepath.ToSlash(rel), size: info.Size(), sum: sum})
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("%s: %w", fullTrustReasonPackageUnreadable, err)
	}
	slices.SortFunc(files, func(a, b packageFile) int { return strings.Compare(a.rel, b.rel) })
	hash := sha256.New()
	for _, file := range files {
		fmt.Fprintf(hash, "%s\x00%d\x00%s\n", file.rel, file.size, file.sum)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// fullTrustPermissionsDigest hashes the confirmation-relevant parts of the
// manifest's actions and commands, each sorted by ID.
func fullTrustPermissionsDigest(manifest *Manifest) string {
	type action struct {
		ID             string   `json:"id"`
		Risk           string   `json:"risk"`
		UICallable     bool     `json:"uiCallable"`
		AICallable     bool     `json:"aiCallable"`
		Permissions    []string `json:"permissions"`
		ResultHandling string   `json:"resultHandling"`
	}
	type command struct {
		ID     string `json:"id"`
		Action string `json:"action"`
		Risk   string `json:"risk"`
	}
	digest := struct {
		Actions  []action  `json:"actions"`
		Commands []command `json:"commands"`
	}{Actions: []action{}, Commands: []command{}}
	if manifest != nil {
		for _, entry := range manifest.Contributes.Actions {
			permissions := make([]string, 0, len(entry.Permissions))
			for _, permission := range entry.Permissions {
				permissions = append(permissions, string(permission))
			}
			slices.Sort(permissions)
			permissions = slices.Compact(permissions)
			digest.Actions = append(digest.Actions, action{ID: entry.ID, Risk: entry.Risk, UICallable: entry.UICallable,
				AICallable: entry.AICallable, Permissions: permissions, ResultHandling: entry.ResultHandling})
		}
		for _, entry := range manifest.Contributes.Commands {
			digest.Commands = append(digest.Commands, command{ID: entry.ID, Action: entry.Action, Risk: entry.Risk})
		}
	}
	slices.SortStableFunc(digest.Actions, func(a, b action) int { return strings.Compare(a.ID, b.ID) })
	slices.SortStableFunc(digest.Commands, func(a, b command) int { return strings.Compare(a.ID, b.ID) })
	raw, _ := json.Marshal(digest) // plain strings and bools cannot fail
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func fullTrustCapabilities(manifest *Manifest) []string {
	capabilities := []string{}
	if manifest != nil {
		for _, capability := range manifest.Capabilities {
			capabilities = append(capabilities, string(capability))
		}
	}
	slices.Sort(capabilities)
	return slices.Compact(capabilities)
}

func fullTrustFingerprint(pluginID string, record fullTrustRecord) string {
	raw, _ := json.Marshal(struct {
		V                 int      `json:"v"`
		PluginID          string   `json:"pluginId"`
		Version           string   `json:"version"`
		Capabilities      []string `json:"capabilities"`
		PermissionsDigest string   `json:"permissionsDigest"`
		PackageDigest     string   `json:"packageDigest"`
	}{1, pluginID, record.PluginVersion, record.Capabilities, record.PermissionsDigest, record.PackageDigest})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

type fullTrustSnapshot struct {
	found    bool
	source   string
	root     string
	enabled  bool
	manifest *Manifest
}

func (manager *pluginManager) fullTrustSnapshot(id string) fullTrustSnapshot {
	record, err := manager.record(id)
	if err != nil {
		return fullTrustSnapshot{}
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	return fullTrustSnapshot{found: true, source: record.source, root: record.root, enabled: record.enabled, manifest: record.manifest}
}

// fullTrustIdentity fills the subject's descriptive fields only.
func fullTrustIdentity(id string, snapshot fullTrustSnapshot) FullTrustSubject {
	subject := FullTrustSubject{PluginID: id, Capabilities: fullTrustCapabilities(snapshot.manifest)}
	if snapshot.manifest != nil {
		subject.Name, subject.Version = snapshot.manifest.Name, snapshot.manifest.Version
	}
	return subject
}

// fullTrustHashPackage is fullTrustPackageDigest; tests count the calls.
var fullTrustHashPackage = fullTrustPackageDigest

// fullTrustUnreadableLogged keeps the package-unreadable log to one line per
// plugin per session: every status read of such a plugin would repeat it.
var fullTrustUnreadableLogged sync.Map

// fullTrustEligibility runs the eligibility checks that need no package
// hashing. Eligible then means "eligible unless the package is unreadable";
// Fingerprint stays empty.
func (manager *pluginManager) fullTrustEligibility(id string) (FullTrustSubject, fullTrustSnapshot) {
	id = strings.TrimSpace(id)
	snapshot := manager.fullTrustSnapshot(id)
	subject := fullTrustIdentity(id, snapshot)
	manifest := snapshot.manifest
	switch {
	case !snapshot.found:
		subject.Reason = fullTrustReasonNotInstalled
	case snapshot.source == pluginSourceBuiltin:
		subject.Reason = fullTrustReasonBuiltin
	case snapshot.source == pluginSourceDevelopment:
		subject.Reason = fullTrustReasonDevelopment
	case snapshot.source != pluginSourceLocal || manifest == nil:
		subject.Reason = fullTrustReasonPackageUnreadable
	case manifest.isDeclarative():
		subject.Reason = fullTrustReasonDeclarative
	case len(manifest.Contributes.Actions) == 0 && len(manifest.Contributes.Commands) == 0:
		subject.Reason = fullTrustReasonNoOperations
	case !snapshot.enabled:
		subject.Reason = fullTrustReasonDisabled
	}
	subject.Eligible = subject.Reason == ""
	return subject, snapshot
}

// fullTrustSubject computes what trusting the plugin now would bind to, or
// why it cannot be trusted. It hashes the whole package: call it only to
// check a stored record or when the user starts enabling full trust.
func (manager *pluginManager) fullTrustSubject(id string) (FullTrustSubject, error) {
	id = strings.TrimSpace(id)
	subject, snapshot := manager.fullTrustEligibility(id)
	if !subject.Eligible {
		return subject, nil
	}
	subject.Eligible = false
	manifest := snapshot.manifest
	packageDigest, err := fullTrustHashPackage(snapshot.root)
	if err != nil {
		if _, logged := fullTrustUnreadableLogged.LoadOrStore(id, true); !logged {
			log.Printf("plugin %s: full trust package digest: %v", id, err)
		}
		subject.Reason = fullTrustReasonPackageUnreadable
		return subject, nil
	}
	record := fullTrustRecord{
		Version: fullTrustRecordVersion, PluginVersion: manifest.Version, Capabilities: subject.Capabilities,
		PermissionsDigest: fullTrustPermissionsDigest(manifest), PackageDigest: packageDigest,
	}
	record.Fingerprint = fullTrustFingerprint(id, record)
	subject.Fingerprint, subject.Eligible, subject.record = record.Fingerprint, true, record
	return subject, nil
}

// fullTrustLapseReason compares a stored record with the current candidate;
// "" means the record still holds.
func fullTrustLapseReason(raw json.RawMessage, subject FullTrustSubject) (fullTrustRecord, string) {
	var stored fullTrustRecord
	if err := json.Unmarshal(raw, &stored); err != nil || stored.Version != fullTrustRecordVersion {
		return stored, fullTrustLapseNotEligible
	}
	if !subject.Eligible {
		// Name the lifecycle change when there is one; a plugin read while
		// disabled (also mid-upgrade) or after removal lapses for that.
		switch subject.Reason {
		case fullTrustReasonDisabled:
			return stored, fullTrustLapseDisabled
		case fullTrustReasonNotInstalled:
			return stored, fullTrustLapseUninstalled
		}
		return stored, fullTrustLapseNotEligible
	}
	candidate := subject.record
	switch {
	case stored.Fingerprint == candidate.Fingerprint && fullTrustFingerprint(subject.PluginID, stored) == stored.Fingerprint:
		return stored, ""
	case stored.PluginVersion != candidate.PluginVersion:
		return stored, fullTrustLapseVersionChanged
	case !slices.Equal(stored.Capabilities, candidate.Capabilities) || stored.PermissionsDigest != candidate.PermissionsDigest:
		return stored, fullTrustLapsePermissionsChanged
	default:
		return stored, fullTrustLapsePackageChanged
	}
}

// fullTrustStatus reads the trust record and re-checks it against the
// installed package. A record that no longer matches is deleted, audited as
// lapsed and announced; it never comes back on its own. Without a record the
// package is not hashed and Subject carries only the identity fields
// (Fingerprint empty, Eligible false); call fullTrustSubject for the rest.
func (manager *pluginManager) fullTrustStatus(id string) (FullTrustStatus, error) {
	id = strings.TrimSpace(id)
	status := FullTrustStatus{PluginID: id}
	if !pluginIDPattern.MatchString(id) {
		return status, fmt.Errorf("full trust: invalid plugin id %q", id)
	}
	if legacy, err := manager.dangerousActionsTrusted(id); err == nil {
		status.LegacyDangerous = legacy
	}
	raw, found, err := pluginKVGetRaw(hostPluginID, fullTrustKey(id))
	if err != nil {
		return status, fmt.Errorf("read full trust: %w", err)
	}
	if !found {
		status.Subject = fullTrustIdentity(id, manager.fullTrustSnapshot(id))
		return status, nil
	}
	subject, err := manager.fullTrustSubject(id)
	if err != nil {
		return status, err
	}
	status.Subject = subject
	stored, reason := fullTrustLapseReason(raw, subject)
	if reason == "" {
		status.Trusted, status.GrantedAt = true, stored.GrantedAt
		return status, nil
	}
	status.Lapsed = reason
	return status, manager.lapseFullTrust(id, raw, reason)
}

// lapseFullTrust deletes the record read as raw (unless it changed since),
// audits the lapse and emits the change event.
func (manager *pluginManager) lapseFullTrust(id string, raw json.RawMessage, reason string) error {
	fullTrustMu.Lock()
	current, found, err := pluginKVGetRaw(hostPluginID, fullTrustKey(id))
	if err == nil && found && bytes.Equal(current, raw) {
		err = pluginKVDeleteRaw(hostPluginID, fullTrustKey(id))
	} else if err == nil {
		found = false // already gone or re-granted: someone else owns it
	}
	fullTrustMu.Unlock()
	if err != nil {
		return fmt.Errorf("delete lapsed full trust: %w", err)
	}
	if found {
		manager.announceFullTrustEnd(id, fullTrustAuditLapsed, reason)
	}
	return nil
}

func (manager *pluginManager) announceFullTrustEnd(id, kind, reason string) {
	entry := FullTrustAuditEntry{Kind: kind}
	if kind != fullTrustAuditRevoked {
		entry.Reason = reason
	}
	if err := appendFullTrustAudit(id, entry); err != nil {
		log.Printf("plugin %s: full trust audit: %v", id, err)
	}
	hostUIEmit(fullTrustChangedEvent, map[string]any{"pluginId": id, "trusted": false, "reason": reason})
}

// fullTrustActive reports whether the plugin's confirmations may be skipped
// now: a matching record, an eligible plugin and an unlocked app.
func (manager *pluginManager) fullTrustActive(id string) bool {
	status, err := manager.fullTrustStatus(id)
	if err != nil {
		log.Printf("plugin %s: full trust status: %v", strings.TrimSpace(id), err)
		return false
	}
	return status.Trusted && !appLocked()
}

// fullTrustSkip is the single call a confirmation point makes: true means
// skip the prompt; the skip is audited (kind forced to "skipped"). A failed
// audit write is logged and does not undo the skip.
func (manager *pluginManager) fullTrustSkip(id string, entry FullTrustAuditEntry) bool {
	if !manager.fullTrustActive(id) {
		return false
	}
	entry.Kind = fullTrustAuditSkipped
	if err := appendFullTrustAudit(id, entry); err != nil {
		log.Printf("plugin %s: full trust audit: %v", strings.TrimSpace(id), err)
	}
	return true
}

// revokeFullTrust ends trust actively: "revoked" is the user's choice,
// anything else (disabled, uninstalled, upgraded, cleared) a lifecycle lapse.
// Either is audited and announced only when there was a record to delete, so
// revoking a legacy-only trust leaves no full-trust "revoked" entry.
func (manager *pluginManager) revokeFullTrust(id, reason string) {
	id = strings.TrimSpace(id)
	if !pluginIDPattern.MatchString(id) {
		return
	}
	kind := fullTrustAuditLapsed
	if reason == fullTrustReasonRevoked {
		kind = fullTrustAuditRevoked
	}
	fullTrustMu.Lock()
	_, found, err := pluginKVGetRaw(hostPluginID, fullTrustKey(id))
	if err == nil && found {
		err = pluginKVDeleteRaw(hostPluginID, fullTrustKey(id))
	}
	fullTrustMu.Unlock()
	if err != nil {
		// The record may still be there: do not announce an end that did not happen.
		log.Printf("plugin %s: revoke full trust: %v", id, err)
		return
	}
	if found {
		manager.announceFullTrustEnd(id, kind, reason)
	}
}
