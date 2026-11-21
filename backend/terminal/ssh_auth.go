package terminal

import (
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"mincode/app/backend/core"
	appcrypto "mincode/app/backend/crypto"
	"mincode/app/backend/sshauth"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func normalizeTerminalSSHHostUser(host, username string) (string, string) {
	h, u := strings.TrimSpace(host), strings.TrimSpace(username)
	if at := strings.IndexByte(h, '@'); at >= 0 {
		if embedded, rest := strings.TrimSpace(h[:at]), strings.TrimSpace(h[at+1:]); rest != "" {
			if u == "" {
				u = embedded
			}
			h = rest
		}
	}
	return h, u
}

func terminalSSHPort(port uint16) int {
	if port == 0 {
		return 22
	}
	return int(port)
}

func terminalSSHSigner(keyFile, passphrase string) (ssh.Signer, error) {
	paths := sshauth.PrivateKeyPaths(keyFile)
	var last error
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			last = err
			continue
		}
		if strings.TrimSpace(passphrase) != "" {
			return ssh.ParsePrivateKeyWithPassphrase(raw, []byte(passphrase))
		}
		return ssh.ParsePrivateKey(raw)
	}
	return nil, fmt.Errorf("未找到可用私钥文件（已尝试 %s）: %w", strings.Join(paths, ", "), last)
}

func tofuHostKeyCallback(host string, port int) ssh.HostKeyCallback {
	return sshauth.TOFUHostKeyCallback(host, port)
}

// noopSSHAuthCleanup 供无需释放资源的认证方式返回，保证调用方可无条件 defer。
func noopSSHAuthCleanup() {}

// terminalSSHAuthMethods 返回认证方法及其资源释放函数：agent 方式持有的 unix socket
// 需在握手完成后由调用方关闭，避免每次连接/测试/重连泄漏一个 fd。
func terminalSSHAuthMethods(authType, password, keyFile, passphrase, sessionID string, totpURI *string) ([]ssh.AuthMethod, func(), error) {
	authType = strings.ToLower(strings.TrimSpace(authType))
	if authType == "" {
		authType = DefaultSshAuthType
	}
	switch authType {
	case "password":
		if strings.TrimSpace(password) == "" {
			return nil, noopSSHAuthCleanup, fmt.Errorf("未提供密码")
		}
		return []ssh.AuthMethod{ssh.Password(password)}, noopSSHAuthCleanup, nil
	case "key", "publickey":
		signer, err := terminalSSHSigner(keyFile, passphrase)
		if err != nil {
			return nil, noopSSHAuthCleanup, fmt.Errorf("加载私钥失败: %w", err)
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, noopSSHAuthCleanup, nil
	case "agent":
		sock := os.Getenv("SSH_AUTH_SOCK")
		if sock == "" {
			return nil, noopSSHAuthCleanup, fmt.Errorf("无法连接 SSH Agent：SSH_AUTH_SOCK 未设置")
		}
		conn, err := net.DialTimeout("unix", sock, 5*time.Second)
		if err != nil {
			return nil, noopSSHAuthCleanup, fmt.Errorf("无法连接 SSH Agent: %w", err)
		}
		var closeOnce sync.Once
		cleanup := func() { closeOnce.Do(func() { _ = conn.Close() }) }
		return []ssh.AuthMethod{ssh.PublicKeysCallback(agent.NewClient(conn).Signers)}, cleanup, nil
	case "keyboard-interactive":
		methods := make([]ssh.AuthMethod, 0, 2)
		// JumpServer 等服务可能要求 publickey 后再 keyboard-interactive；
		// 有配置私钥时先附带它，x/crypto/ssh 会在 partial-success 后继续 MFA。
		if strings.TrimSpace(keyFile) != "" {
			signer, err := terminalSSHSigner(keyFile, passphrase)
			if err != nil {
				return nil, noopSSHAuthCleanup, fmt.Errorf("加载 MFA 前置私钥失败: %w", err)
			}
			methods = append(methods, ssh.PublicKeys(signer))
		}
		methods = append(methods, ssh.KeyboardInteractive(func(name, instruction string, questions []string, echos []bool) ([]string, error) {
			if len(questions) == 0 {
				return []string{}, nil
			}
			if shouldAutoRespondTerminalTOTP(questions, totpURI) {
				if code, err := generateTerminalTOTP(*totpURI, time.Now()); err == nil {
					return []string{code}, nil
				}
			}
			ch := mfaChannels.register(sessionID)
			defer mfaChannels.unregister(sessionID)
			items := make([]SshMfaPromptItem, len(questions))
			for i, question := range questions {
				items[i] = SshMfaPromptItem{Prompt: question, Echo: i < len(echos) && echos[i]}
			}
			prompt := SshMfaPromptPayload{SessionID: sessionID, Name: name, Instructions: instruction, Prompts: items}
			core.Emit("ssh-mfa-prompt", prompt)
			select {
			case answers := <-ch:
				return answers, nil
			case <-time.After(120 * time.Second):
				return nil, fmt.Errorf("MFA 超时（120 秒无响应）")
			}
		}))
		return methods, noopSSHAuthCleanup, nil
	default:
		return nil, noopSSHAuthCleanup, fmt.Errorf("不支持的认证方式: %s（请使用 password、key、agent 或 keyboard-interactive）", authType)
	}
}

// terminalSSHConfig 返回的 cleanup 必须在该配置的握手（含重试）结束后调用。
func terminalSSHConfig(host string, port int, username, authType, password, keyFile, passphrase, sessionID string, totpURI *string, algorithmPolicy string) (*ssh.ClientConfig, func(), error) {
	methods, cleanup, err := terminalSSHAuthMethods(authType, password, keyFile, passphrase, sessionID, totpURI)
	if err != nil {
		cleanup()
		return nil, noopSSHAuthCleanup, err
	}
	algorithms := sshauth.AlgorithmSet(algorithmPolicy)
	return &ssh.ClientConfig{
		User: username, Auth: methods, HostKeyCallback: tofuHostKeyCallback(host, port), Timeout: 20 * time.Second,
		HostKeyAlgorithms: sshauth.PrioritizedHostKeyAlgos(host, port, algorithms.HostKeys),
		Config:            ssh.Config{KeyExchanges: algorithms.KeyExchanges, Ciphers: algorithms.Ciphers, MACs: algorithms.MACs},
	}, cleanup, nil
}

func decryptTerminalCredentials(password **string) (string, error) {
	plain, err := appcrypto.DecryptOptional(*password)
	if err != nil {
		return "", err
	}
	if plain == nil {
		return "", nil
	}
	return *plain, nil
}

func dialTerminalSSH(host string, port uint16, username, authType string, password, keyFile, passphrase, totpURI *string, sessionID string, jump *JumpHostPayload, proxy *SocksProxyPayload, algorithmPolicy string) (*ssh.Client, *ssh.Client, error) {
	return dialTerminalSSHWithDiagnostics(host, port, username, authType, password, keyFile, passphrase, totpURI, sessionID, jump, proxy, algorithmPolicy, nil)
}

func dialTerminalSSHWithDiagnostics(host string, port uint16, username, authType string, password, keyFile, passphrase, totpURI *string, sessionID string, jump *JumpHostPayload, proxy *SocksProxyPayload, algorithmPolicy string, diagnostics *sshConnectionDiagnostics) (*ssh.Client, *ssh.Client, error) {
	host, username = normalizeTerminalSSHHostUser(host, username)
	if host == "" || username == "" {
		err := fmt.Errorf("主机地址和用户名不能为空")
		diagnostics.fail(sshConnectionHopTarget, sshConnectionStageAuthentication, err)
		return nil, nil, err
	}
	p := terminalSSHPort(port)
	pw, err := decryptTerminalCredentials(&password)
	if err != nil {
		diagnostics.fail(sshConnectionHopTarget, sshConnectionStageAuthentication, err)
		return nil, nil, err
	}
	str := func(v *string) string {
		if v == nil {
			return ""
		}
		return *v
	}
	cfg, cleanupAuth, err := terminalSSHConfig(host, p, username, authType, pw, str(keyFile), str(passphrase), sessionID, totpURI, algorithmPolicy)
	if err != nil {
		diagnostics.fail(sshConnectionHopTarget, sshConnectionStageAuthentication, err)
		return nil, nil, err
	}
	// 握手在本函数内同步完成，返回后 agent 连接已无用。
	defer cleanupAuth()
	addr := net.JoinHostPort(host, fmt.Sprint(p))
	socks, err := terminalSocksConfig(proxy)
	if err != nil {
		diagnostics.fail(sshConnectionHopTarget, sshConnectionStageAuthentication, err)
		return nil, nil, err
	}
	if jump == nil {
		client, err := openTerminalSSHClient(addr, cfg, diagnostics, sshConnectionHopTarget,
			buildFirstHopDialer(socks, addr, cfg.Timeout, diagnostics, sshConnectionHopTarget))
		return client, nil, err
	}

	jh, ju := normalizeTerminalSSHHostUser(jump.Host, jump.Username)
	if jh == "" || ju == "" {
		err := fmt.Errorf("跳板机地址和用户名不能为空")
		diagnostics.fail(sshConnectionHopJump, sshConnectionStageAuthentication, err)
		return nil, nil, err
	}
	jp := terminalSSHPort(jump.Port)
	jpw, err := decryptTerminalCredentials(&jump.Password)
	if err != nil {
		diagnostics.fail(sshConnectionHopJump, sshConnectionStageAuthentication, err)
		return nil, nil, err
	}
	jcfg, cleanupJumpAuth, err := terminalSSHConfig(jh, jp, ju, jump.AuthType, jpw, str(jump.KeyFile), str(jump.Passphrase), "__jump_"+sessionID, nil, jump.AlgorithmPolicy)
	if err != nil {
		diagnostics.fail(sshConnectionHopJump, sshConnectionStageAuthentication, err)
		return nil, nil, err
	}
	defer cleanupJumpAuth()
	jumpAddr := net.JoinHostPort(jh, fmt.Sprint(jp))
	// 代理仅作用于第一跳：有跳板机时代理拨跳板机，目标经跳板机转发不再过代理。
	jumpClient, err := openTerminalSSHClient(jumpAddr, jcfg, diagnostics, sshConnectionHopJump,
		buildFirstHopDialer(socks, jumpAddr, jcfg.Timeout, diagnostics, sshConnectionHopJump))
	if err != nil {
		return nil, nil, fmt.Errorf("跳板机连接失败: %w", err)
	}
	targetClient, err := openTerminalSSHClient(addr, cfg, diagnostics, sshConnectionHopTarget, func() (net.Conn, error) {
		return jumpClient.Dial("tcp", addr)
	})
	if err != nil {
		jumpClient.Close()
		return nil, nil, fmt.Errorf("跳板机转发通道失败: %w", err)
	}
	return targetClient, jumpClient, nil
}

// terminalSocksConfig 将前端载荷转为共享拨号器配置：未配置返回 nil（直连），
// 配置不完整（如缺 host）报错而非静默回退，避免用户误以为走了代理。
func terminalSocksConfig(proxy *SocksProxyPayload) (*sshauth.SocksProxyConfig, error) {
	if proxy == nil {
		return nil, nil
	}
	username, err := decryptTerminalCredentials(&proxy.Username)
	if err != nil {
		return nil, err
	}
	password, err := decryptTerminalCredentials(&proxy.Password)
	if err != nil {
		return nil, err
	}
	cfg := sshauth.SocksProxyConfig{
		Host:     strings.TrimSpace(proxy.Host),
		Port:     int(proxy.Port),
		Username: username,
		Password: password,
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// buildFirstHopDialer 构造第一跳 TCP 拨号闭包：配置代理时经 SOCKS5 隧道拨号并
// 记录 proxy 诊断阶段，否则直连。返回的闭包供重连复用。
func buildFirstHopDialer(proxy *sshauth.SocksProxyConfig, addr string, timeout time.Duration, diagnostics *sshConnectionDiagnostics, hop string) func() (net.Conn, error) {
	if proxy == nil {
		return func() (net.Conn, error) {
			return net.DialTimeout("tcp", addr, timeout)
		}
	}
	return func() (net.Conn, error) {
		diagnostics.begin(hop, sshConnectionStageProxy)
		conn, err := sshauth.DialThroughSocks(*proxy, addr, timeout)
		if err != nil {
			diagnostics.fail(hop, sshConnectionStageProxy, err)
			return nil, err
		}
		diagnostics.succeed(hop, sshConnectionStageProxy)
		return conn, nil
	}
}

// legacyFallbackNotified 记录已提示过兼容算法降级的地址，进程内只通知一次，
// 避免断线自动重连时通知中心被同一警告刷屏。
var legacyFallbackNotified sync.Map

// openTerminalSSHClient 先按当前策略拨号；握手因算法无交集失败且配置尚未包含
// 兼容算法时，自动追加历史兼容算法重试一次（对齐 legacy 策略的算法集），成功后
// 通过通知中心提示用户该服务器需要弃用算法，避免静默降级。
func openTerminalSSHClient(addr string, cfg *ssh.ClientConfig, diagnostics *sshConnectionDiagnostics, hop string, dial func() (net.Conn, error)) (*ssh.Client, error) {
	client, err := openTerminalSSHClientAttempt(addr, cfg, diagnostics, hop, dial)
	if err == nil || !sshauth.IsNoCommonAlgorithmError(err) {
		return client, err
	}
	retryCfg, ok := sshauth.LegacyRetryConfig(cfg)
	if !ok {
		// 已是 legacy 策略，重试无意义；补记第一次失败被推迟的诊断。
		diagnostics.failActiveOr(hop, sshConnectionStageHandshake, err)
		return nil, err
	}
	client, retryErr := openTerminalSSHClientAttempt(addr, retryCfg, diagnostics, hop, dial)
	if retryErr != nil {
		// 非算法错误已在尝试内标记；此处兜底标记仍为算法错误的场景。
		diagnostics.failActiveOr(hop, sshConnectionStageHandshake, retryErr)
		return nil, retryErr
	}
	if _, loaded := legacyFallbackNotified.LoadOrStore(addr, struct{}{}); !loaded {
		core.Notify(core.AppNotificationPayload{Source: "ssh", Level: "warning", Code: "ssh-legacy-algorithms-fallback"})
	}
	return client, nil
}

func openTerminalSSHClientAttempt(addr string, cfg *ssh.ClientConfig, diagnostics *sshConnectionDiagnostics, hop string, dial func() (net.Conn, error)) (*ssh.Client, error) {
	diagnostics.begin(hop, sshConnectionStageTCP)
	conn, err := dial()
	if err != nil {
		diagnostics.fail(hop, sshConnectionStageTCP, err)
		return nil, err
	}
	diagnostics.succeed(hop, sshConnectionStageTCP)

	// Bound key exchange as well as the TCP dial. The deadline is cleared after
	// host-key verification so keyboard-interactive/MFA can keep its established
	// 120-second response window.
	if cfg.Timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(cfg.Timeout))
	}
	diagnostics.begin(hop, sshConnectionStageHandshake)
	originalHostKeyCallback := cfg.HostKeyCallback
	config := *cfg
	config.HostKeyCallback = func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		diagnostics.succeed(hop, sshConnectionStageHandshake)
		diagnostics.begin(hop, sshConnectionStageHostKey)
		err := originalHostKeyCallback(hostname, remote, key)
		if err != nil {
			diagnostics.fail(hop, sshConnectionStageHostKey, err)
			return err
		}
		diagnostics.succeed(hop, sshConnectionStageHostKey)
		_ = conn.SetDeadline(time.Time{})
		diagnostics.begin(hop, sshConnectionStageAuthentication)
		return nil
	}
	cc, chans, reqs, err := ssh.NewClientConn(conn, addr, &config)
	if err != nil {
		_ = conn.Close()
		// 算法协商失败可能经兼容算法重试恢复，诊断失败推迟到重试决策后标记。
		if !sshauth.IsNoCommonAlgorithmError(err) {
			diagnostics.failActiveOr(hop, sshConnectionStageHandshake, err)
		}
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	diagnostics.succeed(hop, sshConnectionStageAuthentication)
	return ssh.NewClient(cc, chans, reqs), nil
}
