// Backup sections group stored keys for selective export and restore. The
// mapping must stay in sync with backend/backup/sections.go; both are
// checked against backend/backup/testdata/sections.json.

export const BACKUP_SECTION_SETTINGS = "settings";
export const BACKUP_SECTION_SSH = "ssh";
export const BACKUP_SECTION_DATABASE = "database";
export const BACKUP_SECTION_WORKSPACES = "workspaces";
// AI approval policy and tool authorizations. AI model profiles and MCP
// servers live in app_settings_v2 and belong to the settings section.
export const BACKUP_SECTION_AI = "ai";

export type BackupCoreSectionId =
  | typeof BACKUP_SECTION_SETTINGS
  | typeof BACKUP_SECTION_SSH
  | typeof BACKUP_SECTION_DATABASE
  | typeof BACKUP_SECTION_WORKSPACES
  | typeof BACKUP_SECTION_AI;

export type BackupSectionId = BackupCoreSectionId | `plugin:${string}`;

/** Built-in sections in display order. */
export const BACKUP_CORE_SECTIONS: readonly BackupCoreSectionId[] = Object.freeze([
  BACKUP_SECTION_SETTINGS,
  BACKUP_SECTION_SSH,
  BACKUP_SECTION_DATABASE,
  BACKUP_SECTION_WORKSPACES,
  BACKUP_SECTION_AI,
]);

const SSH_SECTION_KEYS: ReadonlySet<string> = new Set([
  "app_ssh_connections_v1",
  "app_ssh_credentials_v1",
  "app_ssh_connection_group_order_v1",
]);

/** Returns the core section a stored key belongs to; unknown keys fall back to settings. */
export function sectionForKey(key: string): BackupSectionId {
  if (SSH_SECTION_KEYS.has(key)) return BACKUP_SECTION_SSH;
  if (key.startsWith("app_db_")) return BACKUP_SECTION_DATABASE;
  if (key.startsWith("app_workspace") || key.startsWith("app_local_project")) return BACKUP_SECTION_WORKSPACES;
  if (key.startsWith("app_ai_")) return BACKUP_SECTION_AI;
  return BACKUP_SECTION_SETTINGS;
}

/** Returns the section id for a plugin's data. */
export function pluginSectionId(id: string): `plugin:${string}` {
  if (id === "") throw new Error("pluginSectionId: plugin id must not be empty");
  return `plugin:${id}`;
}
