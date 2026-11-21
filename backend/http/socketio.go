package http

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"

	"mincode/app/backend/core"
)

// Socket.IO 客户端只实现 v3/v4 服务端使用的 Engine.IO 4 WebSocket 传输。
const (
	socketIOEngineVersion   = 4
	socketIOMaxAttachments  = 20
	socketIOHandshakeWindow = 30 * time.Second
)

// Socket.IO 数据包类型。
const (
	sioConnect      = 0
	sioDisconnect   = 1
	sioEvent        = 2
	sioAck          = 3
	sioConnectError = 4
	sioBinaryEvent  = 5
	sioBinaryAck    = 6
)

type socketIOConnectArgs struct {
	SessionID string      `json:"sessionId"`
	URL       string      `json:"url"`
	Path      string      `json:"path"`
	Namespace string      `json:"namespace"`
	Auth      string      `json:"auth"`
	Query     [][2]string `json:"query"`
	Headers   [][2]string `json:"headers"`
}

// socketIOAttachment 为用户选择的本地文件，由后端读取，避免大文件经前端转 base64。
type socketIOAttachment struct {
	Path string `json:"path"`
}

type socketIOEmitArgs struct {
	SessionID   string               `json:"sessionId"`
	Event       string               `json:"event"`
	Args        string               `json:"args"`
	Attachments []socketIOAttachment `json:"attachments"`
	Ack         bool                 `json:"ack"`
}

type socketIOPacket struct {
	Type        int
	Attachments int
	Namespace   string
	ID          int64
	HasID       bool
	Data        string
}

var socketIOAckSequence atomic.Int64

func init() {
	core.Register("http_socketio_connect", socketIOConnectHandler)
	core.Register("http_socketio_emit", socketIOEmitHandler)
}

// parseSocketIOPacket 解析去掉 Engine.IO 消息前缀 "4" 之后的 Socket.IO 数据包。
func parseSocketIOPacket(raw string) (socketIOPacket, error) {
	if raw == "" || raw[0] < '0' || raw[0] > '6' {
		return socketIOPacket{}, errors.New("无效的 Socket.IO 数据包类型")
	}
	packet := socketIOPacket{Type: int(raw[0] - '0')}
	rest := raw[1:]
	if packet.Type == sioBinaryEvent || packet.Type == sioBinaryAck {
		count, after, found := strings.Cut(rest, "-")
		attachments, err := strconv.Atoi(count)
		if !found || err != nil || attachments < 0 || attachments > socketIOMaxAttachments {
			return socketIOPacket{}, errors.New("无效的 Socket.IO 二进制附件数量")
		}
		packet.Attachments = attachments
		rest = after
	}
	if strings.HasPrefix(rest, "/") {
		namespace, after, found := strings.Cut(rest, ",")
		packet.Namespace = namespace
		rest = ""
		if found {
			rest = after
		}
	}
	digits := 0
	for digits < len(rest) && rest[digits] >= '0' && rest[digits] <= '9' {
		digits++
	}
	if digits > 0 {
		id, err := strconv.ParseInt(rest[:digits], 10, 64)
		if err != nil {
			return socketIOPacket{}, errors.New("无效的 Socket.IO 回执 ID")
		}
		packet.ID, packet.HasID = id, true
		rest = rest[digits:]
	}
	packet.Data = rest
	return packet, nil
}

func encodeSocketIOPacket(packet socketIOPacket) string {
	var builder strings.Builder
	builder.WriteByte(byte('0' + packet.Type))
	if packet.Type == sioBinaryEvent || packet.Type == sioBinaryAck {
		builder.WriteString(strconv.Itoa(packet.Attachments))
		builder.WriteByte('-')
	}
	if packet.Namespace != "" && packet.Namespace != "/" {
		builder.WriteString(packet.Namespace)
		builder.WriteByte(',')
	}
	if packet.HasID {
		builder.WriteString(strconv.FormatInt(packet.ID, 10))
	}
	builder.WriteString(packet.Data)
	return builder.String()
}

func normalizeSocketIONamespace(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "/" {
		return "/", nil
	}
	if !strings.HasPrefix(value, "/") || len(value) > 200 || strings.ContainsAny(value, ",?#\r\n ") {
		return "", errors.New("Socket.IO 命名空间必须以 / 开头，且不能包含逗号、空格、? 或 #")
	}
	return value, nil
}

// normalizeSocketIOAuth 校验认证参数必须是 JSON 对象，空串表示不带认证。
func normalizeSocketIOAuth(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	var value map[string]any
	if err := json.Unmarshal([]byte(raw), &value); err != nil || value == nil {
		return "", errors.New("Socket.IO 认证参数必须是 JSON 对象")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("编码 Socket.IO 认证参数失败: %w", err)
	}
	return string(encoded), nil
}

// buildSocketIOURL 把 http(s)/ws(s) 地址转换为 Engine.IO 4 WebSocket 地址。
func buildSocketIOURL(raw, path string, query [][2]string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("无效 Socket.IO 地址: %w", err)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "ws":
		parsed.Scheme = "ws"
	case "https", "wss":
		parsed.Scheme = "wss"
	default:
		return "", errors.New("Socket.IO 地址仅支持 http/https/ws/wss 协议")
	}
	if parsed.Hostname() == "" {
		return "", errors.New("Socket.IO 地址缺少主机名")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		path = "/socket.io"
	}
	if !strings.HasPrefix(path, "/") || len(path) > 200 || strings.ContainsAny(path, "?#\r\n ") {
		return "", errors.New("Socket.IO path 必须以 / 开头，且不能包含空格、? 或 #")
	}
	if len(query) > 100 {
		return "", errors.New("查询参数数量超过上限(100)")
	}
	parsed.Path = strings.TrimRight(path, "/") + "/"
	parsed.RawPath = ""
	values := parsed.Query()
	for _, row := range query {
		if name := strings.TrimSpace(row[0]); name != "" {
			values.Set(name, row[1])
		}
	}
	values.Set("EIO", strconv.Itoa(socketIOEngineVersion))
	values.Set("transport", "websocket")
	parsed.RawQuery = values.Encode()
	parsed.Fragment = ""
	result := parsed.String()
	if err := validateWebSocketURL(result); err != nil {
		return "", err
	}
	return result, nil
}

// buildSocketIOEmit 生成事件数据包与随后要发送的二进制帧。
// 参数数组中的 {"$attachment": n} 替换为第 n 个附件；未被引用的附件依次追加到参数末尾。
func buildSocketIOEmit(namespace, event, argsJSON string, attachments [][]byte, ackID int64, hasAck bool) (string, error) {
	event = strings.TrimSpace(event)
	if event == "" || len(event) > 200 || strings.ContainsAny(event, "\r\n") {
		return "", errors.New("Socket.IO 事件名不能为空，且不超过 200 个字符")
	}
	switch event {
	case "connect", "connect_error", "disconnect", "disconnecting", "newListener", "removeListener":
		return "", fmt.Errorf("%s 是 Socket.IO 保留事件名", event)
	}
	if len(attachments) > socketIOMaxAttachments {
		return "", fmt.Errorf("二进制附件数量超过上限(%d)", socketIOMaxAttachments)
	}
	var args []any
	if strings.TrimSpace(argsJSON) != "" {
		decoder := json.NewDecoder(strings.NewReader(argsJSON))
		decoder.UseNumber()
		if err := decoder.Decode(&args); err != nil {
			return "", errors.New("事件参数必须是 JSON 数组，例如 [\"hello\", {\"id\": 1}]")
		}
	}
	used := make([]bool, len(attachments))
	var replace func(value any) (any, error)
	replace = func(value any) (any, error) {
		switch typed := value.(type) {
		case map[string]any:
			if marker, ok := typed["$attachment"]; ok && len(typed) == 1 {
				number, ok := marker.(json.Number)
				index, err := strconv.Atoi(string(number))
				if !ok || err != nil || index < 0 || index >= len(attachments) {
					return nil, fmt.Errorf("附件引用 %v 不存在", marker)
				}
				used[index] = true
				return map[string]any{"_placeholder": true, "num": index}, nil
			}
			for key, item := range typed {
				next, err := replace(item)
				if err != nil {
					return nil, err
				}
				typed[key] = next
			}
			return typed, nil
		case []any:
			for index, item := range typed {
				next, err := replace(item)
				if err != nil {
					return nil, err
				}
				typed[index] = next
			}
			return typed, nil
		default:
			return value, nil
		}
	}
	for index, item := range args {
		next, err := replace(item)
		if err != nil {
			return "", err
		}
		args[index] = next
	}
	for index, isUsed := range used {
		if !isUsed {
			args = append(args, map[string]any{"_placeholder": true, "num": index})
		}
	}
	data, err := json.Marshal(append([]any{event}, args...))
	if err != nil {
		return "", fmt.Errorf("编码 Socket.IO 事件失败: %w", err)
	}
	packet := socketIOPacket{Type: sioEvent, Namespace: namespace, ID: ackID, HasID: hasAck, Data: string(data)}
	if len(attachments) > 0 {
		packet.Type = sioBinaryEvent
		packet.Attachments = len(attachments)
	}
	return encodeSocketIOPacket(packet), nil
}

// fillSocketIOAttachments 把 {"_placeholder":true,"num":n} 替换为可展示的附件描述。
func fillSocketIOAttachments(data string, attachments [][]byte) (string, error) {
	decoder := json.NewDecoder(strings.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", fmt.Errorf("解析 Socket.IO 二进制数据包失败: %w", err)
	}
	var fill func(value any) any
	fill = func(value any) any {
		switch typed := value.(type) {
		case map[string]any:
			if placeholder, _ := typed["_placeholder"].(bool); placeholder {
				if number, ok := typed["num"].(json.Number); ok {
					if index, err := strconv.Atoi(string(number)); err == nil && index >= 0 && index < len(attachments) {
						return map[string]any{
							"$attachment": index,
							"size":        len(attachments[index]),
							"base64":      base64.StdEncoding.EncodeToString(attachments[index]),
						}
					}
				}
			}
			for key, item := range typed {
				typed[key] = fill(item)
			}
			return typed
		case []any:
			for index, item := range typed {
				typed[index] = fill(item)
			}
			return typed
		default:
			return value
		}
	}
	encoded, err := json.Marshal(fill(value))
	if err != nil {
		return "", fmt.Errorf("编码 Socket.IO 二进制数据包失败: %w", err)
	}
	return string(encoded), nil
}

func readSocketIOAttachments(items []socketIOAttachment) ([][]byte, error) {
	if len(items) > socketIOMaxAttachments {
		return nil, fmt.Errorf("二进制附件数量超过上限(%d)", socketIOMaxAttachments)
	}
	total := 0
	result := make([][]byte, 0, len(items))
	for index, item := range items {
		data, _, err := readHTTPUploadFile(item.Path)
		if err != nil {
			return nil, fmt.Errorf("附件 %d: %w", index, err)
		}
		total += len(data)
		if total > streamMaxMessage {
			return nil, fmt.Errorf("二进制附件总大小超过上限(%dB)", streamMaxMessage)
		}
		result = append(result, data)
	}
	return result, nil
}

func socketIOConnectHandler(_ string, argsJSON string) (string, error) {
	var args socketIOConnectArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if err := validateSessionID(args.SessionID); err != nil {
		return "", err
	}
	namespace, err := normalizeSocketIONamespace(args.Namespace)
	if err != nil {
		return "", err
	}
	auth, err := normalizeSocketIOAuth(args.Auth)
	if err != nil {
		return "", err
	}
	endpoint, err := buildSocketIOURL(args.URL, args.Path, args.Query)
	if err != nil {
		return "", err
	}
	headers, err := requestHeaders(args.Headers)
	if err != nil {
		return "", err
	}

	session := &streamSession{kind: "socketio", namespace: namespace}
	if err := installStreamSession(args.SessionID, session); err != nil {
		return "", err
	}
	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = socketIOHandshakeWindow
	conn, response, err := dialer.Dial(endpoint, headers)
	if err != nil {
		removeStreamSession(args.SessionID, session)
		if response != nil {
			return "", fmt.Errorf("Socket.IO 握手失败(HTTP %d): %w", response.StatusCode, err)
		}
		return "", fmt.Errorf("Socket.IO 连接失败: %w", err)
	}
	conn.SetReadLimit(streamMaxMessage)
	session.write.Lock()
	if getStreamSession(args.SessionID) != session {
		session.write.Unlock()
		_ = conn.Close()
		return "", errors.New("Socket.IO 连接已取消")
	}
	session.ws = conn
	session.write.Unlock()

	fail := func(err error) (string, error) {
		closeStreamSession(args.SessionID, session, "handshake failed")
		return "", err
	}
	_ = conn.SetReadDeadline(time.Now().Add(socketIOHandshakeWindow))
	messageType, data, err := conn.ReadMessage()
	if err != nil {
		return fail(fmt.Errorf("等待 Engine.IO 握手失败: %w", err))
	}
	var open struct {
		SID          string `json:"sid"`
		PingInterval int    `json:"pingInterval"`
		PingTimeout  int    `json:"pingTimeout"`
	}
	if messageType != websocket.TextMessage || len(data) < 2 || data[0] != '0' || json.Unmarshal(data[1:], &open) != nil || open.SID == "" {
		return fail(errors.New("服务端不是 Socket.IO v3/v4（Engine.IO 4）或握手响应无效"))
	}
	session.heartbeat = time.Duration(open.PingInterval+open.PingTimeout) * time.Millisecond
	if session.heartbeat <= 0 {
		session.heartbeat = 45 * time.Second
	}
	connect := encodeSocketIOPacket(socketIOPacket{Type: sioConnect, Namespace: namespace, Data: auth})
	session.write.Lock()
	err = conn.WriteMessage(websocket.TextMessage, []byte("4"+connect))
	session.write.Unlock()
	if err != nil {
		return fail(fmt.Errorf("连接 Socket.IO 命名空间失败: %w", err))
	}
	emitStream(args.SessionID, "transport", map[string]any{
		"sid":          open.SID,
		"pingInterval": open.PingInterval,
		"pingTimeout":  open.PingTimeout,
	})
	go readSocketIO(args.SessionID, session)
	return core.EncodeResult(nil)
}

func socketIOEmitHandler(_ string, argsJSON string) (string, error) {
	var args socketIOEmitArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	session := getStreamSession(args.SessionID)
	if session == nil || session.kind != "socketio" {
		return "", errors.New("Socket.IO 尚未连接")
	}
	attachments, err := readSocketIOAttachments(args.Attachments)
	if err != nil {
		return "", err
	}
	var ackID int64
	if args.Ack {
		ackID = socketIOAckSequence.Add(1)
	}
	packet, err := buildSocketIOEmit(session.namespace, args.Event, args.Args, attachments, ackID, args.Ack)
	if err != nil {
		return "", err
	}
	if len(packet) > streamMaxMessage {
		return "", fmt.Errorf("Socket.IO 事件超过上限(%dB)", streamMaxMessage)
	}
	session.write.Lock()
	defer session.write.Unlock()
	if session.ws == nil {
		return "", errors.New("Socket.IO 尚未连接")
	}
	if err := session.ws.WriteMessage(websocket.TextMessage, []byte("4"+packet)); err != nil {
		return "", fmt.Errorf("发送 Socket.IO 事件失败: %w", err)
	}
	for _, attachment := range attachments {
		// Engine.IO 4 的 WebSocket 二进制帧不带类型前缀。
		if err := session.ws.WriteMessage(websocket.BinaryMessage, attachment); err != nil {
			return "", fmt.Errorf("发送 Socket.IO 二进制附件失败: %w", err)
		}
	}
	result := map[string]any{"ackId": nil}
	if args.Ack {
		result["ackId"] = ackID
	}
	return core.EncodeResult(result)
}

// socketIOReader 把 Engine.IO 帧还原成 Socket.IO 事件，便于在不建立网络连接的情况下测试。
type socketIOReader struct {
	namespace string
	pending   *socketIOPacket
	buffers   [][]byte
}

type socketIOFrameResult struct {
	reply  string
	events []map[string]any
	closed bool
}

func (reader *socketIOReader) handleFrame(messageType int, data []byte) (socketIOFrameResult, error) {
	if messageType == websocket.BinaryMessage {
		if reader.pending == nil {
			return socketIOFrameResult{}, errors.New("收到未声明的 Socket.IO 二进制帧")
		}
		reader.buffers = append(reader.buffers, data)
		if len(reader.buffers) < reader.pending.Attachments {
			return socketIOFrameResult{}, nil
		}
		packet := *reader.pending
		buffers := reader.buffers
		reader.pending, reader.buffers = nil, nil
		filled, err := fillSocketIOAttachments(packet.Data, buffers)
		if err != nil {
			return socketIOFrameResult{}, err
		}
		packet.Data = filled
		return reader.packetEvents(packet)
	}
	if !utf8.Valid(data) {
		return socketIOFrameResult{}, errors.New("Socket.IO 文本帧包含无效 UTF-8")
	}
	if len(data) == 0 {
		return socketIOFrameResult{}, nil
	}
	switch data[0] {
	case '1':
		return socketIOFrameResult{closed: true}, nil
	case '2':
		return socketIOFrameResult{reply: "3" + string(data[1:])}, nil
	case '4':
	default:
		return socketIOFrameResult{}, nil
	}
	if reader.pending != nil {
		return socketIOFrameResult{}, errors.New("Socket.IO 二进制附件尚未接收完整")
	}
	packet, err := parseSocketIOPacket(string(data[1:]))
	if err != nil {
		return socketIOFrameResult{}, err
	}
	if packet.Namespace == "" {
		packet.Namespace = "/"
	}
	if packet.Namespace != reader.namespace {
		return socketIOFrameResult{}, nil
	}
	if packet.Attachments > 0 {
		reader.pending = &packet
		return socketIOFrameResult{}, nil
	}
	return reader.packetEvents(packet)
}

func (reader *socketIOReader) packetEvents(packet socketIOPacket) (socketIOFrameResult, error) {
	switch packet.Type {
	case sioConnect:
		var payload struct {
			SID string `json:"sid"`
		}
		_ = json.Unmarshal([]byte(packet.Data), &payload)
		return socketIOFrameResult{events: []map[string]any{{"type": "opened", "namespace": packet.Namespace, "sid": payload.SID}}}, nil
	case sioConnectError:
		var payload struct {
			Message string `json:"message"`
		}
		message := packet.Data
		if json.Unmarshal([]byte(packet.Data), &payload) == nil && payload.Message != "" {
			message = payload.Message
		}
		return socketIOFrameResult{events: []map[string]any{{"type": "error", "message": "Socket.IO 连接被拒绝: " + message}}, closed: true}, nil
	case sioDisconnect:
		return socketIOFrameResult{closed: true}, nil
	case sioEvent, sioBinaryEvent:
		var values []json.RawMessage
		if err := json.Unmarshal([]byte(packet.Data), &values); err != nil || len(values) == 0 {
			return socketIOFrameResult{}, errors.New("Socket.IO 事件数据不是数组")
		}
		var name string
		if err := json.Unmarshal(values[0], &name); err != nil {
			return socketIOFrameResult{}, errors.New("Socket.IO 事件名不是字符串")
		}
		args, _ := json.Marshal(values[1:])
		event := map[string]any{"type": "message", "event": name, "data": string(args), "size": len(packet.Data), "attachments": packet.Attachments}
		if packet.HasID {
			event["ackId"] = packet.ID
		}
		return socketIOFrameResult{events: []map[string]any{event}}, nil
	case sioAck, sioBinaryAck:
		if !packet.HasID {
			return socketIOFrameResult{}, errors.New("Socket.IO 回执缺少 ID")
		}
		return socketIOFrameResult{events: []map[string]any{{"type": "ack", "ackId": packet.ID, "data": packet.Data, "size": len(packet.Data), "attachments": packet.Attachments}}}, nil
	}
	return socketIOFrameResult{}, nil
}

func readSocketIO(sessionID string, session *streamSession) {
	defer func() {
		if removeStreamSession(sessionID, session) {
			_ = session.ws.Close()
			emitStream(sessionID, "closed", map[string]any{})
		}
	}()
	reader := &socketIOReader{namespace: session.namespace}
	for {
		_ = session.ws.SetReadDeadline(time.Now().Add(session.heartbeat))
		messageType, data, err := session.ws.ReadMessage()
		if err != nil {
			var netErr interface{ Timeout() bool }
			switch {
			case errors.As(err, &netErr) && netErr.Timeout():
				emitStream(sessionID, "error", map[string]any{"message": "Socket.IO 心跳超时，服务端未按时发送 ping"})
			case !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway):
				emitStream(sessionID, "error", map[string]any{"message": fmt.Sprintf("读取 Socket.IO 消息失败: %v", err)})
			}
			return
		}
		result, err := reader.handleFrame(messageType, data)
		if err != nil {
			emitStream(sessionID, "error", map[string]any{"message": err.Error()})
			continue
		}
		if result.reply != "" {
			session.write.Lock()
			err = session.ws.WriteMessage(websocket.TextMessage, []byte(result.reply))
			session.write.Unlock()
			if err != nil {
				emitStream(sessionID, "error", map[string]any{"message": fmt.Sprintf("回复 Socket.IO 心跳失败: %v", err)})
				return
			}
		}
		for _, event := range result.events {
			eventType, _ := event["type"].(string)
			delete(event, "type")
			emitStream(sessionID, eventType, event)
		}
		if result.closed {
			return
		}
	}
}
