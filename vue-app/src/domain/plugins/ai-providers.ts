/**
 * Frontend stage of the `aiProviders` contribution point: re-validation of
 * provider presets the Go host already accepted
 * (backend/plugin/contrib_ai_providers.go). No DOM, Vue or IPC
 * here; the runtime wiring lives in services/contributions/ai-providers.ts and
 * stores/plugin-ai-providers.ts, profile helpers in
 * domain/ai/plugin-provider-profile.ts.
 *
 * A preset only fills an ordinary AI profile in Settings → AI → Connection.
 * The user types the API key into the host; the plugin never receives it. The
 * request host is derived here from the base URL (never taken from the
 * snapshot) and shown next to the plugin name, and placeholders may only sit
 * in the URL path, so a preset cannot pass itself off as a known service or
 * redirect a key.
 */
import { AI_ANTHROPIC_AUTH_MODES, type AiAnthropicAuth } from "@/domain/ai/anthropic-auth";
import { AI_API_FORMATS, type AiApiFormat } from "@/domain/ai/api-format";
import type { PointContribution } from "@/domain/plugins/contributions";

export const AI_PROVIDERS_CONTRIBUTION_POINT = "aiProviders";

/** Mirrors contribution-points.json aiProviders limits (the host enforces them first). */
export const AI_PROVIDER_LIMITS = {
  maxEntries: 32,
  labelMaxRunes: 48,
  noteMaxRunes: 120,
  localesMax: 16,
  modelsMax: 32,
  modelIdMaxLength: 128,
  urlMaxLength: 512,
  placeholdersMax: 2,
  contextWindowMax: 10000000,
} as const;

export const AI_PROVIDER_TIERS = ["free", "limited"] as const;
export type AiProviderTier = (typeof AI_PROVIDER_TIERS)[number];
export const AI_PROVIDER_REGIONS = ["cn", "global", "unknown"] as const;
export type AiProviderRegion = (typeof AI_PROVIDER_REGIONS)[number];

export interface PluginAiProviderModel {
  id: string;
  free: boolean;
  tools: boolean;
  note?: string;
  noteLocalized?: Record<string, string>;
}

export interface PluginAiProviderPlaceholder {
  name: string;
  label: string;
  labelLocalized?: Record<string, string>;
}

export interface PluginAiProvider {
  pluginId: string;
  pluginName: string;
  id: string;
  label: string;
  labelLocalized?: Record<string, string>;
  baseUrl: string;
  host: string;
  placeholders: PluginAiProviderPlaceholder[];
  apiFormat: AiApiFormat;
  anthropicAuth?: AiAnthropicAuth;
  model: string;
  models: PluginAiProviderModel[];
  contextWindow: number;
  maxInputTokens: number;
  maxOutputTokens: number;
  tier: AiProviderTier;
  region: AiProviderRegion;
  verifiedAt: string;
  quotaNote?: string;
  quotaNoteLocalized?: Record<string, string>;
  caveat?: string;
  caveatLocalized?: Record<string, string>;
  signupUrl: string;
  docsUrl?: string;
}

export interface AiProviderIssue {
  pluginId: string;
  stage: "host" | "frontend";
  entry: number;
  id?: string;
  message: string;
}

const PLUGIN_ID_PATTERN = /^[a-z0-9][a-z0-9._-]{0,63}$/;
const NAME_PATTERN = /^[a-z0-9][a-z0-9-]{0,31}$/;
const MODEL_ID_PATTERN = /^[A-Za-z0-9@][A-Za-z0-9._:/@+-]*$/;
const PLACEHOLDER_NAME_PATTERN = /^[a-z_]{1,24}$/;
const PLACEHOLDER_PATTERN = /\{([a-z_]{1,24})\}/g;
const DATE_PATTERN = /^\d{4}-\d{2}-\d{2}$/;
const LOCALE_PATTERN = /^[A-Za-z]{2,8}(-[A-Za-z0-9]{1,8}){0,3}$/;
// eslint-disable-next-line no-control-regex -- rejecting control characters is the point
const UNSAFE_TEXT_PATTERN = /[\u0000-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069\u200b-\u200f\u2060\ufeff]/;

type Invalid = { ok: false; error: string };
const invalid = (error: string): Invalid => ({ ok: false, error });

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function line(value: unknown, maxRunes: number, required: boolean): string | undefined | false {
  if (value === undefined && !required) return undefined;
  if (typeof value !== "string") return false;
  const trimmed = value.trim();
  if (!trimmed) return required ? false : undefined;
  if ([...trimmed].length > maxRunes || UNSAFE_TEXT_PATTERN.test(trimmed)) return false;
  return trimmed;
}

function localized(value: unknown, maxRunes: number): Record<string, string> | undefined | false {
  if (value === undefined) return undefined;
  if (!isRecord(value)) return false;
  const entries = Object.entries(value);
  if (entries.length > AI_PROVIDER_LIMITS.localesMax) return false;
  const out: Record<string, string> = {};
  for (const [locale, text] of entries) {
    const normalized = line(text, maxRunes, true);
    if (!normalized || !LOCALE_PATTERN.test(locale)) return false;
    out[locale] = normalized;
  }
  return entries.length ? out : undefined;
}

/** A text and its translations; the translations need the base text. */
function text(raw: Record<string, unknown>, field: string, maxRunes: number, required: boolean) {
  const base = line(raw[field], maxRunes, required);
  const translations = localized(raw[`${field}Localized`], maxRunes);
  if (base === false || translations === false || (!base && translations)) return false;
  return { base, translations };
}

function httpsURL(value: unknown, required: boolean): string | undefined | false {
  if (value === undefined && !required) return undefined;
  if (typeof value !== "string" || !value.trim()) return required ? false : undefined;
  const trimmed = value.trim();
  if (trimmed.length > AI_PROVIDER_LIMITS.urlMaxLength || UNSAFE_TEXT_PATTERN.test(trimmed)) return false;
  try {
    const url = new URL(trimmed);
    if (url.protocol !== "https:" || !url.hostname || url.username || url.password) return false;
  } catch {
    return false;
  }
  return trimmed;
}

/** The base URL's host when every {name} is declared, used and in the path. */
function baseURLHost(baseUrl: string, placeholders: PluginAiProviderPlaceholder[]): string | Invalid {
  const declared = new Set(placeholders.map((item) => item.name));
  const used = new Set([...baseUrl.matchAll(PLACEHOLDER_PATTERN)].map((match) => match[1]!));
  for (const name of used) if (!declared.has(name)) return invalid(`baseUrl placeholder {${name}} is not declared`);
  for (const name of declared) if (!used.has(name)) return invalid(`placeholder "${name}" is not used in baseUrl`);
  const substituted = baseUrl.replace(PLACEHOLDER_PATTERN, "x");
  if (/[{}?#]/.test(substituted)) return invalid("baseUrl must not have stray braces, a query or a fragment");
  if (!httpsURL(substituted, true)) return invalid("baseUrl must be an https URL with a host and no user name or password");
  const url = new URL(substituted);
  const pathStart = "https://".length + url.host.length;
  const first = baseUrl.indexOf("{");
  if (first >= 0 && (first <= pathStart || baseUrl[pathStart] !== "/")) return invalid("baseUrl placeholders must be in the path");
  return url.hostname;
}

function validatePlaceholders(value: unknown): PluginAiProviderPlaceholder[] | Invalid {
  if (value === undefined) return [];
  if (!Array.isArray(value) || value.length > AI_PROVIDER_LIMITS.placeholdersMax) return invalid("placeholders is invalid");
  const out: PluginAiProviderPlaceholder[] = [];
  for (const raw of value) {
    if (!isRecord(raw) || typeof raw.name !== "string" || !PLACEHOLDER_NAME_PATTERN.test(raw.name)) return invalid("placeholder name is invalid");
    if (out.some((item) => item.name === raw.name)) return invalid(`placeholder "${raw.name}" is duplicated`);
    const label = text(raw, "label", AI_PROVIDER_LIMITS.labelMaxRunes, true);
    if (!label) return invalid(`placeholder "${raw.name}" label is invalid`);
    out.push({ name: raw.name, label: label.base!, ...(label.translations ? { labelLocalized: label.translations } : {}) });
  }
  return out;
}

function validateModels(value: unknown): PluginAiProviderModel[] | Invalid {
  if (!Array.isArray(value) || value.length === 0 || value.length > AI_PROVIDER_LIMITS.modelsMax) return invalid("models must have 1 to 32 entries");
  const out: PluginAiProviderModel[] = [];
  for (const raw of value) {
    const id = isRecord(raw) && typeof raw.id === "string" ? raw.id.trim() : "";
    if (!id || id.length > AI_PROVIDER_LIMITS.modelIdMaxLength || !MODEL_ID_PATTERN.test(id)) return invalid("model id is invalid");
    if (out.some((model) => model.id === id)) return invalid(`model id "${id}" is duplicated`);
    const record = raw as Record<string, unknown>;
    if ((record.free !== undefined && typeof record.free !== "boolean") || (record.tools !== undefined && typeof record.tools !== "boolean")) {
      return invalid(`model "${id}" flags are invalid`);
    }
    const note = text(record, "note", AI_PROVIDER_LIMITS.noteMaxRunes, false);
    if (!note) return invalid(`model "${id}" note is invalid`);
    out.push({
      id,
      free: record.free === true,
      tools: record.tools !== false,
      ...(note.base ? { note: note.base } : {}),
      ...(note.translations ? { noteLocalized: note.translations } : {}),
    });
  }
  return out;
}

function tokenCount(value: unknown, max: number): number | undefined {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 1 && value <= max ? value : undefined;
}

function includes<T extends string>(list: readonly T[], value: unknown): value is T {
  return typeof value === "string" && (list as readonly string[]).includes(value);
}

/** Re-validates one host-accepted entry (defense in depth). */
export function validateAiProviderEntry(
  pluginId: string,
  raw: unknown,
): { ok: true; provider: Omit<PluginAiProvider, "pluginId" | "pluginName"> } | Invalid {
  if (!isRecord(raw)) return invalid("entry must be an object");
  const id = typeof raw.id === "string" ? raw.id.trim() : "";
  if (!id.startsWith(`${pluginId}.`) || !NAME_PATTERN.test(id.slice(pluginId.length + 1))) return invalid(`AI provider id "${id}" is invalid`);
  const label = text(raw, "label", AI_PROVIDER_LIMITS.labelMaxRunes, true);
  const quotaNote = text(raw, "quotaNote", AI_PROVIDER_LIMITS.noteMaxRunes, false);
  const caveat = text(raw, "caveat", AI_PROVIDER_LIMITS.noteMaxRunes, false);
  if (!label || !quotaNote || !caveat) return invalid(`AI provider "${id}" text is invalid`);
  if (!includes(AI_PROVIDER_TIERS, raw.tier)) return invalid(`AI provider "${id}" tier is invalid`);
  if (raw.tier === "limited" && !caveat.base) return invalid(`AI provider "${id}" with tier "limited" needs a caveat`);
  if (!includes(AI_PROVIDER_REGIONS, raw.region)) return invalid(`AI provider "${id}" region is invalid`);
  if (typeof raw.verifiedAt !== "string" || !DATE_PATTERN.test(raw.verifiedAt) || Number.isNaN(Date.parse(raw.verifiedAt))) {
    return invalid(`AI provider "${id}" verifiedAt is invalid`);
  }
  if (!includes(AI_API_FORMATS, raw.apiFormat)) return invalid(`AI provider "${id}" apiFormat is invalid`);
  if (raw.anthropicAuth !== undefined && (raw.apiFormat !== "anthropic" || !includes(AI_ANTHROPIC_AUTH_MODES, raw.anthropicAuth))) {
    return invalid(`AI provider "${id}" anthropicAuth is invalid`);
  }
  const placeholders = validatePlaceholders(raw.placeholders);
  if ("ok" in placeholders) return placeholders;
  const baseUrl = typeof raw.baseUrl === "string" ? raw.baseUrl.trim() : "";
  if (baseUrl.length > AI_PROVIDER_LIMITS.urlMaxLength) return invalid(`AI provider "${id}" baseUrl is too long`);
  const host = baseURLHost(baseUrl, placeholders);
  if (typeof host !== "string") return host;
  const signupUrl = httpsURL(raw.signupUrl, true);
  const docsUrl = httpsURL(raw.docsUrl, false);
  if (!signupUrl || docsUrl === false) return invalid(`AI provider "${id}" signupUrl or docsUrl is invalid`);
  const models = validateModels(raw.models);
  if ("ok" in models) return models;
  const model = typeof raw.model === "string" ? raw.model.trim() : "";
  if (!models.some((item) => item.id === model)) return invalid(`AI provider "${id}" model must be one of its models`);
  const contextWindow = tokenCount(raw.contextWindow, AI_PROVIDER_LIMITS.contextWindowMax);
  const maxInputTokens = contextWindow && tokenCount(raw.maxInputTokens, contextWindow);
  const maxOutputTokens = contextWindow && tokenCount(raw.maxOutputTokens, contextWindow);
  if (!contextWindow || !maxInputTokens || !maxOutputTokens) return invalid(`AI provider "${id}" token limits are invalid`);
  return {
    ok: true,
    provider: {
      id,
      label: label.base!,
      ...(label.translations ? { labelLocalized: label.translations } : {}),
      baseUrl,
      host,
      placeholders,
      apiFormat: raw.apiFormat,
      ...(raw.anthropicAuth !== undefined ? { anthropicAuth: raw.anthropicAuth as AiAnthropicAuth } : {}),
      model,
      models,
      contextWindow,
      maxInputTokens,
      maxOutputTokens,
      tier: raw.tier,
      region: raw.region,
      verifiedAt: raw.verifiedAt,
      ...(quotaNote.base ? { quotaNote: quotaNote.base } : {}),
      ...(quotaNote.translations ? { quotaNoteLocalized: quotaNote.translations } : {}),
      ...(caveat.base ? { caveat: caveat.base } : {}),
      ...(caveat.translations ? { caveatLocalized: caveat.translations } : {}),
      signupUrl,
      ...(docsUrl ? { docsUrl } : {}),
    },
  };
}

/** Collects the presets of every enabled plugin (the point is global), in plugin id then declaration order. */
export function collectAiProviders(contributions: readonly PointContribution[]): {
  providers: PluginAiProvider[];
  issues: AiProviderIssue[];
} {
  const providers: PluginAiProvider[] = [];
  const issues: AiProviderIssue[] = [];
  const ordered = [...contributions].sort((a, b) => (a.pluginId < b.pluginId ? -1 : a.pluginId > b.pluginId ? 1 : 0));
  for (const contribution of ordered) {
    if (!PLUGIN_ID_PATTERN.test(contribution.pluginId)) continue;
    for (const issue of contribution.point.issues) {
      issues.push({ pluginId: contribution.pluginId, stage: "host", entry: issue.entry, message: issue.message, ...(issue.id ? { id: issue.id } : {}) });
    }
    const seen = new Set<string>();
    contribution.point.entries.slice(0, AI_PROVIDER_LIMITS.maxEntries).forEach((raw, entry) => {
      const result = validateAiProviderEntry(contribution.pluginId, raw);
      if (!result.ok) {
        issues.push({ pluginId: contribution.pluginId, stage: "frontend", entry, message: result.error });
        return;
      }
      if (seen.has(result.provider.id)) {
        issues.push({ pluginId: contribution.pluginId, stage: "frontend", entry, id: result.provider.id, message: `duplicate AI provider id "${result.provider.id}"` });
        return;
      }
      seen.add(result.provider.id);
      providers.push({ ...result.provider, pluginId: contribution.pluginId, pluginName: contribution.pluginName });
    });
  }
  return { providers, issues };
}

/** A plugin text for the UI locale: exact tag, then its language, then the base text. */
export function aiProviderText(value: string | undefined, translations: Record<string, string> | undefined, locale: string): string {
  const language = locale.split("-")[0]!;
  return translations?.[locale] ?? translations?.[language] ?? value ?? "";
}

/** Number of presets an install preview announces (file references count; the host validates them). */
export function aiProviderPreviewCount(contributes: unknown): number {
  const entries = isRecord(contributes) ? contributes[AI_PROVIDERS_CONTRIBUTION_POINT] : undefined;
  return Array.isArray(entries) ? Math.min(entries.length, AI_PROVIDER_LIMITS.maxEntries) : 0;
}
