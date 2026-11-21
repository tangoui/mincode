/**
 * Run configurations and the per-project persisted debugger state.
 *
 * Everything lives in MinCode's own store (`app_project_debug_v1`), keyed by
 * project root; nothing is ever written into the user's project.
 */
import { DEBUGGER_ADDRESS_LIMITS, isValidDebuggerAddress } from "./address-breakpoints.ts";
import { storedDataBreakpoints, type StoredDataBreakpoint } from "./data-breakpoints.ts";
import { storedExceptionTypes } from "./exception-types.ts";
import { storedFunctionBreakpoints, type StoredFunctionBreakpoint } from "./function-breakpoints.ts";
import { normalizeLogMessage } from "./logpoints.ts";
import type {
  DebugConfigurationSnippet,
  DebugRequestKind,
  DebuggerEntryPoint,
  DebuggerInfo,
  RunConfiguration,
  SourceBreakpoint,
} from "./types.ts";

export const PROJECT_DEBUG_STORAGE_KEY = "app_project_debug_v1";
export const DEFAULT_DEBUG_PANEL_HEIGHT = 260;

/** One project's persisted debugger state. */
export interface ProjectDebugState {
  configs: RunConfiguration[];
  selectedConfigId: string | null;
  /** Keyed by the project-relative `/`-separated path (domain/debug/paths.ts). */
  breakpoints: Record<string, SourceBreakpoint[]>;
  /**
   * Breakpoints in debugger-provided sources (domain/debug/address-breakpoints.ts)
   * per debugger type (an address only means something to its own debugger),
   * keyed by the debugger's address verbatim; at most
   * DEBUGGER_ADDRESS_LIMITS.maxAddresses addresses per type.
   */
  addressBreakpoints: Record<string, Record<string, SourceBreakpoint[]>>;
  /** Enabled exception filter ids per debugger type. */
  exceptionFilters: Record<string, string[]>;
  /**
   * "Pause only on these exception types" per debugger type (debuggers that
   * declare `exceptionTypes`); a missing or empty list does not restrict.
   */
  exceptionTypes: Record<string, string[]>;
  /** Method breakpoints per debugger type, in the order they were added. */
  functionBreakpoints: Record<string, StoredFunctionBreakpoint[]>;
  /**
   * Field breakpoints per debugger type, in the order they were set: only
   * those whose adapter declared `canPersist` (the others live in their session).
   */
  dataBreakpoints: Record<string, StoredDataBreakpoint[]>;
  /** Watch expressions, in display order. */
  watches: string[];
  panelHeight: number;
}

/** The value stored under PROJECT_DEBUG_STORAGE_KEY. */
export type ProjectDebugStates = Record<string, ProjectDebugState>;

export interface ConfigurationVariables {
  /** Absolute path of the active editor file; `${file}` needs it. */
  file?: string;
  projectRoot: string;
}

export function emptyProjectDebugState(): ProjectDebugState {
  return { configs: [], selectedConfigId: null, breakpoints: {}, addressBreakpoints: {}, exceptionFilters: {}, exceptionTypes: {}, functionBreakpoints: {}, dataBreakpoints: {}, watches: [], panelHeight: DEFAULT_DEBUG_PANEL_HEIGHT };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isRequestKind(value: unknown): value is DebugRequestKind {
  return value === "launch" || value === "attach";
}

function expandString(value: string, vars: ConfigurationVariables): string {
  return value.replace(/\$\{(file|projectRoot)\}/g, (_match, name: string) => {
    if (name === "projectRoot") return vars.projectRoot;
    if (!vars.file) throw new Error("debug.config.noActiveFile");
    return vars.file;
  });
}

function expandValue(value: unknown, vars: ConfigurationVariables): unknown {
  if (typeof value === "string") return expandString(value, vars);
  if (Array.isArray(value)) return value.map((item) => expandValue(item, vars));
  if (isRecord(value)) return expandConfigurationVariables(value, vars);
  return value;
}

/**
 * A copy of fields with `${file}` and `${projectRoot}` replaced in every
 * string, at any depth. Throws Error("debug.config.noActiveFile") when a
 * string uses `${file}` and there is no active file.
 */
export function expandConfigurationVariables(fields: Record<string, unknown>, vars: ConfigurationVariables): Record<string, unknown> {
  const expanded: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(fields)) expanded[key] = expandValue(value, vars);
  return expanded;
}

/**
 * The `configuration` sent to prepare/check: the expanded fields with
 * `request` as a member, which is where the debugger's resolve action reads
 * it (python-debug backend/resolve.go `config.text("request")`). The
 * configuration's own request wins over a stray `request` field.
 */
export function configurationPayload(cfg: RunConfiguration, vars: ConfigurationVariables): Record<string, unknown> {
  return { ...expandConfigurationVariables(cfg.fields, vars), request: cfg.request };
}

function newConfigurationId(): string {
  const random = globalThis.crypto?.randomUUID?.();
  return random ?? `cfg-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`;
}

function cloneFields(body: Record<string, unknown>): Record<string, unknown> {
  // Snippet and entry-point bodies are JSON from the plugin host.
  const fields = JSON.parse(JSON.stringify(body)) as Record<string, unknown>;
  // request/type are configuration metadata, kept outside fields.
  delete fields.request;
  delete fields.type;
  return fields;
}

/** A new configuration from a debugger's snippet; name defaults to the snippet label. */
export function newConfigurationFromSnippet(debugger_: DebuggerInfo, snippet: DebugConfigurationSnippet, name?: string): RunConfiguration {
  return {
    id: newConfigurationId(),
    name: name?.trim() || snippet.label,
    pluginId: debugger_.pluginId,
    type: debugger_.type,
    request: snippet.request,
    fields: cloneFields(snippet.body),
  };
}

/** A copy of cfg under a new id and the given name; fields are copied deeply. */
export function duplicateConfiguration(cfg: RunConfiguration, name: string): RunConfiguration {
  return { ...cfg, id: newConfigurationId(), name, fields: JSON.parse(JSON.stringify(cfg.fields)) as Record<string, unknown> };
}

/** The file name without its last extension ("app.py" → "app", ".env" → ".env"). */
function baseName(fileName: string): string {
  const name = fileName.split(/[\\/]/).pop() ?? fileName;
  const dot = name.lastIndexOf(".");
  return dot > 0 ? name.slice(0, dot) : name;
}

/** Structural equality of JSON values (object key order ignored). */
function jsonEqual(a: unknown, b: unknown): boolean {
  if (a === b) return true;
  if (Array.isArray(a) || Array.isArray(b)) {
    return Array.isArray(a) && Array.isArray(b) && a.length === b.length && a.every((item, index) => jsonEqual(item, b[index]));
  }
  if (!isRecord(a) || !isRecord(b)) return false;
  const keys = Object.keys(a);
  return keys.length === Object.keys(b).length && keys.every((key) => Object.prototype.hasOwnProperty.call(b, key) && jsonEqual(a[key], b[key]));
}

/**
 * The configuration an entry-point click runs. An existing configuration with
 * the same pluginId, type, request and fields is returned unchanged. Otherwise
 * a new one is named after the file without its extension, or `<name> (n)`
 * with the next free n when that name is taken by a configuration for
 * something else (e.g. tests/app.py next to src/app.py).
 */
export function newConfigurationFromEntryPoint(
  debugger_: DebuggerInfo,
  entry: DebuggerEntryPoint,
  fileName: string,
  existing: readonly RunConfiguration[] = [],
): RunConfiguration {
  const requested = entry.configuration.request;
  const request: DebugRequestKind = isRequestKind(requested) ? requested : "launch";
  const fields = cloneFields(entry.configuration);
  const reused = existing.find(
    (cfg) => cfg.pluginId === debugger_.pluginId && cfg.type === debugger_.type && cfg.request === request && jsonEqual(cfg.fields, fields)
  );
  if (reused) return reused;
  const base = baseName(fileName);
  const taken = new Set(existing.map((cfg) => cfg.name));
  let name = base;
  for (let suffix = 2; taken.has(name); suffix += 1) name = `${base} (${suffix})`;
  return { id: newConfigurationId(), name, pluginId: debugger_.pluginId, type: debugger_.type, request, fields };
}

// ---- persisted-value validation ----

function normalizeConfiguration(value: unknown): RunConfiguration | null {
  if (!isRecord(value)) return null;
  const { id, name, pluginId, type, request, fields } = value;
  if (typeof id !== "string" || !id || typeof name !== "string" || typeof pluginId !== "string" || typeof type !== "string") return null;
  if (!isRequestKind(request) || !isRecord(fields)) return null;
  return { id, name, pluginId, type, request, fields };
}

function normalizeBreakpoint(value: unknown): SourceBreakpoint | null {
  if (!isRecord(value) || typeof value.line !== "number" || !Number.isInteger(value.line) || value.line < 1) return null;
  const breakpoint: SourceBreakpoint = { line: value.line, enabled: value.enabled !== false };
  if (typeof value.condition === "string" && value.condition) breakpoint.condition = value.condition;
  if (typeof value.hitCondition === "string" && value.hitCondition) breakpoint.hitCondition = value.hitCondition;
  const logMessage = normalizeLogMessage(value.logMessage);
  if (logMessage !== undefined) breakpoint.logMessage = logMessage;
  return breakpoint;
}

/** Each key's breakpoints (one per line, sorted); keys `keep` rejects, empty lists and keys past maxKeys are dropped. */
function normalizeBreakpointMap(value: unknown, keep: (key: string) => boolean = () => true, maxKeys = Infinity): Record<string, SourceBreakpoint[]> {
  const map: Record<string, SourceBreakpoint[]> = {};
  if (!isRecord(value)) return map;
  let count = 0;
  for (const [key, list] of Object.entries(value)) {
    if (count >= maxKeys) break;
    if (!Array.isArray(list) || !keep(key)) continue;
    const byLine = new Map<number, SourceBreakpoint>();
    for (const item of list) {
      const bp = normalizeBreakpoint(item);
      if (bp && !byLine.has(bp.line)) byLine.set(bp.line, bp);
    }
    if (!byLine.size) continue;
    map[key] = [...byLine.values()].sort((a, b) => a.line - b.line);
    count += 1;
  }
  return map;
}

function strings(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((item): item is string => typeof item === "string") : [];
}

/** One project's state from untrusted stored data; malformed members are dropped. */
export function normalizeProjectDebugState(value: unknown): ProjectDebugState | null {
  if (!isRecord(value)) return null;
  const state = emptyProjectDebugState();
  if (Array.isArray(value.configs)) {
    const seen = new Set<string>();
    for (const item of value.configs) {
      const cfg = normalizeConfiguration(item);
      if (cfg && !seen.has(cfg.id)) {
        seen.add(cfg.id);
        state.configs.push(cfg);
      }
    }
  }
  if (typeof value.selectedConfigId === "string" && state.configs.some((cfg) => cfg.id === value.selectedConfigId)) {
    state.selectedConfigId = value.selectedConfigId;
  }
  state.breakpoints = normalizeBreakpointMap(value.breakpoints);
  if (isRecord(value.addressBreakpoints)) {
    for (const [type, map] of Object.entries(value.addressBreakpoints)) {
      // Anything but {type: {address: list}} (e.g. a flat {address: list}) is dropped.
      if (type === "" || !isRecord(map)) continue;
      const addresses = normalizeBreakpointMap(map, isValidDebuggerAddress, DEBUGGER_ADDRESS_LIMITS.maxAddresses);
      if (Object.keys(addresses).length) state.addressBreakpoints[type] = addresses;
    }
  }
  if (isRecord(value.exceptionFilters)) {
    for (const [type, filters] of Object.entries(value.exceptionFilters)) state.exceptionFilters[type] = strings(filters);
  }
  if (isRecord(value.exceptionTypes)) {
    for (const [type, list] of Object.entries(value.exceptionTypes)) {
      const types = storedExceptionTypes(list);
      if (types) state.exceptionTypes[type] = types;
    }
  }
  if (isRecord(value.functionBreakpoints)) {
    for (const [type, list] of Object.entries(value.functionBreakpoints)) {
      const breakpoints = storedFunctionBreakpoints(list);
      if (breakpoints) state.functionBreakpoints[type] = breakpoints;
    }
  }
  if (isRecord(value.dataBreakpoints)) {
    for (const [type, list] of Object.entries(value.dataBreakpoints)) {
      const breakpoints = storedDataBreakpoints(list);
      if (breakpoints) state.dataBreakpoints[type] = breakpoints;
    }
  }
  state.watches = strings(value.watches);
  if (typeof value.panelHeight === "number" && Number.isFinite(value.panelHeight) && value.panelHeight > 0) {
    state.panelHeight = Math.round(value.panelHeight);
  }
  return state;
}

/** The whole stored value; non-object input yields no projects. */
export function normalizeProjectDebugStates(value: unknown): ProjectDebugStates {
  const states: ProjectDebugStates = {};
  if (!isRecord(value)) return states;
  for (const [root, item] of Object.entries(value)) {
    const state = normalizeProjectDebugState(item);
    if (state) states[root] = state;
  }
  return states;
}
