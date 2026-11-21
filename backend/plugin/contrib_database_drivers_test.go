package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"mincode/app/plugins/api"
)

func databaseDriverDecodeContext(t *testing.T) *pointContext {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "icons"), 0o755); err != nil {
		t.Fatal(err)
	}
	svg := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"><rect width="16" height="16" fill="#336699"/></svg>`
	if err := os.WriteFile(filepath.Join(root, "icons", "driver.svg"), []byte(svg), 0o644); err != nil {
		t.Fatal(err)
	}
	return &pointContext{
		root:     root,
		manifest: &Manifest{ID: "sample-driver", APIVersion: 2, Capabilities: []api.Capability{api.CapabilityDatabaseDriver}},
		spec:     databaseDriverSpec,
		validateAsset: func(_ string, ext string, data []byte) ([]byte, string, error) {
			return validateContributionImage(ext, data, databaseDriverImageLimits)
		},
	}
}

func decodeDatabaseDriverForTest(ctx *pointContext, raw string) (DatabaseDriverContribution, error) {
	value, _, err := decodeDatabaseDriverEntry(ctx, 0, "", json.RawMessage(raw))
	if err != nil {
		return DatabaseDriverContribution{}, err
	}
	return value.(DatabaseDriverContribution), nil
}

const databaseDriverFullEntry = `{
	"id":"clickhouse","title":"ClickHouse","titleLocalized":{"zh":"ClickHouse 驱动"},"description":"HTTP driver",
	"icon":"icons/driver.svg","category":"analytics","sqlDialect":"clickhouse","replaces":"clickhouse",
	"connection":{
		"fields":["password","host","username","port","database"],"required":["host","port"],"defaultPort":8123,
		"options":{
			"compression":{"type":"string","title":"Compression","enum":["lz4","none"],"default":"lz4"},
			"maxThreads":{"type":"integer","minimum":1,"maximum":64,"default":8},
			"secureLink":{"type":"boolean","default":false}
		}
	},
	"features":["tableRows","metadata.columns","cancel","metadata.tables","metadata.primaryKeys","rows.update"]
}`

func TestDatabaseDriverDecodeCanonicalSnapshot(t *testing.T) {
	ctx := databaseDriverDecodeContext(t)
	driver, err := decodeDatabaseDriverForTest(ctx, databaseDriverFullEntry)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(driver.Connection.Fields, []string{"host", "port", "database", "username", "password"}) ||
		!reflect.DeepEqual(driver.Connection.Required, []string{"host", "port"}) {
		t.Fatalf("fields not canonical: %+v", driver.Connection)
	}
	if !reflect.DeepEqual(driver.Features, []string{"cancel", "metadata.tables", "metadata.columns", "metadata.primaryKeys", "tableRows", "rows.update"}) {
		t.Fatalf("features not canonical: %v", driver.Features)
	}
	if driver.Icon == "" || driver.Icon == "icons/driver.svg" || driver.Connection.DefaultPort != 8123 || driver.Replaces != "clickhouse" {
		t.Fatalf("snapshot = %+v", driver)
	}
	if option := driver.Connection.Options["maxThreads"]; option.Default != int64(8) && option.Default != float64(8) && option.Default != json.Number("8") {
		t.Fatalf("integer default = %#v", option.Default)
	}
	if len(driver.SpecFingerprint) != 64 {
		t.Fatalf("fingerprint %q", driver.SpecFingerprint)
	}
	minimal, err := decodeDatabaseDriverForTest(ctx, `{"id":"lite","title":"Lite","category":"relational","connection":{"fields":["filePath"],"fileExtensions":[".db",".lite"]}}`)
	if err != nil {
		t.Fatal(err)
	}
	if minimal.SQLDialect != "generic" || len(minimal.Features) != 0 || minimal.Features == nil {
		t.Fatalf("defaults = %+v", minimal)
	}
	changed, err := decodeDatabaseDriverForTest(ctx, strings.Replace(databaseDriverFullEntry, `"default":8`, `"default":9`, 1))
	if err != nil || changed.SpecFingerprint == driver.SpecFingerprint {
		t.Fatalf("option default change must change the fingerprint: %v", err)
	}
}

func TestDatabaseDriverDecodeLocalizedTexts(t *testing.T) {
	ctx := databaseDriverDecodeContext(t)
	driver, err := decodeDatabaseDriverForTest(ctx, `{"id":"a","title":"A","category":"relational",
		"description":"Driver","descriptionLocalized":{"zh":"驱动","en":"Driver"},
		"connection":{"fields":["host"],"options":{
			"auth":{"type":"string","enum":["plain","md5"],"description":"Mode","descriptionLocalized":{"zh":"认证方式"},
				"enumLabelsLocalized":{"zh":["明文","MD5 摘要"],"en":["Plain","MD5"]}},
			"flag":{"type":"boolean","descriptionLocalized":{"zh":"开关"}}
		}}}`)
	if err != nil {
		t.Fatal(err)
	}
	if driver.DescriptionLocalized["zh"] != "驱动" {
		t.Fatalf("descriptionLocalized = %v", driver.DescriptionLocalized)
	}
	auth := driver.Connection.Options["auth"]
	if auth.DescriptionLocalized["zh"] != "认证方式" || !reflect.DeepEqual(auth.EnumLabelsLocalized["zh"], []string{"明文", "MD5 摘要"}) {
		t.Fatalf("option = %+v", auth)
	}
	if driver.Connection.Options["flag"].DescriptionLocalized["zh"] != "开关" {
		t.Fatal("boolean option descriptionLocalized dropped")
	}
	raw, _ := json.Marshal(driver)
	for _, want := range []string{`"descriptionLocalized":{"en":"Driver","zh":"驱动"}`, `"enumLabelsLocalized":{"en":["Plain","MD5"],"zh":["明文","MD5 摘要"]}`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("JSON %s lacks %s", raw, want)
		}
	}
	clone := cloneDatabaseDriverContribution(driver)
	clone.DescriptionLocalized["zh"] = "x"
	clone.Connection.Options["auth"].EnumLabelsLocalized["zh"][0] = "x"
	clone.Connection.Options["auth"].DescriptionLocalized["zh"] = "x"
	if driver.DescriptionLocalized["zh"] != "驱动" || auth.EnumLabelsLocalized["zh"][0] != "明文" || auth.DescriptionLocalized["zh"] != "认证方式" {
		t.Fatal("clone shares localized maps")
	}
}

func TestDatabaseDriverDecodeRejectsInvalidEntries(t *testing.T) {
	ctx := databaseDriverDecodeContext(t)
	base := `"title":"X","category":"relational"`
	cases := map[string]string{
		"bad id":                 `{"id":"Bad",` + base + `,"connection":{"fields":["host"]}}`,
		"unknown field":          `{"id":"a",` + base + `,"connection":{"fields":["host"]},"command":"x"}`,
		"missing connection":     `{"id":"a",` + base + `}`,
		"missing title":          `{"id":"a","category":"relational","connection":{"fields":["host"]}}`,
		"bad category":           `{"id":"a","title":"X","category":"graph","connection":{"fields":["host"]}}`,
		"bad dialect":            `{"id":"a",` + base + `,"sqlDialect":"cobol","connection":{"fields":["host"]}}`,
		"bad replaces":           `{"id":"a",` + base + `,"replaces":"mysql","connection":{"fields":["host"]}}`,
		"no fields":              `{"id":"a",` + base + `,"connection":{"fields":[]}}`,
		"unknown conn field":     `{"id":"a",` + base + `,"connection":{"fields":["apiKey"]}}`,
		"duplicate field":        `{"id":"a",` + base + `,"connection":{"fields":["host","host"]}}`,
		"required not declared":  `{"id":"a",` + base + `,"connection":{"fields":["host"],"required":["port"]}}`,
		"port without field":     `{"id":"a",` + base + `,"connection":{"fields":["host"],"defaultPort":1}}`,
		"port out of range":      `{"id":"a",` + base + `,"connection":{"fields":["port"],"defaultPort":70000}}`,
		"extensions without fp":  `{"id":"a",` + base + `,"connection":{"fields":["host"],"fileExtensions":[".db"]}}`,
		"bad extension":          `{"id":"a",` + base + `,"connection":{"fields":["filePath"],"fileExtensions":["db"]}}`,
		"secret option":          `{"id":"a",` + base + `,"connection":{"fields":["host"],"options":{"apiKey":{"type":"string","secret":true}}}}`,
		"pattern option":         `{"id":"a",` + base + `,"connection":{"fields":["host"],"options":{"mode":{"type":"string","pattern":".*"}}}}`,
		"option collides":        `{"id":"a",` + base + `,"connection":{"fields":["host"],"options":{"host":{"type":"string"}}}}`,
		"option key pattern":     `{"id":"a",` + base + `,"connection":{"fields":["host"],"options":{"Mode":{"type":"string"}}}}`,
		"option type":            `{"id":"a",` + base + `,"connection":{"fields":["host"],"options":{"mode":{"type":"object"}}}}`,
		"default not in enum":    `{"id":"a",` + base + `,"connection":{"fields":["host"],"options":{"mode":{"type":"string","enum":["a"],"default":"b"}}}}`,
		"default wrong type":     `{"id":"a",` + base + `,"connection":{"fields":["host"],"options":{"n":{"type":"integer","default":"1"}}}}`,
		"fractional integer":     `{"id":"a",` + base + `,"connection":{"fields":["host"],"options":{"n":{"type":"integer","default":1.5}}}}`,
		"min above max":          `{"id":"a",` + base + `,"connection":{"fields":["host"],"options":{"n":{"type":"number","minimum":5,"maximum":1}}}}`,
		"boolean enum":           `{"id":"a",` + base + `,"connection":{"fields":["host"],"options":{"b":{"type":"boolean","enum":[true]}}}}`,
		"duplicate enum":         `{"id":"a",` + base + `,"connection":{"fields":["host"],"options":{"s":{"type":"string","enum":["x","x"]}}}}`,
		"unknown feature":        `{"id":"a",` + base + `,"connection":{"fields":["host"]},"features":["transactions"]}`,
		"duplicate feature":      `{"id":"a",` + base + `,"connection":{"fields":["host"]},"features":["cancel","cancel"]}`,
		"icon outside asset dir": `{"id":"a",` + base + `,"icon":"driver.svg","connection":{"fields":["host"]}}`,
		"labels without enum":    `{"id":"a",` + base + `,"connection":{"fields":["host"],"options":{"s":{"type":"string","enumLabelsLocalized":{"zh":["甲"]}}}}}`,
		"labels count mismatch":  `{"id":"a",` + base + `,"connection":{"fields":["host"],"options":{"s":{"type":"string","enum":["a","b"],"enumLabelsLocalized":{"zh":["甲"]}}}}}`,
		"empty enum label":       `{"id":"a",` + base + `,"connection":{"fields":["host"],"options":{"s":{"type":"string","enum":["a"],"enumLabelsLocalized":{"zh":[" "]}}}}}`,
		"bad label locale":       `{"id":"a",` + base + `,"connection":{"fields":["host"],"options":{"s":{"type":"string","enum":["a"],"enumLabelsLocalized":{"":["甲"]}}}}}`,
		"empty localized hint":   `{"id":"a",` + base + `,"connection":{"fields":["host"],"options":{"s":{"type":"string","descriptionLocalized":{"zh":""}}}}}`,
		"empty localized desc":   `{"id":"a",` + base + `,"descriptionLocalized":{"zh":""},"connection":{"fields":["host"]}}`,
	}
	for name, raw := range cases {
		if _, err := decodeDatabaseDriverForTest(ctx, raw); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestDatabaseDriverFinalizeDependenciesAndReplaces(t *testing.T) {
	entry := func(index int, id, replaces string, features ...string) contributionEntry {
		return contributionEntry{Index: index, ID: id, Value: DatabaseDriverContribution{ID: id, Replaces: replaces, Features: features}}
	}
	kept, issues := finalizeDatabaseDriverEntries(nil, []contributionEntry{
		entry(0, "a", "clickhouse", "metadata.tables", "metadata.columns", "metadata.primaryKeys", "rows.update"),
		entry(1, "b", "clickhouse"),
		entry(2, "c", "", "rows.delete", "metadata.columns"),
		entry(3, "d", "", "destructive.dropTable"),
		entry(4, "e", "duckdb", "metadata.tables", "destructive.clearTable"),
	}, nil)
	var ids []string
	for _, entry := range kept {
		ids = append(ids, entry.ID)
	}
	if !reflect.DeepEqual(ids, []string{"a", "e"}) || len(issues) != 3 {
		t.Fatalf("kept %v issues %+v", ids, issues)
	}
	for _, issue := range issues {
		if issue.Point != databaseDriverPointKey || issue.ID == "" {
			t.Fatalf("issue = %+v", issue)
		}
	}
}

func TestDatabaseDriversExpandPermission(t *testing.T) {
	resolved := func(drivers ...DatabaseDriverContribution) *resolvedContribution {
		entries := make([]any, len(drivers))
		for index, driver := range drivers {
			entries[index] = driver
		}
		return &resolvedContribution{Entries: entries}
	}
	base := DatabaseDriverContribution{ID: "a", Features: []string{"metadata.tables"}}
	withWrite := DatabaseDriverContribution{ID: "a", Features: []string{"metadata.tables", "destructive.dropTable"}}
	cases := []struct {
		name      string
		current   *resolvedContribution
		candidate *resolvedContribution
		want      bool
	}{
		{"same", resolved(base), resolved(base), false},
		{"removed", resolved(base), resolved(), false},
		{"new driver", resolved(base), resolved(base, DatabaseDriverContribution{ID: "b"}), true},
		{"first declaration", nil, resolved(base), true},
		{"new write feature", resolved(base), resolved(withWrite), true},
		{"dropped write feature", resolved(withWrite), resolved(base), false},
		{"replaces changed", resolved(base), resolved(DatabaseDriverContribution{ID: "a", Replaces: "duckdb", Features: base.Features}), true},
		{"new snapshotWrite feature", resolved(base), resolved(DatabaseDriverContribution{ID: "a", Features: []string{"metadata.tables", "transaction.snapshotWrite"}}), true},
		{"new read feature", resolved(base), resolved(DatabaseDriverContribution{ID: "a", Features: []string{"cancel", "metadata.tables"}}), false},
	}
	for _, tc := range cases {
		if got := databaseDriversExpandPermission(tc.current, tc.candidate); got != tc.want {
			t.Errorf("%s: got %v", tc.name, got)
		}
	}
}

func TestDatabaseDriverOptionValueValidation(t *testing.T) {
	one, ten := 1.0, 10.0
	integer := DatabaseDriverOption{Type: databaseDriverOptionInteger, Minimum: &one, Maximum: &ten}
	for _, value := range []any{0.0, 11.0, 2.5, "3", true} {
		if _, err := validateDatabaseDriverOptionValue(integer, value, true); err == nil {
			t.Errorf("integer accepted %#v", value)
		}
	}
	if _, err := validateDatabaseDriverOptionValue(integer, 5.0, true); err != nil {
		t.Fatal(err)
	}
	text := DatabaseDriverOption{Type: databaseDriverOptionString, MaxLength: 3}
	if _, err := validateDatabaseDriverOptionValue(text, "long", true); err == nil {
		t.Fatal("maxLength not enforced")
	}
	if !databaseDriverFieldSecret("password") || !databaseDriverFieldSecret("token") || databaseDriverFieldSecret("username") {
		t.Fatal("secret field set is wrong")
	}
}

func TestCloneDatabaseDriverContributionKeepsListsAsArrays(t *testing.T) {
	driver := cloneDatabaseDriverContribution(DatabaseDriverContribution{
		ID:         "duckdb",
		Connection: DatabaseDriverConnection{Fields: []string{"filePath"}},
	})
	data, err := json.Marshal(driver)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"required":[]`, `"features":[]`, `"fields":["filePath"]`} {
		if !strings.Contains(string(data), field) {
			t.Fatalf("%s missing from %s", field, data)
		}
	}
}
