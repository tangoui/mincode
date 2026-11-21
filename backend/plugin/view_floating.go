package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"mincode/app/backend/agentprogress"
	"mincode/app/backend/core"
	"mincode/app/backend/desktop"
	"mincode/app/plugins/api"
)

// Floating views (contributes.views[].floating) are views the host shows in a
// small frameless, transparent, always-on-top window instead of a workspace
// tab: desktop pets, mini players, HUD widgets. The window renders the same
// sandboxed plugin UI session as any other view; the host owns dragging and
// the hide button, so the page itself never receives pointer input there.
//
//   - Only views that declare `floating` can be shown this way, at most
//     floatingViewsPerPlugin per plugin, and never as a tab, command target
//     or contribution-point view (a globalShortcuts entry of run.type
//     floating only shows or hides the window, see global_shortcuts.go).
//   - Windows open only for enabled plugins effective in the active
//     workspace, at the declared size (also the maximum; the page may shrink
//     it through resize) and never take focus from other applications.
//   - Visibility and position are host-owned state in the host KV
//     (`<pluginId>.floating`); restore reopens visible views, and a plugin
//     that becomes disabled or ineffective loses its windows.
const (
	floatingViewMinSize         = 48
	floatingViewMaxSize         = 480
	floatingViewsPerPlugin      = 2
	floatingPersistDelay        = 400 * time.Millisecond
	floatingWindowNamePrefix    = "plugin-floating:"
	pluginFloatingChangedEvent  = "plugin:floating-changed"
	floatingWindowURLModeParam  = "plugin-floating"
	floatingStateKVKeySuffix    = ".floating"
	floatingStateMaxViewEntries = 16
)

// ManifestFloating is views[].floating: the window size in CSS pixels, which
// is also the largest size the page may request.
type ManifestFloating struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

func normalizeFloating(floating *ManifestFloating) error {
	if floating == nil {
		return nil
	}
	if floating.Width < floatingViewMinSize || floating.Width > floatingViewMaxSize ||
		floating.Height < floatingViewMinSize || floating.Height > floatingViewMaxSize {
		return fmt.Errorf("floating width and height must be %d-%d", floatingViewMinSize, floatingViewMaxSize)
	}
	return nil
}

func cloneFloating(floating *ManifestFloating) *ManifestFloating {
	if floating == nil {
		return nil
	}
	copy := *floating
	return &copy
}

// FloatingWindowSpec describes one native floating window. X/Y are absolute
// desktop coordinates and only meaningful when Positioned is true; otherwise
// the driver picks a default spot.
type FloatingWindowSpec struct {
	Name       string
	Title      string
	URL        string
	Width      int
	Height     int
	X          int
	Y          int
	Positioned bool
}

// FloatingWindowDriver creates the native windows. The desktop app installs
// the desktop host implementation; without one floating views report unavailable.
type FloatingWindowDriver interface {
	// Open shows the named window, creating it when needed. moved reports the
	// new absolute position after the user drags it; closed runs once when the
	// native window goes away for any reason.
	Open(spec FloatingWindowSpec, moved func(x, y int), closed func()) error
	Close(name string)
	// Resize keeps the window's bottom-center point in place.
	Resize(name string, width, height int) error
}

type floatingViewState struct {
	Visible    bool `json:"visible"`
	Positioned bool `json:"positioned,omitempty"`
	X          int  `json:"x,omitempty"`
	Y          int  `json:"y,omitempty"`
}

type floatingPluginState struct {
	Views map[string]floatingViewState `json:"views"`
}

type floatingWindowRef struct {
	pluginID string
	viewID   string
}

var (
	floatingMu      sync.Mutex
	floatingDriver  FloatingWindowDriver
	floatingOpen    = map[string]floatingWindowRef{}
	floatingPersist = map[string]*time.Timer{}
)

// SetFloatingWindowDriver installs the native window implementation.
func SetFloatingWindowDriver(driver FloatingWindowDriver) {
	floatingMu.Lock()
	floatingDriver = driver
	floatingMu.Unlock()
}

func floatingWindowName(pluginID, viewID string) string {
	return floatingWindowNamePrefix + pluginID + "/" + viewID
}

func floatingWindowURL(pluginID, viewID string) string {
	return "/?mode=" + floatingWindowURLModeParam + "&pluginId=" + url.QueryEscape(pluginID) + "&viewId=" + url.QueryEscape(viewID)
}

func floatingStateKVKey(pluginID string) string {
	return pluginID + floatingStateKVKeySuffix
}

func readFloatingState(pluginID string) floatingPluginState {
	state := floatingPluginState{Views: map[string]floatingViewState{}}
	raw, found, err := pluginKVGetRaw(hostPluginID, floatingStateKVKey(pluginID))
	if err != nil || !found {
		return state
	}
	var stored floatingPluginState
	if json.Unmarshal(raw, &stored) != nil {
		return state
	}
	for viewID, view := range stored.Views {
		if len(state.Views) >= floatingStateMaxViewEntries {
			break
		}
		if contributionIDPattern.MatchString(viewID) {
			state.Views[viewID] = view
		}
	}
	return state
}

func writeFloatingState(pluginID string, state floatingPluginState) error {
	value, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return pluginKVSetRaw(hostPluginID, floatingStateKVKey(pluginID), value)
}

// updateFloatingView applies change to one view's persisted state. Call with floatingMu held.
func updateFloatingView(pluginID, viewID string, change func(*floatingViewState)) error {
	state := readFloatingState(pluginID)
	view := state.Views[viewID]
	change(&view)
	state.Views[viewID] = view
	return writeFloatingState(pluginID, state)
}

type floatingViewTarget struct {
	pluginID   string
	viewID     string
	pluginName string
	floating   ManifestFloating
}

// floatingView resolves a floating view of an enabled plugin that is
// effective in the active workspace.
func (manager *pluginManager) floatingView(pluginID, viewID string) (floatingViewTarget, error) {
	pluginID, viewID = strings.TrimSpace(pluginID), strings.TrimSpace(viewID)
	record, err := manager.record(pluginID)
	if err != nil {
		return floatingViewTarget{}, err
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	if record.manifest == nil || !manager.contributionsAvailableLocked(record) {
		return floatingViewTarget{}, fmt.Errorf("plugin %q is not available", pluginID)
	}
	for _, view := range record.manifest.Contributes.Views {
		if view.ID == viewID && view.Floating != nil {
			return floatingViewTarget{pluginID: pluginID, viewID: viewID, pluginName: record.manifest.Name, floating: *view.Floating}, nil
		}
	}
	return floatingViewTarget{}, fmt.Errorf("plugin %q has no floating view %q", pluginID, viewID)
}

func currentFloatingDriver() FloatingWindowDriver {
	floatingMu.Lock()
	defer floatingMu.Unlock()
	return floatingDriver
}

// openFloating opens target's window at its persisted position. The driver
// runs without floatingMu: native window calls may wait for the UI thread,
// whose move and close callbacks take the lock.
func openFloating(target floatingViewTarget, saved floatingViewState) error {
	driver := currentFloatingDriver()
	if driver == nil {
		return errors.New("floating windows are not available")
	}
	name := floatingWindowName(target.pluginID, target.viewID)
	spec := FloatingWindowSpec{
		Name: name, Title: target.pluginName, URL: floatingWindowURL(target.pluginID, target.viewID),
		Width: target.floating.Width, Height: target.floating.Height,
		X: saved.X, Y: saved.Y, Positioned: saved.Positioned,
	}
	moved := func(x, y int) { rememberFloatingPosition(target.pluginID, target.viewID, x, y) }
	closed := func() { forgetFloatingWindow(name) }
	if err := driver.Open(spec, moved, closed); err != nil {
		return err
	}
	floatingMu.Lock()
	floatingOpen[name] = floatingWindowRef{pluginID: target.pluginID, viewID: target.viewID}
	floatingMu.Unlock()
	return nil
}

func emitFloatingChanged(pluginID, viewID string, visible bool) {
	core.Emit(pluginFloatingChangedEvent, map[string]any{"pluginId": pluginID, "viewId": viewID, "visible": visible})
}

// ShowFloatingView opens the view's window and remembers it as visible.
func (manager *pluginManager) ShowFloatingView(pluginID, viewID string) error {
	target, err := manager.floatingView(pluginID, viewID)
	if err != nil {
		return err
	}
	floatingMu.Lock()
	saved := readFloatingState(target.pluginID).Views[target.viewID]
	floatingMu.Unlock()
	if err := openFloating(target, saved); err != nil {
		return err
	}
	floatingMu.Lock()
	err = updateFloatingView(target.pluginID, target.viewID, func(view *floatingViewState) { view.Visible = true })
	floatingMu.Unlock()
	emitFloatingChanged(target.pluginID, target.viewID, true)
	return err
}

// HideFloatingView closes the view's window and remembers it as hidden. It
// works for disabled plugins too, so a user can always dismiss a window.
func (manager *pluginManager) HideFloatingView(pluginID, viewID string) error {
	pluginID, viewID = strings.TrimSpace(pluginID), strings.TrimSpace(viewID)
	if _, err := manager.record(pluginID); err != nil {
		return err
	}
	if !contributionIDPattern.MatchString(viewID) {
		return fmt.Errorf("invalid view %q", viewID)
	}
	name := floatingWindowName(pluginID, viewID)
	floatingMu.Lock()
	driver := floatingDriver
	delete(floatingOpen, name)
	err := updateFloatingView(pluginID, viewID, func(view *floatingViewState) { view.Visible = false })
	floatingMu.Unlock()
	if driver != nil {
		driver.Close(name)
	}
	emitFloatingChanged(pluginID, viewID, false)
	return err
}

// FloatingViewVisible reports whether the view's window is open now.
func FloatingViewVisible(pluginID, viewID string) bool {
	floatingMu.Lock()
	defer floatingMu.Unlock()
	_, open := floatingOpen[floatingWindowName(strings.TrimSpace(pluginID), strings.TrimSpace(viewID))]
	return open
}

// ResizeFloatingView changes an open window's size within
// [floatingViewMinSize, declared size].
func (manager *pluginManager) ResizeFloatingView(pluginID, viewID string, width, height int) error {
	target, err := manager.floatingView(pluginID, viewID)
	if err != nil {
		return err
	}
	if width < floatingViewMinSize || height < floatingViewMinSize || width > target.floating.Width || height > target.floating.Height {
		return fmt.Errorf("floating size must be %d-%d x %d-%d", floatingViewMinSize, target.floating.Width, floatingViewMinSize, target.floating.Height)
	}
	name := floatingWindowName(target.pluginID, target.viewID)
	floatingMu.Lock()
	driver := floatingDriver
	_, open := floatingOpen[name]
	floatingMu.Unlock()
	if driver == nil || !open {
		return errors.New("floating view is not open")
	}
	return driver.Resize(name, width, height)
}

// RestoreFloatingViews reopens every floating view remembered as visible whose
// plugin is enabled and effective in the active workspace. Already open
// windows are left alone, so repeated calls are cheap.
func (manager *pluginManager) RestoreFloatingViews() int {
	manager.mu.RLock()
	records := manager.orderedRecordsLocked()
	manager.mu.RUnlock()
	var targets []floatingViewTarget
	for _, record := range records {
		record.mu.Lock()
		if record.manifest != nil && manager.contributionsAvailableLocked(record) {
			for _, view := range record.manifest.Contributes.Views {
				if view.Floating != nil {
					targets = append(targets, floatingViewTarget{pluginID: record.id, viewID: view.ID, pluginName: record.manifest.Name, floating: *view.Floating})
				}
			}
		}
		record.mu.Unlock()
	}
	opened := 0
	for _, target := range targets {
		floatingMu.Lock()
		saved := readFloatingState(target.pluginID).Views[target.viewID]
		_, open := floatingOpen[floatingWindowName(target.pluginID, target.viewID)]
		floatingMu.Unlock()
		if saved.Visible && !open && openFloating(target, saved) == nil {
			opened++
			emitFloatingChanged(target.pluginID, target.viewID, true)
		}
	}
	return opened
}

// closePluginFloatingWindows closes a plugin's windows without changing the
// remembered visibility, so they come back once the plugin is effective again.
func closePluginFloatingWindows(pluginID string) {
	floatingMu.Lock()
	driver := floatingDriver
	var closed []floatingWindowRef
	var names []string
	for name, ref := range floatingOpen {
		if ref.pluginID == pluginID {
			closed = append(closed, ref)
			names = append(names, name)
			delete(floatingOpen, name)
		}
	}
	floatingMu.Unlock()
	for index, name := range names {
		if driver != nil {
			driver.Close(name)
		}
		emitFloatingChanged(pluginID, closed[index].viewID, false)
	}
}

func rememberFloatingPosition(pluginID, viewID string, x, y int) {
	name := floatingWindowName(pluginID, viewID)
	floatingMu.Lock()
	defer floatingMu.Unlock()
	if timer := floatingPersist[name]; timer != nil {
		timer.Stop()
	}
	floatingPersist[name] = time.AfterFunc(floatingPersistDelay, func() {
		floatingMu.Lock()
		defer floatingMu.Unlock()
		delete(floatingPersist, name)
		_ = updateFloatingView(pluginID, viewID, func(view *floatingViewState) {
			view.X, view.Y, view.Positioned = x, y, true
		})
	})
}

func forgetFloatingWindow(name string) {
	floatingMu.Lock()
	ref, open := floatingOpen[name]
	delete(floatingOpen, name)
	floatingMu.Unlock()
	if open {
		emitFloatingChanged(ref.pluginID, ref.viewID, false)
	}
}

// FloatingActivation is what a click in a floating window may open in the
// main window. The frontend only forwards it right after the user clicked
// that window (a single-use token), so a plugin cannot pull MinCode to the
// front on its own.
//
//   - "app": bring the main window to the front;
//   - "view": open one of the plugin's own tab views (ViewID);
//   - "ai-run": open the conversation or agent session of an active AI run
//     (RunID, as received in ai.activity; needs capability ai.activity);
//   - "ai-runs": open the AI task list (needs capability ai.activity);
//   - "ai-run-cancel": stop an unfinished AI run (RunID, as received in
//     ai.tasks; needs capability ai.tasks). It does the same as the stop
//     button of the main window's task list and does not raise the window;
//     the single-use click token makes every stop a user click.
type FloatingActivation struct {
	Kind   string `json:"kind"`
	ViewID string `json:"viewId,omitempty"`
	RunID  string `json:"runId,omitempty"`
}

// floatingNavigate is a variable so tests need not drive the desktop window.
var floatingNavigate = desktop.Navigate

// floatingCancelRun is a variable so tests need not touch the shared registry.
var floatingCancelRun = func(runID string) error {
	if _, ok := agentprogress.Lookup(runID); !ok {
		return errors.New("the AI run is no longer available")
	}
	_, err := agentprogress.RequestCancel(runID)
	return err
}

// ActivateFloatingView opens target in the main window for a click in
// pluginID's floating view viewID.
func (manager *pluginManager) ActivateFloatingView(pluginID, viewID string, target FloatingActivation) error {
	source, err := manager.floatingView(pluginID, viewID)
	if err != nil {
		return err
	}
	record, err := manager.record(source.pluginID)
	if err != nil {
		return err
	}
	record.mu.Lock()
	manifest := record.manifest
	record.mu.Unlock()
	if manifest == nil {
		return errors.New("plugin is not available")
	}
	requireAIActivity := func() error {
		if !slices.Contains(manifest.Capabilities, api.CapabilityAIActivity) {
			return fmt.Errorf("opening AI runs needs the %s capability", api.CapabilityAIActivity)
		}
		return nil
	}
	switch target.Kind {
	case "app":
		return floatingNavigate(desktop.NavigationTarget{Kind: "app"})
	case "view":
		for _, view := range manifest.Contributes.Views {
			if view.ID == target.ViewID && view.Floating == nil {
				value, _ := json.Marshal(map[string]string{"pluginId": source.pluginID, "viewId": view.ID})
				return floatingNavigate(desktop.NavigationTarget{Kind: "plugin", Value: string(value)})
			}
		}
		return fmt.Errorf("plugin %q has no tab view %q", source.pluginID, target.ViewID)
	case "ai-run":
		if err := requireAIActivity(); err != nil {
			return err
		}
		if _, ok := agentprogress.Lookup(target.RunID); !ok {
			return errors.New("the AI run is no longer available")
		}
		return floatingNavigate(desktop.NavigationTarget{Kind: "agent-run", Value: target.RunID})
	case "ai-runs":
		if err := requireAIActivity(); err != nil {
			return err
		}
		return floatingNavigate(desktop.NavigationTarget{Kind: "agent-runs"})
	case "ai-run-cancel":
		if !slices.Contains(manifest.Capabilities, api.CapabilityAITasks) {
			return fmt.Errorf("stopping AI runs needs the %s capability", api.CapabilityAITasks)
		}
		return floatingCancelRun(target.RunID)
	}
	return fmt.Errorf("unsupported floating activation %q", target.Kind)
}

type floatingViewArgs struct {
	PluginID string              `json:"pluginId"`
	ViewID   string              `json:"viewId"`
	Width    int                 `json:"width,omitempty"`
	Height   int                 `json:"height,omitempty"`
	Target   *FloatingActivation `json:"target,omitempty"`
}

func init() {
	core.Register("plugin_host_floating_show", func(_, argsJSON string) (string, error) {
		var args floatingViewArgs
		if err := core.DecodeArgs(argsJSON, &args); err != nil {
			return "", err
		}
		if err := host().ShowFloatingView(args.PluginID, args.ViewID); err != nil {
			return "", err
		}
		return core.EncodeResult(map[string]any{"visible": true})
	})
	core.Register("plugin_host_floating_hide", func(_, argsJSON string) (string, error) {
		var args floatingViewArgs
		if err := core.DecodeArgs(argsJSON, &args); err != nil {
			return "", err
		}
		if err := host().HideFloatingView(args.PluginID, args.ViewID); err != nil {
			return "", err
		}
		return core.EncodeResult(map[string]any{"visible": false})
	})
	core.Register("plugin_host_floating_status", func(_, argsJSON string) (string, error) {
		var args floatingViewArgs
		if err := core.DecodeArgs(argsJSON, &args); err != nil {
			return "", err
		}
		return core.EncodeResult(map[string]any{"visible": FloatingViewVisible(args.PluginID, args.ViewID)})
	})
	core.Register("plugin_host_floating_resize", func(_, argsJSON string) (string, error) {
		var args floatingViewArgs
		if err := core.DecodeArgs(argsJSON, &args); err != nil {
			return "", err
		}
		if err := host().ResizeFloatingView(args.PluginID, args.ViewID, args.Width, args.Height); err != nil {
			return "", err
		}
		return core.EncodeResult(nil)
	})
	core.Register("plugin_host_floating_activate", func(_, argsJSON string) (string, error) {
		var args floatingViewArgs
		if err := core.DecodeArgs(argsJSON, &args); err != nil {
			return "", err
		}
		if args.Target == nil {
			return "", errors.New("activation target is required")
		}
		if err := host().ActivateFloatingView(args.PluginID, args.ViewID, *args.Target); err != nil {
			return "", err
		}
		return core.EncodeResult(true)
	})
	core.Register("plugin_host_floating_restore", func(_, _ string) (string, error) {
		return core.EncodeResult(map[string]any{"opened": host().RestoreFloatingViews()})
	})
	// Stop, crash, reload, disable, uninstall and workspace switches: keep the
	// windows while the plugin stays enabled and effective (the page reconnects
	// to the next process generation), close them otherwise.
	registerRecordLifecycleHook(func(manager *pluginManager, record *pluginRecord) {
		record.mu.Lock()
		keep := record.enabled && manager.workspaceActiveLocked(record)
		record.mu.Unlock()
		if !keep {
			closePluginFloatingWindows(record.id)
		}
	})
	registerRecordRemovalHook(func(_ *pluginManager, pluginID string, _ removalReason) {
		closePluginFloatingWindows(pluginID)
		prefix := floatingWindowName(pluginID, "")
		floatingMu.Lock()
		for name, timer := range floatingPersist {
			if strings.HasPrefix(name, prefix) {
				timer.Stop()
				delete(floatingPersist, name)
			}
		}
		_ = pluginKVDeleteRaw(hostPluginID, floatingStateKVKey(pluginID))
		floatingMu.Unlock()
	})
}
