package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"mincode/app/backend/core"
)

// The plugin_full_trust_* commands are the only way to grant, revoke and
// inspect full trust. They are core commands for the main WebView; no host.*
// method, page bridge request or AI tool reaches them (design
// docs/superpowers/specs/2026-11-03-plugin-full-trust-design.md).

type fullTrustArgs struct {
	PluginID    string `json:"pluginId"`
	Trusted     *bool  `json:"trusted,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

type fullTrustPluginArgs struct {
	PluginID string `json:"pluginId"`
}

// fullTrustGetArgs: Full asks for the fingerprint the user is about to
// trust, which hashes the whole package. Without it get reports the record
// and eligibility only, cheap enough for every plugin card.
type fullTrustGetArgs struct {
	PluginID string `json:"pluginId"`
	Full     bool   `json:"full,omitempty"`
}

type fullTrustAuthorizeArgs struct {
	PluginID string `json:"pluginId"`
	Surface  string `json:"surface"`
	Risk     string `json:"risk"`
	Subject  string `json:"subject"`
}

// fullTrustFrontendSurfaces are the confirmations the frontend shows itself.
// pluginCall, serviceCall and hostService are decided in Go and never
// accepted here, so the frontend cannot forge Go-side audit entries.
var fullTrustFrontendSurfaces = map[string]bool{"viewAction": true, "codeLens": true, "notification": true, "tray": true, "testRun": true}

const (
	fullTrustReasonGranted = "granted"
	fullTrustReasonLegacy  = "legacy"
)

type fullTrustListEntry struct {
	PluginID  string `json:"pluginId"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	GrantedAt int64  `json:"grantedAt"`
}

type fullTrustAuthorizeResult struct {
	Allowed bool   `json:"allowed"`
	Lapsed  string `json:"lapsed,omitempty"`
}

type fullTrustCommand func(manager *pluginManager, argsJSON string) (any, error)

var fullTrustCommands = map[string]fullTrustCommand{
	"plugin_full_trust_get":         fullTrustGetCommand,
	"plugin_full_trust_set":         fullTrustSetCommand,
	"plugin_full_trust_list":        fullTrustListCommand,
	"plugin_full_trust_authorize":   fullTrustAuthorizeCommand,
	"plugin_full_trust_audit":       fullTrustAuditCommand,
	"plugin_full_trust_audit_clear": fullTrustAuditClearCommand,
}

func init() {
	for name, command := range fullTrustCommands {
		core.Register(name, func(_, argsJSON string) (string, error) {
			result, err := command(host(), argsJSON)
			if err != nil {
				return "", err
			}
			return core.EncodeResult(result)
		})
	}
}

// decodeFullTrustArgs decodes strictly: unknown fields and trailing values
// are errors. An empty string is an empty object.
func decodeFullTrustArgs(argsJSON string, destination any) error {
	if strings.TrimSpace(argsJSON) == "" {
		argsJSON = "{}"
	}
	if err := decodeStrictJSONValue([]byte(argsJSON), destination, true, "args"); err != nil {
		return fmt.Errorf("invalid args: %w", err)
	}
	return nil
}

func fullTrustPluginID(raw string) (string, error) {
	id, err := validatePluginID(raw)
	if err != nil {
		return "", err
	}
	if !pluginIDPattern.MatchString(id) {
		return "", fmt.Errorf("full trust: invalid plugin id %q", id)
	}
	return id, nil
}

func fullTrustGetCommand(manager *pluginManager, argsJSON string) (any, error) {
	var args fullTrustGetArgs
	if err := decodeFullTrustArgs(argsJSON, &args); err != nil {
		return nil, err
	}
	id, err := fullTrustPluginID(args.PluginID)
	if err != nil {
		return nil, err
	}
	return manager.fullTrustView(id, args.Full)
}

func fullTrustSetCommand(manager *pluginManager, argsJSON string) (any, error) {
	var args fullTrustArgs
	if err := decodeFullTrustArgs(argsJSON, &args); err != nil {
		return nil, err
	}
	id, err := fullTrustPluginID(args.PluginID)
	if err != nil {
		return nil, err
	}
	if args.Trusted == nil {
		return nil, errors.New("trusted is required")
	}
	return manager.setFullTrust(id, *args.Trusted, strings.TrimSpace(args.Fingerprint))
}

func fullTrustListCommand(manager *pluginManager, argsJSON string) (any, error) {
	var args struct{}
	if err := decodeFullTrustArgs(argsJSON, &args); err != nil {
		return nil, err
	}
	plugins, err := manager.listFullTrust()
	if err != nil {
		return nil, err
	}
	return map[string]any{"plugins": plugins}, nil
}

func fullTrustAuthorizeCommand(manager *pluginManager, argsJSON string) (any, error) {
	var args fullTrustAuthorizeArgs
	if err := decodeFullTrustArgs(argsJSON, &args); err != nil {
		return nil, err
	}
	allowed, lapsed, err := manager.authorizeFullTrust(args)
	if err != nil {
		return nil, err
	}
	return fullTrustAuthorizeResult{Allowed: allowed, Lapsed: lapsed}, nil
}

func fullTrustAuditCommand(_ *pluginManager, argsJSON string) (any, error) {
	var args fullTrustPluginArgs
	if err := decodeFullTrustArgs(argsJSON, &args); err != nil {
		return nil, err
	}
	id, err := fullTrustPluginID(args.PluginID)
	if err != nil {
		return nil, err
	}
	entries, err := readFullTrustAudit(id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"entries": entries}, nil
}

func fullTrustAuditClearCommand(_ *pluginManager, argsJSON string) (any, error) {
	var args fullTrustPluginArgs
	if err := decodeFullTrustArgs(argsJSON, &args); err != nil {
		return nil, err
	}
	id, err := fullTrustPluginID(args.PluginID)
	if err != nil {
		return nil, err
	}
	return nil, clearFullTrustAudit(id)
}

// fullTrustView is fullTrustStatus with the subject's eligibility filled in.
// A stored record is always re-checked against the package (that hashes it);
// without one the package is hashed only when full is set, so the subject's
// fingerprint is the one the user is about to trust.
func (manager *pluginManager) fullTrustView(id string, full bool) (FullTrustStatus, error) {
	status, err := manager.fullTrustStatus(id)
	if err != nil {
		return status, err
	}
	if status.Subject.Eligible || status.Subject.Reason != "" {
		return status, nil // the record was checked: the subject is complete
	}
	if full {
		status.Subject, err = manager.fullTrustSubject(id)
		return status, err
	}
	status.Subject, _ = manager.fullTrustEligibility(id)
	return status, nil
}

func deleteLegacyDangerousTrust(id string) error {
	if err := pluginKVDeleteRaw(hostPluginID, pluginDangerousActionTrustKey(id)); err != nil {
		return fmt.Errorf("delete legacy dangerous action trust: %w", err)
	}
	return nil
}

// setFullTrust grants (trusted) or revokes full trust. A grant needs an
// unlocked app, an eligible plugin and reviewed equal to the fingerprint the
// plugin has right now, so the user trusts exactly what they were shown.
// Both directions drop the legacy dangerous-action record.
func (manager *pluginManager) setFullTrust(id string, trusted bool, reviewed string) (FullTrustStatus, error) {
	id = strings.TrimSpace(id)
	if !pluginIDPattern.MatchString(id) {
		return FullTrustStatus{PluginID: id}, fmt.Errorf("full trust: invalid plugin id %q", id)
	}
	if !trusted {
		manager.revokeFullTrust(id, fullTrustReasonRevoked)
		if err := deleteLegacyDangerousTrust(id); err != nil {
			return FullTrustStatus{PluginID: id}, err
		}
		return manager.fullTrustView(id, false)
	}
	if appLocked() {
		return FullTrustStatus{PluginID: id}, errors.New("locked: unlock MinCode before trusting a plugin")
	}
	subject, err := manager.fullTrustSubject(id)
	if err != nil {
		return FullTrustStatus{PluginID: id}, err
	}
	if !subject.Eligible {
		return FullTrustStatus{PluginID: id}, fmt.Errorf("not_eligible:%s", subject.Reason)
	}
	if reviewed == "" || reviewed != subject.Fingerprint {
		return FullTrustStatus{PluginID: id}, errors.New("changed: the plugin changed since it was reviewed; review it again")
	}
	record := subject.record
	record.GrantedAt = fullTrustNow().UnixMilli()
	raw, err := json.Marshal(record)
	if err != nil {
		return FullTrustStatus{PluginID: id}, fmt.Errorf("encode full trust: %w", err)
	}
	fullTrustMu.Lock()
	err = pluginKVSetRaw(hostPluginID, fullTrustKey(id), raw)
	fullTrustMu.Unlock()
	if err != nil {
		return FullTrustStatus{PluginID: id}, fmt.Errorf("write full trust: %w", err)
	}
	if err := deleteLegacyDangerousTrust(id); err != nil {
		log.Printf("plugin %s: %v", id, err)
	}
	if err := appendFullTrustAudit(id, FullTrustAuditEntry{Kind: fullTrustAuditGranted}); err != nil {
		log.Printf("plugin %s: full trust audit: %v", id, err)
	}
	hostUIEmit(fullTrustChangedEvent, map[string]any{"pluginId": id, "trusted": true, "reason": fullTrustReasonGranted})
	return manager.fullTrustView(id, false)
}

// listFullTrust returns the plugins whose trust still holds, in manager
// order. Only plugins with a stored record are re-checked (and hashed).
func (manager *pluginManager) listFullTrust() ([]fullTrustListEntry, error) {
	manager.mu.RLock()
	ids := append([]string(nil), manager.order...)
	manager.mu.RUnlock()
	plugins := []fullTrustListEntry{}
	for _, id := range ids {
		if !pluginIDPattern.MatchString(id) {
			continue
		}
		_, found, err := pluginKVGetRaw(hostPluginID, fullTrustKey(id))
		if err != nil {
			return nil, fmt.Errorf("read full trust: %w", err)
		}
		if !found {
			continue
		}
		status, err := manager.fullTrustStatus(id)
		if err != nil {
			log.Printf("plugin %s: full trust status: %v", id, err)
			continue
		}
		if status.Trusted {
			plugins = append(plugins, fullTrustListEntry{PluginID: id, Name: status.Subject.Name, Version: status.Subject.Version, GrantedAt: status.GrantedAt})
		}
	}
	return plugins, nil
}

func normalizeFullTrustRisk(risk string) string {
	switch risk = strings.TrimSpace(risk); risk {
	case "network", "write", "dangerous":
		return risk
	default:
		return "dangerous"
	}
}

// authorizeFullTrust answers a confirmation the frontend is about to show:
// allowed means skip it (the skip is audited). Full trust needs an unlocked
// app; a legacy dangerous-action record still covers only dangerous page
// actions of the version it was given for. lapsed reports trust that ended on
// this very read, so the frontend can say why it asks again.
func (manager *pluginManager) authorizeFullTrust(args fullTrustAuthorizeArgs) (allowed bool, lapsed string, err error) {
	id, err := fullTrustPluginID(args.PluginID)
	if err != nil {
		return false, "", err
	}
	surface := strings.TrimSpace(args.Surface)
	if !fullTrustFrontendSurfaces[surface] {
		return false, "", fmt.Errorf("full trust: unsupported surface %q", args.Surface)
	}
	risk := normalizeFullTrustRisk(args.Risk)
	entry := FullTrustAuditEntry{Kind: fullTrustAuditSkipped, Surface: surface, Risk: risk,
		Subject: cleanHostText(args.Subject, fullTrustAuditSubjectMaxRunes, false)}
	status, err := manager.fullTrustStatus(id)
	if err != nil {
		return false, "", err
	}
	if status.Lapsed != "" {
		return false, status.Lapsed, nil
	}
	if appLocked() {
		return false, "", nil
	}
	if !status.Trusted {
		if !status.LegacyDangerous || surface != "viewAction" || risk != "dangerous" {
			return false, "", nil
		}
		entry.Reason = fullTrustReasonLegacy
	}
	if err := appendFullTrustAudit(id, entry); err != nil {
		log.Printf("plugin %s: full trust audit: %v", id, err)
	}
	return true, "", nil
}
