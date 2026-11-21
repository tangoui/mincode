package terminal

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"mincode/app/backend/core"
)

// debuggeeOutputLogMaxBytes caps the output a debuggee terminal keeps for a
// late attach_local_shell.
const debuggeeOutputLogMaxBytes = 256 << 10

func init() {
	core.Register("attach_local_shell", cmdAttachLocalShell)
}

// DebuggeeShellSpec is a program a debug adapter asked to run through the DAP
// `runInTerminal` reverse request. It is built only by backend/debug, never
// from WebView input: the trust source is the debug adapter the user enabled
// and confirmed, together with the run configuration the user wrote. The
// debug package validates Cwd against the session's project root, resolves a
// bare program name to an absolute path and filters Env through the
// `debuggers` contribution-point denylist before calling StartDebuggeeShell.
type DebuggeeShellSpec struct {
	SessionID string
	// Executable is absolute. Besides what StartProfileShell accepts, Windows
	// .cmd/.bat launchers are run through cmd.exe (adapters often hand those
	// out); their arguments must then not contain `"`, `%` or line breaks.
	Executable string
	Args       []string
	// Env overrides inherited variables; UnsetEnv names are removed from the
	// inherited environment (DAP `env` entries whose value is null).
	Env      map[string]string
	UnsetEnv []string
	Cwd      string
}

// StartDebuggeeShell starts spec in a new PTY session and returns the
// program's pid. Unlike StartProfileShell the session keeps a bounded copy of
// its output, so the Console view can attach late (attach_local_shell)
// without losing what the program printed first; after the program exits the
// output stays attachable until CloseLocalShell. Its end is reported as
// "command_exited", not as a failed terminal.
func StartDebuggeeShell(spec DebuggeeShellSpec) (int, error) {
	ps, err := checkDebuggeeSpec(spec)
	if err != nil {
		return 0, fmt.Errorf("start debuggee: %w", err)
	}
	pid := 0
	_, err = runLocalShellSession(ps.SessionID, false, nil, nil, "start_debuggee_shell",
		func(cols, rows uint16) (localPTYHandle, string, error) {
			handle, err := startProfilePTY(ps, cols, rows)
			if err == nil {
				pid = handle.Pid()
			}
			return handle, "", err
		}, nil, newOutputLog(debuggeeOutputLogMaxBytes))
	if err != nil {
		return 0, fmt.Errorf("start debuggee: %w", err)
	}
	return pid, nil
}

// EndDebuggeeShell ends a debuggee session's program (killing its process)
// but, unlike CloseLocalShell, keeps the output it printed attachable, so a
// Console that attaches after a short-lived program already finished still
// shows that output. Only the newest endedOutputsMax ended outputs are kept;
// CloseLocalShell drops one. Unknown IDs are ignored.
func EndDebuggeeShell(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	attention.forget(sessionID)
	if proc, ok := sessions.removeKeepingOutput(sessionID); ok {
		shutdownShellProcess(proc)
	}
}

// CloseLocalShell ends the local session with this ID (killing its process)
// and drops the output an exited debuggee session kept. Unknown IDs are
// ignored.
func CloseLocalShell(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	attention.forget(sessionID)
	if proc, ok := sessions.removeWithOutput(sessionID); ok {
		shutdownShellProcess(proc)
	}
}

// AttachLocalShellResult is the attach_local_shell result. Output is what
// the session kept (only debuggee sessions keep any) and Offset the total
// number of output bytes produced so far; `shell-output:<id>` events of such
// sessions carry the offset after their data, so a view that subscribed
// before attaching drops events with offset <= Offset. Ended means the
// program has exited and only its kept output remains.
type AttachLocalShellResult struct {
	OK     bool   `json:"ok"`
	Output string `json:"output"`
	Offset int64  `json:"offset"`
	Ended  bool   `json:"ended"`
}

// cmdAttachLocalShell attaches to an existing local PTY session and never
// starts one. Errors: "invalid_request: …", "not_found: …".
func cmdAttachLocalShell(_, argsJSON string) (string, error) {
	var payload struct {
		SessionID string `json:"sessionId"`
	}
	if err := core.DecodeArgsPayload(argsJSON, &payload); err != nil {
		return "", fmt.Errorf("invalid_request: %w", err)
	}
	id := strings.TrimSpace(payload.SessionID)
	if id == "" {
		return "", errors.New("invalid_request: sessionId is empty")
	}
	result, ok := attachLocalShell(id)
	if !ok {
		return "", fmt.Errorf("not_found: local shell session %q", id)
	}
	return core.EncodeResult(result)
}

func attachLocalShell(id string) (AttachLocalShellResult, bool) {
	proc, live, ended := sessions.lookupWithOutput(id)
	var log *outputLog
	switch {
	case live:
		proc.mu.Lock()
		local := proc.localPTY != nil
		log = proc.output
		proc.mu.Unlock()
		if !local {
			return AttachLocalShellResult{}, false
		}
	case ended != nil:
		log = ended
	default:
		return AttachLocalShellResult{}, false
	}
	result := AttachLocalShellResult{OK: true, Ended: !live}
	if log != nil {
		result.Output, result.Offset = log.snapshot()
	}
	return result, true
}

// outputLog keeps the newest maxBytes of a session's output and the total
// number of bytes produced.
type outputLog struct {
	mu       sync.Mutex
	maxBytes int
	buf      []byte
	total    int64
}

func newOutputLog(maxBytes int) *outputLog {
	return &outputLog{maxBytes: maxBytes}
}

// append records data and returns the total byte count after it.
func (l *outputLog) append(data string) int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf = append(l.buf, data...)
	l.total += int64(len(data))
	if over := len(l.buf) - l.maxBytes; over > 0 {
		// Start the kept tail on a rune boundary.
		for over < len(l.buf) && !utf8.RuneStart(l.buf[over]) {
			over++
		}
		l.buf = append(l.buf[:0:0], l.buf[over:]...)
	}
	return l.total
}

// snapshot returns the kept output and the total byte count.
func (l *outputLog) snapshot() (string, int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return string(l.buf), l.total
}

// emitLoggedOutput records data in log (when set) and dispatches it with the
// resulting offset, so a concurrent attach sees each chunk either in its
// snapshot or as a later event, never both unmarked.
func emitLoggedOutput(sessionID, data string, log *outputLog) {
	if log == nil {
		dispatchLocalShellOutput(sessionID, data, 0)
		return
	}
	dispatchLocalShellOutput(sessionID, data, log.append(data))
}
