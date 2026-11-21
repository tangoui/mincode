package plugin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func writeDashboardWidgetPackage(t *testing.T, runtimeType string, widgets []any) string {
	t.Helper()
	return writeKeymapPackage(t, "", func(manifest map[string]any) {
		manifest["id"] = "acme-dash"
		contributes := map[string]any{"dashboardWidgets": widgets}
		if runtimeType == "process" {
			manifest["runtime"] = map[string]any{"type": "process", "entry": "bin/plugin.mjs"}
			manifest["capabilities"] = []string{"commands", "ui"}
			contributes["commands"] = []any{map[string]any{"id": "acme.open", "title": "Open Acme", "action": "open"}}
			contributes["views"] = []any{map[string]any{"id": "widget", "title": "Widget", "entry": "ui/widget.html"}}
			contributes["actions"] = []any{
				map[string]any{"id": "dashboard.summary", "risk": "read", "maxPayloadBytes": 1024},
				map[string]any{"id": "dashboard.reset", "risk": "write"},
			}
		}
		manifest["contributes"] = contributes
	}, nil, map[string]string{"bin/plugin.mjs": "process.stdin.resume();\n", "ui/widget.html": "<!doctype html><title>w</title>"})
}

func dataWidget() map[string]any {
	return map[string]any{"id": "engine", "title": "Engine", "kind": "data", "action": "dashboard.summary", "refreshSeconds": 120, "command": "acme.open"}
}

func viewWidget() map[string]any {
	return map[string]any{"id": "live", "title": "Live", "kind": "view", "view": "widget", "size": "medium"}
}

func TestDashboardWidgetsSnapshot(t *testing.T) {
	useTemporaryPluginData(t)
	manual := dataWidget()
	manual["id"] = "manual"
	manual["refreshSeconds"] = 0
	defaults := dataWidget()
	defaults["id"] = "defaults"
	delete(defaults, "refreshSeconds")
	delete(defaults, "command")
	root := writeDashboardWidgetPackage(t, "process", []any{dataWidget(), viewWidget(), manual, defaults})
	manifest, err := readManifestStrict(root)
	if err != nil {
		t.Fatal(err)
	}
	entries := manifest.contribution(dashboardWidgetPointKey).Entries
	if len(entries) != 4 {
		t.Fatalf("entries = %+v", entries)
	}
	data := entries[0].(DashboardWidgetContribution)
	if data.Kind != "data" || data.Action != "dashboard.summary" || data.RefreshSeconds != 120 || data.Size != "small" || data.Command != "acme.open" {
		t.Fatalf("data = %+v", data)
	}
	view := entries[1].(DashboardWidgetContribution)
	if view.View != "widget" || view.Size != "medium" || view.RefreshSeconds != 0 || view.Action != "" {
		t.Fatalf("view = %+v", view)
	}
	if got := entries[2].(DashboardWidgetContribution).RefreshSeconds; got != 0 {
		t.Fatalf("manual refresh = %d", got)
	}
	if got := entries[3].(DashboardWidgetContribution).RefreshSeconds; got != 300 {
		t.Fatalf("default refresh = %d", got)
	}
}

func TestDashboardWidgetsRejectInvalidEntries(t *testing.T) {
	useTemporaryPluginData(t)
	with := func(base map[string]any, mutate func(map[string]any)) map[string]any {
		mutate(base)
		return base
	}
	cases := []struct {
		name   string
		widget map[string]any
		want   string
	}{
		{"unknown view", with(viewWidget(), func(w map[string]any) { w["view"] = "missing" }), "not one of this plugin's contributes.views"},
		{"write action", with(dataWidget(), func(w map[string]any) { w["action"] = "dashboard.reset" }), "must have risk read"},
		{"unknown action", with(dataWidget(), func(w map[string]any) { w["action"] = "nope" }), "not one of this plugin's contributes.actions"},
		{"refresh too fast", with(dataWidget(), func(w map[string]any) { w["refreshSeconds"] = 30 }), "refreshSeconds must be 0 or 60-3600"},
		{"refresh too slow", with(dataWidget(), func(w map[string]any) { w["refreshSeconds"] = 3601 }), "refreshSeconds must be 0 or 60-3600"},
		{"view with action", with(viewWidget(), func(w map[string]any) { w["action"] = "dashboard.summary" }), "must not declare action or refreshSeconds"},
		{"view with refresh", with(viewWidget(), func(w map[string]any) { w["refreshSeconds"] = 60 }), "must not declare action or refreshSeconds"},
		{"data with view", with(dataWidget(), func(w map[string]any) { w["view"] = "widget" }), "must not declare view"},
		{"bad kind", with(dataWidget(), func(w map[string]any) { w["kind"] = "chart" }), "kind must be one of"},
		{"bad size", with(viewWidget(), func(w map[string]any) { w["size"] = "large" }), "size must be one of"},
		{"foreign command", with(dataWidget(), func(w map[string]any) { w["command"] = "other.open" }), "not one of this plugin's contributes.commands"},
		{"bidi title", with(dataWidget(), func(w map[string]any) { w["title"] = "a\u202eb" }), "U+202E"},
		{"unknown field", with(dataWidget(), func(w map[string]any) { w["html"] = "x" }), "unknown field"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := writeDashboardWidgetPackage(t, "process", []any{tc.widget})
			if _, err := readManifestStrict(root); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			manifest, err := readManifest(root)
			if err != nil {
				t.Fatal(err)
			}
			if resolved := manifest.contribution(dashboardWidgetPointKey); len(resolved.Entries) != 0 || len(resolved.Issues) != 1 {
				t.Fatalf("lenient = %+v", resolved)
			}
		})
	}
	root := writeKeymapPackage(t, "", func(manifest map[string]any) {
		manifest["contributes"] = map[string]any{
			"dashboardWidgets": []any{viewWidget()},
			"walkthroughs":     []any{map[string]any{"id": "intro", "title": "Intro", "steps": []any{map[string]any{"id": "one", "title": "One", "text": "Hi"}}}},
		}
	}, nil, nil)
	if _, err := readManifestStrict(root); err == nil || !strings.Contains(err.Error(), "dashboardWidgets") {
		t.Fatalf("declarative: %v", err)
	}
}

func TestDashboardWidgetsExpandPermission(t *testing.T) {
	data := DashboardWidgetContribution{ID: "engine", Kind: "data", Action: "a"}
	view := DashboardWidgetContribution{ID: "live", Kind: "view", View: "widget"}
	resolved := func(entries ...any) *resolvedContribution { return &resolvedContribution{Entries: entries} }
	changed := data
	changed.Action = "b"
	cases := []struct {
		name               string
		current, candidate *resolvedContribution
		want               bool
	}{
		{"unchanged", resolved(data, view), resolved(data, view), false},
		{"view added", resolved(data), resolved(data, view), false},
		{"data added", resolved(view), resolved(view, data), true},
		{"data added to nothing", nil, resolved(data), true},
		{"action changed", resolved(data), resolved(changed), true},
		{"data removed", resolved(data), resolved(), false},
	}
	for _, tc := range cases {
		if got := dashboardWidgetsExpandPermission(tc.current, tc.candidate); got != tc.want {
			t.Fatalf("%s: got %v", tc.name, got)
		}
	}
}

func TestNormalizeDashboardWidgetData(t *testing.T) {
	data, err := normalizeDashboardWidgetData(json.RawMessage(`{"items":[{"label":" Containers ","value":3,"severity":"success","hint":"running"},{"label":"Up","value":true},{"label":"Name","value":"x","extra":1}],"footnote":"via socket","updatedAt":"2026-01-01T00:00:00Z","other":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Items) != 3 || data.Items[0].Label != "Containers" || data.Items[0].Value != "3" || data.Items[0].Severity != "success" || data.Items[1].Value != "true" || data.Items[1].Severity != "normal" || data.Footnote != "via socket" {
		t.Fatalf("data = %+v", data)
	}
	many := `{"items":[` + strings.TrimSuffix(strings.Repeat(`{"label":"a","value":"b"},`, 9), ",") + `]}`
	for name, raw := range map[string]string{
		"not object":     `[1]`,
		"no items":       `{"footnote":"x"}`,
		"too many":       many,
		"bad severity":   `{"items":[{"label":"a","value":"b","severity":"critical"}]}`,
		"object value":   `{"items":[{"label":"a","value":{"x":1}}]}`,
		"missing label":  `{"items":[{"value":"b"}]}`,
		"multiline":      `{"items":[{"label":"a","value":"b\nc"}]}`,
		"bidi hint":      `{"items":[{"label":"a","value":"b","hint":"\u202e"}]}`,
		"long value":     `{"items":[{"label":"a","value":"` + strings.Repeat("v", 65) + `"}]}`,
		"long footnote":  `{"items":[],"footnote":"` + strings.Repeat("f", 129) + `"}`,
		"too many bytes": `{"items":[],"pad":"` + strings.Repeat("p", 16384) + `"}`,
	} {
		if _, err := normalizeDashboardWidgetData(json.RawMessage(raw)); err == nil {
			t.Fatalf("%s: expected an error", name)
		}
	}
}

func dashboardWidgetManager(t *testing.T, widgets ...DashboardWidgetContribution) (*pluginManager, *pluginRecord) {
	t.Helper()
	manager, record := contributionInvokeManager(t)
	entries := make([]any, 0, len(widgets))
	for _, widget := range widgets {
		entries = append(entries, widget)
	}
	record.mu.Lock()
	record.manifest.contributions = map[string]*resolvedContribution{dashboardWidgetPointKey: {Entries: entries}}
	record.mu.Unlock()
	return manager, record
}

func TestDashboardWidgetDataGating(t *testing.T) {
	data := DashboardWidgetContribution{ID: "engine", Kind: "data", Action: "run", Size: "small", RefreshSeconds: 60}
	view := DashboardWidgetContribution{ID: "live", Kind: "view", View: "view", Size: "small"}
	gone := DashboardWidgetContribution{ID: "gone", Kind: "data", Action: "missing", Size: "small"}
	manager, record := dashboardWidgetManager(t, data, view, gone)
	request := func(widgetID string) dashboardWidgetDataRequest {
		return dashboardWidgetDataRequest{PluginID: record.id, WidgetID: widgetID, RequestID: "widget-request-01"}
	}
	// The test helper answers every action with {"ok":true}: the call goes
	// through and the result fails validation.
	if _, err := manager.dashboardWidgetData(context.Background(), request("engine")); contributionErrorCode(err) != contributionErrInvalidResult {
		t.Fatalf("data widget: %v", err)
	}
	assertNoContributionRequestsLeft(t)
	cases := map[string]struct {
		request dashboardWidgetDataRequest
		code    string
	}{
		"view widget":       {request("live"), contributionErrForbidden},
		"unknown widget":    {request("nope"), contributionErrNotFound},
		"invalid widget":    {request("Bad Id"), contributionErrInvalidRequest},
		"unknown plugin":    {dashboardWidgetDataRequest{PluginID: "missing.plugin", WidgetID: "engine", RequestID: "widget-request-02"}, contributionErrNotFound},
		"bad request id":    {dashboardWidgetDataRequest{PluginID: record.id, WidgetID: "engine", RequestID: "x"}, contributionErrInvalidRequest},
		"undeclared action": {request("gone"), contributionErrNotFound},
	}
	for name, tc := range cases {
		if _, err := manager.dashboardWidgetData(context.Background(), tc.request); contributionErrorCode(err) != tc.code {
			t.Fatalf("%s: err = %v, want %s", name, err, tc.code)
		}
		assertNoContributionRequestsLeft(t)
	}

	// A disabled plugin's widgets are unavailable.
	if err := manager.SetEnabled(context.Background(), record.id, false); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.dashboardWidgetData(context.Background(), request("engine")); contributionErrorCode(err) != contributionErrUnavailable {
		t.Fatalf("disabled: %v", err)
	}
	assertNoContributionRequestsLeft(t)
}

func TestDashboardWidgetValidateFollowsSnapshot(t *testing.T) {
	data := DashboardWidgetContribution{ID: "engine", Kind: "data", Action: "run", Size: "small"}
	_, record := dashboardWidgetManager(t, data)
	record.mu.Lock()
	defer record.mu.Unlock()
	if widget, ok := dashboardWidgetLocked(record, "engine"); !ok || widget.Action != "run" {
		t.Fatalf("widget = %+v, %v", widget, ok)
	}
	record.manifest.contributions[dashboardWidgetPointKey].Entries = nil
	if _, ok := dashboardWidgetLocked(record, "engine"); ok {
		t.Fatal("removed widget must not be found")
	}
}
