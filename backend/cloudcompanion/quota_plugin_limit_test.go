package cloudcompanion

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMemberDeviceQueryAsksForSchemaThreeSnapshot(t *testing.T) {
	if got := memberDeviceQuery(deviceIdentity{DeviceID: "device"}).Get("schema_version"); got != "3" {
		t.Fatalf("snapshot request must opt in to schema 3, got %q", got)
	}
}

func TestFreeQuotaPolicyPluginLimit(t *testing.T) {
	for name, tc := range map[string]struct {
		limits   string
		want     int64
		disabled bool
		ok       bool
	}{
		"current cloud":      {`"max_devices":1,"max_active_workspaces":3,"max_windows_per_workspace":6,"max_enabled_plugins":2,"allow_plugins":true`, 2, false, true},
		"plugins forbidden":  {`"max_devices":1,"max_active_workspaces":3,"max_windows_per_workspace":6,"max_enabled_plugins":0,"allow_plugins":false`, 0, true, true},
		"cloud before field": {`"max_devices":1,"max_active_workspaces":3,"max_windows_per_workspace":6`, 0, false, true},
		"negative limit":     {`"max_devices":1,"max_active_workspaces":3,"max_windows_per_workspace":6,"max_enabled_plugins":-1`, 0, false, false},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"code":1,"data":{"plan":"free","limits":{` + tc.limits + `}}}`))
			}))
			defer server.Close()
			fetched, err := fetchFreeQuotaPolicy(context.Background(), server.URL, nil)
			policy := fetched.Limits
			if tc.ok != (err == nil) || policy.MaxEnabledPlugins != tc.want || policy.PluginsDisabled != tc.disabled {
				t.Fatalf("policy=%+v err=%v", policy, err)
			}
		})
	}
}

func TestFrameworkPlansPluginLimit(t *testing.T) {
	item := func(limits string) string {
		return `{"code":1,"data":{"items":[{"plan":"free","display_name":"Free","duration_days":null,"price_minor":0,"currency":"CNY","version_range":"*","policy_revision":1,"effective_at":1,"limits":{` + limits + `}}]}}`
	}
	for name, tc := range map[string]struct {
		body string
		want int64
		ok   bool
	}{
		"current cloud":      {item(`"max_devices":1,"max_active_workspaces":3,"max_windows_per_workspace":6,"max_enabled_plugins":2`), 2, true},
		"cloud before field": {item(`"max_devices":1,"max_active_workspaces":3,"max_windows_per_workspace":6`), 0, true},
		"plugins forbidden":  {item(`"max_devices":1,"max_active_workspaces":3,"max_windows_per_workspace":6,"max_enabled_plugins":0,"allow_plugins":false`), -1, true},
		"overflow limit":     {item(`"max_devices":1,"max_active_workspaces":3,"max_windows_per_workspace":6,"max_enabled_plugins":4294967296`), 0, false},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(tc.body)) }))
			defer server.Close()
			plans, err := fetchFrameworkPlans(context.Background(), server.URL)
			if tc.ok != (err == nil) {
				t.Fatalf("plans=%+v err=%v", plans, err)
			}
			if tc.ok && (len(plans) != 1 || plans[0].Limits.PluginsDisabled != (tc.want < 0) ||
				(tc.want >= 0 && plans[0].Limits.MaxEnabledPlugins != tc.want)) {
				t.Fatalf("unexpected plans %+v", plans)
			}
		})
	}
}
