package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func writeSchedulePackage(t *testing.T, schedules []any) string {
	t.Helper()
	return writeKeymapPackage(t, "", func(manifest map[string]any) {
		manifest["id"] = "acme-sched"
		manifest["runtime"] = map[string]any{"type": "process", "entry": "bin/plugin.mjs"}
		manifest["capabilities"] = []string{"commands", "ui"}
		manifest["contributes"] = map[string]any{
			"actions": []any{
				map[string]any{"id": "check", "risk": "read", "maxPayloadBytes": 1024},
				map[string]any{"id": "tiny", "risk": "read", "maxPayloadBytes": 64},
				map[string]any{"id": "send", "risk": "write", "maxPayloadBytes": 1024},
			},
			"schedules": schedules,
		}
	}, nil, map[string]string{"bin/plugin.mjs": "process.stdin.resume();\n"})
}

func TestSchedulesSnapshotAndValidation(t *testing.T) {
	useTemporaryPluginData(t)
	valid := map[string]any{"id": "certs", "title": "Check certificates", "titleLocalized": map[string]any{"zh": "检查证书"}, "action": "check", "intervalMinutes": 60}
	manifest, err := readManifestStrict(writeSchedulePackage(t, []any{valid}))
	if err != nil {
		t.Fatal(err)
	}
	entries := manifest.contribution(schedulePointKey).Entries
	if len(entries) != 1 || entries[0].(ScheduleContribution).IntervalMinutes != 60 || entries[0].(ScheduleContribution).Action != "check" {
		t.Fatalf("entries %+v", entries)
	}
	with := func(mutate func(map[string]any)) map[string]any {
		entry := map[string]any{}
		for key, value := range valid {
			entry[key] = value
		}
		mutate(entry)
		return entry
	}
	for name, entry := range map[string]map[string]any{
		"write action":   with(func(e map[string]any) { e["action"] = "send" }),
		"unknown action": with(func(e map[string]any) { e["action"] = "nope" }),
		"small payload":  with(func(e map[string]any) { e["action"] = "tiny" }),
		"too often":      with(func(e map[string]any) { e["intervalMinutes"] = 5 }),
		"too rare":       with(func(e map[string]any) { e["intervalMinutes"] = 2000 }),
		"bad id":         with(func(e map[string]any) { e["id"] = "Bad Id" }),
		"unknown field":  with(func(e map[string]any) { e["runOnStartup"] = true }),
		"missing title":  with(func(e map[string]any) { delete(e, "title") }),
	} {
		if _, err := readManifestStrict(writeSchedulePackage(t, []any{entry})); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestSchedulesExpandPermission(t *testing.T) {
	base := ScheduleContribution{ID: "certs", Action: "check", IntervalMinutes: 60}
	resolved := func(entries ...ScheduleContribution) *resolvedContribution {
		out := &resolvedContribution{}
		for _, entry := range entries {
			out.Entries = append(out.Entries, entry)
		}
		return out
	}
	longer := base
	longer.IntervalMinutes = 120
	shorter := base
	shorter.IntervalMinutes = 30
	other := base
	other.Action = "other"
	added := ScheduleContribution{ID: "ci", Action: "check", IntervalMinutes: 60}
	cases := []struct {
		name string
		next *resolvedContribution
		want bool
	}{
		{"same", resolved(base), false},
		{"longer interval", resolved(longer), false},
		{"shorter interval", resolved(shorter), true},
		{"other action", resolved(other), true},
		{"added", resolved(base, added), true},
		{"removed", resolved(), false},
	}
	for _, tc := range cases {
		if got := schedulesExpandPermission(resolved(base), tc.next); got != tc.want {
			t.Fatalf("%s: %v", tc.name, got)
		}
	}
}

func TestScheduleRunnerWaitsBacksOffAndForgets(t *testing.T) {
	manager, record := contributionInvokeManager(t)
	record.mu.Lock()
	record.manifest.contributions = map[string]*resolvedContribution{schedulePointKey: {Entries: []any{ScheduleContribution{ID: "certs", Title: "Certs", Action: "run", IntervalMinutes: 15}}}}
	record.mu.Unlock()

	now := time.Unix(1_800_000_000, 0)
	var mu sync.Mutex
	var calls []contributedActionCall
	fail := false
	runner := &scheduleRunnerState{states: map[string]*scheduleState{}, now: func() time.Time { mu.Lock(); defer mu.Unlock(); return now }}
	runner.invoke = func(_ *pluginManager, _ context.Context, call contributedActionCall) (json.RawMessage, error) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, call)
		if fail {
			return nil, errors.New("boom")
		}
		return json.RawMessage(`{}`), nil
	}
	advance := func(d time.Duration) {
		mu.Lock()
		now = now.Add(d)
		mu.Unlock()
		runner.step(manager)
		runner.wg.Wait()
	}
	count := func() int { mu.Lock(); defer mu.Unlock(); return len(calls) }

	advance(0) // registers the schedule
	advance(14 * time.Minute)
	if count() != 0 {
		t.Fatal("ran before the first interval")
	}
	advance(time.Minute)
	if count() != 1 {
		t.Fatalf("calls = %d", count())
	}
	var payload map[string]string
	if err := json.Unmarshal(calls[0].Payload, &payload); err != nil || payload["schedule"] != "certs" || payload["firedAt"] == "" || calls[0].Point != schedulePointKey || calls[0].ActionID != "run" {
		t.Fatalf("call %+v %s", calls[0], calls[0].Payload)
	}
	// Failure: the next attempt waits twice the interval.
	mu.Lock()
	fail = true
	mu.Unlock()
	advance(15 * time.Minute)
	if count() != 2 {
		t.Fatalf("calls = %d", count())
	}
	advance(15 * time.Minute)
	if count() != 2 {
		t.Fatal("did not back off after a failure")
	}
	advance(15 * time.Minute)
	if count() != 3 {
		t.Fatalf("calls after backoff = %d", count())
	}
	if !strings.Contains(strings.Join(recordLogMessages(record), "\n"), "schedule certs failed") {
		t.Fatal("failure not logged")
	}
	// A disabled plugin is forgotten and waits a full interval once re-enabled.
	if err := manager.SetEnabled(context.Background(), record.id, false); err != nil {
		t.Fatal(err)
	}
	advance(time.Hour)
	runner.mu.Lock()
	remaining := len(runner.states)
	runner.mu.Unlock()
	if remaining != 0 || count() != 3 {
		t.Fatalf("states %d calls %d", remaining, count())
	}
}

func recordLogMessages(record *pluginRecord) []string {
	record.mu.Lock()
	defer record.mu.Unlock()
	out := make([]string, 0, len(record.logs))
	for _, entry := range record.logs {
		out = append(out, entry.Message)
	}
	return out
}

func TestScheduleRunnerRunsOneSchedulePerPluginAndRetriesBusy(t *testing.T) {
	manager, record := contributionInvokeManager(t)
	record.mu.Lock()
	record.manifest.contributions = map[string]*resolvedContribution{schedulePointKey: {Entries: []any{
		ScheduleContribution{ID: "a", Title: "A", Action: "run", IntervalMinutes: 15},
		ScheduleContribution{ID: "b", Title: "B", Action: "run", IntervalMinutes: 15},
	}}}
	record.mu.Unlock()
	now := time.Unix(1_800_000_000, 0)
	var mu sync.Mutex
	runs := map[string]int{}
	busyOnce := true
	runner := &scheduleRunnerState{states: map[string]*scheduleState{}, now: func() time.Time { mu.Lock(); defer mu.Unlock(); return now }}
	runner.invoke = func(_ *pluginManager, _ context.Context, call contributedActionCall) (json.RawMessage, error) {
		mu.Lock()
		defer mu.Unlock()
		if busyOnce && call.EntryID == "b" {
			busyOnce = false
			return nil, newContributionError(contributionErrBusy, "plugin busy")
		}
		runs[call.EntryID]++
		return json.RawMessage(`{}`), nil
	}
	advance := func(d time.Duration) {
		mu.Lock()
		now = now.Add(d)
		mu.Unlock()
		runner.step(manager)
		runner.wg.Wait()
	}
	advance(0)
	for i := 0; i < 24; i++ { // six hours in 15-minute steps, with a tick in between
		advance(scheduleTickInterval)
		advance(15*time.Minute - scheduleTickInterval)
	}
	mu.Lock()
	defer mu.Unlock()
	if runs["a"] < 20 || runs["b"] < 20 {
		t.Fatalf("both schedules keep running: %v", runs)
	}
	for _, message := range recordLogMessages(record) {
		if strings.Contains(message, "failed") {
			t.Fatalf("busy counted as failure: %s", message)
		}
	}
}
