/**
 * Runtime of UI language packs (contributes.localizations): the stored
 * selection (`uiLocale` / `uiLocalePlugin` settings), the available packs from
 * the contribution snapshot, and installing the validated message and plugin
 * title overlays. See docs/architecture/plugin-system.md, "localizations".
 *
 * - Packs never activate on their own: only the stored selection applies.
 * - The previous overlay stays until the new one is fetched and validated;
 *   a pack that disappears (disable, uninstall, error) is removed at once, the
 *   base language (zh | en) is used and the stored selection is kept, so
 *   Settings can show a notice.
 * - Startup waits at most 800 ms for the selected pack; there is no cache.
 * - Browser preview has no plugin host: a stored pack is reported unavailable.
 */
import { computed, shallowRef } from "vue";
import {
  LOCALIZATIONS_CONTRIBUTION_POINT,
  buildOverlay,
  droppedTotal,
  findLocalePack,
  localeCoverage,
  parseLocalePackContributions,
  parsePluginTitlesAsset,
  translatableKeyCount,
  type LocalePackEntry,
  type LocalePackIssue,
  type LocalePackSelection,
  type OverlayDropCounts,
} from "@/domain/i18n/locale-pack";
import { normalizeUiLocale, normalizeUiLocalePlugin } from "@/domain/plugins/contribution-settings";
import { pointContributions, type PointContribution } from "@/domain/plugins/contributions";
import { currentLocaleOverlay, loadBaseMessages, setLocaleOverlay } from "@/i18n/messages";
import { contributionAssetBytes, releasePluginAssets } from "@/services/contribution-assets";
import { setPluginTitleOverlay } from "@/services/plugin-titles";
import { readSettingsSnapshot, updateSettingsRuntimeSnapshot } from "@/services/settings-persistence";
import { isDesktopRuntime } from "@/services/desktop-bridge";

export const LOCALE_PACK_BOOT_WAIT_MS = 800;

export type LocalePackStatus =
  | { kind: "none" }
  | { kind: "loading"; selection: LocalePackSelection }
  | { kind: "unavailable"; selection: LocalePackSelection; reason: "missing" | "failed" }
  | {
      kind: "active";
      selection: LocalePackSelection;
      pack: LocalePackEntry;
      coverage: number;
      translated: number;
      dropped: OverlayDropCounts;
      droppedCount: number;
    };

const packsRef = shallowRef<readonly LocalePackEntry[]>([]);
const issuesRef = shallowRef<readonly LocalePackIssue[]>([]);
const selectionRef = shallowRef<LocalePackSelection | null>(null);
const statusRef = shallowRef<LocalePackStatus>({ kind: "none" });
// True once a contribution snapshot (or a boot fetch) reported the packs;
// before that a stored selection is "loading", not "unavailable".
let packsKnown = false;
let appliedSource = "";
let reconcileRequest = 0;
let reconcileScheduled = false;

/** Available packs, issues, the stored selection and the apply status (read-only). */
export const localePackState = {
  packs: computed(() => packsRef.value),
  issues: computed(() => issuesRef.value),
  selection: computed(() => selectionRef.value),
  status: computed(() => statusRef.value),
};

function sameSelection(a: LocalePackSelection | null, b: LocalePackSelection | null) {
  return a?.tag === b?.tag && a?.pluginId === b?.pluginId;
}

/** The stored selection from a settings record, or null (syntax only). */
export function localePackSelectionFromSettings(settings: { uiLocale?: unknown; uiLocalePlugin?: unknown } | undefined): LocalePackSelection | null {
  const tag = normalizeUiLocale(settings?.uiLocale);
  if (!tag) return null;
  const pluginId = normalizeUiLocalePlugin(settings?.uiLocalePlugin);
  return pluginId ? { tag, pluginId } : { tag };
}

/** Sets the stored selection (null uses the base language) and applies it. */
export function setLocalePackSelection(selection: LocalePackSelection | null): void {
  const next = selection ? { tag: selection.tag, ...(selection.pluginId ? { pluginId: selection.pluginId } : {}) } : null;
  if (sameSelection(selectionRef.value, next)) return;
  selectionRef.value = next;
  scheduleLocalePackReconcile();
}

/** Replaces the available packs (the localizations consumer calls this). */
export function setLocalePacks(packs: readonly LocalePackEntry[], issues: readonly LocalePackIssue[] = []): void {
  packsRef.value = packs;
  issuesRef.value = issues;
  packsKnown = true;
  scheduleLocalePackReconcile();
}

export function applyLocalePackContributions(contributions: PointContribution[]): () => void {
  const { packs, issues } = parseLocalePackContributions(contributions);
  setLocalePacks(packs, issues);
  const pluginIds = new Set(packs.map((pack) => pack.pluginId));
  return () => {
    setLocalePacks([]);
    for (const pluginId of pluginIds) releasePluginAssets(pluginId);
  };
}

/**
 * Coalesces reconciles within one microtask, so a consumer dispose followed
 * by the next apply never flashes the base language.
 */
export function scheduleLocalePackReconcile(): void {
  if (reconcileScheduled) return;
  reconcileScheduled = true;
  queueMicrotask(() => {
    reconcileScheduled = false;
    void reconcileLocalePack();
  });
}

function clearOverlay() {
  appliedSource = "";
  setLocaleOverlay(null);
  setPluginTitleOverlay(null);
}

async function assetJSON(pack: LocalePackEntry, key: string): Promise<unknown> {
  const asset = await contributionAssetBytes(pack.pluginId, LOCALIZATIONS_CONTRIBUTION_POINT, key);
  if (!asset) throw new Error("language pack asset unavailable");
  return JSON.parse(new TextDecoder().decode(asset.bytes)) as unknown;
}

/** Applies the stored selection to the current packs. Resolves when done (or superseded). */
export async function reconcileLocalePack(): Promise<void> {
  const request = ++reconcileRequest;
  const selection = selectionRef.value;
  if (!selection) {
    clearOverlay();
    statusRef.value = { kind: "none" };
    return;
  }
  const pack = findLocalePack(packsRef.value, selection);
  if (!pack) {
    clearOverlay();
    statusRef.value = packsKnown || !isDesktopRuntime()
      ? { kind: "unavailable", selection, reason: "missing" }
      : { kind: "loading", selection };
    return;
  }
  const source = `${pack.pluginId}\n${pack.locale}\n${pack.messages?.asset ?? ""}\n${pack.pluginTitles?.asset ?? ""}`;
  const current = statusRef.value;
  if (source === appliedSource && currentLocaleOverlay()?.tag === pack.locale && current.kind === "active") {
    if (!sameSelection(current.selection, selection)) statusRef.value = { ...current, selection };
    return;
  }
  if (statusRef.value.kind !== "active") statusRef.value = { kind: "loading", selection };
  try {
    const [catalogue, messages, titles] = await Promise.all([
      loadBaseMessages("en"),
      pack.messages ? assetJSON(pack, pack.messages.asset) : Promise.resolve(undefined),
      pack.pluginTitles ? assetJSON(pack, pack.pluginTitles.asset) : Promise.resolve(undefined),
    ]);
    if (request !== reconcileRequest) return;
    const overlay = buildOverlay(catalogue, messages ?? {});
    const pluginTitles = parsePluginTitlesAsset(titles, pack.pluginId);
    appliedSource = source;
    setLocaleOverlay({ tag: pack.locale, pluginId: pack.pluginId, messages: overlay.messages });
    setPluginTitleOverlay(pluginTitles.size ? pluginTitles : null);
    statusRef.value = {
      kind: "active",
      selection,
      pack,
      coverage: localeCoverage(overlay.messages.size, translatableKeyCount(catalogue)),
      translated: overlay.messages.size,
      dropped: overlay.dropped,
      droppedCount: droppedTotal(overlay.dropped),
    };
  } catch {
    if (request !== reconcileRequest) return;
    clearOverlay();
    statusRef.value = { kind: "unavailable", selection, reason: "failed" };
  }
}

/**
 * Clears the language pack, for example for a locale change made by the AI
 * settings tool: AI may switch the base language but can never select (or
 * keep) a pack. `persist` also drops `uiLocale`/`uiLocalePlugin` from the
 * runtime settings snapshot, so the next settings write stores the removal.
 */
export function clearLocalePackSelection(options: { persist?: boolean } = {}): void {
  selectionRef.value = null;
  reconcileRequest++;
  clearOverlay();
  statusRef.value = { kind: "none" };
  if (options.persist) {
    const snapshot: Record<string, unknown> = { ...readSettingsSnapshot() };
    if ("uiLocale" in snapshot || "uiLocalePlugin" in snapshot) {
      delete snapshot.uiLocale;
      delete snapshot.uiLocalePlugin;
      updateSettingsRuntimeSnapshot(snapshot);
    }
  }
}

/**
 * Startup: applies the saved pack before the app mounts, waiting at most
 * 800 ms. On timeout the app mounts in the base language and switches when
 * the pack is ready. Browser preview reports the pack unavailable.
 */
export async function applySavedLocaleOverlay(settings: { uiLocale?: unknown; uiLocalePlugin?: unknown }): Promise<void> {
  const selection = localePackSelectionFromSettings(settings);
  selectionRef.value = selection;
  if (!selection) return;
  if (!isDesktopRuntime()) {
    await reconcileLocalePack();
    return;
  }
  const load = (async () => {
    const { fetchPluginContributions } = await import("@/services/plugin-service");
    const snapshot = await fetchPluginContributions([LOCALIZATIONS_CONTRIBUTION_POINT]);
    // The contribution sync may already have delivered newer data.
    if (!packsKnown) {
      const parsed = parseLocalePackContributions(pointContributions(snapshot, LOCALIZATIONS_CONTRIBUTION_POINT));
      packsRef.value = parsed.packs;
      issuesRef.value = parsed.issues;
      packsKnown = true;
    }
    await reconcileLocalePack();
  })().catch(() => undefined);
  let timer: ReturnType<typeof setTimeout> | undefined;
  await Promise.race([load, new Promise<void>((resolve) => { timer = setTimeout(resolve, LOCALE_PACK_BOOT_WAIT_MS); })]);
  if (timer) clearTimeout(timer);
}
