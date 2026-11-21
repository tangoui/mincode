package plugin

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"mincode/app/backend/core"
)

// InstallPreview is informational, not an approval token or a sandbox guarantee.
// Installation always revalidates the source and the copied package.
type InstallPreview struct {
	SourcePath string   `json:"sourcePath"`
	Manifest   Manifest `json:"manifest"`
	FileCount  int      `json:"fileCount"`
	TotalBytes int64    `json:"totalBytes"`
	Installed  bool     `json:"installed"`
	// GlobalShortcutsChanged lists, for an update, the IDs of global
	// shortcuts that are on now and that this package changes (default key or
	// what they run): installing it turns them off until the user turns them
	// on again.
	GlobalShortcutsChanged []string `json:"globalShortcutsChanged,omitempty"`
	// DataProviders lists the validated entries of the data provider points
	// (sessionSources, scmCommitMessage, fileDecorations) so the preview can say what data the plugin is asked
	// for and when; titles are the manifest's own text.
	DataProviders []InstallPreviewDataProvider `json:"dataProviders,omitempty"`
	// ContributionIssues lists entries the package declares validly that the
	// host will still skip once installed because of other installed plugins
	// (a jdtlsBundles command another plugin already declares).
	ContributionIssues []ContributionIssue `json:"contributionIssues,omitempty"`
}

// InstallPreviewDataProvider is one data provider entry of a previewed
// package: the point, the entry ID and its title with translations.
type InstallPreviewDataProvider struct {
	Point          string            `json:"point"`
	ID             string            `json:"id"`
	Title          string            `json:"title"`
	TitleLocalized map[string]string `json:"titleLocalized,omitempty"`
}

// LocalizedTitle picks the first language of the chain the manifest
// translates (exact key, then case-insensitive), else the default title.
func (provider InstallPreviewDataProvider) LocalizedTitle(locale []string) string {
	return trayLocalized(provider.Title, provider.TitleLocalized, locale)
}

// installPreviewDataProviders collects the data provider lines of a
// validated manifest in point order, then entry order.
func installPreviewDataProviders(manifest Manifest) []InstallPreviewDataProvider {
	var lines []InstallPreviewDataProvider
	for _, source := range manifest.SessionSources() {
		lines = append(lines, InstallPreviewDataProvider{
			Point: sessionSourcePointKey, ID: source.ID, Title: source.Title, TitleLocalized: cloneStringMap(source.TitleLocalized),
		})
	}
	for _, provider := range manifest.ScmCommitMessageProviders() {
		lines = append(lines, InstallPreviewDataProvider{
			Point: scmCommitMessagePointKey, ID: provider.ID, Title: provider.Title, TitleLocalized: cloneStringMap(provider.TitleLocalized),
		})
	}
	for _, provider := range manifest.FileDecorationProviders() {
		lines = append(lines, InstallPreviewDataProvider{
			Point: fileDecorationPointKey, ID: provider.ID, Title: provider.Title, TitleLocalized: cloneStringMap(provider.TitleLocalized),
		})
	}
	return lines
}

type InstallPreviewArgs struct {
	SourcePath string `json:"sourcePath"`
}

// PreviewDownloadedArchive runs the same read-only preflight as a user-selected ZIP.
// The caller keeps the archive until the user confirms installation from preview.SourcePath.
func PreviewDownloadedArchive(path string) (InstallPreview, error) {
	if err := requirePluginZIP(path); err != nil {
		return InstallPreview{}, err
	}
	return host().PreviewInstall(path)
}

func pluginHostInstallPreviewHandler(_, argsJSON string) (string, error) {
	var args InstallPreviewArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if err := requirePluginInstallSource(args.SourcePath); err != nil {
		return "", err
	}
	preview, err := host().PreviewInstall(args.SourcePath)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(preview)
}

func (manager *pluginManager) PreviewInstall(sourcePath string) (InstallPreview, error) {
	preview, err := inspectInstallSource(sourcePath)
	if err != nil {
		return InstallPreview{}, err
	}
	manager.mu.RLock()
	_, preview.Installed = manager.records[preview.Manifest.ID]
	manager.mu.RUnlock()
	if _, err := os.Lstat(filepath.Join(pluginsDirectory(), preview.Manifest.ID)); err == nil {
		preview.Installed = true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return InstallPreview{}, err
	}
	preview.DataProviders = installPreviewDataProviders(preview.Manifest)
	preview.ContributionIssues = manager.jdtlsBundlePreviewIssues(preview.Manifest)
	if preview.Installed {
		if entries, _ := parseGlobalShortcuts(&preview.Manifest); len(entries) > 0 {
			preview.GlobalShortcutsChanged = manager.globalShortcutRegistry().changedByUpdate(preview.Manifest.ID, entries)
		}
	}
	return preview, nil
}

// ZIP preview writes only disposable unpacking files, never plugin data.
func inspectInstallSource(sourcePath string) (result InstallPreview, err error) {
	root, source, cleanup, err := prepareInstallSource(sourcePath)
	if err != nil {
		return InstallPreview{}, err
	}
	defer func() { err = errors.Join(err, cleanup()) }()
	result, err = inspectInstallDirectory(root)
	result.SourcePath = source
	return result, err
}

// Read every regular file, without staging, executing, registering or writing KV.
func inspectInstallDirectory(sourcePath string) (InstallPreview, error) {
	result, err := inspectPackageDirectory(sourcePath)
	if err != nil {
		return result, err
	}
	// Installation is strict: a package with any skipped keymap preset is rejected.
	return result, result.Manifest.contributionIssuesError()
}

// inspectPackageDirectory applies every package check except the strict keymap
// rule. Development sessions use it so a broken preset is skipped and reported
// (as installed packages are at load time) instead of blocking the session.
func inspectPackageDirectory(sourcePath string) (InstallPreview, error) {
	var result InstallPreview
	if strings.TrimSpace(sourcePath) == "" {
		return result, errors.New("plugin sourcePath is required")
	}
	source, err := filepath.Abs(strings.TrimSpace(sourcePath))
	if err != nil {
		return result, err
	}
	info, err := os.Lstat(source)
	if err != nil {
		return result, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return result, errors.New("plugin source must be a directory and cannot be a symlink")
	}
	filter := newPluginInstallFilter(source)
	err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("plugin package contains symlink: %s", path)
		}
		if filter.skip(path, entry) {
			return filepath.SkipDir
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("plugin package contains unsupported file: %s", path)
		}
		result.FileCount++
		if result.FileCount > pluginInstallMaxFiles || info.Size() > pluginInstallMaxBytes-result.TotalBytes {
			return errors.New("plugin package exceeds the file count or 64 MiB size limit")
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		opened, err := file.Stat()
		if err != nil || !os.SameFile(info, opened) {
			file.Close()
			return errors.New("plugin package changed during inspection")
		}
		n, readErr := io.Copy(io.Discard, io.LimitReader(file, pluginInstallMaxBytes-result.TotalBytes+1))
		closeErr := file.Close()
		result.TotalBytes += n
		if readErr != nil {
			return readErr
		}
		if result.TotalBytes > pluginInstallMaxBytes || n != info.Size() {
			return errors.New("plugin package changed or exceeds 64 MiB size limit")
		}
		return closeErr
	})
	if err != nil {
		return InstallPreview{}, err
	}
	result.Manifest, err = readManifest(source)
	if err != nil {
		return result, err
	}
	// Reject a foreign-platform native entry up front: installation preview,
	// InstallLocal, development pairing, and generation switches all inspect
	// through here, so the mismatch never reaches fork/exec.
	if err := verifyPackageExecutableTarget(source, result.Manifest); err != nil {
		return InstallPreview{}, err
	}
	return result, nil
}
