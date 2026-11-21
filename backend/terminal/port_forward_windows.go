//go:build windows

package terminal

import (
	"errors"
	"syscall"

	"golang.org/x/sys/windows"
)

func isPortForwardAddressInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE) || errors.Is(err, windows.WSAEADDRINUSE)
}
