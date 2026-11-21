package plugin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"mincode/app/backend/core"
)

// Plugin views may call methods the plugin process registers itself
// (plugin.call), in addition to manifest actions, and exchange binary frames
// with a framed-transport process. Unlike actions these are view-only: they
// have no risk confirmation, are never offered to AI, and are not listed in the
// manifest, so writes and dangerous operations belong in actions.
const (
	pluginUICallMethod   = "plugin.call"
	pluginUIBinaryEvent  = "plugin:ui-binary"
	pluginUICallMaxBytes = 256 * 1024
	pluginUIMethodMaxLen = 128
)

// Reserved prefixes keep view calls out of the protocol's own namespaces.
var pluginUIMethodPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(?:[./:-][A-Za-z0-9_]+)*$`)

func validPluginUIMethod(method string) bool {
	if len(method) > pluginUIMethodMaxLen || !pluginUIMethodPattern.MatchString(method) {
		return false
	}
	lower := strings.ToLower(method)
	return !strings.HasPrefix(lower, "plugin.") && !strings.HasPrefix(lower, "host.") && !strings.HasPrefix(lower, "rpc.")
}

func init() {
	core.Register("plugin_host_ui_call", pluginHostUICallHandler)
	core.Register("plugin_host_ui_send_binary", pluginHostUISendBinaryHandler)
	core.Register("plugin_host_ui_direct_token", pluginHostUIDirectTokenHandler)
}

type hostUIDirectTokenArgs struct {
	SessionID string `json:"sessionId"`
	Nonce     string `json:"nonce"`
}

func pluginHostUIDirectTokenHandler(_, argsJSON string) (string, error) {
	var args hostUIDirectTokenArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	ticket, err := host().IssueUIDirectToken(args.SessionID, args.Nonce)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(ticket)
}

// IssueUIDirectToken lets a live, current view session open the direct binary
// channel (ui_direct.go).
func (manager *pluginManager) IssueUIDirectToken(sessionID, nonce string) (pluginUIDirectToken, error) {
	ctx, cancel := context.WithTimeout(context.Background(), pluginInvokeTimeout)
	defer cancel()
	admitted, err := manager.admitUISession(ctx, cancel, sessionID, nonce)
	if err != nil {
		return pluginUIDirectToken{}, err
	}
	admitted.release()
	return manager.uiHost.IssueDirectToken(sessionID, nonce)
}

type hostUICallArgs struct {
	SessionID string          `json:"sessionId"`
	Nonce     string          `json:"nonce"`
	Method    string          `json:"method"`
	Params    json.RawMessage `json:"params"`
}

func pluginHostUICallHandler(_, argsJSON string) (string, error) {
	var args hostUICallArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	result, err := host().CallUIMethod(context.Background(), args.SessionID, args.Nonce, args.Method, args.Params)
	if err != nil {
		var rpcErr *processRPCError
		if errors.As(err, &rpcErr) {
			return core.EncodeResult(map[string]any{
				"__mincodePluginError": true,
				"code":                 rpcErr.Code,
				"message":              rpcErr.Message,
				"data":                 rpcErr.Data,
			})
		}
		return "", err
	}
	return core.EncodeResult(result)
}

type hostUISendBinaryArgs struct {
	SessionID string `json:"sessionId"`
	Nonce     string `json:"nonce"`
	Channel   string `json:"channel"`
	Data      string `json:"data"`
}

func pluginHostUISendBinaryHandler(_, argsJSON string) (string, error) {
	var args hostUISendBinaryArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if base64.StdEncoding.DecodedLen(len(args.Data)) > pluginBinaryMaxBytes+2 {
		return "", fmt.Errorf("binary payload exceeds %d bytes; chunk the transfer", pluginBinaryMaxBytes)
	}
	data, err := base64.StdEncoding.DecodeString(args.Data)
	if err != nil {
		return "", errors.New("binary payload is not valid base64")
	}
	if err := host().SendUIBinary(context.Background(), args.SessionID, args.Nonce, args.Channel, data); err != nil {
		return "", err
	}
	return core.EncodeResult(true)
}

// CallUIMethod sends plugin.call {method, params} for a live view session.
func (manager *pluginManager) CallUIMethod(ctx context.Context, sessionID, nonce, method string, params json.RawMessage) (json.RawMessage, error) {
	method = strings.TrimSpace(method)
	if !validPluginUIMethod(method) {
		return nil, fmt.Errorf("plugin UI method %q is invalid or reserved", method)
	}
	if len(params) == 0 {
		params = json.RawMessage("null")
	}
	if len(params) > pluginUICallMaxBytes || !json.Valid(params) {
		return nil, fmt.Errorf("plugin UI method params must be JSON up to %d bytes", pluginUICallMaxBytes)
	}
	ctx, cancel := context.WithTimeout(ctx, pluginInvokeTimeout)
	defer cancel()
	admitted, err := manager.admitUISession(ctx, cancel, sessionID, nonce)
	if err != nil {
		return nil, err
	}
	defer admitted.release()
	request, err := json.Marshal(map[string]any{"method": method, "params": params})
	if err != nil {
		return nil, err
	}
	result, err := admitted.runtime.callGeneration(ctx, admitted.session.Generation, pluginUICallMethod, request)
	if err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return json.RawMessage("null"), nil
	}
	return result, nil
}

// SendUIBinary forwards one binary frame from a live view to its plugin process.
func (manager *pluginManager) SendUIBinary(ctx context.Context, sessionID, nonce, channel string, data []byte) error {
	if !validPluginChannel(channel) {
		return errors.New("invalid binary channel name")
	}
	ctx, cancel := context.WithTimeout(ctx, pluginInvokeTimeout)
	defer cancel()
	admitted, err := manager.admitUISession(ctx, cancel, sessionID, nonce)
	if err != nil {
		return err
	}
	defer admitted.release()
	return admitted.runtime.writeBinary(ctx, admitted.session.Generation, channel, data)
}

// deliverBinary forwards a binary frame from the plugin process to the plugin's
// open views. Frames from a replaced generation are dropped.
func (runtimeHost *processRuntime) deliverBinary(generation uint64, channel string, data []byte) {
	runtimeHost.mu.Lock()
	current := generation == runtimeHost.generation
	runtimeHost.mu.Unlock()
	if !current {
		return
	}
	if manager := runtimeHost.ownerManager; manager != nil && manager.uiHost != nil {
		manager.uiHost.deliverViewBinary(runtimeHost.pluginID, generation, channel, data)
		return
	}
	core.Emit(pluginUIBinaryEvent, map[string]any{
		"pluginId":   runtimeHost.pluginID,
		"generation": generation,
		"channel":    channel,
		"data":       base64.StdEncoding.EncodeToString(data),
	})
}

// wireDirectChannel sends frames that views write to the direct channel to the
// plugin process, through the same admission as plugin_host_ui_send_binary.
func (manager *pluginManager) wireDirectChannel() {
	manager.uiHost.SetDirectInbound(func(sessionID, channel string, data []byte) bool {
		manager.uiHost.mu.RLock()
		session := manager.uiHost.sessions[sessionID]
		nonce := ""
		if session != nil {
			nonce = session.Nonce
		}
		manager.uiHost.mu.RUnlock()
		if nonce == "" {
			return false
		}
		return manager.SendUIBinary(context.Background(), sessionID, nonce, channel, data) == nil
	})
}
