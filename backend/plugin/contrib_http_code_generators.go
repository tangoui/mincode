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

	"mincode/app/plugins/api/codetmpl"
)

// HTTP code generators (contributes.httpCodeGenerators) are data only: each
// entry names a .tmpl asset written in the logic-less codetmpl language
// (plugins/api/codetmpl). The host validates the template grammar and
// vocabulary at install; the WebView renders it purely against a redacted
// request model (auth, sensitive headers/keys and credential-shaped values
// replaced by YOUR_* placeholders, environment variables never resolved). The
// plugin process never sees request data and no plugin code runs when the
// user generates code.

const httpCodeGeneratorPointKey = "httpCodeGenerators"

var (
	httpCodeGeneratorSpec          = mustContributionSpec(httpCodeGeneratorPointKey)
	httpCodeGeneratorTitleMaxRunes = int(httpCodeGeneratorSpec.Limit("titleMaxRunes"))
	httpCodeGeneratorLocalesMax    = int(httpCodeGeneratorSpec.Limit("localesMax"))
	httpCodeGeneratorEscapes       = httpCodeGeneratorSpec.List("escapes")
	httpCodeGeneratorIDPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	httpCodeGeneratorLimits        = codetmpl.TemplateLimits{
		MaxNodes: int(httpCodeGeneratorSpec.Limit("templateNodesMax")),
		MaxDepth: int(httpCodeGeneratorSpec.Limit("sectionDepthMax")),
	}
	httpCodeGeneratorVocabulary = codetmpl.Vocabulary{
		Variables:        httpCodeGeneratorSpec.List("variables"),
		ItemVariables:    httpCodeGeneratorSpec.List("itemVariables"),
		ListSections:     httpCodeGeneratorSpec.List("listSections"),
		FlagSections:     httpCodeGeneratorSpec.List("flagSections"),
		ItemFlagSections: httpCodeGeneratorSpec.List("itemFlagSections"),
		Filters:          httpCodeGeneratorSpec.List("filters"),
	}
)

// HTTPCodeGeneratorContribution is one validated entry as sent to the
// frontend consumer; Template is the content-hash key of the .tmpl asset.
type HTTPCodeGeneratorContribution struct {
	ID             string            `json:"id"`
	Title          string            `json:"title"`
	TitleLocalized map[string]string `json:"titleLocalized,omitempty"`
	Escape         string            `json:"escape"`
	Template       string            `json:"template"`
}

type httpCodeGeneratorManifestEntry struct {
	ID             string            `json:"id"`
	Title          string            `json:"title"`
	TitleLocalized map[string]string `json:"titleLocalized"`
	Escape         string            `json:"escape"`
	Template       *string           `json:"template"`
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:              httpCodeGeneratorPointKey,
		Decode:           decodeHTTPCodeGeneratorEntry,
		DuplicateMessage: func(id string) string { return fmt.Sprintf("duplicate HTTP code generator id %q", id) },
		IssuePrefix:      "invalid httpCodeGenerators contribution",
		IssueText: func(issue ContributionIssue) string {
			if issue.ID != "" {
				return fmt.Sprintf("skipped HTTP code generator %q: %s", issue.ID, issue.Message)
			}
			return fmt.Sprintf("skipped contributes.httpCodeGenerators[%d]: %s", issue.Entry, issue.Message)
		},
		EntryNoun:     "HTTP code generators",
		ValidateAsset: validateHTTPCodeGeneratorAsset,
	})
}

func decodeHTTPCodeGeneratorEntry(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var entry httpCodeGeneratorManifestEntry
	if err := decodeStrictJSON(raw, &entry, true); err != nil {
		return nil, "", fmt.Errorf("parse HTTP code generator entry: %w", err)
	}
	id := strings.TrimSpace(entry.ID)
	if !httpCodeGeneratorIDPattern.MatchString(id) {
		return nil, id, fmt.Errorf("HTTP code generator id %q must match ^[a-z0-9][a-z0-9-]{0,31}$", entry.ID)
	}
	item := HTTPCodeGeneratorContribution{ID: id}
	var err error
	if item.Title, err = validateContributionLine(entry.Title, httpCodeGeneratorTitleMaxRunes, true); err != nil {
		return nil, id, fmt.Errorf("title %w", err)
	}
	if item.TitleLocalized, err = validateContributionLocalized(entry.TitleLocalized, httpCodeGeneratorTitleMaxRunes, httpCodeGeneratorLocalesMax); err != nil {
		return nil, id, fmt.Errorf("titleLocalized %w", err)
	}
	item.Escape = strings.TrimSpace(entry.Escape)
	if !slices.Contains(httpCodeGeneratorEscapes, item.Escape) {
		return nil, id, fmt.Errorf("escape %q must be one of %s", entry.Escape, strings.Join(httpCodeGeneratorEscapes, ", "))
	}
	if entry.Template == nil || strings.TrimSpace(*entry.Template) == "" {
		return nil, id, errors.New(`"template" is required`)
	}
	if item.Template, err = ctx.readAsset(*entry.Template, "template"); err != nil {
		return nil, id, fmt.Errorf("template: %w", err)
	}
	return item, id, nil
}

// validateHTTPCodeGeneratorAsset checks a .tmpl template: UTF-8 (BOM
// stripped), no NUL or controls except tab, LF and CR, no bidirectional or
// zero-width characters (Trojan Source), and a codetmpl grammar within the
// contract vocabulary and limits. The canonical form is the stripped bytes.
func validateHTTPCodeGeneratorAsset(_ string, ext string, data []byte) ([]byte, string, error) {
	if ext != ".tmpl" {
		return nil, "", fmt.Errorf("unsupported template extension %q (must be .tmpl)", ext)
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if err := checkHTTPCodeGeneratorTemplateText(data); err != nil {
		return nil, "", err
	}
	if err := codetmpl.Parse(string(data), httpCodeGeneratorLimits, httpCodeGeneratorVocabulary); err != nil {
		return nil, "", err
	}
	return data, "text/plain", nil
}

func checkHTTPCodeGeneratorTemplateText(data []byte) error {
	if !utf8.Valid(data) {
		return errors.New("template must be valid UTF-8")
	}
	for _, r := range string(data) {
		if r == '\r' {
			continue
		}
		if isUnsafeContributionRune(r, true, true) {
			if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
				return fmt.Errorf("template contains a control character U+%04X", r)
			}
			return fmt.Errorf("template contains a bidirectional or zero-width character U+%04X", r)
		}
	}
	return nil
}

// httpCodeGenerators returns the validated entries of a manifest.
func (manifest Manifest) httpCodeGenerators() []HTTPCodeGeneratorContribution {
	resolved := manifest.contribution(httpCodeGeneratorPointKey)
	if resolved == nil {
		return nil
	}
	items := make([]HTTPCodeGeneratorContribution, 0, len(resolved.Entries))
	for _, entry := range resolved.Entries {
		if item, ok := entry.(HTTPCodeGeneratorContribution); ok {
			items = append(items, item)
		}
	}
	return items
}
