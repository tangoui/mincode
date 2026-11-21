package misc

import (
	"io"
	"os/exec"
	"strings"
	"testing"
)

func powershellScriptOf(t *testing.T, cmd *exec.Cmd) string {
	t.Helper()
	if len(cmd.Args) < 2 || cmd.Args[0] != "powershell" || cmd.Args[len(cmd.Args)-2] != "-Command" {
		t.Fatalf("unexpected command args: %q", cmd.Args)
	}
	return cmd.Args[len(cmd.Args)-1]
}

func stdinOf(t *testing.T, cmd *exec.Cmd) string {
	t.Helper()
	if cmd.Stdin == nil {
		t.Fatal("stdin not set")
	}
	data, err := io.ReadAll(cmd.Stdin)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestWindowsClipboardTextCommandsUseUTF8Streams(t *testing.T) {
	text := "中文 ’quote’ $x `n\r\nline2\n"
	write := windowsClipboardWriteTextCommand(text)
	script := powershellScriptOf(t, write)
	if script != windowsClipboardWriteTextScript || strings.Contains(script, text) {
		t.Fatalf("write script must be constant: %q", script)
	}
	if !strings.Contains(script, "[Text.Encoding]::UTF8.GetString") || !strings.Contains(script, "Set-Clipboard -Value $s") {
		t.Fatalf("write script must decode stdin as UTF-8: %q", script)
	}
	if got := stdinOf(t, write); got != text {
		t.Fatalf("stdin = %q, want %q", got, text)
	}

	read := windowsClipboardReadTextCommand()
	script = powershellScriptOf(t, read)
	if !strings.Contains(script, "Get-Clipboard -Raw") || !strings.Contains(script, "[Text.Encoding]::UTF8.GetBytes") {
		t.Fatalf("read script must emit raw UTF-8: %q", script)
	}
	for _, s := range []string{windowsClipboardWriteTextScript, windowsClipboardReadTextScript} {
		if strings.Contains(s, `"`) {
			t.Fatalf("script must not contain double quotes: %q", s)
		}
	}
}
