package terminal

import (
	"fmt"
	"io"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"

	"mincode/app/backend/core"
)

func init() {
	core.Register("start_ssh_shell", cmdStartSSHShell)
	core.Register("test_ssh_connection", cmdTestSSHConnection)
	core.Register("respond_ssh_mfa", cmdRespondSSHMFA)
	core.Register("shell_session_alive", cmdShellSessionAlive)
	core.Register("shell_ssh_try_recover", cmdSSHTryRecover)
}

func cmdStartSSHShell(_, args string) (string, error) {
	var p StartSshShellPayload
	if err := core.DecodeArgsPayload(args, &p); err != nil {
		return "", err
	}
	v, e := startSSHShell(p)
	if e != nil {
		return "", e
	}
	return core.EncodeResult(v)
}
func cmdTestSSHConnection(_, args string) (string, error) {
	var p TestSshConnectionPayload
	if err := core.DecodeArgsPayload(args, &p); err != nil {
		return "", err
	}
	c, j, e := dialTerminalSSH(p.Host, p.Port, p.Username, p.AuthType, p.Password, p.KeyFile, p.Passphrase, p.TotpURI, "__mfa_test", p.JumpHost, p.SocksProxy, p.AlgorithmPolicy)
	if e != nil {
		return "", fmt.Errorf("连接失败: %w", e)
	}
	defer c.Close()
	if j != nil {
		defer j.Close()
	}
	h, u := normalizeTerminalSSHHostUser(p.Host, p.Username)
	return core.EncodeResult(fmt.Sprintf("%s@%s:%d 连接与认证成功", u, h, terminalSSHPort(p.Port)))
}
func cmdRespondSSHMFA(_, args string) (string, error) {
	var p struct {
		SessionID string   `json:"sessionId"`
		Responses []string `json:"responses"`
	}
	if err := core.DecodeArgsPayload(args, &p); err != nil {
		return "", err
	}
	if !mfaChannels.respond(p.SessionID, p.Responses) {
		return "", fmt.Errorf("未找到对应的 MFA 等待会话（可能已超时）")
	}
	return core.EncodeResult(nil)
}
func cmdShellSessionAlive(_, args string) (string, error) {
	var p ShellSessionIDPayload
	if err := core.DecodeArgsPayload(args, &p); err != nil {
		return "", err
	}
	proc, ok := sessions.get(p.SessionID)
	alive := false
	if ok {
		proc.mu.Lock()
		alive = proc.stdinTx != nil || (proc.sshLinkState == sshLinkGracePeriod && proc.sshClient != nil)
		proc.mu.Unlock()
	}
	return core.EncodeResult(alive)
}
func cmdSSHTryRecover(_, args string) (string, error) {
	var p ShellSessionIDPayload
	if err := core.DecodeArgsPayload(args, &p); err != nil {
		return "", err
	}
	return core.EncodeResult(tryRecoverSSHShell(strings.TrimSpace(p.SessionID)))
}

func startSSHShell(p StartSshShellPayload) (string, error) {
	sid := strings.TrimSpace(p.SessionID)
	if sid == "" {
		return "", fmt.Errorf("session_id 不能为空")
	}
	gateHeld := false
	if p.AttachIfExists {
		for {
			wait, owner, existing := sessions.beginStart(sid, true)
			if existing != nil {
				existing.mu.Lock()
				active := existing.stdinTx != nil && existing.sshLinkState == sshLinkActive
				existing.mu.Unlock()
				if active {
					return "SSH shell attached", nil
				}
				if tryRecoverSSHShell(sid) {
					return "SSH shell recovered", nil
				}
				break
			}
			if owner {
				gateHeld = true
				break
			}
			<-wait
		}
	}

	// attach 发现不可恢复的旧会话时，以及显式重启时，取得替换启动权。
	if !gateHeld {
		for {
			wait, owner, _ := sessions.beginStart(sid, false)
			if owner {
				break
			}
			<-wait
		}
	}

	diagnostics := newSSHConnectionDiagnostics(sid)
	host, user := normalizeTerminalSSHHostUser(p.Host, p.Username)
	if host == "" || user == "" {
		sessions.cancelStart(sid)
		err := fmt.Errorf("主机地址和用户名不能为空")
		diagnostics.fail(sshConnectionHopTarget, sshConnectionStageAuthentication, err)
		return "", err
	}
	cols, rows := clampPtyColsRows(p.Cols, p.Rows)
	initialDirectory, postConnectCommands, err := normalizeSSHInitialization(p.InitialDirectory, p.PostConnectCommands)
	if err != nil {
		sessions.cancelStart(sid)
		return "", err
	}
	client, jump, err := dialTerminalSSHWithDiagnostics(host, p.Port, user, p.AuthType, p.Password, p.KeyFile, p.Passphrase, p.TotpURI, sid, p.JumpHost, p.SocksProxy, p.AlgorithmPolicy, diagnostics)
	if err != nil {
		sessions.cancelStart(sid)
		return "", err
	}
	proc := &shellProcess{
		sshClient: &sshClientHandle{client: client}, sshLinkState: sshLinkActive,
		sshHistoryProfileID: p.ProfileID,
		sshHistoryHost:      host, sshHistoryUser: user, sshHistoryPort: uint16(terminalSSHPort(p.Port)),
		sshRecoveryMeta: &sshRecoveryMeta{cols: cols, rows: rows}, remoteForwardTargets: &sync.Map{},
		portForwardRules:     append([]PortForwardRulePayload(nil), p.PortForwards...),
		sshKeepAliveInterval: p.KeepAliveIntervalSeconds, sshKeepAliveMaxMisses: p.KeepAliveMaxMisses,
		sshInitialDirectory:    initialDirectory,
		control:                addControlledPane(sid, user+"@"+host, initialDirectory, false, nil),
		sshPostConnectCommands: append([]string(nil), postConnectCommands...),
		sshPostConnectPending:  len(postConnectCommands) > 0,
	}
	reconnectEnabled := p.ReconnectEnabled == nil || *p.ReconnectEnabled
	if reconnectEnabled {
		proc.sshRedial = func() (*ssh.Client, *ssh.Client, error) {
			return dialTerminalSSH(host, p.Port, user, p.AuthType, p.Password, p.KeyFile, p.Passphrase, p.TotpURI, sid, p.JumpHost, p.SocksProxy, p.AlgorithmPolicy)
		}
		proc.sshReconnectMaxAttempts = clampReconnectAttempts(p.ReconnectMaxAttempts)
		proc.sshReconnectBaseDelay = clampReconnectDelay(p.ReconnectBaseDelayMs, defaultSSHReconnectBaseDelay)
		proc.sshReconnectMaxDelay = clampReconnectDelay(p.ReconnectMaxDelayMs, defaultSSHReconnectMaxDelay)
		if proc.sshReconnectMaxDelay < proc.sshReconnectBaseDelay {
			proc.sshReconnectMaxDelay = proc.sshReconnectBaseDelay
		}
	}
	if jump != nil {
		proc.jumpClient = &sshClientHandle{client: jump}
	}
	old := sessions.publishStart(sid, proc)
	if old != nil {
		shutdownShellProcess(old)
	}
	diagnostics.begin(sshConnectionHopTarget, sshConnectionStageShell)
	if err := startSSHShellRelay(sid, proc, cols, rows); err != nil {
		diagnostics.fail(sshConnectionHopTarget, sshConnectionStageShell, err)
		removeControlledPane(sid)
		sessions.finishStart(sid)
		if current, ok := sessions.removeIfSame(sid, proc); ok {
			shutdownShellProcess(current)
		}
		return "", err
	}
	diagnostics.succeed(sshConnectionHopTarget, sshConnectionStageShell)
	startSSHKeepAlive(sid, proc, p.KeepAliveIntervalSeconds, p.KeepAliveMaxMisses)
	// 端口转发是 SSH shell 的附加能力。某个本地端口被其他会话占用时，
	// 保留已经建立成功的 shell，并把失败状态交给端口转发面板展示和重试。
	startConfiguredPortForwards(proc, p.PortForwards)
	sessions.finishStart(sid)
	return fmt.Sprintf("SSH shell %s@%s:%d（%d×%d）", user, host, terminalSSHPort(p.Port), cols, rows), nil
}

func startSSHShellRelay(sessionID string, proc *shellProcess, cols, rows uint16) error {
	return startSSHSessionRelay(sessionID, proc, "", true, cols, rows)
}

// startSSHSessionRelay 在会话持有的 SSH 连接上打开一个通道并接管其 I/O。
// command 为空时请求交互式登录 shell，否则执行该命令（如 docker exec / docker logs -f）；
// wantPty 为 false 时不申请 PTY，stdout 与 stderr 保持为两条独立流。
func startSSHSessionRelay(sessionID string, proc *shellProcess, command string, wantPty bool, cols, rows uint16) error {
	proc.mu.Lock()
	client := proc.sshClient.client
	proc.mu.Unlock()
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("打开 SSH session 通道失败: %w", err)
	}
	if wantPty {
		if err = session.RequestPty("xterm-256color", int(rows), int(cols), nil); err != nil {
			session.Close()
			return fmt.Errorf("请求 PTY 失败: %w", err)
		}
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		session.Close()
		return err
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		session.Close()
		return err
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		session.Close()
		return err
	}
	if command == "" {
		err = session.Shell()
	} else {
		err = session.Start(command)
	}
	if err != nil {
		session.Close()
		if command == "" {
			return fmt.Errorf("请求远程 shell 失败: %w", err)
		}
		return fmt.Errorf("启动远程命令失败: %w", err)
	}
	input := make(chan string, 64)
	resize := make(chan [2]uint16, 16)
	done := make(chan struct{})
	proc.mu.Lock()
	proc.stdinTx = input
	proc.stdinDone = done
	proc.sshResizeTx = resize
	proc.sshTaskCancel = func() {
		select {
		case <-done:
		default:
			close(done)
		}
		_ = session.Close()
	}
	proc.mu.Unlock()
	go func() {
		for {
			select {
			case text := <-input:
				if e := writeAll(stdin, []byte(text)); e != nil {
					return
				}
			case size := <-resize:
				_ = session.WindowChange(int(size[1]), int(size[0]))
			case <-done:
				return
			}
		}
	}()
	go relaySSHOutput(sessionID, proc, stdout, stderr, session, done)
	enqueueSSHInitialization(proc, input)
	return nil
}

func relaySSHOutput(sessionID string, proc *shellProcess, stdout, stderr io.Reader, session interface{ Wait() error }, done <-chan struct{}) {
	type outputChunk struct {
		stream int
		data   []byte
	}
	out := make(chan outputChunk, 32)
	var readers sync.WaitGroup
	read := func(stream int, r io.Reader) {
		defer readers.Done()
		buf := make([]byte, ptyReadBufferSize)
		for {
			n, e := r.Read(buf)
			if n > 0 {
				select {
				case out <- outputChunk{stream: stream, data: append([]byte(nil), buf[:n]...)}:
				case <-done:
					return
				}
			}
			if e != nil {
				return
			}
		}
	}
	readers.Add(2)
	go read(0, stdout)
	go read(1, stderr)
	go func() { _ = session.Wait() }()
	go func() { readers.Wait(); close(out) }()
	var carries [2][]byte
	coalesceOutput(out, done, func(batch *outputBatch, data outputChunk) {
		if text := ptyPushAndTakeUTF8(&carries[data.stream], data.data); text != "" {
			observeSSHOutput(proc, text)
			batch.push(text)
		}
	}, func(data string) {
		proc.recordControlOutput(data)
		dispatchShellOutput(sessionID, data)
	}, outputCoalesceWindow)
	proc.mu.Lock()
	ephemeral := proc.ephemeral
	proc.mu.Unlock()
	// 派生会话跑的是一条会正常退出的命令（用户敲 exit、docker logs 结束），
	// 命令退出不是链路故障，不能进入 GracePeriod——否则重连会把命令再执行一遍。
	if ephemeral {
		if proc.control != nil {
			proc.control.end()
		}
		if old, ok := sessions.removeIfSame(sessionID, proc); ok {
			shutdownShellProcess(old)
		}
		emitShellCommandEnded(sessionID)
		return
	}
	enterSSHGracePeriod(sessionID, proc, done)
}
