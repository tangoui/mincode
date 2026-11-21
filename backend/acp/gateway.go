package acp

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
	"sync"

	"mincode/app/backend/core"

	acpsdk "github.com/coder/acp-go-sdk"
)

// acpConnection 是一个已建立的 ACP Agent 连接（子进程 + JSON-RPC 会话）。
type acpConnection struct {
	cmd      *exec.Cmd
	csc      *acpsdk.ClientSideConnection
	client   *bridgeClient
	cancel   context.CancelFunc
	command  string
	serverID string
	stderr   *core.CappedWriter

	agentCapabilities acpsdk.AgentCapabilities
	authMethods       []acpsdk.AuthMethod
	protocolVersion   int
	closeOnce         sync.Once
}

func (c *acpConnection) requestError(operation string, cause error) error {
	if c.stderr != nil {
		data, truncated := c.stderr.Bytes()
		if detail := strings.TrimSpace(string(data)); detail != "" {
			if truncated {
				detail += "\n[stderr truncated]"
			}
			return fmt.Errorf("ACP Agent %q %s失败: %w; stderr: %s", c.command, operation, cause, detail)
		}
	}
	return fmt.Errorf("ACP Agent %q %s失败: %w", c.command, operation, cause)
}

func (c *acpConnection) close() {
	c.closeOnce.Do(func() {
		if c.cancel != nil {
			c.cancel()
		}
		if c.client != nil {
			c.client.close()
		}
		if c.cmd != nil && c.cmd.Process != nil {
			_ = core.KillProcessTree(c.cmd)
			_ = c.cmd.Wait()
		}
	})
}

type acpGateway struct {
	mu         sync.Mutex
	sessions   map[string]*acpConnection
	revisions  map[string]uint64
	connecting map[string]context.CancelFunc
}

func newAcpGateway() *acpGateway {
	return &acpGateway{
		sessions:   map[string]*acpConnection{},
		revisions:  map[string]uint64{},
		connecting: map[string]context.CancelFunc{},
	}
}

func (g *acpGateway) beginConnect(parent context.Context, serverID string) (uint64, context.Context, *acpConnection) {
	g.mu.Lock()
	defer g.mu.Unlock()
	rev := g.revisions[serverID] + 1
	g.revisions[serverID] = rev
	if cancel := g.connecting[serverID]; cancel != nil {
		cancel()
	}
	connectCtx, cancel := context.WithCancel(parent)
	g.connecting[serverID] = cancel
	old := g.sessions[serverID]
	delete(g.sessions, serverID)
	return rev, connectCtx, old
}

func (g *acpGateway) finishConnect(serverID string, rev uint64, conn *acpConnection) error {
	g.mu.Lock()
	if g.revisions[serverID] != rev {
		g.mu.Unlock()
		conn.close()
		return fmt.Errorf("ACP server '%s' connection was cancelled", serverID)
	}
	delete(g.connecting, serverID)
	g.sessions[serverID] = conn
	g.mu.Unlock()
	if conn.csc != nil {
		go g.watchTransport(serverID, conn)
	}
	return nil
}

func (g *acpGateway) watchTransport(serverID string, conn *acpConnection) {
	<-conn.csc.Done()
	g.mu.Lock()
	if g.sessions[serverID] != conn {
		g.mu.Unlock()
		return
	}
	// Revoke this connection's resources before another launch can reuse the server ID.
	conn.close()
	delete(g.sessions, serverID)
	g.mu.Unlock()
	core.Emit(AcpDisconnectedEvent, AcpDisconnectedPayload{ServerIDs: []string{serverID}})
}

func (g *acpGateway) failConnect(serverID string, rev uint64) {
	g.mu.Lock()
	if g.revisions[serverID] == rev {
		if cancel := g.connecting[serverID]; cancel != nil {
			cancel()
		}
		delete(g.connecting, serverID)
	}
	g.mu.Unlock()
}

func (g *acpGateway) disconnect(serverID string) {
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

func (g *acpGateway) disconnectAll() {
	g.mu.Lock()
	for id := range g.revisions {
		g.revisions[id] = g.revisions[id] + 1
	}
	for id, cancel := range g.connecting {
		cancel()
		delete(g.connecting, id)
	}
	connections := make([]*acpConnection, 0, len(g.sessions))
	for id, conn := range g.sessions {
		connections = append(connections, conn)
		delete(g.sessions, id)
	}
	g.mu.Unlock()
	for _, conn := range connections {
		conn.close()
	}
}

func (g *acpGateway) session(serverID string) (*acpConnection, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	conn, ok := g.sessions[serverID]
	if !ok {
		return nil, fmt.Errorf("ACP server '%s' is not connected", serverID)
	}
	return conn, nil
}

func (g *acpGateway) listStatus() []AcpServerStatus {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]AcpServerStatus, 0, len(g.sessions))
	for id := range g.sessions {
		out = append(out, AcpServerStatus{ID: id, State: "connected"})
	}
	return out
}

// launch 启动 Agent 子进程并完成协议初始化握手。
func (g *acpGateway) launch(ctx context.Context, serverID, command string, args []string, env map[string]string, cwd string) (*AcpInitResult, error) {
	rev, connectCtx, old := g.beginConnect(ctx, serverID)
	if old != nil {
		old.close()
	}
	if err := connectCtx.Err(); err != nil {
		g.failConnect(serverID, rev)
		return nil, fmt.Errorf("ACP server '%s' connection was cancelled", serverID)
	}
	conn, err := launchAgent(connectCtx, serverID, command, args, env, cwd)
	if err != nil {
		g.failConnect(serverID, rev)
		return nil, err
	}
	if err := g.finishConnect(serverID, rev, conn); err != nil {
		return nil, err
	}
	return &AcpInitResult{
		AgentCapabilities: conn.agentCapabilities,
		AuthMethods:       conn.authMethods,
		ProtocolVersion:   conn.protocolVersion,
	}, nil
}

func (g *acpGateway) newSession(ctx context.Context, serverID, cwd string, mcpServers []acpsdk.McpServer) (*AcpNewSessionResult, error) {
	conn, err := g.session(serverID)
	if err != nil {
		return nil, err
	}
	releaseRoot := conn.client.addRoot(cwd)
	res, err := conn.csc.NewSession(ctx, acpsdk.NewSessionRequest{
		Cwd:        cwd,
		McpServers: mcpServers,
	})
	if err != nil {
		releaseRoot()
		return nil, conn.requestError("创建会话", err)
	}
	sessionID := string(res.SessionId)
	return &AcpNewSessionResult{
		SessionID:     sessionID,
		Modes:         res.Modes,
		ConfigOptions: res.ConfigOptions,
	}, nil
}

func (g *acpGateway) loadSession(ctx context.Context, serverID, sessionID, cwd string, mcpServers []acpsdk.McpServer) (*AcpLoadSessionResult, error) {
	conn, err := g.session(serverID)
	if err != nil {
		return nil, err
	}
	endReplay := conn.client.beginSessionReplay(sessionID)
	defer endReplay()
	releaseRoot := conn.client.addRoot(cwd)
	res, err := conn.csc.LoadSession(ctx, acpsdk.LoadSessionRequest{
		SessionId:  acpsdk.SessionId(sessionID),
		Cwd:        cwd,
		McpServers: mcpServers,
	})
	if err != nil {
		releaseRoot()
		return nil, conn.requestError("恢复会话", err)
	}
	return &AcpLoadSessionResult{
		Modes:         res.Modes,
		ConfigOptions: res.ConfigOptions,
	}, nil
}

func (g *acpGateway) prompt(ctx context.Context, serverID, sessionID, text string, content []acpPromptContent) (*AcpPromptResult, error) {
	conn, err := g.session(serverID)
	if err != nil {
		return nil, err
	}
	prompt := make([]acpsdk.ContentBlock, 0, len(content)+1)
	if text != "" {
		prompt = append(prompt, acpsdk.TextBlock(text))
	}
	for _, item := range content {
		switch item.Type {
		case "image":
			if !conn.agentCapabilities.PromptCapabilities.Image {
				return nil, fmt.Errorf("当前本地 Agent 不支持图片附件")
			}
			if item.Data == "" {
				return nil, fmt.Errorf("图片附件为空：%s", item.Name)
			}
			prompt = append(prompt, acpsdk.ImageBlock(item.Data, item.MimeType))
		case "resource":
			if conn.agentCapabilities.PromptCapabilities.EmbeddedContext {
				mimeType := item.MimeType
				prompt = append(prompt, acpsdk.ResourceBlock(acpsdk.EmbeddedResourceResource{
					TextResourceContents: &acpsdk.TextResourceContents{
						Uri:      "mincode://attachment/" + url.PathEscape(item.Name),
						MimeType: &mimeType,
						Text:     item.Data,
					},
				}))
			} else {
				prompt = append(prompt, acpsdk.TextBlock(fmt.Sprintf("附件 %s：\n%s", item.Name, item.Data)))
			}
		}
	}
	if len(prompt) == 0 {
		return nil, fmt.Errorf("prompt 或附件不能为空")
	}
	res, err := conn.csc.Prompt(ctx, acpsdk.PromptRequest{
		SessionId: acpsdk.SessionId(sessionID),
		Prompt:    prompt,
	})
	if err != nil {
		return nil, conn.requestError("处理请求", err)
	}
	return &AcpPromptResult{StopReason: string(res.StopReason)}, nil
}

func (g *acpGateway) cancel(ctx context.Context, serverID, sessionID string) error {
	conn, err := g.session(serverID)
	if err != nil {
		return err
	}
	return conn.csc.Cancel(ctx, acpsdk.CancelNotification{
		SessionId: acpsdk.SessionId(sessionID),
	})
}

func (g *acpGateway) setMode(ctx context.Context, serverID, sessionID, modeID string) error {
	conn, err := g.session(serverID)
	if err != nil {
		return err
	}
	_, err = conn.csc.SetSessionMode(ctx, acpsdk.SetSessionModeRequest{
		SessionId: acpsdk.SessionId(sessionID),
		ModeId:    acpsdk.SessionModeId(modeID),
	})
	return err
}

func (g *acpGateway) setConfigOption(ctx context.Context, serverID, sessionID, configID, value string) ([]acpsdk.SessionConfigOption, error) {
	conn, err := g.session(serverID)
	if err != nil {
		return nil, err
	}
	res, err := conn.csc.SetSessionConfigOption(ctx, acpsdk.SetSessionConfigOptionRequest{
		ValueId: &acpsdk.SetSessionConfigOptionValueId{
			SessionId: acpsdk.SessionId(sessionID),
			ConfigId:  acpsdk.SessionConfigId(configID),
			Value:     acpsdk.SessionConfigValueId(value),
		},
	})
	if err != nil {
		return nil, conn.requestError("设置会话配置", err)
	}
	return res.ConfigOptions, nil
}
