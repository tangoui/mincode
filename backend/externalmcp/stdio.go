package externalmcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
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

var tools = []map[string]any{
	{"name": "workspace.list_projects", "description": "List enabled project identifiers and names (no absolute paths)", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}},
	{"name": "workspace.search_files", "description": "Search indexed file path metadata in enabled projects", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer", "maximum": 50}}, "required": []string{"query"}, "additionalProperties": false}},
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
	u, err := url.Parse(c.URL)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.Path != "/tool" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(c.Token) != 64 {
		return config{}, errors.New("invalid MCP rendezvous record")
	}
	return c, nil
}

// RunStdio speaks newline-delimited JSON-RPC MCP. stdout is reserved for protocol output.
func RunStdio(input io.Reader, output io.Writer) error {
	c, err := readConfig()
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 7 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect denied") }}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 8192)
	encoder := json.NewEncoder(output)
	for scanner.Scan() {
		var q request
		if err := json.Unmarshal(scanner.Bytes(), &q); err != nil {
			if err = encoder.Encode(response{JSONRPC: "2.0", Error: map[string]any{"code": -32700, "message": "parse error"}}); err != nil {
				return err
			}
			continue
		}
		if len(q.ID) == 0 || bytes.Equal(q.ID, []byte("null")) {
			continue
		}
		res := response{JSONRPC: "2.0", ID: q.ID}
		fail := func(code int, message string) { res.Error = map[string]any{"code": code, "message": message} }
		if q.JSONRPC != "2.0" {
			fail(-32600, "invalid request")
		} else {
			switch q.Method {
			case "initialize":
				res.Result = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "mincode-external-local", "version": "1.0.0"}}
			case "ping":
				res.Result = map[string]any{}
			case "tools/list":
				res.Result = map[string]any{"tools": tools}
			case "tools/call":
				var p struct {
					Name      string          `json:"name"`
					Arguments json.RawMessage `json:"arguments"`
				}
				if json.Unmarshal(q.Params, &p) != nil || (p.Name != "workspace.list_projects" && p.Name != "workspace.search_files") {
					fail(-32602, "tool not allowed")
					break
				}
				body, _ := json.Marshal(map[string]any{"name": p.Name, "arguments": json.RawMessage(p.Arguments)})
				req, err := http.NewRequest(http.MethodPost, c.URL, bytes.NewReader(body))
				if err != nil {
					fail(-32603, "bridge unavailable")
					break
				}
				req.Header.Set("Authorization", "Bearer "+c.Token)
				req.Header.Set("Content-Type", "application/json")
				reply, err := client.Do(req)
				if err != nil {
					fail(-32603, "bridge unavailable")
					break
				}
				data, readErr := io.ReadAll(io.LimitReader(reply.Body, 64<<10))
				reply.Body.Close()
				if readErr != nil || reply.StatusCode != 200 || len(data) >= 64<<10 {
					res.Result = map[string]any{"content": []map[string]string{{"type": "text", "text": "tool unavailable or denied"}}, "isError": true}
					break
				}
				res.Result = map[string]any{"content": []map[string]string{{"type": "text", "text": strings.TrimSpace(string(data))}}}
			default:
				fail(-32601, "method not found")
			}
		}
		if err := encoder.Encode(res); err != nil {
			return err
		}
	}
	return scanner.Err()
}
