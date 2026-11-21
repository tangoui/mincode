package cloudcompanion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"mincode/app/backend/core"
)

const (
	cloudRealtimeEvent           = "cloud:realtime"
	cloudPayloadEvent            = "cloud:payload"
	cloudAcceptedEvent           = "cloud:accepted"
	cloudTerminalResponseChannel = "terminal.response"
	cloudStatusEvent             = "cloud:status"
	cloudWSPath                  = "/ws/mincode/v1"
	cloudRelayQueueSize          = 128
)

type realtimeTicket struct {
	Ticket    string `json:"ticket"`
	ExpiresAt int64  `json:"expires_at"`
}

type relayAccepted struct {
	RelayID   string
	Duplicate bool
	// Refusal is set instead when the relay answered the frame with an error (for example not_paired).
	Refusal string
}

// relayErrorMessages maps the relay's error frame codes to the text shown to the user.
var relayErrorMessages = map[string]string{
	"not_paired":        "该手机未配对或已解除配对",
	"capability_denied": "手机没有此功能的权限，请重新配对并勾选",
	"unknown_channel":   "不支持的消息类型",
	"invalid_frame":     "消息格式无效",
	"payload_too_large": "消息过大",
	"rate_limited":      "发送过于频繁",
	"replaced":          "此设备已在别处连接",
	"device_removed":    "此设备已被移除，请重新登录",
	"internal_error":    "服务器内部错误，请稍后重试",
}

// relayErrorMessage returns the Chinese text for a relay error code; unknown codes are returned unchanged.
func relayErrorMessage(code string) string {
	if message, ok := relayErrorMessages[code]; ok {
		return message
	}
	return code
}

type RealtimeState struct {
	Connected   bool   `json:"connected"`
	Connecting  bool   `json:"connecting"`
	LastError   string `json:"lastError,omitempty"`
	ConnectedAt int64  `json:"connectedAt,omitempty"`
}

type cloudRelayEnvelope struct {
	Type           string `json:"type"`
	RelayID        string `json:"relayId"`
	RequestID      string `json:"requestId"`
	SourceDeviceID string `json:"sourceDeviceId"`
	Channel        string `json:"channel"`
	Sequence       int64  `json:"sequence"`
	Ciphertext     string `json:"ciphertext"`
	Replayed       bool   `json:"replayed"`
}

type CloudPayloadEvent struct {
	RelayID        string         `json:"relayId"`
	RequestID      string         `json:"requestId"`
	SourceDeviceID string         `json:"sourceDeviceId"`
	Channel        string         `json:"channel"`
	Sequence       int64          `json:"sequence"`
	Payload        map[string]any `json:"payload"`
	Replayed       bool           `json:"replayed"`
}

type realtimeManager struct {
	mu              sync.RWMutex
	writeMu         sync.Mutex
	cancel          context.CancelFunc
	conn            *websocket.Conn
	state           RealtimeState
	generation      uint64
	deviceCache     map[string]serverDevice
	deviceCacheAt   time.Time
	acceptedWaiters map[string]chan relayAccepted
	ackWaiters      map[string]chan struct{}
}

var cloudRealtime = &realtimeManager{}

func (m *realtimeManager) start() error {
	m.mu.Lock()
	if m.cancel != nil {
		m.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	if m.acceptedWaiters == nil {
		m.acceptedWaiters = make(map[string]chan relayAccepted)
		m.ackWaiters = make(map[string]chan struct{})
	}
	m.generation++
	generation := m.generation
	m.state = RealtimeState{Connecting: true}
	m.mu.Unlock()
	m.emitState()
	go m.run(ctx, generation)
	return nil
}

func (m *realtimeManager) stop() {
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
	}
	m.cancel = nil
	m.generation++
	conn := m.conn
	m.conn = nil
	m.state = RealtimeState{}
	m.deviceCache = nil
	m.deviceCacheAt = time.Time{}
	m.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	m.emitState()
}

func (m *realtimeManager) snapshot() RealtimeState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state
}

func (m *realtimeManager) run(ctx context.Context, generation uint64) {
	backoff := time.Second
	for ctx.Err() == nil {
		if err := m.connectAndServe(ctx, generation); err != nil && ctx.Err() == nil {
			m.setError(generation, err)
		}
		if ctx.Err() != nil {
			return
		}
		jitter := time.Duration(rand.IntN(500)) * time.Millisecond
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff + jitter):
		}
		if backoff < 30*time.Second {
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}
}

func (m *realtimeManager) connectAndServe(ctx context.Context, generation uint64) error {
	identity, err := loadOrCreateIdentity()
	if err != nil {
		return err
	}
	session, err := readSession()
	if err != nil {
		return err
	}
	if !isMemberSession(session) {
		return errors.New("云会话已过期，请重新登录 MinCode Cloud")
	}
	var ticket realtimeTicket
	if err := authorizedCloudPost(ctx, session, identity, "/realtime/ticket", map[string]any{}, &ticket); err != nil {
		return fmt.Errorf("获取实时连接票据: %w", err)
	}
	if ticket.Ticket == "" || ticket.ExpiresAt <= cloudNow().Unix() {
		return errors.New("实时连接票据无效")
	}
	wsURL, err := cloudWebSocketURL(session.APIBaseURL)
	if err != nil {
		return err
	}
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	headers := http.Header{}
	headers.Set("Authorization", "Ticket "+ticket.Ticket)
	conn, _, err := dialer.DialContext(ctx, wsURL, headers)
	if err != nil {
		return fmt.Errorf("连接 MinCode Cloud 实时通道: %w", err)
	}
	defer conn.Close()
	conn.SetReadLimit(1024 * 1024)
	_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	})
	if !m.setConnected(generation, conn) {
		return context.Canceled
	}
	defer m.clearConnection(generation, conn)
	return m.serveConnection(ctx, conn, func(relayCtx context.Context, raw []byte) error {
		return m.handleEncryptedRelay(relayCtx, identity, session, raw)
	})
}

// serveConnection 运行单条连接的读循环。中继消息交给独立 worker 顺序处理：
// 处理过程需要等待 accepted / acknowledged 帧，而这些帧只能由读循环投递，
// 在读循环内同步处理会自锁。
func (m *realtimeManager) serveConnection(
	ctx context.Context,
	conn *websocket.Conn,
	handleRelay func(context.Context, []byte) error,
) error {
	connectionCtx, connectionCancel := context.WithCancel(ctx)
	relays := make(chan []byte, cloudRelayQueueSize)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		runRelayWorker(connectionCtx, relays, handleRelay)
	}()
	defer func() {
		connectionCancel()
		// 先关闭连接让在途发送立即失败，再等待 worker 退出，避免与重连后的回放并发处理。
		_ = conn.Close()
		<-workerDone
	}()

	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-connectionCtx.Done():
				return
			case <-ticker.C:
				m.writeMu.Lock()
				_ = conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
				m.writeMu.Unlock()
			}
		}
	}()

	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		var envelope map[string]any
		if err := json.Unmarshal(payload, &envelope); err != nil {
			continue
		}
		if envelope["type"] == "ping" {
			m.writeMu.Lock()
			err := conn.WriteJSON(map[string]string{"type": "pong"})
			m.writeMu.Unlock()
			if err != nil {
				return err
			}
			continue
		}
		if envelope["type"] == "acknowledged" {
			requestID, _ := envelope["requestId"].(string)
			m.resolveAcknowledged(requestID)
			continue
		}
		if envelope["type"] == "accepted" {
			requestID, _ := envelope["requestId"].(string)
			relayID, _ := envelope["relayId"].(string)
			duplicate, _ := envelope["duplicate"].(bool)
			m.resolveAccepted(requestID, relayAccepted{RelayID: relayID, Duplicate: duplicate})
			core.Emit(cloudAcceptedEvent, envelope)
			continue
		}
		if envelope["type"] == "error" {
			code, _ := envelope["code"].(string)
			if code == "not_paired" {
				// The relay no longer sees a grant: re-read /paired-devices before the next send.
				m.forgetDevices()
			}
			envelope["message"] = relayErrorMessage(code)
			if requestID, _ := envelope["requestId"].(string); requestID != "" {
				m.resolveAccepted(requestID, relayAccepted{Refusal: relayErrorMessage(code)})
			}
			core.Emit(cloudRealtimeEvent, envelope)
			continue
		}
		if envelope["type"] == "relay" {
			select {
			case relays <- payload:
			default:
				// 队列已满时丢弃且不 ACK，由云端可靠队列稍后重放，读循环不能被阻塞。
				core.Emit(cloudRealtimeEvent, map[string]any{
					"type": "error",
					"code": "relay_queue_full",
				})
			}
			continue
		}
		core.Emit(cloudRealtimeEvent, envelope)
	}
}

func runRelayWorker(ctx context.Context, relays <-chan []byte, handle func(context.Context, []byte) error) {
	for {
		select {
		case <-ctx.Done():
			return
		case payload := <-relays:
			if ctx.Err() != nil {
				return
			}
			if err := handle(ctx, payload); err != nil {
				core.Emit(cloudRealtimeEvent, map[string]any{
					"type": "error",
					"code": "decrypt_failed",
				})
			}
		}
	}
}

func (m *realtimeManager) handleEncryptedRelay(
	ctx context.Context,
	identity deviceIdentity,
	session persistedSession,
	raw []byte,
) error {
	var envelope cloudRelayEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return err
	}
	if envelope.RelayID == "" || envelope.RequestID == "" || envelope.SourceDeviceID == "" || envelope.Channel == "" {
		return errors.New("云中继信封字段不完整")
	}
	source, err := m.cloudDevice(ctx, session, identity, envelope.SourceDeviceID)
	if errors.Is(err, errDeviceNotPaired) {
		return errors.New("云消息来源设备未授权")
	}
	if err != nil {
		return err
	}
	key, err := deriveSharedKey(identity, source)
	if err != nil {
		return err
	}
	payload, err := decryptCloudPayload(envelope.Ciphertext, key, envelope.RequestID, envelope.Channel)
	if err != nil {
		return err
	}
	event := CloudPayloadEvent{
		RelayID:        envelope.RelayID,
		RequestID:      envelope.RequestID,
		SourceDeviceID: envelope.SourceDeviceID,
		Channel:        envelope.Channel,
		Sequence:       envelope.Sequence,
		Payload:        payload,
		Replayed:       envelope.Replayed,
	}
	if event.Channel == cloudTerminalResponseChannel || event.Channel == "ssh.response" || event.Channel == "ssh.profiles.response" || event.Channel == "database.response" {
		core.Emit(cloudPayloadEvent, event)
		return nil
	}
	if event.Channel == "ssh.profiles.list" {
		profiles, operationErr := listCloudSSHProfiles()
		response := map[string]any{
			"requestId": event.RequestID,
			"success":   operationErr == nil,
			"profiles":  profiles,
		}
		if operationErr != nil {
			response["error"] = core.TakeChars(operationErr.Error(), 240)
		}
		if _, err := sendEncryptedCloudPayload(event.SourceDeviceID, "ssh.profiles.response", event.RequestID, event.Sequence, response); err != nil {
			return err
		}
		ackCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		return m.acknowledge(ackCtx, event.RequestID, event.RelayID)
	}
	if handled, operationErr := cloudDatabases.handlePayload(event); handled {
		if operationErr != nil {
			return operationErr
		}
		ackCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		return m.acknowledge(ackCtx, event.RequestID, event.RelayID)
	}
	if event.Channel == "ssh.session.create" {
		// SSH 连接可能要等手机输入验证码，放到后台进行，处理线程继续接收后续消息（包括验证码回答）。
		if err := cloudTerminals.start(); err != nil {
			return err
		}
		cloudTerminals.startSSHCreate(event, startCloudSSHProfile, func(operationErr error) {
			respondCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := m.respondTerminalOperation(respondCtx, envelope, payload, operationErr); err != nil {
				log.Printf("cloud ssh create response failed: %v", err)
			}
		})
		return nil
	}
	handled, operationErr := cloudTerminals.handlePayload(event)
	if handled {
		return m.respondTerminalOperation(ctx, envelope, payload, operationErr)
	}
	if operationErr != nil {
		return operationErr
	}
	core.Emit(cloudPayloadEvent, event)
	return nil
}

// pairedDeviceCacheTTL bounds how long the /paired-devices snapshot is reused for relay encryption and
// decryption. It is also dropped on reconnect, on a not_paired refusal and after a local pairing change.
const pairedDeviceCacheTTL = 45 * time.Second

// errDeviceNotPaired means the device has no active grant with this desktop in a fresh /paired-devices read.
var errDeviceNotPaired = errors.New("云设备未授权")

// cloudDevice returns a paired peer, reusing the cached /paired-devices snapshot while it is fresh.
// A cache miss re-reads the list once, so a newly paired phone is found without waiting for expiry.
func (m *realtimeManager) cloudDevice(
	ctx context.Context,
	session persistedSession,
	identity deviceIdentity,
	deviceID string,
) (serverDevice, error) {
	m.mu.RLock()
	device, found := m.deviceCache[deviceID]
	fresh := time.Since(m.deviceCacheAt) < pairedDeviceCacheTTL
	m.mu.RUnlock()
	if found && fresh {
		return device, nil
	}

	// Only devices with an active grant to this desktop can be message sources.
	var devices []pairedDevice
	if err := authorizedCloudGet(ctx, session, identity, "/paired-devices", &devices); err != nil {
		return serverDevice{}, err
	}
	cache := make(map[string]serverDevice, len(devices))
	for _, candidate := range devices {
		cache[candidate.DeviceID] = candidate.serverDevice
	}
	m.mu.Lock()
	m.deviceCache = cache
	m.deviceCacheAt = time.Now()
	m.mu.Unlock()
	device, found = cache[deviceID]
	if !found {
		return serverDevice{}, errDeviceNotPaired
	}
	return device, nil
}

// forgetDevices drops the cached paired devices so the next relay re-reads them after a pairing change.
func (m *realtimeManager) forgetDevices() {
	m.mu.Lock()
	m.deviceCache = nil
	m.deviceCacheAt = time.Time{}
	m.mu.Unlock()
}

func (m *realtimeManager) send(value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("编码云实时消息: %w", err)
	}
	m.mu.RLock()
	conn := m.conn
	m.mu.RUnlock()
	if conn == nil {
		return errors.New("MinCode Cloud 实时通道未连接")
	}
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		return fmt.Errorf("发送云实时消息: %w", err)
	}
	return nil
}

func (m *realtimeManager) acknowledge(
	ctx context.Context,
	requestID string,
	relayID string,
) error {
	waiter := make(chan struct{}, 1)
	m.mu.Lock()
	if m.ackWaiters == nil {
		m.ackWaiters = make(map[string]chan struct{})
	}
	if _, exists := m.ackWaiters[requestID]; exists {
		m.mu.Unlock()
		return errors.New("同一云消息正在等待 ACK 确认")
	}
	m.ackWaiters[requestID] = waiter
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		if m.ackWaiters[requestID] == waiter {
			delete(m.ackWaiters, requestID)
		}
		m.mu.Unlock()
	}()
	if err := m.send(map[string]any{"type": "ack", "requestId": requestID, "relayId": relayID}); err != nil {
		return err
	}
	select {
	case <-waiter:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("等待云端 ACK 确认: %w", ctx.Err())
	}
}

func (m *realtimeManager) resolveAcknowledged(requestID string) {
	m.mu.RLock()
	waiter := m.ackWaiters[requestID]
	m.mu.RUnlock()
	if waiter == nil {
		return
	}
	select {
	case waiter <- struct{}{}:
	default:
	}
}

func (m *realtimeManager) sendAndWaitAccepted(
	ctx context.Context,
	requestID string,
	value any,
) (relayAccepted, error) {
	if requestID == "" {
		return relayAccepted{}, errors.New("云消息 requestId 不能为空")
	}
	waiter := make(chan relayAccepted, 1)
	m.mu.Lock()
	if m.acceptedWaiters == nil {
		m.acceptedWaiters = make(map[string]chan relayAccepted)
	}
	if _, exists := m.acceptedWaiters[requestID]; exists {
		m.mu.Unlock()
		return relayAccepted{}, errors.New("同一云消息正在等待服务端确认")
	}
	m.acceptedWaiters[requestID] = waiter
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		if m.acceptedWaiters[requestID] == waiter {
			delete(m.acceptedWaiters, requestID)
		}
		m.mu.Unlock()
	}()

	if err := m.send(value); err != nil {
		return relayAccepted{}, err
	}
	select {
	case accepted := <-waiter:
		if accepted.Refusal != "" {
			return relayAccepted{}, errors.New(accepted.Refusal)
		}
		if accepted.RelayID == "" {
			return relayAccepted{}, errors.New("云端返回的 relayId 无效")
		}
		return accepted, nil
	case <-ctx.Done():
		return relayAccepted{}, fmt.Errorf("等待云端可靠队列确认: %w", ctx.Err())
	}
}

func (m *realtimeManager) resolveAccepted(requestID string, accepted relayAccepted) {
	m.mu.RLock()
	waiter := m.acceptedWaiters[requestID]
	m.mu.RUnlock()
	if waiter == nil {
		return
	}
	select {
	case waiter <- accepted:
	default:
	}
}

func (m *realtimeManager) setConnected(generation uint64, conn *websocket.Conn) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.generation != generation || m.cancel == nil {
		return false
	}
	m.conn = conn
	// Pairings may have changed while disconnected.
	m.deviceCache = nil
	m.deviceCacheAt = time.Time{}
	m.state = RealtimeState{Connected: true, ConnectedAt: time.Now().UnixMilli()}
	go m.emitState()
	return true
}

func (m *realtimeManager) clearConnection(generation uint64, conn *websocket.Conn) {
	m.mu.Lock()
	if m.generation == generation && m.conn == conn {
		m.conn = nil
		m.state.Connected = false
		m.state.Connecting = true
	}
	m.mu.Unlock()
	m.emitState()
}

func (m *realtimeManager) setError(generation uint64, err error) {
	m.mu.Lock()
	if m.generation == generation {
		m.state = RealtimeState{Connecting: true, LastError: core.TakeChars(err.Error(), 240)}
	}
	m.mu.Unlock()
	m.emitState()
}

func (m *realtimeManager) emitState() {
	core.Emit(cloudStatusEvent, m.snapshot())
}

func cloudWebSocketURL(apiBaseURL string) (string, error) {
	if configured := strings.TrimSpace(os.Getenv("MINCODE_CLOUD_WS_URL")); configured != "" {
		return validateWebSocketURL(configured)
	}
	parsed, err := url.Parse(apiBaseURL)
	if err != nil {
		return "", errors.New("MinCode Cloud 地址无效")
	}
	parsed.Path = cloudWSPath
	parsed.RawQuery = ""
	parsed.Fragment = ""
	switch parsed.Scheme {
	case "https":
		parsed.Scheme = "wss"
	case "http":
		parsed.Scheme = "ws"
	default:
		return "", errors.New("MinCode Cloud 协议无效")
	}
	return validateWebSocketURL(parsed.String())
}

func validateWebSocketURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("MinCode Cloud WebSocket 地址无效")
	}
	if parsed.Scheme != "wss" {
		host := parsed.Hostname()
		ip := net.ParseIP(host)
		if parsed.Scheme != "ws" || (!strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback())) {
			return "", errors.New("MinCode Cloud WebSocket 仅允许 WSS；WS 仅限本机开发")
		}
	}
	return parsed.String(), nil
}

// respondTerminalOperation 回复手机的终端 / SSH 操作结果并确认该消息已处理。
func (m *realtimeManager) respondTerminalOperation(
	ctx context.Context,
	envelope cloudRelayEnvelope,
	payload map[string]any,
	operationErr error,
) error {
	response := map[string]any{
		"requestId": envelope.RequestID,
		"channel":   envelope.Channel,
		"success":   operationErr == nil,
	}
	if sessionID, ok := payload["sessionId"].(string); ok {
		response["sessionId"] = sessionID
	}
	if operationErr != nil {
		response["error"] = core.TakeChars(operationErr.Error(), 240)
	}
	responseChannel := "terminal.response"
	if strings.HasPrefix(envelope.Channel, "ssh.") {
		responseChannel = "ssh.response"
	}
	if _, err := sendEncryptedCloudPayload(
		envelope.SourceDeviceID,
		responseChannel,
		envelope.RequestID,
		envelope.Sequence,
		response,
	); err != nil {
		return err
	}
	ackCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return m.acknowledge(ackCtx, envelope.RequestID, envelope.RelayID)
}
