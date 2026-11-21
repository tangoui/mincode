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

// writeAiPromptPackage writes a declarative package whose contributes.aiPrompts
// are `entries`; files are extra package files.
func writeAiPromptPackage(t *testing.T, entries []any, files map[string]string) string {
	t.Helper()
	return writeKeymapPackage(t, "", func(manifest map[string]any) {
		manifest["id"] = "ai-prompt-test"
		manifest["contributes"] = map[string]any{"aiPrompts": entries}
	}, nil, files)
}

func aiPromptEntry(id, kind string, fields map[string]any) map[string]any {
	entry := map[string]any{"id": id, "kind": kind, "title": "Title " + id}
	for key, value := range fields {
		entry[key] = value
	}
	return entry
}

func TestAiPromptsDeclarativePackageStrictAndSnapshot(t *testing.T) {
	useTemporaryPluginData(t)
	skill := "\ufeff---\r\nname: sql\r\nallowed-tools: [\"shell\"]\r\n---\r\n\r\n# SQL tuning\r\nPrefer indexes.\r\n"
	root := writeAiPromptPackage(t, []any{
		aiPromptEntry("explain", "prompt", map[string]any{
			"template":       "Explain {{ selection }} in {{file}} ({{language}}). Keep {{selection}} literal braces {x}.",
			"titleLocalized": map[string]string{"zh": "解释"},
		}),
		aiPromptEntry("review", "prompt", map[string]any{"path": "prompts/review.md", "description": "Review code"}),
		aiPromptEntry("sql-tuning", "skill", map[string]any{"path": "skills/sql.md", "description": "SQL tuning", "descriptionLocalized": map[string]string{"zh": "SQL 调优"}}),
	}, map[string]string{
		"prompts/review.md": "Review this:\n{{selection}}\n",
		"skills/sql.md":     skill,
	})
	inspection, err := inspectInstallDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	resolved := inspection.Manifest.contribution(aiPromptPointKey)
	if resolved == nil || len(resolved.Entries) != 3 || len(resolved.Issues) != 0 {
		t.Fatalf("resolved = %+v", resolved)
	}
	explain := resolved.Entries[0].(AiPromptContribution)
	if strings.Join(explain.Variables, ",") != "selection,file,language" || explain.TitleLocalized["zh"] != "解释" {
		t.Fatalf("explain = %+v", explain)
	}
	sql := resolved.Entries[2].(AiPromptContribution)
	if sql.Body != "# SQL tuning\nPrefer indexes.\n" || len(sql.Variables) != 0 || strings.Contains(sql.Body, "allowed-tools") {
		t.Fatalf("skill body = %q", sql.Body)
	}
	if len(inspection.Manifest.aiPrompts()) != 3 {
		t.Fatal("aiPrompts accessor")
	}

	manager := newPluginManager()
	manifest, err := manager.InstallLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetEnabled(context.Background(), manifest.ID, true); err != nil {
		t.Fatal(err)
	}
	snapshot := manager.Contributions([]string{aiPromptPointKey})
	if len(snapshot.Plugins) != 1 || len(snapshot.Plugins[0].Points[aiPromptPointKey].Entries) != 3 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	encoded, err := json.Marshal(snapshot.Plugins[0].Points[aiPromptPointKey].Entries[1])
	if err != nil || !strings.Contains(string(encoded), `"kind":"prompt"`) || !strings.Contains(string(encoded), `"variables":["selection"]`) ||
		strings.Contains(string(encoded), `"path"`) {
		t.Fatalf("encoded = %s %v", encoded, err)
	}
}

func TestAiPromptsProcessPluginAccepted(t *testing.T) {
	root := writeTestManifest(t, func(manifest *Manifest) {
		manifest.APIVersion = api.ManifestAPIVersionV2
		manifest.Contributes.Points = map[string]json.RawMessage{"aiPrompts": json.RawMessage(`[{"id":"fix","kind":"prompt","title":"Fix","template":"Fix {{selection}}"}]`)}
	})
	manifest, err := readManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.contributionIssuesError(); err != nil || len(manifest.contribution(aiPromptPointKey).Entries) != 1 {
		t.Fatalf("issues = %v", err)
	}
}

func TestAiPromptsEntryValidation(t *testing.T) {
	long := func(n int) string { return strings.Repeat("a", n) }
	cases := map[string]struct {
		entry map[string]any
		want  string
	}{
		"bad id":               {aiPromptEntry("Bad_ID", "prompt", map[string]any{"template": "x"}), "must match"},
		"dotted id":            {aiPromptEntry("a.b", "prompt", map[string]any{"template": "x"}), "must match"},
		"bad kind":             {aiPromptEntry("a", "agent", map[string]any{"template": "x"}), "kind"},
		"blank title":          {map[string]any{"id": "a", "kind": "prompt", "title": " ", "template": "x"}, "title"},
		"long title":           {map[string]any{"id": "a", "kind": "prompt", "title": long(aiPromptMaxTitleRunes + 1), "template": "x"}, "exceeds"},
		"title newline":        {map[string]any{"id": "a", "kind": "prompt", "title": "a\nb", "template": "x"}, "single line"},
		"title bidi":           {map[string]any{"id": "a", "kind": "prompt", "title": "a\u202eb", "template": "x"}, "bidirectional"},
		"long description":     {aiPromptEntry("a", "prompt", map[string]any{"template": "x", "description": long(aiPromptMaxDescription + 1)}), "description"},
		"bad localized":        {aiPromptEntry("a", "prompt", map[string]any{"template": "x", "titleLocalized": map[string]string{"zh": ""}}), "titleLocalized"},
		"skill no description": {aiPromptEntry("a", "skill", map[string]any{"path": "skills/a.md"}), "description"},
		"skill template":       {aiPromptEntry("a", "skill", map[string]any{"template": "x", "description": "d"}), "Markdown file"},
		"template and path":    {aiPromptEntry("a", "prompt", map[string]any{"template": "x", "path": "prompts/a.md"}), "exactly one"},
		"neither":              {aiPromptEntry("a", "prompt", nil), "exactly one"},
		"unknown field":        {aiPromptEntry("a", "prompt", map[string]any{"template": "x", "model": "gpt"}), "unknown field"},
		"unknown variable":     {aiPromptEntry("a", "prompt", map[string]any{"template": "Run {{clipboard}}"}), "{{clipboard}}"},
		"env variable":         {aiPromptEntry("a", "prompt", map[string]any{"template": "{{ env.HOME }}"}), "not allowed"},
		"empty template":       {aiPromptEntry("a", "prompt", map[string]any{"template": "  \n"}), "empty"},
		"large template":       {aiPromptEntry("a", "prompt", map[string]any{"template": long(aiPromptMaxTemplateBytes + 1)}), "exceeds"},
		"zero width":           {aiPromptEntry("a", "prompt", map[string]any{"template": "a\u200bb"}), "zero-width"},
		"bidi isolate":         {aiPromptEntry("a", "prompt", map[string]any{"template": "a\u2067b"}), "U+2067"},
		"nul":                  {aiPromptEntry("a", "prompt", map[string]any{"template": "a\u0000b"}), "control"},
		"c1":                   {aiPromptEntry("a", "prompt", map[string]any{"template": "a\u0085b"}), "control"},
		"line separator":       {aiPromptEntry("a", "prompt", map[string]any{"template": "a\u2028b"}), "U+2028"},
		"inner bom":            {aiPromptEntry("a", "prompt", map[string]any{"template": "a\ufeffb"}), "U+FEFF"},
		"wrong extension":      {aiPromptEntry("a", "prompt", map[string]any{"path": "prompts/a.txt"}), ".md"},
		"missing file":         {aiPromptEntry("a", "prompt", map[string]any{"path": "prompts/missing.md"}), "missing.md"},
		"escape":               {aiPromptEntry("a", "prompt", map[string]any{"path": "../a.md"}), "a.md"},
		"hidden in file":       {aiPromptEntry("a", "skill", map[string]any{"path": "skills/hidden.md", "description": "d"}), "U+202E"},
		"unclosed frontmatter": {aiPromptEntry("a", "skill", map[string]any{"path": "skills/open.md", "description": "d"}), "frontmatter"},
		"frontmatter only":     {aiPromptEntry("a", "skill", map[string]any{"path": "skills/empty.md", "description": "d"}), "empty"},
		"invalid utf8 file":    {aiPromptEntry("a", "skill", map[string]any{"path": "skills/latin1.md", "description": "d"}), "UTF-8"},
		"file variable":        {aiPromptEntry("a", "prompt", map[string]any{"path": "prompts/unknown.md"}), "{{shell}}"},
	}
	files := map[string]string{
		"skills/a.md":        "guidance",
		"prompts/a.txt":      "x",
		"prompts/unknown.md": "{{shell}}",
		"skills/hidden.md":   "safe \u202e text",
		"skills/open.md":     "---\nallowed-tools: all\nbody",
		"skills/empty.md":    "---\nname: x\n---\n\n",
		"skills/latin1.md":   "caf\xe9",
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			manifest, err := readManifest(writeAiPromptPackage(t, []any{testCase.entry}, files))
			if err != nil {
				t.Fatal(err)
			}
			resolved := manifest.contribution(aiPromptPointKey)
			if len(resolved.Entries) != 0 || len(resolved.Issues) != 1 || !strings.Contains(resolved.Issues[0].Message, testCase.want) {
				t.Fatalf("entries = %d issues = %+v, want %q", len(resolved.Entries), resolved.Issues, testCase.want)
			}
		})
	}
	// Exactly at the limits is accepted; skills are literal (no variable check).
	root := writeAiPromptPackage(t, []any{
		map[string]any{"id": strings.Repeat("a", 48), "kind": "prompt", "title": long(aiPromptMaxTitleRunes), "description": long(aiPromptMaxDescription), "template": long(aiPromptMaxTemplateBytes)},
		aiPromptEntry("literal", "skill", map[string]any{"path": "skills/literal.md", "description": "d"}),
		aiPromptEntry("tabs", "prompt", map[string]any{"template": "a\tb\r\nc {{ not closed"}),
	}, map[string]string{"skills/literal.md": "Use {{anything}} as written."})
	manifest, err := readManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	resolved := manifest.contribution(aiPromptPointKey)
	if len(resolved.Entries) != 3 || len(resolved.Issues) != 0 {
		t.Fatalf("entries = %d issues = %+v", len(resolved.Entries), resolved.Issues)
	}
	if body := resolved.Entries[2].(AiPromptContribution).Body; body != "a\tb\nc {{ not closed" {
		t.Fatalf("body = %q", body)
	}
}

func TestAiPromptsDuplicateAndStrictInstall(t *testing.T) {
	root := writeAiPromptPackage(t, []any{
		aiPromptEntry("a", "prompt", map[string]any{"template": "x"}),
		aiPromptEntry("a", "prompt", map[string]any{"template": "y"}),
	}, nil)
	manifest, err := readManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	resolved := manifest.contribution(aiPromptPointKey)
	if len(resolved.Entries) != 1 || len(resolved.Issues) != 1 || !strings.Contains(resolved.Issues[0].Message, "duplicate AI prompt id") {
		t.Fatalf("resolved = %+v", resolved)
	}
	if _, err := inspectInstallDirectory(root); err == nil || !strings.Contains(err.Error(), "invalid aiPrompts contribution") {
		t.Fatalf("strict err = %v", err)
	}
	if got := contributionIssueText(resolved.Issues[0]); !strings.Contains(got, `skipped AI prompt "a"`) {
		t.Fatalf("issue text = %q", got)
	}
}

func TestAiPromptsSymlinkRejected(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := writeAiPromptPackage(t, []any{aiPromptEntry("a", "prompt", map[string]any{"path": "prompts/link.md"})}, map[string]string{"prompts/keep.md": "x"})
	if err := os.Symlink(outside, filepath.Join(root, "prompts", "link.md")); err != nil {
		t.Skip("symlinks unavailable")
	}
	manifest, err := readManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if resolved := manifest.contribution(aiPromptPointKey); len(resolved.Entries) != 0 || len(resolved.Issues) != 1 {
		t.Fatalf("resolved = %+v", resolved)
	}
}

func TestAiPromptsPerPluginTotal(t *testing.T) {
	body := strings.Repeat("a", aiPromptMaxSkillBytes)
	files := map[string]string{}
	entries := []any{}
	count := aiPromptMaxPluginBytes/aiPromptMaxSkillBytes + 1
	for index := 0; index < count; index++ {
		path := fmt.Sprintf("skills/s%d.md", index)
		files[path] = body
		entries = append(entries, aiPromptEntry(fmt.Sprintf("s%d", index), "skill", map[string]any{"path": path, "description": "d"}))
	}
	manifest, err := readManifest(writeAiPromptPackage(t, entries, files))
	if err != nil {
		t.Fatal(err)
	}
	resolved := manifest.contribution(aiPromptPointKey)
	if len(resolved.Entries) != count-1 || len(resolved.Issues) != 1 || !strings.Contains(resolved.Issues[0].Message, "in total") {
		t.Fatalf("entries = %d issues = %+v", len(resolved.Entries), resolved.Issues)
	}
}

func TestAiPromptsListLimitIsFatal(t *testing.T) {
	entries := make([]any, aiPromptSpec.MaxEntries+1)
	for index := range entries {
		entries[index] = aiPromptEntry(fmt.Sprintf("p%d", index), "prompt", map[string]any{"template": "x"})
	}
	if _, err := readManifest(writeAiPromptPackage(t, entries, nil)); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("err = %v", err)
	}
}
