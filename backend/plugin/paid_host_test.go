package plugin

import (
	"context"
	"errors"
	"testing"

	"mincode/app/backend/license"
)

func useHostPlan(t *testing.T, plan string, available bool) {
	t.Helper()
	previous := enabledPluginQuota
	enabledPluginQuota = func() (license.QuotaPolicy, string, bool) {
		return license.QuotaPolicy{}, plan, available
	}
	t.Cleanup(func() { enabledPluginQuota = previous })
}

func TestHostPlanPaidAcceptsOnlyAnnualOrPerpetual(t *testing.T) {
	for _, tc := range []struct {
		plan      string
		available bool
		want      bool
	}{
		{"annual", true, true}, {"perpetual", true, true}, {"free", true, false},
		{"", true, false}, {"enterprise", true, false}, {"annual", false, false},
	} {
		useHostPlan(t, tc.plan, tc.available)
		if got := HostPlanPaid(); got != tc.want {
			t.Errorf("HostPlanPaid(%q, %v) = %v; want %v", tc.plan, tc.available, got, tc.want)
		}
	}
}

func TestPaidHostRequirementGatesEnableAndStart(t *testing.T) {
	useTemporaryPluginData(t)
	useHostPlan(t, "free", true)
	manager, records := quotaTestManager(false, false)
	flagged, plain := records[0], records[1]
	if err := RecordMarketPaidHostRequirement(flagged.id, true); err != nil {
		t.Fatal(err)
	}
	if !pluginRequiresPaidHost(flagged.id) || pluginRequiresPaidHost(plain.id) {
		t.Fatal("requirement was not recorded for the flagged plugin only")
	}

	if err := manager.SetEnabled(context.Background(), flagged.id, true); !errors.Is(err, ErrPaidHostRequired) {
		t.Fatalf("flagged plugin enabled on the free plan: %v", err)
	}
	if flagged.enabled {
		t.Fatal("refused plugin was marked enabled")
	}
	if err := manager.SetEnabled(context.Background(), plain.id, true); err != nil {
		t.Fatalf("unflagged plugin refused on the free plan: %v", err)
	}

	// A plugin enabled before the plan lapsed still cannot start.
	flagged.mu.Lock()
	flagged.enabled, flagged.state = true, pluginStateStopped
	flagged.mu.Unlock()
	if err := manager.startRecord(context.Background(), flagged); !errors.Is(err, ErrPaidHostRequired) {
		t.Fatalf("flagged plugin started on the free plan: %v", err)
	}
	// Disabling is never gated.
	if err := manager.SetEnabled(context.Background(), flagged.id, false); err != nil {
		t.Fatal(err)
	}

	useHostPlan(t, "annual", true)
	if err := manager.SetEnabled(context.Background(), flagged.id, true); err != nil {
		t.Fatalf("flagged plugin refused on a paid plan: %v", err)
	}

	if err := RecordMarketPaidHostRequirement(flagged.id, false); err != nil {
		t.Fatal(err)
	}
	if pluginRequiresPaidHost(flagged.id) {
		t.Fatal("cleared requirement is still recorded")
	}
	if err := RecordMarketPaidHostRequirement("../bad", true); err == nil {
		t.Fatal("invalid plugin ID accepted")
	}
}
