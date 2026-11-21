package plugin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFileTemplatePackage(t *testing.T, entries []any, files map[string]string) string {
	t.Helper()
	return writeKeymapPackage(t, "", func(manifest map[string]any) {
		manifest["id"] = "file-template-test"
		manifest["contributes"] = map[string]any{"fileTemplates": entries}
	}, nil, files)
}

func fileTemplateEntry(id, kind string, files ...[2]string) map[string]any {
	list := make([]any, 0, len(files))
	for _, file := range files {
		list = append(list, map[string]any{"path": file[0], "source": file[1]})
	}
	return map[string]any{"id": id, "kind": kind, "title": "Title " + id, "files": list}
}

func fileTemplateFiles() map[string]string {
	return map[string]string{
		"templates/component.vue.tmpl": "\ufeff<template>{{mincode.namePascal}} {{ other }} {{mincode}}</template>\r\n",
		"templates/go/go.mod.tmpl":     "module {{mincode.nameKebab}}\n",
		"assets/templates/main.tmpl":   "package main\n\t// {{mincode.date}}\f\n",
	}
}

func TestFileTemplatesDeclarativePackageAndSnapshot(t *testing.T) {
	useTemporaryPluginData(t)
	component := fileTemplateEntry("component", "file", [2]string{"src/{{mincode.namePascal}}.vue", "templates/component.vue.tmpl"})
	component["category"] = "Vue"
	component["defaultName"] = "MyComponent"
	component["titleLocalized"] = map[string]string{"zh": "组件"}
	service := fileTemplateEntry("service", "project",
		[2]string{"go.mod", "templates/go/go.mod.tmpl"},
		[2]string{"cmd/{{mincode.nameKebab}}/main.go", "assets/templates/main.tmpl"},
		[2]string{"cmd/{{mincode.nameKebab}}/copy.go", "assets/templates/main.tmpl"},
	)
	root := writeFileTemplatePackage(t, []any{component, service}, fileTemplateFiles())
	inspection, err := inspectInstallDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	items := inspection.Manifest.fileTemplates()
	if len(items) != 2 || items[0].Category != "Vue" || items[0].DefaultName != "MyComponent" || len(items[1].Files) != 3 {
		t.Fatalf("items = %+v", items)
	}
	first := items[0].Files[0]
	if len(first.Source) != 64 || first.Size != int64(len("<template>{{mincode.namePascal}} {{ other }} {{mincode}}</template>\r\n")) {
		t.Fatalf("file = %+v", first)
	}
	if items[1].Files[1].Source != items[1].Files[2].Source {
		t.Fatal("shared sources must hash to the same key")
	}

	manager := newPluginManager()
	manifest, err := manager.InstallLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetEnabled(context.Background(), manifest.ID, true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { contributionAssets.purgePlugin(manifest.ID) })
	snapshot := manager.Contributions([]string{fileTemplatePointKey})
	if len(snapshot.Plugins) != 1 || len(snapshot.Plugins[0].Points[fileTemplatePointKey].Entries) != 2 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	encoded, _ := json.Marshal(snapshot.Plugins[0].Points[fileTemplatePointKey].Entries[0])
	if !strings.Contains(string(encoded), `"path":"src/{{mincode.namePascal}}.vue","source":"`+first.Source+`"`) || strings.Contains(string(encoded), "templates/component") {
		t.Fatalf("snapshot entry = %s", encoded)
	}
	response, err := manager.ContributionAssets(contributionAssetsRequest{PluginID: manifest.ID, Point: fileTemplatePointKey, Keys: []string{first.Source}})
	if err != nil || len(response.Assets) != 1 || response.Assets[0].Mime != fileTemplateMime {
		t.Fatalf("assets = %+v err = %v", response, err)
	}
	data, _ := base64.StdEncoding.DecodeString(response.Assets[0].Data)
	if strings.HasPrefix(string(data), "\ufeff") || !strings.HasPrefix(string(data), "<template>") {
		t.Fatalf("served bytes = %q", data)
	}
}

func TestFileTemplatesEntryValidation(t *testing.T) {
	src := "templates/a.tmpl"
	cases := map[string]struct {
		entry map[string]any
		want  string
	}{
		"bad id":           {fileTemplateEntry("Bad", "file", [2]string{"a.txt", src}), "must match"},
		"bad kind":         {fileTemplateEntry("a", "folder", [2]string{"a.txt", src}), "kind"},
		"file two files":   {fileTemplateEntry("a", "file", [2]string{"a.txt", src}, [2]string{"b.txt", src}), "exactly one"},
		"project no files": {fileTemplateEntry("a", "project"), "empty"},
		"hook field": {func() map[string]any {
			e := fileTemplateEntry("a", "file", [2]string{"a.txt", src})
			e["postCreate"] = "npm i"
			return e
		}(), "unknown field"},
		"file hook":       {map[string]any{"id": "a", "kind": "file", "title": "T", "files": []any{map[string]any{"path": "a.sh", "source": src, "mode": "0755"}}}, "unknown field"},
		"absolute":        {fileTemplateEntry("a", "file", [2]string{"/etc/a", src}), "relative"},
		"escape":          {fileTemplateEntry("a", "file", [2]string{"../a.txt", src}), `".."`},
		"backslash":       {fileTemplateEntry("a", "file", [2]string{`a\b.txt`, src}), "relative"},
		"drive":           {fileTemplateEntry("a", "file", [2]string{"C:/a.txt", src}), "relative"},
		"empty segment":   {fileTemplateEntry("a", "file", [2]string{"a//b.txt", src}), "empty"},
		"git dir":         {fileTemplateEntry("a", "file", [2]string{".GIT/hooks/pre-commit", src}), ".GIT"},
		"node modules":    {fileTemplateEntry("a", "file", [2]string{"node_modules/x.js", src}), "node_modules"},
		"sensitive":       {fileTemplateEntry("a", "file", [2]string{"config/.env", src}), "sensitive"},
		"sensitive case":  {fileTemplateEntry("a", "file", [2]string{"ID_RSA", src}), "sensitive"},
		"unknown var":     {fileTemplateEntry("a", "file", [2]string{"{{mincode.home}}.txt", src}), "unknown variable"},
		"space":           {fileTemplateEntry("a", "file", [2]string{"a b.txt", src}), "may only use"},
		"too deep":        {fileTemplateEntry("a", "file", [2]string{strings.Repeat("d/", fileTemplateDepthMax) + "a.txt", src}), "segments"},
		"too long":        {fileTemplateEntry("a", "file", [2]string{strings.Repeat("a", fileTemplatePathMaxLength+1), src}), "bytes"},
		"duplicate path":  {fileTemplateEntry("a", "project", [2]string{"A.txt", src}, [2]string{"a.txt", src}), "twice"},
		"wrong ext":       {fileTemplateEntry("a", "file", [2]string{"a.txt", "templates/a.txt"}), ".tmpl"},
		"outside dirs":    {fileTemplateEntry("a", "file", [2]string{"a.txt", "other/a.tmpl"}), "templates/"},
		"missing source":  {fileTemplateEntry("a", "file", [2]string{"a.txt", "templates/missing.tmpl"}), "missing.tmpl"},
		"bidi content":    {fileTemplateEntry("a", "file", [2]string{"a.txt", "templates/bidi.tmpl"}), "bidirectional"},
		"nul content":     {fileTemplateEntry("a", "file", [2]string{"a.txt", "templates/nul.tmpl"}), "control"},
		"unknown content": {fileTemplateEntry("a", "file", [2]string{"a.txt", "templates/unknown.tmpl"}), "{{mincode.env}}"},
		"invalid utf8":    {fileTemplateEntry("a", "file", [2]string{"a.txt", "templates/latin1.tmpl"}), "UTF-8"},
		"bad default name": {func() map[string]any {
			e := fileTemplateEntry("a", "file", [2]string{"a.txt", src})
			e["defaultName"] = "../x"
			return e
		}(), "defaultName"},
		"long category": {func() map[string]any {
			e := fileTemplateEntry("a", "file", [2]string{"a.txt", src})
			e["category"] = strings.Repeat("c", 25)
			return e
		}(), "category"},
		"missing source f": {map[string]any{"id": "a", "kind": "file", "title": "T", "files": []any{map[string]any{"path": "a.txt"}}}, "source"},
		"title bidi": {func() map[string]any {
			e := fileTemplateEntry("a", "file", [2]string{"a.txt", src})
			e["title"] = "a\u202eb"
			return e
		}(), "title"},
		"dot segment":       {fileTemplateEntry("a", "file", [2]string{"./a.txt", src}), `"."`},
		"surrounding space": {fileTemplateEntry("a", "file", [2]string{" a.txt", src}), "surrounding"},
	}
	files := map[string]string{
		src:                      "hello {{mincode.name}}",
		"templates/a.txt":        "x",
		"other/a.tmpl":           "x",
		"templates/bidi.tmpl":    "if (a \u202e) {}",
		"templates/nul.tmpl":     "a\x00b",
		"templates/unknown.tmpl": "{{mincode.env}}",
		"templates/latin1.tmpl":  "caf\xe9",
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			manifest, err := readManifest(writeFileTemplatePackage(t, []any{testCase.entry}, files))
			if err != nil {
				t.Fatal(err)
			}
			resolved := manifest.contribution(fileTemplatePointKey)
			if len(resolved.Entries) != 0 || len(resolved.Issues) != 1 || !strings.Contains(resolved.Issues[0].Message, testCase.want) {
				t.Fatalf("entries = %d issues = %+v, want %q", len(resolved.Entries), resolved.Issues, testCase.want)
			}
		})
	}
	// At the limits: depth 8, 200-byte path, 64 files.
	project := make([][2]string, 0, fileTemplateFilesMax)
	for index := 0; index < fileTemplateFilesMax; index++ {
		project = append(project, [2]string{fmt.Sprintf("f%02d.txt", index), src})
	}
	deep := strings.Repeat("d/", fileTemplateDepthMax-1) + "a.txt"
	long := strings.Repeat("a", fileTemplatePathMaxLength)
	manifest, err := readManifest(writeFileTemplatePackage(t, []any{
		fileTemplateEntry("many", "project", project...),
		fileTemplateEntry("deep", "file", [2]string{deep, src}),
		fileTemplateEntry("long", "file", [2]string{long, src}),
	}, files))
	if err != nil {
		t.Fatal(err)
	}
	if resolved := manifest.contribution(fileTemplatePointKey); len(resolved.Entries) != 3 || len(resolved.Issues) != 0 {
		t.Fatalf("issues = %+v", resolved.Issues)
	}
	tooMany := append(project, [2]string{"extra.txt", src})
	manifest, _ = readManifest(writeFileTemplatePackage(t, []any{fileTemplateEntry("many", "project", tooMany...)}, files))
	if resolved := manifest.contribution(fileTemplatePointKey); len(resolved.Issues) != 1 || !strings.Contains(resolved.Issues[0].Message, "at most") {
		t.Fatalf("issues = %+v", resolved.Issues)
	}
}

func TestFileTemplatesTemplateTotalAndStrictInstall(t *testing.T) {
	large := strings.Repeat("a", int(fileTemplateSpec.Assets.MaxBytes))
	files := map[string]string{}
	var entryFiles [][2]string
	count := int(fileTemplateMaxBytes/fileTemplateSpec.Assets.MaxBytes) + 1
	for index := 0; index < count; index++ {
		path := fmt.Sprintf("templates/large%d.tmpl", index)
		// Distinct content so every asset counts separately.
		files[path] = large[:len(large)-index]
		entryFiles = append(entryFiles, [2]string{fmt.Sprintf("f%d.txt", index), path})
	}
	manifest, err := readManifest(writeFileTemplatePackage(t, []any{fileTemplateEntry("big", "project", entryFiles...)}, files))
	if err != nil {
		t.Fatal(err)
	}
	resolved := manifest.contribution(fileTemplatePointKey)
	if len(resolved.Entries) != 0 || len(resolved.Issues) != 1 || !strings.Contains(resolved.Issues[0].Message, "total") {
		t.Fatalf("issues = %+v", resolved.Issues)
	}
	root := writeFileTemplatePackage(t, []any{fileTemplateEntry("a", "file", [2]string{".env", "templates/a.tmpl"})}, map[string]string{"templates/a.tmpl": "x"})
	if _, err := inspectInstallDirectory(root); err == nil || !strings.Contains(err.Error(), "invalid fileTemplates contribution") {
		t.Fatalf("strict err = %v", err)
	}
}

func TestFileTemplatesSymlinkSourceRejected(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "outside.tmpl")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := writeFileTemplatePackage(t, []any{fileTemplateEntry("a", "file", [2]string{"a.txt", "templates/link.tmpl"})}, map[string]string{"templates/keep.tmpl": "x"})
	if err := os.Symlink(outside, filepath.Join(root, "templates", "link.tmpl")); err != nil {
		t.Skip("symlinks unavailable")
	}
	manifest, err := readManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if resolved := manifest.contribution(fileTemplatePointKey); len(resolved.Entries) != 0 || len(resolved.Issues) != 1 {
		t.Fatalf("resolved = %+v", resolved)
	}
}
