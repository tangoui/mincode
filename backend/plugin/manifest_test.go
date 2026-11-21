package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"mincode/app/plugins/api"
)

func writeTestManifest(t *testing.T, mutate func(*Manifest)) string {
	t.Helper()
	root := t.TempDir()
	entry := "plugin-runtime"
	if runtime.GOOS == "windows" {
		entry += ".exe"
	}
	if err := os.WriteFile(filepath.Join(root, entry), []byte("runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		Category:     api.CategoryDevelopment,
		APIVersion:   api.ManifestAPIVersionV2,
		ID:           "example.plugin",
		Name:         "Example Plugin",
		Version:      "1.0.0",
		Platforms:    []string{runtime.GOOS},
		Capabilities: []api.Capability{api.CapabilityStorage},
		Runtime:      ProcessRuntimeManifest{Type: "process", Entry: entry},
		Contributes:  ManifestContributions{Commands: []ManifestCommand{{ID: "example.run", Title: "Run", Action: "run"}}},
	}
	if mutate != nil {
		mutate(&manifest)
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, manifestFilename), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestReadManifestRejectsAPIVersion1(t *testing.T) {
	_, err := readManifest(writeTestManifest(t, func(manifest *Manifest) { manifest.APIVersion = 1 }))
	if err == nil || !strings.Contains(err.Error(), "unsupported plugin apiVersion 1; expected 2") {
		t.Fatalf("expected manifest API v1 rejection, got %v", err)
	}
}

func TestReadManifestValidatesAndAddsCommandCapability(t *testing.T) {
	manifest, err := readManifest(writeTestManifest(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ID != "example.plugin" {
		t.Fatalf("unexpected id %q", manifest.ID)
	}
	capabilities := capabilityStrings(manifest.Capabilities)
	if !strings.Contains(strings.Join(capabilities, ","), string(api.CapabilityCommands)) {
		t.Fatalf("commands capability missing: %v", capabilities)
	}
}

func TestReadManifestAcceptsHyphenatedShortID(t *testing.T) {
	manifest, err := readManifest(writeTestManifest(t, func(manifest *Manifest) {
		manifest.ID = "knowledge-base"
	}))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ID != "knowledge-base" {
		t.Fatalf("unexpected id %q", manifest.ID)
	}
}

func TestReadManifestRejectsInvalidContract(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest)
		want   string
	}{
		{name: "api version", mutate: func(manifest *Manifest) { manifest.APIVersion = 3 }, want: "apiVersion"},
		{name: "plugin id", mutate: func(manifest *Manifest) { manifest.ID = "Bad ID" }, want: "plugin id"},
		{name: "unknown capability", mutate: func(manifest *Manifest) { manifest.Capabilities = []api.Capability{"unknown"} }, want: "unknown plugin capability"},
		{name: "duplicate command", mutate: func(manifest *Manifest) {
			manifest.Contributes.Commands = append(manifest.Contributes.Commands, manifest.Contributes.Commands[0])
		}, want: "duplicate plugin command"},
		{name: "path traversal", mutate: func(manifest *Manifest) { manifest.Runtime.Entry = "../outside" }, want: "escapes"},
		{name: "unsupported platform", mutate: func(manifest *Manifest) {
			if runtime.GOOS == "windows" {
				manifest.Platforms = []string{"darwin"}
			} else {
				manifest.Platforms = []string{"windows"}
			}
		}, want: "does not support"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := readManifest(writeTestManifest(t, test.mutate))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected error containing %q, got %v", test.want, err)
			}
		})
	}
}

func TestReadManifestRejectsEntrypointSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not reliably available on Windows test hosts")
	}
	root := writeTestManifest(t, nil)
	outside := filepath.Join(t.TempDir(), "outside-runtime")
	if err := os.WriteFile(outside, []byte("runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	entry := "escaped-runtime"
	if err := os.Symlink(outside, filepath.Join(root, entry)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, manifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Runtime.Entry = entry
	data, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, manifestFilename), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readManifest(root); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink escape error, got %v", err)
	}
}

func writeTestManifestV2(t *testing.T, mutate func(*Manifest)) string {
	t.Helper()
	root := writeTestManifest(t, func(manifest *Manifest) {
		manifest.APIVersion = api.ManifestAPIVersionV2
		manifest.Capabilities = append(manifest.Capabilities, api.CapabilityUI)
		manifest.Contributes.Views = []ManifestView{{
			ID: "dashboard", Title: "Dashboard", Entry: "ui/index.html",
			TitleLocalized: map[string]string{"zh": "仪表盘"},
		}}
		manifest.Contributes.Actions = []ManifestUIAction{{
			ID: "dashboard.read", Risk: "read", UICallable: true,
		}}
		manifest.Contributes.Commands = []ManifestCommand{{
			ID: "example.open", Title: "Open dashboard",
			Target: &ManifestCommandTarget{Type: "view", ViewID: "dashboard"},
		}}
		if mutate != nil {
			mutate(manifest)
		}
	})
	if err := os.MkdirAll(filepath.Join(root, "ui"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ui", "index.html"), []byte("<!doctype html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestReadManifestV2VisualPlugin(t *testing.T) {
	manifest, err := readManifest(writeTestManifestV2(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.APIVersion != api.ManifestAPIVersionV2 || len(manifest.Contributes.Views) != 1 {
		t.Fatalf("unexpected v2 manifest: %#v", manifest)
	}
	if manifest.Contributes.Actions[0].MaxPayloadBytes != 16*1024 {
		t.Fatalf("unexpected default UI payload limit: %d", manifest.Contributes.Actions[0].MaxPayloadBytes)
	}
}

func TestReadManifestV2AcceptsReadOpenURLResultHandling(t *testing.T) {
	manifest, err := readManifest(writeTestManifestV2(t, func(manifest *Manifest) {
		manifest.Contributes.Actions[0].ResultHandling = " OPEN-URL "
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got := manifest.Contributes.Actions[0].ResultHandling; got != "open-url" {
		t.Fatalf("unexpected normalized resultHandling %q", got)
	}
}

func TestReadManifestV2RejectsInvalidUIContract(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest)
		want   string
	}{
		{name: "missing ui capability", mutate: func(manifest *Manifest) {
			manifest.Capabilities = []api.Capability{api.CapabilityStorage}
		}, want: "require the ui capability"},
		{name: "duplicate view", mutate: func(manifest *Manifest) {
			manifest.Contributes.Views = append(manifest.Contributes.Views, manifest.Contributes.Views[0])
		}, want: "duplicate plugin view"},
		{name: "duplicate action", mutate: func(manifest *Manifest) {
			manifest.Contributes.Actions = append(manifest.Contributes.Actions, manifest.Contributes.Actions[0])
		}, want: "duplicate plugin UI action"},
		{name: "unknown view target", mutate: func(manifest *Manifest) {
			manifest.Contributes.Commands[0].Target.ViewID = "missing"
		}, want: "targets unknown view"},
		{name: "action and target", mutate: func(manifest *Manifest) {
			manifest.Contributes.Commands[0].Action = "dashboard.read"
		}, want: "exactly one"},
		{name: "oversized payload", mutate: func(manifest *Manifest) {
			manifest.Contributes.Actions[0].MaxPayloadBytes = 256*1024 + 1
		}, want: "invalid maxPayloadBytes"},
		{name: "unsupported result handling", mutate: func(manifest *Manifest) {
			manifest.Contributes.Actions[0].Risk = "write"
			manifest.Contributes.Actions[0].ResultHandling = "open-file"
		}, want: "unsupported resultHandling"},
		{name: "read save text result handling", mutate: func(manifest *Manifest) {
			manifest.Contributes.Actions[0].ResultHandling = "save-text-file"
		}, want: "save-text-file resultHandling requires a write or dangerous action"},
		{name: "non UI action result handling", mutate: func(manifest *Manifest) {
			manifest.Contributes.Actions[0].Risk = "write"
			manifest.Contributes.Actions[0].UICallable = false
			manifest.Contributes.Actions[0].ResultHandling = "save-text-file"
		}, want: "resultHandling requires a UI-only action"},
		{name: "AI action result handling", mutate: func(manifest *Manifest) {
			manifest.Contributes.Actions[0].Risk = "write"
			manifest.Contributes.Actions[0].AICallable = true
			manifest.Contributes.Actions[0].ResultHandling = "open-url"
		}, want: "resultHandling requires a UI-only action"},
		{name: "unsupported view icon", mutate: func(manifest *Manifest) {
			manifest.Contributes.Views[0].Icon = "../../custom.svg"
		}, want: "icon is not supported"},
		{name: "unknown action risk", mutate: func(manifest *Manifest) {
			manifest.Contributes.Actions[0].Risk = "mutation"
		}, want: "risk must be read, write, or dangerous"},
		{name: "unknown command risk", mutate: func(manifest *Manifest) {
			manifest.Contributes.Commands[0].Risk = "mutation"
		}, want: "risk must be read, write, or dangerous"},
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

func TestReadManifestV2RejectsViewTraversalAndSymlinkEscape(t *testing.T) {
	t.Run("traversal", func(t *testing.T) {
		root := writeTestManifestV2(t, func(manifest *Manifest) {
			manifest.Contributes.Views[0].Entry = "ui/../outside.html"
		})
		if err := os.WriteFile(filepath.Join(root, "outside.html"), []byte("outside"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readManifest(root); err == nil || !strings.Contains(err.Error(), "escapes") {
			t.Fatalf("expected UI traversal rejection, got %v", err)
		}
	})
	if runtime.GOOS == "windows" {
		return
	}
	t.Run("symlink", func(t *testing.T) {
		root := writeTestManifestV2(t, func(manifest *Manifest) {
			manifest.Contributes.Views[0].Entry = "ui/linked.html"
		})
		outside := filepath.Join(t.TempDir(), "outside.html")
		if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, "ui", "linked.html")); err != nil {
			t.Fatal(err)
		}
		if _, err := readManifest(root); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("expected UI symlink rejection, got %v", err)
		}
	})
}

func TestReadManifestRejectsTrailingJSON(t *testing.T) {
	root := writeTestManifest(t, nil)
	path := filepath.Join(root, manifestFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, []byte(` {"extra":true}`)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readManifest(root); err == nil || !strings.Contains(err.Error(), "exactly one JSON value") {
		t.Fatalf("expected trailing JSON rejection, got %v", err)
	}
}
