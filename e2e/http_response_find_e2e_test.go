package e2e

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tangoui/tango/automation"
)

// TestE2EHttpLargeResponseFind：HTTP 响应体是只渲染可见行的视图（VirtualTextViewer）。
// 在真实窗口里收一个约 5 MB 的单行 JSON，⌘F 找屏幕外的内容：查找条打开、计数正确、
// 跳到匹配并高亮、下一个/上一个循环、Esc 关闭后高亮清除；同时检查 DOM 里只有几十行。
//
//	TANGO_E2E=1 go test ./e2e -run HttpLargeResponseFind -v
//
// MINCODE_E2E_SHOT=<目录> 时保存打开查找条后的截图。
func TestE2EHttpLargeResponseFind(t *testing.T) {
	body := largeJSONBody(5_000_000, "tail-marker-e2e")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	url := "http://" + listener.Addr().String() + "/big.json"

	app := launchMinCode(t, "mincode://workspace?view=http")
	win := app.Window("MinCode")
	waitOrDump(t, app, win, `!!document.querySelector(".http-url-input input")`)

	win.Fill(".http-url-input input", url)
	win.Eval(`document.querySelector(".http-url-input input").dispatchEvent(new KeyboardEvent("keyup", {key: "Enter", code: "Enter", bubbles: true}))`)
	waitOrDump(t, app, win, `!!document.querySelector(".http-response-body .app-virtual-text__row")`)

	var rows int
	win.EvalInto(`document.querySelectorAll(".http-response-body .app-virtual-text__row").length`, &rows)
	if rows < 5 || rows > 120 {
		t.Fatalf("rendered rows %d: the viewer should render only the rows in view", rows)
	}

	// ⌘F inside the viewer opens its find bar (not the browser's find). A WebView window takes
	// no synthetic keys over the automation channel: dispatch the keydown the viewer listens to.
	key(win, ".http-response-body .app-virtual-text__viewport", `{key: "f", metaKey: true, ctrlKey: true}`)
	waitOrDump(t, app, win, `!!document.querySelector(".http-response-body .app-virtual-text__search input")`)

	win.Fill(".http-response-body .app-virtual-text__search input", "TAIL-MARKER-E2E")
	key(win, searchInput, `{key: "Enter"}`)
	waitOrDump(t, app, win, `document.querySelector(".app-virtual-text__match.is-current")?.textContent === "tail-marker-e2e"`)
	status := win.EvalString(`document.querySelector(".http-response-body .app-virtual-text__search-status").textContent.trim()`)
	if status != "1/1" && status != "1 of 1" {
		t.Fatalf("find status %q", status)
	}
	var scrolled int
	win.EvalInto(`Math.round(document.querySelector(".http-response-body .app-virtual-text__viewport").scrollTop)`, &scrolled)
	if scrolled < 1000 {
		t.Fatalf("scrollTop %d: the match at the end of the body should be scrolled into view", scrolled)
	}
	// The current match is on screen, not only somewhere in the rendered (overscan) rows.
	var view struct {
		Inside              bool
		MatchTop, ViewTop   float64
		MatchBottom, Bottom float64
	}
	win.EvalInto(`new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => {
		const viewport = document.querySelector(".http-response-body .app-virtual-text__viewport").getBoundingClientRect();
		const match = document.querySelector(".app-virtual-text__match.is-current").getBoundingClientRect();
		resolve({inside: match.top >= viewport.top && match.bottom <= viewport.bottom, matchTop: match.top, viewTop: viewport.top, matchBottom: match.bottom, bottom: viewport.bottom});
	})))`, &view)
	if !view.Inside {
		t.Fatalf("current match is not on screen: %+v", view)
	}
	if dir := os.Getenv("MINCODE_E2E_SHOT"); dir != "" {
		win.SaveScreenshot(filepath.Join(dir, "http-large-response-find.png"))
	}

	// Many matches: the count, then next / previous wrap around.
	win.Fill(".http-response-body .app-virtual-text__search input", `"id":4999`)
	key(win, searchInput, `{key: "Enter"}`)
	waitOrDump(t, app, win, `/^1\D+\d+/.test(document.querySelector(".http-response-body .app-virtual-text__search-status").textContent.trim())`)
	total := win.EvalString(`document.querySelector(".http-response-body .app-virtual-text__search-status").textContent.trim()`)
	key(win, searchInput, `{key: "Enter", shiftKey: true}`)
	last := win.EvalString(`document.querySelector(".http-response-body .app-virtual-text__search-status").textContent.trim()`)
	if !strings.HasPrefix(total, "1") || strings.HasPrefix(last, "1/") || strings.HasPrefix(last, "1 of") {
		t.Fatalf("previous from the first match should wrap to the last: %q then %q", total, last)
	}

	// Escape closes the bar and clears the highlights.
	key(win, searchInput, `{key: "Escape"}`)
	waitOrDump(t, app, win, `!document.querySelector(".http-response-body .app-virtual-text__search") && !document.querySelector(".app-virtual-text__match")`)
	app.Quit()
}

const searchInput = ".http-response-body .app-virtual-text__search input"

// key 在 selector 指向的元素上派发一次 keydown（init 是 KeyboardEventInit 的 JS 字面量）。
func key(win *automation.Window, selector, init string) {
	win.Eval(fmt.Sprintf(`(() => { const el = document.querySelector(%q); el.focus(); el.dispatchEvent(new KeyboardEvent("keydown", {...%s, bubbles: true, cancelable: true})); return true; })()`, selector, init))
}

// largeJSONBody 生成约 size 字节的单行 JSON 数组，最后一项含 marker（屏幕外）。
func largeJSONBody(size int, marker string) []byte {
	var b strings.Builder
	b.Grow(size + 64)
	b.WriteByte('[')
	for i := 0; b.Len() < size; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"id":%d,"name":"item %d","email":"user%d@example.com","tags":["alpha","beta","gamma"]}`, i, i, i)
	}
	fmt.Fprintf(&b, `,{"marker":%q}]`, marker)
	return []byte(b.String())
}
