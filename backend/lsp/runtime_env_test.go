package lsp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"mincode/app/backend/core"
)

func TestRustAndRubyInstallPrerequisites(t *testing.T) {
	rust, _ := definitionByID("rust-analyzer")
	win := rust.InstallOS["windows"]
	if !win.Installable || win.Prerequisite != "winget" || !strings.Contains(win.InstallHint, "--id Rustlang.rust-analyzer --exact --source winget") || strings.Contains(win.InstallHint, "rust-lang.rust-analyzer") {
		t.Fatalf("incorrect Windows Rust install: %+v", win)
	}
	ruby, _ := definitionByID("ruby-lsp")
	if ruby.Prerequisite != "gem" || ruby.PrerequisiteHint == "" || ruby.InstallOS["windows"].Prerequisite != "gem" {
		t.Fatalf("Ruby installation must require RubyGems: %+v", ruby)
	}
	if err := checkInstallPrerequisite("", ""); err != nil {
		t.Fatal(err)
	}
	missing := "mincode-test-nonexistent-package-manager-47319"
	if err := checkInstallPrerequisite(missing, "install runtime first"); err == nil || !strings.Contains(err.Error(), missing) || !strings.Contains(err.Error(), "install runtime first") {
		t.Fatalf("missing prerequisite must block install with advice: %v", err)
	}
	primeLookPath(t, missing, os.Args[0])
	if err := checkInstallPrerequisite(missing, "install runtime first"); err != nil {
		t.Fatalf("discovered prerequisite must allow installation: %v", err)
	}
}

func TestParameterlessNotificationsOmitNullParams(t *testing.T) {
	var output bytes.Buffer
	s := &session{stdin: &output}
	for _, params := range []json.RawMessage{nil, json.RawMessage("null")} {
		if err := s.notify("exit", params); err != nil {
			t.Fatal(err)
		}
		raw, err := readMessage(bufio.NewReader(&output))
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(raw, &fields)
		if _, ok := fields["params"]; ok {
			t.Fatalf("parameterless method contains params: %s", raw)
		}
	}
}

func TestShutdownRequestOmitsNullParams(t *testing.T) {
	var output bytes.Buffer
	s := &session{stdin: &output, pending: map[uint64]chan rpcReply{}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = s.request(ctx, "shutdown", json.RawMessage("null"))
	raw, err := readMessage(bufio.NewReader(&output))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	if _, ok := fields["params"]; ok {
		t.Fatalf("shutdown contains null params: %s", raw)
	}
}

func TestAstroInitializeReceivesSDKWithoutVueOptions(t *testing.T) {
	root := t.TempDir()
	sdk := writeTypeScriptLib(t, root)
	s := &session{root: root, definition: serverDefinition{ID: "astro-ls"}}
	raw, err := s.prepareInitialize(json.RawMessage(`{"initializationOptions":{"custom":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	options, _ := jsonObject(fields["initializationOptions"])
	ts, _ := jsonObject(options["typescript"])
	var got string
	_ = json.Unmarshal(ts["tsdk"], &got)
	if got != sdk || options["vue"] != nil || string(options["custom"]) != "true" {
		t.Fatalf("incorrect Astro init: %s", raw)
	}
}

func envValue(env []string, name string) string {
	for _, entry := range env {
		if strings.HasPrefix(entry, name+"=") {
			return strings.TrimPrefix(entry, name+"=")
		}
	}
	return ""
}

func TestGraphQLTransportUsesStream(t *testing.T) {
	d, _ := definitionByID("graphql-lsp")
	if strings.Join(d.Arguments, " ") != "server --method stream" {
		t.Fatalf("GraphQL CLI requires stream, not stdio: %v", d.Arguments)
	}
}

func TestAngularGlobalProbeFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable fixture")
	}
	// Resolve /var -> /private/var on macOS so expectations match EvalSymlinks output.
	prefix, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(prefix, "bin")
	server := filepath.Join(prefix, "lib", "node_modules", "@angular", "language-server")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(server, 0755); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(server, "index.js")
	if err := os.WriteFile(entry, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(entry, filepath.Join(bin, "ngserver")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	invalidateLookPathCache("ngserver")
	t.Cleanup(func() { invalidateLookPathCache("ngserver") })
	args := angularProbeArguments(t.TempDir())
	if len(args) < 4 || args[1] != filepath.Join(prefix, "lib", "node_modules") || args[3] != server {
		t.Fatalf("incorrect global probes: %v", args)
	}
}

func TestBuiltinRuntimeEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable fixture")
	}
	// Resolve /var -> /private/var on macOS so expectations match EvalSymlinks output.
	prefix, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ruby := filepath.Join(prefix, "opt", "ruby", "bin", "ruby")
	wrapper := filepath.Join(prefix, "lib", "ruby", "gems", "4.0.0", "bin", "ruby-lsp")
	dotnet := filepath.Join(prefix, "bin", "dotnet")
	for _, path := range []string{ruby, wrapper, dotnet} {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	root := filepath.Join(prefix, "libexec")
	if err := os.MkdirAll(filepath.Join(root, "host", "fxr"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Join(prefix, "bin"))
	t.Setenv("DOTNET_ROOT", "")
	invalidateLookPathCache("dotnet")
	t.Cleanup(func() { invalidateLookPathCache("dotnet") })
	for _, id := range []string{"csharp-ls", "fsautocomplete"} {
		env := builtinServerEnv(serverDefinition{ID: id}, "", nil)
		if got := envValue(env, "DOTNET_ROOT"); got != root {
			t.Fatalf("%s root=%s", id, got)
		}
	}
	env := builtinServerEnv(serverDefinition{ID: "ruby-lsp"}, wrapper, nil)
	if !strings.HasPrefix(envValue(env, "PATH"), filepath.Dir(ruby)+string(os.PathListSeparator)) {
		t.Fatal("gem runtime not preferred")
	}
	t.Setenv("DOTNET_ROOT", "/explicit/user/runtime")
	if envValue(builtinServerEnv(serverDefinition{ID: "csharp-ls"}, "", nil), "DOTNET_ROOT") != "/explicit/user/runtime" {
		t.Fatal("user runtime overridden")
	}
	if envValue(builtinServerEnv(serverDefinition{ID: "gopls"}, wrapper, nil), "PATH") != envValue(core.ExpandedEnv(), "PATH") {
		t.Fatal("unrelated server runtime changed")
	}
}
