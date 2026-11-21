//go:build windows

package misc

import (
	"syscall"
	"unsafe"
)

var (
	user32                         = syscall.NewLazyDLL("user32.dll")
	procGetClipboardSequenceNumber = user32.NewProc("GetClipboardSequenceNumber")
	procRegisterClipboardFormatW   = user32.NewProc("RegisterClipboardFormatW")
	procIsClipboardFormatAvailable = user32.NewProc("IsClipboardFormatAvailable")
)

// Formats clipboard managers must skip (password managers set them).
var concealedClipboardFormats = []string{"ExcludeClipboardContentFromMonitorProcessing", "CanIncludeInClipboardHistory"}

func clipboardState() (int64, bool, error) {
	sequence, _, _ := procGetClipboardSequenceNumber.Call()
	concealed := false
	for _, name := range concealedClipboardFormats {
		pointer, err := syscall.UTF16PtrFromString(name)
		if err != nil {
			continue
		}
		format, _, _ := procRegisterClipboardFormatW.Call(uintptr(unsafe.Pointer(pointer)))
		if format == 0 {
			continue
		}
		if available, _, _ := procIsClipboardFormatAvailable.Call(format); available != 0 {
			concealed = true
			break
		}
	}
	return int64(sequence), concealed, nil
}
