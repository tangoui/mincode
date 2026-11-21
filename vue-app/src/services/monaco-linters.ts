/**
 * Monaco stage of the `linters` contribution point.
 *
 * For every file model whose language has a plugin linter selected by the
 * user, the linter runs on `open` (model attached/created, language changed,
 * or the selection changed), on `save` (onEditorSaved, after the file was
 * written) and, only when the user enabled "Lint while typing" for the
 * language and the linter declares it, on `type` (debounced by the linter's
 * `debounceMs`). Results are dropped when the model changed meanwhile, ranges
 * are clamped with `model.validateRange`, and markers are published under a
 * per-linter owner so they never mix with LSP or built-in markers. Markers are
 * cleared when the preference changes, the model is disposed or the plugin
 * goes away.
 *
 * Monaco is reached only through `registerMonacoContributionHook`, so
 * importing this module never loads it; the host call is lazy-imported.
 */
import {
  LINTER_LIMITS,
  linterMarkerOwner,
  linterRunsOn,
  type LintDiagnostic,
  type LinterLanguagePreference,
  type LinterTrigger,
  type PluginLinter,
} from "@/domain/plugins/linters";
import { utf8ByteLength } from "@/domain/plugins/formatters";
import { onEditorSaved, type EditorSaveContext } from "@/services/editor-save-participants";
import { registerMonacoContributionHook, type MonacoApi } from "@/services/monaco-contributions";
import { jsonSchemaTargetForPath } from "@/services/monaco-json-schemas";
import type { LintDocumentRequest } from "@/services/plugin-linter-service";
import type { LintOutcome } from "@/services/plugin-linter-queue";

import type { editor, IDisposable } from "@/services/monaco-runtime.js";

type TextModel = editor.ITextModel;

export interface LinterFailure {
  linter: PluginLinter;
  code: "too_large" | "timeout" | "unavailable" | "failed";
  detail: string;
}

/** What the Monaco stage needs from the app (the consumer wires the store and i18n). */
export interface MonacoLinterRuntime {
  /** The selected, available plugin linter for a language. */
  selected(language: string): PluginLinter | undefined;
  preference(language: string): LinterLanguagePreference | undefined;
  projectRoots(): readonly string[];
  /** The marker source, "<title> (<plugin name>)". */
  sourceLabel(linter: PluginLinter): string;
  report(failure: LinterFailure): void;
  /** A local project file of this language was opened (the project prompt hooks in here). */
  modelOpened?(language: string): void;
}

/** Sends documents to plugin linters (tests replace it; the default calls the host). */
export interface PluginLinterInvoker {
  lint(linter: PluginLinter, modelKey: string, request: LintDocumentRequest): Promise<LintOutcome>;
  cancelModel(modelKey: string): void;
}

type LinterService = typeof import("@/services/plugin-linter-service");
let service: LinterService | undefined;

const defaultInvoker: PluginLinterInvoker = {
  async lint(linter, modelKey, request) {
    // Loaded on first use: the linter service reaches the desktop bridge.
    service ??= await import("@/services/plugin-linter-service");
    return service.lintWithPlugin(linter, modelKey, request);
  },
  cancelModel(modelKey) {
    service?.cancelModelLints(modelKey);
  },
};

interface ModelState {
  model: TextModel;
  disposables: IDisposable[];
  timer?: ReturnType<typeof setTimeout>;
  /** Marker owner currently holding markers on this model. */
  owner?: string;
  /** The linter ref the model was last linted for (re-run "open" when it changes). */
  ref?: string;
  /** Failure codes already reported for this model (reset on success). */
  reported: Set<string>;
}

let invoker: PluginLinterInvoker = defaultInvoker;
let runtime: MonacoLinterRuntime | undefined;
let linters: readonly PluginLinter[] = [];
let active: MonacoApi | undefined;
let unregisterHook: (() => void) | undefined;
let attachment: IDisposable[] = [];
const models = new Map<string, ModelState>();

function modelKey(model: TextModel): string {
  return model.uri.toString();
}

function modelPath(model: TextModel): string | undefined {
  const uri = model.uri;
  if (uri.scheme === "file") return /^\/[A-Za-z]:\//.test(uri.path) ? uri.path.slice(1) : uri.path;
  if (uri.scheme === "app") return uri.path;
  return undefined;
}

const CLEAN_RELATIVE_PATH = /^(?!\.{1,2}(?:\/|$))(?!.*\/\.{1,2}(?:\/|$))[^:\\]+$/;

function documentFields(model: TextModel): Pick<LintDocumentRequest, "fileName" | "relativePath"> {
  const path = modelPath(model) ?? model.uri.path;
  const target = jsonSchemaTargetForPath(path, model.uri.scheme === "file" ? runtime?.projectRoots() ?? [] : []);
  const relativePath = target.relativePath && CLEAN_RELATIVE_PATH.test(target.relativePath) && !target.relativePath.includes("//")
    ? target.relativePath
    : undefined;
  return { fileName: target.basename || "untitled", ...(relativePath ? { relativePath } : {}) };
}

/** The available linter selected for this model's language, if any. */
function linterFor(model: TextModel): PluginLinter | undefined {
  const linter = runtime?.selected(model.getLanguageId());
  return linter && linters.some((candidate) => candidate.ref === linter.ref) ? linter : undefined;
}

function clearMarkers(state: ModelState) {
  if (state.owner && active && !state.model.isDisposed()) active.editor.setModelMarkers(state.model, state.owner, []);
  state.owner = undefined;
}

function severityOf(monaco: MonacoApi, severity: LintDiagnostic["severity"]) {
  switch (severity) {
    case "error":
      return monaco.MarkerSeverity.Error;
    case "warning":
      return monaco.MarkerSeverity.Warning;
    case "info":
      return monaco.MarkerSeverity.Info;
    default:
      return monaco.MarkerSeverity.Hint;
  }
}

function markersOf(monaco: MonacoApi, model: TextModel, linter: PluginLinter, diagnostics: readonly LintDiagnostic[]): editor.IMarkerData[] {
  const label = runtime?.sourceLabel(linter) ?? linter.title;
  return diagnostics.slice(0, LINTER_LIMITS.diagnosticsMax).map((diagnostic) => {
    const range = model.validateRange({
      startLineNumber: diagnostic.startLineNumber,
      startColumn: diagnostic.startColumn,
      endLineNumber: diagnostic.endLineNumber,
      endColumn: diagnostic.endColumn,
    });
    const marker: editor.IMarkerData = {
      severity: severityOf(monaco, diagnostic.severity),
      message: diagnostic.message,
      source: diagnostic.source ? `${label} · ${diagnostic.source}` : label,
      startLineNumber: range.startLineNumber,
      startColumn: range.startColumn,
      endLineNumber: range.endLineNumber,
      endColumn: range.endColumn,
    };
    if (diagnostic.code) marker.code = diagnostic.code;
    if (diagnostic.tags?.length) {
      marker.tags = diagnostic.tags.map((tag) => (tag === "unnecessary" ? monaco.MarkerTag.Unnecessary : monaco.MarkerTag.Deprecated));
    }
    return marker;
  });
}

function reportOnce(state: ModelState, failure: LinterFailure) {
  if (state.reported.has(failure.code)) return;
  state.reported.add(failure.code);
  runtime?.report(failure);
}

async function lintModel(state: ModelState, trigger: LinterTrigger): Promise<void> {
  const monaco = active;
  const model = state.model;
  if (!monaco || !runtime || model.isDisposed()) return;
  const linter = linterFor(model);
  if (!linter) {
    state.ref = undefined;
    clearMarkers(state);
    return;
  }
  state.ref = linter.ref;
  if (!linterRunsOn(linter, runtime.preference(model.getLanguageId()), trigger)) return;
  const text = model.getValue();
  if (utf8ByteLength(text) > LINTER_LIMITS.payloadMaxBytes - 4096) {
    clearMarkers(state);
    reportOnce(state, { linter, code: "too_large", detail: "" });
    return;
  }
  const version = model.getVersionId();
  const key = modelKey(model);
  let outcome: LintOutcome;
  try {
    outcome = await invoker.lint(linter, key, { languageId: model.getLanguageId(), ...documentFields(model), text, trigger });
  } catch (error) {
    outcome = { ok: false, error: { code: "unknown", detail: error instanceof Error ? error.message : "" } };
  }
  // Discard results for a changed or disposed model, a changed selection or a detached Monaco.
  if (active !== monaco || model.isDisposed() || model.getVersionId() !== version || models.get(key) !== state) return;
  if (linterFor(model)?.ref !== linter.ref) return;
  if (!outcome.ok) {
    const { code, detail } = outcome.error;
    if (code === "cancelled") return;
    if (code === "too_large") clearMarkers(state);
    const mapped: LinterFailure["code"] =
      code === "too_large" || code === "timeout" ? code : code === "not_found" || code === "unavailable" || code === "forbidden" ? "unavailable" : "failed";
    reportOnce(state, { linter, code: mapped, detail });
    return;
  }
  state.reported.clear();
  const owner = linterMarkerOwner(linter.ref);
  if (state.owner && state.owner !== owner) clearMarkers(state);
  monaco.editor.setModelMarkers(model, owner, markersOf(monaco, model, linter, outcome.result.diagnostics));
  state.owner = owner;
}

function scheduleType(state: ModelState) {
  if (state.timer) clearTimeout(state.timer);
  state.timer = undefined;
  const linter = linterFor(state.model);
  if (!linter || !runtime || !linterRunsOn(linter, runtime.preference(state.model.getLanguageId()), "type")) return;
  state.timer = setTimeout(() => {
    state.timer = undefined;
    void lintModel(state, "type");
  }, linter.debounceMs);
}

function untrack(key: string) {
  const state = models.get(key);
  if (!state) return;
  if (state.timer) clearTimeout(state.timer);
  for (const disposable of state.disposables) disposable.dispose();
  clearMarkers(state);
  models.delete(key);
  invoker.cancelModel(key);
}

function track(model: TextModel) {
  if (model.isDisposed() || modelPath(model) === undefined) return;
  const key = modelKey(model);
  if (models.has(key)) return;
  const state: ModelState = { model, disposables: [], reported: new Set() };
  state.disposables.push(
    model.onDidChangeContent(() => scheduleType(state)),
    model.onWillDispose(() => untrack(key)),
  );
  models.set(key, state);
  if (model.uri.scheme === "file") runtime?.modelOpened?.(model.getLanguageId());
  void lintModel(state, "open");
}

/** Re-evaluates every tracked model after the linters or preferences changed. */
function refresh() {
  for (const state of models.values()) {
    const linter = linterFor(state.model);
    if (!linter) {
      if (state.timer) clearTimeout(state.timer);
      state.timer = undefined;
      state.ref = undefined;
      clearMarkers(state);
      continue;
    }
    if (linter.ref !== state.ref) {
      clearMarkers(state);
      state.reported.clear();
      void lintModel(state, "open");
    } else if (state.timer && !linterRunsOn(linter, runtime?.preference(state.model.getLanguageId()), "type")) {
      clearTimeout(state.timer);
      state.timer = undefined;
    }
  }
}

function findState(path: string): ModelState | undefined {
  const normalized = path.replace(/\\/g, "/");
  const drive = /^[A-Za-z]:\//.test(normalized);
  const app = `/${normalized.replace(/^\/+/, "")}`;
  for (const state of models.values()) {
    const model = state.model;
    if (model.isDisposed()) continue;
    if (model.uri.scheme === "app") {
      if (model.uri.path === app) return state;
      continue;
    }
    const candidate = modelPath(model);
    if (candidate !== undefined && (drive ? candidate.toLowerCase() === normalized.toLowerCase() : candidate === normalized)) return state;
  }
  return undefined;
}

function onSaved(context: EditorSaveContext) {
  const state = findState(context.path);
  if (state) void lintModel(state, "save");
}

function detach() {
  for (const disposable of attachment) disposable.dispose();
  attachment = [];
  for (const key of [...models.keys()]) untrack(key);
  active = undefined;
}

/**
 * Attaches to a loaded Monaco (the contribution hook calls this; tests pass a
 * stub). Returns a disposer that removes listeners and every plugin marker.
 */
export function attachMonacoLinters(monaco: MonacoApi): () => void {
  detach();
  active = monaco;
  const removeSaved = onEditorSaved(onSaved);
  attachment = [
    { dispose: removeSaved },
    monaco.editor.onDidCreateModel((model) => track(model)),
    monaco.editor.onDidChangeModelLanguage(({ model }) => {
      const state = models.get(modelKey(model));
      if (!state) return track(model);
      state.ref = undefined;
      clearMarkers(state);
      if (model.uri.scheme === "file") runtime?.modelOpened?.(model.getLanguageId());
      void lintModel(state, "open");
    }),
  ];
  for (const model of monaco.editor.getModels()) track(model);
  return () => {
    if (active === monaco) detach();
  };
}

/** Lints every open model that uses `ref` again, e.g. after its workspace setting changed. */
export function relintMonacoLinter(ref: string): void {
  for (const state of models.values()) {
    if (!state.model.isDisposed() && linterFor(state.model)?.ref === ref) {
      state.ref = undefined;
      void lintModel(state, "open");
    }
  }
}

/** Wires the app runtime (store, i18n); call before the first linters are set. */
export function setMonacoLinterRuntime(next: MonacoLinterRuntime | undefined): void {
  runtime = next;
  refresh();
}

/** Replaces the available plugin linters; an empty list clears every plugin marker. */
export function setMonacoLinters(next: readonly PluginLinter[]): void {
  linters = next;
  if (next.length > 0 && !unregisterHook) {
    unregisterHook = registerMonacoContributionHook((monaco) => attachMonacoLinters(monaco));
    return;
  }
  refresh();
}

/** Re-runs the checks after a preference change (selection or lint while typing). */
export function refreshMonacoLinters(): void {
  refresh();
}

/** Replaces the linter invoker (tests; the default calls the host). */
export function setPluginLinterInvoker(next?: PluginLinterInvoker): void {
  invoker = next ?? defaultInvoker;
}
