package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"
	"unicode/utf8"
)

// Shared target and argument validation for contribution points that let a
// plugin name something the host opens or runs on the user's behalf
// (searchProviders result targets, uriHandlers link targets). Only the
// plugin's own views, its own action commands whose effective risk is
// "read", and http(s) URLs are ever reachable this way.

// contributionCommandEffectiveRisk is the higher of the command's own risk
// and the risk of the action it runs (when that action is declared).
func contributionCommandEffectiveRisk(command ManifestCommand, actions map[string]ManifestUIAction) string {
	risk := command.Risk
	if risk == "" {
		risk = "read"
	}
	if action, ok := actions[command.Action]; ok && riskRank(action.Risk) > riskRank(risk) {
		risk = action.Risk
	}
	return risk
}

// contributionReadCommand returns the plugin's own action command id when it
// exists, runs an action (not a view target) and has effective risk "read".
func contributionReadCommand(commands map[string]ManifestCommand, actions map[string]ManifestUIAction, id string) (ManifestCommand, error) {
	command, ok := commands[id]
	if id == "" || !ok {
		return ManifestCommand{}, fmt.Errorf("command %q is not one of this plugin's contributes.commands", id)
	}
	if command.Action == "" || command.Target != nil {
		return ManifestCommand{}, fmt.Errorf("command %q must be an action command", id)
	}
	if risk := contributionCommandEffectiveRisk(command, actions); risk != "read" {
		return ManifestCommand{}, fmt.Errorf("command %q must have effective risk \"read\", not %q", id, risk)
	}
	return command, nil
}

// manifestCommandMaps indexes a validated manifest's commands, views and
// actions (risk defaults already applied by manifest validation).
func manifestCommandMaps(manifest *Manifest) (map[string]ManifestCommand, map[string]ManifestView, map[string]ManifestUIAction) {
	commands := map[string]ManifestCommand{}
	views := map[string]ManifestView{}
	actions := map[string]ManifestUIAction{}
	if manifest == nil {
		return commands, views, actions
	}
	for _, command := range manifest.Contributes.Commands {
		commands[command.ID] = command
	}
	for _, view := range manifest.Contributes.Views {
		if view.Floating == nil {
			views[view.ID] = view
		}
	}
	for _, action := range manifest.Contributes.Actions {
		actions[action.ID] = action
	}
	return commands, views, actions
}

// validateCommandInputValues checks concrete values against a command
// inputSchema (validateCommandInputSchema dialect, plus an optional string
// maxLength): unknown keys, wrong types, values outside enum, over-long or
// multi-line strings and missing required properties are rejected.
func validateCommandInputValues(schema map[string]any, values map[string]any) error {
	properties, _ := schema["properties"].(map[string]any)
	for name, value := range values {
		raw, ok := properties[name]
		if !ok {
			return fmt.Errorf("parameter %q is not declared", name)
		}
		property, _ := raw.(map[string]any)
		kind, _ := property["type"].(string)
		if err := validateCommandInputScalar(name, kind, value); err != nil {
			return fmt.Errorf("parameter %q has the wrong type", name)
		}
		if number, ok := value.(float64); ok && (math.IsNaN(number) || math.IsInf(number, 0) || math.Abs(number) > 1<<53) {
			return fmt.Errorf("parameter %q is out of range", name)
		}
		if text, ok := value.(string); ok {
			if !utf8.ValidString(text) || checkContributionRunes(text, false, false) != nil {
				return fmt.Errorf("parameter %q must be a single line of text", name)
			}
			if limit := commandInputMaxLength(property); limit > 0 && utf8.RuneCountInString(text) > limit {
				return fmt.Errorf("parameter %q must be at most %d characters", name, limit)
			}
		}
		if enum, ok := property["enum"].([]any); ok {
			matched := false
			for _, candidate := range enum {
				matched = matched || candidate == value
			}
			if !matched {
				return fmt.Errorf("parameter %q is not one of the allowed values", name)
			}
		}
	}
	if required, ok := schema["required"].([]any); ok {
		for _, entry := range required {
			name, _ := entry.(string)
			if _, present := values[name]; !present {
				return fmt.Errorf("parameter %q is required", name)
			}
		}
	}
	return nil
}

// commandInputMaxLength reads a string property's optional maxLength (0: none).
func commandInputMaxLength(property map[string]any) int {
	switch value := property["maxLength"].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case int64:
		return int(value)
	}
	return 0
}

// validateContributionScalarArgs checks pass-through arguments: an object of
// at most maxProperties scalar values (string, finite number, boolean) with
// property-pattern keys, encoding to at most maxBytes.
func validateContributionScalarArgs(args map[string]any, maxProperties, maxBytes int) error {
	if len(args) > maxProperties {
		return fmt.Errorf("args must have at most %d properties", maxProperties)
	}
	for name, value := range args {
		if !commandInputPropertyPattern.MatchString(name) {
			return fmt.Errorf("args key %q is invalid", name)
		}
		switch typed := value.(type) {
		case string:
			if !utf8.ValidString(typed) || checkContributionRunes(typed, false, false) != nil {
				return fmt.Errorf("args %q must be a single line of text", name)
			}
		case float64:
			if math.IsNaN(typed) || math.IsInf(typed, 0) {
				return fmt.Errorf("args %q must be finite", name)
			}
		case bool:
		default:
			return fmt.Errorf("args %q must be a string, number or boolean", name)
		}
	}
	encoded, err := encodeContributionJSON(args)
	if err != nil {
		return err
	}
	if len(encoded) > maxBytes {
		return fmt.Errorf("args must be at most %d bytes", maxBytes)
	}
	return nil
}

// validateContributionHTTPURL accepts an absolute URL whose scheme is one of
// schemes, with a host, without userinfo, within maxLength bytes and without
// control or bidi characters. It returns the re-serialised URL.
func validateContributionHTTPURL(raw string, maxLength int, schemes []string) (string, error) {
	if raw == "" || len(raw) > maxLength || !utf8.ValidString(raw) || checkContributionRunes(raw, false, false) != nil ||
		strings.ContainsAny(raw, " \\") {
		return "", fmt.Errorf("url must be an absolute URL of at most %d bytes", maxLength)
	}
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.Opaque != "" {
		return "", errors.New("url must be an absolute URL")
	}
	scheme := strings.ToLower(parsed.Scheme)
	allowed := false
	for _, candidate := range schemes {
		allowed = allowed || candidate == scheme
	}
	if !allowed {
		return "", fmt.Errorf("url scheme %q is not allowed", parsed.Scheme)
	}
	if parsed.User != nil || strings.Contains(parsed.Host, "@") {
		return "", errors.New("url must not contain credentials")
	}
	if parsed.Hostname() == "" {
		return "", errors.New("url must have a host")
	}
	parsed.Scheme = scheme
	normalized := parsed.String()
	if len(normalized) > maxLength {
		return "", fmt.Errorf("url must be at most %d bytes", maxLength)
	}
	return normalized, nil
}

// encodeContributionJSON marshals without HTML escaping (the frontend and the
// plugin both read the JSON as data, never as markup).
func encodeContributionJSON(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}
