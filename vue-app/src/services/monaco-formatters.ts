/**
 * Monaco stage of the `formatters` contribution point.
 *
 * - Each available plugin formatter registers a document formatting provider
 *   (and a range provider when it declares `range`) for its languages. The
 *   providers are tagged, and a formatter selector installed through
 *   `mincodeFormattingConflicts` makes the editor's own Format Document,
 *   Format Selection and format-on-paste deterministic: the plugin formatter
 *   selected for the language, otherwise the first non-plugin provider. A
 *   plugin formatter the user did not select never runs implicitly; an
 *   explicit Format Document with only plugin formatters asks first.
 * - "Format Document With…" (editor context menu) picks a formatter once and
 *   can remember it as the language default.
 * - A save participant (services/editor-save-participants.ts) formats with
 *   the selected plugin formatter when the user opted into format on save
 *   for that language, and applies the result only if the document did not
 *   change meanwhile, as one undo step.
 *
 * The host returns the whole formatted text; this module applies the minimal
 * single replacement. Monaco is reached only through
 * `registerMonacoContributionHook`, so importing this module never loads it.
 */
import {
  FORMATTER_LIMITS,
  chooseFormatterProvider,
  formattersForLanguage,
  minimalEdit,
  utf8ByteLength,
  type PluginFormatter,
} from "@/domain/plugins/formatters";
import { registerEditorSaveParticipant, type EditorSaveContext } from "@/services/editor-save-participants";
import { registerMonacoContributionHook, type MonacoApi } from "@/services/monaco-contributions";
import { jsonSchemaTargetForPath } from "@/services/monaco-json-schemas";
import type { FormatDocumentRequest, FormatDocumentResult, FormatRange } from "@/services/plugin-formatter-service";

import type { editor, IDisposable, languages, MincodeFormatterSelector } from "@/services/monaco-runtime.js";

type TextModel = editor.ITextModel;
type CodeEditor = editor.ICodeEditor;

export const FORMAT_DOCUMENT_WITH_ACTION_ID = "mincode.plugins.formatDocumentWith";

export type FormatterPickChoice = { kind: "builtin"; remember: boolean } | { kind: "plugin"; ref: string; remember: boolean };

export interface FormatterFailure {
  formatter: PluginFormatter;
  mode: "explicit" | "save";
  code: "too_large" | "timeout" | "changed" | "unavailable" | "failed";
  detail: string;
}

/** What the Monaco stage needs from the app (the consumer wires the store, i18n and dialogs). */
export interface MonacoFormatterRuntime {
  /** The selected, available plugin formatter ref for a language. */
  preferredRef(language: string): string | undefined;
  savePreferredRef?(language: string): string | undefined;
  /** The formatter to run on save for a language, when the user opted in. */
  formatOnSave(language: string): PluginFormatter | undefined;
  projectRoots(): readonly string[];
  displayName(formatter: PluginFormatter): string;
  formatWithLabel(): string;
  pick(options: { language: string; formatters: PluginFormatter[]; current?: string; builtin: boolean }): Promise<FormatterPickChoice | undefined>;
  remember(language: string, ref: string | undefined): void;
  report(failure: FormatterFailure): void;
}

/** Sends a document to a plugin formatter (tests replace it; the default calls the host). */
export type PluginFormatterInvoker = (
  formatter: PluginFormatter,
  request: FormatDocumentRequest,
  signal?: AbortSignal,
) => Promise<FormatDocumentResult>;

async function defaultInvoker(formatter: PluginFormatter, request: FormatDocumentRequest, signal?: AbortSignal) {
  // Loaded on first use: the formatter service reaches the desktop bridge.
  const { formatWithPlugin } = await import("@/services/plugin-formatter-service");
  return formatWithPlugin(formatter, request, signal);
}

let invoke: PluginFormatterInvoker = defaultInvoker;
let runtime: MonacoFormatterRuntime | undefined;
let formatters: readonly PluginFormatter[] = [];
let active: MonacoApi | undefined;
let unregisterHook: (() => void) | undefined;
let registrations: IDisposable[] = [];
let attachment: IDisposable[] = [];
const providerRefs = new WeakMap<object, string>();
/** Range providers reach the selector as synthetic wrappers that keep only the display name. */
const displayNameRefs = new Map<string, string>();
let forcedBuiltin: string | undefined;

function refOf(provider: { displayName?: string }): string | undefined {
  return providerRefs.get(provider) ?? (provider.displayName ? displayNameRefs.get(provider.displayName) : undefined);
}

function modelPath(model: TextModel): string | undefined {
  const uri = model.uri;
  if (uri.scheme === "file") return /^\/[A-Za-z]:\//.test(uri.path) ? uri.path.slice(1) : uri.path;
  if (uri.scheme === "app") return uri.path;
  return undefined;
}

const CLEAN_RELATIVE_PATH = /^(?!\.{1,2}(?:\/|$))(?!.*\/\.{1,2}(?:\/|$))[^:\\]+$/;

function documentRequestFields(model: TextModel) {
  const path = modelPath(model) ?? model.uri.path;
  const target = jsonSchemaTargetForPath(path, model.uri.scheme === "file" ? runtime?.projectRoots() ?? [] : []);
  const relativePath = target.relativePath && CLEAN_RELATIVE_PATH.test(target.relativePath) && !target.relativePath.includes("//")
    ? target.relativePath
    : undefined;
  return { fileName: target.basename || "untitled", ...(relativePath ? { relativePath } : {}) };
}

function failureOf(formatter: PluginFormatter, mode: "explicit" | "save", result: Extract<FormatDocumentResult, { ok: false }>): FormatterFailure | undefined {
  const { code, detail } = result.error;
  if (code === "cancelled") return undefined;
  if (code === "too_large" || code === "timeout") return { formatter, mode, code, detail };
  if (code === "not_found" || code === "unavailable" || code === "forbidden") return { formatter, mode, code: "unavailable", detail };
  return { formatter, mode, code: "failed", detail };
}

/**
 * Runs one formatter on a model and returns the minimal replacement, or
 * undefined when nothing should change (failures are reported).
 */
async function formatModel(
  monaco: MonacoApi,
  formatter: PluginFormatter,
  model: TextModel,
  mode: "explicit" | "save",
  options: { tabSize: number; insertSpaces: boolean },
  range: FormatRange | undefined,
  signal?: AbortSignal,
): Promise<languages.TextEdit | undefined> {
  const text = model.getValue();
  const version = model.getVersionId();
  if (utf8ByteLength(text) > FORMATTER_LIMITS.payloadMaxBytes - 4096) {
    runtime?.report({ formatter, mode, code: "too_large", detail: "" });
    return undefined;
  }
  const eol = model.getEOL() === "\r\n" ? "\r\n" : "\n";
  const result = await invoke(
    formatter,
    {
      languageId: model.getLanguageId(),
      ...documentRequestFields(model),
      text,
      ...(range ? { range } : {}),
      tabSize: options.tabSize,
      insertSpaces: options.insertSpaces,
      eol,
      mode,
    },
    signal,
  );
  if (!result.ok) {
    const failure = failureOf(formatter, mode, result);
    if (failure) runtime?.report(failure);
    return undefined;
  }
  if (model.isDisposed() || model.getVersionId() !== version) {
    runtime?.report({ formatter, mode, code: "changed", detail: "" });
    return undefined;
  }
  if (result.text === undefined) return undefined;
  const edit = minimalEdit(text, result.text.replace(/\r\n|\r|\n/g, eol));
  if (!edit) return undefined;
  const start = model.getPositionAt(edit.start);
  const end = model.getPositionAt(edit.end);
  return { range: new monaco.Range(start.lineNumber, start.column, end.lineNumber, end.column), text: edit.text };
}

function abortOn(token: { isCancellationRequested: boolean; onCancellationRequested(listener: () => void): IDisposable }) {
  const controller = new AbortController();
  if (token.isCancellationRequested) controller.abort();
  const subscription = token.onCancellationRequested(() => controller.abort());
  return { signal: controller.signal, dispose: () => subscription.dispose() };
}

function registerProviders(monaco: MonacoApi) {
  for (const disposable of registrations) disposable.dispose();
  registrations = [];
  displayNameRefs.clear();
  if (!runtime) return;
  for (const formatter of formatters) {
    const displayName = runtime.displayName(formatter);
    displayNameRefs.set(displayName, formatter.ref);
    for (const language of formatter.languages) {
      const provider: languages.DocumentFormattingEditProvider = {
        displayName,
        provideDocumentFormattingEdits: async (model, options, token) => {
          const cancel = abortOn(token);
          try {
            const edit = await formatModel(monaco, formatter, model, "explicit", options, undefined, cancel.signal);
            return edit ? [edit] : [];
          } finally {
            cancel.dispose();
          }
        },
      };
      providerRefs.set(provider, formatter.ref);
      registrations.push(monaco.languages.registerDocumentFormattingEditProvider(language, provider));
      if (!formatter.range) continue;
      const rangeProvider: languages.DocumentRangeFormattingEditProvider = {
        displayName,
        provideDocumentRangeFormattingEdits: async (model, range, options, token) => {
          const cancel = abortOn(token);
          try {
            const edit = await formatModel(
              monaco,
              formatter,
              model,
              "explicit",
              options,
              { startLineNumber: range.startLineNumber, startColumn: range.startColumn, endLineNumber: range.endLineNumber, endColumn: range.endColumn },
              cancel.signal,
            );
            return edit ? [edit] : [];
          } finally {
            cancel.dispose();
          }
        },
      };
      providerRefs.set(rangeProvider, formatter.ref);
      registrations.push(monaco.languages.registerDocumentRangeFormattingEditProvider(language, rangeProvider));
    }
  }
  registerFormatWithAction(monaco);
}

function languagePrecondition(): string | null {
  // Language ids match ^[a-z][a-z0-9+#._-]*$, so they cannot break out of the quoted value.
  const ids = [...new Set(formatters.flatMap((formatter) => formatter.languages))].sort();
  if (!ids.length) return null;
  return `!editorReadonly && (${ids.map((language) => `editorLangId == '${language}'`).join(" || ")})`;
}

function applyEdit(editor: CodeEditor, edit: languages.TextEdit) {
  editor.pushUndoStop();
  editor.executeEdits("mincode.pluginFormatter", [{ range: edit.range, text: edit.text, forceMoveMarkers: true }]);
  editor.pushUndoStop();
}

async function formatDocumentWith(monaco: MonacoApi, editor: CodeEditor) {
  const model = editor.getModel();
  if (!model || !runtime) return;
  const language = model.getLanguageId();
  const candidates = formattersForLanguage(formatters, language);
  if (!candidates.length) return;
  const choice = await runtime.pick({ language, formatters: candidates, current: runtime.preferredRef(language), builtin: true });
  if (!choice || model.isDisposed()) return;
  if (choice.remember) runtime.remember(language, choice.kind === "plugin" ? choice.ref : undefined);
  if (choice.kind === "builtin") {
    const uri = model.uri.toString();
    forcedBuiltin = uri;
    try {
      await editor.getAction("editor.action.formatDocument")?.run();
    } finally {
      if (forcedBuiltin === uri) forcedBuiltin = undefined;
    }
    return;
  }
  const formatter = candidates.find((candidate) => candidate.ref === choice.ref);
  if (!formatter) return;
  const options = model.getOptions();
  const version = model.getVersionId();
  const edit = await formatModel(monaco, formatter, model, "explicit", options, undefined);
  if (edit && !model.isDisposed() && model.getVersionId() === version && editor.getModel() === model) applyEdit(editor, edit);
}

function registerFormatWithAction(monaco: MonacoApi) {
  const precondition = languagePrecondition();
  if (!precondition || !runtime) return;
  registrations.push(
    monaco.editor.addEditorAction({
      id: FORMAT_DOCUMENT_WITH_ACTION_ID,
      label: runtime.formatWithLabel(),
      contextMenuGroupId: "1_modification",
      contextMenuOrder: 1.4,
      precondition,
      run: (editor) => formatDocumentWith(monaco, editor),
    }),
  );
}

const selector: MincodeFormatterSelector = async (providers, document, mode) => {
  const uri = document.uri.toString();
  if (forcedBuiltin === uri) {
    forcedBuiltin = undefined;
    return chooseFormatterProvider(providers, refOf, undefined);
  }
  const language = document.getLanguageId();
  const chosen = chooseFormatterProvider(providers, refOf, runtime?.preferredRef(language));
  if (chosen || mode !== 1 || !runtime) return chosen;
  // Explicit Format Document where only plugin formatters exist: ask the user.
  const refs = new Set(providers.map(refOf).filter((ref): ref is string => ref !== undefined));
  const candidates = formatters.filter((formatter) => refs.has(formatter.ref) && formatter.languages.includes(language));
  if (!candidates.length) return undefined;
  const choice = await runtime.pick({ language, formatters: candidates, builtin: false });
  if (choice?.kind !== "plugin") return undefined;
  if (choice.remember) runtime.remember(language, choice.ref);
  return providers.find((provider) => refOf(provider) === choice.ref);
};

function findModel(monaco: MonacoApi, path: string): TextModel | undefined {
  const normalized = path.replace(/\\/g, "/");
  const drive = /^[A-Za-z]:\//.test(normalized);
  const app = `/${normalized.replace(/^\/+/, "")}`;
  return monaco.editor.getModels().find((model) => {
    if (model.isDisposed()) return false;
    if (model.uri.scheme === "app") return model.uri.path === app;
    const candidate = model.uri.scheme === "file" ? modelPath(model) : undefined;
    if (candidate === undefined) return false;
    return drive ? candidate.toLowerCase() === normalized.toLowerCase() : candidate === normalized;
  });
}

async function saveParticipant(context: EditorSaveContext): Promise<void> {
  const monaco = active;
  if (context.formatterHandled || !monaco || !runtime) return;
  const model = findModel(monaco, context.path);
  if (!model) return;
  const formatter = runtime.formatOnSave(model.getLanguageId());
  if (!formatter || !formatters.some((candidate) => candidate.ref === formatter.ref)) return;
  const version = model.getVersionId();
  const controller = new AbortController();
  const abort = () => controller.abort();
  if (context.signal?.aborted) abort();
  context.signal?.addEventListener("abort", abort, { once: true });
  const timer = setTimeout(abort, FORMATTER_LIMITS.saveTimeoutMs + 1000);
  try {
    const edit = await formatModel(monaco, formatter, model, "save", model.getOptions(), undefined, controller.signal);
    if (controller.signal.aborted && !edit) {
      runtime.report({ formatter, mode: "save", code: "timeout", detail: "" });
      return;
    }
    if (!edit || controller.signal.aborted || context.signal?.aborted || context.isCurrent?.() === false || model.isDisposed() || model.getVersionId() !== version) return;
    model.pushStackElement();
    model.pushEditOperations([], [{ range: edit.range, text: edit.text, forceMoveMarkers: true }], () => null);
    model.pushStackElement();
  } finally {
    clearTimeout(timer);
    context.signal?.removeEventListener("abort", abort);
  }
}

function detach() {
  for (const disposable of [...registrations, ...attachment]) disposable.dispose();
  registrations = [];
  attachment = [];
  displayNameRefs.clear();
  forcedBuiltin = undefined;
  active = undefined;
}

/**
 * Attaches to a loaded Monaco (the contribution hook calls this; tests pass a
 * stub). Returns a disposer that removes providers, the action, the selector
 * and the save participant.
 */
export function attachMonacoFormatters(monaco: MonacoApi): () => void {
  detach();
  active = monaco;
  const removeSaveParticipant = registerEditorSaveParticipant(saveParticipant);
  attachment = [monaco.mincodeFormattingConflicts.setFormatterSelector(selector), { dispose: removeSaveParticipant }];
  registerProviders(monaco);
  return () => {
    if (active === monaco) detach();
  };
}

/** Wires the app runtime (store, i18n, dialogs); call before the first formatters are set. */
export function setMonacoFormatterRuntime(next: MonacoFormatterRuntime | undefined): void {
  runtime = next;
  if (active) registerProviders(active);
}

/** Replaces the available plugin formatters; an empty list removes every provider. */
export function setMonacoFormatters(next: readonly PluginFormatter[]): void {
  formatters = next;
  if (next.length > 0 && !unregisterHook) {
    unregisterHook = registerMonacoContributionHook((monaco) => attachMonacoFormatters(monaco));
    return;
  }
  if (active) registerProviders(active);
}

/** Re-registers providers and the action (display names, labels) after a UI language change. */
export function refreshMonacoFormatters(): void {
  if (active) registerProviders(active);
}

/** The plugin formatter ref of a Monaco formatting provider (tests, diagnostics). */
export function monacoFormatterProviderRef(provider: { displayName?: string }): string | undefined {
  return refOf(provider);
}

/** Replaces the formatter invoker (tests; the default calls the host). */
export function setPluginFormatterInvoker(next?: PluginFormatterInvoker): void {
  invoke = next ?? defaultInvoker;
}

/** One save-format path for an explicitly identified model. Plugin opt-ins remain independent. */
export async function formatProjectModelOnSave(
  monaco: MonacoApi, model: TextModel, context: EditorSaveContext,
): Promise<void> {
  if (context.signal?.aborted || context.isCurrent?.() === false || model.isDisposed()) return;
  if (!runtime) {
    const { ensureFormatterSaveRuntime } = await import("@/services/contributions/formatters-runtime");
    await ensureFormatterSaveRuntime();
    if (context.signal?.aborted || context.isCurrent?.() === false || model.isDisposed()) return;
  }
  const language = model.getLanguageId();
  const optedIn = runtime?.formatOnSave(language);
  if (!optedIn && !context.formatOnSave) return;
  const preferred = runtime?.savePreferredRef?.(language) ?? runtime?.preferredRef(language);
  const formatter = optedIn ?? (preferred ? formatters.find((item) => item.ref === preferred) : undefined);
  const controller = new AbortController();
  const abort = () => controller.abort();
  const version = model.getVersionId();
  if (preferred && !formatter) throw new Error("Selected formatter is unavailable");
  let timedOut = false;
  context.signal?.addEventListener("abort", abort, { once: true });
  const disposed = model.onWillDispose(abort);
  const changed = model.onDidChangeContent(abort);
  const timer = setTimeout(() => { timedOut = true; abort(); }, FORMATTER_LIMITS.saveTimeoutMs);
  try {
    if (formatter) {
      const edit = await formatModel(monaco, formatter, model, "save", model.getOptions(), undefined, controller.signal);
      if (timedOut) throw new Error("Save formatting timed out");
      if (!edit || controller.signal.aborted || context.isCurrent?.() === false || model.isDisposed() || model.getVersionId() !== version) return;
      changed.dispose();
      model.pushStackElement();
      model.pushEditOperations([], [{ ...edit, forceMoveMarkers: true }], () => null);
      model.pushStackElement();
    } else if (!preferred) {
      // Pick only non-plugin providers. Never ask a background save to show a picker.
      await monaco.mincodeSaveFormatting.run(model,
        (providers) => chooseFormatterProvider(providers, refOf, undefined), controller.signal);
      if (timedOut) throw new Error("Save formatting timed out");
    }
  } finally {
    clearTimeout(timer);
    controller.abort();
    disposed.dispose();
    changed.dispose();
    context.signal?.removeEventListener("abort", abort);
  }
}
