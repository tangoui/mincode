package sshauth

import (
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// socksMockServer 为测试用最小 SOCKS5 服务端：支持无认证/用户名密码协商、
// 记录 CONNECT 目标、可配置拒绝应答，成功后回显流量供断言连接可用性。
type socksMockServer struct {
	ln           net.Listener
	wantUser     string
	wantPassword string
	rejectReply  byte
	lastAtyp     byte
	lastHost     string
	lastPort     uint16
}

func startSocksMockServer(t *testing.T) *socksMockServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &socksMockServer{ln: ln}
	go s.serve()
	t.Cleanup(func() { ln.Close() })
	return s
}

func (s *socksMockServer) port() int { return s.ln.Addr().(*net.TCPAddr).Port }

func (s *socksMockServer) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			if err := s.handle(c); err != nil {
				return
			}
			buf := make([]byte, 64)
			for {
				n, err := c.Read(buf)
				if err != nil {
					return
				}
				if _, err := c.Write(buf[:n]); err != nil {
					return
				}
			}
		}(conn)
	}
}

func (s *socksMockServer) handle(conn net.Conn) error {
	head := make([]byte, 2)
	if _, err := io.ReadFull(conn, head); err != nil {
		return err
	}
	if head[0] != 0x05 {
		return errors.New("unexpected version")
	}
	methods := make([]byte, int(head[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return err
	}
	has := func(m byte) bool {
		for _, v := range methods {
			if v == m {
				return true
			}
		}
		return false
	}
	if s.wantUser != "" {
		if !has(0x02) {
			_, err := conn.Write([]byte{0x05, 0xFF})
			return err
		}
		if _, err := conn.Write([]byte{0x05, 0x02}); err != nil {
			return err
		}
		authHead := make([]byte, 2)
		if _, err := io.ReadFull(conn, authHead); err != nil {
			return err
		}
		user := make([]byte, int(authHead[1]))
		if _, err := io.ReadFull(conn, user); err != nil {
			return err
		}
		plenByte := make([]byte, 1)
		if _, err := io.ReadFull(conn, plenByte); err != nil {
			return err
		}
		pass := make([]byte, int(plenByte[0]))
		if _, err := io.ReadFull(conn, pass); err != nil {
			return err
		}
		if string(user) != s.wantUser || string(pass) != s.wantPassword {
			_, err := conn.Write([]byte{0x01, 0x01})
			return err
		}
		if _, err := conn.Write([]byte{0x01, 0x00}); err != nil {
			return err
		}
	} else {
		if !has(0x00) {
			_, err := conn.Write([]byte{0x05, 0xFF})
			return err
		}
		if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
			return err
		}
	}

	reqHead := make([]byte, 4)
	if _, err := io.ReadFull(conn, reqHead); err != nil {
		return err
	}
	if reqHead[1] != 0x01 {
		return errors.New("only CONNECT supported")
	}
	s.lastAtyp = reqHead[3]
	switch reqHead[3] {
	case 0x01:
		addr := make([]byte, 4)
		if _, err := io.ReadFull(conn, addr); err != nil {
			return err
		}
		s.lastHost = net.IP(addr).String()
	case 0x04:
		addr := make([]byte, 16)
		if _, err := io.ReadFull(conn, addr); err != nil {
			return err
		}
		s.lastHost = net.IP(addr).String()
	case 0x03:
		lenByte := make([]byte, 1)
		if _, err := io.ReadFull(conn, lenByte); err != nil {
			return err
		}
		host := make([]byte, int(lenByte[0]))
		if _, err := io.ReadFull(conn, host); err != nil {
			return err
		}
		s.lastHost = string(host)
	default:
		return errors.New("unsupported atyp")
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(conn, portBytes); err != nil {
		return err
	}
	s.lastPort = uint16(portBytes[0])<<8 | uint16(portBytes[1])

	reply := s.rejectReply
	// 应答附带 IPv4 绑定地址，验证客户端能正确跳过。
	_, err := conn.Write([]byte{0x05, reply, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	return err
}

func waitLastHost(t *testing.T, s *socksMockServer) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.lastHost != "" {
			return s.lastHost
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("mock server did not record CONNECT target")
	return ""
}

func TestDialThroughSocksNoAuth(t *testing.T) {
	s := startSocksMockServer(t)
	conn, err := DialThroughSocks(SocksProxyConfig{Host: "127.0.0.1", Port: s.port()}, "example.com:22", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if got := waitLastHost(t, s); got != "example.com" {
		t.Fatalf("want domain example.com, got %q", got)
	}
	if s.lastAtyp != 0x03 {
		t.Fatalf("want atyp domain(0x03), got %#x", s.lastAtyp)
	}
	if s.lastPort != 22 {
		t.Fatalf("want port 22, got %d", s.lastPort)
	}
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "ping" {
		t.Fatalf("want echo ping, got %q", buf)
	}
}

func TestDialThroughSocksIPv4Target(t *testing.T) {
	s := startSocksMockServer(t)
	conn, err := DialThroughSocks(SocksProxyConfig{Host: "127.0.0.1", Port: s.port()}, "10.0.0.7:2222", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if got := waitLastHost(t, s); got != "10.0.0.7" {
		t.Fatalf("want 10.0.0.7, got %q", got)
	}
	if s.lastAtyp != 0x01 {
		t.Fatalf("want atyp ipv4(0x01), got %#x", s.lastAtyp)
	}
	if s.lastPort != 2222 {
		t.Fatalf("want port 2222, got %d", s.lastPort)
	}
}

func TestDialThroughSocksUserPassword(t *testing.T) {
	s := startSocksMockServer(t)
	s.wantUser = "proxy-user"
	s.wantPassword = "proxy-pass"
	conn, err := DialThroughSocks(
		SocksProxyConfig{Host: "127.0.0.1", Port: s.port(), Username: "proxy-user", Password: "proxy-pass"},
		"example.com:22", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	waitLastHost(t, s)
}

func TestDialThroughSocksAuthFailure(t *testing.T) {
	s := startSocksMockServer(t)
	s.wantUser = "proxy-user"
	s.wantPassword = "correct"
	_, err := DialThroughSocks(
		SocksProxyConfig{Host: "127.0.0.1", Port: s.port(), Username: "proxy-user", Password: "wrong"},
		"example.com:22", time.Second)
	if err == nil {
		t.Fatal("want auth failure")
	}
	if !strings.Contains(err.Error(), "代理认证失败") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDialThroughSocksNoCredentialOffered(t *testing.T) {
	s := startSocksMockServer(t)
	s.wantUser = "proxy-user"
	s.wantPassword = "correct"
	// 服务端要求认证但客户端未配置凭据：greeting 只带无认证方法，服务端拒绝。
	_, err := DialThroughSocks(SocksProxyConfig{Host: "127.0.0.1", Port: s.port()}, "example.com:22", time.Second)
	if err == nil {
		t.Fatal("want method mismatch failure")
	}
	if !strings.Contains(err.Error(), "未配置代理凭据") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDialThroughSocksReplyRejected(t *testing.T) {
	s := startSocksMockServer(t)
	s.rejectReply = 0x05 // connection refused
	_, err := DialThroughSocks(SocksProxyConfig{Host: "127.0.0.1", Port: s.port()}, "example.com:22", time.Second)
	if err == nil {
		t.Fatal("want rejected reply")
	}
	if !strings.Contains(err.Error(), "连接被拒绝") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDialThroughSocksProxyUnreachable(t *testing.T) {
	// 占用后立即释放端口，确保无监听者。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	_, err = DialThroughSocks(SocksProxyConfig{Host: "127.0.0.1", Port: port}, "example.com:22", time.Second)
	if err == nil {
		t.Fatal("want unreachable proxy failure")
	}
}

func TestSocksProxyConfigValidate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     SocksProxyConfig
		wantErr bool
	}{
		{name: "valid", cfg: SocksProxyConfig{Host: "127.0.0.1", Port: 1080}, wantErr: false},
		{name: "empty host", cfg: SocksProxyConfig{Port: 1080}, wantErr: true},
		{name: "zero port", cfg: SocksProxyConfig{Host: "127.0.0.1"}, wantErr: true},
		{name: "port too large", cfg: SocksProxyConfig{Host: "127.0.0.1", Port: 70000}, wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.cfg.Validate()
			if c.wantErr && err == nil {
				t.Fatal("want error")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c.wantErr && err != nil && strings.Contains(err.Error(), "proxy-pass") {
				t.Fatal("error must not leak credentials")
			}
		})
	}
	if !(SocksProxyConfig{Host: "127.0.0.1", Port: 1080}).Enabled() {
		t.Fatal("config should be enabled")
	}
	if (SocksProxyConfig{}).Enabled() {
		t.Fatal("zero config must be disabled")
	}
}
