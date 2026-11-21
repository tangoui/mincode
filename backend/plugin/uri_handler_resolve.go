package plugin

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"mincode/app/backend/core"
)

// plugin_host_uri_resolve maps a mincode://<pluginId>/<path>?<query> link to
// one of that plugin's uriHandlers against the current contribution snapshot.
// It only reads host state: it never activates, starts or calls the plugin.
// The frontend always shows the result in a host confirmation and resolves
// again right before acting (the plugin may have changed in between).
var (
	uriHandlerURLMaxLength       = int(uriHandlerSpec.Limit("urlMaxLength"))
	uriHandlerPathParamMaxLength = int(uriHandlerSpec.Limit("pathParamMaxLength"))
)

type uriResolveRequest struct {
	URL string `json:"url"`
}

// UriResolveTarget describes what a confirmed link opens.
type UriResolveTarget struct {
	Type         string `json:"type"`
	View         string `json:"view,omitempty"`
	ViewTitle    string `json:"viewTitle,omitempty"`
	Command      string `json:"command,omitempty"`
	CommandTitle string `json:"commandTitle,omitempty"`
}

type uriResolveResponse struct {
	PluginID       string            `json:"pluginId"`
	PluginName     string            `json:"pluginName"`
	PluginVersion  string            `json:"pluginVersion"`
	HandlerID      string            `json:"handlerId"`
	Title          string            `json:"title,omitempty"`
	TitleLocalized map[string]string `json:"titleLocalized,omitempty"`
	Target         UriResolveTarget  `json:"target"`
	Path           string            `json:"path"`
	PathParams     map[string]string `json:"pathParams"`
	Params         map[string]any    `json:"params"`
	// Payload is the invoke payload of a command target: the validated params
	// as JSON when the command declares an inputSchema, otherwise its static
	// payload.
	Payload string `json:"payload,omitempty"`
}

// parsedPluginLink is a syntactically valid plugin link.
type parsedPluginLink struct {
	pluginID string
	path     string
	segments []string // percent-decoded
	query    url.Values
}

// parsePluginLink validates the URL shape: scheme mincode, host = a plugin id
// that is not reserved, no userinfo or port, at most urlMaxLength bytes; the
// fragment is ignored and repeated query keys are rejected.
func parsePluginLink(raw string) (parsedPluginLink, error) {
	if raw == "" || len(raw) > uriHandlerURLMaxLength || !utf8.ValidString(raw) || checkContributionRunes(raw, false, false) != nil {
		return parsedPluginLink{}, fmt.Errorf("link must be at most %d bytes of single-line text", uriHandlerURLMaxLength)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return parsedPluginLink{}, errors.New("link is not a valid URL")
	}
	if !strings.EqualFold(parsed.Scheme, "mincode") || parsed.Opaque != "" {
		return parsedPluginLink{}, errors.New("link must use mincode://")
	}
	if parsed.User != nil || strings.Contains(parsed.Host, "@") {
		return parsedPluginLink{}, errors.New("link must not contain credentials")
	}
	if strings.Contains(parsed.Host, ":") {
		return parsedPluginLink{}, errors.New("link must not contain a port")
	}
	pluginID := strings.ToLower(parsed.Host)
	if !pluginIDPattern.MatchString(pluginID) || uriHandlerReservedHosts[pluginID] {
		return parsedPluginLink{}, fmt.Errorf("link host %q is not a plugin id", parsed.Host)
	}
	escaped := parsed.EscapedPath()
	if len(escaped) > uriHandlerPathMaxLength*3 || !strings.HasPrefix(escaped, "/") {
		return parsedPluginLink{}, errors.New("link must name a path")
	}
	trimmed := strings.TrimSuffix(escaped[1:], "/")
	if trimmed == "" {
		return parsedPluginLink{}, errors.New("link must name a path")
	}
	parts := strings.Split(trimmed, "/")
	if len(parts) > uriHandlerPathSegmentsMax {
		return parsedPluginLink{}, fmt.Errorf("link path must have at most %d segments", uriHandlerPathSegmentsMax)
	}
	segments := make([]string, len(parts))
	for index, part := range parts {
		decoded, err := url.PathUnescape(part)
		if err != nil || decoded == "" {
			return parsedPluginLink{}, errors.New("link path has an invalid segment")
		}
		segments[index] = decoded
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return parsedPluginLink{}, errors.New("link query is invalid")
	}
	for key, values := range query {
		if len(values) != 1 {
			return parsedPluginLink{}, fmt.Errorf("link query repeats %q", key)
		}
	}
	if len(query) > uriHandlerQueryPropertiesMax {
		return parsedPluginLink{}, fmt.Errorf("link query must have at most %d parameters", uriHandlerQueryPropertiesMax)
	}
	return parsedPluginLink{pluginID: pluginID, path: "/" + trimmed, segments: segments, query: query}, nil
}

// matchUriHandler returns the handler whose shape matches the link segments;
// at the first segment where candidates differ a literal beats a param.
func matchUriHandler(handlers []UriHandlerContribution, segments []string) (UriHandlerContribution, map[string]string, bool) {
	var best *UriHandlerContribution
	var bestParams map[string]string
	for index := range handlers {
		handler := &handlers[index]
		if len(handler.segments) != len(segments) {
			continue
		}
		params := map[string]string{}
		matched := true
		for position, segment := range handler.segments {
			if segment.Param != "" {
				params[segment.Param] = segments[position]
				continue
			}
			if segment.Literal != segments[position] {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		if best == nil || uriHandlerMoreSpecific(handler.segments, best.segments) {
			best, bestParams = handler, params
		}
	}
	if best == nil {
		return UriHandlerContribution{}, nil, false
	}
	return *best, bestParams, true
}

func uriHandlerMoreSpecific(candidate, current []uriPathSegment) bool {
	for index := range candidate {
		candidateLiteral := candidate[index].Param == ""
		currentLiteral := current[index].Param == ""
		if candidateLiteral != currentLiteral {
			return candidateLiteral
		}
	}
	return false
}

// coerceUriQuery converts link query strings per the schema's property types
// and validates the result (unknown, missing required, enum, length).
func coerceUriQuery(schema map[string]any, query url.Values) (map[string]any, error) {
	params := map[string]any{}
	if len(query) == 0 && schema == nil {
		return params, nil
	}
	if schema == nil {
		return nil, errors.New("this link takes no parameters")
	}
	properties, _ := schema["properties"].(map[string]any)
	for name, values := range query {
		raw, ok := properties[name]
		if !ok {
			return nil, fmt.Errorf("parameter %q is not declared", name)
		}
		property, _ := raw.(map[string]any)
		kind, _ := property["type"].(string)
		value := values[0]
		if !utf8.ValidString(value) || utf8.RuneCountInString(value) > uriHandlerQueryValueMaxRunes {
			return nil, fmt.Errorf("parameter %q must be at most %d characters", name, uriHandlerQueryValueMaxRunes)
		}
		switch kind {
		case "string":
			params[name] = value
		case "integer":
			number, err := strconv.ParseInt(value, 10, 64)
			if err != nil || number > 1<<53 || number < -(1<<53) {
				return nil, fmt.Errorf("parameter %q must be an integer", name)
			}
			params[name] = float64(number)
		case "number":
			number, err := strconv.ParseFloat(value, 64)
			if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
				return nil, fmt.Errorf("parameter %q must be a number", name)
			}
			params[name] = number
		case "boolean":
			switch value {
			case "true":
				params[name] = true
			case "false":
				params[name] = false
			default:
				return nil, fmt.Errorf("parameter %q must be true or false", name)
			}
		default:
			return nil, fmt.Errorf("parameter %q has an unsupported type", name)
		}
	}
	if err := validateCommandInputValues(schema, params); err != nil {
		return nil, err
	}
	return params, nil
}

func (manager *pluginManager) resolveURI(request uriResolveRequest) (uriResolveResponse, error) {
	if _, _, ok := enabledContributionPoint(uriHandlerPointKey); !ok {
		return uriResolveResponse{}, newContributionError(contributionErrUnavailable, "plugin links are not enabled in this host")
	}
	link, err := parsePluginLink(request.URL)
	if err != nil {
		return uriResolveResponse{}, newContributionError(contributionErrInvalidRequest, "%v", err)
	}
	record, err := manager.record(link.pluginID)
	if err != nil {
		return uriResolveResponse{}, newContributionError(contributionErrNotFound, "plugin %q is not installed", link.pluginID)
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	if record.builtin != nil || record.manifest == nil || record.manifest.isDeclarative() {
		return uriResolveResponse{}, newContributionError(contributionErrNotFound, "plugin %q does not handle links", link.pluginID)
	}
	if !record.contributionsListedLocked() || !manager.workspaceActiveLocked(record) {
		return uriResolveResponse{}, newContributionError(contributionErrUnavailable, "plugin %q is not active in this workspace", link.pluginID)
	}
	var handlers []UriHandlerContribution
	if resolved := record.manifest.contribution(uriHandlerPointKey); resolved != nil {
		for _, entry := range resolved.Entries {
			if handler, ok := entry.(UriHandlerContribution); ok {
				handlers = append(handlers, handler)
			}
		}
	}
	handler, pathParams, ok := matchUriHandler(handlers, link.segments)
	if !ok {
		return uriResolveResponse{}, newContributionError(contributionErrNotFound, "plugin %q has no handler for %s", link.pluginID, link.path)
	}
	for name, value := range pathParams {
		if utf8.RuneCountInString(value) > uriHandlerPathParamMaxLength || checkContributionRunes(value, false, false) != nil {
			return uriResolveResponse{}, newContributionError(contributionErrInvalidRequest, "path parameter %q must be a single line of at most %d characters", name, uriHandlerPathParamMaxLength)
		}
	}
	commands, views, actions := manifestCommandMaps(record.manifest)
	response := uriResolveResponse{
		PluginID: link.pluginID, PluginName: record.manifest.Name, PluginVersion: record.manifest.Version,
		HandlerID: handler.ID, Title: handler.Title, TitleLocalized: handler.TitleLocalized,
		Path: link.path, PathParams: pathParams,
	}
	switch handler.Target.Type {
	case "view":
		view, ok := views[handler.Target.View]
		if !ok {
			return uriResolveResponse{}, newContributionError(contributionErrUnavailable, "view %q is gone", handler.Target.View)
		}
		params, err := coerceUriQuery(handler.Query, link.query)
		if err != nil {
			return uriResolveResponse{}, newContributionError(contributionErrInvalidRequest, "%v", err)
		}
		response.Params = params
		response.Target = UriResolveTarget{Type: "view", View: view.ID, ViewTitle: view.Title}
	case "command":
		// Decode already required a read action command; re-check against
		// this generation in case the command changed.
		command, err := contributionReadCommand(commands, actions, handler.Target.Command)
		if err != nil {
			return uriResolveResponse{}, newContributionError(contributionErrUnavailable, "%v", err)
		}
		params, err := coerceUriQuery(command.InputSchema, link.query)
		if err != nil {
			return uriResolveResponse{}, newContributionError(contributionErrInvalidRequest, "%v", err)
		}
		response.Params = params
		if command.InputSchema != nil {
			encoded, err := encodeContributionJSON(params)
			if err != nil {
				return uriResolveResponse{}, newContributionError(contributionErrInvalidRequest, "encode payload: %v", err)
			}
			response.Payload = string(encoded)
		} else {
			response.Payload = command.Payload
		}
		response.Target = UriResolveTarget{Type: "command", Command: command.ID, CommandTitle: command.Title}
	default:
		return uriResolveResponse{}, newContributionError(contributionErrUnavailable, "handler %q has no target", handler.ID)
	}
	return response, nil
}

func pluginHostURIResolveHandler(_, args string) (string, error) {
	var request uriResolveRequest
	if err := decodeStrictJSON([]byte(args), &request, true); err != nil {
		return "", newContributionError(contributionErrInvalidRequest, "plugin_host_uri_resolve: invalid arguments: %v", err)
	}
	response, err := host().resolveURI(request)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(response)
}

func init() {
	core.Register("plugin_host_uri_resolve", pluginHostURIResolveHandler)
}
