package plugin

import (
	"encoding/json"
	"strings"
	"testing"

	"mincode/app/plugins/api"
)

// writeMenusManifest writes an API v2 process plugin with two action commands
// ("example.run", "example.inspect") and one view command ("example.open").
func writeMenusManifest(t *testing.T, menus string, capabilities ...api.Capability) string {
	t.Helper()
	return writeTestManifestV2(t, func(manifest *Manifest) {
		manifest.Capabilities = append(manifest.Capabilities, capabilities...)
		manifest.Contributes.Commands = append(manifest.Contributes.Commands,
			ManifestCommand{ID: "example.run", Title: "Run", Action: "run", TitleLocalized: map[string]string{"zh": "运行"}},
			ManifestCommand{ID: "example.inspect", Title: "Inspect", Action: "inspect", Risk: "write"},
		)
		manifest.Contributes.Points = map[string]json.RawMessage{"menus": json.RawMessage(menus)}
	})
}

func TestMenusDecodeValidItems(t *testing.T) {
	root := writeMenusManifest(t, `{
		"editor/context": [{"command": "example.run", "when": "editor.languageId == \"go\" && editor.hasSelection", "group": "navigation", "order": 5, "passContext": true}],
		"sql/editor/context": [{"command": "example.inspect", "when": "database.kind == \"mysql\""}],
		"project/tree/context": [{"command": "example.open", "when": "resource.kind == \"file\" && !resource.isProjectRoot"}],
		"workbench/tab/context": [{"command": "example.inspect", "when": "tab.view == \"projects\" && myplugin.ready", "passContext": true}],
		"terminal/context": [{"command": "example.run", "when": "terminal.kind == \"ssh\" && platform == \"mac\""}],
		"commandPalette": [{"command": "example.inspect", "hidden": true, "group": "z", "order": 1000}]
	}`, api.CapabilityWorkspaceRead)
	manifest, err := readManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.contributionIssuesError(); err != nil {
		t.Fatal(err)
	}
	items := manifest.MenuItems()
	if len(items) != 6 {
		t.Fatalf("items = %+v", items)
	}
	byMenu := map[string]MenuItemContribution{}
	for _, item := range items {
		byMenu[item.Menu] = item
	}
	editor := byMenu[menuEditorContext]
	if editor.Command != "example.run" || editor.Title != "Run" || editor.TitleLocalized["zh"] != "运行" ||
		editor.Group != "navigation" || editor.Order != 5 || !editor.PassContext {
		t.Fatalf("editor item = %+v", editor)
	}
	if palette := byMenu[menuCommandPalette]; !palette.Hidden || palette.Order != 1000 || palette.Group != "z" {
		t.Fatalf("palette item = %+v", palette)
	}
	encoded, err := json.Marshal(byMenu[menuSQLEditorContext])
	if err != nil || string(encoded) != `{"menu":"sql/editor/context","command":"example.inspect","title":"Inspect","when":"database.kind == \"mysql\"","order":0}` {
		t.Fatalf("encoded = %s %v", encoded, err)
	}
}

func TestMenusRejectInvalidItems(t *testing.T) {
	cases := map[string]struct {
		menus    string
		contains string
	}{
		"unknown command":        {`{"editor/context":[{"command":"example.missing"}]}`, "not one of this plugin's contributes.commands"},
		"builtin command":        {`{"editor/context":[{"command":"workbench:close-tab"}]}`, "not one of this plugin's contributes.commands"},
		"qualified own command":  {`{"editor/context":[{"command":"plugin:example.plugin:example.run"}]}`, "not one of this plugin's contributes.commands"},
		"missing command":        {`{"editor/context":[{"when":"platform"}]}`, "command is required"},
		"passContext no read":    {`{"project/tree/context":[{"command":"example.run","passContext":true}]}`, "requires the workspace.read capability"},
		"passContext terminal":   {`{"terminal/context":[{"command":"example.run","passContext":true}]}`, "requires the workspace.read capability"},
		"passContext view":       {`{"workbench/tab/context":[{"command":"example.open","passContext":true}]}`, "requires an action command"},
		"hidden outside palette": {`{"editor/context":[{"command":"example.run","hidden":false}]}`, "only supported in commandPalette"},
		"palette when":           {`{"commandPalette":[{"command":"example.run","when":"platform"}]}`, "accepts only group, order and hidden"},
		"palette passContext":    {`{"commandPalette":[{"command":"example.run","passContext":false}]}`, "accepts only group, order and hidden"},
		"bad group":              {`{"editor/context":[{"command":"example.run","group":"Bad Group"}]}`, "group"},
		"order too high":         {`{"editor/context":[{"command":"example.run","order":1001}]}`, "order must be 0-1000"},
		"order negative":         {`{"editor/context":[{"command":"example.run","order":-1}]}`, "order must be 0-1000"},
		"bad when":               {`{"editor/context":[{"command":"example.run","when":"editor.kind =="}]}`, "when"},
		"when too long":          {`{"editor/context":[{"command":"example.run","when":"` + strings.Repeat("a", 513) + `"}]}`, "exceeds 512"},
		"foreign host key":       {`{"editor/context":[{"command":"example.run","when":"terminal.kind == \"ssh\""}]}`, `context key "terminal.kind" is not provided`},
		"sql key in editor":      {`{"editor/context":[{"command":"example.run","when":"database.kind"}]}`, `context key "database.kind" is not provided`},
		"unknown field":          {`{"editor/context":[{"command":"example.run","icon":"x"}]}`, "unknown field"},
		"duplicate":              {`{"editor/context":[{"command":"example.run"},{"command":"example.run","order":2}]}`, "more than once"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			manifest, err := readManifest(writeMenusManifest(t, tc.menus))
			if err != nil {
				t.Fatalf("lenient load failed: %v", err)
			}
			strict := manifest.contributionIssuesError()
			if strict == nil || !strings.Contains(strict.Error(), "invalid menus contribution") || !strings.Contains(strict.Error(), tc.contains) {
				t.Fatalf("strict error = %v, want %q", strict, tc.contains)
			}
		})
	}
}

func TestMenusIssuesKeepValidItemsAndSameCommandAcrossMenus(t *testing.T) {
	manifest, err := readManifest(writeMenusManifest(t, `{
		"editor/context": [{"command": "example.run"}, {"command": "example.missing"}],
		"terminal/context": [{"command": "example.run"}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if items := manifest.MenuItems(); len(items) != 2 {
		t.Fatalf("items = %+v", items)
	}
	issues := manifest.allContributionIssues()
	if len(issues) != 1 || issues[0].Key != menuEditorContext || issues[0].Entry != 1 {
		t.Fatalf("issues = %+v", issues)
	}
	if text := contributionIssueText(issues[0]); !strings.Contains(text, `contributes.menus["editor/context"][1]`) {
		t.Fatalf("issue text = %q", text)
	}
}

func TestMenusStructuralRules(t *testing.T) {
	cases := map[string]struct {
		mutate   func(*Manifest)
		contains string
	}{
		"unknown menu": {func(manifest *Manifest) {
			manifest.Contributes.Points = map[string]json.RawMessage{"menus": json.RawMessage(`{"explorer/context":[]}`)}
		}, `unknown key "explorer/context"`},
		"too many per menu": {func(manifest *Manifest) {
			items := make([]string, 17)
			for index := range items {
				items[index] = `{"command":"example.run"}`
			}
			manifest.Contributes.Points = map[string]json.RawMessage{"menus": json.RawMessage(`{"editor/context":[` + strings.Join(items, ",") + `]}`)}
		}, "at most 16"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := readManifest(writeTestManifestV2(t, tc.mutate))
			if err == nil || !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("err = %v, want %q", err, tc.contains)
			}
		})
	}
}

func TestWhenAcceptsCamelCaseHostKeysOnly(t *testing.T) {
	if err := validateWhenExpression(`editor.languageId == "go" && resource.isProjectRoot`); err != nil {
		t.Fatal(err)
	}
	if err := validateWhenExpression(`Editor.kind`); err == nil {
		t.Fatal("key starting with an upper-case letter accepted")
	}
	for _, key := range []string{"sideBarVisible", "editorTextFocus", "a" + strings.Repeat(".b", 32)} {
		if err := validateWhenExpression(key); err == nil {
			t.Fatalf("when key %q accepted", key)
		}
	}
	for _, key := range []string{"acme.mode", "a..b", "terminal.selection", "x-y_z.q"} {
		if err := validateWhenExpression(key); err != nil {
			t.Fatalf("when key %q rejected: %v", key, err)
		}
	}
	if pluginContextKeyPattern.MatchString("my.camelKey") {
		t.Fatal("plugin context keys must stay lower-case")
	}
}

func TestMenuTrayEntryRules(t *testing.T) {
	writeTrayManifest := func(t *testing.T, menus string) string {
		t.Helper()
		return writeTestManifestV2(t, func(manifest *Manifest) {
			for _, id := range []string{"a", "b", "c", "d", "e"} {
				manifest.Contributes.Commands = append(manifest.Contributes.Commands, ManifestCommand{ID: id, Title: strings.ToUpper(id), Action: "run." + id})
			}
			manifest.Contributes.Points = map[string]json.RawMessage{"menus": json.RawMessage(menus)}
		})
	}
	for name, menus := range map[string]string{
		"command only":  `{"tray":[{"command":"a"}]}`,
		"group order":   `{"tray":[{"command":"a","group":"g","order":3}]}`,
		"view command":  `{"tray":[{"command":"example.open"}]}`,
		"four items":    `{"tray":[{"command":"a"},{"command":"b"},{"command":"c"},{"command":"example.open"}]}`,
		"with palette":  `{"tray":[{"command":"a"}],"commandPalette":[{"command":"a","hidden":true}]}`,
		"with editor":   `{"tray":[{"command":"a"}],"editor/context":[{"command":"a","when":"editor.hasSelection"}]}`,
		"other menu 16": `{"tray":[{"command":"a"}],"terminal/context":[{"command":"a"},{"command":"b"},{"command":"c"},{"command":"d"},{"command":"e"}]}`,
		"empty when":    `{"tray":[{"command":"a","when":""}]}`,
		"null fields":   `{"tray":[{"command":"a","when":null,"passContext":null,"hidden":null}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			manifest, err := readManifest(writeTrayManifest(t, menus))
			if err != nil {
				t.Fatal(err)
			}
			if err := manifest.contributionIssuesError(); err != nil {
				t.Fatal(err)
			}
			for _, item := range manifest.MenuItems() {
				if item.Menu == menuTray && (item.When != "" || item.PassContext || item.Hidden) {
					t.Fatalf("tray item = %+v", item)
				}
			}
		})
	}
	for name, tc := range map[string]struct {
		menus    string
		contains string
	}{
		"when":                {`{"tray":[{"command":"a","when":"myplugin.ready"}]}`, `tray item "a" accepts only command, group and order`},
		"passContext":         {`{"tray":[{"command":"a","passContext":false}]}`, `tray item "a" accepts only command, group and order`},
		"hidden":              {`{"tray":[{"command":"a","hidden":false}]}`, `tray item "a" accepts only command, group and order`},
		"five items":          {`{"tray":[{"command":"a"},{"command":"b"},{"command":"c"},{"command":"d"},{"command":"e"}]}`, "menu tray allows at most 4 items"},
		"unknown command":     {`{"tray":[{"command":"missing"}]}`, "not one of this plugin's contributes.commands"},
		"unknown field":       {`{"tray":[{"command":"a","title":"Spoof"}]}`, "unknown field"},
		"duplicate":           {`{"tray":[{"command":"a"},{"command":"a"}]}`, "more than once"},
		"when on view target": {`{"tray":[{"command":"example.open","when":"platform"}]}`, `tray item "example.open" accepts only command, group and order`},
	} {
		t.Run(name, func(t *testing.T) {
			manifest, err := readManifest(writeTrayManifest(t, tc.menus))
			if err != nil {
				t.Fatalf("lenient load failed: %v", err)
			}
			strict := manifest.contributionIssuesError()
			if strict == nil || !strings.Contains(strict.Error(), tc.contains) {
				t.Fatalf("strict error = %v, want %q", strict, tc.contains)
			}
		})
	}
	// Lenient loading skips the fifth item and keeps the first four.
	manifest, err := readManifest(writeTrayManifest(t, `{"tray":[{"command":"a"},{"command":"b"},{"command":"c"},{"command":"d"},{"command":"e"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if items := manifest.MenuItems(); len(items) != 4 || items[3].Command != "d" {
		t.Fatalf("items = %+v", items)
	}
	if _, ok := menuHostContextKeys[menuTray]; ok {
		t.Fatal("tray has no host context keys")
	}
	if _, ok := menuPassContextCapability[menuTray]; ok {
		t.Fatal("tray has no passContext")
	}
	if menuTrayMaxPerPlugin != 4 {
		t.Fatalf("menuTrayMaxPerPlugin = %d", menuTrayMaxPerPlugin)
	}
}
