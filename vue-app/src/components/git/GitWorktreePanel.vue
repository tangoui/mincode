<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import { Delete, Lock, Refresh, Unlock } from "@element-plus/icons-vue";
import { createLatestRequestGate } from "@/domain/async/latest-request-gate";
import { useVueI18n } from "@/i18n/messages";
import GitBatchWorktreePanel from "@/components/git/GitBatchWorktreePanel.vue";
import { useWorkspaceStateScope } from "@/composables/use-workspace-state-scope";
import {
  listGitWorktrees,
  lockGitWorktree,
  pruneGitWorktrees,
  removeGitWorktree,
  unlockGitWorktree,
  type GitWorktreeEntry,
} from "@/services/git-service";
import {
  buildWorktreeBinding,
  formatWorktreeChanges,
  workspaceIdsBoundTo,
} from "@/domain/git/worktree";

defineOptions({ name: "GitWorktreePanel" });

const props = defineProps<{ repoPath: string }>();

const emit = defineEmits<{
  /** 绑定成功后由宿主决定如何跟随该目录（切换项目根、重载文件树等）。 */
  bind: [entry: GitWorktreeEntry];
  unbind: [];
  /** worktree 增删后触发，宿主据此刷新自己缓存的仓库拓扑。 */
  changed: [];
  /** 批量创建执行时，宿主需阻止外层抽屉被意外关闭。 */
  batchActive: [active: boolean];
}>();

const { t } = useVueI18n();
const workspaceStateScope = useWorkspaceStateScope();

const worktrees = ref<GitWorktreeEntry[]>([]);
const loading = ref(false);
// 仓库切换后旧仓库的列表响应必须丢弃，否则后续操作会拿 A 的 worktree 作用在 B 上。
const refreshGate = createLatestRequestGate(() => props.repoPath);

const boundWorktreePath = computed(() => workspaceStateScope.workbench.activeWorktreePath);

async function refresh() {
  const request = refreshGate.begin();
  if (!request.target) {
    worktrees.value = [];
    loading.value = false;
    return;
  }
  loading.value = true;
  try {
    const next = await listGitWorktrees(request.target);
    if (!refreshGate.isCurrent(request)) return;
    worktrees.value = next;
  } catch (error) {
    if (refreshGate.isCurrent(request)) ElMessage.error(String(error));
  } finally {
    if (refreshGate.isCurrent(request)) loading.value = false;
  }
}

function bindWorktree(entry: GitWorktreeEntry) {
  const workspace = workspaceStateScope.workbench.activeWorkspace;
  if (!workspace) return;
  const binding = buildWorktreeBinding(props.repoPath, entry);
  if (!binding) return;
  if (!workspaceStateScope.workbench.setWorkspaceWorktree(workspace.id, binding)) return;
  emit("bind", entry);
  ElMessage.success(t("git.worktree.bindSuccess", { name: workspace.name }));
}

function unbindWorktree() {
  const workspace = workspaceStateScope.workbench.activeWorkspace;
  if (!workspace) return;
  if (!workspaceStateScope.workbench.clearWorkspaceWorktree(workspace.id)) return;
  emit("unbind");
  ElMessage.success(t("git.worktree.unbindSuccess", { name: workspace.name }));
}

async function removeWorktree(entry: GitWorktreeEntry) {
  if (entry.is_main) return;
  const repoPath = props.repoPath;
  try {
    const message = entry.is_locked
      ? t("git.worktree.removeLockedConfirm", {
          path: entry.path,
          reason: entry.lock_reason || t("git.worktree.unknownLockReason"),
        })
      : entry.path;
    await ElMessageBox.confirm(message, t("git.worktree.removeTitle"), {
      type: "warning",
      confirmButtonText: t("common.continue"),
      cancelButtonText: t("common.cancel"),
    });
  } catch {
    return;
  }
  if (repoPath !== props.repoPath) return;
  loading.value = true;
  try {
    await removeGitWorktree(repoPath, entry.path, {
      force: entry.is_prunable,
      forceLocked: entry.is_locked,
    });
    // 目录已删除，绑定到它的工作区必须解绑，否则终端与会话会落到不存在的路径。
    for (const id of workspaceIdsBoundTo(workspaceStateScope.workbench.workspaces, entry.path)) {
      workspaceStateScope.workbench.clearWorkspaceWorktree(id);
    }
    await refresh();
    emit("changed");
  } catch (error) {
    ElMessage.error(String(error));
  } finally {
    loading.value = false;
  }
}

async function toggleWorktreeLock(entry: GitWorktreeEntry) {
  loading.value = true;
  try {
    if (entry.is_locked) {
      await unlockGitWorktree(props.repoPath, entry.path);
    } else {
      await lockGitWorktree(props.repoPath, entry.path, "MinCode");
    }
    await refresh();
    if (entry.is_locked) {
      const refreshed = worktrees.value.find((worktree) => worktree.path === entry.path);
      if (refreshed?.is_locked) {
        ElMessage.warning(
          t("git.worktree.relocked", {
            reason: refreshed.lock_reason || t("git.worktree.unknownLockReason"),
          })
        );
      }
    }
  } catch (error) {
    ElMessage.error(String(error));
  } finally {
    loading.value = false;
  }
}

async function pruneWorktrees() {
  loading.value = true;
  try {
    await pruneGitWorktrees(props.repoPath);
    await refresh();
    emit("changed");
  } catch (error) {
    ElMessage.error(String(error));
  } finally {
    loading.value = false;
  }
}

watch(() => props.repoPath, () => {
  worktrees.value = [];
  void refresh();
}, { immediate: true });
onBeforeUnmount(() => refreshGate.invalidate());

defineExpose({ refresh });
</script>

<template>
  <section class="git-inspector">
    <slot name="header" />
    <GitBatchWorktreePanel
      :repo-path="repoPath"
      @busy="emit('batchActive', $event)"
      @changed="refresh(); emit('changed')"
    />
    <section class="git-ref-list git-worktree-list" :aria-label="t('git.worktree.existing')">
      <header class="git-ref-list__header">
        <span>
          {{ t("git.worktree.existing") }}
          <span class="git-ref-list__count">{{ worktrees.length }}</span>
        </span>
        <div class="git-worktree-list__actions">
          <el-button text size="small" :icon="Refresh" :loading="loading" @click="refresh">
            {{ t("common.refresh") }}
          </el-button>
          <el-button
            text
            size="small"
            :disabled="loading"
            :title="t('git.worktree.pruneHint')"
            @click="pruneWorktrees"
          >
            {{ t("git.worktree.prune") }}
          </el-button>
        </div>
      </header>
      <article v-for="entry in worktrees" :key="entry.path" class="git-worktree-row">
        <div class="git-worktree-row__summary">
          <strong :title="entry.path">{{ entry.path }}</strong>
          <div class="git-worktree-row__badges">
            <span class="git-worktree-row__branch">
              {{ entry.branch || entry.head || t("git.worktree.detached") }}
            </span>
            <span v-if="entry.is_main">{{ t("git.worktree.main") }}</span>
            <span v-if="entry.is_current">{{ t("git.current") }}</span>
            <span
              v-if="entry.is_locked"
              :title="entry.lock_reason || t('git.worktree.unknownLockReason')"
            >
              {{ t("git.worktree.locked") }}
            </span>
            <span v-if="entry.is_prunable">{{ t("git.worktree.prunable") }}</span>
            <span v-if="boundWorktreePath === entry.path">{{ t("git.worktree.bound") }}</span>
          </div>
        </div>
        <div class="git-worktree-row__trailing">
          <p
            v-if="formatWorktreeChanges(entry.changes)"
            class="git-worktree-row__stats"
            :title="t('git.worktree.changesHint')"
          >
            <span class="git-worktree-row__stats-added">
              {{ formatWorktreeChanges(entry.changes)?.additions }}
            </span>
            <span class="git-worktree-row__stats-removed">
              {{ formatWorktreeChanges(entry.changes)?.deletions }}
            </span>
          </p>
          <p v-else class="git-worktree-row__stats git-worktree-row__stats--unavailable">
            {{ t("git.worktree.changesUnavailable") }}
          </p>
          <div class="git-worktree-row__actions">
            <el-button
              v-if="boundWorktreePath === entry.path"
              text
              size="small"
              :disabled="loading"
              @click="unbindWorktree"
            >
              {{ t("git.worktree.unbind") }}
            </el-button>
            <el-button v-else text size="small" :disabled="loading" @click="bindWorktree(entry)">
              {{ t("git.worktree.bind") }}
            </el-button>
            <el-button
              text
              size="small"
              :icon="entry.is_locked ? Unlock : Lock"
              :title="
                entry.is_main
                  ? t('git.worktree.lockMainDisabled')
                  : entry.is_locked
                    ? t('git.worktree.unlock')
                    : t('git.worktree.lock')
              "
              :aria-label="entry.is_locked ? t('git.worktree.unlock') : t('git.worktree.lock')"
              :disabled="entry.is_main || loading"
              @click="toggleWorktreeLock(entry)"
            />
            <el-button
              text
              size="small"
              :icon="Delete"
              :title="t('common.delete')"
              :aria-label="t('common.delete')"
              :disabled="entry.is_main || loading"
              @click="removeWorktree(entry)"
            />
          </div>
        </div>
      </article>
      <el-empty
        v-if="worktrees.length === 0 && !loading"
        class="git-ref-list__empty"
        :image-size="56"
        :description="t('git.worktree.empty')"
      />
    </section>
  </section>
</template>
