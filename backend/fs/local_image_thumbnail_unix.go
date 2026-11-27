//go:build unix

package fs

import (
	"os"
	"syscall"
)

func openLocalThumbnailSource(path string) (*os.File, error) {
	// If a FIFO replaces the image between stat and open, opening it must not block.
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
}
