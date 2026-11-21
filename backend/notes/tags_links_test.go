package notes

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"mincode/app/backend/core"
)

func expectCode(t *testing.T, cmd string, args any, code string) {
	t.Helper()
	if _, err := dispatch(t, cmd, args); err == nil || err.Error() != code {
		t.Fatalf("%s %v: err = %v, want %s", cmd, args, err, code)
	}
}

func sshLink(id string) map[string]any {
	return map[string]any{"objectType": "ssh", "objectId": id, "nameSnapshot": "生产 " + id}
}

func TestTagsSetDedupesAndCleansUp(t *testing.T) {
	setupNotesDB(t)
	note := mustDispatch[Note](t, "notes_create", map[string]any{"title": "部署"})

	summary := mustDispatch[NoteSummary](t, "notes_tags_set", map[string]any{"id": note.ID, "tags": []string{"nginx", " Nginx ", "部署", "  "}})
	if len(summary.Tags) != 2 || summary.UpdatedAt <= note.UpdatedAt {
		t.Fatalf("summary after set = %+v (created %d)", summary, note.UpdatedAt)
	}
	got := mustDispatch[Note](t, "notes_get", map[string]any{"id": note.ID})
	if len(got.Tags) != 2 || got.Tags[0] != "nginx" || got.Tags[1] != "部署" {
		t.Fatalf("note tags = %#v", got.Tags)
	}
	tree := mustDispatch[Tree](t, "notes_tree", map[string]any{})
	if len(tree.Notes) != 1 || len(tree.Notes[0].Tags) != 2 {
		t.Fatalf("tree tags = %+v", tree.Notes)
	}

	// 相同集合（大小写不同）不视为变化，不推进 updated_at，也沿用已有标签名。
	same := mustDispatch[NoteSummary](t, "notes_tags_set", map[string]any{"id": note.ID, "tags": []string{"部署", "NGINX"}})
	if same.UpdatedAt != summary.UpdatedAt || len(same.Tags) != 2 || same.Tags[0] != "nginx" {
		t.Fatalf("same set = %+v, previous %+v", same, summary)
	}

	other := mustDispatch[Note](t, "notes_create", map[string]any{"title": "其他"})
	mustDispatch[NoteSummary](t, "notes_tags_set", map[string]any{"id": other.ID, "tags": []string{"NGINX"}})
	list := mustDispatch[[]TagCount](t, "notes_tags_list", map[string]any{})
	if len(list) != 2 || list[0] != (TagCount{Name: "nginx", Count: 2}) || list[1] != (TagCount{Name: "部署", Count: 1}) {
		t.Fatalf("tags list = %+v", list)
	}

	mustDispatch[NoteSummary](t, "notes_tags_set", map[string]any{"id": note.ID, "tags": []string{}})
	mustDispatch[NoteSummary](t, "notes_tags_set", map[string]any{"id": other.ID, "tags": []string{}})
	out, err := dispatch(t, "notes_tags_list", map[string]any{})
	if err != nil || out != "[]" {
		t.Fatalf("tags list after clear = %q, %v", out, err)
	}
	cleared := mustDispatch[Note](t, "notes_get", map[string]any{"id": note.ID})
	if cleared.Tags == nil || len(cleared.Tags) != 0 {
		t.Fatalf("cleared tags = %#v", cleared.Tags)
	}
}

func TestTagsCleanedUpWhenNoteDeleted(t *testing.T) {
	setupNotesDB(t)
	folder := mustDispatch[Folder](t, "notes_folder_create", map[string]any{"name": "f"})
	a := mustDispatch[Note](t, "notes_create", map[string]any{"title": "a"})
	b := mustDispatch[Note](t, "notes_create", map[string]any{"folderId": folder.ID, "title": "b"})
	mustDispatch[NoteSummary](t, "notes_tags_set", map[string]any{"id": a.ID, "tags": []string{"x"}})
	mustDispatch[NoteSummary](t, "notes_tags_set", map[string]any{"id": b.ID, "tags": []string{"y"}})
	mustDispatch[struct{}](t, "notes_delete", map[string]any{"id": a.ID})
	mustDispatch[struct{}](t, "notes_folder_delete", map[string]any{"id": folder.ID})
	out, err := dispatch(t, "notes_tags_list", map[string]any{})
	if err != nil || out != "[]" {
		t.Fatalf("tags list = %q, %v", out, err)
	}
	db, err := openNotesReadDB()
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM note_tags`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("note_tags rows = %d, %v", count, err)
	}
}

func TestTagsSetValidation(t *testing.T) {
	setupNotesDB(t)
	note := mustDispatch[Note](t, "notes_create", map[string]any{"title": "t"})
	expectCode(t, "notes_tags_set", map[string]any{"id": "missing", "tags": []string{"a"}}, ErrNoteNotFound)
	for _, bad := range []any{
		map[string]any{"id": note.ID},
		map[string]any{"id": note.ID, "tags": nil},
		map[string]any{"id": note.ID, "tags": []string{"a\nb"}},
		map[string]any{"id": note.ID, "tags": []string{"‮abc"}},
		map[string]any{"id": note.ID, "tags": []string{strings.Repeat("标", maxTagRunes+1)}},
		map[string]any{"id": note.ID, "tags": []any{1}},
		map[string]any{"id": "", "tags": []string{}},
	} {
		expectCode(t, "notes_tags_set", bad, ErrInvalidRequest)
	}
	tooMany := make([]string, maxTagsPerNote+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("t%d", i)
	}
	expectCode(t, "notes_tags_set", map[string]any{"id": note.ID, "tags": tooMany}, ErrInvalidRequest)
	// 恰好上限长度可用，首尾空白不计入长度。
	ok := mustDispatch[NoteSummary](t, "notes_tags_set", map[string]any{"id": note.ID, "tags": []string{" " + strings.Repeat("标", maxTagRunes) + " "}})
	if len(ok.Tags) != 1 || ok.Tags[0] != strings.Repeat("标", maxTagRunes) {
		t.Fatalf("max tag = %#v", ok.Tags)
	}
}

func TestLinksForObjectAndCounts(t *testing.T) {
	setupNotesDB(t)
	a := mustDispatch[Note](t, "notes_create", map[string]any{"title": "a"})
	b := mustDispatch[Note](t, "notes_create", map[string]any{"title": "b"})
	mustDispatch[Link](t, "notes_links_add", map[string]any{"id": a.ID, "link": sshLink("prod-1")})
	mustDispatch[Link](t, "notes_links_add", map[string]any{"id": b.ID, "link": sshLink("prod-1")})
	mustDispatch[Link](t, "notes_links_add", map[string]any{"id": b.ID, "link": sshLink("prod-2")})

	counts := mustDispatch[map[string]int](t, "notes_links_counts", map[string]any{"objectType": "ssh", "objectIds": []string{"prod-1", "prod-2", "prod-3"}})
	if len(counts) != 2 || counts["prod-1"] != 2 || counts["prod-2"] != 1 {
		t.Fatalf("counts = %v", counts)
	}
	notes := mustDispatch[[]NoteSummary](t, "notes_links_for_object", map[string]any{"objectType": "ssh", "objectId": "prod-1"})
	if len(notes) != 2 {
		t.Fatalf("for object = %+v", notes)
	}
	// 按 updated_at 降序：b 最后被修改。
	if notes[0].ID != b.ID || notes[1].ID != a.ID {
		t.Fatalf("for object order = %+v", notes)
	}

	mustDispatch[struct{}](t, "notes_delete", map[string]any{"id": a.ID})
	counts = mustDispatch[map[string]int](t, "notes_links_counts", map[string]any{"objectType": "ssh", "objectIds": []string{"prod-1", "prod-2"}})
	if counts["prod-1"] != 1 || counts["prod-2"] != 1 {
		t.Fatalf("counts after delete = %v", counts)
	}
	out, err := dispatch(t, "notes_links_counts", map[string]any{"objectType": "ssh", "objectIds": []string{}})
	if err != nil || out != "{}" {
		t.Fatalf("empty counts = %q, %v", out, err)
	}
	out, err = dispatch(t, "notes_links_for_object", map[string]any{"objectType": "ssh", "objectId": "nobody"})
	if err != nil || out != "[]" {
		t.Fatalf("empty for object = %q, %v", out, err)
	}
}

func TestLinkRejectsUnknownType(t *testing.T) {
	setupNotesDB(t)
	note := mustDispatch[Note](t, "notes_create", map[string]any{"title": "a"})
	link := sshLink("x")
	link["objectType"] = "redis"
	expectCode(t, "notes_links_add", map[string]any{"id": note.ID, "link": link}, ErrLinkType)
	expectCode(t, "notes_links_remove", map[string]any{"id": note.ID, "objectType": "redis", "objectId": "x"}, ErrLinkType)
	expectCode(t, "notes_links_for_object", map[string]any{"objectType": "", "objectId": "x"}, ErrLinkType)
	expectCode(t, "notes_links_counts", map[string]any{"objectType": "SSH", "objectIds": []string{"x"}}, ErrLinkType)
}

func TestLinkAddUpdatesSnapshotAndRemove(t *testing.T) {
	setupNotesDB(t)
	note := mustDispatch[Note](t, "notes_create", map[string]any{"title": "a"})
	ai := map[string]any{"objectType": "ai", "objectId": "conv-1", "scopeId": "ws-1", "nameSnapshot": "旧名", "scopeNameSnapshot": "工作区一"}
	first := mustDispatch[Link](t, "notes_links_add", map[string]any{"id": note.ID, "link": ai})
	if first.CreatedAt == 0 || first.ScopeID != "ws-1" || first.NameSnapshot != "旧名" {
		t.Fatalf("first link = %+v", first)
	}
	afterAdd := mustDispatch[Note](t, "notes_get", map[string]any{"id": note.ID})
	if afterAdd.UpdatedAt <= note.UpdatedAt || len(afterAdd.Links) != 1 || afterAdd.Links[0] != first {
		t.Fatalf("after add = %+v", afterAdd)
	}

	// 同主键再次添加：更新快照（与 parentId），不新增行，不改 createdAt。
	ai["nameSnapshot"] = "新名"
	ai["createdAt"] = 1
	second := mustDispatch[Link](t, "notes_links_add", map[string]any{"id": note.ID, "link": ai})
	if second.CreatedAt != first.CreatedAt || second.NameSnapshot != "新名" {
		t.Fatalf("second link = %+v", second)
	}
	afterSnapshot := mustDispatch[Note](t, "notes_get", map[string]any{"id": note.ID})
	if len(afterSnapshot.Links) != 1 || afterSnapshot.Links[0].NameSnapshot != "新名" || afterSnapshot.UpdatedAt <= afterAdd.UpdatedAt {
		t.Fatalf("after snapshot = %+v", afterSnapshot)
	}
	// 完全相同再添加：不推进 updated_at。
	mustDispatch[Link](t, "notes_links_add", map[string]any{"id": note.ID, "link": ai})
	unchanged := mustDispatch[Note](t, "notes_get", map[string]any{"id": note.ID})
	if unchanged.UpdatedAt != afterSnapshot.UpdatedAt {
		t.Fatalf("identical add bumped updated_at: %d -> %d", afterSnapshot.UpdatedAt, unchanged.UpdatedAt)
	}

	// 同对象不同工作区是另一条关联；scopeId 省略时按任意工作区匹配。
	ai["scopeId"] = "ws-2"
	mustDispatch[Link](t, "notes_links_add", map[string]any{"id": note.ID, "link": ai})
	if got := mustDispatch[[]NoteSummary](t, "notes_links_for_object", map[string]any{"objectType": "ai", "objectId": "conv-1"}); len(got) != 1 {
		t.Fatalf("any-scope for object = %+v", got)
	}
	if got := mustDispatch[[]NoteSummary](t, "notes_links_for_object", map[string]any{"objectType": "ai", "objectId": "conv-1", "scopeId": "ws-3"}); len(got) != 0 {
		t.Fatalf("other-scope for object = %+v", got)
	}
	if got := mustDispatch[map[string]int](t, "notes_links_counts", map[string]any{"objectType": "ai", "objectIds": []string{"conv-1"}}); got["conv-1"] != 1 {
		t.Fatalf("counts across scopes = %v", got)
	}

	all := mustDispatch[[]LinkEntry](t, "notes_links_all", map[string]any{})
	if len(all) != 2 || all[0].NoteID != note.ID || all[0].NoteTitle != "a" || all[0].Link.ObjectID != "conv-1" {
		t.Fatalf("links all = %+v", all)
	}

	mustDispatch[struct{}](t, "notes_links_remove", map[string]any{"id": note.ID, "objectType": "ai", "objectId": "conv-1", "scopeId": "ws-1"})
	afterRemove := mustDispatch[Note](t, "notes_get", map[string]any{"id": note.ID})
	if len(afterRemove.Links) != 1 || afterRemove.Links[0].ScopeID != "ws-2" || afterRemove.UpdatedAt <= unchanged.UpdatedAt {
		t.Fatalf("after remove = %+v", afterRemove)
	}
	// 移除不存在的关联是幂等的，不推进 updated_at。
	mustDispatch[struct{}](t, "notes_links_remove", map[string]any{"id": note.ID, "objectType": "ai", "objectId": "conv-1", "scopeId": "ws-1"})
	if again := mustDispatch[Note](t, "notes_get", map[string]any{"id": note.ID}); again.UpdatedAt != afterRemove.UpdatedAt {
		t.Fatalf("idempotent remove bumped updated_at")
	}
	expectCode(t, "notes_links_add", map[string]any{"id": "missing", "link": ai}, ErrNoteNotFound)
	expectCode(t, "notes_links_remove", map[string]any{"id": "missing", "objectType": "ai", "objectId": "conv-1"}, ErrNoteNotFound)

	// 删除笔记后关联随之删除。
	mustDispatch[struct{}](t, "notes_delete", map[string]any{"id": note.ID})
	if out, err := dispatch(t, "notes_links_all", map[string]any{}); err != nil || out != "[]" {
		t.Fatalf("links all after delete = %q, %v", out, err)
	}
}

func TestLinkReAddUpdatesParentID(t *testing.T) {
	setupNotesDB(t)
	note := mustDispatch[Note](t, "notes_create", map[string]any{"title": "a"})
	req := map[string]any{"objectType": "http", "objectId": "req-1", "parentId": "collection-a", "nameSnapshot": "登录接口"}
	first := mustDispatch[Link](t, "notes_links_add", map[string]any{"id": note.ID, "link": req})
	before := mustDispatch[Note](t, "notes_get", map[string]any{"id": note.ID})

	// 请求移到另一个集合后再次添加：更新 parentId，保留 createdAt，并推进笔记 updated_at。
	req["parentId"] = "collection-b"
	second := mustDispatch[Link](t, "notes_links_add", map[string]any{"id": note.ID, "link": req})
	if second.ParentID != "collection-b" || second.CreatedAt != first.CreatedAt || second.NameSnapshot != "登录接口" {
		t.Fatalf("re-added link = %+v, first %+v", second, first)
	}
	after := mustDispatch[Note](t, "notes_get", map[string]any{"id": note.ID})
	if len(after.Links) != 1 || after.Links[0] != second || after.UpdatedAt <= before.UpdatedAt {
		t.Fatalf("after re-add = %+v", after)
	}
	all := mustDispatch[[]LinkEntry](t, "notes_links_all", map[string]any{})
	if len(all) != 1 || all[0].Link.ParentID != "collection-b" {
		t.Fatalf("links all = %+v", all)
	}
}

func TestLinkValidation(t *testing.T) {
	setupNotesDB(t)
	note := mustDispatch[Note](t, "notes_create", map[string]any{"title": "a"})
	long := strings.Repeat("p", maxLinkIDBytes+1)
	for _, link := range []map[string]any{
		{"objectType": "project", "objectId": ""},
		{"objectType": "project", "objectId": long},
		{"objectType": "project", "objectId": "/a", "parentId": long},
		{"objectType": "project", "objectId": "/a", "scopeId": long},
		{"objectType": "project", "objectId": "/a\n"},
		{"objectType": "project", "objectId": "/a", "nameSnapshot": "x\ny"},
		{"objectType": "project", "objectId": "/a", "nameSnapshot": strings.Repeat("名", maxSnapshotRunes+1)},
		{"objectType": "project", "objectId": "/a", "scopeNameSnapshot": "‮"},
		{"objectType": "project", "objectId": "/a", "unknown": 1},
	} {
		expectCode(t, "notes_links_add", map[string]any{"id": note.ID, "link": link}, ErrInvalidRequest)
	}
	expectCode(t, "notes_links_add", map[string]any{"id": note.ID}, ErrInvalidRequest)
	expectCode(t, "notes_links_remove", map[string]any{"id": note.ID, "objectType": "project", "objectId": ""}, ErrInvalidRequest)
	expectCode(t, "notes_links_for_object", map[string]any{"objectType": "project", "objectId": ""}, ErrInvalidRequest)
	expectCode(t, "notes_links_counts", map[string]any{"objectType": "project", "objectIds": []string{""}}, ErrInvalidRequest)
	expectCode(t, "notes_links_counts", map[string]any{"objectType": "project"}, ErrInvalidRequest)
	tooMany := make([]string, maxCountObjectIDs+1)
	for i := range tooMany {
		tooMany[i] = "x"
	}
	expectCode(t, "notes_links_counts", map[string]any{"objectType": "project", "objectIds": tooMany}, ErrInvalidRequest)

	// 项目 id 是绝对路径，允许到 512 字节。
	path := "/" + strings.Repeat("p", maxLinkIDBytes-1)
	link := mustDispatch[Link](t, "notes_links_add", map[string]any{"id": note.ID, "link": map[string]any{"objectType": "project", "objectId": path, "nameSnapshot": strings.Repeat("名", maxSnapshotRunes)}})
	if link.ObjectID != path {
		t.Fatalf("project link = %+v", link)
	}
	if _, err := core.Dispatch("notes_links_all", ""); err != nil {
		t.Fatalf("links all with empty body: %v", err)
	}
	var raw []map[string]json.RawMessage
	out, _ := dispatch(t, "notes_links_all", map[string]any{})
	if err := json.Unmarshal([]byte(out), &raw); err != nil || len(raw) != 1 {
		t.Fatalf("links all raw = %q", out)
	}
	for _, key := range []string{"link", "noteId", "noteTitle"} {
		if _, ok := raw[0][key]; !ok {
			t.Fatalf("links all entry missing %s: %q", key, out)
		}
	}
}
