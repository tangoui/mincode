// MCP HTTP / Streamable HTTP / SSE 传输：JSON-RPC over HTTP POST。
// 对齐 mcp/http.rs。
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mincode/app/backend/core"
)

const (
	mcpMaxResponseBytes = 16 * 1024 * 1024
	mcpMaxErrorBytes    = 64 * 1024
)

type httpConn struct {
	serverID string
	url      string
	headers  map[string]string
	client   *http.Client
	// oauth is set for connectors marked auth "oauth": every request carries
	// Authorization: Bearer <bearer>, replacing any configured header.
	oauth  *OAuthTokenSource
	authM  sync.Mutex
	bearer string

	sessM     sync.Mutex
	sessionID string
	nextID    atomic.Uint64
	ctx       context.Context
	cancel    context.CancelFunc
}

// connectHTTP 校验 URL、建立客户端、完成 initialize 握手并抓取能力列表。
// auth 为 "oauth" 时先向 OAuthTokenSource 取访问令牌（快过期会先刷新），未登录
// 则不发出任何请求、直接返回 [MCP_AUTH_REQUIRED]。
func connectHTTP(parent context.Context, serverID, url string, headers map[string]string, auth string) (*mcpConnection, error) {
	if err := core.ValidateRequestURL(url); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	c := &httpConn{
		serverID: serverID,
		url:      url,
		headers:  headers,
		client:   newMCPHTTPClient(),
		ctx:      ctx,
		cancel:   cancel,
	}
	if auth == authOAuth {
		source, err := currentOAuthSource()
		if err != nil {
			cancel()
			return nil, err
		}
		token, err := source.AccessToken(ctx, serverID, url)
		if err != nil {
			cancel()
			return nil, err
		}
		c.oauth = &source
		c.bearer = token
	}
	if err := c.initialize(); err != nil {
		cancel()
		return nil, err
	}
	tools, _ := c.fetchTools()
	resources, _ := c.fetchResources()
	prompts, _ := c.fetchPrompts()
	return &mcpConnection{
		tools:     tools,
		resources: resources,
		prompts:   prompts,
		transport: c,
	}, nil
}

const mcpMaxRedirects = 5

func newMCPHTTPClient() *http.Client {
	return &http.Client{Timeout: defaultCallTimeoutSecs * time.Second, CheckRedirect: mcpCheckRedirect}
}

// mcpCheckRedirect 只允许同源（scheme + host + port 均相同）重定向：用户配置的
// 自定义头（API Key 等）与 307/308 保留的请求体不能被转发到其他主机，也不能
// 降级为明文 http。每一跳都复用初次连接的 URL 校验，避免借跳转访问元数据端点。
func mcpCheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= mcpMaxRedirects {
		return errors.New("MCP 重定向次数过多")
	}
	if err := core.ValidateRequestURL(req.URL.String()); err != nil {
		return fmt.Errorf("MCP 重定向目标被拒绝: %w", err)
	}
	if len(via) == 0 {
		return nil
	}
	origin := via[0].URL
	if strings.EqualFold(origin.Scheme, "https") && !strings.EqualFold(req.URL.Scheme, "https") {
		return errors.New("MCP 重定向从 https 降级到 http 已被拒绝")
	}
	if !sameOrigin(origin, req.URL) {
		return fmt.Errorf("MCP 跨主机重定向已被拒绝: %s", req.URL.Redacted())
	}
	return nil
}

func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) &&
		strings.EqualFold(a.Hostname(), b.Hostname()) &&
		effectivePort(a) == effectivePort(b)
}

func effectivePort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	return "80"
}

func (c *httpConn) request(method string, params any) (json.RawMessage, error) {
	return c.requestCtx(context.Background(), method, params)
}

// requestCtx aborts the HTTP exchange when ctx is cancelled and then tells the
// server with notifications/cancelled (in the background, best effort).
func (c *httpConn) requestCtx(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	body := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
	result, err := c.postJSONRPC(ctx, body, &id)
	if err != nil && ctx.Err() != nil {
		go func() {
			_ = c.notify("notifications/cancelled", map[string]any{"requestId": id, "reason": "cancelled by the user"})
		}()
		return nil, errToolCallCancelled
	}
	return result, err
}

func (c *httpConn) notify(method string, params any) error {
	body := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
	_, err := c.postJSONRPC(context.Background(), body, nil)
	return err
}

func (c *httpConn) postJSONRPC(ctx context.Context, body any, expectID *uint64) (json.RawMessage, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("JSON encode: %w", err)
	}
	// The request ends when either the connection or this call is cancelled.
	requestCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	for attempt := 0; ; attempt++ {
		resp, bearer, err := c.send(requestCtx, payload)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusUnauthorized {
			retry, err := c.handleUnauthorized(requestCtx, resp, bearer, attempt)
			if err != nil || retry {
				drainAndClose(resp.Body)
				if err != nil {
					return nil, err
				}
				continue
			}
		}
		result, err := c.readResponse(resp, expectID)
		resp.Body.Close()
		return result, err
	}
}

// send posts one JSON-RPC payload and returns the bearer token it carried.
func (c *httpConn) send(ctx context.Context, payload []byte) (*http.Response, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(payload))
	if err != nil {
		return nil, "", fmt.Errorf("[mcp:%s] build request: %w", c.serverID, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	var bearer string
	if c.oauth != nil {
		c.authM.Lock()
		bearer = c.bearer
		c.authM.Unlock()
		// Set after the configured headers so it replaces any Authorization
		// value (header names are canonicalised, so case does not matter).
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	c.sessM.Lock()
	sid := c.sessionID
	c.sessM.Unlock()
	if sid != "" {
		req.Header.Set("Mcp-Session-Id", sid)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("[mcp:%s] HTTP request failed: %w", c.serverID, err)
	}
	return resp, bearer, nil
}

// handleUnauthorized decides what a 401 means. OAuth connectors refresh the
// rejected token once and retry once; a second 401 (or a failed refresh) is
// [MCP_AUTH_REQUIRED]. Other connectors report [MCP_AUTH_REQUIRED] when the
// server offers a Bearer challenge, so the UI can suggest signing in;
// otherwise the 401 is an ordinary HTTP error.
func (c *httpConn) handleUnauthorized(ctx context.Context, resp *http.Response, rejected string, attempt int) (bool, error) {
	if c.oauth == nil {
		if hasBearerChallenge(resp.Header.Values("WWW-Authenticate")) {
			return false, authRequiredError(c.serverID)
		}
		return false, nil
	}
	if attempt > 0 {
		return false, authRequiredError(c.serverID)
	}
	token, err := c.oauth.RefreshAfterUnauthorized(ctx, c.serverID, c.url, rejected)
	if err != nil {
		return false, fmt.Errorf("[mcp:%s] %w", c.serverID, err)
	}
	c.authM.Lock()
	c.bearer = token
	c.authM.Unlock()
	return true, nil
}

func drainAndClose(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, mcpMaxErrorBytes))
	_ = body.Close()
}

func (c *httpConn) readResponse(resp *http.Response, expectID *uint64) (json.RawMessage, error) {
	if h := resp.Header.Get("Mcp-Session-Id"); h != "" {
		c.sessM.Lock()
		c.sessionID = h
		c.sessM.Unlock()
	}

	contentType := strings.ToLower(resp.Header.Get("Content-Type"))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		text, readErr := core.ReadAllLimited(resp.Body, mcpMaxErrorBytes)
		if readErr != nil {
			return nil, fmt.Errorf("[mcp:%s] HTTP status %d（错误响应过大或读取失败）: %w", c.serverID, resp.StatusCode, readErr)
		}
		return nil, fmt.Errorf("[mcp:%s] HTTP status %d: %s", c.serverID, resp.StatusCode, string(text))
	}

	if expectID == nil && (resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNoContent) {
		return json.RawMessage("null"), nil
	}

	if strings.Contains(contentType, "text/event-stream") {
		return parseSSEResponse(resp.Body, expectID, c.serverID)
	}

	text, err := core.ReadAllLimited(resp.Body, mcpMaxResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("[mcp:%s] HTTP read body: %w", c.serverID, err)
	}
	if expectID == nil && strings.TrimSpace(string(text)) == "" {
		return json.RawMessage("null"), nil
	}
	return parseJSONRPCResponse(string(text), expectID)
}

func (c *httpConn) initialize() error {
	_, err := c.request("initialize", map[string]any{
		"protocolVersion": mcpProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": clientName, "version": clientVersion},
	})
	if err != nil {
		return err
	}
	return c.notify("notifications/initialized", map[string]any{})
}

func (c *httpConn) fetchTools() ([]McpToolDef, error) {
	res, err := c.request("tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	return parseList[McpToolDef](res, "tools")
}

func (c *httpConn) fetchResources() ([]McpResourceDef, error) {
	res, err := c.request("resources/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	return parseList[McpResourceDef](res, "resources")
}

func (c *httpConn) fetchPrompts() ([]McpPromptDef, error) {
	res, err := c.request("prompts/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	return parseList[McpPromptDef](res, "prompts")
}

func (c *httpConn) callTool(ctx context.Context, toolName string, arguments json.RawMessage) (*McpCallResult, error) {
	res, err := c.requestCtx(ctx, "tools/call", map[string]any{
		"name":      toolName,
		"arguments": rawOrNull(arguments),
	})
	if err != nil {
		return nil, err
	}
	var out McpCallResult
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, fmt.Errorf("Failed to parse tools/call: %w", err)
	}
	return &out, nil
}

func (c *httpConn) readResource(uri string) (*McpResourceContent, error) {
	res, err := c.request("resources/read", map[string]any{"uri": uri})
	if err != nil {
		return nil, err
	}
	return parseResourceContent(res)
}

func (c *httpConn) getPrompt(name string, arguments json.RawMessage) (*McpPromptResult, error) {
	res, err := c.request("prompts/get", map[string]any{
		"name":      name,
		"arguments": rawOrNull(arguments),
	})
	if err != nil {
		return nil, err
	}
	var out McpPromptResult
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, fmt.Errorf("Failed to parse prompts/get: %w", err)
	}
	return &out, nil
}

func (c *httpConn) close() { c.cancel() }

func parseSSEResponse(body io.Reader, expectID *uint64, serverID string) (json.RawMessage, error) {
	scanner := bufio.NewScanner(io.LimitReader(body, mcpMaxResponseBytes+1))
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		if value, err := parseJSONRPCResponse(data, expectID); err == nil {
			return value, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("[mcp:%s] SSE read failed: %w", serverID, err)
	}
	return nil, fmt.Errorf("[mcp:%s] SSE stream ended without a matching response", serverID)
}

func parseJSONRPCResponse(text string, expectID *uint64) (json.RawMessage, error) {
	var msg struct {
		ID     *uint64         `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(text), &msg); err != nil {
		return nil, fmt.Errorf("Invalid MCP JSON response: %w", err)
	}
	if expectID != nil {
		if msg.ID == nil || *msg.ID != *expectID {
			return nil, fmt.Errorf("MCP response id mismatch")
		}
	}
	if msg.Error != nil {
		m := msg.Error.Message
		if m == "" {
			m = "MCP error"
		}
		return nil, fmt.Errorf("%s", m)
	}
	if msg.Result == nil {
		return json.RawMessage("null"), nil
	}
	return msg.Result, nil
}
