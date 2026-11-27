<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useVueI18n } from "@/i18n/messages";
import { invokeBackend, isDesktopRuntime, nativeErrorMessage } from "@/services/desktop-bridge";
import { agentControlTrustChange, invokeAgentControl, setAgentControlTrusted, type AgentControlLogEntry, type AgentControlStatus } from "@/services/agent-control-service";

const { t, intlLocale } = useVueI18n();
const status = ref<AgentControlStatus>();
const entries = ref<AgentControlLogEntry[]>([]);
const mcpCommand = ref("");
const busy = ref(false);
const refreshing = ref(false);
const error = ref("");
let disposed = false;
watch(agentControlTrustChange, (next) => { if (next) status.value = next; });

async function refresh() {
  if (refreshing.value) return;
  refreshing.value = true;
  error.value = "";
  try {
    const [nextStatus, nextEntries] = await Promise.all([
      invokeAgentControl<AgentControlStatus>("agent_control_status"),
      invokeAgentControl<AgentControlLogEntry[]>("agent_control_log"),
    ]);
    if (!disposed) {
      status.value = nextStatus;
      entries.value = nextEntries;
    }
  } catch (cause) {
    if (!disposed) error.value = nativeErrorMessage(cause);
  } finally {
    refreshing.value = false;
  }
}

async function toggle(enabled: boolean) {
  if (busy.value || refreshing.value || status.value?.trusted === enabled) return;
  busy.value = true;
  error.value = "";
  try {
    const next = await setAgentControlTrusted(enabled);
    if (!disposed) status.value = next;
  } catch (cause) {
    if (!disposed) error.value = nativeErrorMessage(cause);
  } finally {
    busy.value = false;
  }
}

function time(value: string) {
  return new Date(value).toLocaleString(intlLocale.value);
}

onMounted(() => {
  if (!isDesktopRuntime()) return;
  void refresh();
  void invokeBackend<{ executable: string }>("external_mcp_client_executable").then(({ executable }) => {
    if (!disposed) mcpCommand.value = JSON.stringify({ command: executable, args: ["--external-local-mcp"] });
  }).catch(() => undefined);
});
onBeforeUnmount(() => { disposed = true; });
</script>

<template>
  <section v-if="isDesktopRuntime()" class="settings-card settings-agent-control">
    <div class="settings-data-row">
      <span>
        <strong>{{ t("settings.agentControl.trusted") }}</strong>
        <small class="settings-page-note">{{ t("settings.agentControl.trustDescription") }}</small>
      </span>
      <el-switch
        :model-value="status?.trusted ?? false"
        :loading="busy"
        :disabled="!status || busy || refreshing"
        :aria-label="t('settings.agentControl.trusted')"
        @change="toggle(Boolean($event))"
      />
    </div>
    <el-alert v-if="error" type="error" :closable="false" :title="t('settings.agentControl.failed', { message: error })" />
    <div v-if="mcpCommand && status?.trusted" class="settings-data-row">
      <span>
        <strong>{{ t("settings.agentControl.mcpClient") }}</strong>
        <code class="settings-page-note">{{ mcpCommand }}</code>
      </span>
    </div>
    <div class="settings-data-row">
      <strong>{{ t("settings.agentControl.history") }}</strong>
      <el-button :disabled="busy" :loading="refreshing" @click="refresh">{{ t("settings.agentControl.refresh") }}</el-button>
    </div>
    <p v-if="!entries.length && !refreshing && !error" class="settings-page-note">{{ t("settings.agentControl.noCalls") }}</p>
    <el-table v-if="entries.length" :data="entries" max-height="300" size="small">
      <el-table-column :label="t('settings.agentControl.time')" min-width="150">
        <template #default="{ row }">{{ time(row.timestamp) }}</template>
      </el-table-column>
      <el-table-column :label="t('settings.agentControl.source')" width="112">
        <template #default="{ row }">{{ t(row.source === 'pane' ? 'settings.agentControl.pane' : 'settings.agentControl.external') }}</template>
      </el-table-column>
      <el-table-column prop="tool" :label="t('settings.agentControl.tool')" min-width="150" show-overflow-tooltip />
      <el-table-column :label="t('settings.agentControl.result')" width="94">
        <template #default="{ row }">{{ t(row.success ? 'settings.agentControl.success' : 'settings.agentControl.failure') }}</template>
      </el-table-column>
      <el-table-column :label="t('settings.agentControl.duration')" width="104">
        <template #default="{ row }">{{ t("settings.agentControl.millis", { value: row.durationMs }) }}</template>
      </el-table-column>
    </el-table>
  </section>
</template>
