package configsync

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// installFakeGit 把只包含伪造 git 的目录设为 PATH，用于在不依赖真实 Git 的情况下覆盖进程管理行为。
func installFakeGit(t *testing.T, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake git script requires a POSIX shell")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func TestRunGitReturnsStdoutOnlyAndReportsStderr(t *testing.T) {
	installFakeGit(t, `if [ "$1" = fail ]; then echo "fatal: bad thing" >&2; exit 1; fi
echo "warning: noisy" >&2
printf 'abc123\n'
`)
	output, err := runGit("", repositoryCredentials{Mode: "system"}, "ok")
	if err != nil {
		t.Fatalf("runGit: %v", err)
	}
	if output != "abc123\n" {
		t.Fatalf("stderr leaked into stdout: %q", output)
	}
	_, err = runGit("", repositoryCredentials{Mode: "system"}, "fail")
	if err == nil || err.Error() != "fatal: bad thing" {
		t.Fatalf("expected stderr in error, got %v", err)
	}
}

func TestRunGitForcesCLocale(t *testing.T) {
	installFakeGit(t, `printf '%s %s' "$LC_ALL" "$LANGUAGE"`)
	t.Setenv("LC_ALL", "zh_CN.UTF-8")
	t.Setenv("LANGUAGE", "zh_CN")
	output, err := runGit("", repositoryCredentials{Mode: "system"}, "status")
	if err != nil {
		t.Fatalf("runGit: %v", err)
	}
	if output != "C C" {
		t.Fatalf("expected C locale, got %q", output)
	}
}

func TestRunGitTimeoutKillsProcessTreeHoldingPipes(t *testing.T) {
	if _, err := os.Stat("/bin/sleep"); err != nil {
		t.Skip("/bin/sleep is unavailable")
	}
	// 后台子进程继承 stdout/stderr，模拟 ssh / git-remote-https 在超时后仍持有管道。
	installFakeGit(t, "/bin/sleep 30 &\n/bin/sleep 30\n")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := runGitContext(ctx, "", repositoryCredentials{Mode: "system"}, "fetch")
	if err == nil || err.Error() != "Git 操作超时" {
		t.Fatalf("expected timeout error, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("timed-out git command hung for %s", elapsed)
	}
}

func TestAskPassHelperScripts(t *testing.T) {
	name, content := askPassHelperScript("windows")
	if name != "askpass.cmd" {
		t.Fatalf("unexpected windows helper name %q", name)
	}
	for _, want := range []string{"setlocal EnableDelayedExpansion", "echo(!MINCODE_GIT_USERNAME!", "echo(!MINCODE_GIT_TOKEN!"} {
		if !strings.Contains(content, want) {
			t.Fatalf("windows helper missing %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, "%MINCODE_GIT_") {
		t.Fatalf("windows helper must not use parse-time expansion for credentials:\n%s", content)
	}

	name, content = askPassHelperScript("darwin")
	if name != "askpass.sh" || !strings.HasPrefix(content, "#!/bin/sh\n") {
		t.Fatalf("unexpected unix helper %q:\n%s", name, content)
	}
}

func TestUnixAskPassHelperPrintsCredentialsVerbatim(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix helper only")
	}
	t.Setenv("TMPDIR", t.TempDir())
	path, cleanup, err := createAskPassHelper()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	const username = "user&|<>^)"
	const token = "tok!en&|<>^)%PATH%"
	for prompt, want := range map[string]string{
		"Username for 'https://example.com': ": username,
		"Password for 'https://example.com': ": token,
	} {
		cmd := exec.Command(path, prompt)
		cmd.Env = append(os.Environ(), "MINCODE_GIT_USERNAME="+username, "MINCODE_GIT_TOKEN="+token)
		output, err := cmd.Output()
		if err != nil {
			t.Fatalf("run askpass helper: %v", err)
		}
		if got := strings.TrimSuffix(string(output), "\n"); got != want {
			t.Fatalf("prompt %q: got %q, want %q", prompt, got, want)
		}
	}
}
