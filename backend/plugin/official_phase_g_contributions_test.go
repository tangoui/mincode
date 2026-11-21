package plugin

import (
	"path/filepath"
	"slices"
	"testing"

	"mincode/app/plugins/api"
)

// Phase G adoption: the official plugins and the showcase templates that
// declare the Phase G contribution points must pass strict host inspection,
// and every palette/context action they add must stay host-only and read-only.

func phaseGEntries[T any](manifest Manifest, key string) []T {
	resolved := manifest.contribution(key)
	if resolved == nil {
		return nil
	}
	items := make([]T, 0, len(resolved.Entries))
	for _, entry := range resolved.Entries {
		switch value := entry.(type) {
		case T:
			items = append(items, value)
		case *T:
			items = append(items, *value)
		}
	}
	return items
}

func phaseGRequireNoIssues(t *testing.T, name string, manifest Manifest) {
	t.Helper()
	if issues := manifest.allContributionIssues(); len(issues) != 0 {
		t.Fatalf("%s contribution issues: %+v", name, issues)
	}
}

func phaseGRequireHostOnlyReadAction(t *testing.T, name string, manifest Manifest, actionID string) {
	t.Helper()
	for _, action := range manifest.Contributes.Actions {
		if action.ID != actionID {
			continue
		}
		if action.Risk != "read" || action.UICallable || action.AICallable {
			t.Fatalf("%s action %q must be read, not UI-callable and not AI-callable: %+v", name, actionID, action)
		}
		return
	}
	t.Fatalf("%s misses action %q", name, actionID)
}

func TestOfficialPhaseGSearchAndContextProviders(t *testing.T) {
	cases := []struct {
		id      string
		prefix  string
		mention string
	}{
		{id: "docker-manager", prefix: "docker", mention: "docker"},
		{id: "kubernetes-manager", prefix: "k8s", mention: "k8s"},
		{id: "knowledge-base", prefix: "kb"},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			manifest := readContributionManifestForTest(t, filepath.Join("..", "..", "official-plugins", tc.id))
			phaseGRequireNoIssues(t, tc.id, manifest)

			search := phaseGEntries[SearchProviderContribution](manifest, searchProviderPointKey)
			if len(search) != 1 || search[0].Prefix != tc.prefix || search[0].Action != "palette.search" {
				t.Fatalf("search providers = %+v", search)
			}
			phaseGRequireHostOnlyReadAction(t, tc.id, manifest, search[0].Action)

			contexts := phaseGEntries[AiContextProviderContribution](manifest, aiContextProviderPointKey)
			if tc.mention == "" {
				if len(contexts) != 0 {
					t.Fatalf("unexpected AI context providers = %+v", contexts)
				}
				return
			}
			if len(contexts) != 1 || contexts[0].Mention != tc.mention || contexts[0].Action != "ai.context" {
				t.Fatalf("AI context providers = %+v", contexts)
			}
			phaseGRequireHostOnlyReadAction(t, tc.id, manifest, contexts[0].Action)
		})
	}
}

func TestOfficialPhaseGDeclarativeShowcaseTemplate(t *testing.T) {
	inspection, err := inspectInstallDirectory(filepath.Join("..", "..", "tools", "plugin-templates", "contributions-showcase"))
	if err != nil {
		t.Fatal(err)
	}
	manifest := inspection.Manifest
	phaseGRequireNoIssues(t, "contributions-showcase", manifest)

	schemes := phaseGEntries[TerminalColorSchemeContribution](manifest, terminalColorSchemePointKey)
	var schemeKinds []string
	for _, scheme := range schemes {
		schemeKinds = append(schemeKinds, scheme.ID+"="+scheme.Kind)
	}
	if !slices.Equal(schemeKinds, []string{"contributions-showcase.mcdemo-dark=dark", "contributions-showcase.mcdemo-light=light"}) {
		t.Fatalf("terminal color schemes = %v", schemeKinds)
	}

	generators := manifest.httpCodeGenerators()
	if len(generators) != 2 ||
		generators[0].ID != "httpie" || generators[0].Escape != "shell" ||
		generators[1].ID != "python-requests" || generators[1].Escape != "json" ||
		len(generators[0].Template) != 64 || len(generators[1].Template) != 64 {
		t.Fatalf("HTTP code generators = %+v", generators)
	}
}

func TestOfficialPhaseGProcessShowcaseTemplate(t *testing.T) {
	inspection, err := inspectInstallDirectory(filepath.Join("..", "..", "tools", "plugin-templates", "contributions-process-showcase"))
	if err != nil {
		t.Fatal(err)
	}
	manifest := inspection.Manifest
	name := "contributions-process-showcase"
	phaseGRequireNoIssues(t, name, manifest)

	for _, capability := range []string{"shell", "workspace.read"} {
		if !slices.ContainsFunc(manifest.Capabilities, func(value api.Capability) bool { return string(value) == capability }) {
			t.Fatalf("capabilities %v miss %q", manifest.Capabilities, capability)
		}
	}

	linters := phaseGEntries[LinterContribution](manifest, linterPointKey)
	if len(linters) != 1 || linters[0].ID != "mcdemo" || !slices.Equal(linters[0].Triggers, []string{"open", "save", "type"}) {
		t.Fatalf("linters = %+v", linters)
	}
	phaseGRequireHostOnlyReadAction(t, name, manifest, linters[0].Action)

	search := phaseGEntries[SearchProviderContribution](manifest, searchProviderPointKey)
	if len(search) != 1 || search[0].Prefix != "demo" {
		t.Fatalf("search providers = %+v", search)
	}
	phaseGRequireHostOnlyReadAction(t, name, manifest, search[0].Action)

	contexts := phaseGEntries[AiContextProviderContribution](manifest, aiContextProviderPointKey)
	if len(contexts) != 1 || contexts[0].Mention != "demo" {
		t.Fatalf("AI context providers = %+v", contexts)
	}
	phaseGRequireHostOnlyReadAction(t, name, manifest, contexts[0].Action)

	handlers := phaseGEntries[UriHandlerContribution](manifest, uriHandlerPointKey)
	if len(handlers) != 1 || handlers[0].Path != "/counter/show" ||
		handlers[0].Target.Type != "command" || handlers[0].Target.Command != "contributions-process-showcase.show" {
		t.Fatalf("URI handlers = %+v", handlers)
	}

	tasks := phaseGEntries[TaskContribution](manifest, taskPointKey)
	var taskKinds []string
	for _, task := range tasks {
		taskKinds = append(taskKinds, task.ID+"="+task.Kind)
		if task.Kind == "provider" {
			phaseGRequireHostOnlyReadAction(t, name, manifest, task.Action)
		}
	}
	if !slices.Equal(taskKinds, []string{"node-version=static", "npm=provider"}) {
		t.Fatalf("tasks = %v", taskKinds)
	}

	servers := phaseGEntries[LanguageServerContribution](manifest, languageServerPointKey)
	if len(servers) != 1 || servers[0].ID != "mcdemo" || servers[0].Command != "node" ||
		!slices.Equal(servers[0].Args, []string{"${pluginRoot}/lib/mcdemo-lsp.mjs", "--stdio"}) {
		t.Fatalf("language servers = %+v", servers)
	}

	// Shell extensions (host 0.3.0): the settings page and the tray items
	// pass the strict install preview above.
	pages := manifest.SettingsPages()
	if len(pages) != 1 || pages[0].View != "settings" {
		t.Fatalf("settings pages = %+v", pages)
	}
	var tray []string
	for _, item := range manifest.MenuItems() {
		if item.Menu == menuTray {
			tray = append(tray, item.Command)
		}
	}
	if !slices.Equal(tray, []string{"contributions-process-showcase.toggle", "contributions-process-showcase.openSettings"}) {
		t.Fatalf("tray items = %v", tray)
	}
	if !slices.ContainsFunc(manifest.Capabilities, func(value api.Capability) bool { return value == "notifications" }) {
		t.Fatalf("capabilities %v miss notifications", manifest.Capabilities)
	}

	// Data providers (host 0.3.0): one entry per point, each on a host-only
	// read action, and the three install preview lines.
	sources := manifest.SessionSources()
	if len(sources) != 1 || sources[0].ID != "showcase" || sources[0].Icon != "magic" || sources[0].Mark != "SH" {
		t.Fatalf("session sources = %+v", sources)
	}
	phaseGRequireHostOnlyReadAction(t, name, manifest, sources[0].Action)
	commitProviders := manifest.ScmCommitMessageProviders()
	if len(commitProviders) != 1 || commitProviders[0].ID != "showcase" {
		t.Fatalf("commit message providers = %+v", commitProviders)
	}
	phaseGRequireHostOnlyReadAction(t, name, manifest, commitProviders[0].Action)
	decorationProviders := manifest.FileDecorationProviders()
	if len(decorationProviders) != 1 || decorationProviders[0].ID != "showcase" {
		t.Fatalf("file decoration providers = %+v", decorationProviders)
	}
	phaseGRequireHostOnlyReadAction(t, name, manifest, decorationProviders[0].Action)
	var previewLines []string
	for _, line := range installPreviewDataProviders(manifest) {
		previewLines = append(previewLines, line.Point+"="+line.LocalizedTitle([]string{"zh"}))
	}
	if !slices.Equal(previewLines, []string{"sessionSources=示例会话", "scmCommitMessage=示例提交信息", "fileDecorations=示例标记"}) {
		t.Fatalf("install preview data providers = %v", previewLines)
	}
}
