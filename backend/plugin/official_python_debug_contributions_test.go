package plugin

import (
	"encoding/json"
	"strings"
	"testing"
)

// The official python-debug plugin must pass strict host inspection of its
// debuggers entry, and the result shapes its actions return must pass the
// host's strict decoders.
func TestOfficialPythonDebugContributions(t *testing.T) {
	const id = "python-debug"
	manifest := readContributionManifestForTest(t, officialPackageRoot(id))
	phaseGRequireNoIssues(t, id, manifest)

	debuggers := phaseGEntries[DebuggerContribution](manifest, debuggerPointKey)
	if len(debuggers) != 1 {
		t.Fatalf("debuggers = %+v", debuggers)
	}
	debugger := debuggers[0]
	if debugger.Type != "python" || strings.Join(debugger.Languages, ",") != "python" || len(debugger.Platforms) != 3 {
		t.Fatalf("debugger = %+v", debugger)
	}
	if debugger.Adapter.Kind != "stdio" || debugger.Adapter.Command != "${resolved.python}" || strings.Join(debugger.Adapter.Args, " ") != "-m debugpy.adapter" {
		t.Fatalf("adapter = %+v", debugger.Adapter)
	}
	if debugger.Remote == nil || debugger.Remote.Forward != "local" || len(debugger.ConfigurationSnippets) != 3 || len(debugger.ConfigurationSchema) == 0 {
		t.Fatalf("debugger = %+v", debugger)
	}
	// Method breakpoints (debugpy supportsFunctionBreakpoints): the input's texts pass the host's checks.
	if hints := debugger.FunctionBreakpoints; hints == nil || hints.Placeholder == "" || hints.Description == "" {
		t.Fatalf("functionBreakpoints = %+v", debugger.FunctionBreakpoints)
	}
	for _, action := range []string{debugger.ResolveAction, debugger.EntryPointsAction, debugger.CheckAction} {
		phaseGRequireHostOnlyReadAction(t, id, manifest, action)
	}

	resolved, err := parseDebuggerResolveResult(json.RawMessage(`{"variables":{"python":"/p/.venv/bin/python"},"request":"launch","arguments":{"console":"integratedTerminal","cwd":"/p","justMyCode":true,"program":"/p/main.py","python":["/p/.venv/bin/python"],"type":"python"}}`))
	if err != nil || resolved.Variables["python"] != "/p/.venv/bin/python" || *resolved.Request != "launch" {
		t.Fatalf("resolve result = %+v, %v", resolved, err)
	}
	points, err := parseDebuggerEntryPoints(json.RawMessage(`[{"line":12,"label":"app.py (__main__)","configuration":{"program":"pkg/app.py","request":"launch"}}]`))
	if err != nil || len(points) != 1 || points[0].Line != 12 {
		t.Fatalf("entry points = %+v, %v", points, err)
	}
	check, err := parseDebuggerCheckResult(json.RawMessage(`{"ready":false,"missing":[{"name":"debugpy","hint":"解释器 /usr/bin/python3 未安装 debugpy / debugpy is not installed for this interpreter","installCommand":"/usr/bin/python3 -m pip install debugpy"}]}`))
	if err != nil || check.Ready || len(check.Missing) != 1 {
		t.Fatalf("check = %+v, %v", check, err)
	}
	if check, err := parseDebuggerCheckResult(json.RawMessage(`{"ready":true,"missing":[]}`)); err != nil || !check.Ready {
		t.Fatalf("ready check = %+v, %v", check, err)
	}
}
