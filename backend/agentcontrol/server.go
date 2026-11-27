package agentcontrol

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"mincode/app/backend/core"
	"mincode/app/backend/externalmcp"
)

const callTimeout = 10 * time.Minute

var (
	control         serverState
	errNotReady     = errors.New("MinCode interface is not ready")
	ErrPaneNotFound = errors.New("pane not found")
)

type serverState struct {
	lifecycle     sync.Mutex
	mu            sync.Mutex
	server        *http.Server
	url           string
	tokenPanes    map[string]string
	trusted       bool
	externalToken string
	tools         []toolDefinition
	names         map[string]bool
	pending       map[string]pendingRequest
	log           []logEntry
}

func Start() error {
	control.lifecycle.Lock()
	defer control.lifecycle.Unlock()
	control.mu.Lock()
	defer control.mu.Unlock()
	if control.server != nil {
		return nil
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	control.url = "http://" + listener.Addr().String()
	control.tokenPanes = make(map[string]string)
	control.pending = make(map[string]pendingRequest)
	server := &http.Server{
		Handler:           http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { control.handle(w, r, callTimeout) }),
		ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: callTimeout + 20*time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 4096,
	}
	control.server = server
	go server.Serve(listener)
	externalmcp.RemoveStaleRecord()
	return externalmcp.SetControlPresence(control.url)
}

func Shutdown() {
	control.lifecycle.Lock()
	defer control.lifecycle.Unlock()
	control.mu.Lock()
	server := control.server
	if server == nil {
		control.mu.Unlock()
		return
	}
	control.server = nil
	control.url = ""
	control.tokenPanes = nil
	control.trusted, control.externalToken = false, ""
	control.tools, control.names = nil, nil
	control.pending, control.log = nil, nil
	control.mu.Unlock()

	_ = server.Close()
	_ = externalmcp.SetControlRecord("", "")
	_ = externalmcp.SetControlPresence("")
}

func (s *serverState) handle(w http.ResponseWriter, r *http.Request, timeout time.Duration) {
	w.Header().Set("Content-Type", "application/json")
	source, status := s.authenticate(r.Header.Get("Authorization"))
	if status != 0 {
		writeError(w, status, authError(status).Error())
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST is required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	r = r.WithContext(ctx)
	switch r.URL.Path {
	case "/v1/tools/list":
		s.mu.Lock()
		tools := s.tools
		s.mu.Unlock()
		if tools == nil {
			writeForwardError(w, errNotReady)
			return
		}
		writeJSON(w, map[string]any{"tools": tools})
	case "/v1/tools/call":
		s.handleTool(w, r, source)
	case "/v1/panes/split", "/v1/panes/tab", "/v1/panes/close":
		s.handlePaneOp(w, r, source, r.URL.Path[len("/v1/panes/"):])
	case "/v1/panes/send", "/v1/panes/read", "/v1/panes/wait", "/v1/panes/show", "/v1/panes/list":
		s.handleTerminal(w, r, source, r.URL.Path[len("/v1/panes/"):])
	default:
		writeError(w, http.StatusNotFound, "route not found")
	}
}

type paneArgs struct {
	Pane        string `json:"pane,omitempty"`
	Direction   string `json:"direction,omitempty"`
	CWD         string `json:"cwd,omitempty"`
	Title       string `json:"title,omitempty"`
	Command     string `json:"command,omitempty"`
	Text        string `json:"text,omitempty"`
	Submit      bool   `json:"submit,omitempty"`
	Lines       int    `json:"lines,omitempty"`
	TimeoutSecs int    `json:"timeoutSecs,omitempty"`
}

type paneResult struct {
	Pane  json.RawMessage `json:"pane,omitempty"`
	Error string          `json:"error,omitempty"`
}

func init() {
	core.RegisterCtx("terminal_control_split", func(ctx context.Context, _, raw string) (string, error) {
		var args paneArgs
		if err := core.DecodeArgsPayload(raw, &args); err != nil {
			return "", err
		}
		if !validDirection(args.Direction) {
			return "", errors.New("direction must be right or down")
		}
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		result, err := control.paneOperation(ctx, origin{}, "split", args)
		return string(result), err
	})
}

func validDirection(direction string) bool { return direction == "right" || direction == "down" }

func (s *serverState) handlePaneOp(w http.ResponseWriter, r *http.Request, source origin, op string) {
	var args paneArgs
	if !decodeRequest(w, r, &args) {
		return
	}
	start := time.Now()
	success := false
	defer func() { s.record(start, source, "pane."+op, success) }()
	if op == "split" && !validDirection(args.Direction) {
		writeError(w, http.StatusBadRequest, "direction must be right or down")
		return
	}
	if op == "close" && args.Pane == "" {
		writeError(w, http.StatusBadRequest, "pane is required")
		return
	}
	if op == "split" && args.Pane == "" {
		args.Pane = source.PaneID
	}
	data, err := s.paneOperation(r.Context(), source, op, args)
	if err != nil {
		writeForwardError(w, err)
		return
	}
	success = true
	_, _ = w.Write(data)
}

func (s *serverState) paneOperation(ctx context.Context, source origin, op string, args paneArgs) (json.RawMessage, error) {
	if op == "split" && args.Pane == "" {
		op = "tab"
	}
	if args.Pane != "" && (op == "split" || op == "close") {
		raw, _ := core.EncodeResult(map[string]string{"pane": args.Pane})
		_, err := core.DispatchContext(ctx, "terminal_control_info", raw)
		if errors.Is(err, ErrPaneNotFound) {
			return nil, err
		}
	}
	fields := map[string]any{"op": op}
	if args.Pane != "" && op != "tab" {
		fields["pane"] = args.Pane
	}
	if op == "split" {
		fields["direction"] = args.Direction
	}
	if op != "close" {
		for name, value := range map[string]string{"cwd": args.CWD, "title": args.Title, "command": args.Command} {
			if value != "" {
				fields[name] = value
			}
		}
	}
	raw, err := s.forward(ctx, source, "agent-control:pane-op", fields)
	if err != nil {
		return nil, err
	}
	var result paneResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	if result.Error != "" {
		return nil, errors.New(result.Error)
	}
	if op == "close" {
		return json.RawMessage("{}"), nil
	}
	if len(result.Pane) == 0 || string(result.Pane) == "null" {
		return nil, errors.New("missing pane result")
	}
	_, _ = core.DispatchContext(ctx, "terminal_control_set_metadata", string(result.Pane))
	return result.Pane, nil
}

func (s *serverState) handleTerminal(w http.ResponseWriter, r *http.Request, source origin, op string) {
	var args paneArgs
	if !decodeRequest(w, r, &args) {
		return
	}
	if (op != "list" && args.Pane == "") || args.Lines < 0 || args.TimeoutSecs < 0 {
		writeError(w, http.StatusBadRequest, "invalid pane arguments")
		return
	}
	start := time.Now()
	success := false
	defer func() { s.record(start, source, "pane."+op, success) }()
	command := "terminal_control_" + op
	if op == "show" {
		command = "terminal_control_info"
	}
	s.mu.Lock()
	_, status := s.authenticateLocked(source.token)
	s.mu.Unlock()
	if status != 0 {
		writeError(w, status, authError(status).Error())
		return
	}
	raw, _ := core.EncodeResult(args)
	result, err := core.DispatchContext(r.Context(), command, raw)
	if err != nil {
		writeForwardError(w, err)
		return
	}
	success = true
	_, _ = w.Write([]byte(result))
}

func decodeRequest(w http.ResponseWriter, r *http.Request, args any) bool {
	reader := http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(reader)
	var trailing json.RawMessage
	if err := decoder.Decode(args); err != nil || decoder.Decode(&trailing) != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid JSON request")
		return false
	}
	return true
}

func writeForwardError(w http.ResponseWriter, err error) {
	var auth authError
	switch {
	case errors.As(err, &auth):
		writeError(w, int(auth), auth.Error())
	case errors.Is(err, errNotReady):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, ErrPaneNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusGatewayTimeout, "MinCode control request timed out")
	case errors.Is(err, context.Canceled):
		// The caller disconnected before a reply.
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.WriteHeader(status)
	writeJSON(w, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, data any) { _ = json.NewEncoder(w).Encode(data) }
