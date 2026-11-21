package api

import (
	"slices"
	"testing"
)

func TestContractKeySinceHost(t *testing.T) {
	menus, ok := ContributionPointSpecFor("menus")
	if !ok || menus.SinceHost != "0.1.0" {
		t.Fatalf("menus spec = %+v", menus)
	}
	newKeys := []string{"git/changes/context", "git/commit/context", "database/tree/context", "ssh/connection/context", "sftp/file/context", "redis/key/context", "http/request/context"}
	for _, key := range newKeys {
		if !slices.Contains(menus.Keys, key) || menus.KeySince(key) != "0.3.0" {
			t.Fatalf("menus key %q since = %q", key, menus.KeySince(key))
		}
	}
	// tray (host 0.3.0) is covered by TestContractShellExtensions.
	if len(menus.KeySinceHost) != len(newKeys)+1 || menus.KeySince("tray") != "0.3.0" {
		t.Fatalf("menus keySinceHost = %v", menus.KeySinceHost)
	}
	for _, key := range []string{"editor/context", "commandPalette", "unknown/key"} {
		if menus.KeySince(key) != "0.1.0" {
			t.Fatalf("key %q since = %q, want the point sinceHost", key, menus.KeySince(key))
		}
	}
	if keys := menus.Keys; keys[len(keys)-1] != "commandPalette" || keys[len(keys)-3] != "http/request/context" {
		t.Fatalf("new menus must precede commandPalette: %v", keys)
	}
	for name, want := range map[string]int64{"commitSubjectMaxBytes": 1024, "remotePathMaxBytes": 4096, "redisKeyMaxBytes": 4096, "urlMaxLength": 2048, "passContextMaxBytes": 65536, "selectionMaxBytes": 32768} {
		if got := menus.Limit(name); got != want {
			t.Fatalf("menus limit %s = %d, want %d", name, got, want)
		}
	}
	tests, ok := ContributionPointSpecFor("testProviders")
	if !ok || tests.SinceHost != "0.1.0" || tests.KeySince("debugRun") != "0.3.0" || len(tests.KeySinceHost) != 1 {
		t.Fatalf("testProviders keySinceHost = %v", tests.KeySinceHost)
	}
	contract := ContributionPointsContractDocument()
	if len(contract.CapabilitySinceHost) != 2 || contract.CapabilitySinceHost[string(CapabilityRemoteContext)] != "0.3.0" || contract.CapabilitySinceHost[string(CapabilityUIWasm)] != "0.3.0" {
		t.Fatalf("capabilitySinceHost = %v", contract.CapabilitySinceHost)
	}
	if contract.CapabilitySince(CapabilityRemoteContext) != "0.3.0" || contract.CapabilitySince(CapabilityStorage) != "" {
		t.Fatal("CapabilitySince mismatch")
	}
}

func TestContractSinceHostValidation(t *testing.T) {
	const header = `{"version":1,"manifestMaxBytes":1024,`
	point := func(extra string) string {
		return `"points":{"probe":{"status":"enabled","kinds":["process"],"minApiVersion":2,"sinceHost":"0.2.0","shape":"map","maxEntries":4,"maxPerKey":2,"scope":"plugin","keys":["a","b"]` + extra + `}}`
	}
	valid := header + point(`,"keySinceHost":{"b":"0.10.0"}`) + `,"capabilitySinceHost":{"remote.context":"0.3.0"}}`
	parsed, err := parseContributionContract([]byte(valid))
	if err != nil {
		t.Fatalf("valid contract rejected: %v", err)
	}
	if probe := parsed.Points["probe"]; probe.KeySince("b") != "0.10.0" || probe.KeySince("a") != "0.2.0" {
		t.Fatalf("parsed probe = %+v", probe)
	}
	// List points name an optional entry field added after the point shipped.
	list := header + `"points":{"probe":{"status":"enabled","kinds":["process"],"minApiVersion":2,"sinceHost":"0.2.0","shape":"list","maxEntries":4,"scope":"plugin","keySinceHost":{"debugRun":"0.3.0"}}}}`
	if parsed, err := parseContributionContract([]byte(list)); err != nil || parsed.Points["probe"].KeySince("debugRun") != "0.3.0" {
		t.Fatalf("list keySinceHost: %v", err)
	}
	cases := map[string]string{
		"key not in keys":        header + point(`,"keySinceHost":{"c":"0.3.0"}`) + `}`,
		"lower than sinceHost":   header + point(`,"keySinceHost":{"a":"0.1.9"}`) + `}`,
		"bad key version":        header + point(`,"keySinceHost":{"a":"0.3"}`) + `}`,
		"bad capability version": header + point(``) + `,"capabilitySinceHost":{"remote.context":"v0.3.0"}}`,
		"unknown capability":     header + point(``) + `,"capabilitySinceHost":{"root":"0.3.0"}}`,
		"list field name":        header + `"points":{"probe":{"status":"enabled","kinds":["process"],"minApiVersion":2,"sinceHost":"0.2.0","shape":"list","maxEntries":4,"scope":"plugin","keySinceHost":{"Bad-field":"0.3.0"}}}}`,
		"object shape":           header + `"points":{"probe":{"status":"enabled","kinds":["process"],"minApiVersion":2,"sinceHost":"0.2.0","shape":"object","maxProperties":4,"scope":"plugin","keySinceHost":{"a":"0.3.0"}}}}`,
		"unknown field":          header + point(`,"keySince":{"a":"0.3.0"}`) + `}`,
	}
	for name, raw := range cases {
		if _, err := parseContributionContract([]byte(raw)); err == nil {
			t.Fatalf("%s: contract accepted", name)
		}
	}
}
