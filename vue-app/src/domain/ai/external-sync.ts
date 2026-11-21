// Compares AI settings handed over by a plugin (for example a CC Switch
// importer) with MinCode's own lists and applies the items the user picked.
// Pure functions only: persistence, confirmation and secret storage live in
// services/ai-external-sync.ts. Secret values (API keys, MCP env and header
// values) never reach a fingerprint or a preview entry; only their local hash
// is kept, in SecretHashes, outside the synced settings.
import {
  AI_ANTHROPIC_AUTH_MODES,
  normalizeAnthropicAuth,
  type AiAnthropicAuth,
} from "@/domain/ai/anthropic-auth";
import { AI_REASONING_EFFORT_CHOICES } from "@/domain/ai/model-selection";
import { normalizeDocumentationUrl, parseAiSkillMarkdown } from "@/domain/ai/settings-integrations";
import { findSynced, type SyncKind, type SyncSource } from "@/domain/ai/sync-source";

export { findSynced, type SyncKind, type SyncSource };

export interface SyncProfileData {
  baseUrl: string;
  apiKey: string;
  model: string;
  apiFormat: "openai" | "openai-responses" | "anthropic";
  anthropicAuth?: AiAnthropicAuth;
  commonModels: string[];
  subagentModel?: string;
  reasoningEffort?: string;
  contextWindow?: number;
}
/**
 * A value the host confirmation asks the user for, e.g. an API token a
 * connector market cannot know. It names an env or header key of the same
 * item whose source value is empty; withPromptValues fills it in.
 */
export interface SyncMcpPrompt {
  target: "env" | "header";
  name: string;
  /** At most SYNC_PROMPT_LABEL_MAX characters. */
  label: string;
  /** Shown masked in the confirmation. */
  secret: boolean;
  /** Prepended to a filled-in value that does not start with it, e.g. "Bearer "; at most SYNC_PROMPT_PREFIX_MAX characters. */
  valuePrefix?: string;
}
export interface SyncMcpData {
  transport: "stdio" | "http";
  command: string;
  args: string[];
  url: string;
  env: Record<string, string>;
  headers: Record<string, string>;
  prompts?: SyncMcpPrompt[];
  /** The server needs an OAuth sign-in; written to the settings object only when present. */
  auth?: "oauth";
}
export interface SyncSkillData {
  content: string;
  description?: string;
  docsUrl?: string;
  hasExtraFiles: boolean;
}
export type SyncItem =
  | { kind: "profile"; sourceId: string; name: string; sourceApp: string; profile: SyncProfileData }
  | { kind: "mcp"; sourceId: string; name: string; sourceApp: string; mcp: SyncMcpData }
  | { kind: "skill"; sourceId: string; name: string; sourceApp: string; skill: SyncSkillData };
export type SyncStatus = "new" | "update" | "conflict" | "unchanged";
export interface SyncPreviewEntry {
  key: string;
  kind: SyncKind;
  name: string;
  sourceApp: string;
  status: SyncStatus;
  needsSecret: boolean;
  changedFields: string[];
  secretChanged: boolean;
  targetName?: string;
  /** Stable note codes: "possible-duplicate", "extra-files". */
  notes: string[];
}
export interface SyncChoice {
  key: string;
  action: "add" | "overwrite";
}
/**
 * What the host confirmation showed for an entry. A write given these refuses
 * (reason "changed-since-review") every choice whose entry differs now, and
 * every choice without one (reason "not-reviewed").
 */
export type SyncReviewedEntry = Pick<
  SyncPreviewEntry,
  "key" | "status" | "targetName" | "needsSecret" | "secretChanged" | "changedFields"
>;
export interface SyncSettingsLists {
  aiProfiles: Record<string, unknown>[];
  mcpServers: Record<string, unknown>[];
  aiSkills: Record<string, unknown>[];
}
/** Sync key → hash of the secrets last written from the source. Local only, never synced. */
export type SecretHashes = Record<string, string>;

type SettingsObject = Record<string, unknown>;

const PROFILE_API_FORMATS = ["openai", "openai-responses", "anthropic"] as const;
const MCP_TRANSPORTS = ["stdio", "http"] as const;
const MCP_PROMPT_TARGETS = ["env", "header"] as const;
const MCP_PROMPT_FIELD = { env: "env", header: "headers" } as const;
const MCP_AUTH_MODES = ["oauth"] as const;
export const SYNC_PROMPT_LABEL_MAX = 200;
export const SYNC_PROMPT_PREFIX_MAX = 32;
const LIST_BY_KIND = { profile: "aiProfiles", mcp: "mcpServers", skill: "aiSkills" } as const;
// MCP servers are named by their id; profiles and skills carry a separate name.
const NAME_FIELD = { profile: "name", mcp: "id", skill: "name" } as const;
const SOURCE_APP_LABELS: Record<string, string> = { claude: "Claude Code", codex: "Codex" };

// Written fields other than the add-only ones (name, enabled). Secret fields are
// written but replaced by key names (MCP) or left out (apiKey) of fingerprints.
const PROFILE_FIELDS = [
  "provider",
  "baseUrl",
  "apiKey",
  "model",
  "apiFormat",
  "anthropicAuth",
  "commonModels",
  "subagentModel",
  "reasoningEffort",
] as const;
const MCP_FIELDS = ["transport", "command", "args", "url", "env", "headers", "auth"] as const;
// Fingerprints taken before `auth` existed, and of objects without it, cover these.
const MCP_FIELDS_WITHOUT_AUTH = MCP_FIELDS.filter((name) => name !== "auth");
const SKILL_FIELDS = ["content", "description", "docsUrl"] as const;

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

function isFilled(value: unknown): value is string {
  return typeof value === "string" && value.trim() !== "";
}

function isOneOf<T extends string>(choices: readonly T[], value: unknown): value is T {
  return choices.some((choice) => choice === value);
}

function isStringArray(value: unknown): value is string[] {
  return Array.isArray(value) && value.every((item) => typeof item === "string");
}

function isStringRecord(value: unknown): value is Record<string, string> {
  return (
    isRecord(value) &&
    Object.entries(value).every(([key, item]) => key.trim() !== "" && typeof item === "string")
  );
}

function isOptional(value: unknown, check: (value: unknown) => boolean) {
  return value === undefined || check(value);
}

function parseProfileData(value: unknown): SyncProfileData | undefined {
  if (!isRecord(value)) return undefined;
  const { baseUrl, apiKey, model, apiFormat, anthropicAuth, commonModels, subagentModel, reasoningEffort, contextWindow } = value;
  if (
    !isFilled(baseUrl) ||
    !isFilled(apiKey) ||
    typeof model !== "string" ||
    !isOneOf(PROFILE_API_FORMATS, apiFormat) ||
    !isStringArray(commonModels) ||
    !isOptional(anthropicAuth, (item) => isOneOf(AI_ANTHROPIC_AUTH_MODES, item)) ||
    !isOptional(subagentModel, (item) => typeof item === "string") ||
    !isOptional(reasoningEffort, (item) => typeof item === "string") ||
    !isOptional(contextWindow, (item) => Number.isSafeInteger(item) && (item as number) > 0)
  ) {
    return undefined;
  }
  return {
    baseUrl,
    apiKey,
    model,
    apiFormat,
    commonModels: [...commonModels],
    ...(anthropicAuth === undefined ? {} : { anthropicAuth: anthropicAuth as AiAnthropicAuth }),
    ...(subagentModel === undefined ? {} : { subagentModel: subagentModel as string }),
    ...(reasoningEffort === undefined ? {} : { reasoningEffort: reasoningEffort as string }),
    ...(contextWindow === undefined ? {} : { contextWindow: contextWindow as number }),
  };
}

function characterCount(value: string) {
  return Array.from(value).length;
}

/** The env or header record a prompt fills in. */
function promptRecord<T>(prompt: Pick<SyncMcpPrompt, "target">, records: { env: T; headers: T }): T {
  return records[MCP_PROMPT_FIELD[prompt.target]];
}

/** Each prompt must name a distinct key of the item whose source value is empty. */
function parseMcpPrompts(
  value: unknown,
  env: Record<string, string>,
  headers: Record<string, string>,
): SyncMcpPrompt[] | undefined {
  if (!Array.isArray(value)) return undefined;
  const prompts: SyncMcpPrompt[] = [];
  const seen = new Set<string>();
  for (const raw of value) {
    if (!isRecord(raw)) return undefined;
    const { target, name, label, secret, valuePrefix } = raw;
    if (
      !isOneOf(MCP_PROMPT_TARGETS, target) ||
      typeof name !== "string" ||
      typeof label !== "string" ||
      characterCount(label) > SYNC_PROMPT_LABEL_MAX ||
      typeof secret !== "boolean" ||
      !isOptional(valuePrefix, (item) => typeof item === "string" && characterCount(item) <= SYNC_PROMPT_PREFIX_MAX)
    ) {
      return undefined;
    }
    const record = promptRecord({ target }, { env, headers });
    if (record[name] !== "" || seen.has(`${target}:${name}`)) return undefined;
    seen.add(`${target}:${name}`);
    prompts.push({ target, name, label, secret, ...(valuePrefix === undefined ? {} : { valuePrefix: valuePrefix as string }) });
  }
  return prompts;
}

function parseMcpData(value: unknown): SyncMcpData | undefined {
  if (!isRecord(value)) return undefined;
  const { transport, command, args, url, env, headers, prompts, auth } = value;
  if (
    !isOneOf(MCP_TRANSPORTS, transport) ||
    typeof command !== "string" ||
    typeof url !== "string" ||
    !isStringArray(args) ||
    !isStringRecord(env) ||
    !isStringRecord(headers) ||
    (transport === "stdio" && !isFilled(command)) ||
    (transport === "http" && !isFilled(url)) ||
    !isOptional(auth, (item) => isOneOf(MCP_AUTH_MODES, item))
  ) {
    return undefined;
  }
  const parsedPrompts = prompts === undefined ? undefined : parseMcpPrompts(prompts, env, headers);
  if (prompts !== undefined && !parsedPrompts) return undefined;
  return {
    transport,
    command,
    args: [...args],
    url,
    env: { ...env },
    headers: { ...headers },
    ...(parsedPrompts ? { prompts: parsedPrompts } : {}),
    ...(auth === undefined ? {} : { auth: auth as "oauth" }),
  };
}

function parseSkillData(value: unknown, name: string): SyncSkillData | undefined {
  if (!isRecord(value)) return undefined;
  const { content, description, docsUrl, hasExtraFiles } = value;
  if (
    typeof content !== "string" ||
    typeof hasExtraFiles !== "boolean" ||
    !isOptional(description, (item) => typeof item === "string") ||
    !isOptional(docsUrl, (item) => typeof item === "string")
  ) {
    return undefined;
  }
  try {
    parseAiSkillMarkdown(content, "", name);
  } catch {
    return undefined;
  }
  return {
    content,
    hasExtraFiles,
    ...(description === undefined ? {} : { description: description as string }),
    ...(docsUrl === undefined ? {} : { docsUrl: docsUrl as string }),
  };
}

function parseSyncItem(value: unknown): SyncItem | undefined {
  if (!isRecord(value)) return undefined;
  const { kind, sourceId, name, sourceApp } = value;
  if (!isFilled(sourceId) || !isFilled(name) || !isFilled(sourceApp)) return undefined;
  if (kind === "profile") {
    const profile = parseProfileData(value.profile);
    return profile && { kind, sourceId, name, sourceApp, profile };
  }
  if (kind === "mcp") {
    const mcp = parseMcpData(value.mcp);
    return mcp && { kind, sourceId, name, sourceApp, mcp };
  }
  if (kind === "skill") {
    const skill = parseSkillData(value.skill, name);
    return skill && { kind, sourceId, name, sourceApp, skill };
  }
  return undefined;
}

/** Validates plugin-supplied items. Any invalid or duplicate item rejects the whole batch. */
export function parseSyncItems(value: unknown): SyncItem[] | undefined {
  if (!Array.isArray(value)) return undefined;
  const items: SyncItem[] = [];
  const keys = new Set<string>();
  for (const raw of value) {
    const item = parseSyncItem(raw);
    if (!item) return undefined;
    const key = `${item.kind}/${item.sourceId}`;
    if (keys.has(key)) return undefined;
    keys.add(key);
    items.push(item);
  }
  return items;
}

export function syncKey(pluginId: string, item: Pick<SyncItem, "kind" | "sourceId">): string {
  return `${pluginId}/${item.kind}/${item.sourceId}`;
}

/** The value already carries the prefix, in any case ("bearer abc"), or is the bare prefix ("Bearer"). */
function hasPrefix(value: string, prefix: string): boolean {
  const lower = value.toLowerCase();
  const wanted = prefix.toLowerCase();
  return lower.startsWith(wanted) || (wanted.trim() !== "" && lower === wanted.trim());
}

/**
 * The items with the values the user typed into the host confirmation.
 * `values` maps a sync key to `env:NAME` / `header:NAME` → value; only keys a
 * prompt of that item names are filled, blank values are ignored, and the
 * prompt's valuePrefix is added unless the value already starts with it
 * (in any case) or is the bare prefix.
 * The given items are not changed.
 */
export function withPromptValues(
  items: SyncItem[],
  pluginId: string,
  values: Record<string, Record<string, string>>,
): SyncItem[] {
  return items.map((item) => {
    if (item.kind !== "mcp" || !item.mcp.prompts?.length) return item;
    const typed = values[syncKey(pluginId, item)];
    if (!isRecord(typed)) return item;
    const env = { ...item.mcp.env };
    const headers = { ...item.mcp.headers };
    for (const prompt of item.mcp.prompts) {
      const value = typed[`${prompt.target}:${prompt.name}`];
      if (!isFilled(value)) continue;
      const trimmed = value.trim();
      promptRecord(prompt, { env, headers })[prompt.name] = hasPrefix(trimmed, prompt.valuePrefix ?? "")
        ? trimmed
        : `${prompt.valuePrefix ?? ""}${trimmed}`;
    }
    return { ...item, mcp: { ...item.mcp, env, headers } };
  });
}

// ---------------------------------------------------------------------------
// Written fields, fingerprints and secrets
// ---------------------------------------------------------------------------

function joinMcpArgs(args: readonly string[]) {
  return args.map((arg) => (/\s/.test(arg) ? `"${arg}"` : arg)).join(" ");
}

/** Fields an add or overwrite writes, except the add-only name and enabled flag. */
function writtenFields(item: SyncItem): SettingsObject {
  if (item.kind === "profile") {
    const profile = item.profile;
    return {
      provider: "custom",
      baseUrl: profile.baseUrl,
      apiKey: profile.apiKey,
      model: profile.model,
      apiFormat: profile.apiFormat,
      anthropicAuth: normalizeAnthropicAuth(profile.anthropicAuth),
      // Same clean-up the settings page applies on load, so a reload is not an edit.
      commonModels: profile.commonModels.filter((model) => model.trim()),
      subagentModel: (profile.subagentModel ?? "").trim(),
      reasoningEffort: isOneOf(AI_REASONING_EFFORT_CHOICES, profile.reasoningEffort)
        ? profile.reasoningEffort
        : "default",
      ...(profile.contextWindow === undefined ? {} : { contextWindow: profile.contextWindow }),
    };
  }
  if (item.kind === "mcp") {
    const mcp = item.mcp;
    return {
      transport: mcp.transport,
      command: mcp.command,
      args: joinMcpArgs(mcp.args),
      url: mcp.url,
      env: { ...mcp.env },
      headers: { ...mcp.headers },
      ...(mcp.auth === undefined ? {} : { auth: mcp.auth }),
    };
  }
  const parsed = parseAiSkillMarkdown(item.skill.content, "", item.name);
  return {
    content: parsed.content,
    description: item.skill.description?.trim() || parsed.description || "",
    docsUrl: normalizeDocumentationUrl(item.skill.docsUrl ?? "") || parsed.docsUrl || "",
  };
}

/** Field sets a fingerprint may have been taken over; contextWindow and auth are written only when present. */
function fingerprintFieldSets(kind: SyncKind): (readonly string[])[] {
  if (kind === "profile") return [PROFILE_FIELDS, [...PROFILE_FIELDS, "contextWindow"]];
  if (kind === "mcp") return [MCP_FIELDS_WITHOUT_AUTH, MCP_FIELDS];
  return [SKILL_FIELDS];
}

function sortedKeys(value: unknown) {
  return isRecord(value) ? Object.keys(value).sort() : null;
}

/** Secret-free view of written fields: no apiKey, and only the key names of MCP env and headers. */
function fingerprintView(kind: SyncKind, fields: SettingsObject, names: readonly string[]): SettingsObject {
  const view: SettingsObject = {};
  for (const name of names) {
    if (kind === "profile" && name === "apiKey") continue;
    const value = fields[name];
    view[name] = kind === "mcp" && (name === "env" || name === "headers") ? sortedKeys(value) : (value ?? null);
  }
  return view;
}

function stableJson(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(stableJson).join(",")}]`;
  if (isRecord(value)) {
    return `{${Object.keys(value)
      .sort()
      .map((key) => `${JSON.stringify(key)}:${stableJson(value[key])}`)
      .join(",")}}`;
  }
  return JSON.stringify(value ?? null);
}

function fingerprintOf(view: SettingsObject) {
  return sha256Hex(stableJson(view)).slice(0, 16);
}

function sourceFingerprint(item: SyncItem, fields: SettingsObject) {
  return fingerprintOf(fingerprintView(item.kind, fields, Object.keys(fields)));
}

/** True when the object still holds what the recorded sync wrote (renames do not count). */
function matchesRecordedFingerprint(kind: SyncKind, target: SettingsObject, recorded: string) {
  return fingerprintFieldSets(kind).some(
    (names) => fingerprintOf(fingerprintView(kind, target, names)) === recorded,
  );
}

function filledEntries(values: Record<string, string>) {
  return Object.fromEntries(Object.entries(values).filter(([, value]) => isFilled(value)));
}

/** Env and header key names a prompt of the item names. */
function promptedNames(mcp: SyncMcpData) {
  const names = { env: new Set<string>(), headers: new Set<string>() };
  for (const prompt of mcp.prompts ?? []) promptRecord(prompt, names).add(prompt.name);
  return names;
}

function withoutNames(values: Record<string, string>, names: ReadonlySet<string>) {
  return Object.fromEntries(Object.entries(values).filter(([name]) => !names.has(name)));
}

/**
 * Source env and header values that count as the source's secrets. Prompted
 * keys never do (plan R1): their value comes from the confirmation or from
 * this device, so filling one in leaves the reviewed entry unchanged.
 */
function sourceSecretRecords(mcp: SyncMcpData) {
  const prompted = promptedNames(mcp);
  return { env: withoutNames(mcp.env, prompted.env), headers: withoutNames(mcp.headers, prompted.headers) };
}

/** Secret values a source item carries; MCP keys without a value, or prompted, are not secrets. */
function secretValues(item: SyncItem): Record<string, string> | undefined {
  if (item.kind === "profile") return { apiKey: item.profile.apiKey };
  if (item.kind === "mcp") {
    const source = sourceSecretRecords(item.mcp);
    const env = filledEntries(source.env);
    const headers = filledEntries(source.headers);
    return Object.keys(env).length || Object.keys(headers).length
      ? { env: stableJson(env), headers: stableJson(headers) }
      : undefined;
  }
  return undefined;
}

const SECRET_FIELDS = { profile: ["apiKey"], mcp: ["env", "headers"], skill: [] } as const;

function pickFields(fields: SettingsObject, names: readonly string[]) {
  return Object.fromEntries(names.map((name) => [name, fields[name]]));
}

/**
 * An overwrite of this entry writes only the secrets this device lacks: the
 * source's other fields did not change since the last sync, so a MinCode edit
 * is kept and is no conflict (spec §5.1). Mirrored by the host confirmation
 * (domain/plugin-ui/ai-config.ts), which must say only the keys are filled.
 */
export function fillsSecretsOnly(entry: Pick<SyncPreviewEntry, "status" | "needsSecret" | "changedFields">): boolean {
  return entry.status === "update" && entry.needsSecret && entry.changedFields.length === 0;
}

/** The local record with every empty or missing value the source has filled in. Local keys and values stay. */
function withMissingValues(source: unknown, local: unknown) {
  const filled: Record<string, unknown> = isRecord(local) ? { ...local } : {};
  if (!isRecord(source)) return filled;
  for (const [name, value] of Object.entries(source)) {
    if (isFilled(value) && !isFilled(filled[name])) filled[name] = value;
  }
  return filled;
}

/** Source key names for env/headers, keeping the local value of every key the object already has. */
function withLocalValues(source: unknown, local: unknown) {
  const sourceRecord = isRecord(source) ? source : {};
  return Object.fromEntries(
    Object.entries(sourceRecord).map(([name, value]) => [
      name,
      isRecord(local) && typeof local[name] === "string" ? local[name] : value,
    ]),
  );
}

/**
 * Written env/headers with every prompted key set by plan R1: the value filled
 * in the confirmation (already prefixed by withPromptValues), else the value
 * this device has, else "". withLocalValues alone would let an empty local
 * value hide a value just filled in.
 */
function withPromptedValues(mcp: SyncMcpData, written: SettingsObject, target: SettingsObject): SettingsObject {
  const next: SettingsObject = { ...written };
  for (const prompt of mcp.prompts ?? []) {
    const field = MCP_PROMPT_FIELD[prompt.target];
    const record = next[field];
    if (!isRecord(record)) continue;
    const filled = promptRecord(prompt, mcp)[prompt.name];
    const local = target[field];
    const kept = isRecord(local) ? local[prompt.name] : undefined;
    next[field] = { ...record, [prompt.name]: isFilled(filled) ? filled : isFilled(kept) ? kept : "" };
  }
  return next;
}

/**
 * Fields an overwrite writes. A secret fill-in writes only the API key, or only
 * the MCP env and header values that are empty here; a changed or missing
 * secret otherwise takes the source's values; an unchanged secret keeps the
 * local secret values. Prompted MCP keys follow withPromptedValues in every case.
 */
function overwriteFields(
  item: SyncItem,
  fields: SettingsObject,
  target: SettingsObject,
  entry: SyncPreviewEntry,
): SettingsObject {
  const secretNames: readonly string[] = SECRET_FIELDS[item.kind];
  let written: SettingsObject;
  if (fillsSecretsOnly(entry)) {
    if (item.kind !== "mcp") return pickFields(fields, secretNames);
    written = Object.fromEntries(secretNames.map((name) => [name, withMissingValues(fields[name], target[name])]));
  } else if (entry.needsSecret || entry.secretChanged) {
    written = fields;
  } else {
    written = { ...fields };
    for (const name of secretNames) {
      if (item.kind === "mcp") written[name] = withLocalValues(fields[name], target[name]);
      else delete written[name];
    }
  }
  return item.kind === "mcp" ? withPromptedValues(item.mcp, written, target) : written;
}

function secretHash(key: string, secrets: Record<string, string>) {
  return sha256Hex(stableJson({ key, secrets }));
}

function filledEntriesPresent(source: Record<string, string>, target: unknown) {
  return Object.entries(source).every(
    ([name, value]) => !isFilled(value) || (isRecord(target) && isFilled(target[name])),
  );
}

/** The object lacks a secret the source has, e.g. after config sync from another device. */
function localSecretMissing(item: SyncItem, target: SettingsObject) {
  if (item.kind === "profile") return !isFilled(target.apiKey);
  if (item.kind === "mcp") {
    const source = sourceSecretRecords(item.mcp);
    return !filledEntriesPresent(source.env, target.env) || !filledEntriesPresent(source.headers, target.headers);
  }
  return false;
}

function changedFieldNames(item: SyncItem, fields: SettingsObject, target: SettingsObject) {
  const names = Object.keys(fields);
  // auth is written only when present, so a source that dropped it must still compare it.
  if (item.kind === "mcp" && !names.includes("auth")) names.push("auth");
  const source = fingerprintView(item.kind, fields, names);
  const current = fingerprintView(item.kind, target, names);
  return Object.keys(source).filter((name) => stableJson(source[name]) !== stableJson(current[name]));
}

// ---------------------------------------------------------------------------
// Preview and apply
// ---------------------------------------------------------------------------

function nameKey(name: string) {
  return name.trim().toLowerCase();
}

function namesInList(kind: SyncKind, list: readonly SettingsObject[]) {
  const names = new Set<string>();
  for (const target of list) {
    const name = target[NAME_FIELD[kind]];
    if (typeof name === "string") names.add(nameKey(name));
  }
  return names;
}

/** Items of one kind that share a name across source apps get an app prefix, e.g. `Codex · gpt6`. */
function baseNames(items: readonly SyncItem[]) {
  const appsByName = new Map<string, Set<string>>();
  for (const item of items) {
    const group = `${item.kind}/${nameKey(item.name)}`;
    const apps = appsByName.get(group) ?? new Set<string>();
    apps.add(item.sourceApp);
    appsByName.set(group, apps);
  }
  return items.map((item) => {
    const name = item.name.trim();
    const apps = appsByName.get(`${item.kind}/${nameKey(item.name)}`);
    return apps && apps.size > 1 ? `${SOURCE_APP_LABELS[item.sourceApp] ?? item.sourceApp} · ${name}` : name;
  });
}

function uniqueName(base: string, taken: Set<string>) {
  let name = base;
  for (let suffix = 2; taken.has(nameKey(name)); suffix += 1) name = `${base} (${suffix})`;
  taken.add(nameKey(name));
  return name;
}

interface PlannedItem {
  item: SyncItem;
  key: string;
  baseName: string;
  fields: SettingsObject;
  fingerprint: string;
  secretHash?: string;
}

function planItems(pluginId: string, items: readonly SyncItem[]): PlannedItem[] {
  const names = baseNames(items);
  return items.map((item, index) => {
    const key = syncKey(pluginId, item);
    const fields = writtenFields(item);
    const secrets = secretValues(item);
    return {
      item,
      key,
      baseName: names[index]!,
      fields,
      fingerprint: sourceFingerprint(item, fields),
      ...(secrets ? { secretHash: secretHash(key, secrets) } : {}),
    };
  });
}

function previewPlanned(
  pluginId: string,
  planned: readonly PlannedItem[],
  lists: SyncSettingsLists,
  secrets: SecretHashes,
): SyncPreviewEntry[] {
  const takenByKind = {
    profile: namesInList("profile", lists.aiProfiles),
    mcp: namesInList("mcp", lists.mcpServers),
    skill: namesInList("skill", lists.aiSkills),
  };
  return planned.map(({ item, key, baseName, fields, fingerprint, secretHash: sourceSecret }) => {
    const entry: SyncPreviewEntry = {
      key,
      kind: item.kind,
      name: item.name,
      sourceApp: item.sourceApp,
      status: "new",
      needsSecret: false,
      changedFields: [],
      secretChanged: false,
      notes: item.kind === "skill" && item.skill.hasExtraFiles ? ["extra-files"] : [],
    };
    const list = lists[LIST_BY_KIND[item.kind]];
    const index = findSynced(list, pluginId, item);
    if (index < 0) {
      const taken = takenByKind[item.kind];
      if (taken.has(nameKey(baseName))) entry.notes.push("possible-duplicate");
      entry.targetName = uniqueName(baseName, taken);
      return entry;
    }
    const target = list[index]!;
    const recorded = (target.syncSource as SyncSource).fingerprint;
    const currentName = target[NAME_FIELD[item.kind]];
    if (typeof currentName === "string") entry.targetName = currentName;
    const localSecret = secrets[key];
    entry.needsSecret = sourceSecret !== undefined && (localSecret === undefined || localSecretMissing(item, target));
    // A recorded hash that no longer matches covers rotated, added and cleared secrets.
    entry.secretChanged = localSecret !== undefined && localSecret !== sourceSecret;
    const sourceChanged = fingerprint !== recorded;
    if (entry.needsSecret && !sourceChanged) {
      // Only the missing secrets are written (fillsSecretsOnly), so MinCode edits are no conflict.
      entry.status = "update";
      return entry;
    }
    if (!sourceChanged && !entry.secretChanged) {
      entry.status = "unchanged";
      return entry;
    }
    // A missing secret never hides a conflict: it is still flagged by needsSecret.
    entry.status = matchesRecordedFingerprint(item.kind, target, recorded) ? "update" : "conflict";
    entry.changedFields = changedFieldNames(item, fields, target);
    return entry;
  });
}

/** Status of each source item against the settings lists. Items deleted at the source are simply absent. */
export function previewSync(
  pluginId: string,
  items: SyncItem[],
  lists: SyncSettingsLists,
  secrets: SecretHashes,
): SyncPreviewEntry[] {
  return previewPlanned(pluginId, planItems(pluginId, items), lists, secrets);
}

function sameAsReviewed(entry: SyncPreviewEntry | undefined, reviewed: SyncReviewedEntry) {
  return (
    entry !== undefined &&
    entry.status === reviewed.status &&
    entry.targetName === reviewed.targetName &&
    entry.needsSecret === reviewed.needsSecret &&
    entry.secretChanged === reviewed.secretChanged &&
    stableJson(entry.changedFields) === stableJson(reviewed.changedFields)
  );
}

function chosenActions(
  entries: readonly SyncPreviewEntry[],
  choices: readonly SyncChoice[],
  reviewed?: readonly SyncReviewedEntry[],
) {
  const entryByKey = new Map(entries.map((entry) => [entry.key, entry]));
  const reviewedByKey = reviewed && new Map(reviewed.map((entry) => [entry.key, entry]));
  const actions = new Map<string, SyncChoice["action"]>();
  const failed: { key: string; reason: string }[] = [];
  for (const choice of choices) {
    if (reviewedByKey && !actions.has(choice.key)) {
      const shown = reviewedByKey.get(choice.key);
      if (!shown || !sameAsReviewed(entryByKey.get(choice.key), shown)) {
        failed.push({ key: String(choice.key), reason: shown ? "changed-since-review" : "not-reviewed" });
        continue;
      }
    }
    const status = entryByKey.get(choice.key)?.status;
    const valid =
      !actions.has(choice.key) &&
      ((choice.action === "add" && status === "new") ||
        (choice.action === "overwrite" && (status === "update" || status === "conflict")));
    if (valid) actions.set(choice.key, choice.action);
    else failed.push({ key: String(choice.key), reason: "invalid-choice" });
  }
  return { actions, failed };
}

/**
 * Applies the chosen items to copies of the lists. Overwrites replace only the
 * written fields and keep id, name and MinCode-only fields; adds create objects
 * holding the written fields, an id and the syncSource, leaving the remaining
 * defaults to the settings page's normalizeSettings. With `reviewed`, only
 * choices whose entry still matches what the confirmation showed are applied.
 */
export function applySync(
  pluginId: string,
  items: SyncItem[],
  choices: SyncChoice[],
  lists: SyncSettingsLists,
  secrets: SecretHashes,
  createId: (prefix: string) => string,
  reviewed?: readonly SyncReviewedEntry[],
): {
  lists: SyncSettingsLists;
  secrets: SecretHashes;
  added: number;
  overwritten: number;
  failed: { key: string; reason: string }[];
} {
  const planned = planItems(pluginId, items);
  const entries = previewPlanned(pluginId, planned, lists, secrets);
  const entryByKey = new Map(entries.map((entry) => [entry.key, entry]));
  const { actions, failed } = chosenActions(entries, choices, reviewed);
  const next: SyncSettingsLists = {
    aiProfiles: [...lists.aiProfiles],
    mcpServers: [...lists.mcpServers],
    aiSkills: [...lists.aiSkills],
  };
  const nextSecrets: SecretHashes = { ...secrets };
  const takenByKind = {
    profile: namesInList("profile", next.aiProfiles),
    mcp: namesInList("mcp", next.mcpServers),
    skill: namesInList("skill", next.aiSkills),
  };
  let added = 0;
  let overwritten = 0;
  for (const { item, key, baseName, fields, fingerprint, secretHash: sourceSecret } of planned) {
    const action = actions.get(key);
    if (!action) continue;
    const list = next[LIST_BY_KIND[item.kind]];
    const syncSource: SyncSource = { pluginId, kind: item.kind, sourceId: item.sourceId, fingerprint };
    if (action === "add") {
      // The name the preview (and so the host confirmation) showed, unless taken since.
      const name = uniqueName(entryByKey.get(key)?.targetName ?? baseName, takenByKind[item.kind]);
      // Added disabled: an enabled server can be started by an AI turn (mention
      // or mcp.load_tools) without the user ever pressing Connect (spec §4.2, §7).
      if (item.kind === "mcp") list.push({ id: name, ...fields, enabled: false, syncSource });
      else if (item.kind === "skill") list.push({ id: createId("skill"), name, ...fields, enabled: true, syncSource });
      else list.push({ id: createId("profile"), name, ...fields, syncSource });
      added += 1;
    } else {
      const index = findSynced(list, pluginId, item);
      const target = list[index]!;
      const entry = entryByKey.get(key)!;
      const written = overwriteFields(item, fields, target, entry);
      const next: SettingsObject = { ...target, ...written, syncSource };
      // A source without auth clears a marker an earlier sync wrote; a fill-in writes only the secrets.
      if (item.kind === "mcp" && item.mcp.auth === undefined && !fillsSecretsOnly(entry)) delete next.auth;
      list[index] = next;
      overwritten += 1;
    }
    if (sourceSecret === undefined) delete nextSecrets[key];
    else nextSecrets[key] = sourceSecret;
  }
  return { lists: next, secrets: nextSecrets, added, overwritten, failed };
}

// ---------------------------------------------------------------------------
// SHA-256 (synchronous; crypto.subtle is async only)
// ---------------------------------------------------------------------------

const SHA256_K = Uint32Array.from([
  0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
  0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
  0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
  0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
  0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
  0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
  0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
  0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
]);

function rotr(value: number, bits: number) {
  return (value >>> bits) | (value << (32 - bits));
}

/** Hex SHA-256 of the UTF-8 encoding of `text`. */
export function sha256Hex(text: string): string {
  const bytes = new TextEncoder().encode(text);
  const blocks = new Uint8Array(Math.ceil((bytes.length + 9) / 64) * 64);
  blocks.set(bytes);
  blocks[bytes.length] = 0x80;
  const view = new DataView(blocks.buffer);
  view.setUint32(blocks.length - 8, Math.floor(bytes.length / 0x20000000));
  view.setUint32(blocks.length - 4, (bytes.length * 8) >>> 0);
  const hash = Uint32Array.from([
    0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19,
  ]);
  const words = new Uint32Array(64);
  for (let offset = 0; offset < blocks.length; offset += 64) {
    for (let i = 0; i < 16; i += 1) words[i] = view.getUint32(offset + i * 4);
    for (let i = 16; i < 64; i += 1) {
      const w15 = words[i - 15]!;
      const w2 = words[i - 2]!;
      const s0 = rotr(w15, 7) ^ rotr(w15, 18) ^ (w15 >>> 3);
      const s1 = rotr(w2, 17) ^ rotr(w2, 19) ^ (w2 >>> 10);
      words[i] = words[i - 16]! + s0 + words[i - 7]! + s1;
    }
    let a = hash[0]!;
    let b = hash[1]!;
    let c = hash[2]!;
    let d = hash[3]!;
    let e = hash[4]!;
    let f = hash[5]!;
    let g = hash[6]!;
    let h = hash[7]!;
    for (let i = 0; i < 64; i += 1) {
      const t1 = (h + (rotr(e, 6) ^ rotr(e, 11) ^ rotr(e, 25)) + ((e & f) ^ (~e & g)) + SHA256_K[i]! + words[i]!) >>> 0;
      const t2 = ((rotr(a, 2) ^ rotr(a, 13) ^ rotr(a, 22)) + ((a & b) ^ (a & c) ^ (b & c))) >>> 0;
      h = g;
      g = f;
      f = e;
      e = (d + t1) >>> 0;
      d = c;
      c = b;
      b = a;
      a = (t1 + t2) >>> 0;
    }
    hash[0] = hash[0]! + a;
    hash[1] = hash[1]! + b;
    hash[2] = hash[2]! + c;
    hash[3] = hash[3]! + d;
    hash[4] = hash[4]! + e;
    hash[5] = hash[5]! + f;
    hash[6] = hash[6]! + g;
    hash[7] = hash[7]! + h;
  }
  return Array.from(hash, (word) => word.toString(16).padStart(8, "0")).join("");
}
