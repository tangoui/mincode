package sshauth

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

var knownHostsMu sync.Mutex

type address string

func (a address) Network() string { return "tcp" }
func (a address) String() string  { return string(a) }

// TOFUHostKeyCallback 首次连接写入 known_hosts，后续严格校验；任何存储错误均拒绝连接。
func TOFUHostKeyCallback(host string, port int) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		knownHostsMu.Lock()
		defer knownHostsMu.Unlock()

		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("无法定位用户目录: %w", err)
		}
		dir := filepath.Join(home, ".ssh")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("创建 SSH 配置目录失败: %w", err)
		}
		path := filepath.Join(dir, "known_hosts")
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				return fmt.Errorf("创建 known_hosts 失败: %w", err)
			}
		} else if err != nil {
			return fmt.Errorf("读取 known_hosts 失败: %w", err)
		}
		callback, err := knownhosts.New(path)
		if err != nil {
			return fmt.Errorf("解析 known_hosts 失败: %w", err)
		}
		remote := net.JoinHostPort(host, fmt.Sprint(port))
		if err := callback(remote, address(remote), key); err == nil {
			return nil
		} else {
			var keyErr *knownhosts.KeyError
			if !errors.As(err, &keyErr) {
				return err
			}
			if len(keyErr.Want) != 0 {
				return fmt.Errorf("known_hosts 校验失败 %s：主机密钥已变更，已拒绝连接", remote)
			}
		}
		file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("打开 known_hosts 失败: %w", err)
		}
		line := knownhosts.Line([]string{knownhosts.Normalize(remote)}, key) + "\n"
		if _, err := file.WriteString(line); err != nil {
			_ = file.Close()
			return fmt.Errorf("写入 known_hosts 失败: %w", err)
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return fmt.Errorf("同步 known_hosts 失败: %w", err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("关闭 known_hosts 失败: %w", err)
		}
		return nil
	}
}

// knownHostKeyAlgos 返回 known_hosts 中已记录的该主机公钥算法名列表（去重、保持出现顺序）。
func knownHostKeyAlgos(host string, port int) []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(home, ".ssh", "known_hosts"))
	if err != nil {
		return nil
	}
	addr := knownhosts.Normalize(net.JoinHostPort(host, fmt.Sprint(port)))
	var algos []string
	seen := map[string]bool{}
	rest := raw
	for len(rest) > 0 {
		marker, hosts, pubKey, _, remaining, err := ssh.ParseKnownHosts(rest)
		if err != nil {
			break
		}
		rest = remaining
		if marker == "revoked" {
			continue
		}
		for _, h := range hosts {
			if h == addr || h == knownhosts.Normalize(host) {
				t := pubKey.Type()
				if !seen[t] {
					seen[t] = true
					algos = append(algos, t)
				}
				break
			}
		}
	}
	return algos
}

// PrioritizedHostKeyAlgos 将 known_hosts 中已记录的主机密钥算法排到候选列表最前，
// 使 SSH 协商优先选择已信任的密钥类型，避免多 host key 服务器被误判为密钥变更。
// 保留其余算法作为兜底（首次连接或未记录时仍可正常协商）。
func PrioritizedHostKeyAlgos(host string, port int, all []string) []string {
	recorded := knownHostKeyAlgos(host, port)
	if len(recorded) == 0 {
		return all
	}
	// ssh-rsa（known_hosts 中的类型名）在协商时对应 rsa-sha2-256/512 等现代算法，一并前置。
	wanted := map[string]bool{}
	for _, t := range recorded {
		wanted[t] = true
		if t == ssh.KeyAlgoRSA {
			wanted[ssh.KeyAlgoRSASHA256] = true
			wanted[ssh.KeyAlgoRSASHA512] = true
		}
	}
	front := make([]string, 0, len(all))
	back := make([]string, 0, len(all))
	for _, algo := range all {
		if wanted[algo] {
			front = append(front, algo)
		} else {
			back = append(back, algo)
		}
	}
	return append(front, back...)
}

var (
	// ErrUnknownHostKey: known_hosts has no entry for the host.
	ErrUnknownHostKey = errors.New("host key not recorded in known_hosts")
	// ErrHostKeyChanged: known_hosts records a different (or revoked) key.
	ErrHostKeyChanged = errors.New("host key does not match known_hosts")
)

// checkKnownHost checks key against known_hosts without ever writing it.
// A missing known_hosts file counts as an unknown host.
func checkKnownHost(host string, port int, key ssh.PublicKey) error {
	knownHostsMu.Lock()
	defer knownHostsMu.Unlock()
	path, err := KnownHostsPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return ErrUnknownHostKey
	} else if err != nil {
		return fmt.Errorf("read known_hosts: %w", err)
	}
	callback, err := knownhosts.New(path)
	if err != nil {
		return fmt.Errorf("parse known_hosts: %w", err)
	}
	remote := net.JoinHostPort(host, fmt.Sprint(port))
	err = callback(remote, address(remote), key)
	if err == nil {
		return nil
	}
	var keyErr *knownhosts.KeyError
	if errors.As(err, &keyErr) {
		if len(keyErr.Want) == 0 {
			return ErrUnknownHostKey
		}
		return ErrHostKeyChanged
	}
	var revoked *knownhosts.RevokedError
	if errors.As(err, &revoked) {
		return ErrHostKeyChanged
	}
	return err
}

// StrictHostKeyCallback accepts only a key already recorded in known_hosts
// for host:port. Unknown hosts (ErrUnknownHostKey) and changed or revoked keys
// (ErrHostKeyChanged) are refused; known_hosts is never created or written.
// For unattended connections, where nobody can confirm a fingerprint.
func StrictHostKeyCallback(host string, port int) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		return checkKnownHost(host, port, key)
	}
}

// HasKnownHost reports whether known_hosts records any key for host:port
// (port 22 also matches a bare host entry), without connecting.
func HasKnownHost(host string, port int) (bool, error) {
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return false, err
	}
	probe, err := ssh.NewPublicKey(public)
	if err != nil {
		return false, err
	}
	// A fresh random key never matches, so any recorded key for the host
	// surfaces as a mismatch.
	switch err := checkKnownHost(host, port, probe); {
	case err == nil, errors.Is(err, ErrHostKeyChanged):
		return true, nil
	case errors.Is(err, ErrUnknownHostKey):
		return false, nil
	default:
		return false, err
	}
}
