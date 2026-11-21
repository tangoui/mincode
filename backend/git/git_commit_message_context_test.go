package git

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateGitContextPreservesUTF8(t *testing.T) {
	got := truncateGitContext(strings.Repeat("中文", 20), 11)
	if !utf8.ValidString(got) {
		t.Fatalf("截断结果不是有效 UTF-8: %q", got)
	}
	if !strings.HasSuffix(got, "... (diff truncated)") {
		t.Fatalf("截断结果缺少提示: %q", got)
	}
}

func TestGitCommitMessageContextIsCompactAndKeepsFileSummary(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repository := t.TempDir()
	runGitTestCommand(t, repository, "init", "-b", "main")
	configureGitTestIdentity(t, repository)

	largePath := filepath.Join(repository, "large-change.txt")
	secondPath := filepath.Join(repository, "配置说明.md")
	writeGitTestFile(t, largePath, strings.Repeat("before\n", 2500))
	writeGitTestFile(t, secondPath, "旧配置\n")
	runGitTestCommand(t, repository, "add", ".")
	runGitTestCommand(t, repository, "commit", "-m", "seed")
	writeGitTestFile(t, largePath, strings.Repeat("after 中文变更\n", 2500))
	writeGitTestFile(t, secondPath, "新配置\n")
	runGitTestCommand(t, repository, "add", ".")

	resultJSON, err := gitGetCommitMessageContextHandler("", marshalGitTestArgs(t, repoOnlyArgs{RepoPath: repository}))
	if err != nil {
		t.Fatalf("生成提交上下文失败: %v", err)
	}
	var context string
	if err := json.Unmarshal([]byte(resultJSON), &context); err != nil {
		t.Fatalf("解析提交上下文失败: %v", err)
	}
	if len(context) > gitCommitMessageContextMax+64 {
		t.Fatalf("提交上下文过大: %d", len(context))
	}
	if !utf8.ValidString(context) {
		t.Fatal("提交上下文必须是有效 UTF-8")
	}
	if !strings.Contains(context, "large-change.txt") || !strings.Contains(context, "配置说明.md") {
		t.Fatalf("提交上下文缺少完整文件摘要: %s", context[:min(len(context), 500)])
	}
	if !strings.Contains(context, "diff truncated") {
		t.Fatal("大 diff 应被紧凑截断")
	}
}
