package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"mincode/app/plugins/api/filetmpl"
	"regexp"
	"strings"
)

// File and project templates (contributes.fileTemplates) are data only: each
// entry lists target paths and .tmpl source assets. The user picks a
// template, reviews a preview of every path, and the host frontend creates the
// files through local_fs_write_text_file with createOnly (exclusive create,
// mode 0644), so nothing is overwritten and nothing becomes executable. The
// only substitution is a fixed allowlist of {{mincode.<variable>}} tokens;
// any other {{...}} stays literal. There are no hooks, scripts or
// post-create commands. Target paths are relative, shallow, avoid VCS and
// dependency directories and sensitive filenames; contents reject NUL, stray
// control characters and bidirectional controls (Trojan Source).

const fileTemplatePointKey = "fileTemplates"

const fileTemplateMime = "text/plain"

var (
	fileTemplateDeniedSegments   = fileTemplateSpec.List("deniedPathSegments")
	fileTemplateDepthMax         = int(fileTemplateSpec.Limit("targetDepthMax"))
	fileTemplatePathMaxLength    = int(fileTemplateSpec.Limit("targetPathMaxLength"))
	fileTemplateSpec             = mustContributionSpec(fileTemplatePointKey)
	fileTemplateTitleMaxRunes    = int(fileTemplateSpec.Limit("titleMaxRunes"))
	fileTemplateDescriptionRunes = int(fileTemplateSpec.Limit("descriptionMaxRunes"))
	fileTemplateLocalesMax       = int(fileTemplateSpec.Limit("localesMax"))
	fileTemplateCategoryMaxRunes = int(fileTemplateSpec.Limit("categoryMaxRunes"))
	fileTemplateFilesMax         = int(fileTemplateSpec.Limit("filesPerTemplate"))
	fileTemplateMaxBytes         = fileTemplateSpec.Limit("templateMaxBytes")
	fileTemplateIDPattern        = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	fileTemplateDefaultName      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._ -]{0,63}$`)
)

// FileTemplateFile is one target file: Path is the unrendered target path
// template and Source the content-hash key of its .tmpl asset.
type FileTemplateFile struct {
	Path   string `json:"path"`
	Source string `json:"source"`
	Size   int64  `json:"size"`
}

// FileTemplateContribution is one validated contributes.fileTemplates entry
// as sent to the frontend consumer.
type FileTemplateContribution struct {
	ID                   string             `json:"id"`
	Kind                 string             `json:"kind"`
	Title                string             `json:"title"`
	TitleLocalized       map[string]string  `json:"titleLocalized,omitempty"`
	Description          string             `json:"description,omitempty"`
	DescriptionLocalized map[string]string  `json:"descriptionLocalized,omitempty"`
	Category             string             `json:"category,omitempty"`
	DefaultName          string             `json:"defaultName,omitempty"`
	Files                []FileTemplateFile `json:"files"`
}

type fileTemplateFileReference struct {
	Path   *string `json:"path"`
	Source *string `json:"source"`
}

type fileTemplateEntryReference struct {
	ID                   string                      `json:"id"`
	Kind                 string                      `json:"kind"`
	Title                string                      `json:"title"`
	TitleLocalized       map[string]string           `json:"titleLocalized"`
	Description          *string                     `json:"description"`
	DescriptionLocalized map[string]string           `json:"descriptionLocalized"`
	Category             *string                     `json:"category"`
	DefaultName          *string                     `json:"defaultName"`
	Files                []fileTemplateFileReference `json:"files"`
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:              fileTemplatePointKey,
		Decode:           decodeFileTemplateEntry,
		DuplicateMessage: func(id string) string { return fmt.Sprintf("duplicate file template id %q", id) },
		IssuePrefix:      "invalid fileTemplates contribution",
		IssueText: func(issue ContributionIssue) string {
			if issue.ID != "" {
				return fmt.Sprintf("skipped file template %q: %s", issue.ID, issue.Message)
			}
			return fmt.Sprintf("skipped contributes.fileTemplates[%d]: %s", issue.Entry, issue.Message)
		},
		EntryNoun:     "file templates",
		ValidateAsset: validateFileTemplateAsset,
	})
}

func decodeFileTemplateEntry(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var reference fileTemplateEntryReference
	if err := decodeStrictJSON(raw, &reference, true); err != nil {
		return nil, "", fmt.Errorf("parse file template entry: %w", err)
	}
	id := strings.TrimSpace(reference.ID)
	if !fileTemplateIDPattern.MatchString(id) {
		return nil, id, fmt.Errorf("file template id %q must match ^[a-z0-9][a-z0-9-]{0,31}$", reference.ID)
	}
	kind := strings.TrimSpace(reference.Kind)
	if kind != "file" && kind != "project" {
		return nil, id, errors.New(`kind must be "file" or "project"`)
	}
	item := FileTemplateContribution{ID: id, Kind: kind}
	var err error
	if item.Title, err = validateContributionLine(reference.Title, fileTemplateTitleMaxRunes, true); err != nil {
		return nil, id, fmt.Errorf("title %w", err)
	}
	if item.TitleLocalized, err = validateContributionLocalized(reference.TitleLocalized, fileTemplateTitleMaxRunes, fileTemplateLocalesMax); err != nil {
		return nil, id, fmt.Errorf("titleLocalized %w", err)
	}
	if reference.Description != nil {
		if item.Description, err = validateContributionLine(*reference.Description, fileTemplateDescriptionRunes, false); err != nil {
			return nil, id, fmt.Errorf("description %w", err)
		}
	}
	if item.DescriptionLocalized, err = validateContributionLocalized(reference.DescriptionLocalized, fileTemplateDescriptionRunes, fileTemplateLocalesMax); err != nil {
		return nil, id, fmt.Errorf("descriptionLocalized %w", err)
	}
	if reference.Category != nil {
		if item.Category, err = validateContributionLine(*reference.Category, fileTemplateCategoryMaxRunes, false); err != nil {
			return nil, id, fmt.Errorf("category %w", err)
		}
	}
	if reference.DefaultName != nil {
		name := strings.TrimSpace(*reference.DefaultName)
		if !fileTemplateDefaultName.MatchString(name) || strings.Contains(name, "..") {
			return nil, id, errors.New("defaultName must match ^[A-Za-z0-9][A-Za-z0-9._ -]{0,63}$")
		}
		item.DefaultName = name
	}

	switch {
	case kind == "file" && len(reference.Files) != 1:
		return nil, id, errors.New(`a "file" template must have exactly one file`)
	case len(reference.Files) == 0:
		return nil, id, errors.New("files must not be empty")
	case len(reference.Files) > fileTemplateFilesMax:
		return nil, id, fmt.Errorf("files must have at most %d entries", fileTemplateFilesMax)
	}
	seen := map[string]bool{}
	var total int64
	item.Files = make([]FileTemplateFile, 0, len(reference.Files))
	for index, file := range reference.Files {
		label := fmt.Sprintf("files[%d]", index)
		if file.Path == nil || file.Source == nil {
			return nil, id, fmt.Errorf(`%s needs "path" and "source"`, label)
		}
		path, err := validateFileTemplateTargetPath(*file.Path)
		if err != nil {
			return nil, id, fmt.Errorf("%s.path %w", label, err)
		}
		folded := strings.ToLower(path)
		if seen[folded] {
			return nil, id, fmt.Errorf("%s.path %q is listed twice", label, path)
		}
		seen[folded] = true
		key, err := ctx.readAsset(*file.Source, "source")
		if err != nil {
			return nil, id, fmt.Errorf("%s: %w", label, err)
		}
		size := ctx.pending[key].Size
		total += size
		if total > fileTemplateMaxBytes {
			return nil, id, fmt.Errorf("template files must total at most %d bytes", fileTemplateMaxBytes)
		}
		item.Files = append(item.Files, FileTemplateFile{Path: path, Source: key, Size: size})
	}
	return item, id, nil
}

// validateFileTemplateTargetPath checks an unrendered target path: relative
// POSIX, no empty, "." or ".." segments, only [A-Za-z0-9._@+-] and
// {{mincode.<variable>}} tokens, at most targetDepthMax segments and
// targetPathMaxLength bytes, no denied segment and no sensitive literal
// basename. The frontend re-runs these rules after substitution.
func validateFileTemplateTargetPath(value string) (string, error) { return filetmpl.TargetPath(value) }
func validateFileTemplateAsset(path, ext string, data []byte) ([]byte, string, error) {
	return filetmpl.Asset(path, ext, data)
}

// fileTemplates returns the validated entries of a manifest.
func (manifest Manifest) fileTemplates() []FileTemplateContribution {
	resolved := manifest.contribution(fileTemplatePointKey)
	if resolved == nil {
		return nil
	}
	items := make([]FileTemplateContribution, 0, len(resolved.Entries))
	for _, entry := range resolved.Entries {
		if item, ok := entry.(FileTemplateContribution); ok {
			items = append(items, item)
		}
	}
	return items
}

func isFileTemplateSensitiveName(name string) bool {
	for _, s := range fileTemplateSpec.SensitiveFilenames {
		if strings.EqualFold(s, name) {
			return true
		}
	}
	return false
}
