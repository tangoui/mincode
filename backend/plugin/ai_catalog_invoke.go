package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"mincode/app/backend/core"
)

// plugin_host_ai_catalog_search sends one Discover-tab query to a catalog's
// read action and returns validated, plain-text items; the WebView names the
// catalog by reference ({pluginId, catalogId, requestId, query, locale}) and
// the host builds the fixed payload {catalog, kind, query, locale}. Items
// carry display data only (id, name, description, docsUrl); invalid or
// duplicate items are dropped and counted, items past itemsMax are cut.
//
// plugin_host_ai_catalog_install runs only when the user adds an item:
// payload {catalog, kind, id, locale}, result {"content": "..."} of at most
// installResultMaxBytes. The host checks encoding and characters only; the
// frontend parses the content with the same MCP config / SKILL.md parsers it
// uses for pasted or downloaded items and asks the user before saving it as
// ordinary user configuration. The host never fetches a URL the plugin names
// and persists nothing here.
var (
	aiCatalogQueryMaxRunes      = int(aiCatalogSpec.Limit("queryMaxRunes"))
	aiCatalogPayloadMaxBytes    = int(aiCatalogSpec.Limit("payloadMaxBytes"))
	aiCatalogResultMaxBytes     = int(aiCatalogSpec.Limit("resultMaxBytes"))
	aiCatalogItemsMax           = int(aiCatalogSpec.Limit("itemsMax"))
	aiCatalogItemIDMaxLength    = int(aiCatalogSpec.Limit("itemIdMaxLength"))
	aiCatalogItemNameMaxRunes   = int(aiCatalogSpec.Limit("itemNameMaxRunes"))
	aiCatalogItemDescriptionMax = int(aiCatalogSpec.Limit("itemDescriptionMaxRunes"))
	aiCatalogURLMaxLength       = int(aiCatalogSpec.Limit("urlMaxLength"))
	aiCatalogInstallMaxBytes    = int(aiCatalogSpec.Limit("installResultMaxBytes"))
	aiCatalogTimeout            = time.Duration(aiCatalogSpec.Limit("timeoutMs")) * time.Millisecond
	aiCatalogConcurrency        = int(aiCatalogSpec.Limit("concurrentPerPlugin"))
	aiCatalogSchemes            = aiCatalogSpec.List("schemes")

	aiCatalogItemIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/-]*$`)
)

type aiCatalogSearchRequest struct {
	PluginID  string `json:"pluginId"`
	CatalogID string `json:"catalogId"`
	RequestID string `json:"requestId"`
	Query     string `json:"query"`
	Locale    string `json:"locale,omitempty"`
}

type aiCatalogInstallRequest struct {
	PluginID  string `json:"pluginId"`
	CatalogID string `json:"catalogId"`
	RequestID string `json:"requestId"`
	ItemID    string `json:"itemId"`
	Locale    string `json:"locale,omitempty"`
}

type aiCatalogSearchPayload struct {
	Catalog string `json:"catalog"`
	Kind    string `json:"kind"`
	Query   string `json:"query"`
	Locale  string `json:"locale"`
}

type aiCatalogInstallPayload struct {
	Catalog string `json:"catalog"`
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Locale  string `json:"locale"`
}

// AiCatalogItem is one validated catalog row (plain text only).
type AiCatalogItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	DocsURL     string `json:"docsUrl,omitempty"`
}

type aiCatalogSearchResponse struct {
	Kind      string          `json:"kind"`
	Items     []AiCatalogItem `json:"items"`
	Dropped   int             `json:"dropped"`
	Truncated bool            `json:"truncated"`
}

type aiCatalogInstallResponse struct {
	Kind    string `json:"kind"`
	Content string `json:"content"`
}

type aiCatalogRawSearchResult struct {
	Items []json.RawMessage `json:"items"`
}

type aiCatalogRawItem struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
	DocsURL     *string `json:"docsUrl,omitempty"`
}

type aiCatalogRawInstallResult struct {
	Content *string `json:"content"`
}

func (manager *pluginManager) aiCatalogSearch(ctx context.Context, request aiCatalogSearchRequest) (aiCatalogSearchResponse, error) {
	if err := normalizeAiCatalogSearchRequest(&request); err != nil {
		return aiCatalogSearchResponse{}, err
	}
	catalog, actionLimit, err := manager.aiCatalogFor(request.PluginID, request.CatalogID)
	if err != nil {
		return aiCatalogSearchResponse{}, err
	}
	payload := aiCatalogSearchPayload{Catalog: catalog.ID, Kind: catalog.Kind, Query: request.Query, Locale: request.Locale}
	raw, err := manager.invokeAiCatalogAction(ctx, request.PluginID, request.RequestID, catalog, catalog.SearchAction, actionLimit(catalog.SearchAction), payload, aiCatalogResultMaxBytes)
	if err != nil {
		return aiCatalogSearchResponse{}, err
	}
	response, err := parseAiCatalogSearchResult(raw)
	if err != nil {
		return aiCatalogSearchResponse{}, newContributionError(contributionErrInvalidResult, "AI catalog %q: %v", catalog.ID, err)
	}
	response.Kind = catalog.Kind
	return response, nil
}

func (manager *pluginManager) aiCatalogInstall(ctx context.Context, request aiCatalogInstallRequest) (aiCatalogInstallResponse, error) {
	if err := normalizeAiCatalogInstallRequest(&request); err != nil {
		return aiCatalogInstallResponse{}, err
	}
	catalog, actionLimit, err := manager.aiCatalogFor(request.PluginID, request.CatalogID)
	if err != nil {
		return aiCatalogInstallResponse{}, err
	}
	payload := aiCatalogInstallPayload{Catalog: catalog.ID, Kind: catalog.Kind, ID: request.ItemID, Locale: request.Locale}
	// JSON escaping adds overhead: the transport keeps its default 1 MiB
	// message bound, and the decoded content has a separate 512 KiB limit.
	raw, err := manager.invokeAiCatalogAction(ctx, request.PluginID, request.RequestID, catalog, catalog.InstallAction, actionLimit(catalog.InstallAction), payload, contributionResultDefaultMaxBytes)
	if err != nil {
		return aiCatalogInstallResponse{}, err
	}
	content, err := parseAiCatalogInstallResult(raw)
	if err != nil {
		return aiCatalogInstallResponse{}, newContributionError(contributionErrInvalidResult, "AI catalog %q item %q: %v", catalog.ID, request.ItemID, err)
	}
	return aiCatalogInstallResponse{Kind: catalog.Kind, Content: content}, nil
}

// aiCatalogFor resolves the catalog in the plugin's current generation and
// returns a lookup of each action's payload limit.
func (manager *pluginManager) aiCatalogFor(pluginID, catalogID string) (AiCatalogContribution, func(string) int, error) {
	if _, _, ok := enabledContributionPoint(aiCatalogPointKey); !ok {
		return AiCatalogContribution{}, nil, newContributionError(contributionErrUnavailable, "AI catalogs are not enabled in this host")
	}
	record, err := manager.record(pluginID)
	if err != nil {
		return AiCatalogContribution{}, nil, newContributionError(contributionErrNotFound, "plugin %q not found", pluginID)
	}
	record.mu.Lock()
	catalog, lookupErr := aiCatalogLocked(record, catalogID)
	var actions map[string]ManifestUIAction
	if lookupErr == nil {
		_, _, actions = manifestCommandMaps(record.manifest)
	}
	record.mu.Unlock()
	if lookupErr != nil {
		return AiCatalogContribution{}, nil, lookupErr
	}
	return catalog, func(actionID string) int {
		limit := aiCatalogPayloadMaxBytes
		if action, ok := actions[actionID]; ok && action.MaxPayloadBytes > 0 && action.MaxPayloadBytes < limit {
			limit = action.MaxPayloadBytes
		}
		return limit
	}, nil
}

func (manager *pluginManager) invokeAiCatalogAction(ctx context.Context, pluginID, requestID string, catalog AiCatalogContribution, actionID string, payloadLimit int, payload any, maxResult int) (json.RawMessage, error) {
	encoded, err := encodeContributionJSON(payload)
	if err != nil {
		return nil, newContributionError(contributionErrInvalidRequest, "encode payload: %v", err)
	}
	if len(encoded) > payloadLimit {
		return nil, newContributionError(contributionErrTooLarge, "payload is %d bytes, AI catalog %q allows %d", len(encoded), catalog.ID, payloadLimit)
	}
	return manager.invokeContributedAction(ctx, contributedActionCall{
		PluginID: pluginID, Point: aiCatalogPointKey, EntryID: catalog.ID, ActionID: actionID,
		RequestID: requestID, Payload: encoded, Timeout: aiCatalogTimeout,
		MaxResultBytes: maxResult, Concurrency: aiCatalogConcurrency,
		Validate: func(record *pluginRecord) error {
			current, err := aiCatalogLocked(record, catalog.ID)
			if err != nil {
				return err
			}
			if current.Kind != catalog.Kind || current.SearchAction != catalog.SearchAction || current.InstallAction != catalog.InstallAction {
				return newContributionError(contributionErrUnavailable, "AI catalog %q changed", catalog.ID)
			}
			return nil
		},
	})
}

func normalizeAiCatalogSearchRequest(request *aiCatalogSearchRequest) error {
	if err := normalizeAiCatalogTarget(request.PluginID, request.CatalogID, &request.Locale); err != nil {
		return err
	}
	if !utf8.ValidString(request.Query) || utf8.RuneCountInString(request.Query) > aiCatalogQueryMaxRunes ||
		checkContributionRunes(request.Query, false, false) != nil {
		return newContributionError(contributionErrInvalidRequest, "query must be a single line of at most %d characters", aiCatalogQueryMaxRunes)
	}
	request.Query = strings.TrimSpace(request.Query)
	return nil
}

func normalizeAiCatalogInstallRequest(request *aiCatalogInstallRequest) error {
	if err := normalizeAiCatalogTarget(request.PluginID, request.CatalogID, &request.Locale); err != nil {
		return err
	}
	if !validAiCatalogItemID(request.ItemID) {
		return newContributionError(contributionErrInvalidRequest, "itemId %q is invalid", request.ItemID)
	}
	return nil
}

func normalizeAiCatalogTarget(pluginID, catalogID string, locale *string) error {
	if !pluginIDPattern.MatchString(pluginID) {
		return newContributionError(contributionErrInvalidRequest, "pluginId %q is invalid", pluginID)
	}
	if !aiCatalogIDPattern.MatchString(catalogID) {
		return newContributionError(contributionErrInvalidRequest, "catalogId %q is invalid", catalogID)
	}
	if *locale == "" {
		*locale = "en"
	}
	if len(*locale) > 35 || !searchLocalePattern.MatchString(*locale) {
		return newContributionError(contributionErrInvalidRequest, "locale %q is invalid", *locale)
	}
	return nil
}

func validAiCatalogItemID(id string) bool {
	return len(id) <= aiCatalogItemIDMaxLength && aiCatalogItemIDPattern.MatchString(id)
}

// aiCatalogLocked finds the catalog in the record's current generation.
// Called with record.mu held.
func aiCatalogLocked(record *pluginRecord, catalogID string) (AiCatalogContribution, error) {
	if record.manifest != nil {
		if resolved := record.manifest.contribution(aiCatalogPointKey); resolved != nil {
			for _, entry := range resolved.Entries {
				if catalog, ok := entry.(AiCatalogContribution); ok && catalog.ID == catalogID {
					return catalog, nil
				}
			}
		}
	}
	return AiCatalogContribution{}, newContributionError(contributionErrNotFound, "AI catalog %q is not contributed by %q", catalogID, record.id)
}

// parseAiCatalogSearchResult validates {"items": [...]}. The top level is
// strict; invalid or duplicate items are dropped and counted, items past
// itemsMax are cut and reported as truncated.
func parseAiCatalogSearchResult(raw json.RawMessage) (aiCatalogSearchResponse, error) {
	if !utf8.Valid(raw) {
		return aiCatalogSearchResponse{}, errors.New("result must be valid UTF-8")
	}
	var result aiCatalogRawSearchResult
	if err := decodeStrictJSON(raw, &result, true); err != nil {
		return aiCatalogSearchResponse{}, fmt.Errorf("result must be {\"items\": [...]}: %v", err)
	}
	if result.Items == nil {
		return aiCatalogSearchResponse{}, errors.New("result must contain items")
	}
	response := aiCatalogSearchResponse{Items: []AiCatalogItem{}}
	rawItems := result.Items
	if len(rawItems) > aiCatalogItemsMax {
		response.Truncated = true
		rawItems = rawItems[:aiCatalogItemsMax]
	}
	seen := map[string]bool{}
	for _, rawItem := range rawItems {
		item, err := normalizeAiCatalogItem(rawItem)
		if err != nil || seen[item.ID] {
			response.Dropped++
			continue
		}
		seen[item.ID] = true
		response.Items = append(response.Items, item)
	}
	return response, nil
}

func normalizeAiCatalogItem(raw json.RawMessage) (AiCatalogItem, error) {
	var item aiCatalogRawItem
	if err := decodeStrictJSON(raw, &item, true); err != nil {
		return AiCatalogItem{}, err
	}
	if !validAiCatalogItemID(item.ID) {
		return AiCatalogItem{}, errors.New("item id is invalid")
	}
	name, err := validateContributionLine(item.Name, aiCatalogItemNameMaxRunes, true)
	if err != nil {
		return AiCatalogItem{}, fmt.Errorf("name %w", err)
	}
	out := AiCatalogItem{ID: item.ID, Name: name}
	if item.Description != nil {
		if out.Description, err = validateContributionLine(*item.Description, aiCatalogItemDescriptionMax, false); err != nil {
			return AiCatalogItem{}, fmt.Errorf("description %w", err)
		}
	}
	if item.DocsURL != nil {
		if out.DocsURL, err = validateContributionHTTPURL(*item.DocsURL, aiCatalogURLMaxLength, aiCatalogSchemes); err != nil {
			return AiCatalogItem{}, fmt.Errorf("docsUrl: %w", err)
		}
	}
	return out, nil
}

// parseAiCatalogInstallResult validates {"content": "..."}: non-blank UTF-8
// text of at most installResultMaxBytes with no control, bidi or zero-width
// characters (CRLF is normalized to LF).
func parseAiCatalogInstallResult(raw json.RawMessage) (string, error) {
	// encoding/json silently replaces invalid UTF-8, so check the raw bytes.
	if !utf8.Valid(raw) {
		return "", errors.New("result must be valid UTF-8")
	}
	var result aiCatalogRawInstallResult
	if err := decodeStrictJSON(raw, &result, true); err != nil {
		return "", fmt.Errorf("result must be {\"content\": \"...\"}: %v", err)
	}
	if result.Content == nil {
		return "", errors.New("result must contain content")
	}
	content, err := validateContributionMultiline(*result.Content, 0, aiCatalogInstallMaxBytes)
	if err != nil {
		return "", fmt.Errorf("content %w", err)
	}
	if strings.TrimSpace(content) == "" {
		return "", errors.New("content is empty")
	}
	return content, nil
}

func pluginHostAiCatalogSearchHandler(_, args string) (string, error) {
	var request aiCatalogSearchRequest
	if err := decodeStrictJSON([]byte(args), &request, true); err != nil {
		return "", newContributionError(contributionErrInvalidRequest, "plugin_host_ai_catalog_search: invalid arguments: %v", err)
	}
	response, err := host().aiCatalogSearch(context.Background(), request)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(response)
}

func pluginHostAiCatalogInstallHandler(_, args string) (string, error) {
	var request aiCatalogInstallRequest
	if err := decodeStrictJSON([]byte(args), &request, true); err != nil {
		return "", newContributionError(contributionErrInvalidRequest, "plugin_host_ai_catalog_install: invalid arguments: %v", err)
	}
	response, err := host().aiCatalogInstall(context.Background(), request)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(response)
}

func init() {
	core.Register("plugin_host_ai_catalog_search", pluginHostAiCatalogSearchHandler)
	core.Register("plugin_host_ai_catalog_install", pluginHostAiCatalogInstallHandler)
}
