// Package proctree lists local processes and groups them by the terminal that
// started them: what a terminal session holds (its memory) and what closing it
// must end.
package proctree

import (
	"os"
	"sort"
)

// NoTTY is the Tty of a process without a controlling terminal.
const NoTTY int64 = -1

// Process is one entry of a process listing.
type Process struct {
	Pid  int
	Ppid int
	// Tty is the controlling terminal's device (comparable with TTYOf), or NoTTY.
	Tty  int64
	Name string
	// Start identifies this process instance (its start time): a pid reused by
	// a later process has another Start.
	Start string
}

// Members returns the processes of the terminal session rooted at root: root,
// its descendants, and every process still attached to the session's
// terminal tty (a double-forked background job is reparented away from the
// shell but keeps the terminal). This process and init are never members.
func Members(procs []Process, root int, tty int64) []Process {
	self := os.Getpid()
	children := make(map[int][]int, len(procs))
	byPid := make(map[int]Process, len(procs))
	for _, p := range procs {
		byPid[p.Pid] = p
		children[p.Ppid] = append(children[p.Ppid], p.Pid)
	}
	seen := map[int]bool{}
	var out []Process
	add := func(pid int) bool {
		if seen[pid] || pid <= 1 {
			return false
		}
		seen[pid] = true
		if p, ok := byPid[pid]; ok && pid != self {
			out = append(out, p)
		}
		return true
	}
	queue := []int{root}
	if tty != NoTTY {
		for _, p := range procs {
			if p.Tty == tty {
				queue = append(queue, p.Pid)
			}
		}
	}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if !add(pid) {
			continue
		}
		queue = append(queue, children[pid]...)
	}
	return out
}

// ProcessUsage is the memory of one process.
type ProcessUsage struct {
	Pid   int    `json:"pid"`
	Name  string `json:"name"`
	Bytes uint64 `json:"bytes"`
}

// Usage is the memory of a group of processes, largest first.
type Usage struct {
	Bytes     uint64         `json:"bytes"`
	Processes []ProcessUsage `json:"processes"`
}

// Measure returns the physical memory footprint of procs (see Footprint).
func Measure(procs []Process) Usage { return usageOf(procs, Footprint) }

func usageOf(procs []Process, footprint func(pid int) uint64) Usage {
	usage := Usage{Processes: make([]ProcessUsage, 0, len(procs))}
	for _, p := range procs {
		bytes := footprint(p.Pid)
		usage.Bytes += bytes
		usage.Processes = append(usage.Processes, ProcessUsage{Pid: p.Pid, Name: p.Name, Bytes: bytes})
	}
	sort.SliceStable(usage.Processes, func(i, j int) bool { return usage.Processes[i].Bytes > usage.Processes[j].Bytes })
	return usage
}
