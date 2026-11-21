package plugin

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

// The debuggee JVM is not the adapter (which lives inside jdtls). Bind its
// configured name, resolved target and bytes without putting it in PluginFiles.
// Other DAP arguments intentionally do not change the executable's identity.
type jdtlsJavaExecIdentity struct {
	Path   string `json:"path"`
	Target string `json:"target"`
	SHA256 string `json:"sha256"`
}

func resolveJdtlsJavaExec(arguments json.RawMessage) (*jdtlsJavaExecIdentity, error) {
	if len(arguments) == 0 {
		return nil, nil
	}
	var args map[string]json.RawMessage
	if err := json.Unmarshal(arguments, &args); err != nil {
		return nil, newContributionError(contributionErrInvalidResult, "debugger arguments must be an object")
	}
	var path string
	if raw, ok := args["javaExec"]; ok {
		if err := json.Unmarshal(raw, &path); err != nil {
			return nil, newContributionError(contributionErrInvalidResult, "javaExec must be a string")
		}
	}
	if path == "" {
		return nil, nil
	}
	if !filepath.IsAbs(path) {
		return nil, processLaunchError(processLaunchErrForbidden, "javaExec must be an absolute executable path")
	}
	if filepath.Clean(path) != path {
		return nil, processLaunchError(processLaunchErrForbidden, "javaExec must be a normalized absolute path")
	}
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, processLaunchError(processLaunchErrNotFound, "javaExec is unavailable: %v", err)
	}
	if _, _, err = resolveProcessLaunchCommand("", target, true); err != nil {
		return nil, err
	}
	maxBytes := int64(debuggerSpec.Limit("pluginExecutableHashMaxBytes"))
	// The shared file checks word their errors for plugin files; this file is
	// the user's JDK, so keep the code and name it javaExec instead.
	relabel := func(err error) error {
		code := processLaunchErrorCode(err)
		if code == "" {
			return err
		}
		detail := strings.TrimSpace(strings.TrimPrefix(err.Error(), code+":"))
		detail = strings.Replace(detail, "plugin file", "javaExec", 1)
		detail = strings.Replace(detail, "; review the launch again", "", 1)
		return processLaunchError(code, "%s", detail)
	}
	// The shared cache is only a starting point. Always verify against the open
	// file without that cache; one retry refreshes a stale size/mtime cache entry.
	for attempt := 0; attempt < 2; attempt++ {
		hash, hashErr := processLaunchFileHash(target, maxBytes)
		if hashErr != nil {
			return nil, relabel(hashErr)
		}
		identity, verifyErr := verifyProcessLaunchFile(target, hash, maxBytes)
		if verifyErr != nil {
			if attempt == 0 && processLaunchErrorCode(verifyErr) == processLaunchErrChanged {
				continue
			}
			return nil, relabel(verifyErr)
		}
		if !processLaunchExecutableModeOK(identity.info) {
			return nil, processLaunchError(processLaunchErrForbidden, "javaExec is not executable")
		}
		currentTarget, targetErr := filepath.EvalSymlinks(path)
		if targetErr != nil || currentTarget != target {
			return nil, processLaunchError(processLaunchErrChanged, "javaExec target changed while it was checked")
		}
		return &jdtlsJavaExecIdentity{Path: path, Target: target, SHA256: hash}, nil
	}
	return nil, processLaunchError(processLaunchErrChanged, "javaExec changed while it was checked")
}
