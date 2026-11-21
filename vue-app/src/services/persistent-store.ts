import { createSerializedTaskQueue } from "@/domain/async/serialized-task-queue";
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";

const enqueueGlobalPersistedWrite = createSerializedTaskQueue();

/** Serializes every native persistence mutation in invocation order, including cross-key batches. */
export function enqueuePersistedWrite<T>(task: () => Promise<T>): Promise<T> {
  return enqueueGlobalPersistedWrite(task);
}

/**
 * Write freeze after a backup restore (services/backup-host.ts): stores still
 * hold their old in-memory state, and their flushes (debounced saves,
 * beforeunload) would write it back over the restored data. Frozen keys are
 * skipped on every write path, native and browser; the freeze lives only in
 * memory, so the reload that follows a restore clears it.
 */
let allPersistedWritesFrozen = 0;
const frozenPersistedKeys = new Map<string, number>();
const reportedFrozenKeys = new Set<string>();

/**
 * Freezes writes of keys (or of every key); returns the function that lifts
 * this freeze. Freezes nest: a key stays frozen until every freeze holding
 * it was lifted.
 */
export function freezePersistedWrites(keys: Iterable<string> | "all"): () => void {
  if (keys === "all") {
    allPersistedWritesFrozen += 1;
    let liftedAll = false;
    return () => {
      if (liftedAll) return;
      liftedAll = true;
      allPersistedWritesFrozen -= 1;
    };
  }
  const list = [...new Set(keys)];
  for (const key of list) frozenPersistedKeys.set(key, (frozenPersistedKeys.get(key) ?? 0) + 1);
  let lifted = false;
  return () => {
    if (lifted) return;
    lifted = true;
    for (const key of list) {
      const count = (frozenPersistedKeys.get(key) ?? 1) - 1;
      if (count > 0) frozenPersistedKeys.set(key, count);
      else frozenPersistedKeys.delete(key);
    }
  };
}

/** True while writes of key are frozen; skipped writes are logged once per key. */
export function isPersistedWriteFrozen(key: string): boolean {
  const frozen = allPersistedWritesFrozen > 0 || frozenPersistedKeys.has(key);
  if (frozen && !reportedFrozenKeys.has(key)) {
    reportedFrozenKeys.add(key);
    // eslint-disable-next-line no-console
    console.debug(`[persistent-store] write of ${key} skipped: frozen after a backup restore`);
  }
  return frozen;
}

/** Lifts every freeze; tests only (the app lifts a full freeze by reloading). */
export function clearPersistedWriteFreezesForTests(): void {
  allPersistedWritesFrozen = 0;
  frozenPersistedKeys.clear();
}

/** The current freeze, for tests. */
export function persistedWriteFreezeState(): { all: boolean; keys: string[] } {
  return { all: allPersistedWritesFrozen > 0, keys: [...frozenPersistedKeys.keys()].sort() };
}

/**
 * Keys whose localStorage copy is read synchronously before native hydration
 * (first paint). Desktop saves keep a browser snapshot only for these; every
 * other key is written natively only. Keep in sync with the readers noted.
 */
const DESKTOP_BROWSER_SNAPSHOT_KEYS = new Set<string>([
  // settings-persistence readBrowserSnapshot: ui-theme, terminal-theme, i18n locale in main.ts.
  "app_settings_v2",
  // stores/workbench.ts loadWorkspaces / loadActiveWorkspaceId.
  "app_workspaces_v1",
  "app_active_workspace_v1",
  // stores/terminal-workspace.ts readPersistedWorkspace.
  "app_terminal_workspace_v1",
  // stores/ssh-connections.ts readProfiles.
  "app_ssh_connections_v1",
  // components/layout/AppSidebar.vue loadOrder.
  "app_sidebar_order_v2",
  // stores/ai-assistant-shell.ts drawer width, layout mode and placement,
  // stores/ai-assistant.ts conversation sidebar width.
  "app_ai_drawer_width",
  "app_ai_layout_mode",
  "app_ai_assistant_placement",
  "app_ai_conversation_sidebar_width",
  // views/HttpWorkspaceView.vue initial request pane height.
  "app_http_request_height",
  // views/DatabaseWorkspaceView.vue loadStoredArray.
  "app_db_scripts",
  "app_db_query_history",
]);
const clearedDesktopBrowserSnapshots = new Set<string>();

/** Registers keys (e.g. resizable pane sizes) that are read synchronously from localStorage. */
export function registerDesktopBrowserSnapshotKeys(...keys: string[]): void {
  for (const key of keys) DESKTOP_BROWSER_SNAPSHOT_KEYS.add(key);
}

export function writesDesktopBrowserSnapshot(key: string): boolean {
  return DESKTOP_BROWSER_SNAPSHOT_KEYS.has(key);
}

export interface PersistedBatchSet {
  key: string;
  value: unknown;
  /** Defaults to value when no sanitized browser-specific snapshot is needed. */
  browserSnapshot?: unknown;
  writeBrowserSnapshot?: boolean;
  /**
   * Desktop-only: also keep a localStorage copy after the native write. Defaults to
   * writesDesktopBrowserSnapshot(key); otherwise stale browser copies are removed.
   */
  writeBrowserSnapshotInDesktop?: boolean;
}

export interface PersistedBatch {
  sets?: readonly PersistedBatchSet[];
  deleteKeys?: readonly string[];
  deleteAiScopeIds?: readonly string[];
  deleteAllAiHistory?: boolean;
}

interface StoreBatchOperation {
  type: "set" | "delete" | "delete_ai_scope" | "delete_all_ai";
  key: string;
  value?: unknown;
}

function serializePersistedValue(value: unknown): string {
  const serialized = JSON.stringify(value);
  if (serialized === undefined) throw new Error("persisted_value_not_serializable");
  return serialized;
}

function writeSerializedBrowserSnapshot(key: string, value: string): void {
  clearedDesktopBrowserSnapshots.delete(key);
  try {
    window.localStorage.setItem(key, value);
  } catch {
    // Persistence is optional in browser previews with unavailable localStorage.
  }
}

function writeBrowserSnapshot(key: string, value: unknown): void {
  writeSerializedBrowserSnapshot(key, serializePersistedValue(value));
}

/** Best-effort browser snapshot write for sibling stores; never throws. */
export function writePersistedBrowserSnapshot(key: string, value: unknown): void {
  if (isPersistedWriteFrozen(key)) return;
  try {
    const serialized = JSON.stringify(value);
    if (serialized !== undefined) writeSerializedBrowserSnapshot(key, serialized);
  } catch {
    // Browser storage is optional.
  }
}

function deleteBrowserSnapshot(key: string): void {
  clearedDesktopBrowserSnapshots.delete(key);
  try {
    window.localStorage.removeItem(key);
  } catch {
    // Optional browser fallback.
  }
}

/** Removes a stale browser copy after a native save; repeated saves skip the sync call. */
export function clearDesktopBrowserSnapshot(key: string): void {
  if (clearedDesktopBrowserSnapshots.has(key) || isPersistedWriteFrozen(key)) return;
  deleteBrowserSnapshot(key);
  clearedDesktopBrowserSnapshots.add(key);
}

function isPlainRecord(value: unknown): value is Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return false;
  const prototype = Object.getPrototypeOf(value);
  return prototype === Object.prototype || prototype === null;
}

function parseLegacyImportedValue(value: unknown): unknown {
  if (typeof value !== "string") return value;
  try {
    return JSON.parse(value) as unknown;
  } catch {
    return value;
  }
}

export function preparePersistedImportBatch(payload: unknown): PersistedBatch {
  if (!isPlainRecord(payload)) throw new Error("invalid_config_import_payload");
  const entries = Object.entries(payload);
  if (entries.some(([key]) => !key.startsWith("app_"))) {
    throw new Error("invalid_config_import_key");
  }
  return {
    sets: entries.map(([key, rawValue]) => {
      const value = parseLegacyImportedValue(rawValue);
      const encrypted = typeof value === "string" && value.startsWith("enc:v2:");
      return {
        key,
        value,
        browserSnapshot: value,
        writeBrowserSnapshot: !encrypted,
      };
    }),
  };
}

/**
 * Persist module state through the Go backend in the desktop app, while retaining a JSON
 * localStorage fallback for browser preview.
 */
function parseBrowserValue<T>(key: string): T | null {
  try {
    const raw = window.localStorage.getItem(key);
    return raw ? (JSON.parse(raw) as T) : null;
  } catch {
    return null;
  }
}

const MAX_NATIVE_READ_BATCH = 500;
let pendingNativeReads = new Map<string, Array<(value: unknown) => void>>();
let nativeReadFlushScheduled = false;

async function flushNativeReadChunk(
  chunk: Array<[string, Array<(value: unknown) => void>]>
): Promise<void> {
  try {
    if (chunk.length === 1) {
      const [key, resolvers] = chunk[0];
      const value = await invokeBackend<unknown>("store_get", { key });
      for (const resolve of resolvers) resolve(value);
      return;
    }
    const values = await invokeBackend<Record<string, unknown> | null>("store_get_many", {
      keys: chunk.map(([key]) => key),
    });
    for (const [key, resolvers] of chunk) {
      for (const resolve of resolvers) resolve(values?.[key] ?? null);
    }
  } catch {
    // Browser storage keeps the module usable if the native store is unavailable.
    for (const [, resolvers] of chunk) {
      for (const resolve of resolvers) resolve(null);
    }
  }
}

function flushNativeReads(): void {
  nativeReadFlushScheduled = false;
  const entries = [...pendingNativeReads.entries()];
  pendingNativeReads = new Map();
  for (let start = 0; start < entries.length; start += MAX_NATIVE_READ_BATCH) {
    void flushNativeReadChunk(entries.slice(start, start + MAX_NATIVE_READ_BATCH));
  }
}

/** Coalesces native reads issued in the same tick (e.g. startup hydration) into one IPC. */
function loadNativeValue(key: string): Promise<unknown> {
  return new Promise((resolve) => {
    const resolvers = pendingNativeReads.get(key);
    if (resolvers) resolvers.push(resolve);
    else pendingNativeReads.set(key, [resolve]);
    if (!nativeReadFlushScheduled) {
      nativeReadFlushScheduled = true;
      queueMicrotask(flushNativeReads);
    }
  });
}

export async function loadPersistedValue<T>(key: string): Promise<T | null> {
  if (isDesktopRuntime()) {
    const value = await loadNativeValue(key);
    if (value !== null && value !== undefined) return value as T;
  }
  return parseBrowserValue<T>(key);
}

export async function applyPersistedBatch(
  batch: PersistedBatch,
  options: {
    /** Only the backup restore batch itself writes through a freeze. */
    ignoreWriteFreeze?: boolean;
  } = {},
): Promise<void> {
  const writable = (key: string) => options.ignoreWriteFreeze === true || !isPersistedWriteFrozen(key);
  const sets = (batch.sets ?? []).filter(({ key }) => writable(key));
  const deleteKeys = (batch.deleteKeys ?? []).filter(writable);
  const deleteAiScopeIds = [...(batch.deleteAiScopeIds ?? [])];
  const deleteAllAiHistory = batch.deleteAllAiHistory === true;
  const operations: StoreBatchOperation[] = [
    ...sets.map(({ key, value }) => ({ type: "set" as const, key, value })),
    ...deleteKeys.map((key) => ({ type: "delete" as const, key })),
    ...deleteAiScopeIds.map((scopeId) => ({ type: "delete_ai_scope" as const, key: scopeId })),
    ...(deleteAllAiHistory ? [{ type: "delete_all_ai" as const, key: "*" }] : []),
  ];
  for (const { value } of sets) serializePersistedValue(value);
  const desktopRuntime = isDesktopRuntime();
  const browserSnapshots = sets.map(
    ({
      key,
      value,
      browserSnapshot = value,
      writeBrowserSnapshot: shouldWrite = true,
      writeBrowserSnapshotInDesktop = writesDesktopBrowserSnapshot(key),
    }) => ({
      key,
      value: shouldWrite && (!desktopRuntime || writeBrowserSnapshotInDesktop)
        ? serializePersistedValue(browserSnapshot)
        : undefined,
      removeAfterNativeSave: desktopRuntime && shouldWrite && !writeBrowserSnapshotInDesktop,
    })
  );

  await enqueuePersistedWrite(async () => {
    if (desktopRuntime && operations.length > 0) {
      await invokeBackend<void>("store_apply_batch", { operations });
    }
    for (const { key, value, removeAfterNativeSave } of browserSnapshots) {
      if (value !== undefined) writeSerializedBrowserSnapshot(key, value);
      else if (removeAfterNativeSave) clearDesktopBrowserSnapshot(key);
    }
    for (const key of deleteKeys) deleteBrowserSnapshot(key);
  });
}

export async function savePersistedValueWithBrowserSnapshot(
  key: string,
  value: unknown,
  browserSnapshot: unknown,
  options: { writeBrowserSnapshotInDesktop?: boolean } = {},
): Promise<void> {
  if (isPersistedWriteFrozen(key)) return;
  const desktopRuntime = isDesktopRuntime();
  if (!desktopRuntime) {
    writeBrowserSnapshot(key, browserSnapshot);
    return;
  }
  const keepBrowserSnapshot =
    options.writeBrowserSnapshotInDesktop ?? writesDesktopBrowserSnapshot(key);
  if (keepBrowserSnapshot) writeBrowserSnapshot(key, browserSnapshot);

  await enqueuePersistedWrite(async () => {
    if (isPersistedWriteFrozen(key)) return;
    try {
      await invokeBackend<void>("store_set", { key, value });
      if (!keepBrowserSnapshot) clearDesktopBrowserSnapshot(key);
    } catch {
      // Native storage failed: retain the latest value as an emergency browser fallback.
      if (!keepBrowserSnapshot) writeBrowserSnapshot(key, browserSnapshot);
    }
  });
}

export function savePersistedValue(
  key: string,
  value: unknown,
  options: { writeBrowserSnapshotInDesktop?: boolean } = {},
): Promise<void> {
  return savePersistedValueWithBrowserSnapshot(key, value, value, options);
}

export async function deletePersistedValue(key: string): Promise<void> {
  if (isPersistedWriteFrozen(key)) return;
  deleteBrowserSnapshot(key);
  if (!isDesktopRuntime()) return;

  await enqueuePersistedWrite(async () => {
    if (isPersistedWriteFrozen(key)) return;
    try {
      await invokeBackend<void>("store_del", { key });
    } catch {
      // Preserve the existing best-effort single-key persistence contract.
    }
  });
}

/**
 * Returns a portable typed snapshot for the Settings import/export workflow.
 * With strict (backups), a failed or malformed native load in the desktop app
 * throws instead of falling back to the partial browser copy.
 */
export async function loadAllPersistedValues(
  options: { strict?: boolean } = {},
): Promise<Record<string, unknown>> {
  if (isDesktopRuntime()) {
    try {
      const values = await invokeBackend<unknown>("store_load_all", {});
      if (values && typeof values === "object" && !Array.isArray(values)) {
        return values as Record<string, unknown>;
      }
    } catch (error) {
      if (options.strict) throw error;
      // Browser values below remain a usable fallback.
    }
    if (options.strict) throw new Error("store_load_all returned no values");
  }

  const values: Record<string, unknown> = {};
  try {
    for (let index = 0; index < window.localStorage.length; index += 1) {
      const key = window.localStorage.key(index);
      if (!key) continue;
      values[key] = parseBrowserValue<unknown>(key);
    }
  } catch {
    // Browser storage is optional in preview mode.
  }
  return values;
}
