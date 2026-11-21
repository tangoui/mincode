/**
 * Pure logic of the `menus` contribution point: parsing the host snapshot
 * entries, menu context keys, when evaluation, sorting, overflow and the
 * passContext payload. Mirrors backend/plugin/contrib_menus.go and
 * the `menus` entry of plugins/api/contribution-points.json.
 * Nothing here touches IPC, Vue or Monaco.
 */
import type { PointContribution } from "@/domain/plugins/contributions";
import { resolvePluginTitle } from "@/domain/plugins/title";
import {
  evaluateWhenClauses,
  parseWhenExpression,
  type PluginContextValue,
  type PluginContextValues,
  type WhenClause,
} from "@/domain/commands/when";

export const MENU_CONTRIBUTION_POINT = "menus";

/** Mirrors `points.menus.keys` of the contract. */
export const PLUGIN_MENU_IDS = [
  "editor/context",
  "sql/editor/context",
  "project/tree/context",
  "workbench/tab/context",
  "terminal/context",
  "git/changes/context",
  "git/commit/context",
  "database/tree/context",
  "ssh/connection/context",
  "sftp/file/context",
  "redis/key/context",
  "http/request/context",
  "tray",
  "commandPalette",
] as const;
export type PluginMenuId = (typeof PLUGIN_MENU_IDS)[number];
/**
 * Menus rendered by the host UI. commandPalette only adjusts palette entries
 * and tray is the native system tray menu (host 0.3.0): neither has a UI
 * target, host context keys or passContext.
 */
export type PluginContextMenuId = Exclude<PluginMenuId, "commandPalette" | "tray">;

/** Mirrors `points.menus.limits` of the contract. */
export const MENU_LIMITS = {
  whenMaxLength: 512,
  orderMin: 0,
  orderMax: 1000,
  passContextMaxBytes: 65536,
  selectionMaxBytes: 32768,
  /** git/commit/context `subject`: truncated and flagged with `truncated: true`. */
  commitSubjectMaxBytes: 1024,
  /** sftp/file/context `path`: refused above the limit, never cut. */
  remotePathMaxBytes: 4096,
  /** redis/key/context `key`: refused above the limit (a cut key is another key). */
  redisKeyMaxBytes: 4096,
  /** http/request/context `request.url` (UTF-8 bytes, like httpCollections): refused above the limit. */
  urlMaxLength: 2048,
  /** tray items per plugin (the host rejects the fifth at install time). */
  trayMaxPerPlugin: 4,
  /** plugins shown in the tray menu (sorted by name; the rest are omitted). */
  trayPluginsMax: 8,
  /** tray labels (plugin names and command titles) are cut to this many characters. */
  trayLabelMaxRunes: 48,
} as const;

/** Plugin items shown directly in one menu; the rest go under "More plugin actions". */
export const MENU_VISIBLE_PLUGIN_ITEMS = 12;

const GROUP_PATTERN = /^[a-z0-9][a-z0-9_.-]{0,31}$/;
const COMMAND_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/;

/** Host context keys provided by each menu (plus `platform` everywhere). */
export const MENU_HOST_CONTEXT_KEYS: Readonly<Record<PluginContextMenuId, readonly string[]>> = {
  "editor/context": ["editor.kind", "editor.languageId", "editor.hasSelection", "editor.readonly"],
  "sql/editor/context": ["editor.kind", "editor.languageId", "editor.hasSelection", "editor.readonly", "database.kind"],
  "project/tree/context": ["resource.kind", "resource.extname", "resource.isProjectRoot"],
  "workbench/tab/context": ["tab.view", "tab.pinned"],
  "terminal/context": ["terminal.kind", "terminal.selection"],
  "git/changes/context": ["git.group", "git.status", "git.extname"],
  "git/commit/context": ["git.isMerge", "git.hasRefs"],
  "database/tree/context": ["database.kind", "database.objectType", "database.tableKind", "database.connected"],
  "ssh/connection/context": ["ssh.status", "ssh.authType", "ssh.hasJumpHost"],
  "sftp/file/context": ["sftp.kind", "sftp.extname", "sftp.protocol"],
  "redis/key/context": ["redis.type", "redis.hasTtl"],
  "http/request/context": ["http.method", "http.scheme"],
};

/** Mirrors menuReservedContextPrefixes of contrib_menus.go (`platform` is handled as an exact key). */
const RESERVED_PREFIXES = ["editor.", "resource.", "tab.", "terminal.", "database.", "git.", "ssh.", "sftp.", "redis.", "http."];
const PLATFORM_KEY = "platform";

/** Host-owned keys: plugin context (host.context.set) never overrides them in menus. */
export function isReservedMenuContextKey(key: string): boolean {
  return key === PLATFORM_KEY || RESERVED_PREFIXES.some((prefix) => key.startsWith(prefix));
}

export function isPluginMenuId(value: unknown): value is PluginMenuId {
  return typeof value === "string" && (PLUGIN_MENU_IDS as readonly string[]).includes(value);
}

/** True for menus the host renders with a UI target (not commandPalette or tray). */
export function isPluginContextMenuId(value: unknown): value is PluginContextMenuId {
  return isPluginMenuId(value) && value !== "commandPalette" && value !== "tray";
}

/** Items of one host-rendered menu; tray and commandPalette entries never render in an app menu. */
export function contextMenuItems<T extends { menu: PluginMenuId }>(items: readonly T[], menu: PluginMenuId): T[] {
  return isPluginContextMenuId(menu) ? items.filter((item) => item.menu === menu) : [];
}

export interface PluginMenuItem {
  pluginId: string;
  pluginName: string;
  menu: PluginMenuId;
  /** Local command id (contributes.commands[].id of the same plugin). */
  command: string;
  title: string;
  titleLocalized?: Record<string, string>;
  when?: string;
  whenClauses: WhenClause[];
  group: string;
  order: number;
  passContext: boolean;
  hidden: boolean;
}

export interface PluginMenuIssue {
  pluginId: string;
  menu?: string;
  command?: string;
  message: string;
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === "object" && value !== null && !Array.isArray(value) ? (value as Record<string, unknown>) : undefined;
}

function localizedTitles(value: unknown): Record<string, string> | undefined {
  const record = objectValue(value);
  if (!record) return undefined;
  const titles: Record<string, string> = {};
  for (const [locale, title] of Object.entries(record)) {
    if (typeof title === "string" && title.trim()) titles[locale] = title;
  }
  return Object.keys(titles).length ? titles : undefined;
}

/** Validates one untrusted snapshot entry; returns an error message when it must be dropped. */
function parseMenuEntry(contribution: PointContribution, raw: unknown): PluginMenuItem | string {
  const entry = objectValue(raw);
  if (!entry) return "menu item is not an object";
  const menu = entry.menu;
  if (!isPluginMenuId(menu)) return `unknown menu ${JSON.stringify(menu)}`;
  const command = typeof entry.command === "string" ? entry.command.trim() : "";
  if (!COMMAND_PATTERN.test(command)) return "menu item command is invalid";
  const title = typeof entry.title === "string" ? entry.title.trim() : "";
  if (!title) return `menu item ${command} has no title`;
  const group = typeof entry.group === "string" ? entry.group : "";
  if (group && !GROUP_PATTERN.test(group)) return `menu item ${command} group is invalid`;
  const order = entry.order === undefined ? 0 : entry.order;
  if (typeof order !== "number" || !Number.isInteger(order) || order < MENU_LIMITS.orderMin || order > MENU_LIMITS.orderMax) {
    return `menu item ${command} order is invalid`;
  }
  if (menu === "tray" && (entry.when !== undefined || entry.passContext !== undefined || entry.hidden !== undefined)) {
    return `tray item ${command} accepts only command, group and order`;
  }
  const when = typeof entry.when === "string" ? entry.when.trim() : "";
  let whenClauses: WhenClause[] = [];
  if (when) {
    if (!isPluginContextMenuId(menu)) return `commandPalette item ${command} cannot use when`;
    try {
      whenClauses = parseWhenExpression(when);
    } catch (error) {
      return `menu item ${command} when: ${error instanceof Error ? error.message : String(error)}`;
    }
    const allowed = new Set(MENU_HOST_CONTEXT_KEYS[menu]);
    const foreign = whenClauses.find((clause) => clause.key !== PLATFORM_KEY && isReservedMenuContextKey(clause.key) && !allowed.has(clause.key));
    if (foreign) return `menu item ${command} when uses ${foreign.key}, which ${menu} does not provide`;
  }
  const passContext = entry.passContext === true;
  const hidden = entry.hidden === true;
  if (passContext && menu === "commandPalette") return `commandPalette item ${command} cannot use passContext`;
  if (hidden && menu !== "commandPalette") return `menu item ${command} hidden is only supported in commandPalette`;
  const titleLocalized = localizedTitles(entry.titleLocalized);
  return {
    pluginId: contribution.pluginId,
    pluginName: contribution.pluginName,
    menu,
    command,
    title,
    ...(titleLocalized ? { titleLocalized } : {}),
    ...(when ? { when } : {}),
    whenClauses,
    group,
    order,
    passContext,
    hidden,
  };
}

/**
 * Parses the `menus` contributions of the snapshot. Plugins not active in the
 * current workspace contribute nothing; invalid entries are dropped and
 * reported (the host already rejected them at install time).
 */
export function parseMenuContributions(contributions: readonly PointContribution[]): {
  items: PluginMenuItem[];
  issues: PluginMenuIssue[];
} {
  const items: PluginMenuItem[] = [];
  const issues: PluginMenuIssue[] = [];
  for (const contribution of contributions) {
    if (!contribution.activeInWorkspace || contribution.runtime !== "process") continue;
    const seen = new Set<string>();
    let trayItems = 0;
    for (const raw of contribution.point.entries) {
      let parsed = parseMenuEntry(contribution, raw);
      // Mirrors decodeMenuItem: items past trayMaxPerPlugin in one plugin's tray list are dropped.
      if (typeof parsed !== "string" && parsed.menu === "tray" && ++trayItems > MENU_LIMITS.trayMaxPerPlugin) {
        parsed = `menu tray allows at most ${MENU_LIMITS.trayMaxPerPlugin} items`;
      }
      if (typeof parsed === "string") {
        const entry = objectValue(raw);
        issues.push({
          pluginId: contribution.pluginId,
          ...(typeof entry?.menu === "string" ? { menu: entry.menu } : {}),
          ...(typeof entry?.command === "string" ? { command: entry.command } : {}),
          message: parsed,
        });
        continue;
      }
      const key = `${parsed.menu}\u0000${parsed.command}`;
      if (seen.has(key)) {
        issues.push({ pluginId: parsed.pluginId, menu: parsed.menu, command: parsed.command, message: "duplicate menu item" });
        continue;
      }
      seen.add(key);
      items.push(parsed);
    }
  }
  return { items, issues };
}

/** `plugin:<pluginId>:<commandId>` — the registry id of a menu item's command. */
export function menuItemCommandKey(item: Pick<PluginMenuItem, "pluginId" | "command">): string {
  return `plugin:${item.pluginId}:${item.command}`;
}

/** The menu item's own title for `locale` (a tag or useVueI18n().localeChain); language packs never apply. */
export function menuItemTitle(item: PluginMenuItem, locale: string | readonly string[]): string {
  return resolvePluginTitle(item, locale);
}

/**
 * The values a menu `when` sees: the plugin's own context keys (reserved host
 * prefixes dropped), then the menu's host keys and `platform`. Host keys win.
 */
export function menuContextValues(
  menu: PluginContextMenuId,
  host: PluginContextValues,
  plugin: PluginContextValues,
  platform: string,
): PluginContextValues {
  const values: PluginContextValues = {};
  for (const [key, value] of Object.entries(plugin)) {
    if (!isReservedMenuContextKey(key)) values[key] = value;
  }
  for (const key of MENU_HOST_CONTEXT_KEYS[menu]) {
    const value: PluginContextValue | undefined = host[key];
    if (value !== undefined) values[key] = value;
  }
  values[PLATFORM_KEY] = platform;
  return values;
}

export function menuItemVisible(item: PluginMenuItem, values: PluginContextValues): boolean {
  return item.whenClauses.length === 0 || evaluateWhenClauses(item.whenClauses, values);
}

/** Sorts plugin items by (group, order, plugin name, title); built-in items always precede them. */
export function sortMenuItems<T extends Pick<PluginMenuItem, "group" | "order" | "pluginName">>(
  items: readonly T[],
  titleOf: (item: T) => string,
): T[] {
  return [...items].sort(
    (left, right) =>
      left.group.localeCompare(right.group) ||
      left.order - right.order ||
      left.pluginName.localeCompare(right.pluginName) ||
      titleOf(left).localeCompare(titleOf(right)),
  );
}

/** Splits sorted plugin items into the directly shown part and the "More plugin actions" part. */
export function splitMenuOverflow<T>(items: readonly T[], limit = MENU_VISIBLE_PLUGIN_ITEMS): { visible: T[]; overflow: T[] } {
  if (items.length <= limit) return { visible: [...items], overflow: [] };
  return { visible: items.slice(0, limit), overflow: items.slice(limit) };
}

// ---------------------------------------------------------------------------
// passContext payload.

export interface ProjectTreeMenuTarget {
  projectName: string;
  relativePath: string;
  kind: "file" | "directory";
  isProjectRoot: boolean;
  /** Absolute path; sent only when the plugin declares workspace.read (required by the host for passContext). */
  path?: string;
}

export interface EditorMenuTarget {
  kind: string;
  languageId: string;
  relativePath?: string;
  selection?: string;
  readonly: boolean;
  /** SQL editor only: engine and schema, never connection strings or credentials. */
  database?: { kind: string; name?: string };
}

export interface TerminalMenuTarget {
  kind: "local" | "ssh" | "docker";
  selection?: string;
}

export interface TabMenuTarget {
  view: string;
  pinned: boolean;
}

export function extnameOf(path: string): string {
  const base = path.split(/[\\/]/).pop() ?? "";
  const dot = base.lastIndexOf(".");
  return dot > 0 ? base.slice(dot).toLowerCase() : "";
}

export function projectTreeHostContext(target: ProjectTreeMenuTarget): PluginContextValues {
  return {
    "resource.kind": target.kind,
    "resource.extname": target.kind === "directory" ? "" : extnameOf(target.relativePath),
    "resource.isProjectRoot": target.isProjectRoot,
  };
}

export function editorHostContext(target: EditorMenuTarget): PluginContextValues {
  return {
    "editor.kind": target.kind,
    "editor.languageId": target.languageId,
    "editor.hasSelection": Boolean(target.selection),
    "editor.readonly": target.readonly,
    ...(target.database ? { "database.kind": target.database.kind } : {}),
  };
}

export function terminalHostContext(target: TerminalMenuTarget): PluginContextValues {
  return { "terminal.kind": target.kind, "terminal.selection": Boolean(target.selection) };
}

export function tabHostContext(target: TabMenuTarget): PluginContextValues {
  return { "tab.view": target.view, "tab.pinned": target.pinned };
}

/** Targets of the menus built here; the other placements live in menu-targets.ts. */
export interface CoreMenuTargets {
  "editor/context": EditorMenuTarget;
  "sql/editor/context": EditorMenuTarget;
  "project/tree/context": ProjectTreeMenuTarget;
  "workbench/tab/context": TabMenuTarget;
  "terminal/context": TerminalMenuTarget;
}
export type CoreContextMenuId = keyof CoreMenuTargets;

/**
 * The passContext object of an editor, project tree, tab or terminal menu.
 * The git, database tree, ssh, sftp, redis and http menus build theirs in
 * menu-targets.ts (through composables/use-plugin-menu-target.ts).
 */
export function menuPassContext<M extends CoreContextMenuId>(menu: M, target: CoreMenuTargets[M]): Record<string, unknown> {
  const menuId: CoreContextMenuId = menu;
  switch (menuId) {
    case "project/tree/context": {
      const tree = target as ProjectTreeMenuTarget;
      return {
        projectName: tree.projectName,
        relativePath: tree.relativePath,
        kind: tree.kind,
        isProjectRoot: tree.isProjectRoot,
        ...(tree.path ? { path: tree.path } : {}),
      };
    }
    case "editor/context":
    case "sql/editor/context": {
      const editor = target as EditorMenuTarget;
      return {
        languageId: editor.languageId,
        ...(editor.relativePath ? { relativePath: editor.relativePath } : {}),
        ...(editor.selection ? { selection: editor.selection } : {}),
        ...(menuId === "sql/editor/context" && editor.database
          ? { database: { kind: editor.database.kind, ...(editor.database.name ? { name: editor.database.name } : {}) } }
          : {}),
      };
    }
    case "terminal/context": {
      const terminal = target as TerminalMenuTarget;
      return { kind: terminal.kind, ...(terminal.selection ? { selection: terminal.selection } : {}) };
    }
    case "workbench/tab/context":
      return { view: (target as TabMenuTarget).view };
  }
}

/** A passContext item was chosen while its menu had no target; nothing is sent. */
export class MenuTargetUnavailableError extends Error {
  constructor(menu: PluginContextMenuId) {
    super(`menu ${menu} has no target to pass to the plugin`);
    this.name = "MenuTargetUnavailableError";
  }
}

function utf8Length(text: string): number {
  return new TextEncoder().encode(text).length;
}

/** Cuts `text` to at most `maxBytes` UTF-8 bytes without splitting a code point. */
export function truncateUtf8(text: string, maxBytes: number): { text: string; truncated: boolean } {
  const bytes = new TextEncoder().encode(text);
  if (bytes.length <= maxBytes) return { text, truncated: false };
  let end = maxBytes;
  // Step back over continuation bytes (10xxxxxx) so the cut lands on a code point boundary.
  while (end > 0 && (bytes[end]! & 0xc0) === 0x80) end--;
  return { text: new TextDecoder().decode(bytes.subarray(0, end)), truncated: true };
}

export class MenuPayloadTooLargeError extends Error {
  readonly bytes: number;
  constructor(bytes: number) {
    super(`menu context payload is ${bytes} bytes; the limit is ${MENU_LIMITS.passContextMaxBytes}`);
    this.name = "MenuPayloadTooLargeError";
    this.bytes = bytes;
  }
}

/** A field that must not be cut (remote path, Redis key, request URL) is over its limit; nothing was sent. */
export class MenuContextFieldTooLongError extends Error {
  readonly field: "path" | "key" | "url";
  readonly limit: number;
  constructor(field: "path" | "key" | "url", limit: number) {
    // The value itself is never part of the message.
    super(`menu context ${field} is longer than ${limit} bytes`);
    this.name = "MenuContextFieldTooLongError";
    this.field = field;
    this.limit = limit;
  }
}

function rejectLongField(value: unknown, field: "path" | "key" | "url", limit: number) {
  if (typeof value === "string" && utf8Length(value) > limit) throw new MenuContextFieldTooLongError(field, limit);
}

/**
 * Applies the per-field limits of `menu` to a passContext object: `selection`
 * (any menu) and the commit `subject` are truncated and flagged with
 * `truncated: true`; the SFTP `path`, Redis `key` and request `url` are never
 * cut and throw MenuContextFieldTooLongError instead. Idempotent; call it
 * before running a command to fail early.
 */
export function normalizeMenuContext(menu: PluginContextMenuId, context: Record<string, unknown>): Record<string, unknown> {
  const next: Record<string, unknown> = { ...context };
  if (typeof next.selection === "string") {
    const cut = truncateUtf8(next.selection, MENU_LIMITS.selectionMaxBytes);
    next.selection = cut.text;
    if (cut.truncated) next.truncated = true;
  }
  switch (menu) {
    case "git/commit/context":
      if (typeof next.subject === "string") {
        const cut = truncateUtf8(next.subject, MENU_LIMITS.commitSubjectMaxBytes);
        next.subject = cut.text;
        if (cut.truncated) next.truncated = true;
      }
      break;
    case "sftp/file/context":
      rejectLongField(next.path, "path", MENU_LIMITS.remotePathMaxBytes);
      break;
    case "redis/key/context":
      rejectLongField(next.key, "key", MENU_LIMITS.redisKeyMaxBytes);
      break;
    case "http/request/context":
      rejectLongField(objectValue(next.request)?.url, "url", MENU_LIMITS.urlMaxLength);
      break;
    default:
      break;
  }
  return next;
}

/**
 * Builds the invoke payload of a passContext menu item:
 * {"menu", "context", "payload"} where `payload` is the command's own static
 * payload string or its serialized form values. Field limits are applied by
 * normalizeMenuContext (truncation or MenuContextFieldTooLongError); a payload
 * still above passContextMaxBytes is refused.
 */
export function buildMenuInvokePayload(menu: PluginContextMenuId, context: Record<string, unknown>, payload: string): string {
  const encoded = JSON.stringify({ menu, context: normalizeMenuContext(menu, context), payload });
  const bytes = utf8Length(encoded);
  if (bytes > MENU_LIMITS.passContextMaxBytes) throw new MenuPayloadTooLargeError(bytes);
  return encoded;
}

// ---------------------------------------------------------------------------
// Monaco translation (editor and SQL editor context menus).

/** Monaco context key names for the editor host keys. */
export const MONACO_MENU_CONTEXT_KEYS: Readonly<Record<string, string>> = {
  "editor.kind": "mincodeEditorKind",
  "editor.languageId": "editorLangId",
  "editor.hasSelection": "editorHasSelection",
  "editor.readonly": "editorReadonly",
  "database.kind": "mincodeDatabaseKind",
};

/**
 * Translates a menu item's when clauses to a Monaco precondition. Plugin
 * context keys map through `pluginKey` to per-editor Monaco keys; `platform`
 * is resolved now. Returns null when the item can never show (platform
 * mismatch) and throws when a literal cannot be expressed in Monaco syntax.
 */
export function monacoMenuPrecondition(
  item: Pick<PluginMenuItem, "menu" | "whenClauses">,
  platform: string,
  pluginKey: (key: string) => string,
): string | null {
  if (item.menu !== "editor/context" && item.menu !== "sql/editor/context") {
    throw new Error(`menu ${item.menu} is not a Monaco menu`);
  }
  const parts = [item.menu === "sql/editor/context" ? "mincodeEditorKind == 'sql'" : "mincodeEditorKind != 'sql'"];
  for (const clause of item.whenClauses) {
    if (clause.key === PLATFORM_KEY) {
      let result = clause.op === "" ? Boolean(platform) : (platform === clause.value) === (clause.op === "==");
      if (clause.negate) result = !result;
      if (!result) return null;
      continue;
    }
    if (/['\\]/.test(clause.value)) throw new Error(`when literal ${JSON.stringify(clause.value)} cannot be used in an editor menu`);
    const key = MONACO_MENU_CONTEXT_KEYS[clause.key] ?? (isReservedMenuContextKey(clause.key) ? null : pluginKey(clause.key));
    if (!key) throw new Error(`context key ${clause.key} is not available in editor menus`);
    if (clause.op === "") {
      parts.push(clause.negate ? `!${key}` : key);
      continue;
    }
    // `!a == "x"` is `a != "x"` in the when dialect (equality needs a string value).
    const op = clause.negate ? (clause.op === "==" ? "!=" : "==") : clause.op;
    parts.push(`${key} ${op} '${clause.value}'`);
  }
  return parts.join(" && ");
}
