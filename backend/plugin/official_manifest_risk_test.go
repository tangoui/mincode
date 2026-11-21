package plugin

import (
	"encoding/json"
	"os"
	"testing"
)

// The host rejects the whole manifest at install time when any risk is not
// read, write or dangerous; network access is declared as a permission instead.
func TestOfficialManifestRisksAreAccepted(t *testing.T) {
	for _, path := range officialManifestPaths(t) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var manifest Manifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for _, action := range manifest.Contributes.Actions {
			if _, err := normalizeRisk(action.Risk); err != nil {
				t.Errorf("%s action %q: %v (got %q)", manifest.ID, action.ID, err, action.Risk)
			}
		}
		for _, command := range manifest.Contributes.Commands {
			if _, err := normalizeRisk(command.Risk); err != nil {
				t.Errorf("%s command %q: %v (got %q)", manifest.ID, command.ID, err, command.Risk)
			}
		}
	}
}
