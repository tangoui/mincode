package plugin

import (
	"fmt"
	"sync"
)

// Cross-cutting seams for contribution points that hand plugins sensitive
// material (databaseDrivers). Like the hooks in contributions.go they are
// registered from init() and never reference a specific point here.

var (
	logRedactorsMu sync.RWMutex
	logRedactors   []func(pluginID, message string) string
)

// registerLogRedactor adds a filter applied to every plugin log line
// (stderr, host.log, runtime/host errors) before it is stored, and to the
// record's lastError. A point that gives a plugin secrets registers one that
// replaces the values it handed out with a fixed marker.
//
// Redactors run with record.mu held: they must be fast, must not block and
// must not lock any plugin record. Call from init().
func registerLogRedactor(redact func(pluginID, message string) string) {
	if redact == nil {
		panic("nil log redactor")
	}
	logRedactorsMu.Lock()
	defer logRedactorsMu.Unlock()
	logRedactors = append(logRedactors, redact)
}

// redactPluginMessage applies every registered redactor. Points use it for
// plugin-originated text that does not pass through the record log, such as
// RPC error messages and notices shown in the UI.
func redactPluginMessage(pluginID, message string) string {
	logRedactorsMu.RLock()
	redactors := logRedactors
	logRedactorsMu.RUnlock()
	for _, redact := range redactors {
		message = redact(pluginID, message)
	}
	return message
}

var (
	scopeGuardsMu sync.RWMutex
	scopeGuards   []func(manifest *Manifest) string
)

// registerScopeGuard adds a rule forcing some plugins to the global scope.
// The guard returns a non-empty reason when the manifest must stay global;
// the host then ignores a persisted workspace scope at registration and
// upgrade and rejects SetScope to a workspace scope with that reason.
// Guards must be pure functions of the manifest. Call from init().
func registerScopeGuard(guard func(manifest *Manifest) string) {
	if guard == nil {
		panic("nil scope guard")
	}
	scopeGuardsMu.Lock()
	defer scopeGuardsMu.Unlock()
	scopeGuards = append(scopeGuards, guard)
}

// manifestGlobalOnlyReason reports why a manifest may never be
// workspace-scoped, or "" when it may. Declarative packages are always global.
func manifestGlobalOnlyReason(manifest *Manifest) string {
	if manifest == nil {
		return ""
	}
	if manifest.isDeclarative() {
		return fmt.Sprintf("plugin %q is declarative and always global", manifest.ID)
	}
	scopeGuardsMu.RLock()
	guards := scopeGuards
	scopeGuardsMu.RUnlock()
	for _, guard := range guards {
		if reason := guard(manifest); reason != "" {
			return reason
		}
	}
	return ""
}
