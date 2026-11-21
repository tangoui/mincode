package notes

import (
	"encoding/json"
	"testing"

	"mincode/app/backend/core"
	"mincode/app/backend/storage"
)

func setupNotesDB(t *testing.T) {
	t.Helper()
	t.Setenv("MINCODE_DATA_DIR", t.TempDir())
	_ = storage.Close()
	t.Cleanup(func() { _ = storage.Close() })
}

func dispatch(t *testing.T, cmd string, args any) (string, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return core.Dispatch(cmd, string(raw))
}

func mustDispatch[T any](t *testing.T, cmd string, args any) T {
	t.Helper()
	out, err := dispatch(t, cmd, args)
	if err != nil {
		t.Fatalf("%s: %v", cmd, err)
	}
	var result T
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("%s: decode %q: %v", cmd, out, err)
	}
	return result
}

func TestCreateNoteAndTree(t *testing.T) {
	setupNotesDB(t)
	folder := mustDispatch[Folder](t, "notes_folder_create", map[string]any{"name": "运维"})
	if folder.ID == "" || folder.Name != "运维" || folder.ParentID != "" || folder.Position != 1 {
		t.Fatalf("folder = %+v", folder)
	}
	note := mustDispatch[Note](t, "notes_create", map[string]any{"folderId": folder.ID, "title": "部署步骤", "body": "# 部署"})
	if note.ID == "" || note.Title != "部署步骤" || note.Body != "# 部署" || note.FolderID != folder.ID {
		t.Fatalf("note = %+v", note)
	}
	if note.Position != 1 || note.CreatedAt == 0 || note.UpdatedAt == 0 {
		t.Fatalf("note metadata = %+v", note)
	}

	out, err := dispatch(t, "notes_tree", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	var tree Tree
	if err := json.Unmarshal([]byte(out), &tree); err != nil {
		t.Fatal(err)
	}
	if len(tree.Folders) != 1 || len(tree.Notes) != 1 {
		t.Fatalf("tree = %+v", tree)
	}
	if tree.Notes[0].FolderID != folder.ID || tree.Folders[0].ID != folder.ID {
		t.Fatalf("tree = %+v", tree)
	}
	// Tags 与 Links 必须编码为 []，不能是 null。
	var shape struct {
		Notes []map[string]json.RawMessage `json:"notes"`
	}
	if err := json.Unmarshal([]byte(out), &shape); err != nil {
		t.Fatal(err)
	}
	if string(shape.Notes[0]["tags"]) != "[]" {
		t.Fatalf("tree note tags = %s", shape.Notes[0]["tags"])
	}
	got, err := dispatch(t, "notes_get", map[string]any{"id": note.ID})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(got), &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["links"]) != "[]" || string(fields["tags"]) != "[]" {
		t.Fatalf("note links/tags = %s / %s", fields["links"], fields["tags"])
	}

	second := mustDispatch[Note](t, "notes_create", map[string]any{"folderId": folder.ID})
	if second.Position != 2 || second.Title != "" || second.Body != "" {
		t.Fatalf("second note = %+v", second)
	}
	root := mustDispatch[Note](t, "notes_create", map[string]any{})
	if root.FolderID != "" || root.Position != 1 {
		t.Fatalf("root note = %+v", root)
	}
}

func TestUpdateOnlyBumpsTimeOnChange(t *testing.T) {
	setupNotesDB(t)
	note := mustDispatch[Note](t, "notes_create", map[string]any{"title": "t", "body": "same"})
	unchanged := mustDispatch[NoteSummary](t, "notes_update", map[string]any{"id": note.ID, "body": "same"})
	if unchanged.UpdatedAt != note.UpdatedAt {
		t.Fatalf("unchanged update bumped time: %d -> %d", note.UpdatedAt, unchanged.UpdatedAt)
	}
	changed := mustDispatch[NoteSummary](t, "notes_update", map[string]any{"id": note.ID, "body": "different"})
	if changed.UpdatedAt <= note.UpdatedAt {
		t.Fatalf("changed update did not bump time: %d -> %d", note.UpdatedAt, changed.UpdatedAt)
	}
	fav := mustDispatch[NoteSummary](t, "notes_update", map[string]any{"id": note.ID, "favorite": true, "title": "新标题"})
	if !fav.Favorite || fav.Title != "新标题" || fav.UpdatedAt <= changed.UpdatedAt {
		t.Fatalf("favorite update = %+v", fav)
	}
	got := mustDispatch[Note](t, "notes_get", map[string]any{"id": note.ID})
	if got.Body != "different" || got.Title != "新标题" || !got.Favorite {
		t.Fatalf("stored note = %+v", got)
	}
	if _, err := dispatch(t, "notes_update", map[string]any{"id": "missing", "body": "x"}); err == nil || err.Error() != ErrNoteNotFound {
		t.Fatalf("missing update err = %v", err)
	}
}

func TestFolderMoveRejectsCycle(t *testing.T) {
	setupNotesDB(t)
	a := mustDispatch[Folder](t, "notes_folder_create", map[string]any{"name": "A"})
	b := mustDispatch[Folder](t, "notes_folder_create", map[string]any{"parentId": a.ID, "name": "B"})
	if _, err := dispatch(t, "notes_folder_move", map[string]any{"id": a.ID, "parentId": b.ID, "position": 1}); err == nil || err.Error() != ErrFolderCycle {
		t.Fatalf("move under child err = %v", err)
	}
	if _, err := dispatch(t, "notes_folder_move", map[string]any{"id": a.ID, "parentId": a.ID, "position": 1}); err == nil || err.Error() != ErrFolderCycle {
		t.Fatalf("move under self err = %v", err)
	}
	// 合法移动：B 移到根。
	if _, err := dispatch(t, "notes_folder_move", map[string]any{"id": b.ID, "parentId": "", "position": 0.5}); err != nil {
		t.Fatal(err)
	}
	tree := mustDispatch[Tree](t, "notes_tree", map[string]any{})
	if len(tree.Folders) != 2 || tree.Folders[0].ID != b.ID || tree.Folders[0].ParentID != "" {
		t.Fatalf("tree after move = %+v", tree)
	}
	if _, err := dispatch(t, "notes_folder_rename", map[string]any{"id": a.ID, "name": "  "}); err == nil {
		t.Fatal("blank folder name accepted")
	}
	if _, err := dispatch(t, "notes_folder_rename", map[string]any{"id": a.ID, "name": "运维"}); err != nil {
		t.Fatal(err)
	}
}

func TestFolderDeleteCascades(t *testing.T) {
	setupNotesDB(t)
	a := mustDispatch[Folder](t, "notes_folder_create", map[string]any{"name": "A"})
	b := mustDispatch[Folder](t, "notes_folder_create", map[string]any{"parentId": a.ID, "name": "B"})
	inner := mustDispatch[Note](t, "notes_create", map[string]any{"folderId": b.ID, "title": "inner"})
	keep := mustDispatch[Note](t, "notes_create", map[string]any{"title": "keep"})
	if _, err := dispatch(t, "notes_folder_delete", map[string]any{"id": a.ID}); err != nil {
		t.Fatal(err)
	}
	tree := mustDispatch[Tree](t, "notes_tree", map[string]any{})
	if len(tree.Folders) != 0 || len(tree.Notes) != 1 || tree.Notes[0].ID != keep.ID {
		t.Fatalf("tree after delete = %+v", tree)
	}
	if _, err := dispatch(t, "notes_get", map[string]any{"id": inner.ID}); err == nil || err.Error() != ErrNoteNotFound {
		t.Fatalf("inner note err = %v", err)
	}
	if _, err := dispatch(t, "notes_create", map[string]any{"folderId": a.ID}); err == nil || err.Error() != ErrFolderNotFound {
		t.Fatalf("create in deleted folder err = %v", err)
	}
}

func TestGetMissingNote(t *testing.T) {
	setupNotesDB(t)
	if _, err := dispatch(t, "notes_get", map[string]any{"id": "no-such-note"}); err == nil || err.Error() != ErrNoteNotFound {
		t.Fatalf("err = %v", err)
	}
}

func TestNoteMoveAndDelete(t *testing.T) {
	setupNotesDB(t)
	folder := mustDispatch[Folder](t, "notes_folder_create", map[string]any{"name": "F"})
	note := mustDispatch[Note](t, "notes_create", map[string]any{"title": "n"})
	if _, err := dispatch(t, "notes_move", map[string]any{"id": note.ID, "folderId": folder.ID, "position": 3.5}); err != nil {
		t.Fatal(err)
	}
	got := mustDispatch[Note](t, "notes_get", map[string]any{"id": note.ID})
	if got.FolderID != folder.ID || got.Position != 3.5 {
		t.Fatalf("moved note = %+v", got)
	}
	if _, err := dispatch(t, "notes_move", map[string]any{"id": note.ID, "folderId": "missing", "position": 1}); err == nil || err.Error() != ErrFolderNotFound {
		t.Fatalf("move to missing folder err = %v", err)
	}
	if _, err := dispatch(t, "notes_delete", map[string]any{"id": note.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := dispatch(t, "notes_delete", map[string]any{"id": note.ID}); err == nil || err.Error() != ErrNoteNotFound {
		t.Fatalf("second delete err = %v", err)
	}
}

func TestRequestValidation(t *testing.T) {
	setupNotesDB(t)
	cases := []struct{ cmd, raw string }{
		{"notes_get", `{}`},
		{"notes_get", `{"id":""}`},
		{"notes_get", `{"id":"x","extra":1}`},
		{"notes_get", `[]`},
		{"notes_get", `{"id":"x"} {}`},
		{"notes_tree", `{"x":1}`},
		{"notes_folder_create", `{}`},
		{"notes_folder_create", `{"name":"a\nb"}`},
		{"notes_move", `{"id":"x","folderId":""}`},
		{"notes_update", `{"id":"x","title":"a\u0000"}`},
	}
	for _, c := range cases {
		if _, err := core.Dispatch(c.cmd, c.raw); err == nil || err.Error() != ErrInvalidRequest {
			t.Errorf("%s %s: err = %v, want %s", c.cmd, c.raw, err, ErrInvalidRequest)
		}
	}
	if _, err := core.Dispatch("notes_tree", ""); err != nil {
		t.Fatalf("empty args for notes_tree: %v", err)
	}
}

func TestTitlesAndFolderNamesAllowFormatCharacters(t *testing.T) {
	setupNotesDB(t)
	// ZWJ 组合 emoji、ZWNJ（波斯文）、软连字符都是 Cf，但属于合法文本。
	for _, name := range []string{"👩‍💻 学习笔记", "می‌خواهم", "co­operate", "שלום ‏!"} {
		folder := mustDispatch[Folder](t, "notes_folder_create", map[string]any{"name": name})
		if folder.Name != name {
			t.Fatalf("folder name = %q, want %q", folder.Name, name)
		}
		if _, err := dispatch(t, "notes_folder_rename", map[string]any{"id": folder.ID, "name": name + "2"}); err != nil {
			t.Fatalf("rename %q: %v", name, err)
		}
		note := mustDispatch[Note](t, "notes_create", map[string]any{"title": name})
		updated := mustDispatch[NoteSummary](t, "notes_update", map[string]any{"id": note.ID, "title": name + "2"})
		if updated.Title != name+"2" {
			t.Fatalf("title = %q", updated.Title)
		}
	}
	// 双向覆盖 / 隔离字符仍然拒绝。
	for _, bad := range []string{"a‮b", "a‪b", "a⁦b", "a⁩b"} {
		if _, err := dispatch(t, "notes_folder_create", map[string]any{"name": bad}); err == nil || err.Error() != ErrInvalidRequest {
			t.Errorf("folder name %q err = %v", bad, err)
		}
		if _, err := dispatch(t, "notes_create", map[string]any{"title": bad}); err == nil || err.Error() != ErrInvalidRequest {
			t.Errorf("title %q err = %v", bad, err)
		}
	}
}
