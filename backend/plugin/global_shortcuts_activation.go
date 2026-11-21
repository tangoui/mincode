package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// Activation token delivery (docs/architecture/plugin-system.md, "Global
// shortcuts"). After a global-shortcut action has run, whether it succeeded
// or failed, the host issues a token (refused while the app is locked) and
// sends the process generation that ran the action
//
//	plugin.globalShortcut.activation {id, token, expiresAt}
//
// as an acknowledged request: id is the shortcut entry, token the single-use
// activation token, expiresAt its expiry in Unix milliseconds. The plugin
// may spend the token once on host.view.open {viewId, raise: true,
// activation: token} to bring the main window forward on its own tab view.
// Delivery waits at most 3 s for the acknowledgement and never for the
// handler. A token that was not delivered (the generation stopped or
// changed, the runtime has no handler and answers -32601, or any other
// failure) is revoked at once. The token is never logged.
const (
	globalShortcutActivationMethod  = "plugin.globalShortcut.activation"
	globalShortcutActivationTimeout = 3 * time.Second
	// globalShortcutActivationInvalid is the host.view.open error code
	// (error.data.code) for a raise without a valid activation token.
	globalShortcutActivationInvalid = "activation_invalid"
	// globalShortcutRaiseLocked is the host.view.open error code for a raise
	// while the app is locked.
	globalShortcutRaiseLocked = "locked"
)

// globalShortcutRun is the outcome of an action press: whether the plugin
// received the call, and the runtime and process generation that ran it.
type globalShortcutRun struct {
	reached    bool
	runtime    *processRuntime
	generation uint64
}

// sendGlobalShortcutActivation delivers token to the generation in run and
// reports whether the plugin acknowledged it. It runs on the registry's
// dispatch goroutine, holding no lock.
func (manager *pluginManager) sendGlobalShortcutActivation(pluginID, shortcutID, token string, expiresAt time.Time, run globalShortcutRun) bool {
	if run.runtime == nil {
		return false
	}
	params, _ := json.Marshal(map[string]any{"id": shortcutID, "token": token, "expiresAt": expiresAt.UnixMilli()})
	ctx, cancel := context.WithTimeout(context.Background(), globalShortcutActivationTimeout)
	defer cancel()
	_, err := run.runtime.callGeneration(ctx, run.generation, globalShortcutActivationMethod, params)
	if err == nil {
		return true
	}
	if !globalShortcutActivationIgnorable(err) {
		if record, lookupErr := manager.record(pluginID); lookupErr == nil {
			record.appendLog("warn", "global shortcut "+shortcutID+": activation delivery failed: "+err.Error())
		}
	}
	return false
}

// globalShortcutActivationIgnorable reports a runtime that does not handle
// activations (method not found).
func globalShortcutActivationIgnorable(err error) bool {
	var rpcErr *processRPCError
	return errors.As(err, &rpcErr) && rpcErr.Code == -32601
}
