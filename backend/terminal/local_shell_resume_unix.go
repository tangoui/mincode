//go:build (darwin && cgo) || linux

package terminal

import (
	"path/filepath"

	"golang.org/x/sys/unix"
)

func (u *unixPTY) foregroundGroup() int {
	conn, err := u.f.SyscallConn()
	if err != nil {
		return 0
	}
	var group int
	if err := conn.Control(func(fd uintptr) {
		value, err := unix.IoctlGetInt(int(fd), unix.TIOCGPGRP)
		if err == nil {
			group = value
		}
	}); err != nil {
		return 0
	}
	if group <= 0 {
		return foregroundGroupFromShell(u.Pid())
	}
	return group
}

func (u *unixPTY) resumeRecord() (terminalResumeRecord, bool) {
	if !u.resumeEligible {
		return terminalResumeRecord{}, false
	}
	shellPID := u.Pid()
	group := u.foregroundGroup()
	if group <= 0 || group == shellPID {
		return terminalResumeRecord{}, false
	}
	// A foreground process group is identified by its leader's PID.
	// Skip a leader that exited or is no longer in this PTY's session.
	if sid, err := unix.Getsid(group); err != nil || sid != shellPID {
		return terminalResumeRecord{}, false
	}
	executable, args := foregroundProcessArgs(group)
	if !filepath.IsAbs(executable) || len(args) == 0 || sensitiveResumeArgs(args) {
		return terminalResumeRecord{}, false
	}
	// Use the executable path reported by the OS, since a process may set argv[0]
	// to an arbitrary string, including the name of a shell builtin.
	args[0] = executable
	command := terminalResumeCommand(args, u.cmd.Path)
	// The shell cwd is the directory in which the job was launched. A program
	// can chdir after launch, including when it receives relative arguments.
	cwd, err := nativeProcessCwd(shellPID)
	if command == "" || err != nil || !filepath.IsAbs(cwd) || u.foregroundGroup() != group {
		return terminalResumeRecord{}, false
	}
	return terminalResumeRecord{Command: command, Cwd: cwd}, true
}
