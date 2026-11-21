package plugin

import (
	"context"
	"errors"
	"testing"
)

const serviceRegistryTestService = "demo.text.reverse"

// serviceRegistryRecord builds a running local process plugin that provides
// demo.text.reverse (entry "reverse") plus any extra entries.
func serviceRegistryRecord(t *testing.T, id, name string, extra ...map[string]any) *pluginRecord {
	t.Helper()
	services := []any{serviceEntry(nil)}
	for _, entry := range extra {
		services = append(services, entry)
	}
	manifest, err := readManifestStrict(writeServicePackage(t, services, []string{"commands", "ui", "workspace.read"}, func(manifest map[string]any) {
		manifest["id"] = id
		manifest["name"] = name
	}))
	if err != nil {
		t.Fatal(err)
	}
	return &pluginRecord{id: id, root: t.TempDir(), source: pluginSourceLocal, manifest: &manifest, enabled: true, state: pluginStateRunning}
}

func serviceRegistryManager(records ...*pluginRecord) *pluginManager {
	manager := &pluginManager{records: map[string]*pluginRecord{}}
	for _, record := range records {
		manager.records[record.id] = record
		manager.order = append(manager.order, record.id)
	}
	setWorkspaceScopeFixture(manager, "ws-active")
	return manager
}

// restrictToOtherWorkspace scopes record to a workspace that is not active.
func restrictToOtherWorkspace(record *pluginRecord) {
	record.scope = pluginScopeWorkspace
	record.workspaces = map[string]struct{}{"ws-other": {}}
}

type fakeChooser struct {
	calls      int
	candidates []serviceProvider
	pick       string
	remember   bool
	err        error
}

func (chooser *fakeChooser) choose(_ context.Context, _ string, candidates []serviceProvider) (serviceProvider, bool, error) {
	chooser.calls++
	chooser.candidates = candidates
	if chooser.err != nil {
		return serviceProvider{}, false, chooser.err
	}
	for _, candidate := range candidates {
		if candidate.PluginID == chooser.pick {
			return candidate, chooser.remember, nil
		}
	}
	return serviceProvider{}, false, errors.New("pick not offered")
}

func candidateIDs(candidates []serviceProvider) []string {
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.PluginID)
	}
	return ids
}

func wantContributionCode(t *testing.T, err error, code string) {
	t.Helper()
	var coded *contributionError
	if !errors.As(err, &coded) || coded.Code != code {
		t.Fatalf("err = %v, want code %q", err, code)
	}
}

func TestServiceProvidersListing(t *testing.T) {
	useTemporaryPluginData(t)
	zeta := serviceRegistryRecord(t, "zeta-tools", "Zeta Tools")
	alpha := serviceRegistryRecord(t, "alpha-tools", "Alpha Tools", serviceEntry(func(e map[string]any) {
		e["id"] = "upper"
		e["service"] = "demo.text.upper"
		e["title"] = "Upper"
	}))
	disabled := serviceRegistryRecord(t, "beta-tools", "Beta Tools")
	disabled.enabled = false
	inactive := serviceRegistryRecord(t, "gamma-tools", "Gamma Tools")
	restrictToOtherWorkspace(inactive)
	failed := serviceRegistryRecord(t, "delta-tools", "Delta Tools")
	failed.state = pluginStateError
	manager := serviceRegistryManager(zeta, alpha, disabled, inactive, failed)

	if err := setServiceDefault(serviceRegistryTestService, "zeta-tools"); err != nil {
		t.Fatal(err)
	}
	providers := manager.serviceProviders(serviceRegistryTestService, "")
	if got := candidateIDs(providers); len(got) != 2 || got[0] != "alpha-tools" || got[1] != "zeta-tools" {
		t.Fatalf("providers = %v", got)
	}
	if providers[0].IsDefault || !providers[1].IsDefault || providers[1].PluginName != "Zeta Tools" || providers[1].Entry.ID != "reverse" {
		t.Fatalf("providers = %+v", providers)
	}
	all := manager.serviceProviders("", "")
	if len(all) != 3 || all[0].Entry.Title != "Reverse text" || all[1].Entry.Title != "Upper" || all[2].PluginID != "zeta-tools" {
		t.Fatalf("all = %+v", all)
	}
	if excluded := manager.serviceProviders("", "alpha-tools"); len(excluded) != 1 || excluded[0].PluginID != "zeta-tools" {
		t.Fatalf("excluded = %+v", excluded)
	}
}

func TestSelectProviderExplicit(t *testing.T) {
	useTemporaryPluginData(t)
	first := serviceRegistryRecord(t, "first-tools", "First")
	second := serviceRegistryRecord(t, "second-tools", "Second")
	inactive := serviceRegistryRecord(t, "inactive-tools", "Inactive")
	restrictToOtherWorkspace(inactive)
	manager := serviceRegistryManager(first, second, inactive)
	chooser := &fakeChooser{pick: "first-tools"}

	got, err := manager.selectServiceProvider(context.Background(), serviceRegistryTestService, &serviceProviderRef{PluginID: "second-tools"}, "", chooser.choose)
	if err != nil || got.PluginID != "second-tools" || got.Entry.ID != "reverse" {
		t.Fatalf("got = %+v, err = %v", got, err)
	}
	got, err = manager.selectServiceProvider(context.Background(), serviceRegistryTestService, &serviceProviderRef{PluginID: "second-tools", EntryID: "reverse"}, "", chooser.choose)
	if err != nil || got.PluginID != "second-tools" {
		t.Fatalf("got = %+v, err = %v", got, err)
	}
	if chooser.calls != 0 {
		t.Fatalf("chooser called %d times", chooser.calls)
	}

	_, err = manager.selectServiceProvider(context.Background(), serviceRegistryTestService, &serviceProviderRef{PluginID: "missing-tools"}, "", chooser.choose)
	wantContributionCode(t, err, contributionErrNotFound)
	_, err = manager.selectServiceProvider(context.Background(), serviceRegistryTestService, &serviceProviderRef{PluginID: "second-tools", EntryID: "nope"}, "", chooser.choose)
	wantContributionCode(t, err, contributionErrNotFound)
	_, err = manager.selectServiceProvider(context.Background(), "demo.text.other", &serviceProviderRef{PluginID: "second-tools"}, "", chooser.choose)
	wantContributionCode(t, err, contributionErrNotFound)
	_, err = manager.selectServiceProvider(context.Background(), serviceRegistryTestService, &serviceProviderRef{PluginID: "inactive-tools"}, "", chooser.choose)
	wantContributionCode(t, err, contributionErrUnavailable)
	second.enabled = false
	_, err = manager.selectServiceProvider(context.Background(), serviceRegistryTestService, &serviceProviderRef{PluginID: "second-tools"}, "", chooser.choose)
	wantContributionCode(t, err, contributionErrUnavailable)
	// A caller cannot name itself.
	_, err = manager.selectServiceProvider(context.Background(), serviceRegistryTestService, &serviceProviderRef{PluginID: "first-tools"}, "first-tools", chooser.choose)
	wantContributionCode(t, err, contributionErrNotFound)
	if chooser.calls != 0 {
		t.Fatalf("chooser called %d times", chooser.calls)
	}
}

func TestSelectProviderSingle(t *testing.T) {
	useTemporaryPluginData(t)
	inactive := serviceRegistryRecord(t, "inactive-tools", "Inactive")
	restrictToOtherWorkspace(inactive)
	manager := serviceRegistryManager(serviceRegistryRecord(t, "only-tools", "Only"), inactive)
	chooser := &fakeChooser{}
	got, err := manager.selectServiceProvider(context.Background(), serviceRegistryTestService, nil, "", chooser.choose)
	if err != nil || got.PluginID != "only-tools" || chooser.calls != 0 {
		t.Fatalf("got = %+v, err = %v, calls = %d", got, err, chooser.calls)
	}
	_, err = manager.selectServiceProvider(context.Background(), "demo.text.other", nil, "", chooser.choose)
	wantContributionCode(t, err, contributionErrNotFound)
}

func TestSelectProviderDefault(t *testing.T) {
	useTemporaryPluginData(t)
	manager := serviceRegistryManager(serviceRegistryRecord(t, "first-tools", "First"), serviceRegistryRecord(t, "second-tools", "Second"))
	if err := setServiceDefault(serviceRegistryTestService, "second-tools"); err != nil {
		t.Fatal(err)
	}
	chooser := &fakeChooser{pick: "first-tools"}
	got, err := manager.selectServiceProvider(context.Background(), serviceRegistryTestService, nil, "", chooser.choose)
	if err != nil || got.PluginID != "second-tools" || !got.IsDefault || chooser.calls != 0 {
		t.Fatalf("got = %+v, err = %v, calls = %d", got, err, chooser.calls)
	}
	// Clearing the default brings the chooser back.
	if err := setServiceDefault(serviceRegistryTestService, ""); err != nil {
		t.Fatal(err)
	}
	if _, found := readServiceDefaults()[serviceRegistryTestService]; found {
		t.Fatalf("defaults = %v", readServiceDefaults())
	}
	got, err = manager.selectServiceProvider(context.Background(), serviceRegistryTestService, nil, "", chooser.choose)
	if err != nil || got.PluginID != "first-tools" || chooser.calls != 1 {
		t.Fatalf("got = %+v, err = %v, calls = %d", got, err, chooser.calls)
	}
}

func TestSelectProviderSkipsInactiveDefault(t *testing.T) {
	useTemporaryPluginData(t)
	preferred := serviceRegistryRecord(t, "preferred-tools", "Preferred")
	restrictToOtherWorkspace(preferred)
	manager := serviceRegistryManager(preferred, serviceRegistryRecord(t, "first-tools", "First"), serviceRegistryRecord(t, "second-tools", "Second"))
	if err := setServiceDefault(serviceRegistryTestService, "preferred-tools"); err != nil {
		t.Fatal(err)
	}
	chooser := &fakeChooser{pick: "second-tools"}
	got, err := manager.selectServiceProvider(context.Background(), serviceRegistryTestService, nil, "", chooser.choose)
	if err != nil || got.PluginID != "second-tools" || chooser.calls != 1 {
		t.Fatalf("got = %+v, err = %v, calls = %d", got, err, chooser.calls)
	}
	if ids := candidateIDs(chooser.candidates); len(ids) != 2 || ids[0] != "first-tools" || ids[1] != "second-tools" {
		t.Fatalf("candidates = %v", ids)
	}
	// Not remembering keeps the stored default.
	if readServiceDefaults()[serviceRegistryTestService] != "preferred-tools" {
		t.Fatalf("defaults = %v", readServiceDefaults())
	}
}

func TestSelectProviderRemember(t *testing.T) {
	useTemporaryPluginData(t)
	manager := serviceRegistryManager(serviceRegistryRecord(t, "first-tools", "First"), serviceRegistryRecord(t, "second-tools", "Second"))
	chooser := &fakeChooser{pick: "second-tools", remember: true}
	got, err := manager.selectServiceProvider(context.Background(), serviceRegistryTestService, nil, "", chooser.choose)
	if err != nil || got.PluginID != "second-tools" {
		t.Fatalf("got = %+v, err = %v", got, err)
	}
	if readServiceDefaults()[serviceRegistryTestService] != "second-tools" {
		t.Fatalf("defaults = %v", readServiceDefaults())
	}
	// The remembered default now short-circuits the chooser.
	if _, err := manager.selectServiceProvider(context.Background(), serviceRegistryTestService, nil, "", chooser.choose); err != nil || chooser.calls != 1 {
		t.Fatalf("err = %v, calls = %d", err, chooser.calls)
	}

	failing := &fakeChooser{err: newContributionError(contributionErrCancelled, "dismissed")}
	if err := setServiceDefault(serviceRegistryTestService, ""); err != nil {
		t.Fatal(err)
	}
	_, err = manager.selectServiceProvider(context.Background(), serviceRegistryTestService, nil, "", failing.choose)
	wantContributionCode(t, err, contributionErrCancelled)
	// A chooser that returns something it was not offered is rejected.
	rogue := func(context.Context, string, []serviceProvider) (serviceProvider, bool, error) {
		return serviceProvider{PluginID: "rogue-tools", Entry: ServiceContribution{ID: "reverse"}}, true, nil
	}
	if _, err := manager.selectServiceProvider(context.Background(), serviceRegistryTestService, nil, "", rogue); err == nil {
		t.Fatal("rogue choice accepted")
	}
	if _, found := readServiceDefaults()[serviceRegistryTestService]; found {
		t.Fatalf("defaults = %v", readServiceDefaults())
	}
	// Without a chooser the host cannot ask the user.
	_, err = manager.selectServiceProvider(context.Background(), serviceRegistryTestService, nil, "", nil)
	wantContributionCode(t, err, contributionErrUnavailable)
}

func TestSelectProviderExcludesCaller(t *testing.T) {
	useTemporaryPluginData(t)
	manager := serviceRegistryManager(serviceRegistryRecord(t, "caller-tools", "Caller"), serviceRegistryRecord(t, "first-tools", "First"), serviceRegistryRecord(t, "second-tools", "Second"))
	chooser := &fakeChooser{pick: "first-tools"}
	if _, err := manager.selectServiceProvider(context.Background(), serviceRegistryTestService, nil, "caller-tools", chooser.choose); err != nil {
		t.Fatal(err)
	}
	if ids := candidateIDs(chooser.candidates); len(ids) != 2 || ids[0] != "first-tools" || ids[1] != "second-tools" {
		t.Fatalf("candidates = %v", ids)
	}

	alone := serviceRegistryManager(serviceRegistryRecord(t, "caller-tools", "Caller"))
	_, err := alone.selectServiceProvider(context.Background(), serviceRegistryTestService, nil, "caller-tools", chooser.choose)
	wantContributionCode(t, err, contributionErrNotFound)
	// A default pointing at the caller itself is ignored too.
	if err := setServiceDefault(serviceRegistryTestService, "caller-tools"); err != nil {
		t.Fatal(err)
	}
	chooser.calls = 0
	got, err := manager.selectServiceProvider(context.Background(), serviceRegistryTestService, nil, "caller-tools", chooser.choose)
	if err != nil || got.PluginID != "first-tools" || chooser.calls != 1 {
		t.Fatalf("got = %+v, err = %v, calls = %d", got, err, chooser.calls)
	}
}

func TestServiceDefaultsClearedOnUninstall(t *testing.T) {
	useTemporaryPluginData(t)
	manager := serviceRegistryManager(serviceRegistryRecord(t, "first-tools", "First"))
	for service, pluginID := range map[string]string{"demo.text.reverse": "first-tools", "demo.text.upper": "first-tools", "demo.image.ocr": "other-tools"} {
		if err := setServiceDefault(service, pluginID); err != nil {
			t.Fatal(err)
		}
	}
	// Clearing data keeps the user's choice; only uninstall drops it.
	manager.runRecordRemovalHooks("first-tools", removalReasonClearData)
	if len(readServiceDefaults()) != 3 {
		t.Fatalf("defaults after clear data = %v", readServiceDefaults())
	}
	manager.runRecordRemovalHooks("first-tools", removalReasonUninstall)
	defaults := readServiceDefaults()
	if len(defaults) != 1 || defaults["demo.image.ocr"] != "other-tools" {
		t.Fatalf("defaults after uninstall = %v", defaults)
	}
	manager.clearServiceDefaultsFor("other-tools")
	if raw, found, err := pluginKVGetRaw(hostPluginID, serviceDefaultsKVKey); err != nil || found {
		t.Fatalf("raw = %s, found = %v, err = %v", raw, found, err)
	}
}
