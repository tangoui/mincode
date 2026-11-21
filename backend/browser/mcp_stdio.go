package browser

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	internalMCPModeFlag   = "--internal-browser-mcp"
	internalMCPProtocol   = "2024-11-05"
	internalMCPServerName = "mincode-workbench"
	internalMCPVersion    = "1.0.0"
)

const (
	internalMCPURLEnv       = "MINCODE_INTERNAL_BROWSER_MCP_URL"
	internalMCPTokenEnv     = "MINCODE_INTERNAL_BROWSER_MCP_TOKEN"
	internalMCPServerIDEnv  = "MINCODE_INTERNAL_BROWSER_MCP_SERVER_ID"
	internalMCPSessionIDEnv = "MINCODE_INTERNAL_BROWSER_MCP_SESSION_ID"
)

type internalMCPStdioConfig struct {
	URL       string
	Token     string
	ServerID  string
	SessionID string
}

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type internalMCPCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type internalMCPBridgeClient struct {
	config internalMCPStdioConfig
	client *http.Client
}

func IsInternalMCPMode(args []string) bool {
	for _, argument := range args {
		if argument == internalMCPModeFlag {
			return true
		}
	}
	return false
}

func internalMCPConfigFromEnvironment() (internalMCPStdioConfig, error) {
	config := internalMCPStdioConfig{
		URL:       strings.TrimSpace(os.Getenv(internalMCPURLEnv)),
		Token:     strings.TrimSpace(os.Getenv(internalMCPTokenEnv)),
		ServerID:  strings.TrimSpace(os.Getenv(internalMCPServerIDEnv)),
		SessionID: strings.TrimSpace(os.Getenv(internalMCPSessionIDEnv)),
	}
	if config.URL == "" || config.Token == "" || config.ServerID == "" {
		return internalMCPStdioConfig{}, errors.New("internal browser MCP configuration is incomplete")
	}
	if err := validateInternalMCPBridgeURL(config.URL); err != nil {
		return internalMCPStdioConfig{}, err
	}
	return config, nil
}

func validateInternalMCPBridgeURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" {
		return errors.New("internal browser MCP bridge must use an IPv4 loopback URL")
	}
	if parsed.Path != internalMCPBridgePath || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("internal browser MCP bridge URL is invalid")
	}
	return nil
}

// RunInternalMCPStdio serves the private MCP endpoint used by ACP Agents. It writes
// protocol messages only to stdout; credentials are read from inherited environment
// variables and are never included in protocol errors.
func RunInternalMCPStdio(input io.Reader, output io.Writer) error {
	config, err := internalMCPConfigFromEnvironment()
	if err != nil {
		return err
	}
	return runInternalMCPStdio(input, output, config)
}

func runInternalMCPStdio(input io.Reader, output io.Writer, config internalMCPStdioConfig) error {
	bridge := &internalMCPBridgeClient{
		config: config,
		client: &http.Client{
			Timeout: 40 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return errors.New("internal browser MCP bridge redirects are not allowed")
			},
		},
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	encoder := json.NewEncoder(output)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var request jsonRPCRequest
		if err := json.Unmarshal(line, &request); err != nil {
			if writeErr := encoder.Encode(jsonRPCResponse{JSONRPC: "2.0", Error: &jsonRPCError{Code: -32700, Message: "parse error"}}); writeErr != nil {
				return writeErr
			}
			continue
		}
		if len(request.ID) == 0 || bytes.Equal(request.ID, []byte("null")) {
			// MCP notifications do not receive a response.
			continue
		}
		response := bridge.handleRequest(request)
		if err := encoder.Encode(response); err != nil {
			return fmt.Errorf("write internal browser MCP response: %w", err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read internal browser MCP request: %w", err)
	}
	return nil
}

func (b *internalMCPBridgeClient) handleRequest(request jsonRPCRequest) jsonRPCResponse {
	response := jsonRPCResponse{JSONRPC: "2.0", ID: request.ID}
	if request.JSONRPC != "2.0" || strings.TrimSpace(request.Method) == "" {
		response.Error = &jsonRPCError{Code: -32600, Message: "invalid request"}
		return response
	}
	switch request.Method {
	case "initialize":
		response.Result = map[string]any{
			"protocolVersion": internalMCPProtocol,
			"capabilities": map[string]any{
				"tools": map[string]any{"listChanged": false},
			},
			"serverInfo": map[string]any{"name": internalMCPServerName, "version": internalMCPVersion},
		}
	case "ping":
		response.Result = map[string]any{}
	case "tools/list":
		response.Result = map[string]any{"tools": InternalMCPToolDefinitions()}
	case "tools/call":
		var params internalMCPCallParams
		decoder := json.NewDecoder(bytes.NewReader(request.Params))
		decoder.UseNumber()
		if err := decoder.Decode(&params); err != nil || strings.TrimSpace(params.Name) == "" {
			response.Error = &jsonRPCError{Code: -32602, Message: "invalid tool call parameters"}
			return response
		}
		if params.Arguments == nil {
			params.Arguments = map[string]any{}
		}
		result, err := b.callTool(params.Name, params.Arguments)
		if err != nil {
			response.Result = mcpToolErrorResult(err)
			return response
		}
		response.Result = mcpToolResult(params.Name, result)
	default:
		response.Error = &jsonRPCError{Code: -32601, Message: "method not found"}
	}
	return response
}

func (b *internalMCPBridgeClient) callTool(toolName string, arguments map[string]any) (any, error) {
	if !isInternalMCPTool(strings.TrimSpace(toolName)) {
		return nil, errors.New("tool is not available through MinCode ACP MCP")
	}
	body, err := json.Marshal(internalMCPBridgeRequest{ToolName: toolName, Arguments: arguments})
	if err != nil {
		return nil, errors.New("encode MinCode tool request")
	}
	request, err := http.NewRequest(http.MethodPost, b.config.URL, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("create MinCode tool request")
	}
	request.Header.Set("Authorization", "Bearer "+b.config.Token)
	request.Header.Set("Content-Type", "application/json")
	response, err := b.client.Do(request)
	if err != nil {
		return nil, errors.New("MinCode tool bridge is unavailable")
	}
	defer response.Body.Close()
	var payload internalMCPBridgeResponse
	decoder := json.NewDecoder(io.LimitReader(response.Body, 64<<20))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return nil, errors.New("invalid MinCode tool bridge response")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || payload.Error != "" {
		if payload.Error == "" {
			return nil, errors.New("MinCode tool failed")
		}
		if payload.Code != "" {
			return nil, newToolError(payload.Code, payload.Error, payload.Details)
		}
		return nil, errors.New(payload.Error)
	}
	return payload.Result, nil
}

// mcpToolErrorResult reports a failed tool call. Structured browser errors
// lead with "[code] message" and add the {"code","message","details"} JSON so
// agents can react to dialog_pending and similar states.
func mcpToolErrorResult(err error) map[string]any {
	content := []map[string]any{{"type": "text", "text": err.Error()}}
	var toolErr *browserToolError
	if errors.As(err, &toolErr) {
		content = []map[string]any{
			{"type": "text", "text": "[" + toolErr.Code + "] " + toolErr.Message},
			{"type": "text", "text": toolErr.encoded()},
		}
	}
	return map[string]any{"content": content, "isError": true}
}

func mcpToolResult(toolName string, result any) map[string]any {
	if isMCPImageTool(toolName) {
		if values, ok := result.(map[string]any); ok {
			data, _ := values["data"].(string)
			mimeType, _ := values["mimeType"].(string)
			if data != "" && mimeType != "" {
				metadata := make(map[string]any, len(values)-1)
				for key, value := range values {
					if key != "data" {
						metadata[key] = value
					}
				}
				encodedMetadata, _ := json.Marshal(metadata)
				return map[string]any{
					"content": []map[string]any{
						{"type": "text", "text": string(encodedMetadata)},
						{"type": "image", "data": data, "mimeType": mimeType},
					},
					"isError": false,
				}
			}
		}
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		encoded = []byte("null")
	}
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": string(encoded)}},
		"isError": false,
	}
}

func isMCPImageTool(toolName string) bool {
	switch toolName {
	case "browser.capture_view", "computer.capture_screen", "computer.open_app", "computer.open_url", "computer.execute":
		return true
	default:
		return false
	}
}
