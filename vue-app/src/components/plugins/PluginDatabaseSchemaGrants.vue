<script setup lang="ts">
import { onMounted, ref, watch } from "vue";
import { ElMessage } from "element-plus";
import { Coin } from "@element-plus/icons-vue";
import { useVueI18n } from "@/i18n/messages";
import { getPluginDatabaseSchemaGrants, revokePluginDatabaseSchemaGrant, type PluginDatabaseSchemaGrant } from "@/services/plugin-service";

// Saved connections this plugin may read the structure of without asking
// (database.schema). Granted in the approval dialog, revoked here.
const props = defineProps<{ pluginId: string }>();
const { t } = useVueI18n();
const grants = ref<PluginDatabaseSchemaGrant[]>([]);
const failed = ref(false);
const saving = ref(false);

async function load() {
  try {
    grants.value = await getPluginDatabaseSchemaGrants(props.pluginId);
    failed.value = false;
  } catch {
    failed.value = true;
  }
}
onMounted(load);
watch(() => props.pluginId, load);

async function revoke(connectionId = "") {
  if (saving.value) return;
  saving.value = true;
  try {
    grants.value = await revokePluginDatabaseSchemaGrant(props.pluginId, connectionId);
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
      <el-button text :icon="Coin">
        {{ t("plugins.databaseSchemaGrants.button") }}<small class="plugin-db-grants__count">{{ grants.length }}</small>
      </el-button>
    </template>
    <div class="plugin-db-grants">
      <div class="plugin-db-grants__title">{{ t("plugins.databaseSchemaGrants.title") }}</div>
      <p class="plugin-db-grants__hint">{{ t("plugins.databaseSchemaGrants.hint") }}</p>
      <div v-for="grant in grants" :key="grant.id" class="plugin-db-grants__row">
        <span class="plugin-db-grants__name">{{ grant.name }}<small v-if="grant.kind">{{ grant.kind }}</small></span>
        <el-button text type="danger" size="small" :disabled="saving" @click="revoke(grant.id)">{{ t("plugins.callGrants.revoke") }}</el-button>
      </div>
      <p v-if="failed" class="plugin-db-grants__hint">{{ t("plugins.callGrants.loadFailed") }} <el-button text size="small" @click="load">{{ t("plugins.callGrants.retry") }}</el-button></p>
      <p v-else-if="!grants.length" class="plugin-db-grants__hint">{{ t("plugins.databaseSchemaGrants.empty") }}</p>
    </div>
  </el-popover>
</template>

<style scoped>
.plugin-db-grants__count { margin-left: 4px; color: var(--app-text-muted); font-size: 11px; font-weight: 500; }
.plugin-db-grants { display: grid; gap: 8px; }
.plugin-db-grants__title { color: var(--app-text); font-size: 13px; font-weight: 600; }
.plugin-db-grants__hint { margin: 0; color: var(--app-text-muted); font-size: 12px; line-height: 1.5; }
.plugin-db-grants__row { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.plugin-db-grants__name { min-width: 0; overflow: hidden; color: var(--app-text); font-size: 13px; text-overflow: ellipsis; white-space: nowrap; }
.plugin-db-grants__name small { margin-left: 6px; color: var(--app-text-muted); font-size: 11px; }
</style>
