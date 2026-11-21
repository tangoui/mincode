<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { ElMessage } from "element-plus";
import { FolderOpened, Refresh } from "@element-plus/icons-vue";
import { useVueI18n } from "@/i18n/messages";
import { invokeBackend, isDesktopRuntime, nativeErrorMessage } from "@/services/desktop-bridge";
import {
  dataUsageItemLabel,
  formatDataBytes,
  groupDataUsage,
  type DataUsage,
} from "@/domain/settings/data-usage";

const { t } = useVueI18n();
const native = isDesktopRuntime();
const usage = ref<DataUsage | null>(null);
const loading = ref(false);
const error = ref("");
let disposed = false;
let controller: AbortController | undefined;

const groups = computed(() => (usage.value ? groupDataUsage(usage.value) : []));

async function load() {
  if (!native) return;
  controller?.abort();
  const current = new AbortController();
  controller = current;
  loading.value = true;
  error.value = "";
  try {
    const result = await invokeBackend<DataUsage>("app_data_usage", {}, { signal: current.signal });
    if (!disposed && controller === current) usage.value = result;
  } catch (cause) {
    if (!disposed && controller === current && !current.signal.aborted) error.value = nativeErrorMessage(cause);
  } finally {
    if (!disposed && controller === current) loading.value = false;
  }
}

async function reveal(path: string) {
  try {
    await invokeBackend<void>("local_fs_show_in_file_manager", { path, select: true });
  } catch (cause) {
    ElMessage.error(nativeErrorMessage(cause));
  }
}

onMounted(() => void load());
onBeforeUnmount(() => {
  disposed = true;
  controller?.abort();
});
</script>

<template>
  <div class="settings-data-usage">
    <div class="settings-data-usage__header">
      <div class="settings-section-label">{{ t("settings.data.usage.title") }}</div>
      <el-button
        v-if="native"
        size="small"
        text
        :icon="Refresh"
        :loading="loading"
        :aria-label="t('settings.data.usage.refresh')"
        @click="load"
      >{{ t("settings.data.usage.refresh") }}</el-button>
    </div>
    <p v-if="!native" class="settings-page-note">{{ t("settings.data.usage.desktopOnly") }}</p>
    <p v-else-if="error" class="settings-page-note settings-data-usage__error">
      {{ t("settings.data.usage.failed", { message: error }) }}
    </p>
    <p v-else-if="!usage" class="settings-page-note">{{ t("settings.data.usage.loading") }}</p>
    <template v-else>
      <div class="settings-data-usage__total">
        <span>{{ t("settings.data.usage.total") }}</span>
        <strong>{{ formatDataBytes(usage.totalBytes) }}</strong>
      </div>
      <section
        v-for="group in groups"
        :key="group.group"
        class="settings-data-usage__group"
        :data-group="group.group"
      >
        <div class="settings-data-usage__group-head">
          <span class="settings-data-usage__group-title">
            <strong>{{ t(group.group === "data" ? "settings.data.usage.dataGroup" : "settings.data.usage.cacheGroup") }}</strong>
            <span class="settings-data-usage__size">{{ formatDataBytes(group.bytes) }}</span>
          </span>
          <el-button
            size="small"
            :icon="FolderOpened"
            :aria-label="t('settings.data.usage.reveal')"
            @click="reveal(group.root)"
          >{{ t("settings.data.usage.reveal") }}</el-button>
        </div>
        <code class="settings-data-usage__path" :title="group.root">{{ group.root }}</code>
        <ul class="settings-data-usage__items">
          <li v-for="item in group.items" :key="item.id" :data-item="item.id">
            <span class="settings-data-usage__item-main">
              <span class="settings-data-usage__item-name">{{ t(dataUsageItemLabel(item.id)) }}</span>
              <code class="settings-data-usage__path" :title="item.path">{{ item.path }}</code>
            </span>
            <span class="settings-data-usage__item-meta">
              <span class="settings-data-usage__size">{{ item.exists ? formatDataBytes(item.bytes) : t("settings.data.usage.empty") }}</span>
              <small v-if="item.exists">{{ t("settings.data.usage.files", { count: item.files }) }}</small>
            </span>
            <el-button
              size="small"
              text
              :icon="FolderOpened"
              :disabled="!item.exists"
              :aria-label="t('settings.data.usage.revealItem', { name: t(dataUsageItemLabel(item.id)) })"
              @click="reveal(item.path)"
            />
          </li>
        </ul>
      </section>
    </template>
  </div>
</template>
