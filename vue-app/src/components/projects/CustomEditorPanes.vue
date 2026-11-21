<script setup lang="ts">
/**
 * Plugin custom editor panes (contributes.customEditors) for project document
 * tabs. Each pane is a document-mode PluginViewFrame bound by the Go host to
 * one project file. Recently used panes stay mounted (hidden) so switching
 * tabs keeps plugin state; at most MAX_RETAINED panes are kept, and dirty or
 * active panes are never evicted.
 */
import { computed, ref, watch } from "vue";
import { ElButton } from "element-plus";
import { Loading } from "@element-plus/icons-vue";
import PluginViewFrame from "@/components/plugins/PluginViewFrame.vue";
import { customEditorKey } from "@/domain/plugins/custom-editors";
import type { CustomEditorDocumentInfo } from "@/domain/plugin-ui/document-protocol";
import { useVueI18n } from "@/i18n/messages";
import { isDesktopRuntime } from "@/services/desktop-bridge";
import { useCustomEditorsStore } from "@/stores/custom-editors";
import "@/styles/workspaces/plugins/workspace.css";

export interface CustomEditorPane {
  /** Document tab path (absolute). */
  path: string;
  pluginId: string;
  editorId: string;
  projectRoot: string;
  relativePath: string;
  dirty: boolean;
}

const MAX_RETAINED = 8;

const props = defineProps<{
  panes: CustomEditorPane[];
  activePath: string | null;
}>();

const emit = defineEmits<{
  dirty: [path: string, dirty: boolean];
  saved: [path: string];
  failed: [path: string, description: string];
  "reopen-text": [path: string];
  close: [path: string];
}>();

const { t } = useVueI18n();
const editors = useCustomEditorsStore();
const retained = ref<string[]>([]);
const frames = new Map<string, InstanceType<typeof PluginViewFrame>>();

function paneKey(pane: CustomEditorPane) {
  return `${pane.path}\u0000${customEditorKey(pane.pluginId, pane.editorId)}`;
}

const panesByKey = computed(() => new Map(props.panes.map((pane) => [paneKey(pane), pane])));
const activePane = computed(() => props.panes.find((pane) => pane.path === props.activePath));

watch(
  [activePane, () => props.panes],
  () => {
    const valid = retained.value.filter((key) => panesByKey.value.has(key));
    const active = activePane.value ? paneKey(activePane.value) : undefined;
    const next = active ? [active, ...valid.filter((key) => key !== active)] : valid;
    while (next.length > MAX_RETAINED) {
      // Evict the least recently used pane that is neither active nor dirty.
      let index = next.length - 1;
      while (index > 0 && (next[index] === active || panesByKey.value.get(next[index]!)?.dirty)) index -= 1;
      if (index <= 0) break;
      next.splice(index, 1);
    }
    retained.value = next;
  },
  { immediate: true, deep: true },
);

const mountedPanes = computed(() => retained.value.flatMap((key) => {
  const pane = panesByKey.value.get(key);
  return pane ? [{ key, pane }] : [];
}));

function editorFor(pane: CustomEditorPane) {
  return editors.editorByKey(customEditorKey(pane.pluginId, pane.editorId));
}

/** The editor went away (plugin disabled/uninstalled/out of workspace) after contributions loaded. */
function paneMissing(pane: CustomEditorPane) {
  return isDesktopRuntime() && editors.loaded && !editorFor(pane);
}

function paneWaiting(pane: CustomEditorPane) {
  return isDesktopRuntime() && !editors.loaded && !editorFor(pane);
}

// A pane whose editor disappears fails closed; unsaved work is reported once.
watch(
  () => props.panes.filter(paneMissing).map((pane) => paneKey(pane)),
  (missing, previous = []) => {
    for (const key of missing) {
      if (previous.includes(key)) continue;
      const pane = panesByKey.value.get(key);
      if (pane) emit("failed", pane.path, t("plugins.viewStopped"));
    }
  },
);

function setFrame(key: string, frame: unknown) {
  if (frame) frames.set(key, frame as InstanceType<typeof PluginViewFrame>);
  else frames.delete(key);
}

/** Asks the plugin page of `path` to save. Rejects when the pane is not live. */
async function requestSave(path: string) {
  const pane = props.panes.find((candidate) => candidate.path === path);
  const frame = pane ? frames.get(paneKey(pane)) : undefined;
  if (!pane || !frame || paneMissing(pane)) throw new Error(t("pluginContrib.customEditors.unavailable"));
  await frame.requestSave();
}

defineExpose({ requestSave });

function onReady(_pane: CustomEditorPane, _document: CustomEditorDocumentInfo) {
  // The host re-read the baseline (open, reload, or retry): the tab starts clean.
  emit("dirty", _pane.path, false);
}
</script>

<template>
  <div class="custom-editor-panes">
    <div
      v-for="{ key, pane } in mountedPanes"
      :key="key"
      class="custom-editor-panes__pane"
      :class="{ 'is-active': pane.path === activePath }"
      :aria-hidden="pane.path === activePath ? undefined : 'true'"
    >
      <div v-if="paneMissing(pane)" class="plugin-view-unavailable" role="alert">
        <strong>{{ t("pluginContrib.customEditors.unavailable") }}</strong>
        <p>{{ t("plugins.viewStopped") }}</p>
        <div class="custom-editor-panes__actions">
          <ElButton type="primary" native-type="button" @click="emit('reopen-text', pane.path)">
            {{ t("pluginContrib.customEditors.reopenAsText") }}
          </ElButton>
          <ElButton native-type="button" @click="emit('close', pane.path)">{{ t("common.close") }}</ElButton>
        </div>
      </div>
      <div v-else-if="paneWaiting(pane)" class="custom-editor-panes__loading" role="status">
        <el-icon class="is-loading"><Loading /></el-icon>
        <span>{{ t("plugins.viewLoading") }}</span>
      </div>
      <PluginViewFrame
        v-else
        :ref="(frame) => setFrame(key, frame)"
        :plugin-id="pane.pluginId"
        :view-id="editorFor(pane)?.viewId ?? ''"
        :active="pane.path === activePath"
        :drop-target-id="`custom-editor-${key}`"
        :document="{ editorId: pane.editorId, projectRoot: pane.projectRoot, relativePath: pane.relativePath }"
        @document-ready="(document) => onReady(pane, document)"
        @document-dirty="(dirty) => emit('dirty', pane.path, dirty)"
        @document-saved="emit('saved', pane.path)"
        @document-failed="(description) => emit('failed', pane.path, description)"
        @reopen-text="emit('reopen-text', pane.path)"
        @close="emit('close', pane.path)"
      />
    </div>
  </div>
</template>

<style scoped>
.custom-editor-panes {
  position: relative;
  width: 100%;
  height: 100%;
  min-width: 0;
  min-height: 0;
  overflow: hidden;
  background: var(--app-surface);
}
.custom-editor-panes__pane {
  position: absolute;
  inset: 0;
  z-index: 0;
  visibility: hidden;
  pointer-events: none;
}
.custom-editor-panes__pane.is-active {
  z-index: 1;
  visibility: visible;
  pointer-events: auto;
}
.custom-editor-panes__actions {
  display: flex;
  flex-wrap: wrap;
  justify-content: center;
  gap: 8px;
}
.custom-editor-panes__loading {
  display: grid;
  place-content: center;
  place-items: center;
  gap: 9px;
  height: 100%;
  color: var(--app-text-muted);
  font-size: 12px;
}
</style>
