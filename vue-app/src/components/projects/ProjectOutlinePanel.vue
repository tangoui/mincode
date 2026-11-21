<script setup lang="ts">
/**
 * Project sidebar "Outline" tab: the document symbols of the active editor model, merged from
 * every registered provider (plugin LSPs, Monaco's JSON). Follows edits, provider changes and
 * the editor cursor; clicking a symbol reveals it.
 */
import { computed, onBeforeUnmount, ref, shallowRef, watch } from "vue";
import { Refresh } from "@element-plus/icons-vue";
import { projectEditorModelUri } from "@/composables/project-file-uri";
import {
  buildOutlineTree,
  countOutlineNodes,
  filterOutlineTree,
  outlineNodeAtLine,
  type OutlineNode,
} from "@/domain/projects/outline-tree";
import { useVueI18n } from "@/i18n/messages";
import * as monaco from "@/services/monaco-runtime.js";

const props = defineProps<{ path?: string; projectRoot?: string }>();
const emit = defineEmits<{ reveal: [line: number, column: number] }>();

const { t } = useVueI18n();
const filter = ref("");
const loading = ref(false);
const failed = ref(false);
const hasProvider = ref(false);
const nodes = shallowRef<OutlineNode[]>([]);
const currentKey = ref<string>();
const tree = ref<{ setCurrentKey(key?: string | null): void } | null>(null);

const visibleNodes = computed(() => filterOutlineTree(nodes.value, filter.value));
const symbolCount = computed(() => countOutlineNodes(nodes.value));
const treeKey = computed(() => `${props.path ?? ""}\u0000${filter.value.trim()}`);

let model: monaco.editor.ITextModel | null = null;
let modelDisposers: monaco.IDisposable[] = [];
let pending: monaco.CancellationTokenSource | null = null;
let refreshTimer: ReturnType<typeof setTimeout> | undefined;
let cursorLine = 0;

function clearRefreshTimer() {
  if (refreshTimer !== undefined) clearTimeout(refreshTimer);
  refreshTimer = undefined;
}

function cancelPending() {
  pending?.cancel();
  pending?.dispose();
  pending = null;
}

async function refresh() {
  clearRefreshTimer();
  cancelPending();
  const target = model;
  if (!target || target.isDisposed()) {
    nodes.value = [];
    hasProvider.value = false;
    loading.value = false;
    return;
  }
  hasProvider.value = monaco.mincodeDocumentSymbols.has(target);
  if (!hasProvider.value) {
    nodes.value = [];
    loading.value = false;
    failed.value = false;
    return;
  }
  const source = new monaco.CancellationTokenSource();
  pending = source;
  loading.value = true;
  try {
    const symbols = await monaco.mincodeDocumentSymbols.get(target, source.token);
    if (pending !== source || model !== target) return;
    nodes.value = buildOutlineTree(symbols);
    failed.value = false;
    syncCurrent();
  } catch {
    if (pending !== source) return;
    failed.value = true;
  } finally {
    if (pending === source) {
      loading.value = false;
      pending = null;
      source.dispose();
    }
  }
}

function scheduleRefresh(delay = 400) {
  clearRefreshTimer();
  refreshTimer = setTimeout(() => void refresh(), delay);
}

function syncCurrent() {
  currentKey.value = cursorLine ? outlineNodeAtLine(nodes.value, cursorLine) : undefined;
  tree.value?.setCurrentKey(currentKey.value ?? null);
}

function editorCursorLine(target: monaco.editor.ITextModel) {
  const editor = monaco.editor.getEditors().find((item) => item.getModel() === target);
  return editor?.getPosition()?.lineNumber ?? 0;
}

function detachModel() {
  for (const disposer of modelDisposers) disposer.dispose();
  modelDisposers = [];
  model = null;
}

function attachModel() {
  detachModel();
  cancelPending();
  const uri = props.path ? projectEditorModelUri(props.path, props.projectRoot) : null;
  const target = uri ? monaco.editor.getModel(uri) : null;
  if (!target) {
    nodes.value = [];
    hasProvider.value = false;
    loading.value = false;
    return;
  }
  model = target;
  cursorLine = editorCursorLine(target);
  modelDisposers = [
    target.onDidChangeContent(() => scheduleRefresh()),
    target.onDidChangeLanguage(() => scheduleRefresh(0)),
    target.onWillDispose(() => {
      detachModel();
      nodes.value = [];
      hasProvider.value = false;
    }),
  ];
  void refresh();
}

function modelMatches(target: monaco.editor.ITextModel) {
  if (!props.path) return false;
  return target.uri.toString() === projectEditorModelUri(props.path, props.projectRoot).toString();
}

function watchEditorCursor(editor: monaco.editor.ICodeEditor) {
  globalDisposers.push(
    editor.onDidChangeCursorPosition((event) => {
      if (!model || editor.getModel() !== model) return;
      cursorLine = event.position.lineNumber;
      syncCurrent();
    }),
  );
}

const globalDisposers: monaco.IDisposable[] = [
  monaco.mincodeDocumentSymbols.onDidChange(() => scheduleRefresh(0)),
  monaco.editor.onDidCreateModel((created) => {
    if (!model && modelMatches(created)) attachModel();
  }),
  monaco.editor.onDidCreateEditor((editor) => watchEditorCursor(editor)),
];
for (const editor of monaco.editor.getEditors()) watchEditorCursor(editor);

watch(() => [props.path, props.projectRoot], attachModel, { immediate: true });

onBeforeUnmount(() => {
  clearRefreshTimer();
  cancelPending();
  detachModel();
  for (const disposer of globalDisposers) disposer.dispose();
});

function onNodeClick(node: OutlineNode) {
  emit("reveal", node.line, node.column);
}
</script>

<template>
  <section class="project-outline-panel" :aria-label="t('project.outline.tab')">
    <header class="project-outline-panel__toolbar">
      <el-input
        v-model="filter"
        size="small"
        clearable
        :placeholder="t('project.outline.filter')"
        :aria-label="t('project.outline.filter')"
        :disabled="!symbolCount"
      />
      <el-tooltip :content="t('project.outline.refresh')" placement="bottom">
        <el-button
          text
          native-type="button"
          :aria-label="t('project.outline.refresh')"
          :loading="loading"
          :disabled="!path"
          @click="refresh()"
        >
          <el-icon><Refresh /></el-icon>
        </el-button>
      </el-tooltip>
    </header>

    <el-scrollbar class="project-outline-panel__body">
      <p v-if="!path" class="project-sidebar-empty project-sidebar-empty--compact">{{ t("project.outline.noEditor") }}</p>
      <p v-else-if="!hasProvider" class="project-sidebar-empty project-sidebar-empty--compact">
        {{ t("project.outline.noProvider") }}
      </p>
      <p v-else-if="failed" class="project-sidebar-empty project-sidebar-empty--compact">{{ t("project.outline.failed") }}</p>
      <p v-else-if="loading && !symbolCount" class="project-sidebar-empty project-sidebar-empty--compact">
        {{ t("project.outline.loading") }}
      </p>
      <p v-else-if="!symbolCount" class="project-sidebar-empty project-sidebar-empty--compact">{{ t("project.outline.empty") }}</p>
      <p v-else-if="!visibleNodes.length" class="project-sidebar-empty project-sidebar-empty--compact">
        {{ t("project.outline.noMatch") }}
      </p>
      <el-tree
        v-else
        :key="treeKey"
        ref="tree"
        class="project-outline-panel__tree"
        :data="visibleNodes"
        node-key="id"
        :props="{ label: 'name', children: 'children' }"
        :current-node-key="currentKey"
        :expand-on-click-node="false"
        :indent="12"
        highlight-current
        default-expand-all
        @node-click="onNodeClick"
      >
        <template #default="{ data }">
          <span class="project-outline-panel__node" :title="`${data.name}${data.detail ? ` — ${data.detail}` : ''}`">
            <span
              class="project-outline-panel__icon codicon"
              :class="[`codicon-symbol-${data.kindName}`, `is-${data.kindName}`]"
              aria-hidden="true"
            />
            <span class="project-outline-panel__name" dir="auto">{{ data.name }}</span>
            <small v-if="data.detail" class="project-outline-panel__detail" dir="auto">{{ data.detail }}</small>
          </span>
        </template>
      </el-tree>
    </el-scrollbar>
  </section>
</template>

<style scoped>
.project-outline-panel {
  display: flex;
  min-height: 0;
  flex: 1;
  flex-direction: column;
}

.project-outline-panel__toolbar {
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 4px 8px;
  border-bottom: 1px solid var(--app-border);
}

.project-outline-panel__body {
  min-height: 0;
  flex: 1;
}

.project-outline-panel__tree {
  background: transparent;
}

.project-outline-panel__node {
  display: flex;
  min-width: 0;
  flex: 1;
  align-items: center;
  gap: 6px;
  padding-right: 8px;
  font-size: 12px;
}

.project-outline-panel__icon {
  flex: none;
  color: var(--app-text-muted);
  font-size: 14px;
}

.project-outline-panel__icon.is-class,
.project-outline-panel__icon.is-struct,
.project-outline-panel__icon.is-enum,
.project-outline-panel__icon.is-event {
  color: #ee9d28;
}

.project-outline-panel__icon.is-method,
.project-outline-panel__icon.is-function,
.project-outline-panel__icon.is-constructor {
  color: #b180d7;
}

.project-outline-panel__icon.is-interface,
.project-outline-panel__icon.is-field,
.project-outline-panel__icon.is-variable,
.project-outline-panel__icon.is-enum-member,
.project-outline-panel__icon.is-type-parameter {
  color: #75beff;
}

.project-outline-panel__name {
  overflow: hidden;
  color: var(--app-text);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.project-outline-panel__detail {
  overflow: hidden;
  color: var(--app-text-muted);
  font-size: 11px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
