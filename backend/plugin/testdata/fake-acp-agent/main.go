// Command fake-acp-agent is a minimal ACP agent for TestPluginAcpAgentEndToEnd.
// It records how it was started in fake-acp-agent.json in its working
// directory, answers initialize over newline-delimited JSON-RPC on stdio and
// rejects every other request. It exits when stdin closes or it is killed.
package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
)

type message struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
}

func main() {
	cwd, _ := os.Getwd()
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = resolved
	}
	record, _ := json.Marshal(map[string]any{
		"pid":       os.Getpid(),
		"cwd":       cwd,
		"args":      os.Args[1:],
		"project":   os.Getenv("FAKE_ACP_AGENT_PROJECT"),
		"inherited": os.Getenv("MINCODE_E2E_ACP_INHERITED") == "1",
	})
	temporary := filepath.Join(cwd, ".fake-acp-agent.json.tmp")
	if os.WriteFile(temporary, record, 0o644) != nil || os.Rename(temporary, filepath.Join(cwd, "fake-acp-agent.json")) != nil {
		os.Exit(1)
	}

	reader := bufio.NewReader(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			var request message
			if json.Unmarshal(line, &request) == nil && len(request.ID) > 0 && request.Method != "" {
				reply := map[string]any{"jsonrpc": "2.0", "id": request.ID}
				if request.Method == "initialize" {
					var params struct {
						ProtocolVersion int `json:"protocolVersion"`
					}
					_ = json.Unmarshal(request.Params, &params)
					reply["result"] = map[string]any{"protocolVersion": params.ProtocolVersion, "agentCapabilities": map[string]any{}, "authMethods": []any{}}
				} else {
					reply["error"] = map[string]any{"code": -32601, "message": "method not found"}
				}
				if encoder.Encode(reply) != nil {
					os.Exit(1)
				}
			}
		}
		if err != nil {
			return
		}
	}
}
