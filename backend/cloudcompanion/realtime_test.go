package cloudcompanion

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"mincode/app/backend/core"
)

func dialTestRealtimeServer(t *testing.T, serve func(*websocket.Conn)) *websocket.Conn {
	t.Helper()
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer conn.Close()
		serve(conn)
	}))
	t.Cleanup(server.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func waitServeConnection(t *testing.T, conn *websocket.Conn, served <-chan error) {
	t.Helper()
	_ = conn.Close()
	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("serveConnection did not return after the connection closed")
	}
}

func TestServeConnectionRelayHandlerCanWaitForAcceptedAndAcknowledged(t *testing.T) {
	conn := dialTestRealtimeServer(t, func(server *websocket.Conn) {
		_ = server.SetReadDeadline(time.Now().Add(5 * time.Second))
		if err := server.WriteJSON(map[string]any{"type": "relay", "relayId": "relay-1", "requestId": "req-1"}); err != nil {
			t.Errorf("write relay: %v", err)
			return
		}
		var response map[string]any
		if err := server.ReadJSON(&response); err != nil || response["type"] != "relay" || response["requestId"] != "resp-1" {
			t.Errorf("unexpected response frame %v: %v", response, err)
			return
		}
		if err := server.WriteJSON(map[string]any{"type": "accepted", "requestId": "resp-1", "relayId": "relay-2"}); err != nil {
			t.Errorf("write accepted: %v", err)
			return
		}
		var ack map[string]any
		if err := server.ReadJSON(&ack); err != nil || ack["type"] != "ack" || ack["requestId"] != "req-1" || ack["relayId"] != "relay-1" {
			t.Errorf("unexpected ack frame %v: %v", ack, err)
			return
		}
		if err := server.WriteJSON(map[string]any{"type": "acknowledged", "requestId": "req-1"}); err != nil {
			t.Errorf("write acknowledged: %v", err)
			return
		}
		_, _, _ = server.ReadMessage()
	})
	m := &realtimeManager{conn: conn}
	handled := make(chan error, 1)
	served := make(chan error, 1)
	go func() {
		served <- m.serveConnection(context.Background(), conn, func(ctx context.Context, raw []byte) error {
			waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if _, err := m.sendAndWaitAccepted(waitCtx, "resp-1", map[string]any{"type": "relay", "requestId": "resp-1"}); err != nil {
				handled <- err
				return err
			}
			err := m.acknowledge(waitCtx, "req-1", "relay-1")
			handled <- err
			return err
		})
	}()

	select {
	case err := <-handled:
		if err != nil {
			t.Fatalf("relay handler failed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("relay handler deadlocked waiting for frames delivered by the read loop")
	}
	waitServeConnection(t, conn, served)
}

func TestServeConnectionKeepsAnsweringPingsWhenRelayQueueIsFull(t *testing.T) {
	var eventsMu sync.Mutex
	queueFullEvents := 0
	core.SetEmitter(func(name string, data any) {
		payload, _ := data.(map[string]any)
		if name == cloudRealtimeEvent && payload["code"] == "relay_queue_full" {
			eventsMu.Lock()
			queueFullEvents++
			eventsMu.Unlock()
		}
	})
	t.Cleanup(func() { core.SetEmitter(nil) })

	pong := make(chan error, 1)
	conn := dialTestRealtimeServer(t, func(server *websocket.Conn) {
		for i := 0; i < cloudRelayQueueSize+5; i++ {
			if err := server.WriteJSON(map[string]any{"type": "relay", "relayId": "relay", "requestId": "req"}); err != nil {
				pong <- err
				return
			}
		}
		if err := server.WriteJSON(map[string]any{"type": "ping"}); err != nil {
			pong <- err
			return
		}
		_ = server.SetReadDeadline(time.Now().Add(5 * time.Second))
		var response map[string]any
		if err := server.ReadJSON(&response); err != nil {
			pong <- err
			return
		}
		if response["type"] != "pong" {
			t.Errorf("expected pong, got %v", response)
		}
		pong <- nil
		_, _, _ = server.ReadMessage()
	})
	m := &realtimeManager{conn: conn}
	served := make(chan error, 1)
	handlerStarted := make(chan struct{}, 1)
	go func() {
		served <- m.serveConnection(context.Background(), conn, func(ctx context.Context, raw []byte) error {
			select {
			case handlerStarted <- struct{}{}:
			default:
			}
			<-ctx.Done()
			return ctx.Err()
		})
	}()

	select {
	case err := <-pong:
		if err != nil {
			t.Fatalf("read loop did not answer ping: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("read loop blocked by a slow relay handler")
	}
	select {
	case <-handlerStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("relay worker never started handling")
	}
	eventsMu.Lock()
	gotQueueFull := queueFullEvents
	eventsMu.Unlock()
	if gotQueueFull == 0 {
		t.Fatal("expected relay_queue_full events when the relay queue overflows")
	}
	waitServeConnection(t, conn, served)
}

func TestRunRelayWorkerHandlesInOrderAndStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	relays := make(chan []byte, 3)
	relays <- []byte("1")
	relays <- []byte("2")
	relays <- []byte("3")
	var got []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		runRelayWorker(ctx, relays, func(_ context.Context, raw []byte) error {
			got = append(got, string(raw))
			if len(got) == 3 {
				cancel()
			}
			return nil
		})
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("relay worker did not stop after context cancellation")
	}
	if strings.Join(got, ",") != "1,2,3" {
		t.Fatalf("relays handled out of order: %v", got)
	}
}

func TestRealtimeSourceLookupUsesPairedDevices(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		_, _ = w.Write([]byte(`{"code":1,"data":[{"device_id":"mobile-1234567890abcdef","device_type":"mobile","encryption_public_key":"enc","capabilities":["AI_CHAT"],"grant_id":"grant-1"}]}`))
	}))
	defer server.Close()
	identity := seedMemberRelayContext(t, server.URL)
	session, err := readSession()
	if err != nil {
		t.Fatal(err)
	}
	m := &realtimeManager{}
	device, err := m.cloudDevice(context.Background(), session, identity, "mobile-1234567890abcdef")
	if err != nil || device.EncryptionPublicKey != "enc" {
		t.Fatalf("source lookup = %+v, %v", device, err)
	}
	if len(paths) != 1 || paths[0] != "/api/mincode/v1/paired-devices" {
		t.Fatalf("paths = %v", paths)
	}
	if _, err := m.cloudDevice(context.Background(), session, identity, "mobile-unknownunknown00"); err == nil {
		t.Fatal("unpaired source must be rejected")
	}
}

func TestCloudPairedDevicesHandlerReturnsCapabilities(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/mincode/v1/paired-devices" {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"code":1,"data":[{"device_id":"mobile-1234567890abcdef","device_type":"mobile","name":"Phone","capabilities":["AI_CHAT","TERMINAL"],"grant_id":"grant-1"}]}`))
	}))
	defer server.Close()
	seedMemberRelayContext(t, server.URL)
	raw, err := cloudPairedDevicesHandler("", "{}")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, `"grant_id":"grant-1"`) || !strings.Contains(raw, `"capabilities":["AI_CHAT","TERMINAL"]`) || !strings.Contains(raw, `"name":"Phone"`) {
		t.Fatalf("paired devices = %s", raw)
	}
}

func TestRelayErrorMessage(t *testing.T) {
	codes := []string{"not_paired", "capability_denied", "unknown_channel", "invalid_frame", "payload_too_large", "rate_limited", "replaced", "device_removed", "internal_error"}
	for _, code := range codes {
		if message := relayErrorMessage(code); message == "" || message == code {
			t.Errorf("%s has no Chinese message", code)
		}
	}
	if relayErrorMessage("not_paired") != "该手机未配对或已解除配对" || relayErrorMessage("internal_error") != "服务器内部错误，请稍后重试" {
		t.Fatal("unexpected relay error text")
	}
	if relayErrorMessage("something_new") != "something_new" {
		t.Fatal("unknown codes must be returned unchanged")
	}
}

func TestServeConnectionRelayErrorFailsWaiterAndCarriesMessage(t *testing.T) {
	var eventsMu sync.Mutex
	var messages []any
	core.SetEmitter(func(name string, data any) {
		payload, _ := data.(map[string]any)
		if name == cloudRealtimeEvent && payload["type"] == "error" {
			eventsMu.Lock()
			messages = append(messages, payload["message"])
			eventsMu.Unlock()
		}
	})
	t.Cleanup(func() { core.SetEmitter(nil) })
	conn := dialTestRealtimeServer(t, func(server *websocket.Conn) {
		_ = server.SetReadDeadline(time.Now().Add(5 * time.Second))
		var frame map[string]any
		if err := server.ReadJSON(&frame); err != nil {
			return
		}
		_ = server.WriteJSON(map[string]any{"type": "error", "code": "not_paired", "requestId": frame["requestId"]})
		_, _, _ = server.ReadMessage()
	})
	m := &realtimeManager{conn: conn}
	served := make(chan error, 1)
	go func() {
		served <- m.serveConnection(context.Background(), conn, func(context.Context, []byte) error { return nil })
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := m.sendAndWaitAccepted(ctx, "req-1", map[string]any{"type": "relay", "requestId": "req-1"})
	if err == nil || !strings.Contains(err.Error(), "该手机未配对或已解除配对") {
		t.Fatalf("relay error must fail the waiter promptly: %v", err)
	}
	eventsMu.Lock()
	got := append([]any(nil), messages...)
	eventsMu.Unlock()
	if len(got) != 1 || got[0] != "该手机未配对或已解除配对" {
		t.Fatalf("error events = %v", got)
	}
	waitServeConnection(t, conn, served)
}

func TestSendEncryptedCloudPayloadReusesPairedDevicesUntilNotPaired(t *testing.T) {
	peer, err := generateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	const phone = "mobile-1234567890abcdef"
	var lookupsMu sync.Mutex
	lookups := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/mincode/v1/paired-devices" {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		lookupsMu.Lock()
		lookups++
		lookupsMu.Unlock()
		_, _ = w.Write([]byte(`{"code":1,"data":[{"device_id":"` + phone + `","device_type":"mobile","encryption_public_key":"` +
			peer.EncryptionPublicKey + `","capabilities":["TERMINAL"],"grant_id":"grant-1"}]}`))
	}))
	defer api.Close()
	seedMemberRelayContext(t, api.URL)
	countLookups := func() int {
		lookupsMu.Lock()
		defer lookupsMu.Unlock()
		return lookups
	}

	conn := dialTestRealtimeServer(t, func(server *websocket.Conn) {
		for {
			var frame map[string]any
			if err := server.ReadJSON(&frame); err != nil {
				return
			}
			reply := map[string]any{"type": "accepted", "requestId": frame["requestId"], "relayId": "relay-" + frame["requestId"].(string)}
			if frame["requestId"] == "denied" {
				reply = map[string]any{"type": "error", "code": "not_paired", "requestId": frame["requestId"]}
			}
			if err := server.WriteJSON(reply); err != nil {
				return
			}
		}
	})
	previous := cloudRealtime
	cloudRealtime = &realtimeManager{conn: conn}
	t.Cleanup(func() { cloudRealtime = previous })
	served := make(chan error, 1)
	go func() {
		served <- cloudRealtime.serveConnection(context.Background(), conn, func(context.Context, []byte) error { return nil })
	}()

	payload := map[string]any{"data": "chunk"}
	for i := range 5 {
		if _, err := sendEncryptedCloudPayload(phone, "terminal.output", "out-"+string(rune('a'+i)), int64(i), payload); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	if got := countLookups(); got != 1 {
		t.Fatalf("consecutive sends read /paired-devices %d times, want 1", got)
	}
	if _, err := sendEncryptedCloudPayload(phone, "terminal.output", "denied", 9, payload); err == nil {
		t.Fatal("not_paired must fail the send")
	}
	if _, err := sendEncryptedCloudPayload(phone, "terminal.output", "after", 10, payload); err != nil {
		t.Fatalf("send after not_paired: %v", err)
	}
	if got := countLookups(); got != 2 {
		t.Fatalf("not_paired must drop the cache: /paired-devices read %d times, want 2", got)
	}
	if _, err := sendEncryptedCloudPayload("mobile-unknownunknown00", "terminal.output", "nobody", 11, payload); err == nil ||
		!strings.Contains(err.Error(), "目标云设备不存在或未授权") {
		t.Fatalf("unpaired target = %v", err)
	}
	waitServeConnection(t, conn, served)
}

func TestRealtimeReconnectDropsPairedDeviceCache(t *testing.T) {
	m := &realtimeManager{cancel: func() {}, generation: 1,
		deviceCache: map[string]serverDevice{"mobile-1234567890abcdef": {}}, deviceCacheAt: time.Now()}
	if !m.setConnected(1, nil) {
		t.Fatal("setConnected rejected the current generation")
	}
	if m.deviceCache != nil || !m.deviceCacheAt.IsZero() {
		t.Fatal("a (re)connect must drop the paired-device cache")
	}
}
