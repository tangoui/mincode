package plugin

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestPluginShellUsesPluginEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell PATH")
	}
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("MINCODE_TEST_PRIVATE_VALUE", "must-not-leak")
	env := pluginProcessEnvironment("test.plugin", t.TempDir(), 2)
	out, err := runShellCommandEnvironment(context.Background(), `printf '%s\n' "$PATH" "${MINCODE_TEST_PRIVATE_VALUE-unset}" "$MINCODE_PLUGIN_ID"`, "", 5*time.Second, env)
	if err != nil || !out.Success {
		t.Fatalf("shell failed: %v %+v", err, out)
	}
	if !strings.Contains(out.Stdout, "/opt/homebrew/bin") || !strings.Contains(out.Stdout, "\nunset\ntest.plugin") {
		t.Fatalf("unexpected environment: %q", out.Stdout)
	}
}

// Windows per-user folders are paths, not secrets, and installers build their
// paths from them (cua-driver's install.ps1 failed with a null Path without
// LOCALAPPDATA); other variables stay private.
func TestPluginEnvironmentPassesWindowsUserFolders(t *testing.T) {
	t.Setenv("LOCALAPPDATA", `C:\Users\a\AppData\Local`)
	t.Setenv("APPDATA", `C:\Users\a\AppData\Roaming`)
	t.Setenv("MINCODE_TEST_PRIVATE_VALUE", "must-not-leak")
	env := strings.Join(pluginProcessEnvironment("test.plugin", t.TempDir(), 2), "\n")
	for _, want := range []string{`LOCALAPPDATA=C:\Users\a\AppData\Local`, `APPDATA=C:\Users\a\AppData\Roaming`} {
		if !strings.Contains(env, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(env, "must-not-leak") {
		t.Error("private variable leaked")
	}
}
