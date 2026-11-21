//go:build !windows

package plugin

import (
	"os"
	"syscall"
)

func publishReadFlags() int { return os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK }
