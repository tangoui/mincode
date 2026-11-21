package e2e

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Hold back the application modules so the document's initial paint is observable.
func TestE2EStartupBackground(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS WebView startup paint")
	}
	source, err := os.ReadFile("../vue-app/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := strings.Replace(string(source), `<script type="module" src="/src/main.ts"></script>`, "", 1)
	if html == string(source) {
		t.Fatal("entry module not found")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(html), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TANGO_FRONTEND_DIST", dir)
	win := launchMinCode(t).Window("MinCode")
	win.WaitSelector("#app")
	checkPaint := func(label string, light bool) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for {
			img, err := png.Decode(bytes.NewReader(win.Screenshot()))
			if err != nil {
				t.Fatal(err)
			}
			b := img.Bounds()
			r, g, blue, _ := img.At(b.Min.X+b.Dx()/2, b.Min.Y+b.Dy()/2).RGBA()
			matches := light && r>>8 >= 235 && g>>8 >= 235 && blue>>8 >= 235 || !light && r>>8 >= 16 && r>>8 <= 55 && g>>8 <= 55 && blue>>8 <= 65
			if matches || time.Now().After(deadline) {
				t.Logf("%s: center RGB %d,%d,%d", label, r>>8, g>>8, blue>>8)
				if !matches {
					t.Errorf("%s: unexpected startup paint", label)
				}
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	checkPaint("startup without saved theme", true)
	win.Eval(`(() => { localStorage.setItem("app_settings_v2", '{"uiTheme":"dark"}'); setTimeout(() => location.reload(), 100); return true; })()`)
	checkPaint("reload with saved dark theme", false)
	win.Eval(`(() => { localStorage.setItem("app_settings_v2", '{"uiTheme":"light"}'); setTimeout(() => location.reload(), 100); return true; })()`)
	checkPaint("reload with saved light theme", true)
}
