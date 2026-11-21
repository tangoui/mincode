//go:build windows

package misc

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsBackgroundToolCommandsHaveNoConsole(t *testing.T) {
	probe := cliToolProbeCommand(`C:\Program Files\Node\node.exe`, `C:\Program Files\Node`)
	clipboard := windowsPowerShellCommand("exit 0")
	if probe.SysProcAttr == nil || !probe.SysProcAttr.HideWindow || probe.SysProcAttr.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
		t.Fatal("CLI version probe can create a console window")
	}
	if clipboard.SysProcAttr == nil || !clipboard.SysProcAttr.HideWindow || clipboard.SysProcAttr.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
		t.Fatal("clipboard PowerShell command can create a console window")
	}
	if len(probe.Args) != 4 || probe.Args[2] != `C:\Program Files\Node\node.exe` || probe.Args[3] != "--version" {
		t.Fatalf("probe arguments changed: %#v", probe.Args)
	}
}
