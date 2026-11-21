<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import * as monaco from "@/services/monaco-runtime.js";
import type {
  ProjectBlameHunk,
  MonacoPresentationPreferences,
  ProjectEditorRevealPosition,
  ProjectEditorViewState,
} from "@/domain/projects/types";
import { cleanSavedText } from "@/domain/projects/save-policy";
import type { ProjectEditorPreferences } from "@/domain/projects/types";
import { formatProjectModelOnSave } from "@/services/monaco-formatters";
import type { EditorSaveContext } from "@/services/editor-save-participants";
import { buildSqlCompletionItems, type SqlCompletionItem } from "@/domain/database/sql-completion";
import { UI_SESSION_CAPTURE_EVENT } from "@/services/ui-session";
import { getMonacoOverflowWidgetsHost } from "@/services/monaco-overflow-host";
import { PLUGIN_LANGUAGES_CHANGED_EVENT, detectMonacoEditorLanguage } from "@/services/monaco-languages";
import { projectEditorModelUri } from "@/composables/project-file-uri";
import { acquireMonacoModel, releaseMonacoModel } from "@/services/monaco-model-pool";
import {
  ensureMonacoKeymap,
  registerMonacoEditor,
  type MonacoEditorKind,
  type MonacoEditorRegistration,
} from "@/services/monaco-keymap";
import type { MonacoMenuContext } from "@/services/monaco-menus";
import { LOGPOINT_UNSUPPORTED_REASON, normalizeLogMessage } from "@/domain/debug/logpoints";
import type { SourceBreakpoint } from "@/domain/debug/types";
import { hoverExpressionAt } from "@/domain/debug/inline-values";
import { useVueI18n } from "@/i18n/messages";
import AppContextMenu from "@/components/ui/AppContextMenu.vue";
import {
  debugEditorOptions,
  useEditorDebugDecorations,
  type BreakpointMove,
  type DebugAnchor,
  type DebugBreakpointHoverDetails,
  type DebugDecorationLabels,
  type DebugEntryPointMarker,
  type DebugEntryPointState,
} from "@/composables/use-editor-debug-decorations";
import {
  readStoredUiTheme,
  resolveMonacoEditorTheme,
  UI_THEME_CHANGED_EVENT,
  type UiTheme,
} from "@/services/ui-theme";

const props = defineProps<{
  modelValue: string;
  path: string;
  openPaths?: string[];
  preferences: MonacoPresentationPreferences;
  projectRoot?: string;
  revealPosition?: ProjectEditorRevealPosition;
  blameEnabled?: boolean;
  blameHunks?: ProjectBlameHunk[];
  sqlCompletions?: SqlCompletionItem[];
  viewState?: ProjectEditorViewState;
  readonly?: boolean;
  languagePath?: string;
  /**
   * Keep the Monaco model as the source of truth while typing: content is emitted via
   * `sync-content` (debounced, or on `flushContent()`) instead of `update:modelValue` per
   * keystroke, and dirtiness is reported via `dirty-change` against `savedContentFor`.
   */
  deferContentSync?: boolean;
  savedContentFor?: (path: string) => string | undefined;
  /**
   * `editor.kind` for keybinding when clauses (Monaco context key
   * `mincodeEditorKind`). `sql` editors receive `database:run-query` as `run`.
   */
  kind?: MonacoEditorKind;
  /**
   * Extra plugin menu context (contributes.menus editor / SQL editor items):
   * for example the SQL database kind and name. The project-relative path is
   * derived from `path` and `projectRoot`.
   */
  menuContext?: MonacoMenuContext;
  /** Breakpoints of `path` (stored lines). Debug props apply to `kind === "project"` only. */
  breakpoints?: SourceBreakpoint[];
  /** Line → verified by a running session; lines without a result are absent. */
  verifiedLines?: Record<number, boolean>;
  /** Stored line → the debugger's details shown in the breakpoint hover. */
  breakpointDetails?: Record<number, DebugBreakpointHoverDetails>;
  /** Line of the selected paused frame when it is in this file. */
  pausedLine?: number | null;
  entryPoints?: DebugEntryPointMarker[];
  entryPointStates?: Record<number, DebugEntryPointState>;
  /** Variables of the paused frame, shown inline above `pausedLine`; null when not paused. */
  inlineVariables?: { name: string; value: string }[] | null;
}>();
const emit = defineEmits<{
  "update:modelValue": [value: string];
  save: [];
  /** Primary action of the editor kind (SQL: run query), bound by the active keymap. */
  run: [];
  "reveal-complete": [];
  "history-state": [state: { canUndo: boolean; canRedo: boolean }];
  "blame-select": [hunk: ProjectBlameHunk];
  "view-state": [path: string, state: ProjectEditorViewState];
  "sync-content": [path: string, value: string];
  "dirty-change": [path: string, dirty: boolean];
  "model-edit": [path: string];
  "editor-departure": [path: string];
  ready: [];
  "toggle-breakpoint": [line: number];
  /** Edit a breakpoint's conditions/message, with optional logpoint focus. */
  "edit-breakpoint": [line: number, anchor: DebugAnchor, intent?: "logpoint"];
  "set-breakpoint-enabled": [line: number, enabled: boolean];
  "remove-breakpoint": [line: number];
  "entry-point-click": [line: number, anchor: DebugAnchor];
  /** Breakpoints of `path` follow edits: stored line → current line, reported when the file is saved. */
  "breakpoints-moved": [moves: BreakpointMove[], path: string];
}>();

const { t } = useVueI18n();

const CONTENT_SYNC_DELAY_MS = 300;

interface DeferredModelState {
  model: monaco.editor.ITextModel;
  syncedValue: string;
  syncedVersion: number;
  /** Alternative version id whose content equals the saved content, if known. */
  cleanVersion: number | null;
  cleanValue?: string;
  pending: boolean;
  listener: monaco.IDisposable;
}

const host = ref<HTMLElement>();
/** Status line of an editor mode (Vim); stays hidden unless a mode attaches. */
const modeStatus = ref<HTMLElement>();
const ownedModels = new Map<string, monaco.editor.ITextModel>();
let editor: monaco.editor.IStandaloneCodeEditor | null = null;
let keymapRegistration: MonacoEditorRegistration | null = null;
let resizeObserver: ResizeObserver | null = null;
let blameDecorations: monaco.editor.IEditorDecorationsCollection | null = null;
let blameMouseDisposable: monaco.IDisposable | null = null;
let debugDecorations: ReturnType<typeof useEditorDebugDecorations> | null = null;
const isProjectEditor = computed(() => props.kind === "project");
/** Glyph-margin context menu (project editors). */
/**
 * `line` is the shown line; `breakpointLine` / `entryLine` are the stored
 * breakpoint line and entry lookup line shown there (they differ while the
 * file has unsaved edits above them).
 */
const glyphMenu = ref<{ line: number; breakpointLine: number | null; entryLine: number | null; x: number; y: number } | null>(null);
const glyphMenuBreakpoint = computed(() => {
  const line = glyphMenu.value?.breakpointLine;
  return line == null ? undefined : props.breakpoints?.find((bp) => bp.line === line);
});
let sqlCompletionDisposable: monaco.IDisposable | null = null;
// SQL files opened outside the database workspace (project files, SFTP, ...) have no
// schema context; keywords and snippets still complete while the language server
// (sqls) only knows tables once a database connection is configured.
const fallbackSqlCompletions = buildSqlCompletionItems({});
let applyingExternalValue = false;
let currentTheme = resolveMonacoEditorTheme(readStoredUiTheme());
let viewStateTimer: ReturnType<typeof setTimeout> | undefined;
const lastValidViewStates = new Map<string, ProjectEditorViewState>();
const deferredStates = new Map<string, DeferredModelState>();
let contentSyncTimer: ReturnType<typeof setTimeout> | undefined;
function detectLanguage(path: string, value: string) {
  return detectMonacoEditorLanguage(monaco, path, value);
}

/** Plugin languages changed: re-detect every model this editor owns. */
function handlePluginLanguagesChanged() {
  for (const [path, model] of ownedModels) {
    if (model.isDisposed()) continue;
    const languagePath = path === props.path ? props.languagePath || path : path;
    const language = detectLanguage(languagePath, model.getLineCount() > 0 ? model.getLineContent(1) : "");
    if (language !== model.getLanguageId()) monaco.editor.setModelLanguage(model, language);
  }
}

function updateTheme(theme: UiTheme) {
  const nextTheme = resolveMonacoEditorTheme(theme);
  if (nextTheme === currentTheme) return;
  currentTheme = nextTheme;
  monaco.editor.setTheme(nextTheme);
}

function handleThemeChange(event: Event) {
  if (!(event instanceof CustomEvent) || typeof event.detail !== "string") return;
  updateTheme(event.detail as UiTheme);
}


/** Project-relative path of the edited file for plugin menu context, if the file is inside `projectRoot`. */
function menuRelativePath(): string | undefined {
  const path = props.path.replace(/\\/g, "/");
  if (!props.projectRoot) return undefined;
  const isAbsolute = path.startsWith("/") || /^[A-Za-z]:\//.test(path);
  if (!isAbsolute) return path.replace(/^\/+/, "") || undefined;
  const root = props.projectRoot.replace(/\\/g, "/").replace(/\/+$/, "");
  return path.startsWith(`${root}/`) ? path.slice(root.length + 1) : undefined;
}

function modelUri(path: string) {
  return projectEditorModelUri(path, props.projectRoot);
}

function ensureModel(path: string, value: string) {
  const uri = modelUri(path);
  const existing = ownedModels.get(path);
  if (existing?.uri.toString() === uri.toString()) return existing;
  if (existing) {
    releaseMonacoModel(existing);
    ownedModels.delete(path);
  }
  let created = false;
  const model = acquireMonacoModel(uri, () => {
    created = true;
    return monaco.editor.createModel(value, detectLanguage(props.languagePath || path, value), uri);
  });
  ownedModels.set(path, model);
  trackDeferredModel(path, model, created ? value : undefined);
  return model;
}

function releaseClosedModels(openPaths: readonly string[]) {
  const retainedPaths = new Set(openPaths);
  for (const [path, model] of ownedModels) {
    if (retainedPaths.has(path) || editor?.getModel() === model) continue;
    untrackDeferredModel(path);
    releaseMonacoModel(model);
    ownedModels.delete(path);
    lastValidViewStates.delete(path);
  }
}

function trackDeferredModel(path: string, model: monaco.editor.ITextModel, initialValue?: string) {
  if (!props.deferContentSync) return;
  const existing = deferredStates.get(path);
  if (existing?.model === model) return;
  if (existing) untrackDeferredModel(path);
  const state: DeferredModelState = {
    model,
    // Reusing the caller's string keeps later comparisons against it a pointer check.
    syncedValue: initialValue ?? model.getValue(),
    syncedVersion: model.getAlternativeVersionId(),
    cleanVersion: null,
    pending: false,
    listener: model.onDidChangeContent(() => {
      if (applyingExternalValue) return;
      // O(1) per keystroke: compare version ids instead of copying the document.
      state.pending = true;
      emit(
        "dirty-change",
        path,
        state.cleanVersion === null || model.getAlternativeVersionId() !== state.cleanVersion
      );
      emit("model-edit", path);
      scheduleContentSync();
    }),
  };
  deferredStates.set(path, state);
  reconcileDirty(path);
}

function untrackDeferredModel(path: string) {
  const state = deferredStates.get(path);
  if (!state) return;
  syncModelContent(path, state);
  state.listener.dispose();
  deferredStates.delete(path);
}

function syncModelContent(path: string, state: DeferredModelState) {
  if (!state.pending) return;
  state.pending = false;
  if (state.model.isDisposed()) return;
  state.syncedValue = state.model.getValue();
  state.syncedVersion = state.model.getAlternativeVersionId();
  emit("sync-content", path, state.syncedValue);
  if (props.savedContentFor?.(path) === state.syncedValue) state.cleanVersion = state.syncedVersion;
}

function scheduleContentSync() {
  if (contentSyncTimer) clearTimeout(contentSyncTimer);
  contentSyncTimer = setTimeout(flushContent, CONTENT_SYNC_DELAY_MS);
}

/** Emit any edits not yet reported via `sync-content`. */
function flushContent() {
  if (contentSyncTimer) clearTimeout(contentSyncTimer);
  contentSyncTimer = undefined;
  for (const [path, state] of deferredStates) syncModelContent(path, state);
}

/** Re-derive the clean baseline after a load, save or external reload. */
function reconcileDirty(path: string) {
  const state = deferredStates.get(path);
  const saved = props.savedContentFor?.(path);
  if (!state || saved === undefined) return;
  syncModelContent(path, state);
  const clean = state.syncedValue === saved;
  if (clean) state.cleanVersion = state.syncedVersion;
  else if (state.cleanValue !== saved) state.cleanVersion = null;
  state.cleanValue = saved;
  emit("dirty-change", path, !clean);
}

/** Returns whether the model had to be reconciled with an externally provided value. */
function applyExternalValue(path: string, model: monaco.editor.ITextModel, value: string) {
  const state = deferredStates.get(path);
  // Values we emitted ourselves come back as the same string, so this is usually a pointer check.
  if (state && value === state.syncedValue) return false;
  const changed = model.getValue() !== value;
  if (changed) {
    applyingExternalValue = true;
    model.setValue(value);
    applyingExternalValue = false;
  }
  if (!state) return changed;
  state.syncedValue = value;
  state.syncedVersion = model.getAlternativeVersionId();
  state.pending = false;
  reconcileDirty(path);
  return true;
}

function emitHistoryState() {
  const model = editor?.getModel();
  emit("history-state", {
    canUndo: model?.canUndo() ?? false,
    canRedo: model?.canRedo() ?? false,
  });
}

function undo() {
  const model = editor?.getModel();
  if (!editor || !model?.canUndo()) return;
  editor.focus();
  editor.trigger("app.toolbar", "undo", null);
}

function redo() {
  const model = editor?.getModel();
  if (!editor || !model?.canRedo()) return;
  editor.focus();
  editor.trigger("app.toolbar", "redo", null);
}

function toMonacoCompletionKind(kind: SqlCompletionItem["kind"]) {
  const kinds = monaco.languages.CompletionItemKind;
  if (kind === "table") return kinds.Struct;
  if (kind === "column") return kinds.Field;
  if (kind === "snippet") return kinds.Snippet;
  return kinds.Keyword;
}

function registerSqlCompletions() {
  sqlCompletionDisposable = monaco.languages.registerCompletionItemProvider("sql", {
    triggerCharacters: [" ", "."],
    provideCompletionItems(model, position) {
      if (model !== editor?.getModel()) return { suggestions: [] };
      const word = model.getWordUntilPosition(position);
      const range = new monaco.Range(
        position.lineNumber,
        word.startColumn,
        position.lineNumber,
        word.endColumn
      );
      return {
        suggestions: (props.sqlCompletions ?? fallbackSqlCompletions).map((item) => ({
          label: item.label,
          insertText: item.insertText,
          kind: toMonacoCompletionKind(item.kind),
          detail: item.detail,
          documentation: item.documentation,
          sortText: item.sortText,
          range,
          insertTextRules:
            item.kind === "snippet"
              ? monaco.languages.CompletionItemInsertTextRule.InsertAsSnippet
              : undefined,
        })),
      };
    },
  });
}

function preferenceOptions(preferences: MonacoPresentationPreferences): monaco.editor.IEditorOptions & monaco.editor.IGlobalEditorOptions {
  return {
    fontSize: preferences.fontSize,
    tabSize: preferences.tabSize,
    insertSpaces: true,
    wordWrap: preferences.wordWrap,
    minimap: { enabled: preferences.minimap },
    lineNumbers: preferences.lineNumbers,
    formatOnPaste: preferences.formatOnPaste ?? true,
    formatOnType: preferences.formatOnType ?? false,
    renderWhitespace: preferences.renderWhitespace ?? "selection",
    guides: { indentation: preferences.indentationGuides ?? true },
    bracketPairColorization: { enabled: preferences.bracketPairColorization ?? true },
    autoClosingBrackets: "languageDefined",
    autoClosingQuotes: "languageDefined",
    readOnly: props.readonly ?? false,
    stickyScroll: { enabled: preferences.stickyScroll ?? true },
    mouseWheelZoom: true,
    ...(props.sqlCompletions
      ? {
          quickSuggestions: { other: true, comments: false, strings: false },
          suggestOnTriggerCharacters: true,
          tabCompletion: "on" as const,
        }
      : {}),
  };
}

function applyModelPreferences(preferences: MonacoPresentationPreferences) {
  editor?.getModel()?.updateOptions({ tabSize: preferences.tabSize, insertSpaces: true });
}

function applyPreferences(preferences: MonacoPresentationPreferences) {
  editor?.updateOptions(preferenceOptions(preferences));
  applyModelPreferences(preferences);
}

function buildBlameDecorations(hunks: ProjectBlameHunk[]) {
  const colors = new Map<string, number>();
  return hunks.map((hunk) => {
    if (!colors.has(hunk.commit_hash)) colors.set(hunk.commit_hash, colors.size % 8);
    const colorIndex = colors.get(hunk.commit_hash) ?? 0;
    return {
      range: new monaco.Range(hunk.line_start, 1, hunk.line_end, 1),
      options: {
        isWholeLine: true,
        after: {
          content: `  ${hunk.author}, ${hunk.date} • ${hunk.message}`,
          inlineClassName: "blame-gutter-text",
        },
        className: `blame-line-bg blame-bg-${colorIndex}`,
        linesDecorationsClassName: "blame-gutter-decoration",
      },
    };
  });
}

function applyBlameDecorations() {
  if (!editor) return;
  if (!props.blameEnabled || !props.blameHunks?.length) {
    blameDecorations?.clear();
    blameDecorations = null;
    return;
  }
  const decorations = buildBlameDecorations(props.blameHunks);
  if (!blameDecorations) blameDecorations = editor.createDecorationsCollection(decorations);
  else blameDecorations.set(decorations);
}

/** Escapes Markdown in user text shown in a glyph hover. */
function plainMarkdown(text: string) {
  return text.replace(/[\\`*_{}[\]()#+\-.!|<>~&]/g, "\\$&");
}

function debugLabels(): DebugDecorationLabels {
  return {
    breakpoint: (bp, verified, details) => {
      const logMessage = normalizeLogMessage(bp.logMessage);
      const logging = logMessage !== undefined;
      const lines = [
        !bp.enabled
          ? t(logging ? "debug.editor.logpointDisabled" : "debug.editor.breakpointDisabled")
          : verified === false
            ? t(logging ? "debug.editor.logpointUnverified" : "debug.editor.breakpointUnverified")
            : t(logging ? "debug.editor.logpoint" : "debug.editor.breakpoint"),
      ];
      if (bp.enabled && verified === false && details?.message) {
        const reason = details.message === LOGPOINT_UNSUPPORTED_REASON ? t("debug.editor.logpointsUnsupported") : details.message;
        lines.push(t("debug.editor.unverifiedReasonHover", { message: plainMarkdown(reason) }));
      }
      if (bp.enabled && details?.actualLine !== undefined) lines.push(t(logging ? "debug.editor.logpointActualLineHover" : "debug.editor.actualLineHover", { line: details.actualLine }));
      if (bp.condition) lines.push(t("debug.editor.conditionHover", { condition: plainMarkdown(bp.condition) }));
      if (bp.hitCondition) lines.push(t("debug.editor.hitConditionHover", { hitCondition: plainMarkdown(bp.hitCondition) }));
      if (logMessage !== undefined) lines.push(t("debug.editor.logMessageHover", { message: plainMarkdown(logMessage) }));
      return lines.join("\n\n");
    },
    paused: () => t("debug.editor.pausedHere"),
    entryPoint: (label, state) => {
      const hover = t("debug.editor.entryPointHover", { label: plainMarkdown(label) });
      return state ? `${hover}\n\n${t("debug.editor.entryPointLastResult", { status: t(`pluginContrib.testProviders.status.${state}`) })}` : hover;
    },
  };
}

function refreshDebugDecorations() {
  debugDecorations?.refresh();
}

function closeGlyphMenu() {
  glyphMenu.value = null;
}

function glyphMenuAction(action: "edit" | "log" | "toggle" | "enable" | "remove" | "entry") {
  const menu = glyphMenu.value;
  if (!menu) return;
  // Resolve the stored breakpoint before closing the menu invalidates this
  // computed target (the shown line may have moved after unsaved edits).
  const existing = glyphMenuBreakpoint.value;
  glyphMenu.value = null;
  const anchor = { x: menu.x, y: menu.y };
  if (action === "entry") {
    if (menu.entryLine !== null) emit("entry-point-click", menu.entryLine, anchor);
    return;
  }
  if (existing) {
    if (action === "log") emit("edit-breakpoint", existing.line, anchor, "logpoint");
    else if (action === "edit") emit("edit-breakpoint", existing.line, anchor);
    else if (action === "enable") emit("set-breakpoint-enabled", existing.line, !existing.enabled);
    else emit("remove-breakpoint", existing.line);
    return;
  }
  const line = debugDecorations?.newBreakpointLine(menu.line) ?? menu.line;
  if (action === "log") emit("edit-breakpoint", line, anchor, "logpoint");
  else if (action === "edit") emit("edit-breakpoint", line, anchor);
  else if (action === "toggle") emit("toggle-breakpoint", line);
}

function hasUsableEditorLayout() {
  if (!editor || !host.value?.isConnected) return false;
  const layout = editor.getLayoutInfo();
  return (
    host.value.getClientRects().length > 0 &&
    host.value.clientWidth > 0 &&
    host.value.clientHeight > 0 &&
    layout.width > 0 &&
    layout.height > 0
  );
}

function currentViewState(): ProjectEditorViewState | null {
  if (!hasUsableEditorLayout()) return null;
  const selection = editor?.getSelection();
  const position = editor?.getPosition();
  if (!editor || !selection || !position) return null;
  return {
    lineNumber: position.lineNumber,
    column: position.column,
    selectionStartLineNumber: selection.selectionStartLineNumber,
    selectionStartColumn: selection.selectionStartColumn,
    positionLineNumber: selection.positionLineNumber,
    positionColumn: selection.positionColumn,
    scrollTop: editor.getScrollTop(),
    scrollLeft: editor.getScrollLeft(),
  };
}

function rememberCurrentViewState(path = props.path) {
  const state = currentViewState();
  if (state) lastValidViewStates.set(path, state);
  return state;
}

function sameViewState(
  left: ProjectEditorViewState | null | undefined,
  right: ProjectEditorViewState | null | undefined
) {
  return Boolean(
    left &&
      right &&
      left.lineNumber === right.lineNumber &&
      left.column === right.column &&
      left.selectionStartLineNumber === right.selectionStartLineNumber &&
      left.selectionStartColumn === right.selectionStartColumn &&
      left.positionLineNumber === right.positionLineNumber &&
      left.positionColumn === right.positionColumn &&
      left.scrollTop === right.scrollTop &&
      left.scrollLeft === right.scrollLeft
  );
}

function captureViewState(path = props.path) {
  if (viewStateTimer) clearTimeout(viewStateTimer);
  viewStateTimer = undefined;
  const state = rememberCurrentViewState(path) ?? lastValidViewStates.get(path);
  if (state && (path !== props.path || !sameViewState(state, props.viewState))) {
    emit("view-state", path, state);
  }
}

function scheduleViewStateCapture() {
  rememberCurrentViewState();
  if (viewStateTimer) clearTimeout(viewStateTimer);
  viewStateTimer = setTimeout(() => captureViewState(), 120);
}

function restoreViewState(state: ProjectEditorViewState | undefined) {
  if (!editor || !state) return;
  const layout = editor.getLayoutInfo();
  const maxScrollTop = Math.max(0, editor.getScrollHeight() - Math.max(0, layout.height));
  const maxScrollLeft = Math.max(0, editor.getScrollWidth() - Math.max(0, layout.width));
  const restoredState = {
    ...state,
    scrollTop: Math.min(state.scrollTop, maxScrollTop),
    scrollLeft: Math.min(state.scrollLeft, maxScrollLeft),
  };
  lastValidViewStates.set(props.path, restoredState);
  if (sameViewState(currentViewState(), restoredState)) return;
  editor.setSelection(
    new monaco.Selection(
      restoredState.selectionStartLineNumber,
      restoredState.selectionStartColumn,
      restoredState.positionLineNumber,
      restoredState.positionColumn
    )
  );
  editor.setScrollPosition({
    scrollTop: restoredState.scrollTop,
    scrollLeft: restoredState.scrollLeft,
  });
}

function handleUiSessionCapture() {
  captureViewState();
}

function switchDocument(path: string, value: string) {
  if (!editor) return;
  const model = ensureModel(path, value);
  if (!applyExternalValue(path, model, value)) reconcileDirty(path);
  monaco.editor.setModelLanguage(model, detectLanguage(props.languagePath || path, value));
  editor.setModel(model);
  releaseClosedModels(props.openPaths ?? [path]);
  restoreViewState(props.viewState);
  applyPreferences(props.preferences);
  applyBlameDecorations();
  refreshDebugDecorations();
  emitHistoryState();
  editor.focus();
}

function revealRequestedPosition(position: ProjectEditorRevealPosition | undefined) {
  if (!editor || !position || position.line < 1) return;
  const model = editor.getModel();
  if (!model) return;
  const lineNumber = Math.min(position.line, model.getLineCount());
  const column = Math.min(
    Math.max(position.column ?? 1, 1),
    model.getLineMaxColumn(lineNumber),
  );
  editor.revealPositionInCenter({ lineNumber, column });
  editor.setPosition({ lineNumber, column });
  editor.focus();
  emit("reveal-complete");
}

onMounted(() => {
  if (!host.value) return;
  editor = monaco.editor.create(host.value, {
    model: ensureModel(props.path, props.modelValue),
    theme: currentTheme,
    automaticLayout: true,
    // Pass the preferences at creation: updating them afterwards lays out a large file twice.
    ...preferenceOptions(props.preferences),
    // Standalone 主题默认关闭语义着色；LSP 令牌已映射到主题规则作用域。
    "semanticHighlighting.enabled": true,
    mouseWheelZoom: true,
    ...(props.sqlCompletions
      ? {
          quickSuggestions: { other: true, comments: false, strings: false },
          suggestOnTriggerCharacters: true,
          tabCompletion: "on" as const,
        }
      : {}),
    scrollBeyondLastLine: false,
    // 悬停/“找不到定义”等溢出浮层挂到窗口级宿主，避免被顶部导航遮挡。
    overflowWidgetsDomNode: getMonacoOverflowWidgetsHost(),
    fixedOverflowWidgets: true,
    hover: { above: false },
    padding: { top: 10, bottom: 10 },
    // Breakpoints, the paused arrow and entry points live in the glyph margin.
    ...debugEditorOptions(props.kind),
  });
  // 内置 TS/JS 富能力已在 monaco-runtime 统一关闭，智能能力全部来自后端 LSP。
  registerSqlCompletions();
  editor.onDidChangeModelContent(() => {
    if (!applyingExternalValue && !props.deferContentSync) {
      emit("update:modelValue", editor?.getValue() ?? "");
    }
    emitHistoryState();
  });
  editor.onDidChangeCursorSelection(scheduleViewStateCapture);
  editor.onDidScrollChange(scheduleViewStateCapture);
  // Save stays available as an action; its key comes from the active keymap
  // (`editor:save` → `mincode.editor.save`, see services/monaco-keymap.ts).
  editor.addAction({
    id: "app.save-file",
    label: "Save",
    run: () => emit("save"),
  });
  applyModelPreferences(props.preferences);
  editor.onDidBlurEditorWidget(() => emit("editor-departure", props.path));
  ensureMonacoKeymap();
  keymapRegistration = registerMonacoEditor(editor, {
    kind: props.kind ?? "text",
    handlers: { save: () => emit("save"), run: () => emit("run") },
    statusElement: () => modeStatus.value,
    menuContext: () => {
      const relativePath = menuRelativePath();
      return { ...(relativePath ? { relativePath } : {}), ...props.menuContext };
    },
  });
  blameMouseDisposable = editor.onMouseDown((event) => {
    if (!props.blameEnabled) return;
    // Glyph-margin clicks of a project editor are breakpoint clicks.
    if (isProjectEditor.value && event.target.type === monaco.editor.MouseTargetType.GUTTER_GLYPH_MARGIN) return;
    const line = event.target.position?.lineNumber ?? event.target.range?.startLineNumber;
    const hunk = props.blameHunks?.find(
      (candidate) => line && line >= candidate.line_start && line <= candidate.line_end
    );
    if (hunk) emit("blame-select", hunk);
  });
  debugDecorations = useEditorDebugDecorations({
    editor,
    enabled: () => isProjectEditor.value,
    path: () => props.path,
    modelFor: (path) => ownedModels.get(path),
    state: () => ({
      breakpoints: props.breakpoints ?? [],
      verifiedLines: props.verifiedLines,
      breakpointDetails: props.breakpointDetails,
      pausedLine: props.pausedLine,
      entryPoints: props.entryPoints,
      entryPointStates: props.entryPointStates,
      inlineVariables: props.inlineVariables,
    }),
    labels: debugLabels,
    glyphMarginTarget: monaco.editor.MouseTargetType.GUTTER_GLYPH_MARGIN,
    stickiness: monaco.editor.TrackedRangeStickiness.NeverGrowsWhenTypingAtEdges,
    onToggle: (line) => emit("toggle-breakpoint", line),
    onEntryClick: (line, anchor) => emit("entry-point-click", line, anchor),
    onGlyphContextMenu: (line, anchor) => {
      glyphMenu.value = { line, ...debugDecorations!.lineInfo(line), ...anchor };
    },
    onMoved: (moves, path) => emit("breakpoints-moved", moves, path),
  });
  resizeObserver = new ResizeObserver(([entry]) => {
    if (!editor || !entry || entry.contentRect.width <= 0 || entry.contentRect.height <= 0) return;
    editor.layout({ width: entry.contentRect.width, height: entry.contentRect.height });
  });
  resizeObserver.observe(host.value);
  window.addEventListener(UI_THEME_CHANGED_EVENT, handleThemeChange);
  window.addEventListener(UI_SESSION_CAPTURE_EVENT, handleUiSessionCapture);
  window.addEventListener(PLUGIN_LANGUAGES_CHANGED_EVENT, handlePluginLanguagesChanged);
  applyBlameDecorations();
  refreshDebugDecorations();
  restoreViewState(props.viewState);
  emitHistoryState();
  revealRequestedPosition(props.revealPosition);
  emit("ready");
});

watch(
  () => [props.path, props.modelValue, props.projectRoot, props.languagePath] as const,
  ([path, value, projectRoot, languagePath], previous) => {
    if (!previous || path !== previous[0] || projectRoot !== previous[2]) {
      if (previous?.[0]) {
        captureViewState(previous[0]);
        emit("editor-departure", previous[0]);
      }
      flushContent();
      switchDocument(path, value);
      return;
    }
    const model = editor?.getModel();
    if (model && languagePath !== previous[3]) {
      monaco.editor.setModelLanguage(model, detectLanguage(languagePath || path, value));
    }
    if (!model || !applyExternalValue(path, model, value)) return;
    emitHistoryState();
  }
);

watch(
  () => (props.deferContentSync ? props.savedContentFor?.(props.path) : undefined),
  (saved) => {
    if (saved !== undefined) reconcileDirty(props.path);
  }
);

watch(
  () => [props.openPaths, props.path] as const,
  ([openPaths, path]) => releaseClosedModels(openPaths ?? [path]),
  { deep: true, flush: "post" }
);

watch(
  () => props.preferences,
  (preferences) => applyPreferences(preferences),
  { deep: true }
);

watch(
  () => props.kind,
  (kind) => {
    keymapRegistration?.setKind(kind ?? "text");
    const debugOptions = debugEditorOptions(kind);
    if (Object.keys(debugOptions).length) editor?.updateOptions(debugOptions);
    refreshDebugDecorations();
  }
);

// Each prop is replaced as a whole by the workspace, so a shallow watch suffices.
watch(
  () => [props.breakpoints, props.verifiedLines, props.breakpointDetails, props.pausedLine, props.entryPoints, props.entryPointStates, props.inlineVariables] as const,
  () => refreshDebugDecorations()
);

watch(
  () => props.readonly,
  (readonly) => editor?.updateOptions({ readOnly: readonly ?? false })
);

watch(
  () => props.viewState,
  (state) => restoreViewState(state),
  { deep: true }
);

// blameHunks 只会被整体替换，浅监听即可；深监听会在大文件上遍历全部 hunk。
watch(
  () => [props.blameEnabled, props.blameHunks] as const,
  () => applyBlameDecorations()
);

watch(
  () => props.revealPosition,
  (position) => revealRequestedPosition(position),
  { deep: true },
);

onBeforeUnmount(() => {
  captureViewState();
  flushContent();
  for (const path of [...deferredStates.keys()]) untrackDeferredModel(path);
  resizeObserver?.disconnect();
  blameMouseDisposable?.dispose();
  debugDecorations?.dispose();
  debugDecorations = null;
  sqlCompletionDisposable?.dispose();
  blameDecorations?.clear();
  window.removeEventListener(UI_THEME_CHANGED_EVENT, handleThemeChange);
  window.removeEventListener(UI_SESSION_CAPTURE_EVENT, handleUiSessionCapture);
  window.removeEventListener(PLUGIN_LANGUAGES_CHANGED_EVENT, handlePluginLanguagesChanged);
  keymapRegistration?.dispose();
  keymapRegistration = null;
  editor?.dispose();
  for (const model of ownedModels.values()) releaseMonacoModel(model);
  ownedModels.clear();
  lastValidViewStates.clear();
});

async function prepareSave(path: string, preferences: ProjectEditorPreferences, context: EditorSaveContext) {
  const model = ownedModels.get(path);
  if (!model || model.isDisposed() || props.readonly || context.isCurrent?.() === false) return;
  await formatProjectModelOnSave(monaco, model, context);
  if (model.isDisposed() || context.signal?.aborted || context.isCurrent?.() === false) return;
  const text = model.getValue();
  const cleaned = cleanSavedText(text, preferences);
  if (cleaned !== text) {
    model.pushStackElement();
    model.pushEditOperations([], [{ range: model.getFullModelRange(), text: cleaned }], () => null);
    model.pushStackElement();
  }
  flushContent();
}

function hasModel(path: string) { return Boolean(ownedModels.get(path) && !ownedModels.get(path)!.isDisposed()); }
function captureSaveAcknowledgement(path: string): (() => void) | undefined {
  const state = deferredStates.get(path);
  if (!state || state.model.isDisposed()) return;
  const version = state.model.getAlternativeVersionId();
  const content = state.syncedValue;
  return () => {
    if (deferredStates.get(path) !== state || state.model.isDisposed() || props.savedContentFor?.(path) !== content) return;
    state.cleanVersion = version;
    state.cleanValue = content;
    reconcileDirty(path);
  };
}
/**
 * Breakpoint moves of `path` at the content about to be written; call the
 * result once the write succeeded to emit `breakpoints-moved`.
 */
function captureBreakpointMoves(path: string): (() => void) | undefined {
  return debugDecorations?.captureMoves(path);
}
/** Monaco language id of an open path's model. */
function languageIdFor(path: string): string | undefined {
  const model = ownedModels.get(path);
  return model && !model.isDisposed() ? model.getLanguageId() : undefined;
}
/**
 * What Evaluate starts with: the selected text, else the expression at the
 * cursor (with its member chain); "" without either.
 */
function evaluateText(): string {
  const model = editor?.getModel();
  const selection = editor?.getSelection();
  if (!editor || !model || !selection) return "";
  if (!selection.isEmpty()) return model.getValueInRange(selection);
  const position = selection.getPosition();
  const line = model.getLineContent(position.lineNumber);
  // The caret right after a word (`name|`) still means that word.
  return (hoverExpressionAt(line, position.column) ?? hoverExpressionAt(line, position.column - 1))?.expression ?? "";
}
/** The cursor's line (1-based), or null without an editor. */
function cursorLine(): number | null {
  return editor?.getPosition()?.lineNumber ?? null;
}

/**
 * The viewport point just below (line, column) of the shown document, for a
 * popup anchored at that code; null when the line is scrolled out of view or
 * the editor is not laid out (hidden).
 */
function screenPositionOf(line: number, column = 1): { x: number; y: number } | null {
  const node = editor?.getDomNode();
  const model = editor?.getModel();
  if (!editor || !node || !model || line < 1 || line > model.getLineCount()) return null;
  if (!editor.getVisibleRanges().some((range) => line >= range.startLineNumber && line <= range.endLineNumber)) return null;
  const rect = node.getBoundingClientRect();
  if (rect.width === 0 && rect.height === 0) return null;
  const position = editor.getScrolledVisiblePosition({ lineNumber: line, column: Math.min(Math.max(1, column), model.getLineMaxColumn(line)) });
  if (!position) return null;
  return { x: rect.left + position.left, y: rect.top + position.top + position.height };
}

/** Moves the keyboard focus into the editor (e.g. back from a dialog). */
function focus() {
  editor?.focus();
}

defineExpose({
  cursorLine,
  screenPositionOf,
  focus,
  undo,
  redo,
  captureViewState,
  flushContent,
  prepareSave,
  hasModel,
  captureSaveAcknowledgement,
  captureBreakpointMoves,
  languageIdFor,
  evaluateText,
});
</script>

<template>
  <div class="flex h-full min-h-0 min-w-0 flex-1 flex-col">
    <div ref="host" class="min-h-0 min-w-0 flex-1" />
    <div ref="modeStatus" class="app-vim-status" hidden />
    <AppContextMenu
      v-if="glyphMenu"
      :open="true"
      :x="glyphMenu.x"
      :y="glyphMenu.y"
      :width="200"
      @close="closeGlyphMenu"
    >
      <template v-if="glyphMenuBreakpoint">
        <el-dropdown-item @click="glyphMenuAction('edit')">{{ t(normalizeLogMessage(glyphMenuBreakpoint.logMessage) !== undefined ? "debug.editor.editLogpoint" : "debug.editor.editCondition") }}</el-dropdown-item>
        <el-dropdown-item @click="glyphMenuAction('enable')">
          {{ glyphMenuBreakpoint.enabled ? t("debug.editor.disableBreakpoint") : t("debug.editor.enableBreakpoint") }}
        </el-dropdown-item>
        <el-dropdown-item @click="glyphMenuAction('remove')">{{ t("debug.editor.removeBreakpoint") }}</el-dropdown-item>
      </template>
      <template v-else>
        <el-dropdown-item @click="glyphMenuAction('toggle')">{{ t("debug.editor.addBreakpoint") }}</el-dropdown-item>
        <el-dropdown-item @click="glyphMenuAction('edit')">{{ t("debug.editor.addConditionalBreakpoint") }}</el-dropdown-item>
        <el-dropdown-item @click="glyphMenuAction('log')">{{ t("debug.editor.addLogpoint") }}</el-dropdown-item>
      </template>
      <el-dropdown-item v-if="glyphMenu.entryLine !== null" divided @click="glyphMenuAction('entry')">
        {{ t("debug.editor.runOrDebug") }}
      </el-dropdown-item>
    </AppContextMenu>
  </div>
</template>
