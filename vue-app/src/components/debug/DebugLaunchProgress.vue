<script setup lang="ts">
/**
 * A launch in flight in the debug panel (before its session exists): the
 * steps (checking the debugger, starting the language service for debuggers
 * it hosts, resolving the launch, launching), the language service's own
 * status text while it starts (shown verbatim; e.g. import or download
 * progress), a hint when it has not progressed for a while, and Cancel
 * (until the start request is sent; once clicked it says "cancelling" until
 * the launch ends, since some steps cannot be interrupted). The tab disappears when the launch
 * ends: started, failed, declined or cancelled.
 */
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { DEBUG_LAUNCH_STALL_MS, debugLaunchSteps } from "@/domain/debug/launch-progress";
import { useVueI18n } from "@/i18n/messages";
import type { DebugLaunchView } from "@/stores/debug-sessions";

const props = withDefaults(defineProps<{ launch: DebugLaunchView; visible?: boolean }>(), { visible: true });
/** `shown`: the user can now see this panel and its Cancel (true), or no longer (false, also when a shown one unmounts). */
const emit = defineEmits<{ cancel: []; shown: [shown: boolean] }>();

const { t } = useVueI18n();

const steps = computed(() => debugLaunchSteps(props.launch.languageService));
const activeIndex = computed(() => Math.max(0, steps.value.indexOf(props.launch.step)));
const title = computed(() =>
  t(props.launch.noDebug ? "debug.launch.preparingRun" : "debug.launch.preparingDebug", { name: props.launch.configName }),
);

/**
 * No step change nor status text for DEBUG_LAUNCH_STALL_MS while the language
 * service starts; time a confirmation dialog is open does not count (closing
 * it is progress, so the countdown starts again then).
 */
const stalled = ref(false);
let stallTimer: ReturnType<typeof setTimeout> | undefined;
watch(
  () => [props.launch.progressAt, props.launch.step, props.launch.confirming] as const,
  ([at, step, confirming]) => {
    clearTimeout(stallTimer);
    stalled.value = false;
    if (step !== "languageService" || confirming) return;
    stallTimer = setTimeout(() => (stalled.value = true), Math.max(0, at + DEBUG_LAUNCH_STALL_MS - Date.now()));
  },
  { immediate: true },
);
onBeforeUnmount(() => clearTimeout(stallTimer));

// Transitions only (a hidden panel mounting says nothing), so a store can count panels.
watch(
  () => props.visible,
  (visible, previous) => {
    if (previous !== undefined || visible) emit("shown", visible);
  },
  { immediate: true },
);
onBeforeUnmount(() => {
  if (props.visible) emit("shown", false);
});
</script>

<template>
  <section class="debug-launch" :aria-label="title">
    <header class="debug-launch__header">
      <span class="debug-launch__title">{{ title }}</span>
      <el-button size="small" :disabled="launch.step === 'launching' || launch.cancelling" data-action="cancel-launch" @click="emit('cancel')">
        {{ launch.cancelling ? t("debug.launch.cancelling") : t("common.cancel") }}
      </el-button>
    </header>
    <el-steps class="debug-launch__steps" :active="activeIndex" finish-status="success" process-status="process" simple>
      <el-step v-for="step in steps" :key="step" :title="t(`debug.launch.steps.${step}`)" />
    </el-steps>
    <p class="debug-launch__status" role="status" aria-live="polite" :title="launch.status || undefined">
      {{ launch.status || t(`debug.launch.status.${launch.step}`) }}
    </p>
    <el-alert
      v-if="stalled"
      class="debug-launch__slow"
      type="warning"
      :closable="false"
      show-icon
      :title="t('debug.launch.slow')"
    />
  </section>
</template>
