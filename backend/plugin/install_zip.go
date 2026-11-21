package plugin

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func isPluginPackagePath(source string) bool {
	ext := filepath.Ext(source)
	return strings.EqualFold(ext, ".min") || strings.EqualFold(ext, ".zip")
}

// Public local installation accepts a built plugin directory or a .min/.zip ZIP archive.
// The directory remains borrowed and is never executed during preview or install.
func requirePluginInstallSource(source string) error {
	trimmed := strings.TrimSpace(source)
	if trimmed == "" {
		return errors.New("plugin sourcePath is required")
	}
	info, err := os.Lstat(trimmed)
	if err != nil {
		return err
	}
	if info.IsDir() || (info.Mode().IsRegular() && isPluginPackagePath(trimmed)) {
		return nil
	}
	return errors.New("plugin source must be a directory or regular .min/.zip file, not a symlink")
}

// Directory sources are borrowed; ZIP sources are unpacked only into a private,
// disposable directory. Callers must finish validation/copying before cleanup.
func prepareInstallSource(sourcePath string) (root, source string, cleanup func() error, err error) {
	cleanup = func() error { return nil }
	if strings.TrimSpace(sourcePath) == "" {
		return "", "", cleanup, errors.New("plugin sourcePath is required")
	}
	source, err = filepath.Abs(strings.TrimSpace(sourcePath))
	if err != nil {
		return "", "", cleanup, err
	}
	info, err := os.Lstat(source)
	if err != nil {
		return "", source, cleanup, err
	}
	if info.IsDir() {
		root, err = resolvePluginSourceProject(source)
		return root, root, cleanup, err
	}
	if !info.Mode().IsRegular() || !isPluginPackagePath(source) {
		return "", source, cleanup, errors.New("plugin source must be a directory or regular .min/.zip file, not a symlink")
	}
	file, err := os.Open(source)
	if err != nil {
		return "", source, cleanup, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || opened.Size() != info.Size() {
		return "", source, cleanup, errors.New("plugin ZIP changed while opening")
	}
	if opened.Size() > pluginInstallMaxBytes {
		return "", source, cleanup, errors.New("plugin ZIP exceeds 64 MiB compressed input limit")
	}
	archive, err := zip.NewReader(io.NewSectionReader(file, 0, opened.Size()), opened.Size())
	if err != nil {
		return "", source, cleanup, fmt.Errorf("read plugin ZIP: %w", err)
	}
	if err := validatePluginZIPEntries(archive.File); err != nil {
		return "", source, cleanup, err
	}
	temp, err := os.MkdirTemp("", "mincode-plugin-zip-*")
	if err != nil {
		return "", source, cleanup, err
	}
	cleanup = func() error { return os.RemoveAll(temp) }
	root, err = extractPluginZIP(archive.File, temp)
	if err != nil {
		err = errors.Join(err, cleanup())
	}
	return root, source, cleanup, err
}

func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// defaultSourcePackageRoot is where plugin source projects build their runtime package
// unless mincode.config.json names another packageRoot (matching the DevKit CLI).
const defaultSourcePackageRoot = "dist-plugin"

// A plugin source project (no plugin.json at its root) installs from its built
// packageRoot, which must stay inside the project directory. The root comes from the
// optional mincode.config.json, else the dist-plugin/ convention.
func resolvePluginSourceProject(dir string) (string, error) {
	if _, err := os.Lstat(filepath.Join(dir, manifestFilename)); !errors.Is(err, fs.ErrNotExist) {
		return dir, nil
	}
	var config struct {
		PackageRoot string `json:"packageRoot"`
	}
	data, err := os.ReadFile(filepath.Join(dir, "mincode.config.json"))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if !pathExists(filepath.Join(dir, "plugin.template.json")) && !pathExists(filepath.Join(dir, defaultSourcePackageRoot, manifestFilename)) {
			return dir, nil
		}
	case err != nil:
		return "", fmt.Errorf("read mincode.config.json: %w", err)
	default:
		if err := json.Unmarshal(data, &config); err != nil {
			return "", fmt.Errorf("parse mincode.config.json: %w", err)
		}
	}
	relative := defaultSourcePackageRoot
	if configured := strings.TrimSpace(config.PackageRoot); configured != "" {
		relative = filepath.Clean(configured)
	}
	if relative == "." {
		return dir, nil
	}
	if filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("mincode.config.json packageRoot must be a subdirectory of the plugin project")
	}
	packageRoot := filepath.Join(dir, relative)
	if _, err := os.Lstat(filepath.Join(packageRoot, manifestFilename)); err != nil {
		return "", fmt.Errorf("this is a plugin source project: build it first so %s contains %s, then install again", packageRoot, manifestFilename)
	}
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	resolvedRoot, err := filepath.EvalSymlinks(packageRoot)
	if err != nil {
		return "", err
	}
	if rel, err := filepath.Rel(resolvedDir, resolvedRoot); err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("mincode.config.json packageRoot must be a subdirectory of the plugin project")
	}
	return packageRoot, nil
}

func validatePluginZIPEntries(files []*zip.File) error {
	// Include directory records in the count to bound empty-directory archives.
	if len(files) == 0 || len(files) > pluginInstallMaxFiles {
		return errors.New("plugin ZIP is empty or exceeds the 2000 entry limit")
	}
	type node struct {
		name          string
		dir, explicit bool
	}
	nodes := map[string]node{}
	var total uint64
	for _, file := range files {
		mode := file.Mode()
		if (!mode.IsRegular() && !mode.IsDir()) || mode.IsDir() != strings.HasSuffix(file.Name, "/") {
			return fmt.Errorf("plugin ZIP contains unsupported file: %q", file.Name)
		}
		if file.Flags&(1|64|8192) != 0 || (file.Method != zip.Store && file.Method != zip.Deflate) {
			return fmt.Errorf("plugin ZIP contains encrypted or unsupported entry: %q", file.Name)
		}
		name := strings.TrimSuffix(file.Name, "/")
		if !fs.ValidPath(name) || name == "." || strings.ContainsAny(name, "\\:\x00") {
			return fmt.Errorf("plugin ZIP contains unsafe path: %q", file.Name)
		}
		parts := strings.Split(name, "/")
		for i, part := range parts {
			// Reject Windows aliases even when unpacking on a Unix host.
			base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
			if strings.TrimRight(part, ". ") != part || strings.ContainsAny(part, "<>\"|?*") || base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || (len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9') {
				return fmt.Errorf("plugin ZIP contains unsafe path: %q", file.Name)
			}
			prefix := strings.Join(parts[:i+1], "/")
			key := strings.ToLower(prefix)
			explicit := i == len(parts)-1
			dir := !explicit || mode.IsDir()
			previous, exists := nodes[key]
			if exists && (previous.name != prefix || !previous.dir || !dir || (previous.explicit && explicit)) {
				return fmt.Errorf("plugin ZIP contains duplicate or conflicting path: %q", file.Name)
			}
			nodes[key] = node{prefix, dir, explicit || previous.explicit}
		}
		if file.CompressedSize64 > pluginInstallMaxBytes || file.UncompressedSize64 > pluginInstallMaxBytes-total {
			return errors.New("plugin ZIP exceeds 64 MiB package limit")
		}
		total += file.UncompressedSize64
		if mode.IsDir() && file.UncompressedSize64 != 0 {
			return errors.New("plugin ZIP directory contains data")
		}
	}
	return nil
}

func extractPluginZIP(files []*zip.File, temp string) (string, error) {
	var total int64
	for _, file := range files {
		name := strings.TrimSuffix(file.Name, "/")
		destination := filepath.Join(temp, filepath.FromSlash(name))
		if file.Mode().IsDir() {
			input, err := file.Open()
			if err != nil {
				return "", err
			}
			n, readErr := io.Copy(io.Discard, io.LimitReader(input, 1))
			closeErr := input.Close()
			if n != 0 {
				return "", errors.New("plugin ZIP directory contains data")
			}
			if err := errors.Join(readErr, closeErr); err != nil {
				return "", err
			}
			if err := os.MkdirAll(destination, 0o700); err != nil {
				return "", err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return "", err
		}
		n, err := extractPluginZIPFile(file, destination, pluginInstallMaxBytes-total)
		if err != nil {
			return "", fmt.Errorf("extract plugin ZIP entry %q: %w", file.Name, err)
		}
		total += n
	}
	entries, err := os.ReadDir(temp)
	if err != nil {
		return "", err
	}
	rootManifest := false
	wrapper := ""
	for _, entry := range entries {
		if entry.Name() == manifestFilename && !entry.IsDir() {
			rootManifest = true
		}
		if entry.IsDir() {
			if info, err := os.Lstat(filepath.Join(temp, entry.Name(), manifestFilename)); err == nil && info.Mode().IsRegular() {
				if wrapper != "" {
					return "", errors.New("plugin ZIP has ambiguous package roots")
				}
				wrapper = entry.Name()
			}
		}
	}
	if rootManifest && wrapper == "" {
		return temp, nil
	}
	if !rootManifest && wrapper != "" && len(entries) == 1 {
		return filepath.Join(temp, wrapper), nil
	}
	return "", errors.New("plugin ZIP requires root plugin.json or exactly one wrapper directory containing plugin.json; ambiguous roots are rejected")
}

func extractPluginZIPFile(file *zip.File, destination string, remaining int64) (int64, error) {
	input, err := file.Open()
	if err != nil {
		return 0, err
	}
	defer input.Close()
	// Never restore setuid/setgid/sticky or broad write permissions. Keep only
	// executable bits, with owner read/write so validation and cleanup always work.
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600|file.Mode().Perm()&0o111)
	if err != nil {
		return 0, err
	}
	n, copyErr := io.Copy(output, io.LimitReader(input, remaining+1))
	closeErr := output.Close()
	if n > remaining {
		return n, errors.New("plugin ZIP exceeds 64 MiB expanded package limit")
	}
	if copyErr == nil && uint64(n) != file.UncompressedSize64 {
		copyErr = errors.New("plugin ZIP entry size mismatch")
	}
	return n, errors.Join(copyErr, closeErr)
}
