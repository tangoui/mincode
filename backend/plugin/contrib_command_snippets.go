package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// Terminal command snippets (contributes.commandSnippets) are data only. An
// entry is a library of commands the user can pick from a terminal pane; the
// frontend fills the declared variables from user input, shows the exact text
// and inserts it into the prompt. A snippet is never executed: the host never
// writes "\r", strips trailing newlines, and inserts multi-line text only
// inside bracketed paste after the paste confirmation. Command text may
// contain tabs and newlines but no other control character (ESC could end a
// bracketed paste early), no DEL/C1, no bidi override and no zero-width
// character, so what the user reviews is what reaches the shell.

const commandSnippetPointKey = "commandSnippets"

var (
	commandSnippetSpec               = mustContributionSpec(commandSnippetPointKey)
	commandSnippetMaxPerLibrary      = int(commandSnippetSpec.Limit("commandsPerLibrary"))
	commandSnippetMaxPerPlugin       = int(commandSnippetSpec.Limit("commandsPerPlugin"))
	commandSnippetMaxTitleRunes      = int(commandSnippetSpec.Limit("titleMaxRunes"))
	commandSnippetMaxDescription     = int(commandSnippetSpec.Limit("descriptionMaxRunes"))
	commandSnippetMaxLocales         = int(commandSnippetSpec.Limit("localesMax"))
	commandSnippetMaxCommandBytes    = int(commandSnippetSpec.Limit("commandMaxBytes"))
	commandSnippetMaxCommandLines    = int(commandSnippetSpec.Limit("commandMaxLines"))
	commandSnippetMaxVariables       = int(commandSnippetSpec.Limit("variablesMax"))
	commandSnippetMaxVariableTitle   = int(commandSnippetSpec.Limit("variableTitleMaxRunes"))
	commandSnippetMaxVariableDefault = int(commandSnippetSpec.Limit("variableDefaultMaxRunes"))
	commandSnippetMaxTags            = int(commandSnippetSpec.Limit("tagsMax"))
	commandSnippetMaxTagRunes        = int(commandSnippetSpec.Limit("tagMaxRunes"))
	commandSnippetMaxSnapshotBytes   = int(commandSnippetSpec.Limit("snapshotBytesPerPlugin"))
	commandSnippetTargets            = commandSnippetSpec.List("targets")
	commandSnippetPlatforms          = commandSnippetSpec.List("platforms")
	commandSnippetQuoteModes         = commandSnippetSpec.List("quoteModes")

	commandSnippetIDPattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	commandSnippetVariablePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,31}$`)
	// commandSnippetTokenPattern finds {{identifier}} placeholders. Any other
	// {{...}} text ({{.Names}}, {{json .}}) is literal command text.
	commandSnippetTokenPattern = regexp.MustCompile(`\{\{\s*([A-Za-z][A-Za-z0-9_]*)\s*\}\}`)
)

// CommandSnippetLibrary is one validated contributes.commandSnippets entry as
// sent to the frontend consumer.
type CommandSnippetLibrary struct {
	ID             string            `json:"id"`
	Title          string            `json:"title"`
	TitleLocalized map[string]string `json:"titleLocalized,omitempty"`
	Targets        []string          `json:"targets"`
	Platforms      []string          `json:"platforms"`
	Commands       []CommandSnippet  `json:"commands"`
}

// CommandSnippet is one command of a library. Command is the normalized text
// ("\n"-joined, trailing whitespace and newlines trimmed).
type CommandSnippet struct {
	ID                   string                   `json:"id"`
	Title                string                   `json:"title"`
	TitleLocalized       map[string]string        `json:"titleLocalized,omitempty"`
	Description          string                   `json:"description,omitempty"`
	DescriptionLocalized map[string]string        `json:"descriptionLocalized,omitempty"`
	Command              string                   `json:"command"`
	Variables            []CommandSnippetVariable `json:"variables"`
	Tags                 []string                 `json:"tags"`
}

// CommandSnippetVariable is a user-filled placeholder.
type CommandSnippetVariable struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Default string `json:"default,omitempty"`
	Quote   string `json:"quote"`
}

type commandSnippetLibraryReference struct {
	ID             string                           `json:"id"`
	Title          string                           `json:"title"`
	TitleLocalized map[string]string                `json:"titleLocalized"`
	Targets        []string                         `json:"targets"`
	Platforms      []string                         `json:"platforms"`
	Commands       []commandSnippetCommandReference `json:"commands"`
}

type commandSnippetCommandReference struct {
	ID                   string                            `json:"id"`
	Title                string                            `json:"title"`
	TitleLocalized       map[string]string                 `json:"titleLocalized"`
	Description          *string                           `json:"description"`
	DescriptionLocalized map[string]string                 `json:"descriptionLocalized"`
	Command              json.RawMessage                   `json:"command"`
	Variables            []commandSnippetVariableReference `json:"variables"`
	Tags                 []string                          `json:"tags"`
}

type commandSnippetVariableReference struct {
	Name    string  `json:"name"`
	Title   *string `json:"title"`
	Default *string `json:"default"`
	Quote   *string `json:"quote"`
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:              commandSnippetPointKey,
		Decode:           decodeCommandSnippetLibrary,
		Finalize:         finalizeCommandSnippetLibraries,
		DuplicateMessage: func(id string) string { return fmt.Sprintf("duplicate command snippet library id %q", id) },
		IssuePrefix:      "invalid commandSnippets contribution",
		IssueText: func(issue ContributionIssue) string {
			if issue.ID != "" {
				return fmt.Sprintf("skipped command snippet library %q: %s", issue.ID, issue.Message)
			}
			return fmt.Sprintf("skipped contributes.commandSnippets[%d]: %s", issue.Entry, issue.Message)
		},
		EntryNoun:  "command snippet libraries",
		FileLabels: contributionFileLabels{Noun: "command snippet library", Entry: "command snippet library", ShapeHint: "a library object"},
	})
}

func decodeCommandSnippetLibrary(_ *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var reference commandSnippetLibraryReference
	if err := decodeStrictJSON(raw, &reference, true); err != nil {
		return nil, "", fmt.Errorf("parse command snippet library: %w", err)
	}
	id := strings.TrimSpace(reference.ID)
	if !commandSnippetIDPattern.MatchString(id) {
		return nil, id, fmt.Errorf("library id %q must match ^[a-z0-9][a-z0-9-]{0,31}$", reference.ID)
	}
	library := CommandSnippetLibrary{ID: id}
	var err error
	if library.Title, err = validateContributionLine(reference.Title, commandSnippetMaxTitleRunes, true); err != nil {
		return nil, id, fmt.Errorf("title %w", err)
	}
	if library.TitleLocalized, err = validateContributionLocalized(reference.TitleLocalized, commandSnippetMaxTitleRunes, commandSnippetMaxLocales); err != nil {
		return nil, id, fmt.Errorf("titleLocalized %w", err)
	}
	if library.Targets, err = commandSnippetSubset("targets", reference.Targets, commandSnippetTargets); err != nil {
		return nil, id, err
	}
	if library.Platforms, err = commandSnippetSubset("platforms", reference.Platforms, commandSnippetPlatforms); err != nil {
		return nil, id, err
	}
	if len(reference.Commands) == 0 {
		return nil, id, errors.New("commands must list at least one command")
	}
	if len(reference.Commands) > commandSnippetMaxPerLibrary {
		return nil, id, fmt.Errorf("commands must have at most %d entries", commandSnippetMaxPerLibrary)
	}
	seen := map[string]struct{}{}
	library.Commands = make([]CommandSnippet, 0, len(reference.Commands))
	for index, command := range reference.Commands {
		snippet, err := decodeCommandSnippet(command)
		if err != nil {
			label := strings.TrimSpace(command.ID)
			if label == "" {
				label = fmt.Sprintf("#%d", index)
			}
			return nil, id, fmt.Errorf("command %q: %w", label, err)
		}
		if _, duplicate := seen[snippet.ID]; duplicate {
			return nil, id, fmt.Errorf("duplicate command id %q", snippet.ID)
		}
		seen[snippet.ID] = struct{}{}
		library.Commands = append(library.Commands, snippet)
	}
	return library, id, nil
}

func decodeCommandSnippet(reference commandSnippetCommandReference) (CommandSnippet, error) {
	id := strings.TrimSpace(reference.ID)
	if !commandSnippetIDPattern.MatchString(id) {
		return CommandSnippet{}, errors.New("id must match ^[a-z0-9][a-z0-9-]{0,31}$")
	}
	snippet := CommandSnippet{ID: id, Variables: []CommandSnippetVariable{}, Tags: []string{}}
	var err error
	if snippet.Title, err = validateContributionLine(reference.Title, commandSnippetMaxTitleRunes, true); err != nil {
		return CommandSnippet{}, fmt.Errorf("title %w", err)
	}
	if snippet.TitleLocalized, err = validateContributionLocalized(reference.TitleLocalized, commandSnippetMaxTitleRunes, commandSnippetMaxLocales); err != nil {
		return CommandSnippet{}, fmt.Errorf("titleLocalized %w", err)
	}
	if reference.Description != nil {
		if snippet.Description, err = validateContributionLine(*reference.Description, commandSnippetMaxDescription, false); err != nil {
			return CommandSnippet{}, fmt.Errorf("description %w", err)
		}
	}
	if snippet.DescriptionLocalized, err = validateContributionLocalized(reference.DescriptionLocalized, commandSnippetMaxDescription, commandSnippetMaxLocales); err != nil {
		return CommandSnippet{}, fmt.Errorf("descriptionLocalized %w", err)
	}
	if snippet.Command, err = normalizeCommandSnippetText(reference.Command); err != nil {
		return CommandSnippet{}, err
	}
	if len(reference.Variables) > commandSnippetMaxVariables {
		return CommandSnippet{}, fmt.Errorf("variables must have at most %d entries", commandSnippetMaxVariables)
	}
	declared := map[string]struct{}{}
	for _, variable := range reference.Variables {
		decoded, err := decodeCommandSnippetVariable(variable)
		if err != nil {
			return CommandSnippet{}, err
		}
		if _, duplicate := declared[decoded.Name]; duplicate {
			return CommandSnippet{}, fmt.Errorf("duplicate variable %q", decoded.Name)
		}
		declared[decoded.Name] = struct{}{}
		snippet.Variables = append(snippet.Variables, decoded)
	}
	for _, match := range commandSnippetTokenPattern.FindAllStringSubmatch(snippet.Command, -1) {
		name := match[1]
		if !commandSnippetVariablePattern.MatchString(name) {
			continue
		}
		if _, ok := declared[name]; !ok {
			return CommandSnippet{}, fmt.Errorf("command uses undeclared variable {{%s}}", name)
		}
	}
	if len(reference.Tags) > commandSnippetMaxTags {
		return CommandSnippet{}, fmt.Errorf("tags must have at most %d entries", commandSnippetMaxTags)
	}
	for _, tag := range reference.Tags {
		normalized, err := validateContributionLine(tag, commandSnippetMaxTagRunes, true)
		if err != nil {
			return CommandSnippet{}, fmt.Errorf("tag %w", err)
		}
		if !slices.Contains(snippet.Tags, normalized) {
			snippet.Tags = append(snippet.Tags, normalized)
		}
	}
	return snippet, nil
}

func decodeCommandSnippetVariable(reference commandSnippetVariableReference) (CommandSnippetVariable, error) {
	if !commandSnippetVariablePattern.MatchString(reference.Name) {
		return CommandSnippetVariable{}, fmt.Errorf("variable name %q must match ^[A-Za-z][A-Za-z0-9_]{0,31}$", reference.Name)
	}
	variable := CommandSnippetVariable{Name: reference.Name, Quote: "none"}
	var err error
	if reference.Title != nil {
		if variable.Title, err = validateContributionLine(*reference.Title, commandSnippetMaxVariableTitle, false); err != nil {
			return CommandSnippetVariable{}, fmt.Errorf("variable %q title %w", reference.Name, err)
		}
	}
	if reference.Default != nil {
		if variable.Default, err = validateContributionLine(*reference.Default, commandSnippetMaxVariableDefault, false); err != nil {
			return CommandSnippetVariable{}, fmt.Errorf("variable %q default %w", reference.Name, err)
		}
	}
	if reference.Quote != nil {
		if !slices.Contains(commandSnippetQuoteModes, *reference.Quote) {
			return CommandSnippetVariable{}, fmt.Errorf("variable %q quote must be one of %s", reference.Name, strings.Join(commandSnippetQuoteModes, ", "))
		}
		variable.Quote = *reference.Quote
	}
	return variable, nil
}

// normalizeCommandSnippetText accepts a string or a list of lines (joined
// with "\n"), rejects CR and every control character except tab and newline,
// trims trailing whitespace per line and trailing blank lines, and enforces
// the byte and line limits.
func normalizeCommandSnippetText(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", errors.New("command is required")
	}
	var text string
	if trimmed[0] == '[' {
		var lines []string
		if err := json.Unmarshal(trimmed, &lines); err != nil {
			return "", errors.New("command must be a string or a list of strings")
		}
		if len(lines) > commandSnippetMaxCommandLines {
			return "", fmt.Errorf("command must have at most %d lines", commandSnippetMaxCommandLines)
		}
		text = strings.Join(lines, "\n")
	} else if err := json.Unmarshal(trimmed, &text); err != nil {
		return "", errors.New("command must be a string or a list of strings")
	}
	if !utf8.ValidString(text) {
		return "", errors.New("command must be valid UTF-8")
	}
	if strings.ContainsRune(text, '\r') {
		return "", errors.New("command must not contain a carriage return (snippets are never executed)")
	}
	if err := checkContributionRunes(text, true, true); err != nil {
		return "", fmt.Errorf("command %w", err)
	}
	lines := strings.Split(text, "\n")
	for index, line := range lines {
		lines[index] = strings.TrimRight(line, " \t")
	}
	text = strings.TrimRight(strings.Join(lines, "\n"), "\n")
	text = strings.TrimLeft(text, "\n")
	if strings.TrimSpace(text) == "" {
		return "", errors.New("command must not be empty")
	}
	if len(text) > commandSnippetMaxCommandBytes {
		return "", fmt.Errorf("command must be at most %d bytes", commandSnippetMaxCommandBytes)
	}
	if count := strings.Count(text, "\n") + 1; count > commandSnippetMaxCommandLines {
		return "", fmt.Errorf("command must have at most %d lines", commandSnippetMaxCommandLines)
	}
	return text, nil
}

// commandSnippetSubset validates an optional subset of a contract list; nil
// means every allowed value. The result keeps the contract order.
func commandSnippetSubset(field string, values, allowed []string) ([]string, error) {
	if values == nil {
		return append([]string(nil), allowed...), nil
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("%s must not be empty", field)
	}
	set := stringSet(values)
	for _, value := range values {
		if !slices.Contains(allowed, value) {
			return nil, fmt.Errorf("%s value %q must be one of %s", field, value, strings.Join(allowed, ", "))
		}
	}
	result := make([]string, 0, len(set))
	for _, value := range allowed {
		if _, ok := set[value]; ok {
			result = append(result, value)
		}
	}
	return result, nil
}

// finalizeCommandSnippetLibraries enforces the per-plugin command count and
// snapshot size in entry order; a library past either budget is skipped.
func finalizeCommandSnippetLibraries(_ *pointContext, entries []contributionEntry, issues []ContributionIssue) ([]contributionEntry, []ContributionIssue) {
	commands, size := 0, 0
	kept := make([]contributionEntry, 0, len(entries))
	for _, entry := range entries {
		library := entry.Value.(CommandSnippetLibrary)
		encoded, err := json.Marshal(library)
		if err != nil {
			issues = append(issues, ContributionIssue{Point: commandSnippetPointKey, Entry: entry.Index, ID: entry.ID, Message: err.Error()})
			continue
		}
		if commands+len(library.Commands) > commandSnippetMaxPerPlugin {
			issues = append(issues, ContributionIssue{
				Point: commandSnippetPointKey, Entry: entry.Index, ID: entry.ID,
				Message: fmt.Sprintf("plugin command snippets exceed %d commands in total", commandSnippetMaxPerPlugin),
			})
			continue
		}
		if size+len(encoded) > commandSnippetMaxSnapshotBytes {
			issues = append(issues, ContributionIssue{
				Point: commandSnippetPointKey, Entry: entry.Index, ID: entry.ID,
				Message: fmt.Sprintf("plugin command snippets exceed %d bytes in total", commandSnippetMaxSnapshotBytes),
			})
			continue
		}
		commands += len(library.Commands)
		size += len(encoded)
		kept = append(kept, entry)
	}
	return kept, issues
}
