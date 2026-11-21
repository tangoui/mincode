package core

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// WindowsToolPathCandidates 补充 GUI 进程常遗漏的 Windows 工具目录，
// 只检查常见安装位置，不递归扫描磁盘。
func WindowsToolPathCandidates() []string {
	if runtime.GOOS != "windows" {
		return nil
	}

	var candidates []string
	seen := map[string]bool{}
	appendDir := func(dir string) {
		if dir == "" {
			return
		}
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			return
		}
		key := strings.ToLower(filepath.Clean(dir))
		if !seen[key] {
			seen[key] = true
			candidates = append(candidates, dir)
		}
	}
	appendGlob := func(pattern string) {
		if pattern == "" {
			return
		}
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return
		}
		for _, match := range matches {
			appendDir(match)
		}
	}

	programFiles := os.Getenv("ProgramFiles")
	programFilesX86 := os.Getenv("ProgramFiles(x86)")
	localAppData := os.Getenv("LOCALAPPDATA")

	// 官方 .NET 安装目录。
	for _, base := range []string{programFiles, programFilesX86} {
		if base != "" {
			appendDir(filepath.Join(base, "dotnet"))
		}
	}

	// Windows R 的可执行文件可能位于 bin 或 bin/x64。
	for _, base := range []string{programFiles, programFilesX86} {
		if base != "" {
			appendGlob(filepath.Join(base, "R", "R-*", "bin", "x64"))
			appendGlob(filepath.Join(base, "R", "R-*", "bin"))
		}
	}
	if localAppData != "" {
		appendGlob(filepath.Join(localAppData, "Programs", "R", "R-*", "bin", "x64"))
		appendGlob(filepath.Join(localAppData, "Programs", "R", "R-*", "bin"))
	}

	// WinGet Lua 包通常直接在 bin 下提供可执行文件。
	if localAppData != "" {
		packages := filepath.Join(localAppData, "Microsoft", "WinGet", "Packages")
		appendGlob(filepath.Join(packages, "*Lua*", "bin"))
		appendGlob(filepath.Join(packages, "*Lua*", "*", "bin"))
	}

	// Dart WinGet portable archives do not update an already-running GUI's PATH.
	if localAppData != "" {
		packages := filepath.Join(localAppData, "Microsoft", "WinGet", "Packages")
		appendGlob(filepath.Join(packages, "Google.DartSDK_*", "dart-sdk", "bin"))
		appendGlob(filepath.Join(packages, "Google.DartSDK_*", "*", "dart-sdk", "bin"))
	}
	for _, name := range []string{"DART_SDK", "FLUTTER_ROOT"} {
		if root := os.Getenv(name); root != "" {
			appendDir(filepath.Join(root, "bin"))
		}
	}
	for _, base := range []string{programFiles, programFilesX86} {
		if base != "" {
			appendDir(filepath.Join(base, "Dart", "dart-sdk", "bin"))
			appendGlob(filepath.Join(base, "WinGet", "Packages", "Google.DartSDK_*", "dart-sdk", "bin"))
		}
	}

	// RubyInstaller 与 RubyGems 目录。优先使用环境变量，其余仅匹配常见安装根目录。
	for _, name := range []string{"RUBY_ROOT", "RUBY_HOME", "RUBY_INSTALL_DIR"} {
		if root := os.Getenv(name); root != "" {
			appendDir(filepath.Join(root, "bin"))
		}
	}
	if gemHome := os.Getenv("GEM_HOME"); gemHome != "" {
		appendDir(filepath.Join(gemHome, "bin"))
	}
	for _, base := range []string{programFiles, programFilesX86} {
		if base != "" {
			appendGlob(filepath.Join(base, "Ruby*", "bin"))
		}
	}
	if localAppData != "" {
		appendGlob(filepath.Join(localAppData, "Programs", "Ruby*", "bin"))
	}

	return candidates
}
