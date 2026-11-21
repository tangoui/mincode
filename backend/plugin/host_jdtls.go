package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"mincode/app/backend/lsp"
	"mincode/app/plugins/api"
)

// host.jdtls.executeCommand (shell capability, plugins that declare
// contributes.jdtlsBundles; others get -32601 like a host without the method)
// sends workspace/executeCommand to the built-in jdtls running for a project:
//
//	{root: "<absolute project folder>", command: "<id>", arguments?: [...]}
//
// The call is authorized only when all of these hold:
//
//   - the command is listed in one of the caller's own jdtlsBundles entries
//     (otherwise -32001 "command is not declared by this plugin");
//   - the caller still passes the jdtlsBundles gates (enabled, not failed,
//     active in the workspace, shell granted; a disabled plugin is refused at
//     once) and owns the command through a confirmed, unchanged jar
//     (jdtlsCommandRoute; otherwise -32001);
//   - the jdtls running for root was started with the jar that declares the
//     command (otherwise -32000 "unavailable: restart the Java language
//     service to load <plugin>"; -32000 "unavailable: Java language service
//     is not running" when no jdtls runs for root).
//
// root must be absolute; it is resolved like the lsp package does (symlinks
// evaluated) and must be an existing folder. The call waits at most
// jdtlsCommandTimeout (-32603 on expiry) and a result whose reply encoding
// (compacted, HTML-escaped) is larger than jdtlsCommandResultMaxBytes is
// refused with -32603, so the reply always fits the 1 MiB plugin RPC line.
// Only the record's current runtime, in the live process generation that sent
// the request, may call; the command is bound to that generation's run
// context (stopping the plugin cancels it). Errors the language service returns come back as
// -32000 with its message (bounded).
const (
	jdtlsExecuteCommandMethod = "host.jdtls.executeCommand"
	jdtlsCommandTimeout       = 60 * time.Second
	// jdtlsCommandResultMaxBytes keeps the encoded reply (result plus the
	// JSON-RPC envelope) under the 1 MiB plugin RPC line limit.
	jdtlsCommandResultMaxBytes = pluginRPCMaxMessage - 4096
	jdtlsCommandErrorMaxRunes  = 1024
	jdtlsServerID              = "jdtls"
)

// jdtlsExecuteCommand is lsp.ExecuteBuiltinCommand; tests replace it.
var jdtlsExecuteCommand = lsp.ExecuteBuiltinCommand

type jdtlsExecuteCommandParams struct {
	Root      *string         `json:"root"`
	Command   *string         `json:"command"`
	Arguments json.RawMessage `json:"arguments"`
}

func init() {
	registerGenerationHostMethod(jdtlsExecuteCommandMethod, declaresJdtlsBundles, api.CapabilityShell, hostJdtlsExecuteCommand)
	markHostMethodGateNotFound(jdtlsExecuteCommandMethod)
}

// declaresJdtlsBundles is the method's gate: the runtime's manifest
// contributes at least one jdtls bundle.
func declaresJdtlsBundles(runtimeHost *processRuntime) bool {
	return len(jdtlsBundleEntries(&runtimeHost.manifest)) > 0
}

func jdtlsRPCError(code int, kind, message string) *rpcError {
	return &rpcError{Code: code, Message: message, Data: map[string]string{"code": kind}}
}

func hostJdtlsExecuteCommand(runtimeHost *processRuntime, generation uint64, raw json.RawMessage) (any, *rpcError) {
	var params jdtlsExecuteCommandParams
	if err := decodeStrictJSON(raw, &params, true); err != nil {
		return nil, &rpcError{Code: -32602, Message: "invalid host.jdtls.executeCommand params: " + err.Error()}
	}
	if params.Command == nil || *params.Command == "" {
		return nil, &rpcError{Code: -32602, Message: "command is required"}
	}
	command := *params.Command
	if params.Root == nil {
		return nil, &rpcError{Code: -32602, Message: "root is required"}
	}
	var args json.RawMessage
	if len(params.Arguments) > 0 && string(params.Arguments) != "null" {
		var list []json.RawMessage
		if json.Unmarshal(params.Arguments, &list) != nil {
			return nil, &rpcError{Code: -32602, Message: "arguments must be an array"}
		}
		args = params.Arguments
	}
	if !slices.ContainsFunc(jdtlsBundleEntries(&runtimeHost.manifest), func(bundle JdtlsBundleContribution) bool {
		return slices.Contains(bundle.Commands, command)
	}) {
		return nil, jdtlsRPCError(-32001, processLaunchErrForbidden, "command is not declared by this plugin")
	}

	// Record gates (a disabled plugin is refused here, before any filesystem
	// access), then the calling runtime must be the record's current, live
	// process generation; its run context bounds the command.
	manager := runtimeHost.ownerManager
	if manager == nil || runtimeHost.ownerRecord == nil {
		return nil, jdtlsStaleRuntimeError()
	}
	if _, err := manager.jdtlsBundleSource(runtimeHost.pluginID); err != nil {
		return nil, jdtlsRPCError(-32001, processLaunchErrForbidden, err.Error())
	}
	runContext, live := runtimeHost.liveGenerationContext(generation)
	if !live {
		return nil, jdtlsStaleRuntimeError()
	}

	root, rpcErr := jdtlsCommandRoot(*params.Root)
	if rpcErr != nil {
		return nil, rpcErr
	}
	owner, status, jar := manager.jdtlsCommandRoute(command)
	if owner != runtimeHost.pluginID || jar == "" {
		return nil, jdtlsRPCError(-32001, processLaunchErrForbidden,
			fmt.Sprintf("command %q is not available to this plugin: its jdtls bundles are not confirmed or another plugin owns the command", command))
	}
	loaded, running := jdtlsSessionBundles(root)
	if !running {
		return nil, jdtlsRPCError(-32000, processLaunchErrUnavailable, "unavailable: Java language service is not running")
	}
	if !slices.Contains(loaded, jar) {
		name := strings.TrimSpace(status.PluginName)
		if name == "" {
			name = owner
		}
		return nil, jdtlsRPCError(-32000, processLaunchErrUnavailable, "unavailable: restart the Java language service to load "+name)
	}

	ctx, cancel := context.WithTimeout(runContext, jdtlsCommandTimeout)
	defer cancel()
	result, err := jdtlsExecuteCommand(ctx, root, jdtlsServerID, command, args)
	if err != nil {
		return nil, jdtlsCommandError(err)
	}
	if len(result) == 0 {
		return nil, nil
	}
	// Measure the result exactly as the reply encodes it: json.Marshal
	// compacts it and escapes <, >, & and U+2028/2029, which can grow it.
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, &rpcError{Code: -32603, Message: "jdtls command returned an invalid result"}
	}
	if len(encoded) > jdtlsCommandResultMaxBytes {
		return nil, &rpcError{Code: -32603, Message: "jdtls command result exceeds 1 MiB"}
	}
	return json.RawMessage(encoded), nil
}

func jdtlsStaleRuntimeError() *rpcError {
	return jdtlsRPCError(-32000, processLaunchErrUnavailable, "unavailable: this plugin process is no longer running")
}

// liveGenerationContext returns the run context of generation when the
// runtime is still its record's current runtime and that generation's process
// is running and not stopping. Lock order: record -> runtime.
func (runtimeHost *processRuntime) liveGenerationContext(generation uint64) (context.Context, bool) {
	record := runtimeHost.ownerRecord
	if record == nil {
		return nil, false
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	if record.runtime != runtimeHost {
		return nil, false
	}
	runtimeHost.mu.Lock()
	defer runtimeHost.mu.Unlock()
	if runtimeHost.generation != generation || runtimeHost.cmd == nil || runtimeHost.stopping ||
		runtimeHost.runContext == nil || runtimeHost.runContext.Err() != nil {
		return nil, false
	}
	return runtimeHost.runContext, true
}

// jdtlsCommandRoot requires an absolute path naming an existing folder and
// resolves symlinks, as the lsp package does for its session keys.
func jdtlsCommandRoot(value string) (string, *rpcError) {
	if strings.TrimSpace(value) == "" || strings.ContainsRune(value, 0) || !filepath.IsAbs(value) {
		return "", &rpcError{Code: -32602, Message: "root must be an absolute path"}
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(value))
	if err != nil {
		return "", &rpcError{Code: -32602, Message: "root must be an existing folder"}
	}
	if info, err := os.Stat(resolved); err != nil || !info.IsDir() {
		return "", &rpcError{Code: -32602, Message: "root must be an existing folder"}
	}
	return resolved, nil
}

func jdtlsCommandError(err error) *rpcError {
	message := err.Error()
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return &rpcError{Code: -32603, Message: fmt.Sprintf("jdtls command timed out after %s", jdtlsCommandTimeout)}
	case errors.Is(err, context.Canceled):
		return jdtlsRPCError(-32000, processLaunchErrUnavailable, "unavailable: the plugin is stopping")
	case strings.HasPrefix(message, processLaunchErrUnavailable+":"):
		return jdtlsRPCError(-32000, processLaunchErrUnavailable, boundedJdtlsMessage(message))
	case strings.HasPrefix(message, "invalid_request:"):
		return &rpcError{Code: -32602, Message: boundedJdtlsMessage(message)}
	}
	return jdtlsRPCError(-32000, "failed", boundedJdtlsMessage(message))
}

func boundedJdtlsMessage(message string) string {
	message = strings.ToValidUTF8(message, "�")
	if utf8.RuneCountInString(message) <= jdtlsCommandErrorMaxRunes {
		return message
	}
	return string([]rune(message)[:jdtlsCommandErrorMaxRunes]) + "…"
}
