<script setup lang="ts">
/**
 * One debug session's tool window: the toolbar, the exception the session is
 * paused on (if any), hot code replace notices (changes waiting for Apply
 * Code Changes, the last outcome or the adapter's warning; the session goes
 * on), then Frames + Variables (Debugger) and the Console. A run without debugging shows only the Console.
 * Smart Step Into's choice of targets (from the toolbar or its command) opens below the execution line
 * when the editor shows it, otherwise below the toolbar button; only the window of the active
 * workspace view shows it (the choice is the store's, and a cached workspace of the same project
 * has a window for the same session).
 */
import { computed, defineAsyncComponent, nextTick, ref, watch } from "vue";
import AppSplitPane from "@/components/ui/AppSplitPane.vue";
import DebugConsolePane from "@/components/debug/DebugConsolePane.vue";
import DebugExceptionBar from "@/components/debug/DebugExceptionBar.vue";
import DebugFramesPane from "@/components/debug/DebugFramesPane.vue";
import DebugStepTargetsMenu from "@/components/debug/DebugStepTargetsMenu.vue";
import DebugToolbar, { type DebugToolbarAction } from "@/components/debug/DebugToolbar.vue";
import DebugVariablesPane from "@/components/debug/DebugVariablesPane.vue";
import { shortHotCodeReplaceMessage } from "@/domain/debug/hot-code-replace";
import type { DapStackFrame } from "@/domain/debug/types";
import { useVueI18n } from "@/i18n/messages";
import { debugFrameLineAnchor } from "@/services/debug-line-anchor";
import { useDebugSessionsStore } from "@/stores/debug-sessions";
import { isVirtualDebugSource } from "@/domain/debug/virtual-source";

const DebugBreakpointsDialog = defineAsyncComponent(() => import("@/components/debug/DebugBreakpointsDialog.vue"));

export interface DebugCursorLocation {
  /** Absolute path of a project file. */
  path: string;
  line: number;
}

const props = withDefaults(
  defineProps<{
    sessionId: string;
    /** The tool window is visible (its terminal renders only then). */
    active?: boolean;
    /**
     * The workspace view holding the window is the active one (not a
     * KeepAlive-cached workspace of the same project); Smart Step Into's menu
     * opens only then, also while the panel is collapsed.
     */
    viewActive?: boolean;
    /** The editor cursor, for Run to Cursor; absent or null disables it. */
    resolveCursor?: () => DebugCursorLocation | null;
  }>(),
  { active: true, viewActive: true, resolveCursor: undefined }
);

const emit = defineEmits<{
  "open-location": [path: string, line: number];
  /** Open the source of a debugger address (a breakpoint in a debugger-provided source) at line. */
  "open-address": [address: string, type: string, line: number];
}>();

const { t } = useVueI18n();
const sessions = useDebugSessionsStore();

const session = computed(() => sessions.sessions.find((item) => item.id === props.sessionId) ?? null);
const view = ref<"debugger" | "console">("debugger");
const framesWidth = ref(260);
const breakpointsOpen = ref(false);
const breakpointsMounted = ref(false);
const root = ref<HTMLElement>();

/** Smart Step Into's open choice for this session (shown in the active workspace view only) and where its menu opens. */
const stepTargets = computed(() => (props.viewActive ? sessions.stepTargetsPickFor(props.sessionId) : null));
const stepTargetsAnchor = ref<{ x: number; y: number } | null>(null);
watch(stepTargets, async (pick) => {
  stepTargetsAnchor.value = null;
  if (!pick) return;
  await nextTick();
  if (stepTargets.value !== pick) return;
  stepTargetsAnchor.value = stepTargetsMenuAnchor(pick.frameId);
});

/**
 * Below the execution line (the frame the targets belong to) when the editor
 * shows it; otherwise below the Smart Step Into button, and in the upper
 * middle of the window when the panel is collapsed (the button has no box).
 */
function stepTargetsMenuAnchor(frameId: number): { x: number; y: number } {
  const frame = session.value?.frames.find((item) => item.id === frameId);
  const atLine = frame ? debugFrameLineAnchor(props.sessionId, frame) : null;
  if (atLine) return atLine;
  const rect = root.value?.querySelector('[data-action="smart-step-into"]')?.getBoundingClientRect();
  if (rect && (rect.width > 0 || rect.height > 0)) return { x: rect.left, y: rect.bottom + 2 };
  return { x: Math.max(8, window.innerWidth / 2 - 120), y: Math.round(window.innerHeight / 3) };
}

const muted = computed(() => sessions.isMuted(props.sessionId));
const canApplyCodeChanges = computed(() => sessions.canApplyCodeChanges(props.sessionId));
const showConsole = computed(() => Boolean(session.value?.noDebug) || view.value === "console");

const stateLabel = computed(() => {
  const current = session.value;
  if (!current) return "";
  if (current.state === "ended") return current.endReason ? t("debug.window.ended", { reason: current.endReason }) : t("debug.state.ended");
  return t(current.state === "paused" ? "debug.state.paused" : "debug.state.running");
});

// The program's terminal shows up while it runs; a stop brings the frames back.
watch(
  () => session.value?.consoleTerminalId,
  (id, previous) => {
    if (id && id !== previous && session.value?.state === "running") view.value = "console";
  }
);
watch(
  () => session.value?.state,
  (state) => {
    if (state === "paused") view.value = "debugger";
  }
);

function openBreakpoints() {
  breakpointsMounted.value = true;
  breakpointsOpen.value = true;
}

function handleAction(action: DebugToolbarAction) {
  const id = props.sessionId;
  switch (action) {
    case "resume":
      void sessions.resume(id);
      return;
    case "pause":
      void sessions.pause(id);
      return;
    case "step-over":
      void sessions.stepOver(id);
      return;
    case "step-into":
      void sessions.stepInto(id);
      return;
    case "smart-step-into":
      void sessions.smartStepInto(id);
      return;
    case "step-out":
      void sessions.stepOut(id);
      return;
    case "apply-code-changes":
      void sessions.applyCodeChanges(id);
      return;
    case "run-to-cursor": {
      const cursor = props.resolveCursor?.();
      if (cursor) void sessions.runToCursor(id, cursor.path, cursor.line);
      return;
    }
    case "restart":
      void sessions.restart(id);
      return;
    case "stop":
      void sessions.stop(id);
      return;
    case "breakpoints":
      openBreakpoints();
      return;
    case "mute":
      void sessions.setBreakpointsMuted(id, !muted.value);
      return;
  }
}

function selectFrame(frame: DapStackFrame) {
  sessions.selectFrame(props.sessionId, frame.id);
  // A JDK, dependency or in-memory source has no local file: selecting the
  // frame already opens its read-only text through the debugger.
  if (isVirtualDebugSource(frame.source)) return;
  const path = frame.source?.path;
  if (path && frame.line > 0) emit("open-location", path.replace(/\\/g, "/"), frame.line);
}
</script>

<template>
  <section v-if="session" ref="root" class="debug-window">
    <header class="debug-window__header">
      <DebugToolbar
        :session="session"
        :muted="muted"
        :can-run-to-cursor="Boolean(resolveCursor)"
        :can-apply-code-changes="canApplyCodeChanges"
        @action="handleAction"
      />
      <el-radio-group v-if="!session.noDebug" v-model="view" size="small" class="debug-window__views">
        <el-radio-button value="debugger">{{ t("debug.window.debugger") }}</el-radio-button>
        <el-radio-button value="console">{{ t("debug.window.console") }}</el-radio-button>
      </el-radio-group>
      <span class="debug-window__state" :class="`is-${session.state}`" role="status">{{ stateLabel }}</span>
    </header>
    <DebugExceptionBar v-if="!session.noDebug" :session="session" />
    <el-alert
      v-if="session.lastError"
      class="debug-window__error"
      type="error"
      :closable="false"
      show-icon
      :title="session.lastError"
    />
    <template v-if="!session.noDebug && session.hotCodeReplace">
      <el-alert
        v-if="session.pendingCodeChanges"
        class="debug-window__hot-code is-pending"
        type="info"
        :closable="false"
        show-icon
        :title="t('debug.hotCodeReplace.pending')"
      />
      <el-alert
        v-if="session.hotCodeReplaceMessage"
        class="debug-window__hot-code"
        :class="`is-${session.hotCodeReplaceMessage.kind}`"
        :type="session.hotCodeReplaceMessage.kind"
        show-icon
        @close="sessions.dismissHotCodeReplaceMessage(sessionId)"
      >
        <template #title>
          <span class="debug-window__hot-code-text" :title="session.hotCodeReplaceMessage.text">{{
            shortHotCodeReplaceMessage(session.hotCodeReplaceMessage.text)
          }}</span>
        </template>
      </el-alert>
    </template>
    <div v-if="!session.noDebug" v-show="!showConsole" class="debug-window__debugger">
      <AppSplitPane
        v-model="framesWidth"
        :min="160"
        :max="(size: number) => Math.max(160, size - 200)"
        :second-min="200"
        :aria-label="t('debug.frames.title')"
      >
        <template #first>
          <DebugFramesPane
            :session="session"
            @select-thread="sessions.selectThread(sessionId, $event)"
            @select-frame="selectFrame"
          />
        </template>
        <template #second>
          <DebugVariablesPane :session="session" />
        </template>
      </AppSplitPane>
    </div>
    <div v-show="showConsole" class="debug-window__console">
      <DebugConsolePane :session="session" :active="active && showConsole" />
    </div>
    <DebugStepTargetsMenu
      v-if="stepTargets && stepTargetsAnchor"
      :targets="stepTargets.targets"
      :x="stepTargetsAnchor.x"
      :y="stepTargetsAnchor.y"
      @select="(targetId: number) => sessions.chooseStepTarget(sessionId, targetId)"
      @close="sessions.chooseStepTarget(sessionId, null)"
    />
    <DebugBreakpointsDialog
      v-if="breakpointsMounted"
      v-model="breakpointsOpen"
      :project-root="session.projectRoot"
      @open-location="(path: string, line: number) => emit('open-location', path, line)"
      @open-address="(address: string, type: string, line: number) => emit('open-address', address, type, line)"
    />
  </section>
</template>
