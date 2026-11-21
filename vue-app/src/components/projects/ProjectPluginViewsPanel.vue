<script setup lang="ts">
/**
 * Project sidebar "Views" tab: the plugin tree views (contributes.treeViews)
 * of process plugins active in this workspace. One view is shown at a time;
 * its root items load when the tab opens and children load when an item is
 * expanded (one `read`-risk plugin action per level). Activating an item runs
 * its target: a project file, the plugin's own view, one of its read
 * commands or an http(s) URL in the system browser. Nothing is persisted.
 */
import { computed, onBeforeUnmount, ref, shallowRef, watch, type Component } from "vue";
import { ElMessage } from "element-plus";
import {
  Aim,
  Box,
  Check,
  CircleClose,
  Clock,
  Cloudy,
  Coin,
  CollectionTag,
  Cpu,
  Document,
  Files,
  Folder,
  Grid,
  InfoFilled,
  Key,
  Link,
  Memo,
  Operation,
  PriceTag,
  Refresh,
  Setting,
  Share,
  Star,
  Stopwatch,
  User,
  Warning,
} from "@element-plus/icons-vue";
import { treeViewKey, treeViewTitle, type PluginTreeView, type TreeViewIcon, type TreeViewItem } from "@/domain/plugins/tree-views";
import { normalizeProviderURL } from "@/domain/plugins/provider-points";
import { useVueI18n } from "@/i18n/messages";
import { executePluginCommandWithArgs } from "@/services/plugin-commands";
import { providerErrorText } from "@/services/plugin-provider-invoke";
import { loadTreeViewItems } from "@/services/plugin-tree-view-service";
import { openPluginViewWithRequest } from "@/services/plugin-view-open-requests";
import { openSystemBrowser } from "@/services/web-automation-service";
import { usePluginTreeViewsStore } from "@/stores/plugin-tree-views";

const props = defineProps<{ projectRoot?: string }>();
const emit = defineEmits<{ "open-file": [path: string, line: number | undefined] }>();

interface TreeNode extends TreeViewItem {
  key: string;
  depth: number;
  isLeaf: boolean;
}

interface ElTreeNode {
  level: number;
  data: TreeNode;
  expanded: boolean;
  expand(): void;
  collapse(): void;
}

const ICONS: Record<TreeViewIcon, Component> = {
  file: Document,
  folder: Folder,
  symbol: CollectionTag,
  function: Operation,
  class: Grid,
  variable: Memo,
  package: Box,
  module: Files,
  bug: Aim,
  test: Stopwatch,
  check: Check,
  error: CircleClose,
  warning: Warning,
  info: InfoFilled,
  link: Link,
  gear: Setting,
  database: Coin,
  cloud: Cloudy,
  server: Cpu,
  tag: PriceTag,
  star: Star,
  clock: Clock,
  user: User,
  key: Key,
  branch: Share,
};

const { t, localeChain } = useVueI18n();
const store = usePluginTreeViewsStore();
const selectedKey = ref<string>();
const rootLoading = ref(false);
const rootError = ref("");
const rootNotice = ref("");
const rootCount = ref(0);
const activating = ref(false);
const generation = ref(0);
const controllers = new Set<AbortController>();
const rootData = shallowRef<TreeNode[]>([]);

const views = computed(() => store.views);
const selected = computed<PluginTreeView | undefined>(
  () => views.value.find((view) => treeViewKey(view) === selectedKey.value) ?? views.value[0],
);
const options = computed(() =>
  views.value.map((view) => ({ key: treeViewKey(view), label: `${treeViewTitle(view, localeChain.value)} · ${view.pluginName}` })),
);
const treeKey = computed(() => `${selected.value ? treeViewKey(selected.value) : ""}\u0000${generation.value}`);
const locale = computed(() => localeChain.value[0] ?? "en");

function abortAll() {
  for (const controller of controllers) controller.abort();
  controllers.clear();
}

function toNodes(items: TreeViewItem[], depth: number, parentKey: string): TreeNode[] {
  return items.map((item) => ({ ...item, key: `${parentKey}\u0000${item.id}`, depth, isLeaf: !item.collapsible }));
}

function noticeFor(dropped: number, truncated: boolean) {
  if (truncated) return t("pluginContrib.treeViews.truncated");
  if (dropped) return t("pluginContrib.treeViews.dropped", { count: dropped });
  return "";
}

async function fetchLevel(view: PluginTreeView, depth: number, parent?: TreeNode) {
  const controller = new AbortController();
  controllers.add(controller);
  try {
    return await loadTreeViewItems(
      view,
      { depth, locale: locale.value, ...(parent ? { parentId: parent.id } : {}), ...(props.projectRoot ? { projectRoot: props.projectRoot } : {}) },
      controller.signal,
    );
  } finally {
    controllers.delete(controller);
  }
}

async function loadRoot() {
  abortAll();
  generation.value += 1;
  const current = generation.value;
  const view = selected.value;
  rootData.value = [];
  rootError.value = "";
  rootNotice.value = "";
  rootCount.value = 0;
  if (!view) {
    rootLoading.value = false;
    return;
  }
  rootLoading.value = true;
  const result = await fetchLevel(view, 0);
  if (current !== generation.value) return;
  rootLoading.value = false;
  if (!result.ok) {
    if (result.error.code !== "cancelled") rootError.value = providerErrorText(result.error);
    return;
  }
  rootData.value = toNodes(result.response.items, 0, treeViewKey(view));
  rootCount.value = result.response.items.length;
  rootNotice.value = noticeFor(result.response.dropped, result.response.truncated);
}

// el-tree lazy loader for children; the root level is loaded by loadRoot.
async function loadChildren(node: ElTreeNode, resolve: (data: TreeNode[]) => void) {
  if (node.level === 0) {
    resolve(rootData.value);
    return;
  }
  const view = selected.value;
  const current = generation.value;
  const parent = node.data;
  if (!view || parent.isLeaf) {
    resolve([]);
    return;
  }
  const result = await fetchLevel(view, parent.depth + 1, parent);
  if (current !== generation.value) {
    resolve([]);
    return;
  }
  if (!result.ok) {
    if (result.error.code !== "cancelled") {
      ElMessage({ type: "warning", grouping: true, message: t("pluginContrib.treeViews.loadFailed", { error: providerErrorText(result.error) }) });
    }
    resolve([]);
    return;
  }
  const notice = noticeFor(result.response.dropped, result.response.truncated);
  if (notice) ElMessage({ type: "info", grouping: true, message: notice });
  resolve(toNodes(result.response.items, parent.depth + 1, parent.key));
}

async function activate(view: PluginTreeView, item: TreeNode) {
  const target = item.target;
  if (!target) return;
  switch (target.type) {
    case "file":
      emit("open-file", target.path, target.line);
      return;
    case "view":
      await openPluginViewWithRequest(view.pluginId, target.view, {
        source: "tree",
        entry: view.id,
        ...(target.args ? { params: target.args } : {}),
      });
      return;
    case "command":
      await executePluginCommandWithArgs(view.pluginId, target.command, target.args ? { args: target.args } : {});
      return;
    case "url": {
      // Only reached from an explicit click on the item.
      const url = normalizeProviderURL(target.url, 2048);
      if (!url) throw new Error("Unsupported URL");
      await openSystemBrowser(url);
      return;
    }
  }
}

async function onNodeClick(data: TreeNode, node: ElTreeNode) {
  const view = selected.value;
  if (!view) return;
  if (!data.target) {
    if (!data.isLeaf) {
      if (node.expanded) node.collapse();
      else node.expand();
    }
    return;
  }
  if (activating.value) return;
  activating.value = true;
  try {
    await activate(view, data);
  } catch (error) {
    ElMessage({
      type: "warning",
      grouping: true,
      message: t("pluginContrib.treeViews.activateFailed", { error: providerErrorText({ code: "unknown", detail: error instanceof Error ? error.message : String(error) }) }),
    });
  } finally {
    activating.value = false;
  }
}

function itemTitle(item: TreeNode) {
  const lines = [item.tooltip ?? [item.label, item.description].filter(Boolean).join(" — ")];
  if (item.target?.type === "url") lines.push(t("pluginContrib.treeViews.opensInBrowser", { url: item.target.url }));
  return lines.join("\n");
}

watch(
  () => [selected.value ? treeViewKey(selected.value) : "", store.revision, props.projectRoot, locale.value],
  () => void loadRoot(),
  { immediate: true },
);

onBeforeUnmount(() => {
  generation.value += 1;
  abortAll();
});
</script>

<template>
  <section class="project-plugin-views-panel" :aria-label="t('pluginContrib.treeViews.tab')">
    <header class="project-plugin-views-panel__toolbar">
      <el-select
        v-if="options.length > 1"
        :model-value="selected ? treeViewKey(selected) : undefined"
        size="small"
        :aria-label="t('pluginContrib.treeViews.select')"
        @update:model-value="(value: string) => (selectedKey = value)"
      >
        <el-option v-for="option in options" :key="option.key" :value="option.key" :label="option.label" />
      </el-select>
      <span v-else-if="selected" class="project-plugin-views-panel__title" :title="selected.pluginName">
        {{ treeViewTitle(selected, localeChain) }}
      </span>
      <el-tooltip :content="t('pluginContrib.treeViews.refresh')" placement="bottom">
        <el-button
          text
          native-type="button"
          :aria-label="t('pluginContrib.treeViews.refresh')"
          :loading="rootLoading"
          :disabled="!selected"
          @click="loadRoot()"
        >
          <el-icon><Refresh /></el-icon>
        </el-button>
      </el-tooltip>
    </header>

    <el-scrollbar class="project-plugin-views-panel__body">
      <p v-if="!selected" class="project-sidebar-empty project-sidebar-empty--compact">{{ t("pluginContrib.treeViews.none") }}</p>
      <p v-else-if="rootError" class="project-sidebar-empty project-sidebar-empty--compact" dir="auto">
        {{ t("pluginContrib.treeViews.loadFailed", { error: rootError }) }}
      </p>
      <p v-else-if="rootLoading" class="project-sidebar-empty project-sidebar-empty--compact">{{ t("pluginContrib.treeViews.loading") }}</p>
      <p v-else-if="!rootCount" class="project-sidebar-empty project-sidebar-empty--compact">{{ t("pluginContrib.treeViews.empty") }}</p>
      <template v-else>
        <el-tree
          :key="treeKey"
          class="project-plugin-views-panel__tree"
          node-key="key"
          lazy
          :load="loadChildren"
          :props="{ label: 'label', isLeaf: 'isLeaf' }"
          :expand-on-click-node="false"
          :indent="12"
          @node-click="onNodeClick"
        >
          <template #default="{ data }">
            <span class="project-plugin-views-panel__node" :title="itemTitle(data)">
              <el-icon v-if="data.icon" class="project-plugin-views-panel__icon" :class="`is-${data.icon}`" aria-hidden="true">
                <component :is="ICONS[data.icon as TreeViewIcon]" />
              </el-icon>
              <span class="project-plugin-views-panel__label" dir="auto">{{ data.label }}</span>
              <small v-if="data.description" class="project-plugin-views-panel__description" dir="auto">{{ data.description }}</small>
            </span>
          </template>
        </el-tree>
        <p v-if="rootNotice" class="project-plugin-views-panel__notice">{{ rootNotice }}</p>
      </template>
    </el-scrollbar>
  </section>
</template>

<style scoped>
.project-plugin-views-panel {
  display: flex;
  min-height: 0;
  flex: 1;
  flex-direction: column;
}

.project-plugin-views-panel__toolbar {
  display: flex;
  align-items: center;
  gap: 4px;
  min-height: 32px;
  padding: 4px 8px;
  border-bottom: 1px solid var(--app-border);
}

.project-plugin-views-panel__toolbar .el-select {
  min-width: 0;
  flex: 1;
}

.project-plugin-views-panel__title {
  overflow: hidden;
  min-width: 0;
  flex: 1;
  color: var(--app-text);
  font-size: 12px;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.project-plugin-views-panel__body {
  min-height: 0;
  flex: 1;
}

.project-plugin-views-panel__tree {
  background: transparent;
}

.project-plugin-views-panel__node {
  display: flex;
  min-width: 0;
  flex: 1;
  align-items: center;
  gap: 6px;
  padding-right: 8px;
  font-size: 12px;
}

.project-plugin-views-panel__icon {
  flex: none;
  color: var(--app-text-muted);
  font-size: 14px;
}

.project-plugin-views-panel__icon.is-error {
  color: var(--el-color-danger);
}

.project-plugin-views-panel__icon.is-warning {
  color: var(--el-color-warning);
}

.project-plugin-views-panel__icon.is-check {
  color: var(--el-color-success);
}

.project-plugin-views-panel__label {
  overflow: hidden;
  color: var(--app-text);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.project-plugin-views-panel__description {
  overflow: hidden;
  color: var(--app-text-muted);
  font-size: 11px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.project-plugin-views-panel__notice {
  margin: 4px 8px;
  color: var(--app-text-muted);
  font-size: 11px;
}
</style>
