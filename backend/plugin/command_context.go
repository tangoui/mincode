package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Command parameter forms and when-expressions are host-rendered command
// enhancements. The plugin declares data only; MinCode owns evaluation and UI.
const (
	commandWhenMaxLength      = 512
	commandWhenMaxClauses     = 8
	pluginContextMaxKeys      = 64
	pluginContextValueMax     = 1024
	pluginContextChangedEvent = "plugin:context-changed"

	commandInputSchemaMaxBytes       = 16 * 1024
	commandInputSchemaMaxProperties  = 32
	commandInputSchemaMaxEnumValues  = 32
	commandInputSchemaMaxDescription = 200
)

const whenKeyMaxLength = 64

var (
	pluginContextKeyPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	commandInputPropertyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	// when-expressions may also read camelCase host keys (editor.languageId,
	// resource.isProjectRoot): upper-case letters are allowed only after the
	// first dot, so undotted camelCase keys (VS Code's sideBarVisible) stay
	// invalid. Plugin context keys stay lowercase. Length ≤ 64 is checked
	// separately (whenKeyMaxLength).
	whenKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*(?:\.[A-Za-z0-9._-]*)?$`)
)

// whenClause is one conjunct of a when-expression: [!]key [ == | != "string" ].
type whenClause struct {
	key    string
	value  string
	op     string
	negate bool
}

// parseWhenExpression parses the deliberately tiny when dialect:
// clause (&& clause)* where clause = [!]key [("=="|"!=") "literal"].
func parseWhenExpression(expression string) ([]whenClause, error) {
	if expression == "" {
		return nil, errors.New("when expression is empty")
	}
	if len(expression) > commandWhenMaxLength {
		return nil, fmt.Errorf("when expression exceeds %d bytes", commandWhenMaxLength)
	}
	tokens, err := tokenizeWhen(expression)
	if err != nil {
		return nil, err
	}
	clauses := []whenClause{}
	position := 0
	expectClause := true
	for position < len(tokens) {
		token := tokens[position]
		if expectClause {
			clause := whenClause{}
			if token == "!" {
				clause.negate = true
				position++
				if position >= len(tokens) {
					return nil, errors.New("when expression ends after !")
				}
				token = tokens[position]
			}
			if len(token) > whenKeyMaxLength || !whenKeyPattern.MatchString(token) {
				return nil, fmt.Errorf("when expression expects a context key, got %q", token)
			}
			clause.key = token
			position++
			if position < len(tokens) && (tokens[position] == "==" || tokens[position] == "!=") {
				clause.op = tokens[position]
				position++
				if position >= len(tokens) || !strings.HasPrefix(tokens[position], "\"") {
					return nil, errors.New("when comparison requires a quoted string literal")
				}
				clause.value = tokens[position][1 : len(tokens[position])-1]
				position++
			}
			clauses = append(clauses, clause)
			if len(clauses) > commandWhenMaxClauses {
				return nil, fmt.Errorf("when expression exceeds %d clauses", commandWhenMaxClauses)
			}
			expectClause = false
			continue
		}
		if token != "&&" {
			return nil, fmt.Errorf("when expression expects &&, got %q", token)
		}
		position++
		expectClause = true
	}
	if expectClause {
		return nil, errors.New("when expression ends after &&")
	}
	return clauses, nil
}

// tokenizeWhen emits identifiers, operators, and quoted literals (kept quoted
// so the parser can distinguish them from keys). Literals reject escapes.
func tokenizeWhen(expression string) ([]string, error) {
	tokens := []string{}
	index := 0
	for index < len(expression) {
		char := expression[index]
		switch {
		case char == ' ' || char == '\t':
			index++
		case char == '!':
			if index+1 < len(expression) && expression[index+1] == '=' {
				tokens = append(tokens, "!=")
				index += 2
			} else {
				tokens = append(tokens, "!")
				index++
			}
		case char == '=':
			if index+1 < len(expression) && expression[index+1] == '=' {
				tokens = append(tokens, "==")
				index += 2
			} else {
				return nil, errors.New("when expression uses == for equality")
			}
		case char == '&':
			if index+1 < len(expression) && expression[index+1] == '&' {
				tokens = append(tokens, "&&")
				index += 2
			} else {
				return nil, errors.New("when expression uses && between clauses")
			}
		case char == '"':
			end := index + 1
			for end < len(expression) && expression[end] != '"' {
				if expression[end] < 0x20 || expression[end] == '\\' {
					return nil, errors.New("when string literal contains an unsupported character")
				}
				end++
			}
			if end >= len(expression) {
				return nil, errors.New("when string literal is not terminated")
			}
			tokens = append(tokens, expression[index:end+1])
			index = end + 1
		default:
			end := index
			for end < len(expression) && (expression[end] == '.' || expression[end] == '_' ||
				expression[end] == '-' || expression[end] >= 'a' && expression[end] <= 'z' ||
				expression[end] >= '0' && expression[end] <= '9' || expression[end] >= 'A' && expression[end] <= 'Z') {
				end++
			}
			if end == index {
				return nil, fmt.Errorf("when expression has an unexpected character %q", char)
			}
			tokens = append(tokens, expression[index:end])
			index = end
		}
	}
	return tokens, nil
}

func validateWhenExpression(expression string) error {
	_, err := parseWhenExpression(strings.TrimSpace(expression))
	return err
}

// evalWhenClauses documents the evaluation semantics mirrored by the frontend
// domain evaluator. Unknown keys are falsy; equality requires a string value.
func evalWhenClauses(clauses []whenClause, context map[string]any) bool {
	for _, clause := range clauses {
		value, exists := context[clause.key]
		result := false
		switch clause.op {
		case "":
			result = contextTruthy(value, exists)
		case "==", "!=":
			text, ok := value.(string)
			equal := exists && ok && text == clause.value
			if clause.op == "==" {
				result = equal
			} else {
				result = !equal
			}
		}
		if clause.negate {
			result = !result
		}
		if !result {
			return false
		}
	}
	return true
}

func contextTruthy(value any, exists bool) bool {
	if !exists {
		return false
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return typed != ""
	case float64:
		return typed != 0
	default:
		return value != nil
	}
}

// validateContextValue enforces the host.context.set value bounds.
func validateContextValue(value any) error {
	switch value.(type) {
	case nil, string, bool, float64:
	default:
		return errors.New("context value must be a string, number, boolean, or null")
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > pluginContextValueMax {
		return errors.New("context value exceeds 1 KiB")
	}
	return nil
}

// validateCommandInputSchema enforces the form-renderable subset of the
// restricted schema dialect: an object with scalar/enum properties only.
func validateCommandInputSchema(schema map[string]any) error {
	encoded, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("invalid command inputSchema: %w", err)
	}
	if len(encoded) > commandInputSchemaMaxBytes {
		return errors.New("command inputSchema exceeds 16 KiB")
	}
	for key := range schema {
		switch key {
		case "type", "properties", "required", "description", "additionalProperties":
		default:
			return fmt.Errorf("command inputSchema uses unsupported keyword %q", key)
		}
	}
	if schema["type"] != "object" {
		return errors.New("command inputSchema type must be object")
	}
	if description, ok := schema["description"]; ok {
		if text, ok := description.(string); !ok || len(text) > commandInputSchemaMaxDescription {
			return errors.New("command inputSchema description is invalid")
		}
	}
	if _, ok := schema["additionalProperties"]; ok {
		if _, ok := schema["additionalProperties"].(bool); !ok {
			return errors.New("command inputSchema additionalProperties must be boolean")
		}
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok || len(properties) == 0 {
		return errors.New("command inputSchema requires properties")
	}
	if len(properties) > commandInputSchemaMaxProperties {
		return fmt.Errorf("command inputSchema exceeds %d properties", commandInputSchemaMaxProperties)
	}
	for name, raw := range properties {
		if !commandInputPropertyPattern.MatchString(name) {
			return fmt.Errorf("command inputSchema property %q is invalid", name)
		}
		property, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("command inputSchema property %q must be an object", name)
		}
		if err := validateCommandInputProperty(name, property); err != nil {
			return err
		}
	}
	if required, ok := schema["required"]; ok {
		entries, ok := required.([]any)
		if !ok || len(entries) > commandInputSchemaMaxProperties {
			return errors.New("command inputSchema required is invalid")
		}
		seen := map[string]struct{}{}
		for _, entry := range entries {
			name, ok := entry.(string)
			if !ok {
				return errors.New("command inputSchema required entries must be strings")
			}
			if _, exists := properties[name]; !exists {
				return fmt.Errorf("command inputSchema requires unknown property %q", name)
			}
			if _, duplicate := seen[name]; duplicate {
				return fmt.Errorf("command inputSchema duplicates required property %q", name)
			}
			seen[name] = struct{}{}
		}
	}
	return nil
}

func validateCommandInputProperty(name string, property map[string]any) error {
	for key := range property {
		switch key {
		case "type", "description", "enum", "default":
		default:
			return fmt.Errorf("command inputSchema property %q uses unsupported keyword %q", name, key)
		}
	}
	kind, _ := property["type"].(string)
	switch kind {
	case "string", "integer", "number", "boolean":
	default:
		return fmt.Errorf("command inputSchema property %q must be string, integer, number, or boolean", name)
	}
	if description, ok := property["description"]; ok {
		if text, ok := description.(string); !ok || len(text) > commandInputSchemaMaxDescription {
			return fmt.Errorf("command inputSchema property %q description is invalid", name)
		}
	}
	if values, ok := property["enum"]; ok {
		entries, ok := values.([]any)
		if !ok || len(entries) == 0 || len(entries) > commandInputSchemaMaxEnumValues {
			return fmt.Errorf("command inputSchema property %q enum is invalid", name)
		}
		for _, entry := range entries {
			if err := validateCommandInputScalar(name, kind, entry); err != nil {
				return err
			}
			if text, ok := entry.(string); ok && len(text) > 256 {
				return fmt.Errorf("command inputSchema property %q enum value is too long", name)
			}
		}
	}
	if fallback, ok := property["default"]; ok {
		if err := validateCommandInputScalar(name, kind, fallback); err != nil {
			return err
		}
	}
	return nil
}

func validateCommandInputScalar(name, kind string, value any) error {
	switch kind {
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("command inputSchema property %q expects a string value", name)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("command inputSchema property %q expects a boolean value", name)
		}
	case "number":
		if _, ok := value.(float64); !ok {
			return fmt.Errorf("command inputSchema property %q expects a numeric value", name)
		}
	case "integer":
		number, ok := value.(float64)
		if !ok || number != float64(int64(number)) {
			return fmt.Errorf("command inputSchema property %q expects an integer value", name)
		}
	}
	return nil
}
