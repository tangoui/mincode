//go:build production

package sqlsafety

// appDataDirectory is the per-build application data directory under
// core.UserDataDir(); it mirrors backend/plugin/backup_namespace_production.go.
const appDataDirectory = "com.tangoui.mincode"
