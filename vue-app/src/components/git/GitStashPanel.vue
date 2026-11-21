<script setup lang="ts">
import type { BackendCommand } from "@/generated/backend-commands";
import { onBeforeUnmount, ref, watch } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import { CirclePlus, Delete } from "@element-plus/icons-vue";
import GitChangeSelectionDialog from "@/components/git/GitChangeSelectionDialog.vue";
import GitStoredFileList from "@/components/git/GitStoredFileList.vue";
import AppChevron from "@/components/ui/AppChevron.vue";
import { createLatestRequestGate } from "@/domain/async/latest-request-gate";
import { useVueI18n } from "@/i18n/messages";
import { invokeBackend } from "@/services/desktop-bridge";
import { runGitCommand, type GitStashEntry } from "@/services/git-service";

defineOptions({ name: "GitStashPanel" });

const props = withDefaults(defineProps<{ repoPath: string; showCreateAction?: boolean }>(), {
  showCreateAction: true,
});
const emit = defineEmits<{ changed: [] }>();

const { t } = useVueI18n();
const stashes = ref<GitStashEntry[]>([]);
const loading = ref(false);
const createVisible = ref(false);
const expanded = ref<Set<string>>(new Set());
// 仓库切换后旧仓库的列表响应必须丢弃，否则后续操作会拿 A 的 stash 序号作用在 B 上。
const refreshGate = createLatestRequestGate(() => props.repoPath);

async function refresh() {
  const request = refreshGate.begin();
  if (!request.target) {
    stashes.value = [];
    loading.value = false;
    return;
  }
  loading.value = true;
  try {
    const next = await invokeBackend<GitStashEntry[]>("git_stash_list", {
      repoPath: request.target,
    });
    if (!refreshGate.isCurrent(request)) return;
    stashes.value = next;
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

async function pushStash(payload: { message: string; paths: string[] }) {
  if (await run("git_stash_push", payload)) createVisible.value = false;
}

async function confirmThen(message: string, title: string, action: () => Promise<void>) {
  const repoPath = props.repoPath;
  try {
    await ElMessageBox.confirm(message, title, {
      type: "warning",
      confirmButtonText: t("common.continue"),
      cancelButtonText: t("common.cancel"),
    });
  } catch {
    return;
  }
  if (repoPath !== props.repoPath) return;
  await action();
}

function popStash(stash: GitStashEntry) {
  return confirmThen(
    t("git.popStashConfirm", { message: stash.message }),
    t("git.popStashTitle"),
    () => run("git_stash_pop", { stashIndex: stash.index }).then(() => undefined)
  );
}

function dropStash(stash: GitStashEntry) {
  return confirmThen(
    t("git.dropStashConfirm", { message: stash.message }),
    t("git.dropStashTitle"),
    () => run("git_stash_drop", { stashIndex: stash.index }).then(() => undefined)
  );
}

function toggleFiles(stash: GitStashEntry) {
  const next = new Set(expanded.value);
  if (next.has(stash.oid)) next.delete(stash.oid);
  else next.add(stash.oid);
  expanded.value = next;
}

watch(() => props.repoPath, () => {
  createVisible.value = false;
  expanded.value = new Set();
  stashes.value = [];
  void refresh();
}, { immediate: true });
onBeforeUnmount(() => refreshGate.invalidate());

defineExpose({ refresh, openCreate: () => { createVisible.value = true; } });
</script>

<template>
  <section class="git-inspector">
    <div v-if="showCreateAction" class="git-inspector__actions">
      <el-button size="small" type="primary" :icon="CirclePlus" :loading="loading" @click="createVisible = true">
        {{ t("git.stashTitle") }}
      </el-button>
    </div>
    <article v-for="stash in stashes" :key="stash.oid" class="git-stored-entry">
      <div class="git-inspector-row">
        <button type="button" class="git-stored-entry__summary" @click="toggleFiles(stash)">
          <AppChevron :direction="expanded.has(stash.oid) ? 'down' : 'right'" />
          <span>
            <strong>stash@{{ "{" }}{{ stash.index }}{{ "}" }}</strong>
            <small>{{ stash.message }} · {{ t("git.storedFileCount", { count: stash.files?.length ?? 0 }) }}</small>
          </span>
        </button>
        <div>
          <el-button text size="small" :disabled="loading" @click="popStash(stash)">
            {{ t("git.apply") }}
          </el-button>
          <el-button text size="small" :icon="Delete" :disabled="loading" @click="dropStash(stash)" />
        </div>
      </div>
      <GitStoredFileList v-if="expanded.has(stash.oid)" :files="stash.files" />
    </article>
    <p v-if="stashes.length === 0 && !loading" class="git-inspector-empty">
      {{ t("git.noStashes") }}
    </p>
  </section>

  <GitChangeSelectionDialog
    v-model="createVisible"
    :repo-path="repoPath"
    :title="t('git.stashTitle')"
    :description="t('git.stashSelectionDescription')"
    :confirm-text="t('git.stashSelectedFiles')"
    :busy="loading"
    @confirm="pushStash"
  />
</template>
