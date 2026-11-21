package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mincode/app/plugins/api"
)

func gateJSON(t *testing.T, surface string, context any, capabilities ...api.Capability) map[string]any {
	t.Helper()
	raw, err := json.Marshal(context)
	if err != nil {
		t.Fatal(err)
	}
	gated, err := gateSurfaceContext(surface, raw, capabilities)
	if err != nil {
		t.Fatalf("%s: %v", surface, err)
	}
	if gated == nil {
		return nil
	}
	if len(gated) > serviceSurfaceContextMaxBytes {
		t.Fatalf("%s: gated context is %d bytes", surface, len(gated))
	}
	var decoded map[string]any
	if err := json.Unmarshal(gated, &decoded); err != nil {
		t.Fatalf("%s: %s: %v", surface, gated, err)
	}
	return decoded
}

func sftpSurfaceContextFixture() map[string]any {
	return map[string]any{"remote": map[string]any{
		"connection": map[string]any{"id": "c1", "name": "prod", "protocol": "sftp", "host": "files.example.com", "port": 22, "username": "deploy"},
		"path":       "/var/log/app.log",
	}}
}

func dbResultSurfaceContextFixture() map[string]any {
	return map[string]any{"database": map[string]any{
		"connection": map[string]any{"id": "d1", "name": "orders", "kind": "postgresql"},
		"database":   "shop", "table": map[string]any{"name": "orders", "kind": "table"},
	}}
}

func httpSurfaceContextFixture(headers [][]string) map[string]any {
	if headers == nil {
		headers = [][]string{{"Content-Type", "application/json"}}
	}
	return map[string]any{
		"response": map[string]any{"status": 200, "statusText": "OK", "contentType": "application/json", "elapsedMs": 12.5, "headers": headers, "headersRedacted": 0},
		"request":  map[string]any{"method": "GET", "url": "https://api.example.com/v1/orders/{redacted}", "redacted": true},
	}
}

func TestSurfaceContextGatedByProviderCapability(t *testing.T) {
	// sftpFile: remote needs remote.context.
	if got := gateJSON(t, serviceSurfaceSFTPFile, sftpSurfaceContextFixture(), api.CapabilityRemoteContext); got["remote"] == nil {
		t.Fatalf("with remote.context: %v", got)
	}
	for _, capabilities := range [][]api.Capability{nil, {api.CapabilityDatabaseSchema, api.CapabilityWorkspaceRead}} {
		if got := gateJSON(t, serviceSurfaceSFTPFile, sftpSurfaceContextFixture(), capabilities...); got != nil {
			t.Fatalf("without remote.context (%v): %v", capabilities, got)
		}
		if got := gateJSON(t, serviceSurfaceSFTPFile, map[string]any{"remoteOmitted": true}, capabilities...); got != nil {
			t.Fatalf("remoteOmitted without remote.context: %v", got)
		}
	}
	if got := gateJSON(t, serviceSurfaceSFTPFile, map[string]any{"remoteOmitted": true}, api.CapabilityRemoteContext); got["remoteOmitted"] != true || got["remote"] != nil {
		t.Fatalf("remoteOmitted: %v", got)
	}

	// dbResult: database needs database.schema (remote.context is not it).
	got := gateJSON(t, serviceSurfaceDBResult, dbResultSurfaceContextFixture(), api.CapabilityDatabaseSchema)
	database, _ := got["database"].(map[string]any)
	if database == nil || database["database"] != "shop" || database["table"].(map[string]any)["name"] != "orders" {
		t.Fatalf("with database.schema: %v", got)
	}
	if got := gateJSON(t, serviceSurfaceDBResult, dbResultSurfaceContextFixture(), api.CapabilityRemoteContext); got != nil {
		t.Fatalf("without database.schema: %v", got)
	}

	// httpResponse: response always, request needs remote.context.
	got = gateJSON(t, serviceSurfaceHTTPResponse, httpSurfaceContextFixture(nil), api.CapabilityRemoteContext)
	if got["response"] == nil || got["request"] == nil || got["request"].(map[string]any)["redacted"] != true {
		t.Fatalf("with remote.context: %v", got)
	}
	got = gateJSON(t, serviceSurfaceHTTPResponse, httpSurfaceContextFixture(nil), api.CapabilityDatabaseSchema)
	if _, present := got["request"]; present || got["response"] == nil {
		t.Fatalf("without remote.context: %v", got)
	}
	if got["response"].(map[string]any)["status"] != float64(200) {
		t.Fatalf("response: %v", got)
	}

	// terminalSelection: terminal.kind always.
	for _, kind := range []string{"local", "ssh", "docker"} {
		if got := gateJSON(t, serviceSurfaceTerminalSelection, map[string]any{"terminal": map[string]any{"kind": kind}}); got["terminal"].(map[string]any)["kind"] != kind {
			t.Fatalf("terminal %s: %v", kind, got)
		}
	}
	// No context at all is fine, except on httpResponse (response is
	// always there).
	for _, surface := range []string{serviceSurfaceSFTPFile, serviceSurfaceDBResult, serviceSurfaceTerminalSelection} {
		if gated, err := gateSurfaceContext(surface, nil, nil); err != nil || gated != nil {
			t.Fatalf("%s without context: %s %v", surface, gated, err)
		}
	}
	for _, raw := range []string{"", "null", `{"request":{"method":"GET","url":"https://a.example/x"}}`} {
		_, err := gateSurfaceContext(serviceSurfaceHTTPResponse, json.RawMessage(raw), []api.Capability{api.CapabilityRemoteContext})
		wantCommandCode(t, err, contributionErrInvalidRequest)
	}
}

func httpRequestContextJSON(url string) string {
	encoded, _ := json.Marshal(url)
	return `{"response":{"status":200,"statusText":"OK","contentType":"","elapsedMs":1,"headers":[],"headersRedacted":0},"request":{"method":"GET","url":` + string(encoded) + `}}`
}

func TestSurfaceContextRequestURLRefusesUserinfo(t *testing.T) {
	for _, url := range []string{
		"https://u:p@h/x",
		"https://u@h/x",
		"https://u:p@api.example.com/v1",
		"https://u%40corp:p@h.example/x",
		"https://u%40h.example/x",
		"HTTPS://U%40H.example/x",
		"//u@h.example/x",
		"u:p@h.example/x",
		"https://h.example@evil.example/x",
		"ws://u:p@h.example/x",
		// WHATWG reads any mix of / and \ (also none) after a special
		// scheme as the authority start.
		"https:/u:p@h/x",
		"HTTPS:/U:P@H/x",
		"https:///u:p@h/x",
		`https:\\u:p@h/x`,
		`https:\u:p@h/x`,
		`https:/\u:p@h/x`,
		`http:\\u:p@h`,
		"ws:/u:p@h",
		"https:u:p@h/x",
		"FTP:/u@h/x",
		`\\u:p@h/x`,
		// Surrounding whitespace (WHATWG trims it, url.Parse does not).
		" https://u:p@h/x",
		" https:/u:p@h/x",
		"https://u:p@h/x ",
		"https://api.example.com/v1 ",
		" https://api.example.com/v1",
		"\thttps://u:p@h/x",
		" https://u:p@h/x",
		" https://api.example.com/v1",
		"https://api.example.com/v1　",
	} {
		for _, capabilities := range [][]api.Capability{nil, {api.CapabilityRemoteContext}} {
			_, err := gateSurfaceContext(serviceSurfaceHTTPResponse, json.RawMessage(httpRequestContextJSON(url)), capabilities)
			if err == nil || !strings.HasPrefix(err.Error(), contributionErrInvalidRequest+":") {
				t.Fatalf("%q (%v): err = %v, want invalid_request", url, capabilities, err)
			}
		}
	}
	// Sanitized addresses pass, also with "@" in the path and templates.
	for _, url := range []string{
		"https://api.example.com/users/@me",
		"{{baseUrl}}/users/@me",
		"https://{{host}}/v1/orders",
		"https://{redacted}/x",
		"/v1/users/@me",
		"api.example.com/users/{redacted}",
		"https://[::1]:8443/x",
		"https://[::1]/x",
		"https://h/x@",
		`https://h\@evil/x`,
		"https:/{{host}}/x",
		"{{scheme}}://{{host}}/users/@me",
	} {
		got := gateJSON(t, serviceSurfaceHTTPResponse, json.RawMessage(httpRequestContextJSON(url)), api.CapabilityRemoteContext)
		if got["request"].(map[string]any)["url"] != url {
			t.Fatalf("%q: %v", url, got)
		}
	}
}

func TestSurfaceContextURLHeadersLoseQueryAndFragment(t *testing.T) {
	headers := [][]string{
		{"Location", "https://app.example/callback?code=oauth-code-123&state=s#frag"},
		{"Content-Location", "/files/report.pdf?X-Amz-Signature=sig-456"},
		{"Link", `<https://api.example/items?page=2&sig=sig-789>; rel="next", <https://api.example/items?page=1>; rel="prev"`},
		{"Refresh", "5; url=https://app.example/done?ticket=tkt-000"},
		{"Origin", "https://app.example"},
		{"Referer", "https://app.example/page#access_token=at-111"},
		{"Location", "http://[::1:bad/x?y"},
		{"Link", `<https://u:p@api.example/x>; rel="self"`},
		{"Link", `<https://api.example/x>; rel="self"; anchor="https://a.example/?k=v"`},
		{"Refresh", "0"},
		{"Location", "https:/u:p@h/x"},
		{"Location", `https:\\u:p@h/x`},
		{"Content-Location", "HTTPS:/U:P@H/x"},
		{"Refresh", "0?tok=SECRET-REFRESH; url=https://x.example/"},
		{"Refresh", "0x; url=https://x.example/"},
		{"Refresh", "5?tok=SECRET-REFRESH"},
		{"Refresh", "1.5, URL='https://x.example/a?b=SECRET-REFRESH'"},
		{"Content-Type", "application/json"},
	}
	got := gateJSON(t, serviceSurfaceHTTPResponse, httpSurfaceContextFixture(headers))
	response := got["response"].(map[string]any)
	kept := map[string][]string{}
	for _, header := range response["headers"].([]any) {
		pair := header.([]any)
		kept[pair[0].(string)] = append(kept[pair[0].(string)], pair[1].(string))
	}
	want := map[string][]string{
		"Location":         {"https://app.example/callback"},
		"Content-Location": {"/files/report.pdf"},
		"Link":             {`<https://api.example/items>; rel="next", <https://api.example/items>; rel="prev"`},
		"Refresh":          {"5; url=https://app.example/done", "0", "1.5, URL='https://x.example/a'"},
		"Origin":           {"https://app.example"},
		"Referer":          {"https://app.example/page"},
		"Content-Type":     {"application/json"},
	}
	encodedKept, _ := json.Marshal(kept)
	encodedWant, _ := json.Marshal(want)
	if string(encodedKept) != string(encodedWant) {
		t.Fatalf("kept %s\nwant %s", encodedKept, encodedWant)
	}
	// Stripped: Location, Content-Location, Link, Refresh, Referer and the
	// quoted Refresh (6); dropped: the unparsable Location, the Link with
	// userinfo, the Link with a query outside <...>, the two Locations and
	// the Content-Location with slash-variant userinfo, and the three
	// Refresh values with something other than a delay before url= (9).
	if response["headersRedacted"] != float64(15) {
		t.Fatalf("headersRedacted = %v", response["headersRedacted"])
	}
	encoded, _ := json.Marshal(got)
	for _, leak := range []string{"oauth-code-123", "sig-456", "sig-789", "tkt-000", "at-111", "u:p@", "U:P@", "k=v", "SECRET-REFRESH"} {
		if strings.Contains(string(encoded), leak) {
			t.Fatalf("%q leaked: %s", leak, encoded)
		}
	}
}

func TestSurfaceContextBoundsAndFormatCharacters(t *testing.T) {
	response := func(field string) string {
		return `{"response":{"status":200,"statusText":"OK","contentType":"","elapsedMs":1,"headers":[],"headersRedacted":0,` + field + `}}`
	}
	for name, raw := range map[string]string{
		"headersRedacted at MaxInt64":  `{"response":{"status":200,"statusText":"OK","contentType":"","elapsedMs":1,"headers":[],"headersRedacted":9223372036854775807}}`,
		"headersRedacted over bound":   `{"response":{"status":200,"statusText":"OK","contentType":"","elapsedMs":1,"headers":[],"headersRedacted":100001}}`,
		"elapsedMs over a day":         `{"response":{"status":200,"statusText":"OK","contentType":"","elapsedMs":86400001,"headers":[],"headersRedacted":0}}`,
		"bidi override in statusText":  `{"response":{"status":200,"statusText":"O\u202eK","contentType":"","elapsedMs":1,"headers":[],"headersRedacted":0}}`,
		"zero-width in header value":   `{"response":{"status":200,"statusText":"OK","contentType":"","elapsedMs":1,"headers":[["X-A","a\u200bb"]],"headersRedacted":0}}`,
		"bidi override in header name": `{"response":{"status":200,"statusText":"OK","contentType":"","elapsedMs":1,"headers":[["X-\u2066A","b"]],"headersRedacted":0}}`,
		"line separator in statusText": `{"response":{"status":200,"statusText":"O\u2028K","contentType":"","elapsedMs":1,"headers":[],"headersRedacted":0}}`,
		"paragraph separator in value": `{"response":{"status":200,"statusText":"OK","contentType":"","elapsedMs":1,"headers":[["X-A","a\u2029b"]],"headersRedacted":0}}`,
		"headersTruncated not a bool":  response(`"headersTruncated":"yes"`),
	} {
		_, err := gateSurfaceContext(serviceSurfaceHTTPResponse, json.RawMessage(raw), nil)
		if err == nil || !strings.HasPrefix(err.Error(), contributionErrInvalidRequest+":") {
			t.Fatalf("%s: err = %v, want invalid_request", name, err)
		}
	}
	for name, raw := range map[string]string{
		"zero-width in sftp host": `{"remote":{"connection":{"id":"c","name":"n","protocol":"sftp","host":"h\u200b.example","port":22,"username":"u"},"path":"/a"}}`,
		"bidi override in path":   `{"remote":{"connection":{"id":"c","name":"n","protocol":"sftp","host":"h","port":22,"username":"u"},"path":"/a\u202egol.exe"}}`,
	} {
		_, err := gateSurfaceContext(serviceSurfaceSFTPFile, json.RawMessage(raw), nil)
		if err == nil || !strings.HasPrefix(err.Error(), contributionErrInvalidRequest+":") {
			t.Fatalf("%s: err = %v, want invalid_request", name, err)
		}
	}
	// A tab stays allowed in a header value; the bound itself is accepted.
	got := gateJSON(t, serviceSurfaceHTTPResponse, json.RawMessage(`{"response":{"status":200,"statusText":"OK","contentType":"","elapsedMs":86400000,"headers":[["X-A","a\tb"]],"headersRedacted":100000}}`))
	if got["response"].(map[string]any)["headersRedacted"] != float64(100000) {
		t.Fatalf("bound: %v", got)
	}
	// Decode errors name the surface context, not a manifest.
	_, err := gateSurfaceContext(serviceSurfaceTerminalSelection, json.RawMessage(`{"terminal":{"kind":"ssh"}} {}`), nil)
	if err == nil || strings.Contains(err.Error(), "manifest") || !strings.Contains(err.Error(), "surfaceContext") {
		t.Fatalf("trailing data error = %v", err)
	}
}

func TestSurfaceContextHTTPResponseStripsSensitiveHeaders(t *testing.T) {
	headers := [][]string{
		{"Content-Type", "application/json"},
		{"Set-Cookie", "sid=abc"},
		{"set-cookie2", "sid=abc"},
		{"Authorization", "Bearer abc"},
		{"X-Session-Id", "s-1"},
		{"WWW-Authenticate", "Basic realm=x"},
		{"X-Csrf-Token", "t"},
		{"Proxy-Authorization", "Basic x"},
		{"Cookie", "a=b"},
		{"X-Large", strings.Repeat("v", serviceHTTPHeaderValueMaxBytes+1)},
		{"X-Edge", strings.Repeat("v", serviceHTTPHeaderValueMaxBytes)},
		{"ETag", `"1"`},
	}
	context := httpSurfaceContextFixture(headers)
	context["response"].(map[string]any)["headersRedacted"] = 2 // the frontend's own count
	got := gateJSON(t, serviceSurfaceHTTPResponse, context)
	response := got["response"].(map[string]any)
	kept := response["headers"].([]any)
	names := []string{}
	for _, header := range kept {
		names = append(names, header.([]any)[0].(string))
	}
	if strings.Join(names, ",") != "Content-Type,X-Edge,ETag" {
		t.Fatalf("kept %v", names)
	}
	if response["headersRedacted"] != float64(2+9) {
		t.Fatalf("headersRedacted = %v", response["headersRedacted"])
	}
	if _, present := response["headersTruncated"]; present {
		t.Fatalf("not truncated: %v", response)
	}
	encoded, _ := json.Marshal(got)
	for _, leak := range []string{"sid=abc", "Bearer", "s-1", "realm", "Cookie"} {
		if strings.Contains(string(encoded), leak) {
			t.Fatalf("%q leaked: %s", leak, encoded)
		}
	}

	// More than 32 headers: the first 32 stay, headersTruncated is set.
	many := make([][]string, 0, 40)
	for index := range 40 {
		many = append(many, []string{fmt.Sprintf("X-H%02d", index), "v"})
	}
	got = gateJSON(t, serviceSurfaceHTTPResponse, httpSurfaceContextFixture(many))
	response = got["response"].(map[string]any)
	if len(response["headers"].([]any)) != serviceHTTPResponseHeadersMax || response["headersTruncated"] != true || response["headersRedacted"] != float64(0) {
		t.Fatalf("40 headers: %v", response)
	}
	if first := response["headers"].([]any)[0].([]any)[0]; first != "X-H00" {
		t.Fatalf("first header %v", first)
	}

	// Headers that pass filtering but would overflow the context are dropped
	// from the end and headersTruncated is set.
	value := httpResponseSurfaceContext{Response: &httpResponseContext{Status: 200, Headers: [][]string{}}}
	for index := range 10 {
		value.Response.Headers = append(value.Response.Headers, []string{fmt.Sprintf("X-L%d", index), strings.Repeat("v", 500)})
	}
	encoded, err := encodeHTTPResponseContext(serviceSurfaceHTTPResponse, value)
	if err != nil || len(encoded) > serviceSurfaceContextMaxBytes || !strings.Contains(string(encoded), `"headersTruncated":true`) {
		t.Fatalf("overflow: %d bytes %v", len(encoded), err)
	}
}

func TestSurfaceContextRejectsUnknownFieldsAndOversize(t *testing.T) {
	bad := map[string]struct {
		surface string
		context string
	}{
		"unknown top-level":       {serviceSurfaceSFTPFile, `{"remote":null,"extra":1}`},
		"unknown nested":          {serviceSurfaceSFTPFile, `{"remote":{"connection":{"id":"c","name":"n","protocol":"sftp","host":"h","port":22,"username":"u","password":"p"},"path":"/a"}}`},
		"credentials in database": {serviceSurfaceDBResult, `{"database":{"connection":{"id":"d","name":"n","kind":"mysql","host":"db"}}}`},
		"sql text":                {serviceSurfaceDBResult, `{"database":{"connection":{"id":"d","name":"n","kind":"mysql"}},"sql":"select 1"}`},
		"request headers":         {serviceSurfaceHTTPResponse, `{"request":{"method":"GET","url":"https://a.example","headers":[]}}`},
		"request with query":      {serviceSurfaceHTTPResponse, `{"request":{"method":"GET","url":"https://a.example/x?token=1"}}`},
		"request with fragment":   {serviceSurfaceHTTPResponse, `{"request":{"method":"GET","url":"https://a.example/x#t"}}`},
		"header triple":           {serviceSurfaceHTTPResponse, `{"response":{"status":200,"statusText":"","contentType":"","elapsedMs":1,"headers":[["a","b","c"]],"headersRedacted":0}}`},
		"header single":           {serviceSurfaceHTTPResponse, `{"response":{"status":200,"statusText":"","contentType":"","elapsedMs":1,"headers":[["a"]],"headersRedacted":0}}`},
		"terminal host":           {serviceSurfaceTerminalSelection, `{"terminal":{"kind":"ssh","host":"h"}}`},
		"terminal kind":           {serviceSurfaceTerminalSelection, `{"terminal":{"kind":"serial"}}`},
		"not an object":           {serviceSurfaceTerminalSelection, `["terminal"]`},
		"trailing data":           {serviceSurfaceTerminalSelection, `{"terminal":{"kind":"ssh"}} {}`},
		"remote and omitted":      {serviceSurfaceSFTPFile, `{"remote":{"connection":{"id":"c","name":"n","protocol":"sftp","host":"h","port":22,"username":"u"},"path":"/a"},"remoteOmitted":true}`},
		"relative remote path":    {serviceSurfaceSFTPFile, `{"remote":{"connection":{"id":"c","name":"n","protocol":"sftp","host":"h","port":22,"username":"u"},"path":"a"}}`},
	}
	bad["oversize"] = struct {
		surface string
		context string
	}{serviceSurfaceHTTPResponse, `{"response":{"status":200,"statusText":"` + strings.Repeat("x", serviceSurfaceContextMaxBytes) + `","contentType":"","elapsedMs":1,"headers":[],"headersRedacted":0}}`}
	longPath := "/" + strings.Repeat("p", serviceRemoteContextPathMaxBytes)
	bad["remote path over 2048"] = struct {
		surface string
		context string
	}{serviceSurfaceSFTPFile, `{"remote":{"connection":{"id":"c","name":"n","protocol":"sftp","host":"h","port":22,"username":"u"},"path":"` + longPath + `"}}`}
	for name, item := range bad {
		// Rejected whatever the provider's capabilities: decoding comes first.
		for _, capabilities := range [][]api.Capability{nil, {api.CapabilityRemoteContext, api.CapabilityDatabaseSchema}} {
			_, err := gateSurfaceContext(item.surface, json.RawMessage(item.context), capabilities)
			if err == nil || !strings.HasPrefix(err.Error(), contributionErrInvalidRequest+":") {
				t.Fatalf("%s (%v): err = %v, want invalid_request", name, capabilities, err)
			}
		}
	}
	// The older surfaces take no context at all.
	for _, surface := range []string{serviceSurfaceFileMenu, serviceSurfaceSelection, serviceSurfacePalette} {
		_, err := gateSurfaceContext(surface, json.RawMessage(`{}`), nil)
		wantCommandCode(t, err, contributionErrInvalidRequest)
	}
}

func TestHTTPResponseFileName(t *testing.T) {
	for contentType, want := range map[string]string{
		"":                 "response.txt",
		"application/json": "response.json",
		"application/vnd.api+json; charset=utf-8": "response.json",
		"Application/JSON; charset=UTF-8":         "response.json",
		"text/json":                               "response.json",
		"application/x-ndjson":                    "response.ndjson",
		"application/xml":                         "response.xml",
		"text/xml":                                "response.xml",
		"application/atom+xml":                    "response.xml",
		"text/html; charset=utf-8":                "response.html",
		"application/javascript":                  "response.js",
		"application/ecmascript":                  "response.js",
		"text/javascript":                         "response.js",
		"text/csv":                                "response.csv",
		"application/csv":                         "response.csv",
		"application/yaml":                        "response.yaml",
		"application/x-yaml":                      "response.yaml",
		"text/markdown":                           "response.md",
		"text/plain":                              "response.txt",
		"application/toml":                        "response.txt",
		"application/sql":                         "response.txt",
		"application/graphql":                     "response.txt",
		"application/x-www-form-urlencoded":       "response.txt",
	} {
		if got, ok := httpResponseFileName(contentType); !ok || got != want {
			t.Fatalf("%q: %q %v, want %q", contentType, got, ok, want)
		}
	}
	for _, contentType := range []string{"image/png", "application/octet-stream", "application/pdf", "video/mp4", "garbage", "application/json\x00", "multipart/form-data; boundary=x"} {
		if got, ok := httpResponseFileName(contentType); ok {
			t.Fatalf("%q: %q accepted", contentType, got)
		}
	}
}

// sharedHeaderText decodes a header text of testdata/service-response-headers.json:
// a string, or {"repeat": s, "count": n}.
func sharedHeaderText(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var repeat struct {
		Repeat string `json:"repeat"`
		Count  int    `json:"count"`
	}
	if err := json.Unmarshal(raw, &repeat); err != nil {
		t.Fatalf("header text %s: %v", raw, err)
	}
	return strings.Repeat(repeat.Repeat, repeat.Count)
}

// TestSurfaceContextSharedHeaderVectors runs the response header vectors the
// frontend's redactResponseHeaders is tested with, so both filters keep one
// rule set (design ruling 19 / 19a). frontendOnly vectors need the request's
// secrets or the credential-shape patterns, which only the frontend applies.
func TestSurfaceContextSharedHeaderVectors(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "service-response-headers.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Name         string              `json:"name"`
			FrontendOnly bool                `json:"frontendOnly"`
			Headers      [][]json.RawMessage `json:"headers"`
			Kept         [][]json.RawMessage `json:"kept"`
			Redacted     int                 `json:"redacted"`
			Truncated    bool                `json:"truncated"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	pairs := func(raw [][]json.RawMessage) [][]string {
		out := make([][]string, 0, len(raw))
		for _, pair := range raw {
			out = append(out, []string{sharedHeaderText(t, pair[0]), sharedHeaderText(t, pair[1])})
		}
		return out
	}
	ran := 0
	for _, item := range fixture.Cases {
		if item.FrontendOnly {
			continue
		}
		ran++
		response := httpResponseContext{Headers: pairs(item.Headers)}
		response.filterHeaders()
		got, _ := json.Marshal(response.Headers)
		want, _ := json.Marshal(pairs(item.Kept))
		if string(got) != string(want) || response.HeadersRedacted != item.Redacted || response.HeadersTruncated != item.Truncated {
			t.Fatalf("%s: kept %s redacted %d truncated %v\nwant %s redacted %d truncated %v",
				item.Name, got, response.HeadersRedacted, response.HeadersTruncated, want, item.Redacted, item.Truncated)
		}
	}
	if ran == 0 {
		t.Fatal("no shared vectors ran")
	}
}
