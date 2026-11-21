package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"mincode/app/plugins/api"
)

func terminalProfileTestContext(t *testing.T) *pointContext {
	t.Helper()
	root := t.TempDir()
	writeTerminalProfileExecutable(t, root, "bin/repl")
	return &pointContext{
		root:     root,
		manifest: &Manifest{ID: "term.plugin", APIVersion: 2, Capabilities: []api.Capability{api.CapabilityShell}},
		spec:     terminalProfileSpec,
	}
}

func writeTerminalProfileExecutable(t *testing.T, root, relative string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func decodeTerminalProfileForTest(ctx *pointContext, raw string) (TerminalProfileContribution, error) {
	value, _, err := decodeTerminalProfileEntry(ctx, 0, "", json.RawMessage(raw))
	if err != nil {
		return TerminalProfileContribution{}, err
	}
	return value.(TerminalProfileContribution), nil
}

func TestTerminalProfileDecodeDefaultsAndSnapshot(t *testing.T) {
	ctx := terminalProfileTestContext(t)
	profile, err := decodeTerminalProfileForTest(ctx, `{"id":"repl","title":"REPL","titleLocalized":{"zh":"交互"},"icon":"terminal","command":"${pluginRoot}/bin/repl","args":["--dir","${workspaceFolder}","$HOME"],"env":{"MODE":"x"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Cwd != "${workspaceFolder}" || len(profile.Platforms) != 3 || profile.TitleLocalized["zh"] != "交互" || profile.Icon != "terminal" {
		t.Fatalf("unexpected snapshot %+v", profile)
	}
	if len(profile.SpecFingerprint) != 64 {
		t.Fatalf("fingerprint %q", profile.SpecFingerprint)
	}
	other, err := decodeTerminalProfileForTest(ctx, `{"id":"repl","title":"REPL","command":"${pluginRoot}/bin/repl","args":["--dir","${workspaceFolder}","$HOME"],"env":{"MODE":"y"}}`)
	if err != nil || other.SpecFingerprint == profile.SpecFingerprint {
		t.Fatalf("env change must change the spec fingerprint: %v", err)
	}
	for _, raw := range []string{
		`{"id":"py","title":"Python","command":"python3","platforms":["linux","darwin"]}`,
		`{"id":"abs","title":"Top","command":"/usr/bin/top","cwd":"${userHome}/work"}`,
		`{"id":"win","title":"Pwsh","command":"C:\\Program Files\\PowerShell\\7\\pwsh.exe","platforms":["windows"]}`,
		`{"id":"root-cwd","title":"Root","command":"python3","cwd":"${pluginRoot}/bin"}`,
	} {
		if _, err := decodeTerminalProfileForTest(ctx, raw); err != nil {
			t.Errorf("%s rejected: %v", raw, err)
		}
	}
}

func TestTerminalProfileDecodeRejectsInvalidEntries(t *testing.T) {
	ctx := terminalProfileTestContext(t)
	cases := map[string]string{
		"bad id":             `{"id":"Bad","title":"x","command":"python3"}`,
		"missing title":      `{"id":"a","command":"python3"}`,
		"multiline title":    `{"id":"a","title":"a\nb","command":"python3"}`,
		"unknown field":      `{"id":"a","title":"x","command":"python3","initialInput":"ls\n"}`,
		"shell type":         `{"id":"a","title":"x","command":"python3","shellType":"bash"}`,
		"bad icon":           `{"id":"a","title":"x","icon":"rocket-launch","command":"python3"}`,
		"relative command":   `{"id":"a","title":"x","command":"bin/tool"}`,
		"workspace command":  `{"id":"a","title":"x","command":"${workspaceFolder}/tool"}`,
		"root escape":        `{"id":"a","title":"x","command":"${pluginRoot}/../tool"}`,
		"root missing":       `{"id":"a","title":"x","command":"${pluginRoot}/bin/missing"}`,
		"root directory":     `{"id":"a","title":"x","command":"${pluginRoot}/bin"}`,
		"abs dotdot":         `{"id":"a","title":"x","command":"/usr/../bin/sh"}`,
		"unknown variable":   `{"id":"a","title":"x","command":"python3","args":["${env:HOME}"]}`,
		"unterminated":       `{"id":"a","title":"x","command":"python3","args":["${workspaceFolder"]}`,
		"control arg":        `{"id":"a","title":"x","command":"python3","args":["a\u001bb"]}`,
		"bidi arg":           `{"id":"a","title":"x","command":"python3","args":["a\u202eb"]}`,
		"too many args":      `{"id":"a","title":"x","command":"python3","args":[` + strings.TrimSuffix(strings.Repeat(`"a",`, 33), ",") + `]}`,
		"denied PATH":        `{"id":"a","title":"x","command":"python3","env":{"PATH":"/tmp"}}`,
		"denied lowercase":   `{"id":"a","title":"x","command":"python3","env":{"node_options":"--require x"}}`,
		"denied prefix":      `{"id":"a","title":"x","command":"python3","env":{"DYLD_INSERT_LIBRARIES":"x"}}`,
		"mincode prefix":     `{"id":"a","title":"x","command":"python3","env":{"MINCODE_TOKEN":"x"}}`,
		"bad env name":       `{"id":"a","title":"x","command":"python3","env":{"A-B":"x"}}`,
		"env variable":       `{"id":"a","title":"x","command":"python3","env":{"A":"${env:X}"}}`,
		"absolute cwd":       `{"id":"a","title":"x","command":"python3","cwd":"/tmp"}`,
		"cwd dotdot":         `{"id":"a","title":"x","command":"python3","cwd":"${userHome}/../x"}`,
		"cwd two variables":  `{"id":"a","title":"x","command":"python3","cwd":"${userHome}/${pluginRoot}"}`,
		"bad platform":       `{"id":"a","title":"x","command":"python3","platforms":["freebsd"]}`,
		"empty platforms":    `{"id":"a","title":"x","command":"python3","platforms":[]}`,
		"duplicate platform": `{"id":"a","title":"x","command":"python3","platforms":["linux","linux"]}`,
	}
	for name, raw := range cases {
		if _, err := decodeTerminalProfileForTest(ctx, raw); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
}

func TestTerminalProfileDecodeRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	ctx := terminalProfileTestContext(t)
	outside := writeTerminalProfileExecutable(t, t.TempDir(), "tool")
	if err := os.Symlink(outside, filepath.Join(ctx.root, "bin", "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeTerminalProfileForTest(ctx, `{"id":"a","title":"x","command":"${pluginRoot}/bin/link"}`); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink escape rejection, got %v", err)
	}
}

func TestTerminalProfileDecodeRequiresExecBit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no execute bit on Windows")
	}
	ctx := terminalProfileTestContext(t)
	if err := os.WriteFile(filepath.Join(ctx.root, "bin", "plain"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeTerminalProfileForTest(ctx, `{"id":"a","title":"x","command":"${pluginRoot}/bin/plain"}`); err == nil || !strings.Contains(err.Error(), "not executable") {
		t.Fatalf("expected exec-bit rejection, got %v", err)
	}
}
