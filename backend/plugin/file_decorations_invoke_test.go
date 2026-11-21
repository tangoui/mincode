package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"mincode/app/plugins/api"
)

// fileDecorationsManager is a running helper plugin contributing the file
// decoration provider "lint" backed by its read action "run".
func fileDecorationsManager(t *testing.T, capabilities ...api.Capability) (*pluginManager, *pluginRecord) {
	t.Helper()
	manager, record := startedContributionManager(t)
	record.mu.Lock()
	record.manifest.Capabilities = capabilities
	for index := range record.manifest.Contributes.Actions {
		if record.manifest.Contributes.Actions[index].ID == "run" {
			record.manifest.Contributes.Actions[index].MaxPayloadBytes = fileDecorationsPayloadMaxBytes
		}
	}
	record.manifest.contributions = map[string]*resolvedContribution{fileDecorationPointKey: {Entries: []any{
		FileDecorationContribution{ID: "lint", Title: "Lint", Action: "run"},
	}}}
	record.mu.Unlock()
	return manager, record
}

// fileDecorationsProject is a project folder with a "src" directory.
func fileDecorationsProject(t *testing.T) string {
	t.Helper()
	project := filepath.Join(t.TempDir(), "demo")
	if err := os.MkdirAll(filepath.Join(project, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	return project
}

// helperDecorationsPayload reads the last payload the helper logged.
func helperDecorationsPayload(t *testing.T, record *pluginRecord) map[string]json.RawMessage {
	t.Helper()
	line := waitForLog(t, record, "helper decorations payload ")
	_, payloadJSON, _ := strings.Cut(line, "helper decorations payload ")
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		t.Fatalf("payload %q: %v", payloadJSON, err)
	}
	return payload
}

func TestFileDecorationsDropsUnrequestedAndDenied(t *testing.T) {
	project := fileDecorationsProject(t)
	manager, record := fileDecorationsManager(t, api.CapabilityWorkspaceRead)
	request := fileDecorationsRequest{PluginID: record.id, ProviderID: "lint", RequestID: "request-deco-0001", ProjectRoot: project, Directory: "src", Names: []string{"a.ts", "b.ts", ".env", "a.ts"}, Locale: "zh-CN"}
	response, err := manager.fileDecorations(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Decorations) != 1 || response.Decorations["a.ts"] != (FileDecoration{Badge: "E", Color: "danger"}) || response.Dropped != 2 || response.Skipped != 1 {
		t.Fatalf("response = %+v", response)
	}
	encoded, _ := json.Marshal(response)
	if string(encoded) != `{"decorations":{"a.ts":{"badge":"E","color":"danger"}},"dropped":2,"skipped":1}` {
		t.Fatalf("encoded = %s", encoded)
	}
	payload := helperDecorationsPayload(t, record)
	canonical, _ := filepath.EvalSymlinks(project)
	for key, want := range map[string]string{
		"provider": `"lint"`, "directory": `"src"`, "names": `["a.ts","b.ts"]`, "locale": `"zh-CN"`,
		"project": `{"name":"demo","root":` + string(mustJSON(t, canonical)) + `}`,
	} {
		if string(payload[key]) != want {
			t.Errorf("payload %s = %s, want %s", key, payload[key], want)
		}
	}
	if len(payload) != 5 {
		t.Errorf("payload keys = %v", payload)
	}
	assertNoContributionRequestsLeft(t)

	for _, directory := range []string{".git", "node_modules/.git/x", "src/.svn"} {
		request.Directory, request.Names = directory, []string{"a.ts"}
		if _, err := manager.fileDecorations(context.Background(), request); contributionErrorCode(err) != contributionErrForbidden {
			t.Errorf("directory %q: err = %v, want forbidden", directory, err)
		}
	}
	request.Directory = "src"
	for name, names := range map[string][]string{
		"slash":       {"a/b"},
		"dotdot":      {".."},
		"dot":         {"."},
		"nul":         {"a\u0000b"},
		"too long":    {strings.Repeat("a", 256)},
		"empty":       {""},
		"backslash":   {`a\b`},
		"bidi":        {"a\u202eb"},
		"separator":   {"a\u2028b"},
		"soft hyphen": {"a\u00adb"},
		"newline":     {"a\nb"},
		"invalid":     {"a\xffb"},
		"none":        {},
	} {
		request.Names = names
		if _, err := manager.fileDecorations(context.Background(), request); contributionErrorCode(err) != contributionErrInvalidRequest {
			t.Errorf("names %s: err = %v, want invalid_request", name, err)
		}
	}
	many := make([]string, 501)
	for index := range many {
		many[index] = "f" + strconv.Itoa(index)
	}
	request.Names = many
	if _, err := manager.fileDecorations(context.Background(), request); contributionErrorCode(err) != contributionErrInvalidRequest {
		t.Fatalf("501 names: err = %v", err)
	}
	if _, err := manager.fileDecorations(context.Background(), fileDecorationsRequest{PluginID: record.id, ProviderID: "other", RequestID: "request-deco-0002", ProjectRoot: project, Names: []string{"a"}}); contributionErrorCode(err) != contributionErrNotFound {
		t.Fatalf("unknown provider err = %v", err)
	}

	// Without workspace.read the provider is never called.
	manager, record = fileDecorationsManager(t)
	if _, err := manager.fileDecorations(context.Background(), fileDecorationsRequest{PluginID: record.id, ProviderID: "lint", RequestID: "request-deco-0003", ProjectRoot: project, Names: []string{"a"}}); contributionErrorCode(err) != contributionErrForbidden {
		t.Fatalf("no capability err = %v", err)
	}

	for _, args := range []string{
		`{"pluginId":"runtime.test","providerId":"lint","requestId":"request-deco-0004","projectRoot":"/tmp","directory":"","names":["a"],"path":"x"}`,
		`{"pluginId":"runtime.test","providerId":"lint","requestId":"request-deco-0004","projectRoot":"","directory":"","names":["a"]}`,
		`nope`,
	} {
		if _, err := pluginHostFileDecorationsHandler("", args); contributionErrorCode(err) != contributionErrInvalidRequest {
			t.Errorf("handler %s: err = %v", args, err)
		}
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestFileDecorationsRejectsEscapingDirectory(t *testing.T) {
	project := fileDecorationsProject(t)
	outside := filepath.Join(filepath.Dir(project), "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(project, "out")); err != nil {
		t.Fatal(err)
	}
	manager, record := fileDecorationsManager(t, api.CapabilityWorkspaceRead)
	request := fileDecorationsRequest{PluginID: record.id, ProviderID: "lint", RequestID: "request-deco-0101", ProjectRoot: project, Names: []string{"a.ts"}}
	for _, directory := range []string{"out", "out/sub"} {
		request.Directory = directory
		if _, err := manager.fileDecorations(context.Background(), request); contributionErrorCode(err) != contributionErrForbidden {
			t.Errorf("directory %q: err = %v, want forbidden", directory, err)
		}
	}
	for _, directory := range []string{"../x", "/abs", `a\b`, "a%2fb", "src/", "./src", "src//a", "C:/x", "~/x", "src/\u202ex", strings.Repeat("d", 1025)} {
		request.Directory = directory
		if _, err := manager.fileDecorations(context.Background(), request); contributionErrorCode(err) != contributionErrInvalidRequest {
			t.Errorf("directory %q: err = %v, want invalid_request", directory, err)
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	request.Directory, request.ProjectRoot = "", home
	if _, err := manager.fileDecorations(context.Background(), request); contributionErrorCode(err) != contributionErrForbidden {
		t.Errorf("home project: err = %v, want forbidden", err)
	}

	request.Directory, request.ProjectRoot, request.RequestID = "", project, "request-deco-0102"
	if _, err := manager.fileDecorations(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	payload := helperDecorationsPayload(t, record)
	canonical, _ := filepath.EvalSymlinks(project)
	if string(payload["directory"]) != `""` || string(payload["project"]) != `{"name":"demo","root":`+string(mustJSON(t, canonical))+`}` || string(payload["locale"]) != `"en"` {
		t.Fatalf("payload = %s %s %s", payload["directory"], payload["project"], payload["locale"])
	}
	assertNoContributionRequestsLeft(t)
}

func TestFileDecorationsResultValidation(t *testing.T) {
	cases := []struct {
		name string
		item string
		want *FileDecoration
	}{
		{"badge OK", `{"badge":"OK"}`, &FileDecoration{Badge: "OK"}},
		{"badge check", `{"badge":"✓","color":"success"}`, &FileDecoration{Badge: "✓", Color: "success"}},
		{"badge han", `{"badge":"李"}`, &FileDecoration{Badge: "李"}},
		{"badge trimmed", `{"badge":" E "}`, &FileDecoration{Badge: "E"}},
		{"badge three", `{"badge":"ABC"}`, nil},
		{"badge blank", `{"badge":" "}`, nil},
		{"badge empty", `{"badge":"","color":"muted"}`, nil},
		{"badge inner space", `{"badge":"a b"}`, nil},
		{"badge zwj", `{"badge":"👍\u200d"}`, nil},
		{"badge rlo", `{"badge":"\u202eA"}`, nil},
		{"badge control", `{"badge":"\u0007"}`, nil},
		{"badge not string", `{"badge":1}`, nil},
		{"color hex", `{"color":"#f00"}`, nil},
		{"color name", `{"color":"red"}`, nil},
		{"color var", `{"color":"var(--x)"}`, nil},
		{"color with badge", `{"badge":"E","color":"red"}`, nil},
		{"color muted", `{"color":"muted"}`, &FileDecoration{Color: "muted"}},
		{"neither", `{}`, nil},
		{"tooltip only", `{"tooltip":"x"}`, nil},
		{"tooltip ok", `{"badge":"E","tooltip":" 3 errors "}`, &FileDecoration{Badge: "E", Tooltip: "3 errors"}},
		{"tooltip 128", `{"badge":"E","tooltip":"` + strings.Repeat("中", 128) + `"}`, &FileDecoration{Badge: "E", Tooltip: strings.Repeat("中", 128)}},
		{"tooltip 129", `{"badge":"E","tooltip":"` + strings.Repeat("a", 129) + `"}`, nil},
		{"tooltip newline", `{"badge":"E","tooltip":"a\nb"}`, nil},
		{"tooltip bidi", `{"badge":"E","tooltip":"a\u202eb"}`, nil},
		{"unknown field", `{"badge":"E","style":"x"}`, nil},
		{"not an object", `"E"`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response, err := parseFileDecorationsResult(json.RawMessage(`{"decorations":{"a":`+tc.item+`}}`), map[string]bool{"a": true})
			if err != nil {
				t.Fatal(err)
			}
			got, kept := response.Decorations["a"]
			if tc.want == nil {
				if kept || response.Dropped != 1 {
					t.Fatalf("kept %+v dropped %d", got, response.Dropped)
				}
				return
			}
			if !kept || got != *tc.want || response.Dropped != 0 {
				t.Fatalf("got %+v kept %v dropped %d, want %+v", got, kept, response.Dropped, *tc.want)
			}
		})
	}
	for _, raw := range []string{`[]`, `{}`, `{"decorations":null}`, `{"decorations":[]}`, `{"decorations":{},"extra":1}`, `null`} {
		if _, err := parseFileDecorationsResult(json.RawMessage(raw), map[string]bool{"a": true}); err == nil {
			t.Errorf("%s accepted", raw)
		}
	}
	response, err := parseFileDecorationsResult(json.RawMessage(`{"decorations":{"b":{"badge":"E"},"a":{"badge":"W"}}}`), map[string]bool{"a": true})
	if err != nil || len(response.Decorations) != 1 || response.Decorations["a"].Badge != "W" || response.Dropped != 1 {
		t.Fatalf("unrequested = %+v %v", response, err)
	}
	for value, want := range map[string]bool{"OK": true, "✓": true, "李": true, "3": true, "!": true, "ABC": false, " ": false, "": false, "a b": false, "👍\u200d": false, "\u202eA": false, "e\u0301": false} {
		if _, ok := validDecorationBadge(value); ok != want {
			t.Errorf("validDecorationBadge(%q) = %v, want %v", value, ok, want)
		}
	}
}
