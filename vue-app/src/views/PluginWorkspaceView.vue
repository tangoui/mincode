<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import PluginViewFrame from "@/components/plugins/PluginViewFrame.vue";
import PluginViewUnavailable from "@/components/plugins/PluginViewUnavailable.vue";
import { useVueI18n } from "@/i18n/messages";
import { usePluginViewWarmPoolStore } from "@/stores/plugin-view-warm-pool";
import { useWorkbenchStore } from "@/stores/workbench";
import "@/styles/workspaces/plugins/workspace.css";

defineOptions({ name: "PluginWorkspaceView" });

// App.vue 为每个有插件 tab（或保温中的插件视图）的工作区常驻渲染一个实例（不经过 KeepAlive）：
// KeepAlive 停用组件时会把 DOM 移出文档，iframe 随之卸载，切回时插件页面
// 会重新加载并丢失未提交的表单。这里只按 visible 隐藏，iframe 始终留在文档中。
const props = defineProps<{
  workspaceId: string;
  visible: boolean;
}>();

type FrameStatus = "loading" | "ready" | "unavailable" | "error";
interface FrameSlot {
  id: string;
  /** Render key: the tab ID plus a generation that changes whenever the frame must be recreated. */
  key: string;
  pluginId: string;
  viewId: string;
  /** Closed tab kept in the warm pool: hidden, inert, no input, reused on reopen. */
  warm: boolean;
}

const workbench = useWorkbenchStore();
const warmPool = usePluginViewWarmPoolStore();
const { t } = useVueI18n();
// 实例按工作区创建，固定使用 prop，隐藏实例不跟随全局 activeWorkspaceId。
const workspaceId = props.workspaceId;
const visitedTabIds = ref<string[]>([]);
// iframe 的 DOM 顺序只追加、移除，从不重排：移动 iframe 节点会让浏览器重新加载插件页面。
const slotOrder = ref<string[]>([]);
const frameStatus = new Map<string, FrameStatus>();
const slotTargets = new Map<string, { pluginId: string; viewId: string }>();
// A frame that must not be reused (expired or mismatched warm entry) gets a new
// generation, so Vue mounts a fresh instance under a new key; the new slot is
// appended, so no other iframe moves.
const slotGenerations = new Map<string, number>();
function slotKey(id: string) {
  return `${id}#${slotGenerations.get(id) ?? 0}`;
}

// 已弹出到独立窗口的插件由那个窗口承载；主窗口不再为它保留 iframe，
// 否则同一插件页面会在两个窗口同时运行、连接同一个插件进程。
const pluginTabs = computed(() => workbench.tabs.filter((tab) =>
  tab.workspaceId === workspaceId
  && tab.view === "plugin-view"
  && !workbench.poppedOutTabLabels[tab.id]
  && Boolean(tab.pluginView?.pluginId && tab.pluginView.viewId)
));
const activePluginTabId = computed(() => {
  const tab = workbench.activeTab;
  return tab?.workspaceId === workspaceId && tab.view === "plugin-view" && !workbench.poppedOutTabLabels[tab.id]
    ? tab.id
    : "";
});
const warmEntries = computed(() => warmPool.entriesFor(workspaceId));
const frameSlots = computed<FrameSlot[]>(() => {
  const visited = new Set(visitedTabIds.value);
  const live = new Map(pluginTabs.value.filter((tab) => visited.has(tab.id)).map((tab) => [tab.id, tab]));
  const warm = new Map(warmEntries.value.map((entry) => [entry.key, entry]));
  return slotOrder.value.flatMap((id): FrameSlot[] => {
    const tab = live.get(id);
    if (tab?.pluginView) return [{ id, key: slotKey(id), pluginId: tab.pluginView.pluginId, viewId: tab.pluginView.viewId, warm: false }];
    const entry = warm.get(id);
    return entry ? [{ id, key: slotKey(id), pluginId: entry.pluginId, viewId: entry.viewId, warm: true }] : [];
  });
});

function pruneSlots() {
  const keep = new Set([...visitedTabIds.value, ...warmEntries.value.map((entry) => entry.key)]);
  if (slotOrder.value.every((id) => keep.has(id))) return;
  slotOrder.value = slotOrder.value.filter((id) => keep.has(id));
  for (const id of [...frameStatus.keys()]) if (!keep.has(id)) { frameStatus.delete(id); warmPool.untrackFrame(id); }
  for (const id of [...slotTargets.keys()]) if (!keep.has(id)) slotTargets.delete(id);
}

// 插件 iframe 首次访问时才创建；之后切换 tab 仅改变可见性，不卸载，也不使用
// display:none，避免 macOS WebView 在恢复 iframe 时重复触发 load 并被安全策略误判为跳转。
// 重新打开保温池中的同一视图（同一工作区、插件与视图，因此是同一个 tab ID）时直接复用那个 iframe；
// PluginViewFrame 在显示前重新校验插件仍启用、视图仍存在。
watch(activePluginTabId, (tabId) => {
  if (!tabId || visitedTabIds.value.includes(tabId)) return;
  const tab = pluginTabs.value.find((item) => item.id === tabId);
  if (!tab?.pluginView) return;
  const target = { key: tabId, workspaceId, pluginId: tab.pluginView.pluginId, viewId: tab.pluginView.viewId };
  const previous = slotTargets.get(tabId);
  const reusable = warmPool.has(tabId) && previous?.pluginId === target.pluginId && previous.viewId === target.viewId;
  if (warmPool.has(tabId)) {
    if (reusable && warmPool.take(target)) {
      // 复用：页面仍处于就绪状态，重新登记，之后关闭会再次保温。
      if (frameStatus.get(tabId) === "ready") warmPool.trackFrame(target);
    } else {
      // 不匹配或已过期（计时器尚未触发）的保温项不复用：换一个代际 key，Vue 会卸载旧实例
      // （关闭其会话）并在末尾挂载新实例；其他 iframe 不移动。
      warmPool.discard(tabId);
      warmPool.untrackFrame(tabId);
      slotOrder.value = slotOrder.value.filter((id) => id !== tabId);
      frameStatus.delete(tabId);
      slotGenerations.set(tabId, (slotGenerations.get(tabId) ?? 0) + 1);
    }
  }
  slotTargets.set(tabId, { pluginId: target.pluginId, viewId: target.viewId });
  visitedTabIds.value = [...visitedTabIds.value, tabId];
  if (!slotOrder.value.includes(tabId)) slotOrder.value = [...slotOrder.value, tabId];
}, { immediate: true });
// 已就绪页面的 tab 被关闭时，保温池在移除 tab 的同一同步步骤里把它停放（store 的 sync watcher，
// 早于任何组件重渲染，App.vue 因而不会在停放前卸载本宿主）；这里只清理访问记录。弹出的 tab 不算关闭：
// 弹出窗口会自己承载插件页面，主窗口不得在后台再保留一份，iframe 直接释放。
watch(pluginTabs, (tabs) => {
  const present = new Set(tabs.map((tab) => tab.id));
  const removed = visitedTabIds.value.filter((id) => !present.has(id));
  if (removed.length === 0) return;
  for (const id of removed) if (!warmPool.has(id)) warmPool.untrackFrame(id);
  visitedTabIds.value = visitedTabIds.value.filter((id) => present.has(id));
  pruneSlots();
});
// 保温项被淘汰、超时或失效时卸载对应 iframe（PluginViewFrame 卸载即关闭 UI 会话）。
watch(warmEntries, pruneSlots);

function handleFrameStatus(slot: FrameSlot, status: FrameStatus) {
  frameStatus.set(slot.id, status);
  if (status === "ready" && !slot.warm) warmPool.trackFrame({ key: slot.id, workspaceId, pluginId: slot.pluginId, viewId: slot.viewId });
  if (status !== "ready") warmPool.untrackFrame(slot.id);
  // 保温中的页面一旦失效（停用、卸载、崩溃、generation 变化…）立即销毁，绝不留待复用。
  if (slot.warm && status !== "ready") warmPool.discard(slot.id);
}

onBeforeUnmount(() => {
  // 宿主卸载时 iframe 随之销毁，对应保温项不能继续存在。
  warmPool.discardWorkspace(workspaceId);
  for (const id of slotOrder.value) warmPool.untrackFrame(id);
});
</script>

<template>
  <section
    class="plugin-view-workspace"
    :class="{ 'is-hidden': !visible }"
    :aria-hidden="!visible"
    :inert="!visible"
  >
    <div
      v-for="slot in frameSlots"
      :key="slot.key"
      class="plugin-view-workspace__pane"
      :class="{ 'is-active': slot.id === activePluginTabId, 'is-warm': slot.warm }"
      :aria-hidden="slot.id !== activePluginTabId"
      :inert="slot.id !== activePluginTabId"
      :data-plugin-view-warm="slot.warm ? '' : undefined"
    >
      <PluginViewFrame
        :plugin-id="slot.pluginId"
        :view-id="slot.viewId"
        :active="slot.id === activePluginTabId"
        :warm="slot.warm"
        :drop-target-id="slot.id"
        @status="handleFrameStatus(slot, $event)"
      />
    </div>
    <PluginViewUnavailable
      v-if="!activePluginTabId"
      :description="t('plugins.viewUnavailableDescription')"
    />
  </section>
</template>
