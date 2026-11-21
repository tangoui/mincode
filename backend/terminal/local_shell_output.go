package terminal

import (
	"strings"
	"sync"
	"unicode/utf8"
)

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
