package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mincode/app/backend/core"
)

// Opt-in: exercise the installed Vue + TypeScript plugin, not fake executables.
func TestMachineVueHybridFeatures(t *testing.T) {
	if os.Getenv("MINCODE_LSP_MACHINE_SMOKE") != "1" {
		t.Skip("native Vue hybrid feature smoke is opt-in")
	}
	root := canonicalTempDir(t)
	text := "<script setup lang=\"ts\">\nconst greeting: string = 'hello';\ngreeting;\n</script>\n<template>{{ greeting }}</template>\n"
	path := filepath.Join(root, "App.vue")
	for name, content := range map[string]string{
		"App.vue":       text,
		"tsconfig.json": `{"compilerOptions":{"strict":true,"target":"ESNext"},"include":["*.vue"]}`,
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	vueDefinition, _ := definitionByID("vue-ls")
	tsDefinition, _ := definitionByID(vueCompanionID)
	vue := &session{root: root, definition: vueDefinition, pending: map[uint64]chan rpcReply{}, generation: generation.Add(1)}
	ts := &session{root: root, definition: tsDefinition, pending: map[uint64]chan rpcReply{}, generation: generation.Add(1)}
	var bridged atomic.Int32
	core.SetEmitter(func(_ string, payload any) {
		event, ok := payload.(serverEvent)
		if !ok || event.Root != root {
			return
		}
		s := vue
		if event.ServerID == vueCompanionID {
			s = ts
		}
		if event.RequestID != nil {
			result := json.RawMessage("null")
			if event.Method == "workspace/configuration" {
				var params struct {
					Items []json.RawMessage `json:"items"`
				}
				_ = json.Unmarshal(event.Params, &params)
				result = smokeConfigurationReply(params.Items)
			}
			_ = s.reply(*event.RequestID, result)
		} else if event.ServerID == "vue-ls" && event.Method == "tsserver/request" {
			go func() {
				var params []json.RawMessage
				if json.Unmarshal(event.Params, &params) != nil || len(params) == 0 {
					return
				}
				if len(params) == 1 {
					_ = json.Unmarshal(params[0], &params)
				}
				if len(params) != 3 {
					return
				}
				var command string
				_ = json.Unmarshal(params[1], &command)
				request, _ := json.Marshal(map[string]any{
					"command":   "typescript.tsserverRequest",
					"arguments": []any{command, params[2], map[string]bool{"isAsync": true, "lowPriority": true}},
				})
				result, err := ts.request(ctx, "workspace/executeCommand", request)
				var envelope struct {
					Body json.RawMessage `json:"body"`
				}
				body := json.RawMessage("null")
				if err == nil && json.Unmarshal(result, &envelope) == nil && len(envelope.Body) > 0 {
					body = envelope.Body
					bridged.Add(1)
				}
				response, _ := json.Marshal([]any{[]any{params[0], body}})
				_ = vue.notify("tsserver/response", response)
			}()
		}
	})
	defer core.SetEmitter(nil)
	defer vue.close(errors.New("Vue hybrid smoke cleanup"))
	defer ts.close(errors.New("Vue hybrid smoke cleanup"))
	initialize, _ := json.Marshal(map[string]any{
		"processId": nil, "clientInfo": map[string]string{"name": "mincode"}, "rootUri": fileURI(root),
		"workspaceFolders": []any{map[string]string{"uri": fileURI(root), "name": "vue-hybrid-smoke"}},
		"capabilities":     smokeClientCapabilities(),
	})
	for _, s := range []*session{ts, vue} {
		if err := s.start(); err != nil {
			t.Fatal(err)
		}
		if _, err := s.request(ctx, "initialize", initialize); err != nil {
			t.Fatalf("%s initialize: %v", s.definition.ID, err)
		}
		if err := s.notify("initialized", json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
		open, _ := json.Marshal(map[string]any{"textDocument": map[string]any{
			"uri": fileURI(path), "languageId": "vue", "version": 1, "text": text,
		}})
		if err := s.notify("textDocument/didOpen", open); err != nil {
			t.Fatal(err)
		}
	}
	scriptParams, _ := json.Marshal(map[string]any{"textDocument": map[string]string{"uri": fileURI(path)}, "position": map[string]int{"line": 2, "character": 3}})
	scriptHover, scriptErr := ts.request(ctx, "textDocument/hover", scriptParams)
	if scriptErr != nil || !strings.Contains(string(scriptHover), "greeting: string") {
		t.Fatalf("companion script hover returned no real type: %s (%v)", scriptHover, scriptErr)
	}
	// Vue may delegate TypeScript hover to its companion (the frontend merges
	// both results); its request must still complete through the actual bridge.
	params, _ := json.Marshal(map[string]any{"textDocument": map[string]string{"uri": fileURI(path)}, "position": map[string]int{"line": 4, "character": 17}})
	result, err := vue.request(ctx, "textDocument/hover", params)
	if err != nil || bridged.Load() == 0 {
		t.Fatalf("template hover failed: %s err=%v bridged=%d vue=%s ts=%s", result, err, bridged.Load(), vue.logs(), ts.logs())
	}
	result, err = ts.request(ctx, "textDocument/hover", params)
	if err != nil || !strings.Contains(string(result), "greeting") {
		t.Fatalf("companion template hover returned no type: %s (%v)", result, err)
	}
	t.Logf("Vue hybrid script/template hovers returned real types; %d Vue requests bridged through the plugin", bridged.Load())
}
