//go:build windows

package browser

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWindowsBrowserHRESULT(t *testing.T) {
	for _, hr := range []uintptr{0, 1, 0x7fffffff} {
		if err := browserHRESULT(hr); err != nil {
			t.Fatalf("successful HRESULT %x: %v", hr, err)
		}
	}
	if err := browserHRESULT(0x80004005); err == nil || !strings.Contains(err.Error(), "80004005") {
		t.Fatalf("failed HRESULT: %v", err)
	}
}
func TestWindowsBrowserProcessGoneClassification(t *testing.T) {
	for _, hr := range []uintptr{0x8007139F, 0x80010108, 0x800706BA} {
		if !browserProcessGone(browserHRESULT(hr)) {
			t.Fatalf("HRESULT %x not classified as a dead browser process", hr)
		}
		if !browserProcessGone(fmt.Errorf("wrapped: %w", browserHRESULT(hr))) {
			t.Fatalf("wrapped HRESULT %x not classified", hr)
		}
	}
	for _, err := range []error{nil, browserHRESULT(0x80004005), errors.New("WebView2 HRESULT 0x8007139F")} {
		if browserProcessGone(err) {
			t.Fatalf("misclassified %v", err)
		}
	}
}
func TestWindowsBrowserProcessExitKeepsFailedTabs(t *testing.T) {
	// 无原生对象的纯状态测试：旧代 tab 标记失效并保留条目，其他代 tab 不受影响。
	old := &windowsBrowserTab{id: "old", generation: 1, url: "https://example.test/"}
	current := &windowsBrowserTab{id: "current", generation: 2}
	r := &windowsBrowserRuntime{generation: 2, hosts: map[uintptr]*windowsBrowserHost{1: {tabs: map[string]*windowsBrowserTab{"old": old, "current": current}}}}
	r.browserProcessExited(1)
	if old.failure != nil || current.failure != nil {
		t.Fatal("stale generation notification changed tabs")
	}
	r.generation = 1
	r.environment = nil
	r.browserProcessExited(1)
	if old.failure != nil {
		t.Fatal("notification without a live environment changed tabs")
	}
	if _, err := r.liveTab(1, "missing"); err == nil {
		t.Fatal("missing tab reported live")
	}
	old.failure = errBrowserProcessExited
	if _, err := r.liveTab(1, "old"); !errors.Is(err, errBrowserProcessExited) {
		t.Fatalf("failed tab error = %v", err)
	}
	if err := r.nativeFailed(current, errors.New("other")); err == nil || errors.Is(err, errBrowserProcessExited) {
		t.Fatalf("unrelated error was rewritten: %v", err)
	}
}
func TestWindowsBrowserCallbackLifetime(t *testing.T) {
	cleaned := 0
	cb := newBrowserCallback(iidBrowserScriptCompleted, false, func(a uintptr, b unsafe.Pointer) uintptr { return a + uintptr(b) })
	cb.cleanup = func() { cleaned++ }
	id, _ := windows.GUIDFromString(iidBrowserScriptCompleted)
	var out uintptr
	hr, _, _ := syscall.SyscallN(cb.vtable[0], cb.pointer(), uintptr(unsafe.Pointer(&id)), uintptr(unsafe.Pointer(&out)))
	if hr != 0 || out != cb.pointer() || cb.refs.Load() != 2 {
		t.Fatal("QueryInterface did not retain the requested callback")
	}
	value, _, _ := syscall.SyscallN(cb.vtable[3], cb.pointer(), 4, 5)
	if value != 9 {
		t.Fatalf("callback arguments changed: %d", value)
	}
	if cb.release() != 1 || cleaned != 0 {
		t.Fatal("callback released prematurely")
	}
	if cb.release() != 0 || cleaned != 1 {
		t.Fatal("callback cleanup did not run exactly once")
	}
	browserCallbacks.Lock()
	_, live := browserCallbacks.live[cb]
	browserCallbacks.Unlock()
	if live {
		t.Fatal("released callback is still rooted")
	}
}
func TestWindowsBrowserBoundsValidation(t *testing.T) {
	for _, values := range [][4]float64{{0, 0, 0, 1}, {-1, 0, 1, 1}, {0, 0, math.NaN(), 1}, {0, 0, 1, math.Inf(1)}, {0, 0, 1e8, 1}} {
		if _, err := browserBounds(0, values[0], values[1], values[2], values[3]); err == nil {
			t.Fatalf("accepted invalid bounds %v", values)
		}
	}
	rect, err := browserBounds(0, 10, 20, 300, 200)
	if err != nil || rect != (browserRect{10, 20, 310, 220}) {
		t.Fatalf("unexpected fallback DPI geometry: %+v %v", rect, err)
	}
}
func TestWindowsBrowserNavigationPolicy(t *testing.T) {
	for _, url := range []string{"about:blank", "https://example.test/", "http://127.0.0.1:8080/"} {
		if !browserNavigationAllowed(url) {
			t.Fatalf("rejected %q", url)
		}
	}
	for _, url := range []string{"file:///C:/secret", "javascript:alert(1)", "mincode://bridge", "https://0.0.0.0/", "https://169.254.169.254/", "about:config"} {
		if browserNavigationAllowed(url) {
			t.Fatalf("accepted %q", url)
		}
	}
}
func TestWindowsBrowserMissingTabContract(t *testing.T) {
	if err := platformMount(nil, "tab", "https://example.test/", "", 0, 0, 200, 100); err == nil {
		t.Fatal("mount accepted a missing host")
	}
	for _, raw := range []string{platformEval(nil, "missing", "1", 1000), platformCapture(nil, "missing", 1000)} {
		var result evalResult
		if err := json.Unmarshal([]byte(raw), &result); err != nil || result.OK || result.Error == "" {
			t.Fatalf("missing tab returned success or malformed JSON: %s %v", raw, err)
		}
	}
	for _, err := range []error{platformNavigateResult(nil, "missing", "https://example.test/"), platformActivateResult(nil, "missing"), platformSetBoundsResult(nil, "missing", 0, 0, 100, 100, true)} {
		if err == nil {
			t.Fatal("missing tab mutation reported success")
		}
	}
	if platformExists(nil) {
		t.Fatal("missing host exists")
	}
	if err := platformClearDataResult(nil); err == nil {
		t.Fatal("clear data reported success without a profile")
	}
}
func TestWindowsBrowserDispatchNeverWaitsOnSTA(t *testing.T) {
	value, err := browserDispatch(time.Second, func(_ *windowsBrowserRuntime, done func(string)) {
		_, nestedErr := browserDispatch(time.Second, func(_ *windowsBrowserRuntime, _ func(string)) { t.Error("nested dispatch unexpectedly executed") })
		if nestedErr == nil {
			done("missing error")
		} else {
			done(nestedErr.Error())
		}
	})
	if err != nil || !strings.Contains(value, "STA thread") {
		t.Fatalf("nested STA wait not rejected: %q %v", value, err)
	}
}
func TestWindowsBrowserFailureSerialization(t *testing.T) {
	var result evalResult
	if err := json.Unmarshal([]byte(browserFailure(errors.New("failure \"quoted\"\nline"))), &result); err != nil || result.OK || result.Error != "failure \"quoted\"\nline" {
		t.Fatalf("invalid failure JSON: %+v %v", result, err)
	}
	if browserTimeout(0) != 3*time.Second || browserTimeout(40000) != 30*time.Second || browserTabID("") != browserDefaultTab {
		t.Fatal("default or bounded inputs changed")
	}
	script := browserEvalScript("\";throw new Error('injected')//")
	if !strings.Contains(script, `(0,eval)("\";throw new Error('injected')//")`) {
		t.Fatalf("script was not JSON quoted: %s", script)
	}
}
func TestWindowsBrowserDispatcherDoesNotReenterWork(t *testing.T) {
	// An HWND callback may be invoked by a nested COM pump. Queued browser
	// operations must remain pending until the enclosing work has unwound.
	result, err := browserDispatch(time.Second, func(r *windowsBrowserRuntime, done func(string)) {
		running := true
		if err := r.post(func() {
			if running {
				done("reentered")
			} else {
				done("outer loop")
			}
		}); err != nil {
			done(err.Error())
			return
		}
		_, _, _ = syscall.SyscallN(browserDispatcherProcedure, r.dispatcher, browserWorkMessage, 0, 0)
		running = false
	})
	if err != nil || result != "outer loop" {
		t.Fatalf("dispatcher reentered an active operation: %q %v", result, err)
	}
}

func TestWindowsBrowserFinishMountIgnoresCancelledTab(t *testing.T) {
	r := &windowsBrowserRuntime{}
	tab := &windowsBrowserTab{closed: true, mountDone: func(error) { t.Error("cancelled mount completed again") }}
	// A late AddScript completion must not touch released COM objects.
	r.finishMount(tab, nil)
}

func TestWindowsBrowserTabLookupDoesNotAliasUnknownID(t *testing.T) {
	tab := &windowsBrowserTab{id: "active"}
	r := windowsBrowserRuntime{hosts: map[uintptr]*windowsBrowserHost{1: {active: "active", tabs: map[string]*windowsBrowserTab{"active": tab}}}}
	if r.tab(1, "") != tab || r.tab(1, "missing") != nil || r.tab(2, "") != nil {
		t.Fatal("tab lookup unexpectedly aliased another tab")
	}
}
