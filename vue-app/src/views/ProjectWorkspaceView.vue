<script setup lang="ts">
import {
  computed,
  defineAsyncComponent,
  h,
  nextTick,
  onActivated,
  onBeforeUnmount,
  onDeactivated,
  onMounted,
  ref,
  shallowRef,
  watch,
} from "vue";
import { useRoute, useRouter } from "vue-router";
import {
  ArrowDown,
  ArrowUp,
  CircleCheck,
  CirclePlus,
  Collection,
  Connection,
  ChatDotRound,
  Document,
  CopyDocument,
  Delete,
  DocumentAdd,
  DocumentCopy,
  Download,
  EditPen,
  ForkSpoon,
  FolderAdd,
  FolderOpened,
  Grid,
  Guide,
  List,
  MoreFilled,
  Operation,
  Notebook,
  Plus,
  Refresh,
  Remove,
  Search,
  Setting,
  Share,
  Upload,
  VideoPlay,
  View,
  Warning,
} from "@element-plus/icons-vue";
import { ElCheckbox, ElMessage, ElMessageBox, type DropdownInstance } from "element-plus";
import ProjectVirtualTree from "@/components/projects/ProjectVirtualTree.vue";
import type { LazyTreeApi, LoadFunction } from "@/domain/projects/lazy-tree";
import { storeToRefs } from "pinia";
import ProjectEditorSettings from "@/components/projects/ProjectEditorSettings.vue";
import { useProjectAutoSave } from "@/composables/use-project-auto-save";
import { useProjectEditorDebug } from "@/composables/use-project-editor-debug";
import { useDebugHover } from "@/composables/use-debug-hover";
import { useDebugCommands } from "@/composables/use-debug-commands";
import { useDebugPanelTabs } from "@/composables/use-debug-panel-tabs";
import { setDebugTerminalOpener } from "@/services/debug-guidance";
import { sameDebugDocumentPath, setDebugLineAnchorProvider } from "@/services/debug-line-anchor";
import ProjectEditorDebugPopups from "@/components/projects/ProjectEditorDebugPopups.vue";
import { useWorkspaceStateScope } from "@/composables/use-workspace-state-scope";
import { useSourceControlBadgeStore } from "@/stores/source-control-badge";
import { projectSaveCoordinator } from "@/services/project-save-coordinator";
import { isEditableProjectText, type SaveOutcome } from "@/domain/projects/save-policy";
import { fileUriPath } from "@/domain/projects/lsp-problems";
import type { IPosition, IRange, Uri } from "@/services/monaco-runtime.js";
import { registerMonacoContributionHook } from "@/services/monaco-contributions";
import { requestOpenSettings } from "@/services/settings-navigation";
import type { ProjectEditorPreferences } from "@/domain/projects/types";
import type { EditorSaveContext } from "@/services/editor-save-participants";
import ImagePreview from "@/components/projects/ImagePreview.vue";
import ExternalFilePreview from "@/components/projects/ExternalFilePreview.vue";
import { useProjectDocumentLoader } from "@/composables/use-project-document-loader";
import { useProjectLanguageServers } from "@/composables/use-project-language-servers";
import ProjectLanguageServersDialog from "@/components/projects/ProjectLanguageServersDialog.vue";
import ProjectFileIcon from "@/components/projects/ProjectFileIcon.vue";
import ProjectGitPanel from "@/components/projects/ProjectGitPanel.vue";
import ProjectTasksPanel from "@/components/projects/ProjectTasksPanel.vue";
import ProjectSidebarTabs, { type ProjectSidebarTabItem } from "@/components/projects/ProjectSidebarTabs.vue";
import GitWorktreePanel from "@/components/git/GitWorktreePanel.vue";
import GitTagPanel from "@/components/git/GitTagPanel.vue";
import GitStashPanel from "@/components/git/GitStashPanel.vue";
import GitShelfPanel from "@/components/git/GitShelfPanel.vue";
import GitRebasePanel from "@/components/git/GitRebasePanel.vue";
import GitCommitGraph from "@/components/git/GitCommitGraph.vue";
import ProjectRemoteDialogs from "@/components/projects/ProjectRemoteDialogs.vue";
import {
  DEFAULT_EDITOR_PREFERENCES,
  isImageDocument,
  isMarkdownDocument,
  joinProjectPath,
  projectBasename,
  projectDirname,
  sortProjectEntries,
  toProjectTreeNode,
  type GitFileEntry,
  type ProjectBlameHunk,
  type ProjectFileEntry,
  type ProjectEditorDocument,
  type ProjectSearchResult,
  type ProjectTreeNode,
} from "@/domain/projects/types";
import { isProjectDocumentDirty } from "@/domain/projects/document-dirty";
import type { TerminalPaneNode, TerminalSplitDirection } from "@/domain/terminal/types";
import {
  buildWorktreeBinding,
  formatWorktreeChanges,
  groupProjectWorktrees,
  resolveOwningRepoPath,
  type WorktreeProjectRelation,
} from "@/domain/git/worktree";
import {
  getGitCommitDetail,
  getGitCommitFileDiff,
  getGitLog,
  getRepositorySummaries,
  listGitWorktrees,
  listProjectRepositories,
  type GitCommitFile,
  type GitLogEntry,
  type GitRepositoryEntry,
  type GitWorktreeChangeStats,
  type GitWorktreeEntry,
} from "@/services/git-service";
import { deleteTerminalSnapshot } from "@/services/terminal-session-cache";
import type { LocalProjectEntry } from "@/stores/local-project";
import { useVueI18n, type MessageKey } from "@/i18n/messages";
import { registerKeybindingContext } from "@/domain/keybindings/context";
import { registerCommand } from "@/services/command-registry";
import { refreshProjectFileIndex } from "@/services/project-index";
import { canInvoke, chooseNativeDirectory, copyText, invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";
import { isBrowserProjectPath } from "@/domain/projects/browser-root";
import { useBrowserProjectAccess } from "@/composables/use-browser-project-access";
import { consumeProjectFiles, pendingProjectFiles } from "@/services/external-file-open";
import { notifyEditorSaved, runEditorSaveParticipants } from "@/services/editor-save-participants";
import type { FileTemplateCreatedEvent } from "@/services/file-template-service";
import type { FileTemplateKind } from "@/domain/projects/file-templates";
import { useFileTemplatesStore } from "@/stores/file-templates";
import { usePluginTasksStore } from "@/stores/plugin-tasks";
import { usePluginTreeViewsStore } from "@/stores/plugin-tree-views";
import { usePluginTestProvidersStore } from "@/stores/plugin-test-providers";
import { useProjectProblemsStore } from "@/stores/project-problems";
import { PLUGIN_TASKS_REVEAL_EVENT } from "@/services/plugin-task-events";
import { useLocalProjectStore } from "@/stores/local-project";
import { useLinkedNotesDrawer } from "@/stores/linked-notes-drawer";
import { useSshConnectionsStore } from "@/stores/ssh-connections";
import { useTerminalWorkspaceStore } from "@/stores/terminal-workspace";
import { useWorkbenchStore } from "@/stores/workbench";
import AppChevron from "@/components/ui/AppChevron.vue";
import AppWorkspaceTabs from "@/components/ui/AppWorkspaceTabs.vue";
import AppContextMenu from "@/components/ui/AppContextMenu.vue";
import PluginMenuItems from "@/components/plugins/PluginMenuItems.vue";
import PluginServiceMenuItems from "@/components/plugins/PluginServiceMenuItems.vue";
import CustomEditorPanes, { type CustomEditorPane } from "@/components/projects/CustomEditorPanes.vue";
import OpenWithMenu from "@/components/projects/OpenWithMenu.vue";
import {
  associationForPath,
  associationPatternForPath,
  customEditorKey,
  customEditorTitle,
  parseCustomEditorKey,
  TEXT_EDITOR_ASSOCIATION,
  type CustomEditorContribution,
} from "@/domain/plugins/custom-editors";
import { useCustomEditorsStore } from "@/stores/custom-editors";
import { menuPassContext, projectTreeHostContext, type ProjectTreeMenuTarget } from "@/domain/plugins/menus";
import type { FileMenuServiceTarget } from "@/domain/plugins/service-surfaces";
import AppSplitPane from "@/components/ui/AppSplitPane.vue";
import WorkspaceBottomPanel from "@/components/layout/WorkspaceBottomPanel.vue";
import RunConfigSelector from "@/components/debug/RunConfigSelector.vue";
import LspProgressIndicator from "@/components/projects/LspProgressIndicator.vue";
import { sameProjectRoot, useDebugConfigsStore } from "@/stores/debug-configs";
import { addressSourceSession } from "@/domain/debug/address-breakpoints";
import { debugErrorText } from "@/domain/debug/errors";
import { isDebuggerAddressPath, isVirtualSourceDocumentPath } from "@/domain/debug/virtual-source";
import { useDebugSessionsStore } from "@/stores/debug-sessions";
import { loadPersistedValue, savePersistedValue } from "@/services/persistent-store";
import { useResizablePane } from "@/composables/use-resizable-pane";
import { usePluginFileDecorations } from "@/composables/use-plugin-file-decorations";
import {
  decorationNamesOf,
  loadedDirectoryOf,
  type DecorationColor,
  type DecorationDirectory,
  type TreeDecoration,
} from "@/domain/plugins/file-decorations";
import { useActiveViewCommands, useToggleSidebarCommand } from "@/composables/use-active-view-commands";
import { registerDocumentTabHost } from "@/services/document-tab-host";
import { recordClosedEntry } from "@/services/closed-tab-history";
import { registerAiWorkspaceActionHandler } from "@/services/ai-workspace-actions";
import { useAiAssistantStore } from "@/stores/ai-assistant";
import {
  MAX_AI_CONTEXT_TREE_ENTRIES,
  createProjectDirectoryContext,
  createProjectFileContext,
  type ProjectContextAttachmentResult,
  type ProjectContextFileResult,
  type ProjectContextTreeResult,
} from "@/domain/ai/project-context";
import { MAX_AI_TEXT_ATTACHMENT_BYTES } from "@/domain/ai/attachments";

// Editors, terminals and markdown are only rendered on demand; loading them lazily keeps them out of this view's startup chunk.
const MarkdownPreview = defineAsyncComponent(() => import("@/components/projects/MarkdownPreview.vue"));
const MonacoDiffEditor = defineAsyncComponent(() => import("@/components/projects/MonacoDiffEditor.vue"));
const MonacoTextEditor = defineAsyncComponent(() => import("@/components/projects/MonacoTextEditor.vue"));
const ProjectOutlinePanel = defineAsyncComponent(() => import("@/components/projects/ProjectOutlinePanel.vue"));
const ProjectProblemsPanel = defineAsyncComponent(() => import("@/components/projects/ProjectProblemsPanel.vue"));
const ProjectPluginViewsPanel = defineAsyncComponent(() => import("@/components/projects/ProjectPluginViewsPanel.vue"));
const ProjectPluginTestsPanel = defineAsyncComponent(() => import("@/components/projects/ProjectPluginTestsPanel.vue"));
const DebugToolWindow = defineAsyncComponent(() => import("@/components/debug/DebugToolWindow.vue"));
const DebugLaunchProgress = defineAsyncComponent(() => import("@/components/debug/DebugLaunchProgress.vue"));
const RunConfigDialog = defineAsyncComponent(() => import("@/components/debug/RunConfigDialog.vue"));
const DebugEvaluateDialog = defineAsyncComponent(() => import("@/components/debug/DebugEvaluateDialog.vue"));
const TerminalPaneLayout = defineAsyncComponent(() => import("@/components/terminal/TerminalPaneLayout.vue"));
// Pulls in the conflict resolver (and Monaco); only needed once the branches inspector opens.
const GitBranchPanel = defineAsyncComponent(() => import("@/components/git/GitBranchPanel.vue"));
// Plugin fileTemplates: mounted on first use only.
const CustomFileTemplatesDialog = defineAsyncComponent(() => import("@/components/projects/CustomFileTemplatesDialog.vue"));
const customTemplatesVisible = ref(false);
const NewFromTemplateDialog = defineAsyncComponent(() => import("@/components/projects/NewFromTemplateDialog.vue"));

defineOptions({ name: "ProjectWorkspaceView" });

const { t, te, localeChain } = useVueI18n();
const route = useRoute();
const router = useRouter();
const project = useLocalProjectStore();
const linkedNotes = useLinkedNotesDrawer();
const editorScope = useWorkspaceStateScope();
// This instance is cached per workspace scope; publish its change count for the activity bar badge.
const sourceControlBadge = useSourceControlBadgeStore();
const sourceControlBadgeScope = editorScope.scopeId.value;
const ssh = useSshConnectionsStore();
const terminalWorkspace = useTerminalWorkspaceStore();
const workbench = useWorkbenchStore();
const assistant = useAiAssistantStore();
const customEditors = useCustomEditorsStore();
const customEditorPanes = ref<InstanceType<typeof CustomEditorPanes>>();
const projectPathInput = ref(project.projectRoot);
interface WorkspaceTreeNode extends ProjectTreeNode {
  projectRoot: string;
  isProjectRoot?: boolean;
  /** 当前目录自身是 Git 仓库时的分支；普通目录不设置。 */
  repositoryBranch?: string;
  /** 该项目根是某个已列出主仓库的 worktree 时携带的展示信息。 */
  worktree?: {
    branch?: string;
    changes?: GitWorktreeChangeStats;
    isLocked: boolean;
    isPrunable: boolean;
  };
  children?: WorkspaceTreeNode[];
}

interface RegexSearchMatch {
  relativePath: string;
  path: string;
  line: number;
  col: number;
  lineText: string;
}

interface ReplacePreviewFile {
  relativePath: string;
  path: string;
  hunks: Array<{ line: number; oldText: string; newText: string }>;
}

interface WorkspaceSearchResult extends ProjectSearchResult {
  projectRoot: string;
  projectName: string;
  col?: number;
}

interface WorkspaceReplacePreviewFile extends ReplacePreviewFile {
  projectRoot: string;
  projectName: string;
}

interface ProjectBreadcrumb {
  label: string;
  path: string;
  isFile: boolean;
}

interface ProjectContextMenuState {
  node: WorkspaceTreeNode;
  x: number;
  y: number;
}

interface ProjectSearchScope {
  projectRoot: string;
  path: string;
  label: string;
}

interface GitBranchEntry {
  name: string;
  is_current: boolean;
  is_remote: boolean;
}

interface GitBranches {
  current: string;
  branches: GitBranchEntry[];
}

interface ExternalFileInfo {
  path: string;
  name: string;
  kind: "text" | "image" | "pdf" | "binary" | "directory";
  textPreviewAvailable?: boolean;
  mimeType?: string;
  size: number;
}

interface ProjectEditorComparison {
  kind: "git" | "file" | "clipboard";
  label: string;
  originalContent: string;
  originalPath?: string;
  modifiedContent?: string;
  binary?: boolean;
}

interface ProjectGitFileDiff {
  original: string;
  modified: string;
  language: string;
  binary: boolean;
  worktree_content: string;
  worktree_exists: boolean;
  worktree_is_text: boolean;
}

interface ProjectDeployment {
  sshProfileId: string;
  remoteRoot: string;
  excludes: string[];
}

interface ProjectDeploymentSettings {
  default?: ProjectDeployment;
  byPath: Record<string, ProjectDeployment>;
}

interface PersistedProjectDeployments {
  version: 1;
  byProjectRoot: Record<string, ProjectDeploymentSettings>;
}

const PROJECT_DEPLOYMENTS_STORAGE_KEY = "app_project_deployments_v1";
const DEFAULT_DEPLOYMENT_EXCLUDES = [
  "node_modules",
  "vendor",
  ".git",
  "dist",
  "target",
  ".idea",
  ".vscode",
];

const tree = ref<WorkspaceTreeNode[]>([]);
const treeVersion = ref(0);
const repositoryBranches = ref(new Map<string, Map<string, string>>());
let repositoryBranchRefreshToken = 0;
const fileTreeRef = ref<LazyTreeApi<WorkspaceTreeNode>>();
const lsp = useProjectLanguageServers({
  projectRoot: computed(() => project.projectRoot),
  enabledServers: computed(() => project.lspSettings.enabledServers),
  lspSettings: computed(() => project.lspSettings),
  enableServer: (serverId) => project.updateLspSettings({ enabledServers: [...project.lspSettings.enabledServers, serverId] }),
});
const lspDialogVisible = ref(false);

function openLspDialog() {
  lspDialogVisible.value = true;
}
const selectedNode = ref<WorkspaceTreeNode | null>(null);
const copiedFileNode = ref<WorkspaceTreeNode | null>(null);
const contextMenu = ref<ProjectContextMenuState | null>(null);
const { projectTerminalTabs, activeProjectTerminalId } = storeToRefs(project);
const searchScope = ref<ProjectSearchScope | null>(null);
const renamingPath = ref("");
const renameValue = ref("");
// Plugin fileDecorations: a badge or dot after the file name (never the name's own style).
const fileDecorations = usePluginFileDecorations({ renamingPath });
const decorationFor = fileDecorations.decorationFor;
const DECORATION_COLOR_LABELS = {
  accent: "pluginContrib.fileDecorations.color.accent",
  success: "pluginContrib.fileDecorations.color.success",
  warning: "pluginContrib.fileDecorations.color.warning",
  danger: "pluginContrib.fileDecorations.color.danger",
  info: "pluginContrib.fileDecorations.color.info",
  muted: "pluginContrib.fileDecorations.color.muted",
} as const satisfies Record<DecorationColor, string>;
/**
 * Badge text (or a dot when there is none) coloured only through the
 * is-<color> token classes, labelled with the plugin; a dot without a
 * tooltip is read as the colour's meaning.
 */
function decorationAttrs(decoration: TreeDecoration) {
  const label = t("pluginContrib.fileDecorations.label", {
    text:
      decoration.tooltip ||
      decoration.badge ||
      (decoration.color ? t(DECORATION_COLOR_LABELS[decoration.color]) : ""),
    plugin: decoration.pluginName,
  });
  return {
    class: [
      "project-tree-node__decoration",
      decoration.badge ? "" : "project-tree-node__decoration--dot",
      decoration.color ? `is-${decoration.color}` : "",
    ],
    role: "img",
    title: label,
    "aria-label": label,
  };
}
type PanelSideMode = "git" | "tasks" | "outline" | "problems" | "views" | "tests";
const PANEL_SIDE_MODES: ReadonlySet<string> = new Set<PanelSideMode>(["git", "tasks", "outline", "problems", "views", "tests"]);
const sideMode = ref<"explorer" | "search" | PanelSideMode>("explorer");
// Panel modes other than git replace the explorer without changing the route.
const isLocalPanelMode = (mode: string) => PANEL_SIDE_MODES.has(mode) && mode !== "git";
const showsExplorer = computed(() => !PANEL_SIDE_MODES.has(sideMode.value));
// Plugin tasks (contributes.tasks): the Tasks tab only exists while a plugin contributes tasks.
const pluginTasks = usePluginTasksStore();
const pluginTreeViews = usePluginTreeViewsStore();
const pluginTestProviders = usePluginTestProvidersStore();
const errorKey = ref<MessageKey | null>(null);
const loading = ref(false);
const searchQuery = ref("");
const includeContent = ref(true);
const searchCaseSensitive = ref(false);
const searchWholeWord = ref(false);
const searchUseRegex = ref(false);
const searchReplaceMode = ref(false);
const searchAllProjects = ref(false);
const replaceQuery = ref("");
const replacePreview = ref<WorkspaceReplacePreviewFile[]>([]);
const replaceApplying = ref(false);
const searchLoading = ref(false);
const searchResults = ref<WorkspaceSearchResult[]>([]);
let searchRequestGeneration = 0;
const gitChangeCount = ref(0);
watch(gitChangeCount, (count) => sourceControlBadge.report(sourceControlBadgeScope, count));
onBeforeUnmount(() => sourceControlBadge.clear(sourceControlBadgeScope));
const workspaceElement = ref<HTMLElement | null>(null);
const documentTabsElement = ref<HTMLElement | null>(null);
const documentTabsOverflowDropdown = ref<DropdownInstance>();
const tabsOverflow = ref(false);
const editorSettingsOpen = ref(false);
const canUndo = ref(false);
const canRedo = ref(false);
const previewMode = ref(false);
const editorComparison = ref<ProjectEditorComparison | null>(null);
const gitCompareLoading = ref(false);
const clipboardCompareLoading = ref(false);
let clipboardCompareRequest = 0;
const gitBranches = ref<GitBranches | null>(null);
const gitBranchesLoading = ref(false);
const blameEnabled = ref(false);
// 始终整体替换（git_blame 结果），无需对成千上万个 hunk 建立深层响应式代理。
const blameHunks = shallowRef<ProjectBlameHunk[]>([]);
const blameLoading = ref(false);
const deployments = ref<Record<string, ProjectDeploymentSettings>>({});
const deploymentDialogVisible = ref(false);
const deploymentNode = ref<WorkspaceTreeNode | null>(null);
const deploymentProfileId = ref("");
const deploymentRemoteRoot = ref("");
const deploymentExcludes = ref("");
const deploying = ref(false);
const remoteServerPickerVisible = ref(false);
const remoteServerNode = ref<WorkspaceTreeNode | null>(null);
const remoteServerProfileId = ref("");
const branchPickerVisible = ref(false);
const branchPickerNode = ref<WorkspaceTreeNode | null>(null);
const branchPickerBranches = ref<GitBranches | null>(null);
const branchPickerLoading = ref(false);
const deploymentScopeLabel = computed(() => {
  const node = deploymentNode.value;
  if (!node) return "";
  return node.isProjectRoot ? node.name : deploymentScopeForNode(node) || ".";
});
let documentTabsResizeObserver: ResizeObserver | null = null;

interface MonacoEditorHandle {
  undo: () => void;
  redo: () => void;
  flushContent: () => void;
  hasModel: (path: string) => boolean;
  captureSaveAcknowledgement: (path: string) => (() => void) | undefined;
  prepareSave: (path: string, preferences: ProjectEditorPreferences, context: EditorSaveContext) => Promise<void>;
  captureBreakpointMoves: (path: string) => (() => void) | undefined;
  languageIdFor: (path: string) => string | undefined;
  cursorLine: () => number | null;
  screenPositionOf: (line: number, column?: number) => { x: number; y: number } | null;
  evaluateText: () => string;
  focus: () => void;
}

const monacoEditor = ref<MonacoEditorHandle | null>(null);
// The editor model owns in-progress edits; pull them into the store before reading `document.content`.
function flushEditorContent() {
  monacoEditor.value?.flushContent();
}
const unregisterEditorContentFlusher = project.registerDocumentContentFlusher(flushEditorContent);
// Preview and diff views render `activeDocument.content` in place of the editor.
watch([previewMode, editorComparison], flushEditorContent, { flush: "sync" });
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
  | "manage-stash"
  | "interactive-rebase"
  | "discard-all";
interface GitQuickActionState {
  repoPath: string;
  loading: boolean;
  hasStaged: boolean;
  hasUnstaged: boolean;
  totalChanges: number;
  canRenameBranch: boolean;
  canUndoLastCommit: boolean;
}
interface ProjectGitPanelHandle {
  refresh: () => Promise<void>;
  selectRepository: (path: string) => Promise<void>;
  runQuickAction: (action: GitQuickAction) => Promise<void>;
  quickActionState: GitQuickActionState;
}
const gitPanel = ref<ProjectGitPanelHandle | null>(null);
const resourceGitSelection = ref<WorkspaceTreeNode | null>(null);
const emptyGitQuickActionState: GitQuickActionState = {
  repoPath: "",
  loading: false,
  hasStaged: false,
  hasUnstaged: false,
  totalChanges: 0,
  canRenameBranch: false,
  canUndoLastCommit: false,
};
const resourceGitState = computed(
  () => gitPanel.value?.quickActionState ?? emptyGitQuickActionState
);
type GitInspector = "branches" | "tags" | "shelf" | "stash" | "graph" | "worktrees" | "rebase";
const gitInspectorOpen = ref(false);
/** 批量创建执行时避免遮罩或 Esc 误关抽屉，导致执行状态丢失。 */
const worktreeBatchActive = ref(false);
const gitInspector = ref<GitInspector>("worktrees");
const gitInspectorRepoPath = ref("");
const branchRepoOptions = ref<GitRepositoryEntry[]>([]);
const branchRepoLoading = ref(false);
const branchPanelBusy = ref(false);
const worktreeRepoOptions = ref<GitRepositoryEntry[]>([]);
const worktreeRepoSelection = ref("");
const gitGraphLogs = ref<GitLogEntry[]>([]);
// 保留旧存储键，避免已调整过的工作树宽度在升级后丢失。
const GIT_INSPECTOR_WIDTH_KEY = "app_git_worktree_inspector_width";
const DEFAULT_GIT_INSPECTOR_WIDTH = 720;
const MIN_GIT_INSPECTOR_WIDTH = 480;
const MIN_WORKTREE_INSPECTOR_WIDTH = 620;
const gitInspectorWidth = ref(DEFAULT_GIT_INSPECTOR_WIDTH);
const resizingGitInspector = ref(false);
const gitInspectorResizable = computed(
  () => gitInspector.value === "branches" || gitInspector.value === "worktrees"
);
const gitInspectorTitle = computed(() => {
  const titles: Record<GitInspector, string> = {
    branches: t("git.inspector.branches"),
    tags: t("git.inspector.tags"),
    shelf: t("git.inspector.shelf"),
    stash: t("git.inspector.stash"),
    graph: t("git.inspector.graph"),
    worktrees: t("git.inspector.worktrees"),
    rebase: t("git.inspector.rebase"),
  };
  const title = titles[gitInspector.value];
  // 跨项目管理时明确显示当前操作对象。
  if (!["branches", "worktrees", "tags"].includes(gitInspector.value) || !gitInspectorRepoPath.value) return title;
  const repoPath = gitInspectorRepoPath.value;
  // 用户可能重命名过项目，优先显示他自己起的名字。
  const saved = project.projects.find((entry) => entry.root === repoPath);
  const repository = branchRepoOptions.value.find((entry) => entry.path === repoPath);
  return `${title} · ${saved?.name || (gitInspector.value === "branches" && repository?.name) || projectBasename(repoPath)}`;
});
const gitInspectorSize = computed(() => {
  if (gitInspector.value === "graph") return "min(900px, 72vw)";
  if (gitInspectorResizable.value) return `${gitInspectorWidth.value}px`;
  if (gitInspector.value === "rebase") return "560px";
  return "360px";
});

function gitInspectorWidthLimits() {
  const maximum = Math.max(280, window.innerWidth - 48);
  const preferredMinimum = gitInspector.value === "worktrees"
    ? MIN_WORKTREE_INSPECTOR_WIDTH
    : MIN_GIT_INSPECTOR_WIDTH;
  return {
    minimum: Math.min(preferredMinimum, maximum),
    maximum,
  };
}

function clampGitInspectorWidth(value: number) {
  const { minimum, maximum } = gitInspectorWidthLimits();
  return Math.round(Math.max(minimum, Math.min(maximum, value)));
}

function updateGitInspectorWidth(clientX: number) {
  gitInspectorWidth.value = clampGitInspectorWidth(window.innerWidth - clientX);
}

function clampGitInspectorToViewport() {
  gitInspectorWidth.value = clampGitInspectorWidth(gitInspectorWidth.value);
}

function resizeGitInspector(event: PointerEvent) {
  updateGitInspectorWidth(event.clientX);
}

function stopGitInspectorResize() {
  if (!resizingGitInspector.value) return;
  resizingGitInspector.value = false;
  document.body.classList.remove("is-resizing-git-inspector");
  window.removeEventListener("pointermove", resizeGitInspector);
  window.removeEventListener("pointerup", stopGitInspectorResize);
  window.removeEventListener("pointercancel", stopGitInspectorResize);
  void savePersistedValue(GIT_INSPECTOR_WIDTH_KEY, gitInspectorWidth.value);
}

function startGitInspectorResize(event: PointerEvent) {
  if (event.button !== 0) return;
  event.preventDefault();
  resizingGitInspector.value = true;
  document.body.classList.add("is-resizing-git-inspector");
  updateGitInspectorWidth(event.clientX);
  window.addEventListener("pointermove", resizeGitInspector);
  window.addEventListener("pointerup", stopGitInspectorResize);
  window.addEventListener("pointercancel", stopGitInspectorResize);
}

function resizeGitInspectorWithKeyboard(event: KeyboardEvent) {
  const step = event.shiftKey ? 40 : 10;
  let width = gitInspectorWidth.value;
  if (event.key === "ArrowLeft") width += step;
  else if (event.key === "ArrowRight") width -= step;
  else if (event.key === "Home") width = gitInspectorWidthLimits().minimum;
  else if (event.key === "End") width = gitInspectorWidthLimits().maximum;
  else return;
  event.preventDefault();
  gitInspectorWidth.value = clampGitInspectorWidth(width);
  void savePersistedValue(GIT_INSPECTOR_WIDTH_KEY, gitInspectorWidth.value);
}
const {
  size: sidebarWidth,
  collapsed: sidebarCollapsed,
  hydrate: hydrateSidebarLayout,
  persist: persistSidebarLayout,
} = useResizablePane({
  sizeKey: "app_project_sidebar_width",
  collapsedKey: "app_project_sidebar_collapsed",
  defaultSize: 240,
  minSize: 200,
  maxSize: (workspaceWidth) => Math.max(200, workspaceWidth * 0.56),
  containerRef: workspaceElement,
});

function persistSidebarWidth(value: number) {
  sidebarWidth.value = value;
  void persistSidebarLayout();
}

function updateSidebarCollapsed(value: boolean) {
  sidebarCollapsed.value = value;
  void persistSidebarLayout();
}
useToggleSidebarCommand(() => sidebarCollapsed.value, updateSidebarCollapsed);

const activeProjectTerminal = computed(
  () =>
    projectTerminalTabs.value.find((terminal) => terminal.id === activeProjectTerminalId.value) ??
    null
);
const activeProjectEditorTab = computed<string>({
  get: () =>
    activeProjectTerminalId.value
      ? `terminal:${activeProjectTerminalId.value}`
      : (project.activePath ?? ""),
  set: (name) => {
    if (name.startsWith("terminal:")) {
      const terminalId = name.slice("terminal:".length);
      if (projectTerminalTabs.value.some((terminal) => terminal.id === terminalId)) {
        selectProjectTerminal(terminalId);
      }
      return;
    }
    if (project.documents.some((document) => document.path === name)) selectProjectDocument(name);
  },
});
const activeDocument = computed(() =>
  activeProjectTerminalId.value ? null : project.activeDocument
);
const activeDocumentIsImage = computed(() =>
  Boolean(
    activeDocument.value &&
      (activeDocument.value.externalKind === "image" || isImageDocument(activeDocument.value.path)),
  )
);
const activeDocumentIsExternalPreview = computed(() =>
  Boolean(activeDocument.value?.externalKind === "pdf" || activeDocument.value?.externalKind === "binary")
);
const activeDocumentSupportsPreview = computed(() =>
  Boolean(activeDocument.value && isMarkdownDocument(activeDocument.value.path))
);
const isPreviewingDocument = computed(() =>
  Boolean(previewMode.value && activeDocument.value?.loaded && activeDocumentSupportsPreview.value)
);
const isComparingDocument = computed(() =>
  Boolean(editorComparison.value && activeDocument.value?.loaded)
);
const isComparingBinaryDocument = computed(() =>
  Boolean(isComparingDocument.value && editorComparison.value?.binary)
);
const comparableOpenDocuments = computed(() => {
  const activePath = activeDocument.value?.path;
  return project.documents.filter(
    (document) =>
      document.path !== activePath &&
      !document.editor &&
      document.externalKind !== "image" &&
      document.externalKind !== "pdf" &&
      document.externalKind !== "binary" &&
      !isImageDocument(document.path),
  );
});
const isComparingOpenDocument = computed(() =>
  editorComparison.value?.kind === "file" || editorComparison.value?.kind === "clipboard",
);
const activeDirty = computed(() => Boolean(activeDocument.value && isProjectDocumentDirty(activeDocument.value)));
// Plugin custom editor documents have no Monaco model.
const openDocumentPaths = computed(() =>
  project.documents.filter((document) => !document.editor).map((document) => document.path),
);
const customEditorPaneList = computed<CustomEditorPane[]>(() =>
  project.documents.flatMap((document) => {
    if (!document.editor) return [];
    const projectRoot = document.external ? externalDocumentRoot(document.path) : projectRootForPath(document.path);
    return [{
      path: document.path,
      pluginId: document.editor.pluginId,
      editorId: document.editor.editorId,
      projectRoot,
      relativePath: document.external ? projectBasename(document.path) : relativeProjectPath(document.path, projectRoot),
      dirty: isProjectDocumentDirty(document),
    }];
  }),
);
const activeDocumentBreadcrumbs = computed(() => {
  const path = activeDocument.value?.path.replace(/\\/g, "/");
  if (!path) return [] as ProjectBreadcrumb[];

  // `browser://<id>` 不是可浏览的目录：浏览器项目的面包屑从文件夹名开始。
  const browserPrefix = /^browser:\/\/[^/]+/.exec(path)?.[0] ?? "";
  const isAbsolute = path.startsWith("/");
  const parts = path.slice(browserPrefix.length).split("/").filter(Boolean);
  let currentPath = browserPrefix;
  return parts.map((label, index) => {
    if (!currentPath) currentPath = isAbsolute ? `/${label}` : label;
    else currentPath = `${currentPath}/${label}`;
    return { label, path: currentPath, isFile: index === parts.length - 1 };
  });
});
const errorMessage = computed(() => (errorKey.value ? t(errorKey.value) : ""));
/** 读取失败的细节：已知的错误码（如浏览器的 project.browser.permission）显示译文。 */
const documentErrorDetail = (detail: string) => (te(detail) ? t(detail) : detail);
/** 大小写 / 全词 / 正则 / 替换走 local_fs_regex_*，浏览器本地模式下没有实现（仅桌面端）。 */
const advancedSearchAvailable = canInvoke("local_fs_regex_search");
const hasAdvancedSearchOptions = computed(
  () => searchCaseSensitive.value || searchWholeWord.value || searchUseRegex.value
);
const replacePreviewFileCount = computed(() => replacePreview.value.length);
const replacePreviewLineCount = computed(() =>
  replacePreview.value.reduce((count, file) => count + file.hunks.length, 0)
);

function updateEditorHistory(state: { canUndo: boolean; canRedo: boolean }) {
  canUndo.value = state.canUndo;
  canRedo.value = state.canRedo;
}

function togglePreviewMode() {
  previewMode.value = !previewMode.value;
  if (previewMode.value) clearGitEditorTools();
  canUndo.value = false;
  canRedo.value = false;
}

function undoEditor() {
  monacoEditor.value?.undo();
}

function redoEditor() {
  monacoEditor.value?.redo();
}

function resetEditorPreferences() {
  project.updateEditorPreferences({ ...DEFAULT_EDITOR_PREFERENCES });
}

function clearGitEditorTools() {
  editorComparison.value = null;
  blameEnabled.value = false;
  blameHunks.value = [];
}

function canCompareResourceNodeWithCurrent(node: WorkspaceTreeNode) {
  const target = activeDocument.value;
  return Boolean(
    !node.isDir &&
      target?.loaded &&
      !target.editor &&
      nodeAbsolutePath(node) !== target.path &&
      !isImageDocument(node.path) &&
      !isImageDocument(target.path) &&
      target.externalKind !== "image" &&
      target.externalKind !== "pdf" &&
      target.externalKind !== "binary",
  );
}

async function compareResourceNodeWithCurrent(node: WorkspaceTreeNode) {
  closeProjectContextMenu();
  if (!canCompareResourceNodeWithCurrent(node)) return;
  const targetPath = activeDocument.value?.path;
  if (!targetPath) return;
  try {
    const originalContent = await invokeBackend<string>("local_fs_read_text_file", {
      projectRoot: node.projectRoot,
      filePath: nodeRelativePath(node),
    });
    // 读取期间用户可能切换标签，避免把对比结果挂到错误的当前文件上。
    if (activeDocument.value?.path !== targetPath) return;
    previewMode.value = false;
    blameEnabled.value = false;
    blameHunks.value = [];
    editorComparison.value = {
      kind: "file",
      label: node.name,
      originalPath: nodeAbsolutePath(node),
      originalContent,
    };
    canUndo.value = false;
    canRedo.value = false;
  } catch {
    ElMessage.error(t("project.editor.compareOpenFileFailed"));
  }
}

async function compareWithOpenDocument(path: string) {
  flushEditorContent();
  let source = project.documents.find((document) => document.path === path);
  const target = activeDocument.value;
  if (!source || !target?.loaded || source.path === target.path) return;
  if (source.editor || target.editor) {
    ElMessage.warning(t("pluginContrib.customEditors.compareUnavailable"));
    return;
  }
  if (!source.loaded) {
    await ensureDocumentLoaded(source.path, projectRootForPath(source.path));
    source = project.documents.find((document) => document.path === path);
  }
  if (!source?.loaded) {
    ElMessage.error(t("project.editor.compareOpenFileFailed"));
    return;
  }
  previewMode.value = false;
  blameEnabled.value = false;
  blameHunks.value = [];
  editorComparison.value = {
    kind: "file",
    label: source.name,
    originalPath: source.path,
    originalContent: source.content,
  };
  canUndo.value = false;
  canRedo.value = false;
}

function canComparePathWithClipboard(path: string) {
  const document = project.documents.find((item) => item.path === path);
  return Boolean(path && !isImageDocument(path) && !/\.pdf$/i.test(path) && !document?.editor &&
    document?.externalKind !== "image" && document?.externalKind !== "pdf" &&
    document?.externalKind !== "binary");
}

async function compareFileWithClipboard(path: string, root = projectRootForPath(path)) {
  if (clipboardCompareLoading.value || !canComparePathWithClipboard(path)) return;
  clipboardCompareLoading.value = true;
  // 先激活目标文件，等待标签监听器清理旧对比，再开始读取剪贴板。
  activeProjectTerminalId.value = null;
  project.openDocument(path);
  await nextTick();
  const request = ++clipboardCompareRequest;
  const isCurrent = () => request === clipboardCompareRequest && project.activePath === path;
  try {
    if (!isCurrent()) return;
    const clipboard = isDesktopRuntime()
      ? await invokeBackend<string>("clipboard_read_text")
      : await navigator.clipboard.readText();
    if (!isCurrent()) return;
    if (clipboard === "") {
      ElMessage.info(t("project.editor.clipboardEmpty"));
      return;
    }
    let document = project.documents.find((item) => item.path === path);
    if (!document?.loaded) {
      if (document?.external && !isVirtualSourceDocumentPath(path)) await openExternalFile(path);
      else await ensureDocumentLoaded(path, root);
      document = project.documents.find((item) => item.path === path);
    }
    if (!isCurrent()) return;
    flushEditorContent();
    if (!document?.loaded || !canComparePathWithClipboard(path) || document.content.includes("\0") || clipboard.includes("\0")) {
      ElMessage.error(t("project.editor.compareOpenFileFailed"));
      return;
    }
    previewMode.value = false;
    blameEnabled.value = false;
    blameHunks.value = [];
    // 只保留本次读取的快照，不写磁盘；左侧包含编辑器尚未保存的修改。
    editorComparison.value = {
      kind: "clipboard",
      label: t("project.editor.clipboardSource"),
      originalContent: document.content,
      modifiedContent: clipboard,
    };
    canUndo.value = false;
    canRedo.value = false;
  } catch {
    if (isCurrent()) ElMessage.error(t("project.editor.clipboardReadFailed"));
  } finally {
    clipboardCompareLoading.value = false;
  }
}

async function compareResourceNodeWithClipboard(node: WorkspaceTreeNode) {
  closeProjectContextMenu();
  if (node.isDir) return;
  await compareFileWithClipboard(nodeAbsolutePath(node), node.projectRoot);
}

async function handleOpenDocumentComparison(command: string) {
  if (command === "clipboard") {
    const path = activeDocument.value?.path;
    if (path) await compareFileWithClipboard(path);
    return;
  }
  if (command === "exit") {
    editorComparison.value = null;
    return;
  }
  await compareWithOpenDocument(command);
}

function activeGitTarget() {
  const document = activeDocument.value;
  if (!document?.loaded || isImageDocument(document.path)) return null;
  // The opened project may be an outer workspace rather than the Git repository
  // itself. Start discovery from the current file's directory so nested Git
  // repositories (for example `workspace/claw-specs`) work correctly.
  const repoPath = projectDirname(document.path);
  return repoPath && document.path ? { repoPath, relativePath: document.path } : null;
}

async function compareActiveDocumentWithRef(gitRef: string) {
  const target = activeGitTarget();
  if (!target || gitCompareLoading.value) return;
  gitCompareLoading.value = true;
  previewMode.value = false;
  blameEnabled.value = false;
  blameHunks.value = [];
  try {
    const originalContent =
      gitRef === "HEAD"
        ? await invokeBackend<string>("git_read_file_at_head", target)
        : await invokeBackend<string>("git_read_file_at_ref", { ...target, gitRef });
    editorComparison.value = { kind: "git", label: gitRef, originalContent };
    canUndo.value = false;
    canRedo.value = false;
  } catch {
    ElMessage.error(t("project.git.compareFailed"));
  } finally {
    gitCompareLoading.value = false;
  }
}

async function toggleGitComparison() {
  if (editorComparison.value) {
    editorComparison.value = null;
    return;
  }
  await compareActiveDocumentWithRef("HEAD");
}

async function loadGitBranches(visible: boolean) {
  if (!visible || gitBranchesLoading.value) return;
  const target = activeGitTarget();
  if (!target) return;
  gitBranchesLoading.value = true;
  try {
    gitBranches.value = await invokeBackend<GitBranches>("git_list_branches", {
      repoPath: target.repoPath,
    });
  } catch {
    gitBranches.value = null;
    ElMessage.error(t("project.git.branchesFailed"));
  } finally {
    gitBranchesLoading.value = false;
  }
}

async function toggleBlame() {
  if (blameEnabled.value) {
    blameEnabled.value = false;
    blameHunks.value = [];
    return;
  }
  const target = activeGitTarget();
  if (!target || blameLoading.value || editorComparison.value) return;
  blameLoading.value = true;
  previewMode.value = false;
  try {
    blameHunks.value = await invokeBackend<ProjectBlameHunk[]>("git_blame", target);
    blameEnabled.value = true;
  } catch {
    blameEnabled.value = false;
    blameHunks.value = [];
    ElMessage.error(t("project.git.blameFailed"));
  } finally {
    blameLoading.value = false;
  }
}

watch(
  () => project.activePath,
  () => {
    clipboardCompareRequest++;
    clearGitEditorTools();
    canUndo.value = false;
    canRedo.value = false;
  }
);

watch(activeDocumentSupportsPreview, (supported) => {
  if (!supported) previewMode.value = false;
});

function readDirectory(root: string, dirPath: string | null) {
  return invokeBackend<ProjectFileEntry[]>("local_fs_list_dir", {
    projectRoot: root,
    dirPath,
  });
}

function projectTreeRootId(root: string) {
  return `project:${root}`;
}

function normalizeRepositoryRelativePath(path: string) {
  const normalized = path.replace(/\\/g, "/").replace(/^\/+|\/+$/g, "");
  return normalized === "." ? "" : normalized;
}

function repositoryBranchForPath(projectRoot: string, path: string) {
  return repositoryBranches.value
    .get(projectRoot)
    ?.get(normalizeRepositoryRelativePath(path));
}

function toWorkspaceTreeNode(entry: ProjectFileEntry, projectRoot: string): WorkspaceTreeNode {
  const repositoryBranch = entry.isDir
    ? repositoryBranchForPath(projectRoot, entry.path)
    : undefined;
  return {
    ...toProjectTreeNode(entry),
    id: `${projectRoot}:${entry.path}`,
    projectRoot,
    ...(repositoryBranch ? { repositoryBranch } : {}),
  };
}

function toWorkspaceRootNode(entry: LocalProjectEntry): WorkspaceTreeNode {
  return {
    id: projectTreeRootId(entry.root),
    name: entry.name,
    path: "",
    isDir: true,
    leaf: false,
    projectRoot: entry.root,
    isProjectRoot: true,
    ...(repositoryBranchForPath(entry.root, "")
      ? { repositoryBranch: repositoryBranchForPath(entry.root, "") }
      : {}),
  };
}

/** Windows 后端返回反斜杠路径：比较前统一成正斜杠，盘符/UNC 路径按大小写不敏感处理。 */
function projectPathKey(path: string) {
  const normalized = path.replace(/\\/g, "/").replace(/\/+$/, "") || "/";
  return /^[a-z]:/i.test(normalized) || normalized.startsWith("//") ? normalized.toLowerCase() : normalized;
}

function isProjectPathWithin(path: string, root: string) {
  const pathKey = projectPathKey(path);
  const rootKey = projectPathKey(root);
  if (pathKey.split("/").includes("..")) return false;
  return pathKey === rootKey || pathKey.startsWith(rootKey.endsWith("/") ? rootKey : `${rootKey}/`);
}

function projectRootForPath(path: string) {
  const normalizedPath = path.replace(/\\/g, "/");
  return (
    project.projects
      .filter((entry) => {
        const root = entry.root.replace(/\\/g, "/");
        return normalizedPath === root || normalizedPath.startsWith(`${root}/`);
      })
      .sort((a, b) => b.root.length - a.root.length)[0]?.root ?? project.projectRoot
  );
}

function toDocumentPath(projectRoot: string, path: string) {
  const normalizedPath = path.replace(/\\/g, "/");
  if (normalizedPath.startsWith("/") || /^[A-Za-z]:\//.test(normalizedPath) || isBrowserProjectPath(normalizedPath)) {
    return normalizedPath;
  }
  return `${projectRoot.replace(/\/+$/, "")}/${normalizedPath.replace(/^\/+/, "")}`;
}

function relativeProjectPath(path: string, projectRoot: string) {
  const normalizedPath = path.replace(/\\/g, "/");
  const normalizedRoot = projectRoot.replace(/\\/g, "/").replace(/\/+$/, "");
  return normalizedPath === normalizedRoot
    ? ""
    : normalizedPath.startsWith(`${normalizedRoot}/`)
      ? normalizedPath.slice(normalizedRoot.length + 1)
      : normalizedPath;
}

async function navigateBreadcrumb(breadcrumb: ProjectBreadcrumb) {
  const document = activeDocument.value;
  if (!document) return;

  const documentRoot = projectRootForPath(document.path);
  const normalizedRoot = documentRoot.replace(/\\/g, "/").replace(/\/+$/, "");
  const isInsideProject =
    breadcrumb.path === normalizedRoot || breadcrumb.path.startsWith(`${normalizedRoot}/`);

  if (!isInsideProject) {
    try {
      await invokeBackend<void>("local_fs_show_in_file_manager", { path: breadcrumb.path });
    } catch {
      errorKey.value = "project.operationFailed";
    }
    return;
  }

  if (!(await activateProject(documentRoot))) return;
  if (breadcrumb.isFile) {
    await openFile(relativeProjectPath(breadcrumb.path, documentRoot), undefined, documentRoot);
    return;
  }

  selectedNode.value = toWorkspaceTreeNode(
    {
      name: breadcrumb.label,
      path: relativeProjectPath(breadcrumb.path, documentRoot),
      isDir: true,
    },
    documentRoot
  );
  sideMode.value = "explorer";
  await loadProjectTree(documentRoot);
}

/**
 * 已列出项目之间的 worktree 归属关系。异步查询、同步读取：建树的 11 处调用点里
 * 既有 onMounted 也有 openProject 的失败分支，都不能改成需要 await。
 */
const worktreeChildren = ref(new Map<string, WorktreeProjectRelation[]>());
let worktreeGroupingToken = 0;

function toWorktreeChildNode(relation: WorktreeProjectRelation): WorkspaceTreeNode {
  return {
    id: projectTreeRootId(relation.root),
    name: relation.name,
    path: "",
    isDir: true,
    leaf: false,
    projectRoot: relation.root,
    isProjectRoot: true,
    worktree: {
      ...(relation.branch ? { branch: relation.branch } : {}),
      ...(relation.changes ? { changes: relation.changes } : {}),
      isLocked: relation.isLocked,
      isPrunable: relation.isPrunable,
    },
  };
}

/** 项目根的子节点 = 归属它的 worktree 项目 + 自身一级目录内容。 */
async function projectRootChildren(root: string) {
  const nested = (worktreeChildren.value.get(root) ?? []).map(toWorktreeChildNode);
  const entries = await readDirectory(root, null);
  return [...nested, ...sortProjectEntries(entries).map((entry) => toWorkspaceTreeNode(entry, root))];
}

/** 批量扫描项目根及一级子仓库，并缓存资源树需要展示的当前分支。 */
async function refreshRepositoryBranches() {
  const token = ++repositoryBranchRefreshToken;
  const scans = await Promise.all(
    project.projects.map(async (entry) => ({
      root: entry.root,
      repositories: await listProjectRepositories(entry.root).catch(
        () => [] as GitRepositoryEntry[]
      ),
    }))
  );
  const repositories = scans.flatMap((scan) => scan.repositories);
  const paths = [...new Set(repositories.map((entry) => entry.path))];
  const summaries = paths.length ? await getRepositorySummaries(paths).catch(() => []) : [];
  if (token !== repositoryBranchRefreshToken) return;

  const branchByPath = new Map(summaries.map((summary) => [summary.path, summary.branch]));
  const next = new Map<string, Map<string, string>>();
  for (const scan of scans) {
    const branches = new Map<string, string>();
    for (const repository of scan.repositories) {
      const branch = branchByPath.get(repository.path)?.trim();
      if (branch) branches.set(normalizeRepositoryRelativePath(repository.relative_path), branch);
    }
    next.set(scan.root, branches);
  }
  repositoryBranches.value = next;
}

function treeNodeBranch(node: WorkspaceTreeNode) {
  return node.worktree?.branch || node.repositoryBranch || "";
}

/**
 * 重新查询各项目根的 worktree 关系。列表变化或 worktree 增删后必须刷新，
 * 否则 worktree 会挂在过期的父节点下。
 */
async function refreshWorktreeGrouping() {
  const token = ++worktreeGroupingToken;
  const roots = project.projects.map((entry) => entry.root);
  const results = await Promise.all(
    roots.map(async (root) => {
      try {
        return [root, await listGitWorktrees(root)] as const;
      } catch {
        // 非 Git 目录或查询失败：留在顶层，好过错误嵌套。
        return [root, [] as GitWorktreeEntry[]] as const;
      }
    })
  );
  if (token !== worktreeGroupingToken) return;
  const { childrenByRoot } = groupProjectWorktrees(project.projects, new Map(results));
  worktreeChildren.value = childrenByRoot;
}

function setProjectTreeRoots() {
  const nested = new Set(
    [...worktreeChildren.value.values()].flatMap((children) =>
      children.map((child) => child.root)
    )
  );
  tree.value = project.projects
    .filter((entry) => !nested.has(entry.root))
    .map(toWorkspaceRootNode);
  treeVersion.value += 1;
  selectedNode.value = null;
  return tree.value;
}

async function loadProjectTree(activeRoot = project.projectRoot) {
  // Render every remembered project root before listing any files. A folder with a
  // transient read error must never make the whole left explorer disappear.
  const roots = setProjectTreeRoots();
  const activeNode = roots.find((node) => node.projectRoot === activeRoot);
  if (!activeNode) return;

  activeNode.children = await projectRootChildren(activeRoot);
  tree.value = [...roots];
  treeVersion.value += 1;
}

/** 活动项目根被收进主仓库下时，需要连带展开父节点才能看到它。 */
const expandedProjectKeys = computed(() => {
  const activeRoot = project.projectRoot;
  if (!activeRoot) return [];
  const keys = [projectTreeRootId(activeRoot)];
  for (const [owner, children] of worktreeChildren.value) {
    if (children.some((child) => child.root === activeRoot)) keys.push(projectTreeRootId(owner));
  }
  return keys;
});

/**
 * 重新查询归属关系与分支后同步资源树：根节点集合未变时原地更新（保留用户刚展开的目录），
 * 归属变化导致根集合改变时才整树重建。
 */
async function reloadWorktreeGrouping() {
  await Promise.all([refreshWorktreeGrouping(), refreshRepositoryBranches()]);
  await refreshProjectTreeAfterGit();
}

/** 点击文件标签时在左侧文件树定位到该文件：逐级展开惰性加载的祖先目录后选中并滚动到可见区域。 */
async function revealDocumentInTree(documentPath: string) {
  const root = projectRootForPath(documentPath);
  const relative = relativeProjectPath(documentPath, root);
  if (!relative) return;
  const treeApi = fileTreeRef.value;
  if (!treeApi) return;

  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const expandAsync = (node: any) =>
    new Promise<boolean>((resolve) => {
      try {
        node.expand(() => resolve(true), true);
      } catch {
        resolve(false);
      }
    });

  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const ensureExpanded = async (nodeId: string): Promise<any | null> => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const node = treeApi.getNode(nodeId) as any;
    if (!node?.expand) return null;
    if (!node.expanded && !(await expandAsync(node))) return null;
    return node;
  };

  if (!(await ensureExpanded(projectTreeRootId(root)))) return;
  const segments = relative.split("/").filter(Boolean);
  let ancestorPath = "";
  for (let index = 0; index < segments.length - 1; index += 1) {
    ancestorPath = ancestorPath ? `${ancestorPath}/${segments[index]}` : segments[index];
    if (!(await ensureExpanded(`${root}:${ancestorPath}`))) return;
  }

  const nodeId = `${root}:${relative}`;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const node = treeApi.getNode(nodeId) as any;
  if (!node?.data) return;
  treeApi.setCurrentKey(nodeId);
  selectedNode.value = node.data;
  await nextTick();
  // 虚拟树只渲染可见行，目标行可能尚未挂载，按行号滚动到垂直居中。
  treeApi.scrollToNode(nodeId, "center");
}

const loadTree: LoadFunction<WorkspaceTreeNode> = async (node, resolve) => {
  // Element Plus does not consume the `data` prop while lazy mode is enabled:
  // it invokes `load` once for its synthetic level-0 node instead. Resolve the
  // remembered projects there, otherwise the whole explorer appears empty even
  // though projects have already been persisted successfully.
  if (node.level === 0) return resolve(tree.value);

  const current = node.data as WorkspaceTreeNode | undefined;
  if (!current) return resolve([]);
  const root = current.projectRoot;
  let children: WorkspaceTreeNode[];
  try {
    children = current.isProjectRoot
      ? await projectRootChildren(root)
      : sortProjectEntries(await readDirectory(root, current.path)).map((entry) =>
          toWorkspaceTreeNode(entry, root)
        );
  } catch (error) {
    browserAccess.noteFailure(root, error);
    return resolve([]);
  }
  resolve(children);
  fileDecorations.directoryLoaded(root, treeDirectoryPath(current), decorationNamesOf(children));
};

/** The directory a tree node stands for, as plugin decorations name it ("" is the project root). */
function treeDirectoryPath(data: WorkspaceTreeNode) {
  return data.isProjectRoot ? "" : data.path;
}

type DecorationTreeNode = Parameters<typeof loadedDirectoryOf>[0] & {
  expanded?: boolean;
  childNodes?: DecorationTreeNode[];
};

/** Expanded, loaded directories of the tree, for re-requesting their decorations after a refresh. */
function expandedTreeDirectories() {
  const result: DecorationDirectory[] = [];
  const visit = (node: DecorationTreeNode) => {
    for (const child of node.childNodes ?? []) {
      const entry = child.expanded ? loadedDirectoryOf(child) : undefined;
      if (!entry) continue;
      result.push(entry);
      visit(child);
    }
  };
  const root = (fileTreeRef.value as unknown as { root?: DecorationTreeNode } | undefined)?.root;
  if (root) visit(root);
  return result;
}

function onTreeNodeCollapse(data: WorkspaceTreeNode) {
  fileDecorations.directoryCollapsed(data.projectRoot, treeDirectoryPath(data));
}

/** Element Plus calls load only on the first expansion; a re-expanded folder is reported here. */
function onTreeNodeExpand(_data: WorkspaceTreeNode, node: DecorationTreeNode) {
  const entry = loadedDirectoryOf(node);
  if (entry) fileDecorations.directoryLoaded(entry.projectRoot, entry.directory, entry.names);
}

async function confirmDiscardWorkspace() {
  flushEditorContent();
  if (!project.dirtyCount) return true;
  try {
    await ElMessageBox.confirm(t("project.confirm.switchProject"), t("project.unsavedTitle"), {
      type: "warning",
      confirmButtonText: t("project.discard"),
      cancelButtonText: t("common.cancel"),
    });
    return true;
  } catch {
    return false;
  }
}

const browserAccess = useBrowserProjectAccess(project);
const browserAccessBanner = browserAccess.banner;

async function openProject(path = projectPathInput.value, preserveDocuments = false) {
  const normalized = path.trim();
  if (!normalized) return;
  if (!canInvoke("local_fs_list_dir")) {
    errorKey.value = "project.desktopOnly";
    return;
  }
  if (browserAccess.isNativePathInBrowser(normalized)) {
    errorKey.value = "project.browser.pathDesktopOnly";
    return;
  }
  loading.value = true;
  errorKey.value = null;
  try {
    // 刷新后浏览器把文件夹权限重置为 prompt：只登记项目，由顶部「重新授权」按钮在用户点击时恢复。
    if (!(await browserAccess.check(normalized))) {
      project.restoreProject(normalized);
      projectPathInput.value = normalized;
      setProjectTreeRoots();
      return;
    }
    await readDirectory(normalized, null);
    // 项目切换只是改变当前文件树上下文；已打开标签及其未保存内容继续留在工作区。
    if (project.projectRoot !== normalized || preserveDocuments) project.restoreProject(normalized);
    await loadProjectTree(normalized);
    projectPathInput.value = normalized;
    if (project.activePath) await ensureDocumentLoaded(project.activePath);
  } catch (error) {
    setProjectTreeRoots();
    errorKey.value = "project.openFailed";
    // 权限可能在打开途中被收回：刷新状态，必要时显示「重新授权」。
    browserAccess.noteFailure(normalized, error);
  } finally {
    loading.value = false;
  }
}

async function chooseProject() {
  if (!canInvoke("local_fs_list_dir")) {
    errorKey.value = "project.desktopOnly";
    return;
  }
  if (browserAccess.pickerUnsupported) {
    errorKey.value = "project.browser.unsupported";
    return;
  }
  const selected = await chooseNativeDirectory(t("project.chooseFolder"), undefined, { browserProject: true }).catch(
    (error: unknown) => {
      if (isDesktopRuntime()) throw error;
      errorKey.value = "project.openFailed";
      return null;
    }
  );
  if (selected) {
    projectPathInput.value = selected;
    await openProject(selected);
  }
}

/** 「重新授权」：requestPermission 必须在这次点击里调用，授权后（重新）打开该项目。 */
async function reauthorizeBrowserProject() {
  const root = browserAccessBanner.value?.root;
  if (!root) return;
  if (!(await browserAccess.reauthorize(root))) {
    errorKey.value = "project.browser.permissionDenied";
    return;
  }
  await openProject(root, true);
}

const boundWorktreePath = computed(() => workbench.activeWorktreePath);
const currentRootIsBound = computed(
  () => Boolean(project.projectRoot) && project.projectRoot === boundWorktreePath.value
);

/** 当前项目根是 worktree 时反查其主仓库，绑定需要同时记录仓库与 worktree。 */
async function resolveOwningRepo(root: string) {
  const entries = await listGitWorktrees(root);
  return {
    repoPath: resolveOwningRepoPath(entries, root),
    entry: entries.find((entry) => entry.is_current),
  };
}

async function bindRootToWorkspace(root: string) {
  const workspace = workbench.activeWorkspace;
  if (!root || !workspace) return;
  try {
    const { repoPath, entry } = await resolveOwningRepo(root);
    const binding = buildWorktreeBinding(repoPath, { path: root, branch: entry?.branch });
    if (!binding || !workbench.setWorkspaceWorktree(workspace.id, binding)) return;
    ElMessage.success(t("git.worktree.bindSuccess", { name: workspace.name }));
  } catch {
    ElMessage.error(t("project.git.bindFailed"));
  }
}

function bindProjectRootToWorkspace() {
  return bindRootToWorkspace(project.projectRoot);
}

async function bindNodeToWorkspace(node: WorkspaceTreeNode) {
  closeProjectContextMenu();
  await bindRootToWorkspace(node.projectRoot);
}

function unbindProjectRootFromWorkspace() {
  const workspace = workbench.activeWorkspace;
  if (!workspace) return;
  if (!workbench.clearWorkspaceWorktree(workspace.id)) return;
  ElMessage.success(t("git.worktree.unbindSuccess", { name: workspace.name }));
}

let projectIndexRefreshTimer: ReturnType<typeof setTimeout> | undefined;
let projectIndexRefreshGeneration = 0;

function scheduleProjectIndexRefresh(root = project.projectRoot, delay = 250) {
  if (!root || !isDesktopRuntime()) return;
  if (projectIndexRefreshTimer) clearTimeout(projectIndexRefreshTimer);
  const generation = ++projectIndexRefreshGeneration;
  projectIndexRefreshTimer = setTimeout(() => {
    projectIndexRefreshTimer = undefined;
    void refreshProjectFileIndex([root]).catch(() => undefined).finally(() => {
      if (generation === projectIndexRefreshGeneration) projectIndexRefreshTimer = undefined;
    });
  }, delay);
}

async function refreshProject() {
  if (!project.projectRoot) return;
  loading.value = true;
  errorKey.value = null;
  try {
    await refreshRepositoryBranches();
    await loadProjectTree(project.projectRoot);
    // The rebuilt tree reloads its open folders, which queues them again.
    await nextTick();
    fileDecorations.refreshExpanded(expandedTreeDirectories());
  } catch {
    errorKey.value = "project.openFailed";
  } finally {
    loading.value = false;
    scheduleProjectIndexRefresh(project.projectRoot);
  }
}

const documentLoader = useProjectDocumentLoader(project, async (path, projectRoot) => {
  const filePath = relativeProjectPath(path, projectRoot);
  return isImageDocument(path)
    ? invokeBackend<string>("local_fs_read_image_data_url", { projectRoot, filePath })
    : invokeBackend<string>("local_fs_read_text_file", { projectRoot, filePath });
});

/**
 * A paused frame's source outside the project (a JDK or dependency class): a
 * read-only document with the debug adapter's text. It stays open after the
 * session ends, like IDEA; it is never persisted (the source reference only
 * lives as long as the session) and never read from disk.
 */
const virtualSourceLoads = new Map<string, () => Promise<string>>();
function openVirtualSource(path: string, line: number, load: () => Promise<string>) {
  virtualSourceLoads.set(path, load);
  const existing = project.documents.find((item) => item.path === path);
  if (existing?.loaded) {
    project.revealDocumentPosition(path, line);
    return;
  }
  project.openDocument(path);
  void loadVirtualSource(path, line);
}
async function loadVirtualSource(path: string, line?: number) {
  const load = virtualSourceLoads.get(path);
  project.setDocumentLoading(path, true);
  try {
    if (!load) throw new Error("unavailable: the debug session of this source has ended");
    const content = await load();
    project.setDocumentLoaded(path, content, { external: true, externalKind: "debug-source" });
    if (line) project.revealDocumentPosition(path, line);
  } catch (error) {
    // A later retry asks the same session; once it ended the adapter has no source to give.
    project.setDocumentError(path, "project.readFailed", t("debug.source.unavailable", { error: debugErrorText(error) }));
  }
}

async function ensureDocumentLoaded(path: string, projectRoot = projectRootForPath(path)) {
  if (isVirtualSourceDocumentPath(path)) {
    await loadVirtualSource(path);
    return;
  }
  if (!canInvoke("local_fs_read_text_file")) return;
  const document = project.documents.find((item) => item.path === path);
  if (document?.external) {
    if (!document.loaded) await openExternalFile(path);
    return;
  }
  await documentLoader.load(path, projectRoot);
}

async function activateProject(root: string) {
  if (root === project.projectRoot) return true;
  project.restoreProject(root);
  projectPathInput.value = root;
  // 浏览器项目：刷新后可能尚未授权，查询后由横幅提示，而不是显示空文件夹。
  void browserAccess.check(root);
  return true;
}

/** Directory that hosts an external file for a custom editor binding (the file is its basename). */
function externalDocumentRoot(path: string) {
  const root = projectDirname(path);
  return /^[a-zA-Z]:$/.test(root) ? `${root}/` : root;
}

/**
 * The custom editor for a file opened from outside any project (OS double-click,
 * drop). A confirmed default always wins; otherwise only files the built-in
 * viewers cannot show (binary, e.g. Office documents) fall back to the first
 * matching plugin editor, unless the user pinned the text editor.
 */
async function externalCustomEditorFor(info: ExternalFileInfo) {
  if (!isDesktopRuntime()) return undefined;
  await customEditors.hydrate();
  const resolved = customEditors.resolveForPath(projectBasename(info.path));
  if (resolved.default && !resolved.needsConfirmation) return resolved.default;
  if (info.kind !== "binary" || resolved.source === "user" || info.textPreviewAvailable) return undefined;
  return resolved.default ?? resolved.options[0];
}

async function openExternalFile(path: string, options: { builtin?: boolean; preferProject?: boolean } = {}) {
  try {
    const info = await invokeBackend<ExternalFileInfo>("local_fs_inspect_external_file", { path });
    if (info.kind === "directory") {
      sideMode.value = "explorer";
      await openProject(info.path);
      return;
    }
    // A file handed over from outside (OS "open with", drop) that lies inside an
    // open project opens as a project document, so project features (plugin
    // language features, LSP, run configurations) apply. A tab already open as
    // an external file keeps that mode.
    const infoPath = info.path.replace(/\\/g, "/");
    const root = options.preferProject ? projectRootContaining(infoPath) : undefined;
    if (root && !project.documents.some((item) => item.path === infoPath && item.external)) {
      await openFile(infoPath, undefined, root);
      return;
    }
    const metadata = { external: true, externalKind: info.kind, mimeType: info.mimeType, size: info.size, textPreviewAvailable: info.textPreviewAvailable };
    const existing = project.documents.find((item) => item.path === info.path);
    if (existing?.editor && !options.builtin) {
      project.openDocument(info.path);
      return;
    }
    // Restored tabs already exist but have not selected an editor for this session.
    // Only preserve a loaded builtin tab or an explicit "reopen with text" choice.
    const editor = options.builtin || existing?.loaded ? undefined : await externalCustomEditorFor(info);
    if (editor) {
      project.setDocumentLoaded(info.path, "", metadata);
      openDocumentWithCustomEditor(info.path, editor);
      return;
    }
    project.openDocument(info.path);
    let content = "";
    if (info.kind === "text") {
      content = await invokeBackend<string>("local_fs_read_external_text_file", { path: info.path });
    } else if (info.kind === "image" || info.kind === "pdf") {
      content = await invokeBackend<string>("local_fs_read_external_data_url", { path: info.path });
    }
    project.setDocumentLoaded(info.path, content, metadata);
  } catch {
    project.setDocumentError(path, "project.readFailed");
    ElMessage.error(t("project.external.openFailed"));
  }
}

const projectViewActive = ref(true);
let projectViewDisposed = false;
onBeforeUnmount(() => { projectViewDisposed = true; projectViewActive.value = false; });

// Monaco navigates only inside the current model; go to definition, references
// and peek "open" into another file need the host to open it. Files inside an
// open project open as project documents, anything else (e.g. a language's
// standard library) as an external file. Other workspaces' editors are left alone.
function projectRootContaining(path: string): string | undefined {
  // Windows drive letters and paths compare case-insensitively (file URIs often lower-case the drive).
  const fold = (value: string) => (/^[A-Za-z]:\//.test(value) ? value.toLowerCase() : value);
  const target = fold(path);
  return [...new Set([project.projectRoot, ...project.projects.map((entry) => entry.root)])]
    .filter((root) => {
      const normalized = fold(root.replace(/\\/g, "/").replace(/\/+$/, ""));
      return Boolean(normalized) && (target === normalized || target.startsWith(`${normalized}/`));
    })
    .sort((a, b) => b.length - a.length)[0];
}
function openFromEditor(resource: Uri, target?: IRange | IPosition): boolean {
  if (projectViewDisposed || !projectViewActive.value || route.name !== "projects") return false;
  const path = fileUriPath(resource.toString());
  if (!path) return false;
  const line = target ? ("lineNumber" in target ? target.lineNumber : target.startLineNumber) : undefined;
  const column = target ? ("column" in target ? target.column : target.startColumn) : undefined;
  openPathAt(path, line, column);
  return true;
}
/** Opens an absolute (slash-separated) path as a project document, or as an external file outside the projects. */
function openPathAt(path: string, line?: number, column?: number) {
  // A debugger address has no file on disk; its source opens from the paused frame.
  if (isDebuggerAddressPath(path)) return;
  const root = projectRootContaining(path);
  if (root) {
    void openFile(path, line, root, column);
    return;
  }
  void openExternalFile(path, { builtin: true }).then(() => {
    const opened = project.activePath;
    if (opened && line) project.revealDocumentPosition(opened, line, column);
  });
}
// Registered when Monaco loads (with the first editor), not eagerly with the view.
const stopProjectEditorOpener = registerMonacoContributionHook((monaco) => {
  const opener = monaco.editor.registerEditorOpener({ openCodeEditor: (_source, resource, target) => openFromEditor(resource, target) });
  return () => opener.dispose();
});
onBeforeUnmount(stopProjectEditorOpener);
onActivated(() => {
  projectViewActive.value = true;
  void openPendingProjectFiles();
});
onDeactivated(() => { projectViewActive.value = false; });
/** This workspace has the project of root open. */
function ownsProjectRoot(root: string): boolean {
  return projectRootContaining(`${root.replace(/\\/g, "/").replace(/\/+$/, "")}/`) !== undefined;
}
const editorDebug = useProjectEditorDebug({
  activeDocument: () => activeDocument.value,
  projectRootForPath,
  languageIdFor: (path) => monacoEditor.value?.languageIdFor(path),
  editorReady: () => monacoEditor.value !== null,
  active: () => !projectViewDisposed && projectViewActive.value && route.name === "projects",
  ownsProject: ownsProjectRoot,
  openAt: (path, line) => openPathAt(path, line),
  openVirtualSource,
  evaluateText: () => monacoEditor.value?.evaluateText() ?? "",
  openDocumentPaths: () => project.documents.map((item) => item.path),
});
/**
 * The breakpoints dialog's "go to" for a breakpoint in a debugger-provided
 * source of the session's project: opened through that session when it is of
 * the address's debugger type and has not ended, else through the project's
 * newest live session of that type (addressSourceSession); without one, a hint.
 */
function openDebugAddress(sessionId: string, address: string, type: string, line: number) {
  const root = debugSessions.sessions.find((session) => session.id === sessionId)?.projectRoot;
  if (!root) return;
  const same = debugSessions.sessions.filter((session) => session.type === type && sameProjectRoot(session.projectRoot, root));
  const through = addressSourceSession(same, sessionId);
  if (through) editorDebug.openAddress(root, through.id, address, line);
  else ElMessage.info({ message: t("debug.addressBreakpoints.openNeedsSession", { type }), showClose: true, grouping: true });
}
/**
 * A debugger address in a test failure's stack trace: opened through a live
 * session of the run's debugger type in the project when there is one, else
 * through the run's own (possibly ended) session.
 */
function openTestTraceAddress(root: string, address: string, sessionId: string, type: string) {
  const same = debugSessions.sessions.filter((session) => session.type === type && sameProjectRoot(session.projectRoot, root));
  editorDebug.openAddress(root, addressSourceSession(same, sessionId)?.id ?? sessionId, address, 1);
}
const debugViewActive = () => !projectViewDisposed && projectViewActive.value && route.name === "projects";
useDebugHover({
  active: debugViewActive,
  onInspect: (expression) => editorDebug.openEvaluate(expression, { evaluate: true }),
});

// ---- run toolbar and debug panel ----
const debugConfigs = useDebugConfigsStore();
const debugSessions = useDebugSessionsStore();
/** The project whose run configurations the toolbar offers: the active project file's, else the primary one. */
const runProjectRoot = computed(() => {
  const document = activeDocument.value;
  return document && !document.external ? projectRootForPath(document.path) : project.projectRoot;
});
const runActiveFile = computed(() => {
  const document = activeDocument.value;
  return document && !document.external ? document.path : undefined;
});
/** Opening the run configuration editor (rendered by the configuration dialog). */
const runConfigurationRequest = ref<{ mode: "edit" | "new"; projectRoot: string } | null>(null);
function requestRunConfigurations(mode: "edit" | "new") {
  runConfigurationRequest.value = { mode, projectRoot: runProjectRoot.value };
}
function closeRunConfigurations(visible: boolean) {
  if (!visible) runConfigurationRequest.value = null;
}
/** Whether a workspace other than this one still lists root. */
function listedElsewhere(root: string): boolean {
  return project.rootsListedByOtherWorkspaces().some((listed) => sameProjectRoot(listed, root));
}
const debugPanelCollapsed = ref(false);
const {
  tabs: debugPanelTabs,
  activeId: debugPanelActiveId,
  select: selectDebugPanelTab,
  launchFor: debugLaunchFor,
} = useDebugPanelTabs(ownsProjectRoot, () => {
  debugPanelCollapsed.value = false;
});
const debugPanelHeight = computed(() => debugConfigs.stateFor(project.projectRoot).panelHeight);
function setDebugPanelHeight(height: number) {
  if (project.projectRoot) debugConfigs.setPanelHeight(project.projectRoot, height);
}
function debugSessionState(id: string) {
  return debugPanelTabs.value.find((tab) => tab.id === id)?.state ?? "ended";
}
/** The text editor's cursor in a project file, for Run to Cursor. */
function debugCursorLocation() {
  const document = activeDocument.value;
  if (!document || document.external || !document.loaded || document.editor) return null;
  const line = monacoEditor.value?.cursorLine() ?? null;
  return line ? { path: document.path, line } : null;
}
/** Monaco language id of the run toolbar's active file (a project without configurations gets one for it). */
function runActiveLanguageId(): string | undefined {
  const path = runActiveFile.value;
  return path ? monacoEditor.value?.languageIdFor(path) : undefined;
}
useDebugCommands({
  projectRoot: () => runProjectRoot.value,
  activeFile: () => runActiveFile.value,
  activeLanguageId: runActiveLanguageId,
  ownsProject: ownsProjectRoot,
  cursor: debugCursorLocation,
  toggleBreakpoint: (line) => editorDebug.toggleBreakpoint(line),
  openEvaluate: () => editorDebug.openEvaluate(),
  newConfiguration: () => requestRunConfigurations("new"),
});
// Install commands confirmed in the missing-debugger-tools dialog run in a new project terminal tab here.
useActiveViewCommands(() => [
  setDebugTerminalOpener(({ command, cwd, title }) => openProjectTerminalTab(cwd, title, command)),
  // Debug popups at a line of code (Smart Step Into) open below it when the text editor shows it.
  setDebugLineAnchorProvider((path, line, column) => {
    const document = activeDocument.value;
    if (!document || !document.loaded || document.editor || !sameDebugDocumentPath(document.path, path)) return null;
    return monacoEditor.value?.screenPositionOf(line, column) ?? null;
  }),
]);
/** Closing the Evaluate dialog returns the keyboard to the editor (⌥F8, step, ⌥F8 …). */
function closeEvaluateDialog() {
  editorDebug.closeEvaluate();
  void nextTick(() => monacoEditor.value?.focus());
}
const autoSave = useProjectAutoSave({
  active: projectViewActive,
  scope: () => editorScope.scopeId.value,
  documents: () => project.documents,
  preferences: () => project.editorPreferences,
  enabled: () => canInvoke("local_fs_write_text_file") && project.activeWorkspaceId === editorScope.scopeId.value,
  editable: (doc) => !(isComparingDocument.value && doc.path === project.activePath),
  save: (path, valid) => saveDocument(path, valid),
});
watch(() => project.activePath, (path, previous) => {
  if (previous && path !== previous) autoSave.departure(previous);
}, { flush: "sync" });
let projectViewReady = false;
let openingPendingProjectFiles = false;

async function openPendingProjectFiles() {
  if (!projectViewReady || !projectViewActive.value || route.name !== "projects" || openingPendingProjectFiles) return;
  openingPendingProjectFiles = true;
  try {
    while (projectViewActive.value && route.name === "projects" && pendingProjectFiles.value.length) {
      for (const path of consumeProjectFiles()) await openExternalFile(path, { preferProject: true });
    }
  } finally {
    openingPendingProjectFiles = false;
  }
}

async function openActiveDocumentWithSystem() {
  const path = activeDocument.value?.path;
  if (!path) return;
  try {
    await invokeBackend<void>("local_fs_open_external_file", { path });
  } catch {
    ElMessage.error(t("project.external.systemOpenFailed"));
  }
}

async function openFile(
  path: string,
  line?: number,
  projectRoot = projectRootForPath(path),
  column?: number,
) {
  const documentPath = toDocumentPath(projectRoot, path);
  errorKey.value = null;
  activeProjectTerminalId.value = null;
  // A plain open (no position) of a file that is not open yet may go to a plugin custom editor.
  if (line === undefined && column === undefined && !project.documents.some((item) => item.path === documentPath)) {
    if (pendingCustomEditorOpens.has(documentPath)) return;
    pendingCustomEditorOpens.add(documentPath);
    try {
      const editor = await chooseCustomEditorForOpen(documentPath, projectRoot);
      if (editor === null) return;
      if (editor) {
        activeProjectTerminalId.value = null;
        openDocumentWithCustomEditor(documentPath, editor);
        return;
      }
    } finally {
      pendingCustomEditorOpens.delete(documentPath);
    }
    activeProjectTerminalId.value = null;
  }
  project.openDocument(documentPath);
  project.revealDocumentPosition(documentPath, line, column);
  await ensureDocumentLoaded(documentPath, projectRoot);
}

// ---- Plugin custom editors (contributes.customEditors) ----

const pendingCustomEditorOpens = new Set<string>();

/**
 * Path a custom editor binds for a document: project-relative inside an open
 * project, or the basename (under its directory) for an external file.
 */
function customEditorRelativePath(documentPath: string, projectRoot = projectRootForPath(documentPath)) {
  // A debugger source is the adapter's text, not a file a plugin editor could open.
  if (isVirtualSourceDocumentPath(documentPath)) return undefined;
  if (project.documents.some((item) => item.path === documentPath && item.external)) return projectBasename(documentPath);
  if (!project.projects.some((entry) => entry.root === projectRoot) && projectRoot !== project.projectRoot) return undefined;
  const relative = relativeProjectPath(documentPath, projectRoot);
  return relative && relative !== documentPath.replace(/\\/g, "/") ? relative : undefined;
}

/**
 * The custom editor a plain open should use: `undefined` → text editor,
 * `null` → the user cancelled. A plugin `default` asks once; sensitive files
 * never open in a plugin by default (resolveEditorsForPath).
 */
async function chooseCustomEditorForOpen(
  documentPath: string,
  projectRoot: string,
): Promise<CustomEditorContribution | undefined | null> {
  if (!isDesktopRuntime()) return undefined;
  const relativePath = customEditorRelativePath(documentPath, projectRoot);
  if (!relativePath) return undefined;
  await customEditors.hydrate();
  const resolved = customEditors.resolveForPath(relativePath);
  if (!resolved.default) return undefined;
  if (!resolved.needsConfirmation) return resolved.default;
  return confirmCustomEditorDefault(resolved.default, relativePath);
}

async function confirmCustomEditorDefault(editor: CustomEditorContribution, relativePath: string) {
  const pattern = associationPatternForPath(relativePath);
  const title = customEditorTitle(editor, localeChain.value);
  const remember = ref(false);
  let choice: "plugin" | "text" | "cancel";
  try {
    await ElMessageBox.confirm(
      () => h("div", { class: "project-custom-editor-confirm" }, [
        h("p", t("pluginContrib.customEditors.defaultConfirmDescription", {
          plugin: editor.pluginName,
          name: projectBasename(relativePath),
          editor: title,
          writes: editor.access === "readWrite" ? t("pluginContrib.customEditors.defaultConfirmWrites") : "",
        })),
        h(ElCheckbox, {
          modelValue: remember.value,
          "onUpdate:modelValue": (value: string | number | boolean) => {
            remember.value = Boolean(value);
          },
        }, () => t("pluginContrib.customEditors.remember", { pattern })),
      ]),
      t("pluginContrib.customEditors.defaultConfirmTitle"),
      {
        type: "info",
        distinguishCancelAndClose: true,
        confirmButtonText: t("pluginContrib.customEditors.defaultConfirmOpen", { editor: title }),
        cancelButtonText: t("pluginContrib.customEditors.defaultConfirmText"),
      },
    );
    choice = "plugin";
  } catch (action) {
    choice = action === "cancel" ? "text" : "cancel";
  }
  if (choice === "cancel") return null;
  if (remember.value) {
    try {
      if (choice === "plugin") await customEditors.confirmDefault(editor.key);
      else await customEditors.declineDefault(pattern);
    } catch {
      errorKey.value = "project.operationFailed";
    }
  }
  return choice === "plugin" ? editor : undefined;
}

function openDocumentWithCustomEditor(documentPath: string, editor: Pick<CustomEditorContribution, "pluginId" | "id">) {
  project.openDocument(documentPath);
  project.setDocumentCustomEditor(documentPath, { pluginId: editor.pluginId, editorId: editor.id });
}

function documentEditorKey(document: ProjectEditorDocument | undefined) {
  return document?.editor ? customEditorKey(document.editor.pluginId, document.editor.editorId) : TEXT_EDITOR_ASSOCIATION;
}

/** Switches an open document between the text editor and a custom editor (Open/Reopen With…). */
async function reopenDocumentWith(path: string, key: string) {
  flushEditorContent();
  const document = project.documents.find((item) => item.path === path);
  if (!document || documentEditorKey(document) === key) {
    if (document) selectProjectDocument(path);
    return;
  }
  const editor = key === TEXT_EDITOR_ASSOCIATION ? undefined : customEditors.editorByKey(key);
  if (key !== TEXT_EDITOR_ASSOCIATION && (!editor || !parseCustomEditorKey(key))) return;
  if (isProjectDocumentDirty(document)) {
    try {
      await ElMessageBox.confirm(t("project.confirm.closeDirty"), t("project.unsavedTitle"), {
        type: "warning",
        confirmButtonText: t("project.discard"),
        cancelButtonText: t("common.cancel"),
      });
    } catch {
      return;
    }
  }
  activeProjectTerminalId.value = null;
  if (editor) {
    openDocumentWithCustomEditor(path, editor);
    return;
  }
  await reopenDocumentWithBuiltinEditor(path, Boolean(document.external));
}

async function reopenDocumentWithBuiltinEditor(path: string, external: boolean) {
  project.setDocumentCustomEditor(path, null);
  project.openDocument(path);
  if (external) await openExternalFile(path, { builtin: true });
  else await ensureDocumentLoaded(path);
}

function openDocumentEditorKey(path: string) {
  const document = project.documents.find((item) => item.path === path);
  return document ? documentEditorKey(document) : undefined;
}

function openContextNodeWith(key: string) {
  const node = contextMenu.value?.node;
  if (node) void openNodeWith(node, key);
}

/** Tree "Open With…": opens (or switches) the file in the chosen editor, bypassing the default. */
async function openNodeWith(node: WorkspaceTreeNode, key: string) {
  closeProjectContextMenu();
  const documentPath = nodeAbsolutePath(node);
  errorKey.value = null;
  if (project.documents.some((item) => item.path === documentPath)) {
    await reopenDocumentWith(documentPath, key);
    return;
  }
  activeProjectTerminalId.value = null;
  if (key === TEXT_EDITOR_ASSOCIATION) {
    project.openDocument(documentPath);
    await ensureDocumentLoaded(documentPath, node.projectRoot);
    return;
  }
  const editor = customEditors.editorByKey(key);
  if (editor) openDocumentWithCustomEditor(documentPath, editor);
}

function documentCustomEditorPattern(path: string) {
  const relativePath = customEditorRelativePath(path);
  return relativePath ? associationPatternForPath(relativePath) : undefined;
}

/** Whether the tab menu offers editor choices for this document. */
function documentHasCustomEditors(path: string) {
  const relativePath = customEditorRelativePath(path);
  return Boolean(relativePath && customEditors.resolveForPath(relativePath).options.length);
}

function documentHasEditorAssociation(path: string) {
  const relativePath = customEditorRelativePath(path);
  const pattern = documentCustomEditorPattern(path);
  return Boolean(relativePath && pattern && associationForPath(customEditors.associations, relativePath)?.pattern === pattern);
}

async function setDocumentDefaultEditor(path: string, reset: boolean) {
  const document = project.documents.find((item) => item.path === path);
  const pattern = documentCustomEditorPattern(path);
  if (!document || !pattern) return;
  try {
    if (reset) {
      await customEditors.setAssociation(pattern, null);
      ElMessage.success(t("pluginContrib.customEditors.resetDefaultDone", { pattern }));
      return;
    }
    const key = documentEditorKey(document);
    await customEditors.setAssociation(pattern, key);
    const editor = customEditors.editorByKey(key);
    ElMessage.success(t("pluginContrib.customEditors.setDefaultDone", {
      editor: editor ? customEditorTitle(editor, localeChain.value) : t("pluginContrib.customEditors.textEditor"),
      pattern,
    }));
  } catch {
    errorKey.value = "project.operationFailed";
  }
}

function documentEditorSource(document: ProjectEditorDocument) {
  if (!document.editor) return document.path;
  const editor = customEditors.editorByKey(documentEditorKey(document));
  return editor
    ? `${document.path}\n${t("pluginContrib.customEditors.source", { editor: customEditorTitle(editor, localeChain.value), plugin: editor.pluginName })}`
    : document.path;
}

/** Asks a custom editor pane to save; the plugin page answers with a save-result. */
async function saveCustomEditorDocument(document: ProjectEditorDocument) {
  try {
    if (!customEditorPanes.value) throw new Error(t("pluginContrib.customEditors.unavailable"));
    await customEditorPanes.value.requestSave(document.path);
    if (customEditors.editorByKey(documentEditorKey(document))?.access === "readWrite") {
      ElMessage.success(t("project.saved"));
    }
    return true;
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    if (message !== t("pluginContrib.customEditors.saveConflict")) {
      ElMessage.error(message || t("pluginContrib.customEditors.saveFailed"));
    }
    return false;
  }
}

function onCustomEditorDirty(path: string, dirty: boolean) {
  project.setDocumentDirty(path, dirty);
}

function onCustomEditorFailed(path: string, _description: string) {
  const document = project.documents.find((item) => item.path === path);
  if (!document || !isProjectDocumentDirty(document)) return;
  project.setDocumentDirty(path, false);
  ElMessage.warning(t("pluginContrib.customEditors.unsavedLost", { name: document.name }));
}

async function onCustomEditorReopenText(path: string) {
  const external = Boolean(project.documents.find((item) => item.path === path)?.external);
  await reopenDocumentWithBuiltinEditor(path, external);
}

function onCustomEditorClose(path: string) {
  project.closeDocument(path);
  recordClosedDocument(path);
}

async function onTreeNodeClick(data: WorkspaceTreeNode) {
  // 仅当点击项目根节点时才激活项目，避免点击文件夹时触发不必要的项目切换逻辑
  if (data.isProjectRoot && !(await activateProject(data.projectRoot))) return;
  resourceGitSelection.value = data;
  selectedNode.value = data.isProjectRoot ? null : data;
  if (!data.isDir) await openFile(data.path, undefined, data.projectRoot);
}

function nodeRelativePath(node: WorkspaceTreeNode) {
  return node.isProjectRoot ? "" : node.path;
}

function nodeAbsolutePath(node: WorkspaceTreeNode) {
  return toDocumentPath(node.projectRoot, nodeRelativePath(node));
}

function closeProjectContextMenu() {
  contextMenu.value = null;
}

/** 项目以根路径关联笔记；名称取项目列表里的显示名。 */
function openProjectNotes(node: WorkspaceTreeNode, createNew = false) {
  closeProjectContextMenu();
  const name = project.projects.find((entry) => entry.root === node.projectRoot)?.name ?? node.name;
  void linkedNotes.open({ objectType: "project", objectId: node.projectRoot, nameSnapshot: name }, { createNew });
}

/** Plugin menu target (contributes.menus project/tree/context) of a tree node. */
function projectTreeMenuTarget(node: WorkspaceTreeNode): ProjectTreeMenuTarget {
  return {
    projectName: node.projectRoot.split(/[\\/]/).filter(Boolean).pop() ?? node.projectRoot,
    relativePath: nodeRelativePath(node),
    kind: node.isDir ? "directory" : "file",
    isProjectRoot: Boolean(node.isProjectRoot),
    path: nodeAbsolutePath(node),
  };
}

const projectTreePluginMenuHost = computed(() =>
  contextMenu.value ? projectTreeHostContext(projectTreeMenuTarget(contextMenu.value.node)) : null,
);

function projectTreePluginMenuContext() {
  const node = contextMenu.value?.node;
  return node ? menuPassContext("project/tree/context", projectTreeMenuTarget(node)) : {};
}

/** What the tree context menu hands to plugin services (fileMenu surface); directories and a closed menu take none. */
function projectTreeServiceTarget(): FileMenuServiceTarget | null {
  const node = contextMenu.value?.node;
  if (!node || node.isDir || node.isProjectRoot) return null;
  return { surface: "fileMenu", files: [{ name: node.name, absolutePath: nodeAbsolutePath(node) }] };
}

async function removeProjectFromContextMenu(node: WorkspaceTreeNode) {
  closeProjectContextMenu();
  await removeSavedProject(node.projectRoot);
}

/** The collapsed "Open With…" entry adds one row to a file's context menu. */
function customEditorMenuExtraHeight(node: WorkspaceTreeNode) {
  const relativePath = customEditorRelativePath(nodeAbsolutePath(node), node.projectRoot);
  return relativePath && customEditors.resolveForPath(relativePath).options.length ? 42 : 0;
}

function onTreeNodeContextMenu(event: MouseEvent, data: WorkspaceTreeNode) {
  event.preventDefault();
  selectedNode.value = data.isProjectRoot ? null : data;
  const menuWidth = 226;
  // 高度按各分支实际渲染的菜单项数估算，仅用于贴边时的翻转夹紧；
  // 增删菜单项后必须同步，否则底部右键会被夹到错误位置。
  const menuHeight = data.isDir ? (data.isProjectRoot ? 632 : 644) : 518 + customEditorMenuExtraHeight(data);
  // 项目根菜单显示关联笔记数。
  if (data.isProjectRoot) linkedNotes.ensureCounts("project", [data.projectRoot]);
  contextMenu.value = {
    node: data,
    x: Math.max(8, Math.min(event.clientX, window.innerWidth - menuWidth - 8)),
    y: Math.max(8, Math.min(event.clientY, window.innerHeight - menuHeight - 8)),
  };
}

async function activateContextNode(node: WorkspaceTreeNode) {
  if (!(await activateProject(node.projectRoot))) return false;
  selectedNode.value = node.isProjectRoot ? null : node;
  return true;
}

async function revealNode(node: WorkspaceTreeNode) {
  closeProjectContextMenu();
  if (!isDesktopRuntime()) {
    errorKey.value = "project.browser.desktopOnly";
    return;
  }
  try {
    if (node.isProjectRoot) {
      await invokeBackend<void>("local_fs_show_in_file_manager", { path: node.projectRoot });
    } else {
      await invokeBackend<void>("local_fs_show_in_explorer", {
        projectRoot: node.projectRoot,
        path: node.path,
      });
    }
  } catch {
    errorKey.value = "project.operationFailed";
  }
}

async function copyNodePath(node: WorkspaceTreeNode, relative = false) {
  closeProjectContextMenu();
  const path = relative
    ? nodeRelativePath(node) || projectBasename(node.projectRoot)
    : nodeAbsolutePath(node);
  try {
    await copyText(path);
    errorKey.value = null;
    ElMessage.success(t("project.context.pathCopied"));
  } catch {
    errorKey.value = "project.operationFailed";
  }
}

async function copyNodeToClipboard(node: WorkspaceTreeNode) {
  closeProjectContextMenu();
  copiedFileNode.value = node;
  if (isDesktopRuntime()) {
    try {
      await invokeBackend<void>("clipboard_write_file_urls", { paths: [nodeAbsolutePath(node)] });
    } catch {
      // 系统剪贴板写入失败时应用内复制粘贴仍可用，不打断操作
    }
  }
  ElMessage.success(t("project.context.fileCopied"));
}

async function pasteFilesInto(projectRoot: string, destDir: string) {
  if (!isDesktopRuntime()) {
    errorKey.value = "project.desktopOnly";
    return;
  }
  if (!(await activateProject(projectRoot))) return;
  const copied = copiedFileNode.value;
  let srcPaths: string[];
  if (copied) {
    srcPaths = [nodeAbsolutePath(copied)];
  } else {
    try {
      srcPaths = await invokeBackend<string[]>("clipboard_read_file_urls");
    } catch {
      srcPaths = [];
    }
  }
  if (!srcPaths.length) {
    ElMessage.info(t("project.context.clipboardNoFiles"));
    return;
  }
  try {
    await invokeBackend<void>("local_fs_paste_files", { projectRoot, destDir, srcPaths });
    await loadProjectTree(projectRoot);
    scheduleProjectIndexRefresh(projectRoot);
  } catch {
    errorKey.value = "project.operationFailed";
  }
}

async function pasteFilesIntoNode(node: WorkspaceTreeNode) {
  closeProjectContextMenu();
  if (!node.isDir) return;
  await pasteFilesInto(node.projectRoot, nodeRelativePath(node));
}

// ⌘/Ctrl+C/V on the selected tree node run through the keybinding dispatcher
// (keymap commands project:copy-node / project:paste-node). The dispatcher
// never takes these chords from inputs or Monaco; `tree.selection` also
// excludes the terminal and a live DOM text selection keeps native copy.
function pasteIntoSelectedNode() {
  const node = selectedNode.value;
  if (!node) return;
  const destDir = node.isDir ? node.path : projectDirname(node.path);
  void pasteFilesInto(node.projectRoot, destDir);
}

let projectClipboardDisposers: Array<() => void> = [];
function startProjectClipboardHotkey() {
  if (projectClipboardDisposers.length) return;
  projectClipboardDisposers = [
    registerKeybindingContext("tree.selection", (target) =>
      Boolean(selectedNode.value) && !target?.closest("input, textarea, [contenteditable], .monaco-editor, .xterm")),
    registerCommand({
      id: "project:copy-node",
      source: "builtin",
      title: () => t("keybindings.command.project:copy-node"),
      showInPalette: false,
      execute: () => {
        const node = selectedNode.value;
        if (node) void copyNodeToClipboard(node);
      },
    }),
    registerCommand({
      id: "project:paste-node",
      source: "builtin",
      title: () => t("keybindings.command.project:paste-node"),
      showInPalette: false,
      execute: pasteIntoSelectedNode,
    }),
  ];
}

function stopProjectClipboardHotkey() {
  for (const dispose of projectClipboardDisposers) dispose();
  projectClipboardDisposers = [];
}

function formatAiContextBytes(bytes: number) {
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KB", "MB", "GB"];
  let value = bytes / 1024;
  let unitIndex = 0;
  while (value >= 1024 && unitIndex < units.length - 1) {
    value /= 1024;
    unitIndex += 1;
  }
  return `${value >= 10 ? value.toFixed(0) : value.toFixed(1)} ${units[unitIndex]}`;
}

async function addNodeToAi(node: WorkspaceTreeNode) {
  closeProjectContextMenu();
  if (!isDesktopRuntime()) {
    ElMessage.warning(t("project.context.addToAiNativeOnly"));
    return;
  }
  try {
    const relativePath = nodeRelativePath(node);
    const target = {
      workspaceScopeId: assistant.activeWorkspaceScopeId,
      conversationId: assistant.activeConversationId,
    };
    let context: ProjectContextAttachmentResult;
    let scannedEntries = 0;
    if (node.isDir) {
      const tree = await invokeBackend<ProjectContextTreeResult>("local_fs_list_ai_context_tree", {
        projectRoot: node.projectRoot,
        dirPath: relativePath,
      });
      scannedEntries = tree.entries.length;
      context = createProjectDirectoryContext(
        relativePath,
        tree,
        projectBasename(node.projectRoot),
        node.projectRoot,
      );
    } else {
      const file = await invokeBackend<ProjectContextFileResult>("local_fs_read_ai_context_text_file", {
        projectRoot: node.projectRoot,
        filePath: relativePath,
      });
      context = createProjectFileContext(relativePath, file, node.projectRoot);
    }
    if (!assistant.addContextAttachment(context.attachment, target)) return;
    if (!context.truncated) {
      ElMessage.success(t("project.context.addedToAi", { name: context.attachment.name }));
      return;
    }
    const messageKey = node.isDir
      ? "project.context.addedDirectoryToAiCompacted"
      : "project.context.addedFileToAiCompacted";
    ElMessage.success(t(messageKey, {
      name: context.attachment.name,
      originalSize: formatAiContextBytes(context.originalSize),
      attachedSize: formatAiContextBytes(context.attachedSize),
      limit: formatAiContextBytes(MAX_AI_TEXT_ATTACHMENT_BYTES),
      entryCount: String(scannedEntries),
      entryLimit: String(MAX_AI_CONTEXT_TREE_ENTRIES),
    }));
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    ElMessage.error(message.includes("too_large") || message === "TEXT_ATTACHMENT_TOO_LARGE"
      ? t("project.context.aiContextTooLarge")
      : t("project.context.addToAiFailed"));
  }
}

function openTerminalInProject(node: WorkspaceTreeNode) {
  closeProjectContextMenu();
  openProjectTerminalTab(nodeAbsolutePath(node), node.name);
}

/** Opens a new project terminal tab in cwd; initialCommand runs once its shell is ready. */
function openProjectTerminalTab(cwd: string, title: string, initialCommand?: string) {
  const id = `project-terminal-${crypto.randomUUID()}`;
  projectTerminalTabs.value.push({
    id,
    name: `${t("nav.terminal")} · ${title}`,
    cwd,
  });
  activeProjectTerminalId.value = id;
  terminalWorkspace.activateTab(id, initialCommand ? { initialCwd: cwd, initialCommand } : { initialCwd: cwd });
}

function selectProjectTerminal(id: string) {
  activeProjectTerminalId.value = id;
  terminalWorkspace.activateTab(id);
}

function terminalPaneIds(node: TerminalPaneNode): string[] {
  return node.type === "split" ? node.children.flatMap(terminalPaneIds) : [node.id];
}

async function closeProjectTerminalSession(paneId: string) {
  deleteTerminalSnapshot(paneId);
  if (!isDesktopRuntime()) return;
  await invokeBackend<void>("close_local_shell", { payload: { sessionId: paneId } }).catch(
    () => undefined
  );
}

async function closeProjectTerminalPane(paneId: string) {
  await closeProjectTerminalSession(paneId);
  terminalWorkspace.closePane(paneId);
}

async function reloadProjectTerminalPane(paneId: string) {
  await closeProjectTerminalSession(paneId);
  terminalWorkspace.reloadPane(paneId);
}

// Explicit user choice from a blocked plugin-profile pane ("Open default shell"),
// as in the terminal workspace: close whatever the session left behind and start
// the default shell in the same pane.
async function useDefaultShellInProjectTerminalPane(paneId: string) {
  await closeProjectTerminalSession(paneId);
  terminalWorkspace.replaceProfilePaneWithShell(paneId);
}

function splitProjectTerminalPane(paneId: string, direction: TerminalSplitDirection) {
  if (
    !terminalWorkspace.splitPane(paneId, direction, {
      initialCwd: activeProjectTerminal.value?.cwd,
    })
  ) {
    ElMessage.warning(t("terminal.maxPanes"));
  }
}

function selectProjectDocument(path: string) {
  activeProjectTerminalId.value = null;
  project.activePath = path;
  // 切换标签时在文件树定位当前文件，但不强制切换左侧面板。
  void revealDocumentInTree(path);
}

async function closeProjectTerminal(id: string) {
  const index = projectTerminalTabs.value.findIndex((terminal) => terminal.id === id);
  if (index < 0) return;

  const currentId = activeProjectTerminalId.value;
  terminalWorkspace.activateTab(id);
  await Promise.all(terminalPaneIds(terminalWorkspace.root).map(closeProjectTerminalSession));
  terminalWorkspace.discardTab(id);
  projectTerminalTabs.value.splice(index, 1);

  const nextId =
    currentId === id
      ? (projectTerminalTabs.value[index]?.id ?? projectTerminalTabs.value[index - 1]?.id ?? null)
      : currentId;
  activeProjectTerminalId.value = nextId;
  if (nextId) terminalWorkspace.activateTab(nextId);
}

function openTerminalAtNode(node: WorkspaceTreeNode) {
  closeProjectContextMenu();
  const terminalCount = workbench.workspaceTabs.filter((tab) => tab.view === "terminal").length;
  const tab = workbench.createTab(
    "terminal",
    terminalCount ? `${t("nav.terminal")} ${terminalCount + 1}` : undefined
  );
  if (!tab) return;
  terminalWorkspace.activateTab(tab.id, { initialCwd: nodeAbsolutePath(node) });
  void router.push({ name: "terminal" });
}

async function searchInNode(node: WorkspaceTreeNode) {
  closeProjectContextMenu();
  if (!(await activateContextNode(node))) return;
  searchAllProjects.value = false;
  searchScope.value = {
    projectRoot: node.projectRoot,
    path: nodeRelativePath(node),
    label: node.isProjectRoot ? node.name : node.path,
  };
  sideMode.value = "search";
  clearReplacePreview();
  if (searchQuery.value.trim()) await runSearch();
  else
    await nextTick(() =>
      document.querySelector<HTMLInputElement>(".project-search-input input")?.focus()
    );
}

async function createFileInNode(node: WorkspaceTreeNode) {
  closeProjectContextMenu();
  if (!(await activateContextNode(node))) return;
  await createFile();
}

async function createFolderInNode(node: WorkspaceTreeNode) {
  closeProjectContextMenu();
  if (!(await activateContextNode(node))) return;
  await createFolder();
}

async function renameNode(node: WorkspaceTreeNode) {
  closeProjectContextMenu();
  if (node.isProjectRoot || !(await activateContextNode(node))) return;
  await renameSelected();
}

async function deleteNode(node: WorkspaceTreeNode) {
  closeProjectContextMenu();
  if (node.isProjectRoot || !(await activateContextNode(node))) return;
  await deleteSelected();
}

async function copyOrMoveNode(
  node: WorkspaceTreeNode,
  command: "local_fs_copy_to" | "local_fs_move_to"
) {
  closeProjectContextMenu();
  if (!isDesktopRuntime()) {
    errorKey.value = "project.desktopOnly";
    return;
  }
  const destination = await chooseNativeDirectory(t("project.context.chooseDestination"));
  if (!destination) return;

  const root = node.projectRoot.replace(/\\/g, "/").replace(/\/+$/, "");
  const destinationPath = destination.replace(/\\/g, "/").replace(/\/+$/, "");
  const isInsideProject = destinationPath === root || destinationPath.startsWith(`${root}/`);
  if (!isInsideProject) {
    ElMessage.warning(t("project.context.destinationOutsideProject"));
    return;
  }

  const destDir = relativeProjectPath(destinationPath, root);
  try {
    await invokeBackend<void>(command, {
      projectRoot: node.projectRoot,
      srcPath: nodeRelativePath(node),
      destDir,
    });
    if (command === "local_fs_move_to") {
      const oldPath = nodeAbsolutePath(node);
      const newPath = toDocumentPath(node.projectRoot, joinProjectPath(destDir, node.name));
      project.renameDocuments(oldPath, newPath, node.isDir);
    }
    await loadProjectTree(node.projectRoot);
  } catch {
    errorKey.value = "project.operationFailed";
  }
}

function normalizeDeploymentPath(path: string) {
  return path.replace(/\\/g, "/").replace(/^\/+|\/+$/g, "");
}

function deploymentScopeForNode(node: WorkspaceTreeNode) {
  return normalizeDeploymentPath(
    node.isDir ? nodeRelativePath(node) : projectDirname(nodeRelativePath(node))
  );
}

function splitDeploymentExcludes(value: string) {
  return value
    .split(/[\n,]/)
    .map((entry) => entry.trim())
    .filter(Boolean);
}

function shouldExcludeDeploymentPath(path: string, excludes: string[]) {
  const relativePath = normalizeDeploymentPath(path);
  const parts = relativePath.split("/").filter(Boolean);
  return excludes.some((raw) => {
    const rule = normalizeDeploymentPath(raw);
    if (!rule) return false;
    if (rule.startsWith("*."))
      return relativePath.toLowerCase().endsWith(rule.slice(1).toLowerCase());
    return parts.includes(rule) || relativePath === rule || relativePath.startsWith(`${rule}/`);
  });
}

function joinRemotePath(root: string, path: string) {
  const remoteRoot = root.replace(/\\/g, "/").replace(/\/+$/, "");
  const relativePath = path.replace(/\\/g, "/").replace(/^\/+/, "");
  if (!relativePath) return remoteRoot || "/";
  return !remoteRoot || remoteRoot === "/" ? `/${relativePath}` : `${remoteRoot}/${relativePath}`;
}

function parentRemotePath(path: string) {
  const normalized = path.replace(/\\/g, "/").replace(/\/+$/, "");
  const index = normalized.lastIndexOf("/");
  return index <= 0 ? "/" : normalized.slice(0, index);
}

function projectDeploymentSettings(projectRoot: string) {
  return deployments.value[projectRoot] ?? { byPath: {} };
}

function findDeploymentForPath(projectRoot: string, relativePath: string) {
  const settings = projectDeploymentSettings(projectRoot);
  const normalized = normalizeDeploymentPath(relativePath);
  const parts = normalized.split("/").filter(Boolean);
  for (let index = parts.length; index > 0; index -= 1) {
    const scopePath = parts.slice(0, index).join("/");
    const deployment = settings.byPath[scopePath];
    if (deployment) return { deployment, scopePath };
  }
  return { deployment: settings.default, scopePath: "" };
}

function parsePersistedDeployments(value: unknown): Record<string, ProjectDeploymentSettings> {
  if (!value || typeof value !== "object") return {};
  const stored = value as Partial<PersistedProjectDeployments>;
  if (stored.version !== 1 || !stored.byProjectRoot || typeof stored.byProjectRoot !== "object")
    return {};
  const result: Record<string, ProjectDeploymentSettings> = {};
  for (const [projectRoot, rawSettings] of Object.entries(stored.byProjectRoot)) {
    if (!rawSettings || typeof rawSettings !== "object") continue;
    const settings = rawSettings as Partial<ProjectDeploymentSettings>;
    const normalizeDeployment = (value: unknown): ProjectDeployment | undefined => {
      if (!value || typeof value !== "object") return undefined;
      const candidate = value as Partial<ProjectDeployment>;
      if (typeof candidate.sshProfileId !== "string" || typeof candidate.remoteRoot !== "string")
        return undefined;
      const sshProfileId = candidate.sshProfileId.trim();
      const remoteRoot = candidate.remoteRoot.trim();
      if (!sshProfileId || !remoteRoot) return undefined;
      return {
        sshProfileId,
        remoteRoot,
        excludes: Array.isArray(candidate.excludes)
          ? candidate.excludes.filter(
              (item): item is string => typeof item === "string" && Boolean(item.trim())
            )
          : [],
      };
    };
    const byPath: Record<string, ProjectDeployment> = {};
    if (settings.byPath && typeof settings.byPath === "object") {
      for (const [path, rawDeployment] of Object.entries(settings.byPath)) {
        const deployment = normalizeDeployment(rawDeployment);
        const scopePath = normalizeDeploymentPath(path);
        if (deployment && scopePath) byPath[scopePath] = deployment;
      }
    }
    const defaultDeployment = normalizeDeployment(settings.default);
    if (defaultDeployment || Object.keys(byPath).length) {
      result[projectRoot] = { default: defaultDeployment, byPath };
    }
  }
  return result;
}

async function persistDeployments() {
  await savePersistedValue(PROJECT_DEPLOYMENTS_STORAGE_KEY, {
    version: 1,
    byProjectRoot: deployments.value,
  });
}

async function openBranchPicker(node: WorkspaceTreeNode) {
  closeProjectContextMenu();
  if (node.isDir || !(await activateContextNode(node))) return;
  branchPickerNode.value = node;
  branchPickerBranches.value = null;
  branchPickerVisible.value = true;
  branchPickerLoading.value = true;
  try {
    branchPickerBranches.value = await invokeBackend<GitBranches>("git_list_branches", {
      repoPath: node.projectRoot,
    });
  } catch {
    ElMessage.error(t("project.git.branchesFailed"));
  } finally {
    branchPickerLoading.value = false;
  }
}

async function compareNodeWithRef(gitRef: string) {
  const node = branchPickerNode.value;
  branchPickerVisible.value = false;
  branchPickerNode.value = null;
  if (!node || !(await activateContextNode(node))) return;
  await openFile(node.path, undefined, node.projectRoot);
  await compareActiveDocumentWithRef(gitRef);
}

function openRemoteServerPicker(node: WorkspaceTreeNode) {
  closeProjectContextMenu();
  remoteServerNode.value = node;
  remoteServerProfileId.value = ssh.sortedProfiles[0]?.id ?? "";
  remoteServerPickerVisible.value = true;
}

async function openSelectedRemoteServer() {
  const profile = ssh.findProfile(remoteServerProfileId.value);
  if (!profile) {
    ElMessage.warning(t("project.remote.missingConnection"));
    return;
  }
  remoteServerPickerVisible.value = false;
  remoteServerNode.value = null;
  const tab = workbench.createTab("sftp", `${t("nav.sftp")} · ${profile.name}`);
  if (!tab) return;
  await router.push({ name: "sftp", query: { profile: profile.id } });
}

function openDeploymentDialog(node: WorkspaceTreeNode) {
  closeProjectContextMenu();
  const scopePath = deploymentScopeForNode(node);
  const existing = scopePath
    ? projectDeploymentSettings(node.projectRoot).byPath[scopePath]
    : projectDeploymentSettings(node.projectRoot).default;
  deploymentNode.value = node;
  deploymentProfileId.value = existing?.sshProfileId ?? ssh.sortedProfiles[0]?.id ?? "";
  deploymentRemoteRoot.value = existing?.remoteRoot ?? "";
  deploymentExcludes.value = (
    existing?.excludes?.length ? existing.excludes : DEFAULT_DEPLOYMENT_EXCLUDES
  ).join("\n");
  deploymentDialogVisible.value = true;
}

async function saveDeploymentConfig() {
  const node = deploymentNode.value;
  const sshProfileId = deploymentProfileId.value.trim();
  const remoteRoot = deploymentRemoteRoot.value.trim();
  if (!node || !sshProfileId || !remoteRoot) {
    ElMessage.warning(t("project.remote.configRequired"));
    return;
  }
  const scopePath = deploymentScopeForNode(node);
  const previous = projectDeploymentSettings(node.projectRoot);
  const deployment: ProjectDeployment = {
    sshProfileId,
    remoteRoot,
    excludes: splitDeploymentExcludes(deploymentExcludes.value),
  };
  deployments.value = {
    ...deployments.value,
    [node.projectRoot]: scopePath
      ? { ...previous, byPath: { ...previous.byPath, [scopePath]: deployment } }
      : { ...previous, default: deployment },
  };
  try {
    await persistDeployments();
    ElMessage.success(t("project.remote.configSaved"));
    deploymentDialogVisible.value = false;
    deploymentNode.value = null;
  } catch {
    errorKey.value = "project.operationFailed";
  }
}

async function listDeploymentFiles(projectRoot: string, path: string): Promise<string[]> {
  const entries = await invokeBackend<ProjectFileEntry[]>("local_fs_list_dir", {
    projectRoot,
    dirPath: path,
  });
  const files: string[] = [];
  for (const entry of entries) {
    if (entry.isDir) files.push(...(await listDeploymentFiles(projectRoot, entry.path)));
    else files.push(entry.path);
  }
  return files;
}

async function deployNode(node: WorkspaceTreeNode) {
  closeProjectContextMenu();
  if (!isDesktopRuntime() || deploying.value) return;
  const sourcePath = nodeRelativePath(node);
  const deploymentLookup = findDeploymentForPath(node.projectRoot, sourcePath);
  const deployment = deploymentLookup.deployment;
  if (!deployment) {
    openDeploymentDialog(
      node.isDir ? node : { ...node, path: projectDirname(node.path), isDir: true }
    );
    return;
  }
  const profile = ssh.findProfile(deployment.sshProfileId);
  if (!profile) {
    ElMessage.warning(t("project.remote.missingConnection"));
    openDeploymentDialog(
      node.isDir ? node : { ...node, path: projectDirname(node.path), isDir: true }
    );
    return;
  }
  const rawFiles = node.isDir
    ? await listDeploymentFiles(node.projectRoot, sourcePath)
    : [sourcePath];
  const files = rawFiles.filter((path) => !shouldExcludeDeploymentPath(path, deployment.excludes));
  if (!files.length) {
    ElMessage.info(t("project.remote.noFiles"));
    return;
  }
  const credentials = ssh.credentialsFor(profile.id);
  const sessionId = `project-deploy-${profile.id}-${Date.now()}`;
  deploying.value = true;
  try {
    await invokeBackend<void>("sftp_connect", {
      payload: {
        sessionId,
        host: profile.host,
        port: profile.port,
        username: profile.username,
        authType: profile.authType,
        password: credentials?.password,
        keyFile: profile.keyFile,
        passphrase: credentials?.passphrase,
      },
    });
    const createdDirs = new Set<string>();
    for (const filePath of files) {
      const scopedPath =
        deploymentLookup.scopePath && filePath.startsWith(`${deploymentLookup.scopePath}/`)
          ? filePath.slice(deploymentLookup.scopePath.length + 1)
          : filePath;
      const remotePath = joinRemotePath(deployment.remoteRoot, scopedPath);
      const remoteDir = parentRemotePath(remotePath);
      if (!createdDirs.has(remoteDir)) {
        await invokeBackend<void>("sftp_mkdirs", { sessionId, path: remoteDir });
        createdDirs.add(remoteDir);
      }
      await invokeBackend<void>("sftp_upload_file", {
        sessionId,
        localPath: nodeAbsolutePath({ ...node, path: filePath, isProjectRoot: false }),
        remotePath,
      });
    }
    ElMessage.success(t("project.remote.uploadSuccess", { count: String(files.length) }));
  } catch (error) {
    ElMessage.error(t("project.remote.uploadFailed", { error: String(error) }));
  } finally {
    await invokeBackend<void>("sftp_disconnect", { sessionId }).catch(() => undefined);
    deploying.value = false;
  }
}

function savedDocumentContent(path: string) {
  return project.documents.find((item) => item.path === path)?.savedContent;
}

async function saveDocument(path = project.activePath, automatic?: () => boolean): Promise<SaveOutcome> {
  if (!path) return "skipped";
  const scope = editorScope.capture();
  const owner = project.activeWorkspaceId;
  const original = project.documents.find((item) => item.path === path);
  if (!original || owner !== scope.scopeId) return "cancelled";
  const session = original.sessionId;
  const root = original.external ? undefined : projectRootForPath(path);
  const relative = root ? relativeProjectPath(path, root) : path;
  const external = Boolean(original.external);
  const preferences = { ...project.editorPreferences };
  const current = () => editorScope.isCurrent(scope) && project.activeWorkspaceId === owner &&
    !projectViewDisposed && (!automatic || projectViewActive.value) &&
    project.documentForSession(owner, path, session) !== undefined;
  const valid = () => current() && (!automatic || automatic());
  return projectSaveCoordinator.save(path, async () => {
    if (!valid() || !canInvoke("local_fs_write_text_file")) return "cancelled";
    const document = project.documentForSession(owner, path, session)!;
    flushEditorContent();
    if (document.editor) {
      if (automatic) return "skipped";
      return await saveCustomEditorDocument(document) ? "saved" : "failed";
    }
    if (!isEditableProjectText(document) || (automatic && !isProjectDocumentDirty(document))) return "skipped";
    const controller = new AbortController();
    const stop = watch(() => [editorScope.scopeId.value, project.activeWorkspaceId, projectViewDisposed, automatic ? projectViewActive.value : null,
      project.documentForSession(owner, path, session), automatic ? project.editorPreferences : null], () => {
      controller.abort();
    }, { flush: "sync" });
    const saveContext: EditorSaveContext = {
      path, projectRoot: root, external, signal: controller.signal, isCurrent: valid,
      formatOnSave: preferences.formatOnSave,
      formatterHandled: true,
      format: monacoEditor.value?.hasModel(path)
        ? (signal) => monacoEditor.value?.prepareSave(path, preferences, { path, projectRoot: root, external,
          signal, isCurrent: valid, formatOnSave: preferences.formatOnSave }) ?? Promise.resolve()
        : async (signal) => {
          const snapshot = document.content;
          const { prepareProjectSaveModel } = await import("@/services/project-save-model");
          if (!valid() || signal.aborted) return;
          const content = await prepareProjectSaveModel(snapshot, preferences, { path, projectRoot: root,
            external, signal, isCurrent: () => valid() && document.content === snapshot, formatOnSave: preferences.formatOnSave });
          if (content !== undefined && valid() && !signal.aborted && document.content === snapshot) {
            project.updateDocumentContent(path, content);
          }
        },
    };
    try {
      const failures = await runEditorSaveParticipants(saveContext);
      if (!valid() || controller.signal.aborted) return "cancelled";
      if (failures.length) ElMessage({ type: "warning", message: t("project.editor.formatFailed"), grouping: true });
      flushEditorContent();
      const content = document.content;
      const acknowledgeModel = monacoEditor.value?.captureSaveAcknowledgement(path);
      const commitBreakpointMoves = external ? undefined : monacoEditor.value?.captureBreakpointMoves(path);
      if (external) await invokeBackend<void>("local_fs_write_external_text_file", { path, content });
      else await invokeBackend<void>("local_fs_write_text_file", { projectRoot: root, filePath: relative, content });
      // New edits may still be model-only; reconcile before acknowledging the written snapshot.
      if (current()) flushEditorContent();
      project.acknowledgeDocumentSaved(owner, path, session, content);
      if (current()) {
        acknowledgeModel?.();
        commitBreakpointMoves?.();
        if (!external) editorDebug.refreshEntryPoints();
        if (!external) void lsp.notifyFileSaved(path);
        notifyEditorSaved({ path, projectRoot: root, external });
        if (!automatic) ElMessage.success(t("project.saved"));
      }
      return "saved";
    } catch {
      if (automatic) {
        const showFailure = valid();
        if (showFailure) autoSave.failed(path);
        if (showFailure) ElMessage({ type: "error", message: t("project.editor.autoSaveFailed", { name: document.name }), grouping: true });
      } else if (valid()) errorKey.value = "project.saveFailed";
      return "failed";
    } finally {
      stop();
      controller.abort();
    }
  }, automatic ? JSON.stringify([owner, session, path, project.editorPreferences.autoSave]) : undefined);
}

async function saveAllDocuments() {
  flushEditorContent();
  const scope = editorScope.capture();
  const owner = project.activeWorkspaceId;
  const documents = [...project.documents];
  for (const document of documents) {
    if (!editorScope.isCurrent(scope) || project.activeWorkspaceId !== owner) break;
    if (isProjectDocumentDirty(document)) {
      await saveDocument(document.path);
    }
  }
}

function removeProjectEditorTab(name: string | number) {
  if (typeof name !== "string") return;
  if (name.startsWith("terminal:")) {
    void closeProjectTerminal(name.slice("terminal:".length));
    return;
  }
  void requestCloseDocument(name);
}

async function requestCloseDocument(path: string) {
  const scope = editorScope.capture();
  const owner = project.activeWorkspaceId;
  const session = project.documents.find((doc) => doc.path === path)?.sessionId;
  await projectSaveCoordinator.wait(path);
  if (!editorScope.isCurrent(scope) || owner !== project.activeWorkspaceId || !project.documentForSession(owner, path, session)) return false;
  flushEditorContent();
  const document = project.documents.find((item) => item.path === path);
  if (document?.editor && isProjectDocumentDirty(document)) {
    if (!(await confirmCloseDirtyCustomEditor(document))) return false;
  } else if (document && isProjectDocumentDirty(document)) {
    try {
      await ElMessageBox.confirm(t("project.confirm.closeDirty"), t("project.unsavedTitle"), {
        type: "warning",
        confirmButtonText: t("project.discard"),
        cancelButtonText: t("common.cancel"),
      });
    } catch {
      return false;
    }
  }
  if (!editorScope.isCurrent(scope) || owner !== project.activeWorkspaceId || !project.documentForSession(owner, path, session)) return false;
  project.closeDocument(path);
  recordClosedDocument(path);
  return true;
}

/** Dirty custom editor close: Save (save-request to the plugin page), Discard, or keep the tab. */
async function confirmCloseDirtyCustomEditor(document: ProjectEditorDocument) {
  selectProjectDocument(document.path);
  try {
    await ElMessageBox.confirm(
      t("pluginContrib.customEditors.dirtyClose", { name: document.name }),
      t("project.unsavedTitle"),
      {
        type: "warning",
        distinguishCancelAndClose: true,
        confirmButtonText: t("common.save"),
        cancelButtonText: t("project.discard"),
      },
    );
  } catch (action) {
    return action === "cancel";
  }
  return saveCustomEditorDocument(document);
}

function recordClosedDocument(path: string) {
  const root = projectRootForPath(path);
  recordClosedEntry(workbench.activeWorkspaceId, {
    kind: "document",
    path,
    projectRoot: project.projects.some((entry) => entry.root === root) ? root : undefined,
  });
}

// The last save started from an editor (⌘S, Vim `:w`). Closing a tab waits for
// it, so Vim's `:wq` does not hit the unsaved-changes prompt mid-save.
let pendingEditorSave: Promise<SaveOutcome | void> = Promise.resolve();
function saveDocumentFromEditor() {
  pendingEditorSave = saveDocument();
  return pendingEditorSave;
}

function projectEditorTabIds() {
  return [
    ...project.documents.map((document) => document.path),
    ...projectTerminalTabs.value.map((terminal) => `terminal:${terminal.id}`),
  ];
}

async function closeProjectEditorTab(id: string) {
  const scope = editorScope.capture();
  const owner = project.activeWorkspaceId;
  const session = project.documents.find((doc) => doc.path === id)?.sessionId;
  await pendingEditorSave.catch(() => undefined);
  if (!editorScope.isCurrent(scope) || owner !== project.activeWorkspaceId) return;
  if (!id.startsWith("terminal:") && !project.documentForSession(owner, id, session)) return;
  if (id.startsWith("terminal:")) await closeProjectTerminal(id.slice("terminal:".length));
  else await requestCloseDocument(id);
}

/** Closes every open document, asking once when any has unsaved changes. */
async function closeAllDocumentsConfirmOnce() {
  const scope = editorScope.capture();
  const owner = project.activeWorkspaceId;
  const documents = [...project.documents];
  const current = () => !projectViewDisposed && editorScope.isCurrent(scope) && owner === project.activeWorkspaceId &&
    documents.every((doc) => project.documentForSession(owner, doc.path, doc.sessionId) === doc);
  await pendingEditorSave.catch(() => undefined);
  if (!current()) return;
  flushEditorContent();
  await Promise.all(documents.map((doc) => projectSaveCoordinator.wait(doc.path)));
  if (!current()) return;
  flushEditorContent();
  const dirtyCount = documents.filter((document) => isProjectDocumentDirty(document)).length;
  if (dirtyCount) {
    try {
      await ElMessageBox.confirm(
        t("project.confirm.closeAllDirty", { count: dirtyCount }),
        t("project.unsavedTitle"),
        { type: "warning", confirmButtonText: t("project.discard"), cancelButtonText: t("common.cancel") },
      );
    } catch {
      return;
    }
  }
  if (!current()) return;
  for (const document of documents) {
    project.closeDocument(document.path);
    recordClosedDocument(document.path);
  }
}

// Workbench tab commands (close-tab, close-all, next/prev/tab-N) act on these
// document tabs while the focus is in the editor pane (not its bottom panel); save-all saves them.
useActiveViewCommands(() => [
  registerDocumentTabHost({
    contains: (element) =>
      Boolean(
        workspaceElement.value?.contains(element) &&
          element.closest(".project-editor-pane") &&
          !element.closest(".app-bottom-panel")
      ),
    tabIds: projectEditorTabIds,
    activeTabId: () => activeProjectEditorTab.value,
    selectTab: (id) => {
      activeProjectEditorTab.value = id;
    },
    closeTab: closeProjectEditorTab,
    closeAll: closeAllDocumentsConfirmOnce,
    saveAll: saveAllDocuments,
  }),
  registerCommand({
    id: "editor:save-all",
    source: "builtin",
    title: () => t("keybindings.command.editor:save-all"),
    subtitle: () => t("commandPalette.source.builtin"),
    keywords: ["save", "保存"],
    execute: async () => {
      await pendingEditorSave.catch(() => undefined);
      await saveAllDocuments();
    },
  }),
]);

async function closeDocuments(paths: string[]) {
  for (const path of paths) {
    if (!(await requestCloseDocument(path))) break;
  }
}

function selectOverflowTab(target: string) {
  if (target.startsWith("terminal:")) {
    selectProjectTerminal(target.slice("terminal:".length));
  } else {
    selectProjectDocument(target);
  }
  documentTabsOverflowDropdown.value?.handleClose();
}

function updateDocumentTabsOverflow() {
  const tabs = documentTabsElement.value;
  const navScroll = tabs?.querySelector<HTMLElement>(".el-tabs__nav-scroll");
  const nav = tabs?.querySelector<HTMLElement>(".el-tabs__nav");
  tabsOverflow.value = Boolean(navScroll && nav && nav.offsetWidth > navScroll.clientWidth);
}

function observeDocumentTabs() {
  documentTabsResizeObserver?.disconnect();
  documentTabsResizeObserver = null;
  const tabs = documentTabsElement.value;
  if (!tabs) return;

  documentTabsResizeObserver = new ResizeObserver(updateDocumentTabsOverflow);
  documentTabsResizeObserver.observe(tabs);
  const nav = tabs.querySelector<HTMLElement>(".el-tabs__nav");
  if (nav) documentTabsResizeObserver.observe(nav);
  updateDocumentTabsOverflow();
}

async function handleTabCommand(command: string, path: string) {
  const index = project.documents.findIndex((document) => document.path === path);
  if (index < 0) return;
  const paths = project.documents.map((document) => document.path);
  switch (command) {
    case "compare-with-clipboard":
      await compareFileWithClipboard(path);
      return;
    case "set-default-editor":
      await setDocumentDefaultEditor(path, false);
      return;
    case "reset-default-editor":
      await setDocumentDefaultEditor(path, true);
      return;
    case "compare-with-active":
      await compareWithOpenDocument(path);
      return;
    case "copy-path":
      try {
        await copyText(path);
        errorKey.value = null;
        ElMessage.success(t("project.context.pathCopied"));
      } catch {
        errorKey.value = "project.operationFailed";
      }
      return;
    case "reveal":
      try {
        await invokeBackend<void>("local_fs_show_in_file_manager", { path, select: true });
      } catch {
        errorKey.value = "project.operationFailed";
      }
      return;
    case "close":
      await requestCloseDocument(path);
      return;
    case "close-other":
      await closeDocuments(paths.filter((item) => item !== path));
      return;
    case "close-left":
      await closeDocuments(paths.slice(0, index));
      return;
    case "close-right":
      await closeDocuments(paths.slice(index + 1));
      return;
    case "close-saved":
      flushEditorContent();
      for (const document of [...project.documents]) {
        if (!isProjectDocumentDirty(document)) {
          project.closeDocument(document.path);
          recordClosedDocument(document.path);
        }
      }
      return;
    case "close-all":
      await closeDocuments(paths);
      return;
    default:
      return;
  }
}

async function renameSavedProject(root: string) {
  const entry = project.projects.find((item) => item.root === root);
  if (!entry) return;
  const name = await promptForName("project.renameProjectPrompt", entry.name);
  if (name) project.renameProject(root, name);
}

async function removeSavedProject(root: string) {
  const entry = project.projects.find((item) => item.root === root);
  if (!entry) return;
  if (root === project.projectRoot && !(await confirmDiscardWorkspace())) return;
  // Only this workspace loses the project when another one still lists it.
  const debugStops = debugSessions.hasLiveSession(root) && !listedElsewhere(root);
  try {
    await ElMessageBox.confirm(
      t(debugStops ? "project.confirm.removeProjectWithDebug" : "project.confirm.removeProject", { name: entry.name }),
      t("project.removeProject"),
      {
        type: "warning",
        confirmButtonText: t("project.removeProject"),
        cancelButtonText: t("common.cancel"),
      }
    );
  } catch {
    return;
  }
  const removedCurrentProject = root === project.projectRoot;
  project.removeProject(root);
  await loadProjectTree();
  if (removedCurrentProject) {
    projectPathInput.value = "";
  }
}

async function selectSavedProject(root: string) {
  if (root === project.projectRoot) return;
  projectPathInput.value = root;
  await openProject(root);
}

function validEntryName(value: string) {
  const name = value.trim();
  return Boolean(name && name !== "." && name !== ".." && !/[\\/]/.test(name));
}

// Reload a single lazy tree node in-place (collapse→expand) without
// rebuilding the whole tree. Returns false if the node isn't loaded yet
// (collapsed/unvisited) — the caller can fall back to a full refresh then.
async function reloadTreeNode(nodeId: string): Promise<boolean> {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const node = fileTreeRef.value?.getNode(nodeId) as any;
  if (!node?.expand) return false;
  node.loaded = false;
  node.loading = false;
  if (node.expanded) {
    node.collapse();
    await nextTick();
  }
  node.expand();
  return true;
}

/**
 * Git 操作（pull/checkout/stash…）后原地同步资源树：只重新列出已展开的目录，
 * 按 id 增删节点并更新保留节点的分支装饰，不重建 el-tree，展开状态与选中项保持不变。
 * 已加载但折叠的目录标记为未加载，下次展开时再读取。根节点集合变化时返回 false 由调用方整树重建。
 */
async function refreshProjectTreeInPlace(): Promise<boolean> {
  const treeApi = fileTreeRef.value;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const rootNode = (treeApi as any)?.root;
  if (!treeApi || !rootNode) return false;
  const nested = new Set(
    [...worktreeChildren.value.values()].flatMap((children) => children.map((child) => child.root))
  );
  const nextRoots = project.projects
    .filter((entry) => !nested.has(entry.root))
    .map(toWorkspaceRootNode);
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const currentRootIds = (rootNode.childNodes as any[]).map((node) => node.data?.id);
  if (
    currentRootIds.length !== nextRoots.length ||
    nextRoots.some((root, index) => root.id !== currentRootIds[index])
  ) {
    return false;
  }

  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const assignNodeData = (node: any, next: WorkspaceTreeNode) => {
    const data = node.data as WorkspaceTreeNode;
    if (!("repositoryBranch" in next)) delete data.repositoryBranch;
    if (!("worktree" in next)) delete data.worktree;
    const { children: _children, ...fields } = next;
    Object.assign(data, fields);
  };

  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const syncChildren = async (node: any): Promise<void> => {
    if (!node.loaded) return;
    if (!node.expanded) {
      node.loaded = false;
      return;
    }
    const data = node.data as WorkspaceTreeNode;
    let next: WorkspaceTreeNode[];
    try {
      next = data.isProjectRoot
        ? await projectRootChildren(data.projectRoot)
        : sortProjectEntries(await readDirectory(data.projectRoot, data.path)).map((entry) =>
            toWorkspaceTreeNode(entry, data.projectRoot)
          );
    } catch (error) {
      browserAccess.noteFailure(data.projectRoot, error);
      return;
    }
    // 异步读取期间节点可能已被折叠、移除或整树重建。
    if (!node.parent || !node.expanded) return;
    const nextById = new Map(next.map((child) => [child.id, child]));
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    for (const child of [...(node.childNodes as any[])]) {
      const replacement = nextById.get(child.data?.id);
      if (!replacement || replacement.isDir !== child.data.isDir) {
        if (selectedNode.value?.id === child.data?.id) selectedNode.value = null;
        treeApi.remove(child);
      }
    }
    next.forEach((childData, index) => {
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const existingIndex = (node.childNodes as any[]).findIndex(
        (child) => child.data?.id === childData.id
      );
      if (existingIndex === -1) {
        node.insertChild({ data: childData }, index);
        return;
      }
      const existing = node.childNodes[existingIndex];
      if (existingIndex !== index) {
        node.childNodes.splice(existingIndex, 1);
        node.childNodes.splice(index, 0, existing);
      }
      assignNodeData(existing, childData);
    });
    await Promise.all(node.childNodes.map(syncChildren));
  };

  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  (rootNode.childNodes as any[]).forEach((node, index) => assignNodeData(node, nextRoots[index]));
  await Promise.all(rootNode.childNodes.map(syncChildren));
  fileDecorations.refreshExpanded(expandedTreeDirectories());
  return true;
}

/** Git 刷新优先原地同步资源树；无法原地同步（树未挂载或根集合变化）时整树重建。 */
async function refreshProjectTreeAfterGit() {
  if (!(await refreshProjectTreeInPlace())) await loadProjectTree();
}

// After a file/folder operation, reload only the parent directory node.
// Falls back to a full project refresh only when the parent node isn't in
// the tree (never expanded yet, so no reload is needed for collapsed nodes).
async function refreshAfterFileOp(itemPath: string, projectRoot: string): Promise<void> {
  const parentRelPath = projectDirname(itemPath);
  const nodeId = parentRelPath
    ? `${projectRoot}:${parentRelPath}`
    : projectTreeRootId(projectRoot);
  if (!(await reloadTreeNode(nodeId))) {
    await loadProjectTree(projectRoot);
  }
  scheduleProjectIndexRefresh(projectRoot);
}

function operationParentPath() {
  const selected = selectedNode.value;
  if (!selected) return "";
  return selected.isDir ? selected.path : projectDirname(selected.path);
}

// Plugin fileTemplates: "New File / New Project from Template…".
const fileTemplatesStore = useFileTemplatesStore();


const fileTemplateDialogMounted = ref(false);
const fileTemplateDialogVisible = ref(false);
const fileTemplateDialogKind = ref<FileTemplateKind>("file");
const fileTemplateProjectRoot = ref("");
const fileTemplateParentPath = ref("");

function showFileTemplateDialog(kind: FileTemplateKind) {
  if (!isDesktopRuntime()) {
    errorKey.value = "project.desktopOnly";
    return;
  }
  void fileTemplatesStore.hydrateCustom().catch(() => undefined);
  fileTemplateDialogKind.value = kind;
  fileTemplateDialogMounted.value = true;
  fileTemplateDialogVisible.value = true;
}

async function newFileFromTemplate(node?: WorkspaceTreeNode) {
  if (node) {
    closeProjectContextMenu();
    if (!(await activateContextNode(node))) return;
  }
  if (!project.projectRoot) return;
  fileTemplateProjectRoot.value = project.projectRoot;
  fileTemplateParentPath.value = operationParentPath();
  showFileTemplateDialog("file");
}

function newProjectFromTemplate() {
  showFileTemplateDialog("project");
}

async function onFileTemplateCreated(event: FileTemplateCreatedEvent) {
  if (event.kind === "project") {
    if (event.complete && event.projectPath) {
      projectPathInput.value = event.projectPath;
      await openProject(event.projectPath);
    }
    return;
  }
  const [first] = event.created;
  if (!first) return;
  await refreshAfterFileOp(first, event.root);
  if (event.complete) await openFile(first, undefined, event.root);
}

async function promptForName(title: MessageKey, initialValue = "") {
  try {
    const { value } = await ElMessageBox.prompt(t(title), t("project.fileOperations"), {
      inputValue: initialValue,
      inputValidator: (input) => validEntryName(input) || t("project.validation.name"),
      confirmButtonText: t("common.confirm"),
      cancelButtonText: t("common.cancel"),
    });
    return value.trim();
  } catch {
    return null;
  }
}

async function createFile() {
  const name = await promptForName("project.newFilePrompt");
  if (!name) return;
  const path = joinProjectPath(operationParentPath(), name);
  try {
    await invokeBackend<void>("local_fs_write_text_file", {
      projectRoot: project.projectRoot,
      filePath: path,
      content: "",
    });
    await refreshAfterFileOp(path, project.projectRoot);
    await openFile(path);
  } catch {
    errorKey.value = "project.operationFailed";
  }
}

async function createFolder() {
  const name = await promptForName("project.newFolderPrompt");
  if (!name) return;
  const dirPath = joinProjectPath(operationParentPath(), name);
  try {
    await invokeBackend<void>("local_fs_mkdir", {
      projectRoot: project.projectRoot,
      dirPath,
    });
    await refreshAfterFileOp(dirPath, project.projectRoot);
  } catch {
    errorKey.value = "project.operationFailed";
  }
}

async function renameSelected() {
  const selected = selectedNode.value;
  if (!selected) return;
  renamingPath.value = selected.path;
  renameValue.value = selected.name;
  await nextTick();
  document.querySelector<HTMLInputElement>(".project-tree-rename input")?.select();
}

function cancelProjectRename() {
  renamingPath.value = "";
}

async function saveProjectRename() {
  const selected = selectedNode.value;
  if (!selected || selected.path !== renamingPath.value) return;
  const name = renameValue.value.trim();
  const oldPath = selected.path;
  const isDir = selected.isDir;
  renamingPath.value = "";
  if (!validEntryName(name) || name === selected.name) return;
  const newPath = joinProjectPath(projectDirname(oldPath), name);
  try {
    await invokeBackend<void>("local_fs_rename", {
      projectRoot: project.projectRoot,
      oldPath,
      newName: name,
    });
    project.renameDocuments(
      toDocumentPath(project.projectRoot, oldPath),
      toDocumentPath(project.projectRoot, newPath),
      isDir
    );
    await refreshProject();
  } catch {
    errorKey.value = "project.operationFailed";
  }
}

async function deleteSelected() {
  const selected = selectedNode.value;
  if (!selected) return;
  try {
    await ElMessageBox.confirm(
      `${t("project.confirm.delete")} ${selected.name}`,
      t("project.delete"),
      {
        type: "warning",
        confirmButtonText: t("project.delete"),
        cancelButtonText: t("common.cancel"),
      }
    );
  } catch {
    return;
  }
  try {
    await invokeBackend<void>("local_fs_delete", {
      projectRoot: selected.projectRoot,
      path: selected.path,
    });
    project.removeDocuments(toDocumentPath(selected.projectRoot, selected.path), selected.isDir);
    // Remove only the deleted node; rebuilding the tree loses expansion and scroll state.
    const treeNode = fileTreeRef.value?.getNode(selected.id);
    if (treeNode) fileTreeRef.value?.remove(treeNode);
    if (selectedNode.value?.id === selected.id) selectedNode.value = null;
    if (resourceGitSelection.value?.id === selected.id) resourceGitSelection.value = null;
    scheduleProjectIndexRefresh(selected.projectRoot);
  } catch {
    errorKey.value = "project.operationFailed";
  }
}

async function revealSelected() {
  const selected = selectedNode.value;
  const root =
    selected?.projectRoot ??
    (project.activePath ? projectRootForPath(project.activePath) : project.projectRoot);
  const path =
    selected?.path ?? (project.activePath ? relativeProjectPath(project.activePath, root) : "");
  if (!path) return;
  if (!isDesktopRuntime()) {
    errorKey.value = "project.browser.desktopOnly";
    return;
  }
  try {
    await invokeBackend<void>("local_fs_show_in_explorer", {
      projectRoot: root,
      path,
    });
  } catch {
    errorKey.value = "project.operationFailed";
  }
}

async function revealSavedProject(root: string) {
  if (!isDesktopRuntime()) {
    errorKey.value = "project.browser.desktopOnly";
    return;
  }
  try {
    await invokeBackend<void>("local_fs_show_in_file_manager", { path: root });
  } catch {
    errorKey.value = "project.operationFailed";
  }
}

function escapeSearchPattern(value: string) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

function searchPattern(query: string) {
  return searchUseRegex.value ? query : escapeSearchPattern(query);
}

function searchTargets() {
  if (searchAllProjects.value)
    return project.projects.map((entry) => ({ ...entry, scopePath: null }));
  if (searchScope.value) {
    return [
      {
        root: searchScope.value.projectRoot,
        name:
          project.projects.find((entry) => entry.root === searchScope.value?.projectRoot)?.name ??
          projectBasename(searchScope.value.projectRoot),
        scopePath: searchScope.value.path || null,
      },
    ];
  }
  if (!project.projectRoot) return [];
  return [
    {
      root: project.projectRoot,
      name: project.currentProject?.name ?? projectBasename(project.projectRoot),
      scopePath: null,
    },
  ];
}

function clearReplacePreview() {
  replacePreview.value = [];
}

function handleSearchQueryInput(value: string) {
  searchRequestGeneration += 1;
  searchLoading.value = false;
  sideMode.value = value ? "search" : "explorer";
  clearReplacePreview();
  if (!value.trim()) searchResults.value = [];
}

function clearSearchQuery() {
  searchRequestGeneration += 1;
  searchLoading.value = false;
  sideMode.value = "explorer";
  searchScope.value = null;
  searchResults.value = [];
  clearReplacePreview();
}

function refreshSearchAfterOptionChange() {
  clearReplacePreview();
  if (searchQuery.value.trim()) void runSearch();
}

function setIncludeContent(value: boolean) {
  if (!value && hasAdvancedSearchOptions.value) {
    ElMessage.info(t("project.search.contentRequired"));
    return;
  }
  if (includeContent.value === value) return;
  includeContent.value = value;
  refreshSearchAfterOptionChange();
}

function toggleSearchOption(option: "case" | "whole" | "regex" | "replace" | "allProjects") {
  switch (option) {
    case "case":
      searchCaseSensitive.value = !searchCaseSensitive.value;
      if (searchCaseSensitive.value) includeContent.value = true;
      break;
    case "whole":
      searchWholeWord.value = !searchWholeWord.value;
      if (searchWholeWord.value) includeContent.value = true;
      break;
    case "regex":
      searchUseRegex.value = !searchUseRegex.value;
      if (searchUseRegex.value) includeContent.value = true;
      break;
    case "replace":
      searchReplaceMode.value = !searchReplaceMode.value;
      if (searchReplaceMode.value) includeContent.value = true;
      break;
    case "allProjects":
      searchAllProjects.value = !searchAllProjects.value;
      if (searchAllProjects.value) searchScope.value = null;
      break;
  }
  refreshSearchAfterOptionChange();
}

async function searchProject(
  root: string,
  name: string,
  query: string,
  scopePath: string | null
): Promise<WorkspaceSearchResult[]> {
  if (hasAdvancedSearchOptions.value) {
    const rows = await invokeBackend<RegexSearchMatch[]>("local_fs_regex_search", {
      projectRoot: root,
      pattern: searchPattern(query),
      caseSensitive: searchCaseSensitive.value,
      wholeWord: searchWholeWord.value,
      scopePath: scopePath ?? "",
    });
    return rows.map((row) => ({
      name: row.relativePath.split("/").pop() || row.relativePath,
      path: row.path,
      relativePath: row.relativePath,
      isDir: false,
      kind: "content" as const,
      line: row.line,
      col: row.col,
      lineText: row.lineText,
      projectRoot: root,
      projectName: name,
    }));
  }

  const rows = await invokeBackend<ProjectSearchResult[]>("local_fs_search", {
    projectRoot: root,
    scopePath,
    query,
    includeContent: includeContent.value,
  });
  return rows.map((row) => ({ ...row, projectRoot: root, projectName: name }));
}

async function runSearch() {
  const requestGeneration = ++searchRequestGeneration;
  const query = searchQuery.value.trim();
  sideMode.value = query ? "search" : "explorer";
  if (!query) {
    searchResults.value = [];
    clearReplacePreview();
    searchLoading.value = false;
    return;
  }
  const targets = searchTargets();
  if (!targets.length) {
    searchResults.value = [];
    searchLoading.value = false;
    return;
  }

  searchLoading.value = true;
  errorKey.value = null;
  clearReplacePreview();
  try {
    const settled = await Promise.allSettled(
      targets.map((target) => searchProject(target.root, target.name, query, target.scopePath))
    );
    if (requestGeneration !== searchRequestGeneration) return;
    const results = settled.flatMap((result) =>
      result.status === "fulfilled" ? result.value : []
    );
    if (!results.length && settled.every((result) => result.status === "rejected")) {
      throw new Error("search_failed");
    }
    searchResults.value = results;
  } catch {
    if (requestGeneration === searchRequestGeneration) errorKey.value = "project.searchFailed";
  } finally {
    if (requestGeneration === searchRequestGeneration) searchLoading.value = false;
  }
}

async function previewReplace() {
  const query = searchQuery.value.trim();
  const targets = searchTargets();
  if (!query || !targets.length) return;

  searchLoading.value = true;
  errorKey.value = null;
  try {
    const settled = await Promise.allSettled(
      targets.map(async (target) => {
        const files = await invokeBackend<ReplacePreviewFile[]>("local_fs_regex_replace_preview", {
          projectRoot: target.root,
          pattern: searchPattern(query),
          replacement: replaceQuery.value,
          caseSensitive: searchCaseSensitive.value,
          wholeWord: searchWholeWord.value,
          scopePath: target.scopePath ?? "",
        });
        return files.map((file) => ({
          ...file,
          projectRoot: target.root,
          projectName: target.name,
        }));
      })
    );
    replacePreview.value = settled.flatMap((result) =>
      result.status === "fulfilled" ? result.value : []
    );
    if (!replacePreview.value.length) ElMessage.info(t("project.search.replaceEmpty"));
  } catch {
    errorKey.value = "project.searchFailed";
  } finally {
    searchLoading.value = false;
  }
}

async function reloadReplacedDocuments(files: WorkspaceReplacePreviewFile[]) {
  const affectedPaths = new Set(
    files.map((file) => toDocumentPath(file.projectRoot, file.relativePath))
  );
  const activePath = project.activePath;
  const affectedDocumentPaths = project.documents
    .filter((document) => document.loaded && affectedPaths.has(document.path))
    .map((document) => document.path);
  await Promise.all(
    affectedDocumentPaths.map(async (path) => {
      const root = projectRootForPath(path);
      try {
        const content = await invokeBackend<string>("local_fs_read_text_file", {
          projectRoot: root,
          filePath: relativeProjectPath(path, root),
        });
        project.setDocumentLoaded(path, content);
      } catch {
        // The replacement has already succeeded. Leave an affected editor open if it cannot be reloaded.
      }
    })
  );
  project.activePath = activePath;
}

async function applyReplace() {
  const preview = replacePreview.value;
  if (!preview.length) return;
  const affectedPaths = new Set(
    preview.map((file) => toDocumentPath(file.projectRoot, file.relativePath))
  );
  flushEditorContent();
  const hasDirtyDocument = project.documents.some(
    (document) => affectedPaths.has(document.path) && isProjectDocumentDirty(document)
  );
  if (hasDirtyDocument) {
    ElMessage.warning(t("project.search.replaceDirty"));
    return;
  }

  try {
    await ElMessageBox.confirm(
      t("project.search.replaceConfirm", {
        files: String(replacePreviewFileCount.value),
        lines: String(replacePreviewLineCount.value),
      }),
      t("project.search.replaceTitle"),
      {
        type: "warning",
        confirmButtonText: t("project.search.applyReplace"),
        cancelButtonText: t("common.cancel"),
      }
    );
  } catch {
    return;
  }

  const byProject = new Map<string, WorkspaceReplacePreviewFile[]>();
  for (const file of preview) {
    const files = byProject.get(file.projectRoot) ?? [];
    files.push(file);
    byProject.set(file.projectRoot, files);
  }

  replaceApplying.value = true;
  errorKey.value = null;
  try {
    const changes = await Promise.all(
      [...byProject.entries()].map(([projectRoot, files]) =>
        invokeBackend<number>("local_fs_regex_replace_apply", {
          projectRoot,
          pattern: searchPattern(searchQuery.value.trim()),
          replacement: replaceQuery.value,
          caseSensitive: searchCaseSensitive.value,
          wholeWord: searchWholeWord.value,
          filePaths: files.map((file) => file.relativePath),
        })
      )
    );
    await reloadReplacedDocuments(preview);
    await refreshProject();
    replacePreview.value = [];
    ElMessage.success(
      t("project.search.replaceSuccess", {
        files: String(changes.reduce((total, count) => total + count, 0)),
      })
    );
  } catch {
    errorKey.value = "project.searchFailed";
  } finally {
    replaceApplying.value = false;
  }
}

function aiSavedProjectRoot(value: unknown) {
  const root = typeof value === "string" ? value.trim() : "";
  if (!root || !project.projects.some((entry) => entry.root === root)) {
    throw new Error("The requested Local Project is not saved in this workspace");
  }
  return root;
}

function aiOpenDocumentPath(relativePath: string, projectRoot?: string) {
  const root = projectRoot ? aiSavedProjectRoot(projectRoot) : project.projectRoot;
  if (!root) throw new Error("Open or select a saved Local Project first");
  return {
    root,
    path: toDocumentPath(root, relativePath),
  };
}

async function handleAiProjectWorkspaceAction(input: Record<string, unknown>) {
  const action = typeof input.action === "string" ? input.action : "";
  switch (action) {
    case "select": {
      const root = aiSavedProjectRoot(input.root);
      await selectSavedProject(root);
      return { root, selected: project.projectRoot === root };
    }
    case "open-file": {
      const relativePath = typeof input.path === "string" ? input.path : "";
      if (!relativePath) throw new Error("A project-relative file path is required");
      const root = typeof input.projectRoot === "string" && input.projectRoot
        ? aiSavedProjectRoot(input.projectRoot)
        : project.projectRoot;
      if (!root) throw new Error("Open or select a saved Local Project first");
      if (root !== project.projectRoot) await selectSavedProject(root);
      const line = typeof input.line === "number" ? Math.max(1, Math.floor(input.line)) : undefined;
      const column = typeof input.column === "number"
        ? Math.max(1, Math.floor(input.column))
        : undefined;
      await openFile(relativePath, line, root, column);
      return { root, path: relativePath, line, column };
    }
    case "refresh": {
      await refreshProject();
      return { root: project.projectRoot, refreshed: true };
    }
    case "save-file": {
      const relativePath = typeof input.path === "string" ? input.path : "";
      const { path } = aiOpenDocumentPath(relativePath);
      if (!project.documents.some((document) => document.path === path)) {
        throw new Error("The requested file is not open in the project editor");
      }
      const outcome = await saveDocument(path);
      if (outcome !== "saved") throw new Error(`Saving the file did not complete (${outcome})`);
      return { path: relativePath, saved: true };
    }
    case "close-file": {
      const relativePath = typeof input.path === "string" ? input.path : "";
      const { path } = aiOpenDocumentPath(relativePath);
      if (!(await requestCloseDocument(path))) throw new Error("Closing the file was cancelled");
      return { path: relativePath, closed: true };
    }
    case "reveal-project": {
      const root = aiSavedProjectRoot(input.root);
      await revealSavedProject(root);
      return { root, revealed: true };
    }
    case "rename-project": {
      const root = aiSavedProjectRoot(input.root);
      const name = typeof input.name === "string" ? input.name.trim() : "";
      if (!name) throw new Error("A project name is required");
      project.renameProject(root, name);
      return { root, name };
    }
    case "remove-project": {
      const root = aiSavedProjectRoot(input.root);
      const wasActive = root === project.projectRoot;
      project.removeProject(root);
      if (wasActive) {
        projectPathInput.value = "";
        await loadProjectTree();
      }
      return { root, removed: true };
    }
    case "open-worktree-inspector": {
      await openWorktreeInspector();
      if (!gitInspectorOpen.value) throw new Error("No Git repository was found under the open projects");
      return {
        repoPath: gitInspectorRepoPath.value,
        repositories: worktreeRepoOptions.value.map(({ name, path }) => ({ name, path })),
      };
    }
    case "select-worktree-repo": {
      const repoRoot = typeof input.repoPath === "string" ? input.repoPath.trim() : "";
      // 切换器的选项是扫描出来的子仓库，未必是已保存的项目根。
      if (!worktreeRepoOptions.value.some((entry) => entry.path === repoRoot)) {
        throw new Error("Call open-worktree-inspector first and pick one of the repositories it returns");
      }
      await showWorktreeInspector(repoRoot);
      if (!gitInspectorOpen.value) throw new Error(`Could not open worktrees for: ${repoRoot}`);
      return { repoPath: gitInspectorRepoPath.value, selected: repoRoot };
    }
    case "bind-workspace": {
      const workspace = workbench.activeWorkspace;
      if (!workspace) throw new Error("No workspace is active");
      // 绑定目标可以是项目根，也可以是它下面的 worktree 目录。
      const requested = typeof input.root === "string" ? input.root.trim() : "";
      const root = requested || project.projectRoot;
      if (!root) throw new Error("Open or select a saved Local Project first");
      if (requested && !project.projects.some((entry) => isProjectPathWithin(requested, entry.root))) {
        throw new Error("The requested path is not inside a saved Local Project");
      }
      await bindRootToWorkspace(root);
      // bindRootToWorkspace 只弹提示不抛错，AI 侧要能分辨绑定是否真的生效。
      if (workbench.activeWorktreePath !== root) throw new Error(`Could not bind the workspace to: ${root}`);
      return { root, workspace: workspace.name, bound: true };
    }
    case "unbind-workspace": {
      const workspace = workbench.activeWorkspace;
      if (!workspace) throw new Error("No workspace is active");
      if (!workbench.activeWorktreePath) throw new Error("The active workspace is not bound to a worktree");
      unbindProjectRootFromWorkspace();
      return { workspace: workspace.name, unbound: true };
    }
    default:
      throw new Error(`Unsupported Local Projects action: ${action || "unknown"}`);
  }
}

let unregisterAiProjectWorkspaceActions: (() => void) | undefined;
function registerAiProjectWorkspaceActions() {
  unregisterAiProjectWorkspaceActions?.();
  unregisterAiProjectWorkspaceActions = registerAiWorkspaceActionHandler(
    "projects",
    handleAiProjectWorkspaceAction
  );
}
registerAiProjectWorkspaceActions();
onActivated(() => {
  registerAiProjectWorkspaceActions();
  startProjectClipboardHotkey();
});
onDeactivated(() => {
  unregisterAiProjectWorkspaceActions?.();
  unregisterAiProjectWorkspaceActions = undefined;
  stopProjectClipboardHotkey();
});

async function openSearchResult(result: WorkspaceSearchResult) {
  if (result.projectRoot !== project.projectRoot) {
    await selectSavedProject(result.projectRoot);
    if (result.projectRoot !== project.projectRoot) return;
  }
  if (result.isDir) {
    selectedNode.value = toWorkspaceTreeNode(
      {
        name: result.name,
        path: result.relativePath,
        isDir: true,
      },
      result.projectRoot
    );
    sideMode.value = "explorer";
    return;
  }
  await openFile(result.relativePath, result.line, result.projectRoot);
}

async function openGitWorktreeFile(payload: { repoPath: string; entry: GitFileEntry }) {
  const { repoPath, entry } = payload;
  if (!repoPath || entry.status === "deleted") return;
  clearGitEditorTools();
  previewMode.value = false;
  await openFile(entry.path, undefined, repoPath);
}

async function openGitFile(payload: {
  repoPath: string;
  entry: GitFileEntry;
  group: "staged" | "unstaged" | "untracked";
}) {
  const { repoPath, entry, group } = payload;
  if (!repoPath || gitCompareLoading.value) return;

  gitCompareLoading.value = true;
  errorKey.value = null;
  previewMode.value = false;
  blameEnabled.value = false;
  blameHunks.value = [];
  try {
    const fileDiff = await invokeBackend<ProjectGitFileDiff>("git_get_diff", {
      repoPath,
      relativePath: entry.path,
      staged: group === "staged",
      oldPath: entry.old_path ?? "",
    });
    const documentPath = toDocumentPath(repoPath, entry.path);
    const document = project.openDocument(documentPath);

    // git_get_diff returns the comparison and worktree snapshot together, so a
    // file deleted after the status refresh cannot race a second filesystem IPC.
    if (!document.loaded) {
      const documentContent =
        fileDiff.worktree_exists && fileDiff.worktree_is_text
          ? fileDiff.worktree_content
          : fileDiff.modified;
      project.setDocumentLoaded(documentPath, documentContent);
    }

    // Let the active-path watcher clear the previous file state before applying
    // the newly requested comparison.
    await nextTick();
    editorComparison.value = {
      kind: "git",
      label: t(`project.git.${group}` as MessageKey),
      originalContent: fileDiff.original,
      modifiedContent: fileDiff.modified,
      binary: fileDiff.binary,
    };
    canUndo.value = false;
    canRedo.value = false;
  } catch {
    ElMessage.error(t("project.git.compareFailed"));
  } finally {
    gitCompareLoading.value = false;
  }
}

async function openGitCommitFile(payload: {
  repoPath: string;
  commitHash: string;
  file: GitCommitFile;
}) {
  const { repoPath, commitHash, file } = payload;
  if (!repoPath || gitCompareLoading.value) return;

  gitCompareLoading.value = true;
  errorKey.value = null;
  previewMode.value = false;
  blameEnabled.value = false;
  blameHunks.value = [];
  try {
    const fileDiff = await getGitCommitFileDiff(
      repoPath,
      commitHash,
      file.path,
      file.old_path ?? ""
    );
    const documentPath = toDocumentPath(repoPath, file.path);
    const document = project.openDocument(documentPath);
    if (!document.loaded) project.setDocumentLoaded(documentPath, fileDiff.modified);

    await nextTick();
    editorComparison.value = {
      kind: "git",
      label: commitHash.slice(0, 7),
      originalContent: fileDiff.original,
      modifiedContent: fileDiff.modified,
      binary: fileDiff.binary,
    };
    canUndo.value = false;
    canRedo.value = false;
  } catch {
    ElMessage.error(t("project.git.compareFailed"));
  } finally {
    gitCompareLoading.value = false;
  }
}

async function selectGitProject(root: string) {
  await selectSavedProject(root);
  await loadProjectTree(root);
}

function handleGitRepositoryChange(repoPath: string) {
  // 抽屉独立选择仓库，不受外部面板的后台刷新或项目切换影响。
  if (!gitInspectorOpen.value) gitInspectorRepoPath.value = repoPath;
}

async function handleGitExtendedAction(action: GitInspector) {
  gitInspectorRepoPath.value = resourceGitState.value.repoPath;
  gitInspector.value = action;
  gitInspectorOpen.value = true;
  if (action === "graph") void loadGitGraph();
  if (action === "branches") {
    branchRepoLoading.value = true;
    branchRepoOptions.value = [];
    try {
      const options = await scanInspectorRepositories();
      const currentPath = gitInspectorRepoPath.value;
      if (currentPath && !options.some((entry) => entry.path === currentPath)) {
        options.unshift({ name: projectBasename(currentPath), path: currentPath, relative_path: "." });
      }
      branchRepoOptions.value = options;
    } finally {
      branchRepoLoading.value = false;
    }
  }
}

function selectBranchRepository(repoPath: string) {
  if (branchPanelBusy.value || branchRepoLoading.value) return;
  if (!branchRepoOptions.value.some((entry) => entry.path === repoPath)) return;
  gitInspectorRepoPath.value = repoPath;
}

/**
 * 资源面板不经过 Git 面板，抽屉的仓库路径要自己解析。
 * 左侧每个项目都可能是装着多个仓库的外层目录，全部扫一遍汇总成切换器选项，
 * 这样一次就能在 min、saas 下的所有仓库之间切，不必回去换项目再开。
 */
async function scanInspectorRepositories() {
  const projects = [...project.projects];
  const scans = await Promise.all(
    projects.map((entry) =>
      listProjectRepositories(entry.root).catch(() => [] as GitRepositoryEntry[])
    )
  );
  const seen = new Set<string>();
  const options: GitRepositoryEntry[] = [];
  for (const [index, scan] of scans.entries()) {
    const owner = projects[index];
    for (const entry of scan) {
      if (seen.has(entry.path)) continue;
      seen.add(entry.path);
      // 不同项目下可能有同名仓库，挂上项目名才分得清是哪一个。
      const name = entry.relative_path === "." ? owner.name : `${owner.name} / ${entry.name}`;
      options.push({ ...entry, name });
    }
  }
  return options;
}

async function openWorktreeInspector() {
  const root = project.projectRoot;
  if (!root) return;
  const options = await scanInspectorRepositories();
  worktreeRepoOptions.value = options;
  // 当前项目自己就是仓库时从它开始，否则退回它名下第一个仓库。
  const own = options.filter((entry) => isProjectPathWithin(entry.path, root));
  const rootKey = projectPathKey(root);
  const start = own.find((entry) => projectPathKey(entry.path) === rootKey) ?? own[0] ?? options[0];
  if (!start) {
    ElMessage.error(t("git.noRepositories"));
    return;
  }
  await showWorktreeInspector(start.path);
}

/** 选中项可能本身是 linked worktree，必须换成主仓库才能列全同组工作树。 */
async function showWorktreeInspector(repoRoot: string) {
  try {
    const { repoPath } = await resolveOwningRepo(repoRoot);
    gitInspectorRepoPath.value = repoPath;
  } catch {
    ElMessage.error(t("project.git.bindFailed"));
    return;
  }
  // 切换器回显用户点的那一项：解析后的主仓库未必在选项里。
  worktreeRepoSelection.value = repoRoot;
  gitInspector.value = "worktrees";
  gitInspectorOpen.value = true;
}

async function loadGitGraph() {
  // 打开图谱时 Git 面板可能尚未探测到仓库：用当前项目目录兜底解析，避免整块面板空白。
  if (!gitInspectorRepoPath.value) {
    try {
      const { repoPath } = await resolveOwningRepo(project.projectRoot);
      if (repoPath) gitInspectorRepoPath.value = repoPath;
    } catch {
      // 不是仓库或探测失败：保留空状态，由面板给出可操作提示。
    }
  }
  if (!gitInspectorRepoPath.value) {
    gitGraphLogs.value = [];
    return;
  }
  try {
    gitGraphLogs.value = await getGitLog(gitInspectorRepoPath.value, 200);
  } catch (error) {
    ElMessage.error(String(error));
  }
}

async function openGraphCommit(entry: GitLogEntry) {
  gitInspectorOpen.value = false;
  const detail = await getGitCommitDetail(gitInspectorRepoPath.value, entry.hash);
  const first = detail.files[0];
  if (first) {
    await openGitCommitFile({
      repoPath: gitInspectorRepoPath.value,
      commitHash: entry.hash,
      file: first,
    });
  }
}

async function followBoundWorktree(entry: GitWorktreeEntry) {
  gitInspectorOpen.value = false;
  await selectGitProject(entry.path);
}

const resourceGitTargetPath = computed(() => {
  const node = resourceGitSelection.value;
  const root = node?.projectRoot || project.projectRoot;
  if (!root) return "";
  const selectedPath = node ? nodeAbsolutePath(node) : root;
  const repositoryPaths = [...(repositoryBranches.value.get(root)?.keys() ?? [])]
    .map((relativePath) => (relativePath ? toDocumentPath(root, relativePath) : root))
    .sort((a, b) => b.length - a.length);
  const owningRepository = repositoryPaths.find(
    (repoPath) => selectedPath === repoPath || selectedPath.startsWith(`${repoPath}/`)
  );
  if (owningRepository) return owningRepository;
  const currentPath = resourceGitState.value.repoPath;
  if (currentPath === root || currentPath.startsWith(`${root}/`)) return currentPath;
  return repositoryPaths[0] ?? "";
});

async function syncResourceGitRepository() {
  const targetPath = resourceGitTargetPath.value;
  if (!targetPath || !gitPanel.value || gitPanel.value.quickActionState.repoPath === targetPath) return;
  await gitPanel.value.selectRepository(targetPath);
}

async function runResourceGitAction(action: GitQuickAction) {
  await syncResourceGitRepository();
  await gitPanel.value?.runQuickAction(action);
  // 分支装饰取自 repositoryBranches，需先刷新再同步资源树。
  await refreshRepositoryBranches();
  await refreshProjectTreeAfterGit();
}

async function refreshGitPanel() {
  await Promise.all([gitPanel.value?.refresh(), refreshRepositoryBranches()]);
  await refreshProjectTreeAfterGit();
}

async function openSourceControl() {
  sideMode.value = "git";
  await nextTick();
  await gitPanel.value?.refresh();
}

const projectSidebarTab = computed(() => (showsExplorer.value ? "resources" : sideMode.value));

// Language-server diagnostics across the project, including files that are not open.
const projectProblems = useProjectProblemsStore();
const problemCount = computed(() => {
  if (!project.projectRoot) return 0;
  const summary = projectProblems.summary(project.projectRoot);
  return summary.errors + summary.warnings;
});

const projectSidebarTabItems = computed<ProjectSidebarTabItem[]>(() => [
  { name: "resources", label: t("project.resources"), icon: FolderOpened, pinned: true },
  { name: "git", label: t("project.sourceControl"), icon: Share, badge: gitChangeCount.value },
  { name: "outline", label: t("project.outline.tab"), icon: List },
  { name: "problems", label: t("project.problems.tab"), icon: Warning, badge: problemCount.value },
  ...(pluginTasks.available ? [{ name: "tasks", label: t("pluginContrib.tasks.tab"), icon: VideoPlay }] : []),
  ...(pluginTreeViews.views.length ? [{ name: "views", label: t("pluginContrib.treeViews.tab"), icon: Grid }] : []),
  ...(pluginTestProviders.providers.length
    ? [{ name: "tests", label: t("pluginContrib.testProviders.tab"), icon: CircleCheck }]
    : []),
]);

async function setProjectPanelRoute(panel: "resources" | "git") {
  if (route.name !== "projects") return;
  const query = { ...route.query };
  if (panel === "git") query.panel = "git";
  else delete query.panel;
  await router.replace({ name: "projects", query });
}

// The outline follows the text editor's model (same URI MonacoTextEditor uses).
const outlineDocument = computed(() => {
  const document = activeDocument.value;
  if (!document?.loaded || document.editor) return undefined;
  return {
    path: document.path,
    projectRoot: document.external ? undefined : projectRootForPath(document.path),
  };
});

function revealOutlineSymbol(line: number, column: number) {
  const path = outlineDocument.value?.path;
  if (path) project.revealDocumentPosition(path, line, column);
}

function setProjectSidebarTab(value: string | number) {
  if (typeof value === "string" && isLocalPanelMode(value)) {
    sideMode.value = value as PanelSideMode;
    return;
  }
  // Switch the panel directly; the sideMode watcher syncs ?panel. Driving the
  // switch through the route fails when the route was already restored without
  // ?panel (tab switch back) while the git panel stayed open: the replace is a
  // duplicate navigation and the query watcher never fires.
  if (value === "git") {
    if (sideMode.value !== "git") void openSourceControl();
    return;
  }
  if (PANEL_SIDE_MODES.has(sideMode.value)) sideMode.value = "explorer";
}

watch(
  () => route.query.panel,
  (panel) => {
    if (route.name !== "projects") return;
    if (panel === "git") {
      if (sideMode.value !== "git") void openSourceControl();
      return;
    }
    if (sideMode.value === "git") sideMode.value = "explorer";
  },
  { immediate: true },
);

watch(sideMode, (mode) => {
  if (route.name !== "projects") return;
  const routedToGit = route.query.panel === "git";
  if ((mode === "git") !== routedToGit) {
    void setProjectPanelRoute(mode === "git" ? "git" : "resources");
  }
});

watch(
  resourceGitTargetPath,
  () => {
    if (sideMode.value !== "git") void nextTick(syncResourceGitRepository);
  },
  { flush: "post" }
);

watch(
  () => project.projectRoot,
  () => {
    resourceGitSelection.value = null;
  }
);

watch(
  () => project.activePath,
  (path) => {
    // Hydration restores the active path before the initial project tree is ready.
    // openProject loads it after initialization; later tab changes load normally.
    if (path && projectViewReady) void ensureDocumentLoaded(path);
  }
);

// 项目增删都可能改变归属关系：新加入的目录可能是已列出仓库的 worktree，
// 移除主仓库则会让它名下的 worktree 需要回到顶层。
watch(
  () => project.projects.map((entry) => entry.root).join("\n"),
  () => {
    if (isDesktopRuntime()) void reloadWorktreeGrouping();
  }
);

watch(
  documentTabsElement,
  () => observeDocumentTabs(),
  { flush: "post" }
);

watch(
  () => [project.documents.length, projectTerminalTabs.value.length],
  async () => {
    await nextTick();
    observeDocumentTabs();
  }
);

watch(
  () => [pendingProjectFiles.value, route.name] as const,
  ([files]) => {
    if (files.length) void openPendingProjectFiles();
  },
  { flush: "post" },
);

onMounted(async () => {
  startProjectClipboardHotkey();
  window.addEventListener("resize", clampGitInspectorToViewport);
  const [storedDeployments, storedGitInspectorWidth] = await Promise.all([
    loadPersistedValue<unknown>(PROJECT_DEPLOYMENTS_STORAGE_KEY),
    loadPersistedValue<unknown>(GIT_INSPECTOR_WIDTH_KEY),
    hydrateSidebarLayout(),
    project.hydrate(),
    terminalWorkspace.hydrate(),
    ssh.hydrate(),
  ]);
  if (
    typeof storedGitInspectorWidth === "number" &&
    Number.isFinite(storedGitInspectorWidth)
  ) {
    gitInspectorWidth.value = storedGitInspectorWidth;
  }
  clampGitInspectorToViewport();
  deployments.value = parsePersistedDeployments(storedDeployments);
  // 工作区绑定 worktree 时以其为默认项目根；用户此前显式打开过项目则保持不变。
  const initialRoot = project.projectRoot || workbench.activeWorktreePath;
  projectPathInput.value = initialRoot;
  setProjectTreeRoots();
  if (initialRoot && canInvoke("local_fs_list_dir")) await openProject(initialRoot, true);
  if (isDesktopRuntime()) await reloadWorktreeGrouping();
  projectViewReady = true;
  await openPendingProjectFiles();
  const terminalId = activeProjectTerminalId.value;
  const terminal = projectTerminalTabs.value.find((item) => item.id === terminalId);
  if (terminal) {
    terminalWorkspace.activateTab(
      terminal.id,
      terminalWorkspace.hasLayout(terminal.id) ? undefined : { initialCwd: terminal.cwd }
    );
  }
});

watch(
  () => pluginTasks.available,
  (available) => {
    if (!available && sideMode.value === "tasks") sideMode.value = "explorer";
  },
);
watch(
  () => pluginTreeViews.views.length,
  (count) => {
    if (!count && sideMode.value === "views") sideMode.value = "explorer";
  },
);
watch(
  () => pluginTestProviders.providers.length,
  (count) => {
    if (!count && sideMode.value === "tests") sideMode.value = "explorer";
  },
);

async function revealPluginTasksTab(event: Event) {
  if (!pluginTasks.available || !project.projectRoot) return;
  sideMode.value = "tasks";
  if ((event as CustomEvent<{ detect?: boolean }>).detail?.detect) await pluginTasks.detectAll();
}
window.addEventListener(PLUGIN_TASKS_REVEAL_EVENT, revealPluginTasksTab);
watch(
  () => project.projectRoot,
  (root) => pluginTasks.setActiveProjectRoot(root || ""),
  { immediate: true },
);

onBeforeUnmount(() => {
  window.removeEventListener(PLUGIN_TASKS_REVEAL_EVENT, revealPluginTasksTab);
  pluginTasks.setActiveProjectRoot("");
  clipboardCompareRequest++;
  if (projectIndexRefreshTimer) clearTimeout(projectIndexRefreshTimer);
  projectIndexRefreshGeneration += 1;
  stopGitInspectorResize();
  window.removeEventListener("resize", clampGitInspectorToViewport);
  stopProjectClipboardHotkey();
  unregisterAiProjectWorkspaceActions?.();
  unregisterEditorContentFlusher();
  documentTabsResizeObserver?.disconnect();
});
</script>

<template>
  <section ref="workspaceElement" class="project-workspace">
    <AppSplitPane
      :model-value="sidebarWidth"
      :collapsed="sidebarCollapsed"
      side="first"
      :min="200"
      :max="(width) => Math.max(200, width * 0.56)"
      :second-min="360"
      collapsible
      :collapse-label="t('project.collapseSidebar')"
      :expand-label="t('project.expandSidebar')"
      :aria-label="t('project.resizeSidebar')"
      @resize-end="persistSidebarWidth"
      @update:collapsed="updateSidebarCollapsed"
    >
      <template #first>
        <aside class="project-sidebar">
      <ProjectSidebarTabs
        :items="projectSidebarTabItems"
        :model-value="projectSidebarTab"
        storage-key="app_project_sidebar_hidden_tabs"
        fallback="resources"
        @update:model-value="setProjectSidebarTab"
      />

      <div v-if="browserAccessBanner" class="project-browser-access" role="status">
        <span>{{
          t(browserAccessBanner.access === "prompt" ? "project.browser.permissionHint" : "project.browser.missingFolder", {
            name: browserAccessBanner.name,
          })
        }}</span>
        <el-button v-if="browserAccessBanner.access === 'prompt'" type="primary" size="small" native-type="button" @click="reauthorizeBrowserProject">
          {{ t("project.browser.reauthorize") }}
        </el-button>
        <el-button v-else size="small" native-type="button" @click="chooseProject">
          {{ t("project.browser.chooseAgain") }}
        </el-button>
      </div>

      <template v-if="showsExplorer">
        <div class="project-explorer-toolbar">
          <el-tooltip :content="t('project.newFile')" placement="bottom">
            <el-button text native-type="button" :disabled="!project.projectRoot" @click="createFile">
              <el-icon><DocumentAdd /></el-icon>
            </el-button>
          </el-tooltip>
          <el-tooltip :content="t('project.newFolder')" placement="bottom">
            <el-button text native-type="button" :disabled="!project.projectRoot" @click="createFolder">
              <el-icon><FolderAdd /></el-icon>
            </el-button>
          </el-tooltip>
          <el-tooltip :content="t('project.refresh')" placement="bottom">
            <el-button text native-type="button" :disabled="!project.projectRoot" @click="refreshProject">
              <el-icon><Refresh /></el-icon>
            </el-button>
          </el-tooltip>
          <el-tooltip :content="t('git.inspector.worktrees')" placement="bottom">
            <el-button
              text
              native-type="button"
              :disabled="!project.projectRoot"
              :aria-label="t('git.inspector.worktrees')"
              @click="openWorktreeInspector"
            >
              <el-icon><Share /></el-icon>
            </el-button>
          </el-tooltip>
          <el-dropdown trigger="click" placement="bottom-start">
            <el-button text
              native-type="button"
              :title="t('project.openedProjects')"
              :aria-label="t('project.openedProjects')"
            >
              <el-icon><FolderOpened /></el-icon>
            </el-button>
            <template #dropdown>
              <el-dropdown-menu class="project-root-menu">
                <template v-if="project.projects.length">
                  <template v-for="entry in project.projects" :key="entry.root">
                    <el-dropdown-item
                      class="project-root-menu__project"
                      :class="{ 'is-current': entry.root === project.projectRoot }"
                      @click="selectSavedProject(entry.root)"
                    >
                      <el-icon><FolderOpened /></el-icon>
                      <span>
                        <strong>{{ entry.name }}</strong>
                        <small :title="entry.root">{{ entry.root }}</small>
                      </span>
                    </el-dropdown-item>
                    <el-dropdown-item
                      class="project-root-menu__action"
                      @click="renameSavedProject(entry.root)"
                    >
                      {{ t("project.rename") }} {{ entry.name }}
                    </el-dropdown-item>
                    <el-dropdown-item
                      class="project-root-menu__action"
                      @click="revealSavedProject(entry.root)"
                    >
                      {{ t("project.reveal") }} {{ entry.name }}
                    </el-dropdown-item>
                    <el-dropdown-item
                      class="project-root-menu__action is-danger"
                      @click="removeSavedProject(entry.root)"
                    >
                      {{ t("project.removeProject") }} {{ entry.name }}
                    </el-dropdown-item>
                  </template>
                </template>
                <el-dropdown-item divided @click="chooseProject">
                  <el-icon><FolderAdd /></el-icon>{{ t("project.chooseFolder") }}
                </el-dropdown-item>
                <el-dropdown-item :disabled="!project.projectRoot" @click="newFileFromTemplate()">
                  <el-icon><DocumentAdd /></el-icon>{{ t("pluginContrib.fileTemplates.newFile") }}
                </el-dropdown-item>
                <el-dropdown-item @click="customTemplatesVisible = true">{{t("pluginContrib.fileTemplates.manage")}}</el-dropdown-item>
                <el-dropdown-item @click="newProjectFromTemplate">
                  <el-icon><FolderAdd /></el-icon>{{ t("pluginContrib.fileTemplates.newProject") }}
                </el-dropdown-item>
                <el-dropdown-item
                  v-if="project.projectRoot && !currentRootIsBound"
                  @click="bindProjectRootToWorkspace"
                >
                  {{ t("project.git.bindWorkspace") }}
                </el-dropdown-item>
                <el-dropdown-item v-else-if="currentRootIsBound" @click="unbindProjectRootFromWorkspace">
                  {{ t("git.worktree.unbind") }}
                </el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
          <el-tooltip
            :content="browserAccess.pickerUnsupported ? t('project.browser.unsupported') : t('project.chooseFolder')"
            placement="bottom"
          >
            <el-button text native-type="button" @click="chooseProject">
              <span class="project-add-folder">+</span>
            </el-button>
          </el-tooltip>
          <el-dropdown v-if="selectedNode" trigger="click" placement="bottom-end">
            <el-button text native-type="button" :aria-label="t('project.fileOperations')">
              <el-icon><MoreFilled /></el-icon>
            </el-button>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item @click="renameSelected">{{
                  t("project.rename")
                }}</el-dropdown-item>
                <el-dropdown-item @click="revealSelected">{{
                  t("project.reveal")
                }}</el-dropdown-item>
                <el-dropdown-item divided @click="deleteSelected">{{
                  t("project.delete")
                }}</el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
          <span class="project-explorer-toolbar__spacer" aria-hidden="true" />
          <div class="project-explorer-toolbar__git-group" :aria-label="t('git.actions')">
            <el-button
              class="project-explorer-toolbar__git-quick"
              text
              native-type="button"
              :disabled="!resourceGitState.repoPath || resourceGitState.loading"
              :title="t('common.refresh')"
              :aria-label="t('common.refresh')"
              @click="runResourceGitAction('refresh')"
            >
              <el-icon><Refresh /></el-icon>
            </el-button>
            <el-button
              class="project-explorer-toolbar__git-quick"
              text
              native-type="button"
              :disabled="!resourceGitState.hasUnstaged || resourceGitState.loading"
              :title="t('git.stageAll')"
              :aria-label="t('git.stageAll')"
              @click="runResourceGitAction('stage-all')"
            >
              <el-icon><Plus /></el-icon>
            </el-button>
            <el-button
              class="project-explorer-toolbar__git-quick"
              text
              native-type="button"
              :disabled="!resourceGitState.repoPath || resourceGitState.loading"
              :title="t('git.pull')"
              :aria-label="t('git.pull')"
              @click="runResourceGitAction('pull')"
            >
              <el-icon><ArrowDown /></el-icon>
            </el-button>
            <el-button
              class="project-explorer-toolbar__git-quick"
              text
              native-type="button"
              :disabled="!resourceGitState.repoPath || resourceGitState.loading"
              :title="t('git.push')"
              :aria-label="t('git.push')"
              @click="runResourceGitAction('push')"
            >
              <el-icon><ArrowUp /></el-icon>
            </el-button>
            <el-dropdown
              trigger="click"
              placement="bottom-end"
              popper-class="project-git-actions-popper"
              @command="runResourceGitAction"
            >
              <el-button
                text
                native-type="button"
                :disabled="!resourceGitState.repoPath"
                :title="t('git.actions')"
                :aria-label="t('git.actions')"
              >
                <el-icon><MoreFilled /></el-icon>
              </el-button>
              <template #dropdown>
                <el-dropdown-menu>
                  <el-dropdown-item command="fetch" :disabled="resourceGitState.loading">
                    <el-icon><Download /></el-icon>{{ t("git.fetch") }}
                  </el-dropdown-item>
                  <el-dropdown-item
                    command="unstage-all"
                    :disabled="!resourceGitState.hasStaged || resourceGitState.loading"
                  >
                    <el-icon><Remove /></el-icon>{{ t("git.unstageAll") }}
                  </el-dropdown-item>
                  <el-dropdown-item command="manage-branches">
                    <el-icon><Share /></el-icon>{{ t("git.inspector.branches") }}
                  </el-dropdown-item>
                  <el-dropdown-item command="show-graph">
                    <el-icon><Guide /></el-icon>{{ t("git.inspector.graph") }}
                  </el-dropdown-item>
                  <el-dropdown-item command="manage-worktrees">
                    <el-icon><FolderOpened /></el-icon>{{ t("git.inspector.worktrees") }}
                  </el-dropdown-item>
                  <el-dropdown-item command="create-branch">
                    <el-icon><CirclePlus /></el-icon>{{ t("git.createBranchTitle") }}
                  </el-dropdown-item>
                  <el-dropdown-item
                    command="rename-branch"
                    :disabled="!resourceGitState.canRenameBranch"
                  >
                    <el-icon><EditPen /></el-icon>{{ t("git.renameBranchTitle") }}
                  </el-dropdown-item>
                  <el-dropdown-item command="manage-tags">
                    <el-icon><EditPen /></el-icon>{{ t("git.inspector.tags") }}
                  </el-dropdown-item>
                  <el-dropdown-item
                    command="undo-last-commit"
                    :disabled="!resourceGitState.canUndoLastCommit"
                    divided
                  >
                    <el-icon><Refresh /></el-icon>{{ t("git.undoLastCommit") }}
                  </el-dropdown-item>
                  <el-dropdown-item command="stash">
                    <el-icon><Operation /></el-icon>{{ t("git.stashTitle") }}
                  </el-dropdown-item>
                  <el-dropdown-item command="manage-stash">
                    <el-icon><Operation /></el-icon>{{ t("git.manageStash") }}
                  </el-dropdown-item>
                  <el-dropdown-item command="interactive-rebase">
                    <el-icon><Operation /></el-icon>{{ t("git.inspector.rebase") }}
                  </el-dropdown-item>
                  <el-dropdown-item
                    command="discard-all"
                    class="is-danger"
                    :disabled="!resourceGitState.totalChanges"
                    divided
                  >
                    <el-icon><Delete /></el-icon>{{ t("git.discardAllTitle") }}
                  </el-dropdown-item>
                </el-dropdown-menu>
              </template>
            </el-dropdown>
          </div>
        </div>

        <div class="project-search-controls">
          <div class="project-search-row">
            <el-input
              v-model="searchQuery"
              class="project-search-input"
              clearable
              :disabled="!project.projectRoot"
              :placeholder="t('project.searchPlaceholder')"
              @input="handleSearchQueryInput"
              @clear="clearSearchQuery"
              @keyup.enter="runSearch"
            >
              <template #suffix>
                <el-button
                  text
                  native-type="button"
                  class="project-search-submit"
                  :disabled="!project.projectRoot || !searchQuery.trim()"
                  :aria-label="t('project.search')"
                  @click="runSearch"
                >
                  <el-icon><Search /></el-icon>
                </el-button>
              </template>
            </el-input>
          </div>
          <div class="project-search-tools" role="toolbar" :aria-label="t('project.search')">
            <el-tooltip :content="t('project.searchContentShort')" placement="bottom">
              <el-button
                text
                native-type="button"
                class="project-search-tool"
                :class="{ 'is-active': includeContent }"
                :disabled="!project.projectRoot"
                :aria-label="t('project.searchContentShort')"
                :aria-pressed="includeContent"
                @click="setIncludeContent(!includeContent)"
              >
                <el-icon><Document /></el-icon>
              </el-button>
            </el-tooltip>
            <el-tooltip :content="t('project.search.caseSensitive')" placement="bottom">
              <el-button
                text
                native-type="button"
                class="project-search-tool"
                :class="{ 'is-active': searchCaseSensitive }"
                :disabled="!project.projectRoot || !advancedSearchAvailable"
                :aria-label="t('project.search.caseSensitive')"
                :aria-pressed="searchCaseSensitive"
                @click="toggleSearchOption('case')"
              >
                <span class="project-search-tool__text">Aa</span>
              </el-button>
            </el-tooltip>
            <el-tooltip :content="t('project.search.wholeWord')" placement="bottom">
              <el-button
                text
                native-type="button"
                class="project-search-tool"
                :class="{ 'is-active': searchWholeWord }"
                :disabled="!project.projectRoot || !advancedSearchAvailable"
                :aria-label="t('project.search.wholeWord')"
                :aria-pressed="searchWholeWord"
                @click="toggleSearchOption('whole')"
              >
                <span class="project-search-tool__whole">ab</span>
              </el-button>
            </el-tooltip>
            <el-tooltip :content="t('project.search.regex')" placement="bottom">
              <el-button
                text
                native-type="button"
                class="project-search-tool"
                :class="{ 'is-active': searchUseRegex }"
                :disabled="!project.projectRoot || !advancedSearchAvailable"
                :aria-label="t('project.search.regex')"
                :aria-pressed="searchUseRegex"
                @click="toggleSearchOption('regex')"
              >
                <span class="project-search-tool__text">.*</span>
              </el-button>
            </el-tooltip>
            <el-tooltip :content="t('project.search.replaceMode')" placement="bottom">
              <el-button
                text
                native-type="button"
                class="project-search-tool"
                :class="{ 'is-active is-replace': searchReplaceMode }"
                :disabled="!project.projectRoot || !advancedSearchAvailable"
                :aria-label="t('project.search.replaceMode')"
                :aria-pressed="searchReplaceMode"
                @click="toggleSearchOption('replace')"
              >
                <el-icon><Operation /></el-icon>
              </el-button>
            </el-tooltip>
            <el-tooltip :content="t('project.search.allProjects')" placement="bottom">
              <el-button
                text
                native-type="button"
                class="project-search-tool"
                :class="{ 'is-active': searchAllProjects }"
                :disabled="!project.projects.length"
                :aria-label="t('project.search.allProjects')"
                :aria-pressed="searchAllProjects"
                @click="toggleSearchOption('allProjects')"
              >
                <el-icon><Collection /></el-icon>
              </el-button>
            </el-tooltip>
          </div>
          <div v-if="searchReplaceMode" class="project-search-replace">
            <el-input
              v-model="replaceQuery"
              :disabled="!project.projectRoot"
              :placeholder="t('project.search.replacePlaceholder')"
              @input="clearReplacePreview"
              @keyup.enter="previewReplace"
            />
            <el-button
              size="small"
              native-type="button"
              :disabled="
                !project.projectRoot || !searchQuery.trim() || searchLoading || replaceApplying
              "
              @click="previewReplace"
            >
              {{ t("project.search.previewReplace") }}
            </el-button>
            <el-button
              size="small"
              type="primary"
              native-type="button"
              :disabled="!replacePreview.length || searchLoading || replaceApplying"
              @click="applyReplace"
            >
              {{ t("project.search.applyReplace") }}
            </el-button>
          </div>
          <p v-if="replacePreview.length" class="project-search-replace-summary">
            {{
              t("project.search.replacePreviewSummary", {
                files: String(replacePreviewFileCount),
                lines: String(replacePreviewLineCount),
              })
            }}
          </p>
        </div>

        <div v-if="errorMessage" class="project-sidebar-message is-error">{{ errorMessage }}</div>

        <el-scrollbar
          v-if="sideMode === 'search'"
          class="project-sidebar-scroll"
          view-class="project-sidebar-scroll__view"
        >
          <button
            v-for="result in searchResults"
            :key="`${result.kind}:${result.relativePath}:${result.line ?? 0}`"
            type="button"
            class="project-search-result"
            @click="openSearchResult(result)"
          >
            <span>{{ result.relativePath }}</span>
            <em v-if="searchAllProjects" class="project-search-result__project">
              {{ result.projectName }}
            </em>
            <small v-if="result.line">
              {{ result.line }}<template v-if="result.col">:{{ result.col }}</template
              >: {{ result.lineText }}
            </small>
            <small v-else>{{ result.isDir ? t("project.folder") : t("project.file") }}</small>
          </button>
          <p v-if="!searchLoading && !searchResults.length" class="project-sidebar-empty">
            {{ t("project.searchEmpty") }}
          </p>
        </el-scrollbar>

        <div
          v-else
          class="project-sidebar-scroll project-explorer-scroll"
        >
          <section v-if="project.projects.length" class="project-file-section">
            <ProjectVirtualTree
              ref="fileTreeRef"
              :key="treeVersion"
              node-key="id"
              leaf-key="leaf"
              :default-expanded-keys="expandedProjectKeys"
              :load="loadTree"
              :expand-on-click-node="true"
              @node-click="onTreeNodeClick"
              @node-collapse="onTreeNodeCollapse"
              @node-expand="onTreeNodeExpand"
              @node-contextmenu="onTreeNodeContextMenu"
            >
              <template #default="{ node, data }">
                <span
                  class="project-tree-node"
                  :class="{
                    'project-tree-node--root': data.isProjectRoot,
                    'project-tree-node--worktree': data.worktree,
                    'is-active-project':
                      data.isProjectRoot && data.projectRoot === project.projectRoot,
                    'project-tree-node--dotfile': !data.isProjectRoot && data.name.startsWith('.'),
                  }"
                >
                  <el-icon v-if="data.worktree" class="project-tree-node__worktree-icon">
                    <Share />
                  </el-icon>
                  <ProjectFileIcon
                    v-else
                    :name="data.name"
                    :is-dir="data.isDir"
                    :expanded="node.expanded"
                    :root="Boolean(data.isProjectRoot)"
                  />
                  <el-input
                    v-if="!data.isProjectRoot && renamingPath === data.path"
                    v-model="renameValue"
                    class="app-inline-rename project-tree-rename"
                    maxlength="255"
                    @click.stop
                    @mousedown.stop
                    @keydown.enter.stop.prevent="saveProjectRename"
                    @keydown.esc.stop.prevent="cancelProjectRename"
                    @blur="saveProjectRename"
                  />
                  <span v-else>{{ data.name }}</span>
                  <!-- A future host Git status mark goes right after the name; the single plugin decoration follows it. -->
                  <em
                    v-if="decorationFor(data)"
                    v-bind="decorationAttrs(decorationFor(data)!)"
                  >{{ decorationFor(data)!.badge }}</em>
                  <em
                    v-if="treeNodeBranch(data)"
                    class="project-tree-node__branch"
                    :title="treeNodeBranch(data)"
                  >
                    <el-icon><ForkSpoon /></el-icon>
                    <span>{{ treeNodeBranch(data) }}</span>
                  </em>
                  <template v-if="data.worktree">
                    <small
                      v-if="formatWorktreeChanges(data.worktree.changes)"
                      class="project-tree-node__stats"
                    >
                      <b>{{ formatWorktreeChanges(data.worktree.changes)?.additions }}</b>
                      <i>{{ formatWorktreeChanges(data.worktree.changes)?.deletions }}</i>
                    </small>
                  </template>
                </span>
              </template>
            </ProjectVirtualTree>
          </section>
          <p v-else class="project-sidebar-empty project-sidebar-empty--compact">
            {{ t("project.noProject") }}
          </p>
        </div>
      </template>

      <ProjectOutlinePanel
        v-if="sideMode === 'outline'"
        :path="outlineDocument?.path"
        :project-root="outlineDocument?.projectRoot"
        @reveal="revealOutlineSymbol"
      />

      <template v-if="sideMode === 'problems'">
        <ProjectProblemsPanel
          v-if="project.projectRoot"
          :project-root="project.projectRoot"
          @open-file="(path: string, line: number, column: number) => openFile(path, line, project.projectRoot, column)"
        />
        <p v-else class="project-sidebar-empty project-sidebar-empty--compact">{{ t("project.noProject") }}</p>
      </template>
      <template v-if="sideMode === 'tasks'">
        <ProjectTasksPanel v-if="project.projectRoot" :project-root="project.projectRoot" />
        <p v-else class="project-sidebar-empty project-sidebar-empty--compact">{{ t("project.noProject") }}</p>
      </template>

      <ProjectPluginViewsPanel
        v-if="sideMode === 'views'"
        :project-root="project.projectRoot || undefined"
        @open-file="(path: string, line?: number) => openFile(path, line, project.projectRoot)"
      />

      <template v-if="sideMode === 'tests'">
        <ProjectPluginTestsPanel
          v-if="project.projectRoot"
          :project-root="project.projectRoot"
          @open-file="(path: string, line?: number) => openFile(path, line, project.projectRoot)"
          @open-address="(address: string, sessionId: string, type: string) => openTestTraceAddress(project.projectRoot, address, sessionId, type)"
        />
        <p v-else class="project-sidebar-empty project-sidebar-empty--compact">{{ t("project.noProject") }}</p>
      </template>

      <ProjectGitPanel
        v-show="sideMode === 'git'"
        ref="gitPanel"
        extended-actions
        :project-root="project.projectRoot"
        :projects="project.projects"
        @select-project="selectGitProject"
        @choose-project="chooseProject"
        @open-file="openGitFile"
        @open-worktree-file="openGitWorktreeFile"
        @open-commit-file="openGitCommitFile"
        @change-count="gitChangeCount = $event"
        @repository-change="handleGitRepositoryChange"
        @extended-action="handleGitExtendedAction"
      />
        </aside>
      </template>
      <template #second>
        <main
          id="project-editor-drop-target"
          class="project-editor-pane"
          data-file-drop-target
          :data-file-drop-label="t('project.external.dropHint')"
        >
      <WorkspaceBottomPanel
        :tabs="debugPanelTabs"
        :active-id="debugPanelActiveId"
        :height="debugPanelHeight"
        :collapsed="debugPanelCollapsed"
        @update:active-id="selectDebugPanelTab($event)"
        @update:height="setDebugPanelHeight"
        @update:collapsed="debugPanelCollapsed = $event"
        @close="debugSessions.close($event)"
      >
      <template #default>
      <template v-if="project.documents.length || projectTerminalTabs.length">
        <div class="project-document-tabs">
          <div ref="documentTabsElement" class="project-document-tabs__tabs-region">
            <AppWorkspaceTabs
              v-model="activeProjectEditorTab"
              class="project-document-tabs__list"
              :aria-label="t('project.openFiles')"
              @tab-remove="removeProjectEditorTab"
            >
              <el-tab-pane
                v-for="document in project.documents"
                :key="document.path"
                :name="document.path"
                closable
              >
                <template #label>
                  <el-dropdown
                    trigger="contextmenu"
                    placement="bottom-start"
                    @command="handleTabCommand(String($event), document.path)"
                  >
                    <span class="project-document-tab__label" :title="documentEditorSource(document)">
                      <ProjectFileIcon :name="document.name" />
                      <span class="project-document-tab__name">{{ document.name }}</span>
                      <el-icon
                        v-if="document.editor"
                        class="project-document-tab__plugin"
                        :aria-label="documentEditorSource(document)"
                      ><Connection /></el-icon>
                      <i
                        v-if="isProjectDocumentDirty(document)"
                        class="project-document-tab__dirty"
                        >●</i
                      >
                      <span v-else-if="document.loading" class="project-document-tab__loading">…</span>
                    </span>
                    <template #dropdown>
                      <el-dropdown-menu>
                        <template v-if="documentHasCustomEditors(document.path)">
                          <OpenWithMenu
                            reopen
                            :relative-path="customEditorRelativePath(document.path) ?? null"
                            :current="documentEditorKey(document)"
                            @select="(key) => reopenDocumentWith(document.path, key)"
                          />
                          <el-dropdown-item command="set-default-editor">{{
                            t("pluginContrib.customEditors.setDefault", { pattern: documentCustomEditorPattern(document.path) ?? "" })
                          }}</el-dropdown-item>
                          <el-dropdown-item
                            v-if="documentHasEditorAssociation(document.path)"
                            command="reset-default-editor"
                          >{{
                            t("pluginContrib.customEditors.resetDefault", { pattern: documentCustomEditorPattern(document.path) ?? "" })
                          }}</el-dropdown-item>
                        </template>
                        <el-dropdown-item
                          v-if="activeDocument?.path !== document.path"
                          :divided="documentHasCustomEditors(document.path)"
                          command="compare-with-active"
                          :disabled="
                            Boolean(document.editor) ||
                            document.externalKind === 'image' ||
                            document.externalKind === 'pdf' ||
                            document.externalKind === 'binary' ||
                            isImageDocument(document.path)
                          "
                        >{{ t("project.editor.compareWithCurrent") }}</el-dropdown-item>
                        <el-dropdown-item
                          command="compare-with-clipboard"
                          :divided="activeDocument?.path === document.path && documentHasCustomEditors(document.path)"
                          :disabled="clipboardCompareLoading || !canComparePathWithClipboard(document.path)"
                        >{{ t("project.editor.compareWithClipboard") }}</el-dropdown-item>
                        <el-dropdown-item divided command="copy-path">{{ t("project.tab.copyPath") }}</el-dropdown-item>
                        <el-dropdown-item command="reveal">{{ t("project.tab.reveal") }}</el-dropdown-item>
                        <el-dropdown-item divided command="close">{{ t("project.closeTab") }}</el-dropdown-item>
                        <el-dropdown-item command="close-other">{{
                          t("project.tab.closeOther")
                        }}</el-dropdown-item>
                        <el-dropdown-item command="close-left">{{
                          t("project.tab.closeLeft")
                        }}</el-dropdown-item>
                        <el-dropdown-item command="close-right">{{
                          t("project.tab.closeRight")
                        }}</el-dropdown-item>
                        <el-dropdown-item command="close-saved">{{
                          t("project.tab.closeSaved")
                        }}</el-dropdown-item>
                        <el-dropdown-item divided command="close-all">{{
                          t("project.tab.closeAll")
                        }}</el-dropdown-item>
                      </el-dropdown-menu>
                    </template>
                  </el-dropdown>
                </template>
              </el-tab-pane>
              <el-tab-pane
                v-for="terminal in projectTerminalTabs"
                :key="terminal.id"
                :name="`terminal:${terminal.id}`"
                closable
              >
                <template #label>
                  <span class="project-document-tab__label" :title="terminal.cwd">
                    <el-icon><Connection /></el-icon>
                    <span class="project-document-tab__name">{{ terminal.name }}</span>
                  </span>
                </template>
              </el-tab-pane>
            </AppWorkspaceTabs>
          </div>
          <el-dropdown
            v-if="tabsOverflow"
            ref="documentTabsOverflowDropdown"
            class="project-document-tabs__overflow-menu"
            placement="bottom-end"
            trigger="click"
            :hide-on-click="false"
            :teleported="false"
            popper-class="project-document-tabs__overflow-popper"
          >
            <el-button
              text
              native-type="button"
              class="project-document-tabs__overflow"
              :aria-label="t('project.editor.tabs.overflow')"
              :title="t('project.editor.tabs.overflow')"
            >
              <el-icon><MoreFilled /></el-icon>
            </el-button>
            <template #dropdown>
              <el-dropdown-menu class="project-document-tabs__overflow-dropdown">
                <el-scrollbar
                  class="project-document-tabs__overflow-scroll"
                  max-height="min(70vh, 520px)"
                  always
                >
                  <li
                    v-for="document in project.documents"
                    :key="document.path"
                    role="none"
                    class="project-document-tabs__overflow-item"
                    :class="{
                      'is-active': !activeProjectTerminalId && project.activePath === document.path,
                    }"
                  >
                    <el-button
                      text
                      native-type="button"
                      class="project-document-tabs__overflow-select"
                      :title="document.path"
                      @click="selectOverflowTab(document.path)"
                    >
                      <ProjectFileIcon :name="document.name" />
                      <span class="project-document-tabs__overflow-name">{{ document.name }}</span>
                      <i
                        v-if="isProjectDocumentDirty(document)"
                        class="project-document-tabs__overflow-dirty"
                        >●</i
                      >
                    </el-button>
                    <el-button
                      text
                      circle
                      native-type="button"
                      class="project-document-tabs__overflow-close"
                      :aria-label="t('project.closeTab')"
                      :title="t('project.closeTab')"
                      @click.stop="removeProjectEditorTab(document.path)"
                    >
                      <span
                        class="project-document-tabs__overflow-close-icon"
                        aria-hidden="true"
                      />
                    </el-button>
                  </li>
                  <li
                    v-for="terminal in projectTerminalTabs"
                    :key="terminal.id"
                    role="none"
                    class="project-document-tabs__overflow-item"
                    :class="{ 'is-active': activeProjectTerminalId === terminal.id }"
                  >
                    <el-button
                      text
                      native-type="button"
                      class="project-document-tabs__overflow-select"
                      :title="terminal.cwd"
                      @click="selectOverflowTab(`terminal:${terminal.id}`)"
                    >
                      <span class="project-document-tabs__overflow-name">{{ terminal.name }}</span>
                    </el-button>
                    <el-button
                      text
                      circle
                      native-type="button"
                      class="project-document-tabs__overflow-close"
                      :aria-label="t('common.closeTab')"
                      :title="t('common.closeTab')"
                      @click.stop="removeProjectEditorTab(`terminal:${terminal.id}`)"
                    >
                      <span
                        class="project-document-tabs__overflow-close-icon"
                        aria-hidden="true"
                      />
                    </el-button>
                  </li>
                </el-scrollbar>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
        </div>
      </template>
      <header class="project-editor-toolbar">
        <div class="project-editor-toolbar__path">
          <el-scrollbar v-if="activeDocumentBreadcrumbs.length" class="project-editor-breadcrumb">
            <nav class="project-editor-breadcrumb__content">
            <template
              v-for="(breadcrumb, index) in activeDocumentBreadcrumbs"
              :key="breadcrumb.path"
            >
              <el-button text native-type="button"
                :class="{ 'is-current': breadcrumb.isFile }"
                :aria-current="breadcrumb.isFile ? 'page' : undefined"
                :title="breadcrumb.path"
                @click="navigateBreadcrumb(breadcrumb)"
              >
                {{ breadcrumb.label }}
              </el-button>
              <span v-if="index < activeDocumentBreadcrumbs.length - 1" aria-hidden="true">›</span>
            </template>
            </nav>
          </el-scrollbar>
        </div>
        <el-tag v-if="editorComparison?.kind === 'clipboard'" size="small" :title="t('project.editor.clipboardComparisonHint')">
          {{ t("project.editor.clipboardComparisonHint") }}
        </el-tag>
        <el-tooltip :content="t('project.save')" placement="bottom">
          <el-button text native-type="button"
            :disabled="!activeDocument || activeDocumentIsImage || !activeDirty"
            @click="saveDocument()"
          >
            <el-icon><Document /></el-icon>
          </el-button>
        </el-tooltip>
        <el-tooltip :content="t('project.saveAll')" placement="bottom">
          <el-button text native-type="button" :disabled="!project.dirtyCount" @click="saveAllDocuments">
            <el-icon><FolderOpened /></el-icon>
          </el-button>
        </el-tooltip>
        <el-tooltip :content="t('project.undo')" placement="bottom">
          <el-button text native-type="button"
            :disabled="
              !activeDocument?.loaded ||
              activeDocumentIsImage ||
              isPreviewingDocument ||
              isComparingDocument ||
              !canUndo
            "
            @click="undoEditor"
          >
            <!-- Element Plus 没有专用的撤销图标，用通用的左向弯箭头，避免与刷新图标混淆 -->
            <el-icon>
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                <path d="M8.5 4 4 8.5l4.5 4.5" />
                <path d="M4 8.5h9.5a6.5 6.5 0 0 1 0 13H9" />
              </svg>
            </el-icon>
          </el-button>
        </el-tooltip>
        <el-tooltip :content="t('project.redo')" placement="bottom">
          <el-button text native-type="button"
            :disabled="
              !activeDocument?.loaded ||
              activeDocumentIsImage ||
              isPreviewingDocument ||
              isComparingDocument ||
              !canRedo
            "
            @click="redoEditor"
          >
            <!-- 与撤销图标镜像的右向弯箭头 -->
            <el-icon>
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                <path d="M15.5 4 20 8.5l-4.5 4.5" />
                <path d="M20 8.5h-9.5a6.5 6.5 0 0 0 0 13H15" />
              </svg>
            </el-icon>
          </el-button>
        </el-tooltip>
        <el-tooltip :content="t('project.editor.compareOpenFile')" placement="bottom">
          <el-dropdown
            trigger="click"
            placement="bottom-end"
            :disabled="
              !activeDocument?.loaded ||
              activeDocumentIsImage ||
              gitCompareLoading ||
              blameLoading ||
              clipboardCompareLoading ||
              !canComparePathWithClipboard(activeDocument.path)
            "
            @command="handleOpenDocumentComparison(String($event))"
          >
            <el-button
              text
              native-type="button"
              :class="{ 'is-active': isComparingOpenDocument }"
              :aria-label="t('project.editor.compareOpenFile')"
            >
              <el-icon><CopyDocument /></el-icon>
            </el-button>
            <template #dropdown>
              <el-dropdown-menu class="project-editor-compare-menu">
                <el-dropdown-item command="clipboard">{{ t("project.editor.compareWithClipboard") }}</el-dropdown-item>
                <el-dropdown-item v-if="isComparingOpenDocument" command="exit">
                  {{ t("project.editor.exitFileCompare") }}
                </el-dropdown-item>
                <el-dropdown-item
                  v-for="document in comparableOpenDocuments"
                  :key="document.path"
                  :command="document.path"
                  :divided="isComparingOpenDocument && document === comparableOpenDocuments[0]"
                >
                  <ProjectFileIcon :name="document.name" />
                  <span :title="document.path">{{ document.name }}</span>
                </el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
        </el-tooltip>
        <div class="project-editor-toolbar__git-actions">
          <el-tooltip
            :content="
              t(isComparingDocument ? 'project.git.exitCompare' : 'project.git.compareWithHead')
            "
            placement="bottom"
          >
            <el-button text native-type="button"
              :class="{ 'is-active': isComparingDocument }"
              :disabled="
                !activeDocument?.loaded ||
                activeDocumentIsImage ||
                gitCompareLoading ||
                blameLoading
              "
              @click="toggleGitComparison"
            >
              <el-icon><Connection /></el-icon>
            </el-button>
          </el-tooltip>
          <el-dropdown
            trigger="click"
            placement="bottom-end"
            :disabled="
              !activeDocument?.loaded || activeDocumentIsImage || gitCompareLoading || blameLoading
            "
            @visible-change="loadGitBranches"
            @command="compareActiveDocumentWithRef(String($event))"
          >
            <el-button text native-type="button"
              class="project-editor-toolbar__git-dropdown"
              :aria-label="t('project.git.compareWithRef')"
            >
              <AppChevron class="project-editor-toolbar__git-chevron" direction="down" />
            </el-button>
            <template #dropdown>
              <el-dropdown-menu class="project-editor-git-menu">
                <el-dropdown-item command="HEAD">HEAD</el-dropdown-item>
                <el-dropdown-item v-if="gitBranchesLoading" disabled>
                  {{ t("project.loadingFile") }}
                </el-dropdown-item>
                <template v-else-if="gitBranches?.branches.length">
                  <el-dropdown-item divided disabled>{{
                    t("project.git.compareWithRef")
                  }}</el-dropdown-item>
                  <el-dropdown-item
                    v-for="branch in gitBranches.branches"
                    :key="branch.name"
                    :command="branch.name"
                  >
                    <span>{{ branch.name }}</span>
                    <small v-if="branch.is_current">{{ t("project.git.currentBranch") }}</small>
                  </el-dropdown-item>
                </template>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
        </div>
        <el-tooltip
          :content="t(blameEnabled ? 'project.git.hideBlame' : 'project.git.blame')"
          placement="bottom"
        >
          <el-button text native-type="button"
            :class="{ 'is-active': blameEnabled }"
            :disabled="
              !activeDocument?.loaded ||
              activeDocumentIsImage ||
              isComparingDocument ||
              blameLoading ||
              gitCompareLoading
            "
            @click="toggleBlame"
          >
            <el-icon><ForkSpoon /></el-icon>
          </el-button>
        </el-tooltip>
        <el-tooltip
          v-if="activeDocumentSupportsPreview"
          :content="t(isPreviewingDocument ? 'project.editor.edit' : 'project.editor.preview')"
          placement="bottom"
        >
          <el-button text native-type="button"
            :aria-label="t(isPreviewingDocument ? 'project.editor.edit' : 'project.editor.preview')"
            @click="togglePreviewMode"
          >
            <el-icon><EditPen v-if="isPreviewingDocument" /><View v-else /></el-icon>
          </el-button>
        </el-tooltip>
        <el-popover
          v-if="!activeDocumentIsImage"
          v-model:visible="editorSettingsOpen"
          placement="bottom-end"
          trigger="click"
          :width="340"
          :teleported="false"
          popper-class="project-editor-settings-popper"
        >
          <template #reference>
            <el-button text native-type="button" :aria-label="t('project.editor.settings')">
              <el-icon><Setting /></el-icon>
            </el-button>
          </template>
          <ProjectEditorSettings
            :preferences="project.editorPreferences"
            :native="isDesktopRuntime()"
            @change="project.updateEditorPreferences"
            @reset="resetEditorPreferences"
            @close="editorSettingsOpen = false"
            @formatters="editorSettingsOpen = false; requestOpenSettings()"
          />
        </el-popover>
        <el-tooltip :content="t('project.lsp.button')" placement="bottom" :teleported="false">
          <el-button text native-type="button" :aria-label="t('project.lsp.button')" @click="openLspDialog">
            <el-icon><Connection /></el-icon>
          </el-button>
        </el-tooltip>
      </header>

      <ProjectLanguageServersDialog
        v-model="lspDialogVisible"
        :project-root="project.projectRoot"
        :lsp-settings="project.lspSettings"
        @update:lsp-settings="project.updateLspSettings($event)"
      />
      <template v-if="project.documents.length || projectTerminalTabs.length">

        <div
          v-if="projectTerminalTabs.length"
          v-show="activeProjectTerminal"
          class="project-editor-surface project-terminal-surface"
        >
          <TerminalPaneLayout
            :node="terminalWorkspace.root"
            :active-pane-id="terminalWorkspace.activePaneId"
            :can-close="terminalWorkspace.canClose"
            :can-split="terminalWorkspace.canSplit"
            @select="terminalWorkspace.selectPane"
            @close="closeProjectTerminalPane"
            @reload="reloadProjectTerminalPane"
            @use-default-shell="useDefaultShellInProjectTerminalPane"
            @split="splitProjectTerminalPane"
            @resize="terminalWorkspace.resizeSplit"
            @resize-end="terminalWorkspace.persistLayout"
          />
        </div>
        <div
          v-if="customEditorPaneList.length"
          v-show="activeDocument?.editor"
          class="project-editor-surface project-custom-editor-surface"
        >
          <CustomEditorPanes
            ref="customEditorPanes"
            :panes="customEditorPaneList"
            :active-path="activeDocument?.editor ? activeDocument.path : null"
            @dirty="onCustomEditorDirty"
            @failed="onCustomEditorFailed"
            @reopen-text="onCustomEditorReopenText"
            @close="onCustomEditorClose"
          />
        </div>
        <ProjectEditorDebugPopups
          :condition="editorDebug.conditionDraft.value"
          :entry-menu="editorDebug.entryMenu.value"
          @save-condition="editorDebug.saveCondition"
          @cancel-condition="editorDebug.cancelCondition"
          @run="editorDebug.runEntry"
          @close-entry-menu="editorDebug.closeEntryMenu"
        />
        <div v-if="activeDocument && !activeDocument.editor" class="project-editor-surface">
          <ImagePreview
            v-if="activeDocumentIsImage && activeDocument.loaded"
            :src="activeDocument.content"
            :alt="activeDocument.name"
          />
          <ExternalFilePreview
            v-else-if="activeDocumentIsExternalPreview && activeDocument.loaded"
            :document="activeDocument"
            :active="projectViewActive"
            @open-system="openActiveDocumentWithSystem"
          />
          <MarkdownPreview
            v-else-if="isPreviewingDocument && activeDocument.loaded"
            :content="activeDocument.content"
          />
          <div v-else-if="isComparingBinaryDocument" class="project-editor-empty">
            <el-empty :image-size="72" :description="t('project.git.binaryDiffUnsupported')" />
          </div>
          <MonacoDiffEditor
            v-else-if="isComparingDocument && activeDocument.loaded && editorComparison"
            :path="activeDocument.path"
            :original-path="editorComparison.originalPath"
            :original-content="editorComparison.originalContent"
            :modified-content="editorComparison.modifiedContent ?? activeDocument.content"
                                                            :preferences="project.editorPreferences"
            navigation
          />
          <MonacoTextEditor
            v-else-if="activeDocument.loaded"
            ref="monacoEditor"
            kind="project"
            :model-value="activeDocument.content"
            :path="activeDocument.path"
            :open-paths="openDocumentPaths"
            defer-content-sync
            :saved-content-for="savedDocumentContent"
                                                :preferences="project.editorPreferences"
            :project-root="activeDocument.external ? undefined : projectRootForPath(activeDocument.path)"
            :reveal-position="activeDocument.revealPosition"
            :blame-enabled="blameEnabled"
            :blame-hunks="blameHunks"
            :view-state="project.editorViewStates[activeDocument.path]"
            :readonly="activeDocument.externalKind === 'debug-source'"
            :breakpoints="editorDebug.editorProps.value.breakpoints"
            :verified-lines="editorDebug.editorProps.value.verifiedLines"
            :breakpoint-details="editorDebug.editorProps.value.breakpointDetails"
            :paused-line="editorDebug.editorProps.value.pausedLine"
            :entry-points="editorDebug.editorProps.value.entryPoints"
            :entry-point-states="editorDebug.editorProps.value.entryPointStates"
            :inline-variables="editorDebug.editorProps.value.inlineVariables"
            @toggle-breakpoint="editorDebug.toggleBreakpoint"
            @edit-breakpoint="editorDebug.editBreakpoint"
            @set-breakpoint-enabled="editorDebug.setBreakpointEnabled"
            @remove-breakpoint="editorDebug.removeBreakpoint"
            @entry-point-click="editorDebug.openEntryMenu"
            @breakpoints-moved="editorDebug.moveBreakpoints"
            @sync-content="project.updateDocumentContent"
            @dirty-change="project.setDocumentDirty"
            @model-edit="autoSave.edited"
            @editor-departure="autoSave.departure"
            @ready="lsp.ensureLoaded()"
            @view-state="project.setEditorViewState"
            @history-state="updateEditorHistory"
            @save="saveDocumentFromEditor()"
            @reveal-complete="project.consumeRevealPosition(activeDocument.path)"
          />
          <div v-else-if="activeDocument.errorKey" class="project-editor-empty">
            <el-empty :image-size="72" :description="t('project.readFailedFile', { name: activeDocument.name })">
              <p v-if="activeDocument.errorDetail" class="project-document-read-error">{{ documentErrorDetail(activeDocument.errorDetail) }}</p>
              <el-button size="small" @click="ensureDocumentLoaded(activeDocument.path)">{{ t("project.retryRead") }}</el-button>
            </el-empty>
          </div>
          <div v-else class="project-editor-empty">
            <el-empty :image-size="72" :description="t('project.loadingFile')" />
          </div>
        </div>
      </template>
      <div v-else class="project-editor-empty">
        <el-empty :image-size="88" :description="t('project.selectFile')" />
      </div>
      <footer v-if="runProjectRoot" class="project-editor-run-bar">
        <RunConfigSelector
          :project-root="runProjectRoot"
          :active-file="runActiveFile"
          :active-language-id="runActiveLanguageId"
          @edit-configurations="requestRunConfigurations('edit')"
          @new-configuration="requestRunConfigurations('new')"
        />
        <LspProgressIndicator />
      </footer>
      </template>
      <template #tab-label="{ tab }">
        <span class="debug-session-tab__state" :class="`is-${debugSessionState(tab.id)}`" aria-hidden="true" />
        <span>{{ tab.title }}</span>
      </template>
      <template #tab="{ tab, active }">
        <DebugLaunchProgress
          v-if="debugLaunchFor(tab.id)"
          :launch="debugLaunchFor(tab.id)!"
          :visible="active && !debugPanelCollapsed && projectViewActive"
          @cancel="debugSessions.cancelLaunch(debugLaunchFor(tab.id)!.seq)"
          @shown="(shown: boolean) => debugSessions.setLaunchShown(debugLaunchFor(tab.id)?.seq ?? -1, shown)"
        />
        <DebugToolWindow
          v-else
          :session-id="tab.id"
          :active="active && !debugPanelCollapsed && projectViewActive"
          :view-active="projectViewActive"
          :resolve-cursor="debugCursorLocation"
          @open-location="(path: string, line: number) => openPathAt(path, line)"
          @open-address="(address: string, type: string, line: number) => openDebugAddress(tab.id, address, type, line)"
        />
      </template>
      </WorkspaceBottomPanel>
        </main>
      </template>
    </AppSplitPane>

    <AppContextMenu
      v-if="contextMenu"
      :open="true"
      :x="contextMenu.x"
      :y="contextMenu.y"
      :width="218"
      popper-class="project-context-menu"
      @close="closeProjectContextMenu"
    >
      <el-dropdown-item @click="revealNode(contextMenu.node)">
        <el-icon><FolderOpened /></el-icon>{{ t("project.reveal") }}
      </el-dropdown-item>
      <el-dropdown-item @click="copyNodePath(contextMenu.node)">
        <el-icon><CopyDocument /></el-icon>{{ t("project.context.copyAbsolutePath") }}
      </el-dropdown-item>
      <el-dropdown-item @click="copyNodePath(contextMenu.node, true)">
        <el-icon><CopyDocument /></el-icon>{{ t("project.context.copyRelativePath") }}
      </el-dropdown-item>
      <el-dropdown-item divided @click="addNodeToAi(contextMenu.node)">
        <el-icon><ChatDotRound /></el-icon>{{ t("project.context.addToAi") }}
      </el-dropdown-item>

      <template v-if="contextMenu.node.isProjectRoot">
        <el-dropdown-item
          divided
          :disabled="contextMenu.node.projectRoot === boundWorktreePath"
          @click="bindNodeToWorkspace(contextMenu.node)"
        >
          <el-icon><Share /></el-icon>{{ t("project.context.bindToWorkspace") }}
        </el-dropdown-item>
        <el-dropdown-item divided @click="openProjectNotes(contextMenu.node, true)">
          <el-icon><Plus /></el-icon>{{ t("notes.link.newLinked") }}
        </el-dropdown-item>
        <el-dropdown-item @click="openProjectNotes(contextMenu.node)">
          <el-icon><Notebook /></el-icon>{{ t("notes.link.viewLinked", { count: linkedNotes.countOf("project", contextMenu.node.projectRoot) }) }}
        </el-dropdown-item>
        <el-dropdown-item
          divided
          class="is-danger"
          @click="removeProjectFromContextMenu(contextMenu.node)"
        >
          <el-icon><Delete /></el-icon>{{ t("project.removeProject") }}
        </el-dropdown-item>
      </template>

      <template v-if="contextMenu.node.isDir">
        <el-dropdown-item divided @click="openTerminalAtNode(contextMenu.node)">
          <el-icon><Connection /></el-icon>{{ t("project.context.openTerminal") }}
        </el-dropdown-item>
        <el-dropdown-item @click="openTerminalInProject(contextMenu.node)">
          <el-icon><Connection /></el-icon>{{ t("project.context.openTerminalInProject") }}
        </el-dropdown-item>
        <el-dropdown-item @click="searchInNode(contextMenu.node)">
          <el-icon><Search /></el-icon>{{ t("project.context.searchHere") }}
        </el-dropdown-item>
        <el-dropdown-item @click="openRemoteServerPicker(contextMenu.node)">
          <el-icon><Connection /></el-icon>{{ t("project.remote.openServer") }}
        </el-dropdown-item>
        <el-dropdown-item @click="openDeploymentDialog(contextMenu.node)">
          <el-icon><Setting /></el-icon>{{ t("project.remote.configure") }}
        </el-dropdown-item>
        <el-dropdown-item :disabled="deploying" @click="deployNode(contextMenu.node)">
          <el-icon><Upload /></el-icon>{{ t("project.remote.upload") }}
        </el-dropdown-item>
        <el-dropdown-item divided @click="createFileInNode(contextMenu.node)">
          <el-icon><DocumentAdd /></el-icon>{{ t("project.newFile") }}
        </el-dropdown-item>
        <el-dropdown-item @click="createFolderInNode(contextMenu.node)">
          <el-icon><FolderAdd /></el-icon>{{ t("project.newFolder") }}
        </el-dropdown-item>
        <el-dropdown-item @click="newFileFromTemplate(contextMenu.node)">
          <el-icon><DocumentAdd /></el-icon>{{ t("pluginContrib.fileTemplates.newFile") }}
        </el-dropdown-item>
        <el-dropdown-item divided @click="pasteFilesIntoNode(contextMenu.node)">
          <el-icon><Download /></el-icon>{{ t("project.context.paste") }}
        </el-dropdown-item>
      </template>

      <template v-else>
        <OpenWithMenu
          divided
          :relative-path="customEditorRelativePath(nodeAbsolutePath(contextMenu.node), contextMenu.node.projectRoot) ?? null"
          :current="openDocumentEditorKey(nodeAbsolutePath(contextMenu.node))"
          @select="openContextNodeWith"
        />
        <el-dropdown-item
          divided
          :disabled="!canCompareResourceNodeWithCurrent(contextMenu.node)"
          @click="compareResourceNodeWithCurrent(contextMenu.node)"
        >
          <el-icon><CopyDocument /></el-icon>{{ t("project.editor.compareWithCurrent") }}
        </el-dropdown-item>
        <el-dropdown-item
          :disabled="clipboardCompareLoading || !canComparePathWithClipboard(nodeAbsolutePath(contextMenu.node))"
          @click="compareResourceNodeWithClipboard(contextMenu.node)"
        >
          <el-icon><CopyDocument /></el-icon>{{ t("project.editor.compareWithClipboard") }}
        </el-dropdown-item>
        <el-dropdown-item @click="openBranchPicker(contextMenu.node)">
          <el-icon><ForkSpoon /></el-icon>{{ t("project.git.compareWithRef") }}
        </el-dropdown-item>
        <el-dropdown-item @click="openRemoteServerPicker(contextMenu.node)">
          <el-icon><Connection /></el-icon>{{ t("project.remote.openServer") }}
        </el-dropdown-item>
        <el-dropdown-item :disabled="deploying" @click="deployNode(contextMenu.node)">
          <el-icon><Upload /></el-icon>{{ t("project.remote.upload") }}
        </el-dropdown-item>
      </template>

      <template v-if="!contextMenu.node.isProjectRoot">
        <el-dropdown-item divided @click="copyNodeToClipboard(contextMenu.node)">
          <el-icon><DocumentCopy /></el-icon>{{ t("project.context.copy") }}
        </el-dropdown-item>
        <el-dropdown-item @click="renameNode(contextMenu.node)">
          <el-icon><EditPen /></el-icon>{{ t("project.rename") }}
        </el-dropdown-item>
        <el-dropdown-item @click="copyOrMoveNode(contextMenu.node, 'local_fs_copy_to')">
          <el-icon><CopyDocument /></el-icon>{{ t("project.context.copyTo") }}
        </el-dropdown-item>
        <el-dropdown-item @click="copyOrMoveNode(contextMenu.node, 'local_fs_move_to')">
          <el-icon><Operation /></el-icon>{{ t("project.context.moveTo") }}
        </el-dropdown-item>
        <el-dropdown-item class="is-danger" @click="deleteNode(contextMenu.node)">
          <el-icon><Delete /></el-icon>{{ t("project.delete") }}
        </el-dropdown-item>
      </template>

      <PluginMenuItems
        menu="project/tree/context"
        :host="projectTreePluginMenuHost"
        :context="projectTreePluginMenuContext"
      />
      <PluginServiceMenuItems :target="projectTreeServiceTarget" />
    </AppContextMenu>

    <CustomFileTemplatesDialog v-if="customTemplatesVisible" v-model="customTemplatesVisible"/>
    <RunConfigDialog
      v-if="runConfigurationRequest"
      :model-value="true"
      :project-root="runConfigurationRequest.projectRoot"
      :mode="runConfigurationRequest.mode"
      @update:model-value="closeRunConfigurations"
    />
    <DebugEvaluateDialog
      v-if="editorDebug.evaluateRequest.value"
      :key="editorDebug.evaluateRequest.value.id"
      :model-value="true"
      :prefill="editorDebug.evaluateRequest.value.prefill"
      :evaluate-on-open="editorDebug.evaluateRequest.value.evaluate"
      @update:model-value="!$event && closeEvaluateDialog()"
    />
    <NewFromTemplateDialog
      v-if="fileTemplateDialogMounted"
      v-model="fileTemplateDialogVisible"
      :kind="fileTemplateDialogKind"
      :templates="fileTemplatesStore.templates"
      :project-root="fileTemplateProjectRoot"
      :parent-path="fileTemplateParentPath"
      @created="onFileTemplateCreated"
    />

    <ProjectRemoteDialogs
      v-model:branch-visible="branchPickerVisible"
      v-model:remote-visible="remoteServerPickerVisible"
      v-model:deployment-visible="deploymentDialogVisible"
      v-model:remote-profile-id="remoteServerProfileId"
      v-model:deployment-profile-id="deploymentProfileId"
      v-model:deployment-remote-root="deploymentRemoteRoot"
      v-model:deployment-excludes="deploymentExcludes"
      :branches="branchPickerBranches?.branches"
      :branch-loading="branchPickerLoading"
      :profiles="ssh.sortedProfiles"
      :deployment-scope-label="deploymentScopeLabel"
      :deploying="deploying"
      @compare="compareNodeWithRef"
      @open-remote="openSelectedRemoteServer"
      @save-deployment="saveDeploymentConfig"
      @remote-closed="remoteServerNode = null"
      @deployment-closed="deploymentNode = null"
    />

    <el-drawer
      v-model="gitInspectorOpen"
      class="git-inspector-drawer"
      :class="{
        'is-resizing': resizingGitInspector,
        'is-branch-panel': gitInspector === 'branches',
        // 这些面板内容会超出抽屉高度：抽屉本身不滚动，交给面板里的 ElScrollbar，避免出现原生滚动条。
        'has-panel-scrollbar': ['branches', 'worktrees', 'tags', 'shelf', 'stash'].includes(gitInspector),
      }"
      :title="gitInspectorTitle"
      direction="rtl"
      :size="gitInspectorSize"
      :close-on-click-modal="!worktreeBatchActive && !branchPanelBusy"
      :close-on-press-escape="!worktreeBatchActive && !branchPanelBusy"
      :show-close="!branchPanelBusy"
      destroy-on-close
      @closed="worktreeBatchActive = false; branchPanelBusy = false"
      append-to-body
    >
      <div
        v-if="gitInspectorResizable"
        class="git-inspector-drawer__resize-handle"
        role="separator"
        aria-orientation="vertical"
        :aria-label="t('git.inspector.resize')"
        :aria-valuenow="gitInspectorWidth"
        :aria-valuemin="gitInspectorWidthLimits().minimum"
        :aria-valuemax="gitInspectorWidthLimits().maximum"
        tabindex="0"
        @pointerdown="startGitInspectorResize"
        @keydown="resizeGitInspectorWithKeyboard"
      />
      <el-empty v-if="!gitInspectorRepoPath" :description="t('git.graph.noRepo')" />
      <template v-else>
        <section v-if="gitInspector === 'graph'" class="git-inspector git-inspector--graph">
          <div class="git-inspector__actions git-inspector__actions--spread">
            <span>{{ t("git.graph.count", { count: gitGraphLogs.length }) }}</span>
            <el-button size="small" :icon="Refresh" @click="loadGitGraph">
              {{ t("common.refresh") }}
            </el-button>
          </div>
          <GitCommitGraph :entries="gitGraphLogs" @select="openGraphCommit" />
        </section>
        <el-scrollbar
          v-else-if="gitInspector === 'worktrees'"
          class="git-inspector-drawer__scrollbar"
        >
          <GitWorktreePanel
            :repo-path="gitInspectorRepoPath"
            @bind="followBoundWorktree"
            @changed="reloadWorktreeGrouping"
            @batch-active="worktreeBatchActive = $event"
          >
            <template #header>
              <div v-if="worktreeRepoOptions.length > 1" class="git-worktree-repo">
                <label class="git-advanced-field git-advanced-field--wide">
                  <span>{{ t("git.repository") }}</span>
                  <el-select
                    :model-value="worktreeRepoSelection"
                    :placeholder="t('git.repositoryPlaceholder')"
                    size="small"
                    filterable
                    @change="showWorktreeInspector"
                  >
                    <el-option
                      v-for="repository in worktreeRepoOptions"
                      :key="repository.path"
                      :label="repository.name"
                      :value="repository.path"
                    />
                  </el-select>
                </label>
              </div>
            </template>
          </GitWorktreePanel>
        </el-scrollbar>
        <GitBranchPanel
          v-else-if="gitInspector === 'branches'"
          :repo-path="gitInspectorRepoPath"
          @busy="branchPanelBusy = $event"
          @changed="refreshGitPanel"
        >
          <template #header>
            <div class="git-worktree-repo">
              <label class="git-advanced-field git-advanced-field--wide">
                <span>{{ t("git.repository") }}</span>
                <el-select
                  :model-value="gitInspectorRepoPath"
                  :placeholder="t('git.repositoryPlaceholder')"
                  :loading="branchRepoLoading"
                  :disabled="branchRepoLoading || branchPanelBusy"
                  size="small"
                  filterable
                  @change="selectBranchRepository"
                >
                  <el-option
                    v-for="repository in branchRepoOptions"
                    :key="repository.path"
                    :label="repository.name"
                    :value="repository.path"
                    :title="repository.path"
                  />
                </el-select>
                <span class="git-branch-panel__repo-path">{{ gitInspectorRepoPath }}</span>
              </label>
            </div>
          </template>
        </GitBranchPanel>
        <el-scrollbar
          v-else-if="gitInspector === 'tags'"
          class="git-inspector-drawer__scrollbar"
        >
          <GitTagPanel
            :repo-path="gitInspectorRepoPath"
            @changed="refreshGitPanel"
          />
        </el-scrollbar>
        <el-scrollbar
          v-else-if="gitInspector === 'shelf'"
          class="git-inspector-drawer__scrollbar"
        >
          <GitShelfPanel
            :repo-path="gitInspectorRepoPath"
            @changed="refreshGitPanel"
          />
        </el-scrollbar>
        <el-scrollbar
          v-else-if="gitInspector === 'stash'"
          class="git-inspector-drawer__scrollbar"
        >
          <GitStashPanel
            :repo-path="gitInspectorRepoPath"
            @changed="refreshGitPanel"
          />
        </el-scrollbar>
        <GitRebasePanel
          v-else-if="gitInspector === 'rebase'"
          :repo-path="gitInspectorRepoPath"
          @completed="refreshGitPanel"
        />
      </template>
    </el-drawer>
  </section>
</template>
