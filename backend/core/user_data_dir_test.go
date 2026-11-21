package core

import (
	"path/filepath"
	"testing"
)

// MINCODE_DATA_DIR redirects every backend package's data to one directory, so
// another host process (the Viva prototype, a test) never touches the user's
// real MinCode data. Unset, the platform directory is unchanged.
func TestUserDataDirOverride(t *testing.T) {
	t.Setenv(userDataDirEnv, "")
	platform := UserDataDir()
	if platform == "" {
		t.Skip("no platform data directory on this machine")
	}

	override := t.TempDir()
	t.Setenv(userDataDirEnv, override)
	if got := UserDataDir(); got != override {
		t.Fatalf("UserDataDir() = %q, want the override %q", got, override)
	}

	// A relative value is ignored rather than resolved against an unknown working directory.
	t.Setenv(userDataDirEnv, filepath.Join("relative", "dir"))
	if got := UserDataDir(); got != platform {
		t.Fatalf("relative override: UserDataDir() = %q, want the platform directory %q", got, platform)
	}
}
