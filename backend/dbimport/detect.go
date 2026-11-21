package dbimport

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"mincode/app/backend/core"
)

// DetectedLocation is a default location of another tool's connection
// store. Detection only checks that paths exist; nothing is read until the
// user picks a location for preview.
type DetectedLocation struct {
	Source string `json:"source"`
	Path   string `json:"path"`
	Label  string `json:"label"`
}

type locationEnv struct {
	goos      string
	home      string
	appData   string
	xdgData   string
	xdgConfig string
}

func currentLocationEnv() locationEnv {
	home, _ := os.UserHomeDir()
	return locationEnv{
		goos:      runtime.GOOS,
		home:      home,
		appData:   os.Getenv("APPDATA"),
		xdgData:   os.Getenv("XDG_DATA_HOME"),
		xdgConfig: os.Getenv("XDG_CONFIG_HOME"),
	}
}

const maxDetectedLocations = 64

func detectHandler(_, _ string) (string, error) {
	return core.EncodeResult(detectLocations(currentLocationEnv()))
}

// toolRoots returns the per-OS base folders of DBeaver, JetBrains IDEs and
// MySQL Workbench.
func toolRoots(env locationEnv) (dbeaver, jetbrains, workbench string) {
	if env.home == "" {
		return "", "", ""
	}
	switch env.goos {
	case "darwin":
		support := filepath.Join(env.home, "Library", "Application Support")
		return filepath.Join(env.home, "Library", "DBeaverData", "workspace6"),
			filepath.Join(support, "JetBrains"),
			filepath.Join(support, "MySQL", "Workbench")
	case "windows":
		appData := env.appData
		if appData == "" {
			appData = filepath.Join(env.home, "AppData", "Roaming")
		}
		return filepath.Join(appData, "DBeaverData", "workspace6"),
			filepath.Join(appData, "JetBrains"),
			filepath.Join(appData, "MySQL", "Workbench")
	default:
		data := env.xdgData
		if data == "" {
			data = filepath.Join(env.home, ".local", "share")
		}
		config := env.xdgConfig
		if config == "" {
			config = filepath.Join(env.home, ".config")
		}
		return filepath.Join(data, "DBeaverData", "workspace6"),
			filepath.Join(config, "JetBrains"),
			filepath.Join(env.home, ".mysql", "workbench")
	}
}

var jetbrainsProducts = []string{"DataGrip", "IntelliJIdea", "PyCharm", "GoLand", "WebStorm", "PhpStorm", "Rider", "RubyMine", "CLion", "RustRover", "DataSpell"}

func detectLocations(env locationEnv) []DetectedLocation {
	found := []DetectedLocation{}
	add := func(source, path, label string) {
		if len(found) < maxDetectedLocations {
			found = append(found, DetectedLocation{Source: source, Path: path, Label: label})
		}
	}
	dbeaverRoot, jetbrainsRoot, workbenchRoot := toolRoots(env)
	if dbeaverRoot != "" {
		for _, project := range listDirs(dbeaverRoot) {
			dir := filepath.Join(dbeaverRoot, project, ".dbeaver")
			if isFile(filepath.Join(dir, "data-sources.json")) {
				add(SourceDBeaver, dir, "DBeaver · "+project)
			}
		}
	}
	if jetbrainsRoot != "" {
		for _, product := range listDirs(jetbrainsRoot) {
			if !hasProductPrefix(product) {
				continue
			}
			options := filepath.Join(jetbrainsRoot, product, "options")
			if isFile(filepath.Join(options, "dataSources.xml")) {
				add(SourceDataGrip, options, product)
			}
		}
	}
	if env.home != "" {
		projects := filepath.Join(env.home, "DataGripProjects")
		for _, project := range listDirs(projects) {
			idea := filepath.Join(projects, project, ".idea")
			if isFile(filepath.Join(idea, "dataSources.xml")) {
				add(SourceDataGrip, idea, "DataGrip · "+project)
			}
		}
	}
	if workbenchRoot != "" && isFile(filepath.Join(workbenchRoot, "connections.xml")) {
		add(SourceWorkbench, filepath.Join(workbenchRoot, "connections.xml"), "MySQL Workbench")
	}
	return found
}

func hasProductPrefix(name string) bool {
	for _, product := range jetbrainsProducts {
		if strings.HasPrefix(name, product) {
			return true
		}
	}
	return false
}

// listDirs lists sub-directory names (bounded, sorted); errors yield none.
func listDirs(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := []string{}
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
			names = append(names, entry.Name())
		}
		if len(names) >= maxDetectedLocations {
			break
		}
	}
	sort.Strings(names)
	return names
}
