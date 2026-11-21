package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestNormalizeAiCatalogRequests(t *testing.T) {
	search := aiCatalogSearchRequest{PluginID: "acme.catalog", CatalogID: "team", RequestID: "catalog-request-01"}
	if err := normalizeAiCatalogSearchRequest(&search); err != nil || search.Locale != "en" || search.Query != "" {
		t.Fatalf("empty query = %+v, %v", search, err)
	}
	searchCases := map[string]aiCatalogSearchRequest{
		"plugin":    {CatalogID: "team"},
		"catalog":   {PluginID: "p", CatalogID: "Team"},
		"multiline": {PluginID: "p", CatalogID: "team", Query: "a\nb"},
		"long":      {PluginID: "p", CatalogID: "team", Query: strings.Repeat("x", aiCatalogQueryMaxRunes+1)},
		"locale":    {PluginID: "p", CatalogID: "team", Locale: "../x"},
	}
	for name, request := range searchCases {
		if err := normalizeAiCatalogSearchRequest(&request); contributionErrorCode(err) != contributionErrInvalidRequest {
			t.Errorf("search %s: err = %v", name, err)
		}
	}
	install := aiCatalogInstallRequest{PluginID: "acme.catalog", CatalogID: "team", RequestID: "catalog-request-01", ItemID: "io.github/acme"}
	if err := normalizeAiCatalogInstallRequest(&install); err != nil || install.Locale != "en" {
		t.Fatalf("install = %+v, %v", install, err)
	}
	installCases := map[string]aiCatalogInstallRequest{
		"missing item": {PluginID: "p", CatalogID: "team"},
		"item space":   {PluginID: "p", CatalogID: "team", ItemID: "a b"},
		"long item":    {PluginID: "p", CatalogID: "team", ItemID: strings.Repeat("a", aiCatalogItemIDMaxLength+1)},
	}
	for name, request := range installCases {
		if err := normalizeAiCatalogInstallRequest(&request); contributionErrorCode(err) != contributionErrInvalidRequest {
			t.Errorf("install %s: err = %v", name, err)
		}
	}
}

func TestParseAiCatalogSearchResult(t *testing.T) {
	response, err := parseAiCatalogSearchResult(json.RawMessage(`{"items":[
		{"id":"io.github/acme","name":" Acme ","description":"Team server","docsUrl":"HTTPS://example.com/acme"},
		{"id":"io.github/acme","name":"Duplicate"},
		{"id":"bad id","name":"x"},
		{"id":"ftp","name":"x","docsUrl":"ftp://example.com"},
		{"id":"ctl","name":"a\u202eb"},
		{"id":"extra","name":"x","install":{"command":"rm"}},
		{"id":"plain","name":"Plain"}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 2 || response.Dropped != 5 || response.Truncated {
		t.Fatalf("response = %+v", response)
	}
	first := response.Items[0]
	if first.ID != "io.github/acme" || first.Name != "Acme" || first.DocsURL != "https://example.com/acme" || first.Description != "Team server" {
		t.Fatalf("first = %+v", first)
	}
	var many strings.Builder
	many.WriteString(`{"items":[`)
	for index := 0; index < aiCatalogItemsMax+3; index++ {
		if index > 0 {
			many.WriteString(",")
		}
		fmt.Fprintf(&many, `{"id":"item-%d","name":"Item"}`, index)
	}
	many.WriteString("]}")
	if response, err := parseAiCatalogSearchResult(json.RawMessage(many.String())); err != nil || !response.Truncated || len(response.Items) != aiCatalogItemsMax {
		t.Fatalf("many = %d %v, %v", len(response.Items), response.Truncated, err)
	}
	for name, raw := range map[string]string{
		"ok field":      `{"ok":true}`,
		"missing items": `{}`,
		"items type":    `{"items":{}}`,
	} {
		if _, err := parseAiCatalogSearchResult(json.RawMessage(raw)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestParseAiCatalogInstallResult(t *testing.T) {
	content, err := parseAiCatalogInstallResult(json.RawMessage(`{"content":"---\r\nname: x\n---\n\tbody"}`))
	if err != nil || content != "---\nname: x\n---\n\tbody" {
		t.Fatalf("content = %q, %v", content, err)
	}
	tooLarge := `{"content":"` + strings.Repeat("x", aiCatalogInstallMaxBytes+1) + `"}`
	for name, raw := range map[string]string{
		"ok field":  `{"ok":true}`,
		"empty":     `{"content":"  \n"}`,
		"type":      `{"content":1}`,
		"extra":     `{"content":"x","url":"https://example.com"}`,
		"nul":       `{"content":"a\u0000b"}`,
		"bidi":      `{"content":"a\u202eb"}`,
		"lone cr":   `{"content":"a\rb"}`,
		"utf-8":     "{\"content\":\"\xff\"}",
		"too large": tooLarge,
	} {
		if _, err := parseAiCatalogInstallResult(json.RawMessage(raw)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestPluginAiCatalogGating(t *testing.T) {
	// The first dispatch lazily starts the helper process inside the 8 s catalog
	// budget; a parallel go test ./... can take longer than that. This test checks
	// gating and error codes, not the budget, so give it the host invoke timeout.
	previousTimeout := aiCatalogTimeout
	aiCatalogTimeout = pluginInvokeTimeout
	t.Cleanup(func() { aiCatalogTimeout = previousTimeout })
	manager, record := contributionInvokeManager(t)
	record.mu.Lock()
	record.manifest.contributions = map[string]*resolvedContribution{aiCatalogPointKey: {Entries: []any{
		AiCatalogContribution{ID: "team", Kind: "mcp", Title: "Team", SearchAction: "run", InstallAction: "run"},
	}}}
	record.mu.Unlock()
	search := func() aiCatalogSearchRequest {
		return aiCatalogSearchRequest{PluginID: record.id, CatalogID: "team", RequestID: "catalog-request-01", Query: "acme"}
	}
	install := func() aiCatalogInstallRequest {
		return aiCatalogInstallRequest{PluginID: record.id, CatalogID: "team", RequestID: "catalog-request-02", ItemID: "acme"}
	}
	// The helper process answers {"ok":true}, which both result parsers reject.
	if _, err := manager.aiCatalogSearch(context.Background(), search()); contributionErrorCode(err) != contributionErrInvalidResult {
		t.Fatalf("search dispatch err = %v", err)
	}
	if _, err := manager.aiCatalogInstall(context.Background(), install()); contributionErrorCode(err) != contributionErrInvalidResult {
		t.Fatalf("install dispatch err = %v", err)
	}
	assertNoContributionRequestsLeft(t)
	missingPlugin, missingCatalog := search(), install()
	missingPlugin.PluginID = "missing.plugin"
	missingCatalog.CatalogID = "other"
	if _, err := manager.aiCatalogSearch(context.Background(), missingPlugin); contributionErrorCode(err) != contributionErrNotFound {
		t.Errorf("unknown plugin: err = %v", err)
	}
	if _, err := manager.aiCatalogInstall(context.Background(), missingCatalog); contributionErrorCode(err) != contributionErrNotFound {
		t.Errorf("unknown catalog: err = %v", err)
	}
	badID := search()
	badID.RequestID = "short"
	if _, err := manager.aiCatalogSearch(context.Background(), badID); contributionErrorCode(err) != contributionErrInvalidRequest {
		t.Errorf("bad request id: err = %v", err)
	}
	assertNoContributionRequestsLeft(t)
}

func TestPluginHostAiCatalogHandlersRejectBadArguments(t *testing.T) {
	for _, args := range []string{`nope`, `{"pluginId":"a","catalogId":"team","command":"x"}`} {
		if _, err := pluginHostAiCatalogSearchHandler("", args); contributionErrorCode(err) != contributionErrInvalidRequest {
			t.Errorf("search %s: err = %v", args, err)
		}
		if _, err := pluginHostAiCatalogInstallHandler("", args); contributionErrorCode(err) != contributionErrInvalidRequest {
			t.Errorf("install %s: err = %v", args, err)
		}
	}
}

func TestParseAiCatalogSearchResultRejectsInvalidUTF8(t *testing.T) {
	if _, err := parseAiCatalogSearchResult(json.RawMessage("{\"items\":[{\"id\":\"a\",\"name\":\"\xff\"}]}")); err == nil {
		t.Fatal("invalid UTF-8 must be rejected, not replaced")
	}
}

func TestAiCatalogRejectsChangedCatalogBeforeDispatch(t *testing.T) {
	manager, record := contributionInvokeManager(t)
	previous := AiCatalogContribution{ID: "team", Kind: "mcp", SearchAction: "run", InstallAction: "run"}
	current := previous
	current.Kind = "skill"
	record.mu.Lock()
	record.manifest.contributions = map[string]*resolvedContribution{aiCatalogPointKey: {Entries: []any{current}}}
	record.mu.Unlock()
	_, err := manager.invokeAiCatalogAction(context.Background(), record.id, "catalog-request-03", previous, "run", 2048,
		aiCatalogInstallPayload{Catalog: "team", Kind: "mcp", ID: "acme", Locale: "en"}, aiCatalogInstallMaxBytes)
	if contributionErrorCode(err) != contributionErrUnavailable {
		t.Fatalf("changed catalog dispatch = %v", err)
	}
	assertNoContributionRequestsLeft(t)
}
