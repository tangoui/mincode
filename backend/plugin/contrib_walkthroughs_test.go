package plugin

import (
	"encoding/json"
	"strings"
	"testing"
)

const walkthroughTestSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"><path d="M2 2h12v12H2z"/></svg>`

func writeWalkthroughPackage(t *testing.T, process bool, entries []any, files map[string]string) string {
	t.Helper()
	return writeKeymapPackage(t, "", func(manifest map[string]any) {
		manifest["id"] = "acme-guide"
		contributes := map[string]any{"walkthroughs": entries}
		if process {
			manifest["runtime"] = map[string]any{"type": "process", "entry": "bin/plugin.mjs"}
			manifest["capabilities"] = []string{"commands"}
			contributes["commands"] = []any{map[string]any{"id": "acme.open", "title": "Open Acme", "action": "open"}}
		}
		manifest["contributes"] = contributes
	}, nil, files)
}

func walkthroughFiles(t *testing.T) map[string]string {
	return map[string]string{
		"bin/plugin.mjs":              "process.stdin.resume();\n",
		"walkthroughs/connect.md":     "\ufeff# Connect\r\n\r\nSee [docs](https://example.com).\r\n",
		"walkthroughs/connect.zh.md":  "# 连接\n",
		"media/connect.svg":           walkthroughTestSVG,
		"assets/diagram.png":          fileIconTestPNG(t, 32, 32),
		"walkthroughs/bidi.md":        "hello \u202e world",
		"walkthroughs/zero-width.md":  "a\u200bb",
		"media/script.svg":            `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"><script>alert(1)</script></svg>`,
		"walkthroughs/fake-image.svg": "not svg",
		"other/outside.md":            "# Outside\n",
	}
}

func walkthroughEntry() map[string]any {
	return map[string]any{
		"id": "getting-started", "title": " Get started ", "description": "Learn the basics",
		"titleLocalized": map[string]any{"zh": "入门"}, "featured": true,
		"steps": []any{
			map[string]any{
				"id": "connect", "title": "Connect", "markdown": "walkthroughs/connect.md",
				"markdownLocalized": map[string]any{"zh": "walkthroughs/connect.zh.md"},
				"image":             "media/connect.svg", "imageAlt": "Connection diagram",
			},
			map[string]any{"id": "diagram", "title": "Diagram", "text": "Plain\ntext", "image": "assets/diagram.png", "imageAlt": "Diagram"},
		},
	}
}

func TestWalkthroughsDeclarativeSnapshot(t *testing.T) {
	useTemporaryPluginData(t)
	root := writeWalkthroughPackage(t, false, []any{walkthroughEntry()}, walkthroughFiles(t))
	manifest, err := readManifestStrict(root)
	if err != nil {
		t.Fatal(err)
	}
	resolved := manifest.contribution(walkthroughPointKey)
	if resolved == nil || len(resolved.Entries) != 1 {
		t.Fatalf("resolved = %+v", resolved)
	}
	walkthrough := resolved.Entries[0].(WalkthroughContribution)
	if walkthrough.Title != "Get started" || !walkthrough.Featured || len(walkthrough.Steps) != 2 {
		t.Fatalf("walkthrough = %+v", walkthrough)
	}
	connect := walkthrough.Steps[0]
	if len(connect.Markdown) != 64 || len(connect.MarkdownLocalized["zh"]) != 64 || connect.ImageMime != contributionImageMimeSVG || connect.ImageAlt != "Connection diagram" {
		t.Fatalf("connect = %+v", connect)
	}
	if asset := resolved.Assets[connect.Markdown]; asset.Mime != walkthroughMarkdownMime {
		t.Fatalf("markdown asset = %+v", asset)
	}
	canonical, _, err := validateWalkthroughAsset(walkthroughMarkdownHint, ".md", []byte("\ufeff# Connect\r\n\r\nSee [docs](https://example.com).\r\n"))
	if err != nil || strings.Contains(string(canonical), "\r") || strings.HasPrefix(string(canonical), "\ufeff") || contributionAssetKey(canonical) != connect.Markdown {
		t.Fatalf("canonical markdown = %q, %v", canonical, err)
	}
	if diagram := walkthrough.Steps[1]; diagram.Text != "Plain\ntext" || diagram.ImageMime != contributionImageMimePNG {
		t.Fatalf("diagram = %+v", diagram)
	}
	if len(resolved.Assets) != 4 {
		t.Fatalf("assets = %+v", resolved.Assets)
	}
	snapshot, _ := json.Marshal(resolved.Entries)
	if strings.Contains(string(snapshot), "walkthroughs/") || strings.Contains(string(snapshot), "# Connect") {
		t.Fatalf("snapshot must carry keys only: %s", snapshot)
	}
}

func TestWalkthroughsProcessCommand(t *testing.T) {
	useTemporaryPluginData(t)
	entry := walkthroughEntry()
	entry["steps"] = []any{map[string]any{"id": "open", "title": "Open", "text": "Open it.", "command": "acme.open"}}
	root := writeWalkthroughPackage(t, true, []any{entry}, walkthroughFiles(t))
	manifest, err := readManifestStrict(root)
	if err != nil {
		t.Fatal(err)
	}
	walkthrough := manifest.contribution(walkthroughPointKey).Entries[0].(WalkthroughContribution)
	if walkthrough.Steps[0].Command != "acme.open" {
		t.Fatalf("step = %+v", walkthrough.Steps[0])
	}
}

func TestWalkthroughsRejectInvalidEntries(t *testing.T) {
	useTemporaryPluginData(t)
	step := func(mutate func(step map[string]any)) func(entry map[string]any) {
		return func(entry map[string]any) {
			first := entry["steps"].([]any)[0].(map[string]any)
			mutate(first)
		}
	}
	cases := []struct {
		name    string
		process bool
		mutate  func(entry map[string]any)
		want    string
	}{
		{"markdown and text", false, step(func(s map[string]any) { s["text"] = "both" }), "exactly one of markdown or text"},
		{"neither body", false, step(func(s map[string]any) { delete(s, "markdown"); delete(s, "markdownLocalized") }), "exactly one of markdown or text"},
		{"localized without markdown", false, step(func(s map[string]any) { delete(s, "markdown"); s["text"] = "x" }), "markdownLocalized requires markdown"},
		{"image without alt", false, step(func(s map[string]any) { delete(s, "imageAlt") }), "image requires imageAlt"},
		{"alt without image", false, step(func(s map[string]any) { delete(s, "image") }), "imageAlt requires image"},
		{"svg script", false, step(func(s map[string]any) { s["image"] = "media/script.svg" }), "image"},
		{"bidi markdown", false, step(func(s map[string]any) { s["markdown"] = "walkthroughs/bidi.md" }), "U+202E"},
		{"zero width markdown", false, step(func(s map[string]any) { s["markdown"] = "walkthroughs/zero-width.md" }), "U+200B"},
		{"missing markdown", false, step(func(s map[string]any) { s["markdown"] = "walkthroughs/missing.md" }), "missing.md"},
		{"markdown outside dirs", false, step(func(s map[string]any) { s["markdown"] = "other/outside.md" }), "under walkthroughs/"},
		{"markdown pointing at svg", false, step(func(s map[string]any) { s["markdown"] = "media/connect.svg" }), ".md"},
		{"image pointing at markdown", false, step(func(s map[string]any) { s["image"] = "walkthroughs/connect.md" }), ".png or .svg"},
		{"command on declarative", false, step(func(s map[string]any) { s["command"] = "acme.open" }), "requires runtime.type process"},
		{"foreign command", true, step(func(s map[string]any) { s["command"] = "other.open" }), "not one of this plugin's contributes.commands"},
		{"duplicate step", false, func(e map[string]any) {
			steps := e["steps"].([]any)
			steps[1].(map[string]any)["id"] = "connect"
		}, "repeats step id"},
		{"no steps", false, func(e map[string]any) { e["steps"] = []any{} }, "needs 1-16 steps"},
		{"bad id", false, func(e map[string]any) { e["id"] = "Bad Id" }, "must match"},
		{"control title", false, func(e map[string]any) { e["title"] = "a\u0007b" }, "title"},
		{"unknown field", false, func(e map[string]any) { e["html"] = "<b>" }, "unknown field"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entry := walkthroughEntry()
			tc.mutate(entry)
			root := writeWalkthroughPackage(t, tc.process, []any{entry}, walkthroughFiles(t))
			if _, err := readManifestStrict(root); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			manifest, err := readManifest(root)
			if err != nil {
				t.Fatalf("lenient read: %v", err)
			}
			if resolved := manifest.contribution(walkthroughPointKey); resolved == nil || len(resolved.Entries) != 0 || len(resolved.Issues) != 1 {
				t.Fatalf("lenient resolved = %+v", resolved)
			}
		})
	}
}

func TestWalkthroughsSingleFeatured(t *testing.T) {
	useTemporaryPluginData(t)
	second := walkthroughEntry()
	second["id"] = "advanced"
	root := writeWalkthroughPackage(t, false, []any{walkthroughEntry(), second}, walkthroughFiles(t))
	if _, err := readManifestStrict(root); err == nil || !strings.Contains(err.Error(), "only one walkthrough may be featured") {
		t.Fatalf("err = %v", err)
	}
	manifest, err := readManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if resolved := manifest.contribution(walkthroughPointKey); len(resolved.Entries) != 1 || len(resolved.Issues) != 1 {
		t.Fatalf("resolved = %+v", resolved)
	}
}

func TestWalkthroughAssetRevalidation(t *testing.T) {
	if _, _, err := validateWalkthroughAsset(walkthroughImageHint, ".md", []byte("# x")); err == nil {
		t.Fatal("image hint must not accept markdown")
	}
	if _, _, err := validateWalkthroughAsset(walkthroughMarkdownHint, ".md", []byte("a\rb")); err == nil {
		t.Fatal("lone CR must be rejected")
	}
	if _, _, err := validateWalkthroughAsset(walkthroughMarkdownHint, ".md", []byte(strings.Repeat("a", walkthroughMarkdownMaxBytes+1))); err == nil {
		t.Fatal("oversized markdown must be rejected")
	}
	if _, _, err := validateWalkthroughAsset(walkthroughMarkdownHint, ".md", []byte{0xff, 0xfe}); err == nil {
		t.Fatal("invalid UTF-8 must be rejected")
	}
	if _, mime, err := validateWalkthroughAsset(walkthroughMarkdownHint, ".md", []byte("ok\n")); err != nil || mime != walkthroughMarkdownMime {
		t.Fatalf("mime = %q, %v", mime, err)
	}
}
