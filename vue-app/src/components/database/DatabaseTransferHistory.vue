<script setup lang="ts">
import { computed, onBeforeUnmount, watch } from "vue";
import { ElMessage } from "element-plus";
import { useWorkspaceStateScope } from "@/composables/use-workspace-state-scope";
import { useDatabaseTransferTasks } from "@/stores/database-transfer-tasks";
import { cancelTransferTask } from "@/services/database-transfer-service";
import { transferTerminal } from "@/domain/database/transfer-tasks";
import { useVueI18n } from "@/i18n/messages";
const { t } = useVueI18n();
const scope = useWorkspaceStateScope();
const store = useDatabaseTransferTasks();
let stop: (() => void) | undefined;
watch(scope.scopeId, (id) => { stop?.(); stop = store.attach("import", id); }, { immediate: true });
onBeforeUnmount(() => stop?.());
const tasks = computed(() => Object.values(store.tasks).filter(task => task.kind === "import" && task.workspaceId === scope.scopeId.value).sort((a,b) => b.createdAt-a.createdAt));
const pending = computed(() => Object.entries(store.pendingImports).filter(([, value]) => value.workspaceId === scope.scopeId.value));
async function cancel(id: string) { try { store.merge(await cancelTransferTask(id)); } catch (cause) { ElMessage.error(String(cause)); } }
async function retry(id: string) { try { await store.retryImport(id); } catch (cause) { ElMessage.error(String(cause)); } }
</script>
<template>
  <el-collapse v-if="tasks.length || pending.length || store.error" class="db-transfer-history">
    <el-collapse-item name="imports" :title="t('database.transfer.importHistory')">
      <el-alert v-if="store.error" :closable="false" type="error" :title="store.error" />
      <el-scrollbar max-height="220px">
        <div v-for="[id] in pending" :key="id">
          <span>{{ t('database.transfer.admissionUnknown') }}</span>
          <el-button size="small" @click="retry(id)">{{ t('database.transfer.retryAdmission') }}</el-button>
        </div>
        <div v-for="task in tasks" :key="task.id">
          <span>{{ t('database.restore.taskState', { state: task.state, records: task.result.records, certainty: task.result.certainty }) }}</span>
          <small v-if="task.errorCode"> — {{ task.errorCode }}</small>
          <el-button v-if="!transferTerminal(task)" size="small" @click="cancel(task.id)">{{ t('common.cancel') }}</el-button>
        </div>
      </el-scrollbar>
    </el-collapse-item>
  </el-collapse>
</template>
