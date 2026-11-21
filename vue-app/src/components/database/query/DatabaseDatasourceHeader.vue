<script setup lang="ts">
import { computed, defineAsyncComponent, ref } from "vue";
import { ElMessage } from "element-plus";
import { Coin, Connection, Document, Plus, Refresh, Tickets, Timer } from "@element-plus/icons-vue";
import { databaseTypeLabels, type DatabaseConnection } from "@/domain/database/types";
import type { SqlSafetyRecord } from "@/domain/database/sql-safety";
import { useVueI18n } from "@/i18n/messages";
import { useDatabaseWorkspaceStore } from "@/stores/database-workspace";

defineOptions({ name: "DatabaseDatasourceHeader" });

const props = defineProps<{
  connection: DatabaseConnection;
}>();

const emit = defineEmits<{
  (e: "schema-sync"): void;
  (e: "backup"): void;
  (e: "new-connection"): void;
  (e: "refresh"): void;
  (e: "edit", connection: DatabaseConnection): void;
  /** 操作记录里的一次撤回已提交（或可能部分生效）。 */
  (e: "undone", record: SqlSafetyRecord): void;
}>();

const { t } = useVueI18n();
/** SQL 安全网操作记录抽屉：打开时才加载（含撤回对话框），不进工作区主包。 */
const safetyHistoryVisible = ref(false);
const SqlSafetyHistoryPanel = defineAsyncComponent({
  loader: () => import("@/components/database/safety/SqlSafetyHistoryPanel.vue"),
  onError(error, _retry, fail) {
    safetyHistoryVisible.value = false;
    ElMessage.error(t("database.safety.history.panelLoadFailed", { error: String(error) }));
    fail();
  },
});
const databaseStore = useDatabaseWorkspaceStore();
// 插件驱动连接显示“驱动 · 插件”，且不支持结构同步（后端返回 unsupported:）。
const driverDescriptor = computed(() => databaseStore.connectionDescriptor(props.connection));
const typeLabel = computed(() => {
  const descriptor = driverDescriptor.value;
  if (descriptor?.source !== "plugin") return databaseTypeLabels[props.connection.dbType].toUpperCase();
  return t("pluginContrib.databaseDrivers.runtime.via", {
    driver: descriptor.title,
    plugin: descriptor.pluginName || descriptor.pluginId || "",
  });
});
const schemaSyncDisabled = computed(
  () => databaseStore.isRedis || driverDescriptor.value?.source === "plugin"
);
</script>

<template>
  <div class="db-datasource-header">
    <div class="db-datasource-title">
      <el-icon><Coin /></el-icon><strong>{{ props.connection.name }}</strong
      ><span>{{ typeLabel }}</span
      ><i /><small>{{
        props.connection.filePath || `${props.connection.host}:${props.connection.port}`
      }}</small>
    </div>
    <div class="db-datasource-actions">
      <el-button text native-type="button"
        class="db-datasource-actions__schema-sync"
        :title="t('database.schemaSync.title')"
        :aria-label="t('database.schemaSync.title')"
        :disabled="schemaSyncDisabled"
        @click="emit('schema-sync')"
      >
        <el-icon><Tickets /></el-icon><span>{{ t("database.schemaSync.action") }}</span>
      </el-button>
      <el-button text native-type="button"
        class="db-datasource-actions__backup"
        :title="t('database.backup.title')"
        :aria-label="t('database.backup.title')"
        :disabled="databaseStore.isRedis"
        @click="emit('backup')"
      >
        <el-icon><Timer /></el-icon><span>{{ t("database.backup.action") }}</span>
      </el-button>
      <el-button text native-type="button"
        class="db-datasource-actions__safety-history"
        :title="t('database.safety.history.title')"
        :aria-label="t('database.safety.history.title')"
        @click="safetyHistoryVisible = true"
      >
        <el-icon><Document /></el-icon><span>{{ t("database.safety.history.open") }}</span>
      </el-button>
      <el-button text native-type="button" :title="t('database.newConnection')" @click="emit('new-connection')">
        <el-icon><Plus /></el-icon>
      </el-button>
      <el-button text native-type="button" :title="t('database.refreshObjects')" @click="emit('refresh')">
        <el-icon><Refresh /></el-icon>
      </el-button>
      <el-button text native-type="button"
        :title="t('database.editConnection')"
        @click="emit('edit', props.connection)"
      >
        <el-icon><Connection /></el-icon>
      </el-button>
    </div>
    <SqlSafetyHistoryPanel
      v-if="safetyHistoryVisible"
      v-model="safetyHistoryVisible"
      :connection="props.connection"
      @undone="emit('undone', $event)"
    />
  </div>
</template>
