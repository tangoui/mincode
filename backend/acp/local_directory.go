package acp

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"mincode/app/backend/core"
)

func init() {
	core.Register("acp_check_local_directory", acpCheckLocalDirectoryHandler)
	core.Register("acp_default_local_directory", acpDefaultLocalDirectoryHandler)
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

func ensureLocalAgentDefaultDirectory(dataDir string) (string, error) {
	if !filepath.IsAbs(dataDir) {
		return "", fmt.Errorf("invalid application data directory %q: expected an absolute path", dataDir)
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return "", fmt.Errorf("create application data directory: %w", err)
	}
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		return "", fmt.Errorf("open application data directory: %w", err)
	}
	defer root.Close()

	path := filepath.Join(core.AppDataNamespace, "agent-workspace")
	for _, name := range []string{core.AppDataNamespace, path} {
		if err := root.Mkdir(name, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("create default agent directory: %w", err)
		}
		info, err := root.Lstat(name)
		if err != nil {
			return "", fmt.Errorf("inspect default agent directory: %w", err)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("default agent directory %q must be a directory without a symbolic link", filepath.Join(dataDir, name))
		}
	}
	return filepath.Join(dataDir, path), nil
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

func acpDefaultLocalDirectoryHandler(_, _ string) (string, error) {
	path, err := ensureLocalAgentDefaultDirectory(core.UserDataDir())
	if err != nil {
		return "", err
	}
	return core.EncodeResult(struct {
		Path string `json:"path"`
	}{path})
}
