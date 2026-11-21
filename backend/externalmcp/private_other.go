//go:build !windows

package externalmcp

import (
	"errors"
	"os"
)

func lockPrivateParents(path string) (func(), error) { return func() {}, nil }

func makePrivateDir(path string) error {
	if err := os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	return verifyPrivatePath(path, true)
}

func verifyPrivatePath(path string, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0077 != 0 || (directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) {
		return errors.New("unsafe MCP private path")
	}
	return nil
}

func createPrivateTemp(directory string) (*os.File, error) {
	return os.CreateTemp(directory, ".record-*")
}

func protectPrivateFile(file *os.File) error { return file.Chmod(0600) }

func readPrivateRecord(path string) ([]byte, error) {
	if err := verifyPrivatePath(path, false); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil || info.Size() > 1024 {
		return nil, errors.New("unsafe MCP rendezvous record")
	}
	return os.ReadFile(path)
}
