<script setup lang="ts">
import { Close, Download, RefreshRight, Upload } from "@element-plus/icons-vue";
import type { SftpTransferItem } from "@/domain/sftp/transfer-queue";
import { useVueI18n } from "@/i18n/messages";
import { formatSftpTransferBytes } from "./sftp-file-list";

// One component per row: progress writes to a single item only re-render that row.
const props = defineProps<{ item: SftpTransferItem }>();
const emit = defineEmits<{ cancel: [id: string]; retry: [id: string] }>();
const { t } = useVueI18n();

function transferPathLabel() {
  return props.item.direction === "upload"
    ? `${props.item.localPath} → ${props.item.remotePath}`
    : `${props.item.remotePath} → ${props.item.localPath}`;
}

function transferProgressLabel() {
  const item = props.item;
  const transferred = formatSftpTransferBytes(item.bytesTransferred);
  const total = item.totalBytes > 0 ? formatSftpTransferBytes(item.totalBytes) : t("sftp.sizeUnknown");
  const speed = item.speedBytesPerSecond > 0
    ? `${formatSftpTransferBytes(item.speedBytesPerSecond)}/s`
    : "";
  return [t("sftp.transferBytes").replace("{current}", transferred).replace("{total}", total), speed]
    .filter(Boolean)
    .join(" · ");
}

function transferProgressPercentage() {
  const item = props.item;
  if (item.totalBytes <= 0) return item.status === "completed" ? 100 : 0;
  return Math.max(0, Math.min(100, Math.round((item.bytesTransferred / item.totalBytes) * 100)));
}
</script>

<template>
  <article class="sftp-transfer-row">
    <el-icon class="sftp-transfer-row__direction">
      <Upload v-if="item.direction === 'upload'" />
      <Download v-else />
    </el-icon>
    <div class="sftp-transfer-row__identity">
      <strong>{{ item.name }}</strong>
      <small>{{ transferPathLabel() }}</small>
      <el-progress
        v-if="item.operation !== 'create-remote-directory' && item.status !== 'queued'"
        class="sftp-transfer-row__progress"
        :percentage="transferProgressPercentage()"
        :show-text="false"
        :stroke-width="3"
        :status="item.status === 'failed' ? 'exception' : item.status === 'completed' ? 'success' : undefined"
      />
      <small v-if="item.operation !== 'create-remote-directory' && item.status !== 'queued'">
        {{ transferProgressLabel() }}
        <template v-if="item.resumedFrom"> · {{ t("sftp.resumedTransfer") }}</template>
      </small>
    </div>
    <span class="sftp-transfer-row__status" :class="`is-${item.status}`">
      {{ t(`sftp.transferStatus.${item.status}`) }}
    </span>
    <el-button
      v-if="item.status === 'running'"
      text
      :icon="Close"
      :aria-label="t('sftp.cancelRunning')"
      :title="t('sftp.cancelRunning')"
      @click="emit('cancel', item.id)"
    />
    <el-button
      v-else-if="item.status === 'failed' || item.status === 'cancelled'"
      text
      :icon="RefreshRight"
      :aria-label="t('sftp.retry')"
      :title="item.error || t('sftp.retry')"
      @click="emit('retry', item.id)"
    />
  </article>
</template>
