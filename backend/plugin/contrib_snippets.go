package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Editor snippets (contributes.snippets) are data only: each entry names a
// Monaco language and a strict-JSON snippet file in VS Code format
// ({"<name>": {"prefix": string|string[], "body": string|string[],
// "description"?: string|string[]}}) inside the package. The host checks the
// file structure and bounds here; snippet body syntax (tabstops, choices,
// allowlisted variables, no regex transforms, no CLIPBOARD) and whether Monaco
// knows the language are checked by the frontend consumer
// (vue-app/src/domain/editor/snippets.ts). Declarative and process plugins may
// contribute; the point follows the plugin's workspace scope (declarative
// packages are global).

const snippetPointKey = "snippets"

var (
	snippetSpec               = mustContributionSpec(snippetPointKey)
	snippetMaxPerFile         = int(snippetSpec.Limit("snippetsPerFile"))
	snippetMaxPerPlugin       = int(snippetSpec.Limit("snippetsPerPlugin"))
	snippetMaxPrefixLength    = int(snippetSpec.Limit("prefixMaxLength"))
	snippetMaxPrefixes        = int(snippetSpec.Limit("prefixesMax"))
	snippetMaxBodyBytes       = int(snippetSpec.Limit("bodyMaxBytes"))
	snippetMaxDescriptionRune = int(snippetSpec.Limit("descriptionMaxRunes"))
	snippetMaxNameRunes       = int(snippetSpec.Limit("nameMaxRunes"))

	snippetLanguagePattern = regexp.MustCompile(`^[a-z][a-z0-9+#._-]{0,31}$`)
)

// SnippetDefinition is one normalized snippet: prefixes as a list, body and
// description joined with "\n".
type SnippetDefinition struct {
	Name        string   `json:"name"`
	Prefixes    []string `json:"prefixes"`
	Body        string   `json:"body"`
	Description string   `json:"description,omitempty"`
}

// SnippetContribution is one validated contributes.snippets entry as sent to
// the frontend consumer.
type SnippetContribution struct {
	Language string              `json:"language"`
	Path     string              `json:"path"`
	Snippets []SnippetDefinition `json:"snippets"`
}

type snippetEntryReference struct {
	Language string `json:"language"`
	Path     string `json:"path"`
}

// snippetStrings accepts a string or a non-empty list of strings.
type snippetStrings []string

func (values *snippetStrings) UnmarshalJSON(data []byte) error {
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*values = snippetStrings{single}
		return nil
	}
	var list []string
	if err := json.Unmarshal(data, &list); err != nil {
		return errors.New("must be a string or a list of strings")
	}
	*values = snippetStrings(list)
	return nil
}

type snippetFileItem struct {
	Prefix      *snippetStrings `json:"prefix"`
	Body        *snippetStrings `json:"body"`
	Description *snippetStrings `json:"description"`
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:              snippetPointKey,
		Decode:           decodeSnippetEntry,
		Finalize:         finalizeSnippetEntries,
		DuplicateMessage: func(id string) string { return fmt.Sprintf("duplicate snippet file entry %q", id) },
		IssuePrefix:      "invalid snippets contribution",
		IssueText: func(issue ContributionIssue) string {
			if issue.ID != "" {
				return fmt.Sprintf("skipped snippet file %q: %s", issue.ID, issue.Message)
			}
			return fmt.Sprintf("skipped contributes.snippets[%d]: %s", issue.Entry, issue.Message)
		},
		EntryNoun:  "snippet files",
		FileLabels: contributionFileLabels{Noun: "snippet", Entry: "snippet entry"},
	})
}

// decodeSnippetEntry validates {language, path} and the referenced file.
func decodeSnippetEntry(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var reference snippetEntryReference
	if err := decodeStrictJSON(raw, &reference, true); err != nil {
		return nil, "", fmt.Errorf("parse snippet entry: %w", err)
	}
	language := strings.TrimSpace(reference.Language)
	path := strings.TrimSpace(reference.Path)
	id := language + ":" + path
	if !snippetLanguagePattern.MatchString(language) {
		return nil, id, fmt.Errorf("snippet language %q must match ^[a-z][a-z0-9+#._-]{0,31}$", language)
	}
	data, err := ctx.readFile(path, "snippet")
	if err != nil {
		return nil, id, err
	}
	snippets, err := parseSnippetFile(data)
	if err != nil {
		return nil, id, fmt.Errorf("snippet file %q: %w", path, err)
	}
	return SnippetContribution{Language: language, Path: path, Snippets: snippets}, id, nil
}

// finalizeSnippetEntries enforces the per-plugin snippet total in entry order;
// files past the limit are skipped as a whole.
func finalizeSnippetEntries(_ *pointContext, entries []contributionEntry, issues []ContributionIssue) ([]contributionEntry, []ContributionIssue) {
	total := 0
	kept := make([]contributionEntry, 0, len(entries))
	for _, entry := range entries {
		count := len(entry.Value.(SnippetContribution).Snippets)
		if total+count > snippetMaxPerPlugin {
			issues = append(issues, ContributionIssue{
				Point: snippetPointKey, Entry: entry.Index, ID: entry.ID,
				Message: fmt.Sprintf("plugin snippets exceed %d in total", snippetMaxPerPlugin),
			})
			continue
		}
		total += count
		kept = append(kept, entry)
	}
	return kept, issues
}

// parseSnippetFile decodes a strict-JSON VS Code snippet file, keeping the
// file's snippet order and rejecting duplicate names.
func parseSnippetFile(data []byte) ([]SnippetDefinition, error) {
	if !utf8.Valid(data) {
		return nil, errors.New("must be UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("must be strict JSON (comments and trailing commas are not allowed): %w", err)
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return nil, errors.New("must be a JSON object of named snippets")
	}
	snippets := []SnippetDefinition{}
	seen := map[string]struct{}{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("must be strict JSON (comments and trailing commas are not allowed): %w", err)
		}
		name, _ := token.(string)
		if len(snippets) >= snippetMaxPerFile {
			return nil, fmt.Errorf("must contain at most %d snippets", snippetMaxPerFile)
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, fmt.Errorf("must be strict JSON (comments and trailing commas are not allowed): %w", err)
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, fmt.Errorf("duplicate snippet name %q", name)
		}
		seen[name] = struct{}{}
		snippet, err := parseSnippet(name, raw)
		if err != nil {
			return nil, err
		}
		snippets = append(snippets, snippet)
	}
	if _, err := decoder.Token(); err != nil {
		return nil, fmt.Errorf("must be strict JSON (comments and trailing commas are not allowed): %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("must contain exactly one JSON object (comments and trailing data are not allowed)")
	}
	if len(snippets) == 0 {
		return nil, errors.New("contains no snippets")
	}
	return snippets, nil
}

func parseSnippet(name string, raw json.RawMessage) (SnippetDefinition, error) {
	trimmedName := strings.TrimSpace(name)
	if trimmedName == "" || utf8.RuneCountInString(trimmedName) > snippetMaxNameRunes || hasSnippetControl(trimmedName, false) {
		return SnippetDefinition{}, fmt.Errorf("snippet name %q must be 1-%d characters without control characters", name, snippetMaxNameRunes)
	}
	var item snippetFileItem
	if err := decodeStrictJSON(raw, &item, true); err != nil {
		return SnippetDefinition{}, fmt.Errorf("snippet %q must be {prefix, body, description?}: %w", name, err)
	}
	if item.Prefix == nil || len(*item.Prefix) == 0 || len(*item.Prefix) > snippetMaxPrefixes {
		return SnippetDefinition{}, fmt.Errorf("snippet %q needs 1-%d prefixes", name, snippetMaxPrefixes)
	}
	prefixes := make([]string, 0, len(*item.Prefix))
	for _, prefix := range *item.Prefix {
		if prefix == "" || utf8.RuneCountInString(prefix) > snippetMaxPrefixLength || strings.IndexFunc(prefix, func(r rune) bool {
			return unicode.IsSpace(r) || unicode.IsControl(r)
		}) >= 0 {
			return SnippetDefinition{}, fmt.Errorf("snippet %q prefix %q must be 1-%d characters without whitespace", name, prefix, snippetMaxPrefixLength)
		}
		prefixes = append(prefixes, prefix)
	}
	if item.Body == nil || len(*item.Body) == 0 {
		return SnippetDefinition{}, fmt.Errorf("snippet %q needs a body", name)
	}
	body := strings.Join(*item.Body, "\n")
	if strings.TrimSpace(body) == "" {
		return SnippetDefinition{}, fmt.Errorf("snippet %q body must not be empty", name)
	}
	if len(body) > snippetMaxBodyBytes {
		return SnippetDefinition{}, fmt.Errorf("snippet %q body exceeds %d bytes", name, snippetMaxBodyBytes)
	}
	if hasSnippetControl(body, true) {
		return SnippetDefinition{}, fmt.Errorf("snippet %q body contains control characters", name)
	}
	description := ""
	if item.Description != nil {
		description = strings.TrimSpace(strings.Join(*item.Description, "\n"))
		if utf8.RuneCountInString(description) > snippetMaxDescriptionRune || hasSnippetControl(description, true) {
			return SnippetDefinition{}, fmt.Errorf("snippet %q description must be at most %d characters without control characters", name, snippetMaxDescriptionRune)
		}
	}
	return SnippetDefinition{Name: trimmedName, Prefixes: prefixes, Body: body, Description: description}, nil
}

// hasSnippetControl reports control characters; multiline text may contain
// tab, newline and carriage return.
func hasSnippetControl(value string, multiline bool) bool {
	return strings.IndexFunc(value, func(r rune) bool {
		if multiline && (r == '\t' || r == '\n' || r == '\r') {
			return false
		}
		return unicode.IsControl(r) || r == '\u2028' || r == '\u2029'
	}) >= 0
}
