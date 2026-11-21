package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"mincode/app/backend/core"
)

// Bounded invocation of a process plugin's action on behalf of a
// contribution point (formatters, dashboardWidgets). Every call is admitted
// by a per-plugin, per-point semaphore, carries a frontend request ID that
// plugin_host_contribution_cancel can cancel, and has its result checked to
// be JSON within a byte budget. Runtime protocol v1 has no cancel message:
// cancellation abandons the RPC on the host side and discards a late result.
//
// Errors are "<code>: detail" with the codes below; the frontend parses them
// with parseContributionError (services/plugin-contribution-requests.ts).
const (
	contributionErrNotFound       = "not_found"
	contributionErrForbidden      = "forbidden"
	contributionErrUnavailable    = "unavailable"
	contributionErrTooLarge       = "too_large"
	contributionErrTimeout        = "timeout"
	contributionErrCancelled      = "cancelled"
	contributionErrBusy           = "busy"
	contributionErrInvalidResult  = "invalid_result"
	contributionErrPluginError    = "plugin_error"
	contributionErrInvalidRequest = "invalid_request"
	// contributionErrInteractionRequired: a host.services call with
	// interactive:false would have needed a prompt.
	contributionErrInteractionRequired = "interaction_required"
)

const (
	// contributionRequestsMax bounds outstanding cancellable requests.
	contributionRequestsMax = 256
	// contributionResultDefaultMaxBytes applies when MaxResultBytes is unset.
	contributionResultDefaultMaxBytes = 1 << 20
	// contributionInvokeTimeoutMax caps contributedActionCall.Timeout. It is
	// above the host's default invoke timeout only for contract limits that
	// need more (debuggers resolveTimeoutMs, 60 s: a first Maven/Gradle
	// classpath resolution is slow).
	contributionInvokeTimeoutMax = 60 * time.Second
)

var contributionRequestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

// contributionError is a coded contribution invoke error. Point Validate
// callbacks may return one (newContributionError) to choose the code; any
// other Validate error is reported as unavailable.
type contributionError struct {
	Code   string
	Detail string
	// activation marks a failure to start the plugin for the call (lazy
	// activation), as opposed to a refused or invalid call.
	activation bool
}

func (err *contributionError) Error() string {
	if err.Detail == "" {
		return err.Code
	}
	return err.Code + ": " + err.Detail
}

func newContributionError(code, format string, args ...any) error {
	return &contributionError{Code: code, Detail: fmt.Sprintf(format, args...)}
}

// contributedActionCall describes one bounded action invocation.
type contributedActionCall struct {
	PluginID, Point, EntryID, ActionID, RequestID string
	Payload                                       []byte
	// Timeout covers lazy activation and the call; 0 means the host's invoke
	// timeout (30 s) and values above contributionInvokeTimeoutMax (60 s) are
	// capped there.
	Timeout time.Duration
	// MaxResultBytes bounds the JSON result; 0 means 1 MiB.
	MaxResultBytes int
	// Concurrency is the per-plugin, per-point slot count; 0 means 1.
	Concurrency int
	// SlotGroup splits a point's slots into separate pools: a non-empty
	// group counts against its own Concurrency (languageFeatures uses
	// "passive" so passive kinds never crowd out hover and completion). Empty
	// keeps the point's single pool.
	SlotGroup string
	// ActionsOnly marks calls that target contributes.actions directly
	// (plugins.invoke) rather than an entry of a contribution point, so the
	// target need not declare any contribution point to be callable.
	ActionsOnly bool
	// Validate is re-run under record.mu before lazy activation and before
	// dispatch, after the framework's own checks (record listed and
	// workspace-effective, action declared, payload within its
	// maxPayloadBytes). It checks the entry still exists with the same
	// action. It must not block or take other locks.
	Validate func(record *pluginRecord) error
	// Prepare, when set, builds the payload once the call is admitted: the
	// concurrency slot is held and the request is registered, so host-side
	// work such as reading a repository is bounded by the plugin's
	// concurrency and stopped by a cancel (ctx is the admitted call's
	// context, with its timeout). Payload is ignored; the built payload must
	// still fit the action's maxPayloadBytes.
	Prepare func(ctx context.Context) ([]byte, error)
	// PrepareAdmitted is mutually exclusive with Prepare and can inspect the
	// initialized runtime at the admission's exact generation.
	PrepareAdmitted func(context.Context, *contributedAdmission) ([]byte, error)
	// gesture is the global shortcut press behind a globalShortcuts call
	// (global_shortcuts.go); unexported so no decoded request can carry one.
	gesture *globalShortcutGesture
}

type contributionRequest struct {
	pluginID string
	cancel   context.CancelFunc
	reason   string // set under contributionRequests.mu before cancel
}

var contributionRequests = struct {
	mu       sync.Mutex
	requests map[string]*contributionRequest
	slots    map[string]int // contributedActionSlotKey → in-flight calls
}{requests: map[string]*contributionRequest{}, slots: map[string]int{}}

// contributedAdmission is an admitted call: the request is registered (so
// plugin_host_contribution_cancel and the plugin's lifecycle cancel Ctx), a
// concurrency slot and an in-flight count are held, and the plugin is running
// with every check passed under record.mu at Generation.
type contributedAdmission struct {
	// Ctx is cancelled by a cancel request, the plugin's lifecycle and the
	// parent context; it carries the parent's deadline.
	Ctx        context.Context
	Runtime    *processRuntime
	Generation uint64
	request    *contributionRequest
	cancel     context.CancelFunc
	finishOnce sync.Once
	finishFn   func()
	releaseFn  func()
	once       sync.Once
}

// finish unregisters the request: it can no longer be cancelled by ID.
func (admission *contributedAdmission) finish() { admission.finishOnce.Do(admission.finishFn) }

// Release frees the slot and the in-flight count, unregisters the request and
// cancels Ctx. It is idempotent.
func (admission *contributedAdmission) Release() {
	admission.finish()
	admission.once.Do(func() {
		admission.cancel()
		admission.releaseFn()
	})
}

// classify maps a dispatch error to a coded contribution error.
func (admission *contributedAdmission) classify(err error) error {
	return classifyContributionInvokeError(admission.Ctx, admission.request, err)
}

// admitContributedAction runs every check of a contributed action call
// (request ID, process runtime, contributions listed and workspace-effective,
// action declared, payload within maxPayloadBytes, call.Validate), takes the
// per-plugin, per-point slot, registers the cancellable request and lazily
// activates the plugin, re-running the checks before activation and once more
// under record.mu when it captures the runtime. call.Timeout is not applied:
// ctx bounds activation (startup is bounded on its own as well). The caller
// dispatches under the returned Ctx and calls Release when the call is over.
func (manager *pluginManager) admitContributedAction(ctx context.Context, call contributedActionCall) (*contributedAdmission, error) {
	if call.Prepare != nil && call.PrepareAdmitted != nil {
		return nil, newContributionError(contributionErrInvalidRequest, "Prepare and PrepareAdmitted are mutually exclusive")
	}
	if !contributionRequestIDPattern.MatchString(call.RequestID) {
		return nil, newContributionError(contributionErrInvalidRequest, "requestId must match %s", contributionRequestIDPattern)
	}
	if err := admitGlobalShortcutGesture(call); err != nil {
		return nil, err
	}
	record, err := manager.record(call.PluginID)
	if err != nil {
		return nil, newContributionError(contributionErrNotFound, "plugin %q not found", call.PluginID)
	}
	validate := func(record *pluginRecord) error {
		if record.builtin != nil || record.manifest == nil || record.manifest.isDeclarative() {
			return newContributionError(contributionErrForbidden, "plugin %q has no process runtime", call.PluginID)
		}
		listed := record.contributionsListedLocked()
		if call.ActionsOnly {
			listed = record.state != pluginStateError && record.contributionsAvailableLocked()
		}
		if !listed || !manager.workspaceActiveLocked(record) {
			return newContributionError(contributionErrUnavailable, "plugin %q contributions are not active", call.PluginID)
		}
		action, ok := contributedActionLocked(record, call.ActionID)
		if !ok {
			return newContributionError(contributionErrNotFound, "action %q is not declared", call.ActionID)
		}
		// A global shortcut press approves write actions, never dangerous ones.
		if call.Point == globalShortcutPointKey && (action.Risk == "dangerous" || !action.UICallable) {
			return newContributionError(contributionErrForbidden, "action %q cannot run from a global shortcut", call.ActionID)
		}
		if len(call.Payload) > action.MaxPayloadBytes {
			return newContributionError(contributionErrTooLarge, "payload is %d bytes, action %q allows %d", len(call.Payload), call.ActionID, action.MaxPayloadBytes)
		}
		if call.Validate != nil {
			if err := call.Validate(record); err != nil {
				var coded *contributionError
				if errors.As(err, &coded) {
					return err
				}
				return newContributionError(contributionErrUnavailable, "%s %q: %v", call.Point, call.EntryID, err)
			}
		}
		return nil
	}
	record.mu.Lock()
	err = validate(record)
	record.mu.Unlock()
	if err != nil {
		return nil, err
	}

	concurrency := call.Concurrency
	if concurrency <= 0 {
		concurrency = 1
	}
	callCtx, cancel := context.WithCancel(ctx)
	request := &contributionRequest{pluginID: call.PluginID, cancel: cancel}
	slot := contributedActionSlotKey(call)
	contributionRequests.mu.Lock()
	switch {
	case contributionRequests.slots[slot] >= concurrency:
		err = newContributionError(contributionErrBusy, "%s calls for plugin %q are at the limit of %d", call.Point, call.PluginID, concurrency)
	case contributionRequests.requests[call.RequestID] != nil:
		err = newContributionError(contributionErrBusy, "request %q is already in flight", call.RequestID)
	case len(contributionRequests.requests) >= contributionRequestsMax:
		err = newContributionError(contributionErrBusy, "too many outstanding contribution requests")
	default:
		contributionRequests.slots[slot]++
		contributionRequests.requests[call.RequestID] = request
	}
	contributionRequests.mu.Unlock()
	if err != nil {
		cancel()
		return nil, err
	}
	record.inflight.Add(1)
	admission := &contributedAdmission{Ctx: callCtx, request: request, cancel: cancel,
		finishFn: func() {
			contributionRequests.mu.Lock()
			if contributionRequests.requests[call.RequestID] == request {
				delete(contributionRequests.requests, call.RequestID)
			}
			contributionRequests.mu.Unlock()
		},
		releaseFn: func() {
			contributionRequests.mu.Lock()
			if contributionRequests.slots[slot]--; contributionRequests.slots[slot] <= 0 {
				delete(contributionRequests.slots, slot)
			}
			contributionRequests.mu.Unlock()
			record.inflight.Add(-1)
			// Woken only for this call (no view open): stop again once it is quiet.
			if manager.uiHost != nil && manager.uiHost.SessionCount(call.PluginID) == 0 {
				manager.scheduleIdleStop(call.PluginID, pluginIdleAfterInvokeDelay)
			}
		},
	}
	fail := func(err error) (*contributedAdmission, error) {
		classified := admission.classify(err)
		admission.Release()
		return nil, classified
	}
	// failActivation reports that the plugin could not be started; checks the
	// activation re-ran (validate) keep their own code.
	failActivation := func(err error) (*contributedAdmission, error) {
		var coded *contributionError
		if errors.As(err, &coded) {
			return fail(err)
		}
		_, classified := fail(err)
		if errors.As(classified, &coded) {
			coded.activation = true
		}
		return nil, classified
	}
	if err := manager.startRecordValidated(callCtx, record, validate); err != nil {
		return failActivation(err)
	}
	record.mu.Lock()
	enabled, state, runtimeHost := record.enabled, record.state, record.runtime
	if runtimeHost != nil {
		admission.Generation = runtimeHost.Generation()
	}
	err = validate(record)
	record.mu.Unlock()
	switch {
	case err != nil:
		return fail(err)
	case !enabled:
		return fail(fmt.Errorf("plugin %q is disabled", call.PluginID))
	case state != pluginStateRunning:
		return failActivation(fmt.Errorf("plugin %q is not running (state: %s)", call.PluginID, state))
	case runtimeHost == nil:
		return failActivation(fmt.Errorf("plugin %q has no process runtime", call.PluginID))
	}
	if err := callCtx.Err(); err != nil {
		return fail(err)
	}
	admission.Runtime = runtimeHost
	return admission, nil
}

// invokeContributedAction invokes call.ActionID on call.PluginID with the
// bounds described on contributedActionCall and returns the JSON result.
func (manager *pluginManager) invokeContributedAction(ctx context.Context, call contributedActionCall) (json.RawMessage, error) {
	timeout := call.Timeout
	switch {
	case timeout <= 0:
		timeout = pluginInvokeTimeout
	case timeout > contributionInvokeTimeoutMax:
		timeout = contributionInvokeTimeoutMax
	}
	maxResult := call.MaxResultBytes
	if maxResult <= 0 {
		maxResult = contributionResultDefaultMaxBytes
	}
	timed, cancelTimeout := context.WithTimeout(ctx, timeout)
	defer cancelTimeout()
	admission, err := manager.admitContributedAction(timed, call)
	if err != nil {
		return nil, err
	}
	callCtx := admission.Ctx
	payload, err := manager.prepareAdmittedPayload(admission, call)
	if err != nil {
		classified := admission.classify(err)
		admission.Release()
		return nil, classified
	}
	call.Payload = payload

	type invokeOutcome struct {
		result string
		err    error
	}
	done := make(chan invokeOutcome, 1)
	go func() {
		// The slot is held until the underlying call returns, so abandoned
		// calls still count against the plugin's concurrency.
		defer admission.Release()
		result, err := admission.Runtime.InvokeGeneration(callCtx, admission.Generation, call.ActionID, string(call.Payload))
		done <- invokeOutcome{result, err}
	}()
	var outcome invokeOutcome
	select {
	case outcome = <-done:
	case <-callCtx.Done():
		outcome.err = callCtx.Err()
	}
	admission.finish()
	if outcome.err != nil {
		return nil, admission.classify(outcome.err)
	}
	result := strings.TrimSpace(outcome.result)
	if len(result) > maxResult {
		return nil, newContributionError(contributionErrInvalidResult, "result is %d bytes, the limit is %d", len(result), maxResult)
	}
	if !json.Valid([]byte(result)) {
		return nil, newContributionError(contributionErrInvalidResult, "result is not JSON")
	}
	return json.RawMessage(result), nil
}

// prepareAdmittedPayload chooses one builder only after initialization and
// admission. Preparation may block across a lifecycle change, so recheck the
// original instance/generation and action before dispatching the final bytes.
func (manager *pluginManager) prepareAdmittedPayload(admission *contributedAdmission, call contributedActionCall) ([]byte, error) {
	if call.Prepare != nil && call.PrepareAdmitted != nil {
		return nil, newContributionError(contributionErrInvalidRequest, "Prepare and PrepareAdmitted are mutually exclusive")
	}
	ctx := admission.Ctx
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	payload := call.Payload
	var err error
	switch {
	case call.PrepareAdmitted != nil:
		payload, err = call.PrepareAdmitted(ctx, admission)
	case call.Prepare != nil:
		payload, err = call.Prepare(ctx)
	default:
		// Preserve legacy raw Payload behavior (including non-JSON payloads).
		// Admission checked its action limit; InvokeGeneration checks dispatch.
		return payload, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if err != nil {
		return nil, err
	}
	record, err := manager.record(call.PluginID)
	if err != nil {
		return nil, newContributionError(contributionErrNotFound, "plugin %q not found", call.PluginID)
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	if call.PrepareAdmitted != nil {
		if err := manager.validateRuntimeIdentityLocked(record, admission.Runtime.identity(admission.Generation)); err != nil {
			return nil, err
		}
	}
	action, ok := contributedActionLocked(record, call.ActionID)
	if !ok {
		return nil, newContributionError(contributionErrUnavailable, "action %q is no longer declared", call.ActionID)
	}
	if call.PrepareAdmitted != nil && call.Validate != nil {
		if err := call.Validate(record); err != nil {
			var coded *contributionError
			if errors.As(err, &coded) {
				return nil, err
			}
			return nil, newContributionError(contributionErrUnavailable, "%s %q: %v", call.Point, call.EntryID, err)
		}
	}
	if call.PrepareAdmitted != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if len(payload) > action.MaxPayloadBytes {
		return nil, newContributionError(contributionErrTooLarge, "payload is %d bytes, action %q allows %d", len(payload), call.ActionID, action.MaxPayloadBytes)
	}
	// Only the new seam promises JSON. Old Prepare callers may use raw text.
	if call.PrepareAdmitted != nil && !json.Valid(payload) {
		return nil, newContributionError(contributionErrInvalidRequest, "prepared payload is not JSON")
	}
	return payload, nil
}

// contributedActionSlotKey is the contributionRequests.slots key of a call:
// pluginID + "\x00" + point, plus "\x00" + SlotGroup when the call names one.
func contributedActionSlotKey(call contributedActionCall) string {
	key := call.PluginID + "\x00" + call.Point
	if call.SlotGroup != "" {
		key += "\x00" + call.SlotGroup
	}
	return key
}

func contributedActionLocked(record *pluginRecord, actionID string) (ManifestUIAction, bool) {
	for _, action := range record.manifest.Contributes.Actions {
		if action.ID == actionID {
			return action, true
		}
	}
	return ManifestUIAction{}, false
}

func classifyContributionInvokeError(callCtx context.Context, request *contributionRequest, err error) error {
	var coded *contributionError
	if errors.As(err, &coded) {
		return err
	}
	contributionRequests.mu.Lock()
	reason := request.reason
	contributionRequests.mu.Unlock()
	switch {
	case reason == contributionErrCancelled:
		return newContributionError(contributionErrCancelled, "request was cancelled")
	case reason == contributionErrUnavailable:
		return newContributionError(contributionErrUnavailable, "plugin %q stopped", request.pluginID)
	case errors.Is(callCtx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded):
		return newContributionError(contributionErrTimeout, "plugin %q did not answer in time", request.pluginID)
	case errors.Is(err, context.Canceled):
		return newContributionError(contributionErrCancelled, "request was cancelled")
	}
	var rpcErr *processRPCError
	if errors.As(err, &rpcErr) {
		if rpcErr.Code == pluginRPCBusyCode {
			return newContributionError(contributionErrBusy, "plugin %q is busy: %s", request.pluginID, rpcErr.Message)
		}
		return newContributionError(contributionErrPluginError, "%s", rpcErr.Message)
	}
	return newContributionError(contributionErrUnavailable, "%v", err)
}

// pluginRPCBusyCode is the JSON-RPC error the plugin SDKs return while a
// previous invoke is still running.
const pluginRPCBusyCode = -32029

// cancelContributionRequest cancels one outstanding request; it reports
// whether the request was still in flight.
func cancelContributionRequest(requestID string) bool {
	contributionRequests.mu.Lock()
	request := contributionRequests.requests[requestID]
	if request != nil {
		delete(contributionRequests.requests, requestID)
		request.reason = contributionErrCancelled
	}
	contributionRequests.mu.Unlock()
	if request == nil {
		return false
	}
	request.cancel()
	return true
}

// cancelPluginContributionRequests cancels every outstanding request of one
// plugin (lifecycle hook: stop, crash, disable, reload, uninstall).
func cancelPluginContributionRequests(pluginID string) {
	var cancels []context.CancelFunc
	contributionRequests.mu.Lock()
	for requestID, request := range contributionRequests.requests {
		if request.pluginID == pluginID {
			delete(contributionRequests.requests, requestID)
			request.reason = contributionErrUnavailable
			cancels = append(cancels, request.cancel)
		}
	}
	contributionRequests.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

type contributionCancelRequest struct {
	RequestID string `json:"requestId"`
}

type contributionCancelResponse struct {
	Cancelled bool `json:"cancelled"`
}

func pluginHostContributionCancelHandler(_, args string) (string, error) {
	var request contributionCancelRequest
	if err := json.Unmarshal([]byte(args), &request); err != nil {
		return "", fmt.Errorf("plugin_host_contribution_cancel: invalid arguments: %w", err)
	}
	if !contributionRequestIDPattern.MatchString(request.RequestID) {
		return "", newContributionError(contributionErrInvalidRequest, "requestId must match %s", contributionRequestIDPattern)
	}
	return core.EncodeResult(contributionCancelResponse{Cancelled: cancelContributionRequest(request.RequestID)})
}

func init() {
	core.Register("plugin_host_contribution_cancel", pluginHostContributionCancelHandler)
	registerRecordLifecycleHook(func(_ *pluginManager, record *pluginRecord) {
		cancelPluginContributionRequests(record.id)
	})
}
