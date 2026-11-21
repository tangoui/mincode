package db

import (
	"crypto/tls"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

func TestNewMySQLConfigSSLIsStrict(t *testing.T) {
	user := "root"
	cfg := &DbConnectionConfig{DbType: "mysql", Host: "db.example.com", Port: 3307, Username: &user, SSL: true}
	mc := newMySQLConfig(cfg)
	if mc.TLS == nil {
		t.Fatal("SSL enabled must configure TLS")
	}
	if mc.TLS.InsecureSkipVerify {
		t.Fatal("SSL must verify the server certificate")
	}
	if mc.AllowFallbackToPlaintext {
		t.Fatal("SSL must not fall back to plaintext")
	}
	if mc.TLS.ServerName != "db.example.com" {
		t.Fatalf("ServerName = %q", mc.TLS.ServerName)
	}
	if mc.TLSConfig == "preferred" || mc.TLSConfig == "skip-verify" {
		t.Fatalf("insecure TLS mode %q", mc.TLSConfig)
	}
	if mc.Timeout <= 0 {
		t.Fatal("dial timeout must be set")
	}

	ipv6 := newMySQLConfig(&DbConnectionConfig{Host: "[::1]", SSL: true})
	if ipv6.TLS.ServerName != "::1" {
		t.Fatalf("IPv6 ServerName = %q", ipv6.TLS.ServerName)
	}

	plain := newMySQLConfig(&DbConnectionConfig{Host: "localhost"})
	if plain.TLS != nil || plain.TLSConfig != "" {
		t.Fatal("SSL disabled must not configure TLS")
	}
}

// fakeMySQLHandshake builds a minimal protocol-10 initial handshake packet.
func fakeMySQLHandshake(withSSL bool) []byte {
	const (
		clientProtocol41   = 0x0200
		clientSSL          = 0x0800
		clientSecureConn   = 0x8000
		clientPluginAuthHi = 0x0008 // 1<<19 in the upper 16 bits
	)
	lower := uint16(clientProtocol41 | clientSecureConn)
	if withSSL {
		lower |= clientSSL
	}
	p := []byte{10}
	p = append(p, "5.7.0-fake\x00"...)
	p = append(p, 1, 0, 0, 0)    // connection id
	p = append(p, "abcdefgh"...) // auth-plugin-data part 1
	p = append(p, 0)             // filler
	p = binary.LittleEndian.AppendUint16(p, lower)
	p = append(p, 45)   // charset
	p = append(p, 2, 0) // status
	p = binary.LittleEndian.AppendUint16(p, clientPluginAuthHi)
	p = append(p, 21)                  // auth data len
	p = append(p, make([]byte, 10)...) // reserved
	p = append(p, "ijklmnopqrst\x00"...)
	p = append(p, "mysql_native_password\x00"...)

	hdr := []byte{byte(len(p)), byte(len(p) >> 8), byte(len(p) >> 16), 0}
	return append(hdr, p...)
}

func listenFake(t *testing.T, serve func(net.Conn)) (string, int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				serve(conn)
			}()
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port
}

func TestMySQLSSLRejectsPlaintextDowngrade(t *testing.T) {
	plaintextAuth := make(chan struct{}, 4)
	host, port := listenFake(t, func(conn net.Conn) {
		_, _ = conn.Write(fakeMySQLHandshake(false))
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 512)
		if n, _ := conn.Read(buf); n > 0 {
			plaintextAuth <- struct{}{} // client sent credentials in clear text
		}
	})
	user, pass := "root", "s3cret"
	_, err := buildMySQLSession(&DbConnectionConfig{Host: host, Port: port, Username: &user, Password: &pass, SSL: true})
	if !errors.Is(err, mysql.ErrNoTLS) {
		t.Fatalf("server without TLS must be rejected with ErrNoTLS, got %v", err)
	}
	select {
	case <-plaintextAuth:
		t.Fatal("credentials must not be sent over plaintext")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestMySQLSSLRejectsUntrustedCertificate(t *testing.T) {
	// httptest's TLS server provides a self-signed certificate for 127.0.0.1.
	certSrv := httptest.NewTLSServer(nil)
	cert := certSrv.TLS.Certificates[0]
	certSrv.Close()

	host, port := listenFake(t, func(conn net.Conn) {
		_, _ = conn.Write(fakeMySQLHandshake(true))
		// SSLRequest packet: 4-byte header + 32-byte payload.
		if _, err := io.ReadFull(conn, make([]byte, 36)); err != nil {
			return
		}
		tlsConn := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}})
		_ = tlsConn.SetDeadline(time.Now().Add(2 * time.Second))
		_ = tlsConn.Handshake()
	})
	user := "root"
	_, err := buildMySQLSession(&DbConnectionConfig{Host: host, Port: port, Username: &user, SSL: true})
	if err == nil || !strings.Contains(err.Error(), "证书校验失败") {
		t.Fatalf("self-signed certificate must be rejected, got %v", err)
	}
}

func TestMySQLConnectTimesOutOnSilentServer(t *testing.T) {
	old := mysqlConnectTimeout
	mysqlConnectTimeout = 300 * time.Millisecond
	t.Cleanup(func() { mysqlConnectTimeout = old })

	host, port := listenFake(t, func(conn net.Conn) {
		_, _ = io.Copy(io.Discard, conn) // accept but never send the handshake
	})
	start := time.Now()
	_, err := buildMySQLSession(&DbConnectionConfig{Host: host, Port: port})
	elapsed := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "超时") {
		t.Fatalf("silent server must time out, got %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("connect should be bounded by the deadline, took %s", elapsed)
	}
}
