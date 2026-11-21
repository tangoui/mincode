package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"mincode/app/backend/core"
)

// plugin_host_search sends one palette query to a search provider's read
// action and returns validated, plain-text results. The WebView names the
// provider by reference ({pluginId, providerId, requestId, query, locale});
// the host builds the fixed payload {provider, query, locale}. Bounds: query
// queryMaxRunes single line, payload ≤ min(action maxPayloadBytes,
// payloadMaxBytes), timeoutMs, concurrentPerPlugin slots (one abandoned call
// plus one live), result ≤ resultMaxBytes and itemsMax items. Invalid items
// are dropped and counted; targets are limited to the plugin's own views, its
// own read action commands (args checked against inputSchema), http(s)
// URLs and, for a query that negotiated searchFileTargets under a ready
// workspace scope, clean project-relative files (search_file_targets.go),
// which the frontend opens only on explicit activation. Nothing is
// persisted and the plugin is activated only through this explicit call.
var (
	searchProviderQueryMaxRunes      = int(searchProviderSpec.Limit("queryMaxRunes"))
	searchProviderPayloadMaxBytes    = int(searchProviderSpec.Limit("payloadMaxBytes"))
	searchProviderResultMaxBytes     = int(searchProviderSpec.Limit("resultMaxBytes"))
	searchProviderItemsMax           = int(searchProviderSpec.Limit("itemsMax"))
	searchProviderItemTitleMaxRunes  = int(searchProviderSpec.Limit("itemTitleMaxRunes"))
	searchProviderItemDetailMaxRunes = int(searchProviderSpec.Limit("itemDetailMaxRunes"))
	searchProviderArgsMaxBytes       = int(searchProviderSpec.Limit("argsMaxBytes"))
	searchProviderArgsMaxProperties  = int(searchProviderSpec.Limit("argsMaxProperties"))
	searchProviderURLMaxLength       = int(searchProviderSpec.Limit("urlMaxLength"))
	searchProviderPathMaxLength      = int(searchProviderSpec.Limit("pathMaxLength"))
	searchProviderLineMax            = int(searchProviderSpec.Limit("lineMax"))
	searchProviderColumnMax          = int(searchProviderSpec.Limit("columnMax"))
	searchProviderTimeout            = time.Duration(searchProviderSpec.Limit("timeoutMs")) * time.Millisecond
	searchProviderConcurrency        = int(searchProviderSpec.Limit("concurrentPerPlugin"))
	searchProviderSchemes            = searchProviderSpec.List("schemes")
	searchProviderTargetTypes        = searchProviderSpec.List("targetTypes")
	// File targets need a negotiated, workspace-bound query.
	searchProviderLegacyTargetTypes = slices.DeleteFunc(searchProviderSpec.List("targetTypes"), func(value string) bool { return value == "file" })

	searchResultItemIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)
	searchLocalePattern       = regexp.MustCompile(`^[A-Za-z]{2,8}(-[A-Za-z0-9]{1,8}){0,3}$`)
)

type searchRequest struct {
	PluginID   string `json:"pluginId"`
	ProviderID string `json:"providerId"`
	RequestID  string `json:"requestId"`
	Query      string `json:"query"`
	Locale     string `json:"locale,omitempty"`
}

type searchPayload struct {
	Provider string `json:"provider"`
	Query    string `json:"query"`
	Locale   string `json:"locale"`
}

// SearchResultTarget is the normalized target of one result item.
type SearchResultTarget struct {
	Type    string         `json:"type"`
	View    string         `json:"view,omitempty"`
	Command string         `json:"command,omitempty"`
	Args    map[string]any `json:"args,omitempty"`
	URL     string         `json:"url,omitempty"`
	Path    string         `json:"path,omitempty"`
	Line    int            `json:"line,omitempty"`
	Column  int            `json:"column,omitempty"`
}

// SearchResultItem is one validated result row (plain text only).
type SearchResultItem struct {
	ID     string             `json:"id"`
	Title  string             `json:"title"`
	Detail string             `json:"detail,omitempty"`
	Target SearchResultTarget `json:"target"`
}

type searchResponse struct {
	Items     []SearchResultItem `json:"items"`
	Dropped   int                `json:"dropped"`
	Truncated bool               `json:"truncated"`
	// FileScope and ProviderRuntime are host-filled only for a query that
	// could return file targets; legacy responses omit both.
	FileScope       *searchFileScope `json:"fileScope,omitempty"`
	ProviderRuntime *runtimeIdentity `json:"providerRuntime,omitempty"`
}

type searchRawResult struct {
	Items []json.RawMessage `json:"items"`
}

type searchRawItem struct {
	ID     string          `json:"id"`
	Title  string          `json:"title"`
	Detail *string         `json:"detail,omitempty"`
	Target json.RawMessage `json:"target"`
}

type searchRawTarget struct {
	Type    string         `json:"type"`
	View    string         `json:"view,omitempty"`
	Command string         `json:"command,omitempty"`
	Args    map[string]any `json:"args,omitempty"`
	URL     string         `json:"url,omitempty"`
	Path    string         `json:"path,omitempty"`
	Line    *int           `json:"line,omitempty"`
	Column  *int           `json:"column,omitempty"`
}

// searchTargetScope is the plugin generation's views and commands used to
// validate result targets.
type searchTargetScope struct {
	commands map[string]ManifestCommand
	views    map[string]ManifestView
	actions  map[string]ManifestUIAction
	// fileRoot, when set, is the canonical root of the admitted workspace
	// scope: only then are file targets accepted (search providers only).
	fileRoot string
}

func (manager *pluginManager) search(ctx context.Context, request searchRequest) (searchResponse, error) {
	if err := normalizeSearchRequest(&request); err != nil {
		return searchResponse{}, err
	}
	if _, _, ok := enabledContributionPoint(searchProviderPointKey); !ok {
		return searchResponse{}, newContributionError(contributionErrUnavailable, "search providers are not enabled in this host")
	}
	record, err := manager.record(request.PluginID)
	if err != nil {
		return searchResponse{}, newContributionError(contributionErrNotFound, "plugin %q not found", request.PluginID)
	}
	record.mu.Lock()
	provider, lookupErr := searchProviderLocked(record, request.ProviderID)
	var scope searchTargetScope
	if lookupErr == nil {
		scope.commands, scope.views, scope.actions = manifestCommandMaps(record.manifest)
	}
	record.mu.Unlock()
	if lookupErr != nil {
		return searchResponse{}, lookupErr
	}
	if utf8.RuneCountInString(request.Query) < provider.MinQueryLength {
		return searchResponse{}, newContributionError(contributionErrInvalidRequest, "query must be at least %d characters", provider.MinQueryLength)
	}
	encoded, err := encodeContributionJSON(searchPayload{Provider: provider.ID, Query: request.Query, Locale: request.Locale})
	if err != nil {
		return searchResponse{}, newContributionError(contributionErrInvalidRequest, "encode payload: %v", err)
	}
	limit := searchProviderPayloadMaxBytes
	if action, ok := scope.actions[provider.Action]; ok && action.MaxPayloadBytes > 0 && action.MaxPayloadBytes < limit {
		limit = action.MaxPayloadBytes
	}
	if len(encoded) > limit {
		return searchResponse{}, newContributionError(contributionErrTooLarge, "payload is %d bytes, search provider %q allows %d", len(encoded), provider.ID, limit)
	}
	// binding is set by PrepareAdmitted (run synchronously by
	// invokeContributedAction) only for a file-enabled query.
	var binding *searchFileBinding
	defer func() {
		if binding != nil {
			binding.stop()
		}
	}()
	raw, err := manager.invokeContributedAction(ctx, contributedActionCall{
		PluginID: request.PluginID, Point: searchProviderPointKey, EntryID: provider.ID, ActionID: provider.Action,
		RequestID: request.RequestID, Payload: encoded, Timeout: searchProviderTimeout,
		MaxResultBytes: searchProviderResultMaxBytes, Concurrency: searchProviderConcurrency,
		Validate: func(record *pluginRecord) error {
			current, err := searchProviderLocked(record, provider.ID)
			if err != nil {
				return err
			}
			if current.Action != provider.Action || current.Prefix != provider.Prefix {
				return newContributionError(contributionErrUnavailable, "search provider %q changed", provider.ID)
			}
			return nil
		},
		PrepareAdmitted: func(_ context.Context, admission *contributedAdmission) ([]byte, error) {
			var payload []byte
			binding, payload = manager.bindSearchFileScope(admission, request.PluginID, provider.Action,
				searchPayload{Provider: provider.ID, Query: request.Query, Locale: request.Locale}, encoded)
			if hook := searchFileStageHook; hook != nil {
				hook("prepared")
			}
			if err := binding.check(); err != nil {
				return nil, err
			}
			return payload, nil
		},
	})
	if err != nil {
		if scopeErr := binding.check(); scopeErr != nil {
			return searchResponse{}, scopeErr
		}
		return searchResponse{}, err
	}
	if hook := searchFileStageHook; hook != nil {
		hook("invoked")
	}
	if err := binding.check(); err != nil {
		return searchResponse{}, err
	}
	if binding != nil {
		scope.fileRoot = binding.scope.Root
	}
	response, err := parseSearchResult(raw, scope)
	if err != nil {
		return searchResponse{}, newContributionError(contributionErrInvalidResult, "search provider %q: %v", provider.ID, err)
	}
	// Re-check after the filesystem checks: never stamp a stale scope.
	if err := binding.check(); err != nil {
		return searchResponse{}, err
	}
	if binding != nil {
		response.FileScope = &searchFileScope{WorkspaceID: binding.scope.WorkspaceID, Root: binding.scope.Root, Revision: binding.scope.Revision}
		identity := binding.runtime
		response.ProviderRuntime = &identity
	}
	return response, nil
}

func normalizeSearchRequest(request *searchRequest) error {
	invalid := func(format string, args ...any) error {
		return newContributionError(contributionErrInvalidRequest, format, args...)
	}
	if !pluginIDPattern.MatchString(request.PluginID) {
		return invalid("pluginId %q is invalid", request.PluginID)
	}
	if !searchProviderIDPattern.MatchString(request.ProviderID) {
		return invalid("providerId %q is invalid", request.ProviderID)
	}
	if !utf8.ValidString(request.Query) || utf8.RuneCountInString(request.Query) > searchProviderQueryMaxRunes ||
		checkContributionRunes(request.Query, false, false) != nil {
		return invalid("query must be a single line of at most %d characters", searchProviderQueryMaxRunes)
	}
	if request.Locale == "" {
		request.Locale = "en"
	}
	if len(request.Locale) > 35 || !searchLocalePattern.MatchString(request.Locale) {
		return invalid("locale %q is invalid", request.Locale)
	}
	return nil
}

// searchProviderLocked finds the provider in the record's current generation.
// Called with record.mu held.
func searchProviderLocked(record *pluginRecord, providerID string) (SearchProviderContribution, error) {
	if record.manifest != nil {
		if resolved := record.manifest.contribution(searchProviderPointKey); resolved != nil {
			for _, entry := range resolved.Entries {
				if provider, ok := entry.(SearchProviderContribution); ok && provider.ID == providerID {
					return provider, nil
				}
			}
		}
	}
	return SearchProviderContribution{}, newContributionError(contributionErrNotFound, "search provider %q is not contributed by %q", providerID, record.id)
}

// parseSearchResult validates {"items": [...]}. The top level is strict;
// invalid or duplicate items are dropped and counted, items past itemsMax are
// cut and reported as truncated.
func parseSearchResult(raw json.RawMessage, scope searchTargetScope) (searchResponse, error) {
	var result searchRawResult
	if err := decodeStrictJSON(raw, &result, true); err != nil {
		return searchResponse{}, fmt.Errorf("result must be {\"items\": [...]}: %v", err)
	}
	if result.Items == nil {
		return searchResponse{}, errors.New("result must contain items")
	}
	response := searchResponse{Items: []SearchResultItem{}}
	rawItems := result.Items
	if len(rawItems) > searchProviderItemsMax {
		response.Truncated = true
		rawItems = rawItems[:searchProviderItemsMax]
	}
	seen := map[string]bool{}
	for _, rawItem := range rawItems {
		item, err := normalizeSearchResultItem(rawItem, scope)
		if err != nil || seen[item.ID] {
			response.Dropped++
			continue
		}
		seen[item.ID] = true
		response.Items = append(response.Items, item)
	}
	return response, nil
}

func normalizeSearchResultItem(raw json.RawMessage, scope searchTargetScope) (SearchResultItem, error) {
	var item searchRawItem
	if err := decodeStrictJSON(raw, &item, true); err != nil {
		return SearchResultItem{}, err
	}
	if !searchResultItemIDPattern.MatchString(item.ID) {
		return SearchResultItem{}, errors.New("item id is invalid")
	}
	title, err := validateContributionLine(item.Title, searchProviderItemTitleMaxRunes, true)
	if err != nil {
		return SearchResultItem{}, fmt.Errorf("title %w", err)
	}
	out := SearchResultItem{ID: item.ID, Title: title}
	if item.Detail != nil {
		if out.Detail, err = validateContributionLine(*item.Detail, searchProviderItemDetailMaxRunes, false); err != nil {
			return SearchResultItem{}, fmt.Errorf("detail %w", err)
		}
	}
	if len(item.Target) == 0 {
		return SearchResultItem{}, errors.New("target is required")
	}
	if out.Target, err = normalizeSearchResultTarget(item.Target, scope); err != nil {
		return SearchResultItem{}, err
	}
	// The path may not exist yet (shown, but a click needs a regular file);
	// what exists must stay inside the root and away from denied names.
	if out.Target.Type == "file" {
		if err := checkLanguageLocationTarget(scope.fileRoot, out.Target.Path); err != nil {
			return SearchResultItem{}, fmt.Errorf("file target %w", err)
		}
	}
	return out, nil
}

// normalizeSearchResultTarget accepts exactly one of the plugin's own view
// (scalar pass-through args), its own read action command (args only with
// an inputSchema, validated against it), an http(s) URL or, only for a query
// bound to a ready workspace scope (scope.fileRoot), a clean project-relative
// file with an optional 1-based line and column. Filesystem checks of file
// targets happen in normalizeSearchResultItem.
func normalizeSearchResultTarget(raw json.RawMessage, scope searchTargetScope) (SearchResultTarget, error) {
	types := searchProviderLegacyTargetTypes
	if scope.fileRoot != "" {
		types = searchProviderTargetTypes
	}
	target, err := normalizeContributionItemTarget(raw, scope, contributionItemTargetLimits{
		ArgsMaxProperties: searchProviderArgsMaxProperties, ArgsMaxBytes: searchProviderArgsMaxBytes,
		URLMaxLength: searchProviderURLMaxLength, Schemes: searchProviderSchemes, Types: types,
		PathMaxLength: searchProviderPathMaxLength, LineMax: searchProviderLineMax, ColumnMax: searchProviderColumnMax,
	})
	if err == nil && target.Type == "file" {
		err = checkSearchFilePathSyntax(target.Path)
	}
	if err != nil {
		return SearchResultTarget{}, err
	}
	return target, nil
}

// contributionItemTargetLimits bounds normalizeContributionItemTarget. File
// targets are accepted only when Types lists "file"; a column only when
// ColumnMax is set (search providers).
type contributionItemTargetLimits struct {
	ArgsMaxProperties, ArgsMaxBytes, URLMaxLength int
	PathMaxLength, LineMax, ColumnMax             int
	Schemes, Types                                []string
}

// normalizeContributionItemTarget validates the target of a plugin-provided
// item (search result, tree item): the plugin's own view, its own read
// action command, an http(s) URL or, where allowed, a clean project-relative
// file. The frontend runs the target only on explicit activation.
func normalizeContributionItemTarget(raw json.RawMessage, scope searchTargetScope, limits contributionItemTargetLimits) (SearchResultTarget, error) {
	var target searchRawTarget
	if err := decodeStrictJSON(raw, &target, true); err != nil {
		return SearchResultTarget{}, fmt.Errorf("target: %v", err)
	}
	if !slices.Contains(limits.Types, target.Type) {
		return SearchResultTarget{}, fmt.Errorf("target type %q must be one of %s", target.Type, strings.Join(limits.Types, ", "))
	}
	if target.Type != "file" && (target.Path != "" || target.Line != nil || target.Column != nil) {
		return SearchResultTarget{}, fmt.Errorf("%s target has extra fields", target.Type)
	}
	switch target.Type {
	case "view":
		if target.Command != "" || target.URL != "" {
			return SearchResultTarget{}, errors.New("view target has extra fields")
		}
		if _, ok := scope.views[target.View]; target.View == "" || !ok {
			return SearchResultTarget{}, fmt.Errorf("view %q is not one of this plugin's views", target.View)
		}
		if target.Args != nil {
			if err := validateContributionScalarArgs(target.Args, limits.ArgsMaxProperties, limits.ArgsMaxBytes); err != nil {
				return SearchResultTarget{}, err
			}
		}
		return SearchResultTarget{Type: "view", View: target.View, Args: nonEmptyArgs(target.Args)}, nil
	case "command":
		if target.View != "" || target.URL != "" {
			return SearchResultTarget{}, errors.New("command target has extra fields")
		}
		command, err := contributionReadCommand(scope.commands, scope.actions, target.Command)
		if err != nil {
			return SearchResultTarget{}, err
		}
		if len(target.Args) > 0 {
			if command.InputSchema == nil {
				return SearchResultTarget{}, fmt.Errorf("command %q takes no args", command.ID)
			}
			if err := validateContributionScalarArgs(target.Args, limits.ArgsMaxProperties, limits.ArgsMaxBytes); err != nil {
				return SearchResultTarget{}, err
			}
			if err := validateCommandInputValues(command.InputSchema, target.Args); err != nil {
				return SearchResultTarget{}, err
			}
		}
		return SearchResultTarget{Type: "command", Command: command.ID, Args: nonEmptyArgs(target.Args)}, nil
	case "url":
		if target.View != "" || target.Command != "" || target.Args != nil {
			return SearchResultTarget{}, errors.New("url target has extra fields")
		}
		normalized, err := validateContributionHTTPURL(target.URL, limits.URLMaxLength, limits.Schemes)
		if err != nil {
			return SearchResultTarget{}, err
		}
		return SearchResultTarget{Type: "url", URL: normalized}, nil
	case "file":
		if target.View != "" || target.Command != "" || target.URL != "" || target.Args != nil {
			return SearchResultTarget{}, errors.New("file target has extra fields")
		}
		relative, err := validateContributionRelativePath(target.Path, limits.PathMaxLength)
		if err != nil {
			return SearchResultTarget{}, fmt.Errorf("file target path %w", err)
		}
		out := SearchResultTarget{Type: "file", Path: relative}
		if target.Line != nil {
			if *target.Line < 1 || *target.Line > limits.LineMax {
				return SearchResultTarget{}, fmt.Errorf("file target line must be 1-%d", limits.LineMax)
			}
			out.Line = *target.Line
		}
		// Only search providers set ColumnMax; a column needs a line.
		if target.Column != nil {
			if limits.ColumnMax <= 0 {
				return SearchResultTarget{}, errors.New("file target column is not supported here")
			}
			if target.Line == nil || *target.Column < 1 || *target.Column > limits.ColumnMax {
				return SearchResultTarget{}, fmt.Errorf("file target column must be 1-%d and needs a line", limits.ColumnMax)
			}
			out.Column = *target.Column
		}
		return out, nil
	}
	return SearchResultTarget{}, fmt.Errorf("target type %q is not supported", target.Type)
}

// validateContributionRelativePath accepts a clean, slash-separated path
// inside the project ("src/main.go"): no absolute paths, drive letters,
// backslashes, "." or ".." segments, or unsafe characters.
func validateContributionRelativePath(value string, maxLength int) (string, error) {
	if value == "" || len(value) > maxLength || !utf8.ValidString(value) || checkContributionRunes(value, false, false) != nil ||
		strings.ContainsAny(value, "\\:") || strings.HasPrefix(value, "/") || path.Clean(value) != value ||
		value == "." || value == ".." || strings.HasPrefix(value, "../") {
		return "", fmt.Errorf("must be a clean project-relative path of at most %d bytes", maxLength)
	}
	return value, nil
}

func nonEmptyArgs(args map[string]any) map[string]any {
	if len(args) == 0 {
		return nil
	}
	return args
}

func pluginHostSearchHandler(_, args string) (string, error) {
	var request searchRequest
	if err := decodeStrictJSON([]byte(args), &request, true); err != nil {
		return "", newContributionError(contributionErrInvalidRequest, "plugin_host_search: invalid arguments: %v", err)
	}
	response, err := host().search(context.Background(), request)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(response)
}

func init() {
	core.Register("plugin_host_search", pluginHostSearchHandler)
}
