package fs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Exported, symlink-safe project path resolution for other backend packages
// (for example the plugin host's custom-editor document bridge). It wraps the
// same canonical-root and resolve-under-root helpers the local_fs commands use,
// so every caller shares one containment rule.

// ErrProjectPathEscapesRoot reports a path (or a symlink target) outside the
// canonical project root.
var ErrProjectPathEscapesRoot = errors.New("path_escapes_root")

// ErrProjectPathInvalid reports a relative path that is empty, absolute, holds
// a NUL byte or a ".." segment.
var ErrProjectPathInvalid = errors.New("path_invalid")

// ProjectFile is an existing entry resolved under a canonical project root.
type ProjectFile struct {
	// Root is the canonical (symlink-free, absolute) project root.
	Root string
	// RelativePath is the requested path, cleaned and slash-separated.
	RelativePath string
	// Path is the canonical absolute path after following symlinks; it always
	// lies inside Root.
	Path string
	// ResolvedRelativePath is Path relative to Root, slash-separated. It differs
	// from RelativePath when a symlink inside the project was followed.
	ResolvedRelativePath string
}

// CanonicalProjectRoot returns the absolute, symlink-resolved project root and
// requires it to be an existing directory.
func CanonicalProjectRoot(projectRoot string) (string, error) {
	return canonicalLocalProjectRoot(projectRoot)
}

// CleanProjectRelativePath validates a project-relative path and returns it
// cleaned with forward slashes. Unlike joinUnderRoot, which clamps "../x" to
// "x", a ".." segment is rejected so callers never act on a different file
// than the one they named.
func CleanProjectRelativePath(relativePath string) (string, error) {
	value := strings.TrimSpace(relativePath)
	if value == "" || strings.ContainsRune(value, '\x00') {
		return "", ErrProjectPathInvalid
	}
	slashed := strings.ReplaceAll(value, `\`, "/")
	if strings.HasPrefix(slashed, "/") || filepath.IsAbs(value) || filepath.VolumeName(value) != "" {
		return "", ErrProjectPathInvalid
	}
	for _, segment := range strings.Split(slashed, "/") {
		if segment == ".." {
			return "", ErrProjectPathInvalid
		}
	}
	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(slashed)))
	if cleaned == "." || cleaned == "" {
		return "", ErrProjectPathInvalid
	}
	return cleaned, nil
}

// ResolveUnderProjectRoot resolves an existing path under an already canonical
// root, following symlinks, and fails when the final target leaves the root.
func ResolveUnderProjectRoot(rootCanon, relativePath string) (string, error) {
	cleaned, err := CleanProjectRelativePath(relativePath)
	if err != nil {
		return "", err
	}
	resolved, err := resolveUnderRoot(rootCanon, cleaned)
	if err != nil {
		if err.Error() == "path_escapes_root" {
			return "", ErrProjectPathEscapesRoot
		}
		return "", err
	}
	return resolved, nil
}

// ResolveProjectFile canonicalizes projectRoot and resolves relativePath under
// it. The target must exist; its type is not checked (callers Lstat/Stat the
// returned Path). Missing targets return an error wrapping os.ErrNotExist.
func ResolveProjectFile(projectRoot, relativePath string) (ProjectFile, error) {
	root, err := CanonicalProjectRoot(projectRoot)
	if err != nil {
		return ProjectFile{}, err
	}
	cleaned, err := CleanProjectRelativePath(relativePath)
	if err != nil {
		return ProjectFile{}, err
	}
	resolved, err := ResolveUnderProjectRoot(root, cleaned)
	if err != nil {
		return ProjectFile{}, err
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || !withinRoot(root, resolved) {
		return ProjectFile{}, ErrProjectPathEscapesRoot
	}
	return ProjectFile{Root: root, RelativePath: cleaned, Path: resolved, ResolvedRelativePath: filepath.ToSlash(rel)}, nil
}

// IsProjectPathNotExist reports whether a resolution error means the target
// (or one of its parents) does not exist.
func IsProjectPathNotExist(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	var pathErr *os.PathError
	return errors.As(err, &pathErr) && os.IsNotExist(pathErr)
}
