//go:build !windows

package plugin

import (
	"path/filepath"
	"syscall"
	"testing"
)

func TestCustomEditorDocumentRejectsNonRegularFiles(t *testing.T) {
	fixture := newCustomEditorFixture(t, customEditorAccessRead)
	if err := syscall.Mkfifo(filepath.Join(fixture.project, "pipe.drawio"), 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	_, err := fixture.begin("pipe.drawio")
	expectCustomEditorCode(t, err, customEditorErrForbidden)
}
