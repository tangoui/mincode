package plugin

import (
	"os"
	"path/filepath"
)

// The original directory capabilities live across Plan/confirmation/Run, not just one scan.
type pluginDevPublishRoots struct {
	project, release         string
	projectTree, releaseTree *os.Root
	projectInfo, releaseInfo os.FileInfo
}

func (roots *pluginDevPublishRoots) close() {
	if roots.releaseTree != nil {
		_ = roots.releaseTree.Close()
	}
	if roots.projectTree != nil {
		_ = roots.projectTree.Close()
	}
}
func (roots *pluginDevPublishRoots) verify() error {
	for _, item := range []struct {
		path string
		tree *os.Root
		info os.FileInfo
	}{{roots.project, roots.projectTree, roots.projectInfo}, {roots.release, roots.releaseTree, roots.releaseInfo}} {
		canonical, err := canonicalPluginDevRoot(item.path)
		if err != nil || canonical != item.path {
			return publishError("path_outside")
		}
		current, err := os.Stat(item.path)
		if err != nil || !os.SameFile(current, item.info) {
			return publishError("path_outside")
		}
		pinned, err := item.tree.Stat(".")
		if err != nil || !os.SameFile(pinned, item.info) {
			return publishError("path_outside")
		}
	}
	return nil
}
func pinPublishRoots(in pluginDevPublishRequest) (roots *pluginDevPublishRoots, normalized pluginDevPublishRequest, err error) {
	project, e := canonicalPluginDevRoot(in.ProjectRoot)
	if e != nil {
		return nil, in, publishError("invalid_request")
	}
	release, e := canonicalPluginDevRoot(in.ReleaseDir)
	if e != nil {
		return nil, in, publishError("invalid_request")
	}
	if !pathWithinRoot(project, release) {
		return nil, in, publishError("path_outside")
	}
	roots = &pluginDevPublishRoots{project: project, release: release}
	defer func() {
		if err != nil {
			roots.close()
		}
	}()
	roots.projectTree, e = os.OpenRoot(project)
	if e != nil {
		return roots, in, publishError("path_outside")
	}
	roots.projectInfo, e = roots.projectTree.Stat(".")
	if e != nil {
		return roots, in, publishError("path_outside")
	}
	rel, e := filepath.Rel(project, release)
	if e != nil {
		return roots, in, publishError("path_outside")
	}
	roots.releaseTree, e = roots.projectTree.OpenRoot(rel)
	if e != nil {
		return roots, in, publishError("path_outside")
	}
	roots.releaseInfo, e = roots.releaseTree.Stat(".")
	if e != nil {
		return roots, in, publishError("path_outside")
	}
	if e = roots.verify(); e != nil {
		return roots, in, e
	}
	// Capture canonical, root-relative names once; confirmation never reinterprets raw CLI roots.
	relative := func(root, path string) (string, error) {
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		parent, e := filepath.EvalSymlinks(filepath.Dir(path))
		if e != nil {
			return "", publishError("path_outside")
		}
		path = filepath.Join(parent, filepath.Base(path))
		if !pathWithinRoot(root, path) {
			return "", publishError("path_outside")
		}
		return filepath.Rel(root, path)
	}
	normalized = in
	normalized.ProjectRoot = project
	normalized.ReleaseDir = release
	normalized.Tags = append([]string(nil), in.Tags...)
	normalized.Packages = append([]pluginDevPublishPackageInput(nil), in.Packages...)
	for i := range normalized.Packages {
		normalized.Packages[i].Path, e = relative(release, in.Packages[i].Path)
		if e != nil {
			return roots, in, e
		}
	}
	if in.CoverPath != "" {
		normalized.CoverPath, e = relative(project, in.CoverPath)
		if e != nil {
			return roots, in, e
		}
	}
	return roots, normalized, nil
}
