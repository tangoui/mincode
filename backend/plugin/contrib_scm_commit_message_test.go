package plugin

import (
	"encoding/json"
	"strings"
	"testing"

	"mincode/app/plugins/api"
)

// writeScmCommitMessageManifest writes an API v2 process plugin with a read
// action "draft", a write action "w" and an aiCallable read action "r" plus
// the given scmCommitMessage value; withRead adds workspace.read.
func writeScmCommitMessageManifest(t *testing.T, withRead bool, providers string) string {
	t.Helper()
	return writeTestManifestV2(t, func(manifest *Manifest) {
		if withRead {
			manifest.Capabilities = append(manifest.Capabilities, api.CapabilityWorkspaceRead)
		}
		manifest.Contributes.Actions = append(manifest.Contributes.Actions,
			ManifestUIAction{ID: "draft", Risk: "read", MaxPayloadBytes: 196608},
			ManifestUIAction{ID: "w", Risk: "write", MaxPayloadBytes: 1024},
			ManifestUIAction{ID: "r", Risk: "read", AICallable: true, MaxPayloadBytes: 1024, Description: "Callable by the AI", InputSchema: map[string]any{"type": "object"}},
		)
		manifest.Contributes.Points = map[string]json.RawMessage{"scmCommitMessage": json.RawMessage(providers)}
	})
}

func TestScmCommitMessageDecode(t *testing.T) {
	manifest, err := readManifest(writeScmCommitMessageManifest(t, true, `[{"id":"conventional","title":" Ticket format ","titleLocalized":{"zh":"工单格式"},"action":"draft"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.contributionIssuesError(); err != nil {
		t.Fatal(err)
	}
	providers := manifest.ScmCommitMessageProviders()
	if len(providers) != 1 {
		t.Fatalf("providers = %+v", providers)
	}
	encoded, err := json.Marshal(providers[0])
	if err != nil || string(encoded) != `{"id":"conventional","title":"Ticket format","titleLocalized":{"zh":"工单格式"},"action":"draft"}` {
		t.Fatalf("encoded = %s %v", encoded, err)
	}

	cases := map[string]struct {
		withRead  bool
		providers string
		contains  string
	}{
		"without workspace.read": {false, `[{"id":"conventional","title":"Ticket","action":"draft"}]`, "requires capability workspace.read"},
		"write action":           {true, `[{"id":"conventional","title":"Ticket","action":"w"}]`, `commit message provider "conventional" action "w" must have risk "read", not "write"`},
		"ai action":              {true, `[{"id":"conventional","title":"Ticket","action":"r"}]`, `commit message provider "conventional" action "r" must not be aiCallable`},
		"two entries":            {true, `[{"id":"a","title":"A","action":"draft"},{"id":"b","title":"B","action":"draft"}]`, "at most 1"},
		"missing action":         {true, `[{"id":"conventional","title":"Ticket","action":"nope"}]`, `action "nope" is not one of this plugin's contributes.actions`},
		"bad id":                 {true, `[{"id":"Conventional","title":"Ticket","action":"draft"}]`, `commit message provider id "Conventional" must match`},
		"title too long":         {true, `[{"id":"conventional","title":"` + strings.Repeat("a", 33) + `","action":"draft"}]`, "title exceeds 32 characters"},
		"unknown field":          {true, `[{"id":"conventional","title":"Ticket","action":"draft","icon":"chat"}]`, "parse commit message provider"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			manifest, err := readManifest(writeScmCommitMessageManifest(t, tc.withRead, tc.providers))
			if err == nil {
				err = manifest.contributionIssuesError()
			}
			if err == nil || !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("err = %v, want %q", err, tc.contains)
			}
		})
	}
}

func TestScmCommitMessagePermissionExpansion(t *testing.T) {
	contributionPointsMu.RLock()
	point := contributionPoints[scmCommitMessagePointKey]
	contributionPointsMu.RUnlock()
	if point == nil || point.ExpandsPermission == nil {
		t.Fatalf("scmCommitMessage point = %+v", point)
	}
	current := &resolvedContribution{Entries: []any{ScmCommitMessageContribution{ID: "c", Action: "draft"}}}
	if point.ExpandsPermission(current, &resolvedContribution{Entries: []any{ScmCommitMessageContribution{ID: "c", Title: "Renamed", Action: "draft"}}}) {
		t.Fatal("a renamed provider must not expand permission")
	}
	if !point.ExpandsPermission(current, &resolvedContribution{Entries: []any{ScmCommitMessageContribution{ID: "c", Action: "other"}}}) {
		t.Fatal("a changed action must expand permission")
	}
	var empty Manifest
	if providers := empty.ScmCommitMessageProviders(); providers != nil {
		t.Fatalf("empty manifest providers = %+v", providers)
	}
}
