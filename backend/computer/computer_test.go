package computer

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/png"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestExecuteAIToolUsesValidatedComputerBoundary(t *testing.T) {
	result, err := ExecuteAITool("computer.get_status", nil)
	if err != nil {
		t.Fatalf("get status: %v", err)
	}
	if _, ok := result.(Status); !ok {
		t.Fatalf("expected typed status, got %T", result)
	}

	_, err = ExecuteAITool("computer.execute", map[string]any{
		"actions": []any{map[string]any{"type": "click"}},
	})
	if err == nil || !strings.Contains(err.Error(), "requires x and y") {
		t.Fatalf("expected action validation before desktop execution, got %v", err)
	}

	if _, err := ExecuteAITool("computer.unknown", nil); err == nil {
		t.Fatal("expected unknown computer tool rejection")
	}
}

func TestNormalizeOpenURLArgs(t *testing.T) {
	targetURL, browser, err := normalizeOpenURLArgs(openURLArgs{
		URL:     "https://www.baidu.com",
		Browser: "Google Chrome",
	})
	if err != nil {
		t.Fatalf("normalize valid URL: %v", err)
	}
	if targetURL != "https://www.baidu.com" || browser != "chrome" {
		t.Fatalf("unexpected normalized values: url=%q browser=%q", targetURL, browser)
	}

	for _, args := range []openURLArgs{
		{},
		{URL: "file:///tmp/example", Browser: "chrome"},
		{URL: "https://www.baidu.com", Browser: "unknown"},
	} {
		if _, _, err := normalizeOpenURLArgs(args); err == nil {
			t.Fatalf("expected invalid open URL arguments to fail: %#v", args)
		}
	}
}

func TestNormalizeOpenAppArgs(t *testing.T) {
	for _, application := range []string{"Google Chrome", "微信", "com.apple.TextEdit"} {
		normalized, err := normalizeOpenAppArgs(openAppArgs{Application: "  " + application + "  "})
		if err != nil {
			t.Fatalf("normalize valid application %q: %v", application, err)
		}
		if normalized != application {
			t.Fatalf("unexpected normalized application: got %q want %q", normalized, application)
		}
	}

	for _, application := range []string{"", "/Applications/Safari.app", "../Safari.app", "bad\x00name", string(make([]byte, 513))} {
		if _, err := normalizeOpenAppArgs(openAppArgs{Application: application}); err == nil {
			t.Fatalf("expected invalid application to fail: %q", application)
		}
	}
}

func TestScaledScreenshotSize(t *testing.T) {
	cases := []struct{ w, h, wantW, wantH int }{
		{1280, 800, 1280, 800},
		{1568, 700, 1568, 700},
		{3840, 2160, 1429, 804},
		{1512, 982, 1330, 864},
	}
	for _, c := range cases {
		gotW, gotH := scaledScreenshotSize(c.w, c.h)
		if gotW != c.wantW || gotH != c.wantH {
			t.Fatalf("scaledScreenshotSize(%d,%d) = %dx%d, want %dx%d", c.w, c.h, gotW, gotH, c.wantW, c.wantH)
		}
		if gotW > maxScreenshotLongEdge || gotH > maxScreenshotLongEdge || gotW*gotH > maxScreenshotPixels {
			t.Fatalf("scaled size %dx%d exceeds model limits", gotW, gotH)
		}
	}
}

func TestFitCaptureForModelKeepsCoordinateFrame(t *testing.T) {
	source := image.NewRGBA(image.Rect(0, 0, 3000, 2000))
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, source); err != nil {
		t.Fatal(err)
	}
	capture := Capture{
		MimeType: "image/png",
		Data:     base64.StdEncoding.EncodeToString(encoded.Bytes()),
		Width:    3000,
		Height:   2000,
		Display:  DisplayInfo{PointWidth: 1500, PointHeight: 1000, PixelWidth: 3000, PixelHeight: 2000, ScaleX: 2, ScaleY: 2},
	}
	fitted, err := fitCaptureForModel(capture)
	if err != nil {
		t.Fatalf("fit capture: %v", err)
	}
	raw, _ := base64.StdEncoding.DecodeString(fitted.Data)
	decoded, err := png.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decode fitted capture: %v", err)
	}
	if decoded.Width != fitted.Width || decoded.Height != fitted.Height {
		t.Fatalf("image %dx%d does not match reported %dx%d", decoded.Width, decoded.Height, fitted.Width, fitted.Height)
	}
	if fitted.Display.PixelWidth != fitted.Width || fitted.Display.PixelHeight != fitted.Height || fitted.Width >= 3000 {
		t.Fatalf("coordinate frame not updated: %+v", fitted.Display)
	}
	if want := float64(fitted.Width) / 1500; fitted.Display.ScaleX != want {
		t.Fatalf("scaleX = %v, want %v", fitted.Display.ScaleX, want)
	}
}

func TestValidateActionAcceptsTripleClick(t *testing.T) {
	x, y := 10.0, 20.0
	if err := validateAction(&Action{Type: "triple_click", X: &x, Y: &y}); err != nil {
		t.Fatalf("triple_click should be valid: %v", err)
	}
	if err := validateAction(&Action{Type: "triple_click"}); err == nil || !strings.Contains(err.Error(), "requires x and y") {
		t.Fatalf("expected coordinate requirement, got %v", err)
	}
}

func TestSplitAndParseKeyChord(t *testing.T) {
	cases := []struct {
		raw       string
		parts     []string
		key       string
		modifiers uint32
	}{
		{"+", []string{"+"}, "+", 0},
		{"PLUS", []string{"+"}, "+", 0},
		{"ctrl++", []string{"CTRL", "+"}, "+", 1},
		{"cmd+shift+a", []string{"META", "SHIFT", "a"}, "a", 10},
		{"Option+Enter", []string{"ALT", "ENTER"}, "ENTER", 4},
		{"win+r", []string{"META", "r"}, "r", 8},
	}
	for _, tc := range cases {
		parts, err := splitKeyChord(tc.raw)
		if err != nil || !reflect.DeepEqual(parts, tc.parts) {
			t.Fatalf("splitKeyChord(%q) = %v, %v; want %v", tc.raw, parts, err, tc.parts)
		}
		key, modifiers, err := parseKeyChord(tc.raw, nil)
		if err != nil || key != tc.key || modifiers != tc.modifiers {
			t.Fatalf("parseKeyChord(%q) = %q, %d, %v; want %q, %d", tc.raw, key, modifiers, err, tc.key, tc.modifiers)
		}
	}
	for _, raw := range []string{"", "ctrl+", "+a", "ctrl++shift"} {
		if _, err := splitKeyChord(raw); err == nil {
			t.Fatalf("splitKeyChord(%q) should fail", raw)
		}
	}
	if _, _, err := parseKeyChord("hyper+a", nil); err == nil {
		t.Fatal("unknown modifier should fail")
	}
}

func TestValidateDesktopActions(t *testing.T) {
	x, y := 1.0, 2.0
	display := uint32(2)
	valid := []Action{
		{Type: "key_down", Keys: []string{"shift"}},
		{Type: "key_up", Keys: []string{"ctrl+alt"}},
		{Type: "cursor_position"},
		{Type: "screenshot", Display: &display},
	}
	for _, action := range valid {
		if err := validateAction(&action); err != nil {
			t.Fatalf("%s should be valid: %v", action.Type, err)
		}
	}
	middle := Action{Type: "click", X: &x, Y: &y, Button: "Middle"}
	if err := validateAction(&middle); err != nil || middle.Button != "wheel" {
		t.Fatalf("middle should map to wheel, got %q, %v", middle.Button, err)
	}
	invalid := []Action{
		{Type: "click", X: &x, Y: &y, Display: &display},
		{Type: "key_down"},
		{Type: "key_up", Keys: []string{"ctrl+"}},
		{Type: "click", X: &x, Y: &y, Button: "back"},
	}
	for _, action := range invalid {
		if err := validateAction(&action); err == nil {
			t.Fatalf("%+v should be rejected", action)
		}
	}
}

func TestScreenshotCoordinatesInvertsScreenshotPoint(t *testing.T) {
	geometry := captureGeometry{Display: DisplayInfo{
		OriginX: -1440, OriginY: 0, PointWidth: 1440, PointHeight: 900, PixelWidth: 1280, PixelHeight: 800,
	}}
	x, y, onScreen := screenshotCoordinates(geometry, Point{X: -720, Y: 450})
	if x != 640 || y != 400 || !onScreen {
		t.Fatalf("got %v,%v,%v; want 640,400,true", x, y, onScreen)
	}
	if _, _, onScreen := screenshotCoordinates(geometry, Point{X: 10, Y: 10}); onScreen {
		t.Fatal("point on another display should be off-screen")
	}
}

func TestPartialFailure(t *testing.T) {
	cause := errors.New("boom")
	if err := partialFailure(nil, cause); err != cause {
		t.Fatalf("empty results should return cause unchanged, got %v", err)
	}
	err := partialFailure([]map[string]any{{"type": "click"}}, cause)
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "1 action(s) completed") || !strings.Contains(err.Error(), `"click"`) {
		t.Fatalf("unexpected partial failure: %v", err)
	}
}

func TestFinalCaptureBudget(t *testing.T) {
	if got := finalCaptureBudget(time.Now().Add(-time.Second)); got != minFinalCaptureBudget {
		t.Fatalf("expired deadline budget = %v", got)
	}
	if got := finalCaptureBudget(time.Now().Add(time.Minute)); got != defaultCaptureTimeout {
		t.Fatalf("long deadline budget = %v", got)
	}
	if got := finalCaptureBudget(time.Now().Add(5 * time.Second)); got < 4*time.Second || got > 5*time.Second {
		t.Fatalf("mid deadline budget = %v", got)
	}
}

func TestOpenSystemBrowserValidatesAndUsesDefaultBrowser(t *testing.T) {
	previous := systemBrowserOpener
	t.Cleanup(func() { systemBrowserOpener = previous })
	var opened []string
	systemBrowserOpener = func(targetURL, browser string) error {
		opened = append(opened, targetURL+" "+browser)
		return nil
	}

	for _, raw := range []string{"", "file:///etc/passwd", "javascript:alert(1)", "mailto:a@b.c", "/relative/path", "https://" + strings.Repeat("a", 4100)} {
		if err := OpenSystemBrowser(raw); err == nil {
			t.Fatalf("OpenSystemBrowser(%.40q) must be rejected", raw)
		}
	}
	if len(opened) != 0 {
		t.Fatalf("rejected URLs reached the opener: %v", opened)
	}

	if !platformStatus().Supported {
		if err := OpenSystemBrowser("https://example.com/authorize"); err == nil {
			t.Fatal("unsupported platform must report an error")
		}
		return
	}
	if err := OpenSystemBrowser(" https://example.com/authorize?state=x "); err != nil {
		t.Fatal(err)
	}
	if want := []string{"https://example.com/authorize?state=x default"}; !reflect.DeepEqual(opened, want) {
		t.Fatalf("opened = %v, want %v", opened, want)
	}
}
