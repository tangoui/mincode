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
	"log"
	"reflect"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mincode/app/backend/core"
	"mincode/app/backend/desktop"
)

// Global shortcut registry: registers the contributes.globalShortcuts entries
// the user turned on (contrib_global_shortcuts.go validates the manifest) with
// the operating system through the desktop adapter (
// app.GlobalShortcut, shared with MinCode's own global shortcut), and runs
// them when pressed.
//
//   - Settings ({enabled, key?, since?}) live in the host KV under
//     globalShortcut/<pluginId>/<id>. Only the dedicated
//     plugin_global_shortcuts_* commands of the settings page write them;
//     no plugin host method or AI path exists. The host namespace never
//     leaves the device (settings export, config sync, backups and plugin
//     data archives all skip it). Uninstall clears a plugin's settings,
//     disable keeps them.
//   - An entry is registered only while it is on, its plugin is enabled,
//     listed and effective in the active workspace, the app is not locked,
//     the session is not Wayland and the combo is free: the same physical
//     combo on this platform as MinCode's own shortcut or as an earlier
//     enabled plugin entry is a conflict (CmdOrCtrl and Super are both
//     Command on macOS); an OS refusal marks it occupied. MinCode's own
//     shortcut always wins (desktop.OnHostShortcutChange evicts first).
//   - A press runs run.type view (open the plugin's tab view in the main
//     window), floating (show/hide its floating view) or action (call the
//     declared uiCallable action in the background with the fixed payload
//     plus {"trigger":{"type":"globalShortcut","id":...}}, as a long task
//     when the runtime negotiated tasks). The press is the user's gesture
//     for that one call (globalShortcutGesture): a write action needs no
//     further confirmation, a dangerous one is refused (install validation
//     already refuses it). When the action has run the plugin gets a
//     single-use activation token, valid for 120 s, that lets it bring its
//     own tab view to the front once: delivered as
//     plugin.globalShortcut.activation (global_shortcuts_activation.go),
//     consumed by host.view.open {raise: true, activation} (host_ui.go).
//   - Rejections by the host (plugin disabled, not effective, failed, app
//     locked, start failure, still busy) post a system notification.
const (
	globalShortcutsChangedEvent  = "plugin:global-shortcuts-changed"
	globalShortcutSettingPrefix  = "globalShortcut/"
	globalShortcutHostConflict   = "mincode"
	globalShortcutActivationTTL  = 120 * time.Second
	globalShortcutActivationMax  = 64
	globalShortcutTaskTimeout    = 300 * time.Second
	globalShortcutInAppKeysMax   = 512
	globalShortcutNoticeInterval = 3 * time.Second
)

// Entry states reported to the settings page.
const (
	globalShortcutStateEnabled     = "enabled"
	globalShortcutStateDisabled    = "disabled"
	globalShortcutStateConflict    = "conflict"
	globalShortcutStateOccupied    = "occupied"
	globalShortcutStateUnsupported = "unsupported"
	globalShortcutStateInactive    = "inactive"
)

// GlobalShortcutStatus is one entry of plugin_global_shortcuts_list.
type GlobalShortcutStatus struct {
	PluginID       string            `json:"pluginId"`
	PluginName     string            `json:"pluginName"`
	ID             string            `json:"id"`
	Title          string            `json:"title"`
	TitleLocalized map[string]string `json:"titleLocalized,omitempty"`
	DefaultKey     string            `json:"defaultKey"`
	Key            string            `json:"key"`
	Enabled        bool              `json:"enabled"`
	State          string            `json:"state"`
	// ConflictWith is "mincode" (MinCode's own shortcut) or "<pluginId>/<id>".
	ConflictWith string `json:"conflictWith,omitempty"`
	// InAppOverlap: an in-app binding (pushed by the frontend) uses the same
	// combo; a warning only.
	InAppOverlap bool `json:"inAppOverlap,omitempty"`
	// ChangedByUpdate: the user turned the entry on, then a plugin update
	// changed its default key or what it runs; it stays off until turned on
	// again.
	ChangedByUpdate bool `json:"changedByUpdate,omitempty"`
}

// globalShortcutSetting is the stored value. Since (Unix ms) orders claims on
// the same combo, so the entry turned on first keeps it across restarts.
// Fingerprint (globalShortcutFingerprint) is the entry the user turned on; an
// update that changes it turns the entry off. Settings stored without one
// (pre-release builds) are taken as matching.
type globalShortcutSetting struct {
	Enabled     bool   `json:"enabled"`
	Key         string `json:"key,omitempty"`
	Since       int64  `json:"since,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

// globalShortcutFingerprint identifies what an entry presses and does: its
// default key, run type, view, action and payload (canonical: object keys
// sorted). Titles are left out, so a renamed entry stays on.
func globalShortcutFingerprint(entry GlobalShortcutEntry) string {
	payload := string(entry.Run.Payload)
	if len(entry.Run.Payload) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(entry.Run.Payload))
		decoder.UseNumber()
		var value any
		if decoder.Decode(&value) == nil {
			if canonical, err := json.Marshal(value); err == nil {
				payload = string(canonical)
			}
		}
	}
	data, _ := json.Marshal([]string{globalShortcutCanonicalKey(entry.Key), entry.Run.Type, entry.Run.View, entry.Run.Action, payload})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:16])
}

// globalShortcutCanonicalKey is key's platform-independent logical combo:
// the sorted logical modifiers plus the canonical key name, so a manifest
// that only respells its key ("Shift+CmdOrCtrl+a", "Command+Shift+A") keeps
// the same fingerprint. Logical modifiers stay distinct (CmdOrCtrl, Ctrl and
// Super differ on at least one platform). Keys outside the syntax (not valid
// in an installed manifest) fall back to their trimmed text.
//
// Fingerprints were hashed from the raw key before this; that only ever ran
// on the unreleased feature branch, so no legacy fingerprint is accepted.
func globalShortcutCanonicalKey(key string) string {
	key = strings.TrimSpace(key)
	parsed, err := parseGlobalAccelerator(key)
	if err != nil {
		return key
	}
	if parsed.key == "enter" {
		parsed.key = "return" // accelerators treat both names as one key.
	}
	modifiers := slices.Clone(parsed.modifiers)
	slices.Sort(modifiers)
	names := [...]string{modifierCmdOrCtrl: "CmdOrCtrl", modifierCtrl: "Ctrl", modifierAlt: "Alt", modifierShift: "Shift", modifierSuper: "Super"}
	parts := make([]string, 0, len(modifiers)+1)
	for _, modifier := range modifiers {
		parts = append(parts, names[modifier])
	}
	return strings.Join(append(parts, parsed.key), "+")
}

// effectiveEnabled reports whether setting turns entry on, and whether it was
// turned on for an entry an update has since changed.
func (setting globalShortcutSetting) effectiveEnabled(entry GlobalShortcutEntry) (enabled, changedByUpdate bool) {
	if !setting.Enabled {
		return false, false
	}
	if setting.Fingerprint != "" && setting.Fingerprint != globalShortcutFingerprint(entry) {
		return false, true
	}
	return true, false
}

func globalShortcutSettingKey(pluginID, shortcutID string) string {
	return globalShortcutSettingPrefix + pluginID + "/" + shortcutID
}

// globalShortcutDesktop is the native seam (desktop.Adapter in the app).
type globalShortcutDesktop interface {
	RegisterPluginShortcut(accelerator string, fn func()) error
	UnregisterPluginShortcut(accelerator string) error
	HostShortcut() string
	IsWayland() bool
	Available() bool
}

type nativeGlobalShortcuts struct{}

func (nativeGlobalShortcuts) RegisterPluginShortcut(accelerator string, fn func()) error {
	return desktop.RegisterPluginShortcut(accelerator, fn)
}
func (nativeGlobalShortcuts) UnregisterPluginShortcut(accelerator string) error {
	return desktop.UnregisterPluginShortcut(accelerator)
}
func (nativeGlobalShortcuts) HostShortcut() string { return desktop.HostShortcut() }
func (nativeGlobalShortcuts) IsWayland() bool      { return desktop.IsWayland() }
func (nativeGlobalShortcuts) Available() bool      { return desktop.PluginShortcutsAvailable() }

func currentShortcutPlatform() string {
	switch runtime.GOOS {
	case "darwin":
		return "mac"
	case "windows":
		return "windows"
	default:
		return "linux"
	}
}

// globalShortcutPhysical is key's physical combo on platform; keys outside
// the manifest syntax (MinCode's own shortcut is set by the frontend) fall
// back to their lower-cased text.
func globalShortcutPhysical(key, platform string) string {
	key = strings.TrimSpace(key)
	parsed, err := parseGlobalAccelerator(key)
	if err != nil {
		return strings.ToLower(key)
	}
	if parsed.key == "enter" {
		parsed.key = "return" // accelerators treat both names as one key.
	}
	return parsed.physical(platform)
}

// ---- App lock ----

var appLockSource atomic.Pointer[func() bool]

// SetAppLockSource tells the registry how to read the app lock state
// (applock.Locked); wired in backend/register_all.go.
func SetAppLockSource(locked func() bool) {
	if locked == nil {
		appLockSource.Store(nil)
		return
	}
	appLockSource.Store(&locked)
}

// appLocked fails closed: until backend/register_all.go injected the
// applock source, the app counts as locked, so no shortcut registers, runs or
// earns an activation token. Tests that need the registry unlocked set
// registry.locked (newShortcutHarness) or call SetAppLockSource.
func appLocked() bool {
	if source := appLockSource.Load(); source != nil {
		return (*source)()
	}
	return true
}

// AppLockChanged re-evaluates the registry after the app was locked or
// unlocked: locking unregisters every plugin shortcut and revokes every
// activation token, unlocking registers them again. The plugin tray section
// is emptied on lock and restored on unlock (tray_menu.go).
func AppLockChanged() {
	trayAppLockChanged()
	registry := activeGlobalShortcutRegistry.Load()
	if registry == nil {
		if appLocked() {
			revokeActivationTokens("")
		}
		return
	}
	if registry.isLocked() {
		revokeActivationTokens("")
	}
	registry.reconcile(true)
}

// activeGlobalShortcutRegistry is the started registry that host shortcut
// and app lock changes reach.
var activeGlobalShortcutRegistry atomic.Pointer[globalShortcutRegistry]

// ---- Registry ----

type globalShortcutBinding struct {
	pluginID    string
	entryID     string
	accelerator string
	seq         uint64
}

type globalShortcutCandidate struct {
	pluginID   string
	pluginName string
	entry      GlobalShortcutEntry
	active     bool
}

type globalShortcutRegistry struct {
	manager *pluginManager
	// Seams; newGlobalShortcutRegistry sets the native implementations.
	desk     globalShortcutDesktop
	platform string
	locked   func() bool
	navigate func(desktop.NavigationTarget) error
	notify   func(pluginNotification)
	invoke   func(context.Context, contributedActionCall) (globalShortcutRun, error)
	// onActivation delivers an issued token; false revokes it.
	onActivation func(pluginID, shortcutID, token string, expiresAt time.Time, run globalShortcutRun) bool
	emit         func()
	now          func() time.Time

	// opMu serializes evaluation, OS registration and settings writes.
	opMu           sync.Mutex
	settings       map[string]globalShortcutSetting
	settingsLoaded bool

	// mu guards the fields below; never held across OS or plugin calls.
	mu       sync.Mutex
	started  bool
	computed bool
	bound    map[string]globalShortcutBinding // setting key → OS binding
	// stale holds bindings the OS refused to release (accelerator → binding):
	// the OS may still call them, presses are ignored, and every pass retries
	// the release before the combo is used again.
	stale       map[string]globalShortcutBinding
	occupied    map[string]string // setting key → refused accelerator
	statuses    []GlobalShortcutStatus
	inAppKeys   []string
	hostPending string
	seq         uint64
	revision    uint64
	dirty       bool
	worker      bool
	noticeAt    map[string]time.Time
	work        int
	idle        *sync.Cond

	reconciles atomic.Int64
}

func newGlobalShortcutRegistry(manager *pluginManager) *globalShortcutRegistry {
	registry := &globalShortcutRegistry{
		manager:      manager,
		desk:         nativeGlobalShortcuts{},
		platform:     currentShortcutPlatform(),
		locked:       appLocked,
		navigate:     desktop.Navigate,
		notify:       func(notice pluginNotification) { core.Emit(pluginNotifyEvent, notice) },
		onActivation: manager.sendGlobalShortcutActivation,
		now:          time.Now,
		settings:     map[string]globalShortcutSetting{},
		bound:        map[string]globalShortcutBinding{},
		stale:        map[string]globalShortcutBinding{},
		occupied:     map[string]string{},
		noticeAt:     map[string]time.Time{},
	}
	registry.invoke = func(ctx context.Context, call contributedActionCall) (globalShortcutRun, error) {
		return manager.runGlobalShortcutAction(ctx, call, registry.isLocked)
	}
	registry.emit = func() {
		registry.mu.Lock()
		registry.revision++
		revision := registry.revision
		registry.mu.Unlock()
		core.Emit(globalShortcutsChangedEvent, map[string]any{"revision": revision})
	}
	registry.idle = sync.NewCond(&registry.mu)
	return registry
}

// globalShortcutRegistry returns the manager's registry, creating it (not
// started) on first use.
func (manager *pluginManager) globalShortcutRegistry() *globalShortcutRegistry {
	if registry := manager.shortcuts.Load(); registry != nil {
		return registry
	}
	registry := newGlobalShortcutRegistry(manager)
	if manager.shortcuts.CompareAndSwap(nil, registry) {
		return registry
	}
	return manager.shortcuts.Load()
}

// startGlobalShortcuts registers the stored, enabled entries (plugin domain
// startup).
func (manager *pluginManager) startGlobalShortcuts() { manager.globalShortcutRegistry().start() }

// StartGlobalShortcutsInBackground registers the stored, enabled plugin
// global shortcuts at launch (backend startupDomains), without waiting for the
// first plugin command that starts the rest of the plugin domain. It only
// reads the static package scan (shared with PrewarmInBackground) and the
// host KV; no plugin process starts until a shortcut is pressed.
func StartGlobalShortcutsInBackground() { startGlobalShortcutsInBackground(host) }

func startGlobalShortcutsInBackground(get func() *pluginManager) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if recovered := recover(); recovered != nil {
				log.Printf("plugin global shortcuts: start at launch failed: %v", recovered)
			}
		}()
		get().startGlobalShortcuts()
	}()
	return done
}

func (r *globalShortcutRegistry) start() {
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return
	}
	r.started = true
	r.mu.Unlock()
	activeGlobalShortcutRegistry.Store(r)
	r.reconcile(true)
}

// stop unregisters everything (shutdown).
func (r *globalShortcutRegistry) stop() {
	r.opMu.Lock()
	defer r.opMu.Unlock()
	r.mu.Lock()
	r.started = false
	bound := r.bound
	r.bound = map[string]globalShortcutBinding{}
	for _, binding := range r.stale {
		bound["stale:"+binding.accelerator] = binding
	}
	r.stale = map[string]globalShortcutBinding{}
	r.mu.Unlock()
	for _, binding := range bound {
		if err := r.desk.UnregisterPluginShortcut(binding.accelerator); err != nil {
			log.Printf("plugin global shortcut %s/%s: unregister: %v", binding.pluginID, binding.entryID, err)
		}
	}
	activeGlobalShortcutRegistry.CompareAndSwap(r, nil)
}

func (r *globalShortcutRegistry) isLocked() bool { return r.locked != nil && r.locked() }

func (r *globalShortcutRegistry) beginWork() { r.mu.Lock(); r.work++; r.mu.Unlock() }

func (r *globalShortcutRegistry) endWork() {
	r.mu.Lock()
	r.work--
	if r.work == 0 {
		r.idle.Broadcast()
	}
	r.mu.Unlock()
}

// wait blocks until no reconcile worker or dispatch is running (tests).
func (r *globalShortcutRegistry) wait() {
	r.mu.Lock()
	for r.work > 0 {
		r.idle.Wait()
	}
	r.mu.Unlock()
}

// requestReconcile re-evaluates on a background worker; requests that arrive
// while it runs coalesce into one more pass. Callers may hold plugin locks.
func (r *globalShortcutRegistry) requestReconcile() {
	r.mu.Lock()
	if !r.started {
		r.mu.Unlock()
		return
	}
	r.dirty = true
	if r.worker {
		r.mu.Unlock()
		return
	}
	r.worker = true
	r.work++
	r.mu.Unlock()
	go func() {
		defer r.endWork()
		for {
			r.mu.Lock()
			if !r.dirty {
				r.worker = false
				r.mu.Unlock()
				return
			}
			r.dirty = false
			r.mu.Unlock()
			r.reconcile(false)
		}
	}()
}

// hostShortcutChanging runs around every change of MinCode's own shortcut
// (desktop.OnHostShortcutChange): pending is about to be registered, so a
// plugin binding of the same combo is released first.
func (r *globalShortcutRegistry) hostShortcutChanging(pending string) {
	r.mu.Lock()
	r.hostPending = strings.TrimSpace(pending)
	r.mu.Unlock()
	r.reconcile(pending == "")
}

func (r *globalShortcutRegistry) reconcile(retryOccupied bool) {
	r.opMu.Lock()
	defer r.opMu.Unlock()
	r.reconcileLocked(retryOccupied)
}

// loadSettingsLocked reads the stored settings once. Caller holds opMu.
func (r *globalShortcutRegistry) loadSettingsLocked() {
	if r.settingsLoaded {
		return
	}
	keys, err := pluginKVListRaw(hostPluginID)
	if err != nil {
		log.Printf("plugin global shortcuts: read settings: %v", err)
		return
	}
	settings := map[string]globalShortcutSetting{}
	for _, key := range keys {
		rest, ok := strings.CutPrefix(key, globalShortcutSettingPrefix)
		if !ok {
			continue
		}
		slash := strings.LastIndex(rest, "/")
		if slash <= 0 || !globalShortcutIDPattern.MatchString(rest[slash+1:]) {
			continue
		}
		raw, found, err := pluginKVGetRaw(hostPluginID, key)
		if err != nil || !found {
			continue
		}
		var setting globalShortcutSetting
		if json.Unmarshal(raw, &setting) != nil {
			continue
		}
		if setting.Key != "" && ValidAccelerator(setting.Key) != nil {
			setting.Key = "" // A stored key that no longer validates falls back to the default.
		}
		settings[key] = setting
	}
	r.settings, r.settingsLoaded = settings, true
}

func (r *globalShortcutRegistry) storeSettingLocked(key string, setting globalShortcutSetting) error {
	if !setting.Enabled && setting.Key == "" {
		if err := pluginKVDeleteRaw(hostPluginID, key); err != nil {
			return fmt.Errorf("save global shortcut setting: %w", err)
		}
		delete(r.settings, key)
		return nil
	}
	value, err := json.Marshal(setting)
	if err != nil {
		return err
	}
	if err := pluginKVSetRaw(hostPluginID, key, value); err != nil {
		return fmt.Errorf("save global shortcut setting: %w", err)
	}
	r.settings[key] = setting
	return nil
}

// globalShortcutCandidates lists every declared entry of installed process
// plugins in manager order; active means it may be registered now.
func (manager *pluginManager) globalShortcutCandidates() []globalShortcutCandidate {
	manager.mu.RLock()
	records := manager.orderedRecordsLocked()
	manager.mu.RUnlock()
	var candidates []globalShortcutCandidate
	for _, record := range records {
		record.mu.Lock()
		if record.builtin == nil && record.manifest != nil && !record.manifest.isDeclarative() {
			entries, _ := parseGlobalShortcuts(record.manifest)
			active := record.contributionsListedLocked() && manager.workspaceActiveLocked(record)
			for _, entry := range entries {
				candidates = append(candidates, globalShortcutCandidate{pluginID: record.id, pluginName: record.manifest.Name, entry: entry, active: active})
			}
		}
		record.mu.Unlock()
	}
	return candidates
}

type globalShortcutClaim struct {
	index int
	key   string
	since int64
	bound bool
}

// reconcileLocked recomputes every entry's state and brings the OS
// registrations in line. Caller holds opMu.
func (r *globalShortcutRegistry) reconcileLocked(retryOccupied bool) {
	r.reconciles.Add(1)
	candidates := r.manager.globalShortcutCandidates()
	r.loadSettingsLocked()
	r.mu.Lock()
	started := r.started
	if retryOccupied {
		r.occupied = map[string]string{}
	}
	bound := make(map[string]globalShortcutBinding, len(r.bound))
	for key, binding := range r.bound {
		bound[key] = binding
	}
	hostPending := r.hostPending
	inAppKeys := r.inAppKeys
	r.mu.Unlock()

	locked := r.isLocked()
	unsupported := r.desk == nil || !r.desk.Available() || r.desk.IsWayland()
	host := map[string]bool{}
	if !unsupported {
		for _, key := range []string{r.desk.HostShortcut(), hostPending} {
			if physical := globalShortcutPhysical(key, r.platform); physical != "" {
				host[physical] = true
			}
		}
	}
	inApp := map[string]bool{}
	for _, key := range inAppKeys {
		inApp[globalShortcutPhysical(key, r.platform)] = true
	}

	statuses := make([]GlobalShortcutStatus, len(candidates))
	var claims []globalShortcutClaim
	for index, candidate := range candidates {
		key := globalShortcutSettingKey(candidate.pluginID, candidate.entry.ID)
		setting := r.settings[key]
		accelerator := candidate.entry.Key
		if setting.Key != "" {
			accelerator = setting.Key
		}
		enabled, changedByUpdate := setting.effectiveEnabled(candidate.entry)
		if enabled && setting.Fingerprint == "" {
			// Stored by a pre-release build: pin the entry it was turned on for.
			setting.Fingerprint = globalShortcutFingerprint(candidate.entry)
			if err := r.storeSettingLocked(key, setting); err != nil {
				log.Printf("plugin global shortcut %s/%s: %v", candidate.pluginID, candidate.entry.ID, err)
			}
		}
		status := GlobalShortcutStatus{
			PluginID: candidate.pluginID, PluginName: candidate.pluginName, ID: candidate.entry.ID,
			Title: candidate.entry.Title, TitleLocalized: candidate.entry.TitleLocalized,
			DefaultKey: candidate.entry.Key, Key: accelerator, Enabled: enabled, ChangedByUpdate: changedByUpdate,
			InAppOverlap: inApp[globalShortcutPhysical(accelerator, r.platform)],
		}
		switch {
		case unsupported:
			status.State = globalShortcutStateUnsupported
		case !enabled:
			status.State = globalShortcutStateDisabled
		case !candidate.active || locked || !started:
			status.State = globalShortcutStateInactive
		default:
			existing, isBound := bound[key]
			claims = append(claims, globalShortcutClaim{index: index, key: key, since: setting.Since, bound: isBound && existing.accelerator == accelerator})
		}
		statuses[index] = status
	}
	// Bound entries keep their combo; otherwise the one turned on first wins.
	sort.SliceStable(claims, func(a, b int) bool {
		if claims[a].bound != claims[b].bound {
			return claims[a].bound
		}
		return claims[a].since < claims[b].since
	})
	claimed := map[string]string{}
	wanted := map[string]globalShortcutBinding{}
	for _, claim := range claims {
		status := &statuses[claim.index]
		physical := globalShortcutPhysical(status.Key, r.platform)
		if host[physical] {
			status.State, status.ConflictWith = globalShortcutStateConflict, globalShortcutHostConflict
			continue
		}
		if owner, taken := claimed[physical]; taken {
			status.State, status.ConflictWith = globalShortcutStateConflict, owner
			continue
		}
		claimed[physical] = status.PluginID + "/" + status.ID
		wanted[claim.key] = globalShortcutBinding{pluginID: status.PluginID, entryID: status.ID, accelerator: status.Key}
		status.State = globalShortcutStateEnabled
	}

	// Bindings the OS refused to release earlier: retry first, so a combo
	// freed now can be registered again in this pass.
	r.mu.Lock()
	stale := make([]globalShortcutBinding, 0, len(r.stale))
	for _, binding := range r.stale {
		stale = append(stale, binding)
	}
	r.mu.Unlock()
	for _, binding := range stale {
		if err := r.desk.UnregisterPluginShortcut(binding.accelerator); err != nil {
			log.Printf("plugin global shortcut %s/%s: unregister %s (retry): %v", binding.pluginID, binding.entryID, binding.accelerator, err)
			continue
		}
		r.mu.Lock()
		delete(r.stale, binding.accelerator)
		r.mu.Unlock()
	}

	// Release first, so a combo can move between entries in one pass.
	for key, binding := range bound {
		if want, ok := wanted[key]; ok && want.accelerator == binding.accelerator {
			continue
		}
		err := r.desk.UnregisterPluginShortcut(binding.accelerator)
		delete(bound, key)
		r.mu.Lock()
		delete(r.bound, key)
		if err != nil {
			// The OS may still call it: keep it (presses are ignored) and retry.
			r.stale[binding.accelerator] = binding
		}
		r.mu.Unlock()
		if err != nil {
			log.Printf("plugin global shortcut %s/%s: unregister %s: %v", binding.pluginID, binding.entryID, binding.accelerator, err)
		}
	}
	for _, claim := range claims {
		want, ok := wanted[claim.key]
		if !ok {
			continue
		}
		if _, isBound := bound[claim.key]; isBound {
			continue
		}
		r.mu.Lock()
		held, isStale := r.stale[want.accelerator]
		if isStale && held.pluginID == want.pluginID && held.entryID == want.entryID {
			// The same entry wants the combo it still holds: its callback
			// (key and seq) is still the OS's, so take it back.
			delete(r.stale, want.accelerator)
			r.bound[claim.key] = held
			delete(r.occupied, claim.key)
			r.mu.Unlock()
			continue
		}
		refused := r.occupied[claim.key] == want.accelerator
		r.seq++
		want.seq = r.seq
		r.mu.Unlock()
		if isStale {
			// Another entry's binding still holds it at the OS.
			statuses[claim.index].State = globalShortcutStateOccupied
			continue
		}
		if refused {
			statuses[claim.index].State = globalShortcutStateOccupied
			continue
		}
		key, seq := claim.key, want.seq
		if err := r.desk.RegisterPluginShortcut(want.accelerator, func() { r.pressed(key, seq) }); err != nil {
			log.Printf("plugin global shortcut %s/%s: register %s: %v", want.pluginID, want.entryID, want.accelerator, err)
			statuses[claim.index].State = globalShortcutStateOccupied
			r.mu.Lock()
			r.occupied[claim.key] = want.accelerator
			r.mu.Unlock()
			continue
		}
		r.mu.Lock()
		delete(r.occupied, claim.key)
		r.bound[claim.key] = want
		r.mu.Unlock()
	}

	r.mu.Lock()
	for key := range r.occupied {
		if _, ok := wanted[key]; !ok {
			delete(r.occupied, key)
		}
	}
	changed := !r.computed || !reflect.DeepEqual(r.statuses, statuses)
	r.statuses, r.computed = statuses, true
	r.mu.Unlock()
	if changed && r.emit != nil {
		r.emit()
	}
}

// list returns every declared entry with its state.
func (r *globalShortcutRegistry) list() []GlobalShortcutStatus {
	r.mu.Lock()
	computed := r.computed
	r.mu.Unlock()
	if !computed {
		r.reconcile(false)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]GlobalShortcutStatus, len(r.statuses))
	copy(out, r.statuses)
	return out
}

func (r *globalShortcutRegistry) statusLocked(pluginID, shortcutID string) (GlobalShortcutStatus, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, status := range r.statuses {
		if status.PluginID == pluginID && status.ID == shortcutID {
			return status, true
		}
	}
	return GlobalShortcutStatus{}, false
}

// set changes one entry's switch and/or key (nil leaves it; an empty key or
// the manifest default means the default) and returns its new state.
func (r *globalShortcutRegistry) set(pluginID, shortcutID string, enabled *bool, key *string) (GlobalShortcutStatus, error) {
	pluginID, shortcutID = strings.TrimSpace(pluginID), strings.TrimSpace(shortcutID)
	r.opMu.Lock()
	defer r.opMu.Unlock()
	var entry *GlobalShortcutEntry
	for _, candidate := range r.manager.globalShortcutCandidates() {
		if candidate.pluginID == pluginID && candidate.entry.ID == shortcutID {
			entry = &candidate.entry
			break
		}
	}
	if entry == nil {
		return GlobalShortcutStatus{}, fmt.Errorf("plugin %q has no global shortcut %q", pluginID, shortcutID)
	}
	r.loadSettingsLocked()
	if !r.settingsLoaded {
		return GlobalShortcutStatus{}, errors.New("global shortcut settings are not available")
	}
	settingKey := globalShortcutSettingKey(pluginID, shortcutID)
	previous := r.settings[settingKey]
	if enabled, _ := previous.effectiveEnabled(*entry); !enabled {
		// Off, or on for an entry an update has since changed: only turning
		// it on again (which confirms the current entry) enables it.
		previous.Enabled, previous.Since, previous.Fingerprint = false, 0, ""
	}
	setting := previous
	if key != nil {
		value := strings.TrimSpace(*key)
		if value == "" || value == entry.Key {
			setting.Key = ""
		} else {
			if err := ValidAccelerator(value); err != nil {
				return GlobalShortcutStatus{}, fmt.Errorf("global shortcut key: %w", err)
			}
			setting.Key = value
		}
	}
	if enabled != nil {
		setting.Enabled = *enabled
	}
	switch {
	case !setting.Enabled:
		setting.Since, setting.Fingerprint = 0, ""
	case !previous.Enabled || setting.Key != previous.Key:
		setting.Since = r.now().UnixMilli()
	}
	if setting.Enabled {
		setting.Fingerprint = globalShortcutFingerprint(*entry)
	}
	if err := r.storeSettingLocked(settingKey, setting); err != nil {
		return GlobalShortcutStatus{}, err
	}
	r.mu.Lock()
	delete(r.occupied, settingKey) // Turning it on again retries an occupied combo.
	r.mu.Unlock()
	r.reconcileLocked(false)
	status, _ := r.statusLocked(pluginID, shortcutID)
	return status, nil
}

// reset restores the manifest's default key (the switch is unchanged).
func (r *globalShortcutRegistry) reset(pluginID, shortcutID string) (GlobalShortcutStatus, error) {
	empty := ""
	return r.set(pluginID, shortcutID, nil, &empty)
}

// changedByUpdate lists the IDs of pluginID's entries that are on now and
// that installing entries (an update's manifest) would turn off, because
// their default key or what they run changed (install preview).
func (r *globalShortcutRegistry) changedByUpdate(pluginID string, entries []GlobalShortcutEntry) []string {
	current := map[string]GlobalShortcutEntry{}
	for _, candidate := range r.manager.globalShortcutCandidates() {
		if candidate.pluginID == pluginID {
			current[candidate.entry.ID] = candidate.entry
		}
	}
	r.opMu.Lock()
	defer r.opMu.Unlock()
	r.loadSettingsLocked()
	var changed []string
	for _, entry := range entries {
		installed, ok := current[entry.ID]
		if !ok {
			continue
		}
		setting := r.settings[globalShortcutSettingKey(pluginID, entry.ID)]
		if on, _ := setting.effectiveEnabled(installed); !on {
			continue
		}
		if globalShortcutFingerprint(entry) != globalShortcutFingerprint(installed) {
			changed = append(changed, entry.ID)
		}
	}
	return changed
}

// setInAppKeys records the frontend's in-app bindings (accelerator
// syntax) for the inAppOverlap warning; it never changes registrations.
func (r *globalShortcutRegistry) setInAppKeys(keys []string) error {
	if len(keys) > globalShortcutInAppKeysMax {
		return fmt.Errorf("at most %d in-app keys", globalShortcutInAppKeysMax)
	}
	cleaned := make([]string, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key != "" && len(key) <= globalShortcutKeyMaxLength {
			cleaned = append(cleaned, key)
		}
	}
	r.mu.Lock()
	r.inAppKeys = cleaned
	r.mu.Unlock()
	r.reconcile(false)
	return nil
}

// forgetPlugin drops an uninstalled plugin's settings.
func (r *globalShortcutRegistry) forgetPlugin(pluginID string) {
	r.opMu.Lock()
	defer r.opMu.Unlock()
	deleteGlobalShortcutSettings(pluginID)
	prefix := globalShortcutSettingKey(pluginID, "")
	for key := range r.settings {
		if strings.HasPrefix(key, prefix) {
			delete(r.settings, key)
		}
	}
	r.reconcileLocked(false)
}

func deleteGlobalShortcutSettings(pluginID string) {
	keys, err := pluginKVListRaw(hostPluginID)
	if err != nil {
		log.Printf("plugin global shortcuts: list settings: %v", err)
		return
	}
	prefix := globalShortcutSettingKey(pluginID, "")
	for _, key := range keys {
		if strings.HasPrefix(key, prefix) {
			_ = pluginKVDeleteRaw(hostPluginID, key)
		}
	}
}

// ---- Trigger ----

// pressed runs on the OS callback goroutine.
func (r *globalShortcutRegistry) pressed(key string, seq uint64) {
	r.mu.Lock()
	binding, ok := r.bound[key]
	started := r.started
	r.mu.Unlock()
	if !ok || binding.seq != seq || !started {
		return
	}
	r.beginWork()
	go func() {
		defer r.endWork()
		if r.isLocked() {
			// Registrations go away on lock; this press raced it.
			r.reject(binding.pluginID, "", binding.entryID, "locked")
			return
		}
		r.dispatch(binding)
	}()
}

// globalShortcutTarget re-checks a pressed entry: the plugin is enabled,
// effective and listed and still declares it. reason is empty when it may run.
func (manager *pluginManager) globalShortcutTarget(pluginID, shortcutID string) (GlobalShortcutEntry, string, string) {
	record, err := manager.record(pluginID)
	if err != nil {
		return GlobalShortcutEntry{}, "", "unavailable"
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	name := ""
	if record.manifest != nil {
		name = record.manifest.Name
	}
	switch {
	case !record.enabled:
		return GlobalShortcutEntry{}, name, "disabled"
	case !manager.workspaceActiveLocked(record):
		return GlobalShortcutEntry{}, name, "inactive"
	case record.manifest == nil || !record.contributionsListedLocked():
		return GlobalShortcutEntry{}, name, "unavailable"
	}
	entries, _ := parseGlobalShortcuts(record.manifest)
	for _, entry := range entries {
		if entry.ID == shortcutID {
			return entry, name, ""
		}
	}
	return GlobalShortcutEntry{}, name, "unavailable"
}

func (r *globalShortcutRegistry) dispatch(binding globalShortcutBinding) {
	entry, pluginName, reason := r.manager.globalShortcutTarget(binding.pluginID, binding.entryID)
	if reason != "" {
		r.reject(binding.pluginID, pluginName, binding.entryID, reason)
		return
	}
	var err error
	switch entry.Run.Type {
	case "view":
		value, _ := json.Marshal(map[string]string{"pluginId": binding.pluginID, "viewId": entry.Run.View})
		err = r.navigate(desktop.NavigationTarget{Kind: "plugin", Value: string(value)})
	case "floating":
		if FloatingViewVisible(binding.pluginID, entry.Run.View) {
			err = r.manager.HideFloatingView(binding.pluginID, entry.Run.View)
		} else {
			err = r.manager.ShowFloatingView(binding.pluginID, entry.Run.View)
		}
	case "action":
		r.runAction(binding.pluginID, pluginName, entry)
		return
	default:
		err = fmt.Errorf("unknown run type %q", entry.Run.Type)
	}
	if err != nil {
		r.logToPlugin(binding.pluginID, fmt.Sprintf("global shortcut %s failed: %v", entry.ID, err))
		r.reject(binding.pluginID, pluginName, entry.ID, "failed")
	}
}

func (r *globalShortcutRegistry) runAction(pluginID, pluginName string, entry GlobalShortcutEntry) {
	call := contributedActionCall{
		PluginID: pluginID, Point: globalShortcutPointKey, EntryID: entry.ID, ActionID: entry.Run.Action,
		RequestID: globalShortcutRequestID(), Payload: globalShortcutActionPayload(entry.Run.Payload, entry.ID),
		Validate: globalShortcutEntryValidator(entry.ID, entry.Run.Action),
		gesture:  newGlobalShortcutGesture(pluginID, entry.ID, entry.Run.Action),
	}
	run, err := r.invoke(context.Background(), call)
	if !run.reached {
		if err != nil {
			r.logToPlugin(pluginID, fmt.Sprintf("global shortcut %s was not run: %v", entry.ID, err))
		}
		r.reject(pluginID, pluginName, entry.ID, globalShortcutNotRunReason(err))
		return
	}
	if err != nil {
		// The plugin reports its own failures (its page via the token, or host.notify).
		r.logToPlugin(pluginID, fmt.Sprintf("global shortcut %s: action %s failed: %v", entry.ID, entry.Run.Action, err))
	}
	// issue checks the lock under the token store's mutex, so a lock landing
	// now either refuses the token or revokes it (AppLockChanged).
	token, expiresAt, ok := globalShortcutActivations.issue(pluginID, entry.ID, r.isLocked)
	if !ok {
		return
	}
	if r.onActivation == nil || !r.onActivation(pluginID, entry.ID, token, expiresAt, run) {
		globalShortcutActivations.revokeToken(token)
	}
}

// errGlobalShortcutLocked: the app locked between the press and the call.
var errGlobalShortcutLocked = errors.New("MinCode is locked")

// globalShortcutNotRunReason maps why an action press never reached the
// plugin to its notification reason (globalShortcut.<reason>):
//   - locked: the app locked after the press was admitted;
//   - startFailed: the plugin could not be started (lazy activation);
//   - busy: the per-plugin slot, the plugin itself or the task limit;
//   - unavailable: the call was refused (forbidden, the action or entry is
//     gone, contributions no longer active);
//   - failed: anything else (invalid request, payload too large, a runtime
//     without tasks support, unexpected errors).
func globalShortcutNotRunReason(err error) string {
	var coded *contributionError
	switch {
	case errors.Is(err, errGlobalShortcutLocked):
		return "locked"
	case errors.Is(err, errTaskLimitReached):
		return "busy"
	case errors.As(err, &coded) && coded.activation:
		return "startFailed"
	}
	switch contributionErrorCodeOf(err) {
	case contributionErrBusy:
		return "busy"
	case contributionErrForbidden, contributionErrNotFound, contributionErrUnavailable:
		return "unavailable"
	}
	return "failed"
}

func (r *globalShortcutRegistry) logToPlugin(pluginID, message string) {
	if record, err := r.manager.record(pluginID); err == nil {
		record.appendLog("warn", message)
	}
}

var globalShortcutRejections = map[string]struct {
	level, zh, en string
}{
	"disabled":    {"warning", "插件已停用，快捷键未执行。", "The plugin is disabled; the shortcut did nothing."},
	"inactive":    {"warning", "插件在当前工作区未启用，快捷键未执行。", "The plugin is not enabled in this workspace; the shortcut did nothing."},
	"unavailable": {"warning", "插件当前不可用，快捷键未执行。", "The plugin is unavailable; the shortcut did nothing."},
	"locked":      {"warning", "MinCode 已锁定，快捷键未执行。", "MinCode is locked; the shortcut did nothing."},
	"startFailed": {"error", "插件启动失败，快捷键未执行。", "The plugin failed to start; the shortcut did nothing."},
	"busy":        {"warning", "插件仍在处理上一次操作。", "The plugin is still busy with the previous run."},
	"failed":      {"error", "快捷键执行失败。", "The shortcut failed."},
}

// reject posts a system notification for a press the host refused. Code
// ("globalShortcut.<reason>") lets the frontend localize it; the plugin name
// comes from the manifest. Repeated presses of one entry are throttled.
func (r *globalShortcutRegistry) reject(pluginID, pluginName, shortcutID, reason string) {
	text, ok := globalShortcutRejections[reason]
	if !ok || r.notify == nil {
		return
	}
	now := r.now()
	key := pluginID + "/" + shortcutID + "/" + reason
	r.mu.Lock()
	if last, seen := r.noticeAt[key]; seen && now.Sub(last) < globalShortcutNoticeInterval {
		r.mu.Unlock()
		return
	}
	r.noticeAt[key] = now
	for other, at := range r.noticeAt {
		if now.Sub(at) >= globalShortcutNoticeInterval {
			delete(r.noticeAt, other)
		}
	}
	r.mu.Unlock()
	if pluginName == "" {
		pluginName = pluginID
	}
	r.notify(pluginNotification{
		PluginID: pluginID, PluginName: cleanNotificationText(pluginName, notifyMaxTitleRunes),
		Title: text.zh + " " + text.en, Level: text.level, System: true, Code: "globalShortcut." + reason,
	})
}

// globalShortcutTrigger is the object the host adds to an action payload.
type globalShortcutTrigger struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// globalShortcutActionPayload is payload (a compact JSON object or nil) with
// {"trigger": {"type": "globalShortcut", "id": shortcutID}} added first. The
// manifest check reserves globalShortcutTriggerReserve bytes for it.
func globalShortcutActionPayload(payload json.RawMessage, shortcutID string) []byte {
	trigger, _ := json.Marshal(map[string]globalShortcutTrigger{globalShortcutTriggerKey: {Type: "globalShortcut", ID: shortcutID}})
	body := strings.TrimSpace(string(payload))
	if len(body) < 2 || body[0] != '{' {
		return trigger
	}
	rest := strings.TrimSpace(body[1:])
	if rest == "}" {
		return trigger
	}
	return []byte(string(trigger[:len(trigger)-1]) + "," + rest)
}

func globalShortcutRequestID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "gs-" + hex.EncodeToString(b[:])
}

// globalShortcutEntryValidator re-checks, under record.mu, that the entry is
// still declared and still runs actionID.
func globalShortcutEntryValidator(shortcutID, actionID string) func(*pluginRecord) error {
	return func(record *pluginRecord) error {
		entries, _ := parseGlobalShortcuts(record.manifest)
		for _, entry := range entries {
			if entry.ID == shortcutID && entry.Run.Type == "action" && entry.Run.Action == actionID {
				return nil
			}
		}
		return newContributionError(contributionErrNotFound, "global shortcut %q no longer runs action %q", shortcutID, actionID)
	}
}

// runGlobalShortcutAction admits the call (lazy activation included) and runs
// it, as a long task when the runtime negotiated tasks. run.reached reports
// that the plugin received the call; run.runtime and run.generation name the
// process generation that ran it. locked is checked again after admission, which
// may have waited for the plugin to start: a lock in between refuses the call
// (errGlobalShortcutLocked) before the plugin sees it.
func (manager *pluginManager) runGlobalShortcutAction(ctx context.Context, call contributedActionCall, locked func() bool) (globalShortcutRun, error) {
	notRun := globalShortcutRun{}
	bounded, cancel := context.WithTimeout(ctx, pluginStartupTimeout+globalShortcutTaskTimeout)
	defer cancel()
	admission, err := manager.admitContributedAction(bounded, call)
	if err != nil {
		return notRun, err
	}
	defer admission.Release()
	if locked == nil || locked() {
		return notRun, errGlobalShortcutLocked
	}
	runtimeHost := admission.Runtime
	ran := globalShortcutRun{reached: true, runtime: runtimeHost, generation: admission.Generation}
	runtimeHost.mu.Lock()
	tasks := runtimeHost.tasksSupported
	runtimeHost.mu.Unlock()
	if tasks {
		done := make(chan taskSnapshot, 1)
		owner := taskOwner{Generation: admission.Generation, CallID: call.RequestID, OnChange: func(snapshot taskSnapshot) {
			if snapshot.State != "running" {
				select {
				case done <- snapshot:
				default:
				}
			}
		}}
		if _, err := runtimeHost.startTask(admission.Ctx, owner, call.ActionID, string(call.Payload), globalShortcutTaskTimeout); err != nil {
			return notRun, fmt.Errorf("start task: %w", err)
		}
		admission.finish()
		select {
		case snapshot := <-done:
			if snapshot.State == "succeeded" {
				return ran, nil
			}
			return ran, fmt.Errorf("task %s: %s", snapshot.State, snapshot.Error)
		case <-admission.Ctx.Done():
			return ran, admission.classify(admission.Ctx.Err())
		}
	}
	invokeCtx, cancelInvoke := context.WithTimeout(admission.Ctx, pluginInvokeTimeout)
	defer cancelInvoke()
	_, err = runtimeHost.InvokeGeneration(invokeCtx, admission.Generation, call.ActionID, string(call.Payload))
	admission.finish()
	if err != nil {
		return ran, admission.classify(err)
	}
	return ran, nil
}

// ---- User gesture ----

// globalShortcutGesture is the press of an enabled global shortcut, minted
// only by the registry's dispatch for one call. admitContributedAction
// requires one on every globalShortcuts call (it stands in for the
// confirmation a write action gets on a page) and refuses one anywhere else.
// Its fields are unexported and the call field holding it is unexported, so
// no JSON request from a page, command or plugin can carry one.
type globalShortcutGesture struct {
	pluginID string
	entryID  string
	actionID string
	claimed  atomic.Bool
}

func newGlobalShortcutGesture(pluginID, entryID, actionID string) *globalShortcutGesture {
	return &globalShortcutGesture{pluginID: pluginID, entryID: entryID, actionID: actionID}
}

// claim consumes the gesture for exactly this plugin, entry and action.
func (gesture *globalShortcutGesture) claim(pluginID, entryID, actionID string) bool {
	return gesture != nil && gesture.pluginID == pluginID && gesture.entryID == entryID &&
		gesture.actionID == actionID && gesture.claimed.CompareAndSwap(false, true)
}

// admitGlobalShortcutGesture is admitContributedAction's gate.
func admitGlobalShortcutGesture(call contributedActionCall) error {
	if call.Point != globalShortcutPointKey && call.gesture == nil {
		return nil
	}
	if call.Point != globalShortcutPointKey || !call.gesture.claim(call.PluginID, call.EntryID, call.ActionID) {
		return newContributionError(contributionErrForbidden, "global shortcut actions run only once per press of an enabled shortcut")
	}
	return nil
}

// ---- Activation tokens ----

type activationTokenEntry struct {
	pluginID   string
	shortcutID string
	expires    time.Time
}

// activationTokenStore holds the single-use tokens issued after a global
// shortcut action ended; each lets its plugin raise its own tab view once
// within globalShortcutActivationTTL.
type activationTokenStore struct {
	mu     sync.Mutex
	now    func() time.Time
	tokens map[string]activationTokenEntry
}

var globalShortcutActivations = &activationTokenStore{now: time.Now, tokens: map[string]activationTokenEntry{}}

// setClock swaps the clock (tests); the returned func restores it.
func (store *activationTokenStore) setClock(now func() time.Time) func() {
	store.mu.Lock()
	previous := store.now
	store.now = now
	store.mu.Unlock()
	return func() {
		store.mu.Lock()
		store.now = previous
		store.mu.Unlock()
	}
}

// issue adds a token unless locked reports the app locked (nil: no check).
// The check runs under store.mu: locking sets the state before it revokes
// every token (AppLockChanged → revoke), so a concurrent lock either refuses
// this token or revokes it.
func (store *activationTokenStore) issue(pluginID, shortcutID string, locked func() bool) (string, time.Time, bool) {
	var b [16]byte
	_, _ = rand.Read(b[:])
	token := hex.EncodeToString(b[:])
	store.mu.Lock()
	defer store.mu.Unlock()
	if locked != nil && locked() {
		return "", time.Time{}, false
	}
	now := store.now()
	for key, entry := range store.tokens {
		if !now.Before(entry.expires) {
			delete(store.tokens, key)
		}
	}
	for len(store.tokens) >= globalShortcutActivationMax {
		oldest := ""
		for key, entry := range store.tokens {
			if oldest == "" || entry.expires.Before(store.tokens[oldest].expires) {
				oldest = key
			}
		}
		delete(store.tokens, oldest)
	}
	expires := now.Add(globalShortcutActivationTTL)
	store.tokens[token] = activationTokenEntry{pluginID: pluginID, shortcutID: shortcutID, expires: expires}
	return token, expires, true
}

// revokeToken drops one token (an undelivered one).
func (store *activationTokenStore) revokeToken(token string) {
	store.mu.Lock()
	delete(store.tokens, token)
	store.mu.Unlock()
}

func (store *activationTokenStore) lookup(pluginID, token string, consume bool) (string, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	entry, ok := store.tokens[token]
	if !ok || token == "" || entry.pluginID != pluginID {
		return "", false
	}
	if !store.now().Before(entry.expires) {
		delete(store.tokens, token)
		return "", false
	}
	if consume {
		delete(store.tokens, token)
	}
	return entry.shortcutID, true
}

func (store *activationTokenStore) revoke(pluginID string) {
	store.mu.Lock()
	defer store.mu.Unlock()
	for key, entry := range store.tokens {
		if pluginID == "" || entry.pluginID == pluginID {
			delete(store.tokens, key)
		}
	}
}

// held reports an unexpired token of pluginID.
func (store *activationTokenStore) held(pluginID string) bool {
	store.mu.Lock()
	defer store.mu.Unlock()
	now := store.now()
	for _, entry := range store.tokens {
		if entry.pluginID == pluginID && now.Before(entry.expires) {
			return true
		}
	}
	return false
}

// activationTokenHeld: pluginID still holds an unspent, unexpired activation
// token. Idle stop waits for it (pluginBusy), because stopping the process
// revokes its tokens.
func activationTokenHeld(pluginID string) bool { return globalShortcutActivations.held(pluginID) }

// issueActivationToken issues a single-use token for pluginID, valid for
// globalShortcutActivationTTL.
// It returns "" while the app is locked (appLocked, which fails closed).
func issueActivationToken(pluginID, shortcutID string) string {
	token, _, _ := globalShortcutActivations.issue(pluginID, shortcutID, appLocked)
	return token
}

// consumeActivationToken uses token for pluginID: it must exist, belong to
// pluginID and be unexpired. It returns the shortcut ID it was issued for.
func consumeActivationToken(pluginID, token string) (string, bool) {
	return globalShortcutActivations.lookup(pluginID, token, true)
}

// activationTokenValid checks token without consuming it.
func activationTokenValid(pluginID, token string) bool {
	_, ok := globalShortcutActivations.lookup(pluginID, token, false)
	return ok
}

// revokeActivationTokens drops pluginID's tokens ("" drops all).
func revokeActivationTokens(pluginID string) { globalShortcutActivations.revoke(pluginID) }

// ---- Wiring ----

type globalShortcutArgs struct {
	PluginID string   `json:"pluginId"`
	ID       string   `json:"id"`
	Enabled  *bool    `json:"enabled"`
	Key      *string  `json:"key"`
	Keys     []string `json:"keys"`
}

// globalShortcutsContributionsChanged re-evaluates after any change that can
// add or remove entries (state, enable, scope, workspace, install).
func (manager *pluginManager) globalShortcutsContributionsChanged(points []string) {
	if len(points) > 0 && !slices.Contains(points, globalShortcutPointKey) {
		return
	}
	if registry := manager.shortcuts.Load(); registry != nil {
		registry.requestReconcile()
	}
}

func init() {
	core.Register("plugin_global_shortcuts_list", func(_, _ string) (string, error) {
		return core.EncodeResult(host().globalShortcutRegistry().list())
	})
	core.Register("plugin_global_shortcuts_set", func(_, argsJSON string) (string, error) {
		var args globalShortcutArgs
		if err := core.DecodeArgs(argsJSON, &args); err != nil {
			return "", err
		}
		status, err := host().globalShortcutRegistry().set(args.PluginID, args.ID, args.Enabled, args.Key)
		if err != nil {
			return "", err
		}
		return core.EncodeResult(status)
	})
	core.Register("plugin_global_shortcuts_reset", func(_, argsJSON string) (string, error) {
		var args globalShortcutArgs
		if err := core.DecodeArgs(argsJSON, &args); err != nil {
			return "", err
		}
		status, err := host().globalShortcutRegistry().reset(args.PluginID, args.ID)
		if err != nil {
			return "", err
		}
		return core.EncodeResult(status)
	})
	core.Register("plugin_global_shortcuts_set_inapp_keys", func(_, argsJSON string) (string, error) {
		var args globalShortcutArgs
		if err := core.DecodeArgs(argsJSON, &args); err != nil {
			return "", err
		}
		if err := host().globalShortcutRegistry().setInAppKeys(args.Keys); err != nil {
			return "", err
		}
		return core.EncodeResult(nil)
	})
	desktop.OnHostShortcutChange(func(pending string) {
		if registry := activeGlobalShortcutRegistry.Load(); registry != nil {
			registry.hostShortcutChanging(pending)
		}
	})
	// Stop, crash, disable, reload, uninstall: tokens die with the runtime.
	registerRecordLifecycleHook(func(manager *pluginManager, record *pluginRecord) {
		revokeActivationTokens(record.id)
		if registry := manager.shortcuts.Load(); registry != nil {
			registry.requestReconcile()
		}
	})
	registerRecordRemovalHook(func(manager *pluginManager, pluginID string, reason removalReason) {
		revokeActivationTokens(pluginID)
		if reason != removalReasonUninstall {
			return // Clear Plugin Data keeps host-owned preferences.
		}
		if registry := manager.shortcuts.Load(); registry != nil {
			registry.forgetPlugin(pluginID)
			return
		}
		deleteGlobalShortcutSettings(pluginID)
	})
}
