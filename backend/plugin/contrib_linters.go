package plugin

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Linters (contributes.linters) are a process-only contribution point. An
// entry names 1-16 Monaco language ids, one of the plugin's own read-risk
// actions and the triggers (open, save, type) that may run it. The host
// invokes the action through plugin_host_lint (linter_invoke.go) only for
// languages whose machine-local preference selects the linter; results are
// sanitised into Monaco markers and never edit the document.
//
// Optional detect.files lets the host notice that a project uses the tool and
// ask the user once (linter_detect.go); detection only decides when to ask.
// Optional enableSetting names one of the plugin's own workspace-scoped
// boolean settings that the tool needs because it runs project code (for
// example ESLint loading a JavaScript config); accepting the prompt also turns
// it on for the current workspace. Mirrors:
// tools/mincode-cli/linters.go (CLI), assertLintersEntry (cloud) and
// vue-app/src/domain/plugins/linters.ts (frontend revalidation).
const linterPointKey = "linters"

var (
	linterSpec            = mustContributionSpec(linterPointKey)
	linterTitleMaxRunes   = int(linterSpec.Limit("titleMaxRunes"))
	linterLocalesMax      = int(linterSpec.Limit("localesMax"))
	linterLanguagesMax    = int(linterSpec.Limit("languagesMax"))
	linterDebounceMinMs   = int(linterSpec.Limit("debounceMinMs"))
	linterDebounceMaxMs   = int(linterSpec.Limit("debounceMaxMs"))
	linterDebounceDefault = int(linterSpec.Limit("debounceDefaultMs"))
	linterTriggers        = linterSpec.List("triggers")
	linterDefaultTriggers = []string{"open", "save"}
	linterDetectFilesMax  = int(linterSpec.Limit("detectFilesMax"))
	linterDetectPathMax   = int(linterSpec.Limit("detectPathMaxRunes"))
	linterIDPattern       = formatterIDPattern
	linterLanguagePattern = formatterLanguagePattern
)

type linterManifestEntry struct {
	ID             string            `json:"id"`
	Title          string            `json:"title"`
	TitleLocalized map[string]string `json:"titleLocalized,omitempty"`
	Languages      []string          `json:"languages"`
	Action         string            `json:"action"`
	Triggers       []string          `json:"triggers,omitempty"`
	DebounceMs     *int              `json:"debounceMs,omitempty"`
	Detect         *struct {
		Files []string `json:"files"`
	} `json:"detect,omitempty"`
	EnableSetting *string `json:"enableSetting,omitempty"`
}

// LinterDetect lists project-relative paths whose presence means a project
// uses the linter's tool.
type LinterDetect struct {
	Files []string `json:"files"`
}

// LinterContribution is the snapshot entry.
type LinterContribution struct {
	ID             string            `json:"id"`
	Title          string            `json:"title"`
	TitleLocalized map[string]string `json:"titleLocalized,omitempty"`
	Languages      []string          `json:"languages"`
	Action         string            `json:"action"`
	Triggers       []string          `json:"triggers"`
	DebounceMs     int               `json:"debounceMs"`
	Detect         *LinterDetect     `json:"detect,omitempty"`
	EnableSetting  string            `json:"enableSetting,omitempty"`
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:              linterPointKey,
		Decode:           decodeLinterEntry,
		DuplicateMessage: func(id string) string { return fmt.Sprintf("duplicate linter id %q", id) },
		IssuePrefix:      "invalid linters contribution",
		IssueText: func(issue ContributionIssue) string {
			if issue.ID != "" {
				return fmt.Sprintf("skipped linter %q: %s", issue.ID, issue.Message)
			}
			return fmt.Sprintf("skipped contributes.linters[%d]: %s", issue.Entry, issue.Message)
		},
		EntryNoun:         "linters",
		ExpandsPermission: lintersExpandPermission,
	})
}

func decodeLinterEntry(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var entry linterManifestEntry
	if err := decodeStrictJSON(raw, &entry, true); err != nil {
		return nil, "", fmt.Errorf("parse linter: %w", err)
	}
	id := strings.TrimSpace(entry.ID)
	if !linterIDPattern.MatchString(id) {
		return nil, id, fmt.Errorf("linter id %q must match ^[a-z][a-z0-9-]{0,31}$", id)
	}
	title, err := validateContributionLine(entry.Title, linterTitleMaxRunes, true)
	if err != nil {
		return nil, id, fmt.Errorf("linter %q title %w", id, err)
	}
	localized, err := validateContributionLocalized(entry.TitleLocalized, linterTitleMaxRunes, linterLocalesMax)
	if err != nil {
		return nil, id, fmt.Errorf("linter %q titleLocalized %w", id, err)
	}
	if len(entry.Languages) == 0 || len(entry.Languages) > linterLanguagesMax {
		return nil, id, fmt.Errorf("linter %q needs 1-%d languages", id, linterLanguagesMax)
	}
	languages := make([]string, 0, len(entry.Languages))
	seenLanguages := map[string]bool{}
	for _, language := range entry.Languages {
		if !linterLanguagePattern.MatchString(language) {
			return nil, id, fmt.Errorf("linter %q language %q must match ^[a-z][a-z0-9+#._-]{0,31}$", id, language)
		}
		if seenLanguages[language] {
			return nil, id, fmt.Errorf("linter %q repeats language %q", id, language)
		}
		seenLanguages[language] = true
		languages = append(languages, language)
	}
	actionID := strings.TrimSpace(entry.Action)
	action, ok := ctx.actions[actionID]
	if actionID == "" || !ok {
		return nil, id, fmt.Errorf("linter %q action %q is not one of this plugin's contributes.actions", id, actionID)
	}
	if action.Risk != "read" {
		return nil, id, fmt.Errorf("linter %q action %q must have risk \"read\", not %q", id, actionID, action.Risk)
	}
	triggers := linterDefaultTriggers
	if entry.Triggers != nil {
		if len(entry.Triggers) == 0 {
			return nil, id, fmt.Errorf("linter %q triggers must not be empty", id)
		}
		allowed := map[string]bool{}
		for _, trigger := range linterTriggers {
			allowed[trigger] = true
		}
		seen := map[string]bool{}
		triggers = make([]string, 0, len(entry.Triggers))
		for _, trigger := range entry.Triggers {
			if !allowed[trigger] {
				return nil, id, fmt.Errorf("linter %q trigger %q must be one of %s", id, trigger, strings.Join(linterTriggers, ", "))
			}
			if seen[trigger] {
				return nil, id, fmt.Errorf("linter %q repeats trigger %q", id, trigger)
			}
			seen[trigger] = true
			triggers = append(triggers, trigger)
		}
	}
	debounce := linterDebounceDefault
	if entry.DebounceMs != nil {
		hasType := false
		for _, trigger := range triggers {
			hasType = hasType || trigger == "type"
		}
		if !hasType {
			return nil, id, fmt.Errorf("linter %q debounceMs is allowed only with the \"type\" trigger", id)
		}
		debounce = *entry.DebounceMs
		if debounce < linterDebounceMinMs || debounce > linterDebounceMaxMs {
			return nil, id, fmt.Errorf("linter %q debounceMs must be %d-%d", id, linterDebounceMinMs, linterDebounceMaxMs)
		}
	}
	var detect *LinterDetect
	if entry.Detect != nil {
		files, err := validateLinterDetectFiles(id, entry.Detect.Files)
		if err != nil {
			return nil, id, err
		}
		detect = &LinterDetect{Files: files}
	}
	enableSetting := ""
	if entry.EnableSetting != nil {
		enableSetting = strings.TrimSpace(*entry.EnableSetting)
		if !linterWorkspaceToggle(ctx.manifest, enableSetting) {
			return nil, id, fmt.Errorf("linter %q enableSetting %q must name a boolean contributes.configuration property with scope \"workspace\"", id, enableSetting)
		}
	}
	return LinterContribution{
		ID: id, Title: title, TitleLocalized: localized, Languages: languages,
		Action: actionID, Triggers: append([]string(nil), triggers...), DebounceMs: debounce,
		Detect: detect, EnableSetting: enableSetting,
	}, id, nil
}

// validateLinterDetectFiles accepts 1..detectFilesMax clean project-relative
// paths: forward slashes, no "." or ".." segments, no glob characters.
func validateLinterDetectFiles(id string, files []string) ([]string, error) {
	if len(files) == 0 || len(files) > linterDetectFilesMax {
		return nil, fmt.Errorf("linter %q detect.files needs 1-%d paths", id, linterDetectFilesMax)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(files))
	for _, raw := range files {
		file := strings.TrimSpace(raw)
		if !validLinterDetectPath(file) {
			return nil, fmt.Errorf("linter %q detect path %q must be a project-relative path of at most %d characters without \"..\", backslashes or wildcards", id, raw, linterDetectPathMax)
		}
		if seen[file] {
			return nil, fmt.Errorf("linter %q repeats detect path %q", id, file)
		}
		seen[file] = true
		out = append(out, file)
	}
	return out, nil
}

func validLinterDetectPath(file string) bool {
	if file == "" || utf8.RuneCountInString(file) > linterDetectPathMax || strings.ContainsAny(file, "\\*?[]{}:") ||
		strings.HasPrefix(file, "/") || strings.HasSuffix(file, "/") {
		return false
	}
	for _, segment := range strings.Split(file, "/") {
		if segment == "" || segment == "." || segment == ".." || checkContributionRunes(segment, false, false) != nil || strings.TrimSpace(segment) != segment {
			return false
		}
	}
	return true
}

// linterWorkspaceToggle reports whether key is a boolean, workspace-scoped
// property of the manifest's own contributes.configuration.
func linterWorkspaceToggle(manifest *Manifest, key string) bool {
	if manifest == nil || key == "" {
		return false
	}
	raw, ok := manifest.Contributes.Points["configuration"]
	if !ok {
		return false
	}
	var schema struct {
		Properties map[string]struct {
			Type  string `json:"type"`
			Scope string `json:"scope"`
		} `json:"properties"`
	}
	if json.Unmarshal(raw, &schema) != nil {
		return false
	}
	property, ok := schema.Properties[key]
	return ok && property.Type == "boolean" && property.Scope == "workspace"
}

// lintersExpandPermission reports a development generation that sends
// document text to new code: a linter gained a language or changed its action
// (a new linter counts as both).
func lintersExpandPermission(current, candidate *resolvedContribution) bool {
	if candidate == nil {
		return false
	}
	known := map[string]LinterContribution{}
	if current != nil {
		for _, entry := range current.Entries {
			if linter, ok := entry.(LinterContribution); ok {
				known[linter.ID] = linter
			}
		}
	}
	for _, entry := range candidate.Entries {
		linter, ok := entry.(LinterContribution)
		if !ok {
			continue
		}
		previous, exists := known[linter.ID]
		if !exists || previous.Action != linter.Action {
			return true
		}
		languages := map[string]bool{}
		for _, language := range previous.Languages {
			languages[language] = true
		}
		for _, language := range linter.Languages {
			if !languages[language] {
				return true
			}
		}
	}
	return false
}
