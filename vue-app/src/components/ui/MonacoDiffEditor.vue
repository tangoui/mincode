<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { ArrowDown, ArrowUp } from "@element-plus/icons-vue";
import * as monaco from "@/services/monaco-runtime.js";
import { getMonacoOverflowWidgetsHost } from "@/services/monaco-overflow-host";
import { detectMonacoEditorLanguage } from "@/services/monaco-languages";
import type { MonacoPresentationPreferences } from "@/domain/projects/types";
import { useVueI18n } from "@/i18n/messages";
import { ensureMonacoKeymap, registerMonacoDiffNavigation } from "@/services/monaco-keymap";
import {
  readStoredUiTheme,
  resolveMonacoEditorTheme,
  UI_THEME_CHANGED_EVENT,
  type UiTheme,
} from "@/services/ui-theme";

const props = defineProps<{
  path: string;
  originalPath?: string;
  originalContent: string;
  modifiedContent: string;
  preferences: MonacoPresentationPreferences;
  navigation?: boolean;
}>();

const emit = defineEmits<{
  ready: [];
}>();

const { t } = useVueI18n();
const host = ref<HTMLElement>();
let diffEditor: monaco.editor.IStandaloneDiffEditor | null = null;
let originalModel: monaco.editor.ITextModel | null = null;
let modifiedModel: monaco.editor.ITextModel | null = null;
let resizeObserver: ResizeObserver | null = null;
let diffUpdateDisposable: monaco.IDisposable | null = null;
let disposeDiffNavigation: (() => void) | null = null;
let layoutFrame: number | null = null;
let currentTheme = resolveMonacoEditorTheme(readStoredUiTheme());
let didEmitReady = false;
function detectLanguage(path: string, value: string) {
  return detectMonacoEditorLanguage(monaco, path, value);
}

/** Plugin languages changed: re-detect both sides of the diff. */
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

function emitReady() {
  if (didEmitReady) return;
  didEmitReady = true;
  emit("ready");
}

function hostDimensions() {
  const element = host.value;
  if (!element || !element.isConnected) return undefined;
  const { clientWidth: width, clientHeight: height } = element;
  if (width <= 0 || height <= 0) return undefined;
  return { width, height };
}

function applyPreferences(preferences: MonacoPresentationPreferences) {
  diffEditor?.updateOptions({
    fontSize: preferences.fontSize,
    lineNumbers: preferences.lineNumbers,
    renderWhitespace: preferences.renderWhitespace ?? "selection",
    guides: { indentation: preferences.indentationGuides ?? true },
    bracketPairColorization: { enabled: preferences.bracketPairColorization ?? true },
    stickyScroll: { enabled: preferences.stickyScroll ?? true },
  });
}

function setLanguage(path: string, originalPath = props.originalPath) {
  if (originalModel) {
    monaco.editor.setModelLanguage(
      originalModel,
      detectLanguage(originalPath || path, originalModel.getValue()),
    );
  }
  if (modifiedModel) {
    monaco.editor.setModelLanguage(modifiedModel, detectLanguage(path, modifiedModel.getValue()));
  }
}

function goToDiff(direction: "previous" | "next") {
  diffEditor?.goToDiff(direction);
  diffEditor?.focus();
}

// Diff navigation keys come from the active keymap: global Monaco rules for
// mincode.diff.next / mincode.diff.previous (`when: isInDiffEditor`, per
// preset) dispatch to the focused diff editor registered here, see
// services/monaco-keymap.ts. Without `navigation` the keys are a no-op.
function navigateFromKeymap(direction: "previous" | "next") {
  if (props.navigation) goToDiff(direction);
}

function createDiffEditor() {
  const element = host.value;
  const dimensions = hostDimensions();
  if (!element || !dimensions || diffEditor) return;

  const uniqueSuffix = `${Date.now()}-${Math.random().toString(36).slice(2)}`;
  const normalizedPath = props.path.replace(/\\/g, "/").replace(/^\/+/, "");
  const originalLanguage = detectLanguage(props.originalPath || props.path, props.originalContent);
  const modifiedLanguage = detectLanguage(props.path, props.modifiedContent);
  originalModel = monaco.editor.createModel(
    props.originalContent,
    originalLanguage,
    monaco.Uri.from({ scheme: "app-diff", path: `/${normalizedPath}.${uniqueSuffix}.original` })
  );
  modifiedModel = monaco.editor.createModel(
    props.modifiedContent,
    modifiedLanguage,
    monaco.Uri.from({ scheme: "app-diff", path: `/${normalizedPath}.${uniqueSuffix}.modified` })
  );
  diffEditor = monaco.editor.createDiffEditor(element, {
    theme: currentTheme,
    automaticLayout: false,
    overflowWidgetsDomNode: getMonacoOverflowWidgetsHost(),
    fixedOverflowWidgets: true,
    fontSize: props.preferences.fontSize,
    lineNumbers: props.preferences.lineNumbers,
    minimap: { enabled: false },
    renderSideBySide: true,
    useInlineViewWhenSpaceIsLimited: false,
    originalEditable: false,
    readOnly: true,
    scrollBeyondLastLine: false,
    stickyScroll: { enabled: false },
    renderWhitespace: "selection",
    renderControlCharacters: false,
    bracketPairColorization: { enabled: false },
    guides: { bracketPairs: false, indentation: false },
    diffWordWrap: "off",
    diffAlgorithm: "advanced",
    maxComputationTime: 1200,
    maxFileSize: 20,
    renderOverviewRuler: props.navigation ?? false,
  });
  applyPreferences(props.preferences);
  disposeDiffNavigation = registerMonacoDiffNavigation(diffEditor, navigateFromKeymap);
  diffUpdateDisposable = diffEditor.onDidUpdateDiff(emitReady);
  diffEditor.setModel({ original: originalModel, modified: modifiedModel });
  diffEditor.layout(dimensions);

  // Monaco may finish the first render before the diff worker emits its update.
  // Do not keep the review overlay visible in that valid no-change case.
  requestAnimationFrame(() => requestAnimationFrame(emitReady));
}

function scheduleLayout() {
  if (layoutFrame !== null) return;
  layoutFrame = requestAnimationFrame(() => {
    layoutFrame = null;
    const dimensions = hostDimensions();
    if (!dimensions) return;
    if (!diffEditor) createDiffEditor();
    diffEditor?.layout(dimensions);
  });
}

onMounted(async () => {
  ensureMonacoKeymap();
  if (!host.value) return;
  resizeObserver = new ResizeObserver(([entry]) => {
    if (!entry || entry.contentRect.width <= 0 || entry.contentRect.height <= 0) return;
    scheduleLayout();
  });
  resizeObserver.observe(host.value);
  window.addEventListener(UI_THEME_CHANGED_EVENT, handleThemeChange);
  await nextTick();
  scheduleLayout();
});

watch(
  () => props.originalContent,
  (value) => {
    if (originalModel && originalModel.getValue() !== value) originalModel.setValue(value);
  }
);

watch(
  () => props.modifiedContent,
  (value) => {
    if (modifiedModel && modifiedModel.getValue() !== value) modifiedModel.setValue(value);
  }
);

watch(
  [() => props.path, () => props.originalPath],
  ([path, originalPath]) => setLanguage(path, originalPath),
);

watch(
  () => props.preferences,
  (preferences) => applyPreferences(preferences),
  { deep: true }
);

watch(
  () => props.navigation,
  (navigation) => {
    diffEditor?.updateOptions({ renderOverviewRuler: navigation ?? false });
  }
);

onBeforeUnmount(() => {
  if (layoutFrame !== null) cancelAnimationFrame(layoutFrame);
  resizeObserver?.disconnect();
  diffUpdateDisposable?.dispose();
  disposeDiffNavigation?.();
  disposeDiffNavigation = null;
  window.removeEventListener(UI_THEME_CHANGED_EVENT, handleThemeChange);
  diffEditor?.dispose();
  originalModel?.dispose();
  modifiedModel?.dispose();
});
</script>

<template>
  <div class="monaco-diff-editor">
    <div ref="host" class="h-full min-h-0 min-w-0 flex-1" />
    <div v-if="navigation" class="monaco-diff-editor__navigation" role="toolbar" :aria-label="t('project.git.diffNavigation')">
      <el-button
        text
        circle
        size="small"
        :icon="ArrowUp"
        :title="t('project.git.previousChange')"
        :aria-label="t('project.git.previousChange')"
        @click="goToDiff('previous')"
      />
      <el-button
        text
        circle
        size="small"
        :icon="ArrowDown"
        :title="t('project.git.nextChange')"
        :aria-label="t('project.git.nextChange')"
        @click="goToDiff('next')"
      />
    </div>
  </div>
</template>

<style scoped>
.monaco-diff-editor {
  position: relative;
  display: flex;
  width: 100%;
  height: 100%;
}

.monaco-diff-editor__navigation {
  position: absolute;
  top: 8px;
  right: 36px;
  z-index: 1;
  display: flex;
  flex-direction: column;
  padding: 2px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 4px;
  background: color-mix(in srgb, var(--el-bg-color) 88%, transparent);
  box-shadow: var(--el-box-shadow-lighter);
}

.monaco-diff-editor__navigation :deep(.el-button + .el-button) {
  margin-left: 0;
}
</style>
