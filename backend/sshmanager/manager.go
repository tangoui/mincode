package sshmanager

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"mincode/app/backend/core"
	"mincode/app/backend/sshauth"
)

func init() {
	core.Register("ssh_known_hosts_list", listKnownHosts)
	core.Register("ssh_known_hosts_remove", removeKnownHost)
	core.Register("ssh_config_discover", discoverSSHConfig)
}

type knownHostsResult struct {
	Path    string                   `json:"path"`
	Entries []sshauth.KnownHostEntry `json:"entries"`
}

type removeKnownHostPayload struct {
	ID string `json:"id"`
}

func listKnownHosts(_, _ string) (string, error) {
	path, entries, err := sshauth.ListKnownHosts()
	if err != nil {
		return "", err
	}
	return core.EncodeResult(knownHostsResult{Path: path, Entries: entries})
}

func removeKnownHost(_, args string) (string, error) {
	var p removeKnownHostPayload
	if err := core.DecodeArgs(args, &p); err != nil {
		return "", err
	}
	if strings.TrimSpace(p.ID) == "" {
		return "", fmt.Errorf("known_hosts entry id is required")
	}
	if err := sshauth.RemoveKnownHost(p.ID); err != nil {
		return "", err
	}
	return core.EncodeResult(true)
}

type ConfigProfile struct {
	Alias               string `json:"alias"`
	Host                string `json:"host"`
	Port                int    `json:"port"`
	Username            string `json:"username"`
	IdentityFile        string `json:"identityFile,omitempty"`
	ProxyJump           string `json:"proxyJump,omitempty"`
	ServerAliveInterval int    `json:"serverAliveInterval,omitempty"`
	ConnectTimeout      int    `json:"connectTimeout,omitempty"`
	IdentitiesOnly      bool   `json:"identitiesOnly,omitempty"`
	SourcePath          string `json:"sourcePath"`
}

type ConfigDiscoveryResult struct {
	Path         string          `json:"path"`
	Profiles     []ConfigProfile `json:"profiles"`
	Warnings     []string        `json:"warnings"`
	DiscoveredAt int64           `json:"discoveredAt"`
}

type configDirective struct {
	key, value, sourcePath string
	line                   int
}

type configAlias struct {
	alias, sourcePath string
}

type configProfileState struct {
	profile ConfigProfile
	set     map[string]bool
}

func discoverSSHConfig(_, args string) (string, error) {
	var payload struct {
		Path string `json:"path"`
	}
	if strings.TrimSpace(args) != "" {
		if err := core.DecodeArgs(args, &payload); err != nil {
			return "", err
		}
	}
	path := strings.TrimSpace(payload.Path)
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		path = filepath.Join(home, ".ssh", "config")
	}
	path, err := expandConfigPath(path, "")
	if err != nil {
		return "", err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	path, err = sshConfigPathWithinDirectory(path, home)
	if err != nil {
		return "", err
	}
	if info, statErr := os.Stat(path); statErr == nil && info.IsDir() {
		path = filepath.Join(path, "config")
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return "", fmt.Errorf("inspect SSH config path: %w", statErr)
	}
	path, err = sshConfigPathWithinDirectory(path, home)
	if err != nil {
		return "", err
	}
	profiles, warnings, err := parseSSHConfig(path, map[string]bool{}, 0)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(ConfigDiscoveryResult{
		Path:         path,
		Profiles:     profiles,
		Warnings:     warnings,
		DiscoveredAt: time.Now().UnixMilli(),
	})
}

func sshConfigPathWithinDirectory(path, home string) (string, error) {
	root, err := filepath.Abs(filepath.Join(home, ".ssh"))
	if err != nil {
		return "", fmt.Errorf("resolve SSH config directory: %w", err)
	}
	resolved, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("resolve SSH config path: %w", err)
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("SSH config path must stay inside %s", root)
	}
	return resolved, nil
}

func parseSSHConfig(path string, visiting map[string]bool, depth int) ([]ConfigProfile, []string, error) {
	directives, aliases, warnings, err := loadSSHConfig(path, visiting, depth)
	if err != nil {
		return nil, nil, err
	}
	profiles, buildWarnings := buildConfigProfiles(directives, aliases, currentLocalUsername())
	return profiles, append(warnings, buildWarnings...), nil
}

func loadSSHConfig(path string, visiting map[string]bool, depth int) ([]configDirective, []configAlias, []string, error) {
	if depth > 8 {
		return nil, nil, nil, fmt.Errorf("SSH config include depth exceeds 8")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("resolve SSH config path: %w", err)
	}
	if visiting[abs] {
		return nil, nil, []string{"Skipped recursive Include: " + abs}, nil
	}
	visiting[abs] = true
	defer delete(visiting, abs)

	raw, err := os.ReadFile(abs)
	if os.IsNotExist(err) {
		return []configDirective{}, []configAlias{}, []string{}, nil
	}
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read SSH config %s: %w", abs, err)
	}
	if len(raw) > 2<<20 {
		return nil, nil, nil, fmt.Errorf("SSH config exceeds 2 MiB: %s", abs)
	}

	directives := make([]configDirective, 0, 32)
	aliases := make([]configAlias, 0, 16)
	warnings := make([]string, 0)
	for lineNo, rawLine := range strings.Split(string(raw), "\n") {
		key, value, ok := parseConfigDirective(rawLine)
		if !ok {
			continue
		}
		if key == "include" {
			for _, pattern := range splitConfigWords(value) {
				expanded, expandErr := expandConfigPath(pattern, filepath.Dir(abs))
				if expandErr != nil {
					warnings = append(warnings, expandErr.Error())
					continue
				}
				matches, globErr := filepath.Glob(expanded)
				if globErr != nil {
					warnings = append(warnings, fmt.Sprintf("Invalid Include pattern at %s:%d", abs, lineNo+1))
					continue
				}
				if len(matches) == 0 {
					matches = []string{expanded}
				}
				for _, match := range matches {
					included, includedAliases, includedWarnings, includeErr := loadSSHConfig(match, visiting, depth+1)
					if includeErr != nil {
						warnings = append(warnings, includeErr.Error())
						continue
					}
					directives = append(directives, included...)
					aliases = append(aliases, includedAliases...)
					warnings = append(warnings, includedWarnings...)
				}
			}
			continue
		}

		directives = append(directives, configDirective{key: key, value: value, sourcePath: abs, line: lineNo + 1})
		if key == "host" {
			for _, alias := range splitConfigWords(value) {
				if isLiteralHostAlias(alias) {
					aliases = append(aliases, configAlias{alias: alias, sourcePath: abs})
				}
			}
		}
	}
	return directives, aliases, warnings, nil
}

func buildConfigProfiles(directives []configDirective, aliases []configAlias, defaultUser string) ([]ConfigProfile, []string) {
	seen := make(map[string]bool, len(aliases))
	profiles := make([]ConfigProfile, 0, len(aliases))
	warnings := make([]string, 0)
	for _, declaration := range aliases {
		aliasKey := strings.ToLower(strings.TrimSpace(declaration.alias))
		if aliasKey == "" || seen[aliasKey] {
			continue
		}
		seen[aliasKey] = true
		state := configProfileState{
			profile: ConfigProfile{Alias: declaration.alias, Host: declaration.alias, Port: 22, SourcePath: declaration.sourcePath},
			set:     make(map[string]bool),
		}
		active := true
		for _, directive := range directives {
			if directive.key == "host" {
				active = matchHostPatterns(declaration.alias, splitConfigWords(directive.value))
				continue
			}
			if !active {
				continue
			}
			applyConfigDirective(&state, directive, &warnings)
		}
		if strings.TrimSpace(state.profile.Username) == "" {
			state.profile.Username = defaultUser
		}
		state.profile.Host = expandConfigTokens(state.profile.Host, state.profile, declaration.alias)
		state.profile.ProxyJump = expandConfigTokens(state.profile.ProxyJump, state.profile, declaration.alias)
		profiles = append(profiles, state.profile)
	}
	return profiles, warnings
}

func applyConfigDirective(state *configProfileState, directive configDirective, warnings *[]string) {
	key, value := directive.key, strings.TrimSpace(directive.value)
	if state.set[key] {
		return
	}
	switch key {
	case "hostname":
		state.profile.Host = value
	case "user":
		state.profile.Username = value
	case "port":
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 65535 {
			*warnings = append(*warnings, fmt.Sprintf("Invalid Port at %s:%d", directive.sourcePath, directive.line))
			return
		}
		state.profile.Port = n
	case "identityfile":
		expanded, err := expandConfigPath(value, filepath.Dir(directive.sourcePath))
		if err != nil {
			*warnings = append(*warnings, fmt.Sprintf("Invalid IdentityFile at %s:%d: %v", directive.sourcePath, directive.line, err))
			return
		}
		state.profile.IdentityFile = expanded
	case "proxyjump":
		state.profile.ProxyJump = value
	case "serveraliveinterval":
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 || n > 300 {
			*warnings = append(*warnings, fmt.Sprintf("Invalid ServerAliveInterval at %s:%d", directive.sourcePath, directive.line))
			return
		}
		state.profile.ServerAliveInterval = n
	case "connecttimeout":
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 || n > 600 {
			*warnings = append(*warnings, fmt.Sprintf("Invalid ConnectTimeout at %s:%d", directive.sourcePath, directive.line))
			return
		}
		state.profile.ConnectTimeout = n
	case "identitiesonly":
		state.profile.IdentitiesOnly = parseSSHBoolean(value)
	default:
		return
	}
	state.set[key] = true
}

func parseConfigDirective(rawLine string) (string, string, bool) {
	line := strings.TrimSpace(stripConfigComment(rawLine))
	if line == "" {
		return "", "", false
	}
	separator := -1
	for i, r := range line {
		if r == '=' || r == ' ' || r == '\t' {
			separator = i
			break
		}
	}
	if separator <= 0 {
		return "", "", false
	}
	key := strings.ToLower(strings.TrimSpace(line[:separator]))
	value := strings.TrimSpace(line[separator:])
	value = strings.TrimSpace(strings.TrimPrefix(value, "="))
	if key == "" || value == "" {
		return "", "", false
	}
	return key, value, true
}

func stripConfigComment(line string) string {
	var quote rune
	escaped := false
	for i, r := range line {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			continue
		}
		if r == '#' {
			return line[:i]
		}
	}
	return line
}

func splitConfigWords(value string) []string {
	words := make([]string, 0, 4)
	var current strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		if current.Len() > 0 {
			words = append(words, current.String())
			current.Reset()
		}
	}
	for _, r := range strings.TrimSpace(value) {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				current.WriteRune(r)
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			continue
		}
		if r == ' ' || r == '\t' {
			flush()
			continue
		}
		current.WriteRune(r)
	}
	if escaped {
		current.WriteRune('\\')
	}
	flush()
	return words
}

func isLiteralHostAlias(alias string) bool {
	alias = strings.TrimSpace(alias)
	return alias != "" && !strings.HasPrefix(alias, "!") && !strings.ContainsAny(alias, "*?[")
}

func matchHostPatterns(alias string, patterns []string) bool {
	alias = strings.ToLower(alias)
	matched := false
	for _, rawPattern := range patterns {
		negated := strings.HasPrefix(rawPattern, "!")
		pattern := strings.ToLower(strings.TrimPrefix(rawPattern, "!"))
		ok, err := filepath.Match(pattern, alias)
		if err != nil {
			continue
		}
		if negated && ok {
			return false
		}
		if !negated && ok {
			matched = true
		}
	}
	return matched
}

func expandConfigTokens(value string, profile ConfigProfile, originalAlias string) string {
	if value == "" {
		return ""
	}
	replacer := strings.NewReplacer(
		"%%", "%",
		"%h", originalAlias,
		"%n", originalAlias,
		"%p", strconv.Itoa(profile.Port),
		"%r", profile.Username,
	)
	return replacer.Replace(value)
}

func parseSSHBoolean(value string) bool {
	return strings.EqualFold(value, "yes") || strings.EqualFold(value, "true") || value == "1"
}

func currentLocalUsername() string {
	if current, err := user.Current(); err == nil && current.Username != "" {
		username := current.Username
		if slash := strings.LastIndexAny(username, `\\/`); slash >= 0 {
			username = username[slash+1:]
		}
		if username != "" {
			return username
		}
	}
	for _, key := range []string{"USER", "USERNAME"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return "root"
}

func expandConfigPath(path, base string) (string, error) {
	path = strings.Trim(strings.TrimSpace(path), "\"")
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, path[2:])
	}
	if !filepath.IsAbs(path) && base != "" {
		path = filepath.Join(base, path)
	}
	return filepath.Clean(path), nil
}
