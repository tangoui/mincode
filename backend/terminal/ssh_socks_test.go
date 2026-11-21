package terminal

import (
	"net"
	"testing"
	"time"

	"mincode/app/backend/sshauth"
)

func TestBuildFirstHopDialerDirectSkipsProxyStage(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			c.Close()
		}
	}()

	var events []sshConnectionProgressPayload
	diagnostics := newSSHConnectionDiagnosticsWithEmitter("socks-direct", func(name string, data any) {
		if payload, ok := data.(sshConnectionProgressPayload); ok && name == sshConnectionProgressEvent {
			events = append(events, payload)
		}
	})
	dial := buildFirstHopDialer(nil, ln.Addr().String(), time.Second, diagnostics, sshConnectionHopTarget)
	conn, err := dial()
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	for _, event := range events {
		if event.Stage == sshConnectionStageProxy {
			t.Fatal("direct dial must not record proxy stage")
		}
	}
}

func TestBuildFirstHopDialerProxyFailureRecordsProxyStage(t *testing.T) {
	// 占用后立即释放端口，保证代理不可达：拨号必须走代理路径并失败。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxyPort := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	var events []sshConnectionProgressPayload
	diagnostics := newSSHConnectionDiagnosticsWithEmitter("socks-proxy", func(name string, data any) {
		if payload, ok := data.(sshConnectionProgressPayload); ok && name == sshConnectionProgressEvent {
			events = append(events, payload)
		}
	})
	proxy := &sshauth.SocksProxyConfig{Host: "127.0.0.1", Port: proxyPort}
	dial := buildFirstHopDialer(proxy, "203.0.113.10:22", time.Second, diagnostics, sshConnectionHopTarget)
	if _, err := dial(); err == nil {
		t.Fatal("want proxy dial failure")
	}

	var sawProxyBegin, sawProxyFailed bool
	for _, event := range events {
		if event.Stage != sshConnectionStageProxy {
			continue
		}
		if event.State == sshConnectionStateStarted {
			sawProxyBegin = true
		}
		if event.State == sshConnectionStateFailed && event.ErrorCode == "proxy" {
			sawProxyFailed = true
		}
	}
	if !sawProxyBegin || !sawProxyFailed {
		t.Fatalf("want proxy started+failed events, got %+v", events)
	}
}

func TestTerminalSocksConfigNilIsDirect(t *testing.T) {
	cfg, err := terminalSocksConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg != nil {
		t.Fatal("nil payload must mean direct connection")
	}
}

func TestTerminalSocksConfigPlainValuesPassThrough(t *testing.T) {
	username := "proxy-user"
	password := "proxy-pass"
	cfg, err := terminalSocksConfig(&SocksProxyPayload{
		Host:     " 127.0.0.1 ",
		Port:     1080,
		Username: &username,
		Password: &password,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil {
		t.Fatal("want proxy config")
	}
	if cfg.Host != "127.0.0.1" || cfg.Port != 1080 || cfg.Username != "proxy-user" || cfg.Password != "proxy-pass" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestTerminalSocksConfigInvalidRejected(t *testing.T) {
	// 配置了代理但缺 host：必须报错而非静默直连。
	if _, err := terminalSocksConfig(&SocksProxyPayload{Port: 1080}); err == nil {
		t.Fatal("want validation error for missing proxy host")
	}
}
