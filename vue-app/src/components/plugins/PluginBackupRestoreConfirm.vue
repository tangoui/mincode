<script setup lang="ts">
// Host confirmation before a plugin restores MinCode data (backup request
// kind backupRestoreConfirm). Owned by the host, outside any plugin: rows are
// names only (never values), grouped by section and then by kind, and every
// text is made visible by domain/backup/requests. The title names the plugin
// with the host's display name, never with text from the request payload.
import { computed } from "vue";
import { ElAlert, ElButton, ElDialog, ElScrollbar, ElTag } from "element-plus";
import {
  groupRestoreChanges,
  type BackupChangeSection,
  type BackupRestoreConfirmRequest,
  type BackupVisibleChangeKind,
} from "@/domain/backup/requests";
import { visibleReviewText } from "@/domain/plugin-ui/ai-config";
import { useVueI18n } from "@/i18n/messages";

const props = defineProps<{
  request: BackupRestoreConfirmRequest;
}>();
const emit = defineEmits<{
  confirm: [];
  cancel: [];
}>();

const { t } = useVueI18n();

const visible = computed({
  get: () => true,
  set: (next: boolean) => {
    if (!next) emit("cancel");
  },
});

const overwrite = computed(() => props.request.mode === "overwrite");
const grouped = computed(() => groupRestoreChanges(props.request.changes));
const title = computed(() => t("pluginBackup.restore.title", { plugin: visibleReviewText(props.request.pluginName) }));
const removes = computed(() =>
  grouped.value.sections.reduce(
    (count, section) => count + (section.groups.find((group) => group.kind === "remove")?.labels.length ?? 0),
    0,
  ),
);

function sectionName(section: BackupChangeSection) {
  if (section.coreId) return t(`pluginBackup.section.${section.coreId}`);
  if (section.pluginId !== undefined) {
    return t("pluginBackup.restore.pluginSection", { title: section.sectionTitle || section.pluginId, id: section.pluginId });
  }
  return section.sectionTitle || visibleReviewText(section.section);
}

function kindTag(kind: BackupVisibleChangeKind) {
  if (kind === "add") return "success";
  if (kind === "update") return "warning";
  if (kind === "remove") return overwrite.value ? "danger" : "warning";
  return "info";
}
</script>

<template>
  <ElDialog
    v-model="visible"
    :title="title"
    width="min(600px, calc(100vw - 32px))"
    align-center
    append-to-body
    :close-on-click-modal="false"
    class="app-plugin-backup-restore"
  >
    <div class="app-plugin-backup-restore__body">
      <p class="app-plugin-backup-restore__intro">
        <ElTag size="small" :type="overwrite ? 'danger' : 'info'" disable-transitions>
          {{ t(`pluginBackup.restore.mode.${request.mode}`) }}
        </ElTag>
        <span>{{ t(`pluginBackup.restore.intro.${request.mode}`) }}</span>
      </p>
      <ElAlert
        v-if="overwrite && removes"
        type="error"
        :closable="false"
        show-icon
        :title="t('pluginBackup.restore.removeWarning', { count: removes })"
      />
      <p v-if="!grouped.sections.length && !grouped.more" class="app-plugin-backup-restore__empty">
        {{ t("pluginBackup.restore.empty") }}
      </p>
      <ElScrollbar v-else max-height="50vh">
        <section
          v-for="section in grouped.sections"
          :key="section.section"
          class="app-plugin-backup-restore__section"
          :aria-label="sectionName(section)"
        >
          <h3 class="app-plugin-backup-restore__section-title">{{ sectionName(section) }}</h3>
          <div v-for="group in section.groups" :key="group.kind" class="app-plugin-backup-restore__group">
            <ElTag size="small" :type="kindTag(group.kind)" disable-transitions>
              {{ t(`pluginBackup.restore.kind.${group.kind}`) }} · {{ group.labels.length }}
            </ElTag>
            <ul
              class="app-plugin-backup-restore__list"
              :class="{ 'is-danger': overwrite && group.kind === 'remove' }"
              :aria-label="t(`pluginBackup.restore.kind.${group.kind}`)"
            >
              <li v-for="(label, index) in group.labels" :key="index">{{ label }}</li>
            </ul>
          </div>
        </section>
        <p v-if="grouped.more" class="app-plugin-backup-restore__more">
          {{ t("pluginBackup.restore.more", { count: grouped.more }) }}
        </p>
      </ElScrollbar>
    </div>
    <template #footer>
      <ElButton @click="emit('cancel')">{{ t("common.cancel") }}</ElButton>
      <ElButton :type="overwrite ? 'danger' : 'primary'" @click="emit('confirm')">
        {{ t("pluginBackup.restore.confirm") }}
      </ElButton>
    </template>
  </ElDialog>
</template>

<style scoped>
.app-plugin-backup-restore__body {
  display: grid;
  gap: 10px;
}
.app-plugin-backup-restore__intro {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  margin: 0;
  color: var(--app-text);
  font-size: 14px;
  line-height: 1.6;
}
.app-plugin-backup-restore__empty,
.app-plugin-backup-restore__more {
  margin: 0;
  color: var(--app-text-muted);
  font-size: 13px;
}
.app-plugin-backup-restore__more { margin-top: 8px; }
.app-plugin-backup-restore__section + .app-plugin-backup-restore__section {
  margin-top: 12px;
  padding-top: 12px;
  border-top: 1px solid var(--app-border);
}
.app-plugin-backup-restore__section-title {
  margin: 0 0 6px;
  color: var(--app-text);
  font-size: 13px;
  font-weight: 600;
  overflow-wrap: anywhere;
}
.app-plugin-backup-restore__group + .app-plugin-backup-restore__group { margin-top: 6px; }
.app-plugin-backup-restore__list {
  margin: 4px 0 0;
  padding-left: 18px;
  color: var(--app-text);
  font-size: 13px;
  overflow-wrap: anywhere;
}
.app-plugin-backup-restore__list.is-danger { color: var(--el-color-danger); }
</style>
