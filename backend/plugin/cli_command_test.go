package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

func writeExecutable(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestBundledMincodeCLILayouts(t *testing.T) {
	root := t.TempDir()

	app := filepath.Join(root, "MinCode.app", "Contents")
	desktop := writeExecutable(t, filepath.Join(app, "MacOS", "mincode"))
	if got := bundledMincodeCLI(desktop, "darwin"); got != "" {
		t.Fatalf("missing helper must not resolve, got %q", got)
	}
	helper := writeExecutable(t, filepath.Join(app, "Helpers", "mincode"))
	if got := bundledMincodeCLI(desktop, "darwin"); got != helper {
		t.Fatalf("darwin CLI = %q, want %q", got, helper)
	}

	install := filepath.Join(root, "linux")
	linuxDesktop := writeExecutable(t, filepath.Join(install, "mincode"))
	linuxCLI := writeExecutable(t, filepath.Join(install, "bin", "mincode"))
	if got := bundledMincodeCLI(linuxDesktop, "linux"); got != linuxCLI {
		t.Fatalf("linux CLI = %q, want %q", got, linuxCLI)
	}

	linuxInstalledDesktop := writeExecutable(t, filepath.Join(root, "usr", "bin", "MinCode"))
	linuxInstalledCLI := writeExecutable(t, filepath.Join(root, "usr", "lib", "MinCode", "mincode"))
	if got := bundledMincodeCLI(linuxInstalledDesktop, "linux"); got != linuxInstalledCLI {
		t.Fatalf("installed linux CLI = %q, want %q", got, linuxInstalledCLI)
	}

	windows := filepath.Join(root, "windows")
	windowsDesktop := writeExecutable(t, filepath.Join(windows, "mincode.exe"))
	windowsCLI := writeExecutable(t, filepath.Join(windows, "bin", "mincode.exe"))
	if got := bundledMincodeCLI(windowsDesktop, "windows"); got != windowsCLI {
		t.Fatalf("windows CLI = %q, want %q", got, windowsCLI)
	}

	dev := filepath.Join(root, "dev", "bin")
	devDesktop := writeExecutable(t, filepath.Join(dev, "mincode-dev.app", "Contents", "MacOS", "mincode"))
	devCLI := writeExecutable(t, filepath.Join(dev, "cli", "mincode"))
	if got := bundledMincodeCLI(devDesktop, "darwin"); got != devCLI {
		t.Fatalf("dev CLI = %q, want %q", got, devCLI)
	}
}

func TestBundledMincodeCLINeverReturnsDesktopItself(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	desktop := writeExecutable(t, filepath.Join(dir, "bin", "mincode"))
	// A desktop binary running from a directory that has its own bin/ must not pick itself.
	if got := bundledMincodeCLI(filepath.Join(dir, "mincode"), "linux"); got != desktop {
		t.Fatalf("got %q", got)
	}
	if got := bundledMincodeCLI(desktop, "linux"); got != "" {
		t.Fatalf("desktop resolved as its own CLI: %q", got)
	}
}

func TestCLILinkState(t *testing.T) {
	dir := t.TempDir()
	cli := writeExecutable(t, filepath.Join(dir, "MinCode.app", "Contents", "Helpers", "mincode"))
	link := filepath.Join(dir, "bin", "mincode")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if state, _ := cliLinkState(link, cli); state != "missing" {
		t.Fatalf("state = %s", state)
	}
	if err := os.Symlink(cli, link); err != nil {
		t.Fatal(err)
	}
	if state, _ := cliLinkState(link, cli); state != "installed" {
		t.Fatalf("state = %s", state)
	}

	other := writeExecutable(t, filepath.Join(dir, "other", "mincode"))
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	if state, target := cliLinkState(link, cli); state != "conflict" || target != other {
		t.Fatalf("state = %s target = %s", state, target)
	}
	if isOwnedCLILink(link, cli) {
		t.Fatal("foreign symlink treated as owned")
	}

	stale := filepath.Join(dir, "Old", "MinCode.app", "Contents", "Helpers", "mincode")
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(stale, link); err != nil {
		t.Fatal(err)
	}
	if !isOwnedCLILink(link, cli) {
		t.Fatal("link to a moved MinCode app must be replaceable without confirmation")
	}

	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, link)
	if state, _ := cliLinkState(link, cli); state != "conflict" {
		t.Fatalf("regular file state = %s", state)
	}
	if isOwnedCLILink(link, cli) {
		t.Fatal("regular file treated as owned")
	}
}
