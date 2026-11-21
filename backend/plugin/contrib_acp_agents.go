package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// ACP agents (contributes.acpAgents) let a process plugin that declares the
// shell capability offer Agent Client Protocol agents to the AI assistant,
// next to the built-in and user-configured ones. An entry only describes a
// launch; nothing runs until the user confirms it:
//
//   - command, args and env follow the MCP server rules
//     (validateProjectProcessLaunch): a bare program name or
//     "${pluginRoot}/<relative>", ${pluginRoot} / ${projectRoot} in args and
//     env values, no cwd (the agent always runs in its conversation's
//     project root, so there is no requiresProject);
//   - env values are literal and the shared denylist applies; the agent
//     still inherits the user's full environment like the built-in agents;
//   - the frontend and the AI name an agent only by (pluginId, agentId); the
//     host resolves the launch from its own snapshot.

const acpAgentPointKey = "acpAgents"

var (
	acpAgentSpec                = mustContributionSpec(acpAgentPointKey)
	acpAgentTitleMaxRunes       = int(acpAgentSpec.Limit("titleMaxRunes"))
	acpAgentDescriptionMaxRunes = int(acpAgentSpec.Limit("descriptionMaxRunes"))
	acpAgentLocalesMax          = int(acpAgentSpec.Limit("localesMax"))
	acpAgentIDPattern           = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
)

// AcpAgentContribution is one validated contributes.acpAgents entry as kept
// in the snapshot, with its launch unresolved (variables intact).
type AcpAgentContribution struct {
	ID                   string            `json:"id"`
	Title                string            `json:"title"`
	TitleLocalized       map[string]string `json:"titleLocalized,omitempty"`
	Description          string            `json:"description,omitempty"`
	DescriptionLocalized map[string]string `json:"descriptionLocalized,omitempty"`
	Command              string            `json:"command"`
	Args                 []string          `json:"args"`
	Env                  map[string]string `json:"env"`
	Platforms            []string          `json:"platforms"`
}

type acpAgentManifestEntry struct {
	ID                   string            `json:"id"`
	Title                string            `json:"title"`
	TitleLocalized       map[string]string `json:"titleLocalized"`
	Description          *string           `json:"description"`
	DescriptionLocalized map[string]string `json:"descriptionLocalized"`
	Command              *string           `json:"command"`
	Args                 []string          `json:"args"`
	Env                  map[string]string `json:"env"`
	Platforms            []string          `json:"platforms"`
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:              acpAgentPointKey,
		Decode:           decodeAcpAgentEntry,
		DuplicateMessage: func(id string) string { return fmt.Sprintf("ACP agent id %q is duplicated", id) },
		IssuePrefix:      "invalid acpAgents contribution",
		IssueText:        acpAgentIssueText,
		EntryNoun:        "ACP agents",
	})
}

func acpAgentIssueText(issue ContributionIssue) string {
	if issue.ID != "" {
		return fmt.Sprintf("ACP agent %q skipped: %s", issue.ID, issue.Message)
	}
	return fmt.Sprintf("ACP agent entry %d skipped: %s", issue.Entry, issue.Message)
}

func decodeAcpAgentEntry(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var entry acpAgentManifestEntry
	if err := decodeStrictJSON(raw, &entry, true); err != nil {
		return nil, strings.TrimSpace(entry.ID), fmt.Errorf("parse ACP agent: %w", err)
	}
	id := strings.TrimSpace(entry.ID)
	if !acpAgentIDPattern.MatchString(id) {
		return nil, id, fmt.Errorf("ACP agent id %q must match %s", id, acpAgentIDPattern)
	}
	agent := AcpAgentContribution{ID: id}
	var err error
	if agent.Title, err = validateContributionLine(entry.Title, acpAgentTitleMaxRunes, true); err != nil {
		return nil, id, fmt.Errorf("ACP agent %q title %w", id, err)
	}
	if agent.TitleLocalized, err = validateContributionLocalized(entry.TitleLocalized, acpAgentTitleMaxRunes, acpAgentLocalesMax); err != nil {
		return nil, id, fmt.Errorf("ACP agent %q titleLocalized %w", id, err)
	}
	if entry.Description != nil {
		if agent.Description, err = validateContributionLine(*entry.Description, acpAgentDescriptionMaxRunes, true); err != nil {
			return nil, id, fmt.Errorf("ACP agent %q description %w", id, err)
		}
	}
	if agent.DescriptionLocalized, err = validateContributionLocalized(entry.DescriptionLocalized, acpAgentDescriptionMaxRunes, acpAgentLocalesMax); err != nil {
		return nil, id, fmt.Errorf("ACP agent %q descriptionLocalized %w", id, err)
	}
	if agent.Platforms, err = normalizeContributionPlatforms(acpAgentSpec, entry.Platforms); err != nil {
		return nil, id, fmt.Errorf("ACP agent %q %w", id, err)
	}
	if entry.Command == nil {
		return nil, id, fmt.Errorf("ACP agent %q command is required", id)
	}
	definition, err := validateProjectProcessLaunch(acpAgentSpec, ctx.root, *entry.Command, entry.Args, entry.Env)
	if err != nil {
		return nil, id, fmt.Errorf("ACP agent %q %w", id, err)
	}
	agent.Command, agent.Args, agent.Env = definition.Command, definition.Args, definition.Env
	return agent, id, nil
}

// acpAgentSpecFingerprint identifies what the user confirms for an agent:
// its id and unresolved launch (command, args, env, platforms). Titles and
// descriptions do not take part, and ${projectRoot} stays unexpanded, so
// one confirmation holds for every project. The resolved launch fingerprint
// (plugin_process_launch.go) also covers the ${pluginRoot} executable hashes.
func acpAgentSpecFingerprint(agent AcpAgentContribution) string {
	canonical, _ := json.Marshal(struct {
		ID        string            `json:"id"`
		Command   string            `json:"command"`
		Args      []string          `json:"args"`
		Env       map[string]string `json:"env"`
		Platforms []string          `json:"platforms"`
	}{agent.ID, agent.Command, agent.Args, agent.Env, agent.Platforms})
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}
