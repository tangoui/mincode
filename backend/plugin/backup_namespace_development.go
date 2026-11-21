//go:build !production

package plugin

// appDataDirectory is the per-build application data directory under
// core.UserDataDir(); it mirrors backend/storage/storage_namespace_development.go.
const appDataDirectory = "com.mincode.desktop.dev"
