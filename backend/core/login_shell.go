package core

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

var (
	loginShellTimeout = 3 * time.Second
	// loginShellProgram 为空时按 $SHELL 与常见候选解析；测试可替换为假 shell。
	loginShellProgram string
	loginShellPath    = sync.OnceValue(readLoginShellPath)
)

// LoginShellPath 返回登录 shell 中的完整 PATH，弥补 macOS 从 Finder/Dock 启动的应用
// 只继承精简 PATH 的问题。整个进程只启动一次交互式登录 shell，且最多等待 3 秒；
// Windows、超时或失败时回退为当前进程的 PATH。
func LoginShellPath() string {
	return loginShellPath()
}

// PrewarmLoginShell 在后台提前完成登录 shell 查询，避免首个调用方同步等待。
func PrewarmLoginShell() {
	go loginShellPath()
}

func readLoginShellPath() string {
	fallback := os.Getenv("PATH")
	if runtime.GOOS == "windows" {
		return fallback
	}
	shell := resolveLoginShell()
	if shell == "" {
		return fallback
	}
	const marker = "__MINCODE_LOGIN_PATH__"
	printPath := "printf '\\n" + marker + "%s\\n' \"$PATH\""
	args := []string{"-i", "-l", "-c", printPath}
	if filepath.Base(shell) == "fish" {
		args = []string{"-l", "-c", printPath}
	}
	ctx, cancel := context.WithTimeout(context.Background(), loginShellTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, args...)
	// 登录脚本可能留下继承 stdout 的后台子进程，超时后不能无限等管道关闭。
	cmd.WaitDelay = 500 * time.Millisecond
	out, err := cmd.Output()
	if err != nil {
		return fallback
	}
	idx := strings.LastIndex(string(out), marker)
	if idx < 0 {
		return fallback
	}
	line, _, _ := strings.Cut(string(out)[idx+len(marker):], "\n")
	if line = strings.TrimSpace(line); line != "" {
		return line
	}
	return fallback
}

func resolveLoginShell() string {
	candidates := []string{loginShellProgram}
	if loginShellProgram == "" {
		candidates = []string{strings.TrimSpace(os.Getenv("SHELL")), "/bin/zsh", "/bin/bash", "/bin/sh"}
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}
