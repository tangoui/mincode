package plugin

import (
	"encoding/json"
	"strings"
	"testing"

	"mincode/app/plugins/api"
)

// writeFileDecorationsManifest writes an API v2 process plugin with a read
// action "decorate", a write action "w" and an aiCallable read action "r"
// plus the given fileDecorations value; withRead adds workspace.read.
func writeFileDecorationsManifest(t *testing.T, withRead bool, providers string) string {
	t.Helper()
	return writeTestManifestV2(t, func(manifest *Manifest) {
		if withRead {
			manifest.Capabilities = append(manifest.Capabilities, api.CapabilityWorkspaceRead)
		}
		manifest.Contributes.Actions = append(manifest.Contributes.Actions,
			ManifestUIAction{ID: "decorate", Risk: "read", MaxPayloadBytes: 163840},
			ManifestUIAction{ID: "w", Risk: "write", MaxPayloadBytes: 1024},
			ManifestUIAction{ID: "r", Risk: "read", AICallable: true, MaxPayloadBytes: 1024, Description: "Callable by the AI", InputSchema: map[string]any{"type": "object"}},
		)
		manifest.Contributes.Points = map[string]json.RawMessage{"fileDecorations": json.RawMessage(providers)}
	})
}

func TestFileDecorationsDecode(t *testing.T) {
	manifest, err := readManifest(writeFileDecorationsManifest(t, true, `[{"id":"lint","title":" Lint status ","titleLocalized":{"zh":"Lint 状态"},"action":"decorate"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.contributionIssuesError(); err != nil {
		t.Fatal(err)
	}
	providers := manifest.FileDecorationProviders()
	if len(providers) != 1 {
		t.Fatalf("providers = %+v", providers)
	}
	encoded, err := json.Marshal(providers[0])
	if err != nil || string(encoded) != `{"id":"lint","title":"Lint status","titleLocalized":{"zh":"Lint 状态"},"action":"decorate"}` {
		t.Fatalf("encoded = %s %v", encoded, err)
	}

	cases := map[string]struct {
		withRead  bool
		providers string
		contains  string
	}{
		"without workspace.read": {false, `[{"id":"lint","title":"Lint","action":"decorate"}]`, "requires capability workspace.read"},
		"write action":           {true, `[{"id":"lint","title":"Lint","action":"w"}]`, `file decoration provider "lint" action "w" must have risk "read", not "write"`},
		"ai action":              {true, `[{"id":"lint","title":"Lint","action":"r"}]`, `file decoration provider "lint" action "r" must not be aiCallable`},
		"two entries":            {true, `[{"id":"a","title":"A","action":"decorate"},{"id":"b","title":"B","action":"decorate"}]`, "at most 1"},
		"missing action":         {true, `[{"id":"lint","title":"Lint","action":"nope"}]`, `action "nope" is not one of this plugin's contributes.actions`},
		"bad id":                 {true, `[{"id":"Lint","title":"Lint","action":"decorate"}]`, `file decoration provider id "Lint" must match`},
		"title too long":         {true, `[{"id":"lint","title":"` + strings.Repeat("a", 33) + `","action":"decorate"}]`, "title exceeds 32 characters"},
		"unknown field":          {true, `[{"id":"lint","title":"Lint","action":"decorate","color":"red"}]`, "parse file decoration provider"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			manifest, err := readManifest(writeFileDecorationsManifest(t, tc.withRead, tc.providers))
			if err == nil {
				err = manifest.contributionIssuesError()
			}
			if err == nil || !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("err = %v, want %q", err, tc.contains)
			}
		})
	}
}

func TestFileDecorationsPermissionExpansion(t *testing.T) {
	contributionPointsMu.RLock()
	point := contributionPoints[fileDecorationPointKey]
	contributionPointsMu.RUnlock()
	if point == nil || point.ExpandsPermission == nil {
		t.Fatalf("fileDecorations point = %+v", point)
	}
	current := &resolvedContribution{Entries: []any{FileDecorationContribution{ID: "lint", Action: "decorate"}}}
	if point.ExpandsPermission(current, &resolvedContribution{Entries: []any{FileDecorationContribution{ID: "lint", Title: "Renamed", Action: "decorate"}}}) {
		t.Fatal("a renamed provider must not expand permission")
	}
	if !point.ExpandsPermission(current, &resolvedContribution{Entries: []any{FileDecorationContribution{ID: "lint", Action: "other"}}}) {
		t.Fatal("a changed action must expand permission")
	}
	var empty Manifest
	if providers := empty.FileDecorationProviders(); providers != nil {
		t.Fatalf("empty manifest providers = %+v", providers)
	}
}
