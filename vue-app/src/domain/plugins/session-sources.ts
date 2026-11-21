/**
 * Frontend stage of the `sessionSources` contribution point: re-validation
 * of the sources the Go host accepted
 * (backend/plugin/contrib_session_sources.go) and of the session
 * lists and transcripts a source's read action returns. Plugin sessions are
 * read only and their messages are plain text: nothing here produces markup,
 * links or file paths. A source key `plugin:<pluginId>/<id>` never collides
 * with the built-in provider IDs or `custom:<slug>`. No DOM, Vue or IPC
 * here; nothing in this module calls the plugin.
 */
import type { PointContribution } from "@/domain/plugins/contributions";
import {
  PROVIDER_ACTION_ID_PATTERN,
  PROVIDER_ENTRY_ID_PATTERN,
  PROVIDER_PLUGIN_ID_PATTERN,
  boundedLine,
  isProviderItemId,
  isRecord,
  localizedTitles,
  responseCount,
} from "@/domain/plugins/provider-points";

export const SESSION_SOURCES_CONTRIBUTION_POINT = "sessionSources";

/** Mirrors contribution-points.json sessionSources maxEntries and limits (the host enforces them first). */
export const SESSION_SOURCE_LIMITS = {
  maxEntries: 2,
  titleMaxRunes: 32,
  localesMax: 16,
  markMaxLength: 3,
  sessionsMax: 200,
  sessionIdMaxLength: 128,
  sessionTitleMaxRunes: 200,
  sessionDetailMaxRunes: 200,
  modelMaxRunes: 64,
  messageCountMax: 1_000_000_000,
  messagesMax: 200,
  messageTextMaxRunes: 32_768,
  timestampMax: 253_402_300_799_999,
} as const;

/** Mirrors contribution-points.json sessionSources lists.icons. */
export const SESSION_SOURCE_ICONS = ["chat", "cpu", "document", "monitor", "magic"] as const;
export type SessionSourceIcon = (typeof SESSION_SOURCE_ICONS)[number];

/** Mirrors contribution-points.json sessionSources lists.roles. */
export const SESSION_MESSAGE_ROLES = ["user", "assistant", "system", "tool"] as const;
export type PluginSessionMessageRole = (typeof SESSION_MESSAGE_ROLES)[number];

const ICONS: ReadonlySet<string> = new Set(SESSION_SOURCE_ICONS);
const ROLES: ReadonlySet<string> = new Set(SESSION_MESSAGE_ROLES);
const DEFAULT_ICON: SessionSourceIcon = "chat";
const FALLBACK_MARK = "PL";
const MARK_PATTERN = /^[A-Z0-9]{1,3}$/;
const KEY_PREFIX = "plugin:";

export interface PluginSessionSource {
  /** `plugin:<pluginId>/<id>` */
  key: string;
  pluginId: string;
  pluginName: string;
  id: string;
  title: string;
  titleLocalized?: Record<string, string>;
  icon: SessionSourceIcon;
  mark: string;
  activeInWorkspace: boolean;
}

export interface PluginSessionSummary {
  id: string;
  /** Empty when the plugin gave none (the UI shows the id). */
  title: string;
  detail?: string;
  model?: string;
  lastAt?: number;
  messageCount?: number;
}

export interface PluginSessionMessage {
  role: PluginSessionMessageRole;
  /** Plain text: render with text interpolation only, never as Markdown or HTML. */
  text: string;
  timestamp?: number;
  model?: string;
  truncated?: boolean;
}

export function pluginSessionSourceKey(pluginId: string, id: string): string {
  return `${KEY_PREFIX}${pluginId}/${id}`;
}

/** The plugin and source of a `plugin:<pluginId>/<id>` key; undefined for any other provider id. */
export function parsePluginSessionSourceKey(key: string): { pluginId: string; id: string } | undefined {
  if (typeof key !== "string" || !key.startsWith(KEY_PREFIX)) return undefined;
  const rest = key.slice(KEY_PREFIX.length);
  const slash = rest.indexOf("/");
  if (slash < 0) return undefined;
  const pluginId = rest.slice(0, slash);
  const id = rest.slice(slash + 1);
  if (!PROVIDER_PLUGIN_ID_PATTERN.test(pluginId) || !PROVIDER_ENTRY_ID_PATTERN.test(id)) return undefined;
  return { pluginId, id };
}

/**
 * The badge of a source without its own mark: the first two ASCII letters or
 * digits of the title in upper case, else "PL" (same as the host's
 * defaultSessionSourceMark).
 */
export function defaultSessionSourceMark(title: string): string {
  const mark = (title.match(/[A-Za-z0-9]/g) ?? []).slice(0, 2).join("").toUpperCase();
  return mark || FALLBACK_MARK;
}

function validateSessionSourceEntry(raw: unknown): Omit<PluginSessionSource, "key" | "pluginId" | "pluginName" | "activeInWorkspace"> | undefined {
  if (!isRecord(raw)) return undefined;
  const id = typeof raw.id === "string" ? raw.id : "";
  if (!PROVIDER_ENTRY_ID_PATTERN.test(id)) return undefined;
  const title = boundedLine(raw.title, SESSION_SOURCE_LIMITS.titleMaxRunes, true);
  if (!title) return undefined;
  const titleLocalized = localizedTitles(raw.titleLocalized, SESSION_SOURCE_LIMITS.titleMaxRunes, SESSION_SOURCE_LIMITS.localesMax);
  if (titleLocalized === false) return undefined;
  if (typeof raw.action !== "string" || !PROVIDER_ACTION_ID_PATTERN.test(raw.action)) return undefined;
  // The host already defaulted and validated icon and mark; anything else
  // falls back rather than hiding the source.
  const icon = typeof raw.icon === "string" && ICONS.has(raw.icon) ? (raw.icon as SessionSourceIcon) : DEFAULT_ICON;
  const mark = typeof raw.mark === "string" && MARK_PATTERN.test(raw.mark) ? raw.mark : defaultSessionSourceMark(title);
  return { id, title, ...(titleLocalized ? { titleLocalized } : {}), icon, mark };
}

/**
 * The sources of the process plugins in the contribution snapshot, in
 * ascending plugin id order, at most maxEntries per plugin. Entries without a
 * valid id, title or action and duplicate ids are dropped. Inactive plugins
 * are kept with activeInWorkspace false; the sessions page lists only active
 * ones.
 */
export function collectSessionSources(contributions: readonly PointContribution[]): PluginSessionSource[] {
  const sources: PluginSessionSource[] = [];
  const ordered = [...contributions].sort((a, b) => (a.pluginId < b.pluginId ? -1 : a.pluginId > b.pluginId ? 1 : 0));
  for (const contribution of ordered) {
    if (contribution.runtime !== "process" || !PROVIDER_PLUGIN_ID_PATTERN.test(contribution.pluginId)) continue;
    const seen = new Set<string>();
    for (const raw of contribution.point.entries.slice(0, SESSION_SOURCE_LIMITS.maxEntries)) {
      const entry = validateSessionSourceEntry(raw);
      if (!entry || seen.has(entry.id)) continue;
      seen.add(entry.id);
      sources.push({
        key: pluginSessionSourceKey(contribution.pluginId, entry.id),
        pluginId: contribution.pluginId,
        pluginName: contribution.pluginName,
        ...entry,
        activeInWorkspace: contribution.activeInWorkspace,
      });
    }
  }
  return sources;
}

/** An absent optional field: Go structs without omitempty send null. */
function absent(value: unknown): value is null | undefined {
  return value === undefined || value === null;
}

/** An optional single line where null means absent (see boundedLine). */
function optionalLine(value: unknown, maxRunes: number): string | undefined | false {
  return absent(value) ? undefined : boundedLine(value, maxRunes, false);
}

function boundedInteger(value: unknown, max: number): number | undefined | false {
  if (absent(value)) return undefined;
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0 && value <= max ? value : false;
}

/**
 * Re-validates a `plugin_host_session_list` response: invalid or duplicate
 * sessions are dropped and counted, the plugin's order is kept and at most
 * sessionsMax are kept.
 */
export function parseSessionListResponse(value: unknown): { sessions: PluginSessionSummary[]; dropped: number; truncated: boolean } {
  const response = { sessions: [] as PluginSessionSummary[], dropped: 0, truncated: false };
  if (!isRecord(value) || !Array.isArray(value.sessions)) return response;
  response.dropped = responseCount(value.dropped);
  response.truncated = value.truncated === true || value.sessions.length > SESSION_SOURCE_LIMITS.sessionsMax;
  const seen = new Set<string>();
  for (const raw of value.sessions.slice(0, SESSION_SOURCE_LIMITS.sessionsMax)) {
    const item = isRecord(raw) ? raw : {};
    const id = isProviderItemId(item.id, SESSION_SOURCE_LIMITS.sessionIdMaxLength) ? item.id : "";
    const title = optionalLine(item.title, SESSION_SOURCE_LIMITS.sessionTitleMaxRunes);
    const detail = optionalLine(item.detail, SESSION_SOURCE_LIMITS.sessionDetailMaxRunes);
    const model = optionalLine(item.model, SESSION_SOURCE_LIMITS.modelMaxRunes);
    const lastAt = boundedInteger(item.lastAt, SESSION_SOURCE_LIMITS.timestampMax);
    const messageCount = boundedInteger(item.messageCount, SESSION_SOURCE_LIMITS.messageCountMax);
    if (!id || seen.has(id) || title === false || detail === false || model === false || lastAt === false || messageCount === false) {
      response.dropped += 1;
      continue;
    }
    seen.add(id);
    response.sessions.push({
      id,
      title: title ?? "",
      ...(detail ? { detail } : {}),
      ...(model ? { model } : {}),
      ...(lastAt !== undefined ? { lastAt } : {}),
      ...(messageCount !== undefined ? { messageCount } : {}),
    });
  }
  return response;
}

// eslint-disable-next-line no-control-regex -- matching terminal escape sequences is the point
const ANSI_PATTERN = /\u001b(?:\[[0-9;?]*[ -/]*[@-~]|\][^\u0007\u001b]*(?:\u0007|\u001b\\)|[@-Z\\-_])/g;
// eslint-disable-next-line no-control-regex -- C0 except tab and LF, DEL, C1, separators, bidi and zero-width
const UNSAFE_TEXT_PATTERN = /[\u0000-\u0008\u000b-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069\u200b-\u200f\u2060\ufeff]/g;

/**
 * Message text as plain text, like the host's sanitizeContributionText:
 * CRLF and CR become LF, ANSI sequences and unsafe characters are removed
 * (tabs and newlines kept) and the result is cut to maxRunes.
 */
function sessionMessageText(value: string, maxRunes: number): { text: string; truncated: boolean } {
  const cleaned = value.replace(ANSI_PATTERN, "").replace(/\r\n?/g, "\n").replace(UNSAFE_TEXT_PATTERN, "");
  const runes = [...cleaned];
  return runes.length > maxRunes ? { text: runes.slice(0, maxRunes).join(""), truncated: true } : { text: cleaned, truncated: false };
}

/**
 * Re-validates a `plugin_host_session_messages` response: messages with an
 * unknown role, non-string text or an invalid timestamp or model are dropped
 * and counted; the last messagesMax are kept. Text stays plain text (markup
 * and links are kept as literal characters).
 */
export function parseSessionMessagesResponse(value: unknown): { messages: PluginSessionMessage[]; dropped: number; truncated: boolean } {
  const response = { messages: [] as PluginSessionMessage[], dropped: 0, truncated: false };
  if (!isRecord(value) || !Array.isArray(value.messages)) return response;
  response.dropped = responseCount(value.dropped);
  response.truncated = value.truncated === true || value.messages.length > SESSION_SOURCE_LIMITS.messagesMax;
  for (const raw of value.messages.slice(-SESSION_SOURCE_LIMITS.messagesMax)) {
    const item = isRecord(raw) ? raw : {};
    const role = typeof item.role === "string" && ROLES.has(item.role) ? (item.role as PluginSessionMessageRole) : undefined;
    const timestamp = boundedInteger(item.timestamp, SESSION_SOURCE_LIMITS.timestampMax);
    const model = optionalLine(item.model, SESSION_SOURCE_LIMITS.modelMaxRunes);
    if (!role || typeof item.text !== "string" || timestamp === false || model === false) {
      response.dropped += 1;
      continue;
    }
    const { text, truncated } = sessionMessageText(item.text, SESSION_SOURCE_LIMITS.messageTextMaxRunes);
    response.messages.push({
      role,
      text,
      ...(timestamp !== undefined ? { timestamp } : {}),
      ...(model ? { model } : {}),
      ...(truncated || item.truncated === true ? { truncated: true } : {}),
    });
  }
  return response;
}
