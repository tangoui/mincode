package http

import (
	"context"
	"encoding/json"
	"fmt"
	nethttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"mincode/app/backend/core"
)

func TestParseSocketIOPacket(t *testing.T) {
	cases := []struct {
		raw  string
		want socketIOPacket
	}{
		{`2["hello",1]`, socketIOPacket{Type: sioEvent, Data: `["hello",1]`}},
		{`2/chat,12["msg"]`, socketIOPacket{Type: sioEvent, Namespace: "/chat", ID: 12, HasID: true, Data: `["msg"]`}},
		{`3/chat,7["ok"]`, socketIOPacket{Type: sioAck, Namespace: "/chat", ID: 7, HasID: true, Data: `["ok"]`}},
		{`51-["file",{"_placeholder":true,"num":0}]`, socketIOPacket{Type: sioBinaryEvent, Attachments: 1, Data: `["file",{"_placeholder":true,"num":0}]`}},
		{`0/admin,{"sid":"x"}`, socketIOPacket{Type: sioConnect, Namespace: "/admin", Data: `{"sid":"x"}`}},
		{`1/admin,`, socketIOPacket{Type: sioDisconnect, Namespace: "/admin"}},
		{`4{"message":"denied"}`, socketIOPacket{Type: sioConnectError, Data: `{"message":"denied"}`}},
	}
	for _, item := range cases {
		got, err := parseSocketIOPacket(item.raw)
		if err != nil {
			t.Fatalf("parse %q: %v", item.raw, err)
		}
		if got != item.want {
			t.Fatalf("parse %q = %+v, want %+v", item.raw, got, item.want)
		}
		if encoded := encodeSocketIOPacket(got); encoded != strings.TrimSuffix(item.raw, ",") && encoded != item.raw {
			t.Fatalf("encode %+v = %q, want %q", got, encoded, item.raw)
		}
	}
	for _, raw := range []string{"", "9", "5x-[]", "5-[]", "599-[]"} {
		if _, err := parseSocketIOPacket(raw); err == nil {
			t.Fatalf("parse %q should fail", raw)
		}
	}
}

func TestBuildSocketIOURL(t *testing.T) {
	got, err := buildSocketIOURL("https://example.com/app?token=a#x", "", [][2]string{{"room", "1"}, {"EIO", "3"}})
	if err != nil {
		t.Fatal(err)
	}
	if got != "wss://example.com/socket.io/?EIO=4&room=1&token=a&transport=websocket" {
		t.Fatalf("url = %s", got)
	}
	got, err = buildSocketIOURL("ws://127.0.0.1:3000", "/custom/", nil)
	if err != nil || got != "ws://127.0.0.1:3000/custom/?EIO=4&transport=websocket" {
		t.Fatalf("url = %s, err = %v", got, err)
	}
	for _, item := range [][2]string{{"ftp://example.com", ""}, {"http://", ""}, {"http://example.com", "no-slash"}, {"http://169.254.169.254", ""}} {
		if _, err := buildSocketIOURL(item[0], item[1], nil); err == nil {
			t.Fatalf("buildSocketIOURL(%q, %q) should fail", item[0], item[1])
		}
	}
}

func TestNormalizeSocketIONamespaceAndAuth(t *testing.T) {
	for raw, want := range map[string]string{"": "/", "/": "/", " /chat ": "/chat"} {
		if got, err := normalizeSocketIONamespace(raw); err != nil || got != want {
			t.Fatalf("namespace %q = %q, %v", raw, got, err)
		}
	}
	for _, raw := range []string{"chat", "/a,b", "/a b", "/a?x"} {
		if _, err := normalizeSocketIONamespace(raw); err == nil {
			t.Fatalf("namespace %q should fail", raw)
		}
	}
	if got, err := normalizeSocketIOAuth(` { "token" : "abc" } `); err != nil || got != `{"token":"abc"}` {
		t.Fatalf("auth = %q, %v", got, err)
	}
	if got, err := normalizeSocketIOAuth(""); err != nil || got != "" {
		t.Fatalf("empty auth = %q, %v", got, err)
	}
	for _, raw := range []string{"[]", "null", "1", "{"} {
		if _, err := normalizeSocketIOAuth(raw); err == nil {
			t.Fatalf("auth %q should fail", raw)
		}
	}
}

func TestBuildSocketIOEmit(t *testing.T) {
	got, err := buildSocketIOEmit("/", "chat", `["hi",{"n":12345678901234567890}]`, nil, 0, false)
	if err != nil || got != `2["chat","hi",{"n":12345678901234567890}]` {
		t.Fatalf("emit = %q, %v", got, err)
	}
	got, err = buildSocketIOEmit("/room", "chat", "", nil, 5, true)
	if err != nil || got != `2/room,5["chat"]` {
		t.Fatalf("emit ack = %q, %v", got, err)
	}
	got, err = buildSocketIOEmit("/", "upload", `[{"file":{"$attachment":1}}]`, [][]byte{{1}, {2}}, 3, true)
	want := `52-3["upload",{"file":{"_placeholder":true,"num":1}},{"_placeholder":true,"num":0}]`
	if err != nil || got != want {
		t.Fatalf("emit binary = %q, %v", got, err)
	}
	for _, item := range [][2]string{{"", "[]"}, {"connect", "[]"}, {"x", "{}"}, {"x", `[{"$attachment":0}]`}} {
		if _, err := buildSocketIOEmit("/", item[0], item[1], nil, 0, false); err == nil {
			t.Fatalf("emit %q %q should fail", item[0], item[1])
		}
	}
}

func TestReadSocketIOAttachments(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "a.bin")
	if err := os.WriteFile(first, []byte{1, 2}, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readSocketIOAttachments([]socketIOAttachment{{Path: first}})
	if err != nil || len(got) != 1 || string(got[0]) != "\x01\x02" {
		t.Fatalf("attachments = %v, %v", got, err)
	}
	if _, err := readSocketIOAttachments([]socketIOAttachment{{Path: dir}}); err == nil {
		t.Fatal("directory attachment should fail")
	}
	if _, err := readSocketIOAttachments(make([]socketIOAttachment, socketIOMaxAttachments+1)); err == nil {
		t.Fatal("too many attachments should fail")
	}
}

func TestSocketIOReaderHandlesFrames(t *testing.T) {
	reader := &socketIOReader{namespace: "/chat"}
	result, err := reader.handleFrame(websocket.TextMessage, []byte("2"))
	if err != nil || result.reply != "3" {
		t.Fatalf("ping result = %+v, %v", result, err)
	}
	result, _ = reader.handleFrame(websocket.TextMessage, []byte(`42["global"]`))
	if len(result.events) != 0 {
		t.Fatalf("other namespace should be ignored: %+v", result)
	}
	result, _ = reader.handleFrame(websocket.TextMessage, []byte(`40/chat,{"sid":"s1"}`))
	if len(result.events) != 1 || result.events[0]["type"] != "opened" || result.events[0]["sid"] != "s1" {
		t.Fatalf("connect result = %+v", result)
	}
	result, _ = reader.handleFrame(websocket.TextMessage, []byte(`42/chat,9["msg",{"a":1}]`))
	if len(result.events) != 1 || result.events[0]["event"] != "msg" || result.events[0]["data"] != `[{"a":1}]` || result.events[0]["ackId"] != int64(9) {
		t.Fatalf("event result = %+v", result)
	}
	result, _ = reader.handleFrame(websocket.TextMessage, []byte(`43/chat,4["done"]`))
	if len(result.events) != 1 || result.events[0]["type"] != "ack" || result.events[0]["ackId"] != int64(4) {
		t.Fatalf("ack result = %+v", result)
	}
	result, _ = reader.handleFrame(websocket.TextMessage, []byte(`452-/chat,["file",{"_placeholder":true,"num":0},{"_placeholder":true,"num":1}]`))
	if len(result.events) != 0 {
		t.Fatalf("binary header should wait: %+v", result)
	}
	result, _ = reader.handleFrame(websocket.BinaryMessage, []byte{0xff})
	if len(result.events) != 0 {
		t.Fatalf("first attachment should wait: %+v", result)
	}
	result, err = reader.handleFrame(websocket.BinaryMessage, []byte("hi"))
	if err != nil || len(result.events) != 1 {
		t.Fatalf("binary event = %+v, %v", result, err)
	}
	if data := result.events[0]["data"].(string); data != `[{"$attachment":0,"base64":"/w==","size":1},{"$attachment":1,"base64":"aGk=","size":2}]` {
		t.Fatalf("binary data = %s", data)
	}
	result, _ = reader.handleFrame(websocket.TextMessage, []byte(`44/chat,{"message":"bad token"}`))
	if !result.closed || !strings.Contains(result.events[0]["message"].(string), "bad token") {
		t.Fatalf("connect error = %+v", result)
	}
	if _, err := reader.handleFrame(websocket.BinaryMessage, []byte{1}); err == nil {
		t.Fatal("unexpected binary frame should fail")
	}
	if result, _ := reader.handleFrame(websocket.TextMessage, []byte("1")); !result.closed {
		t.Fatal("engine close should close session")
	}
}

type recordingStreamConn struct {
	mu     sync.Mutex
	events []map[string]any
	notify chan struct{}
}

func (conn *recordingStreamConn) Send([]byte) error { return nil }
func (conn *recordingStreamConn) SendJSON(value any) error {
	conn.mu.Lock()
	conn.events = append(conn.events, value.(map[string]any))
	conn.mu.Unlock()
	select {
	case conn.notify <- struct{}{}:
	default:
	}
	return nil
}
func (conn *recordingStreamConn) Receive() ([]byte, error) { select {} }
func (conn *recordingStreamConn) ReceiveJSON(any) error    { return nil }
func (conn *recordingStreamConn) Close() error             { return nil }
func (conn *recordingStreamConn) Context() context.Context { return context.Background() }

func (conn *recordingStreamConn) waitFor(t *testing.T, eventType string) map[string]any {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		conn.mu.Lock()
		for index, event := range conn.events {
			if event["type"] == eventType {
				conn.events = append(conn.events[:index:index], conn.events[index+1:]...)
				conn.mu.Unlock()
				return event
			}
		}
		conn.mu.Unlock()
		select {
		case <-conn.notify:
		case <-deadline:
			t.Fatalf("timed out waiting for %s", eventType)
		}
	}
}

func TestSocketIOSessionEndToEnd(t *testing.T) {
	serverFrames := make(chan string, 16)
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		if r.URL.Path != "/io/" || r.URL.Query().Get("EIO") != "4" || r.URL.Query().Get("room") != "a" || r.Header.Get("X-Test") != "1" {
			nethttp.Error(w, "bad request", nethttp.StatusBadRequest)
			return
		}
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`0{"sid":"eio","pingInterval":25000,"pingTimeout":20000}`))
		for {
			messageType, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			frame := string(data)
			if messageType == websocket.BinaryMessage {
				frame = "binary:" + frame
			}
			serverFrames <- frame
			switch {
			case frame == `40/chat,{"token":"t"}`:
				_ = conn.WriteMessage(websocket.TextMessage, []byte(`40/chat,{"sid":"sio"}`))
				_ = conn.WriteMessage(websocket.TextMessage, []byte("2"))
			case strings.HasPrefix(frame, `451-/chat,`):
				packet, _ := parseSocketIOPacket(frame[1:])
				_ = conn.WriteMessage(websocket.TextMessage, []byte("4"+encodeSocketIOPacket(socketIOPacket{Type: sioAck, Namespace: "/chat", ID: packet.ID, HasID: true, Data: `["ok"]`})))
				_ = conn.WriteMessage(websocket.TextMessage, []byte(`451-/chat,["blob",{"_placeholder":true,"num":0}]`))
				_ = conn.WriteMessage(websocket.BinaryMessage, []byte("xyz"))
			}
		}
	}))
	defer srv.Close()

	const sessionID = "socketio-e2e"
	recorder := &recordingStreamConn{notify: make(chan struct{}, 1)}
	addHTTPStreamSub(sessionID, recorder)
	defer removeHTTPStreamSub(sessionID, recorder)

	connectArgs, _ := json.Marshal(map[string]any{
		"sessionId": sessionID,
		"url":       srv.URL,
		"path":      "/io",
		"namespace": "/chat",
		"auth":      `{"token":"t"}`,
		"query":     [][2]string{{"room", "a"}},
		"headers":   [][2]string{{"X-Test", "1"}},
	})
	if _, err := socketIOConnectHandler("", string(connectArgs)); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = streamDisconnectHandler("", `{"sessionId":"`+sessionID+`"}`) }()
	if event := recorder.waitFor(t, "opened"); event["sid"] != "sio" || event["namespace"] != "/chat" {
		t.Fatalf("opened = %+v", event)
	}
	expectFrame(t, serverFrames, `40/chat,{"token":"t"}`)
	expectFrame(t, serverFrames, "3")

	attachment := filepath.Join(t.TempDir(), "a.bin")
	if err := os.WriteFile(attachment, []byte{1}, 0o600); err != nil {
		t.Fatal(err)
	}
	emitArgs, _ := json.Marshal(map[string]any{
		"sessionId":   sessionID,
		"event":       "say",
		"args":        `["hi"]`,
		"ack":         true,
		"attachments": []map[string]string{{"path": attachment}},
	})
	result, err := socketIOEmitHandler("", string(emitArgs))
	if err != nil {
		t.Fatal(err)
	}
	var emitted struct {
		AckID int64 `json:"ackId"`
	}
	if err := core.DecodeArgs(result, &emitted); err != nil || emitted.AckID <= 0 {
		t.Fatalf("emit result = %s, %v", result, err)
	}
	header := <-serverFrames
	if want := fmt.Sprintf(`451-/chat,%d["say","hi",{"_placeholder":true,"num":0}]`, emitted.AckID); header != want {
		t.Fatalf("emit frame = %q, want %q", header, want)
	}
	expectFrame(t, serverFrames, "binary:\x01")
	if event := recorder.waitFor(t, "ack"); event["data"] != `["ok"]` {
		t.Fatalf("ack = %+v", event)
	}
	if event := recorder.waitFor(t, "message"); event["event"] != "blob" || !strings.Contains(event["data"].(string), `"base64":"eHl6"`) {
		t.Fatalf("binary message = %+v", event)
	}
}

func expectFrame(t *testing.T, frames <-chan string, want string) {
	t.Helper()
	select {
	case got := <-frames:
		if got != want {
			t.Fatalf("frame = %q, want %q", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for frame %q", want)
	}
}
