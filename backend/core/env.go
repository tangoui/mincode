package core

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// ExpandedEnv returns the current environment with PATH augmented by common
// package-manager directories so child processes can find their dependencies.
func ExpandedEnv() []string {
	home, _ := os.UserHomeDir()
	var extras []string
	pathSep := ":"
	if runtime.GOOS == "windows" {
		pathSep = ";"
		appData := os.Getenv("APPDATA")
		localAppData := os.Getenv("LOCALAPPDATA")
		programFiles := os.Getenv("ProgramFiles")
		extras = []string{
			filepath.Join(appData, "npm"),
			filepath.Join(home, "go", "bin"),
			filepath.Join(home, ".cargo", "bin"),
			filepath.Join(home, ".dotnet", "tools"),
			filepath.Join(home, "scoop", "shims"),
			filepath.Join(localAppData, "Microsoft", "WinGet", "Links"),
			filepath.Join(programFiles, "LLVM", "bin"),
			filepath.Join(programFiles, "Go", "bin"),
		}
		for _, pattern := range []string{
			filepath.Join(appData, "nvm", "v*"),
			filepath.Join(localAppData, "nvm", "v*"),
			filepath.Join(localAppData, "fnm", "node-versions", "*", "installation"),
			filepath.Join(localAppData, "Microsoft", "fnm", "node-versions", "*", "installation"),
		} {
			if matches, err := filepath.Glob(pattern); err == nil {
				extras = append(extras, matches...)
			}
		}
		if localAppData != "" {
			extras = append(extras, filepath.Join(localAppData, "Volta", "bin"))
		}
		extras = append(extras, WindowsToolPathCandidates()...)
	} else {
		extras = []string{
			"/usr/local/bin",
			"/opt/homebrew/bin",
			"/opt/homebrew/sbin",
			"/opt/homebrew/opt/llvm/bin",
			"/usr/local/opt/llvm/bin",
			"/opt/homebrew/opt/ruby/bin",
			"/usr/local/opt/ruby/bin",
			filepath.Join(home, "go/bin"),
			filepath.Join(home, ".cargo/bin"),
			filepath.Join(home, ".npm-global/bin"),
			filepath.Join(home, ".local/bin"),
			"/usr/local/go/bin",
			"/usr/local/share/dotnet",
			"/usr/local/share/dotnet/x64",
			filepath.Join(home, ".dotnet/tools"),
		}
		// Add Homebrew Ruby gem bin dirs (versioned) to PATH so language servers
		// installed via `gem install` can be discovered by the child processes.
		for _, pattern := range []string{
			"/opt/homebrew/lib/ruby/gems/*/bin",
			"/usr/local/lib/ruby/gems/*/bin",
			filepath.Join(home, ".gem/ruby/*/bin"),
			filepath.Join(home, ".local/share/gem/ruby/*/bin"),
		} {
			if matches, err := filepath.Glob(pattern); err == nil {
				extras = append(extras, matches...)
			}
		}
		// Node version managers: nvm, fnm, asdf, volta.
		for _, pattern := range []string{
			filepath.Join(home, ".nvm/versions/node/*/bin"),
			filepath.Join(home, ".local/share/fnm/node-versions/*/installation/bin"),
			filepath.Join(home, ".asdf/installs/nodejs/*/bin"),
			filepath.Join(home, ".volta/bin"),
		} {
			if matches, err := filepath.Glob(pattern); err == nil {
				extras = append(extras, matches...)
			}
		}
		// opam switch 的 bin 目录（ocamllsp 等），与探测列表保持一致。
		for _, pattern := range []string{
			filepath.Join(home, ".opam/*/bin"),
		} {
			if matches, err := filepath.Glob(pattern); err == nil {
				extras = append(extras, matches...)
			}
		}
	}
	currentPath := os.Getenv("PATH")
	seen := map[string]bool{}
	for _, dir := range strings.Split(currentPath, pathSep) {
		if runtime.GOOS == "windows" {
			dir = strings.ToLower(dir)
		}
		seen[dir] = true
	}
	for _, dir := range extras {
		key := dir
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if !seen[key] {
			if currentPath != "" {
				currentPath += pathSep
			}
			currentPath += dir
			seen[key] = true
		}
	}
	env := make([]string, 0, len(os.Environ()))
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "PATH=") {
			env = append(env, e)
		}
	}
	return append(env, "PATH="+currentPath)
}

// MergeEnv overrides base entries with extra (names compared
// case-insensitively on Windows).
func MergeEnv(base []string, extra map[string]string) []string {
	if len(extra) == 0 {
		return base
	}
	same := func(a, b string) bool {
		if runtime.GOOS == "windows" {
			return strings.EqualFold(a, b)
		}
		return a == b
	}
	out := make([]string, 0, len(base)+len(extra))
	for _, entry := range base {
		name, _, _ := strings.Cut(entry, "=")
		overridden := false
		for key := range extra {
			if same(name, key) {
				overridden = true
				break
			}
		}
		if !overridden {
			out = append(out, entry)
		}
	}
	names := make([]string, 0, len(extra))
	for name := range extra {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		out = append(out, name+"="+extra[name])
	}
	return out
}
