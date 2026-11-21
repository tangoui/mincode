package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mincode/app/backend/core"
)

// Opt-in only: never depends on user-installed tools in the normal test suite.
// Run MINCODE_LSP_MACHINE_SMOKE=1 go test ./backend/lsp -run TestMachineLSP -v.
func TestMachineLSPInventory(t *testing.T) {
	if os.Getenv("MINCODE_LSP_MACHINE_SMOKE") != "1" {
		t.Skip("machine inventory is opt-in")
	}
	for _, d := range serverDefinitions {
		status := executableStatus(d)
		if status.Installed {
			t.Logf("FOUND %s: %s compatibility=%s", d.ID, status.Executable, status.Error)
		} else {
			t.Logf("MISSING %s", d.ID)
		}
	}
}

// Each empty workspace is independent; no package installation is requested.
// The hard deadline also kills a child blocked in stdin writes or startup.
func TestMachineLSPStdio(t *testing.T) {
	if os.Getenv("MINCODE_LSP_MACHINE_SMOKE") != "1" {
		t.Skip("all-server native stdio smoke is opt-in")
	}
	for name, value := range map[string]string{
		"GOPROXY": "off", "GOTOOLCHAIN": "local", "GOSUMDB": "off",
		"COURSIER_MODE": "offline", "COURSIER_OFFLINE": "true",
		"JULIA_PKG_OFFLINE": "true", "DOTNET_SKIP_FIRST_TIME_EXPERIENCE": "1",
		"BUNDLE_FROZEN": "true", "npm_config_offline": "true",
		"HTTP_PROXY": "http://127.0.0.1:1", "HTTPS_PROXY": "http://127.0.0.1:1",
		"http_proxy": "http://127.0.0.1:1", "https_proxy": "http://127.0.0.1:1",
	} {
		t.Setenv(name, value)
	}
	passed, attempted := 0, 0
	for _, definition := range serverDefinitions {
		t.Run(definition.ID, func(t *testing.T) {
			attempted++
			smokeHandshake(t, definition)
			if !t.Failed() {
				passed++
			}
		})
	}
	t.Logf("HANDSHAKE SUMMARY: %d/%d attempted passed; registry=%d; failures above include unavailable tools and blocked dependencies, not only protocol failures", passed, attempted, len(serverDefinitions))
}

func smokeHandshake(t *testing.T, definition serverDefinition) {
	t.Helper()
	started := time.Now()
	root := t.TempDir()
	key := "machine-smoke:" + definition.ID
	s := &session{key: key, root: root, definition: definition, pending: map[uint64]chan rpcReply{}, generation: generation.Add(1)}
	core.SetEmitter(func(_ string, payload any) {
		event, ok := payload.(serverEvent)
		if !ok || event.Root != root || event.ServerID != definition.ID || event.RequestID == nil {
			return
		}
		result := json.RawMessage("null")
		if event.Method == "workspace/configuration" {
			var params struct {
				Items []json.RawMessage `json:"items"`
			}
			_ = json.Unmarshal(event.Params, &params)
			result = smokeConfigurationReply(params.Items)
		}
		_ = s.reply(*event.RequestID, result)
	})
	defer core.SetEmitter(nil)
	// Cleanup waits for the complete start operation as well: no timeout
	// goroutine can launch a process after this subtest has returned.
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	var startup sync.WaitGroup
	startup.Add(1)
	startResult := make(chan error, 1)
	go func() { defer startup.Done(); startResult <- s.start() }()
	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			startup.Wait()
			s.close(errors.New("machine smoke deadline/cleanup"))
			// Always kill the group, including children surviving a normal exit.
			if s.cmd != nil && s.cmd.Process != nil {
				_ = core.KillProcessTree(s.cmd)
			}
			s.events.flush()
		})
	}
	defer cleanup()
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		select {
		case <-ctx.Done():
			cleanup()
		case <-watchDone:
		}
	}()
	var err error
	select {
	case err = <-startResult:
	case <-ctx.Done():
		err = ctx.Err()
	}
	stage := "start"
	if err == nil {
		stage = "initialize"
		params, _ := json.Marshal(map[string]any{
			"processId": nil, "clientInfo": map[string]string{"name": "mincode"}, "rootUri": fileURI(root),
			"workspaceFolders": []any{map[string]string{"uri": fileURI(root), "name": "empty-offline-smoke"}},
			"capabilities":     smokeClientCapabilities(),
		})
		var result json.RawMessage
		result, err = s.request(ctx, "initialize", params)
		if err == nil {
			var envelope struct {
				Capabilities json.RawMessage `json:"capabilities"`
			}
			if json.Unmarshal(result, &envelope) != nil || len(envelope.Capabilities) == 0 || string(envelope.Capabilities) == "null" {
				err = fmt.Errorf("initialize result has no capabilities object")
			}
		}
	}
	if err == nil {
		stage = "initialized"
		err = s.notify("initialized", json.RawMessage(`{}`))
	}
	if err == nil {
		stage = "shutdown"
		_, err = s.request(ctx, "shutdown", json.RawMessage("null"))
	}
	if err == nil {
		stage = "exit"
		err = s.notify("exit", nil)
	}
	cleanup()
	if err != nil {
		t.Errorf("HANDSHAKE FAIL %s stage=%s duration=%s reason=%v closedReason=%s stderr=%s trace=%s", definition.ID, stage, time.Since(started).Round(time.Millisecond), err, s.failureText(), smokeTail(s.stderr), smokeTail(&s.trace))
	} else {
		t.Logf("HANDSHAKE PASS %s initialize -> initialized -> shutdown -> exit duration=%s stderr=%s", definition.ID, time.Since(started).Round(time.Millisecond), smokeTail(s.stderr))
	}
}

// Mirrors project-language-server-runtime.ts startSession and configuration
// replies. Update together when the real client contract changes.
func smokeClientCapabilities() json.RawMessage {
	return json.RawMessage(`{
		"general":{"positionEncodings":["utf-16"]},
		"textDocument":{
			"synchronization":{"willSave":false,"willSaveWaitUntil":false,"didSave":true},
			"completion":{"completionItem":{"snippetSupport":true,"documentationFormat":["markdown","plaintext"]}},
			"hover":{"contentFormat":["markdown","plaintext"]},
			"signatureHelp":{"signatureInformation":{"documentationFormat":["markdown","plaintext"],"parameterInformation":{"labelOffsetSupport":true}}},
			"definition":{},"typeDefinition":{},"implementation":{},"references":{},"documentHighlight":{},
			"documentSymbol":{"hierarchicalDocumentSymbolSupport":true},
			"rename":{"prepareSupport":false},"formatting":{},"inlayHint":{},
			"codeAction":{"codeActionLiteralSupport":{"codeActionKind":{"valueSet":["","quickfix","refactor","source"]}}},
			"publishDiagnostics":{"relatedInformation":false},
			"diagnostic":{"dynamicRegistration":false,"relatedDocumentSupport":false}
		},
		"workspace":{"applyEdit":true,"workspaceFolders":true,"configuration":true,"didChangeWatchedFiles":{"dynamicRegistration":false}}
	}`)
}

func smokeConfigurationReply(items []json.RawMessage) json.RawMessage {
	result, _ := json.Marshal(make([]any, len(items)))
	return result
}

func smokeTail(buffer *limitedBuffer) string {
	if buffer == nil {
		return ""
	}
	text := buffer.String()
	if len(text) > 2400 {
		text = text[:900] + " [truncated] " + text[len(text)-1500:]
	}
	return strings.ReplaceAll(text, "\n", " | ")
}

func TestMachineLSPFeatures(t *testing.T) {
	if os.Getenv("MINCODE_LSP_MACHINE_SMOKE") != "1" {
		t.Skip("native stdio smoke is opt-in")
	}
	for _, id := range []string{"gopls", "clangd", "ts-ls", "json-ls", "vue-ls"} {
		t.Run(id, func(t *testing.T) {
			d, _ := definitionByID(id)
			if _, err := cachedLookPath(d.Command); err != nil {
				t.Skip("not installed")
			}
			root := t.TempDir()
			filename, language, text, line, character := "main.go", "go", "package main\n\nfunc main() { println(1) }\n", 2, 15
			switch id {
			case "clangd":
				filename, language, text, line, character = "main.cpp", "cpp", "int main() { return 0; }\n", 0, 5
			case "ts-ls":
				filename, language, text, line, character = "main.ts", "typescript", "const greeting = 'hello';\ngreeting;\n", 1, 3
			case "json-ls":
				filename, language, text, line, character = "main.json", "json", "{\"name\":\"smoke\"}\n", 0, 2
			case "vue-ls":
				filename, language, text, line, character = "App.vue", "vue", "<script setup lang=\"ts\">\nconst greeting: string = 'hello';\ngreeting;\n</script>\n<template>{{ greeting }}</template>\n", 2, 3
			}
			if id == "gopls" {
				if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module smoke\n\ngo 1.22\n"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(root, filename)
			if err := os.WriteFile(path, []byte(text), 0644); err != nil {
				t.Fatal(err)
			}
			s := &session{root: root, definition: d, pending: map[uint64]chan rpcReply{}}
			core.SetEmitter(func(_ string, payload any) {
				event, ok := payload.(serverEvent)
				if !ok || event.RequestID == nil {
					return
				}
				result := json.RawMessage("null")
				if event.Method == "workspace/configuration" {
					var params struct {
						Items []json.RawMessage `json:"items"`
					}
					_ = json.Unmarshal(event.Params, &params)
					result = smokeConfigurationReply(params.Items)
				}
				_ = s.reply(*event.RequestID, result)
			})
			t.Cleanup(func() { s.close(errors.New("smoke cleanup")); core.SetEmitter(nil) })
			if err := s.start(); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			initialize, _ := json.Marshal(map[string]any{"processId": nil, "clientInfo": map[string]string{"name": "mincode"}, "rootUri": fileURI(root), "workspaceFolders": []any{map[string]string{"uri": fileURI(root), "name": "smoke"}}, "capabilities": smokeClientCapabilities()})
			result, err := s.request(ctx, "initialize", initialize)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("initialize: %s", result)
			if err = s.notify("initialized", json.RawMessage(`{}`)); err != nil {
				t.Fatal(err)
			}
			open, _ := json.Marshal(map[string]any{"textDocument": map[string]any{"uri": fileURI(path), "languageId": language, "version": 1, "text": text}})
			if err = s.notify("textDocument/didOpen", open); err != nil {
				t.Fatal(err)
			}
			method := "textDocument/hover"
			params := map[string]any{"textDocument": map[string]string{"uri": fileURI(path)}, "position": map[string]int{"line": line, "character": character}}
			if id == "json-ls" {
				method = "textDocument/documentSymbol"
				delete(params, "position")
			}
			raw, _ := json.Marshal(params)
			result, err = s.request(ctx, method, raw)
			if err != nil {
				t.Fatalf("%s: %v logs=%s", method, err, s.logs())
			}
			if string(result) == "null" || len(result) == 0 {
				t.Fatalf("%s returned no feature result", method)
			}
			t.Logf("%s: %s", method, result)
			if id == "json-ls" {
				change, _ := json.Marshal(map[string]any{
					"textDocument":   map[string]any{"uri": fileURI(path), "version": 2},
					"contentChanges": []any{map[string]string{"text": "{\"name\": }"}},
				})
				if err = s.notify("textDocument/didChange", change); err != nil {
					t.Fatal(err)
				}
				params, _ := json.Marshal(map[string]any{"textDocument": map[string]string{"uri": fileURI(path)}})
				report, err := s.request(ctx, "textDocument/diagnostic", params)
				if err != nil {
					t.Fatal(err)
				}
				var diagnostics struct {
					Kind  string            `json:"kind"`
					Items []json.RawMessage `json:"items"`
				}
				if json.Unmarshal(report, &diagnostics) != nil || diagnostics.Kind != "full" || len(diagnostics.Items) == 0 {
					t.Fatalf("invalid JSON produced no pull diagnostics: %s", report)
				}
				t.Logf("textDocument/diagnostic: %s", report)
			}
			if _, err = s.request(ctx, "shutdown", json.RawMessage("null")); err != nil {
				t.Fatal(err)
			}
			if err = s.notify("exit", nil); err != nil {
				t.Fatal(err)
			}
			t.Logf("quiet-stderr=%v trace=%s", s.stderr.String() == "", strings.ReplaceAll(s.trace.String(), "\n", " | "))
		})
	}
}
