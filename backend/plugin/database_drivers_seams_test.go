package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"mincode/app/plugins/api"
)

func TestOfferedRuntimeFeaturesRespectCapabilities(t *testing.T) {
	plain := offeredRuntimeFeatures(map[api.Capability]struct{}{})
	if plain["tasks"] != 1 || plain["configuration"] != 1 {
		t.Fatalf("base offer = %v", plain)
	}
	if _, ok := plain["databaseDrivers"]; ok {
		t.Fatal("databaseDrivers offered without database.driver")
	}
	driver := offeredRuntimeFeatures(map[api.Capability]struct{}{api.CapabilityDatabaseDriver: {}})
	if driver["databaseDrivers"] != 1 {
		t.Fatalf("driver offer = %v", driver)
	}
	describe := (&processRuntime{capabilities: map[api.Capability]struct{}{}}).describeAPI().(map[string]any)
	if features := describe["features"].(map[string]int); features["tasks"] != 1 || features["databaseDrivers"] != 0 {
		t.Fatalf("describe features = %v", features)
	}
}

func TestNegotiateRuntimeFeatures(t *testing.T) {
	offer := map[string]int{"tasks": 1, "configuration": 1, "databaseDrivers": 1}
	cases := []struct {
		name, result string
		want         []string
		details      bool
		problems     int
	}{
		{"malformed", `nope`, nil, false, 0},
		{"no features", `{}`, nil, false, 0},
		{"version mismatch", `{"features":{"tasks":2,"configuration":"1"}}`, nil, false, 0},
		{"unoffered ignored", `{"features":{"tasks":1,"extra":1}}`, []string{"tasks"}, false, 0},
		{"details missing", `{"features":{"databaseDrivers":1}}`, nil, false, 1},
		{"details not object", `{"features":{"databaseDrivers":1},"databaseDrivers":[1]}`, nil, false, 1},
		{"details kept", `{"features":{"tasks":1,"databaseDrivers":1},"databaseDrivers":{"protocol":1,"drivers":[]}}`, []string{"tasks", "databaseDrivers"}, true, 0},
		{"details oversized", `{"features":{"databaseDrivers":1},"databaseDrivers":{"x":"` + strings.Repeat("a", runtimeFeatureDetailsMaxBytes) + `"}}`, nil, false, 1},
	}
	for _, tc := range cases {
		got := negotiateRuntimeFeatures(offer, json.RawMessage(tc.result))
		if len(got.versions) != len(tc.want) {
			t.Fatalf("%s: versions = %v", tc.name, got.versions)
		}
		for _, name := range tc.want {
			if !got.has(name) {
				t.Fatalf("%s: %s not negotiated", tc.name, name)
			}
		}
		if _, ok := got.details["databaseDrivers"]; ok != tc.details {
			t.Fatalf("%s: details = %v", tc.name, got.details)
		}
		if len(got.problems) != tc.problems {
			t.Fatalf("%s: problems = %v", tc.name, got.problems)
		}
	}
	// A feature that was not offered (missing capability) never negotiates.
	if got := negotiateRuntimeFeatures(map[string]int{"tasks": 1}, json.RawMessage(`{"features":{"databaseDrivers":1},"databaseDrivers":{}}`)); got.has("databaseDrivers") {
		t.Fatal("unoffered databaseDrivers negotiated")
	}
}

func TestRuntimeFeatureAccessorsRequireRunningGeneration(t *testing.T) {
	runtimeHost := &processRuntime{generation: 3}
	runtimeHost.features = negotiateRuntimeFeatures(map[string]int{"databaseDrivers": 1}, json.RawMessage(`{"features":{"databaseDrivers":1},"databaseDrivers":{"protocol":1}}`))
	if runtimeHost.featureNegotiated("databaseDrivers") {
		t.Fatal("feature reported without a running process")
	}
	if _, _, ok := runtimeHost.featureDetails("databaseDrivers"); ok {
		t.Fatal("details returned without a running process")
	}
}

var redactorTestOnce sync.Once

func TestLogRedactorAppliesToRecordLogsAndErrors(t *testing.T) {
	redactorTestOnce.Do(func() {
		registerLogRedactor(func(pluginID, message string) string {
			if pluginID != "redact-test" {
				return message
			}
			return strings.ReplaceAll(message, "s3cr3t-value", "[redacted]")
		})
	})
	record := &pluginRecord{id: "redact-test"}
	record.appendLog("error", "auth failed for s3cr3t-value")
	other := &pluginRecord{id: "other"}
	other.appendLog("error", "auth failed for s3cr3t-value")
	if got := record.logs[0].Message; strings.Contains(got, "s3cr3t-value") || !strings.Contains(got, "[redacted]") {
		t.Fatalf("log not redacted: %q", got)
	}
	if got := other.logs[0].Message; !strings.Contains(got, "s3cr3t-value") {
		t.Fatalf("redactor leaked across plugins: %q", got)
	}
	if got := redactPluginMessage("redact-test", "x s3cr3t-value"); got != "x [redacted]" {
		t.Fatalf("redactPluginMessage = %q", got)
	}
}

func TestDatabaseDriverPluginsAreAlwaysGlobal(t *testing.T) {
	if manifestGlobalOnlyReason(&Manifest{ID: "plain"}) != "" {
		t.Fatal("plain plugin forced global")
	}
	byCapability := &Manifest{ID: "driver", Capabilities: []api.Capability{api.CapabilityDatabaseDriver}}
	if reason := manifestGlobalOnlyReason(byCapability); !strings.Contains(reason, "database drivers") {
		t.Fatalf("capability guard reason = %q", reason)
	}
	byPoint := &Manifest{ID: "driver"}
	byPoint.Contributes.Points = map[string]json.RawMessage{"databaseDrivers": json.RawMessage(` [] `)}
	if manifestGlobalOnlyReason(byPoint) == "" {
		t.Fatal("databaseDrivers point not guarded")
	}
	byPoint.Contributes.Points["databaseDrivers"] = json.RawMessage(`null`)
	if manifestGlobalOnlyReason(byPoint) != "" {
		t.Fatal("null databaseDrivers guarded")
	}

	useTemporaryPluginData(t)
	manager, record := testManagerWithPlugin(&testLifecyclePlugin{}, false)
	record.manifest = byCapability
	err := manager.SetScope(context.Background(), record.id, pluginScopeWorkspace, []string{"ws-a"})
	if err == nil || !strings.Contains(err.Error(), "always global") {
		t.Fatalf("workspace scope accepted for a driver plugin: %v", err)
	}
	if record.scope != "" && record.scope != pluginScopeGlobal {
		t.Fatalf("scope changed to %q", record.scope)
	}
	if err := manager.SetScope(context.Background(), record.id, pluginScopeGlobal, nil); err != nil {
		t.Fatal(err)
	}
}

// The golden transcripts are shared with the T2 proxy and T3 SDK tests; here
// they pin the plugin.initialize offer and negotiation outcome.
func TestDatabaseDriverTranscriptNegotiation(t *testing.T) {
	driverCapabilities := map[api.Capability]struct{}{api.CapabilityDatabaseDriver: {}, api.CapabilityNetwork: {}}
	offer := offeredRuntimeFeatures(driverCapabilities)
	for file, want := range map[string]bool{"basic-session.jsonl": true, "negotiation-missing.jsonl": false, "v11-negotiation.jsonl": true, "v2-columnar.jsonl": true, "v2-columnar-declined.jsonl": true} {
		data, err := os.ReadFile(filepath.Join("..", "..", "tools", "verify", "fixtures", "database-driver-protocol", file))
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		var initialize, reply struct {
			Message struct {
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
				Result json.RawMessage `json:"result"`
			} `json:"message"`
		}
		if len(lines) < 3 || json.Unmarshal([]byte(lines[1]), &initialize) != nil || json.Unmarshal([]byte(lines[2]), &reply) != nil || initialize.Message.Method != "plugin.initialize" {
			t.Fatalf("%s: malformed transcript", file)
		}
		var params struct {
			Features map[string]int `json:"features"`
		}
		if err := json.Unmarshal(initialize.Message.Params, &params); err != nil || len(params.Features) != len(offer) {
			t.Fatalf("%s: offer %v, host offers %v", file, params.Features, offer)
		}
		for name, version := range offer {
			if params.Features[name] != version {
				t.Fatalf("%s: offer %v, host offers %v", file, params.Features, offer)
			}
		}
		got := negotiateRuntimeFeatures(offer, reply.Message.Result)
		if got.has("databaseDrivers") != want {
			t.Fatalf("%s: databaseDrivers negotiated = %v", file, got.has("databaseDrivers"))
		}
		if want && !strings.Contains(string(got.details["databaseDrivers"]), `"protocol":1`) {
			t.Fatalf("%s: details = %s", file, got.details["databaseDrivers"])
		}
		// Only the 1.1 (and v2 draft) transcripts raise the page budget and
		// the line cap.
		wantLarge := file == "v11-negotiation.jsonl" || strings.HasPrefix(file, "v2-")
		if limits := negotiatedDatabaseDriverPageLimits(got.details["databaseDrivers"]); limits.large() != wantLarge {
			t.Fatalf("%s: page limits %+v", file, limits)
		}
		if large := got.maxMessage() == pluginRPCMaxLargeReply; large != wantLarge {
			t.Fatalf("%s: line cap %d", file, got.maxMessage())
		}
	}
}
