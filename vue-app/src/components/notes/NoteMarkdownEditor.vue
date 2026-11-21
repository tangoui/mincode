<script setup lang="ts">
// 笔记正文的 Markdown 源码编辑器（CodeMirror 6）。宿主页面没有 CSP 限制，编辑器直接挂在组件元素上，
// 颜色全部来自 --app-* token，明暗主题自动跟随。组件只通过 update:modelValue 上报正文，
// 从不直接修改 store 中的共享对象；父组件传回的新值以「外部同步」写入，不会再次上报，也不进入撤销历史。
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
  placeholder,
} from "@codemirror/view";
import { defaultKeymap, history, historyKeymap, indentWithTab } from "@codemirror/commands";
import { HighlightStyle, StreamLanguage, syntaxHighlighting } from "@codemirror/language";
import { tags } from "@lezer/highlight";
import { formatMarkdown, type MarkdownFormatKind } from "@/domain/notes/markdown-format";
import { markdownStream } from "@/domain/notes/markdown-language";
import { useVueI18n } from "@/i18n/messages";
import { noteErrorMessage, putNoteAsset } from "@/services/notes-service";

const props = withDefaults(defineProps<{ modelValue: string; documentKey: string; disabled?: boolean }>(), {
  disabled: false,
});
const emit = defineEmits<{
  (e: "update:modelValue", value: string): void;
  (e: "upload-error", message: string): void;
}>();
const { t, locale } = useVueI18n();

const host = ref<HTMLElement | null>(null);
let view: EditorView | null = null;
// 每次换文档（setState）递增；上传完成时若已换到别的文档，结果不再插入。
let generation = 0;

/** 标记父组件同步下来的值，避免回传为用户编辑。 */
const external = Annotation.define<boolean>();
const editable = new Compartment();
const localized = new Compartment();

const editableExtension = (disabled: boolean) => [EditorState.readOnly.of(disabled), EditorView.editable.of(!disabled)];
const localizedExtension = () => [
  placeholder(t("notes.editor.placeholder")),
  EditorView.contentAttributes.of({ "aria-label": t("notes.editor.ariaLabel"), spellcheck: "false" }),
];
const labels = () => ({ text: t("notes.format.placeholderText"), column: t("notes.format.placeholderColumn") });

const highlight = HighlightStyle.define([
  { tag: tags.heading, color: "var(--app-accent)", fontWeight: "700" },
  { tag: tags.strong, fontWeight: "700" },
  { tag: tags.emphasis, fontStyle: "italic" },
  { tag: [tags.monospace, tags.meta], color: "var(--app-success)" },
  { tag: tags.link, color: "var(--app-accent)", textDecoration: "underline" },
  { tag: [tags.list, tags.quote, tags.contentSeparator], color: "var(--app-warning)", fontWeight: "600" },
]);

const theme = EditorView.theme({
  "&": { height: "100%", color: "var(--app-text)", backgroundColor: "var(--app-surface)", fontSize: "13px" },
  "&.cm-focused": { outline: "none" },
  ".cm-scroller": { fontFamily: "var(--app-font-mono)", lineHeight: "1.8", overflow: "auto" },
  ".cm-content": { caretColor: "var(--app-accent)", padding: "12px 0" },
  ".cm-line": { padding: "0 14px 0 6px" },
  ".cm-gutters": {
    backgroundColor: "var(--app-surface)",
    color: "var(--app-text-muted)",
    border: "none",
    borderRight: "1px solid var(--app-border)",
  },
  ".cm-activeLine": { backgroundColor: "color-mix(in srgb, var(--app-accent) 6%, transparent)" },
  ".cm-activeLineGutter": { backgroundColor: "color-mix(in srgb, var(--app-accent) 10%, transparent)", color: "var(--app-text)" },
  ".cm-cursor, .cm-dropCursor": { borderLeftColor: "var(--app-accent)" },
  ".cm-placeholder": { color: "var(--app-text-muted)" },
  "&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection": {
    backgroundColor: "color-mix(in srgb, var(--app-accent) 28%, transparent)",
  },
});

function apply(kind: MarkdownFormatKind): boolean {
  if (!view || props.disabled) return false;
  const { from, to } = view.state.selection.main;
  const change = formatMarkdown(view.state.doc.toString(), from, to, kind, labels());
  if (!change) return false;
  view.dispatch({
    changes: { from: change.from, to: change.to, insert: change.insert },
    selection: EditorSelection.single(change.anchor, change.head),
    scrollIntoView: true,
    userEvent: "input.format",
  });
  view.focus();
  return true;
}

function imageFiles(list: FileList | File[] | null | undefined): File[] {
  return Array.from(list ?? []).filter((file) => file.type.startsWith("image/"));
}

/**
 * 依次保存图片，在光标处插入引用；失败的图片 emit upload-error。
 * 编辑器只读（disabled）时拒绝；上传完成时已变为只读则既不插入也不上报；
 * 上传期间换了文档或已卸载则不插入（错误仍上报，图片附件已保存，由后端回收）。
 */
/** 图片替代文字：文件名去掉扩展名，去掉会破坏 Markdown 图片语法的 []() 与换行。 */
function imageAlt(file: File) {
  return file.name
    .replace(/\.[^.]*$/, "")
    .replace(/[[\]()]/g, "")
    .replace(/[\r\n]+/g, " ")
    .trim();
}

async function insertImages(files: File[]) {
  if (props.disabled) return;
  const started = generation;
  const images: string[] = [];
  let failure: unknown;
  for (const file of files) {
    try {
      images.push(`![${imageAlt(file)}](${await putNoteAsset(file)})`);
    } catch (error) {
      failure ??= error;
    }
  }
  if (props.disabled) return;
  if (failure !== undefined) emit("upload-error", noteErrorMessage(failure, t));
  if (!view || !images.length || started !== generation) return;
  const insert = images.join("\n");
  const { from, to } = view.state.selection.main;
  view.dispatch({
    changes: { from, to, insert },
    selection: EditorSelection.cursor(from + insert.length),
    scrollIntoView: true,
    userEvent: "input.paste",
  });
}

function insertImage(file: File): Promise<void> {
  return insertImages([file]);
}

const fileHandlers = EditorView.domEventHandlers({
  paste(event) {
    // Office 等应用会同时放入文字和图片：有文字时按文字粘贴，交给 CodeMirror 默认处理。
    if (event.clipboardData?.getData("text/plain")) return false;
    const files = imageFiles(event.clipboardData?.files);
    if (!files.length || props.disabled) return false;
    event.preventDefault();
    void insertImages(files);
    return true;
  },
  drop(event, editorView) {
    const files = imageFiles(event.dataTransfer?.files);
    if (!files.length || props.disabled) return false;
    event.preventDefault();
    const position = editorView.posAtCoords({ x: event.clientX, y: event.clientY });
    if (position !== null) editorView.dispatch({ selection: EditorSelection.cursor(position) });
    editorView.focus();
    void insertImages(files);
    return true;
  },
});

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
      EditorView.lineWrapping,
      StreamLanguage.define(markdownStream),
      syntaxHighlighting(highlight),
      theme,
      localized.of(localizedExtension()),
      Prec.high(
        keymap.of([
          { key: "Mod-b", run: () => apply("bold") },
          { key: "Mod-i", run: () => apply("italic") },
          // Mod-k 是应用的命令面板，链接用 Mod-Shift-k。
          { key: "Mod-Shift-k", run: () => apply("link") },
        ]),
      ),
      keymap.of([...defaultKeymap, ...historyKeymap, indentWithTab]),
      editable.of(editableExtension(props.disabled)),
      fileHandlers,
      EditorView.updateListener.of((update) => {
        if (update.docChanged && !update.transactions.some((tr) => tr.annotation(external))) {
          emit("update:modelValue", update.state.doc.toString());
        }
      }),
    ],
  });
}

onMounted(() => {
  view = new EditorView({ parent: host.value!, state: createState(props.modelValue) });
});

// 换了一篇笔记（或恢复了其他版本）：整体替换状态，撤销无法回到上一篇的内容。
watch(
  () => props.documentKey,
  () => {
    generation += 1;
    view?.setState(createState(props.modelValue));
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
// 忙碌时变为只读会让浏览器失去焦点；恢复后若用户原本在这里输入且焦点没被别处拿走，就还给编辑器。
let refocus = false;
watch(
  () => props.disabled,
  (disabled) => {
    if (!view) return;
    if (disabled) refocus = view.hasFocus;
    view.dispatch({ effects: editable.reconfigure(editableExtension(disabled)) });
    if (!disabled && refocus) {
      refocus = false;
      const active = document.activeElement;
      if (!active || active === document.body || active === host.value) view.focus();
    }
  },
);
watch(locale, () => view?.dispatch({ effects: localized.reconfigure(localizedExtension()) }));
onBeforeUnmount(() => {
  generation += 1;
  view?.destroy();
  view = null;
});

defineExpose({
  focus: () => view?.focus(),
  apply: (kind: MarkdownFormatKind) => {
    apply(kind);
  },
  insertImage,
});
</script>

<template>
  <div ref="host" class="note-markdown-editor" data-testid="note-markdown-editor" />
</template>
