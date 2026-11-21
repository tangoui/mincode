// Package computer exposes a deliberately small, approval-gated desktop
// Computer Use harness. The frontend registers these commands as dangerous AI
// tools; this package only validates and executes already-authorized requests.
package computer

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"time"

	"mincode/app/backend/core"
)

const (
	maxComputerActions = 32
	maxComputerText    = 20_000
	maxComputerKeys    = 16
	maxDragPoints      = 128
	maxActionDuration  = 10_000
	maxBatchTimeout    = 30_000
	screenshotFrameAge = 2 * time.Minute
	captureSettleDelay = 300 * time.Millisecond
	typeChunkRunes     = 100
	// Standalone captures and the final observation of a batch need a bounded
	// budget of their own so a nearly exhausted batch can still report its state.
	defaultCaptureTimeout = 10 * time.Second
	minFinalCaptureBudget = 3 * time.Second
)

type DisplayInfo struct {
	ID          uint32  `json:"id"`
	Primary     bool    `json:"primary"`
	OriginX     float64 `json:"originX"`
	OriginY     float64 `json:"originY"`
	PointWidth  float64 `json:"pointWidth"`
	PointHeight float64 `json:"pointHeight"`
	PixelWidth  int     `json:"pixelWidth"`
	PixelHeight int     `json:"pixelHeight"`
	ScaleX      float64 `json:"scaleX"`
	ScaleY      float64 `json:"scaleY"`
}

type Status struct {
	Supported               bool          `json:"supported"`
	Platform                string        `json:"platform"`
	AccessibilityAuthorized bool          `json:"accessibilityAuthorized"`
	ScreenCaptureAuthorized bool          `json:"screenCaptureAuthorized"`
	Display                 DisplayInfo   `json:"display"`
	Displays                []DisplayInfo `json:"displays"`
}

type Capture struct {
	MimeType string      `json:"mimeType"`
	Data     string      `json:"data"`
	Width    int         `json:"width"`
	Height   int         `json:"height"`
	Display  DisplayInfo `json:"display"`
}

type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type Action struct {
	Type       string   `json:"type"`
	X          *float64 `json:"x"`
	Y          *float64 `json:"y"`
	Button     string   `json:"button"`
	Text       string   `json:"text"`
	Keys       []string `json:"keys"`
	Modifiers  []string `json:"modifiers"`
	ScrollX    float64  `json:"scroll_x"`
	ScrollY    float64  `json:"scroll_y"`
	Path       []Point  `json:"path"`
	DurationMS *int     `json:"durationMs"`
	Display    *uint32  `json:"display"`
}

type captureArgs struct {
	Display *uint32 `json:"display"`
}

type permissionArgs struct {
	Accessibility *bool `json:"accessibility"`
	ScreenCapture *bool `json:"screenCapture"`
}

type executeArgs struct {
	Actions      []Action `json:"actions"`
	CaptureAfter *bool    `json:"captureAfter"`
	TimeoutMS    int      `json:"timeoutMs"`
}

type openURLArgs struct {
	URL     string `json:"url"`
	Browser string `json:"browser"`
}

type openAppArgs struct {
	Application string `json:"application"`
}

type captureGeometry struct {
	Display       DisplayInfo
	NativeDisplay DisplayInfo
	CapturedAt    time.Time
}

var state = struct {
	sync.RWMutex
	geometry captureGeometry
}{}

// Desktop input must be serialized so two conversations cannot interleave
// mouse and keyboard events or overwrite the screenshot coordinate frame.
var operationMu sync.Mutex

func init() {
	core.Register("computer_get_status", getStatusHandler)
	core.Register("computer_request_permissions", requestPermissionsHandler)
	core.Register("computer_capture_screen", captureScreenHandler)
	core.Register("computer_open_app", openAppHandler)
	core.Register("computer_open_url", openURLHandler)
	core.Register("system_browser_open_url", systemBrowserOpenURLHandler)
	core.Register("computer_execute", executeHandler)
}

func getStatusHandler(_, _ string) (string, error) {
	result, err := ExecuteAITool("computer.get_status", nil)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(result)
}

func requestPermissionsHandler(_, argsJSON string) (string, error) {
	var args permissionArgs
	if err := core.DecodeArgsPayload(argsJSON, &args); err != nil {
		return "", err
	}
	result, err := executeRequestPermissions(args)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(result)
}

func captureScreenHandler(_, argsJSON string) (string, error) {
	var args captureArgs
	if err := core.DecodeArgsPayload(argsJSON, &args); err != nil {
		return "", err
	}
	result, err := executeCapture(args)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(result)
}

func openAppHandler(_, argsJSON string) (string, error) {
	var args openAppArgs
	if err := core.DecodeArgsPayload(argsJSON, &args); err != nil {
		return "", err
	}
	result, err := executeOpenApp(args)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(result)
}

func openURLHandler(_, argsJSON string) (string, error) {
	var args openURLArgs
	if err := core.DecodeArgsPayload(argsJSON, &args); err != nil {
		return "", err
	}
	result, err := executeOpenURL(args)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(result)
}

// systemBrowserOpener is platformOpenURL; tests replace it so no browser opens.
var systemBrowserOpener = platformOpenURL

// OpenSystemBrowser opens an absolute http(s) URL in the default system
// browser with the same validation as system_browser_open_url. Other backend
// packages (for example the MCP OAuth sign-in) use it instead of dispatching
// the command with JSON. Error text never includes the URL.
func OpenSystemBrowser(targetURL string) error {
	normalized, browser, err := normalizeOpenURLArgs(openURLArgs{URL: targetURL})
	if err != nil {
		return err
	}
	return openInSystemBrowser(normalized, browser)
}

func openInSystemBrowser(targetURL, browser string) error {
	if !platformStatus().Supported {
		return fmt.Errorf("system browser is not supported on %s", runtime.GOOS)
	}
	operationMu.Lock()
	defer operationMu.Unlock()
	if err := systemBrowserOpener(targetURL, browser); err != nil {
		return fmt.Errorf("open URL in system browser: %w", err)
	}
	return nil
}

func systemBrowserOpenURLHandler(_, argsJSON string) (string, error) {
	var args openURLArgs
	if err := core.DecodeArgsPayload(argsJSON, &args); err != nil {
		return "", err
	}
	targetURL, browser, err := normalizeOpenURLArgs(args)
	if err != nil {
		return "", err
	}
	if err := openInSystemBrowser(targetURL, browser); err != nil {
		return "", err
	}
	return core.EncodeResult(map[string]any{
		"url":      targetURL,
		"browser":  browser,
		"platform": runtime.GOOS,
	})
}

func executeHandler(_, argsJSON string) (string, error) {
	var args executeArgs
	if err := core.DecodeArgsPayload(argsJSON, &args); err != nil {
		return "", err
	}
	result, err := executeBatch(args)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(result)
}

// ExecuteAITool exposes the same validated Computer Use implementation to trusted
// backend adapters such as MinCode's private ACP MCP bridge. It deliberately does
// not provide an alternate input path: permission checks, screenshot coordinate
// validation, action limits, and serialized desktop access remain centralized here.
func ExecuteAITool(toolName string, arguments map[string]any) (any, error) {
	switch strings.TrimSpace(toolName) {
	case "computer.get_status":
		return platformStatus(), nil
	case "computer.request_permissions":
		var args permissionArgs
		if err := decodeAIToolArguments(arguments, &args); err != nil {
			return nil, err
		}
		return executeRequestPermissions(args)
	case "computer.capture_screen":
		var args captureArgs
		if err := decodeAIToolArguments(arguments, &args); err != nil {
			return nil, err
		}
		return executeCapture(args)
	case "computer.open_app":
		var args openAppArgs
		if err := decodeAIToolArguments(arguments, &args); err != nil {
			return nil, err
		}
		return executeOpenApp(args)
	case "computer.open_url":
		var args openURLArgs
		if err := decodeAIToolArguments(arguments, &args); err != nil {
			return nil, err
		}
		return executeOpenURL(args)
	case "computer.execute":
		var args executeArgs
		if err := decodeAIToolArguments(arguments, &args); err != nil {
			return nil, err
		}
		return executeBatch(args)
	default:
		return nil, fmt.Errorf("unknown computer AI tool: %s", toolName)
	}
}

func executeOpenApp(args openAppArgs) (map[string]any, error) {
	application, err := normalizeOpenAppArgs(args)
	if err != nil {
		return nil, err
	}
	status := platformStatus()
	if !status.Supported {
		return nil, fmt.Errorf("desktop Computer Use is not supported on %s", runtime.GOOS)
	}

	operationMu.Lock()
	defer operationMu.Unlock()
	if err := platformOpenApp(application); err != nil {
		return nil, fmt.Errorf("open local application: %w", err)
	}

	// Allow Launch Services to activate the application before returning a
	// visual observation that the model can use for the next interaction.
	time.Sleep(1200 * time.Millisecond)
	result := map[string]any{
		"application": application,
		"platform":    runtime.GOOS,
	}
	if !status.ScreenCaptureAuthorized {
		return result, nil
	}
	capture, err := captureScreen(0, defaultCaptureTimeout)
	if err != nil {
		result["captureError"] = fmt.Sprintf("verify opened application: %v", err)
		return result, nil
	}
	result["mimeType"] = capture.MimeType
	result["data"] = capture.Data
	result["width"] = capture.Width
	result["height"] = capture.Height
	result["display"] = capture.Display
	return result, nil
}

func normalizeOpenAppArgs(args openAppArgs) (string, error) {
	application := strings.TrimSpace(args.Application)
	if application == "" {
		return "", errors.New("computer.open_app requires application")
	}
	if len(application) > 512 {
		return "", errors.New("computer.open_app application must be at most 512 bytes")
	}
	if strings.ContainsRune(application, '\x00') || strings.ContainsAny(application, `/\\`) {
		return "", errors.New("computer.open_app accepts an application name or bundle identifier, not a filesystem path")
	}
	return application, nil
}

func executeOpenURL(args openURLArgs) (map[string]any, error) {
	targetURL, browser, err := normalizeOpenURLArgs(args)
	if err != nil {
		return nil, err
	}
	status := platformStatus()
	if !status.Supported {
		return nil, fmt.Errorf("desktop Computer Use is not supported on %s", runtime.GOOS)
	}

	operationMu.Lock()
	defer operationMu.Unlock()
	if err := platformOpenURL(targetURL, browser); err != nil {
		return nil, fmt.Errorf("open URL in local browser: %w", err)
	}

	// Give Launch Services and the browser enough time to activate and commit the
	// navigation before returning a visual observation to the model.
	time.Sleep(1500 * time.Millisecond)
	result := map[string]any{
		"url":      targetURL,
		"browser":  browser,
		"platform": runtime.GOOS,
	}
	if !status.ScreenCaptureAuthorized {
		return result, nil
	}
	capture, err := captureScreen(0, defaultCaptureTimeout)
	if err != nil {
		result["captureError"] = fmt.Sprintf("verify opened browser URL: %v", err)
		return result, nil
	}
	result["mimeType"] = capture.MimeType
	result["data"] = capture.Data
	result["width"] = capture.Width
	result["height"] = capture.Height
	result["display"] = capture.Display
	return result, nil
}

func normalizeOpenURLArgs(args openURLArgs) (string, string, error) {
	targetURL := strings.TrimSpace(args.URL)
	if targetURL == "" {
		return "", "", errors.New("computer.open_url requires url")
	}
	if len(targetURL) > 4096 {
		return "", "", errors.New("computer.open_url url must be at most 4096 bytes")
	}
	parsed, err := url.Parse(targetURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", "", errors.New("computer.open_url requires an absolute http or https URL")
	}

	var browser string
	switch strings.ToLower(strings.TrimSpace(args.Browser)) {
	case "", "default":
		browser = "default"
	case "chrome", "google chrome":
		browser = "chrome"
	case "safari":
		browser = "safari"
	case "firefox", "mozilla firefox":
		browser = "firefox"
	case "edge", "microsoft edge":
		browser = "edge"
	default:
		return "", "", fmt.Errorf("computer.open_url unsupported browser %q", args.Browser)
	}
	return parsed.String(), browser, nil
}

func decodeAIToolArguments(arguments map[string]any, target any) error {
	if arguments == nil {
		arguments = map[string]any{}
	}
	encoded, err := json.Marshal(arguments)
	if err != nil {
		return fmt.Errorf("encode computer AI tool arguments: %w", err)
	}
	if err := json.Unmarshal(encoded, target); err != nil {
		return fmt.Errorf("invalid computer AI tool arguments: %w", err)
	}
	return nil
}

func executeRequestPermissions(args permissionArgs) (Status, error) {
	accessibility := args.Accessibility == nil || *args.Accessibility
	screenCapture := args.ScreenCapture == nil || *args.ScreenCapture
	return platformRequestPermissions(accessibility, screenCapture), nil
}

func executeBatch(args executeArgs) (map[string]any, error) {
	if err := validateActions(args.Actions); err != nil {
		return nil, err
	}
	if args.TimeoutMS == 0 {
		args.TimeoutMS = maxBatchTimeout
	}
	if args.TimeoutMS < 1_000 || args.TimeoutMS > maxBatchTimeout {
		return nil, fmt.Errorf("computer.execute timeoutMs must be between 1000 and %d", maxBatchTimeout)
	}
	captureAfter := args.CaptureAfter == nil || *args.CaptureAfter

	operationMu.Lock()
	defer operationMu.Unlock()
	return executeActions(args.Actions, captureAfter, time.Now().Add(time.Duration(args.TimeoutMS)*time.Millisecond))
}

func inputPermissionError() error {
	if runtime.GOOS == "darwin" {
		return errors.New("macOS Accessibility permission is required; run computer.request_permissions and grant MinCode access in System Settings")
	}
	return fmt.Errorf("desktop input permission is required on %s", runtime.GOOS)
}

func screenCapturePermissionError(visualConfirmation bool) error {
	if runtime.GOOS == "darwin" {
		if visualConfirmation {
			return errors.New("macOS Screen Recording permission is required for visual confirmation; run computer.request_permissions and grant MinCode access in System Settings")
		}
		return errors.New("macOS Screen Recording permission is required; run computer.request_permissions and grant MinCode access in System Settings")
	}
	return fmt.Errorf("desktop screen capture permission is required on %s", runtime.GOOS)
}

func executeCapture(args captureArgs) (Capture, error) {
	operationMu.Lock()
	defer operationMu.Unlock()
	displayID := uint32(0)
	if args.Display != nil {
		displayID = *args.Display
	}
	return captureScreen(displayID, defaultCaptureTimeout)
}

// findDisplay looks up a display by platform ID. ID 0 selects the primary display.
func findDisplay(status Status, displayID uint32) (DisplayInfo, bool) {
	if displayID == 0 {
		return status.Display, status.Display.PixelWidth > 0 && status.Display.PixelHeight > 0
	}
	for _, display := range status.Displays {
		if display.ID == displayID {
			return display, true
		}
	}
	if status.Display.ID == displayID && status.Display.PixelWidth > 0 {
		return status.Display, true
	}
	return DisplayInfo{}, false
}

func unknownDisplayError(status Status, displayID uint32) error {
	ids := make([]string, 0, len(status.Displays))
	for _, display := range status.Displays {
		ids = append(ids, fmt.Sprint(display.ID))
	}
	return fmt.Errorf("display %d is not connected; available displays: %s", displayID, strings.Join(ids, ", "))
}

// defaultCaptureDisplay keeps follow-up observations on the display the model
// is currently working with, falling back to the primary display.
func defaultCaptureDisplay() uint32 {
	state.RLock()
	displayID := state.geometry.NativeDisplay.ID
	state.RUnlock()
	if displayID == 0 {
		return 0
	}
	if _, ok := findDisplay(platformStatus(), displayID); !ok {
		return 0
	}
	return displayID
}

func captureScreen(displayID uint32, timeout time.Duration) (Capture, error) {
	status := platformStatus()
	if !status.Supported {
		return Capture{}, fmt.Errorf("desktop Computer Use is not supported on %s", runtime.GOOS)
	}
	if !status.ScreenCaptureAuthorized {
		return Capture{}, screenCapturePermissionError(false)
	}
	target, ok := findDisplay(status, displayID)
	if !ok {
		if displayID == 0 {
			return Capture{}, errors.New("desktop primary display is unavailable")
		}
		return Capture{}, unknownDisplayError(status, displayID)
	}
	if timeout <= 0 {
		return Capture{}, errors.New("computer.execute timeout exceeded before screen capture")
	}
	capture, err := platformCaptureScreen(target.ID, timeout)
	if err != nil {
		return Capture{}, fmt.Errorf("capture desktop screen: %w", err)
	}
	if capture.Width <= 0 || capture.Height <= 0 || strings.TrimSpace(capture.Data) == "" {
		return Capture{}, errors.New("desktop capture returned an empty image")
	}
	if capture.Display.PointWidth <= 0 || capture.Display.PointHeight <= 0 || capture.Display.PixelWidth <= 0 || capture.Display.PixelHeight <= 0 {
		return Capture{}, errors.New("desktop capture returned invalid display geometry")
	}
	if capture.MimeType == "" {
		capture.MimeType = "image/png"
	}
	capture, err = fitCaptureForModel(capture)
	if err != nil {
		return Capture{}, err
	}
	nativeDisplay, ok := findDisplay(platformStatus(), target.ID)
	if !ok || nativeDisplay.PointWidth <= 0 || nativeDisplay.PointHeight <= 0 || nativeDisplay.PixelWidth <= 0 || nativeDisplay.PixelHeight <= 0 {
		return Capture{}, errors.New("desktop display geometry became unavailable after capture")
	}
	state.Lock()
	state.geometry = captureGeometry{Display: capture.Display, NativeDisplay: nativeDisplay, CapturedAt: time.Now()}
	state.Unlock()
	return capture, nil
}

func executeActions(actions []Action, captureAfter bool, deadline time.Time) (map[string]any, error) {
	status := platformStatus()
	if !status.Supported {
		return nil, fmt.Errorf("desktop Computer Use is not supported on %s", runtime.GOOS)
	}
	needsInput := false
	for _, action := range actions {
		if !isObservationAction(action.Type) {
			needsInput = true
			break
		}
	}
	if needsInput && !status.AccessibilityAuthorized {
		return nil, inputPermissionError()
	}
	if (needsInput || captureAfter) && !status.ScreenCaptureAuthorized {
		return nil, screenCapturePermissionError(true)
	}

	results := make([]map[string]any, 0, len(actions))
	held := &heldKeys{}
	// Keys pressed with key_down never outlive the batch, even when it fails.
	defer held.releaseAll()
	var lastCapture *Capture
	for index, action := range actions {
		if time.Now().After(deadline) {
			return nil, partialFailure(results, fmt.Errorf("computer.execute timed out before action %d", index+1))
		}
		result, capture, err := executeAction(action, deadline, held)
		if err != nil {
			return nil, partialFailure(results, fmt.Errorf("computer.execute action %d (%s): %w", index+1, action.Type, err))
		}
		results = append(results, result)
		if capture != nil {
			lastCapture = capture
		}
	}
	held.releaseAll()
	if captureAfter {
		if needsInput {
			// Let the UI repaint after input so the observation reflects its effect.
			if settle := min(captureSettleDelay, time.Until(deadline)); settle > 0 {
				time.Sleep(settle)
			}
		}
		capture, err := captureScreen(defaultCaptureDisplay(), finalCaptureBudget(deadline))
		if err != nil {
			return nil, partialFailure(results, fmt.Errorf("computer.execute final screenshot: %w", err))
		}
		lastCapture = &capture
	}

	response := map[string]any{
		"actions":   results,
		"completed": len(results),
		"platform":  runtime.GOOS,
	}
	if lastCapture != nil {
		response["mimeType"] = lastCapture.MimeType
		response["data"] = lastCapture.Data
		response["width"] = lastCapture.Width
		response["height"] = lastCapture.Height
		response["display"] = lastCapture.Display
	}
	return response, nil
}

func isObservationAction(actionType string) bool {
	switch actionType {
	case "screenshot", "wait", "cursor_position":
		return true
	default:
		return false
	}
}

// finalCaptureBudget gives the post-batch observation a bounded budget even
// when the batch consumed most of its own timeout.
func finalCaptureBudget(deadline time.Time) time.Duration {
	return max(minFinalCaptureBudget, min(time.Until(deadline), defaultCaptureTimeout))
}

// partialFailure reports which actions already changed the desktop so the model
// does not blindly repeat them after a mid-batch failure.
func partialFailure(results []map[string]any, cause error) error {
	if len(results) == 0 {
		return cause
	}
	encoded, err := json.Marshal(results)
	if err != nil {
		encoded = []byte("[]")
	}
	return fmt.Errorf("%w; %d action(s) completed before the failure: %s; call computer.capture_screen to observe the current desktop before retrying", cause, len(results), encoded)
}

func executeAction(action Action, deadline time.Time, held *heldKeys) (map[string]any, *Capture, error) {
	result := map[string]any{"type": action.Type}
	switch action.Type {
	case "screenshot":
		displayID := defaultCaptureDisplay()
		if action.Display != nil {
			displayID = *action.Display
		}
		capture, err := captureScreen(displayID, time.Until(deadline))
		if err != nil {
			return nil, nil, err
		}
		result["width"], result["height"] = capture.Width, capture.Height
		result["display"] = capture.Display.ID
		return result, &capture, nil
	case "wait":
		duration := actionDuration(action, 500)
		if err := sleepUntil(deadline, time.Duration(duration)*time.Millisecond); err != nil {
			return nil, nil, err
		}
		result["durationMs"] = duration
		return result, nil, nil
	case "cursor_position":
		point, err := platformCursorPosition()
		if err != nil {
			return nil, nil, err
		}
		result["screenX"], result["screenY"] = point.X, point.Y
		geometry, err := currentScreenshotGeometry()
		if err != nil {
			result["onScreen"] = false
			result["frameError"] = err.Error()
			return result, nil, nil
		}
		x, y, onScreen := screenshotCoordinates(geometry, point)
		result["x"], result["y"], result["onScreen"] = x, y, onScreen
		result["display"] = geometry.NativeDisplay.ID
		return result, nil, nil
	case "type":
		if err := ensureScreenshotFrame(); err != nil {
			return nil, nil, err
		}
		if err := typeText(action.Text, deadline); err != nil {
			return nil, nil, err
		}
		result["characters"] = len([]rune(action.Text))
		return result, nil, nil
	case "keypress":
		if err := ensureScreenshotFrame(); err != nil {
			return nil, nil, err
		}
		for _, raw := range action.Keys {
			key, modifiers, err := parseKeyChord(raw, action.Modifiers)
			if err != nil {
				return nil, nil, err
			}
			// Modifiers already held by key_down must not be released by this chord.
			if err := platformPressKey(key, modifiers&^held.modifierMask()); err != nil {
				return nil, nil, err
			}
		}
		result["keys"] = len(action.Keys)
		return result, nil, nil
	case "key_down", "key_up":
		if err := ensureScreenshotFrame(); err != nil {
			return nil, nil, err
		}
		names := make([]string, 0, len(action.Keys))
		for _, raw := range action.Keys {
			parts, err := splitKeyChord(raw)
			if err != nil {
				return nil, nil, err
			}
			names = append(names, parts...)
		}
		if action.Type == "key_up" {
			for index := len(names) - 1; index >= 0; index-- {
				if err := held.release(names[index]); err != nil {
					return nil, nil, err
				}
			}
		} else {
			for _, name := range names {
				if err := held.press(name); err != nil {
					return nil, nil, err
				}
			}
		}
		result["keys"] = names
		return result, nil, nil
	case "scroll":
		if err := ensureScreenshotFrame(); err != nil {
			return nil, nil, err
		}
		var point *Point
		if action.X != nil {
			translated, err := screenshotPoint(*action.X, *action.Y)
			if err != nil {
				return nil, nil, err
			}
			point = &translated
		}
		err := withHeldModifiers(held, action.Modifiers, func() error {
			if point != nil {
				if err := platformMovePointer(point.X, point.Y); err != nil {
					return err
				}
			}
			return platformScroll(action.ScrollX, action.ScrollY)
		})
		if err != nil {
			return nil, nil, err
		}
		result["scroll_x"], result["scroll_y"] = action.ScrollX, action.ScrollY
		return result, nil, nil
	case "click", "double_click", "triple_click", "move":
		point, err := screenshotPoint(*action.X, *action.Y)
		if err != nil {
			return nil, nil, err
		}
		if action.Type == "move" {
			err = platformMovePointer(point.X, point.Y)
		} else {
			count := 1
			switch action.Type {
			case "double_click":
				count = 2
			case "triple_click":
				count = 3
			}
			err = withHeldModifiers(held, action.Modifiers, func() error {
				return platformClick(point.X, point.Y, action.Button, count)
			})
		}
		if err != nil {
			return nil, nil, err
		}
		result["x"], result["y"], result["button"] = *action.X, *action.Y, action.Button
		if len(action.Modifiers) > 0 {
			result["modifiers"] = action.Modifiers
		}
		return result, nil, nil
	case "drag":
		points := make([]Point, len(action.Path))
		for index, point := range action.Path {
			translated, err := screenshotPoint(point.X, point.Y)
			if err != nil {
				return nil, nil, fmt.Errorf("drag point %d: %w", index+1, err)
			}
			points[index] = translated
		}
		err := withHeldModifiers(held, action.Modifiers, func() error {
			return executeDrag(points, action.Button, actionDuration(action, 500), deadline)
		})
		if err != nil {
			return nil, nil, err
		}
		result["points"] = len(points)
		result["button"] = action.Button
		return result, nil, nil
	default:
		return nil, nil, fmt.Errorf("unsupported action type %q", action.Type)
	}
}

// typeText sends text in bounded chunks so long input honors the batch deadline.
func typeText(text string, deadline time.Time) error {
	runes := []rune(text)
	for start := 0; start < len(runes); start += typeChunkRunes {
		if time.Now().After(deadline) {
			return fmt.Errorf("computer.execute timeout exceeded after typing %d of %d characters", start, len(runes))
		}
		end := min(start+typeChunkRunes, len(runes))
		if err := platformTypeText(string(runes[start:end])); err != nil {
			return err
		}
	}
	return nil
}

// heldKeys tracks keys pressed with key_down (or implicitly for modifier
// clicks) so they are always released in reverse order.
type heldKeys struct {
	keys []string
}

func (h *heldKeys) index(key string) int {
	for index, value := range h.keys {
		if value == key {
			return index
		}
	}
	return -1
}

func (h *heldKeys) contains(key string) bool {
	return h.index(canonicalKeyName(key)) >= 0
}

func (h *heldKeys) press(key string) error {
	key = canonicalKeyName(key)
	if h.index(key) >= 0 {
		return nil
	}
	if err := platformKeyEvent(key, true); err != nil {
		return err
	}
	h.keys = append(h.keys, key)
	return nil
}

func (h *heldKeys) release(key string) error {
	key = canonicalKeyName(key)
	if index := h.index(key); index >= 0 {
		h.keys = append(h.keys[:index], h.keys[index+1:]...)
	}
	return platformKeyEvent(key, false)
}

func (h *heldKeys) releaseAll() {
	for index := len(h.keys) - 1; index >= 0; index-- {
		_ = platformKeyEvent(h.keys[index], false)
	}
	h.keys = nil
}

func (h *heldKeys) modifierMask() uint32 {
	mask := uint32(0)
	for _, key := range h.keys {
		if flag, ok := normalizeModifier(key); ok {
			mask |= flag
		}
	}
	return mask
}

func withHeldModifiers(held *heldKeys, modifiers []string, run func() error) error {
	pressed := make([]string, 0, len(modifiers))
	defer func() {
		for index := len(pressed) - 1; index >= 0; index-- {
			_ = held.release(pressed[index])
		}
	}()
	for _, modifier := range modifiers {
		name := canonicalKeyName(modifier)
		if held.contains(name) {
			continue
		}
		if err := held.press(name); err != nil {
			return err
		}
		pressed = append(pressed, name)
	}
	return run()
}

func executeDrag(points []Point, button string, durationMS int, deadline time.Time) error {
	if err := platformMouseDown(points[0].X, points[0].Y, button); err != nil {
		return err
	}
	mouseDown := true
	defer func() {
		if mouseDown {
			_ = platformMouseUp(points[len(points)-1].X, points[len(points)-1].Y, button)
		}
	}()

	stepDuration := time.Duration(0)
	if len(points) > 1 && durationMS > 0 {
		stepDuration = time.Duration(durationMS) * time.Millisecond / time.Duration(len(points)-1)
	}
	for _, point := range points[1:] {
		if stepDuration > 0 {
			if err := sleepUntil(deadline, stepDuration); err != nil {
				return err
			}
		}
		if err := platformMouseDrag(point.X, point.Y, button); err != nil {
			return err
		}
	}
	end := points[len(points)-1]
	if err := platformMouseUp(end.X, end.Y, button); err != nil {
		return err
	}
	mouseDown = false
	return nil
}

func currentScreenshotGeometry() (captureGeometry, error) {
	state.RLock()
	geometry := state.geometry
	state.RUnlock()
	if geometry.Display.PixelWidth <= 0 || geometry.Display.PixelHeight <= 0 {
		return captureGeometry{}, errors.New("no desktop screenshot coordinate frame; call computer.capture_screen first")
	}
	if time.Since(geometry.CapturedAt) > screenshotFrameAge {
		return captureGeometry{}, errors.New("desktop screenshot coordinate frame is stale; call computer.capture_screen again")
	}
	frame := geometry.NativeDisplay
	current, ok := findDisplay(platformStatus(), frame.ID)
	if !ok {
		return captureGeometry{}, fmt.Errorf("display %d of the last screenshot is no longer connected; call computer.capture_screen again", frame.ID)
	}
	if current.ID != frame.ID || current.OriginX != frame.OriginX || current.OriginY != frame.OriginY || current.PixelWidth != frame.PixelWidth || current.PixelHeight != frame.PixelHeight || current.PointWidth != frame.PointWidth || current.PointHeight != frame.PointHeight {
		return captureGeometry{}, errors.New("desktop display geometry changed; call computer.capture_screen again")
	}
	return geometry, nil
}

func ensureScreenshotFrame() error {
	_, err := currentScreenshotGeometry()
	return err
}

func screenshotPoint(x, y float64) (Point, error) {
	geometry, err := currentScreenshotGeometry()
	if err != nil {
		return Point{}, err
	}
	if x < 0 || y < 0 || x >= float64(geometry.Display.PixelWidth) || y >= float64(geometry.Display.PixelHeight) {
		return Point{}, fmt.Errorf("screenshot coordinate %.1f,%.1f is outside %dx%d", x, y, geometry.Display.PixelWidth, geometry.Display.PixelHeight)
	}
	return Point{
		X: geometry.Display.OriginX + x*geometry.Display.PointWidth/float64(geometry.Display.PixelWidth),
		Y: geometry.Display.OriginY + y*geometry.Display.PointHeight/float64(geometry.Display.PixelHeight),
	}, nil
}

// screenshotCoordinates is the inverse of screenshotPoint: it maps a global
// desktop point into the pixel frame of the last screenshot.
func screenshotCoordinates(geometry captureGeometry, point Point) (float64, float64, bool) {
	display := geometry.Display
	if display.PointWidth <= 0 || display.PointHeight <= 0 {
		return 0, 0, false
	}
	x := (point.X - display.OriginX) * float64(display.PixelWidth) / display.PointWidth
	y := (point.Y - display.OriginY) * float64(display.PixelHeight) / display.PointHeight
	onScreen := x >= 0 && y >= 0 && x < float64(display.PixelWidth) && y < float64(display.PixelHeight)
	return math.Round(x*10) / 10, math.Round(y*10) / 10, onScreen
}

func validateActions(actions []Action) error {
	if len(actions) < 1 || len(actions) > maxComputerActions {
		return fmt.Errorf("computer.execute requires 1 to %d actions", maxComputerActions)
	}
	for index := range actions {
		if err := validateAction(&actions[index]); err != nil {
			return fmt.Errorf("computer.execute action %d: %w", index+1, err)
		}
	}
	return nil
}

func validateAction(action *Action) error {
	action.Type = strings.ToLower(strings.TrimSpace(action.Type))
	action.Button = strings.ToLower(strings.TrimSpace(action.Button))
	switch action.Button {
	case "":
		action.Button = "left"
	case "middle":
		action.Button = "wheel"
	}
	if action.Button != "left" && action.Button != "right" && action.Button != "wheel" {
		return errors.New("button must be left, right, middle, or wheel")
	}
	if action.Display != nil && action.Type != "screenshot" {
		return errors.New("display is only supported by the screenshot action")
	}
	for _, coordinate := range []*float64{action.X, action.Y} {
		if coordinate != nil && (*coordinate < 0 || math.IsNaN(*coordinate) || math.IsInf(*coordinate, 0)) {
			return errors.New("coordinates must be finite non-negative numbers")
		}
	}
	if (action.X == nil) != (action.Y == nil) {
		return errors.New("x and y must be provided together")
	}
	if math.Abs(action.ScrollX) > 100_000 || math.Abs(action.ScrollY) > 100_000 {
		return errors.New("scroll values exceed the allowed range")
	}
	if len([]rune(action.Text)) > maxComputerText {
		return fmt.Errorf("text exceeds %d characters", maxComputerText)
	}
	if strings.ContainsRune(action.Text, '\x00') {
		return errors.New("text cannot contain a NUL character")
	}
	if len(action.Modifiers) > 4 {
		return errors.New("at most four modifiers are allowed")
	}
	for _, modifier := range action.Modifiers {
		if _, ok := normalizeModifier(modifier); !ok {
			return fmt.Errorf("unsupported modifier %q", modifier)
		}
	}
	if action.DurationMS != nil && (*action.DurationMS < 0 || *action.DurationMS > maxActionDuration) {
		return fmt.Errorf("durationMs must be between 0 and %d", maxActionDuration)
	}
	for _, point := range action.Path {
		if point.X < 0 || point.Y < 0 || math.IsNaN(point.X) || math.IsNaN(point.Y) || math.IsInf(point.X, 0) || math.IsInf(point.Y, 0) {
			return errors.New("drag path coordinates must be finite non-negative numbers")
		}
	}

	switch action.Type {
	case "screenshot", "wait", "cursor_position":
	case "click", "double_click", "triple_click", "move":
		if action.X == nil {
			return fmt.Errorf("%s requires x and y", action.Type)
		}
	case "scroll":
		if action.ScrollX == 0 && action.ScrollY == 0 {
			return errors.New("scroll requires scroll_x or scroll_y")
		}
	case "type":
		if action.Text == "" {
			return errors.New("type requires text")
		}
	case "keypress", "key_down", "key_up":
		if len(action.Keys) < 1 || len(action.Keys) > maxComputerKeys {
			return fmt.Errorf("%s requires 1 to %d keys", action.Type, maxComputerKeys)
		}
		for _, key := range action.Keys {
			if strings.TrimSpace(key) == "" || len(key) > 80 {
				return fmt.Errorf("%s keys must be non-empty and at most 80 bytes", action.Type)
			}
			if _, err := splitKeyChord(key); err != nil {
				return err
			}
		}
	case "drag":
		if len(action.Path) < 2 || len(action.Path) > maxDragPoints {
			return fmt.Errorf("drag requires 2 to %d path points", maxDragPoints)
		}
	default:
		return fmt.Errorf("unsupported action type %q", action.Type)
	}
	return nil
}

func actionDuration(action Action, fallback int) int {
	if action.DurationMS == nil {
		return fallback
	}
	return *action.DurationMS
}

func sleepUntil(deadline time.Time, duration time.Duration) error {
	if duration <= 0 {
		return nil
	}
	remaining := time.Until(deadline)
	if remaining <= 0 || duration > remaining {
		return errors.New("computer.execute timeout exceeded")
	}
	time.Sleep(duration)
	return nil
}

// splitKeyChord splits "ctrl+shift+a" into its parts. A literal plus key can be
// written as "+", "PLUS", or at the end of a chord as "ctrl++".
func splitKeyChord(raw string) ([]string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, errors.New("empty key chord")
	}
	if trimmed == "+" || trimmed == "++" {
		return []string{"+"}, nil
	}
	body, plusKey := trimmed, false
	if strings.HasSuffix(trimmed, "++") {
		body, plusKey = strings.TrimSuffix(trimmed, "++"), true
	}
	parts := strings.Split(body, "+")
	for index, part := range parts {
		parts[index] = strings.TrimSpace(part)
		if parts[index] == "" {
			return nil, fmt.Errorf("invalid key chord %q", raw)
		}
	}
	if plusKey {
		parts = append(parts, "+")
	}
	for index, part := range parts {
		parts[index] = canonicalKeyName(part)
	}
	return parts, nil
}

// canonicalKeyName normalizes key aliases so held-key bookkeeping and native
// lookups agree on one spelling per key.
func canonicalKeyName(key string) string {
	key = strings.TrimSpace(key)
	upper := strings.ToUpper(key)
	if upper == "PLUS" {
		return "+"
	}
	if flag, ok := normalizeModifier(upper); ok {
		switch flag {
		case 1:
			return "CTRL"
		case 2:
			return "SHIFT"
		case 4:
			return "ALT"
		case 8:
			return "META"
		}
	}
	if len([]rune(key)) == 1 {
		return key
	}
	return upper
}

func parseKeyChord(raw string, inherited []string) (string, uint32, error) {
	parts, err := splitKeyChord(raw)
	if err != nil {
		return "", 0, err
	}
	key := parts[len(parts)-1]
	modifiers := uint32(0)
	for _, modifier := range append(append([]string{}, inherited...), parts[:len(parts)-1]...) {
		flag, ok := normalizeModifier(modifier)
		if !ok {
			return "", 0, fmt.Errorf("unsupported modifier %q", modifier)
		}
		modifiers |= flag
	}
	return key, modifiers, nil
}

func normalizeModifier(value string) (uint32, bool) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "CTRL", "CONTROL":
		return 1, true
	case "SHIFT":
		return 2, true
	case "ALT", "OPTION":
		return 4, true
	case "META", "CMD", "COMMAND", "SUPER", "WIN", "WINDOWS":
		return 8, true
	default:
		return 0, false
	}
}
