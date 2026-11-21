<script setup lang="ts">
// Read-only Monaco viewer (table DDL, plugin service results). Heavy: load it
// with defineAsyncComponent so Monaco stays out of the caller's chunk.
import { onBeforeUnmount, onMounted, ref, watch } from "vue";
import * as monaco from "@/services/monaco-runtime.js";
import { getMonacoOverflowWidgetsHost } from "@/services/monaco-overflow-host";
import {
  ensureMonacoKeymap,
  registerMonacoEditor,
  type MonacoEditorKind,
  type MonacoEditorRegistration,
} from "@/services/monaco-keymap";
import {
  readStoredUiTheme,
  resolveMonacoEditorTheme,
  UI_THEME_CHANGED_EVENT,
  type UiTheme,
} from "@/services/ui-theme";

const props = withDefaults(
  defineProps<{
    value: string;
    ariaLabel: string;
    /** Monaco language id of the content. */
    language?: string;
    /** `editor.kind` for keybinding when clauses. */
    editorKind?: MonacoEditorKind;
  }>(),
  { language: "plaintext", editorKind: "text" },
);

const host = ref<HTMLElement>();
let editor: monaco.editor.IStandaloneCodeEditor | null = null;
let model: monaco.editor.ITextModel | null = null;
let keymapRegistration: MonacoEditorRegistration | null = null;

let currentTheme = resolveMonacoEditorTheme(readStoredUiTheme());

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

onMounted(() => {
  ensureMonacoKeymap();
  if (!host.value) return;

  const uniqueSuffix = `${Date.now()}-${Math.random().toString(36).slice(2)}`;
  model = monaco.editor.createModel(
    props.value,
    props.language,
    monaco.Uri.from({ scheme: "app-readonly", path: `/readonly-${uniqueSuffix}` })
  );
  editor = monaco.editor.create(host.value, {
    model,
    theme: currentTheme,
    automaticLayout: true,
    overflowWidgetsDomNode: getMonacoOverflowWidgetsHost(),
    fixedOverflowWidgets: true,
    ariaLabel: props.ariaLabel,
    readOnly: true,
    fontSize: 13,
    fontFamily: "var(--app-font-mono)",
    lineHeight: 22,
    lineNumbers: "on",
    lineNumbersMinChars: 3,
    minimap: { enabled: false },
    folding: true,
    showFoldingControls: "mouseover",
    wordWrap: "on",
    wrappingIndent: "same",
    scrollBeyondLastLine: false,
    stickyScroll: { enabled: false },
    renderLineHighlight: "none",
    selectionHighlight: false,
    occurrencesHighlight: "off",
    matchBrackets: "always",
    bracketPairColorization: { enabled: true },
    guides: { indentation: true, bracketPairs: true },
    renderWhitespace: "selection",
    padding: { top: 12, bottom: 12 },
    scrollbar: {
      verticalScrollbarSize: 10,
      horizontalScrollbarSize: 10,
      useShadows: false,
    },
  });
  // Read-only viewer: the keymap applies (⌘K etc.); save/run have no handler here.
  keymapRegistration = registerMonacoEditor(editor, { kind: props.editorKind, handlers: {} });

  window.addEventListener(UI_THEME_CHANGED_EVENT, handleThemeChange);
});

watch(
  () => props.value,
  (value) => {
    if (model && model.getValue() !== value) model.setValue(value);
  }
);

watch(
  () => props.language,
  (language) => {
    if (model) monaco.editor.setModelLanguage(model, language);
  }
);

watch(
  () => props.ariaLabel,
  (ariaLabel) => editor?.updateOptions({ ariaLabel })
);

onBeforeUnmount(() => {
  window.removeEventListener(UI_THEME_CHANGED_EVENT, handleThemeChange);
  keymapRegistration?.dispose();
  keymapRegistration = null;
  editor?.dispose();
  model?.dispose();
});
</script>

<template>
  <div ref="host" class="app-readonly-code-viewer" />
</template>

<style scoped>
.app-readonly-code-viewer {
  width: 100%;
  height: 100%;
}
</style>
