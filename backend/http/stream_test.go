package http

import (
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// wsSendHandler 与 wsConnectHandler 并发时，session.ws 的读写必须都在 write 锁内；
// 用 go test -race 运行可捕获无锁读取。
func TestWsSendReadsConnectionUnderWriteLock(t *testing.T) {
	received := make(chan string, 1)
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			_, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			select {
			case received <- string(data):
			default:
			}
		}
	}))
	defer srv.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	const sessionID = "ws-send-lock-test"
	session := &streamSession{kind: "ws"}
	if err := installStreamSession(sessionID, session); err != nil {
		t.Fatal(err)
	}
	defer removeStreamSession(sessionID, session)

	args := `{"sessionId":"` + sessionID + `","data":"hello"}`
	if _, err := wsSendHandler("", args); err == nil || err.Error() != "WebSocket 尚未连接" {
		t.Fatalf("send before connect err = %v", err)
	}

	done := make(chan error, 1)
	go func() {
		for {
			_, err := wsSendHandler("", args)
			if err == nil || err.Error() != "WebSocket 尚未连接" {
				done <- err
				return
			}
		}
	}()
	time.Sleep(10 * time.Millisecond)
	session.write.Lock()
	session.ws = conn
	session.write.Unlock()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("send after connect: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("send did not observe the connection")
	}
	select {
	case got := <-received:
		if got != "hello" {
			t.Fatalf("server received %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not receive the message")
	}
}
