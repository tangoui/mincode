<script setup lang="ts">
// Text or JSON result of a host plugin service call (services/plugin-services.ts):
// copy, save as a file, open in a read-only workbench tab
// (services/plugin-service-result-tabs.ts), and for a call started from an
// editor selection, replace the selection or insert below it.
import { computed } from "vue";
import { useRouter } from "vue-router";
import { useVueI18n } from "@/i18n/messages";
import { openPluginServiceResultTab } from "@/services/plugin-service-result-tabs";
import {
  applyPluginServiceResult,
  closePluginServiceResult,
  copyPluginServiceResult,
  pluginServiceResult,
  savePluginServiceResultAs,
} from "@/services/plugin-services";
import { useWorkbenchStore } from "@/stores/workbench";

const { t, intlLocale } = useVueI18n();
const router = useRouter();
const workbench = useWorkbenchStore();

const visible = computed({
  get: () => pluginServiceResult.value !== null,
  set: (next: boolean) => {
    if (!next) closePluginServiceResult();
  },
});

/** Moves the result into a read-only tab titled "<service> <time>"; the dialog closes once the tab opens. */
function openInNewTab() {
  const result = pluginServiceResult.value;
  if (!result) return;
  const time = new Date().toLocaleTimeString(intlLocale.value, { hour12: false });
  const tabTitle = t("plugins.services.result.tabTitle", { name: result.title, time });
  if (openPluginServiceResultTab(workbench, router, result, tabTitle)) closePluginServiceResult();
}
</script>

<template>
  <el-dialog
    v-model="visible"
    :title="t('plugins.services.result.title')"
    width="560px"
    append-to-body
  >
    <template v-if="pluginServiceResult">
      <p class="app-plugin-service-result__subtitle">
        {{ pluginServiceResult.title }}
      </p>
      <el-input
        :model-value="pluginServiceResult.text"
        type="textarea"
        readonly
        :rows="14"
        resize="none"
        :input-style="{ fontFamily: 'var(--app-font-mono)', fontSize: '12px' }"
        :aria-label="t('plugins.services.result.label')"
      />
    </template>
    <template #footer>
      <el-button @click="copyPluginServiceResult()">
        {{ t("plugins.services.result.copy") }}
      </el-button>
      <el-button @click="savePluginServiceResultAs()">
        {{ t("plugins.services.result.saveAs") }}
      </el-button>
      <el-button @click="openInNewTab">
        {{ t("plugins.services.result.openInTab") }}
      </el-button>
      <template v-if="pluginServiceResult?.selection">
        <el-button @click="applyPluginServiceResult('insertBelow')">
          {{ t("plugins.services.result.insertBelow") }}
        </el-button>
        <el-button type="primary" @click="applyPluginServiceResult('replace')">
          {{ t("plugins.services.result.replace") }}
        </el-button>
      </template>
      <el-button v-else type="primary" @click="closePluginServiceResult">
        {{ t("plugins.services.result.close") }}
      </el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.app-plugin-service-result__subtitle {
  margin: 0 0 8px;
  font-size: 13px;
  font-weight: 600;
  color: var(--app-text);
}
</style>
