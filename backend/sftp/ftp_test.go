package sftp

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeFTPServer struct {
	listener net.Listener
	user     string
	password string
	tls      *tls.Config
	roots    *x509.CertPool

	mu       sync.Mutex
	files    map[string][]byte
	dirs     map[string]bool
	dataHits int
}

func newFakeFTPServer(t *testing.T, user, password string, explicitTLS bool) *fakeFTPServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &fakeFTPServer{
		listener: listener,
		user:     user,
		password: password,
		files:    map[string][]byte{"/seed.txt": []byte("seed")},
		dirs:     map[string]bool{"/": true},
	}
	if explicitTLS {
		server.tls, server.roots = fakeFTPServerTLS(t)
	}
	go server.serve()
	t.Cleanup(func() { _ = listener.Close() })
	return server
}

func fakeFTPServerTLS(t *testing.T) (*tls.Config, *x509.CertPool) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		DNSNames:     []string{"localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(certPEM)
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, roots
}

func (s *fakeFTPServer) addr() (string, int) {
	host, portText, _ := net.SplitHostPort(s.listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	return host, port
}

func (s *fakeFTPServer) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.serveControl(conn)
	}
}

func (s *fakeFTPServer) serveControl(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	write := func(format string, values ...any) {
		_, _ = fmt.Fprintf(writer, format+"\r\n", values...)
		_ = writer.Flush()
	}
	write("220 fake ftp")
	var dataListener net.Listener
	var renameFrom string
	var restartOffset int
	loggedIn := false
	protected := false
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimSpace(line)
		command, argument, _ := strings.Cut(line, " ")
		command = strings.ToUpper(command)
		switch command {
		case "AUTH":
			if s.tls == nil {
				write("502 no tls")
				continue
			}
			write("234 tls ready")
			tlsConn := tls.Server(conn, s.tls)
			if tlsConn.Handshake() != nil {
				return
			}
			conn, reader, writer = tlsConn, bufio.NewReader(tlsConn), bufio.NewWriter(tlsConn)
		case "USER":
			if argument != s.user {
				write("530 bad user")
			} else {
				write("331 password")
			}
		case "PASS":
			if argument != s.password {
				write("530 bad password")
			} else {
				loggedIn = true
				write("230 logged in")
			}
		case "FEAT":
			write("211-Features")
			write(" MLST type*;size*;modify*;")
			write(" UTF8")
			write("211 End")
		case "TYPE", "OPTS", "PBSZ":
			write("200 ok")
		case "PROT":
			protected = argument == "P"
			write("200 ok")
		case "PWD":
			write("257 \"/\"")
		case "EPSV":
			if dataListener != nil {
				_ = dataListener.Close()
			}
			dataListener, _ = net.Listen("tcp", "127.0.0.1:0")
			_, portText, _ := net.SplitHostPort(dataListener.Addr().String())
			write("229 Entering Extended Passive Mode (|||%s|)", portText)
		case "MLST":
			entry, ok := s.entry(argument)
			if !ok {
				write("550 missing")
			} else {
				write("250-File details")
				write(" %s", entry)
				write("250 End")
			}
		case "SIZE":
			s.mu.Lock()
			data, ok := s.files[path.Clean(argument)]
			s.mu.Unlock()
			if !ok {
				write("550 missing")
			} else {
				write("213 %d", len(data))
			}
		case "MLSD", "LIST":
			if !loggedIn || dataListener == nil {
				write("425 unavailable")
				continue
			}
			write("150 opening data")
			dataConn, acceptErr := dataListener.Accept()
			if acceptErr == nil {
				if protected {
					dataConn = tls.Server(dataConn, s.tls)
				}
				s.recordDataHit()
				_, _ = io.WriteString(dataConn, s.list(argument))
				_ = dataConn.Close()
			}
			_ = dataListener.Close()
			dataListener = nil
			write("226 done")
		case "REST":
			restartOffset, _ = strconv.Atoi(argument)
			write("350 restart")
		case "RETR", "STOR":
			if dataListener == nil {
				write("425 unavailable")
				continue
			}
			write("150 opening data")
			dataConn, acceptErr := dataListener.Accept()
			if acceptErr == nil {
				if protected {
					dataConn = tls.Server(dataConn, s.tls)
				}
				s.recordDataHit()
				clean := path.Clean(argument)
				if command == "RETR" {
					s.mu.Lock()
					payload := append([]byte(nil), s.files[clean]...)
					s.mu.Unlock()
					if restartOffset <= len(payload) {
						payload = payload[restartOffset:]
					}
					_, _ = dataConn.Write(payload)
				} else {
					payload, _ := io.ReadAll(dataConn)
					s.mu.Lock()
					if restartOffset > 0 && restartOffset <= len(s.files[clean]) {
						payload = append(append([]byte(nil), s.files[clean][:restartOffset]...), payload...)
					}
					s.files[clean] = payload
					s.mu.Unlock()
				}
				_ = dataConn.Close()
			}
			restartOffset = 0
			_ = dataListener.Close()
			dataListener = nil
			write("226 done")
		case "MKD":
			s.mu.Lock()
			s.dirs[path.Clean(argument)] = true
			s.mu.Unlock()
			write("257 created")
		case "DELE":
			s.mu.Lock()
			delete(s.files, path.Clean(argument))
			s.mu.Unlock()
			write("250 deleted")
		case "RMD":
			s.mu.Lock()
			delete(s.dirs, path.Clean(argument))
			s.mu.Unlock()
			write("250 removed")
		case "RNFR":
			renameFrom = path.Clean(argument)
			write("350 ready")
		case "RNTO":
			s.mu.Lock()
			if data, ok := s.files[renameFrom]; ok {
				delete(s.files, renameFrom)
				s.files[path.Clean(argument)] = data
			}
			s.mu.Unlock()
			write("250 renamed")
		case "QUIT":
			write("221 bye")
			return
		default:
			write("502 unsupported")
		}
	}
}

func (s *fakeFTPServer) recordDataHit() {
	s.mu.Lock()
	s.dataHits++
	s.mu.Unlock()
}

func (s *fakeFTPServer) entry(name string) (string, bool) {
	clean := path.Clean(name)
	s.mu.Lock()
	defer s.mu.Unlock()
	if data, ok := s.files[clean]; ok {
		return fmt.Sprintf("type=file;size=%d;modify=20260101000000; %s", len(data), path.Base(clean)), true
	}
	if s.dirs[clean] {
		return fmt.Sprintf("type=dir;size=0;modify=20260101000000; %s", path.Base(clean)), true
	}
	return "", false
}

func (s *fakeFTPServer) list(dir string) string {
	cleanDir := path.Clean(dir)
	if cleanDir == "." {
		cleanDir = "/"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var output strings.Builder
	for name, data := range s.files {
		if path.Dir(name) == cleanDir {
			fmt.Fprintf(&output, "type=file;size=%d;modify=20260101000000; %s\r\n", len(data), path.Base(name))
		}
	}
	for name := range s.dirs {
		if name != cleanDir && path.Dir(name) == cleanDir {
			fmt.Fprintf(&output, "type=dir;size=0;modify=20260101000000; %s\r\n", path.Base(name))
		}
	}
	return output.String()
}

func TestFTPAdapterIndependentPasswordDataAndCRUD(t *testing.T) {
	server := newFakeFTPServer(t, "ftp-user", "ftp-secret", false)
	host, port := server.addr()
	client, err := dialFTP(ftpDialParams{protocol: "ftp", host: host, port: port, username: "ftp-user", password: "ftp-secret"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	if err := client.Mkdir("/work"); err != nil {
		t.Fatal(err)
	}
	file, err := client.Create("/work/item.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("payload")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := client.Stat("/work/item.txt")
	if err != nil || info.Size() != 7 {
		t.Fatalf("stat failed: info=%v err=%v", info, err)
	}
	reader, err := client.Open("/work/item.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Seek(2, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	content, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(content, []byte("yload")) {
		t.Fatalf("unexpected content %q", content)
	}
	if err := client.Rename("/work/item.txt", "/work/renamed.txt"); err != nil {
		t.Fatal(err)
	}
	entries, err := client.ReadDir("/work")
	if err != nil || len(entries) != 1 || entries[0].Name() != "renamed.txt" {
		t.Fatalf("unexpected entries %#v: %v", entries, err)
	}
	if err := client.Remove("/work/renamed.txt"); err != nil {
		t.Fatal(err)
	}
	if err := client.RemoveDirectory("/work"); err != nil {
		t.Fatal(err)
	}
	if client.CanChmod() || !errors.Is(client.Chmod("/seed.txt", 0o600), errors.ErrUnsupported) {
		t.Fatal("FTP 不应伪造 chmod 支持")
	}
	server.mu.Lock()
	dataHits := server.dataHits
	server.mu.Unlock()
	if dataHits < 3 {
		t.Fatalf("expected control-independent data connections, got %d", dataHits)
	}
}

func TestFTPInvalidParametersAndPassword(t *testing.T) {
	if _, err := dialFTP(ftpDialParams{protocol: "ftp", username: "u"}); err == nil {
		t.Fatal("empty host should fail")
	}
	server := newFakeFTPServer(t, "u", "separate", false)
	host, port := server.addr()
	if _, err := dialFTP(ftpDialParams{protocol: "ftp", host: host, port: port, username: "u", password: "ssh-password"}); err == nil {
		t.Fatal("wrong FTP password should fail")
	}
}

func TestExplicitFTPSValidatesCertificateAndDataChannel(t *testing.T) {
	server := newFakeFTPServer(t, "u", "p", true)
	host, port := server.addr()
	if _, err := dialFTP(ftpDialParams{protocol: "ftps-explicit", host: host, port: port, username: "u", password: "p", tlsServerName: "localhost"}); err == nil {
		t.Fatal("FTPS must reject an untrusted server certificate")
	}
	client, err := dialFTP(ftpDialParams{
		protocol: "ftps-explicit", host: host, port: port, username: "u", password: "p",
		tlsServerName: "localhost", tlsConfig: &tls.Config{RootCAs: server.roots, MinVersion: tls.VersionTLS12},
	})
	if err != nil {
		t.Fatal(err)
	}
	file, err := client.Open("/seed.txt")
	if err != nil {
		t.Fatal(err)
	}
	content, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if string(content) != "seed" {
		t.Fatalf("unexpected FTPS content %q", content)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestFTPRemoteFileCloseReportsServerError(t *testing.T) {
	writer := &ftpRemoteFile{writable: true, path: "/x", done: make(chan error, 1), pipeW: nil}
	reader, pipeWriter := io.Pipe()
	writer.pipeW = pipeWriter
	writer.done <- fmt.Errorf("server completion failed")
	close(writer.done)
	_ = reader.Close()
	if err := writer.Close(); err == nil || !strings.Contains(err.Error(), "server completion") {
		t.Fatalf("close should report completion error, got %v", err)
	}
}

func TestFTPRemoteFileRejectsSeekAfterTransferStarts(t *testing.T) {
	file := &ftpRemoteFile{readable: true, done: make(chan error)}
	if _, err := file.Seek(1, io.SeekStart); err == nil {
		t.Fatal("seek after transfer start should fail")
	}
	if _, err := (&ftpClientAdapter{}).OpenFile("bad\npath", os.O_WRONLY); err == nil {
		t.Fatal("command injection path should fail")
	}
}
