package e2e

import (
	"crypto/rand"
	"encoding/hex"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tangoui/tango/automation"
)

// TestE2EAppBoots 驱动真实的 MinCode 桌面应用（开发构建，窗口不激活、不抢前台），
// 在页面里检查：前端在 tango 运行时上完成挂载、后端命令往返、页面事件经 Go 广播回来、
// 窗口原生能力、右键菜单策略与拖拽区样式生效。数据目录用临时目录，不碰真实数据。
//
//	TANGO_E2E=1 go test ./e2e -run E2E -v
//
// 应用是单实例的：已有 mincode-tango-web 在运行时，被测进程会直接退出。
func TestE2EAppBoots(t *testing.T) {
	app := launchMinCode(t)
	win := app.Window("MinCode")

	// 前端挂载：主框架和活动栏都已渲染（启动期间已经跑过大量后端命令）。
	win.WaitJS(`!!window.tango && !!document.querySelector(".app-frame") && !!document.querySelector(".app-activity-bar")`)

	var version string
	win.EvalInto(`window.tango.call("mincode.invoke", {command: "get_app_version", args: {}})`, &version)
	if strings.TrimSpace(version) == "" {
		t.Fatalf("get_app_version returned %q", version)
	}

	// 页面 emit 的事件经 Go 广播回本页（事件下行通道）。
	msg := win.EvalString(`new Promise((resolve) => {
		const off = window.tango.onRaw((m) => { if (m.includes("e2e:ping")) { off(); resolve(m); } });
		window.tango.emit("mincode.emit", {name: "e2e:ping", data: 7});
	})`)
	if !strings.HasPrefix(msg, "wev:") || !strings.Contains(msg, `"data":7`) {
		t.Fatalf("event round trip %q", msg)
	}

	var size struct{ Width, Height int }
	win.EvalInto(`window.tango.call("mincode.native", {op: "window.size"})`, &size)
	if size.Width <= 0 || size.Height <= 0 {
		t.Fatalf("window.size %+v", size)
	}

	// 生产前端：空白处的默认右键菜单被屏蔽，输入框里保留。
	var menu struct{ Body, Input bool }
	win.EvalInto(`(() => {
		const fire = (el) => { const e = new MouseEvent("contextmenu", {bubbles: true, cancelable: true}); el.dispatchEvent(e); return e.defaultPrevented; };
		const input = document.createElement("input"); document.body.appendChild(input);
		const result = {body: fire(document.body), input: fire(input)}; input.remove(); return result;
	})()`, &menu)
	if !menu.Body || menu.Input {
		t.Fatalf("context menu policy body-prevented=%v input-prevented=%v", menu.Body, menu.Input)
	}

	// 全局快捷键经宿主注册（globalshortcuts.go）：用一个不易冲突的组合键注册再注销；
	// 启动时也不应出现“唤起快捷键不可用”的提示。
	var registered string
	win.EvalInto(`window.tango.call("mincode.invoke", {command: "desktop_set_global_shortcut", args: {enabled: true, accelerator: "CmdOrCtrl+Alt+Shift+F12"}}).then(() => "ok", (e) => String(e && e.message || e))`, &registered)
	if registered != "ok" {
		t.Fatalf("global shortcut registration: %s", registered)
	}
	win.EvalInto(`window.tango.call("mincode.invoke", {command: "desktop_set_global_shortcut", args: {enabled: false, accelerator: "CmdOrCtrl+Alt+Shift+F12"}}).then(() => "ok", (e) => String(e && e.message || e))`, &registered)
	if registered != "ok" {
		t.Fatalf("global shortcut release: %s", registered)
	}
	if strings.Contains(win.EvalString(`document.body.innerText`), "global-shortcut-unavailable") {
		t.Fatal("startup still reports the global shortcut as unavailable")
	}

	assertNoCustomWindowControls(t, win)
	if runtime.GOOS == "darwin" {
		var sharesTitleBar bool
		win.EvalInto(`(() => {
			const row = document.querySelector(".app-frame--unified-titlebar .app-top-tabs");
			const tabs = row?.querySelector(".app-top-tabs__scroll");
			const actions = row?.querySelector(".app-window-actions");
			return !!(row && tabs && actions && row.getBoundingClientRect().top === 0
				&& tabs.getBoundingClientRect().left >= 84
				&& getComputedStyle(tabs).getPropertyValue("--tango-drag").trim() === "drag"
				&& getComputedStyle(actions).getPropertyValue("--tango-drag").trim() === "no-drag");
		})()`, &sharesTitleBar)
		if !sharesTitleBar {
			t.Fatal("macOS tabs do not share the top row with the native window controls")
		}
		var compactTitleBar bool
		win.EvalInto(`(() => {
			const row = document.querySelector(".app-frame--unified-titlebar .app-top-tabs");
			const tabs = row.querySelector(".app-top-tabs__scroll");
			const brand = row.querySelector(".app-brand-mark");
			const bounds = row.getBoundingClientRect();
			return bounds.height === 40
				&& tabs.getBoundingClientRect().left - bounds.left === 84
				&& !brand?.getClientRects().length;
		})()`, &compactTitleBar)
		if !compactTitleBar {
			t.Fatal("macOS title bar is not 40px high or still reserves space for the logo")
		}
		assertCompactActivityBar(t, win)
	}

	if dir := os.Getenv("MINCODE_E2E_SHOT"); dir != "" {
		win.SaveScreenshot(filepath.Join(dir, "mincode-e2e.png"))
	}

	var popupOpened bool
	win.EvalInto(`window.tango.call("mincode.invoke", {command: "create_popup_tab_window", args: {
		label: "popup-e2e",
		title: "MinCode E2E Popup",
		tabData: JSON.stringify({popupType: "tab", tab: {id: "e2e-http", view: "http", title: "MinCode E2E Popup"}}),
	}}).then(() => true)`, &popupOpened)
	if !popupOpened {
		t.Fatal("popup window did not open")
	}
	popup := app.Window("MinCode E2E Popup")
	popup.WaitJS(`!!document.querySelector(".popup-window-titlebar .app-notification-button")`)
	assertNoCustomWindowControls(t, popup)

	app.Quit()
}

func assertCompactActivityBar(t *testing.T, win *automation.Window) {
	t.Helper()
	var labeledWidth float64
	win.EvalInto(`(() => {
		const rail = document.querySelector(".app-activity-bar");
		rail.style.transition = "none";
		return rail.getBoundingClientRect().width;
	})()`, &labeledWidth)
	win.ClickSelector(`.app-activity-bar__labels-toggle[aria-pressed="true"]`)
	win.WaitJS(`document.querySelector('.app-frame:not(.app-frame--sidebar-labels) .app-activity-bar__labels-toggle[aria-pressed="false"]')`)
	var layouts []struct{ Scale, Height, Inset, Rail, Button, Navigation float64 }
	win.EvalInto(`(() => {
		const root = document.documentElement;
		const body = document.body;
		const row = document.querySelector(".app-frame--unified-titlebar .app-top-tabs");
		const tabs = row.querySelector(".app-top-tabs__scroll");
		const rail = document.querySelector(".app-activity-bar");
		const navigation = rail.querySelector(".app-activity-bar__nav");
		const button = navigation.querySelector(".app-activity-button");
		const zoom = body.style.zoom;
		const overlayScale = root.style.getPropertyValue("--app-ui-overlay-scale");
		try {
			return [0.8, 1, 1.25].map(scale => {
				body.style.zoom = String(scale);
				root.style.setProperty("--app-ui-overlay-scale", String(1 / scale));
				const bounds = row.getBoundingClientRect();
				return {
					scale,
					height: bounds.height,
					inset: tabs.getBoundingClientRect().left - bounds.left,
					rail: rail.getBoundingClientRect().width,
					button: button.getBoundingClientRect().width,
					navigation: navigation.getBoundingClientRect().width,
				};
			});
		} finally {
			body.style.zoom = zoom;
			root.style.setProperty("--app-ui-overlay-scale", overlayScale);
		}
	})()`, &layouts)
	win.ClickSelector(`.app-activity-bar__labels-toggle[aria-pressed="false"]`)
	win.WaitJS(`document.querySelector('.app-frame--sidebar-labels .app-activity-bar__labels-toggle[aria-pressed="true"]')`)
	win.Eval(`document.querySelector(".app-activity-bar").style.removeProperty("transition")`)
	if labeledWidth != 72 {
		t.Errorf("unexpected labeled activity bar width: %v", labeledWidth)
	}
	for _, layout := range layouts {
		if layout.Height < 39.95 || layout.Height > 40.05 || layout.Inset < 83.95 || layout.Inset > 84.05 ||
			layout.Rail < 55.95 || layout.Rail > 56.05 || layout.Button < 36 || layout.Navigation < layout.Button {
			t.Errorf("unexpected macOS title bar and sidebar layout at %.0f%% zoom: %+v", layout.Scale*100, layout)
		}
	}
}

func assertNoCustomWindowControls(t *testing.T, win *automation.Window) {
	t.Helper()
	var hasCustomControls bool
	win.EvalInto(`!!document.querySelector(".app-window-controls")`, &hasCustomControls)
	if hasCustomControls {
		t.Fatal("desktop window still has custom caption buttons")
	}
}

func TestE2EMacTitleBar(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS native title bar")
	}
	t.Setenv("MINCODE_E2E_TITLEBAR", "1")
	app := launchMinCode(t)
	win := app.Window("MinCode")
	const row = `.app-frame--unified-titlebar .app-top-tabs`
	win.WaitSelector(row)

	type geometry struct {
		ButtonY     [3]float64
		TitleBarTop float64
		ContentTop  float64
		RowY        float64
	}
	readGeometry := func() geometry {
		var layout geometry
		win.EvalInto(`(async () => {
			const native = await window.tango.call("mincode.e2e.titlebar");
			const row = document.querySelector("`+row+`").getBoundingClientRect();
			return { ...native, RowY: native.ContentTop + row.top + row.height / 2 };
		})()`, &layout)
		return layout
	}
	assertCenters := func(phase string) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for {
			layout := readGeometry()
			aligned := true
			for _, y := range layout.ButtonY {
				aligned = aligned && math.Abs(y-layout.RowY) <= 0.5
			}
			if aligned || time.Now().After(deadline) {
				t.Logf("%s: traffic lights y=%v pt, tabs y=%.2f pt", phase, layout.ButtonY, layout.RowY)
				if !aligned {
					t.Errorf("%s: traffic lights are not vertically centered on the tab row", phase)
				}
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	var toolbarOK bool
	win.EvalInto(`(() => {
		const bar = document.querySelector("`+row+`");
		const button = bar?.querySelector(".app-window-actions button:not(.app-header-selector):has(.app-header-chevron)");
		if (!button) return false;
		const style = getComputedStyle(button);
		return Math.abs(bar.getBoundingClientRect().height - 40) < 0.05
			&& parseFloat(style.paddingLeft) >= 6 && parseFloat(style.paddingRight) >= 6;
	})()`, &toolbarOK)
	if !toolbarOK {
		t.Error("macOS title bar must be 40px high and give the new-tab menu horizontal padding")
	}

	assertCenters("unified title bar")
	// Reproduce a native host running against the older 32px frontend assets.
	win.Eval(`document.querySelector(".app-frame--unified-titlebar").style.setProperty("--app-top-tabs-height", "32px")`)
	assertCenters("32px title bar")
	win.Eval(`document.querySelector(".app-frame--unified-titlebar").style.removeProperty("--app-top-tabs-height")`)
	win.Eval(`window.tango.call("mincode.native", { op: "window.setSize", args: { width: 1080, height: 740 } })`)
	assertCenters("resized unified title bar")

	win.ClickSelector(".app-activity-bar__bottom > button:last-child")
	win.WaitJS(`(() => {
		const overlay = document.querySelector(".el-overlay:has(.settings-dialog)");
		return !!overlay && overlay.contains(document.elementFromPoint(innerWidth / 2, 1));
	})()`)
	layout := readGeometry()
	t.Logf("settings open: native title bar top=%.2f pt, web content top=%.2f pt", layout.TitleBarTop, layout.ContentTop)
	// Moving the native title bar container also moves its window-edge decoration
	// into the WebView, where it is drawn on top of the settings overlay.
	if math.Abs(layout.TitleBarTop) > 0.5 || math.Abs(layout.ContentTop) > 0.5 {
		t.Errorf("native title bar and content must both start at the window edge: %+v", layout)
	}
	app.Quit()
}

// TestE2EOpensDeepLinkFromLaunch：应用由 mincode://workspace?view=http 链接启动，链接在前端
// 挂载前到达，排队到前端就绪后仍切到 HTTP 工作区。
func TestE2EOpensDeepLinkFromLaunch(t *testing.T) {
	app := launchMinCode(t, "mincode://workspace?view=http")
	win := app.Window("MinCode")
	waitOrDump(t, app, win, `[...document.querySelectorAll(".app-top-tabs .el-tabs__item.is-active")].some((n) => n.innerText.includes("HTTP"))`)
	app.Quit()
}

// waitOrDump 等 cond 在页面里成立；超时时打印页面现场（路由、正文开头、通知）和应用日志，
// 便于判断事件卡在宿主、后端还是前端。
func waitOrDump(t *testing.T, app *automation.App, win *automation.Window, cond string) {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		var ok bool
		win.EvalInto("!!("+cond+")", &ok)
		if ok {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	if dir := os.Getenv("MINCODE_E2E_SHOT"); dir != "" {
		win.SaveScreenshot(filepath.Join(dir, t.Name()+".png"))
	}
	state := win.EvalString(`JSON.stringify({
		activeTabs: [...document.querySelectorAll("[class*=tab][class*=active], [aria-selected=true]")].map((n) => n.innerText.trim()).filter(Boolean).slice(0, 8),
		text: document.body.innerText.slice(0, 800),
		notices: [...document.querySelectorAll(".el-notification, .el-message, [class*=notification-item]")].map((n) => n.innerText).slice(0, 10),
	})`)
	out := app.Output()
	if len(out) > 4000 {
		out = out[len(out)-4000:]
	}
	t.Fatalf("condition not met: %s\npage: %s\napp output (tail):\n%s", cond, state, out)
}

// launchMinCode 以开发构建启动 MinCode（窗口不激活），每次用一套独立身份：临时数据目录、
// 随机应用名与 ID。WebView 本地存储（~/Library/WebKit/<应用名>）因此是空的，不会带入
// 平时运行或上一次测试留下的界面状态，单实例锁也不会和正在运行的 MinCode 冲突。
func launchMinCode(t *testing.T, args ...string) *automation.App {
	t.Helper()
	var buf [6]byte
	if _, err := rand.Read(buf[:]); err != nil {
		t.Fatal(err)
	}
	suffix := hex.EncodeToString(buf[:])
	name := "MinCodeE2E-tango-" + suffix
	if home, err := os.UserHomeDir(); err == nil {
		t.Cleanup(func() {
			for _, dir := range []string{"WebKit", "Caches", "HTTPStorages"} {
				_ = os.RemoveAll(filepath.Join(home, "Library", dir, name))
			}
		})
	}
	app := automation.LaunchOptions(t, "..", automation.Options{
		BuildFlags: []string{"-tags", "nocoredrivers"},
		Env: []string{
			"MINCODE_DATA_DIR=" + t.TempDir(),
			"MINCODE_APP_NAME=" + name,
			"MINCODE_APP_ID=com.mincode.desktop.e2e." + suffix,
		},
		Args:         args,
		StartTimeout: 3 * time.Minute,
	})
	app.SetTimeout(60 * time.Second)
	return app
}
