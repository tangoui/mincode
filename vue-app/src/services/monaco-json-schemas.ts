/**
 * Monaco stage of the `jsonValidation` contribution point. Keeps Monaco's
 * JSON defaults (`mincodeJsonDefaults`) in sync with the plugin schemas that
 * match the open JSON models:
 *
 * - On model create/dispose/language change and on contribution change
 *   (debounced 100 ms), every open json/jsonc model is matched against the
 *   project-relative path of its file (basename only outside projects).
 * - Matched schemas are fetched lazily as contribution assets, parsed, and
 *   their regexes checked (static heuristics, then one worker probe per
 *   schema). A schema that fails is not registered and reported as a
 *   frontend issue.
 * - Accepted schemas are registered under `mincode-plugin-schema://` URIs with
 *   `fileMatch` set to the exact model URIs, and schema requests stay
 *   disabled, so nothing is ever fetched from the network. Schemas that
 *   other code registered are preserved; disposal removes only plugin ones.
 *
 * Monaco is reached only through `registerMonacoContributionHook`, so
 * importing this module never loads it.
 */
import { regexSafetyIssue } from "@/domain/editor/regex-safety";
import {
  JSON_VALIDATION_CONTRIBUTION_POINT,
  PLUGIN_JSON_SCHEMA_URI_PREFIX,
  collectJsonSchemaRegexes,
  matchJsonSchemas,
  pluginJsonSchemaUri,
  type JsonSchemaEntry,
  type JsonSchemaTarget,
  type JsonValidationIssue,
} from "@/domain/plugins/json-validation";
import { registerMonacoContributionHook, type MonacoApi } from "@/services/monaco-contributions";
import { probeRegexes } from "@/services/regex-probe";
import { frameworkPluginAllowed, frameworkRegistryChanged } from "@/services/editor-framework-registry";

import type { editor } from "@/services/monaco-runtime.js";

type TextModel = editor.ITextModel;
type Disposable = { dispose(): void };

/** Debounce for model and contribution changes. */
export const JSON_SCHEMA_SYNC_DELAY_MS = 100;
const JSON_LANGUAGES = new Set(["json", "jsonc"]);
const MAX_CACHED_SCHEMAS = 64;

/** Asset fetcher (tests replace it); resolves canonical bytes by key. */
export type JsonSchemaFetcher = (pluginId: string, keys: readonly string[]) => Promise<Map<string, Uint8Array>>;

type SchemaVerdict = { ok: true; schema: unknown } | { ok: false; message: string };

interface PluginSchema {
  uri: string;
  fileMatch: string[];
  schema: unknown;
}

let entries: readonly JsonSchemaEntry[] = [];
let rootsProvider: () => readonly string[] = () => [];
let active: MonacoApi | undefined;
let unregisterHook: (() => void) | undefined;
let timer: ReturnType<typeof setTimeout> | undefined;
let generation = 0;
let appliedKey = "";
let issues: JsonValidationIssue[] = [];
const verdicts = new Map<string, Promise<SchemaVerdict>>();
let fetcher: JsonSchemaFetcher = defaultFetcher;

async function defaultFetcher(pluginId: string, keys: readonly string[]) {
  // Loaded on first use: the asset service reaches the desktop bridge.
  const { fetchContributionAssets } = await import("@/services/contribution-assets");
  const result = await fetchContributionAssets(pluginId, JSON_VALIDATION_CONTRIBUTION_POINT, keys);
  const bytes = new Map<string, Uint8Array>();
  for (const [key, asset] of result.assets) bytes.set(key, asset.bytes);
  return bytes;
}

function normalizePath(path: string): string {
  return path.replace(/\\/g, "/").replace(/\/+$/, "");
}

/** The document a model shows: project-relative when inside a known project root. */
export function jsonSchemaTargetForPath(path: string, roots: readonly string[]): JsonSchemaTarget {
  const normalized = normalizePath(path);
  const basename = normalized.slice(normalized.lastIndexOf("/") + 1);
  const drive = /^[A-Za-z]:\//.test(normalized);
  let best: string | undefined;
  for (const root of roots) {
    const prefix = `${normalizePath(root)}/`;
    if (prefix === "/") continue;
    const inside = drive ? normalized.toLowerCase().startsWith(prefix.toLowerCase()) : normalized.startsWith(prefix);
    if (inside && (!best || prefix.length > best.length)) best = prefix;
  }
  return best ? { relativePath: normalized.slice(best.length), basename } : { basename };
}

function modelTarget(model: TextModel): JsonSchemaTarget | undefined {
  const uri = model.uri;
  if (uri.scheme === "file") {
    // Uri.path is "/c:/x" for Windows drive paths.
    const path = /^\/[A-Za-z]:\//.test(uri.path) ? uri.path.slice(1) : uri.path;
    return jsonSchemaTargetForPath(path, rootsProvider());
  }
  if (uri.scheme === "app") {
    const path = normalizePath(uri.path);
    return { basename: path.slice(path.lastIndexOf("/") + 1) };
  }
  return undefined;
}

function issue(entry: JsonSchemaEntry, message: string): JsonValidationIssue {
  return { pluginId: entry.pluginId, stage: "frontend", entry: entry.entry, schema: entry.id, message };
}

/** Parses and regex-checks one schema once per content key. */
function verdictFor(entry: JsonSchemaEntry, bytes: Uint8Array | undefined): Promise<SchemaVerdict> {
  const cacheKey = `${entry.pluginId}\u0000${entry.schema}`;
  const cached = verdicts.get(cacheKey);
  if (cached) return cached;
  const verdict = (async (): Promise<SchemaVerdict> => {
    if (!bytes) return { ok: false, message: "schema asset is unavailable" };
    let schema: unknown;
    try {
      schema = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(bytes));
    } catch {
      return { ok: false, message: "schema asset is not valid JSON" };
    }
    if (typeof schema !== "object" || schema === null || Array.isArray(schema)) return { ok: false, message: "schema root must be an object" };
    const regexes = collectJsonSchemaRegexes(schema);
    if (!regexes) return { ok: false, message: "schema has too many or too long regexes" };
    for (const regex of regexes) {
      const unsafe = regexSafetyIssue(regex.source, { ignoreCase: regex.flags.includes("i"), unicode: regex.flags.includes("u") });
      if (unsafe) return { ok: false, message: `schema regex /${regex.source.slice(0, 64)}/ rejected: ${unsafe}` };
    }
    const probe = await probeRegexes(`jsonValidation\u0000${cacheKey}`, regexes);
    if (probe !== "ok") {
      return {
        ok: false,
        message: probe === "timeout"
          ? "schema regex probe timed out (possible catastrophic backtracking)"
          : "schema regex probe is unavailable",
      };
    }
    return { ok: true, schema };
  })();
  verdicts.set(cacheKey, verdict);
  if (verdicts.size > MAX_CACHED_SCHEMAS) verdicts.delete(verdicts.keys().next().value!);
  // An unavailable asset or probe is retried on the next sync.
  void verdict.then((result) => {
    if (!result.ok && /unavailable/.test(result.message) && verdicts.get(cacheKey) === verdict) verdicts.delete(cacheKey);
  });
  return verdict;
}

function writeSchemas(monaco: MonacoApi, schemas: readonly PluginSchema[]) {
  const defaults = monaco.mincodeJsonDefaults;
  const current = defaults.diagnosticsOptions;
  const others = (current.schemas ?? []).filter((schema) => !schema.uri.startsWith(PLUGIN_JSON_SCHEMA_URI_PREFIX));
  defaults.setDiagnosticsOptions({
    ...current,
    enableSchemaRequest: false,
    schemaRequest: "ignore",
    schemas: [...others, ...schemas.map((schema) => ({ uri: schema.uri, fileMatch: schema.fileMatch, schema: schema.schema }))],
  });
}

async function sync() {
  const monaco = active;
  if (!monaco) return;
  const token = ++generation;
  const wanted = new Map<string, { entry: JsonSchemaEntry; models: string[] }>();
  if (entries.length) {
    for (const model of monaco.editor.getModels()) {
      if (model.isDisposed() || !JSON_LANGUAGES.has(model.getLanguageId())) continue;
      const target = modelTarget(model);
      if (!target) continue;
      for (const entry of matchJsonSchemas(target, entries)) {
        const path = model.uri.scheme === "file" ? model.uri.path.replace(/^\/([A-Za-z]:\/)/, "$1") : "";
        if (!frameworkPluginAllowed(entry.pluginId, path)) continue;
        const key = `${entry.pluginId}\u0000${entry.schema}`;
        const slot = wanted.get(key) ?? { entry, models: [] };
        slot.models.push(model.uri.toString());
        wanted.set(key, slot);
      }
    }
  }
  const byPlugin = new Map<string, string[]>();
  for (const { entry } of wanted.values()) {
    const keys = byPlugin.get(entry.pluginId) ?? [];
    keys.push(entry.schema);
    byPlugin.set(entry.pluginId, keys);
  }
  const bytes = new Map<string, Uint8Array>();
  await Promise.all(
    [...byPlugin].map(async ([pluginId, keys]) => {
      const pending = keys.filter((key) => !verdicts.has(`${pluginId}\u0000${key}`));
      if (!pending.length) return;
      try {
        for (const [key, value] of await fetcher(pluginId, pending)) bytes.set(`${pluginId}\u0000${key}`, value);
      } catch {
        // Reported as unavailable below and retried on the next sync.
      }
    }),
  );
  if (token !== generation || active !== monaco) return;
  const schemas: PluginSchema[] = [];
  const nextIssues: JsonValidationIssue[] = [];
  for (const [key, { entry, models }] of wanted) {
    const verdict = await verdictFor(entry, bytes.get(key));
    if (token !== generation || active !== monaco) return;
    if (!verdict.ok) {
      nextIssues.push(issue(entry, verdict.message));
      continue;
    }
    schemas.push({ uri: pluginJsonSchemaUri(entry.pluginId, entry.schema), fileMatch: [...models].sort(), schema: verdict.schema });
  }
  issues = nextIssues;
  const key = JSON.stringify(schemas.map((schema) => [schema.uri, schema.fileMatch]));
  if (key === appliedKey) return;
  appliedKey = key;
  writeSchemas(monaco, schemas);
}

function schedule() {
  if (!active) return;
  if (timer) clearTimeout(timer);
  timer = setTimeout(() => {
    timer = undefined;
    void sync();
  }, JSON_SCHEMA_SYNC_DELAY_MS);
}

function detach() {
  if (timer) clearTimeout(timer);
  timer = undefined;
  generation += 1;
  const monaco = active;
  active = undefined;
  if (monaco && appliedKey !== "" && appliedKey !== "[]") writeSchemas(monaco, []);
  appliedKey = "";
  issues = [];
}

/**
 * Attaches to a loaded Monaco (the contribution hook calls this; tests pass a
 * stub). Returns a disposer that removes the listeners and plugin schemas.
 */
export function attachMonacoJsonSchemas(monaco: MonacoApi): () => void {
  detach();
  active = monaco;
  const listeners: Disposable[] = [
    monaco.editor.onDidCreateModel(schedule),
    monaco.editor.onWillDisposeModel(schedule),
    monaco.editor.onDidChangeModelLanguage(schedule),
  ];
  const stopFrameworkListener = frameworkRegistryChanged(schedule);
  schedule();
  return () => {
    stopFrameworkListener();
    for (const listener of listeners) listener.dispose();
    if (active === monaco) detach();
  };
}

/**
 * Replaces the plugin schemas and the provider of project roots (used to
 * derive project-relative paths). An empty list removes every plugin schema.
 */
export function setMonacoJsonSchemas(next: readonly JsonSchemaEntry[], roots?: () => readonly string[]): void {
  entries = next;
  if (roots) rootsProvider = roots;
  if (next.length > 0 && !unregisterHook) {
    unregisterHook = registerMonacoContributionHook((monaco) => attachMonacoJsonSchemas(monaco));
  }
  schedule();
}

/** Monaco-stage issues (rejected or unavailable schemas) of the last sync. */
export function monacoJsonSchemaIssues(): readonly JsonValidationIssue[] {
  return issues;
}

/** Replaces the asset fetcher (tests; the default uses the host) and clears cached verdicts. */
export function setJsonSchemaFetcher(next?: JsonSchemaFetcher): void {
  fetcher = next ?? defaultFetcher;
  verdicts.clear();
}

/** Resolves once a scheduled sync ran (tests). */
export async function flushMonacoJsonSchemas(): Promise<void> {
  if (timer) {
    clearTimeout(timer);
    timer = undefined;
  }
  await sync();
}
