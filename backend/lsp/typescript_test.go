package lsp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTypeScriptServerSDKSkipsIncompleteProjectSDK(t *testing.T) {
	root := t.TempDir()
	want := writeTypeScriptLib(t, root)
	project := filepath.Join(root, "packages", "app")
	nearest := writeTypeScriptLib(t, project)
	if err := os.Remove(filepath.Join(nearest, "tsserver.js")); err != nil {
		t.Fatal(err)
	}
	if got := findTypeScriptServerLib(project); got != want {
		t.Fatalf("incomplete project SDK selected: %q, want %q", got, want)
	}
	// TypeScript's native package has a version entry but neither JS SDK file.
	if err := os.Remove(filepath.Join(nearest, "typescript.js")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nearest, "version.cjs"), []byte(`exports.version = '7.0.2'`), 0644); err != nil {
		t.Fatal(err)
	}
	if got := findTypeScriptServerLib(project); got != want {
		t.Fatalf("native SDK did not fall back to compatible ancestor: %q", got)
	}
	if got := findTypeScriptLib(project); got != want {
		t.Fatalf("Volar SDK did not fall back to compatible ancestor: %q", got)
	}
	if got := findTypeScriptServerLib(root); got != want {
		t.Fatalf("complete project SDK not selected: %q", got)
	}
}

func TestTsserverInitializationPinsSDKAndPreservesOptions(t *testing.T) {
	root := t.TempDir()
	lib := writeTypeScriptLib(t, root)
	s := &session{root: root, definition: serverDefinition{ID: "ts-ls"}}
	out, err := s.prepareInitialize(json.RawMessage(`{"processId":42,"initializationOptions":{"preferences":{"quoteStyle":"single"},"tsserver":{"path":"/bad","logVerbosity":"off"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(out, &fields)
	options, _ := jsonObject(fields["initializationOptions"])
	tsserver, _ := jsonObject(options["tsserver"])
	want, _ := json.Marshal(filepath.Join(lib, "tsserver.js"))
	if string(tsserver["path"]) != string(want) || string(tsserver["logVerbosity"]) != `"off"` || options["preferences"] == nil || string(fields["processId"]) != "42" {
		t.Fatalf("SDK/options not preserved: %s", out)
	}
	for _, id := range []string{"ts-ls", vueCompanionID} {
		s := &session{definition: serverDefinition{ID: id}}
		if _, err := s.prepareTsserverInitialize(json.RawMessage(`{}`), "", ""); err == nil || !strings.Contains(err.Error(), "typescript@5") {
			t.Fatalf("missing SDK did not suggest compatible installation: %v", err)
		}
		if logs := s.logs(); !strings.Contains(logs, "category=typescript-sdk-unavailable") {
			t.Fatalf("missing failure category: %s", logs)
		}
		if _, err := s.prepareTsserverInitialize(json.RawMessage(`{"initializationOptions":{"tsserver":"SECRET"}}`), lib, ""); err == nil {
			t.Fatal("invalid options accepted")
		}
		if logs := s.logs(); strings.Contains(logs, "SECRET") || strings.Contains(logs, lib) {
			t.Fatalf("SDK failure leaked data: %s", logs)
		}
	}
}

func TestTypeScriptSDKFromGlobalNpmShim(t *testing.T) {
	for _, shim := range []string{"typescript-language-server.cmd", "typescript-language-server"} {
		t.Run(shim, func(t *testing.T) {
			prefix := filepath.Join(t.TempDir(), "custom npm prefix")
			want := writeTypeScriptLib(t, prefix)
			primeLookPath(t, "typescript-language-server", filepath.Join(prefix, shim))
			// Isolate unrelated globally installed servers from the fixture.
			primeLookPath(t, "vue-language-server", filepath.Join(t.TempDir(), "missing"))
			primeLookPath(t, "astro-ls", filepath.Join(t.TempDir(), "missing"))
			project := t.TempDir()
			if got := findTypeScriptServerLib(project); got != want {
				t.Fatalf("global npm shim SDK: got %q, want %q", got, want)
			}
			if got := findTypeScriptLib(project); got != want {
				t.Fatalf("Vue SDK fallback: got %q, want %q", got, want)
			}
			local := writeTypeScriptLib(t, project)
			if got := findTypeScriptServerLib(project); got != local {
				t.Fatalf("project SDK must win: got %q, want %q", got, local)
			}
		})
	}
}

func TestTypeScriptLibCandidatesForPackageEntrypoint(t *testing.T) {
	root := t.TempDir()
	want := filepath.Join(root, "node_modules", "typescript", "lib")
	for _, executable := range []string{
		filepath.Join(root, "node_modules", "typescript-language-server", "lib", "cli.mjs"),
		filepath.Join(root, "node_modules", ".bin", "typescript-language-server.cmd"),
	} {
		got := typeScriptLibsNearExecutable(executable)
		if len(got) == 0 || got[0] != want {
			t.Fatalf("package entrypoint %s: %v", executable, got)
		}
	}
}

func TestVueHybridInstallationUsesJavaScriptTypeScript(t *testing.T) {
	if !strings.HasSuffix(vueHybridInstall, " typescript@5") {
		t.Fatalf("hybrid reinstall must not select native-only TypeScript: %s", vueHybridInstall)
	}
}
