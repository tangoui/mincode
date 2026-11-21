<script setup lang="ts">
import { computed } from "vue";
import { ElMessage } from "element-plus";
import { useVueI18n } from "@/i18n/messages";
import { isPluginCategory, pluginHomepage } from "@/domain/plugins/catalog";
import { openSystemBrowser } from "@/services/web-automation-service";
import { isDesktopRuntime } from "@/services/desktop-bridge";

const props = defineProps<{
  metadata: { id: string; name: string; version: string; description?: string; author?: string; category?: string; tags?: string[]; homepage?: string; capabilities?: string[]; apiVersion?: number };
  /** "market": an install preview of a package downloaded from the plugin market. */
  source: "official" | "local" | "market";
}>();
const { t } = useVueI18n();
const homepage = computed(() => pluginHomepage(props.metadata.homepage));
const category = computed(() => isPluginCategory(props.metadata.category) ? props.metadata.category : "other");
async function openHomepage() {
  if (!homepage.value) return;
  if (!isDesktopRuntime()) { ElMessage.warning(t("common.desktopRequired")); return; }
  try { await openSystemBrowser(homepage.value); }
  catch (error) { ElMessage.error(t("plugins.homepageFailed", { error: String(error) })); }
}
</script>

<template>
  <el-descriptions :column="1" :label-width="120" border size="small" class="app-plugin-metadata">
    <el-descriptions-item :label="t('plugins.name')">{{ metadata.name }}</el-descriptions-item>
    <el-descriptions-item :label="t('plugins.id')">{{ metadata.id }}</el-descriptions-item>
    <el-descriptions-item :label="t('plugins.version')">{{ metadata.version }}</el-descriptions-item>
    <el-descriptions-item :label="t('plugins.description')">{{ metadata.description || t('plugins.noCapabilities') }}</el-descriptions-item>
    <el-descriptions-item :label="t('plugins.author')">{{ metadata.author || t('plugins.noCapabilities') }}</el-descriptions-item>
    <el-descriptions-item :label="t('plugins.filterSource')">{{ source === "market" ? t("plugins.previewFromMarket") : t(`plugins.catalogSource.${source}`) }}</el-descriptions-item>
    <el-descriptions-item :label="t('plugins.category')">{{ t(`plugins.category.${category}`) }}</el-descriptions-item>
    <el-descriptions-item :label="t('plugins.tags')">
      <el-tag v-for="tag in metadata.tags" :key="tag" size="small" effect="plain">{{ tag }}</el-tag>
      <span v-if="!metadata.tags?.length">{{ t('plugins.noCapabilities') }}</span>
    </el-descriptions-item>
    <el-descriptions-item v-if="metadata.apiVersion" :label="t('plugins.apiVersion')">{{ metadata.apiVersion }}</el-descriptions-item>
    <el-descriptions-item :label="t('plugins.permissions')">
      <el-tag v-for="capability in metadata.capabilities" :key="capability" size="small" type="warning" effect="plain">{{ capability }}</el-tag>
      <span v-if="!metadata.capabilities?.length">{{ t('plugins.noCapabilities') }}</span>
    </el-descriptions-item>
    <el-descriptions-item v-if="homepage" :label="t('plugins.homepage')">
      <el-link tabindex="0" :aria-label="t('plugins.openHomepage')" @keydown.enter.prevent="openHomepage" @click="openHomepage">{{ homepage }}</el-link>
    </el-descriptions-item>
  </el-descriptions>
</template>
