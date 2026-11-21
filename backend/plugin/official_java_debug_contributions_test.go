package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The official java-debug plugin must pass strict host inspection of its
// jdtlsBundles, debuggers and testProviders entries, and the result shapes
// its actions return must pass the host's strict decoders. The jars are
// downloaded at build time (scripts/fetch-assets.mjs, not in git), so the
// manifest is inspected in a copy of the package with stand-in jars.
func TestOfficialJavaDebugContributions(t *testing.T) {
	const id = "java-debug"
	const jar = "lib/com.microsoft.java.debug.plugin-0.53.1.jar"
	data, err := os.ReadFile(filepath.Join(officialPackageRoot(id), "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "plugin.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	var declared struct {
		Contributes struct {
			JdtlsBundles []JdtlsBundleContribution `json:"jdtlsBundles"`
		} `json:"contributes"`
	}
	if err := json.Unmarshal(data, &declared); err != nil {
		t.Fatal(err)
	}
	for _, bundle := range declared.Contributes.JdtlsBundles {
		for _, path := range append([]string{bundle.Path}, bundle.Dependencies...) {
			rel := filepath.FromSlash(strings.TrimPrefix(path, "${pluginRoot}/"))
			if err := os.WriteFile(filepath.Join(root, rel), []byte("PK\x05\x06"+strings.Repeat("\x00", 18)), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	manifest := readContributionManifestForTest(t, root)
	phaseGRequireNoIssues(t, id, manifest)

	// The settings debug.resolve and debug.applySettings read (host.configuration.get);
	// hotCodeReplace.mode is also the debugger's hotCodeReplace.modeSetting (read by the frontend).
	schema := configurationSchemaOf(&manifest)
	if schema == nil || len(schema.Properties) != 7 {
		t.Fatalf("configuration = %+v, want the five stepFilters settings, exceptionBreakpoints.skipFilteredClasses and hotCodeReplace.mode", schema)
	}
	if mode := schema.Properties["hotCodeReplace.mode"]; mode == nil || mode.Type != "string" || mode.Default != "auto" ||
		len(mode.Enum) != 3 || mode.Enum[0] != "auto" || mode.Enum[1] != "manual" || mode.Enum[2] != "never" {
		t.Fatalf("configuration hotCodeReplace.mode = %+v, want a string auto/manual/never, default auto", mode)
	}
	for _, key := range []string{"stepFilters.enabled", "stepFilters.skipClasses", "stepFilters.skipSynthetics", "stepFilters.skipStaticInitializers", "stepFilters.skipConstructors", "exceptionBreakpoints.skipFilteredClasses", "hotCodeReplace.mode"} {
		if property := schema.Properties[key]; property == nil || property.Scope != configurationScopeGlobal || property.Secret {
			t.Fatalf("configuration %s = %+v, want a global setting", key, property)
		}
	}

	bundles := phaseGEntries[JdtlsBundleContribution](manifest, jdtlsBundlePointKey)
	if len(bundles) != 2 || bundles[0].Path != "${pluginRoot}/"+jar || len(bundles[0].Dependencies) != 0 {
		t.Fatalf("jdtls bundles = %+v", bundles)
	}
	// The second bundle is the test plugin with its dependency jars; it
	// declares only the four commands the plugin's test actions call.
	if tests := bundles[1]; !strings.HasPrefix(tests.Path, "${pluginRoot}/lib/") || len(tests.Dependencies) != 28 || len(tests.Commands) != 4 ||
		slices.ContainsFunc(tests.Commands, func(command string) bool { return !strings.HasPrefix(command, "vscode.java.test.") }) {
		t.Fatalf("test jdtls bundle = %+v", tests)
	}
	commands := bundles[0].Commands
	for _, command := range []string{"vscode.java.startDebugSession", "vscode.java.resolveMainClass", "vscode.java.resolveMainMethod", "vscode.java.resolveClasspath", "vscode.java.resolveJavaExecutable", "vscode.java.updateDebugSettings"} {
		if !slices.Contains(commands, command) {
			t.Fatalf("jdtls bundle commands %v miss %s", commands, command)
		}
	}
	if len(commands) != 6 {
		t.Fatalf("jdtls bundle declares commands the plugin does not use: %v", commands)
	}

	debuggers := phaseGEntries[DebuggerContribution](manifest, debuggerPointKey)
	if len(debuggers) != 1 {
		t.Fatalf("debuggers = %+v", debuggers)
	}
	debugger := debuggers[0]
	if debugger.Type != "java" || strings.Join(debugger.Languages, ",") != "java" || len(debugger.Platforms) != 3 {
		t.Fatalf("debugger = %+v", debugger)
	}
	if debugger.Adapter.Kind != debuggerKindJdtls || debugger.Adapter.Command != "vscode.java.startDebugSession" || len(debugger.Adapter.Args) != 0 || len(debugger.Adapter.Env) != 0 {
		t.Fatalf("adapter = %+v", debugger.Adapter)
	}
	if debugger.Remote == nil || debugger.Remote.Forward != "local" || !strings.Contains(debugger.Remote.ArgsTemplate, "-agentlib:jdwp=") ||
		len(debugger.ConfigurationSnippets) != 4 || len(debugger.ConfigurationSchema) == 0 {
		t.Fatalf("debugger = %+v", debugger)
	}
	if h := debugger.HotCodeReplace; h == nil || *h != (DebuggerHotCodeReplace{Event: "hotcodereplace", Request: "redefineClasses", ModeSetting: "hotCodeReplace.mode"}) {
		t.Fatalf("hotCodeReplace = %+v, want java-debug's hotcodereplace / redefineClasses", debugger.HotCodeReplace)
	}
	if debugger.SettingsAction != "debug.applySettings" || debugger.ExceptionTypes == nil || debugger.ExceptionTypes.Placeholder == "" || debugger.ExceptionTypes.PlaceholderLocalized["zh"] == "" {
		t.Fatalf("settings = %q, exceptionTypes = %+v", debugger.SettingsAction, debugger.ExceptionTypes)
	}
	for _, action := range []string{debugger.ResolveAction, debugger.EntryPointsAction, debugger.CheckAction, debugger.SettingsAction} {
		phaseGRequireHostOnlyReadAction(t, id, manifest, action)
	}

	providers := phaseGEntries[TestProviderContribution](manifest, testProviderPointKey)
	if len(providers) != 1 || providers[0].DebugRun == nil || providers[0].DebugRun.DebuggerType != "java" || providers[0].RunRisk != "read" {
		t.Fatalf("test providers = %+v", providers)
	}
	for _, action := range []string{providers[0].DiscoverAction, providers[0].DebugRun.ResolveRunAction} {
		phaseGRequireHostOnlyReadAction(t, id, manifest, action)
	}

	resolved, err := parseDebuggerResolveResult(json.RawMessage(`{"variables":{},"request":"launch","arguments":{"type":"java","mainClass":"com.example.Main","projectName":"app","modulePaths":[],"classPaths":["/p/target/classes"],"javaExec":"/jdk/bin/java","cwd":"/p","console":"integratedTerminal"}}`))
	if err != nil || *resolved.Request != "launch" || len(resolved.Variables) != 0 {
		t.Fatalf("resolve result = %+v, %v", resolved, err)
	}
	if attach, err := parseDebuggerResolveResult(json.RawMessage(`{"variables":{},"request":"attach","arguments":{"type":"java","hostName":"localhost","port":5005}}`)); err != nil || *attach.Request != "attach" {
		t.Fatalf("attach result = %+v, %v", attach, err)
	}
	points, err := parseDebuggerEntryPoints(json.RawMessage(`[{"line":3,"label":"Main.main (com.example.Main)","configuration":{"request":"launch","mainClass":"com.example.Main","projectName":"app"}}]`))
	if err != nil || len(points) != 1 || points[0].Line != 3 {
		t.Fatalf("entry points = %+v, %v", points, err)
	}
	if points, err := parseDebuggerEntryPoints(json.RawMessage(`[]`)); err != nil || len(points) != 0 {
		t.Fatalf("empty entry points = %+v, %v", points, err)
	}
	check, err := parseDebuggerCheckResult(json.RawMessage(`{"ready":false,"missing":[{"name":"jdtls","hint":"未找到 Java 语言服务 jdtls / jdtls was not found on PATH","installCommand":"brew install jdtls"},{"name":"java","hint":"未找到 JDK 8+ / No JDK 8 or later found","installCommand":"brew install openjdk@21"}]}`))
	if err != nil || check.Ready || len(check.Missing) != 2 {
		t.Fatalf("check = %+v, %v", check, err)
	}
}
