package sshauth

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

// SocksProxyConfig 为 SOCKS5 出站代理配置：SSH 的第一跳 TCP 连接先经该代理
// 建立，再在代理隧道内拨目标地址。用户名/密码遵循 RFC 1929，均为可选。
type SocksProxyConfig struct {
	Host     string
	Port     int
	Username string
	Password string
}

const (
	socksVersion     byte = 0x05
	socksAuthNone    byte = 0x00
	socksAuthUserPwd byte = 0x02
	socksAuthNoMatch byte = 0xFF

	socksCmdConnect byte = 0x01

	socksAtypIPv4   byte = 0x01
	socksAtypDomain byte = 0x03
	socksAtypIPv6   byte = 0x04

	socksUserAuthVersion byte = 0x01
)

// socksReplyText 为 RFC 1928 应答码的中文说明，用于生成可读错误。
var socksReplyText = map[byte]string{
	0x01: "常规失败",
	0x02: "规则不允许",
	0x03: "网络不可达",
	0x04: "主机不可达",
	0x05: "连接被拒绝",
	0x06: "TTL 过期",
	0x07: "不支持的命令",
	0x08: "不支持的地址类型",
}

// Enabled 判断配置是否构成可用代理：host 非空且端口合法。
func (c SocksProxyConfig) Enabled() bool {
	return c.Host != "" && c.Port > 0 && c.Port <= 65535
}

// Addr 返回代理的拨号地址。
func (c SocksProxyConfig) Addr() string {
	return net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}

// Validate 在 Go 边界校验代理配置；错误信息不包含用户名/密码。
func (c SocksProxyConfig) Validate() error {
	if c.Host == "" {
		return fmt.Errorf("SOCKS5 代理地址不能为空")
	}
	if c.Port <= 0 || c.Port > 65535 {
		return fmt.Errorf("SOCKS5 代理端口非法: %d", c.Port)
	}
	if len(c.Username) > 255 || len(c.Password) > 255 {
		return fmt.Errorf("SOCKS5 代理用户名或密码超出 RFC 1929 长度上限")
	}
	return nil
}

// DialThroughSocks 经 SOCKS5 代理建立到 targetAddr 的 TCP 连接。
// timeout 同时约束 TCP 拨号与 SOCKS 握手；握手成功后清除 deadline，
// 与 openTerminalSSHClient 对 SSH 握手的 deadline 管理模式保持一致。
func DialThroughSocks(proxy SocksProxyConfig, targetAddr string, timeout time.Duration) (net.Conn, error) {
	if err := proxy.Validate(); err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout("tcp", proxy.Addr(), timeout)
	if err != nil {
		return nil, fmt.Errorf("经 SOCKS5 代理连接失败（代理 %s）: %w", proxy.Addr(), err)
	}
	if timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}
	if err := socksHandshake(conn, proxy, targetAddr); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("经 SOCKS5 代理连接失败（代理 %s）: %w", proxy.Addr(), err)
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

func socksHandshake(conn net.Conn, proxy SocksProxyConfig, targetAddr string) error {
	if err := socksNegotiateMethod(conn, proxy); err != nil {
		return err
	}
	if err := socksSendConnect(conn, targetAddr); err != nil {
		return err
	}
	return socksReadReply(conn)
}

// socksNegotiateMethod 发送 greeting 并按服务端选择完成认证协商。
// 配置了用户名时提供 RFC 1929 与无认证两种方法，否则仅提供无认证。
func socksNegotiateMethod(conn net.Conn, proxy SocksProxyConfig) error {
	methods := []byte{socksAuthNone}
	if proxy.Username != "" {
		methods = append(methods, socksAuthUserPwd)
	}
	greeting := make([]byte, 0, 2+len(methods))
	greeting = append(greeting, socksVersion, byte(len(methods)))
	greeting = append(greeting, methods...)
	if _, err := conn.Write(greeting); err != nil {
		return fmt.Errorf("代理协商写入失败: %w", err)
	}
	head := make([]byte, 2)
	if _, err := io.ReadFull(conn, head); err != nil {
		return fmt.Errorf("代理协商读取失败: %w", err)
	}
	if head[0] != socksVersion {
		return fmt.Errorf("代理返回非 SOCKS5 版本: %#x", head[0])
	}
	switch head[1] {
	case socksAuthNone:
		return nil
	case socksAuthUserPwd:
		return socksUserPassAuth(conn, proxy.Username, proxy.Password)
	case socksAuthNoMatch:
		if proxy.Username != "" {
			return fmt.Errorf("代理不接受提供的认证方法（用户名/密码与无认证均被拒绝）")
		}
		return fmt.Errorf("代理要求认证，但本连接未配置代理凭据")
	default:
		return fmt.Errorf("代理返回不支持的认证方法: %#x", head[1])
	}
}

// socksUserPassAuth 执行 RFC 1929 用户名/密码子协商。
func socksUserPassAuth(conn net.Conn, username, password string) error {
	buf := make([]byte, 0, 3+len(username)+len(password))
	buf = append(buf, socksUserAuthVersion, byte(len(username)))
	buf = append(buf, username...)
	buf = append(buf, byte(len(password)))
	buf = append(buf, password...)
	if _, err := conn.Write(buf); err != nil {
		return fmt.Errorf("代理认证写入失败: %w", err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return fmt.Errorf("代理认证读取失败: %w", err)
	}
	if reply[1] != 0x00 {
		return fmt.Errorf("代理认证失败（用户名或密码不正确）")
	}
	return nil
}

// socksSendConnect 发送 CONNECT 请求；按目标 host 自动选择
// IPv4/域名/IPv6 地址类型。
func socksSendConnect(conn net.Conn, targetAddr string) error {
	host, portStr, err := net.SplitHostPort(targetAddr)
	if err != nil {
		return fmt.Errorf("目标地址非法: %w", err)
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return fmt.Errorf("目标端口非法: %w", err)
	}
	req := []byte{socksVersion, socksCmdConnect, 0x00}
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			req = append(req, socksAtypIPv4)
			req = append(req, v4...)
		} else {
			req = append(req, socksAtypIPv6)
			req = append(req, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return fmt.Errorf("目标域名过长")
		}
		req = append(req, socksAtypDomain, byte(len(host)))
		req = append(req, host...)
	}
	var portBytes [2]byte
	binary.BigEndian.PutUint16(portBytes[:], uint16(port))
	req = append(req, portBytes[:]...)
	if _, err := conn.Write(req); err != nil {
		return fmt.Errorf("代理 CONNECT 写入失败: %w", err)
	}
	return nil
}

// socksReadReply 解析 CONNECT 应答并丢弃绑定地址；应答码非 0 时返回中文错误。
func socksReadReply(conn net.Conn) error {
	head := make([]byte, 4)
	if _, err := io.ReadFull(conn, head); err != nil {
		return fmt.Errorf("代理应答读取失败: %w", err)
	}
	if head[0] != socksVersion {
		return fmt.Errorf("代理应答非 SOCKS5 版本: %#x", head[0])
	}
	if head[1] != 0x00 {
		text, ok := socksReplyText[head[1]]
		if !ok {
			text = "未知错误"
		}
		return fmt.Errorf("代理拒绝连接目标（应答码 %#x: %s）", head[1], text)
	}
	var skip int
	switch head[3] {
	case socksAtypIPv4:
		skip = 4
	case socksAtypIPv6:
		skip = 16
	case socksAtypDomain:
		lenByte := make([]byte, 1)
		if _, err := io.ReadFull(conn, lenByte); err != nil {
			return fmt.Errorf("代理应答读取失败: %w", err)
		}
		skip = int(lenByte[0])
	default:
		return fmt.Errorf("代理应答包含不支持的地址类型: %#x", head[3])
	}
	if _, err := io.ReadFull(conn, make([]byte, skip+2)); err != nil {
		return fmt.Errorf("代理应答读取失败: %w", err)
	}
	return nil
}
