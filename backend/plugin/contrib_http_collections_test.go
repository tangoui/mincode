package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func writeHttpCollectionPackage(t *testing.T, entries []any, files map[string]string) string {
	t.Helper()
	return writeKeymapPackage(t, "", func(manifest map[string]any) {
		manifest["id"] = "http-collection-test"
		manifest["contributes"] = map[string]any{"httpCollections": entries}
	}, nil, files)
}

func httpCollectionDocument(t *testing.T, mutate func(map[string]any)) string {
	t.Helper()
	document := map[string]any{
		"format": "mincode.http-collection", "version": 1, "name": "Contract smoke checks",
		"requests": []any{map[string]any{
			"name": "OpenAPI document", "method": "get", "url": "{{baseUrl}}/openapi.json",
			"headers":       []any{map[string]any{"key": "Accept", "value": "application/json"}, map[string]any{"key": "Authorization", "value": "{{ token }}"}},
			"params":        []any{map[string]any{"key": "page", "value": "1", "enabled": false, "description": "Page"}},
			"documentation": "Uses Basic authentication.\r\nSee docs.",
		}},
		"children": []any{map[string]any{"name": "Health", "requests": []any{
			map[string]any{"name": "Ping", "method": "POST", "url": "https://api.example.com/ping?x=1", "bodyType": "json", "body": "{\"ok\":true}"},
		}, "children": []any{map[string]any{"name": "Deep", "requests": []any{}}}}},
		"environments": []any{map[string]any{"name": "Local", "vars": []any{
			map[string]any{"key": "baseUrl", "value": "http://localhost:8080"},
			map[string]any{"key": "token", "value": "", "description": "Paste your own token"},
		}}},
	}
	if mutate != nil {
		mutate(document)
	}
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func httpCollectionEntry(id, path string) map[string]any {
	return map[string]any{"id": id, "title": "Title " + id, "path": path}
}

func firstHttpRequest(document map[string]any) map[string]any {
	return document["requests"].([]any)[0].(map[string]any)
}

func TestHttpCollectionsDeclarativePackageAndSnapshot(t *testing.T) {
	useTemporaryPluginData(t)
	entry := httpCollectionEntry("smoke", "http/smoke.json")
	entry["titleLocalized"] = map[string]string{"zh": "冒烟"}
	entry["description"] = "Smoke checks"
	root := writeHttpCollectionPackage(t, []any{entry}, map[string]string{"http/smoke.json": "\ufeff" + httpCollectionDocument(t, nil)})
	inspection, err := inspectInstallDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	items := inspection.Manifest.httpCollections()
	if len(items) != 1 {
		t.Fatalf("items = %+v", items)
	}
	item := items[0]
	if item.Counts != (HttpCollectionCounts{Requests: 2, Folders: 2, Environments: 1}) {
		t.Fatalf("counts = %+v", item.Counts)
	}
	request := item.Collection.Requests[0]
	if request.Method != "GET" || request.BodyType != "none" || request.Documentation != "Uses Basic authentication.\nSee docs." ||
		request.Headers[1].Value != "{{ token }}" || !request.Headers[0].Enabled || request.Params[0].Enabled {
		t.Fatalf("request = %+v", request)
	}
	if item.Collection.Children[0].Requests[0].Body != `{"ok":true}` || item.Collection.Environments[0].Vars[1].Value != "" {
		t.Fatalf("collection = %+v", item.Collection)
	}

	manager := newPluginManager()
	manifest, err := manager.InstallLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetEnabled(context.Background(), manifest.ID, true); err != nil {
		t.Fatal(err)
	}
	snapshot := manager.Contributions([]string{httpCollectionPointKey})
	if len(snapshot.Plugins) != 1 || len(snapshot.Plugins[0].Points[httpCollectionPointKey].Entries) != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	encoded, _ := json.Marshal(snapshot.Plugins[0].Points[httpCollectionPointKey].Entries[0])
	for _, want := range []string{`"collection":{"name":"Contract smoke checks"`, `"counts":{"requests":2,"folders":2,"environments":1}`, `"titleLocalized":{"zh":"冒烟"}`} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("snapshot entry %s lacks %s", encoded, want)
		}
	}
	for _, forbidden := range []string{"auth", "path"} {
		if strings.Contains(string(encoded), `"`+forbidden+`"`) {
			t.Fatalf("snapshot entry exposes %q: %s", forbidden, encoded)
		}
	}
}

func TestHttpCollectionsFileValidation(t *testing.T) {
	cases := map[string]struct {
		mutate func(map[string]any)
		want   string
	}{
		"wrong format":  {func(d map[string]any) { d["format"] = "postman" }, "format"},
		"wrong version": {func(d map[string]any) { d["version"] = 2 }, "version"},
		"auth field":    {func(d map[string]any) { firstHttpRequest(d)["authType"] = "bearer" }, "unknown field"},
		"top auth":      {func(d map[string]any) { d["auth"] = map[string]any{} }, "unknown field"},
		"form data":     {func(d map[string]any) { firstHttpRequest(d)["formData"] = []any{} }, "unknown field"},
		"bad method":    {func(d map[string]any) { firstHttpRequest(d)["method"] = "CONNECT" }, "method"},
		"bad body type": {func(d map[string]any) { firstHttpRequest(d)["bodyType"] = "binary" }, "bodyType"},
		"relative url":  {func(d map[string]any) { firstHttpRequest(d)["url"] = "/api" }, "absolute"},
		"file url":      {func(d map[string]any) { firstHttpRequest(d)["url"] = "file:///etc/passwd" }, "absolute"},
		"userinfo":      {func(d map[string]any) { firstHttpRequest(d)["url"] = "https://user:pw@example.com/" }, "user information"},
		"var userinfo":  {func(d map[string]any) { firstHttpRequest(d)["url"] = "{{scheme}}user@example.com/" }, "user information"},
		"no host":       {func(d map[string]any) { firstHttpRequest(d)["url"] = "https:///x" }, "host"},
		"url space":     {func(d map[string]any) { firstHttpRequest(d)["url"] = "https://a.example/ x" }, "whitespace"},
		"missing name":  {func(d map[string]any) { delete(firstHttpRequest(d), "name") }, "name is required"},
		"name bidi":     {func(d map[string]any) { d["name"] = "a‮b" }, "U+202E"},
		"authorization": {func(d map[string]any) {
			firstHttpRequest(d)["headers"] = []any{map[string]any{"key": "authorization", "value": "abc"}}
		}, "sensitive"},
		"cookie": {func(d map[string]any) {
			firstHttpRequest(d)["headers"] = []any{map[string]any{"key": "Cookie", "value": "a={{x}}"}}
		}, "sensitive"},
		"api key param": {func(d map[string]any) {
			firstHttpRequest(d)["params"] = []any{map[string]any{"key": "api_key", "value": "hunter2"}}
		}, "sensitive"},
		"two vars": {func(d map[string]any) {
			firstHttpRequest(d)["headers"] = []any{map[string]any{"key": "X-Api-Key", "value": "{{a}}{{b}}"}}
		}, "sensitive"},
		"env password": {func(d map[string]any) {
			d["environments"] = []any{map[string]any{"name": "E", "vars": []any{map[string]any{"key": "dbPassword", "value": "p"}}}}
		}, "sensitive"},
		"jwt in env": {func(d map[string]any) {
			d["environments"] = []any{map[string]any{"name": "E", "vars": []any{map[string]any{"key": "sample", "value": "eyJhbGciOi.eyJzdWIiOi.sig"}}}}
		}, "credential"},
		"aws key": {func(d map[string]any) { firstHttpRequest(d)["body"] = "AKIAABCDEFGHIJKLMNOP" }, "credential"},
		"github token": {func(d map[string]any) {
			firstHttpRequest(d)["url"] = "https://x.example/?t=ghp_" + strings.Repeat("a", 36)
		}, "credential"},
		"private key": {func(d map[string]any) { firstHttpRequest(d)["documentation"] = "-----BEGIN RSA PRIVATE KEY-----" }, "credential"},
		"bearer value": {func(d map[string]any) {
			firstHttpRequest(d)["params"] = []any{map[string]any{"key": "h", "value": "Bearer abcdefghijklmnopqrstuv"}}
		}, "credential"},
		"sk key":        {func(d map[string]any) { firstHttpRequest(d)["graphqlVariables"] = "sk-" + strings.Repeat("a", 24) }, "credential"},
		"duplicate key": {nil, "duplicate key"},
		"too deep":      {func(d map[string]any) { d["children"] = nestedHttpFolders(httpCollectionDepthMax + 1) }, "levels deep"},
		"too many rows": {func(d map[string]any) { firstHttpRequest(d)["params"] = httpRows(httpCollectionRowsMax + 1) }, "rows"},
		"too many envs": {func(d map[string]any) { d["environments"] = httpEnvironments(httpCollectionEnvironmentsMax + 1) }, "environments"},
		"large body": {func(d map[string]any) {
			firstHttpRequest(d)["body"] = strings.Repeat("a", httpCollectionBodyMaxBytes+1)
		}, "bytes"},
		"control header": {func(d map[string]any) {
			firstHttpRequest(d)["headers"] = []any{map[string]any{"key": "X", "value": "a\nb"}}
		}, "U+000A"},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			document := httpCollectionDocument(t, testCase.mutate)
			if testCase.mutate == nil {
				document = `{"format":"mincode.http-collection","version":1,"name":"a","name":"b"}`
			}
			manifest, err := readManifest(writeHttpCollectionPackage(t, []any{httpCollectionEntry("c", "http/c.json")}, map[string]string{"http/c.json": document}))
			if err != nil {
				t.Fatal(err)
			}
			resolved := manifest.contribution(httpCollectionPointKey)
			if len(resolved.Entries) != 0 || len(resolved.Issues) != 1 || !strings.Contains(resolved.Issues[0].Message, testCase.want) {
				t.Fatalf("entries = %d issues = %+v, want %q", len(resolved.Entries), resolved.Issues, testCase.want)
			}
		})
	}
}

func nestedHttpFolders(depth int) []any {
	if depth == 0 {
		return []any{}
	}
	return []any{map[string]any{"name": fmt.Sprintf("Level %d", depth), "children": nestedHttpFolders(depth - 1)}}
}

func httpRows(count int) []any {
	rows := make([]any, count)
	for index := range rows {
		rows[index] = map[string]any{"key": fmt.Sprintf("k%d", index), "value": "v"}
	}
	return rows
}

func httpEnvironments(count int) []any {
	environments := make([]any, count)
	for index := range environments {
		environments[index] = map[string]any{"name": fmt.Sprintf("E%d", index), "vars": []any{}}
	}
	return environments
}

func TestHttpCollectionsEntryValidationAndStrictInstall(t *testing.T) {
	files := map[string]string{"http/c.json": httpCollectionDocument(t, nil), "http/c.txt": "{}"}
	cases := map[string]struct {
		entry map[string]any
		want  string
	}{
		"bad id":        {httpCollectionEntry("Bad_ID", "http/c.json"), "must match"},
		"no path":       {map[string]any{"id": "a", "title": "T"}, "path"},
		"inline":        {map[string]any{"id": "a", "title": "T", "path": "http/c.json", "collection": map[string]any{}}, "unknown field"},
		"wrong ext":     {httpCollectionEntry("a", "http/c.txt"), ".json"},
		"escape":        {httpCollectionEntry("a", "../c.json"), "c.json"},
		"blank title":   {map[string]any{"id": "a", "title": " ", "path": "http/c.json"}, "title"},
		"title newline": {map[string]any{"id": "a", "title": "a\nb", "path": "http/c.json"}, "title"},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			manifest, err := readManifest(writeHttpCollectionPackage(t, []any{testCase.entry}, files))
			if err != nil {
				t.Fatal(err)
			}
			resolved := manifest.contribution(httpCollectionPointKey)
			if len(resolved.Entries) != 0 || len(resolved.Issues) != 1 || !strings.Contains(resolved.Issues[0].Message, testCase.want) {
				t.Fatalf("issues = %+v, want %q", resolved.Issues, testCase.want)
			}
		})
	}
	root := writeHttpCollectionPackage(t, []any{httpCollectionEntry("a", "http/c.json"), httpCollectionEntry("a", "http/c.json")}, files)
	if _, err := inspectInstallDirectory(root); err == nil || !strings.Contains(err.Error(), "invalid httpCollections contribution") {
		t.Fatalf("strict err = %v", err)
	}
}

func TestHttpCollectionsPerPluginRequestTotal(t *testing.T) {
	requests := make([]any, httpCollectionRequestsMax)
	for index := range requests {
		requests[index] = map[string]any{"name": fmt.Sprintf("R%d", index), "method": "GET", "url": "{{base}}/r"}
	}
	document := httpCollectionDocument(t, func(d map[string]any) {
		d["requests"] = requests
		d["children"] = []any{}
	})
	count := httpCollectionPluginRequests/httpCollectionRequestsMax + 1
	entries := make([]any, count)
	files := map[string]string{}
	for index := range entries {
		path := fmt.Sprintf("http/c%d.json", index)
		files[path] = document
		entries[index] = httpCollectionEntry(fmt.Sprintf("c%d", index), path)
	}
	manifest, err := readManifest(writeHttpCollectionPackage(t, entries, files))
	if err != nil {
		t.Fatal(err)
	}
	resolved := manifest.contribution(httpCollectionPointKey)
	if len(resolved.Entries) != count-1 || len(resolved.Issues) != 1 || !strings.Contains(resolved.Issues[0].Message, "requests in total") {
		t.Fatalf("entries = %d issues = %+v", len(resolved.Entries), resolved.Issues)
	}
	// One more request than a collection allows is rejected per collection.
	tooMany := httpCollectionDocument(t, func(d map[string]any) {
		d["requests"] = append(requests, map[string]any{"name": "extra", "method": "GET", "url": "{{base}}"})
	})
	manifest, err = readManifest(writeHttpCollectionPackage(t, []any{httpCollectionEntry("c", "http/c.json")}, map[string]string{"http/c.json": tooMany}))
	if err != nil {
		t.Fatal(err)
	}
	if resolved := manifest.contribution(httpCollectionPointKey); len(resolved.Issues) != 1 || !strings.Contains(resolved.Issues[0].Message, "at most") {
		t.Fatalf("issues = %+v", resolved.Issues)
	}
}
