package terminal

import "testing"

func TestParseSSHAdminSnapshot(t *testing.T) {
	snapshot := parseSSHAdminSnapshot("hostname=prod-1\nos=Ubuntu 24.04\nkernel=6.8.0\narch=x86_64\nuptime=86461\nload1=0.10\nload5=0.20\nload15=0.30\nmem_total=1000\nmem_used=400\ndisk_total=2000\ndisk_used=500\n")
	if snapshot.Hostname != "prod-1" || snapshot.OperatingSystem != "Ubuntu 24.04" {
		t.Fatalf("unexpected identity: %#v", snapshot)
	}
	if snapshot.UptimeSeconds != 86461 || snapshot.MemoryUsedBytes != 400 || snapshot.Load15 != 0.30 {
		t.Fatalf("unexpected metrics: %#v", snapshot)
	}
}

func TestParseSSHAdminProcessesSkipsMalformedRows(t *testing.T) {
	processes := parseSSHAdminProcesses(" 42 1 root 12.5 3.1 S nginx\nmalformed\n")
	if len(processes) != 1 {
		t.Fatalf("expected one process, got %#v", processes)
	}
	if processes[0].PID != 42 || processes[0].Command != "nginx" || processes[0].CPU != 12.5 {
		t.Fatalf("unexpected process: %#v", processes[0])
	}
}

func TestParseSSHAdminServicesRequiresValidatedUnit(t *testing.T) {
	services := parseSSHAdminServices("sshd.service loaded active running OpenSSH daemon\n● failed.service loaded failed failed Failed unit\n../../bad loaded active running Bad\n")
	if len(services) != 2 || services[0].Unit != "failed.service" || services[1].Unit != "sshd.service" || services[1].Description != "OpenSSH daemon" {
		t.Fatalf("unexpected services: %#v", services)
	}
}

func TestSSHAdminIdentifierValidation(t *testing.T) {
	if !adminUnitPattern.MatchString("postgresql@16-main.service") {
		t.Fatal("valid systemd unit rejected")
	}
	if adminUnitPattern.MatchString("../../evil.service") {
		t.Fatal("unsafe systemd unit accepted")
	}
	if adminUnitPattern.MatchString("-unsafe.service") {
		t.Fatal("option-like systemd unit accepted")
	}
	if !adminContainerPattern.MatchString("web-api_1") {
		t.Fatal("valid container name rejected")
	}
	if adminContainerPattern.MatchString("web;rm -rf /") {
		t.Fatal("unsafe container name accepted")
	}
}
