//go:build darwin && cgo

package terminal

/*
#include <sys/sysctl.h>

static int mincode_procargs(int pid, void *buf, size_t *size) {
	int mib[] = { CTL_KERN, KERN_PROCARGS2, pid };
	return sysctl(mib, 3, buf, size, NULL, 0);
}
*/
import "C"

import (
	"bytes"
	"encoding/binary"
	"unsafe"

	"golang.org/x/sys/unix"
)

func foregroundGroupFromShell(pid int) int {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0
	}
	return int(info.Eproc.Tpgid)
}

func foregroundProcessArgs(pid int) (string, []string) {
	var size C.size_t
	if C.mincode_procargs(C.int(pid), nil, &size) != 0 || size < 4 || size > 1<<20 {
		return "", nil
	}
	raw := make([]byte, int(size))
	if C.mincode_procargs(C.int(pid), unsafe.Pointer(&raw[0]), &size) != 0 || size < 4 {
		return "", nil
	}
	raw = raw[:int(size)]
	argc := int(binary.NativeEndian.Uint32(raw[:4]))
	path, tail, ok := bytes.Cut(raw[4:], []byte{0})
	if !ok || argc <= 0 || argc > len(tail) {
		return "", nil
	}
	// KERN_PROCARGS2 returns the executable path, NUL padding, argc arguments,
	// then the environment. Read exactly argc arguments.
	tail = bytes.TrimLeft(tail, "\x00")
	args := make([]string, 0, argc)
	for range argc {
		arg, rest, ok := bytes.Cut(tail, []byte{0})
		if !ok {
			return "", nil
		}
		args = append(args, string(arg))
		tail = rest
	}
	return string(path), args
}
