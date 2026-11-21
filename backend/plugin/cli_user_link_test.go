package plugin

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAppImageWrapperScriptQuotesPath(t *testing.T) {
	got := string(appImageWrapperScript("/home/a b/it's.AppImage"))
	want := "#!/bin/sh\n" + appImageWrapperMarker + "\nexec '/home/a b/it'\\''s.AppImage' --mincode-cli \"$@\"\n"
	if got != want {
		t.Fatalf("wrapper =\n%q\nwant\n%q", got, want)
	}
}

func TestLinuxCLILinkPath(t *testing.T) {
	if got := linuxCLILinkPath("/home/u"); got != filepath.Join("/home/u", ".local", "bin", "mincode") {
		t.Fatalf("link path = %q", got)
	}
}

func TestInstallUserCLILinkSymlinkAndWrapper(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("user CLI links are a unix install path")
	}
	root := t.TempDir()
	cli := writeExecutable(t, filepath.Join(root, "app", "bin", "mincode"))
	link := filepath.Join(root, "home", ".local", "bin", "mincode")

	// Symlink mode: parent directory is created and the link points at the CLI.
	for range 2 { // second run must be idempotent
		if err := installUserCLILink(link, cli, "", false); err != nil {
			t.Fatalf("install symlink: %v", err)
		}
		info, err := os.Lstat(link)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("link is not a symlink: %v %v", info, err)
		}
		if !sameFile(link, cli) || !isOwnedUserCLILink(link, cli) {
			t.Fatal("symlink does not point at the CLI")
		}
	}

	// AppImage mode replaces our own symlink with a 0755 wrapper script.
	appImage := writeExecutable(t, filepath.Join(root, "Apps", "MinCode.AppImage"))
	for range 2 {
		if err := installUserCLILink(link, cli, appImage, false); err != nil {
			t.Fatalf("install wrapper: %v", err)
		}
		info, err := os.Lstat(link)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o755 {
			t.Fatalf("wrapper mode = %v, %v", info, err)
		}
		content, err := os.ReadFile(link)
		if err != nil || !bytes.Equal(content, appImageWrapperScript(appImage)) {
			t.Fatalf("wrapper content = %q, %v", content, err)
		}
		if !isOwnedUserCLILink(link, cli) {
			t.Fatal("wrapper not recognised as owned")
		}
	}

	// Someone else's file is never replaced without replace=true and never uninstalled.
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(link, []byte("#!/bin/sh\necho other\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if isOwnedUserCLILink(link, cli) {
		t.Fatal("foreign script reported as owned")
	}
	err := installUserCLILink(link, cli, "", false)
	if err == nil || !strings.Contains(err.Error(), "已存在且不是 MinCode 的命令") {
		t.Fatalf("foreign file install error = %v", err)
	}
	if err := uninstallUserCLILink(link, cli); err == nil {
		t.Fatal("uninstall removed a foreign file")
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("foreign file was removed: %v", err)
	}
	if err := installUserCLILink(link, cli, "", true); err != nil {
		t.Fatalf("replace install: %v", err)
	}
	if !sameFile(link, cli) {
		t.Fatal("replace did not link the CLI")
	}

	// Uninstall removes owned links and tolerates a missing one.
	if err := uninstallUserCLILink(link, cli); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatalf("link still present: %v", err)
	}
	if err := uninstallUserCLILink(link, cli); err != nil {
		t.Fatalf("uninstall missing: %v", err)
	}

	// A foreign symlink is a conflict as well.
	other := writeExecutable(t, filepath.Join(root, "other", "mincode"))
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	if isOwnedUserCLILink(link, cli) {
		t.Fatal("foreign symlink reported as owned")
	}
	if err := installUserCLILink(link, cli, "", false); err == nil {
		t.Fatal("foreign symlink replaced without replace")
	}
}

func TestPathListContains(t *testing.T) {
	cases := []struct {
		pathEnv, dir, goos string
		want               bool
	}{
		{"/usr/bin:/home/u/.local/bin/", "/home/u/.local/bin", "linux", true},
		{`C:\A;c:\program files\mincode\bin\`, `C:\Program Files\MinCode\bin`, "windows", true},
		{"", "/x", "linux", false},
		{"/usr/bin:/Home/u/.local/bin", "/home/u/.local/bin", "linux", false},
		{`C:\A;"C:\Program Files\MinCode\bin"`, `C:\Program Files\MinCode\bin`, "windows", true},
		{`C:\A`, `C:\B`, "windows", false},
	}
	for _, tc := range cases {
		if got := pathListContains(tc.pathEnv, tc.dir, tc.goos); got != tc.want {
			t.Errorf("pathListContains(%q, %q, %q) = %v, want %v", tc.pathEnv, tc.dir, tc.goos, got, tc.want)
		}
	}
}

func TestWindowsPathWithDir(t *testing.T) {
	const dir = `D:\M\bin`
	if got, changed := windowsPathWithDir(`C:\A`, dir); got != `C:\A;D:\M\bin` || !changed {
		t.Fatalf("append = %q, %v", got, changed)
	}
	if got, changed := windowsPathWithDir("", dir); got != dir || !changed {
		t.Fatalf("empty = %q, %v", got, changed)
	}
	if got, changed := windowsPathWithDir(`C:\A;`, dir); got != `C:\A;D:\M\bin` || !changed {
		t.Fatalf("trailing separator = %q, %v", got, changed)
	}
	if got, changed := windowsPathWithDir(`C:\A;d:\m\bin\`, dir); got != `C:\A;d:\m\bin\` || changed {
		t.Fatalf("already present = %q, %v", got, changed)
	}
	if got, changed := windowsPathWithoutDir(`C:\A;D:\M\bin;C:\B`, `d:\m\bin\`); got != `C:\A;C:\B` || !changed {
		t.Fatalf("remove = %q, %v", got, changed)
	}
	if got, changed := windowsPathWithoutDir(`C:\A;C:\B`, dir); got != `C:\A;C:\B` || changed {
		t.Fatalf("remove absent = %q, %v", got, changed)
	}
	if got, changed := windowsPathWithoutDir(`C:\A;D:\M\bin\sub;C:\B`, dir); got != `C:\A;D:\M\bin\sub;C:\B` || changed {
		t.Fatalf("remove must only drop the exact dir = %q, %v", got, changed)
	}
}

func TestUserCLILinkState(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("user CLI links are a unix install path")
	}
	root := t.TempDir()
	cli := writeExecutable(t, filepath.Join(root, "app", "bin", "mincode"))
	appImage := writeExecutable(t, filepath.Join(root, "MinCode.AppImage"))
	link := filepath.Join(root, "home", ".local", "bin", "mincode")
	expect := func(appImage, wantState, wantTarget string) {
		t.Helper()
		if state, target := userCLILinkState(link, cli, appImage); state != wantState || target != wantTarget {
			t.Fatalf("state = %q/%q, want %q/%q", state, target, wantState, wantTarget)
		}
	}

	expect("", "missing", "")
	if err := installUserCLILink(link, cli, "", false); err != nil {
		t.Fatal(err)
	}
	expect("", "installed", "")
	// Running as an AppImage now: our symlink is repaired into a wrapper without asking.
	expect(appImage, "missing", "")
	if err := installUserCLILink(link, cli, appImage, false); err != nil {
		t.Fatal(err)
	}
	expect(appImage, "installed", "")
	// A wrapper for a moved AppImage is still ours.
	expect(filepath.Join(root, "Moved.AppImage"), "missing", "")
	expect("", "missing", "")

	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(root, "other", "mincode")
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	expect("", "conflict", other)
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(link, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	expect(appImage, "conflict", link)
}
