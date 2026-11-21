package git

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseNumstatZ(t *testing.T) {
	cases := []struct {
		name      string
		text      string
		additions int
		deletions int
	}{
		{name: "empty input"},
		{
			name:      "ordinary records",
			text:      "3\t1\tsrc/a.ts\x0010\t0\tsrc/b.ts\x00",
			additions: 13,
			deletions: 1,
		},
		{
			name:      "binary contributes zero",
			text:      "-\t-\tassets/logo.png\x005\t2\tsrc/a.ts\x00",
			additions: 5,
			deletions: 2,
		},
		{
			// 重命名记录的路径字段为空，旧/新路径各占一条独立 NUL 字段。
			// 若不跳过这两条，后面文件的数字会被当成新记录解析而错位。
			name:      "rename triple does not shift later records",
			text:      "4\t2\t\x00old/name.ts\x00new/name.ts\x007\t3\tsrc/c.ts\x00",
			additions: 11,
			deletions: 5,
		},
		{
			name:      "malformed record is skipped",
			text:      "not-a-record\x006\t1\tsrc/d.ts\x00",
			additions: 6,
			deletions: 1,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			additions, deletions := parseNumstatZ(testCase.text)
			if additions != testCase.additions || deletions != testCase.deletions {
				t.Fatalf("parseNumstatZ(%q) = (%d, %d), want (%d, %d)",
					testCase.text, additions, deletions, testCase.additions, testCase.deletions)
			}
		})
	}
}

func TestWorktreeChangeStatsCountsWorkingTreeAgainstHead(t *testing.T) {
	repository := createWorktreeTestRepository(t)
	writeWorktreeTestFile(t, repository, "tracked.txt", "one\ntwo\n")
	runGitTestCommand(t, repository, "add", "tracked.txt")
	runGitTestCommand(t, repository, "commit", "-m", "add tracked file")

	// 暂存一行新增，再在工作区把它改回原样。HEAD→工作区 的净差是 0，
	// 而 staged + unstaged 相加会算出 +1/-1，因此这条用例专门锁住实现方式。
	writeWorktreeTestFile(t, repository, "tracked.txt", "one\ntwo\nthree\n")
	runGitTestCommand(t, repository, "add", "tracked.txt")
	writeWorktreeTestFile(t, repository, "tracked.txt", "one\ntwo\n")
	writeWorktreeTestFile(t, repository, "fresh.txt", "alpha\nbeta\n")

	stats := worktreeChangeStats(repository)
	if stats == nil {
		t.Fatal("worktreeChangeStats returned nil for a healthy worktree")
	}
	// 只有未跟踪的 fresh.txt 贡献 2 行新增。
	if stats.Additions != 2 || stats.Deletions != 0 {
		t.Fatalf("stats = %+v, want additions=2 deletions=0 (staged edit reverted in the worktree)", *stats)
	}
}

func TestWorktreeChangeStatsDeletedTrackedFileCountsDeletions(t *testing.T) {
	repository := createWorktreeTestRepository(t)
	if err := os.Remove(filepath.Join(repository, "base.txt")); err != nil {
		t.Fatalf("remove tracked file: %v", err)
	}

	stats := worktreeChangeStats(repository)
	if stats == nil {
		t.Fatal("worktreeChangeStats returned nil")
	}
	if stats.Additions != 0 || stats.Deletions != 1 {
		t.Fatalf("stats = %+v, want additions=0 deletions=1", *stats)
	}
}

func TestWorktreeChangeStatsCleanWorktreeIsZero(t *testing.T) {
	repository := createWorktreeTestRepository(t)

	stats := worktreeChangeStats(repository)
	if stats == nil {
		t.Fatal("clean worktree must report zeroed stats, not nil")
	}
	if stats.Additions != 0 || stats.Deletions != 0 {
		t.Fatalf("stats = %+v, want zeroes", *stats)
	}
}

func TestWorktreeChangeStatsUnbornBranch(t *testing.T) {
	// 空树哈希随对象格式而变，用错格式 git 会拒绝整条 diff，两种格式都要覆盖。
	for _, objectFormat := range []string{"sha1", "sha256"} {
		t.Run(objectFormat, func(t *testing.T) {
			repository := t.TempDir()
			runGitTestCommand(t, repository, "init", "--object-format="+objectFormat)
			runGitTestCommand(t, repository, "config", "user.name", "MinCode Test")
			runGitTestCommand(t, repository, "config", "user.email", "mincode-test@example.invalid")
			writeWorktreeTestFile(t, repository, "first.txt", "line one\nline two\n")
			runGitTestCommand(t, repository, "add", "first.txt")

			stats := worktreeChangeStats(repository)
			if stats == nil {
				t.Fatal("unborn branch must still report stats against the empty tree")
			}
			if stats.Additions != 2 || stats.Deletions != 0 {
				t.Fatalf("stats = %+v, want additions=2 deletions=0", *stats)
			}
		})
	}
}

func TestWorktreeChangeStatsSkipsBinaryAndOversizedUntracked(t *testing.T) {
	repository := createWorktreeTestRepository(t)
	if err := os.WriteFile(filepath.Join(repository, "blob.bin"), []byte{0x00, 0x01, 0x02, '\n'}, 0o644); err != nil {
		t.Fatalf("write binary file: %v", err)
	}
	oversized := make([]byte, maxUntrackedStatBytes+1)
	for index := range oversized {
		oversized[index] = '\n'
	}
	if err := os.WriteFile(filepath.Join(repository, "huge.txt"), oversized, 0o644); err != nil {
		t.Fatalf("write oversized file: %v", err)
	}

	stats := worktreeChangeStats(repository)
	if stats == nil {
		t.Fatal("worktreeChangeStats returned nil")
	}
	if stats.Additions != 0 || stats.Deletions != 0 {
		t.Fatalf("stats = %+v, binary and oversized untracked files must be skipped", *stats)
	}
}

func TestGitListWorktreesReportsLinkedWorktreeStats(t *testing.T) {
	repository := createWorktreeTestRepository(t)
	linked := filepath.Join(t.TempDir(), "linked")
	runGitTestCommand(t, repository, "worktree", "add", "-b", "feature", linked)
	writeWorktreeTestFile(t, linked, "linked-only.txt", "x\ny\nz\n")

	payload := marshalGitTestArgs(t, map[string]string{"repoPath": repository})
	result, err := gitListWorktreesHandler("", payload)
	if err != nil {
		t.Fatalf("gitListWorktreesHandler: %v", err)
	}
	var entries []gitWorktreeEntry
	if err := json.Unmarshal([]byte(result), &entries); err != nil {
		t.Fatalf("decode worktree entries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d worktrees, want 2", len(entries))
	}
	if !entries[0].IsMain {
		t.Error("first entry must be flagged as the main worktree")
	}
	mainEntry := findWorktreeEntry(t, entries, repository)
	if mainEntry.Changes == nil || mainEntry.Changes.Additions != 0 {
		t.Errorf("main worktree stats = %+v, want zeroed additions", mainEntry.Changes)
	}
	linkedEntry := findWorktreeEntry(t, entries, linked)
	if linkedEntry.Changes == nil || linkedEntry.Changes.Additions != 3 {
		t.Errorf("linked worktree stats = %+v, want additions=3", linkedEntry.Changes)
	}
}

func TestGitListWorktreesOmitsStatsForPrunableWorktree(t *testing.T) {
	repository := createWorktreeTestRepository(t)
	linked := filepath.Join(t.TempDir(), "linked")
	runGitTestCommand(t, repository, "worktree", "add", "-b", "feature", linked)
	if err := os.RemoveAll(linked); err != nil {
		t.Fatalf("remove linked worktree directory: %v", err)
	}

	payload := marshalGitTestArgs(t, map[string]string{"repoPath": repository})
	result, err := gitListWorktreesHandler("", payload)
	if err != nil {
		t.Fatalf("gitListWorktreesHandler: %v", err)
	}
	var entries []gitWorktreeEntry
	if err := json.Unmarshal([]byte(result), &entries); err != nil {
		t.Fatalf("decode worktree entries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d worktrees, want the stale entry retained", len(entries))
	}
	stale := findWorktreeEntry(t, entries, linked)
	if stale.Changes != nil {
		t.Errorf("stale worktree stats = %+v, want nil", stale.Changes)
	}
}

func TestGitAddWorktreeCreatesNewBranchFromSelectedBase(t *testing.T) {
	repository := createWorktreeTestRepository(t)
	runGitTestCommand(t, repository, "branch", "base-source")
	writeWorktreeTestFile(t, repository, "main-only.txt", "newer main commit\n")
	runGitTestCommand(t, repository, "add", "main-only.txt")
	runGitTestCommand(t, repository, "commit", "-m", "advance current branch")

	linked := filepath.Join(t.TempDir(), "linked")
	payload := marshalGitTestArgs(t, map[string]any{
		"repoPath":     repository,
		"worktreePath": linked,
		"branch":       "feature-from-base",
		"baseBranch":   "base-source",
		"newBranch":    true,
	})
	if _, err := gitAddWorktreeHandler("", payload); err != nil {
		t.Fatalf("gitAddWorktreeHandler: %v", err)
	}

	wantHead, ok, err := gitLine(repository, "rev-parse", "base-source")
	if err != nil || !ok {
		t.Fatalf("resolve base-source: ok=%v err=%v", ok, err)
	}
	gotHead, ok, err := gitLine(linked, "rev-parse", "HEAD")
	if err != nil || !ok {
		t.Fatalf("resolve linked HEAD: ok=%v err=%v", ok, err)
	}
	if gotHead != wantHead {
		t.Fatalf("linked HEAD = %s, want selected base %s", gotHead, wantHead)
	}
	branch, ok, err := gitLine(linked, "branch", "--show-current")
	if err != nil || !ok || branch != "feature-from-base" {
		t.Fatalf("linked branch = %q, ok=%v err=%v", branch, ok, err)
	}
}

func TestGitRemoveWorktreeUsesDoubleForceForLockedEntry(t *testing.T) {
	repository := createWorktreeTestRepository(t)
	linked := filepath.Join(t.TempDir(), "locked")
	runGitTestCommand(t, repository, "worktree", "add", "-b", "locked-feature", linked)
	runGitTestCommand(t, repository, "worktree", "lock", "--reason", `{"owner":"supacode"}`, linked)

	listPayload := marshalGitTestArgs(t, map[string]string{"repoPath": repository})
	result, err := gitListWorktreesHandler("", listPayload)
	if err != nil {
		t.Fatalf("gitListWorktreesHandler: %v", err)
	}
	var entries []gitWorktreeEntry
	if err := json.Unmarshal([]byte(result), &entries); err != nil {
		t.Fatalf("decode worktree entries: %v", err)
	}
	entry := findWorktreeEntry(t, entries, linked)
	if !entry.IsLocked || entry.LockReason == nil || *entry.LockReason != `{"owner":"supacode"}` {
		t.Fatalf("locked entry = %+v, want preserved lock reason", entry)
	}

	ordinaryPayload := marshalGitTestArgs(t, map[string]any{
		"repoPath": repository, "worktreePath": linked, "force": true,
	})
	if _, err := gitRemoveWorktreeHandler("", ordinaryPayload); err == nil {
		t.Fatal("one force flag must not remove a locked worktree")
	}

	lockedPayload := marshalGitTestArgs(t, map[string]any{
		"repoPath": repository, "worktreePath": linked, "forceLocked": true,
	})
	if _, err := gitRemoveWorktreeHandler("", lockedPayload); err != nil {
		t.Fatalf("gitRemoveWorktreeHandler force locked: %v", err)
	}
	if _, err := os.Stat(linked); !os.IsNotExist(err) {
		t.Fatalf("locked worktree directory still exists, stat err=%v", err)
	}
}

func createWorktreeTestRepository(t *testing.T) string {
	t.Helper()
	repository := t.TempDir()
	runGitTestCommand(t, repository, "init")
	runGitTestCommand(t, repository, "config", "user.name", "MinCode Test")
	runGitTestCommand(t, repository, "config", "user.email", "mincode-test@example.invalid")
	writeWorktreeTestFile(t, repository, "base.txt", "base\n")
	runGitTestCommand(t, repository, "add", "base.txt")
	runGitTestCommand(t, repository, "commit", "-m", "seed worktree test repository")
	return repository
}

func writeWorktreeTestFile(t *testing.T, directory, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// git 对主工作树只会回一句英文 fatal，界面与 AI 都拿不到可读原因。
func TestGitLockWorktreeRejectsMainWorktreeWithReadableMessage(t *testing.T) {
	repository := createWorktreeTestRepository(t)
	linked := filepath.Join(t.TempDir(), "linked")
	runGitTestCommand(t, repository, "worktree", "add", "-b", "feature", linked)

	payload := marshalGitTestArgs(t, map[string]string{
		"repoPath":     repository,
		"worktreePath": repository,
	})
	if _, err := gitLockWorktreeHandler("", payload); err == nil {
		t.Fatal("locking the main worktree must fail")
	} else if !strings.Contains(err.Error(), "主工作树无法锁定") {
		t.Fatalf("error = %q, want the readable main-worktree message", err.Error())
	}

	// 关联工作树不受影响，校验不能挡住正常路径。
	linkedPayload := marshalGitTestArgs(t, map[string]string{
		"repoPath":     repository,
		"worktreePath": linked,
	})
	if _, err := gitLockWorktreeHandler("", linkedPayload); err != nil {
		t.Fatalf("locking a linked worktree: %v", err)
	}
}

// findWorktreeEntry 按路径定位条目。macOS 的 /var 是符号链接，git 返回解析后的
// 真实路径，而目录被删除后 EvalSymlinks 无法再解析，因此回落到后缀比较。
func findWorktreeEntry(t *testing.T, entries []gitWorktreeEntry, path string) gitWorktreeEntry {
	t.Helper()
	candidates := []string{path}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		candidates = append(candidates, resolved)
	}
	for _, entry := range entries {
		for _, candidate := range candidates {
			if entry.Path == candidate || strings.HasSuffix(entry.Path, candidate) ||
				strings.HasSuffix(candidate, entry.Path) {
				return entry
			}
		}
	}
	t.Fatalf("worktree %q not found in %+v", path, entries)
	return gitWorktreeEntry{}
}
