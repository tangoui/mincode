<script setup lang="ts">
import { computed } from "vue";
import { Bell, WarningFilled } from "@element-plus/icons-vue";
import { attentionReasonKey, type TerminalAttention } from "@/domain/terminal/terminal-attention";
import { useVueI18n } from "@/i18n/messages";

// Bell on a tab whose terminal finished, stopped or waits for the user while they
// looked elsewhere (macOS Terminal style). Disappears once the tab is looked at.
const props = defineProps<{ attention: TerminalAttention }>();
const { t } = useVueI18n();

const label = computed(() => {
  const reason = t(attentionReasonKey(props.attention.reason));
  return props.attention.message ? `${reason} · ${props.attention.message}` : reason;
});
const urgent = computed(() => props.attention.reason === "failed" || props.attention.reason === "waiting_approval");
</script>

<template>
  <el-tooltip :content="label" placement="bottom" :show-after="300">
    <el-icon
      class="app-terminal-attention"
      :class="{ 'is-urgent': urgent }"
      role="img"
      :aria-label="label"
    ><WarningFilled v-if="attention.reason === 'failed'" /><Bell v-else /></el-icon>
  </el-tooltip>
</template>
