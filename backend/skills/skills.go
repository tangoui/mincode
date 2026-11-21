// Package skills discovers user-installed Agent Skills without executing them.
package skills

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"mincode/app/backend/core"
)

type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"`
	Valid       bool   `json:"valid"`
	Error       string `json:"error,omitempty"`
}

func init() { core.Register("skills_list_user", handleList) }

func handleList(_ string, _ string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	root := filepath.Join(home, ".agents", "skills")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return "[]", nil
	}
	if err != nil {
		return "", err
	}

	result := make([]Skill, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path, err := findSkillFile(filepath.Join(root, entry.Name()))
		skill := Skill{Name: entry.Name(), Path: path}
		if err != nil {
			skill.Error = err.Error()
			result = append(result, skill)
			continue
		}
		content, err := os.ReadFile(path)
		if err != nil {
			skill.Error = err.Error()
		} else {
			skill.Name, skill.Description = parseFrontmatter(string(content))
			skill.Valid = skill.Description != ""
			if skill.Name == "" {
				skill.Name = entry.Name()
			}
			if !skill.Valid {
				skill.Error = "missing description frontmatter"
			}
		}
		result = append(result, skill)
	}
	encoded, err := json.Marshal(result)
	return string(encoded), err
}

func findSkillFile(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.EqualFold(entry.Name(), "SKILL.md") {
			return filepath.Join(dir, entry.Name()), nil
		}
	}
	return filepath.Join(dir, "SKILL.md"), os.ErrNotExist
}

func parseFrontmatter(content string) (name, description string) {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", ""
	}
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "---" {
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), "\"'")
		switch strings.TrimSpace(key) {
		case "name":
			name = value
		case "description":
			description = value
		}
	}
	return name, description
}
