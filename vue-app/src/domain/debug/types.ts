/**
 * Debugger (contributes.debuggers + DAP sessions) frontend types.
 *
 * Plugin-facing shapes mirror the Go JSON of
 * backend/plugin/debuggers_launch.go; session shapes mirror
 * backend/debug (commands.go, registry.go, session.go) and
 * backend/terminal/debuggee_shell.go. DAP types are the subset the
 * debugger UI reads (https://microsoft.github.io/debug-adapter-protocol/specification).
 */
export interface ProcessLaunchFile {
  relative: string;
  sha256: string;
}

/** JSON shape of the Go DebuggerProcessLaunch. */
export interface DebuggerProcessLaunch {
  pluginId: string;
  pluginName: string;
  pluginVersion: string;
  point: string;
  entryKey: string;
  title: string;
  /** Per-language titles (Go map[string]string), resolved for display by processLaunchTitle. */
  titleLocalized?: Record<string, string>;
  argv: string[];
  env: Record<string, string>;
  cwd: string;
  pluginExecutable: boolean;
  executableSha256?: string;
  pluginFiles?: ProcessLaunchFile[];
  fingerprint: string;
  trusted: boolean;
  previouslyTrusted: boolean;
}

export type DebugRequestKind = "launch" | "attach";

/** The JSON Schema subset a debugger's configurationSchema uses. */
export interface JsonSchemaObject {
  type?: string | string[];
  description?: string;
  default?: unknown;
  enum?: unknown[];
  properties?: Record<string, JsonSchemaObject>;
  items?: JsonSchemaObject;
  required?: string[];
  additionalProperties?: boolean | JsonSchemaObject;
  [keyword: string]: unknown;
}

/** One `configurationSnippets` entry of a debugger (python-debug plugin.json). */
export interface DebugConfigurationSnippet {
  label: string;
  labelLocalized?: Record<string, string>;
  request: DebugRequestKind;
  body: Record<string, unknown>;
}

/**
 * Plugin texts for a host-drawn debugger input (all optional, plain text):
 * the exception type list (`exceptionTypes`) or the method breakpoint name
 * (`functionBreakpoints`).
 */
export interface DebuggerTextHints {
  placeholder?: string;
  placeholderLocalized?: Record<string, string>;
  description?: string;
  descriptionLocalized?: Record<string, string>;
}

/**
 * A debugger's hot code replace extension: the adapter's custom DAP event
 * that reports recompiled code and the custom request that applies it
 * (validated by the host: never a DAP standard name).
 */
export interface DebuggerHotCodeReplace {
  event: string;
  request: string;
  /**
   * The plugin's own setting (contributes.configuration key) holding the
   * mode `auto` | `manual` | `never`; the host checked at install that it is
   * a global string setting with exactly those values. Absent: auto.
   */
  modeSetting?: string;
}

/** A debugger offered on this platform (plugin_host_debuggers_list item). */
export interface DebuggerInfo {
  pluginId: string;
  pluginName: string;
  type: string;
  title: string;
  titleLocalized?: Record<string, string>;
  languages: string[];
  configurationSchema?: { launch?: JsonSchemaObject; attach?: JsonSchemaObject };
  configurationSnippets: DebugConfigurationSnippet[];
  remote?: { forward: "local" | "remote"; argsTemplate: string };
  hasEntryPoints: boolean;
  hasCheck: boolean;
  /**
   * Set when the debugger offers the "pause only on these exception types"
   * list (plugin texts for its input); the list is saved per project and sent
   * through `applyDebuggerSettings`.
   */
  exceptionTypes?: DebuggerTextHints;
  /** The debugger has a settingsAction that receives the saved settings. */
  hasSettings?: boolean;
  /**
   * Plugin texts for the method breakpoint input. Declaring them is what
   * offers the breakpoints dialog's method breakpoint section for this
   * debugger type; the saved names are sent only to sessions whose adapter
   * declares `supportsFunctionBreakpoints`.
   */
  functionBreakpoints?: DebuggerTextHints;
  /** Set when the debugger declares a hot code replace extension. */
  hotCodeReplace?: DebuggerHotCodeReplace;
  /** Adapter kind ("stdio", "tcp", "jdtls"); a "jdtls" debugger runs inside the built-in Java language service. */
  adapterKind?: string;
}

/** A user-authored run configuration; `fields` is the DAP configuration body. */
export interface RunConfiguration {
  id: string;
  name: string;
  pluginId: string;
  type: string;
  request: DebugRequestKind;
  fields: Record<string, unknown>;
}

/** A breakpoint as MinCode stores it; `enabled` never reaches the backend. */
export interface SourceBreakpoint {
  line: number;
  condition?: string;
  hitCondition?: string;
  /** Nonblank DAP logMessage makes this a non-stopping logpoint. */
  logMessage?: string;
  enabled: boolean;
}

/** A breakpoint as the backend accepts it (Go debug.SourceBreakpoint). */
export type BackendSourceBreakpoint = Omit<SourceBreakpoint, "enabled">;

/**
 * A method (DAP function) breakpoint as the backend accepts it (Go
 * debug.FunctionBreakpoint); the adapter defines the syntax of `name`.
 * Host limits: name non-empty single line ≤ 1024 characters, conditions
 * ≤ 4096 characters, ≤ 256 per start.
 */
export interface FunctionBreakpointSpec {
  name: string;
  condition?: string;
  hitCondition?: string;
}

/** DAP DataBreakpointAccessType. */
export type DataBreakpointAccessType = "read" | "write" | "readWrite";

/**
 * A field (DAP data) breakpoint as the backend accepts it (Go
 * debug.DataBreakpoint). `dataId` is the adapter's token from
 * `dataBreakpointInfo`, sent back verbatim; no `accessType` means the
 * adapter's default. Host limits as for FunctionBreakpointSpec.
 */
export interface DataBreakpointSpec {
  dataId: string;
  accessType?: DataBreakpointAccessType;
  condition?: string;
  hitCondition?: string;
}

/** The `mincode.breakpointsSet` result keys of the path-less start-time lists. */
export type DebugBreakpointListKind = "functionBreakpoints" | "dataBreakpoints";

/** The adapter's answer for one function or data breakpoint, in request order. */
export interface DebugBreakpointListItemResult {
  verified: boolean;
  /** The adapter's breakpoint id, for later `breakpoint` events. */
  id?: number;
  /** Why it is not verified (the adapter's text, or the failure of the whole request). */
  message?: string;
}

/** The results of the function or data breakpoints sent when a session started. */
export interface DebugBreakpointListResult {
  kind: DebugBreakpointListKind;
  results: DebugBreakpointListItemResult[];
  /** The adapter's failure message when the whole request failed. */
  error?: string;
}

/**
 * One source's `mincode.breakpointsSet` result (a start-time setBreakpoints or
 * its failure). `address` is true when `path` is a debugger-provided source
 * address (start request `addressBreakpoints`), an opaque string that must
 * never be treated as a project file path; false for an absolute file path.
 */
export interface DebugSourceBreakpointsSet {
  path: string;
  address: boolean;
  /** The adapter's raw `breakpoints` (null on failure); pin them with `breakpointResults`. */
  breakpoints: unknown;
  /** The adapter's failure message when the request failed. */
  error?: string;
}

/** A resolved debugger launch awaiting confirmation or start. */
export interface PreparedDebugLaunch {
  prepareId: string;
  launch: DebuggerProcessLaunch;
  type: string;
  kind: string;
  projectRoot: string;
  request: DebugRequestKind;
  arguments: Record<string, unknown>;
}

export interface DebugStartResult {
  sessionId: string;
  capabilities: DapCapabilities;
  /** Breakpoint paths that failed the project-root check. */
  skippedBreakpoints: string[];
}

/** A runnable line offered by the debugger's entryPointsAction. */
export interface DebuggerEntryPoint {
  line: number;
  label: string;
  configuration: Record<string, unknown>;
}

export interface DebuggerMissingTool {
  name: string;
  hint: string;
  /** Only shown; run in a visible terminal after the user confirms. */
  installCommand?: string;
}

export interface DebuggerCheckResult {
  ready: boolean;
  missing: DebuggerMissingTool[];
}

/** A registered debug session (Go debug.SessionInfo). */
export interface DebugSessionInfo {
  id: string;
  projectRoot: string;
  ownerPluginId: string;
  debuggerType: string;
  /** Adapter start time in Unix milliseconds. */
  startedAt: number;
}

/** attach_local_shell result for a debuggee terminal. */
export interface AttachTerminalResult {
  ok: boolean;
  /** The output the session kept so far. */
  output: string;
  /** Total output bytes produced; drop `shell-output` events at or below it. */
  offset: number;
  /** The program has exited; only its kept output remains. */
  ended: boolean;
}

/** MinCode's own session events on `debug:event` (besides adapter events). */
export const DEBUG_SESSION_ENDED_EVENT = "mincode.sessionEnded";
export const DEBUG_BREAKPOINTS_SET_EVENT = "mincode.breakpointsSet";
export const DEBUG_RUN_IN_TERMINAL_EVENT = "mincode.runInTerminal";

/** One `debug:event` payload: an adapter event or a `mincode.*` event. */
export interface DebugEvent {
  sessionId: string;
  event: string;
  // The body differs per DAP event; reducers narrow it by `event`.
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  body?: any;
}

// ---- DAP subset ----

export interface DapExceptionBreakpointsFilter {
  filter: string;
  label: string;
  description?: string;
  default?: boolean;
  supportsCondition?: boolean;
}

export interface DapCapabilities {
  supportsConditionalBreakpoints?: boolean;
  supportsHitConditionalBreakpoints?: boolean;
  supportsLogPoints?: boolean;
  supportsEvaluateForHovers?: boolean;
  supportsGotoTargetsRequest?: boolean;
  supportsTerminateRequest?: boolean;
  supportsSetVariable?: boolean;
  supportsExceptionInfoRequest?: boolean;
  supportsRestartFrame?: boolean;
  supportsStepInTargetsRequest?: boolean;
  supportsCompletionsRequest?: boolean;
  supportsFunctionBreakpoints?: boolean;
  supportsDataBreakpoints?: boolean;
  exceptionBreakpointFilters?: DapExceptionBreakpointsFilter[];
}

export interface DapThread {
  id: number;
  name: string;
}

export interface DapSource {
  name?: string;
  path?: string;
  sourceReference?: number;
}

export interface DapStackFrame {
  id: number;
  name: string;
  source?: DapSource;
  line: number;
  column: number;
  endLine?: number;
  endColumn?: number;
  /** False when the adapter cannot restart this frame (DAP restartFrame). */
  canRestart?: boolean;
  presentationHint?: "normal" | "label" | "subtle";
}

/** A function call a stepIn can enter (DAP stepInTargets), e.g. one of several calls on a line. */
export interface DapStepInTarget {
  id: number;
  label: string;
  line?: number;
  column?: number;
}

/**
 * A completion of an expression (DAP completions). `text` is inserted
 * (`label` when absent); `length` (UTF-16 code units before the cursor) says
 * what it replaces, see applyDebugCompletion.
 */
export interface DapCompletionItem {
  label: string;
  text?: string;
  type?: string;
  start?: number;
  length?: number;
}

export interface DapScope {
  name: string;
  variablesReference: number;
  expensive: boolean;
  presentationHint?: string;
  namedVariables?: number;
  indexedVariables?: number;
}

export interface DapVariable {
  name: string;
  value: string;
  type?: string;
  evaluateName?: string;
  /** > 0 when the variable has children (load with `variables`). */
  variablesReference: number;
  namedVariables?: number;
  indexedVariables?: number;
}

export interface DapEvaluateResult {
  result: string;
  type?: string;
  variablesReference: number;
  namedVariables?: number;
  indexedVariables?: number;
}

/** The DAP `exceptionInfo` response body: the exception a thread stopped on. */
export interface DapExceptionInfo {
  exceptionId: string;
  description?: string;
  breakMode?: string;
  details?: { message?: string; typeName?: string; stackTrace?: string };
}
