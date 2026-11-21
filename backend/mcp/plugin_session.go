package mcp

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// PluginServerIDPrefix marks gateway sessions owned by a plugin's
// contributes.mcpServers entry. WebView callers cannot connect these ids
// directly; only the plugin host may start them after trust checks.
const PluginServerIDPrefix = "plugin:"

// PluginServerID returns the gateway session id for a plugin MCP server.
func PluginServerID(pluginID, serverID string) string {
	return PluginServerIDPrefix + pluginID + "/" + serverID
}

// IsPluginServerID reports whether id is reserved for plugin-owned sessions.
func IsPluginServerID(id string) bool {
	return strings.HasPrefix(id, PluginServerIDPrefix)
}

// PluginStdioSpec is a fully resolved, trusted plugin MCP server launch.
type PluginStdioSpec struct {
	ServerID       string
	OwnerPluginID  string
	Executable     string
	Args           []string
	Env            []string
	Dir            string
	Fingerprint    string
	StderrMaxBytes int
	// BeforeExec runs right before the process is created; an error refuses
	// the start (for example when plugin files changed after trust).
	BeforeExec func() error
	// Valid reports whether the owning plugin still offers this launch; a
	// session that fails it is closed when next used.
	Valid func() bool
}

type pluginSessionInfo struct {
	owner       string
	fingerprint string
	dir         string
	valid       func() bool
}

var errPluginSpec = errors.New("plugin MCP server launch is incomplete")

// ConnectPlugin starts (or reuses) a plugin-owned stdio MCP session. A live
// session with the same fingerprint and working directory is reused; any
// other session under the id is replaced.
func ConnectPlugin(spec PluginStdioSpec) (*McpConnectResult, bool, error) {
	return gateway.connectPlugin(spec)
}

// PluginSessionLive reports whether serverID currently runs the given launch.
func PluginSessionLive(serverID, fingerprint, dir string) bool {
	return gateway.pluginSessionLive(serverID, fingerprint, dir)
}

// DisconnectPlugin stops one plugin-owned session; it reports whether one ran.
func DisconnectPlugin(serverID string) bool {
	if !IsPluginServerID(serverID) {
		return false
	}
	return gateway.disconnectPlugin(serverID)
}

// DisconnectPluginOwner stops every session owned by pluginID.
func DisconnectPluginOwner(pluginID string) []string {
	return gateway.disconnectPluginOwner(pluginID)
}

func (g *mcpGateway) connectPlugin(spec PluginStdioSpec) (*McpConnectResult, bool, error) {
	if !IsPluginServerID(spec.ServerID) || spec.OwnerPluginID == "" || spec.Executable == "" || spec.Dir == "" || spec.Fingerprint == "" {
		return nil, false, errPluginSpec
	}
	ownerPrefix := PluginServerIDPrefix + spec.OwnerPluginID + "/"
	if !strings.HasPrefix(spec.ServerID, ownerPrefix) || len(spec.ServerID) == len(ownerPrefix) {
		return nil, false, fmt.Errorf("MCP server '%s' does not belong to plugin %s", spec.ServerID, spec.OwnerPluginID)
	}
	g.mu.Lock()
	if conn := g.sessions[spec.ServerID]; conn != nil && conn.plugin != nil &&
		conn.plugin.fingerprint == spec.Fingerprint && conn.plugin.dir == spec.Dir {
		result := conn.connectResult()
		g.mu.Unlock()
		return result, true, nil
	}
	g.mu.Unlock()

	rev, ctx, old := g.beginConnect(spec.ServerID)
	if old != nil {
		old.close()
	}
	stderrMax := spec.StderrMaxBytes
	if stderrMax <= 0 {
		stderrMax = 64 * 1024
	}
	cmd := exec.Command(spec.Executable, spec.Args...)
	cmd.Env = append([]string(nil), spec.Env...)
	cmd.Dir = spec.Dir
	conn, err := startStdio(ctx, spec.ServerID, spec.Executable, cmd, stderrMax, spec.BeforeExec)
	if err != nil {
		g.failConnect(spec.ServerID, rev)
		return nil, false, err
	}
	conn.plugin = &pluginSessionInfo{owner: spec.OwnerPluginID, fingerprint: spec.Fingerprint, dir: spec.Dir, valid: spec.Valid}
	result := conn.connectResult()
	if err := g.finishConnect(spec.ServerID, rev, conn); err != nil {
		return nil, false, err
	}
	return result, false, nil
}

func (g *mcpGateway) pluginSessionLive(serverID, fingerprint, dir string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	conn := g.sessions[serverID]
	return conn != nil && conn.plugin != nil && fingerprint != "" &&
		conn.plugin.fingerprint == fingerprint && conn.plugin.dir == dir
}

func (g *mcpGateway) disconnectPlugin(serverID string) bool {
	g.mu.Lock()
	_, connected := g.sessions[serverID]
	_, pending := g.connecting[serverID]
	g.mu.Unlock()
	g.disconnect(serverID)
	return connected || pending
}

func (g *mcpGateway) disconnectPluginOwner(pluginID string) []string {
	prefix := PluginServerIDPrefix + pluginID + "/"
	g.mu.Lock()
	ids := map[string]struct{}{}
	for id := range g.sessions {
		if strings.HasPrefix(id, prefix) {
			ids[id] = struct{}{}
		}
	}
	for id := range g.connecting {
		if strings.HasPrefix(id, prefix) {
			ids[id] = struct{}{}
		}
	}
	g.mu.Unlock()
	out := make([]string, 0, len(ids))
	for id := range ids {
		g.disconnect(id)
		out = append(out, id)
	}
	return out
}
