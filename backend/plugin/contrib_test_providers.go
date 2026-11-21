package plugin

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Test providers (contributes.testProviders) are a process-only contribution
// point that requires workspace.read. An entry names a read-risk
// discoverAction and a runAction (any risk) of the plugin; the project
// sidebar "Tests" tab discovers tests through plugin_host_test_discover and
// runs the selected ones through plugin_host_test_run (test_provider_invoke.go)
// only on explicit user action. An optional debugRun (host 0.3.0) runs and
// debugs tests as debug sessions with streamed results (test_runs.go).
// Mirrors: tools/mincode-cli/provider_points.go
// (CLI), assertTestProvidersEntry (cloud) and
// vue-app/src/domain/plugins/test-providers.ts (frontend revalidation).
const testProviderPointKey = "testProviders"

var (
	testProviderSpec          = mustContributionSpec(testProviderPointKey)
	testProviderTitleMaxRunes = int(testProviderSpec.Limit("titleMaxRunes"))
	testProviderLocalesMax    = int(testProviderSpec.Limit("localesMax"))

	testProviderIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
)

type testProviderManifestEntry struct {
	ID             string                        `json:"id"`
	Title          string                        `json:"title"`
	TitleLocalized map[string]string             `json:"titleLocalized,omitempty"`
	DiscoverAction string                        `json:"discoverAction"`
	RunAction      string                        `json:"runAction"`
	DebugRun       *testProviderDebugRunManifest `json:"debugRun,omitempty"`
}

type testProviderDebugRunManifest struct {
	DebuggerType     *string `json:"debuggerType"`
	ResolveRunAction *string `json:"resolveRunAction"`
}

// TestProviderContribution is the snapshot entry. RunRisk is the runAction's
// declared risk, so the frontend can ask before running non-read actions.
// DebugRun (host 0.3.0, contract keySinceHost.debugRun) is set when the
// provider runs and debugs tests as debug sessions (test_runs.go).
type TestProviderContribution struct {
	ID             string                `json:"id"`
	Title          string                `json:"title"`
	TitleLocalized map[string]string     `json:"titleLocalized,omitempty"`
	DiscoverAction string                `json:"discoverAction"`
	RunAction      string                `json:"runAction"`
	RunRisk        string                `json:"runRisk"`
	DebugRun       *TestProviderDebugRun `json:"debugRun,omitempty"`
}

// TestProviderDebugRun names the plugin's own debugger type that runs the
// tests and its read-only, non-aiCallable action that resolves a run into a
// debug launch ({variables?, request, arguments}, like a debuggers
// resolveAction).
type TestProviderDebugRun struct {
	DebuggerType     string `json:"debuggerType"`
	ResolveRunAction string `json:"resolveRunAction"`
}

// testProviderActions lists the entry's action references in a fixed order
// (resolveRunAction is "" without debugRun).
func testProviderActions(provider TestProviderContribution) []string {
	resolveRun, debuggerType := "", ""
	if provider.DebugRun != nil {
		resolveRun, debuggerType = provider.DebugRun.ResolveRunAction, provider.DebugRun.DebuggerType
	}
	return []string{provider.DiscoverAction, provider.RunAction, provider.RunRisk, resolveRun, debuggerType}
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:              testProviderPointKey,
		Decode:           decodeTestProviderEntry,
		DuplicateMessage: func(id string) string { return fmt.Sprintf("duplicate test provider id %q", id) },
		IssuePrefix:      "invalid testProviders contribution",
		IssueText: func(issue ContributionIssue) string {
			if issue.ID != "" {
				return fmt.Sprintf("skipped test provider %q: %s", issue.ID, issue.Message)
			}
			return fmt.Sprintf("skipped contributes.testProviders[%d]: %s", issue.Entry, issue.Message)
		},
		EntryNoun: "test providers",
		// debugRun.debuggerType names one of this plugin's accepted debuggers.
		DependsOn: []string{debuggerPointKey},
		ExpandsPermission: func(current, candidate *resolvedContribution) bool {
			return contributionActionsExpand(current, candidate, func(entry any) (string, []string, bool) {
				provider, ok := entry.(TestProviderContribution)
				return provider.ID, testProviderActions(provider), ok
			})
		},
	})
}

func decodeTestProviderEntry(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var entry testProviderManifestEntry
	if err := decodeStrictJSON(raw, &entry, true); err != nil {
		return nil, "", fmt.Errorf("parse test provider: %w", err)
	}
	id := strings.TrimSpace(entry.ID)
	if !testProviderIDPattern.MatchString(id) {
		return nil, id, fmt.Errorf("test provider id %q must match ^[a-z][a-z0-9-]{0,31}$", id)
	}
	title, err := validateContributionLine(entry.Title, testProviderTitleMaxRunes, true)
	if err != nil {
		return nil, id, fmt.Errorf("test provider %q title %w", id, err)
	}
	titleLocalized, err := validateContributionLocalized(entry.TitleLocalized, testProviderTitleMaxRunes, testProviderLocalesMax)
	if err != nil {
		return nil, id, fmt.Errorf("test provider %q titleLocalized %w", id, err)
	}
	discoverAction, err := contributionReadActionRef(ctx, "test provider", id, "discoverAction", entry.DiscoverAction)
	if err != nil {
		return nil, id, err
	}
	runAction, run, err := contributionActionRef(ctx, "test provider", id, "runAction", entry.RunAction)
	if err != nil {
		return nil, id, err
	}
	if runAction == discoverAction {
		return nil, id, fmt.Errorf("test provider %q runAction must differ from discoverAction", id)
	}
	risk := run.Risk
	if risk == "" {
		risk = "read"
	}
	provider := TestProviderContribution{
		ID: id, Title: title, TitleLocalized: titleLocalized,
		DiscoverAction: discoverAction, RunAction: runAction, RunRisk: risk,
	}
	if entry.DebugRun != nil {
		if provider.DebugRun, err = decodeTestProviderDebugRun(ctx, id, *entry.DebugRun, discoverAction, runAction); err != nil {
			return nil, id, err
		}
	}
	return provider, id, nil
}

// decodeTestProviderDebugRun validates debugRun: debuggerType must be the
// type of one of this manifest's accepted debuggers entries (resolved first,
// see DependsOn), resolveRunAction one of its own read actions that is not
// aiCallable and differs from discoverAction and runAction.
func decodeTestProviderDebugRun(ctx *pointContext, id string, raw testProviderDebugRunManifest, discoverAction, runAction string) (*TestProviderDebugRun, error) {
	if raw.DebuggerType == nil || raw.ResolveRunAction == nil {
		return nil, fmt.Errorf("test provider %q debugRun needs debuggerType and resolveRunAction", id)
	}
	debuggerType := strings.TrimSpace(*raw.DebuggerType)
	if !slices.Contains(testProviderDeclaredDebuggers(ctx), debuggerType) {
		return nil, fmt.Errorf("test provider %q debugRun.debuggerType %q is not one of this plugin's contributes.debuggers types", id, debuggerType)
	}
	resolveRun, err := contributionReadActionRef(ctx, "test provider", id, "debugRun.resolveRunAction", *raw.ResolveRunAction)
	if err != nil {
		return nil, err
	}
	if ctx.actions[resolveRun].AICallable {
		return nil, fmt.Errorf("test provider %q debugRun.resolveRunAction %q must not be aiCallable", id, resolveRun)
	}
	if resolveRun == discoverAction || resolveRun == runAction {
		return nil, fmt.Errorf("test provider %q debugRun.resolveRunAction must differ from discoverAction and runAction", id)
	}
	return &TestProviderDebugRun{DebuggerType: debuggerType, ResolveRunAction: resolveRun}, nil
}

// testProviderDeclaredDebuggers lists the types of the manifest's accepted
// debuggers entries.
func testProviderDeclaredDebuggers(ctx *pointContext) []string {
	resolved := ctx.resolved[debuggerPointKey]
	if resolved == nil {
		return nil
	}
	types := []string{}
	for _, entry := range resolved.Entries {
		if debugger, ok := entry.(DebuggerContribution); ok {
			types = append(types, debugger.Type)
		}
	}
	return types
}
