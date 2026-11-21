package plugin

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReadManifestV2AcceptsReadOnlyViewPrefetch(t *testing.T) {
	root := writeTestManifestV2(t, func(manifest *Manifest) {
		manifest.Contributes.Views[0].Prefetch = &ManifestViewPrefetch{Action: " dashboard.read ", Payload: json.RawMessage(`{ "scope" : "today" }`)}
	})
	manifest, err := readManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	prefetch := manifest.Contributes.Views[0].Prefetch
	if prefetch == nil || prefetch.Action != "dashboard.read" || string(prefetch.Payload) != `{"scope":"today"}` {
		t.Fatalf("prefetch not normalized: %+v", prefetch)
	}
	if dto := viewPrefetchDTO(&manifest.Contributes.Views[0]); dto == nil || dto.Action != "dashboard.read" || dto.Payload != `{"scope":"today"}` {
		t.Fatalf("prefetch DTO = %+v", dto)
	}

	// Without a payload (or with null) the page-equivalent payload "null" is forwarded.
	root = writeTestManifestV2(t, func(manifest *Manifest) {
		manifest.Contributes.Views[0].Prefetch = &ManifestViewPrefetch{Action: "dashboard.read", Payload: json.RawMessage(`null`)}
	})
	manifest, err = readManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if dto := viewPrefetchDTO(&manifest.Contributes.Views[0]); dto == nil || dto.Payload != "null" {
		t.Fatalf("prefetch DTO without payload = %+v", dto)
	}
	if viewPrefetchDTO(&ManifestView{}) != nil {
		t.Fatal("a view without prefetch produced a DTO")
	}
}

func TestReadManifestV2RejectsInvalidViewPrefetch(t *testing.T) {
	cases := map[string]struct {
		mutate func(*Manifest)
		want   string
	}{
		"unknown action": {func(manifest *Manifest) {
			manifest.Contributes.Views[0].Prefetch = &ManifestViewPrefetch{Action: "dashboard.missing"}
		}, "not declared"},
		"invalid action id": {func(manifest *Manifest) {
			manifest.Contributes.Views[0].Prefetch = &ManifestViewPrefetch{Action: "Bad Action"}
		}, "invalid"},
		"write action": {func(manifest *Manifest) {
			manifest.Contributes.Actions = append(manifest.Contributes.Actions, ManifestUIAction{ID: "dashboard.save", Risk: "write", UICallable: true})
			manifest.Contributes.Views[0].Prefetch = &ManifestViewPrefetch{Action: "dashboard.save"}
		}, "risk read"},
		"dangerous action": {func(manifest *Manifest) {
			manifest.Contributes.Actions = append(manifest.Contributes.Actions, ManifestUIAction{ID: "dashboard.wipe", Risk: "dangerous", UICallable: true})
			manifest.Contributes.Views[0].Prefetch = &ManifestViewPrefetch{Action: "dashboard.wipe"}
		}, "risk read"},
		"not UI callable": {func(manifest *Manifest) {
			manifest.Contributes.Actions = append(manifest.Contributes.Actions, ManifestUIAction{ID: "dashboard.internal", Risk: "read"})
			manifest.Contributes.Views[0].Prefetch = &ManifestViewPrefetch{Action: "dashboard.internal"}
		}, "uiCallable"},
		"host result handling": {func(manifest *Manifest) {
			manifest.Contributes.Actions = append(manifest.Contributes.Actions, ManifestUIAction{ID: "dashboard.docs", Risk: "read", UICallable: true, ResultHandling: "open-url"})
			manifest.Contributes.Views[0].Prefetch = &ManifestViewPrefetch{Action: "dashboard.docs"}
		}, "resultHandling"},
		"oversized payload": {func(manifest *Manifest) {
			manifest.Contributes.Actions[0].MaxPayloadBytes = 32
			manifest.Contributes.Views[0].Prefetch = &ManifestViewPrefetch{Action: "dashboard.read", Payload: json.RawMessage(`{"filter":"` + strings.Repeat("x", 64) + `"}`)}
		}, "exceeds"},
		"oversized payload at the default limit": {func(manifest *Manifest) {
			manifest.Contributes.Views[0].Prefetch = &ManifestViewPrefetch{Action: "dashboard.read", Payload: json.RawMessage(`"` + strings.Repeat("x", 16*1024) + `"`)}
		}, "exceeds"},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := readManifest(writeTestManifestV2(t, testCase.mutate))
			if err == nil || !strings.Contains(err.Error(), "prefetch") || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("expected prefetch rejection containing %q, got %v", testCase.want, err)
			}
		})
	}
}

func TestBeginUIReturnsViewPrefetch(t *testing.T) {
	fixture := newCustomEditorFixture(t, customEditorAccessRead)
	fixture.record.manifest.Contributes.Actions = append(fixture.record.manifest.Contributes.Actions,
		ManifestUIAction{ID: "canvas.load", Risk: "read", UICallable: true, MaxPayloadBytes: 1024})
	fixture.record.manifest.Contributes.Views[0].Prefetch = &ManifestViewPrefetch{Action: "canvas.load", Payload: json.RawMessage(`{"recent":true}`)}
	session, err := fixture.manager.BeginUI("draw.plugin", "canvas", "tango://app:9250")
	if err != nil {
		t.Fatal(err)
	}
	if session.Prefetch == nil || session.Prefetch.Action != "canvas.load" || session.Prefetch.Payload != `{"recent":true}` {
		t.Fatalf("session prefetch = %+v", session.Prefetch)
	}
	encoded, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"prefetch":{"action":"canvas.load","payload":"{\"recent\":true}"}`) {
		t.Fatalf("session JSON lacks prefetch: %s", encoded)
	}
}
