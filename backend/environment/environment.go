// Package environment exposes a small, read-only development-tool diagnostic
// for the dashboard. It only checks commands available on PATH; it never runs
// installers or modifies the user's environment.
package environment

import (
	"os/exec"
	"runtime"

	"mincode/app/backend/core"
)

func init() {
	core.Register("system_get_environment_checks", getEnvironmentChecksHandler)
}

// EnvironmentCheck describes the availability of a development tool.
type EnvironmentCheck struct {
	ID        string `json:"id"`
	Available bool   `json:"available"`
	Command   string `json:"command,omitempty"`
	Path      string `json:"path,omitempty"`
}

// EnvironmentStatus is the dashboard-facing diagnostic response.
type EnvironmentStatus struct {
	Platform     string             `json:"platform"`
	Architecture string             `json:"architecture"`
	Checks       []EnvironmentCheck `json:"checks"`
}

func getEnvironmentChecksHandler(_, _ string) (string, error) {
	return core.EncodeResult(detectEnvironment())
}

func detectEnvironment() EnvironmentStatus {
	return EnvironmentStatus{
		Platform:     runtime.GOOS,
		Architecture: runtime.GOARCH,
		Checks: []EnvironmentCheck{
			detectCommand("shell", shellCandidates()),
			detectCommand("git", []string{"git"}),
			detectCommand("node", []string{"node"}),
			detectCommand("packageManager", []string{"pnpm", "npm", "yarn", "bun"}),
			detectCommand("docker", []string{"docker"}),
		},
	}
}

func shellCandidates() []string {
	switch runtime.GOOS {
	case "windows":
		return []string{"pwsh", "powershell", "cmd"}
	case "darwin":
		return []string{"zsh", "bash", "sh"}
	default:
		return []string{"bash", "sh"}
	}
}

func detectCommand(id string, candidates []string) EnvironmentCheck {
	for _, command := range candidates {
		path, err := exec.LookPath(command)
		if err == nil {
			return EnvironmentCheck{
				ID:        id,
				Available: true,
				Command:   command,
				Path:      path,
			}
		}
	}
	return EnvironmentCheck{ID: id}
}
