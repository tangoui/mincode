import { isManifestCategory, type ManifestCategory } from "./catalog.ts";
import contributionContract from "./contribution-points.json" with { type: "json" };

export interface PluginManifest {
  id: string;
  name: string;
  version: string;
  apiVersion: number;
  category: ManifestCategory;
  tags?: string[];
  author?: string;
  description?: string;
  homepage?: string;
  icon?: string;
  cover?: string;
  platforms?: string[];
  capabilities?: string[];
  runtime: { type: "process" | "declarative"; entry?: string; entrypoints?: Record<string, string>; args?: string[] };
  contributes?: {
    commands?: { id: string; title: string; titleLocalized?: Record<string, string>; description?: string; keywords?: string[]; action?: string; payload?: string; risk?: string; target?: { type: string; viewId: string } }[];
    views?: { id: string; title: string; titleLocalized?: Record<string, string>; entry: string; icon?: string }[];
    actions?: { id: string; permissions: string[] | null; description?: string; inputSchema?: Record<string, unknown>; examples?: Record<string, unknown>[]; aiCallable?: boolean; risk?: string; uiCallable?: boolean; maxPayloadBytes?: number }[];
    /** Declarative plugins: inline presets or `{ path }` references into the package. */
    keymaps?: Record<string, unknown>[];
  };
}
export interface PluginInstallPreview {
  sourcePath: string;
  manifest: PluginManifest;
  fileCount: number;
  totalBytes: number;
  installed: boolean;
  /** For an update: IDs of global shortcuts that are on now and that this package changes (installing turns them off). */
  globalShortcutsChanged?: string[];
  /** Validated data provider entries (sessionSources, scmCommitMessage, fileDecorations) with their manifest titles. */
  dataProviders?: PluginInstallPreviewDataProvider[];
}
export interface PluginInstallPreviewDataProvider {
  point: string;
  id: string;
  title: string;
  titleLocalized?: Record<string, string>;
}
const object = (value: unknown): value is Record<string, unknown> => !!value && typeof value === "object" && !Array.isArray(value);
const string = (value: unknown): value is string => typeof value === "string";
const strings = (value: unknown) => Array.isArray(value) && value.every(string);
const dictionary = (value: unknown) => object(value) && Object.values(value).every(string);
function optional(value: Record<string, unknown>, keys: string[], check: (value: unknown) => boolean) {
  return keys.every(key => value[key] === undefined || check(value[key]));
}
function contribution(value: unknown, kind: "commands" | "views" | "actions"): boolean {
  if (!object(value) || !string(value.id)) return false;
  if (!optional(value, ["description", "icon", "action", "payload", "risk"], string)
    || !optional(value, ["titleLocalized"], dictionary) || !optional(value, ["keywords"], strings)) return false;
  if (kind === "actions") return (value.permissions === null || strings(value.permissions))
    && optional(value, ["inputSchema"], object)
    && optional(value, ["examples"], item => Array.isArray(item) && item.every(object))
    && optional(value, ["aiCallable", "uiCallable"], item => typeof item === "boolean")
    && optional(value, ["maxPayloadBytes"], item => Number.isSafeInteger(item) && Number(item) >= 0);
  return string(value.title) && (kind !== "views" || string(value.entry))
    && optional(value, ["target"], target => object(target) && string(target.type) && string(target.viewId));
}
// The host validates keymap contents; the preview only needs each entry to be an
// inline preset (with an id) or a `{ path }` reference.
function keymapEntry(value: unknown): boolean {
  return object(value) && (string(value.path) ? Object.keys(value).length === 1 : string(value.id));
}

/** Contribution points a declarative (data-only) package may use; the host validates the entries. */
export const DECLARATIVE_CONTRIBUTION_POINTS: readonly string[] = Object.entries(
  (contributionContract as { points: Record<string, { status?: string; kinds?: string[] }> }).points,
)
  .filter(([, point]) => point.status === "enabled" && point.kinds?.includes("declarative"))
  .map(([key]) => key);

// Mirrors the host's hasContributionEntries: a non-empty list, or an object holding a non-empty list.
function hasEntries(value: unknown): boolean {
  if (Array.isArray(value)) return value.length > 0;
  return object(value) && Object.values(value).some(item => !Array.isArray(item) || item.length > 0);
}

function runtimeValid(runtime: Record<string, unknown>, manifest: Record<string, unknown>): boolean {
  if (runtime.type === "process") {
    return optional(runtime, ["entry"], string) && optional(runtime, ["entrypoints"], dictionary) && optional(runtime, ["args"], strings);
  }
  if (runtime.type !== "declarative") return false;
  const contributes = manifest.contributes;
  return ["entry", "entrypoints", "args", "activation"].every(key => runtime[key] === undefined)
    && object(contributes)
    && optional(contributes, ["keymaps"], list => Array.isArray(list) && list.every(keymapEntry))
    && DECLARATIVE_CONTRIBUTION_POINTS.some(key => hasEntries(contributes[key]));
}

function dataProvider(value: unknown): boolean {
  return object(value) && string(value.point) && string(value.id) && string(value.title) && optional(value, ["titleLocalized"], dictionary);
}

export function parsePluginInstallPreview(value: unknown): PluginInstallPreview {
  const manifest = object(value) ? value.manifest : undefined;
  const runtime = object(manifest) ? manifest.runtime : undefined;
  if (!object(value) || !string(value.sourcePath) || !value.sourcePath.trim() || !object(manifest) || !object(runtime)
    || !["id", "name", "version"].every(key => string(manifest[key]) && manifest[key].trim())
    || manifest.apiVersion !== 2
    || !isManifestCategory(manifest.category) || !runtimeValid(runtime, manifest)
    || !optional(manifest, ["author", "description", "homepage", "icon", "cover"], string)
    || !optional(manifest, ["tags", "platforms", "capabilities"], strings)
    || !optional(manifest, ["contributes"], item => object(item) && (["commands", "views", "actions"] as const).every(kind => optional(item, [kind], list => Array.isArray(list) && list.every(entry => contribution(entry, kind)))))
    || !Number.isSafeInteger(value.fileCount) || Number(value.fileCount) < 1
    || !Number.isSafeInteger(value.totalBytes) || Number(value.totalBytes) < 0
    || typeof value.installed !== "boolean"
    || !optional(value, ["globalShortcutsChanged"], strings)
    || !optional(value, ["dataProviders"], list => Array.isArray(list) && list.every(dataProvider))) throw new Error("Invalid plugin install preview response");
  return value as unknown as PluginInstallPreview;
}
