//go:build windows

package lsp

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	installOutputKernel32        = windows.NewLazySystemDLL("kernel32.dll")
	installOutputConsoleCP       = installOutputKernel32.NewProc("GetConsoleOutputCP")
	installOutputOEMCP           = installOutputKernel32.NewProc("GetOEMCP")
	installOutputMultiByteToWide = installOutputKernel32.NewProc("MultiByteToWideChar")
)

func decodeInstallOutput(output []byte) string {
	if utf8.Valid(output) {
		return string(output)
	}
	codePage, _, _ := installOutputConsoleCP.Call()
	if codePage == 0 {
		codePage, _, _ = installOutputOEMCP.Call()
	}
	return decodeInstallOutputCodePage(output, uint32(codePage))
}

func decodeInstallOutputCodePage(output []byte, codePage uint32) string {
	if utf8.Valid(output) {
		return string(output)
	}
	// 显式传入长度，保留输出中可能包含的 NUL；flags=0 允许替换非法字节。
	size, _, _ := installOutputMultiByteToWide.Call(
		uintptr(codePage), 0, uintptr(unsafe.Pointer(&output[0])), uintptr(len(output)), 0, 0,
	)
	if size == 0 {
		return strings.ToValidUTF8(string(output), "\uFFFD")
	}
	wide := make([]uint16, int(size))
	written, _, _ := installOutputMultiByteToWide.Call(
		uintptr(codePage), 0, uintptr(unsafe.Pointer(&output[0])), uintptr(len(output)),
		uintptr(unsafe.Pointer(&wide[0])), size,
	)
	if written == 0 {
		return strings.ToValidUTF8(string(output), "\uFFFD")
	}
	return string(utf16.Decode(wide[:int(written)]))
}
