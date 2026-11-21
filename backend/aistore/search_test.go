package aistore

import (
	"encoding/json"
	"strings"
	"testing"
)

func conversationWithMessages(id, title string, messages ...map[string]any) map[string]any {
	conversation := sampleConversation(id)
	conversation["title"] = title
	items := make([]any, len(messages))
	for i, message := range messages {
		items[i] = message
	}
	conversation["messages"] = items
	return conversation
}

func TestSearchMessagesMatchesContentAcrossWorkspaces(t *testing.T) {
	db := setTestDatabase(t)
	saveTestConversation(t, db, "workspace:one", conversationWithMessages("c1", "部署问题",
		map[string]any{"id": "m1", "role": "user", "content": "How do I restart the Nginx service?"},
		map[string]any{"id": "m2", "role": "assistant", "content": "Run systemctl restart nginx, then check the status."},
		map[string]any{"id": "m3", "role": "assistant", "content": "nginx failed", "presentation": "error"},
	), 0)
	saveTestConversation(t, db, "workspace:two", conversationWithMessages("c2", "数据库",
		map[string]any{"id": "m9", "role": "user", "content": "迁移 MySQL 表结构时报错 \"duplicate key\""},
	), 0)

	matches, err := searchMessages(db, "NGINX", nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 || matches[0].MessageID != "m1" || matches[1].MessageID != "m2" {
		t.Fatalf("matches = %+v (error presentations must be skipped)", matches)
	}
	if matches[0].Title != "部署问题" || matches[0].ScopeID != "workspace:one" || !strings.Contains(matches[0].Snippet, "Nginx") {
		t.Fatalf("match = %+v", matches[0])
	}

	scoped, err := searchMessages(db, "nginx", []string{"workspace:two"}, 10)
	if err != nil || len(scoped) != 0 {
		t.Fatalf("scoped matches = %+v, err = %v", scoped, err)
	}

	// Quotes are JSON-escaped in the payload, so this query skips the SQL pre-filter.
	quoted, err := searchMessages(db, `"duplicate key"`, nil, 10)
	if err != nil || len(quoted) != 1 || quoted[0].MessageID != "m9" {
		t.Fatalf("quoted matches = %+v, err = %v", quoted, err)
	}
	cjk, err := searchMessages(db, "表结构", nil, 10)
	if err != nil || len(cjk) != 1 {
		t.Fatalf("cjk matches = %+v, err = %v", cjk, err)
	}

	limited, _ := searchMessages(db, "n", nil, 1)
	if len(limited) != 1 {
		t.Fatalf("limit ignored: %d matches", len(limited))
	}
}

func TestSearchMessagesHandlerValidatesQuery(t *testing.T) {
	setTestDatabase(t)
	for _, query := range []string{"", "   ", strings.Repeat("x", maxMessageSearchQueryLen+1)} {
		args, _ := json.Marshal(map[string]any{"query": query})
		if _, err := searchMessagesHandler("", string(args)); err == nil {
			t.Fatalf("query %q accepted", query)
		}
	}
	out, err := searchMessagesHandler("", `{"query":"hello"}`)
	if err != nil || out != "[]" {
		t.Fatalf("empty store result = %q, err = %v", out, err)
	}
}

func TestMatchSnippetTrimsAroundTheMatch(t *testing.T) {
	content := strings.Repeat("a ", 100) + "Target word\nhere " + strings.Repeat("b ", 100)
	snippet, ok := matchSnippet(content, "target")
	if !ok || !strings.HasPrefix(snippet, "…") || !strings.HasSuffix(snippet, "…") || !strings.Contains(snippet, "Target word here") {
		t.Fatalf("snippet = %q", snippet)
	}
	if _, ok := matchSnippet("nothing here", "missing"); ok {
		t.Fatal("unexpected match")
	}
}
