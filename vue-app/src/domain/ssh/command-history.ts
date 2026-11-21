export interface SshHistoryEntry {
  id: string;
  command: string;
  favorite: boolean;
  importedAt: string;
}

export interface SshHistoryData {
  entries: SshHistoryEntry[];
  // Retained for existing saved preferences; the backend also gates imports with it.
  enabled: boolean;
}

export type SshHistoryRequest =
  | { action: "list" }
  | { action: "update"; operation: "clear" }
  | { action: "update"; operation: "setEnabled"; enabled: boolean }
  | { action: "update"; operation: "delete" | "toggleFavorite"; id: string };

export function parseSshHistoryData(value: unknown): SshHistoryData {
  if (!value || typeof value !== "object") throw new Error("Invalid SSH history response");
  const data = value as Record<string, unknown>;
  if (typeof data.enabled !== "boolean" || !Array.isArray(data.entries)) {
    throw new Error("Invalid SSH history response");
  }
  const entries = data.entries.map((value: unknown): SshHistoryEntry => {
    if (!value || typeof value !== "object") throw new Error("Invalid SSH history entry");
    const entry = value as Record<string, unknown>;
    if (typeof entry.id !== "string" || typeof entry.command !== "string"
      || typeof entry.favorite !== "boolean" || typeof entry.importedAt !== "string") {
      throw new Error("Invalid SSH history entry");
    }
    return { id: entry.id, command: entry.command, favorite: entry.favorite, importedAt: entry.importedAt };
  });
  return { entries, enabled: data.enabled };
}
