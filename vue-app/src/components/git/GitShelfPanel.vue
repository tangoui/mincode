<script setup lang="ts">
import type { BackendCommand } from "@/generated/backend-commands";
import { onBeforeUnmount, ref, watch } from "vue";
import { CirclePlus, Delete, RefreshLeft } from "@element-plus/icons-vue";
import { ElMessage, ElMessageBox } from "element-plus";
import GitChangeSelectionDialog from "@/components/git/GitChangeSelectionDialog.vue";
import GitStoredFileList from "@/components/git/GitStoredFileList.vue";
import AppChevron from "@/components/ui/AppChevron.vue";
import { createLatestRequestGate } from "@/domain/async/latest-request-gate";
import { useVueI18n } from "@/i18n/messages";
import { runGitCommand, type GitShelfEntry } from "@/services/git-service";
import { invokeBackend } from "@/services/desktop-bridge";

defineOptions({ name: "GitShelfPanel" });

const props = defineProps<{ repoPath: string }>();
const emit = defineEmits<{ changed: [] }>();
const { t, intlLocale } = useVueI18n();
const shelves = ref<GitShelfEntry[]>([]);
const loading = ref(false);
const createVisible = ref(false);
const expanded = ref<Set<string>>(new Set());
// 仓库切换后旧仓库的列表响应必须丢弃，否则后续操作会拿 A 的条目作用在 B 上。
const refreshGate = createLatestRequestGate(() => props.repoPath);

function formatCreatedAt(timestamp: number) {
  return new Intl.DateTimeFormat(intlLocale.value, {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(new Date(timestamp));
}

async function refresh() {
  const request = refreshGate.begin();
  if (!request.target) {
    shelves.value = [];
    loading.value = false;
    return;
  }
  loading.value = true;
  try {
    const next = await invokeBackend<GitShelfEntry[]>("git_shelf_list", {
      repoPath: request.target,
    });
    if (!refreshGate.isCurrent(request)) return;
    shelves.value = next;
  } catch (error) {
    if (refreshGate.isCurrent(request)) ElMessage.error(String(error));
  } finally {
    if (refreshGate.isCurrent(request)) loading.value = false;
  }
}

async function run(command: BackendCommand, args: Record<string, unknown> = {}) {
  loading.value = true;
  try {
    await runGitCommand(command, props.repoPath, args);
    await refresh();
    emit("changed");
    return true;
  } catch (error) {
    ElMessage.error(String(error));
    return false;
  } finally {
    loading.value = false;
  }
}

async function createShelf(payload: { message: string; paths: string[] }) {
  if (await run("git_shelf_create", payload)) createVisible.value = false;
}

async function applyShelf(shelf: GitShelfEntry) {
  const repoPath = props.repoPath;
  try {
    await ElMessageBox.confirm(
      t("git.shelfApplyConfirm", { message: shelf.message }),
      t("git.shelfApply"),
      {
        type: "warning",
        confirmButtonText: t("git.apply"),
        cancelButtonText: t("common.cancel"),
      }
    );
  } catch {
    return;
  }
  if (repoPath !== props.repoPath) return;
  await run("git_shelf_apply", { shelfId: shelf.id });
}

async function deleteShelf(shelf: GitShelfEntry) {
  const repoPath = props.repoPath;
  try {
    await ElMessageBox.confirm(
      t("git.shelfDeleteConfirm", { message: shelf.message }),
      t("git.shelfDelete"),
      {
        type: "warning",
        confirmButtonText: t("common.continue"),
        cancelButtonText: t("common.cancel"),
      }
    );
  } catch {
    return;
  }
  if (repoPath !== props.repoPath) return;
  await run("git_shelf_delete", { shelfId: shelf.id });
}

function toggleFiles(shelf: GitShelfEntry) {
  const next = new Set(expanded.value);
  if (next.has(shelf.id)) next.delete(shelf.id);
  else next.add(shelf.id);
  expanded.value = next;
}

watch(() => props.repoPath, () => {
  createVisible.value = false;
  expanded.value = new Set();
  shelves.value = [];
  void refresh();
}, { immediate: true });
onBeforeUnmount(() => refreshGate.invalidate());
</script>

<template>
  <section class="git-inspector">
    <div class="git-inspector__actions">
      <el-button size="small" type="primary" :icon="CirclePlus" :loading="loading" @click="createVisible = true">
        {{ t("git.shelfCreate") }}
      </el-button>
    </div>
    <article v-for="shelf in shelves" :key="shelf.id" class="git-stored-entry">
      <div class="git-inspector-row">
        <button type="button" class="git-stored-entry__summary" @click="toggleFiles(shelf)">
          <AppChevron :direction="expanded.has(shelf.id) ? 'down' : 'right'" />
          <span>
            <strong>{{ shelf.message }}</strong>
            <small>{{ formatCreatedAt(shelf.created_at) }} · {{ t("git.storedFileCount", { count: shelf.files?.length ?? 0 }) }}</small>
          </span>
        </button>
        <div>
          <el-button text size="small" :icon="RefreshLeft" :disabled="loading" @click="applyShelf(shelf)">
            {{ t("git.apply") }}
          </el-button>
          <el-button text size="small" :icon="Delete" :disabled="loading" @click="deleteShelf(shelf)" />
        </div>
      </div>
      <GitStoredFileList v-if="expanded.has(shelf.id)" :files="shelf.files" />
    </article>
    <p v-if="shelves.length === 0 && !loading" class="git-inspector-empty">
      {{ t("git.noShelves") }}
    </p>
  </section>

  <GitChangeSelectionDialog
    v-model="createVisible"
    :repo-path="repoPath"
    :title="t('git.shelfCreate')"
    :description="t('git.shelfSelectionDescription')"
    :confirm-text="t('git.shelfSelectedFiles')"
    :busy="loading"
    @confirm="createShelf"
  />
</template>
