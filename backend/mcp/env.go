// MCP 子进程环境变量：占位符展开 + 精简继承，避免 npm 内部配置污染 npx。
// 对齐 mcp/env.rs。
package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"mincode/app/backend/core"
)

var inheritedEnvKeys = []string{
	"PATH", "HOME", "USER", "SHELL", "LANG", "LC_ALL", "LC_CTYPE",
	"TMPDIR", "TEMP", "TMP",
	"SystemRoot", "USERPROFILE", "HOMEDRIVE", "HOMEPATH",
	"APPDATA", "LOCALAPPDATA", "ProgramFiles", "ComSpec",
}

// resolveEnvEntry 展开 ${env:VAR} / ${VAR} / $VAR；若 value 与 key 同名则读取本机环境变量。
// 返回 (值, 是否保留, 错误)。
func resolveEnvEntry(key, value string) (string, bool, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", false, nil
	}

	if name := parseEnvReference(trimmed); name != "" {
		resolved, ok := os.LookupEnv(name)
		if !ok {
			return "", false, fmt.Errorf("环境变量 %s 未设置，请在系统或 ~/.zshrc 中 export %s=...", name, name)
		}
		if strings.TrimSpace(resolved) == "" {
			return "", false, fmt.Errorf("环境变量 %s 为空，无法用于 MCP 配置项 %s", name, key)
		}
		return resolved, true, nil
	}

	if trimmed == key {
		resolved, ok := os.LookupEnv(key)
		if ok && strings.TrimSpace(resolved) != "" && strings.TrimSpace(resolved) != key {
			return resolved, true, nil
		}
		return "", false, fmt.Errorf("MCP 环境变量 %s 仍是占位符，请在设置 → 外部 MCP 中填写实际值", key)
	}

	return trimmed, true, nil
}

func resolveEnvMap(env map[string]string) (map[string]string, error) {
	resolved := map[string]string{}
	for key, value := range env {
		v, keep, err := resolveEnvEntry(key, value)
		if err != nil {
			return nil, err
		}
		if keep {
			resolved[key] = v
		}
	}
	return resolved, nil
}

// buildStdioProcessEnv 清空继承后只保留常用系统变量 + MCP 专用变量，减少 npx 读取 npm 内部 config 警告。
// macOS/Linux 桌面应用通常拿不到登录 Shell 中由 nvm、Volta、Homebrew 等补充的 PATH，
// 因此在用户未显式配置 PATH 时主动合并登录 Shell PATH。
func buildStdioProcessEnv(env map[string]string) ([]string, error) {
	values := make(map[string]string, len(inheritedEnvKeys)+len(env)+2)
	for _, key := range inheritedEnvKeys {
		if value, ok := os.LookupEnv(key); ok {
			values[key] = value
		}
	}
	resolved, err := resolveEnvMap(env)
	if err != nil {
		return nil, err
	}
	for key, value := range resolved {
		values[key] = value
	}
	if _, explicit := resolved["PATH"]; !explicit && runtime.GOOS != "windows" {
		if loginPath := core.LoginShellPath(); loginPath != "" {
			values["PATH"] = mergePathLists(loginPath, values["PATH"])
		}
	}
	values["NPM_CONFIG_LOGLEVEL"] = "error"
	values["NPM_CONFIG_UPDATE_NOTIFIER"] = "false"

	out := make([]string, 0, len(values))
	for key, value := range values {
		out = append(out, key+"="+value)
	}
	return out, nil
}

// PluginProcessEnv builds the environment of a plugin MCP server. Unlike
// buildStdioProcessEnv it never expands ${env:VAR} references: values are
// used literally, so a plugin cannot read the user's host variables.
func PluginProcessEnv(env map[string]string) []string {
	values := make(map[string]string, len(inheritedEnvKeys)+len(env)+2)
	for _, key := range inheritedEnvKeys {
		if value, ok := os.LookupEnv(key); ok {
			values[key] = value
		}
	}
	if runtime.GOOS != "windows" {
		if loginPath := core.LoginShellPath(); loginPath != "" {
			values["PATH"] = mergePathLists(loginPath, values["PATH"])
		}
	}
	values["NPM_CONFIG_LOGLEVEL"] = "error"
	values["NPM_CONFIG_UPDATE_NOTIFIER"] = "false"
	for key, value := range env {
		values[key] = value
	}
	out := make([]string, 0, len(values))
	for key, value := range values {
		out = append(out, key+"="+value)
	}
	return out
}

// resolveStdioCommand 按 MCP 子进程最终 PATH 查找命令，而不是依赖桌面应用启动时的精简 PATH。
func resolveStdioCommand(command string, processEnv []string) string {
	if command == "" || strings.ContainsAny(command, `/\\`) {
		return command
	}
	pathValue := envValue(processEnv, "PATH")
	for _, dir := range filepath.SplitList(pathValue) {
		if dir == "" {
			continue
		}
		for _, name := range executableNames(command) {
			candidate := filepath.Join(dir, name)
			info, err := os.Stat(candidate)
			if err != nil || info.IsDir() {
				continue
			}
			if runtime.GOOS == "windows" || info.Mode().Perm()&0o111 != 0 {
				return candidate
			}
		}
	}
	return command
}

func executableNames(command string) []string {
	if runtime.GOOS != "windows" || filepath.Ext(command) != "" {
		return []string{command}
	}
	extensions := filepath.SplitList(os.Getenv("PATHEXT"))
	if len(extensions) == 0 {
		extensions = []string{".COM", ".EXE", ".BAT", ".CMD"}
	}
	names := make([]string, 0, len(extensions)+1)
	names = append(names, command)
	for _, extension := range extensions {
		names = append(names, command+strings.ToLower(extension), command+strings.ToUpper(extension))
	}
	return names
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for i := len(env) - 1; i >= 0; i-- {
		if strings.HasPrefix(env[i], prefix) {
			return strings.TrimPrefix(env[i], prefix)
		}
	}
	return ""
}

func mergePathLists(paths ...string) string {
	seen := map[string]struct{}{}
	var merged []string
	for _, pathValue := range paths {
		for _, dir := range filepath.SplitList(pathValue) {
			dir = strings.TrimSpace(dir)
			if dir == "" {
				continue
			}
			if _, ok := seen[dir]; ok {
				continue
			}
			seen[dir] = struct{}{}
			merged = append(merged, dir)
		}
	}
	return strings.Join(merged, string(os.PathListSeparator))
}

// parseEnvReference 解析 ${env:VAR} / ${VAR} / $VAR，返回引用的变量名（无引用则返回 ""）。
func parseEnvReference(value string) string {
	if inner, ok := strings.CutPrefix(value, "${env:"); ok {
		if name, ok := strings.CutSuffix(inner, "}"); ok {
			if n := strings.TrimSpace(name); n != "" {
				return n
			}
		}
	}
	if strings.HasPrefix(value, "${") && strings.HasSuffix(value, "}") && len(value) > 3 {
		if n := strings.TrimSpace(value[2 : len(value)-1]); n != "" {
			return n
		}
	}
	if name, ok := strings.CutPrefix(value, "$"); ok {
		if name != "" && isAllIdentChars(name) {
			return name
		}
	}
	return ""
}

func isAllIdentChars(s string) bool {
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}
