package main

import (
	"runtime"
	"testing"

	"mincode/app/backend/core"
)

func TestUnbundledMacAppUsesIsolatedName(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS WebKit process-name fallback")
	}
	t.Setenv("MINCODE_APP_NAME", "")
	t.Setenv("MINCODE_APP_ID", "")
	name, id := appIdentity()
	if name != core.AppDataNamespace || id != core.AppID {
		t.Fatalf("identity = %q, %q; want %q, %q", name, id, core.AppDataNamespace, core.AppID)
	}
}

func TestAppIdentityUsesIsolatedIDWithDataOrNameOverride(t *testing.T) {
	t.Setenv("MINCODE_APP_ID", "")
	t.Setenv("MINCODE_APP_NAME", "MinCodeE2E")
	t.Setenv("MINCODE_DATA_DIR", "")
	_, nameID := appIdentity()
	t.Setenv("MINCODE_APP_NAME", "")
	t.Setenv("MINCODE_DATA_DIR", t.TempDir())
	_, dataID := appIdentity()
	if nameID == core.AppID || dataID == core.AppID || nameID == dataID {
		t.Fatalf("overrides must isolate native identity: name ID %q, data ID %q", nameID, dataID)
	}
}
