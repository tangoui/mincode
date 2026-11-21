package http

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"

	"mincode/app/backend/core"
)

const (
	streamMaxMessage = 10 * 1024 * 1024
	streamMaxLine    = 1024 * 1024
)

type streamConnectArgs struct {
	SessionID string      `json:"sessionId"`
	URL       string      `json:"url"`
	Headers   [][2]string `json:"headers"`
}

type streamSession struct {
	kind   string
	cancel context.CancelFunc
	ws     *websocket.Conn
	write  sync.Mutex
	once   sync.Once
	// Socket.IO 会话使用的命名空间与心跳读超时。
	namespace string
	heartbeat time.Duration
}

var streamSessions = struct {
	sync.Mutex
	items map[string]*streamSession
}{items: make(map[string]*streamSession)}

func init() {
	core.Register("http_ws_connect", wsConnectHandler)
	core.Register("http_ws_send", wsSendHandler)
	core.Register("http_stream_disconnect", streamDisconnectHandler)
	core.Register("http_sse_connect", sseConnectHandler)
	core.HandleStream("http-stream", httpStreamRelayHandler)
}

// httpStreamSubs 记录经 Streams 订阅某个流会话的连接。
var httpStreamSubs = struct {
	sync.Mutex
	conns map[string]map[core.StreamConn]struct{}
}{conns: make(map[string]map[core.StreamConn]struct{})}

func addHTTPStreamSub(sessionID string, conn core.StreamConn) {
	httpStreamSubs.Lock()
	defer httpStreamSubs.Unlock()
	set, ok := httpStreamSubs.conns[sessionID]
	if !ok {
		set = make(map[core.StreamConn]struct{})
		httpStreamSubs.conns[sessionID] = set
	}
	set[conn] = struct{}{}
}

func removeHTTPStreamSub(sessionID string, conn core.StreamConn) {
	httpStreamSubs.Lock()
	defer httpStreamSubs.Unlock()
	if set, ok := httpStreamSubs.conns[sessionID]; ok {
		delete(set, conn)
		if len(set) == 0 {
			delete(httpStreamSubs.conns, sessionID)
		}
	}
}

func emitStream(sessionID, eventType string, fields map[string]any) {
	payload := map[string]any{"type": eventType, "timestamp": time.Now().UnixMilli()}
	for key, value := range fields {
		payload[key] = value
	}
	httpStreamSubs.Lock()
	set := httpStreamSubs.conns[sessionID]
	conns := make([]core.StreamConn, 0, len(set))
	for conn := range set {
		conns = append(conns, conn)
	}
	httpStreamSubs.Unlock()
	for _, conn := range conns {
		if err := conn.SendJSON(payload); err != nil {
			removeHTTPStreamSub(sessionID, conn)
			_ = conn.Close()
		}
	}
}

// httpStreamRelayHandler：前端连 Stream("http-stream") 后首帧 JSON 发 {sessionId}，
// 随后该流会话的事件额外经此连接推送，直到对端断开。
func httpStreamRelayHandler(conn core.StreamConn) {
	var hello struct {
		SessionID string `json:"sessionId"`
	}
	if err := conn.ReceiveJSON(&hello); err != nil {
		return
	}
	sessionID := strings.TrimSpace(hello.SessionID)
	if validateSessionID(sessionID) != nil {
		return
	}
	addHTTPStreamSub(sessionID, conn)
	defer removeHTTPStreamSub(sessionID, conn)
	if err := conn.SendJSON(map[string]any{"type": "subscribed"}); err != nil {
		return
	}
	for {
		if _, err := conn.Receive(); err != nil {
			return
		}
	}
}

func validateSessionID(sessionID string) error {
	if strings.TrimSpace(sessionID) == "" || len(sessionID) > 200 {
		return errors.New("无效的流会话 ID")
	}
	return nil
}

func requestHeaders(rows [][2]string) (http.Header, error) {
	if len(rows) > 200 {
		return nil, errors.New("请求头数量超过上限(200)")
	}
	headers := make(http.Header)
	for _, row := range rows {
		name := strings.TrimSpace(row[0])
		if name == "" {
			continue
		}
		if !validHeaderName(name) || strings.ContainsAny(row[1], "\r\n") {
			return nil, fmt.Errorf("无效的请求头: %s", name)
		}
		headers.Add(name, row[1])
	}
	return headers, nil
}

func validHeaderName(value string) bool {
	for _, char := range value {
		if char <= 32 || char >= 127 || strings.ContainsRune("()<>@,;:\\\"/[]?={}", char) {
			return false
		}
	}
	return value != ""
}

func validateWebSocketURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("无效 WebSocket URL: %w", err)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "ws":
		parsed.Scheme = "http"
	case "wss":
		parsed.Scheme = "https"
	default:
		return errors.New("WebSocket 地址仅支持 ws/wss 协议")
	}
	if parsed.Hostname() == "" {
		return errors.New("WebSocket 地址缺少主机名")
	}
	return core.ValidateRequestURL(parsed.String())
}

func installStreamSession(sessionID string, session *streamSession) error {
	streamSessions.Lock()
	defer streamSessions.Unlock()
	if _, exists := streamSessions.items[sessionID]; exists {
		return fmt.Errorf("流会话已存在: %s", sessionID)
	}
	streamSessions.items[sessionID] = session
	return nil
}

func getStreamSession(sessionID string) *streamSession {
	streamSessions.Lock()
	defer streamSessions.Unlock()
	return streamSessions.items[sessionID]
}

func removeStreamSession(sessionID string, expected *streamSession) bool {
	streamSessions.Lock()
	defer streamSessions.Unlock()
	if streamSessions.items[sessionID] != expected {
		return false
	}
	delete(streamSessions.items, sessionID)
	return true
}

func closeStreamSession(sessionID string, session *streamSession, reason string) bool {
	closed := false
	session.once.Do(func() {
		closed = true
		removeStreamSession(sessionID, session)
		if session.cancel != nil {
			session.cancel()
		}
		session.write.Lock()
		if session.ws != nil {
			_ = session.ws.WriteControl(
				websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, reason),
				time.Now().Add(time.Second),
			)
			_ = session.ws.Close()
		}
		session.write.Unlock()
	})
	return closed
}

func wsConnectHandler(_ string, argsJSON string) (string, error) {
	var args streamConnectArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if err := validateSessionID(args.SessionID); err != nil {
		return "", err
	}
	if err := validateWebSocketURL(args.URL); err != nil {
		return "", err
	}
	headers, err := requestHeaders(args.Headers)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	session := &streamSession{kind: "ws", cancel: cancel}
	if err := installStreamSession(args.SessionID, session); err != nil {
		cancel()
		return "", err
	}
	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = 30 * time.Second
	conn, response, err := dialer.DialContext(ctx, args.URL, headers)
	cancel()
	if err != nil {
		removeStreamSession(args.SessionID, session)
		if response != nil {
			return "", fmt.Errorf("WebSocket 握手失败(HTTP %d): %w", response.StatusCode, err)
		}
		return "", fmt.Errorf("WebSocket 连接失败: %w", err)
	}
	session.write.Lock()
	if getStreamSession(args.SessionID) != session {
		session.write.Unlock()
		_ = conn.Close()
		return "", errors.New("WebSocket 连接已取消")
	}
	conn.SetReadLimit(streamMaxMessage)
	session.ws = conn
	emitStream(args.SessionID, "opened", map[string]any{
		"protocol": conn.Subprotocol(),
		"status":   http.StatusSwitchingProtocols,
	})
	session.write.Unlock()

	go readWebSocket(args.SessionID, session)
	return core.EncodeResult(nil)
}

func readWebSocket(sessionID string, session *streamSession) {
	defer func() {
		if removeStreamSession(sessionID, session) {
			_ = session.ws.Close()
			emitStream(sessionID, "closed", map[string]any{})
		}
	}()
	for {
		messageType, data, err := session.ws.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				emitStream(sessionID, "error", map[string]any{"message": fmt.Sprintf("读取 WebSocket 消息失败: %v", err)})
			}
			return
		}
		kind := "binary"
		text := ""
		if messageType == websocket.TextMessage {
			kind = "text"
			if !utf8.Valid(data) {
				emitStream(sessionID, "error", map[string]any{"message": "WebSocket 文本消息包含无效 UTF-8"})
				continue
			}
			text = string(data)
		}
		emitStream(sessionID, "message", map[string]any{"messageType": kind, "data": text, "size": len(data)})
	}
}

type wsSendArgs struct {
	SessionID string `json:"sessionId"`
	Data      string `json:"data"`
}

func wsSendHandler(_ string, argsJSON string) (string, error) {
	var args wsSendArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	session := getStreamSession(args.SessionID)
	if session == nil || session.kind != "ws" {
		return "", errors.New("WebSocket 尚未连接")
	}
	if len(args.Data) > streamMaxMessage {
		return "", fmt.Errorf("WebSocket 消息超过上限(%dB)", streamMaxMessage)
	}
	// session.ws 由 wsConnectHandler 在 write 锁内赋值，必须在同一把锁内读取。
	session.write.Lock()
	if session.ws == nil {
		session.write.Unlock()
		return "", errors.New("WebSocket 尚未连接")
	}
	err := session.ws.WriteMessage(websocket.TextMessage, []byte(args.Data))
	session.write.Unlock()
	if err != nil {
		return "", fmt.Errorf("发送 WebSocket 消息失败: %w", err)
	}
	return core.EncodeResult(nil)
}

type streamDisconnectArgs struct {
	SessionID string `json:"sessionId"`
}

func streamDisconnectHandler(_ string, argsJSON string) (string, error) {
	var args streamDisconnectArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if session := getStreamSession(args.SessionID); session != nil && closeStreamSession(args.SessionID, session, "client disconnected") {
		emitStream(args.SessionID, "closed", map[string]any{})
	}
	return core.EncodeResult(nil)
}

func sseConnectHandler(_ string, argsJSON string) (string, error) {
	var args streamConnectArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if err := validateSessionID(args.SessionID); err != nil {
		return "", err
	}
	if err := core.ValidateRequestURL(args.URL); err != nil {
		return "", err
	}
	headers, err := requestHeaders(args.Headers)
	if err != nil {
		return "", err
	}
	headers.Set("Accept", "text/event-stream")

	ctx, cancel := context.WithCancel(context.Background())
	session := &streamSession{kind: "sse", cancel: cancel}
	if err := installStreamSession(args.SessionID, session); err != nil {
		cancel()
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, args.URL, nil)
	if err != nil {
		removeStreamSession(args.SessionID, session)
		cancel()
		return "", fmt.Errorf("创建 SSE 请求失败: %w", err)
	}
	req.Header = headers
	client := &http.Client{CheckRedirect: checkSameOriginRedirect}
	response, err := client.Do(req)
	if err != nil {
		removeStreamSession(args.SessionID, session)
		cancel()
		return "", fmt.Errorf("SSE 连接失败: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()
		removeStreamSession(args.SessionID, session)
		cancel()
		message, _ := io.ReadAll(io.LimitReader(response.Body, 64*1024))
		return "", fmt.Errorf("SSE 连接失败(HTTP %d): %s", response.StatusCode, strings.TrimSpace(string(message)))
	}
	if !strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		response.Body.Close()
		removeStreamSession(args.SessionID, session)
		cancel()
		return "", fmt.Errorf("响应不是 SSE 事件流: %s", response.Header.Get("Content-Type"))
	}
	if getStreamSession(args.SessionID) != session {
		response.Body.Close()
		return "", errors.New("SSE 连接已取消")
	}

	emitStream(args.SessionID, "opened", map[string]any{"status": response.StatusCode})
	go readSSE(args.SessionID, session, response.Body)
	return core.EncodeResult(nil)
}

type sseEvent struct {
	Event string
	Data  string
	ID    string
	Retry int
}

func scanSSE(reader io.Reader, emit func(sseEvent)) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), streamMaxLine)
	current := sseEvent{}
	dataLines := make([]string, 0, 1)
	eventSize := 0
	dispatch := func() {
		if len(dataLines) == 0 {
			current = sseEvent{}
			return
		}
		current.Data = strings.Join(dataLines, "\n")
		emit(current)
		current = sseEvent{}
		dataLines = dataLines[:0]
		eventSize = 0
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if !utf8.ValidString(line) {
			return errors.New("SSE 事件流包含无效 UTF-8")
		}
		if line == "" {
			dispatch()
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if !found {
			field, value = line, ""
		} else if strings.HasPrefix(value, " ") {
			value = value[1:]
		}
		switch field {
		case "event":
			current.Event = value
		case "data":
			eventSize += len(value)
			if len(dataLines) > 0 {
				eventSize++
			}
			if eventSize > streamMaxMessage {
				return fmt.Errorf("SSE 事件超过上限(%dB)", streamMaxMessage)
			}
			dataLines = append(dataLines, value)
		case "id":
			if !strings.ContainsRune(value, '\x00') {
				current.ID = value
			}
		case "retry":
			if _, err := fmt.Sscanf(value, "%d", &current.Retry); err != nil || current.Retry < 0 {
				current.Retry = 0
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("读取 SSE 事件流失败: %w", err)
	}
	dispatch()
	return nil
}

func readSSE(sessionID string, session *streamSession, body io.ReadCloser) {
	defer body.Close()
	err := scanSSE(body, func(event sseEvent) {
		eventType := event.Event
		if eventType == "" {
			eventType = "message"
		}
		emitStream(sessionID, "message", map[string]any{
			"event": eventType,
			"data":  event.Data,
			"id":    event.ID,
			"retry": event.Retry,
			"size":  len(event.Data),
		})
	})
	if removeStreamSession(sessionID, session) {
		if err != nil && !errors.Is(err, context.Canceled) {
			emitStream(sessionID, "error", map[string]any{"message": err.Error()})
		}
		emitStream(sessionID, "closed", map[string]any{})
	}
}

func shutdownStreams() {
	streamSessions.Lock()
	items := make(map[string]*streamSession, len(streamSessions.items))
	for sessionID, session := range streamSessions.items {
		items[sessionID] = session
	}
	streamSessions.Unlock()
	for sessionID, session := range items {
		closeStreamSession(sessionID, session, "application shutdown")
	}
}
