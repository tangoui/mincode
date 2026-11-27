package terminal

import (
	"bytes"
	"os"
	"strconv"
	"strings"
)

// The Linux PTY master already exposes the foreground group via TIOCGPGRP.
func foregroundGroupFromShell(_ int) int { return 0 }

func foregroundProcessArgs(pid int) (string, []string) {
	proc := "/proc/" + strconv.Itoa(pid)
	path, err := os.Readlink(proc + "/exe")
	if err != nil || strings.HasSuffix(path, " (deleted)") {
		return "", nil
	}
	raw, err := os.ReadFile(proc + "/cmdline")
	if err != nil || len(raw) == 0 || raw[len(raw)-1] != 0 {
		return "", nil
	}
	var args []string
	for arg := range bytes.SplitSeq(raw[:len(raw)-1], []byte{0}) {
		args = append(args, string(arg))
	}
	return path, args
}
