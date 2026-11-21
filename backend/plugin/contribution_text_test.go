package plugin

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestContributionTextRules(t *testing.T) {
	for _, r := range []rune{0, 0x1b, '\r', 0x7f, 0x85, 0x2028, 0x2029, 0x202e, 0x2067, 0x200b, 0x200f, 0x2060, 0xfeff} {
		if !isUnsafeContributionRune(r, true, true) {
			t.Fatalf("U+%04X must be unsafe", r)
		}
	}
	if isUnsafeContributionRune('\n', true, false) || !isUnsafeContributionRune('\n', false, true) || isUnsafeContributionRune('\t', false, true) || !isUnsafeContributionRune('\t', true, false) {
		t.Fatal("newline/tab switches are wrong")
	}
	if isUnsafeContributionRune('é', false, false) || isUnsafeContributionRune('中', false, false) {
		t.Fatal("printable text must be safe")
	}

	if value, err := validateContributionLine("  Deploy  ", 6, true); err != nil || value != "Deploy" {
		t.Fatalf("line = %q, %v", value, err)
	}
	if value, err := validateContributionLine("  ", 6, false); err != nil || value != "" {
		t.Fatalf("optional blank line = %q, %v", value, err)
	}
	for name, value := range map[string]string{"blank": " ", "long": "Deploys", "newline": "a\nb", "bidi": "a‮b", "utf8": "\xff"} {
		if _, err := validateContributionLine(value, 6, true); err == nil {
			t.Fatalf("line %s accepted", name)
		}
	}

	if value, err := validateContributionMultiline("a\r\n\tb\n", 5, 5); err != nil || value != "a\n\tb\n" {
		t.Fatalf("multiline = %q, %v", value, err)
	}
	for name, value := range map[string]string{"lone CR": "a\rb", "too many bytes": "abcdef", "zero width": "a​b"} {
		if _, err := validateContributionMultiline(value, 0, 5); err == nil {
			t.Fatalf("multiline %s accepted", name)
		}
	}

	localized, err := validateContributionLocalized(map[string]string{"zh-CN": " 部署 "}, 8, 2)
	if err != nil || localized["zh-CN"] != "部署" {
		t.Fatalf("localized = %v, %v", localized, err)
	}
	if localized, err := validateContributionLocalized(nil, 8, 2); err != nil || localized != nil {
		t.Fatalf("empty localized = %v, %v", localized, err)
	}
	for name, values := range map[string]map[string]string{
		"too many locales": {"a": "x", "b": "y", "c": "z"},
		"blank locale":     {" ": "x"},
		"long locale":      {strings.Repeat("a", 33): "x"},
		"blank value":      {"en": " "},
		"unsafe value":     {"en": "a⁦"},
	} {
		if _, err := validateContributionLocalized(values, 8, 2); err == nil {
			t.Fatalf("localized %s accepted", name)
		}
	}
}

func TestStrictJSONWalk(t *testing.T) {
	type visited struct {
		path, key string
		value     any
	}
	var nodes []visited
	canonical, err := strictJSONWalk([]byte("\xef\xbb\xbf{ \"z\": 1.50, \"a\": [true, null, \"<b>\"], \"$id\": {\"x\": [1]}, \"m\": {} }\n"), func(path []string, key string, value any) error {
		nodes = append(nodes, visited{strings.Join(path, "/"), key, value})
		if len(path) == 0 && key == "$id" {
			return errSkipJSONMember
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(canonical) != `{"z":1.50,"a":[true,null,"<b>"],"m":{}}` {
		t.Fatalf("canonical = %s", canonical)
	}
	want := []visited{
		{"", "", jsonObjectValue}, {"", "z", json.Number("1.50")}, {"", "a", jsonArrayValue},
		{"a", "0", true}, {"a", "1", nil}, {"a", "2", "<b>"}, {"", "$id", jsonObjectValue}, {"", "m", jsonObjectValue},
	}
	if len(nodes) != len(want) {
		t.Fatalf("visited %d nodes: %+v", len(nodes), nodes)
	}
	for index := range want {
		if nodes[index] != want[index] {
			t.Fatalf("node %d = %+v, want %+v", index, nodes[index], want[index])
		}
	}

	if canonical, err := strictJSONWalk([]byte(`{"a":{"b":[{"c":"d"}]}}`), nil); err != nil || string(canonical) != `{"a":{"b":[{"c":"d"}]}}` {
		t.Fatalf("nested = %s, %v", canonical, err)
	}
	for name, input := range map[string]string{
		"duplicate key":          `{"a":1,"a":2}`,
		"nested duplicate":       `{"x":{"a":1,"a":2}}`,
		"skipped duplicate":      `{"$id":{"a":1,"a":2}}`,
		"trailing data":          `{} {}`,
		"trailing garbage":       `{}x`,
		"comment":                "{/* c */}",
		"line comment":           "{}\n// c",
		"truncated":              `{"a":`,
		"invalid utf8":           "\"\xff\"",
		"empty":                  "",
		"too deep":               strings.Repeat("[", strictJSONMaxDepth+1) + strings.Repeat("]", strictJSONMaxDepth+1),
		"skip root":              `"$root"`,
		"skip array element":     `["$id"]`,
		"visitor error":          `{"bad":1}`,
		"single quoted":          `{'a':1}`,
		"trailing comma":         `{"a":1,}`,
		"skipped container deep": `{"$id":` + strings.Repeat("[", strictJSONMaxDepth+1) + strings.Repeat("]", strictJSONMaxDepth+1) + `}`,
	} {
		_, err := strictJSONWalk([]byte(input), func(path []string, key string, value any) error {
			switch {
			case key == "$id", value == "$root", value == "$id":
				return errSkipJSONMember
			case key == "bad":
				return errInvalidProbe
			}
			return nil
		})
		if err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

var errInvalidProbe = jsonProbeError("bad key")

type jsonProbeError string

func (err jsonProbeError) Error() string { return string(err) }
