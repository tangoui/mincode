package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"mincode/app/plugins/api/svgsafe"
)

// File icon themes (contributes.fileIcons) are a global, data-only
// contribution point for declarative and process plugins. A theme maps file
// names, compound extensions and folder names to icon definitions whose
// SVG/PNG files live in the package. The host never inlines icon bytes into
// the snapshot: each definition becomes the content-hash key of the
// canonical asset (SVG re-serialized by svgsafe, PNG re-encoded), served
// on demand by plugin_host_contribution_assets and re-validated on every
// fetch. The frontend renders icons only in image context (<img> or a CSS
// mask over a blob: URL), never as markup. Mirrors:
// tools/mincode-cli/file_icons.go (CLI), assertFileIconsEntry (cloud) and
// vue-app/src/domain/projects/file-icon-theme.ts (frontend revalidation).
const (
	fileIconPointKey  = "fileIcons"
	fileIconAssetHint = "icon"

	fileIconMimeSVG = contributionImageMimeSVG
	fileIconMimePNG = contributionImageMimePNG
)

var (
	fileIconSpec                 = mustContributionSpec(fileIconPointKey)
	fileIconTitleMaxRunes        = int(fileIconSpec.Limit("titleMaxRunes"))
	fileIconLocalesMax           = int(fileIconSpec.Limit("localesMax"))
	fileIconDefinitionsMax       = int(fileIconSpec.Limit("iconsPerTheme"))
	fileIconMappingsMax          = int(fileIconSpec.Limit("mappingsMax"))
	fileIconKeyMaxLength         = int(fileIconSpec.Limit("keyMaxLength"))
	fileIconSnapshotBytesPerPlug = int(fileIconSpec.Limit("snapshotBytesPerPlugin"))
	fileIconImageLimits          = contributionImageLimits{
		PNGMaxDimension: int(fileIconSpec.Limit("pngMaxDimension")),
		SVG: svgsafe.Limits{
			MaxBytes:     int(fileIconSpec.Assets.MaxBytes),
			MaxElements:  int(fileIconSpec.Limit("svgMaxElements")),
			MaxDepth:     int(fileIconSpec.Limit("svgMaxDepth")),
			MaxAttrBytes: int(fileIconSpec.Limit("svgMaxAttrBytes")),
			MaxUse:       int(fileIconSpec.Limit("svgMaxUse")),
			MaxDimension: int(fileIconSpec.Limit("svgMaxDimension")),
		},
	}

	fileIconThemeIDPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	fileIconDefinitionIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	fileIconExtensionPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9._+-]{0,63}$`)
)

type fileIconDefinition struct {
	Path string `json:"path"`
}

// fileIconMappings are the fields a theme and its light variant share.
type fileIconMappings struct {
	File                string            `json:"file,omitempty"`
	Folder              string            `json:"folder,omitempty"`
	FolderExpanded      string            `json:"folderExpanded,omitempty"`
	RootFolder          string            `json:"rootFolder,omitempty"`
	RootFolderExpanded  string            `json:"rootFolderExpanded,omitempty"`
	FileExtensions      map[string]string `json:"fileExtensions,omitempty"`
	FileNames           map[string]string `json:"fileNames,omitempty"`
	FolderNames         map[string]string `json:"folderNames,omitempty"`
	FolderNamesExpanded map[string]string `json:"folderNamesExpanded,omitempty"`
}

type fileIconManifestEntry struct {
	ID              string                        `json:"id"`
	Title           string                        `json:"title"`
	TitleLocalized  map[string]string             `json:"titleLocalized,omitempty"`
	Monochrome      bool                          `json:"monochrome,omitempty"`
	IconDefinitions map[string]fileIconDefinition `json:"iconDefinitions"`
	fileIconMappings
	Light *fileIconMappings `json:"light,omitempty"`
}

// FileIconThemeContribution is the snapshot entry: icons maps each
// definition id to the content-hash key of its canonical asset. Mapping maps
// are always present (possibly empty) so the frontend never sees null.
type FileIconThemeContribution struct {
	ID                  string            `json:"id"`
	Title               string            `json:"title"`
	TitleLocalized      map[string]string `json:"titleLocalized,omitempty"`
	Monochrome          bool              `json:"monochrome"`
	Icons               map[string]string `json:"icons"`
	File                string            `json:"file"`
	Folder              string            `json:"folder,omitempty"`
	FolderExpanded      string            `json:"folderExpanded,omitempty"`
	RootFolder          string            `json:"rootFolder,omitempty"`
	RootFolderExpanded  string            `json:"rootFolderExpanded,omitempty"`
	FileExtensions      map[string]string `json:"fileExtensions"`
	FileNames           map[string]string `json:"fileNames"`
	FolderNames         map[string]string `json:"folderNames"`
	FolderNamesExpanded map[string]string `json:"folderNamesExpanded"`
	Light               *fileIconMappings `json:"light,omitempty"`
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:              fileIconPointKey,
		Decode:           decodeFileIconEntry,
		Finalize:         finalizeFileIconEntries,
		DuplicateMessage: func(id string) string { return fmt.Sprintf("duplicate file icon theme id %q", id) },
		IssuePrefix:      "invalid fileIcons contribution",
		IssueText: func(issue ContributionIssue) string {
			if issue.ID != "" {
				return fmt.Sprintf("skipped file icon theme %q: %s", issue.ID, issue.Message)
			}
			if issue.Entry >= 0 {
				return fmt.Sprintf("skipped contributes.fileIcons[%d]: %s", issue.Entry, issue.Message)
			}
			return "skipped file icon theme: " + issue.Message
		},
		EntryNoun:  "file icon themes",
		FileLabels: contributionFileLabels{Noun: "file icon theme", Entry: "file icon theme", ShapeHint: "a file icon theme object"},
		DecodeReference: func(raw json.RawMessage) (string, error) {
			var reference fileIconDefinition
			if err := decodeStrictJSON(raw, &reference, true); err != nil {
				return "", err
			}
			return reference.Path, nil
		},
		ValidateAsset: validateFileIconAsset,
	})
}

// validateFileIconAsset returns the canonical bytes that are hashed and
// served (see validateContributionImage).
func validateFileIconAsset(_ string, ext string, data []byte) ([]byte, string, error) {
	return validateContributionImage(ext, data, fileIconImageLimits)
}

func decodeFileIconEntry(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var entry fileIconManifestEntry
	if err := decodeStrictJSON(raw, &entry, true); err != nil {
		return nil, entry.ID, fmt.Errorf("parse file icon theme: %w", err)
	}
	entry.ID = strings.TrimSpace(entry.ID)
	if !fileIconThemeIDPattern.MatchString(entry.ID) {
		return nil, entry.ID, fmt.Errorf("file icon theme id %q must match [a-z0-9][a-z0-9._-]{0,63}", entry.ID)
	}
	prefix := ctx.manifest.ID + "."
	if !strings.HasPrefix(entry.ID, prefix) || len(entry.ID) == len(prefix) {
		return nil, entry.ID, fmt.Errorf("file icon theme id %q must start with %q", entry.ID, prefix)
	}
	theme, err := validateFileIconTheme(&entry)
	if err != nil {
		return nil, entry.ID, err
	}
	defIDs := make([]string, 0, len(entry.IconDefinitions))
	for defID := range entry.IconDefinitions {
		defIDs = append(defIDs, defID)
	}
	sort.Strings(defIDs)
	for _, defID := range defIDs {
		key, err := ctx.readAsset(entry.IconDefinitions[defID].Path, fileIconAssetHint)
		if err != nil {
			return nil, entry.ID, fmt.Errorf("iconDefinitions.%s: %w", defID, err)
		}
		theme.Icons[defID] = key
	}
	return theme, entry.ID, nil
}

// validateFileIconTheme checks everything except the asset files and
// returns the snapshot value with an empty Icons map.
func validateFileIconTheme(entry *fileIconManifestEntry) (FileIconThemeContribution, error) {
	theme := FileIconThemeContribution{ID: entry.ID, Monochrome: entry.Monochrome, Icons: map[string]string{}}
	theme.Title = strings.TrimSpace(entry.Title)
	if theme.Title == "" || utf8.RuneCountInString(theme.Title) > fileIconTitleMaxRunes || strings.IndexFunc(theme.Title, func(r rune) bool {
		return isStatusBarUnsafeRune(r, false)
	}) >= 0 {
		return theme, fmt.Errorf("file icon theme %q title must be 1-%d characters without control or bidirectional override characters", entry.ID, fileIconTitleMaxRunes)
	}
	if len(entry.TitleLocalized) > fileIconLocalesMax {
		return theme, fmt.Errorf("file icon theme %q titleLocalized exceeds %d locales", entry.ID, fileIconLocalesMax)
	}
	if err := validateLocalizedTitles(entry.TitleLocalized); err != nil {
		return theme, fmt.Errorf("file icon theme %q titleLocalized: %w", entry.ID, err)
	}
	if len(entry.TitleLocalized) > 0 {
		theme.TitleLocalized = make(map[string]string, len(entry.TitleLocalized))
		for locale, title := range entry.TitleLocalized {
			title = strings.TrimSpace(title)
			if utf8.RuneCountInString(title) > fileIconTitleMaxRunes || strings.IndexFunc(title, func(r rune) bool {
				return isStatusBarUnsafeRune(r, false)
			}) >= 0 {
				return theme, fmt.Errorf("file icon theme %q titleLocalized.%s must be 1-%d characters without control or bidirectional override characters", entry.ID, locale, fileIconTitleMaxRunes)
			}
			theme.TitleLocalized[strings.TrimSpace(locale)] = title
		}
	}
	if len(entry.IconDefinitions) == 0 || len(entry.IconDefinitions) > fileIconDefinitionsMax {
		return theme, fmt.Errorf("file icon theme %q needs 1-%d iconDefinitions", entry.ID, fileIconDefinitionsMax)
	}
	for defID, definition := range entry.IconDefinitions {
		if !fileIconDefinitionIDPattern.MatchString(defID) {
			return theme, fmt.Errorf("icon definition id %q must match [A-Za-z0-9_.-]{1,64}", defID)
		}
		if strings.TrimSpace(definition.Path) == "" {
			return theme, fmt.Errorf("iconDefinitions.%s.path is required", defID)
		}
	}
	if entry.File == "" {
		return theme, fmt.Errorf("file icon theme %q needs a default \"file\" icon", entry.ID)
	}
	mappings := 0
	dark, err := normalizeFileIconMappings(entry.fileIconMappings, entry.IconDefinitions, "", &mappings)
	if err != nil {
		return theme, err
	}
	theme.File, theme.Folder, theme.FolderExpanded = dark.File, dark.Folder, dark.FolderExpanded
	theme.RootFolder, theme.RootFolderExpanded = dark.RootFolder, dark.RootFolderExpanded
	theme.FileExtensions, theme.FileNames = dark.FileExtensions, dark.FileNames
	theme.FolderNames, theme.FolderNamesExpanded = dark.FolderNames, dark.FolderNamesExpanded
	if entry.Light != nil {
		light, err := normalizeFileIconMappings(*entry.Light, entry.IconDefinitions, "light.", &mappings)
		if err != nil {
			return theme, err
		}
		theme.Light = &light
	}
	return theme, nil
}

// normalizeFileIconMappings validates references and lower-cases mapping
// keys. It counts every default and map entry toward mappingsMax.
func normalizeFileIconMappings(source fileIconMappings, definitions map[string]fileIconDefinition, path string, count *int) (fileIconMappings, error) {
	result := fileIconMappings{}
	defaults := []struct {
		name   string
		value  string
		target *string
	}{
		{"file", source.File, &result.File},
		{"folder", source.Folder, &result.Folder},
		{"folderExpanded", source.FolderExpanded, &result.FolderExpanded},
		{"rootFolder", source.RootFolder, &result.RootFolder},
		{"rootFolderExpanded", source.RootFolderExpanded, &result.RootFolderExpanded},
	}
	for _, field := range defaults {
		if field.value == "" {
			continue
		}
		if _, ok := definitions[field.value]; !ok {
			return result, fmt.Errorf("%s%s references unknown icon definition %q", path, field.name, field.value)
		}
		*field.target = field.value
		*count++
	}
	maps := []struct {
		name      string
		source    map[string]string
		target    *map[string]string
		extension bool
	}{
		{"fileExtensions", source.FileExtensions, &result.FileExtensions, true},
		{"fileNames", source.FileNames, &result.FileNames, false},
		{"folderNames", source.FolderNames, &result.FolderNames, false},
		{"folderNamesExpanded", source.FolderNamesExpanded, &result.FolderNamesExpanded, false},
	}
	for _, field := range maps {
		normalized := make(map[string]string, len(field.source))
		for key, value := range field.source {
			lower := strings.ToLower(key)
			if err := validateFileIconMappingKey(lower, field.extension); err != nil {
				return result, fmt.Errorf("%s%s key %q: %w", path, field.name, key, err)
			}
			if _, ok := definitions[value]; !ok {
				return result, fmt.Errorf("%s%s[%q] references unknown icon definition %q", path, field.name, key, value)
			}
			if _, duplicate := normalized[lower]; duplicate {
				return result, fmt.Errorf("%s%s repeats key %q (keys are case-insensitive)", path, field.name, lower)
			}
			normalized[lower] = value
			*count++
			if *count > fileIconMappingsMax {
				return result, fmt.Errorf("file icon theme exceeds %d mappings", fileIconMappingsMax)
			}
		}
		*field.target = normalized
	}
	return result, nil
}

func validateFileIconMappingKey(key string, extension bool) error {
	if extension {
		if !fileIconExtensionPattern.MatchString(key) {
			return errors.New("extension must match [a-z0-9][a-z0-9._+-]{0,63} without a leading dot")
		}
		return nil
	}
	if key == "" || len(key) > fileIconKeyMaxLength || !utf8.ValidString(key) {
		return fmt.Errorf("name must be 1-%d bytes", fileIconKeyMaxLength)
	}
	if strings.ContainsAny(key, `/\`) || strings.IndexFunc(key, func(r rune) bool { return isStatusBarUnsafeRune(r, false) }) >= 0 {
		return errors.New("name must not contain path separators, control or bidirectional override characters")
	}
	return nil
}

// finalizeFileIconEntries keeps the plugin's snapshot bounded: themes whose
// canonical JSON would push the plugin past snapshotBytesPerPlugin become
// issues.
func finalizeFileIconEntries(_ *pointContext, entries []contributionEntry, issues []ContributionIssue) ([]contributionEntry, []ContributionIssue) {
	total := 0
	kept := make([]contributionEntry, 0, len(entries))
	for _, entry := range entries {
		encoded, err := json.Marshal(entry.Value)
		if err != nil || total+len(encoded) > fileIconSnapshotBytesPerPlug {
			issues = append(issues, ContributionIssue{
				Point: fileIconPointKey, Entry: entry.Index, ID: entry.ID,
				Message: fmt.Sprintf("plugin file icon themes exceed %d snapshot bytes in total", fileIconSnapshotBytesPerPlug),
			})
			continue
		}
		total += len(encoded)
		kept = append(kept, entry)
	}
	return kept, issues
}
