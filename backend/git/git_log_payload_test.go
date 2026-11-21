package git

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// 图谱每行显示 short_hash/message/author/time；后端 JSON 字段名一旦漂移，界面就会只剩作者。
func TestGitLogPayloadContainsFieldsRenderedByGraph(t *testing.T) {
	repository := t.TempDir()
	runGitTestCommand(t, repository, "init")
	runGitTestCommand(t, repository, "config", "user.name", "MinCode Test")
	runGitTestCommand(t, repository, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(repository, "a.txt"), []byte("a\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGitTestCommand(t, repository, "add", "a.txt")
	runGitTestCommand(t, repository, "commit", "-m", "feat: keep the graph readable")

	payload, err := gitGetLogHandler("", marshalGitTestArgs(t, map[string]any{"repoPath": repository, "limit": 10}))
	if err != nil {
		t.Fatalf("读取日志失败：%v", err)
	}
	var entries []map[string]any
	if err := json.Unmarshal([]byte(payload), &entries); err != nil {
		t.Fatalf("解析返回：%v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("应只有一条提交，实际 %d", len(entries))
	}
	entry := entries[0]
	for field, want := range map[string]string{
		"hash":       "",
		"short_hash": "",
		"author":     "MinCode Test",
		"message":    "feat: keep the graph readable",
	} {
		value, ok := entry[field].(string)
		if !ok || value == "" {
			t.Fatalf("缺少字段 %q 或为空：%+v", field, entry)
		}
		if want != "" && value != want {
			t.Fatalf("%s = %q，期望 %q", field, value, want)
		}
	}
	if _, ok := entry["time"].(float64); !ok {
		t.Fatalf("time 必须是数字：%+v", entry)
	}
}
