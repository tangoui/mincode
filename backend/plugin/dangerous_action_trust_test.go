package plugin

import (
	"encoding/json"
	"testing"
)

func TestPluginDangerousActionTrustMatchesInstalledVersion(t *testing.T) {
	trust, err := json.Marshal(pluginDangerousActionTrust{Version: pluginDangerousActionTrustVersion, PluginVersion: "1.2.3"})
	if err != nil {
		t.Fatal(err)
	}
	if !pluginDangerousActionTrustMatches(trust, "1.2.3") {
		t.Fatal("trust for the installed version should be accepted")
	}
	if pluginDangerousActionTrustMatches(trust, "1.2.4") {
		t.Fatal("trust must not carry over to an upgraded version")
	}
	if pluginDangerousActionTrustMatches(json.RawMessage(`{"version":99,"pluginVersion":"1.2.3"}`), "1.2.3") {
		t.Fatal("unknown trust formats must fail closed")
	}
	if pluginDangerousActionTrustMatches(json.RawMessage(`not-json`), "1.2.3") {
		t.Fatal("malformed trust records must fail closed")
	}
}
