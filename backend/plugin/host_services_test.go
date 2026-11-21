package plugin

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"mincode/app/plugins/api"
)

const serviceFilesHelperID = "service.files"

// serviceEnvelopeHelperPrefix names file providers that echo their envelope.
const serviceEnvelopeHelperPrefix = "service.envelope"

// TestServiceFilesHelperProcess is a provider that uses the host.services
// file methods over its own JSON-RPC channel. File input: it reads every
// file in 512 KiB chunks and returns the SHA-256 of the bytes as text. Text
// input "create:<content>": it creates one output file with that content and
// returns its token. A provider whose ID starts with serviceEnvelopeHelperPrefix
// returns, for file input, {"envelope": <the payload>, "sha256": <hex>} as text.
func TestServiceFilesHelperProcess(t *testing.T) {
	pluginID := os.Getenv("MINCODE_PLUGIN_ID")
	if pluginID != serviceFilesHelperID && !strings.HasPrefix(pluginID, serviceEnvelopeHelperPrefix) {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	encoder := json.NewEncoder(os.Stdout)
	next := 0
	call := func(method string, params any) (json.RawMessage, *rpcError) {
		next++
		id := json.RawMessage(fmt.Sprintf(`"h-%d"`, next))
		raw, _ := json.Marshal(params)
		_ = encoder.Encode(rpcEnvelope{JSONRPC: "2.0", ID: id, Method: method, Params: raw})
		for scanner.Scan() {
			var reply rpcEnvelope
			if json.Unmarshal(scanner.Bytes(), &reply) == nil && reply.Method == "" && string(reply.ID) == string(id) {
				return reply.Result, reply.Error
			}
		}
		os.Exit(0)
		return nil, nil
	}
	handle := func(payload string) any {
		var envelope struct {
			CallID string          `json:"callId"`
			Input  json.RawMessage `json:"input"`
		}
		_ = json.Unmarshal([]byte(payload), &envelope)
		var files []exchangeFile
		if json.Unmarshal(envelope.Input, &files) == nil && len(files) > 0 {
			hash := sha256.New()
			for _, file := range files {
				for offset := 0; ; {
					result, rpcErr := call("host.services.file.read", map[string]any{"token": file.Token, "offset": offset, "length": 524288})
					if rpcErr != nil {
						return map[string]any{"text": "error: " + rpcErr.Message}
					}
					var chunk struct {
						Data string `json:"data"`
					}
					_ = json.Unmarshal(result, &chunk)
					data, _ := base64.StdEncoding.DecodeString(chunk.Data)
					if len(data) == 0 {
						break
					}
					hash.Write(data)
					offset += len(data)
				}
			}
			if strings.HasPrefix(pluginID, serviceEnvelopeHelperPrefix) {
				echoed, _ := json.Marshal(map[string]any{"envelope": json.RawMessage(payload), "sha256": hex.EncodeToString(hash.Sum(nil))})
				return map[string]any{"text": string(echoed)}
			}
			return map[string]any{"text": hex.EncodeToString(hash.Sum(nil))}
		}
		var text string
		_ = json.Unmarshal(envelope.Input, &text)
		content := strings.TrimPrefix(text, "create:")
		begun, rpcErr := call("host.services.file.create.begin", map[string]any{"callId": envelope.CallID, "name": "out.txt"})
		if rpcErr != nil {
			return map[string]any{"text": "error: " + rpcErr.Message}
		}
		var session struct {
			Session string `json:"session"`
		}
		_ = json.Unmarshal(begun, &session)
		if _, rpcErr := call("host.services.file.create.append", map[string]any{"session": session.Session, "offset": 0, "data": base64.StdEncoding.EncodeToString([]byte(content))}); rpcErr != nil {
			return map[string]any{"text": "error: " + rpcErr.Message}
		}
		committed, rpcErr := call("host.services.file.create.commit", map[string]any{"session": session.Session})
		if rpcErr != nil {
			return map[string]any{"text": "error: " + rpcErr.Message}
		}
		var file exchangeFile
		_ = json.Unmarshal(committed, &file)
		return map[string]any{"files": []string{file.Token}}
	}
	for scanner.Scan() {
		var request rpcEnvelope
		if json.Unmarshal(scanner.Bytes(), &request) != nil || request.Method == "" {
			continue
		}
		response := rpcEnvelope{JSONRPC: "2.0", ID: request.ID}
		switch request.Method {
		case "plugin.initialize":
			response.Result, _ = json.Marshal(map[string]any{"runtimeProtocolVersion": api.RuntimeProtocolVersion, "features": map[string]int{}})
		case "plugin.invoke":
			var params struct {
				Payload string `json:"payload"`
			}
			_ = json.Unmarshal(request.Params, &params)
			result, _ := json.Marshal(handle(params.Payload))
			response.Result, _ = json.Marshal(map[string]json.RawMessage{"result": result})
		case "plugin.ping", "plugin.shutdown":
			response.Result = json.RawMessage(`{}`)
		default:
			response.Error = &rpcError{Code: -32601, Message: "unknown"}
		}
		_ = encoder.Encode(response)
		if request.Method == "plugin.shutdown" {
			os.Exit(0)
		}
	}
	os.Exit(0)
}

// useServicesManager routes the host.services handlers to manager.
func useServicesManager(t *testing.T, manager *pluginManager) {
	t.Helper()
	previous := servicesManager
	servicesManager = func() *pluginManager { return manager }
	t.Cleanup(func() { servicesManager = previous })
}

// servicesCaller is a caller plugin runtime with the given capabilities.
func servicesCaller(id string, capabilities ...api.Capability) *processRuntime {
	granted := map[api.Capability]struct{}{}
	for _, capability := range capabilities {
		granted[capability] = struct{}{}
	}
	return &processRuntime{pluginID: id, manifest: Manifest{ID: id, Name: "Caller " + id}, capabilities: granted}
}

// servicesCall runs a handler and decodes its result as JSON.
func servicesCall(t *testing.T, handler hostMethodHandler, runtimeHost *processRuntime, params string) (map[string]any, *rpcError) {
	t.Helper()
	if runtimeHost.ownerRecord == nil {
		bindServiceTestCaller(t, servicesManager(), runtimeHost)
	}
	result, rpcErr := handler(runtimeHost, json.RawMessage(params))
	if rpcErr != nil {
		return nil, rpcErr
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("result %s: %v", raw, err)
	}
	return decoded, nil
}

func rpcErrorCode(rpcErr *rpcError) string {
	if rpcErr == nil {
		return ""
	}
	if data, ok := rpcErr.Data.(map[string]string); ok {
		return data["code"]
	}
	return ""
}

// setServiceRisk changes the provider's backing action risk and, when entry
// is set, the services entry's recorded risk too.
func setServiceRisk(fixture *serviceCallFixture, risk string, entry bool) {
	record := fixture.record
	record.mu.Lock()
	defer record.mu.Unlock()
	record.manifest.Contributes.Actions[0].Risk = risk
	if entry {
		current := servicesLocked(record)[0]
		current.Risk = risk
		record.manifest.contributions[servicePointKey] = &resolvedContribution{Entries: []any{current}}
	}
}

func checksumServiceEntry() ServiceContribution {
	return ServiceContribution{
		ID: "checksum", Service: "demo.file.checksum", Action: "run", Title: "Checksum",
		Input: ServiceShape{Kind: serviceInputFile, Accepts: []string{".bin"}, MaxBytes: serviceFileMaxBytes}, Output: ServiceShape{Kind: serviceInputText},
		Surfaces: []string{}, TimeoutMs: 30000, Risk: "read",
	}
}

func createServiceEntry() ServiceContribution {
	return ServiceContribution{
		ID: "create", Service: "demo.file.create", Action: "run", Title: "Create",
		Input: ServiceShape{Kind: serviceInputText, MaxBytes: 8192}, Output: ServiceShape{Kind: serviceInputFile},
		Surfaces: []string{}, TimeoutMs: 30000, Risk: "read",
	}
}

func TestHostServicesListExcludesSelf(t *testing.T) {
	useTemporaryPluginData(t)
	alpha := serviceRegistryRecord(t, "alpha-tools", "Alpha Tools")
	zeta := serviceRegistryRecord(t, "zeta-tools", "Zeta Tools")
	useServicesManager(t, serviceRegistryManager(alpha, zeta))
	caller := servicesCaller("alpha-tools", api.CapabilityPlugins)
	for _, params := range []string{`{}`, `{"service":"demo.text.reverse"}`} {
		result, rpcErr := servicesCall(t, hostServicesList, caller, params)
		if rpcErr != nil {
			t.Fatal(rpcErr)
		}
		services := result["services"].([]any)
		if len(services) != 1 {
			t.Fatalf("%s: services = %v", params, services)
		}
		group := services[0].(map[string]any)
		providers := group["providers"].([]any)
		if group["service"] != "demo.text.reverse" || len(providers) != 1 {
			t.Fatalf("%s: group = %v", params, group)
		}
		provider := providers[0].(map[string]any)
		if provider["pluginId"] != "zeta-tools" || provider["pluginName"] != "Zeta Tools" || provider["entryId"] != "reverse" ||
			provider["title"] != "Reverse text" || provider["risk"] != "read" || provider["isDefault"] != false ||
			provider["input"].(map[string]any)["kind"] != "text" || provider["output"].(map[string]any)["kind"] != "text" {
			t.Fatalf("provider = %v", provider)
		}
		if localized, _ := provider["titleLocalized"].(map[string]any); localized["zh-CN"] != "反转文字" {
			t.Fatalf("titleLocalized = %v", provider["titleLocalized"])
		}
	}
	if result, rpcErr := servicesCall(t, hostServicesList, caller, `{"service":"demo.other"}`); rpcErr != nil || len(result["services"].([]any)) != 0 {
		t.Fatalf("other service: %v %v", result, rpcErr)
	}
	if _, rpcErr := servicesCall(t, hostServicesList, caller, `{"service":"Bad Name"}`); rpcErr == nil || rpcErr.Code != -32602 {
		t.Fatalf("bad name: %v", rpcErr)
	}
}

func TestHostServicesInvokeByName(t *testing.T) {
	events := captureHostUI(t)
	fixture := serviceCallProvider(t, "service.short", serviceTestEntry(), 16384)
	useServicesManager(t, fixture.manager)
	caller := servicesCaller("caller.a", api.CapabilityPlugins)
	go answerNext(t, events, func(event promptEvent) promptReply {
		if event.Kind != "pluginCall" || event.Call == nil || event.Call.Service != "demo.text.reverse" || event.Call.ServiceTitle != "Reverse" ||
			event.Call.TargetPluginID != "service.short" || event.Call.Action != "run" || event.Call.Risk != "write" || event.Call.Payload != "plain" || len(event.Items) != 1 {
			t.Errorf("approval %+v %+v", event, event.Call)
		}
		return promptReply{IDs: []string{"once"}}
	})
	result, rpcErr := servicesCall(t, hostServicesInvoke, caller, `{"service":"demo.text.reverse","input":"plain"}`)
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	provider := result["provider"].(map[string]any)
	if provider["pluginId"] != "service.short" || provider["entryId"] != "reverse" || result["output"] != "done" {
		t.Fatalf("result = %v", result)
	}
	// The provider may not be the caller itself, and params are validated.
	for name, params := range map[string]string{
		"self":         `{"service":"demo.text.reverse","provider":{"pluginId":"caller.a"},"input":"x"}`,
		"bad service":  `{"service":"demo","input":"x"}`,
		"extra field":  `{"service":"demo.text.reverse","input":"x","secret":1}`,
		"bad timeout":  `{"service":"demo.text.reverse","input":"x","timeoutMs":1}`,
		"bad input":    `{"service":"demo.text.reverse","input":{"a":1}}`,
		"unknown kind": `{"service":"demo.text.reverse","input":[{"source":"ftp","path":"x"}]}`,
	} {
		if _, rpcErr := servicesCall(t, hostServicesInvoke, servicesCaller("caller.a", api.CapabilityPlugins), params); rpcErr == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestHostServicesGrantRules(t *testing.T) {
	events := captureHostUI(t)
	entry := serviceTestEntry()
	entry.Risk = "read"
	fixture := serviceCallProvider(t, "service.short", entry, 16384)
	useServicesManager(t, fixture.manager)
	caller := servicesCaller("caller.a", api.CapabilityPlugins)
	invoke := `{"service":"demo.text.reverse","input":"x","timeoutMs":5000}`

	// Read: "always" is offered and stored in the same calls/<caller> grants
	// as host.plugins.invoke.
	go answerNext(t, events, func(event promptEvent) promptReply {
		if len(event.Items) != 2 || event.Items[1].ID != "always" || event.Call.Risk != "read" {
			t.Errorf("read approval offers %+v", event.Items)
		}
		return promptReply{IDs: []string{"always"}}
	})
	if _, rpcErr := servicesCall(t, hostServicesInvoke, caller, invoke); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	if grants := readPluginCallGrants("caller.a").Targets; len(grants) != 1 || grants[0] != "service.short" {
		t.Fatalf("grants = %v", grants)
	}
	// Granted: no prompt.
	if _, rpcErr := servicesCall(t, hostServicesInvoke, caller, invoke); rpcErr != nil {
		t.Fatal(rpcErr)
	}

	// Write: asked every time despite the grant, never with "always".
	setServiceRisk(fixture, "write", true)
	for range 2 {
		go answerNext(t, events, func(event promptEvent) promptReply {
			if len(event.Items) != 1 || event.Items[0].ID != "once" || event.Call.Risk != "write" {
				t.Errorf("write approval offers %+v", event.Items)
			}
			return promptReply{IDs: []string{"once"}}
		})
		if _, rpcErr := servicesCall(t, hostServicesInvoke, caller, invoke); rpcErr != nil {
			t.Fatal(rpcErr)
		}
	}
	// Declining stops the call.
	go answerNext(t, events, func(promptEvent) promptReply { return promptReply{Cancelled: true} })
	if _, rpcErr := servicesCall(t, hostServicesInvoke, caller, invoke); rpcErrorCode(rpcErr) != "declined" {
		t.Fatalf("declined: %v", rpcErr)
	}
}

func TestServiceInvokeRevalidatesAfterApproval(t *testing.T) {
	events := captureHostUI(t)
	fixture := serviceCallProvider(t, "service.short", serviceTestEntry(), 16384)
	useServicesManager(t, fixture.manager)
	go answerNext(t, events, func(promptEvent) promptReply {
		// The provider is updated while the user reads the prompt.
		setServiceRisk(fixture, "dangerous", false)
		return promptReply{IDs: []string{"once"}}
	})
	_, rpcErr := servicesCall(t, hostServicesInvoke, servicesCaller("caller.a", api.CapabilityPlugins), `{"service":"demo.text.reverse","input":"x"}`)
	if rpcErrorCode(rpcErr) != contributionErrForbidden || !strings.Contains(rpcErr.Message, "call it again") {
		t.Fatalf("changed provider: %v", rpcErr)
	}
}

func TestHostServicesWorkspaceSourceNeedsCapability(t *testing.T) {
	captureHostUI(t)
	useTemporaryPluginData(t)
	useServicesManager(t, serviceRegistryManager())
	root := useWorkspaceRoot(t)
	writeWorkspaceFile(t, root, "data/a.bin", "abc")
	writeWorkspaceFile(t, root, ".env", "TOKEN=x\n")
	source := func(path string) string {
		return `{"service":"demo.file.checksum","input":[{"source":"workspace","path":"` + path + `"}]}`
	}
	if _, rpcErr := servicesCall(t, hostServicesInvoke, servicesCaller("caller.a", api.CapabilityPlugins), source("data/a.bin")); rpcErr == nil || rpcErr.Code != -32602 {
		t.Fatalf("without workspace.read: %v", rpcErr)
	}
	reader := servicesCaller("caller.a", api.CapabilityPlugins, api.CapabilityWorkspaceRead)
	for _, path := range []string{"../a.bin", "data/../../a.bin", "/etc/hosts", "data\\\\a.bin", ""} {
		if _, rpcErr := servicesCall(t, hostServicesInvoke, reader, source(path)); rpcErr == nil || rpcErr.Code != -32602 {
			t.Fatalf("%q: %v", path, rpcErr)
		}
	}
	if _, rpcErr := servicesCall(t, hostServicesInvoke, reader, source(".env")); rpcErr == nil {
		t.Fatal("sensitive file accepted")
	}
	// A valid source gets as far as provider selection.
	if _, rpcErr := servicesCall(t, hostServicesInvoke, reader, source("data/a.bin")); rpcErrorCode(rpcErr) != contributionErrNotFound {
		t.Fatalf("valid source: %v", rpcErr)
	}
}

// uploadServiceBytes uploads data in 512 KiB chunks as caller.
func uploadServiceBytes(t *testing.T, caller *processRuntime, name string, data []byte) (string, int) {
	t.Helper()
	begun, rpcErr := servicesCall(t, hostServicesUploadBegin, caller, `{}`)
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	session := begun["session"].(string)
	chunks := 0
	for offset := 0; offset < len(data); offset += serviceChunkMaxBytes {
		end := min(offset+serviceChunkMaxBytes, len(data))
		params, _ := json.Marshal(map[string]any{"session": session, "offset": offset, "data": base64.StdEncoding.EncodeToString(data[offset:end])})
		appended, rpcErr := servicesCall(t, hostServicesUploadAppend, caller, string(params))
		if rpcErr != nil {
			t.Fatal(rpcErr)
		}
		if appended["size"] != float64(end) {
			t.Fatalf("append size = %v", appended["size"])
		}
		chunks++
	}
	params, _ := json.Marshal(map[string]any{"session": session, "name": name})
	committed, rpcErr := servicesCall(t, hostServicesUploadCommit, caller, string(params))
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	if committed["name"] != name || committed["size"] != float64(len(data)) {
		t.Fatalf("committed = %v", committed)
	}
	return committed["token"].(string), chunks
}

func TestHostServicesUploadRoundTrip(t *testing.T) {
	events := captureHostUI(t)
	fixture := serviceProcessProvider(t, serviceFilesHelperID, "TestServiceFilesHelperProcess", checksumServiceEntry(), 16384)
	useServicesManager(t, fixture.manager)
	caller := servicesCaller("caller.a", api.CapabilityPlugins)
	data := make([]byte, 1258291) // 1.2 MiB
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	token, chunks := uploadServiceBytes(t, caller, "blob.bin", data)
	if chunks != 3 {
		t.Fatalf("chunks = %d", chunks)
	}
	invoke := `{"service":"demo.file.checksum","input":[{"source":"upload","token":"` + token + `"}]}`
	// The upload token is the uploader's alone.
	if _, rpcErr := servicesCall(t, hostServicesInvoke, servicesCaller("caller.b", api.CapabilityPlugins), invoke); rpcErrorCode(rpcErr) != contributionErrNotFound {
		t.Fatalf("foreign upload: %v", rpcErr)
	}
	go answerNext(t, events, func(event promptEvent) promptReply {
		if event.Call == nil || !strings.Contains(event.Call.Payload, "blob.bin") || !strings.Contains(event.Call.Payload, "1258291") {
			t.Errorf("approval %+v", event.Call)
		}
		return promptReply{IDs: []string{"once"}}
	})
	result, rpcErr := servicesCall(t, hostServicesInvoke, caller, invoke)
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	sum := sha256.Sum256(data)
	if result["output"] != hex.EncodeToString(sum[:]) {
		t.Fatalf("output = %v", result["output"])
	}
	// The upload was consumed by the call.
	if _, rpcErr := servicesCall(t, hostServicesInvoke, caller, invoke); rpcErrorCode(rpcErr) != contributionErrNotFound {
		t.Fatalf("reused upload: %v", rpcErr)
	}
	// Names outside accepts are refused before anyone is asked.
	other, _ := uploadServiceBytes(t, caller, "blob.txt", []byte("x"))
	if _, rpcErr := servicesCall(t, hostServicesInvoke, caller, `{"service":"demo.file.checksum","input":[{"source":"upload","token":"`+other+`"}]}`); rpcErr == nil {
		t.Fatal("file outside accepts accepted")
	}
}

func TestHostServicesOutputSave(t *testing.T) {
	events := captureHostUI(t)
	fixture := serviceProcessProvider(t, serviceFilesHelperID, "TestServiceFilesHelperProcess", createServiceEntry(), 16384)
	useServicesManager(t, fixture.manager)
	caller := servicesCaller("caller.a", api.CapabilityPlugins, api.CapabilityStorage)
	go answerNext(t, events, func(promptEvent) promptReply { return promptReply{IDs: []string{"once"}} })
	result, rpcErr := servicesCall(t, hostServicesInvoke, caller, `{"service":"demo.file.create","input":"create:hello services"}`)
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	files := result["output"].([]any)
	file := files[0].(map[string]any)
	token := file["token"].(string)
	if len(files) != 1 || file["name"] != "out.txt" || file["size"] != float64(len("hello services")) {
		t.Fatalf("output = %v", result["output"])
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), userDataDir()) {
		t.Fatalf("result leaks a path: %s", encoded)
	}
	// Someone else cannot save, read or release the caller's output.
	other := servicesCaller("caller.b", api.CapabilityPlugins, api.CapabilityStorage)
	if _, rpcErr := servicesCall(t, hostServicesFileSave, other, `{"token":"`+token+`","path":"stolen.txt"}`); rpcErrorCode(rpcErr) != contributionErrNotFound {
		t.Fatalf("foreign save: %v", rpcErr)
	}
	if _, rpcErr := servicesCall(t, hostServicesFileRead, other, `{"token":"`+token+`"}`); rpcErrorCode(rpcErr) != contributionErrNotFound {
		t.Fatalf("foreign read: %v", rpcErr)
	}
	if _, rpcErr := uiCall(hostServicesFileRelease, other, `{"token":"`+token+`"}`); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	// The caller saves into its own storage, through the storage path rules.
	if _, rpcErr := servicesCall(t, hostServicesFileSave, caller, `{"token":"`+token+`","path":"../escape.txt"}`); rpcErr == nil {
		t.Fatal("escaping save path accepted")
	}
	if _, rpcErr := servicesCall(t, hostServicesFileSave, servicesCaller("caller.a", api.CapabilityPlugins), `{"token":"`+token+`","path":"a.txt"}`); rpcErr == nil {
		t.Fatal("save without the storage capability accepted")
	}
	saved, rpcErr := servicesCall(t, hostServicesFileSave, caller, `{"token":"`+token+`","path":"out/hello.txt"}`)
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	if saved["path"] != "out/hello.txt" || saved["size"] != float64(len("hello services")) {
		t.Fatalf("saved = %v", saved)
	}
	stored, _, err := pluginStorageRead("caller.a", "out/hello.txt", 0, 1024)
	if err != nil || string(stored) != "hello services" {
		t.Fatalf("stored %q %v", stored, err)
	}
	read, rpcErr := servicesCall(t, hostServicesFileRead, caller, `{"token":"`+token+`","offset":6,"length":3}`)
	if rpcErr != nil || read["data"] != base64.StdEncoding.EncodeToString([]byte("ser")) {
		t.Fatalf("read %v %v", read, rpcErr)
	}
	// Released files are gone.
	if _, rpcErr := uiCall(hostServicesFileRelease, caller, `{"token":"`+token+`"}`); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	if _, rpcErr := servicesCall(t, hostServicesFileRead, caller, `{"token":"`+token+`"}`); rpcErrorCode(rpcErr) != contributionErrNotFound {
		t.Fatalf("released read: %v", rpcErr)
	}
}

func TestHostServicesCreateRequiresActiveCall(t *testing.T) {
	events := captureHostUI(t)
	fixture := serviceCallProvider(t, "service.tasks", serviceFilesOutputEntry(), 16384)
	useServicesManager(t, fixture.manager)
	caller := servicesCaller("caller.a", api.CapabilityPlugins)
	go answerNext(t, events, func(promptEvent) promptReply { return promptReply{IDs: []string{"once"}} })
	started, rpcErr := servicesCall(t, hostServicesStart, caller, `{"service":"demo.text.reverse","input":"wait"}`)
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	callID := started["callId"].(string)
	begin := `{"callId":"` + callID + `","name":"out.txt"}`
	provider := servicesCaller("service.tasks")
	for _, intruder := range []*processRuntime{caller, servicesCaller("intruder", api.CapabilityPlugins)} {
		if _, rpcErr := servicesCall(t, hostServicesCreateBegin, intruder, begin); rpcErrorCode(rpcErr) != contributionErrNotFound {
			t.Fatalf("%s created an output: %v", intruder.pluginID, rpcErr)
		}
	}
	if _, rpcErr := servicesCall(t, hostServicesCreateBegin, provider, begin); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	if _, rpcErr := servicesCall(t, hostServicesCancel, caller, `{"callId":"`+callID+`"}`); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	waitServiceCall(t, "caller.a", callID)
	if _, rpcErr := servicesCall(t, hostServicesCreateBegin, provider, begin); rpcErrorCode(rpcErr) != contributionErrNotFound {
		t.Fatalf("create after the call: %v", rpcErr)
	}
}

func TestHostServicesStartStatusCancel(t *testing.T) {
	events := captureHostUI(t)
	entry := serviceTestEntry()
	entry.TimeoutMs = 60000
	fixture := serviceCallProvider(t, "service.tasks", entry, 16384)
	useServicesManager(t, fixture.manager)
	caller := servicesCaller("caller.a", api.CapabilityPlugins)
	go answerNext(t, events, func(promptEvent) promptReply { return promptReply{IDs: []string{"once"}} })
	started, rpcErr := servicesCall(t, hostServicesStart, caller, `{"service":"demo.text.reverse","input":"wait"}`)
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	callID, _ := started["callId"].(string)
	if !strings.HasPrefix(callID, serviceCallIDPrefix) || started["provider"].(map[string]any)["pluginId"] != "service.tasks" {
		t.Fatalf("started = %v", started)
	}
	status := `{"callId":"` + callID + `"}`
	deadline := time.Now().Add(10 * time.Second)
	var snapshot map[string]any
	for time.Now().Before(deadline) {
		snapshot, rpcErr = servicesCall(t, hostServicesStatus, caller, status)
		if rpcErr != nil {
			t.Fatal(rpcErr)
		}
		if snapshot["progress"] != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	progress, _ := snapshot["progress"].(map[string]any)
	if snapshot["state"] != serviceCallRunning || progress["completed"] != float64(1) || progress["total"] != float64(2) || progress["message"] != "half" {
		t.Fatalf("status = %v", snapshot)
	}
	// Another plugin cannot see or cancel the call.
	other := servicesCaller("caller.b", api.CapabilityPlugins)
	for _, handler := range []hostMethodHandler{hostServicesStatus, hostServicesCancel} {
		if _, rpcErr := servicesCall(t, handler, other, status); rpcErrorCode(rpcErr) != contributionErrNotFound {
			t.Fatalf("foreign access: %v", rpcErr)
		}
	}
	if _, rpcErr := servicesCall(t, hostServicesStatus, caller, `{"callId":"nope"}`); rpcErr == nil || rpcErr.Code != -32602 {
		t.Fatalf("bad call id: %v", rpcErr)
	}
	if _, rpcErr := servicesCall(t, hostServicesCancel, caller, status); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	for time.Now().Before(deadline) {
		snapshot, rpcErr = servicesCall(t, hostServicesStatus, caller, status)
		if rpcErr != nil {
			t.Fatal(rpcErr)
		}
		if snapshot["state"] != serviceCallRunning {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if snapshot["state"] != serviceCallCancelled {
		t.Fatalf("after cancel = %v", snapshot)
	}
}

func TestHostServicesProviderChoicePrompt(t *testing.T) {
	events := captureHostUI(t)
	candidates := []serviceProvider{
		{PluginID: "alpha-tools", PluginName: "Alpha", Entry: ServiceContribution{ID: "a", Title: "Alpha reverse"}},
		{PluginID: "zeta-tools", PluginName: "Zeta", Entry: ServiceContribution{ID: "z", Title: "Zeta reverse", Description: "Fast"}},
	}
	var failure *rpcError
	choose := serviceProviderChooser(servicesCaller("caller.a", api.CapabilityPlugins), time.Now().Add(5*time.Second), &failure)
	go answerNext(t, events, func(event promptEvent) promptReply {
		if event.Kind != serviceProviderPromptKind || event.Service != "demo.text.reverse" || len(event.Items) != 2 ||
			event.Items[1].Label != "Zeta" || event.Items[1].Description != "Zeta reverse" || event.Items[1].Detail != "Fast" {
			t.Errorf("choice %+v", event)
		}
		return promptReply{IDs: []string{event.Items[1].ID, serviceProviderRememberID}}
	})
	chosen, remember, err := choose(context.Background(), "demo.text.reverse", candidates)
	if err != nil || chosen.PluginID != "zeta-tools" || !remember || failure != nil {
		t.Fatalf("chosen %+v %v %v %v", chosen, remember, err, failure)
	}
	go answerNext(t, events, func(promptEvent) promptReply { return promptReply{Cancelled: true} })
	if _, _, err := choose(context.Background(), "demo.text.reverse", candidates); err == nil || rpcErrorCode(failure) != "declined" {
		t.Fatalf("declined choice: %v %v", err, failure)
	}

	broker := &promptBroker{pending: map[string]*pendingPrompt{"p": {pluginID: "a", kind: serviceProviderPromptKind,
		items: map[string]bool{"provider-0": true, "provider-1": true}, reply: make(chan promptReply, 1)}}, byPlugin: map[string]string{}}
	for _, bad := range []promptReply{{IDs: []string{"provider-0", "provider-1"}}, {IDs: []string{serviceProviderRememberID}}, {IDs: []string{"provider-0", "provider-0"}},
		{IDs: []string{"provider-0", serviceProviderRememberID, serviceProviderRememberID}}, {IDs: []string{"always"}}, {}} {
		if broker.answer("p", bad) == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	if err := broker.answer("p", promptReply{IDs: []string{serviceProviderRememberID, "provider-1"}}); err != nil {
		t.Fatal(err)
	}
}

func TestHostServicesFileMethodsGate(t *testing.T) {
	plain := servicesCaller("plain")
	if serviceFileGate(plain) {
		t.Fatal("a plugin without plugins or services passed the gate")
	}
	if !serviceFileGate(servicesCaller("caller", api.CapabilityPlugins)) {
		t.Fatal("a caller was refused")
	}
	provider := servicesCaller("provider")
	provider.manifest.contributions = map[string]*resolvedContribution{servicePointKey: {Entries: []any{serviceTestEntry()}}}
	if !serviceFileGate(provider) {
		t.Fatal("a provider was refused")
	}
	described, _ := json.Marshal(plain.describeAPI())
	for _, method := range []string{"host.services.invoke", "host.services.file.read", "host.services.file.create.begin", "host.services.file.upload.commit"} {
		if !strings.Contains(string(described), `"name":"`+method+`"`) {
			t.Fatalf("describe lacks %s", method)
		}
	}
}
