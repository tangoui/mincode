package api

import (
	"slices"
	"testing"
)

func TestContributionPointSpecsDecode(t *testing.T) {
	specs := ContributionPointSpecs()
	want := []string{"acpAgents", "aiCatalogs", "aiContextProviders", "aiPrompts", "aiProviders", "commandSnippets", "configuration", "customEditors", "dashboardWidgets", "databaseDrivers", "debuggers", "editorFrameworks", "editorThemes", "externalOpenHandlers", "fileDecorations", "fileIcons", "fileTemplates", "formatters", "globalShortcuts", "httpCodeGenerators", "httpCollections", "jdtlsBundles", "jsonValidation", "keymaps", "languageFeatures", "languageServers", "languages", "linters", "localizations", "mcpServers", "menus", "schedules", "scmCommitMessage", "searchProviders", "services", "sessionSources", "settingsPages", "snippets", "statusBar", "tasks", "terminalColorSchemes", "terminalLinks", "terminalProfiles", "testProviders", "themes", "treeViews", "uriHandlers", "walkthroughs"}
	if len(specs) != len(want) {
		t.Fatalf("got %d points, want %d", len(specs), len(want))
	}
	for index, spec := range specs {
		if spec.Key != want[index] {
			t.Fatalf("points[%d] = %q, want %q", index, spec.Key, want[index])
		}
	}
	keymaps, ok := ContributionPointSpecFor("keymaps")
	if !ok || !keymaps.Enabled() || !keymaps.AllowsKind("declarative") || keymaps.AllowsKind("process") {
		t.Fatalf("keymaps spec = %+v", keymaps)
	}
	if keymaps.MaxEntries != 8 || keymaps.InlineMaxBytes != 262144 || keymaps.Limit("maxRules") != 1000 || keymaps.FileRef == nil || keymaps.FileRef.Mode != "entry" {
		t.Fatalf("keymaps bounds changed: %+v", keymaps)
	}
	languages, ok := ContributionPointSpecFor("languages")
	if !ok || languages.FileRef == nil || len(languages.ReservedIDs) == 0 {
		t.Fatalf("languages spec = %+v", languages)
	}
	if fields := languages.FileRef.FieldNames(); len(fields) != 2 || fields[0] != "grammar" || fields[1] != "configuration" {
		t.Fatalf("languages fileRef fields = %v", fields)
	}
	if snippets, _ := ContributionPointSpecFor("snippets"); len(snippets.FileRef.FieldNames()) != 1 || snippets.FileRef.FieldNames()[0] != "path" {
		t.Fatalf("snippets fileRef fields default = %v", snippets.FileRef.FieldNames())
	}
	if editors, _ := ContributionPointSpecFor("customEditors"); len(editors.SensitiveFilenames) == 0 {
		t.Fatalf("customEditors sensitiveFilenames missing")
	}
	if ContributionManifestMaxBytes() != 256*1024 {
		t.Fatalf("manifestMaxBytes = %d", ContributionManifestMaxBytes())
	}
}

func TestContributionPointPhaseDSpecs(t *testing.T) {
	for _, key := range []string{"localizations", "terminalProfiles", "fileIcons"} {
		spec, ok := ContributionPointSpecFor(key)
		// Phase D points are enabled one by one; only the shared shape is pinned here.
		if !ok || spec.SinceHost != "0.1.0" || spec.Shape != ContributionShapeList {
			t.Fatalf("%s spec = %+v", key, spec)
		}
	}
	localizations, _ := ContributionPointSpecFor("localizations")
	if !localizations.Enabled() || localizations.Assets == nil || localizations.Assets.MaxCount != 16 || len(localizations.List("builtinLocales")) != 6 {
		t.Fatalf("localizations assets/lists = %+v", localizations)
	}
	terminals, _ := ContributionPointSpecFor("terminalProfiles")
	if len(terminals.RequiresCapabilities) != 1 || terminals.RequiresCapabilities[0] != "shell" || len(terminals.List("deniedEnvPrefixes")) == 0 {
		t.Fatalf("terminalProfiles = %+v", terminals)
	}
	icons, _ := ContributionPointSpecFor("fileIcons")
	if icons.Assets == nil || icons.Assets.Selectors[0] != "iconDefinitions.*.path" || icons.FileRef == nil || icons.Limit("svgMaxElements") != 2048 {
		t.Fatalf("fileIcons = %+v", icons)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("List on a missing list must panic")
		}
	}()
	icons.List("missing")
}

func TestContributionPointDatabaseDriversSpec(t *testing.T) {
	spec, ok := ContributionPointSpecFor("databaseDrivers")
	if !ok || !spec.Enabled() || spec.SinceHost != "0.1.0" || spec.Shape != ContributionShapeList || spec.Scope != "global" || spec.MinAPIVersion != ManifestAPIVersionV2 {
		t.Fatalf("databaseDrivers = %+v", spec)
	}
	if !spec.AllowsKind("process") || spec.AllowsKind("declarative") {
		t.Fatalf("databaseDrivers kinds = %v", spec.Kinds)
	}
	if len(spec.RequiresCapabilities) != 1 || spec.RequiresCapabilities[0] != string(CapabilityDatabaseDriver) {
		t.Fatalf("databaseDrivers requires = %v", spec.RequiresCapabilities)
	}
	if spec.Assets == nil || spec.Assets.Selectors[0] != "icon" || spec.Limit("pngMaxDimension") != 256 || spec.Limit("pageMaxRows") != 1000 {
		t.Fatalf("databaseDrivers assets/limits = %+v", spec)
	}
	for _, list := range []string{"connectionFields", "secretFields", "features", "sqlDialects", "categories", "replaceableTypes"} {
		if len(spec.List(list)) == 0 {
			t.Fatalf("databaseDrivers list %s empty", list)
		}
	}
}

func TestCapabilitiesV2OnlyIsIndependent(t *testing.T) {
	first := CapabilitiesV2Only()
	first[0] = "mutated"
	found := false
	for _, capability := range CapabilitiesV2Only() {
		if capability == "mutated" {
			t.Fatal("CapabilitiesV2Only shares its backing array")
		}
		found = found || capability == CapabilityDatabaseDriver
	}
	for _, capability := range CapabilitiesV1() {
		if capability == CapabilityDatabaseDriver {
			t.Fatal("database.driver must be manifest API v2 only")
		}
	}
	if !found {
		t.Fatal("database.driver missing from CapabilitiesV2Only")
	}
}

func TestContributionPointPhaseFSpecs(t *testing.T) {
	processOnly := map[string]bool{"formatters": true, "dashboardWidgets": true}
	for _, key := range []string{"jsonValidation", "commandSnippets", "httpCollections", "fileTemplates", "terminalLinks", "walkthroughs", "formatters", "dashboardWidgets"} {
		spec, ok := ContributionPointSpecFor(key)
		// Phase F points are enabled one by one; only the shared shape is pinned here.
		if !ok || spec.SinceHost != "0.1.0" || spec.Shape != ContributionShapeList || spec.MinAPIVersion != ManifestAPIVersionV2 || spec.Scope != "plugin" || len(spec.RequiresCapabilities) != 0 {
			t.Fatalf("%s spec = %+v", key, spec)
		}
		if spec.AllowsKind("declarative") == processOnly[key] || !spec.AllowsKind("process") {
			t.Fatalf("%s kinds = %v", key, spec.Kinds)
		}
	}
	walkthroughs, _ := ContributionPointSpecFor("walkthroughs")
	if walkthroughs.Assets == nil || len(walkthroughs.Assets.Selectors) != 3 || walkthroughs.Assets.Selectors[0] != "steps.*.markdown" {
		t.Fatalf("walkthroughs assets = %+v", walkthroughs.Assets)
	}
	templates, _ := ContributionPointSpecFor("fileTemplates")
	if templates.Assets == nil || templates.Assets.Selectors[0] != "files.*.source" || len(templates.SensitiveFilenames) == 0 || len(templates.List("variables")) != 9 {
		t.Fatalf("fileTemplates = %+v", templates)
	}
	http, _ := ContributionPointSpecFor("httpCollections")
	if http.FileRef == nil || http.FileRef.Mode != "field" || len(http.List("sensitiveKeyFragments")) == 0 {
		t.Fatalf("httpCollections = %+v", http)
	}
	snippets, _ := ContributionPointSpecFor("commandSnippets")
	if snippets.FileRef == nil || snippets.FileRef.Mode != "entry" || snippets.Limit("commandMaxLines") != 32 {
		t.Fatalf("commandSnippets = %+v", snippets)
	}
}

func TestContributionPointPhaseGSpecs(t *testing.T) {
	declarative := map[string]bool{"terminalColorSchemes": true, "httpCodeGenerators": true}
	shell := map[string]bool{"tasks": true, "languageServers": true}
	for _, key := range []string{"linters", "searchProviders", "aiContextProviders", "terminalColorSchemes", "httpCodeGenerators", "uriHandlers", "tasks", "languageServers"} {
		spec, ok := ContributionPointSpecFor(key)
		// Phase G points are enabled one by one; only the shared shape is pinned here.
		if !ok || spec.SinceHost != "0.1.0" || spec.Shape != ContributionShapeList || spec.MinAPIVersion != ManifestAPIVersionV2 {
			t.Fatalf("%s spec = %+v", key, spec)
		}
		if spec.AllowsKind("declarative") != declarative[key] || !spec.AllowsKind("process") {
			t.Fatalf("%s kinds = %v", key, spec.Kinds)
		}
		if requires := len(spec.RequiresCapabilities) == 1 && spec.RequiresCapabilities[0] == "shell"; requires != shell[key] {
			t.Fatalf("%s requiresCapabilities = %v", key, spec.RequiresCapabilities)
		}
	}
	// Process-launching points copy the terminalProfiles env denylist; a
	// change to one list must be made to all of them.
	terminals, _ := ContributionPointSpecFor("terminalProfiles")
	for _, key := range []string{"tasks", "languageServers"} {
		spec, _ := ContributionPointSpecFor(key)
		for _, list := range []string{"deniedEnvNames", "deniedEnvPrefixes"} {
			if !slices.Equal(spec.List(list), terminals.List(list)) {
				t.Fatalf("%s %s differs from terminalProfiles", key, list)
			}
		}
		if spec.Limit("pluginRootFilesMax") != 4 || spec.Limit("pluginExecutableHashMaxBytes") != terminals.Limit("pluginExecutableHashMaxBytes") {
			t.Fatalf("%s process launch limits = %v", key, spec.Limits)
		}
	}
	http, _ := ContributionPointSpecFor("httpCollections")
	codegen, _ := ContributionPointSpecFor("httpCodeGenerators")
	for _, list := range []string{"sensitiveHeaderNames", "sensitiveKeyFragments"} {
		if !slices.Equal(codegen.List(list), http.List(list)) {
			t.Fatalf("httpCodeGenerators %s differs from httpCollections", list)
		}
	}
	if codegen.Assets == nil || codegen.Assets.Selectors[0] != "template" || codegen.Assets.Ext[0] != ".tmpl" {
		t.Fatalf("httpCodeGenerators assets = %+v", codegen.Assets)
	}
	editor, _ := ContributionPointSpecFor("editorThemes")
	themes, _ := ContributionPointSpecFor("themes")
	if editor.Scope != "global" || editor.SinceHost != "0.1.0" || !editor.AllowsKind("declarative") || !slices.Equal(editor.MonacoColors, themes.MonacoColors) {
		t.Fatalf("editorThemes = %+v", editor)
	}
	schemes, _ := ContributionPointSpecFor("terminalColorSchemes")
	if schemes.Scope != "global" || schemes.FileRef == nil || len(schemes.List("requiredColors")) != 20 {
		t.Fatalf("terminalColorSchemes = %+v", schemes)
	}
}

func TestValidateContributionSpecGenericFields(t *testing.T) {
	base := func() ContributionPointSpec {
		return ContributionPointSpec{Key: "probe", Status: ContributionStatusPlanned, Kinds: []string{"process"}, MinAPIVersion: 2, SinceHost: "0.2.0", Shape: ContributionShapeList, MaxEntries: 1, Scope: "plugin"}
	}
	assets := func() *ContributionAssets {
		return &ContributionAssets{Dirs: []string{"icons"}, Ext: []string{".svg"}, MaxBytes: 1024, MaxPathLength: 64, MaxCount: 4, MaxTotalBytes: 4096, Selectors: []string{"icons.*.path"}}
	}
	valid := base()
	valid.RequiresCapabilities = []string{"shell", "ui"}
	valid.Assets = assets()
	valid.Lists = map[string][]string{"names": {"a", "b"}}
	valid.ValueSinceHost = map[string]map[string]string{"names": {"b": "0.10.0"}}
	if err := validateContributionSpec(valid); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}
	cases := map[string]func(*ContributionPointSpec){
		"unknown capability":   func(spec *ContributionPointSpec) { spec.RequiresCapabilities = []string{"root"} },
		"duplicate capability": func(spec *ContributionPointSpec) { spec.RequiresCapabilities = []string{"shell", "shell"} },
		"empty list":           func(spec *ContributionPointSpec) { spec.Lists = map[string][]string{"names": {}} },
		"duplicate list value": func(spec *ContributionPointSpec) { spec.Lists = map[string][]string{"names": {"a", "a"}} },
		"upper-case ext":       func(spec *ContributionPointSpec) { spec.Assets = assets(); spec.Assets.Ext = []string{".SVG"} },
		"no selectors":         func(spec *ContributionPointSpec) { spec.Assets = assets(); spec.Assets.Selectors = nil },
		"bad selector": func(spec *ContributionPointSpec) {
			spec.Assets = assets()
			spec.Assets.Selectors = []string{"icons..path"}
		},
		"zero count": func(spec *ContributionPointSpec) { spec.Assets = assets(); spec.Assets.MaxCount = 0 },
		"oversized maxBytes": func(spec *ContributionPointSpec) {
			spec.Assets = assets()
			spec.Assets.MaxBytes = MaxAssetBytesLimit + 1
		},
		"nested dir": func(spec *ContributionPointSpec) { spec.Assets = assets(); spec.Assets.Dirs = []string{"a/b"} },
		"valueSinceHost unknown list": func(spec *ContributionPointSpec) {
			spec.ValueSinceHost = map[string]map[string]string{"other": {"a": "0.3.0"}}
		},
		"valueSinceHost unknown value": func(spec *ContributionPointSpec) {
			spec.ValueSinceHost = map[string]map[string]string{"names": {"c": "0.3.0"}}
		},
		"valueSinceHost bad version": func(spec *ContributionPointSpec) {
			spec.ValueSinceHost = map[string]map[string]string{"names": {"a": "v0.3"}}
		},
		"valueSinceHost below sinceHost": func(spec *ContributionPointSpec) {
			spec.ValueSinceHost = map[string]map[string]string{"names": {"a": "0.1.0"}}
		},
		"valueSinceHost empty": func(spec *ContributionPointSpec) {
			spec.ValueSinceHost = map[string]map[string]string{"names": {}}
		},
	}
	for name, mutate := range cases {
		spec := base()
		spec.Lists = map[string][]string{"names": {"a", "b"}}
		mutate(&spec)
		if err := validateContributionSpec(spec); err == nil {
			t.Fatalf("%s: expected an error", name)
		}
	}
}

func TestContributionPointBatchTwoSpecs(t *testing.T) {
	for _, key := range []string{"treeViews", "languageFeatures", "testProviders"} {
		spec, ok := ContributionPointSpecFor(key)
		if !ok || !spec.Enabled() || spec.MinAPIVersion != ManifestAPIVersionV2 || !spec.AllowsKind("process") || spec.AllowsKind("declarative") {
			t.Fatalf("%s = %+v", key, spec)
		}
	}
	tests, _ := ContributionPointSpecFor("testProviders")
	if len(tests.RequiresCapabilities) != 1 || tests.RequiresCapabilities[0] != string(CapabilityWorkspaceRead) || tests.Limit("runTimeoutMs") > 30000 {
		t.Fatalf("testProviders = %+v", tests)
	}
	features, _ := ContributionPointSpecFor("languageFeatures")
	if !slices.Equal(features.List("features")[:2], []string{"hover", "completion"}) {
		t.Fatalf("languageFeatures features = %v", features.List("features"))
	}
	for name, want := range map[string]int64{"codeActionItemsMax": 20, "codeActionTitleMaxRunes": 128, "codeActionEditsMax": 50, "codeActionEditTextMaxBytes": 65536} {
		if got := features.Limit(name); got != want {
			t.Fatalf("languageFeatures %s = %d, want %d", name, got, want)
		}
	}
}

func TestDebuggersPointContract(t *testing.T) {
	spec, ok := ContributionPointSpecFor("debuggers")
	if !ok {
		t.Fatalf("debuggers point not found")
	}
	if spec.Status != ContributionStatusEnabled || spec.MaxEntries != 4 || spec.SinceHost != "0.3.0" || spec.Scope != "plugin" || !spec.AllowsKind("process") || spec.AllowsKind("declarative") {
		t.Fatalf("debuggers spec = %+v", spec)
	}
	if !slices.Equal(spec.RequiresCapabilities, []string{"shell"}) {
		t.Fatalf("debuggers requiresCapabilities = %v", spec.RequiresCapabilities)
	}
	if spec.Limit("resolveTimeoutMs") != 60000 || spec.Limit("sessionsPerPlugin") != 4 || spec.Limit("tcpConnectTimeoutMs") != 10000 {
		t.Fatalf("debuggers limits = %v", spec.Limits)
	}
	languageServers, _ := ContributionPointSpecFor("languageServers")
	if !slices.Equal(spec.List("deniedEnvNames"), languageServers.List("deniedEnvNames")) {
		t.Fatalf("debuggers deniedEnvNames differ from languageServers")
	}
	if !slices.Equal(spec.List("deniedEnvPrefixes"), languageServers.List("deniedEnvPrefixes")) {
		t.Fatalf("debuggers deniedEnvPrefixes differ from languageServers")
	}
	if !slices.Equal(spec.List("platforms"), languageServers.List("platforms")) || !slices.Equal(spec.List("variables"), append(languageServers.List("variables"), "${port}")) {
		t.Fatalf("debuggers platforms/variables = %v", spec.Lists)
	}
	if !slices.Equal(spec.List("adapterKinds"), []string{"stdio", "tcp", "jdtls"}) || !slices.Equal(spec.List("remoteForwards"), []string{"local", "remote"}) {
		t.Fatalf("debuggers lists = %v", spec.Lists)
	}
}

func TestJdtlsBundlesPointContract(t *testing.T) {
	spec, ok := ContributionPointSpecFor("jdtlsBundles")
	if !ok {
		t.Fatalf("jdtlsBundles point not found")
	}
	if spec.Status != ContributionStatusEnabled || spec.MaxEntries != 4 || spec.SinceHost != "0.3.0" || spec.Scope != "plugin" ||
		spec.MinAPIVersion != 2 || !spec.AllowsKind("process") || spec.AllowsKind("declarative") {
		t.Fatalf("jdtlsBundles spec = %+v", spec)
	}
	if !slices.Equal(spec.RequiresCapabilities, []string{"shell"}) {
		t.Fatalf("jdtlsBundles requiresCapabilities = %v", spec.RequiresCapabilities)
	}
	for name, want := range map[string]int64{"commandsMax": 32, "commandMaxLength": 128, "jarMaxBytes": 67108864, "trustEntriesMax": 64} {
		if got := spec.Limit(name); got != want {
			t.Fatalf("jdtlsBundles %s = %d, want %d", name, got, want)
		}
	}
}

func TestContractValueSinceHost(t *testing.T) {
	services, ok := ContributionPointSpecFor("services")
	if !ok || services.SinceHost != "0.2.0" {
		t.Fatalf("services spec = %+v", services)
	}
	newSurfaces := []string{"sftpFile", "dbResult", "httpResponse", "terminalSelection"}
	if surfaces := services.List("surfaces"); !slices.Equal(surfaces, append([]string{"fileMenu", "selection", "palette"}, newSurfaces...)) {
		t.Fatalf("surfaces = %v", surfaces)
	}
	if len(services.ValueSinceHost) != 1 || len(services.ValueSinceHost["surfaces"]) != len(newSurfaces) {
		t.Fatalf("valueSinceHost = %v", services.ValueSinceHost)
	}
	for _, surface := range newSurfaces {
		if got := services.ValueSince("surfaces", surface); got != "0.3.0" {
			t.Fatalf("ValueSince(surfaces, %s) = %q", surface, got)
		}
	}
	for _, probe := range [][2]string{{"surfaces", "palette"}, {"surfaces", "fileMenu"}, {"inputKinds", "text"}, {"unknown", "x"}} {
		if got := services.ValueSince(probe[0], probe[1]); got != "0.2.0" {
			t.Fatalf("ValueSince(%s, %s) = %q, want the point sinceHost", probe[0], probe[1], got)
		}
	}
	for name, want := range map[string]int64{
		"dbResultMaxRows": 100000, "dbResultMaxBytes": 16777216, "httpResponseMaxBytes": 10485760,
		"httpResponseHeadersMax": 32, "httpHeaderValueMaxBytes": 512, "surfaceContextMaxBytes": 4096,
		"remoteContextPathMaxBytes": 2048, "remoteInputStallMs": 60000,
		"textMaxBytes": 196608, "fileMaxBytes": 67108864, "envelopeReserveBytes": 8192,
	} {
		if got := services.Limit(name); got != want {
			t.Fatalf("services limit %s = %d, want %d", name, got, want)
		}
	}
	wantTextTypes := []string{"text/*", "application/json", "application/*+json", "application/xml", "application/*+xml", "application/javascript", "application/ecmascript", "application/x-www-form-urlencoded", "application/graphql", "application/x-ndjson", "application/yaml", "application/x-yaml", "application/toml", "application/sql", "application/csv"}
	if got := services.List("httpResponseTextTypes"); !slices.Equal(got, wantTextTypes) {
		t.Fatalf("httpResponseTextTypes = %v", got)
	}

	const header = `{"version":1,"manifestMaxBytes":1024,"points":{"probe":{"status":"enabled","kinds":["process"],"minApiVersion":2,"sinceHost":"0.2.0","scope":"plugin",`
	listPoint := func(extra string) string {
		return header + `"shape":"list","maxEntries":4,"lists":{"colors":["red","blue"],"sizes":["s"]}` + extra + `}}}`
	}
	parsed, err := parseContributionContract([]byte(listPoint(`,"valueSinceHost":{"colors":{"blue":"0.10.0"}}`)))
	if err != nil {
		t.Fatalf("valid contract rejected: %v", err)
	}
	if probe := parsed.Points["probe"]; probe.ValueSince("colors", "blue") != "0.10.0" || probe.ValueSince("colors", "red") != "0.2.0" || probe.ValueSince("sizes", "s") != "0.2.0" {
		t.Fatalf("parsed probe = %+v", probe)
	}
	cases := map[string]string{
		"unknown list":         listPoint(`,"valueSinceHost":{"shapes":{"round":"0.3.0"}}`),
		"value not in list":    listPoint(`,"valueSinceHost":{"colors":{"green":"0.3.0"}}`),
		"lower than sinceHost": listPoint(`,"valueSinceHost":{"colors":{"blue":"0.1.9"}}`),
		"bad version":          listPoint(`,"valueSinceHost":{"colors":{"blue":"0.3"}}`),
		"empty values":         listPoint(`,"valueSinceHost":{"colors":{}}`),
		"map point":            header + `"shape":"map","maxEntries":4,"maxPerKey":2,"keys":["a"],"lists":{"colors":["red"]},"valueSinceHost":{"colors":{"red":"0.3.0"}}}}}`,
		"unknown field":        listPoint(`,"valueSince":{"colors":{"blue":"0.3.0"}}`),
	}
	for name, raw := range cases {
		if _, err := parseContributionContract([]byte(raw)); err == nil {
			t.Fatalf("%s: contract accepted", name)
		}
	}
}

func TestContractLanguageFeatureKindsSince(t *testing.T) {
	spec, ok := ContributionPointSpecFor("languageFeatures")
	if !ok || spec.SinceHost != "0.1.0" {
		t.Fatalf("languageFeatures spec = %+v", spec)
	}
	added := []string{"definition", "references", "codeLens", "inlayHints", "documentSymbol", "codeAction"}
	if got := spec.List("features"); !slices.Equal(got, append([]string{"hover", "completion"}, added...)) {
		t.Fatalf("features = %v", got)
	}
	if len(spec.ValueSinceHost) != 1 || len(spec.ValueSinceHost["features"]) != len(added) {
		t.Fatalf("valueSinceHost = %v", spec.ValueSinceHost)
	}
	for _, feature := range added {
		if got := spec.ValueSince("features", feature); got != "0.3.0" {
			t.Fatalf("ValueSince(features, %s) = %q", feature, got)
		}
	}
	for _, feature := range []string{"hover", "completion"} {
		if got := spec.ValueSince("features", feature); got != "0.1.0" {
			t.Fatalf("ValueSince(features, %s) = %q", feature, got)
		}
	}
	if got := spec.Lists["symbolKinds"]; len(got) != 26 || got[0] != "file" || got[25] != "typeParameter" {
		t.Fatalf("symbolKinds = %v", got)
	}
	if got := spec.List("inlayHintKinds"); !slices.Equal(got, []string{"type", "parameter"}) {
		t.Fatalf("inlayHintKinds = %v", got)
	}
	for name, want := range map[string]int64{
		"definitionLocationsMax": 20, "referencesLocationsMax": 500, "referencesTimeoutMs": 5000,
		"codeLensesMax": 100, "codeLensTitleMaxRunes": 64, "codeLensArgsMaxBytes": 1024, "codeLensArgsMaxProperties": 8,
		"inlayHintsMax": 300, "inlayHintLabelMaxRunes": 64, "documentSymbolsMax": 500, "documentSymbolDepthMax": 8,
		"symbolNameMaxRunes": 128, "symbolDetailMaxRunes": 128, "locationPathMaxBytes": 1024, "columnMax": 100000,
		"passiveConcurrentPerPlugin": 2, "passiveDebounceMs": 300, "passiveFailuresMax": 3, "passiveSuspendMs": 60000,
		"timeoutMs": 2000, "concurrentPerPlugin": 2, "textMaxBytes": 65536,
	} {
		if got := spec.Limit(name); got != want {
			t.Fatalf("languageFeatures limit %s = %d, want %d", name, got, want)
		}
	}
}

func TestContractShellExtensions(t *testing.T) {
	pages, ok := ContributionPointSpecFor("settingsPages")
	if !ok || !pages.Enabled() || pages.SinceHost != "0.3.0" || pages.MaxEntries != 1 || pages.Shape != ContributionShapeList ||
		pages.MinAPIVersion != 2 || !slices.Equal(pages.Kinds, []string{"process"}) || pages.Scope != "plugin" || pages.IDPrefix != "none" {
		t.Fatalf("settingsPages spec = %+v", pages)
	}
	if pages.Limit("titleMaxRunes") != 48 || pages.Limit("localesMax") != 16 || len(pages.Limits) != 2 {
		t.Fatalf("settingsPages limits = %v", pages.Limits)
	}
	menus, ok := ContributionPointSpecFor("menus")
	if !ok || menus.SinceHost != "0.1.0" || menus.KeySince("tray") != "0.3.0" {
		t.Fatalf("menus spec = %+v", menus)
	}
	if keys := menus.Keys; keys[len(keys)-1] != "commandPalette" || keys[len(keys)-2] != "tray" {
		t.Fatalf("tray must precede commandPalette: %v", keys)
	}
	for name, want := range map[string]int64{"trayMaxPerPlugin": 4, "trayPluginsMax": 8, "trayLabelMaxRunes": 48} {
		if got := menus.Limit(name); got != want {
			t.Fatalf("menus limit %s = %d, want %d", name, got, want)
		}
	}
}

func TestContractSessionSources(t *testing.T) {
	sources, ok := ContributionPointSpecFor("sessionSources")
	if !ok || !sources.Enabled() || sources.SinceHost != "0.3.0" || sources.MaxEntries != 2 || sources.Shape != ContributionShapeList ||
		sources.MinAPIVersion != 2 || !slices.Equal(sources.Kinds, []string{"process"}) || sources.Scope != "plugin" || sources.IDPrefix != "none" {
		t.Fatalf("sessionSources spec = %+v", sources)
	}
	if len(sources.RequiresCapabilities) != 0 {
		t.Fatalf("sessionSources requires no capability: %v", sources.RequiresCapabilities)
	}
	if sources.Limit("messagesMax") != 200 || sources.Limit("sessionsMax") != 200 || sources.Limit("payloadMaxBytes") != 4096 || sources.Limit("timeoutMs") != 10000 || len(sources.Limits) != 17 {
		t.Fatalf("sessionSources limits = %v", sources.Limits)
	}
	if roles := sources.List("roles"); !slices.Equal(roles, []string{"user", "assistant", "system", "tool"}) {
		t.Fatalf("sessionSources roles = %v", roles)
	}
	if icons := sources.List("icons"); !slices.Equal(icons, []string{"chat", "cpu", "document", "monitor", "magic"}) {
		t.Fatalf("sessionSources icons = %v", icons)
	}
}

func TestContractScmCommitMessage(t *testing.T) {
	provider, ok := ContributionPointSpecFor("scmCommitMessage")
	if !ok || !provider.Enabled() || provider.SinceHost != "0.3.0" || provider.MaxEntries != 1 || provider.Shape != ContributionShapeList ||
		provider.MinAPIVersion != 2 || !slices.Equal(provider.Kinds, []string{"process"}) || provider.Scope != "plugin" || provider.IDPrefix != "none" {
		t.Fatalf("scmCommitMessage spec = %+v", provider)
	}
	if !slices.Equal(provider.RequiresCapabilities, []string{"workspace.read"}) {
		t.Fatalf("scmCommitMessage requires = %v", provider.RequiresCapabilities)
	}
	if provider.Limit("filesMax") != 500 || provider.Limit("diffMaxBytes") != 65536 || provider.Limit("payloadMaxBytes") != 196608 ||
		provider.Limit("resultMaxBytes") != 16384 || provider.Limit("messageMaxRunes") != 4096 || provider.Limit("timeoutMs") != 30000 || len(provider.Limits) != 10 {
		t.Fatalf("scmCommitMessage limits = %v", provider.Limits)
	}
	if statuses := provider.List("statuses"); !slices.Equal(statuses, []string{"added", "modified", "deleted", "renamed", "copied", "typeChanged"}) {
		t.Fatalf("scmCommitMessage statuses = %v", statuses)
	}
}

func TestContractFileDecorations(t *testing.T) {
	provider, ok := ContributionPointSpecFor("fileDecorations")
	if !ok || !provider.Enabled() || provider.SinceHost != "0.3.0" || provider.MaxEntries != 1 || provider.Shape != ContributionShapeList ||
		provider.MinAPIVersion != 2 || !slices.Equal(provider.Kinds, []string{"process"}) || provider.Scope != "plugin" || provider.IDPrefix != "none" {
		t.Fatalf("fileDecorations spec = %+v", provider)
	}
	if !slices.Equal(provider.RequiresCapabilities, []string{"workspace.read"}) {
		t.Fatalf("fileDecorations requires = %v", provider.RequiresCapabilities)
	}
	for name, want := range map[string]int64{
		"titleMaxRunes": 32, "localesMax": 16, "namesMax": 500, "nameMaxBytes": 255, "directoryMaxBytes": 1024, "payloadMaxBytes": 163840,
		"resultMaxBytes": 131072, "badgeMaxRunes": 2, "tooltipMaxRunes": 128, "timeoutMs": 3000, "concurrentPerPlugin": 1, "debounceMs": 150,
		"refreshDirectoriesMax": 32, "cachedDirectoriesMax": 64, "failuresMax": 3, "suspendMs": 60000, "refreshHintMinIntervalMs": 2000, "providersRenderedMax": 1,
	} {
		if got := provider.Limit(name); got != want {
			t.Fatalf("fileDecorations limit %s = %d, want %d", name, got, want)
		}
	}
	if len(provider.Limits) != 18 {
		t.Fatalf("fileDecorations limits = %v", provider.Limits)
	}
	if colors := provider.List("colors"); !slices.Equal(colors, []string{"accent", "success", "warning", "danger", "info", "muted"}) {
		t.Fatalf("fileDecorations colors = %v", colors)
	}
}
