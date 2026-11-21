package plugin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func validFormatRequest() formatRequest {
	return formatRequest{
		PluginID: "runtime.test", FormatterID: "fmt", RequestID: "format-request-01", LanguageID: "json",
		FileName: "/Users/me/project/src/a.json", RelativePath: "src/a.json", Text: `{"a":1}`,
		Options: formatOptions{TabSize: 2, InsertSpaces: true, EOL: "\n"},
	}
}

func TestNormalizeFormatRequest(t *testing.T) {
	request := validFormatRequest()
	if err := normalizeFormatRequest(&request); err != nil {
		t.Fatal(err)
	}
	if request.FileName != "a.json" {
		t.Fatalf("fileName must be reduced to the basename: %q", request.FileName)
	}
	windows := validFormatRequest()
	windows.FileName, windows.RelativePath = `C:\work\b.json`, `src\b.json`
	if err := normalizeFormatRequest(&windows); err != nil || windows.FileName != "b.json" || windows.RelativePath != "src/b.json" {
		t.Fatalf("windows = %+v, %v", windows, err)
	}
	cases := map[string]func(*formatRequest){
		"formatter id":      func(r *formatRequest) { r.FormatterID = "Bad" },
		"language":          func(r *formatRequest) { r.LanguageID = "JSON" },
		"mode":              func(r *formatRequest) { r.Mode = "auto" },
		"empty file name":   func(r *formatRequest) { r.FileName = "/" },
		"bidi file name":    func(r *formatRequest) { r.FileName = "a\u202e.json" },
		"absolute relative": func(r *formatRequest) { r.RelativePath = "/etc/passwd" },
		"escaping relative": func(r *formatRequest) { r.RelativePath = "../secret.json" },
		"unclean relative":  func(r *formatRequest) { r.RelativePath = "src/../a.json" },
		"drive relative":    func(r *formatRequest) { r.RelativePath = "C:/a.json" },
		"invalid utf-8":     func(r *formatRequest) { r.Text = "\xff" },
		"tab size":          func(r *formatRequest) { r.Options.TabSize = 0 },
		"eol":               func(r *formatRequest) { r.Options.EOL = "\r" },
		"range order":       func(r *formatRequest) { r.Range = &formatRange{3, 1, 2, 1} },
		"range columns":     func(r *formatRequest) { r.Range = &formatRange{1, 5, 1, 2} },
	}
	for name, mutate := range cases {
		request := validFormatRequest()
		mutate(&request)
		if err := normalizeFormatRequest(&request); contributionErrorCode(err) != contributionErrInvalidRequest {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestBuildFormatPayloadGatesRelativePath(t *testing.T) {
	request := validFormatRequest()
	_ = normalizeFormatRequest(&request)
	request.Text = "<a>&</a>"
	formatter := FormatterContribution{ID: "fmt"}
	without, _ := buildFormatPayload(formatter, request, false)
	if strings.Contains(string(without), "relativePath") || strings.Contains(string(without), "/Users/") {
		t.Fatalf("payload without workspace.read = %s", without)
	}
	with, _ := buildFormatPayload(formatter, request, true)
	if !strings.Contains(string(with), `"relativePath":"src/a.json"`) || !strings.Contains(string(with), `"text":"<a>&</a>"`) || !strings.Contains(string(with), `"formatter":"fmt"`) {
		t.Fatalf("payload with workspace.read = %s", with)
	}
}

func TestParseFormatterResult(t *testing.T) {
	if response, err := parseFormatterResult(json.RawMessage(`{"text":"b"}`), "a"); err != nil || response.Unchanged || response.Text == nil || *response.Text != "b" {
		t.Fatalf("text = %+v, %v", response, err)
	}
	if response, err := parseFormatterResult(json.RawMessage(`{"unchanged":true}`), "a"); err != nil || !response.Unchanged || response.Text != nil {
		t.Fatalf("unchanged = %+v, %v", response, err)
	}
	if response, err := parseFormatterResult(json.RawMessage(`{"text":"a"}`), "a"); err != nil || !response.Unchanged {
		t.Fatalf("same text = %+v, %v", response, err)
	}
	huge := `{"text":"` + strings.Repeat("x", 64<<10+4*10+1) + `"}`
	cases := map[string]string{
		"ok field":        `{"ok":true}`,
		"empty":           `{}`,
		"both":            `{"text":"b","unchanged":true}`,
		"text not string": `{"text":1}`,
		"nul":             `{"text":"a\u0000b"}`,
		"invalid utf-8":   "{\"text\":\"\xff\"}",
		"growth":          huge,
	}
	for name, raw := range cases {
		if _, err := parseFormatterResult(json.RawMessage(raw), strings.Repeat("y", 10)); contributionErrorCode(err) != contributionErrInvalidResult {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	big := strings.Repeat("y", formatterResultMaxBytes)
	if _, err := parseFormatterResult(json.RawMessage(`{"text":"`+big+`z"}`), big); contributionErrorCode(err) != contributionErrInvalidResult {
		t.Fatalf("result limit err = %v", err)
	}
}

func formatterInvokeManager(t *testing.T, formatter FormatterContribution) (*pluginManager, *pluginRecord) {
	t.Helper()
	manager, record := contributionInvokeManager(t)
	record.mu.Lock()
	record.manifest.contributions = map[string]*resolvedContribution{formatterPointKey: {Entries: []any{formatter}}}
	record.mu.Unlock()
	return manager, record
}

func TestPluginFormatGating(t *testing.T) {
	manager, record := formatterInvokeManager(t, FormatterContribution{ID: "fmt", Title: "Fmt", Languages: []string{"json"}, Action: "run"})
	// The helper process answers {"ok":true}: the call was dispatched and the
	// result was then rejected by result validation.
	if _, err := manager.format(context.Background(), validFormatRequest()); contributionErrorCode(err) != contributionErrInvalidResult {
		t.Fatalf("dispatch err = %v", err)
	}
	assertNoContributionRequestsLeft(t)
	cases := map[string]struct {
		mutate func(*formatRequest)
		code   string
	}{
		"unknown plugin":    {func(r *formatRequest) { r.PluginID = "missing.plugin" }, contributionErrNotFound},
		"unknown formatter": {func(r *formatRequest) { r.FormatterID = "other" }, contributionErrNotFound},
		"other language":    {func(r *formatRequest) { r.LanguageID = "yaml" }, contributionErrUnavailable},
		"range unsupported": {func(r *formatRequest) { r.Range = &formatRange{1, 1, 1, 2} }, contributionErrInvalidRequest},
		// The test action allows 1024 payload bytes.
		"payload too large": {func(r *formatRequest) { r.Text = strings.Repeat("x", 1100) }, contributionErrTooLarge},
		"bad request id":    {func(r *formatRequest) { r.RequestID = "short" }, contributionErrInvalidRequest},
	}
	for name, test := range cases {
		request := validFormatRequest()
		test.mutate(&request)
		if _, err := manager.format(context.Background(), request); contributionErrorCode(err) != test.code {
			t.Errorf("%s: err = %v, want %s", name, err, test.code)
		}
	}
	slot := record.id + "\x00" + formatterPointKey
	contributionRequests.mu.Lock()
	contributionRequests.slots[slot] = formatterConcurrency
	contributionRequests.mu.Unlock()
	_, err := manager.format(context.Background(), validFormatRequest())
	contributionRequests.mu.Lock()
	delete(contributionRequests.slots, slot)
	contributionRequests.mu.Unlock()
	if contributionErrorCode(err) != contributionErrBusy {
		t.Fatalf("busy err = %v", err)
	}
	// A disabled plugin's formatter is unavailable.
	if err := manager.SetEnabled(context.Background(), record.id, false); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.format(context.Background(), validFormatRequest()); err == nil {
		t.Fatal("a disabled plugin must not format")
	}
	assertNoContributionRequestsLeft(t)
}

func TestFormatterForRequestPinsGeneration(t *testing.T) {
	record := &pluginRecord{manifest: &Manifest{contributions: map[string]*resolvedContribution{
		formatterPointKey: {Entries: []any{FormatterContribution{ID: "fmt", Languages: []string{"json"}, Action: "run", Range: true}}},
	}}}
	request := validFormatRequest()
	if formatter, err := formatterForRequestLocked(record, request); err != nil || formatter.Action != "run" {
		t.Fatalf("lookup = %+v, %v", formatter, err)
	}
	// A reload that drops the formatter makes a pending call unavailable.
	record.manifest = &Manifest{contributions: map[string]*resolvedContribution{}}
	if _, err := formatterForRequestLocked(record, request); contributionErrorCode(err) != contributionErrNotFound {
		t.Fatalf("dropped err = %v", err)
	}
}

func TestPluginHostFormatHandlerRejectsBadArguments(t *testing.T) {
	for _, args := range []string{`nope`, `{"pluginId":"a","extra":1}`} {
		if _, err := pluginHostFormatHandler("", args); contributionErrorCode(err) != contributionErrInvalidRequest {
			t.Errorf("%s: err = %v", args, err)
		}
	}
}
