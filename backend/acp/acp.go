// ACP 域：Agent Client Protocol 客户端网关（子进程 Agent，stdio JSON-RPC）。
// 对齐 mcp/mcp.go 的结构模式，使用 github.com/coder/acp-go-sdk 提供的协议实现。
// 纯 stdlib + acp-go-sdk 依赖。
//
// 命令契约（参数/返回均 camelCase JSON）：
//
//	acp_launch             {serverId, command, args:[], env:{}, cwd}         -> AcpInitResult
//	acp_launch_plugin      {serverId, pluginId, agentId, cwd}                -> AcpInitResult
//	acp_new_session        {serverId, cwd, mcpServers?:[]}                   -> AcpNewSessionResult
//	acp_load_session       {serverId, sessionId, cwd, mcpServers?:[]}        -> AcpLoadSessionResult
//	acp_prompt             {serverId, sessionId, text, content?:[]}         -> AcpPromptResult
//	acp_cancel             {serverId, sessionId}                             -> void
//	acp_respond_permission {requestId, optionId?, cancelled?}                -> void
//	acp_disconnect         {serverId}                                       -> void
//	acp_disconnect_all                                                       -> void
//	acp_list_status                                                          -> [AcpServerStatus]
package acp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mincode/app/backend/agentprogress"
	"mincode/app/backend/core"

	acpsdk "github.com/coder/acp-go-sdk"
)

var gateway = newAcpGateway()

// Shutdown 取消连接中的 Agent 并关闭全部已连接 Agent。
func Shutdown() { gateway.disconnectAll() }

func init() {
	core.Register("acp_launch", acpLaunchHandler)
	core.Register("acp_new_session", acpNewSessionHandler)
	core.Register("acp_load_session", acpLoadSessionHandler)
	core.Register("acp_prompt", acpPromptHandler)
	core.Register("acp_cancel", acpCancelHandler)
	core.Register("acp_respond_permission", acpRespondPermissionHandler)
	core.Register("acp_set_mode", acpSetModeHandler)
	core.Register("acp_set_config_option", acpSetConfigOptionHandler)
	core.Register("acp_disconnect", acpDisconnectHandler)
	core.Register("acp_disconnect_all", acpDisconnectAllHandler)
	core.Register("acp_list_status", acpListStatusHandler)
}

// ─── 数据类型（camelCase JSON）───────────────────────────────────────────

// AcpInitResult 是 acp_launch 的返回值，对应握手后 Agent 上报的能力信息。
type AcpInitResult struct {
	AgentCapabilities acpsdk.AgentCapabilities `json:"agentCapabilities"`
	AuthMethods       []acpsdk.AuthMethod      `json:"authMethods"`
	ProtocolVersion   int                      `json:"protocolVersion"`
}

// AcpNewSessionResult 是 acp_new_session 的返回值。
type AcpNewSessionResult struct {
	SessionID     string                       `json:"sessionId"`
	Modes         *acpsdk.SessionModeState     `json:"modes,omitempty"`
	ConfigOptions []acpsdk.SessionConfigOption `json:"configOptions,omitempty"`
}

// AcpLoadSessionResult 是 acp_load_session 的返回值。
type AcpLoadSessionResult struct {
	Modes         *acpsdk.SessionModeState     `json:"modes,omitempty"`
	ConfigOptions []acpsdk.SessionConfigOption `json:"configOptions,omitempty"`
}

// AcpPromptResult 是 acp_prompt 的返回值。
type AcpPromptResult struct {
	StopReason string `json:"stopReason"`
}

// AcpServerStatus 描述一个 ACP Agent 连接的当前状态。
type AcpServerStatus struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

// resolveCwd 将客户端传入的工作目录规范为存在的绝对目录。
// ACP 协议要求 NewSession/LoadSession 使用绝对路径；空值表示使用宿主进程目录。
func resolveCwd(cwd string) (string, error) {
	trimmed := strings.TrimSpace(cwd)
	if trimmed == "" {
		current, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve default cwd: %w", err)
		}
		trimmed = current
	}
	absolute, err := filepath.Abs(trimmed)
	if err != nil {
		return "", fmt.Errorf("resolve cwd %q: %w", cwd, err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("cwd %q is unavailable: %w", absolute, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("cwd %q is unavailable: %w", resolved, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("cwd %q is not a directory", resolved)
	}
	return resolved, nil
}

// ─── handler ─────────────────────────────────────────────────────────

type acpLaunchArgs struct {
	ServerID string            `json:"serverId"`
	Command  string            `json:"command"`
	Args     []string          `json:"args"`
	Env      map[string]string `json:"env"`
	Cwd      string            `json:"cwd"`
}

func acpLaunchHandler(_ string, argsJSON string) (string, error) {
	var a acpLaunchArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	if a.ServerID == "" {
		return "", fmt.Errorf("serverId is required")
	}
	if a.Command == "" {
		return "", fmt.Errorf("command is required")
	}
	cwd, err := resolveCwd(a.Cwd)
	if err != nil {
		return "", err
	}
	res, err := gateway.launch(context.Background(), a.ServerID, a.Command, a.Args, a.Env, cwd)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(res)
}

const AcpDisconnectedEvent = "acp:disconnected"

type AcpDisconnectedPayload struct {
	ServerIDs []string `json:"serverIds"`
}

type acpNewSessionArgs struct {
	ServerID   string             `json:"serverId"`
	Cwd        string             `json:"cwd"`
	McpServers []acpsdk.McpServer `json:"mcpServers"`
}

func acpNewSessionHandler(_ string, argsJSON string) (string, error) {
	var a acpNewSessionArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	mcpServers := a.McpServers
	if mcpServers == nil {
		mcpServers = []acpsdk.McpServer{}
	}
	cwd, err := resolveCwd(a.Cwd)
	if err != nil {
		return "", err
	}
	res, err := gateway.newSession(context.Background(), a.ServerID, cwd, mcpServers)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(res)
}

type acpLoadSessionArgs struct {
	ServerID   string             `json:"serverId"`
	SessionID  string             `json:"sessionId"`
	Cwd        string             `json:"cwd"`
	McpServers []acpsdk.McpServer `json:"mcpServers"`
}

func acpLoadSessionHandler(_ string, argsJSON string) (string, error) {
	var a acpLoadSessionArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	if a.SessionID == "" {
		return "", fmt.Errorf("sessionId is required")
	}
	mcpServers := a.McpServers
	if mcpServers == nil {
		mcpServers = []acpsdk.McpServer{}
	}
	cwd, err := resolveCwd(a.Cwd)
	if err != nil {
		return "", err
	}
	res, err := gateway.loadSession(context.Background(), a.ServerID, a.SessionID, cwd, mcpServers)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(res)
}

type acpPromptArgs struct {
	ServerID  string             `json:"serverId"`
	SessionID string             `json:"sessionId"`
	Text      string             `json:"text"`
	Content   []acpPromptContent `json:"content"`
}

type acpPromptContent struct {
	Type     string `json:"type"`
	Name     string `json:"name"`
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

func acpPromptHandler(_ string, argsJSON string) (string, error) {
	var a acpPromptArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	if a.SessionID == "" {
		return "", fmt.Errorf("sessionId is required")
	}
	agentprogress.UpdateActiveSession(a.SessionID, "acp", agentprogress.PhaseThinking, "")
	res, err := gateway.prompt(context.Background(), a.ServerID, a.SessionID, a.Text, a.Content)
	if err != nil {
		agentprogress.UpdateActiveSession(a.SessionID, "acp", agentprogress.PhaseFailed, "")
		return "", err
	}
	agentprogress.UpdateActiveSession(a.SessionID, "acp", agentprogress.PhaseCompleted, "")
	return core.EncodeResult(res)
}

type acpCancelArgs struct {
	ServerID  string `json:"serverId"`
	SessionID string `json:"sessionId"`
}

func acpCancelHandler(_ string, argsJSON string) (string, error) {
	var a acpCancelArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	if err := gateway.cancel(context.Background(), a.ServerID, a.SessionID); err != nil {
		return "", err
	}
	agentprogress.UpdateActiveSession(a.SessionID, "acp", agentprogress.PhaseCancelled, "")
	return core.EncodeResult(nil)
}

type acpRespondPermissionArgs struct {
	RequestID string  `json:"requestId"`
	ServerID  string  `json:"serverId"`
	SessionID string  `json:"sessionId"`
	OptionID  *string `json:"optionId,omitempty"`
	Cancelled bool    `json:"cancelled,omitempty"`
}

func acpRespondPermissionHandler(_ string, argsJSON string) (string, error) {
	var a acpRespondPermissionArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	if a.RequestID == "" {
		return "", fmt.Errorf("requestId is required")
	}
	if a.ServerID == "" || a.SessionID == "" {
		return "", fmt.Errorf("serverId and sessionId are required")
	}
	var outcome acpsdk.RequestPermissionOutcome
	if a.Cancelled || a.OptionID == nil {
		outcome = acpsdk.RequestPermissionOutcome{
			Cancelled: &acpsdk.RequestPermissionOutcomeCancelled{},
		}
	} else {
		outcome = acpsdk.RequestPermissionOutcome{
			Selected: &acpsdk.RequestPermissionOutcomeSelected{
				OptionId: acpsdk.PermissionOptionId(*a.OptionID),
			},
		}
	}
	if err := resolvePermission(a.RequestID, a.ServerID, a.SessionID, outcome); err != nil {
		return "", err
	}
	phase := agentprogress.PhaseThinking
	if a.Cancelled || a.OptionID == nil {
		phase = agentprogress.PhaseCancelled
	}
	agentprogress.UpdateActiveSession(a.SessionID, "acp", phase, "")
	return core.EncodeResult(nil)
}

type acpSetModeArgs struct {
	ServerID  string `json:"serverId"`
	SessionID string `json:"sessionId"`
	ModeID    string `json:"modeId"`
}

func acpSetModeHandler(_ string, argsJSON string) (string, error) {
	var a acpSetModeArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	if a.SessionID == "" {
		return "", fmt.Errorf("sessionId is required")
	}
	if a.ModeID == "" {
		return "", fmt.Errorf("modeId is required")
	}
	if err := gateway.setMode(context.Background(), a.ServerID, a.SessionID, a.ModeID); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

type acpSetConfigOptionArgs struct {
	ServerID  string `json:"serverId"`
	SessionID string `json:"sessionId"`
	ConfigID  string `json:"configId"`
	Value     string `json:"value"`
}

// AcpSetConfigOptionResult 返回 Agent 上报的最新配置项集合，供前端刷新当前值。
type AcpSetConfigOptionResult struct {
	ConfigOptions []acpsdk.SessionConfigOption `json:"configOptions,omitempty"`
}

func acpSetConfigOptionHandler(_ string, argsJSON string) (string, error) {
	var a acpSetConfigOptionArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	if a.SessionID == "" {
		return "", fmt.Errorf("sessionId is required")
	}
	if a.ConfigID == "" {
		return "", fmt.Errorf("configId is required")
	}
	options, err := gateway.setConfigOption(context.Background(), a.ServerID, a.SessionID, a.ConfigID, a.Value)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(AcpSetConfigOptionResult{ConfigOptions: options})
}

type acpDisconnectArgs struct {
	ServerID string `json:"serverId"`
}

func acpDisconnectHandler(_ string, argsJSON string) (string, error) {
	var a acpDisconnectArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	gateway.disconnect(a.ServerID)
	return core.EncodeResult(nil)
}

func acpDisconnectAllHandler(_ string, _ string) (string, error) {
	gateway.disconnectAll()
	return core.EncodeResult(nil)
}

func acpListStatusHandler(_ string, _ string) (string, error) {
	return core.EncodeResult(gateway.listStatus())
}
