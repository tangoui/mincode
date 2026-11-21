package plugin

import (
	"encoding/json"
	"fmt"
	"strings"
)

type editorFrameworkCondition struct {
	File   string   `json:"file"`
	Field  []string `json:"field,omitempty"`
	Equals *string  `json:"equals,omitempty"`
}

type editorFrameworkCompletion struct {
	Label         string `json:"label"`
	InsertText    string `json:"insertText"`
	Context       string `json:"context"`
	Detail        string `json:"detail"`
	Documentation string `json:"documentation"`
}

type editorFrameworkContribution struct {
	ID        string   `json:"id"`
	Languages []string `json:"languages"`
	Match     struct {
		All     []editorFrameworkCondition `json:"all"`
		Any     []editorFrameworkCondition `json:"any"`
		Exclude []editorFrameworkCondition `json:"exclude"`
	} `json:"match"`
	Completions []editorFrameworkCompletion `json:"completions"`
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:              "editorFrameworks",
		Decode:           decodeEditorFramework,
		DuplicateMessage: func(id string) string { return fmt.Sprintf("duplicate editor framework %q", id) },
		IssuePrefix:      "invalid editorFrameworks contribution",
		EntryNoun:        "editor frameworks",
	})
}

func decodeEditorFramework(_ *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var entry editorFrameworkContribution
	if err := decodeStrictJSON(raw, &entry, true); err != nil {
		return nil, "", err
	}
	if !snippetLanguagePattern.MatchString(entry.ID) {
		return nil, entry.ID, fmt.Errorf("invalid framework id")
	}
	if len(entry.Languages) == 0 || len(entry.Languages) > 16 {
		return nil, entry.ID, fmt.Errorf("languages needs 1-16 entries")
	}
	for _, language := range entry.Languages {
		if !snippetLanguagePattern.MatchString(language) {
			return nil, entry.ID, fmt.Errorf("invalid language")
		}
	}
	if len(entry.Match.All) == 0 {
		return nil, entry.ID, fmt.Errorf("match.all must not be empty")
	}
	for _, conditions := range [][]editorFrameworkCondition{entry.Match.All, entry.Match.Any, entry.Match.Exclude} {
		if len(conditions) > 16 {
			return nil, entry.ID, fmt.Errorf("too many conditions")
		}
		for _, condition := range conditions {
			if condition.File == "" || len(condition.File) > 128 || strings.ContainsAny(condition.File, "\\:*?\x00") || strings.HasPrefix(condition.File, "/") {
				return nil, entry.ID, fmt.Errorf("invalid relative marker file")
			}
			for _, segment := range strings.Split(condition.File, "/") {
				if segment == "" || segment == "." || segment == ".." || strings.HasPrefix(segment, ".") {
					return nil, entry.ID, fmt.Errorf("invalid marker path segment")
				}
			}
			if len(condition.Field) > 8 {
				return nil, entry.ID, fmt.Errorf("field path too deep")
			}
			for _, field := range condition.Field {
				if field == "" || len(field) > 128 || field == "__proto__" || field == "constructor" || field == "prototype" {
					return nil, entry.ID, fmt.Errorf("invalid field path")
				}
			}
			if condition.Equals != nil && (len(condition.Field) == 0 || len(*condition.Equals) > 128) {
				return nil, entry.ID, fmt.Errorf("equals requires bounded field path")
			}
		}
	}
	if len(entry.Completions) > 1000 {
		return nil, entry.ID, fmt.Errorf("too many completions")
	}
	for _, completion := range entry.Completions {
		if completion.Label == "" || completion.Context == "" || !strings.HasPrefix(completion.Label, completion.Context) || len(completion.Label) > 128 || len(completion.Context) > 128 || len(completion.InsertText) > 1024 || completion.InsertText == "" || len(completion.Detail) > 1024 || len(completion.Documentation) > 4096 {
			return nil, entry.ID, fmt.Errorf("invalid completion metadata")
		}
	}
	return entry, entry.ID, nil
}
