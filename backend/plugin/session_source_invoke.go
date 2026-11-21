package plugin

import (
	"context"
	"encoding/json"
	"time"

	"mincode/app/backend/core"
	"mincode/app/plugins/api"
)

// plugin_host_session_list and plugin_host_session_messages call a session
// source's read action on demand: the frontend calls them only when the user
// expands a plugin source or opens one of its sessions. The WebView names
// the source by reference; the host builds the fixed payloads
//
//	{source, request: "list", scope, project?: {name, root?}, locale}
//	{source, request: "messages", session, locale}
//
// project is sent only for scope "workspace" and its root only with
// workspace.read; nothing about built-in sources or other plugins is ever
// forwarded. Bounds: payload ≤ min(action maxPayloadBytes, payloadMaxBytes),
// timeoutMs, concurrentPerPlugin slots shared by both requests, result ≤
// listResultMaxBytes / messagesResultMaxBytes; results are validated in
// session_source_results.go. Session titles and message text are never
// logged.
var (
	sessionSourcePayloadMaxBytes = int(sessionSourceSpec.Limit("payloadMaxBytes"))
	sessionSourceTimeout         = time.Duration(sessionSourceSpec.Limit("timeoutMs")) * time.Millisecond
	sessionSourceConcurrency     = int(sessionSourceSpec.Limit("concurrentPerPlugin"))
)

const (
	sessionScopeWorkspace = "workspace"
	sessionScopeAll       = "all"
)

type sessionListRequest struct {
	PluginID    string `json:"pluginId"`
	SourceID    string `json:"sourceId"`
	RequestID   string `json:"requestId"`
	Scope       string `json:"scope"`
	ProjectRoot string `json:"projectRoot,omitempty"`
	Locale      string `json:"locale,omitempty"`
}

type sessionMessagesRequest struct {
	PluginID  string `json:"pluginId"`
	SourceID  string `json:"sourceId"`
	RequestID string `json:"requestId"`
	SessionID string `json:"sessionId"`
	Locale    string `json:"locale,omitempty"`
}

type sessionListPayload struct {
	Source  string               `json:"source"`
	Request string               `json:"request"`
	Scope   string               `json:"scope"`
	Project *contributionProject `json:"project,omitempty"`
	Locale  string               `json:"locale"`
}

type sessionMessagesPayload struct {
	Source  string `json:"source"`
	Request string `json:"request"`
	Session string `json:"session"`
	Locale  string `json:"locale"`
}

func (manager *pluginManager) sessionList(ctx context.Context, request sessionListRequest) (sessionListResponse, error) {
	if err := normalizeSessionListRequest(&request); err != nil {
		return sessionListResponse{}, err
	}
	source, actionLimit, workspaceRead, err := manager.lookupSessionSource(request.PluginID, request.SourceID)
	if err != nil {
		return sessionListResponse{}, err
	}
	payload := sessionListPayload{Source: source.ID, Request: "list", Scope: request.Scope, Locale: request.Locale}
	if request.Scope == sessionScopeWorkspace {
		if payload.Project, err = contributionProjectFor(request.ProjectRoot, workspaceRead); err != nil {
			return sessionListResponse{}, err
		}
	}
	raw, err := manager.invokeSessionSource(ctx, request.PluginID, request.RequestID, source, actionLimit, payload, sessionSourceListResultMaxBytes)
	if err != nil {
		return sessionListResponse{}, err
	}
	response, err := parseSessionListResult(raw)
	if err != nil {
		return sessionListResponse{}, newContributionError(contributionErrInvalidResult, "session source %q: %v", source.ID, err)
	}
	return response, nil
}

func (manager *pluginManager) sessionMessages(ctx context.Context, request sessionMessagesRequest) (sessionMessagesResponse, error) {
	if err := normalizeSessionMessagesRequest(&request); err != nil {
		return sessionMessagesResponse{}, err
	}
	source, actionLimit, _, err := manager.lookupSessionSource(request.PluginID, request.SourceID)
	if err != nil {
		return sessionMessagesResponse{}, err
	}
	payload := sessionMessagesPayload{Source: source.ID, Request: "messages", Session: request.SessionID, Locale: request.Locale}
	raw, err := manager.invokeSessionSource(ctx, request.PluginID, request.RequestID, source, actionLimit, payload, sessionSourceMessagesResultMaxBytes)
	if err != nil {
		return sessionMessagesResponse{}, err
	}
	response, err := parseSessionMessagesResult(raw)
	if err != nil {
		return sessionMessagesResponse{}, newContributionError(contributionErrInvalidResult, "session source %q: %v", source.ID, err)
	}
	return response, nil
}

// lookupSessionSource finds the source in the plugin's current generation
// and returns it with its action's maxPayloadBytes and whether the plugin
// declares workspace.read.
func (manager *pluginManager) lookupSessionSource(pluginID, sourceID string) (SessionSourceContribution, int, bool, error) {
	if _, _, ok := enabledContributionPoint(sessionSourcePointKey); !ok {
		return SessionSourceContribution{}, 0, false, newContributionError(contributionErrUnavailable, "session sources are not enabled in this host")
	}
	record, err := manager.record(pluginID)
	if err != nil {
		return SessionSourceContribution{}, 0, false, newContributionError(contributionErrNotFound, "plugin %q not found", pluginID)
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	source, err := sessionSourceLocked(record, sourceID)
	if err != nil {
		return SessionSourceContribution{}, 0, false, err
	}
	actionLimit := 0
	if action, ok := contributedActionLocked(record, source.Action); ok {
		actionLimit = action.MaxPayloadBytes
	}
	return source, actionLimit, manifestHasCapability(record.manifest, api.CapabilityWorkspaceRead), nil
}

// invokeSessionSource encodes the payload, enforces the payload bound and
// calls the source's action through invokeContributedAction.
func (manager *pluginManager) invokeSessionSource(ctx context.Context, pluginID, requestID string, source SessionSourceContribution, actionLimit int, payload any, maxResultBytes int) (json.RawMessage, error) {
	encoded, err := encodeContributionJSON(payload)
	if err != nil {
		return nil, newContributionError(contributionErrInvalidRequest, "encode payload: %v", err)
	}
	limit := sessionSourcePayloadMaxBytes
	if actionLimit > 0 && actionLimit < limit {
		limit = actionLimit
	}
	if len(encoded) > limit {
		return nil, newContributionError(contributionErrTooLarge, "payload is %d bytes, session source %q allows %d", len(encoded), source.ID, limit)
	}
	return manager.invokeContributedAction(ctx, contributedActionCall{
		PluginID: pluginID, Point: sessionSourcePointKey, EntryID: source.ID, ActionID: source.Action,
		RequestID: requestID, Payload: encoded, Timeout: sessionSourceTimeout,
		MaxResultBytes: maxResultBytes, Concurrency: sessionSourceConcurrency,
		Validate: sessionSourceUnchanged(source),
	})
}

// sessionSourceUnchanged is the Validate callback of a session source call:
// the entry must still be contributed, with the same action, by the
// generation that will answer. A generation that removed or changed it
// makes the call unavailable.
func sessionSourceUnchanged(source SessionSourceContribution) func(*pluginRecord) error {
	return func(record *pluginRecord) error {
		current, err := sessionSourceLocked(record, source.ID)
		if err != nil {
			return newContributionError(contributionErrUnavailable, "session source %q is no longer contributed", source.ID)
		}
		if current.Action != source.Action {
			return newContributionError(contributionErrUnavailable, "session source %q changed", source.ID)
		}
		return nil
	}
}

// sessionSourceLocked finds the source in the record's current generation.
// Called with record.mu held.
func sessionSourceLocked(record *pluginRecord, sourceID string) (SessionSourceContribution, error) {
	if record.manifest != nil {
		if resolved := record.manifest.contribution(sessionSourcePointKey); resolved != nil {
			for _, entry := range resolved.Entries {
				if source, ok := entry.(SessionSourceContribution); ok && source.ID == sourceID {
					return source, nil
				}
			}
		}
	}
	return SessionSourceContribution{}, newContributionError(contributionErrNotFound, "session source %q is not contributed by %q", sourceID, record.id)
}

func normalizeSessionSourceRef(pluginID, sourceID string, locale *string) error {
	if !pluginIDPattern.MatchString(pluginID) {
		return newContributionError(contributionErrInvalidRequest, "pluginId %q is invalid", pluginID)
	}
	if !sessionSourceIDPattern.MatchString(sourceID) {
		return newContributionError(contributionErrInvalidRequest, "sourceId %q is invalid", sourceID)
	}
	if *locale == "" {
		*locale = "en"
	}
	if len(*locale) > 35 || !searchLocalePattern.MatchString(*locale) {
		return newContributionError(contributionErrInvalidRequest, "locale %q is invalid", *locale)
	}
	return nil
}

// normalizeSessionListRequest checks the request: scope "workspace" needs a
// projectRoot (canonicalized later by contributionProjectFor), scope "all"
// takes none.
func normalizeSessionListRequest(request *sessionListRequest) error {
	if err := normalizeSessionSourceRef(request.PluginID, request.SourceID, &request.Locale); err != nil {
		return err
	}
	switch request.Scope {
	case sessionScopeWorkspace:
		if request.ProjectRoot == "" {
			return newContributionError(contributionErrInvalidRequest, "scope %q needs projectRoot", request.Scope)
		}
	case sessionScopeAll:
		if request.ProjectRoot != "" {
			return newContributionError(contributionErrInvalidRequest, "scope %q takes no projectRoot", request.Scope)
		}
	default:
		return newContributionError(contributionErrInvalidRequest, "scope must be %q or %q", sessionScopeWorkspace, sessionScopeAll)
	}
	return nil
}

func normalizeSessionMessagesRequest(request *sessionMessagesRequest) error {
	if err := normalizeSessionSourceRef(request.PluginID, request.SourceID, &request.Locale); err != nil {
		return err
	}
	if !validSessionID(request.SessionID) {
		return newContributionError(contributionErrInvalidRequest, "sessionId must be single-line text of at most %d bytes", sessionSourceSessionIDMaxLength)
	}
	return nil
}

func pluginHostSessionListHandler(_, args string) (string, error) {
	var request sessionListRequest
	if err := decodeStrictJSON([]byte(args), &request, true); err != nil {
		return "", newContributionError(contributionErrInvalidRequest, "plugin_host_session_list: invalid arguments: %v", err)
	}
	response, err := host().sessionList(context.Background(), request)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(response)
}

func pluginHostSessionMessagesHandler(_, args string) (string, error) {
	var request sessionMessagesRequest
	if err := decodeStrictJSON([]byte(args), &request, true); err != nil {
		return "", newContributionError(contributionErrInvalidRequest, "plugin_host_session_messages: invalid arguments: %v", err)
	}
	response, err := host().sessionMessages(context.Background(), request)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(response)
}

func init() {
	core.Register("plugin_host_session_list", pluginHostSessionListHandler)
	core.Register("plugin_host_session_messages", pluginHostSessionMessagesHandler)
}
