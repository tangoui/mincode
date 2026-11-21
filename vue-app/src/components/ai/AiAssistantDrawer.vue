<script setup lang="ts">
import { computed, defineAsyncComponent, nextTick, onBeforeUnmount, onMounted, reactive, ref, shallowReactive, watch } from "vue";
import "@/styles/workspaces/ai/drawer.css";
import { useRoute, useRouter } from "vue-router";
import { workspaceTabRoute } from "@/domain/workspace/tab-route";
import { ElMessage, ElMessageBox, type ScrollbarInstance } from "element-plus";
import {
  Bottom,
  ArrowLeft,
  Close,
  CopyDocument,
  Expand,
  Fold,
  FolderOpened,
  FullScreen,
  Grid,
  Lock,
  Minus,
  Plus,
  Setting,
  Share,
  TopRight,
} from "@element-plus/icons-vue";
import {
  AI_EVENT_DETAIL_LIMIT,
  useAiAssistantStore,
  type AiAssistantEvent,
  type AiAssistantMessage,
  type AiAssistantPermission,
  type AiConversation,
  type AiConversationStatus,
  type AiQueuedMessage,
} from "@/stores/ai-assistant";
import { useAiPendingEditsStore } from "@/stores/ai-pending-edits";
import { useSessionProgressStore } from "@/stores/session-progress";
import AiQueuedMessages from "@/components/ai/AiQueuedMessages.vue";
import AiPendingEdits from "@/components/ai/AiPendingEdits.vue";
import AiMessageThread from "@/components/ai/AiMessageThread.vue";
import MarkdownPreview from "@/components/ui/MarkdownPreview.vue";

type AiMessageThreadInstance = InstanceType<typeof AiMessageThread>;
import AiMessageNavigator from "@/components/ai/AiMessageNavigator.vue";
import AiPermissionCard from "@/components/ai/AiPermissionCard.vue";
import AiPromptComposer from "@/components/ai/AiPromptComposer.vue";
import AiConversationSidebarPosition from "@/components/ai/AiConversationSidebarPosition.vue";
import AiConversationSidebar from "@/components/ai/AiConversationSidebar.vue";
import AppSplitPane from "@/components/ui/AppSplitPane.vue";
import { useDatabaseWorkspaceStore } from "@/stores/database-workspace";
import { useSshConnectionsStore } from "@/stores/ssh-connections";
import { useSftpSessionsStore } from "@/stores/sftp-sessions";
import { useTerminalWorkspaceStore } from "@/stores/terminal-workspace";
import { useWorkbenchStore } from "@/stores/workbench";
import { useLinkedNotesDrawer } from "@/stores/linked-notes-drawer";
import { usePluginMcpServersStore } from "@/stores/plugin-mcp-servers";
import { useAiConversationPreferencesStore } from "@/stores/ai-conversation-preferences";
import {
  copyText,
  emitAppEvent,
  chooseNativeDirectory,
  invokeBackend,
  isBrowserLocalMode,
  isDesktopRuntime,
  listenAppEvent,
  nativeErrorMessage,
} from "@/services/desktop-bridge";
import {
  claimPluginAiTurn,
  refreshPendingPluginAiTurns,
  subscribePluginAiTurnApprovals,
  subscribePluginAiTurnCancellations,
  subscribePluginAiTurns,
  takePendingPluginAiTurns,
  updatePluginAiTurn,
} from "@/services/plugin-ai-turns";
import {
  approvalModeForConversation,
  externalPluginDenial,
  externalTurnSystemPrompt,
  filterAllowedPluginList,
  externalTurnToolNames,
  externalTurnUserMessage,
  matchesExternalConversation,
  pluginTurnHistory,
  pluginTurnStatus,
  redactExternalReply,
  riskCapDenial,
  type PluginAiTurnHistory,
  type PluginAiTurnRequest,
} from "@/domain/ai/plugin-turns";
import { selectAiTurnHistory } from "@/domain/ai/turn-history";
import { listenResilientJSONStream } from "@/services/resilient-json-stream";
import { isStreamAvailable } from "@/services/desktop-stream";
import {
  listenAgentRunCancelRequested,
  startAgentRun,
  updateAgentRun,
} from "@/services/session-progress-service";
import type { AgentRunPhase, AgentRunSource } from "@/domain/ai/session-progress";
import { applicableAiRules, type AiRuleDefinition } from "@/domain/ai/rules";
import { acpBuiltinAgentLaunch } from "@/domain/ai/acp-agents";
import { resolveLocalAgentDirectory } from "@/domain/ai/local-agent-directory";
import { useAcpAgentStart } from "@/composables/use-acp-agent-start";
import { applyVisibleWorkspaceGroupOrder } from "@/domain/ai/workspace-group-order";
import { connectSftp, disconnectSftp } from "@/services/sftp-service";
import {
  closePopupWindow,
  isPopupWindow,
  popOutAiAssistant,
  popupWindowLabel,
  POPUP_TAB_STATE_EVENT,
} from "@/services/popup-tab";
import {
  AI_ATTACHMENT_ACCEPT,
  getAttachmentImageSrc,
  readAiAttachments,
  readAiAttachmentsFromClipboard,
  sanitizeAiAttachments,
  toAcpPromptContent,
  type AiAttachment,
} from "@/domain/ai/attachments";
import {
  canStreamAi,
  continueAiLoopHistory,
  createConversationSendGate,
  runAiLoop,
  runNextConversationQueueItem,
  REDIRECTED_TOOL_RESULT,
  type AiLoopConfig,
  type AiLoopToolCall,
  type AiLoopToolDefinition,
  type AiLoopToolExecution,
  type AiLoopMessage,
} from "@/services/ai-agent-loop";
import { useVueI18n } from "@/i18n/messages";
import {
  normalizeMcpServerForRuntime,
  type StoredMcpServer,
} from "@/domain/ai/settings-integrations";
import { buildAcpMcpServers, type AcpMcpServer } from "@/domain/ai/acp-mcp-servers";
import {
  expandAiPromptTemplate,
  pluginAiPromptDescription,
  pluginAiPromptTitle,
} from "@/domain/ai/plugin-prompts";
import { currentAiPromptVariables } from "@/services/ai-prompt-variables";
import { useAiPluginPromptsStore } from "@/stores/ai-plugin-prompts";
import { useAiContextProvidersStore } from "@/stores/ai-context-providers";
import { normalizeAiApiFormat } from "@/domain/ai/api-format";
import { normalizeAnthropicAuth } from "@/domain/ai/anthropic-auth";
import {
  parseAcpConfigSelectOptions,
  type AcpConfigSelectOption,
} from "@/domain/ai/session-config-option";
import { normalizeAiMaxLoops } from "@/domain/ai/agent-loop-limit";
import { isAiStreamIdleTimeoutError } from "@/domain/ai/stream-idle-watchdog";
import { mergeAiTokenUsage, withAiTokenRequestStats, type AiTokenUsage } from "@/domain/ai/token-usage";
import {
  AI_APPROVAL_MODES,
  automaticProtocolPermissionOption,
  normalizeAiApprovalMode,
  planModeDenies,
  requiresAiToolApproval,
  type AiApprovalMode,
} from "@/domain/ai/approval-policy";
import {
  PLAN_MODE_ENTER,
  PLAN_MODE_LEAVE,
  createPlanModeReminders,
} from "@/domain/ai/plan-mode-reminders";
import { aiProviderAuthMessageKey, validateAiProviderAuth } from "@/domain/ai/provider-auth";
import {
  buildAiPromptSuggestions,
  parseAiPromptSuggestionQuery,
  type AiPromptSuggestion,
  type AiPromptSuggestionPrompt,
} from "@/domain/ai/tool-command";
import {
  AI_REASONING_EFFORT_CHOICES,
  buildAiModelAttemptOrder,
  buildSubagentModelAttemptOrder,
  configuredAiModels,
  isAiReasoningEffortChoice,
  normalizeAiReasoningEffort,
  resolveAiReasoningEffort,
} from "@/domain/ai/model-selection";
import { streamAiTurnWithFailover } from "@/services/ai-model-failover";
import { optimizeAiPrompt } from "@/services/ai-prompt-optimizer";
import { notifyApp } from "@/services/notifications";
import {
  aiAgentTaskNotification,
  aiErrorNotification,
} from "@/domain/ai/task-notification";
import {
  hydrateSettingsSnapshot,
  persistSettingsSnapshot,
  readSettingsSnapshot,
  settingsSnapshotVersion,
} from "@/services/settings-persistence";
import { loadSidebarVisibility, saveSidebarVisibility, sidebarItemIds } from "@/services/sidebar-preferences";
import { applyUiTheme, normalizeUiTheme } from "@/services/ui-theme";
import { getUserHomeDirectory, setAutostart, setMinimizeToTray } from "@/services/settings-service";
import { aiCapabilityRegistry, type AiCapabilityExecutionResult } from "@/domain/ai/capability-registry";
import { runWithAbort, throwIfAborted } from "@/domain/async/abort";
import { callMcpToolCancellable } from "@/services/mcp-tool-call";
import { startPluginMcpServer } from "@/services/plugin-mcp-server-service";
import { localizedMcpServerText, mcpServerAvailable, mcpServerLaunchRoot } from "@/domain/plugins/mcp-servers";
import { precedingUserMessage } from "@/domain/ai/conversation-rewind";
import { adjacentConversationId } from "@/domain/ai/drawer-shortcuts";
import { registerKeybindingContext } from "@/domain/keybindings/context";
import { registerCommand } from "@/services/command-registry";
import { AI_ASSISTANT_DROP_TARGET_ID, readAiAttachmentsFromPaths } from "@/domain/ai/dropped-files";
import { applyAiFileMention, parseAiFileMention } from "@/domain/ai/file-mention";
import { MAX_AI_CONTEXT_CHARS } from "@/domain/ai/context-budget";
import {
  aiContextProviderDescription,
  isPluginContextAttachment,
  matchContextMentions,
  pluginContextAttachmentName,
  pluginContextBudget,
  type PluginAiContextProvider,
} from "@/domain/ai/plugin-context";
import { conversationExportFileName, conversationToMarkdown } from "@/domain/ai/conversation-export";
import { searchAiConversationMessages, type AiMessageSearchMatch } from "@/services/ai-conversation-persistence";
import { saveTextFileToDisk } from "@/services/file-download";
import { formatAiMessageFullTime } from "@/domain/ai/message-time";
import { formatDataBytes } from "@/domain/settings/data-usage";
import { aiRunStatusText } from "@/domain/ai/run-status";
import { createAcpTextSegments } from "@/domain/ai/acp-text-segments";
import { useStickToBottom } from "@/composables/use-stick-to-bottom";
import { createFrameTextBatcher } from "@/domain/async/frame-text-batcher";
import { aiOperationManager } from "@/domain/ai/operation-manager";
import {
  normalizeSubagentPlan,
  type SubagentPlanDefinition,
  type SubagentPlanSnapshot,
  type SubagentTaskState,
} from "@/domain/ai/subagent-orchestration";
import { createConversationTree } from "@/domain/ai/conversation-tree";
import { executeSubagentPlan as executeSubagentRuntime } from "@/services/ai-subagent-runtime";
import { aiResourceRegistry, createResourceRegistry, type AiResourceRegistry } from "@/domain/ai/resource-registry";
import { createAiTerminalSessionReuseRegistry } from "@/domain/ai/terminal-session-reuse";
import {
  registerNativeAiCapabilities,
  syncNativeAiResources,
} from "@/services/ai-native-tools";
import {
  isTerminalLeafPane,
  type LocalTerminalPaneOptions,
  type TerminalPaneNode,
} from "@/domain/terminal/types";
import { aiCloseTargetForPane, isBackgroundTerminalPane, terminalTabForPane } from "@/domain/terminal/terminal-tabs";
import type { CreateTerminalTabInput } from "@/services/ai-tools/terminal-tools";
import type { WorkspaceView } from "@/types/workspace";
import {
  availableAiCapabilityModules,
  isDirectConversationIntent,
  selectAiCapabilityTools,
} from "@/domain/ai/tool-selection";
import {
  MCP_LOAD_TOOL_NAME,
  mcpLoadSummary,
  mcpLoadToolDefinition,
  mcpServersFromHistory,
  parseMcpLoadRequest,
  pickMcpTools,
  planMcpToolLoading,
} from "@/domain/ai/mcp-tool-loading";
import {
  createToolAuthorization,
  createToolAuthorizationStore,
  sessionGrantBindsParameters,
  type ToolAuthorizationRequest,
} from "@/domain/ai/tool-authorization";
import {
  createToolApprovalQueue,
  type ToolApprovalDecision,
  type ToolApprovalQueueItem,
} from "@/domain/ai/tool-approval-queue";
import { declinedToolResult } from "@/domain/ai/approval-feedback";
import {
  createMcpAuthorizationRequest,
  createNativeAuthorizationRequest,
} from "@/domain/ai/tool-approval";
import {
  AI_CONTEXT_COMPACTION_THRESHOLD,
  AI_CONTEXT_SUMMARY_MARKER,
  compactAiLoopContext,
  estimateAiContextUsage,
  estimateAiToolTokens,
} from "@/domain/ai/context-compaction";
import { compactTokenCount } from "@/domain/ai/token-usage";
import { summarizeToolApprovalArguments } from "@/domain/ai/tool-approval-summary";
import { safeAiText, sanitizeAiResult } from "@/services/ai-tools/shared";
import { requestAiWorkspaceAction } from "@/services/ai-workspace-actions";
import {
  globalToolAuthorizations,
  loadGlobalToolAuthorizations,
  saveGlobalToolAuthorizations,
} from "@/services/tool-authorization-persistence";
import {
  aiApprovalMode,
  hydrateAiApprovalMode,
  saveAiApprovalMode,
} from "@/services/ai-approval-policy";

// Browser and desktop capture tools are not part of this edition; no tool returns an image yet.
const VISUAL_CAPTURE_TOOL_NAMES = new Set<string>();

interface AcpSessionUpdatePayload {
  serverId: string;
  sessionId: string;
  /** True when the Agent is replaying the transcript as part of ACP LoadSession. */
  replay?: boolean;
  update: unknown;
}
interface AcpNewSession {
  sessionId: string;
  configOptions?: unknown;
}
interface AcpConfigOptionsResult {
  configOptions?: unknown;
}
interface StoredAiRule extends AiRuleDefinition {
  id?: string;
  title?: string;
  content?: string;
  enabled?: boolean;
}
interface StoredAiProfile {
  id?: string;
  name?: string;
  provider?: string;
  baseUrl?: string;
  apiKey?: string;
  model?: string;
  commonModels?: string[];
  failoverEnabled?: boolean;
  failoverModels?: string[];
  subagentModel?: string;
  apiFormat?: string;
  anthropicAuth?: string;
  reasoningEffort?: string;
  contextWindow?: number;
  maxInputTokens?: number;
  maxOutputTokens?: number;
  openaiStreamUsage?: "auto" | "always" | "never";
  noToolModels?: string[];
}
interface StoredSettings {
  aiMaxLoops?: number;
  activeAiProfileId?: string;
  aiProfiles?: StoredAiProfile[];
  mcpServers?: StoredMcpServer[];
  aiRules?: StoredAiRule[];
  acpAdapterId?: string;
  acpInjectRules?: boolean;
  acpAgents?: Array<{ id: string; name: string; command: string; args: string }>;
}
interface McpToolDefinition {
  name: string;
  description?: string;
  inputSchema?: unknown;
}
interface McpConnectResult {
  tools: McpToolDefinition[];
}
interface McpServerStatus {
  id: string;
  state: string;
}
interface McpCallResult {
  content: Array<{ type: string; text?: string }>;
  isError?: boolean;
}
interface ActiveMcpTool extends AiLoopToolDefinition {
  serverId: string;
  serverName: string;
  toolName: string;
}
const assistant = useAiAssistantStore();
const delegatedChildren = computed(() => assistant.allConversations.filter(
  (conversation) => conversation.delegation?.parentId === assistant.activeConversationId,
).reverse().sort((left, right) => left.createdAt - right.createdAt));
const settledDelegations = computed(() => delegatedChildren.value.filter(
  (conversation) => conversation.delegation?.result !== undefined || conversation.status === "interrupted",
).length);
const openDelegationReportId = ref("");
function toggleDelegationReport(id: string) {
  openDelegationReportId.value = openDelegationReportId.value === id ? "" : id;
}
const delegationParent = computed(() => {
  const id = assistant.activeConversation?.delegation?.parentId;
  return id ? assistant.conversationById(id) : undefined;
});
const executingParallelChildren = reactive(new Set<string>());
const delegatedTaskAborts = new Map<string, AbortController>();
function parallelDelegationPending(conversation: AiConversation | undefined) {
  return conversation?.delegation?.source === "parallel"
    && conversation.delegation.result === undefined
    && (conversation.running || executingParallelChildren.has(conversation.id));
}
const activeParallelDelegationPending = computed(() => parallelDelegationPending(assistant.activeConversation));
function delegationStatusTag(status: AiConversationStatus) {
  if (status === "completed") return "success";
  if (status === "failed") return "danger";
  if (status === "interrupted" || status === "needs_input") return "warning";
  return status === "in_progress" ? "primary" : "info";
}
const progress = useSessionProgressStore();
void progress.ensureListening();
const pendingEdits = useAiPendingEditsStore();
const databaseWorkspace = useDatabaseWorkspaceStore();
const sshConnections = useSshConnectionsStore();
const sftpSessions = useSftpSessionsStore();
const terminalWorkspace = useTerminalWorkspaceStore();
const workbench = useWorkbenchStore();
const pluginMcpServers = usePluginMcpServersStore();
const conversationPreferences = useAiConversationPreferencesStore();
void conversationPreferences.hydrate();
const pluginPrompts = useAiPluginPromptsStore();
const aiContextProviders = useAiContextProvidersStore();
const AiPluginContextDialog = defineAsyncComponent(() => import("@/components/ai/AiPluginContextDialog.vue"));
void pluginPrompts.hydrate();
const router = useRouter();
const route = useRoute();
// 顶部标签页呈现：路由停在 ai 标签时以全幅覆盖层展示同一宿主实例，
// 与抽屉外壳互斥；关闭标签页只离开路由，不拆除运行时监听。
const tabSurfaceActive = computed(() => !isPopupWindow() && route.name === "ai");
const conversationListOnLeft = computed(() => tabSurfaceActive.value && conversationPreferences.sidebarPosition === "left");
const shellFullscreen = computed(() => assistant.fullscreen || tabSurfaceActive.value);
const emit = defineEmits<{ "open-settings": [] }>();
const { t, setLocale, localeChain, intlLocale } = useVueI18n();
const visibleAgentEvents = computed(() =>
  assistant.agentEvents.filter((event) => event.kind !== "thought" || Boolean(event.detail.trim())),
);

const eventStatusLabels = computed(() => ({
  awaiting_approval: t("ai.taskProgress.waitingApproval"),
  running: t("ai.status.running"),
  pending: t("ai.taskStep.status.pending"),
  in_progress: t("ai.taskStep.status.inProgress"),
  completed: t("ai.taskStep.status.completed"),
  mixed: t("ai.status.mixed"),
  failed: t("ai.taskStep.status.failed"),
  error: t("ai.status.error"),
  cancelled: t("ai.taskStep.status.cancelled"),
  thought: t("ai.thought"),
  tool: t("ai.toolCall"),
  plan: t("ai.plan"),
  mode: t("ai.mode"),
  unknown: t("ai.status.unknown"),
}));
const taskStepStatusLabels = computed(() => ({
  pending: t("ai.taskStep.status.pending"),
  in_progress: t("ai.taskStep.status.inProgress"),
  completed: t("ai.taskStep.status.completed"),
  failed: t("ai.taskStep.status.failed"),
  cancelled: t("ai.taskStep.status.cancelled"),
}));
const activityLabels = computed(() => ({
  analysis: t("ai.activity.analysis"),
  plan: t("ai.activity.plan"),
  modeChange: t("ai.activity.modeChange"),
  readFile: t("ai.activity.readFile"),
  openFile: t("ai.activity.openFile"),
  searchCode: t("ai.activity.searchCode"),
  searchFiles: t("ai.activity.searchFiles"),
  runTest: t("ai.activity.runTest"),
  codeCheck: t("ai.activity.codeCheck"),
  buildProject: t("ai.activity.buildProject"),
  gitOperation: t("ai.activity.gitOperation"),
  runCommand: t("ai.activity.runCommand"),
  writeFile: t("ai.activity.writeFile"),
  createFile: t("ai.activity.createFile"),
  deleteFile: t("ai.activity.deleteFile"),
  browserAction: t("ai.activity.browserAction"),
  databaseAction: t("ai.activity.databaseAction"),
  httpAction: t("ai.activity.httpAction"),
  remoteAction: t("ai.activity.remoteAction"),
  connectorCall: t("ai.activity.connectorCall"),
  systemAction: t("ai.activity.systemAction"),
  genericTool: t("ai.activity.genericTool"),
  detail: t("ai.activity.detail"),
  hideDetail: t("ai.activity.hideDetail"),
  toolName: t("ai.activity.toolName"),
  result: t("ai.activity.result"),
  runningDuration: t("ai.activity.runningDuration"),
  totalDuration: t("ai.activity.totalDuration"),
  duration: t("ai.activity.duration"),
}));
const input = computed({
  get: () => assistant.draft,
  set: (value: string) => assistant.setDraft(value),
});
const promptOptimizationVisible = ref(false);
const promptOptimizationOriginal = ref("");
const promptOptimizationResult = ref("");
const promptOptimizationLoading = ref(false);
let promptOptimizationController: AbortController | undefined;

const promptHistory = computed(() =>
  assistant.messages
    .filter((message) => message.role === "user" && message.content.trim())
    .map((message) => message.content)
    .reverse(),
);
interface AiMessageNavigatorItem {
  id: string;
  title: string;
  preview: string;
  ariaLabel: string;
}

// Previews show at most 180 characters; bounding the source keeps the regexes
// cheap while a long answer streams (the navigator recomputes every frame).
const MESSAGE_PREVIEW_SOURCE_LIMIT = 4_096;

function compactMessagePreview(content = "", limit = 180) {
  const normalized = content
    .slice(0, MESSAGE_PREVIEW_SOURCE_LIMIT)
    .replace(/```[\s\S]*?```/g, " ")
    .replace(/```[\s\S]*$/, " ")
    .replace(/`([^`]+)`/g, "$1")
    .replace(/!\[[^\]]*]\([^)]*\)/g, " ")
    .replace(/\[([^\]]+)]\([^)]*\)/g, "$1")
    .replace(/^[#>*+-]+\s*/gm, "")
    .replace(/\s+/g, " ")
    .trim();
  return normalized.length > limit ? `${normalized.slice(0, limit).trimEnd()}…` : normalized;
}

function navigatorTitle(message: AiAssistantMessage) {
  const content = compactMessagePreview(message.content, 72);
  if (content) return content;
  const names = message.attachments?.map((attachment) => attachment.name).filter(Boolean) ?? [];
  if (names.length) return compactMessagePreview(names.join(t("ai.messageNavigator.nameSeparator")), 72);
  return t("ai.messageNavigator.attachmentOnly", { count: message.attachments?.length ?? 0 });
}

const messageNavigatorItems = computed<AiMessageNavigatorItem[]>(() => {
  const items: AiMessageNavigatorItem[] = [];
  for (let index = 0; index < assistant.messages.length; index += 1) {
    const message = assistant.messages[index];
    if (!message?.id || message.role !== "user") continue;

    let preview = "";
    for (let responseIndex = index + 1; responseIndex < assistant.messages.length; responseIndex += 1) {
      const response = assistant.messages[responseIndex];
      if (!response || response.role === "user") break;
      if (response.role === "assistant" && /\S/.test(response.content)) {
        preview = compactMessagePreview(response.content);
        break;
      }
    }

    const title = navigatorTitle(message);
    const number = items.length + 1;
    items.push({
      id: message.id,
      title,
      preview,
      ariaLabel: t("ai.messageNavigator.item", { number, title }),
    });
  }
  return items;
});

// Depends only on user message ids, so streaming text does not re-run position syncs.
const messageNavigatorIds = computed(() =>
  assistant.messages
    .filter((message) => message?.id && message.role === "user")
    .map((message) => message.id)
    .join("|"),
);

const messageThread = ref<AiMessageThreadInstance>();
const messageScrollbar = ref<ScrollbarInstance>();
const scrollHost = computed(() => messageScrollbar.value?.wrapRef);
const activeMessageNavigatorId = ref("");
const messagesAtBottom = ref(true);
const showScrollToBottom = computed(() => assistant.messages.length > 0 && !messagesAtBottom.value);
const MESSAGE_BOTTOM_THRESHOLD = 32;
let messageNavigatorFrame = 0;
const attachments = computed({
  get: () => assistant.draftAttachments,
  set: (value: AiAttachment[]) => assistant.setDraftAttachments(value),
});
const addingAttachments = ref(false);
const activeToolIndex = ref(0);
const previewAttachment = ref<AiAttachment>();
const conversationSidebarOpen = ref(true);
const AI_CONVERSATION_SIDEBAR_MIN_WIDTH = 210;
const AI_CONVERSATION_SIDEBAR_MAX_WIDTH = 720;
const AI_CONVERSATION_CONTENT_MIN_WIDTH = 120;
const conversationScope = ref<"active" | "archived">("active");
const conversationStatus = ref<"all" | AiConversationStatus>("all");
const conversationSort = ref<"updated" | "created">("updated");
const conversationWorkspaceFilter = ref("all");
const conversationSearch = ref("");

// Message text lives in storage, not in memory, so content search runs in the
// backend; only the latest query's results are applied.
const MESSAGE_SEARCH_DEBOUNCE_MS = 250;
const MESSAGE_SEARCH_MIN_LENGTH = 2;
const messageMatches = ref<AiMessageSearchMatch[]>([]);
let messageSearchTimer: ReturnType<typeof setTimeout> | undefined;
let messageSearchSequence = 0;
watch([conversationSearch, () => conversationPreferences.globalMode, () => assistant.activeWorkspaceScopeId], ([query]) => {
  if (messageSearchTimer) clearTimeout(messageSearchTimer);
  const sequence = ++messageSearchSequence;
  const trimmed = query.trim();
  if (trimmed.length < MESSAGE_SEARCH_MIN_LENGTH) {
    messageMatches.value = [];
    return;
  }
  messageSearchTimer = setTimeout(async () => {
    const scopeIds = conversationPreferences.globalMode ? [] : [assistant.activeWorkspaceScopeId];
    const matches = await searchAiConversationMessages(trimmed, scopeIds).catch(() => []);
    if (sequence === messageSearchSequence) messageMatches.value = matches;
  }, MESSAGE_SEARCH_DEBOUNCE_MS);
});
onBeforeUnmount(() => {
  if (messageSearchTimer) clearTimeout(messageSearchTimer);
});
let stopSessionUpdate: (() => void) | undefined;
let stopPermissionRequest: (() => void) | undefined;
let stopNativeFileDrop: (() => void) | undefined;
let stopRunCancelRequested: (() => void) | undefined;
let stopPluginAiTurns: (() => void) | undefined;
let stopPluginAiTurnCancellations: (() => void) | undefined;
let stopPluginAiTurnApprovals: (() => void) | undefined;

interface PluginTurnRun {
  turnId: string;
  conversationId: string;
  queuedMessageId: string;
  instructions: string;
  allowedPlugins: string[] | null;
  maxRisk: string;
  chatApproval: boolean;
  /** "none": this turn is sent without the conversation's earlier messages. */
  history: PluginAiTurnHistory | "";
  /** The approval waiting in the queue, exposed to the plugin when chatApproval is on. */
  approval?: { id: string; summary: string; risk: string; tool: string };
  /** The user message the reply follows; set once the run starts. */
  messageId?: string;
  approvalSummary?: string;
  cancelled: boolean;
  finished: boolean;
  lastText: string;
  lastStatus: string;
  lastActivity: string;
  lastApprovalId?: string;
  timer?: ReturnType<typeof setInterval>;
}
// Messages relayed by plugins (team-chat bots). Keyed by turn id; the active
// run of a conversation is found through pluginTurnByConversation.
const pluginTurnRuns = new Map<string, PluginTurnRun>();
const pluginTurnByConversation = new Map<string, string>();
let processingPluginTurns = false;
const agenticAborts = new Map<string, AbortController>();
const subagentParentAborts = new Map<string, AbortController>();
const subagentUsageCollectors = new Map<string, {
  onUsage(usage: AiTokenUsage): void;
  onModelAttempt(model: string): void;
}>();
// One main-loop plan reminder per conversation, so a plan -> other mode switch
// between turns still sends the leave notice on the next turn.
const planModeReminders = new Map<string, () => string | undefined>();
const activeRunIds = new Map<string, string>();
const activeRunPhases = new Map<string, AgentRunPhase>();
const liveRunStatus = ref<Record<string, { phase: AgentRunPhase; summary: string }>>({});
const activeRunSummaries = new Map<string, string>();
const failedRunUpdateKeys = new Map<string, string>();
watch(
  () => progress.activeRuns,
  (runs) => {
    const runsByConversationId = new Map<string, (typeof runs)[number]>();
    for (const run of runs) {
      if (run.parentRunId || run.navigation.type !== "ai-conversation") continue;
      if (!runsByConversationId.has(run.navigation.conversationId)) {
        runsByConversationId.set(run.navigation.conversationId, run);
      }
    }
    for (const conversationId of activeRunIds.keys()) {
      if (!runsByConversationId.has(conversationId)) releaseConversationRun(conversationId);
    }
    for (const [conversationId, run] of runsByConversationId) {
      activeRunIds.set(conversationId, run.runId);
      activeRunPhases.set(conversationId, run.progress.phase);
      activeRunSummaries.set(conversationId, run.progress.summary);
    }
  },
  { immediate: true, flush: "sync" },
);
interface ConversationWorkspaceContext {
  workspaceId: string;
  workspaceScopeId: string;
  projectRoot: string;
  capabilityModule?: string;
  resources: AiResourceRegistry;
}
const conversationWorkspaceContexts = new Map<string, ConversationWorkspaceContext>();
interface AcpRunTarget {
  serverId: string;
  sessionId: string;
  context: ConversationWorkspaceContext;
  directoryKind: "project" | "default" | "temporary";
}
interface AcpStartingServer extends Pick<AcpRunTarget, "serverId" | "directoryKind"> {
  needsConnectionCheck: boolean;
}
const acpRunTargets = shallowReactive(new Map<string, AcpRunTarget>());
const unavailableAcpDirectories = shallowReactive(new Map<string, string>());
const acpStartingServers = new Map<string, AcpStartingServer>();
const activeAcpRunTarget = computed(() => acpRunTargets.get(assistant.activeConversationId));
const activeDefaultAcpDirectory = computed(() => {
  const target = activeAcpRunTarget.value;
  return target && target.directoryKind !== "project" ? target.context.projectRoot : undefined;
});
const activeAcpPrompts = new Map<string, { serverId: string; sessionId: string }>();
const acpPromptAborts = new Map<string, AbortController>();
// Sessions the user stopped; their late streaming updates are dropped until the next prompt starts.
const stoppedAcpSessions = new Set<string>();
function acpSessionKey(target: { serverId: string; sessionId: string }) {
  return `${target.serverId}\u0000${target.sessionId}`;
}
function markAcpSessionStopped(target: { serverId: string; sessionId: string } | undefined) {
  if (target?.sessionId) stoppedAcpSessions.add(acpSessionKey(target));
}
const acpConfigOptions = ref<Record<string, AcpConfigSelectOption[]>>({});
const acpConfigPending = ref("");
const agenticSendGate = createConversationSendGate();
const acpSendGate = createConversationSendGate();
const queueInterruptions = new Map<string, Promise<void>>();
const resumeQueueAfterCancellation = new Set<string>();
const QUEUE_INTERRUPT_GRACE_MS = 2_000;
const agentStartPromises = new Map<string, Promise<AcpRunTarget | undefined>>();
const acpWorkspaceRelinks = new Map<string, Promise<void>>();
let acpDisposed = false;
function acpSessionTarget(conversation: AiConversation) {
  const target = acpRunTargets.get(conversation.id);
  if (target) return { serverId: target.serverId, sessionId: target.sessionId };
  if (conversation.acpSessionId) {
    return { serverId: conversation.acpServerId, sessionId: conversation.acpSessionId };
  }
}
function findConversationByAcpSession(serverId: string, sessionId: string) {
  for (const [id, target] of acpRunTargets) {
    if (target.serverId === serverId && target.sessionId === sessionId) return assistant.conversationById(id);
  }
  const conversation = assistant.findConversationByAcpSession(serverId, sessionId);
  if (conversation && !acpRunTargets.has(conversation.id)) return conversation;
}
const acpContextInjected = new Set<string>();
const acpCurrentMessageIds = new Map<string, string>();
const acpTextSegments = createAcpTextSegments(() => createId("agent-message"));
const acpEventMessageIds = new Map<string, Map<string, string>>();
const ACP_STREAM_KEY_SEPARATOR = "\u0000";
const acpStreamTextBatcher = createFrameTextBatcher((key, text) => {
  const [conversationId, kind, messageId] = key.split(ACP_STREAM_KEY_SEPARATOR);
  const conversation = assistant.conversationById(conversationId);
  if (!conversation || !messageId) return;

  if (kind === "message") {
    assistant.settleRunningAgentEvents(conversationId, "completed", "thought");
    assistant.appendAssistantMessageForConversation(conversationId, messageId, text);
    void scrollToBottomIfFollowing(conversationId);
    return;
  }

  const hasOtherRunningThought = conversation.agentEvents.some(
    (item) => item.kind === "thought" && item.id !== messageId && item.status === "running",
  );
  if (hasOtherRunningThought) {
    assistant.settleRunningAgentEvents(conversationId, "completed", "thought");
  }
  const triggerMessageId = acpEventAnchor(conversationId, messageId);
  const previous = conversation.agentEvents.find(
    (item) => item.id === messageId && item.triggerMessageId === triggerMessageId,
  )?.detail ?? "";
  if (!text.trim() && !previous.trim()) return;
  assistant.upsertAgentEventForConversation(conversationId, {
    id: messageId,
    kind: "thought",
    title: t("ai.thought"),
    detail: previous + text,
    status: "running",
    triggerMessageId,
  });
  void scrollToBottomIfFollowing(conversationId);
});
const toolAuthorizations = createToolAuthorizationStore();
const toolAuthorizationHydration = loadGlobalToolAuthorizations().then((authorizations) => {
  toolAuthorizations.replaceGlobals(authorizations);
});
const stopGlobalToolAuthorizationUpdates = watch(globalToolAuthorizations, (authorizations) => {
  toolAuthorizations.replaceGlobals(authorizations);
});
const approvalModeHydration = hydrateAiApprovalMode();
const approvalModeOptions = computed<Array<{ value: AiApprovalMode; label: string; description: string }>>(() => [
  {
    value: "ask",
    label: t("ai.approvalMode.ask"),
    description: t("ai.approvalMode.askHint"),
  },
  {
    value: "agent",
    label: t("ai.approvalMode.agent"),
    description: t("ai.approvalMode.agentHint"),
  },
  {
    value: "full-access",
    label: t("ai.approvalMode.fullAccess"),
    description: t("ai.approvalMode.fullAccessHint"),
  },
  {
    value: "plan",
    label: t("ai.approvalMode.plan"),
    description: t("ai.approvalMode.planHint"),
  },
]);
const currentApprovalModeLabel = computed(() =>
  approvalModeOptions.value.find((option) => option.value === aiApprovalMode.value)?.label
    ?? t("ai.approvalMode.ask")
);
const toolApprovalQueue = createToolApprovalQueue();
const toolApprovals = ref<ToolApprovalQueueItem[]>([]);

// Screen readers hear run state changes from one status region; the streamed text
// itself is not live, or every chunk would be read aloud.
const drawerPanel = ref<HTMLElement>();
const liveAnnouncement = ref("");
function announce(message: string) {
  liveAnnouncement.value = "";
  void nextTick(() => {
    liveAnnouncement.value = message;
  });
}
watch(
  () => [assistant.activeConversationId, assistant.activeConversation?.running ?? false] as const,
  ([conversationId, running], previous) => {
    if (!previous || previous[0] !== conversationId || previous[1] === running) return;
    if (running) {
      announce(t("ai.live.replying"));
      return;
    }
    const status = assistant.activeConversation?.status;
    announce(
      status === "failed"
        ? t("ai.live.failed")
        : status === "interrupted"
          ? t("ai.live.stopped")
          : t("ai.live.completed"),
    );
  },
);
// An approval card takes focus only when focus is already in the AI panel, and
// focus returns to the composer once the last approval is answered.
const activeApprovalCount = computed(() =>
  toolApprovals.value.filter((item) => item.conversationId === assistant.activeConversationId).length,
);
watch(activeApprovalCount, async (count, previous) => {
  const panel = drawerPanel.value;
  const focusInPanel = Boolean(panel && document.activeElement && panel.contains(document.activeElement));
  if (count > previous) {
    announce(t("ai.live.approvalNeeded"));
    await nextTick();
    if (focusInPanel) panel?.querySelector<HTMLElement>(".ai-inline-tool-approval-card button")?.focus();
  } else if (count === 0 && previous > 0 && focusInPanel) {
    await nextTick();
    panel?.querySelector<HTMLTextAreaElement>(".ai-assistant-composer textarea")?.focus();
  }
});
const activeToolApprovals = computed(() =>
  toolApprovals.value.filter((approval) => approval.conversationId === assistant.activeConversationId),
);
// What the running conversation is doing now, shown at the end of the thread for the whole run.
const runStatusText = computed(() => {
  const conversationId = assistant.activeConversationId;
  const runningTool = assistant.mode === "agent"
    ? [...visibleAgentEvents.value].reverse().find((event) => event.kind === "tool" && event.status !== undefined && ["running", "pending", "in_progress"].includes(event.status))
    : undefined;
  return aiRunStatusText({
    waitingApproval: activeToolApprovals.value.length > 0 || Boolean(assistant.pendingPermission),
    live: liveRunStatus.value[conversationId],
    runningToolTitle: runningTool?.title,
  }, {
    processing: t("ai.taskProgress.processing"),
    waitingApproval: t("ai.taskProgress.waitingApproval"),
    runningTool: (tool) => t("ai.taskProgress.runningTool", { tool }),
  });
});
const stopToolApprovalUpdates = toolApprovalQueue.subscribe(() => {
  toolApprovals.value = toolApprovalQueue.list();
});
const editingQueueId = ref<string>();

const capabilityTools = ref(aiCapabilityRegistry.listTools());
/**
 * Browser-local mode is plain chat straight to the provider: no native tools, no MCP and no
 * approvals, so their entries stay hidden (the browser stream also never sends tools).
 */
const browserLocalAi = isBrowserLocalMode();
const agentTools = computed(() => (browserLocalAi ? [] : capabilityTools.value));
const stopCapabilityUpdates = aiCapabilityRegistry.subscribe(() => {
  capabilityTools.value = aiCapabilityRegistry.listTools();
});

// 运行中的会话仍允许编辑和排队下一条消息，斜杆选择器也必须保持可用。
const toolSuggestionsVisible = computed(
  () => parseAiPromptSuggestionQuery(input.value) !== null
);
const statusOptions: Array<{
  value: "all" | AiConversationStatus;
  label: Parameters<typeof t>[0];
}> = [
  { value: "all", label: "ai.conversation.status.all" },
  { value: "not_started", label: "ai.conversation.status.notStarted" },
  { value: "in_progress", label: "ai.conversation.status.inProgress" },
  { value: "needs_input", label: "ai.conversation.status.needsInput" },
  { value: "completed", label: "ai.conversation.status.completed" },
  { value: "failed", label: "ai.conversation.status.failed" },
  { value: "interrupted", label: "ai.conversation.status.interrupted" },
];
// AI 流式输出会频繁刷新 updatedAt。任务开始时记录一次排序时间，运行期间保持
// 会话卡片位置稳定；任务结束后再恢复使用最终更新时间。
const runningConversationSortTimes = reactive(new Map<string, number>());
watch(
  () =>
    assistant.allConversations.map((conversation) => ({
      id: conversation.id,
      running: conversation.running,
      updatedAt: conversation.updatedAt,
    })),
  (conversations, previousConversations = []) => {
    const previousById = new Map(
      previousConversations.map((conversation) => [conversation.id, conversation]),
    );
    const knownIds = new Set(conversations.map((conversation) => conversation.id));
    for (const id of runningConversationSortTimes.keys()) {
      if (!knownIds.has(id)) runningConversationSortTimes.delete(id);
    }
    for (const conversation of conversations) {
      if (conversation.running) {
        if (!runningConversationSortTimes.has(conversation.id)) {
          runningConversationSortTimes.set(
            conversation.id,
            previousById.get(conversation.id)?.updatedAt ?? conversation.updatedAt,
          );
        }
      } else {
        runningConversationSortTimes.delete(conversation.id);
      }
    }
  },
  { immediate: true },
);
function conversationSortTime(conversation: AiConversation) {
  if (conversationSort.value === "created") return conversation.createdAt;
  return runningConversationSortTimes.get(conversation.id) ?? conversation.updatedAt;
}
// 会话搜索与全局模式分组依赖该映射；下方 immediate watcher 会在 setup 早期同步求值，
// 声明必须保持在所有使用点之前，避免 TDZ 崩溃。
const conversationWorkspaceNames = computed(() => Object.fromEntries([
  ...workbench.workspaces.map((workspace) => [workspace.id, workspace.name] as const),
  ...workbench.deletedWorkspaces.map((entry) => [entry.workspace.id, entry.workspace.name] as const),
]));
const visibleConversations = computed(() => {
  const archived = conversationScope.value === "archived";
  const source = conversationPreferences.globalMode
    ? assistant.allConversations
    : assistant.conversations;
  return source
    .filter((conversation) => conversation.archived === archived)
    .filter((conversation) => conversationPreferences.globalMode
      ? conversationWorkspaceFilter.value === "all"
        || conversation.workspaceId === conversationWorkspaceFilter.value
      : conversation.workspaceId === assistant.topWorkspaceId,
    )
    .filter((conversation) => {
      const query = conversationSearch.value.trim().toLocaleLowerCase();
      if (!query) return true;
      return [
        conversation.title,
        conversationWorkspaceName(conversation),
        conversation.projectRoot,
      ].some((value) => value?.toLocaleLowerCase().includes(query));
    })
    .filter(
      (conversation) =>
        conversationStatus.value === "all" || conversation.status === conversationStatus.value,
    )
    .slice()
    .sort((left, right) => conversationSortTime(right) - conversationSortTime(left));
});
const WORKSPACE_GROUP_TONE_COUNT = 8;
function workspaceGroupTone(identity: string) {
  let hash = 2_166_136_261;
  for (let index = 0; index < identity.length; index += 1) {
    hash ^= identity.charCodeAt(index);
    hash = Math.imul(hash, 16_777_619);
  }
  return (hash >>> 0) % WORKSPACE_GROUP_TONE_COUNT;
}
function conversationWorkspaceGroup(conversation: AiConversation) {
  const workspaceId = conversation.workspaceId?.trim() || conversation.workspaceScopeId?.trim() || "";
  const projectRoot = conversation.projectRoot?.trim() || "";
  const workspaceName = conversationWorkspaceName(conversation);
  const showRoot = Boolean(projectRoot && projectRoot !== workspaceName);
  return {
    key: JSON.stringify([workspaceId, projectRoot]),
    // 标题突出项目文件夹名，完整路径作灰色补充，悬停提示保留工作区名与路径。
    label: showRoot ? projectRoot.split(/[\\/]/).filter(Boolean).pop() || projectRoot : workspaceName,
    detail: showRoot ? projectRoot : "",
    title: showRoot ? `${workspaceName} · ${projectRoot}` : workspaceName,
    tone: workspaceGroupTone(workspaceId || projectRoot || workspaceName),
  };
}
const groupConversationsByWorkspace = computed(
  () => conversationPreferences.globalMode && conversationWorkspaceFilter.value === "all",
);
const unorderedConversationGroups = computed(() => {
  const groups: Array<{
    key: string;
    label: string;
    detail?: string;
    title?: string;
    tone?: number;
    conversations: AiConversation[];
  }> = [];
  const tree = createConversationTree(visibleConversations.value);
  for (const conversation of visibleConversations.value) {
    const anchor = tree.anchor(conversation.id) ?? conversation;
    const workspaceGroup = groupConversationsByWorkspace.value
      ? conversationWorkspaceGroup(anchor)
      : undefined;
    const date = new Date(conversationSortTime(anchor));
    const key = workspaceGroup?.key
      ?? `${date.getFullYear()}-${date.getMonth()}-${date.getDate()}`;
    let group = groups.find((item) => item.key === key);
    if (!group) {
      group = {
        key,
        label: workspaceGroup?.label ?? conversationDateLabel(date),
        detail: workspaceGroup?.detail,
        title: workspaceGroup?.title,
        tone: workspaceGroup?.tone,
        conversations: [],
      };
      groups.push(group);
    }
    group.conversations.push(conversation);
  }
  return groups.map((group) => ({
    ...group,
    conversations: [
      ...group.conversations.filter((conversation) => conversation.pinned),
      ...group.conversations.filter((conversation) => !conversation.pinned),
    ],
  }));
});
watch(
  () => groupConversationsByWorkspace.value
    ? unorderedConversationGroups.value.map((group) => group.key)
    : [],
  (keys) => {
    if (keys.length) void conversationPreferences.ensureWorkspaceGroupOrder(keys);
  },
  { immediate: true },
);
const groupedConversations = computed(() => {
  if (!groupConversationsByWorkspace.value) return unorderedConversationGroups.value;
  const orderByKey = new Map(
    conversationPreferences.workspaceGroupOrder.map((key, index) => [key, index]),
  );
  return unorderedConversationGroups.value.slice().sort((left, right) => {
    const leftIndex = orderByKey.get(left.key) ?? Number.MAX_SAFE_INTEGER;
    const rightIndex = orderByKey.get(right.key) ?? Number.MAX_SAFE_INTEGER;
    return leftIndex - rightIndex;
  });
});
function reorderConversationWorkspaceGroups(keys: string[]) {
  if (!groupConversationsByWorkspace.value) return;
  void conversationPreferences.setWorkspaceGroupOrder(
    applyVisibleWorkspaceGroupOrder(conversationPreferences.workspaceGroupOrder, keys),
  );
}

// Plugin prompt templates only expand into the draft, so they are offered in every mode.
const pluginPromptSuggestions = computed<AiPromptSuggestionPrompt[]>(() =>
  pluginPrompts.enabledPrompts.map((prompt) => ({
    key: prompt.key,
    name: pluginAiPromptTitle(prompt, localeChain.value),
    description: pluginAiPromptDescription(prompt, localeChain.value),
    source: prompt.pluginName,
  })),
);
const filteredToolSuggestions = computed(() =>
  buildAiPromptSuggestions(
    input.value,
    agentTools.value,
    pluginPromptSuggestions.value,
  )
);
watch(filteredToolSuggestions, () => {
  activeToolIndex.value = 0;
});

function conversationDateLabel(date: Date) {
  const today = new Date();
  const startOfToday = new Date(today.getFullYear(), today.getMonth(), today.getDate()).getTime();
  const value = new Date(date.getFullYear(), date.getMonth(), date.getDate()).getTime();
  const dayDifference = Math.round((startOfToday - value) / 86_400_000);
  if (dayDifference === 0) return t("ai.conversation.today");
  if (dayDifference === 1) return t("ai.conversation.yesterday");
  return date.toLocaleDateString(undefined, { month: "short", day: "numeric" });
}
const localizedStatusOptions = computed(() =>
  statusOptions.map((option) => ({ value: option.value, label: t(option.label) })),
);
const conversationWorkspaceOptions = computed(() => {
  const availableIds = new Set(workbench.workspaces.map((workspace) => workspace.id));
  const unavailable = new Map<string, string>();
  for (const entry of workbench.deletedWorkspaces) {
    if (!availableIds.has(entry.workspace.id)) unavailable.set(entry.workspace.id, entry.workspace.name);
  }
  for (const conversation of assistant.allConversations) {
    const workspaceId = conversation.workspaceId?.trim();
    if (workspaceId && !availableIds.has(workspaceId) && !unavailable.has(workspaceId)) {
      unavailable.set(workspaceId, conversation.workspaceName || workspaceId);
    }
  }
  return [
    { value: "all", label: t("ai.conversation.workspace.all") },
    ...workbench.workspaces.map((workspace) => ({ value: workspace.id, label: workspace.name })),
    ...[...unavailable].map(([value, name]) => ({
      value,
      label: `${name} · ${t("ai.conversation.workspace.unavailable")}`,
    })),
  ];
});
function conversationWorkspaceAvailable(conversation: AiConversation) {
  return workbench.workspaces.some((workspace) => workspace.id === conversation.workspaceId);
}
/** AI 对话按所属工作区关联笔记；工作区名称取已知名称，不用「不可用」提示文案作快照。 */
function openConversationNotes(conversation: AiConversation) {
  void useLinkedNotesDrawer().open({
    objectType: "ai",
    objectId: conversation.id,
    nameSnapshot: conversation.title,
    scopeId: conversation.workspaceScopeId,
    scopeNameSnapshot: conversationWorkspaceNames.value[conversation.workspaceId || ""] || conversation.workspaceName || "",
  });
}

function conversationWorkspaceName(conversation: AiConversation) {
  return conversationWorkspaceNames.value[conversation.workspaceId || ""]
    || conversation.workspaceName
    || t("ai.conversation.workspace.unavailable");
}
const activeConversationWorkspaceName = computed(() => {
  const conversation = assistant.activeConversation;
  return conversation ? conversationWorkspaceName(conversation) : "";
});
const homeDirectory = ref("");
const homeDirectoryReady = isDesktopRuntime()
  ? getUserHomeDirectory().then((path) => { homeDirectory.value = path; })
  : Promise.resolve();
void homeDirectoryReady.catch(() => undefined);
function canChooseWorkingDirectory() {
  const conversation = assistant.activeConversation;
  return isDesktopRuntime() && Boolean(conversation && !conversation.external && !conversation.running
    && !conversation.agentReady && !agentStartPromises.has(conversation.id)
    && !acpSendGate.isClaimed(conversation.id) && !agenticSendGate.isClaimed(conversation.id));
}
const choosingWorkingDirectory = ref(false);
async function chooseWorkingDirectory() {
  const conversation = assistant.activeConversation;
  if (!conversation || !canChooseWorkingDirectory() || choosingWorkingDirectory.value) return;
  choosingWorkingDirectory.value = true;
  try {
    const path = await chooseNativeDirectory(t("project.chooseFolder"), workspaceRoot());
    if (!path || assistant.activeConversationId !== conversation.id || !canChooseWorkingDirectory()) return;
    assistant.setConversationWorkspace(conversation.id, {
      workspaceId: conversation.workspaceId ?? workbench.activeWorkspaceId,
      workspaceName: conversation.workspaceName,
      scopeId: conversation.workspaceScopeId ?? assistant.activeWorkspaceScopeId,
      projectRoot: path,
    });
    conversationWorkspaceContexts.delete(conversation.id);
    unavailableAcpDirectories.delete(conversation.id);
    await stopDefaultDirectoryAgent(conversation.id);
  } catch (error) {
    ElMessage.error(nativeErrorMessage(error));
  } finally {
    choosingWorkingDirectory.value = false;
  }
}
const pendingEditsProjectRoot = computed(() =>
  assistant.activeConversation?.projectRoot?.trim() || workspaceRoot()
);
const dismissedMentionInput = ref<string>();
// "@mention" of a plugin context provider (contributes.aiContextProviders): offered under the same
// gate as rules injection, it only opens a preview; the user decides whether to insert.
const pluginContextOffered = computed(() => {
  void settingsSnapshotVersion.value;
  return assistant.mode !== "agent" || loadStoredSettings().acpInjectRules === true;
});
const contextMention = computed(() => {
  if (toolSuggestionsVisible.value || input.value === dismissedMentionInput.value) return null;
  if (!pluginContextOffered.value || !aiContextProviders.providers.length) return null;
  return parseAiFileMention(input.value);
});
const contextMentionSuggestions = computed<AiPromptSuggestion[]>(() => {
  const mention = contextMention.value;
  if (!mention) return [];
  return matchContextMentions(aiContextProviders.providers, mention.query).map((provider) => ({
    id: `context:${provider.key}`,
    name: `@${provider.mention}`,
    description: aiContextProviderDescription(provider, localeChain.value),
    kind: "context" as const,
    insertText: provider.mention,
    source: provider.pluginName,
    contextKey: provider.key,
  }));
});
const mentionSuggestionsActive = computed(() => contextMentionSuggestions.value.length > 0);
const composerSuggestions = computed(() => mentionSuggestionsActive.value
  ? contextMentionSuggestions.value
  : filteredToolSuggestions.value);
function dismissComposerSuggestions() {
  if (mentionSuggestionsActive.value) dismissedMentionInput.value = input.value;
  else input.value = "";
}
const pluginContextDialog = ref<{ provider: PluginAiContextProvider; remaining: number }>();
/** Characters of the rules context the next request adds (the budget plugin context shares). */
function baseAiContextChars() {
  const workspaceId = assistant.activeConversation?.workspaceId || workbench.activeWorkspaceId;
  return enabledAiContext(workspaceId).length;
}
const pluginContextBudgetState = computed(() => {
  if (!attachments.value.some(isPluginContextAttachment)) return undefined;
  void settingsSnapshotVersion.value;
  return pluginContextBudget(baseAiContextChars(), attachments.value);
});
function openPluginContext(suggestion: AiPromptSuggestion) {
  const provider = aiContextProviders.byKey(suggestion.contextKey);
  const mention = contextMention.value;
  if (!provider || !mention) return;
  input.value = applyAiFileMention(input.value, mention, provider.mention);
  activeToolIndex.value = 0;
  const name = pluginContextAttachmentName(provider.pluginId, provider.id);
  const others = attachments.value.filter((attachment) => attachment.name !== name);
  pluginContextDialog.value = { provider, remaining: pluginContextBudget(baseAiContextChars(), others).remaining };
}
function insertPluginContext(value: { text: string }) {
  const provider = pluginContextDialog.value?.provider;
  if (!provider || !value.text) return;
  const name = pluginContextAttachmentName(provider.pluginId, provider.id);
  attachments.value = [
    ...attachments.value.filter((attachment) => attachment.name !== name),
    {
      id: globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`,
      name,
      mimeType: "text/markdown",
      size: new TextEncoder().encode(value.text).length,
      kind: "text",
      data: value.text,
      origin: { kind: "plugin-context", pluginId: provider.pluginId, providerId: provider.id },
    },
  ];
}
const attachmentChipLabels = computed<Record<string, string>>(() => Object.fromEntries(
  attachments.value.flatMap((attachment) => {
    if (attachment.origin?.kind !== "plugin-context") return [];
    const pluginId = attachment.origin.pluginId;
    const plugin = aiContextProviders.providers.find((provider) => provider.pluginId === pluginId)?.pluginName ?? pluginId;
    return [[attachment.id, t("pluginContrib.aiContextProviders.chipLabel", { plugin })]];
  }),
));
const activeConversationWorkspaceAvailable = computed(() => {
  const conversation = assistant.activeConversation;
  return Boolean(conversation && conversationWorkspaceAvailable(conversation));
});
const conversationSidebarLabels = computed(() => ({
  title: t("ai.conversations"),
  exportConversation: t("ai.export.action"),
  messageMatches: t("ai.search.messageMatches"),
  create: t("ai.conversation.new"),
  active: t("ai.conversation.active"),
  archived: t("ai.conversation.archived"),
  status: t("ai.conversation.status.label"),
  sort: t("ai.conversation.sort.label"),
  updated: t("ai.conversation.sort.updated"),
  created: t("ai.conversation.sort.created"),
  updatedAt: t("ai.conversation.updatedAt"),
  empty: t("ai.conversation.empty"),
  actions: t("ai.conversation.actions"),
  archive: t("ai.conversation.archive"),
  unarchive: t("ai.conversation.unarchive"),
  pin: t("ai.conversation.pin"),
  unpin: t("ai.conversation.unpin"),
    pinned: t("ai.conversation.pinned"),
  rename: t("ai.conversation.rename"),
  renamePlaceholder: t("ai.conversation.renamePlaceholder"),
  remove: t("ai.conversation.delete"),
  clearMessages: t("ai.conversation.clearMessages"),
  untitled: t("ai.conversation.untitled"),
  confirm: t("common.confirm"),
  cancel: t("common.cancel"),
  backgroundRunning: t("ai.conversation.backgroundRunning"),
      unreadCompleted: t("ai.conversation.unreadCompleted"),
  unread: t("ai.conversation.unread"),
  markRead: t("ai.conversation.markRead"),
  markUnread: t("ai.conversation.markUnread"),
  workspace: t("ai.conversation.workspace.label"),
  search: t("ai.conversation.search"),
  workspaceUnavailable: t("ai.conversation.workspace.unavailable"),
  switchWorkspace: t("ai.conversation.workspace.switch"),
  relinkWorkspace: t("ai.conversation.workspace.relink"),
  reorderWorkspace: t("ai.conversation.workspace.reorder"),
  statusLabels: Object.fromEntries(
    statusOptions
      .filter((option) => option.value !== "all")
      .map((option) => [option.value, t(option.label)]),
  ),
}));
async function createConversation() {
  await assistant.createConversationForTopWorkspace(assistant.mode);
}

async function dispatchConversationTask() {
  const parentId = assistant.activeConversationId;
  let prompt: string;
  try {
    const { value } = await ElMessageBox.prompt(t("ai.delegation.prompt"), t("ai.delegation.dispatch"), {
      inputType: "textarea",
      closeOnClickModal: false,
      confirmButtonText: t("ai.delegation.dispatch"),
      inputValidator: (value) => Boolean(value?.trim()) || t("ai.delegation.required"),
    });
    prompt = value.trim();
  } catch {
    return;
  }
  const parent = assistant.conversationById(parentId);
  if (!parent || parent.mode !== "agentic" || parent.external || parallelDelegationPending(parent)) {
    ElMessage.warning(t("ai.delegation.startFailed"));
    return;
  }
  const content = t("ai.delegation.request", { task: prompt });
  conversationSidebarOpen.value = true;
  if (parent.running) {
    assistant.enqueueMessage(parentId, { content, createdAt: Date.now() });
    ElMessage.info(t("ai.queue.enqueued"));
    return;
  }
  await sendAgentic({ conversationId: parentId, content, attachments: [] });
}
async function openMessageMatch(match: AiMessageSearchMatch) {
  if (!assistant.allConversations.some((item) => item.id === match.conversationId)) {
    ElMessage.info(t("ai.search.conversationHidden"));
    return;
  }
  await switchConversation(match.conversationId);
  if (assistant.activeConversationId !== match.conversationId) return;
  // Only user messages are scroll targets; a matched reply opens its turn.
  const target = match.role === "user"
    ? match.messageId
    : precedingUserMessage(assistant.messages, match.messageId)?.id;
  if (target) await scrollToConversationMessage(target);
}

async function exportConversation(conversation: AiConversation) {
  if (!(await assistant.ensureConversationDetails(conversation.id))) {
    ElMessage.error(t("ai.conversationLoadFailed"));
    return;
  }
  const loaded = assistant.conversationById(conversation.id);
  if (!loaded) return;
  const title = loaded.title || t("ai.conversation.untitled");
  const content = conversationToMarkdown({
    title,
    messages: loaded.messages,
    exportedAt: new Date(),
    formatTime: (timestamp) => formatAiMessageFullTime(timestamp, intlLocale.value),
    labels: {
      user: t("ai.export.user"),
      assistant: t("ai.export.assistant"),
      error: t("ai.errorCard.title"),
      attachments: t("ai.export.attachments"),
      exportedAt: t("ai.export.exportedAt"),
    },
  });
  try {
    const saved = await saveTextFileToDisk({
      title: t("ai.export.dialogTitle"),
      filename: conversationExportFileName(title),
      content,
      filters: [{ DisplayName: "Markdown", Pattern: "*.md" }],
      browserMimeType: "text/markdown",
    });
    if (saved) ElMessage.success(t("ai.export.done"));
  } catch (error) {
    ElMessage.error(t("ai.export.failed", { message: nativeErrorMessage(error) }));
  }
}

async function switchConversation(id: string) {
  const conversation = assistant.allConversations.find((item) => item.id === id);
  if (!conversation || !await assistant.switchToConversation(id, conversation.workspaceScopeId)) return;
  switchTopWorkspaceForConversation(conversation);
  await restoreConversationScroll(id);
}
async function activateConversationForMutation(conversation: AiConversation) {
  return assistant.switchToConversation(conversation.id, conversation.workspaceScopeId);
}
async function renameConversation(conversation: AiConversation, title: string) {
  if (await activateConversationForMutation(conversation)) {
    assistant.renameConversation(conversation.id, title);
  }
}
async function toggleConversationArchive(conversation: AiConversation) {
  try {
    await assistant.setConversationArchived(conversation.id, !conversation.archived);
  } catch (error) {
    ElMessage.error(nativeErrorMessage(error));
  }
}
async function toggleConversationPinned(conversation: AiConversation) {
  if (!await activateConversationForMutation(conversation)) return;
  assistant.setConversationPinned(conversation.id, !conversation.pinned);
}
function setConversationReadState(conversation: AiConversation, unread: boolean) {
  assistant.setConversationUnread(conversation.id, unread);
}
function switchTopWorkspaceForConversation(conversation: AiConversation) {
  if (!conversation.workspaceId || !conversationWorkspaceAvailable(conversation)) return;
  const tab = workbench.switchWorkspace(conversation.workspaceId);
  void router.push(workspaceTabRoute(tab));
}
async function performConversationRelink(conversation: AiConversation) {
  if (!await assistant.switchToConversation(conversation.id, conversation.workspaceScopeId)) return;
  try {
    const current = assistant.conversationById(conversation.id);
    const connectedServerId = current?.agentReady ? current.acpServerId : undefined;
    const moved = await assistant.moveConversationToWorkspace(conversation.id, {
      workspaceId: workbench.activeWorkspaceId,
      workspaceName: workbench.activeWorkspace?.name,
      scopeId: assistant.topWorkspaceScopeId,
      projectRoot: workspaceRoot(),
    });
    if (!moved) return;
    conversationWorkspaceContexts.delete(conversation.id);
    unavailableAcpDirectories.delete(conversation.id);
    const target = releaseAcpRun(conversation.id);
    const serverId = target?.serverId ?? connectedServerId;
    if (serverId) {
      assistant.setConversationAgentState(conversation.id, { agentReady: false });
      await invokeBackend("acp_disconnect", { serverId });
    }
    ElMessage.success(t("ai.conversation.workspace.relinked", {
      name: workbench.activeWorkspace?.name || workbench.activeWorkspaceId,
    }));
  } catch (error) {
    ElMessage.error(t("ai.conversation.workspace.relinkFailed", {
      reason: nativeErrorMessage(error),
    }));
  }
}
async function relinkConversationToTopWorkspace(conversation: AiConversation) {
  const id = conversation.id;
  if (acpWorkspaceRelinks.has(id)) return;
  const acpRunning = conversation.running && (conversation.mode === "agent" || acpRunTargets.has(id));
  if (acpRunning || acpSendGate.isClaimed(id) || agentStartPromises.has(id)) {
    ElMessage.info(t("ai.send.busy"));
    return;
  }
  const relink = performConversationRelink(conversation);
  acpWorkspaceRelinks.set(id, relink);
  try {
    await relink;
  } finally {
    acpWorkspaceRelinks.delete(id);
  }
}
async function deleteConversation(conversation: AiConversation) {
  try {
    let family = await assistant.conversationFamily(conversation.id);
    if (!family.length) return;
    const ids = new Set(family.map(({ location }) => location.conversationId));
    const activeRuns = () => progress.activeRuns.filter((run) => run.navigation.type === "ai-conversation"
      && ids.has(run.navigation.conversationId));
    const inProgress = activeRuns().length > 0 || family.some(({ conversation }) =>
      conversation.running || parallelDelegationPending(conversation));
    const relatedCount = family.length - 1;
    const confirmation = relatedCount
      ? t(inProgress ? "ai.conversation.deleteRunningFamilyConfirm" : "ai.conversation.deleteFamilyConfirm", { count: relatedCount })
      : t(inProgress ? "ai.conversation.deleteRunningConfirm" : "ai.conversation.deleteConfirm");
    try {
      await ElMessageBox.confirm(
        confirmation,
        t("ai.conversation.delete"),
        { confirmButtonText: t("common.delete"), cancelButtonText: t("common.cancel"), type: "warning" },
      );
    } catch {
      return;
    }
    family = await assistant.conversationFamily(conversation.id);
    if (!family.length) return;
    for (const { location } of family) ids.add(location.conversationId);
    const runs = activeRuns();
    if (inProgress || runs.length || family.some(({ conversation }) => parallelDelegationPending(conversation))) {
      for (const id of ids) delegatedTaskAborts.get(id)?.abort();
      await Promise.all(runs.map((run) => progress.requestCancel(run.runId)));
      const stopped = await Promise.all([...ids].map((id) => waitForConversationStop(id, 12_000)));
      if (stopped.some((result) => !result)) {
        ElMessage.warning(t("ai.conversation.deleteStillRunning"));
        return;
      }
    }
    if (!await activateConversationForMutation(conversation)) return;
    const deleted = await assistant.deleteConversation(conversation.id, family.slice(1).map(({ location }) => location));
    if (!deleted) return;
    for (const item of family) await releaseDeletedConversation(item.conversation);
  } catch (error) {
    ElMessage.error(t("ai.conversation.deleteFailed", { reason: nativeErrorMessage(error) }));
    return;
  }
}
async function releaseDeletedConversation(conversation: AiConversation) {
  // 持久化删除成功后再清理外部状态，避免落盘失败回滚出一个已被取消的会话。
  if (conversation.pendingPermission) {
    await invokeBackend("acp_respond_permission", {
      requestId: conversation.pendingPermission.requestId,
      serverId: conversation.pendingPermission.serverId,
      sessionId: conversation.pendingPermission.sessionId,
      cancelled: true,
    }).catch((error) => ElMessage.error(nativeErrorMessage(error)));
  }
  const target = releaseAcpRun(conversation.id);
  if (target) {
    await invokeBackend("acp_disconnect", { serverId: target.serverId })
      .catch((error) => ElMessage.error(nativeErrorMessage(error)));
  }
  unavailableAcpDirectories.delete(conversation.id);
  toolApprovalQueue.rejectConversation(conversation.id);
  planModeReminders.delete(conversation.id);
}
async function clearConversationMessages(conversation: AiConversation) {
  if (conversation.running || !await activateConversationForMutation(conversation)) return;
  const active = assistant.conversationById(conversation.id);
  if (!active?.messages.length) return;
  assistant.clearConversationMessages(conversation.id);
  ElMessage.success(t("ai.conversation.messagesCleared"));
}
function insertToolSuggestion(suggestion: AiPromptSuggestion) {
  if (suggestion.kind === "prompt") {
    insertPluginPrompt(suggestion.promptKey ?? "");
    return;
  }
  if (suggestion.kind === "context") {
    openPluginContext(suggestion);
    return;
  }
  input.value = suggestion.insertText;
  activeToolIndex.value = 0;
}
/** Expands a plugin prompt template into the draft for the user to review; it is never sent automatically. */
function insertPluginPrompt(key: string) {
  const prompt = pluginPrompts.enabledPrompts.find((item) => item.key === key);
  if (!prompt) return;
  const roots = [workspaceRoot()].filter(Boolean);
  const expanded = expandAiPromptTemplate(
    prompt.body,
    prompt.variables.length ? currentAiPromptVariables(roots) : {},
    t("pluginContrib.aiPrompts.selectionTruncated"),
  );
  input.value = expanded.text;
  activeToolIndex.value = 0;
  if (expanded.selectionTruncated) ElMessage.warning(t("pluginContrib.aiPrompts.selectionTruncated"));
}

function loadStoredSettings(): StoredSettings {
  return readSettingsSnapshot<StoredSettings & Record<string, unknown>>();
}
function activeAiProfile() {
  const stored = loadStoredSettings();
  return (
    stored.aiProfiles?.find((item) => item.id === stored.activeAiProfileId) ??
    stored.aiProfiles?.[0] ??
    {}
  );
}
function currentAiConfig(profile = activeAiProfile()): AiLoopConfig {
  const apiFormat = profile.apiFormat;
  return {
    baseUrl: typeof profile.baseUrl === "string" ? profile.baseUrl : "https://api.openai.com/v1",
    apiKey: typeof profile.apiKey === "string" ? profile.apiKey : "",
    model: typeof profile.model === "string" && profile.model.trim() ? profile.model.trim() : "gpt-6-astra",
    apiFormat: normalizeAiApiFormat(apiFormat),
    anthropicAuth: normalizeAnthropicAuth(profile.anthropicAuth),
    reasoningEffort: normalizeAiReasoningEffort(profile.reasoningEffort),
    contextWindow:
      typeof profile.contextWindow === "number" ? profile.contextWindow : undefined,
    maxInputTokens:
      typeof profile.maxInputTokens === "number" ? profile.maxInputTokens : undefined,
    maxOutputTokens:
      typeof profile.maxOutputTokens === "number" ? profile.maxOutputTokens : undefined,
    openaiStreamUsage: profile.openaiStreamUsage ?? "auto",
    noToolModels: Array.isArray(profile.noToolModels) ? profile.noToolModels : [],
  };
}
function profileModels(profile = activeAiProfile()) {
  const configured = currentAiConfig(profile).model;
  return configuredAiModels(configured, Array.isArray(profile.commonModels) ? profile.commonModels : []);
}
interface AiModelSelectOption {
  value: string;
  label: string;
  profileId: string;
  profileLabel: string;
}
function modelOptionValue(profileId: string, model: string) {
  return `${profileId}::${model}`;
}
function profileModelOptions(profile: StoredAiProfile, index: number): AiModelSelectOption[] {
  const profileId = typeof profile.id === "string" && profile.id.trim() ? profile.id : `profile-${index}`;
  const profileLabel = typeof profile.name === "string" && profile.name.trim()
    ? profile.name.trim()
    : profileId;
  return profileModels(profile).map((model) => ({
    value: modelOptionValue(profileId, model),
    label: model,
    profileId,
    profileLabel,
  }));
}
const availableModels = computed<AiModelSelectOption[]>(() => {
  void settingsSnapshotVersion.value;
  const stored = loadStoredSettings();
  return (stored.aiProfiles ?? []).flatMap((profile, index) => profileModelOptions(profile, index));
});
const defaultModelOption = computed(() => {
  const stored = loadStoredSettings();
  const profile = activeAiProfile();
  const profileId = typeof profile.id === "string" ? profile.id : stored.aiProfiles?.[0]?.id;
  const model = currentAiConfig(profile).model;
  return availableModels.value.find(
    (option) => option.profileId === profileId && option.label === model,
  ) ?? availableModels.value[0];
});
const selectedModel = computed({
  get: () => availableModels.value.some((option) => option.value === assistant.selectedModel)
    ? assistant.selectedModel
    : defaultModelOption.value?.value ?? "",
  set: (value: string) => assistant.setSelectedModel(value),
});
watch(availableModels, (models, previous) => {
  if (!previous?.length) return;
  if (!models.some((model) => model.value === assistant.selectedModel)) {
    assistant.setSelectedModel("");
  }
});
const modelSelectionSupported = computed(() => assistant.mode !== "agent");
const activeAcpConfigOptions = computed(
  () => acpConfigOptions.value[assistant.activeConversationId] ?? [],
);
async function applyAcpConfigOption(configId: string, value: string) {
  const conversationId = assistant.activeConversationId;
  const target = acpRunTargets.get(conversationId);
  if (!target) return;
  acpConfigPending.value = configId;
  try {
    const result = await invokeBackend<AcpConfigOptionsResult>("acp_set_config_option", {
      serverId: target.serverId,
      sessionId: target.sessionId,
      configId,
      value,
    });
    if (acpRunTargets.get(conversationId) !== target) return;
    // The Agent echoes the authoritative option set, which may adjust values
    // other than the one just changed.
    acpConfigOptions.value = {
      ...acpConfigOptions.value,
      [conversationId]: parseAcpConfigSelectOptions(result?.configOptions),
    };
  } catch (error) {
    ElMessage.error(String(error));
  } finally {
    if (acpConfigPending.value === configId) acpConfigPending.value = "";
  }
}
// 档位列表从 domain 派生，避免设置页和这里各维护一份而漏掉新增等级。
const localizedReasoningEffortOptions = computed(() => [
  { value: "default", label: t("ai.reasoning.default") },
  ...AI_REASONING_EFFORT_CHOICES.map((effort) => ({
    value: effort,
    label: t(`ai.reasoning.${effort}`),
  })),
]);
// Estimated from the saved model history, which changes per turn (not per chunk).
// Tools and system prompts are not included, so the label says "about".
const CONTEXT_USAGE_WARNING_RATIO = 0.6;
// Prefers the provider-reported prompt size of the latest reply, which includes the
// system prompt and tool definitions; older replies without it fall back to an estimate.
const contextUsage = computed(() => {
  if (assistant.mode === "agent") return undefined;
  const config = currentAiConfig();
  const reported = [...assistant.messages].reverse().find((message) => message.tokenUsage?.contextTokens);
  const estimate = estimateAiContextUsage(assistant.agentHistory, config);
  const usedTokens = reported?.tokenUsage?.contextTokens ?? estimate.estimatedTokens;
  if (!usedTokens || estimate.inputLimit <= 0) return undefined;
  const ratio = usedTokens / estimate.inputLimit;
  const level = ratio >= AI_CONTEXT_COMPACTION_THRESHOLD
    ? "danger" as const
    : ratio >= CONTEXT_USAGE_WARNING_RATIO ? "warning" as const : "normal" as const;
  const percent = ratio > 0 && ratio < 0.01 ? "<1" : String(Math.min(100, Math.round(ratio * 100)));
  return {
    label: t("ai.context.usage", { percent }),
    title: t(reported ? "ai.context.usageDetail" : "ai.context.usageEstimateDetail", {
      used: compactTokenCount(usedTokens),
      limit: compactTokenCount(estimate.inputLimit),
      threshold: Math.round(AI_CONTEXT_COMPACTION_THRESHOLD * 100),
    }),
    level,
  };
});
const composerLabels = computed(() => ({
  placeholder: t("ai.inputPlaceholder"),
  addAttachment: t("ai.attachment.add"),
  sendHint: t("ai.sendHint"),
  send: t("common.send"),
  stop: t("common.stop"),
  model: t("ai.model.label"),
  modelGroup: t("ai.model.group"),
  profileGroup: t("ai.model.profileGroup", { profile: "{name}" }),
  chooseModel: t("ai.model.choose"),
  noModels: t("ai.model.noConfiguredModels"),
  reasoning: t("ai.reasoning.label"),
  removeAttachment: t("ai.attachment.remove", { name: "{name}" }),
  previewAttachment: t("ai.attachment.preview", { name: "{name}" }),
  suggestionList: mentionSuggestionsActive.value ? t("ai.fileMention.label") : t("ai.toolSuggest.label"),
  suggestionHint: mentionSuggestionsActive.value ? t("ai.fileMention.hint") : t("ai.toolSuggest.hint"),
  suggestionPrompt: t("pluginContrib.aiPrompts.suggestionPrompt"),
  suggestionTool: t("ai.toolSuggest.tool"),
  suggestionFile: t("ai.fileMention.file"),
  suggestionContext: t("pluginContrib.aiContextProviders.suggestion"),
  optimizePrompt: t("ai.promptOptimizer.action"),
  cancelOptimization: t("ai.promptOptimizer.cancel"),
}));
const selectedReasoningEffort = computed({
  get: () => isAiReasoningEffortChoice(assistant.selectedReasoningEffort)
    ? assistant.selectedReasoningEffort
    : "default",
  set: (value: string) => assistant.setSelectedReasoningEffort(value === "default" ? "" : value),
});

const noToolModelsNotified = new Set<string>();
/** Tells the user once per model that a chat-only model gets no tools. */
function notifyNoToolModel(config: { model: string; noToolModels?: string[] }) {
  if (!config.noToolModels?.includes(config.model) || noToolModelsNotified.has(config.model)) return;
  noToolModelsNotified.add(config.model);
  ElMessage.info(t("ai.noToolModel", { model: config.model }));
}
function aiRequestPlan(conversationId = assistant.activeConversationId, modelOverride?: string) {
  const target = assistant.conversationById(conversationId);
  const active = conversationId === assistant.activeConversationId;
  const modelSelection = modelOverride ?? (active ? selectedModel.value : target?.selectedModel ?? defaultModelOption.value?.value);
  const effortSelection = active ? selectedReasoningEffort.value : target?.selectedReasoningEffort;
  const selectedOption = availableModels.value.find((option) => option.value === modelSelection);
  const stored = loadStoredSettings();
  const profile = selectedOption
    ? stored.aiProfiles?.find((item) => item.id === selectedOption.profileId) ?? activeAiProfile()
    : activeAiProfile();
  const config = currentAiConfig(profile);
  const configuredModels = profileModels(profile);
  const selectedRawModel = selectedOption?.label;
  const attemptModels = buildAiModelAttemptOrder({
    selectedModel: selectedRawModel,
    profileDefaultModel: config.model,
    configuredModels,
    fallbackEnabled: profile.failoverEnabled === true,
    fallbackModels: Array.isArray(profile.failoverModels) ? profile.failoverModels : [],
  });
  return {
    profile,
    config: {
      ...config,
      model: attemptModels[0] ?? config.model,
      reasoningEffort: resolveAiReasoningEffort(effortSelection, config.reasoningEffort),
    },
    attemptModels,
  };
}

function cancelPromptOptimization() {
  promptOptimizationController?.abort();
}

function closePromptOptimization() {
  cancelPromptOptimization();
  promptOptimizationVisible.value = false;
  promptOptimizationOriginal.value = "";
  promptOptimizationResult.value = "";
}

function applyOptimizedPrompt() {
  if (!promptOptimizationResult.value.trim()) return;
  input.value = promptOptimizationResult.value.trim();
  promptOptimizationVisible.value = false;
  promptOptimizationOriginal.value = "";
  promptOptimizationResult.value = "";
  ElMessage.success(t("ai.promptOptimizer.applied"));
}

async function startPromptOptimization() {
  const original = input.value.trim();
  if (!original || promptOptimizationLoading.value) return;
  if (!canStreamAi()) {
    ElMessage.warning(t("common.desktopRequired"));
    return;
  }
  try {
    await hydrateSettingsSnapshot();
  } catch {
    ElMessage.error(t("ai.credentialsLoadFailed"));
    return;
  }

  const { profile, config, attemptModels } = aiRequestPlan();
  const auth = validateAiProviderAuth({
    provider: profile.provider,
    baseUrl: config.baseUrl,
    apiKey: config.apiKey,
  });
  if (!auth.valid) {
    ElMessage.warning(t(aiProviderAuthMessageKey(auth)));
    return;
  }

  promptOptimizationOriginal.value = original;
  promptOptimizationResult.value = "";
  promptOptimizationVisible.value = true;
  promptOptimizationLoading.value = true;
  const controller = new AbortController();
  promptOptimizationController = controller;
  try {
    promptOptimizationResult.value = await optimizeAiPrompt({
      config,
      models: attemptModels,
      prompt: original,
      signal: controller.signal,
      onFallback: (from, to) => notifyModelFallback(from, to),
    });
  } catch (error) {
    if (error instanceof DOMException && error.name === "AbortError") {
      closePromptOptimization();
    } else {
      ElMessage.error(t("ai.promptOptimizer.failed", { reason: nativeErrorMessage(error) }));
      closePromptOptimization();
    }
  } finally {
    if (promptOptimizationController === controller) {
      promptOptimizationController = undefined;
      promptOptimizationLoading.value = false;
    }
  }
}

function notifyModelFallback(from: string, to: string) {
  ElMessage.warning(t("ai.model.failoverRetry", { from, to }));
  notifyApp({
    source: "ai",
    level: "warning",
    title: t("notifications.ai.modelFallback"),
  });
}

function notifyAi(notification: {
  level: "success" | "warning" | "error";
  titleKey: Parameters<typeof t>[0];
  message?: string;
  detail?: string;
}, conversationId?: string) {
  const conversation = conversationId ? assistant.conversationById(conversationId) : undefined;
  notifyApp({
    source: "ai",
    level: notification.level,
    title: t(notification.titleKey),
    message: notification.message,
    detail: notification.detail,
    target: conversationId
      ? {
          kind: "ai-conversation",
          conversationId,
          workspaceScopeId: conversation?.workspaceScopeId || assistant.activeWorkspaceScopeId,
        }
      : undefined,
  });
}

function stringRecord(value: unknown) {
  if (!value || typeof value !== "object" || Array.isArray(value))
    return {} as Record<string, string>;
  return Object.fromEntries(
    Object.entries(value).filter(
      (entry): entry is [string, string] =>
        typeof entry[0] === "string" && typeof entry[1] === "string"
    )
  );
}
function enabledAiContext(workspaceId: string, includeRules = true) {
  const stored = loadStoredSettings();
  const sections: string[] = [];
  const rules = Array.isArray(stored.aiRules) ? stored.aiRules : [];
  const enabledRules = includeRules
    ? applicableAiRules(rules, workspaceId)
        .filter(
          (rule) =>
            rule?.enabled !== false &&
            typeof rule.content === "string" &&
            rule.content.trim(),
        )
        .map(
          (rule) =>
            `### ${typeof rule.title === "string" && rule.title.trim() ? rule.title.trim() : "Rule"}\n${rule.content!.trim()}`,
        )
    : [];
  if (enabledRules.length) sections.push(`## MinCode rules\n${enabledRules.join("\n\n")}`);

  const context = sections.join("\n\n");
  return context.length > MAX_AI_CONTEXT_CHARS ? context.slice(0, MAX_AI_CONTEXT_CHARS) : context;
}
function configuredAcpMcpServers(projectRoot = workspaceRoot()) {
  const stored = loadStoredSettings();
  return buildAcpMcpServers(Array.isArray(stored.mcpServers) ? stored.mcpServers : [], projectRoot);
}
function mcpToolSchema(value: unknown): Record<string, unknown> {
  const schema = asRecord(value);
  return Object.keys(schema).length
    ? schema
    : { type: "object", properties: {}, additionalProperties: true };
}
function enabledMcpServers(projectRoot = workspaceRoot()) {
  const stored = loadStoredSettings();
  return (Array.isArray(stored.mcpServers) ? stored.mcpServers : [])
    .filter(
      (server): server is StoredMcpServer =>
        Boolean(server && server.enabled !== false && server.id?.trim())
    )
    .map((server) => normalizeMcpServerForRuntime(server, projectRoot));
}
async function connectSavedMcpServerForAi(requestedServerId: string) {
  const serverId = requestedServerId.trim();
  const server = enabledMcpServers().find((candidate) => candidate.id.trim() === serverId);
  if (!server) throw new Error(`Saved enabled connector not found: ${serverId}`);

  const statuses = await invokeBackend<McpServerStatus[]>("mcp_list_status", {}).catch(() => []);
  const connected = statuses.some((status) => status.id === serverId && status.state === "connected");
  const tools = connected
    ? await invokeBackend<McpToolDefinition[]>("mcp_list_tools", { serverId })
    : (await invokeBackend<McpConnectResult>("mcp_connect", {
        serverId,
        transport: server.transport,
        command: server.command,
        args: server.args?.trim() ? server.args.trim().split(/\s+/) : [],
        env: stringRecord(server.env),
        url: server.url,
        headers: stringRecord(server.headers),
      })).tools;
  const [resources, prompts] = await Promise.all([
    invokeBackend<unknown[]>("mcp_list_resources", { serverId }).catch(() => []),
    invokeBackend<unknown[]>("mcp_list_prompts", { serverId }).catch(() => []),
  ]);

  return {
    serverId,
    connected: true,
    toolCount: tools.length,
    resourceCount: resources.length,
    promptCount: prompts.length,
  };
}
/** Connects the given enabled connectors (when needed) and returns their tools. */
async function loadMcpServerTools(
  conversationId: string,
  projectRoot: string,
  serverIds: readonly string[],
): Promise<{ tools: ActiveMcpTool[]; failures: Map<string, string> }> {
  const failures = new Map<string, string>();
  const wanted = new Set(serverIds);
  const servers = enabledMcpServers(projectRoot).filter((server) => wanted.has(server.id.trim()));
  const pluginServers = pluginMcpServers.enabledServers.filter(
    (server) => wanted.has(server.sessionId) && mcpServerAvailable(server, projectRoot),
  );
  if (!servers.length && !pluginServers.length) return { tools: [], failures };
  const reportFailure = (serverId: string, label: string, error: unknown) => {
    // A connector that needs a sign-in is not broken: point at where to sign in.
    const detail = nativeErrorMessage(error);
    failures.set(serverId, detail);
    assistant.upsertAgentEventForConversation(conversationId, {
      id: `mcp-connect-${serverId}`,
      kind: "tool",
      title: `${t("ai.mcp")}: ${label}`,
      detail,
      status: "failed",
    });
  };
  // Plugin servers start through the host, which requires the user's earlier
  // confirmation of the exact launch and never prompts here.
  const pluginToolGroups = Promise.all(pluginServers.map(async (server): Promise<ActiveMcpTool[]> => {
    const serverName = localizedMcpServerText(server.title, server.titleLocalized, localeChain.value);
    try {
      const { tools } = await startPluginMcpServer(server.sessionId, mcpServerLaunchRoot(server, projectRoot));
      return tools.flatMap((tool) => {
        const toolName = tool.name.trim();
        if (!toolName) return [];
        return [{
          name: `mcp.${server.sessionId}.${toolName}`,
          description: `[Connector:${server.sessionId}] ${tool.description?.trim() || toolName}`,
          inputSchema: mcpToolSchema(tool.inputSchema),
          serverId: server.sessionId,
          serverName,
          toolName,
        }];
      });
    } catch (error) {
      reportFailure(server.sessionId, serverName, error);
      return [];
    }
  }));
  if (!servers.length) return { tools: (await pluginToolGroups).flat(), failures };

  const statuses = await invokeBackend<McpServerStatus[]>("mcp_list_status", {}).catch(() => []);
  const connected = new Set(
    statuses.filter((status) => status.state === "connected").map((status) => status.id)
  );

  // MCP server catalogues are independent. Resolve them together so one slow
  // server does not delay every other usable tool; Promise.all preserves server
  // order when the groups are flattened below.
  const toolGroups = await Promise.all(servers.map(async (server): Promise<ActiveMcpTool[]> => {
    const serverId = server.id.trim();
    try {
      const result = connected.has(serverId)
        ? { tools: await invokeBackend<McpToolDefinition[]>("mcp_list_tools", { serverId }) }
        : await invokeBackend<McpConnectResult>("mcp_connect", {
            serverId,
            transport: server.transport,
            command: server.command,
            args: server.args?.trim() ? server.args.trim().split(/\s+/) : [],
            env: stringRecord(server.env),
            url: server.url,
            headers: stringRecord(server.headers),
          });
      return result.tools.flatMap((tool) => {
        if (!tool?.name?.trim()) return [];
        return [{
          name: `mcp.${serverId}.${tool.name.trim()}`,
          description: `[Connector:${serverId}] ${tool.description?.trim() || tool.name.trim()}`,
          inputSchema: mcpToolSchema(tool.inputSchema),
          serverId,
          serverName: serverId,
          toolName: tool.name.trim(),
        }];
      });
    } catch (error) {
      reportFailure(serverId, serverId, error);
      return [];
    }
  }));

  return { tools: [...toolGroups.flat(), ...(await pluginToolGroups).flat()], failures };
}
/** Runs the loader tool: connects one connector and adds its tools to the live turn list. */
async function loadMcpToolsForModel(
  conversationId: string,
  call: AiLoopToolCall,
  serverIds: readonly string[],
  loaded: ActiveMcpTool[],
  projectRoot: string,
  signal: AbortSignal,
): Promise<AiLoopToolExecution> {
  let request: ReturnType<typeof parseMcpLoadRequest>;
  try {
    request = parseMcpLoadRequest(toolArguments(call), serverIds);
  } catch (error) {
    return { content: error instanceof Error ? error.message : String(error), status: "failed" };
  }
  const { tools, failures } = await runWithAbort(signal, () =>
    loadMcpServerTools(conversationId, projectRoot, [request.serverId]));
  const failure = failures.get(request.serverId);
  if (failure) {
    return { content: `Failed to connect connector "${request.serverId}": ${failure}`, status: "failed" };
  }
  const { picked, unknown } = pickMcpTools(tools, request.toolNames);
  for (const tool of picked) {
    if (!loaded.some((item) => item.name === tool.name)) loaded.push(tool);
  }
  return {
    content: mcpLoadSummary(request.serverId, picked, unknown),
    status: "completed",
    source: { kind: "mcp", toolName: MCP_LOAD_TOOL_NAME, serverId: request.serverId, serverName: request.serverId },
  };
}

function safeMcpResult(result: McpCallResult) {
  return sanitizeAiResult(result) as McpCallResult;
}
function mcpResultContent(result: McpCallResult) {
  const sanitized = safeMcpResult(result);
  return safeAiText(
    sanitized.content
      .map((item) => item.text || `[${item.type}]`)
      .filter(Boolean)
      .join("\n\n") || sanitized,
  );
}
function acpPromptText(text: string, conversationId: string, workspaceId: string) {
  const stored = loadStoredSettings();
  if (!stored.acpInjectRules) return text;
  const context = enabledAiContext(
    workspaceId,
    !acpContextInjected.has(conversationId),
  );
  return context ? `${context}

---

${text}` : text;
}
function createId(prefix: string) {
  return typeof crypto.randomUUID === "function"
    ? crypto.randomUUID()
    : `${prefix}-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}
function asRecord(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" ? (value as Record<string, unknown>) : {};
}
function contentText(value: unknown): string {
  const raw = asRecord(value);
  if (raw.type === "text" && typeof raw.text === "string") return raw.text;
  if (typeof raw.text === "string") return raw.text;
  return "";
}
function eventDetail(raw: Record<string, unknown>) {
  if (typeof raw.title === "string") return raw.title;
  if (Array.isArray(raw.entries))
    return raw.entries
      .map((entry) => String(asRecord(entry).content ?? ""))
      .filter(Boolean)
      .join("\n");
  if (raw.rawOutput !== undefined) return JSON.stringify(raw.rawOutput, null, 2);
  if (Array.isArray(raw.content)) return raw.content.map(contentText).filter(Boolean).join("\n");
  return "";
}
function planEventDetail(raw: Record<string, unknown>) {
  if (!Array.isArray(raw.entries)) return eventDetail(raw);
  return JSON.stringify({
    detail: typeof raw.title === "string" ? raw.title : undefined,
    steps: raw.entries.map((entry) => {
      const item = asRecord(entry);
      return {
        title: String(item.content ?? item.title ?? ""),
        detail: typeof item.detail === "string" ? item.detail : undefined,
        status: typeof item.status === "string" ? item.status : undefined,
      };
    }),
  });
}
function toolArguments(call: AiLoopToolCall) {
  try {
    const value = JSON.parse(call.arguments) as unknown;
    return asRecord(value);
  } catch {
    return {};
  }
}
let aiNativeResourcesSynced = false;
let aiNativeResourceSyncPromise: Promise<void> | undefined;
let aiNativeResourceSyncVersion = 0;
let aiNativeResourceSyncTimer: ReturnType<typeof setTimeout> | undefined;
const AI_NATIVE_RESOURCE_SYNC_DEBOUNCE_MS = 250;

function invalidateAiNativeResources() {
  aiNativeResourcesSynced = false;
  aiNativeResourceSyncVersion += 1;
}

/**
 * 监听触发的后台保鲜重建走防抖合并：失效标记立即置位，保证工具执行前的
 * 直接 syncAiNativeResources() 调用能立刻感知并强刷；仅合并窗口内的重复重建。
 */
function scheduleAiNativeResourceSync() {
  if (aiNativeResourceSyncTimer) return;
  aiNativeResourceSyncTimer = setTimeout(() => {
    aiNativeResourceSyncTimer = undefined;
    void syncAiNativeResources();
  }, AI_NATIVE_RESOURCE_SYNC_DEBOUNCE_MS);
}

async function terminalPanes(node: TerminalPaneNode): Promise<Array<{ id: string; type: "local" | "ssh"; name: string; profileId?: string; connected: boolean }>> {
  if (isTerminalLeafPane(node)) {
    const connected = isDesktopRuntime()
      ? await invokeBackend<boolean>("shell_session_alive", { payload: { sessionId: node.id } }).catch(() => false)
      : false;
    return [
      {
        id: node.id,
        type: node.type,
        name: node.type === "ssh" ? node.profile.name : node.id,
        profileId: node.type === "ssh" ? node.profile.id : undefined,
        connected,
      },
    ];
  }
  return (await Promise.all(node.children.map(terminalPanes))).flat();
}
/** Panes of every terminal tab in the current workspace, not only the visible one. */
async function allTerminalPanes() {
  return (await Promise.all(terminalWorkspace.tabs.map((tab) => terminalPanes(tab.root)))).flat();
}
/**
 * Resource discovery probes every terminal session and refreshes the Git snapshot.
 * Keep the snapshot for read-only tool batches; invalidate it after a possible
 * mutation or when workspace state changes so later tools still resolve against
 * current resources.
 */
async function syncAiNativeResources(force = false): Promise<void> {
  if (!force && aiNativeResourcesSynced) return;
  if (aiNativeResourceSyncPromise) {
    await aiNativeResourceSyncPromise;
    if (!force && aiNativeResourcesSynced) return;
  }

  const projectRoot = workspaceRoot();
  const syncVersion = aiNativeResourceSyncVersion;
  aiNativeResourcesSynced = false;
  aiNativeResourceSyncPromise = (async () => {
    const liveTerminalPanes = await allTerminalPanes();
    if (projectRoot !== workspaceRoot() || syncVersion !== aiNativeResourceSyncVersion) return;
    syncNativeAiResources(aiResourceRegistry, {
      nativeRuntime: isDesktopRuntime(),
      projectRoot,
      activeTerminalId: terminalWorkspace.activePaneId,
      database: {
        connections: databaseWorkspace.connections,
        sessions: databaseWorkspace.sessionIds,
        schemas: databaseWorkspace.schemas,
        activeConnectionId: databaseWorkspace.activeConnectionId,
      },
      terminalPanes: liveTerminalPanes,
      sshProfiles: sshConnections.profiles,
      sftpSessions: sftpSessions.sessions.map((session) => ({ ...session, connected: true })),
    });
    aiNativeResourcesSynced = true;
  })();

  try {
    await aiNativeResourceSyncPromise;
  } finally {
    aiNativeResourceSyncPromise = undefined;
  }
  if (!aiNativeResourcesSynced) await syncAiNativeResources(force);
}

// Keep the cached preflight accurate when the user changes a resource outside
// an AI call. This avoids the former expensive forced terminal/Git probe before
// every prompt without hiding newly opened sessions from tool selection.
// 契约：以下源全部整体替换引用（tabs.value = nextTabs 等），浅监听即可感知，
// 不需要 deep 深遍历；新增写入路径若改为原地修改，必须同步改回深监听。
// tabs 覆盖所有终端标签的分屏树（含后台标签），activePaneId 跟随当前标签。
watch(
  [
    () => terminalWorkspace.tabs,
    () => terminalWorkspace.activePaneId,
    () => databaseWorkspace.sessionIds,
    () => databaseWorkspace.activeConnectionId,
  ],
  () => {
    invalidateAiNativeResources();
    scheduleAiNativeResourceSync();
  },
  { flush: "post" }
);
// 契约：connections / SSH profiles / SFTP sessions 存在原地 push、splice 与
// 索引替换写入，getter 浅监听无法感知，必须保留 deep。
watch(
  [
    () => databaseWorkspace.connections,
    () => sshConnections.profiles,
    () => sftpSessions.sessions,
  ],
  () => {
    invalidateAiNativeResources();
    scheduleAiNativeResourceSync();
  },
  { deep: true, flush: "post" }
);

function nativeResourceList(capability: string) {
  return aiResourceRegistry.list(capability).map((resource) => ({
    resourceId: resource.id,
    kind: resource.kind,
    name: resource.name,
    active: resource.active === true,
  }));
}
async function openWorkspaceViewForAi(input: { view: WorkspaceView }) {
  if (!isDesktopRuntime()) throw new Error("workspace.open_view requires the desktop runtime");
  const tab = workbench.openView(input.view);
  await router.push({ name: input.view });
  await nextTick();
  return {
    view: input.view,
    tabId: tab?.id,
  };
}

const aiTerminalSessions = createAiTerminalSessionReuseRegistry();

async function createTerminalTabForAi(input: CreateTerminalTabInput) {
  if (!isDesktopRuntime()) throw new Error("terminal.create_session requires the desktop runtime");
  const reusable = !input.forceNew && input.conversationId
    ? aiTerminalSessions.resolve(input.conversationId, ({ tabId, paneId }) =>
        Boolean(
          workbench.tabs.some((tab) => tab.id === tabId && tab.view === "terminal")
          && terminalWorkspace.localPaneIds().includes(paneId)
        )
      )
    : undefined;
  if (reusable) {
    workbench.selectTab(reusable.tabId);
    terminalWorkspace.activateTab(reusable.tabId);
    terminalWorkspace.selectPane(reusable.paneId);
    await router.push({ name: "terminal" });
    return { ...reusable, reused: true };
  }

  const tab = workbench.createTab("terminal", input.title);
  if (!tab) throw new Error("Failed to create terminal Tab");
  const options: LocalTerminalPaneOptions = {
    shellType: input.shellType,
    initialCwd: input.initialCwd
      || (input.conversationId ? conversationWorkspaceContexts.get(input.conversationId)?.projectRoot : undefined),
  };
  terminalWorkspace.activateTab(tab.id, options);
  const paneId = terminalWorkspace.activePaneId;
  await router.push({ name: "terminal" });
  invalidateAiNativeResources();
  await syncAiNativeResources();
  if (input.conversationId) {
    aiTerminalSessions.remember(input.conversationId, { tabId: tab.id, paneId });
  }
  return { tabId: tab.id, paneId, reused: false };
}
/**
 * Closes the pane's split, or its whole terminal tab when it is that tab's only
 * pane; the last pane of the workspace stays. Ends each closed pane's shell once
 * here — tab cleanup may close it again, which is harmless but not relied on.
 */
async function closeTerminalPaneForAi(sessionId: string) {
  if (!isDesktopRuntime()) return false;
  const tabsState = { tabs: terminalWorkspace.tabs, activeTabId: terminalWorkspace.activeTabId };
  const target = aiCloseTargetForPane(tabsState, sessionId);
  let closed = false;
  if (target === "pane") {
    await invokeBackend<void>("close_local_shell", { payload: { sessionId } });
    closed = terminalWorkspace.closePane(sessionId);
  } else if (target === "tab") {
    const tab = terminalTabForPane(tabsState, sessionId);
    const paneIds = tab ? terminalWorkspace.closeTab(tab.id) : null;
    if (paneIds) {
      await Promise.all(paneIds.map((paneId) =>
        invokeBackend<void>("close_local_shell", { payload: { sessionId: paneId } }).catch(() => undefined)
      ));
      closed = true;
    }
  }
  if (closed) {
    invalidateAiNativeResources();
    await syncAiNativeResources();
  }
  return closed;
}
/** AI 输入或调整后台终端标签里的分屏前，先切换到该标签（与会话复用一致）。 */
function revealTerminalPaneForAi(sessionId: string) {
  const tabsState = { tabs: terminalWorkspace.tabs, activeTabId: terminalWorkspace.activeTabId };
  if (isBackgroundTerminalPane(tabsState, sessionId)) terminalWorkspace.selectPane(sessionId);
}
async function connectSftpForAi(profileId: string) {
  if (!isDesktopRuntime()) throw new Error("sftp.connect requires the desktop runtime");
  const profile = sshConnections.findProfile(profileId);
  if (!profile) throw new Error("SFTP profile not found");
  const sessionId = `sftp-${profileId}`;
  const home = await connectSftp(sessionId, profile, sshConnections.credentialsFor(profileId));
  sftpSessions.connected({ id: sessionId, name: `${profile.name.trim()} SFTP`, profileId });
  invalidateAiNativeResources();
  await syncAiNativeResources();
  return { sessionId, home };
}
async function disconnectSftpForAi(sessionId: string) {
  if (!isDesktopRuntime()) throw new Error("sftp.disconnect requires the desktop runtime");
  await disconnectSftp(sessionId);
  sftpSessions.disconnected(sessionId);
  invalidateAiNativeResources();
  await syncAiNativeResources();
}
function createSubagentResources(projectRoot: string) {
  const resources = createResourceRegistry();
  syncNativeAiResources(resources, {
    nativeRuntime: isDesktopRuntime(),
    projectRoot,
    terminalPanes: [],
    sshProfiles: [],
  }, ["sftp-workspace-transfer"]);
  return resources;
}

function subagentTaskPresentationStatus(state: SubagentTaskState) {
  if (state === "running") return "in_progress";
  if (state === "succeeded") return "completed";
  if (state === "failed" || state === "skipped") return "failed";
  if (state === "cancelled") return "cancelled";
  return "pending";
}

function applySubagentPlanSnapshot(
  conversationId: string,
  snapshot: SubagentPlanSnapshot,
) {
  const conversation = assistant.conversationById(conversationId);
  const event = [...(conversation?.agentEvents ?? [])].reverse().find(
    (item) => item.kind === "tool"
      && item.toolName === "agent.run_parallel"
      && item.status === "running",
  );
  if (!event) return;
  assistant.upsertAgentEventForConversation(conversationId, {
    ...event,
    detail: JSON.stringify({
      tasks: snapshot.tasks.map((task) => ({
        title: task.title,
        detail: task.error,
        status: subagentTaskPresentationStatus(task.state),
      })),
    }),
    status: snapshot.state === "succeeded" ? "completed" : snapshot.state,
  });
}

function subagentPlanProgress(snapshot: SubagentPlanSnapshot) {
  const succeeded = snapshot.tasks.filter((task) => task.state === "succeeded").length;
  const failed = snapshot.tasks.filter((task) => task.state === "failed").length;
  const running = snapshot.tasks.filter((task) => task.state === "running").length;
  const total = snapshot.tasks.length;
  const percent = total ? Math.round((succeeded / total) * 100) : 0;
  const summary = t("ai.taskProgress.subagents", { completed: succeeded, total, percent, running })
    + (failed ? t("ai.taskProgress.subagentsFailed", { failed }) : "");
  return { summary, steps: { completed: succeeded, total } };
}

/** A fresh reminder source; the leave notice is consumed by whichever loop reads it first. */
function planModeRemindersFor(conversationId: string) {
  return createPlanModeReminders(
    () => approvalModeForConversation(aiApprovalMode.value, assistant.conversationById(conversationId)?.external),
    { enter: PLAN_MODE_ENTER, leave: PLAN_MODE_LEAVE },
  );
}

/** The conversation's main-loop reminder source, kept across turns. */
function conversationPlanModeReminder(conversationId: string) {
  let reminder = planModeReminders.get(conversationId);
  if (!reminder) {
    reminder = planModeRemindersFor(conversationId);
    planModeReminders.set(conversationId, reminder);
  }
  return reminder;
}

async function executeSubagentPlanForAi(
  definition: SubagentPlanDefinition,
  execution: { conversationId?: string; signal: AbortSignal },
) {
  const conversationId = execution.conversationId?.trim() ?? "";
  if (!conversationId) throw new Error("Subagent plan requires a conversation context");
  const parentRunId = activeRunIds.get(conversationId);
  if (!parentRunId) throw new Error("Subagent plan requires an active parent run");
  const plan = normalizeSubagentPlan(definition);
  const workspace = workspaceContextForConversation(conversationId);
  const needsProject = plan.tasks.some((task) => task.mode === "write");
  await ensureConversationProjectAvailable(workspace, needsProject);
  const request = aiRequestPlan(conversationId);
  const subagentModels = buildSubagentModelAttemptOrder({
    inheritedModel: request.config.model,
    subagentModel: request.profile.subagentModel,
    configuredModels: profileModels(request.profile),
    fallbackEnabled: request.profile.failoverEnabled === true,
    fallbackModels: Array.isArray(request.profile.failoverModels)
      ? request.profile.failoverModels
      : [],
  });
  const parentController = agenticAborts.get(conversationId);
  const trackedRunIds = new Set<string>([parentRunId]);
  const taskConversations = new Map<string, string>();
  const taskRequests = new Map<string, ReturnType<typeof aiRequestPlan>>();
  const modelOptions = availableModels.value;
  const defaultChildModel = modelOptions.find((option) => option.profileId === request.profile.id
    && option.label === (subagentModels[0] ?? request.config.model))?.value;
  for (const task of plan.tasks) {
    if (!task.model) continue;
    if (!modelOptions.some((option) => option.value === task.model)) {
      throw new Error(t("ai.delegation.unknownModel", { model: task.model }));
    }
    const childRequest = aiRequestPlan(conversationId, task.model);
    const auth = validateAiProviderAuth({
      provider: childRequest.profile.provider,
      baseUrl: childRequest.config.baseUrl,
      apiKey: childRequest.config.apiKey,
    });
    if (!auth.valid) throw new Error(t(aiProviderAuthMessageKey(auth)));
    taskRequests.set(task.id, childRequest);
  }
  if (parentController) subagentParentAborts.set(parentRunId, parentController);
  try {
    return await executeSubagentRuntime(definition, {
      capabilities: aiCapabilityRegistry,
      config: {
        ...request.config,
        model: subagentModels[0] ?? request.config.model,
      },
      models: subagentModels,
      conversationId,
      parentRunId,
      workspace,
      signal: execution.signal,
      invoke: invokeBackend,
      createResources: createSubagentResources,
      executeTool: (call, resources, projectRoot, signal, allowedTools) =>
        executeAgentTool(
          conversationId,
          undefined,
          call,
          [],
          resources,
          projectRoot,
          signal,
          // Subagents run inside the parent turn; writes to the conversation's projects
          { allowedTools, forceProjectRoot: true },
        ),
      startRun: async (input) => {
        const run = await startAgentRun(input);
        trackedRunIds.add(run.runId);
        const child = taskConversations.get(input.taskId);
        const controller = child ? delegatedTaskAborts.get(child) : parentController;
        if (controller) subagentParentAborts.set(run.runId, controller);
        return run;
      },
      updateRun: async (input) => {
        const run = await updateAgentRun(input);
        if (["completed", "failed", "cancelled"].includes(run.progress.phase)) {
          subagentParentAborts.delete(run.runId);
        }
        return run;
      },
      applyRun: (run) => progress.apply(run),
      conversationForTask: (task) => taskConversations.get(task.id),
      taskSignal: (task) => {
        const id = taskConversations.get(task.id);
        return id ? delegatedTaskAborts.get(id)?.signal : undefined;
      },
      requestForTask: (task) => {
        const childRequest = taskRequests.get(task.id);
        return childRequest ? { config: childRequest.config, models: childRequest.attemptModels } : undefined;
      },
      onTaskTurn: (id, message) => {
        if (message.content.trim() && !message.toolCalls?.some((call) => call.name === "task_complete")) {
          assistant.addMessageToConversation(id, { role: "assistant", content: message.content });
          void scrollToBottomIfFollowing(id);
        }
      },
      onPlanUpdate: (snapshot) => {
        for (const task of snapshot.tasks) {
          let id = taskConversations.get(task.id);
          if (!id) {
            const child = assistant.createDelegatedConversation(conversationId, task.title, "parallel", task.model ?? defaultChildModel);
            if (!child) continue;
            id = child.id;
            taskConversations.set(task.id, id);
            executingParallelChildren.add(id);
            delegatedTaskAborts.set(id, new AbortController());
            assistant.addMessageToConversation(id, { role: "user", content: task.prompt });
          }
          const child = assistant.conversationById(id);
          if (!child || child.delegation?.result !== undefined) continue;
          if (task.state === "running") {
            if (child.status !== "in_progress") assistant.setConversationStatus(id, "in_progress");
          } else if (task.state !== "queued" && task.state !== "blocked") {
            const succeeded = task.state === "succeeded";
            const detail = task.state === "cancelled"
              ? t("ai.taskProgress.cancelled")
              : task.result?.summary ?? task.error ?? "";
            const last = child.messages[child.messages.length - 1];
            if (succeeded && last?.id && last.role === "assistant" && last.content.trim() === detail.trim()) {
              assistant.setMessagePresentation(id, last.id, "task-summary");
            } else if (detail) assistant.addMessageToConversation(id, {
              role: "assistant",
              content: detail,
              ...(succeeded ? { presentation: "task-summary" as const } : {}),
            });
            assistant.setConversationStatus(id, succeeded ? "completed" : task.state === "cancelled" ? "interrupted" : "failed");
            assistant.recordDelegatedConversationResult(id, detail);
            executingParallelChildren.delete(id);
            delegatedTaskAborts.delete(id);
          }
        }
        applySubagentPlanSnapshot(conversationId, snapshot);
        const planProgress = subagentPlanProgress(snapshot);
        void reportConversationRun(conversationId, "tool_running", planProgress.summary, undefined, planProgress.steps);
      },
      toolProgress: agentToolProgress,
      errorMessage: nativeErrorMessage,
      onUsage: (usage) => subagentUsageCollectors.get(conversationId)?.onUsage(usage),
      onModelAttempt: (model) => subagentUsageCollectors.get(conversationId)?.onModelAttempt(model),
      createSystemReminder: () => planModeRemindersFor(conversationId),
    });
  } finally {
    for (const id of taskConversations.values()) {
      executingParallelChildren.delete(id);
      delegatedTaskAborts.delete(id);
    }
    for (const runId of trackedRunIds) {
      if (runId !== parentRunId || subagentParentAborts.get(runId) === parentController) {
        subagentParentAborts.delete(runId);
      }
    }
  }
}

const unregisterNativeAiCapabilities = registerNativeAiCapabilities({
  nativeRuntime: isDesktopRuntime(),
  capabilities: aiCapabilityRegistry,
  registry: aiResourceRegistry,
  invoke: invokeBackend,
  listResources: nativeResourceList,
  runSubagentPlan: isDesktopRuntime() ? executeSubagentPlanForAi : undefined,
  createTerminalTab: isDesktopRuntime() ? createTerminalTabForAi : undefined,
  closeTerminalPane: isDesktopRuntime() ? closeTerminalPaneForAi : undefined,
  revealTerminalPane: revealTerminalPaneForAi,
  connectSftp: isDesktopRuntime() ? connectSftpForAi : undefined,
  disconnectSftp: isDesktopRuntime() ? disconnectSftpForAi : undefined,
  workspaceRoot,
  openWorkspaceView: isDesktopRuntime() ? openWorkspaceViewForAi : undefined,
  httpAction: isDesktopRuntime() ? async (input) => {
    await openWorkspaceViewForAi({ view: "http" });
    return requestAiWorkspaceAction("http", input);
  } : undefined,
  databaseAction: isDesktopRuntime() ? async (input) => {
    await openWorkspaceViewForAi({ view: "database" });
    return requestAiWorkspaceAction("database", input);
  } : undefined,
  redisAction: isDesktopRuntime() ? async (input) => {
    await openWorkspaceViewForAi({ view: "database" });
    return requestAiWorkspaceAction("database", {
      ...input,
      action: input.action === "format-json" ? "format-redis-json" : "open-redis-key",
    });
  } : undefined,
  databaseSnapshot: () => ({
    connections: databaseWorkspace.connections,
    sessions: databaseWorkspace.sessionIds,
    schemas: databaseWorkspace.schemas,
    activeConnectionId: databaseWorkspace.activeConnectionId,
  }),
  connectDatabase: (connectionId) => databaseWorkspace.connect(connectionId),
  disconnectDatabase: (connectionId) => databaseWorkspace.disconnect(connectionId),
  databaseDriverLabel: (connectionId) => {
    const connection = databaseWorkspace.connections.find((item) => item.id === connectionId);
    const descriptor = connection ? databaseWorkspace.connectionDescriptor(connection) : undefined;
    if (descriptor?.source !== "plugin" || !descriptor.pluginId) return undefined;
    const name = descriptor.pluginName || descriptor.pluginId;
    return `${descriptor.title} · ${name} (${descriptor.pluginId})`;
  },
  stageWorkspaceEdit: (edit) => pendingEdits.stage(edit),
  sshProfile: (profileId) => sshConnections.findProfile(profileId),
  sshCredentials: (profileId) => sshConnections.credentialsFor(profileId),
  settingsSummary: () => {
    const stored = loadStoredSettings();
    return {
      activeAiProfileId: stored.activeAiProfileId,
      acpAdapterId: stored.acpAdapterId,
      acpInjectRules: stored.acpInjectRules,
      enabledMcpServers: stored.mcpServers?.filter((server) => server.enabled).length ?? 0,
      enabledAiRules: stored.aiRules?.filter((rule) => rule.enabled !== false).length ?? 0,
    };
  },
  listSavedMcpServers: async () => {
    const statuses = await invokeBackend<McpServerStatus[]>("mcp_list_status", {}).catch(() => []);
    const connected = new Set(
      statuses.filter((status) => status.state === "connected").map((status) => status.id)
    );
    return enabledMcpServers().map((server) => ({
      id: server.id.trim(),
      connected: connected.has(server.id.trim()),
    }));
  },
  connectSavedMcpServer: isDesktopRuntime() ? connectSavedMcpServerForAi : undefined,
  updateSafeSettings: isDesktopRuntime() ? async (update) => {
    const stored = await hydrateSettingsSnapshot();
    const next = { ...(stored ?? {}) };
    if (update.theme !== undefined) {
      const theme = normalizeUiTheme(update.theme);
      next.uiTheme = theme;
      applyUiTheme(theme);
    }
    if (update.locale !== undefined) {
      next.locale = update.locale;
      setLocale(update.locale);
    }
    if (update.minimizeToTray !== undefined) {
      next.minimizeToTray = update.minimizeToTray;
      await setMinimizeToTray(update.minimizeToTray);
    }
    if (update.autostart !== undefined) {
      next.autostart = update.autostart;
      await setAutostart(update.autostart);
    }
    await persistSettingsSnapshot(next);
    return update;
  } : undefined,
  sidebarVisibility: loadSidebarVisibility,
  setSidebarItemVisible: async (id, visible) => {
    if (!sidebarItemIds.includes(id)) throw new Error(`Unknown sidebar item: ${id}`);
    const current = await loadSidebarVisibility();
    await saveSidebarVisibility({ ...current, [id]: visible });
    return { itemId: id, visible };
  },
});
function appendAgenticDelta(
  conversationId: string,
  delta: string,
  appendToCurrent: { value: boolean },
) {
  const conversation = assistant.conversationById(conversationId);
  const last = conversation?.messages[conversation.messages.length - 1];
  if (appendToCurrent.value && last?.role === "assistant") {
    assistant.updateLastAssistantForConversation(conversationId, delta);
  } else {
    assistant.addMessageToConversation(conversationId, { role: "assistant", content: delta });
    appendToCurrent.value = true;
  }
}
function ensureAssistantEventAnchor(conversationId: string, current?: string) {
  if (current) return current;
  return assistant.addMessageToConversation(conversationId, {
    role: "assistant",
    content: "",
    eventAnchor: true,
  });
}
function acpEventAnchor(conversationId: string, eventId: string) {
  let anchors = acpEventMessageIds.get(conversationId);
  if (!anchors) {
    anchors = new Map<string, string>();
    acpEventMessageIds.set(conversationId, anchors);
  }
  const existing = anchors.get(eventId);
  if (existing) return existing;
  const triggerMessageId = ensureAssistantEventAnchor(
    conversationId,
    acpCurrentMessageIds.get(conversationId),
  );
  if (triggerMessageId) {
    anchors.set(eventId, triggerMessageId);
    acpCurrentMessageIds.set(conversationId, triggerMessageId);
  }
  return triggerMessageId;
}
function workspaceRoot() {
  const conversation = assistant.activeConversation;
  if (conversation?.external && !conversation.external.workspace) return "";
  return conversation?.projectRoot?.trim() || homeDirectory.value;
}
function agentToolDisplayName(name: string) {
  if (name === MCP_LOAD_TOOL_NAME) return t("ai.tool.mcpLoadTools");
  return name === "workspace.search_code" ? t("ai.tool.workspaceSearchCode") : name;
}

function agentToolProgress(name: string) {
  return name === "workspace.search_code"
    ? t("ai.taskProgress.searchingCode")
    : t("ai.taskProgress.runningTool", { tool: agentToolDisplayName(name) });
}

function upsertToolEvent(
  conversationId: string,
  triggerMessageId: string | undefined,
  call: AiLoopToolCall,
  status: AiAssistantEvent["status"],
  detail?: string
) {
  assistant.upsertAgentEventForConversation(conversationId, {
    id: `loop-tool-${call.id}`,
    kind: "tool",
    title: agentToolDisplayName(call.name),
    detail: safeAiText(detail ?? summarizeToolApprovalArguments(toolArguments(call)) ?? call.name, 2_000),
    status,
    toolName: call.name,
    toolInput: summarizeToolApprovalArguments(toolArguments(call)),
    triggerMessageId,
  });
}
function conversationRunTitle(conversation: AiConversation | undefined, fallback: string) {
  return conversation?.title || fallback || t("ai.conversation.untitled");
}

async function beginConversationRun(
  conversationId: string,
  source: AgentRunSource,
  sessionId: string,
  title: string,
  context: ConversationWorkspaceContext,
) {
  try {
    const run = await startAgentRun({
      sessionId,
      workspaceId: context.workspaceId,
      workspaceScopeId: context.workspaceScopeId,
      source,
      title,
      phase: "thinking",
      summary: t("ai.taskProgress.starting"),
      navigation: { type: "ai-conversation", conversationId },
    });
    progress.apply(run);
    activeRunIds.set(conversationId, run.runId);
    activeRunPhases.set(conversationId, run.progress.phase);
    activeRunSummaries.set(conversationId, run.progress.summary);
    return run.runId;
  } catch (error) {
    ElMessage.error(t("ai.taskProgress.startFailed", { message: String(error) }));
    return undefined;
  }
}

function reportConversationRun(
  conversationId: string,
  phase: AgentRunPhase,
  summary?: string,
  errorCode?: string,
  steps?: { completed: number; total: number },
) {
  // The thread's status line reads this copy at once; the run registry updates only after an
  // async round trip and stays empty when registering the run failed.
  liveRunStatus.value = { ...liveRunStatus.value, [conversationId]: { phase, summary: summary ?? "" } };
  const runId = activeRunIds.get(conversationId);
  if (!runId) return Promise.resolve(undefined);
  const normalizedSummary = summary ?? "";
  const updateKey = JSON.stringify([phase, normalizedSummary, errorCode ?? ""]);
  if (
    activeRunPhases.get(conversationId) === phase
    && activeRunSummaries.get(conversationId) === normalizedSummary
    && errorCode === undefined
    && failedRunUpdateKeys.get(conversationId) !== updateKey
  ) {
    return Promise.resolve(undefined);
  }
  activeRunPhases.set(conversationId, phase);
  activeRunSummaries.set(conversationId, normalizedSummary);

  const sendUpdate = () => updateAgentRun({
    runId,
    phase,
    summary,
    errorCode,
    ...(steps ? { stepsCompleted: steps.completed, stepsTotal: steps.total } : {}),
  });
  return sendUpdate()
    .catch((error) => {
      if (phase === "completed" || phase === "failed" || phase === "cancelled") {
        return sendUpdate();
      }
      throw error;
    })
    .then((run) => {
      progress.apply(run);
      if (failedRunUpdateKeys.get(conversationId) === updateKey) {
        failedRunUpdateKeys.delete(conversationId);
      }
      return run;
    })
    .catch(() => {
      if (
        activeRunIds.get(conversationId) === runId
        && activeRunPhases.get(conversationId) === phase
        && activeRunSummaries.get(conversationId) === normalizedSummary
      ) {
        failedRunUpdateKeys.set(conversationId, updateKey);
      }
      return undefined;
    });
}

function releaseConversationRun(conversationId: string) {
  activeRunIds.delete(conversationId);
  activeRunPhases.delete(conversationId);
  activeRunSummaries.delete(conversationId);
  failedRunUpdateKeys.delete(conversationId);
}

// Every await can outlive a stop: the signal is checked after each one, and an
// approval card enqueued for a stopped run is rejected and removed.
async function authorizeTool(
  request: ToolAuthorizationRequest,
  triggerMessageId: string | undefined,
  summary: string,
  signal: AbortSignal,
): Promise<{ approved: boolean; feedback?: string }> {
  await toolAuthorizationHydration;
  throwIfAborted(signal);
  const relayedTurn = activePluginTurn(request.conversationId);
  // Remembered grants were given for the local user's own work, not for requests
  // relayed from other people; every relayed action is approved one by one.
  if (!relayedTurn && await toolAuthorizations.consume(request)) return { approved: true };
  if (relayedTurn) {
    relayedTurn.approvalSummary = summary;
    void pushPluginTurnProgress(relayedTurn);
    const conversation = assistant.conversationById(request.conversationId);
    notifyAi({
      level: "warning",
      titleKey: "notifications.ai.externalApproval",
      message: t("notifications.ai.externalApprovalMessage", {
        source: conversation?.external?.pluginName || conversation?.title || "",
        summary,
      }),
    }, request.conversationId);
  }
  await reportConversationRun(
    request.conversationId,
    "waiting_approval",
    t("ai.taskProgress.waitingApproval"),
  );
  throwIfAborted(signal);
  const approval = toolApprovalQueue.enqueue(
    request.conversationId,
    triggerMessageId,
    request,
    summary,
    signal,
  );
  if (relayedTurn?.chatApproval) {
    relayedTurn.approval = { id: approval.id, summary, risk: request.risk, tool: request.toolName };
    void pushPluginTurnProgress(relayedTurn);
  }
  let decision: Awaited<typeof approval.decision>;
  try {
    decision = await approval.decision;
  } finally {
    if (relayedTurn) {
      relayedTurn.approvalSummary = undefined;
      relayedTurn.approval = undefined;
      void pushPluginTurnProgress(relayedTurn);
    }
  }
  throwIfAborted(signal);
  await reportConversationRun(request.conversationId, "thinking", t("ai.taskProgress.thinking"));
  const { scope, feedback } = decision;
  if (!scope) return feedback ? { approved: false, feedback } : { approved: false };
  const authorization = await createToolAuthorization(scope, request);
  toolAuthorizations.add(authorization);
  if (scope === "global") {
    // The user approved this call; only remembering the grant across restarts failed.
    await saveGlobalToolAuthorizations(toolAuthorizations.list()).catch((error: unknown) => {
      ElMessage.warning(t("ai.agentic.approvalGrantSaveFailed", { message: nativeErrorMessage(error) }));
    });
  }
  return { approved: await toolAuthorizations.consume(request) };
}
function resolveToolApproval(id: string, decision: ToolApprovalDecision = {}) {
  const approval = toolApprovals.value.find((item) => item.id === id);
  // Feedback always goes through the queue's per-conversation rejection, which normalizes it
  // and declines that conversation's other pending calls with the same instruction.
  if (!decision.scope && decision.feedback && approval) {
    toolApprovalQueue.rejectConversation(approval.conversationId, decision.feedback);
    return;
  }
  toolApprovalQueue.resolve(id, decision.scope ? { scope: decision.scope } : {});
  if (approval && decision.scope === "session") {
    const key = sessionGrantBindsParameters(approval.request) ? "ai.agentic.sessionApprovedSameCall" : "ai.agentic.sessionApproved";
    ElMessage.success(t(key, { tool: approval.request.toolName }));
  }
}
async function selectApprovalMode(command: string | number | object) {
  const mode = normalizeAiApprovalMode(command);
  if (!AI_APPROVAL_MODES.includes(mode) || mode === aiApprovalMode.value) return;
  try {
    await saveAiApprovalMode(mode);
    ElMessage.success(t("ai.approvalMode.updated"));
  } catch (error) {
    ElMessage.error(t("ai.approvalMode.updateFailed", { message: String(error) }));
  }
}
function historicalToolModules(history: readonly AiLoopMessage[]) {
  const modules = new Set<string>();
  for (const message of history) {
    const source = message.toolResult?.source;
    if (source?.kind === "mcp") modules.add("mcp");
    if (source?.kind === "native") modules.add(source.toolName.split(".")[0] ?? "");
  }
  return [...modules].filter(Boolean);
}
function currentCapabilityModule() {
  const view = workbench.activeTab?.view;
  return view;
}
const ACTIVE_WORKSPACE_UI_TOOL_NAMES = new Set([
  "terminal.create_session",
]);
const PROJECT_SCOPED_RESOURCE_KINDS = new Set([
  "workspace-project",
  "sftp-workspace-transfer",
]);

function replaceConversationRuntimeResources(resources: AiResourceRegistry) {
  const projectResources = resources
    .list()
    .filter((resource) => PROJECT_SCOPED_RESOURCE_KINDS.has(resource.kind));
  const runtimeResources = aiResourceRegistry
    .list()
    .filter((resource) => !PROJECT_SCOPED_RESOURCE_KINDS.has(resource.kind));
  resources.replace("snapshot", [...projectResources, ...runtimeResources]);
}

async function ensureConversationProjectAvailable(
  context: ConversationWorkspaceContext,
  required = false,
) {
  if (!context.projectRoot) {
    if (required) throw new Error(t("ai.conversation.workspace.projectUnavailable"));
    return;
  }
  try {
    await invokeBackend("local_fs_file_info", {
      projectRoot: context.projectRoot,
      filePath: ".",
    });
  } catch {
    throw new Error(t("ai.conversation.workspace.projectUnavailable"));
  }
}

async function refreshConversationRuntimeResources(
  resources: AiResourceRegistry,
  projectRoot: string,
  force = false,
) {
  await syncAiNativeResources(force);
  replaceConversationRuntimeResources(resources);
  if (!projectRoot) {
    syncNativeAiResources(resources, { terminalPanes: [], sshProfiles: [] }, ["sftp-workspace-transfer"]);
    return;
  }
  await ensureConversationProjectAvailable({
    workspaceId: "",
    workspaceScopeId: "",
    projectRoot,
    resources,
  });
  syncNativeAiResources(resources, {
    nativeRuntime: isDesktopRuntime(),
    projectRoot,
    terminalPanes: [],
    sshProfiles: [],
  }, ["sftp-workspace-transfer"]);
}

function workspaceContextForConversation(conversationId: string) {
  const conversation = assistant.conversationById(conversationId);
  if (!conversation) throw new Error(t("ai.conversation.workspace.missingContext"));
  const workspace = workbench.workspaces.find((item) => item.id === conversation.workspaceId);
  if (!workspace) throw new Error(t("ai.conversation.workspace.unavailable"));

  const workspaceScopeId = conversation.workspaceScopeId?.trim() || assistant.activeWorkspaceScopeId;
  const explicitDirectory = conversation.external && !conversation.external.workspace
    ? ""
    : conversation.projectRoot?.trim() ?? "";
  const projectRoot = explicitDirectory || (conversation.external ? "" : homeDirectory.value);
  if (
    conversation.workspaceId !== workspace.id
    || conversation.workspaceScopeId !== workspaceScopeId
    || (conversation.projectRoot ?? "") !== explicitDirectory
  ) {
    assistant.setConversationWorkspace(conversationId, {
      workspaceId: workspace.id,
      scopeId: workspaceScopeId,
      projectRoot: explicitDirectory,
    });
  }

  const existing = conversationWorkspaceContexts.get(conversationId);
  if (
    existing
    && existing.workspaceId === workspace.id
    && existing.workspaceScopeId === workspaceScopeId
    && existing.projectRoot === projectRoot
  ) return existing;

  const context: ConversationWorkspaceContext = {
    workspaceId: workspace.id,
    workspaceScopeId,
    projectRoot,
    capabilityModule: currentCapabilityModule(),
    resources: createResourceRegistry(),
  };
  conversationWorkspaceContexts.set(conversationId, context);
  return context;
}
interface AgentToolExecutionPolicy {
  allowedTools?: ReadonlySet<string>;
  forceProjectRoot?: boolean;
}

async function executeAgentTool(
  conversationId: string,
  triggerMessageId: string | undefined,
  call: AiLoopToolCall,
  mcpTools: ActiveMcpTool[],
  resources: AiResourceRegistry,
  projectRoot: string,
  signal: AbortSignal,
  policy: AgentToolExecutionPolicy = {},
) {
  throwIfAborted(signal);
  if (policy.allowedTools && !policy.allowedTools.has(call.name)) {
    return { content: `Tool is not allowed for this subagent: ${call.name}`, status: "failed" as const };
  }
  const conversation = assistant.conversationById(conversationId);
  const owner = workbench.workspaces.find((item) => item.id === conversation?.workspaceId);
  if (call.name === "workspace.open_view") {
    if (!owner) return { content: "This conversation is not linked to a valid workspace, so project resources are unavailable.", status: "failed" as const };
    if (ACTIVE_WORKSPACE_UI_TOOL_NAMES.has(call.name) && owner.id !== workbench.activeWorkspaceId) {
      return { content: "This tool can only act on the active workspace. Ask the user to switch to the workspace linked to this conversation.", status: "failed" as const };
    }
  }
  const parsedArgs = toolArguments(call);
  const args = call.name === "terminal.run_command" && projectRoot
    ? {
        ...parsedArgs,
        ...(!parsedArgs.cwd || policy.forceProjectRoot ? { cwd: projectRoot } : {}),
      }
    : parsedArgs;
  const mcpTool = mcpTools.find((tool) => tool.name === call.name);
  if (mcpTool) {
    const authorizationRequest = {
      ...createMcpAuthorizationRequest({
        conversationId,
        toolName: call.name,
        serverId: mcpTool.serverId,
        args,
      }),
      toolCallId: call.id,
    };
    await approvalModeHydration;
    const mcpMode = approvalModeForConversation(aiApprovalMode.value, conversation?.external);
    if (planModeDenies(mcpMode, authorizationRequest.risk)) {
      return { content: t("ai.agentic.planModeBlocked"), status: "cancelled" as const };
    }
    const mcpDenial = riskCapDenial(authorizationRequest.risk, activePluginTurn(conversationId)?.maxRisk);
    if (mcpDenial) return { content: mcpDenial, status: "failed" as const };
    if (requiresAiToolApproval(mcpMode, authorizationRequest.risk)) {
      const authorization = await authorizeTool(
        authorizationRequest,
        triggerMessageId,
        t("ai.agentic.approveMcp", {
          server: mcpTool.serverName,
          tool: mcpTool.toolName,
          parameters: summarizeToolApprovalArguments(args) ?? t("ai.agentic.approvalParametersHidden"),
        }),
        signal,
      );
      if (!authorization.approved) {
        return declinedToolResult(authorization.feedback, {
          declined: t("ai.agentic.mcpDeclined"),
          declinedWithFeedback: (feedback) => t("ai.agentic.toolDeclinedWithFeedback", { feedback }),
        });
      }
    }
    throwIfAborted(signal);
    const result = safeMcpResult(await callMcpToolCancellable<McpCallResult>(
      invokeBackend,
      { serverId: mcpTool.serverId, toolName: mcpTool.toolName, arguments: args },
      signal,
    ));
    return {
      content: mcpResultContent(result),
      status: result.isError ? ("failed" as const) : ("completed" as const),
      source: {
        kind: "mcp" as const,
        toolName: mcpTool.toolName,
        serverId: mcpTool.serverId,
        serverName: mcpTool.serverName,
      },
    };
  }

  const capability = aiCapabilityRegistry.get(call.name);
  const turnPlugins = activePluginTurn(conversationId)?.allowedPlugins;
  const pluginDenial = externalPluginDenial(call.name, args, turnPlugins);
  if (pluginDenial) return { content: pluginDenial, status: "failed" as const };
  if (capability) {
    // 同一轮内前一个工具可能刚创建浏览器、数据库、终端或 SFTP 资源。
    // 旧快照没有目标时先同步一次，再判定为真正缺失。
    if (
      capability.target !== "none"
      && (!aiNativeResourcesSynced || resources.list(call.name).length === 0)
    ) {
      await refreshConversationRuntimeResources(resources, projectRoot, true);
    }
    const resource = aiCapabilityRegistry.resolveResourceFor(call.name, args, resources);
    const normalizedArgs = { ...args, resourceId: resource.id };
    let authorized = capability.risk === "read";
    if (capability.risk !== "read") {
      await approvalModeHydration;
      const callRisk = capability.resolveRisk
        ? await capability.resolveRisk({ input: normalizedArgs, resource })
        : capability.risk;
      const callMode = approvalModeForConversation(aiApprovalMode.value, conversation?.external);
      if (planModeDenies(callMode, callRisk)) {
        return { content: t("ai.agentic.planModeBlocked"), status: "cancelled" as const };
      }
      const capDenial = riskCapDenial(callRisk, activePluginTurn(conversationId)?.maxRisk);
      if (capDenial) return { content: capDenial, status: "failed" as const };
      if (requiresAiToolApproval(callMode, callRisk)) {
        const summary = capability.approvalPreview
          ? await capability.approvalPreview({ input: normalizedArgs, resource })
          : call.name;
        const authorization = await authorizeTool(
          {
            ...createNativeAuthorizationRequest({
              conversationId,
              toolName: call.name,
              module: capability.module,
              args: normalizedArgs,
              risk: callRisk,
              sessionGrant: capability.sessionGrant,
            }),
            toolCallId: call.id,
          },
          triggerMessageId,
          summary,
          signal,
        );
        if (!authorization.approved) {
          return declinedToolResult(authorization.feedback, {
            declined: t("ai.agentic.toolDeclined"),
            declinedWithFeedback: (feedback) => t("ai.agentic.toolDeclinedWithFeedback", { feedback }),
          });
        }
      }
      throwIfAborted(signal);
      authorized = true;
    }
    let result: AiCapabilityExecutionResult;
    try {
      result = await aiCapabilityRegistry.execute(call.name, normalizedArgs, {
        resources,
        operations: aiOperationManager,
        conversationId,
        signal,
        ...(authorized && capability.risk !== "read" ? {
          authorization: {
            conversationId,
            consume: async (request) => request.toolName === call.name
              && request.resourceId === resource.id
              && request.parameters === normalizedArgs
              && request.risk === capability.risk,
          },
        } : {}),
      });
    } finally {
      if (capability.risk !== "read") invalidateAiNativeResources();
    }
    if (call.name === "plugin.list" && turnPlugins) {
      result = { ...result, result: filterAllowedPluginList(result.result, turnPlugins) };
    }
    if (capability.risk !== "read") {
      // 写入/连接成功后刷新当前任务快照，后续工具可立即消费新资源。
      try {
        await refreshConversationRuntimeResources(resources, projectRoot, true);
      } catch {
        // 操作已经成功；保留失效状态，下一次目标解析会再次尝试同步。
      }
    }
    const capture = VISUAL_CAPTURE_TOOL_NAMES.has(call.name)
      ? asRecord(asRecord(result).result)
      : undefined;
    const captureData = typeof capture?.data === "string" ? capture.data : "";
    const captureMimeType = typeof capture?.mimeType === "string" ? capture.mimeType : "image/png";
    const attachments: AiAttachment[] | undefined = captureData
      ? [{
          id: globalThis.crypto?.randomUUID?.() ?? `capture-${Date.now()}`,
          name: `capture-${Date.now()}.png`,
          mimeType: captureMimeType,
          size: Math.floor(captureData.length * 0.75),
          kind: "image",
          data: captureData,
        }]
      : undefined;
    const safeResult = captureData
      ? {
          ...asRecord(result),
          result: {
            ...capture,
            data: undefined,
            visualObservation: "The updated browser screenshot is attached to this tool result.",
          },
        }
      : result;
    return {
      content: safeAiText(safeResult),
      status: "completed" as const,
      ...(attachments ? { attachments } : {}),
      source: {
        kind: "native" as const,
        toolName: call.name,
        resourceId: resource.id,
      },
    };
  }

  return { content: `Unknown tool: ${call.name}`, status: "failed" as const };
}
function attachmentError(error: unknown) {
  const message = error instanceof Error ? error.message : String(error);
  if (message === "EMPTY_IMAGE_ATTACHMENT") return t("ai.attachment.emptyImage");
  if (message === "IMAGE_ATTACHMENT_TOO_LARGE") return t("ai.attachment.imageTooLarge");
  if (message === "TEXT_ATTACHMENT_TOO_LARGE") return t("ai.attachment.textTooLarge");
  if (message === "UNSUPPORTED_ATTACHMENT") return t("ai.attachment.unsupported");
  return message;
}
// 通过 setter 写回 store，确保草稿附件被标记为待保存。
function appendDraftAttachments(added: AiAttachment[]) {
  if (added.length) attachments.value = [...attachments.value, ...added];
}
function reportAttachmentErrors(errors: Array<{ name: string; code: string }>) {
  if (!errors.length) return;
  ElMessage.warning(
    errors.map((error) => `${error.name}: ${attachmentError(error.code)}`).join(t("ai.attachment.errorSeparator")),
  );
}
async function addAttachmentFiles(files: FileList | File[]) {
  if (!files.length || addingAttachments.value) return;
  addingAttachments.value = true;
  try {
    const result = await readAiAttachments(files);
    appendDraftAttachments(result.attachments);
    reportAttachmentErrors(result.errors);
  } finally {
    addingAttachments.value = false;
  }
}
async function addDroppedFiles(paths: string[]) {
  if (!paths.length || addingAttachments.value) return;
  addingAttachments.value = true;
  try {
    const result = await readAiAttachmentsFromPaths(paths, invokeBackend);
    appendDraftAttachments(result.attachments);
    reportAttachmentErrors(result.errors);
  } finally {
    addingAttachments.value = false;
  }
  drawerPanel.value?.querySelector<HTMLTextAreaElement>(".ai-assistant-composer textarea")?.focus();
}
async function handleAttachmentPaste(event: ClipboardEvent) {
  if (!event.clipboardData) return;
  const result = await readAiAttachmentsFromClipboard(event.clipboardData);
  if (!result.attachments.length && !result.errors.length) return;
  event.preventDefault();
  appendDraftAttachments(result.attachments);
  reportAttachmentErrors(result.errors);
}
// Sent turns of an idle, non-ACP conversation can be rewound: the user message
// and everything after it are removed and the turn is sent again. ACP agents keep
// their own history, so there the text is only copied back into the composer.
const canRewindActiveConversation = computed(() =>
  assistant.mode !== "agent" && !assistant.activeConversation?.running && !assistant.awaitingResponse,
);
const editingMessageId = ref<string>();
// The step-limit message offers "continue" while the conversation is idle; it sends a
// normal follow-up turn so the loop resumes with the full history.
const canContinueActiveConversation = computed(() =>
  assistant.mode !== "agent" && !assistant.activeConversation?.running && !assistant.awaitingResponse,
);
function continueAfterLoopLimit() {
  if (!canContinueActiveConversation.value) return;
  const budget = pluginContextBudgetState.value;
  if (budget?.exceeded) {
    ElMessage.warning(t("pluginContrib.aiContextProviders.sendBlocked", { excess: budget.exceeded }));
    return;
  }
  const conversationId = assistant.activeConversationId;
  void sendAgentic({
    conversationId,
    content: t("ai.agentic.continuePrompt"),
    attachments: [],
  });
}

function editSentMessage(message: AiAssistantMessage) {
  if (canRewindActiveConversation.value && message.id) {
    editingQueueId.value = undefined;
    editingMessageId.value = message.id;
  }
  input.value = message.content;
  attachments.value = sanitizeAiAttachments(message.attachments ?? []);
}
function cancelMessageEdit() {
  editingMessageId.value = undefined;
  input.value = "";
  attachments.value = [];
}
async function resendFromMessage(
  messageId: string,
  replacement?: { content: string; attachments: AiAttachment[] },
  onCancel?: () => void,
) {
  // The conversation can move on before a resend runs; an edit then goes back into the composer.
  if (!canRewindActiveConversation.value) {
    onCancel?.();
    if (onCancel) ElMessage.warning(t("ai.rewindRestore.conversationChanged"));
    return;
  }
  const conversationId = assistant.activeConversationId;
  const removed = assistant.rewindConversationTo(conversationId, messageId);
  if (!removed) {
    onCancel?.();
    ElMessage.warning(t("ai.rewindRestore.conversationChanged"));
    return;
  }
  await sendAgentic({
    conversationId,
    content: replacement?.content ?? removed.content,
    attachments: replacement?.attachments ?? sanitizeAiAttachments(removed.attachments ?? []),
  });
}
function resendEditedMessage() {
  const messageId = editingMessageId.value;
  if (!messageId) return;
  const conversationId = assistant.activeConversationId;
  const content = input.value.trim();
  const selected = takeAttachments();
  if (!content && !selected.length) return;
  editingMessageId.value = undefined;
  input.value = "";
  void resendFromMessage(messageId, { content, attachments: selected }, () => {
    if (assistant.activeConversationId !== conversationId || input.value) return;
    editingMessageId.value = messageId;
    input.value = content;
    attachments.value = selected;
  });
}
/** Regenerate a reply or retry a failed turn: resend the user message behind it. */
function resendTurnOf(message: AiAssistantMessage) {
  const userMessage = message.id ? precedingUserMessage(assistant.messages, message.id) : undefined;
  if (userMessage?.id) void resendFromMessage(userMessage.id);
}
watch(() => assistant.activeConversationId, () => {
  // 切换后 input 已是新会话的草稿，只退出编辑态，不清空它。
  editingMessageId.value = undefined;
});
function removeAttachment(id: string) {
  attachments.value = attachments.value.filter((attachment) => attachment.id !== id);
}
function takeAttachments() {
  const selected = sanitizeAiAttachments(attachments.value);
  attachments.value = [];
  return selected;
}
/**
 * Clears only what a send captured: text typed and files attached while the send
 * was awaiting setup belong to the next message and must survive.
 */
function consumeComposerInput(text: string, sent: AiAttachment[]) {
  if (input.value.trim() === text) input.value = "";
  if (!sent.length) return;
  const sentIds = new Set(sent.map((attachment) => attachment.id));
  attachments.value = attachments.value.filter((attachment) => !sentIds.has(attachment.id));
}
function waitForNextPaint() {
  return new Promise<void>((resolve) => {
    if (typeof window === "undefined") {
      resolve();
      return;
    }
    window.requestAnimationFrame(() => resolve());
  });
}

function userMessageElements(host: HTMLElement) {
  const itemIds = new Set(messageNavigatorItems.value.map((item) => item.id));
  return Array.from(host.querySelectorAll<HTMLElement>("[data-ai-message-id]"))
    .filter((element) => element.dataset.aiMessageId && itemIds.has(element.dataset.aiMessageId));
}

function syncMessagesAtBottom(host = scrollHost.value) {
  if (!host) {
    messagesAtBottom.value = true;
    return;
  }
  const distanceFromBottom = host.scrollHeight - host.scrollTop - host.clientHeight;
  messagesAtBottom.value = distanceFromBottom <= MESSAGE_BOTTOM_THRESHOLD;
}

function syncMessageNavigatorPosition(host = scrollHost.value) {
  const first = messageNavigatorItems.value[0];
  if (!host || !first) {
    activeMessageNavigatorId.value = "";
    return;
  }

  const threshold = host.getBoundingClientRect().top + 32;
  let activeId = first.id;
  for (const element of userMessageElements(host)) {
    if (element.getBoundingClientRect().top > threshold) break;
    activeId = element.dataset.aiMessageId ?? activeId;
  }
  activeMessageNavigatorId.value = activeId;
}

function scheduleMessageNavigatorSync() {
  if (typeof window === "undefined" || messageNavigatorFrame) return;
  messageNavigatorFrame = window.requestAnimationFrame(() => {
    messageNavigatorFrame = 0;
    syncMessageNavigatorPosition();
  });
}

// A switched-to conversation renders only its newest messages; the rest of the window is
// rendered once the reader scrolls up within this distance of the top.
const MESSAGE_FILL_TOP_DISTANCE = 1200;

function onMessagesScroll() {
  syncMessagesAtBottom();
  scheduleMessageNavigatorSync();
  const host = scrollHost.value;
  if (host && host.scrollTop < MESSAGE_FILL_TOP_DISTANCE && !messagesAtBottom.value) {
    messageThread.value?.fillRenderWindow();
  }
}

async function scrollToConversationMessage(messageId: string) {
  messageThread.value?.revealMessage(messageId);
  await nextTick();
  const host = scrollHost.value;
  if (!host) return;
  const target = userMessageElements(host).find((element) => element.dataset.aiMessageId === messageId);
  if (!target) return;

  const hostRect = host.getBoundingClientRect();
  const targetTop = host.scrollTop + target.getBoundingClientRect().top - hostRect.top - 8;
  const reducedMotion = window.matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? false;
  activeMessageNavigatorId.value = messageId;
  host.scrollTo({ top: Math.max(0, targetTop), behavior: reducedMotion ? "auto" : "smooth" });
}

async function preserveScrollAfterLoadingEarlier() {
  const host = scrollHost.value;
  if (!host) return;
  const previousHeight = host.scrollHeight;
  const previousTop = host.scrollTop;
  await nextTick();
  host.scrollTop = previousTop + host.scrollHeight - previousHeight;
  syncMessagesAtBottom(host);
  syncMessageNavigatorPosition(host);
}

async function scrollToBottom(
  conversationId = assistant.activeConversationId,
  behavior: "auto" | "smooth" = "auto",
) {
  await nextTick();
  if ((!assistant.open && !tabSurfaceActive.value) || conversationId !== assistant.activeConversationId) return;

  // The drawer enters with a transition, so its scroll height may still be stale
  // immediately after Vue has rendered the newly selected conversation.
  await waitForNextPaint();
  const host = scrollHost.value;
  if (!host || conversationId !== assistant.activeConversationId) return;
  host.scrollTo({ top: host.scrollHeight, behavior });

  // A second smooth scroll on the following frame can interrupt WebKit's
  // animation in very long conversations and leave the viewport short of the
  // actual bottom. The scroll event will update the button state as the first
  // animation progresses, so only instant alignment needs the extra pass.
  if (behavior === "smooth") return;

  // Tool cards and markdown can expand during the first layout pass. Align once
  // more on the following frame so opening an existing conversation lands at its end.
  await waitForNextPaint();
  if (scrollHost.value === host && conversationId === assistant.activeConversationId) {
    host.scrollTo({ top: host.scrollHeight, behavior });
    if (behavior === "auto") syncMessagesAtBottom(host);
    syncMessageNavigatorPosition(host);
  }
}

// While the reader stays at the bottom, the size observer keeps the view pinned as streamed text,
// tool cards and late markdown layout grow the thread, and as the drawer's viewport settles.
const stickToBottom = useStickToBottom({
  host: scrollHost,
  following: () => messagesAtBottom.value && (assistant.open || tabSurfaceActive.value),
  onAligned: (host) => {
    syncMessagesAtBottom(host);
    scheduleMessageNavigatorSync();
  },
});

// Run steps call this after changing the active conversation's content; awaiting it means the
// change has rendered and, if the reader was following, the view is at the new end.
async function scrollToBottomIfFollowing(conversationId: string): Promise<void> {
  if (conversationId !== assistant.activeConversationId || !messagesAtBottom.value) return;
  await nextTick();
  stickToBottom.align();
}

function returnToMessageBottom() {
  const reducedMotion = window.matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? false;
  void scrollToBottom(assistant.activeConversationId, reducedMotion ? "auto" : "smooth");
}

watch(
  [() => assistant.activeConversationId, messageNavigatorIds],
  async () => {
    await nextTick();
    scheduleMessageNavigatorSync();
  },
  { flush: "post", immediate: true },
);

// Reading position per conversation for this app run: the user message at the top of the view and
// its offset. Pixel offsets do not survive the thread remounting with a fresh render window.
const conversationScrollPositions = new Map<string, { messageId: string; offset: number }>();

function rememberConversationScroll(conversationId: string) {
  const host = scrollHost.value;
  if (!host || !conversationId) return;
  conversationScrollPositions.delete(conversationId);
  if (messagesAtBottom.value) return;
  const top = host.getBoundingClientRect().top;
  // The navigator's item list already follows the next conversation, so read the rendered thread.
  const anchors = Array.from(host.querySelectorAll<HTMLElement>("[data-ai-message-id]"));
  const above = anchors.filter((element) => element.getBoundingClientRect().top <= top + 32);
  const anchor = above[above.length - 1] ?? anchors[0];
  const messageId = anchor?.dataset.aiMessageId;
  if (!anchor || !messageId) return;
  conversationScrollPositions.set(conversationId, { messageId, offset: anchor.getBoundingClientRect().top - top });
}

async function restoreConversationScroll(conversationId = assistant.activeConversationId) {
  const saved = conversationScrollPositions.get(conversationId);
  // A running conversation is followed from its end, as is one last left at the bottom.
  if (!saved || assistant.conversationById(conversationId)?.running) return scrollToBottom(conversationId);
  await nextTick();
  if (conversationId !== assistant.activeConversationId) return;
  messageThread.value?.revealMessage(saved.messageId);
  await nextTick();
  await waitForNextPaint();
  const host = scrollHost.value;
  if (!host || conversationId !== assistant.activeConversationId) return;
  const anchor = Array.from(host.querySelectorAll<HTMLElement>("[data-ai-message-id]"))
    .find((element) => element.dataset.aiMessageId === saved.messageId);
  if (!anchor) return scrollToBottom(conversationId);
  host.scrollTop += anchor.getBoundingClientRect().top - host.getBoundingClientRect().top - saved.offset;
  syncMessagesAtBottom(host);
  syncMessageNavigatorPosition(host);
}

watch(
  () => assistant.activeConversationId,
  (_next, previous) => {
    if (previous) rememberConversationScroll(previous);
  },
  { flush: "sync" },
);

watch(
  [() => assistant.open, () => assistant.activeConversationId, tabSurfaceActive],
  ([open, , tabActive]) => {
    if (open || tabActive) void restoreConversationScroll();
  },
  { flush: "post", immediate: true },
);
watch(
  () => assistant.pendingContextAttachments.length,
  (count) => {
    if (count) appendDraftAttachments(assistant.takeContextAttachments());
  },
  { immediate: true },
);
interface QueuedSendRequest {
  conversationId: string;
  content: string;
  attachments: AiAttachment[];
  createdAt?: number;
  messageId?: string;
  pluginTurn?: AiQueuedMessage["pluginTurn"];
}

// Queued and remote sends can target a conversation whose details are not loaded;
// sending without them would drop its history.
async function ensureSendableConversation(conversationId: string) {
  if (await assistant.ensureConversationDetails(conversationId)) return true;
  if (!assistant.conversationById(conversationId)) return true;
  ElMessage.error(t("ai.conversationLoadFailed"));
  return false;
}

async function sendAgentic(request?: QueuedSendRequest) {
  const text = (request?.content ?? input.value).trim();
  const selectedAttachments = sanitizeAiAttachments(request?.attachments ?? attachments.value);
  if (!text && !selectedAttachments.length) return;
  const conversationId = request?.conversationId ?? assistant.activeConversationId;
  if (!(await ensureSendableConversation(conversationId))) return;
  const targetConversation = assistant.conversationById(conversationId);
  if (parallelDelegationPending(targetConversation)) return;
  // 普通输入在运行中应入队；已出队请求则由发送锁串行，不受可能滞后的运行态投影阻塞。
  if (targetConversation?.running && !request) {
    assistant.enqueueMessage(conversationId, {
      content: text,
      attachments: selectedAttachments,
      createdAt: Date.now(),
    });
    consumeComposerInput(text, selectedAttachments);
    ElMessage.info(t("ai.queue.enqueued"));
    return;
  }
  if (!agenticSendGate.claim(conversationId)) {
    if (!request) ElMessage.info(t("ai.send.busy"));
    return;
  }
  let runContext: ConversationWorkspaceContext;
  try {
    if (!targetConversation?.projectRoot && !targetConversation?.external) await homeDirectoryReady;
    runContext = workspaceContextForConversation(conversationId);
    await refreshConversationRuntimeResources(runContext.resources, runContext.projectRoot);
  } catch (error) {
    agenticSendGate.release(conversationId);
    ElMessage.warning(nativeErrorMessage(error));
    return;
  }
  // 新任务开始前收口任何历史遗留的运行事件，避免旧分析计时跨到下一条消息。
  assistant.settleRunningAgentEvents(conversationId, "cancelled");
  if (!canStreamAi()) {
    agenticSendGate.release(conversationId);
    return ElMessage.warning(t("common.desktopRequired"));
  }
  try {
    await hydrateSettingsSnapshot();
  } catch {
    agenticSendGate.release(conversationId);
    ElMessage.error(t("ai.credentialsLoadFailed"));
    return;
  }
  const { profile, config: requestConfig, attemptModels } = aiRequestPlan(conversationId);
  const auth = validateAiProviderAuth({
    provider: profile.provider,
    baseUrl: requestConfig.baseUrl,
    apiKey: requestConfig.apiKey,
  });
  if (!auth.valid) {
    agenticSendGate.release(conversationId);
    ElMessage.warning(t(aiProviderAuthMessageKey(auth)));
    return;
  }
  notifyNoToolModel(requestConfig);
  const triggerMessageId = assistant.addMessageToConversation(conversationId, {
    role: "user",
    content: text,
    attachments: selectedAttachments,
    createdAt: request?.createdAt,
    id: request?.messageId,
  });
  if (!triggerMessageId) {
    agenticSendGate.release(conversationId);
    return;
  }
  if (request?.pluginTurn) beginPluginTurn(request.pluginTurn.turnId, conversationId, triggerMessageId);
  if (!request) consumeComposerInput(text, selectedAttachments);
  const runId = await beginConversationRun(
    conversationId,
    "agentic",
    conversationId,
    conversationRunTitle(targetConversation, text),
    runContext,
  );
  if (!runId) {
    agenticSendGate.release(conversationId);
    assistant.setConversationStatus(conversationId, "failed");
    return;
  }
  assistant.anchorUnassignedAgentEvents(conversationId, triggerMessageId);
  assistant.setConversationStatus(conversationId, "in_progress");
  const abortController = new AbortController();
  agenticAborts.set(conversationId, abortController);
  // Stall recovery drops this controller and may start the next queued run; a
  // run that is no longer current must not touch the conversation or its run record.
  const superseded = () => agenticAborts.get(conversationId) !== abortController;
  const runProjectRoot = runContext.projectRoot;
  const runCapabilityModule = runContext.capabilityModule;
  const runResources = runContext.resources;
  const appendToCurrent = { value: false };
  let eventTriggerMessageId: string | undefined;
  let completedTurnAssistantMessageId: string | undefined;
  let replyTokenUsage: AiTokenUsage | undefined;
  let attemptedModelRequests = 0;
  const attemptedModels: string[] = [];
  subagentUsageCollectors.set(conversationId, {
    onUsage: (usage) => {
      replyTokenUsage = mergeAiTokenUsage(replyTokenUsage, usage);
    },
    onModelAttempt: (model) => {
      attemptedModelRequests += 1;
      attemptedModels.push(model);
    },
  });

  const applyReasoningDelta = (delta: string) => {
    if (superseded()) return;
    void reportConversationRun(conversationId, "thinking", t("ai.taskProgress.thinking"));
    eventTriggerMessageId = ensureAssistantEventAnchor(conversationId, eventTriggerMessageId);
    const conversation = assistant.conversationById(conversationId);
    const event = conversation?.agentEvents.find(
      (item) => item.id === "loop-reasoning" && item.triggerMessageId === eventTriggerMessageId,
    );
    // A capped detail no longer changes; skip the upsert that would regroup every event.
    if (event?.status === "running" && (event.detail?.length ?? 0) > AI_EVENT_DETAIL_LIMIT) return;
    assistant.upsertAgentEventForConversation(conversationId, {
      id: "loop-reasoning",
      kind: "thought",
      title: t("ai.thought"),
      detail: `${event?.detail ?? ""}${delta}`,
      status: "running",
      triggerMessageId: eventTriggerMessageId,
    });
    void scrollToBottomIfFollowing(conversationId);
  };
  const streamTextBatcher = createFrameTextBatcher((kind, delta) => {
    // 被接管的旧任务仍可能有缓冲片段，不能写进已开始下一条消息的会话。
    if (superseded()) return;
    if (kind === "assistant") {
      void reportConversationRun(conversationId, "streaming", t("ai.taskProgress.streaming"));
      assistant.settleRunningAgentEvents(conversationId, "completed", "thought");
      appendAgenticDelta(conversationId, delta, appendToCurrent);
      const conversation = assistant.conversationById(conversationId);
      eventTriggerMessageId = conversation?.messages[conversation.messages.length - 1]?.id;
      void scrollToBottomIfFollowing(conversationId);
      return;
    }
    applyReasoningDelta(delta);
  });
  const toolTriggerMessageIds = new Map<string, string>();
  // Tool calls the model is still generating, keyed by their position in the response. They are
  // placeholders: the final tool_call events replace them, and a request that ends without them
  // (text reply, truncation, error, fallback to another model, stop) clears them.
  const preparingToolEvents = new Map<number, Pick<AiAssistantEvent, "id" | "triggerMessageId">>();
  const clearPreparingTools = () => {
    for (const event of preparingToolEvents.values()) assistant.removeAgentEventForConversation(conversationId, event);
    preparingToolEvents.clear();
  };
  // Browser-local mode sends no tools at all (agentTools is empty, MCP is skipped), so the prompt must not
  // promise workspace/MCP tools or task_complete.
  const baseSystemPrompt = browserLocalAi
    ? "You are an assistant in a plain chat without tools: you cannot read or change the user's files or run commands. Answer directly from the conversation and any context provided. When the user asks for an action you cannot perform, say so and explain how they could do it themselves."
    : runProjectRoot
    ? "You are a coding assistant. Answer greetings, questions, explanations, and other conversational requests directly without calling tools. Use workspace or MCP tools only when the request requires an action or verified project information. After tool-driven work is completed and verified, call task_complete exactly once. Its summary is the final user-facing delivery report: never reduce project analysis, code review, architecture review, investigation, or research to a few sentences. State the conclusion, inspected scope and evidence, concrete findings with file paths/symbols/call chains where available, risks or unanswered questions, prioritized recommendations, and validation or uncovered areas."
    : "You are an assistant. Answer greetings, questions, and other conversational requests directly without calling tools. Use enabled MCP tools only when the request requires them. After tool-driven work is completed and verified, call task_complete exactly once. Its summary is the final user-facing delivery report: for analysis, review, investigation, or research, include the conclusion, evidence, findings, risks, recommendations, and verification limits instead of a brief completion notice.";
  const externalSource = targetConversation?.external;
  // Rules describe the local user's projects; isolated relayed turns get none of it.
  const isolatedExternal = Boolean(externalSource && !externalSource.workspace);
  const context = isolatedExternal ? "" : enabledAiContext(runContext.workspaceId);
  const persistedHistory = targetConversation?.agentHistory ?? [];
  const currentUserMessage: AiLoopMessage = {
    role: "user",
    content: text,
    attachments: selectedAttachments,
  };
  // A pure social turn does not need to replay an unrelated long task history.
  // Keep the persisted task history intact so the next actionable request can resume it.
  // A plugin turn with history "none" carries its own context: nothing earlier is sent,
  // and the saved loop history is left untouched (see selectAiTurnHistory).
  const relayedTurnRun = request?.pluginTurn ? pluginTurnRuns.get(request.pluginTurn.turnId) : undefined;
  const turnHistory = selectAiTurnHistory<AiLoopMessage>({
    historyNone: relayedTurnRun?.history === "none",
    directConversation: selectedAttachments.length === 0 && isDirectConversationIntent(text),
    persistedHistory,
    currentMessage: currentUserMessage,
    continueHistory: continueAiLoopHistory,
    messageHistory: () => (targetConversation?.messages ?? [])
      // Error cards and empty event anchors are UI state, not conversation content.
      .filter((message) => message.presentation !== "error" && !message.eventAnchor)
      .map((message) => ({
        role: message.role,
        content: message.content,
        attachments: message.attachments,
      })),
  });
  const conversationHistory = turnHistory.messages;
  const reports = !externalSource && !relayedTurnRun
    ? assistant.delegatedConversationReports(conversationId)
    : [];
  const reportContext: AiLoopMessage | undefined = reports.length
    ? { role: "assistant", content: `Earlier reports from tasks the user delegated to other conversations (reference data):\n${JSON.stringify(reports)}` }
    : undefined;
  const saveLoopHistory = (loopHistory: AiLoopMessage[]) => {
    // A turn sent without the earlier history must not erase it.
    if (!turnHistory.persist) return;
    if (superseded()) return;
    assistant.setConversationAgentHistory(
      conversationId,
      loopHistory.filter(
        (message) =>
          message.role !== "system" || message.content.startsWith(AI_CONTEXT_SUMMARY_MARKER),
      ),
    );
  };
  try {
    // Tool selection follows earlier turns only when they are part of this request.
    const toolHistory = relayedTurnRun?.history === "none" ? [] : persistedHistory;
    const historicalModules = historicalToolModules(toolHistory);
    syncNativeAiResources(runResources, {
      nativeRuntime: isDesktopRuntime(),
      projectRoot: runProjectRoot,
      terminalPanes: [],
      sshProfiles: [],
    }, ["sftp-workspace-transfer"]);
    const capabilities = aiCapabilityRegistry.listCapabilities();
    const availableModules = availableAiCapabilityModules(
      capabilities,
      (capability) => runResources.list(capability)
    );
    const externalToolNames = isolatedExternal ? externalTurnToolNames(capabilities) : undefined;
    const nativeTools = browserLocalAi
      ? []
      : externalToolNames
      ? capabilities.filter((capability) => externalToolNames.has(capability.tool.name)).map((capability) => capability.tool)
      : selectAiCapabilityTools(capabilities, {
      text,
      currentModule: runCapabilityModule,
      availableModules,
      historicalModules,
    }).filter((tool) =>
      runContext.workspaceId === workbench.activeWorkspaceId
      || !ACTIVE_WORKSPACE_UI_TOOL_NAMES.has(tool.name),
    );
    if (!isolatedExternal && !browserLocalAi) await pluginMcpServers.hydrate();
    // Plugin servers that need a project are offered only with one; the rest everywhere.
    const mcpServerIds = isolatedExternal || browserLocalAi
      ? []
      : [
          ...enabledMcpServers(runProjectRoot).map((server) => server.id.trim()),
          ...pluginMcpServers.enabledServers
            .filter((server) => mcpServerAvailable(server, runProjectRoot))
            .map((server) => server.sessionId),
        ];
    const mcpPlan = planMcpToolLoading({
      text,
      serverIds: mcpServerIds,
      historyServerIds: mcpServersFromHistory(toolHistory),
    });
    // Live list: the loader tool appends a connector's tools during the turn.
    const mcpTools: ActiveMcpTool[] = mcpPlan.preload.length
      ? (await runWithAbort(abortController.signal, () =>
          loadMcpServerTools(conversationId, runProjectRoot, mcpPlan.preload))).tools
      : [];
    const mcpLoader = mcpPlan.offerLoader ? [mcpLoadToolDefinition(mcpServerIds)] : [];
    const currentTools = () => [...nativeTools, ...mcpLoader, ...mcpTools];
    const selectedTools = currentTools();
    const toolTokens = estimateAiToolTokens(selectedTools);
    const relayedRun = externalSource ? activePluginTurn(conversationId) : undefined;
    const systemPrompt = externalSource
      ? `${baseSystemPrompt}\n\n${externalTurnSystemPrompt(externalSource, {
        instructions: relayedRun?.instructions,
        allowedPlugins: relayedRun?.allowedPlugins,
      })}`
      : baseSystemPrompt;
    const delegationGuidance = selectedTools.some((tool) => tool.name === "agent.run_parallel")
      ? "\n\nWhen the user asks you to delegate work to a new conversation, use agent.run_parallel. Use one task for a single handoff. When the user asks for several child conversations, dispatch each actual task directly from this conversation in one call and give each task a self-contained prompt. Keep coordination and the final summary in this conversation. "
        + "Choose a configured model per task when the user specifies a model or asks you to choose. Use only exact IDs from this list, and omit the model when the default suits the task. Configured delegation models: "
        + JSON.stringify(availableModels.value.map(({ value, label, profileLabel }) => ({ id: value, name: label, profile: profileLabel })))
      : "";
    const uncompactedHistory: AiLoopMessage[] = [
      ...(context ? [{ role: "system" as const, content: context }] : []),
      { role: "system" as const, content: systemPrompt + delegationGuidance },
      ...conversationHistory,
    ];
    if (superseded()) return;
    const contextCompaction = compactAiLoopContext(uncompactedHistory, requestConfig, toolTokens);
    const history = contextCompaction.messages;
    if (contextCompaction.compacted) {
      // 压缩发生在任何流式片段之前，eventTriggerMessageId 仍为空；
      // 锚到触发本轮对话的用户消息，避免压缩事件变成线程末尾的孤儿事件卡。
      assistant.upsertAgentEventForConversation(conversationId, {
        id: createId("context-compaction"),
        kind: "mode",
        title: t("ai.contextCompaction.title"),
        detail: t("ai.contextCompaction.detail", {
          count: contextCompaction.removedMessages,
        }),
        status: "completed",
        triggerMessageId: eventTriggerMessageId ?? triggerMessageId,
      });
    }
    await scrollToBottom(conversationId);
    // Save the request before the first model call: if that call fails, the user's
    // message is still in context for a retry or "continue".
    saveLoopHistory(history);
    const result = await runAiLoop({
      config: requestConfig,
      maxLoops: normalizeAiMaxLoops(loadStoredSettings().aiMaxLoops),
      messages: history,
      tools: selectedTools,
      currentTools,
      compactHistory: (loopHistory) => {
        const compaction = compactAiLoopContext(loopHistory, requestConfig, estimateAiToolTokens(currentTools()));
        return compaction.compacted ? compaction.messages : undefined;
      },
      executeTool: (call, signal) => mcpLoader.length && call.name === MCP_LOAD_TOOL_NAME
        ? loadMcpToolsForModel(conversationId, call, mcpServerIds, mcpTools, runProjectRoot, signal)
        : executeAgentTool(
          conversationId,
          toolTriggerMessageIds.get(call.id),
          call,
          mcpTools,
          runResources,
          runProjectRoot,
          signal,
          {
            ...(externalToolNames ? { allowedTools: externalToolNames } : {}),
          },
        ),
      // MCP tools and every mutation remain serialized. Native capabilities explicitly
      // classified as read-only can be run together because they have no same-turn
      // dependency and the loop preserves the provider's tool-result order.
      canExecuteToolInParallel: (call) => aiCapabilityRegistry.get(call.name)?.risk === "read",
      systemReminder: conversationPlanModeReminder(conversationId),
      signal: abortController.signal,
      streamTurn: (config, messages, tools, onEvent, signal) => {
        // Context comes from the saved child records. Keep it out of persisted loop history.
        const firstTurn = messages.findIndex((message) => message.role !== "system");
        const requestMessages = reportContext && firstTurn >= 0
          ? [...messages.slice(0, firstTurn), reportContext, ...messages.slice(firstTurn)]
          : messages;
        return streamAiTurnWithFailover({
          config,
          models: attemptModels,
          messages: requestMessages,
          tools,
          onEvent,
          signal,
          onFallback: (from, to) => {
            if (!superseded()) clearPreparingTools();
            notifyModelFallback(from, to);
          },
          onAttempt: (model) => {
            attemptedModelRequests += 1;
            attemptedModels.push(model);
          },
        });
      },
      onAssistantDelta: (delta) => {
        streamTextBatcher.append("assistant", delta);
      },
      onReasoningDelta: (delta) => {
        streamTextBatcher.append("reasoning", delta);
      },
      onToolCallProgress: (progress) => {
        streamTextBatcher.flush();
        if (superseded() || progress.name === "task_complete") return;
        assistant.settleRunningAgentEvents(conversationId, "completed", "thought");
        eventTriggerMessageId = ensureAssistantEventAnchor(conversationId, eventTriggerMessageId);
        const tool = agentToolDisplayName(progress.name);
        const event = { id: `loop-tool-preparing-${progress.index}`, triggerMessageId: eventTriggerMessageId };
        assistant.upsertAgentEventForConversation(conversationId, {
          ...event,
          kind: "tool",
          title: tool,
          detail: progress.bytes > 0
            ? t("ai.toolProgress.preparingBytes", { size: formatDataBytes(progress.bytes) })
            : t("ai.toolProgress.preparing"),
          status: "running",
          toolName: progress.name,
        });
        preparingToolEvents.set(progress.index, event);
        void reportConversationRun(conversationId, "tool_running", t("ai.taskProgress.preparingTool", { tool }));
        void scrollToBottomIfFollowing(conversationId);
      },
      onToolCall: (call) => {
        streamTextBatcher.flush();
        if (superseded()) return;
        clearPreparingTools();
        if (call.name === "task_complete") return;
        assistant.settleRunningAgentEvents(conversationId, "completed", "thought");
        // 只有最后一次业务工具之后生成的普通回复才可能与最终总结重复。
        completedTurnAssistantMessageId = undefined;
        void reportConversationRun(
          conversationId,
          "tool_running",
          agentToolProgress(call.name),
        );
        eventTriggerMessageId = ensureAssistantEventAnchor(conversationId, eventTriggerMessageId);
        if (eventTriggerMessageId) toolTriggerMessageIds.set(call.id, eventTriggerMessageId);
        upsertToolEvent(conversationId, eventTriggerMessageId, call, "running");
        void scrollToBottomIfFollowing(conversationId);
      },
      onToolResult: (call, result) => {
        streamTextBatcher.flush();
        if (superseded()) return;
        void reportConversationRun(conversationId, "thinking", t("ai.taskProgress.thinking"));
        upsertToolEvent(
          conversationId,
          toolTriggerMessageIds.get(call.id),
          call,
          result.status ?? "completed",
          // The model gets the English constant; the card shows the note in the UI language.
          result.content === REDIRECTED_TOOL_RESULT ? t("ai.agentic.redirectedToolResult") : result.content,
        );
        void scrollToBottomIfFollowing(conversationId);
      },
      onHistoryChange: saveLoopHistory,
      onLoopNotice: (kind) => {
        if (superseded()) return;
        eventTriggerMessageId = ensureAssistantEventAnchor(conversationId, eventTriggerMessageId);
        assistant.upsertAgentEventForConversation(conversationId, {
          id: createId("loop-notice"),
          kind: "mode",
          title: t(kind === "completion_rejected" ? "ai.loopNotice.completionRejected" : "ai.loopNotice.continuationNudged"),
          detail: t(kind === "completion_rejected" ? "ai.loopNotice.completionRejectedDetail" : "ai.loopNotice.continuationNudgedDetail"),
          status: "completed",
          triggerMessageId: eventTriggerMessageId,
        });
        void scrollToBottomIfFollowing(conversationId);
      },
      onUsage: (usage) => {
        replyTokenUsage = mergeAiTokenUsage(replyTokenUsage, usage);
        if (usage.inputTokens > 0) replyTokenUsage = { ...replyTokenUsage, contextTokens: usage.inputTokens };
      },
      onTurnComplete: (message) => {
        streamTextBatcher.flush();
        if (superseded()) return;
        clearPreparingTools();
        assistant.settleRunningAgentEvents(conversationId, "completed", "thought");
        const conversation = assistant.conversationById(conversationId);
        const hasBusinessToolCall = message.toolCalls?.some((call) => call.name !== "task_complete") === true;
        if (hasBusinessToolCall) completedTurnAssistantMessageId = undefined;
        else if (message.content) {
          completedTurnAssistantMessageId = conversation?.messages[conversation.messages.length - 1]?.id;
          eventTriggerMessageId = completedTurnAssistantMessageId;
        }
        appendToCurrent.value = false;
        eventTriggerMessageId = undefined;
      },
    });
    if (superseded()) return;
    saveLoopHistory(result.history);
    replyTokenUsage = withAiTokenRequestStats(replyTokenUsage, attemptedModelRequests, attemptedModels);
    const answeredBy = attemptedModels[attemptedModels.length - 1];
    if (replyTokenUsage && answeredBy) replyTokenUsage = { ...replyTokenUsage, answeredBy };
    // 本地会话终态必须先于运行记录上报，避免上报延迟时列表长期停留在“进行中”。
    assistant.setConversationStatus(conversationId, result.completed ? "completed" : "interrupted");
    if (result.outcome === "task_completed") {
      // task_complete.summary 是唯一的最终用户交付内容；移除同一轮模型先流式输出的临时普通回复，避免重复展示。
      // 先加总结再删临时回复，临时回复上的思考/工具事件改挂到总结上，不会变成孤儿被挪进下一轮。
      const summaryMessageId = assistant.addMessageToConversation(conversationId, {
        role: "assistant",
        content: result.summary,
        presentation: "task-summary",
        tokenUsage: replyTokenUsage,
      });
      if (completedTurnAssistantMessageId) {
        assistant.removeMessageFromConversation(conversationId, completedTurnAssistantMessageId, summaryMessageId);
      }
      if (summaryMessageId && replyTokenUsage) {
        assistant.setMessageTokenUsage(conversationId, summaryMessageId, replyTokenUsage);
      }
      assistant.settleRunningAgentEvents(conversationId, "completed");
      if (superseded()) return;
      await reportConversationRun(
        conversationId,
        "completed",
        result.summary || t("ai.taskProgress.completed"),
      );
      notifyAi(aiAgentTaskNotification(true, result.summary), conversationId);
    } else if (result.outcome === "response") {
      const conversation = assistant.conversationById(conversationId);
      const lastAssistant = [...(conversation?.messages ?? [])]
        .reverse()
        .find((message) => message.role === "assistant" && !message.eventAnchor);
      if (lastAssistant?.id) {
        if (replyTokenUsage) assistant.setMessageTokenUsage(conversationId, lastAssistant.id, replyTokenUsage);
        // A turn that did work (ran tools) delivers a task summary whether or not the model
        // called task_complete; a plain chat reply stays a normal message.
        if (result.usedTools && lastAssistant.content.trim()) assistant.setMessagePresentation(conversationId, lastAssistant.id, "task-summary");
      }
      assistant.settleRunningAgentEvents(conversationId, "completed");
      if (superseded()) return;
      await reportConversationRun(
        conversationId,
        "completed",
        result.summary || t("ai.taskProgress.completed"),
      );
    } else if (result.outcome === "loop_limit") {
      // The wrap-up reply has already streamed in as its own message. Replace it with the
      // notice plus summary so the summary is shown once, directly below the notice.
      const conversation = assistant.conversationById(conversationId);
      const streamedSummary = completedTurnAssistantMessageId
        ? conversation?.messages.find((message) => message.id === completedTurnAssistantMessageId)
        : undefined;
      const loopLimitMessageId = assistant.addMessageToConversation(conversationId, {
        role: "assistant",
        content: `${t("ai.agentic.loopLimitReached", { steps: result.loopCount })}\n\n${result.summary}`,
        presentation: "loop-limit",
        tokenUsage: replyTokenUsage,
      });
      if (streamedSummary?.id && streamedSummary.content.trim() === result.summary.trim()) {
        assistant.removeMessageFromConversation(conversationId, streamedSummary.id, loopLimitMessageId);
      }
      if (loopLimitMessageId && replyTokenUsage) {
        assistant.setMessageTokenUsage(conversationId, loopLimitMessageId, replyTokenUsage);
      }
      assistant.settleRunningAgentEvents(conversationId, "failed");
      if (superseded()) return;
      await reportConversationRun(
        conversationId,
        "failed",
        t("ai.taskProgress.loopLimit"),
        "loop_limit",
      );
      notifyAi(aiAgentTaskNotification(false, text), conversationId);
    }
  } catch (error) {
    streamTextBatcher.flush();
    if (superseded()) return;
    clearPreparingTools();
    replyTokenUsage = withAiTokenRequestStats(replyTokenUsage, attemptedModelRequests, attemptedModels);
    const aborted = abortController.signal.aborted;
    const streamTimedOut = isAiStreamIdleTimeoutError(error);
    assistant.settleRunningAgentEvents(conversationId, aborted ? "cancelled" : "failed");
    assistant.setConversationStatus(conversationId, aborted ? "interrupted" : "failed");
    if (superseded()) return;
    const runUpdate = reportConversationRun(
      conversationId,
      aborted ? "cancelled" : "failed",
      aborted
        ? t("ai.taskProgress.cancelled")
        : streamTimedOut
          ? t("ai.taskProgress.streamTimeout")
          : t("ai.taskProgress.failed"),
      aborted ? undefined : streamTimedOut ? "stream_timeout" : "agentic_failed",
    );
    await runUpdate;
    const notification = aiErrorNotification(error, "notifications.ai.taskFailed", text);
    if (notification) {
      const message = streamTimedOut
        ? t("ai.agentic.streamTimeout")
        : error instanceof Error
          ? error.message
          : String(error);
      assistant.addMessageToConversation(conversationId, {
        role: "assistant",
        content: streamTimedOut ? message : t("ai.agentic.failed", { error: message }),
        presentation: "error",
        tokenUsage: replyTokenUsage,
      });
      ElMessage.error(message);
      notifyAi(notification, conversationId);
    } else if (replyTokenUsage) {
      // Cancellation may keep a partial assistant reply instead of adding an error message.
      const conversation = assistant.conversationById(conversationId);
      const triggerIndex = conversation?.messages.findIndex((message) => message.id === triggerMessageId) ?? -1;
      const currentTurnAssistant = triggerIndex >= 0
        ? conversation?.messages.slice(triggerIndex + 1)
          .reverse()
          .find((message) => message.role === "assistant" && !message.eventAnchor)
        : undefined;
      if (currentTurnAssistant?.id) {
        assistant.setMessageTokenUsage(conversationId, currentTurnAssistant.id, replyTokenUsage);
      }
    }
  } finally {
    streamTextBatcher.flush();
    streamTextBatcher.dispose();
    // 中断超时兜底可能已经启动下一条消息，旧任务不得清理新任务的控制器和发送锁。
    if (agenticAborts.get(conversationId) === abortController) {
      agenticAborts.delete(conversationId);
      subagentUsageCollectors.delete(conversationId);
      agenticSendGate.release(conversationId);
      await scrollToBottomIfFollowing(conversationId);
      await finishPluginTurnForConversation(conversationId);
      const shouldResumeQueue = !abortController.signal.aborted
        || resumeQueueAfterCancellation.delete(conversationId);
      if (shouldResumeQueue) {
        await runNextQueuedMessage(conversationId);
      }
    }
  }
}
function closeAssistant() {
  if (isPopupWindow()) {
    void closePopupWindow(popupWindowLabel()).catch(() => assistant.close());
    return;
  }
  assistant.close();
}
async function popOutWindow() {
  if (!isDesktopRuntime()) {
    ElMessage.warning(t("tab.popoutDesktopOnly"));
    return;
  }
  if (assistant.loading) {
    ElMessage.warning(t("ai.agentic.popoutWhileRunning"));
    return;
  }
  try {
    await popOutAiAssistant(t("ai.drawer.windowTitle"), assistant.snapshot());
    assistant.close();
    if (tabSurfaceActive.value) void router.push({ name: "dashboard" }).catch(() => undefined);
  } catch {
    ElMessage.error(t("tab.popupUnavailable"));
  }
}
/** Launch of a built-in or custom adapter id (plugin agents launch by reference in useAcpAgentStart). */
function resolveAgentCommand(adapter: string) {
  const stored = loadStoredSettings();
  if (adapter.startsWith("custom:")) {
    const id = adapter.slice("custom:".length);
    const agent = stored.acpAgents?.find((item) => item.id === id);
    if (agent)
      return {
        command: agent.command,
        args: agent.args.trim() ? agent.args.trim().split(/\s+/) : [],
      };
  }
  return acpBuiltinAgentLaunch(adapter);
}
const { startConversationAgent } = useAcpAgentStart({
  selectedAdapterId: () => loadStoredSettings().acpAdapterId,
  customAgents: () => loadStoredSettings().acpAgents,
  addSystemMessage: (conversationId, content) => assistant.addMessageToConversation(conversationId, { role: "system", content }),
  startNewConversation: () => createConversation(),
  markServersDisconnected: (serverIds) => void handleAcpServersDisconnected(serverIds),
});

async function handleAcpServersDisconnected(serverIds: string[]) {
  const disconnected = new Set(serverIds);
  const projectTargets = new Map<string, AcpRunTarget>();
  for (const [id, start] of acpStartingServers) {
    if (!disconnected.delete(start.serverId)) continue;
    if (start.directoryKind === "project") {
      // The event may be from a previous connection under the same ID.
      // Verify the connection after initialization, before publishing the session.
      start.needsConnectionCheck = true;
    } else {
      acpStartingServers.delete(id);
    }
  }
  for (const [id, target] of acpRunTargets) {
    if (!disconnected.has(target.serverId)) continue;
    if (target.directoryKind === "project") {
      projectTargets.set(id, target);
    } else {
      releaseAcpRun(id);
      acpPromptAborts.get(id)?.abort();
      disconnected.delete(target.serverId);
    }
  }
  if (!disconnected.size) return;

  // Project launches reuse the persisted server ID. Check whether the event was
  // queued for a connection that has already been replaced.
  const connected = await invokeBackend<{ id: string }[]>("acp_list_status", {}).catch(() => []);
  if (acpDisposed) return;
  for (const server of connected) disconnected.delete(server.id);
  for (const { serverId } of acpStartingServers.values()) disconnected.delete(serverId);
  for (const [id, target] of acpRunTargets) {
    if (!disconnected.has(target.serverId)) continue;
    if (projectTargets.get(id) !== target) {
      disconnected.delete(target.serverId);
      continue;
    }
    releaseAcpRun(id);
    acpPromptAborts.get(id)?.abort();
  }
  assistant.markAcpServersDisconnected([...disconnected]);
}
function releaseAcpRun(conversationId: string) {
  const target = acpRunTargets.get(conversationId);
  if (!target) return;
  acpRunTargets.delete(conversationId);
  acpContextInjected.delete(conversationId);
  stoppedAcpSessions.delete(acpSessionKey(target));
  delete acpConfigOptions.value[conversationId];
  if (assistant.conversationById(conversationId)?.pendingPermission?.serverId === target.serverId) {
    assistant.setConversationPermission(conversationId);
  }
  return target;
}

async function stopDefaultDirectoryAgent(conversationId: string) {
  const target = acpRunTargets.get(conversationId);
  if (!target || target.directoryKind === "project" || acpSendGate.isClaimed(conversationId)) return;
  releaseAcpRun(conversationId);
  try {
    await invokeBackend("acp_disconnect", { serverId: target.serverId });
  } catch (error) {
    ElMessage.error(nativeErrorMessage(error));
  }
}

const localAgentDirectoryDeps = {
  check: async (path: string) => (await invokeBackend<{ available: boolean }>("acp_check_local_directory", { path })).available,
  getDefault: async () => {
    await homeDirectoryReady;
    return homeDirectory.value;
  },
};

async function startAgent(conversationId?: string, useDefaultOnce = false, request?: QueuedSendRequest) {
  if (!isDesktopRuntime()) {
    ElMessage.warning(t("common.desktopRequired"));
    return;
  }
  const id = conversationId ?? assistant.activeConversationId;
  const relink = acpWorkspaceRelinks.get(id);
  if (relink) await relink;
  const conversation = assistant.conversationById(id);
  if (!conversation) {
    ElMessage.error(t("notifications.ai.agentStartFailed"));
    return;
  }

  const remote = Boolean(conversation.external || request?.pluginTurn);
  try {
    const pending = agentStartPromises.get(id);
    if (pending) {
      const target = await pending;
      if (!remote || !target || target.directoryKind === "project") return target;
    }

    const startPromise = (async (): Promise<AcpRunTarget | undefined> => {
      let running = acpRunTargets.get(id);
      if (running && !remote && running.directoryKind !== "project") return running;
      if (!conversation.projectRoot && !remote) await homeDirectoryReady;
      let context = remote ? workspaceContextForConversation(id) : running?.context ?? workspaceContextForConversation(id);
      let directoryKind: AcpRunTarget["directoryKind"] = "project";
      if (remote) {
        await ensureConversationProjectAvailable(context, true);
        if (acpDisposed || !assistant.conversationById(id)) return;
        running = acpRunTargets.get(id);
        if (running?.directoryKind === "project") return running;
        if (activeAcpPrompts.has(id)) throw new Error(t("ai.send.busy"));
      } else {
        if (useDefaultOnce && (unavailableAcpDirectories.get(id) !== context.projectRoot || conversation.running)) return;
        if (!useDefaultOnce) unavailableAcpDirectories.delete(id);
        const directory = await resolveLocalAgentDirectory(conversation.projectRoot?.trim() ?? "", useDefaultOnce, localAgentDirectoryDeps);
        if (acpDisposed || !assistant.conversationById(id)) return;
        if (directory.kind === "unavailable") {
          unavailableAcpDirectories.set(id, directory.path);
          ElMessage.warning(t("ai.conversation.workspace.projectUnavailable"));
          return;
        }
        if (running && acpRunTargets.get(id) !== running) return;
        directoryKind = directory.kind;
        if (running && directoryKind === "project") return running;
        if (directoryKind !== "project") {
          context = { ...context, projectRoot: directory.path, resources: createResourceRegistry() };
        }
      }
      if (running) {
        releaseAcpRun(id);
        assistant.setConversationAgentState(id, { agentReady: false });
        await invokeBackend("acp_disconnect", { serverId: running.serverId });
      }
      if (acpDisposed || !assistant.conversationById(id)) return;

      // The primary session and adapter belong to the original project.
      const primary = directoryKind === "project";
      const serverId = primary ? conversation.acpServerId : createId("local-acp");
      const previousSessionId = primary ? conversation.acpSessionId : "";
      if (previousSessionId && conversation.agentReady) {
        const target: AcpRunTarget = { serverId, sessionId: previousSessionId, context, directoryKind };
        acpRunTargets.set(id, target);
        return target;
      }
      const cwd = context.projectRoot;
      let mcpServerSet: AcpMcpServer[] | undefined;
      const mcpServers = () => (mcpServerSet ??= configuredAcpMcpServers(cwd));
      const start: AcpStartingServer = { serverId, directoryKind, needsConnectionCheck: false };
      acpStartingServers.set(id, start);
      let launched = false;
      let ready = false;
      const stillStarting = () => acpStartingServers.get(id) === start && !acpDisposed && !!assistant.conversationById(id);
      try {
        const adapterId = await startConversationAgent({
          conversationId: id,
          serverId,
          projectRoot: cwd,
          recordedAdapterId: conversation.acpAdapterId,
          hasPreviousSession: Boolean(previousSessionId),
          mcpServers,
          launchCommandAgent: async (adapter) => {
            if (!stillStarting()) return;
            const launch = resolveAgentCommand(adapter);
            await invokeBackend("acp_launch", { serverId, command: launch.command, args: launch.args, env: {}, cwd });
          },
        });
        if (!adapterId) return;
        launched = true;
        if (!stillStarting()) return;
        const acpMcpServers = mcpServers();
        if (!stillStarting()) return;
        const sessionArgs = { serverId, cwd, mcpServers: acpMcpServers };
        let restored: AcpConfigOptionsResult | undefined;
        if (previousSessionId) {
          try {
            restored = await invokeBackend<AcpConfigOptionsResult>("acp_load_session", {
              ...sessionArgs, sessionId: previousSessionId,
            });
          } catch {
            if (!stillStarting()) return;
            // Keep the old recovery ID until a replacement session is ready.
          }
        }
        if (!stillStarting()) return;
        const session = restored
          ? { sessionId: previousSessionId, configOptions: restored.configOptions }
          : await invokeBackend<AcpNewSession>("acp_new_session", sessionArgs);
        if (!stillStarting()) return;
        while (start.needsConnectionCheck) {
          start.needsConnectionCheck = false;
          const connected = await invokeBackend<{ id: string }[]>("acp_list_status", {});
          if (!stillStarting()) return;
          if (!connected.some((server) => server.id === serverId)) {
            throw new Error(t("ai.localAgent.disconnectedDuringStart"));
          }
        }

        const target: AcpRunTarget = { serverId, sessionId: session.sessionId, context, directoryKind };
        acpRunTargets.set(id, target);
        unavailableAcpDirectories.delete(id);
        acpConfigOptions.value[id] = parseAcpConfigSelectOptions(session.configOptions);
        if (primary) {
          assistant.setConversationAgentState(id, { acpSessionId: session.sessionId, acpAdapterId: adapterId, agentReady: true });
        }
        if (restored) {
          acpContextInjected.add(id);
          ElMessage.success(t("ai.agentSessionRestored"));
        } else {
          acpContextInjected.delete(id);
          if (primary) {
            assistant.addMessageToConversation(id, {
              role: "system",
              content: previousSessionId ? t("ai.agentSessionRestarted") : t("ai.agentReady"),
            });
          }
        }
        ready = true;
        return target;
      } finally {
        if (acpStartingServers.get(id) === start) acpStartingServers.delete(id);
        if (launched && !ready) await invokeBackend("acp_disconnect", { serverId }).catch(() => {});
      }
    })();

    agentStartPromises.set(id, startPromise);
    assistant.agentStarting = true;
    try {
      return await startPromise;
    } finally {
      agentStartPromises.delete(id);
      assistant.agentStarting = agentStartPromises.size > 0;
    }
  } catch (error) {
    if (acpDisposed) return;
    ElMessage.error(nativeErrorMessage(error));
    notifyAi(aiErrorNotification(error, "notifications.ai.agentStartFailed", conversation.title) ?? {
      level: "error",
      titleKey: "notifications.ai.agentStartFailed",
    }, id);
  }
}
async function sendAgent(request?: QueuedSendRequest) {
  const text = (request?.content ?? input.value).trim();
  const selectedAttachments = sanitizeAiAttachments(request?.attachments ?? attachments.value);
  if (!text && !selectedAttachments.length) return;
  const conversationId = request?.conversationId ?? assistant.activeConversationId;
  if (!(await ensureSendableConversation(conversationId))) return;
  const activeConversation = assistant.conversationById(conversationId);
  // 普通输入在运行中应入队；已出队请求则由发送锁串行，不受可能滞后的运行态投影阻塞。
  if (activeConversation?.running && !request) {
    assistant.enqueueMessage(conversationId, {
      content: text,
      attachments: selectedAttachments,
      createdAt: Date.now(),
    });
    consumeComposerInput(text, selectedAttachments);
    ElMessage.info(t("ai.queue.enqueued"));
    return;
  }
  if (agentStartPromises.has(conversationId) || !acpSendGate.claim(conversationId)) {
    if (!request) ElMessage.info(t("ai.send.busy"));
    return;
  }
  // 新任务开始前收口任何历史遗留的运行事件，避免旧分析计时跨到下一条消息。
  assistant.settleRunningAgentEvents(conversationId, "cancelled");
  const target = await startAgent(conversationId, false, request);
  const conversation = assistant.conversationById(conversationId);
  if (!target || !conversation) {
    acpSendGate.release(conversationId);
    return;
  }
  const { context: runContext, serverId, sessionId } = target;
  try {
    await refreshConversationRuntimeResources(runContext.resources, runContext.projectRoot);
  } catch (error) {
    acpSendGate.release(conversationId);
    ElMessage.warning(nativeErrorMessage(error));
    return;
  }
  if (acpRunTargets.get(conversationId) !== target) {
    acpSendGate.release(conversationId);
    return;
  }
  const triggerMessageId = assistant.addMessageToConversation(conversationId, {
    role: "user",
    content: text,
    attachments: selectedAttachments,
    createdAt: request?.createdAt,
    id: request?.messageId,
  });
  if (!triggerMessageId) {
    acpSendGate.release(conversationId);
    return;
  }
  acpCurrentMessageIds.delete(conversationId);
  acpTextSegments.reset(conversationId);
  acpEventMessageIds.set(conversationId, new Map());
  if (!request) consumeComposerInput(text, selectedAttachments);
  const runId = await beginConversationRun(
    conversationId,
    "acp",
    sessionId,
    conversationRunTitle(conversation, text),
    runContext,
  );
  if (!runId || acpRunTargets.get(conversationId) !== target) {
    acpSendGate.release(conversationId);
    assistant.setConversationStatus(conversationId, "failed");
    if (runId) await reportConversationRun(conversationId, "failed", t("ai.taskProgress.failed"));
    return;
  }
  assistant.anchorUnassignedAgentEvents(conversationId, triggerMessageId);
  assistant.setConversationStatus(conversationId, "in_progress");
  const abortController = new AbortController();
  acpPromptAborts.set(conversationId, abortController);
  const superseded = () => acpPromptAborts.get(conversationId) !== abortController;
  await scrollToBottom(conversationId);
  try {
    if (superseded()) return;
    const promptTarget = { serverId, sessionId };
    stoppedAcpSessions.delete(acpSessionKey(promptTarget));
    activeAcpPrompts.set(conversationId, promptTarget);
    await runWithAbort(abortController.signal, () =>
      invokeBackend("acp_prompt", {
        ...promptTarget,
        text: acpPromptText(text, conversationId, runContext.workspaceId),
        content: selectedAttachments.map(toAcpPromptContent),
      }),
    );
    if (superseded()) return;
    acpContextInjected.add(conversationId);
    assistant.settleRunningAgentEvents(conversationId, "completed");
    assistant.setConversationStatus(conversationId, "completed");
    await reportConversationRun(conversationId, "completed", t("ai.taskProgress.completed"));
  } catch (error) {
    acpStreamTextBatcher.flush();
    if (superseded()) return;
    const cancelled = abortController.signal.aborted
      || activeRunPhases.get(conversationId) === "cancelling"
      || activeRunPhases.get(conversationId) === "cancelled";
    assistant.settleRunningAgentEvents(conversationId, cancelled ? "cancelled" : "failed");
    assistant.setConversationStatus(conversationId, cancelled ? "interrupted" : "failed");
    const runUpdate = reportConversationRun(
      conversationId,
      cancelled ? "cancelled" : "failed",
      cancelled ? t("ai.taskProgress.cancelled") : t("ai.taskProgress.failed"),
      cancelled ? undefined : "acp_prompt_failed",
    );
    await runUpdate;
    if (!cancelled) {
      const notification = aiErrorNotification(error, "notifications.ai.agentPromptFailed", text);
      if (notification) {
        ElMessage.error(String(error));
        notifyAi(notification, conversationId);
      }
    }
  } finally {
    acpStreamTextBatcher.flush();
    // 中断超时兜底可能已经启动下一条消息，旧任务不得清理新任务的控制器和发送锁。
    if (acpPromptAborts.get(conversationId) === abortController) {
      acpPromptAborts.delete(conversationId);
      activeAcpPrompts.delete(conversationId);
      acpSendGate.release(conversationId);
      await scrollToBottomIfFollowing(conversationId);
      const shouldResumeQueue = !abortController.signal.aborted
        || resumeQueueAfterCancellation.delete(conversationId);
      if (shouldResumeQueue) {
        await runNextQueuedMessage(conversationId);
      }
    }
  }
}
async function cancelConversation(
  conversationId: string,
  options: { resumeQueue?: boolean } = {},
) {
  const conversation = assistant.conversationById(conversationId);
  if (!conversation) return false;
  if (options.resumeQueue) resumeQueueAfterCancellation.add(conversationId);
  else resumeQueueAfterCancellation.delete(conversationId);
  toolApprovalQueue.rejectConversation(conversationId);
  if (conversation.mode === "agentic") {
    const controller = agenticAborts.get(conversationId);
    if (!controller) {
      if (!conversation.running) {
        resumeQueueAfterCancellation.delete(conversationId);
        return false;
      }
      await recoverStalledQueueInterruption(conversationId, options.resumeQueue ?? false);
      return true;
    }
    controller.abort();
    void reportConversationRun(conversationId, "cancelling", t("ai.taskProgress.cancelling"));
    return true;
  }
  const controller = acpPromptAborts.get(conversationId);
  const prompt = activeAcpPrompts.get(conversationId);
  const cancelTarget = prompt ?? acpSessionTarget(conversation);
  if (!controller) {
    if (!conversation.running && !cancelTarget) {
      resumeQueueAfterCancellation.delete(conversationId);
      return false;
    }
    await recoverStalledQueueInterruption(conversationId, options.resumeQueue ?? false);
    return true;
  }
  controller.abort();
  markAcpSessionStopped(cancelTarget);
  if (cancelTarget) {
    void invokeBackend("acp_cancel", cancelTarget).catch((error) => {
      ElMessage.error(String(error));
    });
  }
  void reportConversationRun(conversationId, "cancelling", t("ai.taskProgress.cancelling"));
  return true;
}
async function cancelCurrent() {
  await cancelConversation(assistant.activeConversationId);
}
// Panel keys run through the keybinding dispatcher (keymap commands ai:*,
// bound in HOST_KEYBINDING_RULES with `ai.panel.focus`); these are the handlers.
async function focusNewConversation() {
  await createConversation();
  await nextTick();
  drawerPanel.value?.querySelector<HTMLTextAreaElement>(".ai-assistant-composer textarea")?.focus();
}
async function openConversationSearch() {
  if (!conversationPreferences.globalMode) return;
  conversationSidebarOpen.value = true;
  await nextTick();
  drawerPanel.value?.querySelector<HTMLInputElement>(".ai-conversation-sidebar__search input")?.focus();
}
async function switchAdjacentConversation(direction: -1 | 1) {
  const ids = groupedConversations.value.flatMap((group) => group.conversations.map((item) => item.id));
  const id = adjacentConversationId(ids, assistant.activeConversationId, direction);
  if (id) await switchConversation(id);
}
function pendingPermissionTitle() {
  const toolCall = assistant.pendingPermission?.toolCall;
  if (toolCall && typeof toolCall === "object" && "title" in toolCall) {
    const title = (toolCall as { title?: unknown }).title;
    if (typeof title === "string" && title.trim()) return title.trim();
  }
  return t("ai.permissionHint");
}

async function handleProtocolPermissionRequest(request: AiAssistantPermission) {
  let conversation = findConversationByAcpSession(request.serverId, request.sessionId);
  if (!conversation) return;

  try {
    await approvalModeHydration;
  } catch (error) {
    ElMessage.error(String(error));
    assistant.setConversationPermission(conversation.id, request);
    assistant.setConversationStatus(conversation.id, "needs_input");
    return;
  }
  const optionId = automaticProtocolPermissionOption(
    aiApprovalMode.value,
    asRecord(request.toolCall).kind,
    Array.isArray(request.options) ? request.options : [],
  );
  if (optionId) {
    try {
      await invokeBackend("acp_respond_permission", {
        requestId: request.requestId,
        serverId: request.serverId,
        sessionId: request.sessionId,
        optionId,
        cancelled: false,
      });
      conversation = findConversationByAcpSession(request.serverId, request.sessionId);
      if (conversation) assistant.setConversationStatus(conversation.id, "in_progress");
      return;
    } catch (error) {
      ElMessage.error(String(error));
    }
  }

  conversation = findConversationByAcpSession(request.serverId, request.sessionId);
  if (!conversation) return;
  assistant.setConversationPermission(conversation.id, request);
  assistant.setConversationStatus(conversation.id, "needs_input");
}

async function answerPermission(optionId?: string) {
  const request = assistant.pendingPermission;
  if (!request) return;
  const conversation = findConversationByAcpSession(request.serverId, request.sessionId);
  try {
    await invokeBackend("acp_respond_permission", {
      requestId: request.requestId,
      serverId: request.serverId,
      sessionId: request.sessionId,
      optionId,
      cancelled: !optionId,
    });
    if (conversation) {
      assistant.setConversationPermission(conversation.id);
      assistant.setConversationStatus(conversation.id, optionId ? "in_progress" : "interrupted");
    }
  } catch (error) {
    ElMessage.error(String(error));
  }
}
function dismissPendingPermission() {
  const request = assistant.pendingPermission;
  if (!request) return;
  const conversation = findConversationByAcpSession(request.serverId, request.sessionId);
  if (conversation) {
    assistant.setConversationPermission(conversation.id);
    assistant.setConversationStatus(conversation.id, "interrupted");
  }
  void invokeBackend("acp_respond_permission", {
    requestId: request.requestId,
    serverId: request.serverId,
    sessionId: request.sessionId,
    cancelled: true,
  });
}
function send() {
  if (activeParallelDelegationPending.value) return;
  const budget = pluginContextBudgetState.value;
  if (budget?.exceeded) {
    ElMessage.warning(t("pluginContrib.aiContextProviders.sendBlocked", { excess: budget.exceeded }));
    return;
  }
  if (editingMessageId.value) return resendEditedMessage();
  if (editingQueueId.value) return updateQueuedMessageFromComposer();
  if (assistant.mode === "agent") return sendAgent();
  return sendAgentic();
}
async function sendQueuedMessage(conversationId: string, msg: AiQueuedMessage) {
  const conversation = assistant.conversationById(conversationId);
  if (!conversation) return;
  const request: QueuedSendRequest = {
    conversationId,
    content: msg.content,
    attachments: msg.attachments?.map((attachment) => ({ ...attachment })) ?? [],
    createdAt: msg.createdAt,
    messageId: msg.id,
    pluginTurn: msg.pluginTurn ? { ...msg.pluginTurn } : undefined,
  };
  if (conversation.mode === "agent") await sendAgent(request);
  else await sendAgentic(request);
  // A relayed message the loop refused to start (no model configured, busy
  // workspace, ...) must still end its turn instead of timing out.
  const run = msg.pluginTurn ? pluginTurnRuns.get(msg.pluginTurn.turnId) : undefined;
  if (run && !run.messageId) await finishPluginTurn(run, "failed", t("ai.external.notStarted"));
}

function activePluginTurn(conversationId: string) {
  const turnId = pluginTurnByConversation.get(conversationId);
  const run = turnId ? pluginTurnRuns.get(turnId) : undefined;
  return run && !run.finished ? run : undefined;
}

function beginPluginTurn(turnId: string, conversationId: string, messageId: string) {
  const run = pluginTurnRuns.get(turnId);
  if (!run || run.finished) return;
  run.messageId = messageId;
  pluginTurnByConversation.set(conversationId, turnId);
  run.timer = setInterval(() => void pushPluginTurnProgress(run), 700);
  void pushPluginTurnProgress(run);
}

function runningEventTitle(conversationId: string) {
  const events = assistant.conversationById(conversationId)?.agentEvents ?? [];
  for (let index = events.length - 1; index >= 0; index -= 1) {
    if (events[index].status === "running" && events[index].title) return events[index].title;
  }
  return "";
}

async function pushPluginTurnProgress(run: PluginTurnRun) {
  if (run.finished || !run.messageId) return;
  const text = redactExternalReply(conversationReplyContent(run.conversationId, run.messageId));
  const status = run.approvalSummary ? "awaiting_approval" : "running";
  const activity = run.approvalSummary
    ? t("ai.external.approvalActivity", { summary: run.approvalSummary })
    : runningEventTitle(run.conversationId);
  const approval = status === "awaiting_approval" && run.chatApproval ? run.approval : undefined;
  if (text === run.lastText && status === run.lastStatus && activity === run.lastActivity && approval?.id === run.lastApprovalId) return;
  run.lastText = text;
  run.lastStatus = status;
  run.lastActivity = activity;
  run.lastApprovalId = approval?.id;
  await updatePluginAiTurn({ id: run.turnId, status, text, activity, conversationId: run.conversationId, ...(approval ? { approval } : {}) }).catch(() => undefined);
}

async function finishPluginTurn(run: PluginTurnRun, status: "completed" | "failed" | "cancelled", error = "") {
  if (run.finished) return;
  run.finished = true;
  if (run.timer) clearInterval(run.timer);
  if (pluginTurnByConversation.get(run.conversationId) === run.turnId) pluginTurnByConversation.delete(run.conversationId);
  pluginTurnRuns.delete(run.turnId);
  const text = run.messageId ? redactExternalReply(conversationReplyContent(run.conversationId, run.messageId)) : "";
  await updatePluginAiTurn({ id: run.turnId, status, text, error, conversationId: run.conversationId }).catch(() => undefined);
}

async function finishPluginTurnForConversation(conversationId: string) {
  const run = activePluginTurn(conversationId);
  if (!run) return;
  const conversation = assistant.conversationById(conversationId);
  const status = pluginTurnStatus(conversation?.status ?? "interrupted", run.cancelled);
  await finishPluginTurn(run, status, status === "failed" ? t("ai.external.failed") : "");
}

function externalConversationFor(request: PluginAiTurnRequest) {
  return assistant.conversations.find((item) => matchesExternalConversation(item, request))
    ?? assistant.conversations.find((item) => matchesExternalConversation(item, { ...request, conversationId: undefined }));
}

/** A chat decision from the plugin; only the approval currently exposed to it is accepted. */
function applyPluginTurnApproval(decision: { id: string; approvalId: string; approved: boolean }) {
  const run = pluginTurnRuns.get(decision.id);
  if (!run || run.finished || !run.chatApproval || run.approval?.id !== decision.approvalId) return;
  resolveToolApproval(decision.approvalId, decision.approved ? { scope: "once" } : {});
}

async function createExternalConversation(request: PluginAiTurnRequest) {
  const previousId = assistant.activeConversationId;
  const conversation = assistant.createConversation("agentic");
  conversation.external = {
    kind: "plugin",
    pluginId: request.pluginId,
    pluginName: request.pluginName,
    key: request.conversationKey,
    ...(request.workspaceAccess ? { workspace: true } : {}),
  };
  assistant.setConversationWorkspace(conversation.id, {
    workspaceId: conversation.workspaceId ?? workbench.activeWorkspaceId,
    scopeId: conversation.workspaceScopeId ?? assistant.activeWorkspaceScopeId,
    projectRoot: request.workspaceAccess ? conversation.projectRoot ?? "" : "",
  });
  assistant.renameConversation(conversation.id, request.title);
  // A relayed message must not pull the local user away from what they are doing.
  if (previousId && previousId !== conversation.id) await assistant.switchConversation(previousId);
  return conversation;
}

async function consumePluginAiTurns() {
  if (processingPluginTurns || isPopupWindow()) return;
  processingPluginTurns = true;
  try {
    for (const turnId of takePendingPluginAiTurns()) {
      let request: PluginAiTurnRequest;
      try {
        request = await claimPluginAiTurn(turnId);
      } catch {
        continue; // Already claimed, cancelled or expired.
      }
      try {
        const conversation = externalConversationFor(request) ?? await createExternalConversation(request);
        if (!(await assistant.ensureConversationDetails(conversation.id))) throw new Error(t("ai.external.notStarted"));
        const queuedMessageId = assistant.enqueueMessage(conversation.id, {
          content: externalTurnUserMessage(request),
          createdAt: request.createdAt || Date.now(),
          pluginTurn: { turnId },
        });
        if (!queuedMessageId) throw new Error(t("ai.external.notStarted"));
        pluginTurnRuns.set(turnId, {
          turnId,
          conversationId: conversation.id,
          queuedMessageId,
          instructions: request.instructions ?? "",
          allowedPlugins: Array.isArray(request.allowedPlugins) ? [...request.allowedPlugins] : null,
          maxRisk: request.maxRisk ?? "",
          chatApproval: request.chatApproval === true,
          history: pluginTurnHistory(request.history),
          cancelled: false,
          finished: false,
          lastText: "",
          lastStatus: "",
          lastActivity: "",
        });
        await updatePluginAiTurn({ id: turnId, status: "running", conversationId: conversation.id }).catch(() => undefined);
        await assistant.flushPersistence();
        if (!conversation.running) {
          void runNextQueuedMessage(conversation.id).catch((error) => ElMessage.error(nativeErrorMessage(error)));
        }
      } catch (error) {
        await updatePluginAiTurn({ id: turnId, status: "failed", error: nativeErrorMessage(error) }).catch(() => undefined);
      }
    }
  } finally {
    processingPluginTurns = false;
  }
}

async function cancelPluginAiTurn(turnId: string) {
  const run = pluginTurnRuns.get(turnId);
  if (!run || run.finished) return;
  run.cancelled = true;
  if (!run.messageId) {
    // Still queued behind another message: drop it before it starts.
    assistant.removeQueuedMessage(run.conversationId, run.queuedMessageId);
    await finishPluginTurn(run, "cancelled");
    return;
  }
  await cancelConversation(run.conversationId, { resumeQueue: true });
}

function conversationReplyContent(conversationId: string, messageId: string) {
  const conversation = assistant.conversationById(conversationId);
  if (!conversation) return "";
  const startIndex = conversation.messages.findIndex((message) => message.id === messageId);
  return conversation.messages
    .slice(startIndex + 1)
    .filter((message) => message.role === "assistant")
    .map((message) => message.content.trim())
    .filter(Boolean)
    .join("\n\n");
}

async function runNextQueuedMessage(conversationId: string) {
  await runNextConversationQueueItem(
    conversationId,
    (id) => assistant.dequeueMessage(id),
    sendQueuedMessage,
    (id) => !agenticSendGate.isClaimed(id) && !acpSendGate.isClaimed(id) && !agentStartPromises.has(id),
  );
}
function waitForConversationStop(conversationId: string, timeoutMs: number) {
  return new Promise<boolean>((resolve) => {
    const startedAt = Date.now();
    const check = () => {
      const running = progress.activeRuns.some((run) => run.navigation.type === "ai-conversation"
        && run.navigation.conversationId === conversationId);
      if (!running && !assistant.conversationById(conversationId)?.running && !executingParallelChildren.has(conversationId)) return resolve(true);
      if (Date.now() - startedAt >= timeoutMs) return resolve(false);
      window.setTimeout(check, 50);
    };
    check();
  });
}

async function recoverStalledQueueInterruption(conversationId: string, runQueue = true) {
  const conversation = assistant.conversationById(conversationId);
  if (!conversation?.running) return;

  // 控制器可能因窗口/组件重建而丢失；仍需使用持久化 ACP 会话取消后端任务并收口运行状态。
  agenticAborts.get(conversationId)?.abort();
  acpPromptAborts.get(conversationId)?.abort();
  agenticAborts.delete(conversationId);
  acpPromptAborts.delete(conversationId);
  const prompt = activeAcpPrompts.get(conversationId) ?? acpSessionTarget(conversation);
  activeAcpPrompts.delete(conversationId);
  markAcpSessionStopped(prompt);
  if (prompt) void invokeBackend("acp_cancel", prompt).catch(() => {});

  toolApprovalQueue.rejectConversation(conversationId);
  agenticSendGate.release(conversationId);
  acpSendGate.release(conversationId);
  assistant.settleRunningAgentEvents(conversationId, "cancelled");
  assistant.setConversationStatus(conversationId, "interrupted");
  await reportConversationRun(conversationId, "cancelled", t("ai.taskProgress.cancelled"));
  resumeQueueAfterCancellation.delete(conversationId);
  if (runQueue) {
    await runNextQueuedMessage(conversationId);
  }
}

async function interruptConversationAndRunQueue(conversationId: string) {
  const existing = queueInterruptions.get(conversationId);
  if (existing) return existing;

  const interruption = (async () => {
    const cancelled = await cancelConversation(conversationId, { resumeQueue: true });
    if (cancelled) ElMessage.info(t("ai.queue.interrupting"));
    const stopped = cancelled
      ? await waitForConversationStop(conversationId, QUEUE_INTERRUPT_GRACE_MS)
      : false;
    if (!stopped) await recoverStalledQueueInterruption(conversationId);
  })().finally(() => {
    queueInterruptions.delete(conversationId);
  });
  queueInterruptions.set(conversationId, interruption);
  return interruption;
}

async function sendQueuedMessageNow(id: string) {
  const conversationId = assistant.activeConversationId;
  const conversation = assistant.activeConversation;
  if (!conversation || !assistant.prioritizeQueuedMessage(conversationId, id)) return;
  if (editingQueueId.value === id) cancelQueueEdit();
  if (conversation.running) {
    await interruptConversationAndRunQueue(conversationId);
    return;
  }
  await runNextQueuedMessage(conversationId);
}
function editQueuedMessage(id: string) {
  const msg = assistant.activeQueue.find((m) => m.id === id);
  if (!msg) return;
  editingMessageId.value = undefined;
  editingQueueId.value = id;
  input.value = msg.content;
  attachments.value = msg.attachments ? msg.attachments.map((a) => ({ ...a })) : [];
}
function deleteQueuedMessage(id: string) {
  assistant.removeQueuedMessage(assistant.activeConversationId, id);
  if (editingQueueId.value === id) {
    editingQueueId.value = undefined;
    input.value = "";
    attachments.value = [];
  }
}
function updateQueuedMessageFromComposer() {
  const id = editingQueueId.value;
  if (!id) return;
  const content = input.value.trim();
  const selected = sanitizeAiAttachments(attachments.value);
  const conversationId = assistant.activeConversationId;
  if (!content && !selected.length) {
    deleteQueuedMessage(id);
    return;
  }
  editingQueueId.value = undefined;
  if (!assistant.updateQueuedMessage(conversationId, id, { content, attachments: selected })) {
    // 编辑期间该消息已出队发送：保留输入框内容，让用户决定是否作为新消息发送。
    ElMessage.warning(t("ai.queue.editTargetGone"));
    return;
  }
  input.value = "";
  attachments.value = [];
}
function cancelQueueEdit() {
  editingQueueId.value = undefined;
  input.value = "";
  attachments.value = [];
}
// 切换会话或队列项已被消费时退出编辑态，避免把修改写到别的会话或已不存在的队列项。
// 草稿按会话保存：这里只退出编辑态，不能清空切换后新会话的输入框。
watch(() => assistant.activeConversationId, () => {
  editingQueueId.value = undefined;
});
watch(
  () => assistant.activeQueue.map((message) => message.id),
  (ids) => {
    if (editingQueueId.value && !ids.includes(editingQueueId.value)) editingQueueId.value = undefined;
  },
);

function closeDefaultDirectoryAgents(event?: PageTransitionEvent) {
  if (event?.persisted || acpDisposed) return;
  acpDisposed = true;
  const servers = new Set([...acpStartingServers.values()].map(({ serverId }) => serverId));
  acpStartingServers.clear();
  for (const [id, target] of acpRunTargets) {
    if (target.directoryKind === "project") continue;
    servers.add(target.serverId);
    releaseAcpRun(id);
    acpPromptAborts.get(id)?.abort();
    if (assistant.conversationById(id)?.running) {
      assistant.settleRunningAgentEvents(id, "cancelled");
      assistant.setConversationStatus(id, "interrupted");
      void reportConversationRun(id, "cancelled", t("ai.taskProgress.cancelled"));
    }
  }
  for (const serverId of servers) void invokeBackend("acp_disconnect", { serverId }).catch(() => {});
}

function stopAllAgents() {
  closeDefaultDirectoryAgents();
  for (const abortController of agenticAborts.values()) abortController.abort();
  agenticAborts.clear();
  subagentParentAborts.clear();
  subagentUsageCollectors.clear();
  for (const abortController of acpPromptAborts.values()) abortController.abort();
  acpPromptAborts.clear();

  const prompts = [...activeAcpPrompts.values()];
  activeAcpPrompts.clear();
  for (const prompt of prompts) {
    markAcpSessionStopped(prompt);
    void invokeBackend("acp_cancel", prompt).catch(() => {});
  }
}

watch(workspaceRoot, () => {
  invalidateAiNativeResources();
  void syncAiNativeResources();
}, { immediate: true });
const layoutToggleLabel = computed(() =>
  assistant.layoutMode === "docked" ? t("ai.layout.switchToOverlay") : t("ai.layout.switchToDocked"),
);
function toggleLayoutMode() {
  assistant.setLayoutMode(assistant.layoutMode === "docked" ? "overlay" : "docked");
}
function updateDrawerWidth(value: number) {
  if (assistant.fullscreen) return;
  assistant.setWidth(value);
}

function persistDrawerWidth(value: number) {
  if (assistant.fullscreen) return;
  assistant.setWidth(value);
  assistant.persistWidth();
}

function updateConversationSidebarWidth(value: number) {
  assistant.setConversationSidebarWidth(value);
}

function persistConversationSidebarWidth(value: number) {
  assistant.setConversationSidebarWidth(value);
  assistant.persistConversationSidebarWidth();
}

function dismissAgentEvent(event: AiAssistantEvent) {
  assistant.removeAgentEventForConversation(assistant.activeConversationId, event);
}
function openSettings() {
  assistant.close();
  emit("open-settings");
}

/** ACP 会话更新统一入口：事件通道与 Streams 通道共用同一处理逻辑。 */
const ACP_TURN_UPDATE_KINDS = new Set([
  "agent_message_chunk",
  "agent_thought_chunk",
  "tool_call",
  "tool_call_update",
  "plan",
]);
async function handleAcpSessionUpdate(payload: AcpSessionUpdatePayload) {
  const conversation = findConversationByAcpSession(payload.serverId, payload.sessionId);
  if (!conversation) return;
  const conversationId = conversation.id;
  // Codex ACP intentionally streams its prior transcript while LoadSession
  // runs. Our store already restores that transcript from persistence, so
  // accepting replay events here would append the same answer and activity again.
  const hasPersistedTranscript = conversation.messages.some(
    (message) => message.role === "user" || message.role === "assistant",
  );
  if (payload.replay && hasPersistedTranscript) return;

  const update = asRecord(payload.update);
  const kind = String(update.sessionUpdate ?? "");
  if (!payload.replay && ACP_TURN_UPDATE_KINDS.has(kind)) {
    // Drop turn output that arrives after the user stopped the session, or that belongs to a
    // session other than the one the conversation is currently prompting.
    if (stoppedAcpSessions.has(acpSessionKey(payload))) return;
    const activePrompt = activeAcpPrompts.get(conversationId);
    if (activePrompt && acpSessionKey(activePrompt) !== acpSessionKey(payload)) return;
  }
  if (kind !== "agent_message_chunk" && kind !== "agent_thought_chunk") {
    acpStreamTextBatcher.flush();
  }
  if (kind === "agent_message_chunk") {
    const text = contentText(update.content);
    const messageId = acpTextSegments.messageIdFor(
      conversationId,
      typeof update.messageId === "string" ? update.messageId : "",
      acpCurrentMessageIds.get(conversationId),
    );
    acpCurrentMessageIds.set(conversationId, messageId);
    acpStreamTextBatcher.append(
      [conversationId, "message", messageId].join(ACP_STREAM_KEY_SEPARATOR),
      text,
    );
  } else if (kind === "agent_thought_chunk") {
    const messageId = String(update.messageId ?? "thought");
    acpStreamTextBatcher.append(
      [conversationId, "thought", messageId].join(ACP_STREAM_KEY_SEPARATOR),
      contentText(update.content),
    );
  } else if (kind === "tool_call" || kind === "tool_call_update") {
    assistant.settleRunningAgentEvents(conversationId, "completed", "thought");
    const id = String(update.toolCallId ?? createId("tool"));
    // Text after a new tool call starts its own message; status updates of a known tool do not
    // split the sentence being streamed.
    if (!acpEventMessageIds.get(conversationId)?.has(id)) acpTextSegments.breakAfterTool(conversationId);
    const triggerMessageId = acpEventAnchor(conversationId, id);
    assistant.upsertAgentEventForConversation(conversationId, {
      id,
      kind: "tool",
      title: String(update.title ?? t("ai.toolCall")),
      detail: eventDetail(update),
      status: typeof update.status === "string" ? update.status : undefined,
      toolName: typeof update.toolName === "string"
        ? update.toolName
        : typeof update.name === "string" ? update.name : undefined,
      toolInput: typeof update.arguments === "string"
        ? update.arguments
        : update.input !== undefined ? summarizeToolApprovalArguments(update.input) : undefined,
      triggerMessageId,
    });
  } else if (kind === "plan") {
    const triggerMessageId = acpEventAnchor(conversationId, "plan");
    assistant.upsertAgentEventForConversation(conversationId, {
      id: "plan",
      kind: "plan",
      title: t("ai.plan"),
      detail: planEventDetail(update),
      triggerMessageId,
    });
  } else if (kind === "current_mode_update") {
    const triggerMessageId = acpEventAnchor(conversationId, "mode");
    assistant.upsertAgentEventForConversation(conversationId, {
      id: "mode",
      kind: "mode",
      title: t("ai.mode"),
      detail: String(update.currentModeId ?? ""),
      triggerMessageId,
    });
  } else if (kind === "config_option_update") {
    acpConfigOptions.value = {
      ...acpConfigOptions.value,
      [conversationId]: parseAcpConfigSelectOptions(update.configOptions),
    };
  }
  await scrollToBottomIfFollowing(conversationId);
}

function selectionElement(node: Node | null) {
  if (node instanceof Element) return node;
  return node?.parentElement ?? null;
}

/** The selected text when the selection lies inside one message body (ai.message.selection). */
function selectedMessageText(): string {
  const selection = window.getSelection();
  if (!selection || selection.rangeCount === 0 || selection.isCollapsed) return "";
  const anchorBody = selectionElement(selection.anchorNode)?.closest(".ai-message-card__content");
  const focusBody = selectionElement(selection.focusNode)?.closest(".ai-message-card__content");
  if (!anchorBody || anchorBody !== focusBody) return "";
  return selection.toString();
}

async function copySelectedMessage() {
  const selectedText = selectedMessageText();
  if (!selectedText) return;
  try {
    await copyText(selectedText);
  } catch (error) {
    ElMessage.error(nativeErrorMessage(error));
  }
}

const keybindingDisposers = [
  registerKeybindingContext("ai.running", () => assistant.loading),
  registerKeybindingContext("ai.conversations.global", () => conversationPreferences.globalMode),
  registerKeybindingContext("ai.multiline.field", (target) =>
    typeof HTMLTextAreaElement !== "undefined" && target instanceof HTMLTextAreaElement && target.value.includes("\n")),
  registerKeybindingContext("ai.message.selection", () => Boolean(selectedMessageText())),
  ...([
    ["ai:stop", cancelCurrent],
    ["ai:new-conversation", focusNewConversation],
    ["ai:search-conversations", openConversationSearch],
    ["ai:previous-conversation", () => switchAdjacentConversation(-1)],
    ["ai:next-conversation", () => switchAdjacentConversation(1)],
    ["ai:copy-message", copySelectedMessage],
  ] as const).map(([id, execute]) =>
    registerCommand({
      id,
      source: "builtin",
      title: () => t(`keybindings.command.${id}`),
      showInPalette: false,
      execute,
    })),
];

function markVisibleConversationRead() {
  assistant.markConversationRead(assistant.activeConversationId);
}

onMounted(() => {
  window.addEventListener("pagehide", closeDefaultDirectoryAgents);
  window.addEventListener("focus", markVisibleConversationRead);
  document.addEventListener("visibilitychange", markVisibleConversationRead);
  if (!isPopupWindow()) {
    stopPluginAiTurns = subscribePluginAiTurns(() => void consumePluginAiTurns());
    stopPluginAiTurnCancellations = subscribePluginAiTurnCancellations((id) => void cancelPluginAiTurn(id));
    stopPluginAiTurnApprovals = subscribePluginAiTurnApprovals(applyPluginTurnApproval);
    void refreshPendingPluginAiTurns().catch(() => false).finally(() => void consumePluginAiTurns());
  }
  // Native drops reach the main window only; a popped-out copy of the drawer ignores them.
  if (!isPopupWindow()) {
    stopNativeFileDrop = listenAppEvent<{ files?: string[]; targetId?: string }>("native-files-dropped", (event) => {
      if (event?.targetId === AI_ASSISTANT_DROP_TARGET_ID && Array.isArray(event.files)) void addDroppedFiles(event.files);
    });
  }
  stopRunCancelRequested = listenAgentRunCancelRequested(({ runId }) => {
    const parentController = subagentParentAborts.get(runId);
    if (parentController) {
      parentController.abort();
      return;
    }
    const match = [...activeRunIds.entries()].find(([, activeRunId]) => activeRunId === runId);
    if (match) void cancelConversation(match[0]);
  });
  stopPermissionRequest = listenAppEvent<AiAssistantPermission>(
    "acp:permission-request",
    (payload) => void handleProtocolPermissionRequest(payload),
  );
  if (isStreamAvailable()) {
    // ACP 暂无历史帧快照；显式确认订阅并在断线后恢复后续增量消费。
    stopSessionUpdate = listenResilientJSONStream<{ event?: string; payload?: unknown }>({
      name: "acp-events",
      isSubscribed: (value) => Boolean(
        value && typeof value === "object" && "subscribed" in value && value.subscribed,
      ),
      onMessage: (frame) => {
        if (frame?.event === "acp:session-update") {
          void handleAcpSessionUpdate(frame.payload as AcpSessionUpdatePayload);
        }
      },
    });
  } else {
    stopSessionUpdate = listenAppEvent<AcpSessionUpdatePayload>(
      "acp:session-update",
      (payload) => void handleAcpSessionUpdate(payload),
    );
  }
});
onBeforeUnmount(() => {
  window.removeEventListener("pagehide", closeDefaultDirectoryAgents);
  window.removeEventListener("focus", markVisibleConversationRead);
  for (const dispose of keybindingDisposers) dispose();
  document.removeEventListener("visibilitychange", markVisibleConversationRead);
  cancelPromptOptimization();
  unregisterNativeAiCapabilities();
  stopCapabilityUpdates();
  stopToolApprovalUpdates();
  stopGlobalToolAuthorizationUpdates();
  toolApprovalQueue.dispose();
  stopSessionUpdate?.();
  stopPermissionRequest?.();
  stopNativeFileDrop?.();
  stopPluginAiTurns?.();
  stopPluginAiTurnCancellations?.();
  stopPluginAiTurnApprovals?.();
  for (const run of pluginTurnRuns.values()) if (run.timer) clearInterval(run.timer);
  stopRunCancelRequested?.();
  stopAllAgents();
  resumeQueueAfterCancellation.clear();
  agenticSendGate.clear();
  acpSendGate.clear();
  acpStreamTextBatcher.dispose({ flush: true });
  acpCurrentMessageIds.clear();
  acpEventMessageIds.clear();
  conversationWorkspaceContexts.clear();
  aiTerminalSessions.clear();
  if (aiNativeResourceSyncTimer) {
    clearTimeout(aiNativeResourceSyncTimer);
    aiNativeResourceSyncTimer = undefined;
  }
  activeRunIds.clear();
  activeRunPhases.clear();
  activeRunSummaries.clear();
  failedRunUpdateKeys.clear();
  dismissPendingPermission();
  if (messageNavigatorFrame && typeof window !== "undefined") {
    window.cancelAnimationFrame(messageNavigatorFrame);
    messageNavigatorFrame = 0;
  }
  if (isPopupWindow() && isDesktopRuntime()) {
    void emitAppEvent(POPUP_TAB_STATE_EVENT, {
      type: "ai-assistant",
      windowLabel: popupWindowLabel(),
      snapshot: assistant.snapshot(),
    });
  }
});
</script>

<template>
  <Transition name="ai-drawer">
    <AppSplitPane
      v-if="assistant.open || tabSurfaceActive"
      class="ai-assistant-drawer"
      :class="{
        'is-fullscreen': shellFullscreen,
        'is-tab-surface': tabSurfaceActive,
        'is-popup-window': isPopupWindow(),
        'is-docked': !tabSurfaceActive && assistant.layoutMode === 'docked',
      }"
      side="second"
      :model-value="shellFullscreen ? 100 : assistant.width"
      :unit="shellFullscreen ? 'percent' : 'px'"
      :min="shellFullscreen ? 100 : 340"
      :max="shellFullscreen ? 100 : (width) => Math.max(340, width * 0.72)"
      :resizable="!shellFullscreen"
      :aria-label="t('ai.resizeDrawer')"
      @resize="updateDrawerWidth"
      @resize-end="persistDrawerWidth"
    >
      <template #first>
        <div class="ai-assistant-drawer__workspace" aria-hidden="true" />
      </template>
      <template #second>
        <aside
          :id="AI_ASSISTANT_DROP_TARGET_ID"
          ref="drawerPanel"
          class="ai-assistant-drawer__panel"
          data-file-drop-target
          :data-file-drop-label="t('ai.attachment.dropHint')"
        >
      <p class="sr-only" role="status" aria-live="polite">{{ liveAnnouncement }}</p>
      <header class="ai-assistant-header">
        <el-segmented
          class="ai-assistant-header__modes"
          size="small"
          v-model="assistant.mode"
          :disabled="activeParallelDelegationPending"
          :options="[
            { label: t('ai.chat'), value: 'agentic' },
            { label: t('ai.agent'), value: 'agent' },
          ]"
        />
        <el-button
          v-if="isDesktopRuntime() && assistant.activeConversation && !assistant.activeConversation.external"
          text
          size="small"
          :icon="FolderOpened"
          :disabled="!canChooseWorkingDirectory()"
          :loading="choosingWorkingDirectory"
          :title="workspaceRoot()"
          @click="chooseWorkingDirectory"
        >{{ t("project.chooseFolder") }}</el-button>
        <div v-if="conversationPreferences.globalMode" class="ai-assistant-workspace-context">
          <span class="ai-assistant-workspace-context__icon" aria-hidden="true">
            <el-icon><FolderOpened /></el-icon>
          </span>
          <span class="ai-assistant-workspace-context__label">
            {{ t("ai.conversation.workspace.contextLabel") }}
          </span>
          <strong class="ai-assistant-workspace-context__name" :title="activeConversationWorkspaceName">
            {{ activeConversationWorkspaceName }}
          </strong>
          <el-tag v-if="!activeConversationWorkspaceAvailable" type="warning" size="small">
            {{ t("ai.conversation.workspace.unavailable") }}
          </el-tag>
          <el-button
            v-if="!activeConversationWorkspaceAvailable && assistant.activeConversation"
            text
            size="small"
            @click="relinkConversationToTopWorkspace(assistant.activeConversation)"
          >
            {{ t("ai.conversation.workspace.relink") }}
          </el-button>
          <el-button
            v-else-if="assistant.activeConversation?.workspaceId !== workbench.activeWorkspaceId"
            text
            size="small"
            @click="assistant.activeConversation && switchTopWorkspaceForConversation(assistant.activeConversation)"
          >
            {{ t("ai.conversation.workspace.switch") }}
          </el-button>
        </div>
        <span class="ai-assistant-header__spacer" aria-hidden="true" />
        <div class="ai-assistant-header__actions">
          <!-- 新建会话放在工具按钮组最前面：列表收起时也可用，且不会被看成模式切换的一部分。 -->
          <el-tooltip :content="t('ai.conversation.new')" placement="bottom">
            <el-button
              class="ai-assistant-header__new-conversation"
              text
              :icon="Plus"
              :aria-label="t('ai.conversation.new')"
              @click="createConversation"
            />
          </el-tooltip>
          <AiConversationSidebarPosition v-if="tabSurfaceActive" compact />
          <el-tooltip
            :content="conversationSidebarOpen ? t('ai.conversation.collapse') : t('ai.conversation.expand')"
            placement="bottom"
          >
            <el-button
              text
              :icon="conversationSidebarOpen ? Fold : Expand"
              :aria-label="conversationSidebarOpen ? t('ai.conversation.collapse') : t('ai.conversation.expand')"
              @click="conversationSidebarOpen = !conversationSidebarOpen"
            />
          </el-tooltip>
          <el-tooltip
            v-if="!isPopupWindow() && !shellFullscreen"
            :content="layoutToggleLabel"
            placement="bottom"
          >
            <el-button
              class="ai-assistant-header__layout-toggle"
              :class="{ 'is-active': assistant.layoutMode === 'docked' }"
              text
              :icon="assistant.layoutMode === 'docked' ? CopyDocument : Grid"
              :aria-label="layoutToggleLabel"
              @click="toggleLayoutMode"
            />
          </el-tooltip>
          <el-tooltip :content="t('settings.ai')" placement="bottom">
            <el-button text :icon="Setting" :aria-label="t('settings.ai')" @click="openSettings" />
          </el-tooltip>
          <el-tooltip :content="t('tab.popout')" placement="bottom">
            <el-button text :icon="TopRight" :aria-label="t('tab.popout')" @click="popOutWindow" />
          </el-tooltip>
          <el-tooltip
            v-if="!tabSurfaceActive"
            :content="assistant.fullscreen ? t('common.minimize') : t('common.maximize')"
            placement="bottom"
          >
            <el-button
              text
              :icon="assistant.fullscreen ? Minus : FullScreen"
              :aria-label="assistant.fullscreen ? t('common.minimize') : t('common.maximize')"
              @click="assistant.fullscreen = !assistant.fullscreen"
            />
          </el-tooltip>
          <el-tooltip v-if="!tabSurfaceActive" :content="t('common.close')" placement="bottom">
            <el-button
              class="ai-assistant-header__close"
              text
              circle
              :icon="Close"
              :aria-label="t('common.close')"
              @click="closeAssistant"
            />
          </el-tooltip>
        </div>
      </header>
      <AppSplitPane
        class="ai-assistant-body"
        :class="{ 'has-left-conversations': conversationListOnLeft }"
        :reverse="conversationListOnLeft"
        side="second"
        :model-value="assistant.conversationSidebarWidth"
        :min="AI_CONVERSATION_SIDEBAR_MIN_WIDTH"
        :max="AI_CONVERSATION_SIDEBAR_MAX_WIDTH"
        :second-min="AI_CONVERSATION_CONTENT_MIN_WIDTH"
        :collapsed="!conversationSidebarOpen"
        :resizable="conversationSidebarOpen"
        :aria-label="t('ai.conversation.resizeSidebar')"
        @resize="updateConversationSidebarWidth"
        @resize-end="persistConversationSidebarWidth"
      >
        <template #first>
          <main class="ai-assistant-content">
          <div
            class="ai-assistant-message-stage"
            :class="{ 'has-message-navigator': messageNavigatorItems.length }"
          >
            <el-scrollbar
              ref="messageScrollbar"
              class="ai-assistant-messages"
              :native="false"
              :always="false"
              @scroll="onMessagesScroll"
            >
              <div v-if="delegationParent" class="ai-delegation-parent">
                <el-button text size="small" :icon="ArrowLeft" :aria-label="t('ai.delegation.parent')"
                  @click="switchConversation(delegationParent.id)">
                  {{ delegationParent.title || t('ai.conversation.untitled') }}
                </el-button>
              </div>
                                                                      <AiMessageThread
                :key="assistant.activeConversationId"
                ref="messageThread"
                :messages="assistant.messages"
                :events="visibleAgentEvents"
                :approvals="activeToolApprovals"
                :approval-labels="{
                  title: t('ai.agentic.inlineApprovalTitle'),
                  tool: t('ai.agentic.approvalTool'),
                  resource: t('ai.agentic.approvalResource'),
                  summary: t('ai.agentic.approvalSummary'),
                  parameters: t('ai.agentic.approvalParameters'),
                  risk: {
                    read: t('ai.agentic.risk.read'),
                    write: t('ai.agentic.risk.write'),
                    network: t('ai.agentic.risk.network'),
                    dangerous: t('ai.agentic.risk.dangerous'),
                  },
                  allowOnce: t('ai.agentic.allowOnce'),
                  allowSession: t('ai.agentic.allowSession'),
                  allowSessionSameParameters: t('ai.agentic.allowSessionSameCall'),
                  allowGlobal: t('ai.agentic.allowGlobal'),
                  reject: t('ai.agentic.reject'),
                  feedbackPlaceholder: t('ai.agentic.approvalFeedbackPlaceholder'),
                  rejectWithFeedback: t('ai.agentic.rejectWithFeedback'),
                }"
                :mode="assistant.mode"
                :title="assistant.mode === 'agent' ? t('ai.agent') : t('ai.chat')"
                :hint="assistant.mode === 'agent' ? t('ai.drawerHint') : browserLocalAi ? t('ai.agentic.browserHint') : t('ai.agentic.hint')"
                :thought-label="t('ai.thought')"
                :tool-label="t('ai.toolCall')"
                :plan-label="t('ai.plan')"
                :mode-label="t('ai.mode')"
                :activity-label="t('ai.activity')"
                :activity-labels="activityLabels"
                :task-steps-label="t('ai.taskSteps')"
                :dismiss-task-steps-label="t('ai.taskSteps.dismiss')"
                :sources-label="t('ai.sources')"
                :artifacts-label="t('ai.artifacts')"
                :preview-label="t('ai.artifact.preview')"
                :collapse-label="t('ai.artifact.collapse')"
                :copy-label="t('ai.artifact.copy')"
                :copied-label="t('ai.artifact.copied')"
                :copy-result-label="t('ai.activity.copyResult')"
                :copied-result-label="t('ai.activity.copiedResult')"
                :copy-result-failed-label="t('ai.activity.copyResultFailed', { message: '{message}' })"
                :event-status-labels="eventStatusLabels"
                :task-step-status-labels="taskStepStatusLabels"
                :empty-message-label="t('ai.message.empty')"
                :metadata-label="t('ai.message.metadata')"
                :expand-message-label="t('ai.message.expand')"
                :collapse-message-label="t('ai.message.collapse')"
                :copy-message-label="t('ai.message.copy')"
                :copied-message-label="t('ai.message.copied')"
                                                :copy-message-failed-label="t('ai.message.copyFailed', { message: '{message}' })"
                :copy-image-label="t('ai.image.copy')"
                :copied-image-label="t('ai.image.copied')"
                :copy-image-failed-label="t('ai.image.copyFailed', { message: '{message}' })"
                :task-summary-title="t('ai.taskSummary.title')"
                :task-summary-status="t('ai.taskSummary.completed')"
                :running="assistant.loading"
                :running-label="runStatusText"
                :generating="assistant.loading"
                :event-group-count-labels="{
                  succeeded: t('ai.eventGroup.succeeded', { count: '{count}' }),
                  failed: t('ai.eventGroup.failed', { count: '{count}' }),
                  running: t('ai.eventGroup.running', { count: '{count}' }),
                }"
                :start-label="t('ai.startAgent')"
                :default-directory-action-label="unavailableAcpDirectories.has(assistant.activeConversationId) && !assistant.activeConversation?.external ? t('ai.localAgent.useDefaultOnce') : undefined"
                :effective-directory="activeDefaultAcpDirectory"
                :effective-directory-label="activeDefaultAcpDirectory ? t('ai.localAgent.runningIn', { path: activeDefaultAcpDirectory }) : undefined"
                :stop-agent-label="t('ai.localAgent.stop')"
                :starting="assistant.agentStarting"
                :failed="assistant.activeConversation?.status === 'failed'"
                :edit-failed-label="t('ai.message.editFailed')"
                :can-rewind="canRewindActiveConversation"
                :edit-message-label="t('ai.message.edit')"
                :regenerate-label="t('ai.message.regenerate')"
                :error-labels="{
                  title: t('ai.errorCard.title'),
                  hint: t('ai.errorCard.hint'),
                  retry: t('ai.errorCard.retry'),
                  settings: t('ai.errorCard.settings'),
                }"
                @edit-failed="editSentMessage"
                @regenerate="resendTurnOf"
                @retry="resendTurnOf"
                :can-continue="canContinueActiveConversation"
                :continue-label="t('ai.agentic.continue')"
                @continue="continueAfterLoopLimit"
                @open-settings="emit('open-settings')"
                :load-earlier-label="t('ai.loadEarlierMessages')"
                @load-earlier="preserveScrollAfterLoadingEarlier"
                @start="startAgent()"
                @start-in-default-directory="startAgent(assistant.activeConversationId, true)"
                @stop-agent="stopDefaultDirectoryAgent(assistant.activeConversationId)"
                @decide-approval="resolveToolApproval"
                @dismiss-event="dismissAgentEvent"
              />
              <section v-if="delegatedChildren.length" class="ai-delegation-reports" :aria-label="t('ai.delegation.reports')">
                <header class="ai-delegation-reports__header">
                  <h3><el-icon aria-hidden="true"><Share /></el-icon>{{ t('ai.delegation.reports') }}</h3>
                  <span>{{ t('ai.delegation.reportProgress', { settled: settledDelegations, total: delegatedChildren.length }) }}</span>
                </header>
                <div v-for="child in delegatedChildren" :key="child.id" class="ai-delegation-reports__row">
                  <details class="ai-delegation-reports__item" :open="openDelegationReportId === child.id">
                    <summary @click.prevent="toggleDelegationReport(child.id)">
                      <strong :title="child.title">{{ child.title }}</strong>
                      <span v-if="child.selectedModel" class="ai-delegation-reports__model">
                        {{ availableModels.find((model) => model.value === child.selectedModel)?.label }}
                      </span>
                      <span class="ai-delegation-reports__status" :class="`is-${delegationStatusTag(child.delegation?.resultStatus ?? child.status)}`">
                        {{ conversationSidebarLabels.statusLabels[child.delegation?.resultStatus ?? child.status] }}
                      </span>
                    </summary>
                    <div v-if="openDelegationReportId === child.id" class="ai-delegation-reports__result">
                      <el-scrollbar v-if="child.delegation?.result" max-height="260px">
                        <MarkdownPreview inline code-copy :content="child.delegation.result" />
                      </el-scrollbar>
                      <span v-else class="ai-delegation-reports__empty">
                        {{ conversationSidebarLabels.statusLabels[child.delegation?.resultStatus ?? child.status] }}
                      </span>
                    </div>
                  </details>
                  <el-button text size="small" :icon="TopRight" :title="t('ai.delegation.open')"
                    :aria-label="t('ai.delegation.open')" @click="switchConversation(child.id)" />
                </div>
              </section>
              <p v-if="activeParallelDelegationPending" class="ai-delegation-pending" role="status">
                {{ t('ai.delegation.running') }}
              </p>
            </el-scrollbar>
            <Transition name="ai-scroll-bottom">
              <el-button
                v-if="showScrollToBottom"
                class="ai-assistant-scroll-bottom"
                :icon="Bottom"
                round
                :aria-label="t('ai.scrollToBottom')"
                @click="returnToMessageBottom"
              >
                {{ t("ai.scrollToBottom") }}
              </el-button>
            </Transition>
            <AiMessageNavigator
              :items="messageNavigatorItems"
              :active-id="activeMessageNavigatorId"
              :label="t('ai.messageNavigator.label')"
              :pending-label="t('ai.messageNavigator.pending')"
              @select="scrollToConversationMessage"
            />
          </div>
          <AiPermissionCard
            v-if="assistant.pendingPermission"
            :title="t('ai.permissionTitle')"
            :description="pendingPermissionTitle()"
            :cancel-label="t('common.cancel')"
            :options="assistant.pendingPermission.options"
            @decide="answerPermission"
          />
          <AiPendingEdits :project-root="pendingEditsProjectRoot" />
          <AiQueuedMessages
            :queue="assistant.activeQueue"
            :editing-id="editingQueueId"
            :labels="{
              title: t('ai.queue.title'),
              badge: t('ai.queue.badge'),
              edit: t('ai.queue.edit'),
              delete: t('ai.queue.delete'),
              sendNow: t('ai.queue.sendNow'),
              attachmentsOnly: (count: number) => t('ai.queue.attachmentsOnly', { count }),
              copyImage: t('ai.image.copy'),
              copiedImage: t('ai.image.copied'),
              copyImageFailed: t('ai.image.copyFailed', { message: '{message}' }),
            }"
            @edit="editQueuedMessage"
            @delete="deleteQueuedMessage"
            @send-now="sendQueuedMessageNow"
          />
          <AiPromptComposer
            v-if="!assistant.workspaceSwitching && !activeParallelDelegationPending"
            v-model="input"
            v-model:active-suggestion-index="activeToolIndex"
            v-model:selected-model="selectedModel"
            v-model:selected-reasoning="selectedReasoningEffort"
                                                :attachments="attachments"
            :attachment-labels="attachmentChipLabels"
            :history="promptHistory"
            :suggestions="composerSuggestions"
            :suggestions-visible="toolSuggestionsVisible || mentionSuggestionsActive"
            :loading="assistant.loading"
            :starting="assistant.agentStarting"
            :adding-attachments="addingAttachments"
            :optimizing-prompt="promptOptimizationLoading"
            :models="availableModels"
            :reasoning-options="localizedReasoningEffortOptions"
            :model-selection-supported="modelSelectionSupported"
            :agent-config-options="activeAcpConfigOptions"
            :agent-config-pending="acpConfigPending"
            :labels="composerLabels"
            :accept="AI_ATTACHMENT_ACCEPT"
            :context-usage="contextUsage"
            :editing-queue="!!editingQueueId || !!editingMessageId"
            :update-label="editingMessageId ? t('ai.message.resend') : t('ai.queue.update')"
            :cancel-edit-label="editingMessageId ? t('ai.message.cancelEdit') : t('ai.queue.cancelEdit')"
            @add-files="addAttachmentFiles"
            @remove-attachment="removeAttachment"
            @preview-attachment="previewAttachment = $event"
            @select-suggestion="insertToolSuggestion"
            @dismiss-suggestions="dismissComposerSuggestions"
            @optimize-prompt="startPromptOptimization"
            @cancel-optimization="cancelPromptOptimization"
            @send="send"
            @cancel="cancelCurrent"
            @paste="handleAttachmentPaste"
            @update-queue="editingMessageId ? resendEditedMessage() : updateQueuedMessageFromComposer()"
            @cancel-edit="editingMessageId ? cancelMessageEdit() : cancelQueueEdit()"
            @change-agent-config="applyAcpConfigOption($event.configId, $event.value)"
          >
            <template #delegation-control>
              <el-tooltip
                v-if="isDesktopRuntime() && assistant.mode === 'agentic' && !assistant.activeConversation?.external"
                :content="t('ai.delegation.dispatch')"
                placement="top"
                :teleported="false"
              >
                <el-button text :icon="Share" :aria-label="t('ai.delegation.dispatch')" @click="dispatchConversationTask" />
              </el-tooltip>
            </template>
            <template #approval-control>
              <el-dropdown
                v-if="assistant.mode === 'agentic' && !browserLocalAi"
                trigger="click"
                placement="top-start"
                :teleported="false"
                @command="selectApprovalMode"
              >
                <el-button text class="ai-approval-policy-bar__trigger">
                  <el-icon aria-hidden="true"><Lock /></el-icon>
                  <span>{{ currentApprovalModeLabel }}</span>
                </el-button>
                <template #dropdown>
                  <el-dropdown-menu class="ai-approval-policy-menu">
                    <el-dropdown-item
                      v-for="option in approvalModeOptions"
                      :key="option.value"
                      :command="option.value"
                      :class="{ 'is-active': option.value === aiApprovalMode }"
                    >
                      <span class="ai-approval-policy-menu__check" aria-hidden="true">
                        {{ option.value === aiApprovalMode ? "✓" : "" }}
                      </span>
                      <span class="ai-approval-policy-menu__content">
                        <strong>{{ option.label }}</strong>
                        <small>{{ option.description }}</small>
                      </span>
                    </el-dropdown-item>
                  </el-dropdown-menu>
                </template>
              </el-dropdown>
            </template>
          </AiPromptComposer>
          </main>
        </template>
        <template #second>
          <AiConversationSidebar
            v-if="conversationSidebarOpen"
            v-model:scope="conversationScope"
            v-model:status="conversationStatus"
            v-model:sort="conversationSort"
            v-model:workspace-filter="conversationWorkspaceFilter"
            v-model:search="conversationSearch"
            :groups="groupedConversations"
            :message-matches="messageMatches"
            :active-conversation-id="assistant.activeConversationId"
            :status-options="localizedStatusOptions"
            :labels="conversationSidebarLabels"
            :global-mode="conversationPreferences.globalMode"
            :workspace-group-orderable="groupConversationsByWorkspace"
            :workspace-options="conversationWorkspaceOptions"
            :top-workspace-id="workbench.activeWorkspaceId"
            :workspace-name="conversationWorkspaceName"
            :workspace-available="conversationWorkspaceAvailable"
            @select="switchConversation"
            @open-message="openMessageMatch"
            @export="exportConversation"
            @archive="toggleConversationArchive"
                                                @pin="toggleConversationPinned"
            @read-state="setConversationReadState"
            @rename="renameConversation"
            @remove="deleteConversation"
            @clear-messages="clearConversationMessages"
            @switch-workspace="switchTopWorkspaceForConversation"
            @relink-workspace="relinkConversationToTopWorkspace"
            @reorder-workspace-groups="reorderConversationWorkspaceGroups"
            @notes="openConversationNotes"
          />
        </template>
      </AppSplitPane>
      <el-dialog
        v-model="promptOptimizationVisible"
        :title="t('ai.promptOptimizer.title')"
        width="min(760px, 92vw)"
        append-to-body
        destroy-on-close
        :close-on-click-modal="!promptOptimizationLoading"
        @closed="closePromptOptimization"
      >
        <div class="ai-prompt-optimizer">
          <section>
            <strong>{{ t("ai.promptOptimizer.original") }}</strong>
            <el-input
              :model-value="promptOptimizationOriginal"
              type="textarea"
              :autosize="{ minRows: 4, maxRows: 10 }"
              readonly
            />
          </section>
          <section>
            <strong>{{ t("ai.promptOptimizer.optimized") }}</strong>
            <el-skeleton v-if="promptOptimizationLoading" :rows="4" animated />
            <el-input
              v-else
              v-model="promptOptimizationResult"
              type="textarea"
              :autosize="{ minRows: 6, maxRows: 14 }"
            />
          </section>
        </div>
        <template #footer>
          <el-button @click="closePromptOptimization">{{ t("common.cancel") }}</el-button>
          <el-button
            v-if="promptOptimizationLoading"
            type="warning"
            @click="cancelPromptOptimization"
          >
            {{ t("ai.promptOptimizer.cancel") }}
          </el-button>
          <el-button
            v-else
            type="primary"
            :disabled="!promptOptimizationResult.trim()"
            @click="applyOptimizedPrompt"
          >
            {{ t("ai.promptOptimizer.apply") }}
          </el-button>
        </template>
      </el-dialog>
      <AiPluginContextDialog
        v-if="pluginContextDialog"
        :key="pluginContextDialog.provider.key"
        :provider="pluginContextDialog.provider"
        :remaining="pluginContextDialog.remaining"
        @insert="insertPluginContext"
        @close="pluginContextDialog = undefined"
      />
      <el-dialog
        v-model="previewAttachment"
        class="ai-attachment-preview-dialog"
        width="min(92vw, 960px)"
        align-center
        append-to-body
      >
        <template #header><strong>{{ previewAttachment?.name }}</strong></template>
        <div class="ai-attachment-preview-dialog__body">
          <img
            v-if="previewAttachment"
            :src="getAttachmentImageSrc(previewAttachment)"
            :alt="previewAttachment.name"
          />
        </div>
      </el-dialog>
        </aside>
      </template>
    </AppSplitPane>
  </Transition>
</template>
