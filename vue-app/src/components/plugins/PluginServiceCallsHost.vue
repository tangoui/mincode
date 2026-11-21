<script setup lang="ts">
// Shows host plugin service calls (services/plugin-services.ts) stacked in the
// bottom-right corner: progress and cancel while running, then the result
// entry, the failure, or "cancelled". Also hosts the services' parameter form
// (shared with plugin commands) and the result dialog. Mounted lazily by
// PluginServicesRoot.vue after the first request and kept mounted afterwards.
import { Close } from "@element-plus/icons-vue";
import PluginCommandParamsDialog from "@/components/plugins/PluginCommandParamsDialog.vue";
import PluginServiceResultDialog from "@/components/plugins/PluginServiceResultDialog.vue";
import { formatDataBytes } from "@/domain/settings/data-usage";
import { useVueI18n } from "@/i18n/messages";
import {
  cancelPluginServiceCall,
  cancelPluginServiceParams,
  confirmPluginServiceParams,
  dismissPluginServiceCall,
  openPluginServiceResult,
  pluginServiceCallErrorText,
  pluginServiceCalls,
  pluginServiceParamRequest,
  savePluginServiceOutputs,
  type PluginServiceCallView,
} from "@/services/plugin-services";

const { t } = useVueI18n();

function statusText(call: PluginServiceCallView) {
  if (call.state === "cancelled") return t("plugins.services.cancelled");
  // Cancelling is not a rollback: an unconfirmed outcome is never reported as cancelled.
  if (call.state === "failed") return call.unconfirmed ? t("plugins.services.unconfirmed") : pluginServiceCallErrorText(call);
  if (call.state === "succeeded") return t("plugins.services.done");
  if (call.cancelling) return t("plugins.services.cancelling");
  return call.phase === "input" ? t("plugins.services.phaseInput") : t("plugins.services.running");
}

/** Bytes read of the remote file while the host reads it (no total yet: nothing, the bar is indeterminate). */
function inputBytesText(call: PluginServiceCallView) {
  const total = call.progress?.total;
  if (call.phase !== "input" || total === undefined) return "";
  return t("plugins.services.phaseInputBytes", { done: formatDataBytes(call.progress?.completed ?? 0), total: formatDataBytes(total) });
}

function hasTextResult(call: PluginServiceCallView) {
  return call.state === "succeeded" && !call.files.length && call.output !== undefined;
}
</script>

<template>
  <section
    v-if="pluginServiceCalls.length"
    class="app-plugin-service-calls"
    :aria-label="t('plugins.services.callsLabel')"
  >
    <el-card
      v-for="call in pluginServiceCalls"
      :key="call.callId"
      shadow="never"
      class="app-plugin-service-call"
      :body-style="{ padding: '10px 12px' }"
    >
      <div class="app-plugin-service-call__header">
        <div class="app-plugin-service-call__heading">
          <span class="app-plugin-service-call__title">{{ call.title }}</span>
          <span class="app-plugin-service-call__provider">{{ call.pluginName }}</span>
        </div>
        <el-button
          v-if="call.state !== 'running'"
          link
          :icon="Close"
          :aria-label="t('plugins.services.dismiss')"
          :title="t('plugins.services.dismiss')"
          @click="dismissPluginServiceCall(call.callId)"
        />
      </div>
      <template v-if="call.state === 'running'">
        <el-progress
          :percentage="call.percent ?? 50"
          :indeterminate="call.percent === undefined"
          :show-text="call.percent !== undefined"
          :stroke-width="6"
          :aria-label="statusText(call)"
        />
        <p v-if="call.progress?.message" class="app-plugin-service-call__message">
          {{ call.progress.message }}
        </p>
        <p v-if="inputBytesText(call)" class="app-plugin-service-call__message">
          {{ inputBytesText(call) }}
        </p>
        <div class="app-plugin-service-call__actions">
          <span class="app-plugin-service-call__status" aria-live="polite">{{ statusText(call) }}</span>
          <el-button
            size="small"
            :loading="call.cancelling"
            :disabled="call.cancelling"
            @click="cancelPluginServiceCall(call.callId)"
          >
            {{ t("plugins.services.cancel") }}
          </el-button>
        </div>
      </template>
      <template v-else>
        <p
          class="app-plugin-service-call__status"
          :class="{ 'is-failed': call.state === 'failed' }"
          role="status"
        >
          {{ statusText(call) }}
        </p>
        <div v-if="call.state === 'succeeded'" class="app-plugin-service-call__actions">
          <el-button
            v-if="hasTextResult(call)"
            size="small"
            type="primary"
            @click="openPluginServiceResult(call.callId)"
          >
            {{ t("plugins.services.viewResult") }}
          </el-button>
          <el-button
            v-else-if="call.files.length"
            size="small"
            type="primary"
            :loading="call.saving"
            @click="savePluginServiceOutputs(call.callId)"
          >
            {{ t("plugins.services.saveFiles") }}
          </el-button>
        </div>
      </template>
    </el-card>
  </section>
  <PluginCommandParamsDialog
    :request="pluginServiceParamRequest"
    @confirm="confirmPluginServiceParams"
    @cancel="cancelPluginServiceParams"
  />
  <PluginServiceResultDialog />
</template>

<style scoped>
.app-plugin-service-calls {
  position: fixed;
  z-index: 1950;
  right: 16px;
  bottom: 36px;
  display: flex;
  flex-direction: column;
  gap: 8px;
  width: 300px;
  max-height: calc(100vh - 120px);
  overflow-y: auto;
}

.app-plugin-service-call {
  border-color: var(--app-border);
  background: var(--app-surface-raised);
}

.app-plugin-service-call__header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 8px;
  margin-bottom: 6px;
}

.app-plugin-service-call__heading {
  display: flex;
  min-width: 0;
  flex-direction: column;
}

.app-plugin-service-call__title {
  overflow: hidden;
  font-size: 13px;
  font-weight: 600;
  color: var(--app-text);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.app-plugin-service-call__provider {
  overflow: hidden;
  font-size: 12px;
  color: var(--app-text-muted);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.app-plugin-service-call__message {
  margin: 6px 0 0;
  font-size: 12px;
  line-height: 1.5;
  color: var(--app-text-secondary);
  overflow-wrap: anywhere;
  white-space: pre-wrap;
}

.app-plugin-service-call__status {
  margin: 0;
  font-size: 12px;
  line-height: 1.5;
  color: var(--app-text-secondary);
  overflow-wrap: anywhere;
}

.app-plugin-service-call__status.is-failed {
  color: var(--app-danger);
}

.app-plugin-service-call__actions {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 8px;
}

.app-plugin-service-call__actions .app-plugin-service-call__status {
  flex: 1;
  min-width: 0;
}
</style>
