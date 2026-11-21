//go:build windows

package terminal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// externalPIDTimeout bounds the wait for the program to appear under cmd.exe.
var externalPIDTimeout = 10 * time.Second

// windowsConsoleName is the terminal shown to the user. A new console opens
// in the system's default terminal application (Windows Terminal or the
// classic console host).
const windowsConsoleName = "Windows Console"

var procIsProcessInJob = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")

// SweepExternalLaunchDirs has nothing to sweep on Windows: a launch writes
// nothing to disk.
func SweepExternalLaunchDirs() {}

// startExternalDebuggee runs the program under cmd.exe in a new console
// (CREATE_NEW_CONSOLE). The environment and working directory go to the
// process directly, so nothing is written to disk and no value is escaped
// for cmd.exe except the program path and arguments
// (windowsConsoleCommandLine).
//
// The shell starts suspended and is put in a job object of its own before
// it runs, so the job holds exactly the launch's processes (the shell, the
// program and everything they start) whatever happens to pids. The
// program's pid is the job member that is the shell's child of that name; a
// batch launcher runs inside cmd.exe itself, so then only the shell's pid is
// known. End ends every job member except the shell and the console host.
func startExternalDebuggee(ctx context.Context, spec ProfileShellSpec, title string) (*ExternalDebuggee, error) {
	comspec := "cmd.exe"
	if root := os.Getenv("SystemRoot"); root != "" {
		comspec = filepath.Join(root, "System32", "cmd.exe")
	}
	line, err := windowsConsoleCommandLine(comspec, spec.Executable, spec.Args)
	if err != nil {
		return nil, err
	}
	env := withoutEnv(os.Environ(), spec.unsetEnv, true)
	env = dedupeEnv(append(env, profileEnvPairs(spec.Env)...), true)
	launch, err := startNewConsoleInJob(comspec, line, env, spec.Cwd, sanitizeTitle(title))
	if err != nil {
		return nil, externalError(ExternalFailed, "start %s: %w", comspec, err)
	}
	d := &ExternalDebuggee{ShellPID: launch.shellPID, Terminal: windowsConsoleName, end: launch.end}
	if isBatchFile(spec.Executable) {
		return d, nil
	}
	pid, err := launch.waitProgram(ctx, filepath.Base(spec.Executable), externalPIDTimeout)
	if err != nil {
		// The session ended while the program was starting: end the whole launch.
		launch.terminate()
		return nil, err
	}
	// pid 0: not seen in time (it may already have finished); never start it
	// a second time, End still reaches whatever the launch left running.
	d.PID = pid
	return d, nil
}

// consoleLaunch is a shell in a new console and the job holding it.
type consoleLaunch struct {
	job, shell windows.Handle
	shellPID   int
}

// startNewConsoleInJob creates the shell suspended with CreateProcess (os/exec
// always hands the child standard handles, NUL when unset, which would keep
// the program's input and output away from its new console window), assigns
// it to a new job object and resumes it.
func startNewConsoleInJob(comspec, commandLine string, env []string, cwd, title string) (*consoleLaunch, error) {
	app, err := windows.UTF16PtrFromString(comspec)
	if err != nil {
		return nil, err
	}
	line, err := windows.UTF16FromString(commandLine)
	if err != nil {
		return nil, err
	}
	dir, err := windows.UTF16PtrFromString(cwd)
	if err != nil {
		return nil, err
	}
	block := windowsEnvBlock(env)
	si := windows.StartupInfo{}
	si.Cb = uint32(unsafe.Sizeof(si))
	if title != "" {
		if si.Title, err = windows.UTF16PtrFromString(title); err != nil {
			return nil, err
		}
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create job object: %w", err)
	}
	var pi windows.ProcessInformation
	err = windows.CreateProcess(app, &line[0], nil, nil, false,
		windows.CREATE_NEW_CONSOLE|windows.CREATE_UNICODE_ENVIRONMENT|windows.CREATE_SUSPENDED, &block[0], dir, &si, &pi)
	if err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	defer windows.CloseHandle(pi.Thread)
	if err := windows.AssignProcessToJobObject(job, pi.Process); err != nil {
		_ = windows.TerminateProcess(pi.Process, 1)
		_ = windows.CloseHandle(pi.Process)
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("assign the console to a job object: %w", err)
	}
	if _, err := windows.ResumeThread(pi.Thread); err != nil {
		_ = windows.TerminateJobObject(job, 1)
		_ = windows.CloseHandle(pi.Process)
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("resume the console: %w", err)
	}
	return &consoleLaunch{job: job, shell: pi.Process, shellPID: int(pi.ProcessId)}, nil
}

// waitProgram polls for the job member that is the shell's child named exe.
// It returns 0 when none appeared within timeout, and an error only when ctx
// ended.
func (l *consoleLaunch) waitProgram(ctx context.Context, exe string, timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	for {
		if pid := windowsProgramPID(processSnapshot(), l.members(), l.shellPID, exe); pid != 0 {
			return pid, nil
		}
		if err := ctx.Err(); err != nil {
			return 0, fmt.Errorf("external terminal: %w", err)
		}
		if time.Now().After(deadline) {
			return 0, nil
		}
		select {
		case <-ctx.Done():
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// end ends what the launch still runs except the shell and the console
// host, so the shell reaches `pause` and the window stays; then it releases
// the job and shell handles. It is forced: a console program in another
// console cannot be asked politely (a non-forced taskkill either fails for
// it or posts WM_CLOSE to the console window, closing the window), and
// GenerateConsoleCtrlEvent would need AttachConsole, process-wide state of
// MinCode itself. A member is ended only through a handle that is checked
// to still be in the job, so a reused pid is never touched.
func (l *consoleLaunch) end() {
	defer windows.CloseHandle(l.job)
	defer windows.CloseHandle(l.shell)
	// A process may start another while its tree is ended: repeat a few times.
	for range 5 {
		targets := windowsEndTargets(processSnapshot(), l.members(), l.shellPID)
		if len(targets) == 0 {
			return
		}
		for _, pid := range targets {
			l.terminateMember(pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// terminate ends the whole launch, the shell (and so the window) included.
func (l *consoleLaunch) terminate() {
	_ = windows.TerminateJobObject(l.job, 1)
	_ = windows.CloseHandle(l.shell)
	_ = windows.CloseHandle(l.job)
}

func (l *consoleLaunch) terminateMember(pid int) {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return // already gone
	}
	defer windows.CloseHandle(h)
	var in int32
	if r, _, _ := procIsProcessInJob.Call(uintptr(h), uintptr(l.job), uintptr(unsafe.Pointer(&in))); r == 0 || in == 0 {
		return // the pid now belongs to another process
	}
	_ = windows.TerminateProcess(h, 1)
	_, _ = windows.WaitForSingleObject(h, 2000)
}

// jobProcessIDList is JOBOBJECT_BASIC_PROCESS_ID_LIST with room for 1024 ids.
type jobProcessIDList struct {
	Assigned uint32
	InList   uint32
	IDs      [1024]uintptr
}

// members lists the job's live processes.
func (l *consoleLaunch) members() []int {
	var list jobProcessIDList
	err := windows.QueryInformationJobObject(l.job, windows.JobObjectBasicProcessIdList,
		uintptr(unsafe.Pointer(&list)), uint32(unsafe.Sizeof(list)), nil)
	if err != nil && !errors.Is(err, windows.ERROR_MORE_DATA) {
		return nil
	}
	n := min(int(list.InList), len(list.IDs))
	out := make([]int, 0, n)
	for _, id := range list.IDs[:n] {
		out = append(out, int(id))
	}
	return out
}

// processSnapshot lists every process with its parent and executable name.
func processSnapshot() []windowsProcess {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	var out []windowsProcess
	for err = windows.Process32First(snap, &entry); err == nil; err = windows.Process32Next(snap, &entry) {
		out = append(out, windowsProcess{
			pid:    int(entry.ProcessID),
			parent: int(entry.ParentProcessID),
			name:   windows.UTF16ToString(entry.ExeFile[:]),
		})
	}
	return out
}
