// ai.config review (spec §7): what the host confirmation lists before a
// plugin's proposed AI settings are written. Pure; the frame builds the rows
// from the plugin's items, the host's own preview and the page's choices.
// Rows never carry secret values: API keys only as set / update / keep, MCP
// env and headers only as key names. Every plugin string passes through
// visibleReviewText so control, bidirectional and zero-width characters cannot
// hide or reorder part of a command line or name.
import type { SyncChoice, SyncItem, SyncPreviewEntry } from "@/domain/ai/external-sync";

export const AI_CONFIG_SKILL_EXCERPT_LENGTH = 300;
/**
 * Field names the host preview reports in changedFields (domain/ai/external-sync
 * writtenFields); each has a plugins.aiConfig.field.* label.
 */
export const AI_CONFIG_FIELD_NAMES = [
  "provider", "baseUrl", "model", "apiFormat", "anthropicAuth", "commonModels", "subagentModel", "reasoningEffort",
  "contextWindow", "transport", "command", "args", "url", "env", "headers", "auth", "content", "description", "docsUrl",
] as const;
export type AiConfigFieldName = (typeof AI_CONFIG_FIELD_NAMES)[number];

export function isAiConfigFieldName(name: string): name is AiConfigFieldName {
  return (AI_CONFIG_FIELD_NAMES as readonly string[]).includes(name);
}

interface AiConfigReviewBase {
  key: string;
  action: SyncChoice["action"];
  /** Name at the source (CC Switch). */
  name: string;
  /** Name it is written under in MinCode. */
  targetName: string;
  sourceApp: string;
  /** Add: an item of that name exists already, so a numbered name is used. */
  duplicateName: boolean;
  /** Overwrite of an item the user edited in MinCode since the last sync. */
  conflict: boolean;
  /**
   * Overwrite that fills in only the API key or the empty MCP env/header
   * values this device lacks; nothing else is written, so the dialog lists no fields.
   */
  secretsOnly: boolean;
  /** Overwrite: fields whose value changes (host preview), never secret values. Empty for adds. */
  changedFields: string[];
}

export interface AiConfigReviewPrompt {
  target: "env" | "header";
  name: string;
  label: string;
  secret: boolean;
  filled: boolean;
}

export type AiConfigReviewRow =
  | (AiConfigReviewBase & {
      kind: "profile";
      baseUrl: string;
      model: string;
      apiFormat: string;
      /** Anthropic format only: how the key is sent (x-api-key header or Authorization: Bearer). */
      anthropicAuth?: "x-api-key" | "bearer";
      /** set: added with a key; update: the saved key is replaced; keep: the saved key stays. */
      apiKey: "set" | "update" | "keep";
    })
  | (AiConfigReviewBase & {
      kind: "mcp";
      transport: "stdio" | "http";
      /** stdio: the full command line; words with spaces, quotes or other shell characters are single-quoted. */
      commandLine: string;
      url: string;
      envKeys: string[];
      headerKeys: string[];
      /** env/header values: set on add; update or keep on overwrite; none when the source has no values. */
      secretValues: "set" | "update" | "keep" | "none";
      /** oauth: the connector needs a sign-in before it can connect. */
      auth: "oauth" | "none";
      /** Values the dialog asks for; filled: this device's synced object already has a non-empty value. */
      prompts: AiConfigReviewPrompt[];
    })
  | (AiConfigReviewBase & {
      kind: "skill";
      description: string;
      excerpt: string;
      truncated: boolean;
      /** The whole skill text, for the dialog's expandable full view. */
      fullText: string;
      extraFiles: boolean;
    });

// Same character set, marker and quoting as visibleTerminalText / formatTerminalArgv
// (domain/terminal/profiles.ts, used by the process-launch confirmation), plus
// the remaining invisible formatting characters; kept here so this module stays
// free of runtime imports. C0/C1 controls, line/paragraph separators,
// bidirectional embeddings/overrides/isolates/marks, zero-width characters, BOM.
// eslint-disable-next-line no-control-regex -- matching control characters is the point
const HIDDEN_CHARACTERS = /[\u0000-\u001f\u007f-\u009f\u061c\u200b-\u200f\u2028-\u202e\u2060-\u2064\u2066-\u2069\ufeff]/g;
const SHELL_SAFE_WORD = /^[A-Za-z0-9_@%+=:,./-]+$/;

/** Replaces characters that render invisibly or reorder text with a visible `\u{…}` marker. */
export function visibleReviewText(value: string, options: { multiline?: boolean } = {}): string {
  return value.replace(HIDDEN_CHARACTERS, (character) =>
    options.multiline && (character === "\n" || character === "\t")
      ? character
      : `\\u{${(character.codePointAt(0) ?? 0).toString(16)}}`,
  );
}

/** Quotes one command word for display (POSIX style); never used to build a command line. */
function commandWord(word: string) {
  const visible = visibleReviewText(word);
  if (visible && SHELL_SAFE_WORD.test(visible)) return visible;
  return `'${visible.replace(/'/g, `'\\''`)}'`;
}

/** Same rule as fillsSecretsOnly in domain/ai/external-sync, which decides what the write fills in. */
function fillsSecretsOnly(entry: SyncPreviewEntry) {
  return entry.status === "update" && entry.needsSecret && entry.changedFields.length === 0;
}

function hasValues(values: Record<string, string>) {
  return Object.values(values).some((value) => value.trim() !== "");
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

/** The local MCP object synced from the entry's source item (same key scheme as syncKey in domain/ai/external-sync). */
function syncedMcpServer(mcpServers: readonly Record<string, unknown>[], key: string) {
  return mcpServers.find((server) => {
    const source = server.syncSource;
    return isRecord(source) && source.kind === "mcp" && `${String(source.pluginId)}/mcp/${String(source.sourceId)}` === key;
  });
}

function hasLocalValue(server: Record<string, unknown> | undefined, field: "env" | "headers", name: string) {
  const record = server?.[field];
  return isRecord(record) && typeof record[name] === "string" && record[name].trim() !== "";
}

function choiceAllowed(choice: SyncChoice, entry: SyncPreviewEntry | undefined) {
  if (!entry) return false;
  return choice.action === "add" ? entry.status === "new" : entry.status === "update" || entry.status === "conflict";
}

/**
 * Rows for the choices the write would carry out, in item order. `entries` is
 * the host preview of exactly these items (previewSync keeps their order).
 * Choices that do not fit the preview (e.g. overwriting an unchanged item) are
 * left out; the write rejects them too. `mcpServers` is the saved MCP list the
 * preview compared against; it only decides which prompts are already filled.
 */
export function aiConfigReviewRows(
  items: readonly SyncItem[],
  entries: readonly SyncPreviewEntry[],
  choices: readonly SyncChoice[],
  mcpServers: readonly Record<string, unknown>[] = [],
): AiConfigReviewRow[] {
  const entryByKey = new Map(entries.map((entry) => [entry.key, entry]));
  const actionByKey = new Map<string, SyncChoice["action"]>();
  for (const choice of choices) {
    if (!actionByKey.has(choice.key) && choiceAllowed(choice, entryByKey.get(choice.key))) actionByKey.set(choice.key, choice.action);
  }
  const rows: AiConfigReviewRow[] = [];
  for (const [index, item] of items.entries()) {
    const entry = entries[index];
    const action = entry?.kind === item.kind ? actionByKey.get(entry.key) : undefined;
    if (!entry || !action) continue;
    actionByKey.delete(entry.key);
    const base: AiConfigReviewBase = {
      key: entry.key,
      action,
      name: visibleReviewText(item.name),
      targetName: visibleReviewText(entry.targetName ?? item.name),
      sourceApp: visibleReviewText(item.sourceApp),
      duplicateName: action === "add" && entry.notes.includes("possible-duplicate"),
      conflict: action === "overwrite" && entry.status === "conflict",
      secretsOnly: action === "overwrite" && fillsSecretsOnly(entry),
      changedFields: action === "overwrite" ? entry.changedFields.map((name) => visibleReviewText(name)) : [],
    };
    const secretUpdate = action === "overwrite" && (entry.needsSecret || entry.secretChanged);
    if (item.kind === "profile") {
      rows.push({
        ...base,
        kind: "profile",
        baseUrl: visibleReviewText(item.profile.baseUrl),
        model: visibleReviewText(item.profile.model),
        apiFormat: item.profile.apiFormat,
        // Missing means x-api-key, as normalizeAnthropicAuth writes it.
        ...(item.profile.apiFormat === "anthropic"
          ? { anthropicAuth: item.profile.anthropicAuth === "bearer" ? ("bearer" as const) : ("x-api-key" as const) }
          : {}),
        apiKey: action === "add" ? "set" : secretUpdate ? "update" : "keep",
      });
    } else if (item.kind === "mcp") {
      const mcp = item.mcp;
      const local = syncedMcpServer(mcpServers, entry.key);
      rows.push({
        ...base,
        kind: "mcp",
        transport: mcp.transport,
        commandLine: mcp.transport === "stdio" ? [mcp.command, ...mcp.args].map(commandWord).join(" ") : "",
        url: mcp.transport === "http" ? visibleReviewText(mcp.url) : "",
        envKeys: Object.keys(mcp.env).map((name) => visibleReviewText(name)),
        headerKeys: Object.keys(mcp.headers).map((name) => visibleReviewText(name)),
        secretValues: secretUpdate
          ? "update"
          : !hasValues(mcp.env) && !hasValues(mcp.headers)
            ? "none"
            : action === "add"
              ? "set"
              : "keep",
        auth: mcp.auth === "oauth" ? "oauth" : "none",
        prompts: (mcp.prompts ?? []).map((prompt) => ({
          target: prompt.target,
          name: visibleReviewText(prompt.name),
          label: visibleReviewText(prompt.label),
          secret: prompt.secret,
          filled: hasLocalValue(local, prompt.target === "env" ? "env" : "headers", prompt.name),
        })),
      });
    } else {
      const characters = Array.from(item.skill.content);
      rows.push({
        ...base,
        kind: "skill",
        description: visibleReviewText(item.skill.description ?? ""),
        excerpt: visibleReviewText(characters.slice(0, AI_CONFIG_SKILL_EXCERPT_LENGTH).join(""), { multiline: true }),
        truncated: characters.length > AI_CONFIG_SKILL_EXCERPT_LENGTH,
        fullText: visibleReviewText(item.skill.content, { multiline: true }),
        extraFiles: item.skill.hasExtraFiles,
      });
    }
  }
  return rows;
}
