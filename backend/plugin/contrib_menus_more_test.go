package plugin

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"mincode/app/plugins/api"
)

// newMenuHostKeys are the host when keys of the menus added in host 0.3.0.
var newMenuHostKeys = map[string][]string{
	"git/changes/context":    {"git.group", "git.status", "git.extname"},
	"git/commit/context":     {"git.isMerge", "git.hasRefs"},
	"database/tree/context":  {"database.kind", "database.objectType", "database.tableKind", "database.connected"},
	"ssh/connection/context": {"ssh.status", "ssh.authType", "ssh.hasJumpHost"},
	"sftp/file/context":      {"sftp.kind", "sftp.extname", "sftp.protocol"},
	"redis/key/context":      {"redis.type", "redis.hasTtl"},
	"http/request/context":   {"http.method", "http.scheme"},
}

func strictMenuError(t *testing.T, menus string, capabilities ...api.Capability) (Manifest, error) {
	t.Helper()
	manifest, err := readManifest(writeMenusManifest(t, menus, capabilities...))
	if err != nil {
		t.Fatalf("lenient load failed: %v", err)
	}
	return manifest, manifest.contributionIssuesError()
}

func TestMenuNewPlacementsHostKeys(t *testing.T) {
	if len(menuHostContextKeys) != 12 {
		t.Fatalf("menuHostContextKeys has %d menus, want 12", len(menuHostContextKeys))
	}
	for menu, keys := range newMenuHostKeys {
		if !slices.Equal(menuHostContextKeys[menu], keys) {
			t.Fatalf("%s host keys = %v, want %v", menu, menuHostContextKeys[menu], keys)
		}
		menus := fmt.Sprintf(`{%q:[{"command":"example.run","when":%q}]}`, menu, strings.Join(keys, " && ")+` && platform == "mac" && myplugin.ready`)
		manifest, err := strictMenuError(t, menus)
		if err != nil {
			t.Fatalf("%s: host keys rejected: %v", menu, err)
		}
		if items := manifest.MenuItems(); len(items) != 1 || items[0].Menu != menu {
			t.Fatalf("%s: items = %+v", menu, items)
		}
	}
	foreign := map[string]string{
		"ssh/connection/context": "git.status",
		"git/changes/context":    "git.isMerge",
		"git/commit/context":     "database.kind",
		"database/tree/context":  "redis.type",
		"sftp/file/context":      "ssh.status",
		"redis/key/context":      "http.method",
		"http/request/context":   "sftp.kind",
		"sql/editor/context":     "database.objectType",
		"editor/context":         "database.connected",
	}
	for menu, key := range foreign {
		_, err := strictMenuError(t, fmt.Sprintf(`{%q:[{"command":"example.run","when":%q}]}`, menu, key))
		want := fmt.Sprintf("context key %q is not provided by menu %s", key, menu)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s with %s: err = %v, want %q", menu, key, err, want)
		}
	}
}

func TestMenuNewReservedPrefixes(t *testing.T) {
	for _, prefix := range []string{"git.", "ssh.", "sftp.", "redis.", "http."} {
		if !slices.Contains(menuReservedContextPrefixes, prefix) {
			t.Fatalf("%q is not reserved", prefix)
		}
		key := prefix + "x"
		_, err := strictMenuError(t, fmt.Sprintf(`{"editor/context":[{"command":"example.run","when":%q}]}`, key))
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("context key %q is not provided by menu editor/context", key)) {
			t.Fatalf("plugin key %s accepted in editor/context: %v", key, err)
		}
	}
	// Keys that only share a word with a prefix stay plugin keys.
	if _, err := strictMenuError(t, `{"editor/context":[{"command":"example.run","when":"github.ready && httpx.on && sshd.up"}]}`); err != nil {
		t.Fatalf("non-reserved plugin keys rejected: %v", err)
	}
}

func TestMenuPassContextCapabilityPerPlacement(t *testing.T) {
	required := map[string]api.Capability{
		"editor/context":         api.CapabilityWorkspaceRead,
		"sql/editor/context":     api.CapabilityWorkspaceRead,
		"project/tree/context":   api.CapabilityWorkspaceRead,
		"workbench/tab/context":  "",
		"terminal/context":       api.CapabilityWorkspaceRead,
		"git/changes/context":    api.CapabilityWorkspaceRead,
		"git/commit/context":     api.CapabilityWorkspaceRead,
		"database/tree/context":  api.CapabilityDatabaseSchema,
		"ssh/connection/context": api.CapabilityRemoteContext,
		"sftp/file/context":      api.CapabilityRemoteContext,
		"redis/key/context":      api.CapabilityRemoteContext,
		"http/request/context":   api.CapabilityRemoteContext,
	}
	// commandPalette and tray never accept passContext (TestMenuTrayEntryRules).
	if len(required) != len(menuSpec.Keys)-2 {
		t.Fatalf("table covers %d menus, contract has %d besides commandPalette and tray", len(required), len(menuSpec.Keys)-2)
	}
	for menu, capability := range required {
		if got, ok := menuPassContextCapability[menu]; capability == "" && ok || capability != "" && got != capability {
			t.Fatalf("%s: menuPassContextCapability = %q, want %q", menu, got, capability)
		}
		menus := fmt.Sprintf(`{%q:[{"command":"example.run","passContext":true}]}`, menu)
		if capability != "" {
			if _, err := strictMenuError(t, menus, capability); err != nil {
				t.Fatalf("%s with %s rejected: %v", menu, capability, err)
			}
		}
		// Every other menu capability does not stand in for the required one.
		others := []api.Capability{}
		for _, candidate := range []api.Capability{api.CapabilityWorkspaceRead, api.CapabilityDatabaseSchema, api.CapabilityRemoteContext} {
			if candidate != capability {
				others = append(others, candidate)
			}
		}
		manifest, err := strictMenuError(t, menus, others...)
		if capability == "" {
			if err != nil {
				t.Fatalf("%s needs no capability: %v", menu, err)
			}
			continue
		}
		want := fmt.Sprintf("menu item %q passContext in %s requires the %s capability", "example.run", menu, capability)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s without %s: err = %v, want %q", menu, capability, err, want)
		}
		if items := manifest.MenuItems(); len(items) != 0 {
			t.Fatalf("%s: lenient load kept the item: %+v", menu, items)
		}
		if issues := manifest.allContributionIssues(); len(issues) != 1 || issues[0].Key != menu || !strings.Contains(issues[0].Message, want) {
			t.Fatalf("%s: lenient issues = %+v", menu, issues)
		}
	}
	// commandPalette never accepts passContext, whatever the capabilities.
	_, err := strictMenuError(t, `{"commandPalette":[{"command":"example.run","passContext":true}]}`,
		api.CapabilityWorkspaceRead, api.CapabilityDatabaseSchema, api.CapabilityRemoteContext)
	if err == nil || !strings.Contains(err.Error(), "accepts only group, order and hidden") {
		t.Fatalf("commandPalette passContext: %v", err)
	}
	// Without passContext no capability is required anywhere.
	if _, err := strictMenuError(t, `{"ssh/connection/context":[{"command":"example.run"}],"database/tree/context":[{"command":"example.run"}],"git/commit/context":[{"command":"example.run"}]}`); err != nil {
		t.Fatalf("menus without passContext rejected: %v", err)
	}
}

// remote.context only discloses menu targets: omitted action permissions never
// inherit it and an action that lists it is rejected.
func TestRemoteContextNotActionPermission(t *testing.T) {
	declared := []api.Capability{api.CapabilityRemoteContext, api.CapabilityNetwork}
	got, err := resolveAIActionPermissions(ManifestUIAction{ID: "probe", Risk: "read"}, declared)
	if err != nil || !slices.Equal(got.Permissions, []api.Capability{api.CapabilityNetwork}) {
		t.Fatalf("inherit kept remote.context: %+v %v", got, err)
	}
	explicit := ManifestUIAction{ID: "probe", Risk: "dangerous", Permissions: []api.Capability{api.CapabilityRemoteContext}}
	if _, err := resolveAIActionPermissions(explicit, declared); err == nil || !strings.Contains(err.Error(), "not an action permission") {
		t.Fatalf("explicit remote.context permission accepted: %v", err)
	}
	manifest := &Manifest{ID: "remote.probe", APIVersion: 2, Capabilities: declared}
	manifest.Runtime.Type = "process"
	manifest.Contributes.Actions = []ManifestUIAction{{ID: "run", Risk: "dangerous", UICallable: true, Permissions: []api.Capability{api.CapabilityRemoteContext}}}
	if err := validateContributions(newPluginPathResolver(t.TempDir()), manifest); err == nil || !strings.Contains(err.Error(), "not an action permission") {
		t.Fatalf("manifest action with remote.context permission accepted: %v", err)
	}
}
