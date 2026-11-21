<script setup lang="ts">
import { computed, onActivated, onBeforeUnmount, onDeactivated, ref, watch } from "vue";
import { Loading, WarningFilled } from "@element-plus/icons-vue";
import { useVueI18n } from "@/i18n/messages";
import {
  aggregateTerminalActivity,
  isTerminalActivityActive,
  terminalActivityDurationMs,
  type TerminalActivityPhase,
  type TerminalPaneActivity,
} from "@/domain/terminal/terminal-activity";

// The tab's activity clock lives here so its per-second tick re-renders only this
// indicator instead of the whole sessions workspace.
const props = defineProps<{
  activities: Record<string, TerminalPaneActivity>;
  createdAt: number;
}>();

const { t } = useVueI18n();
const now = ref(Date.now());
const paused = ref(false);
let timer: ReturnType<typeof setInterval> | undefined;

// 传入每秒推进的时钟，过期的“正在执行”才会自己停下来，而不是一直转圈。
const activity = computed(() => aggregateTerminalActivity(props.activities, props.createdAt, now.value));
const spinning = computed(() => isTerminalActivityActive(activity.value.phase));

function stopClock() {
  if (timer) clearInterval(timer);
  timer = undefined;
}

// Only an active phase can decay to quiet, so an idle tab needs no clock at all.
watch([spinning, paused], ([isActive, isPaused]) => {
  stopClock();
  if (!isActive || isPaused) return;
  now.value = Date.now();
  timer = setInterval(() => {
    now.value = Date.now();
  }, 1_000);
}, { immediate: true });

onActivated(() => {
  now.value = Date.now();
  paused.value = false;
});
onDeactivated(() => {
  paused.value = true;
});
onBeforeUnmount(stopClock);

function refreshClock() {
  now.value = Date.now();
}

function phaseLabel(phase: TerminalActivityPhase) {
  return t(`sessions.terminalActivity.${phase}` as Parameters<typeof t>[0]);
}

function formatActivityDuration(milliseconds: number) {
  const totalSeconds = Math.max(0, Math.floor(milliseconds / 1_000));
  if (totalSeconds < 60) return t("sessions.terminalActivity.seconds", { count: totalSeconds });
  const minutes = Math.floor(totalSeconds / 60);
  if (minutes < 60) return t("sessions.terminalActivity.minutes", { count: minutes });
  return t("sessions.terminalActivity.hours", {
    hours: Math.floor(minutes / 60),
    minutes: minutes % 60,
  });
}

function formatTime(value: number) {
  if (!value) return t("sessions.timeUnknown");
  return new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(value);
}

const tooltip = computed(() => {
  const current = activity.value;
  const detail = [phaseLabel(current.phase)];
  if (current.summary) detail.push(current.summary);
  detail.push(t("sessions.terminalActivity.duration", {
    value: formatActivityDuration(terminalActivityDurationMs(current, now.value)),
  }));
  detail.push(t("sessions.terminalActivity.updated", { value: formatTime(current.updatedAt) }));
  detail.push(t(current.source === "claude-hook"
    ? "sessions.terminalActivity.sourceClaude"
    : "sessions.terminalActivity.sourceGeneric"));
  return detail.join(" · ");
});

// Only running and failure states get an icon. "Finished, not looked at yet" is the
// terminal attention bell next to it, and no icon means the terminal is not running.
const icon = computed(() => {
  const phase = activity.value.phase;
  if (phase === "failed" || phase === "waiting_approval") return WarningFilled;
  if (isTerminalActivityActive(phase)) return Loading;
  return undefined;
});
</script>

<template>
  <!-- An idle tab's clock is stopped; refresh it on hover so the tooltip duration is current. -->
  <el-tooltip v-if="icon" :content="tooltip" placement="bottom" @before-show="refreshClock">
    <span
      class="sessions-live-tab__activity"
      :class="[`is-${activity.phase}`, { 'is-spinning': spinning }]"
      role="img"
      :aria-label="phaseLabel(activity.phase)"
      @click.stop
    >
      <el-icon><component :is="icon" /></el-icon>
    </span>
  </el-tooltip>
</template>
