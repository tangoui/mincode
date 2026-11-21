/**
 * Pure logic of the plugin section of the system tray menu (`menus.tray`,
 * host 0.3.0). Loaded on demand: services/plugin-tray.ts (once a snapshot has
 * tray entries), App.vue (on a plugin-command navigation) and the plugin
 * manager's install preview.
 *
 * The frontend decides which declared (plugin, command) references are shown
 * — the command is registered (plugin enabled and effective in this
 * workspace) and its own `when` holds against the plugin's context keys; none
 * while the app is locked — and sends only references through
 * `plugin_host_tray_set` (backend/plugin/tray_menu.go). The host
 * takes every label from the manifest. A tray click arrives as a
 * `desktop:navigate` target of kind `plugin-command`.
 */
import { evaluateWhen, type PluginContextValues } from "@/domain/commands/when";
import { resolvePluginTitle, type PluginTitle } from "@/domain/plugins/title";

/** One declared tray item: a command of the plugin's own contributes.commands. */
export interface TrayEntry {
  pluginId: string;
  command: string;
}

/** Plugin context keys set through host.context.set, per plugin. */
export type PluginContextMap = Readonly<Record<string, PluginContextValues>>;

/** Request of the `plugin_host_tray_set` core command. */
export interface TraySetArgs {
  locale: string[];
  items: TrayEntry[];
}

/**
 * Mirrors trayItemsMax of tray_menu.go (512 plugins × trayMaxPerPlugin): one
 * request carries every visible candidate, and the host picks the first
 * trayPluginsMax plugins by name.
 */
export const TRAY_ITEMS_MAX = 2048;
/** Changes within this window are sent as one request. */
export const TRAY_SYNC_DELAY_MS = 200;
/** A failed request is retried after these delays, then left until the next change. */
export const TRAY_SYNC_RETRY_MS = [1000, 2000] as const;

/** Mirrors pluginTargetIDPattern of backend/desktop/desktop.go (the plugin ID of a plugin-command target). */
const PLUGIN_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;
/** Mirrors pluginCommandIDPattern of desktop/plugin_tray.go = contributionIDPattern of plugin/manifest.go (':' allowed). */
const COMMAND_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/;

/**
 * References to send: entries whose command is registered and whose `when`
 * holds, in entry order, without repeats. Every visible candidate is sent (up
 * to TRAY_ITEMS_MAX); the host chooses which plugins fit. Locked: none.
 */
export function trayItemsToSync(
  entries: readonly TrayEntry[],
  commands: readonly { pluginId: string; id: string; when?: string }[],
  contexts: PluginContextMap,
  locked: boolean,
): TrayEntry[] {
  if (locked) return [];
  const seen = new Set<string>();
  const items: TrayEntry[] = [];
  for (const { pluginId, command } of entries) {
    const key = `${pluginId}\u0000${command}`;
    if (items.length >= TRAY_ITEMS_MAX || seen.has(key)) continue;
    const registered = commands.find((candidate) => candidate.pluginId === pluginId && candidate.id === command);
    if (!registered || (registered.when && !evaluateWhen(registered.when, contexts[pluginId] ?? {}))) continue;
    seen.add(key);
    items.push({ pluginId, command });
  }
  return items;
}

export interface TraySyncDeps {
  invoke(args: TraySetArgs): Promise<unknown>;
  setTimer(callback: () => void, ms: number): unknown;
  clearTimer(handle: unknown): void;
}

export interface TraySync {
  /** Schedules a sync of this set; an unchanged set is not sent again. */
  update(locale: readonly string[], items: readonly TrayEntry[]): void;
  /** Schedules the empty set (cleanup). */
  clear(locale: readonly string[]): void;
  /** Forgets the last sent set, so the next update is sent even when unchanged. */
  resync(): void;
}

/**
 * Debounced, deduplicated sync. Requests are serialized. A failed request is
 * forgotten and retried after TRAY_SYNC_RETRY_MS unless a newer set was
 * scheduled meanwhile; the next change always sends again.
 */
export function createTraySync(deps: TraySyncDeps): TraySync {
  let lastSent: string | undefined;
  let timer: unknown;
  let pendingArgs: TraySetArgs | undefined;
  let chain: Promise<unknown> = Promise.resolve();

  function schedule(args: TraySetArgs, delay: number, attempt: number) {
    pendingArgs = args;
    if (timer !== undefined) deps.clearTimer(timer);
    timer = deps.setTimer(() => flush(attempt), delay);
  }

  function flush(attempt: number) {
    timer = undefined;
    const args = pendingArgs;
    pendingArgs = undefined;
    if (!args) return;
    const key = JSON.stringify(args);
    if (key === lastSent) return;
    lastSent = key;
    chain = chain
      .then(() => deps.invoke(args))
      .catch(() => {
        if (lastSent !== key) return;
        lastSent = undefined;
        const delay = TRAY_SYNC_RETRY_MS[attempt];
        // A newer set already waiting wins; otherwise retry this one.
        if (delay !== undefined && timer === undefined) schedule(args, delay, attempt + 1);
      });
  }

  function update(locale: readonly string[], items: readonly TrayEntry[]) {
    schedule({ locale: [...locale], items: items.map(({ pluginId, command }) => ({ pluginId, command })) }, TRAY_SYNC_DELAY_MS, 0);
  }

  return {
    update,
    clear: (locale) => update(locale, []),
    resync: () => {
      lastSent = undefined;
    },
  };
}

/** Parses the value of a `plugin-command` navigation target: JSON `{"pluginId","commandId"}` and nothing else. */
export function parseDesktopPluginCommandTarget(value: unknown): { pluginId: string; commandId: string } | undefined {
  if (typeof value !== "string" || value.length > 1024) return undefined;
  let parsed: unknown;
  try {
    parsed = JSON.parse(value);
  } catch {
    return undefined;
  }
  if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) return undefined;
  const record = parsed as Record<string, unknown>;
  const keys = Object.keys(record);
  const { pluginId, commandId } = record;
  if (keys.length !== 2 || typeof pluginId !== "string" || typeof commandId !== "string") return undefined;
  if (!PLUGIN_ID_PATTERN.test(pluginId) || !COMMAND_ID_PATTERN.test(commandId)) return undefined;
  return { pluginId, commandId };
}

/**
 * Dispatches a tray click (App.vue `desktop:navigate`). Ignored while the app
 * is locked or when the value is malformed; otherwise the command runs from
 * the `tray` origin (the runner looks it up again). Returns whether it ran.
 */
export function handlePluginCommandTarget(
  value: unknown,
  options: { locked: boolean; run(origin: "tray", pluginId: string, commandId: string): unknown },
): boolean {
  if (options.locked) return false;
  const target = parseDesktopPluginCommandTarget(value);
  if (!target) return false;
  void options.run("tray", target.pluginId, target.commandId);
  return true;
}

/** Titles of the manifest's tray items for the install preview (manifest command titles; the id when missing). */
export function trayPreviewTitles(contributes: unknown, locale: readonly string[]): string[] {
  const record = objectValue(contributes);
  const tray = objectValue(record?.menus)?.tray;
  if (!Array.isArray(tray)) return [];
  const commands = Array.isArray(record?.commands) ? record.commands : [];
  const titles: string[] = [];
  for (const raw of tray) {
    const command = objectValue(raw)?.command;
    if (typeof command !== "string" || !command) continue;
    const declared = commands.map(objectValue).find((candidate) => candidate?.id === command);
    if (!declared || typeof declared.title !== "string") {
      titles.push(command);
      continue;
    }
    // resolvePluginTitle ignores non-string titleLocalized values.
    const title: PluginTitle = { title: declared.title, titleLocalized: objectValue(declared.titleLocalized) as Record<string, string> | undefined };
    titles.push(resolvePluginTitle(title, locale));
  }
  return titles;
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === "object" && value !== null && !Array.isArray(value) ? (value as Record<string, unknown>) : undefined;
}
