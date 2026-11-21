package license

import "testing"

func TestEvaluateQuota(t *testing.T) {
	policy := QuotaPolicy{MaxDevices: 2, MaxActiveWorkspaces: 4, MaxWindowsPerWorkspace: 12}
	cases := []struct {
		name     string
		policy   QuotaPolicy
		resource QuotaResource
		current  int64
		increase int64
		allowed  bool
		code     string
	}{
		{"first device", policy, QuotaDevice, 0, 1, true, ""},
		{"device at limit", policy, QuotaDevice, 2, 1, false, "devices_limit_reached"},
		{"workspace below limit", policy, QuotaWorkspace, 2, 1, true, ""},
		{"workspace restore at limit", policy, QuotaWorkspace, 4, 1, false, "active_workspaces_limit_reached"},
		{"window crossing limit", policy, QuotaWindow, 11, 2, false, "windows_per_workspace_limit_reached"},
		{"lowered policy preserves existing resources", QuotaPolicy{MaxActiveWorkspaces: 1}, QuotaWorkspace, 5, 1, false, "active_workspaces_limit_reached"},
		{"cloud raises limit", QuotaPolicy{MaxActiveWorkspaces: 6}, QuotaWorkspace, 5, 1, true, ""},
		{"explicit unlimited", QuotaPolicy{}, QuotaDevice, 100, 1, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := EvaluateQuota(tc.policy, tc.resource, tc.current, tc.increase)
			if err != nil {
				t.Fatal(err)
			}
			if got.Allowed != tc.allowed || got.Code != tc.code {
				t.Fatalf("got %+v; want allowed=%t code=%q", got, tc.allowed, tc.code)
			}
		})
	}
}

func TestEvaluateQuotaRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		policy   QuotaPolicy
		resource QuotaResource
		current  int64
		increase int64
	}{
		{QuotaPolicy{MaxDevices: -1}, QuotaDevice, 0, 1},
		{QuotaPolicy{MaxActiveWorkspaces: -1}, QuotaDevice, 0, 1},
		{QuotaPolicy{MaxWindowsPerWorkspace: -1}, QuotaDevice, 0, 1},
		{QuotaPolicy{}, QuotaResource("unknown"), 0, 1},
		{QuotaPolicy{}, QuotaDevice, -1, 1},
		{QuotaPolicy{}, QuotaDevice, 0, 0},
	}
	for _, tc := range cases {
		if _, err := EvaluateQuota(tc.policy, tc.resource, tc.current, tc.increase); err == nil {
			t.Fatalf("expected error for %+v", tc)
		}
	}
}
