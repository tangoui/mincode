package sshauth

import (
	"io"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// TestDialThroughSocksFullSSHChain 为端到端集成用例：
// 客户端经内存 SOCKS5 代理隧道连接真实 golang.org/x/crypto/ssh 服务端，
// 完成密钥交换、认证与会话执行，验证 DialThroughSocks 返回的连接可承载
// 完整 SSH 协议流量（terminal 与 sftp 的代理分支共用此链路）。
func TestDialThroughSocksFullSSHChain(t *testing.T) {
	// 1) 生成服务端主机密钥与客户端公钥认证材料。
	hostSigner, _, _ := testSigner(t)
	clientSigner, _, _ := testSigner(t)

	// 2) 启动真实 SSH 服务端。
	sshLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer sshLn.Close()
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		conn, err := sshLn.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		serverCfg := &ssh.ServerConfig{
			PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
				if string(key.Marshal()) == string(clientSigner.PublicKey().Marshal()) {
					return nil, nil
				}
				return nil, net.ErrClosed
			},
		}
		serverCfg.AddHostKey(hostSigner)
		srvConn, chans, reqs, err := ssh.NewServerConn(conn, serverCfg)
		if err != nil {
			return
		}
		defer srvConn.Close()
		go ssh.DiscardRequests(reqs)
		for newChannel := range chans {
			if newChannel.ChannelType() != "session" {
				_ = newChannel.Reject(ssh.UnknownChannelType, "unsupported")
				continue
			}
			channel, requests, err := newChannel.Accept()
			if err != nil {
				return
			}
			go func() {
				for req := range requests {
					if req.Type == "exec" && len(req.Payload) > 4 {
						_ = req.Reply(true, nil)
						_, _ = channel.Write([]byte("socks-chain-ok"))
						_, _ = channel.SendRequest("exit-status", false, []byte{0, 0, 0, 0})
						_ = channel.Close()
						return
					}
					_ = req.Reply(false, nil)
				}
			}()
		}
	}()

	// 3) 启动 SOCKS5 代理，把 CONNECT 流量桥接到 SSH 服务端。
	proxyLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer proxyLn.Close()
	go bridgeSocksProxyToTarget(t, proxyLn, sshLn.Addr().String())

	// 4) 客户端：经代理拨号，随后用 ssh.NewClientConn 完成完整 SSH 握手。
	sshAddr := sshLn.Addr().String()
	proxyPort := proxyLn.Addr().(*net.TCPAddr).Port
	conn, err := DialThroughSocks(SocksProxyConfig{Host: "127.0.0.1", Port: proxyPort}, sshAddr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	clientCfg := &ssh.ClientConfig{
		User:            "chain-test",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(clientSigner)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // 测试内自签主机密钥，仅验证链路
		Timeout:         5 * time.Second,
	}
	cc, chans, reqs, err := ssh.NewClientConn(conn, sshAddr, clientCfg)
	if err != nil {
		t.Fatal(err)
	}
	client := ssh.NewClient(cc, chans, reqs)
	defer client.Close()

	// 5) 在隧道内执行会话，验证数据面可用。
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	out, err := session.Output("echo chain")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "socks-chain-ok" {
		t.Fatalf("unexpected session output: %q", out)
	}
	// 先关闭客户端连接，服务端 goroutine 才会退出；defer 里的重复 Close 幂等安全。
	session.Close()
	client.Close()
	<-serverDone
}

// bridgeSocksProxyToTarget 为最小 SOCKS5 代理：完成无认证协商后，把 CONNECT
// 流量原样桥接到 targetAddr，模拟 ssh -D 动态转发行为。
func bridgeSocksProxyToTarget(t *testing.T, ln net.Listener, targetAddr string) {
	t.Helper()
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			head := make([]byte, 2)
			if _, err := io.ReadFull(c, head); err != nil {
				return
			}
			methods := make([]byte, int(head[1]))
			if _, err := io.ReadFull(c, methods); err != nil {
				return
			}
			if _, err := c.Write([]byte{0x05, 0x00}); err != nil {
				return
			}
			reqHead := make([]byte, 4)
			if _, err := io.ReadFull(c, reqHead); err != nil {
				return
			}
			switch reqHead[3] {
			case 0x01:
				if _, err := io.ReadFull(c, make([]byte, 4)); err != nil {
					return
				}
			case 0x04:
				if _, err := io.ReadFull(c, make([]byte, 16)); err != nil {
					return
				}
			case 0x03:
				lenByte := make([]byte, 1)
				if _, err := io.ReadFull(c, lenByte); err != nil {
					return
				}
				if _, err := io.ReadFull(c, make([]byte, int(lenByte[0]))); err != nil {
					return
				}
			default:
				return
			}
			if _, err := io.ReadFull(c, make([]byte, 2)); err != nil {
				return
			}
			upstream, err := net.DialTimeout("tcp", targetAddr, 5*time.Second)
			if err != nil {
				_, _ = c.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
				return
			}
			defer upstream.Close()
			if _, err := c.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
				return
			}
			done := make(chan struct{}, 2)
			go func() { _, _ = io.Copy(upstream, c); done <- struct{}{} }()
			go func() { _, _ = io.Copy(c, upstream); done <- struct{}{} }()
			<-done
		}(conn)
	}
}
