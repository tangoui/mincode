package e2e

import (
	"runtime"
	"testing"
)

func TestE2EEmbeddedBrowserMountsInMainWindow(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("requires native embedded browser support")
	}
	app := launchMinCode(t)
	win := app.Window("MinCode")
	win.WaitJS(`!!window.tango`)
	var result string
	win.EvalInto(`window.tango.call("mincode.invoke", {command: "embedded_browser_mount", args: {payload: {tabId: "e2e-browser-host", url: "about:blank", x: 100, y: 100, width: 320, height: 240}}}).then(() => "mounted", e => String(e?.message || e))`, &result)
	if result != "mounted" {
		t.Fatalf("mount native embedded browser: %s", result)
	}
	var exists bool
	win.EvalInto(`window.tango.call("mincode.invoke", {command: "embedded_browser_exists", args: {}})`, &exists)
	if !exists {
		t.Fatal("native browser overlay is not mounted")
	}
}
