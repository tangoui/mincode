package externalmcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   any             `json:"error,omitempty"`
}

func readConfig() (config, error) {
	path, err := rendezvousPath()
	if err != nil {
		return config{}, err
	}
	release, err := lockRendezvousParents(path)
	if err != nil {
		return config{}, err
	}
	defer release()
	data, err := readPrivateRecord(path)
	if err != nil {
		return config{}, err
	}
	var c config
	if err = json.Unmarshal(data, &c); err != nil {
		return config{}, err
	}
	if !validEndpoint(c.ControlURL, c.ControlToken) {
		return config{}, errors.New("invalid MinCode control record")
	}
	return c, nil
}

func validEndpoint(address, token string) bool {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return false
	}
	if u.EscapedPath() != "" && u.EscapedPath() != "/" {
		return false
	}
	_, err = hex.DecodeString(token)
	return len(token) == 64 && err == nil
}

// RunStdio speaks newline-delimited JSON-RPC MCP. stdout is reserved for protocol output.
func RunStdio(input io.Reader, output io.Writer) error {
	controlURL, controlToken := os.Getenv("MINCODE_CONTROL_URL"), os.Getenv("MINCODE_CONTROL_TOKEN")
	if controlURL != "" || controlToken != "" {
		if !validEndpoint(controlURL, controlToken) {
			return errors.New("invalid MinCode control environment")
		}
		fixed := config{ControlURL: controlURL, ControlToken: controlToken}
		return runStdio(input, output, func() (config, error) { return fixed, nil })
	}
	if _, err := readConfig(); err != nil {
		return err
	}
	return runStdio(input, output, readConfig)
}

func initializationResult() any {
	return map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]string{"name": "mincode-external-local", "version": "1.0.0"},
	}
}

type paneTool struct {
	name, description string
	properties        map[string]any
	required          []string
}

var paneTools = []paneTool{
	{"split", "Open a split beside a pane or a new terminal tab", map[string]any{
		"pane": stringSchema(), "direction": map[string]any{"type": "string", "enum": []string{"right", "down"}},
		"cwd": stringSchema(), "title": stringSchema(), "command": stringSchema(),
	}, []string{"direction"}},
	{"tab", "Open a terminal tab", map[string]any{"cwd": stringSchema(), "title": stringSchema(), "command": stringSchema()}, nil},
	{"send", "Send text to a terminal pane and optionally press Enter", map[string]any{"pane": stringSchema(), "text": stringSchema(), "submit": map[string]string{"type": "boolean"}}, []string{"pane", "text"}},
	{"read", "Read recent plain text output of a terminal pane", map[string]any{"pane": stringSchema(), "lines": nonnegativeInteger()}, []string{"pane"}},
	{"wait", "Wait for a terminal pane to finish its turn or request input", map[string]any{"pane": stringSchema(), "timeoutSecs": nonnegativeInteger()}, []string{"pane"}},
	{"list", "List terminal panes", map[string]any{}, nil},
	{"show", "Show a terminal pane", map[string]any{"pane": stringSchema()}, []string{"pane"}},
	{"close", "Close a terminal pane", map[string]any{"pane": stringSchema()}, []string{"pane"}},
}

func stringSchema() map[string]string    { return map[string]string{"type": "string"} }
func nonnegativeInteger() map[string]any { return map[string]any{"type": "integer", "minimum": 0} }

func (tool paneTool) definition() map[string]any {
	schema := map[string]any{"type": "object", "properties": tool.properties, "additionalProperties": false}
	if len(tool.required) > 0 {
		schema["required"] = tool.required
	}
	return map[string]any{"name": "pane." + tool.name, "description": tool.description, "inputSchema": schema}
}

type controlHTTP struct {
	client      *http.Client
	base, token string
}

func (bridge controlHTTP) post(ctx context.Context, route string, body []byte) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, bridge.base+route, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("MinCode control service unavailable")
	}
	req.Header.Set("Authorization", "Bearer "+bridge.token)
	req.Header.Set("Content-Type", "application/json")
	res, err := bridge.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("MinCode control service unavailable")
	}
	defer res.Body.Close()
	const maxResponse = 64 << 20
	data, err := io.ReadAll(io.LimitReader(res.Body, maxResponse+1))
	if err != nil || len(data) > maxResponse || !json.Valid(data) {
		return nil, errors.New("invalid MinCode control response")
	}
	if res.StatusCode != http.StatusOK {
		var rejected struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &rejected) == nil && rejected.Error != "" {
			return nil, errors.New(rejected.Error)
		}
		return nil, errors.New(http.StatusText(res.StatusCode))
	}
	return data, nil
}

func runStdio(input io.Reader, output io.Writer, credentials func() (config, error)) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	noRedirect := func(*http.Request, []*http.Request) error { return errors.New("redirect denied") }
	client := &http.Client{Timeout: 10*time.Minute + 20*time.Second, CheckRedirect: noRedirect}
	respond := func(ctx context.Context, q request) response {
		if q.Method != "tools/list" && q.Method != "tools/call" {
			return (controlHTTP{}).respond(ctx, q)
		}
		c, err := credentials()
		if err != nil {
			return rpcError(q.ID, -32603, "MinCode MCP bridge unavailable")
		}
		bridge := controlHTTP{client: client, base: strings.TrimSuffix(c.ControlURL, "/"), token: c.ControlToken}
		return bridge.respond(ctx, q)
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	encoder := json.NewEncoder(output)
	var writeMu sync.Mutex
	var writeErr error
	write := func(res response) {
		writeMu.Lock()
		defer writeMu.Unlock()
		if writeErr == nil {
			writeErr = encoder.Encode(res)
			if writeErr != nil {
				cancel()
				// A closed output also unblocks the real stdio helper's read.
				if file, ok := input.(*os.File); ok && file == os.Stdin {
					_ = file.Close()
				}
			}
		}
	}
	var requests sync.WaitGroup
	var pendingMu sync.Mutex
	pending := make(map[string]context.CancelFunc)
	for scanner.Scan() {
		if ctx.Err() != nil {
			break
		}
		var q request
		if err := json.Unmarshal(scanner.Bytes(), &q); err != nil {
			write(rpcError(json.RawMessage("null"), -32700, "parse error"))
			continue
		}
		if q.Method == "notifications/cancelled" && (len(q.ID) == 0 || bytes.Equal(q.ID, []byte("null"))) {
			var args struct {
				ID json.RawMessage `json:"requestId"`
			}
			if json.Unmarshal(q.Params, &args) == nil {
				pendingMu.Lock()
				if stop := pending[string(args.ID)]; stop != nil {
					stop()
				}
				pendingMu.Unlock()
			}
			continue
		}
		if len(q.ID) == 0 || bytes.Equal(q.ID, []byte("null")) {
			continue
		}
		if q.JSONRPC != "2.0" {
			write(rpcError(q.ID, -32600, "invalid request"))
			continue
		}
		if q.Method != "tools/list" && q.Method != "tools/call" {
			write(respond(ctx, q))
			continue
		}
		pendingMu.Lock()
		id := string(q.ID)
		if len(pending) >= 32 || pending[id] != nil {
			pendingMu.Unlock()
			write(rpcError(q.ID, -32600, "too many pending requests or duplicate request ID"))
			continue
		}
		requestCtx, stop := context.WithCancel(ctx)
		pending[id] = stop
		pendingMu.Unlock()
		requests.Go(func() {
			defer stop()
			res := respond(requestCtx, q)
			pendingMu.Lock()
			delete(pending, id)
			pendingMu.Unlock()
			write(res)
		})
	}
	if scanner.Err() != nil || input == os.Stdin {
		// EOF on the actual protocol transport means its client has gone.
		cancel()
	}
	requests.Wait()
	if writeErr != nil {
		return writeErr
	}
	return scanner.Err()
}

func rpcError(id json.RawMessage, code int, message string) response {
	return response{JSONRPC: "2.0", ID: id, Error: map[string]any{"code": code, "message": message}}
}

func (bridge controlHTTP) respond(ctx context.Context, q request) response {
	res := response{JSONRPC: "2.0", ID: q.ID}
	switch q.Method {
	case "initialize":
		res.Result = initializationResult()
	case "ping":
		res.Result = map[string]any{}
	case "tools/list":
		data, err := bridge.post(ctx, "/v1/tools/list", []byte("{}"))
		if err != nil {
			return rpcError(q.ID, -32603, err.Error())
		}
		var list struct {
			Tools []json.RawMessage `json:"tools"`
		}
		if json.Unmarshal(data, &list) != nil || list.Tools == nil {
			return rpcError(q.ID, -32603, "invalid MinCode tool list")
		}
		for _, item := range paneTools {
			encoded, _ := json.Marshal(item.definition())
			list.Tools = append(list.Tools, encoded)
		}
		res.Result = list
	case "tools/call":
		var args struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal(q.Params, &args) != nil || args.Name == "" {
			return rpcError(q.ID, -32602, "tool name is required")
		}
		if len(args.Arguments) == 0 || bytes.Equal(args.Arguments, []byte("null")) {
			args.Arguments = json.RawMessage("{}")
		}
		if args.Arguments[0] != '{' {
			return rpcError(q.ID, -32602, "tool arguments must be an object")
		}
		for _, tool := range paneTools {
			if args.Name != "pane."+tool.name {
				continue
			}
			data, err := bridge.post(ctx, "/v1/panes/"+tool.name, args.Arguments)
			if err != nil {
				res.Result = mcpText(err.Error(), true)
			} else {
				res.Result = mcpText(string(bytes.TrimSpace(data)), false)
			}
			return res
		}
		data, _ := json.Marshal(args)
		result, err := bridge.post(ctx, "/v1/tools/call", data)
		if err != nil {
			res.Result = mcpText(err.Error(), true)
		} else {
			res.Result = result
		}
	default:
		return rpcError(q.ID, -32601, "method not found")
	}
	return res
}

func mcpText(text string, failed bool) any {
	return map[string]any{"content": []map[string]string{{"type": "text", "text": text}}, "isError": failed}
}
