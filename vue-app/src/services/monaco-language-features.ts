/**
 * Monaco stage of the `languageFeatures` contribution point.
 *
 * Each provider registers, per declared kind, a hover, completion,
 * definition, reference, CodeLens, inlay hints, document symbol and/or
 * code action provider for its languages (Monaco merges them with the built-in and LSP
 * ones, without suppressing or reordering either source). A request sends a
 * bounded window of whole lines (the whole document for CodeLens, document
 * symbols and code actions); the host bounds time and result size, and the results
 * are re-validated before they reach here. Hover text is escaped Markdown
 * (never trusted, no HTML); completions insert their text literally; CodeLens
 * titles, inlay hint labels and symbol names are plain text.
 *
 * Scope: hover, completion and code actions serve `file:` and `app:`
 * models. The other kinds serve only `file:` models inside an open project root (never the
 * database query editor, other `app:` documents or external files);
 * locations become `file:` URIs under that root, built with `Uri.file` and
 * never parsed from a string, and opening them goes through the host's
 * editor opener (ProjectWorkspaceView `registerEditorOpener`).
 *
 * Passive kinds (codeLens, inlayHints, documentSymbol) wait
 * `passiveDebounceMs` before calling the host (a request cancelled meanwhile
 * is never sent) and pause after repeated plugin failures (the failure gate,
 * cleared when the provider list changes). A CodeLens command reaches Monaco
 * only as an opaque key (`mincode.pluginCodeLens`); a click looks the
 * plugin command up again and runs it through the plugin command paths.
 *
 * A code action only carries edits of the model it was computed for:
 * nothing is applied until the user picks it, then the edits are pushed as
 * one undo step if the model is unchanged and no editor shows it read-only.
 *
 * Errors, cancellations and results for an edited or disposed model yield no
 * result. Monaco is reached only through `registerMonacoContributionHook`,
 * so importing this module never loads it.
 */
import {
  CODE_ACTION_KINDS,
  codeActionDiagnostics,
  escapeHoverMarkdown,
  LANGUAGE_FEATURE_LIMITS,
  languageFeatureRangeWindow,
  languageFeatureTitle,
  languageFeatureWindow,
  SYMBOL_KINDS,
  validateCodeActionResult,
  type CodeActionResponse,
  type CompletionKind,
  type CompletionResponse,
  type HoverResponse,
  type LanguageFeatureDocument,
  type LanguageFeatureKind,
  type PluginLanguageFeature,
  type PluginRange,
  type PluginTextEdit,
} from "@/domain/plugins/language-features";
import {
  createPassiveFailureGate,
  locationTargetPath,
  projectRootForPath,
  type CodeLensResponse,
  type DocumentSymbolResponse,
  type InlayHintsResponse,
  type LanguageLocation,
  type LanguageRange,
  type LanguageSymbol,
  type LocationsResponse,
} from "@/domain/plugins/language-feature-results";
import { utf8Bytes } from "@/domain/plugins/provider-points";
import { registerMonacoContributionHook, type MonacoApi } from "@/services/monaco-contributions";
import { jsonSchemaTargetForPath } from "@/services/monaco-json-schemas";
import type { CodeActionRequest, LanguageFeatureRequest } from "@/services/plugin-language-feature-service";
import type { ProviderInvokeResult } from "@/services/plugin-provider-invoke";

import type { editor, IDisposable, IRange, languages, Position, Uri } from "@/services/monaco-runtime.js";

type TextModel = editor.ITextModel;
type CancellationToken = { isCancellationRequested: boolean; onCancellationRequested(listener: () => void): IDisposable };
type LineRange = { startLine: number; endLine: number };
type CommandArgs = Record<string, string | number | boolean>;

/** The host-internal Monaco command a plugin CodeLens runs; its only argument is an opaque key. */
export const PLUGIN_CODE_LENS_COMMAND = "mincode.pluginCodeLens";
const APPLY_ACTION_COMMAND_ID = "mincode.pluginLanguageFeatures.applyCodeAction";

/** What the Monaco stage needs from the app. */
export interface MonacoLanguageFeatureRuntime {
  locale(): string;
  projectRoots(): readonly string[];
  /** Tells the user a picked quick fix was refused because the editor is read-only. */
  notifyReadOnly?(): void;
  /** Registers a Monaco command; without it code actions are not offered. */
  registerEditorCommand?(id: string, handler: (...args: unknown[]) => unknown): () => void;
}

/** Calls a provider (tests replace it; the default calls the host). */
export interface LanguageFeatureInvoker {
  hover(provider: PluginLanguageFeature, request: LanguageFeatureRequest, signal?: AbortSignal): Promise<ProviderInvokeResult<HoverResponse>>;
  completion(provider: PluginLanguageFeature, request: LanguageFeatureRequest, signal?: AbortSignal): Promise<ProviderInvokeResult<CompletionResponse>>;
  definition(provider: PluginLanguageFeature, request: LanguageFeatureRequest, signal?: AbortSignal): Promise<ProviderInvokeResult<LocationsResponse>>;
  references(
    provider: PluginLanguageFeature,
    request: LanguageFeatureRequest & { includeDeclaration: boolean },
    signal?: AbortSignal,
  ): Promise<ProviderInvokeResult<LocationsResponse>>;
  codeLens(provider: PluginLanguageFeature, request: LanguageFeatureRequest, signal?: AbortSignal): Promise<ProviderInvokeResult<CodeLensResponse>>;
  inlayHints(
    provider: PluginLanguageFeature,
    request: LanguageFeatureRequest & { range: LineRange },
    signal?: AbortSignal,
  ): Promise<ProviderInvokeResult<InlayHintsResponse>>;
  documentSymbol(provider: PluginLanguageFeature, request: LanguageFeatureRequest, signal?: AbortSignal): Promise<ProviderInvokeResult<DocumentSymbolResponse>>;
  codeAction(provider: PluginLanguageFeature, request: CodeActionRequest, signal?: AbortSignal): Promise<ProviderInvokeResult<CodeActionResponse>>;
}

/** Runs the plugin command of a clicked CodeLens (tests replace it; the default uses plugin-commands). */
export interface LanguageFeatureCommandRunner {
  /** A read command with host-validated args (executePluginCommandWithArgs). */
  withArgs(pluginId: string, command: string, args: CommandArgs): Promise<void>;
  /** Any action command without args, through the menu path (when, parameter form, risk confirmation). */
  plain(pluginId: string, command: string): Promise<void>;
  /** Reports a click that did not run (the default shows a warning toast). */
  failed?(message: string): void;
}

function service() {
  return import("@/services/plugin-language-feature-service");
}

const defaultInvoker: LanguageFeatureInvoker = {
  async hover(provider, request, signal) {
    return (await service()).requestHover(provider, request, signal);
  },
  async completion(provider, request, signal) {
    return (await service()).requestCompletion(provider, request, signal);
  },
  async definition(provider, request, signal) {
    return (await service()).requestDefinition(provider, request, signal);
  },
  async references(provider, request, signal) {
    return (await service()).requestReferences(provider, request, signal);
  },
  async codeLens(provider, request, signal) {
    return (await service()).requestCodeLens(provider, request, signal);
  },
  async inlayHints(provider, request, signal) {
    return (await service()).requestInlayHints(provider, request, signal);
  },
  async documentSymbol(provider, request, signal) {
    return (await service()).requestDocumentSymbols(provider, request, signal);
  },
  async codeAction(provider, request, signal) {
    return (await service()).requestCodeAction(provider, request, signal);
  },
};

/** The default runner and toast load on the first click (services/plugin-code-lens-commands.ts). */
function codeLensCommands() {
  return import("@/services/plugin-code-lens-commands");
}

const defaultCommandRunner: LanguageFeatureCommandRunner = {
  async withArgs(pluginId, command, args) {
    await (await codeLensCommands()).runCodeLensCommandWithArgs(pluginId, command, args);
  },
  async plain(pluginId, command) {
    await (await codeLensCommands()).runCodeLensCommand(pluginId, command);
  },
  failed(message) {
    void codeLensCommands().then(({ showCodeLensFailure }) => showCodeLensFailure(message), () => undefined);
  },
};

let invoke: LanguageFeatureInvoker = defaultInvoker;
let commandRunner: LanguageFeatureCommandRunner = defaultCommandRunner;
let runtime: MonacoLanguageFeatureRuntime | undefined;
let providers: readonly PluginLanguageFeature[] = [];
let active: MonacoApi | undefined;
let unregisterHook: (() => void) | undefined;
let registrations: IDisposable[] = [];
let commandRegistration: IDisposable | undefined;

const gate = createPassiveFailureGate();
/** Ends pending debounces (resolves them as cancelled). */
const pendingDebounces = new Set<() => void>();
/** Aborts host requests in flight. */
const inflight = new Set<AbortController>();
/** Opaque CodeLens key -> the plugin command it runs; entries leave with their CodeLensList. */
const lensTargets = new Map<string, { pluginId: string; command: string; args?: CommandArgs }>();
let lensSequence = 0;
/** At most this many live CodeLens keys (20 full lists); older ones are forgotten. */
export const CODE_LENS_TARGETS_MAX = LANGUAGE_FEATURE_LIMITS.codeLensesMax * 20;
const codeLensListeners = new Set<() => void>();
const inlayHintListeners = new Set<() => void>();
/** Disposers of the code action command (runtime.registerEditorCommand). */
let commandDisposers: Array<() => void> = [];
/** Quick fixes offered in the lightbulb menu, applied only when picked. */
const pendingActions = new Map<string, { model: TextModel; version: number; edits: PluginTextEdit[] }>();
let pendingSequence = 0;

const CLEAN_RELATIVE_PATH = /^(?!\.{1,2}(?:\/|$))(?!.*\/\.{1,2}(?:\/|$))[^:\\]+$/;
const KEY_SEPARATOR = "\u{0}";

function modelPath(model: TextModel): string | undefined {
  const uri = model.uri;
  if (uri.scheme === "file") return /^\/[A-Za-z]:\//.test(uri.path) ? uri.path.slice(1) : uri.path;
  if (uri.scheme === "app") return uri.path;
  return undefined;
}

function abortOn(token: CancellationToken) {
  const controller = new AbortController();
  if (token.isCancellationRequested) controller.abort();
  const subscription = token.onCancellationRequested(() => controller.abort());
  return { signal: controller.signal, dispose: () => subscription.dispose() };
}

/** Subscribes `cancel` to the model's disposal (when the model supports it). */
function onModelDispose(model: TextModel, cancel: () => void): IDisposable | undefined {
  return typeof model.onWillDispose === "function" ? model.onWillDispose(cancel) : undefined;
}

/** Like abortOn, and also aborted by model disposal and by a provider list change. */
function trackedAbort(token: CancellationToken, model: TextModel) {
  const controller = new AbortController();
  if (token.isCancellationRequested) controller.abort();
  const abort = () => controller.abort();
  const subscriptions = [token.onCancellationRequested(abort), onModelDispose(model, abort)];
  inflight.add(controller);
  return {
    signal: controller.signal,
    dispose() {
      inflight.delete(controller);
      for (const subscription of subscriptions) subscription?.dispose();
    },
  };
}

/** Waits `ms`; false when the token, the model or a provider list change ended the wait first. */
function debounce(ms: number, token: CancellationToken, model: TextModel): Promise<boolean> {
  if (token.isCancellationRequested) return Promise.resolve(false);
  return new Promise((resolve) => {
    const subscriptions: Array<IDisposable | undefined> = [];
    const finish = (value: boolean) => {
      clearTimeout(timer);
      pendingDebounces.delete(cancel);
      for (const subscription of subscriptions) subscription?.dispose();
      resolve(value);
    };
    const cancel = () => finish(false);
    const timer = setTimeout(() => finish(true), ms);
    pendingDebounces.add(cancel);
    subscriptions.push(token.onCancellationRequested(cancel), onModelDispose(model, cancel));
  });
}

function cancelPending() {
  for (const cancel of [...pendingDebounces]) cancel();
  for (const controller of [...inflight]) controller.abort();
}

function fresh(model: TextModel, version: number, token: CancellationToken) {
  return !token.isCancellationRequested && !model.isDisposed() && model.getVersionId() === version;
}

/** The document fields of every request: language, file name, relative path when clean. */
function documentFields(model: TextModel): Pick<LanguageFeatureRequest, "languageId" | "fileName" | "relativePath"> | undefined {
  const path = modelPath(model);
  if (path === undefined || !runtime) return undefined;
  const target = jsonSchemaTargetForPath(path, model.uri.scheme === "file" ? runtime.projectRoots() : []);
  const relativePath = target.relativePath && CLEAN_RELATIVE_PATH.test(target.relativePath) && !target.relativePath.includes("//")
    ? target.relativePath
    : undefined;
  return {
    languageId: model.getLanguageId(),
    fileName: target.basename || "untitled",
    ...(relativePath ? { relativePath } : {}),
  };
}

function buildRequest(model: TextModel, position: Position, triggerCharacter?: string): LanguageFeatureRequest | undefined {
  const fields = documentFields(model);
  if (!fields || !runtime) return undefined;
  const window = languageFeatureWindow(model.getLinesContent(), position.lineNumber);
  if (!window) return undefined;
  return {
    ...fields,
    text: window.text,
    startLine: window.startLine,
    position: { line: position.lineNumber, column: position.column },
    ...(triggerCharacter ? { triggerCharacter } : {}),
    locale: runtime.locale(),
  };
}

/** The whole document (codeLens, documentSymbol); undefined over textMaxBytes. */
function wholeDocumentRequest(model: TextModel): LanguageFeatureRequest | undefined {
  const fields = documentFields(model);
  if (!fields || !runtime) return undefined;
  const lines = model.getLinesContent();
  if (lines.length > LANGUAGE_FEATURE_LIMITS.lineMax) return undefined;
  // Characters never exceed UTF-8 bytes: a large document is refused before it is joined and encoded.
  let characters = -1;
  for (const line of lines) {
    characters += line.length + 1;
    if (characters > LANGUAGE_FEATURE_LIMITS.textMaxBytes) return undefined;
  }
  const text = lines.join("\n");
  if (utf8Bytes(text) > LANGUAGE_FEATURE_LIMITS.textMaxBytes) return undefined;
  return { ...fields, text, startLine: 1, locale: runtime.locale() };
}

/** The visible lines and a window of whole lines around them (inlayHints). */
function rangeRequest(model: TextModel, range: IRange): (LanguageFeatureRequest & { range: LineRange }) | undefined {
  const fields = documentFields(model);
  if (!fields || !runtime) return undefined;
  const lines = model.getLinesContent();
  const window = languageFeatureRangeWindow(lines, range.startLineNumber, Math.min(range.endLineNumber, lines.length));
  if (!window) return undefined;
  return { ...fields, text: window.text, startLine: window.startLine, range: window.range, locale: runtime.locale() };
}

/** The project a `file:` model belongs to; the new kinds serve nothing else. */
function projectDocument(model: TextModel): { root: string; relativePath: string } | undefined {
  if (!runtime || model.uri.scheme !== "file") return undefined;
  const path = modelPath(model);
  const root = path === undefined ? undefined : projectRootForPath(path, runtime.projectRoots());
  if (!root) return undefined;
  return { root, relativePath: documentFields(model)?.relativePath ?? "" };
}

function toRange(range: LanguageRange): IRange {
  return { startLineNumber: range.startLine, startColumn: range.startColumn, endLineNumber: range.endLine, endColumn: range.endColumn };
}

/** A range in this document, clipped to it; undefined when it starts past the last line. */
function documentRange(model: TextModel, range: LanguageRange): IRange | undefined {
  if (range.startLine > model.getLineCount()) return undefined;
  return model.validateRange(toRange(range));
}

function locationTarget(
  monaco: MonacoApi,
  model: TextModel,
  document: { root: string; relativePath: string },
  location: LanguageLocation,
): { uri: Uri; range: IRange; selection?: IRange } | undefined {
  const target = locationTargetPath(document.root, document.relativePath, location.path);
  if (!target) return undefined;
  if (target.sameDocument) {
    const range = documentRange(model, location.range);
    if (!range) return undefined;
    const selection = location.selectionRange ? documentRange(model, location.selectionRange) : undefined;
    return { uri: model.uri, range, ...(selection ? { selection } : {}) };
  }
  return {
    uri: monaco.Uri.file(target.absolutePath),
    range: toRange(location.range),
    ...(location.selectionRange ? { selection: toRange(location.selectionRange) } : {}),
  };
}

function completionKind(monaco: MonacoApi, kind: CompletionKind | undefined): languages.CompletionItemKind {
  const kinds = monaco.languages.CompletionItemKind;
  switch (kind) {
    case "method": return kinds.Method;
    case "function": return kinds.Function;
    case "constructor": return kinds.Constructor;
    case "field": return kinds.Field;
    case "variable": return kinds.Variable;
    case "class": return kinds.Class;
    case "interface": return kinds.Interface;
    case "module": return kinds.Module;
    case "property": return kinds.Property;
    case "unit": return kinds.Unit;
    case "value": return kinds.Value;
    case "enum": return kinds.Enum;
    case "keyword": return kinds.Keyword;
    case "snippet": return kinds.Snippet;
    case "color": return kinds.Color;
    case "file": return kinds.File;
    case "reference": return kinds.Reference;
    case "folder": return kinds.Folder;
    case "constant": return kinds.Constant;
    case "struct": return kinds.Struct;
    case "event": return kinds.Event;
    case "operator": return kinds.Operator;
    case "type": return kinds.TypeParameter;
    default: return kinds.Text;
  }
}

async function provideHover(provider: PluginLanguageFeature, model: TextModel, position: Position, token: CancellationToken): Promise<languages.Hover | null> {
  const request = buildRequest(model, position);
  if (!request) return null;
  const version = model.getVersionId();
  const abort = abortOn(token);
  try {
    const result = await invoke.hover(provider, request, abort.signal);
    if (!result.ok || token.isCancellationRequested || model.isDisposed() || model.getVersionId() !== version) return null;
    if (result.response.contents.length === 0) return null;
    return {
      contents: result.response.contents.map((content) => ({ value: escapeHoverMarkdown(content), isTrusted: false, supportHtml: false })),
    };
  } catch {
    return null;
  } finally {
    abort.dispose();
  }
}

async function provideCompletion(
  monaco: MonacoApi,
  provider: PluginLanguageFeature,
  model: TextModel,
  position: Position,
  context: languages.CompletionContext,
  token: CancellationToken,
): Promise<languages.CompletionList> {
  const empty: languages.CompletionList = { suggestions: [] };
  const trigger = context.triggerKind === monaco.languages.CompletionTriggerKind.TriggerCharacter
    && context.triggerCharacter
    && provider.triggerCharacters?.includes(context.triggerCharacter)
    ? context.triggerCharacter
    : undefined;
  const request = buildRequest(model, position, trigger);
  if (!request) return empty;
  const version = model.getVersionId();
  const abort = abortOn(token);
  try {
    const result = await invoke.completion(provider, request, abort.signal);
    if (!result.ok || token.isCancellationRequested || model.isDisposed() || model.getVersionId() !== version) return empty;
    const word = model.getWordUntilPosition(position);
    const range = {
      startLineNumber: position.lineNumber,
      startColumn: word.startColumn,
      endLineNumber: position.lineNumber,
      endColumn: position.column,
    };
    const suggestions = result.response.items.map((item) => ({
      label: item.label,
      kind: completionKind(monaco, item.kind),
      ...(item.detail ? { detail: item.detail } : {}),
      // A plain string is rendered as text by Monaco, never as Markdown/HTML.
      ...(item.documentation ? { documentation: item.documentation } : {}),
      insertText: item.insertText ?? item.label,
      range,
    }));
    return { suggestions, incomplete: result.response.incomplete };
  } catch {
    return empty;
  } finally {
    abort.dispose();
  }
}

/** A definition / references call: project files only, not debounced; the locations of a fresh result. */
async function navigate(
  monaco: MonacoApi,
  model: TextModel,
  position: Position,
  token: CancellationToken,
  call: (request: LanguageFeatureRequest, signal: AbortSignal) => Promise<ProviderInvokeResult<LocationsResponse>>,
) {
  const document = projectDocument(model);
  const request = document ? buildRequest(model, position) : undefined;
  if (!document || !request) return [];
  const version = model.getVersionId();
  const abort = trackedAbort(token, model);
  try {
    const result = await call({ ...request, projectRoot: document.root }, abort.signal);
    if (!result.ok || !fresh(model, version, token)) return [];
    return result.response.locations.flatMap((location) => locationTarget(monaco, model, document, location) ?? []);
  } catch {
    return [];
  } finally {
    abort.dispose();
  }
}

async function provideDefinition(
  monaco: MonacoApi,
  provider: PluginLanguageFeature,
  model: TextModel,
  position: Position,
  token: CancellationToken,
): Promise<languages.LocationLink[]> {
  const targets = await navigate(monaco, model, position, token, (request, signal) => invoke.definition(provider, request, signal));
  return targets.map(({ uri, range, selection }) => ({ uri, range, ...(selection ? { targetSelectionRange: selection } : {}) }));
}

async function provideReferences(
  monaco: MonacoApi,
  provider: PluginLanguageFeature,
  model: TextModel,
  position: Position,
  context: languages.ReferenceContext,
  token: CancellationToken,
): Promise<languages.Location[]> {
  const targets = await navigate(monaco, model, position, token, (request, signal) =>
    invoke.references(provider, { ...request, includeDeclaration: context.includeDeclaration === true }, signal));
  return targets.map(({ uri, range }) => ({ uri, range }));
}

function suspensionKey(provider: PluginLanguageFeature, feature: LanguageFeatureKind) {
  return [provider.pluginId, provider.id, feature].join(KEY_SEPARATOR);
}

/**
 * A passive call: debounced, skipped while the (plugin, provider, kind) is
 * suspended, outcome recorded in the failure gate; the response of a fresh
 * successful result, else undefined.
 */
async function passive<R extends LanguageFeatureRequest, T>(
  provider: PluginLanguageFeature,
  feature: LanguageFeatureKind,
  model: TextModel,
  token: CancellationToken,
  build: () => R | undefined,
  call: (request: R, signal: AbortSignal) => Promise<ProviderInvokeResult<T>>,
): Promise<T | undefined> {
  const key = suspensionKey(provider, feature);
  if (!gate.allowed(key)) return undefined;
  if (!(await debounce(LANGUAGE_FEATURE_LIMITS.passiveDebounceMs, token, model))) return undefined;
  if (token.isCancellationRequested || model.isDisposed() || !gate.allowed(key)) return undefined;
  const request = build();
  if (!request) return undefined;
  const version = model.getVersionId();
  const abort = trackedAbort(token, model);
  try {
    const result = await call(request, abort.signal);
    gate.record(key, result.ok ? "ok" : result.error.code);
    return result.ok && fresh(model, version, token) ? result.response : undefined;
  } catch {
    gate.record(key, "unknown");
    return undefined;
  } finally {
    abort.dispose();
  }
}

async function provideCodeLenses(provider: PluginLanguageFeature, model: TextModel, token: CancellationToken): Promise<languages.CodeLensList> {
  const empty: languages.CodeLensList = { lenses: [], dispose() {} };
  if (!projectDocument(model)) return empty;
  const response = await passive(provider, "codeLens", model, token, () => wholeDocumentRequest(model), (request, signal) =>
    invoke.codeLens(provider, request, signal));
  if (!response) return empty;
  const keys: string[] = [];
  const lenses = response.lenses.flatMap((lens): languages.CodeLens[] => {
    const range = documentRange(model, lens.range);
    if (!range) return [];
    // Without a command the lens is plain text (Monaco renders an empty id as a label).
    if (!lens.command) return [{ range, command: { id: "", title: lens.title } }];
    lensSequence += 1;
    const key = `lens-${lensSequence}`;
    lensTargets.set(key, { pluginId: provider.pluginId, command: lens.command, ...(lens.args ? { args: lens.args } : {}) });
    // Bounded even if a list is never disposed: the oldest keys go first (Map keeps insertion order).
    for (const stale of lensTargets.keys()) {
      if (lensTargets.size <= CODE_LENS_TARGETS_MAX) break;
      lensTargets.delete(stale);
    }
    keys.push(key);
    return [{ range, command: { id: PLUGIN_CODE_LENS_COMMAND, title: lens.title, arguments: [key] } }];
  });
  return {
    lenses,
    dispose() {
      for (const key of keys) lensTargets.delete(key);
    },
  };
}

function documentOf(model: TextModel): LanguageFeatureDocument {
  return { lineCount: model.getLineCount(), maxColumn: (line) => model.getLineMaxColumn(line) };
}

function pluginRange(range: IRange): PluginRange {
  return { startLine: range.startLineNumber, startColumn: range.startColumn, endLine: range.endLineNumber, endColumn: range.endColumn };
}

/** Logs how many results were dropped or cut (never their content). */
function logDropped(provider: PluginLanguageFeature, feature: string, response: { dropped: number; truncated: boolean }, received: number, kept: number) {
  if (response.dropped === 0 && !response.truncated && received === kept) return;
  // eslint-disable-next-line no-console -- counts only, never plugin text, so dropped results stay diagnosable.
  console.warn(
    `[languageFeatures] ${provider.pluginId}/${provider.id} ${feature}: kept ${kept} of ${received} (host dropped ${response.dropped}${response.truncated ? ", truncated" : ""})`,
  );
}

function markerSeverity(monaco: MonacoApi, severity: number): string {
  const severities = monaco.MarkerSeverity;
  if (severity === severities.Error) return "error";
  if (severity === severities.Warning) return "warning";
  if (severity === severities.Info) return "info";
  return "hint";
}

async function provideCodeActions(
  monaco: MonacoApi,
  provider: PluginLanguageFeature,
  model: TextModel,
  range: IRange,
  context: languages.CodeActionContext,
  token: CancellationToken,
): Promise<languages.CodeActionList | null> {
  if (commandDisposers.length === 0) return null;
  const base = wholeDocumentRequest(model);
  if (!base) return null;
  const diagnostics = codeActionDiagnostics(
    (context.markers ?? []).map((marker) => ({
      range: pluginRange(marker),
      message: marker.message,
      severity: markerSeverity(monaco, marker.severity),
      ...(typeof marker.source === "string" ? { source: marker.source } : {}),
    })),
    documentOf(model),
  );
  const request: CodeActionRequest = {
    languageId: base.languageId,
    fileName: base.fileName,
    ...(base.relativePath ? { relativePath: base.relativePath } : {}),
    text: base.text,
    startLine: 1,
    locale: base.locale,
    range: pluginRange(range),
    diagnostics,
  };
  const version = model.getVersionId();
  const abort = trackedAbort(token, model);
  try {
    const result = await invoke.codeAction(provider, request, abort.signal);
    if (!result.ok || !fresh(model, version, token)) return null;
    const actions = validateCodeActionResult(result.response.actions, documentOf(model));
    logDropped(provider, "codeAction", result.response, result.response.actions.length, actions.length);
    if (actions.length === 0) return null;
    const keys: string[] = [];
    return {
      actions: actions.map((action) => {
        const key = `${++pendingSequence}`;
        keys.push(key);
        pendingActions.set(key, { model, version, edits: action.edits });
        return {
          title: action.title,
          ...(action.kind ? { kind: action.kind } : {}),
          ...(action.isPreferred ? { isPreferred: true } : {}),
          // No WorkspaceEdit: Monaco never applies anything itself; the command
          // applies the edits to this model only when the user picks the action.
          command: { id: APPLY_ACTION_COMMAND_ID, title: action.title, arguments: [key] },
        };
      }),
      dispose() {
        for (const key of keys) pendingActions.delete(key);
      },
    };
  } catch {
    return null;
  } finally {
    abort.dispose();
  }
}

/** Whether an editor showing the model is read-only (checked when a quick fix is picked). */
function readOnlyModel(model: TextModel): boolean {
  if (!active) return false;
  const readOnly = active.editor.EditorOption.readOnly;
  return active.editor.getEditors().some((candidate) => candidate.getModel() === model && candidate.getOption(readOnly) === true);
}

/**
 * Applies a picked quick fix to its own model as one undo step; stale
 * actions are ignored and a read-only editor refuses it.
 */
function applyCodeAction(key: unknown) {
  if (typeof key !== "string") return;
  const pending = pendingActions.get(key);
  pendingActions.delete(key);
  if (!pending || pending.model.isDisposed() || pending.model.getVersionId() !== pending.version) return;
  const { model, edits } = pending;
  if (readOnlyModel(model)) {
    runtime?.notifyReadOnly?.();
    return;
  }
  model.pushStackElement();
  model.pushEditOperations([], edits.map((edit) => ({ range: toRange(edit.range), text: edit.text, forceMoveMarkers: true })), () => null);
  model.pushStackElement();
}

function registerCommands() {
  if (commandDisposers.length > 0 || !runtime?.registerEditorCommand) return;
  commandDisposers = [runtime.registerEditorCommand(APPLY_ACTION_COMMAND_ID, (key) => applyCodeAction(key))];
}

function inlayHintKind(kind: string | undefined): languages.InlayHintKind | undefined {
  // Monaco InlayHintKind: Type = 1, Parameter = 2 (contract inlayHintKinds).
  if (kind === "type") return 1;
  if (kind === "parameter") return 2;
  return undefined;
}

async function provideInlayHints(provider: PluginLanguageFeature, model: TextModel, range: IRange, token: CancellationToken): Promise<languages.InlayHintList> {
  const empty: languages.InlayHintList = { hints: [], dispose() {} };
  if (!projectDocument(model)) return empty;
  const response = await passive(provider, "inlayHints", model, token, () => rangeRequest(model, range), (request, signal) =>
    invoke.inlayHints(provider, request, signal));
  if (!response) return empty;
  const lineCount = model.getLineCount();
  const hints = response.hints.flatMap((hint): languages.InlayHint[] => {
    if (hint.position.line > lineCount) return [];
    const kind = inlayHintKind(hint.kind);
    return [{
      label: hint.label,
      position: { lineNumber: hint.position.line, column: hint.position.column },
      ...(kind !== undefined ? { kind } : {}),
      ...(hint.paddingLeft ? { paddingLeft: true } : {}),
      ...(hint.paddingRight ? { paddingRight: true } : {}),
    }];
  });
  return { hints, dispose() {} };
}

function toDocumentSymbol(model: TextModel, symbol: LanguageSymbol): languages.DocumentSymbol | undefined {
  const range = documentRange(model, symbol.range);
  if (!range) return undefined;
  const children = symbol.children?.flatMap((child) => toDocumentSymbol(model, child) ?? []);
  return {
    name: symbol.name,
    detail: symbol.detail ?? "",
    // SYMBOL_KINDS is in Monaco SymbolKind order (0-25); the parser only lets listed kinds through.
    kind: SYMBOL_KINDS.indexOf(symbol.kind) as languages.SymbolKind,
    tags: [],
    range,
    selectionRange: documentRange(model, symbol.selectionRange) ?? range,
    ...(children?.length ? { children } : {}),
  };
}

async function provideDocumentSymbols(provider: PluginLanguageFeature, model: TextModel, token: CancellationToken): Promise<languages.DocumentSymbol[]> {
  if (!projectDocument(model)) return [];
  const response = await passive(provider, "documentSymbol", model, token, () => wholeDocumentRequest(model), (request, signal) =>
    invoke.documentSymbol(provider, request, signal));
  return response ? response.symbols.flatMap((symbol) => toDocumentSymbol(model, symbol) ?? []) : [];
}

function reportCodeLensFailure(runner: LanguageFeatureCommandRunner, message: string) {
  (runner.failed ?? defaultCommandRunner.failed)?.(message);
}

/** The `mincode.pluginCodeLens` handler: only an opaque key of a live CodeLensList runs anything. */
async function runCodeLens(key: unknown): Promise<void> {
  if (typeof key !== "string") return;
  const runner = commandRunner;
  const target = lensTargets.get(key);
  if (!target) {
    reportCodeLensFailure(runner, "The CodeLens is out of date");
    return;
  }
  try {
    if (target.args) await runner.withArgs(target.pluginId, target.command, target.args);
    else await runner.plain(target.pluginId, target.command);
  } catch (error) {
    reportCodeLensFailure(runner, error instanceof Error ? error.message : String(error));
  }
}

function subscribe(listeners: Set<() => void>, listener: () => void): IDisposable {
  listeners.add(listener);
  return { dispose: () => listeners.delete(listener) };
}

/** Adds a displayName that follows the UI locale (Monaco reads it when grouping, e.g. the outline). */
function named<T extends object>(provider: PluginLanguageFeature, value: T): T & { readonly displayName: string } {
  return Object.defineProperty(value, "displayName", {
    enumerable: true,
    get: () => languageFeatureTitle(provider, runtime?.locale() ?? "en"),
  }) as T & { readonly displayName: string };
}

function registerProviders(monaco: MonacoApi) {
  for (const disposable of registrations) disposable.dispose();
  registrations = [];
  if (!runtime) return;
  registerCommands();
  for (const provider of providers) {
    const selector = [...provider.languages];
    const has = (feature: LanguageFeatureKind) => provider.features.includes(feature);
    if (has("hover")) {
      registrations.push(monaco.languages.registerHoverProvider(selector, {
        provideHover: (model, position, token) => provideHover(provider, model, position, token),
      }));
    }
    if (has("completion")) {
      registrations.push(monaco.languages.registerCompletionItemProvider(selector, {
        ...(provider.triggerCharacters?.length ? { triggerCharacters: [...provider.triggerCharacters] } : {}),
        provideCompletionItems: (model, position, context, token) => provideCompletion(monaco, provider, model, position, context, token),
      }));
    }
    if (has("definition")) {
      registrations.push(monaco.languages.registerDefinitionProvider(selector, named(provider, {
        provideDefinition: (model: TextModel, position: Position, token: CancellationToken) => provideDefinition(monaco, provider, model, position, token),
      })));
    }
    if (has("references")) {
      registrations.push(monaco.languages.registerReferenceProvider(selector, named(provider, {
        provideReferences: (model: TextModel, position: Position, context: languages.ReferenceContext, token: CancellationToken) =>
          provideReferences(monaco, provider, model, position, context, token),
      })));
    }
    if (has("codeLens")) {
      const lensProvider: languages.CodeLensProvider = named(provider, {
        onDidChange: (listener: (provider: languages.CodeLensProvider) => unknown) => subscribe(codeLensListeners, () => listener(lensProvider)),
        provideCodeLenses: (model: TextModel, token: CancellationToken) => provideCodeLenses(provider, model, token),
      });
      registrations.push(monaco.languages.registerCodeLensProvider(selector, lensProvider));
    }
    if (has("inlayHints")) {
      registrations.push(monaco.languages.registerInlayHintsProvider(selector, named(provider, {
        onDidChangeInlayHints: (listener: () => unknown) => subscribe(inlayHintListeners, () => listener()),
        provideInlayHints: (model: TextModel, range: IRange, token: CancellationToken) => provideInlayHints(provider, model, range, token),
      })));
    }
    if (has("documentSymbol")) {
      registrations.push(monaco.languages.registerDocumentSymbolProvider(selector, named(provider, {
        provideDocumentSymbols: (model: TextModel, token: CancellationToken) => provideDocumentSymbols(provider, model, token),
      })));
    }
    if (has("codeAction")) {
      registrations.push(monaco.languages.registerCodeActionProvider(selector, {
        provideCodeActions: (model, range, context, token) => provideCodeActions(monaco, provider, model, range, context, token),
      }, { providedCodeActionKinds: [...CODE_ACTION_KINDS] }));
    }
  }
  // Editors showing CodeLens or inlay hints ask again (a reloaded plugin may answer differently).
  for (const listener of [...codeLensListeners, ...inlayHintListeners]) listener();
}

/** Ends pending work and forgets per-provider state (provider list changed or Monaco detached). */
function resetProviderState() {
  cancelPending();
  gate.reset();
  lensTargets.clear();
  pendingActions.clear();
}

function detach() {
  for (const disposable of registrations) disposable.dispose();
  registrations = [];
  commandRegistration?.dispose();
  commandRegistration = undefined;
  for (const dispose of commandDisposers) dispose();
  commandDisposers = [];
  resetProviderState();
  codeLensListeners.clear();
  inlayHintListeners.clear();
  active = undefined;
}

/** Attaches to a loaded Monaco (the contribution hook calls this; tests pass a stub). */
export function attachMonacoLanguageFeatures(monaco: MonacoApi): () => void {
  detach();
  active = monaco;
  commandRegistration = monaco.editor.registerCommand(PLUGIN_CODE_LENS_COMMAND, (_accessor: unknown, key: unknown) => runCodeLens(key));
  registerProviders(monaco);
  return () => {
    if (active === monaco) detach();
  };
}

/** Wires the app runtime (locale, project roots); call before the first providers are set. */
export function setMonacoLanguageFeatureRuntime(next: MonacoLanguageFeatureRuntime | undefined): void {
  runtime = next;
  if (active) registerProviders(active);
}

/**
 * Replaces the available providers (a plugin was enabled, reloaded, updated,
 * disabled or uninstalled); an empty list removes every Monaco provider.
 * Pending debounces and host requests end, suspensions lift and CodeLens keys
 * are forgotten.
 */
export function setMonacoLanguageFeatures(next: readonly PluginLanguageFeature[]): void {
  providers = next;
  resetProviderState();
  if (next.length > 0 && !unregisterHook) {
    unregisterHook = registerMonacoContributionHook((monaco) => attachMonacoLanguageFeatures(monaco));
    return;
  }
  if (active) registerProviders(active);
}

/** Removes the hook and every registration (the consumer was unregistered). */
export function stopMonacoLanguageFeatures(): void {
  unregisterHook?.();
  unregisterHook = undefined;
  detach();
  providers = [];
  runtime = undefined;
}

/** The (plugin, provider, kind) keys of passive providers suspended after repeated failures (diagnostics, tests). */
export function languageFeatureSuspensions(): string[] {
  return gate.suspensions();
}

/** Replaces the invoker (tests; the default calls the host). */
export function setLanguageFeatureInvoker(next?: LanguageFeatureInvoker): void {
  invoke = next ?? defaultInvoker;
}

/** Replaces the CodeLens command runner (tests; the default dynamically imports plugin-commands). */
export function setLanguageFeatureCommandRunner(next?: LanguageFeatureCommandRunner): void {
  commandRunner = next ?? defaultCommandRunner;
}
