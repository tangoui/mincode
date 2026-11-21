// MCP 域：Model Context Protocol 客户端网关（stdio 子进程 + Streamable HTTP/SSE 传输）。
// 对齐 Tauri src-tauri/src/mcp/*。纯 stdlib 实现，无第三方依赖。
//
// 命令契约（参数/返回均 camelCase JSON）：
//
//	mcp_connect_stdio  {serverId, command, args:[], env:{}}                 -> McpConnectResult
//	mcp_connect_http   {serverId, url, headers:{}, auth?}                   -> McpConnectResult
//	mcp_connect        {serverId, transport, command?, args?, env?, url?, headers?, auth?} -> McpConnectResult
//	mcp_disconnect     {serverId}                                           -> void
//	mcp_disconnect_all                                                      -> void
//	mcp_list_tools     {serverId}                                           -> [McpToolDef]
//	mcp_list_resources {serverId}                                           -> [McpResourceDef]
//	mcp_list_prompts   {serverId}                                           -> [McpPromptDef]
//	mcp_call_tool      {serverId, toolName, arguments}                      -> McpCallResult
//	mcp_read_resource  {serverId, uri}                                      -> McpResourceContent
//	mcp_get_prompt     {serverId, name, arguments}                          -> McpPromptResult
//	mcp_list_status                                                         -> [McpServerStatus]
//	mcp_read_config_file {path}                                             -> string
package mcp

import (
	"mincode/app/backend/core"

	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	mcpProtocolVersion     = "2024-11-05"
	defaultCallTimeoutSecs = 120
	clientName             = "MinCode"
	clientVersion          = "1.0.0"
)

var gateway = newMcpGateway()

// Shutdown 取消连接中的传输并关闭全部 MCP 会话。
func Shutdown() { gateway.disconnectAll() }

func init() {
	core.Register("mcp_connect_stdio", mcpConnectStdioHandler)
	core.Register("mcp_connect_http", mcpConnectHTTPHandler)
	core.Register("mcp_connect", mcpConnectHandler)
	core.Register("mcp_disconnect", mcpDisconnectHandler)
	core.Register("mcp_disconnect_all", mcpDisconnectAllHandler)
	core.Register("mcp_list_tools", mcpListToolsHandler)
	core.Register("mcp_list_resources", mcpListResourcesHandler)
	core.Register("mcp_list_prompts", mcpListPromptsHandler)
	core.Register("mcp_call_tool", mcpCallToolHandler)
	core.Register("mcp_cancel_tool", mcpCancelToolHandler)
	core.Register("mcp_read_resource", mcpReadResourceHandler)
	core.Register("mcp_get_prompt", mcpGetPromptHandler)
	core.Register("mcp_list_status", mcpListStatusHandler)
	core.Register("mcp_read_config_file", mcpReadConfigFileHandler)
}

// ─── 数据类型（camelCase JSON，对齐 mcp/types.rs）────────────────────────────

type McpToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

type McpResourceDef struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description"`
	MimeType    string `json:"mimeType"`
}

type McpPromptDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Arguments   json.RawMessage `json:"arguments,omitempty"`
}

type McpContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type McpCallResult struct {
	Content []McpContentBlock `json:"content"`
	IsError bool              `json:"isError"`
}

type McpResourceContent struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType"`
	Text     string `json:"text"`
}

type McpPromptResult struct {
	Description string          `json:"description"`
	Messages    json.RawMessage `json:"messages,omitempty"`
}

type McpConnectResult struct {
	Tools     []McpToolDef     `json:"tools"`
	Resources []McpResourceDef `json:"resources"`
	Prompts   []McpPromptDef   `json:"prompts"`
}

type McpServerStatus struct {
	ID            string  `json:"id"`
	State         string  `json:"state"`
	ToolCount     int     `json:"toolCount"`
	ResourceCount int     `json:"resourceCount"`
	PromptCount   int     `json:"promptCount"`
	Error         *string `json:"error"`
}

// ─── 命令处理器 ─────────────────────────────────────────────────────────────

type connectStdioArgs struct {
	ServerID string            `json:"serverId"`
	Command  string            `json:"command"`
	Args     []string          `json:"args"`
	Env      map[string]string `json:"env"`
}

func mcpConnectStdioHandler(_ string, argsJSON string) (string, error) {
	var a connectStdioArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	if err := rejectPluginServerID(a.ServerID); err != nil {
		return "", err
	}
	res, err := gateway.connectStdio(a.ServerID, a.Command, a.Args, a.Env)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(res)
}

// rejectPluginServerID keeps plugin-owned session ids out of the WebView
// connect commands; those sessions start only through the plugin host.
func rejectPluginServerID(id string) error {
	if IsPluginServerID(id) {
		return fmt.Errorf("MCP server id '%s' is reserved for plugin servers", id)
	}
	return nil
}

type connectHTTPArgs struct {
	ServerID string            `json:"serverId"`
	URL      string            `json:"url"`
	Headers  map[string]string `json:"headers"`
	Auth     string            `json:"auth"`
}

func mcpConnectHTTPHandler(_ string, argsJSON string) (string, error) {
	var a connectHTTPArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	if err := rejectPluginServerID(a.ServerID); err != nil {
		return "", err
	}
	res, err := gateway.connectHTTP(a.ServerID, a.URL, a.Headers, a.Auth)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(res)
}

type connectArgs struct {
	ServerID  string            `json:"serverId"`
	Transport string            `json:"transport"`
	Command   string            `json:"command"`
	Args      []string          `json:"args"`
	Env       map[string]string `json:"env"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
	Auth      string            `json:"auth"`
}

func mcpConnectHandler(_ string, argsJSON string) (string, error) {
	var a connectArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	if err := rejectPluginServerID(a.ServerID); err != nil {
		return "", err
	}
	var (
		res *McpConnectResult
		err error
	)
	if a.Transport == "stdio" {
		res, err = gateway.connectStdio(a.ServerID, a.Command, a.Args, a.Env)
	} else {
		res, err = gateway.connectHTTP(a.ServerID, a.URL, a.Headers, a.Auth)
	}
	if err != nil {
		return "", err
	}
	return core.EncodeResult(res)
}

type serverIDArgs struct {
	ServerID string `json:"serverId"`
}

func mcpDisconnectHandler(_ string, argsJSON string) (string, error) {
	var a serverIDArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	gateway.disconnect(a.ServerID)
	return core.EncodeResult(nil)
}

func mcpDisconnectAllHandler(_ string, _ string) (string, error) {
	gateway.disconnectAll()
	return core.EncodeResult(nil)
}

func mcpListToolsHandler(_ string, argsJSON string) (string, error) {
	var a serverIDArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	conn, err := gateway.session(a.ServerID)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(conn.tools)
}

func mcpListResourcesHandler(_ string, argsJSON string) (string, error) {
	var a serverIDArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	conn, err := gateway.session(a.ServerID)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(conn.resources)
}

func mcpListPromptsHandler(_ string, argsJSON string) (string, error) {
	var a serverIDArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	conn, err := gateway.session(a.ServerID)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(conn.prompts)
}

type callToolArgs struct {
	ServerID  string          `json:"serverId"`
	ToolName  string          `json:"toolName"`
	Arguments json.RawMessage `json:"arguments"`
	// CallID, when set, lets mcp_cancel_tool cancel this call.
	CallID string `json:"callId,omitempty"`
}

var errToolCallCancelled = errors.New("MCP tool call was cancelled")

const (
	maxToolCallIDBytes   = 128
	toolCallPreCancelTTL = 30 * time.Second
)

// toolCallRegistry tracks cancellable tool calls. A cancel can reach the backend
// before its call registers, so early cancels are remembered for a short while.
type toolCallRegistry struct {
	mu        sync.Mutex
	running   map[string]context.CancelFunc
	cancelled map[string]time.Time
}

func newToolCallRegistry() *toolCallRegistry {
	return &toolCallRegistry{running: map[string]context.CancelFunc{}, cancelled: map[string]time.Time{}}
}

var toolCalls = newToolCallRegistry()

// start returns the call's context and a release func; an already-cancelled id
// yields a cancelled context.
func (r *toolCallRegistry) start(id string) (context.Context, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	if id == "" {
		return ctx, cancel
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked(time.Now())
	if _, early := r.cancelled[id]; early {
		delete(r.cancelled, id)
		cancel()
		return ctx, cancel
	}
	r.running[id] = cancel
	return ctx, func() {
		r.mu.Lock()
		delete(r.running, id)
		r.mu.Unlock()
		cancel()
	}
}

func (r *toolCallRegistry) cancel(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cancel, ok := r.running[id]; ok {
		delete(r.running, id)
		cancel()
		return
	}
	r.pruneLocked(time.Now())
	r.cancelled[id] = time.Now()
}

func (r *toolCallRegistry) pruneLocked(now time.Time) {
	for id, at := range r.cancelled {
		if now.Sub(at) > toolCallPreCancelTTL {
			delete(r.cancelled, id)
		}
	}
}

func validToolCallID(id string) bool {
	return len(id) <= maxToolCallIDBytes && !strings.ContainsFunc(id, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

func mcpCallToolHandler(_ string, argsJSON string) (string, error) {
	var a callToolArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	if !validToolCallID(a.CallID) {
		return "", fmt.Errorf("invalid MCP tool callId")
	}
	conn, err := gateway.session(a.ServerID)
	if err != nil {
		return "", err
	}
	ctx, release := toolCalls.start(a.CallID)
	defer release()
	res, err := conn.callTool(ctx, a.ToolName, a.Arguments)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(res)
}

type cancelToolArgs struct {
	CallID string `json:"callId"`
}

func mcpCancelToolHandler(_ string, argsJSON string) (string, error) {
	var a cancelToolArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	if a.CallID == "" || !validToolCallID(a.CallID) {
		return "", fmt.Errorf("invalid MCP tool callId")
	}
	toolCalls.cancel(a.CallID)
	return core.EncodeResult(true)
}

type readResourceArgs struct {
	ServerID string `json:"serverId"`
	URI      string `json:"uri"`
}

func mcpReadResourceHandler(_ string, argsJSON string) (string, error) {
	var a readResourceArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	conn, err := gateway.session(a.ServerID)
	if err != nil {
		return "", err
	}
	res, err := conn.readResource(a.URI)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(res)
}

type getPromptArgs struct {
	ServerID  string          `json:"serverId"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func mcpGetPromptHandler(_ string, argsJSON string) (string, error) {
	var a getPromptArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	conn, err := gateway.session(a.ServerID)
	if err != nil {
		return "", err
	}
	res, err := conn.getPrompt(a.Name, a.Arguments)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(res)
}

func mcpListStatusHandler(_ string, _ string) (string, error) {
	return core.EncodeResult(gateway.listStatus())
}

type readConfigArgs struct {
	Path string `json:"path"`
}

func mcpReadConfigFileHandler(_ string, argsJSON string) (string, error) {
	var a readConfigArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	if a.Path == "" {
		return "", fmt.Errorf("MCP 配置文件路径不能为空")
	}
	return "", fmt.Errorf("MCP 配置文件直接读取已禁用，请通过受控配置导入功能选择文件")
}
