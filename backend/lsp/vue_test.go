package lsp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeVueServer fakes an installed @vue/language-server package and returns its executable.
func writeVueServer(t *testing.T, root, version string) string {
	t.Helper()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(bin, "vue-language-server.js")
	if err := os.WriteFile(executable, []byte(""), 0755); err != nil {
		t.Fatal(err)
	}
	metadata, _ := json.Marshal(map[string]string{"name": "@vue/language-server", "version": version})
	if err := os.WriteFile(filepath.Join(root, "package.json"), metadata, 0644); err != nil {
		t.Fatal(err)
	}
	return executable
}

func TestVueVersionGuardAcceptsStandaloneAndHybrid(t *testing.T) {
	for version, hybrid := range map[string]bool{"2.2.12": false, "3.0.0": true, "3.1.0": true, "3.3.12": true} {
		executable := writeVueServer(t, t.TempDir(), version)
		if err := validateVueServer(executable); err != nil {
			t.Fatalf("%s rejected: %v", version, err)
		}
		install, err := inspectVueServer(executable)
		if err != nil || install.hybrid() != hybrid {
			t.Fatalf("%s hybrid=%v err=%v", version, install.hybrid(), err)
		}
	}
	for _, version := range []string{"2.2.0", "2.0.29", "4.0.0", "not-semver"} {
		err := validateVueServer(writeVueServer(t, t.TempDir(), version))
		if err == nil || !strings.Contains(err.Error(), vueStandaloneInstall) || !strings.Contains(err.Error(), vueHybridInstall) {
			t.Fatalf("%s: missing compatibility/reinstall error: %v", version, err)
		}
	}
	if err := validateVueServer(filepath.Join(t.TempDir(), "unknown")); err == nil {
		t.Fatal("unverifiable server accepted")
	}
}

func TestVueHybridInitializeKeepsDelegation(t *testing.T) {
	root := t.TempDir()
	lib := writeTypeScriptLib(t, root)
	s := &session{root: root, definition: serverDefinition{ID: "vue-ls"}, vueHybrid: true}
	out, err := s.prepareInitialize(json.RawMessage(`{"initializationOptions":{"vue":{"custom":2}}}`))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(out, &fields)
	options, _ := jsonObject(fields["initializationOptions"])
	ts, _ := jsonObject(options["typescript"])
	vue, _ := jsonObject(options["vue"])
	want, _ := json.Marshal(lib)
	if string(ts["tsdk"]) != string(want) || vue["hybridMode"] != nil || string(vue["custom"]) != "2" {
		t.Fatalf("hybrid options wrong: %s", out)
	}
	if _, err := s.prepareSDKInitialize(json.RawMessage(`{}`), ""); err == nil || !strings.HasSuffix(err.Error(), "run "+vueHybridInstall) {
		t.Fatalf("hybrid SDK hint: %v", err)
	}
}

// primeLookPath points cachedLookPath at a fake executable for one test.
func primeLookPath(t *testing.T, command, path string) {
	t.Helper()
	lookPathMu.Lock()
	lookPathCache[command] = struct {
		path string
		err  error
		at   time.Time
	}{path, nil, time.Now().Add(time.Hour)}
	lookPathMu.Unlock()
	t.Cleanup(func() { invalidateLookPathCache(command) })
}

func TestVueTypeScriptPluginLocation(t *testing.T) {
	// Global layout: lib/node_modules/@vue/{language-server,typescript-plugin}.
	modules := filepath.Join(t.TempDir(), "lib", "node_modules")
	server := filepath.Join(modules, "@vue", "language-server")
	if _, ok := findVueTypeScriptPlugin(server); ok {
		t.Fatal("plugin found before it exists")
	}
	hoisted := filepath.Join(modules, "@vue", "typescript-plugin")
	if err := os.MkdirAll(hoisted, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hoisted, "package.json"), []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}
	if location, ok := findVueTypeScriptPlugin(server); !ok || location != filepath.Dir(modules) {
		t.Fatalf("hoisted plugin location = %q %v", location, ok)
	}
	nested := filepath.Join(server, "node_modules", "@vue", "typescript-plugin")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "package.json"), []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}
	if location, ok := findVueTypeScriptPlugin(server); !ok || location != server {
		t.Fatalf("nested plugin location = %q %v", location, ok)
	}
}

func TestVueCompanionInitializeLoadsPlugin(t *testing.T) {
	server := filepath.Join(canonicalTempDir(t), "node_modules", "@vue", "language-server")
	executable := writeVueServer(t, server, "3.1.0")
	plugin := filepath.Join(server, "node_modules", "@vue", "typescript-plugin")
	if err := os.MkdirAll(plugin, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plugin, "package.json"), []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}
	primeLookPath(t, "vue-language-server", executable)
	root := t.TempDir()
	lib := writeTypeScriptLib(t, root)
	s := &session{root: root, definition: serverDefinition{ID: vueCompanionID}}
	out, err := s.prepareInitialize(json.RawMessage(`{"processId":1,"initializationOptions":{"preferences":{"quoteStyle":"single"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(out, &fields)
	options, _ := jsonObject(fields["initializationOptions"])
	var plugins []struct {
		Name      string   `json:"name"`
		Location  string   `json:"location"`
		Languages []string `json:"languages"`
	}
	if err := json.Unmarshal(options["plugins"], &plugins); err != nil || len(plugins) != 1 {
		t.Fatalf("plugins = %s", options["plugins"])
	}
	if plugins[0].Name != vueTypeScriptPlugin || plugins[0].Location != server || len(plugins[0].Languages) != 1 || plugins[0].Languages[0] != "vue" {
		t.Fatalf("plugin entry = %+v", plugins[0])
	}
	tsserver, _ := jsonObject(options["tsserver"])
	want, _ := json.Marshal(filepath.Join(lib, "tsserver.js"))
	if string(tsserver["path"]) != string(want) || options["preferences"] == nil || string(fields["processId"]) != "1" {
		t.Fatalf("companion options wrong: %s", out)
	}

	// A 2.x server has no hybrid half: the companion refuses to initialize.
	standalone := writeVueServer(t, t.TempDir(), vueStandaloneVersion)
	primeLookPath(t, "vue-language-server", standalone)
	if _, err := s.prepareInitialize(json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "3.x") {
		t.Fatalf("companion accepted a standalone server: %v", err)
	}
}

func TestVueStatusAnnouncesCompanion(t *testing.T) {
	definition, _ := definitionByID("vue-ls")
	primeLookPath(t, "vue-language-server", writeVueServer(t, t.TempDir(), "3.0.0"))
	if status := executableStatus(definition); status.Error != "" || len(status.Companions) != 1 || status.Companions[0] != vueCompanionID {
		t.Fatalf("hybrid status = %+v", status)
	}
	primeLookPath(t, "vue-language-server", writeVueServer(t, t.TempDir(), vueStandaloneVersion))
	if status := executableStatus(definition); status.Error != "" || len(status.Companions) != 0 {
		t.Fatalf("standalone status = %+v", status)
	}
	companion, ok := definitionByID(vueCompanionID)
	if !ok || !companion.Auxiliary || len(companion.Languages) != 1 || companion.Languages[0] != "vue" {
		t.Fatalf("companion definition = %+v", companion)
	}
}

func TestVueInitializeEnforcesStandalonePreservingOtherFields(t *testing.T) {
	root := t.TempDir()
	lib := writeTypeScriptLib(t, root)
	s := &session{root: root, definition: serverDefinition{ID: "vue-ls"}}
	raw := json.RawMessage(`{"capabilities":{"workspace":{}},"processId":42,"initializationOptions":{"custom":true,"typescript":{"tsdk":"/bad","custom":1},"vue":{"hybridMode":true,"custom":2}}}`)
	out, err := s.prepareInitialize(raw)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(out, &fields)
	options, _ := jsonObject(fields["initializationOptions"])
	ts, _ := jsonObject(options["typescript"])
	vue, _ := jsonObject(options["vue"])
	want, _ := json.Marshal(lib)
	if string(ts["tsdk"]) != string(want) || string(vue["hybridMode"]) != "false" {
		t.Fatalf("options not enforced: %s", out)
	}
	if string(options["custom"]) != "true" || string(ts["custom"]) != "1" || string(vue["custom"]) != "2" || string(fields["processId"]) != "42" {
		t.Fatalf("other fields lost: %s", out)
	}
	for _, input := range []string{`[]`, `{"initializationOptions":[]}`, `{"initializationOptions":{"typescript":1}}`, `{"initializationOptions":{"vue":true}}`} {
		if _, err := s.prepareInitialize(json.RawMessage(input)); err == nil {
			t.Fatalf("invalid options accepted: %s", input)
		}
	}
	for _, id := range []string{"gopls", "plugin:demo/vue"} {
		other := &session{definition: serverDefinition{ID: id}}
		out, err := other.prepareInitialize(raw)
		if err != nil || string(out) != string(raw) {
			t.Fatalf("other server options changed: %s %v", out, err)
		}
	}
}

func TestSDKUnavailableHintMatchesServer(t *testing.T) {
	for id, want := range map[string]string{"vue-ls": vueStandaloneInstall, "astro-ls": typeScriptInstall} {
		s := &session{definition: serverDefinition{ID: id}}
		_, err := s.prepareSDKInitialize(json.RawMessage(`{}`), "")
		if err == nil || !strings.HasSuffix(err.Error(), "run "+want) {
			t.Fatalf("%s hint: %v", id, err)
		}
	}
}
