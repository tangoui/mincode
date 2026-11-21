<script setup lang="ts">
/**
 * The exception a session is paused on (stopped reason "exception"), from
 * DAP `exceptionInfo` when the adapter supports it: its type and
 * description, and the stack trace on request. Shows nothing otherwise, and
 * nothing when the request fails (the detail is optional).
 */
import { computed, ref, watch } from "vue";
import type { DebugSessionView } from "@/domain/debug/session-model";
import type { DapExceptionInfo } from "@/domain/debug/types";
import { useVueI18n } from "@/i18n/messages";
import { useDebugSessionsStore } from "@/stores/debug-sessions";

const props = defineProps<{ session: DebugSessionView }>();

const { t } = useVueI18n();
const sessions = useDebugSessionsStore();

const info = ref<DapExceptionInfo | null>(null);
const stackOpen = ref(false);
/** Bumped on every change of what to show: an older answer is dropped. */
let seq = 0;

/** The thread to ask about while paused on an exception the adapter can describe, else null. */
const exceptionThreadId = computed<number | null>(() => {
  const view = props.session;
  if (view.state !== "paused" || view.stoppedReason !== "exception") return null;
  if (view.capabilities.supportsExceptionInfoRequest !== true) return null;
  return view.stoppedThreadId ?? view.selectedThreadId ?? null;
});

/**
 * The description without the type the title already shows: adapters often
 * describe an exception as "<exceptionId>: <message>", some as that text in
 * double quotes (a string value).
 */
const description = computed(() => {
  const current = info.value;
  if (!current) return "";
  let text = current.description ?? current.details?.message ?? "";
  if (text.length >= 2 && text.startsWith('"') && text.endsWith('"') && text.slice(1).startsWith(`${current.exceptionId}: `)) {
    text = text.slice(1, -1);
  }
  const prefix = `${current.exceptionId}: `;
  if (current.exceptionId && text.startsWith(prefix)) return text.slice(prefix.length).trim();
  return text === current.exceptionId ? "" : text;
});
const stackTrace = computed(() => info.value?.details?.stackTrace ?? "");

// Keyed by the stop too: the same thread stopping on another exception (no
// `continued` in between) asks again.
watch(
  [() => props.session.id, exceptionThreadId, () => props.session.stopSeq],
  async ([id, threadId]) => {
    const mine = ++seq;
    info.value = null;
    stackOpen.value = false;
    if (threadId === null) return;
    const result = await sessions.loadExceptionInfo(id, threadId);
    if (mine === seq) info.value = result;
  },
  { immediate: true }
);
</script>

<template>
  <el-alert
    v-if="info"
    class="debug-window__exception"
    type="warning"
    :closable="false"
    show-icon
    :title="t('debug.exception.title', { type: info.exceptionId })"
  >
    <p v-if="description" class="debug-window__exception-description">{{ description }}</p>
    <el-button
      v-if="stackTrace"
      class="debug-window__exception-toggle"
      link
      type="primary"
      size="small"
      :aria-expanded="stackOpen"
      @click="stackOpen = !stackOpen"
    >
      {{ stackOpen ? t("debug.exception.hideStack") : t("debug.exception.showStack") }}
    </el-button>
    <pre v-if="stackOpen && stackTrace" class="debug-window__exception-stack">{{ stackTrace }}</pre>
  </el-alert>
</template>
