// Persistence side of external AI settings sync (spec §5.1, §7): a plugin such
// as the CC Switch importer hands over its items, this service compares them
// with the saved settings and, once the host has confirmed the user's choices,
// writes them. The comparison and merge rules live in domain/ai/external-sync.
import { shallowRef } from "vue";
import { applySync, previewSync } from "@/domain/ai/external-sync";
import type {
  SecretHashes,
  SyncChoice,
  SyncItem,
  SyncPreviewEntry,
  SyncReviewedEntry,
  SyncSettingsLists,
} from "@/domain/ai/external-sync";
import {
  hydrateSettingsSnapshot,
  persistSettingsSnapshot,
  readSettingsSnapshot,
  updateSettingsRuntimeSnapshot,
} from "@/services/settings-persistence";
import {
  loadSecurePersistedValue,
  saveSecurePersistedValue,
} from "@/services/secure-persistent-store";

/**
 * Hashes of the secrets last written from each source item. Device-local on
 * purpose: the key has no `app_` prefix, so config sync never collects it
 * (domain/settings/config-sync.ts isConfigSyncStorageKey), matching the API
 * keys and MCP env/header values it describes, which also stay on this device.
 */
const SECRET_HASHES_STORAGE_KEY = "ai_external_sync_secrets_v1";

/**
 * Incremented after every successful external write. The open Settings page
 * watches it and replaces its AI lists with externallySyncedSettingsLists(),
 * so a save of its older lists cannot undo the write.
 */
export const aiSettingsExternalRevision = shallowRef(0);

type SettingsRecord = Record<string, unknown>;

let secretHashes: SecretHashes | null = null;
let applyQueue: Promise<unknown> = Promise.resolve();
let settingsFlush: (() => Promise<void>) | null = null;
let writtenLists: SyncSettingsLists | null = null;

/**
 * The open Settings page registers a function that saves its pending
 * debounced edits; an apply awaits it before reading the snapshot so those
 * edits are merged instead of discarded. Returns the unregister function.
 */
export function registerSettingsFlush(flush: () => Promise<void>): () => void {
  settingsFlush = flush;
  return () => {
    if (settingsFlush === flush) settingsFlush = null;
  };
}

/** Saves the open Settings page's pending edits, so a preview compares against them. */
export async function flushPendingSettingsEdits(): Promise<void> {
  await settingsFlush?.();
}

const reviewingPlugins = new Set<string>();

/**
 * Claims the single review-and-write slot of a plugin: across all its frames,
 * only one confirmation may be open and one write in flight at a time.
 * Returns the release function, or null while another one holds the slot.
 */
export function claimExternalSyncReview(pluginId: string): (() => void) | null {
  if (reviewingPlugins.has(pluginId)) return null;
  reviewingPlugins.add(pluginId);
  let released = false;
  return () => {
    if (released) return;
    released = true;
    reviewingPlugins.delete(pluginId);
  };
}

/** The AI lists the latest successful external write saved. */
export function externallySyncedSettingsLists(): SyncSettingsLists | null {
  return writtenLists;
}

function isRecord(value: unknown): value is SettingsRecord {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function recordList(value: unknown): SettingsRecord[] {
  return Array.isArray(value) ? value.filter(isRecord) : [];
}

function settingsLists(snapshot: SettingsRecord): SyncSettingsLists {
  return {
    aiProfiles: recordList(snapshot.aiProfiles),
    mcpServers: recordList(snapshot.mcpServers),
  };
}

/** Same id scheme as the Settings page's createId. */
function createId(prefix: string) {
  return typeof crypto !== "undefined" && typeof crypto.randomUUID === "function"
    ? crypto.randomUUID()
    : `${prefix}-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
}

async function loadSecretHashes(): Promise<SecretHashes> {
  if (secretHashes) return secretHashes;
  const stored = await loadSecurePersistedValue<unknown>(SECRET_HASHES_STORAGE_KEY);
  secretHashes = isRecord(stored)
    ? Object.fromEntries(
        Object.entries(stored).filter((entry): entry is [string, string] => typeof entry[1] === "string")
      )
    : {};
  return secretHashes;
}

/**
 * Best-effort: the settings are already saved when this runs. If the write is
 * lost, the next session offers those items again as needing their key, which
 * an overwrite resolves by rewriting only the secrets; no change is hidden.
 */
async function saveSecretHashes(next: SecretHashes) {
  secretHashes = next;
  // Browser storage gets an empty record: hashes of secrets never reach
  // localStorage, just as the browser settings snapshot carries no API keys.
  await saveSecurePersistedValue(SECRET_HASHES_STORAGE_KEY, next, {});
}

/**
 * The saved MCP servers a preview compares against (call after
 * previewExternalSync). Holds secret values: only for host-side checks such
 * as which confirmation prompts are already filled, never for a plugin.
 */
export function savedMcpServers(): SettingsRecord[] {
  return settingsLists(readSettingsSnapshot()).mcpServers;
}

/** savedMcpServers() once the saved settings are loaded; the same caveats apply. */
export async function loadSavedMcpServers(): Promise<SettingsRecord[]> {
  await hydrateSettingsSnapshot();
  return savedMcpServers();
}

/** Status of each source item; contains no secret values. */
export async function previewExternalSync(
  pluginId: string,
  items: SyncItem[]
): Promise<SyncPreviewEntry[]> {
  await hydrateSettingsSnapshot();
  const secrets = await loadSecretHashes();
  return previewSync(pluginId, items, settingsLists(readSettingsSnapshot()), secrets);
}

async function applyNow(
  pluginId: string,
  items: SyncItem[],
  choices: SyncChoice[],
  reviewed?: readonly SyncReviewedEntry[]
) {
  await settingsFlush?.();
  await hydrateSettingsSnapshot();
  const secrets = await loadSecretHashes();
  // Read synchronously right before the write so a Settings save that landed
  // during the awaits above is not overwritten.
  const current = readSettingsSnapshot();
  const result = applySync(pluginId, items, choices, settingsLists(current), secrets, createId, reviewed);
  if (result.added + result.overwritten > 0) {
    try {
      await persistSettingsSnapshot({ ...current, ...result.lists }, { strict: true });
    } catch (error) {
      // persistSettingsSnapshot updates the in-memory snapshot before the
      // native write; the write failed, so nothing may look applied.
      updateSettingsRuntimeSnapshot(current);
      throw error;
    }
    // Published with the revision rather than re-read from the snapshot: a
    // Settings save that landed during the await above holds the old lists.
    writtenLists = JSON.parse(JSON.stringify(result.lists)) as SyncSettingsLists;
    aiSettingsExternalRevision.value += 1;
    await saveSecretHashes(result.secrets);
  }
  return { added: result.added, overwritten: result.overwritten, failed: result.failed };
}

/**
 * Writes the confirmed choices to the settings. With `reviewed` (the entries
 * the host confirmation showed), choices whose entry changed since are
 * reported as failed instead of written. Rejects, leaving the settings and the
 * secret hashes unchanged, when the settings cannot be saved.
 */
export function applyExternalSync(
  pluginId: string,
  items: SyncItem[],
  choices: SyncChoice[],
  reviewed?: readonly SyncReviewedEntry[]
): Promise<{ added: number; overwritten: number; failed: { key: string; reason: string }[] }> {
  const run = applyQueue.then(() => applyNow(pluginId, items, choices, reviewed));
  applyQueue = run.catch(() => undefined);
  return run;
}
