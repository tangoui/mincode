package plugin

import (
	"strings"
	"sync"
	"testing"

	"mincode/app/backend/desktop"
	"mincode/app/plugins/api"
)

type fakeFloatingDriver struct {
	mu      sync.Mutex
	open    map[string]FloatingWindowSpec
	moved   map[string]func(x, y int)
	closed  map[string]func()
	resized map[string][2]int
}

func newFakeFloatingDriver(t *testing.T) *fakeFloatingDriver {
	t.Helper()
	driver := &fakeFloatingDriver{open: map[string]FloatingWindowSpec{}, moved: map[string]func(int, int){}, closed: map[string]func(){}, resized: map[string][2]int{}}
	SetFloatingWindowDriver(driver)
	t.Cleanup(func() {
		SetFloatingWindowDriver(nil)
		floatingMu.Lock()
		floatingOpen = map[string]floatingWindowRef{}
		for name, timer := range floatingPersist {
			timer.Stop()
			delete(floatingPersist, name)
		}
		floatingMu.Unlock()
	})
	return driver
}

func (d *fakeFloatingDriver) Open(spec FloatingWindowSpec, moved func(x, y int), closed func()) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.open[spec.Name] = spec
	d.moved[spec.Name] = moved
	d.closed[spec.Name] = closed
	return nil
}

func (d *fakeFloatingDriver) Close(name string) {
	d.mu.Lock()
	closed := d.closed[name]
	delete(d.open, name)
	delete(d.closed, name)
	d.mu.Unlock()
	if closed != nil {
		closed()
	}
}

func (d *fakeFloatingDriver) Resize(name string, width, height int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.resized[name] = [2]int{width, height}
	return nil
}

func (d *fakeFloatingDriver) isOpen(name string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.open[name]
	return ok
}

func newFloatingFixture(t *testing.T) (*pluginManager, *pluginRecord, *fakeFloatingDriver) {
	t.Helper()
	useTemporaryPluginData(t)
	driver := newFakeFloatingDriver(t)
	manifest := &Manifest{
		ID: "pet.plugin", Name: "Pet", APIVersion: 2, Capabilities: []api.Capability{api.CapabilityUI},
		Contributes: ManifestContributions{Views: []ManifestView{
			{ID: "main", Title: "Main", Entry: "ui/index.html"},
			{ID: "pet", Title: "Pet", Entry: "ui/index.html", Floating: &ManifestFloating{Width: 200, Height: 240}},
		}},
	}
	record := &pluginRecord{id: "pet.plugin", source: pluginSourceLocal, enabled: true, state: pluginStateRunning, manifest: manifest, runtime: &processRuntime{generation: 1}}
	manager := &pluginManager{records: map[string]*pluginRecord{record.id: record}, order: []string{record.id}}
	return manager, record, driver
}

func TestReadManifestValidatesFloatingViews(t *testing.T) {
	floating := func(mutate func(*Manifest)) error {
		_, err := readManifest(writeTestManifestV2(t, func(manifest *Manifest) {
			manifest.Contributes.Views = append(manifest.Contributes.Views, ManifestView{
				ID: "pet", Title: "Pet", Entry: "ui/index.html", Floating: &ManifestFloating{Width: 200, Height: 240},
			})
			if mutate != nil {
				mutate(manifest)
			}
		}))
		return err
	}
	if err := floating(nil); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Manifest){
		"too small": func(manifest *Manifest) { manifest.Contributes.Views[1].Floating.Width = floatingViewMinSize - 1 },
		"too large": func(manifest *Manifest) { manifest.Contributes.Views[1].Floating.Height = floatingViewMaxSize + 1 },
		"file drop": func(manifest *Manifest) {
			manifest.Contributes.Views[1].FileDrop = &ManifestFileDrop{Mode: FileDropModePaths}
		},
		"command target": func(manifest *Manifest) { manifest.Contributes.Commands[0].Target.ViewID = "pet" },
		"too many": func(manifest *Manifest) {
			for _, id := range []string{"pet2", "pet3"} {
				manifest.Contributes.Views = append(manifest.Contributes.Views, ManifestView{
					ID: id, Title: id, Entry: "ui/index.html", Floating: &ManifestFloating{Width: 64, Height: 64},
				})
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := floating(mutate); err == nil || !strings.Contains(err.Error(), "floating") {
				t.Fatalf("expected a floating rejection, got %v", err)
			}
		})
	}
}

func TestFloatingViewsAreNotTabOrPointTargets(t *testing.T) {
	manager, record, _ := newFloatingFixture(t)
	_, views, _ := manifestCommandMaps(record.manifest)
	if _, ok := views["pet"]; ok {
		t.Fatal("floating view is a contribution target")
	}
	var floating *ManifestFloating
	for _, view := range manager.Views() {
		if view.ID == "pet" {
			floating = view.Floating
		}
	}
	if floating == nil || floating.Width != 200 || floating.Height != 240 {
		t.Fatalf("view list lacks the floating size: %+v", floating)
	}
}

func TestFloatingViewShowHideResizeAndRestore(t *testing.T) {
	manager, record, driver := newFloatingFixture(t)
	name := floatingWindowName("pet.plugin", "pet")
	if err := manager.ShowFloatingView("pet.plugin", "main"); err == nil {
		t.Fatal("a tab view opened as a floating window")
	}
	if err := manager.ShowFloatingView("pet.plugin", "pet"); err != nil {
		t.Fatal(err)
	}
	spec := driver.open[name]
	if spec.Width != 200 || spec.Height != 240 || spec.Positioned || !strings.Contains(spec.URL, "mode=plugin-floating") || !strings.Contains(spec.URL, "viewId=pet") {
		t.Fatalf("unexpected window spec %+v", spec)
	}
	if !FloatingViewVisible("pet.plugin", "pet") {
		t.Fatal("status does not report the open window")
	}
	for _, size := range [][2]int{{floatingViewMinSize - 1, 100}, {201, 100}, {100, 241}} {
		if err := manager.ResizeFloatingView("pet.plugin", "pet", size[0], size[1]); err == nil {
			t.Fatalf("accepted size %v", size)
		}
	}
	if err := manager.ResizeFloatingView("pet.plugin", "pet", 120, 160); err != nil || driver.resized[name] != [2]int{120, 160} {
		t.Fatalf("resize: %v %v", err, driver.resized[name])
	}

	driver.moved[name](640, 480)
	floatingMu.Lock()
	timer := floatingPersist[name]
	floatingMu.Unlock()
	if timer == nil {
		t.Fatal("move was not scheduled for saving")
	}
	timer.Stop()
	floatingMu.Lock()
	delete(floatingPersist, name)
	_ = updateFloatingView("pet.plugin", "pet", func(view *floatingViewState) { view.X, view.Y, view.Positioned = 640, 480, true })
	floatingMu.Unlock()

	// The app quits: the native window closes but the pet stays remembered.
	driver.Close(name)
	if FloatingViewVisible("pet.plugin", "pet") {
		t.Fatal("closed window still reported open")
	}
	if opened := manager.RestoreFloatingViews(); opened != 1 {
		t.Fatalf("restore opened %d windows", opened)
	}
	if spec := driver.open[name]; !spec.Positioned || spec.X != 640 || spec.Y != 480 {
		t.Fatalf("restore lost the position: %+v", spec)
	}
	if opened := manager.RestoreFloatingViews(); opened != 0 {
		t.Fatal("restore reopened an open window")
	}

	if err := manager.HideFloatingView("pet.plugin", "pet"); err != nil {
		t.Fatal(err)
	}
	if driver.isOpen(name) || FloatingViewVisible("pet.plugin", "pet") {
		t.Fatal("hide left the window open")
	}
	if opened := manager.RestoreFloatingViews(); opened != 0 {
		t.Fatal("restore reopened a hidden view")
	}

	record.enabled = false
	if err := manager.ShowFloatingView("pet.plugin", "pet"); err == nil {
		t.Fatal("a disabled plugin opened a floating window")
	}
}

func TestFloatingWindowsFollowPluginLifecycle(t *testing.T) {
	manager, record, driver := newFloatingFixture(t)
	name := floatingWindowName("pet.plugin", "pet")
	if err := manager.ShowFloatingView("pet.plugin", "pet"); err != nil {
		t.Fatal(err)
	}
	// Reload or crash: the plugin stays enabled, so the window stays.
	manager.runRecordLifecycleHooks(record)
	if !driver.isOpen(name) {
		t.Fatal("a reload closed the floating window")
	}
	record.enabled = false
	manager.runRecordLifecycleHooks(record)
	if driver.isOpen(name) {
		t.Fatal("disable left the floating window open")
	}
	if !readFloatingState("pet.plugin").Views["pet"].Visible {
		t.Fatal("disable forgot that the view was shown")
	}
	record.enabled = true
	if opened := manager.RestoreFloatingViews(); opened != 1 {
		t.Fatal("re-enabled plugin did not restore its view")
	}
	manager.runRecordRemovalHooks("pet.plugin", removalReasonUninstall)
	if driver.isOpen(name) {
		t.Fatal("uninstall left the floating window open")
	}
	if _, found, _ := pluginKVGetRaw(hostPluginID, floatingStateKVKey("pet.plugin")); found {
		t.Fatal("uninstall kept the floating state")
	}
}

func TestFloatingActivationOpensOnlyOwnTargets(t *testing.T) {
	manager, record, _ := newFloatingFixture(t)
	var opened []desktop.NavigationTarget
	previous := floatingNavigate
	floatingNavigate = func(target desktop.NavigationTarget) error { opened = append(opened, target); return nil }
	t.Cleanup(func() { floatingNavigate = previous })

	activate := func(target FloatingActivation) error {
		return manager.ActivateFloatingView("pet.plugin", "pet", target)
	}
	if err := activate(FloatingActivation{Kind: "app"}); err != nil {
		t.Fatal(err)
	}
	if err := activate(FloatingActivation{Kind: "view", ViewID: "main"}); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]FloatingActivation{
		"floating view":           {Kind: "view", ViewID: "pet"},
		"unknown view":            {Kind: "view", ViewID: "other"},
		"unknown kind":            {Kind: "shell"},
		"runs without capability": {Kind: "ai-runs"},
		"stop without capability": {Kind: "ai-run-cancel", RunID: "run-00000000000000000000000000000000"},
	} {
		if err := activate(target); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := manager.ActivateFloatingView("pet.plugin", "main", FloatingActivation{Kind: "app"}); err == nil {
		t.Fatal("a tab view activated like a floating one")
	}

	record.manifest.Capabilities = append(record.manifest.Capabilities, api.CapabilityAIActivity)
	if err := activate(FloatingActivation{Kind: "ai-runs"}); err != nil {
		t.Fatal(err)
	}
	if err := activate(FloatingActivation{Kind: "ai-run", RunID: "run-00000000000000000000000000000000"}); err == nil {
		t.Fatal("unknown run opened")
	}
	if len(opened) != 3 || opened[0].Kind != "app" || opened[1].Kind != "plugin" || !strings.Contains(opened[1].Value, `"viewId":"main"`) || opened[2].Kind != "agent-runs" {
		t.Fatalf("opened %+v", opened)
	}
	var stopped []string
	previousCancel := floatingCancelRun
	floatingCancelRun = func(runID string) error {
		stopped = append(stopped, runID)
		return nil
	}
	t.Cleanup(func() { floatingCancelRun = previousCancel })
	record.manifest.Capabilities = append(record.manifest.Capabilities, api.CapabilityAITasks)
	if err := activate(FloatingActivation{Kind: "ai-run-cancel", RunID: "run-00000000000000000000000000000001"}); err != nil {
		t.Fatal(err)
	}
	if len(stopped) != 1 || stopped[0] != "run-00000000000000000000000000000001" || len(opened) != 3 {
		t.Fatalf("stopped %v, opened %+v", stopped, opened)
	}
	record.enabled = false
	if err := activate(FloatingActivation{Kind: "app"}); err == nil {
		t.Fatal("a disabled plugin activated the app")
	}
}
