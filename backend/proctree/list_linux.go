//go:build linux

package proctree

import (
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// Supported reports whether List and Footprint work on this platform.
func Supported() bool { return true }

// List returns every process of the system (from /proc).
func List() ([]Process, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	procs := make([]Process, 0, len(entries))
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		if p, ok := readStat(pid); ok {
			procs = append(procs, p)
		}
	}
	return procs, nil
}

// readStat parses /proc/<pid>/stat: "pid (comm) state ppid pgrp session tty_nr ... starttime(22)".
func readStat(pid int) (Process, bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return Process{}, false
	}
	text := string(data)
	open, closeParen := strings.IndexByte(text, '('), strings.LastIndexByte(text, ')')
	if open < 0 || closeParen < open {
		return Process{}, false
	}
	fields := strings.Fields(text[closeParen+1:])
	// fields[0] is the state (field 3), so field n is fields[n-3].
	if len(fields) < 20 || fields[0] == "Z" {
		return Process{}, false
	}
	ppid, _ := strconv.Atoi(fields[1])
	ttyNr, _ := strconv.ParseInt(fields[4], 10, 64)
	tty := ttyNr
	if ttyNr == 0 {
		tty = NoTTY
	}
	return Process{Pid: pid, Ppid: ppid, Tty: tty, Name: text[open+1 : closeParen], Start: fields[19]}, true
}

// Footprint is the resident plus swapped memory of pid in bytes, 0 when unknown.
func Footprint(pid int) uint64 {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return 0
	}
	var total uint64
	for _, line := range strings.Split(string(data), "\n") {
		name, value, ok := strings.Cut(line, ":")
		if !ok || (name != "VmRSS" && name != "VmSwap") {
			continue
		}
		fields := strings.Fields(value)
		if len(fields) > 0 {
			kb, _ := strconv.ParseUint(fields[0], 10, 64)
			total += kb * 1024
		}
	}
	return total
}

// TTYOf returns the device of the terminal at path, encoded like tty_nr in
// /proc/<pid>/stat, or NoTTY.
func TTYOf(path string) int64 {
	var st unix.Stat_t
	if path == "" || unix.Stat(path, &st) != nil {
		return NoTTY
	}
	major, minor := int64(unix.Major(st.Rdev)), int64(unix.Minor(st.Rdev))
	return (minor & 0xff) | (major&0xfff)<<8 | (minor&^0xff)<<12
}
