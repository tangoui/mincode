package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"mincode/app/backend/core"
	"mincode/app/plugins/api"
)

// Free-form view messages, modelled on VS Code's webview postMessage: a view
// and its own plugin process exchange arbitrary JSON values.
//
//   - view → process: the host sends the request plugin.ui.message
//     {viewId, message}; the reply acknowledges delivery, so the view's
//     postMessage settles once the process accepted it.
//   - process → views: host.ui.postMessage {message, viewId?} (ui capability),
//     normally sent as a notification. Notifications are handled on the stdout
//     reader, so the views receive them in the order the process wrote them.
//     Without viewId every open view of the plugin's current generation
//     receives the message.
//
// Messages never pass the host's risk confirmation and are never offered to AI:
// like plugin.call they carry notifications and data only, so writes and
// dangerous operations belong in manifest actions.
const (
	pluginUIMessageMethod   = "plugin.ui.message"
	hostUIPostMessageMethod = "host.ui.postMessage"
	pluginUIMessageEvent    = "plugin:ui-message"
	pluginUIMessageMaxBytes = 256 * 1024
	hostUIMessagesPerSecond = 500
)

// hostUIMessageLimiter bounds process → view messages per plugin; a variable
// so tests can tighten it.
var hostUIMessageLimiter = newHostRateLimiter(hostUIMessagesPerSecond, time.Second)

// hostUIMessageDropLog reports dropped notifications at most once a second per plugin.
var hostUIMessageDropLog = newHostRateLimiter(1, time.Second)

func init() {
	core.Register("plugin_host_ui_post_message", pluginHostUIPostMessageHandler)
	registerGenerationHostMethod(hostUIPostMessageMethod, nil, api.CapabilityUI, postUIMessageFromProcess)
}

func validUIMessage(message json.RawMessage) bool {
	return len(message) > 0 && len(message) <= pluginUIMessageMaxBytes && string(message) != "null" && json.Valid(message)
}

// postUIMessageFromProcess validates host.ui.postMessage and pushes the
// message to the plugin's views.
func postUIMessageFromProcess(r *processRuntime, generation uint64, params json.RawMessage) (any, *rpcError) {
	if len(params) > pluginUIMessageMaxBytes+512 {
		return nil, &rpcError{Code: -32602, Message: fmt.Sprintf("view message exceeds %d bytes", pluginUIMessageMaxBytes)}
	}
	var p struct {
		Message json.RawMessage `json:"message"`
		ViewID  string          `json:"viewId"`
	}
	if err := json.Unmarshal(params, &p); err != nil || !validUIMessage(p.Message) {
		return nil, &rpcError{Code: -32602, Message: fmt.Sprintf("message must be a non-null JSON value up to %d bytes", pluginUIMessageMaxBytes)}
	}
	if p.ViewID != "" && !hostViewIDPattern.MatchString(p.ViewID) {
		return nil, &rpcError{Code: -32602, Message: "invalid viewId"}
	}
	r.mu.Lock()
	current := generation == r.generation
	r.mu.Unlock()
	if !current {
		return nil, &rpcError{Code: -32001, Message: "plugin process generation changed"}
	}
	if !hostUIMessageLimiter.allow(r.pluginID) {
		return nil, &rpcError{Code: -32029, Message: fmt.Sprintf("more than %d view messages per second", hostUIMessagesPerSecond)}
	}
	core.Emit(pluginUIMessageEvent, map[string]any{
		"pluginId":   r.pluginID,
		"generation": generation,
		"viewId":     p.ViewID,
		"message":    p.Message,
	})
	return true, nil
}

// uiMessageNotification handles a host.ui.postMessage notification inline on
// the stdout reader to keep the process's message order. It never replies.
func (r *processRuntime) uiMessageNotification(generation uint64, params json.RawMessage) {
	if _, granted := r.capabilities[api.CapabilityUI]; !granted {
		if hostUIMessageDropLog.allow(r.pluginID) {
			r.logMessage("error", hostUIPostMessageMethod+" requires the ui capability")
		}
		return
	}
	if _, rpcErr := postUIMessageFromProcess(r, generation, params); rpcErr != nil && hostUIMessageDropLog.allow(r.pluginID) {
		r.logMessage("error", hostUIPostMessageMethod+" dropped: "+rpcErr.Message)
	}
}

// deliverUIMessage sends one view message to the process and waits for its acknowledgement.
func (r *processRuntime) deliverUIMessage(ctx context.Context, generation uint64, viewID string, message json.RawMessage) (json.RawMessage, error) {
	if !validUIMessage(message) {
		return nil, fmt.Errorf("view message must be a non-null JSON value up to %d bytes", pluginUIMessageMaxBytes)
	}
	params, err := json.Marshal(map[string]any{"viewId": viewID, "message": message})
	if err != nil {
		return nil, err
	}
	return r.callGeneration(ctx, generation, pluginUIMessageMethod, params)
}

type hostUIPostMessageArgs struct {
	SessionID string          `json:"sessionId"`
	Nonce     string          `json:"nonce"`
	Message   json.RawMessage `json:"message"`
}

func pluginHostUIPostMessageHandler(_, argsJSON string) (string, error) {
	var args hostUIPostMessageArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if err := host().PostUIMessage(context.Background(), args.SessionID, args.Nonce, args.Message); err != nil {
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
	return core.EncodeResult(true)
}

// PostUIMessage forwards a message from a live view to its plugin process.
func (manager *pluginManager) PostUIMessage(ctx context.Context, sessionID, nonce string, message json.RawMessage) error {
	if !validUIMessage(message) {
		return fmt.Errorf("view message must be a non-null JSON value up to %d bytes", pluginUIMessageMaxBytes)
	}
	ctx, cancel := context.WithTimeout(ctx, pluginInvokeTimeout)
	defer cancel()
	admitted, err := manager.admitUISession(ctx, cancel, sessionID, nonce)
	if err != nil {
		return err
	}
	defer admitted.release()
	_, err = admitted.runtime.deliverUIMessage(ctx, admitted.session.Generation, admitted.session.ViewID, message)
	return err
}
