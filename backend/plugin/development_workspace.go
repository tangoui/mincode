package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mincode/app/backend/core"
)

const (
	pluginDevScanMaxDepth = 6
	pluginDevScanMaxFiles = 50000
	pluginDevScanMaxItems = 500
)

type pluginDevTemplate struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type pluginDevProject struct {
	Path         string `json:"path"`
	ManifestPath string `json:"manifestPath"`
	PackageRoot  string `json:"packageRoot,omitempty"`
	PluginID     string `json:"pluginId,omitempty"`
	PluginName   string `json:"pluginName,omitempty"`
	Description  string `json:"description,omitempty"`
	Version      string `json:"version,omitempty"`
	Kind         string `json:"kind"`
	Status       string `json:"status"`
	Error        string `json:"error,omitempty"`
}

func init() {
	core.Register("plugin_host_development_templates", func(_, _ string) (string, error) {
		return core.EncodeResult([]pluginDevTemplate{{ID: "visual-process-vue-go", Name: "Vue + Go Process Plugin", Description: "Vue UI and Go runtime plugin template"}, {ID: "local-process-node", Name: "Node Process Plugin", Description: "Node process plugin template"}})
	})
	core.Register("plugin_host_development_scan", pluginDevScanHandler)
}

func pluginDevScanHandler(_, argsJSON string) (string, error) {
	var args struct {
		Root string `json:"root"`
	}
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	root, err := canonicalPluginDevRoot(args.Root)
	if err != nil {
		return "", fmt.Errorf("scan plugin directory: %w", err)
	}
	projects, err := scanPluginDevProjects(root)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(projects)
}

func scanPluginDevProjects(root string) ([]pluginDevProject, error) {
	canonical, err := canonicalPluginDevRoot(root)
	if err != nil {
		return nil, err
	}
	root = canonical
	projects := map[string]*pluginDevProject{}
	files := 0
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		files++
		if files > pluginDevScanMaxFiles {
			return errors.New("plugin scan exceeded entry limit")
		}
		if !pathWithinRoot(root, path) {
			return errors.New("plugin scan path escaped root")
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		depth := len(strings.Split(filepath.ToSlash(rel), "/"))
		if depth > pluginDevScanMaxDepth {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".mincode", "node_modules", "dist":
				return filepath.SkipDir
			case "dist-plugin":
				if _, err := readPluginDevDescriptor(root, filepath.Join(filepath.Dir(path), "plugin.template.json")); err == nil {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if entry.Name() != "plugin.json" && entry.Name() != "plugin.template.json" {
			return nil
		}
		if len(projects) >= pluginDevScanMaxItems && projects[filepath.Dir(path)] == nil {
			return errors.New("plugin scan exceeded project limit")
		}
		data, err := readPluginDevDescriptor(root, path)
		if err != nil {
			projects[filepath.Dir(path)] = &pluginDevProject{Path: filepath.Dir(path), ManifestPath: path, Kind: "unknown", Status: "error", Error: err.Error()}
			return nil
		}
		var manifest struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Version     string `json:"version"`
		}
		if err := json.Unmarshal(data, &manifest); err != nil {
			projects[filepath.Dir(path)] = &pluginDevProject{Path: filepath.Dir(path), ManifestPath: path, Kind: "unknown", Status: "error", Error: "invalid manifest"}
			return nil
		}
		projectRoot := filepath.Dir(path)
		kind, packageRoot := "source", ""
		if filepath.Base(path) == "plugin.json" {
			kind = "package"
		}
		if kind == "source" {
			if data, statErr := readPluginDevDescriptor(root, filepath.Join(projectRoot, "dist-plugin", "plugin.json")); statErr == nil {
				var built struct {
					ID string `json:"id"`
				}
				if json.Unmarshal(data, &built) == nil && built.ID == manifest.ID {
					packageRoot = filepath.Join(projectRoot, "dist-plugin")
				}
			}
		}
		if !pluginIDPattern.MatchString(manifest.ID) || strings.TrimSpace(manifest.Name) == "" || !pluginVersionPattern.MatchString(manifest.Version) {
			projects[projectRoot] = &pluginDevProject{Path: projectRoot, ManifestPath: path, Kind: kind, Status: "error", Error: "invalid manifest identity"}
			return nil
		}
		if existing := projects[projectRoot]; existing != nil && existing.Kind == "source" {
			return nil
		}
		projects[projectRoot] = &pluginDevProject{Path: projectRoot, ManifestPath: path, PackageRoot: packageRoot, PluginID: manifest.ID, PluginName: manifest.Name, Description: strings.TrimSpace(manifest.Description), Version: manifest.Version, Kind: kind, Status: "discovered"}
		return nil
	})
	if err != nil {
		return nil, err
	}
	result := make([]pluginDevProject, 0, len(projects))
	for _, project := range projects {
		result = append(result, *project)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	if len(result) > pluginDevScanMaxItems {
		return nil, errors.New("plugin scan exceeded project limit")
	}
	return result, nil
}

func readPluginDevDescriptor(root, path string) ([]byte, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	if !pathWithinRoot(root, resolved) || filepath.Clean(path) != resolved {
		return nil, errors.New("plugin descriptor cannot follow symbolic links")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 256*1024 {
		return nil, errors.New("plugin descriptor must be a regular file within 256 KiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 256*1024+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 256*1024 {
		return nil, errors.New("plugin descriptor exceeds 256 KiB")
	}
	return data, nil
}
