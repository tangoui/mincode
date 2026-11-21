package lsp

import (
	"errors"
	"fmt"
	"os"
	"time"
)

// jdtls keeps the OSGi bundles it installed (initializationOptions.bundles)
// in its OSGi configuration area. The Homebrew/upstream launcher passes no
// -configuration, so that area is the shared install directory
// (<jdtls>/configuration): a bundle loaded once would keep loading in every
// later jdtls — after the user disabled, untrusted or uninstalled the plugin
// — and a changed jar at the same path would be re-read before the user
// re-confirmed it. The built-in jdtls therefore always gets a MinCode-owned,
// per-session -configuration directory (cascading to the launcher's
// read-only shared config_* area), removed when the session's process ends.
// The -data workspace is left to the launcher: it holds project metadata
// only, no bundle state.

// newJdtlsConfigDir creates an empty private per-session configuration directory
// directly under the system temp directory. A host terminated abnormally may
// leave this random directory behind; it is intentionally not swept because
// there is no safe way to identify an orphaned JVM configuration.
func newJdtlsConfigDir() (string, error) {
	dir, err := os.MkdirTemp(os.TempDir(), "mincode-jdtls-config-")
	if err != nil {
		return "", fmt.Errorf("create jdtls configuration directory: %w", err)
	}
	return dir, nil
}

// jdtlsConfigArguments returns the launcher arguments selecting dir as the
// OSGi configuration area.
func jdtlsConfigArguments(dir string) []string {
	return []string{"-configuration", dir}
}

// removeConfigDir deletes the session's per-session jdtls configuration
// directory (once). jdtls may still be flushing files right after the kill,
// so removal retries briefly.
func (session *session) removeConfigDir() {
	session.configOnce.Do(func() {
		dir := session.configDir
		if dir == "" {
			return
		}
		var err error
		for attempt := 0; attempt < 5; attempt++ {
			if err = os.RemoveAll(dir); err == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			session.log("error category=jdtls-configuration-cleanup-failed")
		}
	})
}
