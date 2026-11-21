<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { ElMessage } from "element-plus";
import { FolderOpened, Refresh } from "@element-plus/icons-vue";
import { useVueI18n } from "@/i18n/messages";
import { projectDirname, joinProjectPath } from "@/domain/projects/types";
import {
  buildBatchWorktreePlan,
  type BatchWorktreeBranchMode,
  type BatchWorktreeIssue,
} from "@/domain/git/batch-worktree";
import {
  addGitWorktree,
  chooseGitDirectory,
  listGitWorktrees,
  listProjectRepositories,
  type GitBranches,
  type GitRepositoryEntry,
  type GitWorktreeEntry,
} from "@/services/git-service";
import { invokeBackend } from "@/services/desktop-bridge";

defineOptions({ name: "GitBatchWorktreePanel" });

const props = defineProps<{ repoPath: string }>();
const emit = defineEmits<{ changed: []; busy: [busy: boolean] }>();

interface BatchRepository extends GitRepositoryEntry {
  selected: boolean;
  currentBranch: string;
  branchNames: string[];
  localBranchNames: string[];
  worktrees: GitWorktreeEntry[];
  scanError?: string;
  branchMode: BatchWorktreeBranchMode;
  branch: string;
  baseBranch: string;
  result: "idle" | "creating" | "success" | "failed";
  resultError?: string;
}

const { t } = useVueI18n();
const sourceRoot = ref("");
const targetRoot = ref("");
const targetRootEdited = ref(false);
const branchMode = ref<BatchWorktreeBranchMode>("new");
const newBranchName = ref("");
const newBranchTouched = ref(false);
const existingBranch = ref("");
const baseMode = ref<"current" | "specified">("current");
const specifiedBaseBranch = ref("");
const repositories = ref<BatchRepository[]>([]);
const scanning = ref(false);
const creating = ref(false);

function errorText(error: unknown) {
  return error instanceof Error ? error.message : String(error);
}

async function mapWithConcurrency<T>(
  values: readonly T[],
  limit: number,
  task: (value: T) => Promise<void>,
) {
  let nextIndex = 0;
  const workers = Array.from(
    { length: Math.min(Math.max(1, limit), values.length) },
    async () => {
      while (nextIndex < values.length) {
        const index = nextIndex;
        nextIndex += 1;
        const value = values[index];
        if (value !== undefined) await task(value);
      }
    },
  );
  await Promise.all(workers);
}

function planned(repository: BatchRepository) {
  return buildBatchWorktreePlan(repository, {
    targetRoot: targetRoot.value,
    branch: repository.branch,
    baseBranch: repository.baseBranch,
    branchMode: repository.branchMode,
  });
}

const commonLocalBranches = computed(() => {
  const available = repositories.value.filter((repository) => !repository.scanError);
  if (!available.length) return [];
  return available[0]?.localBranchNames.filter((branchName) =>
    available.every((repository) => repository.localBranchNames.includes(branchName))
  ) ?? [];
});

function defaultExistingBranch(repository: BatchRepository) {
  if (existingBranch.value && repository.localBranchNames.includes(existingBranch.value)) {
    return existingBranch.value;
  }
  if (repository.currentBranch && repository.localBranchNames.includes(repository.currentBranch)) {
    return repository.currentBranch;
  }
  return repository.localBranchNames[0] ?? "";
}

function applyBranchMode(mode: BatchWorktreeBranchMode) {
  branchMode.value = mode;
  if (mode === "existing" && !commonLocalBranches.value.includes(existingBranch.value)) {
    existingBranch.value = commonLocalBranches.value[0] ?? "";
  }
  for (const repository of repositories.value) {
    if (repository.result === "success") continue;
    repository.branchMode = mode;
    repository.branch = mode === "new" ? newBranchName.value : defaultExistingBranch(repository);
  }
  resetResults();
}

function applyNewBranchName(value: string) {
  newBranchTouched.value = true;
  for (const repository of repositories.value) {
    if (repository.result !== "success" && repository.branchMode === "new") {
      repository.branch = value;
    }
  }
  resetResults();
}

function applyExistingBranch(value: string) {
  for (const repository of repositories.value) {
    if (repository.result !== "success" && repository.branchMode === "existing") {
      repository.branch = repository.localBranchNames.includes(value)
        ? value
        : defaultExistingBranch(repository);
    }
  }
  resetResults();
}

function applyBaseMode(mode: "current" | "specified") {
  baseMode.value = mode;
  for (const repository of repositories.value) {
    if (repository.result !== "success" && repository.branchMode === "new") {
      repository.baseBranch = mode === "current"
        ? repository.currentBranch
        : specifiedBaseBranch.value;
    }
  }
  resetResults();
}

function applySpecifiedBaseBranch(value: string) {
  for (const repository of repositories.value) {
    if (repository.result !== "success" && repository.branchMode === "new") {
      repository.baseBranch = value;
    }
  }
  resetResults();
}

function setRepositoryMode(repository: BatchRepository, mode: BatchWorktreeBranchMode) {
  repository.branchMode = mode;
  repository.branch = mode === "new" ? newBranchName.value : defaultExistingBranch(repository);
  if (mode === "new") {
    repository.baseBranch = baseMode.value === "current"
      ? repository.currentBranch
      : specifiedBaseBranch.value;
  }
  resetResults();
}

function issueLabel(issue: BatchWorktreeIssue | "scan_failed") {
  return t(`git.worktree.batch.issue.${issue}`);
}

/** 所有仓库都会命中的输入错误只在表单显示一次，避免列表逐行重复同一提示。 */
const formIssue = computed<BatchWorktreeIssue | undefined>(() => {
  const repository = repositories.value.find((candidate) => !candidate.scanError);
  if (!repository) return undefined;
  const issue = planned(repository).issue;
  if (issue === "missing_target_root") return issue;
  if (branchMode.value === "new" && issue === "invalid_branch") return issue;
  if (branchMode.value === "existing" && issue === "missing_existing_branch") return issue;
  if (branchMode.value === "new" && baseMode.value === "specified" && issue === "missing_base_branch") return issue;
  return undefined;
});

/** 新分支名尚未输入时只提示待填写，不在首次打开时直接标红。 */
const formIssueVisible = computed(() =>
  Boolean(formIssue.value) &&
  !(formIssue.value === "invalid_branch" && !newBranchTouched.value && !newBranchName.value.trim()),
);

function repositoryIssue(repository: BatchRepository) {
  if (repository.scanError) return "scan_failed" as const;
  const plan = planned(repository);
  if (plan.issue && plan.issue !== formIssue.value) return plan.issue;
  const targetPath = plan.worktreePath
    .replace(/\\/g, "/")
    .replace(/\/+$/g, "")
    .toLowerCase();
  const duplicatePaths = repositories.value.filter((candidate) => {
    const candidatePlan = planned(candidate);
    return !candidatePlan.issue && candidatePlan.worktreePath
      .replace(/\\/g, "/")
      .replace(/\/+$/g, "")
      .toLowerCase() === targetPath;
  });
  return duplicatePaths.length > 1 ? "duplicate_target_path" as const : undefined;
}

const selectedRepositories = computed(() =>
  formIssue.value
    ? []
    : repositories.value.filter(
        (repository) =>
          repository.selected &&
          repository.result !== "success" &&
          !repositoryIssue(repository),
      ),
);

const selectedCount = computed(
  () => repositories.value.filter((repository) => repository.selected).length,
);

function resetResults() {
  for (const repository of repositories.value) {
    // 已成功项必须保留，修正参数后只重试失败项，避免重复创建或误报失败。
    if (repository.result === "failed") {
      repository.result = "idle";
      repository.resultError = undefined;
    }
  }
}

async function scanRepositories() {
  const root = sourceRoot.value.trim();
  if (!root) return;
  scanning.value = true;
  repositories.value = [];
  try {
    const entries = await listProjectRepositories(root);
    const rows: BatchRepository[] = entries.map((entry) => ({
      ...entry,
      selected: true,
      currentBranch: "",
      branchNames: [],
      localBranchNames: [],
      worktrees: [],
      branchMode: branchMode.value,
      branch: branchMode.value === "new" ? newBranchName.value : "",
      baseBranch: "",
      result: "idle",
    }));
    repositories.value = rows;
    await mapWithConcurrency(rows, 4, async (repository) => {
      try {
        const [branches, worktrees] = await Promise.all([
          invokeBackend<GitBranches>("git_list_branches", { repoPath: repository.path }),
          listGitWorktrees(repository.path),
        ]);
        repository.currentBranch = branches.current === "HEAD" ? "" : branches.current;
        repository.branchNames = branches.branches.map((entry) => entry.name);
        repository.localBranchNames = branches.branches
          .filter((entry) => !entry.is_remote)
          .map((entry) => entry.name);
        repository.worktrees = worktrees;
        repository.baseBranch = baseMode.value === "current"
          ? repository.currentBranch
          : specifiedBaseBranch.value;
        if (repository.branchMode === "existing") {
          repository.branch = defaultExistingBranch(repository);
        }
      } catch (error) {
        repository.selected = false;
        repository.scanError = errorText(error);
      }
    });
    if (branchMode.value === "existing") {
      if (!commonLocalBranches.value.includes(existingBranch.value)) {
        existingBranch.value = commonLocalBranches.value[0] ?? "";
      }
      for (const repository of rows) {
        if (!repository.scanError) repository.branch = defaultExistingBranch(repository);
      }
    }
    if (!rows.length) ElMessage.warning(t("git.worktree.batch.empty"));
  } catch (error) {
    ElMessage.error(errorText(error));
  } finally {
    scanning.value = false;
  }
}

async function chooseSourceRoot() {
  const path = await chooseGitDirectory(
    t("git.worktree.batch.chooseSourceRoot"),
    sourceRoot.value || projectDirname(props.repoPath),
  );
  if (!path) return;
  sourceRoot.value = path;
  if (!targetRootEdited.value) targetRoot.value = joinProjectPath(path, ".worktrees");
  await scanRepositories();
}

async function chooseTargetRoot() {
  const path = await chooseGitDirectory(
    t("git.worktree.batch.chooseTargetRoot"),
    targetRoot.value || sourceRoot.value,
  );
  if (!path) return;
  targetRoot.value = path;
  targetRootEdited.value = true;
  resetResults();
}

async function initialize() {
  sourceRoot.value = projectDirname(props.repoPath);
  targetRoot.value = joinProjectPath(sourceRoot.value, ".worktrees");
  targetRootEdited.value = false;
  branchMode.value = "new";
  newBranchName.value = "";
  newBranchTouched.value = false;
  existingBranch.value = "";
  baseMode.value = "current";
  specifiedBaseBranch.value = "";
  repositories.value = [];
  await scanRepositories();
}

// 批量区默认常驻；首次挂载及切换仓库时都重新扫描，避免沿用旧仓库状态。
watch(() => props.repoPath, initialize, { immediate: true });

async function createBatch() {
  const targets = selectedRepositories.value;
  if (!targets.length) return;
  creating.value = true;
  emit("busy", true);
  try {
    await mapWithConcurrency(targets, 3, async (repository) => {
      repository.result = "creating";
      repository.resultError = undefined;
      const plan = planned(repository);
      try {
        await addGitWorktree(
          repository.path,
          plan.worktreePath,
          repository.branch.trim(),
          repository.branchMode === "new",
          repository.branchMode === "new" ? plan.baseBranch : undefined,
        );
        repository.result = "success";
      } catch (error) {
        repository.result = "failed";
        repository.resultError = errorText(error);
      }
    });
  } finally {
    creating.value = false;
    emit("busy", false);
  }
  const succeeded = targets.filter((repository) => repository.result === "success").length;
  const failed = targets.length - succeeded;
  if (succeeded) emit("changed");
  if (failed) {
    ElMessage.warning(t("git.worktree.batch.partialResult", { succeeded, failed }));
  } else {
    ElMessage.success(t("git.worktree.batch.success", { count: succeeded }));
  }
}

</script>

<template>
  <section class="git-batch-worktree-panel">
    <header class="git-batch-worktree-panel__head">
      <strong>{{ t("git.worktree.batch.title") }}</strong>
    </header>
    <div class="git-batch-worktree-form">
      <label class="git-advanced-field git-advanced-field--wide">
        <span>{{ t("git.worktree.batch.sourceRoot") }}</span>
        <el-input v-model="sourceRoot" size="small" readonly>
          <template #append>
            <el-button
              :icon="FolderOpened"
              :title="t('git.worktree.batch.chooseSourceRoot')"
              :disabled="creating"
              @click="chooseSourceRoot"
            />
          </template>
        </el-input>
      </label>
      <label
        class="git-advanced-field git-advanced-field--wide"
        :class="{ 'is-error': formIssue === 'missing_target_root' }"
      >
        <span>{{ t("git.worktree.batch.targetRoot") }}</span>
        <el-input
          v-model="targetRoot"
          size="small"
          :placeholder="t('git.worktree.batch.targetRootHint')"
          :disabled="creating"
          @input="targetRootEdited = true; resetResults()"
        >
          <template #append>
            <el-button
              :icon="FolderOpened"
              :title="t('git.worktree.batch.chooseTargetRoot')"
              :disabled="creating"
              @click="chooseTargetRoot"
            />
          </template>
        </el-input>
        <small v-if="formIssue === 'missing_target_root'" class="git-batch-worktree-field-error">
          {{ issueLabel(formIssue) }}
        </small>
      </label>
      <label class="git-advanced-field">
        <span>{{ t("git.worktree.mode") }}</span>
        <el-select
          v-model="branchMode"
          size="small"
          :disabled="creating"
          @change="applyBranchMode"
        >
          <el-option :label="t('git.worktree.newBranch')" value="new" />
          <el-option :label="t('git.worktree.existingBranch')" value="existing" />
        </el-select>
      </label>
      <label
        v-if="branchMode === 'new'"
        class="git-advanced-field"
        :class="{ 'is-error': formIssueVisible && formIssue === 'invalid_branch' }"
      >
        <span>{{ t("git.worktree.branch") }}</span>
        <el-input
          v-model="newBranchName"
          size="small"
          :placeholder="t('git.worktree.branchHint')"
          :disabled="creating"
          @input="applyNewBranchName"
        />
        <small v-if="formIssueVisible && formIssue === 'invalid_branch'" class="git-batch-worktree-field-error">
          {{ issueLabel(formIssue) }}
        </small>
      </label>
      <label
        v-else
        class="git-advanced-field"
        :class="{ 'is-error': formIssue === 'missing_existing_branch' }"
      >
        <span>{{ t("git.worktree.batch.existingBranchName") }}</span>
        <el-select
          v-model="existingBranch"
          size="small"
          filterable
          :placeholder="t('git.worktree.selectBranch')"
          :disabled="creating"
          @change="applyExistingBranch"
        >
          <el-option
            v-for="branchName in commonLocalBranches"
            :key="branchName"
            :label="branchName"
            :value="branchName"
          />
        </el-select>
        <small v-if="formIssue === 'missing_existing_branch'" class="git-batch-worktree-field-error">
          {{ issueLabel(formIssue) }}
        </small>
      </label>
      <label v-if="branchMode === 'new'" class="git-advanced-field">
        <span>{{ t("git.worktree.baseBranch") }}</span>
        <el-select
          v-model="baseMode"
          size="small"
          :disabled="creating"
          @change="applyBaseMode"
        >
          <el-option :label="t('git.worktree.batch.eachCurrentBranch')" value="current" />
          <el-option :label="t('git.worktree.batch.specifiedBaseBranch')" value="specified" />
        </el-select>
      </label>
      <label
        v-if="branchMode === 'new' && baseMode === 'specified'"
        class="git-advanced-field"
        :class="{ 'is-error': formIssue === 'missing_base_branch' }"
      >
        <span>{{ t("git.worktree.batch.baseBranchName") }}</span>
        <el-input
          v-model="specifiedBaseBranch"
          size="small"
          :placeholder="t('git.worktree.selectBaseBranch')"
          :disabled="creating"
          @input="applySpecifiedBaseBranch"
        />
        <small v-if="formIssue === 'missing_base_branch'" class="git-batch-worktree-field-error">
          {{ issueLabel(formIssue) }}
        </small>
      </label>
    </div>

    <div class="git-batch-worktree-toolbar">
      <span>{{ t("git.worktree.batch.selected", { selected: selectedCount, total: repositories.length }) }}</span>
      <el-button
        size="small"
        :icon="Refresh"
        :loading="scanning"
        :disabled="creating || !sourceRoot.trim()"
        @click="scanRepositories"
      >
        {{ t("git.worktree.batch.rescan") }}
      </el-button>
    </div>

    <div class="git-batch-worktree-list">
      <div
        v-for="repository in repositories"
        :key="repository.path"
        class="git-batch-worktree-row"
        :class="{
          'is-disabled': Boolean(repositoryIssue(repository)),
          'is-success': repository.result === 'success',
          'is-failed': repository.result === 'failed',
        }"
      >
        <el-checkbox
          v-model="repository.selected"
          :disabled="creating || Boolean(repositoryIssue(repository)) || repository.result === 'success'"
        />
        <span class="git-batch-worktree-row__repository">
          <strong :title="repository.name">{{ repository.name }}</strong>
          <small :title="repository.path">{{ repository.relative_path }}</small>
        </span>
        <el-select
          :model-value="repository.branchMode"
          class="git-batch-worktree-row__mode"
          size="small"
          :disabled="creating || repository.result === 'success'"
          @change="setRepositoryMode(repository, $event)"
        >
          <el-option :label="t('git.worktree.newBranch')" value="new" />
          <el-option :label="t('git.worktree.existingBranch')" value="existing" />
        </el-select>
        <el-select
          v-if="repository.branchMode === 'existing'"
          v-model="repository.branch"
          class="git-batch-worktree-row__branch"
          size="small"
          filterable
          :placeholder="t('git.worktree.selectBranch')"
          :disabled="creating || repository.result === 'success'"
          @change="resetResults"
        >
          <el-option
            v-for="branchName in repository.localBranchNames"
            :key="branchName"
            :label="branchName"
            :value="branchName"
          />
        </el-select>
        <el-select
          v-else
          v-model="repository.baseBranch"
          class="git-batch-worktree-row__branch"
          size="small"
          filterable
          :placeholder="t('git.worktree.selectBaseBranch')"
          :disabled="creating || repository.result === 'success'"
          @change="resetResults"
        >
          <el-option
            v-for="branchName in repository.branchNames"
            :key="branchName"
            :label="branchName"
            :value="branchName"
          />
        </el-select>
        <span
          class="git-batch-worktree-row__path"
          :class="{ 'is-empty': !planned(repository).worktreePath }"
          :title="planned(repository).worktreePath"
        >
          {{ planned(repository).worktreePath || t("git.worktree.batch.pathPending") }}
        </span>
        <span v-if="repository.result === 'creating'" class="git-batch-worktree-row__status">
          {{ t("git.worktree.batch.creating") }}
        </span>
        <span v-else-if="repository.result === 'success'" class="git-batch-worktree-row__status is-success">
          {{ t("git.worktree.batch.created") }}
        </span>
        <span
          v-else-if="repository.result === 'failed'"
          class="git-batch-worktree-row__status is-failed"
          :title="repository.resultError"
        >
          {{ t("git.worktree.batch.failed") }}
        </span>
        <span
          v-else-if="repositoryIssue(repository)"
          class="git-batch-worktree-row__status is-warning"
          :title="repository.scanError || issueLabel(repositoryIssue(repository)!)"
        >
          {{ issueLabel(repositoryIssue(repository)!) }}
        </span>
        <span v-else-if="formIssue" class="git-batch-worktree-row__status is-pending">
          {{ t("git.worktree.batch.awaitingInput") }}
        </span>
        <span v-else class="git-batch-worktree-row__status">
          {{ t("git.worktree.batch.ready") }}
        </span>
      </div>
      <p v-if="!repositories.length && !scanning" class="git-inspector-empty">
        {{ t("git.worktree.batch.empty") }}
      </p>
    </div>

    <div class="git-batch-worktree-footer">
      <span>{{ t("git.worktree.batch.partialFailureHint") }}</span>
      <el-button
        type="primary"
        :loading="creating"
        :disabled="scanning || !selectedRepositories.length"
        @click="createBatch"
      >
        {{ t("git.worktree.batch.createSelected", { count: selectedRepositories.length }) }}
      </el-button>
    </div>
  </section>
</template>
