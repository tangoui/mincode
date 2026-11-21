/**
 * The debug session view model: what the tool window, gutter and inline
 * values read about one session, and the pure reducer that applies a
 * `debug:event` (adapter events and MinCode's `mincode.*` events) to it.
 */
import type { DebugDataBreakpointResult, StoredDataBreakpoint } from "./data-breakpoints.ts";
import { identityOf, listResultsWithEvent, pairedListResults, type DebugFunctionBreakpointResult } from "./function-breakpoints.ts";
import { isValidDebuggerAddress } from "./address-breakpoints.ts";
import { existingKey } from "./paths.ts";
import {
  DEBUG_BREAKPOINTS_SET_EVENT,
  DEBUG_RUN_IN_TERMINAL_EVENT,
  DEBUG_SESSION_ENDED_EVENT,
  type DapCapabilities,
  type DapStackFrame,
  type DapThread,
  type DebugEvent,
  type DebuggerHotCodeReplace,
  type PreparedDebugLaunch,
} from "./types.ts";

/** Output entries kept per session; the oldest are dropped. */
export const MAX_SESSION_OUTPUT_LINES = 2000;
/**
 * Characters per output entry: a longer `output` (the host batches up to
 * 64 KiB) is split into several entries at line breaks, so nothing is lost
 * in the middle and the entry cap bounds memory.
 */
export const MAX_SESSION_OUTPUT_TEXT = 8 * 1024;

const NOTICE_VALUE_LIMIT = 200;
const NOTICE_ARGUMENTS_LIMIT = 2048;

/** `arguments` members shown on their own line in the launch confirmation. */
const NOTICE_KEYS = ["program", "module", "python", "args", "cwd", "mainClass", "javaExec", "vmArgs", "processId", "host", "port", "console"];

export type DebugSessionState = "running" | "paused" | "ended";

export interface DebugBreakpointResult {
  /** The requested (stored) line: the gutter's key, whatever line the adapter reports. */
  line: number;
  verified: boolean;
  /** The adapter's breakpoint id, for later `breakpoint` events. */
  id?: number;
  /** Why the adapter could not verify it (e.g. "class not loaded"). */
  message?: string;
  /** The line the adapter will actually stop on, when it moved the breakpoint off `line`. */
  actualLine?: number;
}

/** What the adapter said about a breakpoint, beyond verified. */
export interface DebugBreakpointHoverDetails {
  /** Why it is unverified. */
  message?: string;
  /** The line the debugger actually stops on, when it moved the breakpoint. */
  actualLine?: number;
}

export interface DebugOutputLine {
  /** Increases by one per entry in a session; a stable row key. */
  seq: number;
  category: string;
  text: string;
}

export interface DebugSessionView {
  id: string;
  projectRoot: string;
  configId: string;
  configName: string;
  type: string;
  noDebug: boolean;
  state: DebugSessionState;
  stoppedThreadId?: number;
  stoppedReason?: string;
  /** Counts the session's `stopped` events (a stop without a `continued` in between is a new one too); unset before the first. */
  stopSeq?: number;
  endReason?: string;
  threads: DapThread[];
  selectedThreadId?: number;
  frames: DapStackFrame[];
  /**
   * The shown frames reach the bottom of the stack (the adapter answered
   * fewer than requested, or its totalFrames says so): the last one has no
   * caller. Unset while that is not known.
   */
  framesComplete?: boolean;
  selectedFrameId?: number;
  /** Absolute source path as the backend reports it → breakpoint results. */
  verified: Record<string, DebugBreakpointResult[]>;
  /** Adapter breakpoint id → the `verified` key (absolute path) holding it. */
  breakpointIds: Record<number, string>;
  /** Debugger address (domain/debug/address-breakpoints.ts) → breakpoint results; never a project path. */
  addressVerified: Record<string, DebugBreakpointResult[]>;
  /** Adapter breakpoint id → the `addressVerified` key holding it. */
  addressBreakpointIds: Record<number, string>;
  /**
   * The method breakpoints last sent to the adapter (enabled names, in
   * order) with its answers; absent when none were ever sent (e.g. the
   * adapter does not declare supportsFunctionBreakpoints).
   */
  functionBreakpoints?: DebugFunctionBreakpointResult[];
  /**
   * The field breakpoints last sent to the adapter (the saved ones of the
   * type, then sessionDataBreakpoints) with its answers; absent when none
   * were ever sent (e.g. the adapter does not declare supportsDataBreakpoints).
   */
  dataBreakpoints?: DebugDataBreakpointResult[];
  /** Field breakpoints of this session only (the adapter did not declare them `canPersist`); gone with the session. */
  sessionDataBreakpoints?: StoredDataBreakpoint[];
  output: DebugOutputLine[];
  /** The terminal shown by default: the latest runInTerminal of the session. */
  consoleTerminalId?: string;
  /** Every runInTerminal terminal of the session, oldest first. */
  consoleTerminalIds?: string[];
  /** Where the latest runInTerminal program runs when the Console cannot simply show its terminal. */
  consoleNotice?: DebugConsoleNotice;
  capabilities: DapCapabilities;
  startedAt: number;
  /** The last failed request (threads, stack, step, …); cleared by the next success. */
  lastError?: string;
  /** The plugin of the session's debugger (its settings are read for hot code replace). */
  pluginId?: string;
  /** The debugger's hot code replace declaration; absent when it declares none (no entry is shown). */
  hotCodeReplace?: DebuggerHotCodeReplace;
  /** Rebuilt code waits for 「应用代码更改」 (manual mode, or an automatic replace that could not be sent). */
  pendingCodeChanges: boolean;
  /** The last hot code replace outcome or adapter warning, shown until dismissed or replaced. */
  hotCodeReplaceMessage?: { kind: "info" | "warning" | "error"; text: string };
}

/**
 * The latest runInTerminal program runs in a system terminal window
 * (`external`), or was asked to but runs in the integrated terminal because
 * the external one could not be used (`externalFallback`; `code` is the
 * backend's reason code, `message` its explanation).
 */
export type DebugConsoleNotice =
  | { kind: "external"; terminal: string }
  | { kind: "externalFallback"; code: string; message: string };

export type NewDebugSessionView = Pick<
  DebugSessionView,
  "id" | "projectRoot" | "configId" | "configName" | "type" | "noDebug" | "capabilities" | "startedAt"
> &
  Partial<Pick<DebugSessionView, "pluginId" | "hotCodeReplace">>;

export function newSessionView(init: NewDebugSessionView): DebugSessionView {
  return { ...init, state: "running", threads: [], frames: [], verified: {}, breakpointIds: {}, addressVerified: {}, addressBreakpointIds: {}, output: [], pendingCodeChanges: false };
}

/**
 * Whether a stackTrace answer of `count` frames for `levels` requested from
 * the top is the whole stack: fewer frames than requested end it (DAP), and
 * so does a totalFrames the answer reaches.
 */
export function stackTraceComplete(count: number, levels: number, totalFrames: unknown): boolean {
  return count < levels || (typeof totalFrames === "number" && count >= totalFrames);
}

/** Whether frameId is the bottom frame of the session's whole loaded stack (it has no caller). */
export function isBottomFrame(view: Pick<DebugSessionView, "frames" | "framesComplete">, frameId: number): boolean {
  return view.framesComplete === true && view.frames.length > 0 && view.frames[view.frames.length - 1]!.id === frameId;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** The view without the state of a stop (threads stay: they outlive a stop). */
function withoutStop(state: DebugSessionView): DebugSessionView {
  const next = { ...state, frames: [] };
  delete next.framesComplete;
  delete next.stoppedThreadId;
  delete next.stoppedReason;
  delete next.selectedFrameId;
  return next;
}

/**
 * Breakpoint results of one `setBreakpoints`, on the requested lines.
 * DAP answers in request order, so result i belongs to requested line i (a
 * line the adapter moved it to is kept as `actualLine`; the gutter shows the
 * stored one). A failed request (no results) leaves every requested line
 * unverified.
 */
export function breakpointResults(requestedLines: readonly number[], results: unknown): DebugBreakpointResult[] {
  const list = Array.isArray(results) ? results : [];
  if (list.length === 0) return requestedLines.map((line) => ({ line, verified: false }));
  const mapped: DebugBreakpointResult[] = [];
  list.forEach((result, index) => {
    if (!isRecord(result)) return;
    const line = requestedLines[index] ?? result.line;
    if (typeof line !== "number") return;
    // A result already pinned (re-applied from the store) keeps its actualLine.
    const actual = requestedLines[index] === undefined ? result.actualLine : result.line;
    mapped.push(withAdapterDetails({ line, verified: result.verified === true }, result, actual));
  });
  return mapped;
}

/**
 * result with the adapter's `id`, (when not verified) `message`, and
 * `actualLine` when it differs from the requested line. Each event or
 * response replaces the breakpoint's state: a reason it leaves out is dropped.
 */
function withAdapterDetails(result: DebugBreakpointResult, adapter: Record<string, unknown>, actualLine: unknown): DebugBreakpointResult {
  const next: DebugBreakpointResult = { line: result.line, verified: result.verified };
  const id = typeof adapter.id === "number" ? adapter.id : result.id;
  if (id !== undefined) next.id = id;
  if (!next.verified && typeof adapter.message === "string" && adapter.message !== "") next.message = adapter.message;
  if (typeof actualLine === "number" && actualLine !== next.line) next.actualLine = actualLine;
  return next;
}

/** The two kinds of source breakpoint results of a view: by project path, and by debugger address. */
const RESULT_KINDS = {
  path: { results: "verified", ids: "breakpointIds" },
  address: { results: "addressVerified", ids: "addressBreakpointIds" },
} as const;
type ResultKind = keyof typeof RESULT_KINDS;

/** ids with key's ids replaced by those of results. */
function idsWith(ids: Record<number, string>, key: string, results: readonly DebugBreakpointResult[]): Record<number, string> {
  const next: Record<number, string> = {};
  for (const [id, owner] of Object.entries(ids)) if (owner !== key) next[Number(id)] = owner;
  for (const result of results) if (result.id !== undefined) next[result.id] = key;
  return next;
}

function withResults(state: DebugSessionView, kind: ResultKind, key: string, results: DebugBreakpointResult[]): DebugSessionView {
  const fields = RESULT_KINDS[kind];
  return {
    ...state,
    [fields.results]: { ...state[fields.results], [key]: results },
    [fields.ids]: idsWith(state[fields.ids], key, results),
  };
}

export interface DebugReduceOptions {
  /** Source paths compare ignoring case (macOS and Windows file systems). */
  caseInsensitive?: boolean;
}

/**
 * Applies a DAP `breakpoint` event (late verification, a moved line, a
 * removed breakpoint). The result is found by id; failing that, by the
 * event's source path and line (a result requested on that line, else one
 * the adapter moved there). A result keeps its requested line; a different
 * adapter line is recorded as `actualLine`. A source path that is a
 * debugger address goes to `addressVerified` under the address verbatim.
 * Stored breakpoints are not changed.
 */
function applyBreakpointEvent(state: DebugSessionView, body: Record<string, unknown>, options: DebugReduceOptions): DebugSessionView {
  const bp = body.breakpoint;
  if (!isRecord(bp)) return state;
  const id = typeof bp.id === "number" ? bp.id : undefined;
  const line = typeof bp.line === "number" ? bp.line : undefined;
  const listed = id === undefined ? undefined : applyListBreakpointEvent(state, id, bp, body.reason === "removed");
  if (listed) return listed;
  let kind: ResultKind = id !== undefined && state.addressBreakpointIds[id] !== undefined ? "address" : "path";
  let key = id === undefined ? undefined : state[RESULT_KINDS[kind].ids][id];
  let index = key === undefined ? -1 : (state[RESULT_KINDS[kind].results][key] ?? []).findIndex((result) => result.id === id);
  if (key === undefined || index < 0) {
    const source = isRecord(bp.source) && typeof bp.source.path === "string" && bp.source.path !== "" ? bp.source.path : undefined;
    if (source === undefined || line === undefined) return state;
    // A debugger address is kept verbatim, apart from project paths.
    kind = isValidDebuggerAddress(source) ? "address" : "path";
    const keys = Object.keys(state[RESULT_KINDS[kind].results]);
    key = kind === "address" ? source : existingKey(keys, source, options.caseInsensitive === true);
    const candidates = state[RESULT_KINDS[kind].results][key] ?? [];
    index = candidates.findIndex((result) => result.line === line);
    if (index < 0) index = candidates.findIndex((result) => result.actualLine === line);
  }
  const results = [...(state[RESULT_KINDS[kind].results][key] ?? [])];
  const current = results[index];
  if (body.reason === "removed") {
    if (current === undefined) return state;
    results.splice(index, 1);
    return withResults(state, kind, key, results);
  }
  const requested = current?.line ?? line;
  if (requested === undefined) return state;
  const actual = line ?? current?.actualLine;
  const updated = withAdapterDetails({ line: requested, verified: bp.verified === true, id: current?.id }, bp, actual);
  if (current === undefined) results.push(updated);
  else results[index] = updated;
  return withResults(state, kind, key, results);
}

/** The view's path-less breakpoint lists (method and field breakpoints), each paired with the adapter's answers. */
const BREAKPOINT_LISTS = ["functionBreakpoints", "dataBreakpoints"] as const;

/**
 * A `breakpoint` event for a method or field breakpoint, found by id (line
 * breakpoint ids are matched first): see listResultsWithEvent. Id 0 is no
 * identity (java-debug gives it to every method breakpoint it cannot
 * resolve): an event for it changes no list item.
 */
function applyListBreakpointEvent(state: DebugSessionView, id: number, bp: Record<string, unknown>, removed: boolean): DebugSessionView | undefined {
  if (state.breakpointIds[id] !== undefined || state.addressBreakpointIds[id] !== undefined) return undefined;
  if (id === 0 && (state.functionBreakpoints?.some((result) => result.id === 0) || state.dataBreakpoints?.some((result) => result.id === 0))) return state;
  const event = removed ? { id } : bp;
  if (state.functionBreakpoints?.some((result) => result.id === id)) {
    return { ...state, functionBreakpoints: listResultsWithEvent(state.functionBreakpoints, id, event) };
  }
  if (state.dataBreakpoints?.some((result) => result.id === id)) {
    return { ...state, dataBreakpoints: listResultsWithEvent(state.dataBreakpoints, id, event) };
  }
  return undefined;
}

/**
 * The start-time results of a path-less list (`mincode.breakpointsSet` with
 * `functionBreakpoints` or `dataBreakpoints`), on what the session started
 * with (that list of the new view; a view without it was sent none).
 */
function applyStartListResults(state: DebugSessionView, kind: (typeof BREAKPOINT_LISTS)[number], body: Record<string, unknown>): DebugSessionView {
  const error = typeof body.error === "string" && body.error !== "" ? body.error : undefined;
  if (kind === "functionBreakpoints") {
    if (!state.functionBreakpoints) return state;
    return { ...state, functionBreakpoints: pairedListResults(state.functionBreakpoints.map(identityOf), body.functionBreakpoints, error) };
  }
  if (!state.dataBreakpoints) return state;
  return { ...state, dataBreakpoints: pairedListResults(state.dataBreakpoints.map(identityOf), body.dataBreakpoints, error) };
}

/** Applies one session event; returns state itself when nothing changed. */
export function reduceDebugEvent(state: DebugSessionView, e: DebugEvent, options: DebugReduceOptions = {}): DebugSessionView {
  const body: Record<string, unknown> = isRecord(e.body) ? e.body : {};
  switch (e.event) {
    case "stopped": {
      if (state.state === "ended") return state;
      const next: DebugSessionView = { ...state, state: "paused", lastError: undefined, stopSeq: (state.stopSeq ?? 0) + 1 };
      if (typeof body.reason === "string") next.stoppedReason = body.reason;
      else delete next.stoppedReason;
      if (typeof body.threadId === "number") next.stoppedThreadId = body.threadId;
      else delete next.stoppedThreadId;
      return next;
    }
    case "continued":
      if (state.state === "ended") return state;
      return { ...withoutStop(state), state: "running" };
    case DEBUG_SESSION_ENDED_EVENT: {
      // Nothing can be applied to an ended session.
      const next: DebugSessionView = { ...withoutStop(state), state: "ended", pendingCodeChanges: false };
      if (typeof body.reason === "string") next.endReason = body.reason;
      return next;
    }
    case DEBUG_BREAKPOINTS_SET_EVENT: {
      const kind = body.path === undefined || body.path === "" ? BREAKPOINT_LISTS.find((key) => key in body) : undefined;
      if (kind) return applyStartListResults(state, kind, body);
      if (typeof body.path !== "string" || body.path === "") return state;
      // A debugger address is not a project path; its results are kept apart.
      return withResults(state, body.address === true ? "address" : "path", body.path, breakpointResults([], body.breakpoints));
    }
    case "breakpoint":
      return applyBreakpointEvent(state, body, options);
    case "output": {
      if (typeof body.output !== "string") return state;
      const category = typeof body.category === "string" ? body.category : "console";
      // `telemetry` output is for the adapter's owner, not the user.
      if (category === "telemetry") return state;
      if (body.output === "") return state;
      let seq = state.output[state.output.length - 1]?.seq ?? 0;
      const added = splitOutput(body.output, MAX_SESSION_OUTPUT_TEXT).map((text) => ({ seq: (seq += 1), category, text }));
      const output = [...state.output, ...added];
      return { ...state, output: output.length > MAX_SESSION_OUTPUT_LINES ? output.slice(-MAX_SESSION_OUTPUT_LINES) : output };
    }
    case DEBUG_RUN_IN_TERMINAL_EVENT: {
      if (body.external === true) {
        const terminal = typeof body.terminal === "string" ? body.terminal : "";
        return { ...state, consoleNotice: { kind: "external", terminal } };
      }
      if (typeof body.terminalSessionId !== "string" || body.terminalSessionId === "") return state;
      if (state.consoleTerminalIds?.includes(body.terminalSessionId)) return state;
      const next: DebugSessionView = {
        ...state,
        consoleTerminalId: body.terminalSessionId,
        consoleTerminalIds: [...(state.consoleTerminalIds ?? []), body.terminalSessionId],
      };
      const fallback = body.externalFallback;
      if (isRecord(fallback) && typeof fallback.code === "string" && typeof fallback.message === "string") {
        next.consoleNotice = { kind: "externalFallback", code: fallback.code, message: fallback.message };
      } else {
        delete next.consoleNotice;
      }
      return next;
    }
    default:
      return state;
  }
}

/**
 * Splits output into pieces of at most `limit` characters, cutting after the
 * last line break that fits (line breaks stay with their line) and, for a
 * longer line, never between the halves of a surrogate pair.
 */
export function splitOutput(text: string, limit: number): string[] {
  const pieces: string[] = [];
  let start = 0;
  while (text.length - start > limit) {
    let cut = text.lastIndexOf("\n", start + limit - 1) + 1;
    if (cut <= start) {
      cut = start + limit;
      if (isHighSurrogate(text.charCodeAt(cut - 1))) cut -= 1;
    }
    pieces.push(text.slice(start, cut));
    start = cut;
  }
  if (start < text.length) pieces.push(text.slice(start));
  return pieces;
}

function isHighSurrogate(code: number): boolean {
  return code >= 0xd800 && code <= 0xdbff;
}

function truncate(text: string, limit: number): string {
  return text.length > limit ? `${text.slice(0, limit - 1)}…` : text;
}

function noticeValue(value: unknown): string {
  return typeof value === "string" ? value : (JSON.stringify(value) ?? String(value));
}

/**
 * What the debugger will run besides the adapter, for the launch
 * confirmation: the request, the arguments that name the debuggee, and the
 * full arguments (truncated).
 */
export function debuggeeNotices(p: PreparedDebugLaunch, fullArgumentsLabel = "arguments"): string[] {
  const notices = [`request: ${p.request}`];
  const shown = withMaskedSecrets(p.arguments);
  for (const key of NOTICE_KEYS) {
    if (shown[key] === undefined) continue;
    notices.push(`${key}: ${truncate(noticeValue(shown[key]), NOTICE_VALUE_LIMIT)}`);
  }
  for (const key of ["classPaths", "modulePaths"]) {
    if (Array.isArray(p.arguments[key])) notices.push(`${key}: ${p.arguments[key].length} entries`);
  }
  notices.push(`${fullArgumentsLabel}: ${truncate(JSON.stringify(withMaskedEnv(shown)), NOTICE_ARGUMENTS_LIMIT)}`);
  return notices;
}

/** What the confirmation shows instead of an environment variable's value. */
const MASKED_VALUE = "•••";

/**
 * The arguments with the values of the DAP launch `env` (adapter-neutral;
 * often merged from a `.env` file with secrets) masked: the names stay.
 */
function withMaskedEnv(args: Record<string, unknown>): Record<string, unknown> {
  const env = args.env;
  if (env === undefined) return args;
  const masked =
    typeof env === "object" && env !== null && !Array.isArray(env)
      ? Object.fromEntries(Object.keys(env).map((name) => [name, MASKED_VALUE]))
      : MASKED_VALUE;
  return { ...args, env: masked };
}

/** Words that make an option's name look like it holds a secret (compared lower-case, `-` and `.` read as `_`). */
const SECRET_NAME_WORDS = ["password", "passwd", "secret", "token", "apikey", "api_key", "credential", "private_key"];

/** An option name before `=`: `-Dname`, `--name`, `-name` or `name`, possibly after an opening quote. */
const ASSIGNMENT = /(^|[\s"'])(["']?)((?:-D|--?)?)([A-Za-z0-9_.-]+)=/g;

function secretName(name: string): boolean {
  const normalized = name.toLowerCase().replace(/[-.]/g, "_");
  return SECRET_NAME_WORDS.some((word) => normalized.includes(word));
}

/**
 * A command line (vmArgs, args) as the confirmation shows it: the value of
 * every `-Dname=value`, `--name=value` or `name=value` whose name looks like
 * a secret becomes `•••` (a quoted value, or the rest of a quoted option, as a
 * whole). Display only: what is launched is unchanged.
 */
export function maskSecretAssignments(text: string): string {
  let out = "";
  let at = 0;
  ASSIGNMENT.lastIndex = 0;
  for (let match = ASSIGNMENT.exec(text); match; match = ASSIGNMENT.exec(text)) {
    const valueStart = match.index + match[0].length;
    const opening = match[2];
    let valueEnd: number;
    const first = text[valueStart];
    if (first === '"' || first === "'") {
      const close = text.indexOf(first, valueStart + 1);
      valueEnd = close < 0 ? text.length : close + 1;
    } else if (opening) {
      const close = text.indexOf(opening, valueStart);
      valueEnd = close < 0 ? text.length : close;
    } else {
      const space = text.slice(valueStart).search(/\s/);
      valueEnd = space < 0 ? text.length : valueStart + space;
    }
    ASSIGNMENT.lastIndex = Math.max(valueEnd, valueStart);
    if (!secretName(match[4]!)) continue;
    out += text.slice(at, valueStart) + MASKED_VALUE;
    at = valueEnd;
  }
  return out + text.slice(at);
}

/**
 * `scheme://user:password@`: the password, non-empty, up to the last `@`
 * before any `?`, `#`, quote or space, so an unencoded `@` or `/` in it is
 * masked too (a path holding `@` after a `user:` is masked with it).
 */
const URL_USERINFO = /([A-Za-z][A-Za-z0-9+.-]*:\/\/[^\s/?#@:'"]*:)([^\s?#'"]+)@/g;

/** A URL query (`?` up to a fragment, quote or space) of `name=value` pairs. */
const URL_QUERY = /\?([^\s#'"]*)/g;

/** One `name=value` in a query, separated by `&` (or `;`, as some JDBC URLs do). */
const QUERY_PARAMETER = /(^|[&;])([^&;=]+)=([^&;]*)/g;

/** A `jdbc:` URL value (up to a quote or space), whose properties may follow `;` without a `?` (SQL Server, jTDS). */
const JDBC_URL = /jdbc:[^\s'"]*/g;

/** One `;name=value` property of a JDBC URL. */
const JDBC_PROPERTY = /;([^;=?&#]+)=([^;]*)/g;

/**
 * Secrets embedded in a value as the confirmation shows it: the value of each
 * URL query parameter (`?…`, `&`- or `;`-separated) whose name looks like a
 * secret (`jdbc:…?password=x`, `https://h/p?token=y`), of each `;name=value`
 * property of a `jdbc:` URL (`jdbc:sqlserver://h;password=x`), and the
 * password of URL userinfo (`scheme://user:pass@host` →
 * `scheme://user:•••@host`) become `•••`; everything else stays verbatim.
 * Not masked: a URL nested inside another URL's query. Display only.
 */
export function maskEmbeddedSecrets(text: string): string {
  // JDBC properties first: a `;password=` value holding `@` would otherwise
  // look like userinfo and leave its tail after the `@` visible.
  return text
    .replace(JDBC_URL, (url: string) =>
      url.replace(JDBC_PROPERTY, (property: string, name: string) => (secretName(name) ? `;${name}=${MASKED_VALUE}` : property)),
    )
    .replace(URL_USERINFO, (_match, head: string) => `${head}${MASKED_VALUE}@`)
    .replace(URL_QUERY, (query: string, pairs: string) => {
      if (!pairs.includes("=")) return query;
      return `?${pairs.replace(QUERY_PARAMETER, (pair: string, separator: string, name: string) =>
        secretName(name) ? `${separator}${name}=${MASKED_VALUE}` : pair,
      )}`;
    });
}

/** maskSecretAssignments, then maskEmbeddedSecrets. */
function maskSecrets(text: string): string {
  return maskEmbeddedSecrets(maskSecretAssignments(text));
}

/** The arguments with secret-looking values of `vmArgs` and `args` (a string or a list of strings) masked. */
function withMaskedSecrets(args: Record<string, unknown>): Record<string, unknown> {
  const masked = { ...args };
  for (const key of ["vmArgs", "args"]) {
    const value = args[key];
    if (typeof value === "string") masked[key] = maskSecrets(value);
    else if (Array.isArray(value)) masked[key] = value.map((item) => (typeof item === "string" ? maskSecrets(item) : item));
  }
  return masked;
}
