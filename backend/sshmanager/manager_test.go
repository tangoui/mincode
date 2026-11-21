package sshmanager

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseSSHConfigIncludesAndSkipsWildcardHosts(t *testing.T) {
	dir := t.TempDir()
	included := filepath.Join(dir, "included.conf")
	if err := os.WriteFile(included, []byte("Host app\n  HostName 10.0.0.4\n  User deploy\n  Port 2222\n  IdentityFile ~/.ssh/id_app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(dir, "config")
	if err := os.WriteFile(main, []byte("Include included.conf\nHost *.internal\n User ignored\nHost db\n HostName db.internal\n User admin\n ServerAliveInterval 20\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	profiles, warnings, err := parseSSHConfig(main, map[string]bool{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if len(profiles) != 2 {
		t.Fatalf("got %d profiles, want 2: %#v", len(profiles), profiles)
	}
	if profiles[0].Alias != "app" || profiles[0].Port != 2222 || profiles[0].Username != "deploy" {
		t.Fatalf("unexpected included profile: %#v", profiles[0])
	}
	if profiles[1].Alias != "db" || profiles[1].ServerAliveInterval != 20 {
		t.Fatalf("unexpected main profile: %#v", profiles[1])
	}
}

func TestParseSSHConfigDetectsIncludeLoop(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	if err := os.WriteFile(a, []byte("Include b\nHost a\n User root\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("Include a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	profiles, warnings, err := parseSSHConfig(a, map[string]bool{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 1 || len(warnings) == 0 {
		t.Fatalf("profiles=%#v warnings=%#v", profiles, warnings)
	}
}

func TestParseSSHConfigAppliesFirstMatchAndHostDefaults(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "config")
	content := `Host app
  HostName app.internal
  User deploy
Host *
  User fallback
  Port 2200
  IdentityFile "keys/default key"
`
	if err := os.WriteFile(config, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	profiles, warnings, err := parseSSHConfig(config, map[string]bool{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if len(profiles) != 1 {
		t.Fatalf("got %d profiles, want 1: %#v", len(profiles), profiles)
	}
	profile := profiles[0]
	if profile.Username != "deploy" || profile.Port != 2200 || profile.Host != "app.internal" {
		t.Fatalf("unexpected merged profile: %#v", profile)
	}
	if profile.IdentityFile != filepath.Join(dir, "keys", "default key") {
		t.Fatalf("unexpected identity file: %q", profile.IdentityFile)
	}
}

func TestParseSSHConfigUsesGlobalValuesAndNegatedPatterns(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "config")
	content := `User global-user
Host app blocked
  HostName %h.internal
Host * !blocked
  ServerAliveInterval 12
`
	if err := os.WriteFile(config, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	profiles, warnings, err := parseSSHConfig(config, map[string]bool{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if len(profiles) != 2 {
		t.Fatalf("got %d profiles, want 2: %#v", len(profiles), profiles)
	}
	if profiles[0].Username != "global-user" || profiles[0].Host != "app.internal" || profiles[0].ServerAliveInterval != 12 {
		t.Fatalf("unexpected app profile: %#v", profiles[0])
	}
	if profiles[1].Alias != "blocked" || profiles[1].ServerAliveInterval != 0 {
		t.Fatalf("negated Host pattern was not respected: %#v", profiles[1])
	}
}

func TestParseSSHConfigDefaultsMissingUser(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "config")
	if err := os.WriteFile(config, []byte("Host app\n  HostName app.internal\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	profiles, _, err := parseSSHConfig(config, map[string]bool{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 1 || profiles[0].Username == "" {
		t.Fatalf("missing local username default: %#v", profiles)
	}
}

func TestSSHConfigPathStaysInsideSSHDirectory(t *testing.T) {
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	allowed := filepath.Join(sshDir, "config.d", "work")

	if _, err := sshConfigPathWithinDirectory(allowed, home); err != nil {
		t.Fatalf("expected SSH config path to be allowed: %v", err)
	}
	if _, err := sshConfigPathWithinDirectory(filepath.Join(home, ".env"), home); err == nil {
		t.Fatal("expected path outside ~/.ssh to be rejected")
	}
}
