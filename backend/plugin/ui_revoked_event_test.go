package plugin

import (
	"context"
	"slices"
	"sync"
	"testing"

	"mincode/app/backend/core"
)

// The frontend warm pool destroys parked view frames on plugin:ui-revoked and
// open frames fail closed when their session is listed, so every revocation
// must announce the plugin and the closed session IDs (never nonces).
func TestRevokePluginUIEmitsRevokedEvent(t *testing.T) {
	var mu sync.Mutex
	var events []pluginUIRevokedEventData
	core.SetEmitter(func(name string, data any) {
		if event, ok := data.(pluginUIRevokedEventData); ok && name == pluginUIRevokedEvent {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
		}
	})
	t.Cleanup(func() { core.SetEmitter(nil) })

	host := newPluginUIHost()
	for _, session := range []struct{ id, plugin string }{{"s-b", "warm.example"}, {"s-a", "warm.example"}, {"s-other", "other.example"}} {
		ctx, cancel := context.WithCancel(context.Background())
		host.sessions[session.id] = &pluginUISession{ctx: ctx, cancel: cancel, ID: session.id, Nonce: "secret-" + session.id, PluginID: session.plugin}
	}
	manager := &pluginManager{records: map[string]*pluginRecord{}, uiHost: host}
	manager.revokePluginUI("warm.example")

	snapshot := func() []pluginUIRevokedEventData {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(events)
	}
	got := snapshot()
	if len(got) != 1 || got[0].PluginID != "warm.example" || !slices.Equal(got[0].SessionIDs, []string{"s-a", "s-b"}) {
		t.Fatalf("revoked got = %#v, want warm.example with sessions s-a, s-b", got)
	}
	if _, open := host.sessions["s-other"]; !open || len(host.sessions) != 1 {
		t.Fatalf("revocation touched other plugins' sessions: %v", host.sessions)
	}

	// A plugin without sessions still announces the revocation (warm frames may exist).
	manager.revokePluginUI("idle.example")
	got = snapshot()
	if len(got) != 2 || got[1].SessionIDs == nil || len(got[1].SessionIDs) != 0 {
		t.Fatalf("idle revocation = %#v", got[1:])
	}

	// Without a UI host nothing is revoked and nothing is announced.
	(&pluginManager{}).revokePluginUI("warm.example")
	got = snapshot()
	if len(got) != 2 {
		t.Fatalf("revoke without a UI host emitted %d got", len(got)-2)
	}
}
