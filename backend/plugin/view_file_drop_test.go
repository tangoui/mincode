package plugin

import (
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeFileDrop(t *testing.T) {
	paths := &ManifestFileDrop{Mode: " paths ", Extensions: []string{".JPG", ".jpg", " .png "}, Directories: true}
	if err := normalizeFileDrop(paths); err != nil {
		t.Fatal(err)
	}
	if want := (&ManifestFileDrop{Mode: "paths", Extensions: []string{".jpg", ".png"}, Directories: true, MaxEntries: fileDropDefaultPathLimit}); !reflect.DeepEqual(paths, want) {
		t.Fatalf("got %+v, want %+v", paths, want)
	}
	contents := &ManifestFileDrop{Mode: "contents"}
	if err := normalizeFileDrop(contents); err != nil || contents.MaxEntries != fileDropMaxContentEntries {
		t.Fatalf("contents defaults: %+v %v", contents, err)
	}
	if err := normalizeFileDrop(nil); err != nil {
		t.Fatal(err)
	}
	tooManyExtensions := make([]string, fileDropMaxExtensions+1)
	for index := range tooManyExtensions {
		tooManyExtensions[index] = ".x"
	}
	for name, drop := range map[string]ManifestFileDrop{
		"missing mode":         {},
		"unknown mode":         {Mode: "urls"},
		"contents folders":     {Mode: "contents", Directories: true},
		"contents over limit":  {Mode: "contents", MaxEntries: fileDropMaxContentEntries + 1},
		"paths over limit":     {Mode: "paths", MaxEntries: fileDropMaxPathEntries + 1},
		"negative limit":       {Mode: "paths", MaxEntries: -1},
		"extension no dot":     {Mode: "paths", Extensions: []string{"png"}},
		"extension with path":  {Mode: "paths", Extensions: []string{"./png"}},
		"too many extensions":  {Mode: "paths", Extensions: tooManyExtensions},
		"extension too long":   {Mode: "paths", Extensions: []string{"." + strings.Repeat("a", 17)}},
		"extension wildcard":   {Mode: "paths", Extensions: []string{".*"}},
		"extension whitespace": {Mode: "paths", Extensions: []string{". png"}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := normalizeFileDrop(&drop); err == nil {
				t.Fatalf("accepted %+v", drop)
			}
		})
	}
	original := &ManifestFileDrop{Mode: "paths", Extensions: []string{".pdf"}, MaxEntries: 5}
	cloned := cloneFileDrop(original)
	cloned.Extensions[0] = ".png"
	if original.Extensions[0] != ".pdf" || cloneFileDrop(nil) != nil {
		t.Fatal("clone shares state")
	}
}

func TestReadManifestV2ValidatesViewFileDrop(t *testing.T) {
	root := writeTestManifestV2(t, func(manifest *Manifest) {
		manifest.Contributes.Views[0].FileDrop = &ManifestFileDrop{Mode: "paths", Extensions: []string{".PDF"}, Directories: true}
	})
	manifest, err := readManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if drop := manifest.Contributes.Views[0].FileDrop; drop == nil || drop.Extensions[0] != ".pdf" || drop.MaxEntries != fileDropDefaultPathLimit {
		t.Fatalf("fileDrop not normalized: %+v", drop)
	}
	root = writeTestManifestV2(t, func(manifest *Manifest) {
		manifest.Contributes.Views[0].FileDrop = &ManifestFileDrop{Mode: "contents", Directories: true}
	})
	if _, err := readManifest(root); err == nil || !strings.Contains(err.Error(), "fileDrop") {
		t.Fatalf("expected fileDrop rejection, got %v", err)
	}
}

func TestBeginUIReturnsFileDropOutsideDocumentMode(t *testing.T) {
	fixture := newCustomEditorFixture(t, customEditorAccessRead)
	fixture.record.manifest.Contributes.Views[0].FileDrop = &ManifestFileDrop{Mode: FileDropModePaths, Extensions: []string{".drawio"}, MaxEntries: 5}
	plain, err := fixture.manager.BeginUI("draw.plugin", "canvas", "tango://app:9250")
	if err != nil {
		t.Fatal(err)
	}
	if plain.FileDrop == nil || plain.FileDrop.Mode != FileDropModePaths || plain.FileDrop.MaxEntries != 5 {
		t.Fatalf("plain session fileDrop = %+v", plain.FileDrop)
	}
	fixture.write(t, "a.drawio", "abc")
	document := fixture.mustBegin(t, "a.drawio")
	if document.Session.FileDrop != nil {
		t.Fatalf("document session accepts drops: %+v", document.Session.FileDrop)
	}
}
