package plugin

import (
	"encoding/json"
	"strings"
	"testing"
)

func aiCatalogTestContext() *pointContext {
	return &pointContext{
		manifest: &Manifest{ID: "acme.catalog", APIVersion: 2},
		spec:     aiCatalogSpec,
		actions: map[string]ManifestUIAction{
			"search":  {ID: "search", Risk: "read", MaxPayloadBytes: 2048},
			"install": {ID: "install", Risk: "read", MaxPayloadBytes: 2048},
			"write":   {ID: "write", Risk: "write", MaxPayloadBytes: 1024},
			"ask":     {ID: "ask", Risk: "read", AICallable: true, MaxPayloadBytes: 1024},
		},
	}
}

func TestAiCatalogDecode(t *testing.T) {
	ctx := aiCatalogTestContext()
	value, id, err := decodeAiCatalogEntry(ctx, 0, "", json.RawMessage(`{"id":"team","kind":"mcp","title":" Team ","titleLocalized":{"zh-CN":"团队"},"description":"Team connectors","searchAction":"search","installAction":"install"}`))
	if err != nil || id != "team" {
		t.Fatalf("decode = %v, %q", err, id)
	}
	catalog := value.(AiCatalogContribution)
	if catalog.Kind != "mcp" || catalog.Title != "Team" || catalog.TitleLocalized["zh-CN"] != "团队" || catalog.SearchAction != "search" || catalog.InstallAction != "install" {
		t.Fatalf("catalog = %+v", catalog)
	}
	if value, _, err := decodeAiCatalogEntry(ctx, 0, "", json.RawMessage(`{"id":"rules","kind":"skill","title":"Rules","searchAction":"search","installAction":"search"}`)); err != nil || value.(AiCatalogContribution).Kind != "skill" {
		t.Fatalf("skill catalog with one shared action = %+v, %v", value, err)
	}
	base := `"title":"x","searchAction":"search","installAction":"install"`
	cases := map[string]string{
		`{"id":"a","kind":"prompt",` + base + `}`: "kind",
		`{"id":"a",` + base + `}`:                 "kind",
		`{"id":"A","kind":"mcp",` + base + `}`:    "must match",
		`{"id":"a","kind":"mcp","title":"","searchAction":"search","installAction":"install"}`:                       "title is required",
		`{"id":"a","kind":"mcp","title":"x","searchAction":"write","installAction":"install"}`:                       `risk "read"`,
		`{"id":"a","kind":"mcp","title":"x","searchAction":"search","installAction":"write"}`:                        `risk "read"`,
		`{"id":"a","kind":"mcp","title":"x","searchAction":"ask","installAction":"install"}`:                         "aiCallable",
		`{"id":"a","kind":"mcp","title":"x","searchAction":"search","installAction":"ask"}`:                          "aiCallable",
		`{"id":"a","kind":"mcp","title":"x","searchAction":"missing","installAction":"install"}`:                     "contributes.actions",
		`{"id":"a","kind":"mcp","title":"x","searchAction":"search","installAction":""}`:                             "contributes.actions",
		`{"id":"a","kind":"mcp","title":"x","description":"a\nb","searchAction":"search","installAction":"install"}`: "description",
		`{"id":"a","kind":"mcp",` + base + `,"extra":1}`:                                                             "unknown field",
	}
	for raw, want := range cases {
		if _, _, err := decodeAiCatalogEntry(ctx, 0, "", json.RawMessage(raw)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", raw, err, want)
		}
	}
}

func TestAiCatalogPointRejectsDuplicateIDs(t *testing.T) {
	contributionPointsMu.RLock()
	point := contributionPoints[aiCatalogPointKey]
	contributionPointsMu.RUnlock()
	if point == nil || point.DuplicateMessage == nil || !strings.Contains(point.DuplicateMessage("team"), `"team"`) {
		t.Fatalf("aiCatalogs must register a duplicate id check, got %+v", point)
	}
}

func TestAiCatalogsExpandPermission(t *testing.T) {
	base := AiCatalogContribution{ID: "a", Kind: "mcp", SearchAction: "search", InstallAction: "install"}
	resolved := func(catalogs ...AiCatalogContribution) *resolvedContribution {
		entries := make([]any, len(catalogs))
		for index, catalog := range catalogs {
			entries[index] = catalog
		}
		return &resolvedContribution{Entries: entries}
	}
	retitled, otherSearch, otherInstall, otherKind := base, base, base, base
	retitled.Title = "New"
	otherSearch.SearchAction = "search2"
	otherInstall.InstallAction = "install2"
	otherKind.Kind = "skill"
	if aiCatalogsExpandPermission(resolved(base), resolved(retitled)) {
		t.Fatal("a title change must not expand permission")
	}
	for name, candidate := range map[string]AiCatalogContribution{"search action": otherSearch, "install action": otherInstall, "kind": otherKind} {
		if !aiCatalogsExpandPermission(resolved(base), resolved(candidate)) {
			t.Fatalf("a changed %s must expand permission", name)
		}
	}
	if !aiCatalogsExpandPermission(nil, resolved(base)) || !aiCatalogsExpandPermission(resolved(base), resolved(base, AiCatalogContribution{ID: "b", Kind: "skill", SearchAction: "search", InstallAction: "install"})) {
		t.Fatal("a new catalog must expand permission")
	}
	if aiCatalogsExpandPermission(resolved(base), nil) {
		t.Fatal("removing every catalog must not expand permission")
	}
}
