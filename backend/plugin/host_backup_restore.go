package plugin

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mincode/app/backend/backup"
	"mincode/app/plugins/api"
)

// Opening and restoring a backup (design 4.4-4.5). open decrypts a staged
// backup into an in-memory session; prepareRestore shows the host
// confirmation and stages a pre-restore snapshot of this device; applyRestore
// writes MinCode's own data through the frontend (backupApply), then every
// plugin's data, and puts the frontend back if the plugins fail. The plugin
// sees section ids, counts, tokens and ciphertext only, never values.
//
// Frontend kinds added here:
//   - backupRestoreConfirm {mode, changes: [{section, sectionTitle, kind,
//     label}], pluginName} → {confirmed}
//   - backupApply {sets: {<key>: {storage, value}}, deletes: [<key>]} → {ok}
//
// Once a restore that wrote MinCode's own data (step 4 started) is over —
// restored, failed and rolled back, or uncertain — the host emits
// plugin:backup-restore-finished {restoreId, ok}; the frontend keeps its
// stores frozen until then and only then reloads.
const (
	pluginBackupRestoreFinishedEvent = "plugin:backup-restore-finished"

	backupKindRestoreConfirm = "backupRestoreConfirm"
	backupKindApply          = "backupApply"

	backupRestoreConfirmTimeout = 10 * time.Minute

	backupOpenSessionTTL = 30 * time.Minute
	backupRestoreTTL     = 30 * time.Minute

	// backupConfirmMaxChanges bounds the rows sent to the confirmation; a
	// final {kind: "more", label: "<remaining>"} row stands for the rest.
	backupConfirmMaxChanges = 2000

	backupSectionTitleMaxRunes = 80
)

// backupDeferredRestoreDelay is how long after applyRestore returns the
// caller's own data is written (its process is stopped for that).
var backupDeferredRestoreDelay = 500 * time.Millisecond

// backupApplyTimeout bounds each backupApply request; a variable for tests.
var backupApplyTimeout = 2 * time.Minute

// backupDeferredRunning counts scheduled deferred restores; tests wait on it.
var backupDeferredRunning atomic.Int64

func init() {
	registerHostMethod("host.backup.open", nil, api.CapabilityBackup, backupMethod(hostBackupOpen))
	registerHostMethod("host.backup.prepareRestore", nil, api.CapabilityBackup, backupMethod(hostBackupPrepareRestore))
	registerHostMethod("host.backup.applyRestore", nil, api.CapabilityBackup, backupApplyMethod(hostBackupApplyRestore))
	registerHostMethod("host.backup.close", nil, api.CapabilityBackup, backupMethod(hostBackupClose))
	registerRecordLifecycleHook(func(_ *pluginManager, record *pluginRecord) {
		clearBackupRestoreState(record.id)
	})
	registerRecordRemovalHook(func(_ *pluginManager, pluginID string, _ removalReason) {
		clearBackupRestoreState(pluginID)
		cancelBackupDeferredRestore(pluginID)
	})
}

// ---------------------------------------------------------------------------
// Method guard.

// backupRestoring marks plugins whose applyRestore is running. Their other
// host.backup.* calls are refused before reaching the frontend, so nothing
// the plugin does can hold a request open while the restore writes data.
var (
	backupRestoringMu sync.Mutex
	backupRestoring   = map[string]bool{}
)

func backupRestoreInProgressError() *rpcError {
	return &rpcError{Code: -32004, Message: "a restore from this plugin is running", Data: map[string]string{"code": "restoreInProgress"}}
}

func backupRestoreRunning(pluginID string) bool {
	backupRestoringMu.Lock()
	defer backupRestoringMu.Unlock()
	return backupRestoring[pluginID]
}

// backupMethod wraps every host.backup.* handler: expired sessions and
// restores are swept, and calls during the plugin's restore are refused.
func backupMethod(handler hostMethodHandler) hostMethodHandler {
	return func(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
		sweepBackupRestoreState()
		if backupRestoreRunning(runtimeHost.pluginID) {
			return nil, backupRestoreInProgressError()
		}
		return handler(runtimeHost, params)
	}
}

// backupApplyMethod is backupMethod for applyRestore, which also marks the
// plugin as restoring for the duration of the call.
func backupApplyMethod(handler hostMethodHandler) hostMethodHandler {
	return func(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
		sweepBackupRestoreState()
		pluginID := runtimeHost.pluginID
		backupRestoringMu.Lock()
		if backupRestoring[pluginID] {
			backupRestoringMu.Unlock()
			return nil, backupRestoreInProgressError()
		}
		backupRestoring[pluginID] = true
		backupRestoringMu.Unlock()
		defer func() {
			backupRestoringMu.Lock()
			delete(backupRestoring, pluginID)
			backupRestoringMu.Unlock()
		}()
		return handler(runtimeHost, params)
	}
}

// Deferred self-restores waiting for their timer, by plugin. Uninstall and
// Clear Plugin Data cancel them.
var (
	backupDeferredMu     sync.Mutex
	backupDeferredTimers = map[string]*time.Timer{}
)

func cancelBackupDeferredRestore(pluginID string) {
	backupDeferredMu.Lock()
	defer backupDeferredMu.Unlock()
	if timer := backupDeferredTimers[pluginID]; timer != nil {
		delete(backupDeferredTimers, pluginID)
		if timer.Stop() {
			backupDeferredRunning.Add(-1)
		}
	}
}

func scheduleBackupDeferredRestore(manager *pluginManager, ctx context.Context, plan pluginRestorePlan) {
	pluginID := plan.ID
	backupDeferredMu.Lock()
	defer backupDeferredMu.Unlock()
	if previous := backupDeferredTimers[pluginID]; previous != nil && previous.Stop() {
		backupDeferredRunning.Add(-1)
	}
	backupDeferredRunning.Add(1)
	var timer *time.Timer
	timer = time.AfterFunc(backupDeferredRestoreDelay, func() {
		defer backupDeferredRunning.Add(-1)
		backupDeferredMu.Lock()
		current := backupDeferredTimers[pluginID] == timer
		if current {
			delete(backupDeferredTimers, pluginID)
		}
		backupDeferredMu.Unlock()
		if !current {
			return // cancelled after the timer fired
		}
		plan, err := keepLocalRunJournal(plan)
		if err == nil {
			err = manager.applyDeferredPluginRestore(ctx, plan)
		}
		if err != nil {
			manager.logBackupRestore(pluginID, "error", "restoring this plugin's own data failed; its data was left as before")
		}
	})
	backupDeferredTimers[pluginID] = timer
}

// backupRunJournalKey is the KV key where the backup plugin journals the
// backup it is uploading. It describes this device's run, so a restore of
// the plugin's own data never replaces it.
const backupRunJournalKey = "activeRun"

// keepLocalRunJournal returns plan with the run journal taken from the
// device as it is now (or left out when there is none).
func keepLocalRunJournal(plan pluginRestorePlan) (pluginRestorePlan, error) {
	local, found, err := pluginKVGetRaw(plan.ID, backupRunJournalKey)
	if err != nil {
		return plan, err
	}
	kv := make(map[string]json.RawMessage, len(plan.KV))
	for key, value := range plan.KV {
		kv[key] = value
	}
	delete(kv, backupRunJournalKey)
	if found {
		kv[backupRunJournalKey] = local
	}
	plan.KV = kv
	return plan, nil
}

// ---------------------------------------------------------------------------
// Errors.

func backupOpenError(err error) *rpcError {
	switch {
	case errors.Is(err, backup.ErrStagingNotFound):
		return backupStagingError(err)
	case errors.Is(err, backup.ErrWrongPassword):
		return &rpcError{Code: -32011, Message: "the backup password is wrong or the backup is damaged", Data: map[string]string{"code": "wrongPassword"}}
	case errors.Is(err, backup.ErrInvalidFormat):
		return &rpcError{Code: -32012, Message: "the file is not a MinCode backup", Data: map[string]string{"code": "invalidFormat"}}
	case errors.Is(err, backup.ErrCorrupt):
		return &rpcError{Code: -32013, Message: "the backup contents failed verification", Data: map[string]string{"code": "corrupt"}}
	case errors.Is(err, backup.ErrBackupTooLarge):
		return backupFailure(backup.ErrBackupTooLarge.Error(), "too_large")
	default:
		return backupFailure("could not open the backup", "")
	}
}

func backupRestoreExpiredError() *rpcError {
	return &rpcError{Code: -32014, Message: "the restore expired or was already used; prepare it again", Data: map[string]string{"code": "restoreExpired"}}
}

func backupSessionExpiredError() *rpcError {
	return &rpcError{Code: -32015, Message: "the opened backup expired or was closed; open it again", Data: map[string]string{"code": "sessionExpired"}}
}

func newBackupID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func backupManager(runtimeHost *processRuntime) *pluginManager {
	if runtimeHost.ownerManager != nil {
		return runtimeHost.ownerManager
	}
	return host()
}

// ---------------------------------------------------------------------------
// Sessions and restore records.

// backupSessionSection is one restorable part of an opened backup.
type backupSessionSection struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Items     int    `json:"items"`
	Bytes     int64  `json:"bytes"`
	Plugin    string `json:"plugin,omitempty"`
	Installed *bool  `json:"installed,omitempty"`
}

// backupOpenSession is a decrypted backup. entries, manifest and sections
// are never modified after open; password is cleared on close.
type backupOpenSession struct {
	id         string
	owner      string
	generation uint64
	manifest   backup.Manifest
	entries    []backup.Entry
	sections   []backupSessionSection
	password   []byte
	created    time.Time
}

func (session *backupOpenSession) hasSection(id string) bool {
	for _, section := range session.sections {
		if section.ID == id {
			return true
		}
	}
	return false
}

func (session *backupOpenSession) sectionTitle(id string) string {
	for _, section := range session.sections {
		if section.ID == id {
			return section.Title
		}
	}
	return id
}

// backupRestoreRecord is a confirmed restore waiting for applyRestore.
type backupRestoreRecord struct {
	id              string
	owner           string
	generation      uint64
	sessionID       string
	sections        []string
	mode            backup.Mode
	preRestoreToken string
	preRestoreName  string
	created         time.Time
}

// One open session and one pending restore per plugin, keyed by plugin id.
var (
	backupRestoreMu      sync.Mutex
	backupOpenSessions   = map[string]*backupOpenSession{}
	backupRestoreRecords = map[string]*backupRestoreRecord{}
)

// closeBackupSessionLocked drops owner's session and the restore bound to it.
func closeBackupSessionLocked(owner string) {
	session := backupOpenSessions[owner]
	if session == nil {
		return
	}
	clear(session.password)
	session.password = nil
	session.entries = nil
	delete(backupOpenSessions, owner)
	if record := backupRestoreRecords[owner]; record != nil && record.sessionID == session.id {
		dropBackupRestoreRecordLocked(owner)
	}
}

// dropBackupRestoreRecordLocked forgets owner's pending restore and releases
// its snapshot; the plugin can no longer apply it.
func dropBackupRestoreRecordLocked(owner string) {
	record := backupRestoreRecords[owner]
	if record == nil {
		return
	}
	delete(backupRestoreRecords, owner)
	backupStaging().Release(owner, record.preRestoreToken)
}

func clearBackupRestoreState(pluginID string) {
	backupRestoreMu.Lock()
	defer backupRestoreMu.Unlock()
	closeBackupSessionLocked(pluginID)
	dropBackupRestoreRecordLocked(pluginID)
}

// sweepBackupRestoreState drops every expired session and restore.
func sweepBackupRestoreState() {
	now := backupNow()
	backupRestoreMu.Lock()
	defer backupRestoreMu.Unlock()
	for owner, record := range backupRestoreRecords {
		if now.Sub(record.created) > backupRestoreTTL {
			dropBackupRestoreRecordLocked(owner)
		}
	}
	for owner, session := range backupOpenSessions {
		if now.Sub(session.created) > backupOpenSessionTTL {
			closeBackupSessionLocked(owner)
		}
	}
}

func storeBackupSession(session *backupOpenSession) {
	backupRestoreMu.Lock()
	defer backupRestoreMu.Unlock()
	closeBackupSessionLocked(session.owner)
	backupOpenSessions[session.owner] = session
}

// lookupBackupSession returns a copy of runtimeHost's live session with this
// id, so a concurrent close can drop the original's entries, and a copy of
// its password (the caller clears it).
func lookupBackupSession(runtimeHost *processRuntime, sessionID string) (*backupOpenSession, []byte, bool) {
	generation := runtimeGeneration(runtimeHost)
	backupRestoreMu.Lock()
	defer backupRestoreMu.Unlock()
	session := backupOpenSessions[runtimeHost.pluginID]
	if session == nil || session.id != sessionID {
		return nil, nil, false
	}
	if session.generation != generation || backupNow().Sub(session.created) > backupOpenSessionTTL {
		closeBackupSessionLocked(runtimeHost.pluginID)
		return nil, nil, false
	}
	view := *session
	view.password = nil
	return &view, bytes.Clone(session.password), true
}

// consumeBackupRestore removes and returns runtimeHost's pending restore with
// this id when it is still valid. The snapshot token stays staged.
func consumeBackupRestore(runtimeHost *processRuntime, restoreID string) (*backupRestoreRecord, bool) {
	generation := runtimeGeneration(runtimeHost)
	backupRestoreMu.Lock()
	defer backupRestoreMu.Unlock()
	record := backupRestoreRecords[runtimeHost.pluginID]
	if record == nil || record.id != restoreID {
		return nil, false
	}
	if record.generation != generation || backupNow().Sub(record.created) > backupRestoreTTL {
		dropBackupRestoreRecordLocked(runtimeHost.pluginID)
		return nil, false
	}
	delete(backupRestoreRecords, runtimeHost.pluginID)
	return record, true
}

// ---------------------------------------------------------------------------
// open.

// openStagedBackup decrypts owner's committed staging entry and returns the
// size and SHA-256 of the file it read.
func openStagedBackup(owner, token string, password []byte) (backup.Manifest, []backup.Entry, int64, string, error) {
	path, err := backupStaging().Path(owner, token)
	if err != nil {
		return backup.Manifest{}, nil, 0, "", err
	}
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return backup.Manifest{}, nil, 0, "", backup.ErrStagingNotFound
		}
		return backup.Manifest{}, nil, 0, "", fmt.Errorf("open staged backup: %w", err)
	}
	defer file.Close()
	hash := sha256.New()
	counter := &countingWriter{}
	reader := io.TeeReader(file, io.MultiWriter(hash, counter))
	manifest, entries, err := backup.Open(reader, password)
	if err != nil {
		return backup.Manifest{}, nil, 0, "", err
	}
	if _, err := io.Copy(io.Discard, reader); err != nil {
		return backup.Manifest{}, nil, 0, "", fmt.Errorf("read staged backup: %w", err)
	}
	return manifest, entries, counter.n, hex.EncodeToString(hash.Sum(nil)), nil
}

type countingWriter struct{ n int64 }

func (w *countingWriter) Write(p []byte) (int, error) {
	w.n += int64(len(p))
	return len(p), nil
}

// hostBackupOpen decrypts a staged backup. It tries this run's or the
// remembered password first and asks the user (once) only when that is
// missing or does not fit.
func hostBackupOpen(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request struct {
		Token string `json:"token"`
	}
	if err := decodeBackupParams(params, &request); err != nil {
		return nil, err
	}
	if !validBackupToken(request.Token) {
		return nil, hostUIParamsError("token is required")
	}
	backupStaging().Sweep()
	password, ok, rpcErr := backupPassword(runtimeHost)
	if rpcErr != nil {
		return nil, rpcErr
	}
	defer func() { clear(password) }()
	var (
		manifest backup.Manifest
		entries  []backup.Entry
		size     int64
		sha      string
		err      = backup.ErrWrongPassword
		remember bool
	)
	if ok {
		manifest, entries, size, sha, err = openStagedBackup(runtimeHost.pluginID, request.Token, password)
	}
	if errors.Is(err, backup.ErrWrongPassword) {
		clear(password)
		value, rpcErr := backupRequests.ask(runtimeHost.promptContext(), runtimeHost, backupKindPassword,
			struct {
				Mode        string `json:"mode"`
				HasPassword bool   `json:"hasPassword"`
			}{Mode: "open", HasPassword: ok}, decodeBackupPasswordReply, backupPasswordTimeout)
		if rpcErr != nil {
			return nil, rpcErr
		}
		reply := value.(backupPasswordReply)
		password, remember = []byte(reply.Password), reply.Remember
		manifest, entries, size, sha, err = openStagedBackup(runtimeHost.pluginID, request.Token, password)
	}
	if err != nil {
		return nil, backupOpenError(err)
	}
	if remember {
		// The backup is open either way; a password that cannot be
		// remembered (too short for a new backup) is simply not stored.
		_ = backupRememberPassword(password)
	}
	manager := backupManager(runtimeHost)
	sessionID, idErr := newBackupID()
	if idErr != nil {
		return nil, backupFailure("could not open the backup", "")
	}
	session := &backupOpenSession{id: sessionID, owner: runtimeHost.pluginID, generation: runtimeGeneration(runtimeHost),
		manifest: manifest, entries: entries, sections: backupSessionSections(manager, manifest, entries),
		password: bytes.Clone(password), created: backupNow()}
	storeBackupSession(session)
	return map[string]any{
		"sessionId": sessionID,
		"meta": backupMeta{Format: backupFileFormat, Version: backupFileVersion, CreatedAt: manifest.CreatedAt,
			AppVersion: cleanHostText(manifest.AppVersion, backupDeviceMaxRunes, false), Device: cleanHostText(manifest.Device, backupDeviceMaxRunes, false),
			IncludesSecrets: manifest.IncludesSecrets, Size: size, SHA256: sha},
		"sections": session.sections,
	}, nil
}

// backupEntryFrontendKey returns the storage key of an app/<key>.json entry
// that may be restored.
func backupEntryFrontendKey(path string) (string, bool) {
	name, ok := strings.CutPrefix(path, "app/")
	if !ok {
		return "", false
	}
	key, ok := strings.CutSuffix(name, ".json")
	if !ok || !backupFrontendKeyPattern.MatchString(key) {
		return "", false
	}
	if _, excluded := backupExcludedKeys[key]; excluded {
		return "", false
	}
	return key, true
}

// backupEntryPluginID returns the plugin of a plugins/<id>/... entry.
func backupEntryPluginID(path string) (string, bool) {
	rest, ok := strings.CutPrefix(path, pluginBackupPrefix)
	if !ok {
		return "", false
	}
	id, _, found := strings.Cut(rest, "/")
	if !found {
		return "", false
	}
	if valid, err := validatePluginID(id); err != nil || valid != id || id == hostPluginID {
		return "", false
	}
	return id, true
}

// backupSessionSections lists the sections the entries can restore: core
// sections in display order, then plugins by id. Counts come from the
// manifest when it describes the section, else from the entries; plugin
// titles prefer the installed plugin's name.
func backupSessionSections(manager *pluginManager, manifest backup.Manifest, entries []backup.Entry) []backupSessionSection {
	found := map[string]*backupSessionSection{}
	pluginIDs := []string{}
	for _, entry := range entries {
		var section *backupSessionSection
		if key, ok := backupEntryFrontendKey(entry.Path); ok {
			id := backup.SectionForKey(key)
			if section = found[id]; section == nil {
				section = &backupSessionSection{ID: id, Title: id}
				found[id] = section
			}
		} else if pluginID, ok := backupEntryPluginID(entry.Path); ok {
			id := backup.PluginSectionID(pluginID)
			if section = found[id]; section == nil {
				section = &backupSessionSection{ID: id, Plugin: pluginID}
				found[id] = section
				pluginIDs = append(pluginIDs, pluginID)
			}
		} else {
			continue
		}
		section.Items++
		section.Bytes += int64(len(entry.Data))
	}
	described := map[string]backup.SectionSummary{}
	for _, summary := range manifest.Sections {
		described[summary.ID] = summary
	}
	out := []backupSessionSection{}
	for _, id := range backup.CoreSections {
		if section := found[id]; section != nil {
			if summary, ok := described[id]; ok && summary.Items >= 0 && summary.Bytes >= 0 {
				section.Items, section.Bytes = summary.Items, summary.Bytes
			}
			out = append(out, *section)
		}
	}
	sort.Strings(pluginIDs)
	for _, pluginID := range pluginIDs {
		section := found[backup.PluginSectionID(pluginID)]
		summary, ok := described[section.ID]
		if ok && summary.Items >= 0 && summary.Bytes >= 0 {
			section.Items, section.Bytes = summary.Items, summary.Bytes
		}
		installed := false
		if manager != nil {
			_, err := manager.record(pluginID)
			installed = err == nil
		}
		section.Installed = &installed
		switch {
		case installed:
			section.Title = manager.pluginTitle(pluginID)
		case ok && cleanHostText(summary.Title, backupSectionTitleMaxRunes, false) != "":
			section.Title = cleanHostText(summary.Title, backupSectionTitleMaxRunes, false)
		default:
			section.Title = pluginID
		}
		out = append(out, *section)
	}
	return out
}

// ---------------------------------------------------------------------------
// Restore planning.

// backupConfirmChange is one row of the restore confirmation: names only.
type backupConfirmChange struct {
	Section      string `json:"section"`
	SectionTitle string `json:"sectionTitle"`
	Kind         string `json:"kind"`
	Label        string `json:"label"`
}

func capBackupConfirmChanges(changes []backupConfirmChange) []backupConfirmChange {
	if len(changes) <= backupConfirmMaxChanges {
		return changes
	}
	out := append([]backupConfirmChange(nil), changes[:backupConfirmMaxChanges]...)
	return append(out, backupConfirmChange{Kind: "more", Label: strconv.Itoa(len(changes) - backupConfirmMaxChanges)})
}

// backupFrontendRestore is what a restore writes through backupApply.
type backupFrontendRestore struct {
	Sets    map[string]backupFrontendValue `json:"sets"`
	Deletes []string                       `json:"deletes"`
	changes []backupConfirmChange
}

func (restore backupFrontendRestore) empty() bool {
	return len(restore.Sets) == 0 && len(restore.Deletes) == 0
}

// restorableLocalKey reports whether a collected frontend key can take part
// in a restore (the same filter as buildBackupEntries).
func restorableLocalKey(key string) bool {
	if _, excluded := backupExcludedKeys[key]; excluded {
		return false
	}
	return backupFrontendKeyPattern.MatchString(key)
}

// planBackupFrontendRestore merges the backup's frontend keys of the chosen
// core sections into the local values. Keys whose value would not change are
// left out of Sets; in overwrite mode local keys of a chosen section that the
// backup lacks are deleted.
func planBackupFrontendRestore(session *backupOpenSession, local map[string]backupFrontendValue, chosen map[string]bool, mode backup.Mode) (backupFrontendRestore, error) {
	incoming := map[string]backupFrontendValue{}
	for _, entry := range session.entries {
		key, ok := backupEntryFrontendKey(entry.Path)
		if !ok || !chosen[backup.SectionForKey(key)] {
			continue
		}
		if _, duplicate := incoming[key]; duplicate {
			return backupFrontendRestore{}, fmt.Errorf("duplicate entry for %s", key)
		}
		value, err := decodeBackupEntryValue(entry.Data)
		if err != nil {
			return backupFrontendRestore{}, fmt.Errorf("entry %s: %w", key, err)
		}
		incoming[key] = value
	}
	restore := backupFrontendRestore{Sets: map[string]backupFrontendValue{}, Deletes: []string{}}
	keys := make([]string, 0, len(incoming))
	for key := range incoming {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		in := incoming[key]
		current, exists := local[key]
		var localValue json.RawMessage
		if exists {
			localValue = current.Value
		}
		merged, changes, err := backup.MergeValue(key, localValue, in.Value, mode, session.manifest.IncludesSecrets)
		if err != nil {
			return backupFrontendRestore{}, err
		}
		if !exists || current.Storage != in.Storage || !compactJSONEqual(merged, current.Value) {
			restore.Sets[key] = backupFrontendValue{Storage: in.Storage, Value: merged}
		}
		section := backup.SectionForKey(key)
		for _, change := range changes {
			restore.changes = append(restore.changes, backupConfirmChange{Section: section, SectionTitle: session.sectionTitle(section), Kind: change.Kind, Label: change.Label})
		}
	}
	if mode == backup.ModeOverwrite {
		localKeys := make([]string, 0, len(local))
		for key := range local {
			if _, inBackup := incoming[key]; !inBackup && restorableLocalKey(key) && chosen[backup.SectionForKey(key)] {
				localKeys = append(localKeys, key)
			}
		}
		sort.Strings(localKeys)
		for _, key := range localKeys {
			section := backup.SectionForKey(key)
			restore.Deletes = append(restore.Deletes, key)
			restore.changes = append(restore.changes, backupConfirmChange{Section: section, SectionTitle: session.sectionTitle(section), Kind: backup.ChangeRemove, Label: key})
		}
	}
	return restore, nil
}

func decodeBackupEntryValue(data []byte) (backupFrontendValue, error) {
	var value struct {
		Storage string          `json:"storage"`
		Value   json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return backupFrontendValue{}, errors.New("invalid JSON")
	}
	if value.Storage != "plain" && value.Storage != "secure" {
		return backupFrontendValue{}, errors.New("storage must be plain or secure")
	}
	if len(value.Value) == 0 {
		return backupFrontendValue{}, errors.New("value is required")
	}
	return backupFrontendValue{Storage: value.Storage, Value: value.Value}, nil
}

func compactJSONEqual(a, b json.RawMessage) bool {
	var left, right bytes.Buffer
	if json.Compact(&left, a) != nil || json.Compact(&right, b) != nil {
		return bytes.Equal(a, b)
	}
	return bytes.Equal(left.Bytes(), right.Bytes())
}

// planBackupPluginRestores plans every chosen plugin section in section order.
func planBackupPluginRestores(session *backupOpenSession, sections []string, mode backup.Mode) ([]pluginRestorePlan, error) {
	plans := []pluginRestorePlan{}
	for _, id := range sections {
		pluginID, ok := backup.IsPluginSection(id)
		if !ok {
			continue
		}
		plan, err := planPluginRestore(pluginID, session.entries, mode)
		if err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	return plans, nil
}

// collectLocalForRestore asks the frontend for every local value, secrets
// included: restore needs them to merge and the snapshot to roll back.
func collectLocalForRestore(runtimeHost *processRuntime) (map[string]backupFrontendValue, *rpcError) {
	value, rpcErr := backupRequests.ask(runtimeHost.promptContext(), runtimeHost, backupKindCollect,
		struct {
			IncludeSecrets bool `json:"includeSecrets"`
		}{IncludeSecrets: true},
		func(raw json.RawMessage) (any, error) { return decodeBackupCollectReply(raw) }, backupCollectTimeout)
	if rpcErr != nil {
		return nil, rpcErr
	}
	return value.(map[string]backupFrontendValue), nil
}

func decodeBackupConfirmReply(raw json.RawMessage) (any, error) {
	var reply struct {
		Confirmed *bool `json:"confirmed"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		return nil, err
	}
	if reply.Confirmed == nil {
		return nil, errors.New("confirmed is required")
	}
	return *reply.Confirmed, nil
}

func decodeBackupApplyReply(raw json.RawMessage) (any, error) {
	var reply struct {
		OK *bool `json:"ok"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		return nil, err
	}
	if reply.OK == nil || !*reply.OK {
		return nil, errors.New("ok must be true")
	}
	return true, nil
}

// askBackupApply sends a restore write on the reserved path.
func askBackupApply(ctx context.Context, runtimeHost *processRuntime, restore backupFrontendRestore) *rpcError {
	_, rpcErr := backupRequests.askReserved(ctx, runtimeHost, backupKindApply, restore, decodeBackupApplyReply, backupApplyTimeout)
	return rpcErr
}

// ---------------------------------------------------------------------------
// prepareRestore and applyRestore.

type backupRestoreParams struct {
	SessionID string   `json:"sessionId"`
	Sections  []string `json:"sections"`
	Mode      string   `json:"mode"`
}

// validateRestoreSections checks the chosen sections against the session and
// returns them in session order plus the chosen core sections.
func validateRestoreSections(session *backupOpenSession, requested []string) ([]string, map[string]bool, *rpcError) {
	if len(requested) == 0 {
		return nil, nil, hostUIParamsError("sections must name at least one section")
	}
	picked := map[string]bool{}
	for _, id := range requested {
		if picked[id] {
			return nil, nil, hostUIParamsError("sections must not repeat")
		}
		if !session.hasSection(id) {
			return nil, nil, hostUIParamsError("sections must be sections of the opened backup")
		}
		picked[id] = true
	}
	ordered := []string{}
	core := map[string]bool{}
	for _, section := range session.sections {
		if !picked[section.ID] {
			continue
		}
		ordered = append(ordered, section.ID)
		if _, isPlugin := backup.IsPluginSection(section.ID); !isPlugin {
			core[section.ID] = true
		}
	}
	return ordered, core, nil
}

func hostBackupPrepareRestore(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request backupRestoreParams
	if err := decodeBackupParams(params, &request); err != nil {
		return nil, err
	}
	mode := backup.Mode(request.Mode)
	if mode != backup.ModeOverwrite && mode != backup.ModeMerge {
		return nil, hostUIParamsError("mode must be overwrite or merge")
	}
	if request.SessionID == "" {
		return nil, hostUIParamsError("sessionId is required")
	}
	session, sessionPassword, ok := lookupBackupSession(runtimeHost, request.SessionID)
	if !ok {
		return nil, backupSessionExpiredError()
	}
	defer clear(sessionPassword)
	sections, core, rpcErr := validateRestoreSections(session, request.Sections)
	if rpcErr != nil {
		return nil, rpcErr
	}
	manager := backupManager(runtimeHost)

	local, rpcErr := collectLocalForRestore(runtimeHost)
	if rpcErr != nil {
		return nil, rpcErr
	}
	frontend, err := planBackupFrontendRestore(session, local, core, mode)
	if err != nil {
		return nil, backupFailure("the backup's MinCode data is invalid", "invalidBackup")
	}
	plans, err := planBackupPluginRestores(session, sections, mode)
	if err != nil {
		return nil, backupFailure("the backup's plugin data is invalid", "invalidBackup")
	}
	changes := frontend.changes
	for _, plan := range plans {
		section := backup.PluginSectionID(plan.ID)
		for _, change := range plan.Changes {
			changes = append(changes, backupConfirmChange{Section: section, SectionTitle: session.sectionTitle(section), Kind: change.Kind, Label: change.Label})
		}
	}
	if changes == nil {
		changes = []backupConfirmChange{}
	}

	value, rpcErr := backupRequests.ask(runtimeHost.promptContext(), runtimeHost, backupKindRestoreConfirm, struct {
		Mode       string                `json:"mode"`
		Changes    []backupConfirmChange `json:"changes"`
		PluginName string                `json:"pluginName"`
	}{Mode: string(mode), Changes: capBackupConfirmChanges(changes), PluginName: runtimeHost.displayName()},
		decodeBackupConfirmReply, backupRestoreConfirmTimeout)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if confirmed := value.(bool); !confirmed {
		return nil, backupCancelledError()
	}

	// The pre-restore snapshot: everything on this device, secrets included,
	// sealed with the current backup password, else the one this backup
	// opened with.
	snapshotEntries, snapshotSections, err := buildBackupEntries(manager, local, true)
	if err != nil {
		return nil, backupFailure("could not read the data on this device", "")
	}
	password, ok, rpcErr := backupPassword(runtimeHost)
	if rpcErr != nil {
		return nil, rpcErr
	}
	defer func() { clear(password) }()
	if !ok {
		password = bytes.Clone(sessionPassword)
	}
	staged, rpcErr := stageSealedBackup(runtimeHost.pluginID, password, true, snapshotEntries, snapshotSections)
	if rpcErr != nil {
		return nil, rpcErr
	}
	restoreID, err := newBackupID()
	if err != nil {
		backupStaging().Release(runtimeHost.pluginID, staged.Token)
		return nil, backupFailure("could not prepare the restore", "")
	}

	backupRestoreMu.Lock()
	if current := backupOpenSessions[runtimeHost.pluginID]; current == nil || current.id != session.id {
		backupRestoreMu.Unlock()
		backupStaging().Release(runtimeHost.pluginID, staged.Token)
		return nil, backupSessionExpiredError()
	}
	dropBackupRestoreRecordLocked(runtimeHost.pluginID)
	backupRestoreRecords[runtimeHost.pluginID] = &backupRestoreRecord{id: restoreID, owner: runtimeHost.pluginID, generation: session.generation,
		sessionID: session.id, sections: sections, mode: mode, preRestoreToken: staged.Token, preRestoreName: staged.Name, created: backupNow()}
	backupRestoreMu.Unlock()

	return map[string]any{
		"restoreId":       restoreID,
		"preRestoreToken": staged.Token,
		"preRestoreName":  staged.Name,
		"preRestoreMeta":  staged.Meta,
	}, nil
}

// rollbackFrontendRestore undoes restore: keys it set or deleted that existed
// before get their old values back, keys it added are deleted.
func rollbackFrontendRestore(restore backupFrontendRestore, local map[string]backupFrontendValue) backupFrontendRestore {
	out := backupFrontendRestore{Sets: map[string]backupFrontendValue{}, Deletes: []string{}}
	for key := range restore.Sets {
		if previous, existed := local[key]; existed {
			out.Sets[key] = previous
		} else {
			out.Deletes = append(out.Deletes, key)
		}
	}
	for _, key := range restore.Deletes {
		if previous, existed := local[key]; existed {
			out.Sets[key] = previous
		}
	}
	sort.Strings(out.Deletes)
	return out
}

func hostBackupApplyRestore(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request struct {
		RestoreID string `json:"restoreId"`
	}
	if err := decodeBackupParams(params, &request); err != nil {
		return nil, err
	}
	if request.RestoreID == "" {
		return nil, hostUIParamsError("restoreId is required")
	}
	record, ok := consumeBackupRestore(runtimeHost, request.RestoreID)
	if !ok {
		return nil, backupRestoreExpiredError()
	}
	// Registered before every other defer so it runs last, after the staged
	// snapshot is released: the frontend reloads on this event. rolledBack
	// is false when a failed restore could not be undone: data may be half
	// restored and the pre-restore backup is the way back.
	frontendWriteStarted, restoreOK, rolledBack := false, false, true
	defer func() {
		if frontendWriteStarted {
			event := map[string]any{"restoreId": request.RestoreID, "ok": restoreOK, "rolledBack": !restoreOK && rolledBack}
			if !restoreOK && record.preRestoreName != "" {
				event["preRestoreName"] = record.preRestoreName
			}
			hostUIEmit(pluginBackupRestoreFinishedEvent, event)
		}
	}()
	// Step 7: the snapshot stays staged (the plugin uploaded it after
	// prepare) until this call is over. After a failed rollback it is kept
	// until its TTL so the plugin or the desktop can still save it.
	defer func() {
		if restoreOK || rolledBack {
			backupStaging().Release(runtimeHost.pluginID, record.preRestoreToken)
		}
	}()
	session, sessionPassword, ok := lookupBackupSession(runtimeHost, record.sessionID)
	clear(sessionPassword)
	if !ok {
		return nil, backupRestoreExpiredError()
	}
	chosen := map[string]bool{}
	for _, id := range record.sections {
		if _, isPlugin := backup.IsPluginSection(id); !isPlugin {
			chosen[id] = true
		}
	}
	manager := backupManager(runtimeHost)

	// Fresh values and plans: the device may have changed since prepare.
	local, rpcErr := collectLocalForRestore(runtimeHost)
	if rpcErr != nil {
		return nil, rpcErr
	}
	frontend, err := planBackupFrontendRestore(session, local, chosen, record.mode)
	if err != nil {
		return nil, backupFailure("the backup's MinCode data is invalid", "invalidBackup")
	}
	plans, err := planBackupPluginRestores(session, record.sections, record.mode)
	if err != nil {
		return nil, backupFailure("the backup's plugin data is invalid", "invalidBackup")
	}

	// Step 4: MinCode's own data, in one frontend transaction. A plugin stop
	// does not cut it short. An explicit frontend error means nothing was
	// written; a timeout or cancel leaves it unknown, so the old values are
	// written back (idempotent either way).
	ctx := context.WithoutCancel(runtimeHost.promptContext())
	if !frontend.empty() {
		frontendWriteStarted = true
		if rpcErr := askBackupApply(ctx, runtimeHost, frontend); rpcErr != nil {
			if rpcErr.Code != backupCancelledError().Code {
				return nil, rpcErr
			}
			outcome := "the rollback succeeded"
			if rollbackErr := askBackupApply(ctx, runtimeHost, rollbackFrontendRestore(frontend, local)); rollbackErr != nil {
				rolledBack = false
				outcome = "the rollback failed too; restore the pre-restore backup"
			}
			return nil, backupFailure("the desktop did not confirm writing MinCode's data, so the outcome is uncertain; a rollback was attempted and "+outcome, "restoreUncertain")
		}
	}

	// Step 5: every plugin but the caller; on failure put the frontend back.
	_, notInstalled, err := manager.applyPluginRestore(ctx, plans, runtimeHost.pluginID)
	if err != nil {
		manager.logBackupRestore(runtimeHost.pluginID, "error", "data restore failed: "+err.Error())
		// Plugins already written were put back unless the error says the
		// rollback failed too.
		rolledBack = !errors.Is(err, errPluginRestoreRollback)
		if !frontend.empty() {
			if rollbackErr := askBackupApply(ctx, runtimeHost, rollbackFrontendRestore(frontend, local)); rollbackErr != nil {
				rolledBack = false
			}
		}
		message := "the plugin data could not be restored and nothing was changed"
		switch {
		case !rolledBack:
			message = "the plugin data could not be restored and not everything could be put back; restore the pre-restore backup"
		case !frontend.empty():
			message = "the plugin data could not be restored; MinCode's own data was put back and so was any plugin data already written"
		}
		return nil, backupFailure(message, "restoreFailed")
	}

	// Step 6.
	notInstalledSections := make([]string, 0, len(notInstalled))
	for _, id := range notInstalled {
		notInstalledSections = append(notInstalledSections, backup.PluginSectionID(id))
	}
	var deferred *pluginRestorePlan
	for i := range plans {
		if plans[i].ID == runtimeHost.pluginID {
			deferred = &plans[i]
		}
	}
	restored, deferredSections := []string{}, []string{}
	callerSection := backup.PluginSectionID(runtimeHost.pluginID)
	for _, id := range record.sections {
		if deferred != nil && id == callerSection {
			deferredSections = append(deferredSections, id)
		} else {
			restored = append(restored, id)
		}
	}
	if deferred != nil {
		scheduleBackupDeferredRestore(manager, ctx, *deferred)
	}
	restoreOK = true
	return map[string]any{
		"restored":         restored,
		"notInstalled":     notInstalledSections,
		"deferred":         deferred != nil,
		"deferredSections": deferredSections,
	}, nil
}

// logBackupRestore appends to an installed plugin's record log.
func (manager *pluginManager) logBackupRestore(pluginID, level, message string) {
	if manager == nil {
		return
	}
	if record, err := manager.record(pluginID); err == nil {
		record.appendLog(level, message)
	}
}

func hostBackupClose(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request struct {
		SessionID string `json:"sessionId"`
	}
	if err := decodeBackupParams(params, &request); err != nil {
		return nil, err
	}
	if request.SessionID == "" {
		return nil, hostUIParamsError("sessionId is required")
	}
	backupRestoreMu.Lock()
	defer backupRestoreMu.Unlock()
	if session := backupOpenSessions[runtimeHost.pluginID]; session != nil && session.id == request.SessionID {
		closeBackupSessionLocked(runtimeHost.pluginID)
	}
	return true, nil
}
