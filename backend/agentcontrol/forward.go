package agentcontrol

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"mincode/app/backend/core"
)

type toolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type toolCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type content struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	MIMEType string `json:"mimeType,omitempty"`
	Data     string `json:"data,omitempty"`
}

func (part content) MarshalJSON() ([]byte, error) {
	if part.Type == "text" {
		return json.Marshal(map[string]string{"type": "text", "text": part.Text})
	}
	type wireContent content
	return json.Marshal(wireContent(part))
}

type toolResult struct {
	Content []content `json:"content"`
	IsError bool      `json:"isError"`
}

type pendingRequest struct {
	event  string
	reply  chan json.RawMessage
	source origin
	cancel context.CancelFunc
}

type logEntry struct {
	Timestamp  time.Time `json:"timestamp"`
	Source     string    `json:"source"`
	Tool       string    `json:"tool"`
	Success    bool      `json:"success"`
	DurationMS int64     `json:"durationMs"`
}

func excluded(name string) bool {
	switch name {
	case "ask_user", "agent.run_parallel", "agent.integrate_result":
		return true
	default:
		return false
	}
}

func init() {
	for _, command := range []string{
		"agent_control_ready", "agent_control_result", "agent_control_pane_result",
		"agent_control_set_trusted", "agent_control_status", "agent_control_log",
	} {
		core.Register(command, control.command)
	}
}

func (s *serverState) command(command, raw string) (string, error) {
	switch command {
	case "agent_control_ready":
		var args struct {
			Tools []toolDefinition `json:"tools"`
		}
		if err := core.DecodeArgsPayload(raw, &args); err != nil {
			return "", err
		}
		tools := make([]toolDefinition, 0, len(args.Tools))
		names := make(map[string]bool, len(args.Tools))
		for _, tool := range args.Tools {
			if tool.Name != "" && !excluded(tool.Name) && !names[tool.Name] {
				tools = append(tools, tool)
				names[tool.Name] = true
			}
		}
		s.mu.Lock()
		s.tools, s.names = tools, names
		s.mu.Unlock()
	case "agent_control_set_trusted":
		var args struct {
			Enabled *bool `json:"enabled"`
		}
		if err := core.DecodeArgsPayload(raw, &args); err != nil {
			return "", err
		}
		if args.Enabled == nil {
			return "", errors.New("enabled is required")
		}
		if err := SetExternalTrusted(*args.Enabled); err != nil {
			return "", err
		}
		return s.command("agent_control_status", "{}")
	case "agent_control_status":
		s.mu.Lock()
		defer s.mu.Unlock()
		return core.EncodeResult(map[string]bool{"trusted": s.trusted, "ready": s.tools != nil})
	case "agent_control_log":
		s.mu.Lock()
		defer s.mu.Unlock()
		entries := make([]logEntry, 0, len(s.log))
		for i := len(s.log) - 1; i >= 0; i-- {
			entries = append(entries, s.log[i])
		}
		return core.EncodeResult(entries)
	case "agent_control_result", "agent_control_pane_result":
		var reply struct {
			RequestID string          `json:"requestId"`
			Content   []content       `json:"content"`
			IsError   bool            `json:"isError"`
			Pane      json.RawMessage `json:"pane"`
			Error     string          `json:"error"`
		}
		if err := core.DecodeArgsPayload(raw, &reply); err != nil {
			return "", err
		}
		event := "agent-control:call"
		var result any = toolResult{Content: limitContent(reply.Content), IsError: reply.IsError}
		if command == "agent_control_pane_result" {
			event = "agent-control:pane-op"
			result = paneResult{Pane: reply.Pane, Error: reply.Error}
		}
		s.mu.Lock()
		pending, found := s.pending[reply.RequestID]
		if found && pending.event == event {
			delete(s.pending, reply.RequestID)
			data, _ := json.Marshal(result)
			pending.reply <- data
		}
		s.mu.Unlock()
	}
	return "{}", nil
}

func (s *serverState) forward(ctx context.Context, source origin, event string, fields map[string]any) (json.RawMessage, error) {
	requestCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	requestID := randomID(16)
	result := make(chan json.RawMessage, 1)
	s.mu.Lock()
	if s.tools == nil || s.server == nil {
		s.mu.Unlock()
		return nil, errNotReady
	}
	if source.token != "" {
		if _, status := s.authenticateLocked(source.token); status != 0 {
			s.mu.Unlock()
			return nil, authError(status)
		}
	}
	if err := requestCtx.Err(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	s.pending[requestID] = pendingRequest{event: event, reply: result, source: source, cancel: cancel}
	s.mu.Unlock()
	fields["requestId"] = requestID
	core.Emit(event, fields)
	select {
	case data := <-result:
		if requestCtx.Err() == nil {
			return data, nil
		}
	case <-requestCtx.Done():
	}
	s.mu.Lock()
	delete(s.pending, requestID)
	_, status := s.authenticateLocked(source.token)
	s.mu.Unlock()
	core.Emit("agent-control:cancel", map[string]string{"requestId": requestID})
	if source.token != "" && status != 0 && ctx.Err() == nil {
		return nil, authError(status)
	}
	return nil, requestCtx.Err()
}

func (s *serverState) record(start time.Time, source origin, name string, success bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.log) == 200 {
		copy(s.log, s.log[1:])
		s.log = s.log[:199]
	}
	s.log = append(s.log, logEntry{Timestamp: start, Source: source.Kind, Tool: name, Success: success, DurationMS: time.Since(start).Milliseconds()})
}

func (s *serverState) handleTool(w http.ResponseWriter, r *http.Request, source origin) {
	var call toolCall
	if !decodeRequest(w, r, &call) {
		return
	}
	if call.Name == "" || len(call.Name) > 256 {
		writeError(w, http.StatusBadRequest, "tool name is required")
		return
	}
	start := time.Now()
	success := false
	defer func() { s.record(start, source, call.Name, success) }()
	if excluded(call.Name) {
		writeError(w, http.StatusConflict, "tool is unavailable to external agents")
		return
	}
	s.mu.Lock()
	ready, exists := s.tools != nil, s.names[call.Name]
	s.mu.Unlock()
	if !ready {
		writeForwardError(w, errNotReady)
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, "tool not found")
		return
	}
	if len(call.Arguments) == 0 || string(call.Arguments) == "null" {
		call.Arguments = json.RawMessage("{}")
	}
	if call.Arguments[0] != '{' {
		writeError(w, http.StatusBadRequest, "tool arguments must be an object")
		return
	}
	if source.PaneID != "" {
		raw, err := core.EncodeResult(map[string]string{"pane": source.PaneID})
		if err == nil {
			info, err := core.DispatchContext(r.Context(), "terminal_control_info", raw)
			if err == nil {
				var pane struct {
					CWD string `json:"cwd"`
				}
				if json.Unmarshal([]byte(info), &pane) == nil {
					source.CWD = pane.CWD
				}
			}
		}
	}
	result, err := s.forward(r.Context(), source, "agent-control:call", map[string]any{
		"name": call.Name, "arguments": call.Arguments, "origin": source,
	})
	if err != nil {
		writeForwardError(w, err)
		return
	}
	var status struct {
		IsError bool `json:"isError"`
	}
	_ = json.Unmarshal(result, &status)
	success = !status.IsError
	_, _ = w.Write(result)
}

func limitContent(input []content) []content {
	const maxText = 200 * 1024
	const maxImage = 5 * 1024 * 1024
	const truncated = "\n[output truncated]"
	output := make([]content, 0, len(input))
	remaining := maxText
	totalText := 0
	for _, part := range input {
		if part.Type == "text" {
			totalText += len(part.Text)
			if totalText > maxText {
				remaining -= len(truncated)
				break
			}
		}
	}
	textTruncated := false
	for _, part := range input {
		switch part.Type {
		case "text":
			if textTruncated {
				continue
			}
			if len(part.Text) > remaining {
				for remaining > 0 && !utf8.RuneStart(part.Text[remaining]) {
					remaining--
				}
				output = append(output, content{Type: "text", Text: part.Text[:remaining] + truncated})
				textTruncated = true
			} else {
				output = append(output, content{Type: "text", Text: part.Text})
				remaining -= len(part.Text)
			}
		case "image":
			if strings.HasPrefix(part.MIMEType, "image/") && len(part.Data) <= base64.StdEncoding.EncodedLen(maxImage) {
				data, err := base64.StdEncoding.DecodeString(part.Data)
				if err == nil && len(data) <= maxImage {
					output = append(output, content{Type: "image", MIMEType: part.MIMEType, Data: part.Data})
				}
			}
		}
	}
	return output
}
