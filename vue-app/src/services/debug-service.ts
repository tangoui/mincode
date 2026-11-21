/**
 * Debugger and debug session commands: the IPC boundary.
 *
 * Starting a debugger is prepare → (confirm + trust) → start: the WebView
 * names a debugger only by `(pluginId, type, projectRoot)` plus the user's
 * run configuration; the Go host resolves the adapter launch and refuses to
 * start it until the user confirmed exactly that fingerprint
 * (backend/plugin/debuggers_launch.go). Running sessions are driven
 * through `debug_*` (backend/debug/commands.go) and report on the
 * `debug:event` event. Desktop only: there is no browser-local debugger.
 */
import { breakpointListItemResults } from "@/domain/debug/function-breakpoints";
import { normalizeLogMessage } from "@/domain/debug/logpoints";
import {
  DEBUG_BREAKPOINTS_SET_EVENT,
  type AttachTerminalResult,
  type BackendSourceBreakpoint,
  type DataBreakpointSpec,
  type DebugBreakpointListKind,
  type DebugBreakpointListResult,
  type DebugEvent,
  type DebugSessionInfo,
  type DebugSourceBreakpointsSet,
  type DebugStartResult,
  type DebuggerCheckResult,
  type DebuggerEntryPoint,
  type DebuggerInfo,
  type FunctionBreakpointSpec,
  type PreparedDebugLaunch,
} from "@/domain/debug/types";
import { invokeBackend, isDesktopRuntime, listenAppEvent } from "@/services/desktop-bridge";

export const DEBUG_EVENT = "debug:event";

const FINGERPRINT_PATTERN = /^[0-9a-f]{64}$/;

/** A breakpoint the caller may still carry `enabled` on; disabled ones are not sent. */
export type OutgoingSourceBreakpoint = BackendSourceBreakpoint & { enabled?: boolean };

export interface DebuggerPrepareRequest {
  pluginId: string;
  type: string;
  projectRoot: string;
  configuration: Record<string, unknown>;
  noDebug: boolean;
}

/** A function or data breakpoint the caller may still carry `enabled` on; disabled ones are not sent. */
export type OutgoingFunctionBreakpoint = FunctionBreakpointSpec & { enabled?: boolean };
export type OutgoingDataBreakpoint = DataBreakpointSpec & { enabled?: boolean };

export interface DebuggerStartRequest {
  prepareId: string;
  fingerprint: string;
  /** Absolute source path → breakpoints. */
  breakpoints: Record<string, OutgoingSourceBreakpoint[]>;
  /**
   * Debugger-provided source address (an adapter's `source.path` that is not
   * a project file) → breakpoints, at most 256 addresses. The host only
   * checks the address shape (`<scheme>://` with a scheme of at least 2
   * characters, not `file`, ≤ 4096 characters, no control or bidi
   * characters) and sends it verbatim; any invalid entry refuses the start. Results arrive as `mincode.breakpointsSet` with
   * `address: true`, see parseBreakpointsSetEvent.
   */
  addressBreakpoints?: Record<string, OutgoingSourceBreakpoint[]>;
  exceptionFilters: string[];
  /**
   * Method breakpoints; the host sends them (setFunctionBreakpoints) only to
   * an adapter that declares supportsFunctionBreakpoints. Results arrive as a
   * `mincode.breakpointsSet` event, see parseBreakpointListEvent.
   */
  functionBreakpoints?: OutgoingFunctionBreakpoint[];
  /** Field breakpoints (setDataBreakpoints, only with supportsDataBreakpoints). */
  dataBreakpoints?: OutgoingDataBreakpoint[];
}

export interface DebuggerEntryPointsRequest {
  pluginId: string;
  type: string;
  projectRoot: string;
  file: string;
  languageId: string;
}

export interface DebuggerCheckRequest {
  pluginId: string;
  type: string;
  projectRoot: string;
  /** The run configuration about to start, so the plugin checks what it selects. */
  configuration?: Record<string, unknown>;
}

/**
 * The project's saved settings of one debugger type. `exceptionTypes` is
 * always sent (an empty list clears the plugin's previous filter); the host
 * trims, de-duplicates and refuses items that are not dotted identifiers.
 */
export interface DebuggerSettingsRequest {
  pluginId: string;
  type: string;
  projectRoot: string;
  exceptionTypes: string[];
}

function requireDesktop() {
  if (!isDesktopRuntime()) throw new Error("unavailable: debugging needs the MinCode desktop app");
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function invalid(operation: string, what: string): never {
  throw new Error(`unavailable: ${operation} returned an invalid ${what}`);
}

/** Drops disabled breakpoints and the frontend-only `enabled` flag. */
function outgoingBreakpoints(breakpoints: readonly OutgoingSourceBreakpoint[]): BackendSourceBreakpoint[] {
  return breakpoints
    .filter((breakpoint) => breakpoint.enabled !== false)
    .map(({ line, condition, hitCondition, logMessage }) => ({
      line,
      ...(condition ? { condition } : {}),
      ...(hitCondition ? { hitCondition } : {}),
      ...(normalizeLogMessage(logMessage) !== undefined ? { logMessage } : {}),
    }));
}

/** Each source's enabled breakpoints; sources with none are left out. */
function outgoingBreakpointMap(sources: Readonly<Record<string, readonly OutgoingSourceBreakpoint[]>>): Record<string, BackendSourceBreakpoint[]> {
  const out: Record<string, BackendSourceBreakpoint[]> = {};
  for (const [source, list] of Object.entries(sources)) {
    const enabled = outgoingBreakpoints(list);
    if (enabled.length > 0) out[source] = enabled;
  }
  return out;
}

/** Drops disabled function breakpoints, `enabled` and empty conditions. */
function outgoingFunctionBreakpoints(breakpoints: readonly OutgoingFunctionBreakpoint[] = []): FunctionBreakpointSpec[] {
  return breakpoints
    .filter((breakpoint) => breakpoint.enabled !== false)
    .map(({ name, condition, hitCondition }) => ({ name, ...(condition ? { condition } : {}), ...(hitCondition ? { hitCondition } : {}) }));
}

/** Drops disabled data breakpoints, `enabled` and empty optional members. */
function outgoingDataBreakpoints(breakpoints: readonly OutgoingDataBreakpoint[] = []): DataBreakpointSpec[] {
  return breakpoints
    .filter((breakpoint) => breakpoint.enabled !== false)
    .map(({ dataId, accessType, condition, hitCondition }) => ({
      dataId,
      ...(accessType ? { accessType } : {}),
      ...(condition ? { condition } : {}),
      ...(hitCondition ? { hitCondition } : {}),
    }));
}

const BREAKPOINT_LIST_KINDS: readonly DebugBreakpointListKind[] = ["functionBreakpoints", "dataBreakpoints"];

/**
 * The function or data breakpoint results of a session start: the
 * `mincode.breakpointsSet` event without a path that carries
 * `functionBreakpoints` or `dataBreakpoints` (Go debug.BreakpointsSetEvent).
 * DAP answers in request order, so result i belongs to sent breakpoint i;
 * `sentCount` is how many were sent (enabled ones), and exactly that many
 * results come back (a short answer leaves the rest unverified). A failed
 * request (no results) leaves each of them unverified with the failure message. Returns
 * undefined for any other event.
 */
export function parseBreakpointListEvent(event: DebugEvent, sentCount: number): DebugBreakpointListResult | undefined {
  const body: unknown = event.body;
  if (event.event !== DEBUG_BREAKPOINTS_SET_EVENT || !isRecord(body) || (body.path !== undefined && body.path !== "")) return undefined;
  const kind = BREAKPOINT_LIST_KINDS.find((key) => key in body);
  if (!kind) return undefined;
  const error = typeof body.error === "string" && body.error !== "" ? body.error : undefined;
  const results = breakpointListItemResults(body[kind], sentCount, error);
  return error ? { kind, results, error } : { kind, results };
}

/**
 * The result of one source of a session start: the `mincode.breakpointsSet`
 * event with a non-empty `path` and a `breakpoints` key. `address` is true
 * only for a literal `address: true` (a debugger address, not a file path).
 * Returns undefined for path-less lists and any other event.
 */
export function parseBreakpointsSetEvent(event: DebugEvent): DebugSourceBreakpointsSet | undefined {
  const body: unknown = event.body;
  if (event.event !== DEBUG_BREAKPOINTS_SET_EVENT || !isRecord(body) || typeof body.path !== "string" || body.path === "") return undefined;
  if (!("breakpoints" in body)) return undefined;
  const error = typeof body.error === "string" && body.error !== "" ? body.error : undefined;
  const result: DebugSourceBreakpointsSet = { path: body.path, address: body.address === true, breakpoints: body.breakpoints };
  return error ? { ...result, error } : result;
}

/**
 * The list item with a malformed hotCodeReplace dropped (event and request
 * must be non-empty strings); a modeSetting that is not a non-empty string is
 * dropped alone (the mode is then auto).
 */
function debuggerInfo(item: DebuggerInfo): DebuggerInfo {
  const hot: unknown = item.hotCodeReplace;
  if (hot === undefined) return item;
  if (isRecord(hot) && typeof hot.event === "string" && hot.event !== "" && typeof hot.request === "string" && hot.request !== "") {
    const modeSetting = typeof hot.modeSetting === "string" && hot.modeSetting !== "" ? { modeSetting: hot.modeSetting } : {};
    return { ...item, hotCodeReplace: { event: hot.event, request: hot.request, ...modeSetting } };
  }
  const { hotCodeReplace: _dropped, ...rest } = item;
  return rest;
}

/** Validates a prepared launch answered by `operation` (also used by services/plugin-test-runs.ts). */
export function parsePrepared(raw: unknown, operation: string): PreparedDebugLaunch {
  const launch = isRecord(raw) && isRecord(raw.launch) ? raw.launch : undefined;
  const valid =
    isRecord(raw) &&
    launch &&
    typeof raw.prepareId === "string" &&
    raw.prepareId !== "" &&
    typeof raw.type === "string" &&
    typeof raw.projectRoot === "string" &&
    (raw.request === "launch" || raw.request === "attach") &&
    isRecord(raw.arguments) &&
    typeof launch.fingerprint === "string" &&
    FINGERPRINT_PATTERN.test(launch.fingerprint) &&
    typeof launch.cwd === "string" &&
    Array.isArray(launch.argv) &&
    launch.argv.length > 0 &&
    launch.argv.every((arg) => typeof arg === "string");
  if (!valid) invalid(operation, "debugger launch");
  const prepared = raw as unknown as PreparedDebugLaunch;
  return {
    ...prepared,
    kind: typeof raw.kind === "string" ? raw.kind : "",
    launch: {
      ...prepared.launch,
      env: isRecord(launch.env) ? (launch.env as Record<string, string>) : {},
      trusted: launch.trusted === true,
      previouslyTrusted: launch.previouslyTrusted === true,
    },
  };
}

/** The debuggers offered on this platform. */
export async function listDebuggers(): Promise<DebuggerInfo[]> {
  requireDesktop();
  const raw = await invokeBackend<unknown>("plugin_host_debuggers_list");
  if (!Array.isArray(raw)) invalid("plugin_host_debuggers_list", "debugger list");
  return raw.filter((item): item is DebuggerInfo => isRecord(item) && typeof item.pluginId === "string" && typeof item.type === "string").map(debuggerInfo);
}

/** Resolves a launch on the host (nothing is started). */
export async function prepareDebugger(request: DebuggerPrepareRequest): Promise<PreparedDebugLaunch> {
  requireDesktop();
  const raw = await invokeBackend<unknown>("plugin_host_debugger_prepare", { payload: request });
  return parsePrepared(raw, "plugin_host_debugger_prepare");
}

/** Stores the user's confirmation of exactly `fingerprint`. */
export async function trustDebugger(prepareId: string, fingerprint: string): Promise<PreparedDebugLaunch> {
  requireDesktop();
  const raw = await invokeBackend<unknown>("plugin_host_debugger_trust", { payload: { prepareId, fingerprint } });
  return parsePrepared(raw, "plugin_host_debugger_trust");
}

/**
 * Starts a trusted prepared launch; disabled breakpoints are not sent, and
 * function/data breakpoint lists only when something in them is enabled.
 */
export async function startDebugger(request: DebuggerStartRequest): Promise<DebugStartResult> {
  requireDesktop();
  const breakpoints = outgoingBreakpointMap(request.breakpoints);
  const addressBreakpoints = outgoingBreakpointMap(request.addressBreakpoints ?? {});
  const functionBreakpoints = outgoingFunctionBreakpoints(request.functionBreakpoints);
  const dataBreakpoints = outgoingDataBreakpoints(request.dataBreakpoints);
  const raw = await invokeBackend<unknown>("plugin_host_debugger_start", {
    payload: {
      prepareId: request.prepareId,
      fingerprint: request.fingerprint,
      breakpoints,
      ...(Object.keys(addressBreakpoints).length > 0 ? { addressBreakpoints } : {}),
      exceptionFilters: request.exceptionFilters,
      ...(functionBreakpoints.length > 0 ? { functionBreakpoints } : {}),
      ...(dataBreakpoints.length > 0 ? { dataBreakpoints } : {}),
    },
  });
  if (!isRecord(raw) || typeof raw.sessionId !== "string" || raw.sessionId === "") {
    invalid("plugin_host_debugger_start", "debug session");
  }
  return {
    sessionId: raw.sessionId,
    capabilities: isRecord(raw.capabilities) ? raw.capabilities : {},
    skippedBreakpoints: Array.isArray(raw.skippedBreakpoints)
      ? raw.skippedBreakpoints.filter((path): path is string => typeof path === "string")
      : [],
  };
}

/** Runnable lines of a file offered by the debugger's entryPointsAction. */
export async function debuggerEntryPoints(request: DebuggerEntryPointsRequest): Promise<DebuggerEntryPoint[]> {
  requireDesktop();
  const raw = await invokeBackend<unknown>("plugin_host_debugger_entry_points", { payload: request });
  if (!Array.isArray(raw)) invalid("plugin_host_debugger_entry_points", "entry point list");
  return raw.filter(
    (point): point is DebuggerEntryPoint =>
      isRecord(point) && typeof point.line === "number" && typeof point.label === "string" && isRecord(point.configuration),
  );
}

/** Reports the tools a debugger still needs. */
export async function checkDebugger(request: DebuggerCheckRequest): Promise<DebuggerCheckResult> {
  requireDesktop();
  const raw = await invokeBackend<unknown>("plugin_host_debugger_check", { payload: request });
  if (!isRecord(raw) || typeof raw.ready !== "boolean") invalid("plugin_host_debugger_check", "check result");
  return { ready: raw.ready, missing: Array.isArray(raw.missing) ? (raw.missing as DebuggerCheckResult["missing"]) : [] };
}

/**
 * Sends the project's saved settings to the debugger's settingsAction
 * (host timeout 10 s); rejects with the host or plugin error.
 */
export async function applyDebuggerSettings(request: DebuggerSettingsRequest): Promise<void> {
  requireDesktop();
  const raw = await invokeBackend<unknown>("plugin_host_debugger_apply_settings", { payload: request });
  if (!isRecord(raw) || Object.keys(raw).length > 0) invalid("plugin_host_debugger_apply_settings", "settings result");
}

/**
 * Sends a DAP request to a session and resolves to its response body.
 * `timeoutMs` 0 uses the host default (15 s; capped at 120 s).
 */
export async function debugRequest<T>(sessionId: string, command: string, args?: Record<string, unknown>, timeoutMs = 0): Promise<T> {
  requireDesktop();
  return invokeBackend<T>("debug_request", { sessionId, command, arguments: args, timeoutMs });
}

/**
 * Replaces the breakpoints of one source in a session (empty clears it).
 * The host checks that `path` is inside the session's project root.
 */
export async function setSessionBreakpoints(
  sessionId: string,
  path: string,
  breakpoints: readonly OutgoingSourceBreakpoint[],
): Promise<{ breakpoints?: { line?: number; verified: boolean; message?: string }[] }> {
  requireDesktop();
  return invokeBackend("debug_set_breakpoints", { sessionId, path, breakpoints: outgoingBreakpoints(breakpoints) });
}

/**
 * Replaces the breakpoints of one debugger-provided source address in a
 * session (empty clears it). The host validates the address shape (see
 * DebuggerStartRequest.addressBreakpoints) and sends it verbatim as
 * `source.path`; it is never resolved as a project path.
 */
export async function setSessionAddressBreakpoints(
  sessionId: string,
  address: string,
  breakpoints: readonly OutgoingSourceBreakpoint[],
): Promise<{ breakpoints?: { line?: number; verified: boolean; message?: string }[] }> {
  requireDesktop();
  return invokeBackend("debug_set_breakpoints", { sessionId, address, breakpoints: outgoingBreakpoints(breakpoints) });
}

/**
 * Stops a session. Without `terminateDebuggee` the host ends a launched
 * program and leaves an attached one running.
 */
export async function stopDebugSession(sessionId: string, terminateDebuggee?: boolean): Promise<void> {
  requireDesktop();
  await invokeBackend<void>("debug_stop", terminateDebuggee === undefined ? { sessionId } : { sessionId, terminateDebuggee });
}

/** The registered debug sessions. */
export async function listDebugSessions(): Promise<DebugSessionInfo[]> {
  requireDesktop();
  const raw = await invokeBackend<unknown>("debug_list_sessions");
  return Array.isArray(raw) ? (raw as DebugSessionInfo[]) : [];
}

/**
 * Subscribes to every session's events (adapter events and `mincode.*`);
 * malformed payloads are dropped. Returns the unsubscribe function.
 */
export function onDebugEvent(handler: (event: DebugEvent) => void): () => void {
  requireDesktop();
  return listenAppEvent<unknown>(DEBUG_EVENT, (payload) => {
    if (!isRecord(payload) || typeof payload.sessionId !== "string" || typeof payload.event !== "string") return;
    handler({ sessionId: payload.sessionId, event: payload.event, body: payload.body });
  });
}

/**
 * Attaches to a debuggee terminal (the `terminalSessionId` of a
 * `mincode.runInTerminal` event) without starting a process.
 */
export async function attachTerminal(sessionId: string): Promise<AttachTerminalResult> {
  requireDesktop();
  return invokeBackend<AttachTerminalResult>("attach_local_shell", { payload: { sessionId } });
}
