package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"mincode/app/plugins/api"
)

// Project opening is a navigation request, not a workspace-read grant.
// The host frame must confirm the directory before the existing project
// opening flow registers it. No files are read by this callback.
const pluginProjectOpenEvent = "plugin:project-open"

type projectOpenEvent struct {
	PluginID   string `json:"pluginId"`
	PluginName string `json:"pluginName"`
	Path       string `json:"path"`
}

func init() { registerHostMethod("host.project.open", nil, api.CapabilityUI, hostProjectOpen) }

func hostProjectOpen(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request struct {
		Path string `json:"path"`
	}
	if err := decodeHostUIParams(params, &request); err != nil {
		return nil, err
	}
	p := request.Path
	if p == "" || len(p) > 4096 || !utf8.ValidString(p) || strings.ContainsAny(p, "\x00\r\n") || !filepath.IsAbs(p) {
		return nil, hostUIParamsError("path must be an absolute directory path")
	}
	p = filepath.Clean(p)
	if p == filepath.VolumeName(p)+string(filepath.Separator) {
		return nil, hostUIParamsError("filesystem root cannot be opened as a project")
	}
	info, err := os.Lstat(p)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, hostUIParamsError("path must be an existing directory, not a symlink")
	}
	canonical, err := filepath.EvalSymlinks(p)
	if err != nil || canonical == filepath.VolumeName(canonical)+string(filepath.Separator) {
		return nil, hostUIParamsError("cannot resolve project directory")
	}
	if rpcErr := rateLimited(runtimeHost); rpcErr != nil {
		return nil, rpcErr
	}
	hostUIEmit(pluginProjectOpenEvent, projectOpenEvent{PluginID: runtimeHost.pluginID, PluginName: runtimeHost.displayName(), Path: canonical})
	return map[string]any{"requested": true}, nil
}
