<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { ElMessage } from "element-plus";
import type { GitFileEntry, GitProjectStatus } from "@/domain/projects/types";
import { useVueI18n } from "@/i18n/messages";
import { invokeBackend } from "@/services/desktop-bridge";

const props = withDefaults(
  defineProps<{
    modelValue: boolean;
    repoPath: string;
    title: string;
    description: string;
    confirmText: string;
    initialMessage?: string;
    busy?: boolean;
  }>(),
  { initialMessage: "WIP", busy: false }
);
const emit = defineEmits<{
  "update:modelValue": [value: boolean];
  confirm: [payload: { message: string; paths: string[] }];
}>();

const { t } = useVueI18n();
const loading = ref(false);
const message = ref("");
const files = ref<GitFileEntry[]>([]);
const selectedPaths = ref<string[]>([]);
let refreshGeneration = 0;

const allSelected = computed(
  () => files.value.length > 0 && selectedPaths.value.length === files.value.length
);
const partiallySelected = computed(
  () => selectedPaths.value.length > 0 && selectedPaths.value.length < files.value.length
);

function uniqueFiles(status: GitProjectStatus) {
  const result: GitFileEntry[] = [];
  const indexes = new Map<string, number>();
  for (const group of [status.staged, status.unstaged, status.untracked]) {
    for (const file of group) {
      const index = indexes.get(file.path);
      if (index !== undefined) {
        if (!result[index].old_path && file.old_path) result[index].old_path = file.old_path;
        continue;
      }
      indexes.set(file.path, result.length);
      result.push({ ...file });
    }
  }
  return result;
}

type StatusTag = "success" | "warning" | "danger" | "info";

function fileStatusType(file: GitFileEntry): StatusTag {
  if (file.status === "untracked") return "info";
  if (file.status === "added" || file.status.startsWith("A")) return "success";
  if (file.status === "deleted" || file.status.startsWith("D")) return "danger";
  return "warning";
}

// Splits a path into the file name (emphasised) and its folder (muted).
function splitPath(path: string) {
  const index = path.lastIndexOf("/");
  return index < 0 ? { name: path, dir: "" } : { name: path.slice(index + 1), dir: path.slice(0, index) };
}

function fileStatus(file: GitFileEntry) {
  if (file.status === "untracked") return t("project.git.untracked");
  if (file.status === "added" || file.status.startsWith("A")) return t("project.git.status.added");
  if (file.status === "deleted" || file.status.startsWith("D")) return t("project.git.status.deleted");
  return t("project.git.status.modified");
}

async function loadChanges() {
  const generation = ++refreshGeneration;
  files.value = [];
  selectedPaths.value = [];
  if (!props.repoPath) return;
  loading.value = true;
  try {
    const status = await invokeBackend<GitProjectStatus>("git_get_status", {
      repoPath: props.repoPath,
    });
    if (generation !== refreshGeneration || !props.modelValue) return;
    files.value = uniqueFiles(status);
    selectedPaths.value = files.value.map((file) => file.path);
  } catch (error) {
    if (generation === refreshGeneration) ElMessage.error(String(error));
  } finally {
    if (generation === refreshGeneration) loading.value = false;
  }
}

function setAllSelected(value: boolean) {
  selectedPaths.value = value ? files.value.map((file) => file.path) : [];
}

function submit() {
  const trimmedMessage = message.value.trim();
  if (!trimmedMessage || selectedPaths.value.length === 0 || props.busy) return;
  emit("confirm", { message: trimmedMessage, paths: [...selectedPaths.value] });
}

watch(
  () => props.modelValue,
  (visible) => {
    if (!visible) {
      refreshGeneration += 1;
      return;
    }
    message.value = props.initialMessage;
    void loadChanges();
  }
);
</script>

<template>
  <el-dialog
    :model-value="modelValue"
    class="git-change-selection-dialog"
    append-to-body
    :title="title"
    width="min(560px, 92vw)"
    :close-on-click-modal="!busy"
    :close-on-press-escape="!busy"
    :show-close="!busy"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <p class="git-change-selection-dialog__description">{{ description }}</p>
    <el-input
      v-model="message"
      :disabled="busy"
      :placeholder="t('git.changeSelectionMessagePlaceholder')"
      maxlength="200"
      show-word-limit
    />
    <div class="git-change-selection-dialog__toolbar">
      <el-checkbox
        :model-value="allSelected"
        :indeterminate="partiallySelected"
        :disabled="loading || busy || files.length === 0"
        @change="setAllSelected(Boolean($event))"
      >
        {{ t("git.selectAllFiles", { count: files.length }) }}
      </el-checkbox>
      <span>{{ t("git.selectedFiles", { count: selectedPaths.length }) }}</span>
    </div>
    <el-scrollbar v-loading="loading" class="git-change-selection-dialog__files" max-height="min(340px, 44vh)">
      <el-checkbox-group v-model="selectedPaths" :disabled="busy">
        <el-checkbox
          v-for="file in files"
          :key="file.path"
          :value="file.path"
          class="git-change-selection-dialog__file"
        >
          <span
            class="git-change-selection-dialog__file-path"
            :title="file.old_path ? `${file.old_path} → ${file.path}` : file.path"
          >
            <span class="git-change-selection-dialog__file-name">{{ splitPath(file.path).name }}</span>
            <span v-if="file.old_path || splitPath(file.path).dir" class="git-change-selection-dialog__file-dir">
              {{ file.old_path ? `${file.old_path} →` : splitPath(file.path).dir }}
            </span>
          </span>
          <el-tag size="small" :type="fileStatusType(file)" effect="light" disable-transitions>
            {{ fileStatus(file) }}
          </el-tag>
        </el-checkbox>
      </el-checkbox-group>
      <p v-if="!loading && files.length === 0" class="git-inspector-empty">
        {{ t("git.noChangesToStore") }}
      </p>
    </el-scrollbar>
    <template #footer>
      <el-button :disabled="busy" @click="emit('update:modelValue', false)">
        {{ t("common.cancel") }}
      </el-button>
      <el-button
        type="primary"
        :loading="busy"
        :disabled="loading || !message.trim() || selectedPaths.length === 0"
        @click="submit"
      >
        {{ confirmText }}
      </el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.git-change-selection-dialog__description {
  margin: 0 0 12px;
  color: var(--app-text-secondary);
  font-size: 12px;
  line-height: 1.6;
}
.git-change-selection-dialog__toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-top: 16px;
  padding: 0 8px;
  color: var(--app-text-secondary);
  font-size: 12px;
}
.git-change-selection-dialog__toolbar :deep(.el-checkbox__label) {
  color: var(--app-text);
  font-size: 12px;
}
.git-change-selection-dialog__files {
  min-height: 64px;
  margin-top: 8px;
  border: 1px solid var(--app-border);
  border-radius: 6px;
}
.git-change-selection-dialog__files :deep(.el-checkbox-group) {
  display: grid;
  gap: 1px;
  padding: 4px;
}
.git-change-selection-dialog__file {
  width: 100%;
  height: 34px;
  margin: 0;
  padding: 0 8px;
  border-radius: 4px;
}
.git-change-selection-dialog__file:hover {
  background: var(--app-hover);
}
.git-change-selection-dialog__file :deep(.el-checkbox__label) {
  display: flex;
  min-width: 0;
  flex: 1;
  align-items: center;
  gap: 10px;
  color: var(--app-text);
}
.git-change-selection-dialog__file-path {
  display: flex;
  min-width: 0;
  flex: 1;
  align-items: baseline;
  gap: 8px;
  overflow: hidden;
  white-space: nowrap;
}
.git-change-selection-dialog__file-name {
  flex: none;
  max-width: 100%;
  overflow: hidden;
  font-size: 13px;
  font-weight: 500;
  text-overflow: ellipsis;
}
.git-change-selection-dialog__file-dir {
  min-width: 0;
  overflow: hidden;
  color: var(--app-text-secondary);
  font-size: 12px;
  text-overflow: ellipsis;
}
.git-change-selection-dialog__file :deep(.el-tag) {
  flex: none;
}
</style>
