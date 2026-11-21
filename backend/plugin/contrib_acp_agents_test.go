package plugin

import (
	"encoding/json"
	"strings"
	"testing"

	"mincode/app/plugins/api"
)

func decodeAcpAgentForTest(ctx *pointContext, raw string) (AcpAgentContribution, error) {
	value, _, err := decodeAcpAgentEntry(ctx, 0, "", json.RawMessage(raw))
	if err != nil {
		return AcpAgentContribution{}, err
	}
	return value.(AcpAgentContribution), nil
}

func acpAgentTestContext(t *testing.T) *pointContext {
	t.Helper()
	root := t.TempDir()
	writeTerminalProfileExecutable(t, root, "bin/agent")
	return &pointContext{
		root:     root,
		manifest: &Manifest{ID: "acp.fixture", APIVersion: 2, Capabilities: []api.Capability{api.CapabilityShell}},
		spec:     acpAgentSpec,
	}
}

func TestDecodeAcpAgentEntry(t *testing.T) {
	ctx := acpAgentTestContext(t)
	agent, err := decodeAcpAgentForTest(ctx, `{"id":"demo-agent","title":"Demo","titleLocalized":{"zh":"演示"},"description":"Demo agent","descriptionLocalized":{"zh":"演示智能体"},"command":"${pluginRoot}/bin/agent","args":["--acp","--root=${projectRoot}"],"env":{"DEMO_LOG":"${pluginRoot}/log"},"platforms":["linux","darwin"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if agent.ID != "demo-agent" || agent.Title != "Demo" || agent.TitleLocalized["zh"] == "" || agent.Description != "Demo agent" || agent.DescriptionLocalized["zh"] == "" {
		t.Fatalf("agent = %+v", agent)
	}
	if agent.Command != "${pluginRoot}/bin/agent" || strings.Join(agent.Args, " ") != "--acp --root=${projectRoot}" || agent.Env["DEMO_LOG"] != "${pluginRoot}/log" || strings.Join(agent.Platforms, ",") != "darwin,linux" {
		t.Fatalf("agent launch = %+v", agent)
	}
	bare, err := decodeAcpAgentForTest(ctx, `{"id":"`+strings.Repeat("a", 64)+`","title":"Bare","command":"gemini","args":["--experimental-acp"]}`)
	if err != nil || len(bare.Platforms) != 3 || bare.Description != "" || len(bare.Env) != 0 {
		t.Fatalf("bare = %+v, %v", bare, err)
	}

	tooManyArgs := `["` + strings.Repeat(`a","`, int(acpAgentSpec.Limit("argsMax"))) + `a"]`
	cases := map[string]string{
		"bad id":             `{"id":"Demo","title":"D","command":"gemini"}`,
		"long id":            `{"id":"` + strings.Repeat("a", 65) + `","title":"D","command":"gemini"}`,
		"no title":           `{"id":"d","command":"gemini"}`,
		"no command":         `{"id":"d","title":"D"}`,
		"absolute command":   `{"id":"d","title":"D","command":"/usr/local/bin/gemini"}`,
		"relative command":   `{"id":"d","title":"D","command":"./gemini"}`,
		"missing executable": `{"id":"d","title":"D","command":"${pluginRoot}/bin/none"}`,
		"project command":    `{"id":"d","title":"D","command":"${projectRoot}/agent"}`,
		"unknown variable":   `{"id":"d","title":"D","command":"gemini","args":["${env:HOME}"]}`,
		"denied env PATH":    `{"id":"d","title":"D","command":"gemini","env":{"PATH":"/tmp"}}`,
		"denied LD_PRELOAD":  `{"id":"d","title":"D","command":"gemini","env":{"LD_PRELOAD":"/tmp/x.so"}}`,
		"too many args":      `{"id":"d","title":"D","command":"gemini","args":` + tooManyArgs + `}`,
		"unknown field":      `{"id":"d","title":"D","command":"gemini","cwd":"${projectRoot}"}`,
		"requiresProject":    `{"id":"d","title":"D","command":"gemini","requiresProject":false}`,
		"bad platform":       `{"id":"d","title":"D","command":"gemini","platforms":["plan9"]}`,
		"empty platforms":    `{"id":"d","title":"D","command":"gemini","platforms":[]}`,
		"empty description":  `{"id":"d","title":"D","command":"gemini","description":""}`,
		"long title":         `{"id":"d","title":"` + strings.Repeat("a", 65) + `","command":"gemini"}`,
		"bidi arg":           `{"id":"d","title":"D","command":"gemini","args":["a` + "‮" + `"]}`,
	}
	for name, raw := range cases {
		if _, err := decodeAcpAgentForTest(ctx, raw); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}

	ctx.manifest.Capabilities = []api.Capability{api.CapabilityUI}
	if err := requireContributionCapabilities(ctx.manifest, acpAgentSpec); err == nil || err.Error() != "contributes.acpAgents requires capability shell" {
		t.Fatalf("missing shell capability: %v", err)
	}
}

func TestAcpAgentSpecFingerprint(t *testing.T) {
	ctx := acpAgentTestContext(t)
	base := `{"id":"d","title":"D","command":"gemini","args":["--acp"],"env":{"A":"1"},"platforms":["darwin"]}`
	agent, err := decodeAcpAgentForTest(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := acpAgentSpecFingerprint(agent)
	if len(fingerprint) != 64 {
		t.Fatalf("fingerprint = %q", fingerprint)
	}
	relabelled, _ := decodeAcpAgentForTest(ctx, `{"id":"d","title":"Renamed","description":"Other","command":"gemini","args":["--acp"],"env":{"A":"1"},"platforms":["darwin"]}`)
	if acpAgentSpecFingerprint(relabelled) != fingerprint {
		t.Fatal("titles and descriptions must not change the fingerprint")
	}
	for name, raw := range map[string]string{
		"command":   `{"id":"d","title":"D","command":"${pluginRoot}/bin/agent","args":["--acp"],"env":{"A":"1"},"platforms":["darwin"]}`,
		"args":      `{"id":"d","title":"D","command":"gemini","args":["--acp","-v"],"env":{"A":"1"},"platforms":["darwin"]}`,
		"env":       `{"id":"d","title":"D","command":"gemini","args":["--acp"],"env":{"A":"2"},"platforms":["darwin"]}`,
		"platforms": `{"id":"d","title":"D","command":"gemini","args":["--acp"],"env":{"A":"1"},"platforms":["darwin","linux"]}`,
		"id":        `{"id":"e","title":"D","command":"gemini","args":["--acp"],"env":{"A":"1"},"platforms":["darwin"]}`,
	} {
		changed, err := decodeAcpAgentForTest(ctx, raw)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if acpAgentSpecFingerprint(changed) == fingerprint {
			t.Errorf("%s change kept the fingerprint", name)
		}
	}
}
