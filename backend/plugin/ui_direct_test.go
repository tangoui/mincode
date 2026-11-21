package plugin

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"mincode/app/backend/core"
)

// directTestSession starts an asset-mode view session; framed sessions may use the direct channel.
func directTestSession(t *testing.T, host *pluginUIHost, framed bool) pluginUISessionDTO {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "ui"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ui", "index.html"), []byte("<!doctype html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	session, err := host.Begin("example.direct", "main", 1, root, "ui/index.html", "tango://app:9250", nil)
	if err != nil {
		t.Fatal(err)
	}
	if framed {
		host.MarkSessionFramed(session.SessionID)
	}
	return session
}

func dialDirect(t *testing.T, rawURL string) (*websocket.Conn, int) {
	t.Helper()
	conn, response, err := websocket.DefaultDialer.Dial(rawURL, nil)
	status := 0
	if response != nil {
		status = response.StatusCode
		_ = response.Body.Close()
	}
	if err != nil {
		return nil, status
	}
	return conn, status
}

func TestDirectTokenIsSingleUseAndBound(t *testing.T) {
	host := newPluginUIHost()
	defer host.Shutdown()

	plain := directTestSession(t, host, false)
	if _, err := host.IssueDirectToken(plain.SessionID, plain.Nonce); err == nil {
		t.Fatal("a session without the framed transport received a direct token")
	}
	framed := directTestSession(t, host, true)
	if _, err := host.IssueDirectToken(framed.SessionID, "wrong-nonce"); err == nil {
		t.Fatal("a wrong nonce received a direct token")
	}
	ticket, err := host.IssueDirectToken(framed.SessionID, framed.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(ticket.URL)
	if err != nil || parsed.Scheme != "ws" || !strings.HasPrefix(parsed.Host, "127.0.0.1:") || parsed.Path != pluginUIDirectPath {
		t.Fatalf("direct URL = %q", ticket.URL)
	}
	if token := parsed.Query().Get("token"); len(token) < 32 || token != ticket.Token {
		t.Fatalf("token = %q", token)
	}

	conn, status := dialDirect(t, ticket.URL)
	if conn == nil {
		t.Fatalf("first use failed with status %d", status)
	}
	defer conn.Close()
	if again, status := dialDirect(t, ticket.URL); again != nil || status != http.StatusForbidden {
		t.Fatalf("token reused: status %d", status)
	}
	forged := strings.Replace(ticket.URL, ticket.Token, strings.Repeat("a", len(ticket.Token)), 1)
	if forgedConn, status := dialDirect(t, forged); forgedConn != nil || status != http.StatusForbidden {
		t.Fatalf("forged token accepted: status %d", status)
	}
}

func TestDirectTokenExpires(t *testing.T) {
	host := newPluginUIHost()
	defer host.Shutdown()
	now := time.Unix(1_800_000_000, 0)
	previous := directNow
	directNow = func() time.Time { return now }
	t.Cleanup(func() { directNow = previous })

	session := directTestSession(t, host, true)
	ticket, err := host.IssueDirectToken(session.SessionID, session.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(pluginUIDirectTokenTTL + time.Second)
	if conn, status := dialDirect(t, ticket.URL); conn != nil || status != http.StatusForbidden {
		t.Fatalf("expired token accepted: status %d", status)
	}
}

func TestDirectUpgradeRejectsWrongHost(t *testing.T) {
	host := newPluginUIHost()
	defer host.Shutdown()
	session := directTestSession(t, host, true)
	ticket, err := host.IssueDirectToken(session.SessionID, session.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	header := http.Header{}
	header.Set("Host", "evil.example")
	dialer := *websocket.DefaultDialer
	conn, response, err := dialer.Dial(ticket.URL, header)
	if err == nil {
		conn.Close()
		t.Fatal("an upgrade with a foreign Host header was accepted")
	}
	if response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %v", response)
	}
	_ = response.Body.Close()
	// The token was not spent by the refused attempt.
	good, status := dialDirect(t, ticket.URL)
	if good == nil {
		t.Fatalf("valid upgrade refused after a foreign Host attempt: %d", status)
	}
	good.Close()
}

func TestDirectConnClosedOnSessionReleaseAndRevoke(t *testing.T) {
	for name, end := range map[string]func(host *pluginUIHost, session pluginUISessionDTO){
		"close session": func(host *pluginUIHost, session pluginUISessionDTO) { host.CloseSession(session.SessionID) },
		"revoke plugin": func(host *pluginUIHost, session pluginUISessionDTO) { host.RevokePlugin(session.PluginID) },
	} {
		t.Run(name, func(t *testing.T) {
			host := newPluginUIHost()
			defer host.Shutdown()
			session := directTestSession(t, host, true)
			open, err := host.IssueDirectToken(session.SessionID, session.Nonce)
			if err != nil {
				t.Fatal(err)
			}
			unused, err := host.IssueDirectToken(session.SessionID, session.Nonce)
			if err != nil {
				t.Fatal(err)
			}
			conn, status := dialDirect(t, open.URL)
			if conn == nil {
				t.Fatalf("dial failed: %d", status)
			}
			defer conn.Close()
			end(host, session)
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			if _, _, err := conn.ReadMessage(); err == nil {
				t.Fatal("the direct connection stayed open after the session ended")
			}
			if late, status := dialDirect(t, unused.URL); late != nil || status != http.StatusForbidden {
				t.Fatalf("an unused token outlived its session: status %d", status)
			}
		})
	}
}

// BeginUI marks sessions of framed-transport plugins, which is what lets their
// views request a direct token; other plugins never get one.
func TestBeginUIMarksFramedSessionsForDirectChannel(t *testing.T) {
	manager, record := enabledOnDemand(t, "")
	plain, err := manager.BeginUI(record.id, "view", "tango://app")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.IssueUIDirectToken(plain.SessionID, plain.Nonce); err == nil {
		t.Fatal("a JSONL-transport plugin view received a direct token")
	}
	record.mu.Lock()
	record.manifest.Runtime.Transport = pluginTransportFramed
	record.mu.Unlock()
	framed, err := manager.BeginUI(record.id, "view", "tango://app")
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := manager.IssueUIDirectToken(framed.SessionID, framed.Nonce)
	if err != nil || !strings.HasPrefix(ticket.URL, "ws://127.0.0.1:") {
		t.Fatalf("framed view token = %+v, %v", ticket, err)
	}
}

// openDirect dials the direct channel for a framed session and returns the socket.
func openDirect(t *testing.T, host *pluginUIHost, session pluginUISessionDTO) *websocket.Conn {
	t.Helper()
	ticket, err := host.IssueDirectToken(session.SessionID, session.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	conn, status := dialDirect(t, ticket.URL)
	if conn == nil {
		t.Fatalf("dial failed: %d", status)
	}
	t.Cleanup(func() { conn.Close() })
	// The connection is registered just after the upgrade returns.
	deadline := time.Now().Add(2 * time.Second)
	for len(host.directConnsFor(session.PluginID, session.Generation)) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("direct connection was not registered")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return conn
}

func TestDirectRoutesProcessFramesWithoutEmit(t *testing.T) {
	emitted := 0
	core.SetEmitter(func(name string, _ any) {
		if name == pluginUIBinaryEvent {
			emitted++
		}
	})
	t.Cleanup(func() { core.SetEmitter(nil) })
	host := newPluginUIHost()
	defer host.Shutdown()
	session := directTestSession(t, host, true)

	// Without a direct connection the bridge event is used.
	host.deliverViewBinary(session.PluginID, session.Generation, "screen", []byte("bridge"))
	if emitted != 1 {
		t.Fatalf("bridge events = %d, want 1", emitted)
	}

	conn := openDirect(t, host, session)
	payload := bytes.Repeat([]byte{0, 1, 0xff}, 4096)
	host.deliverViewBinary(session.PluginID, session.Generation, "screen", payload)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	kind, frame, err := conn.ReadMessage()
	if err != nil || kind != websocket.BinaryMessage {
		t.Fatalf("read = %v, %v", kind, err)
	}
	want := append([]byte{byte(len("screen"))}, append([]byte("screen"), payload...)...)
	if !bytes.Equal(frame, want) {
		t.Fatalf("frame = %d bytes, want %d", len(frame), len(want))
	}
	if emitted != 1 {
		t.Fatalf("a directly delivered frame was also broadcast: %d events", emitted)
	}
}

func TestDirectDropsStaleGeneration(t *testing.T) {
	core.SetEmitter(func(string, any) {})
	t.Cleanup(func() { core.SetEmitter(nil) })
	host := newPluginUIHost()
	defer host.Shutdown()
	session := directTestSession(t, host, true)
	conn := openDirect(t, host, session)
	host.deliverViewBinary(session.PluginID, session.Generation+1, "screen", []byte("new generation"))
	_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if _, frame, err := conn.ReadMessage(); err == nil {
		t.Fatalf("a frame of another generation reached the view: %q", frame)
	}
}

func TestDirectSlowReaderDoesNotBlockDelivery(t *testing.T) {
	core.SetEmitter(func(string, any) {})
	t.Cleanup(func() { core.SetEmitter(nil) })
	host := newPluginUIHost()
	defer host.Shutdown()
	session := directTestSession(t, host, true)
	conn := openDirect(t, host, session)
	_ = conn // never read: the view is stuck
	// Small frames keep the bridge fallback cheap; what matters is that no
	// single delivery waits on the stuck socket.
	payload := bytes.Repeat([]byte{7}, 64*1024)
	slowest := time.Duration(0)
	for range 1000 {
		started := time.Now()
		host.deliverViewBinary(session.PluginID, session.Generation, "screen", payload)
		slowest = max(slowest, time.Since(started))
	}
	if slowest > 250*time.Millisecond {
		t.Fatalf("one delivery to a stuck view took %v", slowest)
	}
	// The stuck connection is dropped so the view falls back and reconnects.
	deadline := time.Now().Add(2 * time.Second)
	for len(host.directConnsFor(session.PluginID, session.Generation)) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the stuck direct connection was kept")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestDirectForwardsViewFramesToProcess(t *testing.T) {
	host := newPluginUIHost()
	defer host.Shutdown()
	type inbound struct {
		session, channel string
		data             []byte
	}
	received := make(chan inbound, 4)
	host.SetDirectInbound(func(sessionID, channel string, data []byte) bool {
		received <- inbound{sessionID, channel, append([]byte(nil), data...)}
		return true
	})
	session := directTestSession(t, host, true)
	conn := openDirect(t, host, session)
	frame := append([]byte{byte(len("input"))}, append([]byte("input"), 1, 2, 3)...)
	if err := conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-received:
		if got.session != session.SessionID || got.channel != "input" || !bytes.Equal(got.data, []byte{1, 2, 3}) {
			t.Fatalf("inbound = %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("view frame was not forwarded")
	}
	// A malformed channel closes the connection.
	if err := conn.WriteMessage(websocket.BinaryMessage, append([]byte{3}, "../"...)); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("a malformed frame did not close the connection")
	}
}

// A view's frames reach the real framed process over the direct channel and
// the echo comes back the same way, with no frontend event in between.
func TestDirectChannelEndToEndWithFramedProcess(t *testing.T) {
	emitted := make(chan struct{}, 8)
	core.SetEmitter(func(name string, _ any) {
		if name == pluginUIBinaryEvent {
			emitted <- struct{}{}
		}
	})
	t.Cleanup(func() { core.SetEmitter(nil) })
	useTemporaryPluginData(t)
	root := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "helper.exe"), data, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "ui"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ui", "index.html"), []byte("<!doctype html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{ID: "runtime.framed", Name: "Framed", Version: "1.0.0", APIVersion: 2,
		Runtime:     ProcessRuntimeManifest{Type: "process", Entry: "helper.exe", Transport: pluginTransportFramed, Args: []string{"-test.run=^TestProtocolHelperProcess$"}},
		Contributes: ManifestContributions{Views: []ManifestView{{ID: "view", Title: "View", Entry: "ui/index.html"}}}}
	manager := newDirectTestManager(t, root, manifest)
	session, err := manager.BeginUI(manifest.ID, "view", "tango://app")
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := manager.IssueUIDirectToken(session.SessionID, session.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	conn, status := dialDirect(t, ticket.URL)
	if conn == nil {
		t.Fatalf("dial failed: %d", status)
	}
	defer conn.Close()
	deadline := time.Now().Add(2 * time.Second)
	for len(manager.uiHost.directConnsFor(manifest.ID, session.Generation)) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("direct connection was not registered")
		}
		time.Sleep(5 * time.Millisecond)
	}
	payload := bytes.Repeat([]byte{0, 1, 0xff, '\n'}, 2048)
	if err := conn.WriteMessage(websocket.BinaryMessage, append([]byte{byte(len("pty/1"))}, append([]byte("pty/1"), payload...)...)); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, frame, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	channel, body, ok := decodeDirectFrame(frame)
	if !ok || channel != "echo:pty/1" || !bytes.Equal(body, append([]byte("echo:"), payload...)) {
		t.Fatalf("echo = %q (%d bytes)", channel, len(body))
	}
	select {
	case <-emitted:
		t.Fatal("the echo was also broadcast as a frontend event")
	default:
	}
}

func newDirectTestManager(t *testing.T, root string, manifest Manifest) *pluginManager {
	t.Helper()
	manager := &pluginManager{records: map[string]*pluginRecord{}, uiHost: newPluginUIHost()}
	manager.wireDirectChannel()
	if err := manager.registerLocal(root, manifest); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Shutdown(context.Background()) })
	if err := manager.SetEnabled(context.Background(), manifest.ID, true); err != nil {
		t.Fatal(err)
	}
	return manager
}

func fetchCSP(t *testing.T, session pluginUISessionDTO) (string, string) {
	t.Helper()
	parsed, err := url.Parse(session.URL)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Fragment = ""
	response, err := http.Get(parsed.String()) // #nosec G107 -- test-only loopback listener.
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	return response.Header.Get("Content-Security-Policy"), parsed.Host
}

func TestPluginUICSPAddsDirectSocketOnlyForFramed(t *testing.T) {
	host := newPluginUIHost()
	defer host.Shutdown()
	plain := directTestSession(t, host, false)
	csp, address := fetchCSP(t, plain)
	assetOrigin := "http://" + address
	if want := pluginUICSP("tango://app:9250", assetOrigin, false, false); csp != want {
		t.Fatalf("plain CSP changed:\n%s\nwant\n%s", csp, want)
	}
	if strings.Contains(csp, "ws://") || !strings.Contains(csp, "worker-src "+assetOrigin+" blob:;") {
		t.Fatalf("plain CSP opened the socket or lost local workers: %s", csp)
	}

	framedHost := newPluginUIHost()
	defer framedHost.Shutdown()
	framed := directTestSession(t, framedHost, true)
	csp, address = fetchCSP(t, framed)
	assetOrigin = "http://" + address
	for _, expected := range []string{
		"connect-src " + assetOrigin + " ws://" + address + ";",
		"worker-src " + assetOrigin + " blob:;",
	} {
		if !strings.Contains(csp, expected) {
			t.Fatalf("framed CSP %q lacks %q", csp, expected)
		}
	}
	if strings.Count(csp, "ws://") != 1 {
		t.Fatalf("framed CSP allows other sockets: %s", csp)
	}
}
