package plugin

import (
	"encoding/json"
	"strings"
	"testing"
)

func formatterTestContext() *pointContext {
	return &pointContext{
		manifest: &Manifest{ID: "acme.fmt", APIVersion: 2},
		spec:     formatterSpec,
		actions: map[string]ManifestUIAction{
			"format": {ID: "format", Risk: "read", MaxPayloadBytes: 262144},
			"write":  {ID: "write", Risk: "write", MaxPayloadBytes: 1024},
		},
	}
}

func TestFormatterDecode(t *testing.T) {
	ctx := formatterTestContext()
	value, id, err := decodeFormatterEntry(ctx, 0, "", json.RawMessage(`{"id":"mcdemo","title":" Demo ","titleLocalized":{"zh":"演示"},"languages":["mcdemo","c++","json"],"action":"format","range":true}`))
	if err != nil || id != "mcdemo" {
		t.Fatalf("decode = %v, %q", err, id)
	}
	formatter := value.(FormatterContribution)
	if formatter.Title != "Demo" || !formatter.Range || len(formatter.Languages) != 3 || formatter.Action != "format" || formatter.TitleLocalized["zh"] != "演示" {
		t.Fatalf("formatter = %+v", formatter)
	}
	cases := map[string]string{
		`{"id":"Bad","title":"x","languages":["json"],"action":"format"}`:             "must match",
		`{"id":"a","title":"","languages":["json"],"action":"format"}`:                "title is required",
		`{"id":"a","title":"x","languages":[],"action":"format"}`:                     "needs 1-16 languages",
		`{"id":"a","title":"x","languages":["JSON"],"action":"format"}`:               `language "JSON"`,
		`{"id":"a","title":"x","languages":["json","json"],"action":"format"}`:        "repeats language",
		`{"id":"a","title":"x","languages":["json"],"action":"missing"}`:              "not one of this plugin's contributes.actions",
		`{"id":"a","title":"x","languages":["json"],"action":"write"}`:                `risk "read"`,
		`{"id":"a","title":"x","languages":["json"],"action":"format","command":"x"}`: "unknown field",
		`{"id":"a","title":"x\u202e","languages":["json"],"action":"format"}`:         "disallowed character",
	}
	for raw, want := range cases {
		if _, _, err := decodeFormatterEntry(ctx, 0, "", json.RawMessage(raw)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", raw, err, want)
		}
	}
}

func TestFormattersExpandPermission(t *testing.T) {
	base := FormatterContribution{ID: "a", Languages: []string{"json"}, Action: "format"}
	resolved := func(formatters ...FormatterContribution) *resolvedContribution {
		entries := make([]any, len(formatters))
		for index, formatter := range formatters {
			entries[index] = formatter
		}
		return &resolvedContribution{Entries: entries}
	}
	fewer := base
	fewer.Languages = nil
	moreLanguages := base
	moreLanguages.Languages = []string{"json", "yaml"}
	otherAction := base
	otherAction.Action = "format2"
	retitled := base
	retitled.Title = "New"
	cases := []struct {
		name               string
		current, candidate *resolvedContribution
		want               bool
	}{
		{"unchanged", resolved(base), resolved(base), false},
		{"retitled", resolved(base), resolved(retitled), false},
		{"removed language", resolved(base), resolved(fewer), false},
		{"removed formatter", resolved(base), resolved(), false},
		{"added language", resolved(base), resolved(moreLanguages), true},
		{"changed action", resolved(base), resolved(otherAction), true},
		{"new formatter", nil, resolved(base), true},
		{"no candidate", resolved(base), nil, false},
	}
	for _, test := range cases {
		if got := formattersExpandPermission(test.current, test.candidate); got != test.want {
			t.Errorf("%s = %v", test.name, got)
		}
	}
}
