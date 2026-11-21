package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"mincode/app/plugins/api"
)

// contributes.configuration is a process plugin's settings schema. The host
// renders it on Settings > Plugins, stores the values (configuration_store.go)
// and serves them through host.configuration.get (configuration_rpc.go). The
// bounds come from plugins/api/contribution-points.json.
const configurationPointKey = "configuration"

const (
	configurationTypeString  = "string"
	configurationTypeNumber  = "number"
	configurationTypeInteger = "integer"
	configurationTypeBoolean = "boolean"
	configurationTypeArray   = "array"

	configurationScopeGlobal    = "global"
	configurationScopeWorkspace = "workspace"

	configurationTitleMaxRunes       = 200
	configurationDescriptionMaxRunes = 2000
	configurationStringMaxRunes      = 16 * 1024
	configurationArrayMaxItems       = 256
	// Integers stay within the range a JavaScript number represents exactly.
	configurationMaxSafeInteger = 1<<53 - 1
)

var (
	configurationSpec          = mustContributionSpec(configurationPointKey)
	configurationMaxProperties = configurationSpec.MaxProperties
	configurationSchemaMax     = int(configurationSpec.Limit("schemaMaxBytes"))
	configurationKeyMaxLength  = int(configurationSpec.Limit("keyMaxLength"))
	configurationEnumMaxValues = int(configurationSpec.Limit("enumMaxValues"))
	configurationValuesMax     = int(configurationSpec.Limit("valuesMaxBytes"))

	configurationKeyPattern = regexp.MustCompile(`^[a-z][a-zA-Z0-9]*(\.[a-z][a-zA-Z0-9]*){0,3}$`)
)

// ConfigurationSchema is the decoded contributes.configuration object. It is
// also the single snapshot entry the frontend consumer receives.
type ConfigurationSchema struct {
	Title          string                            `json:"title,omitempty"`
	TitleLocalized map[string]string                 `json:"titleLocalized,omitempty"`
	Properties     map[string]*ConfigurationProperty `json:"properties"`
}

// ConfigurationItems describes array elements; only string arrays exist.
type ConfigurationItems struct {
	Type      string   `json:"type"`
	Enum      []string `json:"enum,omitempty"`
	MaxLength int      `json:"maxLength,omitempty"`
}

// ConfigurationProperty is one setting.
type ConfigurationProperty struct {
	Type                 string              `json:"type"`
	Title                string              `json:"title,omitempty"`
	TitleLocalized       map[string]string   `json:"titleLocalized,omitempty"`
	Description          string              `json:"description,omitempty"`
	DescriptionLocalized map[string]string   `json:"descriptionLocalized,omitempty"`
	Default              any                 `json:"default,omitempty"`
	Enum                 []any               `json:"enum,omitempty"`
	EnumLabelsLocalized  map[string][]string `json:"enumLabelsLocalized,omitempty"`
	Format               string              `json:"format,omitempty"`
	Minimum              *float64            `json:"minimum,omitempty"`
	Maximum              *float64            `json:"maximum,omitempty"`
	MaxLength            int                 `json:"maxLength,omitempty"`
	Items                *ConfigurationItems `json:"items,omitempty"`
	MaxItems             int                 `json:"maxItems,omitempty"`
	Secret               bool                `json:"secret,omitempty"`
	Scope                string              `json:"scope,omitempty"`
	Order                int                 `json:"order,omitempty"`
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:         configurationPointKey,
		Decode:      decodeConfigurationSchema,
		IssuePrefix: "invalid configuration contribution",
		IssueText: func(issue ContributionIssue) string {
			return "configuration contribution skipped: " + issue.Message
		},
		EntryNoun: "properties",
	})
}

func decodeConfigurationSchema(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	if len(raw) > configurationSchemaMax {
		return nil, "", fmt.Errorf("configuration schema exceeds %d bytes", configurationSchemaMax)
	}
	var schema ConfigurationSchema
	if err := decodeStrictJSON(raw, &schema, true); err != nil {
		return nil, "", fmt.Errorf("parse configuration schema: %w", err)
	}
	secrets := false
	if ctx != nil && ctx.manifest != nil {
		for _, capability := range ctx.manifest.Capabilities {
			secrets = secrets || capability == api.CapabilitySecrets
		}
	}
	if err := validateConfigurationSchema(&schema, secrets); err != nil {
		return nil, "", err
	}
	return schema, "", nil
}

func validateConfigurationSchema(schema *ConfigurationSchema, secretsGranted bool) error {
	schema.Title = strings.TrimSpace(schema.Title)
	if utf8.RuneCountInString(schema.Title) > configurationTitleMaxRunes {
		return fmt.Errorf("configuration title exceeds %d characters", configurationTitleMaxRunes)
	}
	if err := validateLocalizedTitles(schema.TitleLocalized); err != nil {
		return fmt.Errorf("configuration titleLocalized: %w", err)
	}
	if len(schema.Properties) == 0 {
		return errors.New("configuration must declare at least one property")
	}
	if len(schema.Properties) > configurationMaxProperties {
		return fmt.Errorf("configuration must declare at most %d properties", configurationMaxProperties)
	}
	for key, property := range schema.Properties {
		if len(key) > configurationKeyMaxLength || !configurationKeyPattern.MatchString(key) {
			return fmt.Errorf("configuration key %q must match %s and be at most %d characters", key, configurationKeyPattern, configurationKeyMaxLength)
		}
		if property == nil {
			return fmt.Errorf("configuration property %q must be an object", key)
		}
		if err := validateConfigurationProperty(property, secretsGranted); err != nil {
			return fmt.Errorf("configuration property %q: %w", key, err)
		}
	}
	return nil
}

func validateConfigurationProperty(property *ConfigurationProperty, secretsGranted bool) error {
	property.Title = strings.TrimSpace(property.Title)
	property.Description = strings.TrimSpace(property.Description)
	if utf8.RuneCountInString(property.Title) > configurationTitleMaxRunes {
		return fmt.Errorf("title exceeds %d characters", configurationTitleMaxRunes)
	}
	if utf8.RuneCountInString(property.Description) > configurationDescriptionMaxRunes {
		return fmt.Errorf("description exceeds %d characters", configurationDescriptionMaxRunes)
	}
	if err := validateLocalizedTitles(property.TitleLocalized); err != nil {
		return fmt.Errorf("titleLocalized: %w", err)
	}
	if err := validateConfigurationLocalizedText(property.DescriptionLocalized, configurationDescriptionMaxRunes); err != nil {
		return fmt.Errorf("descriptionLocalized: %w", err)
	}
	switch property.Scope {
	case "":
		property.Scope = configurationScopeGlobal
	case configurationScopeGlobal, configurationScopeWorkspace:
	default:
		return errors.New("scope must be global or workspace")
	}
	scalar := property.Type == configurationTypeString || property.Type == configurationTypeNumber || property.Type == configurationTypeInteger
	switch property.Type {
	case configurationTypeString, configurationTypeNumber, configurationTypeInteger, configurationTypeBoolean, configurationTypeArray:
	default:
		return errors.New("type must be string, number, integer, boolean or array")
	}
	if property.Format != "" && (property.Type != configurationTypeString || (property.Format != "url" && property.Format != "path")) {
		return errors.New("format must be url or path on a string property (pattern is not supported)")
	}
	numeric := property.Type == configurationTypeNumber || property.Type == configurationTypeInteger
	if (property.Minimum != nil || property.Maximum != nil) && !numeric {
		return errors.New("minimum/maximum apply only to number and integer properties")
	}
	for _, bound := range []*float64{property.Minimum, property.Maximum} {
		if bound != nil && (math.IsNaN(*bound) || math.IsInf(*bound, 0)) {
			return errors.New("minimum/maximum must be finite")
		}
	}
	if property.Minimum != nil && property.Maximum != nil && *property.Minimum > *property.Maximum {
		return errors.New("minimum must not exceed maximum")
	}
	if property.MaxLength != 0 && (property.Type != configurationTypeString || property.MaxLength < 1 || property.MaxLength > configurationStringMaxRunes) {
		return fmt.Errorf("maxLength applies to string properties and must be 1-%d", configurationStringMaxRunes)
	}
	if property.Type == configurationTypeArray {
		if property.Items == nil {
			property.Items = &ConfigurationItems{Type: configurationTypeString}
		}
		if property.Items.Type != configurationTypeString {
			return errors.New("array items must be of type string")
		}
		if property.Items.MaxLength < 0 || property.Items.MaxLength > configurationStringMaxRunes {
			return fmt.Errorf("items.maxLength must be 1-%d", configurationStringMaxRunes)
		}
		if len(property.Items.Enum) > configurationEnumMaxValues {
			return fmt.Errorf("items.enum must list at most %d values", configurationEnumMaxValues)
		}
		seen := map[string]bool{}
		for _, value := range property.Items.Enum {
			if seen[value] {
				return fmt.Errorf("items.enum repeats %q", value)
			}
			seen[value] = true
		}
		if property.MaxItems < 0 || property.MaxItems > configurationArrayMaxItems {
			return fmt.Errorf("maxItems must be 1-%d", configurationArrayMaxItems)
		}
	} else if property.Items != nil || property.MaxItems != 0 {
		return errors.New("items/maxItems apply only to array properties")
	}
	if len(property.Enum) > 0 || property.Enum != nil {
		if !scalar {
			return errors.New("enum applies only to string, number and integer properties")
		}
		if len(property.Enum) == 0 || len(property.Enum) > configurationEnumMaxValues {
			return fmt.Errorf("enum must list 1-%d values", configurationEnumMaxValues)
		}
		seen := map[string]bool{}
		for _, value := range property.Enum {
			if err := validateConfigurationScalar(property, value); err != nil {
				return fmt.Errorf("enum value %v: %w", value, err)
			}
			encoded, _ := json.Marshal(value)
			if seen[string(encoded)] {
				return fmt.Errorf("enum repeats %s", encoded)
			}
			seen[string(encoded)] = true
		}
	}
	if property.EnumLabelsLocalized != nil {
		if len(property.Enum) == 0 {
			return errors.New("enumLabelsLocalized requires enum")
		}
		if len(property.EnumLabelsLocalized) > 16 {
			return errors.New("enumLabelsLocalized exceeds 16 locales")
		}
		for locale, labels := range property.EnumLabelsLocalized {
			if strings.TrimSpace(locale) == "" || len(locale) > 32 || len(labels) != len(property.Enum) {
				return errors.New("enumLabelsLocalized needs one label per enum value for each locale")
			}
			for _, label := range labels {
				if strings.TrimSpace(label) == "" || utf8.RuneCountInString(label) > configurationTitleMaxRunes {
					return fmt.Errorf("enum labels must be 1-%d characters", configurationTitleMaxRunes)
				}
			}
		}
	}
	if property.Secret {
		if property.Type != configurationTypeString {
			return errors.New("secret properties must be of type string")
		}
		if property.Default != nil || property.Enum != nil {
			return errors.New("secret properties cannot declare default or enum")
		}
		if property.Scope != configurationScopeGlobal {
			return errors.New("secret properties are global only")
		}
		if !secretsGranted {
			return errors.New("secret properties require the secrets capability")
		}
	}
	if property.Default != nil {
		if err := validateConfigurationValue(property, property.Default); err != nil {
			return fmt.Errorf("default: %w", err)
		}
	}
	return nil
}

func validateConfigurationLocalizedText(text map[string]string, maxRunes int) error {
	if len(text) > 16 {
		return errors.New("exceeds 16 locales")
	}
	for locale, value := range text {
		if strings.TrimSpace(locale) == "" || len(locale) > 32 || strings.TrimSpace(value) == "" || utf8.RuneCountInString(value) > maxRunes {
			return errors.New("localized text is invalid")
		}
	}
	return nil
}

// validateConfigurationValue checks a decoded JSON value (json.Unmarshal into
// any) against its property. It is used for defaults, stored values and writes.
func validateConfigurationValue(property *ConfigurationProperty, value any) error {
	switch property.Type {
	case configurationTypeBoolean:
		if _, ok := value.(bool); !ok {
			return errors.New("expected a boolean")
		}
		return nil
	case configurationTypeArray:
		list, ok := value.([]any)
		if !ok {
			return errors.New("expected a list of strings")
		}
		maxItems := property.MaxItems
		if maxItems == 0 {
			maxItems = configurationArrayMaxItems
		}
		if len(list) > maxItems {
			return fmt.Errorf("at most %d items", maxItems)
		}
		items := property.Items
		if items == nil {
			items = &ConfigurationItems{Type: configurationTypeString}
		}
		maxLength := items.MaxLength
		if maxLength == 0 {
			maxLength = configurationStringMaxRunes
		}
		for _, item := range list {
			text, ok := item.(string)
			if !ok {
				return errors.New("expected a list of strings")
			}
			if utf8.RuneCountInString(text) > maxLength {
				return fmt.Errorf("items must be at most %d characters", maxLength)
			}
			if len(items.Enum) > 0 && !configurationContainsString(items.Enum, text) {
				return fmt.Errorf("%q is not an allowed value", text)
			}
		}
		return nil
	}
	if err := validateConfigurationScalar(property, value); err != nil {
		return err
	}
	if len(property.Enum) > 0 {
		encoded, _ := json.Marshal(value)
		for _, allowed := range property.Enum {
			candidate, _ := json.Marshal(allowed)
			if string(candidate) == string(encoded) {
				return nil
			}
		}
		return errors.New("value is not one of the allowed values")
	}
	return nil
}

// validateConfigurationScalar checks type and bounds for string/number/integer.
func validateConfigurationScalar(property *ConfigurationProperty, value any) error {
	switch property.Type {
	case configurationTypeString:
		text, ok := value.(string)
		if !ok {
			return errors.New("expected a string")
		}
		maxLength := property.MaxLength
		if maxLength == 0 {
			maxLength = configurationStringMaxRunes
		}
		if !utf8.ValidString(text) || utf8.RuneCountInString(text) > maxLength {
			return fmt.Errorf("must be valid text of at most %d characters", maxLength)
		}
		switch property.Format {
		case "url":
			if text != "" {
				parsed, err := url.Parse(text)
				if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
					return errors.New("must be an absolute http(s) URL without credentials")
				}
			}
		case "path":
			for _, r := range text {
				if r < 0x20 || r == 0x7f {
					return errors.New("path must not contain control characters")
				}
			}
		}
		return nil
	case configurationTypeNumber, configurationTypeInteger:
		number, ok := value.(float64)
		if !ok || math.IsNaN(number) || math.IsInf(number, 0) {
			return errors.New("expected a number")
		}
		if property.Type == configurationTypeInteger && (number != math.Trunc(number) || math.Abs(number) > configurationMaxSafeInteger) {
			return errors.New("expected an integer")
		}
		if property.Minimum != nil && number < *property.Minimum {
			return fmt.Errorf("must be at least %v", *property.Minimum)
		}
		if property.Maximum != nil && number > *property.Maximum {
			return fmt.Errorf("must be at most %v", *property.Maximum)
		}
		return nil
	}
	return fmt.Errorf("type %s has no scalar value", property.Type)
}

func configurationContainsString(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// configurationSchemaOf returns a manifest's validated schema, or nil.
func configurationSchemaOf(manifest *Manifest) *ConfigurationSchema {
	if manifest == nil {
		return nil
	}
	resolved := manifest.contribution(configurationPointKey)
	if resolved == nil || len(resolved.Entries) != 1 {
		return nil
	}
	schema, ok := resolved.Entries[0].(ConfigurationSchema)
	if !ok {
		return nil
	}
	return &schema
}

// sortedKeys lists schema keys in a stable order.
func (schema *ConfigurationSchema) sortedKeys() []string {
	keys := make([]string, 0, len(schema.Properties))
	for key := range schema.Properties {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
