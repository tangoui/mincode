package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// Terminal profiles (contributes.terminalProfiles) let a process plugin that
// declares the shell capability offer named launchers in the terminal "+"
// menu. An entry only describes a launch; it never runs anything by itself:
//
//   - the launch spec is resolved by the host from this snapshot by
//     (pluginId, profileId) — the WebView never supplies command, args or env
//     (terminal_profiles_launch.go);
//   - every first launch, and every launch after the resolved spec or a
//     ${pluginRoot} executable changed, needs an explicit user confirmation in
//     every approval mode;
//   - AI tools cannot launch plugin profiles.
//
// Commands are exactly one of "${pluginRoot}/<relative>", a bare program name
// looked up on the login PATH, or an absolute path. Arguments, environment
// values and cwd may only use the contract variables; environment names on
// the contract denylist (PATH, LD_PRELOAD, NODE_OPTIONS, ...) are rejected
// here and again at launch.

const terminalProfilePointKey = "terminalProfiles"

var (
	terminalProfileSpec             = mustContributionSpec(terminalProfilePointKey)
	terminalProfileTitleMaxRunes    = int(terminalProfileSpec.Limit("titleMaxRunes"))
	terminalProfileLocalesMax       = int(terminalProfileSpec.Limit("localesMax"))
	terminalProfileArgsMax          = int(terminalProfileSpec.Limit("argsMax"))
	terminalProfileArgMaxRunes      = int(terminalProfileSpec.Limit("argMaxRunes"))
	terminalProfileArgsTotalBytes   = int(terminalProfileSpec.Limit("argsTotalBytes"))
	terminalProfileEnvMax           = int(terminalProfileSpec.Limit("envMax"))
	terminalProfileEnvValueMaxRunes = int(terminalProfileSpec.Limit("envValueMaxRunes"))
	terminalProfileCommandMaxLength = int(terminalProfileSpec.Limit("commandMaxLength"))
	terminalProfileVariableList     = terminalProfileSpec.List("variables")
	terminalProfileVariables        = stringSet(terminalProfileVariableList)

	terminalProfileIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
)

const (
	terminalProfileVarWorkspace  = "${workspaceFolder}"
	terminalProfileVarUserHome   = "${userHome}"
	terminalProfileVarPluginRoot = "${pluginRoot}"
)

var terminalProfilePlatforms = []string{"darwin", "linux", "windows"}

// TerminalProfileContribution is one validated contributes.terminalProfiles
// entry as sent to the frontend consumer. Command, Args, Env and Cwd are the
// unresolved spec (variables intact) for display; launches always resolve the
// host's own copy. SpecFingerprint is sha256 of the canonical unresolved spec.
type TerminalProfileContribution struct {
	ID              string            `json:"id"`
	Title           string            `json:"title"`
	TitleLocalized  map[string]string `json:"titleLocalized,omitempty"`
	Icon            string            `json:"icon,omitempty"`
	Command         string            `json:"command"`
	Args            []string          `json:"args"`
	Env             map[string]string `json:"env"`
	Cwd             string            `json:"cwd"`
	Platforms       []string          `json:"platforms"`
	SpecFingerprint string            `json:"specFingerprint"`
}

type terminalProfileManifestEntry struct {
	ID             string            `json:"id"`
	Title          string            `json:"title"`
	TitleLocalized map[string]string `json:"titleLocalized"`
	Icon           string            `json:"icon"`
	Command        string            `json:"command"`
	Args           []string          `json:"args"`
	Env            map[string]string `json:"env"`
	Cwd            *string           `json:"cwd"`
	Platforms      []string          `json:"platforms"`
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:              terminalProfilePointKey,
		Decode:           decodeTerminalProfileEntry,
		DuplicateMessage: func(id string) string { return fmt.Sprintf("terminal profile id %q is duplicated", id) },
		IssuePrefix:      "invalid terminalProfiles contribution",
		IssueText:        terminalProfileIssueText,
		EntryNoun:        "terminal profiles",
	})
}

func terminalProfileIssueText(issue ContributionIssue) string {
	if issue.ID != "" {
		return fmt.Sprintf("terminal profile %q skipped: %s", issue.ID, issue.Message)
	}
	return fmt.Sprintf("terminal profile entry %d skipped: %s", issue.Entry, issue.Message)
}

func decodeTerminalProfileEntry(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var entry terminalProfileManifestEntry
	if err := decodeStrictJSON(raw, &entry, true); err != nil {
		return nil, "", fmt.Errorf("parse terminal profile: %w", err)
	}
	id := strings.TrimSpace(entry.ID)
	if !terminalProfileIDPattern.MatchString(id) {
		return nil, id, fmt.Errorf("terminal profile id %q must match ^[a-z0-9][a-z0-9-]{0,31}$", id)
	}
	profile := TerminalProfileContribution{ID: id, Title: strings.TrimSpace(entry.Title)}
	if err := validateTerminalProfileTitle(profile.Title); err != nil {
		return nil, id, fmt.Errorf("terminal profile %q %w", id, err)
	}
	if entry.TitleLocalized != nil {
		if len(entry.TitleLocalized) > terminalProfileLocalesMax {
			return nil, id, fmt.Errorf("terminal profile %q titleLocalized exceeds %d locales", id, terminalProfileLocalesMax)
		}
		localized := make(map[string]string, len(entry.TitleLocalized))
		for locale, title := range entry.TitleLocalized {
			locale = strings.TrimSpace(locale)
			if locale == "" || len(locale) > 32 {
				return nil, id, fmt.Errorf("terminal profile %q titleLocalized locale %q is invalid", id, locale)
			}
			title = strings.TrimSpace(title)
			if err := validateTerminalProfileTitle(title); err != nil {
				return nil, id, fmt.Errorf("terminal profile %q titleLocalized[%s] %w", id, locale, err)
			}
			localized[locale] = title
		}
		if len(localized) > 0 {
			profile.TitleLocalized = localized
		}
	}
	if icon := strings.TrimSpace(entry.Icon); icon != "" {
		if _, ok := pluginViewIcons[icon]; !ok {
			return nil, id, fmt.Errorf("terminal profile %q icon %q is not a known view icon", id, icon)
		}
		profile.Icon = icon
	}

	command, err := validateTerminalProfileCommand(ctx.root, entry.Command)
	if err != nil {
		return nil, id, fmt.Errorf("terminal profile %q command %w", id, err)
	}
	profile.Command = command

	if len(entry.Args) > terminalProfileArgsMax {
		return nil, id, fmt.Errorf("terminal profile %q has more than %d args", id, terminalProfileArgsMax)
	}
	profile.Args = make([]string, 0, len(entry.Args))
	total := 0
	for index, arg := range entry.Args {
		if err := validateTerminalProfileText(arg, terminalProfileArgMaxRunes, true); err != nil {
			return nil, id, fmt.Errorf("terminal profile %q args[%d] %w", id, index, err)
		}
		total += len(arg)
		profile.Args = append(profile.Args, arg)
	}
	if total > terminalProfileArgsTotalBytes {
		return nil, id, fmt.Errorf("terminal profile %q args exceed %d bytes in total", id, terminalProfileArgsTotalBytes)
	}

	if len(entry.Env) > terminalProfileEnvMax {
		return nil, id, fmt.Errorf("terminal profile %q env has more than %d variables", id, terminalProfileEnvMax)
	}
	profile.Env = make(map[string]string, len(entry.Env))
	for _, name := range slices.Sorted(maps.Keys(entry.Env)) {
		value := entry.Env[name]
		if err := validateTerminalProfileEnvName(name); err != nil {
			return nil, id, fmt.Errorf("terminal profile %q %w", id, err)
		}
		if err := validateTerminalProfileText(value, terminalProfileEnvValueMaxRunes, true); err != nil {
			return nil, id, fmt.Errorf("terminal profile %q env %s %w", id, name, err)
		}
		profile.Env[name] = value
	}

	profile.Cwd = terminalProfileVarWorkspace
	if entry.Cwd != nil {
		cwd, err := validateTerminalProfileCwd(*entry.Cwd)
		if err != nil {
			return nil, id, fmt.Errorf("terminal profile %q cwd %w", id, err)
		}
		profile.Cwd = cwd
	}

	if entry.Platforms == nil {
		profile.Platforms = append([]string(nil), terminalProfilePlatforms...)
	} else {
		if len(entry.Platforms) == 0 {
			return nil, id, fmt.Errorf("terminal profile %q platforms must not be empty", id)
		}
		seen := map[string]bool{}
		for _, platform := range entry.Platforms {
			if !slices.Contains(terminalProfilePlatforms, platform) {
				return nil, id, fmt.Errorf("terminal profile %q platform %q must be darwin, linux or windows", id, platform)
			}
			if seen[platform] {
				return nil, id, fmt.Errorf("terminal profile %q lists platform %q more than once", id, platform)
			}
			seen[platform] = true
		}
		for _, platform := range terminalProfilePlatforms {
			if seen[platform] {
				profile.Platforms = append(profile.Platforms, platform)
			}
		}
	}
	profile.SpecFingerprint = terminalProfileSpecFingerprint(profile)
	return profile, id, nil
}

func validateTerminalProfileTitle(title string) error {
	if !utf8.ValidString(title) || strings.TrimSpace(title) == "" {
		return errors.New("title must be non-empty UTF-8")
	}
	if utf8.RuneCountInString(title) > terminalProfileTitleMaxRunes {
		return fmt.Errorf("title exceeds %d characters", terminalProfileTitleMaxRunes)
	}
	for _, r := range title {
		if isStatusBarUnsafeRune(r, false) {
			return errors.New("title must be a single line without control or bidirectional override characters")
		}
	}
	return nil
}

// validateTerminalProfileText checks an argument or environment value: valid
// UTF-8, bounded, single line, no bidi overrides, and "${...}" only for the
// contract variables. "$VAR" stays a literal (no shell runs in between).
func validateTerminalProfileText(value string, maxRunes int, allowVariables bool) error {
	variables := terminalProfileVariableList
	if !allowVariables {
		variables = nil
	}
	return validateProcessLaunchText(value, maxRunes, variables)
}

// validateTerminalProfileEnvName enforces the name pattern and the contract
// denylist (names and prefixes, case-insensitive). Used at install and again
// at launch.
func validateTerminalProfileEnvName(name string) error {
	return validateProcessLaunchEnvName(terminalProfileSpec, name)
}

// validateTerminalProfileCommand returns the canonical command string.
func validateTerminalProfileCommand(root, raw string) (string, error) {
	command := strings.TrimSpace(raw)
	if command == "" {
		return "", errors.New("is required")
	}
	if len(command) > terminalProfileCommandMaxLength {
		return "", fmt.Errorf("exceeds %d characters", terminalProfileCommandMaxLength)
	}
	for _, r := range command {
		if isStatusBarUnsafeRune(r, false) {
			return "", errors.New("must not contain control or bidirectional override characters")
		}
	}
	if !utf8.ValidString(command) {
		return "", errors.New("must be valid UTF-8")
	}
	if strings.HasPrefix(command, terminalProfileVarPluginRoot) {
		rest := strings.TrimPrefix(command, terminalProfileVarPluginRoot)
		if !strings.HasPrefix(rest, "/") {
			return "", errors.New("must be ${pluginRoot}/<relative path>")
		}
		relative := strings.TrimPrefix(rest, "/")
		if _, err := checkTerminalProfileRelative(relative); err != nil {
			return "", err
		}
		if _, err := resolveTerminalProfileExecutable(root, relative); err != nil {
			return "", err
		}
		return command, nil
	}
	if strings.Contains(command, "${") {
		return "", errors.New("may only use ${pluginRoot} as a prefix")
	}
	if processLaunchBareCommand.MatchString(command) {
		return command, nil
	}
	if strings.HasPrefix(command, "/") || processLaunchWindowsAbsolute.MatchString(command) {
		for _, segment := range strings.FieldsFunc(command, func(r rune) bool { return r == '/' || r == '\\' }) {
			if segment == ".." {
				return "", errors.New("must not contain .. segments")
			}
		}
		return command, nil
	}
	return "", errors.New("must be ${pluginRoot}/<relative path>, a bare program name, or an absolute path")
}

// checkTerminalProfileRelative validates a "/"-separated package-relative
// path without empty, "." or ".." segments.
func checkTerminalProfileRelative(relative string) (string, error) {
	return checkProcessLaunchRelative(relative)
}

// resolveTerminalProfileExecutable resolves a ${pluginRoot} executable
// symlink-safely inside the plugin root and requires a regular file (with an
// execute bit on Unix).
func resolveTerminalProfileExecutable(root, relative string) (string, error) {
	return resolveProcessLaunchExecutable(root, relative)
}

// validateTerminalProfileCwd accepts a variable optionally followed by a
// relative path; absolute paths and ".." are rejected.
func validateTerminalProfileCwd(raw string) (string, error) {
	cwd := strings.TrimSpace(raw)
	for variable := range terminalProfileVariables {
		if cwd == variable {
			return cwd, nil
		}
		if rest, ok := strings.CutPrefix(cwd, variable+"/"); ok {
			if strings.Contains(rest, "${") {
				return "", errors.New("may use only one variable, as a prefix")
			}
			if err := validateTerminalProfileText(rest, terminalProfileCommandMaxLength, false); err != nil {
				return "", err
			}
			if _, err := checkTerminalProfileRelative(rest); err != nil {
				return "", err
			}
			return cwd, nil
		}
	}
	return "", errors.New("must be ${workspaceFolder}, ${userHome} or ${pluginRoot}, optionally followed by /<relative path>")
}

// terminalProfileSpecFingerprint hashes the canonical unresolved spec.
func terminalProfileSpecFingerprint(profile TerminalProfileContribution) string {
	canonical, _ := json.Marshal(struct {
		Command   string            `json:"command"`
		Args      []string          `json:"args"`
		Env       map[string]string `json:"env"`
		Cwd       string            `json:"cwd"`
		Platforms []string          `json:"platforms"`
	}{profile.Command, profile.Args, profile.Env, profile.Cwd, profile.Platforms})
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

func upperSet(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[strings.ToUpper(value)] = true
	}
	return out
}

func upperList(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, strings.ToUpper(value))
	}
	return out
}
