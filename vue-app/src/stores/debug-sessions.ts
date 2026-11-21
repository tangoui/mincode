/**
 * Live debug sessions: the launch flow, `debug:event` handling, execution
 * control (continue / pause / step / stop / restart), threads, frames,
 * variables, evaluate and breakpoint sync (line, method and field breakpoints). Runtime state only: sessions live
 * in the desktop host and are not persisted. Intentionally global (not
 * workspace-scoped): a session belongs to a project, and every workspace
 * that shows that project's editor shows its breakpoint state.
 *
 * Ended sessions stay listed (their output stays readable) until `close`.
 */
import { computed, reactive, ref, watch } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import { ElMessage } from "element-plus";
import {
  DATA_BREAKPOINT_LIMITS,
  isValidDataId,
  parseDataBreakpointInfo,
  sameDataBreakpoint,
  sameDataBreakpointLists,
  sessionDataBreakpointList,
  toggledDataBreakpoints,
  type DataBreakpointInfo,
  type DebugDataBreakpointResult,
  type StoredDataBreakpoint,
} from "@/domain/debug/data-breakpoints";
import { breakpointAddress, breakpointAppliesTo, breakpointAtKey, type BreakpointAt } from "@/domain/debug/address-breakpoints";
import {
  enabledFunctionBreakpointNames,
  pairedListResults,
  type BreakpointListIdentity,
  type DebugFunctionBreakpointResult,
} from "@/domain/debug/function-breakpoints";
import {
  frameInChangedTypes,
  hotCodeReplaceMode,
  parseHotCodeReplaceEvent,
  parseHotCodeReplaceResult,
  type HotCodeReplaceMode,
  type HotCodeReplaceResult,
} from "@/domain/debug/hot-code-replace";
import { debugErrorText } from "@/domain/debug/errors";
import { toRelativeKey, toSourcePath } from "@/domain/debug/paths";
import {
  breakpointResults,
  isBottomFrame,
  newSessionView,
  reduceDebugEvent,
  stackTraceComplete,
  type DebugBreakpointHoverDetails,
  type DebugBreakpointResult,
  type DebugSessionView,
} from "@/domain/debug/session-model";
import {
  DEBUG_BREAKPOINTS_SET_EVENT,
  DEBUG_SESSION_ENDED_EVENT,
  type DapCapabilities,
  type DapCompletionItem,
  type DataBreakpointAccessType,
  type DapEvaluateResult,
  type DapExceptionInfo,
  type DapScope,
  type DapSource,
  type DapStackFrame,
  type DapStepInTarget,
  type DapThread,
  type DapVariable,
  type DebugBreakpointListItemResult,
  type DebugEvent,
  type DebuggerHotCodeReplace,
  type DebuggerInfo,
  type RunConfiguration,
  type SourceBreakpoint,
} from "@/domain/debug/types";
import type { DebugLaunchProgress, DebugLaunchStep } from "@/domain/debug/launch-progress";
import { useVueI18n } from "@/i18n/messages";
import {
  debugRequest,
  listDebuggers,
  onDebugEvent,
  setSessionAddressBreakpoints,
  setSessionBreakpoints,
  stopDebugSession,
} from "@/services/debug-service";
import { launchDebugSession, syncDebuggerSettings, type DebugLaunchOptions } from "@/services/debug-launch";
import { isDesktopRuntime } from "@/services/desktop-bridge";
import { pathsAreCaseInsensitive, sameProjectRoot, useDebugConfigsStore } from "@/stores/debug-configs";
import { useLocalProjectStore } from "@/stores/local-project";

/** Events of a session id not listed yet (they arrive while start is pending). */
const MAX_BUFFERED_SESSIONS = 16;
/** Buffered `output` events per session (the oldest go first); other events are always kept. */
const MAX_BUFFERED_OUTPUT_EVENTS = 500;
const STACK_LEVELS = 50;
/** Events kept per session while its debugger's hot code replace declaration is being read again. */
const MAX_UNDECLARED_EVENTS = 32;

export type DebugEvaluateContext = "hover" | "watch" | "repl";

/**
 * A launch in flight as the debug panel shows it (transient, never
 * persisted): its configuration, current step, the language service's
 * latest status text and when anything last progressed.
 */
export interface DebugLaunchView {
  seq: number;
  root: string;
  configId: string;
  configName: string;
  noDebug: boolean;
  step: DebugLaunchStep;
  /** The step list includes starting the language service (debuggers it hosts). */
  languageService: boolean;
  /** The language service's latest status text ("" until one arrives). */
  status: string;
  /** Date.now() of the last step change, status text or confirmation dialog opening or closing. */
  progressAt: number;
  /** A confirmation dialog of the launch is open (the stall hint waits). */
  confirming: boolean;
  /** The user cancelled; the launch ends once its current step returns. */
  cancelling: boolean;
}

/** What the Variables pane remembers for one method of a session (composables/use-debug-variable-expansion.ts). */
export interface DebugFrameExpansion {
  /** The expanded tree paths. */
  paths: Set<string>;
  /** The method was shown before: its paths are what the user left, not the default. */
  seeded: boolean;
  /** Where the user left the pane scrolled (px from the top). */
  scrollTop: number;
}

/** What the breakpoints dialog shows for a method or field breakpoint across the project's running sessions. */
export interface DebugBreakpointListStatus {
  verified: boolean;
  /** The adapter's reason (or the failed request's message) when no session verified it. */
  message?: string;
}

/** What the breakpoints dialog shows for a saved method breakpoint name. */
export type DebugFunctionBreakpointStatus = DebugBreakpointListStatus;

/** status with one more session's result: verified once any session verified it, else the first reason given. */
function mergedStatus(status: DebugBreakpointListStatus | undefined, result: DebugBreakpointListItemResult): DebugBreakpointListStatus {
  if (status?.verified) return status;
  if (result.verified) return { verified: true };
  if (status?.message !== undefined || result.message === undefined) return status ?? { verified: false };
  return { verified: false, message: result.message };
}

/**
 * How one path-less breakpoint list of a session is sent: the capability the
 * adapter must declare, the request, what the session should have (sent in
 * order, as `payload`), how the answers are stored on the view, and the
 * sequence number of each session's latest request (older answers are dropped).
 */
interface BreakpointListSync<T extends DebugBreakpointListItemResult> {
  capability: "supportsFunctionBreakpoints" | "supportsDataBreakpoints";
  command: "setFunctionBreakpoints" | "setDataBreakpoints";
  syncs: Map<string, number>;
  items: (view: DebugSessionView) => BreakpointListIdentity<T>[];
  payload: (item: BreakpointListIdentity<T>) => Record<string, unknown>;
  store: (view: DebugSessionView, results: T[]) => DebugSessionView;
}

/**
 * A field breakpoint the breakpoints dialog lists: a saved one of a debugger
 * type, or one of a running session that cannot be saved (`sessionOnly`).
 */
export interface DebugDataBreakpointEntry extends StoredDataBreakpoint {
  type: string;
  sessionOnly: boolean;
  /** Absent when no running session was sent it; verified when any session verified it. */
  status?: DebugBreakpointListStatus;
}

/**
 * The step-in targets Smart Step Into offers for one stop: the debug tool
 * window of sessionId shows them as a menu while the session is still paused
 * with frameId (the top frame of that stop) on top.
 */
export interface DebugStepTargetsPick {
  sessionId: string;
  frameId: number;
  targets: DapStepInTarget[];
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** Targets by (line, column), a missing column first on its line; targets without a line after them, in their order. */
function sortedStepInTargets(targets: DapStepInTarget[]): DapStepInTarget[] {
  const positioned = targets.filter((target) => target.line !== undefined);
  const unpositioned = targets.filter((target) => target.line === undefined);
  // Array.prototype.sort is stable, so equal positions keep the adapter's order.
  positioned.sort((a, b) => a.line! - b.line! || (a.column ?? 0) - (b.column ?? 0));
  return [...positioned, ...unpositioned];
}

function enabledCopies(list: readonly SourceBreakpoint[]): SourceBreakpoint[] {
  return list.filter((bp) => bp.enabled).map((bp) => ({ ...bp }));
}

export const useDebugSessionsStore = defineStore("debug-sessions", () => {
  const { t, te } = useVueI18n();
  const configs = useDebugConfigsStore();
  const projects = useLocalProjectStore();

  const sessions = ref<DebugSessionView[]>([]);
  const selectedSessionId = ref<string | null>(null);
  /** Sessions whose breakpoints are muted (they get empty lists); in memory only. */
  const mutedSessionIds = ref<string[]>([]);
  /** Session → bumped when a variable was changed (setVariable); shown values are read again. */
  const variablesVersions = ref<Record<string, number>>({});
  /**
   * Launches in flight (guard → prepare → confirmation → start). One per
   * project and configuration: the toolbar, keybindings and Restart all
   * start through `launch`, so a second trigger while one runs is ignored.
   * The debug panel shows each one's steps until it ends (started, failed,
   * declined or cancelled).
   */
  const launching = ref<DebugLaunchView[]>([]);
  let launchSeq = 0;
  /** Launch seq → its cancellation (cancelLaunch). */
  const launchControllers = new Map<number, AbortController>();
  /** Session id → method → what the Variables pane remembers (variableExpansionFor); dropped when the session ends or is closed. */
  const variableExpansions = new Map<string, Map<string, DebugFrameExpansion>>();
  /** Launch seq → how many debug panels show it with its Cancel (setLaunchShown). */
  const shownLaunches = ref<Readonly<Record<number, number>>>({});

  const selectedSession = computed(() => sessions.value.find((s) => s.id === selectedSessionId.value) ?? null);
  /** Some session has not ended. */
  const isActive = computed(() => sessions.value.some((s) => s.state !== "ended"));
  /** The selected session is paused (what step / continue act on). */
  const isPaused = computed(() => selectedSession.value?.state === "paused");

  // Bookkeeping that the UI does not render.
  /** Bumped by every stop / resume / end / thread switch; async results of an older value are dropped. */
  const generations = new Map<string, number>();
  const activeFiles = new Map<string, string | undefined>();
  /** Session → release of what it holds (jdtls for a Java session), called once it ends or is forgotten. */
  const holds = new Map<string, () => void>();
  const buffered = new Map<string, DebugEvent[]>();
  /** Session → file holding a run-to-cursor breakpoint, restored on the next stop. */
  const runToCursorFiles = new Map<string, string>();
  /** Smart Step Into's open choice (one at a time) and the session generation it belongs to. */
  const stepTargetsPick = ref<DebugStepTargetsPick | null>(null);
  let stepTargetsGeneration = 0;
  /** Sessions with a stepInTargets request in flight. */
  const smartStepping = new Set<string>();
  /** Closed or restarted sessions: their late events (e.g. the end) are dropped, not buffered. */
  const retired = new Set<string>();
  let unsubscribe: (() => void) | undefined;

  function subscribe() {
    if (unsubscribe || !isDesktopRuntime()) return;
    try {
      unsubscribe = onDebugEvent(handleEvent);
    } catch {
      unsubscribe = undefined;
    }
  }

  function find(id: string): DebugSessionView | undefined {
    return sessions.value.find((s) => s.id === id);
  }

  function update(id: string, change: (view: DebugSessionView) => DebugSessionView) {
    const index = sessions.value.findIndex((s) => s.id === id);
    if (index < 0) return;
    const next = change(sessions.value[index]!);
    sessions.value[index] = next;
    if (next.state === "ended") releaseHold(id);
  }

  function releaseHold(id: string) {
    const release = holds.get(id);
    holds.delete(id);
    release?.();
  }

  function generation(id: string): number {
    return generations.get(id) ?? 0;
  }

  function bump(id: string): number {
    const next = generation(id) + 1;
    generations.set(id, next);
    return next;
  }

  function setError(id: string, error: unknown) {
    update(id, (view) => ({ ...view, lastError: debugErrorText(error) }));
  }

  function clearError(id: string) {
    if (find(id)?.lastError !== undefined) update(id, (view) => ({ ...view, lastError: undefined }));
  }

  // ---- events ----

  function handleEvent(event: DebugEvent) {
    if (find(event.sessionId)) {
      applyEvent(event);
      return;
    }
    if (retired.has(event.sessionId)) return;
    const list = buffered.get(event.sessionId) ?? [];
    if (!buffered.has(event.sessionId)) {
      if (buffered.size >= MAX_BUFFERED_SESSIONS) buffered.delete(buffered.keys().next().value!);
      buffered.set(event.sessionId, list);
    }
    list.push(event);
    // Control events (stopped, sessionEnded, runInTerminal, …) must survive a
    // chatty program, so only output is capped.
    if (event.event === "output" && list.filter((item) => item.event === "output").length > MAX_BUFFERED_OUTPUT_EVENTS) {
      list.splice(
        list.findIndex((item) => item.event === "output"),
        1,
      );
    }
  }

  /** The enabled breakpoint lines of a file or debugger address, in the order they were sent. */
  function requestedLines(root: string, at: BreakpointAt): number[] {
    return enabledCopies(configs.breakpointsFor(root, at)).map((bp) => bp.line);
  }

  function applyEvent(event: DebugEvent) {
    const view = find(event.sessionId);
    if (!view) return;
    let applied = event;
    const body: unknown = event.body;
    if (event.event === DEBUG_BREAKPOINTS_SET_EVENT && isRecord(body) && typeof body.path === "string" && body.path !== "") {
      // Results may omit `line` (and are null on failure): pin them to the requested lines.
      const at: BreakpointAt = body.address === true ? { address: body.path, type: view.type } : body.path;
      const results = breakpointResults(requestedLines(view.projectRoot, at), body.breakpoints);
      applied = { ...event, body: { ...body, breakpoints: results } };
    }
    update(event.sessionId, (current) => reduceDebugEvent(current, applied, { caseInsensitive: pathsAreCaseInsensitive() }));

    const id = event.sessionId;
    switch (event.event) {
      case "stopped":
        bump(id);
        void restoreRunToCursor(id);
        void refreshPaused(id);
        break;
      case "continued":
        bump(id);
        break;
      case DEBUG_SESSION_ENDED_EVENT:
        bump(id);
        runToCursorFiles.delete(id);
        variableExpansions.delete(id);
        break;
    }
    // Only the event the session's debugger declared; a same-named event of another debugger is not ours.
    if (view.hotCodeReplace && event.event === view.hotCodeReplace.event) void handleHotCodeReplaceEvent(id, event.body);
    else if (!view.hotCodeReplace) holdUndeclaredEvent(event);
  }

  /** On a stop: threads → the stopped thread → its top frames → the top frame. */
  async function refreshPaused(id: string) {
    const token = generation(id);
    try {
      const body = await debugRequest<{ threads?: DapThread[] }>(id, "threads");
      if (generation(id) !== token) return;
      const threads = Array.isArray(body?.threads) ? body.threads : [];
      const view = find(id);
      if (!view) return;
      const kept = threads.some((thread) => thread.id === view.selectedThreadId) ? view.selectedThreadId : undefined;
      const threadId = view.stoppedThreadId ?? kept ?? threads[0]?.id;
      update(id, (current) => ({ ...current, threads, selectedThreadId: threadId }));
      if (threadId !== undefined) await loadFrames(id, threadId, token);
    } catch (error) {
      if (generation(id) === token) setError(id, error);
    }
  }

  /** Reads the thread's top frames and selects `preferredFrameId` when listed, else the top frame. */
  async function loadFrames(id: string, threadId: number, token: number, preferredFrameId?: number) {
    const body = await debugRequest<{ stackFrames?: DapStackFrame[]; totalFrames?: unknown }>(id, "stackTrace", {
      threadId,
      startFrame: 0,
      levels: STACK_LEVELS,
    });
    if (generation(id) !== token) return;
    const frames = Array.isArray(body?.stackFrames) ? body.stackFrames : [];
    const framesComplete = stackTraceComplete(frames.length, STACK_LEVELS, body?.totalFrames);
    const selectedFrameId = frames.some((frame) => frame.id === preferredFrameId) ? preferredFrameId : frames[0]?.id;
    update(id, (view) => ({ ...view, frames, framesComplete, selectedFrameId, lastError: undefined }));
  }

  function replayBuffered(id: string) {
    const events = buffered.get(id);
    buffered.delete(id);
    for (const event of events ?? []) applyEvent(event);
  }

  // ---- launch ----

  /** A failed launch as the user sees it (a message key, or debugErrorText); branching code reads the raw error. */
  function failureText(error: unknown): string {
    const message = errorMessage(error);
    return te(message) ? t(message as Parameters<typeof t>[0]) : debugErrorText(error);
  }

  /** The project's launch in flight, if any (any configuration). */
  function launchFor(root: string): { configId: string; noDebug: boolean } | null {
    return launching.value.find((item) => sameProjectRoot(item.root, root)) ?? null;
  }

  /** The launches in flight (all projects), for the debug panel. */
  const launches = computed(() => launching.value);

  function updateLaunch(seq: number, progress: DebugLaunchProgress) {
    launching.value = launching.value.map((item) => {
      if (item.seq !== seq) return item;
      const next = { ...item, progressAt: Date.now() };
      if (progress.languageService !== undefined) next.languageService = progress.languageService;
      if (progress.step) {
        next.step = progress.step;
        if (progress.step === "languageService") next.languageService = true;
        // A status text belongs to the language service step only.
        if (progress.step !== "languageService") next.status = "";
      }
      if (progress.status !== undefined && next.step === "languageService") next.status = progress.status;
      if (progress.confirming !== undefined) next.confirming = progress.confirming;
      return next;
    });
  }

  /** Cancels the launch seq (see DebugLaunchOptions.signal): it ends as declined, without an error. */
  function cancelLaunch(seq: number) {
    const controller = launchControllers.get(seq);
    if (!controller) return;
    controller.abort();
    // A step that cannot be interrupted (prepare, the settings sync, the guard) runs on: say so until it returns.
    launching.value = launching.value.map((item) => (item.seq === seq && !item.cancelling ? { ...item, cancelling: true } : item));
  }

  /** A debug panel starts (true) or stops (false) showing launch seq with its Cancel. */
  function setLaunchShown(seq: number, shown: boolean) {
    if (!launchControllers.has(seq)) return;
    const count = Math.max(0, (shownLaunches.value[seq] ?? 0) + (shown ? 1 : -1));
    const next = { ...shownLaunches.value };
    if (count > 0) next[seq] = count;
    else delete next[seq];
    shownLaunches.value = next;
  }

  async function launch(root: string, cfg: RunConfiguration, opts: DebugLaunchOptions, replaceId?: string): Promise<string | null> {
    if (launching.value.some((item) => item.configId === cfg.id && sameProjectRoot(item.root, root))) return null;
    const seq = (launchSeq += 1);
    const controller = new AbortController();
    const outer = opts.signal;
    const forward = () => controller.abort();
    if (outer?.aborted) controller.abort();
    else outer?.addEventListener("abort", forward, { once: true });
    launchControllers.set(seq, controller);
    launching.value = [
      ...launching.value,
      { seq, root, configId: cfg.id, configName: cfg.name, noDebug: opts.noDebug, step: "checking", languageService: false, status: "", progressAt: Date.now(), confirming: false, cancelling: false },
    ];
    try {
      const outerProgress = opts.onProgress;
      return await launchNow(root, cfg, {
        ...opts,
        signal: controller.signal,
        cancelInPanel: () => (shownLaunches.value[seq] ?? 0) > 0,
        onProgress: (progress) => {
          updateLaunch(seq, progress);
          outerProgress?.(progress);
        },
      }, replaceId);
    } finally {
      outer?.removeEventListener("abort", forward);
      launchControllers.delete(seq);
      if (seq in shownLaunches.value) {
        const rest = { ...shownLaunches.value };
        delete rest[seq];
        shownLaunches.value = rest;
      }
      launching.value = launching.value.filter((item) => item.seq !== seq);
    }
  }

  async function launchNow(root: string, cfg: RunConfiguration, opts: DebugLaunchOptions, replaceId?: string): Promise<string | null> {
    subscribe();
    let result;
    try {
      result = await launchDebugSession(root, cfg, opts);
    } catch (error) {
      ElMessage.error({ message: t("debug.session.startFailed", { error: failureText(error) }), showClose: true, grouping: true });
      return null;
    }
    if (!result) return null;
    const id = result.sessionId;
    const view = newSessionView({
      id,
      projectRoot: root,
      configId: cfg.id,
      configName: cfg.name,
      type: cfg.type,
      noDebug: opts.noDebug,
      capabilities: result.capabilities ?? {},
      startedAt: Date.now(),
      pluginId: cfg.pluginId,
      ...(result.hotCodeReplace ? { hotCodeReplace: result.hotCodeReplace } : {}),
    });
    // The host sent the start request's method breakpoints only with the
    // capability; their results (a buffered event) land on these names.
    if (!opts.noDebug && view.capabilities.supportsFunctionBreakpoints === true) {
      view.functionBreakpoints = result.functionBreakpointNames.map((name) => ({ name, verified: false }));
    }
    // Likewise the saved field breakpoints.
    if (!opts.noDebug && view.capabilities.supportsDataBreakpoints === true) {
      view.dataBreakpoints = result.dataBreakpoints.map((bp) => ({ ...bp, verified: false }));
    }
    const replaceIndex = replaceId === undefined ? -1 : sessions.value.findIndex((s) => s.id === replaceId);
    if (replaceIndex >= 0) {
      sessions.value.splice(replaceIndex, 1, view);
      forget(replaceId!);
    } else {
      sessions.value.push(view);
    }
    sessionOrigins.set(id, { projectRoot: root, type: cfg.type });
    activeFiles.set(id, opts.activeFile);
    if (result.release) holds.set(id, result.release);
    // The listing failed at launch: read it again before deciding the session has no hot code replace.
    if (!opts.noDebug && !result.debuggerListed) {
      undeclared.set(id, []);
      void resolveHotCodeReplace(id, cfg.pluginId, cfg.type);
    }
    selectedSessionId.value = id;
    replayBuffered(id);
    if (!opts.noDebug) {
      // Read before the defaults are saved: only a change made during the start is sent again below.
      const savedFilters = configs.stateFor(root).exceptionFilters[cfg.type];
      void applyDefaultExceptionFilters(root, cfg.type, id, view.capabilities);
      // Breakpoints changed while the start was pending missed the start snapshot
      // and the sync (the session was not listed yet): send those files now.
      const listed = find(id);
      if (listed && listed.state !== "ended") {
        for (const at of configs.breakpointsChangedSince(root, result.breakpointRevision)) {
          if (breakpointAppliesTo(at, listed.type)) void syncSession(listed, at);
        }
        // Likewise a method breakpoint list edited meanwhile.
        const sent = result.functionBreakpointNames;
        const current = enabledFunctionBreakpointNames(configs.stateFor(root).functionBreakpoints[cfg.type]);
        if (current.length !== sent.length || current.some((name, index) => name !== sent[index])) void syncSessionFunctionBreakpoints(listed);
        // And the saved field breakpoints.
        if (!sameDataBreakpointLists(sessionDataBreakpointList(configs.stateFor(root).dataBreakpoints[cfg.type], []), result.dataBreakpoints)) {
          void syncSessionDataBreakpoints(listed);
        }
        // And the exception filter switches (unsaved ones get the adapter's defaults above).
        const filters = savedFilters;
        if (filters !== undefined && (filters.length !== result.exceptionFilters.length || filters.some((filter, index) => filter !== result.exceptionFilters[index]))) {
          debugRequest(id, "setExceptionBreakpoints", { filters: [...filters] }).catch((error: unknown) => setError(id, error));
        }
      }
    }
    if (result.skippedBreakpoints.length > 0) {
      ElMessage.warning({ message: t("debug.session.skippedBreakpoints", { count: result.skippedBreakpoints.length }), showClose: true });
    }
    return id;
  }

  /**
   * A debugger type without saved exception filters gets the adapter's
   * defaults: sent to the new session (it started with none) and saved, so
   * the breakpoints dialog shows what the adapter actually uses.
   */
  async function applyDefaultExceptionFilters(root: string, type: string, id: string, capabilities: DapCapabilities) {
    const offered = capabilities.exceptionBreakpointFilters;
    if (!Array.isArray(offered) || offered.length === 0) return;
    if (configs.stateFor(root).exceptionFilters[type] !== undefined) return;
    const defaults = offered.filter((filter) => filter.default === true).map((filter) => filter.filter);
    configs.setExceptionFilters(root, type, defaults);
    if (defaults.length === 0) return;
    try {
      await debugRequest(id, "setExceptionBreakpoints", { filters: [...defaults] });
    } catch (error) {
      setError(id, error);
    }
  }

  /**
   * Starts cfg and selects the new session. Resolves to its id, or null when
   * the user declined, the start failed (the failure is shown) or the same
   * configuration of the project is already being launched.
   */
  function start(root: string, cfg: RunConfiguration, opts: DebugLaunchOptions, replaceId?: string): Promise<string | null> {
    return launch(root, cfg, opts, replaceId);
  }

  /**
   * Restart of sessions whose configuration is transient (never saved), by
   * configuration id prefix: e.g. test runs (`test-run:`) start the same
   * tests again (services/test-run-launcher.ts). The handler gets the
   * stopped session and returns the new session id, or null.
   */
  const restartHandlers = new Map<string, (session: DebugSessionView) => Promise<string | null>>();

  function registerRestartHandler(configIdPrefix: string, handler: (session: DebugSessionView) => Promise<string | null>) {
    restartHandlers.set(configIdPrefix, handler);
  }

  /** Stops a session; launched programs end, attached ones keep running (host default). */
  async function stop(id: string) {
    await stopSession(id);
  }

  /** Stops a session; resolves to the failure (also set as lastError), or undefined. */
  async function stopSession(id: string): Promise<unknown> {
    const view = find(id);
    if (!view || view.state === "ended") return undefined;
    try {
      await stopDebugSession(id);
      return undefined;
    } catch (error) {
      setError(id, error);
      return error;
    }
  }

  /**
   * Stops the session and starts its configuration again (same noDebug and
   * active file) in the same tab. A configuration deleted meanwhile is
   * reported and nothing is started; so is a failed stop, unless the
   * session was already gone (`not_found:`). A transient configuration
   * (never saved, e.g. a test run) restarts through its registered handler.
   */
  async function restart(id: string): Promise<string | null> {
    const view = find(id);
    if (!view) return null;
    const failure = await stopSession(id);
    // `not_found:` means the session is already gone; any other failure may
    // leave it running, so the restart stops here (the error stays on the tab).
    if (failure !== undefined && !errorMessage(failure).startsWith("not_found:")) return null;
    for (const [prefix, handler] of restartHandlers) {
      if (view.configId.startsWith(prefix)) return handler(view);
    }
    await configs.load();
    const cfg = configs.stateFor(view.projectRoot).configs.find((item) => item.id === view.configId);
    if (!cfg) {
      ElMessage.warning({ message: t("debug.session.configMissing", { name: view.configName }), showClose: true });
      return null;
    }
    return launch(view.projectRoot, cfg, { noDebug: view.noDebug, activeFile: activeFiles.get(id) }, id);
  }

  /**
   * The project and debugger type of every session listed since the app
   * started, kept after it is closed (a few strings per session): a
   * debugger source tab it opened (its path names the session) still knows
   * where its breakpoints belong.
   */
  const sessionOrigins = new Map<string, { projectRoot: string; type: string }>();

  /** The project and debugger type of a session listed earlier (even if closed since), or undefined. */
  function sessionOrigin(id: string): { projectRoot: string; type: string } | undefined {
    const view = find(id);
    return view ? { projectRoot: view.projectRoot, type: view.type } : sessionOrigins.get(id);
  }

  function forget(id: string) {
    releaseHold(id);
    retired.add(id);
    generations.delete(id);
    activeFiles.delete(id);
    buffered.delete(id);
    runToCursorFiles.delete(id);
    variableExpansions.delete(id);
    functionList.syncs.delete(id);
    dataList.syncs.delete(id);
    replacing.delete(id);
    replaceAgain.delete(id);
    undeclared.delete(id);
    if (id in variablesVersions.value) {
      const rest = { ...variablesVersions.value };
      delete rest[id];
      variablesVersions.value = rest;
    }
    mutedSessionIds.value = mutedSessionIds.value.filter((item) => item !== id);
  }

  /** Removes a session's tab, stopping it first when it still runs. */
  function close(id: string) {
    const index = sessions.value.findIndex((s) => s.id === id);
    if (index < 0) return;
    void stop(id);
    sessions.value.splice(index, 1);
    forget(id);
    if (selectedSessionId.value === id) {
      selectedSessionId.value = sessions.value[Math.min(index, sessions.value.length - 1)]?.id ?? null;
    }
  }

  /**
   * Closes (stopping first when still running) every session whose project
   * is none of roots: a project removed from every workspace has no panel
   * left to show or stop its sessions. Returns how many were closed.
   */
  function closeSessionsOutside(roots: readonly string[]): number {
    const orphaned = sessions.value.filter((view) => !roots.some((root) => sameProjectRoot(root, view.projectRoot)));
    for (const view of orphaned) close(view.id);
    return orphaned.length;
  }

  /** Whether a session of root is still running. */
  function hasLiveSession(root: string): boolean {
    return sessions.value.some((view) => view.state !== "ended" && sameProjectRoot(view.projectRoot, root));
  }

  function selectSession(id: string | null) {
    if (id === null || find(id)) selectedSessionId.value = id;
  }

  // ---- execution control ----

  /** The thread a command acts on; asks the adapter when none is known yet. */
  async function threadIdFor(id: string): Promise<number> {
    const view = find(id);
    const known = view?.selectedThreadId ?? view?.stoppedThreadId ?? view?.threads[0]?.id;
    if (known !== undefined) return known;
    const body = await debugRequest<{ threads?: DapThread[] }>(id, "threads");
    const threads = Array.isArray(body?.threads) ? body.threads : [];
    if (threads.length === 0) throw new Error("unavailable: the debuggee has no threads");
    update(id, (current) => ({ ...current, threads }));
    return threads[0]!.id;
  }

  /**
   * Sends a command that resumes a paused session. The view shows running at
   * once (a fast step's `stopped` can arrive before the response); a failed
   * command restores the paused view unless an event changed it meanwhile.
   */
  async function resumeWith(id: string, command: string, args: Record<string, unknown> = {}): Promise<boolean> {
    const before = find(id);
    if (!before || before.state !== "paused") return false;
    try {
      const threadId = await threadIdFor(id);
      const paused = find(id);
      if (!paused || paused.state !== "paused") return false;
      update(id, (view) => reduceDebugEvent(view, { sessionId: id, event: "continued" }));
      // Continue or a step: a hot code replace outcome is stale now (an adapter's own
      // continued, e.g. while it drops a changed frame, keeps it).
      clearHotCodeReplaceInfo(id);
      const token = bump(id);
      try {
        await debugRequest(id, command, { threadId, ...args });
        clearError(id);
        return true;
      } catch (error) {
        if (generation(id) === token) {
          update(id, () => paused);
          bump(id);
        }
        setError(id, error);
        return false;
      }
    } catch (error) {
      setError(id, error);
      return false;
    }
  }

  function resume(id: string) {
    return resumeWith(id, "continue").then(() => undefined);
  }

  function stepOver(id: string) {
    return resumeWith(id, "next").then(() => undefined);
  }

  function stepInto(id: string) {
    return resumeWith(id, "stepIn").then(() => undefined);
  }

  function stepOut(id: string) {
    return resumeWith(id, "stepOut").then(() => undefined);
  }

  /**
   * The frame Smart Step Into works on: the top frame of the shown thread
   * (the thread stepIn acts on), whichever frame is selected; null unless
   * paused with frames and the adapter declares supportsStepInTargetsRequest.
   */
  function stepInTargetsFrame(view: DebugSessionView | undefined): DapStackFrame | null {
    if (!view || view.state !== "paused" || view.noDebug || view.capabilities.supportsStepInTargetsRequest !== true) return null;
    return view.frames[0] ?? null;
  }

  /**
   * DAP stepInTargets of the top frame: the calls a stepIn there can enter,
   * in source order (by line, then column; targets without a position keep
   * their order after the others). Optional detail, so a failure, an invalid
   * answer, a session that is not paused or an adapter without the capability
   * yields [] (and no session error); invalid entries are dropped.
   */
  async function stepInTargets(id: string): Promise<DapStepInTarget[]> {
    const frame = stepInTargetsFrame(find(id));
    if (!frame) return [];
    let body: unknown;
    try {
      body = await debugRequest<unknown>(id, "stepInTargets", { frameId: frame.id });
    } catch (error) {
      // eslint-disable-next-line no-console -- Smart Step Into falls back to a plain step in; keep the reason diagnosable (no dialog).
      console.warn(`debug: stepInTargets failed for session ${id}, stepping in as usual`, error);
      return [];
    }
    if (!isRecord(body) || !Array.isArray(body.targets)) return [];
    const targets: DapStepInTarget[] = [];
    for (const item of body.targets) {
      if (!isRecord(item) || typeof item.id !== "number" || typeof item.label !== "string") continue;
      const target: DapStepInTarget = { id: item.id, label: item.label };
      if (typeof item.line === "number") target.line = item.line;
      if (typeof item.column === "number") target.column = item.column;
      targets.push(target);
    }
    return sortedStepInTargets(targets);
  }

  /**
   * `stepIn { threadId, targetId }` (resumeWith's thread and state handling).
   * False without the capability, when not paused, or when the step failed.
   */
  function stepIntoTarget(id: string, targetId: number): Promise<boolean> {
    const view = find(id);
    if (!view || view.noDebug || view.capabilities.supportsStepInTargetsRequest !== true) return Promise.resolve(false);
    return resumeWith(id, "stepIn", { targetId });
  }

  /**
   * Smart Step Into: no target steps in as usual, one target steps into it,
   * several become the open choice (stepTargetsPick) for the session's tool
   * window. An answer that comes after the session ran on or another frame
   * was selected is dropped.
   */
  async function smartStepInto(id: string): Promise<void> {
    const frameId = stepInTargetsFrame(find(id))?.id;
    if (frameId === undefined || smartStepping.has(id)) return;
    const token = generation(id);
    smartStepping.add(id);
    let targets: DapStepInTarget[];
    try {
      targets = await stepInTargets(id);
    } finally {
      smartStepping.delete(id);
    }
    if (stepInTargetsFrame(find(id))?.id !== frameId || generation(id) !== token) return;
    if (targets.length === 0) {
      await stepInto(id);
    } else if (targets.length === 1) {
      await stepIntoTarget(id, targets[0]!.id);
    } else {
      stepTargetsPick.value = { sessionId: id, frameId, targets };
      stepTargetsGeneration = token;
    }
  }

  /** The open Smart Step Into choice of session id, null once the session ran on or another thread is shown. */
  function stepTargetsPickFor(id: string): DebugStepTargetsPick | null {
    const pick = stepTargetsPick.value;
    if (!pick || pick.sessionId !== id) return null;
    if (stepInTargetsFrame(find(id))?.id !== pick.frameId || generation(id) !== stepTargetsGeneration) return null;
    return pick;
  }

  /** Closes the choice of session id; with a targetId of it, steps into that target. */
  async function chooseStepTarget(id: string, targetId: number | null): Promise<void> {
    const pick = stepTargetsPickFor(id);
    if (stepTargetsPick.value?.sessionId === id) stepTargetsPick.value = null;
    if (!pick || targetId === null || !pick.targets.some((target) => target.id === targetId)) return;
    await stepIntoTarget(id, targetId);
  }

  async function pause(id: string) {
    const view = find(id);
    if (!view || view.state !== "running") return;
    try {
      await debugRequest(id, "pause", { threadId: await threadIdFor(id) });
      clearError(id);
    } catch (error) {
      setError(id, error);
    }
  }

  /**
   * Runs to line of absPath: a temporary breakpoint there, continue, and the
   * file's own breakpoints back on the next stop or failure. (DAP `goto` is
   * "set next statement": it skips the code in between, so it is not used.)
   */
  async function runToCursor(id: string, absPath: string, line: number) {
    const view = find(id);
    if (!view || view.state !== "paused" || view.noDebug) return;
    const stored = isMuted(id) ? [] : enabledCopies(configs.breakpointsFor(view.projectRoot, absPath));
    // Reaching the cursor requires an unconditional stop, even when the saved
    // target is a logpoint or has a condition/hit count. Restore it on stop.
    const withTarget = [...stored.filter((bp) => bp.line !== line), { line, enabled: true }]
      .sort((a, b) => a.line - b.line);
    try {
      await setSessionBreakpoints(id, absPath, withTarget);
    } catch (error) {
      setError(id, error);
      return;
    }
    runToCursorFiles.set(id, absPath);
    if (!(await resumeWith(id, "continue"))) await restoreRunToCursor(id);
  }

  async function restoreRunToCursor(id: string) {
    const file = runToCursorFiles.get(id);
    const view = find(id);
    runToCursorFiles.delete(id);
    if (file === undefined || !view || view.state === "ended") return;
    await syncSession(view, file);
  }

  // ---- inspection ----

  function selectThread(id: string, threadId: number) {
    const view = find(id);
    if (!view) return;
    const token = bump(id);
    update(id, (current) => ({ ...current, selectedThreadId: threadId, frames: [], framesComplete: undefined, selectedFrameId: undefined }));
    if (view.state !== "paused") return;
    loadFrames(id, threadId, token).catch((error: unknown) => {
      if (generation(id) === token) setError(id, error);
    });
  }

  function selectFrame(id: string, frameId: number) {
    if (find(id)?.frames.some((frame) => frame.id === frameId)) update(id, (view) => ({ ...view, selectedFrameId: frameId }));
  }

  /** The scopes of a frame; a failure is reported as lastError and yields []. */
  async function loadScopes(id: string, frameId: number): Promise<DapScope[]> {
    try {
      const body = await debugRequest<{ scopes?: DapScope[] }>(id, "scopes", { frameId });
      return Array.isArray(body?.scopes) ? body.scopes : [];
    } catch (error) {
      setError(id, error);
      return [];
    }
  }

  /** The children of a variablesReference; a failure is reported as lastError and yields []. */
  async function loadVariables(id: string, variablesReference: number): Promise<DapVariable[]> {
    try {
      const body = await debugRequest<{ variables?: DapVariable[] }>(id, "variables", { variablesReference });
      return Array.isArray(body?.variables) ? body.variables : [];
    } catch (error) {
      setError(id, error);
      return [];
    }
  }

  /**
   * The text of a source without a local file (a JDK or dependency class, code
   * an adapter holds in memory), from the DAP `source` request. java-debug
   * answers with the attached source or a decompiled class.
   */
  async function loadSource(id: string, source: DapSource): Promise<string> {
    const body = await debugRequest<{ content?: unknown }>(id, "source", { source, sourceReference: source.sourceReference ?? 0 });
    if (!isRecord(body) || typeof body.content !== "string") throw new Error("unavailable: source returned no content");
    return body.content;
  }

  /** Evaluates in the selected frame. Rejects with the adapter's message (e.g. a NameError). */
  async function evaluate(id: string, expression: string, context: DebugEvaluateContext): Promise<DapEvaluateResult> {
    const frameId = find(id)?.selectedFrameId;
    const body = await debugRequest<DapEvaluateResult>(id, "evaluate", {
      expression,
      context,
      ...(frameId === undefined ? {} : { frameId }),
    });
    if (!isRecord(body) || typeof body.result !== "string") throw new Error("unavailable: evaluate returned an invalid result");
    return { ...body, variablesReference: typeof body.variablesReference === "number" ? body.variablesReference : 0 };
  }

  /**
   * DAP completions of `text` at `column` (1-based, UTF-16 code units; on
   * `line`, 1-based, of a multi-line text) in the selected frame. Optional
   * detail, so a failure, an invalid answer, a session that is not paused or
   * an adapter without supportsCompletionsRequest yields [] (and no session
   * error); invalid items are dropped. An answer that comes after the
   * session ran on or another frame was selected yields [] too.
   */
  async function completions(id: string, text: string, column: number, line?: number): Promise<DapCompletionItem[]> {
    const view = find(id);
    if (!view || view.state !== "paused" || view.noDebug || view.capabilities.supportsCompletionsRequest !== true) return [];
    const frameId = view.selectedFrameId;
    const token = generation(id);
    let body: unknown;
    try {
      body = await debugRequest<unknown>(id, "completions", {
        text,
        column,
        ...(line === undefined ? {} : { line }),
        ...(frameId === undefined ? {} : { frameId }),
      });
    } catch {
      return [];
    }
    const now = find(id);
    if (generation(id) !== token || now?.state !== "paused" || now.selectedFrameId !== frameId) return [];
    if (!isRecord(body) || !Array.isArray(body.targets)) return [];
    const items: DapCompletionItem[] = [];
    for (const target of body.targets) {
      if (!isRecord(target) || typeof target.label !== "string" || target.label === "") continue;
      const item: DapCompletionItem = { label: target.label };
      if (typeof target.text === "string") item.text = target.text;
      if (typeof target.type === "string") item.type = target.type;
      if (typeof target.start === "number") item.start = target.start;
      if (typeof target.length === "number") item.length = target.length;
      items.push(item);
    }
    return items;
  }

  /**
   * DAP exceptionInfo: the exception threadId stopped on. Optional detail, so
   * a failure or an invalid answer yields null and is not a session error.
   */
  async function loadExceptionInfo(id: string, threadId: number): Promise<DapExceptionInfo | null> {
    try {
      const body = await debugRequest<unknown>(id, "exceptionInfo", { threadId });
      if (!isRecord(body) || typeof body.exceptionId !== "string" || body.exceptionId === "") return null;
      const info: DapExceptionInfo = { exceptionId: body.exceptionId };
      if (typeof body.description === "string" && body.description !== "") info.description = body.description;
      if (typeof body.breakMode === "string") info.breakMode = body.breakMode;
      if (isRecord(body.details)) {
        const details: NonNullable<DapExceptionInfo["details"]> = {};
        for (const key of ["message", "typeName", "stackTrace"] as const) {
          const value = body.details[key];
          if (typeof value === "string" && value !== "") details[key] = value;
        }
        info.details = details;
      }
      return info;
    } catch {
      return null;
    }
  }

  /**
   * What the Variables pane remembers per method of session id (not
   * reactive; the pane keeps its own ref). A session that ended or is not
   * listed gets an empty map that is not kept.
   */
  function variableExpansionFor(id: string): Map<string, DebugFrameExpansion> {
    const view = find(id);
    if (!view || view.state === "ended") return new Map();
    let frames = variableExpansions.get(id);
    if (!frames) {
      frames = new Map();
      variableExpansions.set(id, frames);
    }
    return frames;
  }

  /** Whether the store keeps Variables pane entries for session id (tests and diagnostics). */
  function hasVariableExpansion(id: string): boolean {
    return variableExpansions.has(id);
  }

  /** Bumped by `bumpVariables`: values shown elsewhere (inline values) are read again when it changes. */
  function variablesVersion(id: string): number {
    return variablesVersions.value[id] ?? 0;
  }

  /** Tells the views showing the session's variables to read them again. */
  function bumpVariables(id: string) {
    variablesVersions.value = { ...variablesVersions.value, [id]: variablesVersion(id) + 1 };
  }

  /**
   * DAP setVariable: gives the variable `name` of the container
   * `variablesReference` a new value (the text the user typed), then
   * signals `bumpVariables`. Rejects with the adapter's message (e.g. a
   * value that does not fit the type); nothing is bumped then.
   */
  async function setVariable(
    id: string,
    variablesReference: number,
    name: string,
    value: string
  ): Promise<{ value: string; type?: string; variablesReference: number }> {
    const body = await debugRequest<unknown>(id, "setVariable", { variablesReference, name, value });
    if (!isRecord(body) || typeof body.value !== "string") throw new Error("unavailable: setVariable returned an invalid result");
    bumpVariables(id);
    return {
      value: body.value,
      ...(typeof body.type === "string" ? { type: body.type } : {}),
      variablesReference: typeof body.variablesReference === "number" ? body.variablesReference : 0,
    };
  }

  /**
   * DAP restartFrame: drops the frames above frameId and runs frameId again
   * from its start. Sent only while paused, when the adapter declares
   * supportsRestartFrame and frameId is a shown frame without
   * `canRestart: false` that is not the bottom frame of the whole stack (it
   * has no caller); the adapter's stopped event that follows refreshes the
   * frames, and a hot code replace outcome is stale then. Rejects with the
   * adapter's message (the session stays as it was, no lastError).
   */
  async function restartFrame(id: string, frameId: number): Promise<void> {
    const view = find(id);
    if (!view || view.state !== "paused" || view.noDebug || !view.capabilities.supportsRestartFrame) return;
    // A frame of an older stop (the list was refreshed meanwhile) is not sent.
    const frame = view.frames.find((item) => item.id === frameId);
    if (!frame || frame.canRestart === false || isBottomFrame(view, frameId)) return;
    await debugRequest(id, "restartFrame", { frameId });
    clearHotCodeReplaceInfo(id);
  }

  // ---- hot code replace (the debugger's declared extension, see domain/debug/hot-code-replace.ts) ----

  /** Sessions whose declared request is in flight. */
  const replacing = reactive(new Set<string>());
  /** Sessions rebuilt again while their request was in flight: sent once more when it is answered. */
  const replaceAgain = new Set<string>();
  /**
   * Sessions whose debugger listing failed at launch, so whether the debugger
   * declares hot code replace is not known yet: their non-output events wait
   * here (at most MAX_UNDECLARED_EVENTS) until the listing is read again.
   */
  const undeclared = new Map<string, DebugEvent[]>();

  /**
   * Reads the debugger listing again for a session launched without it and
   * takes the debugger's declaration; the events that came meanwhile are
   * handled once it is known. A second failure leaves the session without
   * hot code replace.
   */
  async function resolveHotCodeReplace(id: string, pluginId: string, type: string) {
    let declared: DebuggerHotCodeReplace | undefined;
    try {
      declared = (await listDebuggers()).find((item) => item.pluginId === pluginId && item.type === type)?.hotCodeReplace;
    } catch {
      declared = undefined;
    }
    const waiting = undeclared.get(id) ?? [];
    undeclared.delete(id);
    if (!declared || !find(id)) return;
    update(id, (view) => ({ ...view, hotCodeReplace: { ...declared } }));
    for (const event of waiting) if (event.event === declared.event) void handleHotCodeReplaceEvent(id, event.body);
  }

  function holdUndeclaredEvent(event: DebugEvent) {
    const waiting = undeclared.get(event.sessionId);
    if (!waiting || event.event === "output") return;
    waiting.push(event);
    if (waiting.length > MAX_UNDECLARED_EVENTS) waiting.shift();
  }

  /** The declaration of a live debug session (not a run without debugging), else undefined. */
  function liveHotCodeReplace(view: DebugSessionView | undefined): DebuggerHotCodeReplace | undefined {
    return view && !view.noDebug && view.state !== "ended" ? view.hotCodeReplace : undefined;
  }

  /**
   * The mode in the setting the debugger declared (modeSetting, checked by
   * the host at install), read when a build comes so a change applies at
   * once; auto without a declared setting or when it cannot be read.
   */
  async function hotCodeReplaceModeFor(view: DebugSessionView): Promise<HotCodeReplaceMode> {
    const key = view.hotCodeReplace?.modeSetting;
    if (!view.pluginId || !key) return "auto";
    try {
      const { getPluginConfiguration } = await import("@/services/plugin-configuration-service");
      const state = await getPluginConfiguration(view.pluginId);
      return hotCodeReplaceMode(state.effective[key]);
    } catch {
      return "auto";
    }
  }

  function setHotCodeReplaceMessage(id: string, message: DebugSessionView["hotCodeReplaceMessage"]) {
    update(id, (view) => ({ ...view, hotCodeReplaceMessage: message }));
  }

  /** Hides the hot code replace message of a session. */
  function dismissHotCodeReplaceMessage(id: string) {
    if (find(id)?.hotCodeReplaceMessage) setHotCodeReplaceMessage(id, undefined);
  }

  /** The program runs on: an outcome (info) is stale now; warnings and errors stay until dismissed or replaced. */
  function clearHotCodeReplaceInfo(id: string) {
    if (find(id)?.hotCodeReplaceMessage?.kind === "info") setHotCodeReplaceMessage(id, undefined);
  }

  /**
   * The declared event: a warning or an error shows the adapter's text (the
   * session goes on); a finished build is applied at once (auto), waits for
   * 「应用代码更改」 (manual) or is ignored (never).
   */
  async function handleHotCodeReplaceEvent(id: string, body: unknown) {
    const event = parseHotCodeReplaceEvent(body);
    if (!event || !liveHotCodeReplace(find(id))) return;
    if (event.kind === "warning" || event.kind === "error") {
      if (event.message) setHotCodeReplaceMessage(id, { kind: event.kind, text: event.message });
      return;
    }
    if (event.kind !== "buildComplete") return;
    const mode = await hotCodeReplaceModeFor(find(id)!);
    if (mode === "never" || !liveHotCodeReplace(find(id))) return;
    if (mode === "manual") {
      update(id, (view) => ({ ...view, pendingCodeChanges: true }));
      return;
    }
    await applyCodeChanges(id);
  }

  /**
   * Sends the debugger's declared hot code replace request and reports the
   * outcome on the session: 「已热替换 N 个类」 (with the drop-frame hint when
   * the adapter can restart frames and the shown top frame runs in a replaced
   * type and has a caller: not the bottom frame of the whole stack, as when
   * paused in main), the adapter's reason when it replaced nothing or not everything,
   * or the failure (the changes stay pending). While paused, after any
   * replaced type, the frames (the selected one kept) and variables are read
   * again, unless the adapter moved the program meanwhile (it may drop a
   * changed frame itself; its stopped event refreshes then). Resolves to the
   * replaced type names ([] when nothing was sent or replaced).
   */
  async function applyCodeChanges(id: string): Promise<{ changed: string[] }> {
    const declared = liveHotCodeReplace(find(id));
    if (!declared) return { changed: [] };
    if (replacing.has(id)) {
      replaceAgain.add(id);
      return { changed: [] };
    }
    replacing.add(id);
    update(id, (view) => ({ ...view, pendingCodeChanges: false }));
    const token = generation(id);
    let result: HotCodeReplaceResult;
    try {
      result = parseHotCodeReplaceResult(await debugRequest<unknown>(id, declared.request));
    } catch (error) {
      replacing.delete(id);
      replaceAgain.delete(id);
      if (find(id)?.state !== "ended") {
        update(id, (view) => ({
          ...view,
          pendingCodeChanges: true,
          hotCodeReplaceMessage: { kind: "error", text: t("debug.hotCodeReplace.failed", { error: debugErrorText(error) }) },
        }));
      }
      return { changed: [] };
    }
    try {
      await reportCodeChanges(id, result, token);
    } finally {
      replacing.delete(id);
    }
    if (replaceAgain.delete(id)) void applyCodeChanges(id);
    return { changed: result.changed };
  }

  /** Reads the paused frames again, keeping the selected frame when the adapter still lists it. */
  async function reloadPausedFrames(id: string, token: number) {
    const view = find(id);
    const threadId = view?.selectedThreadId ?? view?.stoppedThreadId;
    if (!view || view.state !== "paused" || generation(id) !== token || threadId === undefined) return;
    try {
      // Selected in one update: the variables are not read for the top frame first.
      await loadFrames(id, threadId, token, view.selectedFrameId);
    } catch (error) {
      if (generation(id) === token) setError(id, error);
      return;
    }
    if (generation(id) !== token) return;
    bumpVariables(id);
  }

  async function reportCodeChanges(id: string, result: HotCodeReplaceResult, token: number) {
    if (!find(id) || find(id)!.state === "ended") return;
    const count = result.changed.length;
    if (count > 0) await reloadPausedFrames(id, token);
    if (result.error !== undefined) {
      // A partial replace still reports what was replaced.
      const text = count > 0 ? t("debug.hotCodeReplace.partial", { count, error: result.error }) : result.error;
      setHotCodeReplaceMessage(id, { kind: "error", text });
      return;
    }
    if (count === 0) {
      setHotCodeReplaceMessage(id, { kind: "info", text: t("debug.hotCodeReplace.nothing") });
      return;
    }
    const current = find(id);
    const top = current?.frames[0];
    const dropHint =
      current?.state === "paused" &&
      generation(id) === token &&
      current.capabilities.supportsRestartFrame === true &&
      top !== undefined &&
      top.canRestart !== false &&
      !isBottomFrame(current, top.id) &&
      frameInChangedTypes(top, result.changed);
    const key =
      count === 1
        ? dropHint
          ? "debug.hotCodeReplace.replacedOneDropFrame"
          : "debug.hotCodeReplace.replacedOne"
        : dropHint
          ? "debug.hotCodeReplace.replacedDropFrame"
          : "debug.hotCodeReplace.replaced";
    setHotCodeReplaceMessage(id, { kind: "info", text: t(key, { count }) });
  }

  /** Whether 「应用代码更改」 can be clicked for the session: rebuilt code is pending and no request is in flight. */
  function canApplyCodeChanges(id: string): boolean {
    const view = find(id);
    return Boolean(liveHotCodeReplace(view) && view!.pendingCodeChanges && !replacing.has(id));
  }

  // ---- breakpoints ----

  function setVerified(id: string, at: BreakpointAt, results: DebugBreakpointResult[]) {
    const address = breakpointAddress(at);
    const body = address === undefined ? { path: at, breakpoints: results } : { path: address, address: true, breakpoints: results };
    update(id, (view) => reduceDebugEvent(view, { sessionId: id, event: DEBUG_BREAKPOINTS_SET_EVENT, body }));
  }

  // A setBreakpoints-style request replaces the adapter's whole list, so two in
  // flight for the same target may land in either order and leave an older
  // list behind. Each target therefore syncs one request at a time; changes
  // made meanwhile are sent once more, read afresh when that request starts.
  const breakpointSyncs = new Map<string, { again: boolean; done: Promise<void> }>();
  function serializedSync(key: string, run: () => Promise<void>): Promise<void> {
    const pending = breakpointSyncs.get(key);
    if (pending) {
      pending.again = true;
      return pending.done;
    }
    const entry = { again: false, done: Promise.resolve() };
    entry.done = (async () => {
      try {
        do {
          entry.again = false;
          await run();
        } while (entry.again);
      } finally {
        breakpointSyncs.delete(key);
      }
    })();
    breakpointSyncs.set(key, entry);
    return entry.done;
  }

  function syncSession(view: DebugSessionView, at: BreakpointAt) {
    return serializedSync(`${view.id}\u0000${breakpointAtKey(at)}`, async () => {
      const list = isMuted(view.id) ? [] : enabledCopies(configs.breakpointsFor(view.projectRoot, at));
      const address = breakpointAddress(at);
      try {
        const response =
          address === undefined ? await setSessionBreakpoints(view.id, at as string, list) : await setSessionAddressBreakpoints(view.id, address, list);
        setVerified(view.id, at, breakpointResults(list.map((bp) => bp.line), response?.breakpoints));
      } catch (error) {
        setError(view.id, error);
      }
    });
  }

  /**
   * Sends the current breakpoints of a file or debugger address to every
   * running debug (not noDebug) session of the project (an address: of its
   * debugger type only).
   */
  async function syncBreakpointsForFile(root: string, at: BreakpointAt) {
    const targets = sessions.value.filter(
      (view) => view.state !== "ended" && !view.noDebug && sameProjectRoot(view.projectRoot, root) && breakpointAppliesTo(at, view.type),
    );
    await Promise.all(targets.map((view) => syncSession(view, at)));
  }

  function isMuted(id: string): boolean {
    return mutedSessionIds.value.includes(id);
  }

  /**
   * Mutes or unmutes a session's breakpoints: every file and debugger address
   * (of the session's type) with stored breakpoints is sent again (empty
   * while muted). Session-scoped, not saved.
   */
  async function setBreakpointsMuted(id: string, muted: boolean) {
    const view = find(id);
    if (!view || isMuted(id) === muted) return;
    mutedSessionIds.value = muted ? [...mutedSessionIds.value, id] : mutedSessionIds.value.filter((item) => item !== id);
    if (view.state === "ended" || view.noDebug) return;
    const state = configs.stateFor(view.projectRoot);
    const targets: BreakpointAt[] = [
      ...Object.keys(state.breakpoints).map((key) => toSourcePath(view.projectRoot, key)),
      ...Object.keys(state.addressBreakpoints[view.type] ?? {}).map((address) => ({ address, type: view.type })),
    ];
    await Promise.all([...targets.map((at) => syncSession(view, at)), syncSessionFunctionBreakpoints(view), syncSessionDataBreakpoints(view)]);
  }

  /**
   * Sends a path-less breakpoint list of the session (method or field
   * breakpoints) when its adapter declares the capability, and records the
   * adapter's answers on what was sent (a short answer leaves the rest
   * unverified). A failed request leaves every item unverified with its
   * message; it is no session error, since a wrong name or id must not get
   * in the way. Only the latest request's answer per session and list is kept.
   */
  function syncBreakpointList<T extends DebugBreakpointListItemResult>(view: DebugSessionView, list: BreakpointListSync<T>) {
    if (view.state === "ended" || view.noDebug || view.capabilities[list.capability] !== true) return Promise.resolve();
    return serializedSync(`${view.id}\u0000list\u0000${list.command}`, () => sendBreakpointList(view, list));
  }

  async function sendBreakpointList<T extends DebugBreakpointListItemResult>(view: DebugSessionView, list: BreakpointListSync<T>) {
    const sent = list.items(view);
    const seq = (list.syncs.get(view.id) ?? 0) + 1;
    list.syncs.set(view.id, seq);
    let results: T[];
    try {
      const body = await debugRequest<unknown>(view.id, list.command, { breakpoints: sent.map(list.payload) });
      results = pairedListResults<T>(sent, isRecord(body) ? body.breakpoints : undefined);
    } catch (error) {
      results = pairedListResults<T>(sent, undefined, debugErrorText(error));
    }
    if (list.syncs.get(view.id) !== seq) return;
    update(view.id, (current) => list.store(current, results));
  }

  /** Sends a breakpoint list to the project's sessions of a debugger type (see syncBreakpointList). */
  async function syncBreakpointListOfType<T extends DebugBreakpointListItemResult>(root: string, type: string, list: BreakpointListSync<T>) {
    const targets = sessions.value.filter((view) => view.type === type && sameProjectRoot(view.projectRoot, root));
    await Promise.all(targets.map((view) => syncBreakpointList(view, list)));
  }

  /** Method breakpoints: the saved enabled names of the session's type (none while muted), with setFunctionBreakpoints. */
  const functionList: BreakpointListSync<DebugFunctionBreakpointResult> = {
    capability: "supportsFunctionBreakpoints",
    command: "setFunctionBreakpoints",
    syncs: new Map(),
    items: (view) =>
      isMuted(view.id) ? [] : enabledFunctionBreakpointNames(configs.stateFor(view.projectRoot).functionBreakpoints[view.type]).map((name) => ({ name })),
    payload: ({ name }) => ({ name }),
    store: (view, functionBreakpoints) => ({ ...view, functionBreakpoints }),
  };

  function syncSessionFunctionBreakpoints(view: DebugSessionView) {
    return syncBreakpointList(view, functionList);
  }

  /** After the project's method breakpoints of a debugger type changed: sends them to its running sessions of that type. */
  function syncFunctionBreakpoints(root: string, type: string) {
    return syncBreakpointListOfType(root, type, functionList);
  }

  /**
   * Name → status of the method breakpoints of a debugger type across the
   * project's running debug sessions: verified when any session verified
   * it, else the first reason a session gave. Names no session was sent are absent.
   */
  function functionBreakpointStatusFor(root: string, type: string): Record<string, DebugFunctionBreakpointStatus> {
    const status: Record<string, DebugFunctionBreakpointStatus> = {};
    for (const view of sessions.value) {
      if (view.state === "ended" || view.noDebug || view.type !== type || !sameProjectRoot(view.projectRoot, root)) continue;
      for (const result of view.functionBreakpoints ?? []) status[result.name] = mergedStatus(status[result.name], result);
    }
    return status;
  }

  // ---- field (data) breakpoints ----

  /** The session is paused and its adapter declares supportsDataBreakpoints: field breakpoints can be set from its variables. */
  function canSetDataBreakpoints(view: DebugSessionView | undefined): view is DebugSessionView {
    return view !== undefined && view.state === "paused" && !view.noDebug && view.capabilities.supportsDataBreakpoints === true;
  }

  /** Every field breakpoint the session should have: the saved ones of its type, then its own. */
  function currentDataBreakpoints(view: DebugSessionView): StoredDataBreakpoint[] {
    return sessionDataBreakpointList(configs.stateFor(view.projectRoot).dataBreakpoints[view.type], view.sessionDataBreakpoints);
  }

  /**
   * DAP dataBreakpointInfo for the variable `name` of the container
   * `variablesReference`: whether (and how) a field breakpoint can be set on
   * it. Asked only while paused with supportsDataBreakpoints (references
   * belong to the stop). Optional detail, so a failure, no answer (an adapter
   * may answer a variable it cannot watch without a body), an answer after the
   * program ran on or an id the host would refuse reads as dataId null (and
   * is no session error).
   */
  async function dataBreakpointInfo(id: string, variablesReference: number, name: string): Promise<DataBreakpointInfo> {
    const none: DataBreakpointInfo = { dataId: null, description: "" };
    if (!canSetDataBreakpoints(find(id)) || !(variablesReference > 0)) return none;
    const token = generation(id);
    let body: unknown;
    try {
      body = await debugRequest<unknown>(id, "dataBreakpointInfo", { variablesReference, name });
    } catch {
      return none;
    }
    if (generation(id) !== token || !canSetDataBreakpoints(find(id))) return none;
    return parseDataBreakpointInfo(body);
  }

  /** Whether the session has the field breakpoint (saved for its type or its own). */
  function hasDataBreakpoint(id: string, dataId: string, accessType: DataBreakpointAccessType): boolean {
    const view = find(id);
    return view !== undefined && currentDataBreakpoints(view).some((bp) => sameDataBreakpoint(bp, { dataId, accessType }));
  }

  /**
   * Sets the field breakpoint `info` describes with accessType on the paused
   * session, or removes it when it is already set (wherever it is kept). One
   * the adapter declares `canPersist` is saved for the project and debugger
   * type and sent to the project's sessions of that type; any other one is
   * kept by this session only and ends with it. Each session gets all its
   * field breakpoints at once (setDataBreakpoints). More than the host's
   * limit is refused with a warning.
   */
  async function toggleDataBreakpoint(id: string, info: DataBreakpointInfo, accessType: DataBreakpointAccessType): Promise<void> {
    const view = find(id);
    if (!canSetDataBreakpoints(view) || !isValidDataId(info.dataId)) return;
    const breakpoint: StoredDataBreakpoint = { dataId: info.dataId, description: info.description || info.dataId, accessType };
    const saved = configs.stateFor(view.projectRoot).dataBreakpoints[view.type] ?? [];
    const own = view.sessionDataBreakpoints ?? [];
    const set = currentDataBreakpoints(view).some((bp) => sameDataBreakpoint(bp, breakpoint));
    if (!set && currentDataBreakpoints(view).length >= DATA_BREAKPOINT_LIMITS.max) {
      ElMessage.warning({ message: t("debug.variables.watchTooMany", { max: DATA_BREAKPOINT_LIMITS.max }), grouping: true });
      return;
    }
    const savedChanges = set ? saved.some((bp) => sameDataBreakpoint(bp, breakpoint)) : info.canPersist === true;
    const ownChanges = set ? own.some((bp) => sameDataBreakpoint(bp, breakpoint)) : info.canPersist !== true;
    if (savedChanges) configs.setDataBreakpoints(view.projectRoot, view.type, toggledDataBreakpoints(saved, breakpoint));
    if (ownChanges) update(id, (current) => ({ ...current, sessionDataBreakpoints: toggledDataBreakpoints(own, breakpoint) }));
    if (savedChanges) await syncDataBreakpoints(view.projectRoot, view.type);
    else await syncSessionDataBreakpoints(find(id)!);
  }

  /**
   * Removes a field breakpoint of a debugger type (breakpoints dialog): from
   * the saved list and from the project's sessions of that type that keep
   * it, then re-sends their lists.
   */
  async function removeDataBreakpoint(root: string, type: string, breakpoint: Pick<StoredDataBreakpoint, "dataId" | "accessType">) {
    const saved = configs.stateFor(root).dataBreakpoints[type] ?? [];
    if (saved.some((bp) => sameDataBreakpoint(bp, breakpoint))) {
      configs.setDataBreakpoints(root, type, saved.filter((bp) => !sameDataBreakpoint(bp, breakpoint)));
    }
    for (const view of sessions.value) {
      if (view.type !== type || !sameProjectRoot(view.projectRoot, root) || !view.sessionDataBreakpoints?.some((bp) => sameDataBreakpoint(bp, breakpoint))) continue;
      update(view.id, (current) => ({ ...current, sessionDataBreakpoints: (current.sessionDataBreakpoints ?? []).filter((bp) => !sameDataBreakpoint(bp, breakpoint)) }));
    }
    await syncDataBreakpoints(root, type);
  }

  /** Field breakpoints: every one of the session (none while muted), with setDataBreakpoints. */
  const dataList: BreakpointListSync<DebugDataBreakpointResult> = {
    capability: "supportsDataBreakpoints",
    command: "setDataBreakpoints",
    syncs: new Map(),
    items: (view) => (isMuted(view.id) ? [] : currentDataBreakpoints(view).map(({ dataId, description, accessType }) => ({ dataId, description, accessType }))),
    payload: ({ dataId, accessType }) => ({ dataId, accessType }),
    store: (view, dataBreakpoints) => ({ ...view, dataBreakpoints }),
  };

  function syncSessionDataBreakpoints(view: DebugSessionView) {
    return syncBreakpointList(view, dataList);
  }

  /** After the project's saved field breakpoints of a debugger type changed: sends them to its running sessions of that type. */
  function syncDataBreakpoints(root: string, type: string) {
    return syncBreakpointListOfType(root, type, dataList);
  }

  /**
   * The project's field breakpoints for the breakpoints dialog: the saved
   * ones (by type), then those of its running sessions that are not saved.
   * A session's own ones are gone once it ends. The status reads the
   * project's running debug sessions: verified when any verified it, else
   * the first reason given.
   */
  function dataBreakpointEntries(root: string): DebugDataBreakpointEntry[] {
    const running = sessions.value.filter((view) => view.state !== "ended" && !view.noDebug && sameProjectRoot(view.projectRoot, root));
    const entries: DebugDataBreakpointEntry[] = [];
    const add = (type: string, bp: StoredDataBreakpoint, sessionOnly: boolean) => {
      if (entries.some((entry) => entry.type === type && sameDataBreakpoint(entry, bp))) return;
      const entry: DebugDataBreakpointEntry = { dataId: bp.dataId, description: bp.description, accessType: bp.accessType, type, sessionOnly };
      for (const view of running) {
        const result = view.type === type ? view.dataBreakpoints?.find((item) => sameDataBreakpoint(item, bp)) : undefined;
        if (result) entry.status = mergedStatus(entry.status, result);
      }
      entries.push(entry);
    };
    for (const [type, list] of Object.entries(configs.stateFor(root).dataBreakpoints)) for (const bp of list) add(type, bp, false);
    for (const view of running) for (const bp of view.sessionDataBreakpoints ?? []) add(view.type, bp, true);
    return entries;
  }

  /** Sends the enabled exception filters to the project's running debug sessions of that debugger type. */
  async function applyExceptionFilters(root: string, type: string, filters: string[]) {
    const targets = sessions.value.filter(
      (view) => view.state !== "ended" && !view.noDebug && view.type === type && sameProjectRoot(view.projectRoot, root),
    );
    await Promise.all(
      targets.map(async (view) => {
        try {
          await debugRequest(view.id, "setExceptionBreakpoints", { filters: [...filters] });
        } catch (error) {
          setError(view.id, error);
        }
      }),
    );
  }

  /**
   * After the project's exception type list of a debugger changed: when a
   * debug session of that type runs in the project, sends the list to the
   * debugger's settingsAction once (its settings belong to the debugger's
   * process for the project, e.g. jdtls), then sets each running session's
   * exception breakpoints again (the current switches) so it applies them.
   * Measured with java-debug 0.53.1 (machine_smoke_java_idea_test.go): a
   * running session keeps its exception breakpoints after
   * vscode.java.updateDebugSettings unless the settings carry
   * exceptionFiltersUpdated (not sent), and re-reads exceptionTypes on its
   * next setExceptionBreakpoints. A failed sync is a warning; the list stays
   * saved and the next launch sends it.
   */
  async function syncExceptionTypes(root: string, debugger_: Pick<DebuggerInfo, "pluginId" | "type" | "hasSettings">) {
    if (!debugger_.hasSettings) return;
    const running = () =>
      sessions.value.filter(
        (view) => view.state !== "ended" && !view.noDebug && view.type === debugger_.type && sameProjectRoot(view.projectRoot, root),
      );
    if (running().length === 0) return;
    if (!(await syncDebuggerSettings(root, debugger_))) return;
    await Promise.all(
      running().map(async (view) => {
        const saved = configs.stateFor(root).exceptionFilters[debugger_.type];
        const filters = saved ?? (view.capabilities.exceptionBreakpointFilters ?? []).filter((filter) => filter.default).map((filter) => filter.filter);
        try {
          await debugRequest(view.id, "setExceptionBreakpoints", { filters: [...filters] });
        } catch (error) {
          setError(view.id, error);
        }
      }),
    );
  }

  /**
   * Line → verified for a file or debugger address across the project's
   * running debug sessions (verified when any session verified it). Lines
   * without a result are absent.
   */
  function verifiedLinesFor(root: string, at: BreakpointAt): Record<number, boolean> {
    const lines: Record<number, boolean> = {};
    for (const result of activeResultsFor(root, at)) lines[result.line] = (lines[result.line] ?? false) || result.verified;
    return lines;
  }

  /**
   * Stored line → what the gutter hover adds for the breakpoints of a file or address: the
   * adapter's reason when no session verified the line, and the line an
   * adapter actually stops on when it moved the breakpoint. Lines with
   * neither are absent; the first session's detail wins.
   */
  function breakpointDetailsFor(root: string, at: BreakpointAt): Record<number, DebugBreakpointHoverDetails> {
    const verifiedLines = verifiedLinesFor(root, at);
    const details: Record<number, DebugBreakpointHoverDetails> = {};
    for (const result of activeResultsFor(root, at)) {
      const message = result.message && verifiedLines[result.line] === false ? result.message : undefined;
      if (message === undefined && result.actualLine === undefined) continue;
      const entry = (details[result.line] ??= {});
      if (message !== undefined) entry.message ??= message;
      if (result.actualLine !== undefined) entry.actualLine ??= result.actualLine;
    }
    return details;
  }

  /** Breakpoint results of a file or debugger address across the project's running debug (not noDebug) sessions. */
  function activeResultsFor(root: string, at: BreakpointAt): DebugBreakpointResult[] {
    const live = sessions.value.filter((view) => view.state !== "ended" && !view.noDebug && sameProjectRoot(view.projectRoot, root));
    const address = breakpointAddress(at);
    if (address !== undefined) return live.filter((view) => breakpointAppliesTo(at, view.type)).flatMap((view) => view.addressVerified[address] ?? []);
    const caseInsensitive = pathsAreCaseInsensitive();
    const key = toRelativeKey(root, at as string, caseInsensitive);
    if (key === null) return [];
    const same = (other: string | null) =>
      other !== null && (caseInsensitive ? other.toLowerCase() === key.toLowerCase() : other === key);
    const found: DebugBreakpointResult[] = [];
    for (const view of live) {
      for (const [path, results] of Object.entries(view.verified)) {
        if (same(toRelativeKey(root, path, caseInsensitive))) found.push(...results);
      }
    }
    return found;
  }

  subscribe();

  // A project no workspace lists any more (removed everywhere, or its
  // workspace permanently deleted) has no debug panel left to show or stop
  // its sessions. Watched here, not in a view: project views are cached per
  // workspace and may be evicted, and this store loads with the debugger UI,
  // so the entry bundle does not grow.
  watch(
    () => (sessions.value.length && projects.hydrated ? projects.allListedRoots() : null),
    (roots) => {
      // An empty list (the last project removed) closes every session.
      if (roots) closeSessionsOutside(roots);
    }
  );

  return {
    sessions,
    selectedSessionId,
    selectedSession,
    isActive,
    isPaused,
    launchFor,
    launches,
    cancelLaunch,
    setLaunchShown,
    start,
    registerRestartHandler,
    stop,
    restart,
    close,
    closeSessionsOutside,
    hasLiveSession,
    selectSession,
    resume,
    pause,
    stepOver,
    stepInto,
    stepOut,
    runToCursor,
    selectThread,
    selectFrame,
    loadScopes,
    loadVariables,
    loadSource,
    evaluate,
    completions,
    loadExceptionInfo,
    variableExpansionFor,
    hasVariableExpansion,
    variablesVersion,
    bumpVariables,
    setVariable,
    restartFrame,
    applyCodeChanges,
    canApplyCodeChanges,
    dismissHotCodeReplaceMessage,
    stepTargetsPick,
    stepInTargets,
    stepIntoTarget,
    smartStepInto,
    stepTargetsPickFor,
    chooseStepTarget,
    syncBreakpointsForFile,
    sessionOrigin,
    verifiedLinesFor,
    breakpointDetailsFor,
    mutedSessionIds,
    isMuted,
    setBreakpointsMuted,
    applyExceptionFilters,
    syncExceptionTypes,
    syncFunctionBreakpoints,
    functionBreakpointStatusFor,
    canSetDataBreakpoints,
    dataBreakpointInfo,
    hasDataBreakpoint,
    toggleDataBreakpoint,
    removeDataBreakpoint,
    syncDataBreakpoints,
    dataBreakpointEntries,
  };
});

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useDebugSessionsStore, import.meta.hot));
}
