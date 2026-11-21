<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { Bell, WarningFilled } from "@element-plus/icons-vue";
import { attentionReasonKey, attentionShortKey } from "@/domain/terminal/terminal-attention";
import { useVueI18n } from "@/i18n/messages";
import { useTerminalAttentionStore } from "@/stores/terminal-attention";

// The pane's own attention chip in its floating toolbar, so a split shows which
// pane finished or waits. A new attention spells itself out for a few seconds,
// then shrinks to a short label. Clicking it acknowledges, as typing does.
const props = defineProps<{ paneId: string }>();
const { t } = useVueI18n();
const terminalAttention = useTerminalAttentionStore();

const attention = computed(() => terminalAttention.items.get(props.paneId));
const detail = computed(() => {
  const current = attention.value;
  if (!current) return "";
  const reason = t(attentionReasonKey(current.reason));
  return current.message ? `${reason} · ${current.message}` : reason;
});
const SPELLED_OUT_MS = 6_000;
const spelledOut = ref(false);
let collapseTimer: ReturnType<typeof setTimeout> | undefined;
// Only a newly raised attention expands; a remounted pane keeps the short label.
watch(
  () => (attention.value ? `${attention.value.reason}:${attention.value.at}` : ""),
  (key) => {
    clearTimeout(collapseTimer);
    spelledOut.value = Boolean(key);
    if (key) collapseTimer = setTimeout(() => (spelledOut.value = false), SPELLED_OUT_MS);
  },
);
onBeforeUnmount(() => clearTimeout(collapseTimer));
const urgent = computed(() => attention.value?.reason === "failed" || attention.value?.reason === "waiting_approval");
</script>

<template>
  <el-tooltip v-if="attention" :content="detail" placement="bottom">
    <el-button
      text
      native-type="button"
      class="terminal-pane-attention"
      :class="{ 'is-urgent': urgent, 'is-spelled-out': spelledOut }"
      :aria-label="detail"
      @click.stop="terminalAttention.dismiss([paneId])"
    >
      <el-icon class="app-terminal-attention" :class="{ 'is-urgent': urgent }"><WarningFilled v-if="attention.reason === 'failed'" /><Bell v-else /></el-icon>
      <span class="terminal-pane-attention__text">{{ spelledOut ? detail : t(attentionShortKey(attention.reason)) }}</span>
    </el-button>
  </el-tooltip>
</template>
