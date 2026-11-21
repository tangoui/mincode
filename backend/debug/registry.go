package debug

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"mincode/app/backend/core"
)

// FrontendEvent is the core.Emit event name that carries every Event of
// every registered session to the frontend.
const FrontendEvent = "debug:event"

// SessionEndedEvent is the synthetic event emitted (body {"reason": …}) after
// a registered session has closed, its adapter has been killed and it has
// been removed from the registry.
const SessionEndedEvent = "mincode.sessionEnded"

// eventSink pushes one event to the frontend. Tests replace it (setEventSink)
// while sessions of an earlier test may still be closing, so it is read and
// written atomically.
var eventSink atomic.Pointer[func(Event)]

func init() {
	setEventSink(func(ev Event) { core.Emit(FrontendEvent, ev) })
}

// setEventSink replaces the event sink and returns the previous one.
func setEventSink(sink func(Event)) func(Event) {
	prev := eventSink.Swap(&sink)
	if prev == nil {
		return nil
	}
	return *prev
}

// emitEvent pushes one event through the current sink.
func emitEvent(ev Event) { (*eventSink.Load())(ev) }

// validCheckInterval is how often AdapterSpec.Valid is polled. Tests shorten it.
var validCheckInterval = 2 * time.Second

// SessionInfo describes a registered session.
type SessionInfo struct {
	ID            string `json:"id"`
	ProjectRoot   string `json:"projectRoot"`
	OwnerPluginID string `json:"ownerPluginId"`
	DebuggerType  string `json:"debuggerType"`
	// StartedAt is the adapter start time in Unix milliseconds.
	StartedAt int64 `json:"startedAt"`
}

// entry is one registered session and the adapter process behind it.
type entry struct {
	info    SessionInfo
	session *Session
	// proc is nil for a session made by ConnectAdapter: every use must be
	// nil-safe (the adapterProcess methods are).
	proc  *adapterProcess
	pump  *eventPump
	done  chan struct{} // closed when the session starts closing
	ended chan struct{} // closed when cleanup has finished

	// Debuggee PTYs started for runInTerminal (see run_in_terminal.go).
	termMu     sync.Mutex
	termSeq    int
	terminals  []string
	termClosed bool // set by closeTerminals; no PTY may start afterwards
	// Debuggees running in system terminal windows (runInTerminal kind "external").
	externals []externalProgram
}

var registry = struct {
	mu       sync.Mutex
	sessions map[string]*entry
	// starting counts StartAdapter calls per owner that passed the limit
	// check but are not registered yet, so concurrent starts cannot exceed
	// MaxSessionsPerOwner.
	starting map[string]int
	// shuttingDown is set by Shutdown; no session may start afterwards.
	shuttingDown bool
}{sessions: map[string]*entry{}, starting: map[string]int{}}

// errShuttingDown refuses starts once Shutdown has begun.
var errShuttingDown = errors.New("MinCode is shutting down")

// isShuttingDown reports whether Shutdown has begun.
func isShuttingDown() bool {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	return registry.shuttingDown
}

// Lookup returns the registered session with the given id.
func Lookup(id string) (*Session, bool) {
	e, ok := lookupEntry(id)
	if !ok {
		return nil, false
	}
	return e.session, true
}

func lookupEntry(id string) (*entry, bool) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	e, ok := registry.sessions[id]
	return e, ok
}

// List returns the registered sessions, oldest first.
func List() []SessionInfo {
	registry.mu.Lock()
	out := make([]SessionInfo, 0, len(registry.sessions))
	for _, e := range registry.sessions {
		out = append(out, e.info)
	}
	registry.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartedAt != out[j].StartedAt {
			return out[i].StartedAt < out[j].StartedAt
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// StopForOwner gracefully stops every session started by pluginID (see
// stopWhere) and returns once they have all ended.
func StopForOwner(pluginID, reason string) {
	stopWhere(context.Background(), reason, func(e *entry) bool { return e.info.OwnerPluginID == pluginID })
}

// StopAll gracefully stops every registered session (see stopWhere).
func StopAll(reason string) {
	stopWhere(context.Background(), reason, func(*entry) bool { return true })
}

// shutdownReason is the close reason of sessions ended by Shutdown.
const shutdownReason = "MinCode is quitting"

// Shutdown stops every session when the app exits: new starts are refused,
// running sessions are stopped gracefully in parallel (see stopWhere; ctx
// cancellation skips the remaining grace), and whatever is still registered
// afterwards is closed and its adapter killed.
func Shutdown(ctx context.Context) {
	registry.mu.Lock()
	registry.shuttingDown = true
	registry.mu.Unlock()
	stopWhere(ctx, shutdownReason, func(*entry) bool { return true })
	closeWhere(shutdownReason, func(*entry) bool { return true }) // backstop
}

// StopSession gracefully stops s if it is registered (see entry.stop), else
// just closes it. The plugin host uses it when the launch sequence fails.
func StopSession(ctx context.Context, s *Session, reason string) {
	if e, ok := lookupEntry(s.ID()); ok && e.session == s {
		e.stop(ctx, e.defaultTerminateDebuggee(), reason, forcedReason(reason))
		return
	}
	s.Close(reason)
}

// forcedReason is the close reason when an adapter ignored the disconnect
// sent while stopping a session with reason.
func forcedReason(reason string) string {
	return reason + "; debug adapter did not end after disconnect, killed"
}

// stopWhere gracefully stops the matching sessions in parallel and returns
// once each has been cleaned up (adapter killed, entry removed,
// SessionEndedEvent emitted). Each gets `disconnect` with terminateDebuggee
// for a launch session (never for attach), then is closed and its adapter
// killed when the adapter answers or after stopGrace. Killing only the
// adapter's process group is not enough: debugpy starts the debuggee in its
// own process group, so only the adapter's disconnect handling ends it.
func stopWhere(ctx context.Context, reason string, match func(*entry) bool) {
	var wg sync.WaitGroup
	for _, e := range matching(match) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.stop(ctx, e.defaultTerminateDebuggee(), reason, forcedReason(reason))
		}()
	}
	wg.Wait()
}

// closeWhere closes the matching sessions without a disconnect and waits
// until each has been cleaned up.
func closeWhere(reason string, match func(*entry) bool) {
	victims := matching(match)
	for _, e := range victims {
		e.session.Close(reason)
	}
	for _, e := range victims {
		<-e.ended // Close returns early if another goroutine is closing it
	}
}

func matching(match func(*entry) bool) []*entry {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	var out []*entry
	for _, e := range registry.sessions {
		if match(e) {
			out = append(out, e)
		}
	}
	return out
}

// defaultTerminateDebuggee is true only for a launch session: an attach
// target is the user's own process and must survive the stop.
func (e *entry) defaultTerminateDebuggee() bool {
	return e.session.requestKind() == "launch"
}

// stop sends `disconnect` and returns once the session has ended: when the
// adapter answers, ends the session itself, or after stopGrace (or ctx
// cancellation) the session is closed (with reason, or forced after the
// grace) and the adapter's process group killed. It blocks for at most
// stopGrace plus the adapter kill time.
//
// terminateDebuggee is only a request to the adapter. A debuggee in the
// adapter's process group dies with it anyway, but one the adapter put in
// its own group (debugpy does) survives unless the adapter ends it while
// handling disconnect within the grace. Attach targets are never killed by
// MinCode.
func (e *entry) stop(ctx context.Context, terminateDebuggee bool, reason, forced string) {
	reqCtx, cancel := context.WithTimeout(context.Background(), stopGrace)
	defer cancel()
	replied := make(chan struct{})
	// The write may block on an adapter that stopped reading; Close below
	// unblocks it, so this goroutine never outlives the session.
	go func() {
		defer close(replied)
		_, _ = e.session.Request(reqCtx, "disconnect", map[string]bool{"terminateDebuggee": terminateDebuggee})
	}()
	timer := time.NewTimer(stopGrace)
	defer timer.Stop()
	select {
	case <-e.ended:
		return
	case <-replied:
		// Adapter answered (or the request failed): per DAP the client
		// closes the connection now.
	case <-ctx.Done():
	case <-timer.C:
		reason = forced
	}
	e.session.Close(reason)
	<-e.ended // Close returns early if another goroutine is closing it
}

// reserveSlot counts a pending start for owner, failing when owner already
// has max sessions (0 = unlimited). Release it with releaseSlot.
func reserveSlot(owner string, max int) error {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.shuttingDown {
		return errShuttingDown
	}
	if max > 0 {
		n := registry.starting[owner]
		for _, e := range registry.sessions {
			if e.info.OwnerPluginID == owner {
				n++
			}
		}
		if n >= max {
			return fmt.Errorf("too many debug sessions for %q (limit %d)", owner, max)
		}
	}
	registry.starting[owner]++
	return nil
}

func releaseSlot(owner string) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.starting[owner]--; registry.starting[owner] <= 0 {
		delete(registry.starting, owner)
	}
}

// register wraps conn in a Session bound to proc, records it and starts its
// watchers. The caller must hold a slot (reserveSlot) and releases it after.
func register(id string, spec AdapterSpec, proc *adapterProcess, conn *adapterConn) *Session {
	e := &entry{
		info: SessionInfo{
			ID:            id,
			ProjectRoot:   spec.ProjectRoot,
			OwnerPluginID: spec.OwnerPluginID,
			DebuggerType:  spec.DebuggerType,
			StartedAt:     time.Now().UnixMilli(),
		},
		proc:  proc,
		pump:  newEventPump(outputCoalesceWindow, outputBatchMaxBytes, func(ev Event) { emitEvent(ev) }),
		done:  make(chan struct{}),
		ended: make(chan struct{}),
	}
	// Hold the registry lock while the session starts so its OnClose (which
	// may run at once, e.g. on immediate EOF) finds the entry registered.
	registry.mu.Lock()
	e.session = newSession(conn, SessionOptions{
		ID:              id,
		ProjectRoot:     spec.ProjectRoot,
		OwnerPluginID:   spec.OwnerPluginID,
		DebuggerType:    spec.DebuggerType,
		MaxMessageBytes: spec.MaxMessageBytes,
		OnEvent:         e.pump.push,
		OnClose:         func(reason string) { e.closed(reason) },
		OnRunInTerminal: e.runInTerminal,
	})
	registry.sessions[id] = e
	registry.mu.Unlock()

	go e.watchExit()
	if spec.Valid != nil {
		go e.watchValid(spec.Valid)
	}
	return e.session
}

// closed runs once, as the session's OnClose: kill the adapter, close the
// debuggee PTYs, unregister, flush output and announce the end. Every end
// path (adapter exit, debug_stop, plugin lifecycle, Shutdown) comes here.
func (e *entry) closed(reason string) {
	defer close(e.ended)
	close(e.done)
	e.proc.kill()
	e.closeTerminals()
	registry.mu.Lock()
	delete(registry.sessions, e.info.ID)
	registry.mu.Unlock()
	e.pump.close()
	body, err := json.Marshal(map[string]string{"reason": reason})
	if err != nil {
		body = nil // unreachable: strings always encode
	}
	emitEvent(Event{SessionID: e.info.ID, Event: SessionEndedEvent, Body: body})
}

// watchExit closes the session if the adapter exits but its connection
// stays open (e.g. a grandchild still holds the pipe), after a grace period
// that lets the remaining output be read.
func (e *entry) watchExit() {
	if e.proc == nil {
		return
	}
	select {
	case <-e.proc.exited:
	case <-e.done:
		return
	}
	select {
	case <-time.After(adapterExitGrace):
		e.session.Close(e.proc.exitReason(""))
	case <-e.done:
	}
}

// watchValid stops the session gracefully with "plugin changed" once valid
// reports false.
func (e *entry) watchValid(valid func() bool) {
	ticker := time.NewTicker(validCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if !valid() {
				e.stop(context.Background(), e.defaultTerminateDebuggee(), "plugin changed", forcedReason("plugin changed"))
				return
			}
		case <-e.done:
			return
		}
	}
}

// afterAdapterStart, when set by tests, observes every started adapter process.
var afterAdapterStart func(cmd *exec.Cmd)
