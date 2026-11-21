package cloudcompanion

import (
	"encoding/json"
	"testing"

	"mincode/app/backend/db"
)

func TestCloudDatabaseConnectionSupportedNeedsDriverGrant(t *testing.T) {
	granted := map[string]bool{"replaces-granted": true, "plugin-granted": true}
	previous := driverReleaseAllowed
	t.Cleanup(func() { driverReleaseAllowed = previous })
	driverReleaseAllowed = func(_ db.ExternalDriverRef, connectionID string) bool { return granted[connectionID] }
	var connections []storedDatabaseConnection
	raw := `[
		{"id":"builtin","name":"MySQL","dbType":"mysql","host":"127.0.0.1","port":3306},
		{"id":"null-driver","name":"Null","dbType":"mysql","host":"127.0.0.1","port":3306,"driver":null},
		{"id":"replaces","name":"Replaces","dbType":"mysql","host":"127.0.0.1","port":3306,"driver":{"pluginId":"com.acme.db","driverId":"acme"}},
		{"id":"replaces-granted","name":"Granted","dbType":"mysql","host":"127.0.0.1","port":3306,"driver":{"pluginId":"com.acme.db","driverId":"acme"}},
		{"id":"plugin","name":"Plugin","dbType":"plugin","host":"127.0.0.1","port":1},
		{"id":"plugin-granted","name":"Plugin","dbType":"plugin","host":"127.0.0.1","port":1,"driver":{"pluginId":"com.acme.db","driverId":"acme"}},
		{"id":"malformed","name":"Bad","dbType":"mysql","host":"127.0.0.1","port":1,"driver":{"pluginId":"","driverId":"acme"}},
		{"id":"tunnel","name":"Tunnel","dbType":"mysql","host":"127.0.0.1","port":3306,"tunnel":{"type":"ssh"}}
	]`
	if err := json.Unmarshal([]byte(raw), &connections); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := map[string]bool{
		"builtin":          true,
		"null-driver":      true,
		"replaces":         false,
		"replaces-granted": true,
		"plugin":           false,
		"plugin-granted":   true,
		"malformed":        false,
		"tunnel":           false,
	}
	for _, connection := range connections {
		if got := cloudDatabaseConnectionSupported(connection); got != want[connection.ID] {
			t.Errorf("%s: supported = %v, want %v", connection.ID, got, want[connection.ID])
		}
	}
}
