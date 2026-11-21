package plugin

import (
	"encoding/json"
	"io/fs"
	"mincode/app/plugins/api"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestAIParameterSchema(t *testing.T) {
	var schema map[string]any
	json.Unmarshal([]byte(`{"type":"object","properties":{"url":{"type":"string"}},"required":["url"],"additionalProperties":false}`), &schema)
	for _, tc := range []struct {
		payload string
		valid   bool
	}{{`{"url":"https://example.com"}`, true}, {`{}`, false}, {`{"url":12}`, false}, {`{"url":"a","shell":"rm"}`, false}, {`[]`, false}} {
		var value any
		json.Unmarshal([]byte(tc.payload), &value)
		if err := validateAIValue(schema, value, "payload"); (err == nil) != tc.valid {
			t.Errorf("%s: %v", tc.payload, err)
		}
	}
	schema["$ref"] = "remote"
	if validateAIValue(schema, map[string]any{"url": "a"}, "payload") == nil {
		t.Fatal("unsupported schema accepted")
	}
}

func TestAIParameterSchemaBounds(t *testing.T) {
	var schema map[string]any
	json.Unmarshal([]byte(`{"type":"object","properties":{"rows":{"type":"integer","minimum":1,"maximum":10},"name":{"type":"string","minLength":2,"maxLength":3}},"additionalProperties":false}`), &schema)
	for _, tc := range []struct {
		payload string
		valid   bool
	}{
		{`{"rows":1,"name":"ab"}`, true},
		{`{"rows":10,"name":"表格名"}`, true},
		{`{"rows":0}`, false},
		{`{"rows":11}`, false},
		{`{"name":"a"}`, false},
		{`{"name":"abcd"}`, false},
	} {
		var value any
		json.Unmarshal([]byte(tc.payload), &value)
		if err := validateAIValue(schema, value, "payload"); (err == nil) != tc.valid {
			t.Errorf("%s: %v", tc.payload, err)
		}
	}
	schema = nil
	json.Unmarshal([]byte(`{"type":"integer","maximum":"10"}`), &schema)
	if validateAIValue(schema, 5.0, "payload") == nil {
		t.Fatal("non-numeric bound accepted")
	}
}

func TestAIApprovalRejectsAndConsumes(t *testing.T) {
	aiTickets.Lock()
	aiTickets.values["test-deny"] = aiPluginTicket{Expires: time.Now().Add(time.Minute)}
	aiTickets.values["test-expired"] = aiPluginTicket{Expires: time.Now().Add(-time.Minute)}
	aiTickets.Unlock()
	for _, args := range []string{`{"ticket":"missing","approved":true}`, `{"ticket":"test-deny","approved":false}`, `{"ticket":"test-deny","approved":true}`, `{"ticket":"test-expired","approved":true}`} {
		if _, err := pluginAIExecuteHandler("", args); err == nil {
			t.Fatalf("accepted %s", args)
		}
	}
}

func officialManifestPaths(t *testing.T) []string {
	t.Helper()
	const root = "../../official-plugins"
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		packageRoot := filepath.Join(root, entry.Name())
		if _, err := os.Stat(filepath.Join(packageRoot, manifestFilename)); os.IsNotExist(err) {
			continue
		} else if err != nil {
			t.Fatal(err)
		}
		name, err := selectManifestFilename(func(name string) (fs.FileInfo, error) {
			return os.Lstat(filepath.Join(packageRoot, name))
		})
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, filepath.Join(packageRoot, name))
	}
	return paths
}

// Validate the shipped contracts without running plugins or accessing user data.
func TestOfficialAIExamplesMatchSchemas(t *testing.T) {
	paths := officialManifestPaths(t)
	count := 0
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var manifest Manifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			t.Fatal(err)
		}
		for _, action := range manifest.Contributes.Actions {
			if !action.AICallable {
				continue
			}
			count++
			if action.Description == "" || len(action.Examples) == 0 || action.InputSchema == nil {
				t.Fatalf("%s/%s lacks documentation", manifest.ID, action.ID)
			}
			for _, example := range action.Examples {
				if err := validateAIValue(action.InputSchema, example, "payload"); err != nil {
					t.Fatalf("%s/%s: %v", manifest.ID, action.ID, err)
				}
				extra := make(map[string]any)
				for k, v := range example {
					extra[k] = v
				}
				extra["undeclaredArgument"] = true
				if validateAIValue(action.InputSchema, extra, "payload") == nil {
					t.Fatalf("%s/%s accepts unknown arguments", manifest.ID, action.ID)
				}
			}
		}
	}
	if count == 0 {
		t.Fatal("no AI action contracts discovered")
	}
}

// AI callers reach remote systems through saved profiles whose credentials the
// plugin reads from host.secrets; no AI-callable schema may ask for a secret.
func TestOfficialAISchemasCarryNoSecrets(t *testing.T) {
	secretName := regexp.MustCompile(`(?i)password|passwd|secret|token|apikey|privatekey|keypem|credential`)
	var walk func(plugin, action, path string, schema map[string]any)
	walk = func(plugin, action, path string, schema map[string]any) {
		if properties, ok := schema["properties"].(map[string]any); ok {
			for name, child := range properties {
				if secretName.MatchString(name) {
					t.Errorf("%s/%s: AI schema property %s%s looks like a credential", plugin, action, path, name)
				}
				if c, ok := child.(map[string]any); ok {
					walk(plugin, action, path+name+".", c)
				}
			}
		}
		if items, ok := schema["items"].(map[string]any); ok {
			walk(plugin, action, path+"[].", items)
		}
	}
	for _, path := range officialManifestPaths(t) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var manifest Manifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			t.Fatal(err)
		}
		for _, action := range manifest.Contributes.Actions {
			if action.AICallable && action.InputSchema != nil {
				walk(manifest.ID, action.ID, "", action.InputSchema)
			}
		}
	}
}

func TestAIConfirmationEnumAndContractFingerprint(t *testing.T) {
	schema := map[string]any{"type": "boolean", "enum": []any{true}}
	if validateAIValue(schema, false, "confirmUnary") == nil {
		t.Fatal("false confirmation accepted")
	}
	if err := validateAIValue(schema, true, "confirmUnary"); err != nil {
		t.Fatal(err)
	}
	action := ManifestUIAction{ID: "invoke", Risk: "read"}
	original := aiContractFingerprint(action)
	action.Risk = "dangerous"
	if original == aiContractFingerprint(action) {
		t.Fatal("risk change did not invalidate fingerprint")
	}
}

func TestPluginDefinedPermissions(t *testing.T) {
	for _, id := range []string{"third-party.custom", "unknown.future-plugin"} {
		a := ManifestUIAction{ID: id, Risk: "read"}
		got, err := resolveAIActionPermissions(a, []api.Capability{api.CapabilityShell})
		if err != nil || got.Risk != "dangerous" || len(got.Permissions) != 1 {
			t.Fatalf("inherit: %+v %v", got, err)
		}
		a.Permissions = []api.Capability{api.CapabilityNetwork}
		if _, err = resolveAIActionPermissions(a, []api.Capability{api.CapabilityWorkspaceRead}); err == nil {
			t.Fatal("permission escalation accepted")
		}
		got, err = resolveAIActionPermissions(a, []api.Capability{api.CapabilityNetwork})
		if err != nil || got.Risk != "network" {
			t.Fatalf("network: %+v %v", got, err)
		}
		a.Permissions = []api.Capability{}
		got, err = resolveAIActionPermissions(a, []api.Capability{api.CapabilityShell})
		if err != nil || got.Risk != "read" || len(got.Permissions) != 0 {
			t.Fatal("explicit empty permissions not preserved")
		}
	}
}

func TestDatabaseDriverIsNotAnActionPermission(t *testing.T) {
	declared := []api.Capability{api.CapabilityDatabaseDriver, api.CapabilityNetwork}
	got, err := resolveAIActionPermissions(ManifestUIAction{ID: "probe", Risk: "read"}, declared)
	if err != nil || got.Risk != "network" || len(got.Permissions) != 1 || got.Permissions[0] != api.CapabilityNetwork {
		t.Fatalf("inherit skipped database.driver: %+v %v", got, err)
	}
	explicit := ManifestUIAction{ID: "probe", Risk: "dangerous", Permissions: []api.Capability{api.CapabilityDatabaseDriver}}
	if _, err := resolveAIActionPermissions(explicit, declared); err == nil || !strings.Contains(err.Error(), "not an action permission") {
		t.Fatalf("explicit database.driver permission accepted: %v", err)
	}
}

func TestOfficialActionsDeclareExplicitPermissions(t *testing.T) {
	paths := officialManifestPaths(t)
	if len(paths) == 0 {
		t.Fatal("no bundled manifests found")
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var manifest Manifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			t.Fatal(err)
		}
		for _, action := range manifest.Contributes.Actions {
			if action.Permissions == nil {
				t.Errorf("%s/%s must explicitly declare permissions", manifest.ID, action.ID)
				continue
			}
			if _, err := resolveAIActionPermissions(action, manifest.Capabilities); err != nil {
				t.Errorf("%s/%s: %v", manifest.ID, action.ID, err)
			}
		}
	}
}
