package browser

import (
	"math"
	"strings"
	"testing"
)

func TestBrowserMountRejectsUnsafeURLsBeforeNativeCalls(t *testing.T) {
	for _, raw := range []string{"file:///C:/Windows/win.ini", "javascript:alert(1)", "data:text/html,hello", "https://0.0.0.0/", "http://169.254.169.254/"} {
		if err := validateBrowserMountURL(raw); err == nil {
			t.Errorf("unsafe mount URL accepted: %q", raw)
		}
	}
	for _, raw := range []string{"", "about:blank", "https://example.com/", "http://127.0.0.1:5150/"} {
		if err := validateBrowserMountURL(raw); err != nil {
			t.Errorf("valid mount URL rejected: %q: %v", raw, err)
		}
	}
}

func TestBrowserBoundsValidation(t *testing.T) {
	for _, bounds := range [][4]float64{{0, 0, -1, 100}, {math.NaN(), 0, 1, 1}, {0, math.Inf(1), 1, 1}, {0, 0, 40000, 1}} {
		if err := validateBrowserBounds(bounds[0], bounds[1], bounds[2], bounds[3]); err == nil {
			t.Errorf("invalid bounds accepted: %v", bounds)
		}
	}
	if err := validateBrowserBounds(-10, 12, 0, 800); err != nil {
		t.Fatal(err)
	}
}

func TestBrowserNavigationFailureMessage(t *testing.T) {
	if err := browserNavigationFailure(`[{"message":"DNS failed"},{"message":"Connection refused"}]`); err == nil || !strings.Contains(err.Error(), "Connection refused") {
		t.Fatalf("expected current navigation error, got %v", err)
	}
	for _, raw := range []string{"[]", "null", "invalid", `[{"message":""}]`} {
		if err := browserNavigationFailure(raw); err != nil {
			t.Fatalf("unexpected navigation error for %s: %v", raw, err)
		}
	}
}

func TestPreferNativeURLSkipsEvalWhenNativeAvailable(t *testing.T) {
	evaluated := 0
	eval := func() string { evaluated++; return "https://document.example/" }
	if got := preferNativeURL(func() string { return " https://native.example/ " }, eval); got != "https://native.example/" {
		t.Fatalf("got %q, want native URL", got)
	}
	if evaluated != 0 {
		t.Fatal("JS evaluation ran although the native URL was available")
	}
	if got := preferNativeURL(func() string { return "" }, eval); got != "https://document.example/" {
		t.Fatalf("got %q, want evaluated fallback", got)
	}
}

func TestSupportedReportsPlatformAvailability(t *testing.T) {
	got, err := handleSupported("", "")
	if err != nil {
		t.Fatal(err)
	}
	want := "false"
	if platformAvailable() {
		want = "true"
	}
	if got != want {
		t.Fatalf("embedded_browser_supported = %s, want %s", got, want)
	}
}
