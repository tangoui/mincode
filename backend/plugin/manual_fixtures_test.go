package plugin

import (
	"path/filepath"
	"testing"
)

// manualFixturePlugins are the plugins the desktop checklists in
// docs/verification/ install by hand (tools/verify/fixtures/manual).
var manualFixturePlugins = []string{"acp-agents-demo", "codelens-lab"}

func TestManualFixturePluginsPassInstallInspection(t *testing.T) {
	for _, name := range manualFixturePlugins {
		dir := filepath.Join("..", "..", "tools", "verify", "fixtures", "manual", name)
		if _, err := inspectInstallDirectory(dir); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
