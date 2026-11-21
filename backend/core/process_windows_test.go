//go:build windows

package core

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestPrepareBackgroundCommandPreservesProcessAttributes(t *testing.T) {
	cmd := exec.Command("cmd", "/C", "exit", "0")
	attrs := &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP, CmdLine: "existing command line"}
	cmd.SysProcAttr = attrs
	PrepareBackgroundCommand(cmd)
	PrepareBackgroundCommand(cmd)
	if cmd.SysProcAttr != attrs || attrs.CmdLine != "existing command line" {
		t.Fatal("existing process attributes were replaced")
	}
	if !attrs.HideWindow || attrs.CreationFlags != windows.CREATE_NEW_PROCESS_GROUP|windows.CREATE_NO_WINDOW {
		t.Fatalf("unexpected background flags: %#v", attrs)
	}
}

func TestBackgroundCommandFactoriesDisableConsole(t *testing.T) {
	cmd, cancel := CommandWithTimeout(time.Second, "cmd", "/C", "exit", "0")
	defer cancel()
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.HideWindow || cmd.SysProcAttr.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
		t.Fatal("timeout command can create a console")
	}
	if cmd.WaitDelay != commandWaitDelay {
		t.Fatal("timeout command lost its bounded pipe wait")
	}
	cmd = exec.Command("cmd", "/C", "exit", "0")
	PrepareProcessTree(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.HideWindow || cmd.SysProcAttr.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
		t.Fatal("process-tree command can create a console")
	}
}

// A real console-subsystem child must have no attached console. This also
// verifies that cmd.exe passes the no-console behavior on to its child.
func TestBackgroundCommandRunsWithoutConsoleAndPreservesOutput(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, throughShell := range []bool{false, true} {
		name, args := executable, []string{"-test.run=^TestBackgroundConsoleHelper$"}
		if throughShell {
			name, args = "cmd", []string{"/D", "/C", executable, "-test.run=^TestBackgroundConsoleHelper$"}
		}
		cmd, cancel := CommandWithTimeout(10*time.Second, name, args...)
		cmd.Env = append(os.Environ(), "MINCODE_TEST_NO_CONSOLE=1")
		output, runErr := cmd.CombinedOutput()
		cancel()
		if runErr != nil || !strings.Contains(string(output), "no-console-stdout") || !strings.Contains(string(output), "no-console-stderr") {
			t.Fatalf("throughShell=%v err=%v output=%s", throughShell, runErr, output)
		}
	}
}

func TestBackgroundConsoleHelper(t *testing.T) {
	if os.Getenv("MINCODE_TEST_NO_CONSOLE") != "1" {
		return
	}
	getConsoleWindow := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow")
	hwnd, _, _ := getConsoleWindow.Call()
	if hwnd != 0 {
		t.Fatalf("background child has a console window: 0x%x", hwnd)
	}
	fmt.Fprintln(os.Stdout, "no-console-stdout")
	fmt.Fprintln(os.Stderr, "no-console-stderr")
}
