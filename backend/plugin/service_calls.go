package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"mincode/app/backend/core"
)

// Service calls (design §7.3, §7.4). One call runs one provider entry's
// action with the §7.3 envelope as payload. A provider that negotiated
// features.tasks always runs it as a task (plugin.task.*, progress, cancel,
// the entry's timeoutMs); any other provider gets a plain plugin.invoke of at
// most shortTimeoutMs, and an entry that needs longer fails before dispatch.
// Both paths are admitted by admitContributedAction, so they share the
// contribution checks, lazy activation and the provider's concurrency slots.
//
// A call belongs to its caller: a plugin (owner = its ID) or the host (owner
// serviceCallHostOwner, which no plugin ID can equal). Only the owner can
// read or cancel it; anyone else gets not_found. Snapshots stay readable for snapshotRetentionMs after the call ends. A call
// whose input or output is file(s) holds an exchange directory
// (service_exchange.go) for its input snapshots and the provider's outputs;
// outputs survive only a successful call.

const (
	serviceCallRunning   serviceCallState = "running"
	serviceCallSucceeded serviceCallState = "succeeded"
	serviceCallFailed    serviceCallState = "failed"
	serviceCallCancelled serviceCallState = "cancelled"

	// serviceCallChangedEvent carries a ServiceCallSnapshot of a host call
	// each time it changes after start.
	serviceCallChangedEvent = "plugin:service-call-changed"
	// serviceCallHostOwner owns the host's calls and outputs. It contains
	// ':', which plugin IDs never do, so no plugin can act as the host.
	serviceCallHostOwner    = "mincode:host"
	serviceCallIDPrefix     = "sc_"
	serviceCallErrorMaxRune = 2000
	// serviceCallPhaseInput marks a call whose input files are still being
	// prepared (a remote file being read); the provider has not run yet.
	serviceCallPhaseInput = "input"
	// serviceInputProgressInterval spaces the input phase's change events.
	serviceInputProgressInterval = 250 * time.Millisecond
	// servicePageTaskHeadroom is how many of a provider's taskLimit tasks
	// service calls never take (see serviceProviderConcurrency).
	servicePageTaskHeadroom = 16
)

var (
	serviceShortTimeout            = time.Duration(serviceSpec.Limit("shortTimeoutMs")) * time.Millisecond
	serviceResultMaxBytes          = int(serviceSpec.Limit("resultMaxBytes"))
	serviceProgressMaxBytes        = int(serviceSpec.Limit("progressMaxBytes"))
	serviceProgressMessageMaxRunes = int(serviceSpec.Limit("progressMessageMaxRunes"))
	serviceSnapshotRetention       = time.Duration(serviceSpec.Limit("snapshotRetentionMs")) * time.Millisecond
	serviceCallsPerCaller          = int(serviceSpec.Limit("callsPerCaller"))
	// serviceProviderConcurrency bounds one provider's admitted calls below
	// the number of tasks a runtime keeps, leaving servicePageTaskHeadroom
	// tasks for the provider's own pages; the spec's limits are per caller
	// (callsPerCaller) and for calls with files (fileCallsConcurrent).
	serviceProviderConcurrency = taskLimit - servicePageTaskHeadroom
	// serviceTaskResultLimit bounds a service task's raw result at the
	// default stdout line cap, the same transport bound plugin.invoke has, so
	// both paths admit an output of resultMaxBytes plus its wrapper and
	// escaping; the output itself is checked against resultMaxBytes after
	// decoding (too_large, confirmed).
	serviceTaskResultLimit = pluginRPCMaxMessage

	// serviceCallNow is the clock for snapshot retention and the input
	// phase's event spacing (test seam).
	serviceCallNow = time.Now
	// serviceCallEmit sends a host call's change event (test seam).
	serviceCallEmit = core.Emit
)

type serviceCallState = string

// ServiceProgress is the normalized progress of a call: a bar when Total is
// set, indeterminate otherwise, with Message as plain text below it.
type ServiceProgress struct {
	Completed *float64 `json:"completed,omitempty"`
	Total     *float64 `json:"total,omitempty"`
	Message   string   `json:"message,omitempty"`
}

// ServiceCallSnapshot is what callers see. Output is the JSON string (text)
// or value (json) of a successful call; file outputs are in Files.
// RawProgress is the provider's progress as reported (≤ progressMaxBytes),
// for plugin callers. Error is "<code>: detail". Unconfirmed marks a failed
// call whose provider never confirmed the outcome: side effects may have
// happened. Phase is "input" while the host still prepares the input files
// (Progress then counts their bytes) and empty once the provider runs; a
// failed call keeps "input" only when the input preparation itself failed
// (a failure after it, such as activating the provider, has no phase).
type ServiceCallSnapshot struct {
	CallID      string             `json:"callId"`
	State       string             `json:"state"`
	Phase       string             `json:"phase,omitempty"`
	Provider    serviceProviderRef `json:"provider"`
	Progress    *ServiceProgress   `json:"progress,omitempty"`
	RawProgress json.RawMessage    `json:"rawProgress,omitempty"`
	Output      json.RawMessage    `json:"output,omitempty"`
	Files       []exchangeFile     `json:"files,omitempty"`
	Error       string             `json:"error,omitempty"`
	Unconfirmed bool               `json:"unconfirmed,omitempty"`
}

// serviceCaller is who asks: a plugin (PluginID) or the host from a surface.
type serviceCaller struct {
	Kind              string // "plugin" | "host"
	PluginID, Surface string
	Snapshot          serviceCallerSnapshot `json:"-"`
}

// serviceCallRequest is one resolved call: the provider is already chosen
// and the call approved. Input is the JSON string for text input. Files are
// input files already in the exchange; PrepareInputs, when set, adds more
// once the call's exchange directory exists (snapshots, adopted uploads).
// PrepareInputsAsync does the same in the background, after the call has
// started in phase "input": it must honour ctx (the call's cancellation) and
// may report its progress in bytes. It is called exactly when
// startServiceCall succeeds.
type serviceCallRequest struct {
	Caller             serviceCaller
	Provider           serviceProvider
	Input              json.RawMessage
	Params             map[string]any
	Files              []exchangeFile
	PrepareInputs      func(callID string) ([]exchangeFile, error)
	PrepareInputsAsync func(ctx context.Context, callID string, report func(done, total int64)) ([]exchangeFile, error)
	// SurfaceContext is the host surface's extra context for the provider,
	// already validated and trimmed for it (gateSurfaceContext); it reaches
	// the envelope only on host calls from the newer surfaces.
	SurfaceContext json.RawMessage
	// Revalidate, when set, adds the caller's own re-check of the provider
	// (for example the effective risk the user approved) to the one made
	// before activation and dispatch. It runs with record.mu held.
	Revalidate func(record *pluginRecord) error
}

type serviceCall struct {
	manager         *pluginManager
	id, owner       string
	host            bool
	entry           ServiceContribution
	providerID      string
	timeout         time.Duration
	snapshot        ServiceCallSnapshot
	cancel          context.CancelFunc
	cancelRequested bool
	ending          bool
	finished        time.Time
	done            chan struct{}
	admission       *contributedAdmission
	// lastInputEmit is when the input phase last sent a change event.
	lastInputEmit time.Time
	// inputFailed marks a call whose input preparation itself failed: its
	// failed snapshot keeps phase "input" so the failure is attributed to the
	// input (a remote read), not to the provider's activation or run.
	inputFailed bool
}

var serviceCalls = struct {
	mu    sync.Mutex
	calls map[string]*serviceCall
}{calls: map[string]*serviceCall{}}

func init() {
	// A caller that stops (or is disabled, reloaded, uninstalled) abandons
	// its calls. The provider's side is covered by the admission's request,
	// which cancelPluginContributionRequests cancels.
	registerRecordLifecycleHook(func(_ *pluginManager, record *pluginRecord) {
		var cancels []context.CancelFunc
		serviceCalls.mu.Lock()
		for _, call := range serviceCalls.calls {
			if !call.host && call.owner == record.id && call.snapshot.State == serviceCallRunning && call.cancel != nil {
				call.cancelRequested = true
				cancels = append(cancels, call.cancel)
			}
		}
		serviceCalls.mu.Unlock()
		for _, cancel := range cancels {
			cancel()
		}
	})
}

// sweepServiceCallsLocked forgets calls that ended more than the retention ago.
func sweepServiceCallsLocked() {
	now := serviceCallNow()
	for id, call := range serviceCalls.calls {
		if !call.finished.IsZero() && now.Sub(call.finished) > serviceSnapshotRetention {
			delete(serviceCalls.calls, id)
		}
	}
}

func (caller serviceCaller) owner() (string, error) {
	switch caller.Kind {
	case "plugin":
		if caller.PluginID == "" || caller.PluginID == serviceCallHostOwner {
			return "", newContributionError(contributionErrInvalidRequest, "missing caller plugin")
		}
		return caller.PluginID, nil
	case "host":
		if !slices.Contains(serviceSurfaces, caller.Surface) {
			return "", newContributionError(contributionErrInvalidRequest, "unknown surface %q", caller.Surface)
		}
		return serviceCallHostOwner, nil
	}
	return "", newContributionError(contributionErrInvalidRequest, "unknown caller kind %q", caller.Kind)
}

// serviceCallParams fills the schema's defaults and validates the values.
func serviceCallParams(schema, values map[string]any) (map[string]any, error) {
	params := make(map[string]any, len(values))
	for name, value := range values {
		params[name] = value
	}
	if schema == nil {
		if len(params) > 0 {
			return nil, newContributionError(contributionErrInvalidRequest, "this service takes no params")
		}
		return params, nil
	}
	properties, _ := schema["properties"].(map[string]any)
	for name, raw := range properties {
		property, _ := raw.(map[string]any)
		if fallback, ok := property["default"]; ok {
			if _, present := params[name]; !present {
				params[name] = fallback
			}
		}
	}
	if err := validateCommandInputValues(schema, params); err != nil {
		return nil, newContributionError(contributionErrInvalidRequest, "params: %v", err)
	}
	return params, nil
}

// serviceCallInput checks the input against the entry's input shape and
// returns the envelope's input value.
func serviceCallInput(shape ServiceShape, raw json.RawMessage, files []exchangeFile) (any, error) {
	hasInput := len(bytes.TrimSpace(raw)) > 0 && string(bytes.TrimSpace(raw)) != "null"
	switch shape.Kind {
	case serviceInputText:
		var text string
		if len(files) > 0 || json.Unmarshal(raw, &text) != nil {
			return nil, newContributionError(contributionErrInvalidRequest, "input must be text")
		}
		if int64(len(text)) > shape.MaxBytes {
			return nil, newContributionError(contributionErrTooLarge, "input is %d bytes, the service accepts %d", len(text), shape.MaxBytes)
		}
		return text, nil
	case serviceInputFile, serviceInputFiles:
		maxFiles := 1
		if shape.Kind == serviceInputFiles {
			maxFiles = shape.MaxFiles
		}
		if hasInput || len(files) == 0 || len(files) > maxFiles {
			return nil, newContributionError(contributionErrInvalidRequest, "input must be 1-%d files", maxFiles)
		}
		for _, file := range files {
			if file.Size > shape.MaxBytes {
				return nil, newContributionError(contributionErrTooLarge, "file %q is %d bytes, the service accepts %d", file.Name, file.Size, shape.MaxBytes)
			}
		}
		return files, nil
	}
	if hasInput || len(files) > 0 {
		return nil, newContributionError(contributionErrInvalidRequest, "this service takes no input")
	}
	return nil, nil
}

func serviceShapeHasFiles(shape ServiceShape) bool {
	return shape.Kind == serviceInputFile || shape.Kind == serviceInputFiles
}

// startServiceCall admits and dispatches one call and returns its running
// snapshot. Errors before dispatch (invalid input, busy, the provider
// unavailable or changed, the envelope too large, a long call on a provider
// without tasks) are returned and leave no call behind.
//
// With PrepareInputsAsync the call is registered, its input files are
// prepared in the background and the returned snapshot is in phase "input";
// the dispatch that follows fails or cancels the call instead of returning an
// error. An error is then returned only before PrepareInputsAsync runs, so a
// caller that hands it a resource releases that resource exactly when
// startServiceCall fails.
func (manager *pluginManager) startServiceCall(ctx context.Context, req serviceCallRequest) (ServiceCallSnapshot, error) {
	call, params, err := manager.registerServiceCall(req)
	if err != nil {
		return ServiceCallSnapshot{}, err
	}
	if req.PrepareInputsAsync != nil {
		prepareCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		serviceCalls.mu.Lock()
		call.cancel = cancel
		call.snapshot.Phase = serviceCallPhaseInput
		call.snapshot.Progress = &ServiceProgress{Completed: new(float64)}
		started := call.snapshot
		serviceCalls.mu.Unlock()
		go call.prepareAndDispatch(prepareCtx, cancel, ctx, req, params)
		return started, nil
	}
	files := slices.Clone(req.Files)
	if req.PrepareInputs != nil && serviceShapeHasFiles(call.entry.Input) {
		prepared, err := req.PrepareInputs(call.id)
		if err != nil {
			return call.abort(err)
		}
		files = append(files, prepared...)
	}
	started, err := manager.dispatchServiceCall(ctx, call, req, params, files)
	if err != nil {
		return call.abort(err)
	}
	return started, nil
}

// registerServiceCall checks the request and records the call: the
// caller's call slot and, for a call that moves files, an exchange call.
func (manager *pluginManager) registerServiceCall(req serviceCallRequest) (*serviceCall, map[string]any, error) {
	if err := manager.validateServiceCaller(req.Caller); err != nil {
		return nil, nil, err
	}
	owner, err := req.Caller.owner()
	if err != nil {
		return nil, nil, err
	}
	entry, providerID := req.Provider.Entry, req.Provider.PluginID
	if providerID == "" || entry.ID == "" {
		return nil, nil, newContributionError(contributionErrInvalidRequest, "missing provider")
	}
	if req.Caller.Kind == "plugin" && providerID == req.Caller.PluginID {
		return nil, nil, newContributionError(contributionErrInvalidRequest, "a plugin cannot call its own service")
	}
	if req.PrepareInputsAsync != nil && !serviceShapeHasFiles(entry.Input) {
		return nil, nil, newContributionError(contributionErrInvalidRequest, "this service takes no input files")
	}
	params, err := serviceCallParams(entry.Params, req.Params)
	if err != nil {
		return nil, nil, err
	}
	if entry.Input.Kind == serviceInputText || entry.Input.Kind == serviceKindNone {
		if _, err := serviceCallInput(entry.Input, req.Input, req.Files); err != nil {
			return nil, nil, err
		}
	}
	callID, err := newExchangeRandom(serviceCallIDPrefix)
	if err != nil {
		return nil, nil, err
	}
	call := &serviceCall{manager: manager, id: callID, owner: owner, host: req.Caller.Kind == "host", entry: entry, providerID: providerID,
		timeout: time.Duration(entry.TimeoutMs) * time.Millisecond, done: make(chan struct{}), snapshot: ServiceCallSnapshot{CallID: callID, State: serviceCallRunning,
			Provider: serviceProviderRef{PluginID: providerID, EntryID: entry.ID}}}

	serviceCalls.mu.Lock()
	sweepServiceCallsLocked()
	running := 0
	for _, other := range serviceCalls.calls {
		if other.owner == owner && other.finished.IsZero() {
			running++
		}
	}
	if running >= serviceCallsPerCaller {
		serviceCalls.mu.Unlock()
		return nil, nil, newContributionError(contributionErrBusy, "the caller already has %d service calls running", serviceCallsPerCaller)
	}
	serviceCalls.calls[callID] = call
	serviceCalls.mu.Unlock()

	// Only calls that move files take one of the exchange's
	// fileCallsConcurrent slots (design §8.4).
	if serviceShapeHasFiles(entry.Input) || serviceShapeHasFiles(entry.Output) {
		if err := serviceFiles.beginCall(callID); err != nil {
			_, err = call.abort(err)
			return nil, nil, err
		}
	}
	return call, params, nil
}

// abort forgets a call that was never dispatched and returns err.
func (call *serviceCall) abort(err error) (ServiceCallSnapshot, error) {
	serviceFiles.endCall(call.id, false)
	serviceCalls.mu.Lock()
	delete(serviceCalls.calls, call.id)
	serviceCalls.mu.Unlock()
	return ServiceCallSnapshot{}, err
}

// prepareAndDispatch is the background half of an asynchronous call: it
// prepares the input files, then dispatches. A cancellation during the input
// phase ends the call cancelled: the provider never ran, so the outcome is
// confirmed and the snapshots are deleted with the call's exchange.
func (call *serviceCall) prepareAndDispatch(prepareCtx context.Context, stopPrepare context.CancelFunc, ctx context.Context, req serviceCallRequest, params map[string]any) {
	defer stopPrepare()
	files := slices.Clone(req.Files)
	prepared, err := req.PrepareInputsAsync(prepareCtx, call.id, call.inputProgress)
	inputFailed := err != nil
	if err == nil {
		files = append(files, prepared...)
		_, err = call.manager.dispatchServiceCall(ctx, call, req, params, files)
	}
	if err == nil {
		return
	}
	serviceCalls.mu.Lock()
	cancelled := call.cancelRequested
	call.inputFailed = inputFailed
	serviceCalls.mu.Unlock()
	var coded *contributionError
	if cancelled || errors.As(err, &coded) && coded.Code == contributionErrCancelled || errors.Is(err, context.Canceled) {
		call.finish(serviceCallCancelled, nil, nil, "", false)
		return
	}
	call.finish(serviceCallFailed, nil, nil, err.Error(), false)
}

// inputProgress records the input phase's progress (bytes prepared of
// total). The snapshot always holds the latest value; change events are sent
// at most every serviceInputProgressInterval, and always for the last chunk.
func (call *serviceCall) inputProgress(done, total int64) {
	completed, size := float64(done), float64(total)
	now := serviceCallNow()
	serviceCalls.mu.Lock()
	if call.ending || call.snapshot.Phase != serviceCallPhaseInput {
		serviceCalls.mu.Unlock()
		return
	}
	call.snapshot.Progress = &ServiceProgress{Completed: &completed, Total: &size}
	emit := done >= total || call.lastInputEmit.IsZero() || now.Sub(call.lastInputEmit) >= serviceInputProgressInterval
	if emit {
		call.lastInputEmit = now
	}
	snapshot := call.snapshot
	serviceCalls.mu.Unlock()
	if emit {
		call.emit(snapshot)
	}
}

// dispatchServiceCall builds the envelope, admits the provider's action and
// starts it. An error means the call was not dispatched; the caller ends it.
// Both the synchronous and the asynchronous path come through here, so the
// checks before dispatch are the same for both.
func (manager *pluginManager) dispatchServiceCall(ctx context.Context, call *serviceCall, req serviceCallRequest, params map[string]any, files []exchangeFile) (ServiceCallSnapshot, error) {
	if err := manager.validateServiceCaller(req.Caller); err != nil {
		return ServiceCallSnapshot{}, err
	}
	entry, providerID, callID := call.entry, call.providerID, call.id
	long := call.timeout > serviceShortTimeout
	input, err := serviceCallInput(entry.Input, req.Input, files)
	if err != nil {
		return ServiceCallSnapshot{}, err
	}
	// surfaceContext is set only on host calls from the newer surfaces, by
	// the host after trimming it for this provider (gateSurfaceContext).
	var surfaceContext json.RawMessage
	if req.Caller.Kind == "host" && serviceSurfaceTakesContext(req.Caller.Surface) && len(req.SurfaceContext) > 0 {
		surfaceContext = req.SurfaceContext
	}
	buildPayload := func(metadata bool) ([]byte, error) {
		return json.Marshal(struct {
			CallID         string                `json:"callId"`
			Service        string                `json:"service"`
			Entry          string                `json:"entry"`
			Input          any                   `json:"input"`
			Params         map[string]any        `json:"params"`
			Caller         serviceCallerEnvelope `json:"caller"`
			SurfaceContext json.RawMessage       `json:"surfaceContext,omitempty"`
		}{callID, entry.Service, entry.ID, input, params, req.Caller.envelope(metadata), surfaceContext})
	}
	// Admit the old shape first; metadata is constructed only for the actual
	// initialized provider generation and must still fit the same final limit.
	payload, err := buildPayload(false)
	if err != nil {
		return ServiceCallSnapshot{}, err
	}

	// Re-checked before activation and dispatch: the provider may have been
	// updated while the caller confirmed or picked it.
	validate := func(record *pluginRecord) error {
		index := slices.IndexFunc(servicesLocked(record), func(current ServiceContribution) bool { return current.ID == entry.ID })
		if index < 0 {
			return newContributionError(contributionErrNotFound, "plugin %q no longer provides service entry %q", providerID, entry.ID)
		}
		current := servicesLocked(record)[index]
		action, _ := contributedActionLocked(record, entry.Action)
		if current.Service != entry.Service || current.Action != entry.Action || current.Risk != entry.Risk || action.Risk != entry.Risk ||
			current.Input.Kind != entry.Input.Kind || current.Output.Kind != entry.Output.Kind || current.TimeoutMs != entry.TimeoutMs {
			return newContributionError(contributionErrForbidden, "service entry %q changed since it was approved; call it again", entry.ID)
		}
		if call.host && !slices.Contains(current.Surfaces, req.Caller.Surface) {
			return newContributionError(contributionErrForbidden, "service entry %q is not offered on %s", entry.ID, req.Caller.Surface)
		}
		if _, err := serviceCallParams(current.Params, params); err != nil {
			return err
		}
		if req.Revalidate != nil {
			return req.Revalidate(record)
		}
		return nil
	}
	callCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	admitCtx, stopTimeout := callCtx, context.CancelFunc(func() {})
	if !long {
		// plugin.invoke: activation and the call share the short timeout.
		admitCtx, stopTimeout = context.WithTimeout(callCtx, call.timeout)
	}
	serviceCalls.mu.Lock()
	if call.cancelRequested {
		// Cancelled between the input phase and dispatch.
		serviceCalls.mu.Unlock()
		stopTimeout()
		cancel()
		return ServiceCallSnapshot{}, newContributionError(contributionErrCancelled, "the service call was cancelled")
	}
	call.cancel = cancel
	serviceCalls.mu.Unlock()
	contributedCall := contributedActionCall{
		PluginID: providerID, Point: servicePointKey, EntryID: entry.ID, ActionID: entry.Action, RequestID: callID,
		Payload: payload, Concurrency: serviceProviderConcurrency, Validate: validate,
		PrepareAdmitted: func(_ context.Context, admitted *contributedAdmission) ([]byte, error) {
			if err := manager.validateServiceCaller(req.Caller); err != nil {
				return nil, err
			}
			return buildPayload(admitted.Runtime.featureAtGeneration(admitted.Generation, "serviceCallerMetadata"))
		},
	}
	admission, err := manager.admitContributedAction(admitCtx, contributedCall)
	if err != nil {
		stopTimeout()
		cancel()
		return ServiceCallSnapshot{}, err
	}
	runtimeHost := admission.Runtime
	tasks := runtimeHost.featureAtGeneration(admission.Generation, "tasks")
	fail := func(err error) (ServiceCallSnapshot, error) {
		admission.Release()
		stopTimeout()
		cancel()
		return ServiceCallSnapshot{}, err
	}
	payload, err = manager.prepareAdmittedPayload(admission, contributedCall)
	if err != nil {
		return fail(err)
	}
	if long && !tasks {
		return fail(newContributionError(contributionErrUnavailable, "plugin %q does not support long-running services (timeoutMs %d needs features.tasks)", providerID, entry.TimeoutMs))
	}
	serviceCalls.mu.Lock()
	call.admission = admission
	phaseEnded := call.snapshot.Phase != ""
	call.snapshot.Phase = ""
	if phaseEnded {
		// The provider's progress replaces the input phase's.
		call.snapshot.Progress = nil
	}
	started := call.snapshot
	serviceCalls.mu.Unlock()
	if phaseEnded {
		// Sent before the provider starts so that its changes come after.
		call.emit(started)
	}
	if err := manager.validateServiceDispatch(req.Caller, providerID, admission); err != nil {
		return fail(err)
	}
	if tasks {
		owner := taskOwner{Generation: admission.Generation, CallID: callID, OnChange: call.taskChanged, ResultLimit: serviceTaskResultLimit}
		if _, err := runtimeHost.startTask(admission.Ctx, owner, entry.Action, string(payload), call.timeout); err != nil {
			return fail(newContributionError(contributionErrBusy, "%v", err))
		}
		return started, nil
	}
	go call.invoke(admission, req.Caller, entry.Action, string(payload), stopTimeout)
	return started, nil
}

// invoke runs the short path. Like invokeContributedAction it stops waiting
// when the call is cancelled or times out, and holds the provider's slot
// until plugin.invoke returns.
func (call *serviceCall) invoke(admission *contributedAdmission, caller serviceCaller, action, payload string, stopTimeout context.CancelFunc) {
	defer stopTimeout()
	type outcome struct {
		result json.RawMessage
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		defer admission.Release()
		if err := call.manager.validateServiceDispatch(caller, call.providerID, admission); err != nil {
			done <- outcome{err: err}
			return
		}
		result, err := admission.Runtime.InvokeGenerationRaw(admission.Ctx, admission.Generation, action, payload)
		done <- outcome{result, err}
	}()
	var result outcome
	select {
	case result = <-done:
	case <-admission.Ctx.Done():
		result.err = admission.Ctx.Err()
	}
	admission.finish()
	if result.err != nil {
		err := admission.classify(result.err)
		var coded *contributionError
		unconfirmed := errors.As(err, &coded) && (coded.Code == contributionErrTimeout || coded.Code == contributionErrCancelled || coded.Code == contributionErrUnavailable)
		message := err.Error()
		if unconfirmed {
			message += "; the outcome is unconfirmed and side effects may have occurred"
		}
		call.finish(serviceCallFailed, nil, nil, message, unconfirmed)
		return
	}
	call.complete(serviceResultJSON(result.result))
}

// serviceResultJSON unwraps a service result on both paths: an object whose
// only key is "result" (a runtime's formatResult: r => ({ result: r }))
// yields that member; anything else, including an object with "result" next
// to other keys, is returned as is. Keys match exactly as JSON strings
// (escapes decoded, no case folding), unlike pluginInvokeResultJSON, which
// plain plugin.invoke keeps using. raw is a JSON-RPC result or task result,
// so it is valid JSON; empty means null.
func serviceResultJSON(raw json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return json.RawMessage("null")
	}
	if trimmed[0] != '{' {
		return trimmed
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	var member json.RawMessage
	if _, err := decoder.Token(); err != nil {
		return trimmed
	}
	for count := 0; decoder.More(); count++ {
		key, err := decoder.Token()
		if err != nil || count > 0 || key != "result" {
			return trimmed
		}
		if err := decoder.Decode(&member); err != nil {
			return trimmed
		}
	}
	if member == nil {
		return trimmed
	}
	return bytes.TrimSpace(member)
}

// taskChanged receives every change of the call's task (taskOwner.OnChange).
func (call *serviceCall) taskChanged(snapshot taskSnapshot) {
	serviceCalls.mu.Lock()
	cancelRequested := call.cancelRequested
	admission := call.admission
	serviceCalls.mu.Unlock()
	switch snapshot.State {
	case "running":
		call.progress(snapshot.Progress)
		return
	case "succeeded":
		call.progress(snapshot.Progress)
		call.complete(serviceResultJSON(snapshot.Result))
	case "cancelled":
		// The task's own deadline is the only cancellation that is neither
		// a cancel request nor the provider's lifecycle (admission.Ctx).
		if cancelRequested || admission == nil || admission.Ctx.Err() != nil {
			call.finish(serviceCallCancelled, nil, nil, "", false)
		} else {
			call.finish(serviceCallFailed, nil, nil, fmt.Sprintf("%s: the provider did not finish within %d ms", contributionErrTimeout, call.entry.TimeoutMs), false)
		}
	default:
		code := contributionErrPluginError
		if snapshot.OutcomeUnconfirmed {
			code = contributionErrUnavailable
			if cancelRequested {
				code = contributionErrCancelled
			}
		}
		call.finish(serviceCallFailed, nil, nil, code+": "+snapshot.Error, snapshot.OutcomeUnconfirmed)
	}
	if admission != nil {
		admission.Release()
	}
}

// complete validates a provider result against the entry's output shape and
// ends the call.
func (call *serviceCall) complete(raw json.RawMessage) {
	output, files, err := call.output(raw)
	if err != nil {
		call.finish(serviceCallFailed, nil, nil, err.Error(), false)
		return
	}
	call.finish(serviceCallSucceeded, output, files, "", false)
}

// output checks a result of shape {text}, {json}, {files: [token...]} or {}
// and hands output files to the caller.
func (call *serviceCall) output(raw json.RawMessage) (json.RawMessage, []exchangeFile, error) {
	invalid := func(format string, args ...any) error {
		return newContributionError(contributionErrInvalidResult, format, args...)
	}
	kind := call.entry.Output.Kind
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		if kind == serviceKindNone {
			return nil, nil, nil
		}
		return nil, nil, invalid("the provider returned no %s output", kind)
	}
	var fields map[string]json.RawMessage
	if trimmed[0] != '{' || json.Unmarshal(trimmed, &fields) != nil {
		return nil, nil, invalid("the result must be an object")
	}
	for _, key := range []string{"text", "json", "files"} {
		if _, present := fields[key]; present && !(key == kind || key == "files" && kind == serviceInputFile) {
			return nil, nil, invalid("a %s service returned %q", kind, key)
		}
	}
	switch kind {
	case serviceInputText:
		var text string
		if json.Unmarshal(fields["text"], &text) != nil {
			return nil, nil, invalid("text output must be a string")
		}
		if len(text) > serviceResultMaxBytes {
			return nil, nil, newContributionError(contributionErrTooLarge, "text output is %d bytes, the limit is %d", len(text), serviceResultMaxBytes)
		}
		encoded, _ := json.Marshal(text)
		return encoded, nil, nil
	case serviceOutputJSON:
		value, ok := fields["json"]
		if !ok {
			return nil, nil, invalid("json output is missing")
		}
		if len(value) > serviceResultMaxBytes {
			return nil, nil, newContributionError(contributionErrTooLarge, "json output is %d bytes, the limit is %d", len(value), serviceResultMaxBytes)
		}
		return slices.Clone(value), nil, nil
	case serviceInputFile, serviceInputFiles:
		var tokens []string
		if json.Unmarshal(fields["files"], &tokens) != nil {
			return nil, nil, invalid("files output must be a list of file tokens")
		}
		if kind == serviceInputFile && len(tokens) != 1 || len(tokens) > serviceFilesMax {
			return nil, nil, invalid("a %s service returned %d files", kind, len(tokens))
		}
		files, err := serviceFiles.handOutputs(call.id, call.providerID, call.owner, tokens)
		if err != nil {
			return nil, nil, invalid("the result names files this call did not create")
		}
		return nil, files, nil
	}
	return nil, nil, nil
}

// progress records a running task's progress.
func (call *serviceCall) progress(raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	progress, kept, ok := parseServiceProgress(raw)
	if !ok {
		return
	}
	serviceCalls.mu.Lock()
	if call.ending || bytes.Equal(call.snapshot.RawProgress, kept) && call.snapshot.Progress != nil && *call.snapshot.Progress == progress {
		serviceCalls.mu.Unlock()
		return
	}
	call.snapshot.Progress, call.snapshot.RawProgress = &progress, kept
	snapshot := call.snapshot
	serviceCalls.mu.Unlock()
	call.emit(snapshot)
}

// finish ends the call once: the exchange call ends first (outputs are kept
// only on success), then the terminal snapshot becomes visible.
func (call *serviceCall) finish(state serviceCallState, output json.RawMessage, files []exchangeFile, message string, unconfirmed bool) {
	serviceCalls.mu.Lock()
	if call.ending {
		serviceCalls.mu.Unlock()
		return
	}
	call.ending = true
	serviceCalls.mu.Unlock()
	serviceFiles.endCall(call.id, state == serviceCallSucceeded)
	serviceCalls.mu.Lock()
	call.snapshot.State, call.snapshot.Output, call.snapshot.Files = state, output, files
	// An ended call is in no phase, except a failure of the input
	// preparation itself, which keeps "input" so it is not mistaken for a
	// failure of the provider (activation, admission, run).
	call.snapshot.Phase = ""
	if state == serviceCallFailed && call.inputFailed {
		call.snapshot.Phase = serviceCallPhaseInput
	}
	call.snapshot.Error, call.snapshot.Unconfirmed = truncateRunes(message, serviceCallErrorMaxRune), unconfirmed
	call.finished = serviceCallNow()
	snapshot := call.snapshot
	close(call.done)
	serviceCalls.mu.Unlock()
	call.emit(snapshot)
	if !call.host && call.manager != nil {
		// The caller was kept running for this call (serviceCallsRunningFor);
		// give it the post-invoke grace to read the result and outputs.
		call.manager.scheduleIdleStop(call.owner, pluginIdleAfterInvokeDelay)
	}
}

// serviceCallsRunningFor reports whether the plugin owner has a service call
// that has not finished: idle stop keeps the caller running meanwhile, since
// stopping it would cancel the call and drop the outputs handed to it.
func serviceCallsRunningFor(owner string) bool {
	serviceCalls.mu.Lock()
	defer serviceCalls.mu.Unlock()
	for _, call := range serviceCalls.calls {
		if !call.host && call.owner == owner && call.finished.IsZero() {
			return true
		}
	}
	return false
}

func (call *serviceCall) emit(snapshot ServiceCallSnapshot) {
	if call.host {
		serviceCallEmit(serviceCallChangedEvent, snapshot)
	}
}

// ownedServiceCallLocked returns owner's call or not_found.
func ownedServiceCallLocked(owner, callID string) (*serviceCall, error) {
	sweepServiceCallsLocked()
	call := serviceCalls.calls[callID]
	if call == nil || owner == "" || call.owner != owner {
		return nil, newContributionError(contributionErrNotFound, "unknown or expired service call")
	}
	return call, nil
}

// serviceCallStatus returns the latest snapshot of owner's call.
func serviceCallStatus(owner, callID string) (ServiceCallSnapshot, error) {
	serviceCalls.mu.Lock()
	defer serviceCalls.mu.Unlock()
	call, err := ownedServiceCallLocked(owner, callID)
	if err != nil {
		return ServiceCallSnapshot{}, err
	}
	return call.snapshot, nil
}

// cancelServiceCall asks to cancel owner's call and returns the latest
// snapshot; the call ends cancelled only if the provider confirms.
func cancelServiceCall(owner, callID string) (ServiceCallSnapshot, error) {
	serviceCalls.mu.Lock()
	call, err := ownedServiceCallLocked(owner, callID)
	if err != nil {
		serviceCalls.mu.Unlock()
		return ServiceCallSnapshot{}, err
	}
	cancel := call.cancel
	if call.ending || call.cancelRequested {
		cancel = nil
	}
	call.cancelRequested = true
	snapshot := call.snapshot
	serviceCalls.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return snapshot, nil
}

// runServiceCallSync starts a call and waits for its end. When ctx ends
// first the call is cancelled and the latest snapshot returned with ctx's
// error.
func (manager *pluginManager) runServiceCallSync(ctx context.Context, req serviceCallRequest) (ServiceCallSnapshot, error) {
	started, err := manager.startServiceCall(ctx, req)
	if err != nil {
		return started, err
	}
	serviceCalls.mu.Lock()
	call := serviceCalls.calls[started.CallID]
	serviceCalls.mu.Unlock()
	select {
	case <-call.done:
	case <-ctx.Done():
		snapshot, _ := cancelServiceCall(call.owner, call.id)
		return snapshot, ctx.Err()
	}
	serviceCalls.mu.Lock()
	defer serviceCalls.mu.Unlock()
	return call.snapshot, nil
}

// normalizeServiceProgress reads {completed?, total?, message?} from a
// provider's progress and returns it with the raw progress, which is dropped
// above progressMaxBytes. Anything but an object yields zero values.
func normalizeServiceProgress(raw json.RawMessage) (ServiceProgress, json.RawMessage) {
	progress, kept, _ := parseServiceProgress(raw)
	return progress, kept
}

func parseServiceProgress(raw json.RawMessage) (ServiceProgress, json.RawMessage, bool) {
	trimmed := bytes.TrimSpace(raw)
	var fields map[string]json.RawMessage
	if len(trimmed) == 0 || trimmed[0] != '{' || json.Unmarshal(trimmed, &fields) != nil {
		return ServiceProgress{}, nil, false
	}
	number := func(value json.RawMessage) *float64 {
		var parsed float64
		if json.Unmarshal(value, &parsed) != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return nil
		}
		return &parsed
	}
	progress := ServiceProgress{Completed: number(fields["completed"]), Total: number(fields["total"])}
	var message string
	if json.Unmarshal(fields["message"], &message) == nil {
		message = strings.Map(func(r rune) rune {
			if unsafeTextRune(r) {
				return ' '
			}
			return r
		}, message)
		progress.Message = truncateRunes(message, serviceProgressMessageMaxRunes)
	}
	if len(raw) > serviceProgressMaxBytes {
		return progress, nil, true
	}
	return progress, slices.Clone(raw), true
}
