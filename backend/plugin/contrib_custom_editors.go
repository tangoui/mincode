package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"mincode/app/plugins/api"
)

// Custom editors (contributes.customEditors) let a process plugin offer one of
// its own views as an editor for project files that match filename patterns.
// Opening a file with a custom editor is a user grant for that one file: the
// host resolves the path (custom_editor_documents.go), binds it to a fresh UI
// session, and streams bounded chunks to the iframe. Writes are available only
// to access "readWrite" entries of plugins that declare workspace.write, go
// through an atomic, sha256-conflict-checked host save, and never create,
// rename or delete other files.
//
// Patterns are intentionally narrow: an exact filename (or project-relative
// path) or a glob whose literal suffix after the last wildcard contains a dot
// ("*.drawio", "docs/**/*.bpmn"). Catch-alls ("*", "**/*", "*.*", ".*") are
// rejected, and a "default" editor may never claim a sensitive filename
// (contract sensitiveFilenames: .env, id_rsa, server.key, ...).

const customEditorPointKey = "customEditors"

var (
	customEditorSpec             = mustContributionSpec(customEditorPointKey)
	customEditorPatternsMax      = int(customEditorSpec.Limit("patternsMax"))
	customEditorPatternMaxLength = int(customEditorSpec.Limit("patternMaxLength"))
	customEditorTitleMaxRunes    = int(customEditorSpec.Limit("titleMaxRunes"))
	customEditorLocalesMax       = int(customEditorSpec.Limit("localesMax"))

	customEditorIDPattern       = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	customEditorPatternAlphabet = regexp.MustCompile(`^[A-Za-z0-9._\-/*?+@]+$`)
)

const (
	customEditorPriorityDefault = "default"
	customEditorPriorityOption  = "option"
	customEditorAccessRead      = "read"
	customEditorAccessReadWrite = "readWrite"
)

// CustomEditorContribution is one validated contributes.customEditors entry
// as sent to the frontend consumer. Title falls back to the view title.
type CustomEditorContribution struct {
	ID               string            `json:"id"`
	ViewID           string            `json:"viewId"`
	Title            string            `json:"title"`
	TitleLocalized   map[string]string `json:"titleLocalized,omitempty"`
	FilenamePatterns []string          `json:"filenamePatterns"`
	Priority         string            `json:"priority"`
	Access           string            `json:"access"`
	Icon             string            `json:"icon,omitempty"`
}

type customEditorManifestEntry struct {
	ID               string            `json:"id"`
	ViewID           string            `json:"viewId"`
	Title            string            `json:"title"`
	TitleLocalized   map[string]string `json:"titleLocalized"`
	FilenamePatterns []string          `json:"filenamePatterns"`
	Priority         string            `json:"priority"`
	Access           string            `json:"access"`
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:               customEditorPointKey,
		Decode:            decodeCustomEditorEntry,
		DuplicateMessage:  func(id string) string { return fmt.Sprintf("custom editor id %q is duplicated", id) },
		IssuePrefix:       "invalid customEditors contribution",
		IssueText:         customEditorIssueText,
		EntryNoun:         "custom editors",
		ExpandsPermission: customEditorsExpandPermission,
	})
}

func customEditorIssueText(issue ContributionIssue) string {
	if issue.ID != "" {
		return fmt.Sprintf("custom editor %q skipped: %s", issue.ID, issue.Message)
	}
	return fmt.Sprintf("custom editor entry %d skipped: %s", issue.Entry, issue.Message)
}

func decodeCustomEditorEntry(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var entry customEditorManifestEntry
	if err := decodeStrictJSON(raw, &entry, true); err != nil {
		return nil, "", fmt.Errorf("parse custom editor: %w", err)
	}
	id := strings.TrimSpace(entry.ID)
	if !customEditorIDPattern.MatchString(id) {
		return nil, id, fmt.Errorf("custom editor id %q must match ^[a-z][a-z0-9-]{0,31}$", id)
	}
	viewID := strings.TrimSpace(entry.ViewID)
	view, ok := ctx.views[viewID]
	if viewID == "" || !ok {
		return nil, id, fmt.Errorf("custom editor %q viewId %q is not one of this plugin's contributes.views", id, viewID)
	}
	editor := CustomEditorContribution{
		ID:     id,
		ViewID: view.ID,
		Title:  strings.TrimSpace(entry.Title),
		Icon:   resolvePluginViewIcon(view.Icon, ctx.manifest.Icon),
	}
	if editor.Title == "" {
		editor.Title = view.Title
		editor.TitleLocalized = cloneStringMap(view.TitleLocalized)
	} else if err := validateCustomEditorTitle(editor.Title); err != nil {
		return nil, id, err
	}
	if entry.TitleLocalized != nil {
		if len(entry.TitleLocalized) > customEditorLocalesMax {
			return nil, id, fmt.Errorf("custom editor %q titleLocalized exceeds %d locales", id, customEditorLocalesMax)
		}
		localized := make(map[string]string, len(entry.TitleLocalized))
		for locale, title := range entry.TitleLocalized {
			locale = strings.TrimSpace(locale)
			if locale == "" || len(locale) > 32 {
				return nil, id, fmt.Errorf("custom editor %q titleLocalized locale %q is invalid", id, locale)
			}
			title = strings.TrimSpace(title)
			if err := validateCustomEditorTitle(title); err != nil {
				return nil, id, fmt.Errorf("custom editor %q titleLocalized[%s]: %w", id, locale, err)
			}
			localized[locale] = title
		}
		if len(localized) > 0 {
			editor.TitleLocalized = localized
		}
	}

	switch strings.TrimSpace(entry.Priority) {
	case "", customEditorPriorityOption:
		editor.Priority = customEditorPriorityOption
	case customEditorPriorityDefault:
		editor.Priority = customEditorPriorityDefault
	default:
		return nil, id, fmt.Errorf("custom editor %q priority must be default or option", id)
	}
	switch strings.TrimSpace(entry.Access) {
	case "", customEditorAccessRead:
		editor.Access = customEditorAccessRead
	case customEditorAccessReadWrite:
		if !manifestHasCapability(ctx.manifest, api.CapabilityWorkspaceWrite) {
			return nil, id, fmt.Errorf("custom editor %q access readWrite requires the workspace.write capability", id)
		}
		editor.Access = customEditorAccessReadWrite
	default:
		return nil, id, fmt.Errorf("custom editor %q access must be read or readWrite", id)
	}

	if len(entry.FilenamePatterns) == 0 || len(entry.FilenamePatterns) > customEditorPatternsMax {
		return nil, id, fmt.Errorf("custom editor %q needs 1-%d filenamePatterns", id, customEditorPatternsMax)
	}
	seen := map[string]struct{}{}
	for _, rawPattern := range entry.FilenamePatterns {
		pattern := strings.TrimSpace(rawPattern)
		if err := validateCustomEditorPattern(pattern); err != nil {
			return nil, id, fmt.Errorf("custom editor %q filename pattern %q %w", id, pattern, err)
		}
		folded := strings.ToLower(pattern)
		if _, duplicate := seen[folded]; duplicate {
			return nil, id, fmt.Errorf("custom editor %q lists filename pattern %q more than once", id, pattern)
		}
		seen[folded] = struct{}{}
		if editor.Priority == customEditorPriorityDefault {
			if sample, matched := customEditorPatternMatchesSensitive(pattern, ctx.spec.SensitiveFilenames); matched {
				return nil, id, fmt.Errorf("custom editor %q default filename pattern %q matches the sensitive filename %q; use priority option", id, pattern, sample)
			}
		}
		editor.FilenamePatterns = append(editor.FilenamePatterns, pattern)
	}
	return editor, id, nil
}

func validateCustomEditorTitle(title string) error {
	if !utf8.ValidString(title) || strings.TrimSpace(title) == "" {
		return errors.New("custom editor title must be non-empty UTF-8")
	}
	if utf8.RuneCountInString(title) > customEditorTitleMaxRunes {
		return fmt.Errorf("custom editor title exceeds %d characters", customEditorTitleMaxRunes)
	}
	for _, r := range title {
		if isStatusBarUnsafeRune(r, false) {
			return errors.New("custom editor title must be a single line without control or bidirectional override characters")
		}
	}
	return nil
}

// validateCustomEditorPattern enforces the pattern breadth rules. The error
// text continues "filename pattern %q ...".
func validateCustomEditorPattern(pattern string) error {
	if pattern == "" {
		return errors.New("is empty")
	}
	if len(pattern) > customEditorPatternMaxLength {
		return fmt.Errorf("exceeds %d characters", customEditorPatternMaxLength)
	}
	if !customEditorPatternAlphabet.MatchString(pattern) {
		return errors.New("may only use letters, digits and . _ - / * ? + @ (no braces, brackets, !, \\ or spaces)")
	}
	if strings.HasPrefix(pattern, "/") || strings.HasSuffix(pattern, "/") || strings.Contains(pattern, "//") {
		return errors.New("must be a relative path without empty segments")
	}
	if strings.Contains(pattern, "..") {
		return errors.New("must not contain ..")
	}
	for _, segment := range strings.Split(pattern, "/") {
		if segment == "." {
			return errors.New("must not contain . segments")
		}
	}
	last := strings.LastIndexAny(pattern, "*?")
	if last < 0 {
		return nil // exact filename or relative path
	}
	suffix := pattern[last+1:]
	if len(suffix) < 2 || !strings.Contains(suffix, ".") || strings.Contains(suffix, "/") {
		return errors.New("is too broad: text after the last wildcard must be a literal extension such as .drawio")
	}
	return nil
}

// customEditorGlobRegexp mirrors vue-app/src/domain/files/filename-glob.ts:
// "**" matches anything including "/", "*" anything except "/", "?" one
// character except "/"; case-insensitive; everything else literal.
func customEditorGlobRegexp(pattern string) *regexp.Regexp {
	var builder strings.Builder
	builder.WriteString("(?i)^")
	for index := 0; index < len(pattern); index++ {
		switch character := pattern[index]; character {
		case '*':
			if index+1 < len(pattern) && pattern[index+1] == '*' {
				builder.WriteString(".*")
				index++
			} else {
				builder.WriteString("[^/]*")
			}
		case '?':
			builder.WriteString("[^/]")
		default:
			builder.WriteString(regexp.QuoteMeta(string(character)))
		}
	}
	builder.WriteString("$")
	return regexp.MustCompile(builder.String())
}

// customEditorPatternMatches applies the frontend rule: a pattern containing
// "/" is matched against the slash-normalized project-relative path,
// otherwise against the basename.
func customEditorPatternMatches(pattern, relativePath string) bool {
	relativePath = strings.TrimPrefix(strings.ReplaceAll(relativePath, `\`, "/"), "/")
	target := relativePath
	if !strings.Contains(pattern, "/") {
		if index := strings.LastIndex(relativePath, "/"); index >= 0 {
			target = relativePath[index+1:]
		}
	}
	return customEditorGlobRegexp(pattern).MatchString(target)
}

// customEditorPatternMatchesSensitive reports whether a pattern could claim a
// sensitive filename. Path patterns are checked through their final segment,
// so "config/**/.env" and "**/*.pem" are caught as well.
func customEditorPatternMatchesSensitive(pattern string, samples []string) (string, bool) {
	final := pattern
	if index := strings.LastIndex(pattern, "/"); index >= 0 {
		final = pattern[index+1:]
	}
	expression := customEditorGlobRegexp(final)
	for _, sample := range samples {
		if expression.MatchString(sample) {
			return sample, true
		}
	}
	return "", false
}

// customEditorEntries returns the validated editors of a manifest.
func (manifest Manifest) customEditorEntries() []CustomEditorContribution {
	resolved := manifest.contribution(customEditorPointKey)
	if resolved == nil {
		return nil
	}
	editors := make([]CustomEditorContribution, 0, len(resolved.Entries))
	for _, entry := range resolved.Entries {
		if editor, ok := entry.(CustomEditorContribution); ok {
			editors = append(editors, editor)
		}
	}
	return editors
}

// customEditor finds one validated editor by local id.
func (manifest Manifest) customEditor(id string) (CustomEditorContribution, bool) {
	for _, editor := range manifest.customEditorEntries() {
		if editor.ID == id {
			return editor, true
		}
	}
	return CustomEditorContribution{}, false
}

// customEditorsExpandPermission: a development generation needs a newly
// approved session when an editor gains write access or a default pattern
// appears that no current editor claimed by default.
func customEditorsExpandPermission(current, candidate *resolvedContribution) bool {
	if candidate == nil {
		return false
	}
	writable := map[string]bool{}
	defaults := map[string]bool{}
	if current != nil {
		for _, entry := range current.Entries {
			editor, ok := entry.(CustomEditorContribution)
			if !ok {
				continue
			}
			if editor.Access == customEditorAccessReadWrite {
				writable[editor.ID] = true
			}
			if editor.Priority == customEditorPriorityDefault {
				for _, pattern := range editor.FilenamePatterns {
					defaults[strings.ToLower(pattern)] = true
				}
			}
		}
	}
	for _, entry := range candidate.Entries {
		editor, ok := entry.(CustomEditorContribution)
		if !ok {
			continue
		}
		if editor.Access == customEditorAccessReadWrite && !writable[editor.ID] {
			return true
		}
		if editor.Priority == customEditorPriorityDefault {
			for _, pattern := range editor.FilenamePatterns {
				if !defaults[strings.ToLower(pattern)] {
					return true
				}
			}
		}
	}
	return false
}
