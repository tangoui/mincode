package plugin

import (
	"encoding/json"
	"strings"
	"testing"
)

func decodeTerminalLinkForTest(raw string) (TerminalLinkContribution, error) {
	value, _, err := decodeTerminalLinkEntry(&pointContext{spec: terminalLinkSpec}, 0, "", json.RawMessage(raw))
	if err != nil {
		return TerminalLinkContribution{}, err
	}
	return value.(TerminalLinkContribution), nil
}

func TestTerminalLinkDecodeDefaults(t *testing.T) {
	link, err := decodeTerminalLinkForTest(`{"id":"jira","pattern":"\\b([A-Z][A-Z0-9]{1,9}-\\d{1,7})\\b","target":{"type":"url","url":"https://jira.example.com/browse/{1}?from={0}"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(link.Terminals) != 3 || link.IgnoreCase || link.Target.URL == "" {
		t.Fatalf("unexpected link %+v", link)
	}
	file, err := decodeTerminalLinkForTest(`{"id":"demo","pattern":"([\\w./-]{1,200}\\.mcdemo):(\\d{1,7})(?::(\\d{1,5}))?","ignoreCase":true,"target":{"type":"file","path":"{1}","line":"{2}","column":"{3}"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(file.Terminals, ",") != "local" || !file.IgnoreCase || file.Target.Column != "{3}" {
		t.Fatalf("unexpected file link %+v", file)
	}
}

func TestTerminalLinkDecodeRejects(t *testing.T) {
	cases := map[string]string{
		"backreference":         `{"id":"a","pattern":"(a)\\1","target":{"type":"url","url":"https://x.dev/{1}"}}`,
		"named backref":         `{"id":"a","pattern":"(a)\\k<x>","target":{"type":"url","url":"https://x.dev/{1}"}}`,
		"lookbehind":            `{"id":"a","pattern":"(?<=a)(b)","target":{"type":"url","url":"https://x.dev/{1}"}}`,
		"negative lookbehind":   `{"id":"a","pattern":"(?<!a)(b)","target":{"type":"url","url":"https://x.dev/{1}"}}`,
		"lookahead":             `{"id":"a","pattern":"(b)(?=a)","target":{"type":"url","url":"https://x.dev/{1}"}}`,
		"named group":           `{"id":"a","pattern":"(?<x>b)","target":{"type":"url","url":"https://x.dev/"}}`,
		"inline flags":          `{"id":"a","pattern":"(?i)b","target":{"type":"url","url":"https://x.dev/"}}`,
		"unicode property":      `{"id":"a","pattern":"(\\p{L}+)","target":{"type":"url","url":"https://x.dev/{1}"}}`,
		"too many groups":       `{"id":"a","pattern":"(a)(a)(a)(a)(a)(a)(a)(a)(a)(a)","target":{"type":"url","url":"https://x.dev/"}}`,
		"too long":              `{"id":"a","pattern":"` + strings.Repeat("a", 257) + `","target":{"type":"url","url":"https://x.dev/"}}`,
		"empty":                 `{"id":"a","pattern":"","target":{"type":"url","url":"https://x.dev/"}}`,
		"control":               `{"id":"a","pattern":"a\u0007","target":{"type":"url","url":"https://x.dev/"}}`,
		"host placeholder":      `{"id":"a","pattern":"(a)","target":{"type":"url","url":"https://{1}/x"}}`,
		"subdomain placeholder": `{"id":"a","pattern":"(a)","target":{"type":"url","url":"https://{1}.x.dev/x"}}`,
		"query without path":    `{"id":"a","pattern":"(a)","target":{"type":"url","url":"https://x.dev?q={1}"}}`,
		"port placeholder":      `{"id":"a","pattern":"(a)","target":{"type":"url","url":"https://x.dev:{1}/x"}}`,
		"userinfo":              `{"id":"a","pattern":"(a)","target":{"type":"url","url":"https://user@x.dev/{1}"}}`,
		"javascript":            `{"id":"a","pattern":"(a)","target":{"type":"url","url":"javascript:alert({1})"}}`,
		"file scheme":           `{"id":"a","pattern":"(a)","target":{"type":"url","url":"file:///etc/{1}"}}`,
		"upper scheme":          `{"id":"a","pattern":"(a)","target":{"type":"url","url":"HTTPS://x.dev/{1}"}}`,
		"backslash":             `{"id":"a","pattern":"(a)","target":{"type":"url","url":"https://x.dev\\@evil.dev/{1}"}}`,
		"missing group":         `{"id":"a","pattern":"(a)","target":{"type":"url","url":"https://x.dev/{2}"}}`,
		"stray brace":           `{"id":"a","pattern":"(a)","target":{"type":"url","url":"https://x.dev/{name}"}}`,
		"file on ssh":           `{"id":"a","pattern":"(a)","target":{"type":"file","path":"{1}"},"terminals":["local","ssh"]}`,
		"file literal path":     `{"id":"a","pattern":"(a)","target":{"type":"file","path":"/etc/passwd"}}`,
		"file with url":         `{"id":"a","pattern":"(a)","target":{"type":"file","path":"{1}","url":"https://x.dev/"}}`,
		"url with path":         `{"id":"a","pattern":"(a)","target":{"type":"url","url":"https://x.dev/","path":"{1}"}}`,
		"bad type":              `{"id":"a","pattern":"(a)","target":{"type":"command","url":"https://x.dev/"}}`,
		"no target":             `{"id":"a","pattern":"(a)"}`,
		"bad terminal":          `{"id":"a","pattern":"(a)","target":{"type":"url","url":"https://x.dev/"},"terminals":["serial"]}`,
		"unknown field":         `{"id":"a","pattern":"(a)","target":{"type":"url","url":"https://x.dev/"},"html":true}`,
	}
	for name, raw := range cases {
		if _, err := decodeTerminalLinkForTest(raw); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestTerminalLinkPatternAllowsClassesAndEscapes(t *testing.T) {
	for _, pattern := range []string{`[(?<=]+(x)`, `\(\?<=\)(y)`, `(?:a|b)(c)`, `\d+\.\w+`} {
		if _, err := validateTerminalLinkPattern(pattern); err != nil {
			t.Errorf("%s: %v", pattern, err)
		}
	}
}
