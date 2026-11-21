package plugin

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"mincode/app/plugins/api"
)

// startedContributionManager starts the helper process up front so slow
// process startup on a loaded machine does not eat into short call timeouts.
func startedContributionManager(t *testing.T) (*pluginManager, *pluginRecord) {
	t.Helper()
	manager, record := contributionInvokeManager(t)
	if err := manager.startRecord(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	return manager, record
}

func batchTwoTestContext(spec api.ContributionPointSpec) *pointContext {
	return &pointContext{
		manifest: &Manifest{ID: "acme.views", APIVersion: 2},
		spec:     spec,
		actions: map[string]ManifestUIAction{
			"list":  {ID: "list", Risk: "read", MaxPayloadBytes: 2048},
			"other": {ID: "other", Risk: "read"},
			"write": {ID: "write", Risk: "write"},
		},
	}
}

func TestTreeViewDecode(t *testing.T) {
	ctx := batchTwoTestContext(treeViewSpec)
	value, id, err := decodeTreeViewEntry(ctx, 0, "", json.RawMessage(`{"id":"pods","title":" Pods ","titleLocalized":{"zh":"容器组"},"action":"list"}`))
	if err != nil || id != "pods" {
		t.Fatalf("decode = %v, %q", err, id)
	}
	if view := value.(TreeViewContribution); view.Title != "Pods" || view.Action != "list" || view.TitleLocalized["zh"] != "容器组" {
		t.Fatalf("view = %+v", view)
	}
	cases := map[string]string{
		`{"id":"Pods","title":"x","action":"list"}`:           "must match",
		`{"id":"pods","title":"","action":"list"}`:            "title is required",
		`{"id":"pods","title":"x","action":"write"}`:          `risk "read"`,
		`{"id":"pods","title":"x","action":"missing"}`:        "contributes.actions",
		`{"id":"pods","title":"x","action":"list","extra":1}`: "unknown field",
		`{"id":"pods","title":"a\nb","action":"list"}`:        "title",
	}
	for raw, want := range cases {
		if _, _, err := decodeTreeViewEntry(ctx, 0, "", json.RawMessage(raw)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", raw, err, want)
		}
	}
}

func TestTreeViewsExpandPermission(t *testing.T) {
	resolved := func(views ...TreeViewContribution) *resolvedContribution {
		entries := make([]any, len(views))
		for index, view := range views {
			entries[index] = view
		}
		return &resolvedContribution{Entries: entries}
	}
	base := TreeViewContribution{ID: "a", Title: "A", Action: "list"}
	retitled, otherAction := base, base
	retitled.Title = "B"
	otherAction.Action = "other"
	point := contributionPoints[treeViewPointKey]
	cases := []struct {
		name               string
		current, candidate *resolvedContribution
		want               bool
	}{
		{"unchanged", resolved(base), resolved(base), false},
		{"retitled", resolved(base), resolved(retitled), false},
		{"removed", resolved(base), resolved(), false},
		{"changed action", resolved(base), resolved(otherAction), true},
		{"new view", nil, resolved(base), true},
	}
	for _, test := range cases {
		if got := point.ExpandsPermission(test.current, test.candidate); got != test.want {
			t.Errorf("%s = %v, want %v", test.name, got, test.want)
		}
	}
}

func TestParseTreeViewResult(t *testing.T) {
	scope := searchTestScope()
	raw := `{"items":[
		{"id":"ns/default","label":" default ","description":"ns","tooltip":"a\r\nb","icon":"folder","collapsible":true},
		{"id":"f1","label":"File","target":{"type":"file","path":"src/a.go","line":3}},
		{"id":"v1","label":"View","target":{"type":"view","view":"main"}},
		{"id":"u1","label":"Url","target":{"type":"url","url":"https://example.com"}},
		{"id":"x1","label":"Escape","target":{"type":"file","path":"../secret"}},
		{"id":"x2","label":"Abs","target":{"type":"file","path":"/etc/passwd"}},
		{"id":"x3","label":"Icon","icon":"skull"},
		{"id":"x4","label":""},
		{"id":"x5","label":"Html","html":"<b>"},
		{"id":"f1","label":"Duplicate"},
		{"id":" pad","label":"Padded id"}
	]}`
	response, err := parseTreeViewResult(json.RawMessage(raw), scope, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 4 || response.Dropped != 7 || response.Truncated {
		t.Fatalf("response = %+v", response)
	}
	if item := response.Items[0]; item.Label != "default" || item.Tooltip != "a\nb" || !item.Collapsible || item.Icon != "folder" {
		t.Fatalf("folder item = %+v", item)
	}
	if target := response.Items[1].Target; target == nil || target.Type != "file" || target.Path != "src/a.go" || target.Line != 3 {
		t.Fatalf("file target = %+v", target)
	}
	// Without a project, file targets are dropped (so the later "f1" is no
	// longer a duplicate); at the deepest level items cannot expand.
	response, err = parseTreeViewResult(json.RawMessage(raw), scope, false, true)
	if err != nil || len(response.Items) != 4 || response.Dropped != 7 || response.Items[0].Collapsible || response.Items[3].Label != "Duplicate" {
		t.Fatalf("no project = %+v, %v", response, err)
	}
	items := make([]string, treeViewItemsMax+3)
	for index := range items {
		items[index] = `{"id":"i` + strconv.Itoa(index) + `","label":"t"}`
	}
	response, err = parseTreeViewResult(json.RawMessage(`{"items":[`+strings.Join(items, ",")+`]}`), scope, false, false)
	if err != nil || !response.Truncated || len(response.Items) != treeViewItemsMax {
		t.Fatalf("truncated = %d %v, %v", len(response.Items), response.Truncated, err)
	}
	for _, bad := range []string{`{"items":null}`, `[]`, `{"items":[],"more":1}`} {
		if _, err := parseTreeViewResult(json.RawMessage(bad), scope, true, false); err == nil {
			t.Errorf("%s must be rejected", bad)
		}
	}
}

func TestNormalizeTreeViewRequest(t *testing.T) {
	valid := func() treeViewRequest {
		return treeViewRequest{PluginID: "acme.views", ViewID: "pods", RequestID: "request-0001", ParentID: "ns/default", Depth: 1}
	}
	request := valid()
	if err := normalizeTreeViewRequest(&request); err != nil || request.Locale != "en" {
		t.Fatalf("valid = %+v, %v", request, err)
	}
	root := treeViewRequest{PluginID: "acme.views", ViewID: "pods", RequestID: "request-0001"}
	if err := normalizeTreeViewRequest(&root); err != nil {
		t.Fatalf("root = %v", err)
	}
	cases := map[string]func(*treeViewRequest){
		"bad plugin":      func(r *treeViewRequest) { r.PluginID = "Bad Id" },
		"bad view":        func(r *treeViewRequest) { r.ViewID = "X" },
		"root with depth": func(r *treeViewRequest) { r.ParentID, r.Depth = "", 2 },
		"zero depth":      func(r *treeViewRequest) { r.Depth = 0 },
		"too deep":        func(r *treeViewRequest) { r.Depth = treeViewDepthMax },
		"bad parent":      func(r *treeViewRequest) { r.ParentID = "a\nb" },
		"long parent":     func(r *treeViewRequest) { r.ParentID = strings.Repeat("a", treeViewItemIDMaxLength+1) },
		"bad locale":      func(r *treeViewRequest) { r.Locale = "en_US!" },
	}
	for name, mutate := range cases {
		request := valid()
		mutate(&request)
		if err := normalizeTreeViewRequest(&request); contributionErrorCode(err) != contributionErrInvalidRequest {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestPluginTreeViewGating(t *testing.T) {
	manager, record := startedContributionManager(t)
	record.mu.Lock()
	record.manifest.contributions = map[string]*resolvedContribution{treeViewPointKey: {Entries: []any{TreeViewContribution{ID: "pods", Title: "Pods", Action: "run"}}}}
	record.mu.Unlock()
	valid := func() treeViewRequest {
		return treeViewRequest{PluginID: record.id, ViewID: "pods", RequestID: "request-0001", ProjectRoot: t.TempDir()}
	}
	// The helper process answers {"ok":true}: dispatched, then rejected by
	// result validation.
	if _, err := manager.treeViewChildren(context.Background(), valid()); contributionErrorCode(err) != contributionErrInvalidResult {
		t.Fatalf("dispatch err = %v", err)
	}
	assertNoContributionRequestsLeft(t)
	cases := map[string]struct {
		mutate func(*treeViewRequest)
		code   string
	}{
		"unknown plugin":  {func(r *treeViewRequest) { r.PluginID = "missing.plugin" }, contributionErrNotFound},
		"unknown view":    {func(r *treeViewRequest) { r.ViewID = "other" }, contributionErrNotFound},
		"relative root":   {func(r *treeViewRequest) { r.ProjectRoot = "relative" }, contributionErrForbidden},
		"missing root":    {func(r *treeViewRequest) { r.ProjectRoot = "/definitely/missing/project" }, contributionErrNotFound},
		"filesystem root": {func(r *treeViewRequest) { r.ProjectRoot = "/" }, contributionErrForbidden},
	}
	for name, test := range cases {
		request := valid()
		test.mutate(&request)
		if _, err := manager.treeViewChildren(context.Background(), request); contributionErrorCode(err) != test.code {
			t.Errorf("%s: err = %v, want %s", name, err, test.code)
		}
	}
	assertNoContributionRequestsLeft(t)
}

func TestTreeViewPayloadGatesProjectRoot(t *testing.T) {
	root := t.TempDir()
	project, err := contributionProjectFor(root, false)
	if err != nil || project.Root != "" || project.Name == "" {
		t.Fatalf("without workspace.read = %+v, %v", project, err)
	}
	project, err = contributionProjectFor(root, true)
	if err != nil || project.Root == "" {
		t.Fatalf("with workspace.read = %+v, %v", project, err)
	}
	encoded, _ := encodeContributionJSON(treeViewPayload{View: "pods", Locale: "en"})
	if string(encoded) != `{"view":"pods","parent":null,"locale":"en"}` {
		t.Fatalf("root payload = %s", encoded)
	}
}

func TestPluginHostTreeViewHandlerRejectsBadArguments(t *testing.T) {
	for _, args := range []string{`nope`, `{"pluginId":"a","extra":1}`} {
		if _, err := pluginHostTreeViewHandler("", args); contributionErrorCode(err) != contributionErrInvalidRequest {
			t.Errorf("%s: err = %v", args, err)
		}
	}
}
