package plugin

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func writeGlobalShortcutPackage(t *testing.T, shortcuts []any) string {
	t.Helper()
	return writeKeymapPackage(t, "", func(manifest map[string]any) {
		manifest["id"] = "acme-shortcuts"
		manifest["runtime"] = map[string]any{"type": "process", "entry": "bin/plugin.mjs"}
		manifest["capabilities"] = []string{"commands", "ui"}
		manifest["contributes"] = map[string]any{
			"views": []any{
				map[string]any{"id": "main", "title": "Main", "entry": "ui/index.html"},
				map[string]any{"id": "pet", "title": "Pet", "entry": "ui/index.html", "floating": map[string]any{"width": 320, "height": 320}},
			},
			"actions": []any{
				map[string]any{"id": "capture", "risk": "write", "uiCallable": true, "maxPayloadBytes": 2048},
				map[string]any{"id": "status", "risk": "read", "uiCallable": true},
				map[string]any{"id": "tiny", "risk": "read", "uiCallable": true, "maxPayloadBytes": 64},
				map[string]any{"id": "purge", "risk": "dangerous", "uiCallable": true},
				map[string]any{"id": "internal", "risk": "write"},
			},
			"globalShortcuts": shortcuts,
		}
	}, nil, map[string]string{"bin/plugin.mjs": "process.stdin.resume();\n", "ui/index.html": "<!doctype html><title>t</title>\n"})
}

func validGlobalShortcut() map[string]any {
	return map[string]any{
		"id": "capture-region", "title": "Capture region", "titleLocalized": map[string]any{"zh": "框选截图", "en": "Capture region"},
		"key": "CmdOrCtrl+Shift+A", "run": map[string]any{"type": "action", "action": "capture", "payload": map[string]any{"mode": "region"}},
	}
}

func TestGlobalShortcutsValid(t *testing.T) {
	useTemporaryPluginData(t)
	inspection, err := inspectInstallDirectory(filepath.Join(contributionFixtureRoot, "valid", "globalShortcuts-process"))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := parseGlobalShortcuts(&inspection.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		t.Fatalf("entries = %+v", entries)
	}
	capture := entries[0]
	if capture.ID != "capture-region" || capture.Key != "CmdOrCtrl+Shift+A" || capture.TitleLocalized["zh"] != "框选截图" ||
		capture.Run.Type != "action" || capture.Run.Action != "capture" || string(capture.Run.Payload) != `{"mode":"region"}` {
		t.Fatalf("capture = %+v (payload %s)", capture, capture.Run.Payload)
	}
	if entries[1].Run.Payload != nil {
		t.Fatalf("absent payload kept: %s", entries[1].Run.Payload)
	}
	if entries[2].Run.Type != "view" || entries[2].Run.View != "main" || entries[3].Run.Type != "floating" || entries[3].Run.View != "pet" {
		t.Fatalf("view entries = %+v", entries[2:])
	}
	// The snapshot shape the frontend and later tasks read.
	data, err := json.Marshal(entries[2])
	if err != nil || string(data) != `{"id":"open-main","title":"Open main page","key":"CmdOrCtrl+Option+M","run":{"type":"view","view":"main"}}` {
		t.Fatalf("snapshot = %s %v", data, err)
	}

	// The inline helper package accepts the same entry.
	manifest, err := readManifestStrict(writeGlobalShortcutPackage(t, []any{validGlobalShortcut()}))
	if err != nil {
		t.Fatal(err)
	}
	if entries, err := parseGlobalShortcuts(&manifest); err != nil || len(entries) != 1 {
		t.Fatalf("entries %+v %v", entries, err)
	}
	if entries, err := parseGlobalShortcuts(&Manifest{}); err != nil || entries != nil {
		t.Fatalf("no point: %+v %v", entries, err)
	}
}

func TestGlobalShortcutsRejects(t *testing.T) {
	useTemporaryPluginData(t)
	with := func(mutate func(map[string]any)) map[string]any {
		entry := validGlobalShortcut()
		mutate(entry)
		return entry
	}
	run := func(value map[string]any) func(map[string]any) {
		return func(entry map[string]any) { entry["run"] = value }
	}
	cases := map[string]struct {
		entries []any
		want    string
	}{
		"duplicate id":       {[]any{validGlobalShortcut(), with(func(e map[string]any) { e["key"] = "CmdOrCtrl+Shift+B" })}, "duplicate global shortcut id"},
		"bad id":             {[]any{with(func(e map[string]any) { e["id"] = "Capture" })}, "must match"},
		"long id":            {[]any{with(func(e map[string]any) { e["id"] = strings.Repeat("a", 33) })}, "must match"},
		"bad key modifier":   {[]any{with(func(e map[string]any) { e["key"] = "Hyper+A" })}, "not a valid accelerator"},
		"bad key name":       {[]any{with(func(e map[string]any) { e["key"] = "CmdOrCtrl+Banana" })}, "not a valid accelerator"},
		"empty key":          {[]any{with(func(e map[string]any) { e["key"] = "" })}, "not a valid accelerator"},
		"no modifier":        {[]any{with(func(e map[string]any) { e["key"] = "F5" })}, "modifier other than Shift"},
		"shift only":         {[]any{with(func(e map[string]any) { e["key"] = "Shift+A" })}, "modifier other than Shift"},
		"reserved mac":       {[]any{with(func(e map[string]any) { e["key"] = "Cmd+Q" })}, "reserved system shortcut"},
		"reserved windows":   {[]any{with(func(e map[string]any) { e["key"] = "Alt+F4" })}, "reserved system shortcut"},
		"reserved alias":     {[]any{with(func(e map[string]any) { e["key"] = "CmdOrCtrl+Alt+Delete" })}, "reserved system shortcut"},
		"missing title":      {[]any{with(func(e map[string]any) { delete(e, "title") })}, "title"},
		"long title":         {[]any{with(func(e map[string]any) { e["title"] = strings.Repeat("长", 49) })}, "title"},
		"unknown field":      {[]any{with(func(e map[string]any) { e["global"] = true })}, "unknown field"},
		"unknown run type":   {[]any{with(run(map[string]any{"type": "command", "action": "capture"}))}, "run.type"},
		"unknown run field":  {[]any{with(run(map[string]any{"type": "action", "action": "capture", "args": "x"}))}, "unknown field"},
		"missing action":     {[]any{with(run(map[string]any{"type": "action", "action": "nope"}))}, "not one of this plugin's contributes.actions"},
		"dangerous action":   {[]any{with(run(map[string]any{"type": "action", "action": "purge"}))}, "must not have risk dangerous"},
		"not uiCallable":     {[]any{with(run(map[string]any{"type": "action", "action": "internal"}))}, "must be uiCallable"},
		"action with view":   {[]any{with(run(map[string]any{"type": "action", "action": "capture", "view": "main"}))}, "must not set view"},
		"missing view":       {[]any{with(run(map[string]any{"type": "view", "view": "nope"}))}, "not one of this plugin's contributes.views"},
		"view on floating":   {[]any{with(run(map[string]any{"type": "view", "view": "pet"}))}, "is a floating view"},
		"floating on tab":    {[]any{with(run(map[string]any{"type": "floating", "view": "main"}))}, "declares floating"},
		"view with payload":  {[]any{with(run(map[string]any{"type": "view", "view": "main", "payload": map[string]any{}}))}, "must not set action or payload"},
		"payload too large":  {[]any{with(run(map[string]any{"type": "action", "action": "capture", "payload": map[string]any{"note": strings.Repeat("x", 1100)}}))}, "payload must be at most 1024 bytes"},
		"payload not object": {[]any{with(run(map[string]any{"type": "action", "action": "capture", "payload": []any{1}}))}, "payload must be a JSON object"},
		"payload trigger":    {[]any{with(run(map[string]any{"type": "action", "action": "capture", "payload": map[string]any{"trigger": "x"}}))}, "reserved"},
		"action too small":   {[]any{with(run(map[string]any{"type": "action", "action": "tiny"}))}, "maxPayloadBytes"},
		"too many": {[]any{
			with(func(e map[string]any) { e["id"] = "a"; e["key"] = "CmdOrCtrl+Alt+1" }),
			with(func(e map[string]any) { e["id"] = "b"; e["key"] = "CmdOrCtrl+Alt+2" }),
			with(func(e map[string]any) { e["id"] = "c"; e["key"] = "CmdOrCtrl+Alt+3" }),
			with(func(e map[string]any) { e["id"] = "d"; e["key"] = "CmdOrCtrl+Alt+4" }),
			with(func(e map[string]any) { e["id"] = "e"; e["key"] = "CmdOrCtrl+Alt+5" }),
			with(func(e map[string]any) { e["id"] = "f"; e["key"] = "CmdOrCtrl+Alt+6" }),
		}, "at most 5"},
	}
	for name, tc := range cases {
		_, err := readManifestStrict(writeGlobalShortcutPackage(t, tc.entries))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err = %v, want %q", name, err, tc.want)
		}
	}

	declarative := writeKeymapPackage(t, "", func(manifest map[string]any) {
		manifest["contributes"] = map[string]any{
			"keymaps":         []any{keymapPreset("keymap-test.a")},
			"globalShortcuts": []any{map[string]any{"id": "open", "title": "Open", "key": "CmdOrCtrl+Shift+O", "run": map[string]any{"type": "view", "view": "main"}}},
		}
	}, nil, nil)
	if _, err := readManifestStrict(declarative); err == nil || !strings.Contains(err.Error(), "requires runtime.type process") {
		t.Fatalf("declarative: %v", err)
	}

	// Lenient loading skips the bad entry and parseGlobalShortcuts reports it.
	manifest, err := readManifest(writeGlobalShortcutPackage(t, []any{validGlobalShortcut(), with(func(e map[string]any) { e["id"] = "quit"; e["key"] = "Cmd+Q" })}))
	if err != nil {
		t.Fatal(err)
	}
	if entries, err := parseGlobalShortcuts(&manifest); err == nil || len(entries) != 1 || !strings.Contains(err.Error(), "reserved system shortcut") {
		t.Fatalf("lenient: %+v %v", entries, err)
	}
}

func TestValidAccelerator(t *testing.T) {
	for _, key := range []string{
		"CmdOrCtrl+Shift+A", "cmdorctrl+shift+a", "Cmd+Shift+1", "Command+Alt+K", "Ctrl+Option+P", "OptionOrAlt+F12",
		"Super+Shift+K", "Ctrl+Shift+Page Up", "CmdOrCtrl+Plus", "Ctrl+Alt+Shift+Space", "CmdOrCtrl+Shift+/", "Alt+F5",
		// Reserved on one platform only after mapping: Win+L is Super+L on Windows, but Ctrl+L / Cmd+L are not reserved.
		"CmdOrCtrl+L", "Ctrl+Q", "Cmd+Shift+6",
		// Ctrl+Shift+S is Ctrl+Shift+S everywhere; only Win+Shift+S is reserved.
		"Ctrl+Shift+S",
	} {
		if err := ValidAccelerator(key); err != nil {
			t.Fatalf("%q rejected: %v", key, err)
		}
	}
	for key, want := range map[string]string{
		"":                             "not a valid accelerator",
		"A":                            "modifier other than Shift",
		"Shift+F1":                     "modifier other than Shift",
		"Shift+Shift+A":                "repeats",
		"Cmd+Command+A":                "repeats",
		"Win+L":                        "not a valid accelerator", // accelerators spell the Windows key Super
		"Ctrl+Alt+Del":                 "not a valid accelerator", // accelerators have no Del key name
		"Cmd++":                        "not a valid accelerator",
		"Cmd+ ":                        "not a valid accelerator",
		"Cmd+é":                        "not a valid accelerator",
		"Cmd+Shift+A ":                 "not a valid accelerator",
		"Cmd+Shift":                    "not a valid accelerator",
		strings.Repeat("a+", 40) + "b": "not a valid accelerator",
		"Cmd+Q":                        "reserved system shortcut",
		"Command+Tab":                  "reserved system shortcut",
		"CmdOrCtrl+Space":              "reserved system shortcut",
		"Cmd+Shift+3":                  "reserved system shortcut",
		"Shift+CmdOrCtrl+4":            "reserved system shortcut",
		"CmdOrCtrl+Shift+5":            "reserved system shortcut",
		"Ctrl+Cmd+Q":                   "reserved system shortcut",
		"Alt+F4":                       "reserved system shortcut",
		"Option+F4":                    "reserved system shortcut",
		"Ctrl+Alt+Delete":              "reserved system shortcut",
		"CmdOrCtrl+Alt+Delete":         "reserved system shortcut",
		"Super+L":                      "reserved system shortcut",
		"Ctrl+Cmd+Shift+3":             "reserved system shortcut",
		"Ctrl+Command+Shift+4":         "reserved system shortcut",
		"CmdOrCtrl+Shift+Q":            "reserved system shortcut",
		"Cmd+Option+Escape":            "reserved system shortcut",
		"Super+Shift+S":                "reserved system shortcut",
		"CmdOrCtrl+Shift+Escape":       "reserved system shortcut",
		// Input source / launcher / app switcher / Linux lock screen.
		"Ctrl+Space":        "reserved system shortcut",
		"Ctrl+Option+Space": "reserved system shortcut",
		"Ctrl+Alt+Space":    "reserved system shortcut",
		"Super+Space":       "reserved system shortcut",
		"Alt+Tab":           "reserved system shortcut",
		"Option+Tab":        "reserved system shortcut",
		"Ctrl+Alt+L":        "reserved system shortcut",
	} {
		if err := ValidAccelerator(key); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%q: err = %v, want %q", key, err, want)
		}
	}
}

// Default keys (the manifest's key) must not take an application-standard
// combination: CmdOrCtrl plus one of C V X Z A S W Q F N T O P with no other
// modifier. A user may still pick one in the settings (ValidAccelerator).
func TestGlobalShortcutDefaultKey(t *testing.T) {
	for _, key := range []string{
		"CmdOrCtrl+Shift+C", "CmdOrCtrl+Alt+V", "Ctrl+Shift+S", "Cmd+Option+F", "CmdOrCtrl+Shift+A", "Ctrl+Alt+F9",
		"CmdOrCtrl+B", "Ctrl+Alt+N", "Alt+X",
	} {
		if err := validDefaultAccelerator(key); err != nil {
			t.Fatalf("%q rejected as default: %v", key, err)
		}
	}
	for _, key := range []string{
		"CmdOrCtrl+C", "Cmd+V", "Command+X", "CmdOrCtrl+Z", "CmdOrCtrl+A", "CmdOrCtrl+S", "CmdOrCtrl+W",
		"CmdOrCtrl+F", "CmdOrCtrl+N", "CmdOrCtrl+T", "CmdOrCtrl+O", "CmdOrCtrl+P",
		// Ctrl+S is Windows/Linux save; Super+W is Command+W on macOS.
		"Ctrl+S", "Super+W",
	} {
		if err := validDefaultAccelerator(key); err == nil || !strings.Contains(err.Error(), "application-standard shortcut") {
			t.Fatalf("%q as default: %v", key, err)
		}
		if err := ValidAccelerator(key); err != nil {
			t.Fatalf("%q must stay valid as a user key: %v", key, err)
		}
	}
	// CmdOrCtrl+Q stays a reserved system shortcut (macOS Quit) for both.
	if err := validDefaultAccelerator("CmdOrCtrl+Q"); err == nil || !strings.Contains(err.Error(), "reserved system shortcut") {
		t.Fatalf("CmdOrCtrl+Q: %v", err)
	}
	if len(reservedDefaultAccelerators) == 0 {
		t.Fatal("no reserved default accelerators")
	}
	for _, entry := range reservedDefaultAccelerators {
		if _, _, err := parseReservedAccelerator(entry); err != nil {
			t.Fatalf("%q: %v", entry, err)
		}
	}

	// Install validation applies it to the manifest key.
	useTemporaryPluginData(t)
	entry := validGlobalShortcut()
	entry["key"] = "CmdOrCtrl+C"
	_, err := readManifestStrict(writeGlobalShortcutPackage(t, []any{entry}))
	if err == nil || !strings.Contains(err.Error(), "application-standard shortcut") {
		t.Fatalf("install with CmdOrCtrl+C: %v", err)
	}
}

func TestReservedAcceleratorsParse(t *testing.T) {
	if len(reservedAccelerators) == 0 {
		t.Fatal("no reserved accelerators")
	}
	for _, entry := range reservedAccelerators {
		if _, _, err := parseReservedAccelerator(entry); err != nil {
			t.Fatalf("%q: %v", entry, err)
		}
	}
}

func TestGlobalShortcutsExpandPermission(t *testing.T) {
	base := GlobalShortcutEntry{ID: "capture", Title: "Capture", Key: "CmdOrCtrl+Shift+A", Run: GlobalShortcutRun{Type: "action", Action: "capture", Payload: json.RawMessage(`{"mode":"region"}`)}}
	resolved := func(entries ...GlobalShortcutEntry) *resolvedContribution {
		out := &resolvedContribution{}
		for _, entry := range entries {
			out.Entries = append(out.Entries, entry)
		}
		return out
	}
	retitled := base
	retitled.Title = "Grab"
	rekeyed := base
	rekeyed.Key = "CmdOrCtrl+Shift+B"
	otherAction := base
	otherAction.Run.Action = "other"
	otherPayload := base
	otherPayload.Run.Payload = json.RawMessage(`{"mode":"screen"}`)
	added := GlobalShortcutEntry{ID: "screen", Title: "Screen", Key: "CmdOrCtrl+Shift+1", Run: GlobalShortcutRun{Type: "view", View: "main"}}
	for _, tc := range []struct {
		name string
		next *resolvedContribution
		want bool
	}{
		{"same", resolved(base), false},
		{"retitled", resolved(retitled), false},
		{"added (off by default)", resolved(base, added), false},
		{"removed", resolved(), false},
		{"rekeyed", resolved(rekeyed), true},
		{"other action", resolved(otherAction), true},
		{"other payload", resolved(otherPayload), true},
	} {
		if got := globalShortcutsExpandPermission(resolved(base), tc.next); got != tc.want {
			t.Fatalf("%s: %v", tc.name, got)
		}
	}
}
