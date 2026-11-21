<script setup lang="ts">
// Asked before a conversation is rewound when the rewound agent turns changed
// project files: lists, per project root, what restoring would write back,
// delete, and leave alone, and lets the user restore the files too or only
// rewind the conversation.
import { computed } from "vue";
import { restorablePlans, type RewindRestoreBlock, type RewindRestorePlan } from "@/domain/ai/rewind-restore-plan";
import { useVueI18n } from "@/i18n/messages";

const props = defineProps<{
  modelValue: boolean;
  plans: RewindRestorePlan[];
}>();

const emit = defineEmits<{
  confirm: [restoreFiles: boolean];
  cancel: [];
}>();

const { t } = useVueI18n();

const canRestore = computed(() => restorablePlans(props.plans).length > 0);

const blockedReason = (block: RewindRestoreBlock) => {
  if (block === "unsupported") return t("ai.rewindRestore.blocked.unsupported");
  if (block === "git_state_changed") return t("ai.rewindRestore.blocked.gitStateChanged");
  return t("ai.rewindRestore.blocked.noSnapshot");
};

const groups = (plan: RewindRestorePlan) => [
  { key: "restore", title: t("ai.rewindRestore.restoreList"), paths: plan.restore },
  { key: "remove", title: t("ai.rewindRestore.removeList"), paths: plan.remove },
  { key: "conflicts", title: t("ai.rewindRestore.conflictList"), paths: plan.conflicts },
  { key: "uncovered", title: t("ai.rewindRestore.uncoveredList"), paths: plan.uncovered },
].filter((group) => group.paths.length > 0);

function onVisibleChange(visible: boolean) {
  if (!visible) emit("cancel");
}
</script>

<template>
  <el-dialog
    :model-value="modelValue"
    class="ai-rewind-restore-dialog"
    :title="t('ai.rewindRestore.title')"
    width="min(640px, 92vw)"
    append-to-body
    @update:model-value="onVisibleChange"
  >
    <div class="ai-rewind-restore">
      <p class="ai-rewind-restore__intro">{{ t("ai.rewindRestore.intro") }}</p>
      <section v-for="plan in plans" :key="plan.projectRoot" class="ai-rewind-restore__root">
        <code class="ai-rewind-restore__path" :title="plan.projectRoot">{{ plan.projectRoot }}</code>
        <el-alert v-if="plan.blocked" type="info" :closable="false" show-icon :title="blockedReason(plan.blocked)" />
        <template v-else>
          <el-alert v-if="plan.overlapped" type="warning" :closable="false" show-icon :title="t('ai.rewindRestore.overlapped')" />
          <p v-if="!plan.restore.length && !plan.remove.length" class="ai-rewind-restore__muted">
            {{ t("ai.rewindRestore.nothingToRestore") }}
          </p>
          <div v-for="group in groups(plan)" :key="group.key" class="ai-rewind-restore__group">
            <h4 class="ai-rewind-restore__heading">{{ group.title }} ({{ group.paths.length }})</h4>
            <small v-if="group.key === 'conflicts'" class="ai-rewind-restore__muted">{{ t("ai.rewindRestore.conflictHint") }}</small>
            <small v-else-if="group.key === 'uncovered'" class="ai-rewind-restore__muted">{{ t("ai.rewindRestore.uncoveredHint") }}</small>
            <ul class="ai-rewind-restore__files">
              <li v-for="path in group.paths" :key="path">{{ path }}</li>
            </ul>
          </div>
        </template>
      </section>
    </div>
    <template #footer>
      <el-button @click="emit('cancel')">{{ t("common.cancel") }}</el-button>
      <el-button :type="canRestore ? 'default' : 'primary'" @click="emit('confirm', false)">
        {{ t("ai.rewindRestore.rewindOnly") }}
      </el-button>
      <el-button v-if="canRestore" type="primary" @click="emit('confirm', true)">
        {{ t("ai.rewindRestore.restoreAndRewind") }}
      </el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.ai-rewind-restore {
  display: grid;
  gap: 12px;
  min-width: 0;
  max-height: min(60vh, 520px);
  overflow: auto;
}

.ai-rewind-restore__intro {
  margin: 0;
  color: var(--app-text);
}

.ai-rewind-restore__root {
  display: grid;
  gap: 8px;
  min-width: 0;
  padding: 10px 12px;
  border: 1px solid var(--app-border);
  border-radius: 6px;
  background: var(--app-surface);
}

.ai-rewind-restore__path {
  overflow: hidden;
  color: var(--app-text);
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.ai-rewind-restore__group {
  display: grid;
  gap: 4px;
  min-width: 0;
}

.ai-rewind-restore__heading {
  margin: 0;
  color: var(--app-text);
  font-size: 13px;
  font-weight: 600;
}

.ai-rewind-restore__muted {
  margin: 0;
  color: var(--app-text-muted);
  font-size: 12px;
}

.ai-rewind-restore__files {
  margin: 0;
  padding-left: 18px;
  color: var(--app-text);
  font-family: var(--el-font-family-mono, ui-monospace, monospace);
  font-size: 12px;
  overflow-wrap: anywhere;
}
</style>
