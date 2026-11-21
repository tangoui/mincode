//go:build !windows

package terminal

import (
	"errors"
	"syscall"
)

func isPortForwardAddressInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE)
}
