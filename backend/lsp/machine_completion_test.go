package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mincode/app/backend/core"
)

// JSON, YAML and TOML servers complete from schemas only, so they are not
// listed: without a schema an empty answer is their normal behavior.
//
// completionCase opens file (with the cursor at ‸) in a project holding files
// and expects a completion whose label contains want ("" accepts any item).
type completionCase struct {
	id, file, language, text, want string
	files                          map[string]string
}

var machineCompletionCases = []completionCase{
	{id: "intelephense", file: "a.php", language: "php", text: "<?php\n$value = 1;\necho $val‸;\n", want: "$value"},
	{id: "gopls", file: "main.go", language: "go", files: map[string]string{"go.mod": "module smoke\n\ngo 1.22\n"}, text: "package main\n\nfunc main() {\n\tvalue := 1\n\t_ = val‸\n}\n", want: "value"},
	{id: "ts-ls", file: "a.ts", language: "typescript", text: "const value = 1;\nval‸\n", want: "value"},
	{id: "pyright", file: "a.py", language: "python", text: "value = 1\nval‸\n", want: "value"},
	{id: "rust-analyzer", file: "src/main.rs", language: "rust", files: map[string]string{"Cargo.toml": "[package]\nname = \"smoke\"\nversion = \"0.1.0\"\nedition = \"2021\"\n"}, text: "fn main() {\n    let value = 1;\n    let _ = val‸;\n}\n", want: "value"},
	{id: "html-ls", file: "a.html", language: "html", text: "<div>\n  <sp‸\n</div>\n", want: "span"},
	{id: "css-ls", file: "a.css", language: "css", text: "a {\n  colo‸\n}\n", want: "color"},
	{id: "bash-ls", file: "a.sh", language: "shell", text: "#!/bin/bash\nech‸\n", want: "echo"},
	{id: "clangd", file: "a.c", language: "c", text: "int main(void) {\n  int value = 1;\n  return val‸;\n}\n", want: "value"},
	{id: "lua-ls", file: "a.lua", language: "lua", text: "local value = 1\nprint(val‸)\n", want: "value"},
	{id: "csharp-ls", file: "Program.cs", language: "csharp", files: map[string]string{"smoke.csproj": "<Project Sdk=\"Microsoft.NET.Sdk\"><PropertyGroup><OutputType>Exe</OutputType><TargetFramework>net10.0</TargetFramework></PropertyGroup></Project>\n"}, text: "var value = 1;\nSystem.Console.WriteLine(val‸);\n", want: "value"},
	{id: "sqls", file: "a.sql", language: "sql", text: "sel‸", want: "SELECT"},
	{id: "ruby-lsp", file: "a.rb", language: "ruby", text: "value = 1\nputs val‸\n", want: "value"},
	{id: "sourcekit-lsp", file: "Sources/smoke/main.swift", language: "swift", files: map[string]string{"Package.swift": "// swift-tools-version:5.9\nimport PackageDescription\nlet package = Package(name: \"smoke\", targets: [.executableTarget(name: \"smoke\")])\n"}, text: "let value = 1\nprint(val‸)\n", want: "value"},
	{id: "perlnavigator", file: "a.pl", language: "perl", text: "my $value = 1;\nprint $val‸;\n", want: "value"},
	// marksman completes links and references, not prose.
	{id: "marksman", file: "a.md", language: "markdown", text: "# Smoke Title\n\n## Other Part\n\n[link](#‸)\n", want: "Other Part"},
	{id: "dockerfile-ls", file: "Dockerfile", language: "dockerfile", text: "FRO‸\n", want: "FROM"},
	{id: "graphql-lsp", file: "a.graphql", language: "graphql", files: map[string]string{".graphqlrc.yml": "schema: schema.graphql\ndocuments: '*.graphql'\n", "schema.graphql": "type Query { value: Int }\n"}, text: "query { val‸ }\n", want: "value"},
	// The editor sends "terraform" for .tf files (lspDocumentLanguageId); terraform-ls rejects "hcl".
	{id: "terraform-ls", file: "main.tf", language: "terraform", text: "reso‸\n", want: "resource"},
	{id: "svelte-ls", file: "App.svelte", language: "svelte", text: "<script>\n  let value = 1;\n</script>\n{val‸}\n", want: "value"},
	{id: "prisma-ls", file: "schema.prisma", language: "prisma", text: "mod‸\n", want: "model"},
	{id: "zls", file: "main.zig", language: "zig", text: "const value = 1;\npub fn main() void {\n    _ = val‸;\n}\n", want: "value"},
	{id: "astro-ls", file: "a.astro", language: "astro", text: "---\nconst value = 1;\n---\n<div>{val‸}</div>\n", want: "value"},
	// protobuf-language-server only completes in a file it parsed successfully at least once.
	{id: "protobuf-ls", file: "a.proto", language: "proto", text: "syntax = \"proto3\";\nmessage Smoke {\n  ‸\n}\n", want: "string"},
	{id: "fsautocomplete", file: "a.fsx", language: "fsharp", text: "let value = 1\nprintfn \"%d\" val‸\n", want: "value"},
}

func completionPosition(text string) (string, int, int) {
	index := strings.Index(text, "‸")
	before := text[:index]
	line := strings.Count(before, "\n")
	character := len([]rune(before[strings.LastIndex(before, "\n")+1:]))
	return strings.Replace(text, "‸", "", 1), line, character
}

func completionLabels(raw json.RawMessage) []string {
	var list struct {
		Items []struct {
			Label string `json:"label"`
		} `json:"items"`
	}
	var items []struct {
		Label string `json:"label"`
	}
	if json.Unmarshal(raw, &items) != nil {
		_ = json.Unmarshal(raw, &list)
		items = list.Items
	}
	labels := make([]string, 0, len(items))
	for _, item := range items {
		labels = append(labels, item.Label)
	}
	return labels
}

// TestMachineLSPCompletion asks every installed built-in server for a
// completion the way the editor does (opt-in: MINCODE_LSP_MACHINE_SMOKE=1).
func TestMachineLSPCompletion(t *testing.T) {
	if os.Getenv("MINCODE_LSP_MACHINE_SMOKE") != "1" {
		t.Skip("native completion smoke is opt-in")
	}
	only := os.Getenv("MINCODE_LSP_COMPLETION_ONLY")
	for _, c := range machineCompletionCases {
		if only != "" && !strings.Contains(","+only+",", ","+c.id+",") {
			continue
		}
		t.Run(c.id, func(t *testing.T) {
			d, ok := definitionByID(c.id)
			if !ok {
				t.Fatalf("unknown server %s", c.id)
			}
			if _, err := cachedLookPath(d.Command); err != nil {
				t.Skip("not installed")
			}
			root := t.TempDir()
			for name, content := range c.files {
				if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			text, line, character := completionPosition(c.text)
			path := filepath.Join(root, c.file)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
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
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			initialize, _ := json.Marshal(map[string]any{"processId": nil, "clientInfo": map[string]string{"name": "mincode"}, "rootUri": fileURI(root), "workspaceFolders": []any{map[string]string{"uri": fileURI(root), "name": "smoke"}}, "capabilities": smokeClientCapabilities()})
			if _, err := s.request(ctx, "initialize", initialize); err != nil {
				t.Fatalf("initialize: %v logs=%s", err, smokeTail(s.stderr))
			}
			_ = s.notify("initialized", json.RawMessage(`{}`))
			open, _ := json.Marshal(map[string]any{"textDocument": map[string]any{"uri": fileURI(path), "languageId": c.language, "version": 1, "text": text}})
			_ = s.notify("textDocument/didOpen", open)
			params, _ := json.Marshal(map[string]any{"textDocument": map[string]string{"uri": fileURI(path)}, "position": map[string]int{"line": line, "character": character}, "context": map[string]int{"triggerKind": 1}})
			var labels []string
			var lastErr error
			// Indexing servers answer empty until the project is loaded.
			for attempt := 0; attempt < 15 && ctx.Err() == nil; attempt++ {
				attemptCtx, attemptCancel := context.WithTimeout(ctx, 15*time.Second)
				result, err := s.request(attemptCtx, "textDocument/completion", params)
				attemptCancel()
				lastErr = err
				if err == nil {
					labels = completionLabels(result)
					if found(labels, c.want) {
						break
					}
				}
				time.Sleep(2 * time.Second)
			}
			if !found(labels, c.want) {
				t.Fatalf("completion at %d:%d returned %d items %v (want %q), last error %v, stderr=%s", line, character, len(labels), head(labels), c.want, lastErr, smokeTail(s.stderr))
			}
			t.Logf("%d items, e.g. %v", len(labels), head(labels))
		})
	}
}

func found(labels []string, want string) bool {
	if want == "" {
		return len(labels) > 0
	}
	for _, label := range labels {
		if strings.Contains(strings.ToLower(strings.TrimSpace(label)), strings.ToLower(want)) {
			return true
		}
	}
	return false
}

func head(labels []string) []string {
	if len(labels) > 5 {
		return labels[:5]
	}
	return labels
}
