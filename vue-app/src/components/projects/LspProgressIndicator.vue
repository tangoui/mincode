<script setup lang="ts">
/**
 * Language-server readiness in the project editor's run bar: shown while a
 * server starts, scans the project or indexes, so the user can see when
 * navigation and completion become available. Renders nothing when idle.
 * Server-reported text is untrusted and shown as plain text only.
 */
import { computed } from "vue";
import { Loading } from "@element-plus/icons-vue";
import { useVueI18n } from "@/i18n/messages";
import { lspStatusBarProgress } from "@/services/lsp-runtime-state";

const { t } = useVueI18n();

const progress = computed(() => lspStatusBarProgress());
const label = computed(() => {
  const current = progress.value;
  if (!current) return "";
  let text: string;
  if (current.kind === "progress") {
    const title = current.title || current.name;
    text = current.percentage === undefined
      ? title
      : t("project.lsp.statusBar.percent", { title, percentage: current.percentage });
    if (current.message) text += ` · ${current.message}`;
  } else if (current.kind === "scanning") {
    text = t("project.lsp.statusBar.scanning", { name: current.name });
  } else {
    text = t("project.lsp.statusBar.starting", { name: current.name });
  }
  return current.busy > 1 ? `${text} ${t("project.lsp.statusBar.more", { count: current.busy - 1 })}` : text;
});
</script>

<template>
  <el-tooltip
    v-if="progress"
    :content="t('project.lsp.statusBar.tooltip')"
    placement="top"
    :show-after="400"
  >
    <div class="lsp-progress-indicator" role="status" aria-live="polite" :aria-label="label">
      <el-progress
        v-if="progress.kind === 'progress' && progress.percentage !== undefined"
        class="lsp-progress-indicator__bar"
        :percentage="progress.percentage"
        :show-text="false"
        :stroke-width="4"
        aria-hidden="true"
      />
      <el-icon v-else class="lsp-progress-indicator__icon is-loading" aria-hidden="true"><Loading /></el-icon>
      <span class="lsp-progress-indicator__text">{{ label }}</span>
    </div>
  </el-tooltip>
</template>
