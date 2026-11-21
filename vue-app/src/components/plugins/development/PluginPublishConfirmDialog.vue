<script setup lang="ts">
import { computed, ref } from "vue";
import { ElAlert, ElButton, ElDescriptions, ElDescriptionsItem, ElDialog, ElLink, ElProgress, ElTable, ElTableColumn, ElText, ElTooltip } from "element-plus";
import { useVueI18n, type MessageKey } from "@/i18n/messages";
import { isPluginPublishTerminal, metadataChangeRows, releaseNotesExcerpt, type PluginPublishRecord } from "@/domain/plugins/publish-summary";
import { cancelPluginPublish, confirmPluginPublish, refreshPluginPublish, rejectPluginPublish } from "@/services/plugin-publish-service";
import { requestOpenSettings } from "@/services/settings-navigation";

const props = defineProps<{ record: PluginPublishRecord; queued: number }>();
const emit = defineEmits<{ close: [] }>();
const { t } = useVueI18n();
const busy = ref(false);
const commandFailed = ref(false);
const commandErrorKey = ref<MessageKey>("plugins.publish.commandFailed");
const terminal = computed(() => isPluginPublishTerminal(props.record.status));
const changes = computed(() => metadataChangeRows(props.record.metadataChanges));
const reviewText = computed(() => {
  const status = props.record.result?.reviewStatus;
  return t(status === "pending" ? "plugins.publish.reviewPending" : status === "not_submitted" ? "plugins.publish.reviewDraft" : "plugins.publish.reviewUnknown");
});
async function command(action: (id: string) => Promise<unknown>) {
  if (busy.value) return;
  busy.value = true; commandFailed.value = false;
  try { await action(props.record.id); } catch (error) { commandFailed.value = true; commandErrorKey.value = String(error).includes("publish_busy") ? "plugins.publish.error.busy" : "plugins.publish.commandFailed"; } finally { busy.value = false; }
}
function errorText() {
  const codes = ["invalid_request", "path_outside", "package_invalid", "package_changed", "not_signed_in", "account_changed", "confirmation_changed", "confirmation_timeout", "preparation_timeout", "login_timeout", "rejected", "canceled", "product_owned_by_other", "version_submitted", "version_published", "version_retired", "product_unavailable", "artifact_conflict", "artifacts_missing", "cover_invalid", "developer_suspended", "release_not_found", "busy", "resource_limit"];
  return codes.includes(props.record.errorCode ?? "") ? t(`plugins.publish.error.${props.record.errorCode}` as MessageKey) : t("plugins.publish.failed");
}
</script>

<template>
  <ElDialog :model-value="true" :title="t('plugins.publish.title')" width="min(850px, 94vw)" :close-on-click-modal="false" :close-on-press-escape="false" :show-close="false" append-to-body>
    <ElAlert v-if="queued" type="info" :closable="false" :title="t('plugins.publish.queued', { count: queued })" />
    <ElAlert v-if="commandFailed" type="error" :closable="false" :title="t(commandErrorKey)" />
    <ElDescriptions :column="2" border size="small">
      <ElDescriptionsItem :label="t('plugins.publish.pluginId')">{{ record.pluginId }}</ElDescriptionsItem>
      <ElDescriptionsItem :label="t('plugins.publish.name')">{{ record.pluginName }}</ElDescriptionsItem>
      <ElDescriptionsItem :label="t('plugins.publish.version')">{{ record.version }}</ElDescriptionsItem>
      <ElDescriptionsItem :label="t('plugins.publish.account')">{{ record.account || t(record.status === 'preparing' ? 'plugins.publish.preparing' : 'plugins.publish.loginRequired') }}</ElDescriptionsItem>
      <ElDescriptionsItem :label="t('plugins.publish.kind')">{{ t(record.status === 'awaiting_login' || record.status === 'preparing' ? 'plugins.publish.kindPending' : record.newPlugin ? 'plugins.publish.kindNew' : 'plugins.publish.kindUpdate') }}</ElDescriptionsItem>
      <ElDescriptionsItem :label="t('plugins.publish.releaseDir')">{{ record.releaseDir }}</ElDescriptionsItem>
    </ElDescriptions>
    <ElText tag="h3">{{ t('plugins.publish.packages') }}</ElText>
    <ElTable :data="record.packages" size="small" border max-height="300">
      <ElTableColumn :label="t('plugins.publish.target')" width="145"><template #default="{ row }">{{ row.os }}/{{ row.arch }}</template></ElTableColumn>
      <ElTableColumn prop="file" :label="t('plugins.publish.file')" min-width="140" show-overflow-tooltip />
      <ElTableColumn :label="t('plugins.publish.size')" width="100"><template #default="{ row }">{{ t('plugins.publish.bytes', { count: row.size }) }}</template></ElTableColumn>
      <ElTableColumn :label="t('plugins.publish.sha256')" width="140"><template #default="{ row }"><ElTooltip :content="row.sha256"><ElText>{{ row.sha256.slice(0, 12) }}</ElText></ElTooltip></template></ElTableColumn>
      <ElTableColumn v-if="record.status === 'running'" :label="t('plugins.publish.progress')" min-width="140"><template #default="{ row }"><ElProgress :percentage="row.percent" /></template></ElTableColumn>
    </ElTable>
    <ElText v-if="record.status === 'running'" tag="p">{{ t(`plugins.publish.step.${record.step}`) }}</ElText>
    <ElText tag="h3">{{ t('plugins.publish.notes') }}</ElText>
    <ElText tag="p">{{ releaseNotesExcerpt(record.releaseNotes) || t('plugins.publish.empty') }}</ElText>
    <ElText tag="h3">{{ t('plugins.publish.metadataChanges') }}</ElText>
    <ElTable :data="changes" size="small" border :empty-text="t('plugins.publish.noChanges')" max-height="240">
      <ElTableColumn :label="t('plugins.publish.fieldLabel')" width="100"><template #default="{ row }">{{ t(`plugins.publish.field.${row.field}` as MessageKey) }}</template></ElTableColumn>
      <ElTableColumn prop="old" :label="t('plugins.publish.oldValue')" min-width="160" show-overflow-tooltip />
      <ElTableColumn prop="new" :label="t('plugins.publish.newValue')" min-width="160" show-overflow-tooltip />
    </ElTable>
    <ElAlert v-if="!record.submit" type="info" :closable="false" :title="t('plugins.publish.draftOnly')" />
    <ElAlert v-if="record.status === 'preparing'" type="info" :closable="false" :title="t('plugins.publish.preparing')" />
    <ElAlert v-if="record.status === 'awaiting_login'" type="warning" :closable="false" :title="t('plugins.publish.loginRequired')" />
    <ElAlert v-if="terminal" :type="record.status === 'succeeded' ? 'success' : 'warning'" :closable="false" :title="record.status === 'succeeded' ? t('plugins.publish.succeeded') : errorText()" />
    <ElDescriptions v-if="record.result" :column="2" size="small">
      <ElDescriptionsItem :label="t('plugins.publish.releaseId')">{{ record.result.releaseId }}</ElDescriptionsItem>
      <ElDescriptionsItem :label="t('plugins.publish.reviewStatus')">{{ reviewText }}</ElDescriptionsItem>
      <ElDescriptionsItem :label="t('plugins.publish.uploaded')">{{ record.result.uploaded.length }}</ElDescriptionsItem>
      <ElDescriptionsItem :label="t('plugins.publish.skipped')">{{ record.result.skipped.length }}</ElDescriptionsItem>
    </ElDescriptions>
    <ElLink v-if="record.result?.developerUrl" :href="record.result.developerUrl" target="_blank" rel="noopener noreferrer">{{ t('plugins.publish.developerUrl') }}</ElLink>
    <template #footer>
      <template v-if="record.status === 'pending_confirmation'">
        <ElButton :disabled="busy" @click="command(rejectPluginPublish)">{{ t('plugins.publish.reject') }}</ElButton>
        <ElButton type="primary" :loading="busy" :disabled="busy" @click="command(confirmPluginPublish)">{{ t('plugins.publish.submit') }}</ElButton>
      </template>
      <template v-else-if="record.status === 'awaiting_login'">
        <ElButton :disabled="busy" @click="command(cancelPluginPublish)">{{ t('plugins.publish.cancel') }}</ElButton>
        <ElButton :disabled="busy" @click="requestOpenSettings('cloud')">{{ t('plugins.publish.openLogin') }}</ElButton>
        <ElButton type="primary" :loading="busy" :disabled="busy" @click="command(refreshPluginPublish)">{{ t('plugins.publish.loginDone') }}</ElButton>
      </template>
      <ElButton v-else-if="record.status === 'preparing' || record.status === 'running'" :loading="busy" :disabled="busy" @click="command(cancelPluginPublish)">{{ t('plugins.publish.cancel') }}</ElButton>
      <ElButton v-else :disabled="busy" @click="emit('close')">{{ t('plugins.publish.close') }}</ElButton>
    </template>
  </ElDialog>
</template>
