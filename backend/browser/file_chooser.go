package browser

import (
	"runtime"
	"sync"
	"time"

	"mincode/app/backend/core"
)

// Page file choosers (<input type=file>). The user always picks the files in a
// native panel; AI can only click a file input to ask for one and never sees or
// passes local paths. On macOS the overlay shows an NSOpenPanel sheet and
// reports it opening and closing from the main thread, so these functions only
// take the file-chooser lock and emit events.

const (
	fileChooserEvent = "browser:file-chooser"

	// fileChooserWaitTimeout is how long an AI click on a file input waits for the chooser.
	fileChooserWaitTimeout = time.Second

	// fileChooserAllowance is how long after an AI click or real user input the tab may open a chooser.
	fileChooserAllowance = time.Second
)

type fileChooserState struct {
	TabID         string `json:"tabId"`
	Open          bool   `json:"open"`
	SelectedCount int    `json:"selectedCount"`
}

// nativeFileChooser reports whether the platform tells Go when a chooser opens.
// Both native overlays report chooser state; unsupported platforms never claim it opened.
var nativeFileChooser = runtime.GOOS == "darwin" || runtime.GOOS == "windows"

var fileChoosers = struct {
	sync.Mutex
	open map[string]bool
	// armed holds the tabs an AI click is waiting on; true once a chooser opened after the arm.
	armed map[string]bool
	// changed is closed and replaced whenever a chooser opens.
	changed chan struct{}
	// Chooser allowances, kept apart from the popup ones (popups.go) so a click
	// that opens a window and a chooser never uses up the other's allowance.
	ai    map[string]time.Time
	input map[string]time.Time
}{open: map[string]bool{}, armed: map[string]bool{}, changed: make(chan struct{}), ai: map[string]time.Time{}, input: map[string]time.Time{}}

func resetFileChoosers() {
	fileChoosers.Lock()
	defer fileChoosers.Unlock()
	fileChoosers.open = map[string]bool{}
	fileChoosers.armed = map[string]bool{}
	fileChoosers.ai = map[string]time.Time{}
	fileChoosers.input = map[string]time.Time{}
}

// allowAIFileChooser lets tabID open one chooser during the next second; called
// right before an AI click. Injected scripts carry a user gesture, so WebKit
// alone would also let a page open choosers from inside a read-only tool.
func allowAIFileChooser(tabID string) {
	fileChoosers.Lock()
	defer fileChoosers.Unlock()
	fileChoosers.ai[tabID] = now().Add(fileChooserAllowance)
}

// noteFileChooserInput records a real mouse or keyboard event on tabID's web
// view (via noteUserInput). Called on the main thread.
func noteFileChooserInput(tabID string) {
	fileChoosers.Lock()
	defer fileChoosers.Unlock()
	fileChoosers.input[tabID] = now().Add(fileChooserAllowance)
}

// admitFileChooser decides whether tabID's page may show a chooser now and uses
// up the allowance (AI click first). Called on the main thread; only takes the
// file-chooser lock.
func admitFileChooser(tabID string) bool {
	fileChoosers.Lock()
	defer fileChoosers.Unlock()
	for _, allowances := range []map[string]time.Time{fileChoosers.ai, fileChoosers.input} {
		if allowanceOpenLocked(allowances, tabID) {
			delete(allowances, tabID)
			return true
		}
	}
	return false
}

func forgetFileChooserAllowances(tabID string) {
	fileChoosers.Lock()
	defer fileChoosers.Unlock()
	delete(fileChoosers.ai, tabID)
	delete(fileChoosers.input, tabID)
}

func fileChooserIsOpen(tabID string) bool {
	fileChoosers.Lock()
	defer fileChoosers.Unlock()
	return fileChoosers.open[tabID]
}

// fileChooserOpened records that tabID's page opened its chooser. Called on the main thread.
func fileChooserOpened(tabID string) {
	fileChoosers.Lock()
	fileChoosers.open[tabID] = true
	if _, armed := fileChoosers.armed[tabID]; armed {
		fileChoosers.armed[tabID] = true
	}
	close(fileChoosers.changed)
	fileChoosers.changed = make(chan struct{})
	fileChoosers.Unlock()
	core.Emit(fileChooserEvent, fileChooserState{TabID: tabID, Open: true})
}

// fileChooserClosed records that tabID's chooser closed with selectedCount
// files (0 when cancelled). Called on the main thread.
func fileChooserClosed(tabID string, selectedCount int) {
	if selectedCount < 0 {
		selectedCount = 0
	}
	fileChoosers.Lock()
	delete(fileChoosers.open, tabID)
	fileChoosers.Unlock()
	core.Emit(fileChooserEvent, fileChooserState{TabID: tabID, SelectedCount: selectedCount})
}

// armFileChooserWait starts watching tabID before an AI click, so a chooser
// that opens (and even closes) before waitFileChooser runs still counts.
func armFileChooserWait(tabID string) {
	fileChoosers.Lock()
	defer fileChoosers.Unlock()
	fileChoosers.armed[tabID] = false
}

func disarmFileChooserWait(tabID string) {
	fileChoosers.Lock()
	defer fileChoosers.Unlock()
	delete(fileChoosers.armed, tabID)
}

// waitFileChooser reports whether a chooser opened on tabID since the arm,
// waiting up to timeout. A chooser already open before the arm does not count:
// the click's own chooser is refused while one is open. It never needs the main
// thread. Tests replace it.
var waitFileChooser = func(tabID string, timeout time.Duration) bool {
	defer disarmFileChooserWait(tabID)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		fileChoosers.Lock()
		seen := fileChoosers.armed[tabID]
		changed := fileChoosers.changed
		fileChoosers.Unlock()
		if seen {
			return true
		}
		select {
		case <-changed:
		case <-timer.C:
			return false
		}
	}
}

// finishClick turns the click script's result into the tool result. A click on
// a file input (the script marks it fileInput) waits for the native chooser:
// opened means the user is now choosing; otherwise the AI must ask the user to
// click the input themselves.
func finishClick(tabID string, result any) (any, error) {
	fields, ok := result.(map[string]any)
	if !ok || fields["fileInput"] != true {
		return result, nil
	}
	delete(fields, "fileInput")
	if !nativeFileChooser {
		return fields, nil
	}
	if !waitFileChooser(tabID, fileChooserWaitTimeout) {
		if fileChooserIsOpen(tabID) {
			return nil, newToolError(errCodeFileChooserRequiresUser,
				"another file chooser is already open on this tab; ask the user to finish or cancel it in the browser",
				map[string]any{"tabId": tabID, "ref": fields["ref"], "reason": "chooser_already_open"})
		}
		return nil, newToolError(errCodeFileChooserRequiresUser,
			"the page did not open its file chooser for an AI click; ask the user to click the file input in the browser and choose the files themselves",
			map[string]any{"tabId": tabID, "ref": fields["ref"]})
	}
	fields["fileChooser"] = "opened"
	return fields, nil
}
