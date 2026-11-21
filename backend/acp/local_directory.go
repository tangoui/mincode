package acp

import (
	"errors"
	"os"
	"syscall"

	"mincode/app/backend/core"
)

func init() {
	core.Register("acp_check_local_directory", acpCheckLocalDirectoryHandler)
}

func checkLocalAgentDirectory(path string) (bool, error) {
	return checkLocalAgentDirectoryWithStat(path, os.Stat)
}

func checkLocalAgentDirectoryWithStat(path string, stat func(string) (os.FileInfo, error)) (bool, error) {
	info, err := stat(path)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.IsDir(), nil
}

func acpCheckLocalDirectoryHandler(_ string, argsJSON string) (string, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	available, err := checkLocalAgentDirectory(args.Path)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(struct {
		Available bool `json:"available"`
	}{available})
}
