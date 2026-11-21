/**
 * Plugin HTTP code generators (contribution point `httpCodeGenerators`),
 * pure logic.
 *
 * Trust boundary: the Go host (backend/plugin/contrib_http_code_generators.go)
 * validates each entry and its `.tmpl` template at install and serves the
 * template by the sha256 of its canonical bytes. This module re-checks the
 * snapshot defensively (the "frontend" layer), re-parses the template with
 * the contract vocabulary and renders it against the redacted request model
 * (domain/http/code-generator-model.ts). No plugin code runs and the plugin
 * process never sees request data; the output is plain text.
 */
import contributionContract from "./contribution-points.json" with { type: "json" };
import type { PointContribution } from "./contributions.ts";
import { buildRedactedHttpCodeModel } from "../http/code-generator-model.ts";
import {
  compileCodeTemplate,
  renderCodeTemplate,
  type CodeTemplateLimits,
  type CodeTemplateVocabulary,
  type CompiledCodeTemplate,
} from "../http/code-template.ts";
import type { HttpRequestDraft } from "../http/request-collection.ts";

export const HTTP_CODE_GENERATORS_CONTRIBUTION_POINT = "httpCodeGenerators";

interface PointSpec {
  maxEntries: number;
  limits: Record<string, number>;
  lists: Record<string, string[]>;
}

const spec = (contributionContract as unknown as { points: Record<string, PointSpec> }).points[HTTP_CODE_GENERATORS_CONTRIBUTION_POINT]!;
const MAX_ENTRIES = spec.maxEntries;
const TITLE_MAX_RUNES = spec.limits.titleMaxRunes ?? 48;
const LOCALES_MAX = spec.limits.localesMax ?? 16;
/** Rendered output cap (bytes). */
export const HTTP_CODE_OUTPUT_MAX_BYTES = spec.limits.outputMaxBytes ?? 262144;
export const HTTP_CODE_TEMPLATE_MAX_BYTES = (contributionContract as unknown as { points: Record<string, { assets?: { maxBytes?: number } }> })
  .points[HTTP_CODE_GENERATORS_CONTRIBUTION_POINT]?.assets?.maxBytes ?? 32768;
export const HTTP_CODE_ESCAPES: readonly string[] = spec.lists.escapes ?? [];
export const HTTP_CODE_TEMPLATE_LIMITS: CodeTemplateLimits = {
  maxNodes: spec.limits.templateNodesMax ?? 2048,
  maxDepth: spec.limits.sectionDepthMax ?? 3,
};
export const HTTP_CODE_TEMPLATE_VOCABULARY: CodeTemplateVocabulary = {
  variables: spec.lists.variables ?? [],
  itemVariables: spec.lists.itemVariables ?? [],
  listSections: spec.lists.listSections ?? [],
  flagSections: spec.lists.flagSections ?? [],
  itemFlagSections: spec.lists.itemFlagSections ?? [],
  filters: spec.lists.filters ?? [],
};
const ID_PATTERN = /^[a-z0-9][a-z0-9-]{0,31}$/;
const ASSET_KEY_PATTERN = /^[0-9a-f]{64}$/;

export interface PluginHttpCodeGenerator {
  /** `<pluginId>.<id>`. */
  key: string;
  pluginId: string;
  pluginName: string;
  id: string;
  title: string;
  titleLocalized?: Record<string, string>;
  escape: string;
  /** sha256 content key of the `.tmpl` asset. */
  template: string;
}

export interface PluginHttpCodeGeneratorIssue {
  pluginId: string;
  stage: "host" | "frontend";
  entry: number;
  id?: string;
  message: string;
}

export interface PluginHttpCodeGeneratorRegistry {
  generators: PluginHttpCodeGenerator[];
  issues: PluginHttpCodeGeneratorIssue[];
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === "object" && value !== null && !Array.isArray(value) ? (value as Record<string, unknown>) : undefined;
}

function isHiddenCodePoint(code: number): boolean {
  return (
    code < 0x20 ||
    (code >= 0x7f && code <= 0x9f) ||
    code === 0x2028 ||
    code === 0x2029 ||
    (code >= 0x202a && code <= 0x202e) ||
    (code >= 0x2066 && code <= 0x2069) ||
    (code >= 0x200b && code <= 0x200f) ||
    code === 0x2060 ||
    code === 0xfeff
  );
}

function validLine(value: unknown, maxRunes: number): value is string {
  if (typeof value !== "string" || !value.trim() || [...value].length > maxRunes) return false;
  for (const char of value) if (isHiddenCodePoint(char.codePointAt(0) ?? 0)) return false;
  return true;
}

function localizedMap(value: unknown): Record<string, string> | undefined {
  const record = objectValue(value);
  if (!record) return undefined;
  const result: Record<string, string> = {};
  for (const [locale, text] of Object.entries(record).slice(0, LOCALES_MAX)) {
    if (locale.trim() && locale.length <= 32 && validLine(text, TITLE_MAX_RUNES)) result[locale] = text.trim();
  }
  return Object.keys(result).length ? result : undefined;
}

function parseEntry(contribution: PointContribution, value: unknown): PluginHttpCodeGenerator | string {
  const record = objectValue(value);
  if (!record) return "malformed HTTP code generator entry";
  const id = typeof record.id === "string" ? record.id : "";
  if (!ID_PATTERN.test(id)) return "invalid HTTP code generator id";
  if (!validLine(record.title, TITLE_MAX_RUNES)) return "invalid HTTP code generator title";
  if (typeof record.escape !== "string" || !HTTP_CODE_ESCAPES.includes(record.escape)) return "invalid HTTP code generator escape";
  if (typeof record.template !== "string" || !ASSET_KEY_PATTERN.test(record.template)) return "invalid HTTP code generator template";
  const titleLocalized = localizedMap(record.titleLocalized);
  return {
    key: `${contribution.pluginId}.${id}`,
    pluginId: contribution.pluginId,
    pluginName: contribution.pluginName.trim() || contribution.pluginId,
    id,
    title: record.title.trim(),
    ...(titleLocalized ? { titleLocalized } : {}),
    escape: record.escape,
    template: record.template,
  };
}

/**
 * Normalizes the `httpCodeGenerators` snapshot. Generators only render text
 * the user asks for, so every enabled plugin contributes regardless of the
 * workspace. Invalid entries are skipped and reported.
 */
export function buildPluginHttpCodeGeneratorRegistry(contributions: readonly PointContribution[]): PluginHttpCodeGeneratorRegistry {
  const generators: PluginHttpCodeGenerator[] = [];
  const issues: PluginHttpCodeGeneratorIssue[] = [];
  for (const contribution of contributions) {
    for (const issue of contribution.point.issues) {
      issues.push({ pluginId: contribution.pluginId, stage: "host", entry: issue.entry, message: issue.message, ...(issue.id ? { id: issue.id } : {}) });
    }
    const seen = new Set<string>();
    contribution.point.entries.slice(0, MAX_ENTRIES).forEach((value, entry) => {
      const parsed = parseEntry(contribution, value);
      if (typeof parsed === "string" || seen.has(parsed.id)) {
        const id = objectValue(value)?.id;
        issues.push({
          pluginId: contribution.pluginId,
          stage: "frontend",
          entry,
          message: typeof parsed === "string" ? parsed : "duplicate HTTP code generator id",
          ...(typeof id === "string" && id ? { id } : {}),
        });
        return;
      }
      seen.add(parsed.id);
      generators.push(parsed);
    });
  }
  return { generators, issues };
}

/** Parses a template with the contract vocabulary and limits; throws CodeTemplateError. */
export function compileHttpCodeTemplate(source: string): CompiledCodeTemplate {
  return compileCodeTemplate(source.replace(/^\uFEFF/, "").replace(/\r\n/g, "\n"), HTTP_CODE_TEMPLATE_LIMITS, HTTP_CODE_TEMPLATE_VOCABULARY);
}

/** Renders a generator's verified template for a request (redacted model, capped output). */
export function renderPluginHttpCode(generator: Pick<PluginHttpCodeGenerator, "escape">, source: string, request: HttpRequestDraft): string {
  const template = compileHttpCodeTemplate(source);
  return renderCodeTemplate(template, buildRedactedHttpCodeModel(request), generator.escape, HTTP_CODE_OUTPUT_MAX_BYTES);
}

/** Install preview: the generator titles a manifest declares. */
export function httpCodeGeneratorPreviewSummary(contributes: unknown): string[] {
  const entries = objectValue(contributes)?.[HTTP_CODE_GENERATORS_CONTRIBUTION_POINT];
  if (!Array.isArray(entries)) return [];
  return entries.slice(0, MAX_ENTRIES).flatMap((entry) => {
    const record = objectValue(entry);
    return record && validLine(record.title, TITLE_MAX_RUNES) ? [record.title.trim()] : [];
  });
}
