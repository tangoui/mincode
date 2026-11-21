package plugin

import (
	"reflect"
	"strings"
	"testing"
)

// writeServicePackage writes a process plugin that declares services. The
// actions are: run (read, default maxPayloadBytes 16384), big (write,
// maxPayloadBytes 262144) and assist (aiCallable).
func writeServicePackage(t *testing.T, services []any, capabilities []string, mutate func(map[string]any)) string {
	t.Helper()
	return writeKeymapPackage(t, "", func(manifest map[string]any) {
		manifest["id"] = "acme-services"
		manifest["runtime"] = map[string]any{"type": "process", "entry": "bin/plugin.mjs"}
		manifest["capabilities"] = capabilities
		manifest["contributes"] = map[string]any{
			"actions": []any{
				map[string]any{"id": "run", "risk": "read"},
				map[string]any{"id": "big", "risk": "write", "maxPayloadBytes": 262144},
				map[string]any{"id": "assist", "risk": "read", "aiCallable": true, "description": "Assist", "inputSchema": map[string]any{"type": "object"}},
			},
			"services": services,
		}
		if mutate != nil {
			mutate(manifest)
		}
	}, nil, map[string]string{"bin/plugin.mjs": "process.stdin.resume();\n"})
}

func serviceEntry(mutate func(map[string]any)) map[string]any {
	entry := map[string]any{
		"id": "reverse", "service": "demo.text.reverse", "action": "run", "title": "Reverse text",
		"titleLocalized": map[string]any{"zh-CN": "反转文字"}, "description": "Reverse the characters of a text",
		"input": map[string]any{"kind": "text"}, "output": map[string]any{"kind": "text"},
	}
	if mutate != nil {
		mutate(entry)
	}
	return entry
}

func fileServiceEntry(mutate func(map[string]any)) map[string]any {
	return serviceEntry(func(entry map[string]any) {
		entry["input"] = map[string]any{"kind": "file", "accepts": []any{".pdf", "image/*"}}
		entry["output"] = map[string]any{"kind": "json"}
		if mutate != nil {
			mutate(entry)
		}
	})
}

func TestDecodeServiceEntry(t *testing.T) {
	useTemporaryPluginData(t)
	withRead := []string{"commands", "ui", "workspace.read"}
	withoutRead := []string{"commands", "ui"}
	accepts33 := make([]any, 33)
	for index := range accepts33 {
		accepts33[index] = "." + strings.Repeat("x", index%5+1) + string(rune('a'+index%26)) + string(rune('a'+index/26))
	}
	cases := []struct {
		name         string
		services     []any
		capabilities []string
		mutate       func(map[string]any)
		wantErr      string
		check        func(t *testing.T, services []ServiceContribution)
	}{
		{name: "text to text defaults", services: []any{serviceEntry(nil)}, capabilities: withRead, check: func(t *testing.T, services []ServiceContribution) {
			service := services[0]
			if service.Input.MaxBytes != 8192 || service.TimeoutMs != 30000 || service.Risk != "read" ||
				!reflect.DeepEqual(service.Surfaces, []string{"selection", "palette"}) || service.Title != "Reverse text" ||
				service.Service != "demo.text.reverse" || service.Output.Kind != "text" {
				t.Fatalf("service = %+v", service)
			}
		}},
		{name: "file defaults", services: []any{fileServiceEntry(nil)}, capabilities: withRead, check: func(t *testing.T, services []ServiceContribution) {
			service := services[0]
			if !reflect.DeepEqual(service.Surfaces, []string{"fileMenu", "palette"}) || service.Input.MaxBytes != 67108864 ||
				!reflect.DeepEqual(service.Input.Accepts, []string{".pdf", "image/*"}) {
				t.Fatalf("service = %+v", service)
			}
		}},
		{name: "files defaults", services: []any{fileServiceEntry(func(e map[string]any) {
			e["input"] = map[string]any{"kind": "files", "accepts": []any{".txt"}}
		})}, capabilities: withRead, check: func(t *testing.T, services []ServiceContribution) {
			if services[0].Input.MaxFiles != 32 || !reflect.DeepEqual(services[0].Surfaces, []string{"fileMenu", "palette"}) {
				t.Fatalf("service = %+v", services[0])
			}
		}},
		{name: "none defaults", services: []any{serviceEntry(func(e map[string]any) { e["input"] = map[string]any{"kind": "none"} })}, capabilities: withRead, check: func(t *testing.T, services []ServiceContribution) {
			if !reflect.DeepEqual(services[0].Surfaces, []string{"palette"}) {
				t.Fatalf("surfaces = %v", services[0].Surfaces)
			}
		}},
		{name: "empty surfaces keep plugin callers only", services: []any{serviceEntry(func(e map[string]any) { e["surfaces"] = []any{} })}, capabilities: withRead, check: func(t *testing.T, services []ServiceContribution) {
			if services[0].Surfaces == nil || len(services[0].Surfaces) != 0 {
				t.Fatalf("surfaces = %#v", services[0].Surfaces)
			}
		}},
		{name: "uppercase service", services: []any{serviceEntry(func(e map[string]any) { e["service"] = "Demo.x" })}, capabilities: withRead, wantErr: "must match"},
		{name: "single segment service", services: []any{serviceEntry(func(e map[string]any) { e["service"] = "demo" })}, capabilities: withRead, wantErr: "must match"},
		{name: "five segment service", services: []any{serviceEntry(func(e map[string]any) { e["service"] = "a.b.c.d.e" })}, capabilities: withRead, wantErr: "must match"},
		{name: "65 character service", services: []any{serviceEntry(func(e map[string]any) { e["service"] = "a." + strings.Repeat("b", 63) })}, capabilities: withRead, wantErr: "must be at most 64 characters"},
		{name: "reserved mincode prefix", services: []any{serviceEntry(func(e map[string]any) { e["service"] = "mincode.x" })}, capabilities: withRead, wantErr: `reserved prefix "mincode."`},
		{name: "reserved host prefix", services: []any{serviceEntry(func(e map[string]any) { e["service"] = "host.x" })}, capabilities: withRead, wantErr: `reserved prefix "host."`},
		{name: "reserved plugin prefix", services: []any{serviceEntry(func(e map[string]any) { e["service"] = "plugin.x" })}, capabilities: withRead, wantErr: `reserved prefix "plugin."`},
		{name: "unknown action", services: []any{serviceEntry(func(e map[string]any) { e["action"] = "nope" })}, capabilities: withRead, wantErr: "is not one of this plugin's contributes.actions"},
		{name: "aiCallable action", services: []any{serviceEntry(func(e map[string]any) { e["action"] = "assist" })}, capabilities: withRead, wantErr: "must not be aiCallable"},
		{name: "file without accepts", services: []any{fileServiceEntry(func(e map[string]any) { e["input"] = map[string]any{"kind": "file"} })}, capabilities: withRead, wantErr: "accepts is required"},
		{name: "33 accepts", services: []any{fileServiceEntry(func(e map[string]any) { e["input"] = map[string]any{"kind": "file", "accepts": accepts33} })}, capabilities: withRead, wantErr: "accepts must have at most 32 entries"},
		{name: "mixed-case accepts are lower-cased", services: []any{fileServiceEntry(func(e map[string]any) {
			e["input"] = map[string]any{"kind": "file", "accepts": []any{".PNG", "Image/*"}}
		})}, capabilities: withRead, check: func(t *testing.T, services []ServiceContribution) {
			if !reflect.DeepEqual(services[0].Input.Accepts, []string{".png", "image/*"}) {
				t.Fatalf("accepts = %v", services[0].Input.Accepts)
			}
		}},
		{name: "accepts duplicate after lower-casing", services: []any{fileServiceEntry(func(e map[string]any) {
			e["input"] = map[string]any{"kind": "file", "accepts": []any{".png", ".PNG"}}
		})}, capabilities: withRead, wantErr: `accepts lists ".png" twice`},
		{name: "bare extension accepts", services: []any{fileServiceEntry(func(e map[string]any) { e["input"] = map[string]any{"kind": "file", "accepts": []any{"png"}} })}, capabilities: withRead, wantErr: `accepts "png" must be an extension`},
		{name: "text too large for default payload", services: []any{serviceEntry(func(e map[string]any) { e["input"] = map[string]any{"kind": "text", "maxBytes": 16384} })}, capabilities: withRead, wantErr: "maxPayloadBytes of at least 24576"},
		{name: "text at the maximum", services: []any{serviceEntry(func(e map[string]any) {
			e["action"] = "big"
			e["input"] = map[string]any{"kind": "text", "maxBytes": 196608}
		})}, capabilities: withRead, check: func(t *testing.T, services []ServiceContribution) {
			if services[0].Input.MaxBytes != 196608 || services[0].Risk != "write" {
				t.Fatalf("service = %+v", services[0])
			}
		}},
		{name: "text above the maximum", services: []any{serviceEntry(func(e map[string]any) {
			e["action"] = "big"
			e["input"] = map[string]any{"kind": "text", "maxBytes": 196609}
		})}, capabilities: withRead, wantErr: "maxBytes"},
		{name: "explicit content surfaces without workspace.read", services: []any{
			fileServiceEntry(func(e map[string]any) { e["surfaces"] = []any{"fileMenu"} }),
			serviceEntry(func(e map[string]any) {
				e["id"] = "pick"
				e["service"] = "demo.text.pick"
				e["surfaces"] = []any{"selection"}
			}),
		}, capabilities: withoutRead, check: func(t *testing.T, services []ServiceContribution) {
			if !reflect.DeepEqual(services[0].Surfaces, []string{"fileMenu"}) || !reflect.DeepEqual(services[1].Surfaces, []string{"selection"}) {
				t.Fatalf("surfaces = %v %v", services[0].Surfaces, services[1].Surfaces)
			}
		}},
		{name: "text input on fileMenu", services: []any{serviceEntry(func(e map[string]any) { e["surfaces"] = []any{"fileMenu"} })}, capabilities: withRead, wantErr: "not compatible"},
		{name: "file input on selection", services: []any{fileServiceEntry(func(e map[string]any) { e["surfaces"] = []any{"selection"} })}, capabilities: withRead, wantErr: "not compatible"},
		{name: "timeout too long", services: []any{serviceEntry(func(e map[string]any) { e["timeoutMs"] = 300001 })}, capabilities: withRead, wantErr: "timeoutMs"},
		{name: "object params property", services: []any{serviceEntry(func(e map[string]any) {
			e["params"] = map[string]any{"type": "object", "properties": map[string]any{"options": map[string]any{"type": "object"}}}
		})}, capabilities: withRead, wantErr: "must be string, integer, number, or boolean"},
		{name: "duplicate service", services: []any{serviceEntry(nil), serviceEntry(func(e map[string]any) { e["id"] = "again" })}, capabilities: withRead, wantErr: "demo.text.reverse"},
		{name: "declarative runtime", services: []any{serviceEntry(nil)}, capabilities: withRead, mutate: func(manifest map[string]any) {
			manifest["runtime"] = map[string]any{"type": "declarative"}
			delete(manifest, "capabilities")
			contributes := manifest["contributes"].(map[string]any)
			delete(contributes, "actions")
			contributes["keymaps"] = []any{keymapPreset("acme-services.keys")}
		}, wantErr: "runtime.type process"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manifest, err := readManifestStrict(writeServicePackage(t, tc.services, tc.capabilities, tc.mutate))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to mention %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			services := servicesLocked(&pluginRecord{manifest: &manifest})
			if len(services) != len(tc.services) {
				t.Fatalf("services = %+v", services)
			}
			tc.check(t, services)
		})
	}
}

func TestServicesExpandPermission(t *testing.T) {
	base := ServiceContribution{ID: "reverse", Service: "demo.text.reverse", Action: "run", Surfaces: []string{"palette"}}
	resolved := func(entries ...ServiceContribution) *resolvedContribution {
		out := &resolvedContribution{}
		for _, entry := range entries {
			out.Entries = append(out.Entries, entry)
		}
		return out
	}
	withSelection := base
	withSelection.Surfaces = []string{"selection", "palette"}
	fewer := base
	fewer.Surfaces = []string{}
	otherAction := base
	otherAction.Action = "big"
	retitled := base
	retitled.Title = "Other"
	added := ServiceContribution{ID: "ocr", Service: "image.ocr", Action: "run"}
	cases := []struct {
		name string
		next *resolvedContribution
		want bool
	}{
		{"same", resolved(base), false},
		{"retitled", resolved(retitled), false},
		{"fewer surfaces", resolved(fewer), false},
		{"adds selection", resolved(withSelection), true},
		{"other action", resolved(otherAction), true},
		{"added", resolved(base, added), true},
		{"removed", resolved(), false},
	}
	for _, tc := range cases {
		if got := servicesExpandPermission(resolved(base), tc.next); got != tc.want {
			t.Fatalf("%s: %v", tc.name, got)
		}
	}
}

// decodeServicesForTest installs a services package in strict mode and
// returns its resolved services or the strict error.
func decodeServicesForTest(t *testing.T, services ...any) ([]ServiceContribution, error) {
	t.Helper()
	manifest, err := readManifestStrict(writeServicePackage(t, services, []string{"commands", "ui"}, nil))
	if err != nil {
		return nil, err
	}
	return servicesLocked(&pluginRecord{manifest: &manifest}), nil
}

func TestServiceSurfacesNewValuesAreOptIn(t *testing.T) {
	useTemporaryPluginData(t)
	if !reflect.DeepEqual(serviceDefaultSurfaces, []string{serviceSurfaceFileMenu, serviceSurfaceSelection, serviceSurfacePalette}) {
		t.Fatalf("serviceDefaultSurfaces = %v", serviceDefaultSurfaces)
	}
	jsonFile := func(id, service string, surfaces []any) map[string]any {
		return fileServiceEntry(func(e map[string]any) {
			e["id"], e["service"] = id, service
			e["input"] = map[string]any{"kind": "file", "accepts": []any{".json", "text/*"}}
			if surfaces != nil {
				e["surfaces"] = surfaces
			}
		})
	}
	services, err := decodeServicesForTest(t,
		jsonFile("plain", "demo.file.plain", nil),
		serviceEntry(nil),
		serviceEntry(func(e map[string]any) {
			e["id"], e["service"] = "none", "demo.none.run"
			e["input"] = map[string]any{"kind": "none"}
		}),
		jsonFile("remote", "demo.file.remote", []any{"httpResponse", "dbResult", "sftpFile"}),
		serviceEntry(func(e map[string]any) {
			e["id"], e["service"] = "term", "demo.text.term"
			e["surfaces"] = []any{"terminalSelection", "selection"}
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, service := range services {
		got[service.ID] = service.Surfaces
	}
	want := map[string][]string{
		"plain":   {"fileMenu", "palette"},
		"reverse": {"selection", "palette"},
		"none":    {"palette"},
		"remote":  {"sftpFile", "dbResult", "httpResponse"},
		"term":    {"selection", "terminalSelection"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("surfaces = %v, want %v", got, want)
	}
	for _, surface := range []string{serviceSurfaceSFTPFile, serviceSurfaceDBResult, serviceSurfaceHTTPResponse, serviceSurfaceTerminalSelection, serviceSurfaceFileMenu, serviceSurfaceSelection} {
		if !serviceSurfacePassesContent(surface) {
			t.Fatalf("serviceSurfacePassesContent(%s) = false", surface)
		}
	}
	if serviceSurfacePassesContent(serviceSurfacePalette) {
		t.Fatal("palette must not count as passing content")
	}
}

func TestServiceSurfacesCompatibility(t *testing.T) {
	useTemporaryPluginData(t)
	cases := map[string]struct {
		entry map[string]any
		want  string
	}{
		"terminalSelection on file": {fileServiceEntry(func(e map[string]any) { e["surfaces"] = []any{"terminalSelection"} }), `surface "terminalSelection" is not compatible with input kind file`},
		"terminalSelection on none": {serviceEntry(func(e map[string]any) {
			e["input"] = map[string]any{"kind": "none"}
			e["surfaces"] = []any{"terminalSelection"}
		}), `surface "terminalSelection" is not compatible with input kind none`},
		"sftpFile on text":     {serviceEntry(func(e map[string]any) { e["surfaces"] = []any{"sftpFile"} }), `surface "sftpFile" is not compatible with input kind text`},
		"dbResult on text":     {serviceEntry(func(e map[string]any) { e["surfaces"] = []any{"dbResult"} }), `surface "dbResult" is not compatible with input kind text`},
		"httpResponse on text": {serviceEntry(func(e map[string]any) { e["surfaces"] = []any{"httpResponse"} }), `surface "httpResponse" is not compatible with input kind text`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeServicesForTest(t, tc.entry); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
	files, err := decodeServicesForTest(t, fileServiceEntry(func(e map[string]any) {
		e["input"] = map[string]any{"kind": "files", "accepts": []any{".json"}}
		e["surfaces"] = []any{"sftpFile", "dbResult", "httpResponse"}
	}))
	if err != nil || !reflect.DeepEqual(files[0].Surfaces, []string{"sftpFile", "dbResult", "httpResponse"}) {
		t.Fatalf("files kind: %v %+v", err, files)
	}
}

func TestServiceDbResultAcceptsMustMatchResultJSON(t *testing.T) {
	useTemporaryPluginData(t)
	if serviceDBResultFileName != "result.json" {
		t.Fatalf("serviceDBResultFileName = %q", serviceDBResultFileName)
	}
	entry := func(kind string, accepts ...any) map[string]any {
		return fileServiceEntry(func(e map[string]any) {
			e["input"] = map[string]any{"kind": kind, "accepts": accepts}
			e["surfaces"] = []any{"dbResult"}
		})
	}
	for name, accepted := range map[string]map[string]any{
		".json":             entry("file", ".json"),
		"application/json":  entry("file", ".csv", "application/json"),
		"application/*":     entry("file", "application/*"),
		"upper-case MIME":   entry("file", "Application/JSON"),
		"files kind .json":  entry("files", ".JSON"),
		"csv without dbRes": fileServiceEntry(func(e map[string]any) { e["input"] = map[string]any{"kind": "file", "accepts": []any{".csv"}} }),
	} {
		if _, err := decodeServicesForTest(t, accepted); err != nil {
			t.Fatalf("%s rejected: %v", name, err)
		}
	}
	for name, rejected := range map[string]map[string]any{
		".csv":           entry("file", ".csv"),
		"text/*":         entry("file", "text/*"),
		"xml and ndjson": entry("files", "application/xml", ".ndjson"),
	} {
		_, err := decodeServicesForTest(t, rejected)
		if err == nil || !strings.Contains(err.Error(), `surface "dbResult" hands over result.json (application/json)`) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
}

func TestServicesExpandPermissionNewSurfaces(t *testing.T) {
	base := ServiceContribution{ID: "convert", Service: "demo.file.convert", Action: "run", Surfaces: []string{"fileMenu", "palette"}}
	wrap := func(service ServiceContribution) *resolvedContribution {
		return &resolvedContribution{Entries: []any{service}}
	}
	for _, surface := range []string{"sftpFile", "dbResult", "httpResponse", "terminalSelection"} {
		next := base
		next.Surfaces = append(append([]string(nil), base.Surfaces...), surface)
		if !servicesExpandPermission(wrap(base), wrap(next)) {
			t.Fatalf("adding %s must expand permission", surface)
		}
		if servicesExpandPermission(wrap(next), wrap(base)) {
			t.Fatalf("removing %s must not expand permission", surface)
		}
	}
}
