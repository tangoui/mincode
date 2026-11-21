package plugin

import (
	"context"
	"encoding/json"
	"mincode/app/backend/associations"
	"mincode/app/plugins/api"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func externalTestManager(t *testing.T) (*pluginManager, *pluginRecord, externalCandidate) {
	t.Helper()
	m, r := contributionInvokeManager(t)
	_ = host()
	prev := managerInst
	managerInst = m
	t.Cleanup(func() {
		managerInst = prev
		externalQueue.Lock()
		externalQueue.items = map[string]*externalItem{}
		externalQueue.ready = false
		externalQueue.rejected = false
		externalQueue.Unlock()
	})
	r.mu.Lock()
	r.manifest.Capabilities = append(r.manifest.Capabilities, api.CapabilitySystemAssociations)
	r.runtime.capabilities[api.CapabilitySystemAssociations] = struct{}{}
	r.manifest.contributions = map[string]*resolvedContribution{externalOpenPoint: {Fingerprint: "one", Entries: []any{externalHandler{ID: "seed", Type: "torrent", View: "view"}, externalHandler{ID: "magnet", Type: "magnet", View: "view"}}}}
	r.mu.Unlock()
	c := m.externalCandidates("torrent", false)[0]
	return m, r, c
}
func TestExternalHandlerDecode(t *testing.T) {
	ctx := uriHandlerTestContext("example.plugin")
	for _, raw := range []string{`{"id":"a","type":"magnet","view":"main"}`, `{"id":"a","type":"torrent","view":"main","title":"Seed"}`} {
		if _, _, e := decodeExternalHandler(ctx, 0, "", json.RawMessage(raw)); e != nil {
			t.Fatal(e)
		}
	}
	for _, raw := range []string{`{"id":"A","type":"magnet","view":"main"}`, `{"id":"a","type":"http","view":"main"}`, `{"id":"a","type":"torrent","view":"missing"}`, `{"id":"a","type":"torrent","view":"main","command":"evil"}`} {
		if _, _, e := decodeExternalHandler(ctx, 0, "", json.RawMessage(raw)); e == nil {
			t.Fatal(raw)
		}
	}
}
func TestExternalPreferenceAndDispatch(t *testing.T) {
	m, r, c := externalTestManager(t)
	if preference(c) || len(m.externalCandidates("torrent", true)) != 0 {
		t.Fatal("enabled by install")
	}
	if _, e := associationUpdate(c, "enable"); e != nil {
		t.Fatal(e)
	}
	if !preference(c) {
		t.Fatal("not enabled")
	}
	if !EnqueueExternalURI("magnet:?xt=urn:btih:abc") {
		t.Fatal("not queued")
	}
	externalQueue.Lock()
	var id string
	for k := range externalQueue.items {
		id = k
	}
	externalQueue.Unlock()
	// Magnet was not opted in yet.
	v, e := externalResolve(id)
	if e != nil || len(v["candidates"].([]externalCandidate)) != 0 {
		t.Fatal(v, e)
	}
	magnet := m.externalCandidates("magnet", false)[0]
	_, _ = associationUpdate(magnet, "enable")
	result, e := externalDeliver(id, magnet.PluginID, magnet.HandlerID, magnet.Fingerprint, true)
	if e != nil {
		t.Fatal(e)
	}
	if result.(map[string]any)["params"].(map[string]any)["url"] != "magnet:?xt=urn:btih:abc" {
		t.Fatal(result)
	}
	if remembered("magnet", m.externalCandidates("magnet", true)) == "" {
		t.Fatal("not remembered")
	}
	if _, e := externalDeliver(id, magnet.PluginID, magnet.HandlerID, magnet.Fingerprint, true); e == nil {
		t.Fatal("duplicate delivery")
	}
	if r.runtime.Generation() != 0 {
		t.Fatal("dispatch started plugin")
	}
	r.mu.Lock()
	r.manifest.contributions[externalOpenPoint].Fingerprint = "two"
	r.mu.Unlock()
	if remembered("magnet", m.externalCandidates("magnet", true)) != "" || len(m.externalCandidates("torrent", true)) != 0 {
		t.Fatal("stale declaration trusted")
	}
}
func TestExternalAuthorizedFile(t *testing.T) {
	_, r, c := externalTestManager(t)
	_, _ = associationUpdate(c, "enable")
	dir := t.TempDir()
	file := filepath.Join(dir, "seed.torrent")
	_ = os.WriteFile(file, []byte("seed"), 0600)
	if rest := OfferExternalFiles([]string{file, filepath.Join(dir, "a.txt")}); len(rest) != 1 {
		t.Fatal(rest)
	}
	externalQueue.Lock()
	var id string
	for k := range externalQueue.items {
		id = k
	}
	externalQueue.Unlock()
	read := func(rt *processRuntime) (any, *rpcError) {
		raw, _ := json.Marshal(map[string]any{"requestId": id, "offset": 0, "length": 16})
		return hostAssociationsRead(rt, raw)
	}
	if _, e := read(r.runtime); e == nil {
		t.Fatal("read without authorization")
	}
	if _, e := externalDeliver(id, c.PluginID, c.HandlerID, c.Fingerprint, false); e != nil {
		t.Fatal(e)
	}
	if v, e := read(r.runtime); e != nil || v.(map[string]any)["data"] != "c2VlZA==" {
		t.Fatal(v, e)
	}
	fake := &processRuntime{pluginID: r.id}
	if _, e := read(fake); e == nil {
		t.Fatal("different runtime read grant")
	}
	_ = os.WriteFile(file, []byte("changed"), 0600)
	if _, e := read(r.runtime); e == nil {
		t.Fatal("read changed file")
	}
}
func TestExternalQueueBoundsAndExpiry(t *testing.T) {
	_, _, _ = externalTestManager(t)
	for i := 0; i < 15; i++ {
		EnqueueExternalURI("magnet:?x=" + strings.Repeat("a", i+1))
	}
	externalQueue.Lock()
	if !externalQueue.rejected {
		t.Error("cold-start overflow was not reported")
	}
	if len(externalQueue.items) != 8 {
		t.Fatal(len(externalQueue.items))
	}
	var id string
	for k, p := range externalQueue.items {
		id = k
		p.At = time.Now().Add(-3 * time.Minute)
	}
	externalQueue.Unlock()
	if _, e := externalResolve(id); e == nil {
		t.Fatal("expired request")
	}
}
func TestAssociationDefaultOnlyAfterRequest(t *testing.T) {
	_, _, c := externalTestManager(t)
	oldQ, oldS := associationQuery, associationSet
	defer func() { associationQuery, associationSet = oldQ, oldS }()
	sets := 0
	associationQuery = func(string) (associations.Status, error) {
		return associations.Status{Supported: true, Application: "other.app"}, nil
	}
	associationSet = func(id, prev string, restore bool) (associations.Status, error) {
		sets++
		if id != "torrent" || prev != "other.app" {
			t.Fatal(id, prev)
		}
		return associations.Status{Supported: true}, nil
	}
	if _, e := associationSettings(c.PluginID, c.HandlerID); e != nil {
		t.Fatal(e)
	}
	if sets != 0 {
		t.Fatal("query changed defaults")
	}
	if _, e := associationUpdate(c, "default"); e != nil {
		t.Fatal(e)
	}
	if sets != 1 {
		t.Fatal(sets)
	}
}
func TestDisableExternalHandler(t *testing.T) {
	m, r, c := externalTestManager(t)
	_, _ = associationUpdate(c, "enable")
	raw, _ := json.Marshal(c)
	_ = pluginKVSetRaw(hostPluginID, defaultKey("torrent"), raw)
	if e := m.SetEnabled(context.Background(), r.id, false); e != nil {
		t.Fatal(e)
	}
	if len(m.externalCandidates("torrent", true)) != 0 {
		t.Fatal("disabled candidate")
	}
	if _, ok, _ := pluginKVGetRaw(hostPluginID, defaultKey("torrent")); ok {
		t.Fatal("disabled trust retained")
	}
}

func TestExternalDeliveryRejectsStaleTarget(t *testing.T) {
	m, r, c := externalTestManager(t)
	_, _ = associationUpdate(c, "enable")
	file := filepath.Join(t.TempDir(), "a.torrent")
	_ = os.WriteFile(file, []byte("a"), 0600)
	OfferExternalFiles([]string{file})
	externalQueue.Lock()
	var id string
	for key := range externalQueue.items {
		id = key
	}
	externalQueue.Unlock()
	r.mu.Lock()
	r.manifest.Contributes.Views[0].Entry = "ui/other.html"
	r.mu.Unlock()
	if _, err := externalDeliver(id, c.PluginID, c.HandlerID, c.Fingerprint, false); err == nil {
		t.Fatal("changed target delivered")
	}
	if len(m.externalCandidates("torrent", true)) != 0 {
		t.Fatal("old opt-in accepted new view")
	}
}
func TestExternalCapabilityRequired(t *testing.T) {
	rt := uiRuntime("example")
	for _, name := range []string{"host.associations.list", "host.associations.request", "host.associations.read"} {
		reg, ok := registeredHostMethod(name)
		if !ok || reg.available(rt) {
			t.Fatal(name)
		}
	}
}

func TestExternalFileGrantRevokedOnLifecycleStop(t *testing.T) {
	m, r, c := externalTestManager(t)
	if _, err := associationUpdate(c, "enable"); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "seed.torrent")
	if err := os.WriteFile(file, []byte("seed"), 0600); err != nil {
		t.Fatal(err)
	}
	OfferExternalFiles([]string{file})
	externalQueue.Lock()
	var id string
	for key := range externalQueue.items {
		id = key
	}
	externalQueue.Unlock()
	if _, err := externalDeliver(id, c.PluginID, c.HandlerID, c.Fingerprint, false); err != nil {
		t.Fatal(err)
	}
	m.runRecordLifecycleHooks(r)
	if _, err := externalRequest(id); err == nil {
		t.Fatal("stopped plugin retained file grant")
	}
}

func TestExternalInvalidFileReportsRejection(t *testing.T) {
	_, _, c := externalTestManager(t)
	if _, err := associationUpdate(c, "enable"); err != nil {
		t.Fatal(err)
	}
	if rest := OfferExternalFiles([]string{filepath.Join(t.TempDir(), "missing.torrent")}); len(rest) != 0 {
		t.Fatal(rest)
	}
	externalQueue.Lock()
	defer externalQueue.Unlock()
	if !externalQueue.rejected || len(externalQueue.items) != 0 {
		t.Fatal("invalid file was not rejected explicitly")
	}
}
