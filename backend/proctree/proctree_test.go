package proctree

import (
	"os"
	"reflect"
	"sort"
	"testing"
)

func pids(procs []Process) []int {
	out := make([]int, 0, len(procs))
	for _, p := range procs {
		out = append(out, p.Pid)
	}
	sort.Ints(out)
	return out
}

func TestMembersCollectsDescendantsAndTerminalProcesses(t *testing.T) {
	procs := []Process{
		{Pid: 100, Ppid: 1, Tty: 7},       // the shell
		{Pid: 101, Ppid: 100, Tty: 7},     // foreground program
		{Pid: 102, Ppid: 101, Tty: NoTTY}, // its child that dropped the terminal
		{Pid: 103, Ppid: 1, Tty: 7},       // double-forked, reparented, still on the terminal
		{Pid: 200, Ppid: 1, Tty: 8},       // another terminal
		{Pid: 201, Ppid: 200, Tty: 8},
		{Pid: 300, Ppid: 1, Tty: NoTTY},
	}
	got := pids(Members(procs, 100, 7))
	if want := []int{100, 101, 102, 103}; !reflect.DeepEqual(got, want) {
		t.Fatalf("members = %v, want %v", got, want)
	}
}

func TestMembersWithoutTerminalOnlyFollowsTheTree(t *testing.T) {
	procs := []Process{
		{Pid: 100, Ppid: 1, Tty: NoTTY},
		{Pid: 101, Ppid: 100, Tty: NoTTY},
		{Pid: 300, Ppid: 1, Tty: NoTTY},
	}
	if got := pids(Members(procs, 100, NoTTY)); !reflect.DeepEqual(got, []int{100, 101}) {
		t.Fatalf("members = %v", got)
	}
}

func TestMembersNeverIncludesThisProcessOrInit(t *testing.T) {
	self := os.Getpid()
	procs := []Process{
		{Pid: 1, Ppid: 0, Tty: 7},
		{Pid: self, Ppid: 1, Tty: 7},
		{Pid: 100, Ppid: self, Tty: 7},
	}
	if got := pids(Members(procs, self, 7)); !reflect.DeepEqual(got, []int{100}) {
		t.Fatalf("members = %v", got)
	}
}

func TestListAndFootprintSeeThisProcess(t *testing.T) {
	if !Supported() {
		t.Skip("process listing is not supported on this platform")
	}
	procs, err := List()
	if err != nil {
		t.Fatal(err)
	}
	self := os.Getpid()
	var found *Process
	for i := range procs {
		if procs[i].Pid == self {
			found = &procs[i]
		}
	}
	if found == nil {
		t.Fatalf("this process (%d) is not listed", self)
	}
	if found.Ppid != os.Getppid() || found.Name == "" || found.Start == "" {
		t.Fatalf("unexpected entry %+v", *found)
	}
	if bytes := Footprint(self); bytes < 1<<20 {
		t.Fatalf("footprint of this process = %d bytes", bytes)
	}
}

func TestUsageSortsByMemory(t *testing.T) {
	usage := usageOf([]Process{{Pid: 1, Name: "a"}, {Pid: 2, Name: "b"}, {Pid: 3, Name: "c"}}, func(pid int) uint64 {
		return map[int]uint64{1: 10, 2: 30, 3: 20}[pid]
	})
	if usage.Bytes != 60 || len(usage.Processes) != 3 || usage.Processes[0].Name != "b" || usage.Processes[2].Name != "a" {
		t.Fatalf("usage = %+v", usage)
	}
}
