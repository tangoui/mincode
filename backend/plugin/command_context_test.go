package plugin

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"mincode/app/plugins/api"
)

func TestParseWhenExpression(t *testing.T) {
	valid := []string{
		"connected",
		"!connected",
		"mode == \"debug\"",
		"mode != \"debug\" && connected",
		"a && b && c",
	}
	for _, expression := range valid {
		if _, err := parseWhenExpression(expression); err != nil {
			t.Fatalf("expected %q to parse: %v", expression, err)
		}
	}
	invalid := []string{
		"",
		"&&",
		"connected &&",
		"&& connected",
		"connected || ready",
		"mode = \"debug\"",
		"mode == debug",
		"mode == \"unterminated",
		"! ",
		"Connected",
		"a && b && c && d && e && f && g && h && i",
		strings.Repeat("a", commandWhenMaxLength+1),
	}
	for _, expression := range invalid {
		if _, err := parseWhenExpression(expression); err == nil {
			t.Fatalf("expected %q to be rejected", expression)
		}
	}
}

func TestEvalWhenClauses(t *testing.T) {
	context := map[string]any{"connected": true, "mode": "debug", "count": float64(0)}
	tests := []struct {
		expression string
		want       bool
	}{
		{"connected", true},
		{"!connected", false},
		{"missing", false},
		{"!missing", true},
		{"count", false},
		{"mode == \"debug\"", true},
		{"mode == \"release\"", false},
		{"mode != \"release\"", true},
		{"missing != \"release\"", true},
		{"connected == \"true\"", false},
		{"connected && mode == \"debug\"", true},
		{"connected && mode == \"release\"", false},
		{"connected && missing", false},
	}
	for _, test := range tests {
		clauses, err := parseWhenExpression(test.expression)
		if err != nil {
			t.Fatalf("parse %q: %v", test.expression, err)
		}
		if got := evalWhenClauses(clauses, context); got != test.want {
			t.Fatalf("%q evaluated to %v, want %v", test.expression, got, test.want)
		}
	}
}

func validCommandInputSchema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []any{"name"},
		"properties": map[string]any{
			"name":  map[string]any{"type": "string", "description": "名称"},
			"retry": map[string]any{"type": "integer", "default": float64(3)},
			"level": map[string]any{"type": "string", "enum": []any{"info", "error"}},
			"force": map[string]any{"type": "boolean"},
		},
	}
}

func TestValidateCommandInputSchema(t *testing.T) {
	if err := validateCommandInputSchema(validCommandInputSchema()); err != nil {
		t.Fatalf("expected valid schema: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{"unsupported keyword", func(s map[string]any) { s["pattern"] = "x" }, "unsupported keyword"},
		{"non object root", func(s map[string]any) { s["type"] = "string" }, "must be object"},
		{"missing properties", func(s map[string]any) { delete(s, "properties") }, "requires properties"},
		{"bad property name", func(s map[string]any) {
			s["properties"].(map[string]any)["0bad"] = map[string]any{"type": "string"}
		}, "is invalid"},
		{"nested object property", func(s map[string]any) {
			s["properties"].(map[string]any)["nested"] = map[string]any{"type": "object"}
		}, "must be string, integer, number, or boolean"},
		{"property unsupported keyword", func(s map[string]any) {
			s["properties"].(map[string]any)["name"].(map[string]any)["minLength"] = float64(1)
		}, "unsupported keyword"},
		{"empty enum", func(s map[string]any) {
			s["properties"].(map[string]any)["level"].(map[string]any)["enum"] = []any{}
		}, "enum is invalid"},
		{"enum type mismatch", func(s map[string]any) {
			s["properties"].(map[string]any)["level"].(map[string]any)["enum"] = []any{float64(1)}
		}, "expects a string value"},
		{"default type mismatch", func(s map[string]any) {
			s["properties"].(map[string]any)["retry"].(map[string]any)["default"] = "3"
		}, "expects an integer value"},
		{"non integer default", func(s map[string]any) {
			s["properties"].(map[string]any)["retry"].(map[string]any)["default"] = float64(1.5)
		}, "expects an integer value"},
		{"required unknown", func(s map[string]any) { s["required"] = []any{"unknown"} }, "unknown property"},
		{"required duplicated", func(s map[string]any) { s["required"] = []any{"name", "name"} }, "duplicates required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			schema := validCommandInputSchema()
			test.mutate(schema)
			if err := validateCommandInputSchema(schema); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected error containing %q, got %v", test.want, err)
			}
		})
	}
	t.Run("too many properties", func(t *testing.T) {
		properties := map[string]any{}
		for index := 0; index < commandInputSchemaMaxProperties+1; index++ {
			properties[string(rune('a'+index%26))+strings.Repeat("x", index/26+1)] = map[string]any{"type": "string"}
		}
		schema := map[string]any{"type": "object", "properties": properties}
		if err := validateCommandInputSchema(schema); err == nil || !strings.Contains(err.Error(), "properties") {
			t.Fatalf("expected property limit error, got %v", err)
		}
	})
	t.Run("oversized schema", func(t *testing.T) {
		schema := validCommandInputSchema()
		schema["description"] = strings.Repeat("x", commandInputSchemaMaxBytes)
		if err := validateCommandInputSchema(schema); err == nil || !strings.Contains(err.Error(), "exceeds 16 KiB") {
			t.Fatalf("expected schema size rejection, got %v", err)
		}
	})
}

func TestReadManifestV2CommandEnhancements(t *testing.T) {
	actionCommand := func() ManifestCommand {
		return ManifestCommand{
			ID: "example.run", Title: "Run", Action: "run",
			When:        "connected && mode == \"debug\"",
			InputSchema: validCommandInputSchema(),
		}
	}
	t.Run("accepts when and inputSchema", func(t *testing.T) {
		manifest, err := readManifest(writeTestManifestV2(t, func(manifest *Manifest) {
			manifest.Contributes.Commands = []ManifestCommand{actionCommand()}
		}))
		if err != nil {
			t.Fatal(err)
		}
		command := manifest.Contributes.Commands[0]
		if command.When != `connected && mode == "debug"` || command.InputSchema == nil {
			t.Fatalf("unexpected command enhancements: %#v", command)
		}
	})
	tests := []struct {
		name   string
		mutate func(*Manifest)
		want   string
	}{
		{"when on view target", func(manifest *Manifest) {
			manifest.Contributes.Commands[0].When = "connected"
		}, "when requires an action command"},
		{"inputSchema on view target", func(manifest *Manifest) {
			manifest.Contributes.Commands[0].InputSchema = validCommandInputSchema()
		}, "inputSchema requires an action command"},
		{"invalid when syntax", func(manifest *Manifest) {
			manifest.Contributes.Commands = []ManifestCommand{actionCommand()}
			manifest.Contributes.Commands[0].When = "connected ||"
		}, "when"},
		{"invalid schema", func(manifest *Manifest) {
			manifest.Contributes.Commands = []ManifestCommand{actionCommand()}
			manifest.Contributes.Commands[0].InputSchema = map[string]any{"type": "string"}
		}, "must be object"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := readManifest(writeTestManifestV2(t, test.mutate))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected error containing %q, got %v", test.want, err)
			}
		})
	}
}

func TestValidateContextValue(t *testing.T) {
	for _, value := range []any{"text", float64(1), true, nil} {
		if err := validateContextValue(value); err != nil {
			t.Fatalf("expected %v to be accepted: %v", value, err)
		}
	}
	if err := validateContextValue(map[string]any{"a": 1}); err == nil {
		t.Fatal("expected object value rejection")
	}
	if err := validateContextValue([]any{1}); err == nil {
		t.Fatal("expected array value rejection")
	}
	if err := validateContextValue(strings.Repeat("x", pluginContextValueMax)); err == nil {
		t.Fatal("expected oversized value rejection")
	}
}

func TestPluginRecordContextLifecycle(t *testing.T) {
	manager := &pluginManager{}
	record := &pluginRecord{id: "example.plugin"}
	manager.records = map[string]*pluginRecord{record.id: record}
	manager.order = []string{record.id}

	if err := manager.setRecordContext(record, "connected", true); err != nil {
		t.Fatal(err)
	}
	if err := manager.setRecordContext(record, "mode", "debug"); err != nil {
		t.Fatal(err)
	}
	if err := manager.setRecordContext(record, "Bad Key", true); err == nil {
		t.Fatal("expected invalid key rejection")
	}
	if err := manager.setRecordContext(record, "obj", map[string]any{}); err == nil {
		t.Fatal("expected object value rejection")
	}
	for index := 0; index < pluginContextMaxKeys; index++ {
		key := "k" + strings.Repeat("x", 3) + strings.Repeat("0", index%10) + strings.Repeat("1", index/10)
		_ = manager.setRecordContext(record, key, index)
	}
	if len(record.contextValues) > pluginContextMaxKeys {
		t.Fatalf("context exceeds key limit: %d", len(record.contextValues))
	}
	snapshot := manager.Contexts()
	if snapshot["example.plugin"]["connected"] != true || snapshot["example.plugin"]["mode"] != "debug" {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	manager.clearRecordContext(record)
	if len(manager.Contexts()) != 0 {
		t.Fatal("expected context to be cleared")
	}
}

// contextRuntime builds a runtime wired like manager.newRecordRuntime without
// starting a process, so host requests can be driven directly.
func contextRuntime(t *testing.T, capabilities []api.Capability, contextSet func(string, any) error) *processRuntime {
	t.Helper()
	manifest := Manifest{ID: "example.plugin", Capabilities: capabilities}
	runtimeHost := newProcessRuntime(t.TempDir(), manifest, nil, nil)
	runtimeHost.cmd = &exec.Cmd{}
	runtimeHost.generation = 1
	runtimeHost.runContext = context.Background()
	runtimeHost.writes = make(chan rpcWriteRequest, 1)
	runtimeHost.contextSet = contextSet
	return runtimeHost
}

func drainRuntimeWrites(runtimeHost *processRuntime) chan rpcEnvelope {
	responses := make(chan rpcEnvelope, 4)
	go func() {
		for request := range runtimeHost.writes {
			var envelope rpcEnvelope
			if err := json.Unmarshal(request.data, &envelope); err == nil {
				responses <- envelope
			}
			request.result <- nil
		}
	}()
	return responses
}

func TestHostContextSetRequest(t *testing.T) {
	t.Run("stores value", func(t *testing.T) {
		var gotKey string
		var gotValue any
		runtimeHost := contextRuntime(t, []api.Capability{api.CapabilityCommands}, func(key string, value any) error {
			gotKey, gotValue = key, value
			return nil
		})
		runtimeHost.handleHostRequest(1, rpcEnvelope{Method: "host.context.set",
			Params: json.RawMessage(`{"key":"connected","value":true}`)})
		if gotKey != "connected" || gotValue != true {
			t.Fatalf("unexpected context set: %q %#v", gotKey, gotValue)
		}
	})
	t.Run("requires commands capability", func(t *testing.T) {
		runtimeHost := contextRuntime(t, []api.Capability{api.CapabilityStorage}, func(string, any) error {
			t.Fatal("contextSet must not run without the commands capability")
			return nil
		})
		responses := drainRuntimeWrites(runtimeHost)
		runtimeHost.handleHostRequest(1, rpcEnvelope{ID: json.RawMessage(`"1"`), Method: "host.context.set",
			Params: json.RawMessage(`{"key":"connected","value":true}`)})
		response := <-responses
		if response.Error == nil || response.Error.Code != -32001 {
			t.Fatalf("expected capability rejection, got %#v", response.Error)
		}
	})
	t.Run("rejects invalid value", func(t *testing.T) {
		runtimeHost := contextRuntime(t, []api.Capability{api.CapabilityCommands},
			func(key string, value any) error { return validateContextValue(value) })
		responses := drainRuntimeWrites(runtimeHost)
		runtimeHost.handleHostRequest(1, rpcEnvelope{ID: json.RawMessage(`"2"`), Method: "host.context.set",
			Params: json.RawMessage(`{"key":"bad","value":{"nested":true}}`)})
		response := <-responses
		if response.Error == nil || response.Error.Code != -32602 {
			t.Fatalf("expected value rejection, got %#v", response.Error)
		}
	})
}
