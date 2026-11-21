package notes

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

var testAssetRefPattern = regexp.MustCompile(`/__notes-assets/[0-9a-f]{64}\.png`)

func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func loadTestTree(t *testing.T) Tree {
	t.Helper()
	return mustDispatch[Tree](t, "notes_tree", map[string]any{})
}

// childFolder 返回 parentID 下名为 name 的唯一文件夹。
func childFolder(t *testing.T, tree Tree, parentID, name string) Folder {
	t.Helper()
	var found []Folder
	for _, folder := range tree.Folders {
		if folder.ParentID == parentID && folder.Name == name {
			found = append(found, folder)
		}
	}
	if len(found) != 1 {
		t.Fatalf("folders named %q under %q = %+v, want exactly 1 (tree %+v)", name, parentID, found, tree.Folders)
	}
	return found[0]
}

func notesIn(tree Tree, folderID string) []NoteSummary {
	var notes []NoteSummary
	for _, note := range tree.Notes {
		if note.FolderID == folderID {
			notes = append(notes, note)
		}
	}
	return notes
}

func onlyNoteIn(t *testing.T, tree Tree, folderID string) Note {
	t.Helper()
	notes := notesIn(tree, folderID)
	if len(notes) != 1 {
		t.Fatalf("notes in %q = %+v, want exactly 1", folderID, notes)
	}
	return mustDispatch[Note](t, "notes_get", map[string]any{"id": notes[0].ID})
}

func skippedReasons(report ImportReport) []string {
	reasons := []string{}
	for _, skipped := range report.Skipped {
		reasons = append(reasons, skipped.Reason)
	}
	return reasons
}

func TestImportFolderKeepsHierarchyAndFrontMatter(t *testing.T) {
	setupNotesDB(t)
	root := filepath.Join(t.TempDir(), "知识库")
	writeTestFile(t, filepath.Join(root, "a", "b", "c.md"), []byte("---\ntitle: 部署\ntags: [x, y]\nfavorite: true\nauthor: someone\n---\n\n正文第一行\n"))
	writeTestFile(t, filepath.Join(root, "a", "readme.txt"), []byte("not markdown"))
	writeTestFile(t, filepath.Join(root, "img", "unused.png"), pngBytes("unused"))

	report := mustDispatch[ImportReport](t, "notes_import_folder", map[string]any{"path": root})
	if report.Notes != 1 || report.Assets != 0 || len(report.Skipped) != 0 {
		t.Fatalf("report = %+v", report)
	}
	tree := loadTestTree(t)
	if len(tree.Folders) != 3 {
		t.Fatalf("folders = %+v, want 知识库/a/b only (no empty img folder)", tree.Folders)
	}
	top := childFolder(t, tree, "", "知识库")
	a := childFolder(t, tree, top.ID, "a")
	b := childFolder(t, tree, a.ID, "b")
	note := onlyNoteIn(t, tree, b.ID)
	if note.Title != "部署" || !note.Favorite || !reflect.DeepEqual(note.Tags, []string{"x", "y"}) {
		t.Fatalf("note = %+v", note.NoteSummary)
	}
	if strings.Contains(note.Body, "---") || strings.Contains(note.Body, "author") || note.Body != "正文第一行\n" {
		t.Fatalf("body = %q", note.Body)
	}
}

func TestImportFolderSkipsUnsafeAssets(t *testing.T) {
	setupNotesDB(t)
	base := t.TempDir()
	root := filepath.Join(base, "root")
	okPNG := pngBytes("ok image")
	writeTestFile(t, filepath.Join(root, "img", "ok.png"), okPNG)
	writeTestFile(t, filepath.Join(base, "outside.png"), pngBytes("outside"))
	writeTestFile(t, filepath.Join(root, "img", "target.png"), pngBytes("symlink target"))
	if err := os.Symlink(filepath.Join(root, "img", "target.png"), filepath.Join(root, "img", "link.png")); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, "img", "fake.png"), []byte("just text pretending to be a png"))
	writeTestFile(t, filepath.Join(root, "img", "big.png"), append(pngBytes("big"), make([]byte, 25<<20)...))
	body := strings.Join([]string{
		"# 图片",
		"![ok](img/ok.png)",
		"![out](../outside.png)",
		"![link](img/link.png)",
		`<img src="img/fake.png" alt="fake">`,
		"![big](img/big.png 'too big')",
		"![remote](https://example.com/a.png)",
		"![inline](data:image/png;base64,AAAA)",
		"```",
		"![in code](img/ok.png)",
		"```",
		"",
	}, "\n")
	writeTestFile(t, filepath.Join(root, "note.md"), []byte(body))

	report := mustDispatch[ImportReport](t, "notes_import_folder", map[string]any{"path": root})
	if report.Notes != 1 || report.Assets != 1 {
		t.Fatalf("report = %+v", report)
	}
	want := []string{"outside_root", "symlink", "asset_type", "asset_too_large"}
	if got := skippedReasons(report); !reflect.DeepEqual(got, want) {
		t.Fatalf("skipped = %+v, want reasons %v", report.Skipped, want)
	}
	if report.Skipped[1].Path != filepath.Join(root, "img", "link.png") {
		t.Fatalf("symlink skipped path = %q", report.Skipped[1].Path)
	}

	tree := loadTestTree(t)
	note := onlyNoteIn(t, tree, childFolder(t, tree, "", "root").ID)
	if note.Title != "图片" {
		t.Fatalf("title = %q", note.Title)
	}
	refs := testAssetRefPattern.FindAllString(note.Body, -1)
	if len(refs) != 1 || !strings.Contains(note.Body, "![ok]("+refs[0]+")") {
		t.Fatalf("body = %q, want only ok.png rewritten", note.Body)
	}
	for _, kept := range []string{"![out](../outside.png)", "![link](img/link.png)", `<img src="img/fake.png" alt="fake">`,
		"![big](img/big.png 'too big')", "https://example.com/a.png", "data:image/png;base64,AAAA", "![in code](img/ok.png)"} {
		if !strings.Contains(note.Body, kept) {
			t.Fatalf("body = %q, want %q unchanged", note.Body, kept)
		}
	}
	if !assetExists(t, refs[0]) {
		t.Fatalf("asset %s not stored", refs[0])
	}
}

func TestImportFolderSkipsSymlinkedMarkdownAndLargeFiles(t *testing.T) {
	setupNotesDB(t)
	base := t.TempDir()
	root := filepath.Join(base, "root")
	writeTestFile(t, filepath.Join(base, "secret.md"), []byte("# secret"))
	writeTestFile(t, filepath.Join(root, "ok.md"), []byte("![](linkdir/x.png)"))
	if err := os.Symlink(filepath.Join(base, "secret.md"), filepath.Join(root, "link.md")); err != nil {
		t.Fatal(err)
	}
	// 中间目录是软链接：目录本身不跟随，经它引用的图片也按 symlink 跳过。
	writeTestFile(t, filepath.Join(base, "elsewhere", "x.png"), pngBytes("via linked dir"))
	writeTestFile(t, filepath.Join(base, "elsewhere", "inner.md"), []byte("inner"))
	if err := os.Symlink(filepath.Join(base, "elsewhere"), filepath.Join(root, "linkdir")); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, "huge.markdown"), make([]byte, maxImportMarkdownBytes+1))
	writeTestFile(t, filepath.Join(root, "binary.md"), []byte("bad \xff utf8"))

	report := mustDispatch[ImportReport](t, "notes_import_folder", map[string]any{"path": root})
	if report.Notes != 1 || report.Assets != 0 || len(report.Skipped) != 5 {
		t.Fatalf("report = %+v", report)
	}
	got := map[string]string{}
	for _, skipped := range report.Skipped {
		got[filepath.Base(skipped.Path)] = skipped.Reason
	}
	want := map[string]string{"link.md": "symlink", "linkdir": "symlink", "x.png": "symlink",
		"huge.markdown": "too_large", "binary.md": "read_error"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("skipped = %+v, want %v", report.Skipped, want)
	}
}

func TestImportFilesResolvesImagesRelativeToFile(t *testing.T) {
	setupNotesDB(t)
	base := t.TempDir()
	dir := filepath.Join(base, "docs")
	writeTestFile(t, filepath.Join(dir, "my img", "a b.png"), pngBytes("spaced"))
	writeTestFile(t, filepath.Join(base, "up.png"), pngBytes("up"))
	writeTestFile(t, filepath.Join(dir, "same.png"), pngBytes("file url"))
	fileURL := "file://" + filepath.ToSlash(filepath.Join(dir, "same.png"))
	body := "![a](my%20img/a%20b.png?raw=1#top)\n![b](../up.png)\n![c](" + fileURL + ")\n![d](missing.png)\n"
	writeTestFile(t, filepath.Join(dir, "plain.md"), []byte(body))
	folder := mustDispatch[Folder](t, "notes_folder_create", map[string]any{"name": "收件箱"})

	report := mustDispatch[ImportReport](t, "notes_import_files", map[string]any{
		"paths":    []string{filepath.Join(dir, "plain.md"), filepath.Join(dir, "absent.md")},
		"folderId": folder.ID,
	})
	if report.Notes != 1 || report.Assets != 2 {
		t.Fatalf("report = %+v", report)
	}
	want := []string{"outside_root", "missing", "missing"}
	if got := skippedReasons(report); !reflect.DeepEqual(got, want) {
		t.Fatalf("skipped = %+v, want %v", report.Skipped, want)
	}
	note := onlyNoteIn(t, loadTestTree(t), folder.ID)
	if note.Title != "plain" {
		t.Fatalf("title = %q, want file name", note.Title)
	}
	if refs := testAssetRefPattern.FindAllString(note.Body, -1); len(refs) != 2 ||
		!strings.Contains(note.Body, "![b](../up.png)") || !strings.Contains(note.Body, "![d](missing.png)") {
		t.Fatalf("body = %q", note.Body)
	}
}

func TestImportUsesHeadingAsTitle(t *testing.T) {
	setupNotesDB(t)
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "deploy.md"), []byte("# 部署步骤\n\n1. 构建\n"))
	writeTestFile(t, filepath.Join(dir, "commas.md"), []byte("---\ntags: \"a, b\"\nfavorite: false\n---\n内容"))
	writeTestFile(t, filepath.Join(dir, "block.md"), []byte("---\r\ntitle: 'It''s: done'\r\ntags:\r\n  - one\r\n  - \"two, three\"\r\n---\r\n# 不是标题\r\n"))

	report := mustDispatch[ImportReport](t, "notes_import_files", map[string]any{
		"paths": []string{filepath.Join(dir, "deploy.md"), filepath.Join(dir, "commas.md"), filepath.Join(dir, "block.md")},
	})
	if report.Notes != 3 || len(report.Skipped) != 0 {
		t.Fatalf("report = %+v", report)
	}
	notes := notesIn(loadTestTree(t), "")
	if len(notes) != 3 {
		t.Fatalf("root notes = %+v", notes)
	}
	if notes[0].Title != "部署步骤" {
		t.Fatalf("heading title = %q", notes[0].Title)
	}
	if notes[1].Title != "commas" || notes[1].Favorite || !reflect.DeepEqual(notes[1].Tags, []string{"a", "b"}) {
		t.Fatalf("comma tags note = %+v", notes[1])
	}
	if notes[2].Title != "It's: done" || !reflect.DeepEqual(notes[2].Tags, []string{"one", "two, three"}) {
		t.Fatalf("block list note = %+v", notes[2])
	}
	if body := mustDispatch[Note](t, "notes_get", map[string]any{"id": notes[2].ID}).Body; body != "# 不是标题\r\n" {
		t.Fatalf("block body = %q", body)
	}
}

func TestImportValidation(t *testing.T) {
	setupNotesDB(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "a.md")
	writeTestFile(t, file, []byte("a"))
	cases := []struct {
		cmd  string
		args map[string]any
		want string
	}{
		{"notes_import_files", map[string]any{"paths": []string{"relative.md"}}, ErrInvalidRequest},
		{"notes_import_files", map[string]any{"paths": []string{filepath.Join(dir, "a.txt")}}, ErrInvalidRequest},
		{"notes_import_files", map[string]any{}, ErrInvalidRequest},
		{"notes_import_files", map[string]any{"paths": []string{file}, "folderId": "missing"}, ErrFolderNotFound},
		{"notes_import_folder", map[string]any{"path": "relative"}, ErrInvalidRequest},
		{"notes_import_folder", map[string]any{"path": file}, ErrImportPath},
		{"notes_import_folder", map[string]any{"path": filepath.Join(dir, "absent")}, ErrImportPath},
	}
	for _, tc := range cases {
		if _, err := dispatch(t, tc.cmd, tc.args); err == nil || err.Error() != tc.want {
			t.Fatalf("%s %v: err = %v, want %s", tc.cmd, tc.args, err, tc.want)
		}
	}
	if tree := loadTestTree(t); len(tree.Notes) != 0 || len(tree.Folders) != 0 {
		t.Fatalf("validation failures created data: %+v", tree)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestExportRewritesAssetsAndNeverOverwrites(t *testing.T) {
	setupNotesDB(t)
	ref := mustPut(t, pngBytes("export image"))
	assetName := strings.TrimPrefix(ref, assetRefPrefix)
	note := mustDispatch[Note](t, "notes_create", map[string]any{"title": "标题", "body": "图：![x](" + ref + ")\n"})
	dest := t.TempDir()
	writeTestFile(t, filepath.Join(dest, "标题.md"), []byte("existing"))

	result := mustDispatch[ExportResult](t, "notes_export", map[string]any{"noteIds": []string{note.ID}, "destDir": dest})
	written := filepath.Join(dest, "标题 (2).md")
	if !reflect.DeepEqual(result.Written, []string{written}) || len(result.MissingAssets) != 0 {
		t.Fatalf("result = %+v", result)
	}
	if got := readTestFile(t, filepath.Join(dest, "标题.md")); got != "existing" {
		t.Fatalf("existing file overwritten: %q", got)
	}
	content := readTestFile(t, written)
	if !strings.Contains(content, "![x](assets/"+assetName+")") || strings.Contains(content, assetRefPrefix) {
		t.Fatalf("exported = %q", content)
	}
	if got := readTestFile(t, filepath.Join(dest, "assets", assetName)); got != string(pngBytes("export image")) {
		t.Fatalf("copied asset = %q", got)
	}

	// 文件夹导出：同名目录、保留层级，子文件夹中的笔记用 ../ 回溯到导出根目录下的 assets/。
	parent := mustDispatch[Folder](t, "notes_folder_create", map[string]any{"name": "运维"})
	child := mustDispatch[Folder](t, "notes_folder_create", map[string]any{"name": "a/b", "parentId": parent.ID})
	mustDispatch[Note](t, "notes_create", map[string]any{"folderId": parent.ID, "title": "", "body": "![](" + ref + ")"})
	mustDispatch[Note](t, "notes_create", map[string]any{"folderId": child.ID, "title": "子:笔记", "body": "<img src=\"" + ref + "\">"})
	result = mustDispatch[ExportResult](t, "notes_export", map[string]any{"folderId": parent.ID, "destDir": dest})
	top := filepath.Join(dest, "运维", "无标题.md")
	nested := filepath.Join(dest, "运维", "a_b", "子_笔记.md")
	if !reflect.DeepEqual(result.Written, []string{top, nested}) {
		t.Fatalf("written = %v", result.Written)
	}
	if got := readTestFile(t, top); got != "![](assets/"+assetName+")" {
		t.Fatalf("top = %q", got)
	}
	if got := readTestFile(t, nested); !strings.Contains(got, `<img src="../assets/`+assetName+`">`) ||
		!strings.Contains(got, "title: \"子:笔记\"\n") {
		t.Fatalf("nested = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dest, "运维", "assets", assetName)); err != nil {
		t.Fatal(err)
	}

	// 再次导出同一文件夹：生成「运维 (2)」，不写入已有目录。
	result = mustDispatch[ExportResult](t, "notes_export", map[string]any{"folderId": parent.ID, "destDir": dest})
	if len(result.Written) != 2 || filepath.Dir(result.Written[0]) != filepath.Join(dest, "运维 (2)") {
		t.Fatalf("second export = %v", result.Written)
	}
}

func TestExportReusesIdenticalAssetAndReportsMissing(t *testing.T) {
	setupNotesDB(t)
	ref := mustPut(t, pngBytes("shared"))
	name := strings.TrimPrefix(ref, assetRefPrefix)
	missing := assetRefPrefix + strings.Repeat("a", 64) + ".png"
	note := mustDispatch[Note](t, "notes_create", map[string]any{"title": "n", "body": "![](" + ref + ") ![](" + missing + ")"})
	dest := t.TempDir()
	writeTestFile(t, filepath.Join(dest, "assets", name), pngBytes("different content"))

	result := mustDispatch[ExportResult](t, "notes_export", map[string]any{"noteIds": []string{note.ID}, "destDir": dest})
	if !reflect.DeepEqual(result.MissingAssets, []string{missing}) {
		t.Fatalf("missing = %v", result.MissingAssets)
	}
	copyName := strings.TrimSuffix(name, ".png") + " (2).png"
	content := readTestFile(t, result.Written[0])
	if !strings.Contains(content, "![](assets/"+strings.ReplaceAll(copyName, " ", "%20")+") ![]("+missing+")") {
		t.Fatalf("content = %q", content)
	}
	if got := readTestFile(t, filepath.Join(dest, "assets", name)); got != string(pngBytes("different content")) {
		t.Fatal("existing asset overwritten")
	}

	// 已有内容相同的附件直接复用，不再生成副本。
	second := mustDispatch[ExportResult](t, "notes_export", map[string]any{"noteIds": []string{note.ID}, "destDir": dest})
	if !strings.Contains(readTestFile(t, second.Written[0]), "assets/"+strings.ReplaceAll(copyName, " ", "%20")) {
		t.Fatalf("second export = %q", readTestFile(t, second.Written[0]))
	}
	entries, err := os.ReadDir(filepath.Join(dest, "assets"))
	if err != nil || len(entries) != 2 {
		t.Fatalf("assets = %v, %v", entries, err)
	}
}

func TestExportValidation(t *testing.T) {
	setupNotesDB(t)
	dest := t.TempDir()
	note := mustDispatch[Note](t, "notes_create", map[string]any{"title": "x"})
	cases := []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"destDir": dest}, ErrExportEmpty},
		{map[string]any{"destDir": dest, "noteIds": []string{}, "folderId": ""}, ErrExportEmpty},
		{map[string]any{"noteIds": []string{note.ID}}, ErrInvalidRequest},
		{map[string]any{"noteIds": []string{note.ID}, "destDir": "relative"}, ErrInvalidRequest},
		{map[string]any{"noteIds": []string{note.ID}, "destDir": filepath.Join(dest, "absent")}, ErrExportDest},
		{map[string]any{"noteIds": []string{note.ID, "missing"}, "destDir": dest}, ErrNoteNotFound},
		{map[string]any{"folderId": "missing", "destDir": dest}, ErrFolderNotFound},
	}
	for _, tc := range cases {
		if _, err := dispatch(t, "notes_export", tc.args); err == nil || err.Error() != tc.want {
			t.Fatalf("%v: err = %v, want %s", tc.args, err, tc.want)
		}
	}
	if entries, err := os.ReadDir(dest); err != nil || len(entries) != 0 {
		t.Fatalf("failed exports wrote %v (%v)", entries, err)
	}
}

func TestSafeExportName(t *testing.T) {
	cases := map[string]string{
		"":             "无标题",
		"  ":           "无标题",
		"a/b\\c:d*e?f": "a_b_c_d_e_f",
		`"<x>|`:        "__x__",
		"..":           "_",
		"tab\there":    "tab_here",
		"trailing. ":   "trailing",
		"CON":          "CON_",
		"con.txt":      "con_.txt",
		"nul.tar.gz":   "nul_.tar.gz",
		"COM¹":         "COM¹_",
		"lpt³.md":      "lpt³_.md",
		"console.txt":  "console.txt",
		"正常 标题 (1)":    "正常 标题 (1)",
	}
	for in, want := range cases {
		if got := safeExportName(in); got != want {
			t.Errorf("safeExportName(%q) = %q, want %q", in, got, want)
		}
	}
	if got := safeExportName(strings.Repeat("长", 500)); len(got) > maxExportNameBytes {
		t.Errorf("long name has %d bytes", len(got))
	}
}

func TestExportImportRoundTrip(t *testing.T) {
	setupNotesDB(t)
	ref := mustPut(t, pngBytes("round trip"))
	folder := mustDispatch[Folder](t, "notes_folder_create", map[string]any{"name": "原始"})
	body := "正文 ![图](" + ref + ")\n"
	note := mustDispatch[Note](t, "notes_create", map[string]any{"folderId": folder.ID, "title": "C#: [草稿] #1", "body": body})
	mustDispatch[NoteSummary](t, "notes_tags_set", map[string]any{"id": note.ID, "tags": []string{"x, y", "运维", "true"}})
	mustDispatch[NoteSummary](t, "notes_update", map[string]any{"id": note.ID, "favorite": true})
	original := mustDispatch[Note](t, "notes_get", map[string]any{"id": note.ID})

	dest := t.TempDir()
	result := mustDispatch[ExportResult](t, "notes_export", map[string]any{"folderId": folder.ID, "destDir": dest})
	if len(result.Written) != 1 {
		t.Fatalf("written = %v", result.Written)
	}
	// 删除原图，确认导入后的图片来自导出目录。
	mustDispatch[empty](t, "notes_folder_delete", map[string]any{"id": folder.ID})
	if err := os.Remove(filepath.Join(mustAssetsDir(t), strings.TrimPrefix(ref, assetRefPrefix))); err != nil {
		t.Fatal(err)
	}

	report := mustDispatch[ImportReport](t, "notes_import_folder", map[string]any{"path": filepath.Join(dest, "原始")})
	if report.Notes != 1 || report.Assets != 1 || len(report.Skipped) != 0 {
		t.Fatalf("report = %+v", report)
	}
	tree := loadTestTree(t)
	imported := onlyNoteIn(t, tree, childFolder(t, tree, "", "原始").ID)
	if imported.Title != original.Title || !imported.Favorite || !reflect.DeepEqual(imported.Tags, original.Tags) {
		t.Fatalf("imported = %+v, original = %+v", imported.NoteSummary, original.NoteSummary)
	}
	if imported.Body != body || !assetExists(t, ref) {
		t.Fatalf("body = %q, want %q (asset exists %v)", imported.Body, body, assetExists(t, ref))
	}
}

func TestFrontMatterParsing(t *testing.T) {
	cases := []struct {
		in       string
		title    string
		tags     []string
		favorite bool
		body     string
	}{
		{"---\ntitle: Step #1 comment\n---\nb", "Step", nil, false, "b"},
		{"---\ntitle: \"Quoted \\\"x\\\"\"\ntags: [a, 'b, c', \"d\"]\nfavorite: TRUE\n---\nb", `Quoted "x"`, []string{"a", "b, c", "d"}, true, "b"},
		{"---\ntags: one\n...\nb", "", []string{"one"}, false, "b"},
		{"\ufeff---\ntitle: bom\n---\n", "bom", nil, false, ""},
		{"---\ntitle: no close\nbody", "", nil, false, "---\ntitle: no close\nbody"},
		{"text\n---\ntitle: x\n---\n", "", nil, false, "text\n---\ntitle: x\n---\n"},
	}
	for _, tc := range cases {
		fm, body := splitFrontMatter(tc.in)
		if fm.title != tc.title || !reflect.DeepEqual(fm.tags, tc.tags) || fm.favorite != tc.favorite || body != tc.body {
			t.Errorf("splitFrontMatter(%q) = %+v, %q", tc.in, fm, body)
		}
	}
}

func TestExportReservedFolderName(t *testing.T) {
	setupNotesDB(t)
	folder := mustDispatch[Folder](t, "notes_folder_create", map[string]any{"name": "aux.d"})
	mustDispatch[Note](t, "notes_create", map[string]any{"folderId": folder.ID, "title": "NUL.tar", "body": "x"})
	dest := t.TempDir()
	result := mustDispatch[ExportResult](t, "notes_export", map[string]any{"folderId": folder.ID, "destDir": dest})
	if want := []string{filepath.Join(dest, "aux_.d", "NUL_.tar.md")}; !reflect.DeepEqual(result.Written, want) {
		t.Fatalf("written = %v, want %v", result.Written, want)
	}
}

func TestExportOnlyRewritesStandaloneAssetRefs(t *testing.T) {
	setupNotesDB(t)
	ref := mustPut(t, pngBytes("boundary"))
	remote := "https://host" + ref
	body := "![a](" + remote + ")\n" + ref + " <img src=" + ref + "> x" + ref + "\n"
	note := mustDispatch[Note](t, "notes_create", map[string]any{"title": "b", "body": body})
	dest := t.TempDir()
	result := mustDispatch[ExportResult](t, "notes_export", map[string]any{"noteIds": []string{note.ID}, "destDir": dest})
	local := "assets/" + strings.TrimPrefix(ref, assetRefPrefix)
	want := "---\ntitle: b\n---\n\n![a](" + remote + ")\n" + local + " <img src=" + local + "> x" + ref + "\n"
	if got := readTestFile(t, result.Written[0]); got != want {
		t.Fatalf("exported = %q, want %q", got, want)
	}
}

func TestImportKeepsBlockWithoutKnownKeys(t *testing.T) {
	setupNotesDB(t)
	dir := t.TempDir()
	text := "---\nStep 1: foo\n---\nbody"
	writeTestFile(t, filepath.Join(dir, "steps.md"), []byte(text))
	mustDispatch[ImportReport](t, "notes_import_files", map[string]any{"paths": []string{filepath.Join(dir, "steps.md")}})
	notes := notesIn(loadTestTree(t), "")
	if len(notes) != 1 || notes[0].Title != "steps" {
		t.Fatalf("notes = %+v", notes)
	}
	if body := mustDispatch[Note](t, "notes_get", map[string]any{"id": notes[0].ID}).Body; body != text {
		t.Fatalf("body = %q, want unchanged", body)
	}
}

func TestImportFolderRejectsSymlinkRoot(t *testing.T) {
	setupNotesDB(t)
	base := t.TempDir()
	writeTestFile(t, filepath.Join(base, "real", "a.md"), []byte("a"))
	link := filepath.Join(base, "link")
	if err := os.Symlink(filepath.Join(base, "real"), link); err != nil {
		t.Fatal(err)
	}
	if _, err := dispatch(t, "notes_import_folder", map[string]any{"path": link}); err == nil || err.Error() != ErrImportPath {
		t.Fatalf("err = %v, want %s", err, ErrImportPath)
	}
	if tree := loadTestTree(t); len(tree.Notes) != 0 || len(tree.Folders) != 0 {
		t.Fatalf("tree = %+v", tree)
	}
}

func setImportLimits(t *testing.T, entries, notes, skipped int) {
	t.Helper()
	oldEntries, oldNotes, oldSkipped := maxImportEntries, maxImportNotes, maxImportSkipped
	maxImportEntries, maxImportNotes, maxImportSkipped = entries, notes, skipped
	t.Cleanup(func() { maxImportEntries, maxImportNotes, maxImportSkipped = oldEntries, oldNotes, oldSkipped })
}

func TestImportFolderStopsAtLimits(t *testing.T) {
	setupNotesDB(t)
	root := filepath.Join(t.TempDir(), "root")
	for _, name := range []string{"a.md", "b.md", "c.md", "d.md", "e.md"} {
		writeTestFile(t, filepath.Join(root, name), []byte(name))
	}
	importRoot := func() ImportReport {
		return mustDispatch[ImportReport](t, "notes_import_folder", map[string]any{"path": root})
	}

	setImportLimits(t, 3, 100, 100)
	if report := importRoot(); report.Notes != 3 || !report.Truncated {
		t.Fatalf("entry cap: report = %+v", report)
	}
	setImportLimits(t, 100, 2, 100)
	if report := importRoot(); report.Notes != 2 || !report.Truncated {
		t.Fatalf("note cap: report = %+v", report)
	}
	// 恰好达到上限且没有剩余内容时不算截断。
	setImportLimits(t, 5, 5, 100)
	if report := importRoot(); report.Notes != 5 || report.Truncated {
		t.Fatalf("exact cap: report = %+v", report)
	}
}

func TestImportCapsSkippedList(t *testing.T) {
	setupNotesDB(t)
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "a.md"), []byte("![](1.png) ![](2.png) ![](3.png) ![](4.png)"))
	setImportLimits(t, 100, 100, 2)
	report := mustDispatch[ImportReport](t, "notes_import_files", map[string]any{"paths": []string{filepath.Join(dir, "a.md")}})
	if report.Notes != 1 || len(report.Skipped) != 2 || report.SkippedTotal != 4 || report.Truncated {
		t.Fatalf("report = %+v", report)
	}
	if report.Skipped[0].Path != filepath.Join(dir, "1.png") || report.Skipped[1].Path != filepath.Join(dir, "2.png") {
		t.Fatalf("skipped = %+v", report.Skipped)
	}
}
