package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"mincode/app/backend/core"
	"mincode/app/plugins/api"
)

// pluginHostKeymapContribution is the shape of the removed plugin_host_keymaps
// command. Tests keep it as a view over the contributions snapshot so the
// pre-framework keymap assertions stay unchanged.
type pluginHostKeymapContribution struct {
	PluginID   string         `json:"pluginId"`
	PluginName string         `json:"pluginName"`
	Version    string         `json:"version"`
	Presets    []KeymapPreset `json:"presets"`
	Issues     []KeymapIssue  `json:"issues"`
}

func (manager *pluginManager) Keymaps() []pluginHostKeymapContribution {
	result := []pluginHostKeymapContribution{}
	for _, plugin := range manager.Contributions([]string{keymapPointKey}).Plugins {
		point, ok := plugin.Points[keymapPointKey]
		if !ok {
			continue
		}
		contribution := pluginHostKeymapContribution{
			PluginID: plugin.PluginID, PluginName: plugin.PluginName, Version: plugin.Version,
			Presets: []KeymapPreset{}, Issues: []KeymapIssue{},
		}
		for _, entry := range point.Entries {
			contribution.Presets = append(contribution.Presets, entry.(KeymapPreset))
		}
		for _, issue := range point.Issues {
			contribution.Issues = append(contribution.Issues, KeymapIssue{Entry: issue.Entry, Preset: issue.ID, Message: issue.Message})
		}
		result = append(result, contribution)
	}
	return result
}

func TestEnabledContributionPointsMatchRegistry(t *testing.T) {
	for _, spec := range api.ContributionPointSpecs() {
		contributionPointsMu.RLock()
		_, registered := contributionPoints[spec.Key]
		contributionPointsMu.RUnlock()
		if spec.Enabled() != registered {
			t.Errorf("point %q: contract status %q but registered=%v; enabled points need a Go implementation and vice versa", spec.Key, spec.Status, registered)
		}
	}
	// Phase B points enable independently; keymaps and snippets must stay enabled.
	if keys := enabledContributionPointKeys(); !slices.Contains(keys, "keymaps") || !slices.Contains(keys, "snippets") {
		t.Fatalf("enabled points = %v", keys)
	}
}

func TestContributesRejectsUnknownAndPlannedPoints(t *testing.T) {
	// Every contract point is enabled today, so the planned path uses a
	// synthetic planned spec added to the in-memory contract for this test.
	const plannedKey = "fixturePlannedPoint"
	points := api.ContributionPointsContractDocument().Points
	points[plannedKey] = api.ContributionPointSpec{Key: plannedKey, Status: api.ContributionStatusPlanned, Kinds: []string{"process", "declarative"}}
	t.Cleanup(func() { delete(points, plannedKey) })
	for _, key := range []string{plannedKey, "notAPoint"} {
		var contributions ManifestContributions
		err := json.Unmarshal([]byte(`{"`+key+`":[]}`), &contributions)
		if err == nil || !strings.Contains(err.Error(), `unknown contribution point "`+key+`"`) {
			t.Fatalf("%s: err = %v", key, err)
		}
	}
	var contributions ManifestContributions
	if err := json.Unmarshal([]byte(`{"commands":[{"id":"a.run","title":"Run","action":"run"}],"keymaps":[{"path":"keymaps/a.json"}],"views":null}`), &contributions); err != nil {
		t.Fatal(err)
	}
	if len(contributions.Commands) != 1 || string(contributions.Points["keymaps"]) != `[{"path":"keymaps/a.json"}]` {
		t.Fatalf("decoded = %+v", contributions)
	}
	encoded, err := json.Marshal(contributions)
	if err != nil || string(encoded) != `{"commands":[{"id":"a.run","title":"Run","action":"run"}],"keymaps":[{"path":"keymaps/a.json"}]}` {
		t.Fatalf("encoded = %s %v", encoded, err)
	}
	if err := json.Unmarshal([]byte(`{"commands":[{"id":"a.run","title":"Run","bogus":1}]}`), &contributions); err == nil {
		t.Fatal("unknown command field accepted")
	}
}

func TestContributionSnapshotAndEvents(t *testing.T) {
	useTemporaryPluginData(t)
	var mu sync.Mutex
	var events []pluginContributionsChangedDTO
	core.SetEmitter(func(name string, data any) {
		if event, ok := data.(pluginContributionsChangedDTO); ok && name == pluginContributionsChangedEvent {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
		}
	})
	t.Cleanup(func() { core.SetEmitter(nil) })

	source := writeKeymapPackage(t, "", nil, []any{
		keymapPreset("keymap-test.a", map[string]any{"key": "f3", "command": "builtin:ai"}),
	}, nil)
	manager := newPluginManager()
	manifest, err := manager.InstallLocal(source)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot := manager.Contributions(nil); len(snapshot.Plugins) != 0 {
		t.Fatalf("disabled plugin listed: %+v", snapshot)
	}
	if err := manager.SetEnabled(context.Background(), manifest.ID, true); err != nil {
		t.Fatal(err)
	}
	snapshot := manager.Contributions(nil)
	if len(snapshot.Plugins) != 1 || snapshot.Plugins[0].Runtime != pluginRuntimeDeclarative || snapshot.Plugins[0].Source != pluginSourceLocal {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	point := snapshot.Plugins[0].Points[keymapPointKey]
	if len(point.Entries) != 1 || point.Fingerprint == "" || point.Issues == nil {
		t.Fatalf("point = %+v", point)
	}
	if filtered := manager.Contributions([]string{"themes"}); len(filtered.Plugins) != 0 {
		t.Fatalf("filter ignored: %+v", filtered)
	}
	if err := manager.Uninstall(context.Background(), manifest.ID, false); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) < 2 {
		t.Fatalf("events = %+v", events)
	}
	for index := 1; index < len(events); index++ {
		if events[index].Revision <= events[index-1].Revision {
			t.Fatalf("revision not increasing: %+v", events)
		}
	}
	if last := events[len(events)-1]; last.PluginID != manifest.ID || last.Revision != contributionRevision.Load() {
		t.Fatalf("last event = %+v", last)
	}
}

// TestKeymapsGoldenParity compares the framework's keymaps output for the
// official keymaps plugin with the snapshot captured from plugin_host_keymaps
// before the contribution-point refactor.
func TestKeymapsGoldenParity(t *testing.T) {
	useTemporaryPluginData(t)
	golden, err := os.ReadFile(filepath.Join("testdata", "keymaps-golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	var want []struct {
		PluginID   string            `json:"pluginId"`
		PluginName string            `json:"pluginName"`
		Version    string            `json:"version"`
		Presets    json.RawMessage   `json:"presets"`
		Issues     []json.RawMessage `json:"issues"`
	}
	if err := json.Unmarshal(golden, &want); err != nil || len(want) != 1 {
		t.Fatalf("golden = %v %d", err, len(want))
	}
	manager := newPluginManager()
	manifest, err := manager.InstallLocal(filepath.Join("..", "..", "official-plugins", "keymaps"))
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetEnabled(context.Background(), manifest.ID, true); err != nil {
		t.Fatal(err)
	}
	snapshot := manager.Contributions(nil)
	if len(snapshot.Plugins) != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	plugin := snapshot.Plugins[0]
	if plugin.PluginID != want[0].PluginID || plugin.PluginName != want[0].PluginName || plugin.Version != want[0].Version {
		t.Fatalf("plugin = %s %s %s", plugin.PluginID, plugin.PluginName, plugin.Version)
	}
	point := plugin.Points[keymapPointKey]
	if len(point.Issues) != 0 || len(want[0].Issues) != 0 {
		t.Fatalf("issues = %+v", point.Issues)
	}
	got, err := json.Marshal(point.Entries)
	if err != nil {
		t.Fatal(err)
	}
	var wantCompact, gotCompact bytes.Buffer
	if err := json.Compact(&wantCompact, want[0].Presets); err != nil {
		t.Fatal(err)
	}
	if err := json.Compact(&gotCompact, got); err != nil {
		t.Fatal(err)
	}
	if wantCompact.String() != gotCompact.String() {
		t.Fatalf("keymaps entries differ from pre-refactor golden output\n got: %.400s\nwant: %.400s", gotCompact.String(), wantCompact.String())
	}
}

func TestHostMethodSeam(t *testing.T) {
	name := "host.test.seam"
	registerHostMethod(name, func(runtimeHost *processRuntime) bool { return runtimeHost.pluginID == "seam" }, api.CapabilityStorage,
		func(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
			return "ok:" + string(params), nil
		})
	t.Cleanup(func() {
		hostMethodsMu.Lock()
		delete(hostMethods, name)
		hostMethodsMu.Unlock()
	})
	runtimeHost := &processRuntime{pluginID: "seam", capabilities: map[api.Capability]struct{}{}}
	var result any
	var rpcErr *rpcError
	respond := func(value any, err *rpcError) { result, rpcErr = value, err }
	if !runtimeHost.serveRegisteredHostMethod(rpcEnvelope{Method: name}, respond) || rpcErr == nil || rpcErr.Code != -32001 {
		t.Fatalf("ungranted call = %v %+v", result, rpcErr)
	}
	runtimeHost.capabilities[api.CapabilityStorage] = struct{}{}
	if !runtimeHost.serveRegisteredHostMethod(rpcEnvelope{Method: name, Params: json.RawMessage(`1`)}, respond) || rpcErr != nil || result != "ok:1" {
		t.Fatalf("granted call = %v %+v", result, rpcErr)
	}
	if runtimeHost.serveRegisteredHostMethod(rpcEnvelope{Method: "host.test.missing"}, respond) {
		t.Fatal("unknown method served")
	}
	described, _ := json.Marshal(runtimeHost.describeAPI())
	if !strings.Contains(string(described), `"name":"`+name+`"`) {
		t.Fatalf("describe = %s", described)
	}
}

// TestPointContextActions routes the statusBar key to a probe Decode and checks
// that points see the plugin's normalized actions (defaults applied) by ID.
func TestPointContextActions(t *testing.T) {
	var seen map[string]ManifestUIAction
	probe := &contributionPoint{Key: "statusBar", IssuePrefix: "invalid probe contribution", Decode: func(ctx *pointContext, _ int, _ string, _ json.RawMessage) (any, string, error) {
		seen = ctx.actions
		return map[string]any{}, "probe", nil
	}}
	contributionPointsMu.Lock()
	previous := contributionPoints["statusBar"]
	contributionPoints["statusBar"] = probe
	contributionPointsMu.Unlock()
	t.Cleanup(func() {
		contributionPointsMu.Lock()
		contributionPoints["statusBar"] = previous
		contributionPointsMu.Unlock()
	})
	manifest := &Manifest{ID: "actions.probe", APIVersion: 2}
	manifest.Runtime.Type = "process"
	manifest.Contributes.Actions = []ManifestUIAction{{ID: " format "}, {ID: "summary", Risk: "write", MaxPayloadBytes: 1024}}
	manifest.Contributes.Points = map[string]json.RawMessage{"statusBar": json.RawMessage(`[{}]`)}
	if err := validateContributions(newPluginPathResolver(t.TempDir()), manifest); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen["format"].Risk != "read" || seen["format"].MaxPayloadBytes != 16*1024 || seen["summary"].Risk != "write" || seen["summary"].MaxPayloadBytes != 1024 {
		t.Fatalf("ctx.actions = %+v", seen)
	}
}

// The frontend asks for every point it consumes in one request; the handler
// must accept the whole contract instead of a fixed cap new points outgrow.
func TestPluginHostContributionsHandlerAcceptsEveryPoint(t *testing.T) {
	useTemporaryPluginData(t)
	_ = host()
	previous := managerInst
	managerInst = &pluginManager{records: map[string]*pluginRecord{}}
	t.Cleanup(func() { managerInst = previous })
	points := []string{}
	for _, spec := range api.ContributionPointSpecs() {
		points = append(points, spec.Key)
	}
	if len(points) <= 32 {
		t.Fatalf("contract has %d points; the regression needs more than the old cap of 32", len(points))
	}
	encode := func(points []string) string {
		raw, _ := json.Marshal(map[string]any{"points": points})
		return string(raw)
	}
	if _, err := pluginHostContributionsHandler("", encode(points)); err != nil {
		t.Fatalf("all %d contract points rejected: %v", len(points), err)
	}
	if _, err := pluginHostContributionsHandler("", encode(append(points, "extra"))); err == nil {
		t.Fatal("more points than the contract defines must be rejected")
	}
}
