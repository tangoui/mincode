package plugin

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNodePluginInterpreterMissing(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "plugin.mjs")
	if err := os.WriteFile(entry, []byte("#!/usr/bin/env node\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := checkNodePluginInterpreter(entry, []string{"PATH=" + root}); err == nil || !strings.Contains(err.Error(), "Node.js") {
		t.Fatalf("expected actionable Node error: %v", err)
	}
}

func TestNodePluginUsesInterpreterFromAllowedPath(t *testing.T) {
	root := t.TempDir()
	executable := "node"
	if runtime.GOOS == "windows" {
		executable = "node.exe"
	}
	node := filepath.Join(root, executable)
	if err := os.WriteFile(node, []byte("runtime"), 0700); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(root, "plugin.mjs")
	command, err := pluginProcessCommand(entry, []string{"--example"}, []string{"PATH=" + root})
	if err != nil {
		t.Fatal(err)
	}
	if command.Path != node || len(command.Args) != 3 || command.Args[1] != entry || command.Args[2] != "--example" {
		t.Fatalf("unexpected node command: %#v", command.Args)
	}
}

func TestPluginEnvironmentDesktopPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix desktop PATH")
	}
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("MINCODE_TEST_SECRET", "must-not-leak")
	environment := pluginProcessEnvironment("example.test", t.TempDir(), 2)
	count := 0
	for _, item := range environment {
		if strings.HasPrefix(item, "MINCODE_TEST_SECRET=") {
			t.Fatal("environment allowlist bypassed")
		}
		if strings.HasPrefix(item, "PATH=") {
			count++
			if !strings.HasPrefix(item, "PATH=/usr/bin:/bin:") || !strings.Contains(item, "/opt/homebrew/bin") {
				t.Fatalf("unexpected PATH: %s", item)
			}
		}
	}
	if count != 1 {
		t.Fatalf("PATH count: %d", count)
	}
}
