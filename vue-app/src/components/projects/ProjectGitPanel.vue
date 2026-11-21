<script setup lang="ts">
import "@/styles/workspaces/project/git-panel.css";
import {
  computed,
  defineAsyncComponent,
  onActivated,
  onDeactivated,
  nextTick,
  onMounted,
  onUnmounted,
  reactive,
  ref,
  watch,
} from "vue";
import {
  ArrowDown,
  ArrowUp,
  CirclePlus,
  CopyDocument,
  Delete,
  Document,
  Download,
  EditPen,
  FolderOpened,
  Guide,
  MagicStick,
  MoreFilled,
  Operation,
  Plus,
  Refresh,
  RefreshLeft,
  Remove,
  Share,
} from "@element-plus/icons-vue";
import { ElMessage, ElMessageBox } from "element-plus";
import GitHistoryList from "@/components/git/GitHistoryList.vue";
import GitShelfPanel from "@/components/git/GitShelfPanel.vue";
import GitStashPanel from "@/components/git/GitStashPanel.vue";
import AppContextMenu from "@/components/ui/AppContextMenu.vue";
import PluginMenuItems from "@/components/plugins/PluginMenuItems.vue";
import { usePluginMenuTarget } from "@/composables/use-plugin-menu-target";
import AppChevron from "@/components/ui/AppChevron.vue";
import { createLatestRequestGate } from "@/domain/async/latest-request-gate";
import { runWithAbort } from "@/domain/async/abort";
import { isAiStreamIdleTimeoutError } from "@/domain/ai/stream-idle-watchdog";
import type { GitFileEntry, GitProjectStatus } from "@/domain/projects/types";
import { ROW_GROUP_ATTRIBUTE, useGroupedRowWindow } from "@/composables/use-grouped-row-window";
import { normalizeAiApiFormat } from "@/domain/ai/api-format";
import { normalizeAnthropicAuth } from "@/domain/ai/anthropic-auth";
import {
  buildAiModelAttemptOrder,
  configuredAiModels,
  resolveAiModelSelection,
} from "@/domain/ai/model-selection";
import { aiProviderAuthMessageKey, validateAiProviderAuth } from "@/domain/ai/provider-auth";
import { useVueI18n, type MessageKey } from "@/i18n/messages";
import {
  getGitCommitDetail,
  getRepositorySummaries,
  listProjectRepositories,
  type GitCommitDetail,
  type GitCommitFile,
  type GitRepositoryEntry,
  type GitRepositorySummary,
} from "@/services/git-service";
import { loadPersistedValue, savePersistedValue } from "@/services/persistent-store";
import { ownsLegacyWorkspaceSnapshot } from "@/services/workspace-scoped-persistence";
import {
  hydrateSettingsSnapshot,
  readSettingsSnapshot,
} from "@/services/settings-persistence";
import { copyText, invokeBackend, isDesktopRuntime, nativeErrorMessage, type BackendCommand } from "@/services/desktop-bridge";
import { notifyApp } from "@/services/notifications";
import { generateAiCommitMessage } from "@/services/ai-commit-message";
import { shouldApplyGeneratedMessage } from "@/domain/plugins/scm-commit-message";
import { usePluginDataProvidersStore } from "@/stores/plugin-data-providers";
import type { LocalProjectEntry } from "@/stores/local-project";
import { useAiAssistantStore } from "@/stores/ai-assistant";

interface GitBranchEntry {
  name: string;
  is_current: boolean;
  is_remote: boolean;
}

interface GitBranches {
  current: string;
  branches: GitBranchEntry[];
}

interface GitLogEntry {
  hash: string;
  short_hash: string;
  author: string;
  email?: string;
  message: string;
  time: number;
  parents?: string[];
  refs?: string[];
}

interface GitStashEntry {
  index: number;
  message: string;
  oid: string;
  files: GitFileEntry[];
}

interface GitSyncResult {
  ok: boolean;
  message: string;
}

interface StoredAiProfile {
  id?: string;
  provider?: string;
  baseUrl?: string;
  apiKey?: string;
  model?: string;
  apiFormat?: string;
  anthropicAuth?: string;
  reasoningEffort?: string;
  contextWindow?: number;
  maxInputTokens?: number;
  maxOutputTokens?: number;
  openaiStreamUsage?: "auto" | "always" | "never";
  noToolModels?: string[];
  commonModels?: unknown;
  failoverEnabled?: boolean;
  failoverModels?: unknown;
}

interface StoredSettings {
  activeAiProfileId?: string;
  aiProfiles?: StoredAiProfile[];
}

type ChangeGroup = "staged" | "unstaged" | "untracked";
type ChangeTab = "changes" | "history" | "shelf" | "stash";
type GitQuickAction =
  | "refresh"
  | "fetch"
  | "pull"
  | "push"
  | "stage-all"
  | "unstage-all"
  | "manage-branches"
  | "show-graph"
  | "manage-worktrees"
  | "create-branch"
  | "rename-branch"
  | "manage-tags"
  | "undo-last-commit"
  | "stash"
  | "manage-shelf"
  | "manage-stash"
  | "interactive-rebase"
  | "discard-all";

const props = withDefaults(
  defineProps<{
    projectRoot: string;
    projects: LocalProjectEntry[];
    extendedActions?: boolean;
    repositoryStorageKey?: string;
    legacyRepositoryStorageKey?: string;
    legacyWorkspaceScopeId?: string;
  }>(),
  {
    extendedActions: false,
    repositoryStorageKey: "app_project_source_repo_path",
  }
);

const emit = defineEmits<{
  "select-project": [root: string];
  "choose-project": [];
  "open-file": [payload: { repoPath: string; entry: GitFileEntry; group: ChangeGroup }];
  "open-worktree-file": [payload: { repoPath: string; entry: GitFileEntry }];
  "open-commit": [payload: { repoPath: string; detail: GitCommitDetail }];
  "open-commit-file": [payload: { repoPath: string; commitHash: string; file: GitCommitFile }];
  "change-count": [count: number];
  "repository-change": [repoPath: string];
  "extended-action": [action: "branches" | "tags" | "shelf" | "stash" | "graph" | "worktrees" | "rebase"];
}>();

const { t } = useVueI18n();
const aiAssistant = useAiAssistantStore();
const repoPath = ref("");
const repositoryScanGate = createLatestRequestGate(() => props.projectRoot);
const repositoryRescanGate = createLatestRequestGate(() => props.projectRoot);
const repositorySelectionGate = createLatestRequestGate(() => repoPath.value);
const refreshGate = createLatestRequestGate(() => repoPath.value);
const repositorySummaryGate = createLatestRequestGate(() => props.projectRoot);
const repositories = ref<GitRepositoryEntry[]>([]);
const repositorySummaries = ref<Record<string, GitRepositorySummary>>({});
const repositoriesOpen = ref(true);
const status = ref<GitProjectStatus | null>(null);
const branches = ref<GitBranches | null>(null);
const logs = ref<GitLogEntry[]>([]);
const stashes = ref<GitStashEntry[]>([]);
const selectedCommitHash = ref("");
const selectedCommit = ref<GitCommitDetail | null>(null);
const selectedCommitFilePath = ref("");
const loading = ref(false);
const stashPanel = ref<InstanceType<typeof GitStashPanel> | null>(null);
// 提交/推送是「提交 → 刷新 → 推送 → 刷新」的复合流程，中途 runGit 会把 loading
// 归位，按钮会闪一下重新变得可点。用独立标志覆盖整段流程，全程禁用按钮。
const submitting = ref(false);
const expandedCommitSections = ref<string[]>([]);
const changeTab = ref<ChangeTab>("changes");
interface RepositoryCommitDraft {
  message: string;
  amend: boolean;
  generating: boolean;
  generation: number;
  /** When a thinking model started reasoning before any visible text (0 = not thinking). */
  thinkingSince: number;
}
const repositoryCommitDrafts = reactive<Record<string, RepositoryCommitDraft>>({});
// No data at all for this long stops the request. Reasoning counts as data, so a
// thinking model is not cut off before its first visible token.
const COMMIT_MESSAGE_IDLE_TIMEOUT_MS = 30_000;
// Upper bound for the whole run, thinking included; the user can cancel earlier.
const COMMIT_MESSAGE_GENERATION_TIMEOUT_MS = 180_000;
let commitMessageGeneration = 0;
const commitMessageAbortControllers = new Map<string, AbortController>();
const selectedFileKey = ref("");
const fileContextMenu = reactive<{
  open: boolean;
  x: number;
  y: number;
  entry: GitFileEntry | null;
  group: ChangeGroup;
}>({
  open: false,
  x: 0,
  y: 0,
  entry: null,
  group: "unstaged",
});
const commandBarElement = ref<HTMLElement | null>(null);
const commandBarWidth = ref(Number.POSITIVE_INFINITY);
let commandBarResizeObserver: ResizeObserver | undefined;
// Large change lists render only the file rows near the viewport (see use-grouped-row-window).
const changeScroller = ref<{ wrapRef?: HTMLElement }>();
const groupOpen = ref<Record<ChangeGroup, boolean>>({
  staged: true,
  unstaged: true,
  untracked: true,
});
const changeRows = useGroupedRowWindow({
  scroller: () => (changeTab.value === "changes" ? changeScroller.value?.wrapRef : undefined),
  counts: () => Object.fromEntries(
    (["staged", "unstaged", "untracked"] as ChangeGroup[]).map((group) => [group, groupOpen.value[group] ? status.value?.[group].length ?? 0 : 0]),
  ),
  rowSelector: ".git-file-row",
  estimatedRowHeight: 29,
});
// A commit can touch thousands of files (vendoring, renames): only the rows near the viewport render.
const commitFilesScroller = ref<{ wrapRef?: HTMLElement }>();
const commitFileRows = useGroupedRowWindow({
  scroller: () => (changeTab.value === "history" ? commitFilesScroller.value?.wrapRef : undefined),
  counts: () => ({ files: selectedCommit.value?.files.length ?? 0 }),
  rowSelector: ".project-git-commit-file",
  estimatedRowHeight: 30,
});
const commitFileWindow = computed(() => commitFileRows.windowOf("files", selectedCommit.value?.files ?? []));

/** The rendered slice of each group's files and the spacer heights around it. */
const groupWindows = computed(() => Object.fromEntries(
  (["staged", "unstaged", "untracked"] as ChangeGroup[]).map((group) => [group, changeRows.windowOf(group, status.value?.[group] ?? [])]),
) as Record<ChangeGroup, ReturnType<typeof changeRows.windowOf<GitFileEntry>>>);

// Row action buttons only mount for the hovered/focused/selected row; rendering
// them for every changed file keeps thousands of idle ElButton instances alive.
const hoveredFileKey = ref<string | null>(null);
const focusedFileKey = ref<string | null>(null);
function showFileActions(key: string) {
  return hoveredFileKey.value === key || focusedFileKey.value === key || selectedFileKey.value === key;
}
function onFileRowFocusOut(event: FocusEvent, key: string) {
  const row = event.currentTarget as HTMLElement | null;
  if (row?.contains(event.relatedTarget as Node | null)) return;
  if (focusedFileKey.value === key) focusedFileKey.value = null;
}
function onFileRowMouseLeave(key: string) {
  if (hoveredFileKey.value === key) hoveredFileKey.value = null;
}

const totalChanges = computed(() => {
  const current = status.value;
  return current ? current.staged.length + current.unstaged.length + current.untracked.length : 0;
});
const quickActionState = computed(() => ({
  repoPath: repoPath.value,
  loading: loading.value,
  hasStaged: Boolean(status.value?.staged.length),
  hasUnstaged: Boolean(status.value?.unstaged.length || status.value?.untracked.length),
  totalChanges: totalChanges.value,
  canRenameBranch: Boolean(branches.value?.current && branches.value.current !== "HEAD"),
  canUndoLastCommit: logs.value.length > 0,
}));
const localBranches = computed(
  () => branches.value?.branches.filter((branch) => !branch.is_remote) ?? []
);
const remoteBranches = computed(
  () => branches.value?.branches.filter((branch) => branch.is_remote) ?? []
);
const selectedBranchValue = computed(() => {
  const current = branches.value?.current || status.value?.branch;
  return current ? `local:${current}` : "";
});
const showContextSwitchers = computed(() => props.projects.length > 0);
const showToolbarUnstage = computed(() => commandBarWidth.value >= 460);
const showToolbarBranches = computed(() => commandBarWidth.value >= 560);
const showToolbarGraph = computed(() => commandBarWidth.value >= 660);
const showToolbarWorktrees = computed(() => commandBarWidth.value >= 760);
// 单仓库工程不需要列表：那一行既没有选择意义，又白占侧栏纵向空间。
const showRepositoryList = computed(() => repositories.value.length > 1);
const changeTabOptions = computed(() => [
  { label: t("project.git.changes"), value: "changes" as ChangeTab },
  { label: t("project.git.history"), value: "history" as ChangeTab },
  { label: t("git.inspector.shelf"), value: "shelf" as ChangeTab },
  { label: t("git.inspector.stash"), value: "stash" as ChangeTab },
]);
// Always hand out the reactive proxy. `(record[key] = value)` evaluates to the raw
// object, so the first draft of a repository used to be mutated outside Vue's
// tracking: the button kept spinning after a run ended, and "Cancel" then did
// nothing until the next click started a new run.
function repositoryCommitDraft(path: string) {
  if (!repositoryCommitDrafts[path]) {
    repositoryCommitDrafts[path] = { message: "", amend: false, generating: false, generation: 0, thinkingSince: 0 };
  }
  return repositoryCommitDrafts[path];
}
const commitMessage = computed({
  get: () => repositoryCommitDrafts[repoPath.value]?.message ?? "",
  set: (message: string) => {
    if (repoPath.value) repositoryCommitDraft(repoPath.value).message = message;
  },
});
const amendLastCommit = computed({
  get: () => repositoryCommitDrafts[repoPath.value]?.amend ?? false,
  set: (amend: boolean) => {
    if (repoPath.value) repositoryCommitDraft(repoPath.value).amend = amend;
  },
});
const generatingCommitMessage = computed(
  () => repositoryCommitDrafts[repoPath.value]?.generating ?? false
);
// Plugin commit message providers (contributes.scmCommitMessage); the button chunk loads only when one is active.
const GitPluginCommitMessageButton = defineAsyncComponent(
  () => import("@/components/projects/GitPluginCommitMessageButton.vue")
);
const pluginDataProviders = usePluginDataProvidersStore();
const scmProviders = computed(() =>
  pluginDataProviders.scmProviders.filter((provider) => provider.activeInWorkspace)
);
// The plugin generation in flight; it shares the draft's generating/generation with the AI.
const pluginCommitRun = ref<{ targetPath: string; generation: number } | null>(null);
const pluginGeneratingCommitMessage = computed(() => {
  const run = pluginCommitRun.value;
  const draft = repositoryCommitDrafts[repoPath.value];
  return Boolean(run && run.targetPath === repoPath.value && draft?.generating && draft.generation === run.generation);
});
const aiGeneratingCommitMessage = computed(
  () => generatingCommitMessage.value && !pluginGeneratingCommitMessage.value
);
// While a thinking model reasons, the empty message box says so and counts the seconds.
const commitThinkingNow = ref(Date.now());
let commitThinkingTicker: ReturnType<typeof setInterval> | undefined;
const commitThinkingSince = computed(() => {
  const draft = repositoryCommitDrafts[repoPath.value];
  return aiGeneratingCommitMessage.value && draft?.thinkingSince ? draft.thinkingSince : 0;
});
watch(commitThinkingSince, (since) => {
  if (commitThinkingTicker) clearInterval(commitThinkingTicker);
  commitThinkingTicker = undefined;
  if (!since) return;
  commitThinkingNow.value = Date.now();
  commitThinkingTicker = setInterval(() => { commitThinkingNow.value = Date.now(); }, 1000);
});
const commitMessagePlaceholder = computed(() => {
  const since = commitThinkingSince.value;
  if (!since) return t("git.commitPlaceholder");
  return t("git.aiThinking", { seconds: Math.max(0, Math.floor((commitThinkingNow.value - since) / 1000)) });
});
const commitReady = computed(() =>
  amendLastCommit.value
    ? logs.value.length > 0
    : Boolean(commitMessage.value.trim() && status.value?.staged.length)
);

function groupLabel(group: ChangeGroup) {
  return t(`project.git.${group}` as MessageKey);
}

function clearRepositoryState() {
  repositorySelectionGate.invalidate();
  refreshGate.invalidate();
  status.value = null;
  branches.value = null;
  logs.value = [];
  stashes.value = [];
  selectedCommitHash.value = "";
  selectedCommit.value = null;
  selectedCommitFilePath.value = "";
  selectedFileKey.value = "";
}

function repositoryChangeCount(path: string) {
  const summary = repositorySummaries.value[path];
  return summary ? summary.staged + summary.unstaged + summary.untracked : 0;
}

/**
 * 拉取仓库列表每行的分支/领先落后/变更计数。列表不可见时直接跳过，避免为看不见的
 * 行反复跑 N 次 git status；失败只是少显示附加信息，不弹错打断用户。
 */
async function loadRepositorySummaries() {
  if (!showRepositoryList.value || !repositoriesOpen.value || !isDesktopRuntime()) return;
  const request = repositorySummaryGate.begin(props.projectRoot);
  const paths = repositories.value.map((repository) => repository.path);
  try {
    const summaries = await getRepositorySummaries(paths);
    if (!repositorySummaryGate.isCurrent(request)) return;
    const next: Record<string, GitRepositorySummary> = {};
    for (const summary of summaries) next[summary.path] = summary;
    repositorySummaries.value = next;
  } catch {
    // 概览是列表上的附加信息，读不到时仍要保证仓库可点可切换。
  }
}

function toggleRepositories() {
  repositoriesOpen.value = !repositoriesOpen.value;
  if (repositoriesOpen.value) void loadRepositorySummaries();
}

async function scanRepositories(root: string) {
  const request = repositoryScanGate.begin(root);
  repositoryRescanGate.invalidate();
  clearRepositoryState();
  repositories.value = [];
  repositorySummaries.value = {};
  repositorySummaryGate.invalidate();
  repoPath.value = "";
  if (!root || !isDesktopRuntime()) {
    loading.value = false;
    return;
  }

  loading.value = true;
  try {
    const [result, scopedSavedPath] = await Promise.all([
      listProjectRepositories(root),
      loadPersistedValue<string>(props.repositoryStorageKey),
    ]);
    let savedPath = scopedSavedPath;
    if (
      typeof savedPath !== "string" &&
      props.legacyRepositoryStorageKey &&
      props.legacyWorkspaceScopeId &&
      (await ownsLegacyWorkspaceSnapshot(
        props.legacyRepositoryStorageKey,
        props.legacyWorkspaceScopeId
      ))
    ) {
      savedPath = await loadPersistedValue<string>(props.legacyRepositoryStorageKey);
    }
    if (!repositoryScanGate.isCurrent(request)) return;
    repositories.value = result;
    void loadRepositorySummaries();
    const saved = typeof savedPath === "string" ? savedPath : "";
    const next = result.find((repository) => repository.path === saved) ?? result[0];
    if (next) await selectRepository(next.path);
  } catch (error) {
    if (repositoryScanGate.isCurrent(request)) ElMessage.error(String(error));
  } finally {
    if (repositoryScanGate.isCurrent(request)) loading.value = false;
  }
}

async function selectRepository(path: string) {
  repoPath.value = path;
  clearRepositoryState();
  const request = repositorySelectionGate.begin(path);
  await savePersistedValue(props.repositoryStorageKey, path);
  if (!repositorySelectionGate.isCurrent(request)) return;
  emit("repository-change", path);
  await refresh();
}

async function refresh(options?: { silent?: boolean }) {
  const targetPath = repoPath.value;
  if (!targetPath || !isDesktopRuntime()) return;
  const request = refreshGate.begin(targetPath);
  // 静默刷新（自动触发）不切换 loading，避免按钮/aria-busy 周期性闪烁。
  const background = options?.silent === true;
  if (!background) loading.value = true;
  try {
    const [nextStatus, nextBranches, nextLogs, nextStashes] = await Promise.all([
      invokeBackend<GitProjectStatus>("git_get_status", { repoPath: targetPath }),
      invokeBackend<GitBranches>("git_list_branches", { repoPath: targetPath }),
      invokeBackend<GitLogEntry[]>("git_get_log", { repoPath: targetPath, limit: 100 }),
      invokeBackend<GitStashEntry[]>("git_stash_list", { repoPath: targetPath }),
    ]);
    if (!refreshGate.isCurrent(request)) return;
    status.value = nextStatus;
    branches.value = nextBranches;
    logs.value = nextLogs;
    stashes.value = nextStashes;
    // 当前仓库的状态刚拿到手，就地更新它在列表里的那一行，省掉一次重复的 git status。
    if (showRepositoryList.value) {
      repositorySummaries.value = {
        ...repositorySummaries.value,
        [targetPath]: {
          path: targetPath,
          branch: nextStatus.branch,
          upstream: nextStatus.upstream,
          ahead: nextStatus.ahead,
          behind: nextStatus.behind,
          staged: nextStatus.staged.length,
          unstaged: nextStatus.unstaged.length,
          untracked: nextStatus.untracked.length,
        },
      };
    }
  } catch (error) {
    // 已失效请求的错误不属于当前仓库；后台自动刷新失败也不打断用户。
    if (!background && refreshGate.isCurrent(request)) ElMessage.error(String(error));
  } finally {
    if (refreshGate.isCurrent(request)) {
      loading.value = false;
      gitLastRefreshAt = Date.now();
    }
  }
}

/**
 * 手动刷新与定时刷新用这个入口：除当前仓库外，也顺带更新列表里其他仓库的概览。
 * 暂存/提交这类高频操作仍只调 refresh()，不为一次点击跑 N 个仓库的 git status。
 */
async function refreshAll(options?: { silent?: boolean }) {
  if (await rescanRepositoryList()) {
    await loadRepositorySummaries();
    return;
  }
  await refresh(options);
  await loadRepositorySummaries();
}

/**
 * Scans the project's repositories again so a deleted folder leaves the list
 * and a new one appears; when the selected repository is gone, selects the
 * first one left. True when it changed the selection (which refreshes it).
 * A failed scan keeps the list: the panel still works on what it has.
 */
async function rescanRepositoryList(): Promise<boolean> {
  const root = props.projectRoot;
  if (!root || !isDesktopRuntime() || loading.value) return false;
  const request = repositoryRescanGate.begin(root);
  let result: GitRepositoryEntry[];
  try {
    result = await listProjectRepositories(root);
  } catch {
    return false;
  }
  if (!repositoryRescanGate.isCurrent(request)) return false;
  const same =
    result.length === repositories.value.length &&
    result.every((repository, index) => repository.path === repositories.value[index]?.path);
  if (!same) repositories.value = result;
  if (!repoPath.value || result.some((repository) => repository.path === repoPath.value)) return false;
  const next = result[0];
  if (next) {
    await selectRepository(next.path);
  } else {
    clearRepositoryState();
    repoPath.value = "";
  }
  return true;
}

async function pullRepository() {
  if (!repoPath.value) return false;
  loading.value = true;
  try {
    const result = await invokeBackend<GitSyncResult>("git_pull", {
      repoPath: repoPath.value,
    });
    await refresh();
    ElMessage.success(result?.message || t("git.pull"));
    return true;
  } catch (error) {
    ElMessage.error(t("git.pullFailed", { reason: nativeErrorMessage(error) }));
    return false;
  } finally {
    loading.value = false;
  }
}

/** 只同步远程引用不碰工作区：远端新增/删除的分支要先 fetch 才会进分支列表。 */
async function fetchRepository() {
  if (!repoPath.value) return;
  loading.value = true;
  try {
    const result = await invokeBackend<GitSyncResult>("git_fetch", {
      repoPath: repoPath.value,
    });
    await refreshAll();
    ElMessage.success(result?.message || t("git.fetch"));
  } catch (error) {
    ElMessage.error(t("git.fetchFailed", { reason: nativeErrorMessage(error) }));
  } finally {
    loading.value = false;
  }
}

async function runGit(
  command: BackendCommand,
  args: Record<string, unknown> = {},
  targetPath = repoPath.value
) {
  if (!targetPath) return false;
  loading.value = true;
  try {
    await invokeBackend(command, { repoPath: targetPath, ...args });
    if (repoPath.value === targetPath) await refresh();
    return true;
  } catch (error) {
    ElMessage.error(String(error));
    return false;
  } finally {
    loading.value = false;
  }
}

async function confirmAction(message: string, title: string, action: () => Promise<boolean>) {
  try {
    await ElMessageBox.confirm(message, title, {
      type: "warning",
      confirmButtonText: t("common.continue"),
      cancelButtonText: t("common.cancel"),
    });
  } catch {
    return;
  }
  await action();
}

async function promptValue(message: string, title: string, initialValue = "") {
  try {
    const { value } = await ElMessageBox.prompt(message, title, {
      inputValue: initialValue,
      inputValidator: (input) => (input.trim() ? true : t("git.inputRequired")),
      confirmButtonText: t("common.confirm"),
      cancelButtonText: t("common.cancel"),
    });
    return value.trim();
  } catch {
    return "";
  }
}

type BranchOptionSelection = { kind: "local" | "remote"; name: string };

function branchOptionValue(branch: GitBranchEntry) {
  return `${branch.is_remote ? "remote" : "local"}:${branch.name}`;
}

function parseBranchOptionValue(value: string): BranchOptionSelection | null {
  if (value.startsWith("local:")) return { kind: "local", name: value.slice(6) };
  if (value.startsWith("remote:")) return { kind: "remote", name: value.slice(7) };
  return null;
}

async function checkoutBranch(optionValue: string) {
  const selection = parseBranchOptionValue(optionValue);
  if (!selection?.name) return;
  if (selection.kind === "local" && selection.name === branches.value?.current) return;
  const checkoutArgs = {
    branchName: selection.name,
    create: false,
    remoteBranch: selection.kind === "remote",
  };
  if (totalChanges.value === 0) {
    await runGit("git_checkout_branch", checkoutArgs);
    return;
  }

  try {
    await ElMessageBox.confirm(
      t("git.stashBeforeCheckoutConfirm", { branch: selection.name }),
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

  if (await runGit("git_checkout_branch", { ...checkoutArgs, stashChanges: true })) {
    ElMessage.success(t("git.stashBeforeCheckoutSuccess", { branch: selection.name }));
  }
}

async function createBranch() {
  const branchName = await promptValue(t("git.createBranchPrompt"), t("git.createBranchTitle"));
  if (branchName) await runGit("git_checkout_branch", { branchName, create: true });
}

async function renameBranch() {
  const oldName = branches.value?.current;
  if (!oldName || oldName === "HEAD") return;
  const newName = await promptValue(
    t("git.renameBranchPrompt"),
    t("git.renameBranchTitle"),
    oldName
  );
  if (newName && newName !== oldName) await runGit("git_rename_branch", { oldName, newName });
}

async function stageFile(file: GitFileEntry) {
  await runGit("git_stage_file", { relativePath: file.path });
}

async function unstageFile(file: GitFileEntry) {
  await runGit("git_unstage_file", { relativePath: file.path });
}

async function setFileStaged(file: GitFileEntry, group: ChangeGroup, nextValue: boolean) {
  if (group === "staged" && !nextValue) {
    await unstageFile(file);
    return;
  }
  if (group !== "staged" && nextValue) await stageFile(file);
}

async function setGroupStaged(group: ChangeGroup, nextValue: boolean) {
  if (group === "staged" && !nextValue) {
    await runGit("git_unstage_all");
    return;
  }
  if (group !== "staged" && nextValue) await runGit("git_stage_all");
}

async function discardFile(file: GitFileEntry) {
  await confirmAction(t("git.discardFileConfirm", { path: file.path }), t("git.discardTitle"), () =>
    runGit("git_discard_file", { relativePath: file.path })
  );
}

async function discardAll() {
  await confirmAction(t("git.discardAllConfirm"), t("git.discardAllTitle"), () =>
    runGit("git_discard_all")
  );
}

// 分组级放弃：未暂存分组只还原已跟踪文件，未跟踪分组只清理新文件，
// 互不影响，避免复用 git_discard_all 一把梭。
async function discardUnstagedAll() {
  const count = status.value?.unstaged.length ?? 0;
  if (!count) return;
  await confirmAction(t("git.discardUnstagedAllConfirm", { count }), t("git.discardUnstagedAll"), () =>
    runGit("git_discard_unstaged")
  );
}

async function cleanUntrackedAll() {
  const count = status.value?.untracked.length ?? 0;
  if (!count) return;
  await confirmAction(t("git.cleanUntrackedAllConfirm", { count }), t("git.cleanUntrackedAll"), () =>
    runGit("git_clean_untracked")
  );
}

async function pushStash() {
  changeTab.value = "stash";
  await nextTick();
  stashPanel.value?.openCreate();
}

async function popStash(stash: GitStashEntry) {
  await confirmAction(
    t("git.popStashConfirm", { message: stash.message }),
    t("git.popStashTitle"),
    () => runGit("git_stash_pop", { stashIndex: stash.index })
  );
}

async function dropStash(stash: GitStashEntry) {
  await confirmAction(
    t("git.dropStashConfirm", { message: stash.message }),
    t("git.dropStashTitle"),
    () => runGit("git_stash_drop", { stashIndex: stash.index })
  );
}

async function openCommitDetail(entry: GitLogEntry) {
  selectedCommitHash.value = entry.hash;
  selectedCommitFilePath.value = "";
  loading.value = true;
  try {
    selectedCommit.value = await getGitCommitDetail(repoPath.value, entry.hash);
    emit("open-commit", { repoPath: repoPath.value, detail: selectedCommit.value });
  } catch (error) {
    selectedCommit.value = null;
    ElMessage.error(String(error));
  } finally {
    loading.value = false;
  }
}

function openCommitFile(file: GitCommitFile) {
  if (!repoPath.value || !selectedCommit.value) return;
  selectedCommitFilePath.value = file.path;
  emit("open-commit-file", {
    repoPath: repoPath.value,
    commitHash: selectedCommit.value.commit.hash,
    file,
  });
}

async function handleHistoryAction(payload: {
  action: "cherry-pick" | "revert";
  entry: GitLogEntry;
}) {
  const { action, entry } = payload;
  if (action === "cherry-pick") {
    await confirmAction(
      t("git.cherryPickConfirm", { hash: entry.short_hash }),
      t("git.cherryPickTitle"),
      () => runGit("git_cherry_pick", { commitHash: entry.hash })
    );
    return;
  }
  await confirmAction(
    t("git.revertConfirm", { hash: entry.short_hash }),
    t("git.revertTitle"),
    () => runGit("git_revert", { commitHash: entry.hash })
  );
}

async function submitCommit(targetPath = repoPath.value) {
  if (!targetPath) return false;
  const draft = repositoryCommitDraft(targetPath);
  const message = draft.message.trim();
  const command = draft.amend ? "git_commit_amend" : "git_commit";
  if (!draft.amend && !message) return false;
  const committed = await runGit(command, { message }, targetPath);
  if (committed) {
    draft.message = "";
    draft.amend = false;
  }
  return committed;
}

async function commit() {
  if (submitting.value) return;
  const targetPath = repoPath.value;
  if (!targetPath) return;
  const draft = repositoryCommitDraft(targetPath);
  submitting.value = true;
  try {
    if (!draft.amend) {
      await submitCommit(targetPath);
      return;
    }
    await confirmAction(t("git.amendConfirm"), t("git.amendTitle"), () =>
      submitCommit(targetPath)
    );
  } finally {
    submitting.value = false;
  }
}

async function pushAfterCommit(targetPath = repoPath.value) {
  if (!targetPath) return;
  loading.value = true;
  try {
    await invokeBackend("git_push", { repoPath: targetPath });
    if (repoPath.value === targetPath) await refresh();
    const title = t("git.commitAndPushSuccess");
    ElMessage.success(title);
    notifyApp({ source: "git", level: "success", title });
  } catch (error) {
    ElMessage.warning(t("git.commitPushFailed", { reason: String(error) }));
    notifyApp({ source: "git", level: "error", title: t("notifications.git.pushFailed") });
  } finally {
    loading.value = false;
  }
}

async function commitAndPush() {
  if (submitting.value) return;
  const targetPath = repoPath.value;
  if (!targetPath) return;
  const draft = repositoryCommitDraft(targetPath);
  submitting.value = true;
  try {
    if (!draft.amend) {
      if (await submitCommit(targetPath)) await pushAfterCommit(targetPath);
      return;
    }
    await confirmAction(t("git.amendConfirm"), t("git.amendTitle"), async () => {
      if (await submitCommit(targetPath)) await pushAfterCommit(targetPath);
      return true;
    });
  } finally {
    submitting.value = false;
  }
}

async function undoLastCommit() {
  if (!logs.value.length) return;
  await confirmAction(t("git.undoLastCommitConfirm"), t("git.undoLastCommitTitle"), async () => {
    const undone = await runGit("git_undo_last_commit");
    if (undone) ElMessage.success(t("git.undoLastCommitSuccess"));
    return undone;
  });
}

function activeAiProfile() {
  const stored = readSettingsSnapshot<StoredSettings & Record<string, unknown>>();
  return (
    stored.aiProfiles?.find((item) => item.id === stored.activeAiProfileId) ??
    stored.aiProfiles?.[0]
  );
}

function stringList(value: unknown) {
  return Array.isArray(value) ? value.filter((item): item is string => typeof item === "string") : [];
}

/**
 * Uses the model picked in the AI assistant (with that profile's failover chain),
 * like the chat does. The Settings default is only the fallback: it may point to a
 * profile the user no longer chats with, which left generation waiting on it.
 */
function commitMessageModelPlan() {
  const stored = readSettingsSnapshot<StoredSettings & Record<string, unknown>>();
  const resolved = resolveAiModelSelection({
    profiles: stored.aiProfiles ?? [],
    activeProfileId: stored.activeAiProfileId,
    selectedModel: aiAssistant.selectedModel,
  });
  const profile = resolved?.profile ?? activeAiProfile();
  const config = currentAiConfig(profile);
  const models = buildAiModelAttemptOrder({
    selectedModel: resolved?.selection.model,
    profileDefaultModel: config.model,
    configuredModels: configuredAiModels(config.model, stringList(profile?.commonModels)),
    fallbackEnabled: profile?.failoverEnabled === true,
    fallbackModels: stringList(profile?.failoverModels),
  });
  return { profile, config: { ...config, model: models[0] ?? config.model }, models };
}

function currentAiConfig(profile = activeAiProfile()) {
  const apiFormat = profile?.apiFormat;
  return {
    baseUrl: typeof profile?.baseUrl === "string" ? profile.baseUrl : "",
    apiKey: typeof profile?.apiKey === "string" ? profile.apiKey : "",
    model: typeof profile?.model === "string" ? profile.model : "",
    apiFormat: normalizeAiApiFormat(apiFormat),
    anthropicAuth: normalizeAnthropicAuth(profile?.anthropicAuth),
    contextWindow: typeof profile?.contextWindow === "number" ? profile.contextWindow : undefined,
    maxInputTokens:
      typeof profile?.maxInputTokens === "number" ? profile.maxInputTokens : undefined,
    maxOutputTokens:
      typeof profile?.maxOutputTokens === "number" ? profile.maxOutputTokens : undefined,
    openaiStreamUsage: profile?.openaiStreamUsage ?? "auto",
    noToolModels: Array.isArray(profile?.noToolModels) ? profile.noToolModels : [],
  };
}

function cancelCommitMessageGeneration(targetPath = repoPath.value) {
  if (!targetPath) return;
  commitMessageAbortControllers.get(targetPath)?.abort();
  commitMessageAbortControllers.delete(targetPath);
  const draft = repositoryCommitDrafts[targetPath];
  if (!draft?.generating) return;
  draft.generation = ++commitMessageGeneration;
  draft.generating = false;
  draft.thinkingSince = 0;
}

function cancelAllCommitMessageGenerations() {
  for (const targetPath of Object.keys(repositoryCommitDrafts)) {
    cancelCommitMessageGeneration(targetPath);
  }
}

function onPluginCommitMessageStart() {
  const targetPath = repoPath.value;
  if (!targetPath) return;
  const draft = repositoryCommitDraft(targetPath);
  const generation = ++commitMessageGeneration;
  draft.generating = true;
  draft.generation = generation;
  pluginCommitRun.value = { targetPath, generation };
}

/** Fills the draft only when it is unchanged since the click; never commits. */
function onPluginCommitMessageFinish(result: { message?: string; draftAtStart: string }) {
  const run = pluginCommitRun.value;
  pluginCommitRun.value = null;
  const draft = run && repositoryCommitDrafts[run.targetPath];
  if (!run || !draft || draft.generation !== run.generation) return;
  draft.generating = false;
  if (result.message === undefined) return;
  if (shouldApplyGeneratedMessage(result.draftAtStart, draft.message)) draft.message = result.message;
  else ElMessage.warning(t("git.pluginGenerateDraftChanged"));
}

function onPluginCommitMessageCancel() {
  const run = pluginCommitRun.value;
  pluginCommitRun.value = null;
  if (run) cancelCommitMessageGeneration(run.targetPath);
}

async function generateCommitMessage() {
  const targetPath = repoPath.value;
  if (!targetPath || pluginGeneratingCommitMessage.value) return;
  const draft = repositoryCommitDraft(targetPath);
  if (draft.generating) {
    cancelCommitMessageGeneration(targetPath);
    return;
  }
  if (!status.value?.staged.length) {
    ElMessage.warning(t("git.aiGenerateRequiresStaged"));
    return;
  }

  const generation = ++commitMessageGeneration;
  const controller = new AbortController();
  commitMessageAbortControllers.set(targetPath, controller);
  draft.generating = true;
  draft.generation = generation;
  draft.thinkingSince = 0;
  let receivedText = false;
  let generatingModel = "";
  const originalMessage = draft.message;
  const generationTimeoutId = setTimeout(() => controller.abort(), COMMIT_MESSAGE_GENERATION_TIMEOUT_MS);

  try {
    // Cancel and the overall timeout also end a run whose diff or settings never arrive.
    const [patch] = await runWithAbort(controller.signal, () => Promise.all([
      invokeBackend<string>("git_get_commit_message_context", { repoPath: targetPath }),
      hydrateSettingsSnapshot(),
    ]));
    if (controller.signal.aborted || draft.generation !== generation) return;
    const plan = commitMessageModelPlan();
    const profile = plan.profile;
    generatingModel = plan.config.model;
    const auth = validateAiProviderAuth({
      provider: profile?.provider,
      baseUrl: typeof profile?.baseUrl === "string" ? profile.baseUrl : "",
      apiKey: typeof profile?.apiKey === "string" ? profile.apiKey : "",
    });
    if (!auth.valid) throw new Error(t(aiProviderAuthMessageKey(auth)));

    const message = await generateAiCommitMessage({
      config: plan.config,
      models: plan.models,
      patch,
      signal: controller.signal,
      idleTimeoutMs: COMMIT_MESSAGE_IDLE_TIMEOUT_MS,
      onReasoning: () => {
        if (draft.generation !== generation || receivedText || draft.thinkingSince) return;
        draft.thinkingSince = Date.now();
      },
      onDelta: (delta) => {
        if (draft.generation !== generation || controller.signal.aborted) return;
        if (!receivedText) {
          receivedText = true;
          draft.thinkingSince = 0;
          draft.message = "";
        }
        draft.message += delta;
      },
    });
    if (draft.generation === generation) draft.message = message;
  } catch (error) {
    if (draft.generation !== generation) return;
    // Silence (no data for the idle window) or the overall limit; a manual cancel
    // bumped the generation and returned above.
    const idle = isAiStreamIdleTimeoutError(error);
    if (idle || controller.signal.aborted) {
      draft.message = originalMessage;
      if (repoPath.value === targetPath) {
        ElMessage.warning(idle
          ? t("git.aiGenerateTimeout", { model: generatingModel || "AI" })
          : t("git.aiGenerateTooLong", { model: generatingModel || "AI", minutes: COMMIT_MESSAGE_GENERATION_TIMEOUT_MS / 60_000 }));
      }
      return;
    }
    if (draft.generation === generation && repoPath.value === targetPath) {
      ElMessage.error(t("git.aiGenerateFailed", { reason: nativeErrorMessage(error) }));
    }
  } finally {
    clearTimeout(generationTimeoutId);
    if (commitMessageAbortControllers.get(targetPath) === controller) {
      commitMessageAbortControllers.delete(targetPath);
    }
    if (draft.generation === generation) {
      draft.generating = false;
      draft.thinkingSince = 0;
    }
  }
}

async function runQuickAction(action: GitQuickAction) {
  switch (action) {
    case "refresh":
      await refreshAll();
      return;
    case "fetch":
      await fetchRepository();
      return;
    case "pull":
      await pullRepository();
      return;
    case "push":
      await runGit("git_push");
      return;
    default:
      await handleActionCommand(action);
  }
}

async function handleActionCommand(command: string | number) {
  switch (String(command)) {
    case "create-branch":
      await createBranch();
      break;
    case "rename-branch":
      await renameBranch();
      break;
    case "undo-last-commit":
      await undoLastCommit();
      break;
    case "manage-branches":
      emit("extended-action", "branches");
      break;
    case "manage-tags":
      emit("extended-action", "tags");
      break;
    case "manage-shelf":
      changeTab.value = "shelf";
      break;
    case "manage-stash":
      changeTab.value = "stash";
      break;
    case "show-graph":
      emit("extended-action", "graph");
      break;
    case "manage-worktrees":
      emit("extended-action", "worktrees");
      break;
    case "interactive-rebase":
      emit("extended-action", "rebase");
      break;
    case "stage-all":
      await runGit("git_stage_all");
      break;
    case "unstage-all":
      await runGit("git_unstage_all");
      break;
    case "stash":
      await pushStash();
      break;
    case "discard-all":
      await discardAll();
      break;
  }
}

function fileName(path: string) {
  const segments = path.split("/").filter(Boolean);
  return segments[segments.length - 1] ?? path;
}

function fileDirectory(path: string) {
  const segments = path.split("/").filter(Boolean);
  return segments.length > 1 ? segments.slice(0, -1).join("/") : "";
}

function isDeletedFile(file: GitFileEntry) {
  // 后端状态为单词（deleted/modified/...）或 git 单字符码（D/ D）。
  return file.status.trim().toLowerCase().startsWith("d");
}

function openFile(entry: GitFileEntry, group: ChangeGroup) {
  if (!repoPath.value) return;
  selectedFileKey.value = `${group}:${entry.path}`;
  emit("open-file", { repoPath: repoPath.value, entry, group });
}

function openFileContextMenu(event: MouseEvent, entry: GitFileEntry, group: ChangeGroup) {
  selectedFileKey.value = `${group}:${entry.path}`;
  fileContextMenu.open = false;
  fileContextMenu.x = Math.max(8, Math.min(event.clientX, window.innerWidth - 246));
  fileContextMenu.y = Math.max(8, Math.min(event.clientY, window.innerHeight - 260));
  fileContextMenu.entry = entry;
  fileContextMenu.group = group;
  fileContextMenu.open = true;
}

function closeFileContextMenu() {
  fileContextMenu.open = false;
}

function openContextChanges() {
  const entry = fileContextMenu.entry;
  if (!entry) return;
  closeFileContextMenu();
  openFile(entry, fileContextMenu.group);
}

function openContextWorktreeFile() {
  const entry = fileContextMenu.entry;
  if (!entry || !repoPath.value || entry.status === "deleted") return;
  closeFileContextMenu();
  emit("open-worktree-file", { repoPath: repoPath.value, entry });
}

async function stageContextFile() {
  const entry = fileContextMenu.entry;
  if (!entry) return;
  closeFileContextMenu();
  await stageFile(entry);
}

async function unstageContextFile() {
  const entry = fileContextMenu.entry;
  if (!entry) return;
  closeFileContextMenu();
  await unstageFile(entry);
}

async function discardContextFile() {
  const entry = fileContextMenu.entry;
  if (!entry) return;
  closeFileContextMenu();
  await discardFile(entry);
}

function entryAbsolutePath(entry: GitFileEntry) {
  return `${repoPath.value.replace(/[\\/]+$/, "")}/${entry.path.replace(/^[\\/]+/, "")}`;
}

async function revealContextFile() {
  const entry = fileContextMenu.entry;
  if (!entry || !repoPath.value || entry.status === "deleted") return;
  closeFileContextMenu();
  const path = entryAbsolutePath(entry);
  try {
    await invokeBackend<void>("local_fs_show_in_file_manager", { path, select: true });
  } catch (error) {
    ElMessage.error(nativeErrorMessage(error));
  }
}

async function copyContextRelativePath() {
  const entry = fileContextMenu.entry;
  if (!entry) return;
  closeFileContextMenu();
  try {
    await copyText(entry.path);
    ElMessage.success(t("git.fileContext.pathCopied"));
  } catch (error) {
    ElMessage.error(nativeErrorMessage(error));
  }
}

// Plugin menu items (contributes.menus): git/changes/context for the file
// context menu, git/commit/context for the history row "⋯" menu that is open.
function pluginProjectName() {
  return props.projectRoot.split(/[\\/]/).filter(Boolean).pop() ?? props.projectRoot;
}

const changePluginMenu = usePluginMenuTarget("git/changes/context", () => {
  const entry = fileContextMenu.entry;
  if (!fileContextMenu.open || !entry || !repoPath.value) return null;
  return {
    projectName: pluginProjectName(),
    repositoryPath: repoPath.value,
    relativePath: entry.path.replace(/\\/g, "/"),
    ...(entry.old_path ? { oldRelativePath: entry.old_path.replace(/\\/g, "/") } : {}),
    status: entry.status,
    group: fileContextMenu.group,
    path: entryAbsolutePath(entry),
  };
});

const openHistoryMenuEntry = ref<GitLogEntry | null>(null);
const commitPluginMenu = usePluginMenuTarget("git/commit/context", () => {
  const entry = openHistoryMenuEntry.value;
  if (!entry || !repoPath.value) return null;
  return {
    projectName: pluginProjectName(),
    repositoryPath: repoPath.value,
    hash: entry.hash,
    shortHash: entry.short_hash,
    subject: entry.message,
    author: entry.author,
    time: entry.time,
    parents: entry.parents ?? [],
    refs: entry.refs ?? [],
  };
});

watch(
  () => props.projectRoot,
  (root) => void scanRepositories(root),
  { immediate: true }
);

watch(totalChanges, (count) => emit("change-count", count), { immediate: true });

// 自动刷新：外部改动（编辑器保存、终端命令、其他工具）不会触发事件，
// 用低频轮询 + 窗口回焦即时刷新保持文件列表同步；失败静默，不打断用户。
const GIT_AUTO_REFRESH_INTERVAL_MS = 20_000;
let gitAutoRefreshTimer: number | undefined;
let gitLastRefreshAt = 0;
let gitAutoRefreshInFlight = false;

function autoRefreshGitPanel() {
  if (!repoPath.value || loading.value || submitting.value || gitAutoRefreshInFlight) return;
  // 后台时不轮询；切回可见时由 visibilitychange 补一次（仍受节流闸门约束）。
  if (typeof document !== "undefined" && document.hidden) return;
  if (Date.now() - gitLastRefreshAt < GIT_AUTO_REFRESH_INTERVAL_MS - 1000) return;
  gitAutoRefreshInFlight = true;
  void refreshAll({ silent: true }).finally(() => {
    gitAutoRefreshInFlight = false;
  });
}

function handleGitPanelWindowFocus() {
  // 从其他应用切回时尝试同步一次；近期刚刷新过则由节流闸门跳过。
  autoRefreshGitPanel();
}

function handleGitPanelVisibilityChange() {
  if (!document.hidden) autoRefreshGitPanel();
}

function startGitAutoRefresh() {
  if (typeof window === "undefined" || gitAutoRefreshTimer !== undefined) return;
  gitAutoRefreshTimer = window.setInterval(autoRefreshGitPanel, GIT_AUTO_REFRESH_INTERVAL_MS);
  window.addEventListener("focus", handleGitPanelWindowFocus);
  document.addEventListener("visibilitychange", handleGitPanelVisibilityChange);
}

function stopGitAutoRefresh() {
  if (gitAutoRefreshTimer !== undefined) {
    window.clearInterval(gitAutoRefreshTimer);
    gitAutoRefreshTimer = undefined;
  }
  window.removeEventListener("focus", handleGitPanelWindowFocus);
  document.removeEventListener("visibilitychange", handleGitPanelVisibilityChange);
}

function observeCommandBar(element: HTMLElement | null) {
  commandBarResizeObserver?.disconnect();
  commandBarResizeObserver = undefined;
  commandBarWidth.value = element?.getBoundingClientRect().width || Number.POSITIVE_INFINITY;
  if (!element || typeof ResizeObserver === "undefined") return;
  commandBarResizeObserver = new ResizeObserver(([entry]) => {
    commandBarWidth.value = entry?.contentRect.width || element.getBoundingClientRect().width;
  });
  commandBarResizeObserver.observe(element);
}

watch(commandBarElement, observeCommandBar, { flush: "post" });

onMounted(startGitAutoRefresh);
onActivated(startGitAutoRefresh);
onDeactivated(() => {
  stopGitAutoRefresh();
  cancelAllCommitMessageGenerations();
  pluginCommitRun.value = null;
});
onUnmounted(() => {
  if (commitThinkingTicker) clearInterval(commitThinkingTicker);
  stopGitAutoRefresh();
  cancelAllCommitMessageGenerations();
  commandBarResizeObserver?.disconnect();
});
watch(repoPath, (_current, previous) => {
  cancelCommitMessageGeneration(previous);
  if (pluginCommitRun.value?.targetPath === previous) pluginCommitRun.value = null;
});
// The button unmounts when its last provider goes away mid-run; never leave the draft generating.
watch(
  () => scmProviders.value.length,
  (count) => {
    if (!count) onPluginCommitMessageCancel();
  },
);

defineExpose({ refresh, selectRepository, runQuickAction, quickActionState });
</script>

<template>
  <section class="project-git-console project-git-console--ide" :aria-busy="loading">
    <div v-if="showContextSwitchers" class="git-context-card">
      <div class="git-context-switchers">
        <!-- 用 div 而非 label：label 包裹控件时点击任意子元素（含标签文字）都会激活下拉，
             这里标签文字只是说明性文本，不应触发筛选。 -->
        <div class="git-context-switcher">
          <span>{{ t("git.project") }}</span>
          <el-select
            filterable
            :model-value="projectRoot"
            :placeholder="t('git.projectPlaceholder')"
            size="small"
            @change="emit('select-project', $event)"
          >
            <el-option
              v-for="item in projects"
              :key="item.root"
              :label="item.name"
              :value="item.root"
            />
          </el-select>
        </div>
      </div>
    </div>

    <!-- 多仓库工程平铺成列表而不是下拉框：一眼能看到每个仓库在哪个分支、有多少改动，
         点整行即切换（与下方文件行同一套 role/tabindex/键盘约定）。 -->
    <section v-if="showRepositoryList" class="git-repository-list">
      <button
        type="button"
        class="git-repository-list__toggle"
        :aria-expanded="repositoriesOpen"
        @click="toggleRepositories"
      >
        <AppChevron :direction="repositoriesOpen ? 'down' : 'right'" />
        <span>{{ t("git.repositories") }}</span>
        <b>{{ repositories.length }}</b>
      </button>
      <el-scrollbar v-if="repositoriesOpen" class="git-repository-list__scroll">
        <div
          v-for="repository in repositories"
          :key="repository.path"
          class="git-repository-row"
          :class="{ 'is-active': repository.path === repoPath }"
          role="button"
          tabindex="0"
          :aria-current="repository.path === repoPath"
          :title="repository.relative_path || repository.path"
          @click="selectRepository(repository.path)"
          @keydown.enter.prevent="selectRepository(repository.path)"
          @keydown.space.prevent="selectRepository(repository.path)"
        >
          <span class="git-repository-row__name">{{ repository.name }}</span>
          <span
            v-if="repositorySummaries[repository.path]"
            class="git-repository-row__branch"
            :title="t('git.branch')"
          >
            {{ repositorySummaries[repository.path].branch }}
          </span>
          <span
            v-if="
              repositorySummaries[repository.path]?.ahead ||
              repositorySummaries[repository.path]?.behind
            "
            class="git-repository-row__sync"
            :title="
              t('git.repositorySync', {
                ahead: repositorySummaries[repository.path].ahead,
                behind: repositorySummaries[repository.path].behind,
              })
            "
          >
            ↓{{ repositorySummaries[repository.path].behind }} ↑{{
              repositorySummaries[repository.path].ahead
            }}
          </span>
          <b
            v-if="repositoryChangeCount(repository.path)"
            class="git-repository-row__changes"
            :title="t('git.repositoryChanges', { count: repositoryChangeCount(repository.path) })"
          >
            {{ repositoryChangeCount(repository.path) }}
          </b>
        </div>
      </el-scrollbar>
    </section>

    <template v-if="repoPath">
      <div ref="commandBarElement" class="git-command-bar git-command-bar--ide">
        <div class="git-command-bar__branch">
          <!-- 分支标签与上方“工程/仓库”切换器的标签列同宽对齐；
               分支区域保持固定宽度，仓库操作区占据剩余空间并右对齐。 -->
          <span class="git-branch-label">{{ t("git.branch") }}</span>
          <el-select
            filterable
            :model-value="selectedBranchValue"
            class="git-branch-select"
            size="small"
            @change="checkoutBranch"
          >
            <el-option-group v-if="localBranches.length" :label="t('git.localBranches')">
              <el-option
                v-for="branch in localBranches"
                :key="branchOptionValue(branch)"
                :label="branch.name"
                :value="branchOptionValue(branch)"
              />
            </el-option-group>
            <el-option-group v-if="remoteBranches.length" :label="t('git.remoteBranches')">
              <el-option
                v-for="branch in remoteBranches"
                :key="branchOptionValue(branch)"
                :label="branch.name"
                :value="branchOptionValue(branch)"
              />
            </el-option-group>
          </el-select>
        </div>

        <div class="git-command-bar__actions" :aria-label="t('git.actions')">
          <el-button
            text
            :icon="Refresh"
            :loading="loading"
            :title="t('common.refresh')"
            :aria-label="t('common.refresh')"
            @click="refreshAll()"
          />
          <el-button
            text
            :icon="Plus"
            :disabled="!(status?.unstaged.length || status?.untracked.length) || loading"
            :title="t('git.stageAll')"
            :aria-label="t('git.stageAll')"
            @click="runGit('git_stage_all')"
          />
          <el-button
            v-if="showToolbarUnstage"
            text
            :icon="Remove"
            :disabled="!status?.staged.length || loading"
            :title="t('git.unstageAll')"
            :aria-label="t('git.unstageAll')"
            @click="runGit('git_unstage_all')"
          />
          <span class="git-command-bar__divider" aria-hidden="true" />
          <el-button
            text
            :icon="Download"
            :disabled="loading"
            :title="t('git.fetch')"
            :aria-label="t('git.fetch')"
            @click="fetchRepository"
          />
          <el-button
            text
            :icon="ArrowDown"
            :disabled="loading"
            :title="t('git.pull')"
            :aria-label="t('git.pull')"
            @click="pullRepository"
          />
          <el-button
            text
            :icon="ArrowUp"
            :disabled="loading"
            :title="t('git.push')"
            :aria-label="t('git.push')"
            @click="runGit('git_push')"
          />
          <template v-if="extendedActions">
            <span
              v-if="showToolbarBranches || showToolbarGraph || showToolbarWorktrees"
              class="git-command-bar__divider"
              aria-hidden="true"
            />
            <el-button
              v-if="showToolbarBranches"
              data-git-inspector="branches"
              text
              :icon="Share"
              :title="t('git.inspector.branches')"
              :aria-label="t('git.inspector.branches')"
              @click="handleActionCommand('manage-branches')"
            />
            <el-button
              v-if="showToolbarGraph"
              data-git-inspector="graph"
              text
              :icon="Guide"
              :title="t('git.inspector.graph')"
              :aria-label="t('git.inspector.graph')"
              @click="handleActionCommand('show-graph')"
            />
            <el-button
              v-if="showToolbarWorktrees"
              class="git-command-bar__worktrees"
              data-git-inspector="worktrees"
              text
              :icon="FolderOpened"
              :title="t('git.inspector.worktrees')"
              :aria-label="t('git.inspector.worktrees')"
              @click="handleActionCommand('manage-worktrees')"
            />
          </template>
          <el-dropdown
            trigger="click"
            placement="bottom-end"
            popper-class="project-git-actions-popper"
            @command="handleActionCommand"
          >
            <el-button
              text
              :icon="MoreFilled"
              :title="t('git.actions')"
              :aria-label="t('git.actions')"
            />
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item
                  v-if="!showToolbarUnstage"
                  command="unstage-all"
                  :disabled="!status?.staged.length || loading"
                >
                  <el-icon><Remove /></el-icon>{{ t("git.unstageAll") }}
                </el-dropdown-item>
                <el-dropdown-item
                  v-if="extendedActions && !showToolbarBranches"
                  command="manage-branches"
                >
                  <el-icon><Share /></el-icon>{{ t("git.inspector.branches") }}
                </el-dropdown-item>
                <el-dropdown-item
                  v-if="extendedActions && !showToolbarGraph"
                  command="show-graph"
                >
                  <el-icon><Guide /></el-icon>{{ t("git.inspector.graph") }}
                </el-dropdown-item>
                <el-dropdown-item
                  v-if="extendedActions && !showToolbarWorktrees"
                  command="manage-worktrees"
                >
                  <el-icon><FolderOpened /></el-icon>{{ t("git.inspector.worktrees") }}
                </el-dropdown-item>
                <el-dropdown-item command="create-branch">
                  <el-icon><CirclePlus /></el-icon>{{ t("git.createBranchTitle") }}
                </el-dropdown-item>
                <el-dropdown-item
                  command="rename-branch"
                  :disabled="!branches?.current || branches.current === 'HEAD'"
                >
                  <el-icon><EditPen /></el-icon>{{ t("git.renameBranchTitle") }}
                </el-dropdown-item>
                <el-dropdown-item v-if="extendedActions" command="manage-tags">
                  <el-icon><EditPen /></el-icon>{{ t("git.inspector.tags") }}
                </el-dropdown-item>
                <el-dropdown-item command="undo-last-commit" :disabled="!logs.length" divided>
                  <el-icon><Refresh /></el-icon>{{ t("git.undoLastCommit") }}
                </el-dropdown-item>
                <el-dropdown-item v-if="extendedActions" command="interactive-rebase">
                  <el-icon><Operation /></el-icon>{{ t("git.inspector.rebase") }}
                </el-dropdown-item>
                <el-dropdown-item
                  command="discard-all"
                  class="is-danger"
                  :disabled="!totalChanges"
                  divided
                >
                  <el-icon><Delete /></el-icon>{{ t("git.discardAllTitle") }}
                </el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
        </div>
      </div>

      <div v-if="stashes[0]" class="git-stash-current git-stash-current--compact">
        <el-icon><Operation /></el-icon>
        <span :title="stashes[0].message">{{ stashes[0].message }}</span>
        <b v-if="stashes.length > 1">+{{ stashes.length - 1 }}</b>
        <el-button text native-type="button" @click="popStash(stashes[0])">
          {{ t("project.git.restoreStash") }}
        </el-button>
        <el-button
          text
          native-type="button"
          class="git-stash-drop"
          :title="t('git.dropStashTitle')"
          @click="dropStash(stashes[0])"
        >
          <el-icon><Delete /></el-icon>
        </el-button>
      </div>

      <div class="git-change-tabs git-change-tabs--ide" role="tablist">
        <el-segmented v-model="changeTab" size="small" :options="changeTabOptions" />
        <div class="git-change-tabs__actions">
          <span v-if="changeTab === 'changes' && totalChanges" class="git-change-total">{{
            totalChanges
          }}</span>
          <el-button
            text
            native-type="button"
            :icon="Operation"
            :disabled="loading || !repoPath"
            :title="t('git.stashTitle')"
            :aria-label="t('git.stashTitle')"
            @click="pushStash"
          />
        </div>
      </div>

      <el-scrollbar
        v-if="changeTab === 'changes'"
        ref="changeScroller"
        class="git-change-scroll project-git-console__scroll"
      >
        <section
          v-for="group in ['staged', 'unstaged', 'untracked'] as ChangeGroup[]"
          :key="group"
          class="git-change-group"
          :class="{ 'is-empty': !status?.[group].length }"
        >
          <div class="git-change-group__header">
            <el-checkbox
              class="git-change-group__checkbox"
              :model-value="group === 'staged' && Boolean(status?.staged.length)"
              :disabled="!status?.[group].length || loading"
              :title="group === 'staged' ? t('git.unstageAll') : t('git.stageAll')"
              :aria-label="group === 'staged' ? t('git.unstageAll') : t('git.stageAll')"
              @click.stop
              @change="setGroupStaged(group, $event)"
            />
            <button
              type="button"
              class="git-change-group__toggle"
              @click="groupOpen[group] = !groupOpen[group]"
            >
              <AppChevron :direction="groupOpen[group] ? 'down' : 'right'" />
              <span>{{ groupLabel(group) }}</span>
              <b>{{ status?.[group].length ?? 0 }}</b>
            </button>
            <div class="git-change-group__actions">
              <el-button
                v-if="group === 'staged'"
                text
                native-type="button"
                :disabled="!status?.staged.length || loading"
                :title="t('git.unstageAll')"
                :aria-label="t('git.unstageAll')"
                @click="runGit('git_unstage_all')"
              >
                <el-icon><Remove /></el-icon>
              </el-button>
              <template v-else>
                <el-button
                  text
                  native-type="button"
                  :disabled="!(status?.unstaged.length || status?.untracked.length) || loading"
                  :title="t('git.stageAll')"
                  :aria-label="t('git.stageAll')"
                  @click="runGit('git_stage_all')"
                >
                  <el-icon><Plus /></el-icon>
                </el-button>
                <el-button
                  text
                  native-type="button"
                  class="git-group-discard"
                  :disabled="!status?.[group].length || loading"
                  :title="group === 'unstaged' ? t('git.discardUnstagedAll') : t('git.cleanUntrackedAll')"
                  :aria-label="group === 'unstaged' ? t('git.discardUnstagedAll') : t('git.cleanUntrackedAll')"
                  @click="group === 'unstaged' ? discardUnstagedAll() : cleanUntrackedAll()"
                >
                  <el-icon><RefreshLeft /></el-icon>
                </el-button>
              </template>
            </div>
          </div>
          <div v-if="groupOpen[group]" v-bind="{ [ROW_GROUP_ATTRIBUTE]: group }">
            <div v-if="groupWindows[group].paddingTop" aria-hidden="true" :style="{ height: `${groupWindows[group].paddingTop}px` }" />
            <div
              v-for="file in groupWindows[group].rows"
              :key="`${group}:${file.path}`"
              class="git-file-row"
              :class="{
                'is-active': selectedFileKey === `${group}:${file.path}`,
                'is-deleted': isDeletedFile(file),
              }"
              role="button"
              tabindex="0"
              :title="file.path"
              @click="openFile(file, group)"
              @contextmenu.prevent="openFileContextMenu($event, file, group)"
              @keydown.enter.prevent="openFile(file, group)"
              @keydown.space.prevent="openFile(file, group)"
              @mouseenter="hoveredFileKey = `${group}:${file.path}`"
              @mouseleave="onFileRowMouseLeave(`${group}:${file.path}`)"
              @focusin="focusedFileKey = `${group}:${file.path}`"
              @focusout="onFileRowFocusOut($event, `${group}:${file.path}`)"
            >
              <el-checkbox
                class="git-file-row__checkbox"
                :model-value="group === 'staged'"
                :disabled="loading"
                :title="group === 'staged' ? t('git.unstageAll') : t('git.stageAll')"
                :aria-label="group === 'staged' ? t('git.unstageAll') : t('git.stageAll')"
                @click.stop
                @change="setFileStaged(file, group, $event)"
              />
              <span class="git-file-status" :class="`git-file-status--${group}`">{{
                group === "untracked" ? "U" : file.status.slice(0, 1).toUpperCase()
              }}</span>
              <span class="git-file-name">
                <strong>{{ fileName(file.path) }}</strong>
                <small v-if="fileDirectory(file.path)">{{ fileDirectory(file.path) }}</small>
              </span>
              <template v-if="showFileActions(`${group}:${file.path}`)">
              <el-button
                v-if="group === 'staged'"
                text
                native-type="button"
                class="git-file-action"
                :title="t('git.unstageAll')"
                :aria-label="t('git.unstageAll')"
                @click.stop="unstageFile(file)"
                >−</el-button
              >
              <el-button
                v-else
                text
                native-type="button"
                class="git-file-action"
                :title="t('git.stageAll')"
                :aria-label="t('git.stageAll')"
                @click.stop="stageFile(file)"
                >＋</el-button
              >
              <el-button
                v-if="group !== 'staged'"
                text
                native-type="button"
                class="git-file-action git-file-action--danger"
                :title="t('git.discardTitle')"
                :aria-label="t('git.discardTitle')"
                @click.stop="discardFile(file)"
                >↶</el-button
              >
              </template>
              <template v-else>
                <span class="git-file-action-placeholder" aria-hidden="true"></span>
                <span v-if="group !== 'staged'" class="git-file-action-placeholder" aria-hidden="true"></span>
              </template>
            </div>
            <div v-if="groupWindows[group].paddingBottom" aria-hidden="true" :style="{ height: `${groupWindows[group].paddingBottom}px` }" />
          </div>
        </section>
        <div v-if="totalChanges === 0" class="git-clean-state">
          <span>{{ t("project.git.clean") }}</span>
        </div>
      </el-scrollbar>

      <div v-else-if="changeTab === 'history'" class="project-git-history-pane">
        <GitHistoryList
          :entries="logs"
          :selected-hash="selectedCommitHash"
          :empty-text="t('git.noHistory')"
          :actions="['copy-hash', 'cherry-pick', 'revert']"
          @select="openCommitDetail"
          @action="handleHistoryAction"
          @menu-open-change="openHistoryMenuEntry = $event"
        >
          <template #menu-extra="{ open }">
            <PluginMenuItems
              menu="git/commit/context"
              :host="open ? commitPluginMenu.host.value : null"
              :context="commitPluginMenu.context"
            />
          </template>
        </GitHistoryList>
        <section v-if="selectedCommit" class="project-git-commit-detail">
          <header>
            <div>
              <strong :title="selectedCommit.commit.message">{{
                selectedCommit.commit.message
              }}</strong>
              <small
                >{{ selectedCommit.commit.short_hash }} · {{ selectedCommit.commit.author }}</small
              >
            </div>
            <span>{{ t("git.changedFiles", { count: selectedCommit.files.length }) }}</span>
          </header>
          <el-scrollbar v-if="selectedCommit.files.length" ref="commitFilesScroller" class="project-git-commit-files">
            <div v-bind="{ [ROW_GROUP_ATTRIBUTE]: 'files' }">
            <div v-if="commitFileWindow.paddingTop" aria-hidden="true" :style="{ height: `${commitFileWindow.paddingTop}px` }" />
            <button
              v-for="file in commitFileWindow.rows"
              :key="`${file.old_path ?? ''}:${file.path}`"
              type="button"
              class="project-git-commit-file"
              :class="{ 'is-active': selectedCommitFilePath === file.path }"
              :title="file.old_path ? `${file.old_path} → ${file.path}` : file.path"
              @click="openCommitFile(file)"
            >
              <b>{{ file.status }}</b>
              <span>
                <strong>{{ fileName(file.path) }}</strong>
                <small>{{ fileDirectory(file.path) || file.old_path || "" }}</small>
              </span>
            </button>
            <div v-if="commitFileWindow.paddingBottom" aria-hidden="true" :style="{ height: `${commitFileWindow.paddingBottom}px` }" />
            </div>
          </el-scrollbar>
          <div v-else class="project-git-commit-empty">{{ t("git.noChangedFiles") }}</div>
        </section>
      </div>

      <el-scrollbar v-else class="project-git-management-pane project-git-console__scroll">
        <GitShelfPanel
          v-if="changeTab === 'shelf'"
          :repo-path="repoPath"
          @changed="refreshAll"
        />
        <GitStashPanel
          v-else
          ref="stashPanel"
          :repo-path="repoPath"
          :show-create-action="false"
          @changed="refreshAll"
        />
      </el-scrollbar>

      <el-collapse v-model="expandedCommitSections" class="project-git-commit-collapse">
        <el-collapse-item name="commit">
          <template #title>
            <span class="project-git-commit-collapse__title">{{ t("git.commit") }}</span>
            <span class="project-git-commit-collapse__count">{{ status?.staged.length ?? 0 }} {{ t("project.git.staged") }}</span>
            <div
              v-if="expandedCommitSections.includes('commit')"
              class="git-commit-box__header project-git-commit-collapse__actions"
              @click.stop
              @keydown.stop
            >
              <el-checkbox v-model="amendLastCommit" :disabled="!logs.length">
                {{ t("git.amend") }}
              </el-checkbox>
              <el-button
                text
                native-type="button"
                :disabled="pluginGeneratingCommitMessage"
                :title="aiGeneratingCommitMessage ? t('common.cancel') : t('git.aiGenerateCommitMessage')"
                @click="generateCommitMessage"
              >
                <el-icon :class="{ 'is-generating': aiGeneratingCommitMessage }"><MagicStick /></el-icon>
                {{ aiGeneratingCommitMessage ? t("common.cancel") : t("git.aiGenerate") }}
              </el-button>
              <GitPluginCommitMessageButton
                v-if="scmProviders.length"
                :providers="scmProviders"
                :project-root="projectRoot"
                :repo-path="repoPath"
                :has-staged="Boolean(status?.staged.length)"
                :draft="commitMessage"
                :busy="aiGeneratingCommitMessage"
                :generating="pluginGeneratingCommitMessage"
                @start="onPluginCommitMessageStart"
                @finish="onPluginCommitMessageFinish"
                @cancel="onPluginCommitMessageCancel"
              />
            </div>
          </template>
          <section class="git-commit-box git-commit-box--original git-commit-box--ide">
            <el-scrollbar class="git-commit-message-scroll" max-height="132px">
              <el-input
                v-model="commitMessage"
                type="textarea"
                :autosize="{ minRows: 4 }"
                resize="none"
                :placeholder="commitMessagePlaceholder"
              />
            </el-scrollbar>
            <div class="git-commit-submit-row">
              <span>{{ status?.staged.length ?? 0 }} {{ t("project.git.staged") }}</span>
              <div class="git-commit-submit-row__actions">
                <el-button
                  type="primary"
                  size="small"
                  :loading="submitting"
                  :disabled="!commitReady || generatingCommitMessage || submitting"
                  @click="commit"
                >
                  {{ t("git.commit") }}
                </el-button>
                <el-button
                  size="small"
                  :loading="submitting"
                  :disabled="!commitReady || generatingCommitMessage || submitting"
                  @click="commitAndPush"
                >
                  {{ t("git.commitAndPush") }}
                </el-button>
              </div>
            </div>
          </section>
        </el-collapse-item>
      </el-collapse>
    </template>

    <div v-else class="git-source-empty">
      <el-icon :size="28"><FolderOpened /></el-icon>
      <strong>{{
        repositories.length ? t("git.selectRepository") : t("git.noRepositories")
      }}</strong>
      <span>{{ t("git.openRepositoryHint") }}</span>
    </div>

    <AppContextMenu
      :open="fileContextMenu.open"
      :x="fileContextMenu.x"
      :y="fileContextMenu.y"
      :width="238"
      popper-class="git-file-context-menu"
      @close="closeFileContextMenu"
    >
      <el-dropdown-item @click="openContextChanges">
        <el-icon><EditPen /></el-icon>{{ t("git.fileContext.openChanges") }}
      </el-dropdown-item>
      <el-dropdown-item
        :disabled="fileContextMenu.entry?.status === 'deleted'"
        @click="openContextWorktreeFile"
      >
        <el-icon><Document /></el-icon>{{ t("git.fileContext.openFile") }}
      </el-dropdown-item>
      <el-dropdown-item
        v-if="fileContextMenu.group === 'staged'"
        divided
        :disabled="loading"
        @click="unstageContextFile"
      >
        <el-icon><Remove /></el-icon>{{ t("git.fileContext.unstage") }}
      </el-dropdown-item>
      <el-dropdown-item v-else divided :disabled="loading" @click="stageContextFile">
        <el-icon><Plus /></el-icon>{{ t("git.fileContext.stage") }}
      </el-dropdown-item>
      <el-dropdown-item
        v-if="fileContextMenu.group !== 'staged'"
        class="is-danger"
        :disabled="loading"
        @click="discardContextFile"
      >
        <el-icon><Delete /></el-icon>{{ t("git.fileContext.discard") }}
      </el-dropdown-item>
      <el-dropdown-item
        divided
        :disabled="fileContextMenu.entry?.status === 'deleted'"
        @click="revealContextFile"
      >
        <el-icon><FolderOpened /></el-icon>{{ t("git.fileContext.reveal") }}
      </el-dropdown-item>
      <el-dropdown-item @click="copyContextRelativePath">
        <el-icon><CopyDocument /></el-icon>{{ t("git.fileContext.copyRelativePath") }}
      </el-dropdown-item>
      <PluginMenuItems
        menu="git/changes/context"
        :host="changePluginMenu.host.value"
        :context="changePluginMenu.context"
        @run="closeFileContextMenu"
      />
    </AppContextMenu>
  </section>
</template>
