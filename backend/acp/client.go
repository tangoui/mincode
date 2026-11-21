package acp

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mincode/app/backend/agentprogress"
	"mincode/app/backend/core"

	"github.com/google/uuid"

	acpsdk "github.com/coder/acp-go-sdk"
)

// chunkFlushInterval 文本 chunk 合批窗口：窗口内同一消息的相邻纯文本块合并为
// 一次事件推送，显著降低高频 token 流下的 IPC 与前端渲染压力；对肉眼流畅度无影响。
const (
	chunkFlushInterval          = 16 * time.Millisecond
	maxACPTextFileBytes   int64 = 4 * 1024 * 1024
	defaultTerminalOutput       = 1024 * 1024
	maxTerminalOutput           = 4 * 1024 * 1024
)

// sessionUpdatePayload 对齐前端 "acp:session-update" 事件负载。
type sessionUpdatePayload struct {
	ServerID  string `json:"serverId"`
	SessionID string `json:"sessionId"`
	// Replay marks history emitted while an Agent processes LoadSession. Clients that
	// already persist their transcript can ignore it instead of appending it twice.
	Replay bool                 `json:"replay,omitempty"`
	Update acpsdk.SessionUpdate `json:"update"`
}

// permissionRequestPayload 对齐前端 "acp:permission-request" 事件负载。
// 前端展示权限选项后，通过 acp_respond_permission 命令回传结果，
// 该结果经 resolvePermission 唤醒 bridgeClient.RequestPermission 的阻塞等待。
type permissionRequestPayload struct {
	RequestID string                    `json:"requestId"`
	ServerID  string                    `json:"serverId"`
	SessionID string                    `json:"sessionId"`
	Options   []acpsdk.PermissionOption `json:"options"`
	ToolCall  acpsdk.ToolCallUpdate     `json:"toolCall"`
}

// bridgeClient 实现 acpsdk.Client 接口，是 Agent 子进程通过 stdio 回调
// MinCode 侧（文件读写/权限确认/会话更新推送）的桥接层。
// 每个 ACP 连接持有独立的 bridgeClient 实例，serverID 用于事件路由。
type bridgeClient struct {
	serverID string
	termMu   sync.Mutex
	terms    map[string]*acpTerminal
	closed   bool
	rootMu   sync.RWMutex
	roots    map[string]int

	// 文本 chunk 合批状态（仅 pendMu 保护）。
	pendMu      sync.Mutex
	pendKind    string // "agent_message_chunk" / "agent_thought_chunk" / "user_message_chunk"
	pendSession string
	pendMsgID   *string
	pendChunk   *acpsdk.SessionUpdateAgentMessageChunk // 复用同一结构承载各 kind 的文本
	pendReplay  bool
	pendText    strings.Builder
	pendTimer   *time.Timer

	// replayMu protects the sessions currently replaying history for LoadSession.
	replayMu          sync.RWMutex
	replayingSessions map[string]int
}

type acpTerminal struct {
	cmd       *exec.Cmd
	mu        sync.Mutex
	output    []byte
	limit     int
	truncated bool
	exit      *acpsdk.TerminalExitStatus
	done      chan struct{}
	killOnce  sync.Once
}

type terminalWriter struct{ terminal *acpTerminal }

func (w terminalWriter) Write(p []byte) (int, error) {
	w.terminal.appendOutput(p)
	return len(p), nil
}

func (t *acpTerminal) appendOutput(p []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.output = append(t.output, p...)
	if t.limit > 0 && len(t.output) > t.limit {
		start := len(t.output) - t.limit
		for start < len(t.output) && start > 0 && (t.output[start]&0xc0) == 0x80 {
			start++
		}
		t.output = append([]byte(nil), t.output[start:]...)
		t.truncated = true
	}
}

func (t *acpTerminal) snapshot() (string, bool, *acpsdk.TerminalExitStatus) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var exit *acpsdk.TerminalExitStatus
	if t.exit != nil {
		copy := *t.exit
		exit = &copy
	}
	return string(t.output), t.truncated, exit
}

func (t *acpTerminal) setExit(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	status := acpsdk.TerminalExitStatus{}
	if exitErr, ok := err.(*exec.ExitError); ok {
		code := exitErr.ExitCode()
		status.ExitCode = &code
	} else if err == nil && t.cmd.ProcessState != nil {
		code := t.cmd.ProcessState.ExitCode()
		status.ExitCode = &code
	}
	t.exit = &status
}

var _ acpsdk.Client = (*bridgeClient)(nil)

func newBridgeClient(serverID string) *bridgeClient {
	return &bridgeClient{
		serverID:          serverID,
		terms:             make(map[string]*acpTerminal),
		roots:             make(map[string]int),
		replayingSessions: make(map[string]int),
	}
}

func (c *bridgeClient) addRoot(root string) func() {
	c.rootMu.Lock()
	c.roots[root]++
	c.rootMu.Unlock()
	return func() {
		c.rootMu.Lock()
		defer c.rootMu.Unlock()
		if c.roots[root] <= 1 {
			delete(c.roots, root)
			return
		}
		c.roots[root]--
	}
}

func (c *bridgeClient) allowedRoots() []string {
	c.rootMu.RLock()
	defer c.rootMu.RUnlock()
	roots := make([]string, 0, len(c.roots))
	for root := range c.roots {
		roots = append(roots, root)
	}
	return roots
}

func pathWithinRoot(path, root string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func (c *bridgeClient) resolveAllowedPath(path string, writing bool) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("path must be absolute: %s", path)
	}
	cleaned := filepath.Clean(path)
	for _, root := range c.allowedRoots() {
		if !pathWithinRoot(cleaned, root) {
			continue
		}
		relative, err := filepath.Rel(root, cleaned)
		if err != nil {
			continue
		}
		current := root
		parts := strings.Split(relative, string(filepath.Separator))
		for index, part := range parts {
			if part == "" || part == "." {
				continue
			}
			current = filepath.Join(current, part)
			info, statErr := os.Lstat(current)
			if os.IsNotExist(statErr) && writing {
				return cleaned, nil
			}
			if statErr != nil {
				return "", fmt.Errorf("inspect path %q: %w", current, statErr)
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return "", fmt.Errorf("path must not contain symbolic links: %s", path)
			}
			if index < len(parts)-1 && !info.IsDir() {
				return "", fmt.Errorf("path parent is not a directory: %s", current)
			}
		}
		return cleaned, nil
	}
	return "", fmt.Errorf("path is outside the active ACP workspace: %s", path)
}

// beginSessionReplay marks SessionUpdate notifications sent by LoadSession as
// history replay. The returned function must be called once the request ends.
func (c *bridgeClient) beginSessionReplay(sessionID string) func() {
	c.replayMu.Lock()
	c.replayingSessions[sessionID]++
	c.replayMu.Unlock()
	return func() {
		c.replayMu.Lock()
		defer c.replayMu.Unlock()
		if c.replayingSessions[sessionID] <= 1 {
			delete(c.replayingSessions, sessionID)
			return
		}
		c.replayingSessions[sessionID]--
	}
}

func (c *bridgeClient) isSessionReplay(sessionID string) bool {
	c.replayMu.RLock()
	defer c.replayMu.RUnlock()
	return c.replayingSessions[sessionID] > 0
}

// ReadTextFile 仅允许读取已建立会话的工作区内普通文本文件。
func (c *bridgeClient) ReadTextFile(ctx context.Context, p acpsdk.ReadTextFileRequest) (acpsdk.ReadTextFileResponse, error) {
	if err := ctx.Err(); err != nil {
		return acpsdk.ReadTextFileResponse{}, err
	}
	path, err := c.resolveAllowedPath(p.Path, false)
	if err != nil {
		return acpsdk.ReadTextFileResponse{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return acpsdk.ReadTextFileResponse{}, fmt.Errorf("inspect text file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return acpsdk.ReadTextFileResponse{}, fmt.Errorf("path is not a regular file: %s", path)
	}
	if info.Size() > maxACPTextFileBytes {
		return acpsdk.ReadTextFileResponse{}, fmt.Errorf("text file exceeds %d byte limit", maxACPTextFileBytes)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return acpsdk.ReadTextFileResponse{}, fmt.Errorf("read text file: %w", err)
	}
	content := string(b)
	if p.Line != nil || p.Limit != nil {
		lines := strings.Split(content, "\n")
		start := 0
		if p.Line != nil && *p.Line > 0 {
			start = *p.Line - 1
			if start > len(lines) {
				start = len(lines)
			}
		}
		end := len(lines)
		if p.Limit != nil && *p.Limit > 0 && start+*p.Limit < end {
			end = start + *p.Limit
		}
		content = strings.Join(lines[start:end], "\n")
	}
	return acpsdk.ReadTextFileResponse{Content: content}, nil
}

// WriteTextFile 仅允许原子写入已建立会话的工作区，拒绝符号链接和超大内容。
func (c *bridgeClient) WriteTextFile(ctx context.Context, p acpsdk.WriteTextFileRequest) (acpsdk.WriteTextFileResponse, error) {
	if err := ctx.Err(); err != nil {
		return acpsdk.WriteTextFileResponse{}, err
	}
	if int64(len(p.Content)) > maxACPTextFileBytes {
		return acpsdk.WriteTextFileResponse{}, fmt.Errorf("text content exceeds %d byte limit", maxACPTextFileBytes)
	}
	path, err := c.resolveAllowedPath(p.Path, true)
	if err != nil {
		return acpsdk.WriteTextFileResponse{}, err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return acpsdk.WriteTextFileResponse{}, fmt.Errorf("create text file directory: %w", err)
	}
	mode := os.FileMode(0o644)
	if info, statErr := os.Lstat(path); statErr == nil {
		if !info.Mode().IsRegular() {
			return acpsdk.WriteTextFileResponse{}, fmt.Errorf("path is not a regular file: %s", path)
		}
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(statErr) {
		return acpsdk.WriteTextFileResponse{}, fmt.Errorf("inspect text file: %w", statErr)
	}
	temp, err := os.CreateTemp(dir, ".mincode-acp-*.tmp")
	if err != nil {
		return acpsdk.WriteTextFileResponse{}, fmt.Errorf("create temporary text file: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return acpsdk.WriteTextFileResponse{}, fmt.Errorf("set temporary text file mode: %w", err)
	}
	if _, err := temp.WriteString(p.Content); err != nil {
		_ = temp.Close()
		return acpsdk.WriteTextFileResponse{}, fmt.Errorf("write temporary text file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return acpsdk.WriteTextFileResponse{}, fmt.Errorf("sync temporary text file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return acpsdk.WriteTextFileResponse{}, fmt.Errorf("close temporary text file: %w", err)
	}
	if err := replaceFile(tempPath, path); err != nil {
		return acpsdk.WriteTextFileResponse{}, fmt.Errorf("replace text file: %w", err)
	}
	return acpsdk.WriteTextFileResponse{}, nil
}

// RequestPermission 将权限请求经事件推送给前端，阻塞等待用户经
// acp_respond_permission 命令回传结果（或 ctx 被取消，如会话被取消）。
func (c *bridgeClient) RequestPermission(ctx context.Context, p acpsdk.RequestPermissionRequest) (acpsdk.RequestPermissionResponse, error) {
	if len(p.Options) == 0 {
		return acpsdk.RequestPermissionResponse{
			Outcome: acpsdk.RequestPermissionOutcome{
				Cancelled: &acpsdk.RequestPermissionOutcomeCancelled{},
			},
		}, nil
	}
	requestID := uuid.NewString()
	optionIDs := make(map[string]struct{}, len(p.Options))
	for _, option := range p.Options {
		optionIDs[string(option.OptionId)] = struct{}{}
	}
	outcomeCh := permissions.register(permissionRequest{
		requestID: requestID,
		serverID:  c.serverID,
		sessionID: string(p.SessionId),
		optionIDs: optionIDs,
	})
	agentprogress.UpdateActiveSession(string(p.SessionId), "acp", agentprogress.PhaseWaitingApproval, "")
	emitAcpEvent("acp:permission-request", permissionRequestPayload{
		RequestID: requestID,
		ServerID:  c.serverID,
		SessionID: string(p.SessionId),
		Options:   p.Options,
		ToolCall:  p.ToolCall,
	})
	outcome, err := waitForPermission(ctx, requestID, outcomeCh)
	if err != nil {
		return acpsdk.RequestPermissionResponse{
			Outcome: acpsdk.RequestPermissionOutcome{
				Cancelled: &acpsdk.RequestPermissionOutcomeCancelled{},
			},
		}, nil
	}
	return acpsdk.RequestPermissionResponse{Outcome: outcome}, nil
}

// SessionUpdate 将 Agent 推送的会话更新（消息流、工具调用状态等）转发给前端。
// 纯文本的消息/思考 chunk 进入短窗口合批；其余更新（工具调用、plan 等）
// 先 flush 缓冲再即时推送，保证事件顺序不变。
func (c *bridgeClient) SessionUpdate(_ context.Context, n acpsdk.SessionNotification) error {
	sessionID := string(n.SessionId)
	replay := c.isSessionReplay(sessionID)
	if kind, chunk := textChunkOf(n.Update); chunk != nil {
		if !replay {
			phase := agentprogress.PhaseThinking
			if kind == "agent_message_chunk" {
				phase = agentprogress.PhaseStreaming
			}
			agentprogress.UpdateActiveSession(sessionID, "acp", phase, "")
		}
		c.bufferTextChunk(kind, sessionID, chunk, replay)
		return nil
	}
	c.flushPendingChunk()
	if !replay && (n.Update.ToolCall != nil || n.Update.ToolCallUpdate != nil) {
		agentprogress.UpdateActiveSession(sessionID, "acp", agentprogress.PhaseToolRunning, "")
	}
	emitAcpEvent("acp:session-update", sessionUpdatePayload{
		ServerID:  c.serverID,
		SessionID: sessionID,
		Replay:    replay,
		Update:    n.Update,
	})
	return nil
}

// textChunkOf 提取纯文本的流式 chunk；非文本内容（图片/资源等）返回 nil 走直发。
func textChunkOf(u acpsdk.SessionUpdate) (string, *acpsdk.SessionUpdateAgentMessageChunk) {
	switch {
	case u.AgentMessageChunk != nil && u.AgentMessageChunk.Content.Text != nil:
		return "agent_message_chunk", u.AgentMessageChunk
	case u.AgentThoughtChunk != nil && u.AgentThoughtChunk.Content.Text != nil:
		t := u.AgentThoughtChunk
		return "agent_thought_chunk", &acpsdk.SessionUpdateAgentMessageChunk{
			Meta: t.Meta, Content: t.Content, MessageId: t.MessageId, SessionUpdate: t.SessionUpdate,
		}
	case u.UserMessageChunk != nil && u.UserMessageChunk.Content.Text != nil:
		t := u.UserMessageChunk
		return "user_message_chunk", &acpsdk.SessionUpdateAgentMessageChunk{
			Meta: t.Meta, Content: t.Content, MessageId: t.MessageId, SessionUpdate: t.SessionUpdate,
		}
	}
	return "", nil
}

func sameMsgID(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// bufferTextChunk 将文本 chunk 写入缓冲；同一 (kind, session, messageId) 的文本
// 追加合并，否则先 flush 旧缓冲再开新缓冲，并启动定时 flush。
func (c *bridgeClient) bufferTextChunk(kind, sessionID string, chunk *acpsdk.SessionUpdateAgentMessageChunk, replay bool) {
	c.pendMu.Lock()
	if c.pendChunk != nil &&
		(c.pendKind != kind || c.pendSession != sessionID || c.pendReplay != replay || !sameMsgID(c.pendMsgID, chunk.MessageId)) {
		c.flushPendingChunkLocked()
	}
	if c.pendChunk == nil {
		c.pendKind = kind
		c.pendSession = sessionID
		c.pendMsgID = chunk.MessageId
		c.pendReplay = replay
		c.pendChunk = chunk
		c.pendText.Reset()
	}
	c.pendText.WriteString(chunk.Content.Text.Text)
	if c.pendTimer == nil {
		c.pendTimer = time.AfterFunc(chunkFlushInterval, c.flushPendingChunk)
	}
	c.pendMu.Unlock()
}

func (c *bridgeClient) flushPendingChunk() {
	c.pendMu.Lock()
	c.flushPendingChunkLocked()
	c.pendMu.Unlock()
}

// flushPendingChunkLocked 把缓冲合并为单条 chunk 事件推送。调用方需持有 pendMu。
func (c *bridgeClient) flushPendingChunkLocked() {
	if c.pendTimer != nil {
		c.pendTimer.Stop()
		c.pendTimer = nil
	}
	if c.pendChunk == nil {
		return
	}
	text := c.pendText.String()
	textBlock := *c.pendChunk.Content.Text
	textBlock.Text = text
	content := acpsdk.ContentBlock{Text: &textBlock}
	var update acpsdk.SessionUpdate
	switch c.pendKind {
	case "agent_message_chunk":
		update.AgentMessageChunk = &acpsdk.SessionUpdateAgentMessageChunk{
			Meta: c.pendChunk.Meta, Content: content, MessageId: c.pendMsgID, SessionUpdate: c.pendKind,
		}
	case "agent_thought_chunk":
		update.AgentThoughtChunk = &acpsdk.SessionUpdateAgentThoughtChunk{
			Meta: c.pendChunk.Meta, Content: content, MessageId: c.pendMsgID, SessionUpdate: c.pendKind,
		}
	case "user_message_chunk":
		update.UserMessageChunk = &acpsdk.SessionUpdateUserMessageChunk{
			Meta: c.pendChunk.Meta, Content: content, MessageId: c.pendMsgID, SessionUpdate: c.pendKind,
		}
	default:
		c.pendChunk = nil
		return
	}
	sessionID := c.pendSession
	replay := c.pendReplay
	c.pendChunk = nil
	c.pendReplay = false
	c.pendText.Reset()
	emitAcpEvent("acp:session-update", sessionUpdatePayload{
		ServerID:  c.serverID,
		SessionID: sessionID,
		Replay:    replay,
		Update:    update,
	})
}

func (c *bridgeClient) CreateTerminal(ctx context.Context, p acpsdk.CreateTerminalRequest) (acpsdk.CreateTerminalResponse, error) {
	if err := ctx.Err(); err != nil {
		return acpsdk.CreateTerminalResponse{}, err
	}
	if strings.TrimSpace(p.Command) == "" {
		return acpsdk.CreateTerminalResponse{}, fmt.Errorf("terminal command cannot be empty")
	}
	cmd := exec.Command(p.Command, p.Args...)
	if p.Cwd != nil {
		cwd, err := c.resolveAllowedPath(*p.Cwd, false)
		if err != nil {
			return acpsdk.CreateTerminalResponse{}, fmt.Errorf("validate terminal cwd: %w", err)
		}
		info, err := os.Stat(cwd)
		if err != nil || !info.IsDir() {
			return acpsdk.CreateTerminalResponse{}, fmt.Errorf("terminal cwd is not an available directory: %s", cwd)
		}
		cmd.Dir = cwd
	} else {
		roots := c.allowedRoots()
		if len(roots) == 0 {
			return acpsdk.CreateTerminalResponse{}, fmt.Errorf("terminal requires an active ACP workspace")
		}
		cmd.Dir = roots[0]
	}
	cmd.Env = os.Environ()
	for _, item := range p.Env {
		cmd.Env = append(cmd.Env, item.Name+"="+item.Value)
	}
	limit := defaultTerminalOutput
	if p.OutputByteLimit != nil && *p.OutputByteLimit > 0 {
		limit = min(*p.OutputByteLimit, maxTerminalOutput)
	}
	core.PrepareProcessTree(cmd)
	terminal := &acpTerminal{cmd: cmd, limit: limit, done: make(chan struct{})}
	writer := terminalWriter{terminal: terminal}
	cmd.Stdout = writer
	cmd.Stderr = writer
	if err := cmd.Start(); err != nil {
		return acpsdk.CreateTerminalResponse{}, fmt.Errorf("start terminal command: %w", err)
	}
	id := uuid.NewString()
	c.termMu.Lock()
	if c.closed {
		c.termMu.Unlock()
		terminal.kill()
		_ = cmd.Wait()
		return acpsdk.CreateTerminalResponse{}, fmt.Errorf("ACP connection is closed")
	}
	c.terms[id] = terminal
	c.termMu.Unlock()
	go func() {
		terminal.setExit(cmd.Wait())
		close(terminal.done)
	}()
	return acpsdk.CreateTerminalResponse{TerminalId: id}, nil
}

func (c *bridgeClient) terminal(id string) (*acpTerminal, error) {
	c.termMu.Lock()
	defer c.termMu.Unlock()
	terminal := c.terms[id]
	if terminal == nil {
		return nil, fmt.Errorf("terminal not found: %s", id)
	}
	return terminal, nil
}

func (c *bridgeClient) KillTerminal(_ context.Context, p acpsdk.KillTerminalRequest) (acpsdk.KillTerminalResponse, error) {
	terminal, err := c.terminal(p.TerminalId)
	if err != nil {
		return acpsdk.KillTerminalResponse{}, err
	}
	terminal.kill()
	return acpsdk.KillTerminalResponse{}, nil
}

func (c *bridgeClient) TerminalOutput(_ context.Context, p acpsdk.TerminalOutputRequest) (acpsdk.TerminalOutputResponse, error) {
	terminal, err := c.terminal(p.TerminalId)
	if err != nil {
		return acpsdk.TerminalOutputResponse{}, err
	}
	output, truncated, exit := terminal.snapshot()
	return acpsdk.TerminalOutputResponse{Output: output, Truncated: truncated, ExitStatus: exit}, nil
}

func (c *bridgeClient) ReleaseTerminal(_ context.Context, p acpsdk.ReleaseTerminalRequest) (acpsdk.ReleaseTerminalResponse, error) {
	c.termMu.Lock()
	terminal := c.terms[p.TerminalId]
	delete(c.terms, p.TerminalId)
	c.termMu.Unlock()
	if terminal == nil {
		return acpsdk.ReleaseTerminalResponse{}, fmt.Errorf("terminal not found: %s", p.TerminalId)
	}
	select {
	case <-terminal.done:
	default:
		terminal.kill()
	}
	return acpsdk.ReleaseTerminalResponse{}, nil
}

func (t *acpTerminal) kill() {
	t.killOnce.Do(func() {
		_ = core.KillProcessTree(t.cmd)
	})
}

func (c *bridgeClient) close() {
	c.flushPendingChunk()
	c.termMu.Lock()
	c.closed = true
	terminals := make([]*acpTerminal, 0, len(c.terms))
	for id, terminal := range c.terms {
		terminals = append(terminals, terminal)
		delete(c.terms, id)
	}
	c.termMu.Unlock()
	for _, terminal := range terminals {
		terminal.kill()
	}
}

func (c *bridgeClient) WaitForTerminalExit(ctx context.Context, p acpsdk.WaitForTerminalExitRequest) (acpsdk.WaitForTerminalExitResponse, error) {
	terminal, err := c.terminal(p.TerminalId)
	if err != nil {
		return acpsdk.WaitForTerminalExitResponse{}, err
	}
	select {
	case <-ctx.Done():
		return acpsdk.WaitForTerminalExitResponse{}, ctx.Err()
	case <-terminal.done:
		_, _, exit := terminal.snapshot()
		if exit == nil {
			return acpsdk.WaitForTerminalExitResponse{}, io.ErrUnexpectedEOF
		}
		return acpsdk.WaitForTerminalExitResponse{ExitCode: exit.ExitCode, Signal: exit.Signal}, nil
	}
}
