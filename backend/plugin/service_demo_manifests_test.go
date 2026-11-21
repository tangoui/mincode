package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestServiceDemoManifestsInstall runs the strict install preview over the
// devkit's service demos (tools/plugin-devkit/examples), so the demos keep
// passing the host's checks, not only the CLI's structural ones.
func TestServiceDemoManifestsInstall(t *testing.T) {
	useTemporaryPluginData(t)
	examples := filepath.Join("..", "..", "tools", "plugin-devkit", "examples")
	cases := []struct {
		name     string
		services map[string][]string // service → surfaces
		commands int
	}{
		{name: "service-provider-demo", services: map[string][]string{
			"demo.text.reverse":  {"selection", "palette"},
			"demo.file.checksum": {"fileMenu", "palette"},
			"demo.file.copy":     {"fileMenu", "palette"},
			"demo.wait":          {"palette"},
		}},
		{name: "service-consumer-demo", commands: 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(examples, tc.name, "plugin.json"))
			if err != nil {
				t.Fatal(err)
			}
			// The binary is built on demand; the preview only needs the entry to exist.
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "plugin.json"), data, 0o644); err != nil {
				t.Fatal(err)
			}
			entry := filepath.Join(root, "bin", tc.name+".exe")
			if err := os.MkdirAll(filepath.Dir(entry), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(entry, []byte("binary"), 0o755); err != nil {
				t.Fatal(err)
			}
			// View pages are build output too (git-ignored); stub each declared entry.
			var declared struct {
				Contributes struct {
					Views []struct {
						Entry string `json:"entry"`
					} `json:"views"`
				} `json:"contributes"`
			}
			if err := json.Unmarshal(data, &declared); err != nil {
				t.Fatal(err)
			}
			for _, view := range declared.Contributes.Views {
				page := filepath.Join(root, filepath.FromSlash(view.Entry))
				if err := os.MkdirAll(filepath.Dir(page), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(page, []byte("<!doctype html>"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			preview, err := inspectInstallDirectory(root)
			if err != nil {
				t.Fatalf("install preview: %v", err)
			}
			manifest := preview.Manifest
			if manifest.Runtime.Type != "process" || len(manifest.Contributes.Commands) != tc.commands {
				t.Fatalf("manifest runtime %q, %d commands", manifest.Runtime.Type, len(manifest.Contributes.Commands))
			}
			got := map[string][]string{}
			if resolved := manifest.contribution(servicePointKey); resolved != nil {
				for _, entry := range resolved.Entries {
					service := entry.(ServiceContribution)
					got[service.Service] = service.Surfaces
				}
			}
			if len(tc.services) == 0 {
				tc.services = map[string][]string{}
			}
			if !reflect.DeepEqual(got, tc.services) {
				t.Fatalf("services = %v, want %v", got, tc.services)
			}
		})
	}
}
