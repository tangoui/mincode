//go:build windows

package git

import (
	"context"
	"errors"
	"os/exec"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"mincode/app/backend/core"
)

// Sampler-only containment. No taskkill subprocess, timer-only cancellation or
// change to core's other native process users. A child starts suspended so it
// cannot execute or create descendants before assignment to the owned job.
type pluginStatusWindowsProcess struct {
	cmd      *exec.Cmd
	job      windows.Handle
	assigned bool
}

func startPluginStatusProcess(ctx context.Context, cmd *exec.Cmd) (pluginStatusProcess, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	limit := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limit.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limit)), uint32(unsafe.Sizeof(limit))); err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	core.PrepareBackgroundCommand(cmd)
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
	if err = cmd.Start(); err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	process := &pluginStatusWindowsProcess{cmd: cmd, job: job}
	handle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return process, err
	}
	defer windows.CloseHandle(handle)
	if err = windows.AssignProcessToJobObject(job, handle); err != nil {
		return process, err
	}
	process.assigned = true
	if err = resumePluginStatusProcess(ctx, uint32(cmd.Process.Pid)); err != nil {
		return process, err
	}
	return process, nil
}

func resumePluginStatusProcess(ctx context.Context, pid uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if err = ctx.Err(); err != nil {
			return err
		}
		if entry.OwnerProcessID != pid {
			continue
		}
		thread, openErr := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if openErr != nil {
			return openErr
		}
		_, resumeErr := windows.ResumeThread(thread)
		windows.CloseHandle(thread)
		return resumeErr
	}
	return errors.New("git status suspended thread unavailable")
}

func (process *pluginStatusWindowsProcess) terminate(deadline time.Time) error {
	if !process.assigned {
		// Assignment failure is fail-closed: the root has never been resumed and
		// cannot have descendants. The caller still joins its cmd.Wait.
		return process.cmd.Process.Kill()
	}
	if err := windows.TerminateJobObject(process.job, 1); err != nil {
		return err
	}
	// JOBOBJECT_BASIC_ACCOUNTING_INFORMATION (Windows SDK winnt.h). Querying
	// active membership is needed: scheduling taskkill or closing handles alone
	// would not demonstrate that the descendants actually stopped.
	var accounting struct {
		TotalUserTime, TotalKernelTime, ThisPeriodTotalUserTime, ThisPeriodTotalKernelTime int64
		TotalPageFaultCount, TotalProcesses, ActiveProcesses, TotalTerminatedProcesses     uint32
	}
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	remaining := time.Until(deadline)
	if remaining < 0 {
		remaining = 0
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	for {
		if err := windows.QueryInformationJobObject(process.job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&accounting)), uint32(unsafe.Sizeof(accounting)), nil); err != nil {
			return err
		}
		if accounting.ActiveProcesses == 0 {
			return nil
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			return errors.New("git status job termination deadline exceeded")
		}
	}
}
func (process *pluginStatusWindowsProcess) close() { _ = windows.CloseHandle(process.job) }
