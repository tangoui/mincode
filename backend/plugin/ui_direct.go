package plugin

import (
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"mincode/app/backend/core"
)

// The direct channel carries binary frames between a framed-transport plugin's
// view and its process over a WebSocket on the plugin UI asset server, instead
// of base64 text broadcast as a frontend event. It moves the same data to the same
// process as the bridge channel; it grants nothing new.
//
// Plugin views are opaque-origin sandboxed iframes, so every request carries
// Origin "null" and cannot identify the view. A single-use, short-lived token
// issued over the view's own MessagePort session (host bridge) is what binds
// a connection to one view session; the asset server's Host check keeps other
// local origins from reaching it by DNS rebinding.
const (
	pluginUIDirectPath     = "/__mincode/direct"
	pluginUIDirectTokenTTL = 10 * time.Second
	// A frame is [channel length][channel][payload].
	pluginUIDirectMaxMessage = 1 + pluginChannelMaxLen + pluginBinaryMaxBytes
	// Frames queued for a slow view before the connection is dropped; the view
	// then reconnects and meanwhile falls back to the bridge channel.
	pluginUIDirectSendQueue = 64
)

// directNow is replaced in tests.
var directNow = time.Now

var pluginUIDirectUpgrader = websocket.Upgrader{
	ReadBufferSize:  64 * 1024,
	WriteBufferSize: 64 * 1024,
	// Views are sandboxed without allow-same-origin, so Origin is always
	// "null"; the token, not the origin, authorizes the connection.
	CheckOrigin: func(*http.Request) bool { return true },
}

type pluginUIDirectTicket struct {
	sessionID string
	expires   time.Time
}

// pluginUIDirectToken is what the host returns to the view: the token and the
// ready-to-dial URL that carries it.
type pluginUIDirectToken struct {
	Token string `json:"token"`
	URL   string `json:"url"`
}

type pluginUIDirectConn struct {
	pluginID   string
	sessionID  string
	generation uint64
	socket     *websocket.Conn
	queue      chan []byte
	closeOnce  sync.Once
	done       chan struct{}
}

// MarkSessionFramed records that the session's plugin uses the framed
// transport, which is what makes the direct channel available to the view.
func (host *pluginUIHost) MarkSessionFramed(sessionID string) {
	host.mu.Lock()
	defer host.mu.Unlock()
	if session := host.sessions[strings.TrimSpace(sessionID)]; session != nil {
		session.Framed = true
		if session.assets != nil {
			session.assets.framed = true
		}
	}
}

// MarkSessionWebAssembly records that the session's plugin declares ui.wasm,
// which lets its pages compile WebAssembly (CSP 'wasm-unsafe-eval').
func (host *pluginUIHost) MarkSessionWebAssembly(sessionID string) {
	host.mu.Lock()
	defer host.mu.Unlock()
	if session := host.sessions[strings.TrimSpace(sessionID)]; session != nil && session.assets != nil {
		session.assets.wasm = true
	}
}

// IssueDirectToken returns a single-use token that lets the session's view open
// the direct channel. Expired tokens are swept on every issue.
func (host *pluginUIHost) IssueDirectToken(sessionID, nonce string) (pluginUIDirectToken, error) {
	token, err := randomHex(32)
	if err != nil {
		return pluginUIDirectToken{}, err
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	session := host.sessions[strings.TrimSpace(sessionID)]
	if session == nil || subtle.ConstantTimeCompare([]byte(session.Nonce), []byte(strings.TrimSpace(nonce))) != 1 {
		return pluginUIDirectToken{}, errors.New("plugin UI session is unavailable")
	}
	if !session.Framed {
		return pluginUIDirectToken{}, errors.New("plugin does not use the framed transport; declare runtime.transport \"framed\" to use the direct channel")
	}
	if host.address == "" {
		return pluginUIDirectToken{}, errors.New("plugin UI asset server is not running")
	}
	now := directNow()
	for key, ticket := range host.directTickets {
		if !now.Before(ticket.expires) {
			delete(host.directTickets, key)
		}
	}
	if host.directTickets == nil {
		host.directTickets = map[string]pluginUIDirectTicket{}
	}
	host.directTickets[token] = pluginUIDirectTicket{sessionID: session.ID, expires: now.Add(pluginUIDirectTokenTTL)}
	return pluginUIDirectToken{Token: token, URL: "ws://" + host.address + pluginUIDirectPath + "?token=" + token}, nil
}

// takeDirectTicketLocked consumes a token and returns its live session.
func (host *pluginUIHost) takeDirectTicketLocked(token string) *pluginUISession {
	ticket, ok := host.directTickets[token]
	if !ok {
		return nil
	}
	delete(host.directTickets, token)
	if !directNow().Before(ticket.expires) {
		return nil
	}
	session := host.sessions[ticket.sessionID]
	if session == nil || !session.Framed {
		return nil
	}
	return session
}

// serveDirect upgrades a token-bearing request to the direct channel. The
// caller has already checked the Host header.
func (host *pluginUIHost) serveDirect(response http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	token := query.Get("token")
	if request.Method != http.MethodGet || len(query) != 1 || len(token) != 64 || !websocket.IsWebSocketUpgrade(request) {
		http.Error(response, "invalid direct channel request", http.StatusForbidden)
		return
	}
	host.mu.Lock()
	session := host.takeDirectTicketLocked(token)
	host.mu.Unlock()
	if session == nil {
		http.Error(response, "invalid direct channel token", http.StatusForbidden)
		return
	}
	socket, err := pluginUIDirectUpgrader.Upgrade(response, request, nil)
	if err != nil {
		return
	}
	socket.SetReadLimit(pluginUIDirectMaxMessage)
	conn := &pluginUIDirectConn{
		pluginID: session.PluginID, sessionID: session.ID, generation: session.Generation,
		socket: socket, queue: make(chan []byte, pluginUIDirectSendQueue), done: make(chan struct{}),
	}
	host.mu.Lock()
	// The session may have ended while upgrading.
	if current := host.sessions[session.ID]; current == nil || current.Generation != session.Generation {
		host.mu.Unlock()
		conn.close()
		return
	}
	if host.directConns == nil {
		host.directConns = map[string]*pluginUIDirectConn{}
	}
	previous := host.directConns[session.ID]
	host.directConns[session.ID] = conn
	host.mu.Unlock()
	if previous != nil {
		previous.close()
	}
	go conn.writeLoop()
	go host.readDirect(conn)
}

// closeDirectLocked drops the session's connection and any unused tokens.
func (host *pluginUIHost) closeDirectLocked(sessionID string) {
	if conn := host.directConns[sessionID]; conn != nil {
		delete(host.directConns, sessionID)
		go conn.close()
	}
	for key, ticket := range host.directTickets {
		if ticket.sessionID == sessionID {
			delete(host.directTickets, key)
		}
	}
}

// directConnsFor returns the open connections of the plugin's views for one
// process generation; closed connections are no longer returned.
func (host *pluginUIHost) directConnsFor(pluginID string, generation uint64) []*pluginUIDirectConn {
	host.mu.RLock()
	defer host.mu.RUnlock()
	conns := make([]*pluginUIDirectConn, 0, len(host.directConns))
	for _, conn := range host.directConns {
		if conn.pluginID == pluginID && conn.generation == generation && !conn.closed() {
			conns = append(conns, conn)
		}
	}
	return conns
}

// readDirect hands frames from the view to the plugin process. It is replaced
// by the manager wiring; without it frames from the view are dropped.
func (host *pluginUIHost) readDirect(conn *pluginUIDirectConn) {
	defer func() {
		host.mu.Lock()
		if host.directConns[conn.sessionID] == conn {
			delete(host.directConns, conn.sessionID)
		}
		host.mu.Unlock()
		conn.close()
	}()
	for {
		kind, data, err := conn.socket.ReadMessage()
		if err != nil {
			return
		}
		if kind != websocket.BinaryMessage {
			return
		}
		channel, payload, ok := decodeDirectFrame(data)
		if !ok {
			return
		}
		host.mu.RLock()
		forward := host.directInbound
		host.mu.RUnlock()
		if forward != nil && !forward(conn.sessionID, channel, payload) {
			return
		}
	}
}

// send queues one frame without blocking. A full queue means the view cannot
// keep up; the connection is closed so the stdout reader is never held up.
func (conn *pluginUIDirectConn) send(channel string, data []byte) bool {
	if conn.closed() {
		return false
	}
	frame, err := encodeDirectFrame(channel, data)
	if err != nil {
		return false
	}
	select {
	case conn.queue <- frame:
		return true
	default:
		// Stop queueing now; the goodbye write may block on a peer that is not
		// reading, so it never runs on the delivering (stdout reader) goroutine.
		if conn.markClosed() {
			go func() {
				_ = conn.socket.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseGoingAway, "view too slow"), time.Now().Add(time.Second))
				_ = conn.socket.Close()
			}()
		}
		return false
	}
}

func (conn *pluginUIDirectConn) writeLoop() {
	for {
		select {
		case <-conn.done:
			return
		case frame := <-conn.queue:
			_ = conn.socket.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.socket.WriteMessage(websocket.BinaryMessage, frame); err != nil {
				conn.close()
				return
			}
		}
	}
}

func (conn *pluginUIDirectConn) closed() bool {
	select {
	case <-conn.done:
		return true
	default:
		return false
	}
}

// close marks the connection closed at once (so senders stop queueing), then
// says goodbye and releases the socket, which may block on a stuck peer.
func (conn *pluginUIDirectConn) close() {
	if !conn.markClosed() {
		return
	}
	_ = conn.socket.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseGoingAway, ""), time.Now().Add(time.Second))
	_ = conn.socket.Close()
}

// markClosed closes done once and reports whether this call did it.
func (conn *pluginUIDirectConn) markClosed() bool {
	marked := false
	conn.closeOnce.Do(func() {
		close(conn.done)
		marked = true
	})
	return marked
}

func encodeDirectFrame(channel string, data []byte) ([]byte, error) {
	if !validPluginChannel(channel) {
		return nil, errors.New("invalid binary channel name")
	}
	if len(data) > pluginBinaryMaxBytes {
		return nil, errors.New("binary payload is too large")
	}
	frame := make([]byte, 0, 1+len(channel)+len(data))
	frame = append(frame, byte(len(channel)))
	frame = append(frame, channel...)
	return append(frame, data...), nil
}

func decodeDirectFrame(frame []byte) (string, []byte, bool) {
	if len(frame) < 1 {
		return "", nil, false
	}
	length := int(frame[0])
	if length == 0 || len(frame) < 1+length {
		return "", nil, false
	}
	channel := string(frame[1 : 1+length])
	payload := frame[1+length:]
	if !validPluginChannel(channel) || len(payload) > pluginBinaryMaxBytes {
		return "", nil, false
	}
	return channel, payload, true
}

// SetDirectInbound sets where frames from views go; returning false closes the
// connection that sent the frame.
func (host *pluginUIHost) SetDirectInbound(forward func(sessionID, channel string, data []byte) bool) {
	host.mu.Lock()
	defer host.mu.Unlock()
	host.directInbound = forward
}

// deliverViewBinary sends a frame from the plugin process to its views: over
// their direct connections when any view of this generation has one, otherwise
// as the bridge event. Views keep both paths, so a view without a direct
// connection still hears frames that other views received directly.
func (host *pluginUIHost) deliverViewBinary(pluginID string, generation uint64, channel string, data []byte) {
	conns := host.directConnsFor(pluginID, generation)
	if len(conns) > 0 {
		host.mu.RLock()
		viewsWithout := 0
		for _, session := range host.sessions {
			if conn := host.directConns[session.ID]; session.PluginID == pluginID && session.Generation == generation && (conn == nil || conn.closed()) {
				viewsWithout++
			}
		}
		host.mu.RUnlock()
		for _, conn := range conns {
			conn.send(channel, data)
		}
		if viewsWithout == 0 {
			return
		}
	}
	core.Emit(pluginUIBinaryEvent, map[string]any{
		"pluginId":   pluginID,
		"generation": generation,
		"channel":    channel,
		"data":       base64.StdEncoding.EncodeToString(data),
	})
}
