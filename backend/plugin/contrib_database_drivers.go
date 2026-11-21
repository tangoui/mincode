package plugin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"mincode/app/plugins/api/svgsafe"
)

// Database drivers (contributes.databaseDrivers) let an API v2 process plugin
// that declares the database.driver capability act as a database driver for
// saved connections the user explicitly binds to it. An entry only describes
// the driver; the host connects through database_drivers_proxy.go, releases
// credentials only under a grant (database_driver_grants.go) and speaks the
// plugin.db.* protocol (docs/architecture/database-driver-protocol.md).
//
// Mirrors: tools/mincode-cli/database_drivers.go (CLI) and
// assertDatabaseDriversEntry (cloud).
const (
	databaseDriverPointKey  = "databaseDrivers"
	databaseDriverAssetHint = "icon"

	databaseDriverOptionString  = "string"
	databaseDriverOptionInteger = "integer"
	databaseDriverOptionNumber  = "number"
	databaseDriverOptionBoolean = "boolean"
)

var (
	databaseDriverSpec                = mustContributionSpec(databaseDriverPointKey)
	databaseDriverTitleMaxRunes       = int(databaseDriverSpec.Limit("titleMaxRunes"))
	databaseDriverDescriptionMaxRunes = int(databaseDriverSpec.Limit("descriptionMaxRunes"))
	databaseDriverLocalesMax          = int(databaseDriverSpec.Limit("localesMax"))
	databaseDriverOptionsMax          = int(databaseDriverSpec.Limit("optionsMax"))
	databaseDriverOptionStringMax     = int(databaseDriverSpec.Limit("optionStringMaxLength"))
	databaseDriverEnumMax             = int(databaseDriverSpec.Limit("enumMax"))
	databaseDriverFileExtensionsMax   = int(databaseDriverSpec.Limit("fileExtensionsMax"))
	databaseDriverConnectionFields    = databaseDriverSpec.List("connectionFields")
	databaseDriverSecretFields        = stringSet(databaseDriverSpec.List("secretFields"))
	databaseDriverFeatures            = stringSet(databaseDriverSpec.List("features"))
	databaseDriverDialects            = stringSet(databaseDriverSpec.List("sqlDialects"))
	databaseDriverCategories          = stringSet(databaseDriverSpec.List("categories"))
	databaseDriverReplaceableTypes    = stringSet(databaseDriverSpec.List("replaceableTypes"))
	databaseDriverImageLimits         = contributionImageLimits{
		PNGMaxDimension: int(databaseDriverSpec.Limit("pngMaxDimension")),
		SVG: svgsafe.Limits{
			MaxBytes:     int(databaseDriverSpec.Assets.MaxBytes),
			MaxElements:  int(databaseDriverSpec.Limit("svgMaxElements")),
			MaxDepth:     int(databaseDriverSpec.Limit("svgMaxDepth")),
			MaxAttrBytes: int(databaseDriverSpec.Limit("svgMaxAttrBytes")),
			MaxUse:       int(databaseDriverSpec.Limit("svgMaxUse")),
			MaxDimension: int(databaseDriverSpec.Limit("svgMaxDimension")),
		},
	}

	databaseDriverIDPattern        = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	databaseDriverOptionKeyPattern = regexp.MustCompile(`^[a-z][a-zA-Z0-9]{0,63}$`)
	databaseDriverExtensionPattern = regexp.MustCompile(`^\.[a-z0-9]{1,16}$`)
)

// DatabaseDriverOption is one validated driver connection option. Options
// are never secrets in protocol v1.
type DatabaseDriverOption struct {
	Type                 string              `json:"type"`
	Title                string              `json:"title,omitempty"`
	TitleLocalized       map[string]string   `json:"titleLocalized,omitempty"`
	Description          string              `json:"description,omitempty"`
	DescriptionLocalized map[string]string   `json:"descriptionLocalized,omitempty"`
	EnumLabelsLocalized  map[string][]string `json:"enumLabelsLocalized,omitempty"`
	Default              any                 `json:"default,omitempty"`
	Enum                 []any               `json:"enum,omitempty"`
	Minimum              *float64            `json:"minimum,omitempty"`
	Maximum              *float64            `json:"maximum,omitempty"`
	MaxLength            int                 `json:"maxLength,omitempty"`
}

// DatabaseDriverConnection is the connection dialog model of a driver.
type DatabaseDriverConnection struct {
	Fields         []string                        `json:"fields"`
	Required       []string                        `json:"required"`
	DefaultPort    int                             `json:"defaultPort,omitempty"`
	FileExtensions []string                        `json:"fileExtensions,omitempty"`
	Options        map[string]DatabaseDriverOption `json:"options,omitempty"`
}

// DatabaseDriverContribution is one validated contributes.databaseDrivers
// entry. Icon is the content-hash key of the canonical icon asset.
// SpecFingerprint is sha256 of the canonical entry (without itself) so the
// frontend and grants can notice a changed driver declaration.
type DatabaseDriverContribution struct {
	ID             string            `json:"id"`
	Title          string            `json:"title"`
	TitleLocalized map[string]string `json:"titleLocalized,omitempty"`
	Description    string            `json:"description,omitempty"`
	// DescriptionLocalized maps a locale to the translated description.
	DescriptionLocalized map[string]string        `json:"descriptionLocalized,omitempty"`
	Icon                 string                   `json:"icon,omitempty"`
	Category             string                   `json:"category"`
	SQLDialect           string                   `json:"sqlDialect"`
	Replaces             string                   `json:"replaces,omitempty"`
	Connection           DatabaseDriverConnection `json:"connection"`
	Features             []string                 `json:"features"`
	SpecFingerprint      string                   `json:"specFingerprint"`
}

type databaseDriverOptionManifest struct {
	Type           string            `json:"type"`
	Title          string            `json:"title"`
	TitleLocalized map[string]string `json:"titleLocalized"`
	Description    string            `json:"description"`
	// DescriptionLocalized and EnumLabelsLocalized translate the hint and the
	// enum values shown in the connection dialog (one label per enum value).
	DescriptionLocalized map[string]string   `json:"descriptionLocalized"`
	EnumLabelsLocalized  map[string][]string `json:"enumLabelsLocalized"`
	Default              json.RawMessage     `json:"default"`
	Enum                 []json.RawMessage   `json:"enum"`
	Minimum              *float64            `json:"minimum"`
	Maximum              *float64            `json:"maximum"`
	MaxLength            *int                `json:"maxLength"`
	// Declared only to give precise errors; they are always rejected.
	Secret  json.RawMessage `json:"secret"`
	Pattern json.RawMessage `json:"pattern"`
	Format  json.RawMessage `json:"format"`
}

type databaseDriverConnectionManifest struct {
	Fields         []string                                `json:"fields"`
	Required       []string                                `json:"required"`
	DefaultPort    *int                                    `json:"defaultPort"`
	FileExtensions []string                                `json:"fileExtensions"`
	Options        map[string]databaseDriverOptionManifest `json:"options"`
}

type databaseDriverManifestEntry struct {
	ID             string            `json:"id"`
	Title          string            `json:"title"`
	TitleLocalized map[string]string `json:"titleLocalized"`
	Description    string            `json:"description"`
	// DescriptionLocalized maps a locale to the translated description.
	DescriptionLocalized map[string]string                 `json:"descriptionLocalized"`
	Icon                 string                            `json:"icon"`
	Category             string                            `json:"category"`
	SQLDialect           string                            `json:"sqlDialect"`
	Replaces             string                            `json:"replaces"`
	Connection           *databaseDriverConnectionManifest `json:"connection"`
	Features             []string                          `json:"features"`
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:              databaseDriverPointKey,
		Decode:           decodeDatabaseDriverEntry,
		Finalize:         finalizeDatabaseDriverEntries,
		DuplicateMessage: func(id string) string { return fmt.Sprintf("database driver id %q is duplicated", id) },
		IssuePrefix:      "invalid databaseDrivers contribution",
		IssueText: func(issue ContributionIssue) string {
			if issue.ID != "" {
				return fmt.Sprintf("skipped database driver %q: %s", issue.ID, issue.Message)
			}
			return fmt.Sprintf("skipped contributes.databaseDrivers[%d]: %s", issue.Entry, issue.Message)
		},
		EntryNoun:         "database drivers",
		ExpandsPermission: databaseDriversExpandPermission,
		ValidateAsset: func(_ string, ext string, data []byte) ([]byte, string, error) {
			return validateContributionImage(ext, data, databaseDriverImageLimits)
		},
	})
}

func decodeDatabaseDriverEntry(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var entry databaseDriverManifestEntry
	if err := decodeStrictJSON(raw, &entry, true); err != nil {
		return nil, "", fmt.Errorf("parse database driver: %w", err)
	}
	id := strings.TrimSpace(entry.ID)
	if !databaseDriverIDPattern.MatchString(id) {
		return nil, id, fmt.Errorf("database driver id %q must match ^[a-z][a-z0-9-]{0,31}$", id)
	}
	driver := DatabaseDriverContribution{ID: id}
	var err error
	if driver.Title, err = validateContributionLine(entry.Title, databaseDriverTitleMaxRunes, true); err != nil {
		return nil, id, fmt.Errorf("database driver %q title %w", id, err)
	}
	if driver.TitleLocalized, err = validateContributionLocalized(entry.TitleLocalized, databaseDriverTitleMaxRunes, databaseDriverLocalesMax); err != nil {
		return nil, id, fmt.Errorf("database driver %q titleLocalized %w", id, err)
	}
	if driver.Description, err = validateContributionLine(entry.Description, databaseDriverDescriptionMaxRunes, false); err != nil {
		return nil, id, fmt.Errorf("database driver %q description %w", id, err)
	}
	if driver.DescriptionLocalized, err = validateContributionLocalized(entry.DescriptionLocalized, databaseDriverDescriptionMaxRunes, databaseDriverLocalesMax); err != nil {
		return nil, id, fmt.Errorf("database driver %q descriptionLocalized %w", id, err)
	}
	if icon := strings.TrimSpace(entry.Icon); icon != "" {
		key, err := ctx.readAsset(icon, databaseDriverAssetHint)
		if err != nil {
			return nil, id, fmt.Errorf("database driver %q icon %w", id, err)
		}
		driver.Icon = key
	}
	driver.Category = strings.TrimSpace(entry.Category)
	if _, ok := databaseDriverCategories[driver.Category]; !ok {
		return nil, id, fmt.Errorf("database driver %q category %q must be one of %s", id, driver.Category, strings.Join(databaseDriverSpec.List("categories"), ", "))
	}
	driver.SQLDialect = strings.TrimSpace(entry.SQLDialect)
	if driver.SQLDialect == "" {
		driver.SQLDialect = "generic"
	}
	if _, ok := databaseDriverDialects[driver.SQLDialect]; !ok {
		return nil, id, fmt.Errorf("database driver %q sqlDialect %q must be one of %s", id, driver.SQLDialect, strings.Join(databaseDriverSpec.List("sqlDialects"), ", "))
	}
	if replaces := strings.TrimSpace(entry.Replaces); replaces != "" {
		if _, ok := databaseDriverReplaceableTypes[replaces]; !ok {
			return nil, id, fmt.Errorf("database driver %q replaces %q must be one of %s", id, replaces, strings.Join(databaseDriverSpec.List("replaceableTypes"), ", "))
		}
		driver.Replaces = replaces
	}
	if entry.Connection == nil {
		return nil, id, fmt.Errorf("database driver %q connection is required", id)
	}
	if driver.Connection, err = decodeDatabaseDriverConnection(*entry.Connection); err != nil {
		return nil, id, fmt.Errorf("database driver %q connection %w", id, err)
	}
	if driver.Features, err = decodeDatabaseDriverFeatures(entry.Features); err != nil {
		return nil, id, fmt.Errorf("database driver %q %w", id, err)
	}
	driver.SpecFingerprint = databaseDriverSpecFingerprint(driver)
	return driver, id, nil
}

func decodeDatabaseDriverConnection(entry databaseDriverConnectionManifest) (DatabaseDriverConnection, error) {
	connection := DatabaseDriverConnection{Fields: []string{}, Required: []string{}}
	if len(entry.Fields) == 0 {
		return connection, errors.New("fields must list at least one connection field")
	}
	declared := map[string]bool{}
	for _, field := range entry.Fields {
		if !slices.Contains(databaseDriverConnectionFields, field) {
			return connection, fmt.Errorf("field %q must be one of %s", field, strings.Join(databaseDriverConnectionFields, ", "))
		}
		if declared[field] {
			return connection, fmt.Errorf("field %q is listed more than once", field)
		}
		declared[field] = true
	}
	// Canonical contract order keeps fingerprints and dialogs stable.
	for _, field := range databaseDriverConnectionFields {
		if declared[field] {
			connection.Fields = append(connection.Fields, field)
		}
	}
	required := map[string]bool{}
	for _, field := range entry.Required {
		if !declared[field] {
			return connection, fmt.Errorf("required field %q is not in fields", field)
		}
		if required[field] {
			return connection, fmt.Errorf("required field %q is listed more than once", field)
		}
		required[field] = true
	}
	for _, field := range databaseDriverConnectionFields {
		if required[field] {
			connection.Required = append(connection.Required, field)
		}
	}
	if entry.DefaultPort != nil {
		if !declared["port"] {
			return connection, errors.New("defaultPort requires the port field")
		}
		if *entry.DefaultPort < 1 || *entry.DefaultPort > 65535 {
			return connection, errors.New("defaultPort must be 1-65535")
		}
		connection.DefaultPort = *entry.DefaultPort
	}
	if entry.FileExtensions != nil {
		if !declared["filePath"] {
			return connection, errors.New("fileExtensions requires the filePath field")
		}
		if len(entry.FileExtensions) == 0 || len(entry.FileExtensions) > databaseDriverFileExtensionsMax {
			return connection, fmt.Errorf("fileExtensions must list 1-%d extensions", databaseDriverFileExtensionsMax)
		}
		seen := map[string]bool{}
		for _, extension := range entry.FileExtensions {
			if !databaseDriverExtensionPattern.MatchString(extension) {
				return connection, fmt.Errorf("fileExtension %q must match ^\\.[a-z0-9]{1,16}$", extension)
			}
			if seen[extension] {
				return connection, fmt.Errorf("fileExtension %q is listed more than once", extension)
			}
			seen[extension] = true
			connection.FileExtensions = append(connection.FileExtensions, extension)
		}
	}
	if entry.Options != nil {
		if len(entry.Options) > databaseDriverOptionsMax {
			return connection, fmt.Errorf("options must have at most %d keys", databaseDriverOptionsMax)
		}
		if len(entry.Options) > 0 {
			connection.Options = make(map[string]DatabaseDriverOption, len(entry.Options))
		}
		keys := make([]string, 0, len(entry.Options))
		for key := range entry.Options {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if !databaseDriverOptionKeyPattern.MatchString(key) {
				return connection, fmt.Errorf("option key %q must match ^[a-z][a-zA-Z0-9]{0,63}$", key)
			}
			if slices.Contains(databaseDriverConnectionFields, key) {
				return connection, fmt.Errorf("option key %q collides with a connection field", key)
			}
			option, err := decodeDatabaseDriverOption(entry.Options[key])
			if err != nil {
				return connection, fmt.Errorf("option %q %w", key, err)
			}
			connection.Options[key] = option
		}
	}
	return connection, nil
}

func decodeDatabaseDriverOption(entry databaseDriverOptionManifest) (DatabaseDriverOption, error) {
	option := DatabaseDriverOption{Type: entry.Type}
	if entry.Secret != nil {
		return option, errors.New("cannot be secret: driver options never hold credentials in protocol v1 (use the password or token connection field)")
	}
	if entry.Pattern != nil || entry.Format != nil {
		return option, errors.New("pattern and format are not supported")
	}
	switch entry.Type {
	case databaseDriverOptionString, databaseDriverOptionInteger, databaseDriverOptionNumber, databaseDriverOptionBoolean:
	default:
		return option, errors.New("type must be string, integer, number or boolean")
	}
	var err error
	if option.Title, err = validateContributionLine(entry.Title, databaseDriverTitleMaxRunes, false); err != nil {
		return option, fmt.Errorf("title %w", err)
	}
	if option.TitleLocalized, err = validateContributionLocalized(entry.TitleLocalized, databaseDriverTitleMaxRunes, databaseDriverLocalesMax); err != nil {
		return option, fmt.Errorf("titleLocalized %w", err)
	}
	if option.Description, err = validateContributionLine(entry.Description, databaseDriverDescriptionMaxRunes, false); err != nil {
		return option, fmt.Errorf("description %w", err)
	}
	if option.DescriptionLocalized, err = validateContributionLocalized(entry.DescriptionLocalized, databaseDriverDescriptionMaxRunes, databaseDriverLocalesMax); err != nil {
		return option, fmt.Errorf("descriptionLocalized %w", err)
	}
	numeric := entry.Type == databaseDriverOptionInteger || entry.Type == databaseDriverOptionNumber
	if (entry.Minimum != nil || entry.Maximum != nil) && !numeric {
		return option, errors.New("minimum/maximum apply only to integer and number options")
	}
	for _, bound := range []*float64{entry.Minimum, entry.Maximum} {
		if bound == nil {
			continue
		}
		if math.IsNaN(*bound) || math.IsInf(*bound, 0) {
			return option, errors.New("minimum/maximum must be finite")
		}
		if entry.Type == databaseDriverOptionInteger && *bound != math.Trunc(*bound) {
			return option, errors.New("minimum/maximum of an integer option must be integers")
		}
	}
	if entry.Minimum != nil && entry.Maximum != nil && *entry.Minimum > *entry.Maximum {
		return option, errors.New("minimum must not exceed maximum")
	}
	option.Minimum, option.Maximum = entry.Minimum, entry.Maximum
	if entry.MaxLength != nil {
		if entry.Type != databaseDriverOptionString || *entry.MaxLength < 1 || *entry.MaxLength > databaseDriverOptionStringMax {
			return option, fmt.Errorf("maxLength applies to string options and must be 1-%d", databaseDriverOptionStringMax)
		}
		option.MaxLength = *entry.MaxLength
	}
	if entry.Enum != nil {
		if entry.Type == databaseDriverOptionBoolean {
			return option, errors.New("enum applies only to string, integer and number options")
		}
		if len(entry.Enum) == 0 || len(entry.Enum) > databaseDriverEnumMax {
			return option, fmt.Errorf("enum must list 1-%d values", databaseDriverEnumMax)
		}
		seen := map[string]bool{}
		for _, raw := range entry.Enum {
			value, err := databaseDriverOptionLiteral(option, raw, false)
			if err != nil {
				return option, fmt.Errorf("enum value %w", err)
			}
			canonical, _ := json.Marshal(value)
			if seen[string(canonical)] {
				return option, fmt.Errorf("enum repeats %s", canonical)
			}
			seen[string(canonical)] = true
			option.Enum = append(option.Enum, value)
		}
	}
	if entry.EnumLabelsLocalized != nil {
		if option.Enum == nil {
			return option, errors.New("enumLabelsLocalized requires enum")
		}
		if option.EnumLabelsLocalized, err = validateDatabaseDriverEnumLabels(entry.EnumLabelsLocalized, len(option.Enum)); err != nil {
			return option, fmt.Errorf("enumLabelsLocalized %w", err)
		}
	}
	if len(bytes.TrimSpace(entry.Default)) > 0 {
		value, err := databaseDriverOptionLiteral(option, entry.Default, true)
		if err != nil {
			return option, fmt.Errorf("default %w", err)
		}
		option.Default = value
	}
	return option, nil
}

// databaseDriverOptionLiteral decodes one manifest literal for option.
// Enum membership is checked only when checkEnum is set (defaults).
func databaseDriverOptionLiteral(option DatabaseDriverOption, raw json.RawMessage, checkEnum bool) (any, error) {
	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("is not valid JSON")
	}
	value, err := validateDatabaseDriverOptionValue(option, decoded, checkEnum)
	if err != nil {
		return nil, err
	}
	return value, nil
}

// validateDatabaseDriverOptionValue checks one option value (manifest
// literal or connection option) against its declaration and returns the
// normalized value (json.Number and float64 become float64/int64).
func validateDatabaseDriverOptionValue(option DatabaseDriverOption, value any, checkEnum bool) (any, error) {
	var normalized any
	switch option.Type {
	case databaseDriverOptionString:
		text, ok := value.(string)
		if !ok {
			return nil, errors.New("must be a string")
		}
		limit := databaseDriverOptionStringMax
		if option.MaxLength > 0 {
			limit = option.MaxLength
		}
		if !utf8.ValidString(text) || utf8.RuneCountInString(text) > limit {
			return nil, fmt.Errorf("must be valid UTF-8 of at most %d characters", limit)
		}
		if err := checkContributionRunes(text, false, false); err != nil {
			return nil, err
		}
		normalized = text
	case databaseDriverOptionBoolean:
		flag, ok := value.(bool)
		if !ok {
			return nil, errors.New("must be a boolean")
		}
		normalized = flag
	case databaseDriverOptionInteger, databaseDriverOptionNumber:
		var number float64
		switch typed := value.(type) {
		case json.Number:
			parsed, err := typed.Float64()
			if err != nil {
				return nil, errors.New("must be a finite number")
			}
			number = parsed
		case float64:
			number = typed
		case int:
			number = float64(typed)
		case int64:
			number = float64(typed)
		default:
			return nil, errors.New("must be a number")
		}
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return nil, errors.New("must be a finite number")
		}
		if option.Type == databaseDriverOptionInteger {
			if number != math.Trunc(number) || math.Abs(number) > 1<<53 {
				return nil, errors.New("must be an integer within ±2^53")
			}
			normalized = int64(number)
		} else {
			normalized = number
		}
		if option.Minimum != nil && number < *option.Minimum {
			return nil, fmt.Errorf("must be at least %v", *option.Minimum)
		}
		if option.Maximum != nil && number > *option.Maximum {
			return nil, fmt.Errorf("must be at most %v", *option.Maximum)
		}
	default:
		return nil, fmt.Errorf("has unknown type %q", option.Type)
	}
	if checkEnum && len(option.Enum) > 0 {
		for _, allowed := range option.Enum {
			if databaseDriverOptionEqual(allowed, normalized) {
				return normalized, nil
			}
		}
		return nil, errors.New("must be one of the enum values")
	}
	return normalized, nil
}

func databaseDriverOptionEqual(left, right any) bool {
	toFloat := func(value any) (float64, bool) {
		switch typed := value.(type) {
		case int64:
			return float64(typed), true
		case float64:
			return typed, true
		}
		return 0, false
	}
	if a, ok := toFloat(left); ok {
		b, ok := toFloat(right)
		return ok && a == b
	}
	return left == right
}

func decodeDatabaseDriverFeatures(values []string) ([]string, error) {
	features := []string{}
	seen := map[string]bool{}
	for _, feature := range values {
		if _, ok := databaseDriverFeatures[feature]; !ok {
			return nil, fmt.Errorf("feature %q must be one of %s", feature, strings.Join(databaseDriverSpec.List("features"), ", "))
		}
		if seen[feature] {
			return nil, fmt.Errorf("feature %q is listed more than once", feature)
		}
		seen[feature] = true
	}
	for _, feature := range databaseDriverSpec.List("features") {
		if seen[feature] {
			features = append(features, feature)
		}
	}
	return features, nil
}

// finalizeDatabaseDriverEntries applies the feature dependencies and the
// cross-entry rule that one plugin replaces a built-in type at most once.
func finalizeDatabaseDriverEntries(_ *pointContext, entries []contributionEntry, issues []ContributionIssue) ([]contributionEntry, []ContributionIssue) {
	kept := entries[:0]
	replaced := map[string]string{}
	for _, entry := range entries {
		driver, ok := entry.Value.(DatabaseDriverContribution)
		if !ok {
			continue
		}
		if err := databaseDriverFeatureDependencies(driver); err != nil {
			issues = append(issues, ContributionIssue{Point: databaseDriverPointKey, Entry: entry.Index, ID: driver.ID, Message: err.Error()})
			continue
		}
		if driver.Replaces != "" {
			if other, exists := replaced[driver.Replaces]; exists {
				issues = append(issues, ContributionIssue{Point: databaseDriverPointKey, Entry: entry.Index, ID: driver.ID,
					Message: fmt.Sprintf("database driver %q replaces %q, which driver %q already replaces", driver.ID, driver.Replaces, other)})
				continue
			}
			replaced[driver.Replaces] = driver.ID
		}
		kept = append(kept, entry)
	}
	return kept, issues
}

func databaseDriverFeatureDependencies(driver DatabaseDriverContribution) error {
	has := func(feature string) bool { return slices.Contains(driver.Features, feature) }
	for _, feature := range driver.Features {
		switch {
		case strings.HasPrefix(feature, "rows."):
			if !has("metadata.primaryKeys") || !has("metadata.columns") {
				return fmt.Errorf("database driver %q feature %s requires metadata.primaryKeys and metadata.columns", driver.ID, feature)
			}
		case strings.HasPrefix(feature, "destructive."):
			if !has("metadata.tables") {
				return fmt.Errorf("database driver %q feature %s requires metadata.tables", driver.ID, feature)
			}
		}
	}
	return nil
}

// databaseDriversExpandPermission reports a development generation that
// gives credentials or write paths to new code: a new driver id, a changed
// replaces target, or a newly added write feature (databaseDriverWriteFeature).
func databaseDriversExpandPermission(current, candidate *resolvedContribution) bool {
	if candidate == nil {
		return false
	}
	known := map[string]DatabaseDriverContribution{}
	if current != nil {
		for _, entry := range current.Entries {
			if driver, ok := entry.(DatabaseDriverContribution); ok {
				known[driver.ID] = driver
			}
		}
	}
	for _, entry := range candidate.Entries {
		driver, ok := entry.(DatabaseDriverContribution)
		if !ok {
			continue
		}
		previous, exists := known[driver.ID]
		if !exists || previous.Replaces != driver.Replaces {
			return true
		}
		for _, feature := range driver.Features {
			if databaseDriverWriteFeature(feature) && !slices.Contains(previous.Features, feature) {
				return true
			}
		}
	}
	return false
}

// databaseDriverWriteFeature reports a feature that lets the host write
// through the driver beyond execute: rows.*, destructive.* and
// transaction.snapshotWrite.
func databaseDriverWriteFeature(feature string) bool {
	return strings.HasPrefix(feature, "rows.") || strings.HasPrefix(feature, "destructive.") || feature == "transaction.snapshotWrite"
}

func databaseDriverSpecFingerprint(driver DatabaseDriverContribution) string {
	driver.SpecFingerprint = ""
	canonical, _ := json.Marshal(driver)
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

// databaseDriverFieldSecret reports whether a connection field carries a
// credential (contract list secretFields).
func databaseDriverFieldSecret(field string) bool {
	_, ok := databaseDriverSecretFields[field]
	return ok
}

// validateDatabaseDriverEnumLabels checks the translated enum labels: each
// locale lists exactly one label per enum value, in enum order.
func validateDatabaseDriverEnumLabels(values map[string][]string, enumLen int) (map[string][]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	if len(values) > databaseDriverLocalesMax {
		return nil, fmt.Errorf("must have at most %d locales", databaseDriverLocalesMax)
	}
	result := make(map[string][]string, len(values))
	for locale, labels := range values {
		if strings.TrimSpace(locale) == "" || len(locale) > 32 || !utf8.ValidString(locale) || checkContributionRunes(locale, false, false) != nil {
			return nil, fmt.Errorf("locale %q is invalid", locale)
		}
		if len(labels) != enumLen {
			return nil, fmt.Errorf("locale %q must list %d labels, one per enum value", locale, enumLen)
		}
		normalized := make([]string, len(labels))
		for i, label := range labels {
			value, err := validateContributionLine(label, databaseDriverTitleMaxRunes, true)
			if err != nil {
				return nil, fmt.Errorf("locale %q label %d %w", locale, i, err)
			}
			normalized[i] = value
		}
		result[locale] = normalized
	}
	return result, nil
}

func cloneDatabaseDriverContribution(driver DatabaseDriverContribution) DatabaseDriverContribution {
	driver.TitleLocalized = cloneStringMap(driver.TitleLocalized)
	driver.DescriptionLocalized = cloneStringMap(driver.DescriptionLocalized)
	// Lists without omitempty must stay arrays: the frontend rejects a null list.
	driver.Features = append([]string{}, driver.Features...)
	driver.Connection.Fields = append([]string{}, driver.Connection.Fields...)
	driver.Connection.Required = append([]string{}, driver.Connection.Required...)
	driver.Connection.FileExtensions = append([]string(nil), driver.Connection.FileExtensions...)
	if driver.Connection.Options != nil {
		options := make(map[string]DatabaseDriverOption, len(driver.Connection.Options))
		for key, option := range driver.Connection.Options {
			option.TitleLocalized = cloneStringMap(option.TitleLocalized)
			option.DescriptionLocalized = cloneStringMap(option.DescriptionLocalized)
			if option.EnumLabelsLocalized != nil {
				labels := make(map[string][]string, len(option.EnumLabelsLocalized))
				for locale, list := range option.EnumLabelsLocalized {
					labels[locale] = append([]string(nil), list...)
				}
				option.EnumLabelsLocalized = labels
			}
			option.Enum = append([]any(nil), option.Enum...)
			options[key] = option
		}
		driver.Connection.Options = options
	}
	return driver
}
