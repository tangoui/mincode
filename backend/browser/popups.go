package browser

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"

	"mincode/app/backend/core"
)

// Page-opened windows (target=_blank, window.open). On macOS the overlay
// returns a child web view built from WebKit's configuration so window.opener
// keeps working, and registers it as a new tab that the front end adopts. The
// functions here are called on the main thread from native callbacks, so they
// only take the popup-window lock and the dialogs lock, then emit events.

const (
	popupOpenedEvent = "browser:popup-opened"
	tabClosedEvent   = "browser:tab-closed"
	popupTabIDPrefix = "browser:popup:"

	// maxPopupTabs caps the page-opened tabs alive at once (enforced by the overlay).
	maxPopupTabs = 10

	// aiPopupWindow is how long after an AI click or real user input the tab may open a window.
	aiPopupWindow = time.Second
)

type popupOpened struct {
	TabID       string `json:"tabId"`
	OpenerTabID string `json:"openerTabId"`
	URL         string `json:"url"`
}

type tabClosed struct {
	TabID  string `json:"tabId"`
	Reason string `json:"reason"`
}

// now is the clock for the popup allowances; tests replace it.
var now = time.Now

// Popup allowances. WKWebView runs every injected script with a user gesture,
// so WebKit's own gesture check cannot tell an AI click from a page abusing a
// read-only tool's script. A tab may only open a window during the second after
// an AI click-type action or a real mouse/keyboard event on its web view, and
// each such action admits one window.
var popupAllowances = struct {
	sync.Mutex
	ai    map[string]time.Time
	input map[string]time.Time
}{ai: map[string]time.Time{}, input: map[string]time.Time{}}

// allowAIPopupWindow lets tabID open one window during the next second. It is
// called right before an AI click-type action runs, since the page may open the
// window synchronously.
func allowAIPopupWindow(tabID string) {
	popupAllowances.Lock()
	defer popupAllowances.Unlock()
	popupAllowances.ai[tabID] = now().Add(aiPopupWindow)
}

// noteUserInput records a real mouse or keyboard event on tabID's web view.
// Called on the main thread by the overlay.
// It also opens the tab's file-chooser and save-panel allowances
// (file_chooser.go, downloads.go).
func noteUserInput(tabID string) {
	popupAllowances.Lock()
	popupAllowances.input[tabID] = now().Add(aiPopupWindow)
	popupAllowances.Unlock()
	noteFileChooserInput(tabID)
	noteDownloadInput(tabID)
}

// aiPopupAllowed reports whether tabID's AI click allowance is open, without using it.
func aiPopupAllowed(tabID string) bool {
	popupAllowances.Lock()
	defer popupAllowances.Unlock()
	return allowanceOpenLocked(popupAllowances.ai, tabID)
}

func allowanceOpenLocked(allowances map[string]time.Time, tabID string) bool {
	until, ok := allowances[tabID]
	if ok && !now().Before(until) {
		delete(allowances, tabID)
		return false
	}
	return ok
}

// takePopupAllowance uses up tabID's open allowance, AI click first.
func takePopupAllowance(tabID string) bool {
	popupAllowances.Lock()
	defer popupAllowances.Unlock()
	for _, allowances := range []map[string]time.Time{popupAllowances.ai, popupAllowances.input} {
		if allowanceOpenLocked(allowances, tabID) {
			delete(allowances, tabID)
			return true
		}
	}
	return false
}

func forgetPopupAllowances(tabID string) {
	popupAllowances.Lock()
	defer popupAllowances.Unlock()
	delete(popupAllowances.ai, tabID)
	delete(popupAllowances.input, tabID)
}

func resetAIPopupWindows() {
	popupAllowances.Lock()
	defer popupAllowances.Unlock()
	popupAllowances.ai = map[string]time.Time{}
	popupAllowances.input = map[string]time.Time{}
}

var (
	errPopupAddressNotAllowed = errors.New("Blocked a new window: the address is not allowed")
	errPopupNoUserAction      = errors.New("Blocked a new window: it was not opened by a click or key press")
)

// popupTargetURL validates a page-opened window's address and drops any
// user:password so credentials never reach events. Empty means about:blank.
func popupTargetURL(rawURL string) (string, error) {
	target := strings.TrimSpace(rawURL)
	if target == "" || target == "about:blank" {
		return "about:blank", nil
	}
	if validateNavigationURL(target) != nil {
		return "", errPopupAddressNotAllowed
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return "", errPopupAddressNotAllowed
	}
	parsed.User = nil
	return parsed.String(), nil
}

// admitPopup decides on a window the page in openerTabID asked for. Allowed
// addresses (about:blank included, so scripts can open a window and fill it
// in) are admitted when the tab has an open allowance, which this uses up; the
// new tab is named and the front end told. A refused address keeps the
// allowance. The caller records refusals as navigation errors on the opener.
func admitPopup(openerTabID, rawURL string) (string, error) {
	event, err := preparePopup(openerTabID, rawURL)
	if err != nil {
		return "", err
	}
	core.Emit(popupOpenedEvent, event)
	return event.TabID, nil
}

// Windows must finish creating the child before publishing it: the frontend
// immediately activates a published tab. Keep the allowance decision separate.
func preparePopup(openerTabID, rawURL string) (popupOpened, error) {
	target, err := popupTargetURL(rawURL)
	if err != nil {
		return popupOpened{}, err
	}
	if !takePopupAllowance(openerTabID) {
		return popupOpened{}, errPopupNoUserAction
	}
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return popupOpened{}, errors.New("Blocked a new window: could not name its tab")
	}
	tabID := popupTabIDPrefix + hex.EncodeToString(suffix[:])
	return popupOpened{TabID: tabID, OpenerTabID: openerTabID, URL: target}, nil
}

// computerActionMayOpenPopup reports whether a computer_action step clicks the
// page: a left/middle click or double click, or Enter/Return (which clicks a
// focused link or button).
func computerActionMayOpenPopup(action browserComputerAction) bool {
	switch action.Type {
	case "click", "double_click":
		return action.Button != "right"
	case "keypress":
		for _, raw := range action.Keys {
			parts := strings.Split(raw, "+")
			key := strings.ToUpper(strings.TrimSpace(parts[len(parts)-1]))
			if key == "ENTER" || key == "RETURN" {
				return true
			}
		}
	}
	return false
}

// popupClosed handles a page closing its own window (window.close()).
func popupClosed(tabID string) {
	cancelDialogsForTab(tabID)
	forgetPopupAllowances(tabID)
	forgetFileChooserAllowances(tabID)
	forgetDownloadAllowances(tabID)
	core.Emit(tabClosedEvent, tabClosed{TabID: tabID, Reason: "window.close"})
}
