package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// AI prompts and skills (contributes.aiPrompts) are data only. A "prompt"
// entry is a template the user picks in the AI composer; the host frontend
// expands its allowlisted variables ({{selection}}, {{file}}, {{language}})
// into the input draft and never sends it automatically. A "skill" entry is
// literal Markdown guidance that is added to the AI context only when the user
// explicitly names it with /skill, wrapped with a visible third-party label.
// Plugin data can never change the approval mode, tool grants, model, system
// prompt or MCP servers; a leading frontmatter block (including any
// allowed-tools key) is stripped and ignored. Text must be UTF-8 without
// control, bidirectional-override or zero-width characters so a plugin cannot
// hide instructions from the user reviewing it.

const aiPromptPointKey = "aiPrompts"

var (
	aiPromptSpec             = mustContributionSpec(aiPromptPointKey)
	aiPromptMaxTitleRunes    = int(aiPromptSpec.Limit("titleMaxRunes"))
	aiPromptMaxDescription   = int(aiPromptSpec.Limit("descriptionMaxRunes"))
	aiPromptMaxTemplateBytes = int(aiPromptSpec.Limit("templateMaxBytes"))
	aiPromptMaxSkillBytes    = int(aiPromptSpec.Limit("skillMaxBytes"))
	aiPromptMaxPluginBytes   = int(aiPromptSpec.Limit("perPluginBytes"))
	aiPromptMaxLocales       = int(aiPromptSpec.Limit("localesMax"))

	aiPromptIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)
	// aiPromptVariablePattern matches every {{name}} placeholder; each name
	// must be one of aiPromptVariables. Other braces are literal text.
	aiPromptVariablePattern = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_.$-]+)\s*\}\}`)
	aiPromptVariables       = []string{"selection", "file", "language"}
)

// AiPromptContribution is one validated contributes.aiPrompts entry as sent to
// the frontend consumer. Body is the normalized template or skill text
// (BOM and leading frontmatter removed); Variables lists the distinct template
// variables a prompt uses, in first-use order.
type AiPromptContribution struct {
	ID                   string            `json:"id"`
	Kind                 string            `json:"kind"`
	Title                string            `json:"title"`
	TitleLocalized       map[string]string `json:"titleLocalized,omitempty"`
	Description          string            `json:"description,omitempty"`
	DescriptionLocalized map[string]string `json:"descriptionLocalized,omitempty"`
	Body                 string            `json:"body"`
	Variables            []string          `json:"variables"`
}

type aiPromptEntryReference struct {
	ID                   string            `json:"id"`
	Kind                 string            `json:"kind"`
	Title                string            `json:"title"`
	TitleLocalized       map[string]string `json:"titleLocalized"`
	Description          *string           `json:"description"`
	DescriptionLocalized map[string]string `json:"descriptionLocalized"`
	Template             *string           `json:"template"`
	Path                 *string           `json:"path"`
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:              aiPromptPointKey,
		RuntimeOverlay:   customAiPromptsOverlay,
		Decode:           decodeAiPromptEntry,
		Finalize:         finalizeAiPromptEntries,
		DuplicateMessage: func(id string) string { return fmt.Sprintf("duplicate AI prompt id %q", id) },
		IssuePrefix:      "invalid aiPrompts contribution",
		IssueText: func(issue ContributionIssue) string {
			if issue.ID != "" {
				return fmt.Sprintf("skipped AI prompt %q: %s", issue.ID, issue.Message)
			}
			return fmt.Sprintf("skipped contributes.aiPrompts[%d]: %s", issue.Entry, issue.Message)
		},
		EntryNoun:  "AI prompts",
		FileLabels: contributionFileLabels{Noun: "AI prompt", Entry: "AI prompt entry"},
	})
}

func decodeAiPromptEntry(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var reference aiPromptEntryReference
	if err := decodeStrictJSON(raw, &reference, true); err != nil {
		return nil, "", fmt.Errorf("parse AI prompt entry: %w", err)
	}
	id := strings.TrimSpace(reference.ID)
	if !aiPromptIDPattern.MatchString(id) {
		return nil, id, fmt.Errorf("AI prompt id %q must match ^[a-z0-9][a-z0-9-]{0,47}$", reference.ID)
	}
	kind := strings.TrimSpace(reference.Kind)
	if kind != "prompt" && kind != "skill" {
		return nil, id, errors.New(`kind must be "prompt" or "skill"`)
	}
	item := AiPromptContribution{ID: id, Kind: kind, Variables: []string{}}

	title, err := validateAiPromptLine(reference.Title, aiPromptMaxTitleRunes, true)
	if err != nil {
		return nil, id, fmt.Errorf("title: %w", err)
	}
	item.Title = title
	if item.TitleLocalized, err = validateAiPromptLocalized(reference.TitleLocalized, aiPromptMaxTitleRunes); err != nil {
		return nil, id, fmt.Errorf("titleLocalized: %w", err)
	}
	if reference.Description != nil {
		if item.Description, err = validateAiPromptLine(*reference.Description, aiPromptMaxDescription, false); err != nil {
			return nil, id, fmt.Errorf("description: %w", err)
		}
	}
	if kind == "skill" && item.Description == "" {
		return nil, id, errors.New("skill entries need a description")
	}
	if item.DescriptionLocalized, err = validateAiPromptLocalized(reference.DescriptionLocalized, aiPromptMaxDescription); err != nil {
		return nil, id, fmt.Errorf("descriptionLocalized: %w", err)
	}

	if (reference.Template == nil) == (reference.Path == nil) {
		return nil, id, errors.New(`exactly one of "template" or "path" is required`)
	}
	var body string
	if reference.Template != nil {
		if kind == "skill" {
			return nil, id, errors.New(`skill entries must reference a Markdown file with "path"`)
		}
		body = *reference.Template
	} else {
		data, err := ctx.readFile(*reference.Path, "AI prompt")
		if err != nil {
			return nil, id, err
		}
		if !utf8.Valid(data) {
			return nil, id, fmt.Errorf("AI prompt file %q must be UTF-8", strings.TrimSpace(*reference.Path))
		}
		data = bytes.TrimPrefix(data, []byte("\ufeff"))
		body, err = stripAiPromptFrontmatter(string(data))
		if err != nil {
			return nil, id, fmt.Errorf("AI prompt file %q: %w", strings.TrimSpace(*reference.Path), err)
		}
	}
	if item.Body, item.Variables, err = validateAiPromptBody(kind, body); err != nil {
		return nil, id, err
	}
	return item, id, nil
}

// finalizeAiPromptEntries enforces the per-plugin body total in entry order.
func finalizeAiPromptEntries(_ *pointContext, entries []contributionEntry, issues []ContributionIssue) ([]contributionEntry, []ContributionIssue) {
	total := 0
	kept := make([]contributionEntry, 0, len(entries))
	for _, entry := range entries {
		size := len(entry.Value.(AiPromptContribution).Body)
		if total+size > aiPromptMaxPluginBytes {
			issues = append(issues, ContributionIssue{
				Point: aiPromptPointKey, Entry: entry.Index, ID: entry.ID,
				Message: fmt.Sprintf("plugin AI prompts exceed %d bytes in total", aiPromptMaxPluginBytes),
			})
			continue
		}
		total += size
		kept = append(kept, entry)
	}
	return kept, issues
}

// validateAiPromptBody checks the size, hidden characters and, for prompts,
// the variable allowlist. Line endings are normalized to "\n".
func validateAiPromptBody(kind, body string) (string, []string, error) {
	if !utf8.ValidString(body) {
		return "", nil, errors.New("text must be valid UTF-8")
	}
	body = strings.TrimPrefix(body, "\ufeff")
	body = strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\r", "\n")
	if strings.TrimSpace(body) == "" {
		return "", nil, errors.New("text must not be empty")
	}
	limit := aiPromptMaxTemplateBytes
	if kind == "skill" {
		limit = aiPromptMaxSkillBytes
	}
	if len(body) > limit {
		return "", nil, fmt.Errorf("%s text exceeds %d bytes", kind, limit)
	}
	if index := strings.IndexFunc(body, func(r rune) bool { return isAiPromptHiddenRune(r, true) }); index >= 0 {
		r, _ := utf8.DecodeRuneInString(body[index:])
		return "", nil, fmt.Errorf("text contains a hidden, control, bidirectional or zero-width character (U+%04X)", r)
	}
	variables := []string{}
	if kind == "prompt" {
		seen := map[string]struct{}{}
		for _, match := range aiPromptVariablePattern.FindAllStringSubmatch(body, -1) {
			name := match[1]
			if !isAiPromptVariable(name) {
				return "", nil, fmt.Errorf("template variable {{%s}} is not allowed; use {{selection}}, {{file}} or {{language}}", name)
			}
			if _, ok := seen[name]; !ok {
				seen[name] = struct{}{}
				variables = append(variables, name)
			}
		}
	}
	return body, variables, nil
}

func isAiPromptVariable(name string) bool {
	for _, allowed := range aiPromptVariables {
		if name == allowed {
			return true
		}
	}
	return false
}

// stripAiPromptFrontmatter removes a leading "---" YAML frontmatter block.
// Its keys (name, description, allowed-tools, ...) are ignored: manifest
// fields are the only metadata and plugins cannot grant tools.
func stripAiPromptFrontmatter(text string) (string, error) {
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	if !strings.HasPrefix(normalized, "---\n") {
		return normalized, nil
	}
	rest := normalized[len("---\n"):]
	offset := 0
	for offset <= len(rest) {
		end := strings.IndexByte(rest[offset:], '\n')
		line := rest[offset:]
		if end >= 0 {
			line = rest[offset : offset+end]
		}
		if trimmed := strings.TrimRight(line, " \t"); trimmed == "---" || trimmed == "..." {
			if end < 0 {
				return "", nil
			}
			return strings.TrimLeft(rest[offset+end+1:], "\n"), nil
		}
		if end < 0 {
			break
		}
		offset += end + 1
	}
	return "", errors.New("frontmatter is not closed with ---")
}

// isAiPromptHiddenRune reports characters that could hide or reorder text:
// C0/C1 controls (tab and newlines allowed in multiline text), line/paragraph
// separators, bidi embeddings/overrides/isolates, zero-width characters and
// the byte-order mark.
func isAiPromptHiddenRune(r rune, multiline bool) bool {
	if multiline && (r == '\t' || r == '\n' || r == '\r') {
		return false
	}
	switch {
	case unicode.IsControl(r), r == '\u2028', r == '\u2029':
		return true
	case r >= '\u202a' && r <= '\u202e', r >= '\u2066' && r <= '\u2069':
		return true
	case r >= '\u200b' && r <= '\u200f', r == '\ufeff':
		return true
	}
	return false
}

// validateAiPromptLine checks a single-line label (title or description).
func validateAiPromptLine(value string, maxRunes int, required bool) (string, error) {
	if !utf8.ValidString(value) {
		return "", errors.New("must be valid UTF-8")
	}
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		if required {
			return "", errors.New("must not be blank")
		}
		return "", nil
	}
	if utf8.RuneCountInString(trimmed) > maxRunes {
		return "", fmt.Errorf("exceeds %d characters", maxRunes)
	}
	if strings.IndexFunc(trimmed, func(r rune) bool { return isAiPromptHiddenRune(r, false) }) >= 0 {
		return "", errors.New("must be a single line without control, bidirectional or zero-width characters")
	}
	return trimmed, nil
}

func validateAiPromptLocalized(values map[string]string, maxRunes int) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	if len(values) > aiPromptMaxLocales {
		return nil, fmt.Errorf("exceeds %d locales", aiPromptMaxLocales)
	}
	result := make(map[string]string, len(values))
	for locale, value := range values {
		key := strings.TrimSpace(locale)
		if key == "" || len(key) > 32 {
			return nil, fmt.Errorf("locale %q is invalid", locale)
		}
		normalized, err := validateAiPromptLine(value, maxRunes, true)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		result[key] = normalized
	}
	return result, nil
}

// aiPrompts returns the validated entries of a manifest.
func (manifest Manifest) aiPrompts() []AiPromptContribution {
	resolved := manifest.contribution(aiPromptPointKey)
	if resolved == nil {
		return nil
	}
	items := make([]AiPromptContribution, 0, len(resolved.Entries))
	for _, entry := range resolved.Entries {
		if item, ok := entry.(AiPromptContribution); ok {
			items = append(items, item)
		}
	}
	return items
}
