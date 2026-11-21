package sftp

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	pkgsftp "github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	appcrypto "mincode/app/backend/crypto"
)

// useSavedStore isolates HOME (known_hosts, machine key salt) and replaces
// app_kv with an in-memory map for one test.
func useSavedStore(t *testing.T) (home string, kv map[string][]byte) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("APPDATA", filepath.Join(home, "data"))
	kv = map[string][]byte{}
	var mu sync.Mutex
	previous := savedAppKV
	savedAppKV = func(key string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		return kv[key], nil
	}
	t.Cleanup(func() { savedAppKV = previous })
	return home, kv
}

func seedSavedProfiles(t *testing.T, kv map[string][]byte, profiles []map[string]any, credentials map[string]map[string]string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"version": 2, "profiles": profiles})
	if err != nil {
		t.Fatal(err)
	}
	kv[savedProfilesKey] = raw
	encrypted := map[string]map[string]string{}
	for id, fields := range credentials {
		encrypted[id] = map[string]string{}
		for name, value := range fields {
			sealed, err := appcrypto.EncryptForStorage(value)
			if err != nil {
				t.Fatal(err)
			}
			encrypted[id][name] = sealed
		}
	}
	raw, err = json.Marshal(map[string]any{"version": 1, "credentialsByProfileId": encrypted})
	if err != nil {
		t.Fatal(err)
	}
	kv[savedCredentialsKey] = raw
}

func profileFixture(id, host string, port int, extra map[string]any) map[string]any {
	profile := map[string]any{
		"id": id, "name": "Profile " + id, "host": host, "port": port, "username": "backup",
		"authType": "password", "tags": []string{}, "favorite": false, "saveCredentials": true,
		"portForwards": []any{}, "createdAt": 1, "updatedAt": 1,
	}
	for key, value := range extra {
		profile[key] = value
	}
	return profile
}

func recordKnownHost(t *testing.T, home, host string, port int, key ssh.PublicKey) {
	t.Helper()
	dir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(filepath.Join(dir, "known_hosts"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	address := knownhosts.Normalize(net.JoinHostPort(host, strconv.Itoa(port)))
	if _, err := file.WriteString(knownhosts.Line([]string{address}, key) + "\n"); err != nil {
		t.Fatal(err)
	}
}

func randomPublicKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// writePrivateKey writes an OpenSSH ed25519 private key, encrypted when
// passphrase is non-empty, and returns its path.
func writePrivateKey(t *testing.T, dir, name, passphrase string) string {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var block *pem.Block
	if passphrase == "" {
		block, err = ssh.MarshalPrivateKey(private, "")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(private, "", []byte(passphrase))
	}
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSavedConnectionEligibility(t *testing.T) {
	home, kv := useSavedStore(t)
	for _, host := range []string{"known.example", "keyhost.example"} {
		recordKnownHost(t, home, host, 22, randomPublicKey(t))
	}
	plainKey := writePrivateKey(t, home, "plain_key", "")
	lockedKey := writePrivateKey(t, home, "locked_key", "secret phrase")
	key := func(path string) map[string]any { return map[string]any{"authType": "key", "keyFile": path} }
	seedSavedProfiles(t, kv, []map[string]any{
		profileFixture("ok", "known.example", 22, nil),
		profileFixture("nopass", "known.example", 22, nil),
		profileFixture("nosave", "known.example", 22, map[string]any{"saveCredentials": false}),
		profileFixture("kbd", "known.example", 22, map[string]any{"authType": "keyboard-interactive"}),
		profileFixture("agent", "known.example", 22, map[string]any{"authType": "agent"}),
		profileFixture("totp", "known.example", 22, nil),
		profileFixture("jump", "known.example", 22, map[string]any{"jumpHost": map[string]any{"host": "j", "port": 22, "username": "u", "authType": "password"}}),
		profileFixture("ftp", "known.example", 22, map[string]any{"fileTransfer": map[string]any{"enabled": true, "protocol": "ftp", "host": "known.example", "port": 21, "username": "u", "viaCurrentSsh": false}}),
		profileFixture("unknown", "unknown.example", 22, nil),
		profileFixture("userhost", "backup@known.example", 22, nil),
		profileFixture("keyplain", "keyhost.example", 22, key(plainKey)),
		profileFixture("keylocked", "keyhost.example", 22, key(lockedKey)),
		profileFixture("keyunlocked", "keyhost.example", 22, key(lockedKey)),
		profileFixture("socks", "known.example", 22, map[string]any{"socksProxy": map[string]any{"host": "127.0.0.1", "port": 1080, "username": "proxy"}}),
		profileFixture("socksok", "known.example", 22, map[string]any{"socksProxy": map[string]any{"host": "127.0.0.1", "port": 1080, "username": "proxy"}}),
	}, map[string]map[string]string{
		"ok":          {"password": "pw"},
		"nosave":      {"password": "pw"},
		"kbd":         {"password": "pw"},
		"totp":        {"password": "pw", "totpUri": "otpauth://totp/x?secret=ABC"},
		"jump":        {"password": "pw"},
		"ftp":         {"password": "pw", "ftpPassword": "pw"},
		"unknown":     {"password": "pw"},
		"userhost":    {"password": "pw"},
		"keyunlocked": {"passphrase": "secret phrase"},
		"socks":       {"password": "pw"},
		"socksok":     {"password": "pw", "socksPassword": "proxy-pw"},
	})
	connections, err := ListSavedConnections()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"ok": "", "nopass": ReasonNoSavedCredential, "nosave": ReasonNoSavedCredential, "kbd": ReasonInteractive,
		"agent": ReasonInteractive, "totp": ReasonInteractive, "jump": ReasonJumpHost, "ftp": ReasonNotSftp,
		"unknown": ReasonUnknownHostKey, "userhost": "", "keyplain": "", "keylocked": ReasonNoSavedCredential,
		"keyunlocked": "", "socks": ReasonNoSavedCredential, "socksok": "",
	}
	if len(connections) != len(want) {
		t.Fatalf("got %d connections", len(connections))
	}
	for _, connection := range connections {
		reason, ok := want[connection.ID]
		if !ok {
			t.Fatalf("unexpected %q", connection.ID)
		}
		if connection.Eligible != (reason == "") || connection.Reason != reason {
			t.Errorf("%s: eligible=%v reason=%q, want reason %q", connection.ID, connection.Eligible, connection.Reason, reason)
		}
		if connection.Name != "Profile "+connection.ID {
			t.Errorf("%s: name %q", connection.ID, connection.Name)
		}
	}
	if userhost, err := SavedConnectionByID("userhost"); err != nil || userhost.Host != "known.example" || userhost.Username != "backup" || userhost.Port != 22 {
		t.Fatalf("userhost %+v %v", userhost, err)
	}
	if _, err := SavedConnectionByID("missing"); SavedErrorCode(err) != SavedNotFound {
		t.Fatalf("missing: %v", err)
	}
}

// testSSHServer serves SFTP over SSH with password auth on 127.0.0.1.
type testSSHServer struct {
	port    int
	hostKey ssh.PublicKey
	root    string
}

func startTestSSHServer(t *testing.T, password string) *testSSHServer {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{PasswordCallback: func(meta ssh.ConnMetadata, given []byte) (*ssh.Permissions, error) {
		if meta.User() == "backup" && string(given) == password {
			return nil, nil
		}
		return nil, errors.New("denied")
	}}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go serveTestSSH(conn, config, root)
		}
	}()
	return &testSSHServer{port: listener.Addr().(*net.TCPAddr).Port, hostKey: signer.PublicKey(), root: root}
}

func serveTestSSH(conn net.Conn, config *ssh.ServerConfig, root string) {
	defer conn.Close()
	_, channels, requests, err := ssh.NewServerConn(conn, config)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(requests)
	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "unsupported")
			continue
		}
		channel, channelRequests, err := newChannel.Accept()
		if err != nil {
			return
		}
		go func() {
			for request := range channelRequests {
				ok := request.Type == "subsystem" && len(request.Payload) > 4 && string(request.Payload[4:]) == "sftp"
				_ = request.Reply(ok, nil)
				if ok {
					server, err := pkgsftp.NewServer(channel, pkgsftp.WithServerWorkingDirectory(root))
					if err == nil {
						_ = server.Serve()
					}
					_ = channel.Close()
				}
			}
		}()
	}
}

func TestOpenSavedSFTPRoundTripAndStrictHostKey(t *testing.T) {
	home, kv := useSavedStore(t)
	server := startTestSSHServer(t, "right")
	seedSavedProfiles(t, kv, []map[string]any{
		profileFixture("good", "127.0.0.1", server.port, nil),
		profileFixture("wrong", "127.0.0.1", server.port, nil),
	}, map[string]map[string]string{"good": {"password": "right"}, "wrong": {"password": "nope"}})

	// Not in known_hosts yet: refused before dialing, and nothing is recorded.
	if _, _, err := OpenSavedSFTP(context.Background(), "good"); SavedErrorCode(err) != SavedNotEligible {
		t.Fatalf("unknown host: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".ssh", "known_hosts")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("known_hosts written: %v", err)
	}

	// A different key on record: the strict callback refuses the handshake.
	recordKnownHost(t, home, "127.0.0.1", server.port, randomPublicKey(t))
	if _, _, err := OpenSavedSFTP(context.Background(), "good"); SavedErrorCode(err) != SavedHostKey {
		t.Fatalf("changed key: %v", err)
	}

	if err := os.Remove(filepath.Join(home, ".ssh", "known_hosts")); err != nil {
		t.Fatal(err)
	}
	recordKnownHost(t, home, "127.0.0.1", server.port, server.hostKey)
	before, _ := os.ReadFile(filepath.Join(home, ".ssh", "known_hosts"))

	if _, _, err := OpenSavedSFTP(context.Background(), "wrong"); SavedErrorCode(err) != SavedAuth {
		t.Fatalf("wrong password: %v", err)
	}

	client, closeClient, err := OpenSavedSFTP(context.Background(), "good")
	if err != nil {
		t.Fatal(err)
	}
	file, err := client.Create("hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("hi")); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	reader, err := client.Open("hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(reader)
	_ = reader.Close()
	if string(data) != "hi" {
		t.Fatalf("read %q", data)
	}
	closeClient()
	closeClient() // idempotent
	if _, err := client.Getwd(); err == nil {
		t.Fatal("client still usable after close")
	}
	after, _ := os.ReadFile(filepath.Join(home, ".ssh", "known_hosts"))
	if string(before) != string(after) {
		t.Fatal("known_hosts changed")
	}

	// The overall context closes the connection.
	ctx, cancel := context.WithCancel(context.Background())
	client, closeClient, err = OpenSavedSFTP(ctx, "good")
	if err != nil {
		t.Fatal(err)
	}
	defer closeClient()
	cancel()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if _, err = client.Getwd(); err != nil {
			break
		}
	}
	if err == nil {
		t.Fatal("cancelled context left the connection open")
	}
}

func TestOpenSavedSFTPNetworkError(t *testing.T) {
	home, kv := useSavedStore(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	recordKnownHost(t, home, "127.0.0.1", port, randomPublicKey(t))
	seedSavedProfiles(t, kv, []map[string]any{profileFixture("down", "127.0.0.1", port, nil)},
		map[string]map[string]string{"down": {"password": "pw"}})
	if _, _, err := OpenSavedSFTP(context.Background(), "down"); SavedErrorCode(err) != SavedNetwork {
		t.Fatalf("closed port: %v", err)
	}
}
