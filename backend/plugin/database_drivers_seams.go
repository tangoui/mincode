package plugin

import (
	"bytes"
	"fmt"

	"mincode/app/plugins/api"
)

// Phase E T0 seams for contributes.databaseDrivers. The point itself (Decode,
// assets, lifecycle, db.* dispatch) is registered by later tasks:
//   - T1: backend/db ExternalDriverProvider + session registry (db_connect routing).
//   - T2: databaseDrivers.go point file, db.* JSON-RPC client, log redactor
//     registration for handed-out credentials, removal/lifecycle hooks.
//   - T3: marketplace/CLI entry checks.  T4a/T4b: frontend consumer + UI.
//   - T5: official sample driver.  T7: flip the contract status to enabled.

func init() {
	// Driver connections are bound to saved connections, which are global;
	// a workspace-scoped driver would silently break those bindings when the
	// workspace changes, so driver plugins are always global.
	registerScopeGuard(func(manifest *Manifest) string {
		if manifestDeclaresDatabaseDrivers(manifest) {
			return fmt.Sprintf("plugin %q provides database drivers and is always global", manifest.ID)
		}
		return ""
	})
}

// manifestDeclaresDatabaseDrivers reports a plugin that may act as a
// database driver: it declares the database.driver capability or a non-empty
// contributes.databaseDrivers value.
func manifestDeclaresDatabaseDrivers(manifest *Manifest) bool {
	if manifest == nil {
		return false
	}
	for _, capability := range manifest.Capabilities {
		if capability == api.CapabilityDatabaseDriver {
			return true
		}
	}
	raw := bytes.TrimSpace(manifest.Contributes.Points["databaseDrivers"])
	return len(raw) > 0 && !bytes.Equal(raw, []byte("null"))
}
