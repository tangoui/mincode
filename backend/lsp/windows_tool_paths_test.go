//go:build windows

package lsp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mincode/app/backend/core"
)

func isolateWindowsToolPaths(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for name, value := range map[string]string{
		"PATH":              "",
		"PATHEXT":           ".COM;.EXE;.BAT;.CMD",
		"USERPROFILE":       filepath.Join(root, "home"),
		"HOME":              filepath.Join(root, "home"),
		"APPDATA":           filepath.Join(root, "roaming"),
		"LOCALAPPDATA":      filepath.Join(root, "local"),
		"ProgramFiles":      filepath.Join(root, "programs"),
		"ProgramFiles(x86)": filepath.Join(root, "programs-x86"),
		"RUBY_ROOT":         "",
		"RUBY_HOME":         "",
		"RUBY_INSTALL_DIR":  "",
		"GEM_HOME":          "",
	} {
		t.Setenv(name, value)
	}
	return root
}

func writeWindowsTool(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("测试工具占位文件"), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsToolPathDiscoveryAndEnvironment(t *testing.T) {
	root := isolateWindowsToolPaths(t)
	tools := map[string]string{
		"dotnet":              filepath.Join(root, "programs", "dotnet", "dotnet.exe"),
		"R":                   filepath.Join(root, "programs", "R", "R-4.6.1", "bin", "x64", "R.exe"),
		"Rscript":             filepath.Join(root, "local", "Programs", "R", "R-4.5.0", "bin", "Rscript.exe"),
		"lua-language-server": filepath.Join(root, "local", "Microsoft", "WinGet", "Packages", "LuaLS.lua-language-server_test", "bin", "lua-language-server.exe"),
		"gem":                 filepath.Join(root, "programs", "Ruby34-x64", "bin", "gem.cmd"),
		"ruby-lsp":            filepath.Join(root, "local", "Programs", "Ruby35-x64", "bin", "ruby-lsp.bat"),
	}
	for _, path := range tools {
		writeWindowsTool(t, path)
	}
	originalPath := filepath.Join(root, "existing-bin")
	t.Setenv("PATH", originalPath)

	var expandedPath string
	for _, entry := range core.ExpandedEnv() {
		if strings.HasPrefix(entry, "PATH=") {
			expandedPath = strings.TrimPrefix(entry, "PATH=")
		}
	}
	if !strings.HasPrefix(expandedPath, originalPath+";") {
		t.Fatalf("原有 PATH 未保留在首位：%s", expandedPath)
	}
	paths := map[string]bool{}
	for _, dir := range filepath.SplitList(expandedPath) {
		paths[strings.ToLower(dir)] = true
	}
	for command, want := range tools {
		got, err := lookPathExpanded(command)
		if err != nil || got != want {
			t.Errorf("%s 探测结果 = %q, %v；期望 %q", command, got, err, want)
		}
		if !paths[strings.ToLower(filepath.Dir(want))] {
			t.Errorf("%s 所在目录未加入子进程 PATH", command)
		}
	}
	preferredDotnet := filepath.Join(originalPath, "dotnet.exe")
	writeWindowsTool(t, preferredDotnet)
	if got, err := lookPathExpanded("dotnet"); err != nil || got != preferredDotnet {
		t.Fatalf("必须优先使用现有 PATH：%q, %v", got, err)
	}
}

func TestWindowsToolPathCandidatesLayouts(t *testing.T) {
	root := isolateWindowsToolPaths(t)
	rubyRoot := filepath.Join(root, "custom-ruby")
	gemHome := filepath.Join(root, "custom-gems")
	t.Setenv("RUBY_ROOT", rubyRoot)
	t.Setenv("RUBY_HOME", rubyRoot)
	t.Setenv("GEM_HOME", gemHome)

	expected := []string{
		filepath.Join(root, "programs-x86", "dotnet"),
		filepath.Join(root, "programs", "R", "R-4.6.1", "bin"),
		filepath.Join(root, "programs", "R", "R-4.6.1", "bin", "x64"),
		filepath.Join(root, "programs-x86", "R", "R-4.5.0", "bin"),
		filepath.Join(root, "local", "Microsoft", "WinGet", "Packages", "LuaLS.test", "lua-language-server", "bin"),
		filepath.Join(root, "programs", "Ruby34-x64", "bin"),
		filepath.Join(rubyRoot, "bin"),
		filepath.Join(gemHome, "bin"),
	}
	for _, dir := range expected {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	// 名称看似 bin 的普通文件不能进入 PATH。
	writeWindowsTool(t, filepath.Join(root, "programs", "Ruby35-x64", "bin"))
	got := core.WindowsToolPathCandidates()
	counts := map[string]int{}
	for _, dir := range got {
		counts[strings.ToLower(dir)]++
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			t.Errorf("加入了不存在或非目录路径 %q", dir)
		}
	}
	if len(got) != len(expected) {
		t.Errorf("候选目录数量 = %d，期望 %d：%v", len(got), len(expected), got)
	}
	for _, dir := range expected {
		if counts[strings.ToLower(dir)] != 1 {
			t.Errorf("目录 %q 未恰好出现一次：%v", dir, got)
		}
	}
}

func TestWindowsToolPathCandidatesEmptyEnvironment(t *testing.T) {
	isolateWindowsToolPaths(t)
	for _, name := range []string{"ProgramFiles", "ProgramFiles(x86)", "LOCALAPPDATA"} {
		t.Setenv(name, "")
	}
	if got := core.WindowsToolPathCandidates(); len(got) != 0 {
		t.Fatalf("环境路径为空时不应探测相对目录：%v", got)
	}
}
