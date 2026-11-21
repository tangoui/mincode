//go:build !windows

package acp

import "os"

func replaceFile(source, target string) error {
	return os.Rename(source, target)
}
