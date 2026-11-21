/**
 * Pure model of the `terminalProfiles` contribution point (process plugins that
 * declare the `shell` capability). The Go host
 * (backend/plugin/contrib_terminal_profiles.go) validates entries at
 * install and resolves, re-validates and fingerprints the launch spec at every
 * launch (terminal_profiles_launch.go). Entries are still untrusted here: they
 * are re-checked and only used for display (menu, palette, install preview).
 * The WebView never sends a command, args or env for a profile; it only sends
 * the `(pluginId, profileId)` reference. Nothing here touches IPC or Vue.
 */
import type { PointContribution } from "@/domain/plugins/contributions";
import { resolvePluginTitle } from "@/domain/plugins/title";
import type { TerminalPaneNode, TerminalProfileRef } from "@/domain/terminal/types";
import { isTerminalLeafPane } from "@/domain/terminal/types";

export const TERMINAL_PROFILES_CONTRIBUTION_POINT = "terminalProfiles";

// Mirrors `points.terminalProfiles` in plugins/api/contribution-points.json
// (vue-app/scripts/terminal-profiles-contributions.test.mjs keeps them in sync).
export const TERMINAL_PROFILE_MAX_ENTRIES = 16;
export const TERMINAL_PROFILE_TITLE_MAX_RUNES = 64;
export const TERMINAL_PROFILE_LOCALES_MAX = 16;
export const TERMINAL_PROFILE_ARGS_MAX = 32;
export const TERMINAL_PROFILE_ARG_MAX_RUNES = 1024;
export const TERMINAL_PROFILE_ENV_MAX = 32;
export const TERMINAL_PROFILE_COMMAND_MAX_LENGTH = 512;

/** Palette command prefix: `terminal.profile:<pluginId>.<profileId>`. */
export const TERMINAL_PROFILE_COMMAND_PREFIX = "terminal.profile:";
/** "+" menu command prefix: `profile:<pluginId>.<profileId>`. */
export const TERMINAL_PROFILE_MENU_PREFIX = "profile:";

export type TerminalProfilePlatform = "darwin" | "linux" | "windows";
export type TerminalProfileErrorCode = "not_found" | "forbidden" | "untrusted" | "changed" | "unavailable";

const PLATFORMS: readonly TerminalProfilePlatform[] = ["darwin", "linux", "windows"];
const ID_PATTERN = /^[a-z0-9][a-z0-9-]{0,31}$/;
const PLUGIN_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;
const LOCALE_PATTERN = /^[A-Za-z]{2,8}(?:-[A-Za-z0-9]{1,8}){0,3}$/;
const ICON_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$/;
const ENV_NAME_PATTERN = /^[A-Za-z_][A-Za-z0-9_]{0,63}$/;
const FINGERPRINT_PATTERN = /^[0-9a-f]{64}$/;
const ERROR_CODES: readonly TerminalProfileErrorCode[] = ["not_found", "forbidden", "untrusted", "changed", "unavailable"];
// eslint-disable-next-line no-control-regex
const UNSAFE_TEXT = /[\u0000-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069\u200b-\u200f\ufeff]/g;
// eslint-disable-next-line no-control-regex
const UNSAFE_CHAR = /[\u0000-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069\u200b-\u200f\ufeff]/;
const SHELL_SAFE_ARG = /^[A-Za-z0-9_@%+=:,./-]+$/;

export interface TerminalProfileContribution {
  /** `<pluginId>.<profileId>`. */
  key: string;
  pluginId: string;
  pluginName: string;
  id: string;
  title: string;
  titleLocalized?: Record<string, string>;
  icon?: string;
  /** Declared command (display only; the host resolves the executable). */
  command: string;
  args: string[];
  /** Declared env names (values are shown only in the host-resolved confirmation). */
  envNames: string[];
  cwd: string;
  platforms: TerminalProfilePlatform[];
}

/** Host-resolved launch spec (plugin_host_terminal_profile_prepare / _trust). */
export interface TerminalProfileLaunch {
  pluginId: string;
  pluginName: string;
  profileId: string;
  title: string;
  titleLocalized?: Record<string, string>;
  argv: string[];
  env: Record<string, string>;
  cwd: string;
  pluginExecutable: boolean;
  executableSha256?: string;
  fingerprint: string;
  trusted: boolean;
  previouslyTrusted: boolean;
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === "object" && value !== null && !Array.isArray(value) ? (value as Record<string, unknown>) : undefined;
}

function sanitizeText(value: string, maxRunes: number) {
  const clean = value.replace(UNSAFE_TEXT, "").trim();
  return Array.from(clean).slice(0, maxRunes).join("");
}

function hasUnsafeText(value: string) {
  return UNSAFE_CHAR.test(value);
}

export function terminalProfileKey(pluginId: string, profileId: string) {
  return `${pluginId}.${profileId}`;
}

/** Splits `<pluginId>.<profileId>` (plugin ids may contain dots; profile ids cannot). */
export function parseTerminalProfileKey(key: string): { pluginId: string; profileId: string } | undefined {
  const index = key.lastIndexOf(".");
  if (index <= 0) return undefined;
  const pluginId = key.slice(0, index);
  const profileId = key.slice(index + 1);
  return PLUGIN_ID_PATTERN.test(pluginId) && ID_PATTERN.test(profileId) ? { pluginId, profileId } : undefined;
}

/** Maps `navigator.platform` (or a Go GOOS value) to a contract platform. */
export function terminalProfilePlatform(platform: string): TerminalProfilePlatform {
  const value = platform.toLowerCase();
  if (value.startsWith("win")) return "windows";
  if (value.startsWith("mac") || value === "darwin" || value.startsWith("iphone") || value.startsWith("ipad")) return "darwin";
  return "linux";
}

function parseTitleLocalized(value: unknown): Record<string, string> | undefined {
  const record = objectValue(value);
  if (!record) return undefined;
  const result: Record<string, string> = {};
  let count = 0;
  for (const [locale, title] of Object.entries(record)) {
    if (count >= TERMINAL_PROFILE_LOCALES_MAX) break;
    if (!LOCALE_PATTERN.test(locale) || typeof title !== "string") continue;
    const clean = sanitizeText(title, TERMINAL_PROFILE_TITLE_MAX_RUNES);
    if (!clean) continue;
    result[locale] = clean;
    count += 1;
  }
  return count > 0 ? result : undefined;
}

function stringList(value: unknown, max: number, maxRunes: number): string[] | undefined {
  if (value === undefined) return [];
  if (!Array.isArray(value) || value.length > max) return undefined;
  const result: string[] = [];
  for (const item of value) {
    if (typeof item !== "string" || hasUnsafeText(item) || Array.from(item).length > maxRunes) return undefined;
    result.push(item);
  }
  return result;
}

/** Re-validates one entry; returns undefined when anything is out of contract. */
export function parseTerminalProfileEntry(
  value: unknown,
  plugin: { pluginId: string; pluginName: string },
): TerminalProfileContribution | undefined {
  const record = objectValue(value);
  if (!record) return undefined;
  const id = record.id;
  if (typeof id !== "string" || !ID_PATTERN.test(id)) return undefined;
  const command = record.command;
  if (typeof command !== "string" || !command.trim() || command.length > TERMINAL_PROFILE_COMMAND_MAX_LENGTH || hasUnsafeText(command)) {
    return undefined;
  }
  const args = stringList(record.args ?? undefined, TERMINAL_PROFILE_ARGS_MAX, TERMINAL_PROFILE_ARG_MAX_RUNES);
  if (!args) return undefined;
  const env = record.env === undefined || record.env === null ? {} : objectValue(record.env);
  if (!env) return undefined;
  const envNames = Object.keys(env);
  if (envNames.length > TERMINAL_PROFILE_ENV_MAX || !envNames.every((name) => ENV_NAME_PATTERN.test(name))) return undefined;
  const cwd = record.cwd === undefined || record.cwd === "" ? "${workspaceFolder}" : record.cwd;
  if (typeof cwd !== "string" || hasUnsafeText(cwd)) return undefined;
  let platforms: TerminalProfilePlatform[] = [...PLATFORMS];
  if (record.platforms !== undefined && record.platforms !== null) {
    if (!Array.isArray(record.platforms) || record.platforms.length === 0) return undefined;
    if (!record.platforms.every((platform) => PLATFORMS.includes(platform as TerminalProfilePlatform))) return undefined;
    platforms = [...new Set(record.platforms as TerminalProfilePlatform[])];
  }
  const title = typeof record.title === "string" ? sanitizeText(record.title, TERMINAL_PROFILE_TITLE_MAX_RUNES) : "";
  const icon = typeof record.icon === "string" && ICON_PATTERN.test(record.icon) ? record.icon : undefined;
  return {
    key: terminalProfileKey(plugin.pluginId, id),
    pluginId: plugin.pluginId,
    pluginName: sanitizeText(plugin.pluginName, TERMINAL_PROFILE_TITLE_MAX_RUNES) || plugin.pluginId,
    id,
    title: title || id,
    titleLocalized: parseTitleLocalized(record.titleLocalized),
    icon,
    command: command.trim(),
    args,
    envNames: [...envNames].sort(),
    cwd,
    platforms,
  };
}

/**
 * Builds the profile list offered on `platform`: process plugins active in the
 * workspace only, deduplicated, sorted by pluginId then profile id.
 */
export function resolveTerminalProfiles(
  contributions: readonly PointContribution[],
  platform: TerminalProfilePlatform,
): TerminalProfileContribution[] {
  const profiles: TerminalProfileContribution[] = [];
  const seen = new Set<string>();
  for (const contribution of contributions) {
    if (contribution.runtime !== "process" || !contribution.activeInWorkspace) continue;
    if (!PLUGIN_ID_PATTERN.test(contribution.pluginId)) continue;
    const entries = Array.isArray(contribution.point?.entries) ? contribution.point.entries : [];
    let count = 0;
    for (const entry of entries) {
      if (count >= TERMINAL_PROFILE_MAX_ENTRIES) break;
      const profile = parseTerminalProfileEntry(entry, contribution);
      if (!profile || seen.has(profile.key)) continue;
      count += 1;
      seen.add(profile.key);
      if (!profile.platforms.includes(platform)) continue;
      profiles.push(profile);
    }
  }
  return profiles.sort((left, right) => left.pluginId.localeCompare(right.pluginId) || left.id.localeCompare(right.id));
}

export function terminalProfileTitle(
  profile: { title: string; titleLocalized?: Record<string, string> },
  locale: string | readonly string[],
) {
  return resolvePluginTitle({ title: profile.title, titleLocalized: profile.titleLocalized }, locale);
}

/** Restores a persisted pane reference; anything malformed is dropped (the pane becomes a plain shell). */
export function restoreTerminalProfileRef(value: unknown): TerminalProfileRef | undefined {
  const record = objectValue(value);
  if (!record) return undefined;
  const { pluginId, profileId } = record;
  if (typeof pluginId !== "string" || !PLUGIN_ID_PATTERN.test(pluginId)) return undefined;
  if (typeof profileId !== "string" || !ID_PATTERN.test(profileId)) return undefined;
  const title = typeof record.title === "string" ? sanitizeText(record.title, TERMINAL_PROFILE_TITLE_MAX_RUNES) : "";
  const workspaceFolder =
    typeof record.workspaceFolder === "string" && record.workspaceFolder.length <= 4096 && !hasUnsafeText(record.workspaceFolder)
      ? record.workspaceFolder
      : undefined;
  return { pluginId, profileId, title: title || profileId, ...(workspaceFolder ? { workspaceFolder } : {}) };
}

/**
 * "Open default shell" from a blocked plugin-profile pane (explicit user
 * choice only): the pane keeps its ID and working directory but drops the
 * profile reference and becomes a system shell. `reloadVersion` is bumped so
 * the pane remounts; callers close the old native session first. Returns the
 * same tree when `paneId` is not a profile pane.
 */
export function replaceProfilePaneWithDefaultShell(node: TerminalPaneNode, paneId: string): TerminalPaneNode {
  if (isTerminalLeafPane(node)) {
    if (node.id !== paneId || node.type !== "local" || !node.profile) return node;
    return {
      id: node.id,
      type: "local",
      shellType: "system",
      initialCwd: node.initialCwd,
      cwd: node.initialCwd,
      reloadVersion: (node.reloadVersion ?? 0) + 1,
    };
  }
  const children = node.children.map((child) => replaceProfilePaneWithDefaultShell(child, paneId));
  if (children.every((child, index) => child === node.children[index])) return node;
  return { ...node, children };
}

function stringRecord(value: unknown): Record<string, string> | undefined {
  const record = objectValue(value);
  if (!record) return undefined;
  const result: Record<string, string> = {};
  for (const [name, item] of Object.entries(record)) {
    if (!ENV_NAME_PATTERN.test(name) || typeof item !== "string") return undefined;
    result[name] = item;
  }
  return result;
}

/** Parses the host's resolved launch spec; undefined when malformed. */
export function parseTerminalProfileLaunch(value: unknown): TerminalProfileLaunch | undefined {
  const record = objectValue(value);
  if (!record) return undefined;
  const { pluginId, profileId, fingerprint, cwd } = record;
  if (typeof pluginId !== "string" || !PLUGIN_ID_PATTERN.test(pluginId)) return undefined;
  if (typeof profileId !== "string" || !ID_PATTERN.test(profileId)) return undefined;
  if (typeof fingerprint !== "string" || !FINGERPRINT_PATTERN.test(fingerprint)) return undefined;
  if (typeof cwd !== "string" || !cwd) return undefined;
  if (!Array.isArray(record.argv) || record.argv.length === 0 || !record.argv.every((arg) => typeof arg === "string")) {
    return undefined;
  }
  const env = record.env === null || record.env === undefined ? {} : stringRecord(record.env);
  if (!env) return undefined;
  const sha = typeof record.executableSha256 === "string" && FINGERPRINT_PATTERN.test(record.executableSha256)
    ? record.executableSha256
    : undefined;
  const title = typeof record.title === "string" ? sanitizeText(record.title, TERMINAL_PROFILE_TITLE_MAX_RUNES) : "";
  return {
    pluginId,
    pluginName: typeof record.pluginName === "string" ? sanitizeText(record.pluginName, TERMINAL_PROFILE_TITLE_MAX_RUNES) || pluginId : pluginId,
    profileId,
    title: title || profileId,
    titleLocalized: parseTitleLocalized(record.titleLocalized),
    argv: record.argv as string[],
    env,
    cwd,
    pluginExecutable: record.pluginExecutable === true,
    executableSha256: sha,
    fingerprint,
    trusted: record.trusted === true,
    previouslyTrusted: record.previouslyTrusted === true,
  };
}

/** Makes a value safe to show: control, bidi and zero-width characters become `\u{…}` escapes. */
export function visibleTerminalText(value: string) {
  return value.replace(UNSAFE_TEXT, (char) => `\\u{${char.codePointAt(0)!.toString(16)}}`);
}

/** Quotes one argument for display (POSIX style); never used to build a command line. */
export function quoteTerminalArg(arg: string) {
  const visible = visibleTerminalText(arg);
  if (visible && SHELL_SAFE_ARG.test(visible)) return visible;
  return `'${visible.replace(/'/g, `'\\''`)}'`;
}

/** Display string of an argv (for the confirmation dialog and the pane tooltip). */
export function formatTerminalArgv(argv: readonly string[]) {
  return argv.map(quoteTerminalArg).join(" ");
}

/** Sorted `NAME=value` lines for display. */
export function formatTerminalEnv(env: Readonly<Record<string, string>>) {
  return Object.keys(env)
    .sort()
    .map((name) => `${name}=${visibleTerminalText(env[name] ?? "")}`);
}

/** The `<code>:` prefix of a launch error from the host, if any. */
export function terminalProfileErrorCode(error: unknown): TerminalProfileErrorCode | undefined {
  const message = error instanceof Error ? error.message : typeof error === "string" ? error : "";
  for (const code of ERROR_CODES) {
    const index = message.indexOf(`${code}:`);
    if (index === 0 || (index > 0 && /[\s:]/.test(message[index - 1] ?? ""))) return code;
  }
  return undefined;
}

export interface TerminalProfilePreviewEntry {
  title: string;
  command: string;
  args: string[];
  envNames: string[];
  cwd: string;
  platforms: TerminalProfilePlatform[];
}

/** Install preview (manifest `contributes.terminalProfiles`): the declared commands, before any launch. */
export function terminalProfilePreviewSummary(contributes: unknown, pluginId = "plugin"): TerminalProfilePreviewEntry[] {
  const entries = objectValue(contributes)?.terminalProfiles;
  if (!Array.isArray(entries)) return [];
  return entries.slice(0, TERMINAL_PROFILE_MAX_ENTRIES).flatMap((entry) => {
    const profile = parseTerminalProfileEntry(entry, { pluginId, pluginName: pluginId });
    return profile
      ? [{ title: profile.title, command: profile.command, args: profile.args, envNames: profile.envNames, cwd: profile.cwd, platforms: profile.platforms }]
      : [];
  });
}
