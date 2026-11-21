package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

// AI providers (contributes.aiProviders) add presets to the provider list in
// Settings → AI → Connection. They are pure data: a base URL, an API format
// and model names the host already supports. The plugin never sees the API
// key the user types, never learns which preset was chosen and never sends a
// request; the host's own AI client talks to the provider. A saved profile is
// ordinary user configuration and stays when the plugin is removed.
//
// The base URL, sign-up and docs links must be https without userinfo, and a
// base URL may carry up to placeholdersMax {name} placeholders (for example a
// Cloudflare account ID) only in its path, so whatever the user fills in can
// never change the host the key is sent to. The frontend shows that host next
// to the plugin name so a plugin cannot pass itself off as a known service.
// Tiers are "free" and "limited"; a pinned "partner" tier waits for a trust
// signal that marks official marketplace packages.
//
// An entry is inline or a {"path": "providers/<name>.json"} reference
// (fileRef mode "entry"). Ids are "<pluginId>.<name>".

const aiProviderPointKey = "aiProviders"

var (
	aiProviderSpec            = mustContributionSpec(aiProviderPointKey)
	aiProviderLabelMax        = int(aiProviderSpec.Limit("labelMaxRunes"))
	aiProviderNoteMax         = int(aiProviderSpec.Limit("noteMaxRunes"))
	aiProviderLocalesMax      = int(aiProviderSpec.Limit("localesMax"))
	aiProviderModelsMax       = int(aiProviderSpec.Limit("modelsMax"))
	aiProviderModelIDMax      = int(aiProviderSpec.Limit("modelIdMaxLength"))
	aiProviderURLMax          = int(aiProviderSpec.Limit("urlMaxLength"))
	aiProviderPlaceholdersMax = int(aiProviderSpec.Limit("placeholdersMax"))
	aiProviderContextMax      = int(aiProviderSpec.Limit("contextWindowMax"))

	aiProviderNamePattern        = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	aiProviderModelIDPattern     = regexp.MustCompile(`^[A-Za-z0-9@][A-Za-z0-9._:/@+-]*$`)
	aiProviderPlaceholderPattern = regexp.MustCompile(`\{([a-z_]{1,24})\}`)
	aiProviderPlaceholderName    = regexp.MustCompile(`^[a-z_]{1,24}$`)
)

// AiProviderModelContribution is one model of a provider preset. Tools is
// false for models that cannot take tool definitions; the host then sends
// plain chat requests to them.
type AiProviderModelContribution struct {
	ID            string            `json:"id"`
	Free          bool              `json:"free"`
	Tools         bool              `json:"tools"`
	Note          string            `json:"note,omitempty"`
	NoteLocalized map[string]string `json:"noteLocalized,omitempty"`
}

// AiProviderPlaceholderContribution is a {name} in the base URL path that
// the user fills in.
type AiProviderPlaceholderContribution struct {
	Name           string            `json:"name"`
	Label          string            `json:"label"`
	LabelLocalized map[string]string `json:"labelLocalized,omitempty"`
}

// AiProviderContribution is one validated provider preset as sent to the
// frontend. Host is the base URL's host name, shown beside the plugin name.
type AiProviderContribution struct {
	ID                 string                              `json:"id"`
	Label              string                              `json:"label"`
	LabelLocalized     map[string]string                   `json:"labelLocalized,omitempty"`
	BaseURL            string                              `json:"baseUrl"`
	Host               string                              `json:"host"`
	Placeholders       []AiProviderPlaceholderContribution `json:"placeholders"`
	APIFormat          string                              `json:"apiFormat"`
	AnthropicAuth      string                              `json:"anthropicAuth,omitempty"`
	Model              string                              `json:"model"`
	Models             []AiProviderModelContribution       `json:"models"`
	ContextWindow      int                                 `json:"contextWindow"`
	MaxInputTokens     int                                 `json:"maxInputTokens"`
	MaxOutputTokens    int                                 `json:"maxOutputTokens"`
	Tier               string                              `json:"tier"`
	Region             string                              `json:"region"`
	VerifiedAt         string                              `json:"verifiedAt"`
	QuotaNote          string                              `json:"quotaNote,omitempty"`
	QuotaNoteLocalized map[string]string                   `json:"quotaNoteLocalized,omitempty"`
	Caveat             string                              `json:"caveat,omitempty"`
	CaveatLocalized    map[string]string                   `json:"caveatLocalized,omitempty"`
	SignupURL          string                              `json:"signupUrl"`
	DocsURL            string                              `json:"docsUrl,omitempty"`
}

type aiProviderModelEntry struct {
	ID            string            `json:"id"`
	Free          bool              `json:"free"`
	Tools         *bool             `json:"tools"`
	Note          string            `json:"note"`
	NoteLocalized map[string]string `json:"noteLocalized"`
}

type aiProviderManifestEntry struct {
	ID                 string                              `json:"id"`
	Label              string                              `json:"label"`
	LabelLocalized     map[string]string                   `json:"labelLocalized"`
	BaseURL            string                              `json:"baseUrl"`
	Placeholders       []AiProviderPlaceholderContribution `json:"placeholders"`
	APIFormat          string                              `json:"apiFormat"`
	AnthropicAuth      string                              `json:"anthropicAuth"`
	Model              string                              `json:"model"`
	Models             []aiProviderModelEntry              `json:"models"`
	ContextWindow      int                                 `json:"contextWindow"`
	MaxInputTokens     int                                 `json:"maxInputTokens"`
	MaxOutputTokens    int                                 `json:"maxOutputTokens"`
	Tier               string                              `json:"tier"`
	Region             string                              `json:"region"`
	VerifiedAt         string                              `json:"verifiedAt"`
	QuotaNote          string                              `json:"quotaNote"`
	QuotaNoteLocalized map[string]string                   `json:"quotaNoteLocalized"`
	Caveat             string                              `json:"caveat"`
	CaveatLocalized    map[string]string                   `json:"caveatLocalized"`
	SignupURL          string                              `json:"signupUrl"`
	DocsURL            string                              `json:"docsUrl"`
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:              aiProviderPointKey,
		Decode:           decodeAiProviderEntry,
		DuplicateMessage: func(id string) string { return fmt.Sprintf("AI provider id %q is duplicated", id) },
		IssuePrefix:      "invalid aiProviders contribution",
		IssueText: func(issue ContributionIssue) string {
			if issue.ID != "" {
				return fmt.Sprintf("AI provider %q skipped: %s", issue.ID, issue.Message)
			}
			return fmt.Sprintf("AI provider entry %d skipped: %s", issue.Entry, issue.Message)
		},
		EntryNoun:  "AI providers",
		FileLabels: contributionFileLabels{Noun: "AI provider", Entry: "AI provider", ShapeHint: "an AI provider object"},
	})
}

func decodeAiProviderEntry(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var entry aiProviderManifestEntry
	if err := decodeStrictJSON(raw, &entry, true); err != nil {
		return nil, strings.TrimSpace(entry.ID), fmt.Errorf("parse AI provider: %w", err)
	}
	provider, err := validateAiProvider(ctx.manifest.ID, entry)
	if err != nil {
		return nil, provider.ID, fmt.Errorf("AI provider %q %w", provider.ID, err)
	}
	return provider, provider.ID, nil
}

func validateAiProvider(pluginID string, entry aiProviderManifestEntry) (AiProviderContribution, error) {
	id := strings.TrimSpace(entry.ID)
	provider := AiProviderContribution{ID: id}
	name, ok := strings.CutPrefix(id, pluginID+".")
	if !ok || !aiProviderNamePattern.MatchString(name) {
		return provider, fmt.Errorf("id must be %q followed by a name matching ^[a-z0-9][a-z0-9-]{0,31}$", pluginID+".")
	}
	var err error
	if provider.Label, provider.LabelLocalized, err = aiProviderText("label", entry.Label, entry.LabelLocalized, aiProviderLabelMax, true); err != nil {
		return provider, err
	}
	if provider.QuotaNote, provider.QuotaNoteLocalized, err = aiProviderText("quotaNote", entry.QuotaNote, entry.QuotaNoteLocalized, aiProviderNoteMax, false); err != nil {
		return provider, err
	}
	if provider.Caveat, provider.CaveatLocalized, err = aiProviderText("caveat", entry.Caveat, entry.CaveatLocalized, aiProviderNoteMax, false); err != nil {
		return provider, err
	}

	if !slices.Contains(aiProviderSpec.List("tiers"), entry.Tier) {
		return provider, fmt.Errorf("tier must be one of %s", strings.Join(aiProviderSpec.List("tiers"), ", "))
	}
	provider.Tier = entry.Tier
	if provider.Tier == "limited" && provider.Caveat == "" {
		return provider, errors.New(`with tier "limited" needs a caveat that states the limitation`)
	}
	if !slices.Contains(aiProviderSpec.List("regions"), entry.Region) {
		return provider, fmt.Errorf("region must be one of %s", strings.Join(aiProviderSpec.List("regions"), ", "))
	}
	provider.Region = entry.Region
	if _, err := time.Parse(time.DateOnly, entry.VerifiedAt); err != nil {
		return provider, errors.New("verifiedAt must be a YYYY-MM-DD date")
	}
	provider.VerifiedAt = entry.VerifiedAt

	if !slices.Contains(aiProviderSpec.List("apiFormats"), entry.APIFormat) {
		return provider, fmt.Errorf("apiFormat must be one of %s", strings.Join(aiProviderSpec.List("apiFormats"), ", "))
	}
	provider.APIFormat = entry.APIFormat
	if entry.AnthropicAuth != "" {
		if provider.APIFormat != "anthropic" || !slices.Contains(aiProviderSpec.List("anthropicAuth"), entry.AnthropicAuth) {
			return provider, errors.New(`anthropicAuth is allowed only with apiFormat "anthropic" and must be x-api-key or bearer`)
		}
		provider.AnthropicAuth = entry.AnthropicAuth
	}

	if provider.BaseURL, provider.Host, provider.Placeholders, err = validateAiProviderBaseURL(entry.BaseURL, entry.Placeholders); err != nil {
		return provider, err
	}
	if provider.SignupURL, err = validateAiProviderLink("signupUrl", entry.SignupURL, true); err != nil {
		return provider, err
	}
	if provider.DocsURL, err = validateAiProviderLink("docsUrl", entry.DocsURL, false); err != nil {
		return provider, err
	}

	if provider.Models, err = validateAiProviderModels(entry.Models); err != nil {
		return provider, err
	}
	provider.Model = strings.TrimSpace(entry.Model)
	if !slices.ContainsFunc(provider.Models, func(model AiProviderModelContribution) bool { return model.ID == provider.Model }) {
		return provider, fmt.Errorf("model %q must be one of its models", provider.Model)
	}

	if entry.ContextWindow < 1 || entry.ContextWindow > aiProviderContextMax {
		return provider, fmt.Errorf("contextWindow must be between 1 and %d", aiProviderContextMax)
	}
	if entry.MaxInputTokens < 1 || entry.MaxInputTokens > entry.ContextWindow || entry.MaxOutputTokens < 1 || entry.MaxOutputTokens > entry.ContextWindow {
		return provider, errors.New("maxInputTokens and maxOutputTokens must be between 1 and contextWindow")
	}
	provider.ContextWindow, provider.MaxInputTokens, provider.MaxOutputTokens = entry.ContextWindow, entry.MaxInputTokens, entry.MaxOutputTokens
	return provider, nil
}

func aiProviderText(field, value string, localized map[string]string, maxRunes int, required bool) (string, map[string]string, error) {
	text, err := validateContributionLine(value, maxRunes, required)
	if err != nil {
		return "", nil, fmt.Errorf("%s %w", field, err)
	}
	if text == "" && len(localized) > 0 {
		return "", nil, fmt.Errorf("%sLocalized needs %s", field, field)
	}
	texts, err := validateContributionLocalized(localized, maxRunes, aiProviderLocalesMax)
	if err != nil {
		return "", nil, fmt.Errorf("%sLocalized %w", field, err)
	}
	return text, texts, nil
}

// validateAiProviderBaseURL requires an https URL without userinfo, query or
// fragment whose {name} placeholders are exactly the declared ones and all
// sit after the start of the path.
func validateAiProviderBaseURL(raw string, declared []AiProviderPlaceholderContribution) (string, string, []AiProviderPlaceholderContribution, error) {
	value := strings.TrimSpace(raw)
	if len(value) > aiProviderURLMax {
		return "", "", nil, fmt.Errorf("baseUrl must be at most %d bytes", aiProviderURLMax)
	}
	if len(declared) > aiProviderPlaceholdersMax {
		return "", "", nil, fmt.Errorf("placeholders must have at most %d entries", aiProviderPlaceholdersMax)
	}
	placeholders := make([]AiProviderPlaceholderContribution, 0, len(declared))
	names := map[string]bool{}
	for _, item := range declared {
		if !aiProviderPlaceholderName.MatchString(item.Name) || names[item.Name] {
			return "", "", nil, fmt.Errorf("placeholder name %q must be unique and match ^[a-z_]{1,24}$", item.Name)
		}
		names[item.Name] = true
		label, labels, err := aiProviderText("placeholder label", item.Label, item.LabelLocalized, aiProviderLabelMax, true)
		if err != nil {
			return "", "", nil, err
		}
		placeholders = append(placeholders, AiProviderPlaceholderContribution{Name: item.Name, Label: label, LabelLocalized: labels})
	}
	used := map[string]bool{}
	for _, match := range aiProviderPlaceholderPattern.FindAllStringSubmatch(value, -1) {
		if !names[match[1]] {
			return "", "", nil, fmt.Errorf("baseUrl placeholder {%s} is not declared in placeholders", match[1])
		}
		used[match[1]] = true
	}
	for name := range names {
		if !used[name] {
			return "", "", nil, fmt.Errorf("placeholder %q is not used in baseUrl", name)
		}
	}
	substituted := aiProviderPlaceholderPattern.ReplaceAllString(value, "x")
	if strings.ContainsAny(substituted, "{}") {
		return "", "", nil, errors.New("baseUrl braces must form {name} placeholders")
	}
	parsed, err := url.Parse(substituted)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Opaque != "" {
		return "", "", nil, errors.New("baseUrl must be an https URL with a host and no user name or password")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" || strings.ContainsAny(value, "?#") {
		return "", "", nil, errors.New("baseUrl must not have a query or fragment")
	}
	pathStart := len("https://") + len(parsed.Host)
	if first := strings.IndexByte(value, '{'); first >= 0 && (first <= pathStart || value[pathStart] != '/') {
		return "", "", nil, errors.New("baseUrl placeholders must be in the path, never in the host")
	}
	return value, parsed.Hostname(), placeholders, nil
}

func validateAiProviderLink(field, raw string, required bool) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		if required {
			return "", fmt.Errorf("%s is required", field)
		}
		return "", nil
	}
	if len(value) > aiProviderURLMax {
		return "", fmt.Errorf("%s must be at most %d bytes", field, aiProviderURLMax)
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil {
		return "", fmt.Errorf("%s must be an https URL with a host and no user name or password", field)
	}
	if err := checkContributionRunes(value, false, false); err != nil {
		return "", fmt.Errorf("%s %w", field, err)
	}
	return value, nil
}

func validateAiProviderModels(entries []aiProviderModelEntry) ([]AiProviderModelContribution, error) {
	if len(entries) == 0 || len(entries) > aiProviderModelsMax {
		return nil, fmt.Errorf("models must have 1 to %d entries", aiProviderModelsMax)
	}
	models := make([]AiProviderModelContribution, 0, len(entries))
	seen := map[string]bool{}
	for _, entry := range entries {
		id := strings.TrimSpace(entry.ID)
		if len(id) > aiProviderModelIDMax || !aiProviderModelIDPattern.MatchString(id) {
			return nil, fmt.Errorf("model id %q must be at most %d characters of letters, digits and ._:/@+-", entry.ID, aiProviderModelIDMax)
		}
		if seen[id] {
			return nil, fmt.Errorf("model id %q is duplicated", id)
		}
		seen[id] = true
		note, notes, err := aiProviderText("model note", entry.Note, entry.NoteLocalized, aiProviderNoteMax, false)
		if err != nil {
			return nil, fmt.Errorf("model %q %w", id, err)
		}
		models = append(models, AiProviderModelContribution{ID: id, Free: entry.Free, Tools: entry.Tools == nil || *entry.Tools, Note: note, NoteLocalized: notes})
	}
	return models, nil
}
