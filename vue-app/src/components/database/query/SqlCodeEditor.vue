<script setup lang="ts">
// The database query editor (CodeMirror 6 + @codemirror/lang-sql). It replaces Monaco here
// only: a SQL tab needs highlighting, completion of tables and columns, select-and-run and a
// few keys, and CodeMirror creates in milliseconds with a small memory footprint, while the
// rest of the app (project files, diffs, read-only viewers) keeps Monaco.
//
// Contract kept from MonacoTextEditor for the query workspace:
// - `update:modelValue` reports edits; a modelValue from the parent is applied without
//   reporting back and without entering the undo history;
// - `view-state` reports cursor, selection and scroll (1-based, Monaco's shape) so the tab's
//   select-and-run and restore keep working; `viewState` restores them when the tab changes;
// - `run` is ⌘/Ctrl+Enter and ⌘/Ctrl+S, bound in the editor's own keymap. The app keymap
//   counts this editor as editor focus (domain/keybindings/context CODE_EDITOR_SELECTOR) with
//   `editor.kind == "sql"`, so app-wide chords stay out of it the way they do in Monaco.
//
// Colours come from --app-* tokens, so light and dark themes follow without a theme switch.
import { onBeforeUnmount, onMounted, ref, watch } from "vue";
import { Annotation, Compartment, EditorSelection, EditorState, Prec, Transaction } from "@codemirror/state";
import {
  EditorView,
  drawSelection,
  highlightActiveLine,
  highlightActiveLineGutter,
  highlightSpecialChars,
  keymap,
  lineNumbers,
} from "@codemirror/view";
import { defaultKeymap, history, historyKeymap, indentWithTab } from "@codemirror/commands";
import { HighlightStyle, bracketMatching, indentOnInput, syntaxHighlighting } from "@codemirror/language";
import { autocompletion, closeBrackets, closeBracketsKeymap, completionKeymap, snippetCompletion, type Completion, type CompletionContext } from "@codemirror/autocomplete";
import { Cassandra, MSSQL, MariaSQL, MySQL, PLSQL, PostgreSQL, SQLite, StandardSQL, sql, type SQLDialect } from "@codemirror/lang-sql";
import { tags } from "@lezer/highlight";
import type { SqlCompletionItem } from "@/domain/database/sql-completion";
import type { ProjectEditorViewState } from "@/domain/projects/types";
import type { DbType } from "@/domain/database/types";
import { codeMirrorSnippet, selectionOf, sqlEditorDialect, viewStateOf, type SqlEditorDialect, type TextLines } from "@/domain/database/sql-code-editor";
import { registerKeybindingContext } from "@/domain/keybindings/context";
import type { EditorMenuTarget } from "@/domain/plugins/menus";
import { useVueI18n } from "@/i18n/messages";
import { copyText, readText } from "@/services/desktop-bridge";
import SqlEditorContextMenu, { type SqlEditorMenuAction } from "@/components/database/query/SqlEditorContextMenu.vue";

const props = defineProps<{
  modelValue: string;
  /** Identifies the query tab: a new path is a new document (fresh undo history and view state). */
  path: string;
  viewState?: ProjectEditorViewState;
  /** Table, column, keyword and snippet completions (domain/database/sql-completion). */
  sqlCompletions?: SqlCompletionItem[];
  /** Connection type: the SQL dialect for highlighting and keywords. */
  dbType?: DbType;
  fontSize: number;
  /** Schema the query runs in, for the plugin menu context (`database.name`). */
  databaseName?: string;
}>();
const emit = defineEmits<{
  (e: "update:modelValue", value: string): void;
  (e: "view-state", path: string, state: ProjectEditorViewState): void;
  (e: "run", mode?: "smart" | "all"): void;
  (e: "format"): void;
}>();
const { t, locale } = useVueI18n();

const host = ref<HTMLElement | null>(null);
let view: EditorView | null = null;

const DIALECTS: Record<SqlEditorDialect, SQLDialect> = { StandardSQL, MySQL, MariaSQL, PostgreSQL, SQLite, MSSQL, PLSQL, Cassandra };

/** Marks a value the parent pushed down, so it is not reported back as an edit. */
const external = Annotation.define<boolean>();
const language = new Compartment();
const fontSizeSlot = new Compartment();
const localized = new Compartment();

const highlight = HighlightStyle.define([
  { tag: [tags.keyword, tags.operatorKeyword, tags.modifier], color: "var(--app-accent)", fontWeight: "600" },
  { tag: [tags.string, tags.special(tags.string)], color: "var(--app-success)" },
  { tag: [tags.number, tags.bool, tags.null], color: "var(--app-warning)" },
  { tag: [tags.lineComment, tags.blockComment], color: "var(--app-text-muted)", fontStyle: "italic" },
  { tag: [tags.typeName, tags.standard(tags.name)], color: "var(--app-warning)" },
  { tag: [tags.special(tags.name), tags.quote], color: "var(--app-accent)" },
  { tag: tags.invalid, color: "var(--el-color-danger)" },
]);

const theme = EditorView.theme({
  "&": { height: "100%", color: "var(--app-text)", backgroundColor: "var(--app-surface)" },
  "&.cm-focused": { outline: "none" },
  ".cm-scroller": { fontFamily: "var(--app-font-mono)", lineHeight: "1.6", overflow: "auto" },
  ".cm-content": { caretColor: "var(--app-accent)", padding: "10px 0" },
  ".cm-line": { padding: "0 12px 0 6px" },
  ".cm-gutters": {
    backgroundColor: "var(--app-surface)",
    color: "var(--app-text-muted)",
    border: "none",
    borderRight: "1px solid var(--app-border)",
  },
  ".cm-activeLine": { backgroundColor: "color-mix(in srgb, var(--app-accent) 6%, transparent)" },
  ".cm-activeLineGutter": { backgroundColor: "color-mix(in srgb, var(--app-accent) 10%, transparent)", color: "var(--app-text)" },
  ".cm-cursor, .cm-dropCursor": { borderLeftColor: "var(--app-accent)" },
  "&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection": {
    backgroundColor: "color-mix(in srgb, var(--app-accent) 28%, transparent)",
  },
  ".cm-matchingBracket": { backgroundColor: "color-mix(in srgb, var(--app-accent) 18%, transparent)", outline: "none" },
  ".cm-tooltip": { border: "1px solid var(--app-border)", backgroundColor: "var(--app-surface)", color: "var(--app-text)" },
  ".cm-tooltip-autocomplete > ul > li[aria-selected]": { backgroundColor: "var(--app-accent-soft)", color: "var(--app-text)" },
  ".cm-completionDetail": { color: "var(--app-text-muted)", fontStyle: "normal", marginLeft: "12px" },
});

const COMPLETION_TYPE: Record<SqlCompletionItem["kind"], string> = { keyword: "keyword", snippet: "text", table: "class", column: "property" };

function toCompletion(item: SqlCompletionItem, rank: number): Completion {
  const base: Completion = {
    label: item.label,
    type: COMPLETION_TYPE[item.kind],
    ...(item.detail ? { detail: item.detail } : {}),
    ...(item.documentation ? { info: item.documentation } : {}),
    // Keep the catalog's order (sortText) among equally good matches.
    boost: -Math.min(rank, 99),
  };
  if (item.kind === "snippet") return snippetCompletion(codeMirrorSnippet(item.insertText), base);
  return item.insertText === item.label ? base : { ...base, apply: item.insertText };
}

let completions: Completion[] = [];
function rebuildCompletions(items: readonly SqlCompletionItem[] | undefined) {
  const sorted = [...(items ?? [])].sort((a, b) => (a.sortText ?? a.label).localeCompare(b.sortText ?? b.label));
  completions = sorted.map((item, index) => toCompletion(item, Math.floor((index / Math.max(1, sorted.length)) * 100)));
}
rebuildCompletions(props.sqlCompletions);

/** Completes catalog items for the word before the cursor (also after "table." for a qualified name). */
function catalogCompletions(context: CompletionContext) {
  const word = context.matchBefore(/[\w$.]+/);
  if (!word && !context.explicit) return null;
  if (word && word.from === word.to && !context.explicit) return null;
  return { from: word ? word.from : context.pos, options: completions, validFor: /^[\w$.]*$/ };
}

function languageExtension(dbType: DbType | undefined) {
  // lang-sql: highlighting and indentation for the dialect. Its own keyword/schema completion is
  // left out: the catalog above already has keywords, tables and columns for this connection.
  return sql({ dialect: DIALECTS[sqlEditorDialect(dbType)], upperCaseKeywords: true }).language;
}
const fontSizeExtension = (size: number) => EditorView.theme({ "&": { fontSize: `${size}px` } });
const localizedExtension = () => EditorView.contentAttributes.of({ "aria-label": t("database.sqlEditor"), spellcheck: "false" });

function textLines(state: EditorState): TextLines {
  const doc = state.doc;
  return {
    lines: doc.lines,
    lineStart: (line) => doc.line(line).from,
    lineLength: (line) => doc.line(line).length,
    lineAt: (offset) => doc.lineAt(offset).number,
  };
}

let reportTimer: ReturnType<typeof setTimeout> | undefined;
/** Reports cursor, selection and scroll; scroll and cursor moves come in bursts, so batch them. */
function scheduleViewState() {
  clearTimeout(reportTimer);
  reportTimer = setTimeout(reportViewState, 80);
}
function reportViewState() {
  clearTimeout(reportTimer);
  reportTimer = undefined;
  if (!view) return;
  const { anchor, head } = view.state.selection.main;
  emit("view-state", props.path, viewStateOf(textLines(view.state), anchor, head, view.scrollDOM.scrollTop, view.scrollDOM.scrollLeft));
}

function run(): boolean {
  // Select-and-run reads the selection from the tab's view state: report it first.
  reportViewState();
  emit("run");
  return true;
}

function createState(doc: string) {
  return EditorState.create({
    doc,
    extensions: [
      lineNumbers(),
      highlightActiveLineGutter(),
      highlightSpecialChars(),
      history(),
      drawSelection(),
      highlightActiveLine(),
      bracketMatching(),
      closeBrackets(),
      indentOnInput(),
      EditorView.lineWrapping,
      language.of(languageExtension(props.dbType)),
      syntaxHighlighting(highlight),
      autocompletion({ override: [catalogCompletions], activateOnTyping: true, icons: false }),
      theme,
      fontSizeSlot.of(fontSizeExtension(props.fontSize)),
      localized.of(localizedExtension()),
      // Run the query (the app keymap's database:run-query chords for the SQL editor).
      Prec.high(keymap.of([
        { key: "Mod-Enter", run },
        { key: "Mod-s", run, preventDefault: true },
      ])),
      keymap.of([...closeBracketsKeymap, ...completionKeymap, ...defaultKeymap, ...historyKeymap, indentWithTab]),
      EditorView.updateListener.of((update) => {
        if (update.docChanged && !update.transactions.some((tr) => tr.annotation(external))) {
          emit("update:modelValue", update.state.doc.toString());
        }
        if (update.selectionSet || update.docChanged) scheduleViewState();
      }),
      EditorView.domEventHandlers({
        scroll: () => scheduleViewState(),
        contextmenu: (event) => {
          openMenu(event);
          return true;
        },
      }),
    ],
  });
}

function restoreViewState(state: ProjectEditorViewState | undefined) {
  if (!view || !state) return;
  const { anchor, head } = selectionOf(textLines(view.state), state);
  view.dispatch({ selection: EditorSelection.single(anchor, head) });
  const scroller = view.scrollDOM;
  requestAnimationFrame(() => {
    scroller.scrollTop = state.scrollTop;
    scroller.scrollLeft = state.scrollLeft;
  });
}

// ---- Context menu (run, format, clipboard, plugin items of sql/editor/context) ----
const menu = ref<{ x: number; y: number; target: EditorMenuTarget } | null>(null);

function menuTarget(): EditorMenuTarget {
  const state = view!.state;
  const { from, to } = state.selection.main;
  const kind = props.dbType;
  return {
    kind: "sql",
    languageId: "sql",
    readonly: false,
    ...(from !== to ? { selection: state.sliceDoc(from, to) } : {}),
    ...(kind ? { database: { kind, ...(props.databaseName ? { name: props.databaseName } : {}) } } : {}),
  };
}

function openMenu(event: MouseEvent) {
  if (!view) return;
  event.preventDefault();
  // A right click outside the selection moves the cursor there first, like an editor does.
  const at = view.posAtCoords({ x: event.clientX, y: event.clientY });
  const { from, to } = view.state.selection.main;
  if (at !== null && (at < from || at > to)) view.dispatch({ selection: { anchor: at } });
  view.focus();
  menu.value = { x: event.clientX, y: event.clientY, target: menuTarget() };
}

function closeMenu() {
  menu.value = null;
}

async function runMenuAction(action: SqlEditorMenuAction) {
  if (!view) return;
  const editor = view;
  const { from, to } = editor.state.selection.main;
  switch (action) {
    case "run":
      run();
      break;
    case "run-all":
      reportViewState();
      emit("run", "all");
      break;
    case "format":
      emit("format");
      break;
    case "copy":
    case "cut":
      if (from === to) break;
      await copyText(editor.state.sliceDoc(from, to));
      if (action === "cut") editor.dispatch({ changes: { from, to }, userEvent: "delete.cut" });
      break;
    case "paste": {
      const text = await readText().catch(() => "");
      if (text) editor.dispatch(editor.state.replaceSelection(text), { userEvent: "input.paste", scrollIntoView: true });
      break;
    }
    case "select-all":
      editor.dispatch({ selection: { anchor: 0, head: editor.state.doc.length } });
      break;
  }
  editor.focus();
}

let disposeContext: (() => void) | undefined;
onMounted(() => {
  view = new EditorView({ parent: host.value!, state: createState(props.modelValue) });
  restoreViewState(props.viewState);
  // `editor.kind == "sql"` for the app keymap while focus is inside this editor.
  disposeContext = registerKeybindingContext("editor.kind", (target) => (target && host.value?.contains(target as unknown as Node) ? "sql" : undefined));
});

// A new query tab: a fresh document, undo history and view state.
watch(
  () => props.path,
  () => {
    if (!view) return;
    view.setState(createState(props.modelValue));
    restoreViewState(props.viewState);
  },
);
watch(
  () => props.modelValue,
  (value) => {
    if (!view || value === view.state.doc.toString()) return;
    view.dispatch({
      changes: { from: 0, to: view.state.doc.length, insert: value },
      annotations: [external.of(true), Transaction.addToHistory.of(false)],
    });
  },
);
watch(() => props.sqlCompletions, rebuildCompletions);
watch(() => props.dbType, (dbType) => view?.dispatch({ effects: language.reconfigure(languageExtension(dbType)) }));
watch(() => props.fontSize, (size) => view?.dispatch({ effects: fontSizeSlot.reconfigure(fontSizeExtension(size)) }));
watch(locale, () => view?.dispatch({ effects: localized.reconfigure(localizedExtension()) }));

onBeforeUnmount(() => {
  if (reportTimer) reportViewState();
  disposeContext?.();
  view?.destroy();
  view = null;
});

defineExpose({
  focus: () => view?.focus(),
  /** The editor view (tests and automation). */
  view: () => view,
});
</script>

<template>
  <div ref="host" class="app-code-editor db-sql-code-editor" data-testid="sql-code-editor" />
  <SqlEditorContextMenu
    v-if="menu"
    :open="true"
    :x="menu.x"
    :y="menu.y"
    :target="menu.target"
    @action="runMenuAction"
    @close="closeMenu"
  />
</template>
