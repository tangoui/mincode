package debug

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// logpointUnsupportedMessage is a stable diagnostic code localized by the UI.
const logpointUnsupportedMessage = "unsupported: logpoints"

// sourceBreakpointBatch is shared by initial configuration and live updates.
// Unsupported logpoints MUST be omitted, not stripped down to ordinary stopping
// breakpoints. The adapter still receives an empty list when necessary to clear
// an old stopping breakpoint converted to a logpoint by the user.
type sourceBreakpointBatch struct {
	requested   []SourceBreakpoint
	sent        []SourceBreakpoint
	unsupported []bool
}

func (s *Session) prepareSourceBreakpoints(bps []SourceBreakpoint) sourceBreakpointBatch {
	batch := sourceBreakpointBatch{
		requested:   make([]SourceBreakpoint, len(bps)),
		sent:        make([]SourceBreakpoint, 0, len(bps)),
		unsupported: make([]bool, len(bps)),
	}
	supported := s.logpointsSupported.Load()
	for i, bp := range bps {
		if strings.TrimSpace(bp.LogMessage) == "" {
			bp.LogMessage = ""
		}
		batch.requested[i] = bp
		if bp.LogMessage != "" && !supported {
			batch.unsupported[i] = true
		} else {
			batch.sent = append(batch.sent, bp)
		}
	}
	return batch
}

func (batch sourceBreakpointBatch) arguments(path string) map[string]any {
	return map[string]any{
		"source":      map[string]string{"path": path, "name": filepath.Base(path)},
		"breakpoints": batch.sent,
	}
}

// addressArguments are the setBreakpoints arguments for a debugger address:
// the address goes verbatim into source.path and, unlike a file path, gets no
// derived name (the host does not parse addresses).
func (batch sourceBreakpointBatch) addressArguments(address string) map[string]any {
	return map[string]any{
		"source":      map[string]string{"path": address},
		"breakpoints": batch.sent,
	}
}

// finish restores the original request ordering so ordinary adapter results
// retain their IDs, relocated lines, columns and other metadata. Existing raw
// responses are returned untouched unless unsupported logpoints were filtered.
func (batch sourceBreakpointBatch) finish(body json.RawMessage, requestErr error) (json.RawMessage, error) {
	if requestErr != nil || len(batch.requested) == len(batch.sent) {
		return body, requestErr
	}
	fields := make(map[string]json.RawMessage)
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
		if err := json.Unmarshal(trimmed, &fields); err != nil {
			return nil, fmt.Errorf("invalid_response: breakpoint body: %w", err)
		}
	}
	if fields == nil {
		fields = make(map[string]json.RawMessage)
	}
	var returned []json.RawMessage
	if raw := fields["breakpoints"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &returned); err != nil {
			return nil, fmt.Errorf("invalid_response: breakpoints: %w", err)
		}
	}
	results := make([]json.RawMessage, 0, len(batch.requested))
	next := 0
	for i, bp := range batch.requested {
		if !batch.unsupported[i] && next < len(returned) && len(bytes.TrimSpace(returned[next])) > 0 && !bytes.Equal(bytes.TrimSpace(returned[next]), []byte("null")) {
			results = append(results, returned[next])
			next++
			continue
		}
		message := logpointUnsupportedMessage
		if !batch.unsupported[i] {
			message = "invalid_response: breakpoint result missing"
			next++
		}
		result, _ := json.Marshal(map[string]any{"line": bp.Line, "verified": false, "message": message})
		results = append(results, result)
	}
	fields["breakpoints"], _ = json.Marshal(results)
	return json.Marshal(fields)
}
