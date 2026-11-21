<script setup lang="ts">
import { onMounted, ref, watch } from "vue";
import { ElMessage } from "element-plus";
import { Connection } from "@element-plus/icons-vue";
import { useVueI18n } from "@/i18n/messages";
import { getPluginCallGrants, revokePluginCallGrant } from "@/services/plugin-service";

// Plugins this plugin may call without asking (host.plugins.invoke). Only
// read-only calls are ever remembered; the user grants them in the approval
// dialog and revokes them here.
const props = defineProps<{ pluginId: string; pluginNames: Record<string, string> }>();
const { t } = useVueI18n();
const targets = ref<string[]>([]);
const failed = ref(false);
const saving = ref(false);

// Grants change from the approval dialog while this page is open, so the
// list is read again every time the popover opens.
async function load() {
  try {
    targets.value = await getPluginCallGrants(props.pluginId);
    failed.value = false;
  } catch {
    failed.value = true;
  }
}
onMounted(load);
watch(() => props.pluginId, load);

async function revoke(target = "") {
  if (saving.value) return;
  saving.value = true;
  try {
    targets.value = await revokePluginCallGrant(props.pluginId, target);
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : String(error));
  } finally {
    saving.value = false;
  }
}
</script>

<template>
  <el-popover trigger="click" placement="top-end" :width="300" @show="load">
    <template #reference>
      <el-button text :icon="Connection">
        {{ t("plugins.callGrants.button") }}<small class="plugin-call-grants__count">{{ targets.length }}</small>
      </el-button>
    </template>
    <div class="plugin-call-grants">
      <div class="plugin-call-grants__title">{{ t("plugins.callGrants.title") }}</div>
      <p class="plugin-call-grants__hint">{{ t("plugins.callGrants.hint") }}</p>
      <div v-for="target in targets" :key="target" class="plugin-call-grants__row">
        <span class="plugin-call-grants__name">{{ pluginNames[target] || target }}</span>
        <el-button text type="danger" size="small" :disabled="saving" @click="revoke(target)">{{ t("plugins.callGrants.revoke") }}</el-button>
      </div>
      <p v-if="failed" class="plugin-call-grants__hint">{{ t("plugins.callGrants.loadFailed") }} <el-button text size="small" @click="load">{{ t("plugins.callGrants.retry") }}</el-button></p>
      <p v-else-if="!targets.length" class="plugin-call-grants__hint">{{ t("plugins.callGrants.empty") }}</p>
    </div>
  </el-popover>
</template>

<style scoped>
.plugin-call-grants__count { margin-left: 4px; color: var(--app-text-muted); font-size: 11px; font-weight: 500; }
.plugin-call-grants { display: grid; gap: 8px; }
.plugin-call-grants__title { color: var(--app-text); font-size: 13px; font-weight: 600; }
.plugin-call-grants__hint { margin: 0; color: var(--app-text-muted); font-size: 12px; line-height: 1.5; }
.plugin-call-grants__row { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.plugin-call-grants__name { min-width: 0; overflow: hidden; color: var(--app-text); font-size: 13px; text-overflow: ellipsis; white-space: nowrap; }
</style>
