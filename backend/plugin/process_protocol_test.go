package plugin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"mincode/app/backend/core"
	"mincode/app/plugins/api"
)

// TestProtocolHelperProcess is the fake plugin for the protocol tests below.
// runtime.framed speaks the framed transport and echoes binary frames;
// runtime.identified and runtime.wrong-id use JSONL and report an identity.
func TestProtocolHelperProcess(t *testing.T) {
	pluginID := os.Getenv("MINCODE_PLUGIN_ID")
	if pluginID != "runtime.framed" && pluginID != "runtime.identified" && pluginID != "runtime.wrong-id" && pluginID != "runtime.messages" {
		return
	}
	framed := pluginID == "runtime.framed"
	reader := bufio.NewReader(os.Stdin)
	writeJSON := func(message rpcEnvelope) {
		encoded, _ := json.Marshal(message)
		if framed {
			_, _ = os.Stdout.Write(encodeJSONFrame(encoded))
		} else {
			_, _ = os.Stdout.Write(append(encoded, '\n'))
		}
	}
	for {
		var payload []byte
		if framed {
			frame, err := readPluginFrame(reader, pluginRPCMaxMessage)
			if err != nil {
				os.Exit(0)
			}
			if frame.kind == pluginFrameBinary {
				echo, _ := encodeBinaryFrame("echo:"+frame.channel, append([]byte("echo:"), frame.data...))
				_, _ = os.Stdout.Write(echo)
				continue
			}
			payload = frame.json
		} else {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				os.Exit(0)
			}
			payload = line
		}
		var request rpcEnvelope
		if json.Unmarshal(payload, &request) != nil {
			continue
		}
		response := rpcEnvelope{JSONRPC: "2.0", ID: request.ID}
		switch request.Method {
		case "plugin.initialize":
			id := pluginID
			if pluginID == "runtime.wrong-id" {
				id = "someone.else"
			}
			response.Result, _ = json.Marshal(map[string]any{
				"runtimeProtocolVersion": api.RuntimeProtocolVersion,
				"plugin":                 map[string]string{"id": id, "version": "1.0.0"},
			})
		case "plugin.call":
			var params struct {
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			_ = json.Unmarshal(request.Params, &params)
			response.Result, _ = json.Marshal(map[string]any{"method": params.Method, "params": params.Params})
		case pluginUIMessageMethod:
			// Echo each view message back to the views as an ordered notification.
			var params struct {
				ViewID  string          `json:"viewId"`
				Message json.RawMessage `json:"message"`
			}
			_ = json.Unmarshal(request.Params, &params)
			echo, _ := json.Marshal(map[string]any{"viewId": params.ViewID, "message": map[string]any{"echo": params.Message}})
			writeJSON(rpcEnvelope{JSONRPC: "2.0", Method: hostUIPostMessageMethod, Params: echo})
			response.Result = json.RawMessage(`{}`)
		case "plugin.ping", "plugin.shutdown":
			response.Result = json.RawMessage(`true`)
		default:
			response.Error = &rpcError{Code: -32601, Message: "unknown"}
		}
		writeJSON(response)
		if request.Method == "plugin.shutdown" {
			os.Exit(0)
		}
	}
}

func startProtocolHelper(t *testing.T, id, transport string, capabilities ...api.Capability) (*processRuntime, error) {
	t.Helper()
	root := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	entry := "plugin-helper"
	if runtime.GOOS == "windows" {
		entry += ".exe"
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, entry), data, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		APIVersion: api.APIVersion, ID: id, Name: "Protocol Test", Version: "1.0.0", Capabilities: capabilities,
		Runtime: ProcessRuntimeManifest{Type: "process", Entry: entry, Transport: transport, Args: []string{"-test.run=TestProtocolHelperProcess"}},
	}
	runtimeHost := newProcessRuntime(root, manifest, func(string, string) {}, nil)
	t.Cleanup(func() { _ = runtimeHost.forceStop(context.Background()) })
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return runtimeHost, runtimeHost.Start(ctx)
}

func TestInitializeIdentityIsVerified(t *testing.T) {
	if _, err := startProtocolHelper(t, "runtime.identified", ""); err != nil {
		t.Fatalf("matching identity rejected: %v", err)
	}
	_, err := startProtocolHelper(t, "runtime.wrong-id", "")
	if err == nil || !strings.Contains(err.Error(), `reports id "someone.else"`) {
		t.Fatalf("mismatched identity accepted or wrong error: %v", err)
	}
}

func TestCheckInitializeIdentity(t *testing.T) {
	manifest := Manifest{ID: "vendor.example", Version: "1.2.0"}
	cases := []struct {
		result     string
		identified bool
		wantErr    string
	}{
		{`true`, false, ""},
		{`{"runtimeProtocolVersion":1}`, false, ""},
		{`{"runtimeProtocolVersion":1,"plugin":{"id":"vendor.example"}}`, true, ""},
		{`{"plugin":{"id":"vendor.example","version":"1.2.0"}}`, true, ""},
		{`{"plugin":{"id":"vendor.example","version":"1.3.0"}}`, false, "version"},
		{`{"plugin":{"id":"other"}}`, false, "reports id"},
		{`{"plugin":{}}`, false, "reports id"},
		{`{"runtimeProtocolVersion":2,"plugin":{"id":"vendor.example"}}`, false, "runtime protocol 2"},
	}
	for _, testCase := range cases {
		identified, err := checkInitializeIdentity(json.RawMessage(testCase.result), manifest)
		if identified != testCase.identified || (testCase.wantErr == "") != (err == nil) ||
			(err != nil && !strings.Contains(err.Error(), testCase.wantErr)) {
			t.Errorf("%s: identified=%v err=%v", testCase.result, identified, err)
		}
	}
}

func TestFramedTransportCarriesCallsAndBinary(t *testing.T) {
	events := make(chan map[string]any, 4)
	core.SetEmitter(func(name string, data any) {
		if name == pluginUIBinaryEvent {
			events <- data.(map[string]any)
		}
	})
	t.Cleanup(func() { core.SetEmitter(nil) })

	runtimeHost, err := startProtocolHelper(t, "runtime.framed", pluginTransportFramed)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	generation := runtimeHost.Generation()

	request, _ := json.Marshal(map[string]any{"method": "files/list", "params": map[string]string{"prefix": "docs/"}})
	result, err := runtimeHost.callGeneration(ctx, generation, pluginUICallMethod, request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result), `"method":"files/list"`) || !strings.Contains(string(result), `"prefix":"docs/"`) {
		t.Fatalf("plugin.call result = %s", result)
	}

	payload := bytes.Repeat([]byte{0, 1, 2, 0xff, '\n'}, 1024)
	if err := runtimeHost.writeBinary(ctx, generation, "pty/1", payload); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-events:
		data, _ := base64.StdEncoding.DecodeString(event["data"].(string))
		if event["pluginId"] != "runtime.framed" || event["channel"] != "echo:pty/1" || !bytes.Equal(data, append([]byte("echo:"), payload...)) {
			t.Fatalf("binary event = %v (%d bytes)", event["channel"], len(data))
		}
	case <-ctx.Done():
		t.Fatal("binary echo was not delivered")
	}
	if err := runtimeHost.writeBinary(ctx, generation, "../bad channel", nil); err == nil {
		t.Fatal("invalid channel accepted")
	}
}

func TestJSONLTransportRejectsBinary(t *testing.T) {
	runtimeHost, err := startProtocolHelper(t, "runtime.identified", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := runtimeHost.writeBinary(context.Background(), runtimeHost.Generation(), "data", []byte("x")); err == nil || !strings.Contains(err.Error(), "framed") {
		t.Fatalf("binary on JSONL transport: %v", err)
	}
}

func TestPluginFrameCodecLimits(t *testing.T) {
	var stream bytes.Buffer
	stream.Write(encodeJSONFrame([]byte(`{"jsonrpc":"2.0"}`)))
	binaryFrame, err := encodeBinaryFrame("chan-1", []byte("raw"))
	if err != nil {
		t.Fatal(err)
	}
	stream.Write(binaryFrame)
	reader := bufio.NewReader(&stream)
	frame, err := readPluginFrame(reader, pluginRPCMaxMessage)
	if err != nil || frame.kind != pluginFrameJSON || string(frame.json) != `{"jsonrpc":"2.0"}` {
		t.Fatalf("json frame = %+v, %v", frame, err)
	}
	frame, err = readPluginFrame(reader, pluginRPCMaxMessage)
	if err != nil || frame.channel != "chan-1" || string(frame.data) != "raw" {
		t.Fatalf("binary frame = %+v, %v", frame, err)
	}
	if _, err := readPluginFrame(reader, pluginRPCMaxMessage); err != io.EOF {
		t.Fatalf("end of stream = %v", err)
	}

	for _, header := range [][]byte{
		{9, 0, 0, 0, 1},    // unknown kind
		{0, 0, 0x10, 0, 0}, // JSON frame of 1 MiB
		{1, 0x01, 0, 0, 0}, // binary frame of 16 MiB
		{1, 0, 0, 0, 1},    // binary frame too short for a channel length
	} {
		if _, err := readPluginFrame(bufio.NewReader(bytes.NewReader(header)), pluginRPCMaxMessage); err == nil || err == io.EOF {
			t.Errorf("header %v accepted: %v", header, err)
		}
	}
	if _, err := encodeBinaryFrame("ok", make([]byte, pluginBinaryMaxBytes+1)); err == nil {
		t.Fatal("oversized binary accepted")
	}
}

func TestValidPluginUIMethod(t *testing.T) {
	for _, method := range []string{"list", "files/list", "db.query", "ssh:exec", "a_b-c"} {
		if !validPluginUIMethod(method) {
			t.Errorf("%q rejected", method)
		}
	}
	for _, method := range []string{"", "plugin.invoke", "Plugin.call", "host.shell.exec", "rpc.discover", "../x", "a b", "/x", strings.Repeat("a", 129)} {
		if validPluginUIMethod(method) {
			t.Errorf("%q accepted", method)
		}
	}
}

func TestManifestTransportValidation(t *testing.T) {
	for _, transport := range []string{"", "jsonl", "framed"} {
		manifest := Manifest{Runtime: ProcessRuntimeManifest{Type: "process", Transport: transport}}
		if manifest.Runtime.transport() == "" {
			t.Fatalf("transport %q resolves to empty", transport)
		}
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "plugin"), []byte("x"), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := &Manifest{Runtime: ProcessRuntimeManifest{Type: "process", Entry: "plugin", Transport: "websocket"}}
	if err := validateRuntime(newPluginPathResolver(root), manifest); err == nil || !strings.Contains(err.Error(), "runtime.transport") {
		t.Fatalf("unknown transport: %v", err)
	}
	manifest.Runtime.Transport = "framed"
	if err := validateRuntime(newPluginPathResolver(root), manifest); err != nil {
		t.Fatalf("framed transport rejected: %v", err)
	}
}
