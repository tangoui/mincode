package plugin

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// realStartupClearServiceExchange is the production hook; every other test in
// the package runs with a no-op so manager startup never clears the real
// user data directory's exchange.
var realStartupClearServiceExchange = startupClearServiceExchange

func init() { startupClearServiceExchange = func() {} }

// TestStartupClearsServiceExchange: manager startup clears what a previous
// run left in the exchange (no call can be running yet), using the same once
// as first use, so a later first use does not clear again.
func TestStartupClearsServiceExchange(t *testing.T) {
	for name, start := range map[string]func(*pluginManager) error{
		"Startup": func(manager *pluginManager) error { return manager.Startup(context.Background()) },
		"StartupInBackground": func(manager *pluginManager) error {
			return manager.StartupInBackground(0, nil)
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(useTemporaryPluginData(t), "exchange")
			previousFiles, previousHook := serviceFiles, startupClearServiceExchange
			serviceFiles = newServiceExchange(root)
			startupClearServiceExchange = realStartupClearServiceExchange
			t.Cleanup(func() { serviceFiles, startupClearServiceExchange = previousFiles, previousHook })
			leftover := filepath.Join(root, "calls", "old", "input.bin")
			if err := os.MkdirAll(filepath.Dir(leftover), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(leftover, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			manager := &pluginManager{records: map[string]*pluginRecord{}, uiHost: newPluginUIHost()}
			t.Cleanup(func() { manager.Shutdown(context.Background()) })
			if err := start(manager); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(leftover); !os.IsNotExist(err) {
				t.Fatalf("leftover survived startup: %v", err)
			}
			dir, err := serviceFiles.ensureDir("uploads")
			if err != nil {
				t.Fatal(err)
			}
			kept := filepath.Join(dir, "new.bin")
			if err := os.WriteFile(kept, []byte("y"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := serviceFiles.ensureDir("uploads"); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(kept); err != nil {
				t.Fatalf("first use cleared again: %v", err)
			}
		})
	}
}
