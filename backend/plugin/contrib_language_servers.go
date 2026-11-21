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

	"mincode/app/plugins/api"
)

// Language servers (contributes.languageServers) let a process plugin that
// declares the shell capability offer a Language Server Protocol server for
// editor languages. An entry only describes a launch; nothing runs until the
// user starts it:
//
//   - the command is a bare program name looked up on the login PATH or
//     "${pluginRoot}/<relative>" (absolute paths are rejected); args and env
//     values may use ${pluginRoot} and ${projectRoot}; there is no cwd (the
//     server always runs in the project root);
//   - initializationOptions is an optional JSON object, at most 16 KiB in
//     canonical form and 16 levels deep; the host sends its own copy in
//     initialize whatever the client asks for;
//   - launches are resolved by the host from its own snapshot by (pluginId,
//     serverId, projectRoot), confirmed once per server spec (the project
//     root stays deferred in the fingerprint) and started only through
//     plugin_host_language_server_start (language_servers_launch.go);
//   - AI tools cannot start or drive plugin language servers.

const (
	languageServerPointKey   = "languageServers"
	languageServerVarProject = "${projectRoot}"
)

var (
	languageServerSpec          = mustContributionSpec(languageServerPointKey)
	languageServerTitleMaxRunes = int(languageServerSpec.Limit("titleMaxRunes"))
	languageServerLocalesMax    = int(languageServerSpec.Limit("localesMax"))
	languageServerLanguagesMax  = int(languageServerSpec.Limit("languagesMax"))
	languageServerInitMaxBytes  = int(languageServerSpec.Limit("initializationOptionsMaxBytes"))
	languageServerInitMaxDepth  = int(languageServerSpec.Limit("initializationOptionsMaxDepth"))

	languageServerIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
)

// LanguageServerContribution is one validated contributes.languageServers
// entry as kept in the snapshot, with its launch unresolved (variables
// intact). SpecFingerprint hashes the canonical entry.
type LanguageServerContribution struct {
	ID                    string            `json:"id"`
	Title                 string            `json:"title"`
	TitleLocalized        map[string]string `json:"titleLocalized,omitempty"`
	Languages             []string          `json:"languages"`
	Command               string            `json:"command"`
	Args                  []string          `json:"args"`
	Env                   map[string]string `json:"env"`
	InitializationOptions json.RawMessage   `json:"initializationOptions,omitempty"`
	Platforms             []string          `json:"platforms"`
	SpecFingerprint       string            `json:"specFingerprint"`
}

type languageServerManifestEntry struct {
	ID                    string            `json:"id"`
	Title                 string            `json:"title"`
	TitleLocalized        map[string]string `json:"titleLocalized"`
	Languages             []string          `json:"languages"`
	Command               *string           `json:"command"`
	Args                  []string          `json:"args"`
	Env                   map[string]string `json:"env"`
	InitializationOptions json.RawMessage   `json:"initializationOptions"`
	Platforms             []string          `json:"platforms"`
}

// languageServerLaunchDefinition is the unresolved launch of one entry.
type languageServerLaunchDefinition struct {
	Command string
	Args    []string
	Env     map[string]string
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:              languageServerPointKey,
		Decode:           decodeLanguageServerEntry,
		DuplicateMessage: func(id string) string { return fmt.Sprintf("language server id %q is duplicated", id) },
		IssuePrefix:      "invalid languageServers contribution",
		IssueText:        languageServerIssueText,
		EntryNoun:        "language servers",
	})
}

func languageServerIssueText(issue ContributionIssue) string {
	if issue.ID != "" {
		return fmt.Sprintf("language server %q skipped: %s", issue.ID, issue.Message)
	}
	return fmt.Sprintf("language server entry %d skipped: %s", issue.Entry, issue.Message)
}

func decodeLanguageServerEntry(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var entry languageServerManifestEntry
	if err := decodeStrictJSON(raw, &entry, true); err != nil {
		return nil, strings.TrimSpace(entry.ID), fmt.Errorf("parse language server: %w", err)
	}
	id := strings.TrimSpace(entry.ID)
	if !languageServerIDPattern.MatchString(id) {
		return nil, id, fmt.Errorf("language server id %q must match ^[a-z0-9][a-z0-9-]{0,31}$", id)
	}
	server := LanguageServerContribution{ID: id}
	title, err := validateContributionLine(entry.Title, languageServerTitleMaxRunes, true)
	if err != nil {
		return nil, id, fmt.Errorf("language server %q title %w", id, err)
	}
	server.Title = title
	if server.TitleLocalized, err = validateContributionLocalized(entry.TitleLocalized, languageServerTitleMaxRunes, languageServerLocalesMax); err != nil {
		return nil, id, fmt.Errorf("language server %q titleLocalized %w", id, err)
	}
	if server.Languages, err = validateLanguageServerLanguages(entry.Languages); err != nil {
		return nil, id, fmt.Errorf("language server %q %w", id, err)
	}
	if server.Platforms, err = normalizeLanguageServerPlatforms(entry.Platforms); err != nil {
		return nil, id, fmt.Errorf("language server %q %w", id, err)
	}
	if entry.Command == nil {
		return nil, id, fmt.Errorf("language server %q command is required", id)
	}
	definition, err := validateLanguageServerLaunch(ctx.root, *entry.Command, entry.Args, entry.Env)
	if err != nil {
		return nil, id, fmt.Errorf("language server %q %w", id, err)
	}
	server.Command, server.Args, server.Env = definition.Command, definition.Args, definition.Env
	if server.InitializationOptions, err = canonicalLanguageServerInitOptions(entry.InitializationOptions); err != nil {
		return nil, id, fmt.Errorf("language server %q initializationOptions %w", id, err)
	}
	server.SpecFingerprint = languageServerSpecFingerprint(server)
	return server, id, nil
}

// validateLanguageServerLanguages accepts 1..languagesMax unique editor
// language ids (the contributes.languages id syntax).
func validateLanguageServerLanguages(languages []string) ([]string, error) {
	return validateContributionLanguageIDs(languages, languageServerLanguagesMax)
}

func normalizeLanguageServerPlatforms(platforms []string) ([]string, error) {
	return normalizeContributionPlatforms(languageServerSpec, platforms)
}

// normalizeContributionPlatforms returns the spec's platforms list order;
// nil means every platform.
func normalizeContributionPlatforms(spec api.ContributionPointSpec, platforms []string) ([]string, error) {
	all := spec.List("platforms")
	if platforms == nil {
		return append([]string(nil), all...), nil
	}
	if len(platforms) == 0 {
		return nil, errors.New("platforms must not be empty")
	}
	seen := map[string]bool{}
	for _, platform := range platforms {
		if !slices.Contains(all, platform) {
			return nil, fmt.Errorf("platform %q must be darwin, linux or windows", platform)
		}
		if seen[platform] {
			return nil, fmt.Errorf("lists platform %q more than once", platform)
		}
		seen[platform] = true
	}
	out := []string{}
	for _, platform := range all {
		if seen[platform] {
			out = append(out, platform)
		}
	}
	return out, nil
}

// validateLanguageServerLaunch validates command, args and env with the
// current contract lists, at install and again at every prepare and start.
// root is the plugin directory; a ${pluginRoot} executable must exist there.
func validateLanguageServerLaunch(root, rawCommand string, args []string, env map[string]string) (languageServerLaunchDefinition, error) {
	return validateProjectProcessLaunch(languageServerSpec, root, rawCommand, args, env)
}

// validateProjectProcessLaunch validates a launch that runs in the project
// root (no cwd field) against spec's limits and lists: the command is a bare
// program name or ${pluginRoot}/<relative executable>; args and env values
// may use the spec's variables.
func validateProjectProcessLaunch(spec api.ContributionPointSpec, root, rawCommand string, args []string, env map[string]string) (languageServerLaunchDefinition, error) {
	command, err := validateProjectProcessCommand(spec, root, rawCommand)
	if err != nil {
		return languageServerLaunchDefinition{}, err
	}
	variables := spec.List("variables")
	definition, err := validateProjectProcessArgsEnv(spec, args, env, variables, variables)
	if err != nil {
		return languageServerLaunchDefinition{}, err
	}
	definition.Command = command
	return definition, nil
}

// validateProjectProcessCommand validates a static command: a bare program
// name or ${pluginRoot}/<relative executable> that exists in root. It returns
// the trimmed command.
func validateProjectProcessCommand(spec api.ContributionPointSpec, root, rawCommand string) (string, error) {
	commandMax := int(spec.Limit("commandMaxLength"))
	command := strings.TrimSpace(rawCommand)
	if command == "" {
		return "", errors.New("command is required")
	}
	if len(command) > commandMax {
		return "", fmt.Errorf("command exceeds %d characters", commandMax)
	}
	if err := validateProcessLaunchText(command, commandMax, []string{processLaunchVarPluginRoot}); err != nil {
		return "", fmt.Errorf("command %w", err)
	}
	if rest, ok := strings.CutPrefix(command, processLaunchVarPluginRoot); ok {
		relative, found := strings.CutPrefix(rest, "/")
		if !found {
			return "", errors.New("command must be ${pluginRoot}/<relative path>")
		}
		if _, err := checkProcessLaunchRelative(relative); err != nil {
			return "", fmt.Errorf("command %w", err)
		}
		if _, err := resolveProcessLaunchExecutable(root, relative); err != nil {
			return "", fmt.Errorf("command %w", err)
		}
	} else if strings.Contains(command, "${") {
		return "", errors.New("command may only use ${pluginRoot} as a prefix")
	} else if !processLaunchBareCommand.MatchString(command) {
		return "", errors.New("command must be a bare program name or ${pluginRoot}/<relative path> (absolute paths are not allowed)")
	}
	return command, nil
}

// validateProjectProcessArgsEnv validates args (which may use argVariables)
// and env (names against the spec's denylist, values may use envVariables)
// against spec's limits. The returned definition has no Command.
func validateProjectProcessArgsEnv(spec api.ContributionPointSpec, args []string, env map[string]string, argVariables, envVariables []string) (languageServerLaunchDefinition, error) {
	argsMax, argMaxRunes, argsTotalBytes := int(spec.Limit("argsMax")), int(spec.Limit("argMaxRunes")), int(spec.Limit("argsTotalBytes"))
	envMax, envValueMaxRunes := int(spec.Limit("envMax")), int(spec.Limit("envValueMaxRunes"))
	definition := languageServerLaunchDefinition{Args: []string{}, Env: map[string]string{}}

	if len(args) > argsMax {
		return languageServerLaunchDefinition{}, fmt.Errorf("has more than %d args", argsMax)
	}
	total := 0
	for index, arg := range args {
		if err := validateProcessLaunchText(arg, argMaxRunes, argVariables); err != nil {
			return languageServerLaunchDefinition{}, fmt.Errorf("args[%d] %w", index, err)
		}
		total += len(arg)
		definition.Args = append(definition.Args, arg)
	}
	if total > argsTotalBytes {
		return languageServerLaunchDefinition{}, fmt.Errorf("args exceed %d bytes in total", argsTotalBytes)
	}

	if len(env) > envMax {
		return languageServerLaunchDefinition{}, fmt.Errorf("env has more than %d variables", envMax)
	}
	for _, name := range slices.Sorted(maps.Keys(env)) {
		if err := validateProcessLaunchEnvName(spec, name); err != nil {
			return languageServerLaunchDefinition{}, err
		}
		if err := validateProcessLaunchText(env[name], envValueMaxRunes, envVariables); err != nil {
			return languageServerLaunchDefinition{}, fmt.Errorf("env %s %w", name, err)
		}
		definition.Env[name] = env[name]
	}
	return definition, nil
}

// canonicalLanguageServerInitOptions returns the strict canonical form of an
// initializationOptions object, or nil when absent.
func canonicalLanguageServerInitOptions(raw json.RawMessage) (json.RawMessage, error) {
	return canonicalContributionObject(raw, languageServerInitMaxBytes, languageServerInitMaxDepth)
}

// canonicalContributionObject returns the strict canonical form of a JSON
// object of at most maxBytes (canonical) and maxDepth levels, or nil when
// raw is absent.
func canonicalContributionObject(raw json.RawMessage, maxBytes, maxDepth int) (json.RawMessage, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, nil
	}
	if !strings.HasPrefix(trimmed, "{") {
		return nil, errors.New("must be a JSON object")
	}
	canonical, err := strictJSONWalk([]byte(trimmed), func(path []string, _ string, value any) error {
		if value != jsonObjectValue && value != jsonArrayValue {
			return nil
		}
		// The root (path nil) is depth 1; path lists the ancestors below it.
		depth := 1
		if path != nil {
			depth = len(path) + 2
		}
		if depth > maxDepth {
			return fmt.Errorf("is nested deeper than %d levels", maxDepth)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(canonical) > maxBytes {
		return nil, fmt.Errorf("exceeds %d bytes", maxBytes)
	}
	return canonical, nil
}

func languageServerSpecFingerprint(server LanguageServerContribution) string {
	copy := server
	copy.SpecFingerprint = ""
	canonical, _ := json.Marshal(copy)
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}
