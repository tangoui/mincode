<script setup lang="ts">
/**
 * Plugin manager "Services" tab (design §12): every service effective in this
 * workspace with its providers. A service with several providers gets a
 * default provider choice; "Ask every time" clears the default so the host
 * asks at each call. Not the "Capabilities" tab, which is the permission
 * catalog (PluginCapabilityCatalog.vue).
 */
import { computed, ref } from "vue";
import { ElMessage } from "element-plus";
import { usePluginServiceLabels } from "@/composables/use-plugin-service-labels";
import { serviceCatalogGroups, type PluginServiceEntry } from "@/domain/plugins/services";
import { useVueI18n } from "@/i18n/messages";
import { parseContributionError } from "@/services/plugin-contribution-requests";
import {
  loadPluginServicesOnce,
  pluginServiceErrorText,
  pluginServices,
  pluginServicesLoad,
  setPluginServiceDefault,
} from "@/services/plugin-services";
import { isDesktopRuntime } from "@/services/desktop-bridge";

const { t } = useVueI18n();
const { entryTitle, riskType, riskLabel, shapeText } = usePluginServiceLabels();
const desktop = isDesktopRuntime();
const groups = computed(() => serviceCatalogGroups(pluginServices.value));
const saving = ref("");

const loading = computed(() => pluginServicesLoad.value.status === "loading");
const loadError = computed(() => (pluginServicesLoad.value.status === "error" ? pluginServicesLoad.value.error ?? "" : undefined));

// A popped-out plugin manager has no service sync; read the list here then.
// Started during setup so the first render already shows the loading state.
void loadPluginServicesOnce();

function retryLoad() {
  void loadPluginServicesOnce();
}

function providerKey(entry: PluginServiceEntry) {
  return `${entry.pluginId}/${entry.entryId}`;
}

async function chooseDefault(service: string, value: unknown) {
  if (saving.value) return;
  const pluginId = typeof value === "string" ? value : "";
  saving.value = service;
  try {
    await setPluginServiceDefault(service, pluginId);
  } catch (error) {
    ElMessage.error({ message: pluginServiceErrorText(parseContributionError(error), service), grouping: true, showClose: true });
  } finally {
    saving.value = "";
  }
}
</script>

<template>
  <el-scrollbar class="plugin-services-catalog-scroll">
    <main class="plugin-services-catalog" :aria-busy="loading">
      <div class="plugin-services-catalog__intro">
        <strong>{{ t("plugins.services.catalog.title") }}</strong>
        <span>{{ t("plugins.services.catalog.count", { count: groups.length }) }}</span>
      </div>
      <el-alert
        v-if="loadError !== undefined"
        type="error"
        show-icon
        :closable="false"
        :title="t('plugins.services.catalog.loadFailed', { detail: loadError })"
      >
        <el-button size="small" native-type="button" @click="retryLoad">{{ t("plugins.services.catalog.retry") }}</el-button>
      </el-alert>
      <el-skeleton v-if="loading && !groups.length" :rows="3" animated :aria-label="t('plugins.services.catalog.loading')" />
      <el-empty
        v-else-if="!groups.length && loadError === undefined"
        :image-size="64"
        :description="desktop ? t('plugins.services.catalog.empty') : t('plugins.services.desktopOnly')"
      />
      <section
        v-for="group in groups"
        :key="group.service"
        class="plugin-services-catalog__group"
        :aria-label="group.service"
      >
        <header class="plugin-services-catalog__header">
          <h3>{{ group.service }}</h3>
          <label v-if="group.providers.length >= 2" class="plugin-services-catalog__default">
            <span>{{ t("plugins.services.default.label") }}</span>
            <el-select
              :model-value="group.defaultPluginId"
              :empty-values="[null, undefined]"
              size="small"
              :disabled="saving !== '' && saving !== group.service"
              :loading="saving === group.service"
              :aria-label="t('plugins.services.default.aria', { service: group.service })"
              @change="chooseDefault(group.service, $event)"
            >
              <el-option value="" :label="t('plugins.services.default.ask')" />
              <el-option
                v-for="provider in group.providers"
                :key="provider.pluginId"
                :value="provider.pluginId"
                :label="provider.pluginName"
              />
            </el-select>
          </label>
        </header>
        <el-table :data="group.providers" :row-key="providerKey" size="small">
          <el-table-column :label="t('plugins.services.column.title')" min-width="160">
            <template #default="{ row }">
              <strong class="plugin-services-catalog__title">{{ entryTitle(row) }}</strong>
              <small v-if="row.description" class="plugin-services-catalog__description">{{ row.description }}</small>
            </template>
          </el-table-column>
          <el-table-column :label="t('plugins.services.column.provider')" min-width="140">
            <template #default="{ row }">
              <span class="plugin-services-catalog__provider">
                <span>{{ row.pluginName }}</span>
                <el-tag v-if="row.isDefault" size="small" effect="plain">{{ t("plugins.services.default.tag") }}</el-tag>
              </span>
            </template>
          </el-table-column>
          <el-table-column :label="t('plugins.services.column.risk')" width="110">
            <template #default="{ row }">
              <el-tag size="small" :type="riskType(row.risk)">{{ riskLabel(row.risk) }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column :label="t('plugins.services.column.input')" min-width="150">
            <template #default="{ row }">{{ shapeText(row.input) }}</template>
          </el-table-column>
          <el-table-column :label="t('plugins.services.column.output')" min-width="130">
            <template #default="{ row }">{{ shapeText(row.output) }}</template>
          </el-table-column>
        </el-table>
      </section>
    </main>
  </el-scrollbar>
</template>
