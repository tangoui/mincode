package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Plugin link handlers (contributes.uriHandlers) are a process-only
// contribution point. An entry claims a path shape under
// mincode://<pluginId>/... and routes matching links to one of the plugin's
// own views (with a declared query schema) or to one of its own action
// commands whose effective risk is "read" (parameters checked against the
// command's inputSchema). Links are resolved host-side by
// plugin_host_uri_resolve (uri_handler_resolve.go) and always confirmed by
// the user before anything opens. Mirrors: tools/mincode-cli/uri_handlers.go
// (CLI), assertUriHandlersEntry (cloud) and
// vue-app/src/domain/plugins/uri-handlers.ts (frontend revalidation).
const uriHandlerPointKey = "uriHandlers"

var (
	uriHandlerSpec               = mustContributionSpec(uriHandlerPointKey)
	uriHandlerTitleMaxRunes      = int(uriHandlerSpec.Limit("titleMaxRunes"))
	uriHandlerLocalesMax         = int(uriHandlerSpec.Limit("localesMax"))
	uriHandlerPathMaxLength      = int(uriHandlerSpec.Limit("pathMaxLength"))
	uriHandlerPathSegmentsMax    = int(uriHandlerSpec.Limit("pathSegmentsMax"))
	uriHandlerQueryPropertiesMax = int(uriHandlerSpec.Limit("queryPropertiesMax"))
	uriHandlerQueryValueMaxRunes = int(uriHandlerSpec.Limit("queryValueMaxRunes"))
	uriHandlerReservedHosts      = contributionListSet(uriHandlerSpec.List("reservedHosts"))

	uriHandlerIDPattern         = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	uriHandlerLiteralPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	uriHandlerParamNamePattern  = regexp.MustCompile(`^[a-z][a-zA-Z0-9]{0,31}$`)
	uriHandlerParamSegmentRegex = regexp.MustCompile(`^\{([^{}]*)\}$`)
)

type uriHandlerManifestTarget struct {
	Type    string `json:"type"`
	View    string `json:"view,omitempty"`
	Command string `json:"command,omitempty"`
}

type uriHandlerManifestEntry struct {
	ID             string                   `json:"id"`
	Path           string                   `json:"path"`
	Title          string                   `json:"title,omitempty"`
	TitleLocalized map[string]string        `json:"titleLocalized,omitempty"`
	Target         uriHandlerManifestTarget `json:"target"`
	Query          map[string]any           `json:"query,omitempty"`
}

// UriHandlerTarget is the snapshot target (exactly one of View or Command).
type UriHandlerTarget struct {
	Type    string `json:"type"`
	View    string `json:"view,omitempty"`
	Command string `json:"command,omitempty"`
}

// UriHandlerContribution is the snapshot entry.
type UriHandlerContribution struct {
	ID             string            `json:"id"`
	Path           string            `json:"path"`
	Title          string            `json:"title,omitempty"`
	TitleLocalized map[string]string `json:"titleLocalized,omitempty"`
	Target         UriHandlerTarget  `json:"target"`
	Query          map[string]any    `json:"query,omitempty"`
	// segments is the parsed path (host-only).
	segments []uriPathSegment
}

// uriPathSegment is a literal segment or a {param} placeholder.
type uriPathSegment struct {
	Literal string
	Param   string
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:              uriHandlerPointKey,
		Decode:           decodeUriHandlerEntry,
		Finalize:         finalizeUriHandlerEntries,
		DuplicateMessage: func(id string) string { return fmt.Sprintf("duplicate link handler id %q", id) },
		IssuePrefix:      "invalid uriHandlers contribution",
		IssueText: func(issue ContributionIssue) string {
			if issue.ID != "" {
				return fmt.Sprintf("skipped link handler %q: %s", issue.ID, issue.Message)
			}
			return fmt.Sprintf("skipped contributes.uriHandlers[%d]: %s", issue.Entry, issue.Message)
		},
		EntryNoun:         "link handlers",
		ExpandsPermission: uriHandlersExpandPermission,
	})
}

func decodeUriHandlerEntry(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var entry uriHandlerManifestEntry
	if err := decodeStrictJSON(raw, &entry, true); err != nil {
		return nil, "", fmt.Errorf("parse link handler: %w", err)
	}
	id := strings.TrimSpace(entry.ID)
	if ctx.manifest != nil && uriHandlerReservedHosts[strings.ToLower(ctx.manifest.ID)] {
		return nil, id, fmt.Errorf("plugin id %q is a reserved mincode:// host and cannot handle links", ctx.manifest.ID)
	}
	if !uriHandlerIDPattern.MatchString(id) {
		return nil, id, fmt.Errorf("link handler id %q must match ^[a-z][a-z0-9-]{0,31}$", id)
	}
	segments, err := parseUriHandlerPath(entry.Path)
	if err != nil {
		return nil, id, fmt.Errorf("link handler %q %w", id, err)
	}
	title, err := validateContributionLine(entry.Title, uriHandlerTitleMaxRunes, false)
	if err != nil {
		return nil, id, fmt.Errorf("link handler %q title %w", id, err)
	}
	titleLocalized, err := validateContributionLocalized(entry.TitleLocalized, uriHandlerTitleMaxRunes, uriHandlerLocalesMax)
	if err != nil {
		return nil, id, fmt.Errorf("link handler %q titleLocalized %w", id, err)
	}
	handler := UriHandlerContribution{ID: id, Path: entry.Path, Title: title, TitleLocalized: titleLocalized, segments: segments}
	switch entry.Target.Type {
	case "view":
		if entry.Target.Command != "" {
			return nil, id, fmt.Errorf("link handler %q view target must not name a command", id)
		}
		if _, ok := ctx.views[entry.Target.View]; entry.Target.View == "" || !ok {
			return nil, id, fmt.Errorf("link handler %q target view %q is not one of this plugin's contributes.views", id, entry.Target.View)
		}
		handler.Target = UriHandlerTarget{Type: "view", View: entry.Target.View}
		if entry.Query != nil {
			query, err := normalizeUriHandlerQuerySchema(entry.Query)
			if err != nil {
				return nil, id, fmt.Errorf("link handler %q query: %w", id, err)
			}
			handler.Query = query
		}
	case "command":
		if entry.Target.View != "" {
			return nil, id, fmt.Errorf("link handler %q command target must not name a view", id)
		}
		if _, err := contributionReadCommand(ctx.commands, ctx.actions, entry.Target.Command); err != nil {
			return nil, id, fmt.Errorf("link handler %q target %w", id, err)
		}
		if entry.Query != nil {
			return nil, id, fmt.Errorf("link handler %q command target takes its parameters from the command's inputSchema; remove query", id)
		}
		for _, segment := range segments {
			if segment.Param != "" {
				return nil, id, fmt.Errorf("link handler %q command target path must not contain {%s}; commands take parameters only from the query", id, segment.Param)
			}
		}
		handler.Target = UriHandlerTarget{Type: "command", Command: entry.Target.Command}
	default:
		return nil, id, fmt.Errorf("link handler %q target type %q must be view or command", id, entry.Target.Type)
	}
	return handler, id, nil
}

// parseUriHandlerPath validates "/seg/{param}/...": 1-pathSegmentsMax
// segments, literal ^[a-z0-9][a-z0-9._-]{0,63}$ or {name} with
// ^[a-z][a-zA-Z0-9]{0,31}$, unique param names, at most pathMaxLength bytes.
func parseUriHandlerPath(path string) ([]uriPathSegment, error) {
	if len(path) > uriHandlerPathMaxLength {
		return nil, fmt.Errorf("path must be at most %d characters", uriHandlerPathMaxLength)
	}
	if !strings.HasPrefix(path, "/") || len(path) < 2 {
		return nil, errors.New("path must start with / and name at least one segment")
	}
	parts := strings.Split(path[1:], "/")
	if len(parts) > uriHandlerPathSegmentsMax {
		return nil, fmt.Errorf("path must have at most %d segments", uriHandlerPathSegmentsMax)
	}
	segments := make([]uriPathSegment, 0, len(parts))
	params := map[string]bool{}
	for _, part := range parts {
		if match := uriHandlerParamSegmentRegex.FindStringSubmatch(part); match != nil {
			name := match[1]
			if !uriHandlerParamNamePattern.MatchString(name) {
				return nil, fmt.Errorf("path parameter {%s} must match ^[a-z][a-zA-Z0-9]{0,31}$", name)
			}
			if params[name] {
				return nil, fmt.Errorf("path repeats parameter {%s}", name)
			}
			params[name] = true
			segments = append(segments, uriPathSegment{Param: name})
			continue
		}
		if !uriHandlerLiteralPattern.MatchString(part) {
			return nil, fmt.Errorf("path segment %q must match ^[a-z0-9][a-z0-9._-]{0,63}$ or be {name}", part)
		}
		segments = append(segments, uriPathSegment{Literal: part})
	}
	return segments, nil
}

// normalizeUriHandlerQuerySchema validates a view target's query schema: the
// command inputSchema dialect plus an optional string maxLength (1 to
// queryValueMaxRunes), at most queryPropertiesMax properties, and
// additionalProperties forced false.
func normalizeUriHandlerQuerySchema(schema map[string]any) (map[string]any, error) {
	if value, ok := schema["additionalProperties"]; ok && value != false {
		return nil, errors.New("additionalProperties must be false")
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		return nil, errors.New("query requires properties")
	}
	if len(properties) > uriHandlerQueryPropertiesMax {
		return nil, fmt.Errorf("query must have at most %d properties", uriHandlerQueryPropertiesMax)
	}
	stripped := make(map[string]any, len(schema))
	for key, value := range schema {
		stripped[key] = value
	}
	strippedProperties := make(map[string]any, len(properties))
	for name, raw := range properties {
		property, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("query property %q must be an object", name)
		}
		copyProperty := make(map[string]any, len(property))
		for key, value := range property {
			if key == "maxLength" {
				if property["type"] != "string" {
					return nil, fmt.Errorf("query property %q maxLength requires type string", name)
				}
				number, ok := value.(float64)
				if !ok || number != float64(int(number)) || number < 1 || int(number) > uriHandlerQueryValueMaxRunes {
					return nil, fmt.Errorf("query property %q maxLength must be an integer 1-%d", name, uriHandlerQueryValueMaxRunes)
				}
				continue
			}
			copyProperty[key] = value
		}
		strippedProperties[name] = copyProperty
	}
	stripped["properties"] = strippedProperties
	if err := validateCommandInputSchema(stripped); err != nil {
		return nil, errors.New(strings.Replace(err.Error(), "command inputSchema", "query schema", 1))
	}
	normalized := make(map[string]any, len(schema)+1)
	for key, value := range schema {
		normalized[key] = value
	}
	normalized["additionalProperties"] = false
	return normalized, nil
}

// uriHandlerShape is the literal/param sequence used for duplicate detection.
func uriHandlerShape(segments []uriPathSegment) string {
	parts := make([]string, len(segments))
	for index, segment := range segments {
		if segment.Param != "" {
			parts[index] = "{}"
		} else {
			parts[index] = segment.Literal
		}
	}
	return "/" + strings.Join(parts, "/")
}

// finalizeUriHandlerEntries rejects handlers whose path shape repeats an
// earlier handler's (param names do not matter).
func finalizeUriHandlerEntries(_ *pointContext, entries []contributionEntry, issues []ContributionIssue) ([]contributionEntry, []ContributionIssue) {
	seen := map[string]string{}
	kept := make([]contributionEntry, 0, len(entries))
	for _, entry := range entries {
		handler := entry.Value.(UriHandlerContribution)
		shape := uriHandlerShape(handler.segments)
		if owner, duplicate := seen[shape]; duplicate {
			issues = append(issues, ContributionIssue{
				Point: uriHandlerPointKey, Entry: entry.Index, ID: entry.ID,
				Message: fmt.Sprintf("path %q has the same shape as link handler %q", handler.Path, owner),
			})
			continue
		}
		seen[shape] = handler.ID
		kept = append(kept, entry)
	}
	return kept, issues
}

// uriHandlersExpandPermission reports a development generation that routes
// links to new code: a handler was added or changed its target.
func uriHandlersExpandPermission(current, candidate *resolvedContribution) bool {
	if candidate == nil {
		return false
	}
	known := map[string]UriHandlerTarget{}
	if current != nil {
		for _, entry := range current.Entries {
			if handler, ok := entry.(UriHandlerContribution); ok {
				known[handler.ID] = handler.Target
			}
		}
	}
	for _, entry := range candidate.Entries {
		handler, ok := entry.(UriHandlerContribution)
		if !ok {
			continue
		}
		if target, exists := known[handler.ID]; !exists || target != handler.Target {
			return true
		}
	}
	return false
}
