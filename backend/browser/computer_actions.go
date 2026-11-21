package browser

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
	"unsafe"
)

type browserComputerPoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type browserComputerAction struct {
	Type       string                 `json:"type"`
	X          *float64               `json:"x"`
	Y          *float64               `json:"y"`
	Button     string                 `json:"button"`
	Text       string                 `json:"text"`
	Keys       []string               `json:"keys"`
	Modifiers  []string               `json:"modifiers"`
	ScrollX    float64                `json:"scroll_x"`
	ScrollY    float64                `json:"scroll_y"`
	Path       []browserComputerPoint `json:"path"`
	DurationMS int                    `json:"durationMs"`
}

type browserCapturePayload struct {
	OK       bool   `json:"ok"`
	Error    string `json:"error"`
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

type browserViewport struct {
	Width       float64 `json:"width"`
	Height      float64 `json:"height"`
	DeviceScale float64 `json:"deviceScale"`
	URL         string  `json:"url"`
	Title       string  `json:"title"`
}

type browserCaptureGeometry struct {
	ImageWidth     int
	ImageHeight    int
	ViewportWidth  float64
	ViewportHeight float64
	DeviceScale    float64
	URL            string
	CapturedAt     time.Time
}

const browserComputerFrameMaxAge = 2 * time.Minute

// Visual actions and captures share one coordinate frame. Serialize them so a
// second conversation cannot replace that frame while an action batch is using it.
var browserComputerOperationMu sync.Mutex

var browserComputerCaptures = struct {
	sync.RWMutex
	byTab map[string]browserCaptureGeometry
}{byTab: make(map[string]browserCaptureGeometry)}

func invalidateBrowserComputerCapture(tabID string) {
	browserComputerCaptures.Lock()
	delete(browserComputerCaptures.byTab, tabID)
	browserComputerCaptures.Unlock()
}

func invalidateAllBrowserComputerCaptures() {
	browserComputerCaptures.Lock()
	clear(browserComputerCaptures.byTab)
	browserComputerCaptures.Unlock()
}

func captureBrowserView(win unsafe.Pointer, tabID string, timeout int) (map[string]any, error) {
	browserComputerOperationMu.Lock()
	defer browserComputerOperationMu.Unlock()
	return captureBrowserViewLocked(win, tabID, timeout)
}

func captureBrowserViewLocked(win unsafe.Pointer, tabID string, timeout int) (map[string]any, error) {
	raw := platformCapture(win, tabID, timeout)
	var capture browserCapturePayload
	if err := json.Unmarshal([]byte(raw), &capture); err != nil {
		return nil, fmt.Errorf("decode browser capture: %w", err)
	}
	if !capture.OK {
		return nil, errors.New(capture.Error)
	}
	if capture.Width <= 0 || capture.Height <= 0 || strings.TrimSpace(capture.Data) == "" {
		return nil, errors.New("browser capture returned an empty image")
	}

	result := map[string]any{
		"mimeType": capture.MimeType,
		"data":     capture.Data,
		"width":    capture.Width,
		"height":   capture.Height,
		"tabId":    tabID,
	}
	var viewport browserViewport
	if err := evalValue(win, tabID, `({width:window.innerWidth,height:window.innerHeight,deviceScale:window.devicePixelRatio||1,url:location.href,title:document.title})`, 2000, &viewport); err != nil {
		invalidateBrowserComputerCapture(tabID)
		result["coordinateFrameAvailable"] = false
		result["coordinateFrameError"] = fmt.Sprintf("could not inspect browser viewport: %v", err)
		return result, nil
	}
	if viewport.Width <= 0 || viewport.Height <= 0 || viewport.DeviceScale <= 0 || strings.TrimSpace(viewport.URL) == "" {
		invalidateBrowserComputerCapture(tabID)
		result["coordinateFrameAvailable"] = false
		result["coordinateFrameError"] = "browser viewport returned invalid geometry"
		return result, nil
	}

	browserComputerCaptures.Lock()
	browserComputerCaptures.byTab[tabID] = browserCaptureGeometry{
		ImageWidth: capture.Width, ImageHeight: capture.Height,
		ViewportWidth: viewport.Width, ViewportHeight: viewport.Height,
		DeviceScale: viewport.DeviceScale, URL: viewport.URL,
		CapturedAt: time.Now(),
	}
	browserComputerCaptures.Unlock()

	result["viewportWidth"] = viewport.Width
	result["viewportHeight"] = viewport.Height
	result["deviceScale"] = viewport.DeviceScale
	result["url"] = viewport.URL
	result["title"] = viewport.Title
	result["coordinateFrameAvailable"] = true
	return result, nil
}

func browserComputerGeometryLocked(win unsafe.Pointer, tabID string) (browserCaptureGeometry, error) {
	browserComputerCaptures.RLock()
	geometry, ok := browserComputerCaptures.byTab[tabID]
	browserComputerCaptures.RUnlock()
	if !ok || geometry.ImageWidth <= 0 || geometry.ImageHeight <= 0 {
		return browserCaptureGeometry{}, errors.New("no browser screenshot coordinate frame; call browser.capture_view first")
	}
	if time.Since(geometry.CapturedAt) > browserComputerFrameMaxAge {
		return browserCaptureGeometry{}, errors.New("browser screenshot coordinate frame is stale; call browser.capture_view again")
	}
	var current browserViewport
	if err := evalValue(win, tabID, `({width:window.innerWidth,height:window.innerHeight,deviceScale:window.devicePixelRatio||1,url:location.href})`, 2_000, &current); err != nil {
		return browserCaptureGeometry{}, fmt.Errorf("validate browser screenshot coordinate frame: %w", err)
	}
	if current.Width != geometry.ViewportWidth || current.Height != geometry.ViewportHeight || current.DeviceScale != geometry.DeviceScale || current.URL != geometry.URL {
		return browserCaptureGeometry{}, errors.New("browser page or viewport changed; call browser.capture_view again")
	}
	return geometry, nil
}

func parseBrowserComputerActions(arguments map[string]any) ([]browserComputerAction, error) {
	raw, ok := arguments["actions"]
	if !ok {
		return nil, errors.New("browser.computer_action requires actions")
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("encode browser computer actions: %w", err)
	}
	var actions []browserComputerAction
	if err := json.Unmarshal(encoded, &actions); err != nil {
		return nil, fmt.Errorf("decode browser computer actions: %w", err)
	}
	if len(actions) == 0 || len(actions) > 32 {
		return nil, errors.New("browser.computer_action requires 1 to 32 actions")
	}
	for index := range actions {
		if err := validateBrowserComputerAction(&actions[index]); err != nil {
			return nil, fmt.Errorf("action %d: %w", index+1, err)
		}
	}
	return actions, nil
}

func validateBrowserComputerAction(action *browserComputerAction) error {
	action.Type = strings.ToLower(strings.TrimSpace(action.Type))
	action.Button = strings.ToLower(strings.TrimSpace(action.Button))
	if action.Button == "" {
		action.Button = "left"
	}
	if action.Button != "left" && action.Button != "right" && action.Button != "wheel" {
		return errors.New("button must be left, right, or wheel")
	}
	if (action.X == nil) != (action.Y == nil) {
		return errors.New("x and y must be provided together")
	}
	for _, coordinate := range []*float64{action.X, action.Y} {
		if coordinate != nil && (*coordinate < 0 || math.IsNaN(*coordinate) || math.IsInf(*coordinate, 0)) {
			return errors.New("coordinates must be finite non-negative numbers")
		}
	}
	if math.Abs(action.ScrollX) > 100_000 || math.Abs(action.ScrollY) > 100_000 || math.IsNaN(action.ScrollX) || math.IsNaN(action.ScrollY) || math.IsInf(action.ScrollX, 0) || math.IsInf(action.ScrollY, 0) {
		return errors.New("scroll values exceed the allowed range")
	}
	if len([]rune(action.Text)) > 20_000 {
		return errors.New("text is too long")
	}
	if strings.ContainsRune(action.Text, '\x00') {
		return errors.New("text cannot contain a NUL character")
	}
	if len(action.Modifiers) > 4 {
		return errors.New("at most four modifiers are allowed")
	}
	for _, modifier := range action.Modifiers {
		switch strings.ToUpper(strings.TrimSpace(modifier)) {
		case "CTRL", "CONTROL", "SHIFT", "ALT", "OPTION", "META", "CMD", "COMMAND":
		default:
			return fmt.Errorf("unsupported modifier %q", modifier)
		}
	}
	if action.DurationMS < 0 || action.DurationMS > 10_000 {
		return errors.New("durationMs must be between 0 and 10000")
	}
	for _, point := range action.Path {
		if point.X < 0 || point.Y < 0 || math.IsNaN(point.X) || math.IsNaN(point.Y) || math.IsInf(point.X, 0) || math.IsInf(point.Y, 0) {
			return errors.New("drag path coordinates must be finite non-negative numbers")
		}
	}

	switch action.Type {
	case "screenshot", "wait":
		return nil
	case "click", "double_click", "move":
		if action.X == nil {
			return fmt.Errorf("%s requires x and y", action.Type)
		}
		return nil
	case "scroll":
		if action.ScrollX == 0 && action.ScrollY == 0 {
			return errors.New("scroll requires scroll_x or scroll_y")
		}
		return nil
	case "type":
		if action.Text == "" {
			return errors.New("type requires text")
		}
		return nil
	case "keypress":
		if len(action.Keys) == 0 || len(action.Keys) > 16 {
			return errors.New("keypress requires 1 to 16 keys")
		}
		for _, key := range action.Keys {
			if strings.TrimSpace(key) == "" || len(key) > 80 {
				return errors.New("keypress keys must be non-empty and at most 80 bytes")
			}
		}
		return nil
	case "drag":
		if len(action.Path) < 2 || len(action.Path) > 128 {
			return errors.New("drag requires 2 to 128 path points")
		}
		return nil
	default:
		return fmt.Errorf("unsupported action type %q", action.Type)
	}
}

func executeBrowserComputerActions(win unsafe.Pointer, tabID string, arguments map[string]any) (map[string]any, error) {
	actions, err := parseBrowserComputerActions(arguments)
	if err != nil {
		return nil, err
	}
	captureAfter := true
	if raw, exists := arguments["captureAfter"]; exists {
		value, ok := raw.(bool)
		if !ok {
			return nil, errors.New("captureAfter must be a boolean")
		}
		captureAfter = value
	}
	timeout := numericArgument(arguments, "timeoutMs", 30_000, 1_000, 30_000)
	deadline := time.Now().Add(time.Duration(timeout) * time.Millisecond)

	browserComputerOperationMu.Lock()
	defer browserComputerOperationMu.Unlock()

	results := make([]any, 0, len(actions))
	var lastCapture map[string]any
	lastActionWasCapture := false
	for index, action := range actions {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("browser.computer_action timed out before action %d", index+1)
		}
		if action.Type == "wait" {
			duration := action.DurationMS
			if duration == 0 {
				duration = 500
			}
			if err := sleepBrowserComputer(deadline, time.Duration(duration)*time.Millisecond); err != nil {
				return nil, fmt.Errorf("browser computer action %d (wait): %w", index+1, err)
			}
			results = append(results, map[string]any{"type": action.Type, "durationMs": duration})
			lastActionWasCapture = false
			continue
		}
		if action.Type == "screenshot" {
			capture, err := captureBrowserViewLocked(win, tabID, remainingBrowserComputerTimeout(deadline, 10_000))
			if err != nil {
				return nil, fmt.Errorf("browser computer action %d (screenshot): %w", index+1, err)
			}
			lastCapture = capture
			results = append(results, map[string]any{"type": action.Type, "width": capture["width"], "height": capture["height"]})
			lastActionWasCapture = true
			continue
		}

		geometry, err := browserComputerGeometryLocked(win, tabID)
		if err != nil {
			return nil, fmt.Errorf("browser computer action %d (%s): %w", index+1, action.Type, err)
		}
		actionJSON, err := json.Marshal(action)
		if err != nil {
			return nil, fmt.Errorf("encode browser computer action %d: %w", index+1, err)
		}
		geometryJSON, err := json.Marshal(map[string]any{
			"imageWidth":     geometry.ImageWidth,
			"imageHeight":    geometry.ImageHeight,
			"viewportWidth":  geometry.ViewportWidth,
			"viewportHeight": geometry.ViewportHeight,
		})
		if err != nil {
			return nil, fmt.Errorf("encode browser computer geometry: %w", err)
		}
		if computerActionMayOpenPopup(action) {
			// Before the action: the page may open its window synchronously.
			allowAIPopupWindow(tabID)
		}
		var result any
		if err := evalMutation(win, tabID, pageScript("computer_action", json.RawMessage(actionJSON), json.RawMessage(geometryJSON)), remainingBrowserComputerTimeout(deadline, 5_000), &result); err != nil {
			return nil, fmt.Errorf("browser computer action %d (%s): %w", index+1, action.Type, err)
		}
		results = append(results, result)
		lastActionWasCapture = false
	}

	if captureAfter && !lastActionWasCapture {
		capture, err := captureBrowserViewLocked(win, tabID, remainingBrowserComputerTimeout(deadline, 10_000))
		if err != nil {
			return nil, err
		}
		lastCapture = capture
	}
	result := map[string]any{
		"ok":          true,
		"actions":     results,
		"actionCount": len(actions),
	}
	for key, value := range lastCapture {
		result[key] = value
	}
	return result, nil
}

func remainingBrowserComputerTimeout(deadline time.Time, maximum int) int {
	remaining := int(time.Until(deadline) / time.Millisecond)
	if remaining < 1 {
		return 1
	}
	if remaining > maximum {
		return maximum
	}
	return remaining
}

func sleepBrowserComputer(deadline time.Time, duration time.Duration) error {
	if duration <= 0 {
		return nil
	}
	remaining := time.Until(deadline)
	if remaining <= 0 || duration > remaining {
		return errors.New("browser.computer_action timeout exceeded")
	}
	time.Sleep(duration)
	return nil
}
