package plugin

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"mincode/app/backend/backup"
	"mincode/app/backend/core"
	"mincode/app/plugins/api"
)

// Encrypted backups (capability backup; docs/superpowers/specs/
// 2026-10-29-data-backup-design.md 4.1-4.4). The host collects MinCode's own
// data (from the frontend persistence layer) and every plugin's data, seals it
// with the user's backup password and stages the ciphertext; the plugin moves
// files and schedules runs. Plaintext and the password exist only inside this
// process: the frontend hands values over an in-process frontend event, Seal
// writes straight into the staging file, and the plugin reads ciphertext only.
//
// The backup password is entered only in a host dialog (request kind
// backupPassword). "Remember on this device" stores it machine-sealed in
// app_kv (backend/backup/password.go); otherwise it is kept in memory for the
// current process generation of the plugin that set it.
//
// Host → frontend requests use a dedicated channel rather than hostPrompts:
// collected values can be megabytes, while prompt replies are capped at 4 KiB
// and shaped for input boxes. One request per plugin is open at a time.
//
//   - event plugin:backup-request {id, kind, pluginId, pluginName, payload}
//   - event plugin:backup-request-cancel {id}
//   - command plugin_host_backup_reply {id, cancelled?, error?, result?}
//
// Kinds: backupCollect {includeSecrets} → {values: {<key>: {storage, value}}};
// backupPassword {mode: "set"|"open"} → {password, remember}. Restore adds
// backupRestoreConfirm and backupApply (host_backup_restore.go).
const (
	pluginBackupRequestEvent       = "plugin:backup-request"
	pluginBackupRequestCancelEvent = "plugin:backup-request-cancel"
	pluginBackupReplyCmd           = "plugin_host_backup_reply"

	// backupReplyMaxBytes bounds the whole plugin_host_backup_reply args.
	backupReplyMaxBytes = 32 << 20

	backupKindCollect  = "backupCollect"
	backupKindPassword = "backupPassword"

	backupCollectTimeout  = 2 * time.Minute
	backupPasswordTimeout = 5 * time.Minute

	backupPasswordMaxBytes      = 1024
	backupDeviceMaxRunes        = 64
	backupFrontendErrorMaxRunes = 300
	backupTokenMaxBytes         = 128

	backupStagingDirectory = "backup-staging"
	backupFileFormat       = "mincode-backup"
	backupFileVersion      = 1
)

var (
	// Frontend storage keys accepted into a backup; the key becomes the
	// entry name app/<key>.json.
	backupFrontendKeyPattern = regexp.MustCompile(`^app_[A-Za-z0-9_.-]{1,128}$`)
	backupSHA256Pattern      = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// backupExcludedKeys never travel, even if the frontend sent them: licences
// and the app lock are bound to this device (design 4.3).
var backupExcludedKeys = map[string]struct{}{
	"app_license":     {},
	"mincode_license": {},
	"app_lock_v1":     {},
}

// Seams for tests.
var (
	backupRememberedPassword = backup.RememberedPassword
	backupRememberPassword   = backup.RememberPassword
	backupForgetPassword     = backup.ForgetPassword
	backupHostname           = os.Hostname
	backupNow                = time.Now
)

// backupAppVersion is misc.AppVersion, installed by main through
// SetAppVersion: backend/misc depends on the desktop application package, which
// this package must not import.
var backupAppVersion atomic.Value

// SetAppVersion records the MinCode version written into backup manifests.
func SetAppVersion(version string) { backupAppVersion.Store(version) }

func currentAppVersion() string {
	version, _ := backupAppVersion.Load().(string)
	return version
}

func init() {
	// Every host.backup.* method goes through backupMethod (host_backup_restore.go):
	// refused while the plugin's restore runs, and sweeping expired sessions.
	registerHostMethod("host.backup.status", nil, api.CapabilityBackup, backupMethod(hostBackupStatus))
	registerHostMethod("host.backup.setOptions", nil, api.CapabilityBackup, backupMethod(hostBackupSetOptions))
	registerHostMethod("host.backup.setPassword", nil, api.CapabilityBackup, backupMethod(hostBackupSetPassword))
	registerHostMethod("host.backup.create", nil, api.CapabilityBackup, backupMethod(hostBackupCreate))
	registerHostMethod("host.backup.read", nil, api.CapabilityBackup, backupMethod(hostBackupRead))
	registerHostMethod("host.backup.stage.begin", nil, api.CapabilityBackup, backupMethod(hostBackupStageBegin))
	registerHostMethod("host.backup.stage.append", nil, api.CapabilityBackup, backupMethod(hostBackupStageAppend))
	registerHostMethod("host.backup.stage.commit", nil, api.CapabilityBackup, backupMethod(hostBackupStageCommit))
	registerHostMethod("host.backup.release", nil, api.CapabilityBackup, backupMethod(hostBackupRelease))
	core.Register(pluginBackupReplyCmd, pluginHostBackupReplyHandler)
	registerRecordLifecycleHook(func(_ *pluginManager, record *pluginRecord) {
		clearBackupSessionPassword(record.id)
	})
	registerRecordRemovalHook(func(_ *pluginManager, pluginID string, _ removalReason) {
		clearBackupSessionPassword(pluginID)
		_ = pluginKVDeleteRaw(hostPluginID, backupOptionsKVKey(pluginID))
	})
}

// ---------------------------------------------------------------------------
// Errors.

func backupCancelledError() *rpcError {
	return &rpcError{Code: -32800, Message: "the user cancelled", Data: map[string]string{"code": "cancelled"}}
}

func backupPasswordRequiredError() *rpcError {
	return &rpcError{Code: -32010, Message: "no backup password is set", Data: map[string]string{"code": "passwordRequired"}}
}

func backupFailure(message, code string) *rpcError {
	if code == "" {
		return &rpcError{Code: -32000, Message: message}
	}
	return &rpcError{Code: -32000, Message: message, Data: map[string]string{"code": code}}
}

// backupStagingError maps staging errors to rpc errors without exposing
// file system paths.
func backupStagingError(err error) *rpcError {
	switch {
	case errors.Is(err, backup.ErrStagingNotFound):
		return &rpcError{Code: -32002, Message: err.Error(), Data: map[string]string{"code": "not_found"}}
	case errors.Is(err, backup.ErrStagingOffset), errors.Is(err, backup.ErrStagingChunk), errors.Is(err, backup.ErrStagingRange):
		return hostUIParamsError(err.Error())
	case errors.Is(err, backup.ErrStagingChecksum):
		return backupFailure(err.Error(), "checksum")
	case errors.Is(err, backup.ErrBackupTooLarge):
		return backupFailure(err.Error(), "too_large")
	default:
		return backupFailure("the backup staging area is unavailable", "")
	}
}

// decodeBackupParams decodes plugin params strictly; empty params mean {}.
func decodeBackupParams(params json.RawMessage, target any) *rpcError {
	trimmed := bytes.TrimSpace(params)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		trimmed = []byte("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		return hostUIParamsError("invalid params")
	}
	return nil
}

func validBackupToken(token string) bool {
	return token != "" && len(token) <= backupTokenMaxBytes
}

// ---------------------------------------------------------------------------
// Frontend request channel.

type backupRequestEvent struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	PluginID   string `json:"pluginId"`
	PluginName string `json:"pluginName"`
	Payload    any    `json:"payload"`
}

type backupReply struct {
	cancelled bool
	errText   string
	value     any
}

type pendingBackupRequest struct {
	pluginID string
	kind     string
	// decode validates the result for kind and returns the decoded value.
	decode func(json.RawMessage) (any, error)
	reply  chan backupReply
}

type backupRequestBroker struct {
	mu       sync.Mutex
	seq      uint64
	pending  map[string]*pendingBackupRequest
	byPlugin map[string]string
}

var backupRequests = &backupRequestBroker{pending: map[string]*pendingBackupRequest{}, byPlugin: map[string]string{}}

// ask sends one request to the frontend and waits for its reply, the timeout
// or the plugin's prompt context to end. Timeouts and stops count as cancel.
func (b *backupRequestBroker) ask(ctx context.Context, runtimeHost *processRuntime, kind string, payload any,
	decode func(json.RawMessage) (any, error), timeout time.Duration) (any, *rpcError) {
	return b.send(ctx, runtimeHost, kind, payload, decode, timeout, false)
}

// askReserved is ask for the writes of a running restore (step 4 and its
// rollback): it does not take or wait for the plugin's request slot, so a
// request the plugin left open can never block putting data back.
func (b *backupRequestBroker) askReserved(ctx context.Context, runtimeHost *processRuntime, kind string, payload any,
	decode func(json.RawMessage) (any, error), timeout time.Duration) (any, *rpcError) {
	return b.send(ctx, runtimeHost, kind, payload, decode, timeout, true)
}

func (b *backupRequestBroker) send(ctx context.Context, runtimeHost *processRuntime, kind string, payload any,
	decode func(json.RawMessage) (any, error), timeout time.Duration, reserved bool) (any, *rpcError) {
	pluginID := runtimeHost.pluginID
	b.mu.Lock()
	if _, busy := b.byPlugin[pluginID]; busy && !reserved {
		b.mu.Unlock()
		return nil, &rpcError{Code: -32004, Message: "another backup request from this plugin is still open", Data: map[string]string{"code": "busy"}}
	}
	b.seq++
	id := fmt.Sprintf("backup-%d-%d", time.Now().UnixNano(), b.seq)
	request := &pendingBackupRequest{pluginID: pluginID, kind: kind, decode: decode, reply: make(chan backupReply, 1)}
	b.pending[id] = request
	if !reserved {
		b.byPlugin[pluginID] = id
	}
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.pending, id)
		if b.byPlugin[pluginID] == id {
			delete(b.byPlugin, pluginID)
		}
		b.mu.Unlock()
	}()
	hostUIEmit(pluginBackupRequestEvent, backupRequestEvent{ID: id, Kind: kind, PluginID: pluginID, PluginName: runtimeHost.displayName(), Payload: payload})
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case reply := <-request.reply:
		switch {
		case reply.cancelled:
			return nil, backupCancelledError()
		case reply.errText != "":
			return nil, backupFailure("the desktop could not complete the backup request: "+reply.errText, "frontend")
		default:
			return reply.value, nil
		}
	case <-ctx.Done():
	case <-timer.C:
	}
	hostUIEmit(pluginBackupRequestCancelEvent, map[string]string{"id": id})
	return nil, backupCancelledError()
}

// answer delivers a frontend reply. Exactly one of cancelled, error and
// result must be given; result is validated for the request kind.
func (b *backupRequestBroker) answer(id string, cancelled bool, errText *string, result json.RawMessage) error {
	b.mu.Lock()
	request := b.pending[id]
	b.mu.Unlock()
	if request == nil {
		return fmt.Errorf("backup request %q is no longer open", id)
	}
	hasResult := len(bytes.TrimSpace(result)) > 0
	given := 0
	for _, present := range []bool{cancelled, errText != nil, hasResult} {
		if present {
			given++
		}
	}
	if given != 1 {
		return errors.New("a backup reply needs exactly one of cancelled, error or result")
	}
	var reply backupReply
	switch {
	case cancelled:
		reply.cancelled = true
	case errText != nil:
		reply.errText = cleanHostText(*errText, backupFrontendErrorMaxRunes, false)
		if reply.errText == "" {
			reply.errText = "unknown error"
		}
	default:
		value, err := request.decode(result)
		if err != nil {
			return fmt.Errorf("invalid %s reply: %w", request.kind, err)
		}
		reply.value = value
	}
	select {
	case request.reply <- reply:
		return nil
	default:
		return fmt.Errorf("backup request %q was already answered", id)
	}
}

func pluginHostBackupReplyHandler(_, argsJSON string) (string, error) {
	if len(argsJSON) > backupReplyMaxBytes {
		return "", fmt.Errorf("backup reply exceeds %d bytes", backupReplyMaxBytes)
	}
	var request struct {
		ID        string          `json:"id"`
		Cancelled bool            `json:"cancelled"`
		Error     *string         `json:"error"`
		Result    json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &request); err != nil {
		return "", fmt.Errorf("invalid backup reply: %w", err)
	}
	if err := backupRequests.answer(request.ID, request.Cancelled, request.Error, request.Result); err != nil {
		return "", err
	}
	return "{}", nil
}

// backupFrontendValue is one frontend storage key: where the frontend keeps
// it (plain or secure storage) and its value.
type backupFrontendValue struct {
	Storage string          `json:"storage"`
	Value   json.RawMessage `json:"value"`
}

// decodeBackupCollectReply validates {values: {<key>: {storage, value}}}.
// Unknown fields are ignored; keys are filtered later by buildBackupEntries.
func decodeBackupCollectReply(raw json.RawMessage) (map[string]backupFrontendValue, error) {
	var reply struct {
		Values *map[string]json.RawMessage `json:"values"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		return nil, err
	}
	if reply.Values == nil {
		return nil, errors.New("values is required")
	}
	out := make(map[string]backupFrontendValue, len(*reply.Values))
	for key, item := range *reply.Values {
		var value struct {
			Storage *string         `json:"storage"`
			Value   json.RawMessage `json:"value"`
		}
		if err := json.Unmarshal(item, &value); err != nil {
			return nil, fmt.Errorf("values[%q]: %w", key, err)
		}
		if value.Storage == nil || (*value.Storage != "plain" && *value.Storage != "secure") {
			return nil, fmt.Errorf("values[%q].storage must be plain or secure", key)
		}
		if len(value.Value) == 0 {
			return nil, fmt.Errorf("values[%q].value is required", key)
		}
		out[key] = backupFrontendValue{Storage: *value.Storage, Value: value.Value}
	}
	return out, nil
}

type backupPasswordReply struct {
	Password string
	Remember bool
}

func decodeBackupPasswordReply(raw json.RawMessage) (any, error) {
	var reply struct {
		Password *string `json:"password"`
		Remember *bool   `json:"remember"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		return nil, err
	}
	if reply.Password == nil || reply.Remember == nil {
		return nil, errors.New("password and remember are required")
	}
	if len(*reply.Password) > backupPasswordMaxBytes || !utf8.ValidString(*reply.Password) {
		return nil, errors.New("password is invalid")
	}
	return backupPasswordReply{Password: *reply.Password, Remember: *reply.Remember}, nil
}

// ---------------------------------------------------------------------------
// Staging, options and passwords.

var (
	backupStagingOnce sync.Once
	backupStagingArea *backup.Staging
)

// backupStaging is created on first use; NewStaging removes files a previous
// run left behind.
func backupStaging() *backup.Staging {
	backupStagingOnce.Do(func() {
		backupStagingArea = backup.NewStaging(filepath.Join(userDataDir(), appDataDirectory, backupStagingDirectory), nil)
	})
	return backupStagingArea
}

// backupOptionsKVKey contains "/", which plugin IDs never do.
func backupOptionsKVKey(pluginID string) string { return "backup/options/" + pluginID }

type backupOptions struct {
	IncludeSecrets bool `json:"includeSecrets"`
}

func readBackupOptions(pluginID string) backupOptions {
	var options backupOptions
	raw, found, err := pluginKVGetRaw(hostPluginID, backupOptionsKVKey(pluginID))
	if err != nil || !found || json.Unmarshal(raw, &options) != nil {
		return backupOptions{}
	}
	return options
}

// A password set without "remember" lasts while the plugin process runs: it
// is keyed by plugin and process generation, and cleared when the plugin stops.
type backupSessionSecret struct {
	generation uint64
	password   []byte
}

var (
	backupSessionMu        sync.Mutex
	backupSessionPasswords = map[string]backupSessionSecret{}
)

func setBackupSessionPassword(runtimeHost *processRuntime, password []byte) {
	generation := runtimeGeneration(runtimeHost)
	backupSessionMu.Lock()
	defer backupSessionMu.Unlock()
	if previous, ok := backupSessionPasswords[runtimeHost.pluginID]; ok {
		clear(previous.password)
	}
	backupSessionPasswords[runtimeHost.pluginID] = backupSessionSecret{generation: generation, password: append([]byte(nil), password...)}
}

// backupSessionPassword returns a copy of the current generation's password.
func backupSessionPassword(runtimeHost *processRuntime) ([]byte, bool) {
	generation := runtimeGeneration(runtimeHost)
	backupSessionMu.Lock()
	defer backupSessionMu.Unlock()
	secret, ok := backupSessionPasswords[runtimeHost.pluginID]
	if !ok || secret.generation != generation {
		return nil, false
	}
	return append([]byte(nil), secret.password...), true
}

func clearBackupSessionPassword(pluginID string) {
	backupSessionMu.Lock()
	defer backupSessionMu.Unlock()
	if secret, ok := backupSessionPasswords[pluginID]; ok {
		clear(secret.password)
		delete(backupSessionPasswords, pluginID)
	}
}

func clearBackupSessionPasswords() {
	backupSessionMu.Lock()
	defer backupSessionMu.Unlock()
	for pluginID, secret := range backupSessionPasswords {
		clear(secret.password)
		delete(backupSessionPasswords, pluginID)
	}
}

// backupPassword returns the password create seals with: this run's session
// password, else the remembered one.
func backupPassword(runtimeHost *processRuntime) ([]byte, bool, *rpcError) {
	if password, ok := backupSessionPassword(runtimeHost); ok {
		return password, true, nil
	}
	password, ok, err := backupRememberedPassword()
	if err != nil {
		return nil, false, backupFailure("could not read the remembered backup password", "")
	}
	return password, ok, nil
}

// ---------------------------------------------------------------------------
// Host methods.

func hostBackupStatus(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request struct{}
	if err := decodeBackupParams(params, &request); err != nil {
		return nil, err
	}
	remembered, ok, err := backupRememberedPassword()
	clear(remembered)
	if err != nil {
		return nil, backupFailure("could not read the remembered backup password", "")
	}
	session, inSession := backupSessionPassword(runtimeHost)
	clear(session)
	return map[string]any{
		"passwordSet":    ok || inSession,
		"remembered":     ok,
		"includeSecrets": readBackupOptions(runtimeHost.pluginID).IncludeSecrets,
	}, nil
}

func hostBackupSetOptions(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request struct {
		IncludeSecrets *bool `json:"includeSecrets"`
	}
	if err := decodeBackupParams(params, &request); err != nil {
		return nil, err
	}
	if request.IncludeSecrets == nil {
		return nil, hostUIParamsError("includeSecrets is required")
	}
	raw, _ := json.Marshal(backupOptions{IncludeSecrets: *request.IncludeSecrets})
	if err := pluginKVSetRaw(hostPluginID, backupOptionsKVKey(runtimeHost.pluginID), raw); err != nil {
		return nil, backupFailure("could not save the backup options", "")
	}
	return true, nil
}

// hostBackupSetPassword asks for a new backup password in a host dialog.
// Remembered: stored machine-sealed for every later run. Not remembered: kept
// for this run only, and any previously remembered password is forgotten so a
// later run never seals with a password the user replaced.
func hostBackupSetPassword(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request struct{}
	if err := decodeBackupParams(params, &request); err != nil {
		return nil, err
	}
	value, rpcErr := backupRequests.ask(runtimeHost.promptContext(), runtimeHost, backupKindPassword,
		struct {
			Mode string `json:"mode"`
		}{Mode: "set"}, decodeBackupPasswordReply, backupPasswordTimeout)
	if rpcErr != nil {
		return nil, rpcErr
	}
	reply := value.(backupPasswordReply)
	password := []byte(reply.Password)
	defer clear(password)
	if err := backup.ValidatePassword(password); err != nil {
		return nil, &rpcError{Code: -32602, Message: err.Error(), Data: map[string]string{"code": "passwordTooShort"}}
	}
	if reply.Remember {
		if err := backupRememberPassword(password); err != nil {
			return nil, backupFailure("could not remember the backup password", "")
		}
		clearBackupSessionPassword(runtimeHost.pluginID)
	} else {
		if err := backupForgetPassword(); err != nil {
			return nil, backupFailure("could not forget the previously remembered backup password", "")
		}
		setBackupSessionPassword(runtimeHost, password)
	}
	return map[string]any{"set": true, "remembered": reply.Remember}, nil
}

// buildBackupEntries turns collected frontend values and every plugin's data
// into backup entries with per-section summaries (core sections in display
// order, then plugins). Keys outside the app_ namespace, with characters an
// entry name cannot carry, or bound to this device are skipped.
func buildBackupEntries(manager *pluginManager, values map[string]backupFrontendValue, includeSecrets bool) ([]backup.Entry, []backup.SectionSummary, error) {
	keys := make([]string, 0, len(values))
	for key := range values {
		if _, excluded := backupExcludedKeys[key]; excluded || !backupFrontendKeyPattern.MatchString(key) {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	entries := make([]backup.Entry, 0, len(keys))
	totals := map[string]*backup.SectionSummary{}
	for _, key := range keys {
		data, err := json.Marshal(values[key])
		if err != nil {
			return nil, nil, fmt.Errorf("encode %s: %w", key, err)
		}
		entries = append(entries, backup.Entry{Path: "app/" + key + ".json", Data: data})
		section := backup.SectionForKey(key)
		summary := totals[section]
		if summary == nil {
			summary = &backup.SectionSummary{ID: section, Title: section}
			totals[section] = summary
		}
		summary.Items++
		summary.Bytes += int64(len(data))
	}
	sections := []backup.SectionSummary{}
	for _, id := range backup.CoreSections {
		if summary := totals[id]; summary != nil {
			sections = append(sections, *summary)
		}
	}
	if manager != nil {
		pluginEntries, pluginSections, err := manager.collectPluginBackupEntries(includeSecrets)
		if err != nil {
			return nil, nil, err
		}
		entries = append(entries, pluginEntries...)
		sections = append(sections, pluginSections...)
	}
	return entries, sections, nil
}

// backupMeta is the plugin-visible description of a backup, uploaded as the
// <name>.json side file. It names no section, plugin or connection.
type backupMeta struct {
	Format          string    `json:"format"`
	Version         int       `json:"version"`
	CreatedAt       time.Time `json:"createdAt"`
	AppVersion      string    `json:"appVersion"`
	Device          string    `json:"device"`
	IncludesSecrets bool      `json:"includesSecrets"`
	Size            int64     `json:"size"`
	SHA256          string    `json:"sha256"`
}

func backupDeviceName() string {
	name, err := backupHostname()
	if err != nil {
		return ""
	}
	return cleanHostText(name, backupDeviceMaxRunes, false)
}

func backupFileName(createdAt time.Time) (string, error) {
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", err
	}
	return "mincode-backup-" + createdAt.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(suffix[:]) + ".mcbak", nil
}

// hostBackupCreate collects, compares and seals a backup into the staging
// area. It never prompts: without a password it fails with passwordRequired.
func hostBackupCreate(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request struct {
		LastContentSHA256 string `json:"lastContentSha256"`
	}
	if err := decodeBackupParams(params, &request); err != nil {
		return nil, err
	}
	if request.LastContentSHA256 != "" && !backupSHA256Pattern.MatchString(request.LastContentSHA256) {
		return nil, hostUIParamsError("lastContentSha256 must be a lowercase hex SHA-256")
	}
	password, ok, rpcErr := backupPassword(runtimeHost)
	if rpcErr != nil {
		return nil, rpcErr
	}
	defer clear(password)
	if !ok {
		return nil, backupPasswordRequiredError()
	}
	manager := runtimeHost.ownerManager
	if manager == nil {
		manager = host()
	}
	includeSecrets := readBackupOptions(runtimeHost.pluginID).IncludeSecrets
	value, rpcErr := backupRequests.ask(runtimeHost.promptContext(), runtimeHost, backupKindCollect,
		struct {
			IncludeSecrets bool `json:"includeSecrets"`
		}{IncludeSecrets: includeSecrets},
		func(raw json.RawMessage) (any, error) { return decodeBackupCollectReply(raw) }, backupCollectTimeout)
	if rpcErr != nil {
		return nil, rpcErr
	}
	entries, sections, err := buildBackupEntries(manager, value.(map[string]backupFrontendValue), includeSecrets)
	if err != nil {
		return nil, backupFailure("could not read the data to back up", "")
	}
	contentHash := backup.ContentHash(entries)
	if request.LastContentSHA256 != "" && request.LastContentSHA256 == contentHash {
		return map[string]any{"unchanged": true, "contentSha256": contentHash}, nil
	}

	staged, rpcErr := stageSealedBackup(runtimeHost.pluginID, password, includeSecrets, entries, sections)
	if rpcErr != nil {
		return nil, rpcErr
	}
	return map[string]any{
		"token": staged.Token, "name": staged.Name, "size": staged.Meta.Size, "sha256": staged.Meta.SHA256, "contentSha256": contentHash,
		"meta": staged.Meta,
	}, nil
}

// stagedBackup is a sealed backup in owner's staging area.
type stagedBackup struct {
	Token string
	Name  string
	Meta  backupMeta
}

// stageSealedBackup seals entries with password into owner's staging area,
// as create and the pre-restore snapshot do.
func stageSealedBackup(owner string, password []byte, includeSecrets bool, entries []backup.Entry, sections []backup.SectionSummary) (stagedBackup, *rpcError) {
	createdAt := backupNow().UTC().Truncate(time.Second)
	name, err := backupFileName(createdAt)
	if err != nil {
		return stagedBackup{}, backupFailure("could not name the backup", "")
	}
	manifest := backup.Manifest{CreatedAt: createdAt, AppVersion: currentAppVersion(), Device: backupDeviceName(),
		IncludesSecrets: includeSecrets, Sections: sections}
	staging := backupStaging()
	staging.Sweep()
	token, size, sha, err := staging.Create(owner, func(w io.Writer) error {
		return backup.Seal(w, password, manifest, entries)
	})
	if err != nil {
		if errors.Is(err, backup.ErrBackupTooLarge) {
			return stagedBackup{}, backupFailure(err.Error(), "too_large")
		}
		return stagedBackup{}, backupFailure("could not create the backup", "")
	}
	return stagedBackup{Token: token, Name: name, Meta: backupMeta{Format: backupFileFormat, Version: backupFileVersion, CreatedAt: createdAt,
		AppVersion: manifest.AppVersion, Device: manifest.Device, IncludesSecrets: includeSecrets, Size: size, SHA256: sha}}, nil
}

func hostBackupRead(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request struct {
		Token  string `json:"token"`
		Offset int64  `json:"offset"`
		Length int    `json:"length"`
	}
	if err := decodeBackupParams(params, &request); err != nil {
		return nil, err
	}
	if !validBackupToken(request.Token) || request.Offset < 0 || request.Length <= 0 {
		return nil, hostUIParamsError("token, offset >= 0 and length > 0 are required")
	}
	length := min(request.Length, backup.MaxChunk)
	staging := backupStaging()
	staging.Sweep()
	path, err := staging.Path(runtimeHost.pluginID, request.Token)
	if err != nil {
		return nil, backupStagingError(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, backupStagingError(backup.ErrStagingNotFound)
	}
	data, err := staging.Read(runtimeHost.pluginID, request.Token, request.Offset, length)
	if err != nil {
		return nil, backupStagingError(err)
	}
	return map[string]any{"data": base64.StdEncoding.EncodeToString(data), "eof": request.Offset+int64(len(data)) >= info.Size()}, nil
}

func hostBackupStageBegin(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request struct{}
	if err := decodeBackupParams(params, &request); err != nil {
		return nil, err
	}
	staging := backupStaging()
	staging.Sweep()
	token, err := staging.Begin(runtimeHost.pluginID)
	if err != nil {
		return nil, backupStagingError(err)
	}
	return map[string]any{"token": token}, nil
}

func hostBackupStageAppend(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request struct {
		Token  string `json:"token"`
		Offset int64  `json:"offset"`
		Data   string `json:"data"`
	}
	if err := decodeBackupParams(params, &request); err != nil {
		return nil, err
	}
	if !validBackupToken(request.Token) || request.Offset < 0 {
		return nil, hostUIParamsError("token and offset >= 0 are required")
	}
	if len(request.Data) > base64.StdEncoding.EncodedLen(backup.MaxChunk) {
		return nil, backupStagingError(backup.ErrStagingChunk)
	}
	data, err := base64.StdEncoding.DecodeString(request.Data)
	if err != nil {
		return nil, hostUIParamsError("data must be base64")
	}
	size, err := backupStaging().Append(runtimeHost.pluginID, request.Token, request.Offset, data)
	if err != nil {
		return nil, backupStagingError(err)
	}
	return map[string]any{"size": size}, nil
}

func hostBackupStageCommit(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request struct {
		Token  string `json:"token"`
		SHA256 string `json:"sha256"`
	}
	if err := decodeBackupParams(params, &request); err != nil {
		return nil, err
	}
	if !validBackupToken(request.Token) || !backupSHA256Pattern.MatchString(request.SHA256) {
		return nil, hostUIParamsError("token and a lowercase hex sha256 are required")
	}
	if err := backupStaging().Commit(runtimeHost.pluginID, request.Token, request.SHA256); err != nil {
		return nil, backupStagingError(err)
	}
	return map[string]any{"token": request.Token}, nil
}

func hostBackupRelease(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request struct {
		Token string `json:"token"`
	}
	if err := decodeBackupParams(params, &request); err != nil {
		return nil, err
	}
	if !validBackupToken(request.Token) {
		return nil, hostUIParamsError("token is required")
	}
	backupStaging().Release(runtimeHost.pluginID, request.Token)
	return true, nil
}
