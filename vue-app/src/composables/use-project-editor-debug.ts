/**
 * Debugging in the project workspace's text editor: the props the editor
 * renders (breakpoints, verified lines, paused line, entry points), its
 * breakpoint events, the condition editor and the entry-point run / debug
 * menu, opening the paused frame's file, the paused frame's variables for
 * inline values, and opening the Evaluate dialog.
 *
 * Breakpoints work the same in a project file and in a read-only
 * debugger-provided source whose adapter gave it an address (saved under the
 * address, domain/debug/address-breakpoints.ts); a source known only by its
 * sourceReference cannot hold one, and a click there says so once.
 *
 * Test run buttons: a project file's tests discovered by the workspace's
 * streaming test providers (testProviders with debugRun; stores/test-discovery.ts)
 * show as entry ▶ glyphs on their lines with the same Run / Debug menu, plus
 * the test's last result on the glyph (stores/test-runs.ts). Discovery runs
 * when a file of a project without a discovery is opened and again after a
 * save (debounced, only when the saved file can hold tests), never per
 * keystroke. A discovery that failed or found nothing (the provider's
 * language service may still be starting) is retried with backoff while a
 * file of that project is open, and when another of its files is activated
 * once the backoff passed (stores/test-discovery.ts retryIn; bounded).
 */
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { ElMessage } from "element-plus";
import { DEBUGGER_ADDRESS_LIMITS, breakpointAddress, type BreakpointAt } from "@/domain/debug/address-breakpoints";
import { normalizeLogMessage } from "@/domain/debug/logpoints";
import { newConfigurationFromEntryPoint } from "@/domain/debug/configurations";
import {
  isVirtualDebugSource,
  isVirtualSourceDocumentPath,
  virtualSourceDocumentInfo,
  virtualSourceDocumentPath,
} from "@/domain/debug/virtual-source";
import { useVueI18n } from "@/i18n/messages";
import { readJdtClassFileContents } from "@/services/lsp-service";
import type { DapSource, DebuggerEntryPoint, DebuggerInfo, SourceBreakpoint } from "@/domain/debug/types";
import { debuggerEntryPoints, listDebuggers } from "@/services/debug-service";
import { isDesktopRuntime } from "@/services/desktop-bridge";
import { pathsAreCaseInsensitive, useDebugConfigsStore } from "@/stores/debug-configs";
import { useDebugSessionsStore } from "@/stores/debug-sessions";
import type {
  BreakpointMove,
  DebugAnchor,
  DebugBreakpointHoverDetails,
  DebugEntryPointMarker,
} from "@/composables/use-editor-debug-decorations";

const ENTRY_POINTS_DEBOUNCE_MS = 500;
export interface ProjectEditorDebugDocument {
  path: string;
  external?: boolean;
  loaded?: boolean;
}

export interface ProjectEditorEntryItem {
  key: string;
  label: string;
}

interface EntryCandidate {
  key: string;
  debugger: DebuggerInfo;
  entry: DebuggerEntryPoint;
}

export interface ProjectEditorConditionDraft {
  path: string;
  root: string;
  /** Where the breakpoint is saved: the file, or the source's debugger address. */
  at: BreakpointAt;
  line: number;
  anchor: DebugAnchor;
  condition: string;
  hitCondition: string;
  logMessage: string;
  /** A logpoint action or existing logpoint starts in the message field. */
  focusLogMessage: boolean;
}

export interface ProjectEditorEntryMenu {
  path: string;
  root: string;
  anchor: DebugAnchor;
  items: ProjectEditorEntryItem[];
}

export interface ProjectEditorDebugOptions {
  /** The document the project text editor shows, or null. */
  activeDocument: () => ProjectEditorDebugDocument | null;
  projectRootForPath: (path: string) => string;
  /** Monaco language id of an open document (from the editor). */
  languageIdFor: (path: string) => string | undefined;
  /** The text editor is mounted (`languageIdFor` can answer). */
  editorReady: () => boolean;
  /** The workspace view is the active one (paused frames open files only there). */
  active: () => boolean;
  /** This workspace has the project open. */
  ownsProject: (root: string) => boolean;
  /** Opens path at line through the workspace's document flow. */
  openAt: (path: string, line: number) => void;
  /**
   * Opens a read-only document for a paused frame's source that is not a
   * local file (path from virtualSourceDocumentPath), revealing line; load
   * reads its text from the debug adapter.
   */
  openVirtualSource: (path: string, line: number, load: () => Promise<string>) => void;
  /** The text editor's selection, or the expression at its cursor ("" without one). */
  evaluateText?: () => string;
  /** Paths of the open documents (an open source of a debugger address is reused). */
  openDocumentPaths?: () => readonly string[];
}

export interface ProjectEditorEvaluateRequest {
  prefill: string;
  /** Evaluate the prefill at once (hover "inspect"). */
  evaluate: boolean;
  /** Distinct per request, so the dialog remounts with the new prefill. */
  id: number;
}

function comparablePath(path: string): string {
  const slashed = path.replace(/\\/g, "/");
  return pathsAreCaseInsensitive() ? slashed.toLowerCase() : slashed;
}

/** The debug props of the project text editor. */
export interface ProjectEditorDebugProps {
  breakpoints: SourceBreakpoint[];
  verifiedLines: Record<number, boolean>;
  breakpointDetails: Record<number, DebugBreakpointHoverDetails>;
  pausedLine: number | null;
  entryPoints: DebugEntryPointMarker[];
  /** Last results of the test entries, by stored line. */
  inlineVariables: { name: string; value: string }[] | null;
}

/** Same keys with equal values (values compared as JSON: small plain records). */
function sameRecord<T>(a: Record<number, T>, b: Record<number, T>): boolean {
  const keys = Object.keys(a);
  if (keys.length !== Object.keys(b).length) return false;
  return keys.every((key) => {
    const left = a[Number(key)], right = b[Number(key)];
    return left === right || JSON.stringify(left) === JSON.stringify(right);
  });
}

export function useProjectEditorDebug(options: ProjectEditorDebugOptions) {
  const { t } = useVueI18n();
  // Restart of a test-run session works from the start, not only after this window launched one.
  const configs = useDebugConfigsStore();
  const sessions = useDebugSessionsStore();
  void configs.load().catch(() => undefined);

  /** Entry points and file debugging apply to project files (not external ones). */
  const fileTarget = computed(() => {
    const document = options.activeDocument();
    if (!document || document.external || isVirtualSourceDocumentPath(document.path)) return null;
    return { path: document.path, root: options.projectRootForPath(document.path) };
  });

  /**
   * The active document that can hold breakpoints: a project file, or a
   * debugger source with an address, whose breakpoints belong to the project
   * and debugger type of the session that opened it (kept after that session
   * closed). A debugger source of a session this window never listed has no
   * target (never the project its path happens to fall in).
   */
  const target = computed<{ path: string; root: string; at: BreakpointAt } | null>(() => {
    const file = fileTarget.value;
    if (file) return { ...file, at: file.path };
    const path = options.activeDocument()?.path;
    const info = path ? virtualSourceDocumentInfo(path) : null;
    const origin = info?.address ? sessions.sessionOrigin(info.sessionId) : undefined;
    if (!path || !info?.address || !origin) return null;
    return { path, root: origin.projectRoot, at: { address: info.address, type: origin.type } };
  });

  const breakpoints = computed<SourceBreakpoint[]>(() =>
    target.value ? configs.breakpointsFor(target.value.root, target.value.at) : []
  );
  // Every session event (each `output` batch too) replaces the session view,
  // which re-runs these; the previous object is kept when nothing changed so
  // the editor props, the decorations and the workspace view are not redone
  // for program output.
  const verifiedLines = computed<Record<number, boolean>>((previous) => {
    const next = target.value ? sessions.verifiedLinesFor(target.value.root, target.value.at) : {};
    return previous && sameRecord(previous, next) ? previous : next;
  });
  const breakpointDetails = computed<Record<number, DebugBreakpointHoverDetails>>((previous) => {
    const next = target.value ? sessions.breakpointDetailsFor(target.value.root, target.value.at) : {};
    return previous && sameRecord(previous, next) ? previous : next;
  });

  /** Debugger source documents already told that they cannot hold breakpoints (until their tab closes). */
  const toldNoAddress = new Set<string>();
  if (options.openDocumentPaths) {
    const openPaths = options.openDocumentPaths;
    watch(
      () => openPaths(),
      (paths) => {
        for (const path of toldNoAddress) if (!paths.includes(path)) toldNoAddress.delete(path);
      },
    );
  }

  /**
   * The breakpoint target of a gutter action, or null. A debugger source
   * without an address (only a sourceReference, or one the host would
   * refuse) or of an unknown session gets one hint per document instead.
   */
  function actionTarget() {
    const current = target.value;
    if (current) return current;
    const path = options.activeDocument()?.path;
    const info = path ? virtualSourceDocumentInfo(path) : null;
    if (path && info && !toldNoAddress.has(path)) {
      toldNoAddress.add(path);
      const key = info.address ? "debug.addressBreakpoints.noSession" : "debug.addressBreakpoints.noAddress";
      ElMessage.info({ message: t(key), showClose: true, grouping: true });
    }
    return null;
  }

  /**
   * Whether the store applied a change at `at`; a refused one at a debugger
   * address (the only refusal a gutter target can meet: a new address past
   * the limit) is reported.
   */
  function changed(applied: boolean, at: BreakpointAt) {
    if (!applied && breakpointAddress(at) !== undefined) {
      ElMessage.warning({ message: t("debug.addressBreakpoints.tooMany", { max: DEBUGGER_ADDRESS_LIMITS.maxAddresses }), showClose: true, grouping: true });
    }
    return applied;
  }

  /** The selected session's selected frame while it is paused. */
  // A frame in a class outside the project (a JDK or dependency class:
  // java-debug reports `jdt://…` with a sourceReference) has no file to open:
  // its path is a virtual document whose text comes from the DAP `source`
  // request (`source` is set then).
  const pausedFrame = computed(() => {
    const session = sessions.selectedSession;
    if (!session || session.state !== "paused" || session.selectedFrameId === undefined) return null;
    const frame = session.frames.find((item) => item.id === session.selectedFrameId);
    if (!frame || frame.line < 1) return null;
    const base = { sessionId: session.id, frameId: frame.id, root: session.projectRoot, line: frame.line };
    if (frame.source && isVirtualDebugSource(frame.source)) {
      return { ...base, path: virtualSourceDocumentPath(session.id, frame.source, frame.name), source: frame.source };
    }
    const path = frame.source?.path;
    return path ? { ...base, path, source: undefined } : null;
  });

  // ---- inline values ----

  /** The paused frame's first cheap scope (locals), for inline values; null when not paused. */
  const inlineVariables = ref<{ name: string; value: string }[] | null>(null);
  let inlineRequest = 0;

  async function loadInlineVariables(sessionId: string, frameId: number, request: number) {
    const scopes = await sessions.loadScopes(sessionId, frameId);
    const scope = scopes.find((item) => !item.expensive && item.variablesReference > 0);
    const variables = scope ? await sessions.loadVariables(sessionId, scope.variablesReference) : [];
    if (request !== inlineRequest) return;
    inlineVariables.value = variables.map((variable) => ({ name: variable.name, value: variable.value }));
  }

  // A new frame clears the values first; a changed variable (setVariable)
  // reads them again in place. The key is a string so session events that
  // change neither do not reload.
  let inlineFrame: string | null = null;
  watch(
    () => {
      const frame = pausedFrame.value;
      return frame ? `${frame.sessionId}\u0000${frame.frameId}\u0000${sessions.variablesVersion(frame.sessionId)}` : null;
    },
    () => {
      const request = (inlineRequest += 1);
      const frame = pausedFrame.value;
      const frameKey = frame ? `${frame.sessionId}\u0000${frame.frameId}` : null;
      if (frameKey !== inlineFrame) inlineVariables.value = null;
      inlineFrame = frameKey;
      if (frame) void loadInlineVariables(frame.sessionId, frame.frameId, request);
    },
    { immediate: true }
  );

  const pausedLine = computed<number | null>(() => {
    const frame = pausedFrame.value;
    const document = options.activeDocument();
    if (!frame || !document) return null;
    return comparablePath(frame.path) === comparablePath(document.path) ? frame.line : null;
  });

  // ---- entry points ----

  /** `markers` is built once per lookup: the editor treats a new array as a new lookup. */
  const entryState = ref<{ path: string; candidates: EntryCandidate[]; markers: DebugEntryPointMarker[] } | null>(null);
  const noEntryPoints: DebugEntryPointMarker[] = [];
  let entryTimer: ReturnType<typeof setTimeout> | undefined;
  let entryRequest = 0;

  const entryPoints = computed(() => {
    const state = entryState.value;
    return state && state.path === fileTarget.value?.path ? state.markers : noEntryPoints;
  });

  /** Looks the active file's entry points up again after the debounce. */
  function scheduleEntryPoints() {
    if (entryTimer !== undefined) clearTimeout(entryTimer);
    entryTimer = setTimeout(() => {
      entryTimer = undefined;
      void loadEntryPoints();
    }, ENTRY_POINTS_DEBOUNCE_MS);
  }

  /** Failures are silent: entry points are a convenience, the file still edits and runs. */
  async function loadEntryPoints() {
    const request = (entryRequest += 1);
    const current = fileTarget.value;
    const languageId = current ? options.languageIdFor(current.path) : undefined;
    if (!current || !languageId || !isDesktopRuntime()) {
      entryState.value = null;
      return;
    }
    let debuggers: DebuggerInfo[];
    try {
      debuggers = await listDebuggers();
    } catch {
      debuggers = [];
    }
    const matching = debuggers.filter((item) => item.hasEntryPoints && item.languages.includes(languageId));
    const results = await Promise.all(
      matching.map(async (debugger_) => {
        try {
          const entries = await debuggerEntryPoints({
            pluginId: debugger_.pluginId,
            type: debugger_.type,
            projectRoot: current.root,
            file: current.path,
            languageId,
          });
          return entries.map((entry, index) => ({ key: `${debugger_.pluginId}:${debugger_.type}:${index}`, debugger: debugger_, entry }));
        } catch {
          return [];
        }
      })
    );
    if (request !== entryRequest) return;
    const candidates = results.flat();
    entryState.value = {
      path: current.path,
      candidates,
      markers: candidates.map(({ entry }) => ({ line: entry.line, label: entry.label })),
    };
  }

  watch(
    () => {
      const document = options.activeDocument();
      return document && !document.external && document.loaded ? document.path : null;
    },
    (path) => {
      if (path) scheduleEntryPoints();
      else {
        entryRequest += 1;
        entryState.value = null;
      }
    },
    { immediate: true }
  );

  // The editor loads lazily: a document opened before it mounted is looked up once it is ready.
  watch(
    () => options.editorReady(),
    (ready) => {
      if (ready && fileTarget.value) scheduleEntryPoints();
    }
  );

  // ---- breakpoint events ----

  function sync(root: string, at: BreakpointAt) {
    void sessions.syncBreakpointsForFile(root, at);
  }

  function toggleBreakpoint(line: number) {
    const current = actionTarget();
    if (current && changed(configs.toggleBreakpoint(current.root, current.at, line), current.at)) sync(current.root, current.at);
  }

  function setBreakpointEnabled(line: number, enabled: boolean) {
    const current = actionTarget();
    const existing = breakpoints.value.find((bp) => bp.line === line);
    if (!current || !existing) return;
    if (changed(configs.setBreakpoint(current.root, current.at, { ...existing, enabled }), current.at)) sync(current.root, current.at);
  }

  function removeBreakpoint(line: number) {
    const current = actionTarget();
    if (current && configs.removeBreakpoint(current.root, current.at, line)) sync(current.root, current.at);
  }

  /** Line moves come from edits: only project files are edited. */
  function moveBreakpoints(moves: BreakpointMove[], path: string) {
    if (!moves.length) return;
    const root = options.projectRootForPath(path);
    configs.moveBreakpoints(root, path, moves);
    sync(root, path);
  }

  const conditionDraft = ref<ProjectEditorConditionDraft | null>(null);

  function editBreakpoint(line: number, anchor: DebugAnchor, intent?: "logpoint") {
    const current = actionTarget();
    if (!current) return;
    const existing = breakpoints.value.find((bp) => bp.line === line);
    conditionDraft.value = {
      ...current,
      line,
      anchor,
      condition: existing?.condition ?? "",
      hitCondition: existing?.hitCondition ?? "",
      logMessage: normalizeLogMessage(existing?.logMessage) ?? "",
      focusLogMessage: intent === "logpoint" || normalizeLogMessage(existing?.logMessage) !== undefined,
    };
  }

  /** Saves the draft; empty fields clear that condition (the breakpoint stays). */
  function saveCondition(values: { condition: string; hitCondition: string; logMessage: string }) {
    const draft = conditionDraft.value;
    conditionDraft.value = null;
    if (!draft) return;
    const existing = configs.breakpointsFor(draft.root, draft.at).find((bp) => bp.line === draft.line);
    const condition = values.condition.trim();
    const hitCondition = values.hitCondition.trim();
    const logMessage = normalizeLogMessage(values.logMessage);
    const applied = configs.setBreakpoint(draft.root, draft.at, {
      line: draft.line,
      enabled: existing?.enabled ?? true,
      ...(condition ? { condition } : {}),
      ...(hitCondition ? { hitCondition } : {}),
      ...(logMessage !== undefined ? { logMessage } : {}),
    });
    if (changed(applied, draft.at)) sync(draft.root, draft.at);
  }

  function cancelCondition() {
    conditionDraft.value = null;
  }

  // ---- entry-point menu ----

  const entryMenu = ref<ProjectEditorEntryMenu | null>(null);

  function openEntryMenu(line: number, anchor: DebugAnchor) {
    const current = fileTarget.value;
    const state = entryState.value;
    if (!current) return;
    const items = [
      ...(state && state.path === current.path ? state.candidates : [])
        .filter(({ entry }) => entry.line === line)
        .map(({ key, entry }) => ({ key, label: entry.label })),
    ];
    if (!items.length) return;
    entryMenu.value = { ...current, anchor, items };
  }

  function closeEntryMenu() {
    entryMenu.value = null;
  }

  /** Creates (or reuses) the entry's run configuration, selects it and starts it. */
  async function runEntry(key: string, noDebug: boolean) {
    const menu = entryMenu.value;
    entryMenu.value = null;
    const candidate = entryState.value?.candidates.find((item) => item.key === key);
    if (!menu || !candidate || entryState.value?.path !== menu.path) return;
    await configs.load().catch(() => undefined);
    const cfg = newConfigurationFromEntryPoint(candidate.debugger, candidate.entry, menu.path, configs.stateFor(menu.root).configs);
    configs.upsertConfig(menu.root, cfg);
    configs.selectConfig(menu.root, cfg.id);
    await sessions.start(menu.root, cfg, { noDebug, activeFile: menu.path });
  }

  // ---- paused frame and debugger sources ----

  /** Reads a debugger source's text: a `jdt://` class through the Java language service, else the adapter's `source` request. */
  function sourceLoader(root: string, sessionId: string, source: DapSource): () => Promise<string> {
    return () =>
      source.path?.startsWith("jdt://") ? readJdtClassFileContents(root, source.path) : sessions.loadSource(sessionId, source);
  }

  /**
   * Opens the source of a debugger address of root's project at line (the
   * breakpoints dialog's "go to"): an open document of the address is
   * revealed; otherwise a new one is opened for sessionId, named like the
   * session's frame of that source when it shows one.
   */
  function openAddress(root: string, sessionId: string, address: string, line: number) {
    const open = options.openDocumentPaths?.().find((path) => virtualSourceDocumentInfo(path)?.address === address);
    const frames = sessions.sessions.find((view) => view.id === sessionId)?.frames ?? [];
    const known = frames.find((frame) => frame.source?.path === address)?.source;
    const source: DapSource = known ? { ...known } : { path: address };
    options.openVirtualSource(open ?? virtualSourceDocumentPath(sessionId, source, ""), line, sourceLoader(root, sessionId, source));
  }

  watch(
    () => {
      const frame = pausedFrame.value;
      return frame ? `${frame.sessionId}\u0000${frame.frameId}\u0000${frame.path}\u0000${frame.line}` : null;
    },
    (key) => {
      const frame = pausedFrame.value;
      if (!key || !frame || !options.active() || !options.ownsProject(frame.root)) return;
      const source = frame.source;
      if (source) {
        options.openVirtualSource(frame.path, frame.line, sourceLoader(frame.root, frame.sessionId, source));
        return;
      }
      options.openAt(frame.path.replace(/\\/g, "/"), frame.line);
    }
  );

  // ---- evaluate ----

  const evaluateRequest = ref<ProjectEditorEvaluateRequest | null>(null);
  let evaluateSeq = 0;

  /**
   * Opens the Evaluate dialog with prefill, else the editor's selection (or
   * the expression at its cursor). `evaluate` evaluates it at once.
   */
  function openEvaluate(prefill?: string, opts: { evaluate?: boolean } = {}) {
    evaluateSeq += 1;
    const text = prefill ?? options.evaluateText?.() ?? "";
    evaluateRequest.value = { prefill: text, evaluate: Boolean(opts.evaluate) && text.trim() !== "", id: evaluateSeq };
  }

  function closeEvaluate() {
    evaluateRequest.value = null;
  }

  onBeforeUnmount(() => {
    if (entryTimer !== undefined) clearTimeout(entryTimer);
    entryRequest += 1;
    inlineRequest += 1;
  });

  /** The editor props of the debug feature. */
  const editorProps = computed<ProjectEditorDebugProps>((previous) => {
    const next: ProjectEditorDebugProps = {
      breakpoints: breakpoints.value,
      verifiedLines: verifiedLines.value,
      breakpointDetails: breakpointDetails.value,
      pausedLine: pausedLine.value,
      entryPoints: entryPoints.value,
      inlineVariables: pausedLine.value === null ? null : inlineVariables.value,
    };
    // Unchanged parts keep their identity, so an unchanged whole does too.
    return previous && (Object.keys(next) as (keyof typeof next)[]).every((key) => previous[key] === next[key]) ? previous : next;
  });

  return {
    editorProps,
    conditionDraft,
    entryMenu,
    /** Call when a project document was saved (entry points are looked up again). */
    refreshEntryPoints: () => {
      scheduleEntryPoints();
    },
    toggleBreakpoint,
    editBreakpoint,
    setBreakpointEnabled,
    removeBreakpoint,
    moveBreakpoints,
    saveCondition,
    cancelCondition,
    openEntryMenu,
    closeEntryMenu,
    openAddress,
    runEntry,
    evaluateRequest,
    openEvaluate,
    closeEvaluate,
  };
}
