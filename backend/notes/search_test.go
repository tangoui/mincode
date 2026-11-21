package notes

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSearchEscapesWildcards(t *testing.T) {
	setupNotesDB(t)
	hit := mustDispatch[Note](t, "notes_create", map[string]any{"title": "a", "body": "磁盘使用率 100% 告警"})
	mustDispatch[Note](t, "notes_create", map[string]any{"title": "b", "body": "磁盘使用率 100x 告警"})
	results := mustDispatch[[]SearchResult](t, "notes_search", map[string]any{"query": "100%"})
	if len(results) != 1 || results[0].Note.ID != hit.ID || !strings.Contains(results[0].Snippet, "100%") {
		t.Fatalf("results = %+v", results)
	}

	mustDispatch[Note](t, "notes_create", map[string]any{"title": "c", "body": `path a_b and C:\temp`})
	mustDispatch[Note](t, "notes_create", map[string]any{"title": "d", "body": `path axb and C:temp`})
	if got := mustDispatch[[]SearchResult](t, "notes_search", map[string]any{"query": "a_b"}); len(got) != 1 || got[0].Note.Title != "c" {
		t.Fatalf("underscore results = %+v", got)
	}
	if got := mustDispatch[[]SearchResult](t, "notes_search", map[string]any{"query": `C:\temp`}); len(got) != 1 || got[0].Note.Title != "c" {
		t.Fatalf("backslash results = %+v", got)
	}
}

func TestSearchTitleFirst(t *testing.T) {
	setupNotesDB(t)
	titled := mustDispatch[Note](t, "notes_create", map[string]any{"title": "Nginx 配置", "body": "反向代理"})
	bodyOnly := mustDispatch[Note](t, "notes_create", map[string]any{"title": "部署", "body": "重启 NGINX 服务"})
	// bodyOnly 更新得更晚，但标题命中仍排在前面。
	mustDispatch[NoteSummary](t, "notes_tags_set", map[string]any{"id": bodyOnly.ID, "tags": []string{"ops"}})
	results := mustDispatch[[]SearchResult](t, "notes_search", map[string]any{"query": "nginx"})
	if len(results) != 2 || results[0].Note.ID != titled.ID || results[1].Note.ID != bodyOnly.ID {
		t.Fatalf("results = %+v", results)
	}
	if results[1].Snippet != "重启 NGINX 服务" || len(results[1].Note.Tags) != 1 || results[1].Note.Tags[0] != "ops" {
		t.Fatalf("body hit = %+v", results[1])
	}
	// 只命中标题时，片段取正文开头。
	if results[0].Snippet != "反向代理" {
		t.Fatalf("title-only snippet = %q", results[0].Snippet)
	}
}

func TestSearchOrdersByUpdatedAtWithinGroup(t *testing.T) {
	setupNotesDB(t)
	older := mustDispatch[Note](t, "notes_create", map[string]any{"title": "redis 1"})
	newer := mustDispatch[Note](t, "notes_create", map[string]any{"title": "redis 2"})
	// 同一毫秒内创建与修改可能得到相同的 updated_at，直接把 older 设为严格更新，避免并列时按随机 id 排序。
	db, err := openNotesDB()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE notes SET updated_at = ? WHERE id = ?`, newer.UpdatedAt+1000, older.ID); err != nil {
		t.Fatal(err)
	}
	results := mustDispatch[[]SearchResult](t, "notes_search", map[string]any{"query": "REDIS"})
	if len(results) != 2 || results[0].Note.ID != older.ID || results[1].Note.ID != newer.ID {
		t.Fatalf("results = %+v", results)
	}
}

func TestSearchSnippetWindowByRune(t *testing.T) {
	setupNotesDB(t)
	before := strings.Repeat("前", 60)
	after := strings.Repeat("后", 60)
	mustDispatch[Note](t, "notes_create", map[string]any{"title": "x", "body": before + "\n关键词\t" + after})
	results := mustDispatch[[]SearchResult](t, "notes_search", map[string]any{"query": "关键词"})
	if len(results) != 1 {
		t.Fatalf("results = %+v", results)
	}
	want := strings.Repeat("前", 39) + " 关键词 " + strings.Repeat("后", 39)
	if results[0].Snippet != want {
		t.Fatalf("snippet = %q, want %q", results[0].Snippet, want)
	}
	if !utf8.ValidString(results[0].Snippet) {
		t.Fatal("snippet not valid UTF-8")
	}

	mustDispatch[Note](t, "notes_create", map[string]any{"title": "y", "body": "短 needle 文"})
	short := mustDispatch[[]SearchResult](t, "notes_search", map[string]any{"query": "NEEDLE"})
	if len(short) != 1 || short[0].Snippet != "短 needle 文" {
		t.Fatalf("short = %+v", short)
	}
}

func TestSearchEmptyQueryAndLimit(t *testing.T) {
	setupNotesDB(t)
	for i := 0; i < 205; i++ {
		mustDispatch[Note](t, "notes_create", map[string]any{"title": "item", "body": "共同内容"})
	}
	for _, q := range []string{"", "   "} {
		out, err := dispatch(t, "notes_search", map[string]any{"query": q})
		if err != nil || out != "[]" {
			t.Fatalf("empty query %q = %q, %v", q, out, err)
		}
	}
	if got := mustDispatch[[]SearchResult](t, "notes_search", map[string]any{"query": "共同"}); len(got) != defaultSearchLimit {
		t.Fatalf("default limit = %d", len(got))
	}
	if got := mustDispatch[[]SearchResult](t, "notes_search", map[string]any{"query": "共同", "limit": 1000}); len(got) != maxSearchLimit {
		t.Fatalf("max limit = %d", len(got))
	}
	if got := mustDispatch[[]SearchResult](t, "notes_search", map[string]any{"query": "共同", "limit": 3}); len(got) != 3 {
		t.Fatalf("limit 3 = %d", len(got))
	}
	expectCode(t, "notes_search", map[string]any{"query": strings.Repeat("q", maxQueryRunes+1)}, ErrInvalidRequest)
	expectCode(t, "notes_search", map[string]any{"query": "a\x00"}, ErrInvalidRequest)
	expectCode(t, "notes_search", map[string]any{"query": "a", "limit": 1.5}, ErrInvalidRequest)
}
