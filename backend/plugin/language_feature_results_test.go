package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"mincode/app/plugins/api"
)

// languageTestRange encodes a range object.
func languageTestRange(startLine, startColumn, endLine, endColumn int) string {
	return fmt.Sprintf(`{"startLine":%d,"startColumn":%d,"endLine":%d,"endColumn":%d}`, startLine, startColumn, endLine, endColumn)
}

func languageTestArray(item string, count int) string {
	items := make([]string, count)
	for index := range items {
		items[index] = item
	}
	return "[" + strings.Join(items, ",") + "]"
}

func TestParseLanguageRange(t *testing.T) {
	got, err := parseLanguageRange(json.RawMessage(languageTestRange(3, 1, 3, 100000)))
	if err != nil || got != (LanguageRange{StartLine: 3, StartColumn: 1, EndLine: 3, EndColumn: 100000}) {
		t.Fatalf("valid range = %+v, %v", got, err)
	}
	if _, err := parseLanguageRange(json.RawMessage(languageTestRange(2, 9, 2, 9))); err != nil {
		t.Fatalf("empty range: %v", err)
	}
	invalid := map[string]string{
		"line 0":            languageTestRange(0, 1, 1, 1),
		"column 0":          languageTestRange(1, 0, 1, 1),
		"column 100001":     languageTestRange(1, 1, 1, 100001),
		"line past lineMax": languageTestRange(1, 1, languageFeatureLineMax+1, 1),
		"end before start":  languageTestRange(5, 1, 4, 1),
		"end column before": languageTestRange(5, 9, 5, 2),
		"missing field":     `{"startLine":1,"startColumn":1,"endLine":1}`,
		"fraction":          `{"startLine":1.5,"startColumn":1,"endLine":2,"endColumn":1}`,
		"string":            `{"startLine":"1","startColumn":1,"endLine":2,"endColumn":1}`,
		"unknown field":     `{"startLine":1,"startColumn":1,"endLine":1,"endColumn":1,"uri":"x"}`,
		"null":              `null`,
		"absent":            ``,
	}
	for name, raw := range invalid {
		if _, err := parseLanguageRange(json.RawMessage(raw)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A bad range drops the item, not the result.
	response, err := parseLanguageLocationsResult(json.RawMessage(`{"locations":[{"range":`+languageTestRange(0, 1, 1, 1)+`},{"range":`+languageTestRange(1, 1, 1, 2)+`}]}`), languageResultScope{}, 20, true)
	if err != nil || len(response.Locations) != 1 || response.Dropped != 1 {
		t.Fatalf("locations with a bad range = %+v, %v", response, err)
	}
}

// languageTestProject creates a project root holding the requesting
// document doc.yaml and returns a scope (with workspace.read) for it.
func languageTestProject(t *testing.T) (string, languageResultScope) {
	t.Helper()
	root := t.TempDir()
	languageTestWrite(t, filepath.Join(root, "doc.yaml"), "key: value")
	return root, languageResultScope{workspaceRead: true, projectRoot: root, documentPath: "doc.yaml"}
}

func languageTestWrite(t *testing.T, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func languageTestSymlink(t *testing.T, target, name string) {
	t.Helper()
	if err := os.Symlink(target, name); err != nil {
		t.Fatal(err)
	}
}

// languageTestKeeps reports whether a one-location result keeps its item
// (and fails the test on a whole-result error or a wrong dropped count).
func languageTestKeeps(t *testing.T, scope languageResultScope, target string) bool {
	t.Helper()
	encodedTarget, _ := json.Marshal(target)
	raw := fmt.Sprintf(`{"locations":[{"path":%s,"range":%s}]}`, encodedTarget, languageTestRange(1, 1, 1, 2))
	response, err := parseLanguageLocationsResult(json.RawMessage(raw), scope, 20, true)
	if err != nil {
		t.Fatalf("%q: %v", target, err)
	}
	if len(response.Locations)+response.Dropped != 1 {
		t.Fatalf("%q: response = %+v", target, response)
	}
	return len(response.Locations) == 1
}

func TestParseLanguageLocationsRejectsEscapes(t *testing.T) {
	_, scope := languageTestProject(t)
	r := languageTestRange(1, 1, 1, 5)
	escapes := []string{"/etc/passwd", "../x", "C:/x", `a\\b`, "https://x", "a/./b", strings.Repeat("a", 1025)}
	items := []string{}
	for _, escape := range escapes {
		items = append(items, fmt.Sprintf(`{"path":%q,"range":%s}`, escape, r))
	}
	items = append(items, `{"path":"src/a.go","range":`+r+`}`, `{"range":`+r+`}`)
	response, err := parseLanguageLocationsResult(json.RawMessage(`{"locations":[`+strings.Join(items, ",")+`]}`), scope, 500, false)
	if err != nil {
		t.Fatal(err)
	}
	if response.Dropped != 7 || len(response.Locations) != 2 || response.Truncated {
		t.Fatalf("response = %+v", response)
	}
	if response.Locations[0].Path != "src/a.go" || response.Locations[1].Path != "" {
		t.Fatalf("locations = %+v", response.Locations)
	}
	// More malicious targets, one at a time.
	malicious := map[string]string{
		"file uri":           "file:///etc/passwd",
		"scheme":             "vscode:extension",
		"windows abs":        `C:\\Windows\\win.ini`,
		"unc":                `\\\\server\\share`,
		"dot dot inside":     "src/../../x",
		"trailing slash":     "src/",
		"double slash":       "src//a.go",
		"dot":                ".",
		"empty":              "",
		"newline":            "src/a\n.go",
		"nul":                "src/a\x00.go",
		"bidi":               "src/\u202egol.exe",
		"zero width":         "src/a\u200b.go",
		"line separator":     "src/a\u2028.go",
		"percent encoded":    "%2e%2e/etc/passwd",
		"percent":            "src/a%20b.go",
		"query":              "src/a.go?x=1",
		"fragment":           "src/a.go#L1",
		"home":               "~/x",
		"variable":           "$HOME/x",
		"fullwidth dots":     "\uff0e\uff0e/x",
		"fullwidth slash":    "src\uff0fa.go",
		"division slash":     "src\u2215a.go",
		"fraction slash":     "src\u2044a.go",
		"one dot leader":     "\u2024\u2024/x",
		"fullwidth backslsh": "src\uff3ca.go",
		"env file":           ".env",
		"nested env":         "config/.env.local",
		"ssh key":            "home/id_rsa",
		"git internals":      ".git/config",
		"git upper":          "sub/.GIT/HEAD",
		"credentials":        "aws/credentials",
		"absolute nested":    "/src/a.go",
	}
	for name, target := range malicious {
		if languageTestKeeps(t, scope, target) {
			t.Errorf("%s (%q): kept", name, target)
		}
	}
	for _, target := range []string{"src/a.go", "docs/读我.md", "a b/c-d_e.f+g@h.txt", "src/~backup.go", "pkg/$x.go"} {
		if !languageTestKeeps(t, scope, target) {
			t.Errorf("%q: dropped", target)
		}
	}
	// Invalid UTF-8 in the path drops the item (encoding/json would turn it
	// into U+FFFD, which is still not a clean path the plugin asked for).
	invalidUTF8 := []byte(`{"locations":[{"path":"src/`)
	invalidUTF8 = append(invalidUTF8, 0xff, 0xfe)
	invalidUTF8 = append(invalidUTF8, []byte(`.go","range":`+r+`}]}`)...)
	if response, err := parseLanguageLocationsResult(invalidUTF8, scope, 20, true); err != nil || len(response.Locations) != 0 || response.Dropped != 1 {
		t.Errorf("invalid UTF-8 path: response = %+v, %v", response, err)
	}
	// Shapes that are not a locations result fail the whole result.
	for _, raw := range []string{`{}`, `{"locations":null}`, `[]`, `{"locations":[],"extra":1}`, `{"items":[]}`, `not json`} {
		if _, err := parseLanguageLocationsResult(json.RawMessage(raw), scope, 20, true); err == nil {
			t.Errorf("%s: accepted", raw)
		}
	}
	// An unknown item field (uri) drops only that item.
	response, err = parseLanguageLocationsResult(json.RawMessage(`{"locations":[{"uri":"file:///etc/passwd","range":`+r+`},{"range":`+r+`}]}`), scope, 20, true)
	if err != nil || len(response.Locations) != 1 || response.Dropped != 1 {
		t.Fatalf("uri item = %+v, %v", response, err)
	}
}

func TestParseLanguageLocationsRejectsSymlinkEscapes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	root, scope := languageTestProject(t)
	outside := t.TempDir()
	languageTestWrite(t, filepath.Join(outside, "secret.txt"), "x")
	languageTestWrite(t, filepath.Join(root, "src", "a.go"), "package a")
	languageTestWrite(t, filepath.Join(root, ".env"), "TOKEN=x")
	languageTestWrite(t, filepath.Join(root, ".git", "config"), "[core]")
	languageTestSymlink(t, outside, filepath.Join(root, "escape"))
	languageTestSymlink(t, filepath.Join(root, ".env"), filepath.Join(root, "innocent.txt"))
	languageTestSymlink(t, filepath.Join(root, ".git"), filepath.Join(root, "vcs"))
	languageTestSymlink(t, filepath.Join(root, "src", "a.go"), filepath.Join(root, "alias.go"))
	languageTestSymlink(t, filepath.Join(root, "src"), filepath.Join(root, "source"))
	languageTestSymlink(t, filepath.Join(outside, "missing.txt"), filepath.Join(root, "dangling-out"))
	languageTestSymlink(t, filepath.Join(root, "src", "missing.go"), filepath.Join(root, "dangling-in"))
	languageTestSymlink(t, "/etc", filepath.Join(root, "etc"))
	cases := map[string]bool{
		"escape/secret.txt":  false, // symlinked directory leaves the root
		"escape/nonexistent": false, // missing target under a directory outside the root
		"escape/a/b/c":       false, // deeper missing target, ancestor outside
		"etc/passwd":         false,
		"innocent.txt":       false, // symlink to a sensitive file inside the root
		"vcs/config":         false, // symlink into .git
		"vcs/missing":        false, // missing target whose resolved ancestor is .git
		"dangling-out":       false, // dangling symlink pointing outside
		"dangling-out/x":     false,
		"dangling-in":        false, // dangling symlinks are dropped
		"doc.yaml/x":         false, // a file used as a directory
		"src/a.go":           true,
		"alias.go":           true, // symlink that stays inside the root
		"source/a.go":        true,
		"source/new/file.go": true, // missing target under an in-root symlink
		"src/missing.go":     true, // not created yet: opening reports it
		"new/dir/file.go":    true,
	}
	for target, keep := range cases {
		if got := languageTestKeeps(t, scope, target); got != keep {
			t.Errorf("%s: kept %v, want %v", target, got, keep)
		}
	}
}

func TestLanguageLocationsResolveUnderDocumentProject(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	// Two open projects. The host's workspace root is A; the document lives
	// in B, so B is the root its locations are joined under.
	projectA := t.TempDir()
	languageTestWrite(t, filepath.Join(projectA, "a.go"), "package a")
	languageTestWrite(t, filepath.Join(projectA, "shared", "lib.go"), "package shared")
	projectB, scope := languageTestProject(t)
	languageTestWrite(t, filepath.Join(projectB, ".env"), "TOKEN=x")
	languageTestWrite(t, filepath.Join(projectB, "b.go"), "package b")
	languageTestSymlink(t, filepath.Join(projectA, "shared"), filepath.Join(projectB, "shared"))
	languageTestSymlink(t, filepath.Join(projectB, ".env"), filepath.Join(projectB, "settings.txt"))
	previousRoot := pluginWorkspaceRoot
	pluginWorkspaceRoot = func() string { return projectA }
	t.Cleanup(func() { pluginWorkspaceRoot = previousRoot })
	cases := map[string]bool{
		"b.go":          true,
		"shared/lib.go": false, // B's symlink into project A leaves B
		"shared/new.go": false,
		"settings.txt":  false, // B's symlink to B/.env
	}
	for target, keep := range cases {
		if got := languageTestKeeps(t, scope, target); got != keep {
			t.Errorf("%s: kept %v, want %v", target, got, keep)
		}
	}
	// Naming project A as the root of a document that is only in B fails
	// closed: the document must exist under the root.
	wrong := scope
	wrong.projectRoot = projectA
	if languageTestKeeps(t, wrong, "a.go") {
		t.Error("root that does not hold the document was used")
	}
}

func TestLanguageLocationsWithoutProjectRootFailClosed(t *testing.T) {
	root, scope := languageTestProject(t)
	r := languageTestRange(1, 1, 1, 2)
	raw := json.RawMessage(`{"locations":[{"path":"src/a.go","range":` + r + `},{"path":"src/b.go","range":` + r + `},{"range":` + r + `}]}`)
	home, _ := os.UserHomeDir()
	cases := map[string]languageResultScope{
		"no root":            {workspaceRead: true, documentPath: "doc.yaml"},
		"no document":        {workspaceRead: true, projectRoot: root},
		"missing document":   {workspaceRead: true, projectRoot: root, documentPath: "other.yaml"},
		"document is a dir":  {workspaceRead: true, projectRoot: filepath.Dir(root), documentPath: filepath.Base(root)},
		"filesystem root":    {workspaceRead: true, projectRoot: "/", documentPath: strings.TrimPrefix(filepath.Join(root, "doc.yaml"), "/")},
		"home":               {workspaceRead: true, projectRoot: home, documentPath: "doc.yaml"},
		"missing root":       {workspaceRead: true, projectRoot: filepath.Join(root, "gone"), documentPath: "doc.yaml"},
		"relative root":      {workspaceRead: true, projectRoot: "project", documentPath: "doc.yaml"},
		"document escapes":   {workspaceRead: true, projectRoot: filepath.Join(root, "sub"), documentPath: "../doc.yaml"},
		"no workspace.read ": {workspaceRead: false, projectRoot: root, documentPath: "doc.yaml"},
	}
	previousRoot := pluginWorkspaceRoot
	pluginWorkspaceRoot = func() string { return root }
	t.Cleanup(func() { pluginWorkspaceRoot = previousRoot })
	for name, scope := range cases {
		response, err := parseLanguageLocationsResult(raw, scope, 20, true)
		if err != nil || len(response.Locations) != 1 || response.Locations[0].Path != "" || response.Dropped != 2 {
			t.Errorf("%s: response = %+v, %v", name, response, err)
		}
	}
	if response, err := parseLanguageLocationsResult(raw, scope, 20, true); err != nil || len(response.Locations) != 3 {
		t.Fatalf("with a project root = %+v, %v", response, err)
	}
}

func TestLanguageLocationsResolveBudget(t *testing.T) {
	_, scope := languageTestProject(t)
	r := languageTestRange(1, 1, 1, 2)
	location := func(target string) string { return fmt.Sprintf(`{"path":%q,"range":%s}`, target, r) }
	raw := json.RawMessage(`{"locations":[` + strings.Join([]string{
		location("a.go"), location("b.go"), location("a.go"), location("c.go"), `{"range":` + r + `}`,
	}, ",") + `]}`)
	previousMax, previousBudget := languageLocationResolveMax, languageLocationResolveBudget
	t.Cleanup(func() { languageLocationResolveMax, languageLocationResolveBudget = previousMax, previousBudget })
	// Two distinct paths may be resolved; a repeated path uses the cache.
	languageLocationResolveMax = 2
	response, err := parseLanguageLocationsResult(raw, scope, 20, true)
	if err != nil || len(response.Locations) != 4 || response.Dropped != 1 || response.Locations[2].Path != "a.go" {
		t.Fatalf("distinct path cap = %+v, %v", response, err)
	}
	// A spent time budget drops every cross-file location.
	languageLocationResolveMax, languageLocationResolveBudget = previousMax, -time.Second
	response, err = parseLanguageLocationsResult(raw, scope, 20, true)
	if err != nil || len(response.Locations) != 1 || response.Dropped != 4 {
		t.Fatalf("time budget = %+v, %v", response, err)
	}
	// So does a cancelled request.
	languageLocationResolveBudget = previousBudget
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled := scope
	cancelled.ctx = ctx
	response, err = parseLanguageLocationsResult(raw, cancelled, 20, true)
	if err != nil || len(response.Locations) != 1 || response.Dropped != 4 {
		t.Fatalf("cancelled = %+v, %v", response, err)
	}
	// 500 distinct references stay within the default budget's cap.
	many := make([]string, 500)
	for index := range many {
		many[index] = location(fmt.Sprintf("pkg/f%d.go", index))
	}
	response, err = parseLanguageLocationsResult(json.RawMessage(`{"locations":[`+strings.Join(many, ",")+`]}`), scope, 500, false)
	if err != nil || len(response.Locations) != languageLocationResolveMax || response.Dropped != 500-languageLocationResolveMax {
		t.Fatalf("500 distinct = %d kept, %d dropped, %v", len(response.Locations), response.Dropped, err)
	}
}

func TestLanguageLocationsNeedWorkspaceRead(t *testing.T) {
	_, scope := languageTestProject(t)
	r := languageTestRange(4, 1, 4, 3)
	raw := json.RawMessage(`{"locations":[{"path":"src/a.go","range":` + r + `},{"range":` + r + `}]}`)
	without := scope
	without.workspaceRead = false
	response, err := parseLanguageLocationsResult(raw, without, 20, true)
	if err != nil || len(response.Locations) != 1 || response.Locations[0].Path != "" || response.Dropped != 1 {
		t.Fatalf("without workspace.read = %+v, %v", response, err)
	}
	response, err = parseLanguageLocationsResult(raw, scope, 20, true)
	if err != nil || len(response.Locations) != 2 || response.Dropped != 0 {
		t.Fatalf("with workspace.read = %+v, %v", response, err)
	}
}

func TestNormalizeLanguageFeatureRequestProjectRoot(t *testing.T) {
	definition := func() languageFeatureRequest {
		request := validLanguageFeatureRequest()
		request.Feature, request.ProjectRoot = "definition", "/Users/me/project"
		return request
	}
	if request := definition(); normalizeLanguageFeatureRequest(&request) != nil {
		t.Fatal("definition with projectRoot rejected")
	}
	cases := map[string]func(*languageFeatureRequest){
		"hover":           func(r *languageFeatureRequest) { r.Feature = "hover" },
		"codeLens":        func(r *languageFeatureRequest) { r.Feature, r.Position = "codeLens", nil },
		"no relativePath": func(r *languageFeatureRequest) { r.RelativePath = "" },
		"relative root":   func(r *languageFeatureRequest) { r.ProjectRoot = "project" },
		"newline in root": func(r *languageFeatureRequest) { r.ProjectRoot = "/Users/me/pro\nject" },
		"oversized root":  func(r *languageFeatureRequest) { r.ProjectRoot = "/" + strings.Repeat("a", 4096) },
		"invalid UTF-8":   func(r *languageFeatureRequest) { r.ProjectRoot = "/Users/\xff" },
	}
	for name, mutate := range cases {
		request := definition()
		mutate(&request)
		if err := normalizeLanguageFeatureRequest(&request); contributionErrorCode(err) != contributionErrInvalidRequest {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	// projectRoot never reaches the plugin.
	payload := languageFeaturePayload{Provider: "p", Feature: "definition", Text: "x", StartLine: 1, Locale: "en"}
	encoded, _ := encodeContributionJSON(payload)
	if strings.Contains(string(encoded), "projectRoot") {
		t.Fatalf("payload = %s", encoded)
	}
}

func TestParseLanguageDefinitionSelectionRange(t *testing.T) {
	outer := languageTestRange(2, 1, 6, 1)
	inside := languageTestRange(2, 5, 2, 9)
	outside := languageTestRange(1, 1, 1, 2)
	straddle := languageTestRange(5, 1, 7, 1)
	raw := `{"locations":[` +
		`{"range":` + outer + `,"selectionRange":` + inside + `},` +
		`{"range":` + outer + `,"selectionRange":` + outside + `},` +
		`{"range":` + outer + `,"selectionRange":` + straddle + `},` +
		`{"range":` + outer + `,"selectionRange":` + languageTestRange(0, 1, 1, 1) + `},` +
		`{"range":` + outer + `}]}`
	response, err := parseLanguageLocationsResult(json.RawMessage(raw), languageResultScope{}, languageFeatureDefinitionMax, true)
	if err != nil || len(response.Locations) != 2 || response.Dropped != 3 {
		t.Fatalf("definition = %+v, %v", response, err)
	}
	if selection := response.Locations[0].SelectionRange; selection == nil || *selection != (LanguageRange{StartLine: 2, StartColumn: 5, EndLine: 2, EndColumn: 9}) {
		t.Fatalf("selectionRange = %+v", selection)
	}
	if response.Locations[1].SelectionRange != nil {
		t.Fatalf("absent selectionRange = %+v", response.Locations[1].SelectionRange)
	}
	if languageFeatureDefinitionMax != 20 {
		t.Fatalf("definitionLocationsMax = %d", languageFeatureDefinitionMax)
	}
	many := `{"locations":` + languageTestArray(`{"range":`+outer+`}`, 21) + `}`
	response, err = parseLanguageLocationsResult(json.RawMessage(many), languageResultScope{}, languageFeatureDefinitionMax, true)
	if err != nil || len(response.Locations) != 20 || !response.Truncated || response.Dropped != 0 {
		t.Fatalf("21 definitions = %d, truncated %v, %v", len(response.Locations), response.Truncated, err)
	}
}

func TestParseLanguageReferencesCap(t *testing.T) {
	if languageFeatureReferencesMax != 500 {
		t.Fatalf("referencesLocationsMax = %d", languageFeatureReferencesMax)
	}
	r := languageTestRange(1, 1, 1, 2)
	many := `{"locations":` + languageTestArray(`{"range":`+r+`}`, 501) + `}`
	response, err := parseLanguageLocationsResult(json.RawMessage(many), languageResultScope{}, languageFeatureReferencesMax, false)
	if err != nil || len(response.Locations) != 500 || !response.Truncated {
		t.Fatalf("501 references = %d, truncated %v, %v", len(response.Locations), response.Truncated, err)
	}
	withSelection := `{"locations":[{"range":` + r + `,"selectionRange":` + r + `},{"range":` + r + `}]}`
	response, err = parseLanguageLocationsResult(json.RawMessage(withSelection), languageResultScope{}, languageFeatureReferencesMax, false)
	if err != nil || len(response.Locations) != 1 || response.Dropped != 1 {
		t.Fatalf("references with selectionRange = %+v, %v", response, err)
	}
}

// languageCodeLensScope mirrors a plugin's own commands: read commands with
// and without an inputSchema, a write and a dangerous command, a command
// whose action is dangerous, and a view-target command.
func languageCodeLensScope() languageResultScope {
	commands := map[string]ManifestCommand{
		"acme.run":       {ID: "acme.run", Action: "run", Risk: "read", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string", "maxLength": float64(16)}, "count": map[string]any{"type": "number"}, "text": map[string]any{"type": "string"}}, "required": []any{"name"}}},
		"acme.plain":     {ID: "acme.plain", Action: "run", Risk: "read"},
		"acme.write":     {ID: "acme.write", Action: "save", Risk: "write", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}}},
		"acme.danger":    {ID: "acme.danger", Action: "run", Risk: "dangerous"},
		"acme.escalated": {ID: "acme.escalated", Action: "wipe", Risk: "read", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}}},
		"acme.view":      {ID: "acme.view", Target: &ManifestCommandTarget{Type: "view", ViewID: "panel"}},
	}
	actions := map[string]ManifestUIAction{
		"run":  {ID: "run", Risk: "read"},
		"save": {ID: "save", Risk: "write"},
		"wipe": {ID: "wipe", Risk: "dangerous"},
	}
	return languageResultScope{commands: commands, actions: actions}
}

func TestParseLanguageCodeLensCommands(t *testing.T) {
	scope := languageCodeLensScope()
	r := languageTestRange(3, 1, 3, 1)
	lens := func(fields string) string { return `{"range":` + r + `,` + fields + `}` }
	cases := []struct {
		name string
		item string
		keep bool
	}{
		{"no command", lens(`"title":"12 references"`), true},
		{"write without args", lens(`"title":"Save","command":"acme.write"`), true},
		{"dangerous without args", lens(`"title":"Wipe","command":"acme.danger"`), true},
		{"read without args", lens(`"title":"Run","command":"acme.plain"`), true},
		{"read with valid args", lens(`"title":"Run","command":"acme.run","args":{"name":"build","count":2}`), true},
		{"other plugin's command", lens(`"title":"Run","command":"other.plugin.run"`), false},
		{"host command", lens(`"title":"Run","command":"mincode.pluginCodeLens"`), false},
		{"undeclared command", lens(`"title":"Run","command":"acme.missing"`), false},
		{"view target command", lens(`"title":"Open","command":"acme.view"`), false},
		{"url command", lens(`"title":"Open","command":"https://example.com"`), false},
		{"empty command", lens(`"title":"Run","command":""`), false},
		{"write with args", lens(`"title":"Save","command":"acme.write","args":{"name":"x"}`), false},
		{"write with empty args", lens(`"title":"Save","command":"acme.write","args":{}`), false},
		{"dangerous action with args", lens(`"title":"Run","command":"acme.escalated","args":{"name":"x"}`), false},
		{"read without schema with args", lens(`"title":"Run","command":"acme.plain","args":{"name":"x"}`), false},
		{"args not in schema", lens(`"title":"Run","command":"acme.run","args":{"name":"x","other":1}`), false},
		{"args wrong type", lens(`"title":"Run","command":"acme.run","args":{"name":1}`), false},
		{"args missing required", lens(`"title":"Run","command":"acme.run","args":{"count":1}`), false},
		{"args over maxLength", lens(`"title":"Run","command":"acme.run","args":{"name":"` + strings.Repeat("x", 17) + `"}`), false},
		{"args nested object", lens(`"title":"Run","command":"acme.run","args":{"name":{"x":1}}`), false},
		{"args 1024 bytes", lens(`"title":"Run","command":"acme.run","args":{"name":"a","text":"` + strings.Repeat("x", 1002) + `"}`), true},
		{"args 1025 bytes", lens(`"title":"Run","command":"acme.run","args":{"name":"a","text":"` + strings.Repeat("x", 1003) + `"}`), false},
		{"args 9 properties", lens(`"title":"Run","command":"acme.run","args":{"name":"a","a1":1,"a2":1,"a3":1,"a4":1,"a5":1,"a6":1,"a7":1,"a8":1}`), false},
		{"args without command", lens(`"title":"Run","args":{"name":"x"}`), false},
		{"args null", lens(`"title":"Run","command":"acme.run","args":null`), false},
		{"title codicon", lens(`"title":"$(play) Run"`), false},
		{"title codicon later", lens(`"title":"Run $(debug)","command":"acme.plain"`), false},
		{"title 65 characters", lens(`"title":"` + strings.Repeat("é", 65) + `"`), false},
		{"title 64 characters", lens(`"title":"` + strings.Repeat("é", 64) + `"`), true},
		{"title blank", lens(`"title":"   "`), false},
		{"title missing", lens(`"command":"acme.plain"`), false},
		{"title newline", lens(`"title":"Run\nnow"`), false},
		{"title bidi", lens(`"title":"Run \u202eevil"`), false},
		{"title soft hyphen (Cf)", lens(`"title":"Ru\u00adn"`), false},
		{"title line separator", lens(`"title":"Run\u2028x"`), false},
		{"bad range", `{"range":` + languageTestRange(3, 2, 3, 1) + `,"title":"x"}`, false},
		{"unknown field", lens(`"title":"Run","arguments":["x"]`), false},
	}
	for _, test := range cases {
		response, err := parseLanguageCodeLensResult(json.RawMessage(`{"lenses":[`+test.item+`]}`), scope)
		if err != nil {
			t.Errorf("%s: err = %v", test.name, err)
			continue
		}
		if (len(response.Lenses) == 1) != test.keep || response.Dropped != map[bool]int{true: 0, false: 1}[test.keep] {
			t.Errorf("%s: response = %+v, keep %v", test.name, response, test.keep)
		}
	}
	response, err := parseLanguageCodeLensResult(json.RawMessage(`{"lenses":[`+lens(`"title":" Run ","command":"acme.run","args":{"name":"build"}`)+`]}`), scope)
	if err != nil || len(response.Lenses) != 1 {
		t.Fatalf("lens = %+v, %v", response, err)
	}
	got := response.Lenses[0]
	if got.Title != "Run" || got.Command != "acme.run" || got.Args["name"] != "build" || got.Range != (LanguageRange{StartLine: 3, StartColumn: 1, EndLine: 3, EndColumn: 1}) {
		t.Fatalf("lens = %+v", got)
	}
	// Invalid UTF-8 in a title drops the lens.
	invalid := append([]byte(`{"lenses":[{"range":`+r+`,"title":"R`), 0xc3, 0x28)
	invalid = append(invalid, []byte(`"}]}`)...)
	if response, err := parseLanguageCodeLensResult(invalid, scope); err != nil || len(response.Lenses) != 0 || response.Dropped != 1 {
		t.Fatalf("invalid UTF-8 title = %+v, %v", response, err)
	}
	many := `{"lenses":` + languageTestArray(lens(`"title":"x"`), 101) + `}`
	response, err = parseLanguageCodeLensResult(json.RawMessage(many), scope)
	if err != nil || len(response.Lenses) != 100 || !response.Truncated {
		t.Fatalf("101 lenses = %d, truncated %v, %v", len(response.Lenses), response.Truncated, err)
	}
	for _, raw := range []string{`{}`, `{"lenses":{}}`, `{"locations":[]}`} {
		if _, err := parseLanguageCodeLensResult(json.RawMessage(raw), scope); err == nil {
			t.Errorf("%s: accepted", raw)
		}
	}
}

func TestParseLanguageInlayHints(t *testing.T) {
	scope := languageResultScope{hintRange: &languageFeatureRange{StartLine: 10, EndLine: 20}}
	hint := func(line, column int, rest string) string {
		return fmt.Sprintf(`{"position":{"line":%d,"column":%d},%s}`, line, column, rest)
	}
	cases := []struct {
		name string
		item string
		keep bool
	}{
		{"plain", hint(10, 1, `"label":": int"`), true},
		{"type kind with padding", hint(20, 5, `"label":"T","kind":"type","paddingLeft":true,"paddingRight":false`), true},
		{"parameter kind", hint(15, 100000, `"label":"name:","kind":"parameter"`), true},
		{"enum kind", hint(12, 1, `"label":"x","kind":"enum"`), false},
		{"label newline", hint(12, 1, `"label":"a\nb"`), false},
		{"label empty", hint(12, 1, `"label":""`), false},
		{"label 65 characters", hint(12, 1, `"label":"`+strings.Repeat("x", 65)+`"`), false},
		{"label codicon is plain text", hint(12, 1, `"label":"$(x)"`), true},
		{"label zero width", hint(12, 1, `"label":"a\u200db"`), false},
		{"label object", hint(12, 1, `"label":[{"label":"x"}]`), false},
		{"tooltip", hint(12, 1, `"label":"x","tooltip":"y"`), false},
		{"before range", hint(9, 1, `"label":"x"`), false},
		{"after range", hint(21, 1, `"label":"x"`), false},
		{"column 0", hint(12, 0, `"label":"x"`), false},
		{"column 100001", hint(12, 100001, `"label":"x"`), false},
		{"position missing column", `{"position":{"line":12},"label":"x"}`, false},
		{"position missing", `{"label":"x"}`, false},
	}
	for _, test := range cases {
		response, err := parseLanguageInlayHintsResult(json.RawMessage(`{"hints":[`+test.item+`]}`), scope)
		if err != nil {
			t.Errorf("%s: err = %v", test.name, err)
			continue
		}
		if (len(response.Hints) == 1) != test.keep || response.Dropped != map[bool]int{true: 0, false: 1}[test.keep] {
			t.Errorf("%s: response = %+v, keep %v", test.name, response, test.keep)
		}
	}
	response, err := parseLanguageInlayHintsResult(json.RawMessage(`{"hints":[`+hint(20, 5, `"label":" T ","kind":"type","paddingLeft":true`)+`]}`), scope)
	if err != nil || len(response.Hints) != 1 {
		t.Fatal(response, err)
	}
	if got := response.Hints[0]; got.Label != "T" || got.Kind != "type" || !got.PaddingLeft || got.PaddingRight || got.Position != (languageFeaturePosition{Line: 20, Column: 5}) {
		t.Fatalf("hint = %+v", got)
	}
	many := `{"hints":` + languageTestArray(hint(10, 1, `"label":"x"`), 301) + `}`
	response, err = parseLanguageInlayHintsResult(json.RawMessage(many), scope)
	if err != nil || len(response.Hints) != 300 || !response.Truncated {
		t.Fatalf("301 hints = %d, truncated %v, %v", len(response.Hints), response.Truncated, err)
	}
	if _, err := parseLanguageInlayHintsResult(json.RawMessage(`{"hints":null}`), scope); err == nil {
		t.Fatal("null hints accepted")
	}
}

// languageTestSymbol encodes a symbol with optional extra fields and children.
func languageTestSymbol(name, kind, extra string, children ...string) string {
	value := fmt.Sprintf(`{"name":%q,"kind":%q,"range":%s`, name, kind, languageTestRange(1, 1, 50, 1))
	if extra != "" {
		value += "," + extra
	}
	if children != nil {
		value += `,"children":[` + strings.Join(children, ",") + `]`
	}
	return value + "}"
}

func TestParseLanguageDocumentSymbols(t *testing.T) {
	if languageFeatureSymbolsMax != 500 || languageFeatureSymbolDepthMax != 8 {
		t.Fatalf("limits = %d, %d", languageFeatureSymbolsMax, languageFeatureSymbolDepthMax)
	}
	raw := `{"symbols":[` +
		languageTestSymbol("Server", "class", `"detail":"struct"`, languageTestSymbol("Run", "method", ""), languageTestSymbol("bad", "widget", "")) + `,` +
		languageTestSymbol("Widget", "widget", "", languageTestSymbol("child", "field", "")) +
		`]}`
	response, err := parseLanguageDocumentSymbolResult(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Symbols) != 1 || response.Dropped != 2 || response.Truncated {
		t.Fatalf("symbols = %+v", response)
	}
	server := response.Symbols[0]
	if server.Name != "Server" || server.Detail != "struct" || server.Kind != "class" || len(server.Children) != 1 || server.Children[0].Name != "Run" {
		t.Fatalf("server = %+v", server)
	}
	if server.SelectionRange != server.Range {
		t.Fatalf("selectionRange default = %+v, range %+v", server.SelectionRange, server.Range)
	}
	// Every contract symbol kind is accepted.
	for _, kind := range languageFeatureSpec.List("symbolKinds") {
		if response, err := parseLanguageDocumentSymbolResult(json.RawMessage(`{"symbols":[` + languageTestSymbol("x", kind, "") + `]}`)); err != nil || len(response.Symbols) != 1 {
			t.Errorf("kind %s: %+v, %v", kind, response, err)
		}
	}
	invalid := map[string]string{
		"selection outside":     languageTestSymbol("x", "class", `"selectionRange":`+languageTestRange(60, 1, 60, 2)),
		"name blank":            languageTestSymbol(" ", "class", ""),
		"name 129 characters":   languageTestSymbol(strings.Repeat("n", 129), "class", ""),
		"detail 129 characters": languageTestSymbol("x", "class", `"detail":"`+strings.Repeat("d", 129)+`"`),
		"name newline":          languageTestSymbol("a\nb", "class", ""),
		"detail bidi":           languageTestSymbol("x", "class", `"detail":"\u2066x"`),
		"kind missing":          `{"name":"x","range":` + languageTestRange(1, 1, 1, 1) + `}`,
		"kind number":           `{"name":"x","kind":5,"range":` + languageTestRange(1, 1, 1, 1) + `}`,
		"children object":       languageTestSymbol("x", "class", `"children":{}`),
		"unknown field":         languageTestSymbol("x", "class", `"tags":[1]`),
	}
	for name, symbol := range invalid {
		response, err := parseLanguageDocumentSymbolResult(json.RawMessage(`{"symbols":[` + symbol + `]}`))
		if err != nil || len(response.Symbols) != 0 || response.Dropped != 1 {
			t.Errorf("%s: response = %+v, %v", name, response, err)
		}
	}
	selected, err := parseLanguageDocumentSymbolResult(json.RawMessage(`{"symbols":[` + languageTestSymbol("x", "class", `"selectionRange":`+languageTestRange(3, 2, 3, 6)) + `]}`))
	if err != nil || len(selected.Symbols) != 1 || selected.Symbols[0].SelectionRange != (LanguageRange{StartLine: 3, StartColumn: 2, EndLine: 3, EndColumn: 6}) {
		t.Fatalf("selectionRange = %+v, %v", selected, err)
	}
	// Nine levels: the ninth is cut and the result marked truncated.
	nested := languageTestSymbol("level9", "field", "")
	for level := 8; level >= 1; level-- {
		nested = languageTestSymbol(fmt.Sprintf("level%d", level), "class", "", nested)
	}
	response, err = parseLanguageDocumentSymbolResult(json.RawMessage(`{"symbols":[` + nested + `]}`))
	if err != nil || !response.Truncated || response.Dropped != 0 {
		t.Fatalf("nested = %+v, %v", response, err)
	}
	depth, node := 0, response.Symbols
	for len(node) > 0 {
		depth++
		node = node[0].Children
	}
	if depth != 8 {
		t.Fatalf("depth = %d, want 8", depth)
	}
	// Deep nesting far beyond the cap is cut without recursing into it.
	deep := `{"name":"x","kind":"class","range":` + languageTestRange(1, 1, 1, 1)
	deep = strings.Repeat(deep+`,"children":[`, 2000) + deep + "}" + strings.Repeat("]}", 2000)
	if response, err := parseLanguageDocumentSymbolResult(json.RawMessage(`{"symbols":[` + deep + `]}`)); err != nil || !response.Truncated {
		t.Fatalf("deep nesting = truncated %v, %v", response.Truncated, err)
	}
	// 501 nodes in total (one parent and 500 children): 500 are kept.
	children := make([]string, 500)
	for index := range children {
		children[index] = languageTestSymbol(fmt.Sprintf("c%d", index), "field", "")
	}
	response, err = parseLanguageDocumentSymbolResult(json.RawMessage(`{"symbols":[` + languageTestSymbol("parent", "class", "", children...) + `]}`))
	if err != nil || !response.Truncated || countLanguageSymbols(response.Symbols) != 500 {
		t.Fatalf("501 nodes = %d, truncated %v, %v", countLanguageSymbols(response.Symbols), response.Truncated, err)
	}
	invalidUTF8 := append([]byte(`{"symbols":[{"name":"`), 0xff)
	invalidUTF8 = append(invalidUTF8, []byte(`","kind":"class","range":`+languageTestRange(1, 1, 1, 1)+`}]}`)...)
	if response, err := parseLanguageDocumentSymbolResult(invalidUTF8); err != nil || len(response.Symbols) != 0 || response.Dropped != 1 {
		t.Fatalf("invalid UTF-8 name = %+v, %v", response, err)
	}
	if _, err := parseLanguageDocumentSymbolResult(json.RawMessage(`{"symbols":null}`)); err == nil {
		t.Fatal("null symbols accepted")
	}
}

func countLanguageSymbols(symbols []LanguageSymbol) int {
	count := 0
	for _, symbol := range symbols {
		count += 1 + countLanguageSymbols(symbol.Children)
	}
	return count
}

func TestPluginLanguageFeatureNewKindsEndToEnd(t *testing.T) {
	manager, record := startedContributionManager(t)
	previousRoot := pluginWorkspaceRoot
	pluginWorkspaceRoot = func() string { return "" }
	t.Cleanup(func() { pluginWorkspaceRoot = previousRoot })
	record.mu.Lock()
	record.manifest.contributions = map[string]*resolvedContribution{languageFeaturePointKey: {Entries: []any{LanguageFeatureContribution{
		ID: "docs", Title: "Docs", Languages: []string{"yaml"}, Features: []string{"definition", "references", "codeLens", "inlayHints", "documentSymbol"}, Action: "run",
	}}}}
	record.mu.Unlock()
	base := func(feature string) languageFeatureRequest {
		request := validLanguageFeatureRequest()
		request.PluginID, request.Feature, request.Text = record.id, feature, "helper-langfeat\nline two"
		request.Position = &languageFeaturePosition{Line: 1, Column: 1}
		return request
	}
	// The helper answers each kind with one valid item, one item to drop
	// (a cross-file path, another plugin's command, a hint outside the
	// range, an unknown symbol kind) and, for definition, a path.
	definition, err := manager.languageFeature(context.Background(), base("definition"))
	if err != nil || len(definition.Locations) != 1 || definition.Locations[0].Path != "" || definition.Locations[0].SelectionRange == nil || definition.Dropped != 1 {
		t.Fatalf("definition = %+v, %v", definition, err)
	}
	include := true
	references := base("references")
	references.IncludeDeclaration = &include
	refs, err := manager.languageFeature(context.Background(), references)
	if err != nil || len(refs.Locations) != 1 || refs.Dropped != 1 {
		t.Fatalf("references = %+v, %v", refs, err)
	}
	lenses := base("codeLens")
	lenses.Position = nil
	lensResponse, err := manager.languageFeature(context.Background(), lenses)
	if err != nil || len(lensResponse.Lenses) != 2 || lensResponse.Lenses[1].Command != "run" || lensResponse.Dropped != 1 {
		t.Fatalf("codeLens = %+v, %v", lensResponse, err)
	}
	hints := base("inlayHints")
	hints.Position, hints.Range = nil, &languageFeatureRange{StartLine: 1, EndLine: 1}
	hintResponse, err := manager.languageFeature(context.Background(), hints)
	if err != nil || len(hintResponse.Hints) != 1 || hintResponse.Hints[0].Kind != "type" || hintResponse.Dropped != 1 {
		t.Fatalf("inlayHints = %+v, %v", hintResponse, err)
	}
	symbols := base("documentSymbol")
	symbols.Position = nil
	symbolResponse, err := manager.languageFeature(context.Background(), symbols)
	if err != nil || len(symbolResponse.Symbols) != 1 || symbolResponse.Symbols[0].Name != "task" || symbolResponse.Dropped != 1 {
		t.Fatalf("documentSymbol = %+v, %v", symbolResponse, err)
	}
	// With workspace.read and the document's project root the cross-file
	// definition is kept; without the root it is still dropped.
	record.mu.Lock()
	record.manifest.Capabilities = append(record.manifest.Capabilities, api.CapabilityWorkspaceRead)
	record.mu.Unlock()
	definition, err = manager.languageFeature(context.Background(), base("definition"))
	if err != nil || len(definition.Locations) != 1 || definition.Dropped != 1 {
		t.Fatalf("definition with workspace.read, no projectRoot = %+v, %v", definition, err)
	}
	project := t.TempDir()
	languageTestWrite(t, filepath.Join(project, "a.yaml"), "key: value")
	rooted := base("definition")
	rooted.ProjectRoot = project
	definition, err = manager.languageFeature(context.Background(), rooted)
	if err != nil || len(definition.Locations) != 2 || definition.Locations[1].Path != "src/a.yaml" || definition.Dropped != 0 {
		t.Fatalf("definition with workspace.read = %+v, %v", definition, err)
	}
	// The response carries the parsed fields under their JSON names.
	encoded, err := json.Marshal(symbolResponse)
	if err != nil || !strings.Contains(string(encoded), `"symbols":[{"name":"task","kind":"function","range":{"startLine":1,"startColumn":1,"endLine":1,"endColumn":5},"selectionRange":{"startLine":1,"startColumn":1,"endLine":1,"endColumn":5}}]`) {
		t.Fatalf("encoded = %s, %v", encoded, err)
	}
	encoded, _ = json.Marshal(lensResponse)
	if !strings.Contains(string(encoded), `"lenses":[{"range":`) || !strings.Contains(string(encoded), `"title":"Run","command":"run"`) {
		t.Fatalf("encoded lenses = %s", encoded)
	}
	assertNoContributionRequestsLeft(t)
}
