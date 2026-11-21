package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"mincode/app/backend/core"
)

// plugin_host_dashboard_widget_data fetches one data widget's content. The
// WebView names the widget only by reference ({pluginId, widgetId,
// requestId}); the host looks up the widget's declared read action, sends the
// fixed payload {"widget": id} and validates the result into a small
// plain-text DTO. Nothing is persisted.

type dashboardWidgetDataRequest struct {
	PluginID  string `json:"pluginId"`
	WidgetID  string `json:"widgetId"`
	RequestID string `json:"requestId"`
}

// DashboardWidgetDataItem is one label/value row of a data widget.
type DashboardWidgetDataItem struct {
	Label    string `json:"label"`
	Value    string `json:"value"`
	Severity string `json:"severity"`
	Hint     string `json:"hint,omitempty"`
}

// DashboardWidgetData is the normalized data widget result.
type DashboardWidgetData struct {
	Items     []DashboardWidgetDataItem `json:"items"`
	Footnote  string                    `json:"footnote,omitempty"`
	UpdatedAt string                    `json:"updatedAt,omitempty"`
}

type dashboardWidgetRawItem struct {
	Label    *string `json:"label"`
	Value    any     `json:"value"`
	Severity *string `json:"severity"`
	Hint     *string `json:"hint"`
}

type dashboardWidgetRawResult struct {
	Items     []json.RawMessage `json:"items"`
	Footnote  *string           `json:"footnote"`
	UpdatedAt *string           `json:"updatedAt"`
}

func (manager *pluginManager) dashboardWidgetData(ctx context.Context, request dashboardWidgetDataRequest) (DashboardWidgetData, error) {
	pluginID := strings.TrimSpace(request.PluginID)
	widgetID := strings.TrimSpace(request.WidgetID)
	if pluginID == "" || !dashboardWidgetIDPattern.MatchString(widgetID) {
		return DashboardWidgetData{}, newContributionError(contributionErrInvalidRequest, "pluginId and a valid widgetId are required")
	}
	if _, _, ok := enabledContributionPoint(dashboardWidgetPointKey); !ok {
		return DashboardWidgetData{}, newContributionError(contributionErrUnavailable, "dashboard widgets are not enabled in this host")
	}
	record, err := manager.record(pluginID)
	if err != nil {
		return DashboardWidgetData{}, newContributionError(contributionErrNotFound, "plugin %q not found", pluginID)
	}
	record.mu.Lock()
	widget, ok := dashboardWidgetLocked(record, widgetID)
	record.mu.Unlock()
	if !ok {
		return DashboardWidgetData{}, newContributionError(contributionErrNotFound, "plugin %q has no dashboard widget %q", pluginID, widgetID)
	}
	if widget.Kind != dashboardWidgetKindData {
		return DashboardWidgetData{}, newContributionError(contributionErrForbidden, "dashboard widget %q is not a data widget", widgetID)
	}
	payload, err := json.Marshal(map[string]string{"widget": widgetID})
	if err != nil || len(payload) > dashboardWidgetPayloadMaxBytes {
		return DashboardWidgetData{}, newContributionError(contributionErrTooLarge, "dashboard widget payload exceeds %d bytes", dashboardWidgetPayloadMaxBytes)
	}
	action := widget.Action
	raw, err := manager.invokeContributedAction(ctx, contributedActionCall{
		PluginID:       pluginID,
		Point:          dashboardWidgetPointKey,
		EntryID:        widgetID,
		ActionID:       action,
		RequestID:      request.RequestID,
		Payload:        payload,
		Timeout:        dashboardWidgetTimeout,
		MaxResultBytes: dashboardWidgetResultMaxBytes,
		Concurrency:    dashboardWidgetConcurrency,
		Validate: func(record *pluginRecord) error {
			current, ok := dashboardWidgetLocked(record, widgetID)
			if !ok || current.Kind != dashboardWidgetKindData || current.Action != action {
				return errDashboardWidgetGone
			}
			return nil
		},
	})
	if err != nil {
		return DashboardWidgetData{}, err
	}
	data, err := normalizeDashboardWidgetData(raw)
	if err != nil {
		return DashboardWidgetData{}, newContributionError(contributionErrInvalidResult, "dashboard widget %q: %v", widgetID, err)
	}
	return data, nil
}

// normalizeDashboardWidgetData validates a data widget result: at most
// itemsMax single-line label/value rows with a known severity, an optional
// footnote and updatedAt. Numbers and booleans in value are stringified;
// unknown fields are ignored.
func normalizeDashboardWidgetData(raw json.RawMessage) (DashboardWidgetData, error) {
	if len(raw) > dashboardWidgetResultMaxBytes {
		return DashboardWidgetData{}, fmt.Errorf("result exceeds %d bytes", dashboardWidgetResultMaxBytes)
	}
	var result dashboardWidgetRawResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return DashboardWidgetData{}, errors.New("result must be an object with an items array")
	}
	if result.Items == nil {
		return DashboardWidgetData{}, errors.New("result must contain items")
	}
	if len(result.Items) > dashboardWidgetItemsMax {
		return DashboardWidgetData{}, fmt.Errorf("result must have at most %d items", dashboardWidgetItemsMax)
	}
	data := DashboardWidgetData{Items: make([]DashboardWidgetDataItem, 0, len(result.Items))}
	for index, rawItem := range result.Items {
		var item dashboardWidgetRawItem
		if err := json.Unmarshal(rawItem, &item); err != nil {
			return DashboardWidgetData{}, fmt.Errorf("items[%d] must be an object", index)
		}
		normalized, err := normalizeDashboardWidgetItem(item)
		if err != nil {
			return DashboardWidgetData{}, fmt.Errorf("items[%d] %w", index, err)
		}
		data.Items = append(data.Items, normalized)
	}
	var err error
	if result.Footnote != nil {
		if data.Footnote, err = validateContributionLine(*result.Footnote, dashboardWidgetFootnoteMaxRunes, false); err != nil {
			return DashboardWidgetData{}, fmt.Errorf("footnote %w", err)
		}
	}
	if result.UpdatedAt != nil {
		if data.UpdatedAt, err = validateContributionLine(*result.UpdatedAt, 64, false); err != nil {
			return DashboardWidgetData{}, fmt.Errorf("updatedAt %w", err)
		}
	}
	return data, nil
}

func normalizeDashboardWidgetItem(item dashboardWidgetRawItem) (DashboardWidgetDataItem, error) {
	var out DashboardWidgetDataItem
	var err error
	if item.Label == nil {
		return out, errors.New("label is required")
	}
	if out.Label, err = validateContributionLine(*item.Label, dashboardWidgetLabelMaxRunes, true); err != nil {
		return out, fmt.Errorf("label %w", err)
	}
	var value string
	switch typed := item.Value.(type) {
	case string:
		value = typed
	case float64:
		encoded, _ := json.Marshal(typed)
		value = string(encoded)
	case bool:
		value = fmt.Sprint(typed)
	default:
		return out, errors.New("value must be a string, number or boolean")
	}
	if out.Value, err = validateContributionLine(value, dashboardWidgetValueMaxRunes, false); err != nil {
		return out, fmt.Errorf("value %w", err)
	}
	out.Severity = "normal"
	if item.Severity != nil {
		severity := strings.TrimSpace(*item.Severity)
		if !dashboardWidgetSeverities[severity] {
			return out, fmt.Errorf("severity must be one of %s", strings.Join(dashboardWidgetSpec.List("severities"), ", "))
		}
		out.Severity = severity
	}
	if item.Hint != nil {
		if out.Hint, err = validateContributionLine(*item.Hint, dashboardWidgetHintMaxRunes, false); err != nil {
			return out, fmt.Errorf("hint %w", err)
		}
	}
	return out, nil
}

func pluginHostDashboardWidgetDataHandler(_, args string) (string, error) {
	var request dashboardWidgetDataRequest
	if err := core.DecodeArgsPayload(args, &request); err != nil {
		return "", newContributionError(contributionErrInvalidRequest, "plugin_host_dashboard_widget_data: %v", err)
	}
	data, err := host().dashboardWidgetData(context.Background(), request)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(data)
}

func init() {
	core.Register("plugin_host_dashboard_widget_data", pluginHostDashboardWidgetDataHandler)
}
