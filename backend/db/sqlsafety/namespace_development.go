//go:build !production

package sqlsafety

// appDataDirectory is the per-build application data directory under
// core.UserDataDir(); it mirrors backend/plugin/backup_namespace_development.go.
const appDataDirectory = "com.mincode.desktop.dev"
