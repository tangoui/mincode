package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"mincode/app/backend/core"
	"mincode/app/plugins/api"
)

// Credential-release grants for plugin database drivers.
//
// A saved connection's decrypted credentials cross to a driver plugin only
// when this host store records that the user bound exactly that connection
// to exactly that (plugin, driver) on this machine. The grant is written by
// the connection dialog's Save/Test (picking the plugin driver there is the
// user's decision) or by the protected "send credentials to plugin X"
// confirmation after an untrusted: connect error. It is required in ask,
// agent and full-access alike; these commands are host-frame only and no AI
// tool dispatches them.
//
// Storage: host KV "<pluginId>.databaseDrivers.grants" in the __host__
// namespace = {version:1, connections:{<connectionId>:{driverId, grantedAt}}}.
// Configuration export/import, the plugin data archive and configuration sync
// never include the __host__ namespace (only "%.configuration" keys travel),
// so imported or synced connections arrive without grants. Grants survive
// disable and upgrade and are deleted on uninstall and Clear Plugin Data.
//
// Per-plugin trust: the user may explicitly mark a driver plugin as trusted
// (plugin manager switch after a protected confirm, or the "always trust"
// checkbox of the credential-release confirm). While trusted, every
// connection bound to one of that plugin's drivers releases its credentials
// without a per-connection grant; the capability, enabled/effective and
// driver gates still apply. Stored as host KV "<pluginId>.databaseDrivers.trusted"
// in the same never-exported __host__ namespace, host-frame only (no AI
// tool dispatches the setter), deleted on uninstall and Clear Plugin Data.
// Untrusting keeps explicit grants; connections without one prompt again.

const (
	databaseDriverGrantVersion      = 1
	databaseDriverGrantKeySuffix    = ".databaseDrivers.grants"
	databaseDriverTrustKeySuffix    = ".databaseDrivers.trusted"
	databaseDriverTrustVersion      = 1
	databaseDriverConnectionIDMax   = 128
	databaseDriverStatusRefsMax     = 256
	databaseDriverPruneIDsMax       = 10000
	databaseDriverErrNotFound       = "not_found"
	databaseDriverErrUnavailable    = "unavailable"
	databaseDriverErrForbidden      = "forbidden"
	databaseDriverErrUntrusted      = "untrusted"
	databaseDriverErrProtocol       = "protocol"
	databaseDriverErrTimeout        = "timeout"
	databaseDriverErrCancelled      = "cancelled"
	databaseDriverErrDriver         = "driver_error"
	databaseDriverErrTooLarge       = "too_large"
	databaseDriverErrBusy           = "busy"
	databaseDriverErrInvalidRequest = "invalid"
)

type databaseDriverGrant struct {
	DriverID  string `json:"driverId"`
	GrantedAt int64  `json:"grantedAt"`
}

type databaseDriverGrantStore struct {
	Version     int                            `json:"version"`
	Connections map[string]databaseDriverGrant `json:"connections"`
}

// databaseDriverGrantsMu serializes every read-modify-write of grant records.
var databaseDriverGrantsMu sync.Mutex

func databaseDriverError(code, format string, args ...any) error {
	return fmt.Errorf("%s: %s", code, fmt.Sprintf(format, args...))
}

func databaseDriverGrantKey(pluginID string) string {
	return pluginID + databaseDriverGrantKeySuffix
}

func validateDatabaseDriverConnectionID(connectionID string) error {
	if connectionID == "" || len(connectionID) > databaseDriverConnectionIDMax || !utf8.ValidString(connectionID) ||
		strings.TrimSpace(connectionID) != connectionID || checkContributionRunes(connectionID, false, false) != nil {
		return databaseDriverError(databaseDriverErrInvalidRequest, "connectionId must be 1-%d printable characters", databaseDriverConnectionIDMax)
	}
	return nil
}

// readDatabaseDriverGrantsLocked returns the plugin's grants; an unreadable
// or foreign-version record means "no grants". Caller holds databaseDriverGrantsMu.
func readDatabaseDriverGrantsLocked(pluginID string) (databaseDriverGrantStore, error) {
	store := databaseDriverGrantStore{Version: databaseDriverGrantVersion, Connections: map[string]databaseDriverGrant{}}
	raw, found, err := pluginKVGetRaw(hostPluginID, databaseDriverGrantKey(pluginID))
	if err != nil || !found {
		return store, err
	}
	var stored databaseDriverGrantStore
	if json.Unmarshal(raw, &stored) != nil || stored.Version != databaseDriverGrantVersion {
		return store, nil
	}
	for connectionID, grant := range stored.Connections {
		if validateDatabaseDriverConnectionID(connectionID) == nil && grant.DriverID != "" {
			store.Connections[connectionID] = grant
		}
	}
	return store, nil
}

func writeDatabaseDriverGrantsLocked(pluginID string, store databaseDriverGrantStore) error {
	if len(store.Connections) == 0 {
		return pluginKVDeleteRaw(hostPluginID, databaseDriverGrantKey(pluginID))
	}
	store.Version = databaseDriverGrantVersion
	raw, err := json.Marshal(store)
	if err != nil {
		return err
	}
	return pluginKVSetRaw(hostPluginID, databaseDriverGrantKey(pluginID), raw)
}

// databaseDriverGranted reports whether connectionID is bound to (pluginID, driverID).
func databaseDriverGranted(pluginID, driverID, connectionID string) (bool, error) {
	databaseDriverGrantsMu.Lock()
	defer databaseDriverGrantsMu.Unlock()
	store, err := readDatabaseDriverGrantsLocked(pluginID)
	if err != nil {
		return false, err
	}
	grant, ok := store.Connections[connectionID]
	return ok && grant.DriverID == driverID, nil
}

// grantDatabaseDriver binds connectionID to (pluginID, driverID). The driver
// must currently be offered (same gates as connect, without the grant). A
// connection is bound to at most one plugin driver: other plugins' grants
// for the same connection are removed.
func (manager *pluginManager) grantDatabaseDriver(pluginID, driverID, connectionID string) error {
	if err := validateDatabaseDriverConnectionID(connectionID); err != nil {
		return err
	}
	if _, err := manager.databaseDriverSourceFor(pluginID, driverID); err != nil {
		return err
	}
	databaseDriverGrantsMu.Lock()
	defer databaseDriverGrantsMu.Unlock()
	if _, err := revokeDatabaseDriverGrantsLocked(connectionID, "", pluginID); err != nil {
		return err
	}
	store, err := readDatabaseDriverGrantsLocked(pluginID)
	if err != nil {
		return err
	}
	store.Connections[connectionID] = databaseDriverGrant{DriverID: driverID, GrantedAt: time.Now().UnixMilli()}
	return writeDatabaseDriverGrantsLocked(pluginID, store)
}

// databaseDriverGrantPluginIDs lists plugin ids that have a grant record.
func databaseDriverGrantPluginIDs() ([]string, error) {
	keys, err := pluginKVListRaw(hostPluginID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, key := range keys {
		if id, ok := strings.CutSuffix(key, databaseDriverGrantKeySuffix); ok && id != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// revokeDatabaseDriverGrantsLocked removes connectionID's grant from pluginID
// (or from every plugin when pluginID is empty), skipping keepPluginID.
func revokeDatabaseDriverGrantsLocked(connectionID, pluginID, keepPluginID string) (int, error) {
	ids := []string{pluginID}
	if pluginID == "" {
		var err error
		if ids, err = databaseDriverGrantPluginIDs(); err != nil {
			return 0, err
		}
	}
	removed := 0
	for _, id := range ids {
		if id == keepPluginID {
			continue
		}
		store, err := readDatabaseDriverGrantsLocked(id)
		if err != nil {
			return removed, err
		}
		if _, ok := store.Connections[connectionID]; !ok {
			continue
		}
		delete(store.Connections, connectionID)
		if err := writeDatabaseDriverGrantsLocked(id, store); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

func revokeDatabaseDriverGrant(connectionID, pluginID string) (int, error) {
	if err := validateDatabaseDriverConnectionID(connectionID); err != nil {
		return 0, err
	}
	databaseDriverGrantsMu.Lock()
	defer databaseDriverGrantsMu.Unlock()
	return revokeDatabaseDriverGrantsLocked(connectionID, strings.TrimSpace(pluginID), "")
}

// pruneDatabaseDriverGrants drops every grant whose connection is not live.
func pruneDatabaseDriverGrants(liveConnectionIDs []string) (int, error) {
	if len(liveConnectionIDs) > databaseDriverPruneIDsMax {
		return 0, databaseDriverError(databaseDriverErrInvalidRequest, "liveConnectionIds must list at most %d ids", databaseDriverPruneIDsMax)
	}
	live := make(map[string]bool, len(liveConnectionIDs))
	for _, id := range liveConnectionIDs {
		live[id] = true
	}
	databaseDriverGrantsMu.Lock()
	defer databaseDriverGrantsMu.Unlock()
	ids, err := databaseDriverGrantPluginIDs()
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, id := range ids {
		store, err := readDatabaseDriverGrantsLocked(id)
		if err != nil {
			return removed, err
		}
		changed := false
		for connectionID := range store.Connections {
			if !live[connectionID] {
				delete(store.Connections, connectionID)
				removed++
				changed = true
			}
		}
		if changed {
			if err := writeDatabaseDriverGrantsLocked(id, store); err != nil {
				return removed, err
			}
		}
	}
	return removed, nil
}

func deleteDatabaseDriverGrants(pluginID string) {
	databaseDriverGrantsMu.Lock()
	defer databaseDriverGrantsMu.Unlock()
	_ = pluginKVDeleteRaw(hostPluginID, databaseDriverGrantKey(pluginID))
	_ = pluginKVDeleteRaw(hostPluginID, databaseDriverTrustKey(pluginID))
}

type databaseDriverTrustRecord struct {
	Version   int   `json:"version"`
	Trusted   bool  `json:"trusted"`
	TrustedAt int64 `json:"trustedAt"`
}

func databaseDriverTrustKey(pluginID string) string {
	return pluginID + databaseDriverTrustKeySuffix
}

func validateDatabaseDriverPluginID(pluginID string) error {
	if pluginID == "" || len(pluginID) > databaseDriverConnectionIDMax || !utf8.ValidString(pluginID) ||
		strings.TrimSpace(pluginID) != pluginID || checkContributionRunes(pluginID, false, false) != nil {
		return databaseDriverError(databaseDriverErrInvalidRequest, "pluginId must be 1-%d printable characters", databaseDriverConnectionIDMax)
	}
	return nil
}

// databaseDriverPluginTrusted reports whether the user explicitly trusts
// pluginID to receive credentials of connections bound to its drivers. An
// unreadable or foreign-version record means "not trusted".
func databaseDriverPluginTrusted(pluginID string) (bool, error) {
	if validateDatabaseDriverPluginID(pluginID) != nil {
		return false, nil
	}
	databaseDriverGrantsMu.Lock()
	defer databaseDriverGrantsMu.Unlock()
	raw, found, err := pluginKVGetRaw(hostPluginID, databaseDriverTrustKey(pluginID))
	if err != nil || !found {
		return false, err
	}
	var record databaseDriverTrustRecord
	if json.Unmarshal(raw, &record) != nil || record.Version != databaseDriverTrustVersion {
		return false, nil
	}
	return record.Trusted, nil
}

// setDatabaseDriverTrust records the user's explicit per-plugin trust
// decision. Trusting requires an installed plugin whose manifest declares the
// database.driver capability; untrusting only deletes the record and keeps
// every explicit per-connection grant.
func (manager *pluginManager) setDatabaseDriverTrust(pluginID string, trusted bool) error {
	if err := validateDatabaseDriverPluginID(pluginID); err != nil {
		return err
	}
	if trusted {
		record, err := manager.record(pluginID)
		if err != nil {
			return databaseDriverError(databaseDriverErrNotFound, "plugin %s is not installed", pluginID)
		}
		record.mu.Lock()
		manifest := record.manifest
		record.mu.Unlock()
		if manifest == nil || !slices.Contains(manifest.Capabilities, api.CapabilityDatabaseDriver) {
			return databaseDriverError(databaseDriverErrForbidden, "plugin %s does not declare the database.driver capability", pluginID)
		}
	}
	databaseDriverGrantsMu.Lock()
	defer databaseDriverGrantsMu.Unlock()
	if !trusted {
		return pluginKVDeleteRaw(hostPluginID, databaseDriverTrustKey(pluginID))
	}
	raw, err := json.Marshal(databaseDriverTrustRecord{Version: databaseDriverTrustVersion, Trusted: true, TrustedAt: time.Now().UnixMilli()})
	if err != nil {
		return err
	}
	return pluginKVSetRaw(hostPluginID, databaseDriverTrustKey(pluginID), raw)
}

// databaseDriverReleaseAllowed reports whether connectionID's credentials may
// cross to (pluginID, driverID): an explicit grant, or explicit plugin trust.
func databaseDriverReleaseAllowed(pluginID, driverID, connectionID string) (bool, error) {
	granted, err := databaseDriverGranted(pluginID, driverID, connectionID)
	if err != nil || granted {
		return granted, err
	}
	return databaseDriverPluginTrusted(pluginID)
}

// ---------------------------------------------------------------------------
// frontend commands (host frame only).

type databaseDriverRef struct {
	ConnectionID string `json:"connectionId"`
	PluginID     string `json:"pluginId"`
	DriverID     string `json:"driverId"`
}

type databaseDriverGrantRequest struct {
	PluginID     string `json:"pluginId"`
	DriverID     string `json:"driverId"`
	ConnectionID string `json:"connectionId"`
}

type databaseDriverRevokeRequest struct {
	ConnectionID string `json:"connectionId"`
	PluginID     string `json:"pluginId"`
}

type databaseDriverPruneRequest struct {
	LiveConnectionIDs []string `json:"liveConnectionIds"`
}

type databaseDriverStatusRequest struct {
	Refs []databaseDriverRef `json:"refs"`
}

// DatabaseDriverStatus is one connection's driver availability. Reason is
// set when Available is false.
type DatabaseDriverStatus struct {
	ConnectionID string `json:"connectionId"`
	PluginID     string `json:"pluginId"`
	DriverID     string `json:"driverId"`
	Available    bool   `json:"available"`
	Reason       string `json:"reason,omitempty"`
	Granted      bool   `json:"granted"`
	// Trusted is true when the user trusts the plugin: connecting releases
	// credentials without a per-connection grant.
	Trusted bool `json:"trusted"`
}

func (manager *pluginManager) databaseDriverStatuses(refs []databaseDriverRef) ([]DatabaseDriverStatus, error) {
	if len(refs) > databaseDriverStatusRefsMax {
		return nil, databaseDriverError(databaseDriverErrInvalidRequest, "refs must list at most %d connections", databaseDriverStatusRefsMax)
	}
	out := make([]DatabaseDriverStatus, 0, len(refs))
	for _, ref := range refs {
		status := DatabaseDriverStatus{ConnectionID: ref.ConnectionID, PluginID: ref.PluginID, DriverID: ref.DriverID}
		status.Reason = manager.databaseDriverUnavailableReason(ref.PluginID, ref.DriverID)
		status.Available = status.Reason == ""
		if validateDatabaseDriverConnectionID(ref.ConnectionID) == nil {
			granted, err := databaseDriverGranted(strings.TrimSpace(ref.PluginID), strings.TrimSpace(ref.DriverID), ref.ConnectionID)
			if err != nil {
				return nil, err
			}
			status.Granted = granted
		}
		trusted, err := databaseDriverPluginTrusted(strings.TrimSpace(ref.PluginID))
		if err != nil {
			return nil, err
		}
		status.Trusted = trusted
		out = append(out, status)
	}
	return out, nil
}

func pluginHostDatabaseDriverGrantHandler(_, args string) (string, error) {
	var request databaseDriverGrantRequest
	if err := core.DecodeArgsPayload(args, &request); err != nil {
		return "", err
	}
	if err := host().grantDatabaseDriver(strings.TrimSpace(request.PluginID), strings.TrimSpace(request.DriverID), request.ConnectionID); err != nil {
		return "", err
	}
	return core.EncodeResult(map[string]bool{"granted": true})
}

func pluginHostDatabaseDriverRevokeHandler(_, args string) (string, error) {
	var request databaseDriverRevokeRequest
	if err := core.DecodeArgsPayload(args, &request); err != nil {
		return "", err
	}
	removed, err := revokeDatabaseDriverGrant(request.ConnectionID, request.PluginID)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(map[string]int{"revoked": removed})
}

func pluginHostDatabaseDriverPruneHandler(_, args string) (string, error) {
	var request databaseDriverPruneRequest
	if err := core.DecodeArgsPayload(args, &request); err != nil {
		return "", err
	}
	if request.LiveConnectionIDs == nil {
		return "", errors.New("invalid: liveConnectionIds is required")
	}
	removed, err := pruneDatabaseDriverGrants(request.LiveConnectionIDs)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(map[string]int{"removed": removed})
}

func pluginHostDatabaseDriverStatusHandler(_, args string) (string, error) {
	var request databaseDriverStatusRequest
	if err := core.DecodeArgsPayload(args, &request); err != nil {
		return "", err
	}
	items, err := host().databaseDriverStatuses(request.Refs)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(map[string]any{"items": items})
}

type databaseDriverTrustRequest struct {
	PluginID string `json:"pluginId"`
	Trusted  *bool  `json:"trusted"`
}

func pluginHostDatabaseDriverTrustGetHandler(_, args string) (string, error) {
	var request databaseDriverTrustRequest
	if err := core.DecodeArgsPayload(args, &request); err != nil {
		return "", err
	}
	pluginID := strings.TrimSpace(request.PluginID)
	if err := validateDatabaseDriverPluginID(pluginID); err != nil {
		return "", err
	}
	trusted, err := databaseDriverPluginTrusted(pluginID)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(map[string]any{"pluginId": pluginID, "trusted": trusted})
}

// pluginHostDatabaseDriverTrustSetHandler is a frontend command for the host
// frame only: no AI tool, plugin RPC or plugin UI action dispatches it.
func pluginHostDatabaseDriverTrustSetHandler(_, args string) (string, error) {
	var request databaseDriverTrustRequest
	if err := core.DecodeArgsPayload(args, &request); err != nil {
		return "", err
	}
	if request.Trusted == nil {
		return "", errors.New("invalid: trusted is required")
	}
	pluginID := strings.TrimSpace(request.PluginID)
	if err := host().setDatabaseDriverTrust(pluginID, *request.Trusted); err != nil {
		return "", err
	}
	return core.EncodeResult(map[string]any{"pluginId": pluginID, "trusted": *request.Trusted})
}

func init() {
	core.Register("plugin_host_database_driver_trust_get", pluginHostDatabaseDriverTrustGetHandler)
	core.Register("plugin_host_database_driver_trust_set", pluginHostDatabaseDriverTrustSetHandler)
	core.Register("plugin_host_database_driver_grant", pluginHostDatabaseDriverGrantHandler)
	core.Register("plugin_host_database_driver_revoke", pluginHostDatabaseDriverRevokeHandler)
	core.Register("plugin_host_database_driver_prune", pluginHostDatabaseDriverPruneHandler)
	core.Register("plugin_host_database_driver_status", pluginHostDatabaseDriverStatusHandler)
	registerRecordRemovalHook(func(_ *pluginManager, pluginID string, _ removalReason) {
		deleteDatabaseDriverGrants(pluginID)
	})
}
