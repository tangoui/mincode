<script setup lang="ts">
import { watch } from "vue";
import { FolderAdd } from "@element-plus/icons-vue";
import AppSearchField from "@/components/ui/AppSearchField.vue";
import { installSourceFromSearch, type CatalogFilter, type CatalogSource, type CatalogStatus } from "@/domain/plugins/catalog";
import { useVueI18n } from "@/i18n/messages";

const props = defineProps<{ filters: CatalogFilter; sourcePath: string; busy: boolean }>();
const emit = defineEmits<{
  "update:filters": [value: CatalogFilter];
  "update:sourcePath": [value: string];
  choosePackage: [];
  chooseDirectory: [];
  preview: [];
}>();
const { t } = useVueI18n();

function updateFilter<K extends keyof CatalogFilter>(key: K, value: CatalogFilter[K]) {
  emit("update:filters", { ...props.filters, [key]: value });
}
// The search box doubles as the install source field: a path or HTTPS URL typed (or picked) there
// becomes the source to preview, and ordinary text stays a catalog search.
watch(() => props.filters.search, (search) => {
  const source = installSourceFromSearch(search);
  if (source !== props.sourcePath) emit("update:sourcePath", source);
});
watch(() => props.sourcePath, (source) => {
  if (source && source !== installSourceFromSearch(props.filters.search)) updateFilter("search", source);
});
function choosePackage() {
  if (!props.busy) emit("choosePackage");
}
function installCommand(command: string) {
  if (!props.busy && command === "directory") emit("chooseDirectory");
}
function preview() {
  if (!props.busy && props.sourcePath.trim()) emit("preview");
}
</script>

<template>
  <div class="app-plugin-toolbar">
    <div class="app-plugin-toolbar__row">
      <div class="app-plugin-toolbar__query">
        <AppSearchField
          :model-value="filters.search"
          :placeholder="t('plugins.searchOrSource')"
          clearable
          :clear-label="t('common.clear')"
          @update:model-value="updateFilter('search', $event)"
          @submit="preview"
        />
        <el-button
          v-if="sourcePath"
          class="app-plugin-toolbar__preview"
          size="small"
          type="primary"
          :disabled="busy"
          @click="preview"
        >{{ t('plugins.preview') }}</el-button>
      </div>
      <div class="app-plugin-toolbar__actions">
        <el-select
          class="app-plugin-toolbar__filter"
          :model-value="filters.source"
          :aria-label="t('plugins.filterSource')"
          size="small"
          @update:model-value="updateFilter('source', $event as CatalogSource)"
        >
          <el-option v-for="source in (['all', 'local', 'cloud'] as const)" :key="source" :value="source" :label="t(`plugins.catalogSource.${source}`)" />
        </el-select>
        <el-select
          class="app-plugin-toolbar__filter"
          :model-value="filters.status"
          :aria-label="t('plugins.filterStatus')"
          size="small"
          @update:model-value="updateFilter('status', $event as CatalogStatus)"
        >
          <el-option v-for="status in (['all', 'installed', 'upgradable', 'available'] as const)" :key="status" :value="status" :label="t(`plugins.catalogStatus.${status}`)" />
        </el-select>
        <!-- One native picker cannot offer both files and folders on Windows/Linux, so folders get their own entry. -->
        <el-dropdown
          class="app-plugin-toolbar__install"
          split-button
          type="primary"
          size="small"
          :disabled="busy"
          trigger="click"
          @click="choosePackage"
          @command="installCommand"
        >
          <el-icon><FolderAdd /></el-icon>
          {{ t('plugins.installPlugin') }}
          <template #dropdown>
            <el-dropdown-menu>
              <el-dropdown-item command="directory" :disabled="busy">{{ t('plugins.installFromDirectory') }}</el-dropdown-item>
            </el-dropdown-menu>
          </template>
        </el-dropdown>
      </div>
    </div>
  </div>
</template>
