package browser

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"

	"mincode/app/backend/core"
)

const browserAIResourceID = "browser:active"

// browserAIAccessState is the set of tabs the user shared with AI plus the
// tab AI targets when a tool call does not name one. Only shared tabs are
// reachable; an empty set means sharing is off.
type browserAIAccessState struct {
	mu          sync.RWMutex
	sharedTabs  []string
	targetTabID string
}

var aiAccess browserAIAccessState

// aiAccessArgs accepts { sharedTabIds, targetTabId } and the legacy
// { enabled, tabId } payload (enabled=true shares exactly tabId).
type aiAccessArgs struct {
	SharedTabIDs *[]string `json:"sharedTabIds"`
	TargetTabID  string    `json:"targetTabId"`
	Enabled      bool      `json:"enabled"`
	TabID        string    `json:"tabId"`
}

type aiToolArgs struct {
	ToolName  string         `json:"toolName"`
	Arguments map[string]any `json:"arguments"`
}

type aiOpenCompletionArgs struct {
	RequestID string `json:"requestId"`
	TabID     string `json:"tabId"`
	URL       string `json:"url"`
	Error     string `json:"error"`
}

type aiOpenRequest struct {
	RequestID string `json:"requestId"`
	URL       string `json:"url,omitempty"`
}

type aiOpenResult struct {
	TabID string `json:"tabId"`
	URL   string `json:"url"`
}

type aiOpenWaiter struct {
	result chan aiOpenCompletionArgs
}

var aiOpenRequests = struct {
	sync.Mutex
	pending map[string]*aiOpenWaiter
}{pending: make(map[string]*aiOpenWaiter)}

// The front end owns the tab list (titles, URLs, which tab is in front), so the
// ACP MCP browser.list_tabs and browser.switch_tab ask it through
// browser:ai-tab-request and wait for embedded_browser_complete_tab_request.
const aiTabRequestEvent = "browser:ai-tab-request"

type aiTabRequest struct {
	RequestID string `json:"requestId"`
	Action    string `json:"action"`
	TabID     string `json:"tabId,omitempty"`
}

type aiTabCompletionArgs struct {
	RequestID string          `json:"requestId"`
	Result    json.RawMessage `json:"result"`
	Error     string          `json:"error"`
}

var aiTabRequests = struct {
	sync.Mutex
	pending map[string]chan aiTabCompletionArgs
}{pending: make(map[string]chan aiTabCompletionArgs)}

// aiTabRequestTimeout bounds how long a tab tool waits for the front end; a
// closed browser workspace must produce an error, never a hung tool call.
var aiTabRequestTimeout = 5 * time.Second

func init() {
	core.Register("embedded_browser_set_ai_access", handleSetAIAccess)
	core.Register("embedded_browser_ai_execute", handleAIExecute)
	core.Register("embedded_browser_complete_open_request", handleCompleteOpenRequest)
	core.Register("embedded_browser_complete_tab_request", handleCompleteTabRequest)
}

func handleSetAIAccess(_, argsJSON string) (string, error) {
	var args aiAccessArgs
	if err := core.DecodeArgsPayload(argsJSON, &args); err != nil {
		return "", err
	}
	var requested []string
	target := ""
	if args.SharedTabIDs != nil {
		requested = *args.SharedTabIDs
		target = strings.TrimSpace(args.TargetTabID)
	} else if args.Enabled {
		target = strings.TrimSpace(args.TabID)
		requested = []string{target}
	}
	shared := make([]string, 0, len(requested))
	seen := make(map[string]bool, len(requested))
	for _, tabID := range requested {
		tabID = strings.TrimSpace(tabID)
		if tabID == "" || seen[tabID] {
			continue
		}
		seen[tabID] = true
		shared = append(shared, tabID)
	}
	// A target outside the shared set is never implicitly shared.
	if !seen[target] {
		target = ""
	}
	aiAccess.mu.Lock()
	aiAccess.sharedTabs = shared
	aiAccess.targetTabID = target
	aiAccess.mu.Unlock()
	return core.EncodeResult(map[string]any{
		"enabled":      len(shared) > 0,
		"resourceId":   browserAIResourceID,
		"sharedTabIds": shared,
		"targetTabId":  target,
		"tabId":        target,
	})
}

func handleAIExecute(_, argsJSON string) (string, error) {
	var args aiToolArgs
	if err := core.DecodeArgsPayload(argsJSON, &args); err != nil {
		return "", err
	}
	result, err := ExecuteAITool(args.ToolName, args.Arguments)
	if err != nil {
		return "", encodeToolError(err)
	}
	return core.EncodeResult(result)
}

func handleCompleteOpenRequest(_, argsJSON string) (string, error) {
	var args aiOpenCompletionArgs
	if err := core.DecodeArgsPayload(argsJSON, &args); err != nil {
		return "", err
	}
	aiOpenRequests.Lock()
	waiter := aiOpenRequests.pending[strings.TrimSpace(args.RequestID)]
	aiOpenRequests.Unlock()
	if waiter != nil {
		select {
		case waiter.result <- args:
		default:
		}
	}
	return core.EncodeResult(nil)
}

func handleCompleteTabRequest(_, argsJSON string) (string, error) {
	var args aiTabCompletionArgs
	if err := core.DecodeArgsPayload(argsJSON, &args); err != nil {
		return "", err
	}
	aiTabRequests.Lock()
	waiter := aiTabRequests.pending[strings.TrimSpace(args.RequestID)]
	aiTabRequests.Unlock()
	if waiter != nil {
		select {
		case waiter <- args:
		default:
		}
	}
	return core.EncodeResult(nil)
}

// requestTabAction asks the browser workspace to list the shared tabs or to
// switch the AI target, and returns its answer.
func requestTabAction(action, tabID string) (any, error) {
	requestID := randomRequestID()
	waiter := make(chan aiTabCompletionArgs, 1)
	aiTabRequests.Lock()
	aiTabRequests.pending[requestID] = waiter
	aiTabRequests.Unlock()
	defer func() {
		aiTabRequests.Lock()
		delete(aiTabRequests.pending, requestID)
		aiTabRequests.Unlock()
	}()
	core.Emit(aiTabRequestEvent, aiTabRequest{RequestID: requestID, Action: action, TabID: tabID})
	select {
	case completion := <-waiter:
		if text := strings.TrimSpace(completion.Error); text != "" {
			return nil, toolErrorFromText(text)
		}
		var result any
		if len(completion.Result) > 0 {
			if err := json.Unmarshal(completion.Result, &result); err != nil {
				return nil, fmt.Errorf("invalid browser tab result: %w", err)
			}
		}
		return result, nil
	case <-time.After(aiTabRequestTimeout):
		return nil, errors.New("timed out waiting for the browser workspace to answer the tab request; open the browser workspace and try again")
	}
}

// toolErrorFromText restores a structured {"code","message","details"} error
// the front end reported, so MCP callers still receive its code.
func toolErrorFromText(text string) error {
	if strings.HasPrefix(text, "{") {
		var parsed struct {
			Code    string         `json:"code"`
			Message string         `json:"message"`
			Details map[string]any `json:"details"`
		}
		if json.Unmarshal([]byte(text), &parsed) == nil && parsed.Code != "" && parsed.Message != "" {
			return newToolError(parsed.Code, parsed.Message, parsed.Details)
		}
	}
	return errors.New(text)
}

// switchTabTool implements browser.switch_tab for ACP: only a tab the backend
// already knows as shared is forwarded; the front end re-checks and activates it.
// It is not dialog-gated: switching never runs page JS, and switching to the
// tab named in dialog_pending is how the user gets to see that dialog.
func switchTabTool(arguments map[string]any) (any, error) {
	tabID := stringArg(arguments, "tabId")
	if tabID == "" {
		return nil, errors.New("browser.switch_tab requires tabId (from browser.list_tabs)")
	}
	if _, err := resolveAITab(map[string]any{"tabId": tabID}); err != nil {
		return nil, err
	}
	return requestTabAction("switch", tabID)
}

// sharedAITabs returns a copy of the tabs shared with AI and the target tab.
func sharedAITabs() (shared []string, target string) {
	aiAccess.mu.RLock()
	defer aiAccess.mu.RUnlock()
	return append([]string{}, aiAccess.sharedTabs...), aiAccess.targetTabID
}

func errSharingDisabled() error {
	return newToolError(errCodeTabNotShared, "AI browser sharing is disabled; enable it in the browser toolbar", nil)
}

// activeAITab returns the target tab AI acts on when a call names no tab.
func activeAITab() (string, error) {
	shared, target := sharedAITabs()
	if len(shared) == 0 {
		return "", errSharingDisabled()
	}
	if target == "" {
		return "", newToolError(errCodeTabNotShared, "no active browser tab is shared with AI", nil)
	}
	return target, nil
}

// resolveAITab returns the tab a tool call operates on: the requested tabId
// when it is shared, otherwise the target tab.
func resolveAITab(arguments map[string]any) (string, error) {
	requested, _ := arguments["tabId"].(string)
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return activeAITab()
	}
	shared, _ := sharedAITabs()
	if len(shared) == 0 {
		return "", errSharingDisabled()
	}
	for _, tabID := range shared {
		if tabID == requested {
			return requested, nil
		}
	}
	return "", newToolError(errCodeTabNotShared, "the requested browser tab is not shared with AI", map[string]any{"tabId": requested})
}

// requireForegroundTab rejects view-level tools (capture, pointer/keyboard
// actions) when the target tab is not the natively displayed one. An unknown
// active tab ("") does not block.
func requireForegroundTab(win unsafe.Pointer, tabID string) error {
	active := platformActiveTabID(win)
	if active == "" || active == tabID {
		return nil
	}
	return newToolError(errCodeTabNotVisible, "the browser tab is not currently visible; switch to it in the browser workspace before capturing or acting on the view", map[string]any{"tabId": tabID})
}

func randomRequestID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err == nil {
		return "browser-ai-" + hex.EncodeToString(value[:])
	}
	return fmt.Sprintf("browser-ai-%d", time.Now().UnixNano())
}

func requestBrowserOpen(rawURL string) (aiOpenResult, error) {
	if strings.TrimSpace(rawURL) != "" {
		if err := validateNavigationURL(rawURL); err != nil {
			return aiOpenResult{}, err
		}
	}
	requestID := randomRequestID()
	waiter := &aiOpenWaiter{result: make(chan aiOpenCompletionArgs, 1)}
	aiOpenRequests.Lock()
	aiOpenRequests.pending[requestID] = waiter
	aiOpenRequests.Unlock()
	defer func() {
		aiOpenRequests.Lock()
		delete(aiOpenRequests.pending, requestID)
		aiOpenRequests.Unlock()
	}()
	core.Emit("browser:ai-open-request", aiOpenRequest{RequestID: requestID, URL: strings.TrimSpace(rawURL)})
	select {
	case completion := <-waiter.result:
		if strings.TrimSpace(completion.Error) != "" {
			return aiOpenResult{}, errors.New(completion.Error)
		}
		if strings.TrimSpace(completion.TabID) == "" {
			return aiOpenResult{}, errors.New("browser tab open completed without a tab id")
		}
		return aiOpenResult{TabID: completion.TabID, URL: completion.URL}, nil
	case <-time.After(20 * time.Second):
		return aiOpenResult{}, errors.New("timed out waiting for the browser workspace to open the tab")
	}
}

func numericArgument(arguments map[string]any, key string, fallback, minimum, maximum int) int {
	value := fallback
	switch raw := arguments[key].(type) {
	case float64:
		value = int(raw)
	case int:
		value = raw
	case json.Number:
		if parsed, err := raw.Int64(); err == nil {
			value = int(parsed)
		}
	}
	if value < minimum {
		value = minimum
	}
	if value > maximum {
		value = maximum
	}
	return value
}

func stringArg(arguments map[string]any, key string) string {
	value, _ := arguments[key].(string)
	return strings.TrimSpace(value)
}

func fillValueArg(arguments map[string]any) (string, error) {
	for _, key := range []string{"value", "text"} {
		raw, exists := arguments[key]
		if !exists {
			continue
		}
		value, ok := raw.(string)
		if !ok {
			return "", fmt.Errorf("%s must be a string", key)
		}
		return value, nil
	}
	return "", errors.New("value is required")
}

func boolArg(arguments map[string]any, key string) bool {
	value, _ := arguments[key].(bool)
	return value
}

func ExecuteAITool(toolName string, arguments map[string]any) (any, error) {
	if arguments == nil {
		arguments = map[string]any{}
	}
	switch toolName {
	case "browser.open_tab":
		return requestBrowserOpen(stringArg(arguments, "url"))
	case "browser.list_tabs":
		// Works with sharing off too: the front end then lists no tabs and only
		// counts the unshared ones.
		return requestTabAction("list", "")
	case "browser.switch_tab":
		return switchTabTool(arguments)
	}
	tabID, err := resolveAITab(arguments)
	if err != nil {
		return nil, err
	}
	if err := dialogGate(toolName, tabID); err != nil {
		return nil, err
	}
	if toolName == "browser.get_downloads" {
		// Records live in Go; off macOS downloads stay blocked and the list is empty.
		return downloadsForTab(tabID), nil
	}
	winPointer, err := requireWindow()
	if err != nil {
		return nil, err
	}

	switch toolName {
	case "browser.handle_dialog":
		return handleDialogTool(tabID, arguments)
	case "browser.get_url":
		return map[string]any{"resourceId": browserAIResourceID, "tabId": tabID, "url": currentBrowserURL(winPointer, tabID)}, nil
	case "browser.navigate":
		url := stringArg(arguments, "url")
		if err := validateNavigationURL(url); err != nil {
			return nil, err
		}
		browserComputerOperationMu.Lock()
		defer browserComputerOperationMu.Unlock()
		previousURL := currentBrowserURL(winPointer, tabID)
		invalidateBrowserComputerCapture(tabID)
		// The dialog gate normally stops this first; release any dialog regardless.
		cancelDialogsForTab(tabID)
		if err := platformNavigateResult(winPointer, tabID, url); err != nil {
			return nil, err
		}
		return waitForReady(winPointer, tabID, numericArgument(arguments, "timeoutMs", 15000, 1000, 30000), url, previousURL)
	case "browser.reload":
		browserComputerOperationMu.Lock()
		defer browserComputerOperationMu.Unlock()
		currentURL := currentBrowserURL(winPointer, tabID)
		invalidateBrowserComputerCapture(tabID)
		cancelDialogsForTab(tabID)
		if err := platformReloadResult(winPointer, tabID); err != nil {
			return nil, err
		}
		return waitForReady(winPointer, tabID, numericArgument(arguments, "timeoutMs", 15000, 1000, 30000), currentURL, currentURL)
	case "browser.go_back", "browser.go_forward":
		delta := -1
		if toolName == "browser.go_forward" {
			delta = 1
		}
		return goHistory(winPointer, tabID, delta, numericArgument(arguments, "timeoutMs", 15000, 1000, 30000), evalValue)
	case "browser.select_option":
		ref, snapshotID, err := snapshotRefArgs(arguments)
		if err != nil {
			return nil, err
		}
		values, hasValues, err := stringListArg(arguments, "values")
		if err != nil {
			return nil, err
		}
		labels, hasLabels, err := stringListArg(arguments, "labels")
		if err != nil {
			return nil, err
		}
		if hasValues == hasLabels {
			return nil, errors.New("browser.select_option needs either values or labels (a non-empty string array), not both")
		}
		return evalAction(winPointer, tabID, pageScript("select_option", snapshotID, ref, values, labels))
	case "browser.set_checked":
		ref, snapshotID, err := snapshotRefArgs(arguments)
		if err != nil {
			return nil, err
		}
		checked, ok := arguments["checked"].(bool)
		if !ok {
			return nil, errors.New("browser.set_checked requires checked (true or false)")
		}
		// No popup, file-chooser or download allowance: only browser.click opens those.
		return setChecked(winPointer, tabID, snapshotID, ref, checked, checkedSettleTimeout, evalMutation)
	case "browser.hover":
		ref, snapshotID, err := snapshotRefArgs(arguments)
		if err != nil {
			return nil, err
		}
		return evalAction(winPointer, tabID, pageScript("hover", snapshotID, ref))
	case "browser.scroll":
		script, err := scrollScript(arguments)
		if err != nil {
			return nil, err
		}
		return evalAction(winPointer, tabID, script)
	case "browser.get_page_snapshot":
		var result any
		script := pageScript("snapshot", numericArgument(arguments, "maxText", 12000, 1000, 30000), numericArgument(arguments, "maxElements", 200, 20, 500))
		if err := evalValue(winPointer, tabID, script, 5000, &result); err != nil {
			return nil, err
		}
		return boundSnapshot(result, maxSnapshotBytes)
	case "browser.extract_content":
		script, err := extractContentScript(arguments)
		if err != nil {
			return nil, err
		}
		var result any
		if err := evalValue(winPointer, tabID, script, 10000, &result); err != nil {
			return nil, err
		}
		return boundExtract(result, maxSnapshotBytes)
	case "browser.inspect_element":
		ref, snapshotID := stringArg(arguments, "ref"), stringArg(arguments, "snapshotId")
		if ref == "" || snapshotID == "" {
			return nil, errors.New("snapshotId and ref are required")
		}
		var result any
		if err := evalValue(winPointer, tabID, pageScript("inspect", snapshotID, ref), 3000, &result); err != nil {
			return nil, err
		}
		return result, nil
	case "browser.click":
		ref, snapshotID := stringArg(arguments, "ref"), stringArg(arguments, "snapshotId")
		script := pageScript("click", snapshotID, ref)
		// Before the click: the page may open its window, file chooser or download synchronously.
		allowAIPopupWindow(tabID)
		allowAIFileChooser(tabID)
		allowAIDownload(tabID)
		armFileChooserWait(tabID)
		defer disarmFileChooserWait(tabID)
		var result any
		if err := evalMutation(winPointer, tabID, script, 3000, &result); err != nil {
			return nil, err
		}
		return finishClick(tabID, result)
	case "browser.fill":
		ref, snapshotID := stringArg(arguments, "ref"), stringArg(arguments, "snapshotId")
		value, valueErr := fillValueArg(arguments)
		if valueErr != nil {
			return nil, valueErr
		}
		script := pageScript("fill", snapshotID, ref, value)
		var result any
		if err := evalMutation(winPointer, tabID, script, 3000, &result); err != nil {
			return nil, err
		}
		return result, nil
	case "browser.press_key":
		key := stringArg(arguments, "key")
		allowed := map[string]bool{"Enter": true, "Tab": true, "Escape": true, "ArrowUp": true, "ArrowDown": true, "ArrowLeft": true, "ArrowRight": true, "Backspace": true, "Delete": true, " ": true}
		if !allowed[key] {
			return nil, errors.New("unsupported key")
		}
		ref, snapshotID := stringArg(arguments, "ref"), stringArg(arguments, "snapshotId")
		script := pageScript("press_key", snapshotID, ref, key)
		var result any
		if err := evalMutation(winPointer, tabID, script, 3000, &result); err != nil {
			return nil, err
		}
		return result, nil
	case "browser.wait_for":
		return waitForCondition(winPointer, tabID, arguments)
	case "browser.capture_view":
		if err := requireForegroundTab(winPointer, tabID); err != nil {
			return nil, err
		}
		return captureBrowserView(winPointer, tabID, numericArgument(arguments, "timeoutMs", 10000, 1000, 30000))
	case "browser.computer_action":
		if err := requireForegroundTab(winPointer, tabID); err != nil {
			return nil, err
		}
		return executeBrowserComputerActions(winPointer, tabID, arguments)
	case "browser.get_console_logs":
		return diagnosticsValue(winPointer, tabID, "console", numericArgument(arguments, "limit", 100, 1, 200))
	case "browser.get_network_failures":
		return diagnosticsValue(winPointer, tabID, "network_failures", numericArgument(arguments, "limit", 100, 1, 200))
	case "browser.get_page_errors":
		return diagnosticsValue(winPointer, tabID, "errors", numericArgument(arguments, "limit", 100, 1, 200))
	case "browser.get_navigation_errors":
		var items any
		if err := json.Unmarshal([]byte(platformNavigationErrors(winPointer, tabID)), &items); err != nil {
			return nil, err
		}
		return items, nil
	case "browser.evaluate":
		if !boolArg(arguments, "unsafeConfirmed") {
			return nil, errors.New("browser.evaluate requires unsafeConfirmed=true after explicit user approval")
		}
		var result any
		if err := evalMutation(winPointer, tabID, stringArg(arguments, "script"), numericArgument(arguments, "timeoutMs", 3000, 100, 10000), &result); err != nil {
			return nil, err
		}
		return result, nil
	default:
		return nil, fmt.Errorf("unknown browser AI tool: %s", toolName)
	}
}

// pageEvaluator runs a page script and decodes its value; evalValue in production.
type pageEvaluator func(win unsafe.Pointer, tabID, script string, timeout int, target any) error

// evalAction runs an element action script and returns its value.
func evalAction(win unsafe.Pointer, tabID, script string) (any, error) {
	var result any
	if err := evalMutation(win, tabID, script, 3000, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// maxSnapshotBytes caps a serialized page snapshot. snapshot.js bounds text, names,
// option labels and hrefs, but a hostile page can still multiply them (500 elements
// of 50 options each), so Go drops trailing elements beyond this size.
const maxSnapshotBytes = 2 << 20

// boundSnapshot returns snapshot unchanged when its JSON fits in limit bytes;
// otherwise a copy keeping the longest prefix of elements that fits, with
// truncatedElements set (the dropped refs stay valid in the page). A snapshot
// that does not fit even without elements is an error.
func boundSnapshot(snapshot any, limit int) (any, error) {
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	if len(encoded) <= limit {
		return snapshot, nil
	}
	tooLarge := errors.New("page snapshot is too large; call browser.get_page_snapshot with a smaller maxElements")
	// The title is capped in the page and text by maxText, so a base this large means a huge URL.
	baseTooLarge := errors.New("page snapshot is too large even without elements: the page title or URL is too large")
	fields, ok := snapshot.(map[string]any)
	if !ok {
		return nil, tooLarge
	}
	elements, _ := fields["elements"].([]any)
	bounded := make(map[string]any, len(fields))
	for key, value := range fields {
		bounded[key] = value
	}
	bounded["elements"] = []any{}
	bounded["truncatedElements"] = true
	base, err := json.Marshal(bounded)
	if err != nil {
		return nil, err
	}
	if len(base) > limit {
		return nil, baseTooLarge
	}
	size, kept := len(base), 0
	for _, element := range elements {
		item, err := json.Marshal(element)
		if err != nil {
			return nil, err
		}
		add := len(item)
		if kept > 0 {
			add++ // the comma between elements
		}
		if size+add > limit {
			break
		}
		size += add
		kept++
	}
	if kept > 0 {
		bounded["elements"] = elements[:kept:kept]
	}
	return bounded, nil
}

// extractContentScript validates browser.extract_content's arguments and builds
// its page script: format markdown (default) or text, maxChars clamped to
// 1000-100000 (default 20000), and an optional snapshotId+ref pair.
func extractContentScript(arguments map[string]any) (string, error) {
	format := "markdown"
	if raw, exists := arguments["format"]; exists && raw != nil {
		value, ok := raw.(string)
		if !ok {
			return "", errors.New("browser.extract_content format must be markdown or text")
		}
		switch strings.TrimSpace(value) {
		case "", "markdown":
		case "text":
			format = "text"
		default:
			return "", errors.New("browser.extract_content format must be markdown or text")
		}
	}
	ref, snapshotID := stringArg(arguments, "ref"), stringArg(arguments, "snapshotId")
	if (ref == "") != (snapshotID == "") {
		return "", errors.New("browser.extract_content needs both snapshotId and ref, or neither")
	}
	return pageScript("extract", format, numericArgument(arguments, "maxChars", 20000, 1000, 100000), snapshotID, ref), nil
}

// boundExtract is the size backstop for browser.extract_content: extract.js caps
// content at maxChars and the title at 300 code points, so only a huge page URL
// can push the result past limit bytes.
func boundExtract(result any, limit int) (any, error) {
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if len(encoded) > limit {
		return nil, errors.New("extracted page content is too large: the page URL is too large")
	}
	return result, nil
}

func snapshotRefArgs(arguments map[string]any) (ref, snapshotID string, err error) {
	ref, snapshotID = stringArg(arguments, "ref"), stringArg(arguments, "snapshotId")
	if ref == "" || snapshotID == "" {
		return "", "", errors.New("snapshotId and ref are required")
	}
	return ref, snapshotID, nil
}

// stringListArg reads an optional array of strings; present reports a non-empty array.
func stringListArg(arguments map[string]any, key string) (values []string, present bool, err error) {
	values = []string{}
	raw, exists := arguments[key]
	if !exists || raw == nil {
		return values, false, nil
	}
	switch list := raw.(type) {
	case []string:
		values = append(values, list...)
	case []any:
		for _, item := range list {
			text, ok := item.(string)
			if !ok {
				return nil, false, fmt.Errorf("%s must be an array of strings", key)
			}
			values = append(values, text)
		}
	default:
		return nil, false, fmt.Errorf("%s must be an array of strings", key)
	}
	if len(values) > 500 {
		return nil, false, fmt.Errorf("%s accepts at most 500 entries", key)
	}
	return values, len(values) > 0, nil
}

// checkedSettleTimeout bounds how long setChecked waits for an ARIA widget
// that updates aria-checked after the click (Vue nextTick, React batching).
const checkedSettleTimeout = time.Second

// setChecked clicks the element at most once (scripts/actions.js set_checked)
// and, when the page has not applied an ARIA toggle yet (pending), polls the
// read-only checked_state script every 50ms until it matches or timeout passes.
func setChecked(win unsafe.Pointer, tabID, snapshotID, ref string, checked bool, timeout time.Duration, eval pageEvaluator) (any, error) {
	var result any
	if err := eval(win, tabID, pageScript("set_checked", snapshotID, ref, checked), 3000, &result); err != nil {
		return nil, err
	}
	if fields, _ := result.(map[string]any); fields == nil || fields["pending"] != true {
		return result, nil
	}
	script := pageScript("checked_state", snapshotID, ref)
	deadline := time.Now().Add(timeout)
	for {
		time.Sleep(50 * time.Millisecond)
		var state any
		if err := eval(win, tabID, script, 1500, &state); err != nil {
			return nil, err
		}
		if fields, _ := state.(map[string]any); fields != nil && fields["checked"] == checked {
			return map[string]any{"changed": true, "ref": ref, "checked": checked}, nil
		}
		if time.Now().After(deadline) {
			state := "checked"
			if !checked {
				state = "unchecked"
			}
			return nil, fmt.Errorf("element did not become %s within %s of the click", state, timeout)
		}
	}
}

var scrollDirections = map[string]bool{"up": true, "down": true, "left": true, "right": true}

// scrollAmountArg returns "page" (the default) or a pixel count within 1..100000.
func scrollAmountArg(arguments map[string]any) (any, error) {
	switch raw := arguments["amount"].(type) {
	case nil:
		return "page", nil
	case string:
		if strings.TrimSpace(raw) == "page" {
			return "page", nil
		}
	case float64:
		if raw > 0 && !math.IsInf(raw, 0) {
			return min(max(int(math.Round(raw)), 1), 100000), nil
		}
	case int:
		if raw > 0 {
			return min(raw, 100000), nil
		}
	}
	return nil, errors.New(`browser.scroll amount must be a positive number of pixels or "page"`)
}

func scrollScript(arguments map[string]any) (string, error) {
	ref, snapshotID := stringArg(arguments, "ref"), stringArg(arguments, "snapshotId")
	direction, containerRef := stringArg(arguments, "direction"), stringArg(arguments, "containerRef")
	if (ref == "") == (direction == "") {
		return "", errors.New("browser.scroll needs either ref or direction, not both")
	}
	if direction != "" && !scrollDirections[direction] {
		return "", errors.New("browser.scroll direction must be up, down, left or right")
	}
	amount, err := scrollAmountArg(arguments)
	if err != nil {
		return "", err
	}
	if (ref != "" || containerRef != "") && snapshotID == "" {
		return "", errors.New("browser.scroll snapshotId is required with ref or containerRef")
	}
	return pageScript("scroll", snapshotID, ref, direction, amount, containerRef), nil
}

// historyState is what scripts/history_state.js reports.
type historyState struct {
	ReadyState string `json:"readyState"`
	URL        string `json:"url"`
	Title      string `json:"title"`
	Marked     bool   `json:"marked"`
}

// goHistory steps the tab one entry back (delta < 0) or forward and waits for
// the entry to load. The current document is marked first, so a history entry
// at the same URL is still recognised as a new page. Like navigate and reload
// it holds browserComputerOperationMu, invalidates the computer capture and
// releases the tab's dialogs before the page changes.
func goHistory(win unsafe.Pointer, tabID string, delta, timeout int, eval pageEvaluator) (any, error) {
	browserComputerOperationMu.Lock()
	defer browserComputerOperationMu.Unlock()
	deadline := time.Now().Add(time.Duration(timeout) * time.Millisecond)
	token := randomRequestID()
	var before historyState
	marked := eval(win, tabID, pageScript("history_state", token, true), 1500, &before) == nil && before.Marked
	if before.URL == "" {
		marked = false
	}
	invalidateBrowserComputerCapture(tabID)
	cancelDialogsForTab(tabID)
	moved, err := platformGoHistory(win, tabID, delta)
	if err != nil {
		return nil, err
	}
	direction := "back"
	if delta > 0 {
		direction = "forward"
	}
	if !moved {
		return nil, newToolError(errCodeNoHistory, fmt.Sprintf("the browser tab has no page to go %s to", direction), map[string]any{"tabId": tabID, "direction": direction})
	}
	if before.URL == "" {
		// The page could not be read before the step; wait for any ready document.
		return waitForReady(win, tabID, max(int(time.Until(deadline).Milliseconds()), 1), "", "")
	}
	after, err := waitForHistoryChange(win, tabID, token, before, marked, deadline, eval)
	if err != nil {
		return nil, err
	}
	return waitForReady(win, tabID, max(int(time.Until(deadline).Milliseconds()), 1), after.URL, "")
}

// waitForHistoryChange polls until the tab shows another URL or, when the old
// document was marked, a document without the mark.
func waitForHistoryChange(win unsafe.Pointer, tabID, token string, before historyState, marked bool, deadline time.Time, eval pageEvaluator) (historyState, error) {
	script := pageScript("history_state", token, false)
	for {
		var state historyState
		if err := eval(win, tabID, script, 1500, &state); err == nil && state.URL != "" {
			if state.URL != before.URL || (marked && !state.Marked) {
				return state, nil
			}
		}
		if time.Now().After(deadline) {
			return historyState{}, errors.New("browser history navigation did not start before timeout")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// evalPlatform is platformEval; tests swap it to simulate a page that blocks.
var evalPlatform = platformEval

// evalValue runs a page script and decodes its value. A script that makes the
// page open confirm/prompt/alert (a click on a button whose handler asks)
// does not finish until the dialog is answered, so evalValue also watches for
// a dialog on the tab and then returns dialog_pending at once, with
// details.actionDelivered false: evalValue is for reads and probes. Scripts that
// perform the action itself go through evalMutation. The native eval keeps its
// own timeout; its late result lands in a buffered channel and is dropped, so
// nothing leaks.
func evalValue(win unsafe.Pointer, tabID, script string, timeout int, target any) error {
	return evalScript(win, tabID, script, timeout, target, false)
}

// evalMutation is evalValue for the script that performs an action (click,
// fill, key press, ...): a dialog it causes reports actionDelivered true, so
// the AI answers the dialog instead of repeating the action.
func evalMutation(win unsafe.Pointer, tabID, script string, timeout int, target any) error {
	return evalScript(win, tabID, script, timeout, target, true)
}

func evalScript(win unsafe.Pointer, tabID, script string, timeout int, target any, delivered bool) error {
	opened, stopWatching := watchDialogOpened(tabID)
	defer stopWatching()
	results := make(chan string, 1)
	go func() { results <- evalPlatform(win, tabID, script, timeout) }()
	var raw string
	select {
	case raw = <-results:
	case <-opened:
		if d, ok := dialogForTab(tabID); ok {
			return dialogInterruptedError(d, delivered)
		}
		// Answered already (by the user, in the message bar): the script resumes.
		raw = <-results
	}
	var result evalResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return fmt.Errorf("browser eval response: %w", err)
	}
	if !result.OK {
		// A dialog that opened while the script ran (and the watcher missed,
		// e.g. a timeout racing the open) is the real reason; say so.
		if d, ok := dialogForTab(tabID); ok {
			return dialogInterruptedError(d, delivered)
		}
		return errors.New(result.Error)
	}
	if target == nil || len(result.Value) == 0 {
		return nil
	}
	if err := json.Unmarshal(result.Value, target); err != nil {
		return fmt.Errorf("browser eval value: %w", err)
	}
	return nil
}

func waitForReady(win unsafe.Pointer, tabID string, timeout int, expectedURL, previousURL string) (any, error) {
	deadline := time.Now().Add(time.Duration(timeout) * time.Millisecond)
	for {
		var state browserReadyState
		if runtime.GOOS == "windows" {
			if err := browserNavigationFailure(platformNavigationErrors(win, tabID)); err != nil {
				return nil, err
			}
		}
		if err := evalValue(win, tabID, `({readyState:document.readyState,url:location.href,title:document.title})`, 1500, &state); err == nil && browserReadyMatches(state, expectedURL, previousURL) {
			if runtime.GOOS == "windows" {
				if err := browserNavigationFailure(platformNavigationErrors(win, tabID)); err != nil {
					return nil, err
				}
			}
			return state, nil
		}
		if time.Now().After(deadline) {
			return nil, errors.New("browser navigation did not become ready before timeout")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func waitForCondition(win unsafe.Pointer, tabID string, arguments map[string]any) (any, error) {
	timeout := numericArgument(arguments, "timeoutMs", 10000, 100, 30000)
	text, urlContains, ref, snapshotID := stringArg(arguments, "text"), stringArg(arguments, "urlContains"), stringArg(arguments, "ref"), stringArg(arguments, "snapshotId")
	if text == "" && urlContains == "" && ref == "" {
		return nil, errors.New("wait_for requires text, urlContains, or ref")
	}
	script := pageScript("wait_for", text, urlContains, snapshotID, ref)
	deadline := time.Now().Add(time.Duration(timeout) * time.Millisecond)
	for {
		var result map[string]any
		if err := evalValue(win, tabID, script, 1500, &result); err == nil && result["matched"] == true {
			return result, nil
		}
		if time.Now().After(deadline) {
			return nil, errors.New("browser.wait_for timed out")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// diagnosticsValue reads window.__mincodeDiagnostics; kind is "console", "errors" or "network_failures".
func diagnosticsValue(win unsafe.Pointer, tabID, kind string, limit int) (any, error) {
	script := pageScript("diagnostics", kind, limit)
	var result any
	if err := evalValue(win, tabID, script, 3000, &result); err != nil {
		return nil, err
	}
	return result, nil
}
