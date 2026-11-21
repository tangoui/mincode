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

func TestWindowsClipboardWriteFilesCommandDoesNotInterpolatePaths(t *testing.T) {
	paths := []string{
		`C:\x\a’; calc; ’.txt`,
		`C:\x\b'; calc; '.txt`,
		`C:\x\foo[1]*.txt`,
		`C:\用户\文档 $env:PATH.txt`,
	}
	cmd, err := windowsClipboardWriteFilesCommand(paths)
	if err != nil {
		t.Fatal(err)
	}
	script := powershellScriptOf(t, cmd)
	if script != windowsClipboardWriteFilesScript {
		t.Fatalf("script must be constant, got %q", script)
	}
	if !strings.Contains(script, "Set-Clipboard -LiteralPath") || strings.Contains(script, "-Path ") {
		t.Fatalf("script must use -LiteralPath: %q", script)
	}
	if strings.Contains(script, `"`) {
		t.Fatalf("script must not contain double quotes: %q", script)
	}
	if got, want := stdinOf(t, cmd), strings.Join(paths, "\n"); got != want {
		t.Fatalf("stdin = %q, want %q", got, want)
	}
}

func TestWindowsClipboardWriteFilesCommandRejectsSeparators(t *testing.T) {
	for _, bad := range [][]string{
		nil,
		{""},
		{`C:\a` + "\n" + `C:\b`},
		{`C:\a` + "\r"},
		{`C:\a` + "\x00"},
	} {
		if _, err := windowsClipboardWriteFilesCommand(bad); err == nil {
			t.Fatalf("expected error for %q", bad)
		}
	}
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
	files := powershellScriptOf(t, windowsClipboardReadFilesCommand())
	if !strings.Contains(files, "Get-Clipboard -Format FileDropList") || !strings.Contains(files, "$_.FullName") || !strings.Contains(files, "[Text.Encoding]::UTF8.GetBytes") {
		t.Fatalf("read files script must emit full paths as raw UTF-8: %q", files)
	}
	for _, s := range []string{windowsClipboardWriteTextScript, windowsClipboardReadTextScript, windowsClipboardReadFilesScript} {
		if strings.Contains(s, `"`) {
			t.Fatalf("script must not contain double quotes: %q", s)
		}
	}
}
