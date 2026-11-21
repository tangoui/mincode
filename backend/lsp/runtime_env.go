package lsp

import (
	"os"
	"path/filepath"
	"strings"

	"mincode/app/backend/core"
)

// GUI PATH expansion discovers tools, but apphosts and env shebangs also need
// the runtime belonging to the installed tool. Keep these overrides scoped.
// log (optional) receives path-free lifecycle notes.
func builtinServerEnv(definition serverDefinition, executable string, log func(string)) []string {
	env := core.ExpandedEnv()
	if definition.ID == jdtlsServerID {
		env = jdtlsJavaHomeEnv(env, executable, log)
	}
	if definition.ID == "csharp-ls" || definition.ID == "fsautocomplete" {
		if os.Getenv("DOTNET_ROOT") == "" {
			if dotnet, err := cachedLookPath("dotnet"); err == nil {
				if resolved, err := filepath.EvalSymlinks(dotnet); err == nil {
					dotnet = resolved
				}
				for _, root := range []string{filepath.Dir(dotnet), filepath.Join(filepath.Dir(filepath.Dir(dotnet)), "libexec")} {
					if info, err := os.Stat(filepath.Join(root, "host", "fxr")); err == nil && info.IsDir() {
						env = core.MergeEnv(env, map[string]string{"DOTNET_ROOT": root})
						break
					}
				}
			}
		}
	}
	if definition.ID == "ruby-lsp" {
		resolved, err := filepath.EvalSymlinks(executable)
		if err == nil {
			// Homebrew gem wrappers use env ruby; the system Ruby cannot load their gems.
			marker := string(filepath.Separator) + "lib" + string(filepath.Separator) + "ruby" + string(filepath.Separator) + "gems" + string(filepath.Separator)
			if index := strings.Index(resolved, marker); index >= 0 {
				prefix := resolved[:index]
				for _, dir := range []string{filepath.Join(prefix, "opt", "ruby", "bin"), filepath.Join(prefix, "bin")} {
					if info, err := os.Stat(filepath.Join(dir, "ruby")); err == nil && !info.IsDir() {
						for _, entry := range env {
							if strings.HasPrefix(entry, "PATH=") {
								env = core.MergeEnv(env, map[string]string{"PATH": dir + string(os.PathListSeparator) + strings.TrimPrefix(entry, "PATH=")})
								break
							}
						}
						break
					}
				}
			}
		}
	}
	return env
}
