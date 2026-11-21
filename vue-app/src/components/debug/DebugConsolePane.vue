<script setup lang="ts">
/**
 * A session's console: the debuggee's terminal when the adapter ran it in
 * one (attached, never started or closed here; the latest is shown, earlier
 * ones stay reachable through a switcher), the adapter's output, and a REPL
 * input (with completions) that evaluates in the selected frame.
 */
import { computed, defineAsyncComponent, nextTick, ref, watch } from "vue";
import type { ScrollbarInstance } from "element-plus";
import { useVueI18n, type MessageKey } from "@/i18n/messages";
import { debugErrorText } from "@/domain/debug/errors";
import type { DebugSessionView } from "@/domain/debug/session-model";
import { useDebugSessionsStore } from "@/stores/debug-sessions";
import DebugCompletionInput from "@/components/debug/DebugCompletionInput.vue";

const LocalTerminal = defineAsyncComponent(() => import("@/components/terminal/LocalTerminal.vue"));

/** REPL entries kept per console. */
const MAX_REPL_ENTRIES = 500;

interface ConsoleLine {
  key: string;
  category: string;
  text: string;
}

const props = defineProps<{
  session: DebugSessionView;
  /** The console is the visible pane (the terminal renders only then). */
  active: boolean;
}>();

const { t } = useVueI18n();
const sessions = useDebugSessionsStore();

const replEntries = ref<ConsoleLine[]>([]);
const replInput = ref("");
const evaluating = ref(false);
const scrollbar = ref<ScrollbarInstance>();
let replSeq = 0;

const terminalIds = computed(() => {
  const ids = props.session.consoleTerminalIds ?? [];
  const latest = props.session.consoleTerminalId;
  return latest && !ids.includes(latest) ? [...ids, latest] : ids;
});
const pickedTerminalId = ref<string>();
/** The terminal on screen: the picked one, else the latest. */
const shownTerminalId = computed(() =>
  pickedTerminalId.value && terminalIds.value.includes(pickedTerminalId.value) ? pickedTerminalId.value : props.session.consoleTerminalId
);
const terminalOptions = computed(() =>
  terminalIds.value.map((id, index) => ({ value: id, label: t("debug.console.terminalOption", { n: index + 1 }) }))
);

// A new runInTerminal takes the screen.
watch(
  () => props.session.consoleTerminalId,
  () => {
    pickedTerminalId.value = undefined;
  }
);

/** Where the program runs, when the Console does not simply show its terminal. */
const notice = computed(() => {
  const value = props.session.consoleNotice;
  if (!value) return undefined;
  if (value.kind === "external") {
    return {
      type: "info" as const,
      text: value.terminal
        ? t("debug.console.externalTerminal", { terminal: value.terminal })
        : t("debug.console.externalTerminalUnnamed"),
    };
  }
  return {
    type: "warning" as const,
    text: t("debug.console.externalFallback", { reason: t(fallbackReasonKey(value.code)) }),
    // The backend's explanation (bounded there), shown as the detail.
    detail: value.message,
  };
});

/** The translated reason of an external-terminal fallback, by its backend code. */
function fallbackReasonKey(code: string): MessageKey {
  if (code === "unavailable") return "debug.console.externalFallbackReason.unavailable";
  if (code === "unsupported") return "debug.console.externalFallbackReason.unsupported";
  return "debug.console.externalFallbackReason.failed";
}

const lines = computed<ConsoleLine[]>(() => [
  // seq is stable per entry, so dropping the oldest at the cap re-renders nothing else.
  ...props.session.output.map((line) => ({ key: `out:${line.seq}`, category: line.category, text: line.text })),
  // REPL use happens while paused, when the program writes nothing, so the entries follow the output.
  ...replEntries.value,
]);

const replDisabledReason = computed(() => {
  if (props.session.state === "ended") return t("debug.console.replEnded");
  if (props.session.state !== "paused") return t("debug.console.replNotPaused");
  return "";
});

function pushRepl(category: string, text: string) {
  replSeq += 1;
  replEntries.value = [...replEntries.value, { key: `repl:${replSeq}`, category, text }].slice(-MAX_REPL_ENTRIES);
}

async function evaluateRepl() {
  const expression = replInput.value.trim();
  if (!expression || replDisabledReason.value || evaluating.value) return;
  replInput.value = "";
  pushRepl("repl-input", `> ${expression}`);
  evaluating.value = true;
  try {
    const result = await sessions.evaluate(props.session.id, expression, "repl");
    pushRepl("repl-result", result.result);
  } catch (error) {
    pushRepl("repl-error", debugErrorText(error));
  } finally {
    evaluating.value = false;
  }
}

// The last key (not the length: it stays put at the cap) follows new entries.
watch(
  () => lines.value[lines.value.length - 1]?.key,
  async () => {
    await nextTick();
    scrollbar.value?.setScrollTop(Number.MAX_SAFE_INTEGER);
  }
);
</script>

<template>
  <section class="debug-console" :class="{ 'has-terminal': Boolean(shownTerminalId) }">
    <el-alert
      v-if="notice"
      class="debug-console__notice"
      :type="notice.type"
      :title="notice.text"
      :description="'detail' in notice ? notice.detail : undefined"
      :closable="false"
      show-icon
      role="status"
    />
    <div v-if="terminalIds.length > 1" class="debug-console__terminals">
      <el-select
        :model-value="shownTerminalId"
        class="debug-console__terminal-select"
        size="small"
        :aria-label="t('debug.console.terminalSwitch')"
        @update:model-value="pickedTerminalId = $event"
      >
        <el-option v-for="option in terminalOptions" :key="option.value" :value="option.value" :label="option.label" />
      </el-select>
    </div>
    <div v-if="shownTerminalId" class="debug-console__terminal">
      <LocalTerminal
        :key="shownTerminalId"
        :session-id="shownTerminalId"
        :attach-only-session-id="shownTerminalId"
        :active="active"
        :can-close="false"
        :can-split="false"
      />
    </div>
    <el-scrollbar
      v-if="!shownTerminalId || lines.length"
      ref="scrollbar"
      class="debug-console__log"
      role="log"
      :aria-label="t('debug.window.console')"
    >
      <div v-if="!lines.length && !notice" class="debug-pane-empty">{{ t("debug.console.noOutput") }}</div>
      <template v-else>
        <!-- v-memo: an output batch renders only its new lines, not the whole console. -->
        <pre
          v-for="line in lines"
          :key="line.key"
          v-memo="[line.key]"
          class="debug-console__line"
          :class="`is-${line.category}`"
        >{{ line.text }}</pre>
      </template>
    </el-scrollbar>
    <DebugCompletionInput
      v-if="!session.noDebug"
      v-model="replInput"
      :session="session"
      class="debug-console__repl"
      size="small"
      :disabled="Boolean(replDisabledReason)"
      :placeholder="replDisabledReason || t('debug.console.replPlaceholder')"
      :aria-label="t('debug.console.replPlaceholder')"
      @submit="evaluateRepl"
    >
      <template #prefix><span class="debug-console__prompt" aria-hidden="true">›</span></template>
    </DebugCompletionInput>
  </section>
</template>
