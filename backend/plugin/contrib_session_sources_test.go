package plugin

import (
	"encoding/json"
	"strings"
	"testing"
)

// writeSessionSourcesManifest writes an API v2 process plugin with a read
// action "read", a write action "w" and an aiCallable read action "r" plus
// the given sessionSources value.
func writeSessionSourcesManifest(t *testing.T, sources string) string {
	t.Helper()
	return writeTestManifestV2(t, func(manifest *Manifest) {
		manifest.Contributes.Actions = append(manifest.Contributes.Actions,
			ManifestUIAction{ID: "read", Risk: "read", MaxPayloadBytes: 4096},
			// No risk: the manifest default is read.
			ManifestUIAction{ID: "implicit", MaxPayloadBytes: 4096},
			ManifestUIAction{ID: "w", Risk: "write", MaxPayloadBytes: 1024},
			ManifestUIAction{ID: "r", Risk: "read", AICallable: true, MaxPayloadBytes: 1024, Description: "Callable by the AI", InputSchema: map[string]any{"type": "object"}},
		)
		manifest.Contributes.Points = map[string]json.RawMessage{"sessionSources": json.RawMessage(sources)}
	})
}

func TestSessionSourcesDecode(t *testing.T) {
	cases := map[string]struct {
		sources  string
		want     *SessionSourceContribution
		contains string
	}{
		"defaults": {
			sources: `[{"id":"team","title":"Team","action":"read"}]`,
			want:    &SessionSourceContribution{ID: "team", Title: "Team", Icon: "chat", Mark: "TE", Action: "read"},
		},
		"empty icon and mark": {
			sources: `[{"id":"team","title":"Team","icon":"","mark":"","action":"read"}]`,
			want:    &SessionSourceContribution{ID: "team", Title: "Team", Icon: "chat", Mark: "TE", Action: "read"},
		},
		"null icon and mark": {
			sources: `[{"id":"team","title":"Team","icon":null,"mark":null,"action":"implicit"}]`,
			want:    &SessionSourceContribution{ID: "team", Title: "Team", Icon: "chat", Mark: "TE", Action: "implicit"},
		},
		"no ascii title": {
			sources: `[{"id":"team","title":"会话","action":"read"}]`,
			want:    &SessionSourceContribution{ID: "team", Title: "会话", Icon: "chat", Mark: "PL", Action: "read"},
		},
		"explicit icon and mark": {
			sources: `[{"id":"team","title":" Team Agent ","titleLocalized":{"zh":"团队"},"icon":"cpu","mark":"TA1","action":"read"}]`,
			want:    &SessionSourceContribution{ID: "team", Title: "Team Agent", TitleLocalized: map[string]string{"zh": "团队"}, Icon: "cpu", Mark: "TA1", Action: "read"},
		},
		"mark from digits and punctuation": {
			sources: `[{"id":"team","title":"·4o mini","action":"read"}]`,
			want:    &SessionSourceContribution{ID: "team", Title: "·4o mini", Icon: "chat", Mark: "4O", Action: "read"},
		},
		"bad icon":       {sources: `[{"id":"team","title":"Team","icon":"rocket","action":"read"}]`, contains: `session source "team" icon "rocket" is not one of chat, cpu, document, monitor, magic`},
		"bad mark":       {sources: `[{"id":"team","title":"Team","mark":"abcd","action":"read"}]`, contains: `session source "team" mark "abcd" must match ^[A-Z0-9]{1,3}$`},
		"lowercase mark": {sources: `[{"id":"team","title":"Team","mark":"ab","action":"read"}]`, contains: `mark "ab" must match`},
		"write action":   {sources: `[{"id":"team","title":"Team","action":"w"}]`, contains: `session source "team" action "w" must have risk "read", not "write"`},
		"ai action":      {sources: `[{"id":"team","title":"Team","action":"r"}]`, contains: `session source "team" action "r" must not be aiCallable`},
		"missing action": {sources: `[{"id":"team","title":"Team","action":"nope"}]`, contains: `session source "team" action "nope" is not one of this plugin's contributes.actions`},
		"three entries": {
			sources:  `[{"id":"a","title":"A","action":"read"},{"id":"b","title":"B","action":"read"},{"id":"c","title":"C","action":"read"}]`,
			contains: "maxEntries",
		},
		"title too long":  {sources: `[{"id":"team","title":"` + strings.Repeat("a", 33) + `","action":"read"}]`, contains: "title exceeds 32 characters"},
		"unknown field":   {sources: `[{"id":"team","title":"Team","action":"read","scope":"all"}]`, contains: "parse session source"},
		"bad id":          {sources: `[{"id":"Team","title":"Team","action":"read"}]`, contains: `session source id "Team" must match`},
		"multiline title": {sources: `[{"id":"team","title":"a\nb","action":"read"}]`, contains: "title"},
		"duplicate id":    {sources: `[{"id":"team","title":"A","action":"read"},{"id":"team","title":"B","action":"read"}]`, contains: `duplicate session source id "team"`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			manifest, err := readManifest(writeSessionSourcesManifest(t, tc.sources))
			if tc.want != nil {
				if err != nil {
					t.Fatal(err)
				}
				if err := manifest.contributionIssuesError(); err != nil {
					t.Fatal(err)
				}
				sources := manifest.SessionSources()
				if len(sources) != 1 {
					t.Fatalf("sources = %+v", sources)
				}
				got := sources[0]
				if got.ID != tc.want.ID || got.Title != tc.want.Title || got.Icon != tc.want.Icon || got.Mark != tc.want.Mark || got.Action != tc.want.Action ||
					len(got.TitleLocalized) != len(tc.want.TitleLocalized) || got.TitleLocalized["zh"] != tc.want.TitleLocalized["zh"] {
					t.Fatalf("source = %+v, want %+v", got, *tc.want)
				}
				return
			}
			if err == nil {
				err = manifest.contributionIssuesError()
			}
			if tc.contains == "maxEntries" {
				if err == nil || !strings.Contains(err.Error(), "at most 2 session sources") {
					t.Fatalf("err = %v, want maxEntries", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("err = %v, want %q", err, tc.contains)
			}
		})
	}
}

func TestDefaultSessionSourceMark(t *testing.T) {
	for title, want := range map[string]string{
		"Team":     "TE",
		"t":        "T",
		"会话":       "PL",
		"":         "PL",
		"会话 ab9":   "AB",
		"Ünïcode9": "NC",
	} {
		if got := defaultSessionSourceMark(title, "example.plugin"); got != want {
			t.Fatalf("defaultSessionSourceMark(%q) = %q, want %q", title, got, want)
		}
	}
}

func TestSessionSourcesSnapshotAndPermission(t *testing.T) {
	manifest, err := readManifest(writeSessionSourcesManifest(t, `[{"id":"team","title":"Team","action":"read"}]`))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(manifest.SessionSources()[0])
	if err != nil || string(encoded) != `{"id":"team","title":"Team","icon":"chat","mark":"TE","action":"read"}` {
		t.Fatalf("encoded = %s %v", encoded, err)
	}
	contributionPointsMu.RLock()
	point := contributionPoints[sessionSourcePointKey]
	contributionPointsMu.RUnlock()
	if point == nil || point.ExpandsPermission == nil {
		t.Fatalf("sessionSources point = %+v", point)
	}
	current := &resolvedContribution{Entries: []any{SessionSourceContribution{ID: "team", Action: "read"}}}
	if point.ExpandsPermission(current, &resolvedContribution{Entries: []any{SessionSourceContribution{ID: "team", Title: "Renamed", Action: "read"}}}) {
		t.Fatal("a renamed source must not expand permission")
	}
	if !point.ExpandsPermission(current, &resolvedContribution{Entries: []any{SessionSourceContribution{ID: "team", Action: "other"}}}) {
		t.Fatal("a changed action must expand permission")
	}
	if !point.ExpandsPermission(current, &resolvedContribution{Entries: []any{SessionSourceContribution{ID: "team", Action: "read"}, SessionSourceContribution{ID: "more", Action: "read"}}}) {
		t.Fatal("a new source must expand permission")
	}
	var empty Manifest
	if sources := empty.SessionSources(); sources != nil {
		t.Fatalf("empty manifest sources = %+v", sources)
	}
}

func TestContributionProviderActionRef(t *testing.T) {
	ctx := &pointContext{actions: map[string]ManifestUIAction{
		"read": {ID: "read", Risk: "read"},
		"w":    {ID: "w", Risk: "write"},
		"r":    {ID: "r", Risk: "read", AICallable: true},
	}}
	if id, err := contributionProviderActionRef(ctx, "provider", "p", " read "); err != nil || id != "read" {
		t.Fatalf("read = %q, %v", id, err)
	}
	for value, want := range map[string]string{
		"w":    `provider "p" action "w" must have risk "read", not "write"`,
		"r":    `provider "p" action "r" must not be aiCallable`,
		"none": `provider "p" action "none" is not one of this plugin's contributes.actions`,
	} {
		if _, err := contributionProviderActionRef(ctx, "provider", "p", value); err == nil || err.Error() != want {
			t.Fatalf("%s: err = %v, want %q", value, err, want)
		}
	}
}
