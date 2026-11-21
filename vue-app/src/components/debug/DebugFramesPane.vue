<script setup lang="ts">
/**
 * The paused session's threads and the selected thread's stack frames. When
 * the adapter supports restartFrame, a frame's context menu (right-click,
 * ContextMenu key or Shift+F10) has "Drop Frame" (greyed out for a frame the
 * adapter cannot restart and for the bottom frame of a fully loaded stack).
 */
import { computed, ref, watch } from "vue";
import { ElMessage } from "element-plus";
import { RefreshLeft } from "@element-plus/icons-vue";
import AppContextMenu from "@/components/ui/AppContextMenu.vue";
import { claimContextMenuKey, contextMenuAnchor } from "@/components/ui/app-context-menu";
import { useVueI18n } from "@/i18n/messages";
import { debugErrorText } from "@/domain/debug/errors";
import { isBottomFrame, type DebugSessionView } from "@/domain/debug/session-model";
import type { DapStackFrame } from "@/domain/debug/types";
import { useDebugSessionsStore } from "@/stores/debug-sessions";

const props = defineProps<{ session: DebugSessionView }>();

const emit = defineEmits<{
  "select-thread": [threadId: number];
  "select-frame": [frame: DapStackFrame];
}>();

const { t } = useVueI18n();
const sessions = useDebugSessionsStore();

/** The open context menu: the frame it belongs to, where it opened. */
const menu = ref<{ frame: DapStackFrame; x: number; y: number } | null>(null);
/** A restartFrame request is in flight. */
const restartInFlight = ref(false);
/** The state or the frames changed since the last restartFrame was sent (the adapter's stop that follows it). */
const changedSinceRestart = ref(true);
/** Another Drop Frame waits for the answer and for the state or frames to change (in either order). */
const restarting = computed(() => restartInFlight.value || !changedSinceRestart.value);

const canRestartFrames = computed(
  () => props.session.state === "paused" && !props.session.noDebug && props.session.capabilities.supportsRestartFrame === true
);

// A step, a new stop or the end makes the menu's frame stale (and ends a drop).
// (Separate sources: program output replaces the session view but neither of these.)
watch(
  [() => props.session.state, () => props.session.frames],
  () => {
    menu.value = null;
    changedSinceRestart.value = true;
  }
);

const emptyText = computed(() => {
  if (props.session.state === "ended") return t("debug.frames.ended");
  if (props.session.state === "running") return t("debug.frames.running");
  return t("debug.frames.empty");
});

function location(frame: DapStackFrame): string {
  const name = frame.source?.name ?? frame.source?.path?.split(/[\\/]/).pop();
  return name ? `${name}:${frame.line}` : t("debug.frames.noSource");
}

function openMenu(event: MouseEvent | KeyboardEvent, frame: DapStackFrame) {
  if (!canRestartFrames.value) return;
  event.preventDefault();
  menu.value = { frame, ...contextMenuAnchor(event) };
}

/** A frame can be dropped unless the adapter says it cannot or it is the bottom frame of the whole stack (no caller). */
function canDrop(frame: DapStackFrame): boolean {
  return frame.canRestart !== false && !isBottomFrame(props.session, frame.id);
}

function onFrameKeydown(event: KeyboardEvent, frame: DapStackFrame) {
  if (canRestartFrames.value && claimContextMenuKey(event)) openMenu(event, frame);
}

async function restartFromMenu() {
  const frame = menu.value?.frame;
  menu.value = null;
  if (!frame || !canDrop(frame) || restarting.value) return;
  restartInFlight.value = true;
  changedSinceRestart.value = false;
  try {
    await sessions.restartFrame(props.session.id, frame.id);
  } catch (error) {
    changedSinceRestart.value = true;
    ElMessage.error({ message: t("debug.frames.restartFailed", { error: debugErrorText(error) }), grouping: true });
  } finally {
    restartInFlight.value = false;
  }
}
</script>

<template>
  <section class="debug-frames" :aria-label="t('debug.frames.title')">
    <el-select
      class="debug-frames__thread"
      size="small"
      :model-value="session.selectedThreadId"
      :placeholder="t('debug.frames.noThreads')"
      :aria-label="t('debug.frames.thread')"
      :disabled="session.state !== 'paused' || session.threads.length === 0"
      @update:model-value="emit('select-thread', Number($event))"
    >
      <el-option v-for="thread in session.threads" :key="thread.id" :value="thread.id" :label="thread.name" />
    </el-select>
    <el-scrollbar v-if="session.state === 'paused' && session.frames.length" class="debug-frames__list">
      <ul role="listbox" :aria-label="t('debug.frames.title')">
        <li v-for="frame in session.frames" :key="frame.id" role="none">
          <el-button
            text
            native-type="button"
            role="option"
            class="debug-frames__item"
            :class="{ 'is-selected': frame.id === session.selectedFrameId, 'is-subtle': frame.presentationHint === 'subtle' }"
            :aria-selected="frame.id === session.selectedFrameId"
            :aria-haspopup="canRestartFrames ? 'menu' : undefined"
            :title="frame.source?.path ?? frame.name"
            @click="emit('select-frame', frame)"
            @contextmenu="openMenu($event, frame)"
            @keydown="onFrameKeydown($event, frame)"
          >
            <span class="debug-frames__name">{{ frame.name }}</span>
            <span class="debug-frames__location">{{ location(frame) }}</span>
          </el-button>
        </li>
      </ul>
    </el-scrollbar>
    <div v-else class="debug-pane-empty">{{ emptyText }}</div>
    <AppContextMenu
      v-if="menu"
      :open="true"
      :x="menu.x"
      :y="menu.y"
      :width="160"
      popper-class="debug-frames__menu"
      :aria-label="menu.frame.name"
      @close="menu = null"
    >
      <el-dropdown-item :icon="RefreshLeft" :disabled="!canDrop(menu.frame) || restarting" @click="restartFromMenu">
        {{ t("debug.frames.restartFrame") }}
      </el-dropdown-item>
    </AppContextMenu>
  </section>
</template>
