//go:build !unix

package fs

import "os"

func openLocalThumbnailSource(path string) (*os.File, error) {
	return os.Open(path)
}
