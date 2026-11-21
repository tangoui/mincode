package backup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func mustMergeValue(t *testing.T, key, local, incoming string, mode Mode, includesSecrets bool) (json.RawMessage, []Change) {
	t.Helper()
	var l, in json.RawMessage
	if local != "" {
		l = json.RawMessage(local)
	}
	if incoming != "" {
		in = json.RawMessage(incoming)
	}
	got, changes, err := MergeValue(key, l, in, mode, includesSecrets)
	if err != nil {
		t.Fatalf("MergeValue(%q): %v", key, err)
	}
	return got, changes
}

func assertJSONEqual(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("result is not JSON: %v (%s)", err, got)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want is not JSON: %v", err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("result = %s\nwant     %s", got, want)
	}
}

// assertIDOrder checks the order of list elements, which assertJSONEqual
// also covers, but reports it more readably.
func assertIDOrder(t *testing.T, got json.RawMessage, want ...string) {
	t.Helper()
	var items []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(got, &items); err != nil {
		t.Fatalf("result is not a list: %v", err)
	}
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.ID
	}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
}

func assertChanges(t *testing.T, got []Change, want ...Change) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("changes = %+v\nwant      %+v", got, want)
	}
}

const (
	listLocal    = `[{"id":"a","name":"A","host":"h1"},{"id":"b","name":"B","host":"h2"},{"id":"x","name":"X","host":"h9"}]`
	listIncoming = `[{"id":"a","name":"A","host":"h1-new"},{"id":"b","name":"B","host":"h2"},{"id":"c","name":"C","host":"h3"}]`
)

func TestMergeListByIDKeepsLocalOnly(t *testing.T) {
	got, changes := mustMergeValue(t, "app_ssh_connections_v1", listLocal, listIncoming, ModeMerge, true)
	// Incoming order first, then local-only elements in local order.
	assertIDOrder(t, got, "a", "b", "c", "x")
	assertJSONEqual(t, got, `[{"id":"a","name":"A","host":"h1-new"},{"id":"b","name":"B","host":"h2"},{"id":"c","name":"C","host":"h3"},{"id":"x","name":"X","host":"h9"}]`)
	assertChanges(t, changes,
		Change{Kind: "update", Label: "A"},
		Change{Kind: "add", Label: "C"},
		Change{Kind: "keep", Label: "X"},
	)
}

func TestOverwriteListDropsLocalOnly(t *testing.T) {
	got, changes := mustMergeValue(t, "app_ssh_connections_v1", listLocal, listIncoming, ModeOverwrite, true)
	assertJSONEqual(t, got, listIncoming)
	assertChanges(t, changes,
		Change{Kind: "update", Label: "A"},
		Change{Kind: "add", Label: "C"},
		Change{Kind: "remove", Label: "X"},
	)
}

func TestMergeListElementBackupWins(t *testing.T) {
	// A matched element is restored as in the backup: its local-only fields go.
	got, changes := mustMergeValue(t, "k",
		`[{"id":"a","name":"A","extra":1}]`,
		`[{"id":"a","name":"A"}]`, ModeMerge, true)
	assertJSONEqual(t, got, `[{"id":"a","name":"A"}]`)
	assertChanges(t, changes, Change{Kind: "update", Label: "A"})
}

func TestMergeListIntoAbsentLocal(t *testing.T) {
	for _, local := range []string{"", "null"} {
		got, changes := mustMergeValue(t, "k", local, `[{"id":"a","name":"A"},{"id":"b"}]`, ModeMerge, true)
		assertJSONEqual(t, got, `[{"id":"a","name":"A"},{"id":"b"}]`)
		assertChanges(t, changes, Change{Kind: "add", Label: "A"}, Change{Kind: "add", Label: "b"})
	}
}

func TestListWithoutIDsReplacedWholesale(t *testing.T) {
	cases := []struct{ name, local, incoming string }{
		{"strings", `["a","x"]`, `["a","b"]`},
		{"one element lacks id", `[{"id":"a"},{"id":"x"}]`, `[{"id":"a"},{"name":"no id"}]`},
		{"empty id", `[{"id":"a"},{"id":"x"}]`, `[{"id":""}]`},
		{"numeric id", `[{"id":"a"}]`, `[{"id":1}]`},
	}
	for _, tc := range cases {
		for _, mode := range []Mode{ModeMerge, ModeOverwrite} {
			t.Run(tc.name+"/"+string(mode), func(t *testing.T) {
				got, changes := mustMergeValue(t, "app_list_v1", tc.local, tc.incoming, mode, true)
				assertJSONEqual(t, got, tc.incoming)
				assertChanges(t, changes, Change{Kind: "update", Label: "app_list_v1"})
			})
		}
	}
}

func TestUnchangedValueHasNoChanges(t *testing.T) {
	for _, mode := range []Mode{ModeMerge, ModeOverwrite} {
		_, changes := mustMergeValue(t, "k", `{"a":1,"b":[{"id":"x"}]}`, `{"b":[{"id":"x"}],"a":1}`, mode, true)
		assertChanges(t, changes)
		_, changes = mustMergeValue(t, "k", `"same"`, `"same"`, mode, true)
		assertChanges(t, changes)
	}
}

func TestMergeObjectRecursive(t *testing.T) {
	local := `{"theme":"dark","editor":{"fontSize":12,"tabSize":2},"profiles":[{"id":"p1","name":"Local"}],"localOnly":true}`
	incoming := `{"theme":"light","editor":{"fontSize":14,"wrap":true},"profiles":[{"id":"p2","name":"Backup"}]}`

	got, changes := mustMergeValue(t, "app_settings_v2", local, incoming, ModeMerge, true)
	assertJSONEqual(t, got, `{"theme":"light","editor":{"fontSize":14,"wrap":true,"tabSize":2},"profiles":[{"id":"p2","name":"Backup"},{"id":"p1","name":"Local"}],"localOnly":true}`)
	assertChanges(t, changes,
		Change{Kind: "update", Label: "theme"},
		Change{Kind: "update", Label: "editor.fontSize"},
		Change{Kind: "add", Label: "editor.wrap"},
		Change{Kind: "keep", Label: "editor.tabSize"},
		Change{Kind: "add", Label: "Backup"},
		Change{Kind: "keep", Label: "Local"},
		Change{Kind: "keep", Label: "localOnly"},
	)

	got, changes = mustMergeValue(t, "app_settings_v2", local, incoming, ModeOverwrite, true)
	assertJSONEqual(t, got, incoming)
	assertChanges(t, changes,
		Change{Kind: "update", Label: "theme"},
		Change{Kind: "update", Label: "editor.fontSize"},
		Change{Kind: "add", Label: "editor.wrap"},
		Change{Kind: "remove", Label: "editor.tabSize"},
		Change{Kind: "add", Label: "Backup"},
		Change{Kind: "remove", Label: "Local"},
		Change{Kind: "remove", Label: "localOnly"},
	)
}

func TestMergeScalarUsesKeyLabel(t *testing.T) {
	got, changes := mustMergeValue(t, "app_locale_v1", `"zh"`, `"en"`, ModeMerge, true)
	assertJSONEqual(t, got, `"en"`)
	assertChanges(t, changes, Change{Kind: "update", Label: "app_locale_v1"})

	got, changes = mustMergeValue(t, "app_locale_v1", "", `"en"`, ModeOverwrite, true)
	assertJSONEqual(t, got, `"en"`)
	assertChanges(t, changes, Change{Kind: "add", Label: "app_locale_v1"})
}

func TestMergeValueNilIncomingKeepsLocal(t *testing.T) {
	for _, mode := range []Mode{ModeMerge, ModeOverwrite} {
		got, changes, err := MergeValue("k", json.RawMessage(`{"a":1}`), nil, mode, false)
		if err != nil {
			t.Fatalf("MergeValue: %v", err)
		}
		if string(got) != `{"a":1}` {
			t.Fatalf("got %s, want local unchanged", got)
		}
		assertChanges(t, changes)
	}
}

func TestMergeValueRejectsInvalidJSON(t *testing.T) {
	if _, _, err := MergeValue("k", json.RawMessage(`{"a":1}`), json.RawMessage(`{bad`), ModeMerge, true); err == nil {
		t.Fatal("invalid incoming accepted")
	}
	if _, _, err := MergeValue("k", json.RawMessage(`[1,`), json.RawMessage(`[]`), ModeMerge, true); err == nil {
		t.Fatal("invalid local accepted")
	}
	if _, _, err := MergeValue("k", nil, json.RawMessage(`1`), Mode("replace"), true); err == nil {
		t.Fatal("unknown mode accepted")
	}
}

func TestNoSecretsBackupKeepsLocalPasswords(t *testing.T) {
	local := `[{"id":"s1","name":"prod","host":"old","password":"enc:v2:abc","env":{"API_KEY":"enc:v2:k","MODE":"x"},"authorization":{"user":"u","pass":"enc:v2:p"}},{"id":"s2","name":"dev","passphrase":"enc:v2:q"}]`
	incoming := `[{"id":"s1","name":"prod","host":"new","password":"","env":{"API_KEY":"","MODE":"y"},"authorization":{"user":"","pass":""}},{"id":"s2","name":"dev"}]`

	got, changes := mustMergeValue(t, "app_ssh_connections_v1", local, incoming, ModeOverwrite, false)
	assertJSONEqual(t, got, `[{"id":"s1","name":"prod","host":"new","password":"enc:v2:abc","env":{"API_KEY":"enc:v2:k","MODE":"y"},"authorization":{"user":"u","pass":"enc:v2:p"}},{"id":"s2","name":"dev","passphrase":"enc:v2:q"}]`)
	// s2 only differs by its redacted secret, so it is unchanged.
	assertChanges(t, changes, Change{Kind: "update", Label: "prod"})

	// Top-level object, secret field missing from the backup.
	got, _ = mustMergeValue(t, "app_settings_v2", `{"apiKey":"enc:v2:z","theme":"dark","Access-Token":"t"}`, `{"theme":"light","accessToken":""}`, ModeOverwrite, false)
	assertJSONEqual(t, got, `{"theme":"light","accessToken":"","apiKey":"enc:v2:z","Access-Token":"t"}`)

	// A secret-named field holding an object whose values are all empty.
	got, _ = mustMergeValue(t, "k", `{"cookie":{"a":"1","b":["2"]}}`, `{"cookie":{"a":"","b":[""]}}`, ModeOverwrite, false)
	assertJSONEqual(t, got, `{"cookie":{"a":"1","b":["2"]}}`)

	// A non-empty secret in the backup still wins.
	got, _ = mustMergeValue(t, "k", `{"token":"enc:v2:old"}`, `{"token":"enc:v2:new"}`, ModeOverwrite, false)
	assertJSONEqual(t, got, `{"token":"enc:v2:new"}`)

	// Merge mode behaves the same.
	got, _ = mustMergeValue(t, "app_ssh_connections_v1", local, incoming, ModeMerge, false)
	assertJSONEqual(t, got, `[{"id":"s1","name":"prod","host":"new","password":"enc:v2:abc","env":{"API_KEY":"enc:v2:k","MODE":"y"},"authorization":{"user":"u","pass":"enc:v2:p"}},{"id":"s2","name":"dev","passphrase":"enc:v2:q"}]`)
}

func TestSecretsBackupOverwritesLocalPasswords(t *testing.T) {
	got, changes := mustMergeValue(t, "k", `{"password":"enc:v2:abc"}`, `{"password":""}`, ModeOverwrite, true)
	assertJSONEqual(t, got, `{"password":""}`)
	assertChanges(t, changes, Change{Kind: "update", Label: "password"})

	got, changes = mustMergeValue(t, "k", `{"password":"enc:v2:abc","a":1}`, `{"a":1}`, ModeOverwrite, true)
	assertJSONEqual(t, got, `{"a":1}`)
	assertChanges(t, changes, Change{Kind: "remove", Label: "password"})
}

func TestIsSecretFieldNameNormalizes(t *testing.T) {
	for _, name := range []string{"password", "PASSWORD", "api_key", "Api-Key", "oauth2.clientSecret", "TOTP URI", "authorization"} {
		if !isSecretFieldName(name) {
			t.Errorf("isSecretFieldName(%q) = false", name)
		}
	}
	for _, name := range []string{"", "name", "host", "env", "headers", "passwordHint"} {
		if isSecretFieldName(name) {
			t.Errorf("isSecretFieldName(%q) = true", name)
		}
	}
}

func TestSecretNamesMatchFrontend(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "vue-app", "src", "domain", "settings", "config-sync.ts"))
	if err != nil {
		t.Fatalf("read config-sync.ts: %v", err)
	}
	block := regexp.MustCompile(`SECRET_FIELD_NAMES\s*=\s*new Set\(\[([^\]]*)\]\)`).FindSubmatch(raw)
	if block == nil {
		t.Fatal("SECRET_FIELD_NAMES literal not found in config-sync.ts")
	}
	var frontend []string
	for _, m := range regexp.MustCompile(`["']([^"']+)["']`).FindAllSubmatch(block[1], -1) {
		frontend = append(frontend, string(m[1]))
	}
	if len(frontend) == 0 {
		t.Fatal("SECRET_FIELD_NAMES is empty")
	}
	var goNames []string
	for name := range secretFieldNames {
		goNames = append(goNames, name)
	}
	sort.Strings(frontend)
	sort.Strings(goNames)
	if !reflect.DeepEqual(frontend, goNames) {
		t.Fatalf("secret field names drifted\nfrontend %v\ngo       %v", frontend, goNames)
	}
}

func TestMergeKV(t *testing.T) {
	local := map[string]json.RawMessage{
		"a": json.RawMessage(`{"k": 1}`),
		"b": json.RawMessage(`"x"`),
		"z": json.RawMessage(`0`),
	}
	incoming := map[string]json.RawMessage{
		"a": json.RawMessage(`{ "k" : 1 }`),
		"b": json.RawMessage(`"y"`),
		"d": json.RawMessage(`true`),
	}

	got, changes := MergeKV(local, incoming, ModeMerge)
	if len(got) != 4 || string(got["b"]) != `"y"` || string(got["d"]) != `true` || string(got["z"]) != `0` || got["a"] == nil {
		t.Fatalf("merge result = %v", rawMapString(got))
	}
	assertChanges(t, changes,
		Change{Kind: "update", Label: "b"},
		Change{Kind: "add", Label: "d"},
		Change{Kind: "keep", Label: "z"},
	)

	got, changes = MergeKV(local, incoming, ModeOverwrite)
	if len(got) != 3 || got["z"] != nil || string(got["b"]) != `"y"` || string(got["d"]) != `true` || got["a"] == nil {
		t.Fatalf("overwrite result = %v", rawMapString(got))
	}
	assertChanges(t, changes,
		Change{Kind: "update", Label: "b"},
		Change{Kind: "add", Label: "d"},
		Change{Kind: "remove", Label: "z"},
	)

	got, changes = MergeKV(nil, nil, ModeOverwrite)
	if len(got) != 0 || len(changes) != 0 {
		t.Fatalf("empty KV = %v, %v", got, changes)
	}
}

func rawMapString(m map[string]json.RawMessage) string {
	parts := make([]string, 0, len(m))
	for k, v := range m {
		parts = append(parts, k+"="+string(v))
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}

func TestMergeFileSet(t *testing.T) {
	local := []string{"b.txt", "a.txt", "dir/x.txt"}
	incoming := []string{"c.txt", "a.txt", "b.txt"}

	write, remove, changes := MergeFileSet(local, incoming, ModeMerge)
	if !reflect.DeepEqual(write, []string{"a.txt", "b.txt", "c.txt"}) {
		t.Fatalf("merge write = %v", write)
	}
	if len(remove) != 0 {
		t.Fatalf("merge remove = %v, want empty", remove)
	}
	assertChanges(t, changes,
		Change{Kind: "update", Label: "a.txt"},
		Change{Kind: "update", Label: "b.txt"},
		Change{Kind: "add", Label: "c.txt"},
		Change{Kind: "keep", Label: "dir/x.txt"},
	)

	write, remove, changes = MergeFileSet(local, incoming, ModeOverwrite)
	if !reflect.DeepEqual(write, []string{"a.txt", "b.txt", "c.txt"}) {
		t.Fatalf("overwrite write = %v", write)
	}
	if !reflect.DeepEqual(remove, []string{"dir/x.txt"}) {
		t.Fatalf("overwrite remove = %v", remove)
	}
	assertChanges(t, changes,
		Change{Kind: "update", Label: "a.txt"},
		Change{Kind: "update", Label: "b.txt"},
		Change{Kind: "add", Label: "c.txt"},
		Change{Kind: "remove", Label: "dir/x.txt"},
	)
}

func TestChangeLabelsNeverContainValues(t *testing.T) {
	const secret = "s3cr3t"
	var all []Change
	collect := func(key, local, incoming string, mode Mode, includesSecrets bool) {
		_, changes := mustMergeValue(t, key, local, incoming, mode, includesSecrets)
		all = append(all, changes...)
	}
	for _, mode := range []Mode{ModeMerge, ModeOverwrite} {
		for _, includesSecrets := range []bool{true, false} {
			collect("k_scalar", `"old"`, `"s3cr3t"`, mode, includesSecrets)
			collect("k_list", `["s3cr3t"]`, `["s3cr3t","x"]`, mode, includesSecrets)
			collect("k_ids", `[{"id":"a","password":"s3cr3t"},{"id":"x","host":"s3cr3t"}]`,
				`[{"id":"a","password":"s3cr3t-new"},{"id":"c","token":"s3cr3t"}]`, mode, includesSecrets)
			collect("k_obj", `{"apiKey":"s3cr3t","nested":{"v":"s3cr3t"},"gone":"s3cr3t"}`,
				`{"apiKey":"s3cr3t2","nested":{"v":"s3cr3t3"},"new":"s3cr3t"}`, mode, includesSecrets)
		}
		_, kv := MergeKV(map[string]json.RawMessage{"a": json.RawMessage(`"s3cr3t"`), "z": json.RawMessage(`"s3cr3t"`)},
			map[string]json.RawMessage{"a": json.RawMessage(`"s3cr3t2"`), "b": json.RawMessage(`"s3cr3t"`)}, mode)
		all = append(all, kv...)
	}
	if len(all) == 0 {
		t.Fatal("no changes collected")
	}
	for _, change := range all {
		if strings.Contains(change.Label, secret) {
			t.Fatalf("label %q contains a value", change.Label)
		}
		switch change.Kind {
		case "add", "update", "remove", "keep":
		default:
			t.Fatalf("unexpected change kind %q", change.Kind)
		}
	}
}

func TestNoSecretsKeepsBlankedMCPEnvAndHeaders(t *testing.T) {
	local := `{"mcpServers":[{"id":"m1","name":"tools","env":{"OPENAI_API_KEY":"sk-plain","MODE":"x"},"headers":{"X-Api-Key":"enc:v2:hdr"}}]}`
	incoming := `{"mcpServers":[{"id":"m1","name":"tools","env":{"OPENAI_API_KEY":"","MODE":""},"headers":{"X-Api-Key":""}}]}`
	for _, mode := range []Mode{ModeMerge, ModeOverwrite} {
		t.Run(string(mode), func(t *testing.T) {
			got, changes := mustMergeValue(t, "app_settings_v2", local, incoming, mode, false)
			assertJSONEqual(t, got, local)
			assertChanges(t, changes)

			got, _ = mustMergeValue(t, "app_settings_v2", local, incoming, mode, true)
			assertJSONEqual(t, got, incoming)
		})
	}
	// A non-empty backup value still wins, and blanking is limited to env and headers.
	got, _ := mustMergeValue(t, "app_settings_v2",
		`{"mcpServers":[{"id":"m1","env":{"A":"old"},"args":{"A":"old"}}]}`,
		`{"mcpServers":[{"id":"m1","env":{"A":"new"},"args":{"A":""}}]}`, ModeOverwrite, false)
	assertJSONEqual(t, got, `{"mcpServers":[{"id":"m1","env":{"A":"new"},"args":{"A":""}}]}`)
}

func TestNoSecretsRehydratesHTTPBlanks(t *testing.T) {
	local := `{"requests":[{"id":"r1","name":"Login","url":"https://api.example.test/login","body":"{\"user\":1}","headers":[{"key":"Authorization","value":"Bearer t"},{"key":"Accept","value":"json"}]}]}`
	incoming := `{"requests":[{"id":"r1","name":"Login","url":"","body":"","headers":[{"key":"Accept","value":""},{"key":"Authorization","value":""}]},{"id":"r2","name":"New","url":"","body":""}]}`
	want := `{"requests":[{"id":"r1","name":"Login","url":"https://api.example.test/login","body":"{\"user\":1}","headers":[{"key":"Accept","value":"json"},{"key":"Authorization","value":"Bearer t"}]},{"id":"r2","name":"New","url":"","body":""}]}`
	for _, mode := range []Mode{ModeMerge, ModeOverwrite} {
		t.Run(string(mode), func(t *testing.T) {
			got, changes := mustMergeValue(t, "app_http_state_v1__workspace__w1", local, incoming, mode, false)
			assertJSONEqual(t, got, want)
			// r1 only differs by its header order once blanks are restored.
			assertChanges(t, changes, Change{Kind: "update", Label: "Login"}, Change{Kind: "add", Label: "New"})
		})
	}
	for _, key := range []string{"app_http_state_v1", "app_http_resources_v1"} {
		got, _ := mustMergeValue(t, key, local, incoming, ModeOverwrite, false)
		assertJSONEqual(t, got, want)
	}
	if !isHTTPStorageKey("app_http_state_v1__workspace__") || isHTTPStorageKey("app_http_state_v1_x") || isHTTPStorageKey("app_http_resources_v1__workspace__w1") {
		t.Fatal("isHTTPStorageKey rule mismatch")
	}
	// With secrets in the backup, its blanks are taken as real values.
	got, _ := mustMergeValue(t, "app_http_state_v1", local, incoming, ModeOverwrite, true)
	assertJSONEqual(t, got, incoming)
}

func TestNonHTTPKeyDoesNotRehydrateBlanks(t *testing.T) {
	local := `{"requests":[{"id":"r1","url":"https://api.example.test","body":"b","headers":[{"key":"Accept","value":"json"}]}]}`
	incoming := `{"requests":[{"id":"r1","url":"","body":"","headers":[{"key":"Accept","value":""}]}]}`
	for _, mode := range []Mode{ModeMerge, ModeOverwrite} {
		got, _ := mustMergeValue(t, "app_other_v1", local, incoming, mode, false)
		assertJSONEqual(t, got, incoming)
	}
}
