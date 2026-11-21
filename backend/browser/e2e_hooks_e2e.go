//go:build e2e && darwin && cgo

package browser

// Desktop smoke hooks for the native panels (tools/e2e/desktop-smoke.mjs).
// This file exists only in `-tags e2e` builds: its cgo CFLAGS define
// MINCODE_BROWSER_E2E for the whole package, which is the only way
// overlay_darwin.m compiles the automatic file pick and save-panel answer.
// Production builds have neither the flag nor the C code, so every page
// download still goes through a user-confirmed NSSavePanel.
//
// The hooks also stay off unless the harness launched this process
// (MINCODE_E2E_CONFIG and MINCODE_E2E_REPORT set, as e2esmoke.Enabled checks)
// and named an absolute fixture directory / file.

/*
#cgo darwin CFLAGS: -DMINCODE_BROWSER_E2E
void mincode_browser_e2e_configure(const char *saveDirectory, const char *uploadFile);
*/
import "C"

import (
	"os"
	"path/filepath"
)

const (
	e2eSaveDirectoryEnv = "MINCODE_E2E_BROWSER_SAVE_DIR"
	e2eUploadFileEnv    = "MINCODE_E2E_BROWSER_UPLOAD_FILE"
)

func e2eNativePanelHooksCompiled() bool { return true }

func init() {
	if os.Getenv("MINCODE_E2E_CONFIG") == "" || os.Getenv("MINCODE_E2E_REPORT") == "" {
		return
	}
	saveDirectory := e2eAbsolutePath(os.Getenv(e2eSaveDirectoryEnv), true)
	uploadFile := e2eAbsolutePath(os.Getenv(e2eUploadFileEnv), false)
	if saveDirectory == "" && uploadFile == "" {
		return
	}
	cSave, freeSave := cOptional(saveDirectory)
	defer freeSave()
	cUpload, freeUpload := cOptional(uploadFile)
	defer freeUpload()
	C.mincode_browser_e2e_configure(cSave, cUpload)
}

// e2eAbsolutePath returns path when it is absolute and names an existing
// directory (wantDirectory) or regular file; otherwise "".
func e2eAbsolutePath(path string, wantDirectory bool) string {
	if path == "" || !filepath.IsAbs(path) {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() != wantDirectory || (!wantDirectory && !info.Mode().IsRegular()) {
		return ""
	}
	return filepath.Clean(path)
}
