package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// MCP servers (contributes.mcpServers) let a process plugin that declares the
// shell capability offer stdio Model Context Protocol servers to the AI
// assistant. An entry only describes a launch; nothing runs until the user
// confirms it:
//
//   - command, args and env follow the language-server rules
//     (validateProjectProcessLaunch): a bare program name or
//     "${pluginRoot}/<relative>", ${pluginRoot} / ${projectRoot} in args and
//     env values, no cwd (the server runs in the project root);
//   - requiresProject: false declares a server that does not work on a
//     project (desktop automation, for example): it runs in the plugin root,
//     starts without a project and may not reference ${projectRoot};
//   - env values are literal: unlike user-configured MCP servers there is no
//     ${env:VAR} expansion, so a plugin cannot read host variables;
//   - launches are resolved by the host from its own snapshot by (pluginId,
//     serverId, projectRoot), confirmed once per server spec and started only
//     through plugin_host_mcp_server_start (mcp_servers_launch.go). The
//     gateway session id is "plugin:<pluginId>/<serverId>", which the WebView
//     connect commands refuse.

const mcpServerPointKey = "mcpServers"

var (
	mcpServerSpec                = mustContributionSpec(mcpServerPointKey)
	mcpServerTitleMaxRunes       = int(mcpServerSpec.Limit("titleMaxRunes"))
	mcpServerDescriptionMaxRunes = int(mcpServerSpec.Limit("descriptionMaxRunes"))
	mcpServerLocalesMax          = int(mcpServerSpec.Limit("localesMax"))
)

// McpServerContribution is one validated contributes.mcpServers entry as kept
// in the snapshot, with its launch unresolved (variables intact).
type McpServerContribution struct {
	ID                   string            `json:"id"`
	Title                string            `json:"title"`
	TitleLocalized       map[string]string `json:"titleLocalized,omitempty"`
	Description          string            `json:"description,omitempty"`
	DescriptionLocalized map[string]string `json:"descriptionLocalized,omitempty"`
	Command              string            `json:"command"`
	Args                 []string          `json:"args"`
	Env                  map[string]string `json:"env"`
	Platforms            []string          `json:"platforms"`
	RequiresProject      bool              `json:"requiresProject"`
	SpecFingerprint      string            `json:"specFingerprint"`
}

type mcpServerManifestEntry struct {
	ID                   string            `json:"id"`
	Title                string            `json:"title"`
	TitleLocalized       map[string]string `json:"titleLocalized"`
	Description          *string           `json:"description"`
	DescriptionLocalized map[string]string `json:"descriptionLocalized"`
	Command              *string           `json:"command"`
	Args                 []string          `json:"args"`
	Env                  map[string]string `json:"env"`
	Platforms            []string          `json:"platforms"`
	RequiresProject      *bool             `json:"requiresProject"`
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:              mcpServerPointKey,
		Decode:           decodeMcpServerEntry,
		DuplicateMessage: func(id string) string { return fmt.Sprintf("MCP server id %q is duplicated", id) },
		IssuePrefix:      "invalid mcpServers contribution",
		IssueText:        mcpServerIssueText,
		EntryNoun:        "MCP servers",
	})
}

func mcpServerIssueText(issue ContributionIssue) string {
	if issue.ID != "" {
		return fmt.Sprintf("MCP server %q skipped: %s", issue.ID, issue.Message)
	}
	return fmt.Sprintf("MCP server entry %d skipped: %s", issue.Entry, issue.Message)
}

func decodeMcpServerEntry(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var entry mcpServerManifestEntry
	if err := decodeStrictJSON(raw, &entry, true); err != nil {
		return nil, strings.TrimSpace(entry.ID), fmt.Errorf("parse MCP server: %w", err)
	}
	id := strings.TrimSpace(entry.ID)
	if !languageServerIDPattern.MatchString(id) {
		return nil, id, fmt.Errorf("MCP server id %q must match ^[a-z0-9][a-z0-9-]{0,31}$", id)
	}
	server := McpServerContribution{ID: id}
	var err error
	if server.Title, err = validateContributionLine(entry.Title, mcpServerTitleMaxRunes, true); err != nil {
		return nil, id, fmt.Errorf("MCP server %q title %w", id, err)
	}
	if server.TitleLocalized, err = validateContributionLocalized(entry.TitleLocalized, mcpServerTitleMaxRunes, mcpServerLocalesMax); err != nil {
		return nil, id, fmt.Errorf("MCP server %q titleLocalized %w", id, err)
	}
	if entry.Description != nil {
		if server.Description, err = validateContributionLine(*entry.Description, mcpServerDescriptionMaxRunes, true); err != nil {
			return nil, id, fmt.Errorf("MCP server %q description %w", id, err)
		}
	}
	if server.DescriptionLocalized, err = validateContributionLocalized(entry.DescriptionLocalized, mcpServerDescriptionMaxRunes, mcpServerLocalesMax); err != nil {
		return nil, id, fmt.Errorf("MCP server %q descriptionLocalized %w", id, err)
	}
	if server.Platforms, err = normalizeContributionPlatforms(mcpServerSpec, entry.Platforms); err != nil {
		return nil, id, fmt.Errorf("MCP server %q %w", id, err)
	}
	if entry.Command == nil {
		return nil, id, fmt.Errorf("MCP server %q command is required", id)
	}
	definition, err := validateProjectProcessLaunch(mcpServerSpec, ctx.root, *entry.Command, entry.Args, entry.Env)
	if err != nil {
		return nil, id, fmt.Errorf("MCP server %q %w", id, err)
	}
	server.Command, server.Args, server.Env = definition.Command, definition.Args, definition.Env
	server.RequiresProject = entry.RequiresProject == nil || *entry.RequiresProject
	if !server.RequiresProject && mcpServerUsesProject(server) {
		return nil, id, fmt.Errorf("MCP server %q has requiresProject false but uses %s", id, languageServerVarProject)
	}
	server.SpecFingerprint = mcpServerSpecFingerprint(server)
	return server, id, nil
}

// mcpServerUsesProject reports whether the launch references ${projectRoot}.
func mcpServerUsesProject(server McpServerContribution) bool {
	for _, arg := range server.Args {
		if strings.Contains(arg, languageServerVarProject) {
			return true
		}
	}
	for _, value := range server.Env {
		if strings.Contains(value, languageServerVarProject) {
			return true
		}
	}
	return false
}

func mcpServerSpecFingerprint(server McpServerContribution) string {
	copy := server
	copy.SpecFingerprint = ""
	canonical, _ := json.Marshal(copy)
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}
