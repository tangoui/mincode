package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// JSON schema associations (contributes.jsonValidation) are a data-only
// contribution point for declarative and process plugins. An entry binds 1-8
// narrow file-name patterns to a JSON schema file in the package. Schemas are
// assets: the snapshot carries only the content-hash key, the frontend fetches
// the canonical bytes lazily and hands them to Monaco's JSON worker with
// schema requests disabled. The host rejects anything that could reach the
// network or redirect resolution (non-fragment $ref, nested $id) and bounds
// size, depth, node and regex counts; the frontend runs the regex heuristics
// and probe before a schema reaches the worker. Mirrors:
// tools/mincode-cli/json_validation.go (CLI), assertJsonValidationEntry /
// assertJsonValidationAsset (cloud) and
// vue-app/src/domain/plugins/json-validation.ts (frontend revalidation).
const (
	jsonValidationPointKey  = "jsonValidation"
	jsonValidationAssetHint = "schema"
	jsonValidationMime      = "application/schema+json"
	// jsonValidationSchemaURIMax bounds $schema and fragment $ref strings.
	jsonValidationSchemaURIMax = 256
)

var (
	jsonValidationSpec             = mustContributionSpec(jsonValidationPointKey)
	jsonValidationFileMatchMax     = int(jsonValidationSpec.Limit("fileMatchMax"))
	jsonValidationPatternMaxLength = int(jsonValidationSpec.Limit("patternMaxLength"))
	jsonValidationTitleMaxRunes    = int(jsonValidationSpec.Limit("titleMaxRunes"))
	jsonValidationLocalesMax       = int(jsonValidationSpec.Limit("localesMax"))
	jsonValidationMaxDepth         = int(jsonValidationSpec.Limit("schemaMaxDepth"))
	jsonValidationMaxNodes         = int(jsonValidationSpec.Limit("schemaMaxNodes"))
	jsonValidationRegexMax         = int(jsonValidationSpec.Limit("schemaRegexMax"))
	jsonValidationRegexMaxLength   = int(jsonValidationSpec.Limit("schemaRegexMaxLength"))
)

type jsonValidationManifestEntry struct {
	FileMatch      []string          `json:"fileMatch"`
	Schema         string            `json:"schema"`
	Title          string            `json:"title,omitempty"`
	TitleLocalized map[string]string `json:"titleLocalized,omitempty"`
}

// JSONValidationContribution is the snapshot entry. ID is the schema path as
// written in the manifest; Schema is the canonical asset's content-hash key.
type JSONValidationContribution struct {
	ID             string            `json:"id"`
	Title          string            `json:"title,omitempty"`
	TitleLocalized map[string]string `json:"titleLocalized,omitempty"`
	FileMatch      []string          `json:"fileMatch"`
	Schema         string            `json:"schema"`
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:              jsonValidationPointKey,
		Decode:           decodeJSONValidationEntry,
		DuplicateMessage: func(id string) string { return fmt.Sprintf("duplicate jsonValidation schema %q", id) },
		IssuePrefix:      "invalid jsonValidation contribution",
		IssueText: func(issue ContributionIssue) string {
			if issue.ID != "" {
				return fmt.Sprintf("skipped JSON schema %q: %s", issue.ID, issue.Message)
			}
			return fmt.Sprintf("skipped contributes.jsonValidation[%d]: %s", issue.Entry, issue.Message)
		},
		EntryNoun:     "JSON schema associations",
		ValidateAsset: validateJSONValidationAsset,
	})
}

func decodeJSONValidationEntry(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var entry jsonValidationManifestEntry
	if err := decodeStrictJSON(raw, &entry, true); err != nil {
		return nil, "", fmt.Errorf("parse jsonValidation entry: %w", err)
	}
	id := strings.TrimSpace(entry.Schema)
	if id == "" {
		return nil, "", errors.New("schema is required")
	}
	result := JSONValidationContribution{ID: id}
	if len(entry.FileMatch) == 0 || len(entry.FileMatch) > jsonValidationFileMatchMax {
		return nil, id, fmt.Errorf("fileMatch needs 1-%d patterns", jsonValidationFileMatchMax)
	}
	seen := map[string]bool{}
	for _, pattern := range entry.FileMatch {
		if err := validateJSONValidationPattern(pattern); err != nil {
			return nil, id, fmt.Errorf("fileMatch %q %w", pattern, err)
		}
		lower := strings.ToLower(pattern)
		if seen[lower] {
			return nil, id, fmt.Errorf("fileMatch repeats %q", pattern)
		}
		seen[lower] = true
		result.FileMatch = append(result.FileMatch, pattern)
	}
	title, err := validateContributionLine(entry.Title, jsonValidationTitleMaxRunes, false)
	if err != nil {
		return nil, id, fmt.Errorf("title %w", err)
	}
	result.Title = title
	localized, err := validateContributionLocalized(entry.TitleLocalized, jsonValidationTitleMaxRunes, jsonValidationLocalesMax)
	if err != nil {
		return nil, id, fmt.Errorf("titleLocalized %w", err)
	}
	result.TitleLocalized = localized
	key, err := ctx.readAsset(id, jsonValidationAssetHint)
	if err != nil {
		return nil, id, err
	}
	result.Schema = key
	return result, id, nil
}

// validateJSONValidationPattern applies the customEditors alphabet and path
// structure rules plus the jsonValidation breadth rule: the pattern ends in
// .json or .jsonc and its basename keeps at least 3 literal characters
// before that extension, so "*.json", "**/*.json" and "?.json" are refused.
// Errors are bare ("must ...").
func validateJSONValidationPattern(pattern string) error {
	if pattern == "" {
		return errors.New("is empty")
	}
	if len(pattern) > jsonValidationPatternMaxLength {
		return fmt.Errorf("exceeds %d characters", jsonValidationPatternMaxLength)
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
	lower := strings.ToLower(pattern)
	base := lower[strings.LastIndex(lower, "/")+1:]
	var stem string
	switch {
	case strings.HasSuffix(base, ".jsonc"):
		stem = strings.TrimSuffix(base, ".jsonc")
	case strings.HasSuffix(base, ".json"):
		stem = strings.TrimSuffix(base, ".json")
	default:
		return errors.New("must end in .json or .jsonc")
	}
	literal := strings.NewReplacer("*", "", "?", "").Replace(stem)
	if utf8.RuneCountInString(literal) < 3 {
		return errors.New("is too broad: the file name needs at least 3 literal characters before .json")
	}
	return nil
}

// validateJSONValidationAsset checks one schema file and returns its
// canonical bytes (compact, key order preserved, root $id/id removed).
func validateJSONValidationAsset(_ string, ext string, data []byte) ([]byte, string, error) {
	if ext != ".json" {
		return nil, "", fmt.Errorf("schema must be a .json file, not %q", ext)
	}
	nodes, regexes := 0, 0
	regex := func(value string, what string) error {
		regexes++
		if regexes > jsonValidationRegexMax {
			return fmt.Errorf("schema has more than %d patterns", jsonValidationRegexMax)
		}
		if utf8.RuneCountInString(value) > jsonValidationRegexMaxLength {
			return fmt.Errorf("schema %s exceeds %d characters", what, jsonValidationRegexMaxLength)
		}
		return nil
	}
	canonical, err := strictJSONWalk(data, func(path []string, key string, value any) error {
		nodes++
		if nodes > jsonValidationMaxNodes {
			return fmt.Errorf("schema has more than %d nodes", jsonValidationMaxNodes)
		}
		if path == nil {
			if value != jsonObjectValue {
				return errors.New("schema root must be an object")
			}
			return nil
		}
		if len(path) >= jsonValidationMaxDepth {
			return fmt.Errorf("schema is nested deeper than %d levels", jsonValidationMaxDepth)
		}
		text, isString := value.(string)
		// patternProperties keys are regexes whatever their value.
		if len(path) > 0 && path[len(path)-1] == "patternProperties" {
			if err := regex(key, "patternProperties key"); err != nil {
				return err
			}
		}
		// Keyword names are never array indices, so array elements fall through.
		if !isString {
			return nil
		}
		switch key {
		case "$ref", "$dynamicRef", "$recursiveRef":
			if !strings.HasPrefix(text, "#") {
				return fmt.Errorf("schema %s %q must be a same-document fragment starting with #", key, text)
			}
			if len(text) > jsonValidationRegexMaxLength {
				return fmt.Errorf("schema %s exceeds %d characters", key, jsonValidationRegexMaxLength)
			}
		case "$id", "id":
			if len(path) == 0 {
				return errSkipJSONMember
			}
			if key == "$id" {
				return fmt.Errorf("schema must not declare a nested $id (%s)", strings.Join(path, "."))
			}
		case "$schema":
			if len(text) > jsonValidationSchemaURIMax {
				return fmt.Errorf("schema $schema exceeds %d characters", jsonValidationSchemaURIMax)
			}
		case "pattern":
			if err := regex(text, "pattern"); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return canonical, jsonValidationMime, nil
}
