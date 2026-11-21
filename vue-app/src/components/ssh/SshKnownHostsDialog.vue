<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { Delete, Refresh } from "@element-plus/icons-vue";
import { ElMessage, ElMessageBox } from "element-plus";
import AppSearchField from "@/components/ui/AppSearchField.vue";
import { useVueI18n } from "@/i18n/messages";
import {
  listKnownHosts,
  removeKnownHost,
  type SshKnownHostEntry,
} from "@/services/ssh-security-service";

const props = defineProps<{ modelValue: boolean }>();
const emit = defineEmits<{ "update:modelValue": [value: boolean] }>();
const { t } = useVueI18n();
const loading = ref(false);
const path = ref("");
const query = ref("");
const entries = ref<SshKnownHostEntry[]>([]);
const filteredEntries = computed(() => {
  const needle = query.value.trim().toLowerCase();
  if (!needle) return entries.value;
  return entries.value.filter((entry) =>
    [entry.hosts.join(" "), entry.algorithm, entry.fingerprint, entry.marker ?? ""]
      .join(" ")
      .toLowerCase()
      .includes(needle)
  );
});

async function refresh() {
  loading.value = true;
  try {
    const result = await listKnownHosts();
    path.value = result.path;
    entries.value = result.entries;
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : t("ssh.knownHosts.loadFailed"));
  } finally {
    loading.value = false;
  }
}

async function remove(entry: SshKnownHostEntry) {
  try {
    await ElMessageBox.confirm(
      t("ssh.knownHosts.removeConfirm"),
      t("ssh.knownHosts.title"),
      {
        type: "warning",
        confirmButtonText: t("common.delete"),
        cancelButtonText: t("common.cancel"),
      }
    );
    await removeKnownHost(entry.id);
    await refresh();
    ElMessage.success(t("ssh.knownHosts.removed"));
  } catch (error) {
    if (error instanceof Error) ElMessage.error(error.message);
  }
}

watch(
  () => props.modelValue,
  (visible) => {
    if (visible) void refresh();
    else query.value = "";
  },
  { immediate: true }
);
</script>

<template>
  <el-dialog
    append-to-body
    class="ssh-security-dialog"
    :model-value="modelValue"
    width="min(820px, calc(100vw - 40px))"
    align-center
    destroy-on-close
    @update:model-value="emit('update:modelValue', $event)"
  >
    <template #header>
      <div class="ssh-security-dialog__header">
        <div class="ssh-security-dialog__heading">
          <strong>{{ t("ssh.knownHosts.title") }}</strong>
          <code v-if="path" :title="path">{{ path }}</code>
        </div>
        <div class="ssh-security-dialog__toolbar">
          <AppSearchField
            v-model="query"
            :placeholder="t('ssh.knownHosts.search')"
            :aria-label="t('ssh.knownHosts.search')"
            clearable
          />
          <el-button size="small" :icon="Refresh" :loading="loading" @click="refresh">{{ t("common.refresh") }}</el-button>
        </div>
      </div>
    </template>
    <div v-loading="loading" class="ssh-security-dialog__body">
      <el-table
        :data="filteredEntries"
        max-height="460"
        table-layout="fixed"
        :empty-text="t('ssh.knownHosts.empty')"
      >
        <el-table-column :label="t('ssh.knownHosts.host')" min-width="190">
          <template #default="{ row }">
            <span class="ssh-security-dialog__hosts">{{ row.hosts.join(", ") }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="algorithm" :label="t('ssh.knownHosts.algorithm')" width="150" />
        <el-table-column :label="t('ssh.knownHosts.fingerprint')" min-width="260">
          <template #default="{ row }">
            <code class="ssh-security-dialog__fingerprint">{{ row.fingerprint }}</code>
          </template>
        </el-table-column>
        <el-table-column width="52" fixed="right" align="center">
          <template #default="{ row }">
            <el-button
              class="ssh-security-dialog__delete"
              text
              circle
              type="danger"
              :aria-label="t('common.delete')"
              :title="t('common.delete')"
              @click="remove(row)"
            >
              <el-icon><Delete /></el-icon>
            </el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>
  </el-dialog>
</template>

<style scoped>
:global(.ssh-security-dialog) {
  overflow: hidden;
  border: 1px solid var(--app-border);
  border-radius: 10px;
  background: var(--app-surface);
}

:global(.ssh-security-dialog .el-dialog__header) {
  margin: 0;
  border-bottom: 1px solid var(--app-border);
  padding: 14px 48px 12px 18px;
}

:global(.ssh-security-dialog .el-dialog__body) {
  padding: 14px 18px 18px;
}

:global(.ssh-security-dialog .el-dialog__headerbtn) {
  top: 5px;
  right: 8px;
}

.ssh-security-dialog__header {
  display: flex;
  min-width: 0;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
}

.ssh-security-dialog__heading {
  display: flex;
  flex: 1;
  min-width: 0;
  flex-direction: column;
  gap: 4px;
}

.ssh-security-dialog__heading code {
  overflow: hidden;
  color: var(--app-text-muted);
  font-size: 11px;
  font-weight: 400;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.ssh-security-dialog__body {
  min-height: 220px;
}

.ssh-security-dialog__toolbar {
  display: flex;
  min-width: 0;
  flex: 0 1 360px;
  align-items: center;
  justify-content: flex-end;
  gap: 8px;
}

.ssh-security-dialog__toolbar .app-search-field {
  min-width: 0;
  flex: 1;
}

.ssh-security-dialog__toolbar > .el-button {
  height: var(--app-search-field-height);
  margin: 0;
  flex: none;
}

.ssh-security-dialog__hosts,
.ssh-security-dialog__fingerprint {
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.ssh-security-dialog__fingerprint {
  color: var(--app-text-regular);
  font-size: 11px;
}

.ssh-security-dialog__delete {
  width: 28px;
  height: 28px;
  margin: 0;
  padding: 0;
}

@media (max-width: 680px) {
  .ssh-security-dialog__header {
    align-items: stretch;
    flex-direction: column;
    gap: 10px;
  }

  .ssh-security-dialog__toolbar {
    flex: none;
    width: 100%;
  }
}
</style>
