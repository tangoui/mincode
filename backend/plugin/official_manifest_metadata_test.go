package plugin

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// validateManifest also requires the built runtime entry, which the source tree
// does not contain, so the install-time metadata limits are checked here directly.
func TestOfficialManifestsFitInstallLimits(t *testing.T) {
	for _, path := range officialManifestPaths(t) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var manifest Manifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			t.Fatal(err)
		}
		if manifest.Name == "" || len(manifest.Name) > 120 {
			t.Errorf("%s: name must contain 1-120 bytes, has %d", manifest.ID, len(manifest.Name))
		}
		if len(manifest.Description) > maxPluginDescriptionBytes {
			t.Errorf("%s: description is %d bytes; the installer rejects more than %d", manifest.ID, len(manifest.Description), maxPluginDescriptionBytes)
		}
		if len(manifest.Author) > 120 {
			t.Errorf("%s: author is %d bytes; the installer rejects more than 120", manifest.ID, len(manifest.Author))
		}
		if icon := strings.TrimSpace(manifest.Icon); icon != "" {
			if _, allowed := pluginViewIcons[icon]; !allowed {
				t.Errorf("%s: icon %q is not supported; the installer accepts only the pluginViewIcons names", manifest.ID, icon)
			}
		}
		if !pluginVersionPattern.MatchString(manifest.Version) {
			t.Errorf("%s: version %q is not semantic", manifest.ID, manifest.Version)
		}
		for _, action := range manifest.Contributes.Actions {
			if action.MaxPayloadBytes < 0 || action.MaxPayloadBytes > 256*1024 {
				t.Errorf("%s/%s: maxPayloadBytes %d is outside the installer's 1..262144 range", manifest.ID, action.ID, action.MaxPayloadBytes)
			}
		}
	}
}
