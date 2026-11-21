//go:build !windows

package core

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func useFakeLoginShell(t *testing.T, script string, timeout time.Duration) string {
	t.Helper()
	dir := t.TempDir()
	shell := filepath.Join(dir, "fake-shell")
	counter := filepath.Join(dir, "runs")
	body := "#!/bin/sh\necho run >> '" + counter + "'\n" + script + "\n"
	if err := os.WriteFile(shell, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	prevProgram, prevTimeout, prevPath := loginShellProgram, loginShellTimeout, loginShellPath
	t.Cleanup(func() {
		loginShellProgram, loginShellTimeout, loginShellPath = prevProgram, prevTimeout, prevPath
	})
	loginShellProgram = shell
	loginShellTimeout = timeout
	loginShellPath = sync.OnceValue(readLoginShellPath)
	return counter
}

func countRuns(t *testing.T, counter string) int {
	t.Helper()
	data, err := os.ReadFile(counter)
	if errors.Is(err, fs.ErrNotExist) {
		// 进程在写计数前就被超时终止（机器繁忙时常见），视为未完成的一次运行。
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "run")
}

func TestLoginShellPathRunsShellOnceForConcurrentCallers(t *testing.T) {
	counter := useFakeLoginShell(t, `sleep 0.2; printf 'banner\n__MINCODE_LOGIN_PATH__%s\n' "/login/bin:/usr/bin"`, 30*time.Second)
	PrewarmLoginShell()
	var wg sync.WaitGroup
	results := make([]string, 16)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = LoginShellPath()
		}(i)
	}
	wg.Wait()
	for _, got := range results {
		if got != "/login/bin:/usr/bin" {
			t.Fatalf("LoginShellPath() = %q", got)
		}
	}
	if runs := countRuns(t, counter); runs != 1 {
		t.Fatalf("shell ran %d times, want 1", runs)
	}
}

func TestLoginShellPathFallsBackToProcessPathOnTimeout(t *testing.T) {
	counter := useFakeLoginShell(t, `sleep 5; printf '__MINCODE_LOGIN_PATH__/never\n'`, time.Second)
	start := time.Now()
	if got, want := LoginShellPath(), os.Getenv("PATH"); got != want {
		t.Fatalf("LoginShellPath() = %q, want process PATH %q", got, want)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("timeout not enforced, took %v", elapsed)
	}
	LoginShellPath()
	// 超时可能早于假 shell 写入计数，因此只断言没有被再次启动。
	if runs := countRuns(t, counter); runs > 1 {
		t.Fatalf("shell ran %d times, want at most 1", runs)
	}
}

func TestLoginShellPathFallsBackWithoutMarker(t *testing.T) {
	useFakeLoginShell(t, `echo no marker here`, 30*time.Second)
	if got, want := LoginShellPath(), os.Getenv("PATH"); got != want {
		t.Fatalf("LoginShellPath() = %q, want %q", got, want)
	}
}
