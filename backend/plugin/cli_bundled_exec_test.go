//go:build !windows

package plugin

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"testing"
)

func TestIsBundledCLIMode(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"--mincode-cli", "plugin"}, true},
		{[]string{"--mincode-cli"}, true},
		{[]string{"x", "--mincode-cli"}, false},
		{[]string{}, false},
		{nil, false},
	}
	for _, tc := range cases {
		if got := IsBundledCLIMode(tc.args); got != tc.want {
			t.Errorf("IsBundledCLIMode(%q) = %v, want %v", tc.args, got, tc.want)
		}
	}
}

// bundledCLITestLayout lays out a desktop binary and its bundled CLI the way
// this platform's bundledMincodeCLI looks for them, and returns both paths.
func bundledCLITestLayout(t *testing.T) (desktop, cli string) {
	t.Helper()
	root := t.TempDir()
	if runtime.GOOS == "darwin" {
		contents := filepath.Join(root, "MinCode.app", "Contents")
		desktop, cli = filepath.Join(contents, "MacOS", "mincode"), filepath.Join(contents, "Helpers", "mincode")
	} else {
		// The AppImage layout: AppRun runs usr/bin/mincode, the CLI sits in usr/bin/bin/.
		desktop, cli = filepath.Join(root, "usr", "bin", "mincode"), filepath.Join(root, "usr", "bin", "bin", "mincode")
	}
	writeExecutable(t, desktop)
	writeExecutable(t, cli)
	return desktop, cli
}

func stubBundledCLIExec(t *testing.T, desktop string) (calls *[][]string, envs *[][]string, dirs *[]string) {
	t.Helper()
	oldExecutable, oldExec := bundledCLIExecutable, execCLI
	t.Cleanup(func() { bundledCLIExecutable, execCLI = oldExecutable, oldExec })
	t.Setenv("APPDIR", "") // tests opt into AppImage variables explicitly
	t.Setenv("OWD", "")
	bundledCLIExecutable = func() string { return desktop }
	calls, envs, dirs = &[][]string{}, &[][]string{}, &[]string{}
	execCLI = func(argv0 string, argv []string, env []string) error {
		wd, _ := os.Getwd()
		*calls = append(*calls, append([]string{argv0}, argv...))
		*envs = append(*envs, env)
		*dirs = append(*dirs, wd)
		return errors.New("exec stubbed")
	}
	return calls, envs, dirs
}

func TestRunBundledCLIExecsBundledBinary(t *testing.T) {
	desktop, cli := bundledCLITestLayout(t)
	calls, _, _ := stubBundledCLIExec(t, desktop)

	// The stub returns an error, as a failed exec would; a real exec never returns.
	if code := RunBundledCLI([]string{"plugin", "templates"}); code != 1 {
		t.Fatalf("RunBundledCLI after a failed exec = %d, want 1", code)
	}
	if len(*calls) != 1 {
		t.Fatalf("exec calls = %d, want 1", len(*calls))
	}
	got := (*calls)[0]
	want := []string{cli, cli, "plugin", "templates"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("exec argv0+argv = %q, want %q", got, want)
	}
}

func TestRunBundledCLIWithoutBundledBinary(t *testing.T) {
	root := t.TempDir()
	desktop := writeExecutable(t, filepath.Join(root, "usr", "bin", "mincode"))
	calls, _, _ := stubBundledCLIExec(t, desktop)
	if code := RunBundledCLI([]string{"--version"}); code != 1 {
		t.Fatalf("RunBundledCLI without a bundled CLI = %d, want 1", code)
	}
	if len(*calls) != 0 {
		t.Fatalf("exec must not run without a bundled CLI, got %q", *calls)
	}
}

// AppRun changes to $APPDIR/usr/lib and points PATH and library paths into the
// mount; the CLI must see the user's directory and environment instead.
func TestRunBundledCLIRestoresAppImageCallerContext(t *testing.T) {
	desktop, _ := bundledCLITestLayout(t)
	calls, envs, dirs := stubBundledCLIExec(t, desktop)
	appDir := t.TempDir()
	caller := t.TempDir()
	t.Chdir(appDir)
	t.Setenv("APPDIR", appDir)
	t.Setenv("OWD", caller)
	t.Setenv("PATH", appDir+"/usr/bin:/usr/local/bin:/usr/bin")
	t.Setenv("LD_LIBRARY_PATH", appDir+"/usr/lib:"+appDir+"/usr/lib64:/opt/lib")
	t.Setenv("XDG_DATA_DIRS", appDir+"/usr/share:/usr/share")
	t.Setenv("GSETTINGS_SCHEMA_DIR", appDir+"/usr/share/glib-2.0/schemas")
	t.Setenv("WEBKIT_INJECTED_BUNDLE_PATH", appDir+"/usr/lib/webkitgtk-6.0/injected-bundle/")

	RunBundledCLI([]string{"plugin", "create", "demo"})
	if len(*calls) != 1 {
		t.Fatalf("exec calls = %d, want 1", len(*calls))
	}
	wantDir, _ := filepath.EvalSymlinks(caller)
	if gotDir, _ := filepath.EvalSymlinks((*dirs)[0]); gotDir != wantDir {
		t.Fatalf("CLI working directory = %q, want the caller's %q", gotDir, wantDir)
	}
	env := (*envs)[0]
	for _, want := range []string{"PATH=/usr/local/bin:/usr/bin", "LD_LIBRARY_PATH=/opt/lib", "XDG_DATA_DIRS=/usr/share", "OWD=" + caller} {
		if !slices.Contains(env, want) {
			t.Errorf("env lacks %q: %q", want, env)
		}
	}
	for _, entry := range env {
		for _, dropped := range []string{"GSETTINGS_SCHEMA_DIR=", "WEBKIT_INJECTED_BUNDLE_PATH="} {
			if len(entry) >= len(dropped) && entry[:len(dropped)] == dropped {
				t.Errorf("env keeps AppImage-only %q", entry)
			}
		}
	}
}

func TestBundledCLIEnvironment(t *testing.T) {
	env := []string{
		"HOME=/home/u",
		"APPDIR=/tmp/.mount_MinCodeX",
		"APPIMAGE=/home/u/Apps/MinCode.AppImage",
		"PATH=/tmp/.mount_MinCodeX/usr/bin:/home/u/.local/bin:/usr/bin",
		"LD_LIBRARY_PATH=/tmp/.mount_MinCodeX/usr/lib:/tmp/.mount_MinCodeX/usr/lib/x86_64-linux-gnu",
		"XDG_DATA_DIRS=/tmp/.mount_MinCodeX/usr/share:/usr/local/share:/usr/share",
		"GSETTINGS_SCHEMA_DIR=/tmp/.mount_MinCodeX/usr/share/glib-2.0/schemas",
		"WEBKIT_INJECTED_BUNDLE_PATH=/elsewhere/",
		"MINCODE_CLI=/opt/mincode",
	}
	got := bundledCLIEnvironment(env)
	want := []string{
		"HOME=/home/u",
		"APPDIR=/tmp/.mount_MinCodeX",
		"APPIMAGE=/home/u/Apps/MinCode.AppImage",
		"PATH=/home/u/.local/bin:/usr/bin",
		"XDG_DATA_DIRS=/usr/local/share:/usr/share",
		"WEBKIT_INJECTED_BUNDLE_PATH=/elsewhere/",
		"MINCODE_CLI=/opt/mincode",
		// A bare `mincode` must start the AppImage, not the binary in this mount.
		"MINCODE_DESKTOP=/home/u/Apps/MinCode.AppImage",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bundledCLIEnvironment =\n%q\nwant\n%q", got, want)
	}
	// A desktop path the user chose wins.
	chosen := append(slices.Clone(env), "MINCODE_DESKTOP=/opt/MinCode")
	if got := bundledCLIEnvironment(chosen); slices.Contains(got, "MINCODE_DESKTOP=/home/u/Apps/MinCode.AppImage") || !slices.Contains(got, "MINCODE_DESKTOP=/opt/MinCode") {
		t.Fatalf("user MINCODE_DESKTOP must be kept, got %q", got)
	}
	// Outside an AppImage nothing changes.
	plain := slices.DeleteFunc(slices.Clone(env), func(entry string) bool { return entry == "APPDIR=/tmp/.mount_MinCodeX" })
	if got := bundledCLIEnvironment(plain); !reflect.DeepEqual(got, plain) {
		t.Fatalf("without APPDIR the environment must be kept, got %q", got)
	}
	// A sibling directory sharing the prefix is not inside the mount.
	sibling := []string{"APPDIR=/tmp/.mount_MinCodeX", "PATH=/tmp/.mount_MinCodeXY/bin:/usr/bin"}
	if got := bundledCLIEnvironment(sibling); !reflect.DeepEqual(got, sibling) {
		t.Fatalf("prefix sibling must be kept, got %q", got)
	}
}
