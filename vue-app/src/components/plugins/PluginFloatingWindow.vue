<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watchEffect } from "vue";
import { ElButton } from "element-plus";
import { Close } from "@element-plus/icons-vue";
import PluginViewFrame from "@/components/plugins/PluginViewFrame.vue";
import { hidePluginFloatingView, type PluginState } from "@/services/plugin-service";
import { isDesktopRuntime, listenAppEvent } from "@/services/desktop-bridge";
import { useAppLockStore } from "@/stores/app-lock";
import { useVueI18n } from "@/i18n/messages";
import "@/styles/workspaces/plugins/workspace.css";
import "@/styles/workspaces/plugins/floating-window.css";

// Root of a views[].floating window (?mode=plugin-floating). The host owns
// dragging (the whole window is a drag region; the page gets no pointer input)
// and the hide button; the plugin page only draws its content. A click that is
// not a drag is announced to the page, which may then open something in the
// main window (PluginViewFrame issues a single-use token for it).
const props = defineProps<{ pluginId: string; viewId: string }>();

const { t } = useVueI18n();
const appLock = useAppLockStore();
const frameKey = ref(0);
const frame = ref<InstanceType<typeof PluginViewFrame>>();
let frameDown = false;
// A press that ends within a few pixels and a short time is a click; anything else was a drag.
const CLICK_SLOP_PX = 4;
const CLICK_MAX_MS = 600;
let press: { x: number; y: number; at: number } | undefined;

function handlePointerDown(event: PointerEvent) {
  press = event.button === 0 ? { x: event.screenX, y: event.screenY, at: Date.now() } : undefined;
}

function handleClick(event: MouseEvent) {
  const start = press;
  press = undefined;
  if (!start || (event.target as Element | null)?.closest?.(".plugin-floating-window__hide")) return;
  if (Math.abs(event.screenX - start.x) > CLICK_SLOP_PX || Math.abs(event.screenY - start.y) > CLICK_SLOP_PX || Date.now() - start.at > CLICK_MAX_MS) return;
  frame.value?.notifyFloatingClick(event.clientX, event.clientY);
}
let stopStateListener: (() => void) | undefined;

const windowTitle = computed(() => t("plugins.floatingWindowTitle"));
watchEffect(() => {
  document.title = windowTitle.value;
});

function hide() {
  void hidePluginFloatingView(props.pluginId, props.viewId).catch(() => undefined);
}

function handleFrameStatus(status: "loading" | "ready" | "unavailable" | "error") {
  frameDown = status === "unavailable" || status === "error";
}

// A reload or crash recovery starts a new process generation and the frame
// closes its session; reconnect as soon as the plugin runs again.
function handlePluginState(event: { id?: string; state?: PluginState | "uninstalled"; enabled?: boolean }) {
  if (event?.id !== props.pluginId || !event.enabled || event.state !== "running" || !frameDown) return;
  frameDown = false;
  frameKey.value += 1;
}

onMounted(() => {
  appLock.ensureListening();
  void appLock.hydrate();
  if (isDesktopRuntime()) stopStateListener = listenAppEvent("plugin:state-changed", handlePluginState);
});

onBeforeUnmount(() => {
  stopStateListener?.();
  appLock.dispose();
});
</script>

<template>
  <div class="plugin-floating-window" @pointerdown="handlePointerDown" @click="handleClick">
    <PluginViewFrame
      v-if="!appLock.locked"
      ref="frame"
      :key="frameKey"
      :plugin-id="pluginId"
      :view-id="viewId"
      :active="true"
      drop-target-id=""
      variant="floating"
      @status="handleFrameStatus"
    />
    <ElButton
      class="plugin-floating-window__hide"
      circle
      size="small"
      :icon="Close"
      :title="t('plugins.floatingHide')"
      :aria-label="t('plugins.floatingHide')"
      @click="hide"
    />
  </div>
</template>
