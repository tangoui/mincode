package backup

import "strings"

// Backup sections group stored keys for selective export and restore. The
// mapping must stay in sync with vue-app/src/domain/backup/sections.ts; both
// are checked against testdata/sections.json.
const (
	SectionSettings   = "settings"
	SectionSSH        = "ssh"
	SectionDatabase   = "database"
	SectionWorkspaces = "workspaces"
	// SectionAI holds AI approval policy and tool authorizations. AI model
	// profiles and MCP servers live in app_settings_v2 and belong to settings.
	SectionAI = "ai"
)

// CoreSections lists the built-in sections in display order.
var CoreSections = []string{SectionSettings, SectionSSH, SectionDatabase, SectionWorkspaces, SectionAI}

const pluginSectionPrefix = "plugin:"

var sshSectionKeys = map[string]struct{}{
	"app_ssh_connections_v1":            {},
	"app_ssh_credentials_v1":            {},
	"app_ssh_connection_group_order_v1": {},
}

// SectionForKey returns the core section a stored key belongs to. Rules are
// matched in order; any other key falls back to settings.
func SectionForKey(key string) string {
	if _, ok := sshSectionKeys[key]; ok {
		return SectionSSH
	}
	switch {
	case strings.HasPrefix(key, "app_db_"):
		return SectionDatabase
	case strings.HasPrefix(key, "app_workspace"), strings.HasPrefix(key, "app_local_project"):
		return SectionWorkspaces
	case strings.HasPrefix(key, "app_ai_"):
		return SectionAI
	default:
		return SectionSettings
	}
}

// PluginSectionID returns the section id for a plugin's data, or "" when
// pluginID is empty.
func PluginSectionID(pluginID string) string {
	if pluginID == "" {
		return ""
	}
	return pluginSectionPrefix + pluginID
}

// IsPluginSection reports whether id is a plugin section and returns the
// plugin id. "plugin:" with no id is not a plugin section.
func IsPluginSection(id string) (string, bool) {
	pluginID, ok := strings.CutPrefix(id, pluginSectionPrefix)
	if !ok || pluginID == "" {
		return "", false
	}
	return pluginID, true
}
