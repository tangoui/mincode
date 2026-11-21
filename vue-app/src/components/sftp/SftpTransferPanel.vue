<script setup lang="ts">
import { computed } from "vue";
import { Close, VideoPause, VideoPlay } from "@element-plus/icons-vue";
import type { SftpTransferQueue } from "@/composables/useSftpTransferQueue";
import { useVueI18n } from "@/i18n/messages";
import SftpTransferRow from "./SftpTransferRow.vue";

const props = defineProps<{ queue: SftpTransferQueue }>();
const open = defineModel<boolean>("open", { default: false });
const { t } = useVueI18n();

// Recomputed only when the list itself changes (enqueue/retry/prune), not on progress.
const visibleTransfers = computed(() => [...props.queue.items.value].reverse());
</script>

<template>
  <section class="sftp-transfer-panel">
    <header class="sftp-transfer-panel__header">
      <el-button
        text
        native-type="button"
        class="sftp-transfer-panel__toggle"
        :aria-expanded="open"
        @click="open = !open"
      >
        <strong>{{ t("sftp.transferQueue") }}</strong>
        <span v-if="queue.active.value">
          {{ t(queue.active.value.direction === "upload" ? "sftp.upload" : "sftp.download") }} ·
          {{ queue.active.value.name }}
        </span>
        <span v-else>
          {{
            t("sftp.transferSummary")
              .replace("{completed}", String(queue.summary.value.completed))
              .replace("{total}", String(queue.summary.value.total))
          }}
        </span>
      </el-button>
      <div class="sftp-transfer-panel__actions">
        <el-button
          v-if="queue.paused.value"
          text
          :icon="VideoPlay"
          :aria-label="t('sftp.resumeQueue')"
          :title="t('sftp.resumeQueue')"
          @click="queue.resume"
        />
        <el-button
          v-else
          text
          :icon="VideoPause"
          :disabled="!queue.summary.value.queued"
          :aria-label="t('sftp.pauseQueue')"
          :title="t('sftp.pauseQueue')"
          @click="queue.pause"
        />
        <el-button
          text
          :icon="Close"
          :disabled="!queue.summary.value.queued"
          :aria-label="t('sftp.cancelQueued')"
          :title="t('sftp.cancelQueued')"
          @click="queue.cancelQueued"
        />
        <el-button
          text
          native-type="button"
          class="sftp-transfer-panel__text-action"
          :disabled="queue.summary.value.total === queue.summary.value.queued + queue.summary.value.running"
          @click="queue.clearSettled"
        >
          {{ t("sftp.clearSettled") }}
        </el-button>
      </div>
    </header>
    <el-scrollbar v-if="open" class="sftp-transfer-list">
      <SftpTransferRow
        v-for="item in visibleTransfers"
        :key="item.id"
        :item="item"
        @cancel="queue.cancel"
        @retry="queue.retry"
      />
    </el-scrollbar>
  </section>
</template>
