//go:build darwin

package proctree

/*
#include <libproc.h>
#include <sys/resource.h>

// Physical footprint, what Activity Monitor and the force-quit dialog show as
// memory: unlike the resident size it includes compressed memory, which is
// most of a process's memory when the system is short of it.
static unsigned long long mc_phys_footprint(int pid) {
	struct rusage_info_v2 info;
	if (proc_pid_rusage(pid, RUSAGE_INFO_V2, (rusage_info_t *)&info) != 0) return 0;
	return info.ri_phys_footprint;
}
*/
import "C"

import (
	"bytes"
	"fmt"

	"golang.org/x/sys/unix"
)

// Supported reports whether List and Footprint work on this platform.
func Supported() bool { return true }

// List returns every process of the system.
func List() ([]Process, error) {
	kinfos, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	procs := make([]Process, 0, len(kinfos))
	for _, k := range kinfos {
		const szomb = 5 // sys/proc.h SZOMB
		if k.Proc.P_stat == szomb {
			continue
		}
		name := k.Proc.P_comm[:]
		if i := bytes.IndexByte(name, 0); i >= 0 {
			name = name[:i]
		}
		tty := int64(k.Eproc.Tdev)
		if k.Eproc.Tdev == -1 { // NODEV
			tty = NoTTY
		}
		procs = append(procs, Process{
			Pid:   int(k.Proc.P_pid),
			Ppid:  int(k.Eproc.Ppid),
			Tty:   tty,
			Name:  string(name),
			Start: fmt.Sprintf("%d.%d", k.Proc.P_starttime.Sec, k.Proc.P_starttime.Usec),
		})
	}
	return procs, nil
}

// Footprint is the physical memory footprint of pid in bytes, 0 when unknown.
func Footprint(pid int) uint64 { return uint64(C.mc_phys_footprint(C.int(pid))) }

// TTYOf returns the device of the terminal at path (as Process.Tty), or NoTTY.
func TTYOf(path string) int64 {
	var st unix.Stat_t
	if path == "" || unix.Stat(path, &st) != nil {
		return NoTTY
	}
	return int64(st.Rdev)
}
