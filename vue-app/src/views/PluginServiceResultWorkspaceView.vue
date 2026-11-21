<script setup lang="ts">
// Read-only tab of a plugin service result (services/plugin-service-result-tabs.ts):
// the text or pretty-printed JSON in a read-only Monaco viewer, with copy and
// save as a file. The content lives in memory only; once its tab has closed
// (or after a restart) the route shows that the result is gone.
import { computed, defineAsyncComponent } from "vue";
import { useRoute } from "vue-router";
import { DocumentCopy, Download } from "@element-plus/icons-vue";
import { useVueI18n } from "@/i18n/messages";
import { pluginServiceResultDocument } from "@/services/plugin-service-result-tabs";
import { copyPluginServiceResult, savePluginServiceResultAs } from "@/services/plugin-services";

const ReadonlyCodeViewer = defineAsyncComponent(() => import("@/components/ui/ReadonlyCodeViewer.vue"));

const { t, intlLocale } = useVueI18n();
const route = useRoute();

const resultId = computed(() => (typeof route.params.resultId === "string" ? route.params.resultId : ""));
const result = computed(() => (resultId.value ? pluginServiceResultDocument(resultId.value) : undefined));
const createdAt = computed(() =>
  result.value ? new Date(result.value.createdAt).toLocaleTimeString(intlLocale.value, { hour12: false }) : "",
);
</script>

<template>
  <section class="app-service-result-view">
    <template v-if="result">
      <header class="app-service-result-view__header">
        <div class="app-service-result-view__identity">
          <span class="app-service-result-view__title">{{ result.title }}</span>
          <span class="app-service-result-view__meta">
            {{ createdAt }} · {{ t(result.kind === "json" ? "plugins.services.result.kindJson" : "plugins.services.result.kindText") }} ·
            {{ t("plugins.services.result.readOnly") }}
          </span>
        </div>
        <div class="app-service-result-view__actions">
          <el-button size="small" :icon="DocumentCopy" @click="copyPluginServiceResult(result)">
            {{ t("plugins.services.result.copy") }}
          </el-button>
          <el-button size="small" :icon="Download" @click="savePluginServiceResultAs(result)">
            {{ t("plugins.services.result.saveAs") }}
          </el-button>
        </div>
      </header>
      <div class="app-service-result-view__body">
        <ReadonlyCodeViewer
          :key="result.id"
          :value="result.text"
          :language="result.language"
          :ariaLabel="t('plugins.services.result.label')"
        />
      </div>
    </template>
    <el-result
      v-else
      class="app-service-result-view__gone"
      icon="info"
      :title="t('plugins.services.result.gone')"
      :sub-title="t('plugins.services.result.goneDetail')"
    />
  </section>
</template>

<style scoped>
.app-service-result-view {
  display: flex;
  flex-direction: column;
  width: 100%;
  height: 100%;
  min-height: 0;
  overflow: hidden;
  background: var(--app-surface);
}

.app-service-result-view__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 8px 12px;
  border-bottom: 1px solid var(--app-border);
}

.app-service-result-view__identity {
  display: flex;
  flex-direction: column;
  min-width: 0;
}

.app-service-result-view__title {
  overflow: hidden;
  font-size: 13px;
  font-weight: 600;
  color: var(--app-text);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.app-service-result-view__meta {
  font-size: 12px;
  color: var(--app-text-muted);
}

.app-service-result-view__actions {
  display: flex;
  flex-shrink: 0;
  gap: 8px;
}

.app-service-result-view__body {
  flex: 1;
  min-height: 0;
  background: var(--app-code-surface);
}

.app-service-result-view__gone {
  margin: auto;
}
</style>
