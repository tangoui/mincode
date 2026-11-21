package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"mincode/app/backend/core"
	"mincode/app/plugins/api"
)

// plugin_host_tree_view loads one level of a contributed sidebar tree view.
// The WebView names the view by reference ({pluginId, viewId, requestId,
// parentId?, depth, projectRoot?, locale}); the host builds the fixed payload
// {view, parent, locale, project?} where project.root is sent only with
// workspace.read. Bounds: payload ≤ min(action maxPayloadBytes,
// payloadMaxBytes), timeoutMs, concurrentPerPlugin slots, result ≤
// resultMaxBytes and itemsMax items, nesting ≤ depthMax. Invalid or duplicate
// items are dropped and counted. Item targets are the plugin's own views, its
// own read action commands, http(s) URLs and clean project-relative files,
// which the frontend runs only on explicit activation.
var (
	treeViewPayloadMaxBytes  = int(treeViewSpec.Limit("payloadMaxBytes"))
	treeViewResultMaxBytes   = int(treeViewSpec.Limit("resultMaxBytes"))
	treeViewItemsMax         = int(treeViewSpec.Limit("itemsMax"))
	treeViewItemIDMaxLength  = int(treeViewSpec.Limit("itemIdMaxLength"))
	treeViewLabelMaxRunes    = int(treeViewSpec.Limit("labelMaxRunes"))
	treeViewDescriptionMax   = int(treeViewSpec.Limit("descriptionMaxRunes"))
	treeViewTooltipMaxRunes  = int(treeViewSpec.Limit("tooltipMaxRunes"))
	treeViewDepthMax         = int(treeViewSpec.Limit("depthMax"))
	treeViewTimeout          = time.Duration(treeViewSpec.Limit("timeoutMs")) * time.Millisecond
	treeViewConcurrency      = int(treeViewSpec.Limit("concurrentPerPlugin"))
	treeViewIcons            = contributionListSet(treeViewSpec.List("icons"))
	treeViewItemTargetLimits = contributionItemTargetLimits{
		ArgsMaxProperties: int(treeViewSpec.Limit("argsMaxProperties")), ArgsMaxBytes: int(treeViewSpec.Limit("argsMaxBytes")),
		URLMaxLength: int(treeViewSpec.Limit("urlMaxLength")), PathMaxLength: int(treeViewSpec.Limit("pathMaxLength")),
		LineMax: int(treeViewSpec.Limit("lineMax")), Schemes: treeViewSpec.List("schemes"), Types: treeViewSpec.List("targetTypes"),
	}
)

type treeViewRequest struct {
	PluginID    string `json:"pluginId"`
	ViewID      string `json:"viewId"`
	RequestID   string `json:"requestId"`
	ParentID    string `json:"parentId,omitempty"`
	Depth       int    `json:"depth"`
	ProjectRoot string `json:"projectRoot,omitempty"`
	Locale      string `json:"locale,omitempty"`
}

type treeViewPayload struct {
	View    string               `json:"view"`
	Parent  *string              `json:"parent"`
	Locale  string               `json:"locale"`
	Project *contributionProject `json:"project,omitempty"`
}

// TreeViewItem is one validated tree row (plain text only).
type TreeViewItem struct {
	ID          string              `json:"id"`
	Label       string              `json:"label"`
	Description string              `json:"description,omitempty"`
	Tooltip     string              `json:"tooltip,omitempty"`
	Icon        string              `json:"icon,omitempty"`
	Collapsible bool                `json:"collapsible,omitempty"`
	Target      *SearchResultTarget `json:"target,omitempty"`
}

type treeViewResponse struct {
	Items     []TreeViewItem `json:"items"`
	Dropped   int            `json:"dropped"`
	Truncated bool           `json:"truncated"`
}

type treeViewRawResult struct {
	Items []json.RawMessage `json:"items"`
}

type treeViewRawItem struct {
	ID          string          `json:"id"`
	Label       string          `json:"label"`
	Description *string         `json:"description,omitempty"`
	Tooltip     *string         `json:"tooltip,omitempty"`
	Icon        *string         `json:"icon,omitempty"`
	Collapsible *bool           `json:"collapsible,omitempty"`
	Target      json.RawMessage `json:"target,omitempty"`
}

func (manager *pluginManager) treeViewChildren(ctx context.Context, request treeViewRequest) (treeViewResponse, error) {
	if err := normalizeTreeViewRequest(&request); err != nil {
		return treeViewResponse{}, err
	}
	if _, _, ok := enabledContributionPoint(treeViewPointKey); !ok {
		return treeViewResponse{}, newContributionError(contributionErrUnavailable, "tree views are not enabled in this host")
	}
	record, err := manager.record(request.PluginID)
	if err != nil {
		return treeViewResponse{}, newContributionError(contributionErrNotFound, "plugin %q not found", request.PluginID)
	}
	record.mu.Lock()
	view, lookupErr := treeViewLocked(record, request.ViewID)
	var scope searchTargetScope
	workspaceRead := false
	if lookupErr == nil {
		scope.commands, scope.views, scope.actions = manifestCommandMaps(record.manifest)
		workspaceRead = manifestHasCapability(record.manifest, api.CapabilityWorkspaceRead)
	}
	record.mu.Unlock()
	if lookupErr != nil {
		return treeViewResponse{}, lookupErr
	}
	payload := treeViewPayload{View: view.ID, Locale: request.Locale}
	if request.ParentID != "" {
		parent := request.ParentID
		payload.Parent = &parent
	}
	if request.ProjectRoot != "" {
		if payload.Project, err = contributionProjectFor(request.ProjectRoot, workspaceRead); err != nil {
			return treeViewResponse{}, err
		}
	}
	encoded, err := encodeContributionJSON(payload)
	if err != nil {
		return treeViewResponse{}, newContributionError(contributionErrInvalidRequest, "encode payload: %v", err)
	}
	limit := treeViewPayloadMaxBytes
	if action, ok := scope.actions[view.Action]; ok && action.MaxPayloadBytes > 0 && action.MaxPayloadBytes < limit {
		limit = action.MaxPayloadBytes
	}
	if len(encoded) > limit {
		return treeViewResponse{}, newContributionError(contributionErrTooLarge, "payload is %d bytes, tree view %q allows %d", len(encoded), view.ID, limit)
	}
	raw, err := manager.invokeContributedAction(ctx, contributedActionCall{
		PluginID: request.PluginID, Point: treeViewPointKey, EntryID: view.ID, ActionID: view.Action,
		RequestID: request.RequestID, Payload: encoded, Timeout: treeViewTimeout,
		MaxResultBytes: treeViewResultMaxBytes, Concurrency: treeViewConcurrency,
		Validate: func(record *pluginRecord) error {
			current, err := treeViewLocked(record, view.ID)
			if err != nil {
				return err
			}
			if current.Action != view.Action {
				return newContributionError(contributionErrUnavailable, "tree view %q changed", view.ID)
			}
			return nil
		},
	})
	if err != nil {
		return treeViewResponse{}, err
	}
	// Items at the deepest level cannot be expanded further.
	leafOnly := request.Depth+1 >= treeViewDepthMax
	response, err := parseTreeViewResult(raw, scope, payload.Project != nil, leafOnly)
	if err != nil {
		return treeViewResponse{}, newContributionError(contributionErrInvalidResult, "tree view %q: %v", view.ID, err)
	}
	return response, nil
}

func normalizeTreeViewRequest(request *treeViewRequest) error {
	invalid := func(format string, args ...any) error {
		return newContributionError(contributionErrInvalidRequest, format, args...)
	}
	if !pluginIDPattern.MatchString(request.PluginID) {
		return invalid("pluginId %q is invalid", request.PluginID)
	}
	if !treeViewIDPattern.MatchString(request.ViewID) {
		return invalid("viewId %q is invalid", request.ViewID)
	}
	if request.ParentID == "" {
		if request.Depth != 0 {
			return invalid("depth must be 0 for the root level")
		}
	} else {
		if !validateContributionItemID(request.ParentID, treeViewItemIDMaxLength) {
			return invalid("parentId is invalid")
		}
		if request.Depth < 1 || request.Depth >= treeViewDepthMax {
			return invalid("depth must be 1-%d", treeViewDepthMax-1)
		}
	}
	if request.Locale == "" {
		request.Locale = "en"
	}
	if len(request.Locale) > 35 || !searchLocalePattern.MatchString(request.Locale) {
		return invalid("locale %q is invalid", request.Locale)
	}
	return nil
}

// treeViewLocked finds the view in the record's current generation. Called
// with record.mu held.
func treeViewLocked(record *pluginRecord, viewID string) (TreeViewContribution, error) {
	if record.manifest != nil {
		if resolved := record.manifest.contribution(treeViewPointKey); resolved != nil {
			for _, entry := range resolved.Entries {
				if view, ok := entry.(TreeViewContribution); ok && view.ID == viewID {
					return view, nil
				}
			}
		}
	}
	return TreeViewContribution{}, newContributionError(contributionErrNotFound, "tree view %q is not contributed by %q", viewID, record.id)
}

// parseTreeViewResult validates {"items": [...]}. The top level is strict;
// invalid or duplicate items are dropped and counted, items past itemsMax
// are cut and reported as truncated. File targets need a project.
func parseTreeViewResult(raw json.RawMessage, scope searchTargetScope, hasProject, leafOnly bool) (treeViewResponse, error) {
	var result treeViewRawResult
	if err := decodeStrictJSON(raw, &result, true); err != nil {
		return treeViewResponse{}, fmt.Errorf("result must be {\"items\": [...]}: %v", err)
	}
	if result.Items == nil {
		return treeViewResponse{}, errors.New("result must contain items")
	}
	response := treeViewResponse{Items: []TreeViewItem{}}
	rawItems := result.Items
	if len(rawItems) > treeViewItemsMax {
		response.Truncated = true
		rawItems = rawItems[:treeViewItemsMax]
	}
	seen := map[string]bool{}
	for _, rawItem := range rawItems {
		item, err := normalizeTreeViewItem(rawItem, scope, hasProject)
		if err != nil || seen[item.ID] {
			response.Dropped++
			continue
		}
		if leafOnly {
			item.Collapsible = false
		}
		seen[item.ID] = true
		response.Items = append(response.Items, item)
	}
	return response, nil
}

func normalizeTreeViewItem(raw json.RawMessage, scope searchTargetScope, hasProject bool) (TreeViewItem, error) {
	var item treeViewRawItem
	if err := decodeStrictJSON(raw, &item, true); err != nil {
		return TreeViewItem{}, err
	}
	if !validateContributionItemID(item.ID, treeViewItemIDMaxLength) {
		return TreeViewItem{}, errors.New("item id is invalid")
	}
	label, err := validateContributionLine(item.Label, treeViewLabelMaxRunes, true)
	if err != nil {
		return TreeViewItem{}, fmt.Errorf("label %w", err)
	}
	out := TreeViewItem{ID: item.ID, Label: label}
	if item.Description != nil {
		if out.Description, err = validateContributionLine(*item.Description, treeViewDescriptionMax, false); err != nil {
			return TreeViewItem{}, fmt.Errorf("description %w", err)
		}
	}
	if item.Tooltip != nil {
		if out.Tooltip, err = validateContributionMultiline(*item.Tooltip, treeViewTooltipMaxRunes, 0); err != nil {
			return TreeViewItem{}, fmt.Errorf("tooltip %w", err)
		}
	}
	if item.Icon != nil {
		if !treeViewIcons[*item.Icon] {
			return TreeViewItem{}, fmt.Errorf("icon %q is not a known icon", *item.Icon)
		}
		out.Icon = *item.Icon
	}
	if item.Collapsible != nil {
		out.Collapsible = *item.Collapsible
	}
	if len(item.Target) > 0 && string(item.Target) != "null" {
		target, err := normalizeContributionItemTarget(item.Target, scope, treeViewItemTargetLimits)
		if err != nil {
			return TreeViewItem{}, err
		}
		if target.Type == "file" && !hasProject {
			return TreeViewItem{}, errors.New("file targets need a project")
		}
		out.Target = &target
	}
	return out, nil
}

func pluginHostTreeViewHandler(_, args string) (string, error) {
	var request treeViewRequest
	if err := decodeStrictJSON([]byte(args), &request, true); err != nil {
		return "", newContributionError(contributionErrInvalidRequest, "plugin_host_tree_view: invalid arguments: %v", err)
	}
	response, err := host().treeViewChildren(context.Background(), request)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(response)
}

func init() {
	core.Register("plugin_host_tree_view", pluginHostTreeViewHandler)
}
