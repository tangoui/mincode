<script setup lang="ts">
/**
 * A debug session's actions. Buttons that do not apply to the session's
 * state are disabled; a run without debugging (noDebug) only restarts and stops.
 * Smart Step Into is shown only when the adapter supports stepInTargets;
 * Apply Code Changes only when the session's debugger declares hot code
 * replace (enabled while rebuilt code waits to be applied).
 */
import { computed } from "vue";
import { useVueI18n, type MessageKey } from "@/i18n/messages";
import type { DebugSessionView } from "@/domain/debug/session-model";

export type DebugToolbarAction =
  | "resume"
  | "pause"
  | "step-over"
  | "step-into"
  | "smart-step-into"
  | "step-out"
  | "run-to-cursor"
  | "apply-code-changes"
  | "restart"
  | "stop"
  | "breakpoints"
  | "mute";

const props = defineProps<{
  session: DebugSessionView;
  muted: boolean;
  /** The editor has a cursor in a project file. */
  canRunToCursor: boolean;
  /** Rebuilt code waits to be applied (hot code replace) and no request is in flight. */
  canApplyCodeChanges?: boolean;
}>();

const emit = defineEmits<{ action: [action: DebugToolbarAction] }>();

const { t } = useVueI18n();

interface ToolbarButton {
  action: DebugToolbarAction;
  label: MessageKey;
  disabled: boolean;
  /** SVG path data (24×24, stroked). */
  icon: string;
  filled?: boolean;
  active?: boolean;
  tone?: "run" | "stop";
}

const ICONS = {
  resume: "M7 4.5v15l12-7.5z",
  pause: "M8 5v14M16 5v14",
  stepOver: "M4 14a8 8 0 0 1 15-4M19 4v6h-6M12 19.5h.01",
  stepInto: "M12 3v12M7 10l5 5 5-5M12 20h.01",
  smartStepInto: "M9 3v12M4 10l5 5 5-5M9 20h.01M16 5h5M16 10h5M16 15h5",
  stepOut: "M12 15V3M7 8l5-5 5 5M12 20h.01",
  runToCursor: "M4 12h11M11 7l5 5-5 5M20 5v14",
  applyCodeChanges: "M13 3 5 14h6l-1 7 8-11h-6z",
  restart: "M4 12a8 8 0 1 0 2.4-5.7M4 4v5h5",
  stop: "M6 6h12v12H6z",
  breakpoints: "M12 5a7 7 0 1 0 0 14a7 7 0 1 0 0-14M18 3l3 3",
  mute: "M12 5a7 7 0 1 0 0 14a7 7 0 1 0 0-14M5 5l14 14",
};

const buttons = computed<ToolbarButton[]>(() => {
  const session = props.session;
  const ended = session.state === "ended";
  const paused = session.state === "paused";
  const restartStop: ToolbarButton[] = [
    { action: "restart", label: "debug.toolbar.restart", disabled: false, icon: ICONS.restart, tone: "run" },
    { action: "stop", label: "debug.toolbar.stop", disabled: ended, icon: ICONS.stop, filled: true, tone: "stop" },
  ];
  if (session.noDebug) return restartStop;
  const smartStepInto: ToolbarButton[] =
    session.capabilities.supportsStepInTargetsRequest === true
      ? [{ action: "smart-step-into", label: "debug.toolbar.smartStepInto", disabled: !paused, icon: ICONS.smartStepInto }]
      : [];
  const applyCodeChanges: ToolbarButton[] = session.hotCodeReplace
    ? [{ action: "apply-code-changes", label: "debug.toolbar.applyCodeChanges", disabled: ended || !props.canApplyCodeChanges, icon: ICONS.applyCodeChanges }]
    : [];
  return [
    paused || ended
      ? { action: "resume", label: "debug.toolbar.resume", disabled: !paused, icon: ICONS.resume, filled: true, tone: "run" }
      : { action: "pause", label: "debug.toolbar.pause", disabled: false, icon: ICONS.pause },
    { action: "step-over", label: "debug.toolbar.stepOver", disabled: !paused, icon: ICONS.stepOver },
    { action: "step-into", label: "debug.toolbar.stepInto", disabled: !paused, icon: ICONS.stepInto },
    ...smartStepInto,
    { action: "step-out", label: "debug.toolbar.stepOut", disabled: !paused, icon: ICONS.stepOut },
    { action: "run-to-cursor", label: "debug.toolbar.runToCursor", disabled: !paused || !props.canRunToCursor, icon: ICONS.runToCursor },
    ...applyCodeChanges,
    ...restartStop,
    { action: "breakpoints", label: "debug.toolbar.breakpoints", disabled: false, icon: ICONS.breakpoints },
    {
      action: "mute",
      label: props.muted ? "debug.toolbar.unmute" : "debug.toolbar.mute",
      disabled: ended,
      icon: ICONS.mute,
      active: props.muted,
    },
  ];
});
</script>

<template>
  <div class="debug-toolbar" role="toolbar" :aria-label="t('debug.toolbar.label')">
    <el-tooltip v-for="button in buttons" :key="button.action" :content="t(button.label)" placement="top">
      <el-button
        text
        native-type="button"
        class="debug-toolbar__button"
        :class="[{ 'is-active': button.active }, button.tone ? `is-${button.tone}` : '']"
        :data-action="button.action"
        :aria-label="t(button.label)"
        :aria-pressed="button.action === 'mute' ? muted : undefined"
        :disabled="button.disabled"
        @click="emit('action', button.action)"
      >
        <el-icon>
          <svg
            viewBox="0 0 24 24"
            :fill="button.filled ? 'currentColor' : 'none'"
            stroke="currentColor"
            stroke-width="2"
            stroke-linecap="round"
            stroke-linejoin="round"
            aria-hidden="true"
          >
            <path :d="button.icon" />
          </svg>
        </el-icon>
      </el-button>
    </el-tooltip>
  </div>
</template>
