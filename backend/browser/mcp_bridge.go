package browser

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	_ "mincode/app/backend/computer"
	"mincode/app/backend/webautomation"
)

const internalMCPBridgePath = "/internal/browser-mcp/v1/tool"

type InternalMCPLease struct {
	URL       string
	Token     string
	ServerID  string
	SessionID string
}

type internalMCPLeaseState struct {
	ServerID  string
	SessionID string
	CreatedAt time.Time
}

type internalMCPBridgeRequest struct {
	ToolName  string         `json:"toolName"`
	Arguments map[string]any `json:"arguments"`
}

type internalMCPBridgeResponse struct {
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
	// Code and Details are set for structured browser tool errors (dialog_pending, ...).
	Code    string         `json:"code,omitempty"`
	Details map[string]any `json:"details,omitempty"`
}

type internalMCPBridge struct {
	mu       sync.RWMutex
	listener net.Listener
	server   *http.Server
	endpoint string
	leases   map[string]*internalMCPLeaseState
	execute  func(string, map[string]any) (any, error)
}

var browserMCPBridge = &internalMCPBridge{
	leases:  make(map[string]*internalMCPLeaseState),
	execute: executeInternalMCPTool,
}

func executeInternalMCPTool(toolName string, arguments map[string]any) (any, error) {
	return dispatchInternalMCPTool(toolName, arguments, ExecuteAITool, func(name string, args map[string]any) (any, error) {
		return webautomation.Execute(webautomation.DefaultDriverID, name, args)
	})
}

func dispatchInternalMCPTool(
	toolName string,
	arguments map[string]any,
	browserExecutor func(string, map[string]any) (any, error),
	computerExecutor func(string, map[string]any) (any, error),
) (any, error) {
	switch {
	case strings.HasPrefix(toolName, "browser."):
		return browserExecutor(toolName, arguments)
	case strings.HasPrefix(toolName, "computer."):
		return computerExecutor(toolName, arguments)
	default:
		return nil, fmt.Errorf("unsupported internal MCP tool: %s", toolName)
	}
}

func (b *internalMCPBridge) ensureStarted() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.listener != nil {
		return nil
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("start internal MinCode MCP bridge: %w", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc(internalMCPBridgePath, b.handleTool)
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      45 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	b.listener = listener
	b.server = server
	b.endpoint = "http://" + listener.Addr().String() + internalMCPBridgePath
	go func() {
		if serveErr := server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			log.Printf("internal MinCode MCP bridge stopped unexpectedly: %v", serveErr)
		}
	}()
	return nil
}

func randomBridgeToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate internal MinCode MCP token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func NewInternalMCPLease(serverID, sessionID string) (InternalMCPLease, error) {
	serverID = strings.TrimSpace(serverID)
	if serverID == "" {
		return InternalMCPLease{}, errors.New("serverId is required for internal browser MCP")
	}
	if err := browserMCPBridge.ensureStarted(); err != nil {
		return InternalMCPLease{}, err
	}
	token, err := randomBridgeToken()
	if err != nil {
		return InternalMCPLease{}, err
	}
	browserMCPBridge.mu.Lock()
	browserMCPBridge.leases[token] = &internalMCPLeaseState{
		ServerID:  serverID,
		SessionID: strings.TrimSpace(sessionID),
		CreatedAt: time.Now(),
	}
	endpoint := browserMCPBridge.endpoint
	browserMCPBridge.mu.Unlock()
	return InternalMCPLease{URL: endpoint, Token: token, ServerID: serverID, SessionID: strings.TrimSpace(sessionID)}, nil
}

func BindInternalMCPLease(token, serverID, sessionID string) error {
	browserMCPBridge.mu.Lock()
	defer browserMCPBridge.mu.Unlock()
	lease := browserMCPBridge.leases[token]
	if lease == nil || subtle.ConstantTimeCompare([]byte(lease.ServerID), []byte(strings.TrimSpace(serverID))) != 1 {
		return errors.New("internal MinCode MCP lease is unavailable")
	}
	lease.SessionID = strings.TrimSpace(sessionID)
	return nil
}

func RevokeInternalMCPLease(token string) {
	browserMCPBridge.mu.Lock()
	delete(browserMCPBridge.leases, token)
	browserMCPBridge.mu.Unlock()
}

func RevokeInternalMCPServer(serverID string) {
	serverID = strings.TrimSpace(serverID)
	browserMCPBridge.mu.Lock()
	for token, lease := range browserMCPBridge.leases {
		if lease.ServerID == serverID {
			delete(browserMCPBridge.leases, token)
		}
	}
	browserMCPBridge.mu.Unlock()
}

func ShutdownInternalMCP(ctx context.Context) {
	browserMCPBridge.mu.Lock()
	server := browserMCPBridge.server
	browserMCPBridge.server = nil
	browserMCPBridge.listener = nil
	browserMCPBridge.endpoint = ""
	browserMCPBridge.leases = make(map[string]*internalMCPLeaseState)
	browserMCPBridge.mu.Unlock()
	if server != nil {
		_ = server.Shutdown(ctx)
	}
}

func (b *internalMCPBridge) authenticate(request *http.Request) *internalMCPLeaseState {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil || !net.ParseIP(host).IsLoopback() {
		return nil
	}
	authorization := strings.TrimSpace(request.Header.Get("Authorization"))
	if !strings.HasPrefix(authorization, "Bearer ") {
		return nil
	}
	candidate := strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer "))
	b.mu.RLock()
	defer b.mu.RUnlock()
	for token, lease := range b.leases {
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(token)) == 1 {
			copy := *lease
			return &copy
		}
	}
	return nil
}

func (b *internalMCPBridge) handleTool(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Type", "application/json")
	if request.Method != http.MethodPost {
		response.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(response).Encode(internalMCPBridgeResponse{Error: "method not allowed"})
		return
	}
	lease := b.authenticate(request)
	if lease == nil {
		response.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(response).Encode(internalMCPBridgeResponse{Error: "unauthorized"})
		return
	}
	var payload internalMCPBridgeRequest
	decoder := json.NewDecoder(io.LimitReader(request.Body, 2<<20))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		response.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(response).Encode(internalMCPBridgeResponse{Error: "invalid request"})
		return
	}
	payload.ToolName = strings.TrimSpace(payload.ToolName)
	if !isInternalMCPTool(payload.ToolName) {
		response.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(response).Encode(internalMCPBridgeResponse{Error: "tool is not available through MinCode ACP MCP"})
		return
	}
	if payload.Arguments == nil {
		payload.Arguments = map[string]any{}
	}
	result, err := b.execute(payload.ToolName, payload.Arguments)
	log.Printf("internal MinCode MCP server=%q session=%q tool=%q ok=%t", lease.ServerID, lease.SessionID, payload.ToolName, err == nil)
	if err != nil {
		failure := internalMCPBridgeResponse{Error: err.Error()}
		var toolErr *browserToolError
		if errors.As(err, &toolErr) {
			failure.Code, failure.Details = toolErr.Code, toolErr.Details
		}
		response.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(response).Encode(failure)
		return
	}
	_ = json.NewEncoder(response).Encode(internalMCPBridgeResponse{Result: result})
}
