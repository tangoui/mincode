package plugin

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"mincode/app/plugins/api"
)

// The official drawing plugins open their project files by default as
// read/write custom editors on the existing main view.
func TestOfficialDrawingCustomEditors(t *testing.T) {
	cases := []struct {
		plugin   string
		id       string
		patterns []string
	}{
		{plugin: "draw", id: "drawio", patterns: []string{"*.drawio", "*.dio"}},
		{plugin: "excalidraw", id: "excalidraw", patterns: []string{"*.excalidraw", "*.excalidraw.json"}},
	}
	for _, tc := range cases {
		t.Run(tc.plugin, func(t *testing.T) {
			dir := filepath.Join("../../official-plugins", tc.plugin)
			// official-plugins/draw is a gitlink without a submodule; clean checkouts leave it empty.
			if _, err := os.Stat(filepath.Join(dir, "plugin.json")); tc.plugin == "draw" && errors.Is(err, fs.ErrNotExist) {
				t.Skip("official-plugins/draw is not checked out")
			}
			manifest := readContributionManifestForTest(t, dir)
			if !slices.Contains(manifest.Capabilities, api.CapabilityWorkspaceWrite) {
				t.Fatalf("capabilities %v lack %s", manifest.Capabilities, api.CapabilityWorkspaceWrite)
			}
			resolved := manifest.contribution("customEditors")
			if resolved == nil {
				t.Fatal("customEditors contribution is missing")
			}
			if len(resolved.Issues) != 0 {
				t.Fatalf("customEditors issues: %+v", resolved.Issues)
			}
			if len(resolved.Entries) != 1 {
				t.Fatalf("customEditors entries = %d, want 1", len(resolved.Entries))
			}
			editor, ok := resolved.Entries[0].(CustomEditorContribution)
			if !ok {
				t.Fatalf("entry type %T", resolved.Entries[0])
			}
			if editor.ID != tc.id || editor.ViewID != "main" || editor.Priority != "default" || editor.Access != "readWrite" {
				t.Fatalf("unexpected editor %+v", editor)
			}
			if !slices.Equal(editor.FilenamePatterns, tc.patterns) {
				t.Fatalf("patterns = %v, want %v", editor.FilenamePatterns, tc.patterns)
			}
			if editor.TitleLocalized["zh"] == "" || editor.TitleLocalized["en"] == "" {
				t.Fatalf("titleLocalized = %v, want zh and en", editor.TitleLocalized)
			}
		})
	}
}

// readContributionManifestForTest decodes a v2 manifest and resolves its
// contribution points without the runtime-entry checks, which need build output.
func readContributionManifestForTest(t *testing.T, root string) Manifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var wire manifestV2
	if err := decodeStrictJSON(data, &wire, true); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest(wire)
	if err := resolveManifestContributionPoints(root, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}
