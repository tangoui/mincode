import {
  clearDesktopBrowserSnapshot,
  enqueuePersistedWrite,
  writePersistedBrowserSnapshot as writeBrowserSnapshot,
  writesDesktopBrowserSnapshot,
} from "@/services/persistent-store";
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";

const SECURE_VALUE_PREFIX = "enc:v2:";

function parseBrowserValue<T>(key: string): T | null {
  try {
    const raw = window.localStorage.getItem(key);
    return raw ? (JSON.parse(raw) as T) : null;
  } catch {
    return null;
  }
}

async function sealAndStore(key: string, value: unknown): Promise<void> {
  const json = JSON.stringify(value);
  if (json === undefined) throw new Error(`无法序列化安全存储值: ${key}`);
  const cipher = await invokeBackend<string>("seal_object", { json });
  // Re-checked: a restore may have frozen the key while sealing.
  await invokeBackend<void>("store_set", { key, value: cipher });
}

/**
 * Loads an encrypted IPC object. Legacy plain values are encrypted and
 * written back before they are returned, so migration cannot silently remain
 * incomplete after a successful read.
 */
export async function loadSecurePersistedValue<T>(key: string): Promise<T | null> {
  if (isDesktopRuntime()) {
    const stored = await invokeBackend<unknown>("store_get", { key });
    if (typeof stored === "string" && stored.startsWith(SECURE_VALUE_PREFIX)) {
      const json = await invokeBackend<string>("unseal_object", { cipher: stored });
      return JSON.parse(json) as T;
    }
    if (stored !== null && stored !== undefined) {
      await sealAndStore(key, stored);
      return stored as T;
    }
    return null;
  }
  return parseBrowserValue<T>(key);
}

/** Prepares an encrypted value without writing either native or browser storage. */
export async function prepareSecurePersistedValue(value: unknown): Promise<unknown> {
  if (!isDesktopRuntime()) return value;
  return invokeBackend<string>("seal_object", { json: JSON.stringify(value) });
}

/**
 * Persists the complete value as one encrypted object in IPC. Browser
 * storage receives only the caller-provided non-sensitive snapshot, and on
 * desktop only for keys read synchronously before hydration (or as a
 * fallback when the native write fails).
 *
 * By default a native failure is swallowed (best-effort automatic
 * persistence). With `strict`, the browser fallback is still written but the
 * error is rethrown so explicit/security-relevant saves never report success
 * for a value that will not survive a restart.
 */
export async function saveSecurePersistedValue(
  key: string,
  value: unknown,
  browserSnapshot: unknown,
  options: { strict?: boolean } = {}
): Promise<void> {
  const desktopRuntime = isDesktopRuntime();
  const keepBrowserSnapshot = !desktopRuntime || writesDesktopBrowserSnapshot(key);
  if (keepBrowserSnapshot) writeBrowserSnapshot(key, browserSnapshot);
  if (!desktopRuntime) return;
  await enqueuePersistedWrite(async () => {
    try {
      await sealAndStore(key, value);
      if (!keepBrowserSnapshot) clearDesktopBrowserSnapshot(key);
    } catch (error) {
      if (!keepBrowserSnapshot) writeBrowserSnapshot(key, browserSnapshot);
      if (options.strict) throw error;
    }
  });
}
