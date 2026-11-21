// The syncSource record that ties a saved AI settings entry to the plugin item
// it came from (domain/ai/external-sync). Kept dependency-free so host code that
// only needs to find a plugin's entry, such as the ai.config login, does not
// load the comparison and merge rules.

export type SyncKind = "profile" | "mcp" | "skill";

/** Stored on every synced settings object; travels with config sync, so it holds no secret. */
export interface SyncSource {
  pluginId: string;
  kind: SyncKind;
  sourceId: string;
  fingerprint: string;
}

function isSyncSource(value: unknown): value is SyncSource {
  return (
    Boolean(value) &&
    typeof value === "object" &&
    !Array.isArray(value) &&
    typeof (value as Record<string, unknown>).fingerprint === "string"
  );
}

/** Index of the saved entry this plugin wrote for the item (by syncSource), or -1. */
export function findSynced(
  list: readonly Record<string, unknown>[],
  pluginId: string,
  item: { kind: SyncKind; sourceId: string }
) {
  return list.findIndex((target) => {
    const source = target.syncSource;
    return (
      isSyncSource(source) &&
      source.pluginId === pluginId &&
      source.kind === item.kind &&
      source.sourceId === item.sourceId
    );
  });
}
