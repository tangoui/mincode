<script setup lang="ts">
import type { GitFileEntry } from "@/domain/projects/types";
import { useVueI18n } from "@/i18n/messages";

withDefaults(defineProps<{ files?: GitFileEntry[] }>(), { files: () => [] });
const { t } = useVueI18n();

function statusLabel(file: GitFileEntry) {
  if (file.status === "untracked") return t("project.git.untracked");
  if (file.status === "added" || file.status.startsWith("A")) return t("project.git.status.added");
  if (file.status === "deleted" || file.status.startsWith("D")) return t("project.git.status.deleted");
  return t("project.git.status.modified");
}
</script>

<template>
  <ul v-if="files.length" class="git-stored-files">
    <li v-for="file in files" :key="`${file.old_path ?? ''}:${file.path}`">
      <span :title="file.old_path ? `${file.old_path} → ${file.path}` : file.path">
        {{ file.old_path ? `${file.old_path} → ${file.path}` : file.path }}
      </span>
      <small>{{ statusLabel(file) }}</small>
    </li>
  </ul>
  <p v-else class="git-stored-files__empty">{{ t("git.noStoredFileDetails") }}</p>
</template>

<style scoped>
.git-stored-files {
  display: grid;
  gap: 2px;
  margin: 0;
  padding: 5px 8px 7px 30px;
  list-style: none;
}
.git-stored-files li {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 8px;
  font-size: 11px;
  line-height: 24px;
}
.git-stored-files li::before {
  width: 4px;
  height: 4px;
  flex: none;
  border-radius: 50%;
  background: var(--app-text-secondary);
  content: "";
}
.git-stored-files span {
  min-width: 0;
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.git-stored-files small,
.git-stored-files__empty {
  color: var(--app-text-secondary);
  font-size: 11px;
}
.git-stored-files__empty {
  margin: 0;
  padding: 5px 8px 7px 30px;
}
</style>
