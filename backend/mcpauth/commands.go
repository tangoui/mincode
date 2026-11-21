package mcpauth

// frontend commands for connector sign-in (argument and result JSON is camelCase):
//
//	mcp_oauth_login        {serverId, url} -> {status: "signed-in" | "cancelled"}
//	mcp_oauth_logout       {serverId}      -> void
//	mcp_oauth_status       {serverId, url} -> {status: "signed-out" | "signed-in" | "needs-login"}
//	mcp_oauth_access_token {serverId, url} -> {token}
//
// During device flow mcp_oauth_login emits DeviceCodeEvent with
// {serverId, userCode, verificationUri}. Failures are errors whose text starts
// with a stable "[MCP_AUTH_*]" code. mcp_oauth_access_token is the only path
// that returns a token, for handing a signed-in connector to an external ACP
// agent as an Authorization header (spec §7.2); nothing here logs tokens.

import (
	"context"
	"strings"
	"sync"

	"mincode/app/backend/core"
)

// DeviceCodeEvent tells the UI which code to enter on the verification page.
const DeviceCodeEvent = "mcp-oauth-device-code"

var (
	openerMu sync.RWMutex
	opener   func(url string) error
)

// SetBrowserOpener installs the system-browser opener used by
// mcp_oauth_login. The backend package wires computer.OpenSystemBrowser in;
// this package does not import it because that package needs cgo on macOS.
func SetBrowserOpener(open func(url string) error) {
	openerMu.Lock()
	defer openerMu.Unlock()
	opener = open
}

func browserOpener() func(string) error {
	openerMu.RLock()
	defer openerMu.RUnlock()
	return opener
}

func init() {
	core.RegisterCtx("mcp_oauth_login", loginHandler)
	core.Register("mcp_oauth_logout", logoutHandler)
	core.Register("mcp_oauth_status", statusHandler)
	core.RegisterCtx("mcp_oauth_access_token", accessTokenHandler)
}

type connectorArgs struct {
	ServerID string `json:"serverId"`
	URL      string `json:"url"`
}

func decodeConnectorArgs(argsJSON string) (connectorArgs, error) {
	var args connectorArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return args, err
	}
	if err := validateServerID(args.ServerID); err != nil {
		return args, err
	}
	if _, err := checkURL(args.URL, "连接器"); err != nil {
		return args, err
	}
	if len(args.URL) > maxRecordURLBytes {
		return args, authError("MCP_AUTH_INSECURE_URL", "连接器地址过长")
	}
	return args, nil
}

type deviceCodePayload struct {
	ServerID        string `json:"serverId"`
	UserCode        string `json:"userCode"`
	VerificationURI string `json:"verificationUri"`
}

func loginHandler(ctx context.Context, _ string, argsJSON string) (string, error) {
	args, err := decodeConnectorArgs(argsJSON)
	if err != nil {
		return "", err
	}
	open := browserOpener()
	err = Login(ctx, args.ServerID, args.URL, open, func(userCode, verifyURL string) {
		core.Emit(DeviceCodeEvent, deviceCodePayload{ServerID: args.ServerID, UserCode: userCode, VerificationURI: verifyURL})
	})
	if err != nil {
		if strings.HasPrefix(err.Error(), "[MCP_AUTH_CANCELLED]") {
			return core.EncodeResult(map[string]string{"status": "cancelled"})
		}
		return "", err
	}
	return core.EncodeResult(map[string]string{"status": StatusSignedIn})
}

func logoutHandler(_ string, argsJSON string) (string, error) {
	var args struct {
		ServerID string `json:"serverId"`
	}
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	return "", Logout(args.ServerID)
}

func statusHandler(_ string, argsJSON string) (string, error) {
	args, err := decodeConnectorArgs(argsJSON)
	if err != nil {
		return "", err
	}
	status, err := Status(args.ServerID, args.URL)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(map[string]string{"status": status})
}

func accessTokenHandler(ctx context.Context, _ string, argsJSON string) (string, error) {
	args, err := decodeConnectorArgs(argsJSON)
	if err != nil {
		return "", err
	}
	token, err := AccessToken(ctx, args.ServerID, args.URL)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(map[string]string{"token": token})
}
