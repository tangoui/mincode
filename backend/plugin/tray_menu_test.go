package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"mincode/app/backend/desktop"
)

// trayRecorder replaces trayApply and records every applied set.
type trayRecorder struct {
	mu      sync.Mutex
	applied [][]desktop.PluginTrayGroup
}

func (r *trayRecorder) calls() [][]desktop.PluginTrayGroup {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]desktop.PluginTrayGroup(nil), r.applied...)
}

func (r *trayRecorder) last(t *testing.T) []desktop.PluginTrayGroup {
	t.Helper()
	calls := r.calls()
	if len(calls) == 0 {
		t.Fatal("trayApply was not called")
	}
	return calls[len(calls)-1]
}

// useTrayHarness swaps trayApply, appLocked and the manager the core command
// sees, and resets the remembered tray state.
func useTrayHarness(t *testing.T, manager *pluginManager, locked bool) *trayRecorder {
	t.Helper()
	recorder := &trayRecorder{}
	previousApply, previousLocked, previousManager := trayApply, trayAppLocked, trayManager
	trayApply = func(groups []desktop.PluginTrayGroup) error {
		recorder.mu.Lock()
		recorder.applied = append(recorder.applied, groups)
		recorder.mu.Unlock()
		return nil
	}
	trayAppLocked = func() bool { return locked }
	trayManager = func() *pluginManager { return manager }
	resetTrayState()
	t.Cleanup(func() {
		trayApply, trayAppLocked, trayManager = previousApply, previousLocked, previousManager
		resetTrayState()
	})
	return recorder
}

type trayTestCommand struct {
	id, title string
	localized map[string]string
	group     string
	order     int
	// notTray keeps the command out of menus.tray.
	notTray bool
}

func trayPluginRecord(id, name string, commands ...trayTestCommand) *pluginRecord {
	manifest := &Manifest{ID: id, Name: name, APIVersion: 2, Runtime: ProcessRuntimeManifest{Type: "process", Entry: "bin/plugin"}}
	resolved := &resolvedContribution{}
	// Another menu's entry first: only the tray key counts.
	for _, command := range commands {
		manifest.Contributes.Commands = append(manifest.Contributes.Commands, ManifestCommand{ID: command.id, Title: command.title, TitleLocalized: command.localized, Action: "run"})
		resolved.Entries = append(resolved.Entries, MenuItemContribution{Menu: menuEditorContext, Command: command.id, Title: command.title})
	}
	for _, command := range commands {
		if command.notTray {
			continue
		}
		resolved.Entries = append(resolved.Entries, MenuItemContribution{
			Menu: menuTray, Command: command.id, Title: command.title, TitleLocalized: command.localized, Group: command.group, Order: command.order,
		})
	}
	manifest.contributions = map[string]*resolvedContribution{menuPointKey: resolved}
	return &pluginRecord{id: id, source: pluginSourceLocal, enabled: true, state: pluginStateStopped, manifest: manifest, runtime: &processRuntime{}}
}

func traySet(t *testing.T, args string) (trayResult, error) {
	t.Helper()
	raw, err := pluginHostTraySetHandler("", args)
	if err != nil {
		return trayResult{}, err
	}
	var result trayResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	return result, nil
}

func trayItemsJSON(pairs ...string) string {
	items := make([]string, 0, len(pairs)/2)
	for index := 0; index+1 < len(pairs); index += 2 {
		items = append(items, fmt.Sprintf(`{"pluginId":%q,"command":%q}`, pairs[index], pairs[index+1]))
	}
	return "[" + strings.Join(items, ",") + "]"
}

func TestTraySetDropsUndeclaredReferences(t *testing.T) {
	a := trayPluginRecord("acme.a", "Alpha", trayTestCommand{id: "a.one", title: "One"}, trayTestCommand{id: "a.two", title: "Two"}, trayTestCommand{id: "a.other", title: "Other", notTray: true})
	b := trayPluginRecord("acme.b", "Beta", trayTestCommand{id: "b.one", title: "B one"})
	b.enabled = false
	b.state = pluginStateDisabled
	recorder := useTrayHarness(t, shortcutManager(t, a, b), false)
	result, err := traySet(t, `{"locale":["en"],"items":`+trayItemsJSON("acme.a", "a.one", "acme.a", "a.missing", "acme.b", "b.one", "acme.x", "x")+`}`)
	if err != nil {
		t.Fatal(err)
	}
	if result.Shown != 1 || result.Dropped != 3 || result.Omitted != 0 {
		t.Fatalf("result %+v", result)
	}
	want := []desktop.PluginTrayGroup{{PluginID: "acme.a", Name: "Alpha", Items: []desktop.PluginTrayItem{{CommandID: "a.one", Title: "One"}}}}
	if got := recorder.last(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("applied %+v", got)
	}
	// A command that is not in menus.tray is dropped as well.
	result, err = traySet(t, `{"locale":["en"],"items":`+trayItemsJSON("acme.a", "a.other", "acme.a", "a.one", "acme.a", "a.one")+`}`)
	if err != nil || result.Shown != 1 || result.Dropped != 2 {
		t.Fatalf("result %+v err %v", result, err)
	}
}

func TestTraySetRejectsUnknownFields(t *testing.T) {
	a := trayPluginRecord("acme.a", "Alpha", trayTestCommand{id: "a.one", title: "One"})
	recorder := useTrayHarness(t, shortcutManager(t, a), false)
	for _, args := range []string{
		`{"locale":["en"],"items":[{"pluginId":"acme.a","command":"a.one","title":"Quit MinCode"}]}`,
		`{"locale":["en"],"items":[],"title":"x"}`,
		`{"locale":"en","items":[]}`,
		`{"locale":["en"],"items":[{"pluginId":"acme.a","command":1}]}`,
		`{"locale":["en"],"items":[]} {}`,
		`not-json`,
	} {
		if _, err := traySet(t, args); err == nil {
			t.Fatalf("accepted %s", args)
		}
	}
	// 512 plugins × trayMaxPerPlugin: every visible candidate fits in one request.
	if trayItemsMax != 2048 {
		t.Fatalf("trayItemsMax %d", trayItemsMax)
	}
	pairs := make([]string, 0, 2*(trayItemsMax+1))
	for index := 0; index <= trayItemsMax; index++ {
		pairs = append(pairs, "acme.a", "a.one")
	}
	if _, err := traySet(t, `{"locale":["en"],"items":`+trayItemsJSON(pairs...)+`}`); err == nil {
		t.Fatalf("accepted %d items", trayItemsMax+1)
	}
	if calls := recorder.calls(); len(calls) != 0 {
		t.Fatalf("trayApply called %d times", len(calls))
	}
	// trayItemsMax items is the limit, not an error.
	if _, err := traySet(t, `{"locale":["en"],"items":`+trayItemsJSON(pairs[:2*trayItemsMax]...)+`}`); err != nil {
		t.Fatalf("%d items rejected: %v", trayItemsMax, err)
	}
}

// Twenty plugins with four items each (80 references) fit in one request;
// resolveTray keeps the first eight by name whatever the request order.
func TestTrayTwentyPluginsKeepsFirstEightByName(t *testing.T) {
	var records []*pluginRecord
	var pairs []string
	// Request order is the reverse of the name order.
	for index := 19; index >= 0; index-- {
		id := fmt.Sprintf("p%02d.plugin", index)
		var commands []trayTestCommand
		for item := 0; item < 4; item++ {
			command := fmt.Sprintf("cmd:%d", item)
			commands = append(commands, trayTestCommand{id: command, title: command})
			pairs = append(pairs, id, command)
		}
		records = append(records, trayPluginRecord(id, fmt.Sprintf("Plugin %02d", index), commands...))
	}
	recorder := useTrayHarness(t, shortcutManager(t, records...), false)
	result, err := traySet(t, `{"locale":["en"],"items":`+trayItemsJSON(pairs...)+`}`)
	if err != nil {
		t.Fatal(err)
	}
	if result.Shown != 32 || result.Omitted != 12 || result.Dropped != 0 {
		t.Fatalf("result %+v", result)
	}
	var order []string
	for _, group := range recorder.last(t) {
		order = append(order, group.PluginID)
		if len(group.Items) != 4 || group.Items[0].CommandID != "cmd:0" {
			t.Fatalf("group %s items %+v", group.PluginID, group.Items)
		}
	}
	want := []string{"p00.plugin", "p01.plugin", "p02.plugin", "p03.plugin", "p04.plugin", "p05.plugin", "p06.plugin", "p07.plugin"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("order %v", order)
	}
}

// A tray click target carries any manifest command ID, ':' included
// (proxy:toggle); the plugin ID keeps its own shape.
func TestTrayCommandTargetMatchesContributionIDs(t *testing.T) {
	for _, id := range []string{"proxy:toggle", "proxy.toggle", "a", "ns:sub:cmd", "x-y_z"} {
		if !contributionIDPattern.MatchString(id) {
			t.Fatalf("sample %q is not a contribution ID", id)
		}
		if err := desktop.ValidateTarget(desktop.PluginCommandTarget("acme.capture", id)); err != nil {
			t.Fatalf("command %q rejected: %v", id, err)
		}
	}
	for _, id := range []string{":x", "a b", "a/b", strings.Repeat("a", 129)} {
		if contributionIDPattern.MatchString(id) {
			t.Fatalf("sample %q is a contribution ID", id)
		}
		if err := desktop.ValidateTarget(desktop.PluginCommandTarget("acme.capture", id)); err == nil {
			t.Fatalf("command %q accepted", id)
		}
	}
}

func TestTrayResolveTitlesAndOrder(t *testing.T) {
	longName := "A" + strings.Repeat("长", 59)
	records := []*pluginRecord{
		trayPluginRecord("zeta.plugin", "beta",
			trayTestCommand{id: "z.late", title: "Late", group: "b", order: 1},
			trayTestCommand{id: "z.first", title: "First", group: "a", order: 2},
			trayTestCommand{id: "z.second", title: "Second", group: "a", order: 2},
			trayTestCommand{id: "z.early", title: "Early", group: "a", order: 1, localized: map[string]string{"zh": "早‮期\n", "en": "Early"}},
		),
		trayPluginRecord("alpha.plugin", "Beta", trayTestCommand{id: "x.run", title: "Run", localized: map[string]string{"zh": "运行", "zh-TW": "執行"}}),
		trayPluginRecord("mid.plugin", longName, trayTestCommand{id: "m.run", title: "Bad\u0007⁦title⁩"}),
	}
	for index := 0; index < 6; index++ {
		records = append(records, trayPluginRecord(fmt.Sprintf("p%d.plugin", index), fmt.Sprintf("Plugin %d", index), trayTestCommand{id: "p.run", title: "Run"}))
	}
	manager := shortcutManager(t, records...)
	recorder := useTrayHarness(t, manager, false)
	pairs := []string{"zeta.plugin", "z.late", "zeta.plugin", "z.first", "zeta.plugin", "z.second", "zeta.plugin", "z.early", "alpha.plugin", "x.run", "mid.plugin", "m.run"}
	for index := 0; index < 6; index++ {
		pairs = append(pairs, fmt.Sprintf("p%d.plugin", index), "p.run")
	}
	result, err := traySet(t, `{"locale":["zh-CN","zh"],"items":`+trayItemsJSON(pairs...)+`}`)
	if err != nil {
		t.Fatal(err)
	}
	groups := recorder.last(t)
	if result.Omitted != 1 || result.Dropped != 0 || len(groups) != 8 {
		t.Fatalf("result %+v groups %d", result, len(groups))
	}
	// Names compared case-insensitively, ties by ID: "A长…" first, then
	// "Beta"(alpha.plugin) before "beta"(zeta.plugin); "Plugin 5" is the
	// ninth and is omitted.
	var order []string
	for _, group := range groups {
		order = append(order, group.PluginID)
	}
	wantOrder := []string{"mid.plugin", "alpha.plugin", "zeta.plugin", "p0.plugin", "p1.plugin", "p2.plugin", "p3.plugin", "p4.plugin"}
	if !reflect.DeepEqual(order, wantOrder) {
		t.Fatalf("order %v", order)
	}
	if got := groups[1].Items; !reflect.DeepEqual(got, []desktop.PluginTrayItem{{CommandID: "x.run", Title: "运行"}}) {
		t.Fatalf("alpha items %+v", got)
	}
	wantZeta := []desktop.PluginTrayItem{
		{CommandID: "z.early", Title: "早期"},
		{CommandID: "z.first", Title: "First"},
		{CommandID: "z.second", Title: "Second"},
		{Separator: true},
		{CommandID: "z.late", Title: "Late"},
	}
	if got := groups[2].Items; !reflect.DeepEqual(got, wantZeta) {
		t.Fatalf("zeta items %+v", got)
	}
	mid := groups[0]
	if mid.Name != "A"+strings.Repeat("长", 46)+"…" {
		t.Fatalf("name %q", mid.Name)
	}
	if mid.Items[0].Title != "Badtitle" {
		t.Fatalf("title %q", mid.Items[0].Title)
	}
	shown := 0
	for _, group := range groups {
		for _, item := range group.Items {
			if !item.Separator {
				shown++
			}
		}
	}
	if result.Shown != shown {
		t.Fatalf("shown %d, want %d", result.Shown, shown)
	}
}

func TestTraySetWhileLockedAppliesEmpty(t *testing.T) {
	a := trayPluginRecord("acme.a", "Alpha", trayTestCommand{id: "a.one", title: "One"})
	recorder := useTrayHarness(t, shortcutManager(t, a), true)
	result, err := traySet(t, `{"locale":["en"],"items":`+trayItemsJSON("acme.a", "a.one")+`}`)
	if err != nil {
		t.Fatal(err)
	}
	if result.Shown != 0 {
		t.Fatalf("result %+v", result)
	}
	calls := recorder.calls()
	if len(calls) != 1 || calls[0] != nil {
		t.Fatalf("applied %+v", calls)
	}
}

func TestTrayLifecycleRemovesPlugin(t *testing.T) {
	a := trayPluginRecord("acme.a", "Alpha", trayTestCommand{id: "a.one", title: "One"})
	c := trayPluginRecord("acme.c", "Gamma", trayTestCommand{id: "c.one", title: "C one"})
	manager := shortcutManager(t, a, c)
	recorder := useTrayHarness(t, manager, false)
	if _, err := traySet(t, `{"locale":["en"],"items":`+trayItemsJSON("acme.a", "a.one", "acme.c", "c.one")+`}`); err != nil {
		t.Fatal(err)
	}
	if got := recorder.last(t); len(got) != 2 {
		t.Fatalf("applied %+v", got)
	}
	// An idle stop keeps the group: nothing is re-applied.
	a.mu.Lock()
	a.state = pluginStateStopped
	a.mu.Unlock()
	manager.runRecordLifecycleHooks(a)
	if calls := recorder.calls(); len(calls) != 1 {
		t.Fatalf("idle stop re-applied: %+v", calls)
	}
	// Disable: enabled is cleared before the hooks run.
	a.mu.Lock()
	a.enabled = false
	a.state = pluginStateDisabled
	a.mu.Unlock()
	manager.runRecordLifecycleHooks(a)
	calls := recorder.calls()
	if len(calls) != 2 {
		t.Fatalf("calls %d", len(calls))
	}
	want := []desktop.PluginTrayGroup{{PluginID: "acme.c", Name: "Gamma", Items: []desktop.PluginTrayItem{{CommandID: "c.one", Title: "C one"}}}}
	if !reflect.DeepEqual(calls[1], want) {
		t.Fatalf("after disable %+v", calls[1])
	}
}

// addTrayMenu puts every command of record into its menus.tray.
func addTrayMenu(record *pluginRecord) {
	record.mu.Lock()
	defer record.mu.Unlock()
	resolved := &resolvedContribution{}
	for _, command := range record.manifest.Contributes.Commands {
		resolved.Entries = append(resolved.Entries, MenuItemContribution{Menu: menuTray, Command: command.ID, Title: command.Title})
	}
	if record.manifest.contributions == nil {
		record.manifest.contributions = map[string]*resolvedContribution{}
	}
	record.manifest.contributions[menuPointKey] = resolved
}

func TestTrayKeepsGroupAcrossRealStopAndRemovesOnDisable(t *testing.T) {
	manager, record := activationManager(t, "")
	ctx := context.Background()
	if err := manager.SetEnabled(ctx, record.id, true); err != nil {
		t.Fatal(err)
	}
	addTrayMenu(record)
	recorder := useTrayHarness(t, manager, false)
	if _, err := traySet(t, `{"locale":["en"],"items":`+trayItemsJSON(record.id, "run")+`}`); err != nil {
		t.Fatal(err)
	}
	want := []desktop.PluginTrayGroup{{PluginID: record.id, Name: "Lazy", Items: []desktop.PluginTrayItem{{CommandID: "run", Title: "Run"}}}}
	if got := recorder.last(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("applied %+v", got)
	}
	// Idle stop / reload: the real stop path runs the hooks in the stopping
	// state; the group stays and nothing is re-applied.
	for round := 0; round < 2; round++ {
		if err := manager.startRecord(ctx, record); err != nil {
			t.Fatalf("start: %v", err)
		}
		if err := manager.stopRecord(ctx, record, false); err != nil {
			t.Fatalf("stop: %v", err)
		}
	}
	if calls := recorder.calls(); len(calls) != 1 {
		t.Fatalf("normal stop re-applied: %+v", calls)
	}
	// A later sync while stopped still shows the plugin.
	result, err := traySet(t, `{"locale":["en"],"items":`+trayItemsJSON(record.id, "run")+`}`)
	if err != nil || result.Shown != 1 || result.Dropped != 0 {
		t.Fatalf("sync while stopped %+v %v", result, err)
	}
	// Disable (running first, so the stop path runs too) removes it.
	if err := manager.startRecord(ctx, record); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := manager.SetEnabled(ctx, record.id, false); err != nil {
		t.Fatal(err)
	}
	if got := recorder.last(t); got != nil {
		t.Fatalf("after disable %+v", got)
	}
	trayState.mu.Lock()
	refs := len(trayState.refs)
	trayState.mu.Unlock()
	if refs != 0 {
		t.Fatalf("references kept after disable: %d", refs)
	}
}

func TestTrayDevelopmentSessionDetachRemovesGroup(t *testing.T) {
	dev := trayPluginRecord("acme.dev", "Dev", trayTestCommand{id: "d.run", title: "Run"})
	dev.source = pluginSourceDevelopment
	dev.developmentID = "session-1"
	c := trayPluginRecord("acme.c", "Gamma", trayTestCommand{id: "c.one", title: "C one"})
	manager := shortcutManager(t, dev, c)
	recorder := useTrayHarness(t, manager, false)
	if _, err := traySet(t, `{"locale":["en"],"items":`+trayItemsJSON("acme.dev", "d.run", "acme.c", "c.one")+`}`); err != nil {
		t.Fatal(err)
	}
	if got := recorder.last(t); len(got) != 2 {
		t.Fatalf("applied %+v", got)
	}
	manager.detachDevelopmentSession(pluginDevSession{ID: "session-1", PluginID: "acme.dev"})
	want := []desktop.PluginTrayGroup{{PluginID: "acme.c", Name: "Gamma", Items: []desktop.PluginTrayItem{{CommandID: "c.one", Title: "C one"}}}}
	if got := recorder.last(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("after detach %+v", got)
	}
}

func TestTrayPluginNameAvoidsHostLabels(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"退出", "退出 (acme.p)"},
		{" Quit ", "Quit (acme.p)"},
		{"MinCode", "MinCode (acme.p)"},
		{"Show  Main  Window", "Show Main Window (acme.p)"},
		{"显示​主窗口", "显示主窗口 (acme.p)"},
		{"⠀⠀", "acme.p"},
		{"ㅤ ​", "acme.p"},
		{"", "acme.p"},
		{"Capture⠀", "Capture"},
		{"Capture Tool", "Capture Tool"},
	} {
		if got := trayPluginName(tc.name, "acme.p"); got != tc.want {
			t.Fatalf("trayPluginName(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := trayLabelText("⠀", "cmd.id"); got != "cmd.id" {
		t.Fatalf("blank title %q", got)
	}
}

func TestTrayAppLockClearsAndRestores(t *testing.T) {
	a := trayPluginRecord("acme.a", "Alpha", trayTestCommand{id: "a.one", title: "One"})
	locked := false
	var mu sync.Mutex
	recorder := useTrayHarness(t, shortcutManager(t, a), false)
	trayAppLocked = func() bool { mu.Lock(); defer mu.Unlock(); return locked }
	if _, err := traySet(t, `{"locale":["en"],"items":`+trayItemsJSON("acme.a", "a.one")+`}`); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	locked = true
	mu.Unlock()
	trayAppLockChanged()
	if got := recorder.last(t); got != nil {
		t.Fatalf("locked tray %+v", got)
	}
	mu.Lock()
	locked = false
	mu.Unlock()
	trayAppLockChanged()
	if got := recorder.last(t); len(got) != 1 || got[0].PluginID != "acme.a" {
		t.Fatalf("unlocked tray %+v", got)
	}
}
