<script setup lang="ts">
import type { BackendCommand } from "@/generated/backend-commands";
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import { CirclePlus, Delete, EditPen, Share } from "@element-plus/icons-vue";
import { useVueI18n } from "@/i18n/messages";
import { invokeBackend } from "@/services/desktop-bridge";
import {
  getGitMergeState,
  runGitCommand,
  type GitBranches,
  type GitMergeState,
} from "@/services/git-service";
import GitConflictResolver from "@/components/git/GitConflictResolver.vue";
import AppSearchField from "@/components/ui/AppSearchField.vue";
import { createLatestRequestGate } from "@/domain/async/latest-request-gate";
import type { GitProjectStatus } from "@/domain/projects/types";

defineOptions({ name: "GitBranchPanel" });

const props = defineProps<{ repoPath: string }>();
const emit = defineEmits<{ changed: []; busy: [value: boolean] }>();

const { t } = useVueI18n();
const branches = ref<GitBranches | null>(null);
const status = ref<GitProjectStatus | null>(null);
const mergeState = ref<GitMergeState>({ active: false, conflicts: [] });
const conflictResolverVisible = ref(false);
const loading = ref(false);
const operationPending = ref(false);
const refreshGate = createLatestRequestGate(() => props.repoPath);
let actionRepoPath = "";

// 从确认框打开到命令完成始终锁定仓库，不能只覆盖网络请求阶段。
async function withBranchAction(action: () => Promise<void>) {
  if (loading.value || operationPending.value || !branches.value || !status.value) return;
  operationPending.value = true;
  actionRepoPath = props.repoPath;
  try {
    await action();
  } finally {
    actionRepoPath = "";
    operationPending.value = false;
  }
}

const localBranches = computed(
  () => branches.value?.branches.filter((branch) => !branch.is_remote) ?? []
);
const branchQuery = ref("");
// 当前分支始终排在最前，其余保持 git 给出的顺序；搜索只过滤名字，不改变顺序。
const visibleBranches = computed(() => {
  const query = branchQuery.value.trim().toLowerCase();
  const matches = query
    ? localBranches.value.filter((branch) => branch.name.toLowerCase().includes(query))
    : localBranches.value;
  return [...matches.filter((branch) => branch.is_current), ...matches.filter((branch) => !branch.is_current)];
});
const mergeLocalBranches = computed(() =>
  localBranches.value.filter((branch) => !branch.is_current && branch.name !== branches.value?.current)
);
const mergeRemoteBranches = computed(
  () => branches.value?.branches.filter((branch) => branch.is_remote) ?? []
);
const mergeBranchNames = computed(() => [
  ...mergeLocalBranches.value.map((branch) => branch.name),
  ...mergeRemoteBranches.value.map((branch) => branch.name),
]);
const mergeDialogVisible = ref(false);
const selectedMergeBranch = ref("");
const totalChanges = computed(
  () =>
    (status.value?.staged.length ?? 0) +
    (status.value?.unstaged.length ?? 0) +
    (status.value?.untracked.length ?? 0)
);

async function refresh() {
  const request = refreshGate.begin();
  branches.value = null;
  status.value = null;
  mergeState.value = { active: false, conflicts: [] };
  if (!request.target) {
    loading.value = false;
    return;
  }
  loading.value = true;
  try {
    const [nextBranches, nextStatus, nextMergeState] = await Promise.all([
      invokeBackend<GitBranches>("git_list_branches", { repoPath: request.target }),
      invokeBackend<GitProjectStatus>("git_get_status", { repoPath: request.target }),
      getGitMergeState(request.target),
    ]);
    if (!refreshGate.isCurrent(request)) return;
    branches.value = nextBranches;
    status.value = nextStatus;
    mergeState.value = nextMergeState;
  } catch (error) {
    if (refreshGate.isCurrent(request)) ElMessage.error(String(error));
  } finally {
    if (refreshGate.isCurrent(request)) loading.value = false;
  }
}

async function run(command: BackendCommand, args: Record<string, unknown> = {}) {
  const targetPath = actionRepoPath;
  if (!targetPath || targetPath !== props.repoPath) return false;
  loading.value = true;
  try {
    await runGitCommand(command, targetPath, args);
    if (targetPath === props.repoPath) await refresh();
    emit("changed");
    return true;
  } catch (error) {
    ElMessage.error(String(error));
    if (targetPath === props.repoPath) {
      await refresh();
      emit("changed");
    }
    return false;
  } finally {
    loading.value = false;
  }
}

async function promptValue(message: string, title: string, initialValue = "", placeholder = "") {
  try {
    const { value } = await ElMessageBox.prompt(message, title, {
      inputValue: initialValue,
      inputPlaceholder: placeholder,
      inputValidator: (input) => (input.trim() ? true : t("git.inputRequired")),
      confirmButtonText: t("common.confirm"),
      cancelButtonText: t("common.cancel"),
    });
    return value.trim();
  } catch {
    return "";
  }
}

async function checkoutBranch(branchName: string) {
  if (!branchName || branchName === branches.value?.current) return;
  if (totalChanges.value === 0) {
    await run("git_checkout_branch", { branchName, create: false });
    return;
  }
  // 工作区有改动时切分支会失败，先征得同意再连带 stash，避免用户丢失上下文。
  try {
    await ElMessageBox.confirm(
      t("git.stashBeforeCheckoutConfirm", { branch: branchName }),
      t("git.stashBeforeCheckoutTitle"),
      {
        type: "warning",
        confirmButtonText: t("git.stashAndCheckout"),
        cancelButtonText: t("common.cancel"),
      }
    );
  } catch {
    return;
  }
  if (await run("git_checkout_branch", { branchName, create: false, stashChanges: true })) {
    ElMessage.success(t("git.stashBeforeCheckoutSuccess", { branch: branchName }));
  }
}

async function createBranch() {
  const branchName = await promptValue(
    t("git.createBranchPrompt"),
    t("git.createBranchTitle"),
    "",
    "feature/my-change"
  );
  if (branchName) await run("git_checkout_branch", { branchName, create: true });
}

async function renameCurrentBranch() {
  const oldName = branches.value?.current;
  if (!oldName || oldName === "HEAD") return;
  const newName = await promptValue(
    t("git.renameBranchPrompt"),
    t("git.renameBranchTitle"),
    oldName
  );
  if (newName && newName !== oldName) await run("git_rename_branch", { oldName, newName });
}

async function deleteBranch(branchName: string) {
  try {
    await ElMessageBox.confirm(
      t("git.deleteBranchConfirm", { branch: branchName }),
      t("git.deleteBranchTitle"),
      {
        type: "warning",
        confirmButtonText: t("common.continue"),
        cancelButtonText: t("common.cancel"),
      }
    );
  } catch {
    return;
  }
  await run("git_delete_branch", { branchName, force: false });
}

function openMergeBranchDialog() {
  if (totalChanges.value > 0) {
    ElMessage.warning(t("git.conflict.cleanWorktreeRequired"));
    return;
  }
  selectedMergeBranch.value = "";
  mergeDialogVisible.value = true;
}

async function mergeBranch() {
  const branchName = selectedMergeBranch.value;
  if (!mergeBranchNames.value.includes(branchName)) return;
  if (await run("git_merge_branch", { branchName })) {
    mergeDialogVisible.value = false;
    conflictResolverVisible.value = false;
    selectedMergeBranch.value = "";
    return;
  }
  if (mergeState.value.active) {
    mergeDialogVisible.value = false;
    conflictResolverVisible.value = true;
  }
}

async function handleConflictChanged() {
  await refresh();
  emit("changed");
}

watch(
  () => props.repoPath,
  () => {
    mergeDialogVisible.value = false;
    conflictResolverVisible.value = false;
    selectedMergeBranch.value = "";
    branchQuery.value = "";
    void refresh();
  },
  { immediate: true }
);

watch(
  () => loading.value || operationPending.value || mergeDialogVisible.value || conflictResolverVisible.value,
  (busy) => emit("busy", busy),
  { immediate: true, flush: "sync" }
);
onBeforeUnmount(() => refreshGate.invalidate());

defineExpose({ refresh });
</script>

<template>
  <el-scrollbar class="git-branch-panel__scrollbar">
    <section class="git-inspector">
      <slot name="header" />
      <div v-if="mergeState.active" class="git-merge-conflict-banner">
        <div>
          <strong>
            {{ t(mergeState.conflicts.length > 0
              ? "git.conflict.mergeInProgress"
              : "git.conflict.readyForManualCommitTitle") }}
          </strong>
          <small>
            {{ mergeState.conflicts.length > 0
              ? t("git.conflict.remaining", { count: mergeState.conflicts.length })
              : t("git.conflict.readyForManualCommitHint") }}
          </small>
        </div>
        <el-button
          v-if="mergeState.conflicts.length > 0"
          type="danger"
          plain
          size="small"
          @click="conflictResolverVisible = true"
        >
          {{ t("git.conflict.openResolver") }}
        </el-button>
      </div>
      <div class="git-inspector__actions git-ref-toolbar">
        <div class="git-ref-toolbar__buttons">
          <el-button
            size="small"
            type="primary"
            :icon="CirclePlus"
            :loading="loading"
            :disabled="operationPending || mergeState.active || !branches || !status"
            @click="withBranchAction(createBranch)"
            >{{ t("git.createBranchTitle") }}</el-button
          >
          <el-button
            size="small"
            :icon="EditPen"
            :disabled="operationPending || loading || mergeState.active || !branches?.current || branches.current === 'HEAD'"
            @click="withBranchAction(renameCurrentBranch)"
            >{{ t("git.renameBranchTitle") }}</el-button
          >
          <el-button
            size="small"
            :disabled="operationPending || loading || mergeState.active || mergeBranchNames.length === 0"
            @click="openMergeBranchDialog"
          >{{ t("git.mergeBranchTitle") }}</el-button>
        </div>
        <AppSearchField
          v-model="branchQuery"
          class="git-ref-toolbar__search"
          clearable
          :clear-label="t('common.clear')"
          :placeholder="t('git.branchSearchPlaceholder')"
        />
      </div>
      <section class="git-ref-list" :aria-label="t('git.localBranches')">
        <header class="git-ref-list__header">
          <span>{{ t("git.localBranches") }}</span>
          <span class="git-ref-list__count">{{
            branchQuery.trim() ? `${visibleBranches.length} / ${localBranches.length}` : localBranches.length
          }}</span>
        </header>
        <el-skeleton v-if="!branches && loading" class="git-ref-list__skeleton" :rows="3" animated />
        <el-empty
          v-else-if="branches && visibleBranches.length === 0"
          class="git-ref-list__empty"
          :image-size="56"
          :description="branchQuery.trim() ? t('git.branchSearchEmpty') : t('git.noLocalBranches')"
        />
        <div
          v-for="branch in visibleBranches"
          :key="branch.name"
          class="git-inspector-row git-ref-list__row"
          :class="{ 'is-current': branch.is_current }"
        >
          <el-button
            text
            native-type="button"
            class="git-inspector-row__name"
            :class="{ 'is-current': branch.is_current }"
            :title="branch.is_current ? branch.name : t('git.branchCheckoutHint', { branch: branch.name })"
            :disabled="loading || operationPending"
            @click="withBranchAction(() => checkoutBranch(branch.name))"
          >
            <el-icon class="git-ref-list__icon" aria-hidden="true"><Share /></el-icon>
            <span class="git-ref-list__name">{{ branch.name }}</span>
            <el-tag v-if="branch.is_current" size="small" effect="light" disable-transitions>{{ t("git.current") }}</el-tag>
          </el-button>
          <el-button
            v-if="!branch.is_current"
            text
            size="small"
            class="git-inspector-row__action"
            :icon="Delete"
            :title="t('git.deleteBranchTitle')"
            :aria-label="t('git.deleteBranchTitle')"
            :disabled="loading || operationPending"
            @click="withBranchAction(() => deleteBranch(branch.name))"
          />
        </div>
      </section>
    </section>
  </el-scrollbar>

  <el-dialog
    v-model="conflictResolverVisible"
    class="git-conflict-resolver-dialog"
    append-to-body
    destroy-on-close
    :title="t('git.conflict.title')"
    width="min(1500px, 96vw)"
    top="3vh"
  >
    <GitConflictResolver
      v-if="conflictResolverVisible"
      :repo-path="repoPath"
      @changed="handleConflictChanged"
      @close="conflictResolverVisible = false"
    />
  </el-dialog>

  <el-dialog
    v-model="mergeDialogVisible"
    class="git-merge-branch-dialog"
    append-to-body
    :title="t('git.mergeBranchTitle')"
    width="480px"
    :close-on-click-modal="!loading && !operationPending"
    :close-on-press-escape="!loading && !operationPending"
    :show-close="!loading && !operationPending"
  >
    <p class="git-merge-branch-dialog__hint">
      {{ t("git.mergeBranchPrompt", { branch: branches?.current ?? "HEAD" }) }}
    </p>
    <el-select
      v-model="selectedMergeBranch"
      class="git-merge-branch-dialog__select"
      filterable
      :disabled="loading || operationPending"
      :placeholder="t('git.mergeBranchPlaceholder')"
    >
      <el-option-group
        v-if="mergeLocalBranches.length"
        :label="t('git.localBranches')"
      >
        <el-option
          v-for="branch in mergeLocalBranches"
          :key="branch.name"
          :label="branch.name"
          :value="branch.name"
        />
      </el-option-group>
      <el-option-group
        v-if="mergeRemoteBranches.length"
        :label="t('git.remoteBranches')"
      >
        <el-option
          v-for="branch in mergeRemoteBranches"
          :key="branch.name"
          :label="branch.name"
          :value="branch.name"
        />
      </el-option-group>
    </el-select>
    <template #footer>
      <el-button :disabled="loading || operationPending" @click="mergeDialogVisible = false">
        {{ t("common.cancel") }}
      </el-button>
      <el-button
        type="primary"
        :loading="loading"
        :disabled="!selectedMergeBranch"
        @click="withBranchAction(mergeBranch)"
      >
        {{ t("common.confirm") }}
      </el-button>
    </template>
  </el-dialog>
</template>
