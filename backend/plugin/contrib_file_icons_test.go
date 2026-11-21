package plugin

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	fileIconTestSVG       = `<?xml version="1.0"?><svg viewBox="0 0 16 16" xmlns="http://www.w3.org/2000/svg"><path d="M2 2h12v12H2z" fill="#39f"/></svg>`
	fileIconTestSVGFolder = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"><rect width="16" height="12" y="2" rx="1"/></svg>`
)

func fileIconTestPNG(t *testing.T, width, height int) string {
	t.Helper()
	canvas := image.NewNRGBA(image.Rect(0, 0, width, height))
	canvas.Set(0, 0, color.NRGBA{R: 255, A: 255})
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, canvas); err != nil {
		t.Fatal(err)
	}
	return buffer.String()
}

func writeFileIconPackage(t *testing.T, entries []any, files map[string]string) string {
	t.Helper()
	return writeKeymapPackage(t, "", func(manifest map[string]any) {
		manifest["id"] = "acme-icons"
		manifest["contributes"] = map[string]any{"fileIcons": entries}
	}, nil, files)
}

func acmeFileIconEntry() map[string]any {
	return map[string]any{
		"id": "acme-icons.acme", "title": " Acme Icons ", "titleLocalized": map[string]any{"zh": "Acme 图标"},
		"iconDefinitions": map[string]any{
			"file":        map[string]any{"path": "fileicons/file.svg"},
			"ts":          map[string]any{"path": "fileicons/ts.svg"},
			"ts-light":    map[string]any{"path": "icons/ts.png"},
			"folder":      map[string]any{"path": "fileicons/folder.svg"},
			"folder-open": map[string]any{"path": "fileicons/folder.svg"},
		},
		"file": "file", "folder": "folder", "folderExpanded": "folder-open",
		"fileExtensions":      map[string]any{"TS": "ts", "d.ts": "ts"},
		"fileNames":           map[string]any{"Package.json": "file"},
		"folderNames":         map[string]any{"src": "folder"},
		"folderNamesExpanded": map[string]any{"src": "folder-open"},
		"light":               map[string]any{"fileExtensions": map[string]any{"ts": "ts-light"}},
	}
}

func acmeFileIconFiles(t *testing.T) map[string]string {
	return map[string]string{
		"fileicons/file.svg":   fileIconTestSVG,
		"fileicons/ts.svg":     strings.Replace(fileIconTestSVG, "#39f", "#3178c6", 1),
		"fileicons/folder.svg": fileIconTestSVGFolder,
		"icons/ts.png":         fileIconTestPNG(t, 16, 16),
	}
}

func TestFileIconsDeclarativePackageSnapshot(t *testing.T) {
	useTemporaryPluginData(t)
	files := acmeFileIconFiles(t)
	entryJSON, _ := json.Marshal(acmeFileIconEntry())
	files["fileicons/acme.json"] = string(entryJSON)
	root := writeFileIconPackage(t, []any{map[string]any{"path": "fileicons/acme.json"}}, files)
	inspection, err := inspectInstallDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	resolved := inspection.Manifest.contribution(fileIconPointKey)
	if resolved == nil || len(resolved.Entries) != 1 || len(resolved.Issues) != 0 {
		t.Fatalf("resolved = %+v", resolved)
	}
	theme := resolved.Entries[0].(FileIconThemeContribution)
	if theme.Title != "Acme Icons" || theme.File != "file" || theme.FolderExpanded != "folder-open" || theme.Monochrome {
		t.Fatalf("theme = %+v", theme)
	}
	if theme.FileExtensions["ts"] != "ts" || theme.FileExtensions["d.ts"] != "ts" || theme.FileNames["package.json"] != "file" {
		t.Fatalf("mapping keys must be lower-cased: %+v", theme)
	}
	if theme.Light == nil || theme.Light.FileExtensions["ts"] != "ts-light" {
		t.Fatalf("light = %+v", theme.Light)
	}
	if len(theme.Icons) != 5 || theme.Icons["folder"] != theme.Icons["folder-open"] {
		t.Fatalf("icons = %+v", theme.Icons)
	}
	// folder.svg is shared: 4 distinct canonical assets.
	if len(resolved.Assets) != 4 {
		t.Fatalf("assets = %+v", resolved.Assets)
	}
	for _, key := range theme.Icons {
		if len(key) != 64 {
			t.Fatalf("icon key %q is not a content hash", key)
		}
	}
	if asset := resolved.Assets[theme.Icons["ts-light"]]; asset.Mime != fileIconMimePNG {
		t.Fatalf("png asset = %+v", asset)
	}
	canonical, _, err := validateFileIconAsset(fileIconAssetHint, ".svg", []byte(fileIconTestSVG))
	if err != nil || theme.Icons["file"] != contributionAssetKey(canonical) || strings.Contains(string(canonical), "<?xml") {
		t.Fatalf("svg canonical err = %v, %s", err, canonical)
	}
	snapshot, _ := json.Marshal(resolved.Entries)
	if strings.Contains(string(snapshot), "fileicons/") || strings.Contains(string(snapshot), "<svg") {
		t.Fatalf("snapshot must carry keys only: %s", snapshot)
	}
}

func TestFileIconsThemeCountBoundary(t *testing.T) {
	for _, referenced := range []bool{false, true} {
		for _, count := range []int{6, 7} {
			t.Run(fmt.Sprintf("referenced=%t/count=%d", referenced, count), func(t *testing.T) {
				files := acmeFileIconFiles(t)
				entries := make([]any, 0, count)
				for index := 0; index < count; index++ {
					entry := acmeFileIconEntry()
					entry["id"] = fmt.Sprintf("acme-icons.theme%d", index)
					if referenced {
						path := fmt.Sprintf("fileicons/theme%d.json", index)
						encoded, err := json.Marshal(entry)
						if err != nil {
							t.Fatal(err)
						}
						files[path] = string(encoded)
						entries = append(entries, map[string]any{"path": path})
					} else {
						entries = append(entries, entry)
					}
				}
				inspection, err := inspectInstallDirectory(writeFileIconPackage(t, entries, files))
				if count == 7 {
					if err == nil || !strings.Contains(err.Error(), "contributes.fileIcons must contain at most 6") {
						t.Fatalf("seven themes: err = %v", err)
					}
					return
				}
				if err != nil {
					t.Fatalf("six themes rejected: %v", err)
				}
				resolved := inspection.Manifest.contribution(fileIconPointKey)
				if resolved == nil || len(resolved.Entries) != 6 || len(resolved.Issues) != 0 || len(resolved.Assets) != 4 {
					t.Fatalf("six themes snapshot = %+v", resolved)
				}
			})
		}
	}
}

func TestOfficialFileIconsSixThemesPreflight(t *testing.T) {
	root := filepath.Join("..", "..", "official-plugins", "file-icons")
	inspection, err := inspectInstallDirectory(root)
	if err != nil {
		t.Fatalf("official file-icons preflight: %v", err)
	}
	resolved := inspection.Manifest.contribution(fileIconPointKey)
	if !inspection.Manifest.isDeclarative() || resolved == nil || len(resolved.Entries) != 6 || len(resolved.Issues) != 0 {
		t.Fatalf("official file-icons must resolve six declarative themes without issues: %+v", resolved)
	}
	ids := make(map[string]bool)
	for _, entry := range resolved.Entries {
		theme := entry.(FileIconThemeContribution)
		ids[theme.ID] = true
		if len(theme.Icons) == 0 {
			t.Fatalf("theme %s has no canonical assets", theme.ID)
		}
		for _, key := range theme.Icons {
			if _, ok := resolved.Assets[key]; len(key) != 64 || !ok {
				t.Fatalf("theme %s has unresolved asset %q", theme.ID, key)
			}
		}
	}
	for _, style := range []string{"color", "mono", "classic", "compact", "material", "outline"} {
		if !ids["file-icons."+style] {
			t.Errorf("missing official theme file-icons.%s", style)
		}
	}
	snapshot, err := json.Marshal(resolved.Entries)
	if err != nil || len(snapshot) > fileIconSnapshotBytesPerPlug {
		t.Fatalf("official snapshot size = %d, err = %v", len(snapshot), err)
	}
	t.Logf("preflight: six themes, %d canonical assets, %d snapshot bytes", len(resolved.Assets), len(snapshot))
}

func TestFileIconsRejectInvalidEntries(t *testing.T) {
	useTemporaryPluginData(t)
	files := acmeFileIconFiles(t)
	files["fileicons/script.svg"] = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"><script>alert(1)</script></svg>`
	files["fileicons/onload.svg"] = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1" onload="alert(1)"/>`
	files["fileicons/external.svg"] = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"><use href="https://evil.example/a.svg#x"/></svg>`
	files["fileicons/big.png"] = fileIconTestPNG(t, 257, 16)
	files["fileicons/fake.png"] = "not a png"
	files["fileicons/icon.gif"] = "GIF89a"
	files["other/file.svg"] = fileIconTestSVG
	with := func(mutate func(entry map[string]any)) map[string]any {
		entry := acmeFileIconEntry()
		mutate(entry)
		return entry
	}
	icon := func(path string) func(map[string]any) {
		return func(entry map[string]any) {
			entry["iconDefinitions"].(map[string]any)["file"] = map[string]any{"path": path}
		}
	}
	cases := []struct {
		name  string
		entry map[string]any
		want  string
	}{
		{"foreign id", with(func(e map[string]any) { e["id"] = "other.acme" }), `must start with "acme-icons."`},
		{"blank title", with(func(e map[string]any) { e["title"] = " " }), "title"},
		{"bidi title", with(func(e map[string]any) { e["title"] = "a\u202eb" }), "bidirectional"},
		{"unknown field", with(func(e map[string]any) { e["languageIds"] = map[string]any{"go": "file"} }), "unknown field"},
		{"missing file default", with(func(e map[string]any) { delete(e, "file") }), `default "file"`},
		{"no definitions", with(func(e map[string]any) { e["iconDefinitions"] = map[string]any{} }), "iconDefinitions"},
		{"bad definition id", with(func(e map[string]any) {
			e["iconDefinitions"].(map[string]any)["bad id"] = map[string]any{"path": "fileicons/file.svg"}
		}), "icon definition id"},
		{"unknown reference", with(func(e map[string]any) { e["fileNames"] = map[string]any{"x": "nope"} }), "unknown icon definition"},
		{"unknown light reference", with(func(e map[string]any) { e["light"] = map[string]any{"folder": "nope"} }), "light.folder"},
		{"leading dot", with(func(e map[string]any) { e["fileExtensions"] = map[string]any{".ts": "ts"} }), "leading dot"},
		{"path in name", with(func(e map[string]any) { e["fileNames"] = map[string]any{"a/b": "file"} }), "path separators"},
		{"case duplicate", with(func(e map[string]any) { e["folderNames"] = map[string]any{"Src": "folder", "src": "folder"} }), "case-insensitive"},
		{"svg script", with(icon("fileicons/script.svg")), "<script> is not allowed"},
		{"svg onload", with(icon("fileicons/onload.svg")), "event handler"},
		{"svg external", with(icon("fileicons/external.svg")), "svg: attribute href"},
		{"png too big", with(icon("fileicons/big.png")), "257x16"},
		{"fake png", with(icon("fileicons/fake.png")), "not a PNG"},
		{"gif", with(icon("fileicons/icon.gif")), ".svg or .png"},
		{"outside dirs", with(icon("other/file.svg")), "fileicons/"},
		{"missing asset", with(icon("fileicons/missing.svg")), "missing.svg"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := writeFileIconPackage(t, []any{tc.entry}, files)
			_, err := inspectInstallDirectory(root)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "invalid fileIcons contribution") {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}

	root := writeFileIconPackage(t, []any{acmeFileIconEntry(), acmeFileIconEntry()}, files)
	if _, err := inspectInstallDirectory(root); err == nil || !strings.Contains(err.Error(), `duplicate file icon theme id "acme-icons.acme"`) {
		t.Fatalf("duplicate err = %v", err)
	}
}

func TestFileIconsLenientSkipsBadThemes(t *testing.T) {
	useTemporaryPluginData(t)
	files := acmeFileIconFiles(t)
	files["fileicons/script.svg"] = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"><script/></svg>`
	bad := acmeFileIconEntry()
	bad["id"] = "acme-icons.bad"
	bad["iconDefinitions"] = map[string]any{"file": map[string]any{"path": "fileicons/script.svg"}, "only": map[string]any{"path": "fileicons/folder.svg"}}
	bad["folder"], bad["folderExpanded"] = "only", "only"
	delete(bad, "fileExtensions")
	delete(bad, "light")
	delete(bad, "folderNamesExpanded")
	delete(bad, "folderNames")
	root := writeFileIconPackage(t, []any{acmeFileIconEntry(), bad}, files)
	manifest, err := readManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	resolved := manifest.contribution(fileIconPointKey)
	if resolved == nil || len(resolved.Entries) != 1 || len(resolved.Issues) != 1 || resolved.Issues[0].ID != "acme-icons.bad" {
		t.Fatalf("resolved = %+v", resolved)
	}
	if len(resolved.Assets) != 4 {
		t.Fatalf("assets of the skipped theme must not be servable: %+v", resolved.Assets)
	}
}

func TestFileIconsFinalizeSnapshotBudget(t *testing.T) {
	large := FileIconThemeContribution{ID: "p.a", FileNames: map[string]string{}}
	for index := 0; index < 2000; index++ {
		large.FileNames[fmt.Sprintf("%04d-%s", index, strings.Repeat("n", 96))] = strings.Repeat("d", 64)
	}
	encoded, _ := json.Marshal(large)
	if len(encoded) > fileIconSnapshotBytesPerPlug || 2*len(encoded) <= fileIconSnapshotBytesPerPlug {
		t.Fatalf("fixture size %d does not straddle the budget", len(encoded))
	}
	entries := []contributionEntry{{Index: 0, ID: "p.a", Value: large}, {Index: 1, ID: "p.b", Value: large}}
	kept, issues := finalizeFileIconEntries(nil, entries, nil)
	if len(kept) != 1 || len(issues) != 1 || issues[0].ID != "p.b" {
		t.Fatalf("kept = %d issues = %+v", len(kept), issues)
	}
}

func pngChunk(kind string, data []byte) []byte {
	var chunk bytes.Buffer
	_ = binary.Write(&chunk, binary.BigEndian, uint32(len(data)))
	chunk.WriteString(kind)
	chunk.Write(data)
	_ = binary.Write(&chunk, binary.BigEndian, crc32.ChecksumIEEE(append([]byte(kind), data...)))
	return chunk.Bytes()
}

func TestFileIconsPNGCanonicalization(t *testing.T) {
	source := []byte(fileIconTestPNG(t, 16, 16))
	// Insert an ancillary tEXt chunk before IEND; the canonical PNG drops it.
	withText := append(append(append([]byte{}, source[:len(source)-12]...), pngChunk("tEXt", []byte("Comment\x00<script>"))...), source[len(source)-12:]...)
	if _, err := png.Decode(bytes.NewReader(withText)); err != nil {
		t.Fatalf("fixture png does not decode: %v", err)
	}
	canonical, mime, err := validateFileIconAsset(fileIconAssetHint, ".png", withText)
	if err != nil || mime != fileIconMimePNG {
		t.Fatalf("png err = %v mime = %q", err, mime)
	}
	if _, err := png.Decode(bytes.NewReader(canonical)); err != nil {
		t.Fatalf("canonical png does not decode: %v", err)
	}
	if bytes.Contains(canonical, []byte("tEXt")) || bytes.Contains(canonical, []byte("<script>")) {
		t.Fatal("canonical png kept an ancillary chunk")
	}
	if _, _, err := validateFileIconAsset(fileIconAssetHint, ".png", []byte(fileIconTestPNG(t, 16, 300))); err == nil {
		t.Fatal("oversize png accepted")
	}
	if _, _, err := validateFileIconAsset(fileIconAssetHint, ".jpg", []byte("x")); err == nil {
		t.Fatal("jpg accepted")
	}
}

// TestFileIconsAssetFetchServesCanonicalSVG covers the on-demand fetch:
// canonical (not author) bytes are served with the SVG MIME type and a file
// swapped on disk for a malicious one is never served.
func TestFileIconsAssetFetchServesCanonicalSVG(t *testing.T) {
	useTemporaryPluginData(t)
	manager := newPluginManager()
	root := writeFileIconPackage(t, []any{acmeFileIconEntry()}, acmeFileIconFiles(t))
	inspection, err := inspectInstallDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	manifest := inspection.Manifest
	record := &pluginRecord{id: manifest.ID, source: pluginSourceLocal, root: root, manifest: &manifest, enabled: true, state: pluginStateStopped, scope: pluginScopeGlobal}
	manager.mu.Lock()
	manager.records[record.id] = record
	manager.order = append(manager.order, record.id)
	manager.mu.Unlock()
	t.Cleanup(func() { contributionAssets.purgePlugin(record.id) })

	theme := manifest.contribution(fileIconPointKey).Entries[0].(FileIconThemeContribution)
	key := theme.Icons["file"]
	response, err := manager.ContributionAssets(contributionAssetsRequest{PluginID: record.id, Point: fileIconPointKey, Keys: []string{key}})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Assets) != 1 || response.Assets[0].Mime != fileIconMimeSVG {
		t.Fatalf("response = %+v", response)
	}
	data, _ := base64.StdEncoding.DecodeString(response.Assets[0].Data)
	if !strings.HasPrefix(string(data), `<svg xmlns="http://www.w3.org/2000/svg"`) || strings.Contains(string(data), "<?xml") {
		t.Fatalf("served bytes are not canonical: %s", data)
	}

	evil := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" onload="alert(1)"/>`
	if err := os.WriteFile(filepath.Join(root, "fileicons", "file.svg"), []byte(evil), 0o600); err != nil {
		t.Fatal(err)
	}
	contributionAssets.purgePlugin(record.id)
	response, _ = manager.ContributionAssets(contributionAssetsRequest{PluginID: record.id, Point: fileIconPointKey, Keys: []string{key}})
	if len(response.Assets) != 0 {
		t.Fatalf("a swapped malicious SVG was served: %+v", response)
	}
}
