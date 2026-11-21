package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mincode/app/plugins/api"
)

func snippetFileJSON(t *testing.T, snippets map[string]any) string {
	t.Helper()
	data, err := json.Marshal(snippets)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// writeSnippetPackage writes a declarative package whose contributes.snippets
// are `entries`; files are extra package files.
func writeSnippetPackage(t *testing.T, entries []any, files map[string]string) string {
	t.Helper()
	return writeKeymapPackage(t, "", func(manifest map[string]any) {
		manifest["id"] = "snippet-test"
		manifest["contributes"] = map[string]any{"snippets": entries}
	}, nil, files)
}

func snippetEntry(language, path string) map[string]string {
	return map[string]string{"language": language, "path": path}
}

func TestSnippetsDeclarativePackageStrictAndSnapshot(t *testing.T) {
	useTemporaryPluginData(t)
	ts := `{"Log value":{"prefix":["log","clog"],"body":["console.log($1);","$0"],"description":"Log to the console"},"For loop":{"prefix":"fori","body":"for (let ${1:i} = 0; ${1:i} < ${2:n}; ${1:i}++) {\n\t$0\n}"}}`
	sql := `{"Select all":{"prefix":"sel*","body":"SELECT * FROM ${1:table};","description":["Select every","column"]}}`
	root := writeSnippetPackage(t, []any{snippetEntry("typescript", "snippets/typescript.json"), snippetEntry("sql", "assets/sql.json")},
		map[string]string{"snippets/typescript.json": ts, "assets/sql.json": sql})
	inspection, err := inspectInstallDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	resolved := inspection.Manifest.contribution(snippetPointKey)
	if resolved == nil || len(resolved.Entries) != 2 || len(resolved.Issues) != 0 {
		t.Fatalf("resolved = %+v", resolved)
	}
	first := resolved.Entries[0].(SnippetContribution)
	if first.Language != "typescript" || first.Path != "snippets/typescript.json" || len(first.Snippets) != 2 {
		t.Fatalf("first = %+v", first)
	}
	// File order is kept; arrays are normalized.
	if first.Snippets[0].Name != "Log value" || strings.Join(first.Snippets[0].Prefixes, ",") != "log,clog" ||
		first.Snippets[0].Body != "console.log($1);\n$0" || first.Snippets[1].Prefixes[0] != "fori" {
		t.Fatalf("snippets = %+v", first.Snippets)
	}
	if second := resolved.Entries[1].(SnippetContribution); second.Snippets[0].Description != "Select every\ncolumn" {
		t.Fatalf("second = %+v", second)
	}

	manager := newPluginManager()
	manifest, err := manager.InstallLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetEnabled(context.Background(), manifest.ID, true); err != nil {
		t.Fatal(err)
	}
	snapshot := manager.Contributions([]string{snippetPointKey})
	if len(snapshot.Plugins) != 1 || len(snapshot.Plugins[0].Points[snippetPointKey].Entries) != 2 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	encoded, err := json.Marshal(snapshot.Plugins[0].Points[snippetPointKey].Entries[0])
	if err != nil || !strings.Contains(string(encoded), `"prefixes":["log","clog"]`) || !strings.Contains(string(encoded), `"language":"typescript"`) {
		t.Fatalf("encoded = %s %v", encoded, err)
	}
}

func TestSnippetsProcessPluginAccepted(t *testing.T) {
	root := writeTestManifest(t, func(manifest *Manifest) {
		manifest.APIVersion = api.ManifestAPIVersionV2
		manifest.Contributes.Points = map[string]json.RawMessage{"snippets": json.RawMessage(`[{"language":"python","path":"snippets/python.json"}]`)}
	})
	if err := os.MkdirAll(filepath.Join(root, "snippets"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "snippets", "python.json"), []byte(`{"Main":{"prefix":"main","body":"if __name__ == \"__main__\":\n\t${1:pass}"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := readManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.contributionIssuesError(); err != nil || len(manifest.contribution(snippetPointKey).Entries) != 1 {
		t.Fatalf("issues = %v", err)
	}
}

func TestSnippetsStructureAndBounds(t *testing.T) {
	long := func(n int) string { return strings.Repeat("a", n) }
	manyPrefixes := make([]string, snippetMaxPrefixes+1)
	for index := range manyPrefixes {
		manyPrefixes[index] = fmt.Sprintf("p%d", index)
	}
	tooMany := map[string]any{}
	for index := 0; index <= snippetMaxPerFile; index++ {
		tooMany[fmt.Sprintf("s%04d", index)] = map[string]any{"prefix": "p", "body": "x"}
	}
	cases := map[string]struct {
		content string
		want    string
	}{
		"jsonc comment":        {`{// comment` + "\n" + `"a":{"prefix":"a","body":"b"}}`, "strict JSON"},
		"block comment":        {`{"a":{"prefix":"a","body":"b"}} /* x */`, "exactly one JSON object"},
		"trailing comma":       {`{"a":{"prefix":"a","body":"b"},}`, "strict JSON"},
		"not an object":        {`[]`, "JSON object of named snippets"},
		"empty":                {`{}`, "no snippets"},
		"duplicate name":       {`{"a":{"prefix":"a","body":"b"},"a":{"prefix":"c","body":"d"}}`, "duplicate snippet name"},
		"missing prefix":       {`{"a":{"body":"b"}}`, "prefixes"},
		"empty prefix list":    {`{"a":{"prefix":[],"body":"b"}}`, "prefixes"},
		"prefix whitespace":    {`{"a":{"prefix":"a b","body":"b"}}`, "without whitespace"},
		"prefix too long":      {snippetFileJSON(t, map[string]any{"a": map[string]any{"prefix": long(snippetMaxPrefixLength + 1), "body": "b"}}), "without whitespace"},
		"too many prefixes":    {snippetFileJSON(t, map[string]any{"a": map[string]any{"prefix": manyPrefixes, "body": "b"}}), "prefixes"},
		"prefix number":        {`{"a":{"prefix":3,"body":"b"}}`, "string or a list"},
		"missing body":         {`{"a":{"prefix":"a"}}`, "needs a body"},
		"blank body":           {`{"a":{"prefix":"a","body":["  "]}}`, "must not be empty"},
		"body object":          {`{"a":{"prefix":"a","body":{"x":1}}}`, "string or a list"},
		"body too large":       {snippetFileJSON(t, map[string]any{"a": map[string]any{"prefix": "a", "body": long(snippetMaxBodyBytes + 1)}}), "exceeds"},
		"body control":         {`{"a":{"prefix":"a","body":"x\u0000"}}`, "control characters"},
		"description too long": {snippetFileJSON(t, map[string]any{"a": map[string]any{"prefix": "a", "body": "b", "description": long(snippetMaxDescriptionRune + 1)}}), "description"},
		"unknown field":        {`{"a":{"prefix":"a","body":"b","scope":"x"}}`, "unknown field"},
		"too many snippets":    {snippetFileJSON(t, tooMany), fmt.Sprintf("at most %d snippets", snippetMaxPerFile)},
		"invalid utf8":         {"{\"a\":{\"prefix\":\"a\",\"body\":\"\xff\"}}", "UTF-8"},
		"blank name":           {`{" ":{"prefix":"a","body":"b"}}`, "snippet name"},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseSnippetFile([]byte(testCase.content)); err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("err = %v, want %q", err, testCase.want)
			}
		})
	}
	// Exactly at the limits is accepted.
	atLimit := map[string]any{"a": map[string]any{
		"prefix": manyPrefixes[:snippetMaxPrefixes], "body": long(snippetMaxBodyBytes), "description": long(snippetMaxDescriptionRune),
	}}
	if _, err := parseSnippetFile([]byte(snippetFileJSON(t, atLimit))); err != nil {
		t.Fatal(err)
	}
}

func TestSnippetsEntryAndFileReferenceIssues(t *testing.T) {
	good := `{"a":{"prefix":"a","body":"b"}}`
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	root := writeSnippetPackage(t, []any{
		snippetEntry("typescript", "snippets/good.json"),
		snippetEntry("Type Script", "snippets/good.json"),                      // bad language id
		snippetEntry("go", "snippets/missing.json"),                            // missing file
		snippetEntry("go", "snippets/good.txt"),                                // wrong extension
		snippetEntry("go", "../outside.json"),                                  // escapes the package
		snippetEntry("go", "snippets/big.json"),                                // oversize
		map[string]any{"language": "go", "path": "snippets/good.json", "x": 1}, // unknown field
		snippetEntry("typescript", "snippets/good.json"),                       // duplicate
		snippetEntry("go", "snippets/link.json"),                               // symlink
		snippetEntry("rust", "snippets/bad.json"),                              // bad structure
	}, map[string]string{
		"snippets/good.json": good,
		"snippets/good.txt":  good,
		"snippets/big.json":  strings.Repeat(" ", int(snippetSpec.FileRef.MaxBytes)+1),
		"snippets/bad.json":  `{"a":{"prefix":"a","body":7}}`,
	})
	symlinkOK := os.Symlink(outside, filepath.Join(root, "snippets", "link.json")) == nil
	manifest, err := readManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	resolved := manifest.contribution(snippetPointKey)
	wantIssues := 9
	if !symlinkOK {
		wantIssues = 8
	}
	if len(resolved.Entries) != 1 || len(resolved.Issues) < wantIssues {
		t.Fatalf("entries = %d issues = %+v", len(resolved.Entries), resolved.Issues)
	}
	text := fmt.Sprint(resolved.Issues)
	for _, want := range []string{"snippet language", "duplicate snippet file entry", "unknown field", "exceeds", "package-relative"} {
		if !strings.Contains(text, want) {
			t.Fatalf("issues %s do not mention %q", text, want)
		}
	}
	// Install is strict (the symlink alone already fails the package walk).
	_ = os.Remove(filepath.Join(root, "snippets", "link.json"))
	if _, err := inspectInstallDirectory(root); err == nil || !strings.Contains(err.Error(), "invalid snippets contribution") {
		t.Fatalf("strict err = %v", err)
	}
	if got := contributionIssueText(resolved.Issues[0]); !strings.Contains(got, "skipped snippet file") {
		t.Fatalf("issue text = %q", got)
	}
}

func TestSnippetsPerPluginTotal(t *testing.T) {
	file := map[string]any{}
	for index := 0; index < snippetMaxPerFile; index++ {
		file[fmt.Sprintf("s%04d", index)] = map[string]any{"prefix": "p", "body": "x"}
	}
	content := snippetFileJSON(t, file)
	files := map[string]string{}
	entries := []any{}
	for index := 0; index <= snippetMaxPerPlugin/snippetMaxPerFile; index++ {
		path := fmt.Sprintf("snippets/f%d.json", index)
		files[path] = content
		entries = append(entries, snippetEntry("plaintext", path))
	}
	manifest, err := readManifest(writeSnippetPackage(t, entries, files))
	if err != nil {
		t.Fatal(err)
	}
	resolved := manifest.contribution(snippetPointKey)
	if len(resolved.Entries) != snippetMaxPerPlugin/snippetMaxPerFile || len(resolved.Issues) != 1 ||
		!strings.Contains(resolved.Issues[0].Message, "in total") {
		t.Fatalf("entries = %d issues = %+v", len(resolved.Entries), resolved.Issues)
	}
}

func TestSnippetsListLimitIsFatal(t *testing.T) {
	entries := make([]any, snippetSpec.MaxEntries+1)
	for index := range entries {
		entries[index] = snippetEntry("go", "snippets/a.json")
	}
	if _, err := readManifest(writeSnippetPackage(t, entries, map[string]string{"snippets/a.json": `{"a":{"prefix":"a","body":"b"}}`})); err == nil ||
		!strings.Contains(err.Error(), fmt.Sprintf("at most %d snippet files", snippetSpec.MaxEntries)) {
		t.Fatalf("err = %v", err)
	}
}
