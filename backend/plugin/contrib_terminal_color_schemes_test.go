package plugin

import (
	"encoding/json"
	"strings"
	"testing"
)

func validTerminalColorSchemeColors() map[string]string {
	return map[string]string{
		"background": "#1E1E1E", "foreground": "#d4d4d4", "cursor": "#ffffff", "cursorAccent": "#1e1e1e",
		"selectionBackground": "#264f78",
		"black":               "#000000", "red": "#f14c4c", "green": "#23d18b", "yellow": "#f5f543",
		"blue": "#3b8eea", "magenta": "#d670d6", "cyan": "#29b8db", "white": "#e5e5e5",
		"brightBlack": "#808080", "brightRed": "#f14c4c", "brightGreen": "#23d18b", "brightYellow": "#f5f543",
		"brightBlue": "#3b8eea", "brightMagenta": "#d670d6", "brightCyan": "#29b8db", "brightWhite": "#ffffff",
	}
}

func decodeTerminalColorSchemeForTest(t *testing.T, mutate func(map[string]any)) (TerminalColorSchemeContribution, error) {
	t.Helper()
	colors := map[string]any{}
	for key, value := range validTerminalColorSchemeColors() {
		colors[key] = value
	}
	entry := map[string]any{"id": "acme.night", "title": "Night", "titleLocalized": map[string]string{"zh": "夜"}, "kind": "dark", "colors": colors}
	if mutate != nil {
		mutate(entry)
	}
	raw, _ := json.Marshal(entry)
	ctx := &pointContext{manifest: &Manifest{ID: "acme", APIVersion: 2}, spec: terminalColorSchemeSpec}
	value, _, err := decodeTerminalColorSchemeEntry(ctx, 0, "", raw)
	if err != nil {
		return TerminalColorSchemeContribution{}, err
	}
	return value.(TerminalColorSchemeContribution), nil
}

func TestTerminalColorSchemeDecode(t *testing.T) {
	scheme, err := decodeTerminalColorSchemeForTest(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if scheme.ID != "acme.night" || scheme.Kind != "dark" || scheme.Colors["background"] != "#1e1e1e" || scheme.TitleLocalized["zh"] != "夜" {
		t.Fatalf("scheme = %+v", scheme)
	}
	// Alpha is allowed for the selection keys and composited on the background.
	if _, err := decodeTerminalColorSchemeForTest(t, func(entry map[string]any) {
		entry["colors"].(map[string]any)["selectionBackground"] = "#264f7880"
	}); err != nil {
		t.Fatalf("translucent selection: %v", err)
	}
}

func TestTerminalColorSchemeRejects(t *testing.T) {
	color := func(key, value string) func(map[string]any) {
		return func(entry map[string]any) { entry["colors"].(map[string]any)[key] = value }
	}
	cases := map[string]struct {
		mutate func(map[string]any)
		want   string
	}{
		"foreign id":        {func(entry map[string]any) { entry["id"] = "other.night" }, "must be"},
		"bad name":          {func(entry map[string]any) { entry["id"] = "acme.Night" }, "must be"},
		"kind":              {func(entry map[string]any) { entry["kind"] = "dim" }, "kind"},
		"missing ansi":      {func(entry map[string]any) { delete(entry["colors"].(map[string]any), "brightCyan") }, "missing required color"},
		"unknown color":     {color("orange", "#ff8800"), "unknown color"},
		"named color":       {color("red", "red"), "hex"},
		"css function":      {color("red", "rgb(255,0,0)"), "hex"},
		"short hex":         {color("red", "#f00"), "hex"},
		"alpha foreground":  {color("foreground", "#d4d4d480"), "alpha"},
		"low foreground":    {color("foreground", "#2a2a2a"), "contrast of foreground"},
		"hidden cursor":     {color("cursor", "#1f1f1f"), "contrast of cursor"},
		"hidden ansi":       {color("blue", "#1e1e2e"), "contrast of blue"},
		"hidden selection":  {color("selectionBackground", "#d4d4d4"), "selectionBackground"},
		"dim bright black":  {color("brightBlack", "#222222"), "brightBlack"},
		"control title":     {func(entry map[string]any) { entry["title"] = "a\u0007" }, "title"},
		"extra field":       {func(entry map[string]any) { entry["font"] = "x" }, "parse"},
		"colors not object": {func(entry map[string]any) { entry["colors"] = nil }, "colors"},
	}
	for name, testCase := range cases {
		_, err := decodeTerminalColorSchemeForTest(t, testCase.mutate)
		if err == nil || !strings.Contains(err.Error(), testCase.want) {
			t.Errorf("%s: err = %v, want %q", name, err, testCase.want)
		}
	}
}

func TestTerminalColorSchemeLightSkipsBrightBlackFloor(t *testing.T) {
	light := map[string]string{
		"background": "#ffffff", "foreground": "#333333", "cursor": "#000000", "selectionBackground": "#add6ff",
		"black": "#000000", "red": "#cd3131", "green": "#107c10", "yellow": "#949800", "blue": "#0451a5",
		"magenta": "#bc05bc", "cyan": "#0598bc", "white": "#555555", "brightBlack": "#eeeeee",
		"brightRed": "#cd3131", "brightGreen": "#107c10", "brightYellow": "#949800", "brightBlue": "#0451a5",
		"brightMagenta": "#bc05bc", "brightCyan": "#0598bc", "brightWhite": "#a5a5a5",
	}
	_, err := decodeTerminalColorSchemeForTest(t, func(entry map[string]any) {
		colors := map[string]any{}
		for key, value := range light {
			colors[key] = value
		}
		entry["kind"], entry["colors"] = "light", colors
	})
	if err != nil {
		t.Fatalf("light scheme: %v", err)
	}
}
