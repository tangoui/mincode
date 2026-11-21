package plugin

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Editor language features (contributes.languageFeatures) are a process-only
// contribution point. An entry lists Monaco languages and features and names
// one of the plugin's own read-risk actions, which answers every listed
// feature and dispatches on payload.feature; the editor calls it through
// plugin_host_language_feature (language_feature_invoke.go). Features are the
// contract list lists.features: hover and completion (host 0.1.0) and
// definition, references, codeLens, inlayHints, documentSymbol and
// codeAction (host 0.3.0, contract valueSinceHost.features, which the CLI and
// the cloud use to raise the minimum host). hover, completion, definition,
// references and codeAction are interactive (the user hovers, types,
// navigates or opens the lightbulb); codeLens, inlayHints and documentSymbol
// are passive (the editor asks while the document is shown) and are listed in
// languageFeaturePassive. Mirrors: tools/mincode-cli/provider_points.go
// (CLI), assertLanguageFeaturesEntry (cloud) and
// vue-app/src/domain/plugins/language-features.ts (frontend revalidation,
// LANGUAGE_FEATURE_KINDS and PASSIVE_LANGUAGE_FEATURES).
const languageFeaturePointKey = "languageFeatures"

// languageFeaturePassive marks the features the editor requests without a
// user gesture; they get their own concurrency slot group, debounce and
// failure suspension (contract limits passive*).
var languageFeaturePassive = map[string]bool{"codeLens": true, "inlayHints": true, "documentSymbol": true}

var (
	languageFeatureSpec                 = mustContributionSpec(languageFeaturePointKey)
	languageFeatureTitleMaxRunes        = int(languageFeatureSpec.Limit("titleMaxRunes"))
	languageFeatureLocalesMax           = int(languageFeatureSpec.Limit("localesMax"))
	languageFeatureLanguagesMax         = int(languageFeatureSpec.Limit("languagesMax"))
	languageFeatureTriggerCharactersMax = int(languageFeatureSpec.Limit("triggerCharactersMax"))
	languageFeatureFeatures             = languageFeatureSpec.List("features")

	languageFeatureIDPattern      = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	languageFeatureTriggerPattern = regexp.MustCompile(`^[!-~]$`)
)

type languageFeatureManifestEntry struct {
	ID                string            `json:"id"`
	Title             string            `json:"title"`
	TitleLocalized    map[string]string `json:"titleLocalized,omitempty"`
	Languages         []string          `json:"languages"`
	Features          []string          `json:"features"`
	TriggerCharacters []string          `json:"triggerCharacters,omitempty"`
	Action            string            `json:"action"`
}

// LanguageFeatureContribution is the snapshot entry.
type LanguageFeatureContribution struct {
	ID                string            `json:"id"`
	Title             string            `json:"title"`
	TitleLocalized    map[string]string `json:"titleLocalized,omitempty"`
	Languages         []string          `json:"languages"`
	Features          []string          `json:"features"`
	TriggerCharacters []string          `json:"triggerCharacters,omitempty"`
	Action            string            `json:"action"`
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:              languageFeaturePointKey,
		Decode:           decodeLanguageFeatureEntry,
		DuplicateMessage: func(id string) string { return fmt.Sprintf("duplicate language feature provider id %q", id) },
		IssuePrefix:      "invalid languageFeatures contribution",
		IssueText: func(issue ContributionIssue) string {
			if issue.ID != "" {
				return fmt.Sprintf("skipped language feature provider %q: %s", issue.ID, issue.Message)
			}
			return fmt.Sprintf("skipped contributes.languageFeatures[%d]: %s", issue.Entry, issue.Message)
		},
		EntryNoun: "language feature providers",
		// Adding a language or feature also sends new documents to the action.
		ExpandsPermission: func(current, candidate *resolvedContribution) bool {
			return contributionActionsExpand(current, candidate, func(entry any) (string, []string, bool) {
				provider, ok := entry.(LanguageFeatureContribution)
				languages := slices.Clone(provider.Languages)
				slices.Sort(languages)
				features := slices.Clone(provider.Features)
				slices.Sort(features)
				return provider.ID, []string{provider.Action, strings.Join(languages, ","), strings.Join(features, ",")}, ok
			})
		},
	})
}

func decodeLanguageFeatureEntry(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var entry languageFeatureManifestEntry
	if err := decodeStrictJSON(raw, &entry, true); err != nil {
		return nil, "", fmt.Errorf("parse language feature provider: %w", err)
	}
	id := strings.TrimSpace(entry.ID)
	if !languageFeatureIDPattern.MatchString(id) {
		return nil, id, fmt.Errorf("language feature provider id %q must match ^[a-z][a-z0-9-]{0,31}$", id)
	}
	title, err := validateContributionLine(entry.Title, languageFeatureTitleMaxRunes, true)
	if err != nil {
		return nil, id, fmt.Errorf("language feature provider %q title %w", id, err)
	}
	titleLocalized, err := validateContributionLocalized(entry.TitleLocalized, languageFeatureTitleMaxRunes, languageFeatureLocalesMax)
	if err != nil {
		return nil, id, fmt.Errorf("language feature provider %q titleLocalized %w", id, err)
	}
	if len(entry.Languages) == 0 || len(entry.Languages) > languageFeatureLanguagesMax {
		return nil, id, fmt.Errorf("language feature provider %q needs 1-%d languages", id, languageFeatureLanguagesMax)
	}
	for index, language := range entry.Languages {
		if !formatterLanguagePattern.MatchString(language) {
			return nil, id, fmt.Errorf("language feature provider %q language %q must match ^[a-z][a-z0-9+#._-]{0,31}$", id, language)
		}
		if slices.Contains(entry.Languages[:index], language) {
			return nil, id, fmt.Errorf("language feature provider %q repeats language %q", id, language)
		}
	}
	if len(entry.Features) == 0 {
		return nil, id, fmt.Errorf("language feature provider %q needs at least one feature", id)
	}
	for index, feature := range entry.Features {
		if !slices.Contains(languageFeatureFeatures, feature) {
			return nil, id, fmt.Errorf("language feature provider %q feature %q must be one of %s", id, feature, strings.Join(languageFeatureFeatures, ", "))
		}
		if slices.Contains(entry.Features[:index], feature) {
			return nil, id, fmt.Errorf("language feature provider %q repeats feature %q", id, feature)
		}
	}
	if len(entry.TriggerCharacters) > 0 && !slices.Contains(entry.Features, "completion") {
		return nil, id, fmt.Errorf("language feature provider %q triggerCharacters need the completion feature", id)
	}
	if len(entry.TriggerCharacters) > languageFeatureTriggerCharactersMax {
		return nil, id, fmt.Errorf("language feature provider %q allows at most %d triggerCharacters", id, languageFeatureTriggerCharactersMax)
	}
	for index, trigger := range entry.TriggerCharacters {
		if !languageFeatureTriggerPattern.MatchString(trigger) {
			return nil, id, fmt.Errorf("language feature provider %q trigger character %q must be one printable ASCII character", id, trigger)
		}
		if slices.Contains(entry.TriggerCharacters[:index], trigger) {
			return nil, id, fmt.Errorf("language feature provider %q repeats trigger character %q", id, trigger)
		}
	}
	actionID, err := contributionReadActionRef(ctx, "language feature provider", id, "action", entry.Action)
	if err != nil {
		return nil, id, err
	}
	var triggers []string
	if len(entry.TriggerCharacters) > 0 {
		triggers = slices.Clone(entry.TriggerCharacters)
	}
	return LanguageFeatureContribution{
		ID: id, Title: title, TitleLocalized: titleLocalized,
		Languages: slices.Clone(entry.Languages), Features: slices.Clone(entry.Features),
		TriggerCharacters: triggers, Action: actionID,
	}, id, nil
}
