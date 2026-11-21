//go:build darwin && cgo

package terminal

/*
#include <libproc.h>
#include <string.h>
#include <sys/proc_info.h>

static int mincode_proc_cwd(int pid, char *buf, int size) {
  struct proc_vnodepathinfo info;
  int n = proc_pidinfo(pid, PROC_PIDVNODEPATHINFO, 0, &info, sizeof(info));
  if (n != (int)sizeof(info)) {
    return -1;
  }
  strlcpy(buf, info.pvi_cdir.vip_path, size);
  return 0;
}
*/
import "C"

import (
	"errors"
	"unsafe"
)

// nativeProcessCwd 经 libproc 直接读取进程 cwd，免去每个面板一次 lsof 子进程。
func nativeProcessCwd(pid int) (string, error) {
	if pid <= 0 {
		return "", errors.New("invalid pid")
	}
	var buf [C.MAXPATHLEN]C.char
	if C.mincode_proc_cwd(C.int(pid), &buf[0], C.int(len(buf))) != 0 {
		return "", errors.New("proc_pidinfo failed")
	}
	return C.GoString((*C.char)(unsafe.Pointer(&buf[0]))), nil
}
