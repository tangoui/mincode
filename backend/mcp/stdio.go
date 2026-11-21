// MCP stdio 传输：子进程 stdin/stdout 上的 line-delimited JSON-RPC 2.0。
// 对齐 mcp/stdio.rs。
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mincode/app/backend/core"
)

type stdioConn struct {
	serverID string

	cmd    *exec.Cmd
	stdin  interface{ Write(p []byte) (int, error) }
	stderr *limitedBuffer
	writeM sync.Mutex

	nextID  atomic.Uint64
	pendM   sync.Mutex
	pending map[uint64]chan rpcReply
	closed  atomic.Bool
}

type limitedBuffer struct {
	mu    sync.Mutex
	limit int
	data  []byte
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if len(b.data) > b.limit {
		b.data = append([]byte(nil), b.data[len(b.data)-b.limit:]...)
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(string(b.data))
}

type rpcReply struct {
	result json.RawMessage
	err    error
}

var errStdioConnClosed = fmt.Errorf("MCP stdio connection closed")

// connectStdio 启动子进程、完成 initialize 握手并抓取 tools/resources/prompts。
func connectStdio(ctx context.Context, serverID, command string, args []string, env map[string]string) (*mcpConnection, error) {
	processEnv, err := buildStdioProcessEnv(env)
	if err != nil {
		return nil, err
	}
	resolvedCommand := resolveStdioCommand(command, processEnv)
	cmd := exec.Command(resolvedCommand, args...)
	cmd.Env = processEnv
	return startStdio(ctx, serverID, command, cmd)
}

// startStdio starts a prepared command and runs the MCP handshake.
func startStdio(ctx context.Context, serverID, label string, cmd *exec.Cmd) (*mcpConnection, error) {
	core.PrepareProcessTree(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("MCP stdin unavailable: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("MCP stdout unavailable: %w", err)
	}
	stderr := &limitedBuffer{limit: 64 * 1024}
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("Failed to spawn MCP server '%s': %w", label, err)
	}

	c := &stdioConn{
		serverID: serverID,
		cmd:      cmd,
		stdin:    stdin,
		stderr:   stderr,
		pending:  map[uint64]chan rpcReply{},
	}
	context.AfterFunc(ctx, c.close)
	go c.readStdout(stdout)

	if err := c.initialize(); err != nil {
		c.close()
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

func (c *stdioConn) readStdout(r interface{ Read(p []byte) (int, error) }) {
	defer c.close()
	scanner := bufio.NewScanner(readerFunc(r.Read))
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var msg struct {
			ID     *uint64         `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		if msg.ID == nil {
			continue
		}
		c.pendM.Lock()
		ch, ok := c.pending[*msg.ID]
		if ok {
			delete(c.pending, *msg.ID)
		}
		c.pendM.Unlock()
		if !ok {
			continue
		}
		if msg.Error != nil {
			errMsg := msg.Error.Message
			if errMsg == "" {
				errMsg = "MCP error"
			}
			ch <- rpcReply{err: fmt.Errorf("%s", errMsg)}
		} else {
			ch <- rpcReply{result: msg.Result}
		}
	}
	_ = scanner.Err()
}

func (c *stdioConn) request(method string, params any) (json.RawMessage, error) {
	return c.requestCtx(context.Background(), method, params)
}

// requestCtx waits for the reply until ctx is cancelled; a cancelled request is
// dropped and the server is told with notifications/cancelled.
func (c *stdioConn) requestCtx(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	ch := make(chan rpcReply, 1)
	c.pendM.Lock()
	if c.closed.Load() {
		c.pendM.Unlock()
		return nil, errStdioConnClosed
	}
	c.pending[id] = ch
	c.pendM.Unlock()

	body := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	if err := c.writeMessage(body); err != nil {
		c.removePending(id, ch)
		return nil, err
	}

	timer := time.NewTimer(defaultCallTimeoutSecs * time.Second)
	defer timer.Stop()
	select {
	case reply := <-ch:
		if reply.err != nil {
			return nil, reply.err
		}
		return reply.result, nil
	case <-timer.C:
		c.removePending(id, ch)
		return nil, fmt.Errorf("MCP request '%s' timed out", method)
	case <-ctx.Done():
		c.removePending(id, ch)
		_ = c.notify("notifications/cancelled", map[string]any{"requestId": id, "reason": "cancelled by the user"})
		return nil, errToolCallCancelled
	}
}

func (c *stdioConn) removePending(id uint64, expected chan rpcReply) {
	c.pendM.Lock()
	if current, ok := c.pending[id]; ok && current == expected {
		delete(c.pending, id)
	}
	c.pendM.Unlock()
}

func (c *stdioConn) notify(method string, params any) error {
	body := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
	return c.writeMessage(body)
}

func (c *stdioConn) writeMessage(message any) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("JSON encode: %w", err)
	}
	payload = append(payload, '\n')
	c.writeM.Lock()
	defer c.writeM.Unlock()
	if c.closed.Load() {
		return errStdioConnClosed
	}
	if _, err := c.stdin.Write(payload); err != nil {
		return fmt.Errorf("stdin write: %w", err)
	}
	return nil
}

func (c *stdioConn) initialize() error {
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

func (c *stdioConn) fetchTools() ([]McpToolDef, error) {
	res, err := c.request("tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	return parseList[McpToolDef](res, "tools")
}

func (c *stdioConn) fetchResources() ([]McpResourceDef, error) {
	res, err := c.request("resources/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	return parseList[McpResourceDef](res, "resources")
}

func (c *stdioConn) fetchPrompts() ([]McpPromptDef, error) {
	res, err := c.request("prompts/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	return parseList[McpPromptDef](res, "prompts")
}

func (c *stdioConn) callTool(ctx context.Context, toolName string, arguments json.RawMessage) (*McpCallResult, error) {
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

func (c *stdioConn) readResource(uri string) (*McpResourceContent, error) {
	res, err := c.request("resources/read", map[string]any{"uri": uri})
	if err != nil {
		return nil, err
	}
	return parseResourceContent(res)
}

func (c *stdioConn) getPrompt(name string, arguments json.RawMessage) (*McpPromptResult, error) {
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

func (c *stdioConn) close() {
	if c.closed.Swap(true) {
		return
	}
	var waitErr error
	if c.cmd != nil && c.cmd.Process != nil {
		_ = core.KillProcessTree(c.cmd)
		waitErr = c.cmd.Wait()
	}
	closeErr := c.connectionClosedError(waitErr)
	c.pendM.Lock()
	pending := c.pending
	c.pending = make(map[uint64]chan rpcReply)
	c.pendM.Unlock()
	for _, ch := range pending {
		ch <- rpcReply{err: closeErr}
	}
}

func (c *stdioConn) connectionClosedError(waitErr error) error {
	if c.stderr != nil {
		if detail := c.stderr.String(); detail != "" {
			return fmt.Errorf("%w: %s", errStdioConnClosed, detail)
		}
	}
	if waitErr != nil && !strings.Contains(waitErr.Error(), "signal: killed") {
		return fmt.Errorf("%w: process exited: %v", errStdioConnClosed, waitErr)
	}
	return errStdioConnClosed
}

// readerFunc 把一个 Read 方法适配为 io.Reader（stdout/stderr 管道）。
type readerFunc func(p []byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }
