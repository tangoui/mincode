package fs

import (
	"errors"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"mincode/app/backend/core"
)

type localPathArgs struct {
	Path   string `json:"path"`
	Reveal bool   `json:"reveal"`
}

type localPathInfo struct {
	Exists     bool       `json:"exists"`
	IsFile     bool       `json:"isFile"`
	IsDir      bool       `json:"isDir"`
	Size       int64      `json:"size"`
	ModifiedAt *time.Time `json:"modifiedAt"`
	Mime       string     `json:"mime"`
	Path       string     `json:"path,omitempty"`
}

func init() {
	core.Register("local_path_info", handleLocalPathInfo)
	core.Register("local_path_open", handleLocalPathOpen)
	core.RegisterCtx("local_image_thumbnail", handleLocalImageThumbnail)
}

func inspectLocalPath(path string) (string, os.FileInfo, error) {
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) || !isLocalVolumePath(path) {
		return "", nil, errors.New("expected an absolute local path")
	}
	canonical, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", nil, err
	}
	if !isLocalVolumePath(canonical) {
		return "", nil, errors.New("expected an absolute local path")
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", nil, err
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return "", nil, errors.New("expected a regular file or directory")
	}
	return canonical, info, nil
}

func isLocalVolumePath(path string) bool {
	if runtime.GOOS != "windows" {
		return true
	}
	// Reject UNC/device namespaces, alternate data streams and DOS device names.
	volume := filepath.VolumeName(path)
	if len(volume) != 2 || volume[1] != ':' || strings.ContainsRune(path[2:], ':') {
		return false
	}
	for _, part := range strings.FieldsFunc(path[2:], func(r rune) bool { return r == '/' || r == '\\' }) {
		if part != ".." && !filepath.IsLocal(part) {
			return false
		}
	}
	return true
}

func handleLocalPathInfo(_, argsJSON string) (string, error) {
	var args localPathArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	path, info, err := inspectLocalPath(args.Path)
	if os.IsNotExist(err) {
		return core.EncodeResult(localPathInfo{})
	}
	if err != nil {
		return "", err
	}
	modified := info.ModTime()
	result := localPathInfo{Exists: true, IsDir: info.IsDir(), IsFile: info.Mode().IsRegular(), ModifiedAt: &modified, Path: path}
	if result.IsFile {
		result.Size = info.Size()
		result.Mime = mime.TypeByExtension(filepath.Ext(path))
		if result.Mime == "" {
			result.Mime = "application/octet-stream"
		}
	}
	return core.EncodeResult(result)
}

func handleLocalPathOpen(_, argsJSON string) (string, error) {
	var args localPathArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	path, _, err := inspectLocalPath(args.Path)
	if err != nil {
		return "", err
	}
	if args.Reveal {
		if err := rejectSensitiveLocateDir(path); err != nil {
			return "", err
		}
	}
	name, argv := localPathOpenCommand(runtime.GOOS, path, args.Reveal)
	cmd, cancel := core.CommandWithTimeout(core.ShortCommandTimeout, name, argv...)
	defer cancel()
	if err := cmd.Run(); err != nil {
		// Explorer can return 1 after handing off successfully to an existing instance.
		if runtime.GOOS != "windows" || cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != 1 {
			return "", fmt.Errorf("open local path: %w", err)
		}
	}
	return "", nil
}

func localPathOpenCommand(goos, path string, reveal bool) (string, []string) {
	switch goos {
	case "darwin":
		if reveal {
			return "open", []string{"-R", path}
		}
		return "open", []string{path}
	case "windows":
		if reveal {
			return "explorer.exe", []string{"/select," + path}
		}
		return "explorer.exe", []string{path}
	default:
		if reveal {
			path = filepath.Dir(path)
		}
		return "xdg-open", []string{path}
	}
}
