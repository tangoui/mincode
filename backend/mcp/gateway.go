// MCP 连接池：serverId -> 连接。带 revision 机制以取消进行中的重连。
// 对齐 mcp/gateway.rs + mcp/connection.rs。
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// transport 是 stdio / http 连接的公共接口。
type transport interface {
	callTool(ctx context.Context, toolName string, arguments json.RawMessage) (*McpCallResult, error)
	readResource(uri string) (*McpResourceContent, error)
	getPrompt(name string, arguments json.RawMessage) (*McpPromptResult, error)
	close()
}

// mcpConnection 是一个已建立的 MCP 会话（含初始能力快照）。
type mcpConnection struct {
	tools     []McpToolDef
	resources []McpResourceDef
	prompts   []McpPromptDef
	transport
	// plugin is set for sessions started from a plugin's contributes.mcpServers.
	plugin *pluginSessionInfo
}

func (c *mcpConnection) connectResult() *McpConnectResult {
	return &McpConnectResult{
		Tools:     c.tools,
		Resources: c.resources,
		Prompts:   c.prompts,
	}
}

type mcpGateway struct {
	mu         sync.Mutex
	sessions   map[string]*mcpConnection
	revisions  map[string]uint64
	connecting map[string]context.CancelFunc
}

func newMcpGateway() *mcpGateway {
	return &mcpGateway{
		sessions:   map[string]*mcpConnection{},
		revisions:  map[string]uint64{},
		connecting: map[string]context.CancelFunc{},
	}
}

// beginConnect 递增 revision 并移除旧会话，返回本次连接的 revision。
func (g *mcpGateway) beginConnect(serverID string) (uint64, context.Context, *mcpConnection) {
	g.mu.Lock()
	defer g.mu.Unlock()
	rev := g.revisions[serverID] + 1
	g.revisions[serverID] = rev
	if cancel := g.connecting[serverID]; cancel != nil {
		cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	g.connecting[serverID] = cancel
	old := g.sessions[serverID]
	delete(g.sessions, serverID)
	return rev, ctx, old
}

// finishConnect 仅在 revision 仍匹配时安装连接，否则视为已被取消并关闭。
func (g *mcpGateway) finishConnect(serverID string, rev uint64, conn *mcpConnection) error {
	g.mu.Lock()
	if g.revisions[serverID] != rev {
		g.mu.Unlock()
		conn.close()
		return fmt.Errorf("MCP server '%s' connection was cancelled", serverID)
	}
	delete(g.connecting, serverID)
	g.sessions[serverID] = conn
	g.mu.Unlock()
	return nil
}

func (g *mcpGateway) failConnect(serverID string, rev uint64) {
	g.mu.Lock()
	if g.revisions[serverID] == rev {
		if cancel := g.connecting[serverID]; cancel != nil {
			cancel()
		}
		delete(g.connecting, serverID)
	}
	g.mu.Unlock()
}

func (g *mcpGateway) connectStdio(serverID, command string, args []string, env map[string]string) (*McpConnectResult, error) {
	rev, ctx, old := g.beginConnect(serverID)
	if old != nil {
		old.close()
	}
	conn, err := connectStdio(ctx, serverID, command, args, env)
	if err != nil {
		g.failConnect(serverID, rev)
		return nil, err
	}
	result := conn.connectResult()
	if err := g.finishConnect(serverID, rev, conn); err != nil {
		return nil, err
	}
	return result, nil
}

func (g *mcpGateway) connectHTTP(serverID, url string, headers map[string]string, auth string) (*McpConnectResult, error) {
	rev, ctx, old := g.beginConnect(serverID)
	if old != nil {
		old.close()
	}
	conn, err := connectHTTP(ctx, serverID, url, headers, auth)
	if err != nil {
		g.failConnect(serverID, rev)
		return nil, err
	}
	result := conn.connectResult()
	if err := g.finishConnect(serverID, rev, conn); err != nil {
		return nil, err
	}
	return result, nil
}

func (g *mcpGateway) disconnect(serverID string) {
	g.mu.Lock()
	g.revisions[serverID] = g.revisions[serverID] + 1
	if cancel := g.connecting[serverID]; cancel != nil {
		cancel()
		delete(g.connecting, serverID)
	}
	conn := g.sessions[serverID]
	delete(g.sessions, serverID)
	g.mu.Unlock()
	if conn != nil {
		conn.close()
	}
}

func (g *mcpGateway) disconnectAll() {
	g.mu.Lock()
	for id := range g.revisions {
		g.revisions[id] = g.revisions[id] + 1
	}
	for id, cancel := range g.connecting {
		cancel()
		delete(g.connecting, id)
	}
	connections := make([]*mcpConnection, 0, len(g.sessions))
	for id, conn := range g.sessions {
		connections = append(connections, conn)
		delete(g.sessions, id)
	}
	g.mu.Unlock()
	for _, conn := range connections {
		conn.close()
	}
}

func (g *mcpGateway) session(serverID string) (*mcpConnection, error) {
	g.mu.Lock()
	conn, ok := g.sessions[serverID]
	g.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("MCP server '%s' is not connected", serverID)
	}
	// A plugin session whose owner no longer offers this exact launch
	// (disabled, uninstalled, upgraded, changed entry) is stopped on use.
	if conn.plugin != nil && conn.plugin.valid != nil && !conn.plugin.valid() {
		g.disconnectIf(serverID, conn)
		return nil, fmt.Errorf("MCP server '%s' is no longer available", serverID)
	}
	return conn, nil
}

// disconnectIf closes serverID only while it still holds conn.
func (g *mcpGateway) disconnectIf(serverID string, conn *mcpConnection) bool {
	g.mu.Lock()
	if g.sessions[serverID] != conn {
		g.mu.Unlock()
		return false
	}
	g.revisions[serverID] = g.revisions[serverID] + 1
	delete(g.sessions, serverID)
	g.mu.Unlock()
	conn.close()
	return true
}

func (g *mcpGateway) listStatus() []McpServerStatus {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]McpServerStatus, 0, len(g.sessions))
	for id, conn := range g.sessions {
		out = append(out, McpServerStatus{
			ID:            id,
			State:         "connected",
			ToolCount:     len(conn.tools),
			ResourceCount: len(conn.resources),
			PromptCount:   len(conn.prompts),
			Error:         nil,
		})
	}
	return out
}
