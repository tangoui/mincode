package browser

import (
	"strings"
	"testing"
)

func TestInternalMCPToolsIncludeDesktopComputerUse(t *testing.T) {
	definitions := InternalMCPToolDefinitions()
	byName := make(map[string]InternalMCPToolDefinition, len(definitions))
	for _, definition := range definitions {
		byName[definition.Name] = definition
	}
	for _, name := range []string{
		"computer.get_status",
		"computer.request_permissions",
		"computer.capture_screen",
		"computer.open_app",
		"computer.open_url",
		"computer.execute",
	} {
		if _, ok := byName[name]; !ok {
			t.Fatalf("missing internal MCP tool %q", name)
		}
	}
	if isInternalMCPTool("browser.evaluate") || isInternalMCPTool("computer.shell") {
		t.Fatal("unexpected dangerous tool in private MCP allowlist")
	}
	capture := byName["computer.capture_screen"]
	if capture.Annotations["readOnlyHint"] != false {
		t.Fatalf("computer.capture_screen is risk dangerous natively and must not be advertised as read-only: %#v", capture.Annotations)
	}
	execute := byName["computer.execute"]
	if execute.Annotations["readOnlyHint"] != false || execute.Annotations["destructiveHint"] != true || execute.InputSchema["required"] == nil {
		t.Fatalf("computer.execute is missing mutation annotations or required actions schema: %#v", execute)
	}
	openApp := byName["computer.open_app"]
	if openApp.Annotations["openWorldHint"] != true || openApp.InputSchema["required"] == nil {
		t.Fatalf("computer.open_app is missing open-world annotations or required application schema: %#v", openApp)
	}
	openURL := byName["computer.open_url"]
	if openURL.Annotations["openWorldHint"] != true || openURL.InputSchema["required"] == nil {
		t.Fatalf("computer.open_url is missing open-world annotations or required URL schema: %#v", openURL)
	}
}

func TestInternalMCPToolsIncludeHandleDialog(t *testing.T) {
	for _, definition := range InternalMCPToolDefinitions() {
		if definition.Name != "browser.handle_dialog" {
			continue
		}
		properties, _ := definition.InputSchema["properties"].(map[string]any)
		if properties["accept"] == nil || properties["promptText"] == nil || properties["dialogId"] == nil {
			t.Fatalf("browser.handle_dialog schema must expose accept, promptText and dialogId: %#v", definition.InputSchema)
		}
		if required, _ := definition.InputSchema["required"].([]string); len(required) != 1 || required[0] != "accept" {
			t.Fatalf("browser.handle_dialog must require accept: %#v", definition.InputSchema)
		}
		return
	}
	t.Fatal("missing internal MCP tool browser.handle_dialog")
}

func TestInternalMCPToolsIncludeSemanticBrowserTools(t *testing.T) {
	want := map[string][]string{
		"browser.go_back":       nil,
		"browser.go_forward":    nil,
		"browser.select_option": {"snapshotId", "ref"},
		"browser.set_checked":   {"snapshotId", "ref", "checked"},
		"browser.hover":         {"snapshotId", "ref"},
		"browser.scroll":        nil,
	}
	found := map[string]bool{}
	for _, definition := range InternalMCPToolDefinitions() {
		required, ok := want[definition.Name]
		if !ok {
			continue
		}
		found[definition.Name] = true
		got, _ := definition.InputSchema["required"].([]string)
		if strings.Join(got, ",") != strings.Join(required, ",") {
			t.Fatalf("%s must require %v, got %v", definition.Name, required, got)
		}
		if !isInternalMCPTool(definition.Name) {
			t.Fatalf("%s must be callable through MCP", definition.Name)
		}
	}
	for name := range want {
		if !found[name] {
			t.Fatalf("missing internal MCP tool %s", name)
		}
	}
	scroll := map[string]any{}
	for _, definition := range InternalMCPToolDefinitions() {
		if definition.Name == "browser.scroll" {
			scroll, _ = definition.InputSchema["properties"].(map[string]any)
		}
	}
	for _, key := range []string{"snapshotId", "ref", "direction", "amount", "containerRef"} {
		if scroll[key] == nil {
			t.Fatalf("browser.scroll schema must expose %s: %v", key, scroll)
		}
	}
	if amount, _ := scroll["amount"].(map[string]any); amount["type"] != "number" || amount["anyOf"] != nil {
		t.Fatalf("browser.scroll amount must be a plain number schema: %v", scroll["amount"])
	}
	for _, definition := range InternalMCPToolDefinitions() {
		if definition.Name == "browser.hover" && !strings.Contains(definition.Description, "browser.get_page_snapshot") {
			t.Fatalf("browser.hover must tell the AI to take a fresh snapshot: %q", definition.Description)
		}
	}
}

func TestInternalMCPToolsIncludeTabListAndSwitch(t *testing.T) {
	byName := map[string]InternalMCPToolDefinition{}
	for _, definition := range InternalMCPToolDefinitions() {
		byName[definition.Name] = definition
	}
	list, ok := byName["browser.list_tabs"]
	if !ok || !isInternalMCPTool("browser.list_tabs") {
		t.Fatal("missing internal MCP tool browser.list_tabs")
	}
	if required, _ := list.InputSchema["required"].([]string); len(required) != 0 {
		t.Fatalf("browser.list_tabs takes no arguments: %#v", list.InputSchema)
	}
	switchTab, ok := byName["browser.switch_tab"]
	if !ok || !isInternalMCPTool("browser.switch_tab") {
		t.Fatal("missing internal MCP tool browser.switch_tab")
	}
	if required, _ := switchTab.InputSchema["required"].([]string); strings.Join(required, ",") != "tabId" {
		t.Fatalf("browser.switch_tab must require tabId: %#v", switchTab.InputSchema)
	}
	if !strings.Contains(switchTab.Description, "browser.list_tabs") {
		t.Fatalf("browser.switch_tab must point at browser.list_tabs for tab ids: %q", switchTab.Description)
	}
	handle := byName["browser.handle_dialog"]
	for _, want := range []string{"dialogId", "dialog_pending", "browser.switch_tab"} {
		if !strings.Contains(handle.Description, want) {
			t.Fatalf("browser.handle_dialog description must mention %s: %q", want, handle.Description)
		}
	}
}

func TestInternalMCPToolsIncludeExtractContent(t *testing.T) {
	for _, definition := range InternalMCPToolDefinitions() {
		if definition.Name != "browser.extract_content" {
			continue
		}
		properties, _ := definition.InputSchema["properties"].(map[string]any)
		format, _ := properties["format"].(map[string]any)
		if enum, _ := format["enum"].([]string); strings.Join(enum, ",") != "markdown,text" {
			t.Fatalf("browser.extract_content format must be markdown or text: %#v", format)
		}
		maxChars, _ := properties["maxChars"].(map[string]any)
		if maxChars["minimum"] != 1000 || maxChars["maximum"] != 100000 {
			t.Fatalf("browser.extract_content maxChars must range 1000-100000: %#v", maxChars)
		}
		if properties["snapshotId"] == nil || properties["ref"] == nil || definition.InputSchema["required"] != nil {
			t.Fatalf("browser.extract_content takes optional snapshotId and ref: %#v", definition.InputSchema)
		}
		if !strings.Contains(definition.Description, "untrusted") {
			t.Fatalf("browser.extract_content must mark the content as untrusted page text: %q", definition.Description)
		}
		return
	}
	t.Fatal("missing internal MCP tool browser.extract_content")
}
