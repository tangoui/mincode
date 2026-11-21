// SSH 拨号与认证辅助，供 SFTP 部署功能使用。
//
// 行为对齐 src-tauri/src/terminal/ssh_auth.rs 的核心逻辑：
//   - 支持密码 / 公钥（含 passphrase）认证；
//   - key_file 为空时尝试默认 ~/.ssh/id_ed25519、id_rsa；
//   - host 字段允许 user@hostname 前缀，需拆分出合法 TCP hostname；
//   - HostKeyCallback 采用 fail-closed TOFU known_hosts 校验。
//
// 说明：keyboard-interactive（MFA）在部署场景中较少使用，这里用密码回答 prompt 的
// 方式做尽力支持；若服务端要求交互式多步 MFA，则超出当前范围。
package sftp

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"mincode/app/backend/core"
	appcrypto "mincode/app/backend/crypto"
	"mincode/app/backend/sshauth"
)

// sshConnectParams 为建立 SSH 连接所需的规范化参数。
type sshConnectParams struct {
	host            string
	port            int
	username        string
	authType        string
	password        string
	keyFile         string
	passphrase      string
	algorithmPolicy string
	// socksProxy 非 nil 时，第一跳 TCP 拨号经 SOCKS5 代理建立。
	socksProxy *sshauth.SocksProxyConfig
	// hostKeyCallback 为空时使用 TOFU；无人值守的已保存连接传入严格校验。
	hostKeyCallback ssh.HostKeyCallback
	// timeout 约束 TCP 拨号与 SSH 握手，0 表示 20 秒。
	timeout time.Duration
	// ctx 非 nil 时直连拨号可被取消，且握手同样受 timeout 约束。
	ctx context.Context
}

// normalizeSSHHostUser 解析 host 字段中可选的 user@hostname 前缀，
// 返回合法 TCP hostname 与最终 username。逻辑对齐 Rust 侧 normalize_ssh_host_user。
func normalizeSSHHostUser(host, username string) (string, string) {
	h := strings.TrimSpace(host)
	u := strings.TrimSpace(username)
	at := strings.Index(h, "@")
	if at < 0 {
		return h, u
	}
	embedded := strings.TrimSpace(h[:at])
	rest := strings.TrimSpace(h[at+1:])
	if rest == "" {
		return h, u
	}
	if u == "" {
		return rest, embedded
	}
	// 无论用户名与前缀是否一致，都需去除 @ 前缀以获得合法 TCP hostname；
	// 显式设置的用户名优先。
	return rest, u
}

// loadSSHSigner 加载用于公钥认证的私钥签名器。
// keyFile 为空时按默认路径顺序尝试；passphrase 为空则以无密码方式解析。
func loadSSHSigner(keyFile, passphrase string) (ssh.Signer, error) {
	paths := sshauth.PrivateKeyPaths(keyFile)

	var lastErr error
	var tried []string
	for _, p := range paths {
		if _, statErr := os.Stat(p); statErr != nil {
			continue
		}
		tried = append(tried, p)
		raw, readErr := os.ReadFile(p)
		if readErr != nil {
			lastErr = fmt.Errorf("%s: %w", p, readErr)
			continue
		}
		var signer ssh.Signer
		var parseErr error
		if passphrase != "" {
			signer, parseErr = ssh.ParsePrivateKeyWithPassphrase(raw, []byte(passphrase))
		} else {
			signer, parseErr = ssh.ParsePrivateKey(raw)
		}
		if parseErr != nil {
			lastErr = fmt.Errorf("%s: %w", p, parseErr)
			continue
		}
		return signer, nil
	}
	if lastErr != nil {
		return nil, fmt.Errorf("加载私钥失败（已尝试: %v）: %w", tried, lastErr)
	}
	return nil, fmt.Errorf("未找到可用私钥文件，已尝试: %v", paths)
}

// buildAuthMethods 依据 authType 组装认证方法列表。
// authType 语义与前端对齐："password" / "key" / "agent"；空值按 password 处理。
// 为提升成功率，密码存在时同时挂载 keyboard-interactive 回退（服务端仅开放交互式密码时可用）。
func buildAuthMethods(p sshConnectParams) ([]ssh.AuthMethod, error) {
	authType := strings.ToLower(strings.TrimSpace(p.authType))
	password := p.password
	keyFile := strings.TrimSpace(p.keyFile)

	var methods []ssh.AuthMethod

	wantKey := authType == "key" || authType == "publickey" || (authType == "" && keyFile != "")
	wantPassword := authType == "password" || authType == "" || password != ""

	if wantKey {
		signer, err := loadSSHSigner(keyFile, p.passphrase)
		if err != nil {
			// 若同时可用密码，则容忍密钥加载失败继续尝试密码。
			if !wantPassword {
				return nil, err
			}
		} else {
			methods = append(methods, ssh.PublicKeys(signer))
		}
	}

	if wantPassword && strings.TrimSpace(password) != "" {
		methods = append(methods, ssh.Password(password))
		// keyboard-interactive 回退：用密码回答所有 prompt。
		methods = append(methods, ssh.KeyboardInteractive(
			func(_, _ string, questions []string, _ []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range questions {
					answers[i] = password
				}
				return answers, nil
			},
		))
	}

	if len(methods) == 0 {
		return nil, fmt.Errorf("未提供可用的 SSH 认证凭据")
	}
	return methods, nil
}

// buildSftpSocksConfig 将前端代理载荷转为拨号器配置：未配置返回 nil（直连），
// 配置不完整报错而非静默回退。凭据字段走与连接密码一致的透明解密路径。
func buildSftpSocksConfig(payload *sftpSocksProxyPayload) (*sshauth.SocksProxyConfig, error) {
	if payload == nil {
		return nil, nil
	}
	decrypt := func(v *string) (string, error) {
		plain, err := appcrypto.DecryptOptional(v)
		if err != nil {
			return "", fmt.Errorf("解密代理凭据失败: %w", err)
		}
		if plain == nil {
			return "", nil
		}
		return *plain, nil
	}
	username, err := decrypt(payload.Username)
	if err != nil {
		return nil, err
	}
	password, err := decrypt(payload.Password)
	if err != nil {
		return nil, err
	}
	cfg := sshauth.SocksProxyConfig{
		Host:     strings.TrimSpace(payload.Host),
		Port:     int(payload.Port),
		Username: username,
		Password: password,
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// dialSSH 建立 SSH 客户端连接。
func dialSSH(p sshConnectParams) (*ssh.Client, error) {
	host, username := normalizeSSHHostUser(p.host, p.username)
	if strings.TrimSpace(host) == "" {
		return nil, fmt.Errorf("host 不能为空")
	}
	if strings.TrimSpace(username) == "" {
		return nil, fmt.Errorf("username 不能为空")
	}
	port := p.port
	if port <= 0 {
		port = 22
	}

	methods, err := buildAuthMethods(sshConnectParams{
		host:            host,
		port:            port,
		username:        username,
		authType:        p.authType,
		password:        p.password,
		keyFile:         p.keyFile,
		passphrase:      p.passphrase,
		algorithmPolicy: p.algorithmPolicy,
	})
	if err != nil {
		return nil, err
	}

	algorithms := sshauth.AlgorithmSet(p.algorithmPolicy)
	hostKeyCallback := p.hostKeyCallback
	if hostKeyCallback == nil {
		hostKeyCallback = sshauth.TOFUHostKeyCallback(host, port)
	}
	timeout := p.timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	cfg := &ssh.ClientConfig{
		User:            username,
		Auth:            methods,
		HostKeyCallback: hostKeyCallback,
		Timeout:         timeout,
		// 与终端 SSH 一致：将 known_hosts 已记录的密钥算法前置，
		// 避免多 host key 服务器协商到未记录类型而被误判为密钥变更。
		HostKeyAlgorithms: sshauth.PrioritizedHostKeyAlgos(host, port, algorithms.HostKeys),
		Config:            ssh.Config{KeyExchanges: algorithms.KeyExchanges, Ciphers: algorithms.Ciphers, MACs: algorithms.MACs},
	}

	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	dialOnce := func(c *ssh.ClientConfig) (*ssh.Client, error) {
		if p.socksProxy != nil {
			// 经 SOCKS5 代理建立隧道后在隧道内完成 SSH 握手；
			// c.Timeout 仅在 ssh.Dial 中生效，这里手动约束握手 deadline。
			conn, err := sshauth.DialThroughSocks(*p.socksProxy, addr, c.Timeout)
			if err != nil {
				return nil, err
			}
			if c.Timeout > 0 {
				_ = conn.SetDeadline(time.Now().Add(c.Timeout))
			}
			cc, chans, reqs, err := ssh.NewClientConn(conn, addr, c)
			if err != nil {
				_ = conn.Close()
				return nil, err
			}
			_ = conn.SetDeadline(time.Time{})
			return ssh.NewClient(cc, chans, reqs), nil
		}
		if p.ctx != nil {
			// ssh.Dial 的 Timeout 不覆盖握手；这里拨号可取消，握手同样受 deadline 约束。
			conn, err := (&net.Dialer{Timeout: c.Timeout}).DialContext(p.ctx, "tcp", addr)
			if err != nil {
				return nil, err
			}
			_ = conn.SetDeadline(time.Now().Add(c.Timeout))
			cc, chans, reqs, err := ssh.NewClientConn(conn, addr, c)
			if err != nil {
				_ = conn.Close()
				return nil, err
			}
			_ = conn.SetDeadline(time.Time{})
			return ssh.NewClient(cc, chans, reqs), nil
		}
		return ssh.Dial("tcp", addr, c)
	}
	client, err := dialOnce(cfg)
	if err != nil && sshauth.IsNoCommonAlgorithmError(err) {
		// 与终端 SSH 一致：现代策略协商失败时自动追加历史兼容算法重试一次，
		// 成功后提示用户该服务器依赖弃用算法，避免静默降级。
		if retryCfg, ok := sshauth.LegacyRetryConfig(cfg); ok {
			if retryClient, retryErr := dialOnce(retryCfg); retryErr == nil {
				core.Notify(core.AppNotificationPayload{Source: "ssh", Level: "warning", Code: "ssh-legacy-algorithms-fallback"})
				return retryClient, nil
			} else {
				return nil, fmt.Errorf("SFTP 连接失败: %w", retryErr)
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("SFTP 连接失败: %w", err)
	}
	return client, nil
}
