// Package externalmcp provides rendezvous files and a stdio client for local Agent control.
package externalmcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"mincode/app/backend/core"
)

const flag = "--external-local-mcp"

type config struct {
	URL          string `json:"url,omitempty"`
	ControlURL   string `json:"controlUrl,omitempty"`
	ControlToken string `json:"controlToken,omitempty"`
}

var dataDir = core.UserDataDir

func IsHelper(args []string) bool { return len(args) == 1 && args[0] == flag }

func init() {
	core.Register("external_mcp_client_executable", clientExecutable)
}

func clientExecutable(_, _ string) (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	transient := runtime.GOOS == "darwin" &&
		(strings.HasPrefix(executable, "/Volumes/") || strings.Contains(executable, "/AppTranslocation/"))
	return core.EncodeResult(map[string]any{"executable": executable, "transient": transient})
}

func rendezvousPath() (string, error) {
	base := dataDir()
	if base == "" {
		return "", errors.New("app data directory unavailable")
	}
	release, err := lockPrivateParents(base)
	if err != nil {
		return "", err
	}
	defer release()
	parent := filepath.Join(base, core.AppDataNamespace)
	if err := os.Mkdir(parent, 0700); err != nil && !os.IsExist(err) {
		return "", err
	}
	if info, err := os.Lstat(parent); err != nil || info.Mode().Type() != fs.ModeDir {
		return "", errors.New("MCP app data path must be a real directory")
	}
	unlockParent, err := lockPrivateParents(parent)
	if err != nil {
		return "", err
	}
	defer unlockParent()
	private := filepath.Join(parent, "external-mcp")
	if err := makePrivateDir(private); err != nil {
		return "", err
	}
	return filepath.Join(private, "current.json"), nil
}

func lockRendezvousParents(path string) (func(), error) {
	dir := filepath.Dir(path)
	release, err := lockPrivateParents(dir)
	if err != nil {
		return nil, err
	}
	if err := verifyPrivatePath(dir, true); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

func SetControlRecord(url, token string) error {
	path, err := rendezvousPath()
	if err != nil {
		return err
	}
	return writeRecord(path, config{ControlURL: url, ControlToken: token})
}

// SetControlPresence publishes a token-free address for the CLI's liveness check.
func SetControlPresence(url string) error {
	path, err := rendezvousPath()
	if err != nil {
		return err
	}
	return writeRecord(filepath.Join(filepath.Dir(path), "running.json"), config{URL: url})
}

func writeRecord(path string, record config) error {
	release, err := lockRendezvousParents(path)
	if err != nil {
		return err
	}
	defer release()
	if err := verifyPrivatePath(path, false); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("unsafe MCP rendezvous record: %w", err)
	}
	if record == (config{}) {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	tmp, err := createPrivateTemp(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err = protectPrivateFile(tmp); err == nil {
		err = json.NewEncoder(tmp).Encode(record)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func RemoveStaleRecord() {
	if path, err := rendezvousPath(); err == nil {
		_ = writeRecord(path, config{})
	}
}
